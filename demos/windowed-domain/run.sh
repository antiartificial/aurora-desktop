#!/usr/bin/env bash
# Windowed apps in domains (decisions 0012 and 0017): a dock click puts a Haiku app
# in a domain, and the compositor knows which one.
#
# Real stack (busd, controllerd, compositor), real bubblewrap domains, real Haiku
# Interface Kit apps built from HAIKU_SRC. The click is a pointer event on the
# compositor's own dock. Everything is read back from outside the app: the kernel's
# view of its cgroup and uid (/proc), decoded pixels of the compositor's frame and of
# a client capture, busd's own handle table, and the compositor's own state.
#
#    1. build                     the kits (cmake, HAIKU_SRC) and the compositor
#    2. stack and baseline        nothing launched: no window, no indicator
#    3. a dock click              the app runs in a domain: subordinate uid, aurora-domains
#                                 cgroup, device:wayland held, and a window with real pixels
#    4. the indicator             lit for it, by a handle busd holds, named for the domain;
#                                 invisible to a capture; the weakest point of 0016, closed
#    5. the compositor knows      the window's domain comes from the kernel; a host client
#                                 that TITLES itself with the domain's name gets none
#    6. it exits                  close button: the domain, its cgroup, its handle, its grant
#                                 and its indicator are gone, and the frame is the baseline
#    7. a second launch           Haiku Text, closed: the same teardown (by itself, or by stop)
#    8. what a domain gets        a fixture app with one declared read path and one write
#                                 path: those two and the display, nothing else, keys included
#    9. a launch that fails       says why, on launch_problem, and leaves nothing behind
#   10. the kits' shared memory   aurora-kits-<uid>-<ns> inside a domain
#   11. shut the stack down       no stray cgroups
#   12. the escape hatch          --unsandboxed-windows: loud, explicit, and what it costs
#
# What this does NOT prove is printed at the end.
#
# Needs: go, cargo, cmake, ninja, bwrap, socat, python3, HAIKU_SRC (the kits).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
RUN=$(mktemp -d /tmp/aurora-windom.XXXXXX); WORK=$(mktemp -d /tmp/aurora-windom-work.XXXXXX)
RUN2=$(mktemp -d /tmp/aurora-windom2.XXXXXX)
PNG="python3 -I $ROOT/demos/full-stack/pngtool.py"
EX=$ROOT/compositor/target/release/examples
UP=0; UPPID=; STEPS=0; PASS=0; JOBS_PIDS=(); EXTRA_PIDS=()
CGDIR=/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/aurora-domains
cgnames() { ls "$CGDIR" 2>/dev/null | sort; }
CG0=$(cgnames)
APP=org.aurora.haiku.controls   # a real Haiku app, iface_controls, in the dock

