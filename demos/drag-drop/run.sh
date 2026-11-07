#!/usr/bin/env bash
# Drag and drop and the clipboard, BOTH WAYS between a host desktop and Aurora, proved with bytes.
#
# What this demo is for: "seamless" is a claim about a round trip. A protocol trace that shows an
# offer, or a log line that says a drag was bridged, is not the claim. So every step here drives
# real binaries and then reads the bytes out of the other end: the file a guest app received, the
# text a host app received, the four representations one copy carried. Nothing is asserted from a
# compositor log that the data path itself could not also prove -- the log lines are only used for
# the refusals, where the point IS that no data moved.
#
# The host is a second, headless Aurora, the same stand-in the drag and clipboard tests use (see
# compositor/src/dnd/standin.rs): winit connects to a host through XDG_RUNTIME_DIR +
# WAYLAND_DISPLAY, and that is exactly how the compositor under test connects here, so Aurora shows
# up on it as a window. The user's real session is never touched: WAYLAND_DISPLAY is unset, both
# compositors get their own runtime dir under a fresh /tmp directory, and no window is ever opened
# on a real screen.
#
# The one thing two headless compositors cannot reproduce is a human hand on one physical mouse
# across two compositors. Steps 1-6 inject that pointer on both sides, which is faithful (one
# physical click IS a press on the host and a press inside Aurora), but it is injected. `run.sh
# --manual` is the same drag with the real thing: it starts Aurora nested on YOUR session under
# winit, tells you what to drag where, and then asserts on the bytes. It is opt-in because it opens
# a window on your screen. See "MANUAL STEP" at the bottom.
#
# Exits 0 only if every assertion passed. Exits 2 for "could not run" (binaries missing, no socat):
# that is unproven, never a pass. A step that needs a cgroup this machine will not delegate says so
# and is counted as skipped, not passed.
# Needs: cargo-built compositor and examples, socat, python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
COMP=$ROOT/compositor/target/release/aurora-compositor
EX=$ROOT/compositor/target/release/examples
MANUAL=0
[ "${1:-}" = "--manual" ] && MANUAL=1

PASS=0; FAIL=0; SKIP=0; STEPS=0
step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$1" "$2"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
skip() { SKIP=$((SKIP+1)); printf '   SKIPPED: %s\n' "$*"; }
check(){ local m=$1; shift; if "$@"; then ok "$m"; else bad "$m"; fi; }
has()  { case "$1" in *"$2"*) true;; *) false;; esac; }
unproven() { printf '\nUNPROVEN: %s\n   This demo did not run, so it proves NOTHING. Exiting non-zero on purpose.\n' "$*" >&2; exit 2; }

for f in "$COMP" "$EX/dndclient" "$EX/clipclient"; do
  [ -x "$f" ] || unproven "$f is missing: run 'cd $ROOT/compositor && cargo build --release --examples' first"
done
command -v socat >/dev/null || unproven "socat is not installed (used to speak to --test-input)"
command -v python3 >/dev/null || unproven "python3 is not installed"

# A short run dir: a unix socket path is capped at 108 bytes.
RUN=$(mktemp -d /tmp/audnd-XXXXXX) || unproven "cannot make a run directory"
HRT=$RUN/h; GRT=$RUN/g; mkdir -p "$HRT" "$GRT"; chmod 700 "$HRT" "$GRT"
# Never inherit the user's session: every client below is given an explicit display.
unset WAYLAND_DISPLAY
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; [ -n "${CG:-}" ] && rmdir "$CG" 2>/dev/null; }
trap cleanup EXIT

waitf() { local n=$1; shift; for _ in $(seq "${TRIES:-200}"); do "$@" 2>/dev/null && return 0; sleep 0.05; done; printf '   (timed out waiting for %s)\n' "$n" >&2; return 1; }
grepq() { grep -q -- "$2" "$1"; }

