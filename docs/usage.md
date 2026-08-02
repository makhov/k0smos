# Using k0smos

- [Run it on KubeVirt](#run-it-on-kubevirt)
- [Run it on bare metal](#run-it-on-bare-metal)
- [When something goes wrong](#when-something-goes-wrong)

Throughout: k0smos has **no shell and no SSH**. Everything a machine needs is
supplied before it boots, and everything it reports comes out of the console.
That is the design, not a gap.

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
