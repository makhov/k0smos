# Get started

This guide creates a single-controller k0s cluster on your machine. The
controller also runs workloads, so it is useful on its own.

## Prerequisites

- QEMU with UEFI firmware
- `kubectl`
- hardware virtualization: KVM on Linux, HVF on macOS

## 1. Install k0smosctl

Download the binary for your host from the
[latest release](https://github.com/makhov/k0smos/releases/latest):

```bash
curl -sSLo k0smosctl \
  https://github.com/makhov/k0smos/releases/latest/download/k0smosctl-$(uname -s | tr A-Z a-z)-$(uname -m)
chmod +x k0smosctl
sudo mv k0smosctl /usr/local/bin/
```

Check it runs:

```bash
k0smosctl --version
```

## 2. Create a cluster

```bash
k0smosctl cluster create --name dev
```

The command resolves the latest release for your architecture, downloads the
node image and verifies its checksum, gives the machine its own copy of that
image, boots it, waits for the Kubernetes node to become Ready, and writes
`kubeconfig`.

Images are cached under `~/.cache/k0smos/images/`, so later clusters start
without downloading again. Machine and cluster state lives under
`~/.local/state/k0smos/`.

## 3. Use it

```bash
KUBECONFIG=kubeconfig kubectl get nodes
KUBECONFIG=kubeconfig kubectl create deployment nginx --image=nginx
```

To watch what a machine is doing:

```bash
k0smosctl machine logs --name dev-controller-0 -f
```

## 4. Remove it

```bash
k0smosctl cluster rm --name dev
```

Every machine is shut down cleanly before its disk and network state are
deleted.

## Variations

A three-controller control plane with two workers:

```bash
k0smosctl cluster create --name dev --controllers 3 --workers 2
```

An exact k0s release rather than the latest:

```bash
k0smosctl cluster create --name dev --release v1.36.3+k0s.0
```

An image you already have, without contacting GitHub:

```bash
k0smosctl cluster create --name dev --image ./k0smos-metal-x86_64.qcow2
```

## Next

- [Local clusters](../usage/cluster.md) — multi-machine clusters, tokens, state
- [Local machines](../usage/boot.md) — running one machine at a time
- [Machine configuration](../usage/cloud-init.md) — what a cloud-init drive can
  carry
