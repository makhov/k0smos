//go:build e2e

package e2e

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

// The init-only path: no k0s, so this is the fast check that the whole boot
// sequence still works. It is the one to run while changing anything in
// cmd/k0smos.
func TestInitOnlyBootCompletesSequence(t *testing.T) {
	requireArtifacts(t, "dist/k0smos-initramfs.gz")
	v := boot(t, bootOpts{Exec: "/init", Mem: "1024"})

	v.waitFor(`k0smos: starting as PID1`, bootTimeout)
	for _, want := range []string{
		`pseudo-filesystems mounted`,
		`kernel modules loaded from /lib/modules/`,
		`cgroup2 hierarchy ready`,
		`loopback up`,
		`supervising \[/init\]`,
	} {
		v.waitFor(want, bootTimeout)
	}
	// A module that fails to load is logged; none should here.
	if strings.Contains(v.k0smosLines(), "warn: modules:") {
		t.Errorf("module loading reported a problem:\n%s", v.k0smosLines())
	}
}

// switch_root onto the real root, found by label rather than device path. The
// label indirection is what survives a disk reordering, which happens as soon as
// a second disk is attached.
func TestSwitchRootByLabel(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	v := boot(t, bootOpts{Disk: disk, Root: "LABEL=k0smos", Exec: execNoop})
	v.waitFor(`resolved LABEL=k0smos to /dev/vd`, bootTimeout)
	v.waitFor(`switching root`, bootTimeout)
	v.waitFor(`starting as PID1 \(switched-root=true\)`, bootTimeout)
	v.waitFor(`supervising`, bootTimeout)
}

// A cloud-init drive shifts the root disk's device name, which is the real-world
// case the label lookup exists for.
func TestCloudInitWriteFilesHostnameAndDeviceShift(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	iso := makeCidata(t, `#cloud-config
write_files:
  - path: /etc/k0s/plain.txt
    permissions: "0640"
    content: |
      plain content
  - path: /etc/k0s/secret
    permissions: "0600"
    encoding: b64
    content: `+base64.StdEncoding.EncodeToString([]byte("decoded-secret"))+`
`, "instance-id: i-e2e\nlocal-hostname: e2e-node\n")

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Exec: execNoop})
	v.waitFor(`mounted /dev/vd\w+ \(iso9660, LABEL=cidata\)`, bootTimeout)
	v.waitFor(`wrote 2 file\(s\) from user-data`, bootTimeout)
	// meta-data must win over the k0smos.hostname= default.
	v.waitFor(`hostname set to "e2e-node"`, bootTimeout)
	v.waitFor(`supervising`, bootTimeout)
	v.stop()

	ls := debugfsCmd(t, disk, "ls -l /etc/k0s")
	if !strings.Contains(ls, "100640") {
		t.Errorf("plain.txt mode missing from:\n%s", ls)
	}
	if !strings.Contains(ls, "100600") {
		t.Errorf("secret mode missing from:\n%s", ls)
	}
	if got := debugfsCmd(t, disk, "cat /etc/k0s/secret"); !strings.Contains(got, "decoded-secret") {
		t.Errorf("base64 content not decoded on disk: %q", got)
	}
}

// runcmd is interpreted, never executed. The file verbs must take effect via
// syscalls, and anything else must be refused rather than run.
func TestCloudInitRuncmdIsInterpretedNotExecuted(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	iso := makeCidata(t, `#cloud-config
runcmd:
  - [mkdir, -p, /var/lib/e2e/deep/path]
  - [chmod, "0700", /var/lib/e2e]
  - [ln, -s, /etc/k0s, /var/lib/e2e/link]
  - [curl, -o, /tmp/x, https://example.com]
  - [systemctl, enable, k0s]
  - k0s status | grep Running
`, "")

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Exec: execNoop})
	v.waitFor(`applied 3 file action\(s\) from runcmd`, bootTimeout)
	v.waitFor(`UNSUPPORTED runcmd \[curl`, bootTimeout)
	v.waitFor(`needs a shell, skipped`, bootTimeout)
	v.waitFor(`supervising`, bootTimeout)

	// systemctl is dropped silently: it is expected, not a problem to report.
	if strings.Contains(v.k0smosLines(), "UNSUPPORTED runcmd [systemctl") {
		t.Error("systemctl should be dropped quietly, not reported as unsupported")
	}
	v.stop()

	if got := debugfsCmd(t, disk, "ls -l /var/lib/e2e/deep"); !strings.Contains(got, "path") {
		t.Errorf("mkdir -p did not create the deep path:\n%s", got)
	}
	if got := debugfsCmd(t, disk, "ls -l /var/lib"); !strings.Contains(got, "40700") {
		t.Errorf("chmod 0700 not applied:\n%s", got)
	}
	if got := debugfsCmd(t, disk, "ls -l /var/lib/e2e"); !strings.Contains(got, "120777") {
		t.Errorf("symlink not created:\n%s", got)
	}
}

