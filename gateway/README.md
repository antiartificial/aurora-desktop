# gateway: the agent gateway, MVP slice of Phase 10

An MCP server on stdio that offers Aurora verbs as tools and runs a tool call
by sending `registry.invoke@v1` to the controller. It is an adapter. It starts
no process, makes no grant, draws no prompt and writes no provenance: isolation,
grants, capability handles, approval and provenance are the controller's, and a
call through the gateway is the same `registry.invoke@v1` the launcher sends.

    aurora-gateway --policy policy.json [--socket PATH] [--timeout 30s] [--approval-wait 10s]

Demo: `demos/agent-gateway/run.sh` (real busd, controllerd, bwrap domains,
ImageMagick; a scripted MCP client, no LLM). Tests: `go test ./...` here.

## What it does

**Tool generation.** One tool per verb, from `registry.verbs@v1` and
`registry.describe@v1`, the same manifests the registry serves. Name
`<app id>.<verb with @ as _>` (`org.aurora.magick.resize_v1`). The input schema
is JSON Schema built from the manifest's `input` (`int?` becomes an optional
integer), plus `input` (required when the verb declares `accepts`; the MIME
types are in its description) and `output` (required for any non-read verb). The
effect class and accepted types are in `_meta` (`aurora/effect`,
`aurora/accepts`) and in MCP annotation hints. One extra tool, `aurora_result`,
reports on an earlier call by id. The list is rebuilt from the registry on
every `tools/list`.

**Which verbs an agent sees: the rule.** A policy file (operator-written JSON:
`apps`, optional `effects`, `roots`) decides. A verb is a tool iff its app is in
`apps` and its effect class is in `effects` (default: all four). There is no
allow-all: empty `apps` or `roots` is rejected. A call may only name files that
resolve (symlinks followed) under a root. This is a per-session allow-list, not
a derivation from the agent's real capabilities: the plan's "the verbs its
capabilities allow" needs agent domains with their own handles, which do not
exist yet.

**Enforced or advisory.** Enforced by the gateway process, for a client that
speaks only MCP to it: a tool outside the policy is not listed and is not
callable (the refusal is the same text as for a tool that does not exist), and a
path outside the roots is refused before anything reaches the controller. It
is advisory against everything else. The gateway runs as the same uid as every
other Aurora process; the controller accepts `registry.invoke@v1` from any
same-uid process; so anything that bypasses the gateway is not constrained by
the policy file at all. The gateway is not a boundary against a same-uid
attacker, and a prompt-injected agent that can run shell commands is outside it.

**Effect-class policy.**
- `read`: runs, no prompt.
- `write`: runs (inside the roots). It is **not staged in a snapshot**: the plan
  promises "writes inside the agent's snapshot" and a reviewable diff; neither is
  built. A write lands in the real directory, immediately.
- `destructive`, `external`: must reach a user approval. The gateway has no way
  to decide one (its `Controller` interface has no such method, and it never
  calls `approval.decide@v1`). The call returns `awaiting_approval` with the
  controller's request id as soon as the controller raises the request; the
  invoke keeps blocking in the controller; the agent polls `aurora_result`
  and sees `denied`, `approval_timeout` or `ok`. Two guards, both because the
  controller asks only when some grant is not declared by the app's manifest
  (decision 0007), so a declared path or no path at all runs with no prompt:
  1. before the call, the gateway refuses a destructive or external call whose
     paths are all covered by a `path:` capability the verb declares. The test
     ignores read/write access, so it can only err toward refusing.
  2. after the call, a gated call that succeeded without an interactive approval
     on record carries a `warning` and is audited. The effect has already
     happened by then. Weakness: the pre-check sees only the verb's own
     `path:` references from `registry.describe`, not the app's whole
     `capabilities.paths`, so a path declared at app level but not referenced
     by the verb is not caught beforehand (only by the warning).
  An unknown effect class is treated as gated.

