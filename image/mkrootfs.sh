#!/usr/bin/env bash
# Assemble the real (switch_root) root filesystem: k0smos + k0s only.
#
# ROOTFS=erofs (default) writes a read-only image small enough to live inside the
# initramfs, which is what makes a node one artifact. Everything mutable then lives
# on the data volume at /var, so that volume is required rather than optional.
#
# erofs rather than squashfs because the default kernel (Kata's) builds in erofs and
# not squashfs: verified against the kernel image, which carries erofs's superblock
# error paths and kthread names and no squashfs driver at all.
#
# ROOTFS=ext4 writes a sparse ext4 image with room for k0s to work in, for a node
# that boots from a disk rather than carrying its root along — bare metal, once
# there is an installer for it.
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
  arm64 | aarch64) goarch=arm64; apkarch=aarch64; platform=linux/arm64
    triple=aarch64-linux-gnu; loader=/lib/ld-linux-aarch64.so.1 ;;
  x86_64 | amd64) goarch=amd64; apkarch=x86_64; platform=linux/amd64
    triple=x86_64-linux-gnu; loader=/lib64/ld-linux-x86-64.so.2 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

k0s_bin=${K0S_BIN:?set K0S_BIN to a linux k0s binary path}
moddir=${MODULES_DIR:-dist/kernel/$apkarch/lib/modules}

# Image identity for /etc/os-release. A node has no shell and no package
# manager, so this is the only way to ask a running machine which build it is
# -- and kubelet reports PRETTY_NAME as the node's OS-IMAGE, which puts it in
# `kubectl get nodes -o wide`. Resolved here, on the host, because the
# container stage below has neither git nor the dist tree's sibling files.
K0S_VERSION=${K0S_VERSION:-$(cat "$k0s_bin.version" 2>/dev/null || echo unknown)}
K0SMOS_BUILD=${K0SMOS_BUILD:-$(git -C "$repo" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}
export K0S_VERSION K0SMOS_BUILD

# Cross-compile on the host so the container stage needs only mkfs.ext4.
if [ -z "${K0SMOS_BIN:-}" ]; then
  K0SMOS_BIN=dist/k0smos-$goarch
  echo "building k0smos for linux/$goarch"
  GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 \
    go build -ldflags '-s -w -extldflags "-static"' -o "$K0SMOS_BIN" ./cmd/k0smos
  export K0SMOS_BIN
fi

rootfs=${ROOTFS:-erofs}
case "$rootfs" in
  ext4) mkfs_tool=mkfs.ext4; mkfs_pkg=e2fsprogs ;;
  erofs) mkfs_tool=mkfs.erofs; mkfs_pkg=erofs-utils ;;
  *) echo "unsupported ROOTFS=$rootfs (ext4 or erofs)" >&2; exit 1 ;;
esac

# The C library on the root. Only four binaries link against one at all --
# mount, umount, mkfs.ext4 and mkfs.xfs -- because k0smos, k0s and everything
# k0s stages are static Go. So this decides what *other* software can run on a
# node, not what k0smos needs: anything built for an ordinary distro expects
# glibc, and on a musl root it either falls back to a reduced static build or
# does not run. Both roots come out within a megabyte of each other, so the
# default is not about size.
#
# LIBC=musl assembles the Alpine-based root instead, which is what every k0smos
# image before this one carried.
libc=${LIBC:-glibc}
case "$libc" in
  glibc)
    build_img=ubuntu:${UBUNTU_RELEASE:-26.04}
    build_install="export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -qq -y --no-install-recommends $mkfs_pkg e2fsprogs xfsprogs mount ca-certificates >/dev/null"
    build_need=("$mkfs_tool" mount umount mkfs.ext4 mkfs.xfs ldconfig)
    ;;
  musl)
    build_img=alpine:3.23
    build_install="apk add -q --no-cache $mkfs_pkg bash >/dev/null"
    build_need=("$mkfs_tool" apk)
    ;;
  *) echo "unsupported LIBC=$libc (glibc or musl)" >&2; exit 1 ;;
esac

# Every tool is needed, not just the mkfs: this step also populates the
# userspace helpers below. A host with mkfs.ext4 but none of the rest — any
# Ubuntu, so every CI runner — used to build natively and then skip those
# helpers silently, producing an image with no mount(8), no e2fsprogs and no CA
# bundle. That surfaced only at runtime, as "mkfs.ext4: executable file not
# found in $PATH" while formatting a data volume, and would equally have broken
# kubelet and every image pull.
native=1
for t in "${build_need[@]}"; do
  command -v "$t" >/dev/null 2>&1 || native=
