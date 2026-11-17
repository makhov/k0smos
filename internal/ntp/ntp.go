// Package ntp asks a time server what the time is, over SNTP.
//
// A node that boots with the wrong clock fails in ways that name anything but
// the clock: certificates are not yet valid or already expired, etcd refuses
// to elect a leader, tokens are rejected, and logs from several machines
// cannot be read side by side. Nothing else here sets the clock -- there is no
// systemd-timesyncd and no chronyd -- so a machine keeps whatever its firmware
// handed it until this runs.
//
// SNTP (RFC 4330) rather than NTP: one exchange, step the clock, done. The
// discipline loop a real NTP daemon runs matters for staying in sync over
// weeks, and k0smos machines are replaced rather than kept.
package ntp

import (
	"encoding/binary"
	"fmt"
	"time"
)

// packetSize is an SNTP packet, header only: 48 bytes.
const packetSize = 48

// era is the offset between NTP's epoch (1900) and Unix's (1970), in seconds.
// NTP timestamps roll over in 2036; a machine whose clock is that wrong has
// worse problems than this package can fix.
const era = 2208988800

// Request is a client packet asking for the time. The transmit timestamp is
// the only field a server reads back, and it returns it untouched -- which is
// what lets the reply be matched to this request.
func Request(t time.Time) []byte {
	b := make([]byte, packetSize)
	// Leap 0, version 4, mode 3 (client).
	b[0] = 0<<6 | 4<<3 | 3
	binary.BigEndian.PutUint64(b[40:], timestamp(t))
	return b
}

// Offset is how far this machine's clock is from the server's, given the reply
// to a request sent at t1 and received at t4.
//
// The usual four-timestamp calculation: the round trip is split in half and
// taken off, so the answer does not include the time the packets spent in
// flight. A reply that is not an answer to this request, or that says the
// server does not know the time either, is an error rather than a correction.
func Offset(req, resp []byte, t1, t4 time.Time) (time.Duration, error) {
	if len(resp) < packetSize {
		return 0, fmt.Errorf("reply is %d bytes, want at least %d", len(resp), packetSize)
	}
	if mode := resp[0] & 0x7; mode != 4 && mode != 5 {
		return 0, fmt.Errorf("reply mode %d, want a server's", mode)
	}
	if leap := resp[0] >> 6; leap == 3 {
		return 0, fmt.Errorf("server says its own clock is not synchronised")
	}
	if stratum := resp[1]; stratum == 0 || stratum > 15 {
		// Stratum 0 is a "kiss of death": the server is telling us to go away,
		// and resp[3] holds a reason it would be rude to ignore.
		return 0, fmt.Errorf("server stratum %d, which is not a usable clock", stratum)
	}
	// The server echoes our transmit timestamp as its origin timestamp. An
	// older or duplicated reply does not match, and must not move the clock.
	if got, want := binary.BigEndian.Uint64(resp[24:]), binary.BigEndian.Uint64(req[40:]); got != want {
		return 0, fmt.Errorf("reply answers a different request")
	}
	t2 := unixTime(binary.BigEndian.Uint64(resp[32:])) // server received
	t3 := unixTime(binary.BigEndian.Uint64(resp[40:])) // server replied
	if t2.IsZero() || t3.IsZero() {
		return 0, fmt.Errorf("server sent no timestamp")
	}
	return (t2.Sub(t1) + t3.Sub(t4)) / 2, nil
}

// timestamp converts a wall-clock time to NTP's 64-bit fixed-point form.
func timestamp(t time.Time) uint64 {
	s := uint64(t.Unix() + era)
	// The fraction is in units of 1/2^32 of a second.
	f := uint64(t.Nanosecond()) << 32 / uint64(time.Second)
	return s<<32 | f
}

// unixTime converts NTP's form back, and reports the zero time for the zero
// timestamp -- which is what a server that has no time to give sends.
func unixTime(ts uint64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	s := int64(ts>>32) - era
	ns := int64(ts&0xffffffff) * int64(time.Second) >> 32
	return time.Unix(s, ns)
}
