#!/usr/bin/env bash
# Boot the k0smos image under QEMU with direct kernel boot.
# Requires: KERNEL=/path/to/vmlinuz (with virtio-blk/virtio-net built in).
#
# SERIAL controls where the guest console goes:
#   SERIAL=stdio (default) — interactive, console on this terminal
#   SERIAL=/path/to/log    — headless, console written to that file
set -euo pipefail
img=${1:-dist/k0smos.img}
kernel=${KERNEL:?set KERNEL to a bzImage/vmlinuz with virtio built in}
serial=${SERIAL:-stdio}
mem=${MEM:-4096}
cpus=${CPUS:-2}

[ -f "$img" ] || { echo "image $img not found — run 'make image'" >&2; exit 1; }

# Hardware acceleration only when the host actually offers it; otherwise TCG
# (correct but slow — a k0s boot under TCG can take many minutes).
accel=()
if [ -w /dev/kvm ]; then
  accel=(-enable-kvm -cpu host)
fi

display=(-nographic -serial mon:stdio)
if [ "$serial" != "stdio" ]; then
  display=(-display none -serial "file:$serial")
fi

exec qemu-system-x86_64 \
  -m "$mem" -smp "$cpus" \
  "${accel[@]}" \
  -kernel "$kernel" \
  -append "root=/dev/vda rw init=/sbin/k0smos ip=dhcp console=ttyS0" \
  -drive file="$img",if=virtio,format=raw \
  -netdev user,id=n0,hostfwd=tcp::6443-:6443 -device virtio-net,netdev=n0 \
  "${display[@]}"
