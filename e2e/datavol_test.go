//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amakhov/k0smos/internal/gpt"
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
	v1.waitFor(`formatted and mounted data volume /dev/vd\w+ at /var`, bootTimeout)
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

// What a platform does to a VM's volume -- an RBD resize, qemu-img resize, a
// bigger disk under the metal image: the disk grows and the data partition, last
// on its GPT, must grow into it, with the filesystem, while mounted. The table
// is checked afterwards on the host, and the third boot must find nothing to do.
func TestDataVolumeGrowsIntoItsDisk(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	// One partition from 1 MiB to the end of a 256 MiB disk. Attached before the
	// root disk as the data volume, it is vda.
	data := blankVolume(t, "data")
	f, err := os.Create(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(256 << 20); err != nil {
		t.Fatal(err)
	}
	if err := gpt.Create(f, 256<<20, 512, [2]uint64{2048, 256<<20/512 - 34}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	opts := bootOpts{Disk: disk, Data: data, Net: "k0smos.ip=dhcp k0smos.data=/dev/vda1", Exec: execNoop}

	opts.Name = "first"
	v := boot(t, opts)
	v.waitFor(`formatted and mounted data volume /dev/vda1 at /var`, bootTimeout)
	v.refute(`grew`, `supervising`, bootTimeout)
	v.stop()

	if err := os.Truncate(data, 1<<30); err != nil {
		t.Fatal(err)
	}
	opts.Name = "grown"
	v = boot(t, opts)
	v.waitFor(`grew partition 1 of vda from 0\.2 GiB to 1\.0 GiB`, bootTimeout)
	v.waitFor(`grew the filesystem at /var from 0\.2 GiB to 1\.0 GiB`, bootTimeout)
	v.stop()

	// The table the guest wrote is valid and already reaches the end.
	f, err = os.OpenFile(data, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if res, err := gpt.Grow(f, 1<<30, 512, 1); err != nil || res.Grown() {
		t.Errorf("table after the guest grew it: %+v, %v; want valid and full", res, err)
	}

	opts.Name = "again"
	v = boot(t, opts)
	v.refute(`grew`, `supervising`, bootTimeout)
	v.stop()
}
