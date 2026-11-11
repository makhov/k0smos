#!/usr/bin/env bash
# Ask a running k0smos guest to shut down cleanly via its control port.
#
# Use this instead of killing QEMU: a hard kill leaves the ext4 root with
# "Block bitmap checksum does not match", which loses recent writes and makes
# the image unreadable with debugfs afterwards.
set -euo pipefail
sock=${1:-dist/control.sock}
cmd=${CMD:-poweroff}

[ -S "$sock" ] || { echo "no control socket at $sock (is the guest running?)" >&2; exit 1; }
# -w: nc reads until the guest closes the socket, and a guest that takes the
# command without closing hangs the caller indefinitely. The reply is discarded
# anyway, so there is nothing to wait for beyond the write.
printf '%s\n' "$cmd" | nc -w 5 -U "$sock"
echo "sent $cmd to $sock"
