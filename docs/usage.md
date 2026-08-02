# Using k0smos

- [The CLI](#the-cli)
- [Create a local cluster](#create-a-local-cluster)
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
| `k0smosctl machine up` | create or start one local artifact-backed machine |
| `k0smosctl machine logs` | show a machine's console (`-f` to follow) |
| `k0smosctl machine list` | what machines exist, and which are running |
| `k0smosctl machine shutdown` / `reboot` | stop or restart a machine cleanly |
| `k0smosctl machine rm` | discard a stopped machine and its disk |
| `k0smosctl cluster create` | create a Ready local cluster from one artifact |
| `k0smosctl cluster rm` | cleanly stop and discard a local cluster and its network |
| `k0smosctl cluster kubeconfig` | fetch the admin kubeconfig from a running cluster |
| `k0smosctl cluster token` | mint a join token so another machine can join |

Each takes `--help`. Two things it deliberately replaces: building an ISO with
`xorriso`, and reading a kubeconfig off a stopped guest's disk with `debugfs` —
neither of which is installed on macOS, so both meant Docker.

The node has no SSH and no shell. `gen` configures it before it boots; machine and
cluster operations talk to a running one over its virtio-serial control port.

## Create a local cluster

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

## Boot a node locally

Needs Go 1.25+ to build the host CLI, plus QEMU. Docker is required only when
building an OS artifact locally. Works on Apple Silicon via HVF and on Linux/KVM.

To consume a release, build only the host CLI:

```bash
make ctl
```

Then boot with the CLI:

```bash
k0smosctl machine up
guest "default" running in the background (pid 7547)
  console:  k0smosctl machine logs -f
  cluster:  k0smosctl cluster kubeconfig -o kubeconfig   (API on :6443)
  stop it:  k0smosctl machine shutdown
```

**The guest runs in the background and the command returns.** A k0smos node has no
shell, so there is nothing to sit in front of. Its console goes to a file you read
with `k0smosctl machine logs` — `-f` follows it, `-n 50` shows the last fifty lines. Give it
a minute or two; `k0s controller --single` has a lot to start.

`--attach` stays in the foreground streaming the console, and there **ctrl-c shuts
the guest down cleanly** rather than killing it. (`--interactive` hands the terminal
to QEMU, whose only escape is `ctrl-a x` — which kills the guest. It exists for the
rare case of wanting the QEMU monitor; prefer `--attach`.)

Useful flags: `--release` pins a GitHub tag, `--cache-dir` relocates downloaded
artifacts, `--image` selects a local qcow2 without contacting GitHub, `--cidata`
attaches a configuration drive, and `--dry-run` prints the QEMU command instead
of running it. The artifact already contains its writable `/var` partition.

`machine up` uses the same verified release cache automatically:

```bash
k0smosctl machine up --arch amd64

# Or use a local build directly.
k0smosctl machine up --image dist/k0smos-metal-x86_64.qcow2 --arch amd64
```

Separate `--kernel`, `--initramfs`, `--disk`, `--data`, `--exec` and `--no-image`
belong to `--direct-kernel`, the low-level development and smoke-test path.

### Guests, and where their state lives

Each guest has a `--name` (default `default`) and its own directory under
`~/.local/state/k0smos/<name>/` — root disk, console log, control socket and a
little metadata. Nothing runtime is written into the working tree, so
`make clean-dist` cannot take a running machine's socket with it. `K0SMOS_STATE_DIR`
moves it elsewhere.

**The platform image is a template.** `machine up` clones it into the guest's directory the
first time and never writes to the image itself, which is what makes a second guest
one command:

```bash
k0smosctl machine up --name vm2 --api-port 7443
k0smosctl cluster kubeconfig --name vm2 -o kubeconfig2   # port comes from the machine
```

Machine lifecycle and cluster access commands take `--name`:

```bash
k0smosctl machine list                     # what exists, and what is running
k0smosctl machine logs -f --name vm2
k0smosctl machine shutdown --name vm2
k0smosctl machine rm --name vm2            # discard it; the next up re-clones the image
```

A later `machine up` of the same name reuses that guest's disk, so a reboot keeps the
cluster. `machine rm` throws it away, which is how these nodes are meant to be treated —
replaced rather than repaired.

This matters more than convenience. Booting the image in place would allow only one
guest per machine, and any copy taken afterwards would inherit that guest's cluster
identity: k0s writes its PKI on first boot, so two clones of a booted image come up
with the same CA and the same node UID. Cloning per guest makes that impossible.

While iterating on k0smos itself, the fast path skips k0s entirely and takes about
15 seconds:

```bash
make smoke
```

**If you change any k0smos code, rebuild the complete artifact.** After `switch_root`,
k0smos re-execs `/sbin/k0smos` from the EROFS root, so everything after the pivot
runs the binary in `dist/k0smos.erofs`, not `/init` in the initramfs:

```bash
make metal
```

Use the target rather than the scripts. There are two copies of the k0smos binary
in the build inputs — `/init`, and `/sbin/k0smos` inside the
root image, which is the one `switch_root` re-execs — and `mkinitramfs.sh` on its own
refreshes only the first. `make artifacts` rebuilds the root and then embeds it, in
that order.

Rebuilding only the initramfs tests stale code that boots perfectly. It cost real
debugging time to notice.

## Configure a node with cloud-init

This is the whole configuration interface. `k0smosctl` builds the drive — it
writes the ISO itself, so there is no `xorriso` and no Docker involved:

```bash
make ctl

# put files on the node, taking their permissions from the source file
k0smosctl gen --file k0s.yaml:/etc/k0s/k0s.yaml --hostname demo-node -o dist/cidata.iso

k0smosctl machine up --cidata dist/cidata.iso
```

For a cloud-config you have written or rendered elsewhere, pass it whole
(`-` reads stdin):

```bash
k0smosctl gen --user-data cloud-config.yaml --hostname demo-node -o dist/cidata.iso
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

**`k0smos`** — an optional cloud-config section with `ip`, `iface`, `gateway`,
and `dns`. These have the same meanings as their `k0smos.*` kernel parameters and
override only the fields present. They are read before networking is configured.
`cluster create` uses this to give each clone a distinct address on its second
NIC without changing the shared artifact.

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
k0smosctl gen \
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

## The data volume

The platform qcow2 already contains an ext4 `k0smos-data` partition mounted at
`/var`. Because `k0smosctl machine up` clones the complete artifact once per named
guest, etcd state and cached images survive a reboot without another flag.

An external data image is only part of the direct-kernel development path:

```bash
k0smosctl machine up --direct-kernel --data dist/data.img --data-size 8G
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

QEMU forwards 6443, and `k0smosctl` asks the running node for its
kubeconfig over the control port — no shell on the guest, no shutting it down
first:

```bash
k0smosctl cluster kubeconfig -o kubeconfig
KUBECONFIG=kubeconfig kubectl get nodes
```

The server address is rewritten from `localhost` (right on the node, wrong
everywhere else) to `127.0.0.1` and the guest's own forwarded port, which `machine up`
recorded — so a second guest's kubeconfig points at the second guest. `--server
host:port` overrides it, `--server ''` keeps what the node wrote, and `-o -` prints
instead of writing a file. The file is written 0600, because it is a cluster-admin
credential.

If the cluster has not written its PKI yet you get a clear error naming
`admin.conf` rather than an empty file.

> Whoever can write to the control port obtains cluster-admin. That is not a new
> exposure — the same channel stops the machine — but do not expose the port
> anywhere the disk is not equally exposed.

Reading it off the disk still works and needs no running guest, which is
occasionally what you want:

```bash
k0smosctl machine shutdown
docker run --rm -v "$PWD/dist:/d" alpine:3.20 sh -c \
  'apk add -q --no-cache e2fsprogs e2fsprogs-extra >/dev/null &&
   debugfs -R "cat /var/lib/k0s/pki/admin.conf" /d/k0smos.img 2>/dev/null' \
  | sed 's/localhost/127.0.0.1/' > kubeconfig
```

Docker because `debugfs` is not on macOS. If the data volume is separate, read
`/var/lib/k0s` from that image instead — the root image will only have the empty
mountpoint.

## More than one node

For local QEMU, prefer `k0smosctl cluster create`; it automates everything in
this section. The details below describe the provider-facing mechanism and are
useful when another system creates the machines.

A second machine joins with a token, which only a machine already in the cluster
can produce — it is signed with the cluster CA. `k0smosctl cluster token` asks a running
node for one over the control port, the same way `kubeconfig` does:

```bash
k0smosctl cluster token --role controller -o join-token   # or --role worker
```

The joining node needs three things: the token, its own address, and a k0s
configuration naming that address. Nodes have to reach each other, so give each
one an address on a network they share and tell kubelet to use it — left alone,
kubelet picks the address behind the default route and every node claims the same
one.

```yaml
# node2.yaml
#cloud-config
write_files:
  - path: /etc/k0s/k0s.yaml
    content: |
      apiVersion: k0s.k0sproject.io/v1beta1
      kind: ClusterConfig
      spec:
        api:
          address: 10.10.0.12
          sans: [10.10.0.11, 10.10.0.12, 127.0.0.1]
        storage:
          type: etcd
          etcd:
            peerAddress: 10.10.0.12
```

```bash
k0smosctl gen --user-data node2.yaml --hostname node2 \
  --file join-token:/etc/k0s/join-token -o node2.iso
```

`spec.api.address` and `spec.storage.etcd.peerAddress` are per-node: they say
which address this machine answers on, so they cannot come from the shared cluster
configuration. The SAN list has to cover every node's address, plus `127.0.0.1` if
you reach the API through a forwarded port.

The first node bootstraps the cluster and needs no token; the rest are booted with
`--token-file`:

```
k0smos.exec=/usr/local/bin/k0s,controller,--enable-worker,--no-taints,\
--config=/etc/k0s/k0s.yaml,--token-file=/etc/k0s/join-token,\
--kubelet-extra-args=--node-ip=10.10.0.12
```

`--enable-worker --no-taints` makes each node a control plane that also runs
workloads. Leave them off for a worker-only machine and use `--role worker` when
minting its token.

Three of these form an etcd quorum. `e2e/cluster_test.go` builds exactly that, and
is the place to look for a working set of arguments.

> A controller token confers control-plane membership on whoever holds it. Treat
> it like the kubeconfig above.

Locally, guests need a network they share: QEMU's user-mode networking puts every
guest behind its own NAT at the same address, where they cannot see each other.
`run-qemu.sh` takes `CLUSTER_NET=<host:port>` to give a guest a second NIC
connected to an Ethernet hub, and `CLUSTER_MAC=` to keep their addresses distinct
on it. `internal/nethub` is that hub, and the e2e suite runs one in-process; it
has to be listening before a guest starts, because QEMU does not retry. QEMU's own
answers do not work here — tap and vmnet want root, and its multicast backend is
silently dead on macOS. On real hardware or KubeVirt none of this arises: the
machines are already on a network.

## Shut it down

**Do not kill QEMU.** A hard kill leaves the ext4 journal unreplayed and any later
disk inspection will lie to you.

```bash
k0smosctl machine shutdown        # or: k0smosctl machine reboot
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

That produces one image, `k0smos:<tag>`, holding the kernel at `/boot/vmlinuz` and
the initramfs at `/boot/initramfs.gz`. The initramfs carries the immutable erofs
root, so the VM references the image once as its `kernelBoot` container; it does
not need a `containerDisk`.

One image rather than two because the kernel and the root are not independently
versionable: the root carries the module tree, so mixing versions produces the skew
k0smos warns about at boot. `mkoci.sh` prints a matching VM spec when it finishes,
and `image/kubevirt-vm.yaml` is a worked example. The shape that matters:

```yaml
spec:
  domain:
    firmware:
      kernelBoot:
        container:
          image: ghcr.io/you/k0smos:v0
          kernelPath: /boot/vmlinuz
          initrdPath: /boot/initramfs.gz
        kernelArgs: "console=ttyS0 k0smos.ip=dhcp k0smos.data=auto"
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

Build the single Metal3-facing artifact:

```bash
ARCH=x86_64 make metal
# dist/k0smos-metal-x86_64.qcow2
```

It is a complete UEFI/GPT disk with a hardware-oriented `linux-lts` kernel,
platform modules in the initramfs, the same immutable EROFS root used by
KubeVirt, and an ext4 `/var`. Use it as a `format: qcow2` image in CAPM3; machine
role, token, hostname and network configuration still arrive from CAPI rather
than being baked into the disk.

The full amd64 image is firmware-tested under OVMF. Physical hardware remains
the next validation boundary, particularly platform-specific firmware and NICs.

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
