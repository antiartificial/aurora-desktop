#!/usr/bin/env bash
# Theme drift: two programs read contracts/theme/aurora-glass.toml, the compositor (Rust, fx/theme.rs) and the Interface
# Kit (C++, kits/interface/aurora_theme.cpp). They must agree, byte for byte, on what a colour token is, or a Haiku
# app's panel stops matching the desktop it sits on. This pins that in four independent ways, none of which trusts
# either reader's description of itself:
#   1. ORACLE. harness/theme-oracle includes compositor/src/fx/theme.rs unmodified and prints the sRGB bytes it derives
#      for every [theme.colors] token. The toolkit's reader, and ui_color() through the real colour table, must print the
#      same bytes. (Same file, then 400 random tokens: out-of-gamut, greys, extremes, ties at .5 of a code.)
#   2. THE COMPOSITOR'S PIXELS. A headless compositor draws a solid desktop of one token; its own capture is decoded and the
#      pixel must be the oracle's bytes. Run for several tokens by hot-reloading the theme file.
#   3. A HAIKU APP'S PIXELS. The kitchen sink runs on that compositor with that theme. A pixel of its panel and a pixel of the
#      desktop, when the theme's desktop colour IS the panel token, must be the same three bytes.
#   4. A DIFFERENT PALETTE. The same run with another palette in the file, so nothing above is true only of today's colours.
# Prints a pass/fail count; exits non-zero on any failure. Nothing is screenshotted from the host.
# Needs: cmake, ninja, g++, cargo (offline: the compositor's own crates), python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
KITS=$ROOT/kits; BUILD=$KITS/build/compat
COMP=$ROOT/compositor/target/release/aurora-compositor
CAP=$ROOT/harness/capture/target/release/aurora-capture
ORACLE=$ROOT/harness/theme-oracle/target/release/theme-oracle
THEME=$ROOT/contracts/theme/aurora-glass.toml
PIX="python3 -I $ROOT/demos/kitchen-sink/pix.py"; TI="python3 -I $ROOT/demos/kitchen-sink/ti.py"
W=$(mktemp -d "${TMPDIR:-/tmp}/theme-drift.XXXXXX")
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/td.XXXXXX"); chmod 700 "$RUN"
PIDS=(); PASS=0; FAIL=0
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && [ "$p" != "$$" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null; rm -rf "${W:?}" "${RUN:?}"; }
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
check() { local msg=$1; shift; if "$@"; then ok "$msg"; else bad "$msg"; fi; }
eq() { [ "$1" = "$2" ]; }

step "build: the toolkit's dumper, the kitchen sink, the oracle (the compositor's theme.rs, unmodified)"
(cd "$KITS" && cmake -S . -B build -G Ninja >"$W/cmake.log" 2>&1 && ninja -C build test_theme iface_kitchensink >"$W/ninja.log" 2>&1) || { tail -20 "$W/cmake.log" "$W/ninja.log"; echo "FATAL: kits build"; exit 2; }
(cd "$ROOT/harness/theme-oracle" && cargo build --release --offline >"$W/oracle-build.log" 2>&1) || { tail "$W/oracle-build.log"; echo "FATAL: oracle build"; exit 2; }
[ -x "$COMP" ] || (cd "$ROOT/compositor" && cargo build --release >"$W/comp-build.log" 2>&1) || { tail "$W/comp-build.log"; echo "FATAL: compositor build"; exit 2; }
[ -x "$CAP" ] || (cd "$ROOT/harness/capture" && cargo build --release >"$W/cap-build.log" 2>&1) || { tail "$W/cap-build.log"; echo "FATAL: capture build"; exit 2; }
ok "built"

# oracle FILE -> "name r g b" lines;  toolkit DIR -> the same from the C++ reader (tok) and from ui_color() (ui)
oracle() { "$ORACLE" "$1" | sort; }
toolkit_tok() { env AURORA_THEME_DIR="$1" "$BUILD/test_theme" --dump | sed -n 's/^tok //p' | sort; }
toolkit_ui()  { env AURORA_THEME_DIR="$1" "$BUILD/test_theme" --dump | sed -n 's/^ui //p' | sort -u; }

