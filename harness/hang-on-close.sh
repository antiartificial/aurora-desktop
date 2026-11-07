#!/bin/bash
# Reproduce the hang on close: compositor headless, one Haiku app, click the title bar's close
# button, and see whether the process exits. N=14 reproduced it in roughly a third of runs before
# the fix below. STACKS=1 runs the app under gdb and dumps every thread's stack when it hangs,
# which is the only way to get one here: yama ptrace_scope=1, so gdb must be the launcher, not an
# attacher. Only ever kills pids this script started.
#
# DIAGNOSIS (from the stacks STACKS=1 captures, now fixed):
#
#   Thread 1, the app looper:  wait_for_thread <- BLooper::Quit <- BWindow::Quit
#                              <- BApplication::_WindowQuitLoop <- _QuitAllWindows
#                              <- BApplication::QuitRequested <- task_looper
#   and the window's looper thread was already gone.
#
#   Closing a window drives two quit paths at once. The window's own thread dispatches
#   B_QUIT_REQUESTED, so BLooper::Quit() runs on it and leaves through exit_thread(). The app
#   thread, meanwhile, gets its own B_QUIT_REQUESTED and walks every window in gLooperList:
#   if it still sees that window, it calls BWindow::Quit() on it, which takes the cross-thread
#   path -- post _QUIT_, then wait_for_thread(). Who wins is a race, which is why it hung in
#   only some runs.
#
#   Losing that race was fatal because haiku-compat/stubs.cpp defined exit_thread() as a bare
#   pthread_exit(). Our thread layer (kits/src/thread.c) marks a thread done in its own registry
#   when its entry function RETURNS, and that is what wait_for_thread() waits on. A thread that
#   left through exit_thread() was never marked, so wait_for_thread() on it blocked forever.
#
#   THE FIX (applied): exit_thread() lives in kits/src/thread.c, records the status, and exits
#   through a pthread cleanup handler that marks the thread done -- so every way out of a thread,
#   including cancellation, wakes its waiters. Regressions: test_exit_thread_wakes_waiter in
#   kits/tests/test_kits.cpp, which hangs without it.
#
#   MEASURED: 4 hangs of 14 before the fix; 0 of 45 after (20 + 25 runs). For comparison, the
#   BLooper change in 671f967 alone measured 9 of 40 -- it is sound on its own terms but it is not
#   what fixes this, and no lock split is needed to explain the stacks above.
#
#   An earlier diagnosis in this header blamed a lost quit code in kits/src/app/Looper.cpp. That
#   was wrong: this app links Haiku's own Looper.cpp (kits/haiku-compat), not ours. The defect it
#   described is real, and is fixed in our BLooper too, but it was never this hang.
set -u
ROOT=/home/arti/projects/aurora
COMP=$ROOT/compositor/target/release/aurora-compositor
TI="python3 -I $ROOT/demos/kitchen-sink/ti.py"
APP=${APP:-$ROOT/kits/build/interface/iface_controls}
N=${N:-10}
STACKS=${STACKS:-0}
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT
hangs=0
for i in $(seq "$N"); do
  R=$W/r$i; mkdir -p "$R"
  env -u WAYLAND_DISPLAY XDG_RUNTIME_DIR="$R" "$COMP" --headless=1280x800 --test-input "$R/ti" \
      >"$R/comp.log" 2>&1 & CP=$!
  for _ in $(seq 200); do [ -S "$R/ti" ] && grep -q 'socket wayland' "$R/comp.log" && break; sleep 0.05; done
  sock=$(grep -oE 'socket wayland-[0-9]+' "$R/comp.log" | head -1 | awk '{print $2}')
  if [ "$STACKS" = 1 ]; then
    env XDG_RUNTIME_DIR="$R" WAYLAND_DISPLAY="$sock" gdb -q -batch -ex "set confirm off" \
        -ex "handle SIGPIPE nostop noprint pass" -ex run -ex "echo \n=== STACKS ===\n" \
        -ex "thread apply all bt 25" -ex kill --args "$APP" >"$R/app.out" 2>&1 & AP=$!
  else
    env XDG_RUNTIME_DIR="$R" WAYLAND_DISPLAY="$sock" "$APP" >"$R/app.out" 2>&1 & AP=$!
  fi
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
    if [ "$STACKS" = 1 ]; then
      kill -INT $AP                               # gdb stops its inferior and runs the rest
      for _ in $(seq 100); do grep -q '=== STACKS ===' "$R/app.out" && break; sleep 0.1; done
      sleep 2
      sed -n '/=== STACKS ===/,$p' "$R/app.out"
    else
      echo "--- threads: tid state comm wchan (no stacks: rerun with STACKS=1) ---"
      for t in /proc/$AP/task/*; do
        echo "$(basename $t) $(awk '{print $3}' $t/stat 2>/dev/null) $(cat $t/comm 2>/dev/null) wchan=$(cat $t/wchan 2>/dev/null)"
      done
    fi
    echo "--- the app's own stderr tail ---"; tail -5 "$R/app.out"
    echo "--- the compositor's close lines ---"; sed 's/\x1b\[[0-9;]*m//g' "$R/comp.log" | grep -iE "close|quit" | tail -5
    kill -KILL $AP 2>/dev/null
  fi
  wait $AP 2>/dev/null
  kill $CP 2>/dev/null; wait $CP 2>/dev/null
done
echo "HANGS: $hangs of $N"
