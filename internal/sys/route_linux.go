//go:build linux

package sys

import (
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// rtentry mirrors struct rtentry from <linux/route.h>, which x/sys/unix does
// not expose. Only the gateway, destination and flags are used; the pad fields
// exist to reproduce the kernel's layout exactly. Valid for LP64 (amd64,
// arm64), which are the only targets k0smos builds for.
type rtentry struct {
	pad1    uint64
	dst     unix.RawSockaddrInet4
	gateway unix.RawSockaddrInet4
	genmask unix.RawSockaddrInet4
	flags   uint16
	pad2    int16
	pad3    uint64
	pad4    uintptr
	metric  int16
	dev     *byte
	mtu     uint64
	window  uint64
	irtt    uint16
}

const (
	rtfUp      = 0x0001 // RTF_UP
	rtfGateway = 0x0002 // RTF_GATEWAY
)

// SetAddr assigns an IPv4 address and netmask to an interface.
func (s *Sys) SetAddr(iface string, ip net.IP, mask net.IPMask) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	for _, step := range []struct {
		req  uint
		addr []byte
	}{
		{unix.SIOCSIFADDR, ip.To4()},
		{unix.SIOCSIFNETMASK, net.IP(mask).To4()},
	} {
		ifr, err := unix.NewIfreq(iface)
		if err != nil {
			return err
		}
		if err := ifr.SetInet4Addr(step.addr); err != nil {
			return err
		}
		if err := unix.IoctlIfreq(fd, step.req, ifr); err != nil {
			return err
		}
	}
	return nil
}

// AddDefaultRoute installs a default route via gw (0.0.0.0/0). The destination
// and genmask are left zeroed, which is what makes the route a default one.
func (s *Sys) AddDefaultRoute(gw net.IP) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	rt := rtentry{flags: rtfUp | rtfGateway}
	rt.dst.Family = unix.AF_INET
	rt.genmask.Family = unix.AF_INET
	rt.gateway.Family = unix.AF_INET
	copy(rt.gateway.Addr[:], gw.To4())

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.SIOCADDRT),
		uintptr(unsafe.Pointer(&rt)))
	if errno != 0 {
		return errno
	}
	return nil
}
