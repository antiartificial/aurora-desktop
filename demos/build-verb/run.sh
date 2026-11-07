#!/usr/bin/env bash
# Building as a first-class operation: the host's C compiler runs inside an
# Aurora domain as a typed verb, granted exactly its source (ro) and its output
# (rw), offline, and the binary it produces carries a provenance record that
# names the app, the verb, the grants and the toolchain. Every claim is
# asserted from decoded output; the script exits non-zero at the first thing
# that is wrong. Reproducibility is MEASURED here, and the result is printed
# whichever way it comes out.
# Real stack: busd + controllerd (--require-isolation) + bwrap domains; the
# compositor is not involved, so this does not touch any running desktop.
# Needs on the host: go, bwrap, cc, jq, readelf, cmp, sha256sum, python3.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
W=$(mktemp -d /tmp/build-demo.XXXXXX)
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/bv.XXXXXX"); chmod 700 "$RUN"   # AF_UNIX paths stay short
PIDS=(); STEPS=0; CHECKS=0
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  for d in "$W"/domains/*; do [ -d "$d" ] && "$W/bin/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done
  rm -rf "$W" "$RUN"
}
trap cleanup EXIT
step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n' "$STEPS" "$*"; }
ok()   { CHECKS=$((CHECKS+1)); printf '   ok: %s\n' "$*"; }
note() { printf '   .. %s\n' "$*"; }
fail() { printf '   FAIL: %s\n' "$*" >&2; exit 1; }
eq()   { [ "$1" = "$2" ] && ok "$3 ($1)" || fail "$3: got '$1', want '$2'"; }
ne()   { [ "$1" != "$2" ] && ok "$3" || fail "$3: both are '$1'"; }
has()  { case $1 in *"$2"*) ok "$3";; *) fail "$3 (no '$2' in: ${1:0:500})";; esac; }
js()   { jq -r "$1"; }

for t in go bwrap cc jq readelf cmp sha256sum python3; do command -v $t >/dev/null || { echo "need $t on the host"; exit 1; }; done
export AURORA_BUS_SOCK=$RUN/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$RUN
mkdir -p "$W/bin"
APP=org.aurora.build

step "build the stack (go build -o <path>)"
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || fail "bus build"
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || fail "domain build"
(cd "$ROOT/controller" && go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller) || fail "controller build"
ok "built into $W/bin"
C=$W/bin/controller; D=$W/bin/domain
export AURORA_POWERBOX_KEY=$W/powerbox.key HOME=$W/home
mkdir -p "$HOME"

step "start busd and controllerd with --require-isolation"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
[ -S "$AURORA_BUS_SOCK" ] || { cat "$W/busd.log"; fail "busd socket never bound"; }
"$W/bin/controllerd" --apps "$ROOT/apps" --domain-bin "$D" --require-isolation --no-compositor-approval >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
grep -q 'registered service' "$W/controllerd.log" || { cat "$W/controllerd.log"; fail "controllerd did not register"; }
has "$($C apps)" "$APP" "the registry lists the build app"
VERBS=$($C verbs --accepts text/x-csrc); has "$VERBS" "compile@v1" "verbs --accepts text/x-csrc lists compile@v1"
has "$VERBS" "verify@v1" "and verify@v1"

step "inputs: a C program, a sibling file in its directory, and a secret outside every grant"
mkdir -p "$W/proj" "$W/proj2" "$W/out"
cat > "$W/proj/hello.c" <<'EOF'
#include <stdio.h>
int main(void) { printf("hello from a provenanced build: 6*7=%d\n", 6 * 7); return 0; }
EOF
cp "$W/proj/hello.c" "$W/proj2/hello.c"
echo 'int sibling_value = 1;' > "$W/proj/sibling.h"
echo '#define SECRET_TOKEN "TOP-SECRET-7731"' > "$W/secret.h"
# a source that reaches for a file it was not granted: a sibling of its own source
printf '#include "%s/sibling.h"\n#include <stdio.h>\nint main(void){printf("%%d\\n", sibling_value);return 0;}\n' "$W/proj" > "$W/proj/needs_sibling.c"
printf '#include "%s/secret.h"\n#include <stdio.h>\nint main(void){puts(SECRET_TOKEN);return 0;}\n' "$W" > "$W/proj/needs_secret.c"
# a source that bakes the build time into the binary
cat > "$W/proj/stamp.c" <<'EOF'
#include <stdio.h>
int main(void) { puts("built " __DATE__ " " __TIME__); return 0; }
EOF
HOSTCC=$(cc --version | head -1)
ok "host compiler: $HOSTCC"
cc -fsyntax-only "$W/proj/needs_sibling.c" && ok "control: on the host the sibling include resolves" || fail "control failed"

step "the toolchain is already inside every domain, and the domain is offline"
"$D" create probe >/dev/null || fail "domain create"
inside=$("$D" run probe -- sh -c 'cc --version | head -1; ls /usr/bin/as /usr/bin/ld; test -r /usr/include/stdio.h && echo headers-present' 2>&1 | grep -v '^domain: warning: bus ')
has "$inside" "$HOSTCC" "cc inside the domain is the host's cc: $HOSTCC"
has "$inside" "/usr/bin/ld" "as and ld are there too"
has "$inside" "headers-present" "libc headers are visible (the read-only /usr)"
IFS_IN=$("$D" run probe -- sh -c 'sed -n "3,\$p" /proc/net/dev | cut -d: -f1 | tr -d " "' 2>/dev/null)
eq "$IFS_IN" "lo" "the only network interface is loopback (no net grant, so --unshare-net)"
"$D" destroy probe >/dev/null

step "compile@v1: a source compiles inside a domain, and the binary runs"
OUT=$($C invoke $APP compile@v1 --input "$W/proj/hello.c" --output "$W/out/hello" 2>&1); echo "$OUT" | sed 's/^/   | /' | head -50
has "$OUT" '"isolated": true' "the controller reports the run was isolated"
[ -x "$W/out/hello" ] && ok "the output is executable" || fail "output is not executable"
RAN=$("$W/out/hello"); eq "$RAN" "hello from a provenanced build: 6*7=42" "the binary runs and prints the expected line"
eq "$(head -c4 "$W/out/hello" | od -An -c | tr -d ' ')" '177ELF' "and it is an ELF file"

step "what the domain could see: the source and the output, nothing else"
NS=$($C invoke $APP compile@v1 --input "$W/proj/needs_sibling.c" --output "$W/out/ns" 2>&1); RC=$?
[ $RC != 0 ] && ok "compile of a source that includes its sibling FAILS (exit $RC)" || fail "sibling include succeeded inside the domain"
has "$NS" "app_failed" "typed failure: app_failed"
has "$NS" "sibling.h" "the compiler's own error names the file it could not see"
[ ! -e "$W/out/ns" ] && ok "no output file left behind" || fail "failed build left $W/out/ns"
NX=$($C invoke $APP compile@v1 --input "$W/proj/needs_secret.c" --output "$W/out/nx" 2>&1); RC=$?
[ $RC != 0 ] && has "$NX" "secret.h" "a header elsewhere on the host (secret.h) is not visible either" || fail "secret include succeeded"
cc -fsyntax-only "$W/proj/needs_secret.c" && ok "control: on the host the same secret.h IS readable" || fail "control failed"
DOM=$($C provenance "$W/out/hello" | js .domain | cut -d: -f2)
"$D" grant "$DOM" "$W/proj/hello.c" --ro >/dev/null; "$D" grant "$DOM" "$W/out/probe.bin" --rw >/dev/null 2>&1 || { : > "$W/out/probe.bin"; "$D" grant "$DOM" "$W/out/probe.bin" --rw >/dev/null; }
dr() { "$D" run "$DOM" -- "$@" 2>&1 | grep -v "^domain: warning: bus "; }
eq "$(dr ls "$W/proj")" "hello.c" "inside the domain the source directory holds only the granted hello.c"
eq "$(dr ls "$W/out")" "probe.bin" "and the output directory only the one granted output"
has "$(dr sh -c "echo x > $W/proj/hello.c")" "Read-only" "the source is read-only"
has "$(dr cat "$W/secret.h")" "No such file" "plain cat of a host file outside the grants fails"
dr sh -c "echo x > $W/out/other.bin" >/dev/null
[ ! -e "$W/out/other.bin" ] && ok "a second file written next to the output inside the domain never reaches the host directory" || fail "other.bin reached the host"
has "$(dr sh -c "echo x > /usr/bin/cc")" "Read-only" "the toolchain itself is read-only"
"$D" revoke "$DOM" "$W/proj/hello.c" >/dev/null; "$D" revoke "$DOM" "$W/out/probe.bin" >/dev/null

step "provenance: the record names the app, the verb, the grants and the toolchain"
$C provenance "$W/out/hello" >"$W/prov.json" || fail "no provenance on the binary"
P=$(cat "$W/prov.json")
eq "$(js .app <<<"$P")" "$APP" "app"
eq "$(js .verb <<<"$P")" "compile@v1" "verb"
eq "$(js .isolated <<<"$P")" true "isolated"
eq "$(js .domain_attested <<<"$P")" true "the domain is attested by the bus"
eq "$(js '.inputs[0]' <<<"$P")" "$W/proj/hello.c" "input recorded"
eq "$(js '[.grants[]|.mode]|join(",")' <<<"$P")" "ro,rw" "grants are exactly: source ro, output rw"
eq "$(js '.grants[0].path' <<<"$P")" "$W/proj/hello.c" "grant 0 is the source file itself (not its directory)"
eq "$(js '.grants[1].path' <<<"$P")" "$W/out/hello" "grant 1 is the output file itself"
for i in 0 1; do
  eq "$(js ".grants[$i].source" <<<"$P")" gesture "grant $i source (nothing in the manifest covers it, the caller named it)"
  case "$(js ".grants[$i].cap" <<<"$P")" in cap:*) ok "grant $i has a daemon-minted $(js ".grants[$i].cap" <<<"$P")";; *) fail "grant $i has no cap id";; esac
done
eq "$(js .reported.toolchain.cc <<<"$P")" "$HOSTCC" "reported toolchain.cc is the compiler"
eq "$(js .reported.toolchain.cc_sha256 <<<"$P")" "$(sha256sum "$(readlink -f "$(command -v cc)")" | cut -d' ' -f1)" "reported cc_sha256 is the sha256 of the cc binary (checked on the host)"
has "$(js .reported.toolchain.ld <<<"$P")" "GNU ld" "reported linker: $(js .reported.toolchain.ld <<<"$P")"
has "$(js .reported.flags <<<"$P")" "-O2" "reported flags: $(js .reported.flags <<<"$P")"
eq "$(js .reported.output_sha256 <<<"$P")" "$(sha256sum "$W/out/hello" | cut -d' ' -f1)" "reported output_sha256 is the sha256 of the binary, recomputed on the host"
eq "$(js .reported.source_sha256 <<<"$P")" "$(sha256sum "$W/proj/hello.c" | cut -d' ' -f1)" "reported source_sha256 is the sha256 of the source, recomputed on the host"
# the same compiler string, read out of the binary's own bytes
has "$(readelf -p .comment "$W/out/hello" 2>&1)" "$(cut -d'(' -f2- <<<"$HOSTCC" | sed 's/^[^)]*) //')" "the ELF .comment section independently names the compiler: $(readelf -p .comment "$W/out/hello" 2>&1 | grep -m1 GCC | sed 's/^ *\[ *[0-9a-f]*\] *//')"
VER=$($C provenance "$W/out/hello" --verify 2>&1); RC=$?
[ $RC = 0 ] && ok "controller provenance --verify exits 0: $(tail -1 <<<"$VER" | cut -c1-70)" || fail "verify exit $RC"
cp --preserve=xattr "$W/out/hello" "$W/out/tampered"
python3 -I - "$W/out/tampered" <<'PY'
import os, sys, json
a = "user.aurora.provenance"
r = json.loads(os.getxattr(sys.argv[1], a)); r["grants"][-1]["cap"] = "cap:0123456789abcdef"
os.setxattr(sys.argv[1], a, json.dumps(r).encode())
PY
$C provenance "$W/out/tampered" --verify >/dev/null 2>&1; eq "$?" 3 "a tampered copy of the record fails --verify (exit 3)"

