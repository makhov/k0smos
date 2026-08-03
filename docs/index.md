# k0smos

**Run k0s as the machine.**

k0smos is a minimal Linux PID 1 built for [k0s](https://k0sproject.io/). It
boots an immutable node image, configures the machine from cloud-init, starts
k0s, and supervises it for the lifetime of the machine.

There is no package manager, shell, SSH daemon, or service manager. A node is
configured before boot and replaced instead of modified in place.

## Try it locally

```bash
k0smosctl cluster create --name dev
KUBECONFIG=kubeconfig kubectl get nodes
```

`cluster create` downloads and verifies the node image, starts a local cluster,
and returns when every requested node is Ready.

[Get started](install/quick-start.md){ .md-button .md-button--primary }
[Understand the artifacts](deployment/artifacts.md){ .md-button }

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

## What a node does at boot

1. mounts the immutable system read-only, and writable state at `/var`;
2. loads kernel modules for the hardware it finds;
3. configures networking from the cloud-init drive or DHCP;
4. applies the files and settings from that drive; and
5. starts k0s in the role the drive selected, and supervises it.

Shutting down reverses it: k0s stops, filesystems are synced and unmounted, and
the machine powers off with a consistent disk.
