# What gets built

| Artifact | Built by | Contents |
|---|---|---|
| `dist/kernel/<arch>/vmlinuz` | `make kernel` | Kata guest kernel (monolithic, pinned). `make kernel-alpine` fetches Alpine `linux-virt` + a module tree instead |
| `dist/k0smos-initramfs.gz` | `make initramfs` | k0smos as `/init`, plus the module tree if the kernel has one |
| `dist/k0smos.erofs` | `make root` | Canonical immutable OS payload: k0smos, k0s, `/etc`; no platform modules |
| `dist/k0smos.img` | `make disk` | ext4 root: k0smos, k0s, `/etc` |
| `dist/k0smos-metal-<arch>.qcow2` | `make metal` | UEFI/GPT Metal3 image wrapping the canonical root plus writable `/var` |

The platform artifact boots through UEFI and GRUB into its initramfs; PID1 then
discovers and `switch_root`s onto its EROFS partition. The separate kernel,
initramfs and ext4 image are development/build inputs, not the local boot UX.
