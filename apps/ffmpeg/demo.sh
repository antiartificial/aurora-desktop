#!/usr/bin/env bash
# A second real, pre-existing program (ffmpeg) as an Aurora domain app, through
# busd + controllerd + a bwrap domain, with --require-isolation (no unisolated
# fallback). Every step is asserted; the script exits non-zero at the first
# thing that is wrong. Output is judged by cmd/mediacheck, which parses
# WAV/MP3/Ogg/FLAC/MP4/PNG/JPEG itself; ffmpeg's and ffprobe's own say-so is
# never the evidence. Needs: go, bwrap, ffmpeg, ffprobe, jq on the host.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
W=$(mktemp -d /tmp/ffmpeg-demo.XXXXXX)
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/ff.XXXXXX"); chmod 700 "$RUN"   # AF_UNIX paths stay short
PIDS=(); STEPS=0
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; wait 2>/dev/null
  for d in "$W"/domains/*; do [ -d "$d" ] && "$W/bin/domain" destroy "$(basename "$d")" >/dev/null 2>&1; done
  rm -rf "$W" "$RUN"
}
trap cleanup EXIT
step() { STEPS=$((STEPS+1)); printf '\n== Step %s: %s\n' "$STEPS" "$*"; }
ok()   { printf '   ok: %s\n' "$*"; }
fail() { printf '   FAIL: %s\n' "$*" >&2; exit 1; }
eq()   { [ "$1" = "$2" ] && ok "$3 ($1)" || fail "$3: got '$1', want '$2'"; }
has()  { case $1 in *"$2"*) ok "$3";; *) fail "$3 (no '$2' in: ${1:0:400})";; esac; }
# field NAME "k=v k=v ..." -> v
field() { tr ' ' '\n' <<<"$2" | sed -n "s/^$1=//p" | head -1; }
# within VALUE LOW HIGH WHAT
within() { awk -v v="$1" -v l="$2" -v h="$3" 'BEGIN{exit !(v>=l && v<=h)}' && ok "$4: $1 is within [$2, $3]" || fail "$4: $1 is outside [$2, $3]"; }

for t in ffmpeg ffprobe jq bwrap; do command -v $t >/dev/null || { echo "need $t on the host"; exit 1; }; done
export AURORA_BUS_SOCK=$RUN/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$RUN
mkdir -p "$W/bin"

step "build (go build -o <path> ./cmd/x)"
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || fail "bus build"
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || fail "domain build"
(cd "$ROOT/controller" && go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller) || fail "controller build"
(cd "$HERE" && go build -o "$W/bin/mediacheck" ./cmd/mediacheck) || fail "mediacheck build"
ok "built into $W/bin"
MC=$W/bin/mediacheck; C=$W/bin/controller
export AURORA_POWERBOX_KEY=$W/powerbox.key HOME=$W/home
mkdir -p "$HOME/Music" "$HOME/Videos" "$HOME/Pictures"
MUS=$HOME/Music; VID=$HOME/Videos; PIC=$HOME/Pictures

step "the closure: what ffmpeg needs, and where the domain gets it"
echo "   host: $(ffmpeg -version | head -1)"
need=$(ldd /usr/bin/ffmpeg /usr/bin/ffprobe /usr/bin/jq /usr/bin/timeout | awk '/=>/ {print $3}' | sort -u)
outside=$(for f in $need; do readlink -f "$f"; done | grep -v '^/usr/' || true)
echo "   $(wc -l <<<"$need") distinct shared libraries across ffmpeg, ffprobe, jq, timeout"
eq "${outside:-none}" none "every one resolves under /usr, which the domain binds read-only (nothing needs copying into the image)"
"$W/bin/domain" create probe >/dev/null || fail "domain create"
inside=$("$W/bin/domain" run probe -- sh -c 'for b in ffmpeg ffprobe jq timeout; do ldd $(command -v $b); done | grep -c "not found"; ls -d /etc/ffmpeg* 2>&1 | head -1' 2>/dev/null)
has "$inside" "0" "inside a domain, ldd reports 0 'not found'"
has "$inside" "No such file" "ffmpeg has no config directory on the host either, so unlike magick the app ships no etc/"
# the real test of "needs nothing outside /usr": encode every codec we claim, inside a bare domain, with no app files
enc=$("$W/bin/domain" run probe -- sh -c '
  cd /tmp && for spec in "pcm_s16le wav" "libmp3lame mp3" "libvorbis ogg" "flac flac"; do set -- $spec
    ffmpeg -nostdin -v error -y -f lavfi -i sine=duration=0.2 -c:a $1 x.$2 && echo "$1 ok $(stat -c %s x.$2)"; done
  ffmpeg -nostdin -v error -y -f lavfi -i testsrc=duration=0.4:size=64x48:rate=5 -c:v mpeg4 -f mp4 x.mp4 && echo "mpeg4+mp4 ok"
  ffmpeg -nostdin -v error -y -f lavfi -i testsrc=duration=0.2:size=64x48:rate=5 -frames:v 1 x.png && echo "png ok"' 2>&1 | grep -v "^domain: warning: bus ")
echo "$enc" | sed 's/^/   | /'
[ "$(grep -c ' ok' <<<"$enc")" = 6 ] && ok "all 6 encoders the manifest relies on run in a bare domain" || fail "an encoder failed in the bare domain"
"$W/bin/domain" destroy probe >/dev/null

step "start busd and controllerd with --require-isolation"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
[ -S "$AURORA_BUS_SOCK" ] || { cat "$W/busd.log"; fail "busd socket $AURORA_BUS_SOCK never bound"; }
"$W/bin/controllerd" --apps "$ROOT/apps" --domain-bin "$W/bin/domain" --require-isolation --no-compositor-approval >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
grep -q 'registered service' "$W/controllerd.log" || { cat "$W/controllerd.log"; fail "controllerd did not register"; }
grep -E 'loaded org.aurora.ffmpeg|SKIPPED.*ffmpeg' "$W/controllerd.log" | sed 's/^/   | /'
has "$($C apps)" org.aurora.ffmpeg "the registry lists the app"
has "$($C verbs --accepts video/mp4)" "poster@v1" "verbs --accepts video/mp4 includes poster@v1"
"$W/bin/bus" sub provenance.created >"$W/events.log" 2>&1 & PIDS+=($!); sleep 0.5

step "inputs, made with ffmpeg's synthetic sources so their content is known exactly"
# 2 s, 440 Hz sine, 44.1 kHz mono, 16-bit PCM
ffmpeg -nostdin -v error -y -f lavfi -i "sine=frequency=440:duration=2:sample_rate=44100" -c:a pcm_s16le "$MUS/tone.wav" || fail "tone"
# 2 s clip: 160x120 testsrc at 5 fps (10 frames) + the same 440 Hz sine as AAC
ffmpeg -nostdin -v error -y -f lavfi -i "sine=frequency=440:duration=2:sample_rate=44100" -f lavfi -i "testsrc=size=160x120:rate=5:duration=2" -c:a aac -c:v mpeg4 -pix_fmt yuv420p "$VID/clip.mp4" || fail "clip"
ffmpeg -nostdin -v error -y -f lavfi -i "testsrc=size=160x120:rate=5:duration=2" -c:v libvpx -f webm "$VID/clip.webm" || fail "webm"
ffmpeg -nostdin -v error -y -f lavfi -i "anullsrc=r=44100:cl=mono" -t 1 -c:a pcm_s16le "$MUS/silence.wav" || fail "silence"
echo "   sizes: tone.wav $(stat -c %s "$MUS/tone.wav"), clip.mp4 $(stat -c %s "$VID/clip.mp4"), clip.webm $(stat -c %s "$VID/clip.webm") bytes"
IN=$($MC wav "$MUS/tone.wav"); echo "   mediacheck: $IN"
TONE_PEAK=$(field peak "$IN"); TONE_RMS=$(field rms "$IN")
eq "$(field rate "$IN") $(field channels "$IN") $(field bits "$IN") $(field frames "$IN")" "44100 1 16 88200" "tone.wav is 44.1 kHz mono 16-bit, 88200 frames (2.000 s)"
$MC tone "$MUS/tone.wav" 440 >/dev/null && ok "tone.wav carries 440 Hz: $($MC tone "$MUS/tone.wav" 440)"
$MC tone "$MUS/tone.wav" 1000 >/dev/null 2>&1 && fail "the tone check passed at 1000 Hz" || ok "negative control: the tone check rejects 1000 Hz on the same file"
echo "secret $RANDOM" > "$W/secret.txt"; cp "$MUS/tone.wav" "$W/secret.wav"; cp "$MUS/tone.wav" "$MUS/sibling.wav"
$MC wav "$MUS/silence.wav" >/dev/null 2>&1 && fail "silent file passed the checker" || ok "negative control: mediacheck FAILS an all-zero WAV ($($MC wav "$MUS/silence.wav" 2>&1 | tail -1))"
head -c 100000 "$MUS/tone.wav" > "$W/trunc.wav"
$MC wav "$W/trunc.wav" >/dev/null 2>&1 && fail "truncated file passed the checker" || ok "negative control: mediacheck FAILS a truncated WAV ($($MC wav "$W/trunc.wav" 2>&1 | tail -1))"
CLIP=$($MC container "$VID/clip.mp4"); echo "   mediacheck: $CLIP"
eq "$(field video_frames "$CLIP") $(field width "$CLIP")x$(field height "$CLIP")" "10 160x120" "clip.mp4: 10 frames at 160x120 by its own stts and tkhd boxes"

step "verb probe@v1 (read effect), through the controller"
OUT=$($C invoke org.aurora.ffmpeg probe@v1 --input "$VID/clip.mp4" 2>&1); echo "$OUT" | sed 's/^/   | /' | head -20
has "$OUT" '"isolated": true' "the controller reports the run was isolated"
has "$OUT" '"width": 160' "width 160 (matches mediacheck's tkhd)"
has "$OUT" '"video_streams": 1' "one video stream"
has "$OUT" '"audio_streams": 1' "one audio stream"
has "$OUT" '"sample_rate": 44100' "sample rate 44100"
D=$(sed -n 's/.*"duration": *\([0-9.]*\).*/\1/p' <<<"$OUT" | head -1)
within "$D" 1.95 2.1 "probe duration vs the 2 s the clip was made with (mediacheck read $(field dur_ms "$CLIP") ms from mvhd)"
BAD=$($C invoke org.aurora.ffmpeg probe@v1 --input "$VID/clip.webm" 2>&1); has "$BAD" '"video_streams": 1' "probe of a webm works (the container is not mp4-specific)"