# ---- the two compositors -------------------------------------------------------------------
start_comp() { # name runtimedir extra-args...
  local name=$1 rt=$2; shift 2
  XDG_RUNTIME_DIR=$rt "$COMP" --headless=1280x800 --legacy-scene --test-input "$rt/input.sock" "$@" \
    >"$RUN/$name.out" 2>"$RUN/$name.log" &
  PIDS+=($!)
  waitf "$name's socket" grepq "$RUN/$name.out" "aurora-compositor: socket " || unproven "the $name compositor did not start; see $RUN/$name.log"
  sed -n 's/.*aurora-compositor: socket //p' "$RUN/$name.out" | head -1
}
ask() { printf '%s\n' "$2" | socat - "UNIX-CONNECT:$1/input.sock" >/dev/null; }
hostask()  { ask "$HRT" "$1"; }
guestask() { ask "$GRT" "$1"; }
spawn() { # runtimedir display logname binary args...
  local rt=$1 disp=$2 log=$3; shift 3
  XDG_RUNTIME_DIR=$rt WAYLAND_DISPLAY=$disp "$@" >"$RUN/$log.log" 2>"$RUN/$log.err" &
  PIDS+=($!)
  waitf "$log to map" grepq "$RUN/$log.log" mapped
}

step "stand up a host and Aurora nested in it" "the drag bridge comes up on the host's connection, BOTH WAYS"
HD=$(start_comp host "$HRT")
# A cgroup under this user's aurora-domains, so a client can be put in a domain the way the kernel
# does in production. Only some machines delegate a cgroup subtree; the domain steps say so if not.
CG=
MYCG=$(sed -n 's/^0:://p' /proc/self/cgroup)
case "$MYCG" in
  *.service*) CGPARENT=/sys/fs/cgroup${MYCG%%.service*}.service/aurora-domains
              mkdir -p "$CGPARENT" 2>/dev/null && mkdir "$CGPARENT/zz-dnd-demo-$$" 2>/dev/null && CG=$CGPARENT/zz-dnd-demo-$$ ;;
esac
python3 -I "$HERE/controller-standin.py" "$RUN/drop.sock" "$RUN/drops.jsonl" >"$RUN/ctl.log" 2>&1 &
PIDS+=($!)
waitf "the stand-in controller" test -S "$RUN/drop.sock"
GD=$(start_comp guest "$GRT" --host-dnd-display "$HRT/$HD" --host-clipboard-display "$HRT/$HD" --drop-socket "$RUN/drop.sock")
waitf "the dnd bridge" grepq "$RUN/guest.log" "host drag and drop bridging is on"
BRIDGE=$(sed -n 's/.*host drag and drop bridging is on //p' "$RUN/guest.log" | head -1)
printf '   host display=%s  aurora display=%s\n   bridge: %s\n' "$HD" "$GD" "$BRIDGE"
check "the bridge is up both ways" has "$BRIDGE" "both ways"
check "the clipboard bridge is up" grepq "$RUN/guest.log" "host clipboard bridging is on"
# Aurora's window on the host is window 0, 640x400, placed at (100,100): a pointer at host (X,Y)
# over it is at (X-100,Y-100) inside Aurora.
hostask "move 0 100 100"

step "drag a file, text and HTML FROM the host INTO an Aurora app" "the host's bytes arrive in the guest app, one pipe per type, nothing guessed"
spawn "$GRT" "$GD" guest-target "$EX/dndclient" target --size 400x300
guestask "move 0 0 0"
NOTE=$RUN/dropped-note.txt; printf 'a file the user dragged in\n' >"$NOTE"
spawn "$HRT" "$HD" host-source "$EX/dndclient" source --size 200x200 \
  "text/uri-list|file://$NOTE
