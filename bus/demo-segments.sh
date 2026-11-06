#!/usr/bin/env bash
# Aurora bus segment demo: a large file moves over the bus as a sealed memfd
# passed with SCM_RIGHTS, and never appears in the log.
# Leaves no process or temp file behind.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aurora-bus-seg.XXXXXX")"
export AURORA_BUS_SOCK="$TMP/bus.sock"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && wait "$p" 2>/dev/null || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM
step() { printf '\n== %s\n' "$*"; }
waitfor() { for _ in $(seq 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.05; done; echo "timed out: $2" >&2; exit 1; }

step "build"
(cd "$HERE" && go build -o "$TMP/busd" ./cmd/busd && go build -o "$TMP/bus" ./cmd/bus)
BUS="$TMP/bus"
"$TMP/busd" --data-dir "$TMP/log" 2>"$TMP/busd.err" &
PIDS+=($!)
waitfor "$TMP/busd.err" "listening on"

step "make a 64 MiB file with a marker in the middle"
head -c 33554432 /dev/urandom > "$TMP/big.bin"
printf 'AURORA-DEMO-MARKER-5d1e' >> "$TMP/big.bin"
head -c 33554432 /dev/urandom >> "$TMP/big.bin"
ls -l "$TMP/big.bin" | awk '{print $5, "bytes"}'
echo "sender sha256: $(sha256sum < "$TMP/big.bin" | cut -d' ' -f1)"

step "service 'hasher': gets the segment as read-only fd 3 and hashes it"
export HANDLER_NOTE="$TMP/handler.note"
cat > "$TMP/hasher.sh" <<'SH'
#!/bin/sh
echo "handler sees BUS_SEGMENTS=$BUS_SEGMENTS fds=$BUS_SEGMENT_FDS" > "$HANDLER_NOTE"
printf '{"handler_sha256":"%s"}' "$(sha256sum <&3 | cut -d' ' -f1)"
SH
chmod +x "$TMP/hasher.sh"
"$BUS" serve hasher "$TMP/hasher.sh" 2>"$TMP/serve.err" &
PIDS+=($!)
waitfor "$TMP/serve.err" "serving hasher"

step "bus send --segment big.bin   (request line stays tiny; the fd carries the data)"
"$BUS" send hasher hash@v1 --segment "$TMP/big.bin=application/octet-stream"
cut -c1-220 "$TMP/handler.note"

step "event path: bus pub --segment, bus sub --segment-dir saves what it receives"
mkdir "$TMP/recv"
"$BUS" sub 'files.*' --segment-dir "$TMP/recv" >"$TMP/sub.out" 2>"$TMP/sub.err" &
PIDS+=($!)
waitfor "$TMP/sub.err" "subscribed"
"$BUS" pub files.new '{"note":"here is a file"}' --segment "$TMP/big.bin"
waitfor "$TMP/sub.out" "files.new"
sleep 0.5
echo "subscriber's copy sha256: $(sha256sum < "$TMP/recv/big.bin" | cut -d' ' -f1)"
echo "event line seen by subscriber (descriptor only):"
cut -c1-400 "$TMP/sub.out"

step "bus inspect (segments per client)"
"$BUS" inspect | head -12

step "the log: descriptors and hashes, never contents"
echo "log size on disk: $(cat "$TMP"/log/seg-*.jsonl | wc -c) bytes (for 2 x 64 MiB moved)"
if grep -q "AURORA-DEMO-MARKER-5d1e" "$TMP"/log/seg-*.jsonl; then echo "FAIL: marker found in log"; exit 1; else echo "marker absent from log: OK"; fi
"$BUS" log --since 5m | cut -c1-420

step "bus replay --check (descriptors and hashes compared; contents are not replayable)"
"$BUS" replay --check "$TMP/log"

step "stop"
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
for p in "${PIDS[@]}"; do wait "$p" 2>/dev/null || true; done
PIDS=()
echo "done: no processes left behind."
