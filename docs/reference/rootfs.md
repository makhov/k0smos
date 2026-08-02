# The read-only root (erofs)

`ROOTFS=erofs` builds the root as a read-only erofs image instead of a sparse ext4
one, and `EMBED_ROOT=` puts that image *inside the initramfs*. The kernel and the
whole OS then travel as one artifact — 18 MB + 165 MB — with no root disk to attach,
publish or mismatch:

```bash
ROOTFS=erofs K0S_BIN=dist/k0s-$(go env GOARCH) ./image/mkrootfs.sh dist/k0smos.erofs
EMBED_ROOT=dist/k0smos.erofs ./image/mkinitramfs.sh
```

k0smos loop-attaches the image, detects that it is erofs (so no
`k0smos.rootfstype=` is needed) and `switch_root`s onto it read-only. A node reaches
`Ready` this way, covered by `TestEmbeddedEROFSRootBoots`.

If the initramfs does not embed the root, PID1 automatically waits for and mounts
the filesystem labelled `k0smos`. This is the metal path: GRUB only supplies
machine configuration, while root discovery remains part of the k0smos boot
contract. `k0smos.root=` is still available as an override for custom layouts.

Two things a read-only root forces:

- **A data volume is required, mounted at `/var`** — not `/var/lib/k0s`, because
  kubelet writes to `/var/lib/kubelet`. That is the same split Talos uses, with `/var`
  as its EPHEMERAL partition. `k0smos.data=auto k0smos.datadir=/var`.
- **`/etc` and `/usr/libexec` get a tmpfs overlay**, with the image's contents as the
  lower layer, since k0s creates and `chmod`s `/etc/k0s` and kubelet creates
  `/usr/libexec/k0s`. A cloud-init or k0smotron-supplied `k0s.yaml` lands in the upper
  layer and shadows the baked default. `/opt` is a symlink to `/var/opt` instead —
  containerd stages plugins there and CNI binaries are tens of megabytes, which
  belong on disk rather than in RAM.

erofs and not squashfs because the default kernel decides it: Kata's builds in erofs
and has no squashfs driver at all.

The metal wrapper uses Alpine 3.23's `linux-lts`: it carries `igb`, `ixgbe`,
`megaraid_sas` and the other broad hardware drivers, and provides EROFS as a
module. Those platform modules live in the metal initramfs rather than in the
root, preserving the root's byte identity with KubeVirt.

ext4 remains supported for direct-kernel development images and for the writable
data volume, so `mkfs.ext4` stays in the image.

This page is operational — which `ROOTFS` value produces what, and which kernels
can mount it. For the rationale — why a read-only root works at all — see
[Why a read-only root works at all](../design/decisions.md#why-a-read-only-root-works-at-all)
in the design doc.