" "text/plain;charset=utf-8|$NOTE" "text/html|<b>from the host</b>"
hostask "move 1 800 100"; hostask "pointer 850 150"; hostask "press"
waitf "the host app to start a drag" grepq "$RUN/host-source.log" drag-started
hostask "pointer 300 250"; hostask "pointer 310 260"
waitf "the guest app to see the drag" grepq "$RUN/guest-target.log" enter
ENTER=$(grep -m1 '^enter' "$RUN/guest-target.log")
printf '   the guest app was offered: %s\n' "${ENTER#enter * }"
check "every type the host offered reached the guest app" has "$ENTER" "types=text/uri-list,text/plain;charset=utf-8,text/html"
hostask "release"
waitf "the guest app to read every type" grepq "$RUN/guest-target.log" finished
sed -n 's/^data /   got: /p' "$RUN/guest-target.log"
check "the dropped file's URI arrived"  grepq "$RUN/guest-target.log" "file://$NOTE"
check "the HTML representation arrived" grepq "$RUN/guest-target.log" "<b>from the host</b>"
check "the host app was asked for data and finished, not cancelled" bash -c "grep -q '^send ' '$RUN/host-source.log' && grep -q finished '$RUN/host-source.log' && ! grep -q cancelled '$RUN/host-source.log'"

step "drop that same file on an app INSIDE A DOMAIN" "a drop is a GRANT, not a path: the app is handed an in-domain path the controller granted, and never the host's"
if [ -z "$CG" ]; then
  skip "this machine does not delegate a cgroup subtree, so no client can be put in a domain here (the same step runs in compositor/tests/dnd.rs)"
else
  : >"$RUN/drops.jsonl"
  XDG_RUNTIME_DIR=$GRT WAYLAND_DISPLAY=$GD sh -c 'echo $$ > "$0/cgroup.procs" && exec "$@"' "$CG" \
     "$EX/dndclient" target --size 400x300 >"$RUN/domain-target.log" 2>"$RUN/domain-target.err" &
  PIDS+=($!)
  waitf "the domain app to map" grepq "$RUN/domain-target.log" mapped
  guestask "move 1 0 0"
  spawn "$HRT" "$HD" host-source2 "$EX/dndclient" source --size 200x200 \
    "text/uri-list|file://$NOTE
" "text/plain;charset=utf-8|$NOTE"
  hostask "move 2 800 100"; hostask "pointer 850 150"; hostask "press"
  waitf "the host app to start a drag" grepq "$RUN/host-source2.log" drag-started
  hostask "pointer 300 250"; hostask "pointer 310 260"
  waitf "the domain app to see the drag" grepq "$RUN/domain-target.log" enter
  DENTER=$(grep -m1 '^enter' "$RUN/domain-target.log")
  printf '   the app in the domain was offered: %s\n' "${DENTER#enter * }"
  check "only the file list is offered, not its plain-text twin (which is a host path)" has "$DENTER" "types=text/uri-list"
  hostask "release"
  waitf "the domain app to read the list" grepq "$RUN/domain-target.log" finished
  printf '   the controller was asked: %s\n' "$(cat "$RUN/drops.jsonl")"
  check "the controller was asked to grant exactly the dropped file, read-only" python3 -I -c '
import json,sys
rs=[json.loads(l) for l in open(sys.argv[1]) if l.strip()]
sys.exit(0 if len(rs)==1 and rs[0]["op"]=="drop" and rs[0]["mode"]=="ro" and rs[0]["paths"]==[sys.argv[2]] else 1)' "$RUN/drops.jsonl" "$NOTE"
  sed -n 's/^data /   got: /p' "$RUN/domain-target.log"
  check "the app was handed the GRANTED in-domain path" grepq "$RUN/domain-target.log" "file:///run/aurora/drops/1/dropped-note.txt"
  check "no byte of the host path reached the app" bash -c "! grep -q '$RUN/dropped-note.txt' '$RUN/domain-target.log'"
  check "the compositor read the host's list once, and never asked for the text twin" bash -c "[ \$(grep -c 'send text/uri-list' '$RUN/host-source2.log') = 1 ] && ! grep -q 'send text/plain' '$RUN/host-source2.log'"
fi

