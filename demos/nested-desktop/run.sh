#!/usr/bin/env bash
# The desktop as a person runs it: the real compositor under winit, NESTED in a host compositor,
# with busd and controllerd under a DIFFERENT XDG_RUNTIME_DIR. Every other test runs the compositor
# --headless; nothing else exercises this configuration, and three bugs reached a user from it:
#
#   1  approval key   the compositor derived approval.key from the HOST's runtime dir, controllerd from
#                     $RUN/xdg, so every compositor-routed approval failed its signature check
#   2  no display     the file the controller reads to find the compositor's socket was written only by
#                     aurora-up, which `make desktop` runs with --no-compositor: nothing wrote it
#   3  relative name  the first fix wrote "wayland-1"; the controller resolved that against ITS runtime dir
#
# The shape they share, and the thing this tests: TWO PROCESSES DERIVE A PATH INDEPENDENTLY AND DISAGREE,
# AND NOTHING REPORTS AN ERROR. 2 and 3 were worse than a failure: `registry.launch@v1` answered ok,
# isolated=true, the app stayed up, and no window ever mapped. So the pass condition here is a window,
# never the launch's reply.
#
# How the configuration is reproduced rather than approximated:
#   - the host is a second, headless Aurora (the stand-in the clipboard and drag-and-drop tests use; see
#     compositor/src/dnd/standin.rs). winit connects to it exactly as it connects to KWin: through
#     XDG_RUNTIME_DIR + WAYLAND_DISPLAY, and the nested compositor shows up there as a window.
#   - the nested compositor is started by `make desktop` itself (build skipped), so the Makefile's key and
#     --display-file lines are what is tested, not a copy of them that could drift.
#   - the host's runtime dir and $RUN/xdg are different directories, asserted, because if they agree
#     all three bugs are invisible and a pass proves nothing.
#
# What cannot be done here, said once: --test-input exists only with --headless (main.rs refuses it under
# winit), so the nested compositor has no `state` command. Windows are read from its own log, from its own
# Wayland capture, and from the HOST's `state`. Keys reach it the way a person's do: through the host.
#
# One thing is NOT as `make desktop` leaves it: aurora-up --no-compositor starts controllerd with
# --no-compositor-approval, so on the real desktop approvals never go to the compositor at all and bug 1
# cannot occur there today. To test the signature check, controllerd is restarted here with the compositor
# route on, under the same $RUN/xdg. That is the configuration bug 1 needed. See the report in step 6.
#
# Exits non-zero on any failure, and 2 when it could not run (no bwrap, no subuid, kits not built): that
# is "unproven", never a pass. Nothing is screenshotted from the user's host; the only captures are the
# nested compositor's own, from its own socket.
# Needs: go, cargo, cmake+ninja (kits), make, bwrap + a subuid range, socat, python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
COMP=$ROOT/compositor/target/release/aurora-compositor
CAP=$ROOT/harness/capture/target/release/aurora-capture
PNG="python3 -I $ROOT/demos/full-stack/pngtool.py"
APP=org.aurora.haiku.controls; APPNAME="Haiku Controls"
HOSTRT=$(mktemp -d /tmp/aurora-nd-host.XXXXXX); RUN=$(mktemp -d /tmp/aurora-nd.XXXXXX); WORK=$(mktemp -d /tmp/aurora-nd-work.XXXXXX)
chmod 700 "$HOSTRT" "$RUN"
PASS=0; FAIL=0; STEPS=0; HOSTPID=; MAKEPID=; CTLPID=; UPPID=; COMPPID=; DOM=

