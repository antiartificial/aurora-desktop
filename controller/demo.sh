#!/usr/bin/env bash
# End-to-end controller demo: busd + controllerd + CLI, a verb run inside a
# real bwrap domain, the provenance event and the xattr. Leaves nothing behind.
set -uo pipefail
cd "$(dirname "$0")"
CTL=$(pwd); ROOT=$(cd .. && pwd)
W=$(mktemp -d /tmp/ctl-demo.XXXXXX)
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  wait 2>/dev/null
  for d in "$W"/domains/app-*; do [ -d "$d" ] && "$W/bin/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done
  rm -rf "$W"
}
trap cleanup EXIT
step() { printf '\n== %s\n' "$*"; }

export AURORA_BUS_SOCK=$W/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$W/run
mkdir -p "$W/bin" "$W/pics" "$W/run"

step "build"
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || { echo "cannot build bus"; exit 1; }
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || echo "cannot build domain CLI: verbs will run UNISOLATED"
go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller || exit 1
CGO_ENABLED=0 go build -o "$W/bin/busprobe" ./cmd/busprobe || exit 1
echo "built into $W/bin"

# Pick the app to invoke: real Lumen if its binary builds, else the fixture.
APPS="$ROOT/apps,$CTL/testdata/apps,$CTL/testdata/probeapps"
EXEC=(--exec "org.aurora.busprobe=$W/bin/busprobe")
if (cd "$ROOT/apps/lumen" && go build -o "$W/bin/lumen" ./cmd/lumen 2>/dev/null); then
  APP=org.aurora.lumen; VERB=resize@v1; ARGS=(--arg width=300); OUT=$W/pics/small.png
  EXEC+=(--exec "org.aurora.lumen=$W/bin/lumen")
  (cd "$ROOT/apps/lumen" && go build -o "$W/bin/genimage" ./cmd/genimage) && "$W/bin/genimage" "$W/pics/in.png"
  echo "using real Lumen (apps/lumen/cmd/lumen)"
else
  APP=org.aurora.demo; VERB=copy@v1; ARGS=(--arg tag=demo); OUT=$W/pics/copy.png
  printf '\x89PNG\r\n\x1a\n\0\0\0\rIHDR' > "$W/pics/in.png"
  echo "NOTE: apps/lumen has no buildable implementation yet; degrading to the fixture app org.aurora.demo"
fi

step "start busd and controllerd"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
"$W/bin/controllerd" --apps "$APPS" --domain-bin "$W/bin/domain" "${EXEC[@]}" >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
cat "$W/controllerd.log"

step "bus sub provenance.created (captured in background)"
"$W/bin/bus" sub provenance.created >"$W/events.log" 2>&1 & PIDS+=($!)
sleep 0.5

C="$W/bin/controller"
step "controller apps";            $C apps
step "controller verbs --accepts image/png"; $C verbs --accepts image/png
step "controller describe $APP $VERB"; $C describe "$APP" "$VERB"

step "files in $W/pics before"; ls "$W/pics"
step "controller invoke $APP $VERB"
$C invoke "$APP" "$VERB" --input "$W/pics/in.png" --output "$OUT" "${ARGS[@]}" || echo "(invoke failed; see above)"
step "files in $W/pics after"; ls -l "$W/pics"

step "provenance.created event seen by bus sub"
sleep 0.5; cat "$W/events.log"

step "xattr read back: controller provenance"
$C provenance "$OUT" || echo "(no xattr: filesystem may not support user xattrs)"
step "raw xattr (getfattr, if installed)"
command -v getfattr >/dev/null && getfattr -n user.aurora.provenance "$OUT" || echo "(getfattr not installed)"

step "grants after the run (must be empty: revoked)"
for d in "$W"/domains/app-"$(echo "$APP" | tr . - | cut -c1-11 | sed 's/-*$//')"-*; do "$W/bin/domain" show "$(basename "$d")" 2>&1; done

step "isolation proof: the fixture app (org.aurora.demo) runs in a domain and reports what it sees"
printf '\x89PNG\r\n\x1a\n\0\0\0\rIHDR' > "$W/pics/probe.png"
$C invoke org.aurora.demo copy@v1 --input "$W/pics/probe.png" --output "$W/pics/probe-out.png" | grep -E '"isolated"|"log"'
echo "(0 entries in /home means the domain cannot see the host's home; only the granted input and output files exist)"
step "bus identity: an app INSIDE its domain connects to the bus (AURORA_BUS_SOCKET=/run/aurora/bus.sock)"
"$W/bin/bus" sub app.probe >"$W/probe-events.log" 2>&1 & PIDS+=($!)
sleep 0.5
grep -E 'registered .* with the bus|WARNING.*bus' "$W/controllerd.log" || echo "(no registration logged yet; first invoke registers)"
cp "$W/pics/probe.png" "$W/pics/pin.png"
$C invoke org.aurora.busprobe probe@v1 --input "$W/pics/pin.png" --output "$W/pics/pout.png" | grep -E '"isolated"|"domain|"ok"'
sleep 0.5
echo "-- what busd saw (from= is decided by the socket, not by the app):"
grep -o '"from":"[^"]*"' "$W/probe-events.log" | sort -u
grep -q '"from":"domain:app-org-aurora-' "$W/probe-events.log" && ! grep -q '"from":"uid' "$W/probe-events.log" && echo "RESULT: app appears as its domain identity, not uid:comm" || echo "RESULT: FAILED to appear as a domain identity"
echo "-- bus inspect: registered domain sockets"
"$W/bin/bus" inspect | sed -n '/^domain sockets/,/^$/p' | head -8
echo "-- provenance record now names the domain identity:"
$C provenance "$W/pics/pout.png" | grep -E '"domain|"app"'

