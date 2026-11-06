#!/usr/bin/env bash
# Phase 7 demo: a real, pre-existing program (ImageMagick) running as an Aurora
# domain app, through busd + controllerd + a bwrap domain, with --require-isolation
# so there is no unisolated fallback. Every step is asserted; the script exits
# non-zero at the first thing that is wrong. Output is judged by decoding the
# bytes with imgcheck (Go decoders), never by ImageMagick's own say-so.
# Needs: go, bwrap, ImageMagick (magick) and jq on the host. Installs nothing.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
W=$(mktemp -d /tmp/magick-demo.XXXXXX)
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/mg.XXXXXX"); chmod 700 "$RUN"   # AF_UNIX paths stay short
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
near() { python3 -I -c 'import sys; a=list(map(int,sys.argv[1].split())); b=list(map(int,sys.argv[2].split())); t=int(sys.argv[3]); sys.exit(0 if all(abs(x-y)<=t for x,y in zip(a,b)) else 1)' "$1" "$2" "$3" \
        && ok "$4: pixel $1 within $3 of source $2" || fail "$4: pixel '$1' is not within $3 of source '$2'"; }

command -v magick >/dev/null && command -v jq >/dev/null && command -v bwrap >/dev/null || { echo "need magick, jq and bwrap on the host"; exit 1; }
export AURORA_BUS_SOCK=$RUN/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$RUN
mkdir -p "$W/bin"

step "build (go build -o <path> ./cmd/x)"
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || fail "bus build"
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || fail "domain build"
(cd "$ROOT/controller" && go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller ./cmd/aurora-preflight) || fail "controller build"
(cd "$ROOT/apps/lumen" && go build -o "$W/bin/genimage" ./cmd/genimage) || fail "genimage build"
(cd "$HERE" && go build -o "$W/bin/imgcheck" ./cmd/imgcheck) || fail "imgcheck build"
ok "built into $W/bin"
IC=$W/bin/imgcheck; C=$W/bin/controller
# only now: a throwaway HOME (go would otherwise put its module cache there)
export AURORA_POWERBOX_KEY=$W/powerbox.key HOME=$W/home
mkdir -p "$HOME/Pictures"
PIC=$HOME/Pictures

step "the library closure: what magick needs, and where the domain gets it"
echo "   host: $(magick -version | head -1)"
need=$(ldd /usr/bin/magick /usr/bin/jq | awk '/=>/ {print $3}' | sort -u)
mods=$(ls /usr/lib64/ImageMagick-7.*/modules-*/coders/{png,jpeg,gif,webp,bmp}.so)
outside=$(for f in $need $mods; do readlink -f "$f"; done | grep -v '^/usr/' || true)
eq "$(echo "$need" | wc -l) libs + 5 coder modules" "$(echo "$need" | wc -l) libs + $(echo "$mods" | wc -l) coder modules" "shared-library closure of magick+jq, plus the coders the manifest claims"
eq "${outside:-none}" none "every one resolves under /usr, which the domain binds read-only (nothing needs copying into the image)"
"$W/bin/domain" create probe >/dev/null || fail "domain create"
inside=$("$W/bin/domain" run probe -- sh -c 'for b in /usr/bin/magick /usr/bin/jq; do ldd $b; done | grep -c "not found"; ls /etc/IM-7 /etc/fonts 2>&1 | head -2' 2>/dev/null)
has "$inside" "0" "inside a domain, ldd reports 0 'not found'"
has "$inside" "No such file" "inside a domain, ImageMagick's /etc/IM-7 and /etc/fonts are NOT there (the app ships etc/policy.xml instead)"
"$W/bin/domain" destroy probe >/dev/null

step "start busd and controllerd with --require-isolation"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
[ -S "$AURORA_BUS_SOCK" ] || { cat "$W/busd.log"; fail "busd socket $AURORA_BUS_SOCK never bound"; }
"$W/bin/controllerd" --apps "$ROOT/apps" --domain-bin "$W/bin/domain" --require-isolation --no-compositor-approval >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
grep -q 'registered service' "$W/controllerd.log" || { cat "$W/controllerd.log"; fail "controllerd did not register"; }
grep -E 'loaded org.aurora.magick|SKIPPED.*magick' "$W/controllerd.log" | sed 's/^/   | /'
has "$($C apps)" org.aurora.magick "the registry lists the app"
has "$($C verbs --accepts image/webp)" "identify@v1" "verbs --accepts image/webp includes identify@v1"
"$W/bin/bus" sub provenance.created >"$W/events.log" 2>&1 & PIDS+=($!); sleep 0.5

step "inputs: a real 1200x800 PNG, a sibling file in the same directory, and a secret outside every grant"
"$W/bin/genimage" "$PIC/photo.png"
eq "$($IC info "$PIC/photo.png")" "png 1200 800" "photo.png decodes"
SRC00=$($IC px "$PIC/photo.png" 0 0); SRC_MID=$($IC px "$PIC/photo.png" 650 450); SRCGRAY=$($IC gray "$PIC/photo.png")
[ "$SRCGRAY" -gt 20 ] && ok "the source is colourful (gray deviation $SRCGRAY), so grayscale has work to do" || fail "source not colourful"
magick -size 40x40 xc:'#ff00aa' "$PIC/sibling.png"
magick -size 40x40 xc:'#00ffaa' "$W/secret.png"; echo "TOP-SECRET-TOKEN-7731" > "$W/secret.txt"

