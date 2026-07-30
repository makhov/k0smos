//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/amakhov/k0smos/internal/blkid"
	"github.com/amakhov/k0smos/internal/cgroup"
	"github.com/amakhov/k0smos/internal/config"
	"github.com/amakhov/k0smos/internal/control"
	"github.com/amakhov/k0smos/internal/dhcp"
	"github.com/amakhov/k0smos/internal/module"
	"github.com/amakhov/k0smos/internal/mount"
	knet "github.com/amakhov/k0smos/internal/net"
	"github.com/amakhov/k0smos/internal/power"
	"github.com/amakhov/k0smos/internal/reaper"
	"github.com/amakhov/k0smos/internal/shutdown"
	"github.com/amakhov/k0smos/internal/supervise"
	"github.com/amakhov/k0smos/internal/switchroot"
	"github.com/amakhov/k0smos/internal/sys"

	"golang.org/x/sys/unix"
)

const (
	cmdlinePath = "/proc/cmdline"

	// newRootDir is where the real root filesystem is mounted before the switch.
	newRootDir = "/newroot"
	// initPath is k0smos's own location on the real root, re-executed after the
	// switch. It must match where mkrootfs.sh installs the binary.
	initPath = "/sbin/k0smos"
	// switchedFlag marks the post-switch invocation, so it does not switch again.
	switchedFlag = "--switched-root"

	// virtioPortsDir is where the kernel names virtio-serial ports, and
	// controlPortName is the port k0smos takes shutdown commands from. The host
	// side is set up by image/run-qemu.sh.
	virtioPortsDir  = "/sys/class/virtio-ports"
	controlPortName = "k0smos.control"
	// inputDevDir holds the evdev nodes the ACPI power button reports through.
	inputDevDir = "/dev/input"
	// controlRetry is how long to wait before reopening an idle control port.
	controlRetry = time.Second

	// How long to keep looking for the root device before giving up.
	rootProbeAttempts = 30
	rootProbeInterval = 500 * time.Millisecond

	// childGrace is how long the k0s child gets to exit after ctx cancellation
	// before we start detaching filesystems under it.
	childGrace = 5 * time.Second
)

// logf writes a progress line to the console. As PID1 there is nowhere else to
// report to, and a silent init is undebuggable when a boot goes wrong.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "k0smos: "+format+"\n", args...)
}

// shutdownAdapter lets *sys.Sys satisfy shutdown.Shutdowner, whose Mounts()
// returns []string so that package stays free of any internal/sys import.
type shutdownAdapter struct{ *sys.Sys }

func (a shutdownAdapter) Mounts() ([]string, error) { return a.Sys.MountTargets() }

func run(ctx context.Context) error {
	return boot(ctx, sys.New(), slices.Contains(os.Args, switchedFlag))
}