step "verb extract_audio@v1: mp4 -> WAV, resampled and downmixed"
t0=$(date +%s.%N)
$C invoke org.aurora.ffmpeg extract_audio@v1 --input "$VID/clip.mp4" --output "$MUS/clip.wav" --arg rate=22050 --arg channels=1 >"$W/e1.out" 2>&1 || { cat "$W/e1.out"; fail "extract-audio failed"; }
echo "   wall time through busd+controllerd+domain: $(awk -v a="$t0" -v b="$(date +%s.%N)" 'BEGIN{printf "%.2f s",b-a}')"
R=$($MC wav "$MUS/clip.wav") || fail "clip.wav fails the checker: $R"; echo "   mediacheck: $R"
eq "$(field rate "$R") $(field channels "$R") $(field bits "$R")" "22050 1 16" "clip.wav is 22.05 kHz mono 16-bit, as asked"
within "$(field frames "$R")" 43000 46000 "frame count (2 s at 22050 Hz is 44100; AAC adds a little priming)"
within "$(field peak "$R")" $((TONE_PEAK*8/10)) $((TONE_PEAK*12/10)) "peak amplitude vs the source sine's $TONE_PEAK"
within "$(field rms "$R")" "$(awk -v r="$TONE_RMS" 'BEGIN{print r*0.8}')" "$(awk -v r="$TONE_RMS" 'BEGIN{print r*1.2}')" "RMS vs the source sine's $TONE_RMS"
NZ=$(field nonzero "$R"); OF=$(field of "$R"); [ "$((NZ*100/OF))" -ge 95 ] && ok "$NZ of $OF samples are non-zero" || fail "mostly zeros: $NZ of $OF"
$MC tone "$MUS/clip.wav" 440 >/dev/null && ok "the extracted audio is still the 440 Hz tone: $($MC tone "$MUS/clip.wav" 440)" || fail "no 440 Hz in clip.wav"
$C invoke org.aurora.ffmpeg extract_audio@v1 --input "$VID/clip.mp4" --output "$MUS/clip44.wav" >/dev/null 2>&1 || fail "default extract failed"
R2=$($MC wav "$MUS/clip44.wav"); eq "$(field rate "$R2") $(field channels "$R2")" "44100 1" "with no args the source rate and channel count are kept"
$C invoke org.aurora.ffmpeg extract_audio@v1 --input "$VID/clip.webm" --output "$MUS/nov.wav" >"$W/nv.out" 2>&1 && fail "extract from a video with no audio succeeded"
has "$(cat "$W/nv.out")" "no audio stream" "a video with no audio track fails clean: $(grep -o 'ffmpeg-app: [^"]*' "$W/nv.out" | head -1)"
[ ! -e "$MUS/nov.wav" ] && ok "no output file left behind" || fail "nov.wav exists"

