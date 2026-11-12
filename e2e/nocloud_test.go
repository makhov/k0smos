//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amakhov/k0smos/internal/iso9660"
)

// A NoCloud seed as libvirt usually attaches one: labelled CIDATA -- upper case,
// as an ISO writer that follows the standard makes it -- on a cdrom on
// virtio-scsi, carrying a network-config that gives the machine a static
// address. The hardest common shape: a /32 whose gateway is reachable only
// through a link-scoped route, an MTU, two nameservers. The cmdline says DHCP
// and names another resolver; network-config wins.
func TestNoCloudSeedOnACDROMConfiguresTheNetwork(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	iso := filepath.Join(repoRoot(t), "dist", "e2e", sanitise(t.Name())+".iso")
	t.Cleanup(func() { os.Remove(iso) })
	f, err := os.Create(iso)
	if err != nil {
		t.Fatal(err)
	}
	err = iso9660.Write(f, "CIDATA", []iso9660.File{
		{Name: "user-data", Data: []byte("#cloud-config\nhostname: ignored-by-k0smos\n")},
		{Name: "meta-data", Data: []byte("instance-id: i-seed\nlocal-hostname: from-the-seed\n")},
		{Name: "network-config", Data: []byte(`network:
  version: 2
  ethernets:
    eth0:
      mtu: 1400
      addresses:
        - 10.0.2.15/32
      routes:
        - to: default
          via: 10.0.2.2
          from: 10.0.2.15
          mtu: 1400
        - to: 10.0.2.2/32
          scope: link
      nameservers:
        addresses:
          - 10.0.2.3
          - 1.1.1.1
`)},
	})
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, CidataCD: true, Exec: execNoop,
		Net: "k0smos.ip=dhcp k0smos.dns=9.9.9.9"})
	// Kata's monolithic guest kernel has no CD-ROM driver at all; a seed on a
	// cdrom needs one with sr_mod, which the metal image's kernel has.
	if got := v.waitForAny(bootTimeout, `hostname set to "from-the-seed"`, `no module tree; assuming a monolithic kernel`); strings.Contains(got, "monolithic") {
		v.stop()
		t.Skip("this kernel has no CD-ROM driver; run with a modular kernel (make kernel-alpine)")
	}
	v.waitFor(`eth0 configured 10\.0\.2\.15/32 gw 10\.0\.2\.2`, bootTimeout)
	v.refute(`warn: (configure|network-config|dhcp)`, `supervising`, bootTimeout)

	for path, want := range map[string]string{
		"/etc/resolv.conf":        "nameserver 10.0.2.3\nnameserver 1.1.1.1\n",
		"/sys/class/net/eth0/mtu": "1400",
		"/proc/net/route":         "0202000A", // 10.0.2.2, the gateway, little-endian hex
	} {
		// Retried: on macOS hosts the control channel itself drops requests,
		// which is not what this test is about. A wrong value still fails.
		got, err := requestNode(t, v, "cat "+path)
		for try := 0; err != nil && try < 3; try++ {
			got, err = requestNode(t, v, "cat "+path)
		}
		if err != nil {
			t.Errorf("cat %s: %v", path, err)
		} else if !strings.Contains(string(got), want) {
			t.Errorf("%s = %q, want it to contain %q", path, got, want)
		}
	}
	v.stop()
}
