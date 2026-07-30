// Package shutdown flushes and detaches writable filesystems, then hands the
// machine back to the kernel via reboot(2).
package shutdown

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Reboot commands and unmount flags, declared locally so this package builds
// and tests on non-linux dev machines. shutdown_linux_test.go asserts these
// match golang.org/x/sys/unix on the real target.
const (
	// PowerOff is LINUX_REBOOT_CMD_POWER_OFF.
	PowerOff = 0x4321fedc
	// Reboot is LINUX_REBOOT_CMD_RESTART.
	Reboot = 0x1234567

	// mntDetach is MNT_DETACH: lazy unmount, so a busy filesystem still gets
	// detached once its last user goes away instead of failing outright.
	mntDetach = 0x2

	// msRemount|msRdonly is MS_REMOUNT|MS_RDONLY, used on "/" because the root
	// filesystem cannot be unmounted.
	msRemount = 0x20
	msRdonly  = 0x1

	sigTerm = 0xf // SIGTERM
	sigKill = 0x9 // SIGKILL
)

// termGrace is how long everything gets to exit after SIGTERM before SIGKILL.
var termGrace = 5 * time.Second

// Shutdowner is the subset of *sys.Sys that shutdown needs. Mounts returns
// mount targets as strings so this package imports nothing from internal/sys.
type Shutdowner interface {
	Mounts() ([]string, error)
	Sync()
	Unmount(target string, flags int) error
	Mount(source, target, fstype string, flags uintptr, data string) error
	// KillAll signals every process except this one, i.e. kill(-1, sig).
	KillAll(sig int) error
	Reboot(cmd int) error
}

// Do flushes disks, unmounts writable filesystems (best-effort, pseudo-fs and
// "/" skipped), then issues reboot(2) with cmd. reboot(2) does not return on
// success in production; Do returns only so fakes can assert the sequence.
func Do(s Shutdowner, cmd int) error {
	// Everything must be gone before the filesystems are touched: a process
	// holding the root makes the read-only remount fail with EBUSY, which
	// leaves a journal for the next boot to replay. SIGTERM first so daemons
	// like containerd can flush, then SIGKILL for whatever ignored it.
	_ = s.KillAll(sigTerm)
	time.Sleep(termGrace)
	_ = s.KillAll(sigKill)

	s.Sync()
	targets, err := s.Mounts()
	if err != nil {
		return fmt.Errorf("read mounts: %w", err)
	}
	for _, target := range unmountOrder(targets) {
		_ = s.Unmount(target, mntDetach) // best-effort
	}
	s.Sync()

	// "/" cannot be unmounted, so remount it read-only instead. This is what
	// checkpoints the journal and clears the "needs recovery" state; syncing
	// alone leaves the image failing e2fsck even though its superblock reads
	// "clean". Best-effort: a busy root must not prevent the machine stopping.
	_ = s.Mount("", "/", "", msRemount|msRdonly, "")

	return s.Reboot(cmd)
}

// unmountOrder filters out what must not be unmounted and orders the rest
// deepest-first, so a child mount is always detached before its parent.
func unmountOrder(targets []string) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if t == "/" || isPseudo(t) {
			continue
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i], "/") > strings.Count(out[j], "/")
	})
	return out
}

// isPseudo reports whether target is a kernel pseudo-filesystem (or lives
// under one). Unmounting these buys nothing and can break the reboot path.
func isPseudo(target string) bool {
	for _, p := range []string{"/proc", "/sys", "/dev", "/run", "/tmp"} {
		if target == p || strings.HasPrefix(target, p+"/") {
			return true
		}
	}
	return false
}
