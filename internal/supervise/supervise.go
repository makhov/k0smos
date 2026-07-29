// Package supervise keeps the k0s child process running for the lifetime of
// the init.
package supervise

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// Options configures the supervised child. Command/Args describe the real
// child; start/sleep are injectable seams for testing.
type Options struct {
	Command    string
	Args       []string
	MaxBackoff time.Duration
	// OnExit, if set, is called with the child's exit error every time it dies.
	// Without it a crash-looping child is invisible on the console.
	OnExit func(error)

	start func(ctx context.Context) error
	sleep func(time.Duration)
}

// Run supervises the child, restarting it with capped exponential backoff
// until ctx is cancelled. Returns nil on clean context cancellation.
func Run(ctx context.Context, opts Options) error {
	if opts.start == nil {
		opts.start = func(ctx context.Context) error {
			cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}
	if opts.sleep == nil {
		opts.sleep = time.Sleep
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 10 * time.Second
	}

	backoff := 200 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Note: as PID1 the zombie reaper may wait4(-1) the child first, in
		// which case this error is ECHILD rather than a real exit status.
		// Either way the child is gone and must be restarted.
		err := opts.start(ctx)
		if opts.OnExit != nil {
			opts.OnExit(err)
		}
		if ctx.Err() != nil {
			return nil
		}
		opts.sleep(backoff)
		if backoff *= 2; backoff > opts.MaxBackoff {
			backoff = opts.MaxBackoff
		}
	}
}
