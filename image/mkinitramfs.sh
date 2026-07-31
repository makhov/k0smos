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

# Optional: a root filesystem carried inside the initramfs. k0smos loop-attaches it
# and switch_roots onto it, so the kernel and the whole OS travel as one artifact —
# no separate disk to publish, version or mismatch. Only sensible for a read-only
# image (ROOTFS=erofs in mkrootfs.sh): an ext4 root with room to work in would make
# the initramfs gigabytes and hold it all in RAM.
#
# The path must match embeddedRoot in cmd/k0smos/init_linux.go.
# Defaults to the erofs root if one has been built, since carrying it is the point.
# EMBED_ROOT=none opts out, for a boot that switches onto a disk instead.
embed=${EMBED_ROOT:-$repo/dist/k0smos.erofs}
if [ "$embed" = none ]; then
  embed=""
elif [ ! -f "$embed" ] && [ -z "${EMBED_ROOT:-}" ]; then
  # Not built: fall back quietly rather than failing a plain `make initramfs`.
  embed=""
fi

# Only if this kernel can mount it. Alpine's linux-virt leaves CONFIG_EROFS_FS unset
# entirely — not a module away — while the default (Kata) kernel builds it in and has
# no squashfs at all. Embedding a root the kernel cannot mount produces an initramfs
# that fails at switch_root with nothing pointing at the cause, so the decision is
# made here from the kernel being built for rather than left to the caller.
kconfig=$repo/dist/kernel/$apkarch/config
if [ -n "$embed" ] && [ -f "$kconfig" ] && ! grep -qE '^CONFIG_EROFS_FS=[ym]' "$kconfig"; then
  echo "kernel at $apkarch has no erofs (CONFIG_EROFS_FS unset) — not embedding a root;" >&2
  echo "  boot from a disk instead, with ROOTFS=ext4 and k0smos.root=LABEL=k0smos" >&2
  embed=""
fi
if [ -n "$embed" ]; then
  EMBED_ROOT=$embed
  [ -f "$EMBED_ROOT" ] || { echo "EMBED_ROOT=$EMBED_ROOT not found" >&2; exit 1; }
  cp "$EMBED_ROOT" "$root/k0smos-root.img"
  echo "embedded root filesystem from $EMBED_ROOT ($(du -m "$EMBED_ROOT" | cut -f1)M)"
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