step "reproducibility, measured (1/2): plain cc, same source, two fresh domains, two host paths"
# What a bare compiler invocation does. -g is on because that is what an
# engineer's default build looks like; the point is to see what leaks in.
naive() { # domain src outfile [cc flags...]
  local dom=$1 src=$2 outf=$3; shift 3
  "$D" create "$dom" >/dev/null 2>&1; "$D" grant "$dom" "$src" --ro >/dev/null
  "$D" run "$dom" -- sh -c "cd $(dirname "$src") && cc $* $(basename "$src") -o /tmp/o && cat /tmp/o" 2>/dev/null > "$outf"
  "$D" destroy "$dom" >/dev/null 2>&1
}
naive nv1 "$W/proj/hello.c"  "$W/n1" -O2 -g
naive nv2 "$W/proj2/hello.c" "$W/n2" -O2 -g
[ -s "$W/n1" ] && [ -s "$W/n2" ] || fail "naive builds produced nothing"
NDIFF=$(cmp -l "$W/n1" "$W/n2" | wc -l)
if [ "$NDIFF" = 0 ]; then note "RESULT: plain 'cc -O2 -g' in two domains: byte-identical (sha256 $(sha256sum < "$W/n1" | cut -c1-16))"
else note "RESULT: plain 'cc -O2 -g' from two source paths: NOT byte-identical, $NDIFF bytes differ of $(stat -c %s "$W/n1") (sha256 $(sha256sum < "$W/n1" | cut -c1-12) vs $(sha256sum < "$W/n2" | cut -c1-12))"
  note "what differs (strings found in exactly one build's .debug_line_str, via readelf -p):"
  for f in n1 n2; do readelf -p .debug_line_str "$W/$f" 2>/dev/null | grep -o '/[^ ]*' | sort -u > "$W/$f.str"; done
  comm -3 "$W/n1.str" "$W/n2.str" | sed 's/^/       /' | head -6
  [ "$NDIFF" -gt 0 ] && ok "measured: the default build is path-dependent (this is the number the app has to remove)"
