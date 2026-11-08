#!/bin/sh
# aurora-chooser driven for real: the compositor headless, the chooser on it, and the keyboard through its test-input
# socket. Only keys are used, so nothing depends on where the window lands. Checks what a caller sees: exit code and stdout.
#
#   chooser_ui.sh path/to/aurora-chooser
#
# Needs the compositor (default ../../compositor/target/release/aurora-compositor from the repo root, or $AURORA_COMPOSITOR).
# Without it the test exits 77, which ctest reports as skipped rather than passed.
BIN=$1
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
COMP=${AURORA_COMPOSITOR:-$ROOT/compositor/target/release/aurora-compositor}
TI="python3 -I $ROOT/demos/kitchen-sink/ti.py"
[ -x "$COMP" ] || { echo "chooser_ui: no compositor at $COMP, skipping" >&2; exit 77; }

# A short path: the compositor's socket path has a 108-byte limit.
R=$(mktemp -d /tmp/chu.XXXXXX)
D=$R/home
mkdir -p "$D/sub"
printf a > "$D/a.txt"
printf b > "$D/b.txt"
CP=
trap 'kill $CP 2>/dev/null; rm -rf "$R"' EXIT
env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$R" "$COMP" --headless=1280x800 --test-input "$R/ti" >"$R/comp.log" 2>&1 &
CP=$!
for _ in $(seq 200); do [ -S "$R/ti" ] && grep -aq 'socket wayland' "$R/comp.log" && break; sleep 0.05; done
SOCK=$(grep -aoE 'socket wayland-[0-9]+' "$R/comp.log" | head -1 | awk '{print $2}')
[ -n "$SOCK" ] || { echo "chooser_ui: the compositor made no socket" >&2; exit 1; }

fails=0
# run_case NAME WANT_RC WANT_STDOUT KEYS... -- the chooser args come from $ARGS; stdout is compared byte for byte.
run_case() {
    name=$1; want_rc=$2; want_out=$3; shift 3
    rm -f "$R/out" "$R/rc"
    ( env XDG_RUNTIME_DIR="$R" WAYLAND_DISPLAY="$SOCK" HOME="$D" "$BIN" $ARGS >"$R/out" 2>"$R/err"; echo $? >"$R/rc" ) &
    sleep 1.2
    $TI "$R/ti" --pause 0.12 "$@" >/dev/null 2>&1
    for _ in $(seq 40); do [ -e "$R/rc" ] && break; sleep 0.1; done
    if [ ! -e "$R/rc" ]; then
        # Still open: it was meant to be. Close it so the next case starts clean, and say it did not exit.
        $TI "$R/ti" --pause 0.12 "key Escape" >/dev/null 2>&1
        for _ in $(seq 40); do [ -e "$R/rc" ] && break; sleep 0.1; done
        got_rc=still-open
    else
        got_rc=$(cat "$R/rc")
    fi
    got_out=$(cat "$R/out"; echo x); got_out=${got_out%x}
    if [ "$got_rc" != "$want_rc" ] || [ "$got_out" != "$want_out" ]; then
        printf 'FAIL %s: want rc=%s stdout=[%s], got rc=%s stdout=[%s]\n' "$name" "$want_rc" "$want_out" "$got_rc" "$got_out" >&2
        sed 's/^/    stderr: /' "$R/err" >&2
        fails=$((fails + 1))
    fi
}

nl='
'
ARGS="--app Notes --verb open --mode ro --kind file --start $D"
# Folders sort first, so the rows are sub/, a.txt, b.txt.
run_case "arrows then Enter grant the file"    0 "{\"path\":\"$D/a.txt\"}$nl" "key Down" "key Down" "key Return"
run_case "one more arrow moves the selection"  0 "{\"path\":\"$D/b.txt\"}$nl" "key Down" "key Down" "key Down" "key Return"
run_case "Escape cancels, stdout empty"        3 ""                            "key Escape"
run_case "Enter on a folder row opens it and grants nothing" 3 "" "key Down" "key Return" "key Escape"
run_case "Ctrl+Enter with nothing selected grants nothing" 3 "" "down ctrl" "key Return" "up ctrl" "key Escape"
run_case "a file row grants with Ctrl+Enter"   0 "{\"path\":\"$D/a.txt\"}$nl" "key Down" "key Down" "down ctrl" "key Return" "up ctrl"

ARGS="--app Notes --verb export --mode rw --kind save --suggest out.txt --start $D"
run_case "save: the suggested name is granted on Enter" 0 "{\"path\":\"$D/out.txt\"}$nl" "key Return"
ARGS="--app Notes --verb export --mode rw --kind save --suggest sub --start $D"
run_case "save: a name that is a folder is refused" 3 "" "key Return" "key Escape"
ARGS="--app Notes --verb export --mode rw --kind save --suggest a.txt --start $D"
run_case "save: replacing an existing file is allowed" 0 "{\"path\":\"$D/a.txt\"}$nl" "key Return"

ARGS="--app Notes --verb scan --mode ro --kind dir --start $D"
run_case "dir: Ctrl+Enter grants the folder on show" 0 "{\"path\":\"$D\"}$nl" "down ctrl" "key Return" "up ctrl"
run_case "dir: Enter alone grants nothing"           3 ""                       "key Return" "key Escape"

[ "$fails" = 0 ] && echo "chooser_ui: ok"
exit "$fails"
