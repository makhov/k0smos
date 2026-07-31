#!/usr/bin/env bash
# Fetch a Kata Containers guest kernel into dist/kernel/<arch>/vmlinuz.
#
# Why this instead of Alpine's linux-virt: Kata's kernel is monolithic, and it
# already builds in everything a k0s node needs — verified against its config
# fragments and then by booting. Using it removes the module tree entirely, and
# with it the 50 hard-coded module names, the ~21 MB they add to the initramfs,
# and the kernel/module version-skew hazard.
#
# Confirmed working: a node reaches Ready with zero modules loaded.
#
# The catch: this is a *guest* kernel. It has no NVME, ATA, SCSI, USB or physical
# NIC drivers, so it cannot boot bare metal. Use fetch-kernel.sh (Alpine) there.
#
# Apple's `container` uses the same artifact, pinning url + digest + inner path.
# This does the same, except the digest pins the kernel itself rather than the
# archive: the archive is 999 MB and we only want 18 MB of it, so the stream is
# aborted as soon as tar has the member — 170 MB transferred instead of 999 MB.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)

# Pinned so builds are reproducible. Bumping means updating the version, the
# kernel path (it carries the version) and the digests together.
kata_version=${KATA_VERSION:-4.0.0}
kernel_release=${KATA_KERNEL_RELEASE:-6.18.35-200}

arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64)
    apkarch=aarch64; kata_arch=arm64
    # arm64: vmlinux is the raw Image QEMU wants; vmlinuz is gzip-wrapped.
    kernel_name=${KATA_KERNEL:-vmlinux-$kernel_release}
    want_sha=${KATA_KERNEL_SHA256:-4a8998a2e7ac12d6ad1f15b5e7d00571e4518ea9f33db1a1a568310373ca428d}
    ;;
  x86_64 | amd64)
    apkarch=x86_64; kata_arch=amd64
    # x86: vmlinuz is the bzImage, which is what QEMU boots. vmlinux is the ELF
    # used by Firecracker and Cloud Hypervisor.
    kernel_name=${KATA_KERNEL:-vmlinuz-$kernel_release}
    want_sha=${KATA_KERNEL_SHA256:-f10415216b52b2f05d173f8ea20934c386b5911c1d22b99ff02c30b64977ea34}
    ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

out="$repo/dist/kernel/$apkarch"
kernel="$out/vmlinuz"

# Cached? The archive is large enough that re-fetching it casually is rude.
if [ -f "$kernel" ] && [ -n "$want_sha" ]; then
  have=$(shasum -a 256 "$kernel" 2>/dev/null | cut -d' ' -f1 || echo none)
  if [ "$have" = "$want_sha" ]; then
    echo "kernel already present and matches digest: $kernel"
    exit 0
  fi
fi

command -v docker >/dev/null || { echo "docker required (needs zstd)" >&2; exit 1; }
mkdir -p "$out"
url="https://github.com/kata-containers/kata-containers/releases/download/${kata_version}/kata-static-${kata_version}-${kata_arch}.tar.zst"
echo "fetching $kernel_name from kata-static $kata_version ($kata_arch)"

docker run --rm -v "$out:/out" -e URL="$url" -e MEMBER="./opt/kata/share/kata-containers/$kernel_name" \
  alpine:3.20 sh -c '
set -e
apk add -q --no-cache curl zstd tar >/dev/null
cd /tmp
# --occurrence=1 makes tar exit after the member, which SIGPIPEs curl. That is
# what turns a 999 MB download into ~170 MB.
# curl exits 23 when tar closes the pipe early; that abort is the point, so its
# complaint is suppressed rather than reported as a failure.
curl -fsSL "$URL" 2>/dev/null | zstd -dc 2>/dev/null | tar -xf - --occurrence=1 "$MEMBER" 2>/dev/null || true
[ -f "$MEMBER" ] || { echo "member $MEMBER not found in archive" >&2; exit 1; }
cp "$MEMBER" /out/vmlinuz
'

got=$(shasum -a 256 "$kernel" | cut -d' ' -f1)
if [ -n "$want_sha" ] && [ "$got" != "$want_sha" ]; then
  echo "digest mismatch for $kernel" >&2
  echo "  want $want_sha" >&2
  echo "  got  $got" >&2
  rm -f "$kernel"
  exit 1
fi

# Deliberately no module tree: this kernel is monolithic, and k0smos reports
# "no module tree; assuming a monolithic kernel" when /lib/modules is absent.
# Leaving a stale tree behind would instead trigger the version-skew warning.
rm -rf "$out/lib" "$out/config"

echo "wrote $kernel ($(du -h "$kernel" | cut -f1), sha256:$got)"
echo "monolithic: no module tree written — build images with MODULES_DIR=/nonexistent"