fi
# same measurement, no -g: does the path still leak?
naive nv3 "$W/proj/hello.c"  "$W/n3" -O2
naive nv4 "$W/proj2/hello.c" "$W/n4" -O2
note "RESULT: plain 'cc -O2' (no -g) from two source paths: $(cmp -l "$W/n3" "$W/n4" | wc -l) bytes differ"

step "reproducibility, measured (2/2): the build app's fixed flags, two domains, two host paths"
"$D" destroy "$DOM" >/dev/null 2>&1
$C invoke $APP compile@v1 --input "$W/proj/hello.c" --output "$W/out/r1" >/dev/null 2>&1 || fail "build 1"
D1=$($C provenance "$W/out/r1" | js .domain)
"$D" destroy "$DOM" >/dev/null 2>&1; [ ! -d "$W/domains/$DOM" ] && ok "domain $DOM destroyed between the builds" || fail "domain not destroyed"
$C invoke $APP compile@v1 --input "$W/proj2/hello.c" --output "$W/out/r2" >/dev/null 2>&1 || fail "build 2"
D2=$($C provenance "$W/out/r2" | js .domain)
note "build 1 ran in $D1"; note "build 2 ran in $D2"
ne "$D1" "$D2" "the two builds ran in two different domain instances (different exec ids)"
S1=$(sha256sum < "$W/out/r1" | cut -d' ' -f1); S2=$(sha256sum < "$W/out/r2" | cut -d' ' -f1)
if cmp -s "$W/out/r1" "$W/out/r2"; then
  note "RESULT: byte-identical across two domains and two host paths (sha256 $S1)"; ok "measured: reproducible for this source+toolchain+flags"