step()  { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$1" "$2"; }
fail()  { printf '   FAIL: %s\n' "$*" >&2; exit 1; }
ok()    { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
eq()    { [ "$1" = "$2" ] && ok "$3 (= '$2')" || fail "$3: got '$1', want '$2'"; }
waitf() { local d=$1; shift; for _ in $(seq 300); do "$@" 2>/dev/null && return 0; sleep 0.1; done; fail "timed out: $d"; }
js()    { python3 -I -c 'import json,sys; d=json.load(sys.stdin)
for k in sys.argv[1].split("."): d=d[int(k)] if isinstance(d,list) else d[k]
print(d if not isinstance(d,bool) else str(d).lower())' "$1"; }
ask()   { echo "$1" | socat - "UNIX-CONNECT:$AURORA_INPUT_SOCK"; }
inp()   { ask "$1" >/dev/null; }
cap()   { "$AURORA_CAPTURE" --display "$AURORA_WAYLAND" -o "$1" >/dev/null 2>&1 || fail "capture failed"; }
own()   { rm -f "$1"; inp "dump $1"; waitf "the compositor to write its frame" test -s "$1"; }
clog()  { cat "$RUN/compositor.log"; }
layouts() { clog | grep -c 'indicator: layout' || true; }
field() { sed -n "s/.* $2=\([^ ]*\) .*/\1/p" <<<"$1" | head -1; }
# the compositor's window list: "title|domain|x|y|w|h" per window ("-" for no domain)
windows() { ask state | sed 's/^ok //' | python3 -I -c 'import json,sys
for w in json.load(sys.stdin)["windows"]: print("|".join([w["title"], w["domain"] or "-", str(w["x"]), str(w["y"]), str(w["w"]), str(w["h"])]))'; }
# pids whose cgroup is inside domain $1
inpids() { for p in /proc/[0-9]*; do grep -q "aurora-domains/$1\$" "$p/cgroup" 2>/dev/null && echo "${p#/proc/}"; done; }
has_win() { windows | grep -q "|$1|"; }
no_win() { ! has_win "$1"; }
domlist() { domain list 2>/dev/null | awk '{print $1}'; }
handles() { bus inspect --json 2>/dev/null; }

cleanup() {
  for p in "${JOBS_PIDS[@]:-}" "${EXTRA_PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  [ $UP = 1 ] && { kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0; }
  if [ -n "${AURORA_DOMAINS_DIR:-}" ]; then for d in wd-shm; do domain destroy "$d" >/dev/null 2>&1; done; fi
  rm -rf "$WORK" "$RUN" "$RUN2" "$RUN.up.log" "$RUN2.up.log" 2>/dev/null
}
trap cleanup EXIT

step "build" "the kits from HAIKU_SRC, the controller, the adapter and the compositor, all from this tree"
"$ROOT/aurora-up" --build-only >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; fail "build failed"; }
(cd "$ROOT/compositor" && cargo build --release >"$WORK/cargo.log" 2>&1) || { cat "$WORK/cargo.log"; fail "compositor build failed"; }
[ -x "$EX/testclient" ] || fail "no $EX/testclient"
IFACE=$ROOT/kits/build/interface/iface_controls
[ -x "$IFACE" ] || fail "no $IFACE: the Haiku kits did not build (needs HAIKU_SRC, cmake and ninja); this demo is about real Haiku apps and will not fake one"
ok "built; the Haiku app is $IFACE"

# ---- fixture apps: windowed, with declared capabilities, to see exactly what a domain gets
FX=$WORK/apps; mkdir -p "$FX/probe" "$FX/crash" "$WORK/shared" "$WORK/out" "$WORK/secret"
echo "declared-and-readable" >"$WORK/shared/hello"; echo "not-declared" >"$WORK/secret/token";
KEYS=$RUN/xdg/aurora
cat >"$FX/probe/manifest.toml" <<EOF
[app]
id = "org.zz.probe"
name = "Probe"
version = "0.1.0"
image_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
windowed = true
exec = "probe.sh"

[capabilities]
paths = [{ path = "$WORK/shared", access = "read" }, { path = "$WORK/out", access = "write" }]
EOF
cat >"$FX/probe/probe.sh" <<EOF
#!/usr/bin/sh
{
echo "uid=\$(id -u)"
echo "control=\$(cat /etc/passwd 2>/dev/null | wc -c | tr -d ' ')"
echo "wayland_display=\$WAYLAND_DISPLAY"
echo "socket=\$(test -S /run/aurora-display/wayland-0 && echo yes || echo no)"
echo "run=\$(ls /run | tr '\n' ' ')"
echo "rundisplay=\$(ls /run/aurora-display | tr '\n' ' ')"
echo "declared_read=\$(cat $WORK/shared/hello 2>/dev/null || echo DENIED)"
echo "declared_write_to_ro=\$( { echo x >$WORK/shared/new; } 2>/dev/null && echo WROTE || echo DENIED)"
echo "undeclared=\$(cat $WORK/secret/token 2>/dev/null || echo DENIED)"
echo "undeclared_ls=\$(ls $WORK/secret 2>/dev/null | wc -l | tr -d ' ')"
echo "home=\$HOME"
echo "home_entries=\$(ls -A \$HOME | grep -v '^app\$' | tr '\n' ' ')"
echo "host_run=\$(test -e $RUN/xdg && echo present || echo absent)"
echo "keys=\$(for k in approval.key powerbox.key; do cat $KEYS/\$k 2>/dev/null; done | wc -c | tr -d ' ')"
echo "key32=\$(find / -xdev -type f -size 32c 2>/dev/null | xargs -r sha256sum 2>/dev/null | cut -d' ' -f1 | tr '\n' ' ')"
echo "net=\$(tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ' | tr '\n' ' ')"
echo "dri=\$(ls /dev/dri 2>/dev/null | wc -l | tr -d ' ')"
echo "shm=\$(ls /dev/shm | tr '\n' ' ')"
echo "env=\$(env | cut -d= -f1 | sort | tr '\n' ' ')"
} >$WORK/out/report.tmp 2>&1
mv $WORK/out/report.tmp $WORK/out/report
exec /usr/bin/sleep 600
EOF
cat >"$FX/crash/manifest.toml" <<EOF
[app]
id = "org.zz.crash"
name = "Crash"
version = "0.1.0"
image_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
windowed = true
exec = "crash.sh"
EOF
printf '#!/usr/bin/sh\necho "boom: cannot open the display (this is the fixture)" >&2\nexit 3\n' >"$FX/crash/crash.sh"
chmod +x "$FX/probe/probe.sh" "$FX/crash/crash.sh"

step "bring the stack up, and take a baseline" "nothing launched: no window, no indicator, and a frame and a capture to compare against"
AURORA_APPS_EXTRA=$FX "$ROOT/aurora-up" --run "$RUN" --no-build --no-haiku --no-windows >"$RUN.up.log" 2>&1 & UPPID=$!; UP=1
waitf "aurora-up ready" test -f "$RUN/ready"
sed -i 's/^mode = "shader"/mode = "solid"/; s/^clock = true/clock = false/' "$RUN/theme/aurora-glass.toml"; sleep 1
source "$RUN/env"
waitf "the indicator's bus thread" grep -q 'indicator: bus snapshot held=0 up=true' "$RUN/compositor.log"
waitf "the dock to learn the apps" grep -q 'dock: registry.apps@v1 -> Ok' "$RUN/compositor.log"
for k in approval.key powerbox.key; do [ -s "$KEYS/$k" ] && ok "the host has $KEYS/$k ($(stat -c %s "$KEYS/$k") bytes): the key checks below are about something that exists" || fail "no $k"; done
KEYSUMS=$(sha256sum "$KEYS/approval.key" "$KEYS/powerbox.key" | cut -d' ' -f1 | tr '\n' ' ')
eq "$(controller apps | python3 -I -c 'import json,sys; print([a["launchable"] for a in json.load(sys.stdin)["apps"] if a["id"]=="'$APP'"][0])')" "True" "$APP is launchable (its binary is built)"
eq "$(windows | wc -l | tr -d ' ')" "0" "no window is open"
eq "$(layouts)" "0" "the compositor has laid out no indicator"
own "$WORK/own-before.png"; cap "$WORK/cap-before.png"
ok "baseline frame and capture taken ($($PNG info "$WORK/own-before.png"))"

step "a dock click" "the app runs in a domain: subordinate uid, aurora-domains cgroup, device:wayland held, and a window with real pixels"
IDS=$(clog | sed 's/\x1b\[[0-9;]*m//g' | grep 'dock: registry.apps@v1 -> Ok' | tail -1 | sed 's/.*app(s): \(\[.*\]\).*/\1/')
IDX=$(python3 -I -c 'import json,sys; ids=json.loads(sys.argv[1]); print(ids.index(sys.argv[2]))' "$IDS" "$APP") || fail "$APP is not in the dock's list: $IDS"
RECT=$(clog | grep 'dock: layout' | tail -1 | sed 's/\x1b\[[0-9;]*m//g' | sed 's/.*items=\[\(.*\)\]/\1/' | tr ';' '\n' | sed -n "$((IDX+1))p")
[ -n "$RECT" ] || fail "dock item $IDX has no rectangle (the dock is clipped): $(clog | grep 'dock: layout' | tail -1)"
IFS=, read -r RX RY RW RH <<<"$RECT"
CX=$((RX+RW/2)); CY=$((RY+RH/2))
ok "the dock lists $APP as item $IDX at $RECT; clicking its centre ($CX,$CY) with the compositor's own pointer"
inp "pointer $CX $CY"; sleep 0.3; inp "click"
waitf "the dock to report the launch" grep -q "dock: launched windowed app \"$APP\"" "$RUN/compositor.log"
LL=$(clog | grep "dock: launched windowed app \"$APP\"" | tail -1 | sed 's/\x1b\[[0-9;]*m//g'); echo "   | ${LL#*dock: }" | cut -c1-200
DOM=$(sed -n 's/.* in domain \([a-z0-9_-]*\) .*/\1/p' <<<"$LL")
case "$DOM" in win-controls-*) ok "the controller put it in domain $DOM (named for the launch, not shared with the app's verbs)";; *) fail "no domain in: $LL";; esac
case "$LL" in *"isolated=true"*) ok "the controller says isolated=true";; *) fail "not isolated: $LL";; esac
case "$LL" in *'"device:wayland"'*) ok "its grants: $(sed -n 's/.*grants \(\[[^]]*\]\).*/\1/p' <<<"$LL"), and nothing else (the manifest declares nothing)";; *) fail "no wayland grant: $LL";; esac
eq "$(sed -n 's/.*grants \(\[[^]]*\]\).*/\1/p' <<<"$LL")" '["device:wayland"]' "the grant list is exactly the display"
PIDS=$(inpids "$DOM"); [ -n "$PIDS" ] || fail "no process is in cgroup .../aurora-domains/$DOM"
APPPID=; for p in $PIDS; do [ "$(cat /proc/$p/comm)" = iface_controls ] && APPPID=$p; done
[ -n "$APPPID" ] && ok "iface_controls is pid $APPPID, in cgroup .../aurora-domains/$DOM (the kernel's /proc/$APPPID/cgroup says so)" || fail "iface_controls is not among the domain's pids: $PIDS"
AUID=$(stat -c %u /proc/$APPPID); [ "$AUID" != "$(id -u)" ] && ok "it runs as uid $AUID, not ours ($(id -u)): the domain's own subordinate uid" || fail "it runs as our uid"
case "$(domain show "$DOM")" in *"device wayland"*) ok "domain show lists the wayland grant";; *) fail "no wayland grant on the domain: $(domain show "$DOM")";; esac
waitf "the window to map" bash -c "grep -q 'deco: layout.*domain=$DOM' '$RUN/compositor.log'"
sleep 0.8   # the bar's ease is over
WL=$(windows | grep "|$DOM|"); [ -n "$WL" ] && ok "the compositor has one window for it: $WL" || fail "the compositor has no window attributed to $DOM: $(windows)"
IFS='|' read -r WT WD WX WY WW WH <<<"$WL"
DL=$(clog | grep 'deco: layout' | grep "domain=$DOM" | tail -1 | sed 's/\x1b\[[0-9;]*m//g'); BAR=$(field "$DL " bar); CLOSE=$(field "$DL " close)
IFS=, read -r BX BY BW BH <<<"$BAR"
inp "pointer 900 300"; sleep 0.5   # off the dock: its hover highlight is a change too
cap "$WORK/cap-win.png"
eval "$($PNG diffrect "$WORK/cap-before.png" "$WORK/cap-win.png" $BX $BY $BW $((BH+WH)) | sed 's/\(.*\) \(.*\)/IN=\1 OUT=\2/')"
[ "$IN" -gt 5000 ] && ok "the capture changed by $IN pixels inside the window and its bar ($BAR + $WW x $WH)" || fail "the frame barely changed in the window's rectangle ($IN)"
eq "$OUT" "0" "and by 0 pixels anywhere else: the window is all there is"
C=$($PNG colors "$WORK/cap-win.png" $WX $WY $WW $WH); [ "$C" -ge 8 ] && ok "the window's client area holds $C distinct colours: real controls, drawn" || fail "the window is blank ($C colours)"
[ "$(clog | grep -c "refusing ext_data_control to pid $APPPID")" -ge 1 ] && ok "the compositor also refused it the clipboard, by the same cgroup read" || fail "no clipboard refusal for pid $APPPID"

