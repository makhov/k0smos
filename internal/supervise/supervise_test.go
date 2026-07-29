package supervise

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunRestartsUntilContextCancelled(t *testing.T) {
	var calls atomic.Int64
	opts := Options{
		start: func(ctx context.Context) error {
			calls.Add(1)
			return errors.New("child died")
		},
		sleep:      func(time.Duration) {},
		MaxBackoff: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// let it restart a few times, then cancel
		for calls.Load() < 3 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	err := Run(ctx, opts)
	if err != nil {
		t.Fatalf("Run returned %v, want nil on cancel", err)
	}
	if n := calls.Load(); n < 3 {
		t.Fatalf("child started %d times, want >=3", n)
	}
}

func TestRunReportsEachChildExit(t *testing.T) {
	boom := errors.New("child died")
	var seen []error
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	opts := Options{
		start: func(context.Context) error {
			calls++
			if calls == 3 {
				cancel()
			}
			return boom
		},
		sleep:  func(time.Duration) {},
		OnExit: func(err error) { seen = append(seen, err) },
	}
	if err := Run(ctx, opts); err != nil {
		t.Fatalf("Run returned %v, want nil on cancel", err)
	}
	if len(seen) != calls {
		t.Fatalf("OnExit called %d times, want %d", len(seen), calls)
	}
	for i, err := range seen {
		if !errors.Is(err, boom) {
			t.Errorf("seen[%d] = %v, want %v", i, err, boom)
		}
	}
}

func TestRunBackoffIsCapped(t *testing.T) {
	var slept []time.Duration
	var calls int
	opts := Options{
		start: func(ctx context.Context) error {
			calls++
			return errors.New("child died")
		},
		sleep:      func(d time.Duration) { slept = append(slept, d) },
		MaxBackoff: 500 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	opts.start = func(ctx context.Context) error {
		calls++
		if calls == 6 {
			cancel()
		}
		return errors.New("child died")
	}
	if err := Run(ctx, opts); err != nil {
		t.Fatalf("Run returned %v, want nil on cancel", err)
	}
	if len(slept) == 0 {
		t.Fatal("no backoff sleeps recorded")
	}
	for i, d := range slept {
		if d > opts.MaxBackoff {
			t.Errorf("sleep[%d] = %v, exceeds MaxBackoff %v", i, d, opts.MaxBackoff)
		}
	}
	if slept[len(slept)-1] != opts.MaxBackoff {
		t.Errorf("last sleep = %v, want capped at %v", slept[len(slept)-1], opts.MaxBackoff)
	}
}
