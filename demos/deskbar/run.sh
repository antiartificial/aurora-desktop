#!/usr/bin/env bash
# The Deskbar (decision 0021), against the REAL stack: busd, controllerd and the compositor are the
# real ones, and the window that proves the numbers is a real Wayland client running inside a real
# bubblewrap domain (the way decision 0014's demo maps one), in the domain's real cgroup.
#
# Proved here, each read from the compositor's own `deskbar: layout` log line (the words it
# rasterised), from decoded pixels of a client capture (aurora-capture), and from /proc and cgroupfs:
#   1. no windows              -> no Deskbar
#   2. two host-process apps   -> a box, grouped by application, each "not in a domain", no numeral
#   3. a click on an app       -> it opens to its windows (ease-out); a click on a window raises it
#   4. hover                   -> nothing outside the box restyles; the box is IN a client capture
#   5. a real domain's window  -> its row carries the CPU and process figures read from ITS cgroup,
#                                 the host apps' rows still carry none
#   6. real net.conn events    -> a refused host appears in the domain's row, with the word "refused"
#   7. what the cgroup cannot give (memory, when the controller is not delegated) is NOT drawn
#
# Not proven here: the network COUNTS (the proxy flushes a summary every 30 s; the demo does not wait),
# and anything on a real display (this reads pixels and logs; nobody looked at it).
#
# Needs: go, cargo, bwrap, socat, python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
RUN=$(mktemp -d /tmp/aurora-deskbar.XXXXXX); WORK=$(mktemp -d /tmp/aurora-deskbar-work.XXXXXX)
PNG="python3 -I $ROOT/demos/full-stack/pngtool.py"
EX=$ROOT/compositor/target/release/examples
UP=0; UPPID=; STEPS=0; PASS=0; FAILS=0; PIDS=()
CGDIR=/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/aurora-domains
cgnames() { ls "$CGDIR" 2>/dev/null | sort; }
CG0=$(cgnames)
DOM=

