// Package dhcp is a minimal DHCPv4 client.
//
// It exists because neither of the usual shortcuts is available. The kernel's
// own ip= autoconfiguration runs before /init, so it cannot see a NIC whose
// driver k0smos loads as a module — and this kernel does not even build
// CONFIG_IP_PNP. Shipping an external client such as udhcpc would put a second
// binary, and in practice a shell, into an image specified to have neither.
//
// The wire handling deliberately avoids AF_PACKET: the client sets the BOOTP
// broadcast flag, which RFC 2131 requires servers to honour by broadcasting
// their reply, so an ordinary UDP socket bound to 0.0.0.0:68 can receive it
// with no address configured. That avoids hand-built IP/UDP headers and
// checksums, and avoids needing the af_packet module (CONFIG_PACKET=m here).
package dhcp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	bootRequest = 1
	bootReply   = 2

	htypeEthernet = 1
	magicCookie   = 0x63825363
	flagBroadcast = 0x8000

	// minPacketLen covers the fixed BOOTP fields (236) plus the magic cookie.
	minPacketLen = 240

	msgDiscover = 1
	msgOffer    = 2
	msgRequest  = 3
	msgAck      = 5
	msgNak      = 6

	optSubnetMask  = 1
	optRouter      = 3
	optDNS         = 6
	optHostname    = 12
	optRequestedIP = 50
	optLeaseTime   = 51
	optMessageType = 53
	optServerID    = 54
	optParamList   = 55
	optT1          = 58
	optT2          = 59
	optEnd         = 255
)

// Lease is an accepted DHCP offer.
type Lease struct {
	IP        net.IP
	Mask      net.IPMask
	Router    net.IP
	DNS       []net.IP
	Server    net.IP
	LeaseTime time.Duration
	// T1 is when to renew with the issuing server, T2 when to start rebinding
	// with any server.
	T1 time.Duration
	T2 time.Duration
}

// CIDR renders the address in prefix form, for internal/net.Configure.
func (l Lease) CIDR() string {
	ones, _ := l.Mask.Size()
	return fmt.Sprintf("%s/%d", l.IP, ones)
}

// BuildDiscover builds a DHCPDISCOVER.
func BuildDiscover(xid uint32, mac net.HardwareAddr, hostname string) []byte {
	return build(msgDiscover, xid, mac, hostname, nil, nil)
}

// BuildRequest builds a DHCPREQUEST. requested and server may be nil, which is
// what a renewal looks like: the address is already held, so it travels in
// ciaddr rather than as an option.
func BuildRequest(xid uint32, mac net.HardwareAddr, hostname string, requested, server net.IP) []byte {
	return build(msgRequest, xid, mac, hostname, requested, server)
}

// BuildRenew builds the unicast DHCPREQUEST used at T1, which carries the
// current address in ciaddr and omits the requested-IP and server options.
func BuildRenew(xid uint32, mac net.HardwareAddr, hostname string, current net.IP) []byte {
	p := build(msgRequest, xid, mac, hostname, nil, nil)
	copy(p[12:16], current.To4()) // ciaddr
	return p
}

func build(msgType byte, xid uint32, mac net.HardwareAddr, hostname string, requested, server net.IP) []byte {
	p := make([]byte, minPacketLen)
	p[0] = bootRequest
	p[1] = htypeEthernet
	p[2] = byte(len(mac))
	binary.BigEndian.PutUint32(p[4:], xid)
	binary.BigEndian.PutUint16(p[10:], flagBroadcast)
	copy(p[28:44], mac)
	binary.BigEndian.PutUint32(p[236:], magicCookie)

	opts := []byte{optMessageType, 1, msgType}
	if requested != nil {
		opts = append(opts, optRequestedIP, 4)
		opts = append(opts, requested.To4()...)
	}
	if server != nil {
		opts = append(opts, optServerID, 4)
		opts = append(opts, server.To4()...)
	}
	if hostname != "" {
		opts = append(opts, optHostname, byte(len(hostname)))
		opts = append(opts, hostname...)
	}
	opts = append(opts, optParamList, 4, optSubnetMask, optRouter, optDNS, optLeaseTime)
	opts = append(opts, optEnd)

	p = append(p, opts...)
	// Some servers ignore packets shorter than the BOOTP minimum.
	for len(p) < 300 {
		p = append(p, 0)
	}
	return p
}

