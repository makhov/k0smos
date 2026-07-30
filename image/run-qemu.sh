#!/usr/bin/env bash
# Boot k0smos under QEMU with direct kernel boot.
#
# Two modes:
#   default             — boot the initramfs and stay there (init smoke test).
#   IMG=dist/k0smos.img — boot the initramfs, then switch_root onto this ext4
#       disk. Required for kubelet, which cannot run on a ramfs root.
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

# Static networking, not ip=dhcp: the kernel's IP autoconfiguration runs before
# /init and so cannot see the virtio NIC, whose driver k0smos loads as a module.
# The address and gateway are QEMU user-mode networking's fixed values (guest
# .15, gw .2), so they are correct for every run of this script.
#
# DNS deliberately does NOT use slirp's built-in resolver at 10.0.2.3: on a
# macOS host it accepts queries and never answers them (verified -- every
# lookup ended in "read udp 10.0.2.15->10.0.2.3:53: i/o timeout" and no image
# could be pulled). A public resolver is NATed out normally and works.
net_args=${NET_ARGS:-k0smos.ip=10.0.2.15/24 k0smos.gw=10.0.2.2 k0smos.dns=1.1.1.1}
append="console=$console panic=10 $net_args"

# Boot is always via the initramfs (k0smos is /init there): a stock kernel has
# virtio_blk and ext4 as modules, so it cannot mount a disk root by itself.
[ -f "$initramfs" ] || { echo "initramfs $initramfs not found — run 'make initramfs'" >&2; exit 1; }
boot=(-initrd "$initramfs")

# With a disk attached, k0smos loads the storage modules and switch_roots onto
# it. Without one it stays on the initramfs — fine for an init smoke test, but
# kubelet cannot run on a ramfs root.
if [ -n "$img" ]; then
  [ -f "$img" ] || { echo "disk $img not found — run 'make disk'" >&2; exit 1; }
  boot+=(-drive file="$img",if=virtio,format=raw)
  append="$append k0smos.root=/dev/vda k0smos.rootfstype=ext4"
fi
[ -n "${EXEC:-}" ] && append="$append k0smos.exec=$EXEC"

# Control port for clean shutdown. This guest has no usable power button --
# direct kernel boot means no UEFI hence no ACPI, and this kernel builds no
# gpio-keys driver -- so without this the only way to stop it is to kill QEMU,
# which corrupts the ext4 root. Write "poweroff" or "reboot" to the socket:
#   printf 'poweroff\n' | nc -U dist/control.sock
control=${CONTROL:-dist/control.sock}
control_args=()
if [ -n "$control" ]; then
  mkdir -p "$(dirname "$control")"
  rm -f "$control"
  control_args=(
    -chardev "socket,path=$control,server=on,wait=off,id=k0smosctl"
    -device virtio-serial-pci
    -device "virtserialport,chardev=k0smosctl,name=k0smos.control"
  )
fi

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
  "${control_args[@]}" \
  -netdev user,id=n0,hostfwd=tcp::6443-:6443 -device virtio-net-pci,netdev=n0 \
  "${display[@]}"
