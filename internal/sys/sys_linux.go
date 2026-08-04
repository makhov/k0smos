//go:build linux

package sys

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

// MkdirAll, Chmod, Chown and Symlink carry out the file operations interpreted
// from cloud-init runcmd (see internal/metadata). They exist so k0smos can
// honour those entries without exec'ing coreutils it does not ship.
func (s *Sys) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (s *Sys) Chmod(path string, mode os.FileMode) error    { return os.Chmod(path, mode) }
func (s *Sys) Chown(path string, uid, gid int) error        { return os.Chown(path, uid, gid) }
func (s *Sys) Symlink(target, link string) error            { return os.Symlink(target, link) }

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

// modaliasRoot is where the kernel exposes every device it has enumerated. Each
// device that can be driven by a module carries a "modalias" file naming what it
// is, in the same syntax as the patterns in modules.alias.
const modaliasRoot = "/sys/devices"

// Modaliases returns the modalias of every enumerated device, deduplicated.
//
// This is the input to module.LoadForDevices: matching these against
// modules.alias is how a driver is found for hardware nobody listed in advance,
// and is what udev does. There is no udev here, so k0smos walks sysfs itself.
//
// Errors on individual files are ignored: sysfs is full of entries that vanish
// mid-walk or refuse to be read, and none of that should stop the others being
// found.
func (s *Sys) Modaliases() ([]string, error) {
	var out []string
	seen := map[string]bool{}

	err := filepath.WalkDir(modaliasRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that disappeared or cannot be entered: skip it, keep
			// walking. Returning the error would abandon the whole tree.
			return nil //nolint:nilerr // deliberate: partial results beat none
		}
		if d.IsDir() || d.Name() != "modalias" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if v := strings.TrimSpace(string(data)); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, nil
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

// Mkfs creates a filesystem on a device.
//
// This runs a bundled binary, which k0smos otherwise avoids — but like the etcd
// leave it is k0smos's own decision with a fixed command, not something taken
// from user-data. The caller must have established that the device is blank:
// mkfs on a populated volume destroys it.
func (s *Sys) Mkfs(dev, fstype, label string) error {
	out, err := exec.Command("mkfs."+fstype, "-q", "-L", label, dev).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs.%s %s: %w: %s", fstype, dev, err, bytes.TrimSpace(out))
	}
	return nil
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

// KernelLog returns the kernel ring buffer, the same content dmesg prints.
//
// klogctl rather than reading /dev/kmsg: that is a stream which blocks once
// drained, so serving it to a host request would hang. SYSLOG_ACTION_READ_ALL
// returns what is buffered and stops.
func (s *Sys) KernelLog() ([]byte, error) {
	const readAll = 3 // SYSLOG_ACTION_READ_ALL
	// Ask the kernel how much it is holding rather than guessing a size.
	size, err := unix.Klogctl(10 /* SYSLOG_ACTION_SIZE_BUFFER */, nil)
	if err != nil || size <= 0 {
		size = 256 << 10
	}
	buf := make([]byte, size)
	n, err := unix.Klogctl(readAll, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

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

// IsReadOnly reports whether the filesystem holding path is mounted read-only.
//
// Used to decide whether the writable-path overlays are needed: an ext4 root
// serves them itself, an erofs root cannot.
func (s *Sys) IsReadOnly(path string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false, err
	}
	return st.Flags&unix.ST_RDONLY != 0, nil
}

// loopControl is the device that hands out free loop devices.
const loopControl = "/dev/loop-control"

// LoopAttach backs a free loop device with the file at path and returns its device
// node, e.g. "/dev/loop0".
//
// This is what lets the root filesystem travel inside the initramfs: an erofs image
// there is a file, and mount(2) will not take a file — the "-o loop" that mount(8)
// accepts is userspace work, which is why k0smos has to do it itself.
//
// readOnly matters for erofs, whose images are read-only by construction: attaching
// them writable and then mounting fails with EACCES.
func (s *Sys) LoopAttach(path string, readOnly bool) (string, error) {
	flags := os.O_RDWR
	if readOnly {
		flags = os.O_RDONLY
	}
	backing, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return "", err
	}
	defer backing.Close()

	ctl, err := os.OpenFile(loopControl, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", loopControl, err)
	}
	defer ctl.Close()

	// The kernel allocates the device; picking a number ourselves would race with
	// anything else doing the same.
	n, err := unix.IoctlRetInt(int(ctl.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return "", fmt.Errorf("allocate loop device: %w", err)
	}
	dev := fmt.Sprintf("/dev/loop%d", n)

	loop, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", dev, err)
	}
	defer loop.Close()

	cfg := unix.LoopConfig{Fd: uint32(backing.Fd())}
	if readOnly {
		cfg.Info.Flags = unix.LO_FLAGS_READ_ONLY
	}
	// LOOP_CONFIGURE rather than LOOP_SET_FD plus LOOP_SET_STATUS64: it sets the
	// backing file and its flags in one call, so the device is never briefly
	// attached with the wrong ones.
	if err := unix.IoctlLoopConfigure(int(loop.Fd()), &cfg); err != nil {
		return "", fmt.Errorf("configure %s: %w", dev, err)
	}
	return dev, nil
}