step "the capability indicator lights for it" "it is driven by a handle busd holds and the controller announced; a screen capture cannot see it; it was dark for every dock app before this change"
waitf "the indicator's layout" bash -c "grep -q 'indicator: layout.*$DOM' '$RUN/compositor.log'"; sleep 0.6
L=$(clog | grep 'indicator: layout' | tail -1 | sed 's/\x1b\[[0-9;]*m//g'); echo "   | ${L#*indicator: }" | cut -c1-230
ROWS=$(sed -n 's/.*rows=\[\([^]]*\)\].*/\1/p' <<<"$L")
eq "$ROWS" "Display access|$DOM|attested" "the indicator's row names the domain busd stamped and says busd attests it"
DOT=$(field "$L" dot_rgb); DOTRGB=${DOT//,/ }
own "$WORK/own-lit.png"; cap "$WORK/cap-lit.png"
N=$($PNG count "$WORK/own-lit.png" $DOTRGB); [ "$N" -ge 30 ] && ok "the compositor's own frame has $N pixels of the dot colour ($DOT)" || fail "no dot in the compositor's frame ($N)"
eq "$($PNG count "$WORK/cap-lit.png" $DOTRGB)" "0" "a Wayland capture at the same moment has none: a screen recorder cannot record the evidence"
SNAP=$(clog | grep 'indicator: bus snapshot held=1' | tail -1 | sed 's/\x1b\[[0-9;]*m//g'); HCAP=$(sed -n 's/.*device:wayland@[^#]*#\(cap:[0-9a-f]*\).*/\1/p' <<<"$SNAP")
[ -n "$HCAP" ] && ok "the grant on screen is $HCAP" || fail "no cap id in: $SNAP"
HJ=$(handles); eq "$(python3 -I -c 'import json,sys; h=[x for x in json.load(sys.stdin)["handles"] if x["id"]=="'$HCAP'"]; print(h[0]["resource"] if h else "MISSING")' <<<"$HJ")" "device:wayland" "busd's own handle table holds $HCAP, for device:wayland"
python3 -I -c 'import json,sys; h=[x for x in json.load(sys.stdin)["handles"] if x["id"]=="'$HCAP'"][0]; sys.exit(0 if not h["minter"].startswith("domain:") else 1)' <<<"$HJ" && ok "and its minter is not a domain: $(python3 -I -c 'import json,sys; print([x for x in json.load(sys.stdin)["handles"] if x["id"]=="'$HCAP'"][0]["minter"])' <<<"$HJ")" || fail "the handle was minted by a domain"

step "the compositor knows which domain a window is in" "it reads the kernel's word at connect; a host client that titles itself with the domain's name gets no attribution"
WAYLAND_DISPLAY=$AURORA_WAYLAND "$EX/testclient" --size 400x120 --title "$DOM" --rgb 10,200,60 >"$WORK/host.out" 2>&1 & HC=$!; JOBS_PIDS+=($HC)
waitf "the host client to map" grep -q mapped "$WORK/host.out"
sleep 0.9
H=$(windows | grep "^$DOM|-|"); [ -n "$H" ] && ok "a host client titled exactly \"$DOM\" is in the window list with domain '-': $H" || fail "host window not found: $(windows)"
HL=$(clog | sed 's/\x1b\[[0-9;]*m//g' | grep 'deco: layout' | grep -E "text=$DOM\$" | tail -1)
[ -n "$HL" ] && ok "its bar's layout line carries no domain= field" || fail "no layout line for the host window"
case "$HL" in *domain=*) fail "the host window was attributed: $HL";; esac
HBAR=$(field "$HL " bar); IFS=, read -r HBX HBY HBW HBH <<<"$HBAR"
cap "$WORK/cap-two.png"
ACC="233 191 59"
DN=$($PNG near "$WORK/cap-two.png" $ACC 8 $((BX+BW-190)) $((BY+4)) 190 $((BH-10)))
HN=$($PNG near "$WORK/cap-two.png" $ACC 8 $((HBX+HBW-190)) $((HBY+4)) 190 $((HBH-10)))
[ "$DN" -gt 600 ] && ok "the domain window's bar has a badge: $DN pixels of the accent colour at its right end" || fail "no badge on the domain window ($DN)"
eq "$HN" "0" "the host window's bar has none at the same place, although its title says the domain's name"
kill "$HC" 2>/dev/null; wait "$HC" 2>/dev/null; sleep 0.5
eq "$(windows | grep -vc "|$DOM|")" "0" "the host window is gone; the domain's is still up (a window with no domain was normal and worked)"

