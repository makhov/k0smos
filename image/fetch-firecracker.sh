#!/usr/bin/env bash
# Download the Firecracker VMM and its jailer from GitHub releases into
# dist/firecracker-<arch>/. FIRECRACKER_VERSION defaults to the latest release.
#
# Both binaries are statically linked, which is why they can go into an image
# that has no shell and only the handful of apk packages mkrootfs installs.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) fcarch=aarch64 ;;
  x86_64 | amd64) fcarch=x86_64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

ver=${FIRECRACKER_VERSION:-}
if [ -z "$ver" ]; then
  ver=$(curl -fsSL https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$ver" ] || { echo "could not determine latest firecracker version" >&2; exit 1; }
fi

base="https://github.com/firecracker-microvm/firecracker/releases/download/$ver"
tgz="firecracker-$ver-$fcarch.tgz"
out="$repo/dist/firecracker-$fcarch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "fetching firecracker $ver ($fcarch)"
curl -fsSL --retry 3 -o "$tmp/$tgz" "$base/$tgz"
curl -fsSL --retry 3 -o "$tmp/$tgz.sha256" "$base/$tgz.sha256.txt"

# Verified, unlike the k0s fetch next door: these binaries get root on every
# sandbox host, and upstream publishes a checksum, so there is no reason not to.
# GNU coreutils on CI, BSD shasum on a developer's mac.
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp" && sha256sum -c "$tgz.sha256" >/dev/null)
else
  (cd "$tmp" && shasum -a 256 -c "$tgz.sha256" >/dev/null)
fi

# Only the VMM and the jailer. The rest of the tarball is debug symbols, CPU
# templates and the seccomp filter source — Firecracker compiles its default
# filter in, so none of it is needed at runtime.
tar -xzf "$tmp/$tgz" -C "$tmp" \
  "release-$ver-$fcarch/firecracker-$ver-$fcarch" \
  "release-$ver-$fcarch/jailer-$ver-$fcarch"

mkdir -p "$out"
install -m 0755 "$tmp/release-$ver-$fcarch/firecracker-$ver-$fcarch" "$out/firecracker"
install -m 0755 "$tmp/release-$ver-$fcarch/jailer-$ver-$fcarch" "$out/jailer"
printf '%s\n' "$ver" > "$out/VERSION"
echo "wrote dist/firecracker-$fcarch/{firecracker,jailer} ($(du -ch "$out/firecracker" "$out/jailer" | tail -1 | cut -f1), $ver)"
