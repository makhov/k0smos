# k0smos

**Run k0s as the machine.**

k0smos is a minimal Linux PID 1 built for [k0s](https://k0sproject.io/). It
boots an immutable node image, configures the machine from cloud-init, starts
k0s, and supervises it for the lifetime of the machine.

There is no package manager, shell, SSH daemon, or service manager. A node is
configured before boot and replaced instead of modified in place.

## The product model

Each upstream k0s release produces one versioned k0smos release set. Every
platform artifact for an architecture contains the same read-only EROFS system
payload and the exact k0s version named by the release.

| Where it runs | What you deploy |
|---|---|
| Local QEMU | UEFI-bootable qcow2, managed by `k0smosctl` |
| KubeVirt | OCI image containing the kernel and initramfs |
| Bare metal / Metal3 | UEFI-bootable qcow2 or compressed raw disk |

Machine-specific configuration is not baked into those artifacts. A cloud-init
drive supplies the hostname, network settings, k0s role, join token, and files
for each machine.

## Try it locally

```bash
make ctl
./dist/k0smosctl cluster create --name dev -o kubeconfig
KUBECONFIG=kubeconfig kubectl get nodes
```

`cluster create` downloads and verifies the latest matching release artifact,
starts a local cluster, and returns when every requested node is Ready.

[Get started](install/quick-start.md){ .md-button .md-button--primary }
[Understand the artifacts](deployment/artifacts.md){ .md-button }

## Current scope

Local QEMU boot and cluster creation are tested in CI. The KubeVirt artifact and
Cluster API manifests are available but have not yet been reconciled end to end.
The bare-metal image is tested with UEFI firmware under QEMU; physical hardware
validation is still pending.

See [support status](known-limitations.md) before using k0smos outside a local
environment.
