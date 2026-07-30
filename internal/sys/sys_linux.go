//go:build linux

package sys

import (
	"bytes"
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

func (s *Sys) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// BlockDevices lists block device names from sysfs. sysfs is used rather than
// /dev/disk/by-* because those symlinks come from udev, which k0smos does not
// run; the device nodes in /dev come from devtmpfs and do exist.
func (s *Sys) BlockDevices() ([]string, error) {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// ReadAt fills p from /dev/<dev> at off.
func (s *Sys) ReadAt(dev string, p []byte, off int64) error {
	f, err := os.Open("/dev/" + dev)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.ReadAt(p, off)
	return err
}

func (s *Sys) Chdir(dir string) error  { return unix.Chdir(dir) }
func (s *Sys) Chroot(dir string) error { return unix.Chroot(dir) }

// Exec replaces this process image. On success it does not return.
func (s *Sys) Exec(argv0 string, argv, env []string) error {
	return unix.Exec(argv0, argv, env)
}

// InitModule loads a decompressed kernel module image via init_module(2).
func (s *Sys) InitModule(image []byte, params string) error {
	return unix.InitModule(image, params)
}

// Release is the running kernel's release string, e.g. "6.6.142-0-virt". It
// names the /lib/modules subdirectory holding that kernel's modules.
func (s *Sys) Release() (string, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(u.Release[:], "\x00")), nil
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

// KillAll signals every process except this one. kill(-1, sig) as PID1 reaches
// everything else on the machine, which is how an init clears the way before
// unmounting.
func (s *Sys) KillAll(sig int) error { return unix.Kill(-1, unix.Signal(sig)) }

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
