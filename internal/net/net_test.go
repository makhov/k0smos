package net

import (
	"net"
	"testing"
)

type fakeLinker struct {
	up     []string
	addrs  []string
	routes []string
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