done
# Nothing to copy onto the root without one, and a root with no trust store
# fails every image pull.
if [ "$libc" = glibc ] && [ ! -e /etc/ssl/certs/ca-certificates.crt ]; then
  native=
fi
if [ -z "$native" ]; then
  command -v docker >/dev/null || {
    echo "need docker, or all of ${build_need[*]} on this host, to build the root image" >&2
    exit 1
  }
  # The container sees only the repository, mounted at /repo, so an absolute host
  # path has to be rewritten relative to it. Without this the image is written
  # inside the container, the success message names a path that looks right, and
  # the file disappears with --rm — a silent nothing rather than an error.
  case "$img" in
    "$repo"/*) img=${img#"$repo"/} ;;
    /*)
      echo "output path $img is outside the repository, which the build container cannot see" >&2
      exit 1
      ;;
  esac
  echo "assembling $libc root inside $platform container (need ${build_need[*]})"
  # Every knob has to be forwarded explicitly: the container stage re-runs this
  # script, and anything not listed here is silently lost on a macOS host.
  exec docker run --rm --platform "$platform" -v "$repo:/repo" -w /repo \
    -e K0S_BIN -e K0SMOS_BIN -e MODULES_DIR -e ARCH -e K0S_VERSION -e K0SMOS_BUILD \
    -e PAD_MB -e FSLABEL -e FSUUID -e APK_PKGS -e ROOT_TOOLS -e LIBC -e UBUNTU_RELEASE \
    -e ROOTFS -e FIRECRACKER_DIR \
    "$build_img" sh -c "$build_install"' && exec bash image/mkrootfs.sh "$1"' _ "$img"
fi

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT

mkdir -p "$root"/{sbin,usr/local/bin,etc/k0s,proc,sys,dev,run,tmp,var/lib/k0s,newroot,srv,mnt,lib/firmware}
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
#   xfsprogs       mkfs.xfs, for an extra disk that cloud-init's fs_setup asks
#                  to be XFS.
# k0s stages its own iptables, so those are not needed here.
#
# Neither path installs a shell: this image is specified to have none. On musl
# that means the narrow Alpine subpackages rather than the "util-linux" meta
# package, which would pull in busybox and /bin/sh. On glibc it means copying
# the binaries instead of unpacking .debs, whose maintainer scripts want one.
if [ "$libc" = musl ]; then
  apk_pkgs=${APK_PKGS:-mount umount ca-certificates-bundle e2fsprogs xfsprogs}
  if [ -n "$apk_pkgs" ] && ! command -v apk >/dev/null 2>&1; then
    # Never skip these quietly: the image boots and then fails in ways that
    # point nowhere near the build.
    echo "apk not available but APK_PKGS is set — cannot install $apk_pkgs" >&2
    exit 1
  fi
  if [ -n "$apk_pkgs" ]; then
    # --keys-dir and --repositories-file are required because --initdb creates
    # an empty root with no signing keys or repository list of its own.
    apk add -q --no-cache --root "$root" --initdb \
      --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
      $apk_pkgs
    echo "installed userspace helpers: $apk_pkgs (musl)"
  fi
else
  # Each binary with everything it links against, the same way fetch-nvidia.sh
  # assembles the NVIDIA driver root. ROOT_TOOLS is the glibc counterpart of
  # APK_PKGS: absolute paths in the build container, so add a tool's package to
  # build_install above when naming one that is not there already.
  tools=${ROOT_TOOLS:-/usr/bin/mount /usr/bin/umount /usr/sbin/mkfs.ext4 /usr/sbin/mkfs.xfs}
  lib=$root/usr/lib/$triple
  mkdir -p "$lib" "$root/usr/bin" "$root/usr/sbin" "$root/etc/ssl/certs"
  for t in $tools; do
    [ -x "$t" ] || { echo "$t is not in the $build_img build container" >&2; exit 1; }
    install -m 0755 "$t" "$root$t"
  done
  # The loader goes to the absolute path named in each binary's PT_INTERP;
  # everything else to the multiarch directory the cache below will index.
  for t in $tools; do
    ldd "$t" | awk '{for (i = 1; i <= NF; i++) if ($i ~ /^\//) print $i}'
  done | sort -u | while read -r dep; do
    case "${dep##*/}" in
      ld-linux* | ld64* | ld.so*)
        mkdir -p "$root$(dirname "$dep")"
        cp -L "$dep" "$root$dep"
        ;;
      *) [ -e "$lib/${dep##*/}" ] || cp -L "$dep" "$lib/" ;;
    esac
  done
  # mkfs.ext4 reads this for its feature defaults. Without it a data volume
  # comes out with a different feature set from every other ext4 around.
  cp /etc/mke2fs.conf "$root/etc/mke2fs.conf"
  # The trust store apk's ca-certificates-bundle provides on the musl root.
  cp /etc/ssl/certs/ca-certificates.crt "$root/etc/ssl/certs/"
  # A fresh root has no cache, and the multiarch directory is not on the
  # loader's built-in search path.
  echo "/usr/lib/$triple" > "$root/etc/ld.so.conf"
  ldconfig -r "$root"
  # Proves the root is self-contained: the loader resolves each helper from
  # inside it, with no host directory in reach. Cheaper and more honest than
  # running the tools, which have no common --version flag between them.
  for t in $tools; do
    if chroot "$root" "$loader" --list "$t" | grep -q "not found"; then
      echo "$t on the assembled root is missing a library" >&2
      exit 1
    fi
  done
  echo "installed userspace helpers: $tools (glibc)"