else
  note "RESULT: NOT byte-identical: $(cmp -l "$W/out/r1" "$W/out/r2" | wc -l) bytes differ"; cmp -l "$W/out/r1" "$W/out/r2" | head -5 | sed 's/^/       /'
  fail "the app's pinned flags did not remove the nondeterminism"
fi
# what it does NOT cover, each measured
cp "$W/proj/hello.c" "$W/proj/renamed.c"
$C invoke $APP compile@v1 --input "$W/proj/renamed.c" --output "$W/out/r3" >/dev/null 2>&1 || fail "build 3"
S3=$(sha256sum < "$W/out/r3" | cut -d' ' -f1)
[ "$S3" != "$S1" ] && note "RESULT: same bytes of source under a different FILE NAME: NOT identical ($(cmp -l "$W/out/r1" "$W/out/r3" 2>/dev/null | wc -l) bytes differ; the name is embedded as the ELF FILE symbol). The base name is an input." || note "RESULT: renaming the source changed nothing"
$C invoke $APP compile@v1 --input "$W/proj/stamp.c" --output "$W/out/t1" >/dev/null 2>&1 || fail "stamp build 1"
sleep 2
$C invoke $APP compile@v1 --input "$W/proj/stamp.c" --output "$W/out/t2" >/dev/null 2>&1 || fail "stamp build 2"
T1=$("$W/out/t1"); T2=$("$W/out/t2")
note "stamp.c (uses __DATE__/__TIME__) built twice, 2s apart: '$T1' vs '$T2'"
ne "$T1" "$T2" "measured: a source that embeds __TIME__ is NOT reproducible by default"
$C invoke $APP compile@v1 --input "$W/proj/stamp.c" --output "$W/out/t3" --arg source_date_epoch=1700000000 >/dev/null 2>&1 || fail "stamp build 3"
sleep 2
$C invoke $APP compile@v1 --input "$W/proj/stamp.c" --output "$W/out/t4" --arg source_date_epoch=1700000000 >/dev/null 2>&1 || fail "stamp build 4"
cmp -s "$W/out/t3" "$W/out/t4" && ok "with source_date_epoch pinned, the same two builds ARE byte-identical ('$("$W/out/t3")')" || fail "SOURCE_DATE_EPOCH did not make the stamp reproducible"
eq "$(js .reported.source_date_epoch < <($C provenance "$W/out/t3"))" 1700000000 "and the pinned epoch is recorded in the binary's provenance"
note "NOT claimed: reproducibility across compiler versions or hosts (the toolchain hash is recorded so a difference is detectable, see next step); only this host's cc was exercised."

