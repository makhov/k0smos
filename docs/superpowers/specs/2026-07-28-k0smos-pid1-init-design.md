# k0smos — PID1 init for a Talos-like k0s VM distro

**Date:** 2026-07-28
**Status:** Design approved, pre-implementation
**Repo:** `github.com/amakhov/k0smos` (name provisional, rename later)

## Goal

A minimal Linux `init` (PID1) that boots a single k0s node inside a VM with no
rootfs dependencies beyond the k0s binary. Talos-like posture: immutable-ish
image, **no shell, no ssh, no user exec** into the machine; the machine is
driven through the k0s/Kubernetes API and observed via serial console only.

k0smos owns the OS init responsibilities the kernel hands to PID1; k0s owns all
Kubernetes/container weight. The two are loosely coupled: k0smos **execs the k0s
binary as a supervised child**.

## Why exec k0s (not import it as a Go module)

Grounded in the k0s codebase (`github.com/k0sproject/k0s`, module Go 1.26):

1. **Controller orchestration is not importable.** `cmd/controller`'s
   `command.start` is unexported and depends on `internal/...` packages
   (`internal/supervised`, `internal/pkg/*`), which Go forbids importing from
   another module. Importing would mean reimplementing the controller component
   graph and maintaining it against k0s internal churn every release.
2. **k0s is self-extracting.** containerd/runc/kubelet/kube-apiserver/etcd/kine/
   konnectivity/iptables/keepalived are appended to the k0s ELF as a ZIP and
   extracted at runtime via `os.Executable()` (`pkg/assets/stage.go`). If k0s
   were imported into k0smos, `os.Executable()` would point at the k0smos binary
   and extraction would break unless the ZIP were also appended to k0smos.
   Exec'ing the real k0s binary makes all embedded binaries work with zero effort.
3. **Loose coupling.** k0s upgrades become a binary swap — no recompile, no
   version lock.

Note: k0s does **not** mount filesystems or reap arbitrary orphans; it only
supervises its own named child processes and does per-worker kernel-module/sysctl
tuning (`worker.KernelSetup`). As PID1, k0smos is responsible for base mounts and
zombie reaping before k0s starts.

## Architecture

```
cmd/k0smos/main.go        getpid()==1 gate, run init sequence, never exit
internal/mount/           pseudo-fs mounts (proc, sys, dev, run, tmp)
internal/cgroup/          cgroup2 unified mount + subtree_control
internal/net/             lo up (primary NIC via kernel ip= cmdline for MVP)
internal/reaper/          subreaper wait4(WNOHANG) loop
internal/signals/         PID1 signal handlers -> shutdown trigger
internal/supervise/       run k0s child, restart-on-crash w/ backoff, logs->console
internal/shutdown/        stop k0s, sync, unmount, reboot(2)
internal/config/          parse /proc/cmdline + optional config-drive
image/                    rootfs assembly, Makefile, QEMU test script
go.mod                    github.com/amakhov/k0smos
```

Language: Go, static, `CGO_ENABLED=0` — matches k0s.

### Design for isolation

Each `internal/*` package has one purpose, a small exported surface, and takes a
filesystem/syscall interface (see Testing) so it can be exercised without being
PID1. `main.go` wires them in sequence and holds no logic of its own beyond
ordering and fatal-error handling.

## Boot sequence (PID1)

1. Verify `getpid()==1`. If not, print a one-line explanation and exit non-zero
   (k0smos is an init, not a general CLI).
2. **Mounts:** `/proc` (proc), `/sys` (sysfs), `/dev` (devtmpfs — kernel
   auto-populates nodes), `/dev/pts` (devpts), `/dev/shm` (tmpfs), `/run`
   (tmpfs), `/tmp` (tmpfs). Idempotent: skip anything already mounted
   (check `/proc/self/mountinfo`).
3. **cgroup2:** mount `/sys/fs/cgroup` unified; write `+cpu +memory +pids +io`
   to `cgroup.subtree_control` so containerd/kubelet/runc get delegation.
4. **Net:** bring `lo` up. Primary NIC handled by kernel `ip=dhcp` cmdline param
   at boot for MVP (zero code). DHCP/static in k0smos is milestone 2.
5. **Seed `/etc`** (only entries not baked into the image): hostname,
   `/etc/hosts`, `/etc/resolv.conf`, `/etc/os-release`.
6. Install **reaper** goroutine and **signal handlers**.
7. Read **config** (below).
8. **Supervise** child: `k0s controller --single`. Restart-on-crash with
   capped backoff. Child stdout/stderr → console.
9. Block forever. On shutdown signal → graceful path.

## Config (MVP)

- `/etc/k0s/k0s.yaml` baked into the image (absent → k0s single-node defaults).
- Kernel cmdline `k0smos.*` params parsed from `/proc/cmdline` for knobs
  (hostname, role).
- Config-drive (labeled disk, ignition-style) deferred to milestone 2.

## Persistence

k0s state (`/var/lib/k0s`: etcd/kine data, certs) must survive reboot.

- **MVP:** single read-write ext4 root on virtio-blk; state lives on the disk.
- **Milestone 2 (immutable, Talos-like):** read-only squashfs root + separate
  read-write `/var` partition.

## Image + boot (MVP)

rootfs contents:

- `/sbin/k0smos`
- `/usr/local/bin/k0s` (self-extracting; carries all child binaries)
- `/etc/k0s/k0s.yaml`
- minimal `/etc` (hosts, resolv.conf, os-release)
- empty dirs: `/proc /sys /dev /run /var/lib/k0s /tmp`
- **no shell, no busybox**

Build: assemble rootfs dir → `mkfs.ext4` into a raw/qcow2 image.

Boot in QEMU with direct-kernel (no bootloader): an existing distro kernel with
virtio drivers, `-append "root=/dev/vda rw init=/sbin/k0smos ip=dhcp"`.

Milestone 2 swaps in a monolithic virtio kernel (drivers compiled in, DKMS for
the few out-of-tree modules) — purely additive, does not touch the init code.

## Shutdown

On SIGTERM/SIGINT (or a poweroff signal): stop the k0s child gracefully
(SIGTERM + timeout), `sync`, unmount writable filesystems, then `reboot(2)` with
`RB_POWER_OFF` (poweroff) or `RB_AUTOBOOT` (reboot). PID1 must never return; a
failed shutdown loops/halts rather than exiting.

## Testing

- **Fast loop (no VM):** init packages take a small filesystem/syscall interface
  (mount, mkdir, write, mountinfo read) so they can be unit-tested with a mock.
  The reaper is testable with a fake child that forks-and-orphans. The full init
  sequence can run under `unshare -m` / a container namespace for iteration
  without booting a VM.
- **Real proof (milestone-1 acceptance):** a headless QEMU boot script that
  polls until `k0s kubectl get nodes` reports the node `Ready`, reachable via
  serial console or an exposed port.

### Milestone-1 acceptance criteria

- QEMU boots the image → k0smos runs as PID1 → `k0s controller --single` starts
  → the node reaches `Ready`.
- No shell present in the image.
- SIGTERM produces a clean `sync` + unmount + poweroff.

## Milestones

1. **M1 (this spec):** k0smos PID1 boots single-node k0s in QEMU on a stock
   distro kernel, ext4 rw root. Acceptance criteria above.
2. **M2:** monolithic virtio kernel build + DKMS; immutable squashfs root + rw
   `/var`; config-drive; in-init networking.

## Out of scope (YAGNI for M1)

Multi-node/HA, custom kernel build, immutable rootfs, config-drive/ignition,
in-init DHCP/static networking, over-the-air upgrades, secure boot.
