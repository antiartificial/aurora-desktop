#!/bin/sh
# Re-render png/ from the two SVGs. Needs rsvg-convert or resvg; the PNGs are checked in so the
# install does not. Sizes up to 32 use aurora-small.svg (the 16px design), larger use aurora.svg.
set -eu
cd "$(dirname "$0")"
for s in 16 22 24 32 48 64 128 256; do
  f=aurora.svg; [ "$s" -le 32 ] && f=aurora-small.svg
  if command -v rsvg-convert >/dev/null; then rsvg-convert -w "$s" -h "$s" -o "png/$s.png" "$f"
  elif command -v resvg >/dev/null; then resvg -w "$s" -h "$s" "$f" "png/$s.png"
  else echo "render.sh: need rsvg-convert or resvg" >&2; exit 1; fi
done
