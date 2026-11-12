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
# -w is nc's idle timeout, and it has to cover the guest not listening yet.
# Until the guest opens its end of the port, QEMU leaves the command unread in
# the socket, and if nc hangs up first the command goes with the connection.
# Early in boot that gap is several seconds: a 5s timeout lost every poweroff
# sent right after user-data was written (QEMU's trace showed the host connect
# and disconnect with no read in between). The guest acks as soon as it reads
# the command and then powers off, closing the socket, so in practice nc exits
# well inside this; the bound is for a guest that never reads or never closes.
printf '%s\n' "$cmd" | nc -w 60 -U "$sock"
echo "sent $cmd to $sock"
