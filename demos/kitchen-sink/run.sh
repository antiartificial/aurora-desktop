#!/usr/bin/env bash
# Kitchen sink: one BApplication (kits/interface/demo/kitchensink.cpp) with every Interface Kit control that runs on
# Aurora, on a real headless compositor. The compositor's own capture (ext-image-copy-capture) is decoded and every
# claim is about pixels: the controls appear, a click toggles a checkbox and ONLY the checkbox's region changes, a drag
# moves the slider thumb and the status bar's fill follows, a tab click shows a page that is pixel-identical to a
# render made by a separate process, typed keys reach the text control, and the server served every opcode.
# Nothing is screenshotted from the host. Prints a pass/fail count; exits non-zero on any failure.
# Needs: cmake, ninja, g++, freetype2, harfbuzz, icu-uc, libwayland-client + wayland-scanner, cargo (only if the
# compositor or aurora-capture are not built), python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
KITS=$ROOT/kits; BUILD=$KITS/build/interface
COMP=$ROOT/compositor/target/release/aurora-compositor
CAP=$ROOT/harness/capture/target/release/aurora-capture
PIX="python3 -I $HERE/pix.py"; TI="python3 -I $HERE/ti.py"
W=$(mktemp -d "${TMPDIR:-/tmp}/ks-demo.XXXXXX")
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/ks.XXXXXX"); chmod 700 "$RUN"    # AF_UNIX paths stay under 108 bytes
PIDS=(); PASS=0; FAIL=0
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && [ "$p" != "$$" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  [ -n "${KEEP_CAPTURES:-}" ] && { mkdir -p "$KEEP_CAPTURES"; cp "$W"/*.png "$W"/*.out "$W"/*.err "$W"/comp.log "$KEEP_CAPTURES"/ 2>/dev/null; }    # KEEP_CAPTURES=dir keeps every capture
  rm -rf "${W:?}" "${RUN:?}"
}
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
check() { local msg=$1; shift; if "$@"; then ok "$msg"; else bad "$msg"; fi; }   # check "message" test-command...
ge() { [ "$1" -ge "$2" ]; }; le() { [ "$1" -le "$2" ]; }; eq() { [ "$1" = "$2" ]; }

step "build the kit, the kitchen sink and the tests (CMake + Ninja in kits/)"
(cd "$KITS" && cmake -S . -B build -G Ninja >"$W/cmake.log" 2>&1 && ninja -C build iface_kitchensink test_controls test_widgets >"$W/ninja.log" 2>&1) \
  || { tail -20 "$W/cmake.log" "$W/ninja.log"; echo "FATAL: build failed"; exit 2; }
ok "built $BUILD/iface_kitchensink"
[ -x "$COMP" ] || (cd "$ROOT/compositor" && cargo build --release >"$W/comp-build.log" 2>&1) || { tail "$W/comp-build.log"; echo "FATAL: compositor build"; exit 2; }
[ -x "$CAP" ] || (cd "$ROOT/harness/capture" && cargo build --release >"$W/cap-build.log" 2>&1) || { tail "$W/cap-build.log"; echo "FATAL: capture build"; exit 2; }

# ---- references: the same program rendered by SEPARATE processes with no compositor, no pointer, no keyboard ----------
step "references: each tab rendered headless by its own process (AURORA_APPSERVER_PRESENT=0)"
for t in 0 1 2 3; do
  mkdir -p "$W/ref$t"
  env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 AURORA_APPSERVER_DUMP="$W/ref$t" KS_TAB=$t \
      HELLO_QUIT_AFTER_MS=1200 "$BUILD/iface_kitchensink" >"$W/ref$t.out" 2>&1
  [ -s "$W/ref$t/window-0.ppm" ] || { cat "$W/ref$t.out"; bad "no reference frame for tab $t"; }
done
read -r WW WH < <(sed -n 2p "$W/ref0/window-0.ppm")
ok "reference frames rendered: window is ${WW}x${WH} layout units, laid out by BLayoutBuilder"
n01=$(cmp -l "$W/ref0/window-0.ppm" "$W/ref1/window-0.ppm" | wc -l)
n12=$(cmp -l "$W/ref1/window-0.ppm" "$W/ref2/window-0.ppm" | wc -l)
n23=$(cmp -l "$W/ref2/window-0.ppm" "$W/ref3/window-0.ppm" | wc -l)
check "the four pages differ from each other ($n01, $n12, $n23 differing bytes)" bash -c "[ $n01 -gt 2000 ] && [ $n12 -gt 2000 ] && [ $n23 -gt 2000 ]"

step "start the compositor headless (private socket dir $RUN) and the kitchen sink"
env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$RUN" "$COMP" --headless=1280x800 --test-input "$RUN/ti" >"$W/comp.log" 2>&1 & PIDS+=($!)
SOCK=
for _ in $(seq 100); do
  for s in "$RUN"/wayland-[0-9]*; do case $s in *.lock) ;; *) [ -S "$s" ] && SOCK=$(basename "$s");; esac; done
  [ -n "$SOCK" ] && break; sleep 0.1
done
[ -n "$SOCK" ] || { tail "$W/comp.log"; echo "FATAL: compositor socket never bound in $RUN"; exit 2; }
mkdir -p "$W/live"
env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_APPSERVER_DUMP="$W/live" KS_REPORT=1 HELLO_QUIT_AFTER_MS=300000 \
    "$BUILD/iface_kitchensink" >"$W/app.out" 2>"$W/app.err" & APP_PID=$!; PIDS+=($APP_PID)
for _ in $(seq 150); do grep -q '^REPORT done' "$W/app.out" && break; sleep 0.1; done
grep -q '^REPORT done' "$W/app.out" || { cat "$W/app.err" "$W/app.out"; echo "FATAL: the app never finished its layout report"; exit 2; }
sleep 0.5
shot() { XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/$1.png" >"$W/cap.log" 2>&1 || { cat "$W/cap.log"; bad "capture $1 failed"; }; }
ti() { $TI "$RUN/ti" "$@" >"$W/ti.log" 2>&1 || { cat "$W/ti.log"; bad "test-input refused: $*"; }; }
shot c0
ok "captured a frame: $($PIX size "$W/c0.png") from the compositor"
sed 's/^/   | /' "$W/app.out" | grep -E 'WINDOW|CTL (tab|bar|tabs)' | head -8

# box NAME -> sets L T R B CX CY (window coordinates; the compositor places the window at 0,0, checked below)
box() { local l; l=$(grep "^CTL $1 " "$W/app.out" | head -1 | cut -d' ' -f3); [ -n "$l" ] || { bad "no frame reported for $1"; return 1; }
  IFS=, read -r L T R B <<<"$l"; CX=$(( (L + R) / 2 )); CY=$(( (T + B) / 2 )); }

step "the window is on the compositor, and it is the window the reference process drew"
pos=$($PIX find "$W/c0.png" "$W/live/window-0.ppm")
check "the app's own last frame appears verbatim in the capture (at $pos)" eq "$pos" "0 0"
check "capture of tab 0 equals the headless reference of tab 0, pixel for pixel" eq "$($PIX cmp "$W/c0.png" "$W/ref0/window-0.ppm" 0 0)" 0
check "the menu bar and the tab strip are in the capture (ink: $($PIX ink "$W/c0.png" 0 0 $((WW-1)) 56))" ge "$($PIX ink "$W/c0.png" 0 0 $((WW-1)) 56)" 500

# appears CAPTURE NAME...: every named control has pixels of its own (ink: not just the background colour) in that capture
appears() { local cap=$1 miss= n i; shift
  for n in "$@"; do box "$n" || continue; i=$($PIX ink "$W/$cap.png" $L $T $R $B); [ "$i" -gt 20 ] || miss="$miss $n($i)"; done
  [ -z "$miss" ] || echo "   (no ink:$miss)"; [ -z "$miss" ]; }

# ---- helpers for driving --------------------------------------------------------------------------------------------
click() { ti "pointer $1 $2" click; sleep 0.5; }
hold()  { ti "pointer $1 $2" press "sleep ${3:-0.3}" release; sleep 0.5; }       # a held left button: what BSlider and BMenu need
ev()    { grep -c "^EV $1\$" "$W/app.out"; }                                    # how many times "EV <what> <value>" was printed
inside() { [ "$1" -ge "$5" ] && [ "$2" -ge "$6" ] && [ "$3" -le "$7" ] && [ "$4" -le "$8" ]; }   # x0 y0 x1 y1 inside l t r b
# diffbox A B -> sets DN DX0 DY0 DX1 DY1 (how many pixels differ, and their bounding box)
diffbox() { read -r DN DX0 DY0 DX1 DY1 < <($PIX diff "$W/$1.png" "$W/$2.png" 0 0 $((WW-1)) $((WH-1))); }   # inside the window only: the desktop behind it moves
render_ref() {  # name click-x,y ms: the same window rendered with one click, by a separate process
  mkdir -p "$W/$1"; env -u WAYLAND_DISPLAY AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 AURORA_APPSERVER_CLICK="$2" \
    AURORA_APPSERVER_DUMP="$W/$1" KS_TAB=${3:-0} HELLO_QUIT_AFTER_MS=1800 "$BUILD/iface_kitchensink" >/dev/null 2>&1
}

step "every control on the Buttons page is on screen"
P0="press pressed bold italic style small medium large sizelabel stylebox sizebox alertbtn alertlabel"
check "BButton, BStringView, BCheckBox x2, BRadioButton x3, BBox x2 and the alert button each have pixels of their own ($P0)" appears c0 $P0

step "BCheckBox: a click toggles it; only the style box changes; the label's font becomes bold"
box bold; BX=$CX; BY=$CY; BL=$L; BT=$T
render_ref ref-bold "$((BL+8)),$BY"
click $((BL+8)) $BY
check "the checkbox's message arrived with value 1 (EV bold 1)" eq "$(ev 'bold 1')" 1
shot c1; diffbox c0 c1
box stylebox
check "pixels changed ($DN) and every changed pixel is inside the style box ($DX0,$DY0..$DX1,$DY1 in $L,$T..$R,$B)" bash -c "[ $DN -gt 40 ] && [ $DX0 -ge $L ] && [ $DY0 -ge $T ] && [ $DX1 -le $R ] && [ $DY1 -le $B ]"
box style
check "the 'Plain' label is now drawn bold: its ink widened" bash -c "[ $($PIX ink "$W/c1.png" $L $T $((L+90)) $B) -gt $(( $($PIX ink "$W/c0.png" $L $T $((L+90)) $B) )) ]"
check "capture equals the render of a clicked window made by a separate process" eq "$($PIX cmp "$W/c1.png" "$W/ref-bold/window-0.ppm" 0 0)" 0

step "BCheckBox again: a second click clears it, and the window returns to the original render"
click $((BL+8)) $BY
check "message arrived with value 0 (EV bold 0)" eq "$(ev 'bold 0')" 1
shot c2
check "capture equals the very first capture, pixel for pixel (toggled on, toggled off)" eq "$($PIX cmp "$W/c2.png" "$W/ref0/window-0.ppm" 0 0)" 0

step "BRadioButton: choosing Large moves the dot; the other buttons keep their look"
box medium; MCX=$((L+8)); MCY=$CY
dot=$($PIX px "$W/c0.png" $MCX $MCY)
box large; LCX=$((L+8)); LCY=$CY
dotn() { $PIX count "$W/$1.png" $(($2-8)) $(($3-8)) $(($2+8)) $(($3+8)) $dot 12; }
m0=$(dotn c2 $MCX $MCY); l0=$(dotn c2 $LCX $LCY)
check "before: the accent-coloured dot ($dot) is in Medium ($m0 px) and not in Large ($l0 px)" bash -c "[ $m0 -gt 10 ] && [ $l0 -eq 0 ]"
click $LCX $LCY
check "the radio button's message arrived (EV size 2)" eq "$(ev 'size 2')" 1
shot c3
m1=$(dotn c3 $MCX $MCY); l1=$(dotn c3 $LCX $LCY)
check "after: the dot is in Large ($l1 px) and gone from Medium ($m1 px)" bash -c "[ $l1 -gt 10 ] && [ $m1 -eq 0 ]"
diffbox c2 c3; box sizebox
check "every changed pixel ($DN of them) is inside the Size box" bash -c "[ $DN -gt 40 ] && [ $DX0 -ge $L ] && [ $DY0 -ge $T ] && [ $DX1 -le $R ] && [ $DY1 -le $B ]"

step "BButton: three clicks; the label counts them"
box press; PRL=$L; PRT=$T; PRB=$B; PCX=$CX; PCY=$CY
click $PCX $PCY; shot c4; click $PCX $PCY; click $PCX $PCY; shot c5
check "three EV button lines arrived (1, 2, 3)" bash -c "[ $(ev 'button 1') = 1 ] && [ $(ev 'button 2') = 1 ] && [ $(ev 'button 3') = 1 ]"
diffbox c3 c4; box pressed
check "after the first click only the button and its label changed ($DN px in $DX0,$DY0..$DX1,$DY1)" bash -c "[ $DN -gt 30 ] && [ $DX0 -ge $PRL ] && [ $DY0 -ge $PRT ] && [ $DX1 -le $R ] && [ $DY1 -le $PRB ]"
i4=$($PIX ink "$W/c4.png" $L $T $((L+120)) $B); i5=$($PIX ink "$W/c5.png" $L $T $((L+120)) $B)
check "the label grew from 'Pressed 1 time' to 'Pressed 3 times' (ink $i4 -> $i5)" bash -c "[ $i5 -gt $i4 ]"

step "BTabView: clicking the Input tab shows the Input page, identical to its reference render"
box tab1; click $CX $CY; shot c6
check "page 1 in the capture equals the headless reference of tab 1, pixel for pixel" eq "$($PIX cmp "$W/c6.png" "$W/ref1/window-0.ppm" 0 0)" 0
P1="name hello flavour flavourlabel slider vollabel statusbar spinner countlabel"
check "BTextControl, BOptionPopUp, BSlider, BStatusBar, BSpinner and their labels each have pixels of their own ($P1)" appears c6 $P1
d=$($PIX cmp "$W/c6.png" "$W/ref0/window-0.ppm" 0 0)
check "and it is not page 0: $d pixels differ from that" ge "$d" 5000

step "BTextControl: click it and type 'ab' through the compositor's keyboard"
box name; NL=$L; NT=$T; NR=$R; NB=$B; box hello; HL=$L; HT=$T
before=$($PIX ink "$W/c6.png" $((NL+2)) $((NT+2)) $((NR-2)) $((NB-2)))
hb=$($PIX dark "$W/c6.png" 300 $((HT-2)) $((WW-1)) $((HT+20)) 110)
click $((NL+30)) $((NT+8)); ti "key a" "key b"; sleep 0.5; shot c7
check "two text-modified messages arrived (EV text 1, EV text 2)" bash -c "[ $(ev 'text 1') -ge 1 ] && [ $(ev 'text 2') -ge 1 ]"
after=$($PIX ink "$W/c7.png" $((NL+2)) $((NT+2)) $((NR-2)) $((NB-2)))
check "glyph pixels appeared inside the field (ink $before -> $after)" bash -c "[ $after -gt $((before+25)) ]"
ha=$($PIX dark "$W/c7.png" 300 $((HT-2)) $((WW-1)) $((HT+20)) 110)
check "the greeting label now spells 'Hello, ab!': its dark pixels went from '$hb' to '$ha' (n xmin xmax ymin ymax)" bash -c "[ $(echo $ha | cut -d' ' -f1) -gt $(echo $hb | cut -d' ' -f1) ]"

step "BSlider: press the thumb, drag 80 px to the right, release (held button through --test-input press/release)"
box slider; SL=$L; ST=$T; SR=$R
box statusbar; BARL=$L; BART=$T; BARR=$R; BARB=$B
bar_fill() { $PIX xspan "$W/$1.png" $((BARL+2)) $((BART+16)) $((BARR-2)) $((BARB-2)) $fill 40; }
fill=$($PIX px "$W/c7.png" $((BARL+40)) $((BART+30)))
thumb() { $PIX xspan "$W/$1.png" $((SL+4)) $((ST+23)) $((SR-4)) $((ST+25)) 250 250 250 2; }     # xmin xmax of the thumb face (the track is darker, the panel 226)
read -r tx0 tx1 < <(thumb c7); TCX=$(( (tx0 + tx1) / 2 )); TCY=$((ST+30))
echo "   thumb face at x=$tx0..$tx1 (value 25 of 0..100); status bar fill colour $fill, fill spans $(bar_fill c7)"
read -r f0 f1 < <(bar_fill c7)
ti "pointer $TCX $TCY" press "sleep 0.25" "pointer $((TCX+30)) $TCY" "sleep 0.15" "pointer $((TCX+60)) $TCY" "sleep 0.15" "pointer $((TCX+80)) $TCY" "sleep 0.25" release
sleep 0.6; shot c8
read -r ux0 ux1 < <(thumb c8)
echo "   thumb face now at x=$ux0..$ux1"
d=$((ux0 - tx0))
check "the thumb followed the pointer: its face moved from x=$tx0 by $d px for an 80 px drag" bash -c "[ $d -ge 74 ] && [ $d -le 84 ]"
last=$(grep '^EV slider ' "$W/app.out" | tail -1 | cut -d' ' -f3)
n_ev=$(grep -c '^EV slider ' "$W/app.out")
check "the slider reported $n_ev values while dragging, the last one $last (about 25 + 80/4.05 = 45)" bash -c "[ $n_ev -ge 3 ] && [ $last -ge 42 ] && [ $last -le 48 ]"
read -r g0 g1 < <(bar_fill c8)
check "the status bar's fill follows the slider: it ended at x=$f1, now x=$g1 (width $((g1-g0+1)) of $((BARR-BARL-3)); $last% expected)" \
  bash -c "[ $(( (g1-g0+1) * 100 / (BARR-BARL-3) )) -ge $((last-2)) ] && [ $(( (g1-g0+1) * 100 / (BARR-BARL-3) )) -le $((last+2)) ]"
box vollabel
check "the 'Volume N' label beside the slider changed ($($PIX ink "$W/c7.png" $L $T $R $B) -> $($PIX ink "$W/c8.png" $L $T $R $B) ink pixels)" \
  bash -c "[ $($PIX diff "$W/c7.png" "$W/c8.png" $L $T $R $B | cut -d' ' -f1) -gt 5 ]"

step "BSpinner: click its + arrow twice"
box spinner; SPR=$R; SPT=$T; SPB=$B; SPL=$L; box countlabel; CLL=$L; CLT=$T; CLR=$R; CLB=$B
cb=$($PIX ink "$W/c8.png" $CLL $CLT $CLR $CLB)
click $((SPR-11)) $(( (SPT+SPB)/2 )); click $((SPR-11)) $(( (SPT+SPB)/2 )); shot c9
check "two spinner messages arrived, value 2 last (EV spinner 1, EV spinner 2)" bash -c "[ $(ev 'spinner 1') = 1 ] && [ $(ev 'spinner 2') = 1 ]"
check "the count label changed to 'Count is 2' (pixels in its box differ: $($PIX diff "$W/c8.png" "$W/c9.png" $CLL $CLT $((CLR+8)) $CLB | cut -d' ' -f1))" \
  bash -c "[ $($PIX diff "$W/c8.png" "$W/c9.png" $CLL $CLT $((CLR+8)) $CLB | cut -d' ' -f1) -gt 4 ]"
check "the spinner's own field shows the digit (pixels in the field differ: $($PIX diff "$W/c8.png" "$W/c9.png" $SPL $SPT $((SPR-45)) $SPB | cut -d' ' -f1))" \
  bash -c "[ $($PIX diff "$W/c8.png" "$W/c9.png" $SPL $SPT $((SPR-45)) $SPB | cut -d' ' -f1) -gt 3 ]"

step "BOptionPopUp: hold the button on it, the popup opens, drag to 'Mint', release"
box flavour; FL=$L; FT=$T; FR=$R; FB=$B; FCY=$CY
shot c10
ti "pointer $((FL+100)) $FCY" press "sleep 0.5"
shot c11
read -r pn px0 py0 px1 py1 < <($PIX diff "$W/c10.png" "$W/c11.png" $((FL+50)) $((FT-10)) $FR $((FT+120)))     # the field row is unchanged; the Chocolate and Mint rows are new
echo "   while held, the capture differs from before in $pn pixels, bbox $px0,$py0..$px1,$py1 (the popup menu)"
check "an open popup menu is visible: $pn pixels changed in $px0,$py0..$px1,$py1, two new item rows' worth of text" bash -c "[ $pn -gt 40 ] && [ $((py1-py0)) -ge 14 ]"
MX=$(( (px0 + px1) / 2 )); MY=$(( py1 - 5 ))      # the bottom row of new text is Mint
ti "pointer $MX $MY" "sleep 0.3" release; sleep 0.6
check "releasing on the third item selected it (EV flavour 3)" eq "$(ev 'flavour 3')" 1
shot c12
box flavourlabel
check "its label changed to 'Flavour value 3'" bash -c "[ $($PIX diff "$W/c10.png" "$W/c12.png" $L $T $R $B | cut -d' ' -f1) -gt 4 ]"
check "the popup is gone again: the rows it covered equal the capture before opening" eq "$($PIX diff "$W/c10.png" "$W/c12.png" $((FL+50)) $((FB+3)) $FR $((FB+36)) | cut -d' ' -f1)" 0

step "BListView + BScrollBar: the Data page; a click on the scroll bar's down arrow scrolls the list"
box tab2; click 300 330; click $CX $CY; shot d0
check "page 2 in the capture equals its headless reference, pixel for pixel (list, split view, scroll bars, text view)" eq "$($PIX cmp "$W/d0.png" "$W/ref2/window-0.ppm" 0 0)" 0
P2="list listscroll text textscroll"
check "BListView, BTextView and both BScrollViews each have pixels of their own ($P2)" appears d0 $P2
box listscroll; LSL=$L; LST=$T; LSR=$R; LSB=$B
for _ in 1 2 3 4 5; do click $((LSR-8)) $((LSB-9)); done; shot d1
diffbox d0 d1; box listscroll
check "five clicks on the arrow scrolled the list: $DN pixels changed, all inside the list's scroll view" bash -c "[ $DN -gt 500 ] && [ $DX0 -ge $L ] && [ $DY0 -ge $T ] && [ $DX1 -le $R ] && [ $DY1 -le $B ]"
first_before=$($PIX ink "$W/d0.png" $((LSL+3)) $((LST+3)) $((LSL+60)) $((LST+22)))
first_after=$($PIX ink "$W/d1.png" $((LSL+3)) $((LST+3)) $((LSL+60)) $((LST+22)))
check "the first row no longer reads 'Item 00' (its text region changed: ink $first_before -> $first_after)" bash -c "[ $($PIX diff "$W/d0.png" "$W/d1.png" $((LSL+3)) $((LST+3)) $((LSL+60)) $((LST+22)) | cut -d' ' -f1) -gt 10 ]"

step "BListView: click an item; it is highlighted and BTextView receives a line"
box list; ROWY=$((T+60)); box textscroll; TVL=$L; TVT=$T; TVR=$R; TVB=$B
sel_before=$($PIX px "$W/d1.png" $((LSL+70)) $ROWY)
click $((LSL+70)) $ROWY; shot d2
picked=$(grep '^EV list ' "$W/app.out" | tail -1 | cut -d' ' -f3)
check "the list reported a selection (EV list $picked)" bash -c "[ -n '$picked' ] && [ '$picked' -ge 3 ]"
sel_after=$($PIX px "$W/d2.png" $((LSL+70)) $ROWY)
check "the clicked row changed colour from ($sel_before) to ($sel_after), the selection highlight" bash -c "[ '$sel_before' != '$sel_after' ]"
diffbox d1 d2
check "the BTextView got a line ('Picked item ...'): $($PIX diff "$W/d1.png" "$W/d2.png" $TVL $TVT $TVR $TVB | cut -d' ' -f1) pixels changed inside it" bash -c "[ $($PIX diff "$W/d1.png" "$W/d2.png" $TVL $TVT $TVR $TVB | cut -d' ' -f1) -gt 20 ]"
check "and the status line says 'List: item NN' ($($PIX diff "$W/d1.png" "$W/d2.png" 0 $((WH-24)) 200 $((WH-1)) | cut -d' ' -f1) pixels)" bash -c "[ $($PIX diff "$W/d1.png" "$W/d2.png" 0 $((WH-24)) 200 $((WH-1)) | cut -d' ' -f1) -gt 10 ]"

step "BTextView: click into it and type"
click $((TVL+100)) $((TVB-40)); ti "key x" "key y" "key z"; sleep 0.5; shot d3
n=$($PIX diff "$W/d2.png" "$W/d3.png" $TVL $TVT $TVR $TVB | cut -d' ' -f1)
check "three typed characters changed $n pixels inside the text view" bash -c "[ $n -gt 15 ]"
diffbox d2 d3
check "and nothing outside the page moved except the list losing its focus ring and the text view gaining one ($DX0,$DY0..$DX1,$DY1 within $LSL,$TVT..$TVR,$TVB)" bash -c "[ $DX0 -ge $LSL ] && [ $DY0 -ge $TVT ] && [ $DX1 -le $TVR ] && [ $DY1 -le $TVB ]"

step "BSplitView: press on the divider and drag it 60 px to the right"
EY=$(( (LST+LSB)/2 )); before=$($PIX darkx "$W/d3.png" $EY $((LSR+2)) $((TVL+70)) 170 | tr ' ' '\n' | tail -1)
DIVX=$(( (LSR + TVL) / 2 ))
ti "pointer $DIVX $EY" press "sleep 0.25" "pointer $((DIVX+25)) $EY" "sleep 0.15" "pointer $((DIVX+60)) $EY" "sleep 0.25" release; sleep 0.6; shot d4
after=$($PIX darkx "$W/d4.png" $EY $((LSR+2)) $((TVL+70)) 170 | tr ' ' '\n' | tail -1)
echo "   the right-hand view's left border (last dark pixel on that row left of its text) was at x=$before, now x=$after"
check "the divider moved: the right-hand view's edge went from x=$before to x=$after (+60 expected)" bash -c "[ -n '$after' ] && [ $((after - before)) -ge 55 ] && [ $((after - before)) -le 65 ]"

step "BColorControl and BCardLayout: the Extras page"
box tab3; click $CX $CY; shot e0
box colour; CCL=$L; CCT=$T
e0d=$($PIX cmp "$W/e0.png" "$W/ref3/window-0.ppm" 0 0 0 0 $((WW-1)) $((WH-28)))     # everything above the status line, which still says "List: item NN"
e0b=$($PIX cmpbox "$W/e0.png" "$W/ref3/window-0.ppm" 0 0 0 0 $((WW-1)) $((WH-28)))
# One thing may differ: the text caret of the colour control's focused field blinks (B_PULSE, 500 ms), so a capture can land
# on the other phase than the reference did. It is one pixel column, at most one text line tall.
read -r e0n e0x0 e0y0 e0x1 e0y1 <<<"$e0b"
check "page 3 in the capture equals its reference above the status line, bar a blinking caret (differing pixels: $e0b)" bash -c "[ $e0n -eq 0 ] || ( [ $e0x0 -eq $e0x1 ] && [ $((e0y1-e0y0)) -le 20 ] )"
P3="colour prev next cardhost"
check "BColorControl, the two buttons and the BCardLayout's first card each have pixels of their own ($P3)" appears e0 $P3
box swatch; SWX=$CX; SWY=$CY
sw0=$($PIX px "$W/e0.png" $SWX $SWY)
check "the colour swatch is filled with the control's initial colour (60 120 200): $sw0" eq "$sw0" "60 120 200"
click $((CCL+2+5*16)) $((CCT+2)); shot e1
colour_ev=$(grep '^EV colour ' "$W/app.out" | tail -1 | cut -d' ' -f3)
sw1=$($PIX px "$W/e1.png" $SWX $SWY)
want="$(( (colour_ev >> 16) & 255 )) $(( (colour_ev >> 8) & 255 )) $(( colour_ev & 255 ))"
check "the swatch was $sw0 and is now $sw1: exactly the colour the control reported (EV colour $colour_ev = $want)" bash -c "[ '$sw1' = '$want' ] && [ '$sw0' != '$sw1' ]"
box next; click $CX $CY; shot e2; box cardhost
c1=$($PIX diff "$W/e1.png" "$W/e2.png" $L $T $R $B | cut -d' ' -f1)
check "clicking '>' replaced the card's text ($c1 pixels changed in the card host, EV card 1: $(ev 'card 1'))" bash -c "[ $c1 -gt 30 ] && [ $(ev 'card 1') = 1 ]"
box prev; click $CX $CY; click $CX $CY; shot e3; box cardhost
check "'<' twice goes round the three cards to card 2 (EV card 2: $(ev 'card 2'))" eq "$(ev 'card 2')" 1

step "BMenuBar and BMenu: File > Reset all, picked with a held button (the menu tracker polls the button)"
box menu0; FMX=$(( (L+R)/2 )); FMY=$(( (T+B)/2 )); BARBOT=$B; box menu1; EMX=$(( (L+R)/2 ))
menu_pick() {   # title-x item-y [capture-name]: press on the bar title, (capture the open menu,) drag onto an item of the popup, release
  ti "pointer $1 $FMY" press "sleep 0.5"; [ -n "${3:-}" ] && shot "$3"
  ti "pointer $1 $(($2-6))" "sleep 0.2" "pointer $1 $2" "sleep 0.3" release; sleep 0.8; }
shot m0
ITEM1=$((BARBOT + 11))
menu_pick $((FMX+14)) $ITEM1 m1
read -r mn mx0 my0 mx1 my1 < <($PIX diff "$W/m0.png" "$W/m1.png" 0 $((BARBOT+1)) 300 $((BARBOT+130)))
echo "   with File held open the capture differs in $mn pixels, bbox $mx0,$my0..$mx1,$my1"
check "the File menu was open and drawn under the bar: $mn new pixels, $((my1-my0+1)) px tall" bash -c "[ $mn -gt 400 ] && [ $((my1-my0)) -ge 60 ]"
check "choosing 'Reset all' sent its message (EV reset 1)" eq "$(ev 'reset 1')" 1
shot m2
r0=$($PIX cmp "$W/m2.png" "$W/ref0/window-0.ppm" 0 0)
check "after Reset all the window is the first page again, as the reference renders it ($r0 pixels differ over everything the pages changed: bold, radio, text, slider, spinner, popup, list, split, colour, card)" eq "$r0" 0

step "BMenu: Edit > Next tab twice walks to the Data page; Edit > Clear text empties the BTextView"
menu_pick $EMX $((BARBOT + 31)); menu_pick $EMX $((BARBOT + 31)); shot m3
check "two EV tab lines arrived (1 then 2)" bash -c "[ $(ev 'tab 1') = 1 ] && [ $(ev 'tab 2') = 1 ]"
m3d=$($PIX cmp "$W/m3.png" "$W/ref2/window-0.ppm" 0 0 0 0 $((WW-1)) $((WH-28)))
check "the Data page is showing, equal to its reference above the status line ($m3d pixels differ)" eq "$m3d" 0
box textscroll; tin=$($PIX ink "$W/m3.png" $((L+3)) $((T+3)) $((R-20)) $((B-3)))
menu_pick $EMX $((BARBOT + 11)); shot m4
tout=$($PIX ink "$W/m4.png" $((L+3)) $((T+3)) $((R-20)) $((B-3)))
check "Clear text removed the text: ink in the text view went from $tin to $tout (EV clear $(ev 'clear 1'))" bash -c "[ $tin -gt 50 ] && [ $tout -eq 0 ] && [ $(ev 'clear 1') = 1 ]"

step "BAlert: a real window from the Show alert button; the compositor shows it; clicking OK answers it"
box tab0; click $CX $CY
box alertbtn; ABX=$CX; ABY=$CY
shot a0
click $ABX $ABY; sleep 0.5; shot a1
check "the alert opened (EV alert 1)" eq "$(ev 'alert 1')" 1
ALW=$(grep '^ALERT size' "$W/app.out" | cut -d' ' -f3); ALF=
for f in "$W"/live/window-*.ppm; do
  case $f in */window-0.ppm) continue;; esac
  p=$($PIX find "$W/a1.png" "$f"); if [ "$p" != none ]; then ALF=$f; AX=${p% *}; AY=${p#* }; fi
done
[ -n "$ALF" ] && ok "the alert's own frame ($(sed -n 2p "$ALF"), $(basename "$ALF")) appears verbatim in the capture at $AX,$AY" || bad "no alert window found in the capture"
sed 's/^/   | /' <(grep '^ALERT' "$W/app.out")
if [ -n "$ALF" ]; then
  d=$($PIX diff "$W/a0.png" "$W/a1.png" "$AX" "$AY" $((AX+${ALW%x*}-1)) $((AY+${ALW#*x}-1)) | cut -d' ' -f1)
  check "the alert is a separate surface with its own pixels ($d differ from the desktop behind it)" ge "$d" 1000
  OKB=$(grep '^ALERT button1' "$W/app.out" | cut -d' ' -f3); IFS=, read -r OL OT OR OB <<<"$OKB"
  ink=$($PIX ink "$W/a1.png" $((AX+OL)) $((AY+OT)) $((AX+OR)) $((AY+OB)))
  check "the OK button has ink (its label and bevel: $ink px)" ge "$ink" 60
  click $((AX + (OL+OR)/2)) $((AY + (OT+OB)/2)); sleep 0.5; shot a2
  check "clicking OK delivered the answer to the application (EV alert-which 1)" eq "$(ev 'alert-which 1')" 1
  gone=$($PIX find "$W/a2.png" "$ALF")
  check "the alert window is gone from the capture" eq "$gone" none
  box alertlabel
  check "the label on the Buttons page now reads 'Alert answered: OK' ($($PIX diff "$W/a0.png" "$W/a2.png" $L $T $((R+60)) $B | cut -d' ' -f1) pixels changed)" \
    bash -c "[ $($PIX diff "$W/a0.png" "$W/a2.png" $L $T $((R+60)) $B | cut -d' ' -f1) -gt 40 ]"
fi

step "keyboard shortcut: Alt+A (Command+A) shows the alert again; Escape closes it as Cancel"
click 300 330      # the compositor gives the keyboard to the window you click
ti "down alt" "key a" "up alt"; sleep 0.8
check "a second EV alert line arrived" eq "$(grep -c '^EV alert 1$' "$W/app.out")" 2
ti "key Escape"; sleep 0.6
check "Escape answered it as Cancel (EV alert-which 0)" eq "$(ev 'alert-which 0')" 1

step "File > Quit: the application leaves, and the server says how many opcodes it did not serve"
shot q0
menu_pick $((FMX+14)) $((BARBOT + 62))
for _ in $(seq 50); do kill -0 "$APP_PID" 2>/dev/null || break; sleep 0.1; done
if kill -0 "$APP_PID" 2>/dev/null; then bad "File > Quit did not quit the app"; kill "$APP_PID" 2>/dev/null; else ok "File > Quit ended the process"; fi
wait "$APP_PID" 2>/dev/null
unh=$(grep '^UNHANDLED' "$W/app.out" | tail -1)
echo "   | $unh"
check "aurora::unhandled_opcodes() == 0 for everything this app used: all four pages, three menus, the option popup, the alerts ($unh)" bash -c "case '$unh' in 'UNHANDLED 0 '*) true;; *) false;; esac"
check "the app wrote nothing to stderr that looks like a failure" bash -c "! grep -qiE 'segfault|abort|assert|not handled|FAIL' '$W/app.err'"

step "headless unit tests"
for t in test_controls test_widgets; do
  out=$("$BUILD/$t" 2>&1); rc=$?
  [ $rc = 0 ] && ok "$t: $(echo "$out" | tail -1)" || { bad "$t"; echo "$out" | grep FAIL; }
done

printf '\n== %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
