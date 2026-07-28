package config

import "strings"

// Config holds k0smos knobs parsed from the kernel cmdline (k0smos.* keys).
type Config struct {
	Hostname string
}

// Parse extracts k0smos.* parameters from a kernel cmdline string.
func Parse(cmdline string) Config {
	c := Config{Hostname: "k0smos"}
	for _, tok := range strings.Fields(cmdline) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || !strings.HasPrefix(k, "k0smos.") {
			continue
		}
		switch strings.TrimPrefix(k, "k0smos.") {
		case "hostname":
			c.Hostname = v
		}
	}
	return c
}