step "drag OUT of an Aurora app ONTO a host app" "the guest app's bytes arrive in the host app: the half that did not exist before"
# Nothing may be left held from the steps above: a button still down inside Aurora would keep the
# old drag's grab and the next press would start nothing.
hostask "release"; guestask "release"
spawn "$HRT" "$HD" host-target "$EX/dndclient" target --size 400x300
# Place the host's target out on the desktop, clear of Aurora's window (which covers 100..740 x 100..500).
HOSTWINS=$(printf 'state\n' | socat - "UNIX-CONNECT:$HRT/input.sock" | sed -n 's/^ok //p' | python3 -I -c 'import json,sys; print(len(json.load(sys.stdin)["windows"]))')
hostask "move $((HOSTWINS-1)) 800 100"
spawn "$GRT" "$GD" guest-source "$EX/dndclient" source --size 400x300 \
  "text/plain;charset=utf-8|dragged out of Aurora" "text/html|<i>out of Aurora</i>"
guestask "move 0 0 0"
# One physical pointer, injected on both: host (300,250) IS Aurora's own (200,150).
hostask "pointer 300 250"; hostask "press"
guestask "pointer 200 150"; guestask "press"
waitf "the guest app to start a drag" grepq "$RUN/guest-source.log" drag-started
check "while the pointer is inside Aurora the host knows nothing of this drag" bash -c "! grep -q enter '$RUN/host-target.log'"
hostask "pointer 900 200"     # out of Aurora's window: the hand-off
waitf "the hand-off" grepq "$RUN/guest.log" "the host now has it"
printf '   %s\n' "$(sed -n 's/.*dnd: //p' "$RUN/guest.log" | grep 'the host now has it')"
hostask "pointer 910 210"     # the first motion the host's own drag sees
waitf "the host app to see the drag" grepq "$RUN/host-target.log" enter
HENTER=$(grep -m1 '^enter' "$RUN/host-target.log")
check "both representations reached the host app" has "$HENTER" "types=text/plain;charset=utf-8,text/html"
waitf "the guest app to be told the host will take it" grepq "$RUN/guest-source.log" "target text/plain"
ok "the dragging app was told, mid-drag, that the thing under the pointer accepts (wl_data_source.target)"
hostask "release"
waitf "the host app to read the data" grepq "$RUN/host-target.log" finished
sed -n 's/^data /   got: /p' "$RUN/host-target.log"
check "the text the guest app owns arrived in the host app" grepq "$RUN/host-target.log" "dragged out of Aurora"
check "so did the HTML" grepq "$RUN/host-target.log" "<i>out of Aurora</i>"
check "the guest app saw the drop complete, not a cancel" bash -c "grep -q dropped '$RUN/guest-source.log' && grep -q finished '$RUN/guest-source.log' && ! grep -q cancelled '$RUN/guest-source.log'"

step "drag a FILE out of an app in a domain" "an in-domain path is never handed to the host: it would name nothing there"
if [ -z "$CG" ]; then
  skip "no cgroup subtree on this machine (the same step runs in compositor/tests/dnd.rs)"
else
  XDG_RUNTIME_DIR=$GRT WAYLAND_DISPLAY=$GD sh -c 'echo $$ > "$0/cgroup.procs" && exec "$@"' "$CG" \
     "$EX/dndclient" source --size 400x300 "text/uri-list|file:///run/aurora/drops/1/dropped-note.txt