# everything that must be true once a launched window's app has ended: $1 domain, $2 cap id
gone() {
  local dom=$1 cap=$2 hj
  eq "$(inpids "$dom" | wc -l | tr -d ' ')" "0" "no process is left in the domain's cgroup"
  [ ! -d "$CGDIR/$dom" ] && ok "its cgroup directory is gone" || fail "the cgroup remains"
  case " $(domlist | tr '\n' ' ') " in *" $dom "*) fail "domain list still has $dom";; *) ok "domain list no longer has $dom";; esac
  [ ! -e "$AURORA_DOMAINS_DIR/$dom" ] && ok "its storage is gone ($AURORA_DOMAINS_DIR/$dom)" || fail "the domain's directory remains"
  hj=$(handles)
  eq "$(python3 -I -c 'import json,sys; print(len([x for x in json.load(sys.stdin)["handles"] if x["id"]=="'$cap'"]))' <<<"$hj")" "0" "busd no longer holds $cap"
  eq "$(python3 -I -c 'import json,sys; print(len([d for d in json.load(sys.stdin)["domains"] if d["identity"].split(":")[1]=="'$dom'"]))' <<<"$hj")" "0" "busd no longer lists the domain"
}
# the cap id the indicator is showing for domain $1
capof() { clog | sed 's/\x1b\[[0-9;]*m//g' | grep 'indicator: bus snapshot held=[1-9]' | grep "@$1#" | tail -1 | sed -n 's/.*device:wayland@[^#]*#\(cap:[0-9a-f]*\).*/\1/p'; }

