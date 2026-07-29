//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/amakhov/k0smos/internal/cgroup"
	"github.com/amakhov/k0smos/internal/config"
	"github.com/amakhov/k0smos/internal/mount"
	knet "github.com/amakhov/k0smos/internal/net"
	"github.com/amakhov/k0smos/internal/reaper"
	"github.com/amakhov/k0smos/internal/shutdown"
	"github.com/amakhov/k0smos/internal/supervise"
	"github.com/amakhov/k0smos/internal/sys"

	"golang.org/x/sys/unix"
)

const (
	cmdlinePath = "/proc/cmdline"

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
	return boot(ctx, sys.New())
}

// boot performs OS init, starts the reaper and the supervised k0s child, then
// blocks until a termination signal arrives and shuts the machine down.
func boot(ctx context.Context, s *sys.Sys) error {
	logf("starting as PID1")
	if err := mount.Ensure(s); err != nil {
		return fmt.Errorf("mounts: %w", err)
	}
	logf("pseudo-filesystems mounted")
	if err := cgroup.Setup(s); err != nil {
		return fmt.Errorf("cgroup: %w", err)
	}
	logf("cgroup2 hierarchy ready")
	if err := knet.Up(s); err != nil {
		return fmt.Errorf("net: %w", err)
	}
	logf("loopback up")

	// Only readable now that /proc is mounted.
	cfg := config.Parse(readCmdline(cmdlinePath))
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
