#!/usr/bin/env bash
# Build NVIDIA's open GPU kernel modules for the metal kernel into an initramfs
# overlay, dist/nvidia/<version>/<arch>/, a tree laid out like / :
#
#   lib/modules/<release>/extra/nvidia/  the modules
#   lib/modules/<release>/modules.*      the kernel's index, redone to include them
#   lib/firmware/nvidia/<version>/       GSP firmware and ucodes
#   etc/modules-load.d/nvidia.conf       nvidia_uvm, which no device asks for
#
# and next to it, outside the overlay, driver-<arch>.erofs: the user-space
# driver, a root of its own (see below).
#
# build-metal.sh lays it over the initramfs (METAL_OVERLAY). Everything goes
# there, not into the root: the modules belong to one exact kernel like every
# other module, and the canonical root stays the same for every platform.
# k0smos carries the firmware onto the root at switch_root, because the driver
# reads it when it first initialises the GPU, long after.
#
# driver-<arch>.erofs is what the GPU Operator's driver container would otherwise
# provide: libcuda, NVML, nvidia-smi and the glibc they run on, laid out like a
# glibc distro with its own ld.so.cache. mkmetal.sh gives it a partition and
# k0smos mounts it at /run/nvidia/driver, the operator's driverInstallDir.
#
# Everything comes from one NVIDIA package per version, pinned by digest: the
# open module sources, the firmware and the user-space libraries are
# then guaranteed to be the same release. The modules match one kernel build
# exactly, so they are built against Debian's headers for that exact build,
# and this fails when Debian no longer has them -- re-run 'make kernel-metal'
# when Debian's kernel moves on.
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
driver=dist/nvidia/$version/driver-$arch.erofs

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
  -e owner="$(id -u):$(id -g)" \
  debian:trixie-slim bash -euo pipefail -c '
apt-get update -qq >/dev/null
if ! apt-get install -qq -y --no-install-recommends build-essential kmod "linux-headers-$release" >/tmp/apt.log 2>&1; then
  tail -5 /tmp/apt.log >&2
  echo "no Debian headers for the metal kernel $release: it has moved on, run make kernel-metal" >&2
  exit 1
fi
sh "$pkg" -x --target /tmp/pkg >/dev/null
# Debian splits the headers: sources in -common, the build tree per flavour.
common=$(ls -d /usr/src/linux-headers-*-common | head -1)
make -C /tmp/pkg/kernel-open -j"$(nproc)" SYSSRC="$common" SYSOUT="/usr/src/linux-headers-$release" modules >/tmp/build.log 2>&1 || {
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
chown -R "$owner" "$out"
'

# The user-space driver, built on Debian for its glibc. Compute and utility
# only -- what NVIDIA_DRIVER_CAPABILITIES=compute,utility mounts into a pod.
# shortcut: no OpenCL, video (NVENC/NVDEC) or graphics libraries; add their
# names below when a workload needs them.
docker run --rm --platform linux/amd64 -v "$repo:/repo" -w /repo \
  -e version="$version" -e pkg="$pkg" -e driver="$driver" -e owner="$(id -u):$(id -g)" \
  debian:trixie-slim bash -euo pipefail -c '
apt-get update -qq >/dev/null && apt-get install -qq -y --no-install-recommends erofs-utils libssl3t64 >/dev/null
sh "$pkg" -x --target /tmp/pkg >/dev/null
r=/tmp/driver lib=/tmp/driver/usr/lib/x86_64-linux-gnu
mkdir -p "$lib" "$r/usr/bin" "$r/usr/sbin" "$r/usr/lib64" "$r/etc" "$r/proc" "$r/sys" "$r/dev" "$r/tmp" "$r/run"
ln -s usr/lib "$r/lib"; ln -s usr/lib64 "$r/lib64"; ln -s usr/bin "$r/bin"; ln -s usr/sbin "$r/sbin"
for f in /tmp/pkg/lib{cuda,cudadebugger,nvidia-ml,nvidia-cfg,nvidia-ptxjitcompiler,nvidia-nvvm,nvidia-nvvm70,nvidia-gpucomp,nvidia-allocator,nvidia-pkcs11-openssl3,nvidia-tileiras,nvidia-sandboxutils}.so.*; do
  if [ -e "$f" ]; then cp "$f" "$lib/"; fi
done
for b in nvidia-smi nvidia-debugdump nvidia-persistenced nvidia-cuda-mps-control nvidia-cuda-mps-server; do
  cp "/tmp/pkg/$b" "$r/usr/bin/"
done
# libnvidia-pkcs11 (confidential computing) only in its OpenSSL 3 build: 1.1 is
# long gone from Debian. Soname links first, which the libraries link by.
ldconfig -n "$lib"
# glibc and whatever else they link against, from this Debian. The NVIDIA
# libraries resolve among themselves through the root'"'"'s own path.
for f in "$r"/usr/bin/* "$lib"/*; do
  LD_LIBRARY_PATH=$lib ldd "$f" | awk "{for (i = 1; i <= NF; i++) if (\$i ~ /^\//) print \$i}"
done | sort -u | while read -r dep; do
  case "$dep" in "$r"/*) continue ;; esac
  [ -e "$lib/${dep##*/}" ] || cp -L "$dep" "$lib/"
done
if LD_LIBRARY_PATH=$lib ldd "$r"/usr/bin/* "$lib"/* | grep "not found"; then exit 1; fi
ln -s ../lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 "$r/usr/lib64/ld-linux-x86-64.so.2"
# The toolkit runs this ldconfig (Debian'"'"'s is static) to update containers'"'"' caches.
cp /sbin/ldconfig "$r/usr/sbin/ldconfig"
echo /usr/lib/x86_64-linux-gnu > "$r/etc/ld.so.conf"
ldconfig -r "$r"
for f in "$lib"/libcuda.so.1 "$lib"/libnvidia-ml.so.1; do ln -s "${f##*/}" "${f%.1}"; done
printf "NAME=k0smos-nvidia\nID=k0smos-nvidia\nVERSION_ID=%s\n" "$version" > "$r/etc/os-release"
# Proves the root is complete: the binary starts on its own glibc, no GPU needed.
chroot "$r" /usr/bin/nvidia-smi --help >/dev/null
mkfs.erofs -zlz4hc -T0 --all-root -L k0smos-nvidia "$driver" "$r" >/dev/null
chown "$owner" "$driver"
'

echo "built NVIDIA $version for $release: $(du -sh "$out/lib/modules/$release/extra/nvidia" | cut -f1) of modules, $(du -sh "$out/lib/firmware" | cut -f1) of firmware in $out, $(du -h "$driver" | cut -f1) driver root in $driver"
