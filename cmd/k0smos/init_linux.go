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
	k0sBinary   = "/usr/local/bin/k0s"

	// childGrace is how long the k0s child gets to exit after ctx cancellation
	// before we start detaching filesystems under it.
	childGrace = 5 * time.Second
)

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
	if err := mount.Ensure(s); err != nil {
		return fmt.Errorf("mounts: %w", err)
	}
	if err := cgroup.Setup(s); err != nil {
		return fmt.Errorf("cgroup: %w", err)
	}
	if err := knet.Up(s); err != nil {
		return fmt.Errorf("net: %w", err)
	}

	// Only readable now that /proc is mounted.
	cfg := config.Parse(readCmdline(cmdlinePath))
	if err := s.Sethostname(cfg.Hostname); err != nil {
		fmt.Fprintf(os.Stderr, "warn: sethostname: %v\n", err)
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

	childDone := make(chan struct{})
	go func() {
		defer close(childDone)
		_ = supervise.Run(runCtx, supervise.Options{
			Command:    k0sBinary,
			Args:       []string{"controller", "--single"},
			MaxBackoff: 10 * time.Second,
		})
	}()

	// Wait for a shutdown request. SIGINT is what the kernel delivers to PID1
	// on ctrl-alt-del; SIGTERM is the conventional request.
	term := make(chan os.Signal, 1)
	signal.Notify(term, unix.SIGTERM, unix.SIGINT)
	select {
	case <-term:
	case <-ctx.Done():
	}

	stopChild()
	select {
	case <-childDone:
	case <-time.After(childGrace):
		fmt.Fprintln(os.Stderr, "warn: k0s did not exit within grace period")
	}

	return shutdown.Do(shutdownAdapter{s}, shutdown.PowerOff)
}
