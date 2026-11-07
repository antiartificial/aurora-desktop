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
  [ -n "${KEEP:-}" ] && [ -f "$W/browser.out" ] && cp "$W/browser.out" "$KEEP.browser.out"
  rm -rf "${W:?}" "${RUN:?}"
}
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
need() { "$@" >/dev/null 2>&1 || { echo "FATAL: $*"; exit 2; }; }

step "build the kit, the BeOS apps and the tests (CMake + Ninja in kits/)"
(cd "$KITS" && cmake -S . -B build -G Ninja >"$W/cmake.log" 2>&1 && ninja -C build test_font test_iface_window test_controls iface_hello iface_controls iface_keys iface_scroll iface_list iface_text iface_menu iface_layout iface_browser >"$W/ninja.log" 2>&1) \
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
# BTextView's layout is in layout units too: the GEOM line (caret x, every character boundary, BFont prefix widths) must not move with scale.
for scale in 1 1.45 2; do
  env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_UI_SCALE=$scale TEXT_PRESET="hi there" TEXT_SELECT=8,8 HELLO_METRICS=1 HELLO_QUIT_AFTER_MS=500 "$BUILD/iface_text" 2>&1 | grep '^GEOM' > "$W/tg-$scale"
done
[ -s "$W/tg-1" ] && [ "$(cat "$W/tg-1")" = "$(cat "$W/tg-1.45")" ] && [ "$(cat "$W/tg-1")" = "$(cat "$W/tg-2")" ] \
  && ok "BTextView geometry is scale-independent (scale 1, 1.45, 2 print the same GEOM line)" || bad "BTextView GEOM differs between scales: $(cat "$W"/tg-1 "$W"/tg-1.45 "$W"/tg-2)"
out=$("$BUILD/test_controls" 2>&1); rc=$?
[ $rc = 0 ] && ok "test_controls: $(echo "$out" | tail -1)" || { bad "test_controls"; echo "$out" | grep FAIL; }

# --legacy-scene: this demo compares window pixels at fixed window coordinates (a click at 60,43; a frame found at 0,0), so it runs
# on the pre-shell scene: no title bars, new windows at the origin. The shell with its decorations is exercised by
# demos/kitchen-sink and compositor/tests/decoration.rs.
step "start the compositor headless (private socket dir $RUN)"
env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$RUN" "$COMP" --headless=1280x800 --legacy-scene --test-input "$RUN/ti" >"$W/comp.log" 2>&1 & PIDS+=($!)
SOCK=
for _ in $(seq 100); do
  for s in "$RUN"/wayland-[0-9]*; do case $s in *.lock) ;; *) [ -S "$s" ] && SOCK=$(basename "$s");; esac; done
  [ -n "$SOCK" ] && break; sleep 0.1
done
[ -n "$SOCK" ] || { tail "$W/comp.log"; echo "FATAL: compositor socket never bound in $RUN"; exit 2; }
caps=$(XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" --list 2>&1)
case $caps in *"ext-image-copy-capture-v1"*) ok "compositor is up on $SOCK and advertises ext-image-copy-capture-v1";; *) bad "no capture protocol: $caps"; exit 1;; esac

