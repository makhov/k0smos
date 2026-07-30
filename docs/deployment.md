# Deploying k0smos

What it takes to run k0smos somewhere other than the local QEMU setup, and what
is still missing for each target.

Read [Limitations](../README.md#limitations) first. This is a working prototype;
the gaps below are real.

## What you ship

Three artifacts, and a kernel cmdline. Nothing else — no package manager, no
configuration management, no SSH.

```
vmlinuz                  kernel (yours, or Alpine's via make kernel)
k0smos-initramfs.gz      k0smos as /init, plus the module tree
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

KubeVirt needs two OCI artifacts, because it cannot direct-kernel-boot from the
same place it gets the root disk. `make oci` builds both:

```bash
ARCH=x86_64 REGISTRY=ghcr.io/you TAG=v0 PUSH=1 make oci
```

- `k0smos-boot:<tag>` — `/boot/vmlinuz` and `/boot/initramfs.gz`, referenced by
  `spec.domain.firmware.kernelBoot.container`
- `k0smos-disk:<tag>` — the ext4 root at `/disk/k0smos.img`, the containerDisk
  convention

`image/kubevirt-vm.yaml` is a complete working VM spec. The essentials:

```yaml
firmware:
  kernelBoot:
    kernelArgs: "console=ttyS0 k0smos.root=LABEL=k0smos k0smos.ip=dhcp"
    container:
      image: ghcr.io/you/k0smos-boot:v0
      kernelPath: /boot/vmlinuz
      initrdPath: /boot/initramfs.gz
```

Under Cluster API the same fields live in the `KubevirtMachineTemplate`, and
CAPK populates `cloudInitNoCloud` from the bootstrap Secret rather than you
writing it. Two things to know:

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

**Data.** k0s state lives in `/var/lib/k0s` on the ext4 root and survives
reboots. Rebuilding the disk image destroys the cluster. There is no A/B image
scheme, so plan upgrades as rebuild-and-restore, or do not upgrade machines you
care about yet.

**Kubernetes access.** The API server listens on 6443. The admin kubeconfig is
written inside the guest at `/var/lib/k0s/pki/admin.conf`; with no shell, the
practical way to retrieve it today is to power down and read it out of the image
with `debugfs`. Wiring that out properly is not done.

## Still missing for production

Roughly in the order worth fixing:

1. **Monolithic kernel.** Correct operation currently depends on 50 named
   modules existing under the names Alpine uses. Compiling virtio, ext4,
   overlayfs and netfilter into a kernel you control removes this entire class of
   failure — and would let `ip=dhcp` work as a fallback, since you would also set
   `CONFIG_IP_PNP`.
2. **A/B images and upgrades.** Nothing to roll forward or back today.
3. **Partition table and grow-on-first-boot**, so one image fits any disk.
4. **Config-drive / IMDS**, for hostname, SSH keys and k0s join tokens.
5. **Multi-node.** `k0s controller --single` only; no join tokens, no HA.
6. **A way to export the kubeconfig** that does not involve powering the machine
   off.
