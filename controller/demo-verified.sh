#!/usr/bin/env bash
# End-to-end: domain verification on the trusted path, with a real busd, a real
# controllerd, a real headless compositor and a real bwrap domain. Shows
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
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || { echo "cannot build domain CLI"; exit 1; }
go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller || exit 1
cat > "$W/impostor.go" <<'GO'
package main
import ("bufio";"fmt";"net";"os")
func main() { l, err := net.Listen("unix", os.Args[1]); if err != nil { panic(err) }
  fmt.Println("impostor listening"); for { c, _ := l.Accept(); go func() { line, _ := bufio.NewReader(c).ReadString('\n')
  fmt.Printf("IMPOSTOR RECEIVED: %q\n", line); fmt.Fprintln(c, `{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"true"}`); c.Close() }() } }
GO
(cd "$W" && go build -o "$W/bin/impostor" impostor.go) || exit 1
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

step "1. FIRST destructive invoke of this app: its domain does not exist yet, so busd cannot know it"
rm -f "$W/overlay.png"; $C "${DEL[@]}" --output "$W/pics/one.png" >"$W/one.out" 2>&1 & DP=$!
await_overlay; [ -n "$KEEP" ] && cp "$W/overlay.png" "$KEEP/verify-1-unverified.png"
key a; wait $DP
echo "-- provenance xattr on the output (approval block):"; $C provenance "$W/pics/one.png" | grep -A12 '"approval"'
echo "-- controllerd:"; grep -E 'allowed at the compositor' "$W/controllerd.log" | tail -1

step "2. SECOND invoke: the domain now exists; the controller registered it with busd, with its cgroup"
"$W/bin/bus" inspect | sed -n '/domain sockets/,/handles/p'
rm -f "$W/overlay.png"; $C "${DEL[@]}" --output "$W/pics/two.png" >"$W/two.out" 2>&1 & DP=$!
await_overlay; [ -n "$KEEP" ] && cp "$W/overlay.png" "$KEEP/verify-2-verified.png"
key a; wait $DP
echo "-- provenance xattr on the output (approval block):"; $C provenance "$W/pics/two.png" | grep -A12 '"approval"'
echo "-- controllerd:"; grep -E 'allowed at the compositor' "$W/controllerd.log" | tail -1
cp "$W/overlay.png" "$W/two-overlay.png" 2>/dev/null

step "3. forged request straight at the compositor: a domain busd has never heard of"
python3 - "$W/run/aurora-trusted.sock" "$W/overlay.png" "${KEEP:-}" <<'PY' &
import socket, sys, json, os, time, shutil
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
os.path.exists(sys.argv[2]) and os.remove(sys.argv[2])
s.sendall((json.dumps({"v":1,"op":"approve","request_id":"forged","app":"org.aurora.demo","domain":"app-org-aurora-a20146bbdf88c9da",
  "domain_identity":"domain:app-org-aurora-a20146bbdf88c9da:deadbeefdeadbeef","verb":"delete@v1","effect":"destructive",
  "grants":[{"path":"/home/user/Documents","mode":"rw","source":"gesture"}],"args":{},"timeout_ms":30000})+"\n").encode())
for _ in range(100):
    if os.path.exists(sys.argv[2]): break
    time.sleep(0.1)
time.sleep(0.5)
if sys.argv[3]: shutil.copy(sys.argv[2], sys.argv[3] + "/verify-3-forged.png")
print("-- the forger's reply after the human pressed d:")
print("   ", s.makefile().readline().strip())
PY
FP=$!
sleep 1.5; key d; wait $FP

step "4a. impostor: a compositor socket whose peer has the wrong uid (controllerd told to expect uid+1)"
pkill -f "$W/bin/controllerd" ; sleep 0.3
"$W/bin/controllerd" --apps "$ROOT/apps,$CTL/testdata/apps" --domain-bin "$W/bin/domain" --approval-timeout 30s --compositor-uid $(( $(id -u) + 1 )) >"$W/controllerd2.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd2.log" && break; sleep 0.1; done
"$W/bin/bus" sub approval.requested >"$W/approvals.log" 2>&1 & PIDS+=($!); sleep 0.5
rm -f "$W/overlay.png"; $C "${DEL[@]}" --output "$W/pics/three.png" >"$W/three.out" 2>&1 & DP=$!
for _ in $(seq 50); do ID=$(grep -ao '"request_id":"[a-f0-9]*"' "$W/approvals.log" | head -1 | cut -d'"' -f4); [ -n "$ID" ] && break; sleep 0.1; done
echo "overlay drawn by the real compositor for this request: $([ -f "$W/overlay.png" ] && echo YES || echo no)"
$C decide "$ID" deny | grep -E '"decision"|"by"'; wait $DP
grep -E '"code"|"error"' "$W/three.out" | head -2
echo "-- controllerd log:"; grep -E 'REFUSING|compositor' "$W/controllerd2.log" | sed 's/^/   /'

step "4b. impostor INSIDE a real domain squatting a socket path (same uid, but in an aurora-domains cgroup)"
"$W/bin/domain" create impostor >/dev/null
mkdir -p "$W/squat"; "$W/bin/domain" grant impostor "$W/squat" --rw >/dev/null; "$W/bin/domain" grant impostor "$W/bin/impostor" --ro >/dev/null
"$W/bin/domain" run impostor -- "$W/bin/impostor" "$W/squat/trusted.sock" >"$W/impostor.out" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$W/squat/trusted.sock" ] && break; sleep 0.1; done
pkill -f "$W/bin/controllerd"; sleep 0.3
"$W/bin/controllerd" --apps "$ROOT/apps,$CTL/testdata/apps" --domain-bin "$W/bin/domain" --approval-timeout 30s --compositor-socket "$W/squat/trusted.sock" >"$W/controllerd3.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd3.log" && break; sleep 0.1; done
: > "$W/approvals.log"
$C "${DEL[@]}" --output "$W/pics/four.png" >"$W/four.out" 2>&1 & DP=$!
for _ in $(seq 50); do ID=$(grep -ao '"request_id":"[a-f0-9]*"' "$W/approvals.log" | head -1 | cut -d'"' -f4); [ -n "$ID" ] && break; sleep 0.1; done
$C decide "$ID" deny | grep -E '"decision"|"by"'; wait $DP
grep -E '"code"' "$W/four.out" | head -1
echo "-- what the impostor saw (it accepted a connection, got no request):"; cat "$W/impostor.out"
echo "-- controllerd log:"; grep -E 'REFUSING' "$W/controllerd3.log" | sed 's/^/   /'
echo; echo "done; cleaning up"
