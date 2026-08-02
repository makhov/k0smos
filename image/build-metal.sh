#!/usr/bin/env bash
# Build all inputs for the single Metal3 machine image, then package them into a
# firmware-bootable disk. Kept as one command so kernel/modules/root/initramfs
# cannot be assembled from different versions by accident.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"

arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) goarch=arm64; apkarch=aarch64 ;;
  x86_64 | amd64) goarch=amd64; apkarch=x86_64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

work=${METAL_WORK_DIR:-dist/metal-$apkarch}
mkdir -p "$work"
rootfs=${ROOTFS_IMAGE:-dist/k0smos.erofs}
initramfs=$work/initramfs.gz
raw=${METAL_RAW:-$work/k0smos-metal-$apkarch.raw}
qcow2=${METAL_OUTPUT:-dist/k0smos-metal-$apkarch.qcow2}

[ -s "$rootfs" ] || {
  echo "missing canonical root $rootfs — run 'make rootfs'" >&2
  exit 1
}

ARCH=$arch \
MODULES_DIR=dist/kernel-metal/$apkarch/lib/modules \
KERNEL_CONFIG=dist/kernel-metal/$apkarch/config \
EMBED_ROOT=none \
  "$here/mkinitramfs.sh" "$initramfs"

ARCH=$arch \
KERNEL=dist/kernel-metal/$apkarch/vmlinuz \
INITRAMFS=$initramfs \
ROOTFS_IMAGE=$rootfs \
METAL_QCOW2=$qcow2 \
  "$here/mkmetal.sh" "$raw"

echo "metal artifact: $qcow2"
