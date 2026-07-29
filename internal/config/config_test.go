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

func TestParseExecEmptyValueKeepsDefault(t *testing.T) {
	c := Parse("k0smos.exec=")
	if len(c.Exec) == 0 || c.Exec[0] != "/usr/local/bin/k0s" {
		t.Errorf("exec = %v, want default", c.Exec)
	}
}
