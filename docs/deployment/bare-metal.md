# Bare metal

Build the single Metal3-facing artifact:

```bash
ARCH=x86_64 make metal
# dist/k0smos-metal-x86_64.qcow2
```

`make metal` produces `dist/k0smos-metal-<arch>.qcow2`, a complete UEFI/GPT disk:

```
ESP             GRUB, linux-lts, platform initramfs + hardware modules
K0SMOS-ROOT     the canonical read-only EROFS payload
K0SMOS-DATA     ext4 mounted at /var
```

It is a complete UEFI/GPT disk with a hardware-oriented `linux-lts` kernel,
platform modules in the initramfs, the same immutable EROFS root used by
KubeVirt, and an ext4 `/var`. Use it as a `format: qcow2` image in CAPM3; machine
role, token, hostname and network configuration still arrive from CAPI rather
than being baked into the disk.

Each upstream k0s release produces one same-tagged k0smos release set. For
example, k0s `v1.36.3+k0s.0` produces the k0smos GitHub release
`v1.36.3+k0s.0`, containing the qcow2 and its adjacent `.sha256` file for both
architectures. CAPM3 can consume the public release URLs directly (or the same
two files mirrored internally):

```yaml
spec:
  template:
    spec:
      image:
        url: https://github.com/makhov/k0smos/releases/download/v1.36.3%2Bk0s.0/k0smos-metal-x86_64.qcow2
        format: qcow2
        checksumType: sha256
        checksum: https://github.com/makhov/k0smos/releases/download/v1.36.3%2Bk0s.0/k0smos-metal-x86_64.qcow2.sha256
```

Set the `BareMetalHost` boot mode to UEFI. Ironic writes the image to the selected
root device; on boot, k0smos reads the CAPI config-drive and starts the requested
k0s controller or worker role.

The full amd64 image is firmware-tested under OVMF. Physical hardware remains
the next validation boundary, particularly platform-specific firmware and NICs.

Shutdown on bare metal relies on the ACPI power button, which k0smos honours.