step "it exits" "the close button; then the domain, its cgroup, its handle, its grant and its indicator are gone, and the frame is the baseline"
IFS=, read -r KX KY KW KH <<<"$CLOSE"
[ -n "$KX" ] && ok "the close button is at $CLOSE (from the compositor's layout line)" || fail "no close rectangle (CLOSE=$CLOSE, DL=$DL)"
sleep 1   # the compositor has finished with the host window that was just closed
inp "pointer $((KX+KW/2)) $((KY+KH/2))"; sleep 0.5; inp "click"
waitf "the app's window to go" no_win "$DOM"
ok "the compositor's window list no longer has the window (the app closed it)"
for _ in $(seq 80); do grep -q "window $APP (domain $DOM) ended" "$RUN/controllerd.log" && break; sleep 0.1; done
if grep -q "window $APP (domain $DOM) ended" "$RUN/controllerd.log"; then
  ok "and the app itself ended"
else
  # Haiku Controls (and Layout) sometimes close their window and keep running: the same happens with
  # --unsandboxed-windows, so it is the kits' shutdown, not the domain. Say so, then end it the other way.
  echo "   NOTE: $APP closed its window and did not quit within 8 s (a kits shutdown quirk, also seen without a domain: see step 12). Ending it with controller stop."
  controller stop --domain "$DOM" >/dev/null || fail "controller stop failed"
