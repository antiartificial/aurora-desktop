#!/usr/bin/env bash
# Aurora bus demo: busd, a service, a subscriber, events, log, replay check.
# Leaves no process or temp file behind.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aurora-bus-demo.XXXXXX")"
export AURORA_BUS_SOCK="$TMP/bus.sock"
PIDS=()

cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && wait "$p" 2>/dev/null || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

step() { printf '\n== %s\n' "$*"; }
waitfor() { # waitfor <file> <text>
  for _ in $(seq 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.05; done
  echo "timed out waiting for '$2' in $1" >&2; exit 1
}

step "build busd and bus"
(cd "$HERE" && go build -o "$TMP/busd" ./cmd/busd && go build -o "$TMP/bus" ./cmd/bus)
BUS="$TMP/bus"

step "start busd (socket $AURORA_BUS_SOCK)"
"$TMP/busd" --data-dir "$TMP/log" 2>"$TMP/busd.err" &
PIDS+=($!)
waitfor "$TMP/busd.err" "listening on"
cat "$TMP/busd.err"

step "client 1: 'bus serve greeter' answers greet@v1 requests with a shell command"
cat > "$TMP/greeter.sh" <<'SH'
#!/bin/sh
# payload JSON on stdin -> reply JSON on stdout
name=$(sed -n 's/.*"name" *: *"\([^"]*\)".*/\1/p')
printf '{"greeting":"hello, %s","asked_by":"%s"}' "${name:-stranger}" "$BUS_FROM"
SH
chmod +x "$TMP/greeter.sh"
"$BUS" serve greeter "$TMP/greeter.sh" 2>"$TMP/serve.err" &
PIDS+=($!)
waitfor "$TMP/serve.err" "serving greeter"

step "client 2: 'bus sub demo.*' streams events in the background"
"$BUS" sub 'demo.*' >"$TMP/sub.out" 2>"$TMP/sub.err" &
PIDS+=($!)
waitfor "$TMP/sub.err" "subscribed"

step "typed request/response:  bus send greeter greet@v1 '{\"name\":\"aurora\"}'"
"$BUS" send greeter greet@v1 '{"name":"aurora"}'

step "an unknown service gives a typed error and a non-zero exit"
"$BUS" send nobody ping@v1 || echo "(exit code $?)"

step "publish events (one ephemeral: delivered, never logged)"
"$BUS" pub demo.opened '{"id":1}' --qos user-interactive
"$BUS" pub demo.closed '{"id":1}'
"$BUS" pub --ephemeral demo.pointer '{"x":10,"y":20}'
"$BUS" pub other.topic '{"note":"not matched by demo.*"}'

waitfor "$TMP/sub.out" "demo.pointer"
step "what 'bus sub demo.*' received (payload and sender; sender is from SO_PEERCRED)"
sed -e 's/,"ephemeral":true//' -n -e 's/.*"from":"\([^"]*\)".*"topic":"\([^"]*\)".*"payload":\(.*\)}$/  \2  from=\1  payload=\3/p' "$TMP/sub.out"

step "bus inspect"
"$BUS" inspect

step "bus log (note: demo.pointer is absent; the greeter request/response are present)"
"$BUS" log --since 5m

step "bus log --topic 'demo.*'"
"$BUS" log --topic 'demo.*'

step "bus replay --check: re-deliver the session to a FRESH daemon, verify invariants"
"$BUS" replay --check "$TMP/log"

step "stop clients and daemon"
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
for p in "${PIDS[@]}"; do wait "$p" 2>/dev/null || true; done
cat "$TMP/busd.err" | tail -2
PIDS=()
echo
echo "done: no processes left behind."
