#!/usr/bin/env bash
# Verify the coupling between a kernelBoot initramfs and its immutable root.
# A missing or stale embedded root yields an OCI image which builds and pushes
# successfully but cannot start k0s, so this is deliberately a release gate.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) apkarch=aarch64 ;;
  x86_64 | amd64) apkarch=x86_64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

kernel=${KERNEL:-$repo/dist/kernel/$apkarch/vmlinuz}
initramfs=${INITRAMFS:-$repo/dist/k0smos-initramfs.gz}
rootfs=${ROOTFS_IMAGE:-$repo/dist/k0smos.erofs}

for artifact in "$kernel" "$initramfs" "$rootfs"; do
  [ -s "$artifact" ] || { echo "missing or empty artifact: $artifact" >&2; exit 1; }
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Extract only the embedded root. Accept both path spellings emitted by common
# cpio implementations, then compare bytes so a stale root cannot slip through.
(
  cd "$tmp"
  gzip -dc "$initramfs" | cpio --quiet -id 'k0smos-root.img' './k0smos-root.img'
)
embedded=$tmp/k0smos-root.img
[ -s "$embedded" ] || {
  echo "initramfs does not contain k0smos-root.img" >&2
  echo "build the erofs root before the initramfs" >&2
  exit 1
}
cmp -s "$rootfs" "$embedded" || {
  echo "initramfs contains a stale root; it does not match $rootfs" >&2
  exit 1
}

# EROFS magic is 0xe0f5e1e2 at byte 1024, stored little-endian. Checking it here
# prevents an ext4 disk from accidentally being embedded and consuming gigabytes
# of guest RAM.
magic=$(od -An -tx1 -j1024 -N4 "$rootfs" | tr -d ' \n')
[ "$magic" = e2e1f5e0 ] || {
  echo "$rootfs is not an erofs filesystem (magic: ${magic:-missing})" >&2
  exit 1
}

echo "verified kernelBoot artifacts: initramfs embeds the matching erofs root"