// findControlPort locates the virtio-serial port named controlPortName and
// returns its device path, or "" if the host did not attach one.
//
// virtio-serial ports are identified by name through sysfs: the directory
// /sys/class/virtio-ports/vportNpM holds a "name" file, and the device is
// /dev/<that directory's basename>.
func findControlPort(name string) string {
	entries, err := os.ReadDir(virtioPortsDir)
	if err != nil {
		return "" // no virtio-serial bus at all
	}
	for _, e := range entries {
		got, err := os.ReadFile(filepath.Join(virtioPortsDir, e.Name(), "name"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(got)) == name {
			return "/dev/" + e.Name()
		}
	}
	return ""
}

// watchControlPort returns a channel of host shutdown requests, or nil when no
// control port is present.
//
// The port is reopened on EOF: with no host client attached it reads EOF
// immediately, so watching it once would stop listening before anyone could
// send anything.
func watchControlPort(ctx context.Context) <-chan control.Command {
	dev := findControlPort(controlPortName)
	if dev == "" {
		logf("no control port; shutdown only via SIGTERM/SIGINT")
		return nil
	}
	logf("listening for shutdown commands on %s", dev)
	return control.WatchReopen(ctx, func() (io.ReadCloser, error) {
		return os.Open(dev)
	}, controlRetry)
}

// configureDHCP brings the interface up, acquires a lease, applies it, and keeps
// renewing in the background. It returns the lease's first DNS server, if any.
//
// The link must be up before DHCP runs: with the interface down the kernel has
// nothing to send through, whereas static configuration can set an address
// first and bring the link up afterwards.
func configureDHCP(ctx context.Context, s *sys.Sys, cfg config.Config) (string, error) {
	if err := s.LinkUp(cfg.Iface); err != nil {
		return "", fmt.Errorf("link up: %w", err)
	}
	mac, err := s.InterfaceMAC(cfg.Iface)
	if err != nil {
		return "", fmt.Errorf("read MAC: %w", err)
	}
	conn, err := s.DHCPConn(cfg.Iface)
	if err != nil {
		return "", fmt.Errorf("open socket: %w", err)
	}

	client := &dhcp.Client{Conn: conn, MAC: mac, Hostname: cfg.Hostname}
	lease, err := client.Acquire(ctx)
	if err != nil {
		conn.Close()
		return "", err
	}

	apply := func(l dhcp.Lease) error {
		gw := ""
		if l.Router != nil {
			gw = l.Router.String()
		}
		if err := knet.Configure(s, cfg.Iface, l.CIDR(), gw); err != nil {
			return err
		}
		logf("%s configured %s gw %s (lease %s)", cfg.Iface, l.CIDR(), gw, l.LeaseTime)
		return nil
	}
	if err := apply(lease); err != nil {
		conn.Close()
		return "", err
	}

	go func() {
		defer conn.Close()
		// Renew returns only when the context ends or the lease is
		// unrecoverable; either way the node keeps running with what it has.
		if err := client.Renew(ctx, lease, apply); err != nil && ctx.Err() == nil {
			logf("warn: dhcp renewal stopped: %v", err)
		}
	}()

	if len(lease.DNS) > 0 {
		return lease.DNS[0].String(), nil
	}
	return "", nil
}

// watchPowerButton returns a channel that fires when the hardware power button
// is pressed, or nil if there are no input devices to watch.
//
// Every /dev/input/event* is watched because which one carries the power key
// depends on the platform, and without udev there are no by-path symlinks to
// pick from. Non-power events are filtered out by internal/power.
func watchPowerButton(ctx context.Context) <-chan struct{} {
	entries, err := os.ReadDir(inputDevDir)
	if err != nil {
		return nil // no evdev: no ACPI button, or the module is not loaded
	}
	merged := make(chan struct{})
	watched := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "event") {
			continue
		}
		f, err := os.Open(filepath.Join(inputDevDir, e.Name()))
		if err != nil {
			continue
		}
		watched++
		go func() {
			for range power.Watch(ctx, f) {
				select {
				case merged <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	if watched == 0 {
		return nil
	}
	logf("watching %d input device(s) for the power button", watched)
	return merged
}

// pivot mounts the real root filesystem and switches into it, re-executing
// k0smos there as PID1. It does not return on success.
func pivot(s *sys.Sys, cfg config.Config) error {
	// UUID=/LABEL= are resolved here rather than passed to mount(2): on real
	// hardware disks enumerate as /dev/sda or /dev/nvme0n1 and can reorder
	// between boots. Retried because virtio_blk and friends probe
	// asynchronously, so the device can appear just after its module loads.
	dev, err := blkid.ResolveWait(s, cfg.Root, rootProbeAttempts, func() {
		time.Sleep(rootProbeInterval)
	})
	if err != nil {
		return fmt.Errorf("find root %s: %w", cfg.Root, err)
	}
	if dev != cfg.Root {
		logf("resolved %s to %s", cfg.Root, dev)
	}

	if err := s.Mkdir(newRootDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", newRootDir, err)
	}
	if err := s.Mount(dev, newRootDir, cfg.RootFSType, 0, cfg.RootFlags); err != nil {
		return fmt.Errorf("mount %s (%s): %w", dev, cfg.RootFSType, err)
	}
	logf("mounted %s at %s, switching root", dev, newRootDir)
	// The marker tells the next k0smos it is already on the real root, so it
	// proceeds with the rest of init instead of trying to switch again.
	return switchroot.Do(s, newRootDir, initPath, []string{initPath, switchedFlag})
}

// loadModules loads the configured kernel modules from the running kernel's
// /lib/modules directory.
func loadModules(s *sys.Sys, cfg config.Config) error {
	names := cfg.Modules
	if names == nil {
		names = module.Default
	}
	if len(names) == 0 {
		logf("module loading disabled")
		return nil
	}
	release, err := s.Release()
	if err != nil {
		return fmt.Errorf("uname: %w", err)
	}
	base := "/lib/modules/" + release
	if err := module.Load(s, base, names); err != nil {
		return err
	}
	logf("kernel modules loaded from %s", base)
	return nil
}

// boot performs OS init, starts the reaper and the supervised k0s child, then
// blocks until a termination signal arrives and shuts the machine down.
//
// switched reports whether this is the post-switch_root invocation, which must
// not attempt the switch a second time.
func boot(ctx context.Context, s *sys.Sys, switched bool) error {
	logf("starting as PID1 (switched-root=%t)", switched)
	if err := mount.Ensure(s); err != nil {
		return fmt.Errorf("mounts: %w", err)
	}
	logf("pseudo-filesystems mounted")

	// Only readable now that /proc is mounted.
	cfg := config.Parse(readCmdline(cmdlinePath))

	// Modules come before cgroup and net: a stock distro kernel ships virtio,
	// ext4 and overlayfs as modules, so without this there is no NIC and no
	// container storage. Failures are not fatal — the kernel may have the
	// functionality built in, in which case there is nothing to load.
	if err := loadModules(s, cfg); err != nil {
		logf("warn: modules: %v", err)
	}

	// Leave the initramfs for the real root, if one was given. kubelet cannot
	// run on a ramfs root — cadvisor finds no filesystem info for it. Modules
	// had to be loaded first: the kernel needs virtio_blk and ext4 before the
	// root device is even visible. On success this execs and does not return.
	if cfg.Root != "" && !switched {
		if err := pivot(s, cfg); err != nil {
			return fmt.Errorf("switch root: %w", err)
		}
	}

	if err := cgroup.Setup(s); err != nil {
		return fmt.Errorf("cgroup: %w", err)
	}
	logf("cgroup2 hierarchy ready")
	if err := knet.Up(s); err != nil {
		return fmt.Errorf("net: %w", err)
	}
	logf("loopback up")

	// Created here rather than just before the child starts, because the DHCP
	// renewal goroutine below needs to be cancelled on shutdown too.
	runCtx, stopChild := context.WithCancel(ctx)
	defer stopChild()

	// Not fatal: a node with only loopback still boots, it just cannot pull
	// images. Better a degraded node with a console message than a panic.
	dns := cfg.DNS
	switch {
	case cfg.IP == "dhcp":
		leaseDNS, err := configureDHCP(runCtx, s, cfg)
		if err != nil {
			logf("warn: dhcp on %s: %v", cfg.Iface, err)
		} else if dns == "" {
			// An explicit k0smos.dns= wins: the lease's resolver is not always
			// usable (QEMU's slirp hands out one that never answers).
			dns = leaseDNS
		}
	case cfg.IP != "":
		if err := knet.Configure(s, cfg.Iface, cfg.IP, cfg.Gateway); err != nil {
			logf("warn: configure %s: %v", cfg.Iface, err)
		} else {
			logf("%s configured %s gw %s", cfg.Iface, cfg.IP, cfg.Gateway)
		}
	}
	if dns != "" {
		resolv := fmt.Appendf(nil, "nameserver %s\n", dns)
		if err := s.WriteFile("/etc/resolv.conf", resolv, 0644); err != nil {
			logf("warn: write resolv.conf: %v", err)
		}
	}

	if err := s.Sethostname(cfg.Hostname); err != nil {
		logf("warn: sethostname %q: %v", cfg.Hostname, err)
	} else {
		logf("hostname set to %q", cfg.Hostname)
	}

	// Reaper: SIGCHLD -> coalescing trigger -> drain wait4.
	chld := make(chan os.Signal, 1)
	signal.Notify(chld, unix.SIGCHLD)
	defer signal.Stop(chld)
	trigger := make(chan struct{}, 1)
	go pump(chld, trigger)
	go reaper.Run(runCtx, s, trigger)

	// PID1 starts with no environment at all, so children inherit an empty
	// PATH and cannot exec the binaries k0s stages at runtime.
	if err := os.Setenv("PATH", cfg.Path); err != nil {
		logf("warn: set PATH: %v", err)
	}

	// Started before the child so a host shutdown request is never missed while
	// k0s is coming up, which can take minutes.
	hostCmds := watchControlPort(runCtx)
	powerBtn := watchPowerButton(runCtx)

	logf("supervising %v", cfg.Exec)
	childDone := make(chan struct{})
	go func() {
		defer close(childDone)
		_ = supervise.Run(runCtx, supervise.Options{
			Command:    cfg.Exec[0],
			Args:       cfg.Exec[1:],
			MaxBackoff: 10 * time.Second,
			OnExit:     func(err error) { logf("child exited: %v", err) },
		})
	}()

	// Wait for a shutdown request. SIGINT is what the kernel delivers to PID1
	// on ctrl-alt-del; SIGTERM is the conventional request; the control port is
	// how the host asks, since this guest has no working power button.
	term := make(chan os.Signal, 1)
	signal.Notify(term, unix.SIGTERM, unix.SIGINT)
	how := shutdown.PowerOff
wait:
	for {
		select {
		case sig := <-term:
			logf("got %v, shutting down", sig)
			break wait
		case cmd, ok := <-hostCmds:
			// A closed channel yields the zero Command, which must not be
			// mistaken for a request: that alone once powered the machine off
			// seconds into boot with nothing connected to the port.
			if !ok {
				hostCmds = nil // nil channel blocks, disabling this case
				continue
			}
			logf("host requested %v", cmd)
			if cmd == control.Reboot {
				how = shutdown.Reboot
			}
			break wait
		case _, ok := <-powerBtn:
			// Same closed-channel hazard as above: a closed channel yields a
			// zero value that must not be read as a button press.
			if !ok {
				powerBtn = nil
				continue
			}
			logf("power button pressed, shutting down")
			break wait
		case <-ctx.Done():
			logf("context cancelled, shutting down")
			break wait
		}
	}

	stopChild()
	select {
	case <-childDone:
	case <-time.After(childGrace):
		logf("warn: child did not exit within %s", childGrace)
	}

	logf("syncing and unmounting")
	return shutdown.Do(shutdownAdapter{s}, how)
}
