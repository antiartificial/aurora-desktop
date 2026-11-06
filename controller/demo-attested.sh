#!/usr/bin/env bash
# End-to-end: bus-ATTESTED domain identity on the trusted path, with a real busd,
# controllerd, headless compositor, bwrap domains and the real in-domain helper:
#   1. a real invoke: controllerd runs aurora-preflight inside the app's domain;
#      the prompt shows the identity busd stamped (BUS-ATTESTED); provenance says so
#   2. the cost of that pre-flight, measured
#   3. DISAGREEMENT: the helper runs inside domain B for a request id while a
#      forged request naming domain A uses that id: the compositor shows B, flagged
#   4. a same-uid forger naming a real domain with no event: shown as a claim
# (Older text of the verification demo, kept for reference:)
#   1. an approval for a domain busd does NOT know (fresh app, no domain yet): UNVERIFIED
#   2. an approval for the same app once its domain exists and the controller
#      registered it with busd, cgroup included: VERIFIED
#   3. a forged request straight at the compositor socket naming a domain
#      busd has never heard of: UNVERIFIED, prompt still drawn
#   4. a compositor socket whose peer fails the controller's credential check
#      (wrong uid; then a listener running INSIDE a real domain): refused,
#      bus route used
# Keystrokes come from the compositor's headless --test-input affordance.
# Usage: demo-verified.sh [DIR to keep overlay PNGs in]
set -uo pipefail
cd "$(dirname "$0")"
CTL=$(pwd); ROOT=$(cd .. && pwd); KEEP=${1:-}
W=$(mktemp -d /tmp/ctl-verified.XXXXXX); PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  for d in "$W"/domains/*; do [ -d "$d" ] && "$W/bin/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done; rm -rf "$W"; }
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
export AURORA_BUS_SOCK=$W/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$W/run
mkdir -p "$W/bin" "$W/pics" "$W/run" "$W/home/Pictures"; chmod 700 "$W/run"

step "build"
export CGO_ENABLED=0
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || exit 1
(CGO_ENABLED=0 go build -o "$W/bin/aurora-preflight" ./cmd/aurora-preflight) || exit 1
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || { echo "cannot build domain CLI"; exit 1; }
go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller || exit 1
unset CGO_ENABLED
(cd "$ROOT/compositor" && cargo build --release --examples 2>&1 | tail -1) || exit 1
COMP=$ROOT/compositor/target/release/aurora-compositor
HOME=$W/home; export HOME
printf '\x89PNG\r\n\x1a\n\0\0\0\rIHDR' > "$W/pics/in.png"

step "start busd, the headless compositor (told where busd is) with a focused client, and controllerd"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
"$COMP" --headless=1280x800 --bus-socket "$AURORA_BUS_SOCK" --test-input "$W/run/input.sock" --dump-overlay "$W/overlay.png" >"$W/comp.out" 2>"$W/comp.err" & COMPPID=$!; PIDS+=($COMPPID)
for _ in $(seq 100); do grep -q 'socket wayland' "$W/comp.out" && [ -S "$W/run/input.sock" ] && break; sleep 0.1; done
grep -E 'domain claims|WARNING' "$W/comp.err" "$W/comp.out" | sed 's/^[^:]*://'
WL=$(sed -n 's/^aurora-compositor: socket //p' "$W/comp.out")
WAYLAND_DISPLAY=$WL "$ROOT/compositor/target/release/examples/testclient" >"$W/client.log" 2>&1 & PIDS+=($!)
for _ in $(seq 100); do grep -q enter "$W/client.log" && break; sleep 0.1; done
"$W/bin/controllerd" --apps "$ROOT/apps,$CTL/testdata/apps" --domain-bin "$W/bin/domain" --approval-timeout 30s >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
grep -E 'compositor' "$W/controllerd.log"
C="$W/bin/controller"
key() { printf 'key %s\n' "$1" | socat - "UNIX-CONNECT:$W/run/input.sock"; }
await_overlay() { for _ in $(seq 100); do [ -f "$W/overlay.png" ] && break; sleep 0.1; done; sleep 0.4; }
DEL=(invoke org.aurora.demo delete@v1 --input "$W/pics/in.png")
show() { grep -E 'domain_verified|domain_verify_detail|"route"|"key"|"domain"' "$1" | sed 's/^ */    /'; }


