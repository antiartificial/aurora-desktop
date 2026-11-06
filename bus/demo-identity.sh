#!/usr/bin/env bash
# Aurora bus identity demo: two REAL domains (bwrap, via adapter/linux), each
# with its own bus socket AND cgroup; distinct sender identities; a minted
# handle, a transfer, a forgery; then impersonation by a host process, refused
# by kernel-attested identity, permitted with --attest-domains=off; then the
# join-the-cgroup attack: accepted when only the cgroup is attested (the old
# hole), REFUSED now that namespaces are attested too, accepted with attestation
# off; then the same-uid attacks against PER-DOMAIN SUBORDINATE UIDS: what
# setns-into-the-namespaces does now, and whether a same-user attacker can
# become the domain's uid (attackers: demo-attacks.py). Leaves nothing behind.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aurora-bus-id.XXXXXX")"
export AURORA_DOMAINS_DIR="$TMP/domains"
export XDG_RUNTIME_DIR="$TMP/run"; mkdir -p "$XDG_RUNTIME_DIR"
export AURORA_BUS_SOCK="$TMP/bus.sock"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && wait "$p" 2>/dev/null || true; done
  for d in "${A:-}" "${B:-}"; do [ -n "$d" ] && "$DOMAIN" destroy "$d" >/dev/null 2>&1 || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM
step() { printf '\n== %s\n' "$*"; }
waitfor() { for _ in $(seq 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.05; done; echo "timed out: $2" >&2; exit 1; }
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }

step "build busd, bus, domain (static, so the binary runs inside a domain)"
export CGO_ENABLED=0
(cd "$HERE" && go build -o "$TMP/busd" ./cmd/busd && go build -o "$TMP/bus" ./cmd/bus)
(cd "$HERE/../adapter/linux" && go build -o "$TMP/domain" ./cmd/domain)
BUS="$TMP/bus"; DOMAIN="$TMP/domain"

step "two domains; each is given the bus binary (read-only) and nothing else"
A="alpha-$$"; B="beta-$$"   # unique: a domain's cgroup is named after it
for d in $A $B; do "$DOMAIN" create $d >/dev/null; "$DOMAIN" grant $d "$BUS" --ro >/dev/null; done
IDA=$("$DOMAIN" exec-id $A); IDB=$("$DOMAIN" exec-id $B)
SA=$("$DOMAIN" sockpath $A); SB=$("$DOMAIN" sockpath $B)
CGA=$("$DOMAIN" cgroup $A); CGB=$("$DOMAIN" cgroup $B)
UIDA=$("$DOMAIN" uid $A); UIDB=$("$DOMAIN" uid $B)
SUBSTART=$(awk -F: -v u="$(id -un)" '$1==u{print $2;exit}' /etc/subuid); SUBCOUNT=$(awk -F: -v u="$(id -un)" '$1==u{print $3;exit}' /etc/subuid)
INNERA=$((UIDA - SUBSTART + 1))   # alpha's uid inside its user namespace
ATK="$HERE/demo-attacks.py"
printf "alpha: %s\n       socket %s\n       cgroup %s\n       uid    %s (this user's real uid: %s)\n" "$IDA" "$SA" "$CGA" "$UIDA" "$(id -u)"
printf "beta:  %s\n       socket %s\n       cgroup %s\n       uid    %s\n" "$IDB" "$SB" "$CGB" "$UIDB"

step "busd --attest-domains=require: per domain a socket, a cgroup, a uid and namespace attestation (each domain run registers its namespaces first)"
"$TMP/busd" --data-dir "$TMP/log" --attest-domains=require --domain-ns "$IDA" --domain-ns "$IDB" \
  --domain "$IDA=$SA" --domain-cgroup "$IDA=$CGA" --domain-uid "$IDA=$UIDA" \
  --domain "$IDB=$SB" --domain-cgroup "$IDB=$CGB" --domain-uid "$IDB=$UIDB" 2>"$TMP/busd.err" &
PIDS+=($!); BUSD=$!
waitfor "$TMP/busd.err" "listening on"
cat "$TMP/busd.err"

# A host-side service that reports who asked and answers any request.
"$BUS" serve vault 'printf "{\"served\":true,\"asked_by\":\"%s\"}" "$BUS_FROM"' 2>"$TMP/serve.err" &
PIDS+=($!)
waitfor "$TMP/serve.err" "serving vault"
inA() { local c=$1; shift; "$DOMAIN" run $A -- "$BUS" "$c" --socket /run/aurora/bus.sock "$@"; }
inB() { local c=$1; shift; "$DOMAIN" run $B -- "$BUS" "$c" --socket /run/aurora/bus.sock "$@"; }

