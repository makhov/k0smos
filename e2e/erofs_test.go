//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildEmbeddedRoot builds a read-only erofs root and an initramfs carrying it,
// returning the initramfs path.
//
// Built here rather than by the Makefile because this is the only test that wants
// it: the artifacts the rest of the suite shares are the ext4 ones, and a 165MB
// initramfs is not something to make every other boot pay for.
func buildEmbeddedRoot(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	goarch := runtime.GOARCH
	k0s := filepath.Join(root, "dist", "k0s-"+goarch)
	if _, err := os.Stat(k0s); err != nil {
		t.Skipf("missing %s — run 'make k0s' first", k0s)
	}

	out := filepath.Join(root, "dist", "e2e")
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	erofs := filepath.Join(out, "erofs-root.img")
	initramfs := filepath.Join(out, "erofs-initramfs.gz")
	t.Cleanup(func() { os.Remove(erofs); os.Remove(initramfs) })

	mkroot := exec.Command("./image/mkrootfs.sh", erofs)
	mkroot.Dir = root
	mkroot.Env = append(os.Environ(), "ROOTFS=erofs", "K0S_BIN=dist/k0s-"+goarch)
	if o, err := mkroot.CombinedOutput(); err != nil {
		t.Fatalf("build erofs root: %v\n%s", err, o)
	}

	mkinit := exec.Command("./image/mkinitramfs.sh", "dist/e2e/erofs-initramfs.gz")
	mkinit.Dir = root
	mkinit.Env = append(os.Environ(), "EMBED_ROOT="+erofs)
	if o, err := mkinit.CombinedOutput(); err != nil {
		t.Fatalf("build initramfs with embedded root: %v\n%s", err, o)
	}
	return initramfs
}

// The whole point of the erofs root: the kernel and the OS travel as one artifact,
// with no root disk attached at all. k0smos loop-attaches the image out of the
// initramfs, detects that it is erofs, and switch_roots onto it read-only.
//
// This is the configuration a KubeVirt VM would use with only a kernelBoot
// container — no containerDisk — so it needs to keep working before the ext4 root
// can be retired.
func TestEmbeddedEROFSRootBoots(t *testing.T) {
	initramfs := buildEmbeddedRoot(t)
	data := blankVolume(t, "erofs-data")

	// No Disk: the root comes out of the initramfs. Data at /var rather than
	// /var/lib/k0s because kubelet writes to /var/lib/kubelet, which a read-only
	// root cannot serve.
	v := boot(t, bootOpts{
		Initramfs: initramfs,
		Data:      data,
		Net:       "k0smos.ip=dhcp k0smos.dns=1.1.1.1 k0smos.data=auto k0smos.datadir=/var",
		Exec:      execNoop,
	})

	v.waitFor(`attached /k0smos-root\.img at /dev/loop\d+`, bootTimeout)
	// The filesystem is detected, not taken from the cmdline: nothing passed
	// k0smos.rootfstype here.
	v.waitFor(`holds erofs`, bootTimeout)
	v.waitFor(`mounted /dev/loop\d+ at /newroot read-only, switching root`, bootTimeout)
	v.waitFor(`starting as PID1 \(switched-root=true\)`, bootTimeout)
	// A read-only root cannot serve what k0s and cloud-init write to.
	v.waitFor(`root is read-only; overlaid \[/etc /usr/libexec\] with tmpfs`, bootTimeout)
	v.waitFor(`formatted and mounted data volume /dev/vd\w+ at /var`, bootTimeout)
	v.waitFor(`supervising`, bootTimeout)

	// Nothing may have tried and failed to write to the root. Every one of these
	// was a real failure while this was being built, so a regression would show up
	// here rather than as a node that mysteriously never goes Ready.
	if text := v.consoleText(); strings.Contains(text, "read-only file system") {
		t.Errorf("something failed writing to the read-only root:\n%s",
			extractLines(text, "read-only file system", 5))
	}
	v.stop()
}

// extractLines returns up to n lines containing want, for a readable failure.
func extractLines(text, want string, n int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, want) {
			out = append(out, line)
			if len(out) == n {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}