run_app() {  # name scale -> sets APP_PID; stdout to $W/<name>.out
  local name=$1 scale=$2 bin=$3 quit_ms=${4:-20000}
  env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_UI_SCALE="$scale" AURORA_APPSERVER_DUMP="$W" HELLO_METRICS=1 \
      HELLO_QUIT_AFTER_MS=$quit_ms "$bin" >"$W/$name.out" 2>"$W/$name.err" & APP_PID=$!; PIDS+=($APP_PID)
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

step "KEYBOARD end to end: compositor --test-input -> xkb seat -> wl_keyboard -> BWindow -> BView::KeyDown -> pixels"
# The app is a BView that draws what it was typed. Keys enter through the compositor's own keyboard_input path
# (not injected into our server). The expectation is rendered by a SEPARATE process with no compositor and no
# keyboard (KEYS_PRESET), so "typed" and "preset" meet only in the pixels.
render_ref() {  # text outfile-dir
  mkdir -p "$2"; env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 KEYS_PRESET="$1" AURORA_APPSERVER_DUMP="$2" HELLO_QUIT_AFTER_MS=700 "$BUILD/iface_keys" >/dev/null 2>&1
}
render_ref ""                 "$W/ref-empty"
render_ref 'heLo<UP><CR>'     "$W/ref-typed"
render_ref 'helo<UP><CR>'     "$W/ref-noshift"
rm -f "$W/window-0.ppm"
run_app keys 1 "$BUILD/iface_keys"; KEYS_PID=$APP_PID
for _ in $(seq 100); do [ -s "$W/window-0.ppm" ] && break; sleep 0.1; done
sleep 0.5
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/keys-before.png" >/dev/null 2>&1
pos=$(python3 -I "$HERE/imgcheck.py" --find "$W/keys-before.png" "$W/ref-empty/window-0.ppm")
[ "$pos" != none ] && ok "before any key: the window is in the capture and is the empty render (at $pos)" || bad "before typing, the window does not match the empty reference"
python3 -I "$HERE/ti.py" "$RUN/ti" "key h" "key e" "down shift" "key l" "up shift" "key o" "key Up" "key Return" >"$W/ti.log" 2>&1 \
  && ok "compositor accepted 8 key commands (h e Shift+l o Up Return)" || { cat "$W/ti.log"; bad "test-input refused a key"; }
sleep 0.7
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/keys-after.png" >/dev/null 2>&1
grep -E '^KEYDOWN' "$W/keys.out" | sed 's/^/   | /'
p1=$(python3 -I "$HERE/imgcheck.py" --find "$W/keys-after.png" "$W/ref-typed/window-0.ppm" ${pos:+$pos})
[ "$p1" != none ] && ok "typed h,e,Shift+l,o,Up,Return: the capture shows exactly 'heLo<UP><CR>' (pixel-identical to the keyboard-less render, at $p1)" \
  || bad "after typing, the capture does not equal the 'heLo<UP><CR>' reference"
p2=$(python3 -I "$HERE/imgcheck.py" --find "$W/keys-after.png" "$W/ref-noshift/window-0.ppm")
[ "$p2" = none ] && ok "negative control: it is NOT the render of 'helo<UP><CR>' (Shift reached the view: capital L)" || bad "capture equals the no-shift render: Shift was lost"
p3=$(python3 -I "$HERE/imgcheck.py" --find "$W/keys-after.png" "$W/ref-empty/window-0.ppm")
[ "$p3" = none ] && ok "negative control: the window is no longer the empty render" || bad "window still empty after typing"
grep -q 'KEYDOWN n=1 byte0=0x4c key=0x44 raw=0x6c mods=0x101' "$W/keys.out" \
  && ok "the view saw bytes 'L', raw_char 'l', BeOS key 0x44, modifiers B_SHIFT_KEY|B_LEFT_SHIFT_KEY (0x101)" || bad "key message fields differ from what Haiku's app_server would send"
# BWindow's own key handling: Alt is Command; Command+Q is handled inside BWindow::_HandleKeyDown and quits the app.
python3 -I "$HERE/ti.py" "$RUN/ti" "down alt" "key q" "up alt" >/dev/null 2>&1
for _ in $(seq 30); do kill -0 "$KEYS_PID" 2>/dev/null || break; sleep 0.1; done
if kill -0 "$KEYS_PID" 2>/dev/null; then bad "Command+Q (Alt+q) did not quit the app: BWindow shortcut handling is not working"; kill "$KEYS_PID" 2>/dev/null
else ok "Alt+q (Command+Q) was handled by BWindow::_HandleKeyDown and the application quit"; fi
wait "$KEYS_PID" 2>/dev/null

step "SCROLLING: BScrollView + BScrollBar around a 600 px view in a 160 px frame; arrows clicked through the compositor's pointer"
# What is asserted is decoded from the capture alone: for each of the 160 visible rows of pixels, the colour that
# row-of-content (y + offset) must have. The offset is the one the PIXELS agree with (0..440, must be unique), then
# compared with what the keys/clicks should have produced (3 x step 13 = 39, ...). Preset frames are rendered by a
# separate process with SCROLL_PRESET (no compositor, no input) and must equal the capture, scroll bar thumb included.
render_scroll() { mkdir -p "$2"; env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 SCROLL_PRESET="$1" AURORA_APPSERVER_DUMP="$2" HELLO_QUIT_AFTER_MS=700 "$BUILD/iface_scroll" >/dev/null 2>&1; }
for o in 0 39 52 192; do render_scroll $o "$W/ref-scroll-$o"; done
run_app scroll 1 "$BUILD/iface_scroll"; SCROLL_PID=$APP_PID
sleep 1.5
scroll_state() {  # name expected-offset : capture, decode, assert
  XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/$1.png" >/dev/null 2>&1
  read -r got miss ties < <(python3 -I "$HERE/imgcheck.py" --scroll "$W/$1.png" 0 0)
  if [ "$got" = "$2" ] && [ "$miss" = 0 ] && [ "$ties" = 1 ]; then ok "$3: all 160 visible pixel rows are content rows (y+$2); offset $got is the unique fit"
  else bad "$3: decoded scroll offset $got (mismatching pixels $miss, $ties candidates), expected $2"; fi
  if [ -s "$W/ref-scroll-$2/window-0.ppm" ]; then
    q=$(python3 -I "$HERE/imgcheck.py" --find "$W/$1.png" "$W/ref-scroll-$2/window-0.ppm" 0 0)
    [ "$q" != none ] && ok "$3: the whole window (scroll bar thumb too) equals a render that was scrolled to $2 by the program itself" || bad "$3: window differs from the preset-$2 render"
  fi
}
scroll_state c0 0 "initial"
python3 -I "$HERE/ti.py" "$RUN/ti" "pointer 240 165" "click" "click" "click" >"$W/ti2.log" 2>&1 && ok "pointer moved over the scroll bar's lower arrow and clicked 3 times through the compositor" || { cat "$W/ti2.log"; bad "compositor pointer injection failed"; }
sleep 0.5; scroll_state c1 39 "after 3 arrow clicks"
python3 -I "$HERE/ti.py" "$RUN/ti" "key Down" "key Down" "key Up" >/dev/null 2>&1; sleep 0.5; scroll_state c2 52 "after Down, Down, Up on the keyboard"
python3 -I "$HERE/ti.py" "$RUN/ti" "pointer 240 150" "click" >/dev/null 2>&1; sleep 0.5; scroll_state c3 192 "after a click in the scroll bar track (one page, 140)"
kill "$SCROLL_PID" 2>/dev/null; wait "$SCROLL_PID" 2>/dev/null

step "LIST: BListView of 40 BStringItems in a BScrollView; selection by mouse click and keyboard through the compositor"
# Decoded from the capture: the selection is the run of non-white pixels down one column of the list (item height is
# measured, not assumed). The same window with the server clicking item 3 itself (AURORA_APPSERVER_CLICK, no compositor,
# no keyboard) must be pixel-identical: a click makes BScrollView draw its focus frame around the bar, a preset does not.
mkdir -p "$W/ref-list-3"; env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 AURORA_APPSERVER_CLICK=60,85 AURORA_APPSERVER_DUMP="$W/ref-list-3" HELLO_QUIT_AFTER_MS=1500 "$BUILD/iface_list" >/dev/null 2>&1
run_app list 1 "$BUILD/iface_list"; LIST_PID=$APP_PID
sleep 1.5
list_band() { XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/$1.png" >/dev/null 2>&1; python3 -I "$HERE/imgcheck.py" --band "$W/$1.png" 0 0; }
b=$(list_band l0); [ -z "$b" ] && ok "initially nothing is selected (no highlight in the list column)" || bad "unexpected highlight before any input: $b"
python3 -I "$HERE/ti.py" "$RUN/ti" "pointer 60 43" "click" >/dev/null 2>&1; sleep 0.5
b=$(list_band l1); read -r t1 e1 _ <<<"$b"; h=$((e1 - t1 + 1))
[ "$(echo "$b" | tr -cd ';' | wc -c)" = 0 ] && [ "$t1" -gt 0 ] && ok "mouse click at y=43 selected one item: highlight rows $t1..$e1 ($h px = one item), colour $(echo "$b" | cut -d' ' -f3-)" || bad "click did not produce exactly one highlight: '$b'"
python3 -I "$HERE/ti.py" "$RUN/ti" "key Down" "key Down" >/dev/null 2>&1; sleep 0.5
b=$(list_band l2); read -r t3 e3 _ <<<"$b"
[ "$t3" = "$((t1 + 2 * h))" ] && [ "$((e3 - t3 + 1))" = "$h" ] && ok "Down, Down moved the highlight by exactly 2 items ($t1 -> $t3, item height $h)" || bad "highlight at $t3..$e3 after two Down keys (item 1 was $t1..$e1, height $h)"
read -r nd d1 d2 _ < <(python3 -I "$HERE/imgcheck.py" --diff "$W/l2.png" "$W/ref-list-3/window-0.ppm" 0 0)
# (An earlier version tolerated 2 focus-ring corner pixels as "unexplained". They were the first symptom of B_WINDOW_ACTIVATED
# never reaching the focus view; with activation fixed and a click-driven reference the whole window is identical.)
[ "$nd" = 0 ] && ok "the whole window equals a render in which the server itself clicked item 3 (focus frame, scroll bar and all)" || bad "window differs from the item-3 render in $nd pixels: $d1 $d2"
python3 -I "$HERE/ti.py" "$RUN/ti" "key Down" "key Down" "key Down" "key Down" "key Down" "key Down" "key Down" "key Down" "key Down" "key Down" >/dev/null 2>&1; sleep 0.6
b=$(list_band l3); read -r t13 e13 _ <<<"$b"
[ "$(echo "$b" | tr -cd ';' | wc -c)" = 0 ] && [ -n "$t13" ] && [ "$e13" -le 159 ] && [ "$t13" -lt "$((13 * h))" ] \
  && ok "10 more Down keys: selection is item 13, which is at content y=$((13 * h)) unscrolled; the highlight is at $t13..$e13 inside the 160 px frame, so the list scrolled" \
  || bad "after 10 more Down keys the highlight is '$b': the list did not scroll to the selection"
python3 -I "$HERE/ti.py" "$RUN/ti" "key Return" >/dev/null 2>&1; sleep 0.4
grep -E '^(SELECTED|INVOKED)' "$W/list.out" | tr '\n' ' ' | sed 's/^/   | /; s/$/\n/'
grep -q '^INVOKED 13$' "$W/list.out" && ok "Return invoked item 13 (BListView invocation message reached the window; log line, the pixels above carry the proof)" || bad "no INVOKED 13"
kill "$LIST_PID" 2>/dev/null; wait "$LIST_PID" 2>/dev/null

step "BROWSER (demos/browser): a directory listing from the filesystem, enriched from a REAL indexd, opened through a launcher"
# Real busd + real indexd + the real `aurora` CLI (built from shell/, bus/, index/). indexd's index.jsonl is
# HAND-WRITTEN with one entry (made.png <- source.jpg): there is no controllerd/bwrap here, so nothing is made by an
# app; what is real is indexd's answer to `aurora query show --json` and the browser's reading of it.
if ! command -v go >/dev/null; then bad "go is not installed: the browser section needs busd, indexd and aurora built from source"; else
mkdir -p "$W/bin" "$W/fx/sub" "$W/idx/queries"
(cd "$ROOT/bus" && go build -o "$W/bin/busd" ./cmd/busd) && (cd "$ROOT/index" && go build -o "$W/bin/indexd" ./cmd/indexd) \
  && (cd "$ROOT/shell" && go build -o "$W/bin/aurora-real" ./cmd/aurora) && ok "built busd, indexd and aurora" || { bad "go build of busd/indexd/aurora failed"; }
echo hi > "$W/fx/plain.txt"; head -c 3000 /dev/urandom > "$W/fx/made.png"; head -c 5000 /dev/urandom > "$W/fx/source.jpg"; echo x > "$W/fx/sub/inner.txt"
printf '{"seq":1,"observed_at":"2026-11-06T10:00:00Z","sender":"uid1000:controllerd","bus_id":"b1","bus_ts":"2026-11-06T10:00:00Z","file":"%s/fx/made.png","app":"org.aurora.magick","verb":"resize@v1","domain":"magick-1","domain_attested":true,"inputs":["%s/fx/source.jpg"],"record_ts":"2026-11-06T10:00:00Z"}\n' "$W" "$W" > "$W/idx/index.jsonl"
# a launcher stand-in: `open` records its argv; everything else is the real CLI
cat > "$W/bin/aurora" <<STUB
#!/bin/sh
if [ "\$1" = open ]; then echo "\$@" > "$W/open.args"; exit 0; fi
exec "$W/bin/aurora-real" "\$@"
STUB
chmod +x "$W/bin/aurora"
BUS=$RUN/bus.sock
"$W/bin/busd" --socket "$BUS" --data-dir "$W/buslog" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$BUS" ] && break; sleep 0.1; done; [ -S "$BUS" ] || { cat "$W/busd.log"; bad "busd socket never bound"; }
"$W/bin/indexd" --socket "$BUS" --data-dir "$W/idx" --queries "$W/idx/queries" >"$W/indexd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'serving "index"' "$W/indexd.log" && break; sleep 0.1; done
grep -q 'serving "index"; .*1 entries already indexed' "$W/indexd.log" && ok "indexd is serving with 1 indexed entry (made.png)" || { cat "$W/indexd.log"; bad "indexd did not come up with the fixture entry"; }
benv() { env AURORA_BIN="$W/bin/aurora" AURORA_SOCKET="$BUS" BROWSER_LOG=1 BROWSER_NO_TERMINAL=1 BROWSER_DIR="$W/fx" "$@"; }
# references: same program, same real indexd, no compositor and no input: the entry is selected by the program itself
mkdir -p "$W/ref-b-made"; benv env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 AURORA_APPSERVER_CLICK=60,87 AURORA_APPSERVER_DUMP="$W/ref-b-made" HELLO_QUIT_AFTER_MS=2500 "$BUILD/iface_browser" >"$W/ref-b-made.out" 2>&1
benv env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_SOCKET="$RUN/nobus.sock" BROWSER_SELECT=made.png HELLO_QUIT_AFTER_MS=2500 "$BUILD/iface_browser" >"$W/b-nobus.out" 2>&1
grep -q '^PROV state=1 ' "$W/b-nobus.out" && grep -q 'bus is not running' "$W/b-nobus.out" \
  && ok "with no bus the pane says the index could not be asked (state 1, the launcher's own error shown), not 'knows nothing': $(grep -m1 'bus is not running' "$W/b-nobus.out" | sed 's/^PANE //')" || { cat "$W/b-nobus.out"; bad "no-bus case is not reported as 'could not be asked'"; }

rm -f "$W/window-0.ppm"
export AURORA_BIN="$W/bin/aurora" AURORA_SOCKET="$BUS" BROWSER_LOG=1 BROWSER_NO_TERMINAL=1 BROWSER_DIR="$W/fx"   # run_app is a function; it passes these on
BR_T0=$SECONDS; run_app browser 1 "$BUILD/iface_browser" "${BR_QUIT_MS:-120000}"; BR_PID=$APP_PID
unset AURORA_BIN AURORA_SOCKET BROWSER_LOG BROWSER_NO_TERMINAL BROWSER_DIR
for _ in $(seq 50); do grep -q '^DIR ' "$W/browser.out" && break; sleep 0.1; done
[ "$(grep -c '^DIR ' "$W/browser.out")" = 1 ] && grep -q "^DIR $W/fx (4 items)" "$W/browser.out" && ok "listing comes from readdir: $W/fx has 4 entries (the index knows 1 file)" || bad "unexpected listing: $(grep '^DIR' "$W/browser.out")"
sleep 0.5
# Input is asynchronous end to end (test-input socket -> compositor -> wl_pointer -> app thread -> BWindow queue), and
# the browser used to be driven with fixed sleeps. Every step now waits for the line the browser prints when the
# step has taken effect (SEL on a selection, DIR on a listing, OPEN on a launch) and fails only after a generous budget.
bcount() { grep -c -- "$1" "$W/browser.out"; }
bwaitn() { # bwaitn PATTERN MIN-COUNT [seconds]: poll until the pattern has MIN-COUNT lines
  local n=$(( ${3:-6} * 10 )); for _ in $(seq $n); do [ "$(bcount "$1")" -ge "$2" ] && return 0; sleep 0.1; done; return 1
}
# bclick Y NAME: click the list row at y, and wait until the browser reports that NAME was selected (SEL NAME count grows)
bclick() { local before; before=$(bcount "^SEL $2\$"); python3 -I "$HERE/ti.py" "$RUN/ti" "pointer 60 $1" "click" >/dev/null 2>&1; bwaitn "^SEL $2\$" $((before+1)) 6; }
# The provenance pane is filled by a worker thread running `aurora query show`,
# so its PROV/PANE lines appear asynchronously after the click. A bare grep
# races them: it failed about 1 run in 4 on exactly the two plain.txt checks.
# Wait for the line instead of sleeping longer, so a slow answer costs time
# rather than a false failure.
bgrep() { # bgrep PATTERN DESC [seconds]
  local pat=$1 desc=$2 n=$(( ${3:-6} * 10 ))
  for _ in $(seq $n); do grep -q "$pat" "$W/browser.out" && { ok "$desc"; return 0; }; sleep 0.1; done
  bad "$desc (no match for /$pat/ after ${3:-6}s)"; return 1
}
bclick 87 made.png   # item 2: made.png (rows are 21 px, list content starts at y=35; ../ and sub/ come first)
bwaitn "^PROV state=3 path=$W/fx/made.png" 1
grep -q "^PROV state=3 path=$W/fx/made.png" "$W/browser.out" && ok "click on made.png: indexd knows it (state 3)" || bad "no state-3 answer for made.png: $(grep '^PROV' "$W/browser.out" | tr '\n' ' ')"
for want in "PANE Made by org.aurora.magick resize@v1" "PANE   $W/fx/source.jpg" "PANE Domain: magick-1" "PANE Indexed writes: 1, last seen 2026-11-06T10:00:00"; do
  grep -qF -- "$want" "$W/browser.out" && ok "pane text: ${want#PANE }" || bad "pane lacks: $want"; done
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/b1.png" >/dev/null 2>&1
read -r nd d1 d2 d3 < <(python3 -I "$HERE/imgcheck.py" --diff "$W/b1.png" "$W/ref-b-made/window-0.ppm" 0 0)
if [ "$nd" = 0 ]; then ok "the window the compositor shows after a mouse click equals a render in which the server clicked made.png itself (0 differing pixels)"
else bad "window differs from the made.png render in $nd pixels: $d1 $d2 $d3"; fi
bclick 108 plain.txt
bgrep "^PROV state=2 path=$W/fx/plain.txt" "plain.txt: index knows nothing (state 2): the pane says so and that it is not a statement about the file"
bgrep 'PANE The index knows nothing about this file.' "pane text for an unknown file is shown"
bclick 129 source.jpg
bwaitn "^PROV state=2 path=$W/fx/source.jpg" 1
sed -n '/^PROV state=2 path=.*source.jpg/,/^PANE-END/p' "$W/browser.out" | grep -q "PANE   made.png" && ok "source.jpg: unknown as an output but listed as an input of made.png (used_in)" || bad "source.jpg pane lacks the used-in edge"
# navigation: double click on a directory enters it; Alt+Up goes up and re-selects it; Enter enters it again
python3 -I "$HERE/ti.py" "$RUN/ti" "pointer 60 66" "click" "click" >/dev/null 2>&1; bwaitn "^DIR $W/fx/sub " 1
grep -q "^DIR $W/fx/sub (1 items)" "$W/browser.out" && ok "double-click (two clicks through the compositor, 'clicks' = 2) entered sub/" || bad "double-click did not enter sub/: $(grep '^DIR' "$W/browser.out" | tr '\n' ' ')"
python3 -I "$HERE/ti.py" "$RUN/ti" "down alt" "key Up" "up alt" >/dev/null 2>&1; bwaitn "^DIR $W/fx (4 items)" 2
[ "$(grep -c "^DIR $W/fx (4 items)" "$W/browser.out")" -ge 2 ] && ok "Alt+Up (a BWindow shortcut) went back up" || bad "Alt+Up did not go up"
python3 -I "$HERE/ti.py" "$RUN/ti" "key Return" >/dev/null 2>&1; bwaitn "^DIR $W/fx/sub " 2
[ "$(grep -c "^DIR $W/fx/sub " "$W/browser.out")" -ge 2 ] && ok "Enter on the re-selected sub/ entered it again" || bad "Enter did not enter sub/"
python3 -I "$HERE/ti.py" "$RUN/ti" "down alt" "key Up" "up alt" >/dev/null 2>&1; bwaitn "^DIR $W/fx (4 items)" 3
bclick 87 made.png
python3 -I "$HERE/ti.py" "$RUN/ti" "key Return" >/dev/null 2>&1; bwaitn '^OPEN ' 1
grep -q "^OPEN $W/bin/aurora open --socket $BUS $W/fx/made.png\$" "$W/browser.out" && ok "Enter on a file ran: aurora open --socket <bus> $W/fx/made.png (no verb chosen by the browser)" || bad "no OPEN line: $(grep '^OPEN' "$W/browser.out")"
[ "$(cat "$W/open.args" 2>/dev/null)" = "open --socket $BUS $W/fx/made.png" ] && ok "the launcher process really received that argv (recorded by the stand-in)" || bad "launcher stand-in did not record the argv"
echo "   (browser section took $((SECONDS-BR_T0))s)"
kill "$BR_PID" 2>/dev/null; wait "$BR_PID" 2>/dev/null
fi
step "TEXT: BTextView (Haiku's TextView.cpp) - typing, backspace, caret, blinking, selection, through the compositor"
# Typing goes through the compositor's --test-input and its xkb keymap. BackSpace/Home/End/Delete have no name in
# --test-input (compositor/src/trusted/testinput.rs keycode()), so those arrive through aurora::inject_special_key:
# the same internal event the wl_keyboard callback posts, so BWindow/BTextView see a real B_KEY_DOWN, but NOT via the
# compositor or libxkbcommon. Expected pictures are rendered by separate processes with no compositor and no
# keyboard (TEXT_PRESET/TEXT_SELECT). The caret is InvertRect and blinks, so a live capture may or may not contain
# it: "equals the reference" means: zero differing pixels, or ONLY the caret column (x = BTextView's caret x, one
# pixel wide, inside the line). Glyph positions are checked against BFont numbers the app prints (GEOM lines).
trender() {  # name text select [activate]
  mkdir -p "$W/rt-$1"; env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 TEXT_PRESET="$2" TEXT_SELECT="$3" ${4:+AURORA_APPSERVER_ACTIVE=1} \
    AURORA_APPSERVER_DUMP="$W/rt-$1" HELLO_METRICS=1 HELLO_QUIT_AFTER_MS=900 "$BUILD/iface_text" >"$W/rt-$1.out" 2>&1
}
trender empty "" 0,0; trender typed "hi there" 8,8; trender back "hi ther" 7,7; trender mid "hi toher" 5,5
trender sel "hi toher" 3,5 act; trender repl "hi xher" 4,4; trender home "hi xher" 0,0
for r in empty typed back mid sel repl home; do [ -s "$W/rt-$r/window-0.ppm" ] || bad "reference render '$r' produced no frame"; done
tgeom() { grep '^GEOM' "$1" | tail -1; }
# classify a capture against a reference: prints  same | caret@X | DIFF n x0 y0 x1 y1   (caret-only differences allowed)
tcmp() {  # capture ref-name geom-line
  local g=$3 n x0 y0 x1 y1 cx top lh; read -r n x0 y0 x1 y1 < <(python3 -I "$HERE/imgcheck.py" --diffbox "$1" "$W/rt-$2/window-0.ppm" 0 0)
  [ "$n" = 0 ] && { echo same; return; }
  cx=$(echo "$g" | sed 's/.* caret=\([0-9]*\).*/\1/'); top=$(echo "$g" | sed 's/.* top=\([0-9]*\).*/\1/'); lh=$(echo "$g" | sed 's/.* lineh=\([0-9]*\).*/\1/')
  if [ "$x0" = "$cx" ] && [ "$x1" = "$cx" ] && [ "$y0" -ge "$top" ] && [ "$y1" -le "$((top + lh - 1))" ]; then echo "caret@$cx"; else echo "DIFF $n $x0 $y0 $x1 $y1 (caret column is $cx)"; fi
}
TFIFO=$RUN/tfifo; mkfifo "$TFIFO"
env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_UI_SCALE=1 AURORA_APPSERVER_DUMP="$W" HELLO_METRICS=1 HELLO_QUIT_AFTER_MS=90000 TEXT_CMD_FIFO="$TFIFO" \
  "$BUILD/iface_text" >"$W/text.out" 2>"$W/text.err" & TEXT_PID=$!; PIDS+=($TEXT_PID)
exec 7<>"$TFIFO"
tgrep() { local pat=$1 desc=$2 n=$(( ${3:-6} * 10 )); for _ in $(seq $n); do grep -q "$pat" "$W/text.out" && { ok "$desc"; return 0; }; sleep 0.1; done; bad "$desc (no match for /$pat/ after ${3:-6}s)"; return 1; }
tkeys() { for _ in $(seq 60); do [ "$(grep -c '^KEY ' "$W/text.out")" -ge "$1" ] && break; sleep 0.1; done; sleep 0.5; }
tshot() { XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/$1.png" >/dev/null 2>&1; }
# shoot until the caret is in its ON phase (it blinks), judging each capture against the reference; echoes the verdicts
tuntil_on() {  # name ref geom
  local v seen=; for i in 1 2 3 4 5 6; do tshot "$1-$i"; v=$(tcmp "$W/$1-$i.png" "$2" "$3"); seen="$seen $v"; cp "$W/$1-$i.png" "$W/$1.png"; case $v in caret@*|DIFF*) echo "$seen"; return;; esac; done; echo "$seen"
}
tstate() {  # name ref geom desc : the window equals the reference (caret column aside) AND the caret was seen on
  local verdicts; verdicts=$(tuntil_on "$1" "$2" "$3")
  case $verdicts in *DIFF*) bad "$4: differs from the reference: $verdicts";; *caret@*) ok "$4: equals the reference render, caret seen at its column ($verdicts )";; *) bad "$4: matches but the caret was never seen on:$verdicts";; esac
}
tgrep '^GEOM ' "iface_text started: BTextView constructed, fonts and the first GEOM line reported (BTextView had never run before)" 10
for _ in $(seq 40); do [ -s "$W/window-0.ppm" ] && break; sleep 0.1; done
sleep 0.6; g=$(tgeom "$W/text.out"); tshot t0; v=$(tcmp "$W/t0.png" empty "$g")
case $v in same|caret@*) ok "before any key: the window is the empty reference ($v)";; *) bad "empty window differs from the reference: $v";; esac

