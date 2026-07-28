package config

import "testing"

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
