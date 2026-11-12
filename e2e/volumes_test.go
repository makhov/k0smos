//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A disk beyond the data volume -- a second NVMe on a bare-metal machine, one
// attached in libvirt -- set up from user-data the way cloud-init does it:
// fs_setup formats it once, by path, and mounts finds it by label ever after.
//
// The second disk is the safety half. It carries a GPT signature and no
// filesystem; it used to count as blank, and so as formattable. The third is
// XFS, created and then mounted by type "auto": mkfs.xfs in the image, the
// probe, and the xfs module all have to be there.
func TestCloudInitFormatsAndMountsAnExtraDisk(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	extra := blankVolume(t, "extra")
	used := blankVolume(t, "used")
	xfs := blankVolume(t, "xfs")
	writeDisk(t, extra, nil)
	writeDisk(t, used, gpt)
	writeDisk(t, xfs, nil)

	// Attached in order after the cloud-init drive: vda, then vdb, vdc, vdd.
	// XFS's smallest filesystem is 300 MiB, so that disk is bigger.
	iso := makeCidata(t, `#cloud-config
fs_setup:
  - {device: /dev/vdb, filesystem: ext4, label: e2e-extra}
  - {device: /dev/vdc, filesystem: ext4, label: e2e-used}
  - {device: /dev/vdd, filesystem: xfs, label: e2e-xfs}
mounts:
  - [LABEL=e2e-extra, /srv/extra]
  - [LABEL=e2e-xfs, /mnt/xfs, auto]
`, "")
	opts := bootOpts{Disk: disk, Cidata: iso, Exec: execNoop, Disks: []string{extra, used, xfs}}

	opts.Name = "first"
	v := boot(t, opts)
	v.waitFor(`volumes: formatted /dev/vdb as ext4, LABEL=e2e-extra`, bootTimeout)
	v.waitFor(`volumes: mounted LABEL=e2e-extra at /srv/extra`, bootTimeout)
	v.waitFor(`refusing to format /dev/vdc`, bootTimeout)
	v.waitFor(`volumes: formatted /dev/vdd as xfs, LABEL=e2e-xfs`, bootTimeout)
	v.waitFor(`volumes: mounted LABEL=e2e-xfs at /mnt/xfs`, bootTimeout)
	v.stop()

	f, err := os.Open(used)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := make([]byte, len(gpt))
	if _, err := f.ReadAt(head, 0); err != nil || !bytes.Equal(head, gpt) {
		t.Errorf("the used disk was written to: %v", err)
	}

	// Every later boot finds the filesystem and leaves it, and its data, alone.
	opts.Name = "second"
	v = boot(t, opts)
	v.waitFor(`volumes: mounted LABEL=e2e-extra at /srv/extra`, bootTimeout)
	v.waitFor(`volumes: mounted LABEL=e2e-xfs at /mnt/xfs`, bootTimeout)
	v.refute(`volumes: formatted`, `supervising`, bootTimeout)
	v.stop()
}

// gpt is the start of a disk with a GPT and nothing else: a protective MBR
// signature and the "EFI PART" header in the next sector.
var gpt = func() []byte {
	b := make([]byte, 520)
	b[510], b[511] = 0x55, 0xaa
	copy(b[512:], "EFI PART")
	return b
}()

// writeDisk creates a sparse 512 MiB raw disk image starting with head.
func writeDisk(t *testing.T, path string, head []byte) {
	t.Helper()
	if err := os.WriteFile(path, head, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 512<<20); err != nil {
		t.Fatal(err)
	}
}
