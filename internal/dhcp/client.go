package dhcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

var broadcast = net.IP{255, 255, 255, 255}

// Conn carries DHCP packets. The implementation in internal/sys is a UDP socket
// bound to 0.0.0.0:68 with SO_BINDTODEVICE, which is what lets it send and
// receive before the interface has an address.
type Conn interface {
	SendTo(p []byte, dst net.IP) error
	// Recv fills p, returning os.ErrDeadlineExceeded if nothing arrives within
	// timeout.
	Recv(p []byte, timeout time.Duration) (int, error)
	Close() error
}

// Client performs DHCP for one interface.
type Client struct {
	Conn     Conn
	MAC      net.HardwareAddr
	Hostname string

	// MaxAttempts bounds how many times Acquire re-sends before failing. Zero
	// means the default.
	MaxAttempts int
	// Timeout is how long to wait for each reply. Zero means the default.
	Timeout time.Duration

	// Seams for tests.
	Rand  func() uint32
	Sleep func(context.Context, time.Duration) error
}

const (
	defaultAttempts = 6
	defaultTimeout  = 4 * time.Second
	// retryBackoff is the pause between attempts. Deliberately flat rather than
	// exponential: a node that cannot get an address is useless, so there is no
	// value in backing off into minutes.
	retryBackoff = 2 * time.Second
	// maxReply is generous; DHCP replies are far smaller.
	maxReply = 1500
)

func (c *Client) attempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return defaultAttempts
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (c *Client) xid() uint32 {
	if c.Rand != nil {
		return c.Rand()
	}
	// Derived from the clock rather than math/rand so there is no global state
	// to seed; uniqueness only has to hold against concurrent clients on the
	// same link, and there is one per machine.
	return uint32(time.Now().UnixNano())
}

// Acquire runs DISCOVER/OFFER/REQUEST/ACK until it holds a lease, retrying on
// loss and restarting after a NAK.
func (c *Client) Acquire(ctx context.Context) (Lease, error) {
	var lastErr error = errors.New("no DHCP server responded")
	for attempt := range c.attempts() {
		if ctx.Err() != nil {
			return Lease{}, ctx.Err()
		}
		lease, err := c.once()
		if err == nil {
			return lease, nil
		}
		lastErr = err
		if attempt < c.attempts()-1 {
			if err := c.sleep(ctx, retryBackoff); err != nil {
				return Lease{}, err
			}
		}
	}
	return Lease{}, lastErr
}

// once performs a single discover/request exchange.
func (c *Client) once() (Lease, error) {
	xid := c.xid()
	if err := c.Conn.SendTo(BuildDiscover(xid, c.MAC, c.Hostname), broadcast); err != nil {
		return Lease{}, fmt.Errorf("send discover: %w", err)
	}
	offer, mt, err := c.await(xid)
	if err != nil {
		return Lease{}, err
	}
	if mt != msgOffer {
		return Lease{}, fmt.Errorf("expected offer, got message type %d", mt)
	}

	req := BuildRequest(xid, c.MAC, c.Hostname, offer.IP, offer.Server)
	if err := c.Conn.SendTo(req, broadcast); err != nil {
		return Lease{}, fmt.Errorf("send request: %w", err)
	}
	lease, mt, err := c.await(xid)
	if err != nil {
		return Lease{}, err
	}
	if mt == msgNak {
		// The offer was withdrawn; the caller retries from discovery.
		return Lease{}, errors.New("server sent NAK")
	}
	if mt != msgAck {
		return Lease{}, fmt.Errorf("expected ack, got message type %d", mt)
	}
	return lease, nil
}

// await reads until a packet parses as a reply to xid, or the read times out.
// Replies for other clients share the broadcast address, so mismatches are
// skipped rather than treated as failures.
func (c *Client) await(xid uint32) (Lease, byte, error) {
	buf := make([]byte, maxReply)
	for {
		n, err := c.Conn.Recv(buf, c.timeout())
		if err != nil {
			return Lease{}, 0, err
		}
		lease, mt, err := Parse(buf[:n], xid, c.MAC)
		if err != nil {
			continue // not ours, or malformed
		}
		return lease, mt, nil
	}
}

// Renew keeps the lease alive, calling apply whenever a new one is granted. It
// returns only when ctx is done or apply fails.
//
// At T1 it unicasts a renewal to the issuing server. If that goes unanswered it
// broadcasts a rebind, and if that fails too it starts over from discovery,
// which is what RFC 2131 requires when a lease lapses.
func (c *Client) Renew(ctx context.Context, lease Lease, apply func(Lease) error) error {
	for {
		if err := c.sleep(ctx, lease.T1); err != nil {
			return err
		}
		next, err := c.renewOnce(lease)
		if err != nil {
			// Rebind: any server on the link may answer.
			next, err = c.rebind(lease)
		}
		if err != nil {
			// Lease is effectively gone; go back to discovery.
			next, err = c.Acquire(ctx)
			if err != nil {
				return fmt.Errorf("lease lost and re-acquire failed: %w", err)
			}
		}
		lease = next
		if err := apply(lease); err != nil {
			return err
		}
	}
}

func (c *Client) renewOnce(lease Lease) (Lease, error) {
	if lease.Server == nil {
		return Lease{}, errors.New("no server to renew with")
	}
	xid := c.xid()
	if err := c.Conn.SendTo(BuildRenew(xid, c.MAC, c.Hostname, lease.IP), lease.Server); err != nil {
		return Lease{}, err
	}
	got, mt, err := c.await(xid)
	if err != nil {
		return Lease{}, err
	}
	if mt != msgAck {
		return Lease{}, fmt.Errorf("renewal got message type %d", mt)
	}
	return got, nil
}

func (c *Client) rebind(lease Lease) (Lease, error) {
	xid := c.xid()
	if err := c.Conn.SendTo(BuildRenew(xid, c.MAC, c.Hostname, lease.IP), broadcast); err != nil {
		return Lease{}, err
	}
	got, mt, err := c.await(xid)
	if err != nil {
		return Lease{}, err
	}
	if mt != msgAck {
		return Lease{}, fmt.Errorf("rebind got message type %d", mt)
	}
	return got, nil
}