step "verb transcode_audio@v1: lossless round trip is bit-exact"
$C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.wav" --output "$MUS/tone.flac" --arg format=flac >"$W/f1.out" 2>&1 || { cat "$W/f1.out"; fail "wav->flac failed"; }
FL=$($MC container "$MUS/tone.flac"); echo "   mediacheck: $FL"
eq "$(field rate "$FL") $(field channels "$FL") $(field bits "$FL") $(field samples "$FL")" "44100 1 16 88200" "tone.flac STREAMINFO says 44.1 kHz mono 16-bit, 88200 samples"
echo "   flac is $(stat -c %s "$MUS/tone.flac") bytes vs $(stat -c %s "$MUS/tone.wav") for the wav"
$C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.flac" --output "$MUS/back.wav" --arg format=wav >"$W/f2.out" 2>&1 || { cat "$W/f2.out"; fail "flac->wav failed"; }
CMP=$($MC pcmcmp "$MUS/tone.wav" "$MUS/back.wav") && ok "wav -> flac -> wav: $CMP" || fail "round trip changed the samples: $CMP"

step "verb transcode_audio@v1: lossy mp3 and ogg; structure by mediacheck, signal by Goertzel"
for br in 32 192; do
  $C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.wav" --output "$MUS/b$br.mp3" --arg format=mp3 --arg bitrate=$br >"$W/m$br.out" 2>&1 || { cat "$W/m$br.out"; fail "mp3 $br failed"; }
