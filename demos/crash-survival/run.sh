#!/usr/bin/env bash
# Crash-survival demo (the compositor half of Phase 8; clients do NOT survive).
#
# A real busd and a real headless compositor run under supervisord. We change the
# shell the way a user would (the desktop menu and the settings panel, driven by
# key and pointer events), move two windows, kill -9 the compositor, and show:
#   - the supervisor restarted it, and published a crash report to the bus
#     (service, signal SIGKILL, uptime, restart number);
#   - it came back with the same scale, background, dock side and visibility and
#     font (read from the compositor's own `state` command, plus a capture);
#   - its clients are GONE (a kill -9 destroys the wayland socket; there is no
#     display proxy yet) and the layout is a placement memory: the same apps,
#     restarted on their own, map exactly where their predecessors were left;
#   - a compositor that crashes in a loop is given up on, not respawned forever.
# Counts passes and failures, exits non-zero if any failed.
# Needs: go, cargo (builds the compositor, its testclient and the capture tool), python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
W=$(mktemp -d /tmp/crash-demo.XXXXXX)
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/cs.XXXXXX"); chmod 700 "$RUN"   # AF_UNIX paths stay short
PASS=0 FAIL=0 STEPS=0 SUP=0
CLIENTS=()
cleanup() {
  local p
  for p in "${CLIENTS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  if [ "$SUP" != 0 ] && kill -0 "$SUP" 2>/dev/null; then kill -TERM "$SUP" 2>/dev/null; wait "$SUP" 2>/dev/null; fi
  rm -rf "$W" "$RUN"
}
trap cleanup EXIT
step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n' "$STEPS" "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*"; }
check(){ local d=$1; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d  [$*]"; fi; }
eq()   { [ "$1" = "$2" ] && ok "$3 ($1)" || bad "$3: got '$1', want '$2'"; }
has()  { case $1 in *"$2"*) ok "$3";; *) bad "$3 (no '$2' in: ${1:0:300})";; esac; }
waitfor() { local d=$1 t=$2; shift 2; local n=$((t*10)); while [ $n -gt 0 ]; do "$@" >/dev/null 2>&1 && return 0; sleep 0.1; n=$((n-1)); done; return 1; }
show() { sed 's/^/   | /'; }

export AURORA_BUS_SOCK=$RUN/bus.sock AURORA_SUPERVISOR_SOCK=$RUN/supervisor.sock XDG_RUNTIME_DIR=$RUN
BIN=$W/bin; mkdir -p "$BIN"
PATH=$BIN:$PATH
COMP=$ROOT/compositor/target/release/aurora-compositor
TESTCLIENT=$ROOT/compositor/target/release/examples/testclient
CAPTURE=$ROOT/harness/capture/target/release/aurora-capture
INPUT=$RUN/in.sock LAYOUT=$RUN/layout.json THEME=$W/theme CLOG=$RUN/compositor.log

