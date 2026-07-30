//go:build linux

package sys

import (
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// DHCPConn is a UDP socket suitable for DHCP before the interface has an
// address. Three options make that possible:
//
//   - SO_BINDTODEVICE ties the socket to one interface, which lets the kernel
//     send to 255.255.255.255 with source 0.0.0.0 and no route present;
//   - SO_BROADCAST permits the broadcast destination;
//   - SO_REUSEADDR allows binding port 68 even if it lingers from a prior boot.
type DHCPConn struct{ fd int }

// DHCPConn opens a DHCP socket on iface.
func (s *Sys) DHCPConn(iface string) (*DHCPConn, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_UDP)
	if err != nil {
		return nil, err
	}
	closeOnErr := func(err error) (*DHCPConn, error) {
		unix.Close(fd)
		return nil, err
	}
	for _, opt := range []int{unix.SO_REUSEADDR, unix.SO_BROADCAST} {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, opt, 1); err != nil {
			return closeOnErr(err)
		}
	}
	if err := unix.SetsockoptString(fd, unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface); err != nil {
		return closeOnErr(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrInet4{Port: 68}); err != nil {
		return closeOnErr(err)
	}
	return &DHCPConn{fd: fd}, nil
}

// SendTo sends p to dst on the DHCP server port.
func (c *DHCPConn) SendTo(p []byte, dst net.IP) error {
	var addr unix.SockaddrInet4
	addr.Port = 67
	copy(addr.Addr[:], dst.To4())
	return unix.Sendto(c.fd, p, 0, &addr)
}

// Recv reads one datagram, returning os.ErrDeadlineExceeded if none arrives
// within timeout.
func (c *DHCPConn) Recv(p []byte, timeout time.Duration) (int, error) {
	tv := unix.NsecToTimeval(int64(timeout))
	if err := unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return 0, err
	}
	n, _, err := unix.Recvfrom(c.fd, p, 0)
	if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
		// Normalised so callers need not know about errno values.
		return 0, os.ErrDeadlineExceeded
	}
	return n, err
}

func (c *DHCPConn) Close() error { return unix.Close(c.fd) }

// InterfaceMAC reads an interface's hardware address from sysfs. sysfs rather
// than an ioctl because x/sys/unix exposes no hardware-address accessor on
// Ifreq, and this needs no udev.
func (s *Sys) InterfaceMAC(iface string) (net.HardwareAddr, error) {
	b, err := os.ReadFile("/sys/class/net/" + iface + "/address")
	if err != nil {
		return nil, err
	}
	return net.ParseMAC(strings.TrimSpace(string(b)))
}
