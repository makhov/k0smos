package config

import "strings"

// Config holds k0smos knobs parsed from the kernel cmdline (k0smos.* keys).
type Config struct {
	Hostname string
	// Exec is the supervised child: argv[0] followed by its arguments.
	Exec []string
	// Modules are the kernel modules to load. Nil means "use the built-in
	// default set"; empty (via k0smos.modules=none) disables module loading.
	Modules []string

	// Iface, IP, Gateway and DNS statically configure the primary NIC. IP
	// empty means leave networking alone (loopback only). The kernel's own ip=
	// autoconfiguration cannot be used because it runs before /init, i.e.
	// before the virtio_net module is loaded.
	Iface   string
	IP      string // CIDR, e.g. 10.0.2.15/24
	Gateway string
	DNS     string

	// Root, if set, is a block device holding the real root filesystem to
	// switch_root into (e.g. /dev/vda). Empty means stay on the initramfs,
	// which is fine for an init-only smoke test but not for running kubelet.
	Root       string
	RootFSType string
	RootFlags  string

	// Data selects the mutable data volume, mounted at DataDir. This follows
	// Talos's split: an interchangeable root plus a separate volume holding
	// everything that changes, so a machine can be disposable without being
	// diskless. "auto" finds a volume labelled DataLabel or formats the single
	// blank device; a path or LABEL=/UUID= names one explicitly; empty disables it.
	Data       string
	DataLabel  string
	DataFSType string
	DataDir    string

	// Path is the PATH exported to child processes. PID1 inherits no
	// environment from anyone, so without this k0s and kubelet cannot find the
	// iptables binaries k0s stages into /var/lib/k0s/bin, and kubelet reports
	// "No iptables support on this system".
	Path string
}

// defaultExec is the workload k0smos exists to run.
var defaultExec = []string{"/usr/local/bin/k0s", "controller", "--single"}

// defaultPath includes /var/lib/k0s/bin because that is where k0s stages the
// binaries it embeds (containerd, runc, kubelet, iptables) at runtime.
const defaultPath = "/var/lib/k0s/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin"

// Parse extracts k0smos.* parameters from a kernel cmdline string.
func Parse(cmdline string) Config {
	c := Config{
		Hostname:   "k0smos",
		Exec:       defaultExec,
		Iface:      "eth0",
		RootFSType: "ext4",
		DataLabel:  "k0smos-data",
		DataFSType: "ext4",
		DataDir:    "/var/lib/k0s",
		Path:       defaultPath,
	}
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
		case "path":
			if v != "" {
				c.Path = v
			}
		case "root":
			c.Root = v
		case "rootfstype":
			if v != "" {
				c.RootFSType = v
			}
		case "rootflags":
			c.RootFlags = v
		case "data":
			c.Data = v
		case "datalabel":
			if v != "" {
				c.DataLabel = v
			}
		case "datafstype":
			if v != "" {
				c.DataFSType = v
			}
		case "datadir":
			if v != "" {
				c.DataDir = v
			}
		case "iface":
			if v != "" {
				c.Iface = v
			}
		case "ip":
			c.IP = v
		case "gw":
			c.Gateway = v
		case "dns":
			c.DNS = v
		case "modules":
			// "none" disables loading entirely; otherwise a comma-separated
			// list replaces the default set.
			if v == "none" {
				c.Modules = []string{}
			} else if parts := strings.Split(v, ","); parts[0] != "" {
				c.Modules = parts
			}
		}
	}
	return c
}
