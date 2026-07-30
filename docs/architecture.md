# k0smos architecture

Why the boot sequence is ordered the way it is. Nearly every step exists to
prevent a specific failure that was observed on a real boot; the ordering looks
arbitrary until you know what each one is for.

## The boot chain

```
firmware/QEMU
  └── kernel (Alpine linux-virt, or your own)
        └── initramfs: k0smos as /init          ← PID1, pre-switch
              ├── mount /proc /sys /dev /run /tmp …
              ├── read /proc/cmdline
              ├── load kernel modules            (virtio_blk, ext4, …)
              ├── resolve k0smos.root (UUID=/LABEL= → /dev/…)
              ├── mount it at /newroot
              └── switch_root ── exec /sbin/k0smos --switched-root
                    └── k0smos as /sbin/k0smos   ← PID1, post-switch
                          ├── mount anything that did not come across
                          ├── load modules (again; harmless if already in)
                          ├── set up cgroup2
                          ├── loopback up
                          ├── network: DHCP or static
                          ├── set hostname, write /etc/resolv.conf
                          ├── export PATH
                          ├── install SIGCHLD reaper
                          ├── watch control port + power button
                          ├── supervise k0s controller --single
                          └── on shutdown request:
                                killall TERM → grace → KILL
                                sync → unmount → sync → remount / ro
                                reboot(2)
```

## Why an initramfs at all

The original design booted `root=/dev/vda init=/sbin/k0smos` directly. That
cannot work on a stock distro kernel: Alpine's `linux-virt` builds
`CONFIG_VIRTIO_BLK=m` and `CONFIG_EXT4_FS=m`, so the kernel cannot see the disk
or mount ext4 until something loads those modules. Every distro kernel is like
this, which is why every distro ships an initramfs.

`CONFIG_BLK_DEV_INITRD=y` is universal, so booting k0smos from an initramfs
works everywhere and needs no cooperation from the kernel build.

## Why switch_root, and why after module loading

kubelet cannot run on an initramfs. cadvisor asks the kernel for filesystem
statistics about the root device, and a ramfs root reports none, so kubelet dies
with:

```
Failed to start ContainerManager: failed to get rootfs info:
cannot find filesystem info for device "rootfs"
```

k0s then restarts it every ~15 seconds, forever. The fix is a real filesystem as
`/`, which is what `switch_root` provides.

The ordering is forced: modules must load **before** the switch, because the
kernel cannot see the root device until its storage drivers are in.

## Why the root is named by UUID/LABEL

On real hardware disks enumerate as `/dev/sda` or `/dev/nvme0n1` and can reorder
between boots, so a hard-coded path is not dependable.

Resolution reads ext4 superblocks directly instead of looking in
`/dev/disk/by-uuid`, because those symlinks are created by udev and k0smos runs
no udev. The device nodes themselves do exist — devtmpfs creates them. Block
devices are enumerated from `/sys/class/block`.

Resolution retries for ~15s: `virtio_blk` and friends probe asynchronously, so
the root can appear slightly after its module loads.

## Why networking is configured in userspace

The kernel's own `ip=` autoconfiguration is a `late_initcall` — it runs before
`/init`, so it cannot see a NIC whose driver k0smos loads as a module. On top of
that, Alpine's kernel does not build `CONFIG_IP_PNP` at all, so `ip=dhcp` is
inert regardless of driver timing.

That leaves userspace. Shipping `udhcpc` would add a second binary and in
practice a shell to an image specified to have neither, so k0smos includes a
small DHCPv4 client.

**It avoids AF_PACKET.** The usual way to speak DHCP without an address is a raw
packet socket, which means hand-building IP and UDP headers and checksums.
Instead the client sets the BOOTP broadcast flag, which RFC 2131 requires
servers to honour by broadcasting their reply — so a plain UDP socket bound to
`0.0.0.0:68` with `SO_BINDTODEVICE` can complete the exchange with no address
configured. `SO_BINDTODEVICE` is the key ingredient: it lets the kernel send to
`255.255.255.255` from `0.0.0.0` with no route present. This also avoids needing
the `af_packet` module (`CONFIG_PACKET=m` on this kernel).

Renewal follows RFC 2131: unicast to the issuing server at T1, broadcast rebind
at T2, full re-acquisition if the lease lapses. T1/T2 default to ½ and ⅞ of the
lease when the server omits them.

Note the ordering difference: DHCP needs the link **up first**, whereas static
addressing sets the address and then brings the link up.

`k0smos.dns=` overrides the lease's resolver. This is not cosmetic — QEMU's
slirp offers `10.0.2.3`, which accepts queries and never answers them on a macOS
host, so without the override every image pull fails with `i/o timeout`.

## Why PID1 must export a PATH

