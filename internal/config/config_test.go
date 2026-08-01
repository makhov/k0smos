package config

import (
	"slices"
	"strings"
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

// PID1 inherits no environment, so a usable PATH must be synthesised — and it
// has to include where k0s stages its embedded binaries.
func TestParseDefaultPathIncludesK0sStagingDir(t *testing.T) {
	c := Parse("console=ttyAMA0")
	if !strings.Contains(c.Path, "/var/lib/k0s/bin") {
		t.Errorf("path = %q, want it to include /var/lib/k0s/bin", c.Path)
	}
}

func TestParsePathOverride(t *testing.T) {
	if c := Parse("k0smos.path=/opt/bin"); c.Path != "/opt/bin" {
		t.Errorf("path = %q, want /opt/bin", c.Path)
	}
}

func TestParseRootKnobs(t *testing.T) {
	c := Parse("k0smos.root=/dev/vda k0smos.rootfstype=xfs k0smos.rootflags=noatime")
	if c.Root != "/dev/vda" || c.RootFSType != "xfs" || c.RootFlags != "noatime" {
		t.Errorf("root=%q fstype=%q flags=%q", c.Root, c.RootFSType, c.RootFlags)
	}
}

func TestParseRootDefaultsToInitramfsAndExt4(t *testing.T) {
	c := Parse("console=ttyAMA0")
	if c.Root != "" {
		t.Errorf("root = %q, want empty (stay on initramfs)", c.Root)
	}
	if c.RootFSType != "ext4" {
		t.Errorf("rootfstype = %q, want ext4", c.RootFSType)
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

func TestNICsBareAddressConfiguresTheNamedInterface(t *testing.T) {
	for _, addr := range []string{"dhcp", "10.0.2.15/24"} {
		c := Parse("k0smos.iface=enp0s1 k0smos.gw=10.0.2.2 k0smos.ip=" + addr)
		want := []NIC{{Name: "enp0s1", Addr: addr, Gateway: "10.0.2.2"}}
		if got := c.NICs(); !slices.Equal(got, want) {
			t.Errorf("NICs() for ip=%s = %+v, want %+v", addr, got, want)
		}
	}
}

func TestNICsNoAddressLeavesNetworkingAlone(t *testing.T) {
	if got := Parse("root=/dev/vda").NICs(); got != nil {
		t.Errorf("NICs() = %+v, want nil", got)
	}
}

// The shape a multi-homed node uses: a management NIC on DHCP and a cluster NIC
// with a static address on a segment that has no router.
func TestNICsPerInterfaceList(t *testing.T) {
	c := Parse("k0smos.ip=eth0:dhcp,eth1:10.10.0.11/24 k0smos.gw=10.0.2.2")
	want := []NIC{
		{Name: "eth0", Addr: "dhcp", Gateway: "10.0.2.2"},
		{Name: "eth1", Addr: "10.10.0.11/24"},
	}
	if got := c.NICs(); !slices.Equal(got, want) {
		t.Errorf("NICs() = %+v, want %+v", got, want)
	}
}

// One default route, so the gateway follows k0smos.iface rather than being
// applied to every static NIC — which would install conflicting routes.
func TestNICsGatewayFollowsTheNamedInterface(t *testing.T) {
	c := Parse("k0smos.iface=eth1 k0smos.gw=10.10.0.1 k0smos.ip=eth0:10.0.2.15/24,eth1:10.10.0.11/24")
	got := c.NICs()
	if len(got) != 2 {
		t.Fatalf("NICs() = %+v, want 2", got)
	}
	if got[0].Gateway != "" {
		t.Errorf("eth0 gateway = %q, want none", got[0].Gateway)
	}
	if got[1].Gateway != "10.10.0.1" {
		t.Errorf("eth1 gateway = %q, want 10.10.0.1", got[1].Gateway)
	}
}

func TestNICsSkipsMalformedEntriesWithoutLosingTheRest(t *testing.T) {
	c := Parse("k0smos.ip=eth0:dhcp,garbage,:10.0.0.1/24,eth1:10.10.0.11/24")
	want := []NIC{{Name: "eth0", Addr: "dhcp"}, {Name: "eth1", Addr: "10.10.0.11/24"}}
	if got := c.NICs(); !slices.Equal(got, want) {
		t.Errorf("NICs() = %+v, want %+v", got, want)
	}
}

// An IPv6 CIDR is full of colons and must not be read as interface:address.
// Nothing configures IPv6 yet; this only keeps it from being misparsed.
func TestNICsDoesNotMistakeIPv6ForAnInterfaceName(t *testing.T) {
	c := Parse("k0smos.ip=fd00::5/64")
	want := []NIC{{Name: "eth0", Addr: "fd00::5/64"}}
	if got := c.NICs(); !slices.Equal(got, want) {
		t.Errorf("NICs() = %+v, want %+v", got, want)
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