# st NAME FIELD: one field of one service from the supervisor's JSON status
st() { auroractl status --json | python3 -I -c 'import json,sys
for s in json.load(sys.stdin)["services"]:
    if s["name"]==sys.argv[1]: print(s[sys.argv[2]]); break' "$1" "$2"; }
state_is() { [ "$(st "$1" state)" = "$2" ]; }
new_pid() { local p; p=$(st "$1" pid); [ "$p" != 0 ] && [ "$p" != "$2" ]; }
# inj CMD: one line to the compositor's test-input socket, its one-line reply on stdout
inj() { python3 -I -c 'import socket,sys
s=socket.socket(socket.AF_UNIX); s.settimeout(5); s.connect(sys.argv[1]); s.sendall((sys.argv[2]+"\n").encode())
b=b""
while not b.endswith(b"\n"):
    c=s.recv(65536)
    if not c: break
    b+=c
sys.stdout.write(b.decode())' "$INPUT" "$1"; }
answering() { [ "$(inj 'frame 0' 2>/dev/null)" = ok ]; }
# jf JSON EXPR: python expression over the parsed JSON `j` (reply prefix "ok " stripped)
jf() { python3 -I -c 'import json,sys
t=sys.argv[1]; j=json.loads(t[3:] if t.startswith("ok ") else t); print(eval(sys.argv[2]))' "$1" "$2"; }
# shell state as one comparable string, from the compositor's own `state` command
shell_of() { jf "$1" '"scale=%s setting=%s bg=%s dock=%s/%s font=%r/%s" % (j["scale"], j["scale_setting"], j["background"], j["dock"]["edge"], j["dock"]["enabled"], j["font"]["family"], j["font"]["size"])'; }
wins_of() { jf "$1" '";".join("%s:%s@%d,%d" % (w["app_id"], w["title"], w["x"], w["y"]) for w in sorted(j["windows"], key=lambda w: w["app_id"]))'; }
wl_name() { sed -n 's/^aurora-compositor: socket //p' "$CLOG" | tail -1; }
frames() { inj "frame $1" >/dev/null; }
# menu_row N: logical centre "x y" of row N of the newest menu/settings layout in the log
nlayouts() { sed 's/\x1b\[[0-9;]*m//g' "$CLOG" | grep -c 'menu: layout'; }
menu_click() {
  local n=$1 pt
  pt=$(sed 's/\x1b\[[0-9;]*m//g' "$CLOG" | grep 'menu: layout' | tail -1 | python3 -I -c 'import re,sys
l=sys.stdin.read(); sc=float(re.search(r"scale=([0-9.]+)",l).group(1)); rows=[[int(v) for v in r.split(",")] for r in re.search(r"rows=\[([^\]]*)\]",l).group(1).split(";")]
r=rows[int(sys.argv[1])]; print((r[0]+r[2]/2)/sc, (r[1]+r[3]/2)/sc)' "$n")
  inj "pointer $pt" >/dev/null; inj click >/dev/null
}
open_menu() { local n; n=$(nlayouts); inj "pointer 560 40" >/dev/null; inj rclick >/dev/null; waitfor "menu layout" 5 layouts_gt "$n"; sleep 0.5; }
theme_has() { grep -q "$1" "$THEME/aurora-glass.toml"; }
reloaded() { [ "$(sed 's/\x1b\[[0-9;]*m//g' "$CLOG" | grep -c 'theme: loaded')" -gt "$1" ]; }
nreload() { sed 's/\x1b\[[0-9;]*m//g' "$CLOG" | grep -c 'theme: loaded'; }
# decoded pixels of a capture: "WxH sha256-of-rgb" and, with a point, the pixel there
png() { python3 -I - "$@" <<'PY'
import sys, zlib, struct, hashlib
d = open(sys.argv[1], 'rb').read(); p = 8; idat = b''
while p < len(d):
    n, t = struct.unpack('>I4s', d[p:p+8]); body = d[p+8:p+8+n]; p += 12 + n
    if t == b'IHDR': w, h, bd, ct = struct.unpack('>IIBB', body[:10])
    elif t == b'IDAT': idat += body
bpp = {2: 3, 6: 4}[ct]; raw = zlib.decompress(idat); st = w * bpp; rows = []; prev = bytearray(st); q = 0
for _ in range(h):
    f = raw[q]; line = bytearray(raw[q+1:q+1+st]); q += 1 + st
    for i in range(st):
        a = line[i-bpp] if i >= bpp else 0; b = prev[i]; c = prev[i-bpp] if i >= bpp else 0
        if f == 1: line[i] = (line[i] + a) & 255
        elif f == 2: line[i] = (line[i] + b) & 255
        elif f == 3: line[i] = (line[i] + (a + b) // 2) & 255
        elif f == 4:
            pa, pb, pc = abs(b - c), abs(a - c), abs(a + b - 2 * c)
            line[i] = (line[i] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
    rows.append(line); prev = line
if len(sys.argv) > 3:
    x, y = int(sys.argv[2]), int(sys.argv[3]); print(*rows[y][x*bpp:x*bpp+3])
else:
    print(w, h, hashlib.sha256(b''.join(bytes(r) for r in rows)).hexdigest()[:16])
PY
}
snap() { WAYLAND_DISPLAY=$(wl_name) "$CAPTURE" --display "$(wl_name)" -o "$1" >/dev/null 2>&1; }
spawn_client() {  # spawn_client APPID TITLE SIZE RGB: a real wayland client, on the CURRENT compositor
  WAYLAND_DISPLAY=$(wl_name) "$TESTCLIENT" --size "$3" --pattern solid --rgb "$4" --app-id "$1" --title "$2" >"$RUN/$1.log" 2>&1 & CLIENTS+=($!)
}
layouts_gt() { [ "$(nlayouts)" -gt "$1" ]; }
nwin_is() { [ "$(nwin)" = "$1" ]; }
clients_gone() { local p; for p in "${CLIENT_PIDS[@]}"; do kill -0 "$p" 2>/dev/null && return 1; done; return 0; }
have_crash() { [ -n "$(crashes)" ]; }
# mv APPID X Y: place that app'"'"'s window (the compositor numbers windows by stacking order, so look the index up by identity)
mv() { inj "move $(jf "$(inj state)" "[w['app_id'] for w in j['windows']].index('$1')") $2 $3" >/dev/null; }
# px FILE LOGX LOGY: pixel of a capture at a logical position (output scale 1.5)
px() { png "$1" "$(( $2 * 3 / 2 ))" "$(( $3 * 3 / 2 ))"; }
nwin() { jf "$(inj state)" 'len(j["windows"])'; }
# events: every supervisor.service.crashed payload on the bus, one JSON per line
crashes() { bus log 2>&1 | grep 'supervisor.service.crashed' | grep -o '{.*}$'; }

step "build"
(cd "$ROOT/bus" && go build -o "$BIN/" ./cmd/busd ./cmd/bus) || bad "bus build"
(cd "$ROOT/supervisor" && go build -o "$BIN/" ./cmd/supervisord ./cmd/auroractl) || bad "supervisor build"
(cd "$ROOT/compositor" && cargo build --release --examples 2>&1 | tail -1) || bad "compositor build"
[ -x "$CAPTURE" ] || (cd "$ROOT/harness/capture" && cargo build --release 2>&1 | tail -1) || bad "capture build"
for b in "$BIN/busd" "$BIN/bus" "$BIN/supervisord" "$BIN/auroractl" "$COMP" "$TESTCLIENT" "$CAPTURE"; do [ -x "$b" ] || { bad "$b missing"; echo "cannot go on"; exit 1; }; done
ok "built"

step "busd and the compositor run under supervisord (the compositor is just another service entry)"
cp -r "$ROOT/contracts/theme" "$THEME"
cat > "$W/services.json" <<EOF
{
  "defaults": {"backoff_initial":"200ms","backoff_max":"1s","stable_after":"2s","crash_limit":5,"crash_window":"60s","stop_grace":"2s"},
  "services": [
    {"name":"busd","command":["$BIN/busd","--socket","$AURORA_BUS_SOCK","--data-dir","$RUN/buslog"],
     "health":{"type":"bus"},"log":"$RUN/busd.log"},
    {"name":"compositor","needs":["busd"],"health":{"type":"none"},"log":"$CLOG",
     "command":["$COMP","--headless=960x600","--freeze-time","--bus-socket","$AURORA_BUS_SOCK","--trusted-socket","$RUN/trusted.sock",
                "--theme","$THEME","--test-input","$INPUT","--state-file","$LAYOUT"]}
  ]
}
EOF
"$BIN/supervisord" --config "$W/services.json" --control "$AURORA_SUPERVISOR_SOCK" --bus-socket "$AURORA_BUS_SOCK" \
   --children-file "$RUN/children" >"$RUN/supervisord.log" 2>&1 & SUP=$!
waitfor "control socket" 10 test -S "$AURORA_SUPERVISOR_SOCK" && ok "supervisor is listening" || bad "no control socket"
waitfor "bus socket" 10 test -S "$AURORA_BUS_SOCK" && ok "busd's socket exists" || bad "busd socket never appeared"
waitfor "compositor up" 20 answering && ok "compositor answers on its test-input socket" || bad "compositor never answered"
eq "$(st compositor state)" running "supervisor state of the compositor"
auroractl status | show
CP0=$(st compositor pid)
eq "$(readlink /proc/$CP0/exe)" "$COMP" "status pid $CP0 is really the compositor binary"
has "$(cat "$CLOG")" "no earlier layout (first run)" "first start: nothing to restore"
S0=$(shell_of "$(inj state)"); echo "   fresh shell: $S0"
has "$S0" "bg=shader dock=bottom/True" "defaults: shader background, dock at the bottom, shown"

step "set up a desktop the way a user would: menu and settings panel, then two windows"
open_menu
R=$(nreload); menu_click 8   # "Solid colour"
waitfor "theme file says solid" 10 theme_has 'mode = "solid"' && ok "menu click: the compositor wrote background mode = solid into the theme file" || bad "theme not written"
waitfor "reload" 10 reloaded "$R"; sleep 0.5
open_menu; R=$(nreload); menu_click 10   # "Move Dock to Left"
waitfor "dock left" 10 theme_has 'edge = "left"' && ok "menu click: dock edge = left written" || bad "dock edge not written"
waitfor "reload" 10 reloaded "$R"; sleep 0.5
open_menu; menu_click 1; sleep 0.8   # "Settings..."
for k in Down Right Right Right; do inj "key $k" >/dev/null; sleep 0.15; done   # Display page, row Scale: Auto -> 1 -> 1.25 -> 1.5
waitfor "scale written" 10 theme_has 'scale = 1.5' && ok "settings panel (keyboard): output scale = 1.5 written" || bad "scale not written"
# Tab until the Dock & Text page is actually up, rather than a fixed two Tabs. The count was
# two and became wrong the moment a page was inserted before it (Network, 2026-11-07), which
# has nothing to do with what this step is checking. The settings layout line names the rows
# it drew, so "Dock" appearing in it is the page arriving.
for _ in 1 2 3 4 5 6 7 8; do
  grep -q 'settings: layout.*Dock_edge' "$CLOG" && break
  inj "key Tab" >/dev/null; sleep 0.25
done
grep -q 'settings: layout.*Dock_edge' "$CLOG" || bad "Tab never reached the Dock & Text page"
sleep 0.2
for k in Down Down Left; do inj "key $k" >/dev/null; sleep 0.15; done   # row 'Dock': Left flips it off
waitfor "dock hidden" 10 theme_has 'enabled = false' && ok "settings panel: dock hidden (enabled = false) written" || bad "dock visibility not written"
for k in Down Down Down Right Right Right; do inj "key $k" >/dev/null; sleep 0.15; done   # Font size 14 -> 17
waitfor "font size" 10 theme_has 'size = 17' && ok "settings panel: font size = 17 written" || bad "font size not written"
inj "key Escape" >/dev/null; sleep 1.2   # let hot reload settle
spawn_client demo.alpha "Alpha" 200x150 220,60,60
spawn_client demo.beta "Beta" 180x120 60,60,220
waitfor "two windows" 10 nwin_is 2 && ok "two client windows are mapped" || bad "windows did not map: $(inj state)"
mv demo.alpha 210 90; mv demo.beta 333 177
BEFORE=$(inj state)
SB=$(shell_of "$BEFORE"); WB=$(wins_of "$BEFORE")
echo "   shell before: $SB"; echo "   windows before: $WB"
has "$SB" "scale=1.5" "scale 1.5 is live"; has "$SB" "bg=solid" "background solid is live"; has "$SB" "dock=left/False" "dock left and hidden is live"; has "$SB" "/17.0" "font size 17 is live"
eq "$WB" "demo.alpha:Alpha@210,90;demo.beta:Beta@333,177" "windows placed"
waitfor "layout file written" 10 grep -q Beta "$LAYOUT" && ok "the settled layout reached $LAYOUT" || bad "layout file not written"
sleep 1; frames 3
snap "$W/before.png"; B1=$(png "$W/before.png"); echo "   capture before: $B1"
BG=$(px "$W/before.png" 600 380)
check "capture: alpha's pixel at its reported place ($(px "$W/before.png" 230 110)) is not the background ($BG)" test "$(px "$W/before.png" 230 110)" != "$BG"
check "capture: beta's pixel at its reported place ($(px "$W/before.png" 470 270)) is not the background" test "$(px "$W/before.png" 470 270)" != "$BG"
python3 -I -c 'import json,sys
d=json.load(open(sys.argv[1])); print(d["version"], [(w["app_id"],w["x"],w["y"]) for w in d["windows"]])' "$LAYOUT" | show

step "kill -9 the compositor"
CLIENT_PIDS=("${CLIENTS[@]}")
kill -9 "$CP0"; echo "   kill -9 $CP0"
waitfor "compositor replaced" 15 new_pid compositor "$CP0" && ok "supervisor restarted it: pid $CP0 -> $(st compositor pid)" || bad "compositor did not come back"
waitfor "compositor answering" 20 answering && ok "the new compositor answers on the same test-input socket" || bad "new compositor not answering"
auroractl status | show
eq "$(st compositor restarts)" 1 "restart count is 1"
eq "$(st compositor last_exit)" "killed by SIGKILL" "supervisor's last exit reason"
eq "$(st busd restarts)" 0 "busd was not touched"
has "$(cat "$CLOG")" "2 remembered placement(s) loaded" "the new process says it loaded 2 placements from the state file"

step "the crash report is on the bus"
waitfor "crash report" 10 have_crash && ok "supervisor.service.crashed is on the bus" || bad "no crash report"
crashes | show
C=$(crashes | tail -1)
eq "$(jf "$C" 'j["service"]')" compositor "report names the service"
eq "$(jf "$C" 'j["signal"]')" SIGKILL "report names the signal"
eq "$(jf "$C" 'j["exit_code"]')" -1 "exit_code is -1 for a death by signal"
eq "$(jf "$C" 'j["pid"]')" "$CP0" "report carries the pid that died"
eq "$(jf "$C" 'j["restart"]')" 1 "restart number"
eq "$(jf "$C" 'j["will_restart"]')" True "it says it will restart"
check "uptime is real and positive ($(jf "$C" 'j["uptime_ms"]') ms)" test "$(jf "$C" 'j["uptime_ms"]')" -gt 3000
LOG=$(bus log 2>&1)
has "$LOG" "uid$(id -u):supervisord -> * supervisor.service.crashed" "sender stamped by busd from SO_PEERCRED, not claimed in the payload"
check "an orderly stop is NOT a crash: no report for busd or for earlier clean events" test "$(crashes | grep -c '"service":"busd"')" = 0

step "the shell came back as it was"
AFTER=$(inj state)
SA=$(shell_of "$AFTER"); echo "   shell after:  $SA"
eq "$SA" "$SB" "scale, background, dock side and visibility, font: identical to before the kill"
eq "$(jf "$AFTER" 'j["scale"]')" 1.5 "the live output scale is 1.5 again"
waitfor "clients gone" 5 clients_gone && ok "its clients did NOT survive: both testclients exited when the display died" || bad "clients still running"
eq "$(nwin)" 0 "the new compositor has no windows: there is nothing to restore a window from"
eq "$(jf "$AFTER" 'j["restored"]')" 0 "no placement applied yet"

step "the layout is a placement memory: the same apps, restarted on their own, land where they were"
CLIENTS=()
# One at a time, each mapped before the next is started. The claim below is about the cascade
# ORDER -- gamma takes the THIRD slot because alpha and beta took the first two -- and that is
# only true if they map in that order. Started together, under load, gamma could map first and
# land in slot one, which failed as "a window the layout never saw is NOT moved" and named
# nothing about the race that caused it.
spawn_client demo.alpha "Alpha" 200x150 220,60,60
waitfor "alpha mapped" 10 nwin_is 1 || bad "alpha did not map"
spawn_client demo.beta "Beta" 180x120 60,60,220
waitfor "beta mapped" 10 nwin_is 2 || bad "beta did not map"
spawn_client demo.gamma "Gamma" 100x100 60,220,60   # never seen before: no memory, so the default spot
waitfor "three windows" 10 nwin_is 3 && ok "three client windows mapped, in order" || bad "windows did not map"
sleep 1; frames 3
RESTORED=$(inj state); WA=$(wins_of "$RESTORED"); echo "   windows after: $WA"
has "$WA" "demo.alpha:Alpha@210,90" "alpha is back at 210,90"
has "$WA" "demo.beta:Beta@333,177" "beta is back at 333,177"
has "$WA" "demo.gamma:Gamma@96,118" "a window the layout never saw is NOT moved: it sits in its cascade slot, the third (32+2*32, 24+30+2*32), where alpha and beta took the first two"
eq "$(jf "$RESTORED" 'j["restored"]')" 2 "exactly two placements were applied"
mv demo.gamma 20 300; frames 3   # gamma out of the way so the frame can be compared with the earlier one
snap "$W/after.png"; A1=$(png "$W/after.png"); echo "   capture after: $A1"
eq "$(px "$W/after.png" 230 110)" "$(px "$W/before.png" 230 110)" "capture: alpha's pixel after the restart equals the one before"
eq "$(px "$W/after.png" 470 270)" "$(px "$W/before.png" 470 270)" "capture: beta's pixel after the restart equals the one before"
eq "$(px "$W/after.png" 600 380)" "$BG" "capture: the solid background pixel is unchanged"
eq "$(px "$W/after.png" 215 95)" "$(px "$W/before.png" 215 95)" "capture: a pixel just inside alpha's corner matches too"
eq "$(px "$W/after.png" 40 320)" "60 220 60" "capture: gamma (never remembered) is where it was put, drawn green"

step "a crash loop is still given up on"
for i in 1 2 3 4 5 6 7 8; do
  state_is compositor gave_up && break
  P=$(st compositor pid)
  if [ "$P" != 0 ]; then kill -9 "$P" 2>/dev/null; waitfor "compositor replaced or given up" 8 new_pid compositor "$P"; fi
  sleep 0.3
done
waitfor "gave up" 10 state_is compositor gave_up && ok "compositor is gave_up" || bad "compositor state is '$(st compositor state)'"
auroractl status | show
has "$(st compositor detail)" "unstable exits" "reason: $(st compositor detail)"
eq "$(st compositor last_exit)" "killed by SIGKILL" "last exit"
R1=$(st compositor restarts); sleep 3
eq "$(st compositor restarts)" "$R1" "3 seconds later: still $R1 restarts, nothing is spinning"
eq "$(grep -c -- '--- supervisord: start #' "$CLOG")" "$((R1+1))" "its log shows exactly $((R1+1)) runs"
check "the supervisor published gave_up for it" bash -c "bus log 2>&1 | grep supervisor.service.gave_up | grep -q '\"service\":\"compositor\"'"
n=$(crashes | grep -c '"service":"compositor"'); check "every SIGKILL got a crash report ($n reports, $R1 restarts + the final death)" test "$n" -ge "$R1"
LASTC=$(crashes | tail -1)
eq "$(jf "$LASTC" 'j["will_restart"]')" False "the last report says it will NOT restart"
eq "$(st busd restarts)" 0 "busd, which the compositor needs, was never disturbed"
step "the supervisor's own shutdown leaves nothing"
kill -TERM "$SUP"; wait "$SUP" 2>/dev/null; RC=$?; SUP=0
eq "$RC" 0 "supervisord exited 0"
LEFT=$(RUN=$RUN awk -v me=$$ '$1!=me && index($0,ENVIRON["RUN"]) && !index($0,"awk") {print $1}' < <(ps -eo pid=,args=)); [ -z "$LEFT" ] && ok "no process mentions $RUN" || bad "stray processes: $LEFT"

printf '\n'
if [ $FAIL = 0 ]; then echo "CRASH-SURVIVAL DEMO PASSED ($PASS checks, $STEPS steps)"; else echo "CRASH-SURVIVAL DEMO FAILED: $FAIL failed, $PASS passed ($STEPS steps)"; exit 1; fi