" "text/plain;charset=utf-8|/run/aurora/drops/1/dropped-note.txt" "text/html|<i>from a domain</i>" \
     >"$RUN/domain-source.log" 2>"$RUN/domain-source.err" &
  PIDS+=($!)
  waitf "the domain app to map" grepq "$RUN/domain-source.log" mapped
  hostask "release"; guestask "release"
  # A target of its own, stacked on top of the one from step 4 at the same place, so this drop
  # lands on it: truncating a log a running client still holds would leave holes, not an empty file.
  spawn "$HRT" "$HD" host-target2 "$EX/dndclient" target --size 400x300
  HOSTWINS=$(printf 'state\n' | socat - "UNIX-CONNECT:$HRT/input.sock" | sed -n 's/^ok //p' | python3 -I -c 'import json,sys; print(len(json.load(sys.stdin)["windows"]))')
  hostask "move $((HOSTWINS-1)) 800 100"
  hostask "pointer 300 250"; hostask "press"
  guestask "pointer 200 150"; guestask "press"
  waitf "the domain app to start a drag" grepq "$RUN/domain-source.log" drag-started
  hostask "pointer 900 200"
  waitf "the hand-off" grepq "$RUN/guest.log" "withholding .* from the host"
  printf '   %s\n' "$(sed -n 's/.*dnd: //p' "$RUN/guest.log" | grep 'from the host')"
  hostask "pointer 910 210"
  waitf "the host app to see the drag" grepq "$RUN/host-target2.log" enter
  DHENTER=$(grep -m1 '^enter' "$RUN/host-target2.log")
  printf '   the host app was offered: %s\n' "${DHENTER#enter * }"
  check "the host was offered the HTML and NOT the path" has "$DHENTER" "types=text/html"
  hostask "release"
  waitf "the host app to read the data" grepq "$RUN/host-target2.log" finished
  sed -n 's/^data /   got: /p' "$RUN/host-target2.log"
  check "no in-domain path reached the host" bash -c "! grep -q '/run/aurora/drops' '$RUN/host-target2.log'"
fi

step "one rich copy, four representations, both ways" "the PASTING side picks from exactly what was offered; nothing is converted or invented"
RICH=("text/plain;charset=utf-8" "Aurora" text/html "<b>Aurora</b>" text/uri-list "file://$NOTE" image/png "PNG-pretend")
XDG_RUNTIME_DIR=$HRT WAYLAND_DISPLAY=$HD "$EX/clipclient" set "${RICH[@]}" >"$RUN/host-copy.log" 2>&1 & PIDS+=($!)
waitf "the host copy" grepq "$RUN/host-copy.log" set
GT=$(XDG_RUNTIME_DIR=$GRT WAYLAND_DISPLAY=$GD "$EX/clipclient" types)
printf '   Aurora sees: %s\n' "$GT"
check "all four representations crossed into Aurora" has "$GT" "types=text/plain;charset=utf-8,text/html,text/uri-list,image/png"
for i in 0 2 4 6; do
  M=${RICH[$i]}; D=${RICH[$((i+1))]}
  G=$(XDG_RUNTIME_DIR=$GRT WAYLAND_DISPLAY=$GD "$EX/clipclient" get "$M")
  check "pasting $M in Aurora gives the host's bytes" has "$G" "$D"
done
XDG_RUNTIME_DIR=$GRT WAYLAND_DISPLAY=$GD "$EX/clipclient" set "${RICH[@]}" >"$RUN/guest-copy.log" 2>&1 & PIDS+=($!)
waitf "the guest copy" grepq "$RUN/guest-copy.log" set
HT=$(XDG_RUNTIME_DIR=$HRT WAYLAND_DISPLAY=$HD "$EX/clipclient" types)
printf '   the host sees: %s\n' "$HT"
check "all four representations crossed out to the host" has "$HT" "types=text/plain;charset=utf-8,text/html,text/uri-list,image/png"
for i in 0 2 4 6; do
  M=${RICH[$i]}; D=${RICH[$((i+1))]}
  H=$(XDG_RUNTIME_DIR=$HRT WAYLAND_DISPLAY=$HD "$EX/clipclient" get "$M")
  check "pasting $M on the host gives Aurora's bytes" has "$H" "$D"
done

