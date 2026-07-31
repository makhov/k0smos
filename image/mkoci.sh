#!/usr/bin/env bash
# Package a k0smos node as a single OCI image for KubeVirt.
#
#   <repo>/k0smos:<tag>   /boot/vmlinuz         kernelBoot.container.kernelPath
#                         /boot/initramfs.gz    kernelBoot.container.initrdPath
#
# Two files, and they are the whole node: the initramfs carries the read-only erofs
# root, so there is no containerDisk. A VM needs only the kernelBoot container plus
# somewhere writable for /var — an emptyDisk or a PVC.
#
# It was two images once, one of them a 3.3GB-apparent containerDisk, and the kernel
# and root could be paired at mismatched versions — the skew k0smos reports at boot
# as "kernel and modules are out of step". Neither is expressible now.
#
# IMG= still adds a root disk at /disk/k0smos.img, for booting from a disk rather
# than carrying the root along.
#
# FROM scratch: it carries data, not a runtime. Set PUSH=1 to push.
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
# Optional: only for an image that boots from a disk.
img=${IMG:-}
for f in "$kernel" "$initramfs"; do
  [ -f "$f" ] || { echo "missing $f — run 'make artifacts'" >&2; exit 1; }
done
if [ -n "$img" ] && [ ! -f "$img" ]; then
  echo "missing $img — run 'make disk'" >&2
  exit 1
fi

ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

mkdir -p "$ctx/node/boot"
cp "$kernel" "$ctx/node/boot/vmlinuz"
cp "$initramfs" "$ctx/node/boot/initramfs.gz"
cat > "$ctx/node/Dockerfile" <<'EOF'
FROM scratch
# /boot paths are what the VM spec references as kernelPath and initrdPath.
ADD boot/vmlinuz /boot/vmlinuz
ADD boot/initramfs.gz /boot/initramfs.gz
EOF
if [ -n "$img" ]; then
  mkdir -p "$ctx/node/disk"
  cp "$img" "$ctx/node/disk/k0smos.img"
  # /disk is where KubeVirt looks for a containerDisk's image.
  printf 'ADD disk/k0smos.img /disk/k0smos.img\n' >> "$ctx/node/Dockerfile"
fi

ref="$registry/k0smos:$tag"
echo "building $ref ($platform)"
if [ "${PUSH:-0}" = "1" ]; then
  # buildx is required to push a cross-platform image.
  docker buildx build --platform "$platform" -t "$ref" --push "$ctx/node"
else
  docker build --platform "$platform" -t "$ref" "$ctx/node"
fi

cat <<EOF

Built $ref. It is the whole node — the initramfs carries the root:

  firmware:
    kernelBoot:
      container:
        image: $ref
        kernelPath: /boot/vmlinuz
        initrdPath: /boot/initramfs.gz
      kernelArgs: "console=ttyS0 k0smos.ip=dhcp k0smos.data=auto"
  volumes:
    # Writable /var. Required: the root is read-only.
    - name: data
      emptyDisk:
        capacity: 20Gi

See image/kubevirt-vm.yaml for a complete example.
EOF
