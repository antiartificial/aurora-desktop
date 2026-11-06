#!/usr/bin/env bash
# Does the bus's process-ancestry attestation survive ptrace injection?
#
# busd requires a peer to descend, by real parent, from the sandbox init of a
# live registered run (bus/server/ancestry.go). That closed attack 8g: a
# process that merely setns'd into a domain has a parent chain leading to the
# host. It was never tested against an attacker who takes over a process whose
# ancestry IS genuine.
#
# Yama (/proc/sys/kernel/yama/ptrace_scope = 1 here) normally allows ptrace
# only from an ancestor. The question is whether that protects a domain.
#
# Targets differ only in sandboxing. The attacker is always this script: same
# uid, not an ancestor of any target.
set -u
cd "$(dirname "$0")"
HERE=$PWD
UNIT=aurora-ptrace-probe
pass=0; fail=0
say() { printf '   %s\n' "$*"; }

cleanup() { systemctl --user stop $UNIT 2>/dev/null; systemctl --user reset-failed $UNIT 2>/dev/null; }
trap cleanup EXIT

echo "yama ptrace_scope: $(cat /proc/sys/kernel/yama/ptrace_scope 2>/dev/null || echo '(absent)')"
echo "attacker: pid $$, uid $(id -u), not an ancestor of any target"
echo

probe() { # name expected cmd...
  local name=$1 expect=$2; shift 2
  cleanup
  systemd-run --user --quiet --unit=$UNIT --service-type=exec "$@" || { echo "   $name: could not start"; return; }
  sleep 2
  local m p c
  m=$(systemctl --user show -p MainPID --value $UNIT)
  if ! [ "$m" -gt 1 ] 2>/dev/null; then echo "   $name: unit did not start"; fail=$((fail+1)); return; fi
  # walk to the leaf by ppid links only: no pattern matching, which would
  # match this script's own command line
  p=$m
  while c=$(ps --ppid "$p" -o pid= 2>/dev/null | head -1 | tr -d ' '); [ -n "$c" ]; do p=$c; done
  if [ "$p" = "$$" ]; then echo "   $name: refusing to probe my own shell"; fail=$((fail+1)); return; fi
  local uns myuns where got
  uns=$(readlink "/proc/$p/ns/user"); myuns=$(readlink "/proc/$$/ns/user")
  [ "$uns" = "$myuns" ] && where="shared userns" || where="own userns  "
  got=$(python3 -I "$HERE/attach.py" "$p")
  printf '   %-38s %s  %s' "$name" "$where" "$got"
  if [ "${got%% *}" = "$expect" ]; then echo "   (as expected)"; pass=$((pass+1)); else echo "   !! expected $expect"; fail=$((fail+1)); fi
}

BW="--ro-bind /usr /usr --symlink usr/lib /lib --symlink usr/lib64 /lib64 --proc /proc --dev /dev"
probe "1 plain host process"             DENIED   /usr/bin/sleep 300
probe "2 bwrap, no --unshare-user"       ATTACHED bwrap --unshare-pid $BW /usr/bin/sleep 300
probe "3 bwrap --unshare-user (a domain)" ATTACHED bwrap --unshare-pid --unshare-user $BW /usr/bin/sleep 300

echo
cat <<'TXT'
What this shows
   An ordinary process is protected from a same-uid non-ancestor by Yama.
   A process inside a user namespace is NOT: whoever owns that namespace
   holds CAP_SYS_PTRACE inside it, and bwrap always creates one (it needs
   one to work unprivileged), so --unshare-user is not what causes this.

   So sandboxing a process into a domain makes it MORE reachable by
   same-uid ptrace than leaving it alone, and busd's ancestry check cannot
   help: an injected process has genuine ancestry. The connection is real;
   the words on it are the attacker's.

   This does not weaken Aurora against a confined app, which is what the
   domains are for. It is one more face of "one uid owns everything"
   (README, "What this does not do"), and it is a worse one than 8g because
   it needs no setns and leaves the attested identity intact.
TXT
echo
echo "passed $pass, failed $fail"
[ "$fail" -eq 0 ]
