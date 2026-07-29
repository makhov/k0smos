//go:build linux

package sys

import (
	"os"

	"golang.org/x/sys/unix"
)

type Sys struct{}

func New() *Sys { return &Sys{} }

func (s *Sys) Getpid() int { return os.Getpid() }

func (s *Sys) Mount(source, target, fstype string, flags uintptr, data string) error {
	return unix.Mount(source, target, fstype, flags, data)
}

func (s *Sys) Unmount(target string, flags int) error { return unix.Unmount(target, flags) }

func (s *Sys) Mkdir(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (s *Sys) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (s *Sys) Mounts() ([]MountPoint, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseMountInfo(data)
}

// MountTargets returns just the mount targets, for shutdown unmounting.
func (s *Sys) MountTargets() ([]string, error) {
	mps, err := s.Mounts()
	if err != nil {
		return nil, err
	}
	return targetsOf(mps), nil
}

func (s *Sys) Sethostname(name string) error { return unix.Sethostname([]byte(name)) }

func (s *Sys) Sync() { unix.Sync() }

func (s *Sys) Reboot(cmd int) error { return unix.Reboot(cmd) }

// Reap collects one exited child. ok=false means "no child ready right now".
func (s *Sys) Reap() (int, bool, error) {
	var ws unix.WaitStatus
	pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
	if err == unix.ECHILD {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if pid <= 0 {
		return 0, false, nil
	}
	return pid, true, nil
}

// LinkUp brings a network interface up via ioctl SIOCSIFFLAGS.
func (s *Sys) LinkUp(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}
