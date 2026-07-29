#!/usr/bin/env bash
# Fetch an Alpine linux-virt kernel for the target arch into dist/kernel/.
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
out="$repo/dist/kernel/$apkarch"

command -v docker >/dev/null || { echo "docker required to unpack the .apk" >&2; exit 1; }
mkdir -p "$out"

docker run --rm --platform "$platform" -v "$out:/out" alpine:3.20 sh -c '
set -e
cd /tmp
apk fetch --no-cache -q linux-virt
mkdir -p x && tar -xzf linux-virt-*.apk -C x 2>/dev/null || true
cp x/boot/vmlinuz-virt /out/vmlinuz
cp x/boot/config-* /out/config
'
echo "wrote $out/vmlinuz"
# Recorded so the initramfs-vs-disk decision stays checkable, not folklore.
grep -E '^CONFIG_(VIRTIO_BLK|EXT4_FS|BLK_DEV_INITRD)=' "$out/config" || true
