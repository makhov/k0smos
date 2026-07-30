//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// The Talos-style split: an interchangeable root plus a separate volume holding
// everything mutable. This is the test that matters most, because it boots twice
// against the same volume — the first boot must format it, the second must reuse
// it untouched. Reformatting on every boot would silently destroy a cluster's
// etcd while looking perfectly healthy in the logs.
func TestDataVolumeFormatsOnceThenReuses(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	data := blankVolume(t, "data")

	// First boot: the volume is blank, so k0smos formats and mounts it.
	first := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))
	v1 := boot(t, bootOpts{Disk: first, Data: data, Net: "k0smos.ip=dhcp k0smos.data=auto", Exec: execNoop})
	v1.waitFor(`formatted and mounted data volume /dev/vd\w+ at /var/lib/k0s`, bootTimeout)
	v1.waitFor(`supervising`, bootTimeout)
	v1.stop()

	// Prove the filesystem outlived the machine before relying on the reuse claim.
	if got := debugfsCmd(t, data, "ls -l /"); !strings.Contains(got, "lost+found") {
		t.Fatalf("data volume has no filesystem after the first boot:\n%s", got)
	}

	// Second boot, same volume, fresh root. It must be found by label and left
	// alone.
	second := cloneDisk2(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))
	v2 := boot(t, bootOpts{Disk: second, Data: data, Net: "k0smos.ip=dhcp k0smos.data=auto", Exec: execNoop})
	v2.waitFor(`supervising`, bootTimeout)

	text := v2.consoleText()
	if strings.Contains(text, "formatted and mounted data volume") {
		t.Error("reformatted an existing data volume — this would destroy a cluster")
	}
	if !strings.Contains(text, "mounted data volume") {
		t.Errorf("did not mount the existing volume:\n%s", v2.k0smosLines())
	}
}

// With no data volume attached, the machine must still boot: k0s then uses the
// root filesystem, which is the behaviour before this feature existed.
func TestNoDataVolumeStillBoots(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	v := boot(t, bootOpts{Disk: disk, Net: "k0smos.ip=dhcp k0smos.data=auto", Exec: execNoop})
	v.waitFor(`supervising`, bootTimeout)
	if strings.Contains(v.consoleText(), "data volume") {
		t.Errorf("acted on a data volume that is not attached:\n%s", v.k0smosLines())
	}
}

// The cloud-init drive has a filesystem, so it must never be mistaken for a
// blank data volume and formatted.
func TestDataVolumeAutoIgnoresTheCloudInitDrive(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))
	data := blankVolume(t, "data")

	iso := makeCidata(t, "#cloud-config\nwrite_files:\n  - path: /etc/marker\n    content: x\n", "")

	v := boot(t, bootOpts{
		Disk: disk, Cidata: iso, Data: data,
		Net: "k0smos.ip=dhcp k0smos.data=auto", Exec: execNoop,
	})
	v.waitFor(`formatted and mounted data volume`, bootTimeout)
	v.waitFor(`wrote 1 file\(s\) from user-data`, bootTimeout)
	v.stop()

	// The cidata ISO must still be readable, i.e. it was not the thing formatted.
	if got := debugfsCmd(t, data, "ls -l /"); !strings.Contains(got, "lost+found") {
		t.Errorf("the data volume is not what got formatted:\n%s", got)
	}
}
