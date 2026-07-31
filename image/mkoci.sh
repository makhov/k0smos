#!/usr/bin/env bash
# Package a k0smos node as a single OCI image for KubeVirt.
#
#   <repo>/k0smos:<tag>   /boot/vmlinuz         kernelBoot.container.kernelPath
#                         /boot/initramfs.gz    kernelBoot.container.initrdPath
#                         /disk/k0smos.img      the containerDisk convention
#
# One image, referenced twice in a VM spec — once as the kernelBoot container and
# once as the containerDisk volume. KubeVirt supports exactly this and documents it.
#
# It was two images, and that was a mistake worth not repeating: the kernel and the
# root are not independently versionable. The root carries the module tree, so
# pairing k0smos:v1's kernel with a v2 root gives the version skew k0smos warns
# about at boot ("kernel and modules are out of step, so NO modules were loaded").
# One image makes that pairing unrepresentable, and the node is pulled once instead
# of twice.
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
img=${IMG:-dist/k0smos.img}
for f in "$kernel" "$initramfs" "$img"; do
  [ -f "$f" ] || { echo "missing $f — run 'make kernel initramfs disk'" >&2; exit 1; }
done

ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

mkdir -p "$ctx/node/boot"
cp "$kernel" "$ctx/node/boot/vmlinuz"
cp "$initramfs" "$ctx/node/boot/initramfs.gz"
mkdir -p "$ctx/node/disk"
cp "$img" "$ctx/node/disk/k0smos.img"
cat > "$ctx/node/Dockerfile" <<'EOF'
FROM scratch
# /boot paths are what the VM spec references as kernelPath and initrdPath.
ADD boot/vmlinuz /boot/vmlinuz
ADD boot/initramfs.gz /boot/initramfs.gz
# /disk is where KubeVirt looks for a containerDisk's image.
ADD disk/k0smos.img /disk/k0smos.img
EOF

ref="$registry/k0smos:$tag"
echo "building $ref ($platform)"
if [ "${PUSH:-0}" = "1" ]; then
  # buildx is required to push a cross-platform image.
  docker buildx build --platform "$platform" -t "$ref" --push "$ctx/node"
else
  docker build --platform "$platform" -t "$ref" "$ctx/node"
fi

cat <<EOF

Built $ref. Reference it twice in a KubeVirt VM — once to boot, once as the disk:

  firmware:
    kernelBoot:
      container:
        image: $ref
        kernelPath: /boot/vmlinuz
        initrdPath: /boot/initramfs.gz
      # kernelArgs go here; see image/kubevirt-vm.yaml
  volumes:
    - name: root
      containerDisk:
        image: $ref

See image/kubevirt-vm.yaml for a complete example.
EOF
