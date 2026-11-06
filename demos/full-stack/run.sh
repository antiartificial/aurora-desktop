#!/usr/bin/env bash
# Aurora full-stack walkthrough: one story, every step asserted. Exits non-zero at
# the first thing that is wrong. Brings the stack up with aurora-up, drives it
# the way a person would, and shuts it down, then checks nothing was left behind.
# Captures are saved next to this script. Needs: go, cargo, bwrap, socat, python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
RUN=$(mktemp -d /tmp/aurora-demo.XXXXXX); WORK=$(mktemp -d /tmp/aurora-demo-work.XXXXXX)
PNG="python3 -I $HERE/pngtool.py"; STEPS=0; UP=0
CGDIR=/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/aurora-domains
cgnames() { ls "$CGDIR" 2>/dev/null | grep '^app-' | sort; }
CG0=$(cgnames)

say()  { printf '%s\n' "$*"; }
step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$1" "$2"; }
fail() { printf '   FAIL: %s\n' "$*" >&2; exit 1; }
ok()   { printf '   ok: %s\n' "$*"; }
need() { local d=$1; shift; "$@" >/dev/null 2>&1 && ok "$d" || fail "$d  [$*]"; }
has()  { case $1 in *"$2"*) ok "$3";; *) fail "$3 (no '$2' in: ${1:0:300})";; esac; }
key()  { echo "$1" | socat - "UNIX-CONNECT:$AURORA_INPUT_SOCK" >/dev/null; }
cap()  { "$AURORA_CAPTURE" --display "$AURORA_WAYLAND" -o "$1" >/dev/null 2>&1 || fail "capture failed"; }
waitf(){ local d=$1; shift; for _ in $(seq 200); do "$@" 2>/dev/null && return 0; sleep 0.1; done; fail "timed out: $d"; }
js()   { python3 -I -c 'import json,sys; d=json.load(sys.stdin)
for k in sys.argv[1].split("."): d=d[int(k)] if isinstance(d,list) else d[k]
print(d if not isinstance(d,bool) else str(d).lower())' "$1"; }

stop_stack() { [ $UP = 1 ] || return 0; UP=0; kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; }
trap 'stop_stack; rm -rf "$WORK" "$RUN" "$RUN.up.log"' EXIT

step "build and bring the stack up" "aurora-up builds busd, domain, controllerd, aurora, Lumen, the compositor and the Haiku app, and starts them in order"
"$ROOT/aurora-up" --build-only >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; fail "build failed"; }
ok "built (build/bin, compositor, capture, kits)"
"$ROOT/aurora-up" --run "$RUN" --dump-overlay "$RUN/overlay.png" --approval-timeout 30s --no-build >"$RUN.up.log" 2>&1 & UPPID=$!; UP=1
waitf "aurora-up ready" test -f "$RUN/ready"
source "$RUN/env"
sed -n '/== Aurora is up/,/logs/p' "$RUN.up.log" | sed 's/^/   | /'
need "busd socket exists" test -S "$AURORA_BUS_SOCK"
need "compositor trusted socket exists" test -S "$AURORA_TRUSTED_SOCK"
need "compositor wayland socket exists" test -S "$XDG_RUNTIME_DIR/$AURORA_WAYLAND"
INSPECT=$(bus inspect 2>&1); has "$INSPECT" registry "bus lists the controller's 'registry' service"
has "$INSPECT" haiku-ledger "bus lists the Haiku app's 'haiku-ledger' service"

step "the registry shows manifest-declared apps and verbs" "verbs come from manifests, filtered by type, with effect classes"
APPS=$(aurora apps); has "$APPS" org.aurora.lumen "aurora apps lists Lumen"
has "$APPS" "resize@v1, export@v1, histogram@v1" "with the verbs its manifest declares"
VERBS=$(aurora verbs --accepts image/png); has "$VERBS" resize@v1 "aurora verbs --accepts image/png lists resize@v1"
has "$VERBS" "DESTRUCTIVE" "and marks the destructive verb as such"

