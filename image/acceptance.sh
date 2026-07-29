#!/usr/bin/env bash
# Boot k0smos in QEMU headless, wait for the readiness marker on the serial
# console, then shut the VM down. Exits non-zero if the marker never appears.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
img=${1:-dist/k0smos.img}
log=${LOG:-dist/console.log}
timeout_s=${TIMEOUT:-600}

# NOTE: tighten this against a real dist/console.log before trusting a pass.
# The exact string depends on the k0s version's log output.
marker=${MARKER:-'Kube-api server is ready|node .* Ready|kubelet is ready'}

mkdir -p "$(dirname "$log")"
: > "$log"

# QEMU writes serial straight to the log — no pipeline, so $! is QEMU itself
# and the kill below actually reaches it.
SERIAL="$log" "$here/run-qemu.sh" "$img" &
qpid=$!
cleanup() {
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
