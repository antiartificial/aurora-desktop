#!/usr/bin/env bash
# Standalone Lumen demo: no bus, no controller. Needs only Go.
set -euo pipefail
cd "$(dirname "$0")"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

go build -o "$work/lumen" ./cmd/lumen
go build -o "$work/genimage" ./cmd/genimage
mkdir "$work/out"
"$work/genimage" "$work/in.png"
size() { printf '  %-14s %8d bytes\n' "$1" "$(stat -c %s "$2")"; }

echo "== input"; size in.png "$work/in.png"
echo "== resize (subcommand form): 1200x800 -> width 300"
"$work/lumen" resize --input "$work/in.png" --output "$work/out/small.png" --width 300
size small.png "$work/out/small.png"
echo "== export png -> jpeg q80 (JSON form)"
echo "{\"input\":\"$work/in.png\",\"output\":\"$work/out/photo.jpg\",\"args\":{\"format\":\"jpeg\",\"quality\":80}}" |
  "$work/lumen" --verb export@v1
size photo.jpg "$work/out/photo.jpg"
echo "== export to avif (must fail cleanly)"
"$work/lumen" export --input "$work/in.png" --output "$work/out/x.avif" --format avif 2>/dev/null || echo "  exit status $?"
echo "== histogram (JSON form, abbreviated)"
echo "{\"input\":\"$work/in.png\"}" | "$work/lumen" --verb histogram@v1 |
  sed -E 's/"(red|green|blue|alpha)":\[([0-9]+,[0-9]+,[0-9]+),[^]]*\]/"\1":[\2,...]/g'
echo "== files in output dir (histogram wrote none)"
ls "$work/out"
echo "done; cleaning up"
