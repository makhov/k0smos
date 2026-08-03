# k0smos

k0smos runs [k0s](https://k0sproject.io/) as the machine. It is a minimal Linux
PID 1, not a general-purpose distribution: no shell, no SSH daemon, no package
manager, no systemd, no mutable system partition.

A node boots an immutable system image, reads its configuration from cloud-init,
mounts writable state at `/var`, and starts k0s. Nodes are replaced rather than
modified in place.

## Install

Download `k0smosctl` for your host from the
[latest release](https://github.com/makhov/k0smos/releases/latest):

```bash
curl -sSLo k0smosctl \
  https://github.com/makhov/k0smos/releases/latest/download/k0smosctl-$(uname -s | tr A-Z a-z)-$(uname -m)
chmod +x k0smosctl
sudo mv k0smosctl /usr/local/bin/
```

You also need QEMU with UEFI firmware, and `kubectl` to use the cluster.

## Quick start

```bash
k0smosctl cluster create --name dev
KUBECONFIG=kubeconfig kubectl get nodes
```

This downloads the node image from the latest release, verifies its checksum,
boots the machine, and writes a kubeconfig once the node is Ready.

Remove it when you are done:

```bash
k0smosctl cluster rm --name dev
```

## Where it runs

One upstream k0s release produces one same-tagged k0smos release. Every platform
artifact for an architecture carries the same system payload and k0s version.

| Target | Artifact |
|---|---|
| Local QEMU | UEFI-bootable qcow2, downloaded and managed by `k0smosctl` |
| KubeVirt | OCI `kernelBoot` image with kernel and embedded-root initramfs |
| Bare metal / Metal3 | UEFI-bootable qcow2 or compressed raw disk |

Per-machine configuration — hostname, network, k0s role, join token, files —
is delivered separately on a cloud-init drive. That is the same contract used by
`k0smosctl` and by Cluster API bootstrap providers.

## Documentation

<https://makhov.github.io/k0smos/>

- [Get started](https://makhov.github.io/k0smos/install/quick-start/)
- [Local clusters](https://makhov.github.io/k0smos/usage/cluster/)
- [Machine configuration](https://makhov.github.io/k0smos/usage/cloud-init/)
- [KubeVirt and Cluster API](https://makhov.github.io/k0smos/deployment/kubevirt/)
- [Bare metal and Metal3](https://makhov.github.io/k0smos/deployment/bare-metal/)
- [CLI reference](https://makhov.github.io/k0smos/reference/k0smosctl/)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building the CLI and OS artifacts
from source.