python3 -I "$HERE/ti.py" "$RUN/ti" "key h" "key i" "key space" "key t" "key h" "key e" "key r" "key e" >"$W/ti-t.log" 2>&1 \
  && ok "compositor accepted 8 key commands: h i space t h e r e" || { cat "$W/ti-t.log"; bad "test-input refused a key"; }
tkeys 8
grep -q '^KEY text="hi there" len=8 sel=8,8' "$W/text.out" && ok "BTextView's own state after the 8 keys: text \"hi there\", caret at 8" || bad "BTextView state is not 'hi there' sel 8,8: $(grep '^KEY' "$W/text.out" | tail -1)"
g=$(tgeom "$W/text.out"); tstate t1 typed "$g" "typed window"
python3 -I "$HERE/imgcheck.py" --text "$W/t1.png" 0 0 "$g" > "$W/t1.check"; cat "$W/t1.check"
PASS=$((PASS + $(grep -c '^   ok:' "$W/t1.check"))); FAIL=$((FAIL + $(grep -c '^   FAIL:' "$W/t1.check")))
v=$(tcmp "$W/t1.png" empty "$g"); case $v in DIFF*) ok "negative control: the typed window is NOT the empty render";; *) bad "typed window still looks empty ($v)";; esac
v=$(tcmp "$W/t1.png" back "$g"); case $v in DIFF*) ok "negative control: it is NOT the 'hi ther' render either";; *) bad "typed window equals the 'hi ther' render ($v)";; esac