PID1 inherits no environment from anyone. With an empty `PATH`, k0s cannot exec
the binaries it stages into `/var/lib/k0s/bin` at runtime — it logs `Failed to
detect iptables mode` and kubelet reports `No iptables support on this system`.
`k0smos.path` therefore includes the staging directory.

## Why so many kernel modules

k0s needs more of the kernel than is obvious, and a missing module usually
surfaces as a confusing userspace error rather than "module not found":

- **`nf_tables` + `nft_compat`** — k0s selects iptables-nft mode. Without these
  kube-proxy dies with `iptables: Failed to initialize nft: Protocol not
  supported`.
- **REJECT (`nft_reject*`, `ipt_REJECT`, `nf_reject_ipv4`)** — kube-proxy's
  `KUBE-SERVICES` chain rejects traffic to services with no endpoints. Missing
  REJECT makes `iptables-restore` fail the *entire batch* with `RULE_APPEND
  failed (No such file or directory)`, so no service rules are programmed at all.
- **`xt_nfacct`** — `KUBE-FORWARD` counts invalid-conntrack drops with
  `-m nfacct`. `nfnetlink_acct` alone is not enough; the match module is what
  iptables needs.
- **`overlay`** — containerd's default snapshotter.
- **`button` + `evdev`** — the ACPI power button (below).

A downstream symptom worth remembering: kube-router crashlooping with `failed to
synchronize cache: 1m0s timeout` was **not** a kube-router problem. With no
service rules programmed, the `kubernetes` ClusterIP did not work, so it could
not reach the API. Chasing kube-router directly would have been chasing the
wrong component.

Module loading honours `modules.dep` for hard dependencies **and
`modules.softdep`** for soft ones, resolving names through `modules.alias`.
Soft dependencies are not optional in practice: `libcrc32c` fails with ENOENT
("unknown symbol") unless `crc32c` — provided by the `crc32c_generic` module via
an alias — is loaded first. A module that fails is logged and skipped rather
than aborting the list, so one bad module cannot cost you ext4, overlayfs and
netfilter.

## Why there are two shutdown triggers

A guest must be stoppable cleanly, or its filesystem is corrupted on every stop.

**The power button** (`internal/power`) is the mechanism on real hardware and
hypervisors: the ACPI button driver raises the press, evdev exposes it as
`/dev/input/eventN`, and k0smos watches every such device. This covers bare
metal, `virsh shutdown`, and a cloud provider's "stop instance".

**The control port** (`internal/control`) exists because the power button is
*unavailable* under QEMU's arm64 `virt` machine with direct kernel boot: there
is no UEFI, therefore no ACPI, therefore nothing for `system_powerdown` to
deliver. QEMU's device tree does expose a gpio-keys poweroff button, but
Alpine's kernel builds neither `CONFIG_GPIO_KEYS` nor `CONFIG_INPUT_KEYBOARD`.
`CONFIG_VIRTIO_CONSOLE=y` is built into every stock kernel, so a virtio-serial
port needs no module and works regardless.

Two non-obvious details in the control port:

- It is **reopened on EOF**. With no host client attached the port reads EOF
  immediately, so watching it once stops listening before anyone can send
  anything.
- A **closed channel is not a command**. Receiving from a closed Go channel
  yields the zero value; treating that as a request powered the machine off
  seconds into boot on the first attempt. The same hazard applies to the power
  button channel.

## Why shutdown kills everything before touching filesystems

`/` cannot be unmounted, so the only way to leave the root consistent is to
remount it read-only, which checkpoints the ext4 journal. That remount fails
with `EBUSY` while any process still holds the root — and k0s's children
(containerd, kubelet, pods) do. So the sequence is:

1. `kill(-1, SIGTERM)` — let daemons flush.
2. Grace period.
3. `kill(-1, SIGKILL)` — anything that ignored it.
4. `sync`.
5. Unmount writable filesystems **deepest-first** (a child mount must be
   detached before its parent), skipping pseudo-filesystems and `/`.
6. `sync`.
7. Remount `/` read-only.
8. `reboot(2)`.

The evidence that this works is that a read-only `e2fsck -fn` of the image
afterwards is completely silent — **no journal left to replay**. If the remount
had quietly failed, there would be one.

## Testability

`internal/sys` holds every real syscall. Every other package declares its own
narrow interface over the subset it needs and fakes it in tests, so all logic is
unit-testable with no root and no VM.

Two packages declare kernel constants locally rather than importing
`golang.org/x/sys/unix` — `internal/shutdown` (reboot commands, `MS_REMOUNT`)
and `internal/switchroot` (`MS_MOVE`) — so that they build and test on a non-Linux
dev machine. Each has a `_linux_test.go` asserting the local values match `unix`
on the real target. Those assertions compile on macOS but only *run* under
`GOOS=linux`.
