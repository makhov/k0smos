package control

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseRecognisedCommands(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Command
	}{
		{"poweroff", PowerOff},
		{"POWEROFF", PowerOff},
		{"  poweroff  ", PowerOff},
		{"reboot", Reboot},
		{"restart", Reboot},
	} {
		got, ok := Parse(tc.line)
		if !ok || got != tc.want {
			t.Errorf("Parse(%q) = %v, %t; want %v, true", tc.line, got, ok, tc.want)
		}
	}
}

func TestParseRejectsUnknown(t *testing.T) {
	for _, line := range []string{"", "   ", "halt now please", "rm -rf /", "powerof"} {
		if got, ok := Parse(line); ok {
			t.Errorf("Parse(%q) = %v, true; want not ok", line, got)
		}
	}
}

func TestWatchEmitsCommandsInOrder(t *testing.T) {
	r := strings.NewReader("poweroff\nreboot\n")
	ch := Watch(context.Background(), r)

	for _, want := range []Command{PowerOff, Reboot} {
		select {
		case got, ok := <-ch:
			if !ok {
				t.Fatal("channel closed early")
			}
			if got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for command")
		}
	}
}

// Unparseable input must not kill the watcher: the port is a host-facing
// channel and a stray byte should not disable shutdown for the rest of boot.
func TestWatchSkipsGarbageAndKeepsReading(t *testing.T) {
	ch := Watch(context.Background(), strings.NewReader("nonsense\n\nhalt\npoweroff\n"))
	select {
	case got, ok := <-ch:
		if !ok || got != PowerOff {
			t.Fatalf("got %v, %t; want PowerOff, true", got, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out; watcher stopped at bad input")
	}
}

func TestWatchClosesChannelOnEOF(t *testing.T) {
	ch := Watch(context.Background(), strings.NewReader(""))
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel on EOF")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed on EOF")
	}
}

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

// A virtio-serial port with no host client attached returns EOF immediately.
// The watcher must reopen instead of giving up, or a host that connects later
// can never ask the guest to shut down.
func TestWatchReopenSurvivesImmediateEOF(t *testing.T) {
	opens := 0
	open := func() (io.ReadCloser, error) {
		opens++
		if opens < 3 {
			return nopCloser{strings.NewReader("")}, nil // nobody connected yet
		}
		return nopCloser{strings.NewReader("poweroff\n")}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := WatchReopen(ctx, open, time.Millisecond)
	select {
	case got, ok := <-ch:
		if !ok || got != PowerOff {
			t.Fatalf("got %v, %t; want PowerOff, true", got, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no command after %d reopens", opens)
	}
}

// An open that keeps failing must not spin or panic; it just keeps retrying.
func TestWatchReopenToleratesOpenErrors(t *testing.T) {
	open := func() (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF }
	ctx, cancel := context.WithCancel(context.Background())
	ch := WatchReopen(ctx, open, time.Millisecond)
	select {
	case <-ch:
		t.Fatal("received a command from a never-opening port")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchReopen did not stop on cancel")
	}
}

// blockingReader never returns, standing in for an idle virtio port.
type blockingReader struct{ done chan struct{} }

func (b blockingReader) Read([]byte) (int, error) {
	<-b.done
	return 0, io.EOF
}

func TestWatchStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	br := blockingReader{done: make(chan struct{})}
	ch := Watch(ctx, br)
	cancel()
	select {
	case <-ch: // closed or drained — either means Watch gave up
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not stop after context cancel")
	}
	close(br.done)
}
