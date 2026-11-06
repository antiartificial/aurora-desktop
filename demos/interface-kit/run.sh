#!/usr/bin/env bash
# Interface Kit demo: a BeOS program (BApplication/BWindow/BView/BFont/BButton, Haiku's real Interface Kit source)
# opens a window on the Aurora compositor. The compositor runs HEADLESS; its frame is captured over
# ext-image-copy-capture, the PNG is decoded, and real pixels are asserted. Nothing is screenshotted from the host.
# Prints a pass/fail count; exits non-zero on any failure.
# Needs: cmake, ninja, g++, freetype2, harfbuzz, icu-uc, libwayland-client + wayland-scanner, cargo (only if the
# compositor or aurora-capture are not built), python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
KITS=$ROOT/kits; BUILD=$KITS/build/interface
COMP=$ROOT/compositor/target/release/aurora-compositor
CAP=$ROOT/harness/capture/target/release/aurora-capture
W=$(mktemp -d "${TMPDIR:-/tmp}/ik-demo.XXXXXX")
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/ik.XXXXXX"); chmod 700 "$RUN"    # AF_UNIX paths stay under 108 bytes
PIDS=(); PASS=0; FAIL=0
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && [ "$p" != "$$" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  rm -rf "${W:?}" "${RUN:?}"
}
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
need() { "$@" >/dev/null 2>&1 || { echo "FATAL: $*"; exit 2; }; }

step "build the kit, the BeOS apps and the tests (CMake + Ninja in kits/)"
(cd "$KITS" && cmake -S . -B build -G Ninja >"$W/cmake.log" 2>&1 && ninja -C build test_font test_iface_window test_controls iface_hello iface_controls >"$W/ninja.log" 2>&1) \
  || { tail -20 "$W/cmake.log" "$W/ninja.log"; echo "FATAL: build failed"; exit 2; }
ok "built into $BUILD"
[ -x "$COMP" ] || (cd "$ROOT/compositor" && cargo build --release >"$W/comp-build.log" 2>&1) || { tail "$W/comp-build.log"; echo "FATAL: compositor build"; exit 2; }
[ -x "$CAP" ] || (cd "$ROOT/harness/capture" && cargo build --release >"$W/cap-build.log" 2>&1) || { tail "$W/cap-build.log"; echo "FATAL: capture build"; exit 2; }

step "headless tests (no compositor): BFont against the font file, a BWindow's pixels, a clicked BButton"
for scale in 1 1.45 2; do
  out=$(AURORA_UI_SCALE=$scale "$BUILD/test_font" 2>&1); rc=$?
  [ $rc = 0 ] && ok "test_font at scale $scale: $(echo "$out" | tail -1)" || { bad "test_font at scale $scale"; echo "$out" | grep FAIL; }
  echo "$out" | grep '^INVARIANT' > "$W/inv-$scale"
  out=$(AURORA_UI_SCALE=$scale "$BUILD/test_iface_window" 2>&1); rc=$?
  [ $rc = 0 ] && ok "test_iface_window at scale $scale: $(echo "$out" | tail -1)" || { bad "test_iface_window at scale $scale"; echo "$out" | grep FAIL; }
done
[ "$(cat "$W/inv-1")" = "$(cat "$W/inv-1.45")" ] && [ "$(cat "$W/inv-1")" = "$(cat "$W/inv-2")" ] \
  && ok "metrics are scale-independent: $(cat "$W/inv-1")" || bad "BFont metrics differ between output scales 1, 1.45 and 2"
out=$("$BUILD/test_controls" 2>&1); rc=$?
[ $rc = 0 ] && ok "test_controls: $(echo "$out" | tail -1)" || { bad "test_controls"; echo "$out" | grep FAIL; }

step "start the compositor headless (private socket dir $RUN)"
env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$RUN" "$COMP" --headless=1280x800 >"$W/comp.log" 2>&1 & PIDS+=($!)
SOCK=
for _ in $(seq 100); do
  for s in "$RUN"/wayland-[0-9]*; do case $s in *.lock) ;; *) [ -S "$s" ] && SOCK=$(basename "$s");; esac; done
  [ -n "$SOCK" ] && break; sleep 0.1