step "run Lumen in a domain" "a real resize, inside a bwrap domain that has its own uid"
"$ROOT/build/bin/genimage" "$WORK/photo.png"
[ "$($PNG info "$WORK/photo.png")" = "1200 800" ] && ok "input photo.png is 1200x800" || fail "bad input image"
OPEN=$(aurora open "$WORK/photo.png" --verb resize@v1 --arg width=300 </dev/null 2>&1); echo "$OPEN" | sed 's/^/   | /' | cut -c1-200
has "$OPEN" "isolated: true" "the controller reports the run was isolated"
[ "$($PNG info "$WORK/photo.resize.png")" = "300 200" ] && ok "photo.resize.png exists and is 300x200 (aspect kept)" || fail "output is not a 300x200 PNG"

step "provenance" "the record names the domain, the grants (source and cap id) and verifies against the daemon"
controller provenance "$WORK/photo.resize.png" >"$WORK/prov.json" || fail "no provenance on the output"
P=$(cat "$WORK/prov.json")
[ "$(js isolated <<<"$P")" = true ] && ok "provenance.isolated = true" || fail "not isolated"
DOM=$(js domain <<<"$P"); [ "$(js domain_attested <<<"$P")" = true ] && ok "domain $DOM is attested by the bus" || fail "domain not attested"
for i in 0 1; do ok "grant $i: $(js grants.$i.mode <<<"$P") $(js grants.$i.path <<<"$P") source=$(js grants.$i.source <<<"$P") $(js grants.$i.cap <<<"$P")"
  case "$(js grants.$i.cap <<<"$P")" in cap:*) ;; *) fail "grant $i has no cap id";; esac; done
DNAME=$(sed 's/^domain:\(.*\):[0-9a-f]*$/\1/' <<<"$DOM"); DUID=$(domain uid "$DNAME")
[ "$DUID" != "$(id -u)" ] && [ "$DUID" != 0 ] && ok "the domain's uid is $DUID (ours is $(id -u))" || fail "domain shares our uid"
VER=$(controller provenance "$WORK/photo.resize.png" --verify 2>&1); RC=$?
[ $RC = 0 ] && ok "controller provenance --verify: $(tail -1 <<<"$VER" | cut -c1-60)" || fail "verify exit $RC"

step "a destructive verb blocks on a prompt the compositor draws" "approval is a trusted overlay, excluded from captures, decided by a key"
aurora open "$WORK/photo.png" --verb copy@v1 --arg tag=demo </dev/null >/dev/null 2>&1 || fail "copy@v1 failed (creates the demo app's domain so the prompt can verify it)"
cap "$HERE/desktop-before-approval.png"; rm -f "$RUN/overlay.png"
aurora open "$WORK/photo.png" --verb delete@v1 --output "$WORK/gone.png" </dev/null >"$WORK/del.out" 2>&1 & DP=$!
waitf "the overlay to be drawn" test -f "$RUN/overlay.png"; sleep 0.5
kill -0 $DP 2>/dev/null && ok "the invoke is blocked, waiting for a human" || fail "invoke did not block"
[ ! -e "$WORK/gone.png" ] && ok "nothing has run: no output file yet" || fail "output exists before approval"
cap "$HERE/approval-client-capture.png"; cp "$RUN/overlay.png" "$HERE/approval-compositor-frame.png"
IN=$($PNG count "$HERE/approval-compositor-frame.png" 255 90 20); OUT=$($PNG count "$HERE/approval-client-capture.png" 255 90 20)
[ "$IN" -gt 1000 ] && ok "the compositor's own frame has the overlay ($IN border pixels)" || fail "overlay not in compositor frame ($IN)"
[ "$OUT" = 0 ] && ok "a Wayland capture taken at the same moment has none" || fail "overlay leaked into capture ($OUT)"
[ "$($PNG diff "$HERE/desktop-before-approval.png" "$HERE/approval-client-capture.png")" = 0 ] && ok "and is pixel-identical to the capture taken before the prompt" || fail "capture changed while overlay up"
key "key a"; wait $DP && ok "key 'a' pressed at the compositor: invoke allowed and finished" || { cat "$WORK/del.out"; fail "invoke failed after allow"; }
[ -e "$WORK/gone.png" ] && ok "output file now exists" || fail "no output after allow"
controller provenance "$WORK/gone.png" >"$WORK/prov2.json"; P2=$(cat "$WORK/prov2.json")
[ "$(js approval.route <<<"$P2")" = compositor ] && [ "$(js approval.decision <<<"$P2")" = allow ] && [ "$(js approval.key <<<"$P2")" = a ] \
  && ok "provenance records approval: route=compositor decision=allow key=a by=$(js approval.by <<<"$P2")" || fail "approval not recorded as compositor/allow"
