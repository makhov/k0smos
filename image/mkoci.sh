#!/usr/bin/env bash
# Package the boot artifacts as OCI images for KubeVirt.
#
# KubeVirt needs two, because it cannot direct-kernel-boot from the same place
# it gets the root disk:
#
#   <repo>/k0smos-boot:<tag>   kernel + initramfs, referenced by
#                              spec.domain.firmware.kernelBoot.container
#                              (KernelBootContainer{Image,KernelPath,InitrdPath})
#   <repo>/k0smos-disk:<tag>   the ext4 root at /disk/k0smos.img, which is the
#                              containerDisk convention
#
# Both are FROM scratch: they carry data, not a runtime. Set PUSH=1 to push.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"

registry=${REGISTRY:-ghcr.io/amakhov}
tag=${TAG:-dev}
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) platform=linux/arm64; apkarch=aarch64 ;;
  x86_64 | amd64) platform=linux/amd64; apkarch=x86_64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

kernel=${KERNEL:-dist/kernel/$apkarch/vmlinuz}
initramfs=${INITRAMFS:-dist/k0smos-initramfs.gz}
img=${IMG:-dist/k0smos.img}
for f in "$kernel" "$initramfs" "$img"; do
  [ -f "$f" ] || { echo "missing $f — run 'make kernel initramfs disk'" >&2; exit 1; }
done

ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

# --- boot image: kernel + initramfs ---
mkdir -p "$ctx/boot/boot"
cp "$kernel" "$ctx/boot/boot/vmlinuz"
cp "$initramfs" "$ctx/boot/boot/initramfs.gz"
cat > "$ctx/boot/Dockerfile" <<'EOF'
FROM scratch
# Paths here are what the VM spec must reference as kernelPath/initrdPath.
ADD boot/vmlinuz /boot/vmlinuz
ADD boot/initramfs.gz /boot/initramfs.gz
EOF

# --- disk image: the ext4 root ---
mkdir -p "$ctx/disk/disk"
cp "$img" "$ctx/disk/disk/k0smos.img"
cat > "$ctx/disk/Dockerfile" <<'EOF'
FROM scratch
# /disk is where KubeVirt looks for a containerDisk's image.
ADD disk/k0smos.img /disk/k0smos.img
EOF

build() {
  local dir=$1 name=$2
  local ref="$registry/$name:$tag"
  echo "building $ref ($platform)"
  local args=(build --platform "$platform" -t "$ref" "$ctx/$dir")
  if [ "${PUSH:-0}" = "1" ]; then
    # buildx is required to push a cross-platform image.
    docker buildx build --platform "$platform" -t "$ref" --push "$ctx/$dir"
  else
    docker "${args[@]}"
  fi
  echo "  $ref"
}

build boot k0smos-boot
build disk k0smos-disk

cat <<EOF

Artifacts ready. Reference them from a KubeVirt VM as:

  firmware:
    kernelBoot:
      container:
        image: $registry/k0smos-boot:$tag
        kernelPath: /boot/vmlinuz
        initrdPath: /boot/initramfs.gz
      # kernelArgs go here; see image/kubevirt-vm.yaml
  volumes:
    - name: root
      containerDisk:
        image: $registry/k0smos-disk:$tag

See image/kubevirt-vm.yaml for a complete example.
EOF
