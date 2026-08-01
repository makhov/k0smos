// Package nethub is an Ethernet hub for QEMU guests on one host.
//
// Guests need a network they share before they can be a cluster, and QEMU's
// user-mode networking cannot give them one: every guest sits behind its own NAT
// at the same address and sees only the host. The alternatives QEMU offers are a
// tap device or vmnet, both of which want root, and its multicast socket backend,
// which does not work on macOS at all — QEMU binds that socket to the multicast
// group address, which receives nothing on BSD, so guests come up with an
// interface that carries no traffic and nothing says why.
//
// What is left is QEMU's socket backend in connect mode, which is point to point.
// This is the missing piece: a process every guest connects to, which forwards
// each frame to all the others. It needs no privileges, behaves the same on macOS
// and Linux, and is one goroutine per guest.
//
// The wire format is QEMU's: each frame is a 4-byte big-endian length followed by
// that many bytes of Ethernet frame.
package nethub

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// maxFrame bounds what one read will allocate.
//
// Well above an Ethernet frame on purpose. A guest with segmentation offload
// hands the backend one buffer of up to 64KB rather than a wire-sized frame, and
// QEMU's own socket buffer is NET_BUFSIZE — 4096 + 65536 — so anything at or
// below 64KB is too small. Getting this wrong is not a dropped packet: framing
// cannot resynchronise, so the port is closed and the guest silently loses the
// network. That is exactly what happened at 65536, where a cluster joined, ran,
// and then reported "no route to host" to every peer once etcd started moving
// real data.
const maxFrame = 1 << 17

// Hub forwards Ethernet frames between everything connected to it.
//
// It is a hub and not a switch on purpose: it learns no addresses and floods
// every frame to every other port. With a handful of guests that costs nothing,
// and it cannot drop traffic by mislearning — which a test would experience as an
// intermittent network and spend a long time blaming on something else.
type Hub struct {
	ln net.Listener

	// OnDrop, if set, is called when a port is closed for a reason other than the
	// guest going away. Losing a port takes a guest off the network for good, and
	// without this it looks like a network that stopped working on its own — set
	// it to a test log and the cause is in the output instead.
	OnDrop func(error)

	mu     sync.Mutex
	ports  map[*port]struct{}
	closed bool

	wg sync.WaitGroup
}

type port struct {
	conn net.Conn
	// mu serialises writes: a frame must not interleave with another.
	mu sync.Mutex
}

// Listen starts a hub on addr ("127.0.0.1:0" for any free port).
//
// It must be listening before any guest starts: QEMU's connect mode fails at
// startup rather than retrying.
func Listen(addr string) (*Hub, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("nethub listen: %w", err)
	}
	h := &Hub{ln: ln, ports: map[*port]struct{}{}}
	h.wg.Go(h.accept)
	return h, nil
}

// Addr is the address guests connect to.
func (h *Hub) Addr() string { return h.ln.Addr().String() }

func (h *Hub) accept() {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return // the listener is closed; existing ports keep running
		}
		p := &port{conn: conn}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			conn.Close()
			return
		}
		h.ports[p] = struct{}{}
		h.mu.Unlock()

		h.wg.Go(func() { h.serve(p) })
	}
}

// serve reads frames from one guest and floods them to the others.
func (h *Hub) serve(p *port) {
	defer func() {
		h.mu.Lock()
		delete(h.ports, p)
		h.mu.Unlock()
		p.conn.Close()
	}()

	var hdr [4]byte
	buf := make([]byte, maxFrame)
	for {
		if _, err := io.ReadFull(p.conn, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > maxFrame {
			// Out of sync with the peer. Nothing can be recovered from here: the
			// stream has no framing to resynchronise on.
			h.drop(fmt.Errorf("port %s sent a %d-byte frame (limit %d); "+
				"dropping it, which takes that guest off the network",
				p.conn.RemoteAddr(), n, maxFrame))
			return
		}
		if _, err := io.ReadFull(p.conn, buf[:n]); err != nil {
			return
		}
		h.flood(p, hdr[:], buf[:n])
	}
}

// drop reports a port lost for a reason worth knowing about.
func (h *Hub) drop(err error) {
	h.mu.Lock()
	cb := h.OnDrop
	h.mu.Unlock()
	if cb != nil {
		cb(err)
	}
}

// flood writes one frame to every port except the one it came from.
func (h *Hub) flood(from *port, hdr, frame []byte) {
	h.mu.Lock()
	others := make([]*port, 0, len(h.ports))
	for p := range h.ports {
		if p != from {
			others = append(others, p)
		}
	}
	h.mu.Unlock()

	for _, p := range others {
		p.mu.Lock()
		// Errors are dropped deliberately: a guest that has gone away is not this
		// guest's problem, and its read loop will clean it up.
		if _, err := p.conn.Write(hdr); err == nil {
			p.conn.Write(frame)
		}
		p.mu.Unlock()
	}
}

// Close stops the hub and disconnects every guest.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	ports := make([]*port, 0, len(h.ports))
	for p := range h.ports {
		ports = append(ports, p)
	}
	h.mu.Unlock()

	err := h.ln.Close()
	for _, p := range ports {
		p.conn.Close()
	}
	h.wg.Wait()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
