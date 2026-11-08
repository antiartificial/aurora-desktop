#!/usr/bin/env bash
# Data grid: Haiku's real BColumnListView (ColumnListView.cpp + ColumnTypes.cpp, compiled in place) with a string, an
# integer and a size column and a nested row, on a real headless compositor. The compositor's own capture is decoded and
# every claim is about pixels. Expectations are rendered by SEPARATE processes with no compositor and no pointer
# (AURORA_APPSERVER_PRESENT=0; GRID_SORT / GRID_DESC / GRID_W0 / GRID_EXPAND set the state a click, a drag or an expander
# click should reach), so "the grid did it" and "this is what it should look like" meet only in pixels:
#   - the grid is on the compositor verbatim (title row, typed columns, rows, scroll bars, expander);
#   - a click on the title of a column sorts by it: ascending, then descending;
#   - a click on the nested row's expander opens it, and again closes it, back to the first frame;
#   - a drag on a column's right edge widens it;
#   - aurora::unhandled_opcodes() is 0 for the whole run.
# The window is found in the capture (pix.py find), not assumed at 0,0. Prints a pass/fail count; exits non-zero on failure.
# Needs: cmake, ninja, g++, freetype2, harfbuzz, icu-uc, libwayland-client + wayland-scanner, cargo (only if the compositor or
# aurora-capture are not built), python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
KITS=$ROOT/kits; BUILD=$KITS/build/compat
COMP=$ROOT/compositor/target/release/aurora-compositor
CAP=$ROOT/harness/capture/target/release/aurora-capture
PIX="python3 -I $ROOT/demos/kitchen-sink/pix.py"; TI="python3 -I $ROOT/demos/kitchen-sink/ti.py"
W=$(mktemp -d "${TMPDIR:-/tmp}/grid-demo.XXXXXX")
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/gd.XXXXXX"); chmod 700 "$RUN"
PIDS=(); PASS=0; FAIL=0
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && [ "$p" != "$$" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  [ -n "${KEEP_CAPTURES:-}" ] && { mkdir -p "$KEEP_CAPTURES"; cp "$W"/*.png "$W"/*.out "$W"/*.err "$KEEP_CAPTURES"/ 2>/dev/null; }
  rm -rf "${W:?}" "${RUN:?}"
}
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
check() { local msg=$1; shift; if "$@"; then ok "$msg"; else bad "$msg"; fi; }
eq() { [ "$1" = "$2" ]; }

step "build the kit and the grid demo"
(cd "$KITS" && cmake -S . -B build -G Ninja >"$W/cmake.log" 2>&1 && ninja -C build iface_grid >"$W/ninja.log" 2>&1) || { tail -20 "$W/cmake.log" "$W/ninja.log"; echo "FATAL: build failed"; exit 2; }
[ -x "$COMP" ] || (cd "$ROOT/compositor" && cargo build --release >"$W/comp-build.log" 2>&1) || { tail "$W/comp-build.log"; echo "FATAL: compositor build"; exit 2; }
[ -x "$CAP" ] || (cd "$ROOT/harness/capture" && cargo build --release >"$W/cap-build.log" 2>&1) || { tail "$W/cap-build.log"; echo "FATAL: capture build"; exit 2; }
ok "built $BUILD/iface_grid"

step "expectations: the same program rendered by separate processes, no compositor, no pointer"
render() {  # name [VAR=val ...]
  local n=$1; shift; mkdir -p "$W/$n"
  env -u WAYLAND_DISPLAY "$@" AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 AURORA_APPSERVER_DUMP="$W/$n" HELLO_QUIT_AFTER_MS=1200 "$BUILD/iface_grid" >"$W/$n.out" 2>&1
  [ -s "$W/$n/window-0.ppm" ] || { cat "$W/$n.out"; bad "no frame for $n"; }
}
render plain A=1; render asc GRID_SORT=1; render desc GRID_SORT=1 GRID_DESC=1; render expanded GRID_SORT=0 GRID_W0=190 GRID_EXPAND=1; render wide GRID_SORT=0 GRID_W0=190; render byname GRID_SORT=0
read -r WW WH < <(sed -n 2p "$W/plain/window-0.ppm")
d_pa=$(cmp -l "$W/plain/window-0.ppm" "$W/asc/window-0.ppm" | wc -l); d_ad=$(cmp -l "$W/asc/window-0.ppm" "$W/desc/window-0.ppm" | wc -l)
d_pe=$(cmp -l "$W/plain/window-0.ppm" "$W/expanded/window-0.ppm" | wc -l); d_pw=$(cmp -l "$W/plain/window-0.ppm" "$W/wide/window-0.ppm" | wc -l)
check "the expectations differ from each other (plain/asc $d_pa, asc/desc $d_ad, plain/expanded $d_pe, plain/wide $d_pw bytes)" bash -c "[ $d_pa -gt 500 ] && [ $d_ad -gt 500 ] && [ $d_pe -gt 500 ] && [ $d_pw -gt 500 ]"

step "start the compositor headless (private socket dir $RUN) and the grid"
env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$RUN" "$COMP" --headless=1280x800 --test-input "$RUN/ti" >"$W/comp.log" 2>&1 & PIDS+=($!)
SOCK=
for _ in $(seq 100); do
  for s in "$RUN"/wayland-[0-9]*; do case $s in *.lock) ;; *) [ -S "$s" ] && SOCK=$(basename "$s");; esac; done
  [ -n "$SOCK" ] && break; sleep 0.1
done
[ -n "$SOCK" ] || { tail "$W/comp.log"; echo "FATAL: compositor socket never bound"; exit 2; }
mkdir -p "$W/live"
env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_APPSERVER_DUMP="$W/live" GRID_REPORT=1 HELLO_QUIT_AFTER_MS=24000 "$BUILD/iface_grid" >"$W/app.out" 2>"$W/app.err" & APP_PID=$!; PIDS+=($APP_PID)
for _ in $(seq 100); do grep -q '^COL 2 ' "$W/app.out" && break; sleep 0.1; done
sleep 0.5
shot() { XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/$1.png" >"$W/cap.log" 2>&1 || { cat "$W/cap.log"; bad "capture $1 failed"; }; }
ti() { $TI "$RUN/ti" "$@" >"$W/ti.log" 2>&1 || { cat "$W/ti.log"; bad "test-input refused: $*"; }; }
shot g0
POS=$($PIX find "$W/g0.png" "$W/plain/window-0.ppm"); OX=${POS% *}; OY=${POS#* }
[ "$POS" != none ] && ok "the grid window is on the compositor verbatim (at $POS), $WW x $WH" || { bad "the plain render does not appear in the capture"; OX=0; OY=0; }
cmpwin() { $PIX cmp "$W/$1.png" "$W/$2/window-0.ppm" "$OX" "$OY"; }     # differing pixels between a capture and an expectation
click() { ti "pointer $((OX+$1)) $((OY+$2))" click; sleep 0.5; }
read -r gl gt gr gb <<<"$(grep '^GRID grid ' "$W/app.out" | cut -d' ' -f3 | tr ',' ' ')"
c0=$(grep '^COL 0 ' "$W/app.out" | cut -d' ' -f3); c1=$(grep '^COL 1 ' "$W/app.out" | cut -d' ' -f3)
echo "   grid at $gl,$gt..$gr,$gb; columns $c0 and $c1 wide"
LEFT=27; TITLEY=$((gt + 10))                                    # the first column starts after the latch margin; the title row is the grid's first line
COUNTX=$((LEFT + c0 + c1 / 2)); NAMEX=$((LEFT + c0 / 2)); EDGE=$((LEFT + c0 - 1))

step "the grid: typed columns, rows, a nested row"
check "capture of the plain grid equals its headless render, pixel for pixel" eq "$(cmpwin g0 plain)" 0
ink=$($PIX ink "$W/g0.png" $((OX+gl)) $((OY+gt)) $((OX+gr)) $((OY+gb)))
check "the title row, the three typed columns and the five rows have ink of their own ($ink pixels)" bash -c "[ $ink -gt 1500 ]"

step "BColumnListView: a click on a column's title sorts by it"
click $COUNTX $TITLEY; shot g3
check "click on 'Count': ascending by count, equal to the render sorted by column 1" eq "$(cmpwin g3 asc)" 0
check "the rows moved: $(cmpwin g3 plain) pixels differ from the unsorted grid" bash -c "[ $(cmpwin g3 plain) -gt 300 ]"
click $COUNTX $TITLEY; shot g4
check "a second click on 'Count': descending, equal to the descending render" eq "$(cmpwin g4 desc)" 0
click $NAMEX $TITLEY; shot g5
check "click on 'Name': by name, equal to the render sorted by column 0" eq "$(cmpwin g5 byname)" 0

step "BColumnListView: dragging a column's right edge widens it (a held button through --test-input press/release)"
ti "pointer $((OX+EDGE)) $((OY+TITLEY))" press "sleep 0.25" "pointer $((OX+EDGE+15)) $((OY+TITLEY))" "sleep 0.15" "pointer $((OX+EDGE+30)) $((OY+TITLEY))" "sleep 0.15" "pointer $((OX+EDGE+40)) $((OY+TITLEY))" "sleep 0.25" release
sleep 0.6; shot g6
dw=$(cmpwin g6 wide); dp=$(cmpwin g6 plain)
echo "   after the drag the capture differs from the 190-wide render in $dw pixels and from the plain grid in $dp"
# the sort by name from the click above is still in force, and the render has it too, so only the width differs
check "Name widened by the drag: the capture equals the render with column 0 at 190 (150 + 40)" eq "$dw" 0

step "BColumnListView: the nested row's expander (last: the click selects the row, and a selected row stays outlined)"
click 20 57; shot g1
check "a click on the expander opens the nested row: the capture equals the render with it open, pixel for pixel" eq "$(cmpwin g1 expanded)" 0
check "and it is not the grid before the click ($(cmpwin g1 wide) pixels differ)" bash -c "[ $(cmpwin g1 wide) -gt 100 ]"
click 20 57; shot g2
read -r n2 x0 y0 x1 y1 < <($PIX cmpbox "$W/g2.png" "$W/wide/window-0.ppm" "$OX" "$OY")
# (the row that was clicked keeps the focus outline BColumnListView draws round the current row: that, and nothing else)
check "a second click closes it again: the capture is the wide, sorted grid except for the clicked row's outline ($n2 pixels in $x0,$y0..$x1,$y1, inside that row's 18 px band)" bash -c "[ $n2 -lt 1500 ] && [ $y0 -ge 46 ] && [ $y1 -le 68 ] && [ $x0 -ge 10 ] && [ $x1 -le 450 ]"

step "the server served every opcode the grid used"
for _ in $(seq 400); do kill -0 "$APP_PID" 2>/dev/null || break; sleep 0.1; done      # the app quits on its own timer and prints its count
kill -0 "$APP_PID" 2>/dev/null && { bad "the app did not exit"; kill "$APP_PID"; }
wait "$APP_PID" 2>/dev/null
unh=$(grep '^UNHANDLED' "$W/app.out" | tail -1); echo "   | $unh"
check "aurora::unhandled_opcodes() for everything the live grid used: all clicks, sorts, the expander and the drag ($unh)" bash -c "case '$unh' in 'UNHANDLED 0 '*) true;; *) false;; esac"
check "the app wrote nothing to stderr that looks like a failure" bash -c "! grep -qiE 'segfault|abort|assert|not handled|FAIL' '$W/app.err'"

printf '\n== %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
