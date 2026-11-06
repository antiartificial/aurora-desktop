#!/usr/bin/env bash
# End-to-end trusted-path demo: a destructive verb is invoked, the HEADLESS
# compositor draws the approval overlay, a keystroke decides, and the verb is
# denied or allowed accordingly; then the compositor is stopped and the same
# verb falls back to the bus route. The keystrokes are injected through the
# compositor's --test-input affordance (headless only: there is no real
# keyboard here). Usage: demo-trusted.sh [DIR to keep overlay PNGs in]
set -uo pipefail
cd "$(dirname "$0")"
CTL=$(pwd); ROOT=$(cd .. && pwd); KEEP=${1:-}
W=$(mktemp -d /tmp/ctl-trusted.XXXXXX); PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  for d in "$W"/domains/app-*; do [ -d "$d" ] && "$W/bin/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done; rm -rf "$W"; }
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
export AURORA_BUS_SOCK=$W/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$W/run
mkdir -p "$W/bin" "$W/pics" "$W/run" "$W/home/Pictures"; chmod 700 "$W/run"

step "build"
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || exit 1
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || echo "cannot build domain CLI: verbs run UNISOLATED"
go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller || exit 1
(cd "$ROOT/compositor" && cargo build --release --examples 2>&1 | tail -1) || exit 1
CAP=$ROOT/harness/capture/target/release/aurora-capture
COMP=$ROOT/compositor/target/release/aurora-compositor
[ -x "$CAP" ] || { echo "build harness/capture first"; exit 1; }
HOME=$W/home; export HOME   # after the builds: the fixture declares ~/Pictures, gestures lie outside it
printf '\x89PNG\r\n\x1a\n\0\0\0\rIHDR' > "$W/pics/in.png"

