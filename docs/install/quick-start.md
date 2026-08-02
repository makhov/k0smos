# Quick start

Needs Go 1.25+ to build `k0smosctl`, plus QEMU. Works on Apple Silicon via HVF
and on Linux/KVM. Docker is needed only when building OS artifacts locally.

Build the host CLI once:

```bash
make ctl
```

Everything after that is `k0smosctl`. The shortest path downloads the matching
qcow2 from the latest GitHub release, verifies its published SHA-256 checksum,
caches it under `~/.cache/k0smos/images/<tag>/`, and waits for every requested
node to become Ready:

```bash
./dist/k0smosctl cluster create --name dev -o kubeconfig
KUBECONFIG=kubeconfig kubectl get nodes
./dist/k0smosctl cluster rm --name dev      # clean shutdown and discard
```

That defaults to one controller which also runs workloads. An HA topology is
`cluster create --controllers 3 --workers 2`. The CLI starts a rootless shared
network, boots the first controller, mints join tokens there, boots the remaining
machines with role-specific config drives, and writes the kubeconfig only after
all five nodes are Ready.

For one-machine work, `machine up` remains the lower-level primitive. Each guest
gets its own disk cloned from the image and state under
`~/.local/state/k0smos/<name>/`; `machine list`, `logs`, `shutdown`, and `rm`
operate on it. Never kill QEMU: use `machine shutdown`, so `/var` is cleanly
unmounted.

The download happens once per release and architecture; later clusters reuse the
verified cached image and make only per-machine clones. A k0smos release tag is
the exact embedded k0s tag: one k0s release produces one complete k0smos artifact
set. For example, `--release v1.36.3+k0s.0` pins that set. `--cache-dir` relocates
the cache, and `--image path.qcow2` bypasses GitHub
for a locally built or mirrored artifact. If GitHub is unavailable, a previously
verified cached release remains usable. `GITHUB_TOKEN` or `GH_TOKEN` provides
authentication for private repositories and avoids anonymous API rate limits.

`make` is for building the CLI or local OS artifacts. The kernel, k0s and EROFS
root require Linux tooling, but consuming a release does not.

A good boot looks like:

```
k0smos: starting as PID1 (switched-root=false)
k0smos: pseudo-filesystems mounted
k0smos: no module tree; assuming a monolithic kernel
k0smos: no explicit or embedded root; discovering canonical LABEL=k0smos
k0smos: resolved LABEL=k0smos to /dev/vda2
k0smos: mounted /dev/vda2 at /newroot read-only, switching root
k0smos: starting as PID1 (switched-root=true)
k0smos: cgroup2 hierarchy ready
k0smos: loopback up
k0smos: eth0 configured 10.0.2.15/24 gw 10.0.2.2
k0smos: hostname set to "k0smos"
k0smos: supervising [/usr/local/bin/k0s controller --single]
```

**Changing k0smos code means rebuilding both images.** After `switch_root`, k0smos
re-execs `/sbin/k0smos` from the immutable root, so everything after the pivot runs the
binary in `dist/k0smos.erofs` — rebuilding only the initramfs tests stale code that
boots perfectly.
