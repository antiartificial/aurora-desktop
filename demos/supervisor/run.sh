#!/usr/bin/env bash
# Supervisor demo: the real busd, controllerd and indexd run under supervisord;
# we show status, KILL services with kill -9 and watch them come back with the
# restart recorded, show a service that hangs being noticed (up, but not
# answering on the bus) and killed, show a crash-looping service being given up
# on instead of spun forever, refuse then perform a busd restart, read the
# supervisor's events off the bus log, and check the supervisor's own shutdown
# leaves nothing. Counts passes and failures, exits non-zero if any failed.
# Needs: go, python3. No compositor, no containers, installs nothing.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
W=$(mktemp -d /tmp/sup-demo.XXXXXX)
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/sd.XXXXXX"); chmod 700 "$RUN"   # AF_UNIX paths stay short
PASS=0 FAIL=0 STEPS=0 SUP=0 FOREIGN=0
cleanup() {
  [ "$FOREIGN" != 0 ] && kill "$FOREIGN" 2>/dev/null
  if [ "$SUP" != 0 ] && kill -0 "$SUP" 2>/dev/null; then kill -TERM "$SUP" 2>/dev/null; wait "$SUP" 2>/dev/null; fi
  rm -rf "$W" "$RUN"
}
trap cleanup EXIT
step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n' "$STEPS" "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*"; }
check(){ local d=$1; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d  [$*]"; fi; }
eq()   { [ "$1" = "$2" ] && ok "$3 ($1)" || bad "$3: got '$1', want '$2'"; }
has()  { case $1 in *"$2"*) ok "$3";; *) bad "$3 (no '$2' in: ${1:0:300})";; esac; }
waitfor() { local d=$1 t=$2; shift 2; local n=$((t*10)); while [ $n -gt 0 ]; do "$@" >/dev/null 2>&1 && return 0; sleep 0.1; n=$((n-1)); done; return 1; }
show() { sed 's/^/   | /'; }

