package metadata

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The common shapes for a VM: wrapped in "network:", one ethernets entry keyed
// eth0. Static, a /32 whose gateway is only reachable through a link-scoped
// route, and DHCP -- with the per-route from and mtu keys netplan allows.
func TestParseNetworkConfigCommonShapes(t *testing.T) {
	cases := []struct {
		name, doc string
		want      Network
	}{
		{"static", `network:
  version: 2
  ethernets:
    eth0:
      mtu: 1500
      addresses:
        - 192.0.2.10/24
      routes:
        - to: default
          via: 192.0.2.1
          mtu: 1500
      nameservers:
        addresses:
          - 192.0.2.53
          - 1.1.1.1
`, Network{
			Interfaces: []Interface{{Name: "eth0", Address: "192.0.2.10/24", Gateway: "192.0.2.1", MTU: 1500}},
			DNS:        []string{"192.0.2.53", "1.1.1.1"},
		}},
		{"/32 with an on-link gateway", `network:
  version: 2
  ethernets:
    eth0:
      mtu: 9100
      addresses:
        - 203.0.113.17/32
      routes:
        - to: default
          via: 203.0.113.1
          from: 203.0.113.17
          mtu: 9100
        - to: 203.0.113.1/32
          scope: link
      nameservers:
        addresses:
          - 1.1.1.1
`, Network{
			Interfaces: []Interface{{
				Name: "eth0", Address: "203.0.113.17/32", Gateway: "203.0.113.1", MTU: 9100,
				LinkRoutes: []string{"203.0.113.1/32"},
			}},
			DNS: []string{"1.1.1.1"},
		}},
		{"dhcp", `network:
  version: 2
  ethernets:
    eth0:
      critical: true
      dhcp4: true
`, Network{Interfaces: []Interface{{Name: "eth0", DHCP: true}}}},
	}
	for _, c := range cases {
		got, warn, err := ParseNetworkConfig([]byte(c.doc))
		if err != nil || len(warn) != 0 {
			t.Errorf("%s: err %v, warnings %v", c.name, err, warn)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

// cloud-localds -N passes the file through as written: unwrapped, matched by MAC,
// with gateway4 or on-link spelled the netplan way.
func TestParseNetworkConfigUnwrappedMatchedByMAC(t *testing.T) {
	got, warn, err := ParseNetworkConfig([]byte(`version: 2
ethernets:
  uplink:
    match: {macaddress: "52:54:00:12:34:56"}
    set-name: eth0
    addresses: [10.0.0.5/24]
    routes: [{to: 0.0.0.0/0, via: 10.0.0.1, on-link: true}]
`))
	if err != nil || len(warn) != 0 {
		t.Fatalf("err %v, warnings %v", err, warn)
	}
	want := []Interface{{Name: "uplink", MAC: "52:54:00:12:34:56", Address: "10.0.0.5/24", Gateway: "10.0.0.1", LinkRoutes: []string{"10.0.0.1/32"}}}
	if !reflect.DeepEqual(got.Interfaces, want) {
		t.Errorf("got %+v, want %+v", got.Interfaces, want)
	}
}

// What is not supported is named on the console. A route nobody added is a node
// that boots and never answers, and nothing else would say why.
func TestParseNetworkConfigWarnsAboutWhatItCannotDo(t *testing.T) {
	got, warn, err := ParseNetworkConfig([]byte(`network:
  version: 2
  ethernets:
    eth0:
      dhcp6: true
      addresses: [10.0.0.5/24, 10.0.0.6/24, "fd00::5/64"]
      routes:
        - {to: 192.168.0.0/16, via: 10.0.0.254}
    eth1: {}
  bonds:
    bond0: {interfaces: [eth0]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Interfaces) != 1 || got.Interfaces[0].Address != "10.0.0.5/24" {
		t.Errorf("interfaces = %+v, want eth0 with its first address", got.Interfaces)
	}
	for _, w := range []string{"bonds", "dhcp6", "only one address", "IPv6", "192.168.0.0/16", "eth1: neither"} {
		if !slices.ContainsFunc(warn, func(s string) bool { return strings.Contains(s, w) }) {
			t.Errorf("no warning mentioning %q in %q", w, warn)
		}
	}

	if _, warn, _ := ParseNetworkConfig([]byte("version: 1\nconfig: []\n")); len(warn) != 1 || !strings.Contains(warn[0], "version 1") {
		t.Errorf("version 1: warnings %q", warn)
	}
}

// Read from the drive alongside user-data, and a broken one costs only itself.
func TestLoadReadsNetworkConfigAndSurvivesABrokenOne(t *testing.T) {
	_, md, err := Load(mapFiles{"meta-data": "instance-id: i\n", "network-config": "version: 2\nethernets: {eth0: {dhcp4: true}}\n"})
	if err != nil || len(md.Network.Interfaces) != 1 || !md.Network.Interfaces[0].DHCP {
		t.Errorf("Load = %+v, %v", md.Network, err)
	}
	ud, md, err := Load(mapFiles{"user-data": "#cloud-config\nwrite_files: [{path: /etc/x, content: y}]\n", "network-config": "version: [2\n"})
	if err != nil || len(ud.WriteFiles) != 1 || len(md.Network.Interfaces) != 0 {
		t.Errorf("a broken network-config cost more than itself: %v, %+v", err, ud)
	}
	if !slices.ContainsFunc(ud.Warnings, func(s string) bool { return strings.Contains(s, "network-config") }) {
		t.Errorf("no warning about the broken network-config: %q", ud.Warnings)
	}
}