ok "compositor's check of the domain against busd: domain_verified=$(js approval.domain_verified <<<"$P2") ($(js approval.domain_verify_detail <<<"$P2" | cut -c1-90))"

step "glass and the switcher, captured from the compositor" "real pixels: blur across an edge under a translucent window; the Flip 3D switcher on top"
THEME=$RUN/theme/aurora-glass.toml
sed -i 's/^enabled = false/enabled = true/' "$THEME"; sleep 1; cap "$HERE/glass-on.png"
sed -i 's/^enabled = true/enabled = false/' "$THEME"; sleep 1; cap "$HERE/glass-off.png"
sed -i 's/^enabled = false/enabled = true/' "$THEME"; sleep 1
ON=$($PNG profile "$HERE/glass-on.png" 350 260 340); OFF=$($PNG profile "$HERE/glass-off.png" 350 260 340)
say "   row y=350 across the window edge at x=300 (the glass window covers it): glass on: $ON; glass off: $OFF"
ONM=${ON#mids=}; ONM=${ONM%% *}; ONJ=${ON##*maxjump=}; OFFJ=${OFF##*maxjump=}
[ "$ONM" -ge 10 ] && [ "$ONJ" -lt 40 ] && [ "$OFFJ" -ge 100 ] && ok "with glass the edge is a smooth gradient, with it off a hard step: it is a blur" || fail "no blur gradient"
[ "$($PNG diff "$HERE/glass-on.png" "$HERE/glass-off.png")" -gt 1000 ] && ok "glass on/off frames differ in many pixels" || fail "glass frames identical"
key "down alt"; key "key Tab"; sleep 1; cap "$HERE/switcher-flip.png"
SW=$($PNG diff "$HERE/glass-on.png" "$HERE/switcher-flip.png"); [ "$SW" -gt 100000 ] && ok "Alt+Tab: the Flip 3D switcher changed $SW pixels of the frame" || fail "switcher not visible ($SW)"
key "key Escape"; key "up alt"; sleep 1; cap "$WORK/after.png"
[ "$($PNG diff "$HERE/glass-on.png" "$WORK/after.png")" = 0 ] && ok "Escape closes it: the desktop is back, pixel-identical" || fail "desktop not restored"

step "a Haiku application serves a bus verb" "a BApplication/BHandler is a bus service; the Go bus CLI calls it; identity is the daemon's"
R1=$(bus send haiku-ledger add@v1 '{"label":"coffee","cents":350}'); R2=$(bus send haiku-ledger add@v1 '{"label":"book","cents":1499}')
has "$R1" '"total":350' "add@v1 coffee -> $R1"; has "$R2" '"total":1849' "add@v1 book -> $R2"
R3=$(bus send haiku-ledger list@v1 '{}'); has "$R3" '"entries":[{"label":"coffee","cents":350},{"label":"book","cents":1499}]' "list@v1 -> both entries, in order"
E=$(bus send haiku-ledger frobnicate@v1 '{}' 2>&1 || true); has "$E" not_understood "an unknown verb comes back as a typed error, not a hang"

step "the session's log replays" "bus replay --check: causal order, subscriber state and capability grants hold on everything above"
REP=$(bus replay --check "$RUN/buslog" 2>&1); RC=$?; echo "$REP" | sed 's/^/   | /'
[ $RC = 0 ] && has "$REP" "replay check: PASS" "bus replay --check passes" || fail "replay check exit $RC"

step "shutdown leaves nothing behind" "no processes, domains, cgroups, sockets or temp files survive"
stop_stack
LEFT=$(pgrep -af "$RUN" | grep -v pgrep || true)
[ -z "$LEFT" ] && ok "no process mentions $RUN" || fail "stray processes: $LEFT"
need "run dir (sockets, domains, logs) removed" test ! -e "$RUN/bus.sock" -a ! -e "$RUN/domains"
NEW=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEW" ] && ok "no new aurora-domains cgroups" || fail "stray cgroups: $NEW"
need "no kits shared-memory left for this stack" test -z "$(ls /dev/shm | grep "aurora-kits-$(id -u)-up")"
say; say "captures in $HERE:"; ls -1 "$HERE"/*.png | sed 's/^/   /'
say; say "FULL-STACK WALKTHROUGH PASSED ($STEPS steps)"
