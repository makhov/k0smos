#!/usr/bin/env bash
# Boot k0smos in QEMU headless, wait for the readiness marker on the serial
# console, then shut the VM down. Exits non-zero if the marker never appears.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
img=${1:-dist/k0smos.img}
# Its own log rather than dist/console.log, so a run of this cannot truncate a
# console someone is reading. (k0smosctl keeps a guest's console under its state
# directory instead, which is out of the way of both.)
log=${LOG:-dist/accept-console.log}
timeout_s=${TIMEOUT:-900}

# Taken from a real console log of a node reaching Ready (k0s v1.36.3), not
# guessed: kubelet logs the first, kube-controller-manager the second.
marker=${MARKER:-'just became ready|Exiting master disruption mode'}

mkdir -p "$(dirname "$log")"
: > "$log"

# QEMU writes serial straight to the log — no pipeline, so $! is QEMU itself
# and the kill below actually reaches it.
sock=${CONTROL:-dist/control.sock}
SERIAL="$log" IMG="$img" CONTROL="$sock" MEM=${MEM:-8192} CPUS=${CPUS:-4} "$here/run-qemu.sh" &
qpid=$!

# Always try a clean shutdown first: killing QEMU corrupts the ext4 root, which
# also destroys the pod logs needed to diagnose a failure.
cleanup() {
  if [ -S "$sock" ] && kill -0 "$qpid" 2>/dev/null; then
    CMD=poweroff "$here/poweroff.sh" "$sock" >/dev/null 2>&1 || true
    for _ in $(seq 1 30); do
      kill -0 "$qpid" 2>/dev/null || break
      sleep 1
    done
  fi
  kill "$qpid" 2>/dev/null || true
  wait "$qpid" 2>/dev/null || true
}
trap cleanup EXIT

ok=0
for _ in $(seq 1 $(( timeout_s / 5 ))); do
  if ! kill -0 "$qpid" 2>/dev/null; then
    echo "ACCEPTANCE FAIL: QEMU exited early" >&2
    break
  fi
  if grep -Eq "$marker" "$log"; then
    ok=1
    break
  fi
  sleep 5
done

if [ "$ok" -ne 1 ]; then
  echo "ACCEPTANCE FAIL: readiness marker not found in $log" >&2
  tail -50 "$log" >&2
  exit 1
fi
echo "ACCEPTANCE PASS (marker matched in $log)"
