package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

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
		kernel      string
		initramfs   string
		disk        string
		cidata      string
		data        string
		dataSize    string
		socket      string
		mem         string
		cpus        string
		arch        string
		console     string
		exec_       string
		apiPort     int
		attach      bool
		interactive bool
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:   "boot",
		Short: "Boot a k0smos node locally under QEMU",
		Long: `Boots a node with direct kernel boot: the initramfs comes up as PID1, then
switch_roots onto the ext4 root.

This is the same thing image/run-qemu.sh does, without needing the repository or a
shell — so a downloaded kernel, initramfs and root image are enough.

The guest runs in the background and the command returns: there is no shell on a
k0smos node, so there is nothing to sit in front of. The console is written to a
file, port 6443 is forwarded, and a control port is attached so
"k0smosctl kubeconfig" and "k0smosctl shutdown" can reach it.

Use --attach to stay and watch the console instead, where ctrl-c then shuts the
guest down cleanly rather than killing it.`,
		Example: `  # artifacts built locally, or unpacked from a release
  k0smosctl boot

  # with configuration, and a data volume for /var/lib/k0s
  k0smosctl boot --cidata cidata.iso --data data.img

  # watch the console; ctrl-c stops the guest cleanly
  k0smosctl boot --attach

  # a second guest alongside the first
  k0smosctl boot --disk second.img --socket second.sock --api-port 7443

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

			spec := bootSpec{
				kernel: kernel, initramfs: initramfs, disk: disk, cidata: cidata,
				data: data, dataSize: dataSize, socket: socket,
				mem: mem, cpus: cpus, console: console, exec: exec_,
				apiPort: apiPort, attach: attach, interactive: interactive,
			}
			// A detached guest writes its console somewhere findable rather than
			// into a terminal that is about to be given back.
			if !attach && !interactive && spec.console == "" {
				spec.console = filepath.Join("dist", "console.log")
			}
			// Refuse to start alongside a guest already answering on this socket.
			// Two guests sharing one root image corrupt it, and the second would
			// also fail to forward the API port.
			if !dryRun && socket != "" && guestIsLive(socket) {
				return fatalf("a guest is already running on %s — give this one its own "+
					"--socket, --api-port and --disk, or stop that one first", socket)
			}
			args, err := qemuArgs(g, spec)
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), g.qemu+" "+strings.Join(args, " "))
				return nil
			}
			q := exec.Command(g.qemu, args...)
			if interactive || attach {
				hint := "ctrl-c shuts the guest down cleanly"
				if interactive {
					hint = "escape with ctrl-a x — but that kills the guest, so prefer " +
						"`k0smosctl shutdown` from another terminal"
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "booting %s; %s\n", g.qemu, hint)
				q.Stdin, q.Stdout, q.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
				return runGuest(q, spec, cmd.ErrOrStderr())
			}
			return detach(q, spec, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&kernel, "kernel", "", "kernel image (default dist/kernel/<arch>/vmlinuz)")
	f.StringVar(&initramfs, "initramfs", filepath.Join("dist", "k0smos-initramfs.gz"), "initramfs image")
	f.StringVar(&disk, "disk", filepath.Join("dist", "k0smos.img"), `ext4 root to switch onto; "" stays on the initramfs`)
	f.StringVar(&cidata, "cidata", "", "cloud-init drive to attach, as written by 'k0smosctl gen'")
	f.StringVar(&data, "data", "", "data volume for /var/lib/k0s; created blank if absent")
	f.StringVar(&dataSize, "data-size", "4G", "size for a newly created --data volume")
	f.StringVar(&socket, "socket", defaultSocket, "where to put the control socket")
	f.StringVar(&mem, "memory", "4096", "guest memory in MiB")
	f.StringVar(&cpus, "cpus", "2", "guest CPUs")
	f.StringVar(&arch, "arch", runtime.GOARCH, "guest architecture: amd64 or arm64")
	f.StringVar(&console, "console", "",
		"where to write the console (default dist/console.log; with --attach it also streams to stdout)")
	f.StringVar(&exec_, "exec", "", "override the supervised workload (comma-separated)")
	f.IntVar(&apiPort, "api-port", 6443, "host port forwarded to the API server; 0 forwards nothing")
	f.BoolVar(&attach, "attach", false,
		"stay in the foreground streaming the console; ctrl-c then shuts the guest down cleanly")
	f.BoolVar(&interactive, "interactive", false,
		"hand QEMU the terminal (implies --attach; escape is ctrl-a x). The guest has no shell, so this is rarely useful")
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
	apiPort                         int
	attach, interactive             bool
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

	netdev := "user,id=n0"
	if s.apiPort != 0 {
		netdev += fmt.Sprintf(",hostfwd=tcp::%d-:6443", s.apiPort)
	}
	args = append(args, "-netdev", netdev, "-device", "virtio-net-pci,netdev=n0")

	consoleArgs, err := consoleArgs(s)
	if err != nil {
		return nil, err
	}
	return append(args, consoleArgs...), nil
}

// consoleArgs wires up the guest console.
//
// The default is deliberately *not* QEMU's interactive console. That puts the
// terminal in raw mode, which swallows ctrl-c — it becomes a byte sent to the
// guest rather than a signal — so the only way out is QEMU's own ctrl-a x escape.
// Since a k0smos guest has no shell, there is nothing to type at it anyway: the
// console is output, and giving up the terminal buys nothing.
//
// With signal=on the host keeps ctrl-c, which boot turns into a clean shutdown.
func consoleArgs(s bootSpec) ([]string, error) {
	// Detached, the console cannot go to a terminal that will not be there, so it
	// always goes to a file. Enforced here rather than trusting the caller to have
	// filled in a default: an empty path yields "-serial file:", which QEMU
	// accepts and then writes nowhere useful.
	if !s.attach && !s.interactive {
		if s.console == "" {
			return nil, errors.New("a detached guest needs somewhere to write its console")
		}
		return []string{"-display", "none", "-serial", "file:" + s.console}, nil
	}
	if s.interactive {
		if s.console != "" {
			return nil, errors.New("--interactive and --console cannot be combined; " +
				"QEMU can only own the terminal or write to a file, not both")
		}
		// mon:stdio multiplexes the QEMU monitor onto the same terminal, which is
		// what makes ctrl-a x available to escape.
		return []string{"-nographic", "-serial", "mon:stdio"}, nil
	}
	if s.console != "" {
		if err := os.MkdirAll(filepath.Dir(s.console), 0755); err != nil {
			return nil, err
		}
		// Both at once: a file to keep and stdout to watch.
		return []string{"-display", "none",
			"-chardev", "stdio,id=con,signal=on,logfile=" + s.console,
			"-serial", "chardev:con"}, nil
	}
	return []string{"-display", "none",
		"-chardev", "stdio,id=con,signal=on",
		"-serial", "chardev:con"}, nil
}

// guestIsLive reports whether something is already answering on a control socket.
func guestIsLive(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
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

// shutdownGrace is how long the guest gets to stop cleanly after an interrupt.
// Generous: it has to unmount, and a hard kill is what corrupts the root.
const shutdownGrace = 60 * time.Second

// runGuest runs QEMU and turns an interrupt into a clean guest shutdown.
//
// Killing QEMU leaves the ext4 root with an unreplayed journal, which loses recent
// writes and makes the image read as empty afterwards — so ctrl-c must not simply
// take the process down. Instead it asks the guest to stop the way `k0smosctl
// shutdown` does, and waits.
//
// This only works because the console is not in QEMU's raw mode: see consoleArgs.
// Under --interactive the terminal belongs to QEMU and ctrl-c never arrives here.
func runGuest(q *exec.Cmd, s bootSpec, out io.Writer) error {
	if err := q.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- q.Wait() }()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	select {
	case err := <-done:
		return err
	case <-sig:
	}

	if s.socket == "" {
		// No control port to ask through, so there is nothing gentler available.
		fmt.Fprintln(out, "\ninterrupted, and no control port to stop the guest through — killing QEMU")
		_ = q.Process.Kill()
		<-done
		return errors.New("guest killed; its filesystem may need a repair pass")
	}

	fmt.Fprintf(out, "\nstopping the guest cleanly (up to %s; interrupt again to kill it)\n", shutdownGrace)
	if conn, err := net.DialTimeout("unix", s.socket, 5*time.Second); err == nil {
		fmt.Fprintf(conn, "%s\n", "poweroff")
		conn.Close()
	} else {
		fmt.Fprintf(out, "warn: could not reach %s: %v\n", s.socket, err)
	}

	select {
	case err := <-done:
		fmt.Fprintln(out, "guest stopped")
		return err
	case <-sig:
		fmt.Fprintln(out, "killing QEMU; the filesystem may need a repair pass")
		_ = q.Process.Kill()
		<-done
		return errors.New("guest killed before it finished shutting down")
	case <-time.After(shutdownGrace):
		fmt.Fprintf(out, "guest did not stop within %s; killing QEMU\n", shutdownGrace)
		_ = q.Process.Kill()
		<-done
		return errors.New("guest did not shut down in time")
	}
}