fi
E=$(grep "window $APP (domain $DOM) ended" "$RUN/controllerd.log" | tail -1); echo "   | ${E#*controllerd: }"
gone "$DOM" "$HCAP"
waitf "the indicator to clear" bash -c "grep 'indicator: bus snapshot' '$RUN/compositor.log' | tail -1 | grep -q 'held=0 up=true'"
sleep 0.5; own "$WORK/own-after.png"; cap "$WORK/cap-after.png"
eq "$($PNG diff "$WORK/own-before.png" "$WORK/own-after.png")" "0" "the compositor's own frame is pixel-identical to the baseline: no window, no dot"
eq "$($PNG diff "$WORK/cap-before.png" "$WORK/cap-after.png")" "0" "and so is a capture"
eq "$(windows | wc -l | tr -d ' ')" "0" "no window remains"

step "a second launch, closed by its own close button" "Haiku Text, launched by the same registry.launch@v1 the dock sends: close it, and everything goes (it quits by itself when the kits let it; see below)"
TR=$(controller launch org.aurora.haiku.text) || fail "launch failed: $TR"
TDOM=$(js domain <<<"$TR")
waitf "Text's window" has_win "$TDOM"
sleep 1.5
TCAP=$(capof "$TDOM"); [ -n "$TCAP" ] && ok "Text is attributed to $TDOM and the indicator holds $TCAP for it" || fail "the indicator does not hold a grant for $TDOM"
TCL=$(clog | sed 's/\x1b\[[0-9;]*m//g' | grep 'deco: layout' | grep "domain=$TDOM" | tail -1); IFS=, read -r TX TY TW TH <<<"$(field "$TCL " close)"
inp "pointer $((TX+TW/2)) $((TY+TH/2))"; sleep 0.5; inp "click"
waitf "Text's window to go" no_win "$TDOM"
ok "the compositor's window list no longer has Text's window"
for _ in $(seq 80); do grep -q "window org.aurora.haiku.text (domain $TDOM) ended" "$RUN/controllerd.log" && break; sleep 0.1; done
if grep -q "window org.aurora.haiku.text (domain $TDOM) ended" "$RUN/controllerd.log"; then
  ok "the app ended by itself after its close button was pressed"
else
  echo "   NOTE: Text closed its window and did not quit within 8 s (the kits' shutdown hang again: main thread in a futex wait, window already gone). Ending it with controller stop."
  controller stop --domain "$TDOM" >/dev/null || fail "controller stop failed"
fi
gone "$TDOM" "$TCAP"
waitf "the indicator to clear" bash -c "grep 'indicator: bus snapshot' '$RUN/compositor.log' | tail -1 | grep -q 'held=0 up=true'"

step "what a domain gets" "a fixture app declares one read path and one write path: the display and those two, nothing else, and not the keys"
PR=$(controller launch org.zz.probe) || fail "launch failed: $PR"
PDOM=$(js domain <<<"$PR"); GR=$(python3 -I -c 'import json,sys; print(" ".join(g["path"] for g in json.load(sys.stdin)["grants"]))' <<<"$PR")
echo "   | grants: $GR"
eq "$GR" "device:wayland $WORK/shared $WORK/out" "the grant list is the display, then the declared read path, then the declared write path"
waitf "the probe's report" test -s "$WORK/out/report"
R() { sed -n "s/^$1=//p" "$WORK/out/report"; }
cat "$WORK/out/report" | cut -c1-160 | sed 's/^/   | /'
[ "$(R uid)" != "$(id -u)" ] && ok "it ran as uid $(R uid), a subordinate uid" || fail "ran as our uid"
PP=; for p in $(inpids "$PDOM"); do [ "$(cat /proc/$p/comm)" = sleep ] && PP=$p; done
[ -n "$PP" ] && ok "and from the host, pid $PP (comm sleep, the app) is in cgroup .../aurora-domains/$PDOM" || fail "the probe's sleep is not in its cgroup: $(inpids "$PDOM")"
HU=$(stat -c %u /proc/$PP); [ "$HU" != "$(id -u)" ] && [ "$HU" != 0 ] && ok "the host sees it as uid $HU (the domain's user namespace calls it $(R uid)): neither ours nor root"  || fail "the host sees uid $HU"
eq "$(R wayland_display)" "/run/aurora-display/wayland-0" "WAYLAND_DISPLAY is the one bound socket"
eq "$(R socket)" "yes" "and it is there"
eq "$(R rundisplay)" "wayland-0 " "/run/aurora-display holds that socket and nothing else"
eq "$(R declared_read)" "declared-and-readable" "the declared read path is readable"
eq "$(R declared_write_to_ro)" "DENIED" "but not writable: read means read"
eq "$(R undeclared)" "DENIED" "a sibling of it, never declared, cannot be read"
eq "$(R undeclared_ls)" "0" "or listed"
eq "$(R home)" "/domain/data" "HOME is the domain's own directory, not the user's"
eq "$(R host_run)" "absent" "the host's run directory is not a path in the domain"
eq "$(R keys)" "0" "approval.key and powerbox.key read as 0 bytes"
[ "$(R control)" -gt 0 ] && ok "control: the same cat reads a file that is there (/etc/passwd, $(R control) bytes)" || fail "control failed: cat reads nothing in the domain"
HIT=0; for s in $KEYSUMS; do case " $(R key32) " in *" $s "*) HIT=1;; esac; done
eq "$HIT" "0" "no 32-byte file anywhere in the domain has either key's contents"
eq "$(R net | sed 's/ *$//')" "lo" "its only network interface is loopback: no network"
eq "$(R dri)" "0" "no /dev/dri (no GPU was declared)"
ok "(its environment is only: $(R env))"
[ -f "$WORK/out/report" ] && [ "$(stat -c %u "$WORK/out/report")" != "$(id -u)" ] && ok "the report file it wrote in the declared write path belongs to uid $(stat -c %u "$WORK/out/report")"
[ ! -e "$WORK/shared/new" ] && ok "and nothing appeared in the read-only path" || fail "wrote into a read grant"
controller stop org.zz.probe >"$WORK/stop.out" || fail "stop failed"
eq "$(js stopped.0 <"$WORK/stop.out")" "$PDOM" "controller stop ended it"
eq "$(inpids "$PDOM" | wc -l | tr -d ' ')" "0" "no process is left"
case " $(domlist | tr '\n' ' ') " in *" $PDOM "*) fail "domain list still has $PDOM";; *) ok "the domain is gone";; esac
eq "$(python3 -I -c 'import json,sys; print(len([x for x in json.load(sys.stdin)["handles"] if x["resource"].startswith("device:") or "'$WORK'" in x["resource"]]))' <<<"$(handles)")" "0" "and so are all of its handles on busd (display and both paths)"

