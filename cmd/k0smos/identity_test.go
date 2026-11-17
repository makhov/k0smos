package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two machines must not derive the same identity, and one machine must derive
// the same identity every time. That is the whole contract.
func TestIdentityIsStableAndDistinct(t *testing.T) {
	a := identityFrom([2]string{"dmi", "4c4c4544-0051-3010-8056-b4c04f564d32"})
	again := identityFrom([2]string{"dmi", "4c4c4544-0051-3010-8056-b4c04f564d32\n"})
	b := identityFrom([2]string{"dmi", "4c4c4544-0051-3010-8056-b4c04f564d33"})
	if a == "" || a != again {
		t.Errorf("same machine derived %q then %q", a, again)
	}
	if a == b {
		t.Error("two machines derived the same identity")
	}
	// A UUID and a MAC that happen to read alike are still different machines.
	if identityFrom([2]string{"dmi", "x"}) == identityFrom([2]string{"mac", "x"}) {
		t.Error("the source must be part of what is hashed")
	}
}

// The sources are tried in order, and anything a machine reports as "nothing"
// is passed over rather than hashed into a value every such machine shares.
func TestIdentitySkipsSourcesThatSayNothing(t *testing.T) {
	want := identityFrom([2]string{"mac", "52:54:00:12:34:56"})
	for _, empty := range []string{"", "   ", "00000000-0000-0000-0000-000000000000", "00:00:00:00:00:00"} {
		got := identityFrom([2]string{"dmi", empty}, [2]string{"mac", "52:54:00:12:34:56"})
		if got != want {
			t.Errorf("dmi %q: got %q, want the MAC's identity", empty, got)
		}
	}
	if identityFrom([2]string{"dmi", ""}, [2]string{"mac", ""}) != "" {
		t.Error("a machine offering nothing must derive nothing, not a shared value")
	}
}

// A node's name has to be a DNS label and has to differ between machines; a
// machine that offers no identity keeps the old name rather than an odd one.
func TestHostnameAndMachineID(t *testing.T) {
	id := identityFrom([2]string{"mac", "52:54:00:12:34:56"})
	h := hostnameFor(id)
	if len(h) > 63 || h == "k0smos" {
		t.Errorf("hostname = %q", h)
	}
	if hostnameFor(identityFrom([2]string{"mac", "52:54:00:12:34:57"})) == h {
		t.Error("two machines got the same hostname")
	}
	if hostnameFor("") != "k0smos" || machineIDFor("") != "" {
		t.Error("a machine with no identity must fall back, not invent one")
	}
	if got := machineIDFor(id); len(got) != 33 || got[32] != '\n' {
		t.Errorf("machine-id = %q, want 32 hex digits and a newline", got)
	}
}

// A real card is preferred over whatever else is in /sys/class/net, and
// loopback is never the answer.
func TestFirstMACPrefersRealHardware(t *testing.T) {
	dir := t.TempDir()
	write := func(name, addr string, real bool) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "address"), []byte(addr+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if real {
			if err := os.MkdirAll(filepath.Join(dir, name, "device"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("lo", "00:00:00:00:00:00", false)
	write("dummy0", "aa:bb:cc:dd:ee:ff", false)
	write("eth0", "52:54:00:12:34:56", true)
	if got := firstMAC(dir); got != "52:54:00:12:34:56\n" {
		t.Errorf("firstMAC = %q, want eth0's address", got)
	}
	if got := firstMAC(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("firstMAC on a missing directory = %q", got)
	}
}

// The contract the hybrid exists for: an identity is taken once and then kept,
// so a node keeps its name across reboots.
func TestIdentityIsTakenOnceAndKept(t *testing.T) {
	dir := t.TempDir()
	first := machineIdentity(dir, os.MkdirAll, os.WriteFile)
	if first.Source != "new" || len(first.Value) != 64 {
		t.Fatalf("first boot: %+v", first)
	}
	again := machineIdentity(dir, os.MkdirAll, os.WriteFile)
	if again.Value != first.Value || again.Source != "kept" {
		t.Errorf("second boot got %+v, want the kept %q", again, first.Value)
	}
	// And two machines take different ones.
	other := machineIdentity(t.TempDir(), os.MkdirAll, os.WriteFile)
	if other.Value == first.Value {
		t.Error("two machines took the same identity")
	}
}

// A file holding something that is not an identity is replaced rather than
// obeyed: whatever it is, it cannot name this node.
func TestIdentityReplacesRubbish(t *testing.T) {
	for _, junk := range []string{"", "not hex at all", "abcd", strings.Repeat("a", 63)} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "lib/k0smos"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, identityFile), []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		got := machineIdentity(dir, os.MkdirAll, os.WriteFile)
		if got.Source != "new" || len(got.Value) != 64 {
			t.Errorf("%q: got %+v, want a new identity", junk, got)
		}
	}
}

// Nowhere to keep one: the hardware answers instead, and it must answer the
// same way every boot rather than randomly, or the node renames itself each
// time and leaves a stale Node object behind.
func TestIdentityFallsBackToHardwareDeterministically(t *testing.T) {
	fail := func(string, os.FileMode) error { return os.ErrPermission }
	nowrite := func(string, []byte, os.FileMode) error { return os.ErrPermission }
	a := machineIdentity(t.TempDir(), fail, nowrite)
	b := machineIdentity(t.TempDir(), fail, nowrite)
	if a.Value != b.Value {
		t.Errorf("fallback is not deterministic: %q then %q", a.Value, b.Value)
	}
	if a.Value != "" && !strings.Contains(a.Source, "hardware") {
		t.Errorf("source = %q, want it to say where the identity came from", a.Source)
	}
}