# --- the caret blinks: B_PULSE (BWindow pulse rate 500 ms -> BMessageRunner -> our registrar slice) flips it
on=0; off=0; for i in 1 2 3 4 5 6 7 8; do tshot "tb$i"; case $(python3 -I "$HERE/imgcheck.py" --caret "$W/tb$i.png" 0 0 "$g") in on) on=$((on+1));; off) off=$((off+1));; esac; done
[ "$on" -ge 1 ] && [ "$off" -ge 1 ] && ok "caret blinks: of 8 captures the caret column was fully black in $on and absent in $off" || bad "caret did not blink: on in $on, off in $off of 8 captures"
ints=$(grep -o '^PULSE t=[0-9]*' "$W/text.out" | sed 's/.*=//' | awk 'NR>1 { print $1-p } { p=$1 }' | sort -n | awk '{a[NR]=$1} END { print a[1], a[int((NR+1)/2)], a[NR], NR }')
read -r pmin pmed pmax pn <<<"$ints"
[ "${pn:-0}" -ge 8 ] && [ "$pmed" -ge 450 ] && [ "$pmed" -le 560 ] && ok "B_PULSE reaches the view every ${pmed} ms (min $pmin, max $pmax over $pn intervals): the 500 ms BWindow pulse, delivered by the message-runner service" || bad "pulse interval median ${pmed:-?} ms over ${pn:-0} intervals"