# ---- MANUAL STEP ---------------------------------------------------------------------------
# Everything above injects the pointer. This is the same two drags with a real hand on a real
# mouse, across your real compositor and a nested Aurora. It is opt-in (`run.sh --manual`) because
# it opens a window on your screen; it uses its own run directory and leaves any Aurora you already
# have running alone.
if [ "$MANUAL" = 1 ]; then
  step "the same drags, by hand, on your real session" "the transport works against a real host compositor, which no headless stand-in can prove"
  if [ -z "${WAYLAND_DISPLAY_REAL:-}" ] && [ -z "${AURORA_MANUAL_DISPLAY:-}" ]; then
    skip "set AURORA_MANUAL_DISPLAY to your session's WAYLAND_DISPLAY (e.g. wayland-0) to run this step"
  else
    MD=${AURORA_MANUAL_DISPLAY:-$WAYLAND_DISPLAY_REAL}
    MRT=${XDG_RUNTIME_DIR_REAL:-/run/user/$(id -u)}
    MANRUN=$RUN/manual; mkdir -p "$MANRUN"; chmod 700 "$MANRUN"
    XDG_RUNTIME_DIR=$MRT WAYLAND_DISPLAY=$MD "$COMP" --legacy-scene --drop-socket "$RUN/drop.sock" \
       >"$RUN/manual.out" 2>"$RUN/manual.log" &
    PIDS+=($!)
    waitf "nested Aurora on your session" grepq "$RUN/manual.out" "aurora-compositor: socket "
    MDISP=$(sed -n 's/.*aurora-compositor: socket //p' "$RUN/manual.out" | head -1)
    MBRIDGE=$(sed -n 's/.*host drag and drop bridging is on //p' "$RUN/manual.log" | head -1)
    printf '   nested Aurora is up on your session (display %s)\n   bridge: %s\n' "$MDISP" "$MBRIDGE"
    if ! has "$MBRIDGE" "both ways"; then
      bad "the bridge came up host-to-guest only, so a drag OUT cannot be tested: $MBRIDGE"
    else
      spawn "$MRT" "$MDISP" manual-target "$EX/dndclient" target --size 400x300
      spawn "$MRT" "$MDISP" manual-source "$EX/dndclient" source --size 400x300 \
        "text/plain;charset=utf-8|dragged out by hand" "text/html|<i>by hand</i>"
      cat <<'INSTRUCTIONS'

   ----------------------------------------------------------------------------
   YOUR TURN. Two windows are inside the nested Aurora: a green one (a drop
   target) and a blue one (a drag source).

   1. Drag any file from your file manager onto the GREEN window inside Aurora.
   2. Press on the BLUE window inside Aurora, drag OUT of the Aurora window,
      and drop on something on your own desktop that takes text (a text editor,
      a terminal that accepts drops, a chat box).

   Take as long as you like; this waits up to 120 seconds for each, then checks
   what actually arrived.
   ----------------------------------------------------------------------------
INSTRUCTIONS
      TRIES=2400 waitf "a drop into the green window" grepq "$RUN/manual-target.log" "^data "
      check "the file you dragged in arrived with its bytes" grepq "$RUN/manual-target.log" "^data "
      sed -n 's/^data /   got: /p' "$RUN/manual-target.log"
      TRIES=2400 waitf "a drag out of Aurora" grepq "$RUN/manual.log" "the host now has it"
      check "your real compositor granted Aurora the drag" grepq "$RUN/manual.log" "the host now has it"
      check "the app you dropped on asked for the data" grepq "$RUN/manual-source.log" "^send "
      sed -n 's/^send /   your desktop asked Aurora for: /p' "$RUN/manual-source.log"
    fi
  fi
else
  printf '\n(Step for a human hand not run. `%s --manual` with AURORA_MANUAL_DISPLAY set does the\n same two drags on your real session, and asserts on the bytes. It opens a window.)\n' "$0"
fi

printf '\n===============================================================\n'
printf 'passed %s, failed %s, skipped %s, over %s steps\n' "$PASS" "$FAIL" "$SKIP" "$STEPS"
printf 'logs: %s\n' "$RUN"
if [ "$FAIL" != 0 ]; then printf 'FAILED\n'; exit 1; fi
if [ "$PASS" = 0 ]; then printf 'nothing was proven\n'; exit 2; fi
printf 'PASSED\n'
# Keep the logs only when something failed; a pass leaves nothing behind.
rm -rf "$RUN"
trap - EXIT; cleanup
exit 0