done
M=$($MC container "$MUS/b192.mp3"); echo "   mediacheck: $M"
eq "$(field rate "$M") $(field channels "$M")" "44100 1" "b192.mp3 is 44.1 kHz mono, every frame header walks cleanly to the end of the file"
within "$(field dur_ms "$M")" 1950 2100 "mp3 duration by frame count (ms)"
S32=$(stat -c %s "$MUS/b32.mp3"); S192=$(stat -c %s "$MUS/b192.mp3")
[ "$S192" -gt $((S32*3)) ] && ok "bitrate is honoured: 32k -> $S32 bytes, 192k -> $S192 bytes" || fail "bitrate had no effect ($S32 vs $S192)"
within "$S32" $((32*1000/8*2*85/100)) $((32*1000/8*2*125/100)) "32 kbit/s for 2 s should be about 8000 bytes"
$C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/b192.mp3" --output "$MUS/fromm.wav" --arg format=wav >/dev/null 2>&1 || fail "mp3->wav failed"
R=$($MC wav "$MUS/fromm.wav"); echo "   mp3 -> wav: $R"
$MC tone "$MUS/fromm.wav" 440 >/dev/null && ok "440 Hz survived the lossy mp3 round trip: $($MC tone "$MUS/fromm.wav" 440)" || fail "tone lost"
$C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.wav" --output "$MUS/tone.ogg" --arg format=ogg --arg bitrate=64 >"$W/o.out" 2>&1 || { cat "$W/o.out"; fail "ogg failed"; }
O=$($MC container "$MUS/tone.ogg"); echo "   mediacheck: $O"
has "$O" "crc=ok" "every Ogg page's CRC32 verifies and the last page has the EOS flag"
eq "$(field rate "$O") $(field dur_ms "$O")" "44100 2000" "ogg is 44.1 kHz and its final granule position is exactly 88200 samples (2000 ms)"
$C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.ogg" --output "$MUS/fromo.wav" --arg format=wav >/dev/null 2>&1 || fail "ogg->wav failed"
$MC tone "$MUS/fromo.wav" 440 >/dev/null && ok "440 Hz survived the vorbis round trip: $($MC tone "$MUS/fromo.wav" 440)" || fail "tone lost"
BAD=$($C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.wav" --output "$MUS/x.aac" --arg format=aac 2>&1); has "$BAD" "format must be one of" "format=aac is refused: the manifest does not claim it"
[ ! -e "$MUS/x.aac" ] && ok "no output file left behind" || fail "x.aac exists"
BAD=$($C invoke org.aurora.ffmpeg transcode_audio@v1 --input "$MUS/tone.wav" --output "$MUS/x.mp3" --arg format=mp3 --arg bitrate=9999 2>&1); has "$BAD" "bitrate must be" "an absurd bitrate is refused by the wrapper"

step "verb poster@v1: frames from video, decoded with Go's png/jpeg"
$C invoke org.aurora.ffmpeg poster@v1 --input "$VID/clip.mp4" --output "$PIC/p0.png" >"$W/p0.out" 2>&1 || { cat "$W/p0.out"; fail "poster failed"; }
P0=$($MC container "$PIC/p0.png"); echo "   mediacheck: $P0"
eq "$(field width "$P0")x$(field height "$P0")" 160x120 "p0.png decodes as a 160x120 PNG"
[ "$(field colors "$P0")" -gt 20 ] && ok "the frame is the test pattern, not a blank ($(field colors "$P0") colours)" || fail "blank frame"
$C invoke org.aurora.ffmpeg poster@v1 --input "$VID/clip.mp4" --output "$PIC/p1.png" --arg at=1.2 --arg width=80 >"$W/p1.out" 2>&1 || { cat "$W/p1.out"; fail "poster at=1.2 failed"; }
eq "$($MC container "$PIC/p1.png" | cut -d' ' -f2-3)" "width=80 height=60" "width=80 keeps aspect: 80x60"
$C invoke org.aurora.ffmpeg poster@v1 --input "$VID/clip.mp4" --output "$PIC/p1full.png" --arg at=1.2 >/dev/null 2>&1 || fail "poster at=1.2 full"
DIFF=$($MC imgdiff "$PIC/p0.png" "$PIC/p1full.png"); echo "   frame at 0 s vs frame at 1.2 s: $DIFF"
[ "$(field differing "$DIFF")" -gt 100 ] && ok "different timestamps give different pictures, so -ss really seeks" || fail "same picture at both times"
$C invoke org.aurora.ffmpeg poster@v1 --input "$VID/clip.webm" --output "$PIC/pw.jpg" >"$W/pw.out" 2>&1 || { cat "$W/pw.out"; fail "poster from webm failed"; }
eq "$(head -c2 "$PIC/pw.jpg" | xxd -p)" ffd8 "pw.jpg (from a webm) starts with the JPEG SOI marker"
eq "$($MC container "$PIC/pw.jpg" | cut -d' ' -f1-3)" "jpeg width=160 height=120" "and decodes as a 160x120 JPEG"
BAD=$($C invoke org.aurora.ffmpeg poster@v1 --input "$VID/clip.mp4" --output "$PIC/late.png" --arg at=99 2>&1); has "$BAD" "no frame at 99s" "a timestamp past the end is a clean error"
[ ! -e "$PIC/late.png" ] && ok "no output file left behind" || fail "late.png exists"
BAD=$($C invoke org.aurora.ffmpeg poster@v1 --input "$VID/clip.mp4" --output "$PIC/x.tiff" 2>&1); has "$BAD" "cannot tell the output format" "an extension outside the manifest is refused"
BAD=$($C invoke org.aurora.ffmpeg poster@v1 --input "$MUS/tone.wav" --output "$PIC/x.png" 2>&1); has "$BAD" "unsupported_type" "poster of an audio file is refused by the controller (accepts is video only)"

step "verb scale_video@v1: mp4 -> mp4 at half width, boxes and frames counted by mediacheck"
$C invoke org.aurora.ffmpeg scale_video@v1 --input "$VID/clip.mp4" --output "$VID/small.mp4" --arg width=80 >"$W/sv.out" 2>&1 || { cat "$W/sv.out"; fail "scale-video failed"; }
V=$($MC container "$VID/small.mp4"); echo "   mediacheck: $V"
eq "$(field width "$V")x$(field height "$V") $(field video_frames "$V") $(field video_tracks "$V") $(field audio_tracks "$V")" "80x60 10 1 1" "small.mp4: 80x60, still 10 frames, one video and one audio track"
within "$(field dur_ms "$V")" 1900 2150 "mp4 duration by mvhd (ms)"
eq "$(head -c12 "$VID/small.mp4" | tail -c8 | head -c4)" ftyp "bytes 4..8 are the ftyp box"
$C invoke org.aurora.ffmpeg extract_audio@v1 --input "$VID/small.mp4" --output "$MUS/small.wav" >/dev/null 2>&1 || fail "extract from scaled video"
$MC tone "$MUS/small.wav" 440 >/dev/null && ok "chained: the audio inside the re-encoded video is still 440 Hz" || fail "tone lost in scale-video"

step "isolation proof: the domain is real; the app sees its granted input and nothing else"
DOM=$($C provenance "$MUS/clip.wav" | jq -r .domain | cut -d: -f2)
has "$DOM" app-org-aurora "the output's provenance names the domain ($DOM)"
APP=/domain/data/app/run
"$W/bin/domain" grant "$DOM" "$MUS/tone.wav" --ro >/dev/null
dr() { "$W/bin/domain" run "$DOM" -- "$@" 2>&1 | grep -v "^domain: warning: "; }
req() { printf '{"input":"%s","args":{}}' "$1"; }
G=$(req "$MUS/tone.wav" | dr $APP --verb probe@v1)
has "$G" '"sample_rate":44100' "the app, run in its domain with tone.wav granted ro, CAN read it: $G"
S1=$(req "$MUS/sibling.wav" | dr $APP --verb probe@v1); has "$S1" "cannot read input" "a sibling in the SAME directory is not readable: $S1"
S2=$(req "$W/secret.wav" | dr $APP --verb probe@v1); has "$S2" "cannot read input" "a secret.wav outside every grant is not readable: $S2"
echo "   on the host the same file IS readable: $(ffprobe -v error -show_entries stream=sample_rate -of csv=p=0 "$W/secret.wav")"
S3=$(dr cat "$W/secret.txt"); has "$S3" "No such file" "even plain cat of secret.txt fails inside the domain: $S3"
S4=$(dr ls "$MUS"); eq "$S4" "tone.wav" "ls of the Music directory inside the domain shows only the granted file"
S5=$(dr sh -c "echo x > $MUS/tone.wav"); has "$S5" "Read-only" "the granted input is read-only: $S5"
S6=$(dr sh -c "sed -n '3,\$p' /proc/net/dev | cut -d: -f1 | tr -d ' '" | tr '\n' ' '); eq "$S6" "lo " "no network inside: the only interface is loopback"
# a playlist is the classic way to make ffmpeg read some other file. ffmpeg's own default (-safe 1) already refuses
# absolute paths in one; the control turns that off to show what the domain and the "file:" prefix are standing behind.
printf "ffconcat version 1.0\nfile '%s'\n" "$W/secret.wav" > "$W/list.wav"
ffmpeg -nostdin -v error -y -f concat -safe 0 -i "$W/list.wav" -c:a pcm_s16le "$W/viaconcat.wav" 2>/dev/null && [ -s "$W/viaconcat.wav" ] && ok "control: on the host, with -safe 0, a concat playlist DOES pull in the secret's audio ($(stat -c %s "$W/viaconcat.wav") bytes)" || ok "control: concat is not usable here either"
cp "$W/list.wav" "$MUS/list.wav"; "$W/bin/domain" grant "$DOM" "$MUS/list.wav" --ro >/dev/null
PL=$(req "$MUS/list.wav" | dr $APP --verb probe@v1); echo "   probe of the playlist, in the domain: ${PL:0:200}"
case $PL in *'"ok":true'*'"audio_streams":1'*) fail "the playlist leaked the secret's audio stream";; *) ok "the playlist did not yield the secret's audio";; esac
"$W/bin/domain" revoke "$DOM" "$MUS/tone.wav" >/dev/null; "$W/bin/domain" revoke "$DOM" "$MUS/list.wav" >/dev/null

