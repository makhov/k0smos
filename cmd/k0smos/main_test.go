package main

import (
	"maps"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestGateRejectsNonPID1(t *testing.T) {
	if err := gate(4242); err == nil {
		t.Fatal("expected error when pid != 1, got nil")
	}
}

func TestGateAcceptsPID1(t *testing.T) {
	if err := gate(1); err != nil {
		t.Fatalf("expected nil for pid 1, got %v", err)
	}
}

func TestReadCmdlineTrimsTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmdline")
	if err := os.WriteFile(path, []byte("root=/dev/vda k0smos.hostname=node1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := readCmdline(path), "root=/dev/vda k0smos.hostname=node1"; got != want {
		t.Errorf("readCmdline = %q, want %q", got, want)
	}
}

func TestReadCmdlineMissingFileIsNotFatal(t *testing.T) {
	if got := readCmdline(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Errorf("readCmdline = %q, want empty", got)
	}
}

func TestPumpCoalescesSignalsAndExitsOnClose(t *testing.T) {
	sigs := make(chan os.Signal, 4)
	trigger := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { pump(sigs, trigger); close(done) }()

	// More signals than trigger capacity must not block the pump.
	for range 4 {
		sigs <- syscall.SIGCHLD
	}
	select {
	case <-trigger:
	case <-time.After(time.Second):
		t.Fatal("pump did not deliver a trigger")
	}

	close(sigs)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pump did not exit after sigs closed")
	}
}

// The os-release the image scripts write: the wanted values sit among keys
// that must be ignored, and PRETTY_NAME is quoted and full of spaces.
func TestOSReleaseIDs(t *testing.T) {
	version, build := osReleaseIDs("NAME=k0smos\nID=k0smos\nVERSION_ID=v1.37.2+k0s.1\nBUILD_ID=ff7354c2e5f1\nK0SMOS_BASE=ubuntu@sha256:0bc48e0\nPRETTY_NAME=\"k0smos (k0s v1.37.2+k0s.1)\"\n")
	if version != "v1.37.2+k0s.1" {
		t.Errorf("version = %q", version)
	}
	if build != "ff7354c2e5f1" {
		t.Errorf("build = %q", build)
	}
}

// A custom initramfs may carry no os-release at all. That machine reports the
// kernel it can see rather than refusing to report anything.
func TestOSReleaseIDsWithoutAFileIsEmpty(t *testing.T) {
	if version, build := osReleaseIDs(""); version != "" || build != "" {
		t.Errorf("version = %q, build = %q, want both empty", version, build)
	}
}

func TestSysctlDefaultsYieldToCmdline(t *testing.T) {
	got := sysctlDefaults("console=ttyS0 sysctl.fs.protected_regular=0 sysctl.kernel/kexec_load_disabled=0 kvm_amd.nested=1")
	// Whatever hostSysctls holds, less the two the command line set -- one
	// written with dots and one with slashes, both of which the kernel takes.
	// Derived rather than restated, so a setting added to the table does not
	// fail a test that is about the yielding, not about the table.
	want := maps.Clone(hostSysctls)
	delete(want, "fs.protected_regular")
	delete(want, "kernel.kexec_load_disabled")
	if !maps.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if len(sysctlDefaults("")) != len(hostSysctls) {
		t.Error("an empty command line must keep every default")
	}
}
