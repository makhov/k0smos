#!/usr/bin/env bash
# Assemble a GPT disk, bootable by UEFI and on x86 by legacy BIOS too, and its
# qcow2 representation without loop devices or mounts. Partition files are created independently, then copied into
# fixed sector offsets; this works in an unprivileged CI container.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"

raw=${1:-dist/k0smos-metal.raw}
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64)
    goarch=arm64; platform=linux/arm64; grub_target=arm64-efi; efi_name=BOOTAA64.EFI; serial=ttyAMA0
    bios=false
    ;;
  x86_64 | amd64)
    goarch=amd64; platform=linux/amd64; grub_target=x86_64-efi; efi_name=BOOTX64.EFI; serial=ttyS0
    # Most libvirt and bare-metal x86 machines still boot SeaBIOS or a BIOS
    # CSM unless told otherwise; an image that cannot is not a drop-in.
    bios=true
    ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

kernel=${KERNEL:?set KERNEL to the metal kernel}
initramfs=${INITRAMFS:?set INITRAMFS to the matching initramfs}
rootfs=${ROOTFS_IMAGE:?set ROOTFS_IMAGE to the canonical EROFS root}
qcow2=${METAL_QCOW2:-${raw%.raw}.qcow2}

required=(sgdisk mkfs.vfat mkfs.ext4 mmd mcopy grub-mkstandalone qemu-img)
$bios && required+=(grub-mkimage)
missing=()
for tool in "${required[@]}"; do
  command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done

