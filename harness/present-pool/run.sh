#!/bin/bash
# Prove, or disprove, the shm buffer-pool defect behind the resize flicker.
#
# Builds harness/present-pool/client.c against the real present_wl.c, runs it under a real
# headless compositor, screenshots the result and decides from the pixels. See client.c for
# what the frame sequence is and why it forces the pool's second slot into use.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
cd "$ROOT"
OUT=${OUT:-$HERE/out}
mkdir -p "$OUT"
# A unix socket path is capped at 108 bytes, so the run dir has to be short.
R=$(mktemp -d /tmp/aupool-XXXXXX)
export XDG_RUNTIME_DIR="$R"
unset WAYLAND_DISPLAY              # never open a window on the user's real desktop
export TI="$R/ti"

BIN="$OUT/pool-client"
echo "== build"
cc -O1 -g -o "$BIN" "$HERE/client.c" \
   kits/build/interface/libaurora_wl_present.a \
   $(pkg-config --cflags --libs wayland-client xkbcommon) \
   -Ikits/build/interface/gen -lm || { echo "BUILD FAILED"; exit 2; }

./compositor/target/release/aurora-compositor --headless=800x600 --freeze-time --scale 1 \
  --test-input "$TI" > "$R/comp.log" 2>&1 &
COMP=$!
trap 'kill $COMP 2>/dev/null; kill ${APP:-0} 2>/dev/null; rm -rf "$R"' EXIT
for i in $(seq 200); do [ -S "$TI" ] && break; sleep 0.05; done
[ -S "$TI" ] || { echo "FAIL: compositor did not start"; tail -5 "$R/comp.log"; exit 2; }
DISP=$(ls "$R" | grep -m1 '^wayland-[0-9]*$')

WAYLAND_DISPLAY=$DISP POOL_STEPS=${POOL_STEPS:-3} "$BIN" > "$R/client.log" 2>&1 &
APP=$!
for i in $(seq 100); do grep -q 'presented; holding' "$R/client.log" && break; sleep 0.1; done
sleep 1
cat "$R/client.log"
./harness/capture/target/release/aurora-capture --display "$DISP" -o "$OUT/frame.png" >/dev/null 2>&1 \
  || { echo "FAIL: capture"; exit 2; }

python3 "$HERE/check.py" "$OUT/frame.png" "${POOL_STEPS:-3}"
