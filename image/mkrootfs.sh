#!/usr/bin/env bash
# Assemble a minimal ext4 rootfs image containing only k0smos + k0s.
# Linux-only: needs mkfs.ext4 with -d (e2fsprogs >= 1.43).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
img=${1:-dist/k0smos.img}
k0s_bin=${K0S_BIN:?set K0S_BIN to a linux k0s binary path}

command -v mkfs.ext4 >/dev/null || {
  echo "mkfs.ext4 not found — run this on Linux (or in a Linux container)" >&2
  exit 1
}
[ -x "$repo/dist/k0smos" ] || {
  echo "dist/k0smos missing — run 'make build' first" >&2
  exit 1
}

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT

mkdir -p "$root"/{sbin,usr/local/bin,etc/k0s,proc,sys,dev,run,tmp,var/lib/k0s}
install -m 0755 "$repo/dist/k0smos" "$root/sbin/k0smos"
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

# size: k0s embeds containerd/runc/etc, so pad generously for image pulls too
size_mb=$(( $(du -sm "$root" | cut -f1) + 2048 ))
mkdir -p "$(dirname "$img")"
rm -f "$img"
truncate -s "${size_mb}M" "$img"
mkfs.ext4 -q -d "$root" "$img"
echo "wrote $img (${size_mb}M)"
