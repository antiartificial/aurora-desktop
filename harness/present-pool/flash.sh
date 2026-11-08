#!/bin/bash
# Does a window flash WHITE on resize?
#
# The app_server answers a resize by reallocating the window's pixel buffer and then posting
# B_WINDOW_RESIZED to the client, which repaints whenever it gets round to it. Every frame
# presented in between shows whatever the reallocation left behind. Canvas::Init leaves opaque
# white, so it showed a white rectangle the size of the window.
#
# This captures the frame IMMEDIATELY after the configure, before the client has repainted, and
# counts pure white inside the window. The app's own body is light grey (0xEDEDED), never
# 0xFFFFFF, so pure white is unambiguous.
set -u
APPBIN=${1:-iface_controls}
OUT=${OUT:-$(cd "$(dirname "$0")" && pwd)/out}
mkdir -p "$OUT"
R=$(mktemp -d /tmp/auflash-XXXXXX)
export XDG_RUNTIME_DIR="$R"
unset WAYLAND_DISPLAY
cd /home/arti/projects/aurora
export TI="$R/ti"
./compositor/target/release/aurora-compositor --headless=1200x800 --freeze-time \
  --scale "${SCALE:-1.45}" --test-input "$TI" > "$R/comp.log" 2>&1 &
COMP=$!
trap 'kill $COMP 2>/dev/null; kill ${APP:-0} 2>/dev/null; rm -rf "$R"' EXIT
for i in $(seq 200); do [ -S "$TI" ] && break; sleep 0.05; done
DISP=$(ls "$R" | grep -m1 '^wayland-[0-9]*$')
WAYLAND_DISPLAY=$DISP ./kits/build/compat/$APPBIN > "$R/app.log" 2>&1 &
APP=$!
sleep 3
TI="$TI" DISP="$DISP" OUT="$OUT" python3 - <<'PY'
import socket, os, json, subprocess, sys
TI, OUT, DISP = os.environ["TI"], os.environ["OUT"], os.environ["DISP"]
def ti(c):
    s=socket.socket(socket.AF_UNIX); s.settimeout(20); s.connect(TI)
    s.sendall((c+"\n").encode()); return s.makefile().readline().strip()
st=json.loads(ti("state")[3:])
if not st["windows"]:
    print("FAIL: no window"); sys.exit(2)
w=st["windows"][0]
print(f"window {w['w']}x{w['h']} at {w['x']},{w['y']}")
ti("pointer 1 1"); ti("frame 2")
# Grow it, then take the very next frame: the client has not repainted yet.
ti(f"resize 0 {w['w']+260} {w['h']+200}")
ti("frame 1")
subprocess.run(["./harness/capture/target/release/aurora-capture","--display",DISP,
                "-o",f"{OUT}/flash.png"],capture_output=True)
print("captured the frame right after the configure")
PY
python3 "$(cd "$(dirname "$0")" && pwd)/white.py" "$OUT/flash.png"
