package main

import (
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