step "start busd, the headless compositor with a focused client window, and controllerd"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
"$COMP" --headless=1280x800 --test-input "$W/run/input.sock" --dump-overlay "$W/overlay.png" >"$W/comp.out" 2>"$W/comp.err" & COMPPID=$!; PIDS+=($COMPPID)
for _ in $(seq 100); do grep -q 'socket wayland' "$W/comp.out" && [ -S "$W/run/input.sock" ] && break; sleep 0.1; done
cat "$W/comp.out"; grep WARNING "$W/comp.err"
WL=$(sed -n 's/^aurora-compositor: socket //p' "$W/comp.out")
WAYLAND_DISPLAY=$WL "$ROOT/compositor/target/release/examples/testclient" >"$W/client.log" 2>&1 & PIDS+=($!)
for _ in $(seq 100); do grep -q enter "$W/client.log" && break; sleep 0.1; done
"$W/bin/controllerd" --apps "$ROOT/apps,$CTL/testdata/apps" --domain-bin "$W/bin/domain" --approval-timeout 30s >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
grep -E 'compositor|approval' "$W/controllerd.log"
C="$W/bin/controller"
key() { printf 'key %s\n' "$1" | socat - "UNIX-CONNECT:$W/run/input.sock"; }
pixels() { python3 - "$1" <<'PY'
import sys, zlib, struct
d = open(sys.argv[1], "rb").read(); p = 8; idat = b""
while p < len(d):
    n, t = struct.unpack(">I4s", d[p:p+8]); c = d[p+8:p+8+n]; p += 12 + n
    if t == b"IHDR": w, h, bd, ct = struct.unpack(">IIBB", c[:10])
    if t == b"IDAT": idat += c
bpp = {2: 3, 6: 4}[ct]; raw = zlib.decompress(idat); st = w * bpp; prev = bytearray(st); out = []; i = 0
for _ in range(h):
    f = raw[i]; row = bytearray(raw[i+1:i+1+st]); i += 1 + st
    for x in range(st):
        a = row[x-bpp] if x >= bpp else 0; b = prev[x]; c2 = prev[x-bpp] if x >= bpp else 0
        if f == 1: row[x] = (row[x] + a) & 255
        elif f == 2: row[x] = (row[x] + b) & 255
        elif f == 3: row[x] = (row[x] + (a + b) // 2) & 255
        elif f == 4:
            pa, pb, pc = abs(b - c2), abs(a - c2), abs(a + b - 2 * c2)
            row[x] = (row[x] + (a if pa <= pb and pa <= pc else b if pb <= pc else c2)) & 255
    out.append(row); prev = row
brd = sum(1 for r in out for x in range(0, st, bpp) if tuple(r[x:x+3]) == (255, 90, 20))
print(f"{sys.argv[1].rsplit('/',1)[-1]}: {w}x{h}, overlay-border-coloured pixels = {brd}, pixel(10,10) = {tuple(out[10][10*bpp:10*bpp+3])}, centre = {tuple(out[h//2][(w//2)*bpp:(w//2)*bpp+3])}")
PY
}
DEL=(invoke org.aurora.demo delete@v1 --input "$W/pics/in.png")

step "1. DENY: destructive verb on a gesture grant; the compositor draws the overlay; key d"
rm -f "$W/overlay.png"; $C "${DEL[@]}" --output "$W/pics/deny.png" >"$W/deny.out" 2>&1 & DP=$!
for _ in $(seq 100); do [ -f "$W/overlay.png" ] && break; sleep 0.1; done
echo "invoke still running: $(kill -0 $DP 2>/dev/null && echo yes || echo NO)   output: $(ls "$W/pics/deny.png" 2>&1 | sed 's/.*No such.*/absent/')"
"$CAP" --display "$WL" -o "$W/capture-during.png" >/dev/null 2>&1
echo "-- the compositor's own frame vs a capture taken through the Wayland socket, same instant:"
pixels "$W/overlay.png"; pixels "$W/capture-during.png"
[ -n "$KEEP" ] && cp "$W/overlay.png" "$KEEP/trusted-overlay-compositor-frame.png" && cp "$W/capture-during.png" "$KEEP/trusted-overlay-client-capture.png"
echo "-- client window's keyboard log so far (it lost focus when the overlay rose):"; cat "$W/client.log"
key z; key q; sleep 0.3; echo "-- after typing z and q at the overlay, client log unchanged:"; cat "$W/client.log" | tr '\n' ' '; echo
key d; wait $DP
grep -E '"code"|"message"' "$W/deny.out" | head -2
echo "output file after denial: $(ls "$W/pics/deny.png" 2>&1 | sed 's/.*No such.*/absent/')"

step "2. ALLOW: same verb; key a"
rm -f "$W/overlay.png"; $C "${DEL[@]}" --output "$W/pics/allow.png" >"$W/allow.out" 2>&1 & DP=$!
for _ in $(seq 100); do [ -f "$W/overlay.png" ] && break; sleep 0.1; done
sleep 0.3; key a; wait $DP
grep -A8 '"approval"' "$W/allow.out" | head -9
echo "output file after allow: $(ls "$W/pics/allow.png" 2>&1)"
echo "-- provenance xattr on the output:"; $C provenance "$W/pics/allow.png" | grep -A8 '"approval"'
echo "-- client log, whole run (no key from the overlay interaction):"; cat "$W/client.log" | tr '\n' ' '; echo

step "3. FALLBACK: compositor stopped; the same verb must use the bus route"
kill $COMPPID; wait $COMPPID 2>/dev/null; sleep 0.3
"$W/bin/bus" sub approval.requested >"$W/approvals.log" 2>&1 & PIDS+=($!); sleep 0.5
$C "${DEL[@]}" --output "$W/pics/fallback.png" >"$W/fb.out" 2>&1 & DP=$!
for _ in $(seq 50); do ID=$(grep -o '"request_id":"[a-f0-9]*"' "$W/approvals.log" | head -1 | cut -d'"' -f4); [ -n "$ID" ] && break; sleep 0.1; done
$C decide "$ID" allow | grep -E '"decision"|"by"'; wait $DP
grep -A8 '"approval"' "$W/fb.out" | head -9
echo "-- controllerd log:"; grep -E 'compositor|falling back|approval' "$W/controllerd.log"
echo; echo "done; cleaning up"
