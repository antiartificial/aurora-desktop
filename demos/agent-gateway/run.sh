#!/usr/bin/env bash
# Phase 10 slice demo: an agent gateway, driven by a SCRIPT that speaks MCP over
# stdio (no LLM). Real busd, real controllerd (bwrap domains, --require-isolation),
# real ImageMagick, the real aurora-gateway binary. It shows: a policy-filtered
# tool list generated from manifests; a read call; a write call that produces a
# real file with provenance; a destructive call that stops at an approval, is
# DENIED and leaves no file; the same call allowed as a control; and the audit
# trail on the bus, as `aurora activity` renders it. Pass/fail count; exits
# non-zero if any check failed.
#
# The "user" who denies and allows is this script calling approval.decide@v1 as
# the same uid. That is a stand-in: the gateway has no way to decide, but any
# same-uid process does, and this demo does not claim otherwise (gateway/README.md).
#
# NOT shown, because not built: an agent's writes staged in a snapshot (the
# write below lands in the real directory), taint labels, a GUI, a real LLM.
# Needs: go, bwrap, ImageMagick (magick), jq, python3. Installs nothing.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
W=$(mktemp -d /tmp/gw-demo.XXXXXX)
RUN=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/gw.XXXXXX"); chmod 700 "$RUN"   # AF_UNIX paths stay short
DRVP=; PIDS=(); STEPS=0; PASS=0; FAIL=0
cleanup() {
  [ -n "${DRVP:-}" ] && { eval "exec ${DRV[1]}>&-" 2>/dev/null; sleep 0.3; kill "$DRVP" 2>/dev/null; }
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
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

for t in magick jq bwrap python3; do command -v $t >/dev/null || { echo "need $t on the host"; exit 1; }; done
export AURORA_BUS_SOCK=$RUN/bus.sock AURORA_DOMAINS_DIR=$W/domains XDG_RUNTIME_DIR=$RUN
mkdir -p "$W/bin"

step "build (go build -o <path> ./cmd/x)"
(cd "$ROOT/bus" && go build -o "$W/bin/" ./cmd/busd ./cmd/bus) || die "bus build"
(cd "$ROOT/adapter/linux" && go build -o "$W/bin/domain" ./cmd/domain) || die "domain build"
(cd "$ROOT/controller" && go build -o "$W/bin/" ./cmd/controllerd ./cmd/controller ./cmd/aurora-preflight) || die "controller build"
(cd "$ROOT/shell" && go build -o "$W/bin/aurora" ./cmd/aurora) || die "aurora build"
(cd "$ROOT/gateway" && go build -o "$W/bin/aurora-gateway" ./cmd/aurora-gateway && go build -o "$W/bin/mcpdrive" ./cmd/mcpdrive) || die "gateway build"
(cd "$ROOT/apps/lumen" && go build -o "$W/bin/genimage" ./cmd/genimage) || die "genimage build"
(cd "$ROOT/apps/magick" && go build -o "$W/bin/imgcheck" ./cmd/imgcheck) || die "imgcheck build"
ok "built into $W/bin"
A=$W/bin/aurora; C=$W/bin/controller; B=$W/bin/bus; IC=$W/bin/imgcheck
export AURORA_POWERBOX_KEY=$W/powerbox.key HOME=$W/home
PIC=$HOME/Pictures; AG=$PIC/agent; mkdir -p "$AG"

step "start busd and controllerd (each socket checked, not assumed); approvals on the bus route, 60s timeout"
"$W/bin/busd" --socket "$AURORA_BUS_SOCK" --data-dir "$W/log" >"$W/busd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do [ -S "$AURORA_BUS_SOCK" ] && break; sleep 0.1; done
[ -S "$AURORA_BUS_SOCK" ] || { cat "$W/busd.log"; die "busd socket $AURORA_BUS_SOCK never bound"; }
"$W/bin/controllerd" --apps "$ROOT/apps,$HERE/apps" --domain-bin "$W/bin/domain" --require-isolation --no-compositor-approval --approval-timeout 60s >"$W/controllerd.log" 2>&1 & PIDS+=($!)
for _ in $(seq 50); do grep -q 'registered service' "$W/controllerd.log" && break; sleep 0.1; done
grep -q 'registered service' "$W/controllerd.log" || { cat "$W/controllerd.log"; die "controllerd did not register"; }
grep -E 'loaded org.aurora.(magick|demo.purge)|SKIPPED' "$W/controllerd.log" | indent
CAT=$($C verbs 2>&1)
has "$CAT" "redact@v1" "the registry's catalogue has the demo's destructive verb"
has "$CAT" "grayscale@v1" "the registry's catalogue has magick's verbs"
NCAT=$(echo "$CAT" | grep -c '@v1'); echo "   the full catalogue the registry serves: $NCAT verbs across $($C apps | grep -c 'verbs:') apps"
"$B" sub 'agent.**' >"$W/agent-events.log" 2>&1 & PIDS+=($!)
"$B" sub 'approval.**' >"$W/approval-events.log" 2>&1 & PIDS+=($!); sleep 0.5

step "inputs: a real 1200x800 PNG and a text file in the agent's folder, and a secret outside it"
"$W/bin/genimage" "$AG/photo.png"
eq "$($IC info "$AG/photo.png")" "png 1200 800" "photo.png decodes"
printf 'the launch code is TOKEN-7731, keep TOKEN-7731 private\n' > "$AG/notes.txt"
magick -size 40x40 xc:'#00ffaa' "$W/secret.png"
cat >"$W/agent.json" <<P
{"label": "photo-agent",
 "apps": ["org.aurora.magick", "org.aurora.demo.purge"],
 "roots": ["$AG"]}
P
cat >"$W/readonly.json" <<P
{"label": "viewer", "apps": ["org.aurora.magick"], "effects": ["read"], "roots": ["$AG"]}
P
GW="$W/bin/aurora-gateway --socket $AURORA_BUS_SOCK --approval-wait 30s"

step "a read-only policy sees only the read verb; calling a hidden tool is refused and runs nothing"
RO=$(printf '%s\n' list \
  "call org.aurora.magick.resize_v1 {\"input\":\"$AG/photo.png\",\"output\":\"$AG/ro.png\",\"width\":100}" \
  | "$W/bin/mcpdrive" --name viewer-script -- $GW --policy "$W/readonly.json" 2>"$W/gw-ro.log")
ROTOOLS=$(sed -n 2p <<<"$RO" | jq -r '[.result.tools[].name]|sort|join(",")')
eq "$ROTOOLS" "aurora_result,org.aurora.magick.identify_v1" "read-only policy: tools/list is identify + aurora_result"
HID=$(sed -n 3p <<<"$RO")
eq "$(jq -r .result.isError <<<"$HID")" true "resize on a read-only policy is an isError result"
has "$(jq -r .result.structuredContent.message <<<"$HID")" "no such tool" "its message does not distinguish 'hidden' from 'absent'"
[ ! -e "$AG/ro.png" ] && ok "no output file was created" || bad "ro.png exists"

step "start the full-policy MCP session: the scripted client does initialize, then reads commands"
coproc DRV { "$W/bin/mcpdrive" --name demo-agent -- $GW --policy "$W/agent.json" 2>"$W/gw.log"; }
DRVP=$DRV_PID
ask() { printf '%s\n' "$1" >&"${DRV[1]}"; IFS= read -r -t 120 -u "${DRV[0]}" LINE || { cat "$W/gw.log"; die "no reply from the gateway to: ${1:0:80}"; }; }
IFS= read -r -t 30 -u "${DRV[0]}" LINE || { cat "$W/gw.log"; die "gateway did not answer initialize"; }
has "$(jq -r .result.serverInfo.name <<<"$LINE")" aurora-gateway "initialize: serverInfo"
eq "$(jq -r .result.protocolVersion <<<"$LINE")" 2025-06-18 "initialize: protocol version negotiated"
sc() { jq -c '.result.structuredContent' <<<"$LINE"; }
field() { jq -r ".result.structuredContent.$1" <<<"$LINE"; }

step "tools/list: generated from manifests, filtered by the policy"
ask list
TOOLS=$(jq -r '[.result.tools[].name]|sort|join(",")' <<<"$LINE")
eq "$TOOLS" "aurora_result,org.aurora.demo.purge.redact_v1,org.aurora.magick.convert_v1,org.aurora.magick.grayscale_v1,org.aurora.magick.identify_v1,org.aurora.magick.resize_v1" "exactly the policy's verbs, plus aurora_result"
[ "$(jq '.result.tools|length' <<<"$LINE")" -lt "$NCAT" ] && ok "the agent sees $(jq '.result.tools|length' <<<"$LINE") tools; the registry serves $NCAT verbs (lumen, ffmpeg, imagemagick are not offered)" || bad "tool list is not smaller than the catalogue"
lacks "$TOOLS" lumen "no lumen tool"
jq '.result.tools[]|select(.name=="org.aurora.magick.resize_v1" or .name=="org.aurora.demo.purge.redact_v1")' <<<"$LINE" | indent
eq "$(jq -r '.result.tools[]|select(.name=="org.aurora.magick.resize_v1")|.inputSchema.required|join(",")' <<<"$LINE")" "input,output" "resize schema: input and output required, width/height optional"
eq "$(jq -r '.result.tools[]|select(.name=="org.aurora.magick.resize_v1")|.inputSchema.properties.width.type' <<<"$LINE")" integer "manifest 'int?' became JSON Schema integer"
eq "$(jq -r '.result.tools[]|select(.name=="org.aurora.demo.purge.redact_v1")|._meta["aurora/effect"]' <<<"$LINE")" destructive "effect class carried in _meta"
eq "$(jq -r '.result.tools[]|select(.name=="org.aurora.magick.identify_v1")|.annotations.readOnlyHint' <<<"$LINE")" true "read verb annotated readOnlyHint"

step "READ: identify through registry.invoke@v1 (no prompt)"
ask "call org.aurora.magick.identify_v1 {\"input\":\"$AG/photo.png\"}"
sc | indent
eq "$(field status)" ok "status ok"
eq "$(field result.width)" 1200 "width 1200 (imgcheck above agrees)"
eq "$(field isolated)" true "the controller ran it isolated"

step "WRITE: resize through the controller; the file is real and carries provenance"
ask "call org.aurora.magick.resize_v1 {\"input\":\"$AG/photo.png\",\"output\":\"$AG/small.png\",\"width\":300}"
sc | jq -c 'del(.provenance)' | indent
eq "$(field status)" ok "status ok"
eq "$($IC info "$AG/small.png")" "png 300 200" "small.png exists and decodes as 300x200 (Go decoder, not magick)"
P=$($C provenance "$AG/small.png"); echo "   provenance xattr on the file the agent made:"; jq -c '{app,verb,inputs,isolated,domain,domain_attested,grants}' <<<"$P" | indent
eq "$(jq -r '.app+" "+.verb' <<<"$P")" "org.aurora.magick resize@v1" "provenance names app and verb"
eq "$(jq -r .isolated <<<"$P")" true "provenance: isolated"
eq "$(jq -r .domain_attested <<<"$P")" true "provenance: domain attested by the bus"
$C provenance "$AG/small.png" --verify >/dev/null 2>&1; eq "$?" 0 "controller provenance --verify exits 0 (capability handles check out)"
echo "   note: this record does NOT name the agent; the controller has no caller attribution. The agent is in the audit events below."

step "refused by policy, before the controller: an input outside the agent's folder, and terminal escapes in the tool name"
ask "call org.aurora.magick.identify_v1 {\"input\":\"$W/secret.png\"}"
eq "$(field status)" refused "outside-root input: refused"
has "$(field message)" "outside every directory" "reason given"
ESCNAME=$'org.aurora.\033[2J\033]0;pwned\007x'
ask "$(jq -rn --arg n "$ESCNAME" '"call " + $n + " {}"')"
MSG=$(jq -r '.result.structuredContent.message' <<<"$LINE")
if printf '%s' "$MSG" | LC_ALL=C grep -q $'[\033\007]'; then bad "a terminal escape survived into the tool result: $(printf %q "$MSG")"; else ok "the escape bytes are gone from the result text: $MSG"; fi

step "DESTRUCTIVE: redact@v1 stops at an approval; the script plays the user and DENIES"
OUT=$AG/notes.red.txt
ask "call org.aurora.demo.purge.redact_v1 {\"input\":\"$AG/notes.txt\",\"output\":\"$OUT\",\"word\":\"TOKEN\"}"
sc | indent
eq "$(field status)" awaiting_approval "the call returns awaiting_approval, not a result"
RID=$(field approval.request_id); CID=$(field call_id)
[ -n "$RID" ] && [ "$RID" != null ] && ok "request id from the controller: $RID" || die "no request id"
[ ! -e "$OUT" ] && ok "the output file does not exist while the approval is pending" || bad "$OUT exists before any decision"
has "$(field message)" "cannot approve" "the message tells the agent it cannot approve"
sleep 0.3; has "$(cat "$W/approval-events.log")" "$RID" "the controller published approval.requested for that id"
has "$(grep -m1 'approval.requested' "$W/approval-events.log")" "TOKEN" "the prompt payload carries the real verb args"
echo "   the user denies (approval.decide@v1 from the session uid; scripted stand-in):"
"$B" send registry approval.decide@v1 "{\"request_id\":\"$RID\",\"decision\":\"deny\"}" 2>&1 | indent
FINAL=; for _ in $(seq 40); do ask "call aurora_result {\"call_id\":\"$CID\"}"; [ "$(field status)" != awaiting_approval ] && break; sleep 0.25; done
sc | indent
eq "$(field status)" denied "aurora_result: denied"
eq "$(field code)" approval_denied "controller error code approval_denied"
[ ! -e "$OUT" ] && ok "the output file STILL does not exist after the denial" || bad "$OUT exists after a denial"
$C provenance "$OUT" >/dev/null 2>&1 && bad "a provenance record exists for the denied output" || ok "no provenance record for it either"
eq "$(cat "$AG/notes.txt")" 'the launch code is TOKEN-7731, keep TOKEN-7731 private' "the input is untouched"

step "CONTROL: the same call, the user ALLOWS; this shows the approval, not the app, is what stopped the first"
ask "call org.aurora.demo.purge.redact_v1 {\"input\":\"$AG/notes.txt\",\"output\":\"$OUT\",\"word\":\"TOKEN\"}"
eq "$(field status)" awaiting_approval "again awaiting_approval"
RID2=$(field approval.request_id); CID2=$(field call_id)
[ "$RID2" != "$RID" ] && ok "a fresh request id (approvals are not remembered)" || bad "request id reused"
[ ! -e "$OUT" ] && ok "no file yet" || bad "file exists before the decision"
"$B" send registry approval.decide@v1 "{\"request_id\":\"$RID2\",\"decision\":\"allow\"}" >/dev/null 2>&1
for _ in $(seq 60); do ask "call aurora_result {\"call_id\":\"$CID2\"}"; [ "$(field status)" != awaiting_approval ] && break; sleep 0.25; done
eq "$(field status)" ok "after allow: ok"
eq "$(cat "$OUT" 2>/dev/null)" 'the launch code is [REDACTED]-7731, keep [REDACTED]-7731 private' "the file now exists with the redaction applied"
P2=$($C provenance "$OUT"); jq -c '{app,verb,isolated,approval}' <<<"$P2" | indent
eq "$(jq -r '.approval.route+" "+.approval.decision' <<<"$P2")" "interactive allow" "provenance records the interactive approval"
eq "$(field warning)" null "no gateway warning: it ran with an approval on record"

step "audit: every call is a bus event; what aurora activity shows"
sleep 0.5
EV=$(cat "$W/agent-events.log")
echo "$EV" | head -c 2500 | indent
NCALL=$(grep -c 'agent.tool.call' "$W/agent-events.log"); NRES=$(grep -c 'agent.tool.result' "$W/agent-events.log")
eq "$NCALL" 7 "agent.tool.call events: viewer's refused resize, identify, resize, outside-root refusal, escaped-name refusal, 2x redact"
eq "$NRES" 4 "agent.tool.result events: identify, resize, redact denied, redact allowed (refusals have only the call event)"
has "$EV" '"claimed_by_client":"demo-agent"' "events name the agent as the client CLAIMED it"
has "$EV" '"authenticated":false' "and say it is not authenticated"
has "$EV" "\"input\":\"$AG/notes.txt\"" "events record the inputs"
has "$EV" '"status":"denied"' "the denial is recorded"
ACT=$("$A" activity 2>&1); echo "$ACT" > "$W/activity.txt"
echo "--- aurora activity, the agent-related parts:"
grep -E 'event agent\.tool|ASKED by|REPLY|DENIED' "$W/activity.txt" | cut -c1-330 | head -24 | indent
has "$ACT" "agent.tool.call" "aurora activity lists the agent.tool.call events"
has "$ACT" "aurora-gateway" "with the sender busd stamped (the gateway process), shown as the sender"
has "$ACT" "approval_denied" "the denied invoke appears in the activity timeline"
[ "$(LC_ALL=C grep -c $'\033' "$W/activity.txt")" = 0 ] && ok "no raw ESC byte in the activity output" || bad "ESC byte in aurora activity output"

summary
[ "$FAIL" -eq 0 ]
