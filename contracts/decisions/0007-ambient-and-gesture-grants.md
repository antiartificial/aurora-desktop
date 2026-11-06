# 0007. Ambient and gesture grants

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Decision 0002 makes the manifest's requested capabilities the thing the user consents to, and says wrapped tools are safe only because their domain is granted exactly what they need. The controller (Phase 5) took `input` and `output` from the `registry.invoke@v1` caller and granted exactly those, never reading `capabilities.paths`. The declaration was decorative.

The plan's Phase 6 powerbox gives a second, legitimate source of access: "picking a file or folder grants exactly that, by bind-mounting it into the app's domain". A rule of "reject anything outside the manifest" would forbid the powerbox itself. So the controller must tell the two sources apart.

## Decision

Every filesystem grant for an invoke is classified and the class is recorded.

1. **ambient.** Covered by a declared `capabilities.paths` entry: the grant path is the declared path or below it, and the declared access covers the mode (`read` covers `ro` only; `write` covers `ro` and `rw`). The user consented at install time; 0002's consent rule governs changes to these declarations.
2. **gesture.** Not covered. Legitimate because the user's action for this one invocation is the authority, as with the powerbox picker: it grants that path and nothing more, even if the manifest never named it. Nothing checks that action happened: a bare gesture is only a caller's request.
3. **powerbox.** Not covered by the manifest, but backed by a valid grant ticket (below) that the powerbox picker minted for exactly this path, mode, app and verb. This is a gesture the controller can distinguish from a bare request, to the limited extent the ticket scheme allows.

**Recorded.** The `registry.invoke@v1` reply, the `provenance.created` event and the `user.aurora.provenance` xattr carry `grants: [{"path","mode":"ro|rw","source":"ambient|gesture|powerbox"}]`. For `external` and `destructive` verbs the reply and the record also carry `approval:{required, route, decision, request_id, by, at}` (see Approval, below).

**Enforced today.**
- A `read` verb takes no output and so never receives an `rw` grant.
- A verb's `capabilities` refs must name declared capabilities; otherwise the manifest validator rejects it and the controller skips the app with the reason.
- `controllerd --no-gesture-grants` (default off) refuses any invoke that needs a gesture grant (`gesture_grant_refused`), before touching a domain.
- Approval is enforced (see "Approval is enforced", below). `needs_approval` is now true when approval was required and given.

- `registry.invoke@v1` takes optional `ticket` (authorises the input grant) and `output_ticket` (the output grant). A valid ticket reclassifies that grant from gesture (or ambient) to `powerbox`. A ticket that is expired, reused, tampered, minted under another key, or for a different app, verb, mode or path is refused with its own error (`ticket_expired`, `ticket_reused`, `ticket_bad_signature`, `ticket_malformed`, `ticket_wrong_app|verb|mode|path`); it is never downgraded to a gesture. A refused request does not spend the ticket.
- `controllerd --require-powerbox` (default off) refuses any grant that is neither `ambient` nor `powerbox` (`powerbox_required`). This is the setting that makes the distinction bite.
- Approval applies to powerbox grants too (amended: see below).

**The ticket (powerbox v0).** `controller/ticket`; picker in `shell/cmd/powerbox`. A ticket is `base64url(claims) "." base64url(HMAC-SHA256(key, claims))` with claims `{v, path, mode, app, verb, nonce, exp}`. The picker resolves the path (symlinks) before signing; the controller compares against its own resolved path. Default lifetime 60s. The controller keeps spent nonces in memory and redeems all of an invoke's tickets atomically, after every other check has passed. The key is 32 random bytes in `$XDG_RUNTIME_DIR/aurora/powerbox.key` (or `$AURORA_POWERBOX_KEY`), mode 0600, owner-checked on load, created by whichever of controllerd or powerbox starts first. The launcher carries tickets as opaque strings.

What this guarantees: a ticket cannot be altered, extended or retargeted without the key; it names one path, mode, app and verb; it is used once and dies after its lifetime; a bus client that does not hold the key cannot obtain `source:"powerbox"` for a path the user did not pick, so with `--require-powerbox` such a client gets only the manifest's declared paths.

