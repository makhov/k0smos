# Which kernel

| | `make kernel` | `make kernel-alpine` | `make kernel-metal` |
|---|---|---|---|
| kernel | Kata guest | Alpine `linux-virt` | Alpine `linux-lts` |
| modules | none | VM-oriented set | broad hardware set, including EROFS |
| use | KubeVirt | modular-kernel tests | physical machines / Metal3 |

Kata's is a *guest* kernel: ideal for VMs, useless on bare metal. Nothing special
is needed to build against it — `make disk` and `./image/mkinitramfs.sh` handle a
missing module tree as "monolithic".

**Bring your own instead.** k0smos does not own a kernel; the fetch scripts only
fetch. Point `MODULES_DIR` at any distro kernel's module tree — you do not need to
build one. It must provide drivers for your hardware, `ext4`, `nf_tables` +
`nft_compat`, `overlayfs` and cgroup v2. It does **not** need any filesystem
support for the cloud-init drive.