step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$1" "$2"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
check(){ local m=$1; shift; if "$@"; then ok "$m"; else bad "$m"; fi; }
has()  { case "$1" in *"$2"*) true;; *) false;; esac; }
unproven() { printf '\nUNPROVEN: %s\n   This demo did not run, so it proves NOTHING about the nested desktop. Exiting non-zero on purpose.\n' "$*" >&2; exit 2; }
waitf(){ local n=$1; shift; for _ in $(seq "${TRIES:-150}"); do "$@" 2>/dev/null && return 0; sleep 0.1; done; return 1; }
js()   { python3 -I -c 'import json,sys; d=json.load(sys.stdin)
for k in sys.argv[1].split("."): d=d[int(k)] if isinstance(d,list) else d[k]
print(d if not isinstance(d,bool) else str(d).lower())' "$1"; }
hostask() { printf '%s\n' "$1" | socat - "UNIX-CONNECT:$HOSTRT/input.sock"; }
clog() { sed 's/\x1b\[[0-9;]*m//g' "$WORK/make.log"; }
# children of a pid, recursively, deepest first: only processes this script started
descendants() { local c; for c in $(pgrep -P "$1" 2>/dev/null); do descendants "$c"; echo "$c"; done; }

cleanup() {
  [ -n "$COMPPID" ] && kill -TERM "$COMPPID" 2>/dev/null
  [ -n "$CTLPID" ] && kill -TERM "$CTLPID" 2>/dev/null
  [ -n "$UPPID" ] && kill -TERM "$UPPID" 2>/dev/null
  for _ in $(seq 100); do { [ -z "$UPPID" ] || ! kill -0 "$UPPID" 2>/dev/null; } && break; sleep 0.1; done
  [ -n "$MAKEPID" ] && { for p in $(descendants "$MAKEPID"); do kill -TERM "$p" 2>/dev/null; done; kill -TERM "$MAKEPID" 2>/dev/null; wait "$MAKEPID" 2>/dev/null; }
  [ -n "$HOSTPID" ] && { kill -TERM "$HOSTPID" 2>/dev/null; wait "$HOSTPID" 2>/dev/null; }
  [ -n "${KEEP:-}" ] && { mkdir -p "$KEEP"; cp "$WORK"/*.log "$WORK"/*.png "$RUN"/*.log "$KEEP"/ 2>/dev/null; }
  rm -rf "${WORK:?}" "${RUN:?}" "${RUN:?}.up.log" "${HOSTRT:?}" 2>/dev/null
}
trap cleanup EXIT

step "prerequisites" "a test that cannot run must say so and fail, not pass quietly"
command -v bwrap >/dev/null || unproven "bwrap is not installed: a windowed app cannot be put in a domain"
command -v socat >/dev/null || unproven "socat is not installed: keys cannot be sent to the host compositor"
command -v make  >/dev/null || unproven "make is not installed: the Makefile's desktop target is what is under test"
grep -q "^$(id -un):" /etc/subuid 2>/dev/null || unproven "no subuid range for $(id -un): domains cannot be created"
"$ROOT/aurora-up" --build-only >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; unproven "aurora-up --build-only failed"; }
(cd "$ROOT/compositor" && cargo build --release >"$WORK/cargo.log" 2>&1) || { tail -20 "$WORK/cargo.log"; unproven "the compositor did not build"; }
[ -x "$ROOT/kits/build/compat/iface_controls" ] || unproven "kits/build/compat/iface_controls is not built (HAIKU_SRC, cmake, ninja): there is no windowed Haiku app to launch, and this demo will not fake one"
[ -x "$CAP" ] || (cd "$ROOT/harness/capture" && cargo build --release >"$WORK/cap.log" 2>&1) || unproven "aurora-capture did not build"
ok "bwrap, socat, make, subuid, the compositor, the kits and the capture tool are all present"

step "a host compositor" "winit needs something to open a window in; a headless Aurora is the stand-in the other host-facing tests use"
env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$HOSTRT" "$COMP" --headless=1280x800 --legacy-scene --trusted-socket "$HOSTRT/t.sock" --test-input "$HOSTRT/input.sock" \
  >"$WORK/host.out" 2>"$WORK/host.log" & HOSTPID=$!
TRIES=100 waitf "host socket" grep -q '^aurora-compositor: socket wayland' "$WORK/host.out" || { cat "$WORK/host.log"; unproven "the host compositor did not start"; }
HD=$(sed -n 's/^aurora-compositor: socket //p' "$WORK/host.out" | tail -1)
check "the host serves $HOSTRT/$HD" test -S "$HOSTRT/$HD"
TRIES=100 waitf "host input" test -S "$HOSTRT/input.sock"
hostwins() { hostask state | sed 's/^ok //' | python3 -I -c 'import json,sys; print(len(json.load(sys.stdin)["windows"]))'; }
check "the host has no windows yet" test "$(hostwins)" = 0

step "the desktop, started by 'make desktop'" "the real Makefile target, nested in the host, with aurora-up --no-compositor behind it: the two runtime dirs must differ"
# -o build: do not rebuild (done above). XWAYLAND= : no Xwayland, it is not what is tested.
# WAYLAND_DISPLAY and XDG_RUNTIME_DIR are the HOST's, as they are in a user's shell: that is why winit
# works and why this compositor's runtime dir is not $RUN/xdg.
env XDG_RUNTIME_DIR="$HOSTRT" WAYLAND_DISPLAY="$HD" make -s -C "$ROOT" -o build desktop RUN="$RUN" XWAYLAND= >"$WORK/make.log" 2>&1 & MAKEPID=$!
if ! TRIES=600 waitf "the nested compositor" grep -q '^aurora-compositor: socket wayland' "$WORK/make.log"; then
  tail -30 "$WORK/make.log"; tail -20 "$RUN.up.log" 2>/dev/null; bad "the nested compositor never came up"; exit 1
fi
WL=$(sed -n 's/^aurora-compositor: socket //p' "$WORK/make.log" | tail -1)
for p in $(descendants "$MAKEPID"); do
  case $(readlink "/proc/$p/exe" 2>/dev/null) in */aurora-compositor) COMPPID=$p;; esac
  case $(tr '\0' ' ' <"/proc/$p/cmdline" 2>/dev/null) in *aurora-up*--no-compositor*) UPPID=$p;; esac
done
check "the compositor and the aurora-up behind it are processes this script started ($COMPPID, $UPPID)" test -n "$COMPPID" -a -n "$UPPID"
check "the compositor is running under winit, not headless" grep -q 'winit: window' "$WORK/make.log"
check "the host runtime dir ($HOSTRT) is not the stack's ($RUN/xdg): the precondition for all three bugs" test "$HOSTRT" != "$RUN/xdg"
check "the nested compositor's socket is in the HOST's dir, not the stack's" bash -c "test -S '$HOSTRT/$WL' && ! test -e '$RUN/xdg/$WL'"
check "the host now has one window: the nested desktop" test "$(hostwins)" = 1
source "$RUN/env"
# `make desktop` needs its stack ready before it starts the compositor, so this is already true
check "busd and controllerd run under $RUN/xdg" bash -c "test -S '$AURORA_BUS_SOCK' && test '$XDG_RUNTIME_DIR' = '$RUN/xdg'"

step "the display file" "bug 2 (nothing wrote it) and bug 3 (a bare name the controller resolves against its OWN runtime dir)"
DF=$RUN/xdg/aurora/display
check "$DF exists" test -f "$DF"
DFC=$(head -1 "$DF" 2>/dev/null)
case "$DFC" in /*) ok "it holds an absolute path: $DFC";; *) bad "it holds '$DFC', which is not absolute: a reader in another runtime dir resolves it somewhere else";; esac
check "that path is a socket" test -S "$DFC"
check "and it is the nested compositor's socket ($HOSTRT/$WL)" test "$DFC" = "$HOSTRT/$WL"

step "a windowed Haiku app, launched through registry.launch@v1" "the call the dock makes; its reply said ok three times while no window existed, so the assertion is the window"
LJ=$(controller launch "$APP" 2>"$WORK/launch.err"); LRC=$?
printf '   | %s\n' "$(printf '%s' "$LJ" | cut -c1-220)"
check "the launch call answered ok" test $LRC = 0
DOM=$(js domain <<<"$LJ" 2>/dev/null)
check "it is in a launch domain: $DOM" bash -c "case '$DOM' in win-controls-*) true;; *) false;; esac"
check "isolated = true (what the user saw while NO window mapped)" test "$(js isolated <<<"$LJ" 2>/dev/null)" = true
has "$LJ" "device:wayland" && ok "device:wayland is among the grants"  || bad "no device:wayland grant in: $LJ"
if TRIES=200 waitf "the window" bash -c "clog() { sed 's/\x1b\[[0-9;]*m//g' '$WORK/make.log'; }; clog | grep -q 'deco: layout.*domain=$DOM'"; then
  ok "A WINDOW MAPPED, attributed to $DOM: $(clog | grep 'deco: layout' | grep "domain=$DOM" | tail -1 | sed 's/.*deco: //' | cut -c1-110)"
  MAPPED=1
else
  bad "NO WINDOW MAPPED for $DOM within 20s although the launch said ok. This is the failure the user saw. The app's side: $(ls "$AURORA_DOMAINS_DIR/$DOM" 2>/dev/null | tr '\n' ' ')"
  MAPPED=0
fi
if [ $MAPPED = 1 ]; then
  sleep 0.8
  DL=$(clog | grep 'deco: layout' | grep "domain=$DOM" | tail -1)
  BAR=$(sed -n 's/.* bar=\([^ ]*\) .*/\1/p' <<<"$DL" | head -1)
  XDG_RUNTIME_DIR=$HOSTRT "$CAP" --display "$WL" -o "$WORK/cap.png" >/dev/null 2>"$WORK/cap.err" && ok "the nested compositor's own capture was taken from its own socket" || bad "capture failed: $(cat "$WORK/cap.err")"
  if [ -s "$WORK/cap.png" ] && [ -n "$BAR" ]; then
    IFS=, read -r BX BY BW BH <<<"$BAR"
    N=$($PNG colors "$WORK/cap.png" "$BX" "$((BY+BH))" "$BW" 150)
    check "the window's client area holds $N distinct colours: real controls, drawn, not a blank surface" test "${N:-0}" -ge 8
  fi
  check "the window is in the nested compositor's frame, not on the host: the host still has exactly one window" test "$(hostwins)" = 1
fi

step "the Deskbar names the app" "from the attested domain name (launch_hash/app_hash), not the client's app_id, which every Haiku app shares"
if [ $MAPPED = 1 ]; then
  TRIES=80 waitf "a deskbar row" bash -c "sed 's/\x1b\[[0-9;]*m//g' '$WORK/make.log' | grep 'deskbar: layout' | tail -1 | grep -q '$APPNAME'" \
    && ok "the Deskbar lists '$APPNAME' (the registry's name for $APP)" \
    || bad "no Deskbar row says '$APPNAME'; last: $(clog | grep 'deskbar: layout' | tail -1 | sed 's/.*rows=/rows=/')"
  LASTROWS=$(clog | grep 'deskbar: layout' | tail -1)
  has "$LASTROWS" "org.aurora.interface-kit" && bad "the Deskbar shows the kit's shared app_id, not the app's name" || ok "and not the kit's shared app_id (org.aurora.interface-kit)"
else
  bad "no window, so there is nothing for the Deskbar to list"
fi

step "a compositor-routed approval passes its signature check" "bug 1: compositor and controller must derive the SAME key from DIFFERENT runtime dirs"
# See the header: aurora-up --no-compositor leaves controllerd on the bus route. Restart it with the compositor
# route on, otherwise there is no signature to check. Same env (XDG_RUNTIME_DIR=$RUN/xdg), same flags otherwise.
auroractl stop controllerd >/dev/null 2>&1
TRIES=100 waitf "the stock controllerd to stop" bash -c "! test -S '$AURORA_BUS_SOCK' || ! '$ROOT/build/bin/bus' inspect 2>/dev/null | grep -q registry"
"$ROOT/build/bin/controllerd" --apps "$ROOT/apps,$ROOT/controller/testdata/apps" --domain-bin "$ROOT/build/bin/domain" --approval-timeout 30s \
  --compositor-socket "$AURORA_TRUSTED_SOCK" >"$WORK/controllerd.log" 2>&1 & CTLPID=$!
TRIES=150 waitf "controllerd's registry" bash -c "'$ROOT/build/bin/bus' inspect 2>/dev/null | grep -q registry" || { tail "$WORK/controllerd.log"; bad "the restarted controllerd never registered"; }
grep -q "approvals are drawn by the compositor" "$WORK/controllerd.log" && ok "controllerd routes approvals to the compositor, from $RUN/xdg" || bad "controllerd is not using the compositor route: $(tail -3 "$WORK/controllerd.log")"
"$ROOT/build/bin/genimage" "$WORK/photo.png" >/dev/null 2>&1
aurora open "$WORK/photo.png" --verb copy@v1 --arg tag=nd </dev/null >/dev/null 2>&1   # makes the demo app's domain so the prompt can verify it
aurora open "$WORK/photo.png" --verb delete@v1 --output "$WORK/gone.png" </dev/null >"$WORK/del.out" 2>&1 & DP=$!
# the compositor logs "trusted: approval N queued" once the prompt is up: wait for that, not for a sleep
TRIES=150 waitf "the approval prompt" grep -q "trusted: approval .* queued: .*delete@v1" "$WORK/make.log" || bad "the compositor never queued the approval: the request did not reach it"
sleep 0.5
check "the invoke is blocked on the prompt: nothing has run" bash -c "kill -0 $DP 2>/dev/null && [ ! -e '$WORK/gone.png' ]"
hostask "key a" >/dev/null     # through the HOST, as a person's keypress reaches a nested window
for _ in $(seq 100); do kill -0 $DP 2>/dev/null || break; sleep 0.1; done
if kill -0 $DP 2>/dev/null; then kill $DP 2>/dev/null; bad "the invoke is still blocked 10s after the key: $(cat "$WORK/del.out" | head -3)"; else
  wait $DP; DRC=$?
  check "the invoke was allowed and finished (exit $DRC)" test $DRC = 0 -a -e "$WORK/gone.png"
  controller provenance "$WORK/gone.png" >"$WORK/prov.json" 2>/dev/null
  check "provenance: route=compositor decision=allow key=a (the key came through the host, the reply through the signature check)" \
    test "$(js approval.route <"$WORK/prov.json"):$(js approval.decision <"$WORK/prov.json"):$(js approval.key <"$WORK/prov.json")" = compositor:allow:a
fi
check "the controller did not refuse the compositor's reply" bash -c "! grep -q 'REFUSING THE COMPOSITOR' '$WORK/controllerd.log'"
grep 'REFUSING\|unusable' "$WORK/controllerd.log" | head -2 | sed 's/^/   | /'

printf '\n%s passed, %s failed (%s steps)\n' "$PASS" "$FAIL" "$STEPS"
[ "$FAIL" = 0 ] && printf 'NESTED DESKTOP: PASSED\n' || printf 'NESTED DESKTOP: FAILED\n' >&2
[ "$FAIL" = 0 ]