step "verb identify@v1 (read effect), through the controller"
OUT=$($C invoke org.aurora.magick identify@v1 --input "$PIC/photo.png" 2>&1); echo "$OUT" | sed 's/^/   | /' | head -30
has "$OUT" '"isolated": true' "the controller reports the run was isolated"
has "$OUT" '"width": 1200' "result width 1200 (checked against imgcheck above)"
has "$OUT" '"format": "PNG"' "format PNG"
has "$OUT" '"frames": 1' "frames 1"

step "verb resize@v1: width only keeps aspect"
$C invoke org.aurora.magick resize@v1 --input "$PIC/photo.png" --output "$PIC/small.png" --arg width=300 >"$W/r1.out" 2>&1 || { cat "$W/r1.out"; fail "resize failed"; }
eq "$($IC info "$PIC/small.png")" "png 300 200" "small.png decodes as a 300x200 PNG"
near "$($IC px "$PIC/small.png" 0 0)" "$SRC00" 10 "small.png corner (0,0)"
near "$($IC px "$PIC/small.png" 160 110)" "$SRC_MID" 25 "small.png at the scaled centre (160,110) vs source (650,450)"
step "verb resize@v1: exact width and height"
$C invoke org.aurora.magick resize@v1 --input "$PIC/photo.png" --output "$PIC/sq.jpg" --arg width=64 --arg height=64 >"$W/r2.out" 2>&1 || { cat "$W/r2.out"; fail "resize w+h failed"; }
eq "$($IC info "$PIC/sq.jpg")" "jpeg 64 64" "sq.jpg decodes as a 64x64 JPEG (format came from the extension)"

step "verb convert@v1: png -> jpeg at two qualities, webp, gif, bmp; each decoded with Go, not magick"
for q in 10 95; do
  $C invoke org.aurora.magick convert@v1 --input "$PIC/photo.png" --output "$PIC/q$q.jpg" --arg format=jpeg --arg quality=$q >"$W/c$q.out" 2>&1 || { cat "$W/c$q.out"; fail "convert q$q failed"; }
done
eq "$($IC info "$PIC/q95.jpg")" "jpeg 1200 800" "q95.jpg decodes as a 1200x800 JPEG"
eq "$(head -c2 "$PIC/q95.jpg" | xxd -p)" ffd8 "q95.jpg starts with the JPEG SOI marker"
near "$($IC px "$PIC/q95.jpg" 650 450)" "$SRC_MID" 12 "q95.jpg centre"
[ "$(stat -c %s "$PIC/q10.jpg")" -lt "$(stat -c %s "$PIC/q95.jpg")" ] && ok "quality is honoured: q10 is $(stat -c %s "$PIC/q10.jpg") bytes < q95 $(stat -c %s "$PIC/q95.jpg")" || fail "quality had no effect"
for f in webp gif bmp; do
  $C invoke org.aurora.magick convert@v1 --input "$PIC/photo.png" --output "$PIC/o.$f" --arg format=$f >"$W/c$f.out" 2>&1 || { cat "$W/c$f.out"; fail "convert $f failed"; }
  eq "$($IC info "$PIC/o.$f")" "$f 1200 800" "o.$f decodes as a 1200x800 $f"
done
step "re-open a converted file as input: webp -> png"
$C invoke org.aurora.magick convert@v1 --input "$PIC/o.webp" --output "$PIC/back.png" --arg format=png >"$W/cb.out" 2>&1 || { cat "$W/cb.out"; fail "webp->png failed"; }
eq "$($IC info "$PIC/back.png")" "png 1200 800" "back.png decodes"
near "$($IC px "$PIC/back.png" 650 450)" "$SRC_MID" 12 "back.png centre survived png->webp->png"

step "verb grayscale@v1"
$C invoke org.aurora.magick grayscale@v1 --input "$PIC/photo.png" --output "$PIC/gray.png" >"$W/g.out" 2>&1 || { cat "$W/g.out"; fail "grayscale failed"; }
eq "$($IC info "$PIC/gray.png")" "png 1200 800" "gray.png decodes"
eq "$($IC gray "$PIC/gray.png")" 0 "every pixel has R=G=B (source deviation was $SRCGRAY)"

step "failures are clean: a format outside the manifest, and a non-image input"
BAD=$($C invoke org.aurora.magick convert@v1 --input "$PIC/photo.png" --output "$PIC/x.pdf" --arg format=pdf 2>&1); has "$BAD" "format must be one of" "format=pdf is refused by the wrapper"
[ ! -s "$PIC/x.pdf" ] && ok "no output file left behind" || fail "x.pdf exists"
echo "not an image" > "$PIC/fake.png"
BAD=$($C invoke org.aurora.magick identify@v1 --input "$PIC/fake.png" 2>&1); has "$BAD" "app_failed" "a text file named .png is refused"

