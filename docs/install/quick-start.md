# Get started

This guide creates a single-controller k0s cluster on the local machine. The
controller also runs workloads.

## Prerequisites

- Go 1.25 or newer, to build `k0smosctl`
- QEMU with UEFI firmware
- `kubectl`, to use the resulting cluster
- hardware virtualization (KVM on Linux or HVF on macOS) is strongly recommended

Docker and Linux image-building tools are **not** required when consuming a
published release.

## 1. Build the CLI

```bash
make ctl
```

This creates `dist/k0smosctl` for the host. The node image is downloaded in the
next step.

## 2. Create a cluster

```bash
./dist/k0smosctl cluster create --name dev -o kubeconfig
```

The command:

1. resolves the latest k0smos release for the host architecture;
2. downloads the qcow2 and verifies its published SHA-256 checksum;
3. creates a per-machine disk from that pristine image;
4. boots the machine and waits for the Kubernetes node to become Ready; and
5. writes an immediately usable admin kubeconfig.

Images are cached under `~/.cache/k0smos/images/`. Machine and cluster state is
kept under `~/.local/state/k0smos/`.

## 3. Use the cluster

```bash
KUBECONFIG=kubeconfig kubectl get nodes
KUBECONFIG=kubeconfig kubectl create deployment nginx --image=nginx
```

Follow the machine console when diagnosing a boot:

```bash
./dist/k0smosctl machine logs --name dev-controller-0 -f
```

## 4. Remove it

```bash
./dist/k0smosctl cluster rm --name dev
```

This shuts every machine down cleanly before deleting its local disks and
network state.

## Useful variations

Create three controllers and two workers:

```bash
./dist/k0smosctl cluster create \
  --name dev \
  --controllers 3 \
  --workers 2 \
  -o kubeconfig
```

Pin the artifact set to an exact k0s release:

```bash
./dist/k0smosctl cluster create --release v1.36.3+k0s.0
```

Use a locally built or internally mirrored image without contacting GitHub:

```bash
./dist/k0smosctl cluster create \
  --image /path/to/k0smos-metal-x86_64.qcow2
```

Next, see [local clusters](../usage/cluster.md), [individual machines](../usage/boot.md),
or the [deployment artifacts](../deployment/artifacts.md).
