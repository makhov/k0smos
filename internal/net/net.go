package net

import (
	"fmt"
	"net"
)

// Linker is the subset of *sys.Sys that bringing a link up needs.
type Linker interface {
	LinkUp(name string) error
}

// Configurer is the subset of *sys.Sys needed to give an interface an address
// and a default route.
type Configurer interface {
	Linker
	SetAddr(iface string, ip net.IP, mask net.IPMask) error
	AddDefaultRoute(gw net.IP) error
	SetMTU(iface string, mtu int) error
	AddLinkRoute(iface string, dst net.IP, mask net.IPMask) error
}

// Static is one interface's static configuration.
type Static struct {
	Iface, CIDR, Gateway string
	MTU                  int
	// LinkRoutes are CIDRs reached directly on the link, with no gateway.
	LinkRoutes []string
}

// Up brings the loopback interface up.
func Up(l Linker) error {
	if err := l.LinkUp("lo"); err != nil {
		return fmt.Errorf("lo up: %w", err)
	}
	return nil
}

// Configure assigns a static IPv4 address to iface and optionally installs a
// default route via gw. gw may be empty to skip the route.
//
// Static rather than DHCP because the kernel's own ip= autoconfiguration runs
// before /init and so cannot see a NIC whose driver k0smos loads as a module.
// Running a DHCP client would mean shipping a second binary; a VM's address is
// known from its network setup, so the kernel cmdline carries it instead.
func Configure(c Configurer, iface, cidr, gw string) error {
	return Apply(c, Static{Iface: iface, CIDR: cidr, Gateway: gw})
}

// Apply configures one interface: MTU, address, link up, the routes that need
// no gateway, then the default route -- in that order, because the kernel
// rejects a route whose gateway it cannot yet reach.
func Apply(c Configurer, s Static) error {
	ip, ipNet, err := net.ParseCIDR(s.CIDR)
	if err != nil {
		return fmt.Errorf("parse address %q: %w", s.CIDR, err)
	}
	if ip.To4() == nil {
		return fmt.Errorf("address %q is not IPv4", s.CIDR)
	}
	if s.MTU > 0 {
		if err := c.SetMTU(s.Iface, s.MTU); err != nil {
			return fmt.Errorf("set %s mtu %d: %w", s.Iface, s.MTU, err)
		}
	}
	if err := c.SetAddr(s.Iface, ip.To4(), ipNet.Mask); err != nil {
		return fmt.Errorf("set %s address %s: %w", s.Iface, s.CIDR, err)
	}
	if err := c.LinkUp(s.Iface); err != nil {
		return fmt.Errorf("%s up: %w", s.Iface, err)
	}
	for _, r := range s.LinkRoutes {
		dst, dstNet, err := net.ParseCIDR(r)
		if err != nil || dst.To4() == nil {
			return fmt.Errorf("parse link route %q: not an IPv4 CIDR", r)
		}
		if err := c.AddLinkRoute(s.Iface, dstNet.IP.To4(), dstNet.Mask); err != nil {
			return fmt.Errorf("add route to %s on %s: %w", r, s.Iface, err)
		}
	}
	if s.Gateway == "" {
		return nil
	}
	// After LinkUp: the kernel rejects a route whose gateway is not reachable
	// through an interface that is already up.
	gwIP := net.ParseIP(s.Gateway)
	if gwIP == nil || gwIP.To4() == nil {
		return fmt.Errorf("parse gateway %q: not an IPv4 address", s.Gateway)
	}
	if err := c.AddDefaultRoute(gwIP.To4()); err != nil {
		return fmt.Errorf("add default route via %s: %w", s.Gateway, err)
	}
	return nil
}