**Audit.** Every call publishes `agent.tool.call` before it is made (the call is
not made if that publish fails: fail closed) and `agent.tool.result` after, as
logged bus events. `from` is stamped by busd, so it names the gateway process,
not the agent. In the payload, the agent is `{claimed_by_client, policy_label,
authenticated:false}`: `claimed_by_client` is the `clientInfo.name` the MCP
client sent in `initialize`, and `policy_label` is the operator's text; neither
is authenticated. The event carries the tool, app, verb, effect, resolved
input and output, and arguments. A refused call has a call event with a
`refused` reason and no result event.

What `aurora activity` shows (checked against a real log in the demo): each
invoke the gateway made appears in the invoke timeline as "ASKED by
uid1000:aurora-gateway (stamped by busd)" with its approval, denial and reply.
The `agent.tool.*` events appear under "Not tied to any invoke", as
`uid1000:aurora-gateway event agent.tool.call {...}`, payload truncated at 200
characters, so the claimed agent name is visible but the input path may be cut.
activity does not join an agent event to its invoke (it has no key for it: the
gateway cannot see the controller's request id), so the join is by timestamp and
arguments by eye. The controller's provenance record for an agent-made file does
not name the agent either, because `registry.invoke@v1` has no caller
attribution (decision 0007); the file's record names the controller's view
(app, verb, domain, grants, approval) and the agent is only in the audit events.

**Terminal safety.** Tool names, arguments and paths come from the agent. Every
string the gateway puts in a result is passed through `clean()`, a copy of
`shell/launcher/approvals.go`'s (control characters including ESC, C1, bidi and
zero-width characters become `?`), including the strings inside the controller's
own reply, which are app-supplied. Tested in `gateway_test.go`
(`TestTerminalEscapesDoNotSurvive`, which fails if the cleaning is removed); the demo checks a real
tool name carrying ESC sequences. The gateway draws no approval prompt;
the controller's approval event carries the real verb and arguments and
`aurora`/the compositor render it with their own `clean()`.

## MCP: implemented and not

Implemented: the stdio transport (newline-delimited JSON-RPC 2.0), `initialize`
with protocol-version negotiation (2025-06-18, 2025-03-26, 2024-11-05),
`notifications/initialized` (accepted and ignored: requests are allowed after
`initialize`, not after the notification), `ping`, `tools/list`, `tools/call`
returning `content` (text), `structuredContent` and `isError`. Policy refusals,
approval outcomes and controller errors are `isError` results; a malformed call
or unknown method is a JSON-RPC error. Tested with a scripted client over pipes
(`TestMCPProtocol`) and over a real stdio subprocess in the demo
(`cmd/mcpdrive`).

Not implemented: resources, prompts, sampling, roots, elicitation, logging,
completions, progress, cancellation (`notifications/cancelled` is ignored),
`tools/list` pagination, `listChanged` notifications, `outputSchema`, batches,
the streamable HTTP transport, authorization. Not tested against any real MCP
client or SDK, only against the scripted one written alongside it, so
conformance to the spec beyond what that client exercises is unverified.

## Not built

- Snapshot transactions and a reviewable diff. Writes are live.
- Taint labels. Agent domains: the agent's calls run in the *app's* domain, the
  same one the launcher uses; there is no per-session agent domain with only
  granted handles, and no handle is passed to the agent.
- Query-layer tools (`indexd`); only verbs become tools.
- Any trusted-path guarantee for the approval: that is whatever the controller
  gives (decision 0007's list of what the terminal and compositor routes do not
  give applies unchanged). The demo's "user" is a script calling
  `approval.decide@v1` as the same uid.
- Agent authentication. `claimed_by_client` is text.
- Matching an approval request to a call is by app, verb and arguments only; two
  simultaneous identical gated calls cannot be told apart, and the watcher does
  not check the sender of `approval.requested` beyond ignoring domain senders
  (the bus does not stop a same-uid process publishing that topic), so a forged
  event could make a call report `awaiting_approval` early. The effect is still
  gated by the controller.
- A call's tool list view can be up to 5 seconds stale.
- Path confinement is lexical-plus-symlink at call time; a symlink swapped after
  the check is the controller's problem (it resolves again and grants the
  resolved path), not re-checked here.
