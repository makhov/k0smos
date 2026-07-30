package dhcp

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// fakeConn is a scripted DHCP server. Each Send consumes one entry from
// replies; an empty entry means "no answer", i.e. Recv times out.
type fakeConn struct {
	mu      sync.Mutex
	sent    [][]byte
	sentTo  []string
	replies [][]byte
	mac     net.HardwareAddr
	// leaseSecs is used when a reply entry is generated on the fly.
	closed bool
}

func (f *fakeConn) SendTo(p []byte, dst net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, append([]byte(nil), p...))
	f.sentTo = append(f.sentTo, dst.String())
	return nil
}

func (f *fakeConn) Recv(p []byte, _ time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) == 0 {
		return 0, os.ErrDeadlineExceeded
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	if len(r) == 0 {
		return 0, os.ErrDeadlineExceeded
	}
	return copy(p, r), nil
}

func (f *fakeConn) Close() error { f.closed = true; return nil }

func (f *fakeConn) messageTypes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []byte
	for _, p := range f.sent {
		if mt, ok := findOption(p, optMessageType); ok && len(mt) == 1 {
			out = append(out, mt[0])
		}
	}
	return out
}

// newClient wires a client with a deterministic xid and no real sleeping.
func newClient(c Conn) *Client {
	return &Client{
		Conn:     c,
		MAC:      testMAC,
		Hostname: "k0smos",
		Rand:     func() uint32 { return 7 },
		Sleep:    func(context.Context, time.Duration) error { return nil },
	}
}

func TestAcquireCompletesTheHandshake(t *testing.T) {
	f := &fakeConn{mac: testMAC}
	f.replies = [][]byte{
		reply(msgOffer, 7, testMAC, 3600),
		reply(msgAck, 7, testMAC, 3600),
	}
	lease, err := newClient(f).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !lease.IP.Equal(net.IP{10, 0, 2, 15}) {
		t.Errorf("IP = %v, want 10.0.2.15", lease.IP)
	}
	if got := f.messageTypes(); len(got) != 2 || got[0] != msgDiscover || got[1] != msgRequest {
		t.Errorf("sent message types %v, want [discover request]", got)
	}
	// Both must go to the broadcast address: the client has no address yet.
	for i, dst := range f.sentTo {
		if dst != "255.255.255.255" {
			t.Errorf("packet %d sent to %s, want broadcast", i, dst)
		}
	}
}

// Lost packets are the normal case on a busy network, so a missing OFFER must
// be retried rather than failing the boot.
func TestAcquireRetriesWhenNoOfferArrives(t *testing.T) {
	f := &fakeConn{mac: testMAC}
	f.replies = [][]byte{
		nil, // timeout
		nil, // timeout
		reply(msgOffer, 7, testMAC, 3600),
		reply(msgAck, 7, testMAC, 3600),
	}
	if _, err := newClient(f).Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.messageTypes()); n < 3 {
		t.Errorf("sent %d packets, want at least 3 (two retried discovers)", n)
	}
}

func TestAcquireGivesUpAfterMaxAttempts(t *testing.T) {
	f := &fakeConn{mac: testMAC}
	c := newClient(f)
	c.MaxAttempts = 3
	if _, err := c.Acquire(context.Background()); err == nil {
		t.Fatal("Acquire = nil, want error when no server answers")
	}
	if got := len(f.messageTypes()); got != 3 {
		t.Errorf("sent %d discovers, want 3", got)
	}
}

// A NAK means the server refused; retrying the same REQUEST forever would hang
// the boot, so it must restart discovery.
func TestAcquireRestartsAfterNak(t *testing.T) {
	f := &fakeConn{mac: testMAC}
	f.replies = [][]byte{
		reply(msgOffer, 7, testMAC, 3600),
		reply(msgNak, 7, testMAC, 0),
		reply(msgOffer, 7, testMAC, 3600),
		reply(msgAck, 7, testMAC, 3600),
	}
	if _, err := newClient(f).Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.messageTypes()
	if len(got) != 4 {
		t.Fatalf("sent %v, want discover,request,discover,request", got)
	}
	if got[2] != msgDiscover {
		t.Errorf("after NAK sent %d, want a fresh discover", got[2])
	}
}

func TestAcquireStopsOnContextCancel(t *testing.T) {
	f := &fakeConn{mac: testMAC}
	ctx, cancel := context.WithCancel(context.Background())
	c := newClient(f)
	c.Sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	if _, err := c.Acquire(ctx); err == nil {
		t.Fatal("Acquire = nil, want error on cancel")
	}
}

// Renew must wait until T1, then unicast to the issuing server rather than
// broadcasting, and report the refreshed lease.
func TestRenewUnicastsToServerAtT1(t *testing.T) {
	f := &fakeConn{mac: testMAC}
	f.replies = [][]byte{reply(msgAck, 7, testMAC, 3600)}
	var slept []time.Duration
	c := newClient(f)
	c.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	lease := Lease{
		IP:     net.IP{10, 0, 2, 15},
		Server: net.IP{10, 0, 2, 2},
		Mask:   net.CIDRMask(24, 32),
		T1:     30 * time.Minute,
		T2:     52 * time.Minute,
	}
	var got []Lease
	ctx, cancel := context.WithCancel(context.Background())
	err := c.Renew(ctx, lease, func(l Lease) error {
		got = append(got, l)
		cancel() // one renewal is enough for the test
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(slept) == 0 || slept[0] != 30*time.Minute {
		t.Errorf("first sleep = %v, want T1 30m", slept)
	}
	if len(f.sentTo) == 0 || f.sentTo[0] != "10.0.2.2" {
		t.Errorf("renewal sent to %v, want unicast 10.0.2.2", f.sentTo)
	}
	if len(got) != 1 || !got[0].IP.Equal(net.IP{10, 0, 2, 15}) {
		t.Errorf("applied leases = %v, want one for 10.0.2.15", got)
	}
}

// If the server never answers the renewal, the client must fall back to a
// broadcast rebind rather than sitting on an expiring lease.
func TestRenewFallsBackToBroadcastRebind(t *testing.T) {
	f := &fakeConn{mac: testMAC}
	f.replies = [][]byte{nil, reply(msgAck, 7, testMAC, 3600)}
	c := newClient(f)
	applied := 0
	ctx, cancel := context.WithCancel(context.Background())
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	err := c.Renew(ctx, Lease{
		IP: net.IP{10, 0, 2, 15}, Server: net.IP{10, 0, 2, 2},
		Mask: net.CIDRMask(24, 32), T1: time.Minute, T2: 2 * time.Minute,
	}, func(Lease) error {
		applied++
		cancel()
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied %d leases, want 1 via rebind", applied)
	}
	if len(f.sentTo) < 2 || f.sentTo[1] != "255.255.255.255" {
		t.Errorf("second attempt went to %v, want a broadcast rebind", f.sentTo)
	}
}
