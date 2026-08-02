#!/usr/bin/env bash
# Firmware-level smoke test for the Metal3 artifact through the public CLI.
# Success proves k0smosctl -> qcow2 clone -> UEFI -> GRUB -> kernel -> EROFS root.
set -euo pipefail

image=${1:-dist/k0smos-metal-x86_64.qcow2}
[ -s "$image" ] || { echo "missing metal image: $image" >&2; exit 1; }

command -v qemu-system-x86_64 >/dev/null || { echo "qemu-system-x86_64 is required" >&2; exit 1; }
ctl=${K0SMOSCTL:-dist/k0smosctl}
[ -x "$ctl" ] || { echo "$ctl is required — run 'make ctl'" >&2; exit 1; }

firmware_args=()
if [ -n "${FIRMWARE:-}" ]; then
  firmware_args=(--firmware "$FIRMWARE")
fi

# Keep the control socket below macOS's 104-byte Unix path limit.
tmp=$(mktemp -d /tmp/k0smos-metal.XXXXXX)
pid=""
cleanup() {
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
base=$(cd "$(dirname "$image")" && pwd)/$(basename "$image")
log=$tmp/console.log
export K0SMOS_STATE_DIR=$tmp/state

"$ctl" machine up --image "$base" --arch amd64 \
  ${firmware_args[@]+"${firmware_args[@]}"} \
  --attach --api-port 0 --name firmware --memory "${MEM:-2048}" \
  --cpus "${CPUS:-2}" >"$log" 2>&1 &
pid=$!

deadline=$((SECONDS + ${BOOT_TIMEOUT:-180}))
marker='supervising [/usr/local/bin/k0s controller --single]'
while ! grep -Fq "$marker" "$log"; do
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "k0smosctl artifact boot exited before reaching k0s supervision" >&2
    tail -200 "$log" >&2
    exit 1
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "k0smosctl artifact boot did not reach k0s supervision in time" >&2
    tail -200 "$log" >&2
    exit 1
  fi
  sleep 1
done

required=(
  'Command line: BOOT_IMAGE=/boot/vmlinuz'
  'no explicit or embedded root; discovering canonical LABEL=k0smos'
  'resolved LABEL=k0smos to /dev/vda2'
  'mounted /dev/vda2 at /newroot read-only, switching root'
  'mounted data volume /dev/vda3 at /var'
  'supervising [/usr/local/bin/k0s controller --single]'
)
for line in "${required[@]}"; do
  if ! grep -Fq "$line" "$log"; then
    echo "metal boot did not reach: $line" >&2
    tail -200 "$log" >&2
    exit 1
  fi
done

"$ctl" machine shutdown --name firmware >>"$log" 2>&1
wait "$pid"
pid=""
for line in 'host requested poweroff' 'reboot: Power down'; do
  grep -Fq "$line" "$log" || {
    echo "metal boot did not shut down cleanly: missing $line" >&2
    tail -200 "$log" >&2
    exit 1
  }
done

echo "verified k0smosctl single-artifact boot through UEFI, GRUB, EROFS, /var, k0s, and clean shutdown"
