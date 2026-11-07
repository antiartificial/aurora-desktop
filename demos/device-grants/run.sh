#!/usr/bin/env bash
# Device grants (decision 0014): is a device node or the Wayland socket in a
# domain's namespace only when a grant puts it there?
#
# Real stack (busd, controllerd, compositor), real bubblewrap domains. Every
# claim is proved in BOTH directions and read from decoded output, not from an
# error string:
#
#   no grant  -> the node / socket is ABSENT from the namespace (we list the
#                directory and stat the path: it is not there to refuse us)
#   grant     -> it is present, and it is the very device (same major:minor)
#
# and the part that must stay shut stays shut: a wayland grant binds the one
# socket, not its parent, so approval.key and powerbox.key are not reachable.
#
# What this does NOT prove is printed at the end, and counted as UNPROVEN, not
# as a pass. With --strict an unproven assertion is a failure.
#
# Needs: go, cargo, bwrap, python3. A render node (/dev/dri/renderD*) for the
# GPU half; without one that half is reported UNPROVEN, not faked.
set -uo pipefail
STRICT=0; [ "${1:-}" = "--strict" ] && STRICT=1
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
RUN=$(mktemp -d /tmp/aurora-devgrants.XXXXXX); WORK=$(mktemp -d /tmp/aurora-devgrants-work.XXXXXX)
PNG="python3 -I $ROOT/demos/full-stack/pngtool.py"
EX=$ROOT/compositor/target/release/examples
UP=0; UPPID=; STEPS=0; PASS=0; UNPROVEN=()
CGDIR=/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/aurora-domains
cgnames() { ls "$CGDIR" 2>/dev/null | sort; }
CG0=$(cgnames)

step()  { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$1" "$2"; }
fail()  { printf '   FAIL: %s\n' "$*" >&2; exit 1; }
ok()    { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
unproven() { UNPROVEN+=("$*"); printf '   UNPROVEN: %s\n' "$*"; }
eq()    { [ "$1" = "$2" ] && ok "$3 (= '$2')" || fail "$3: got '$1', want '$2'"; }
waitf() { local d=$1; shift; for _ in $(seq 200); do "$@" 2>/dev/null && return 0; sleep 0.1; done; fail "timed out: $d"; }
js()    { python3 -I -c 'import json,sys; d=json.load(sys.stdin)
for k in sys.argv[1].split("."): d=d[int(k)] if isinstance(d,list) else d[k]
print(d if not isinstance(d,bool) else str(d).lower())' "$1"; }
# a domain's own view: run a sh -c script inside it
inside() { local d=$1; shift; domain run "$d" -- /usr/bin/sh -c "$*" 2>/dev/null; }

DOMS=(dg-none dg-gpu dg-wl dg-nowl)
cleanup() {
  [ $UP = 1 ] && { kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0; }
  if [ -n "${AURORA_DOMAINS_DIR:-}" ]; then
    for d in "${DOMS[@]}" "${CTLDOM:-}"; do [ -n "$d" ] && domain destroy "$d" >/dev/null 2>&1; done
  fi
  rm -rf "$WORK" "$RUN" "$RUN.up.log" 2>/dev/null
}
trap cleanup EXIT

step "build and bring the stack up" "the same real stack as the full-stack demo, in its own run dir"
"$ROOT/aurora-up" --build-only --no-haiku >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; fail "build failed"; }
# aurora-up --build-only builds the compositor's examples, not always its binary
(cd "$ROOT/compositor" && cargo build --release >"$WORK/cargo.log" 2>&1) || { cat "$WORK/cargo.log"; fail "compositor build failed"; }
[ -x "$EX/testclient" ] || fail "no $EX/testclient (a Wayland client that maps a window)"
"$ROOT/aurora-up" --run "$RUN" --no-build --no-haiku --no-windows >"$RUN.up.log" 2>&1 & UPPID=$!; UP=1
waitf "aurora-up ready" test -f "$RUN/ready"
sed -i 's/^mode = "shader"/mode = "solid"/' "$RUN/theme/aurora-glass.toml"; sleep 1   # a still wallpaper, so a capture changes only when a window maps
source "$RUN/env"
ok "stack up: bus, controller, compositor on $AURORA_WAYLAND"
[ -S "$XDG_RUNTIME_DIR/$AURORA_WAYLAND" ] && ok "compositor socket exists" || fail "no compositor socket"
KEYS="$XDG_RUNTIME_DIR/aurora"
for k in approval.key powerbox.key; do
  [ -s "$KEYS/$k" ] && ok "host has $KEYS/$k ($(stat -c %s "$KEYS/$k") bytes): the negative tests below are about something that exists" || fail "no host $k"
done
KEYSUMS=$(sha256sum "$KEYS/approval.key" "$KEYS/powerbox.key" | cut -d' ' -f1 | tr '\n' ' ')

echo; echo "host render nodes: $(ls /dev/dri/renderD* 2>/dev/null | tr '\n' ' ')(none = GPU half unproven)"
echo "host card nodes:   $(ls /dev/dri/card* 2>/dev/null | tr '\n' ' ')"
HOSTNODE=$(ls /dev/dri/renderD* 2>/dev/null | head -1)
genimage "$WORK/in.png"

# ---------------------------------------------------------------- GPU
step "GPU, through the controller" "a verb that lists device:gpu gets the render node; the same app's verb that does not, gets nothing"
P=$(aurora open "$WORK/in.png" --verb plain@v1 --app org.aurora.devprobe </dev/null 2>&1) || fail "plain@v1 failed: $P"
G=$(aurora open "$WORK/in.png" --verb gpu@v1 --app org.aurora.devprobe </dev/null 2>&1) || fail "gpu@v1 failed: $G"
PR=$(sed -n 's/^    result: //p' <<<"$P"); GR=$(sed -n 's/^    result: //p' <<<"$G")
echo "   plain@v1 saw: $PR"; echo "   gpu@v1   saw: $GR"
eq "$(js dri <<<"$PR")" "" "no grant: /dev/dri is empty or absent in the domain"
eq "$(js render_node <<<"$PR")" "" "no grant: no renderD* node"
eq "$(js card_node <<<"$PR")" "" "no grant: no card node"
eq "$(js card_node <<<"$GR")" "" "WITH grant: still no card node (the render node only)"
if [ -n "$HOSTNODE" ]; then
  eq "$(js render_node <<<"$GR")" "$HOSTNODE" "with the grant the render node is present"
  eq "$(js dri <<<"$GR" | tr -d ' ')" "$(basename "$HOSTNODE")" "and /dev/dri holds nothing but render nodes"
  case "$(js open <<<"$GR")" in
    yes) ok "the domain opened $HOSTNODE read-write" ;;
    denied) unproven "open($HOSTNODE) from inside the domain returned EACCES: the node is present but its mode is $(stat -c '%a %U:%G' "$HOSTNODE") with a per-user ACL, which does not reach the domain's own subordinate uid" ;;
    *) fail "unexpected open result: $(js open <<<"$GR")" ;;
  esac
