#!/usr/bin/env bash
# The capability-in-use indicator (decision 0016), against REAL grants: busd,
# controllerd and the compositor are the real ones, the domain is a real
# bubblewrap domain, and the grant is the one the controller mints and revokes
# for a verb that lists device:wayland (decision 0014).
#
# Proved here, each read from decoded pixels of the compositor's own frame
# (`dump`), of a client capture (aurora-capture), and from the compositor's log:
#   1. nothing held            -> no indicator
#   2. a real grant is held    -> the dot is in the compositor's frame, names the
#                                 domain busd attests, and the cap id on screen is
#                                 the cap id in the provenance record
#   3. the same moment         -> a client capture has NO dot and is pixel-identical
#                                 to the capture before the grant
#   4. the grant is revoked    -> the dot is gone and the frame is what it was
#   5. device:gpu is held      -> nothing, by the default class list (a theme choice)
#   6. a REAL domain forges    -> it mints its own device:wayland handle and publishes
#                                 the attribution topic over its own bus socket:
#                                 nothing lights
#
# And the weakest point, run rather than described:
#   7. a display client that is a host process (before decision 0022 that was every dock app; it is still anything not launched by the controller:
#      no grants) maps a fullscreen window and holds the display: NOTHING shows.
#
# Needs: go, cargo, bwrap, socat, python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
RUN=$(mktemp -d /tmp/aurora-capind.XXXXXX); WORK=$(mktemp -d /tmp/aurora-capind-work.XXXXXX)
PNG="python3 -I $ROOT/demos/full-stack/pngtool.py"
EX=$ROOT/compositor/target/release/examples
UP=0; UPPID=; STEPS=0; PASS=0; JOBS_PIDS=()
CGDIR=/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/aurora-domains
cgnames() { ls "$CGDIR" 2>/dev/null | sort; }
CG0=$(cgnames)

