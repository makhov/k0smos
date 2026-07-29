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
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse address %q: %w", cidr, err)
	}
	if ip.To4() == nil {
		return fmt.Errorf("address %q is not IPv4", cidr)
	}
	if err := c.SetAddr(iface, ip.To4(), ipNet.Mask); err != nil {
		return fmt.Errorf("set %s address %s: %w", iface, cidr, err)
	}
	if err := c.LinkUp(iface); err != nil {
		return fmt.Errorf("%s up: %w", iface, err)
	}
	if gw == "" {
		return nil
	}
	// After LinkUp: the kernel rejects a route whose gateway is not reachable
	// through an interface that is already up.
	gwIP := net.ParseIP(gw)
	if gwIP == nil || gwIP.To4() == nil {
		return fmt.Errorf("parse gateway %q: not an IPv4 address", gw)
	}
	if err := c.AddDefaultRoute(gwIP.To4()); err != nil {
		return fmt.Errorf("add default route via %s: %w", gw, err)
	}
	return nil
}
