#!/usr/bin/env bash
# Fetch Ubuntu's generic kernel for the metal image into dist/kernel-metal/<arch>/:
# vmlinuz, config and lib/modules/<release> with its index.
#
# Ubuntu's, from the current LTS: a recent kernel (7.0 in 26.04) whose KVM is
# what the sandboxes run under on a nested node, with userfaultfd, the broad
# driver set physical hardware needs, and headers for each exact build, which
# the NVIDIA modules are compiled against (fetch-nvidia.sh). It is also the
# kernel the Ubuntu VMs k0smos is compared with run, so a difference between
# them is k0smos's and not the kernel's.
#
# Modules no k0s node can use are dropped, because the module tree lives in
# the initramfs, which stays in memory: sound, cameras and TV tuners, display
# GPUs (NVIDIA's compute driver needs none of them), industrial I/O, wireless
# and bluetooth. Whatever then lacks a symbol from them goes too, so nothing
# is left to fail at load time.
#
# Ubuntu compresses modules with zstd. k0smos hands those to the kernel, which
# decompresses them itself (CONFIG_MODULE_DECOMPRESS).
#
# KERNEL_RELEASE pins the exact build, e.g. 7.0.0-43-generic, because the
# NVIDIA modules are compiled against headers for that one build and nothing
# else reproduces a GPU image. Ubuntu drops both from the archive when the
# release moves on, so a stale pin fails here, loudly, instead of silently
# building a different kernel than the last time. Leave it empty to take
# whatever linux-image-generic currently resolves to.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) apkarch=aarch64; platform=linux/arm64 ;;
  x86_64 | amd64) apkarch=x86_64; platform=linux/amd64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac
out="$repo/${KERNEL_ROOT:-dist/kernel-metal}/$apkarch"
ubuntu=${UBUNTU_RELEASE:-26.04}

command -v docker >/dev/null || { echo "docker required to unpack the .deb" >&2; exit 1; }
mkdir -p "$out"

# netfilter ships xt_DSCP.ko and xt_dscp.ko, and three more pairs like them.
# On a case-insensitive filesystem -- macOS's default -- they land on each
# other as the module tree is copied out, which surfaces as a bare "cp: File
# exists" from inside the container. Say what it actually is instead.
: > "$out/.case-test"
if [ -e "$out/.CASE-TEST" ]; then
  rm -f "$out/.case-test"
  echo "$out is on a case-insensitive filesystem, which cannot hold the kernel's" >&2
  echo "module tree (xt_DSCP.ko and xt_dscp.ko are different modules)." >&2
  echo "Build on Linux, or put the repository on a case-sensitive volume." >&2
  exit 1
fi
rm -f "$out/.case-test"

docker run --rm --platform "$platform" -v "$out:/out" -e owner="$(id -u):$(id -g)" \
  -e release="${KERNEL_RELEASE:-}" \
  "ubuntu:$ubuntu" bash -euo pipefail -c '
apt-get update -qq >/dev/null
apt-get install -qq -y --no-install-recommends kmod >/dev/null
cd /tmp
if [ -z "$release" ]; then
  apt-get download -qq linux-image-generic >/dev/null
  release=$(dpkg-deb -f linux-image-generic_*.deb Depends | grep -o "linux-image-[0-9][^ ,]*" | head -1)
  release=${release#linux-image-}
fi
apt-get download -qq "linux-image-$release" "linux-modules-$release" >/dev/null || {
  echo "linux-image-$release is not in this Ubuntu archive any more." >&2
  echo "Bump KERNEL_RELEASE in the Makefile to a build it still has." >&2
  exit 1
}
for d in linux-image-"$release"_*.deb linux-modules-"$release"_*.deb; do dpkg-deb -x "$d" x; done
mods=x/usr/lib/modules/$release
before=$(du -sm "$mods" | cut -f1)
rm -rf "$mods"/kernel/sound "$mods"/kernel/drivers/{media,gpu,iio,comedi,staging,bluetooth,net/wireless} \
  "$mods"/kernel/net/{wireless,mac80211,bluetooth}
# What needed a symbol from the dropped modules, until nothing does.
while true; do
  missing=$(depmod -b x/usr -e -F "x/boot/System.map-$release" "$release" 2>&1 |
    sed -n "s|.*: \(/.*\.ko[^ ]*\) needs unknown symbol.*|\1|p" | sort -u)
  [ -n "$missing" ] || break
  echo "$missing" | sed "s|.*/lib/modules/$release/||" | xargs -r -I{} rm -f "$mods/{}"
done
rm -rf /out/lib /out/vmlinuz /out/config
mkdir -p /out/lib/modules
cp "x/boot/vmlinuz-$release" /out/vmlinuz
cp "x/boot/config-$release" /out/config
cp -R "$mods" /out/lib/modules/
depmod -b /out "$release"
chown -R "$owner" /out
echo "wrote $release: modules ${before}M -> $(du -sm "/out/lib/modules/$release" | cut -f1)M"
'
grep -E '^CONFIG_(USERFAULTFD|MODULE_DECOMPRESS|EROFS_FS|HZ)=' "$out/config"
