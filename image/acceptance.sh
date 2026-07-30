#!/usr/bin/env bash
# Boot k0smos in QEMU headless, wait for the readiness marker on the serial
# console, then shut the VM down. Exits non-zero if the marker never appears.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
img=${1:-dist/k0smos.img}
log=${LOG:-dist/console.log}
timeout_s=${TIMEOUT:-900}

# NOTE: tighten this against a real dist/console.log before trusting a pass.
# The exact string depends on the k0s version's log output.
marker=${MARKER:-'Kube-api server is ready|node .* Ready|kubelet is ready'}

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