step "1. distinct identities: the vault sees which domain called (from = the socket it arrived on)"
echo "alpha -> $(inA send vault who@v1)"
echo "beta  -> $(inB send vault who@v1)"
echo "host session socket -> $("$BUS" send vault who@v1)   (uid:comm, not a domain)"

step "2. alpha mints a handle for resource photo:42"
M=$(inA send bus bus.mint@v1 '{"resource":"photo:42"}')
echo "$M" | sed 's/"handle":"cap_[0-9a-f]*"/"handle":"cap_<64 hex, redacted>"/'
TOK=$(echo "$M" | field handle)

step "3. forgery: beta invents a handle; beta presents alpha's real token without holding it"
inB send vault use@v1 --cap cap_0000000000000000000000000000000000000000000000000000000000000000 || echo "(rejected, exit $?)"
inB send vault use@v1 --cap "$TOK" || echo "(rejected, exit $?)"

step "4. the holder presents it: accepted"
inA send vault use@v1 --cap "$TOK"

step "5. transfer alpha -> beta (beta must be connected; the daemon pushes the token to it)"
inB sub bus.cap.granted >"$TMP/beta.grants" 2>"$TMP/beta.err" &
PIDS+=($!)
waitfor "$TMP/beta.err" "subscribed"
inA send bus bus.transfer@v1 "{\"handle\":\"$TOK\",\"to\":\"$IDB\"}"
waitfor "$TMP/beta.grants" "bus.cap.granted"
BTOK=$(sed -n 's/.*"caps":\["\([^"]*\)"\].*/\1/p' "$TMP/beta.grants" | head -1)
[ "$BTOK" = "$TOK" ] && echo "beta received the handle from the daemon"
echo "beta presents:  $(inB send vault use@v1 --cap "$BTOK")"
echo "alpha presents (no longer holds it):"
inA send vault use@v1 --cap "$TOK" || echo "(rejected, exit $?)"

step "6. bus inspect: outstanding handles (id, resource, holder, expiry; never the token)"
"$BUS" inspect | sed -n '/^domain sockets/,$p'

step "7. the log has the handle's identity (cap_ids hash) and not the token"
"$BUS" log --since 5m | grep -c "caps=cap:" | sed 's/^/messages carrying a cap id: /'
if grep -rqF "$TOK" "$TMP/log"; then echo "!! TOKEN IN LOG"; exit 1; else echo "token absent from log bytes"; fi

spoof() { python3 - "$1" "$IDB" <<'PY'
import socket, sys
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
s.settimeout(1)
s.sendall(b'{"kind":"event","topic":"demo.spoof","from":"' + sys.argv[2].encode() + b'"}\n')
try:
    print("daemon replied: " + s.recv(4096).decode().strip())
except Exception as e:
    print("daemon replied nothing:", e)
PY
}
restart() {  # restart busd with extra flags (--domain-ns "$IDA" attests namespaces); NOUID=1 / NOCG=1 leave that signal unregistered
  kill "$BUSD" 2>/dev/null; wait "$BUSD" 2>/dev/null || true
  rm -rf "$TMP/log"; : >"$TMP/busd.err"
  local u=(--domain-uid "$IDA=$UIDA") c=(--domain-cgroup "$IDA=$CGA")
  [ -n "${NOUID:-}" ] && u=(); [ -n "${NOCG:-}" ] && c=()
  "$TMP/busd" --data-dir "$TMP/log" "$@" --domain "$IDA=$SA" "${c[@]}" "${u[@]}" 2>"$TMP/busd.err" &
  PIDS+=($!); BUSD=$!
  waitfor "$TMP/busd.err" "listening on"
}
logspoof() { sleep 0.2; "$BUS" log --topic demo.spoof 2>&1 | sed 's/^/log: /'; }

step "8a. impersonation attempt, attestation ON (require): a HOST process opens alpha's socket and claims to be beta"
spoof "$SA"
grep -E "REFUSED|attested" "$TMP/busd.err" | sed 's/^[0-9:]* /busd log: /'
echo "log entries with topic demo.spoof: $("$BUS" log --topic demo.spoof 2>/dev/null | grep -c demo.spoof || true)   (refused; nothing recorded)"
echo "and a real process in alpha's domain still gets in: $(inA send vault who@v1 2>&1 | head -c 200)" || true

step "8b. the SAME attempt, --attest-domains=off (the old behaviour): the socket is the identity"
restart --attest-domains=off --domain-ns "$IDA"
spoof "$SA"
logspoof
echo "(claimed from=$IDB; recorded as alpha, because with attestation off the socket is the identity)"