step "isolation proof: the domain is real; the app sees its granted input and nothing else"
DOMID=$($C provenance "$PIC/small.png" | jq -r .domain); DOM=$(cut -d: -f2 <<<"$DOMID")
has "$DOM" app-org-aurora "the output's provenance names the domain ($DOMID)"
APP=/domain/data/app/run
"$W/bin/domain" grant "$DOM" "$PIC/photo.png" --ro >/dev/null
# the manual probes talk to no busd, which the domain CLI warns about; drop only that line
dr() { "$W/bin/domain" run "$DOM" -- "$@" 2>&1 | grep -v "^domain: warning: bus "; }
req() { printf '{"input":"%s","args":{}}' "$1"; }
G=$(req "$PIC/photo.png" | dr $APP --verb identify@v1)
has "$G" '"width":1200' "the app, run in its domain with photo.png granted ro, CAN read it: $G"
S1=$(req "$PIC/sibling.png" | dr $APP --verb identify@v1); has "$S1" "cannot read input" "a sibling in the SAME directory is not readable: $S1"
S2=$(req "$W/secret.png" | dr $APP --verb identify@v1); has "$S2" "cannot read input" "a secret.png outside every grant is not readable: $S2"
echo "   on the host the same file IS readable: $(magick identify -format '%m %wx%h' "$W/secret.png")"
S3=$(dr cat "$W/secret.txt"); has "$S3" "No such file" "even plain cat of secret.txt fails inside the domain: $S3"
S4=$(dr ls "$PIC"); eq "$S4" "photo.png" "ls of the Pictures directory inside the domain shows only the granted file"
S5=$(dr sh -c "echo x > $PIC/photo.png"); has "$S5" "Read-only" "the granted input is read-only: $S5"
S6=$(dr sh -c "sed -n '3,\$p' /proc/net/dev | cut -d: -f1 | tr -d ' '" | tr '\n' ' '); eq "$S6" "lo " "no network inside: the only interface is loopback"
"$W/bin/domain" revoke "$DOM" "$PIC/photo.png" >/dev/null

step "the policy shipped with the app is enforced even if the domain would allow the path"
"$W/bin/domain" grant "$DOM" "$PIC/photo.png" --ro >/dev/null
POL=$(dr sh -c "MAGICK_CONFIGURE_PATH=/domain/data/app/etc magick $PIC/photo.png pdf:- 2>&1 | head -c 200")
has "$POL" "not authorized by the security policy" "magick refuses pdf:/svg: output under etc/policy.xml: ${POL:0:110}"
NOPOL=$(dr sh -c "magick $PIC/photo.png pdf:/tmp/control.pdf && head -c4 /tmp/control.pdf")
eq "$NOPOL" "%PDF" "control: with no policy the same domain happily writes a PDF, so the refusal above is the policy's doing"
"$W/bin/domain" revoke "$DOM" "$PIC/photo.png" >/dev/null

step "provenance: xattr, bus event, --verify"
for f in small.png sq.jpg q95.jpg o.webp gray.png; do
  P=$($C provenance "$PIC/$f") || fail "no provenance xattr on $f"
  [ "$(jq -r '.app+" "+(.isolated|tostring)' <<<"$P")" = "org.aurora.magick true" ] || fail "$f provenance wrong: $P"
done
ok "5 outputs carry user.aurora.provenance naming org.aurora.magick, isolated=true"
P=$($C provenance "$PIC/small.png")
eq "$(jq -r .verb <<<"$P")" resize@v1 "verb recorded"
eq "$(jq -r '.inputs[0]' <<<"$P")" "$PIC/photo.png" "input recorded"
eq "$(jq -r .domain_attested <<<"$P")" true "domain attested by the bus"
eq "$(jq -r '[.grants[]|.mode]|join(",")' <<<"$P")" "ro,rw" "grants are exactly input ro + output rw"
sleep 0.3
EV=$(grep -c "$PIC/small.png" "$W/events.log"); [ "$EV" -ge 1 ] && ok "provenance.created seen on the bus for small.png" || fail "no bus event for small.png"
VOUT=$($C provenance "$PIC/small.png" --verify 2>&1); VRC=$?
eq "$VRC" 0 "controller provenance --verify exits 0"
echo "$VOUT" | sed -n '/handle verification/,$p' | sed 's/^/   | /' | head -8
cp --preserve=xattr "$PIC/small.png" "$PIC/tampered.png"
python3 -I - "$PIC/tampered.png" <<'PY'
import os, sys, json
r = json.loads(os.getxattr(sys.argv[1], "user.aurora.provenance")); r["grants"][-1]["cap"] = "cap:0123456789abcdef"
os.setxattr(sys.argv[1], "user.aurora.provenance", json.dumps(r).encode())
PY
$C provenance "$PIC/tampered.png" --verify >/dev/null 2>&1; eq "$?" 3 "a tampered copy fails --verify with exit 3"

printf '\nALL %s STEPS PASSED\n' "$STEPS"