else
  unproven "this host has no /dev/dri/renderD*: presence with a grant, and open(), could not be tested"
fi
controller provenance "$WORK/in.gpu.png" >"$WORK/gpu.prov.json" || fail "no provenance for gpu@v1"
controller provenance "$WORK/in.plain.png" >"$WORK/plain.prov.json" || fail "no provenance for plain@v1"
GP=$(cat "$WORK/gpu.prov.json")
DEVI=$(python3 -I -c 'import json,sys; g=json.load(sys.stdin)["grants"]; print([i for i,x in enumerate(g) if x.get("device")=="gpu"][0])' <<<"$GP") || fail "gpu@v1 provenance has no device grant"
eq "$(js grants.$DEVI.path <<<"$GP")" "device:gpu" "provenance names the grant device:gpu"
eq "$(js grants.$DEVI.source <<<"$GP")" "ambient" "its source is ambient (the manifest declared it, the verb asked)"
case "$(js grants.$DEVI.cap <<<"$GP")" in cap:*) ok "and it carries a daemon-minted handle $(js grants.$DEVI.cap <<<"$GP")";; *) fail "the device grant has no cap id";; esac
python3 -I -c 'import json,sys; g=json.load(open(sys.argv[1]))["grants"]; sys.exit(1 if any(x.get("device") for x in g) else 0)' "$WORK/plain.prov.json" \
  && ok "plain@v1's provenance has NO device grant (declared in the manifest, never asked for)" || fail "plain@v1 got a device grant"