# --- Backspace (injected; see the note above)
echo "key BackSpace" >&7; tkeys 9
grep -q '^INJECT key BackSpace ok' "$W/text.out" && ok "BackSpace was injected (aurora::inject_special_key)" || bad "BackSpace injection refused"
grep -q '^KEY text="hi ther" len=7 sel=7,7' "$W/text.out" && ok "BTextView state after Backspace: \"hi ther\", exactly one character removed, caret at 7" || bad "state after Backspace: $(grep '^KEY' "$W/text.out" | tail -1)"
g=$(tgeom "$W/text.out"); tstate t2 back "$g" "after Backspace"
python3 -I "$HERE/imgcheck.py" --text "$W/t2.png" 0 0 "$g" > "$W/t2.check"; cat "$W/t2.check"
PASS=$((PASS + $(grep -c '^   ok:' "$W/t2.check"))); FAIL=$((FAIL + $(grep -c '^   FAIL:' "$W/t2.check")))
v=$(tcmp "$W/t2.png" typed "$(tgeom "$W/rt-typed.out")"); case $v in DIFF*) ok "negative control: it is no longer the 'hi there' render";; *) bad "window still shows 'hi there' ($v)";; esac

# --- caret movement and insertion in the middle: Left x3 (compositor), type 'o'
python3 -I "$HERE/ti.py" "$RUN/ti" "key Left" "key Left" "key Left" "key o" >/dev/null 2>&1; tkeys 13
grep -q '^KEY text="hi toher" len=8 sel=5,5' "$W/text.out" && ok "Left x3 then 'o': inserted in the middle, \"hi toher\", caret at 5" || bad "state after Left x3, o: $(grep '^KEY' "$W/text.out" | tail -1)"
g=$(tgeom "$W/text.out"); tstate t3 mid "$g" "mid-line insert (caret at PointAt(5), x=$(echo "$g" | sed 's/.* caret=\([0-9]*\).*/\1/'), not at the end of the text)"

