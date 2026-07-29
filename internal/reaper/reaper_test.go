package reaper

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeReaper struct {
	mu     sync.Mutex
	queue  []int // pids to hand out, one per Reap until empty
	reaped []int
}

func (f *fakeReaper) Reap() (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return 0, false, nil
	}
	pid := f.queue[0]
	f.queue = f.queue[1:]
	f.reaped = append(f.reaped, pid)
	return pid, true, nil
}

func TestRunDrainsAllReadyChildren(t *testing.T) {
	f := &fakeReaper{queue: []int{10, 11, 12}}
	trigger := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { Run(ctx, f, trigger); close(done) }()

	trigger <- struct{}{}
	// give the loop time to drain
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		n := len(f.reaped)
		f.mu.Unlock()
		if n == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("reaped %d, want 3", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
