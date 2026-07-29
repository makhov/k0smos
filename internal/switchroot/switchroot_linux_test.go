//go:build linux

package switchroot

import (
	"testing"

	"golang.org/x/sys/unix"
)

// msMove is declared locally to keep this package testable off-linux. Verify it
// is the genuine flag on the real target — a wrong value would silently mount
// instead of move, leaving the initramfs as the root.
func TestMsMoveMatchesUnix(t *testing.T) {
	if msMove != unix.MS_MOVE {
		t.Errorf("msMove = %#x, want %#x", msMove, unix.MS_MOVE)
	}
}