// Parse validates a server reply and extracts its lease. It returns an error
// for anything that is not a well-formed reply to this client's xid and MAC,
// since broadcast replies for other clients arrive on the same socket.
func Parse(p []byte, xid uint32, mac net.HardwareAddr) (Lease, byte, error) {
	if len(p) < minPacketLen {
		return Lease{}, 0, fmt.Errorf("packet too short: %d bytes", len(p))
	}
	if p[0] != bootReply {
		return Lease{}, 0, errors.New("not a BOOTP reply")
	}
	if binary.BigEndian.Uint32(p[236:]) != magicCookie {
		return Lease{}, 0, errors.New("bad magic cookie")
	}
	if got := binary.BigEndian.Uint32(p[4:]); got != xid {
		return Lease{}, 0, fmt.Errorf("xid %#x is not ours (%#x)", got, xid)
	}
	if net.HardwareAddr(p[28:28+len(mac)]).String() != mac.String() {
		return Lease{}, 0, errors.New("reply is for another MAC")
	}

	mt, ok := findOption(p, optMessageType)
	if !ok || len(mt) != 1 {
		return Lease{}, 0, errors.New("no message type option")
	}

	l := Lease{IP: net.IP(append([]byte(nil), p[16:20]...))}
	if v, ok := findOption(p, optSubnetMask); ok && len(v) == 4 {
		l.Mask = net.IPMask(append([]byte(nil), v...))
	}
	if v, ok := findOption(p, optRouter); ok && len(v) >= 4 {
		l.Router = net.IP(append([]byte(nil), v[:4]...))
	}
	if v, ok := findOption(p, optDNS); ok {
		for i := 0; i+4 <= len(v); i += 4 {
			l.DNS = append(l.DNS, net.IP(append([]byte(nil), v[i:i+4]...)))
		}
	}
	if v, ok := findOption(p, optServerID); ok && len(v) == 4 {
		l.Server = net.IP(append([]byte(nil), v...))
	}
	if v, ok := findOption(p, optLeaseTime); ok && len(v) == 4 {
		l.LeaseTime = time.Duration(binary.BigEndian.Uint32(v)) * time.Second
	}
	// RFC 2131: renew at half the lease, rebind at seven eighths.
	l.T1 = l.LeaseTime / 2
	l.T2 = l.LeaseTime / 8 * 7
	if v, ok := findOption(p, optT1); ok && len(v) == 4 {
		l.T1 = time.Duration(binary.BigEndian.Uint32(v)) * time.Second
	}
	if v, ok := findOption(p, optT2); ok && len(v) == 4 {
		l.T2 = time.Duration(binary.BigEndian.Uint32(v)) * time.Second
	}
	return l, mt[0], nil
}

// findOption returns the value of the first instance of code. A truncated
// option ends the scan rather than reading past the buffer.
func findOption(p []byte, code byte) ([]byte, bool) {
	if len(p) <= minPacketLen {
		return nil, false
	}
	for i := minPacketLen; i < len(p); {
		switch p[i] {
		case optEnd:
			return nil, false
		case 0: // pad
			i++
			continue
		}
		if i+2 > len(p) {
			return nil, false
		}
		length := int(p[i+1])
		if i+2+length > len(p) {
			return nil, false // truncated
		}
		if p[i] == code {
			return p[i+2 : i+2+length], true
		}
		i += 2 + length
	}
	return nil, false
}
