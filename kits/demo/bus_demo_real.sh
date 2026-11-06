#!/usr/bin/env bash
# Demo: haiku_ledger_real (Haiku's own BMessage/BHandler/BLooper, no BApplication) serves bus verbs.
# usage: bus_demo_real.sh <busd> <bus> <haiku_ledger_real>
set -euo pipefail
BUSD="$1"; BUS="$2"; APPBIN="$3"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aurora-bus-real.XXXXXX")"
export AURORA_BUS_SOCK="$TMP/bus.sock"
export AURORA_KITS_NS="busreal$$"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && wait "$p" 2>/dev/null || true; done
  rm -f "/dev/shm/aurora-kits-$(id -u)-$AURORA_KITS_NS"; rm -rf "$TMP"
}
trap cleanup EXIT INT TERM
waitfor() { for _ in $(seq 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.05; done
  echo "timed out waiting for '$2' in $1" >&2; exit 1; }
fail() { echo "DEMO FAILED: $*" >&2; exit 1; }

"$BUSD" --data-dir "$TMP/log" 2>"$TMP/busd.err" & PIDS+=($!)
waitfor "$TMP/busd.err" "listening on"
"$APPBIN" >"$TMP/app.out" 2>"$TMP/app.err" & APP=$!; PIDS+=($APP)
waitfor "$TMP/app.out" READY
"$BUS" sub --payload-only 'ledger.**' >"$TMP/sub.out" 2>"$TMP/sub.err" & PIDS+=($!)
waitfor "$TMP/sub.err" subscribed

echo "\$ bus send haiku-ledger-real add@v1 '{\"label\":\"coffee\",\"cents\":350}'"
R1=$("$BUS" send haiku-ledger-real add@v1 '{"label":"coffee","cents":350}'); echo "$R1"
echo "\$ bus send haiku-ledger-real add@v1 '{\"label\":\"book\",\"cents\":1499}'"
R2=$("$BUS" send haiku-ledger-real add@v1 '{"label":"book","cents":1499}'); echo "$R2"
echo "\$ bus send haiku-ledger-real list@v1 '{}'"
R3=$("$BUS" send haiku-ledger-real list@v1 '{}'); echo "$R3"
echo "\$ bad payload / unknown verb"
E1=$("$BUS" send haiku-ledger-real add@v1 '{"label":"bad","cents":"lots"}' 2>&1 || true); echo "$E1"
E2=$("$BUS" send haiku-ledger-real frobnicate@v1 '{}' 2>&1 || true); echo "$E2"
waitfor "$TMP/sub.out" '"book"'
kill -TERM "$APP"; RC=0; wait "$APP" || RC=$?; PIDS=("${PIDS[@]/$APP}")

[ "$RC" = 0 ] || fail "app exit status $RC"
case "$R1" in '{"total":350,'*) ;; *) fail "add reply: $R1";; esac
case "$R2" in '{"total":1849,'*) ;; *) fail "add reply: $R2";; esac
[ "$R3" = '{"entries":[{"label":"coffee","cents":350},{"label":"book","cents":1499}],"total":1849}' ] || fail "list reply: $R3"
case "$E1" in *not_understood*) ;; *) fail "E1: $E1";; esac
case "$E2" in *no_reply*|*not_understood*) ;; *) fail "E2: $E2";; esac
[ "$(grep -c . "$TMP/sub.out")" = 2 ] || fail "expected 2 events"
echo "DEMO REAL OK (exit $RC)"
