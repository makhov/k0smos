package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// Guest networking values. QEMU's user-mode stack always hands out the same
// address and gateway, so these are correct for every run rather than a guess.
//
// Static rather than ip=dhcp: the kernel's own IP autoconfiguration is a
// late_initcall, so it runs before /init and cannot see a virtio NIC whose driver
// k0smos loads as a module.
const (
	guestCIDR    = "10.0.2.15/24"
	guestGateway = "10.0.2.2"
	// Not slirp's resolver at 10.0.2.3: on a macOS host it accepts queries and
	// never answers them, so every image pull ends in an i/o timeout. A public
	// resolver is NATed out normally.
	guestDNS = "1.1.1.1"
)

func bootCmd() *cobra.Command {
	var (
		kernel    string
		initramfs string
		disk      string
		cidata    string
		data      string
		dataSize  string
		socket    string
		mem       string
		cpus      string
		arch      string
		console   string
		exec_     string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "boot",
		Short: "Boot a k0smos node locally under QEMU",
		Long: `Boots a node with direct kernel boot: the initramfs comes up as PID1, then
switch_roots onto the ext4 root.

This is the same thing image/run-qemu.sh does, without needing the repository or a
shell — so a downloaded kernel, initramfs and root image are enough.

Port 6443 is forwarded to the host, and a control port is attached so
"k0smosctl kubeconfig" and "k0smosctl shutdown" can reach the guest.`,
		Example: `  # artifacts built locally, or unpacked from a release
  k0smosctl boot

  # with configuration, and a data volume for /var/lib/k0s
  k0smosctl boot --cidata cidata.iso --data data.img

  # headless, logging the console to a file
  k0smosctl boot --console console.log

  # print the qemu command instead of running it
  k0smosctl boot --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g, err := guestFor(arch)
			if err != nil {
				return err
			}
			if kernel == "" {
				kernel = filepath.Join("dist", "kernel", g.apkArch, "vmlinuz")
			}

			for what, path := range map[string]string{"kernel": kernel, "initramfs": initramfs} {
				if _, err := os.Stat(path); err != nil {
					return fatalf("%s %s not found — build it with `make %s`, or point at one with --%s",
						what, path, what, what)
				}
			}
			if _, err := exec.LookPath(g.qemu); err != nil {
				return fatalf("%s is not installed", g.qemu)
			}

			args, err := qemuArgs(g, bootSpec{
				kernel: kernel, initramfs: initramfs, disk: disk, cidata: cidata,
				data: data, dataSize: dataSize, socket: socket,
				mem: mem, cpus: cpus, console: console, exec: exec_,
			})
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), g.qemu+" "+strings.Join(args, " "))
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "booting %s (control port %s)\n", g.qemu, socket)
			q := exec.Command(g.qemu, args...)
			q.Stdin, q.Stdout, q.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
			return q.Run()
		},
	}
	f := cmd.Flags()
	f.StringVar(&kernel, "kernel", "", "kernel image (default dist/kernel/<arch>/vmlinuz)")
	f.StringVar(&initramfs, "initramfs", filepath.Join("dist", "k0smos-initramfs.gz"), "initramfs image")
	f.StringVar(&disk, "disk", filepath.Join("dist", "k0smos.img"), `ext4 root to switch onto; "" stays on the initramfs`)
	f.StringVar(&cidata, "cidata", "", "cloud-init drive to attach, as written by `k0smosctl gen`")
	f.StringVar(&data, "data", "", "data volume for /var/lib/k0s; created blank if absent")
	f.StringVar(&dataSize, "data-size", "4G", "size for a newly created --data volume")
	f.StringVar(&socket, "socket", defaultSocket, "where to put the control socket")
	f.StringVar(&mem, "memory", "4096", "guest memory in MiB")
	f.StringVar(&cpus, "cpus", "2", "guest CPUs")
	f.StringVar(&arch, "arch", runtime.GOARCH, "guest architecture: amd64 or arm64")
	f.StringVar(&console, "console", "", `file to log the console to; "" is interactive`)
	f.StringVar(&exec_, "exec", "", "override the supervised workload (comma-separated)")
	f.BoolVar(&dryRun, "dry-run", false, "print the qemu command instead of running it")
	return cmd
}

// guest holds the per-architecture QEMU details.
type guest struct {
	qemu    string
	machine string
	console string
	apkArch string
	accel   []string
}

func guestFor(arch string) (guest, error) {
	switch arch {
	case "arm64", "aarch64":
		return guest{
			qemu: "qemu-system-aarch64", machine: "virt",
			console: "ttyAMA0", apkArch: "aarch64", accel: accelFor("arm64"),
		}, nil
	case "amd64", "x86_64":
		return guest{
			qemu: "qemu-system-x86_64", machine: "q35",
			console: "ttyS0", apkArch: "x86_64", accel: accelFor("amd64"),
		}, nil
	default:
		return guest{}, fmt.Errorf("unsupported architecture %q", arch)
	}
}

// accelFor picks hardware virtualisation when the guest matches the host, and
// falls back to emulation otherwise. Emulation works and is slow enough to be
// worth saying so.
func accelFor(guestArch string) []string {
	if guestArch != runtime.GOARCH {
		return []string{"-accel", "tcg"}
	}
	if runtime.GOOS == "darwin" {
		return []string{"-accel", "hvf", "-cpu", "host"}
	}
	// Writable /dev/kvm is the actual requirement, not merely its existence.
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
		return []string{"-accel", "kvm", "-cpu", "host"}
	}
	return []string{"-accel", "tcg"}
}

type bootSpec struct {
	kernel, initramfs, disk, cidata string
	data, dataSize, socket          string
	mem, cpus, console, exec        string
}

// qemuArgs assembles the command line. Separated from running it so --dry-run can
// show exactly what would happen, and so it is testable without QEMU.
func qemuArgs(g guest, s bootSpec) ([]string, error) {
	append_ := fmt.Sprintf("console=%s panic=10 k0smos.ip=%s k0smos.gw=%s k0smos.dns=%s",
		g.console, guestCIDR, guestGateway, guestDNS)

	args := []string{"-M", g.machine}
	args = append(args, g.accel...)
	args = append(args, "-m", s.mem, "-smp", s.cpus, "-kernel", s.kernel, "-initrd", s.initramfs)

	// Read-only, as an infrastructure provider attaches it.
	if s.cidata != "" {
		if _, err := os.Stat(s.cidata); err != nil {
			return nil, fatalf("cloud-init drive %s not found", s.cidata)
		}
		args = append(args, "-drive", "file="+s.cidata+",if=virtio,format=raw,readonly=on")
	}
	if s.data != "" {
		if err := ensureDataVolume(s.data, s.dataSize); err != nil {
			return nil, err
		}
		args = append(args, "-drive", "file="+s.data+",if=virtio,format=raw")
	}
	if s.disk != "" {
		if _, err := os.Stat(s.disk); err != nil {
			return nil, fatalf("root image %s not found — build it with `make disk`, "+
				"or pass --disk '' to stay on the initramfs (kubelet cannot run there)", s.disk)
		}
		args = append(args, "-drive", "file="+s.disk+",if=virtio,format=raw")
		// By label rather than /dev/vda: attaching a cloud-init drive shifts the
		// device names, and on real hardware enumeration is not stable at all.
		append_ += " k0smos.root=LABEL=k0smos k0smos.rootfstype=ext4"
	}
	if s.exec != "" {
		append_ += " k0smos.exec=" + s.exec
	}
	args = append(args, "-append", append_)

	if s.socket != "" {
		if err := os.MkdirAll(filepath.Dir(s.socket), 0755); err != nil {
			return nil, err
		}
		// A stale socket file makes QEMU fail to bind.
		if err := os.Remove(s.socket); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		args = append(args,
			"-chardev", "socket,path="+s.socket+",server=on,wait=off,id=k0smosctl",
			"-device", "virtio-serial-pci",
			"-device", "virtserialport,chardev=k0smosctl,name=k0smos.control")
	}

	args = append(args, "-netdev", "user,id=n0,hostfwd=tcp::6443-:6443",
		"-device", "virtio-net-pci,netdev=n0")

	if s.console == "" {
		args = append(args, "-nographic", "-serial", "mon:stdio")
	} else {
		if err := os.MkdirAll(filepath.Dir(s.console), 0755); err != nil {
			return nil, err
		}
		args = append(args, "-display", "none", "-serial", "file:"+s.console)
	}
	return args, nil
}

// ensureDataVolume creates a sparse file for the data volume if it is absent.
// k0smos formats it on first boot and reuses it afterwards.
func ensureDataVolume(path, size string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	n, err := parseSize(size)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Truncate rather than write: the file is sparse, so this costs no space
	// until the guest formats it.
	return f.Truncate(n)
}

// parseSize accepts plain bytes or a K/M/G suffix.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, errors.New("empty size")
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K':
		mult, s = 1<<10, s[:len(s)-1]
	case 'M':
		mult, s = 1<<20, s[:len(s)-1]
	case 'G':
		mult, s = 1<<30, s[:len(s)-1]
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return n * mult, nil
}