# --- selection: Shift+Left x2 selects "to"; the highlight is steady (no caret while a range is selected)
python3 -I "$HERE/ti.py" "$RUN/ti" "down shift" "key Left" "key Left" "up shift" >/dev/null 2>&1; tkeys 15
grep -q '^KEY text="hi toher" len=8 sel=3,5' "$W/text.out" && ok "Shift+Left x2 selected offsets 3..5 (\"to\")" || bad "selection state: $(grep '^KEY' "$W/text.out" | tail -1)"
g=$(tgeom "$W/text.out"); tshot t4; v=$(tcmp "$W/t4.png" sel "$g"); [ "$v" = same ] && ok "the selection highlight is pixel-identical to the render of preset selection 3..5 in an active window" || bad "selection window vs reference: $v"
v=$(tcmp "$W/t4.png" mid "$g"); case $v in DIFF*) ok "negative control: it is not the unselected render (the highlight is real)";; *) bad "selection looks like the unselected window ($v)";; esac
tshot t4b; v=$(tcmp "$W/t4b.png" sel "$g"); [ "$v" = same ] && ok "and it is the same on a second capture 0.6 s later (no caret blinks over a selection)" || bad "selection window changed between captures: $v"
# --- typing replaces the selection
python3 -I "$HERE/ti.py" "$RUN/ti" "key x" >/dev/null 2>&1; tkeys 16
grep -q '^KEY text="hi xher" len=7 sel=4,4' "$W/text.out" && ok "typing 'x' over the selection replaced it: \"hi xher\"" || bad "after typing over a selection: $(grep '^KEY' "$W/text.out" | tail -1)"
g=$(tgeom "$W/text.out"); tstate t5 repl "$g" "replaced selection"
# --- Home (injected)
echo "key Home" >&7; tkeys 17
grep -q '^KEY text="hi xher" len=7 sel=0,0' "$W/text.out" && ok "Home moved the caret to 0" || bad "after Home: $(grep '^KEY' "$W/text.out" | tail -1)"
g=$(tgeom "$W/text.out"); tstate t6 home "$g" "caret at the start of the line"
grep -q '^INJECT .* REFUSED' "$W/text.out" && bad "an injected key was refused"
grep -q 'not handled' "$W/text.err" && bad "server reported unhandled opcodes: $(grep -m3 'not handled' "$W/text.err")"
exec 7>&-; kill "$TEXT_PID" 2>/dev/null; wait "$TEXT_PID" 2>/dev/null