step "1. the shipped theme file: the compositor's bytes vs the toolkit's"
mkdir -p "$W/shipped"; cp "$THEME" "$W/shipped/aurora-glass.toml"
oracle "$THEME" >"$W/oracle.txt"; toolkit_tok "$W/shipped" >"$W/tok.txt"; toolkit_ui "$W/shipped" >"$W/ui.txt"
n=$(wc -l <"$W/oracle.txt")
check "$n tokens: the toolkit's reader prints exactly the bytes the compositor's code derives (diff: $(diff "$W/oracle.txt" "$W/tok.txt" | grep -c '^[<>]') lines)" diff -q "$W/oracle.txt" "$W/tok.txt"
# every ui_color() answer is some token's bytes: join on the token name
bad_ui=0; nui=0
while read -r name r g b; do
  nui=$((nui+1)); want=$(grep "^$name " "$W/oracle.txt" | cut -d' ' -f2-)
  [ "$want" = "$r $g $b" ] || { bad_ui=$((bad_ui+1)); echo "   $name: ui_color $r $g $b, oracle $want" >&2; }
done <"$W/ui.txt"
check "$nui colour_which mappings: ui_color() answers the oracle's bytes for each ($bad_ui differ)" eq "$bad_ui" 0
check "the compositor still parses the file with the toolkit tables in it (the oracle ran Theme::parse on it)" test -s "$W/oracle.txt"

step "1b. 400 random tokens (the Rust oracle vs the C++ rule, bit for bit)"
mkdir -p "$W/rand"
python3 -I - "$W/rand/aurora-glass.toml" <<'PY'
import random, sys
random.seed(20261106)
out = ['[theme]\nname = "drift"\nversion = 1\n', '[theme.colors]']
fmt = lambda v: repr(round(v, random.choice([1, 2, 3, 4])))
for i in range(400):
    k = i % 8
    if k == 0:   lab = [random.random(), random.uniform(-.45, .45), random.uniform(-.45, .45)]       # anything, out of gamut included
    elif k == 1: lab = [random.random(), 0.0, 0.0]                                                   # greys
    elif k == 2: lab = [random.choice([0.0, 1.0, 0.5, 0.6, 0.999]), random.uniform(-.1, .1), random.uniform(-.1, .1)]
    elif k == 3: lab = [random.uniform(.2, .9), random.uniform(-.04, .04), random.uniform(-.04, .04)] # near-neutral: where rounding ties sit
    else:        lab = [random.uniform(.05, .98), random.uniform(-.15, .15), random.uniform(-.15, .15)]
    out.append('t%03d = [%s, %s, %s]' % (i, fmt(lab[0]), fmt(lab[1]), fmt(lab[2])))
open(sys.argv[1], 'w').write('\n'.join(out) + '\n')
PY
oracle "$W/rand/aurora-glass.toml" >"$W/rand-oracle.txt"
# the toolkit overlays the file on its built-in copy (a key the file lacks keeps the default), so it also knows the shipped
# tokens this file does not mention; compare the tokens the FILE defines
toolkit_tok "$W/rand" | awk 'NR==FNR { k[$1]; next } $1 in k' "$W/rand-oracle.txt" - >"$W/rand-tok.txt"
check "$(grep -c '^theme.colors.t' "$W/rand-oracle.txt") random tokens: identical bytes (differing lines: $(diff "$W/rand-oracle.txt" "$W/rand-tok.txt" | grep -c '^[<>]'))" diff -q "$W/rand-oracle.txt" "$W/rand-tok.txt"

# ---- a compositor that draws a solid desktop of one token ---------------------------------------------------------------
# theme_with_desktop FILE-IN DIR OKLAB -> DIR/aurora-glass.toml: FILE-IN with [background] replaced by a solid desktop
theme_with_desktop() {
  python3 -I - "$1" "$2/aurora-glass.toml" "$3" <<'PY'
import sys
src, dst, lab = sys.argv[1:4]
keep, out = True, []
for l in open(src).read().splitlines():
    if l.startswith('['): keep = l.strip() != '[background]'
    if keep: out.append(l)
out += ['', '[background]', 'mode = "solid"', 'top = %s' % lab]
open(dst, 'w').write('\n'.join(out) + '\n')
PY
}
token_lab() { grep -E "^$2 *=" "$1" | head -1 | sed 's/^[^=]*= *//; s/ *#.*//'; }

