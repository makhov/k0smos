//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// These boot real k0s and take several minutes each. -short skips them, so the
// fast suite stays usable while iterating.
func requireFullSuite(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping k0s boot in -short mode")
	}
}

// The whole point of the project: a node that reaches Ready. The markers are the
// ones a real cluster logs, taken from a captured console rather than guessed.
func TestK0sNodeReachesReady(t *testing.T) {
	requireFullSuite(t)
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	base := filepath.Join(repoRoot(t), "dist/k0smos.img")
	requirePristineDisk(t, base)
	disk := cloneDisk(t, base)

	iso := makeCidata(t, `#cloud-config
write_files:
  - path: /etc/k0s/k0s.yaml
    permissions: "0644"
    content: |
      apiVersion: k0s.k0sproject.io/v1beta1
      kind: ClusterConfig
      spec:
        storage:
          type: kine
runcmd:
  - /usr/local/bin/k0s install controller --force --single --config /etc/k0s/k0s.yaml
  - /usr/local/bin/k0s start
`, "instance-id: i-ready\nlocal-hostname: e2e-ready\n")

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Mem: "8192", CPUs: "4"})

	// --force and --env are install-only; k0s rejects them on the foreground
	// command, so the translation must have stripped them.
	v.waitFor(`supervising \[/usr/local/bin/k0s controller --single --config`, bootTimeout)

	v.waitFor(`just became ready`, k0sTimeout)
	if strings.Contains(v.consoleText(), "unknown flag") {
		t.Error("k0s rejected a flag from the translated install command")
	}
	// A missing kernel module shows up here rather than as "module not found".
	for _, bad := range []string{
		"Failed to initialize nft",
		"RULE_APPEND failed",
		"cannot find filesystem info for device",
	} {
		if strings.Contains(v.consoleText(), bad) {
			t.Errorf("regression: %q appeared on the console", bad)
		}
	}
}

// k0s applies what is left in its manifest directory, which is how addons deploy
// with no shell. Asserts k0s picked the stack up, not merely that the file exists.
func TestK0sAppliesShippedManifest(t *testing.T) {
	requireFullSuite(t)
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	base := filepath.Join(repoRoot(t), "dist/k0smos.img")
	requirePristineDisk(t, base)
	disk := cloneDisk(t, base)

	manifest := `apiVersion: v1
kind: Namespace
metadata:
  name: e2e-applied
`
	iso := makeCidata(t, `#cloud-config
write_files:
  - path: /etc/k0s/k0s.yaml
    permissions: "0644"
    content: |
      apiVersion: k0s.k0sproject.io/v1beta1
      kind: ClusterConfig
      spec:
        storage:
          type: kine
  - path: /var/lib/k0s/manifests/e2e-applied/ns.yaml
    permissions: "0644"
    encoding: gzip+base64
    content: `+gzipBase64(t, manifest)+`
runcmd:
  - /usr/local/bin/k0s install controller --force --single --config /etc/k0s/k0s.yaml
  - /usr/local/bin/k0s start
`, "")

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Mem: "8192", CPUs: "4"})
	v.waitFor(`Running stack\s+.*stack=/var/lib/k0s/manifests/e2e-applied`, k0sTimeout)
	v.waitFor(`applier-e2e-applied`, k0sTimeout)

	for _, l := range strings.Split(v.consoleText(), "\n") {
		if strings.Contains(l, "applier-e2e-applied") &&
			(strings.Contains(l, "error") || strings.Contains(l, "failed")) {
			t.Errorf("applying the stack reported a problem: %s", l)
		}
	}
}

// An etcd-backed controller must try to leave the cluster before stopping.
//
// On a single-member cluster etcd refuses, because removing the last member
// would destroy quorum — that refusal is the expected result here, and it proves
// the request reached etcd rather than failing on plumbing.
func TestK0sEtcdLeaveIsAttemptedOnShutdown(t *testing.T) {
	requireFullSuite(t)
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	base := filepath.Join(repoRoot(t), "dist/k0smos.img")
	requirePristineDisk(t, base)
	disk := cloneDisk(t, base)

	iso := makeCidata(t, `#cloud-config
write_files:
  - path: /etc/k0s/k0s.yaml
    permissions: "0644"
    content: |
      apiVersion: k0s.k0sproject.io/v1beta1
      kind: ClusterConfig
      spec:
        storage:
          type: etcd
runcmd:
  - /usr/local/bin/k0s install controller --force --enable-worker --config /etc/k0s/k0s.yaml
  - /usr/local/bin/k0s start
`, "")

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Mem: "8192", CPUs: "4"})
	// Wait for etcd itself, not just the controller: leaving before it is up
	// would not exercise anything.
	v.waitFor(`Started successfully.*component=etcd`, k0sTimeout)
	v.stop()

	text := v.consoleText()
	if !strings.Contains(text, "leaving etcd cluster") {
		t.Fatalf("no etcd leave attempted:\n%s", v.k0smosLines())
	}
	// The leave must precede the child being killed, or there is nothing to
	// leave with.
	leaveAt := strings.Index(text, "leaving etcd cluster")
	killAt := strings.Index(text, "child exited")
	if killAt != -1 && leaveAt > killAt {
		t.Error("etcd leave ran after the child was stopped")
	}
	// Either outcome is acceptable; a plumbing failure is not.
	if !strings.Contains(text, "left etcd cluster") &&
		!strings.Contains(text, "less than quorum") &&
		!strings.Contains(text, "not enough started members") {
		t.Errorf("etcd leave failed for an unexpected reason:\n%s", v.k0smosLines())
	}
	if !strings.Contains(text, "syncing and unmounting") {
		t.Error("shutdown did not continue after the etcd leave")
	}
}
