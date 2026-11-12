package control

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
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

// nopCloser is a port that discards anything written back to it, which is what a
// host that only sends shutdown commands looks like.
type nopCloser struct{ io.Reader }

func (nopCloser) Close() error                { return nil }
func (nopCloser) Write(p []byte) (int, error) { return len(p), nil }

// driverPort behaves as a virtio console port does: while no host is
// connected a read returns EOF at once, and what a host sends is read off the
// same descriptor whenever it arrives. Each element of script is one read's
// result; "" is an EOF. After the script, it reports EOF forever.
type driverPort struct {
	mu     sync.Mutex
	script []string
	opens  int
	out    bytes.Buffer
}

func (d *driverPort) Read(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.script) == 0 {
		return 0, io.EOF
	}
	next := d.script[0]
	d.script = d.script[1:]
	if next == "" {
		return 0, io.EOF
	}
	return copy(p, next), nil
}

func (d *driverPort) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.out.Write(p)
}

func (d *driverPort) Close() error { return nil }

func (d *driverPort) open() (io.ReadWriteCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opens++
	return d, nil
}

// A port with no host attached returns EOF at once. The watcher must keep
// reading the same descriptor rather than close it: the driver discards
// whatever it has queued for a port when the guest closes it, which lost
// requests that arrived between an EOF and the close.
func TestWatchReopenHoldsThePortAcrossEOF(t *testing.T) {
	d := &driverPort{script: []string{"", "", "poweroff\n"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := WatchReopen(ctx, d.open, time.Millisecond, nil)
	select {
	case got, ok := <-ch:
		if !ok || got != PowerOff {
			t.Fatalf("got %v, %t; want PowerOff, true", got, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no command")
	}
	if d.opens != 1 {
		t.Errorf("opened the port %d times; want it held open", d.opens)
	}
}

// Two hosts one after the other, the first leaving half a line behind: both
// full requests are answered on the one descriptor, and the fragment does not
// become part of the second.
func TestHeldPortServesSuccessiveHosts(t *testing.T) {
	d := &driverPort{script: []string{RequestStatus + "\n", "", "", "garb", "", RequestStatus + "\n"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []string
	var mu sync.Mutex
	WatchReopen(ctx, d.open, time.Millisecond, func(req string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, req)
		return []byte("x"), nil
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(got, []string{RequestStatus, RequestStatus}) {
		t.Errorf("requests = %q, want two clean %q", got, RequestStatus)
	}
	if d.opens != 1 {
		t.Errorf("opened the port %d times; want it held open", d.opens)
	}
}

// An open that keeps failing must not spin or panic; it just keeps retrying.
func TestWatchReopenToleratesOpenErrors(t *testing.T) {
	open := func() (io.ReadWriteCloser, error) { return nil, io.ErrUnexpectedEOF }
	ctx, cancel := context.WithCancel(context.Background())
	ch := WatchReopen(ctx, open, time.Millisecond, nil)
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

// port is a bidirectional control channel for tests: what the host sent can be
// read back, and what the node replied is captured.
type port struct {
	in    io.Reader
	mu    sync.Mutex // the watcher writes while a test reads what it wrote
	out   bytes.Buffer
	close func()
}

func (p *port) Read(b []byte) (int, error) { return p.in.Read(b) }
func (p *port) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.Write(b)
}

// written is what the watcher has replied so far.
func (p *port) written() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}
func (p *port) Close() error {
	if p.close != nil {
		p.close()
	}
	return nil
}

func TestPowerCommandIsAcknowledged(t *testing.T) {
	p := &port{in: strings.NewReader("poweroff\n")}
	commands := make(chan Command, 1)
	// The port is held open after EOF, so readOnce returns only when told to.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if !readOnce(ctx, func() (io.ReadWriteCloser, error) {
		return p, nil
	}, commands, nil, &portState{}) {
		t.Fatal("power command was not forwarded")
	}

	select {
	case cmd := <-commands:
		if cmd != PowerOff {
			t.Fatalf("command = %v, want PowerOff", cmd)
		}
	default:
		t.Fatal("power command was not delivered")
	}
	if got, want := p.written(), ReplyOK+" 0\n"; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// A data request must be answered on the same port, not forwarded as a command:
// the caller holding the command channel has no handle to reply through.
func TestRequestIsAnsweredOnThePort(t *testing.T) {
	p := &port{in: strings.NewReader(RequestKubeconfig + "\n")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	respond := func(req string) ([]byte, error) {
		if req != RequestKubeconfig {
			t.Errorf("responder got %q", req)
		}
		return []byte("apiVersion: v1\nkind: Config\n"), nil
	}

	cmds := WatchReopen(ctx, func() (io.ReadWriteCloser, error) {
		return p, nil
	}, time.Millisecond, respond)

	// Give the loop a moment, then check nothing arrived as a command.
	select {
	case cmd := <-cmds:
		t.Fatalf("request surfaced as command %v; it must be answered in place", cmd)
	case <-time.After(50 * time.Millisecond):
	}

	got := p.written()
	want := ReplyOK + " 28\napiVersion: v1\nkind: Config\n"
	if got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// A request with an argument reaches the responder whole, so it can read the
// role off it. Everything on the line after the verb belongs to the request.
func TestRequestWithArgumentReachesTheResponder(t *testing.T) {
	p := &port{in: strings.NewReader(RequestToken + " controller\n")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan string, 1)
	cmds := WatchReopen(ctx, func() (io.ReadWriteCloser, error) { return p, nil },
		time.Millisecond, func(req string) ([]byte, error) {
			select {
			case got <- req:
			default:
			}
			return []byte("tok"), nil
		})

	select {
	case cmd := <-cmds:
		t.Fatalf("request surfaced as command %v; it must be answered in place", cmd)
	case req := <-got:
		if want := RequestToken + " controller"; req != want {
			t.Errorf("responder got %q, want %q", req, want)
		}
	case <-time.After(time.Second):
		t.Fatal("responder was never called")
	}
}

// An unknown verb must not be treated as a request: the port is host-facing, and
// answering anything at all to arbitrary input widens what it does.
func TestUnknownVerbIsNeitherRequestNorCommand(t *testing.T) {
	p := &port{in: strings.NewReader("tokens controller\nkubeconfigx\npoweroff\n")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmds := WatchReopen(ctx, func() (io.ReadWriteCloser, error) { return p, nil },
		time.Millisecond, func(req string) ([]byte, error) {
			t.Errorf("responder called for %q", req)
			return nil, nil
		})

	// The poweroff behind them still arrives, so the stray lines were skipped
	// rather than having stopped the reader.
	select {
	case cmd := <-cmds:
		if cmd != PowerOff {
			t.Errorf("got %v, want PowerOff", cmd)
		}
	case <-time.After(time.Second):
		t.Fatal("poweroff never arrived")
	}
}

// Round-trip through the code the host actually uses, so the two ends cannot
// drift apart in framing.
func TestRequestRoundTrip(t *testing.T) {
	payload := []byte("apiVersion: v1\nkind: Config\nclusters: []\n")

	// The node's side of the wire: read the request, write the reply.
	nodeIn, hostOut := io.Pipe()
	hostIn, nodeOut := io.Pipe()
	go func() {
		line, _ := bufio.NewReader(nodeIn).ReadString('\n')
		serveRequest(nodeOut, strings.TrimSpace(line), func(string) ([]byte, error) {
			return payload, nil
		})
	}()

	got, err := Request(struct {
		io.Reader
		io.Writer
	}{hostIn, hostOut}, RequestKubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %q, want %q", got, payload)
	}
}

// An error must reach the host as an error, not as an empty success — a node with
// no cluster yet is the common case.
func TestRequestSurfacesNodeErrors(t *testing.T) {
	nodeIn, hostOut := io.Pipe()
	hostIn, nodeOut := io.Pipe()
	go func() {
		line, _ := bufio.NewReader(nodeIn).ReadString('\n')
		serveRequest(nodeOut, strings.TrimSpace(line), func(string) ([]byte, error) {
			return nil, errors.New("open /var/lib/k0s/pki/admin.conf: no such file or directory")
		})
	}()

	_, err := Request(struct {
		io.Reader
		io.Writer
	}{hostIn, hostOut}, RequestKubeconfig)
	if err == nil {
		t.Fatal("no error from a failing responder")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error = %v, want it to name the cause", err)
	}
}

// A node built without a responder must say so rather than hang the host waiting
// for a reply that never comes.
func TestRequestWithoutResponderIsRefused(t *testing.T) {
	var buf bytes.Buffer
	serveRequest(&buf, RequestKubeconfig, nil)
	if !strings.HasPrefix(buf.String(), ReplyError) {
		t.Errorf("reply = %q, want an error line", buf.String())
	}
}

// A length the host cannot have meant must be refused before it is allocated.
func TestRequestRejectsAbsurdLength(t *testing.T) {
	reply := strings.NewReader(ReplyOK + " 99999999999\n")
	_, err := Request(struct {
		io.Reader
		io.Writer
	}{reply, io.Discard}, RequestKubeconfig)
	if err == nil {
		t.Error("accepted an out-of-range reply length")
	}
}

// Shutdown must still work on a port that also serves requests.
func TestCommandsStillFlowAlongsideRequests(t *testing.T) {
	p := &port{in: strings.NewReader(RequestKubeconfig + "\npoweroff\n")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmds := WatchReopen(ctx, func() (io.ReadWriteCloser, error) { return p, nil },
		time.Millisecond, func(string) ([]byte, error) { return []byte("x"), nil })

	select {
	case cmd := <-cmds:
		if cmd != PowerOff {
			t.Errorf("command = %v, want poweroff", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poweroff never arrived after a request on the same port")
	}
}
