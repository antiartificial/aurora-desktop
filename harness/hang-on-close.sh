#!/bin/bash
# Reproduce the hang-on-close bug. N=14 reproduces it in roughly a third of runs.
#
# DIAGNOSIS (from the thread states this script captures, plus kits/src/app/Looper.cpp):
#
#   BLooper::Quit() does
#       write_port_etc(fPort, kPortCodeQuit, NULL, 0, B_RELATIVE_TIMEOUT, 0);  // return DISCARDED
#       status_t rv; wait_for_thread(fThread, &rv);                            // waits FOREVER
#
#   and BLooper::RunLoop() does
#       while (!fTerminating) { wire_receive(fPort, B_INFINITE_TIMEOUT, ...); }
#
#   The quit write is non-blocking and its result is thrown away. If the port is full at that
#   instant -- plausible during a close, with pointer and redraw traffic in flight -- the quit
#   code never arrives. Setting fTerminating cannot help, because the loop only tests it BETWEEN
#   messages while blocked in an INFINITE receive. So the looper sleeps forever and
#   wait_for_thread blocks forever.
#
#   Signature this script prints when it catches one: the main thread in futex_do_wait
#   (wait_for_thread), the looper in futex_do_wait (blocked receive), and one thread in
#   poll_schedule_timeout (the presenter). Nondeterministic because it depends on whether the
#   port happened to be full.
#
# THE FIX, not yet applied: make the quit write blocking (or retry, and CHECK the return), and
# give the receive a finite timeout so the loop re-tests fTerminating. Belt and braces -- either
# alone closes the window of this race, both together close it properly.
#
# Original reproduce-the-hang-on-close: compositor headless, one Haiku app, click the title bar's
# close button, and see whether the process exits. Captures a backtrace when it does not.
# Only ever kills pids this script started.
set -u
ROOT=/home/arti/projects/aurora
COMP=$ROOT/compositor/target/release/aurora-compositor
TI="python3 -I $ROOT/demos/kitchen-sink/ti.py"
APP=${APP:-$ROOT/kits/build/interface/iface_controls}
N=${N:-10}
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT
hangs=0
for i in $(seq "$N"); do
  R=$W/r$i; mkdir -p "$R"
  env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$R" "$COMP" --headless=1280x800 --test-input "$R/ti" \
      >"$R/comp.log" 2>&1 & CP=$!
  for _ in $(seq 200); do [ -S "$R/ti" ] && grep -q 'socket wayland' "$R/comp.log" && break; sleep 0.05; done
  sock=$(grep -oE 'socket wayland-[0-9]+' "$R/comp.log" | head -1 | awk '{print $2}')
  env XDG_RUNTIME_DIR="$R" WAYLAND_DISPLAY="$sock" "$APP" >"$R/app.out" 2>&1 & AP=$!
  # wait for the compositor to lay out its title bar
  for _ in $(seq 120); do grep -q 'deco: layout' "$R/comp.log" && break; sleep 0.05; done
  c=$(sed 's/\x1b\[[0-9;]*m//g' "$R/comp.log" | grep 'deco: layout' | tail -1 | sed 's/.* close=\([0-9,]*\) .*/\1/')
  if [ -z "$c" ]; then echo "run $i: no title bar, skipping"; kill $AP $CP 2>/dev/null; wait $AP $CP 2>/dev/null; continue; fi
  IFS=, read -r x y w h <<<"$c"
  $TI "$R/ti" "pointer $((x+w/2)) $((y+h/2))" click >/dev/null 2>&1
  # does the app exit?
  gone=0
  for _ in $(seq 50); do kill -0 $AP 2>/dev/null || { gone=1; break; }; sleep 0.1; done
  if [ "$gone" = 0 ]; then
    hangs=$((hangs+1))
    echo "=== run $i: HUNG after close (pid $AP) ==="
    eu-stack -p $AP 2>&1 | head -40
    echo "--- threads: tid state comm wchan syscall ---"
    for t in /proc/$AP/task/*; do
      echo "$(basename $t) $(awk '{print $3}' $t/stat 2>/dev/null) $(cat $t/comm 2>/dev/null) wchan=$(cat $t/wchan 2>/dev/null) syscall=$(cut -d' ' -f1 $t/syscall 2>/dev/null)"
    done
    echo "--- gdb backtrace attempt ---"
    timeout 25 gdb -p $AP -batch -ex "thread apply all bt 8" 2>&1 | grep -vE "^\[|^Reading|^warning:|Download|^$" | head -45
    echo "--- the app's own stderr tail ---"; tail -5 "$R/app.out"
    echo "--- the compositor's close lines ---"; sed 's/\x1b\[[0-9;]*m//g' "$R/comp.log" | grep -iE "close|quit" | tail -5
    kill -KILL $AP 2>/dev/null
  fi
  wait $AP 2>/dev/null
  kill $CP 2>/dev/null; wait $CP 2>/dev/null
done
echo "HANGS: $hangs of $N"
