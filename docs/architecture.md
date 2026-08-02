# k0smos architecture

Why the boot sequence is ordered the way it is. Nearly every step exists to
prevent a specific failure that was observed on a real boot; the ordering looks
arbitrary until you know what each one is for.

This is the *why*. For how to use the thing, see [usage.md](usage.md); for running
it on KubeVirt or Cluster API, [deployment.md](deployment.md).

## The boot chain

```
firmware/QEMU/KubeVirt
  └── kernel (Kata guest kernel by default, Alpine linux-virt, or your own)
        └── initramfs: k0smos as /init          ← PID1, pre-switch
              ├── mount /proc /sys /dev /run /tmp …
              ├── read /proc/cmdline
              ├── export PATH                    (mkfs and k0s both need it)
              ├── load modules: named set, then autoload by modalias
              ├── choose root: explicit override → embedded EROFS → LABEL=k0smos
              ├── mount it at /newroot
              └── switch_root ── exec /sbin/k0smos --switched-root
                    └── k0smos as /sbin/k0smos   ← PID1, post-switch
                          ├── mount anything that did not come across
                          ├── load modules (again; harmless if already in)
                          ├── prepare the data volume → /var/lib/k0s
                          ├── set up cgroup2
                          ├── loopback up
                          ├── network: DHCP or static, write /etc/resolv.conf
                          ├── read the cloud-init drive (no mount)
                          │     ├── write_files → syscalls
                          │     ├── runcmd → interpreted, never executed
                          │     └── meta-data may supply the hostname
                          ├── set hostname
                          ├── install SIGCHLD reaper
                          ├── watch control port + power button
                          ├── supervise the workload
                          │     (k0s controller --single, unless user-data
                          │      names a role and join token)
                          └── on shutdown request:
                                k0s etcd leave (controllers, not --single)
                                killall TERM → grace → KILL
                                sync → unmount → sync → remount / ro
                                reboot(2)
```

Two orderings in there are load-bearing and easy to get backwards: the data volume
is mounted before anything can write to `/var/lib/k0s`, and the cloud-init drive is
read after networking (so an HTTP source could later work) but before the hostname
is set (so it can supply one).

## Why a read-only root works at all

The block-backed root exists because kubelet cannot run on a ramfs: cadvisor asks the kernel
for filesystem statistics about the root device and a ramfs reports none. That
constraint is about the root being *block-backed*, not about it being *writable* — so
a read-only erofs image, loop-attached, satisfies it. That is how Talos ships its own
root, and it is why the OS can travel inside the initramfs instead of as a second
artifact.

What a read-only root does force is a writable layer for the paths that are written,
and the useful distinction is between paths that need to *exist* and paths that need
to be *written*:

| path | how | why |
|---|---|---|
| `/var/run` → `/run` | symlink in the image | containerd's NRI socket; a `mkdir` here is fatal |
| `/lib/modules` | empty dir in the image | kubelet bind-mounts it into kube-router, even with no modules |
| `/usr/libexec/kubernetes/…` | dirs in the image | the plugin prober only needs them present |
| `/etc` | tmpfs overlay | k0s creates and `chmod`s `/etc/k0s`; cloud-init writes here |
| `/usr/libexec` | tmpfs overlay | kubelet creates `/usr/libexec/k0s` |
| `/opt` → `/var/opt` | symlink to the data volume | CNI binaries are tens of MB — disk, not RAM |
| `/var` | the data volume itself | kubelet writes `/var/lib/kubelet`, not just `/var/lib/k0s` |

Overlays rather than plain tmpfs mounts, so the image's own `/etc/passwd` and baked
`k0s.yaml` stay visible underneath; a file written at runtime shadows them, which is
exactly what a user-supplied config wants.

Every row came from a boot that failed without it, which is why the list is this
shape rather than "mount tmpfs over everything".

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

### And why a named list is not enough

The list above is hand-written, which works for virtio and cannot work for real
hardware: it names 50 modules, and no list enumerates the NICs and HBAs of an
arbitrary machine. So after loading it, k0smos does what udev does — reads each
device's `modalias` from `/sys/devices` and matches it against the glob patterns
in `modules.alias`, which is the kernel's own index of what drives what:

```
alias virtio:d00000002v*                    virtio_blk
alias pci:v00008086d000010D3sv*sd*bc*sc*i*  e1000e
```

Two things about this were not obvious until the real file was read:

- **The patterns are globs**, so the exact-match lookup used for `crc32c` cannot
  serve them. Device aliases and name aliases live in the same file and need
  different treatment.
- **Discovery is not one-shot.** `virtio_pci` is *built into* Alpine's kernel and
  has no alias at all; the virtio devices it enumerates then appear with
  `virtio:…` modaliases, and those are what `virtio_blk` binds to. Where the
  transport is itself a module (`virtio_mmio`), its children do not exist until it
  is loaded. udev sees this as a stream of events; with no udev, the equivalent is
  to re-enumerate after each round and stop when nothing new loads.

