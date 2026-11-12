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
// The second disk is the safety half. It carries an XFS signature, which no
// probe here recognises; it used to count as blank, and so as formattable.
func TestCloudInitFormatsAndMountsAnExtraDisk(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	extra := blankVolume(t, "extra")
	used := blankVolume(t, "used")
	writeDisk(t, extra, nil)
	writeDisk(t, used, []byte("XFSB"))

	// Attached in order after the cloud-init drive: vda, then vdb and vdc.
	iso := makeCidata(t, `#cloud-config
fs_setup:
  - {device: /dev/vdb, filesystem: ext4, label: e2e-extra}
  - {device: /dev/vdc, filesystem: ext4, label: e2e-used}
mounts:
  - [LABEL=e2e-extra, /srv/extra]
`, "")
	opts := bootOpts{Disk: disk, Cidata: iso, Exec: execNoop, Disks: []string{extra, used}}

	opts.Name = "first"
	v := boot(t, opts)
	v.waitFor(`volumes: formatted /dev/vdb as ext4, LABEL=e2e-extra`, bootTimeout)
	v.waitFor(`volumes: mounted LABEL=e2e-extra at /srv/extra`, bootTimeout)
	v.waitFor(`refusing to format /dev/vdc`, bootTimeout)
	v.stop()

	head := make([]byte, 4)
	f, err := os.Open(used)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.ReadAt(head, 0); err != nil || !bytes.Equal(head, []byte("XFSB")) {
		t.Errorf("the used disk was written to: starts %q, %v", head, err)
	}

	// Every later boot finds the filesystem and leaves it, and its data, alone.
	opts.Name = "second"
	v = boot(t, opts)
	v.waitFor(`volumes: mounted LABEL=e2e-extra at /srv/extra`, bootTimeout)
	v.refute(`volumes: formatted`, `supervising`, bootTimeout)
	v.stop()
}

// writeDisk creates a 64 MiB raw disk image starting with head.
func writeDisk(t *testing.T, path string, head []byte) {
	t.Helper()
	if err := os.WriteFile(path, head, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 64<<20); err != nil {
		t.Fatal(err)
	}
}
