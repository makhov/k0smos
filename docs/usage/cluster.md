# Create a local cluster

`cluster create` is the normal local entry point. With no `--image`, it resolves
the latest GitHub release for the selected architecture, downloads the complete
qcow2, verifies the adjacent `.sha256` release asset, and stores it under
`~/.cache/k0smos/images/<tag>/`:

```bash
k0smosctl cluster create \
  --name dev \
  --arch amd64 \
  -o kubeconfig

KUBECONFIG=kubeconfig kubectl get nodes
```

Without topology flags this creates one controller that also runs workloads. For
an HA control plane and separate workers:

```bash
k0smosctl cluster create --name dev --controllers 3 --workers 2 -o kubeconfig
```

The command creates one clone and one config drive per machine, starts a rootless
shared Ethernet segment, boots the initial controller, asks it to mint the
role-specific join tokens, and then starts the other machines. It returns only
after the Kubernetes API reports the requested number of nodes and every node is
Ready. `--dry-run` prints names, roles, addresses, and forwarded API ports without
creating state.

The release API is checked on later runs, but the large image is downloaded only
once for each tag and architecture. k0smos uses the embedded k0s release as the
release identity: tag `v1.36.3+k0s.0` is the complete artifact set built with that
exact k0s binary. `--release v1.36.3+k0s.0` pins that set; `--cache-dir` moves the
cache. If GitHub cannot be reached, the last verified
cached `latest` artifact is used. For development or an internal mirror,
`--image /path/to/k0smos-metal-x86_64.qcow2` bypasses release resolution entirely.
Set `GITHUB_TOKEN` or `GH_TOKEN` when the repository is private or anonymous API
rate limits are too low.

All machines still appear in `k0smosctl machine list`, with names such as
`dev-controller-0` and `dev-worker-0`. Their disks and consoles use the normal
per-machine state directories. The cluster's config drives and network-daemon
metadata live under `~/.local/state/k0smos/.clusters/dev/`.

Remove the whole local cluster cleanly with:

```bash
k0smosctl cluster rm --name dev
```
