package ntp

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// DefaultServer is asked when nothing else is configured. The same one Talos
// settled on, and the same operator as the resolver k0smosctl hands a guest.
const DefaultServer = "time.cloudflare.com"

// Dialer opens a connection to a server, so a test can answer without a
// network and so the caller decides what "reachable" means.
type Dialer func(network, address string) (net.Conn, error)

// Query asks one server for this machine's clock error. The address may omit
// the port, in which case 123 is used.
func Query(dial Dialer, address string, timeout time.Duration, now func() time.Time) (time.Duration, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "123")
	}
	conn, err := dial("udp", address)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(now().Add(timeout)); err != nil {
		return 0, err
	}

	t1 := now()
	req := Request(t1)
	if _, err := conn.Write(req); err != nil {
		return 0, err
	}
	resp := make([]byte, packetSize)
	n, err := conn.Read(resp)
	if err != nil {
		return 0, err
	}
	// Read after the write, so the round trip the offset subtracts is this
	// exchange's and not a timer's.
	return Offset(req, resp[:n], t1, now())
}

// Sync asks each server in turn and returns the first answer. Servers are
// tried in order rather than in parallel: the first one usually answers, and a
// machine that cannot reach any of them is going to wait for the deadline
// whatever the order.
//
// The error names every server that was asked, because "no time" with no
// indication of what was tried is not something a node with no shell can be
// debugged from.
func Sync(dial Dialer, servers []string, timeout time.Duration, now func() time.Time) (time.Duration, string, error) {
	if len(servers) == 0 {
		return 0, "", errors.New("no time servers configured")
	}
	deadline := now().Add(timeout)
	var errs error
	for _, s := range servers {
		left := deadline.Sub(now())
		if left <= 0 {
			errs = errors.Join(errs, fmt.Errorf("%s: out of time before it could be asked", s))
			break
		}
		// Each server gets what is left rather than the whole budget, so one
		// black-holing server cannot use up every server's turn.
		offset, err := Query(dial, s, min(left, timeout/time.Duration(len(servers))+time.Second), now)
		if err == nil {
			return offset, s, nil
		}
		errs = errors.Join(errs, fmt.Errorf("%s: %w", s, err))
	}
	return 0, "", errs
}