step "MENUS: BMenuBar/BMenu/BMenuItem (Haiku's own). A menu is a separate window; here it is an xdg_popup of the main window"
# Opened by a click through the compositor; the popup's pixels in the capture are compared with frames rendered by a
# SEPARATE process with no compositor (its window 1, the BMenuWindow, dumped frame by frame). Selecting an item needs a
# button held for longer than the menu's 16 ms polling, which --test-input cannot do (its `click` is press+release at
# once), so selection is driven by the server's internal pointer events (a human-length 80 ms click), not the compositor.
mrender() {  # dir script
  mkdir -p "$1"; env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 AURORA_APPSERVER_DUMP="$1" AURORA_APPSERVER_DUMP_SEQ=1 AURORA_APPSERVER_TRACE=1 \
    MENU_SCRIPT="$2" HELLO_QUIT_AFTER_MS=${3:-3500} "$BUILD/iface_menu" >"$1/out" 2>"$1/err"
}
mrender "$W/mr-open" "0:20,10:80"
mrender "$W/mr-hover" "0:20,10:80;1:29,30:-1" 4500
mexpect=$(grep -m1 'bar cached menu\]: SHOW' "$W/mr-open/err" | sed 's/.*frame \([0-9]*\),\([0-9]*\),.*/\1 \2/')
env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_UI_SCALE=1 HELLO_QUIT_AFTER_MS=60000 "$BUILD/iface_menu" >"$W/menu.out" 2>"$W/menu.err" & MENU_PID=$!; PIDS+=($MENU_PID)
for _ in $(seq 60); do grep -q '^BAR frame' "$W/menu.out" && break; sleep 0.1; done; sleep 0.8
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/mn0.png" >/dev/null 2>&1
python3 -I "$HERE/ti.py" "$RUN/ti" "pointer 20 10" "click" >/dev/null 2>&1; sleep 1.2
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/mn1.png" >/dev/null 2>&1
mfind() {  # capture frame-glob : first frame of the sequence that appears verbatim in the capture -> "frame x y"
  local f p; for f in $2; do p=$(python3 -I "$HERE/imgcheck.py" --find "$1" "$f"); [ "$p" != none ] && { echo "$(basename "$f") $p"; return; }; done; echo none
}
m=$(mfind "$W/mn1.png" "$W/mr-open/window-1-0*.ppm")
set -- $m
[ "$m" != none ] && ok "click on File through the compositor: the BMenu popup is in the capture, pixel-identical to the kit's own frame $1 (separate process, no compositor), at ($2,$3)" || bad "no frame of the reference menu window appears in the capture after the click"
[ "$m" != none ] && [ "$2 $3" = "$mexpect" ] && ok "and at the screen position BMenu asked for (frame $mexpect, relative to the main window at 0,0): the xdg_popup was placed where BMenu said" || bad "popup at '${m#* }' but BMenu's menu frame was '$mexpect'"
python3 -I "$HERE/ti.py" "$RUN/ti" "pointer 30 52" >/dev/null 2>&1; sleep 1.0
XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/mn2.png" >/dev/null 2>&1
m2=$(mfind "$W/mn2.png" "$W/mr-hover/window-1-0*.ppm"); set -- $m2
if [ "$m2" != none ]; then
  b=$(python3 -I "$HERE/imgcheck.py" --diffbox "$W/mn2.png" "$W/mr-open/window-1-000.ppm" "$2" "$3")
  ok "pointer moved over 'Open' through the compositor: the popup equals reference frame $1 (the item highlight), and differs from the plain menu in ${b%% *} pixels"
