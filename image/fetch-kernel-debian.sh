#!/usr/bin/env bash
# Fetch Debian's kernel for the metal image into dist/kernel-metal/<arch>/:
# vmlinuz, config and lib/modules/<release> with its index.
#
# Debian's rather than Alpine's linux-lts: Alpine builds without userfaultfd,
# which Firecracker's snapshot restore needs, and Debian's is built for broad
# hardware and publishes headers for each exact build, which the NVIDIA modules
# are compiled against (fetch-nvidia.sh).
#
# Debian compresses its modules with xz. k0smos hands those to the kernel, which
# decompresses them itself (CONFIG_MODULE_DECOMPRESS), so they stay compressed
# in the initramfs.
#
# Tracks the suite's current kernel, security updates included, as fetching
# Alpine's did; the release it got is printed and is in the module tree's name.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) apkarch=aarch64; debarch=arm64 ;;
  x86_64 | amd64) apkarch=x86_64; debarch=amd64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac
out="$repo/${KERNEL_ROOT:-dist/kernel-metal}/$apkarch"
suite=${DEBIAN_SUITE:-trixie}

command -v docker >/dev/null || { echo "docker required to unpack the .deb" >&2; exit 1; }
mkdir -p "$out"

# The container runs natively whatever the target: it only downloads and
# unpacks, and depmod reads modules of any architecture.
docker run --rm -v "$out:/out" \
  -e debarch="$debarch" -e owner="$(id -u):$(id -g)" \
  "debian:$suite-slim" bash -euo pipefail -c '
dpkg --add-architecture "$debarch"
apt-get update -qq >/dev/null
apt-get install -qq -y --no-install-recommends kmod >/dev/null
cd /tmp
apt-get download -qq "linux-image-$debarch:$debarch" >/dev/null
pkg=$(dpkg-deb -f linux-image-"$debarch"_*.deb Depends | grep -o "linux-image-[^ ,]*" | head -1)
apt-get download -qq "$pkg:$debarch" >/dev/null
dpkg-deb -x "$pkg"_*.deb x
release=$(ls x/usr/lib/modules)
rm -rf /out/lib /out/vmlinuz /out/config
mkdir -p /out/lib/modules
cp "x/boot/vmlinuz-$release" /out/vmlinuz
cp "x/boot/config-$release" /out/config
cp -R "x/usr/lib/modules/$release" /out/lib/modules/
# The package leaves the index to its postinst.
depmod -b /out "$release"
chown -R "$owner" /out
echo "wrote $release"
'
# One at a time: a single grep over all three passes while any one of them is set.
for c in USERFAULTFD MODULE_DECOMPRESS EROFS_FS; do
  grep -q "^CONFIG_$c=" "$out/config" || { echo "Debian's kernel is built without CONFIG_$c" >&2; exit 1; }
done
