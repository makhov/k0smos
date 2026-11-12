package metadata

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Network is a NoCloud drive's network-config: how each interface is set up.
// It is how a seed ISO built with cloud-localds -N, or its equivalent, hands
// a VM a static address.
type Network struct {
	Interfaces []Interface
	DNS        []string
}

// Interface is one ethernets entry. Name is the key, which is the kernel's
// name when MAC is empty; with a MAC, the interface is found by its address.
type Interface struct {
	Name    string
	MAC     string
	DHCP    bool
	Address string // IPv4 CIDR
	Gateway string
	MTU     int
	// LinkRoutes are destinations reached directly on this link, with no
	// gateway: what makes a gateway outside the address's own prefix -- a /32
	// address, as some providers hand out -- reachable at all.
	LinkRoutes []string
}

type netplanV2 struct {
	Version   int                     `json:"version"`
	Ethernets map[string]netplanEther `json:"ethernets"`
	Bonds     map[string]any          `json:"bonds"`
	Vlans     map[string]any          `json:"vlans"`
	Bridges   map[string]any          `json:"bridges"`
	Config    any                     `json:"config"` // version 1's list
}

type netplanEther struct {
	Match struct {
		MAC string `json:"macaddress"`
	} `json:"match"`
	DHCP4       bool     `json:"dhcp4"`
	DHCP6       bool     `json:"dhcp6"`
	Addresses   []string `json:"addresses"`
	Gateway4    string   `json:"gateway4"`
	MTU         int      `json:"mtu"`
	Nameservers struct {
		Addresses []string `json:"addresses"`
	} `json:"nameservers"`
	Routes []struct {
		To     string `json:"to"`
		Via    string `json:"via"`
		Scope  string `json:"scope"`
		OnLink bool   `json:"on-link"`
		// Not applied. Read only to say so, unless they restate what is
		// already true: a from that is the interface's own address, an mtu
		// that is the link's.
		From   string `json:"from"`
		MTU    int    `json:"mtu"`
		Table  int    `json:"table"`
		Metric int    `json:"metric"`
	} `json:"routes"`
}

// ParseNetworkConfig reads the netplan-style version 2 subset VMs are commonly
// given: ethernets matched by name or MAC, with DHCP or one
// static IPv4 address, a default route, link-scoped routes, nameservers and an
// MTU. Everything else is warned about by name rather than dropped silently,
// since a missing route is a node that boots and never answers.
//
// The document may or may not be wrapped in a top-level "network:" key; cloud
// init accepts both, and both are in use.
func ParseNetworkConfig(b []byte) (Network, []string, error) {
	var out Network
	var warn []string
	if strings.TrimSpace(string(b)) == "" {
		return out, nil, nil
	}
	var wrapped struct {
		Network *netplanV2 `json:"network"`
	}
	if err := yaml.Unmarshal(b, &wrapped); err != nil {
		return out, nil, fmt.Errorf("parse network-config: %w", err)
	}
	doc := wrapped.Network
	if doc == nil {
		doc = &netplanV2{}
		if err := yaml.Unmarshal(b, doc); err != nil {
			return out, nil, fmt.Errorf("parse network-config: %w", err)
		}
	}
	if doc.Version != 2 {
		return out, []string{fmt.Sprintf("network-config version %d is not supported, only 2; ignored", doc.Version)}, nil
	}
	for kind, m := range map[string]map[string]any{"bonds": doc.Bonds, "vlans": doc.Vlans, "bridges": doc.Bridges} {
		if len(m) > 0 {
			warn = append(warn, fmt.Sprintf("network-config %s are not supported, ignored", kind))
		}
	}

	names := make([]string, 0, len(doc.Ethernets))
	for name := range doc.Ethernets {
		names = append(names, name)
	}
	sort.Strings(names) // a map has no order; the console should
	seen := map[string]bool{}
	for _, name := range names {
		e := doc.Ethernets[name]
		ifc := Interface{Name: name, MAC: strings.ToLower(e.Match.MAC), DHCP: e.DHCP4, MTU: e.MTU, Gateway: e.Gateway4}
		if e.DHCP6 {
			warn = append(warn, fmt.Sprintf("network-config %s: dhcp6 is not supported, ignored", name))
		}
		for _, a := range e.Addresses {
			p, err := netip.ParsePrefix(a)
			switch {
			case err != nil:
				warn = append(warn, fmt.Sprintf("network-config %s: address %q is not a CIDR, ignored", name, a))
			case !p.Addr().Is4():
				warn = append(warn, fmt.Sprintf("network-config %s: IPv6 address %s is not supported, ignored", name, a))
			case ifc.Address != "":
				warn = append(warn, fmt.Sprintf("network-config %s: only one address per interface is supported, %s ignored", name, a))
			default:
				ifc.Address = p.String()
			}
		}
		for _, r := range e.Routes {
			if r.From != "" && !strings.HasPrefix(ifc.Address, r.From+"/") {
				warn = append(warn, fmt.Sprintf("network-config %s: route to %s: from %s is not supported, ignored", name, r.To, r.From))
			}
			if r.MTU != 0 && r.MTU != e.MTU {
				warn = append(warn, fmt.Sprintf("network-config %s: route to %s: mtu %d is not supported, ignored", name, r.To, r.MTU))
			}
			if r.Table != 0 || r.Metric != 0 {
				warn = append(warn, fmt.Sprintf("network-config %s: route to %s: table and metric are not supported, ignored", name, r.To))
			}
			switch {
			case (r.To == "default" || r.To == "0.0.0.0/0") && r.Via != "":
				ifc.Gateway = r.Via
				if r.OnLink {
					ifc.LinkRoutes = append(ifc.LinkRoutes, r.Via+"/32")
				}
			case r.Via == "" && r.Scope == "link":
				ifc.LinkRoutes = append(ifc.LinkRoutes, r.To)
			default:
				warn = append(warn, fmt.Sprintf("network-config %s: route to %s via %q is not supported, ignored", name, r.To, r.Via))
			}
		}
		if !ifc.DHCP && ifc.Address == "" {
			warn = append(warn, fmt.Sprintf("network-config %s: neither dhcp4 nor an IPv4 address, ignored", name))
			continue
		}
		for _, d := range e.Nameservers.Addresses {
			if !seen[d] {
				seen[d] = true
				out.DNS = append(out.DNS, d)
			}
		}
		out.Interfaces = append(out.Interfaces, ifc)
	}
	return out, warn, nil
}
