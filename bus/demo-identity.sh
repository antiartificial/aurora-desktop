#!/usr/bin/env bash
# Aurora bus identity demo: two REAL domains (bwrap, via adapter/linux), each
# with its own bus socket; distinct sender identities; a minted handle, a
# transfer, a forgery; and the documented residual weakness. Leaves nothing behind.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aurora-bus-id.XXXXXX")"
export AURORA_DOMAINS_DIR="$TMP/domains"
export XDG_RUNTIME_DIR="$TMP/run"; mkdir -p "$XDG_RUNTIME_DIR"
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
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }

step "build busd, bus, domain (static, so the binary runs inside a domain)"
export CGO_ENABLED=0
(cd "$HERE" && go build -o "$TMP/busd" ./cmd/busd && go build -o "$TMP/bus" ./cmd/bus)
(cd "$HERE/../adapter/linux" && go build -o "$TMP/domain" ./cmd/domain)
BUS="$TMP/bus"; DOMAIN="$TMP/domain"

step "two domains; each is given the bus binary (read-only) and nothing else"
for d in alpha beta; do "$DOMAIN" create $d >/dev/null; "$DOMAIN" grant $d "$BUS" --ro >/dev/null; done
IDA=$("$DOMAIN" exec-id alpha); IDB=$("$DOMAIN" exec-id beta)
SA=$("$DOMAIN" sockpath alpha); SB=$("$DOMAIN" sockpath beta)
echo "alpha: $IDA  socket $SA"
echo "beta:  $IDB  socket $SB"

step "busd: session socket plus one socket per domain"
"$TMP/busd" --data-dir "$TMP/log" --domain "$IDA=$SA" --domain "$IDB=$SB" 2>"$TMP/busd.err" &
PIDS+=($!)
waitfor "$TMP/busd.err" "listening on"
cat "$TMP/busd.err"

# A host-side service that reports who asked and answers any request.
"$BUS" serve vault 'printf "{\"served\":true,\"asked_by\":\"%s\"}" "$BUS_FROM"' 2>"$TMP/serve.err" &
PIDS+=($!)
waitfor "$TMP/serve.err" "serving vault"
inA() { local c=$1; shift; "$DOMAIN" run alpha -- "$BUS" "$c" --socket /run/aurora/bus.sock "$@"; }
inB() { local c=$1; shift; "$DOMAIN" run beta -- "$BUS" "$c" --socket /run/aurora/bus.sock "$@"; }

step "1. distinct identities: the vault sees which domain called (from = the socket it arrived on)"
echo "alpha -> $(inA send vault who@v1)"
echo "beta  -> $(inB send vault who@v1)"
echo "host session socket -> $("$BUS" send vault who@v1)   (uid:comm, not a domain)"

step "2. alpha mints a handle for resource photo:42"
M=$(inA send bus bus.mint@v1 '{"resource":"photo:42"}')
echo "$M" | sed 's/"handle":"cap_[0-9a-f]*"/"handle":"cap_<64 hex, redacted>"/'
TOK=$(echo "$M" | field handle)

step "3. forgery: beta invents a handle; beta presents alpha's real token without holding it"
inB send vault use@v1 --cap cap_0000000000000000000000000000000000000000000000000000000000000000 || echo "(rejected, exit $?)"
inB send vault use@v1 --cap "$TOK" || echo "(rejected, exit $?)"

step "4. the holder presents it: accepted"
inA send vault use@v1 --cap "$TOK"

step "5. transfer alpha -> beta (beta must be connected; the daemon pushes the token to it)"
inB sub bus.cap.granted >"$TMP/beta.grants" 2>"$TMP/beta.err" &
PIDS+=($!)
waitfor "$TMP/beta.err" "subscribed"
inA send bus bus.transfer@v1 "{\"handle\":\"$TOK\",\"to\":\"$IDB\"}"
waitfor "$TMP/beta.grants" "bus.cap.granted"
BTOK=$(sed -n 's/.*"caps":\["\([^"]*\)"\].*/\1/p' "$TMP/beta.grants" | head -1)
[ "$BTOK" = "$TOK" ] && echo "beta received the handle from the daemon"
echo "beta presents:  $(inB send vault use@v1 --cap "$BTOK")"
echo "alpha presents (no longer holds it):"
inA send vault use@v1 --cap "$TOK" || echo "(rejected, exit $?)"

step "6. bus inspect: outstanding handles (id, resource, holder, expiry; never the token)"
"$BUS" inspect | sed -n '/^domain sockets/,$p'

step "7. the log has the handle's identity (cap_ids hash) and not the token"
"$BUS" log --since 5m | grep -c "caps=cap:" | sed 's/^/messages carrying a cap id: /'
if grep -rqF "$TOK" "$TMP/log"; then echo "!! TOKEN IN LOG"; exit 1; else echo "token absent from log bytes"; fi

step "8. residual weakness, shown honestly: a HOST process (same uid) that reaches alpha's socket IS alpha"
python3 - "$SA" "$IDB" <<'PY'
import socket, sys
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
s.sendall(b'{"kind":"event","topic":"demo.spoof","from":"' + sys.argv[2].encode() + b'"}\n')
PY
sleep 0.2
"$BUS" log --topic demo.spoof | sed 's/^/log: /'
echo "(claimed from=$IDB; recorded as alpha, because the socket is the identity)"

step "stop"
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
for p in "${PIDS[@]}"; do wait "$p" 2>/dev/null || true; done
PIDS=()
echo "done: no processes left behind."
