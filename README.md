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
· [deployment/kubevirt.md](docs/deployment/kubevirt.md) (KubeVirt, Cluster API)

## Make targets

| Target | What it does |
|---|---|
| `make test` | unit tests — no root, no VM |
| `make ctl` | build `k0smosctl` into `dist/` (host platform) |
| `make vet` | vet for the host and for `GOOS=linux` |
| `make kernel` | fetch the Kata guest kernel |
| `make kernel-alpine` | fetch Alpine `linux-virt` + its module tree |
| `make k0s` | download the latest k0s release (~240 MB) |
| `make initramfs` / `make root` | build the boot initramfs / canonical EROFS root |
| `make metal` | build the one UEFI-bootable qcow2 consumed by Metal3 |
| `make artifacts` | build the KubeVirt kernelBoot inputs and embedded-root initramfs |
| `make smoke` | init-only boot, no k0s — the ~15s check while changing k0smos |
| `make boot` | rebuild and boot the single metal qcow2 with `k0smosctl` |
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
k0smosctl machine up --cidata cidata.iso
```

`--user-data <file>` passes a cloud-config through whole instead. Either way it is
checked with the same parser the node uses, so a drive that would be ignored —
malformed YAML, or a cloud-config missing its `#cloud-config` first line — is
refused before it can boot into a silently unconfigured machine. See
[usage/cloud-init.md](docs/usage/cloud-init.md) for more.

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
- **Physical hardware is untested.** The complete amd64 qcow2 has booted through
  OVMF/GRUB, found its GPT partitions, autoloaded drivers, mounted EROFS + `/var`,
  acquired DHCP and started k0s; the next evidence must come from real machines.
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