show() { grep -E "$2" "$1" | sed 's/^ */    /'; }
step "1. a real destructive invoke: controllerd runs the helper inside the app's domain, then asks the compositor"
rm -f "$W/overlay.png"; $C "${DEL[@]}" --output "$W/pics/one.png" >"$W/one.out" 2>&1 & DP=$!
await_overlay; [ -n "$KEEP" ] && cp "$W/overlay.png" "$KEEP/attest-1-bus.png"
echo "-- compositor log for this request:"; grep -E 'request from' "$W/comp.err" | tail -1 | sed 's/^/   /'
key a; wait $DP
echo "-- controllerd log:"; grep -E 'pre-flight|compositor prompt' "$W/controllerd.log" | tail -2 | sed 's/^/   /'
echo "-- provenance xattr on the output (approval block):"; $C provenance "$W/pics/one.png" | grep -A16 '"approval"'
echo "-- what busd logged for the helper (stamped from, not typed by anyone):"
"$W/bin/bus" log --topic 'approval.preflight' --json 2>/dev/null | tail -1 | python3 -c 'import sys,json; m=json.loads(sys.stdin.read()); print("   from =", m["from"]); print("   payload =", json.dumps(m["payload"]))'

step "2. timing: helper inside the domain, 10 runs, wall clock"
DOM=$(ls "$W/domains" | grep app- | head -1)
t() { local s=$(date +%s%N); "$@" >/dev/null 2>&1; echo $(( ($(date +%s%N) - s) / 1000000 )); }
echo "-- domain run of /bin/true (sandbox setup alone), ms:"; for _ in 1 2 3 4 5 6 7 8 9 10; do t "$W/bin/domain" run "$DOM" -- /bin/true; done | tr '\n' ' '; echo
echo "-- domain run of aurora-preflight (sandbox + connect + publish + confirm), ms:"
for i in 1 2 3 4 5 6 7 8 9 10; do t "$W/bin/domain" run "$DOM" -- /domain/data/helpers/aurora-preflight "t$i" org.aurora.demo delete@v1 destructive; done | tr '\n' ' '; echo
echo "-- controllerd's own measurement of each pre-flight (install + run), from its log:"; grep -o 'published from inside .* in [0-9.]*m\?s' "$W/controllerd.log" | sed 's/^/   /'

step "3. DISAGREEMENT: the event is published from inside domain B; the request names domain A"
"$W/bin/domain" create other >/dev/null
OID=$("$W/bin/domain" exec-id other); OSOCK=$("$W/bin/domain" sockpath other); OCG=$("$W/bin/domain" cgroup other)
"$W/bin/bus" send bus bus.domain.add@v1 "{\"identity\":\"$OID\",\"path\":\"$OSOCK\",\"cgroup\":\"$OCG\"}" >/dev/null 2>&1 || echo "(bus send to register failed; see controller registration)"
mkdir -p "$W/other"; cp "$W/bin/aurora-preflight" "$W/domains/other/data/aurora-preflight" 2>/dev/null
AID=$("$W/bin/domain" exec-id "$DOM"); ANAME=$DOM
GR='rw /home/user/Documents (gesture)'
"$W/bin/domain" run other -- /domain/data/aurora-preflight forged-2 org.aurora.demo delete@v1 destructive "$GR"; echo "helper exit $?"
forge() { # id claimed-name claimed-identity pic
python3 - "$W/run/aurora-trusted.sock" "$W/overlay.png" "${KEEP:-}" "$1" "$2" "$3" "$4" <<'PY' &
import socket, sys, json, os, time, shutil
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
os.path.exists(sys.argv[2]) and os.remove(sys.argv[2])
s.sendall((json.dumps({"v":1,"op":"approve","request_id":sys.argv[4],"app":"org.aurora.demo","domain":sys.argv[5],
  "domain_identity":sys.argv[6],"verb":"delete@v1","effect":"destructive",
  "grants":[{"path":"/home/user/Documents","mode":"rw","source":"gesture"}],"args":{},"timeout_ms":30000})+"\n").encode())
for _ in range(100):
    if os.path.exists(sys.argv[2]): break
    time.sleep(0.1)
time.sleep(0.5)
if sys.argv[3]: shutil.copy(sys.argv[2], sys.argv[3] + "/" + sys.argv[7])
print("-- the compositor's reply after the human pressed a:")
print("   ", s.makefile().readline().strip())
PY
FP=$!; sleep 2; key a; wait $FP; }
forge forged-2 "$ANAME" "$AID" attest-3-mismatch.png

step "4. same-uid forger, a real domain ($ANAME), NO event for its request id"
forge forged-3 "$ANAME" "$AID" attest-4-claim.png
echo; echo "done; cleaning up"
