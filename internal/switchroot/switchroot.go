// Package switchroot replaces the initramfs with a real root filesystem.
//
// This exists because kubelet cannot run on an initramfs: cadvisor asks the
// kernel for filesystem stats about the root device and a ramfs root reports
// none, so kubelet dies with "failed to get rootfs info: cannot find
// filesystem info for device \"rootfs\"". Booting from the initramfs is still
// how we get here — a stock kernel needs virtio_blk and ext4 loaded as modules
// before it can see the real root at all.
package switchroot

import (
	"fmt"
	"os"
)

// msMove is MS_MOVE, declared locally so this package builds and tests on
// non-linux dev machines. switchroot_linux_test.go asserts it matches
// golang.org/x/sys/unix on the real target.
const msMove = 0x2000

// kernelMounts are the pseudo-filesystems that must come along to the new root.
// Re-mounting them afterwards would work too, but moving preserves the open
// file descriptors the kernel and any already-running process hold.
var kernelMounts = []string{"/dev", "/proc", "/sys"}

// Switcher is the subset of *sys.Sys that switching root needs.
type Switcher interface {
	Mount(source, target, fstype string, flags uintptr, data string) error
	Chdir(dir string) error
	Chroot(dir string) error
	Exec(argv0 string, argv []string, env []string) error
}

// Do makes newRoot the root filesystem and executes init inside it, replacing
// the current process. newRoot must already be mounted.
//
// On success Do does not return — the process is replaced. It returns an error
// only when the switch could not be completed.
func Do(s Switcher, newRoot, init string, args []string) error {
	// Best-effort: a kernel filesystem that is not mounted yet, or whose mount
	// point is missing in the new root, must not abort the switch. The init
	// running after the switch mounts anything that did not come across.
	for _, m := range kernelMounts {
		_ = s.Mount(m, newRoot+m, "", msMove, "")
	}

	if err := s.Chdir(newRoot); err != nil {
		return fmt.Errorf("chdir %s: %w", newRoot, err)
	}
	// Move the new root over "/" — after this the initramfs is unreachable and
	// its memory can be reclaimed.
	if err := s.Mount(".", "/", "", msMove, ""); err != nil {
		return fmt.Errorf("move %s to /: %w", newRoot, err)
	}
	if err := s.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := s.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	if err := s.Exec(init, args, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", init, err)
	}
	// Unreachable in production: a successful execve does not return.
	return nil
}
