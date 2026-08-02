# Local clusters

`k0smosctl cluster create` is the primary local workflow. It turns one platform
artifact into a ready single- or multi-machine k0s cluster.

## Create a cluster

```bash
k0smosctl cluster create --name dev -o kubeconfig
```

By default this creates one controller that also runs workloads. To create an HA
control plane with dedicated workers:

```bash
k0smosctl cluster create \
  --name dev \
  --controllers 3 \
  --workers 2 \
  -o kubeconfig
```

Every machine receives a clone of the same image and a generated configuration
drive. A rootless userspace network connects the guests. The first controller
bootstraps the cluster and provides role-specific join tokens for the remaining
machines.

The command returns only after the requested nodes are Ready.

## Select an artifact

With no image flags, the CLI downloads and verifies the latest release:

```bash
k0smosctl cluster create --name dev
```

Pin a release or use a local image:

```bash
k0smosctl cluster create --name dev --release v1.36.3+k0s.0
k0smosctl cluster create --name dev --image ./k0smos-metal-x86_64.qcow2
```

The release cache is reused between clusters. Each machine disk remains private
to that machine.

## Inspect and remove

Cluster machines appear in the normal machine list:

```bash
k0smosctl machine list
k0smosctl machine logs --name dev-controller-0 -f
```

Remove the cluster as one operation:

```bash
k0smosctl cluster rm --name dev
```

Do not remove individual cluster machine directories manually. `cluster rm`
coordinates clean shutdown, the shared network, and all recorded cluster state.

## State locations

- release cache: `~/.cache/k0smos/images/`
- machine state: `~/.local/state/k0smos/<machine>/`
- cluster state: `~/.local/state/k0smos/.clusters/<cluster>/`

`K0SMOS_CACHE_DIR` and `K0SMOS_STATE_DIR` relocate these roots.
