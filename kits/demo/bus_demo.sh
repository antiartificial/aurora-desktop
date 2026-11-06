#!/usr/bin/env bash
# Demo: a Haiku BApplication serves a typed verb on the Aurora bus, driven by the Go `bus` CLI.
# usage: bus_demo.sh [build-dir]   (builds kits and busd/bus there if missing; leaves nothing behind)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
BUILD="${1:-$HERE/../build}"
if [ ! -x "$BUILD/haiku_ledger" ] || [ ! -x "$BUILD/busd" ] || [ ! -x "$BUILD/bus" ]; then
  cmake -S "$HERE/.." -B "$BUILD" -G Ninja >/dev/null && cmake --build "$BUILD" >/dev/null
fi
BUILD="$(cd "$BUILD" && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aurora-bus-haiku.XXXXXX")"
export AURORA_BUS_SOCK="$TMP/bus.sock"
export AURORA_KITS_NS="busdemo$$"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && wait "$p" 2>/dev/null || true; done
  rm -f "/dev/shm/aurora-kits-$(id -u)-$AURORA_KITS_NS"
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM
waitfor() { for _ in $(seq 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.05; done
  echo "timed out waiting for '$2' in $1" >&2; exit 1; }
say() { printf '\n== %s\n' "$*"; }

say "start busd"
"$BUILD/busd" --data-dir "$TMP/log" 2>"$TMP/busd.err" & PIDS+=($!)
waitfor "$TMP/busd.err" "listening on"

say "start the Haiku application (BApplication + BHandler)"
"$BUILD/haiku_ledger" >"$TMP/app.out" 2>"$TMP/app.err" & APP=$!; PIDS+=($APP)
waitfor "$TMP/app.out" READY
cat "$TMP/app.out"

say "bus sub 'ledger.**' in the background"
"$BUILD/bus" sub --payload-only 'ledger.**' >"$TMP/sub.out" 2>"$TMP/sub.err" & PIDS+=($!)
waitfor "$TMP/sub.err" subscribed

say "bus send haiku-ledger add@v1 '{\"label\":\"coffee\",\"cents\":350}'"
R1=$("$BUILD/bus" send haiku-ledger add@v1 '{"label":"coffee","cents":350}'); echo "$R1"
say "bus send haiku-ledger add@v1 '{\"label\":\"book\",\"cents\":1499}'"
R2=$("$BUILD/bus" send haiku-ledger add@v1 '{"label":"book","cents":1499}'); echo "$R2"
say "bus send haiku-ledger list@v1 '{}'"
R3=$("$BUILD/bus" send haiku-ledger list@v1 '{}'); echo "$R3"

say "errors come back as bus errors, not hangs"
E1=$("$BUILD/bus" send haiku-ledger add@v1 '{"label":"bad","cents":"lots"}' 2>&1 || true); echo "$E1"
E2=$("$BUILD/bus" send haiku-ledger frobnicate@v1 '{}' 2>&1 || true); echo "$E2"
E3=$("$BUILD/bus" send haiku-ledger add@v1 '{"label":"x","cents":[1,"a"]}' 2>&1 || true); echo "$E3"

say "what the Haiku handler printed"
cat "$TMP/app.out"

say "what 'bus sub' received (events the app published)"
waitfor "$TMP/sub.out" '"book"'
cat "$TMP/sub.out"

say "quit the app"
kill -TERM "$APP"; RC=0; wait "$APP" || RC=$?; echo "haiku_ledger exit status $RC"
PIDS=("${PIDS[@]/$APP}")

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
[ "$RC" = 0 ] || fail "app exit status $RC"
case "$R1" in '{"total":350,'*) ;; *) fail "add reply: $R1";; esac
case "$R2" in '{"total":1849,'*) ;; *) fail "add reply: $R2";; esac
[ "$R3" = '{"entries":[{"label":"coffee","cents":350},{"label":"book","cents":1499}],"total":1849}' ] || fail "list reply: $R3"
case "$E1" in *not_understood*) ;; *) fail "E1: $E1";; esac
case "$E2" in *not_understood*) ;; *) fail "E2: $E2";; esac
case "$E3" in *bad_payload*) ;; *) fail "E3: $E3";; esac
[ "$(grep -c . "$TMP/sub.out")" = 2 ] || fail "expected 2 events"
echo; echo "DEMO OK"
