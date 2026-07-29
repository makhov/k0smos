package reaper

import "context"

// Reaper is the subset of *sys.Sys that reaping needs.
type Reaper interface {
	Reap() (pid int, ok bool, err error)
}

// Run drains exited children whenever trigger fires (or ctx is done). As PID1
// we inherit all orphans; each SIGCHLD may cover several exits, so we loop
// Reap() until it reports no child ready.
func Run(ctx context.Context, r Reaper, trigger <-chan struct{}) {
	drain := func() {
		for {
			_, ok, err := r.Reap()
			if err != nil || !ok {
				return
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			drain()
		}
	}
}
