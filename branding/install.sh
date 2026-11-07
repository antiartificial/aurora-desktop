#!/bin/sh
# Install the Aurora launcher icon and .desktop entry for the current user. Idempotent, no sudo.
# The window's app_id (compositor/src/winit.rs APP_ID) must equal the .desktop basename and Icon=.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
id=org.aurora.Desktop
data=${XDG_DATA_HOME:-$HOME/.local/share}
icons=$data/icons/hicolor

mkdir -p "$icons/scalable/apps" "$data/applications"
install -m 644 "$here/aurora.svg" "$icons/scalable/apps/$id.svg"
for p in "$here"/png/*.png; do
  s=$(basename "$p" .png)
  mkdir -p "$icons/${s}x${s}/apps"
  install -m 644 "$p" "$icons/${s}x${s}/apps/$id.png"
done
sed "s|@REPO@|$repo|" "$here/$id.desktop" > "$data/applications/$id.desktop"
chmod 644 "$data/applications/$id.desktop"

command -v desktop-file-validate >/dev/null && desktop-file-validate "$data/applications/$id.desktop" || true
command -v update-desktop-database >/dev/null && update-desktop-database "$data/applications" || true
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t -f "$icons" || true
command -v kbuildsycoca6 >/dev/null && kbuildsycoca6 >/dev/null 2>&1 || true
echo "installed $id: icons under $icons, entry $data/applications/$id.desktop"
