//go:build e2e

package e2e

import "testing"

// Taken from a kata console in CI, where TestEmbeddedEROFSRootBoots timed out
// waiting for a line the guest had printed, split by two kernel messages.
func TestUntangleRejoinsLinesSplitByKernelMessages(t *testing.T) {
	in := "k0smos: no module tree\r\n" +
		"k0smos: ho[    1.053015] loop0: detected capacity change from 0 to 401936\r\n" +
		"st filesystems mounted\r\n" +
		"k0smos: attached /k0smos[    1.054466] erofs (device loop0): mounted with root inode @ nid 36.\r\n" +
		"-root.img at /dev/loop0\r\n" +
		"k0smos: ho[    1.061733] overlayfs: one\r\n" +
		"st filesystems m[    1.062914] overlayfs: two\r\n" +
		"ounted\r\n" +
		"[    1.086127] EXT4-fs (vda): mounted filesystem\r\n" +
		"k0smos: done"
	want := "k0smos: no module tree\n" +
		"[    1.053015] loop0: detected capacity change from 0 to 401936\n" +
		"k0smos: host filesystems mounted\n" +
		"[    1.054466] erofs (device loop0): mounted with root inode @ nid 36.\n" +
		"k0smos: attached /k0smos-root.img at /dev/loop0\n" +
		"[    1.061733] overlayfs: one\n" +
		"[    1.062914] overlayfs: two\n" +
		"k0smos: host filesystems mounted\n" +
		"[    1.086127] EXT4-fs (vda): mounted filesystem\n" +
		"k0smos: done"
	if got := untangle(in); got != want {
		t.Errorf("untangle:\n got: %q\nwant: %q", got, want)
	}
}