start_compositor() {  # theme-dir
  rm -rf "$RUN"/wayland-* "$RUN/ti"
  env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$RUN" "$COMP" --headless=1280x800 --test-input "$RUN/ti" --theme "$1" >"$W/comp.log" 2>&1 & PIDS+=($!)
  SOCK=
  for _ in $(seq 100); do
    for s in "$RUN"/wayland-[0-9]*; do case $s in *.lock) ;; *) [ -S "$s" ] && SOCK=$(basename "$s");; esac; done
    [ -n "$SOCK" ] && break; sleep 0.1
  done
  [ -n "$SOCK" ] || { tail "$W/comp.log"; echo "FATAL: compositor socket never bound"; exit 2; }
}
shot() { XDG_RUNTIME_DIR="$RUN" "$CAP" --display "$SOCK" -o "$W/$1.png" >"$W/cap.log" 2>&1 || { cat "$W/cap.log"; bad "capture $1 failed"; }; }

step "2. the compositor's own pixels: a solid desktop of token T, hot-reloaded token by token, decoded from its capture"
mkdir -p "$W/live"; theme_with_desktop "$THEME" "$W/live" "$(token_lab "$THEME" panel)"
start_compositor "$W/live"
sleep 0.8
for tok in panel panel_text accent control_border document list_selected success failure scroll_thumb; do
  theme_with_desktop "$THEME" "$W/live" "$(token_lab "$THEME" $tok)"; sleep 0.9
  shot "bg-$tok"
  got=$($PIX px "$W/bg-$tok.png" 200 300); want=$(grep "^theme.colors.$tok " "$W/oracle.txt" | cut -d' ' -f2-)
  check "desktop = token '$tok': the compositor drew $got, its own oklab_to_u8 says $want" eq "$got" "$want"
done
# a second palette in the same file: the dark one test_theme uses, so none of this is true of one set of numbers only
for lab in "[0.28, 0.01, -0.03]" "[0.95, 0.0, 0.0]" "[0.62, 0.0, -0.03]"; do
  theme_with_desktop "$THEME" "$W/live" "$lab"; sleep 0.9; shot bg-x
  mkdir -p "$W/one"; printf '[theme.colors]\nx = %s\n' "$lab" >"$W/one/aurora-glass.toml"
  want=$(oracle "$W/one/aurora-glass.toml" | grep '^theme.colors.x ' | cut -d' ' -f2-); got=$($PIX px "$W/bg-x.png" 200 300)
  check "desktop = $lab: the compositor drew $got, the oracle says $want" eq "$got" "$want"
done

step "3. a Haiku app's panel vs the compositor's desktop, one token, one file"
# The desktop is the panel token; the kitchen sink reads the same file (AURORA_THEME_DIR). Its panel pixel and the
# desktop pixel are two programs' reading of one token.
mkdir -p "$W/e2e"; theme_with_desktop "$THEME" "$W/e2e" "$(token_lab "$THEME" panel)"
kill "${PIDS[0]}" 2>/dev/null; wait "${PIDS[0]}" 2>/dev/null; PIDS=()
start_compositor "$W/e2e"
mkdir -p "$W/app"
env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_THEME_DIR="$W/e2e" AURORA_APPSERVER_DUMP="$W/app" KS_REPORT=1 HELLO_QUIT_AFTER_MS=60000 \
    "$BUILD/iface_kitchensink" >"$W/app.out" 2>"$W/app.err" & PIDS+=($!)
