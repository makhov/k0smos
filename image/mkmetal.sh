#!/usr/bin/env bash
# Assemble a UEFI-bootable GPT disk and its qcow2 representation without loop
# devices or mounts. Partition files are created independently, then copied into
# fixed sector offsets; this works in an unprivileged CI container.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"

raw=${1:-dist/k0smos-metal.raw}
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64)
    goarch=arm64; platform=linux/arm64; grub_target=arm64-efi; efi_name=BOOTAA64.EFI
    ;;
  x86_64 | amd64)
    goarch=amd64; platform=linux/amd64; grub_target=x86_64-efi; efi_name=BOOTX64.EFI
    ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

kernel=${KERNEL:?set KERNEL to the metal kernel}
initramfs=${INITRAMFS:?set INITRAMFS to the matching initramfs}
rootfs=${ROOTFS_IMAGE:?set ROOTFS_IMAGE to the canonical EROFS root}
qcow2=${METAL_QCOW2:-${raw%.raw}.qcow2}

required=(sgdisk mkfs.vfat mkfs.ext4 mmd mcopy grub-mkstandalone qemu-img)
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
    -e K0SMOS_METAL_CONTAINER=1 -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    -v "$repo:/repo" -w /repo "$tag" bash image/mkmetal.sh "$raw"
fi
if [ ${#missing[@]} -gt 0 ]; then
  echo "missing metal image tools inside builder: ${missing[*]}" >&2
  exit 1
fi

for artifact in "$kernel" "$initramfs" "$rootfs"; do
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
data_start=$((root_start + root_sectors))
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

kernel_args=${METAL_KERNEL_ARGS:-console=tty0 console=ttyS0 k0smos.data=LABEL=k0smos-data k0smos.ip=dhcp}
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

rm -f "$raw" "$qcow2"
truncate -s "$((disk_sectors * sector_size))" "$raw"
sgdisk --clear \
  --disk-guid=c0dec0de-0000-4000-8000-000000000010 \
  --new=1:${esp_start}:$((root_start - 1)) --typecode=1:ef00 --change-name=1:K0SMOS-BOOT \
  --partition-guid=1:c0dec0de-0000-4000-8000-000000000011 \
  --new=2:${root_start}:$((data_start - 1)) --typecode=2:8300 --change-name=2:K0SMOS-ROOT \
  --partition-guid=2:c0dec0de-0000-4000-8000-000000000012 \
  --new=3:${data_start}:${data_end} --typecode=3:8300 --change-name=3:K0SMOS-DATA \
  --partition-guid=3:c0dec0de-0000-4000-8000-000000000013 \
  "$raw" >/dev/null

dd if="$esp" of="$raw" bs=$sector_size seek=$esp_start conv=notrunc,sparse status=none
dd if="$rootfs" of="$raw" bs=$sector_size seek=$root_start conv=notrunc,sparse status=none
dd if="$data" of="$raw" bs=$sector_size seek=$data_start conv=notrunc,sparse status=none

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

echo "wrote $qcow2 ($(du -h "$qcow2" | cut -f1), UEFI/GPT, linux/$goarch)"
