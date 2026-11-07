#!/bin/bash
# A drag must not cost one window reallocation per pointer motion.
#
# The compositor's resize grab sends an xdg configure on EVERY pointer motion, so a drag
# delivers a burst of them between two frames. The Haiku app_server used to apply each one:
# a full ApplyWindowResize, which reallocates the window's pixel buffer (8.2 MB at 1280x800
# scale 1.45), walks the view tree applying follow modes, posts B_WINDOW_RESIZED and dirties
# the whole window -- for sizes nobody would ever see. That was the chunk in a chunky drag.
#
# It also lost the drag: with the storm on, the window never reached the size it was dragged
# to. So this checks both, because the cheap version that gets the size wrong is not a fix.
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
OUT=$("$HERE/../../harness/present-pool/chunky.sh" 2>&1)
echo "$OUT" | sed 's/^/  /'
N=$(echo "$OUT" | sed -n 's/.*ApplyWindowResize calls: *//p')
SZ=$(echo "$OUT" | sed -n 's/.*final size: *//p')
[ -n "$N" ] || { echo "   FAIL: could not read the resize count"; exit 1; }
# 60 motion events. A handful of bursts is expected; one per motion is the defect.
[ "$N" -le 8 ] || { echo "   FAIL: $N reallocations for 60 motion events; resizes are not being coalesced"; exit 1; }
[ "$SZ" != "260x120" ] || { echo "   FAIL: the window never left its starting size, so the drag was lost"; exit 1; }
echo "   ok: $N reallocations for 60 motion events, and the drag landed at $SZ"