export AURORA_BUS_SOCK=$RUN/bus.sock AURORA_SUPERVISOR_SOCK=$RUN/supervisor.sock XDG_RUNTIME_DIR=$RUN AURORA_DOMAINS_DIR=$W/domains
export AURORA_POWERBOX_KEY=$W/powerbox.key
BIN=$W/bin; mkdir -p "$BIN" "$W/index"
PATH=$BIN:$PATH
# st NAME FIELD: one field of one service from the supervisor's JSON status
st() { auroractl status --json | python3 -I -c 'import json,sys
for s in json.load(sys.stdin)["services"]:
    if s["name"]==sys.argv[1]: print(s[sys.argv[2]]); break' "$1" "$2"; }
healthy() { [ "$(st "$1" state)/$(st "$1" health)" = running/healthy ]; }
state_is() { [ "$(st "$1" state)" = "$2" ]; }
health_is() { [ "$(st "$1" health)" = "$2" ]; }
# new_pid NAME OLD: running under a pid that is not OLD (waitfor re-runs the function each poll; "$(...)" in its arguments would be expanded only once)
new_pid() { local p; p=$(st "$1" pid); [ "$p" != 0 ] && [ "$p" != "$2" ]; }
stack_healthy() { healthy busd && healthy controllerd && healthy indexd; }
run_pids() { RUN=$RUN awk -v me=$$ '$1!=me && index($0,ENVIRON["RUN"]) {print $1}' < <(ps -eo pid=,args=); }
buslog() { bus log 2>&1; }

step "build (go build -o <path> ./cmd/x)"
(cd "$ROOT/bus" && go build -o "$BIN/" ./cmd/busd ./cmd/bus) || bad "bus build"
(cd "$ROOT/adapter/linux" && go build -o "$BIN/domain" ./cmd/domain) || bad "domain build"
(cd "$ROOT/controller" && go build -o "$BIN/" ./cmd/controllerd ./cmd/controller) || bad "controller build"
(cd "$ROOT/index" && go build -o "$BIN/indexd" ./cmd/indexd) || bad "indexd build"
(cd "$ROOT/supervisor" && go build -o "$BIN/" ./cmd/supervisord ./cmd/auroractl) || bad "supervisor build"
for b in busd bus domain controllerd indexd supervisord auroractl; do [ -x "$BIN/$b" ] || { bad "$BIN/$b missing"; echo "cannot go on"; exit 1; }; done
ok "built into $BIN"

step "the service list is data: five entries, two of them there to misbehave"
cat > "$W/services.json" <<EOF
{
  "defaults": {"backoff_initial":"200ms","backoff_max":"1s","stable_after":"2s","crash_limit":5,"crash_window":"60s","stop_grace":"2s"},
  "services": [
    {"name":"busd","command":["$BIN/busd","--socket","$AURORA_BUS_SOCK","--data-dir","$RUN/buslog"],
     "health":{"type":"bus"},"log":"$RUN/busd.log"},
    {"name":"controllerd","command":["$BIN/controllerd","--apps","$ROOT/controller/testdata/apps","--domain-bin","$BIN/domain","--no-compositor-approval"],
     "needs":["busd"],"health":{"type":"service","name":"registry"},"log":"$RUN/controllerd.log"},
    {"name":"indexd","command":["$BIN/indexd","--socket","$AURORA_BUS_SOCK","--data-dir","$W/index"],
     "needs":["busd"],"health":{"type":"service","name":"index"},"log":"$RUN/indexd.log","unhealthy_after":"4s"},
    {"name":"ghost","command":["sleep","600"],"health":{"type":"service","name":"ghost"},"log":"$RUN/ghost.log","unhealthy_after":"0s"},
    {"name":"flaky","command":["sh","-c","echo flaky: starting; exit 7"],"no_autostart":true,"log":"$RUN/flaky.log"}
  ]
}
EOF
grep -E '"name"' "$W/services.json" | sed 's/^ *{//; s/,"command.*//' | show
"$BIN/supervisord" --config "$W/services.json" --control "$AURORA_SUPERVISOR_SOCK" --bus-socket "$AURORA_BUS_SOCK" \
   --children-file "$RUN/children" >"$RUN/supervisord.log" 2>&1 & SUP=$!
waitfor "control socket" 10 test -S "$AURORA_SUPERVISOR_SOCK" && ok "supervisor is listening on its control socket" || bad "no control socket"
# a busd that cannot bind its socket only WARNS and keeps running: never trust a result before the socket exists
waitfor "bus socket" 10 test -S "$AURORA_BUS_SOCK" && ok "busd's socket exists ($AURORA_BUS_SOCK)" || bad "busd socket never appeared"
waitfor "stack healthy" 20 stack_healthy && ok "busd, controllerd, indexd all healthy" || bad "stack did not become healthy"

step "auroractl status: pid, uptime, restarts, last exit, and health judged on the bus"
auroractl status | show
BP=$(st busd pid)
eq "$(ps -o args= -p "$BP" | awk '{print $1}')" "$BIN/busd" "status pid $BP is really busd"
INS=$(bus inspect 2>&1)
has "$INS" "registry  (client" "bus inspect lists the 'registry' service"
has "$INS" "index  (client" "bus inspect lists the 'index' service"
has "$INS" "pid=$(st controllerd pid)" "the client registered as 'registry' is the very pid the supervisor started"
eq "$(st busd restarts)" 0 "nothing has been restarted yet"

step "healthy is more than a pid: a live process that is not answering on the bus is NOT healthy"
GP=$(st ghost pid); kill -0 "$GP" 2>/dev/null && ok "ghost (pid $GP) is a live process" || bad "ghost not running"
waitfor "ghost unhealthy" 10 health_is ghost unhealthy && ok "supervisor says ghost is unhealthy" || bad "ghost health is '$(st ghost health)'"
has "$(auroractl status)" 'not registered on the bus as "ghost"' "and says why"
IP=$(st indexd pid); kill -STOP "$IP"
echo "   (SIGSTOP indexd pid $IP: still registered, no longer answering; the answer is probed every 10s once healthy)"
waitfor "indexd unhealthy" 20 health_is indexd unhealthy && ok "frozen indexd is reported unhealthy" || bad "frozen indexd health is '$(st indexd health)'"
has "$(auroractl status)" "not answering requests" "reason names the silent request loop, not a missing pid"
waitfor "indexd replaced" 20 new_pid indexd "$IP" && ok "after unhealthy_after (4s) it was killed and replaced (pid $IP -> $(st indexd pid))" || bad "frozen indexd was not replaced"
waitfor "indexd healthy again" 10 healthy indexd && ok "the replacement is healthy" || bad "replacement not healthy"
has "$(st indexd last_exit)" "unhealthy for 4s" "last exit says why: $(st indexd last_exit)"

step "kill -9 controllerd: it comes back, and the restart is recorded"
CP=$(st controllerd pid); kill -9 "$CP"; echo "   kill -9 $CP"
waitfor "controllerd replaced" 15 new_pid controllerd "$CP" && ok "new pid $(st controllerd pid)" || bad "controllerd did not come back"
waitfor "controllerd healthy" 15 healthy controllerd && ok "healthy again (registered as 'registry' and answering)" || bad "controllerd not healthy after restart"
auroractl status | show
eq "$(st controllerd restarts)" 1 "restart count is 1"
eq "$(st controllerd last_exit)" "killed by SIGKILL" "last exit reason"
APPS=$(controller apps 2>&1); has "$APPS" "org.aurora" "the restarted registry serves requests ($(echo "$APPS" | head -1 | cut -c1-60))"
eq "$(st busd restarts)" 0 "busd, which controllerd needs, was not touched"

step "a crash-looping service is given up on, not spun forever"
auroractl start --no-wait flaky | show
waitfor "flaky gave up" 20 state_is flaky gave_up && ok "flaky is gave_up" || bad "flaky state is '$(st flaky state)'"
auroractl status | show
eq "$(st flaky restarts)" 4 "5 runs = 4 restarts (crash_limit 5), then it stopped"
has "$(st flaky detail)" "5 unstable exits" "reason: $(st flaky detail)"
eq "$(st flaky last_exit)" "exit status 7" "last exit"
R1=$(st flaky restarts); sleep 3
eq "$(st flaky restarts)" "$R1" "3 seconds later: still $R1 restarts, nothing is spinning"
eq "$(grep -c 'flaky: starting' "$RUN/flaky.log")" 5 "its log shows exactly 5 runs"
eq "$(st busd restarts)/$(st controllerd restarts)" "0/1" "the rest of the stack was not disturbed by the loop"

step "restarting busd: refused while foreign clients are connected, done (with its dependents) when not"
bus sub 'demo.**' >/dev/null 2>&1 & FOREIGN=$!
sleep 0.5
BP0=$(st busd pid)
OUT=$(auroractl restart busd 2>&1); RC=$?
echo "$OUT" | show
[ $RC != 0 ] && ok "refused (exit $RC)" || bad "restart of busd was not refused"
has "$OUT" "pid $FOREIGN" "the refusal names the client that would be orphaned (pid $FOREIGN, a 'bus sub' this script started)"
eq "$(st busd pid)" "$BP0" "busd untouched"
kill "$FOREIGN"; wait "$FOREIGN" 2>/dev/null; FOREIGN=0; sleep 0.3
CP0=$(st controllerd pid); IP0=$(st indexd pid)
OUT=$(auroractl restart busd 2>&1); RC=$?; echo "$OUT" | show
[ $RC = 0 ] && ok "restart accepted once nothing foreign is connected" || bad "restart failed (exit $RC)"
check "busd has a new pid" test "$(st busd pid)" != "$BP0"
waitfor "stack healthy again" 20 stack_healthy && ok "controllerd and indexd were restarted after it and are healthy" || bad "dependents did not recover"
check "dependents have new pids (their bus connections died with the old busd)" test "$(st controllerd pid)" != "$CP0" -a "$(st indexd pid)" != "$IP0"
auroractl status | show
INS=$(bus inspect 2>&1); has "$INS" "registry  (client" "'registry' is registered on the new bus"; has "$INS" "index  (client" "'index' is registered on the new bus"
has "$(buslog)" '"service":"flaky"' "the bus log (kept in busd's data dir) still holds events from before the restart"
echo "   and the crash path: kill -9 busd with nothing orchestrating it"
BP1=$(st busd pid); kill -9 "$BP1"
waitfor "busd replaced" 15 new_pid busd "$BP1" && ok "busd restarted by the supervisor (pid $BP1 -> $(st busd pid))" || bad "busd not restarted"
waitfor "stack healthy" 20 stack_healthy && ok "and everything that needs it came back healthy" || bad "stack not healthy after busd crash"
auroractl status | show
eq "$(st flaky state)" gave_up "flaky is still given up (a busd restart does not reset a crash loop)"

step "the supervisor's events are on the bus"
LOG=$(buslog)
for t in started restarted exited gave_up health; do
  n=$(grep -c "supervisor.service.$t " <<<"$LOG"); [ "$n" -gt 0 ] && ok "supervisor.service.$t: $n event(s)" || bad "no supervisor.service.$t on the bus"
done
has "$LOG" "uid$(id -u):supervisord -> * supervisor.service.gave_up" "sender is stamped by busd from SO_PEERCRED (uid:supervisord), not claimed in the payload"
grep 'supervisor.service' <<<"$LOG" | grep -E '"service":"(controllerd|flaky)"' | grep -E 'gave_up|SIGKILL' | cut -c1-230 | head -3 | show
has "$LOG" "killed by SIGKILL" "the kill -9 is in the log as the exit reason"
if grep -q '"supervisor\.' "$ROOT/bus/server/server.go"; then ok "busd treats supervisor. as a system namespace: domains do not receive these"
else echo "   NOTE (known gap, not asserted): busd's systemPrefixes does not yet include \"supervisor.\", so a confined domain subscribed to ** would receive these events. They carry service names, restart counts and exit reasons only."; fi

step "the supervisor's own shutdown leaves nothing"
CH=$(awk '{print $2}' "$RUN/children" | tr '\n' ' ')
echo "   supervised pids: $CH"
kill -TERM "$SUP"; wait "$SUP" 2>/dev/null; RC=$?; SUP=0
eq "$RC" 0 "supervisord exited 0"
tail -8 "$RUN/supervisord.log" | show
LAST=$(grep -E ': exited: ' "$RUN/supervisord.log" | tail -1)
has "$LAST" "busd: exited" "busd was stopped last, after what depends on it"
for p in $CH; do [ -e "/proc/$p" ] && bad "pid $p survived" || ok "pid $p is gone"; done
LEFT=$(run_pids); [ -z "$LEFT" ] && ok "no process mentions $RUN" || bad "stray processes: $LEFT"
check "control socket and bus socket removed" test ! -e "$AURORA_SUPERVISOR_SOCK" -a ! -e "$AURORA_BUS_SOCK"
[ -s "$RUN/children" ] && bad "children file not empty: $(cat "$RUN/children")" || ok "children file is empty"

printf '\n'
if [ $FAIL = 0 ]; then echo "SUPERVISOR DEMO PASSED ($PASS checks, $STEPS steps)"; else echo "SUPERVISOR DEMO FAILED: $FAIL failed, $PASS passed ($STEPS steps)"; exit 1; fi