step()  { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$1" "$2"; }
fail()  { printf '   FAIL: %s\n' "$*" >&2; FAILS=$((FAILS+1)); finish; }
ok()    { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
eq()    { [ "$1" = "$2" ] && ok "$3 (= '$2')" || fail "$3: got '$1', want '$2'"; }
waitf() { local d=$1; shift; for _ in $(seq 300); do "$@" 2>/dev/null && return 0; sleep 0.1; done; fail "timed out: $d"; }
inp()   { echo "$1" | socat - "UNIX-CONNECT:$AURORA_INPUT_SOCK" >/dev/null; }
ask()   { echo "$1" | socat - "UNIX-CONNECT:$AURORA_INPUT_SOCK"; }
cap()   { "$AURORA_CAPTURE" --display "$AURORA_WAYLAND" -o "$1" >/dev/null 2>&1 || fail "capture failed"; }
clog()  { cat "$RUN/compositor.log"; }
# The last settled Deskbar layout as JSON: {"box":[x,y,w,h],"rows":[{"kind","rect":[x,y,w,h] (absolute),"text","meta"}]}
layout() { clog | python3 -I -c '
import sys, json, re
ls = [l for l in sys.stdin if "deskbar: layout " in l]
if not ls: sys.exit(1)
b = ls[-1].split("deskbar: layout ", 1)[1].rstrip("\n")
box = [int(v) for v in re.search(r"box=([-0-9,]+)", b).group(1).split(",")]
rows = []
for r in b.split("rows=[", 1)[1].rsplit("]", 1)[0].split(";"):
    if not r: continue
    k, rect, text, meta = (r.split("|", 3) + ["", ""])[:4]
    x, y, w, h = [int(v) for v in rect.split(",")]
    rows.append({"kind": k, "rect": [box[0] + x, box[1] + y, w, h], "text": text, "meta": meta})
print(json.dumps({"box": box, "rows": rows}))'; }
# jq-free: lq 'expr over d'
lq()    { layout | python3 -I -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
click_row() { # kind text
  local c; c=$(lq "[(r['rect'][0]+r['rect'][2]//2, r['rect'][1]+r['rect'][3]//2) for r in d['rows'] if r['kind']=='$1' and r['text']=='$2'][0]") || fail "no $1 row '$2'"
  c=${c//[(),]/}; inp "pointer $c"; inp click
}
state() { ask state | sed 's/^ok //'; }
js()    { python3 -I -c 'import json,sys; d=json.load(sys.stdin)
for k in sys.argv[1].split("."): d=d[int(k)] if isinstance(d,list) else d[k]
print(json.dumps(d) if isinstance(d,(list,dict)) or d is None else (str(d).lower() if isinstance(d,bool) else d))' "$1"; }

finish() {
  echo
  echo "NOT PROVEN BY THIS DEMO (read decision 0021):"
  echo "  - network counts: the proxy sends a summary every 30 s and this run does not wait for one."
  echo "  - memory: this machine's systemd user manager delegates only the pids controller, so a domain has no"
  echo "    memory.current to read (step 8 checks that the Deskbar then draws no memory figure)."
  echo "  - that it looks right on a real display: pixels and logs were read, nobody looked."
  echo
  echo "$PASS passed, $FAILS failed ($STEPS steps)"
  [ "$FAILS" = 0 ] && exit 0 || exit 1
}
cleanup() {
  [ -n "${KEEP:-}" ] && mkdir -p "$KEEP" && cp "$WORK"/*.png "$KEEP"/ 2>/dev/null
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  [ $UP = 1 ] && { kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0; }
  [ -n "${AURORA_DOMAINS_DIR:-}" ] && domain destroy "$DOM" >/dev/null 2>&1
  rm -rf "$WORK" "$RUN" "$RUN.up.log" 2>/dev/null
}
trap cleanup EXIT

step "build and bring the stack up" "the real stack in its own run dir; the compositor is told where busd is, which is all the Deskbar's network feed needs"
"$ROOT/aurora-up" --build-only --no-haiku >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; fail "build failed"; }
(cd "$ROOT/compositor" && cargo build --release >"$WORK/cargo.log" 2>&1) || { cat "$WORK/cargo.log"; fail "compositor build failed"; }
[ -x "$EX/testclient" ] || fail "no $EX/testclient"
"$ROOT/aurora-up" --run "$RUN" --no-build --no-haiku --no-windows >"$RUN.up.log" 2>&1 & UPPID=$!; UP=1
waitf "aurora-up ready" test -f "$RUN/ready"
# A still wallpaper and no clock, so two frames differ only if something real changed.
sed -i 's/^mode = "shader"/mode = "solid"/; s/^clock = true/clock = false/' "$RUN/theme/aurora-glass.toml"; sleep 1
source "$RUN/env"
waitf "the Deskbar's bus thread to connect" grep -q 'deskbar: bus snapshot up=true' "$RUN/compositor.log"
ok "stack up; the compositor's Deskbar is subscribed to busd (net.conn, net.summary)"

step "no windows, no Deskbar" "an empty box is clutter: with nothing to list it is not drawn at all"
eq "$(state | js deskbar)" "null" "the compositor reports no Deskbar"
cap "$WORK/empty.png"; ok "baseline capture taken ($($PNG info "$WORK/empty.png"))"

step "two host-process apps" "grouped by application; an app that is not in a domain says so and carries no figure"
for spec in "org.demo.alpha|alpha one|200,40,200" "org.demo.alpha|alpha two|40,40,200" "org.demo.beta|beta one|200,200,40"; do
  IFS='|' read -r app title rgb <<<"$spec"
  env XDG_RUNTIME_DIR="$XDG_RUNTIME_DIR" WAYLAND_DISPLAY="$AURORA_WAYLAND" "$EX/testclient" --size 300x200 --app-id "$app" --title "$title" --rgb "$rgb" >"$WORK/$title.out" 2>&1 & PIDS+=($!)
  waitf "$title to map" grep -q mapped "$WORK/$title.out"
done
waitf "the Deskbar" bash -c "grep -q 'deskbar: layout' '$RUN/compositor.log'"; sleep 0.6
L=$(layout); echo "   | $(python3 -I -c 'import json,sys; d=json.loads(sys.argv[1]); print("box", d["box"], [(r["kind"], r["text"], r["meta"]) for r in d["rows"]])' "$L")"
eq "$(lq "[r['text'] for r in d['rows'] if r['kind']=='app']")" "['org.demo.alpha', 'org.demo.beta']" "two application rows, three windows folded into them"
eq "$(lq "d['rows'][0]['text']")" "2 apps · 3 windows" "the header counts them"
eq "$(lq "sorted(set(r['meta'] for r in d['rows'] if r['kind']=='app'))")" "['not in a domain']" "every host app says 'not in a domain'"
eq "$(lq "any(c.isdigit() or c=='%' for r in d['rows'] if r['kind']=='app' for c in r['meta'])")" "False" "and no app row carries a numeral or a percentage"
cap "$WORK/apps.png"
BOX=$(lq "d['box']"); eq "$(state | js deskbar)" "$BOX" "the compositor and the log agree where the box is"
D=$($PNG diff "$WORK/empty.png" "$WORK/apps.png"); [ "$D" -gt 5000 ] && ok "the box is in a client capture (chrome in the scene, like the dock): $D pixels differ from the empty desktop" || fail "capture unchanged ($D)"

step "open an application, raise a window" "a click opens an app to its windows on an ease-out; a click on a window raises and focuses it"
H0=$(lq "d['box'][3]"); BEFORE_ORDER=$(state | js windows | python3 -I -c 'import json,sys; print([w["title"] for w in json.load(sys.stdin)])')
click_row app org.demo.alpha; sleep 0.8
eq "$(lq "[r['text'] for r in d['rows'] if r['kind']=='win']")" "['alpha one', 'alpha two']" "alpha opened to its two windows"
H1=$(lq "d['box'][3]"); [ "$H1" -gt "$H0" ] && ok "the box grew ($H0 -> $H1 px)" || fail "the box did not grow"
cap "$WORK/open.png"
# 'alpha one' is the first window and the lowest; clicking it must bring it to the top
click_row win "alpha one"; sleep 0.5
AFTER=$(state | js windows | python3 -I -c 'import json,sys; print(json.load(sys.stdin)[-1]["title"])')
eq "$AFTER" "alpha one" "the window clicked is now on top of the stack (was: $BEFORE_ORDER)"
grep -q 'deskbar: raise window' "$RUN/compositor.log" && ok "the compositor logged the raise" || fail "no raise logged"
click_row app org.demo.alpha; sleep 0.6
eq "$(lq "len([r for r in d['rows'] if r['kind']=='win'])")" "0" "a second click closes it again"

step "hover restyles nothing outside the box" "the full-stack capture assertion has broken on animated wallpapers and on focus restyles; this is bounded to what the Deskbar does"
inp "pointer 600 700"; sleep 0.5; cap "$WORK/rest.png"
C=$(lq "[(r['rect'][0]+r['rect'][2]//2, r['rect'][1]+r['rect'][3]//2) for r in d['rows'] if r['kind']=='app'][0]"); C=${C//[(),]/}
inp "pointer $C"; sleep 0.6; cap "$WORK/hover.png"
BX=$(lq "d['box'][0]"); BY=$(lq "d['box'][1]"); BW=$(lq "d['box'][2]"); BHH=$(lq "d['box'][3]")
OUT=$($PNG diffrect "$WORK/rest.png" "$WORK/hover.png" "$BX" "$BY" "$BW" "$BHH")
set -- $OUT
[ "$1" -gt 50 ] && ok "hovering a row changed $1 pixels inside the box (the wash)" || fail "hover changed only $1 pixels in the box"
eq "$2" "0" "and $2 pixels outside it: no title bar, no wallpaper, nothing a client can see restyled"
inp "pointer 600 700"

step "a REAL domain's window" "a Wayland client inside a bubblewrap domain: its row carries what ITS cgroup says; the host apps' rows still carry nothing"
# The controller creates and registers the domain with busd (a hand-made one is not registered, and busd then refuses to publish
# network events for it: decision 0015). plain@v1 runs the probe app once and leaves the registered domain behind.
genimage "$WORK/in.png"
aurora open "$WORK/in.png" --verb plain@v1 --app org.aurora.devprobe </dev/null >"$WORK/plain.out" 2>&1 || { cat "$WORK/plain.out"; fail "plain@v1 failed"; }
controller provenance "$WORK/in.plain.png" >"$WORK/plain.prov.json" || fail "no provenance for plain@v1"
DOM=$(python3 -I -c 'import json,sys; print(json.load(sys.stdin)["domain"].split(":")[1])' <"$WORK/plain.prov.json")
ok "the controller made and registered domain $DOM"
domain grant "$DOM" --device wayland >/dev/null; domain grant "$DOM" "$EX" --ro >/dev/null
domain grant "$DOM" --net allowed.test >/dev/null
# One run: the window, and two connections through the domain's own proxy (one to the granted host, one not).
domain run "$DOM" -- /usr/bin/sh -c "'$EX/testclient' --size 300x200 --app-id org.demo.boxed --title 'boxed one' --rgb 40,200,200 & sleep 1.5; curl -s -m 3 -o /dev/null http://refused.example/; curl -s -m 3 -o /dev/null http://allowed.test/; wait" >"$WORK/dom.out" 2>"$WORK/dom.err" & DPID=$!; PIDS+=($DPID)
waitf "the domain's window to map" grep -q mapped "$WORK/dom.out"
CP=$(ps -o pid= -C testclient | tr -d ' ' | while read -r p; do grep -q "aurora-domains/$DOM" "/proc/$p/cgroup" 2>/dev/null && echo "$p"; done | head -1)
[ -n "$CP" ] && ok "the window's client is pid $CP, in cgroup .../aurora-domains/$DOM (uid $(stat -c %u /proc/$CP), not ours): a domain, not a host process" || fail "no testclient in the $DOM cgroup"
# a CPU share needs two readings a second apart
for _ in $(seq 100); do lq "[r['meta'] for r in d['rows'] if r['kind']=='app' and r['text']=='org.demo.boxed'][0]" 2>/dev/null | grep -q 'cpu ' && break; sleep 0.2; done
META=$(lq "[r['meta'] for r in d['rows'] if r['kind']=='app' and r['text']=='org.demo.boxed'][0]") || fail "no row for the domain's app"
echo "   | domain row meta: $META"
case "$META" in "cpu "*) ok "the domain's row has a CPU figure";; *) fail "no CPU figure on the domain's row: $META";; esac
case "$META" in *" proc"*) ok "and a process count (pids.current: $(cat "$CGDIR/$DOM/pids.current"))";; *) fail "no process figure: $META";; esac
CPU=${META#cpu }; CPU=${CPU%%%*}
USAGE=$(sed -n 's/^usage_usec //p' "$CGDIR/$DOM/cpu.stat"); [ -n "$USAGE" ] && ok "and it is read from $CGDIR/$DOM/cpu.stat (usage_usec $USAGE), a file about that domain alone" || fail "cpu.stat unreadable"
eq "$(lq "sorted(set(r['meta'] for r in d['rows'] if r['kind']=='app' and r['text']!='org.demo.boxed'))")" "['not in a domain']" "while the host apps' rows in the same box still say 'not in a domain'"
cap "$WORK/domain.png"

step "real net.conn events reach the row" "the proxy's own events for the domain, over the real bus, stamped by busd; a refused host is shown at once"
waitf "the Deskbar to have seen the domain's connections" bash -c "grep -q 'deskbar: bus snapshot up=true domains=[1-9]' '$RUN/compositor.log'"
for _ in $(seq 50); do lq "[r['meta'] for r in d['rows'] if r['kind']=='app' and r['text']=='org.demo.boxed'][0]" | grep -q 'refused' && break; sleep 0.2; done
META=$(lq "[r['meta'] for r in d['rows'] if r['kind']=='app' and r['text']=='org.demo.boxed'][0]"); echo "   | domain row meta: $META"
case "$META" in *"net 2 hosts"*"1 refused"*) ok "the row says 'net 2 hosts' and '1 refused' (refused.example refused, allowed.test attempted)";; *) fail "no network line: $META";; esac
click_row app org.demo.boxed; sleep 0.8
echo "   | open rows: $(lq "[(r['text'], r['meta']) for r in d['rows'] if r['kind']=='info']")"
eq "$(lq "[r['text'] for r in d['rows'] if r['kind']=='info'][1]")" "refused.example" "opened, the refused host is the first host listed"
eq "$(lq "[r['meta'] for r in d['rows'] if r['kind']=='info' and r['text']=='refused.example'][0]")" "refused" "and it carries the word 'refused', not only a colour"
cap "$WORK/net.png"

step "what the cgroup cannot give is not drawn" "this machine's user manager delegates only 'pids': there is no memory.current, so no memory figure may appear"
if [ -e "$CGDIR/$DOM/memory.current" ]; then
  case "$META" in *"mem "*) ok "memory.current exists and the row shows memory";; *) fail "memory.current exists but the row has no memory figure";; esac
else
  case "$META" in *mem*) fail "the row shows memory but $CGDIR/$DOM has no memory.current: $META";; *) ok "no memory.current in $CGDIR/$DOM, and the row draws no memory figure: it left it out rather than invent one";; esac
fi
HDR=$(lq "d['rows'][0]['meta']"); echo "   | header (machine-wide, labelled): $HDR"
case "$HDR" in "cpu "*|"mem "*|"") ok "the header carries only labelled machine figures ('$HDR')";; *) fail "unlabelled header figure: $HDR";; esac

step "shutdown leaves nothing behind" "no processes, run dir or cgroups survive"
kill "$CP" 2>/dev/null; wait "$DPID" 2>/dev/null
domain destroy "$DOM" >/dev/null 2>&1
kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0
NEW=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEW" ] && ok "no new aurora-domains cgroups" || fail "stray cgroups: $NEW"

finish
