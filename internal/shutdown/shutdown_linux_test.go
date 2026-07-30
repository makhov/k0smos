//go:build linux

package shutdown

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The package declares the reboot/unmount numbers itself so it stays portable
// for dev-machine tests. On the real target, verify they are the genuine ABI
// values — a mismatch here would mean a wrong syscall at shutdown.
func TestConstantsMatchUnix(t *testing.T) {
	if PowerOff != unix.LINUX_REBOOT_CMD_POWER_OFF {
		t.Errorf("PowerOff = %#x, want %#x", PowerOff, unix.LINUX_REBOOT_CMD_POWER_OFF)
	}
	if Reboot != unix.LINUX_REBOOT_CMD_RESTART {
		t.Errorf("Reboot = %#x, want %#x", Reboot, unix.LINUX_REBOOT_CMD_RESTART)
	}
	if mntDetach != unix.MNT_DETACH {
		t.Errorf("mntDetach = %#x, want %#x", mntDetach, unix.MNT_DETACH)
	}
	if msRemount != unix.MS_REMOUNT {
		t.Errorf("msRemount = %#x, want %#x", msRemount, unix.MS_REMOUNT)
	}
	if msRdonly != unix.MS_RDONLY {
		t.Errorf("msRdonly = %#x, want %#x", msRdonly, unix.MS_RDONLY)
	}
}
