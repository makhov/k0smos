#!/usr/bin/env bash
# Build NVIDIA's open GPU kernel modules for the metal kernel into an initramfs
# overlay, dist/nvidia/<version>/<arch>/, a tree laid out like / :
#
#   lib/modules/<release>/extra/nvidia/  the modules
#   lib/modules/<release>/modules.*      the kernel's index, redone to include them
#   lib/firmware/nvidia/<version>/       GSP firmware and ucodes
#   etc/modules-load.d/nvidia.conf       nvidia_uvm, which no device asks for
#
# build-metal.sh lays it over the initramfs (METAL_OVERLAY). Everything goes
# there, not into the root: the modules belong to one exact kernel like every
# other module, and the canonical root stays the same for every platform.
# k0smos carries the firmware onto the root at switch_root, because the driver
# reads it when it first initialises the GPU, long after.
#
# Everything comes from one NVIDIA package per version, pinned by digest: the
# open module sources, the firmware and, later, the user-space libraries are
# then guaranteed to be the same release. The modules match one kernel build
# exactly, so this refuses a kernel tree that is not the one Alpine's headers
# describe -- re-run 'make kernel-metal' when Alpine's linux-lts moves on.
#
# x86_64 only, for data center GPUs: H100, H200 and B200 are what this targets.
# NVSwitch systems run their fabric manager on the host, not in guests.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"

version=${NVIDIA_VERSION:?set NVIDIA_VERSION, e.g. 620.70.06 or 580.196.07}
arch=x86_64
kernel_dir=${KERNEL_DIR:-dist/kernel-metal/$arch}
out=dist/nvidia/$version/$arch

# NVIDIA's data center packages: the R580 and R620 long-term support branches.
case "$version" in
  580.196.07) sha256=0aab0518db53cf73c412dc7f32bbc850ca95e839d8923d0de4bff42c1c9169ce ;;
  620.70.06) sha256=29118e3abd08c07559bcca3d9c76238d039490af0362a2e5bcfcbe7204afd4b1 ;;
  *) echo "no pinned digest for NVIDIA $version; add one here" >&2; exit 1 ;;
esac

[ -d "$kernel_dir/lib/modules" ] || { echo "no module tree in $kernel_dir — run 'make kernel-metal'" >&2; exit 1; }
release=$(ls "$kernel_dir/lib/modules")
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }

pkg=dist/nvidia/NVIDIA-Linux-$arch-$version.run
if [ ! -s "$pkg" ]; then
  mkdir -p "$(dirname "$pkg")"
  curl -fL --retry 3 -o "$pkg.tmp" "https://us.download.nvidia.com/tesla/$version/NVIDIA-Linux-$arch-$version.run"
  mv "$pkg.tmp" "$pkg"
fi
if command -v sha256sum >/dev/null; then got=$(sha256sum "$pkg" | cut -d' ' -f1); else got=$(shasum -a 256 "$pkg" | cut -d' ' -f1); fi
[ "$got" = "$sha256" ] || { echo "$pkg: sha256 $got, want $sha256" >&2; exit 1; }

rm -rf "$out"
mkdir -p "$out"

docker run --rm --platform linux/amd64 -v "$repo:/repo" -w /repo \
  -e version="$version" -e release="$release" -e pkg="$pkg" -e out="$out" -e kernel_dir="$kernel_dir" \
  alpine:3.23 sh -euc '
apk add -q --no-cache build-base linux-lts-dev perl kmod binutils >/dev/null
headers=$(ls /usr/src | sed -n "s/^linux-headers-//p")
if [ "$headers" != "$release" ]; then
  echo "Alpine linux-lts-dev is $headers but the metal kernel is $release: run make kernel-metal" >&2
  exit 1
fi
sh "$pkg" -x --target /tmp/pkg >/dev/null
make -C /tmp/pkg/kernel-open -j"$(nproc)" SYSSRC="/usr/src/linux-headers-$release" modules >/tmp/build.log 2>&1 || {
  tail -30 /tmp/build.log >&2
  exit 1
}
# depmod needs the whole tree to index the new modules against; only what
# changed goes into the overlay.
mkdir -p /tmp/tree/lib
cp -R "$kernel_dir/lib/modules" /tmp/tree/lib/
mods=/tmp/tree/lib/modules/$release/extra/nvidia
mkdir -p "$mods"
# Compute only: nvidia and nvidia_uvm, plus nvidia_peermem for GPUDirect RDMA.
# No modeset or drm -- nothing here drives a display.
for m in nvidia nvidia-uvm nvidia-peermem; do
  strip --strip-debug -o "$mods/$m.ko" "/tmp/pkg/kernel-open/$m.ko"
done
depmod -b /tmp/tree "$release"
mkdir -p "$out/lib/modules/$release" "$out/lib/firmware/nvidia/$version" "$out/etc/modules-load.d"
cp -R /tmp/tree/lib/modules/$release/extra /tmp/tree/lib/modules/$release/modules.* "$out/lib/modules/$release/"
cp /tmp/pkg/firmware/*.bin "$out/lib/firmware/nvidia/$version/"
printf "# CUDA needs it, and no device asks for it: nvidia autoloads by PCI id.\nnvidia_uvm\n" >"$out/etc/modules-load.d/nvidia.conf"
'

echo "built NVIDIA $version for $release: $(du -sh "$out/lib/modules/$release/extra/nvidia" | cut -f1) of modules, $(du -sh "$out/lib/firmware" | cut -f1) of firmware in $out"
