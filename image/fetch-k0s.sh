#!/usr/bin/env bash
# Download a k0s binary from GitHub releases into dist/k0s-<arch>.
# K0S_VERSION defaults to the latest stable release.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64 | aarch64) k0sarch=arm64 ;;
  x86_64 | amd64) k0sarch=amd64 ;;
  *) echo "unsupported ARCH=$arch" >&2; exit 1 ;;
esac

ver=${K0S_VERSION:-}
if [ -z "$ver" ]; then
  # The k0s "latest" release tag, e.g. v1.34.1+k0s.0.
  ver=$(curl -fsSL https://api.github.com/repos/k0sproject/k0s/releases/latest |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$ver" ] || { echo "could not determine latest k0s version" >&2; exit 1; }
fi

# The '+' in the tag must stay literal in the filename but be %2B in the URL.
enc=${ver//+/%2B}
url="https://github.com/k0sproject/k0s/releases/download/${enc}/k0s-${enc}-${k0sarch}"
out="$repo/dist/k0s-${k0sarch}"

echo "fetching k0s $ver ($k0sarch)"
mkdir -p "$repo/dist"
curl -fsSL --retry 3 -o "$out.part" "$url"
mv "$out.part" "$out"
chmod 0755 "$out"
echo "wrote dist/k0s-${k0sarch} ($(du -h "$out" | cut -f1), $ver)"
