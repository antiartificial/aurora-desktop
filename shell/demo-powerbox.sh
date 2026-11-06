#!/usr/bin/env bash
# Powerbox v0 end to end: busd + controllerd --require-powerbox, tickets minted
# by the powerbox, invoke with and without them. Uses the powerbox's --path
# TEST AFFORDANCE (no user gesture) because a script cannot drive /dev/tty;
# the interactive picker is exercised by picker/picker_test.go. Leaves nothing behind.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; ROOT="$(cd "$HERE/.." && pwd)"
W="$(mktemp -d /tmp/aurora-pb-demo.XXXXXX)"
export AURORA_POWERBOX_ALLOW_PATH=1 AURORA_BUS_SOCK="$W/bus.sock" AURORA_POWERBOX_KEY="$W/powerbox.key" AURORA_DOMAINS_DIR="$W/domains"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  for d in "$W"/domains/app-*; do [ -d "$d" ] && "$W/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done
  rm -rf "$W"
}
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
waitfor() { for _ in $(seq 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.05; done; echo "timeout: $2" >&2; return 1; }

(cd "$ROOT/bus" && go build -o "$W/busd" ./cmd/busd) &&
(cd "$ROOT/controller" && go build -o "$W/controllerd" ./cmd/controllerd && go build -o "$W/controller" ./cmd/controller) &&
(cd "$HERE" && go build -o "$W/powerbox" ./cmd/powerbox) || exit 1
(cd "$ROOT/adapter/linux" && go build -o "$W/domain" ./cmd/domain) && DOM=(--domain-bin "$W/domain") || DOM=()

"$W/busd" --data-dir "$W/log" 2>"$W/busd.err" & PIDS+=($!); waitfor "$W/busd.err" "listening on"
mkdir -p "$W/work"; printf '\x89PNG\r\n\x1a\n\0\0\0\rIHDR' > "$W/work/in.png"
"$W/controllerd" --apps "$ROOT/controller/testdata/apps" --require-powerbox "${DOM[@]}" 2>"$W/ctl.err" & PIDS+=($!)
waitfor "$W/ctl.err" "registered service"; grep -E "powerbox|isolation|domain tooling|UNISOLATED" "$W/ctl.err"

APP=org.aurora.demo VERB=copy@v1 IN="$W/work/in.png" OUT="$W/work/out.png"
tick() { "$W/powerbox" --app "${APP_T:-$APP}" --verb "${VERB_T:-$VERB}" --mode "$1" --path "$2" 2>/dev/null | sed 's/.*"ticket":"\([^"]*\)".*/\1/'; }
inv() { "$W/controller" invoke "$APP" "$VERB" --input "$IN" --output "$OUT" "$@" 2>&1; echo "(exit $?)"; }

step "1. bare gesture invoke under --require-powerbox: refused"
inv; test ! -e "$OUT" && echo "output file was not created"

step "2. mint tickets (powerbox --path: TEST AFFORDANCE, no user gesture) and invoke"
T_IN=$(tick ro "$IN"); T_OUT=$(tick rw "$OUT")
echo "input ticket: ${T_IN:0:60}..."
inv --ticket "$T_IN" --output-ticket "$T_OUT" | grep -E '"(source|ok|error|code)"|exit|grants' | head
step "2b. provenance xattr on the output file"
"$W/controller" provenance "$OUT"

step "3. reuse the same tickets: refused"
rm -f "$OUT"; inv --ticket "$T_IN" --output-ticket "$T_OUT"

step "4. a ticket for another path / verb / mode is refused with a specific error"
cp "$IN" "$W/work/other.png"
echo "-- other path (ticket for other.png, request names in.png):";  inv --ticket "$(tick ro "$W/work/other.png")" --output-ticket "$(tick rw "$OUT")"
echo "-- other verb:";  VERB_T=size@v1 inv --ticket "$(VERB_T=size@v1 tick ro "$IN")" --output-ticket "$(tick rw "$OUT")"
echo "-- wrong mode (rw ticket as input):"; inv --ticket "$(tick rw "$IN")" --output-ticket "$(tick rw "$OUT")"
echo "-- tampered:"; inv --ticket "${T_IN%?}X" --output-ticket "$(tick rw "$OUT")"