step()  { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$2" "$3"; }
fail()  { printf '   FAIL: %s\n' "$*" >&2; exit 1; }
ok()    { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
eq()    { [ "$1" = "$2" ] && ok "$3 (= '$2')" || fail "$3: got '$1', want '$2'"; }
waitf() { local d=$1; shift; for _ in $(seq 300); do "$@" 2>/dev/null && return 0; sleep 0.1; done; fail "timed out: $d"; }
js()    { python3 -I -c 'import json,sys; d=json.load(sys.stdin)
for k in sys.argv[1].split("."): d=d[int(k)] if isinstance(d,list) else d[k]
print(d if not isinstance(d,bool) else str(d).lower())' "$1"; }
inp()   { echo "$1" | socat - "UNIX-CONNECT:$AURORA_INPUT_SOCK" >/dev/null; }
cap()   { "$AURORA_CAPTURE" --display "$AURORA_WAYLAND" -o "$1" >/dev/null 2>&1 || fail "capture failed"; }
# the compositor's own frame, indicator included (a test affordance: --test-input dump)
own()   { rm -f "$1"; inp "dump $1"; waitf "the compositor to write its frame" test -s "$1"; }
clog()  { cat "$RUN/compositor.log"; }
layouts() { clog | grep -c 'indicator: layout' || true; }
lastlayout() { clog | grep 'indicator: layout' | tail -1; }
field() { sed -n "s/.* $2=\([^ ]*\) .*/\1/p" <<<"$1" | head -1; }
dotrgb() { local c; c=$(field "$1" dot_rgb); echo "${c//,/ }"; }

# KEEP=DIR keeps every frame and capture the run took, to look at.
cleanup() {
  [ -n "${KEEP:-}" ] && mkdir -p "$KEEP" && cp "$WORK"/*.png "$KEEP"/ 2>/dev/null
  for p in "${JOBS_PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  [ $UP = 1 ] && { kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0; }
  [ -n "${AURORA_DOMAINS_DIR:-}" ] && for d in "${DOM:-}" capind-manual; do [ -n "$d" ] && domain destroy "$d" >/dev/null 2>&1; done
  rm -rf "$WORK" "$RUN" "$RUN.up.log" 2>/dev/null
}
trap cleanup EXIT

step 1 "build and bring the stack up" "the real stack in its own run dir; the compositor is told where busd is, which is all the indicator needs"
"$ROOT/aurora-up" --build-only --no-haiku >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; fail "build failed"; }
(cd "$ROOT/compositor" && cargo build --release >"$WORK/cargo.log" 2>&1) || { cat "$WORK/cargo.log"; fail "compositor build failed"; }
[ -x "$EX/testclient" ] || fail "no $EX/testclient"
"$ROOT/aurora-up" --run "$RUN" --no-build --no-haiku --no-windows >"$RUN.up.log" 2>&1 & UPPID=$!; UP=1
waitf "aurora-up ready" test -f "$RUN/ready"
# A still wallpaper and no clock, so two frames differ only if something real changed (the clock ticking over
# a minute between a baseline and a later capture cost one run 508 pixels).
sed -i 's/^mode = "shader"/mode = "solid"/; s/^clock = true/clock = false/; s/^system = true/system = false/' "$RUN/theme/aurora-glass.toml"; sleep 1
source "$RUN/env"
waitf "the indicator's bus thread to connect" grep -q 'indicator: bus snapshot held=0 up=true' "$RUN/compositor.log"
ok "stack up; the compositor's indicator is subscribed to busd and sees nothing held"
genimage "$WORK/in.png"
# create (and register with busd) the probe app's domain; this run holds no device
aurora open "$WORK/in.png" --verb plain@v1 --app org.aurora.devprobe </dev/null >"$WORK/plain.out" 2>&1 || { cat "$WORK/plain.out"; fail "plain@v1 failed"; }

step 2 "baseline" "nothing held: no indicator line, and the compositor's frame and a client capture to compare against"
eq "$(layouts)" "0" "the compositor has laid out no indicator"
own "$WORK/own-before.png"; cap "$WORK/cap-before.png"
ok "baseline frame and capture taken ($($PNG info "$WORK/own-before.png"))"

step 3 "a REAL domain forges the grant" "it mints its own device:wayland handle and publishes the attribution topic, over its own bus socket"
controller provenance "$WORK/in.plain.png" >"$WORK/plain.prov.json" || fail "no provenance for plain@v1"
DOM=$(js domain <"$WORK/plain.prov.json" | cut -d: -f2)
ok "the probe app's domain is $DOM"
BUSBIN=$ROOT/build/bin/bus; [ -x "$BUSBIN" ] || fail "no $BUSBIN"
domain grant "$DOM" "$BUSBIN" --ro >/dev/null
IDENT=$(bus inspect --json 2>/dev/null | python3 -I -c 'import json,sys; print([d["identity"] for d in json.load(sys.stdin)["domains"] if d["identity"].startswith("domain:'"$DOM"':")][0])') || fail "busd does not list $DOM"
ok "busd has $IDENT registered"
FM=$(domain run "$DOM" -- /usr/bin/env "$BUSBIN" send --socket /run/aurora/bus.sock bus bus.mint@v1 '{"resource":"device:wayland"}' 2>&1) || { echo "$FM"; fail "the domain could not mint over its own socket (so it cannot be the forger this step needs)"; }
FID=$(grep '^{' <<<"$FM" | python3 -I -c 'import json,sys; print(json.loads(sys.stdin.readline())["id"])' 2>/dev/null) || { echo "$FM"; fail "no handle id in: $FM"; }
ok "from INSIDE the domain a bus.mint@v1 for device:wayland succeeded: $FID (so a bus.cap.minted for that resource really exists, minted by a domain)"
domain run "$DOM" -- /usr/bin/env "$BUSBIN" pub --socket /run/aurora/bus.sock provenance.grant.held "{\"cap\":\"$FID\",\"resource\":\"device:wayland\",\"domain\":\"$IDENT\"}" >/dev/null 2>&1 \
  && ok "and it published provenance.grant.held for it, naming itself" || fail "the domain could not publish the attribution (the forgery this step needs)"
MINTLINE=$(bus log -json -topic bus.cap.minted 2>/dev/null | grep "$FID" | head -1)
[ -n "$MINTLINE" ] && ok "busd's own log shows the forged mint, minter $(sed -n 's/.*"minter":"\([^"]*\)".*/\1/p' <<<"$MINTLINE")" || fail "the forged mint is not in busd's log (so this step proves nothing)"
sleep 2
eq "$(layouts)" "0" "the compositor lays out NO indicator for the forged grant"
clog | grep 'indicator: bus snapshot' | tail -1 | grep -q "held=0" && ok "and its model holds nothing: $(clog | grep 'indicator: bus snapshot' | tail -1 | sed 's/.*snapshot //')" || fail "the compositor believes a forged grant: $(clog | grep 'indicator: bus snapshot' | tail -1)"
own "$WORK/own-forged.png"; eq "$($PNG diff "$WORK/own-before.png" "$WORK/own-forged.png")" "0" "the frame is pixel-identical to the baseline"
domain run "$DOM" -- /usr/bin/env "$BUSBIN" send --socket /run/aurora/bus.sock bus bus.revoke@v1 "{\"id\":\"$FID\"}" >/dev/null 2>&1 || true

step 4 "a real grant: lit while held" "controllerd mints device:wayland for hold@v1, composes it into the domain, runs it for 8 s, then revokes"
aurora open "$WORK/in.png" --verb hold@v1 --app org.aurora.devprobe </dev/null >"$WORK/hold.out" 2>&1 & HOLD=$!; JOBS_PIDS+=($HOLD)
waitf "the compositor to lay out the indicator" bash -c "grep -q 'indicator: layout' '$RUN/compositor.log'"
sleep 0.6   # the ease-out is over
L=$(lastlayout); echo "   | ${L#*indicator: }" | cut -c1-230
kill -0 $HOLD 2>/dev/null && ok "the invoke is still running: the grant is being held right now" || fail "the invoke already finished"
DOT=$(dotrgb "$L"); PILL=$(field "$L" pill); DOTBOX=$(field "$L" dot)
own "$WORK/own-held.png"; cap "$WORK/cap-held.png"
N=$($PNG count "$WORK/own-held.png" $DOT); [ "$N" -ge 30 ] && ok "the compositor's own frame has $N pixels of the dot colour ($DOT)" || fail "no dot in the compositor's frame ($N)"
D=$($PNG diff "$WORK/own-before.png" "$WORK/own-held.png"); [ "$D" -gt 100 ] && ok "its frame differs from the baseline by $D pixels (the pill)" || fail "frame unchanged ($D)"
eq "$($PNG count "$WORK/cap-held.png" $DOT)" "0" "a Wayland capture taken at the same moment has no pixel of the dot colour"
eq "$($PNG diff "$WORK/cap-before.png" "$WORK/cap-held.png")" "0" "and is pixel-identical to the capture before the grant: a screen recorder cannot record the evidence"
TEXT=$(sed -n 's/.* text="\([^"]*\)" rows=.*/\1/p' <<<"$L"); ROWS=$(sed -n 's/.*rows=\[\([^]]*\)\].*/\1/p' <<<"$L")
echo "   | pill text: $TEXT"; echo "   | row: $ROWS"
case "$ROWS" in "Display access|$DOM|attested") ok "the row names the domain busd stamped ($DOM) and says busd attests it";; *) fail "unexpected row: $ROWS";; esac
SNAP=$(clog | grep 'indicator: bus snapshot held=1' | tail -1); echo "   | ${SNAP#*snapshot }"
HCAP=$(sed -n 's/.*device:wayland@[^#]*#\(cap:[0-9a-f]*\).*/\1/p' <<<"$SNAP")
[ -n "$HCAP" ] && ok "the grant on screen is $HCAP" || fail "no cap id in the snapshot"
# the panel, by hovering
CX=$(( $(sed 's/,.*//' <<<"$PILL") + $(sed 's/[0-9]*,[0-9]*,\([0-9]*\),.*/\1/' <<<"$PILL") / 2 )); CY=$(( $(sed 's/[0-9]*,\([0-9]*\),.*/\1/' <<<"$PILL") + 12 ))
inp "pointer $CX $CY"; sleep 0.6
L2=$(lastlayout); [ "$(field "$L2" panel)" != none ] && ok "hovering the dot opens the detail panel ($(field "$L2" panel))" || fail "no panel on hover"
own "$WORK/own-panel.png"; cap "$WORK/cap-panel.png"
[ "$($PNG diff "$WORK/own-held.png" "$WORK/own-panel.png")" -gt 500 ] && ok "the panel is in the compositor's frame" || fail "panel did not draw"
eq "$($PNG diff "$WORK/cap-before.png" "$WORK/cap-panel.png")" "0" "and still not in the capture"
inp "pointer 10 700"
wait $HOLD && ok "the invoke finished" || { cat "$WORK/hold.out"; fail "hold@v1 failed"; }
controller provenance "$WORK/in.hold.png" >"$WORK/hold.prov.json" || fail "no provenance"
PCAP=$(python3 -I -c 'import json,sys; g=json.load(sys.stdin)["grants"]; print([x["cap"] for x in g if x.get("device")=="wayland"][0])' <"$WORK/hold.prov.json")
eq "$PCAP" "$HCAP" "the cap id the indicator was showing is the cap id in the provenance record of the run"
eq "$(js domain <"$WORK/hold.prov.json" | cut -d: -f2)" "$DOM" "and the record's domain is the one the indicator named"

step 5 "revoked: gone" "the controller revoked the handle; the dot clears and the frame is what it was"
waitf "the compositor to see the revoke" grep -q 'indicator: bus snapshot held=0 up=true' <(clog | tail -3)
sleep 0.4
own "$WORK/own-after.png"; cap "$WORK/cap-after.png"
eq "$($PNG count "$WORK/own-after.png" $DOT)" "0" "no pixel of the dot colour is left in the compositor's frame"
eq "$($PNG diff "$WORK/own-before.png" "$WORK/own-after.png")" "0" "the frame is pixel-identical to the baseline"
eq "$($PNG diff "$WORK/cap-before.png" "$WORK/cap-after.png")" "0" "and so is a capture"

step 6 "device:gpu is held: nothing, by the default class list" "the list is a theme choice (contracts/theme/aurora-glass.toml [indicator] classes), and the default is the display alone"
BEFORE=$(layouts)
aurora open "$WORK/in.png" --verb gpu@v1 --app org.aurora.devprobe </dev/null >"$WORK/gpu.out" 2>&1 || fail "gpu@v1 failed: $(cat "$WORK/gpu.out")"
sleep 1
clog | grep 'indicator: bus snapshot' | grep -q 'held=1 up=true device:gpu@' && ok "the compositor's model DID see the real device:gpu grant ($(clog | grep 'indicator: bus snapshot' | grep 'device:gpu@' | tail -1 | sed 's/.*snapshot //' | cut -c1-90))" || fail "the gpu grant never reached the indicator's model"
eq "$(layouts)" "$BEFORE" "and drew nothing for it"

step 7 "the weakest point, run: a process the controller did not launch holds the display" "a host-process client (how every dock app launched before decision 0022, and how anything the user starts still does) maps a fullscreen window: no grant, so no indicator"
XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR WAYLAND_DISPLAY=$AURORA_WAYLAND "$EX/testclient" --fullscreen --hidpi --rgb 10,200,60 >"$WORK/host.out" 2>&1 & HC=$!; JOBS_PIDS+=($HC)
waitf "the host client to map" grep -q mapped "$WORK/host.out"
sleep 1; BEFORE=$(layouts)
own "$WORK/own-host.png"
G=$($PNG count "$WORK/own-host.png" 10 200 60); [ "$G" -gt 900000 ] && ok "the host process owns the screen: $G of $(( $($PNG info "$WORK/own-host.png" | sed 's/ /*/') )) pixels are its own" || fail "the control failed: the client covers only $G pixels"
eq "$(layouts)" "$BEFORE" "and the compositor lays out NO indicator: it holds no grant, so there is nothing to show"
eq "$($PNG count "$WORK/own-host.png" $DOT)" "0" "and no pixel of the dot colour is in the compositor's frame, while an app that can see and inject into the display is running"
kill "$HC" 2>/dev/null; wait "$HC" 2>/dev/null

step 8 "the second gap, run: a grant made by hand" "domain grant --device wayland, with no controller in the loop, mints no handle on the bus, so the indicator never hears of it"
domain create capind-manual >/dev/null; domain grant capind-manual --device wayland >/dev/null
BEFORE=$(layouts); sleep 2
eq "$(domain run capind-manual -- /usr/bin/sh -c 'test -S /run/aurora-display/wayland-0 && echo socket' 2>/dev/null)" "socket" "that domain really does hold the compositor's socket"
eq "$(layouts)" "$BEFORE" "and the compositor lays out NO indicator for it"
domain destroy capind-manual >/dev/null 2>&1

step 9 "shutdown leaves nothing behind" "no processes, run dir or cgroups survive"
kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0
NEW=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEW" ] && ok "no new aurora-domains cgroups" || { domain destroy "$DOM" >/dev/null 2>&1; NEW=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEW" ] && ok "no new aurora-domains cgroups after destroying the probe domain" || fail "stray cgroups: $NEW"; }

echo
echo "NOT PROVEN BY THIS DEMO (read decision 0016):"
echo "  - any process that holds no grant: step 7 is the proof that it is invisible to the indicator."
echo "  - any grant that was not minted by the controller: step 8."
echo "  - that a camera or microphone shows: no such class exists yet."
echo "  - that the indicator is visible to a person on a real display: it was read from pixels, not looked at."
echo
echo "passed $PASS checks in $STEPS steps"