What it does NOT guarantee, plainly:
- **It does not prove a human chose anything.** It proves a process holding the key signed it. Any process of the same user that can read the key file can mint a ticket for any path. 0600 only keeps out other users; it does not separate processes of one user. An app confined in a domain cannot read the file (nothing grants it), so the scheme does constrain confined apps. It does not constrain an unconfined same-user process.
- **The picker is not a trusted path.** It is an ordinary process on `/dev/tty`. A same-user process can run it under its own pty and answer for the user (done by hand with a python pty script while building this; not part of the test suite). `powerbox --path`, a test affordance gated only by an environment variable, mints with no gesture at all; any same-user process can set the variable.
- A ticket is a bearer token for its lifetime: whoever holds the string can spend it once, for that path, mode, app and verb.
- It binds a path, not content: the file can change between pick and invoke. A symlink swap changes the resolved path and is refused.
- The nonce ledger is in memory: a controllerd restart forgets it, so a ticket spent before a restart can be spent again if still unexpired.
- The folder kind exists in the picker but the controller grants only single regular files, so no folder ticket is consumed yet.

**Approval is enforced (amended).** An `external` or `destructive` verb whose invoke needs any grant that is not `ambient` (so gesture or powerbox) no longer runs on a flag. The controller does not run it. It publishes `approval.requested` carrying a request id, the app, its domain identity, the verb, the effect class, every grant (path, mode, source) and the arguments, and waits `--approval-timeout` (default 60s). No decision is a deny (`approval_timeout`); a deny is `approval_denied`. Either leaves no output file, no spent ticket and no provenance. A destructive verb whose grants are all `ambient` needs none: the declared capability was consented to at install, and the record says `route:"ambient", required:false`.

A decision is `approval.decide@v1 {request_id, decision}` to the `registry` service. The controller accepts it only when busd's SO_PEERCRED-derived `from` is `uid<its own uid>:<process>`. A `domain:` identity is refused `not_permitted`, so an app cannot approve its own request (tested over a real busd with the app's registered domain socket). The reply and provenance say how it was approved: `interactive`, `auto-approve` (`controllerd --auto-approve`, a test affordance that defeats the point, logged on every use) or `ambient`. With no approval channel configured the verb is refused (`approval_unavailable`), never run.

**This is NOT the trusted approval Phase 10 specifies.** The prompt is a terminal prompt drawn by `aurora` on `/dev/tty`. What that does not give:
- Any process that can write to that terminal can draw a convincing fake prompt, or answer for the user.
- Any unconfined process of the same uid is `uid<N>:<comm>` to the bus, passes the decider check, and can call `approval.decide@v1` or publish a spoofed `approval.requested`. The check separates confined apps from the user's session, not one session process from another.
- The decision is bound to a random request id, not to what the user was shown or to their input. Nothing proves a human pressed a key.
- `registry.invoke@v1` itself has no caller attribution, so a request is not tied to who asked.

Still needs the compositor: drawing the approval from the real verb and arguments on a surface no client can write to, and attributing the answer to the user's input events rather than to a socket peer.

**Not decided.** Whether a powerbox ticket should count as approval for non-destructive-but-external verbs, and whether approvals can be remembered (none are; each invoke asks).

## Open questions

- **Trusted approval UI.** Mechanism done (blocking, timeout, decider check, record); the compositor-drawn, user-attributed prompt is Phase 10.
- **Powerbox trusted path.** Gesture grants must come from a picker the user actually operated. What exists (the third source above) is the mechanism: tickets, binding, single use, `--require-powerbox`. What still waits for Phase 6 and the compositor: the picker drawn by the compositor on its trusted UI path, with input the compositor attributes to the user, and the signing capability held by the compositor rather than a key file any same-user process can read. Until then the property "this grant came from the user" is NOT achieved; only "this grant came from something holding the key".
- **Shared coverage rule.** `contracts/manifest` keeps its path-coverage rule unexported, so the controller has its own in `controller/internal/invoke/grants.go`. It should move into the manifest package so the consent diff and the grant check cannot drift. Phase 6.
- **Gesture grant in the provenance chain.** Whether a later reader should treat output from a gesture-granted run differently. Phase 6.

## Consequences

- Provenance now says how access was obtained, so a reader can see when an app ran outside its consented paths.
- **Limitation:** the launcher's plain `aurora open <file>` still passes whatever path was typed, so it is still a gesture; only `aurora open --pick` produces `powerbox` grants. `--no-gesture-grants` and `--require-powerbox` break the plain form. The distinction is recorded and testable.
- With `--require-powerbox` off (the default) a same-user bus client can still ask for any path and have it classed as a gesture. With it on, such a client is refused unless it holds the key or can drive the picker, both of which a same-user process can do today (see limits above). The setting raises the cost of the attack; it does not close it.
- Output directories are granted whole, so an app can write other files there; the grant is as wide as the directory, and the classification is of the directory.
