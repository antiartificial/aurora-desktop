#!/bin/sh
# aurora-chooser as its caller sees it, with no display. Usage: chooser_cli.sh path/to/aurora-chooser
#
# Each case runs the real binary and checks two things a Go caller depends on: the exit code, and that stdout is
# byte-for-byte empty, because anything on stdout is parsed as a grant.
BIN=$1
fails=0
# No compositor reachable: no WAYLAND_DISPLAY, and a runtime dir with no socket in it.
EMPTY=$(mktemp -d)
trap 'rmdir "$EMPTY"' EXIT

check() {
    want=$1; shift
    out=$(env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$EMPTY" "$BIN" "$@" 2>/dev/null; echo "rc=$?")
    if [ "$out" != "rc=$want" ]; then
        printf 'FAIL [%s] want exit %s and empty stdout, got: %s\n' "$*" "$want" "$out" >&2
        fails=$((fails + 1))
    fi
}

check 2  --app Notes --verb open   --mode ro --kind file
check 2  --app Notes --verb open   --mode rw --kind file
check 2  --app Notes --verb export --mode rw --kind save --suggest out.txt --start /tmp
check 2  --app Notes --verb scan   --mode ro --kind dir  --start /does/not/exist
check 64
check 64 --app Notes --verb open --mode ro
check 64 --app Notes --verb open --mode readwrite --kind file
check 64 --app Notes --verb open --mode ro --kind folder
check 64 --app Notes --verb open --mode ro --kind file --bogus
check 64 --help

[ "$fails" = 0 ] && echo "chooser_cli: ok"
exit "$fails"