step "a launch that fails" "says why, through launch_problem, and leaves no domain, no process and no handle"
CR=$(controller launch org.zz.crash 2>&1); RC=$?
[ $RC != 0 ] && ok "controller launch exited $RC" || fail "a crashing app launched: $CR"
echo "   | $(tr '\n' ' ' <<<"$CR" | cut -c1-200)"
case "$CR" in *"boom: cannot open the display"*) ok "the error carries the app's own stderr";; *) fail "no reason in: $CR";; esac
AJ=$(controller apps)
eq "$(python3 -I -c 'import json,sys; a=[x for x in json.load(sys.stdin)["apps"] if x["id"]=="org.zz.crash"][0]; print(a["launchable"])' <<<"$AJ")" "False" "registry.apps@v1 now reports it launchable=false"
LP=$(python3 -I -c 'import json,sys; a=[x for x in json.load(sys.stdin)["apps"] if x["id"]=="org.zz.crash"][0]; print(a["launch_problem"])' <<<"$AJ"); echo "   | launch_problem: $LP"
case "$LP" in "last launch failed: "*boom*) ok "with the reason on launch_problem, the channel the dock already reads";; *) fail "launch_problem: $LP";; esac
sleep 0.5
eq "$(domlist | grep -c '^win-crash')" "0" "no win-crash domain survives"
eq "$(python3 -I -c 'import json,sys; print(len([x for x in json.load(sys.stdin)["handles"] if x["resource"].startswith("device:")]))' <<<"$(handles)")" "0" "no device handle survives"
grep -q 'indicator: bus snapshot held=0' <(clog | tail -3) && ok "and the indicator holds nothing" || fail "the indicator holds something: $(clog | grep 'indicator: bus snapshot' | tail -1)"