step "grant sources: restart controllerd with --no-gesture-grants and HOME=$W/home (fixture declares ~/Pictures write)"
kill "${PIDS[1]}" 2>/dev/null; wait "${PIDS[1]}" 2>/dev/null
mkdir -p "$W/home/Pictures"; cp "$W/pics/probe.png" "$W/home/Pictures/in.png"
HOME=$W/home "$W/bin/controllerd" --apps "$APPS" --domain-bin "$W/bin/domain" --no-gesture-grants "${EXEC[@]}" >"$W/controllerd2.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd2.log" && break; sleep 0.1; done
grep -- 'gesture' "$W/controllerd2.log"
echo "-- input outside ~/Pictures (a gesture): must be refused"
$C invoke org.aurora.demo copy@v1 --input "$W/pics/probe.png" --output "$W/pics/refused.png" 2>&1 | grep -E '"error"|"detail"|"code"|ok'
ls "$W/pics/refused.png" 2>&1 | sed 's/^/   /'
echo "-- input and output inside ~/Pictures (ambient): must succeed"
$C invoke org.aurora.demo copy@v1 --input "$W/home/Pictures/in.png" --output "$W/home/Pictures/out.png" | grep -A12 '"grants"'

step "approval: destructive verb (org.aurora.demo delete@v1) on a gesture grant must block until decided"
echo "(decider here is SCRIPTED: 'controller decide' over the session socket. A script cannot drive /dev/tty;"
echo " the interactive route is 'aurora approvals' / 'aurora open', see shell/README.md)"
kill "${PIDS[-1]}" 2>/dev/null; wait "${PIDS[-1]}" 2>/dev/null
HOME=$W/home "$W/bin/controllerd" --apps "$APPS" --domain-bin "$W/bin/domain" --approval-timeout 4s "${EXEC[@]}" >"$W/controllerd3.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd3.log" && break; sleep 0.1; done
grep -- 'approval' "$W/controllerd3.log"
"$W/bin/bus" sub approval.requested >"$W/approvals.log" 2>&1 & PIDS+=($!)
sleep 0.5
reqid() { for _ in $(seq 50); do id=$(grep -o '"request_id":"[a-f0-9]*"' "$W/approvals.log" | tail -n +"$1" | head -1 | cut -d'"' -f4); [ -n "$id" ] && { echo "$id"; return; }; sleep 0.1; done; }
DEL=(invoke org.aurora.demo delete@v1 --input "$W/pics/probe.png")
echo "-- 1. invoke in background, nobody has decided yet"
$C "${DEL[@]}" --output "$W/pics/deny.png" >"$W/deny.out" 2>&1 & DP=$!
ID1=$(reqid 1); sleep 1
echo "request $ID1 published; invoke still running: $(kill -0 $DP 2>/dev/null && echo yes || echo NO); output file: $(ls "$W/pics/deny.png" 2>&1 | sed 's/.*No such.*/absent/')"
grep -o '"verb":"[^"]*","effect":"[^"]*","grants":\[[^]]*\],"args":{[^}]*}' "$W/approvals.log" | head -1
echo "-- 2. DENY"
$C decide "$ID1" deny | grep -E '"decision"|"by"'; wait $DP
grep -E '"code"|"error"' "$W/deny.out" | head -2
echo "output file after denial: $(ls "$W/pics/deny.png" 2>&1 | sed 's/.*No such.*/absent/')"
echo "-- 3. ALLOW"
$C "${DEL[@]}" --output "$W/pics/allow.png" >"$W/allow.out" 2>&1 & DP=$!
ID2=$(reqid 2); sleep 0.5
echo "output file while pending: $(ls "$W/pics/allow.png" 2>&1 | sed 's/.*No such.*/absent/')"
$C decide "$ID2" allow | grep -E '"decision"|"by"'; wait $DP
grep -A8 '"approval"' "$W/allow.out" | head -9
echo "output file after allow: $(ls "$W/pics/allow.png" 2>&1)"
echo "-- provenance xattr on the output:"
$C provenance "$W/pics/allow.png" | grep -A8 '"approval"'
echo "-- 4. timeout (4s) with nobody answering"
$C "${DEL[@]}" --output "$W/pics/late.png" 2>&1 | grep -E '"code"|"error"' | head -2
echo "output file after timeout: $(ls "$W/pics/late.png" 2>&1 | sed 's/.*No such.*/absent/')"
echo "-- 5. destructive but ALL grants ambient (~/Pictures declared): no approval needed"
cp "$W/pics/probe.png" "$W/home/Pictures/in2.png"
$C invoke org.aurora.demo delete@v1 --input "$W/home/Pictures/in2.png" --output "$W/home/Pictures/gone.png" | grep -A5 '"approval"' | head -6

echo
echo "done; cleaning up"
