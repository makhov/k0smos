package ntp

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// reply builds what a server would send back to req, as if the server's clock
// read srv when ours read cli.
func reply(req []byte, cli, srv time.Time, tweak func([]byte)) []byte {
	b := make([]byte, packetSize)
	b[0] = 0<<6 | 4<<3 | 4 // leap 0, version 4, mode 4 (server)
	b[1] = 2               // stratum
	copy(b[24:32], req[40:48])
	binary.BigEndian.PutUint64(b[32:], timestamp(srv))
	binary.BigEndian.PutUint64(b[40:], timestamp(srv))
	if tweak != nil {
		tweak(b)
	}
	_ = cli
	return b
}

// The number that matters: a machine an hour behind must be told it is an hour
// behind, give or take the round trip.
func TestOffsetMeasuresTheError(t *testing.T) {
	cli := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	srv := cli.Add(time.Hour)
	req := Request(cli)
	got, err := Offset(req, reply(req, cli, srv, nil), cli, cli.Add(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if d := got - time.Hour; d < -30*time.Millisecond || d > 30*time.Millisecond {
		t.Errorf("offset = %v, want about 1h", got)
	}
	// A clock that is already right is told so.
	req = Request(cli)
	got, err = Offset(req, reply(req, cli, cli, nil), cli, cli.Add(2*time.Millisecond))
	if err != nil || got > 5*time.Millisecond || got < -5*time.Millisecond {
		t.Errorf("offset = %v, err = %v; want about zero", got, err)
	}
}

// Every reply that is not an answer to this request, or that says the server
// does not know the time, must fail rather than move the clock. Setting a
// node's clock from a stray packet is worse than leaving it wrong.
func TestOffsetRejectsRepliesItMustNotTrust(t *testing.T) {
	cli := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	srv := cli.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		tweak func([]byte)
	}{
		{"not a server's mode", func(b []byte) { b[0] = 0<<6 | 4<<3 | 3 }},
		{"server's own clock unsynchronised", func(b []byte) { b[0] |= 3 << 6 }},
		{"kiss of death", func(b []byte) { b[1] = 0 }},
		{"absurd stratum", func(b []byte) { b[1] = 16 }},
		{"answers another request", func(b []byte) { binary.BigEndian.PutUint64(b[24:], 12345) }},
		{"no timestamp", func(b []byte) { binary.BigEndian.PutUint64(b[40:], 0) }},
	} {
		req := Request(cli)
		if _, err := Offset(req, reply(req, cli, srv, tc.tweak), cli, cli); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	req := Request(cli)
	if _, err := Offset(req, make([]byte, 10), cli, cli); err == nil {
		t.Error("a truncated reply was accepted")
	}
}

// A timestamp has to survive the trip through NTP's fixed-point form, or every
// correction is wrong by the rounding.
func TestTimestampRoundTrips(t *testing.T) {
	for _, want := range []time.Time{
		time.Unix(0, 0), time.Date(2026, 11, 17, 1, 2, 3, 123456789, time.UTC),
	} {
		got := unixTime(timestamp(want))
		if d := got.Sub(want); d > time.Microsecond || d < -time.Microsecond {
			t.Errorf("%v round-tripped to %v", want, got)
		}
	}
	if !unixTime(0).IsZero() {
		t.Error("the zero timestamp must read as no time at all")
	}
}

// fakeConn answers one exchange in memory.
type fakeConn struct {
	srv    time.Time
	req    []byte
	broken error
}

func (c *fakeConn) Write(p []byte) (int, error) {
	c.req = append([]byte(nil), p...)
	return len(p), nil
}

func (c *fakeConn) Read(p []byte) (int, error) {
	if c.broken != nil {
		return 0, c.broken
	}
	return copy(p, reply(c.req, c.srv, c.srv, nil)), nil
}

func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return nil }
func (c *fakeConn) RemoteAddr() net.Addr             { return nil }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// Sync moves on from a server that cannot be reached, and says which one
// answered -- a node with no shell is debugged from that line.
func TestSyncFallsThroughToAServerThatAnswers(t *testing.T) {
	cli := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return cli }
	dial := func(_, addr string) (net.Conn, error) {
		switch {
		case strings.HasPrefix(addr, "dead"):
			return nil, errors.New("no route to host")
		case strings.HasPrefix(addr, "mute"):
			return &fakeConn{broken: errors.New("i/o timeout")}, nil
		}
		return &fakeConn{srv: cli.Add(time.Hour)}, nil
	}
	offset, server, err := Sync(dial, []string{"dead", "mute", "good"}, 5*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	if server != "good" {
		t.Errorf("server = %q, want good", server)
	}
	if d := offset - time.Hour; d > time.Second || d < -time.Second {
		t.Errorf("offset = %v", offset)
	}
}

// Nothing reachable: the error has to name every server tried, so the console
// says what the machine attempted.
func TestSyncReportsEveryServerItTried(t *testing.T) {
	cli := time.Now()
	dial := func(string, string) (net.Conn, error) { return nil, errors.New("no route to host") }
	_, _, err := Sync(dial, []string{"a.example", "b.example"}, time.Second, func() time.Time { return cli })
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"a.example", "b.example"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if _, _, err := Sync(dial, nil, time.Second, time.Now); err == nil {
		t.Error("no servers configured must be an error, not a silent success")
	}
}
