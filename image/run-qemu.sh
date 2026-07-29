#!/usr/bin/env bash
# Boot k0smos under QEMU with direct kernel boot.
#
# Two modes:
#   INITRAMFS=dist/k0smos-initramfs.gz  — rootfs in RAM, works on a stock
#       distro kernel (virtio_blk/ext4 as modules is fine). Default.
#   IMG=dist/k0smos.img                 — persistent ext4 root on /dev/vda,
#       requires a kernel with virtio_blk + ext4 built in.
#
# Env:
#   KERNEL   kernel image (default dist/kernel/<apkarch>/vmlinuz)
#   ARCH     guest arch (default host arch)
#   SERIAL   "stdio" (default, interactive) or a file path (headless)
#   EXEC     comma-separated k0smos.exec override, e.g. /bin/true
#   MEM/CPUS guest sizing
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"

arch=${ARCH:-$(uname -m)}
serial=${SERIAL:-stdio}
mem=${MEM:-4096}
cpus=${CPUS:-2}
initramfs=${INITRAMFS:-dist/k0smos-initramfs.gz}
img=${IMG:-}

case "$arch" in
  arm64 | aarch64)
    qemu=qemu-system-aarch64
    machine=(-M virt)
    console=ttyAMA0
    apkarch=aarch64
    # hvf is native virtualization on Apple Silicon; only valid same-arch.
    if [ "$(uname -m)" = "arm64" ] && [ "$(uname -s)" = "Darwin" ]; then
      accel=(-accel hvf -cpu host)
    elif [ -w /dev/kvm ]; then
      accel=(-accel kvm -cpu host)
    else
      accel=(-accel tcg)
    fi
    ;;
  x86_64 | amd64)
    qemu=qemu-system-x86_64
    machine=(-M q35)
    console=ttyS0
    apkarch=x86_64
    if [ -w /dev/kvm ]; then
      accel=(-accel kvm -cpu host)
    else
      # x86 guest on a non-x86 host is full emulation — expect it to be slow.
      accel=(-accel tcg)
    fi
    ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

kernel=${KERNEL:-dist/kernel/$apkarch/vmlinuz}
[ -f "$kernel" ] || { echo "kernel $kernel not found — run 'make kernel'" >&2; exit 1; }
command -v "$qemu" >/dev/null || { echo "$qemu not installed" >&2; exit 1; }

append="console=$console panic=10"
boot=()
if [ -n "$img" ]; then
  [ -f "$img" ] || { echo "image $img not found — run 'make image'" >&2; exit 1; }
  append="root=/dev/vda rw init=/sbin/k0smos $append"
  boot=(-drive file="$img",if=virtio,format=raw)
else
  [ -f "$initramfs" ] || { echo "initramfs $initramfs not found — run 'make initramfs'" >&2; exit 1; }
  # k0smos is /init in the archive, which the kernel runs by default.
  boot=(-initrd "$initramfs")
fi
[ -n "${EXEC:-}" ] && append="$append k0smos.exec=$EXEC"

display=(-nographic -serial mon:stdio)
if [ "$serial" != "stdio" ]; then
  mkdir -p "$(dirname "$serial")"
  display=(-display none -serial "file:$serial")
fi

set -x
exec "$qemu" \
  "${machine[@]}" "${accel[@]}" \
  -m "$mem" -smp "$cpus" \
  -kernel "$kernel" -append "$append" \
  "${boot[@]}" \
  -netdev user,id=n0,hostfwd=tcp::6443-:6443 -device virtio-net-pci,netdev=n0 \
  "${display[@]}"
