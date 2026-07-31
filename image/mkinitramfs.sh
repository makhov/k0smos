#!/usr/bin/env bash
# Build an initramfs (gzipped cpio) with k0smos as /init.
#
# Why an initramfs and not the ext4 disk image: stock distro kernels ship
# virtio_blk and ext4 as MODULES (verified: Alpine linux-virt has
# CONFIG_VIRTIO_BLK=m, CONFIG_EXT4_FS=m), so a kernel cannot mount an ext4
# root off /dev/vda unaided. An initramfs needs only CONFIG_BLK_DEV_INITRD=y,
# which every distro kernel has, so it boots anywhere with no module loading.
#
# Trade-off: the rootfs lives in RAM and nothing persists across a reboot.
# This is the local smoke-test path; mkrootfs.sh remains the persistent-disk
# path for a kernel with virtio/ext4 built in.
#
# Runs on macOS and Linux — needs only cpio and gzip.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
out=${1:-dist/k0smos-initramfs.gz}
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) goarch=arm64; apkarch=aarch64 ;;
  x86_64 | amd64) goarch=amd64; apkarch=x86_64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT

echo "building k0smos for linux/$goarch"
(cd "$repo" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 \
  go build -ldflags '-s -w -extldflags "-static"' -o "$root/init" ./cmd/k0smos)

mkdir -p "$root"/{proc,sys,dev,run,tmp,etc/k0s,usr/local/bin,var/lib/k0s}
printf 'k0smos\n' > "$root/etc/hostname"
printf '127.0.0.1 localhost\n' > "$root/etc/hosts"
printf 'nameserver 1.1.1.1\n' > "$root/etc/resolv.conf"
printf 'NAME=k0smos\nID=k0smos\n' > "$root/etc/os-release"
# k0s looks up UIDs for its components and warns "open /etc/passwd: no such
# file or directory" (then runs everything as root) without these.
printf 'root:x:0:0:root:/root:/sbin/nologin\nnobody:x:65534:65534:nobody:/:/sbin/nologin\n' > "$root/etc/passwd"
printf 'root:x:0:\nnobody:x:65534:\n' > "$root/etc/group"
cp "$here/k0s.yaml" "$root/etc/k0s/k0s.yaml"

# Kernel modules, so k0smos can load virtio_net/ext4/overlay etc. at boot.
# MODULES_DIR should be a /lib/modules tree matching the kernel being booted;
# fetch-kernel.sh leaves one in dist/kernel/<arch>/lib/modules.
# Keyed off the target arch, not the host's: a cross-arch build was otherwise
# packing the host's modules, which cannot load on the guest.
moddir=${MODULES_DIR:-$repo/dist/kernel/$apkarch/lib/modules}
if [ -d "$moddir" ]; then
  mkdir -p "$root/lib/modules"
  cp -R "$moddir/." "$root/lib/modules/"
  echo "included kernel modules from $moddir"
else
  # Expected with a monolithic kernel, which is the default: fetch-kernel-kata.sh
  # deliberately writes no tree, and k0smos reports "no module tree; assuming a
  # monolithic kernel". Only a concern if the kernel needs modules and has none,
  # in which case it will have no virtio NIC or disk.
  echo "no modules dir at $moddir — assuming a monolithic kernel" >&2
fi

# Optional: bake in a real k0s binary. Without it, boot with
# k0smos.exec=... to supervise something else.
if [ -n "${K0S_BIN:-}" ]; then
  install -m 0755 "$K0S_BIN" "$root/usr/local/bin/k0s"
  echo "included k0s from $K0S_BIN"
fi

mkdir -p "$(dirname "$repo/$out")"
# -H newc is the only format the kernel's initramfs unpacker accepts.
(cd "$root" && find . -print | cpio --quiet -o -H newc) | gzip -9 > "$repo/$out"
echo "wrote $out ($(du -h "$repo/$out" | cut -f1), linux/$goarch)"
