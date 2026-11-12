package net

import (
	"net"
	"slices"
	"strconv"
	"testing"
)

type fakeLinker struct {
	up     []string
	addrs  []string
	routes []string
	order  []string // MTU and routes, in the order they were set
}

func (f *fakeLinker) LinkUp(name string) error {
	f.up = append(f.up, name)
	return nil
}

func (f *fakeLinker) SetAddr(iface string, ip net.IP, mask net.IPMask) error {
	f.addrs = append(f.addrs, iface+"="+ip.String()+"/"+net.IP(mask).String())
	return nil
}

func (f *fakeLinker) AddDefaultRoute(gw net.IP) error {
	f.routes = append(f.routes, gw.String())
	f.order = append(f.order, "default via "+gw.String())
	return nil
}

func (f *fakeLinker) SetMTU(iface string, mtu int) error {
	f.order = append(f.order, iface+" mtu "+strconv.Itoa(mtu))
	return nil
}

func (f *fakeLinker) AddLinkRoute(iface string, dst net.IP, mask net.IPMask) error {
	ones, _ := mask.Size()
	f.order = append(f.order, dst.String()+"/"+strconv.Itoa(ones)+" dev "+iface)
	return nil
}

func TestUpBringsLoopbackUp(t *testing.T) {
	f := &fakeLinker{}
	if err := Up(f); err != nil {
		t.Fatal(err)
	}
	if len(f.up) != 1 || f.up[0] != "lo" {
		t.Errorf("brought up %v, want [lo]", f.up)
	}
}

func TestConfigureSetsAddressBringsLinkUpThenRoutes(t *testing.T) {
	f := &fakeLinker{}
	if err := Configure(f, "eth0", "10.0.2.15/24", "10.0.2.2"); err != nil {
		t.Fatal(err)
	}
	if len(f.addrs) != 1 || f.addrs[0] != "eth0=10.0.2.15/255.255.255.0" {
		t.Errorf("addrs = %v", f.addrs)
	}
	if len(f.up) != 1 || f.up[0] != "eth0" {
		t.Errorf("up = %v, want [eth0]", f.up)
	}
	// The gateway is only reachable once the interface is up, so the route must
	// be added after LinkUp.
	if len(f.routes) != 1 || f.routes[0] != "10.0.2.2" {
		t.Errorf("routes = %v", f.routes)
	}
}

func TestConfigureWithoutGatewaySkipsRoute(t *testing.T) {
	f := &fakeLinker{}
	if err := Configure(f, "eth0", "10.0.2.15/24", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.routes) != 0 {
		t.Errorf("routes = %v, want none", f.routes)
	}
	if len(f.up) != 1 {
		t.Errorf("up = %v, want [eth0]", f.up)
	}
}

func TestConfigureRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ name, cidr, gw string }{
		{"not a cidr", "10.0.2.15", "10.0.2.2"},
		{"garbage cidr", "nope", ""},
		{"bad gateway", "10.0.2.15/24", "nope"},
		{"ipv6 unsupported", "fd00::1/64", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Configure(&fakeLinker{}, "eth0", tc.cidr, tc.gw); err == nil {
				t.Errorf("Configure(%q, %q) = nil, want error", tc.cidr, tc.gw)
			}
		})
	}
}

// A /32 address, as some providers hand out: the gateway is outside the prefix
// and reachable only through a route on the link, which must exist before the
// default route or the kernel refuses that.
func TestApplyAddsLinkRoutesBeforeTheDefaultRoute(t *testing.T) {
	f := &fakeLinker{}
	err := Apply(f, Static{Iface: "eth0", CIDR: "203.0.113.17/32", Gateway: "203.0.113.1", MTU: 9100,
		LinkRoutes: []string{"203.0.113.1/32"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"eth0 mtu 9100", "203.0.113.1/32 dev eth0", "default via 203.0.113.1"}
	if !slices.Equal(f.order, want) {
		t.Errorf("order = %v, want %v", f.order, want)
	}
	if len(f.addrs) != 1 || f.addrs[0] != "eth0=203.0.113.17/255.255.255.255" {
		t.Errorf("addrs = %v", f.addrs)
	}
}
