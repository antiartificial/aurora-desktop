#!/usr/bin/env bash
# Phase 9 slice demo: saved queries over an index built from the bus's own
# provenance.created events, with live updates. Starts busd, controllerd (real
# bwrap domains, --require-isolation), indexd; saves queries; runs real
# ImageMagick invokes; times how long a new file takes to appear in a watching
# query; shows its provenance and its derived-from edge. Prints a pass/fail
# count and exits non-zero if any check failed.
#
# The last step is the capability test: a query asked from INSIDE the ImageMagick
# domain and from inside the ffmpeg domain (real bwrap, real attested busd
# sockets) returns only that domain's files, and the user's own session sees
# both. It also tries to ask for the other domain's view by naming it in the
# payload and by forging the envelope's `from`, polls for live matches from
# both domains, and subscribes to ** from inside a domain.
#
# What this demo does NOT show, or shows as still open (see index/README.md):
#   - the index's own filter does not cover the bus; that is busd's job. busd
#     never delivers system topics (provenance. approval. bus. agent. index.)
#     to a domain connection. The last step ASSERTS that: a domain's ** sees a
#     canary and no provenance.created, and an explicit subscribe is refused.
#   - fanotify, history from btrfs snapshots, xattr attributes, a GUI/dock.
# Needs: go, bwrap, ImageMagick (magick), ffmpeg, jq, python3. Installs nothing.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
W=$(mktemp -d /tmp/lq-demo.XXXXXX)
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/lq.XXXXXX"); chmod 700 "$RUN"   # AF_UNIX paths stay short
PIDS=(); STEPS=0; PASS=0; FAIL=0
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  for p in $(ps -o pid= -C aurora); do grep -qa "$W" "/proc/$p/cmdline" 2>/dev/null && [ "$p" != "$$" ] && kill "$p" 2>/dev/null; done
  wait 2>/dev/null
  for d in "$W"/domains/*; do [ -d "$d" ] && "$W/bin/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done
  rm -rf "$W" "$RUN"
}
trap cleanup EXIT
step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n' "$STEPS" "$*"; }
ok()   { PASS=$((PASS+1)); printf '   ok: %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '   FAIL: %s\n' "$*" >&2; }
die()  { printf '   FATAL: %s\n' "$*" >&2; FAIL=$((FAIL+1)); summary; exit 1; }
eq()   { [ "$1" = "$2" ] && ok "$3 ($1)" || bad "$3: got '$1', want '$2'"; }
has()  { case $1 in *"$2"*) ok "$3";; *) bad "$3 (no '$2' in: ${1:0:500})";; esac; }
lacks(){ case $1 in *"$2"*) bad "$3 ('$2' present)";; *) ok "$3";; esac; }
indent() { sed 's/^/   | /'; }

summary() { printf '\n%s checks passed, %s failed (%s steps)\n' "$PASS" "$FAIL" "$STEPS"; }
us() { echo "${EPOCHREALTIME/./}"; }                       # microseconds, no fork
ms() { python3 -I -c 'import sys; print(round(int(sys.argv[1])/1000,1))' "$1"; }

for t in magick ffmpeg jq bwrap python3; do command -v $t >/dev/null || { echo "need $t on the host"; exit 1; }; done
export AURORA_BUS_SOCK=$RUN/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$RUN
mkdir -p "$W/bin"

step "build (go build -o <path> ./cmd/x)"
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || die "bus build"
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || die "domain build"
(cd "$ROOT/controller" && go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller) || die "controller build"
(cd "$ROOT/index" && go build -o "$W/bin/indexd" ./cmd/indexd) || die "indexd build"
(cd "$ROOT/shell" && go build -o "$W/bin/aurora" ./cmd/aurora) || die "aurora build"
(cd "$ROOT/apps/lumen" && go build -o "$W/bin/genimage" ./cmd/genimage) || die "genimage build"
# static copies, handed to a domain read-only in the capability step
(cd "$ROOT/shell" && CGO_ENABLED=0 go build -o "$W/dbin/aurora" ./cmd/aurora) || die "static aurora build"
(cd "$ROOT/bus" && CGO_ENABLED=0 go build -o "$W/dbin/bus" ./cmd/bus) || die "static bus build"
ok "built into $W/bin"
A=$W/bin/aurora; C=$W/bin/controller; B=$W/bin/bus
export AURORA_POWERBOX_KEY=$W/powerbox.key HOME=$W/home
PIC=$HOME/Pictures; mkdir -p "$PIC"

step "start busd, controllerd and indexd (each socket checked, not assumed)"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
[ -S "$AURORA_BUS_SOCK" ] || { cat "$W/busd.log"; die "busd socket $AURORA_BUS_SOCK never bound"; }
"$W/bin/controllerd" --apps "$ROOT/apps" --domain-bin "$W/bin/domain" --require-isolation --no-compositor-approval >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
grep -q 'registered service' "$W/controllerd.log" || { cat "$W/controllerd.log"; die "controllerd did not register"; }
IDX=$W/index; QD=$IDX/queries; mkdir -p "$QD"
start_indexd() { "$W/bin/indexd" --data-dir "$IDX" --queries "$QD" >>"$W/indexd.log" 2>&1 & INDEXD=$!; PIDS+=($INDEXD)
  for _ in $(seq 50); do grep -q 'serving "index"' "$W/indexd.log" && break; sleep 0.1; done
  grep -q 'serving "index"' "$W/indexd.log" || { cat "$W/indexd.log"; die "indexd did not register"; }; }
start_indexd
has "$("$B" inspect 2>&1)" index "the bus lists the 'index' service"
echo "   indexd: $(head -1 "$W/indexd.log")"

step "save queries: a query is a small file in indexd's queries directory"
cat >"$QD/images-this-week.query" <<'Q'
# The plan's exit-demo query.
title: Images exported this week
mime: image/*
since: this-week
app: org.aurora.magick
Q
printf 'title: Anything derived from photo.png\nderived-from: %s/photo.png\n' "$PIC" >"$QD/from-photo.query"
printf 'title: Audio, any time\nmime: audio/*\n' >"$QD/audio.query"
printf 'colour: red\n' >"$QD/broken.query"
LIST=$($A query 2>&1); echo "$LIST" | indent
has "$LIST" "Images exported this week" "the plan's query is listed"
has "$LIST" "broken" "a query that does not parse is listed too, not dropped"
has "$LIST" "!! does not parse" "and marked as broken"
has "$LIST" "index: 0 entries" "the index starts empty"

step "inputs: photo.png exists BEFORE anything runs through Aurora; the index must not know it"
"$W/bin/genimage" "$PIC/photo.png"
SHOW=$($A query show "$PIC/photo.png" 2>&1); echo "$SHOW" | indent | head -4
has "$SHOW" "not in the index" "photo.png is on disk but not indexed: the index only knows events it saw"
has "$SHOW" "does NOT mean" "and says that is not evidence Aurora never made it"
R0=$($A query run images-this-week 2>&1)
has "$R0" "0 match(es)" "images-this-week is empty"

step "two watchers subscribe: one for people, one --json for timing"
$A query run images-this-week --watch --count 3 2>"$W/w1.err" | while IFS= read -r l; do printf '%s %s\n' "$(us)" "$l"; done >"$W/watch.txt" & PIDS+=($!)
$A query run images-this-week --watch --count 3 --json 2>"$W/w2.err" | while IFS= read -r l; do printf '%s %s\n' "$(us)" "$l"; done >"$W/watch.json" & PIDS+=($!)
for _ in $(seq 50); do grep -q 'watching images-this-week' "$W/watch.txt" && break; sleep 0.1; done
has "$(cat "$W/watch.txt")" "watching images-this-week by long poll" "the text watcher took its snapshot and is polling index.next@v1"
sleep 0.3
eq "$("$B" inspect 2>&1 | grep -c 'index.match')" 0 "no client holds a subscription to any index.match topic (there is none: a topic reaches every subscriber, a poll answer reaches only the asker)"

step "a real invoke: ImageMagick converts photo.png in a bwrap domain; time until the file shows up in the watching query"
T0=$(us)
$C invoke org.aurora.magick convert@v1 --input "$PIC/photo.png" --output "$PIC/export.png" --arg format=png >"$W/i1.out" 2>&1 || { cat "$W/i1.out"; die "invoke failed"; }
T1=$(us)
has "$(cat "$W/i1.out")" '"isolated": true' "the controller says the run was isolated"
has "$(cat "$W/i1.out")" '"provenance"' "the invoke REPLY carries the provenance record, so a caller needs no provenance.created subscription"
echo "   the reply's provenance record:"; jq -c '.provenance' "$W/i1.out" 2>/dev/null | cut -c1-400 | indent
has "$(jq -r '.provenance.file // .provenance.output // empty' "$W/i1.out" 2>/dev/null)" "$PIC/export.png" "and that record names the output file"
for _ in $(seq 100); do grep -q '"live":true' "$W/watch.json" && break; sleep 0.05; done
LIVE=$(cut -d' ' -f2- "$W/watch.json" | jq -c 'select(.live)' 2>/dev/null | head -1)
TL=$(grep -m1 '"live":true' "$W/watch.json" | cut -d' ' -f1)
if [ -z "$LIVE" ]; then bad "no live match arrived"; else
  eq "$(jq -r .hit.file <<<"$LIVE")" "$PIC/export.png" "the live match is the new file"
  eq "$(jq -r .hit.mime <<<"$LIVE")" image/png "type derived from the extension"
  eq "$(jq -r .hit.app <<<"$LIVE")" org.aurora.magick "app, as the record states it"
  has "$(jq -r .hit.sender <<<"$LIVE")" ":controllerd" "the event's sender, stamped by busd: $(jq -r .hit.sender <<<"$LIVE")"
  OA=$(python3 -I -c 'import sys,datetime; t=datetime.datetime.fromisoformat(sys.argv[1].replace("Z","+00:00")); print(int(t.timestamp()*1e6))' "$(jq -r .hit.observed_at <<<"$LIVE")")
  RT=$(( $(jq -r .hit.record_ts <<<"$LIVE" | cut -c1-19) / 1000 ))
  echo "   measured, this run, this host (all one machine's clock):"
  echo "     invoke started   -> match line printed by the watcher: $(ms $((TL-T0))) ms  (includes the whole bwrap run of ImageMagick)"
  echo "     invoke returned  -> match line printed by the watcher: $(ms $((TL-T1))) ms  (negative would mean the match beat the reply)"
  echo "     controller record ts -> indexd saw it:                 $(ms $((OA-RT))) ms"
  echo "     indexd saw it   -> match line printed by the watcher: $(ms $((TL-OA))) ms"
  echo "     controller record ts -> match line printed:            $(ms $((TL-RT))) ms   <- the index's share of the wait"
  [ $((TL-RT)) -lt 1000000 ] && ok "the new file reached the watching query $(ms $((TL-RT))) ms after the controller wrote its provenance record (< 1000 ms)" || bad "match took $(ms $((TL-RT))) ms from the record"
fi

step "two more writes: a jpeg export, then a resize that OVERWRITES export.png"
$C invoke org.aurora.magick convert@v1 --input "$PIC/photo.png" --output "$PIC/export.jpg" --arg format=jpeg >"$W/i2.out" 2>&1 || { cat "$W/i2.out"; die "jpeg invoke failed"; }
$C invoke org.aurora.magick resize@v1 --input "$PIC/photo.png" --output "$PIC/export.png" --arg width=300 >"$W/i3.out" 2>&1 || { cat "$W/i3.out"; die "resize invoke failed"; }
for _ in $(seq 100); do [ "$(grep -c '"live":true' "$W/watch.json")" -ge 3 ] && break; sleep 0.05; done
eq "$(grep -c '"live":true' "$W/watch.json")" 3 "the watcher saw three live matches (one per write, the overwrite included)"
echo "   what a person watching saw:"; grep -E '^[0-9]+ \+ ' "$W/watch.txt" | cut -d' ' -f2- | indent
has "$(cat "$W/watch.txt")" "VIEW: indexd says it saw this request from uid" "the watcher printed whose view it got (the session's)"
lacks "$(cat "$W/watch.txt")" "THE FILTER COVERS THE INDEX ONLY" "the obsolete 'provenance.created is broadcast' warning is gone (busd closes it)"
lacks "$(cat "$W/watch.txt")" "NOT FILTERED" "the old no-filter warning is gone"

step "run the saved queries now"
R=$($A query run images-this-week 2>&1); echo "$R" | indent
has "$R" "2 match(es)" "two files (export.png was written twice but is one file)"
has "$R" "export.jpg" "the jpeg is in"
RF=$($A query run from-photo 2>&1); has "$RF" "2 match(es)" "from-photo: both outputs are derived from photo.png"
RA=$($A query run audio 2>&1); has "$RA" "0 match(es)" "audio: nothing, the predicates are ANDed and applied"
JQ=$($A query run images-this-week --json | jq -s 'length'); eq "$JQ" 2 "--json: one line per match"

step "provenance and history of the new file, then its edges"
S=$($A query show "$PIC/export.png" 2>&1); echo "$S" | indent | head -24
has "$S" "2 indexed write(s)" "history: both writes of export.png, oldest first"
has "$S" "convert@v1" "the first write's verb"
has "$S" "resize@v1" "the second write's verb"
has "$S" "derived from $PIC/photo.png" "the derived-from edge names the input"
has "$S" "(stamped by busd)" "the publisher is marked as the bus's word"
has "$S" "indexd did not check" "domain trust is the controller's claim, labelled as such"
eq "$(grep -o 'attested' <<<"$S" | wc -l)" "$(grep -o 'controller says the bus attested it' <<<"$S" | wc -l)" "every use of the word 'attested' is the controller's claim, labelled as such"
XP=$($C provenance "$PIC/export.png"); eq "$(jq -r '.inputs[0]' <<<"$XP")" "$PIC/photo.png" "cross-check: the xattr on the file (a different source) records the same input"
eq "$(jq -r .verb <<<"$XP")" resize@v1 "cross-check: the xattr holds the latest write's verb"
U=$($A query show "$PIC/photo.png" 2>&1 | grep 'used in')
has "$U" "export.jpg" "reverse edge: photo.png was 'used in' export.jpg"
has "$U" "export.png" "reverse edge: and export.png"

step "honesty checks: a deletion behind the index's back, a restart, a forged event"
rm "$PIC/export.jpg"
has "$($A query run images-this-week 2>&1 | grep export.jpg)" "MISSING now" "a file deleted behind the index's back is flagged MISSING (an lstat at answer time), not silently kept as present"
kill $INDEXD; wait $INDEXD 2>/dev/null; start_indexd
RR=$($A query run images-this-week 2>&1)
has "$RR" "2 match(es)" "after killing and restarting indexd the matches are back (replayed from the append-only index.jsonl)"
$B pub provenance.created "{\"file\":\"$W/forged.png\",\"app\":\"org.aurora.magick\",\"verb\":\"convert@v1\",\"inputs\":[],\"ts\":\"x\"}" >/dev/null 2>&1; sleep 0.5
has "$(cat "$W/indexd.log")" "not the controller" "an event from a non-controller sender is refused by indexd"
lacks "$($A query run images-this-week 2>&1)" forged.png "and the forged file is not in the query"
has "$RR" "as a session" "the run output says which view the caller got"
lacks "$RR" "THE FILTER COVERS THE INDEX ONLY" "and the obsolete broadcast warning is gone from the run output"
has "$RR" "invisible" "and that files made outside Aurora or before indexd ran are not known"
[ "$(jq -s 'length' "$IDX/index.jsonl" 2>/dev/null)" = 3 ] && ok "index.jsonl holds exactly 3 entries (one per provenance event)" || bad "index.jsonl: $(wc -l <"$IDX/index.jsonl") lines"

step "capabilities: the same questions asked from INSIDE the magick domain and the ffmpeg domain (real bwrap, busd-stamped identities)"
MUS=$HOME/Music; mkdir -p "$MUS"
ffmpeg -nostdin -v error -y -f lavfi -i "sine=frequency=440:duration=1:sample_rate=44100" -c:a pcm_s16le "$MUS/tone.wav" || die "tone"
$C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.wav" --output "$MUS/tone.flac" --arg format=flac >"$W/f1.out" 2>&1 || { cat "$W/f1.out"; die "ffmpeg invoke failed"; }
# the controller names an app's domain app-<11 chars of the id>-<first 8 bytes of sha256(id)> (invoke.DomainName)
dname() { printf 'app-org-aurora-%s' "$(printf '%s' "$1" | sha256sum | cut -c1-16)"; }
DM=$(dname org.aurora.magick); DF=$(dname org.aurora.ffmpeg)
[ -d "$W/domains/$DM" ] && [ -d "$W/domains/$DF" ] || die "expected domains $DM and $DF, have: $(ls "$W/domains")"
IDM=$("$W/bin/domain" exec-id "$DM"); IDF=$("$W/bin/domain" exec-id "$DF")
echo "   magick domain: $IDM"; echo "   ffmpeg domain: $IDF"
for d in "$DM" "$DF"; do "$W/bin/domain" grant "$d" "$W/dbin" --ro >/dev/null || die "grant the static binaries to $d"; done
inM() { "$W/bin/domain" run "$DM" -- "$@" 2>"$W/inM.err"; }
inF() { "$W/bin/domain" run "$DF" -- "$@" 2>"$W/inF.err"; }
SOCK=/run/aurora/bus.sock; DA=$W/dbin/aurora; DB=$W/dbin/bus
# the control: what the user's own session sees
SA=$($A query run audio 2>&1); SI=$($A query run images-this-week 2>&1)
has "$SA" "1 match(es)" "session: audio has 1 match"; has "$SA" "tone.flac" "session: it is the ffmpeg domain's tone.flac"
has "$SI" "export.png" "session: images-this-week has the magick domain's files"
# the adversarial case: the magick domain asks for audio, which only the ffmpeg domain made
MA=$(inM $DA query run audio --socket $SOCK); echo "$MA" | indent | head -9
has "$MA" "0 match(es)" "magick domain: audio has 0 matches (the ffmpeg domain's tone.flac is NOT visible)"
lacks "$MA" "tone.flac" "magick domain: tone.flac does not appear anywhere in the reply"
has "$MA" "indexd says it saw this request from $IDM" "magick domain: indexd reports the bus-stamped identity it filtered for"
has "$MA" "as a domain" "magick domain: and that it applied the domain rule"
MI=$(inM $DA query run images-this-week --socket $SOCK)
has "$MI" "export.png" "magick domain: sees its OWN export.png"; has "$MI" "export.jpg" "magick domain: and its own export.jpg"
lacks "$MI" "tone.flac" "magick domain: still no tone.flac"
FA=$(inF $DA query run audio --socket $SOCK); FI=$(inF $DA query run images-this-week --socket $SOCK)
has "$FA" "1 match(es)" "ffmpeg domain: sees its own tone.flac"
has "$FI" "0 match(es)" "ffmpeg domain: images-this-week has 0 matches"
lacks "$FI" "export." "ffmpeg domain: none of the magick domain's files appear"
MS=$(inM $DA query show "$MUS/tone.flac" --socket $SOCK); MSR=$?
has "$MS" "not in the index, or not visible to you" "magick domain: show on the other domain's file answers as if it were not indexed"
lacks "$MS" "transcode_audio" "magick domain: no provenance of the other domain's file leaks through show"
FS=$(inF $DA query show "$PIC/photo.png" --socket $SOCK)
lacks "$FS" "export" "ffmpeg domain: show of the shared input photo.png does not list the magick domain's outputs under 'used in'"
has "$($A query show "$PIC/photo.png" 2>&1)" "export.png" "session: the same show does list them"
# naming someone else does nothing: payload fields, then a forged envelope `from`
for who in "$IDF" "uid$(id -u):bash"; do
  FORGED=$(inM $DB send index index.run@v1 "{\"name\":\"audio\",\"domain\":\"$who\",\"as\":\"$who\",\"viewer\":\"$who\",\"from\":\"$who\",\"identity\":\"$who\",\"kind\":\"session\"}" --socket $SOCK)
  eq "$(jq -r '.total' <<<"$FORGED")" 0 "magick domain asking for audio 'as' $who: still 0 matches"
  eq "$(jq -r '.viewer.identity' <<<"$FORGED")" "$IDM" "the viewer indexd used is still the bus-stamped $IDM"
done
RAW=$(inM python3 -I -c '
import socket, json, sys
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
s.sendall((json.dumps({"kind": "request", "id": "forge1", "to": "index", "verb": "index.run@v1", "from": sys.argv[2], "payload": {"name": "audio"}}) + "\n").encode())
print(s.makefile().readline())' $SOCK "uid$(id -u):bash")
eq "$(jq -r '.payload.total' <<<"$RAW")" 0 "a raw request with a forged envelope from=uid$(id -u):bash (the user's session): still 0 matches"
eq "$(jq -r '.from' <<<"$RAW")" "uid$(id -u):indexd" "the reply is from indexd, stamped by busd"
# live: polls and a ** subscriber inside the domains, then new files from each domain
ASOF=$($B send index index.run@v1 '{"name":"audio"}' | jq -r .as_of_seq)
poll() { # poll <in-fn> <query> <outfile>: one index.next@v1 held for up to 6 s
  "$1" $DB send index index.next@v1 "{\"name\":\"$2\",\"after\":$ASOF,\"wait_ms\":6000}" --socket $SOCK >"$3" 2>&1 & PIDS+=($!); BG+=($!); }
BG=()
poll inM audio "$W/pM-a.json"; poll inF audio "$W/pF-a.json"; poll inM images-this-week "$W/pM-i.json"; poll inF images-this-week "$W/pF-i.json"
$B send index index.next@v1 "{\"name\":\"audio\",\"after\":$ASOF,\"wait_ms\":6000}" >"$W/pS-a.json" 2>&1 & PIDS+=($!); BG+=($!)
inM timeout 7 $DB sub '**' --socket $SOCK >"$W/subM.txt" 2>&1 & PIDS+=($!); BG+=($!)
sleep 1.5
$C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.wav" --output "$MUS/tone2.flac" --arg format=flac >"$W/f2.out" 2>&1 || { cat "$W/f2.out"; die "second ffmpeg invoke failed"; }
$C invoke org.aurora.magick resize@v1 --input "$PIC/photo.png" --output "$PIC/export2.png" --arg width=200 >"$W/i4.out" 2>&1 || { cat "$W/i4.out"; die "magick invoke failed"; }
$B pub demo.canary '{"n":1}' >/dev/null 2>&1
wait "${BG[@]}" 2>/dev/null
eq "$(jq -r '.hits[].file' "$W/pS-a.json" 2>/dev/null)" "$MUS/tone2.flac" "live, session: its audio poll was answered with the ffmpeg domain's new tone2.flac"
eq "$(jq -r '.hits[].file' "$W/pF-a.json" 2>/dev/null)" "$MUS/tone2.flac" "live, ffmpeg domain: its own new file arrived"
eq "$(jq -r '.hits | length' "$W/pM-a.json" 2>/dev/null)" 0 "live, magick domain: its audio poll came back EMPTY (waited out; never told about tone2.flac)"
lacks "$(cat "$W/pM-a.json")" tone2 "live, magick domain: tone2 appears nowhere in its reply"
eq "$(jq -r '.hits[].file' "$W/pM-i.json" 2>/dev/null)" "$PIC/export2.png" "live, magick domain: its own new export2.png arrived"
eq "$(jq -r '.hits | length' "$W/pF-i.json" 2>/dev/null)" 0 "live, ffmpeg domain: its images poll came back EMPTY (never told about export2.png)"
lacks "$(cat "$W/pF-i.json")" export2 "live, ffmpeg domain: export2 appears nowhere in its reply"
has "$(cat "$W/subM.txt")" "demo.canary" "control: the magick domain's ** subscription was live (it saw the canary event)"
lacks "$(cat "$W/subM.txt")" "index.match" "the magick domain's ** subscription received NO index.match.* event for either new file"
lacks "$(cat "$W/subM.txt")" "provenance.created" "LEAK CLOSED: the magick domain's ** subscription received NO provenance.created at all"
lacks "$(cat "$W/subM.txt")" "tone2.flac" "LEAK CLOSED: and the ffmpeg domain's file name tone2.flac appears nowhere in what it received"
SUBERR=$(inM timeout 5 $DB sub 'provenance.created' --socket $SOCK 2>&1; cat "$W/inM.err")   # inM sends the tool's stderr to inM.err
has "$SUBERR" "reserved_topic" "an explicit subscribe to provenance.created from the magick domain is refused with reserved_topic"
[ "$(jq -s 'length' "$IDX/index.jsonl" 2>/dev/null)" = 6 ] && ok "index.jsonl holds exactly 6 entries (export.png twice, export.jpg, tone.flac, tone2.flac, export2.png: one per provenance event)" || bad "index.jsonl: $(wc -l <"$IDX/index.jsonl") lines"

# a domain loses nothing by not hearing provenance.created: the registry.invoke@v1 REPLY to a call made from inside the domain carries the record of its own output
DOMINV=$(inM timeout 60 $DB send registry registry.invoke@v1 "{\"app\":\"org.aurora.magick\",\"verb\":\"resize@v1\",\"input\":\"$PIC/photo.png\",\"output\":\"$PIC/fromdomain.png\",\"args\":{\"width\":50}}" --socket $SOCK 2>/dev/null)
eq "$(jq -r '.provenance.file' <<<"$DOMINV" 2>/dev/null)" "$PIC/fromdomain.png" "a registry.invoke@v1 made from INSIDE the magick domain: its reply carries the provenance record of its own output"
eq "$(jq -r '.provenance.verb' <<<"$DOMINV" 2>/dev/null)" "resize@v1" "with the verb, from the reply, not from any subscription"

summary
[ "$FAIL" = 0 ]
