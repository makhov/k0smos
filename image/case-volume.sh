#!/usr/bin/env bash
# Give the kernel's module tree a case-sensitive filesystem, on a host whose
# own is not.
#
# macOS formats its boot volume case-insensitively, and the module tree cannot
# be unpacked there at all: netfilter ships four pairs of modules whose names
# differ only in case -- xt_DSCP.ko with xt_dscp.ko, and the same for HL,
# RATEEST and TCPMSS -- because iptables names a target in upper case and a
# match in lower. The second of each pair lands on the first, and the build
# stops with "cp: File exists". Nothing can work around it in the build: both
# modules are real, and dropping either ships a kernel that cannot do -j DSCP
# or cannot do -m dscp.
#
# So this attaches a sparse disk image, formatted case-sensitive APFS, at
# dist/kernel-metal. It is a file, not a partition: nothing about the real disk
# changes, it grows only as the tree fills it, and detaching it and deleting
# the file undoes everything. The container builds write the tree through it
# and read it back, because the host filesystem share crosses into the mount.
#
#   ./image/case-volume.sh          attach (creating the image the first time)
#   ./image/case-volume.sh detach   give it back
#
# Idempotent, and a no-op where the filesystem already distinguishes the two
# spellings -- every Linux host, and a repository already on such a volume.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
target="$repo/${KERNEL_ROOT:-dist/kernel-metal}"
image="$target.sparseimage"
size=${CASE_VOLUME_SIZE:-8g}

# Does this directory already tell the two spellings apart?
case_sensitive() {
	local dir=$1 probe
	[ -d "$dir" ] || return 1
	probe=$dir/.case-probe
	: > "$probe"
	if [ -e "$dir/.CASE-PROBE" ]; then
		rm -f "$probe"
		return 1
	fi
	rm -f "$probe"
	return 0
}

if [ "${1:-attach}" = detach ]; then
	if mount | grep -q " on $target "; then
		hdiutil detach -quiet "$target"
		echo "detached $target; the image is still at $image"
	else
		echo "$target is not a mounted image; nothing to detach"
	fi
	exit 0
fi

mkdir -p "$target"
if case_sensitive "$target"; then
	echo "$target already tells xt_DSCP from xt_dscp; nothing to do"
	exit 0
fi
if [ "$(uname -s)" != Darwin ]; then
	echo "$target is on a case-insensitive filesystem, and this helper only knows" >&2
	echo "how to fix that on macOS. Move the repository to a case-sensitive volume." >&2
	exit 1
fi

if [ ! -e "$image" ]; then
	# Sparse, so the file is a few megabytes until the tree is written into it,
	# and never larger than the tree needs.
	hdiutil create -size "$size" -fs "Case-sensitive APFS" -volname k0smos-build \
		-type SPARSE -quiet "${image%.sparseimage}"
	echo "created $image (sparse, up to $size)"
fi
hdiutil attach -quiet -mountpoint "$target" "$image"
case_sensitive "$target" || { echo "$target is still case-insensitive after attaching $image" >&2; exit 1; }
echo "attached $image at $target; the module tree can be unpacked there now"