step "the kits' shared memory in a domain" "the Haiku kits keep a segment named aurora-kits-<uid>-<ns>: does it survive being in a domain, and where does it end up?"
domain create wd-shm >/dev/null; domain grant wd-shm --device wayland >/dev/null
mkdir -p "$AURORA_DOMAINS_DIR/wd-shm/data/app"; cp "$IFACE" "$AURORA_DOMAINS_DIR/wd-shm/data/app/"
SH=$(domain run wd-shm -- /usr/bin/sh -c '/domain/data/app/iface_controls >/dev/null 2>&1 & sleep 3; echo "uid=$(id -u)"; echo "devshm=$(ls -ld /dev/shm | cut -d" " -f1,3,4)"; echo "access_w=$(python3 -c "import os; print(os.access(\"/dev/shm\", os.W_OK))" 2>&1 | tail -1)"; echo "touch=$(touch /dev/shm/probe 2>/dev/null && echo works || echo fails)"; echo "caps=$(sed -n "s/^CapEff:.//p" /proc/self/status | tr -d "\t")"; echo "devshm_has=$(ls /dev/shm | tr "\n" " ")"; echo "tmp_has=$(ls /tmp | tr "\n" " ")"; echo "size=$(stat -c %s /tmp/aurora-kits-* /dev/shm/aurora-kits-* 2>/dev/null | head -1)"; kill %1' 2>/dev/null)
echo "$SH" | sed 's/^/   | /'
SU=$(sed -n 's/^uid=//p' <<<"$SH"); SEG=aurora-kits-$SU-default
eq "$(sed -n 's/^access_w=//p' <<<"$SH")" "False" "access(2) tells the domain's uid that /dev/shm is not writable ($(sed -n 's/^devshm=//p' <<<"$SH"), root-owned)"
eq "$(sed -n 's/^touch=//p' <<<"$SH")" "works" "although a write there does succeed (the domain keeps CAP_DAC_OVERRIDE and CAP_FOWNER in its own user namespace: CapEff $(sed -n 's/^caps=//p' <<<"$SH"))"
case "$(sed -n 's/^tmp_has=//p' <<<"$SH")" in *"$SEG"*) ok "so the kits' own fallback applies (shm.c asks access(2): /dev/shm if writable, else /tmp), and the segment is /tmp/$SEG, $(sed -n 's/^size=//p' <<<"$SH") bytes, created by an app that ran and mapped a window";; *) fail "no kits segment in the domain: $SH";; esac
[ ! -e "/tmp/$SEG" ] && [ ! -e "/dev/shm/$SEG" ] && ok "and it is not on the host: the domain's /tmp is its own tmpfs, so two domains do not share kit ports" || fail "the segment leaked to the host"
domain destroy wd-shm >/dev/null 2>&1

step "shut the stack down" "nothing survives"
kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0
NEW=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEW" ] && ok "no new aurora-domains cgroups" || fail "stray cgroups: $NEW"

step "the escape hatch" "--unsandboxed-windows runs the same dock launch as a host process: loud, explicit, and invisible to the indicator"
"$ROOT/aurora-up" --run "$RUN2" --no-build --no-haiku --no-windows --unsandboxed-windows >"$RUN2.up.log" 2>&1 & UPPID=$!; UP=1
waitf "the second stack" test -f "$RUN2/ready"
sed -i 's/^mode = "shader"/mode = "solid"/; s/^clock = true/clock = false/' "$RUN2/theme/aurora-glass.toml"
source "$RUN2/env"
grep -q 'WARNING: --unsandboxed-windows: windowed apps launch as HOST PROCESSES' "$RUN2/controllerd.log" && ok "controllerd said so at startup: $(grep -m1 'unsandboxed-windows' "$RUN2/controllerd.log" | sed 's/.*WARNING: //' | cut -c1-110)..." || fail "no startup warning"
HR=$(controller launch $APP) || fail "launch failed: $HR"
eq "$(js isolated <<<"$HR")" "false" "the reply says isolated=false"
case "$(js warning <<<"$HR")" in UNSANDBOXED*) ok "and carries the warning: $(js warning <<<"$HR" | cut -c1-110)...";; *) fail "no warning in reply";; esac
grep -q "WARNING: UNSANDBOXED: $APP" "$RUN2/controllerd.log" && ok "controllerd logged it again for this launch" || fail "no per-launch warning"
HPID=; for p in /proc/[0-9]*; do [ "$(cat $p/comm 2>/dev/null)" = iface_controls ] && tr '\0' '\n' <$p/environ 2>/dev/null | grep -qx "XDG_RUNTIME_DIR=$RUN2/xdg" && HPID=${p#/proc/}; done
[ -n "$HPID" ] && ok "iface_controls is pid $HPID" || fail "no iface_controls for this stack"
EXTRA_PIDS+=($HPID)
eq "$(stat -c %u /proc/$HPID)" "$(id -u)" "it runs as OUR uid"
case "$(cat /proc/$HPID/cgroup)" in *aurora-domains*) fail "it is in a domain";; *) ok "and in no domain cgroup: $(cat /proc/$HPID/cgroup)";; esac
sleep 1.5
eq "$(grep -c 'indicator: layout' "$RUN2/compositor.log")" "0" "and the indicator shows nothing for it: the cost of the hatch, in plain sight"
kill "$HPID" 2>/dev/null
kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0
NEW=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEW" ] && ok "no stray cgroups" || fail "stray cgroups: $NEW"

echo
echo "NOT PROVEN BY THIS DEMO (read decisions 0012 and 0017):"
echo "  - that device:wayland is narrow: it is everything the Wayland protocol allows a client (decision 0014)."
echo "  - that a title bar badge cannot be imitated by a client in its OWN window area: a client can draw anything there."
echo "  - that a windowed app can do its job in a domain: Files sees an empty home and cannot run aurora; that is the sandbox, not a pass."
echo "  - GPU use, network use and per-domain resource attribution for a window."
echo "  - that the indicator is visible to a person on a real display: it was read from pixels, not looked at."
echo
echo "passed $PASS checks in $STEPS steps"
