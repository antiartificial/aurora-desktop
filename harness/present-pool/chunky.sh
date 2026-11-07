#!/bin/bash
# How much work does one resize drag make a client do?
#
# The compositor's resize grab sends an xdg configure on EVERY pointer motion, with no
# throttling. aurora::ui coalesces them (it records the wanted size and the loop applies the
# latest), but the Haiku app_server does not: each AU_EV_RESIZE runs a full ApplyWindowResize,
# which reallocates the window's pixel buffer, walks the whole view tree applying follow modes,
# posts B_WINDOW_RESIZED to the client looper and dirties the entire window.
#
# So this counts ApplyWindowResize calls against frames actually presented. A client that is
# doing the work of a hundred resizes to show ten frames is the "chunky" drag.
set -u
N=${N:-60}
R=$(mktemp -d /tmp/auchunk-XXXXXX)
export XDG_RUNTIME_DIR="$R"
unset WAYLAND_DISPLAY
cd /home/arti/projects/aurora
export TI="$R/ti"
./compositor/target/release/aurora-compositor --headless=1400x900 --scale "${SCALE:-1.45}" \
  --test-input "$TI" > "$R/comp.log" 2>&1 &
COMP=$!
trap 'kill $COMP 2>/dev/null; kill ${APP:-0} 2>/dev/null; rm -rf "$R"' EXIT
for i in $(seq 200); do [ -S "$TI" ] && break; sleep 0.05; done
DISP=$(ls "$R" | grep -m1 '^wayland-[0-9]*$')
AURORA_APPSERVER_TRACE=1 WAYLAND_DISPLAY=$DISP ./kits/build/interface/iface_controls \
  > "$R/app.log" 2>"$R/app.err" &
APP=$!
sleep 3
TI="$TI" N="$N" python3 - <<'PY'
import socket, os, json
TI=os.environ["TI"]; N=int(os.environ["N"])
def ti(c):
    s=socket.socket(socket.AF_UNIX); s.settimeout(20); s.connect(TI)
    s.sendall((c+"\n").encode()); return s.makefile().readline().strip()
st=json.loads(ti("state")[3:]); w=st["windows"][0]
rx,by = w["x"]+w["w"]+4, w["y"]+w["h"]+4
print(f"  dragging the bottom-right grip, {N} motion events")
ti(f"pointer {rx} {by}"); ti("press")
for k in range(N):
    ti(f"pointer {rx+k*6} {by+k*4}")
ti("release")
ti("frame 2")
st=json.loads(ti("state")[3:])
print(f"  final size: {st['windows'][0]['w']}x{st['windows'][0]['h']}")
PY
echo "  motion events sent:          ${N}"
echo "  ApplyWindowResize calls:     $(grep -c 'RESIZE to' "$R/app.err" 2>/dev/null || echo 0)"
