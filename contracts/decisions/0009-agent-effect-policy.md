# 0009. Agent gateway and agent effect policy

- **Status:** accepted
- **Date:** 2026-11-06

## Context

Phase 10 wants agents to "use the same verbs as everything else, inside their
own domains, and every risky effect goes through the user". The gateway built
for that slice generates one MCP tool per manifest verb and executes calls
through `registry.invoke@v1`, so isolation, grants, capability handles,
approval and provenance all come from the controller rather than a second
execution path. Nothing new was needed for any of that.

One thing did need deciding. Decision 0007 says, in terms:

> A destructive verb whose grants are all `ambient` needs none: the declared
> capability was consented to at install, and the record says
> `route:"ambient", required:false`.

`needsApproval()` implements exactly that: an `external` or `destructive` verb
prompts only when some grant is not ambient. That is correct for 0007's own
threat model, where a person drives an app and the install-time declaration is
their consent.

It does not carry over to an agent. The user consented to the app holding that
path; they did not consent to an agent deciding what to do inside it. The
install-time grant answers "may this app touch this folder", and an agent
raises a different question, "who chose to run this destructive verb just
now". A `destructive` call with all-ambient grants would therefore run with no
prompt at all, on an agent's initiative, and the user would learn of it only
from the audit trail afterwards.

## Decision

The gateway is stricter than 0007 for agent-initiated calls, and 0007 is
unchanged for everything else.

- A tool whose verb is `destructive` or `external` always reaches a user
  approval. The gateway refuses a gated call whose paths are entirely covered
  by the verb's declared `path:` capabilities, rather than letting the
  controller's ambient route run it unprompted.
- The gateway has no way to decide an approval. It never sends
  `approval.decide@v1`, and the tool description says the tool cannot approve
  itself.
- A gated call returns `awaiting_approval` with the controller's request id;
  the agent polls `aurora_result`.
- If a gated call nevertheless completes with no interactive approval on
  record, the result carries a `warning` and the audit event records it. By
  then the effect has happened; this is detection, not prevention.
- The tool list comes from an operator-written policy file (`apps`, `effects`,
  `roots`) with no allow-all. It is a per-session allow-list, not a derivation
  from the agent's capabilities, because per-session agent domains with their
  own handles do not exist yet.
- Audit: `agent.tool.call` is published before the call and the call is
  abandoned if that publish fails; `agent.tool.result` follows. The
  bus-stamped sender names the gateway process, so the agent's own name is
  carried as `claimed_by_client` with `authenticated:false`.

Enforcement boundary, stated plainly: the gateway enforces this for a client
that speaks only MCP to it. Against any other same-uid process it is
advisory. The gateway is not a boundary against a same-uid attacker.

## Open questions

- Should 0007's ambient exemption be narrowed for *all* callers rather than
  only for agents? The argument that a declaration authorises the path but not
  the moment applies to a mis-clicked app too. Phase 6 (generations) or a
  revision of 0007.
- An agent identity is a claim. Authenticating one needs a per-session agent
  domain so busd can stamp it, which Phase 10's "agent domains" bullet wants
  and this slice did not build.
- Approval-to-call matching is by app, verb and arguments only, so two
  simultaneous identical gated calls are indistinguishable, and a same-uid
  process can forge `approval.requested` to make a call report
  `awaiting_approval` early. Needs the controller to attribute a request to a
  caller.
- The pre-check sees only the verb's own `path:` references from
  `registry.describe`, not the app's whole `capabilities.paths`. A path
  declared at app level but not by the verb is caught only by the post-hoc
  warning.

## Consequences

- An agent can read and write freely within its policy roots, and cannot
  destroy or send anything without a person answering a prompt. That is the
  property Phase 10 asked for, and it holds without agent domains existing.
- Two policies now govern the same verb: 0007's for a person, this one for an
  agent. A reader of `needsApproval()` alone will not see the stricter rule,
  because it lives in the gateway. That split is the cost of not amending
  0007.
- An agent's writes are NOT staged. Phase 10 promises a copy-on-write snapshot
  with a reviewable diff; this slice writes live into the real directory, and
  the gateway's README says so. Until that exists, "writes are allowed freely"
  is a larger grant than the plan intends.
- Taint labels (0010 or later) are unaffected: nothing here propagates labels,
  and the union rule the plan describes is still unbuilt.
