package net

import "fmt"

// Linker is the subset of *sys.Sys that network setup needs.
type Linker interface {
	LinkUp(name string) error
}

// Up brings the loopback interface up. The primary NIC is configured by the
// kernel `ip=` cmdline parameter at boot in the MVP.
func Up(l Linker) error {
	if err := l.LinkUp("lo"); err != nil {
		return fmt.Errorf("lo up: %w", err)
	}
	return nil
}
