//go:build e2e

package e2e

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The sandbox runtime (Firecracker behind a privileged, hostPID DaemonSet) needs
// two things from a node that only the OS can give: a shared mount under
// /var/run/netns, which it mounts Bidirectional, and somewhere writable for its
// state. The first is asserted. The rest -- /dev/kvm, modules, which host
// binaries nsenter can reach -- depends on the kernel and the hypervisor, so it
// is logged as an inventory rather than asserted.
//
// Results go to /dev/kmsg as one line: the console is the only channel the
// harness reads, and devkmsg rate-limiting would drop a burst of short lines.
func TestSandboxHostContract(t *testing.T) {
	requireFullSuite(t)
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	base := filepath.Join(repoRoot(t), "dist/k0smos.img")
	requirePristineDisk(t, base)
	disk := cloneDisk(t, base)

	manifest := `apiVersion: v1
kind: Pod
metadata: {name: sbx-probe, namespace: default}
spec:
  hostPID: true
  hostNetwork: true
  restartPolicy: Never
  containers:
    - name: probe
      image: docker.io/library/busybox:1.36.1
      securityContext: {privileged: true}
      command: ["/bin/sh", "-c"]
      args:
        - |
          out=""
          add() { out="$out $1"; }
          [ -c /dev/kvm ] && add kvm=yes || add kvm=no
          add mode.tun=$(stat -c %a /proc/1/root/dev/net/tun)
          grep -qwE 'svm|vmx' /proc/cpuinfo && add cpu.virt=yes || add cpu.virt=no
          for m in kvm kvm_amd kvm_intel tun vhost_net vhost_vsock ublk_drv target_core_user; do
            [ -d /sys/module/$m ] && add mod.$m=yes || add mod.$m=no
          done
          for b in sh mount blkid mkfs.xfs modprobe ip; do
            f=no
            for d in /bin /sbin /usr/bin /usr/sbin; do [ -x /proc/1/root$d/$b ] && f=yes; done
            add host.$b=$f
          done
          for p in / /run /var; do
            awk -v p=$p '$5==p' /proc/1/mountinfo | grep -q 'shared:' && add shared$p=yes || add shared$p=no
          done
          for p in /srv /var/lib/sandboxes; do
            mkdir -p /proc/1/root$p/.probe 2>/dev/null && add rw$p=yes || add rw$p=no
          done
          echo "sbxprobe:$out end" > /dev/kmsg
---
apiVersion: v1
kind: Pod
metadata: {name: sbx-bidir, namespace: default}
spec:
  restartPolicy: Never
  containers:
    - name: bidir
      image: docker.io/library/busybox:1.36.1
      securityContext: {privileged: true}
      command: ["/bin/sh", "-c", "echo sbxbidir: ok > /dev/kmsg"]
      volumeMounts:
        - {name: netns, mountPath: /var/run/netns, mountPropagation: Bidirectional}
  volumes:
    - name: netns
      hostPath: {path: /var/run/netns, type: DirectoryOrCreate}
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
  - path: /var/lib/k0s/manifests/sbx-probe/pods.yaml
    permissions: "0644"
    encoding: gzip+base64
    content: `+gzipBase64(t, manifest)+`
runcmd:
  - /usr/local/bin/k0s install controller --force --single --config /etc/k0s/k0s.yaml
  - /usr/local/bin/k0s start
`, "instance-id: i-sbx\nlocal-hostname: e2e-sbx\n")

	v := boot(t, bootOpts{
		Disk: disk, Cidata: iso, Data: seededVolume(t, 4096),
		Net: "k0smos.ip=dhcp k0smos.dns=1.1.1.1 k0smos.data=auto",
		Mem: "8192", CPUs: "4",
	})
	inventory := v.waitFor(`sbxprobe:.* end`, k0sTimeout)
	t.Logf("inventory: %s", inventory)
	// A VMM runs unprivileged; a 0600 tun is a sandbox with no network.
	if !strings.Contains(inventory, " mode.tun=666 ") {
		t.Errorf("/dev/net/tun is not 0666 for an unprivileged VMM")
	}

	// kubelet refuses a Bidirectional mount on a private mount with this message.
	got := v.waitForAny(k0sTimeout, `sbxbidir: ok`, `is not a shared mount`)
	if got != "sbxbidir: ok" {
		line := regexp.MustCompile(`.*is not a shared mount.*`).FindString(v.consoleText())
		t.Errorf("Bidirectional hostPath mount did not start (got %q): %s", got, line)
	}
}