VER=$(controller provenance "$WORK/in.gpu.png" --verify 2>&1); RC=$?
[ $RC = 0 ] && ok "controller provenance --verify passes with the device handle: $(tail -1 <<<"$VER" | cut -c1-70)" || { echo "$VER"; fail "verify exit $RC"; }
CTLDOM=$(sed 's/^domain:\(.*\):[0-9a-f]*$/\1/' <<<"$(js domain <<<"$GP")")
SHOW=$(domain show "$CTLDOM"); case "$SHOW" in *"device "*) fail "the device grant outlived the run: $SHOW";; *) ok "after the run the domain has no device grant left (revoked)";; esac

step "GPU, one domain, three states" "the same domain: absent, then present as the SAME device, then absent again after revoke"
domain create dg-gpu >/dev/null
if [ -n "$HOSTNODE" ]; then
  HM=$(stat -c '%t:%T' "$HOSTNODE")
  B=$(inside dg-gpu "ls /dev/dri 2>&1; test -e $HOSTNODE && echo present || echo absent" | tr '\n' ' ')
  case "$B" in *absent*) ok "before the grant: $HOSTNODE absent ($B)";; *) fail "node present without a grant: $B";; esac
  domain grant dg-gpu --device gpu >/dev/null
  A=$(inside dg-gpu "stat -c '%F %t:%T' $HOSTNODE 2>&1"); eq "$A" "character special file $HM" "granted: present, a character device with the host's major:minor"
  domain revoke dg-gpu --device gpu >/dev/null
  B=$(inside dg-gpu "test -e $HOSTNODE && echo present || echo absent"); eq "$B" "absent" "after revoke: absent again"
  domain grant dg-gpu --device bogus >/dev/null 2>&1 && fail "an unknown device class was accepted" || ok "an unknown device class (bogus) is refused, so no manifest can claim a device nothing composes"
  domain grant dg-gpu --device card0 >/dev/null 2>&1 && fail "card0 was grantable" || ok "the card node is not a grantable class"
fi

# ---------------------------------------------------------------- Wayland
step "the Wayland socket, through the controller" "device:wayland puts exactly one socket in the namespace; without it there is none"
W1=$(aurora open "$WORK/in.png" --verb display@v1 --app org.aurora.devprobe </dev/null 2>&1) || fail "display@v1 failed: $W1"
WR=$(sed -n 's/^    result: //p' <<<"$W1")
echo "   plain@v1 saw: $PR"; echo "   display@v1 saw: $WR"
eq "$(js wayland_socket <<<"$PR")" "false" "no grant: no compositor socket in the domain"
eq "$(js wayland_socket <<<"$WR")" "true" "granted: the socket is there"
eq "$(js keys <<<"$WR")" "" "granted: a search of the whole domain finds no approval.key or powerbox.key"
WP=$(controller provenance "$WORK/in.display.png"); case "$WP" in *'"device": "wayland"'*) ok "provenance records the wayland grant";; *) fail "no wayland grant in provenance";; esac

step "the key-exposure test" "binding the socket alone does not hand over the directory that holds approval.key and powerbox.key"
domain create dg-wl >/dev/null; domain grant dg-wl --device wayland >/dev/null
domain create dg-none >/dev/null
LS=$(inside dg-wl "ls /run/aurora-display"); eq "$LS" "wayland-0" "/run/aurora-display contains only the socket"
eq "$(inside dg-wl "ls /run | tr '\n' ' '")" "aurora aurora-display " "/run holds the domain's own dir and the display dir, nothing else"
eq "$(inside dg-wl "test -S /run/aurora-display/wayland-0 && echo socket")" "socket" "wayland-0 is a unix socket"
[ "$(inside dg-wl "cat /etc/passwd | wc -c | tr -d ' '")" -gt 0 ] && ok "control: the same cat does read a file that is there (/etc/passwd)" || fail "control failed: cat reads nothing in the domain"
for f in /run/aurora/approval.key /run/aurora/powerbox.key /run/aurora-display/../approval.key /run/aurora-display/../aurora/approval.key /run/aurora-display/../aurora/powerbox.key "$KEYS/approval.key" "$KEYS/powerbox.key"; do
  eq "$(inside dg-wl "cat $f 2>/dev/null | wc -c | tr -d ' '")" "0" "cannot read $f"
done
eq "$(inside dg-wl "test -e $KEYS || test -e $XDG_RUNTIME_DIR/aurora; test -e $KEYS && echo present || echo absent")" "absent" "the host runtime directory $KEYS is not a path in the domain"
SUMS=$(inside dg-wl "find / -xdev -type f -size 32c 2>/dev/null | xargs -r sha256sum 2>/dev/null | cut -d' ' -f1 | tr '\n' ' '")
HIT=0; for s in $KEYSUMS; do case " $SUMS " in *" $s "*) HIT=1;; esac; done
eq "$HIT" "0" "no 32-byte file anywhere in the domain has the contents of either key"

