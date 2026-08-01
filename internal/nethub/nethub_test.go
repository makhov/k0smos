package nethub

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// dialPort connects to the hub the way QEMU's socket backend does.
func dialPort(t *testing.T, h *Hub) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", h.Addr())
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func writeFrame(t *testing.T, c net.Conn, frame []byte) {
	t.Helper()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(frame)))
	if _, err := c.Write(append(hdr[:], frame...)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// readFrame reads one frame, or reports that none arrived in time.
func readFrame(t *testing.T, c net.Conn, within time.Duration) ([]byte, error) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(within)); err != nil {
		t.Fatal(err)
	}
	var hdr [4]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func newHub(t *testing.T) *Hub {
	t.Helper()
	h, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// The whole point: a frame from one guest reaches the others. This is what the
// multicast backend failed to do on macOS.
func TestFrameReachesEveryOtherPort(t *testing.T) {
	h := newHub(t)
	a, b, c := dialPort(t, h), dialPort(t, h), dialPort(t, h)
	// Give the accept loop a moment to register all three, or the first frame
	// arrives before the later ports exist and is legitimately not forwarded.
	waitForPorts(t, h, 3)

	writeFrame(t, a, []byte("broadcast"))
	for i, peer := range []net.Conn{b, c} {
		got, err := readFrame(t, peer, 2*time.Second)
		if err != nil {
			t.Fatalf("peer %d received nothing: %v", i, err)
		}
		if string(got) != "broadcast" {
			t.Errorf("peer %d got %q", i, got)
		}
	}
}

// A hub must not echo. A guest that hears its own frames sees duplicate ARP
// replies and its own broadcasts coming back, which reads as a network loop.
func TestSenderDoesNotSeeItsOwnFrame(t *testing.T) {
	h := newHub(t)
	a, b := dialPort(t, h), dialPort(t, h)
	waitForPorts(t, h, 2)

	writeFrame(t, a, []byte("mine"))
	if _, err := readFrame(t, b, 2*time.Second); err != nil {
		t.Fatalf("the other port should have received it: %v", err)
	}
	if got, err := readFrame(t, a, 200*time.Millisecond); err == nil {
		t.Errorf("sender received its own frame back: %q", got)
	}
}

// One guest going away must not take the segment with it: in a cluster test a
// node is stopped while the others keep running.
func TestRemainingPortsKeepWorkingAfterOneLeaves(t *testing.T) {
	h := newHub(t)
	a, b, gone := dialPort(t, h), dialPort(t, h), dialPort(t, h)
	waitForPorts(t, h, 3)
	gone.Close()
	waitForPorts(t, h, 2)

	writeFrame(t, a, []byte("still here"))
	got, err := readFrame(t, b, 2*time.Second)
	if err != nil {
		t.Fatalf("surviving peer received nothing: %v", err)
	}
	if string(got) != "still here" {
		t.Errorf("got %q", got)
	}
}

// A length that cannot be a frame means the stream has desynchronised, and there
// is nothing to resynchronise on. The port is dropped rather than allocating on
// whatever the peer claimed.
func TestAbsurdLengthDropsThePortAndSaysSo(t *testing.T) {
	h := newHub(t)
	dropped := make(chan error, 1)
	h.mu.Lock()
	h.OnDrop = func(err error) {
		select {
		case dropped <- err:
		default:
		}
	}
	h.mu.Unlock()

	bad := dialPort(t, h)
	waitForPorts(t, h, 1)

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 1<<30)
	bad.Write(hdr[:])
	waitForPorts(t, h, 0)

	select {
	case err := <-dropped:
		if err == nil {
			t.Fatal("OnDrop called with a nil error")
		}
	case <-time.After(2 * time.Second):
		t.Error("the port was dropped without reporting why")
	}
}

// A guest with segmentation offload hands the backend far more than a wire-sized
// frame. Rejecting those closed the port and took the guest off the network for
// the rest of the run, which is the failure this bound exists to avoid.
func TestOffloadSizedFrameIsForwarded(t *testing.T) {
	h := newHub(t)
	a, b := dialPort(t, h), dialPort(t, h)
	waitForPorts(t, h, 2)

	// QEMU's own socket buffer is NET_BUFSIZE, 4096 + 65536.
	big := make([]byte, 4096+65536)
	for i := range big {
		big[i] = byte(i)
	}
	writeFrame(t, a, big)

	got, err := readFrame(t, b, 5*time.Second)
	if err != nil {
		t.Fatalf("a %d-byte frame was not forwarded: %v", len(big), err)
	}
	if len(got) != len(big) {
		t.Fatalf("got %d bytes, want %d", len(got), len(big))
	}
	if string(got) != string(big) {
		t.Error("frame came through corrupted")
	}
}

// Close must not block, whether or not guests are still attached.
func TestCloseWithPortsAttached(t *testing.T) {
	h, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", h.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitForPorts(t, h, 1)

	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked")
	}
	if err := h.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// waitForPorts blocks until the hub has exactly n ports.
func waitForPorts(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		got := len(h.ports)
		h.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hub never settled at %d ports", n)
}
