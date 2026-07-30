package power

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"
)

// event builds one struct input_event as the kernel writes it on a 64-bit
// system: two longs of timestamp, then type, code and value.
func event(typ, code uint16, value int32) []byte {
	b := make([]byte, eventSize)
	binary.LittleEndian.PutUint64(b[0:], 1700000000) // tv_sec
	binary.LittleEndian.PutUint64(b[8:], 0)          // tv_usec
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	return b
}

func TestIsPowerPress(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   []byte
		want bool
	}{
		{"power key press", event(evKey, keyPower, 1), true},
		{"power key release", event(evKey, keyPower, 0), false},
		{"power key autorepeat", event(evKey, keyPower, 2), false},
		{"different key", event(evKey, 30, 1), false},
		{"not a key event", event(0, keyPower, 1), false},
		{"truncated", event(evKey, keyPower, 1)[:8], false},
	} {
		if got := isPowerPress(tc.ev); got != tc.want {
			t.Errorf("%s: isPowerPress = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestWatchEmitsOnPowerPress(t *testing.T) {
	stream := append(event(evKey, 30, 1), event(evKey, keyPower, 1)...)
	ch := Watch(context.Background(), bytes.NewReader(stream))
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no event emitted for a power press")
	}
}

// A keyboard or mouse also shows up as an input device; traffic from those must
// not be mistaken for a shutdown request.
func TestWatchIgnoresUnrelatedEvents(t *testing.T) {
	var stream []byte
	for _, code := range []uint16{30, 31, 32, 57} {
		stream = append(stream, event(evKey, code, 1)...)
	}
	ch := Watch(context.Background(), bytes.NewReader(stream))
	// Must check ok: a receive from the closed-at-EOF channel also succeeds,
	// and would look identical to a spurious press.
	if _, ok := <-ch; ok {
		t.Fatal("emitted for an unrelated key")
	}
}

// A partial record at EOF must not be misparsed.
func TestWatchHandlesTruncatedTrailingRecord(t *testing.T) {
	stream := append(event(evKey, keyPower, 1), 0x00, 0x01, 0x02)
	ch := Watch(context.Background(), bytes.NewReader(stream))
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("valid press before a truncated record was missed")
	}
}

func TestWatchClosesOnEOF(t *testing.T) {
	ch := Watch(context.Background(), bytes.NewReader(nil))
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("want closed channel on EOF")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed on EOF")
	}
}
