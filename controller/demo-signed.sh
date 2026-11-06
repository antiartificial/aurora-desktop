#!/usr/bin/env bash
# End to end on the real stack (busd, headless compositor, controllerd, bwrap domains,
# the in-domain helper): the compositor's reply is signed, and the bus route is attested.
#   1. a signed, accepted approval: the real reply with its signature, and provenance
#   2. a same-uid squatter on the trusted socket answers `allow` without the key: refused,
#      nothing runs. Then the RESIDUAL: a squatter that reads the key file is accepted.
#   3. the bus route (no compositor): approval.requested carries the bus-attested domain
set -uo pipefail
cd "$(dirname "$0")"
CTL=$(pwd); ROOT=$(cd .. && pwd)
W=$(mktemp -d /tmp/ctl-signed.XXXXXX); PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  for d in "$W"/domains/*; do [ -d "$d" ] && "$W/bin/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done; rm -rf "$W"; }
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
export AURORA_BUS_SOCK=$W/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$W/run
mkdir -p "$W/bin" "$W/pics" "$W/run" "$W/home/Pictures"; chmod 700 "$W/run"
KEY=$W/run/aurora/approval.key; SOCK=$W/run/aurora-trusted.sock

step "build"
export CGO_ENABLED=0
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || exit 1
(go build -o "$W/bin/aurora-preflight" ./cmd/aurora-preflight) || exit 1
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || exit 1
go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller || exit 1
unset CGO_ENABLED
(cd "$ROOT/compositor" && cargo build --release --examples 2>&1 | tail -1) || exit 1
COMP=$ROOT/compositor/target/release/aurora-compositor
HOME=$W/home; export HOME
printf '\x89PNG\r\n\x1a\n\0\0\0\rIHDR' > "$W/pics/in.png"

"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
"$COMP" --headless=1280x800 --bus-socket "$AURORA_BUS_SOCK" --test-input "$W/run/input.sock" --dump-overlay "$W/overlay.png" >"$W/comp.out" 2>"$W/comp.err" & COMPPID=$!; PIDS+=($COMPPID)
for _ in $(seq 100); do grep -q 'socket wayland' "$W/comp.out" && [ -S "$W/run/input.sock" ] && break; sleep 0.1; done
grep -E 'signed' "$W/comp.err" | sed 's/^/   compositor: /'
WL=$(sed -n 's/^aurora-compositor: socket //p' "$W/comp.out")
WAYLAND_DISPLAY=$WL "$ROOT/compositor/target/release/examples/testclient" >"$W/client.log" 2>&1 & PIDS+=($!)
for _ in $(seq 100); do grep -q enter "$W/client.log" && break; sleep 0.1; done
startctl() { "$W/bin/controllerd" --apps "$ROOT/apps,$CTL/testdata/apps" --domain-bin "$W/bin/domain" --approval-timeout 30s "$@" >"$W/ctl.log" 2>&1 & CTLPID=$!; PIDS+=($CTLPID)
  for _ in $(seq 50); do grep -q 'registered service' "$W/ctl.log" && break; sleep 0.1; done; }
startctl; grep -E 'signed with' "$W/ctl.log" | sed 's/^[^ ]* [^ ]* /   controllerd: /'
echo "   key file: $(stat -c '%a %U %s bytes' "$KEY")"
C="$W/bin/controller"; DEL=(invoke org.aurora.demo delete@v1 --input "$W/pics/in.png")
key() { printf 'key %s\n' "$1" | socat - "UNIX-CONNECT:$W/run/input.sock"; }
await_overlay() { for _ in $(seq 100); do [ -f "$W/overlay.png" ] && break; sleep 0.1; done; sleep 0.4; }
apprblock() { $C provenance "$1" | grep -A14 '"approval"'; }
$C "${DEL[@]}" --output "$W/pics/warm.png" >/dev/null 2>&1 & WP=$!; await_overlay; key a; wait $WP  # creates the app's domain

step "1. SIGNED AND ACCEPTED: a destructive invoke, 'a' pressed at the compositor"
rm -f "$W/overlay.png"; $C "${DEL[@]}" --output "$W/pics/one.png" >"$W/one.out" 2>&1 & DP=$!
await_overlay; key a; wait $DP; echo "invoke exit $?"
echo "-- controllerd:"; grep -E 'allowed at the compositor' "$W/ctl.log" | tail -1 | sed 's/^[^ ]* [^ ]* /   /'
echo "-- provenance (approval block):"; apprblock "$W/pics/one.png"
echo "-- the raw reply the compositor sends (a direct request, 'a' pressed), with its signature:"
rm -f "$W/overlay.png"
python3 - "$SOCK" "$W/overlay.png" "$W/run/input.sock" <<'PY'
import socket, sys, json, os, time
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
s.sendall((json.dumps({"v":1,"op":"approve","request_id":"demo-raw","app":"org.aurora.demo","domain":"x","verb":"delete@v1","effect":"destructive","grants":[],"args":{},"timeout_ms":30000})+"\n").encode())
while not os.path.exists(sys.argv[2]): time.sleep(0.1)
time.sleep(0.3)
k = socket.socket(socket.AF_UNIX); k.connect(sys.argv[3]); k.sendall(b"key a\n"); k.close()
print("   " + s.makefile().readline().strip())
PY

step "2. SQUATTER: a same-uid process owns the trusted socket path and answers allow, with no key"
kill $COMPPID; wait $COMPPID 2>/dev/null; rm -f "$SOCK"
cat > "$W/squat.py" <<'PY'
import socket, sys, json, os, hmac, hashlib
path, keyfile = sys.argv[1], sys.argv[2]
s = socket.socket(socket.AF_UNIX); s.bind(path); s.listen(4)
def lp(f): b = f.encode(); return len(b).to_bytes(4, "big") + b
while True:
    c, _ = s.accept(); f = c.makefile()
    req = json.loads(f.readline()); print("squatter got:", req["verb"], req["request_id"][:8], flush=True)
    rep = {"v":1,"request_id":req["request_id"],"decision":"allow","key":"a","reason":"key","domain_verified":"true","domain_verify_detail":"squatter says so","domain_source":"bus","domain_identity":req.get("domain_identity",""),"domain_mismatch":"false"}
    if keyfile != "-":
        k = open(keyfile, "rb").read()
        m = b"aurora-approval-reply-v1\0" + b"".join(lp(rep[x]) for x in ["request_id","decision","key","reason","domain_verified","domain_verify_detail","domain_source","domain_identity","domain_mismatch"])
        rep["sig"] = hmac.new(k, m, hashlib.sha256).hexdigest()
    c.sendall((json.dumps(rep) + "\n").encode()); c.close()
PY
python3 "$W/squat.py" "$SOCK" - >"$W/squat.out" 2>&1 & SQ=$!; PIDS+=($SQ)
for _ in $(seq 30); do [ -S "$SOCK" ] && break; sleep 0.1; done
"$W/bin/bus" sub approval.requested >"$W/appr.log" 2>&1 & PIDS+=($!)
rm -f "$W/pics/squat.png"; $C "${DEL[@]}" --output "$W/pics/squat.png" >"$W/squat.invoke" 2>&1 & DP=$!
id() { for _ in $(seq 80); do i=$(grep -a -o '"request_id":"[a-f0-9]*"' "${LOGF:-$W/appr.log}" | tail -n +"$1" | head -1 | cut -d'"' -f4); [ -n "$i" ] && { echo "$i"; return; }; sleep 0.1; done; }
ID=$(id 1); echo "-- the squatter received the request and answered allow; the controller:"
grep -E 'REFUSING|squatter' "$W/ctl.log" "$W/squat.out" | sed 's/^[^ ]* [^ ]* /   /' | cut -c1-420
kill -0 $DP 2>/dev/null && echo "-- the invoke is still blocked, now on the bus route (not run on the squatter's allow)"
[ -e "$W/pics/squat.png" ] && echo "FAIL: output exists" || echo "-- output file exists? no: nothing ran"
$C decide "$ID" deny >/dev/null; wait $DP; echo "-- invoke (after a deny on the bus route): $(grep -o 'approval_denied[^"]*' "$W/squat.invoke" | head -1)"
[ -e "$W/pics/squat.png" ] && echo "FAIL: output exists" || echo "-- output file still absent"
echo
echo "-- RESIDUAL: a same-uid squatter that READS the key file signs correctly and is accepted:"
kill $SQ; wait $SQ 2>/dev/null; rm -f "$SOCK"
python3 "$W/squat.py" "$SOCK" "$KEY" >"$W/squat2.out" 2>&1 & SQ=$!; PIDS+=($SQ)
for _ in $(seq 30); do [ -S "$SOCK" ] && break; sleep 0.1; done
$C "${DEL[@]}" --output "$W/pics/forged.png" >"$W/forged.out" 2>&1; echo "invoke exit $?; output exists: $([ -e "$W/pics/forged.png" ] && echo yes || echo no)"
apprblock "$W/pics/forged.png" | grep -E 'route|decision|by|key|detail'
kill $SQ 2>/dev/null; rm -f "$SOCK"

step "3. BUS ROUTE, ATTESTED: no compositor approval; approval.requested carries the identity busd stamped"
kill $CTLPID; wait $CTLPID 2>/dev/null
startctl --no-compositor-approval
"$W/bin/bus" sub approval.requested >"$W/appr3.log" 2>&1 & PIDS+=($!); sleep 0.5
rm -f "$W/pics/bus.png"
$C "${DEL[@]}" --output "$W/pics/bus.png" >"$W/bus.out" 2>&1 & DP=$!
ID=$(LOGF=$W/appr3.log id 1); echo "-- approval.requested payload (what a responder displays):"
grep -a -o '"domain[a-z_]*":[^,}]*' "$W/appr3.log" | head -6 | sed 's/^/   /'
echo "-- what busd stamped for the helper (from= is not typed by anyone):"
"$W/bin/bus" log --topic 'approval.preflight' --json 2>/dev/null | tail -1 | python3 -c 'import sys,json; m=json.loads(sys.stdin.read()); print("   from =", m["from"])'
$C decide "$ID" allow >/dev/null; wait $DP; echo "invoke exit $?"
echo "-- provenance (approval block):"; apprblock "$W/pics/bus.png"
echo "-- controllerd:"; grep -E 'approval.*(pre-flight|allowed by)' "$W/ctl.log" | tail -2 | sed 's/^[^ ]* [^ ]* /   /'