# The attack: a same-uid host process writes its own pid into alpha's cgroup.procs
# (the directory is owned by this uid), then connects and claims to be beta.
joincg() { python3 - "$SA" "$IDB" "/sys/fs/cgroup$CGA" <<'PY'
import socket, sys
try:
    open(sys.argv[3] + "/cgroup.procs", "w").write("0")
except OSError as e:
    print("could not join the cgroup:", e); sys.exit(0)
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
s.settimeout(1)
s.sendall(b'{"kind":"event","topic":"demo.spoof","from":"' + sys.argv[2].encode() + b'"}\n')
try:
    print("daemon replied: " + s.recv(4096).decode().strip())
except Exception as e:
    print("daemon replied nothing:", e)
PY
}

live_run() {  # a long-lived run of alpha, registered with the daemon; sets TGT to its payload pid
  local n; n=$(grep -c "registered run" "$TMP/busd.err" || true)
  "$DOMAIN" run $A -- /usr/bin/sleep 20 >/dev/null 2>&1 &
  PIDS+=($!)
  for _ in $(seq 100); do [ "$(grep -c "registered run" "$TMP/busd.err" || true)" -gt "$n" ] && break; sleep 0.05; done
  for _ in $(seq 100); do TGT=$(pgrep -n -x sleep || true); [ -n "$TGT" ] && break; sleep 0.05; done
}

step "8c. THE FIX: --attest-domains=require, cgroup AND namespaces attested. The attacker joins alpha's cgroup, then connects"
restart --attest-domains=require --domain-ns "$IDA"
live_run   # alpha is running; its namespaces are registered
echo "a real alpha process is still admitted: $(inA send bus bus.ping@v1 2>&1 | head -c 60)"
joincg
kill "$TGT" 2>/dev/null || true
grep -E "REFUSED|attested" "$TMP/busd.err" | sed 's/^[0-9:]* /busd log: /'
echo "log entries with topic demo.spoof: $("$BUS" log --topic demo.spoof 2>/dev/null | grep -c demo.spoof || true)   (refused; nothing recorded)"

step "8d. BEFORE the fix, for contrast: the same attack when the domain is registered with a cgroup only (no --domain-ns, no --domain-uid)"
NOUID=1 restart --attest-domains=require
joincg
logspoof
echo "(cgroup membership alone: the join-the-cgroup attacker WAS alpha)"

step "8e. the same attack with --attest-domains=off: permissive, as documented"
restart --attest-domains=off --domain-ns "$IDA"
joincg
logspoof

step "8f. setns attacker, same real uid: joins alpha's cgroup AND enters the domain's pid/mnt/ipc/uts namespaces (it keeps its own uid)"
restart --attest-domains=require --domain-ns "$IDA"
live_run
python3 "$ATK" setns /run/aurora/bus.sock "$IDB" "/sys/fs/cgroup$CGA" "$TGT"
logspoof
grep -E "REFUSED|attested" "$TMP/busd.err" | tail -1 | sed 's/^[0-9:]* /busd log: /'
kill "$TGT" 2>/dev/null || true

step "8g. the same attacker, plus it takes alpha's uid after entering the domain's user namespace (it owns that namespace: it was made by this user)"
restart --attest-domains=require --domain-ns "$IDA"
live_run
python3 "$ATK" setns /run/aurora/bus.sock "$IDB" "/sys/fs/cgroup$CGA" "$TGT" "$INNERA"
logspoof
grep -E "REFUSED|attested" "$TMP/busd.err" | tail -1 | sed 's/^[0-9:]* /busd log: /'
kill "$TGT" 2>/dev/null || true

step "8h. can a same-user attacker become alpha's uid ($UIDA) at all? (a) plain setuid from the host"
python3 "$ATK" setuid "$UIDA"

step "8i. (b) via a user namespace of its own, mapped over this user's subuid range (the one alpha uses): uid is the ONLY signal registered"
NOCG=1 restart --attest-domains=prefer
forge() { unshare --user --map-users=0:$(id -u):1 --map-users=1:$SUBSTART:$SUBCOUNT -- python3 "$ATK" forge "$SA" "$IDB" "$INNERA" "$@"; }
forge
logspoof
echo "(uid alone is forgeable by anyone who can use the user's subuid range, which is that same user)"

step "8j. (c) the same forged uid against the full stack (uid + cgroup + namespaces); it even joins the cgroup first"
restart --attest-domains=require --domain-ns "$IDA"
live_run
forge "/sys/fs/cgroup$CGA"
logspoof
grep -E "REFUSED|attested" "$TMP/busd.err" | tail -1 | sed 's/^[0-9:]* /busd log: /'
kill "$TGT" 2>/dev/null || true

step "stop"
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
for p in "${PIDS[@]}"; do wait "$p" 2>/dev/null || true; done
PIDS=()
echo "done: no processes left behind."