step "limits: what run.sh enforces itself, shown biting; what the domain does not enforce"
LIM=$("$W/bin/domain" limit "$DOM" --memory 512M --cpu 50 --pids 64 2>&1); echo "$LIM" | sed 's/^/   | /' | head -8
"$W/bin/domain" grant "$DOM" "$VID/clip.mp4" --ro >/dev/null; "$W/bin/domain" grant "$DOM" "$W/lim.wav" --rw >/dev/null 2>&1 || { : > "$W/lim.wav"; "$W/bin/domain" grant "$DOM" "$W/lim.wav" --rw >/dev/null; }
lreq() { printf '{"input":"%s","output":"%s","args":{}}' "$VID/clip.mp4" "$W/lim.wav"; }
L0=$(lreq | dr $APP --verb extract_audio@v1); has "$L0" '"ok":true' "control: with the default limits the same request succeeds"
L1=$(lreq | dr env FFMPEG_APP_VMEM_KB=60000 $APP --verb extract_audio@v1); has "$L1" "ffmpeg failed" "address-space cap (ulimit -v 60 MB): ffmpeg cannot even start: ${L1:0:160}"
L2=$(lreq | dr env FFMPEG_APP_FSIZE_BLK=64 $APP --verb extract_audio@v1); has "$L2" "ffmpeg failed" "output-size cap (32 KB): ffmpeg is stopped part way through the 88 KB it would write"
# a long job: 20 s of 720p scaled up to 3840 wide (a 40 MB output, a couple of seconds of 4 threads), with a 1 s wall-clock cap
ffmpeg -nostdin -v error -y -f lavfi -i "testsrc2=size=1280x720:rate=25:duration=20" -c:v mpeg4 -q:v 8 "$W/long.mp4" || fail "long.mp4"
"$W/bin/domain" grant "$DOM" "$W/long.mp4" --ro >/dev/null; "$W/bin/domain" grant "$DOM" "$W/long-out.mp4" --rw >/dev/null 2>&1 || { : > "$W/long-out.mp4"; "$W/bin/domain" grant "$DOM" "$W/long-out.mp4" --rw >/dev/null; }
lq() { printf '{"input":"%s","output":"%s","args":{"width":3840}}' "$W/long.mp4" "$W/long-out.mp4"; }
ts=$(date +%s.%N); L3=$(lq | dr $APP --verb scale_video@v1); te=$(date +%s.%N)
has "$L3" '"ok":true' "control: the uncapped 20 s 720p -> 4K re-encode succeeds: ${L3:0:90}"
echo "   uncapped 20 s 720p -> 4K re-encode: $(awk -v a="$ts" -v b="$te" 'BEGIN{printf "%.1f s",b-a}')"
ts=$(date +%s.%N); L4=$(lq | dr env FFMPEG_APP_WALL_S=1 $APP --verb scale_video@v1); te=$(date +%s.%N)
has "$L4" "ffmpeg failed" "wall-clock cap (1 s): the same job is killed: ${L4:0:100}"
within "$(awk -v a="$ts" -v b="$te" 'BEGIN{printf "%.1f",b-a}')" 0.5 3.5 "seconds until it was stopped"
"$W/bin/domain" revoke "$DOM" "$W/long.mp4" >/dev/null

