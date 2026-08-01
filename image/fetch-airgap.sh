#!/usr/bin/env bash
# Download the k0s airgap image bundle into dist/k0s-airgap-<arch>.tar.
#
# k0s imports anything under <data-dir>/images into containerd at startup, so a
# node with this bundle in place never pulls. That is worth ~350MB of download
# because the alternative is paying for the pull on every boot: a k0s e2e boot has
# been observed taking 5 to 13 minutes fetching the same images over QEMU's
# user-mode network, which is why k0sTimeout is 25 minutes and the nightly job's
# timeout is nearly two hours.
#
# This is for tests. A shipped node image deliberately does not carry it: it would
# add ~350MB to an artifact whose whole point is being small, and a real cluster
# pulls from a registry it can reach.
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
  ver=$(curl -fsSL https://api.github.com/repos/k0sproject/k0s/releases/latest |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
[ -n "$ver" ] || { echo "could not resolve a k0s version" >&2; exit 1; }

out="$repo/dist/k0s-airgap-$k0sarch.tar"
if [ -f "$out" ]; then
  echo "airgap bundle already present: $out ($(du -m "$out" | cut -f1)M)"
  exit 0
fi

# The version contains a "+", which has to survive the URL.
enc=${ver//+/%2B}
url="https://github.com/k0sproject/k0s/releases/download/${enc}/k0s-airgap-bundle-${ver}-linux-${k0sarch}.tar"
mkdir -p "$repo/dist"
echo "fetching k0s airgap bundle $ver ($k0sarch) — a few hundred MB, once"
curl -fL --progress-bar -o "$out.part" "$url"
mv "$out.part" "$out"
echo "wrote $out ($(du -m "$out" | cut -f1)M, $ver)"