// detach starts the guest in its own session and returns, leaving it running.
//
// Its own session matters: without it the guest sits in this terminal's process
// group, so a later ctrl-c there — or closing the terminal — would deliver SIGINT
// or SIGHUP straight to QEMU and kill the machine, taking the filesystem with it.
func detach(q *exec.Cmd, s bootSpec, out io.Writer) error {
	log, err := os.Create(s.console)
	if err != nil {
		return err
	}
	defer log.Close()
	q.Stdout, q.Stderr, q.Stdin = log, log, nil
	q.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := q.Start(); err != nil {
		return err
	}
	// Read the pid before releasing: Release invalidates it, which is how the
	// first version came to report "pid -1".
	pid := q.Process.Pid

	// Do not report success until the guest is actually up. QEMU exits
	// immediately for several ordinary mistakes — a root image another guest
	// already holds a write lock on, a port already bound — and reporting a pid
	// for a process that is already gone sends the user to look for a guest that
	// does not exist.
	if err := waitUntilUp(q, s.socket, startupGrace); err != nil {
		return fmt.Errorf("%w\n%s", err, lastLines(s.console, 5))
	}
	// Release only once it is known to be running: this process is about to exit,
	// and waiting for the guest would defeat the point of detaching.
	if err := q.Process.Release(); err != nil {
		return err
	}

	fmt.Fprintf(out, "guest running in the background (pid %d)\n", pid)
	fmt.Fprintf(out, "  console:  tail -f %s\n", s.console)
	if s.apiPort != 0 {
		fmt.Fprintf(out, "  cluster:  k0smosctl kubeconfig -o kubeconfig   (API on :%d)\n", s.apiPort)
	}
	stop := "k0smosctl shutdown"
	if s.socket != defaultSocket {
		stop += " --socket " + s.socket
	}
	fmt.Fprintf(out, "  stop it:  %s\n", stop)
	return nil
}

// startupGrace is how long the guest gets to create its control socket before it
// is assumed not to be coming up. QEMU makes it almost at once; this only has to
// outlast process startup.
const startupGrace = 10 * time.Second

// waitUntilUp returns once the control socket exists, or an error if QEMU exits
// first. Waiting for the socket also means a `kubeconfig` or `shutdown` typed
// straight afterwards finds something to talk to.
func waitUntilUp(q *exec.Cmd, socket string, timeout time.Duration) error {
	exited := make(chan error, 1)
	go func() { exited <- q.Wait() }()

	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("qemu exited immediately: %w", err)
			}
			return errors.New("qemu exited immediately")
		default:
		}
		if socket == "" || fileExists(socket) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no control socket at %s after %s", socket, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// lastLines returns the tail of a file, so a failure to start can show what QEMU
// said rather than making the user go and find the log.
func lastLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
