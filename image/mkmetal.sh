#!/usr/bin/env bash
# Put cmd/mkimage somewhere its tools are installed, then run it: natively when
# the build host has them, otherwise in the builder container. The assembly
# itself -- geometry, partition table, GRUB configuration, dm-verity and the
# invariants over the finished disk -- is Go, in cmd/mkimage.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"

raw=${1:-dist/k0smos-metal.raw}
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) goarch=arm64; platform=linux/arm64; bios=false ;;
  x86_64 | amd64) goarch=amd64; platform=linux/amd64; bios=true ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

args=(-arch "$arch" -out "$raw"
  -kernel "${KERNEL:?set KERNEL to the metal kernel}"
  -initramfs "${INITRAMFS:?set INITRAMFS to the matching initramfs}"
  -rootfs "${ROOTFS_IMAGE:?set ROOTFS_IMAGE to the canonical EROFS root}")
[ -n "${METAL_QCOW2:-}" ] && args+=(-qcow2 "$METAL_QCOW2")
[ -n "${METAL_DATA_MB:-}" ] && args+=(-data-mb "$METAL_DATA_MB")
[ -n "${METAL_KERNEL_ARGS:-}" ] && args+=(-kernel-args "$METAL_KERNEL_ARGS")
# Optional: a read-only image for a partition of its own, such as the NVIDIA
# driver root (fetch-nvidia.sh), which k0smos finds by its filesystem label.
[ -n "${DRIVER_IMAGE:-}" ] && args+=(-driver "$DRIVER_IMAGE")

required=(mkfs.vfat mkfs.ext4 mmd mcopy grub-mkstandalone qemu-img veritysetup)
$bios && required+=(grub-mkimage)
missing=()
for tool in "${required[@]}"; do
  command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done

mkdir -p dist
if [ ${#missing[@]} -eq 0 ]; then
  go build -o dist/mkimage ./cmd/mkimage
  exec ./dist/mkimage "${args[@]}"
fi

command -v docker >/dev/null || {
  echo "missing metal image tools: ${missing[*]}; docker is also unavailable" >&2
  exit 1
}
# The builder has the tools but no Go, so cross-compile here and run that binary
# in there. It writes as root, hence -chown.
GOOS=linux GOARCH=$goarch CGO_ENABLED=0 go build -o "dist/mkimage-linux-$goarch" ./cmd/mkimage
tag=k0smos-metal-builder:$goarch
# --load because docker build may be routed to buildx: its container driver
# leaves the result in the build cache, and the run below then cannot find
# the image. Harmless on the classic driver, which loads anyway.
docker build --load --platform "$platform" --build-arg TARGETARCH="$goarch" \
  -t "$tag" -f image/metal-builder.Dockerfile image
exec docker run --rm --platform "$platform" -v "$repo:/repo" -w /repo "$tag" \
  "./dist/mkimage-linux-$goarch" "${args[@]}" -chown "$(id -u):$(id -g)"