fi
# /sbin/k0smos must match initPath in cmd/k0smos/init_linux.go — that is the
# path the pre-switch k0smos re-executes after switching root.
install -m 0755 "$K0SMOS_BIN" "$root/sbin/k0smos"
install -m 0755 "$k0s_bin" "$root/usr/local/bin/k0s"
install -m 0644 "$here/k0s.yaml" "$root/etc/k0s/k0s.yaml"
# The sandbox VMM, for a sandbox-host image built with FIRECRACKER_DIR set. The
# released images do not set it: a VMM in every node is weight and attack
# surface for a capability most of them never use. Both binaries are statically
# linked, so they need nothing else in the image when they are included.
if [ -n "${FIRECRACKER_DIR:-}" ] && [ -d "$FIRECRACKER_DIR" ]; then
  install -m 0755 "$FIRECRACKER_DIR/firecracker" "$root/usr/local/bin/firecracker"
  install -m 0755 "$FIRECRACKER_DIR/jailer" "$root/usr/local/bin/jailer"
  echo "installed firecracker $(cat "$FIRECRACKER_DIR/VERSION" 2>/dev/null || echo '(unknown version)')"
fi
printf 'k0smos\n' > "$root/etc/hostname"
printf '127.0.0.1 localhost\n' > "$root/etc/hosts"
printf 'nameserver 1.1.1.1\n' > "$root/etc/resolv.conf"
printf 'NAME=k0smos\nID=k0smos\nVERSION_ID=%s\nBUILD_ID=%s\nPRETTY_NAME="k0smos (k0s %s)"\n' \
  "$K0S_VERSION" "$K0SMOS_BUILD" "$K0S_VERSION" > "$root/etc/os-release"
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
  mkfs.erofs -zlz4hc -T0 \
    -U "${FSUUID:-c0dec0de-0000-4000-8000-000000000001}" \
    -L "${FSLABEL:-k0smos}" "$img" "$root" >/dev/null
  echo "wrote $img ($(du -m "$img" | cut -f1)M erofs, read-only, linux/$goarch)"
  exit 0
fi

# Headroom on top of the content. This is working space, not storage: k0s
# extracts its embedded containerd/runc/kubelet into /var/lib/k0s/bin at
# runtime, and pulled container images land there too. Size it for the images a
# node will pull, since that dominates.
#
# Nothing here is expected to persist. This ext4 mode is for direct-kernel disk
# boot; the default KubeVirt artifact uses an erofs root embedded in its initramfs.
# On bare metal a disk would persist, but Cluster API replaces machines rather
# than repairing them either way.
#
# The file is sparse, so a generous number costs little: a 3 GB pad over ~290 MB
# of content allocates ~290 MB and compresses to ~220 MB in an OCI layer.
pad_mb=${PAD_MB:-3072}
size_mb=$(( $(du -sm "$root" | cut -f1) + pad_mb ))
truncate -s "${size_mb}M" "$img"
# -L gives PID1 the canonical LABEL=k0smos root it discovers automatically,
# avoiding device paths that can reorder between boots. k0smos.root= remains an
# override for non-canonical images.
uuid_args=()
[ -n "${FSUUID:-}" ] && uuid_args=(-U "$FSUUID")
mkfs.ext4 -q -L "${FSLABEL:-k0smos}" "${uuid_args[@]}" -d "$root" "$img"
echo "wrote $img (${size_mb}M apparent, $(du -m "$img" | cut -f1)M allocated, linux/$goarch, LABEL=${FSLABEL:-k0smos})"
