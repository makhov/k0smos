package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

func gate(pid int) error {
	if pid != 1 {
		return fmt.Errorf("k0smos is an init (PID1), not a CLI; got pid %d", pid)
	}
	return nil
}

// readCmdline returns the kernel cmdline, or "" if it cannot be read. It must
// be called after /proc is mounted. An unreadable cmdline is not fatal —
// config.Parse falls back to defaults.
func readCmdline(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: read %s: %v\n", path, err)
		return ""
	}
	return strings.TrimSpace(string(data))
}

// pump forwards signal arrivals to a coalescing trigger channel. Several
// signals collapsing into one trigger is correct: the consumer drains until
// there is no work left.
func pump(sigs <-chan os.Signal, trigger chan<- struct{}) {
	for range sigs {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
}

func main() {
	if err := gate(os.Getpid()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := run(context.Background()); err != nil {
		panic(err) // PID1: surface on console; kernel handles the rest
	}
}
