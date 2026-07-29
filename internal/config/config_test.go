package config

import (
	"slices"
	"testing"
)

func TestParseReadsHostnameAndDefaults(t *testing.T) {
	c := Parse("root=/dev/vda ip=dhcp k0smos.hostname=node1 quiet")
	if c.Hostname != "node1" {
		t.Errorf("hostname = %q, want node1", c.Hostname)
	}
}

func TestParseDefaultsHostname(t *testing.T) {
	c := Parse("root=/dev/vda")
	if c.Hostname != "k0smos" {
		t.Errorf("hostname = %q, want default k0smos", c.Hostname)
	}
}

func TestParseDefaultsExecToK0sController(t *testing.T) {
	c := Parse("root=/dev/vda")
	want := []string{"/usr/local/bin/k0s", "controller", "--single"}
	if !slices.Equal(c.Exec, want) {
		t.Errorf("exec = %v, want %v", c.Exec, want)
	}
}

// The cmdline cannot carry spaces inside a value, so the supervised command is
// comma-separated. This exists so a boot can be smoke-tested without k0s.
func TestParseExecOverride(t *testing.T) {
	c := Parse("k0smos.exec=/bin/true,--flag,arg")
	want := []string{"/bin/true", "--flag", "arg"}
	if !slices.Equal(c.Exec, want) {
		t.Errorf("exec = %v, want %v", c.Exec, want)
	}
}

func TestParseNetworkingKnobs(t *testing.T) {
	c := Parse("k0smos.ip=10.0.2.15/24 k0smos.gw=10.0.2.2 k0smos.dns=10.0.2.3 k0smos.iface=enp0s1")
	if c.IP != "10.0.2.15/24" || c.Gateway != "10.0.2.2" || c.DNS != "10.0.2.3" {
		t.Errorf("ip=%q gw=%q dns=%q", c.IP, c.Gateway, c.DNS)
	}
	if c.Iface != "enp0s1" {
		t.Errorf("iface = %q, want enp0s1", c.Iface)
	}
}

func TestParseDefaultsIfaceAndNoStaticIP(t *testing.T) {
	c := Parse("root=/dev/vda")
	if c.Iface != "eth0" {
		t.Errorf("iface = %q, want eth0", c.Iface)
	}
	if c.IP != "" {
		t.Errorf("ip = %q, want empty (leave networking alone)", c.IP)
	}
}

func TestParseModulesDefaultsToNilMeaningBuiltInSet(t *testing.T) {
	if c := Parse("root=/dev/vda"); c.Modules != nil {
		t.Errorf("modules = %v, want nil (use default set)", c.Modules)
	}
}

func TestParseModulesOverride(t *testing.T) {
	c := Parse("k0smos.modules=virtio_net,ext4")
	if !slices.Equal(c.Modules, []string{"virtio_net", "ext4"}) {
		t.Errorf("modules = %v", c.Modules)
	}
}

// "none" must be distinguishable from "unset", so it needs to be non-nil.
func TestParseModulesNoneDisablesLoading(t *testing.T) {
	c := Parse("k0smos.modules=none")
	if c.Modules == nil || len(c.Modules) != 0 {
		t.Errorf("modules = %v, want empty non-nil slice", c.Modules)
	}
}

func TestParseExecEmptyValueKeepsDefault(t *testing.T) {
	c := Parse("k0smos.exec=")
	if len(c.Exec) == 0 || c.Exec[0] != "/usr/local/bin/k0s" {
		t.Errorf("exec = %v, want default", c.Exec)
	}
}
