# Using k0smos

- [The CLI](#the-cli)
- [Boot a node locally](#boot-a-node-locally)
- [Configure a node with cloud-init](#configure-a-node-with-cloud-init)
- [Ship Kubernetes manifests](#ship-kubernetes-manifests)
- [Give it a data volume](#give-it-a-data-volume)
- [Reach the cluster from the host](#reach-the-cluster-from-the-host)
- [Shut it down](#shut-it-down)
- [Run it on KubeVirt](#run-it-on-kubevirt)
- [Run it on bare metal](#run-it-on-bare-metal)
- [When something goes wrong](#when-something-goes-wrong)

Throughout: k0smos has **no shell and no SSH**. Everything a machine needs is
supplied before it boots, and everything it reports comes out of the console.
That is the design, not a gap.

## The CLI

`k0smosctl` runs on the host and covers the routine work. Build it once:

```bash
make ctl        # -> dist/k0smosctl
```

| Command | What it does |
|---|---|
| `k0smosctl gen` | write a cloud-init drive for a node to boot with |
| `k0smosctl kubeconfig` | fetch the admin kubeconfig from a running node |
| `k0smosctl shutdown` | stop a node cleanly |
| `k0smosctl reboot` | restart a node cleanly |

Each takes `--help`. Two things it deliberately replaces: building an ISO with
`xorriso`, and reading a kubeconfig off a stopped guest's disk with `debugfs` —
neither of which is installed on macOS, so both meant Docker.

The node has no SSH and no shell. `gen` configures it before it boots; the other
three talk to a running one over its virtio-serial control port.

## Boot a node locally

Needs Go 1.25+, QEMU, and Docker (the ext4 image and the kernel unpack both need
Linux tools). Works on Apple Silicon via HVF and on linux/KVM.

```bash
make boot
```

That fetches the kernel and the latest k0s release, builds the initramfs and a
~3.3 GB ext4 root, and boots a single-node controller. Give it a minute or two;
`k0s controller --single` has a lot to start.

While iterating on k0smos itself, use the fast path instead — it boots the init
alone with no k0s at all, in about 15 seconds:

```bash
make smoke
```

**If you change any k0smos code, rebuild both images.** After `switch_root`,
k0smos re-execs `/sbin/k0smos` from the ext4 root, so everything after the pivot
runs the binary in `dist/k0smos.img`, not the one in the initramfs:

```bash
./image/mkinitramfs.sh
K0S_BIN=dist/k0s-$(go env GOARCH) ./image/mkrootfs.sh dist/k0smos.img
```

Rebuilding only the initramfs tests stale code that boots perfectly. It cost real
debugging time to notice.

## Configure a node with cloud-init

This is the whole configuration interface. `k0smosctl` builds the drive — it
writes the ISO itself, so there is no `xorriso` and no Docker involved:

```bash
make ctl

# put files on the node, taking their permissions from the source file
./dist/k0smosctl gen --file k0s.yaml:/etc/k0s/k0s.yaml --hostname demo-node -o dist/cidata.iso

CIDATA=dist/cidata.iso make boot
```

For a cloud-config you have written or rendered elsewhere, pass it whole
(`-` reads stdin):

```bash
./dist/k0smosctl gen --user-data cloud-config.yaml --hostname demo-node -o dist/cidata.iso
```

It checks what it generates with the same parser the node uses, so a drive that
would be ignored is refused here rather than booting into a machine that comes up
silently unconfigured. That catches malformed YAML, and also cloud-config missing
its `#cloud-config` first line — which k0smos ignores by design, so writing one
produces a drive with no effect. Nothing is written when it refuses.

Building the drive by hand still works if you prefer — the format is nothing
special:

```bash
xorriso -as mkisofs -V cidata -r -o dist/cidata.iso /tmp/cidata
```

`-r` (Rock Ridge) is required; `-J` (Joliet) is not.

Rock Ridge is what preserves the name `user-data`, whose hyphen is outside the
ISO9660 charset.

The drive is read **without being mounted** — k0smos parses the ISO itself — so no
kernel filesystem support is involved. An OpenStack config-drive works too:
label it `config-2` and use `openstack/latest/user_data` and
`openstack/latest/meta_data.json`.

### What is supported

**`write_files`** — `path`, `content`, `permissions`, and `encoding` of `b64`,
`gzip+base64` (or `gz+b64`), or plain. Parent directories are created. Bare
`gzip` without base64 is *not* supported: content arrives as a JSON/YAML string
and raw deflate bytes do not survive that.

**`meta-data`** — `local-hostname` sets the hostname, beating `k0smos.hostname=`
on the cmdline. `instance-id` is read and otherwise unused.

**`runcmd`** — **interpreted, never executed.** Nothing named in user-data is ever
exec'd. Four verbs are carried out with syscalls: `mkdir` (with `-p`), `chmod`,
`chown`, and `ln -s`. A `k0s install <role> …` is translated into the equivalent
foreground command, since k0smos supervises one process instead of registering a
systemd unit, and `--env KEY=VALUE` is lifted into the child's environment.
`systemctl` calls are dropped silently. Everything else — `curl`, `sed`, a script,
or any string containing `|`, `>`, `&&`, `$(…)` — is refused and logged as
`UNSUPPORTED runcmd`.

If a provider's user-data depends on something in that last category, the machine
will boot and tell you what it ignored. It will not half-apply it.

## Ship Kubernetes manifests

k0s applies anything under `/var/lib/k0s/manifests/<stack>/`, so a manifest is just
a file — no shell, no `kubectl apply`, nothing to run on the node:

```bash
./dist/k0smosctl gen \
  --file ns.yaml:/var/lib/k0s/manifests/demo/ns.yaml \
  --file deployment.yaml:/var/lib/k0s/manifests/demo/deployment.yaml \
  -o dist/cidata.iso
```

The file must sit in a **subdirectory** of `manifests/`; that directory name is the
stack. k0smos writes it before starting k0s, so it is applied on the first
reconcile — and because nothing persists, it is rewritten and reapplied on every
boot, which makes it idempotent by design.

Writing the cloud-config yourself, which is what a Cluster API bootstrap provider
does, the same thing is:

```yaml
#cloud-config
write_files:
  - path: /var/lib/k0s/manifests/demo/ns.yaml
    permissions: "0644"
    encoding: gzip+base64
    content: <gzip -c ns.yaml | base64 -w0>
```

`gzip+base64` matters there and not here: a provider delivers user-data through a
Secret or a metadata service with size limits, whereas a drive written locally has
no practical limit, so `gen` uses plain base64.

## Give it a data volume

Without one, k0s writes to the root filesystem, which is fine for a throwaway
boot. With one, `/var/lib/k0s` is a separate volume that survives a guest reboot
— which is what lets etcd survive an in-place restart and images stay cached.

```bash
DATA=dist/data.img DATA_SIZE=8G make boot
```

The image is created blank if it does not exist. Then on the guest side:

```
k0smos.data=auto
```

`auto` picks the one blank device. The safety rule is absolute: **k0smos never
formats a device that already has a filesystem**, and if the choice is ambiguous
it refuses and says so rather than guessing. A volume it formatted once is
recognised by label on later boots and reused.

Other forms: `k0smos.data=/dev/vdb`, or `LABEL=`/`UUID=`. `k0smos.datadir=`
changes the mountpoint, `k0smos.datafstype=` the filesystem.

## Reach the cluster from the host

`run-qemu.sh` forwards 6443, and `k0smosctl` asks the running node for its
kubeconfig over the control port — no shell on the guest, no shutting it down
first:

```bash
./dist/k0smosctl kubeconfig -o kubeconfig
KUBECONFIG=kubeconfig kubectl get nodes
```

The server address is rewritten from `localhost` (right on the node, wrong
everywhere else) to `127.0.0.1`, which is where the forward lands. `--server ''`
keeps what the node wrote; `-o -` prints instead of writing a file. The file is
written 0600, because it is a cluster-admin credential.

If the cluster has not written its PKI yet you get a clear error naming
`admin.conf` rather than an empty file.

> Whoever can write to the control port obtains cluster-admin. That is not a new
> exposure — the same channel stops the machine — but do not expose the port
> anywhere the disk is not equally exposed.

Reading it off the disk still works and needs no running guest, which is
occasionally what you want:

```bash
./dist/k0smosctl shutdown
docker run --rm -v "$PWD/dist:/d" alpine:3.20 sh -c \
  'apk add -q --no-cache e2fsprogs e2fsprogs-extra >/dev/null &&
   debugfs -R "cat /var/lib/k0s/pki/admin.conf" /d/k0smos.img 2>/dev/null' \
  | sed 's/localhost/127.0.0.1/' > kubeconfig
```

Docker because `debugfs` is not on macOS. If the data volume is separate, read
`/var/lib/k0s` from that image instead — the root image will only have the empty
mountpoint.

## Shut it down

**Do not kill QEMU.** A hard kill leaves the ext4 journal unreplayed and any later
disk inspection will lie to you.

```bash
./dist/k0smosctl shutdown        # or: k0smosctl reboot
./image/poweroff.sh              # the same thing, without building the CLI
```

Both write to a virtio-serial control port. A real hypervisor or machine uses the
ACPI power button instead, which k0smos also watches — the control port exists
because QEMU's arm64 `virt` machine with direct kernel boot has no ACPI at all.
`SIGTERM`/`SIGINT` work too.

On the way down k0smos runs `k0s etcd leave` **before** stopping the child (a
controller cannot give up membership once stopped), then kills everything, syncs,
unmounts deepest-first, and remounts `/` read-only. The evidence that it worked is
that `e2fsck -fn` afterwards is completely silent — no journal to replay.

Skipped for workers and for `--single`, which is kine-backed rather than etcd.

## Run it on KubeVirt

Boots and has been verified as a VM; the CAPI loop around it has **not** been
reconciled. Build the OCI artifacts:

```bash
ARCH=x86_64 make oci                              # for an amd64 cluster
PUSH=1 REGISTRY=ghcr.io/you TAG=v0 make oci
```

That produces `k0smos-boot` (kernel at `/boot/vmlinuz`, initramfs at
`/boot/initramfs.gz`) and `k0smos-disk` (the ext4 root at `/disk/k0smos.img`, where
KubeVirt looks for a containerDisk). `mkoci.sh` prints a matching VM spec when it
finishes, and `image/kubevirt-vm.yaml` is a worked example. The shape that matters:

```yaml
spec:
  domain:
    firmware:
      kernelBoot:
        container:
          image: ghcr.io/you/k0smos-boot:v0
          kernelPath: /boot/vmlinuz
          initrdPath: /boot/initramfs.gz
        kernelArgs: "console=ttyS0 k0smos.root=LABEL=k0smos k0smos.ip=dhcp k0smos.data=auto"
```

Note `k0smos.data=auto` with an `emptyDisk`: that shares the VMI's lifecycle, so it
survives a guest reboot and is discarded when the machine is replaced. Swap it for
a PVC if you want it to outlive the machine.

**`k0smosctl`'s node commands do not reach a KubeVirt VM.** `kubeconfig`,
`shutdown` and `reboot` speak to a virtio-serial control port, which the local
QEMU runner attaches and a KubeVirt VM does not. There, shutdown is
`virtctl stop` (KubeVirt delivers ACPI, which k0smos watches for), and the
kubeconfig comes from wherever the cluster API server is reachable — or from the
disk offline. Attaching a port to a VMI spec would make them work, but nothing
here does that yet.

See [deployment.md](deployment.md) for the Cluster API manifests and the three
things that are easy to get wrong there — including `checkStrategy: none`, without
which machines never report bootstrapped because CAPK tries to read a sentinel file
over SSH that does not exist here.

## Run it on bare metal

**Untested.** The pieces are in place and nothing has run. What you need:

1. **A kernel with drivers for your hardware.** Not the default — that is a Kata
   *guest* kernel with no NVMe, ATA, SCSI, USB or physical NIC support. Alpine's
   `linux-virt` covers NVMe, ATA, SCSI, USB-storage, `e1000e` and `mlx5`, but not
   `igb`, `ixgbe`, `i40e`, `bnxt` or `megaraid_sas`. For anything else, supply a
   distro kernel with a module tree — you do not need to build one. See
   [Which kernel](../README.md#which-kernel).
2. Point `MODULES_DIR` at that tree when building the images. Drivers are then
   autoloaded by matching each device's `modalias` against `modules.alias`, so you
   do not have to name them.
3. Accept that the image writes **no partition table** and does not grow on first
   boot, so it must be written to a disk it fits.

## When something goes wrong

The console is the only interface, and k0smos is deliberately talkative. Lines
worth recognising:

| Line | Meaning |
|---|---|
| `no module tree; assuming a monolithic kernel` | Normal on the default kernel |
| `warn: /lib/modules has module trees […] but none for the running kernel` | Kernel and module tree are out of step — the images were built against a different kernel |
| `reading /dev/vdX (iso9660, LABEL=cidata) directly, no mount` | The cloud-init drive was found and is being parsed |
| `metadata: could not read user-data` | The drive was found but is unreadable — bootstrap data was **not** applied |
| `refusing to guess: N blank devices` | `k0smos.data=auto` with more than one candidate; name the device explicitly |
| `UNSUPPORTED runcmd […]` | A user-data command k0smos will not execute |
| `warn: dhcp on eth0` | No lease; the node has loopback only and cannot pull images |

Two failures that look like something else:

- **kube-router crashlooping with `failed to synchronize cache`** is usually *not*
  a kube-router problem. It means no service rules were programmed, so the
  `kubernetes` ClusterIP does not work and it cannot reach the API. Look for a
  missing netfilter module instead.
- **DNS timeouts under QEMU on macOS.** slirp's resolver never answers. Pass
  `k0smos.dns=1.1.1.1`; the local boot scripts already do.

For e2e failures, guest consoles are saved to `dist/e2e/<test>.console.log`. They
are kept on failure only — set `K0SMOS_E2E_KEEP_CONSOLE=1` to keep them for
passing tests too, which is the only way to tell a passing assertion from one that
was skipped.
