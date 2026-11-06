#!/usr/bin/env bash
# Phase 2 exit demo: isolate, grant, snapshot, destroy, rollback. Leaves nothing behind.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "$HOME/.aurora-demo-XXXXXX")   # under $HOME so it is btrfs here and a real "user file" location
export AURORA_DOMAINS_DIR="$work/domains"
bin="$work/domain"
D=demo
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
cleanup() { "$bin" destroy "$D" >/dev/null 2>&1 || true; chmod -R u+rwx "$work" 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT

(cd "$here" && go build -o "$bin" ./cmd/domain)
echo "SECRET: my-tax-return" > "$work/secret.txt"
echo "PHOTO: lake.png"       > "$work/photo.png"
echo "sibling, never granted" > "$work/other.txt"

step "1. create a domain"
"$bin" create $D

step "2. the default domain cannot read a user file (expect failure)"
echo "\$ domain run $D -- cat $work/secret.txt"
if "$bin" run $D -- cat "$work/secret.txt"; then echo "!! LEAK"; exit 1; else echo "-> denied, as intended"; fi
echo "\$ domain run $D -- ls / /home"
"$bin" run $D -- ls / /home 2>&1 || true

step "3. no network by default (expect failure)"
if "$bin" run $D -- bash -c 'exec 3<>/dev/tcp/1.1.1.1/53' 2>/dev/null; then echo "!! NET"; exit 1; else echo "-> unreachable, as intended"; fi

step "4. grant exactly one file, read-only"
"$bin" grant $D "$work/photo.png" --ro
"$bin" run $D -- cat "$work/photo.png"
echo "sibling files stay hidden:"
if "$bin" run $D -- cat "$work/secret.txt" 2>/dev/null; then echo "!! LEAK"; exit 1; else echo "-> secret.txt still denied"; fi
if "$bin" run $D -- sh -c "echo x >> $work/photo.png" 2>/dev/null; then echo "!! WRITABLE"; exit 1; else echo "-> ro grant not writable"; fi

step "5. show the domain"
"$bin" show $D

step "6. write work inside the domain, then snapshot"
"$bin" run $D -- sh -c 'echo "chapter one" > /domain/data/novel.txt; ls -l /domain/data'
"$bin" snapshot $D --tag before

step "7. delete everything inside the domain"
"$bin" run $D -- sh -c 'rm -f /domain/data/*; echo "data dir now:"; ls -A /domain/data | wc -l'

step "8. roll back"
"$bin" rollback $D before
"$bin" run $D -- cat /domain/data/novel.txt

step "9. destroy"
"$bin" destroy $D
"$bin" list
[ ! -e "$AURORA_DOMAINS_DIR/$D" ] && echo "nothing left on disk"
echo; echo "demo complete"
