#!/usr/bin/env bash
# Assemble the real (switch_root) ext4 root filesystem: k0smos + k0s only.
#
# This is the root kubelet needs. Booting still goes through the initramfs
# (mkinitramfs.sh) because a stock kernel cannot see this disk until k0smos has
# loaded virtio_blk and ext4; k0smos then switch_roots onto it.
#
# Needs mkfs.ext4 -d, which is Linux-only, so on macOS the assembly step is
# re-run inside a Linux container. The k0smos binary is cross-compiled on the
# host beforehand, so that container needs no Go toolchain.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cd "$repo"
img=${1:-dist/k0smos.img}
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) goarch=arm64; apkarch=aarch64; platform=linux/arm64 ;;
  x86_64 | amd64) goarch=amd64; apkarch=x86_64; platform=linux/amd64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

k0s_bin=${K0S_BIN:?set K0S_BIN to a linux k0s binary path}
moddir=${MODULES_DIR:-dist/kernel/$apkarch/lib/modules}

# Cross-compile on the host so the container stage needs only mkfs.ext4.
if [ -z "${K0SMOS_BIN:-}" ]; then
  K0SMOS_BIN=dist/k0smos-$goarch
  echo "building k0smos for linux/$goarch"
  GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 \
    go build -ldflags '-s -w -extldflags "-static"' -o "$K0SMOS_BIN" ./cmd/k0smos
  export K0SMOS_BIN
fi

if ! command -v mkfs.ext4 >/dev/null 2>&1; then
  command -v docker >/dev/null || {
    echo "need either mkfs.ext4 (Linux) or docker to build the ext4 image" >&2
    exit 1
  }
  echo "no mkfs.ext4 on this host — assembling inside $platform container"
  exec docker run --rm --platform "$platform" -v "$repo:/repo" -w /repo \
    -e K0S_BIN -e K0SMOS_BIN -e MODULES_DIR -e ARCH \
    alpine:3.20 sh -c 'apk add -q --no-cache e2fsprogs bash >/dev/null && exec bash image/mkrootfs.sh "$1"' _ "$img"
fi

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT

mkdir -p "$root"/{sbin,usr/local/bin,etc/k0s,proc,sys,dev,run,tmp,var/lib/k0s,newroot}

# Userspace bits k0s/kubelet genuinely need, each added in response to an
# observed boot failure:
#   mount, umount  kubelet shells out to mount(8) for projected volumes; without
#                  it every pod fails 'mount failed: exec: "mount": executable
#                  file not found in $PATH' and no CNI pod ever starts.
#   ca-certificates-bundle  image pulls fail TLS verification without a trust
#                  store: 'x509: certificate signed by unknown authority'.
# k0s stages its own iptables, so those are not needed here.
#
# Deliberately the narrow subpackages, not the "util-linux" meta package: that
# one pulls in busybox and /bin/sh, and this image is specified to have no shell.
apk_pkgs=${APK_PKGS:-mount umount ca-certificates-bundle}
if [ -n "$apk_pkgs" ] && command -v apk >/dev/null 2>&1; then
  # --keys-dir and --repositories-file are required because --initdb creates an
  # empty root with no signing keys or repository list of its own.
  apk add -q --no-cache --root "$root" --initdb \
    --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
    $apk_pkgs
  echo "installed userspace helpers: $apk_pkgs"
fi
# /sbin/k0smos must match initPath in cmd/k0smos/init_linux.go — that is the
# path the pre-switch k0smos re-executes after switching root.
install -m 0755 "$K0SMOS_BIN" "$root/sbin/k0smos"
install -m 0755 "$k0s_bin" "$root/usr/local/bin/k0s"
install -m 0644 "$here/k0s.yaml" "$root/etc/k0s/k0s.yaml"
printf 'k0smos\n' > "$root/etc/hostname"
printf '127.0.0.1 localhost\n' > "$root/etc/hosts"
printf 'nameserver 1.1.1.1\n' > "$root/etc/resolv.conf"
printf 'NAME=k0smos\nID=k0smos\n' > "$root/etc/os-release"
# k0s looks up UIDs for its components and warns "open /etc/passwd: no such
# file or directory" (then runs everything as root) without these.
printf 'root:x:0:0:root:/root:/sbin/nologin\nnobody:x:65534:65534:nobody:/:/sbin/nologin\n' > "$root/etc/passwd"
printf 'root:x:0:\nnobody:x:65534:\n' > "$root/etc/group"

# Modules again on the real root: the post-switch k0smos re-runs module loading,
# and containerd may need more than the boot set.
if [ -d "$moddir" ]; then
  mkdir -p "$root/lib/modules"
  cp -R "$moddir/." "$root/lib/modules/"
else
  echo "warn: no modules dir at $moddir" >&2
fi

# k0s embeds containerd/runc/kubelet and extracts them at runtime, and pulled
# images land here too — pad generously.
size_mb=$(( $(du -sm "$root" | cut -f1) + 3072 ))
mkdir -p "$(dirname "$img")"
rm -f "$img"
truncate -s "${size_mb}M" "$img"
# -L so the root can be named as LABEL=k0smos on the kernel cmdline instead of
# a device path, which is not dependable on real hardware where disks enumerate
# as /dev/sda or /dev/nvme0n1 and can reorder between boots.
mkfs.ext4 -q -L "${FSLABEL:-k0smos}" -d "$root" "$img"
echo "wrote $img (${size_mb}M, linux/$goarch, LABEL=${FSLABEL:-k0smos})"
