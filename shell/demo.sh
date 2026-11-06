#!/usr/bin/env bash
# Aurora launcher demo. Uses a real busd and controllerd when they can be
# found or built; otherwise falls back to a STUB registry and says so loudly.
# Leaves no process or temp file behind.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aurora-shell-demo.XXXXXX")"
export AURORA_BUS_SOCK="$TMP/bus.sock"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && wait "$p" 2>/dev/null || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM
step() { printf '\n== %s\n' "$*"; }
waitfor() { for _ in $(seq 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.05; done
            echo "timed out waiting for '$2' in $1" >&2; return 1; }

step "build aurora, busd, bus"
(cd "$HERE" && GOWORK=off go build -o "$TMP/aurora" ./cmd/aurora)
(cd "$ROOT/bus" && go build -o "$TMP/busd" ./cmd/busd && go build -o "$TMP/bus" ./cmd/bus)

step "start busd"
"$TMP/busd" --data-dir "$TMP/log" 2>"$TMP/busd.err" &
PIDS+=($!)
waitfor "$TMP/busd.err" "listening on"

# --- find a real controllerd -------------------------------------------------
CTL=""
if [ -n "${DEMO_NO_CONTROLLER:-}" ]; then echo "(DEMO_NO_CONTROLLER set: pretending controllerd is missing)"
elif command -v controllerd >/dev/null; then CTL="$(command -v controllerd)"
elif [ -x "$ROOT/controller/out/controllerd" ]; then CTL="$ROOT/controller/out/controllerd"
elif (cd "$ROOT/controller" 2>/dev/null && go build -o "$TMP/controllerd" ./cmd/controllerd 2>/dev/null); then CTL="$TMP/controllerd"
fi

# Sample image: a 1x1 PNG (the demo fixture app copies it; it does not resize).
mkdir -p "$TMP/work"
printf 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==' | base64 -d > "$TMP/work/sample.png"

REAL=0
if [ -n "$CTL" ] && [ -d "$ROOT/controller/testdata/apps" ]; then
  step "start controllerd ($CTL) with the controller's fixture app (org.aurora.demo, not Lumen)"
  cp -r "$ROOT/controller/testdata/apps" "$TMP/apps"
  "$CTL" --apps "$TMP/apps" 2>"$TMP/ctl.err" &
  PIDS+=($!)
  if waitfor "$TMP/ctl.err" 'registered service'; then REAL=1; head -5 "$TMP/ctl.err"; VERB=copy; ARGS=(--arg tag=demo); fi
fi

if [ "$REAL" = 0 ]; then
  cat <<'MSG'

!! ---------------------------------------------------------------------
!! CONTROLLER UNAVAILABLE: controllerd was not found or did not start.
!! Everything below runs against a STUB registry (a shell script served
!! with `bus serve registry`), NOT the real controller. It proves the
!! launcher's wire handling only, not the controller's behaviour.
!! ---------------------------------------------------------------------
MSG
  cat > "$TMP/stub.sh" <<'SH'
#!/bin/sh
payload=$(cat)
field() { printf '%s' "$payload" | sed -n "s/.*\"$1\" *: *\"\([^\"]*\)\".*/\1/p"; }
case "$BUS_VERB" in
  registry.apps@v1) echo '{"apps":[{"id":"stub.copier","name":"Stub","version":"0","verbs":["copy@v1"]}]}';;
  registry.verbs@v1) echo '{"verbs":[{"name":"copy@v1","app":"stub.copier","effect":"write","accepts":["image/png"],"summary":"Copy an image (stub)"}]}';;
  registry.describe@v1) echo '{"app":"stub.copier","verb":"copy@v1","effect":"write","input":{"tag":"string?"},"output":{}}';;
  registry.invoke@v1)
    in=$(field input); out=$(field output); cp "$in" "$out" || exit 1
    printf '{"ok":true,"output":"%s","provenance":{"file":"%s","app":"stub.copier","verb":"copy@v1","inputs":["%s"]}}' "$out" "$out" "$in"
    "$BUS_BIN" pub provenance.created "{\"file\":\"$out\",\"app\":\"stub.copier\",\"verb\":\"copy@v1\",\"inputs\":[\"$in\"]}" >/dev/null;;
  *) exit 1;;
esac
SH
  chmod +x "$TMP/stub.sh"
  BUS_BIN="$TMP/bus" "$TMP/bus" serve registry "$TMP/stub.sh" 2>"$TMP/stub.err" &
  PIDS+=($!)
  sleep 0.5
  VERB=copy; ARGS=(--arg tag=demo)
fi

A="$TMP/aurora"
step "aurora apps"
"$A" apps
step "aurora verbs --accepts image/png"
"$A" verbs --accepts image/png

step "aurora watch --count 1 (background)"
"$A" watch --count 1 >"$TMP/watch.out" 2>&1 &
WPID=$!
waitfor "$TMP/watch.out" "watching"
sleep 0.3

step "aurora open sample.png --verb $VERB ${ARGS[*]}   (non-interactive)"
"$A" open "$TMP/work/sample.png" --verb "$VERB" "${ARGS[@]}"
echo "-- output file:"; ls -l "$TMP/work"

step "provenance.created as seen by aurora watch"
wait "$WPID" || true
cat "$TMP/watch.out"

step "failure paths: unknown verb, then no registry"
"$A" open "$TMP/work/sample.png" --verb nope || echo "(exit $?)"
AURORA_BUS_SOCK="$TMP/absent.sock" "$A" verbs || echo "(exit $?)"

if [ "$REAL" = 0 ]; then echo; echo "REMINDER: this run used a STUB registry; the real controller was unavailable."
else echo; echo "This run used the REAL controllerd with the controller's fixture app."; fi
echo "done; cleaning up"
