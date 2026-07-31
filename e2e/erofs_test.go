//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requireEROFSKernel skips unless the kernel in dist can mount erofs.
//
// Not every kernel can, and it is not a module away: Alpine's linux-virt leaves
// CONFIG_EROFS_FS unset entirely, while the default (Kata) kernel builds it in and
// has no squashfs at all. So a read-only root carried in the initramfs works on one
// and cannot work on the other, and this test has to say which it is looking at
// rather than time out waiting for a loop device that will never appear.
func requireEROFSKernel(t *testing.T) {
	t.Helper()
	arch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]
	kdir := filepath.Join(repoRoot(t), "dist", "kernel", arch)

	// Alpine ships its config beside the kernel; grep it when present.
	if cfg, err := os.ReadFile(filepath.Join(kdir, "config")); err == nil {
		if !strings.Contains(string(cfg), "CONFIG_EROFS_FS=y") &&
			!strings.Contains(string(cfg), "CONFIG_EROFS_FS=m") {
			t.Skip("this kernel has no erofs (CONFIG_EROFS_FS unset) — an embedded root cannot work on it")
		}
		return
	}
	// No config file means the monolithic kernel, which builds erofs in. Verified
	// against the image: it carries erofs's superblock error paths and kthreads.
}

// requireEmbeddedRoot skips unless the initramfs carries a root image.
//
// The artifacts are built by `make artifacts`, not by this test: building a 178MB
// erofs image and a 165MB initramfs per run cost more than the boot it was testing.
func requireEmbeddedRoot(t *testing.T) {
	t.Helper()
	requireArtifacts(t, "dist/k0smos-initramfs.gz")
	info, err := os.Stat(filepath.Join(repoRoot(t), "dist", "k0smos-initramfs.gz"))
	if err != nil {
		t.Skip("no initramfs")
	}
	// An initramfs with a root inside is two orders of magnitude larger than one
	// without: ~165MB against ~1.3MB. Cheaper and more honest than unpacking it.
	const withRoot = 50 << 20
	if info.Size() < withRoot {
		t.Skipf("initramfs is %dMB, so it carries no root — build it with `make artifacts`",
			info.Size()>>20)
	}
}

// The whole point of the erofs root: the kernel and the OS travel as one artifact,
// with no root disk attached at all. k0smos loop-attaches the image out of the
// initramfs, detects that it is erofs, and switch_roots onto it read-only.
//
// This is the configuration a KubeVirt VM uses with only a kernelBoot container and
// no containerDisk, which is the default this repository now builds.
func TestEmbeddedEROFSRootBoots(t *testing.T) {
	requireEROFSKernel(t)
	requireEmbeddedRoot(t)
	data := blankVolume(t, "erofs-data")

	// No Disk and no Root: with neither named, k0smos uses the image it carries.
	// Data at /var rather than /var/lib/k0s because kubelet writes /var/lib/kubelet,
	// which a read-only root cannot serve.
	v := boot(t, bootOpts{
		Data: data,
		Net:  "k0smos.ip=dhcp k0smos.dns=1.1.1.1 k0smos.data=auto",
		Exec: execNoop,
	})

	v.waitFor(`attached /k0smos-root\.img at /dev/loop\d+`, bootTimeout)
	// Detected, not taken from the cmdline: nothing passed k0smos.rootfstype here.
	v.waitFor(`holds erofs`, bootTimeout)
	v.waitFor(`mounted /dev/loop\d+ at /newroot read-only, switching root`, bootTimeout)
	v.waitFor(`starting as PID1 \(switched-root=true\)`, bootTimeout)
	// A read-only root cannot serve what k0s and cloud-init write to.
	v.waitFor(`root is read-only; overlaid \[/etc /usr/libexec\] with tmpfs`, bootTimeout)
	v.waitFor(`formatted and mounted data volume /dev/vd\w+ at /var`, bootTimeout)
	v.waitFor(`supervising`, bootTimeout)

	// Nothing may have tried and failed to write to the root. Every one of these was
	// a real failure while this was being built, so a regression shows up here rather
	// than as a node that mysteriously never goes Ready.
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