step "verify@v1: rebuild from the recorded provenance and compare"
vargs() { # binary -> --arg list from its own provenance
  local p; p=$($C provenance "$1")
  printf '%s\0' --arg "expect_sha256=\"$(js .reported.output_sha256 <<<"$p")\"" \
    --arg "expect_cc_sha256=\"$(js .reported.toolchain.cc_sha256 <<<"$p")\"" \
    --arg "expect_flags=$(jq -c .reported.flags <<<"$p")"
}
mapfile -d '' VA < <(vargs "$W/out/hello")
V=$($C invoke $APP verify@v1 --input "$W/proj/hello.c" --output "$W/out/rebuild" "${VA[@]}" 2>&1); echo "$V" | sed 's/^/   | /' | grep -E '"(identical|toolchain_match|flags_match|expected_sha256|rebuilt_sha256)"'
eq "$(echo "$V" | js .result.identical)" true "verify@v1: the rebuild is byte-identical to the recorded binary"
eq "$(echo "$V" | js .result.toolchain_match)" true "the toolchain matches the recorded hash"
eq "$(echo "$V" | js .result.flags_match)" true "the flags match the recorded flags"
cmp -s "$W/out/hello" "$W/out/rebuild" && ok "and cmp(1) on the host agrees: the files are identical" || fail "cmp differs although verify said identical"
eq "$($C provenance "$W/out/rebuild" | js .verb)" verify@v1 "the scratch rebuild carries its own provenance: verb verify@v1"
eq "$($C provenance "$W/out/rebuild" | js .reported.rebuild_of_sha256)" "$(js .reported.output_sha256 <<<"$P")" "naming the sha256 it is a rebuild of"
# negative 1: the source changed after the build
sed 's/6 \* 7/6 * 8/' "$W/proj/hello.c" > "$W/proj/hello_edit.c"; mkdir -p "$W/proj3"; mv "$W/proj/hello_edit.c" "$W/proj3/hello.c"
V2=$($C invoke $APP verify@v1 --input "$W/proj3/hello.c" --output "$W/out/rebuild2" "${VA[@]}" 2>&1)
eq "$(echo "$V2" | js .result.identical)" false "negative: with the source edited (6*8), verify@v1 reports NOT identical"
# negative 2: the binary was altered after the build
cp "$W/out/hello" "$W/out/hello.patched"; printf 'X' | dd of="$W/out/hello.patched" bs=1 seek=100 conv=notrunc 2>/dev/null
ne "$(sha256sum < "$W/out/hello.patched")" "$(sha256sum < "$W/out/hello")" "a one-byte patch changes the binary's hash"
eq "$([ "$(sha256sum < "$W/out/hello.patched" | cut -d' ' -f1)" = "$(js .result.rebuilt_sha256 <<<"$V")" ] && echo same || echo different)" different "so the patched binary no longer matches what the recorded provenance rebuilds to"
# negative 3: a different compiler than the one recorded
V3=$($C invoke $APP verify@v1 --input "$W/proj/hello.c" --output "$W/out/rebuild3" --arg "expect_sha256=\"$(js .reported.output_sha256 <<<"$P")\"" --arg 'expect_cc_sha256="0000000000000000000000000000000000000000000000000000000000000000"' 2>&1)
eq "$(echo "$V3" | js .result.toolchain_match)" false "negative: a recorded compiler hash that is not this one is reported as toolchain_match=false"

step "the build domain is gone and nothing leaked"
for d in "$W"/domains/*; do [ -d "$d" ] && "$D" destroy "$(basename "$d")" >/dev/null 2>&1; done
[ -z "$(ls "$W/domains" 2>/dev/null)" ] && ok "no domains left" || fail "domains remain: $(ls "$W/domains")"
[ ! -e "$W/out/other.bin" ] && ok "the probe's attempted second output never existed on the host" || fail "other.bin exists"

printf '\nBUILD-VERB DEMO PASSED (%s steps, %s checks)\n' "$STEPS" "$CHECKS"
