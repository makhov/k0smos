# k0smos

A minimal Go PID1 for running [k0s](https://k0sproject.io) Kubernetes nodes. No
shell, no busybox, no systemd — the image contains k0smos, k0s, and a handful of
libraries.

k0smos does the OS init a Kubernetes node needs and nothing else: mount
pseudo-filesystems, load kernel modules, switch onto a real root, prepare a data
volume, configure networking, read its bootstrap data off a cloud-init drive, then
supervise one workload for the life of the machine and shut it down cleanly when
asked.

There is no SSH and no shell to get into. A machine is configured entirely by
cloud-init — which is what Cluster API providers already produce — and replaced
rather than administered. `runcmd` is **interpreted, never executed**.

Docs: **[usage.md](docs/usage.md)** (how to use it) ·
[architecture.md](docs/architecture.md) (why the boot sequence is ordered as it is)
· [deployment.md](docs/deployment.md) (KubeVirt, Cluster API)

## Quick start

Needs Go 1.25+, QEMU, and Docker (the ext4 image and the kernel unpack both need
Linux tools). Works on Apple Silicon via HVF and on linux/KVM.

Build the artifacts and the CLI once:

```bash
make artifacts ctl   # kernel, k0s, initramfs, ext4 root, and k0smosctl
```

Everything after that is `k0smosctl`. The guest runs in the background — a k0smos
node has no shell, so there is nothing to sit in front of:

```bash
k0smosctl boot                        # returns immediately
k0smosctl logs -f                     # watch it come up
k0smosctl kubeconfig -o kubeconfig
KUBECONFIG=kubeconfig kubectl get nodes      # k0smos   Ready   v1.36.3+k0s
k0smosctl shutdown                    # never kill QEMU: it corrupts the image
```

A second node is one more command — `boot --name vm2 --api-port 7443` — because each
guest gets its own disk cloned from the image, and its own state under
`~/.local/state/k0smos/<name>/`. `list` shows what is running, `rm` discards one.
`boot --attach` stays in the foreground, where ctrl-c stops the guest cleanly.

`make` is for *building* — the kernel, k0s and the ext4 root need Linux tools. With
a release's artifacts unpacked, `k0smosctl boot` needs no `make`, no repository and
no Go toolchain.

A good boot looks like:

```
k0smos: starting as PID1 (switched-root=false)
k0smos: pseudo-filesystems mounted
k0smos: no module tree; assuming a monolithic kernel
k0smos: resolved LABEL=k0smos to /dev/vda
k0smos: mounted /dev/vda at /newroot, switching root
k0smos: starting as PID1 (switched-root=true)
k0smos: cgroup2 hierarchy ready
k0smos: loopback up
k0smos: eth0 configured 10.0.2.15/24 gw 10.0.2.2
k0smos: hostname set to "k0smos"
k0smos: supervising [/usr/local/bin/k0s controller --single]
```

**Changing k0smos code means rebuilding both images.** After `switch_root`, k0smos
re-execs `/sbin/k0smos` from the ext4 root, so everything after the pivot runs the
binary in `dist/k0smos.img` — rebuilding only the initramfs tests stale code that
boots perfectly.

## What gets built

| Artifact | Built by | Contents |
|---|---|---|
| `dist/kernel/<arch>/vmlinuz` | `make kernel` | Kata guest kernel (monolithic, pinned). `make kernel-alpine` fetches Alpine `linux-virt` + a module tree instead |
| `dist/k0smos-initramfs.gz` | `make initramfs` | k0smos as `/init`, plus the module tree if the kernel has one |
| `dist/k0smos.img` | `make disk` | ext4 root: k0smos, k0s, `/etc` |

Boot always goes through the initramfs, even on a monolithic kernel: kubelet
cannot run on a ramfs root, so k0smos always `switch_root`s onto the ext4 image.

## Make targets

| Target | What it does |
|---|---|
| `make test` | unit tests — no root, no VM |
| `make ctl` | build `k0smosctl` into `dist/` (host platform) |
| `make vet` | vet for the host and for `GOOS=linux` |
| `make kernel` | fetch the Kata guest kernel |
| `make kernel-alpine` | fetch Alpine `linux-virt` + its module tree |
| `make k0s` | download the latest k0s release (~240 MB) |
| `make initramfs` / `make disk` | build the boot initramfs / the ext4 root |
| `make artifacts` | all of the above — everything `k0smosctl boot` needs |
| `make smoke` | init-only boot, no k0s — the ~15s check while changing k0smos |
| `make boot` | `k0smosctl boot` with the artifacts rebuilt first |
| `make e2e` | QEMU boots asserting on behaviour, no k0s (~10s/boot) |
| `make e2e-full` | adds the k0s tests: node Ready, manifests, etcd leave |
| `make accept` | boot headless, wait for `Ready`, power off |
| `make clean-dist` | delete `dist/` |

## Configuration: cloud-init

The only configuration interface. A NoCloud ISO labelled `cidata` or an OpenStack
config-drive labelled `config-2`, read **without being mounted** — so no kernel
filesystem support is involved.

`k0smosctl` builds the drive, writing the ISO itself so no `xorriso` (and on macOS
no Docker) is needed:

```bash
make ctl
k0smosctl gen --file k0s.yaml:/etc/k0s/k0s.yaml --hostname node-1 -o cidata.iso
k0smosctl boot --cidata cidata.iso
```

`--user-data <file>` passes a cloud-config through whole instead. Either way it is
checked with the same parser the node uses, so a drive that would be ignored —
malformed YAML, or a cloud-config missing its `#cloud-config` first line — is
refused before it can boot into a silently unconfigured machine. See
[usage.md](docs/usage.md#configure-a-node-with-cloud-init) for more.

## Talking to a running node

A node has no SSH and no shell. It answers a small set of requests on a
virtio-serial control port instead:

```bash
k0smosctl kubeconfig -o kubeconfig   # then KUBECONFIG=kubeconfig kubectl get nodes
k0smosctl token --role controller    # a join token, so another machine can join
k0smosctl shutdown                   # or reboot — never kill QEMU
```

The kubeconfig comes off the node's filesystem, so it works whether or not k0s is
still running, and says so plainly when the cluster has not written its PKI yet.
The API server address is rewritten to `127.0.0.1`, which is where `run-qemu.sh`
forwards 6443.

A join token is signed with the cluster CA, so only a machine already in the
cluster can produce one — the node runs `k0s token create` and sends the result
back. That is what lets a machine with no shell be joined to; see
[docs/usage.md](docs/usage.md#more-than-one-node) for the whole flow.

Whoever can write to that port obtains cluster-admin, and a controller token
confers control-plane membership. That is not a new exposure — the same channel
stops the machine — but do not expose the port anywhere the disk is not equally
exposed.

From `user-data`:

| Key | Behaviour |
|---|---|
| `write_files` | Written with the requested `permissions`; parent directories created. Encodings: plain, `b64`/`base64`, `gzip+base64` (and `gz+b64` spellings) |
| `runcmd`: `k0s install <role> …` | Becomes the supervised workload (`k0s <role> …`); `--env KEY=VAL` goes to the child's environment |
| `runcmd`: `k0s start`/`stop`, `systemctl`, `service` | Dropped — there is no service manager |
| `runcmd`: `mkdir`, `chmod`, `chown`, `ln -s` | Interpreted and performed via syscalls |
| `runcmd`: anything else | Logged `UNSUPPORTED` and skipped |

From `meta-data`: `local-hostname` sets the hostname (overriding
`k0smos.hostname=`), plus `instance-id`.

Kubernetes resources ship as files, not commands: k0s applies anything under
`/var/lib/k0s/manifests/<stack>/`, so an addon is a `write_files` entry — which is
what makes refusing arbitrary commands practical.

## Kernel cmdline options

All optional; k0smos boots with defaults if none are given.

| Option | Default | Meaning |
|---|---|---|
| `k0smos.root=` | *(none)* | Root to switch onto: `/dev/vda`, `UUID=…` or `LABEL=…`. Unset stays on the initramfs — fine for a smoke test, but kubelet cannot run there |
| `k0smos.rootfstype=` | `ext4` | Filesystem type for the above |
| `k0smos.rootflags=` | *(none)* | Mount data string, e.g. `noatime` |
| `k0smos.data=` | *(none)* | Data volume: `auto`, a device path, or `LABEL=`/`UUID=`. Unset disables it |
| `k0smos.datalabel=` | `k0smos-data` | Label applied when formatting, and searched for by `auto` |
| `k0smos.datafstype=` | `ext4` | Filesystem created and mounted |
| `k0smos.datadir=` | `/var/lib/k0s` | Where it is mounted |
| `k0smos.ip=` | *(none)* | `dhcp`, a static CIDR like `10.0.0.20/24`, or a per-interface list (below). Unset leaves loopback only |
| `k0smos.gw=` | *(none)* | Default gateway. Applies to `k0smos.iface` only — a machine has one default route |
| `k0smos.dns=` | *(none)* | Resolver for `/etc/resolv.conf`. **Overrides the DHCP lease** |
| `k0smos.iface=` | `eth0` | Interface a bare `k0smos.ip=` configures, and the one `k0smos.gw=` attaches to |
| `k0smos.hostname=` | `k0smos` | Hostname, also sent as the DHCP hostname |
| `k0smos.exec=` | `/usr/local/bin/k0s,controller,--single` | Supervised child, comma-separated (a cmdline value cannot contain spaces) |
| `k0smos.modules=` | *(built-in set)* | Comma-separated module list, or `none` to disable module loading entirely |
| `k0smos.path=` | see below | `PATH` exported to children |

`k0smos.path` defaults to
`/var/lib/k0s/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin`.
`/var/lib/k0s/bin` matters: k0s stages containerd, runc, kubelet and iptables
there at runtime.

### More than one interface

`k0smos.ip=` also takes a list of `interface:address` pairs, for a machine whose
management network is not the one the cluster talks over:

```
k0smos.ip=eth0:dhcp,eth1:10.10.0.11/24 k0smos.gw=10.0.2.2
```

Each entry is `dhcp` or a CIDR, applied in order. The gateway attaches to
`k0smos.iface` (`eth0` by default), so a second NIC on a segment with no router
needs nothing further. Tell kubelet which address is the node's — otherwise it
picks the one behind the default route, and every node in the cluster claims the
same one:

```
k0smos.exec=/usr/local/bin/k0s,controller,--enable-worker,--kubelet-extra-args=--node-ip=10.10.0.11
```

The built-in module set covers virtio, ext4, overlayfs, the netfilter and nft
pieces kube-proxy needs, ipsets, veth/bridge and the ACPI power button. Beyond it,
drivers are autoloaded by matching each device's `modalias` against
`modules.alias`, the same way udev does — so a distro kernel drives hardware
nothing named in advance. Modules absent from the kernel are skipped, so the same
list is safe on a monolithic kernel.

## The read-only root (erofs)

`ROOTFS=erofs` builds the root as a read-only erofs image instead of a sparse ext4
one, and `EMBED_ROOT=` puts that image *inside the initramfs*. The kernel and the
whole OS then travel as one artifact — 18 MB + 165 MB — with no root disk to attach,
publish or mismatch:

```bash
ROOTFS=erofs K0S_BIN=dist/k0s-$(go env GOARCH) ./image/mkrootfs.sh dist/k0smos.erofs
EMBED_ROOT=dist/k0smos.erofs ./image/mkinitramfs.sh
```

k0smos loop-attaches the image, detects that it is erofs (so no
`k0smos.rootfstype=` is needed) and `switch_root`s onto it read-only. A node reaches
`Ready` this way, covered by `TestEmbeddedEROFSRootBoots`.

Two things a read-only root forces:

- **A data volume is required, mounted at `/var`** — not `/var/lib/k0s`, because
  kubelet writes to `/var/lib/kubelet`. That is the same split Talos uses, with `/var`
  as its EPHEMERAL partition. `k0smos.data=auto k0smos.datadir=/var`.
- **`/etc` and `/usr/libexec` get a tmpfs overlay**, with the image's contents as the
  lower layer, since k0s creates and `chmod`s `/etc/k0s` and kubelet creates
  `/usr/libexec/k0s`. A cloud-init or k0smotron-supplied `k0s.yaml` lands in the upper
  layer and shadows the baked default. `/opt` is a symlink to `/var/opt` instead —
  containerd stages plugins there and CNI binaries are tens of megabytes, which
  belong on disk rather than in RAM.

erofs and not squashfs because the default kernel decides it: Kata's builds in erofs
and has no squashfs driver at all.

Alpine's kernels are the mirror image — **no erofs in either `linux-virt` or
`linux-lts`**, but `CONFIG_SQUASHFS=m` — so a read-only root there would have to be
squashfs. Nothing builds that today, which is why an Alpine kernel boots from an ext4
disk instead. `mkinitramfs.sh` reads the kernel's config and declines to embed a root
it could not mount, rather than producing an initramfs that fails at `switch_root`.

That matters for bare metal, which wants `linux-lts` — it carries `igb`, `ixgbe` and
`megaraid_sas`, which `linux-virt` does not. So the read-only-root model reaches bare
metal only once squashfs is supported alongside erofs.

ext4 remains the default root for now, since it is the configuration with the most
boots behind it. ext4 does not go away either way — the data volume needs a
read-write filesystem, so `mkfs.ext4` stays in the image.

## Which kernel

| | `make kernel` (Kata, **default**) | `make kernel-alpine` (Alpine `linux-virt`) |
|---|---|---|
| virtio, ext4, netfilter, overlayfs | **built in** | modules |
| module tree in the image | **none** | ~29 MB, must match the kernel exactly |
| initramfs | **~1.2 MB** | ~22 MB |
| pinned | **by digest** | no, tracks Alpine |
| bare metal | **no** — no NVMe/ATA/SCSI/USB/NIC drivers | some: NVMe, ATA, SCSI, USB-storage, `e1000e`, `mlx5`; but no `igb`/`ixgbe`/`i40e`/`bnxt`/`megaraid_sas` |

Kata's is a *guest* kernel: ideal for VMs, useless on bare metal. Nothing special
is needed to build against it — `make disk` and `./image/mkinitramfs.sh` handle a
missing module tree as "monolithic".

**Bring your own instead.** k0smos does not own a kernel; the fetch scripts only
fetch. Point `MODULES_DIR` at any distro kernel's module tree — you do not need to
build one. It must provide drivers for your hardware, `ext4`, `nf_tables` +
`nft_compat`, `overlayfs` and cgroup v2. It does **not** need any filesystem
support for the cloud-init drive.

## The data volume

`/var/lib/k0s` — etcd, containerd, kubelet, pulled images — can live on a separate
volume, which is what lets a machine be disposable without being diskless:

```
k0smos.root=LABEL=k0smos k0smos.data=auto
```

Attach an ephemeral per-VM disk (KubeVirt `emptyDisk`) and it dies with the
machine; attach a PVC and it survives. Same image either way.

How `auto` picks, and why it is safe:

1. A volume already labelled `k0smos-data` is mounted as-is — the steady state
   after the first boot.
2. Otherwise it looks for a device with **no recognised filesystem**, so the root
   and the cloud-init drive can never be selected.
3. Exactly one blank device is formatted. Zero is not an error — k0s then uses the
   root filesystem. **More than one is refused**, not guessed at.

k0smos never formats a device that already has a filesystem.

## Script environment variables

| Variable | Used by | Meaning |
|---|---|---|
| `ARCH` | all | Target arch (`aarch64`/`x86_64`); defaults to the host |
| `IMG` | run-qemu | ext4 disk to switch onto; omit to stay on the initramfs |
| `INITRAMFS`, `KERNEL` | run-qemu | artifact paths |
| `MEM`, `CPUS` | run-qemu | guest sizing |
| `SERIAL` | run-qemu | `stdio` (default) or a file path for headless runs |
| `CONTROL` | run-qemu | control socket path (default `dist/control.sock`) |
| `MONITOR` | run-qemu | optional QEMU monitor socket |
| `CIDATA` | run-qemu | cloud-init drive (ISO) to attach |
| `DATA`, `DATA_SIZE` | run-qemu | data volume to attach; created blank at `DATA_SIZE` (default `4G`) if absent |
| `NET_ARGS` | run-qemu | replaces the default `k0smos.ip=…` cmdline fragment |
| `API_PORT` | run-qemu | host port forwarded to the guest's 6443; unset forwards nothing |
| `CLUSTER_NET`, `CLUSTER_MAC` | run-qemu | second NIC on a shared Ethernet segment: `host:port` of a hub (`internal/nethub`) and this guest's address on it. How several guests on one host reach each other |
| `ROOT` | run-qemu | overrides `k0smos.root=` (default `LABEL=k0smos`) |
| `EXEC` | run-qemu | sets `k0smos.exec=` |
| `K0S_BIN` | mkrootfs, mkinitramfs | k0s binary to bake in |
| `K0S_VERSION` | fetch-k0s | release tag; defaults to latest |
| `FSLABEL`, `APK_PKGS` | mkrootfs | filesystem label (default `k0smos`), extra userspace packages |
| `MODULES_DIR` | mkrootfs, mkinitramfs | module tree to bundle |
| `PUSH`, `REGISTRY`, `TAG` | mkoci | push the OCI image, and where to |
| `MARKER`, `TIMEOUT`, `LOG` | acceptance | readiness pattern, deadline, log path |
| `K0SMOS_E2E_KEEP_CONSOLE` | e2e | `1` keeps guest consoles for passing tests too |

## Tests

```bash
make test       # unit tests: no root, no VM
make e2e        # QEMU boots, no k0s
make e2e-full   # adds the k0s tests
```

The e2e suite asserts on console output and, after a clean shutdown, on the
guest's filesystem via `debugfs` — file contents, modes, symlinks, and that
`e2fsck` finds nothing to repair. Consoles from failures land in
`dist/e2e/<test>.console.log`.

`e2e-full` includes a three-node cluster: three `k0s controller --enable-worker`
guests on a shared Ethernet segment, joined with a token minted by the first, and
checked by querying the API for three Ready nodes. Fetch the k0s airgap bundle
first with `./image/fetch-airgap.sh` — without it every node pulls its images over
QEMU's user-mode network and the run takes tens of minutes instead of a few.

## Debugging

No shell and no SSH, so there are two ways in.

**Read the console.** Every init step is logged with a `k0smos:` prefix, and k0s
logs to the same console. `SERIAL=dist/console.log` captures it headlessly.

**Read the disk offline.** Container logs never reach the console, but the root is
a raw ext4 file, so `debugfs` reads it without mounting or root (Docker because
`debugfs` is not on macOS):

```bash
docker run --rm -v "$PWD/dist:/d" alpine:3.20 sh -c '
  apk add -q --no-cache e2fsprogs e2fsprogs-extra >/dev/null
  debugfs -R "ls /var/log/pods" /d/k0smos.img'
```

> **Shut the guest down cleanly first.** Killing QEMU leaves `Block bitmap
> checksum does not match`, which loses recent writes and makes directories read
> as empty — a diagnosis will silently mislead you.

[usage.md](docs/usage.md#when-something-goes-wrong) lists the console lines worth
recognising and two failures that look like something else.

## Limitations

- **Single node in practice.** The default workload is `k0s controller --single`.
  Roles and `--token-file` pass through from cloud-init, but no multi-node cluster
  has ever been run.
- **Cluster API has never been reconciled.** `examples/capi-kubevirt.yaml` was
  derived from the providers' Go types and never applied. The cloud-init contract
  it relies on is covered by e2e tests; the loop itself is not.
- **Bare metal is untested.** Driver autoloading makes it possible in principle
  but has only ever run against virtio. No partition table and no
  grow-on-first-boot, so the image must be written to a disk it fits.
- **amd64 is unverified past the initramfs.** Verified on arm64 under QEMU/HVF.
- **CI has never executed** — the repository has no remote.
- **`k0smosctl` talks to local guests only.** `kubeconfig`, `shutdown` and `reboot`
  use a virtio-serial control port that the QEMU runner attaches and a KubeVirt VMI
  does not. `gen` is host-side and works anywhere.
- **No upgrade path.** Rebuilding the disk image wipes the cluster. No A/B images.
- **Everything runs as root.** `/etc/passwd` exists only so k0s stops warning.

## Layout

```
cmd/k0smos/         PID1 entry point and boot sequence
cmd/k0smosctl/      host-side CLI: builds configuration drives
internal/sys/       every real syscall; other packages take a narrow interface
internal/mount/     pseudo-filesystem mounts
internal/module/    module loading: named set (dep + softdep + alias) plus
                    autoload by device modalias
internal/blkid/     UUID=/LABEL= resolution by probing superblocks directly
internal/iso9660/   reads and writes cloud-init ISOs without mounting them
internal/metadata/  cloud-init user-data/meta-data: parse, interpret, apply
internal/datavol/   the data volume: find, format once, reuse
internal/etcd/      giving up etcd membership on shutdown
internal/switchroot/ initramfs → real root
internal/cgroup/    cgroup2 unified hierarchy
internal/net/       loopback and static addressing
internal/dhcp/      DHCPv4 client with lease renewal
internal/config/    kernel cmdline parsing
internal/reaper/    zombie reaping
internal/supervise/ child supervision with backoff
internal/control/   host shutdown requests over virtio-serial
internal/power/     ACPI power button
internal/shutdown/  sync, unmount, remount-ro, reboot(2)
image/              kernel/k0s fetchers, image builders, QEMU runner
e2e/                QEMU boots asserting on console output and guest disks
examples/           Cluster API manifests (never reconciled — see Limitations)
```

Every package defines its own minimal interface over `internal/sys` and fakes it
in tests, so the logic is unit-testable with no root and no VM.
