#!/bin/bash
# A client's shm buffer pool must not show a frame that was never drawn.
#
# This is the resize flicker, as a check. `au_wl_win_present_damage` keeps two shm buffers and
# copies only the damaged rectangles into whichever the compositor has handed back. That is
# only sound if the buffer already holds every earlier frame, and a pooled buffer does not: a
# slot allocated this frame is a zeroed memfd, and a slot written two frames ago never received
# last frame's damage. Either way the window shows pixels the app never drew.
#
# It stays hidden while the compositor releases a buffer before the next frame, because then
# slot 0 is always free and slot 1 is never allocated. A resize is when that stops being true.
#
# Two cases, because the two halves fail independently:
#   fresh    the second slot is allocated mid-sequence   -> the window would be black
#   backlog  a slot is reused one frame behind           -> an earlier frame's content is lost
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
echo "   case fresh:   a slot allocated mid-sequence must still show the whole window"
POOL_STEPS=2 "$HERE/../../harness/present-pool/run.sh" | tail -1 | grep -q '^PASS' \
  || { echo "   FAIL: a freshly allocated pool buffer showed the zeroed memfd"; exit 1; }
echo "   ok"
echo "   case backlog: a slot reused a frame behind must still show the frame it missed"
POOL_SETTLE=1 POOL_STEPS=3 "$HERE/../../harness/present-pool/run.sh" | tail -1 | grep -q '^PASS' \
  || { echo "   FAIL: a reused pool buffer lost a frame it never received"; exit 1; }
echo "   ok"
