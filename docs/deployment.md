# Deploying k0smos

What it takes to run k0smos somewhere other than the local QEMU setup, and what
is still missing for each target.

`k0smosctl`'s `kubeconfig`, `shutdown` and `reboot` are **local-only**: they use a
virtio-serial control port that `image/run-qemu.sh` attaches and a KubeVirt VMI
does not. On KubeVirt, stop a machine with `virtctl stop` — KubeVirt delivers ACPI
and k0smos watches the power button — and get a kubeconfig from the cluster's own
API server. `k0smosctl gen` is host-side and works anywhere, but Cluster API
generates the drive itself, so it is not needed there either.

Read [Limitations](../README.md#limitations) first. This is a working prototype;
the gaps below are real. For day-to-day use — booting, configuring, shipping
manifests, getting a kubeconfig — see [usage.md](usage.md).

## What you ship

Three artifacts, and a kernel cmdline. Nothing else — no package manager, no
configuration management, no SSH.

```
vmlinuz                  kernel (the Kata guest kernel via make kernel,
                         Alpine's via make kernel-alpine, or your own)
k0smos-initramfs.gz      k0smos as /init, plus the module tree if there is one
k0smos.img               ext4 root: k0smos, k0s, /etc, modules
```

Build them for the target architecture:

```bash
ARCH=x86_64 ./image/fetch-kernel.sh
ARCH=x86_64 ./image/fetch-k0s.sh
ARCH=x86_64 ./image/mkinitramfs.sh
ARCH=x86_64 K0S_BIN=dist/k0s-amd64 ./image/mkrootfs.sh dist/k0smos.img
```

> The amd64 path builds and boots the initramfs, but the full
> disk/`switch_root`/k0s chain has only been verified on arm64. Treat a first
> amd64 deployment as a test.

## KVM / libvirt

The closest target to what is verified. Direct kernel boot, so no bootloader is
involved:

```xml
<os>
  <type arch='x86_64' machine='q35'>hvm</type>
  <kernel>/var/lib/k0smos/vmlinuz</kernel>
  <initrd>/var/lib/k0smos/k0smos-initramfs.gz</initrd>
  <cmdline>console=ttyS0 k0smos.root=LABEL=k0smos k0smos.ip=dhcp</cmdline>
</os>
<devices>
  <disk type='file' device='disk'>
    <driver name='qemu' type='raw'/>
    <source file='/var/lib/k0smos/k0smos.img'/>
    <target dev='vda' bus='virtio'/>
  </disk>
  <interface type='network'>
    <source network='default'/>
    <model type='virtio'/>
  </interface>
  <!-- Optional: shutdown channel, if you would rather not rely on ACPI. -->
  <channel type='unix'>
    <source mode='bind' path='/var/lib/k0smos/control.sock'/>
    <target type='virtio' name='k0smos.control'/>
  </channel>
  <serial type='pty'><target port='0'/></serial>
</devices>
```

`virsh shutdown` works here: libvirt raises an ACPI power button event, which
k0smos honours. `virsh console` gives you the boot log.

With real KVM this boots considerably faster than the HVF setup used for
development.

## KubeVirt (and Cluster API via CAPK + k0smotron)

`make oci` builds one OCI image holding everything a node needs:

```bash
ARCH=x86_64 REGISTRY=ghcr.io/you TAG=v0 PUSH=1 make oci
```

`k0smos:<tag>` contains `/boot/vmlinuz`, `/boot/initramfs.gz` and the ext4 root at
`/disk/k0smos.img`. A VM references it **twice** — as
`spec.domain.firmware.kernelBoot.container` and as a `containerDisk` volume — which
KubeVirt supports and documents:

```yaml
firmware:
  kernelBoot:
    container:
      image: ghcr.io/you/k0smos:v0
      kernelPath: /boot/vmlinuz
      initrdPath: /boot/initramfs.gz
    kernelArgs: "console=ttyS0 k0smos.root=LABEL=k0smos k0smos.ip=dhcp k0smos.data=auto"
volumes:
  - name: root
    containerDisk:
      image: ghcr.io/you/k0smos:v0
```

One image rather than two because the kernel and the root cannot be versioned
independently: the root carries the module tree, so a mismatched pair produces the
skew k0smos reports at boot as "kernel and modules are out of step". It is also
pulled once instead of twice.

A complete Cluster API manifest set is in
[`examples/capi-kubevirt.yaml`](../examples/capi-kubevirt.yaml): `Cluster`,
`KubevirtCluster`, `K0sControlPlane`, `MachineDeployment`,
`K0sWorkerConfigTemplate` and both `KubevirtMachineTemplate`s. Field names come
from the providers' Go types, but the set has not yet been reconciled by a real
cluster — treat the first run as a test of the manifests too.

Three things that are easy to get wrong:

- **`virtualMachineBootstrapCheck.checkStrategy: none`.** CAPK defaults to `ssh`
  and reads the CAPI sentinel file over it. k0smos has no SSH, so without this
  machines never report bootstrapped even though the node joins.
- **CAPK attaches a config-drive, not NoCloud.** It uses
  `CloudInitConfigDrive`, so the drive is labelled `config-2` with the
  `openstack/latest/user_data` layout. k0smos handles both, and there is an e2e
  test for the config-drive path specifically because that is the one CAPI
  exercises.
- **Do not add a cloud-init volume yourself.** CAPK appends the volume and disk
  from the bootstrap Secret.
- **Attach a data volume** and pass `k0smos.data=auto`. The manifests use
  KubeVirt's `emptyDisk`, which shares the VMI's lifecycle: it survives a guest
  reboot and is discarded when the machine is replaced. That is what makes these
  nodes disposable without being diskless — kubelet gets a real filesystem, etcd
  survives an in-place reboot, and images are cached rather than re-pulled. Swap
  it for a `DataVolume`/PVC if you want it to outlive the machine.

Two more things to know:

- **Set `preInstalledK0s: true`** in the `K0sControllerConfig` /
  `K0sWorkerConfig` spec. k0smotron's `DownloadCommands` returns nothing when it
  is set, so no `curl`/`wget` commands are emitted — the image already ships k0s.
- **runcmd is interpreted, not executed** (see
  [architecture.md](architecture.md)). `k0s install <role>` becomes the
  supervised workload, `--force` is dropped, `--env KEY=VALUE` is applied to the
  child's environment, and anything else is logged `UNSUPPORTED`. Avoid
  `preK0sCommands`/`postK0sCommands` that assume a shell.

Deleting a VM raises an ACPI power button event, which k0smos honours by shutting
down cleanly, so no extra channel is needed.

## Bare metal

Works in principle, with two things to arrange.

**Booting.** Use PXE/iPXE or a bootloader that can load a kernel and initrd with
a cmdline. iPXE:

```
kernel http://boot.example.com/vmlinuz console=ttyS0 k0smos.root=LABEL=k0smos k0smos.ip=dhcp
initrd http://boot.example.com/k0smos-initramfs.gz
boot
```

GRUB works equally well (`linux` + `initrd` stanzas).

**Getting the root onto the disk.** `mkrootfs.sh` writes a bare filesystem image
with **no partition table**, so either write it to a partition:

```bash
dd if=k0smos.img of=/dev/sda1 bs=4M status=progress && sync
```

or use it as a whole-disk filesystem. Either way the label survives, so
`k0smos.root=LABEL=k0smos` finds it. There is no grow-on-first-boot: the
filesystem stays the size it was built at (content + 3 GB).

Shutdown on bare metal relies on the ACPI power button, which k0smos honours.

## Cloud

Feasible but the least exercised path.

- **Addressing** comes from DHCP (`k0smos.ip=dhcp`), which is what every major
  provider expects.
- **Boot** is the obstacle. Providers that allow direct kernel boot (OpenStack,
  some bare-metal clouds) work like the libvirt case. EC2 and similar boot
  through their own chain and would need a bootloader in the image, which is not
  built here.
- **Metadata is not wired up.** There is no IMDS or config-drive support, so
  hostname, SSH keys and user-data are not read. Set the hostname via
  `k0smos.hostname=` on the cmdline instead. Note IMDS could not solve
  addressing anyway — you need an address to reach it.
- **Stopping** an instance raises an ACPI power button event, which k0smos
  honours, so a provider-initiated stop is graceful.

## Per-machine configuration

Everything is on the kernel cmdline; see the
[options table](../README.md#kernel-cmdline-options). A typical fleet line:

```
console=ttyS0 k0smos.root=LABEL=k0smos k0smos.ip=dhcp k0smos.hostname=node-07
```

With DHCP this is identical on every machine except the hostname, which is what
makes fleet deployment practical. Static addressing needs a unique line per
machine:

```
k0smos.ip=10.0.0.20/24 k0smos.gw=10.0.0.1 k0smos.dns=10.0.0.53
```

## Operating it

**No shell, no SSH.** Plan for console access — serial, IPMI/SOL, or
`virsh console` — on every machine. That is your only live window.

**Diagnosis is offline.** Container logs never reach the console. Power the
machine down cleanly, then read the disk with `debugfs`; see
[Debugging](../README.md#debugging).

**Data — treat every machine as disposable.** k0s state lives in `/var/lib/k0s`
on the root filesystem, and nothing is designed to outlive the machine:

- On **KubeVirt**, a `containerDisk` is read-only and the guest writes to an
  ephemeral overlay discarded with the pod. The root is therefore never storage.
  Attach a data volume (`k0smos.data=auto`) and everything mutable lands there
  instead: with `emptyDisk` it survives a guest reboot and dies with the machine;
  with a `DataVolume`/PVC it outlives both.
- On **bare metal** the disk does persist across reboots, but Cluster API still
  replaces machines rather than repairing them, so do not rely on it.

Size the root for the container images a node will pull (`PAD_MB=`, default 3072
on top of the content). There is no A/B image scheme and no in-place upgrade:
roll machines instead.

**etcd membership is handled.** If you run the k0s control plane *on* k0smos
machines, every replacement is an etcd membership change — and because nothing
persists, a member that vanishes without leaving would sit in the member list
counting against quorum forever. k0smos therefore runs `k0s etcd leave` on
shutdown, while the controller is still up, before stopping anything. It is
skipped for workers and for `--single` (kine-backed) controllers, and a failure
is logged rather than blocking the shutdown: a cluster that has already lost
quorum cannot process the removal, and stopping is still correct.

**Kubernetes access.** The API server listens on 6443. The admin kubeconfig is
written inside the guest at `/var/lib/k0s/pki/admin.conf`; with no shell, the
practical way to retrieve it today is to power down and read it out of the image
with `debugfs`. Wiring that out properly is not done.

## Still missing for production

Roughly in the order worth fixing:

1. **A/B images and upgrades.** Nothing to roll forward or back today.
2. **Partition table and grow-on-first-boot**, so one image fits any disk.
3. **Multi-node.** The default workload is `k0s controller --single`. Roles and
   `--token-file` already pass through from cloud-init, but no multi-node cluster
   has been run, so treat it as unproven rather than supported.

Two entries have since been dealt with and are recorded here because the reasoning
matters:

- **Monolithic kernel** — done, and it is the default. Kata's guest kernel builds
  in virtio, ext4, overlayfs and netfilter, so the named-module class of failure
  does not arise. Hardware drivers on the modular path are autoloaded from
  `modules.alias` rather than named.
- **Config-drive / IMDS** — done for config-drive and NoCloud, including hostname
  and join tokens. Read in userspace, so it needs no kernel filesystem support.
  There is no IMDS client: CAPI attaches a drive, and nothing so far has needed
  the network path.
6. **A way to export the kubeconfig** that does not involve powering the machine
   off.