if [ ${#missing[@]} -gt 0 ] && [ "${K0SMOS_METAL_CONTAINER:-0}" != 1 ]; then
  command -v docker >/dev/null || {
    echo "missing metal image tools: ${missing[*]}; docker is also unavailable" >&2
    exit 1
  }
  tag=k0smos-metal-builder:$goarch
  docker build --platform "$platform" --build-arg TARGETARCH="$goarch" \
    -t "$tag" -f image/metal-builder.Dockerfile image
  exec docker run --rm --platform "$platform" \
    -e ARCH="$arch" -e KERNEL="$kernel" -e INITRAMFS="$initramfs" \
    -e ROOTFS_IMAGE="$rootfs" -e METAL_QCOW2="$qcow2" \
    -e METAL_DATA_MB="${METAL_DATA_MB:-}" -e METAL_KERNEL_ARGS="${METAL_KERNEL_ARGS:-}" \
    -e DRIVER_IMAGE="${DRIVER_IMAGE:-}" \
    -e K0SMOS_METAL_CONTAINER=1 -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    -v "$repo:/repo" -w /repo "$tag" bash image/mkmetal.sh "$raw"
fi
if [ ${#missing[@]} -gt 0 ]; then
  echo "missing metal image tools inside builder: ${missing[*]}" >&2
  exit 1
fi

# Optional: a read-only image for a partition of its own, such as the NVIDIA
# driver root (fetch-nvidia.sh), which k0smos finds by its filesystem label.
driver=${DRIVER_IMAGE:-}
for artifact in "$kernel" "$initramfs" "$rootfs" ${driver:+"$driver"}; do
  [ -s "$artifact" ] || { echo "missing or empty artifact: $artifact" >&2; exit 1; }
done

mkdir -p "$(dirname "$raw")" "$(dirname "$qcow2")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

esp_mb=256
data_mb=${METAL_DATA_MB:-8192}
sector_size=512
alignment=2048
esp_start=$alignment
esp_sectors=$((esp_mb * 1024 * 1024 / sector_size))
root_bytes=$(wc -c < "$rootfs" | tr -d ' ')
root_sectors=$(((root_bytes + sector_size - 1) / sector_size))
root_sectors=$((((root_sectors + alignment - 1) / alignment) * alignment))
root_start=$((esp_start + esp_sectors))
driver_start=$((root_start + root_sectors))
driver_sectors=0
if [ -n "$driver" ]; then
  driver_sectors=$((($(wc -c < "$driver" | tr -d ' ') + sector_size - 1) / sector_size))
  driver_sectors=$((((driver_sectors + alignment - 1) / alignment) * alignment))
fi
data_start=$((driver_start + driver_sectors))
data_sectors=$((data_mb * 1024 * 1024 / sector_size))
data_end=$((data_start + data_sectors - 1))
# Room for the backup GPT plus an alignment-sized tail.
disk_sectors=$((data_end + alignment + 1))

esp=$tmp/esp.fat
data=$tmp/data.ext4
cfg=$tmp/grub.cfg
efi=$tmp/$efi_name

truncate -s "${esp_mb}M" "$esp"
mkfs.vfat -F 32 -n K0SMOSBOOT -i C0DE0001 "$esp" >/dev/null

# The serial console is per-arch: aarch64 virt has no ttyS0, so a baked-in
# console=ttyS0 left tty0 as the preferred console -- and qemu -display none
# throws tty0 away. Every line k0smos and k0s wrote went nowhere, which is why
# a guest stuck on DNS looked like a guest stuck in init. Serial comes last
# because the last console= wins /dev/console.
kernel_args=${METAL_KERNEL_ARGS:-console=tty0 console=$serial k0smos.data=LABEL=k0smos-data k0smos.ip=dhcp}
cat > "$cfg" <<EOF
search --no-floppy --label K0SMOSBOOT --set=root
linux /boot/vmlinuz $kernel_args
initrd /boot/initramfs.gz
boot
EOF
grub-mkstandalone -O "$grub_target" -o "$efi" \
  --modules="part_gpt fat ext2 normal linux search search_label" \
  "boot/grub/grub.cfg=$cfg"

mmd -i "$esp" ::/EFI ::/EFI/BOOT ::/boot
mcopy -i "$esp" "$efi" ::/EFI/BOOT/$efi_name
mcopy -i "$esp" "$kernel" ::/boot/vmlinuz
mcopy -i "$esp" "$initramfs" ::/boot/initramfs.gz

truncate -s "${data_mb}M" "$data"
mkfs.ext4 -q -F -L k0smos-data \
  -U c0dec0de-0000-4000-8000-000000000002 "$data"

# GRUB's BIOS core lives in its own small partition, in the gap before the
# ESP that alignment leaves anyway. Numbered 4 so ESP, root and data keep their
# numbers, and placed first so data stays physically last -- which is what lets
# /var grow into a bigger disk.
bios_start=34
bios_end=$((esp_start - 1))
driver_part=()
if [ -n "$driver" ]; then
  driver_part=(--new=5:${driver_start}:$((data_start - 1)) --typecode=5:8300 --change-name=5:K0SMOS-DRIVER
    --partition-guid=5:c0dec0de-0000-4000-8000-000000000015)
fi
bios_part=()
if $bios; then
  bios_part=(--set-alignment=1 --new=4:${bios_start}:${bios_end} --typecode=4:ef02
    --change-name=4:K0SMOS-BIOS --partition-guid=4:c0dec0de-0000-4000-8000-000000000014)
fi

rm -f "$raw" "$qcow2"
truncate -s "$((disk_sectors * sector_size))" "$raw"
sgdisk --clear \
  --disk-guid=c0dec0de-0000-4000-8000-000000000010 \
  --new=1:${esp_start}:$((root_start - 1)) --typecode=1:ef00 --change-name=1:K0SMOS-BOOT \
  --partition-guid=1:c0dec0de-0000-4000-8000-000000000011 \
  --new=2:${root_start}:$((driver_start - 1)) --typecode=2:8300 --change-name=2:K0SMOS-ROOT \
  --partition-guid=2:c0dec0de-0000-4000-8000-000000000012 \
  --new=3:${data_start}:${data_end} --typecode=3:8300 --change-name=3:K0SMOS-DATA \
  --partition-guid=3:c0dec0de-0000-4000-8000-000000000013 \
  "${bios_part[@]}" "${driver_part[@]}" \
  "$raw" >/dev/null

dd if="$esp" of="$raw" bs=$sector_size seek=$esp_start conv=notrunc,sparse status=none
dd if="$rootfs" of="$raw" bs=$sector_size seek=$root_start conv=notrunc,sparse status=none
dd if="$data" of="$raw" bs=$sector_size seek=$data_start conv=notrunc,sparse status=none
if [ -n "$driver" ]; then
  dd if="$driver" of="$raw" bs=$sector_size seek=$driver_start conv=notrunc,sparse status=none
fi

# GRUB for BIOS, installed by hand rather than by grub-bios-setup, which probes
# the build host's own mounts and fails in a container. The steps are the ones
# it performs, at offsets fixed by GRUB's i386-pc boot.h:
#   core.img into the BIOS boot partition, its first sector (diskboot) told
#     where the rest of it is;
#   boot.img's code -- never the partition table after it -- into the MBR,
#     told where core starts, to use the drive the BIOS booted from, and with
#     the floppy probe that misreads hard disks on some BIOSes jumped over.
# The config is the EFI loader's own grub.cfg, so the two paths cannot drift.
if $bios; then
  le() { local n=$1 out=""; for ((i = 0; i < $2; i++)); do out+=$(printf '\\x%02x' $(((n >> (8 * i)) & 255))); done; printf '%s' "$out"; }
  put() { printf "$2" | dd of="$1" bs=1 seek="$3" conv=notrunc status=none; }
  core=$tmp/core.img
  boot=$tmp/boot.img
  grub-mkimage -O i386-pc -o "$core" -c "$cfg" -p /boot/grub \
    biosdisk part_gpt fat ext2 search search_label linux boot
  core_sectors=$((($(wc -c < "$core") + sector_size - 1) / sector_size))
  [ "$core_sectors" -le $((bios_end - bios_start + 1)) ] || {
    echo "GRUB core ($core_sectors sectors) does not fit the BIOS boot partition" >&2
    exit 1
  }
  put "$core" "$(le $((bios_start + 1)) 8)" 500 # blocklist: first sector
  put "$core" "$(le $((core_sectors - 1)) 2)" 508 # blocklist: length
  put "$core" "$(le $((0x820)) 2)" 510 # blocklist: load segment
  dd if="$core" of="$raw" bs=$sector_size seek=$bios_start conv=notrunc status=none
  cp /usr/lib/grub/i386-pc/boot.img "$boot"
  put "$boot" "$(le $bios_start 8)" $((0x5c)) # GRUB_BOOT_MACHINE_KERNEL_SECTOR
  put "$boot" '\xff' $((0x64)) # GRUB_BOOT_MACHINE_BOOT_DRIVE: as the BIOS says
  put "$boot" '\x90\x90' $((0x66)) # GRUB_BOOT_MACHINE_DRIVE_CHECK: nop
  dd if="$boot" of="$raw" bs=1 count=440 conv=notrunc status=none
fi

sgdisk --verify "$raw"
# The platform wrapper must carry the canonical root byte-for-byte. Checking the
# exact partition offset makes that identity a build invariant, not a convention.
cmp -s -i "$((root_start * sector_size)):0" -n "$root_bytes" "$raw" "$rootfs" || {
  echo "ROOT partition does not match canonical root $rootfs" >&2
  exit 1
}
qemu-img convert -f raw -O qcow2 -c "$raw" "$qcow2"
qemu-img check -f qcow2 "$qcow2"

if [ "$(id -u)" = 0 ] && [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
  chown "$HOST_UID:$HOST_GID" "$raw" "$qcow2"
fi

firmware=UEFI
$bios && firmware=UEFI+BIOS
echo "wrote $qcow2 ($(du -h "$qcow2" | cut -f1), $firmware/GPT, linux/$goarch)"