else bad "after moving over Open, no reference frame of the hover state appears in the capture"; fi
kill "$MENU_PID" 2>/dev/null; wait "$MENU_PID" 2>/dev/null
mrender "$W/mr-sel" "0:20,10:80;1:29,30:80" 4500
grep -q '^ITEM open Open' "$W/mr-sel/out" && ok "File, then a 80 ms click on Open: BMenuItem delivered its message to the window (ITEM open Open)" || bad "no ITEM for the click on Open: $(grep ITEM "$W/mr-sel/out")"
mrender "$W/mr-fast" "0:20,10:80;1:29,30:1" 4500
grep -q '^ITEM' "$W/mr-fast/out" && bad "a 1 ms click selected an item (expected: not seen by the 16 ms polling)" || ok "KNOWN LIMIT, shown on purpose: a 1 ms press+release (all --test-input can send) is NOT seen by BMenu's polling tracker, so no item is selected"
grep -q 'not handled' "$W/mr-sel/err" && bad "menu run reported unhandled opcodes: $(grep -m3 'not handled' "$W/mr-sel/err")"

step "LAYOUTS: BLayoutBuilder / BGroupLayout / BGridLayout (Haiku's own) size a window and place its children"
for scale in 1 1.45 2; do
  mkdir -p "$W/lay-$scale"; env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_UI_SCALE=$scale AURORA_APPSERVER_DUMP="$W/lay-$scale" LAYOUT_REPORT=1 HELLO_QUIT_AFTER_MS=1200 "$BUILD/iface_layout" >"$W/lay-$scale/out" 2>&1
done
[ "$(wc -l < "$W/lay-1/out")" = 8 ] && cmp -s "$W/lay-1/out" "$W/lay-1.45/out" && cmp -s "$W/lay-1/out" "$W/lay-2/out" \
  && ok "layout results are identical at scale 1, 1.45 and 2 (layout units): $(head -1 "$W/lay-1/out")" || bad "layout differs between scales: $(cat "$W"/lay-*/out | tr '\n' ' ')"
fr() { grep "^VIEW $1 " "$W/lay-1/out" | sed 's/.* //'; }
IFS=, read -r al at ar ab <<<"$(fr a)"; IFS=, read -r bl bt br bb <<<"$(fr b)"; IFS=, read -r cl ct cr cb <<<"$(fr c)"
ww=$(head -1 "$W/lay-1/out" | sed 's/WINDOW \([0-9]*\)x.*/\1/')
[ "$ww" = "$((al + (cr - al) + al))" ] && ok "the window grew to the layout: width $ww = inset $al + the button row ($((cr - al)) units, BRect width) + inset $al" || bad "window width $ww does not equal inset + button row ($((cr - al)))"
[ "$((bl - ar - 1))" = 6 ] && [ "$((cl - br - 1))" = 6 ] && [ "$at" = "$bt" ] && [ "$bt" = "$ct" ] && ok "buttons sit in one row with the 6 px spacing the builder asked for" || bad "button spacing is off: a $ar b $bl-$br c $cl"
bg=$(python3 -I "$HERE/imgcheck.py" --px "$W/lay-1/window-0.ppm" "$((ar + 3))" "$((at + 12))")
edge=$(python3 -I "$HERE/imgcheck.py" --px "$W/lay-1/window-0.ppm" "$al" "$((at + 12))")
[ "$bg" != "$edge" ] && ok "pixels: the gap between Alpha and Beta is window background ($bg), Alpha's left edge column x=$al is its border ($edge)" || bad "no button border at x=$al"

step "KEYMAP HOT RELOAD: the compositor's [input] layout changes while the app runs; the same physical key types a different letter"
HR=$RUN/hr; mkdir -p "$HR/theme"; chmod 700 "$HR"
printf '[input]\nlayout = "us"\nvariant = "dvorak"\n' > "$HR/theme/aurora-glass.toml"
env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$HR" "$COMP" --headless=1280x800 --legacy-scene --test-input "$HR/ti" --theme "$HR/theme" >"$W/comp2.log" 2>&1 & PIDS+=($!)
HSOCK=; for _ in $(seq 100); do for s in "$HR"/wayland-[0-9]*; do case $s in *.lock) ;; *) [ -S "$s" ] && HSOCK=$(basename "$s");; esac; done; [ -n "$HSOCK" ] && break; sleep 0.1; done
[ -n "$HSOCK" ] || bad "second compositor never bound its socket"
env XDG_RUNTIME_DIR="$HR" WAYLAND_DISPLAY="$HSOCK" HELLO_QUIT_AFTER_MS=12000 "$BUILD/iface_keys" >"$W/hr.out" 2>&1 & HR_PID=$!; PIDS+=($HR_PID)
sleep 1.5
python3 -I "$HERE/ti.py" "$HR/ti" "key h" >/dev/null 2>&1; sleep 0.4
printf '[input]\nlayout = "us"\n' > "$HR/theme/aurora-glass.toml"; sleep 3.5
python3 -I "$HERE/ti.py" "$HR/ti" "key h" >/dev/null 2>&1; sleep 0.4
[ "$(grep KEYDOWN "$W/hr.out" | sed 's/.*byte0=\(0x[0-9a-f]*\) key=\(0x[0-9a-f]*\).*/\1:\2/' | tr '\n' ' ')" = "0x64:0x41 0x68:0x41 " ] \
  && ok "physical key 0x41 typed 'd' under us/dvorak, then 'h' after the compositor hot-reloaded us/qwerty: the client took the new wl_keyboard.keymap" || { grep KEYDOWN "$W/hr.out"; bad "keymap change did not reach the client"; }
kill "$HR_PID" 2>/dev/null

printf '\n== %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
