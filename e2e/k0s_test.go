//go:build e2e

package e2e

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/amakhov/k0smos/internal/control"
)

// These boot real k0s and take several minutes each. -short skips them, so the
// fast suite stays usable while iterating.
func requireFullSuite(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping k0s boot in -short mode")
	}
}

// A pod the node must be able to run once Ready, and that reports what only a
// running node can show. It mounts a hostPath Bidirectional, as CSI node plugins
// do, which kubelet refuses unless the mount is shared. And it reports the modes
// of the devices an unprivileged VMM opens, which k0smos sets in place of udev.
// The console is the only channel the harness reads, hence /dev/kmsg.
const nodeProbe = `apiVersion: v1
kind: Pod
metadata: {name: node-probe, namespace: default}
spec:
  hostPID: true
  restartPolicy: Never
  containers:
    - name: probe
      image: docker.io/library/busybox:1.36.1
      securityContext: {privileged: true}
      command: ["/bin/sh", "-c"]
      args:
        - |
          m() { stat -c %a /proc/1/root$1 2>/dev/null || echo absent; }
          echo "nodeprobe: kvm=$(m /dev/kvm) tun=$(m /dev/net/tun) end" > /dev/kmsg
      volumeMounts:
        - {name: shared, mountPath: /mnt/shared, mountPropagation: Bidirectional}
  volumes:
    - name: shared
      hostPath: {path: /var/lib/e2e-shared, type: DirectoryOrCreate}
`

// The whole point of the project: a node that reaches Ready. The markers are the
// ones a real cluster logs, taken from a captured console rather than guessed.
//
// It then runs nodeProbe, rather than paying for a k0s boot of its own.
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
  - path: /var/lib/k0s/manifests/node-probe/pod.yaml
    permissions: "0644"
    encoding: gzip+base64
    content: `+gzipBase64(t, nodeProbe)+`
runcmd:
  - /usr/local/bin/k0s install controller --force --single --config /etc/k0s/k0s.yaml
  - /usr/local/bin/k0s start
`, "instance-id: i-ready\nlocal-hostname: e2e-ready\n")

	// A data volume carrying k0s's airgap bundle, so containerd imports the images
	// instead of pulling them over slirp — the difference between a couple of
	// minutes and up to thirteen.
	v := boot(t, bootOpts{
		Disk: disk, Cidata: iso, Data: seededVolume(t, 4096),
		Net: "k0smos.ip=dhcp k0smos.dns=1.1.1.1 k0smos.data=auto",
		Mem: "8192", CPUs: "4",
	})

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

	// kubelet refuses a Bidirectional mount on a private one with this message.
	got := v.waitForAny(k0sTimeout, `nodeprobe: .* end`, `is not a shared mount`)
	if !strings.HasPrefix(got, "nodeprobe:") {
		line := regexp.MustCompile(`.*is not a shared mount.*`).FindString(v.consoleText())
		t.Fatalf("Bidirectional hostPath mount did not start (got %q): %s", got, line)
	}
	t.Logf("%s", got)
	// tun is in the default module set, so it must be there. /dev/kvm depends on
	// the hypervisor offering nested virtualisation; when present it must be 666.
	if !strings.Contains(got, " tun=666 ") {
		t.Errorf("/dev/net/tun is not 0666 for an unprivileged VMM: %s", got)
	}
	if !strings.Contains(got, " kvm=666 ") && !strings.Contains(got, " kvm=absent ") {
		t.Errorf("/dev/kvm is not 0666 for an unprivileged VMM: %s", got)
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

// Fetching the kubeconfig from a live node, which is what k0smosctl kubeconfig
// does. Not in the fast suite: it needs a cluster that has got as far as writing
// its PKI, which means a real k0s boot.
func TestK0sKubeconfigOverControlPort(t *testing.T) {
	requireFullSuite(t)
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	base := filepath.Join(repoRoot(t), "dist/k0smos.img")
	requirePristineDisk(t, base)
	disk := cloneDisk(t, base)

	v := boot(t, bootOpts{Disk: disk, Net: "k0smos.ip=dhcp k0smos.dns=1.1.1.1"})
	v.waitFor(`supervising \[`, bootTimeout)

	var data []byte
	deadline := time.Now().Add(k0sTimeout)
	for time.Now().Before(deadline) {
		got, err := requestNode(t, v, control.RequestKubeconfig)
		if err == nil {
			data = got
			break
		}
		time.Sleep(2 * time.Second)
	}
	if len(data) == 0 {
		t.Fatal("never received a kubeconfig")
	}

	// It must be a kubeconfig, not merely bytes: a truncated reply would satisfy
	// a length check and nothing else.
	for _, want := range []string{"apiVersion:", "kind: Config", "client-certificate-data:", "server: https://"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("reply is missing %q:\n%s", want, firstLines(string(data), 12))
		}
	}
	v.stop()
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