for _ in $(seq 150); do grep -q '^REPORT done' "$W/app.out" && break; sleep 0.1; done
grep -q '^REPORT done' "$W/app.out" || { cat "$W/app.err" "$W/app.out"; echo "FATAL: the app never finished its layout report"; exit 2; }
sleep 0.5; shot e2e
read -r WW WH < <(sed -n 2p "$W/app/window-0.ppm")
# a spot of the Buttons page with nothing on it: right of the alert row, in the page's panel
l=$(grep '^CTL alertlabel ' "$W/app.out" | cut -d' ' -f3); IFS=, read -r AL AT AR AB <<<"$l"
PX=$((AR + 120)); PY=$(((AT + AB) / 2))
# Locate the window rather than assume the origin: windows are decorated and
# cascade-placed now, so a frame is rarely at 0,0. The property this step needs
# is that the app's frame appears on the compositor VERBATIM -- found at all,
# byte for byte -- not that it appears at any particular place.
WPOS=$($PIX find "$W/e2e.png" "$W/app/window-0.ppm")
check "the app's frame is on the compositor verbatim, found at ($WPOS), so the capture's pixel is the app's own" bash -c "[ '$WPOS' != none ]"
read -r WX WY <<<"$WPOS"; [ "$WPOS" = none ] && { WX=0; WY=0; }
app_px=$($PIX px "$W/e2e.png" $((WX + PX)) $((WY + PY))); desk_px=$($PIX px "$W/e2e.png" 900 500); want=$(grep '^theme.colors.panel ' "$W/oracle.txt" | cut -d' ' -f2-)
check "the Haiku app's panel pixel ($PX,$PY) is $app_px; the oracle's panel token is $want" eq "$app_px" "$want"
check "the compositor's desktop pixel is $desk_px: the same three bytes as the Haiku app's panel" eq "$app_px" "$desk_px"

step "4. the same, with another palette in the file (panel and desktop both [0.28, 0.01, -0.03])"
kill "${PIDS[@]}" 2>/dev/null; wait 2>/dev/null; PIDS=()
mkdir -p "$W/dark"
python3 -I - "$THEME" "$W/dark/aurora-glass.toml" <<'PY'
import sys
src, dst = sys.argv[1:3]
t = open(src).read().replace('panel = [0.94, -0.002, -0.007]', 'panel = [0.28, 0.01, -0.03]').replace('panel_text = [0.24, -0.002, -0.012]', 'panel_text = [0.95, 0.0, -0.01]')
assert 'panel = [0.28' in t
keep, out = True, []
for l in t.splitlines():
    if l.startswith('['): keep = l.strip() != '[background]'
    if keep: out.append(l)
out += ['', '[background]', 'mode = "solid"', 'top = [0.28, 0.01, -0.03]']
open(dst, 'w').write('\n'.join(out) + '\n')
PY
start_compositor "$W/dark"
mkdir -p "$W/app2"
env XDG_RUNTIME_DIR="$RUN" WAYLAND_DISPLAY="$SOCK" AURORA_THEME_DIR="$W/dark" AURORA_APPSERVER_DUMP="$W/app2" KS_REPORT=1 HELLO_QUIT_AFTER_MS=60000 \
    "$BUILD/iface_kitchensink" >"$W/app2.out" 2>"$W/app2.err" & PIDS+=($!)
for _ in $(seq 150); do grep -q '^REPORT done' "$W/app2.out" && break; sleep 0.1; done
sleep 0.5; shot e2e2
want=$(oracle "$W/dark/aurora-glass.toml" | grep '^theme.colors.panel ' | cut -d' ' -f2-)
app_px=$($PIX px "$W/e2e2.png" $PX $PY); desk_px=$($PIX px "$W/e2e2.png" 900 500)
check "dark palette: the Haiku app's panel pixel is $app_px, the oracle says $want, the desktop is $desk_px" bash -c "[ '$app_px' = '$want' ] && [ '$app_px' = '$desk_px' ]"
check "and it is not today's panel: the shipped panel token is $(grep '^theme.colors.panel ' "$W/oracle.txt" | cut -d' ' -f2-)" bash -c "[ '$app_px' != '$(grep '^theme.colors.panel ' "$W/oracle.txt" | cut -d' ' -f2-)' ]"

printf '\n== %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
