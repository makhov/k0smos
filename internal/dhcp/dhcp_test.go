package dhcp

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

var testMAC = net.HardwareAddr{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}

func TestBuildDiscoverIsAValidBootpRequest(t *testing.T) {
	p := BuildDiscover(0xdeadbeef, testMAC, "k0smos")
	if len(p) < minPacketLen {
		t.Fatalf("packet is %d bytes, want at least %d", len(p), minPacketLen)
	}
	if p[0] != bootRequest {
		t.Errorf("op = %d, want %d", p[0], bootRequest)
	}
	if p[1] != htypeEthernet || p[2] != 6 {
		t.Errorf("htype/hlen = %d/%d, want 1/6", p[1], p[2])
	}
	if got := binary.BigEndian.Uint32(p[4:]); got != 0xdeadbeef {
		t.Errorf("xid = %#x, want 0xdeadbeef", got)
	}
	// The broadcast flag is what makes a server reply to 255.255.255.255,
	// which is the only way a client with no address can hear the answer.
	if got := binary.BigEndian.Uint16(p[10:]); got&flagBroadcast == 0 {
		t.Errorf("flags = %#x, broadcast bit not set", got)
	}
	if got := net.HardwareAddr(p[28:34]); got.String() != testMAC.String() {
		t.Errorf("chaddr = %s, want %s", got, testMAC)
	}
	if got := binary.BigEndian.Uint32(p[236:]); got != magicCookie {
		t.Errorf("magic = %#x, want %#x", got, magicCookie)
	}
	if mt, ok := findOption(p, optMessageType); !ok || len(mt) != 1 || mt[0] != msgDiscover {
		t.Errorf("message type option = %v, %t; want [1], true", mt, ok)
	}
}

func TestBuildRequestCarriesRequestedIPAndServer(t *testing.T) {
	p := BuildRequest(1, testMAC, "", net.IP{10, 0, 2, 15}, net.IP{10, 0, 2, 2})
	if mt, _ := findOption(p, optMessageType); mt[0] != msgRequest {
		t.Errorf("message type = %v, want request", mt)
	}
	if got, ok := findOption(p, optRequestedIP); !ok || !net.IP(got).Equal(net.IP{10, 0, 2, 15}) {
		t.Errorf("requested IP = %v, want 10.0.2.15", net.IP(got))
	}
	if got, ok := findOption(p, optServerID); !ok || !net.IP(got).Equal(net.IP{10, 0, 2, 2}) {
		t.Errorf("server id = %v, want 10.0.2.2", net.IP(got))
	}
}

// reply builds a server response: the slirp-style answer we expect in practice.
func reply(msgType byte, xid uint32, mac net.HardwareAddr, leaseSecs uint32, opts ...[]byte) []byte {
	p := make([]byte, minPacketLen)
	p[0] = bootReply
	p[1] = htypeEthernet
	p[2] = 6
	binary.BigEndian.PutUint32(p[4:], xid)
	copy(p[16:20], net.IP{10, 0, 2, 15}.To4()) // yiaddr
	copy(p[28:], mac)
	binary.BigEndian.PutUint32(p[236:], magicCookie)

	body := []byte{optMessageType, 1, msgType}
	body = append(body, optSubnetMask, 4, 255, 255, 255, 0)
	body = append(body, optRouter, 4, 10, 0, 2, 2)
	body = append(body, optDNS, 8, 10, 0, 2, 3, 1, 1, 1, 1)
	body = append(body, optServerID, 4, 10, 0, 2, 2)
	lt := make([]byte, 4)
	binary.BigEndian.PutUint32(lt, leaseSecs)
	body = append(body, optLeaseTime, 4)
	body = append(body, lt...)
	for _, o := range opts {
		body = append(body, o...)
	}
	body = append(body, optEnd)
	return append(p[:240], body...)
}

func TestParseExtractsLease(t *testing.T) {
	got, mt, err := Parse(reply(msgOffer, 7, testMAC, 3600), 7, testMAC)
	if err != nil {
		t.Fatal(err)
	}
	if mt != msgOffer {
		t.Errorf("message type = %d, want offer", mt)
	}
	if !got.IP.Equal(net.IP{10, 0, 2, 15}) {
		t.Errorf("IP = %v, want 10.0.2.15", got.IP)
	}
	if ones, _ := got.Mask.Size(); ones != 24 {
		t.Errorf("mask = %v, want /24", got.Mask)
	}
	if !got.Router.Equal(net.IP{10, 0, 2, 2}) {
		t.Errorf("router = %v", got.Router)
	}
	if len(got.DNS) != 2 || !got.DNS[0].Equal(net.IP{10, 0, 2, 3}) {
		t.Errorf("dns = %v, want two entries starting 10.0.2.3", got.DNS)
	}
	if !got.Server.Equal(net.IP{10, 0, 2, 2}) {
		t.Errorf("server = %v", got.Server)
	}
	if got.LeaseTime != time.Hour {
		t.Errorf("lease = %v, want 1h", got.LeaseTime)
	}
}

// Absent T1/T2 must fall back to the RFC 2131 fractions of the lease.
func TestParseDerivesRenewalTimersWhenAbsent(t *testing.T) {
	got, _, err := Parse(reply(msgAck, 7, testMAC, 3600), 7, testMAC)
	if err != nil {
		t.Fatal(err)
	}
	if got.T1 != 30*time.Minute {
		t.Errorf("T1 = %v, want 30m (half the lease)", got.T1)
	}
	if got.T2 != 52*time.Minute+30*time.Second {
		t.Errorf("T2 = %v, want 52m30s (7/8 of the lease)", got.T2)
	}
}

func TestParseUsesExplicitRenewalTimers(t *testing.T) {
	t1 := []byte{optT1, 4, 0, 0, 0, 60}
	t2 := []byte{optT2, 4, 0, 0, 0, 120}
	got, _, err := Parse(reply(msgAck, 7, testMAC, 3600, t1, t2), 7, testMAC)
	if err != nil {
		t.Fatal(err)
	}
	if got.T1 != time.Minute || got.T2 != 2*time.Minute {
		t.Errorf("T1/T2 = %v/%v, want 1m/2m", got.T1, got.T2)
	}
}

// Another client's traffic arrives on the same broadcast address, so replies
// that are not ours must be rejected rather than adopted.
func TestParseRejectsForeignReplies(t *testing.T) {
	otherMAC := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	if _, _, err := Parse(reply(msgOffer, 7, testMAC, 3600), 8, testMAC); err == nil {
		t.Error("accepted a reply with the wrong xid")
	}
	if _, _, err := Parse(reply(msgOffer, 7, otherMAC, 3600), 7, testMAC); err == nil {
		t.Error("accepted a reply addressed to another MAC")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		pkt  []byte
	}{
		{"too short", make([]byte, 100)},
		{"bad magic", func() []byte { p := reply(msgAck, 7, testMAC, 60); p[236] = 0; return p }()},
		{"not a reply", func() []byte { p := reply(msgAck, 7, testMAC, 60); p[0] = bootRequest; return p }()},
	} {
		if _, _, err := Parse(tc.pkt, 7, testMAC); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// A truncated option must not read past the end of the buffer.
func TestParseSurvivesTruncatedOptions(t *testing.T) {
	p := reply(msgAck, 7, testMAC, 60)
	p = append(p[:len(p)-1], optRouter, 4, 10, 0) // claims 4 bytes, supplies 2
	if _, _, err := Parse(p, 7, testMAC); err != nil {
		t.Fatalf("Parse = %v, want it to tolerate a truncated option", err)
	}
}
