#!/usr/bin/env bash
# Assemble the real (switch_root) root filesystem: k0smos + k0s only.
#
# ROOTFS=ext4 (default) writes a sparse ext4 image with room for k0s to work in.
# ROOTFS=erofs writes a read-only image instead, small enough to live inside the
# initramfs — which removes the separate root artifact altogether. erofs rather
# than squashfs because the default kernel (Kata's) builds in erofs and not
# squashfs: verified against the kernel image, which carries erofs's superblock
# error paths and kthread names and no squashfs driver at all.
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

rootfs=${ROOTFS:-ext4}
case "$rootfs" in
  ext4) mkfs_tool=mkfs.ext4; apk_build=e2fsprogs ;;
  erofs) mkfs_tool=mkfs.erofs; apk_build=erofs-utils ;;
  *) echo "unsupported ROOTFS=$rootfs (ext4 or erofs)" >&2; exit 1 ;;
esac

if ! command -v "$mkfs_tool" >/dev/null 2>&1; then
  command -v docker >/dev/null || {
    echo "need either $mkfs_tool (Linux) or docker to build the root image" >&2
    exit 1
  }
  echo "no $mkfs_tool on this host — assembling inside $platform container"
  # Every knob has to be forwarded explicitly: the container stage re-runs this
  # script, and anything not listed here is silently lost on a macOS host.
  exec docker run --rm --platform "$platform" -v "$repo:/repo" -w /repo \
    -e K0S_BIN -e K0SMOS_BIN -e MODULES_DIR -e ARCH \
    -e PAD_MB -e FSLABEL -e APK_PKGS -e ROOTFS \
    alpine:3.20 sh -c 'apk add -q --no-cache '"$apk_build"' bash >/dev/null && exec bash image/mkrootfs.sh "$1"' _ "$img"
fi

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT

mkdir -p "$root"/{sbin,usr/local/bin,etc/k0s,proc,sys,dev,run,tmp,var/lib/k0s,newroot}
# Directories that components only need to *exist*. Creating them here rather than
# at runtime is what keeps them off the writable-path list: on a read-only root the
# kube-controller-manager plugin prober otherwise logs "error (re-)creating driver
# directory: mkdir /usr/libexec: read-only file system" on every start.
mkdir -p "$root"/usr/libexec/kubernetes/kubelet-plugins/volume/exec
# /opt is a symlink onto the data volume rather than an overlay: containerd stages
# its plugins under /opt/containerd and kube-router's CNI binaries land in
# /opt/cni, which is tens of megabytes — that belongs on disk, not in a tmpfs.
# Talos does the same thing for the same reason.
ln -sfn /var/opt "$root/opt"
# /var/run is a symlink to /run on every modern distro, and its absence is fatal
# on a read-only root: containerd's NRI socket lands in /var/run/nri, so it tries
# mkdir /var/run and dies with "failed to set up NRI for CRI service". /run is
# already a tmpfs, so the symlink puts it somewhere writable.
ln -sfn /run "$root/var/run"
# /lib/modules must exist even on a monolithic kernel with nothing in it: kubelet
# bind-mounts it into kube-router, and creating it is a mkdir on a read-only root
# otherwise. The modular path populates it a few lines below.
mkdir -p "$root/lib/modules"

# Userspace bits k0s/kubelet genuinely need, each added in response to an
# observed boot failure:
#   mount, umount  kubelet shells out to mount(8) for projected volumes; without
#                  it every pod fails 'mount failed: exec: "mount": executable
#                  file not found in $PATH' and no CNI pod ever starts.
#   ca-certificates-bundle  image pulls fail TLS verification without a trust
#                  store: 'x509: certificate signed by unknown authority'.
#   e2fsprogs      mkfs.ext4, to format a blank data volume on first boot. Talos
#                  bundles mkfs for the same reason: it formats its own
#                  EPHEMERAL partition rather than requiring a pre-made one.
# k0s stages its own iptables, so those are not needed here.
#
# Deliberately the narrow subpackages, not the "util-linux" meta package: that
# one pulls in busybox and /bin/sh, and this image is specified to have no shell.
apk_pkgs=${APK_PKGS:-mount umount ca-certificates-bundle e2fsprogs}
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
  # Expected with a monolithic kernel — see the same note in mkinitramfs.sh.
  echo "no modules dir at $moddir — assuming a monolithic kernel" >&2
fi

mkdir -p "$(dirname "$img")"
rm -f "$img"

if [ "$rootfs" = erofs ]; then
  # Read-only, so no padding and no working space: everything mutable lives on
  # the data volume at /var/lib/k0s, which a node therefore requires.
  #
  # -zlz4hc compresses well while staying cheap to read at random, which is what
  # a root filesystem does. -T0 pins timestamps so the same tree gives the same
  # bytes, keeping the initramfs that carries it reproducible.
  mkfs.erofs -q -zlz4hc -T0 -U "${FSUUID:-c0dec0de-0000-4000-8000-k0smos00000}" "$img" "$root" >/dev/null 2>&1 ||
    mkfs.erofs -zlz4hc -T0 "$img" "$root" >/dev/null
  echo "wrote $img ($(du -m "$img" | cut -f1)M erofs, read-only, linux/$goarch)"
  exit 0
fi

# Headroom on top of the content. This is working space, not storage: k0s
# extracts its embedded containerd/runc/kubelet into /var/lib/k0s/bin at
# runtime, and pulled container images land there too. Size it for the images a
# node will pull, since that dominates.
#
# Nothing here is expected to persist. On KubeVirt a containerDisk is read-only
# and the guest writes to an ephemeral overlay that is discarded with the pod;
# on bare metal the disk does persist, but Cluster API replaces machines rather
# than repairing them either way.
#
# The file is sparse, so a generous number costs little: a 3 GB pad over ~290 MB
# of content allocates ~290 MB and compresses to ~220 MB in an OCI layer.
pad_mb=${PAD_MB:-3072}
size_mb=$(( $(du -sm "$root" | cut -f1) + pad_mb ))
truncate -s "${size_mb}M" "$img"
# -L so the root can be named as LABEL=k0smos on the kernel cmdline instead of
# a device path, which is not dependable on real hardware where disks enumerate
# as /dev/sda or /dev/nvme0n1 and can reorder between boots.
mkfs.ext4 -q -L "${FSLABEL:-k0smos}" -d "$root" "$img"
echo "wrote $img (${size_mb}M apparent, $(du -m "$img" | cut -f1)M allocated, linux/$goarch, LABEL=${FSLABEL:-k0smos})"
