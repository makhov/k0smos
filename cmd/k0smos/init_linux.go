//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"time"

	"github.com/amakhov/k0smos/internal/cgroup"
	"github.com/amakhov/k0smos/internal/config"
	"github.com/amakhov/k0smos/internal/module"
	"github.com/amakhov/k0smos/internal/mount"
	knet "github.com/amakhov/k0smos/internal/net"
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

// pivot mounts the real root filesystem and switches into it, re-executing
// k0smos there as PID1. It does not return on success.
func pivot(s *sys.Sys, cfg config.Config) error {
	if err := s.Mkdir(newRootDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", newRootDir, err)
	}
	if err := s.Mount(cfg.Root, newRootDir, cfg.RootFSType, 0, cfg.RootFlags); err != nil {
		return fmt.Errorf("mount %s (%s): %w", cfg.Root, cfg.RootFSType, err)
	}
	logf("mounted %s at %s, switching root", cfg.Root, newRootDir)
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

	// Not fatal: a node with only loopback still boots, it just cannot pull
	// images. Better a degraded node with a console message than a panic.
	if cfg.IP != "" {
		if err := knet.Configure(s, cfg.Iface, cfg.IP, cfg.Gateway); err != nil {
			logf("warn: configure %s: %v", cfg.Iface, err)
		} else {
			logf("%s configured %s gw %s", cfg.Iface, cfg.IP, cfg.Gateway)
		}
	}
	if cfg.DNS != "" {
		resolv := fmt.Appendf(nil, "nameserver %s\n", cfg.DNS)
		if err := s.WriteFile("/etc/resolv.conf", resolv, 0644); err != nil {
			logf("warn: write resolv.conf: %v", err)
		}
	}

	if err := s.Sethostname(cfg.Hostname); err != nil {
		logf("warn: sethostname %q: %v", cfg.Hostname, err)
	} else {
		logf("hostname set to %q", cfg.Hostname)
	}

	// Shut the workload down before shutdown.Do touches the filesystems.
	runCtx, stopChild := context.WithCancel(ctx)
	defer stopChild()

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
	// on ctrl-alt-del; SIGTERM is the conventional request.
	term := make(chan os.Signal, 1)
	signal.Notify(term, unix.SIGTERM, unix.SIGINT)
	select {
	case sig := <-term:
		logf("got %v, shutting down", sig)
	case <-ctx.Done():
		logf("context cancelled, shutting down")
	}

	stopChild()
	select {
	case <-childDone:
	case <-time.After(childGrace):
		logf("warn: child did not exit within %s", childGrace)
	}

	logf("syncing and unmounting")
	return shutdown.Do(shutdownAdapter{s}, shutdown.PowerOff)
}