Named and discovered modules share one pass of bookkeeping. Two independent
passes would re-submit an already-loaded driver, get `EEXIST` — which counts as
success — and report it as autoloaded, overstating the one thing the number
exists to measure. On a QEMU guest the honest figure is 2 drivers beyond the
named set, not the 5 the double-counting version claimed.

## Why runcmd is interpreted and never executed

Cluster API hands a machine its identity as cloud-init user-data: a bootstrap
provider (kubeadm, or k0smotron for k0s) renders a cloud-config, and the
infrastructure provider attaches it as a NoCloud ISO (`cidata`) or an OpenStack
config-drive (`config-2`). Reading it is what tells a machine whether it is a
control plane or a worker, and with which join token.

The same drive may carry a small `k0smos` network section. PID1 reads metadata
before bringing interfaces up, so clones of one immutable artifact can receive
different addresses without patching GRUB or rebuilding the root. Metadata is
still applied after the writable overlays and `/var` mount, because its
`write_files` entries must land on writable paths before k0s starts.

The drive is read **without being mounted**. `internal/iso9660` parses the image
straight off the block device: primary volume descriptor at sector 16, root
directory extent, directory records, and the Rock Ridge `NM` entries that carry
real filenames. It is read-only and needs no kernel filesystem support, so
`CONFIG_ISO9660_FS` is not a requirement — which is what makes monolithic guest
kernels usable, Kata's included. Malformed images must fail rather than panic,
because this parses a device PID1 does not control and a panic there is a boot
failure.

A drive that cannot be read is reported, and is deliberately distinguished from
one that simply does not carry a given file. The latter is normal — a drive
carries the NoCloud layout or the config-drive one, not both — so warning about it
would make every boot look broken. The former is not, and silence there is the
worst outcome available: a machine whose bootstrap data was dropped comes up
configured as though none was supplied, joins nothing, and offers no explanation.
`internal/iso9660` wraps `fs.ErrNotExist` for genuine absence so the two can be
told apart, and `TestCorruptCloudInitDriveStillBoots` boots a drive whose root
directory points past the end of the image to prove both halves: the warning
appears, and PID1 still reaches `supervising`.

This covers every config-drive k0smos will realistically meet, Ironic's included.
The spec permits ISO9660 or vfat, and the tooling only produces ISO9660: nova
defaults to it, openstacksdk builds Ironic's config-drives with
`genisoimage`/`mkisofs`/`xorrisofs`, and KubeVirt uses `xorrisofs`. A vfat drive
would fall back to `mount` and fail, since no vfat module is shipped — one line in
`internal/module` if one ever appears, as Alpine has vfat and the `nls_*`
codepages as modules.

But cloud-init assumes a machine k0smos deliberately is not: one with a shell
and a service manager. Rather than acquiring those, k0smos **interprets**
user-data instead of executing it — the same stance Talos takes, which supports
no cloud-init and no arbitrary boot-time commands at all, because machine state
should be a function of declared configuration.

Concretely:

- **`k0s install <role> …` followed by `k0s start`** is how providers register a
  systemd unit. k0smos supervises one child, so the install form is translated
  into the equivalent foreground command and service-manager calls are dropped.
- **File verbs are interpreted, not run.** `mkdir`, `chmod`, `chown` and `ln -s`
  become typed actions carried out with syscalls. This is why the image needs no
  coreutils: an earlier version exec'd them and every entry failed with
  `mkdir: executable file not found in $PATH`.
- **Everything else is refused and logged.** `curl`, `sed`, a custom script — all
  reported as `UNSUPPORTED runcmd` and skipped. Nothing named in user-data is
  ever exec'd.
- **The k0sleave shell blocks are skipped**, but what they were for is done
  natively: k0smos runs `k0s etcd leave` on shutdown (see below). That is the one
  command k0smos executes, and it is not user-data driven — the binary is the
  workload already being supervised and the subcommand is fixed in code.
- **Shell syntax is refused.** A bare-string runcmd is meant for a shell; one
  containing `|`, `>`, `&&`, `$(…)` and so on is skipped with a warning rather
  than mis-executed by naive word splitting.

The trade-off is deliberate: some provider's user-data may ask for something
k0smos ignores. A loud console line is better than half-applying a bootstrap, and
far better than shipping a scripting substrate into an image specified to have
none.

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

## Why etcd is left before anything is stopped

Nothing on a k0smos machine persists, and Cluster API replaces machines rather
than repairing them, so every stop is effectively a permanent departure. An etcd
member that disappears without leaving stays in the member list and counts
against quorum — a three-controller cluster would degrade with each replacement.

So `k0s etcd leave` runs first, before the child is signalled: a controller
cannot give up its membership once it has been stopped. It is bounded by a
timeout, because a cluster that has lost quorum cannot process the removal and
must not be able to keep the machine alive. Skipped for workers (no membership)
and for `--single`, which is kine-backed rather than etcd.

Verified on a single-node cluster, where etcd correctly *refuses*: `rejecting
member remove; started member will be less than quorum`. That is the request
reaching etcd and being declined on quorum grounds, not a plumbing failure —
k0smos logs it and shuts down cleanly regardless.

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
