#!/usr/bin/env bash
# Fetch an Alpine kernel for the target arch into dist/kernel/.
# KERNEL_PACKAGE defaults to linux-virt for VM/e2e use; metal images select
# linux-lts, whose much broader driver set is suitable for physical hardware.
# Uses docker because the kernel ships as an .apk; only tar/gzip are needed
# inside, so any linux image with busybox works.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) apkarch=aarch64; platform=linux/arm64 ;;
  x86_64 | amd64) apkarch=x86_64; platform=linux/amd64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac
kernel_root=${KERNEL_ROOT:-dist/kernel}
out="$repo/$kernel_root/$apkarch"
kernel_package=${KERNEL_PACKAGE:-linux-virt}
case "$kernel_package" in
  linux-virt) flavor=virt ;;
  linux-lts) flavor=lts ;;
  *) echo "unsupported KERNEL_PACKAGE=$kernel_package" >&2; exit 1 ;;
esac

command -v docker >/dev/null || { echo "docker required to unpack the .apk" >&2; exit 1; }
mkdir -p "$out"

docker run --rm --platform "$platform" -v "$out:/out" \
  -e KERNEL_PACKAGE="$kernel_package" -e KERNEL_FLAVOR="$flavor" \
  alpine:3.23 sh -c '
set -e
cd /tmp
apk fetch --no-cache -q "$KERNEL_PACKAGE"
mkdir -p x && tar -xzf "$KERNEL_PACKAGE"-*.apk -C x 2>/dev/null || true
cp "x/boot/vmlinuz-$KERNEL_FLAVOR" /out/vmlinuz
cp x/boot/config-* /out/config
# The matching module tree — k0smos loads virtio_net/ext4/overlay from it.
rm -rf /out/lib
mkdir -p /out/lib
cp -R x/lib/modules /out/lib/
'
echo "wrote $out/vmlinuz ($kernel_package)"
# Recorded so the initramfs-vs-disk decision stays checkable, not folklore.
grep -E '^CONFIG_(VIRTIO_BLK|EXT4_FS|BLK_DEV_INITRD)=' "$out/config" || true
