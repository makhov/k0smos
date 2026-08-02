# k0smos

k0smos runs [k0s](https://k0sproject.io/) as the machine's supervised workload.
It is a minimal Linux PID 1, not a general-purpose distribution: there is no
shell, SSH daemon, package manager, systemd, or mutable system partition.

A node boots an immutable EROFS system, reads its machine-specific configuration
from cloud-init, mounts writable state at `/var`, and starts k0s. Nodes are
replaced instead of modified in place.

## Quick start

Build the host CLI, then create a local cluster from the latest published
artifact:

```bash
make ctl
./dist/k0smosctl cluster create --name dev -o kubeconfig
KUBECONFIG=kubeconfig kubectl get nodes
```

Remove it cleanly:

```bash
./dist/k0smosctl cluster rm --name dev
```

Requirements are Go 1.25+, QEMU with UEFI firmware, and hardware virtualization
where available. Docker is required only to build OS artifacts locally.

## Artifacts

One upstream k0s release produces one same-tagged k0smos release set. Every
platform wrapper for an architecture carries the same immutable system payload
and exact k0s version.

| Target | Artifact |
|---|---|
| Local QEMU | UEFI-bootable qcow2, downloaded and managed by `k0smosctl` |
| KubeVirt | OCI `kernelBoot` image with kernel and embedded-root initramfs |
| Bare metal / Metal3 | UEFI-bootable qcow2 or compressed raw disk |

Machine configuration is delivered separately through NoCloud or OpenStack
config-drive data. This is the contract used by both `k0smosctl` and Cluster API
bootstrap providers.

## Documentation

- [Get started](https://makhov.github.io/k0smos/install/quick-start/)
- [Artifacts and releases](https://makhov.github.io/k0smos/deployment/artifacts/)
- [KubeVirt and Cluster API](https://makhov.github.io/k0smos/deployment/kubevirt/)
- [Bare metal and Metal3](https://makhov.github.io/k0smos/deployment/bare-metal/)
- [Support status](https://makhov.github.io/k0smos/known-limitations/)
- [CLI reference](https://makhov.github.io/k0smos/reference/k0smosctl/)

## Build and test

```bash
make test       # unit tests
make e2e        # fast QEMU boot tests
make e2e-full   # k0s readiness and multi-node tests
make metal      # qcow2 and raw platform image
make oci        # KubeVirt OCI image
```

The repository layout is intentionally split between the node and the host:

```text
cmd/k0smos/       Linux PID 1 and boot lifecycle
cmd/k0smosctl/    host CLI for local machines and clusters
internal/         boot, configuration, networking, and shutdown components
image/            kernels, root filesystem, and platform packaging
e2e/              QEMU-based acceptance tests
examples/         experimental Cluster API manifests
```

## Project status

Local QEMU clusters and amd64 UEFI boot are tested in CI. The KubeVirt and
Cluster API manifests have not yet completed an end-to-end reconciliation, and
the metal image has not yet been validated on physical hardware. See the
[support matrix](https://makhov.github.io/k0smos/known-limitations/) for the
current boundary.