step "provenance: xattr, bus event, --verify"
for f in Music/clip.wav Music/tone.flac Music/b192.mp3 Music/tone.ogg Pictures/p1.png Pictures/pw.jpg Videos/small.mp4; do
  P=$($C provenance "$HOME/$f") || fail "no provenance xattr on $f"
  [ "$(jq -r '.app+" "+(.isolated|tostring)' <<<"$P")" = "org.aurora.ffmpeg true" ] || fail "$f provenance wrong: $P"
done
ok "7 outputs (wav flac mp3 ogg png jpg mp4) carry user.aurora.provenance naming org.aurora.ffmpeg, isolated=true"
P=$($C provenance "$MUS/clip.wav")
eq "$(jq -r .verb <<<"$P")" extract_audio@v1 "verb recorded"
eq "$(jq -r '.inputs[0]' <<<"$P")" "$VID/clip.mp4" "input recorded"
eq "$(jq -r .domain_attested <<<"$P")" true "domain attested by the bus"
eq "$(jq -r '[.grants[]|.mode]|join(",")' <<<"$P")" "ro,rw" "grants are exactly input ro + output rw"
sleep 0.3
EV=$(grep -c "$MUS/clip.wav" "$W/events.log"); [ "$EV" -ge 1 ] && ok "provenance.created seen on the bus for clip.wav" || fail "no bus event for clip.wav"
VOUT=$($C provenance "$MUS/clip.wav" --verify 2>&1); VRC=$?
eq "$VRC" 0 "controller provenance --verify exits 0"
echo "$VOUT" | sed -n '/handle verification/,$p' | sed 's/^/   | /' | head -8
cp --preserve=xattr "$MUS/clip.wav" "$MUS/tampered.wav"
python3 -I - "$MUS/tampered.wav" <<'PY'
import os, sys, json
r = json.loads(os.getxattr(sys.argv[1], "user.aurora.provenance")); r["grants"][-1]["cap"] = "cap:0123456789abcdef"
os.setxattr(sys.argv[1], "user.aurora.provenance", json.dumps(r).encode())
PY
$C provenance "$MUS/tampered.wav" --verify >/dev/null 2>&1; eq "$?" 3 "a tampered copy fails --verify with exit 3"

printf '\nALL %s STEPS PASSED\n' "$STEPS"