step "a window maps from inside a domain" "with the grant a Wayland client in a domain connects to Aurora and maps; without it, it cannot connect"
domain grant dg-wl "$EX" --ro >/dev/null
cap() { "$AURORA_CAPTURE" --display "$AURORA_WAYLAND" -o "$1" >/dev/null 2>&1 || fail "capture failed"; }
cap "$WORK/before.png"
domain run dg-wl -- "$EX/testclient" --size 300x300 --rgb 255,0,0 >"$WORK/wl.out" 2>"$WORK/wl.err" & CLI=$!
waitf "the client in the domain mapped a window" grep -q mapped "$WORK/wl.out"
ok "testclient inside dg-wl: $(grep -c . "$WORK/wl.out") lines, including 'enter' and 'mapped' (configured and drawn by Aurora)"
sleep 1; cap "$WORK/during.png"
D=$($PNG diff "$WORK/before.png" "$WORK/during.png"); [ "$D" -gt 10000 ] && ok "the compositor's frame changed by $D pixels: the window is on Aurora's screen" || fail "the frame did not change ($D px)"
CP=$(ps -o pid= -C testclient | tr -d ' ' | while read -r p; do [ "$p" != $$ ] && grep -q "aurora-domains/dg-wl" "/proc/$p/cgroup" 2>/dev/null && echo "$p"; done | head -1)
[ -n "$CP" ] && ok "that client is pid $CP in cgroup .../aurora-domains/dg-wl, uid $(stat -c %u /proc/$CP) (not ours, $(id -u)): it is the domain, not a host process" || fail "found no testclient in the dg-wl cgroup"
[ "$(stat -c %u /proc/$CP)" != "$(id -u)" ] && ok "it runs as the domain's own subordinate uid" || fail "client runs as our uid"
grep -q "refusing ext_data_control to pid $CP" "$RUN/compositor.log" && ok "the compositor already treats it as a domain client: it refused it the clipboard by cgroup" \
  || echo "   (note: no clipboard refusal logged for pid $CP; testclient may not bind ext_data_control)"
kill "$CP" 2>/dev/null; wait "$CLI" 2>/dev/null

domain create dg-nowl >/dev/null; domain grant dg-nowl "$EX" --ro >/dev/null
OUT=$(domain run dg-nowl -- /usr/bin/env WAYLAND_DISPLAY=/run/aurora-display/wayland-0 "$EX/testclient" --size 100x100 2>&1); RC=$?
[ $RC != 0 ] && ok "without the grant the same client exits $RC and maps nothing: $(tr '\n' ' ' <<<"$OUT" | cut -c1-110)" || fail "a client with no grant connected"
case "$OUT" in *mapped*) fail "client mapped a window with no grant";; *) ok "no 'mapped' without the grant";; esac
eq "$(inside dg-nowl "test -e /run/aurora-display && echo present || echo absent")" "absent" "and /run/aurora-display does not exist in that namespace"

step "shutdown leaves nothing behind" "no processes, run dir or cgroups survive"
cleanup_domains() { for d in "${DOMS[@]}" "$CTLDOM"; do domain destroy "$d" >/dev/null 2>&1; done; }
cleanup_domains
kill -TERM "$UPPID" 2>/dev/null; wait "$UPPID" 2>/dev/null; UP=0
NEW=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEW" ] && ok "no new aurora-domains cgroups" || fail "stray cgroups: $NEW"

echo
echo "NOT PROVEN BY THIS DEMO (read decision 0014):"
echo "  - that a render-node grant is narrow: it is not. It gives wide, mostly unisolated GPU access."
echo "  - that a Wayland-connected domain is confined to a window: it can do whatever a Wayland client can."
echo "  - per-domain compositor POLICY: the compositor now attributes a window to its domain (decision 0017, demos/windowed-domain) but applies a rule only to the clipboard."
echo
echo "passed $PASS checks in $STEPS steps, unproven ${#UNPROVEN[@]}"
for u in "${UNPROVEN[@]}"; do echo "  UNPROVEN: $u"; done
if [ ${#UNPROVEN[@]} -gt 0 ] && [ $STRICT = 1 ]; then echo "--strict: unproven assertions fail the run"; exit 1; fi
exit 0
