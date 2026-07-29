package config

import "strings"

// Config holds k0smos knobs parsed from the kernel cmdline (k0smos.* keys).
type Config struct {
	Hostname string
	// Exec is the supervised child: argv[0] followed by its arguments.
	Exec []string
}

// defaultExec is the workload k0smos exists to run.
var defaultExec = []string{"/usr/local/bin/k0s", "controller", "--single"}

// Parse extracts k0smos.* parameters from a kernel cmdline string.
func Parse(cmdline string) Config {
	c := Config{Hostname: "k0smos", Exec: defaultExec}
	for _, tok := range strings.Fields(cmdline) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || !strings.HasPrefix(k, "k0smos.") {
			continue
		}
		switch strings.TrimPrefix(k, "k0smos.") {
		case "hostname":
			if v != "" {
				c.Hostname = v
			}
		case "exec":
			// Comma-separated: a kernel cmdline parameter value cannot contain
			// spaces. Lets a boot be smoke-tested without the real k0s binary.
			if parts := strings.Split(v, ","); parts[0] != "" {
				c.Exec = parts
			}
		}
	}
	return c
}
