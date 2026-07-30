# k0smos

A minimal Go PID1 that boots a single-node [k0s](https://k0sproject.io) Kubernetes
cluster inside a VM. No shell, no busybox, no systemd — the image contains
k0smos, k0s, and a handful of libraries.

k0smos performs OS init (mount pseudo-filesystems, load kernel modules, set up
cgroup2, configure networking), switches onto a real root filesystem, then
supervises `k0s controller --single` for the life of the machine and shuts down
cleanly when asked.

**Status: working prototype.** A node reaches `Ready` and shuts down leaving a
consistent filesystem, verified on Apple Silicon under QEMU/HVF. See
[Limitations](#limitations) before relying on it.

## Quick start

Requirements: Go 1.25+, QEMU, and Docker (only to build the ext4 image and
unpack the kernel — both need Linux tools).

```bash
make smoke     # ~15s: boots the init alone, no k0s. Use this while iterating.
make boot      # the real thing: k0smos as PID1 running single-node k0s
```

`make boot` downloads an Alpine kernel and the latest k0s release, builds an
initramfs and a ~3.3 GB ext4 root image, and boots it. Console is interactive;
`Ctrl-a x` quits QEMU.

To stop the guest **cleanly** (do not kill QEMU — see [Debugging](#debugging)):

```bash
./image/poweroff.sh          # or CMD=reboot ./image/poweroff.sh
```

You should see:

```
k0smos: starting as PID1 (switched-root=false)
k0smos: pseudo-filesystems mounted
k0smos: kernel modules loaded from /lib/modules/6.6.142-0-virt
k0smos: resolved LABEL=k0smos to /dev/vda
k0smos: mounted /dev/vda at /newroot, switching root
k0smos: starting as PID1 (switched-root=true)
k0smos: cgroup2 hierarchy ready
k0smos: loopback up
k0smos: eth0 configured 10.0.2.15/24 gw 10.0.2.2
k0smos: hostname set to "k0smos"
k0smos: supervising [/usr/local/bin/k0s controller --single]
```

## What gets built

Three artifacts, in `dist/`:

| Artifact | Built by | Purpose |
|---|---|---|
| `kernel/<arch>/vmlinuz` + `lib/modules` | `make kernel` | Alpine `linux-virt` kernel and its modules |
| `k0smos-initramfs.gz` | `make initramfs` | k0smos as `/init`, plus the module tree |
| `k0smos.img` | `make disk` | ext4 root: k0smos, k0s, `/etc`, modules |

Boot always goes through the initramfs, because a stock distro kernel ships
`virtio_blk` and `ext4` as *modules* and therefore cannot mount a disk root
unaided. k0smos loads those, then `switch_root`s onto the ext4 image. See
[docs/architecture.md](docs/architecture.md) for why each step is where it is.

## Make targets

| Target | What it does |
|---|---|
| `make test` | `go test -race ./...` |
| `make vet` | vet for the host and for `GOOS=linux` |
| `make build` | static `linux/amd64` binary into `dist/k0smos` |
| `make kernel` | fetch kernel + modules for the host arch |
| `make k0s` | download the latest k0s release binary (~240 MB) |
| `make initramfs` | build the boot initramfs |
| `make disk` | build the ext4 root image |
| `make smoke` | init-only boot, 1 GB, no k0s |
| `make boot` | full boot: initramfs → switch_root → k0s |
| `make accept` | boot headless, wait for `Ready`, power off |
| `make clean-dist` | delete `dist/` |

## Kernel cmdline options

All are optional; k0smos boots with defaults if none are given.

| Option | Default | Meaning |
|---|---|---|
| `k0smos.root=` | *(none)* | Root filesystem to switch onto: `/dev/vda`, `UUID=…` or `LABEL=…`. Unset means stay on the initramfs — fine for a smoke test, but kubelet cannot run there. |
| `k0smos.rootfstype=` | `ext4` | Filesystem type for the above. |
| `k0smos.rootflags=` | *(none)* | Mount data string, e.g. `noatime`. |
| `k0smos.ip=` | *(none)* | `dhcp`, or a static CIDR like `10.0.0.20/24`. Unset leaves loopback only. |
| `k0smos.gw=` | *(none)* | Default gateway (static addressing only). |
| `k0smos.dns=` | *(none)* | Resolver written to `/etc/resolv.conf`. **Overrides the DHCP lease** when both are present. |
| `k0smos.iface=` | `eth0` | Interface to configure. |
| `k0smos.hostname=` | `k0smos` | Hostname, also sent as the DHCP hostname. |
| `k0smos.exec=` | `/usr/local/bin/k0s,controller,--single` | Supervised child, comma-separated (a cmdline value cannot contain spaces). |
| `k0smos.modules=` | *(built-in set)* | Comma-separated module list, or `none` to disable loading. |
| `k0smos.path=` | see below | `PATH` exported to children. |

`k0smos.path` defaults to
`/var/lib/k0s/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin`.
`/var/lib/k0s/bin` matters: it is where k0s stages the binaries it embeds
(containerd, runc, kubelet, iptables) at runtime.

The built-in module set covers virtio, ext4, overlayfs, the netfilter and nft
pieces kube-proxy needs, ipsets, veth/bridge, and the ACPI power button.
Modules absent from the running kernel are skipped, so the same list is safe on
a monolithic kernel.

## Script environment variables

`image/run-qemu.sh` and the builders are driven by environment variables:

| Variable | Used by | Meaning |
|---|---|---|
| `ARCH` | all | Target arch (`aarch64`/`x86_64`); defaults to the host |
| `IMG` | run-qemu | ext4 disk to switch onto; omit to stay on the initramfs |
| `INITRAMFS` | run-qemu | initramfs path |
| `KERNEL` | run-qemu | kernel image path |
| `MEM`, `CPUS` | run-qemu | guest sizing |
| `SERIAL` | run-qemu | `stdio` (default) or a file path for headless runs |
| `CONTROL` | run-qemu | control socket path (default `dist/control.sock`) |
| `MONITOR` | run-qemu | optional QEMU monitor socket |
| `NET_ARGS` | run-qemu | replaces the default `k0smos.ip=…` cmdline fragment |
| `ROOT` | run-qemu | overrides `k0smos.root=` (default `LABEL=k0smos`) |
| `EXEC` | run-qemu | sets `k0smos.exec=` |
| `K0S_BIN` | mkrootfs, mkinitramfs | k0s binary to bake in |
| `K0S_VERSION` | fetch-k0s | release tag; defaults to latest |
| `FSLABEL` | mkrootfs | filesystem label (default `k0smos`) |
| `APK_PKGS` | mkrootfs | userspace packages for the root image |
| `MODULES_DIR` | mkrootfs, mkinitramfs | module tree to bundle |
| `MARKER`, `TIMEOUT`, `LOG` | acceptance | readiness pattern, deadline, log path |

## Debugging

There is **no shell and no SSH** in the image — that is the design, not an
omission. Two consequences:

**Read the console.** k0smos logs each init step with a `k0smos:` prefix, and
k0s logs to the same console. `SERIAL=dist/console.log` captures it headlessly.

**Read the disk offline.** Container logs never reach the console, but the root
disk is a raw ext4 file, so `debugfs` can read it without mounting or root:

```bash
docker run --rm -v "$PWD/dist:/d" alpine:3.20 sh -c '
  apk add -q --no-cache e2fsprogs e2fsprogs-extra
  debugfs -R "ls /var/log/pods" /d/k0smos.img'
```

Then `cat` a specific container log:

```
debugfs -R "cat /var/log/pods/<pod>/<container>/0.log" /d/k0smos.img
```

This is how the real root causes in this project were found rather than guessed.

> **Shut the guest down cleanly before doing this.** Killing QEMU leaves the
> image with `Block bitmap checksum does not match`, which loses recent writes
> and makes directories read as empty — so a diagnosis will silently mislead
> you. Use `./image/poweroff.sh`.

## Limitations

Known and deliberate, as of this prototype:

- **Single node only.** It runs `k0s controller --single`; no join tokens, no HA.
- **Verified on arm64.** amd64 builds and boots the initramfs, but the full
  disk/`switch_root`/k0s path has not been run there.
- **No upgrade path.** k0s data persists in `/var/lib/k0s` across reboots, but
  rebuilding the disk image wipes the cluster. No A/B images.
- **Fixed image size.** `mkrootfs.sh` sizes the filesystem at content + 3 GB and
  writes no partition table; there is no grow-on-first-boot.
- **Everything runs as root.** `/etc/passwd` exists only so k0s stops warning.
- **Module fragility.** Correct operation depends on 50 named modules being
  present. A kernel that splits them differently will fail in obscure ways; a
  monolithic kernel would remove this class of problem.

For deploying outside the local QEMU setup, see
[docs/deployment.md](docs/deployment.md).

## Layout

```
cmd/k0smos/         PID1 entry point and boot sequence
internal/sys/       every real syscall; other packages take a narrow interface
internal/mount/     pseudo-filesystem mounts
internal/module/    kernel module loading (modules.dep + softdep + alias)
internal/blkid/     UUID=/LABEL= root resolution via ext4 superblocks
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
```

Every package defines its own minimal interface over `internal/sys` and fakes it
in tests, so the logic is unit-testable with no root and no VM.