// Kubernetes resources ship as files, gzip-compressed, and k0s applies them
// from its manifest directory. This is the deploy path that needs no shell.
func TestCloudInitGzippedManifestIsWritten(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	manifest := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: e2e-demo\n"
	iso := makeCidata(t, `#cloud-config
write_files:
  - path: /var/lib/k0s/manifests/e2e-demo/ns.yaml
    permissions: "0644"
    encoding: gzip+base64
    content: `+gzipBase64(t, manifest)+`
`, "")

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Exec: execNoop})
	v.waitFor(`wrote 1 file\(s\) from user-data`, bootTimeout)
	v.refute(`unsupported encoding`, `supervising`, bootTimeout)
	v.stop()

	got := debugfsCmd(t, disk, "cat /var/lib/k0s/manifests/e2e-demo/ns.yaml")
	if !strings.Contains(got, "kind: Namespace") || !strings.Contains(got, "e2e-demo") {
		t.Errorf("gzipped manifest not decoded on disk:\n%s", got)
	}
}

// The config-drive layout, which is what Cluster API on KubeVirt actually
// attaches (CAPK uses CloudInitConfigDrive, so label "config-2" and
// openstack/latest/ paths rather than NoCloud's "cidata" and /user-data).
func TestConfigDriveLayoutIsConsumed(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	iso := makeConfigDrive(t, `#cloud-config
write_files:
  - path: /etc/k0s/from-config-drive
    permissions: "0600"
    content: |
      arrived via openstack/latest/user_data
`, `{"uuid":"i-cd-1","hostname":"cd-node"}`)

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Exec: execNoop})
	v.waitFor(`mounted /dev/vd\w+ \((iso9660|vfat), LABEL=config-2\)`, bootTimeout)
	v.waitFor(`wrote 1 file\(s\) from user-data`, bootTimeout)
	// meta_data.json spells the hostname differently from NoCloud's meta-data.
	v.waitFor(`hostname set to "cd-node"`, bootTimeout)
	v.stop()

	if got := debugfsCmd(t, disk, "cat /etc/k0s/from-config-drive"); !strings.Contains(got, "openstack/latest") {
		t.Errorf("config-drive user_data not applied: %q", got)
	}
}

// DHCP against QEMU's built-in server, which hands out the same addresses a real
// network would supply.
func TestDHCPAcquiresLease(t *testing.T) {
	requireArtifacts(t, "dist/k0smos-initramfs.gz")
	v := boot(t, bootOpts{
		Exec: "/init",
		Net:  "k0smos.ip=dhcp k0smos.dns=1.1.1.1",
		Mem:  "1024",
	})
	v.waitFor(`eth0 configured 10\.0\.2\.15/24 gw 10\.0\.2\.2 \(lease `, bootTimeout)
	v.refute(`warn: dhcp`, `supervising`, bootTimeout)
}

// The control port must not be mistaken for a shutdown request when nothing is
// connected — a closed channel once powered the machine off seconds into boot.
func TestControlPortDoesNotSelfTrigger(t *testing.T) {
	requireArtifacts(t, "dist/k0smos-initramfs.gz")
	v := boot(t, bootOpts{Exec: "/init", Mem: "1024"})
	v.waitFor(`listening for shutdown commands on /dev/vport`, bootTimeout)
	v.refute(`syncing and unmounting`, `supervising \[/init\]`, bootTimeout)
}

// A clean shutdown must leave the root consistent: killing QEMU instead corrupts
// it, which silently breaks every disk assertion above.
func TestCleanShutdownLeavesFilesystemClean(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	v := boot(t, bootOpts{Disk: disk, Exec: execNoop})
	v.waitFor(`supervising`, bootTimeout)
	v.stop()

	if !strings.Contains(v.consoleText(), "syncing and unmounting") {
		t.Error("shutdown did not run the sync/unmount path")
	}
	// The kernel itself confirms the read-only remount took effect.
	if !strings.Contains(v.consoleText(), "re-mounted") {
		t.Error("root was never remounted read-only")
	}
	requireFsckClean(t, disk)
}
