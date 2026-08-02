# Bare metal and Metal3

Bare-metal machines use the complete UEFI/GPT disk image from a k0smos release.

| Format | Use |
|---|---|
| `k0smos-metal-<arch>.qcow2` | Metal3/Ironic or virtualization platforms |
| `k0smos-metal-<arch>.raw.zst` | decompress and write directly to a disk |

Both formats contain the same partitions and immutable system payload. The
metal wrapper uses a hardware-oriented kernel and includes its driver modules in
the initramfs.

## Metal3 image

Point the machine template at the qcow2 and its adjacent checksum file:

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

Set the `BareMetalHost` boot mode to UEFI. Ironic writes the complete disk, and
the bootstrap provider attaches per-machine configuration separately.

An image-template example is available in
[`examples/capi-metal3-image.yaml`](https://github.com/makhov/k0smos/blob/main/examples/capi-metal3-image.yaml).

## Direct imaging

For hardware managed outside Ironic:

```bash
zstd -d k0smos-metal-x86_64.raw.zst -o k0smos-metal-x86_64.raw
sudo dd if=k0smos-metal-x86_64.raw of=/dev/<target> bs=16M conv=fsync status=progress
```

Verify the checksum and the target device before writing. This operation
overwrites the selected disk.

The machine still needs a supported cloud-init or config-drive source containing
its k0s role and any join token. The release image is intentionally generic.

## Drivers

The metal image uses a hardware-oriented kernel and carries its driver modules in
the initramfs. At boot k0smos matches each device's `modalias` against
`modules.alias`, the same way udev does, so storage controllers and NICs are
loaded without being named in advance.