done
[ -n "$SOCK" ] || { tail "$W/comp.log"; echo "FATAL: compositor socket never bound in $RUN"; exit 2; }
caps=$(XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" --list 2>&1)
case $caps in *"ext-image-copy-capture-v1"*) ok "compositor is up on $SOCK and advertises ext-image-copy-capture-v1";; *) bad "no capture protocol: $caps"; exit 1;; esac

run_app() {  # name scale -> sets APP_PID; stdout to $W/<name>.out
  local name=$1 scale=$2 bin=$3
  env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_UI_SCALE="$scale" AURORA_APPSERVER_DUMP="$W" HELLO_METRICS=1 \
      HELLO_QUIT_AFTER_MS=20000 "$bin" >"$W/$name.out" 2>"$W/$name.err" & APP_PID=$!; PIDS+=($APP_PID)
}

step "BeOS app on the compositor at output scale 1: capture and decode"
run_app hello1 1 "$BUILD/iface_hello"
for _ in $(seq 100); do [ -s "$W/window-0.ppm" ] && grep -q '^METRIC fixed' "$W/hello1.out" && break; sleep 0.1; done
[ -s "$W/window-0.ppm" ] || { cat "$W/hello1.err"; bad "the app never finished a frame"; }
sleep 0.5
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/capture1.png" >"$W/cap1.log" 2>&1 && ok "captured a frame: $(tail -1 "$W/cap1.log" | sed 's/.*(//; s/)//')" || { cat "$W/cap1.log"; bad "capture failed"; }
cat "$W/hello1.out" | sed 's/^/   | /'
python3 -I "$HERE/imgcheck.py" "$W/capture1.png" "$W/window-0.ppm" "$W/hello1.out" | tee "$W/check1.txt"
PASS=$((PASS + $(grep -c '^   ok:' "$W/check1.txt"))); FAIL=$((FAIL + $(grep -c '^   FAIL:' "$W/check1.txt")))
[ -n "${KEEP_CAPTURE:-}" ] && cp "$W/capture1.png" "$KEEP_CAPTURE"
kill "$APP_PID" 2>/dev/null; wait "$APP_PID" 2>/dev/null

step "BeOS app at output scale 2: the same 320x200 logical window rendered into a 640x400 buffer"
rm -f "$W/window-0.ppm"
run_app hello2 2 "$BUILD/iface_hello"
for _ in $(seq 100); do [ -s "$W/window-0.ppm" ] && break; sleep 0.1; done
sleep 0.5
dim=$(head -2 "$W/window-0.ppm" | tail -1)
[ "$dim" = "640 400" ] && ok "server rendered the 320x200 window at 640x400 device pixels (scale 2)" || bad "scale-2 frame is '$dim', want 640 400"
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/capture2.png" >"$W/cap2.log" 2>&1 || cat "$W/cap2.log"
read -r bx0 by0 bx1 by1 < <(python3 -I "$HERE/imgcheck.py" --bbox "$W/capture2.png" 240 240 230)
printf '   view-colour pixels in the capture span x=%s..%s y=%s..%s\n' "$bx0" "$bx1" "$by0" "$by1"
# 320x200 logical window with a 1px frame: interior 318x198. At 640x400 it would be 638x398.
if [ "$((bx1-bx0+1))" -ge 316 ] && [ "$((bx1-bx0+1))" -le 320 ] && [ "$((by1-by0+1))" -ge 196 ] && [ "$((by1-by0+1))" -le 200 ]; then
  ok "compositor honoured buffer_scale=2: the 640x400 buffer occupies the same 320x200 logical rectangle as at scale 1"
else
  bad "scale 2: window interior is $((bx1-bx0+1))x$((by1-by0+1)) in the capture, want about 318x198 (320x200 logical)"
fi
kill "$APP_PID" 2>/dev/null; wait "$APP_PID" 2>/dev/null

printf '\n== %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
