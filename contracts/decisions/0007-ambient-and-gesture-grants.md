# 0007. Ambient and gesture grants

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Decision 0002 makes the manifest's requested capabilities the thing the user consents to, and says wrapped tools are safe only because their domain is granted exactly what they need. The controller (Phase 5) took `input` and `output` from the `registry.invoke@v1` caller and granted exactly those, never reading `capabilities.paths`. The declaration was decorative.

The plan's Phase 6 powerbox gives a second, legitimate source of access: "picking a file or folder grants exactly that, by bind-mounting it into the app's domain". A rule of "reject anything outside the manifest" would forbid the powerbox itself. So the controller must tell the two sources apart.

## Decision

Every filesystem grant for an invoke is classified and the class is recorded.

1. **ambient.** Covered by a declared `capabilities.paths` entry: the grant path is the declared path or below it, and the declared access covers the mode (`read` covers `ro` only; `write` covers `ro` and `rw`). The user consented at install time; 0002's consent rule governs changes to these declarations.
2. **gesture.** Not covered. Legitimate because the user's action for this one invocation is the authority, as with the powerbox picker: it grants that path and nothing more, even if the manifest never named it.

**Recorded.** The `registry.invoke@v1` reply, the `provenance.created` event and the `user.aurora.provenance` xattr carry `grants: [{"path","mode":"ro|rw","source":"ambient|gesture"}]`. The reply also carries `needs_approval:true` when an `external` or `destructive` verb ran on a gesture grant.

**Enforced today.**
- A `read` verb takes no output and so never receives an `rw` grant.
- A verb's `capabilities` refs must name declared capabilities; otherwise the manifest validator rejects it and the controller skips the app with the reason.
- `controllerd --no-gesture-grants` (default off) refuses any invoke that needs a gesture grant (`gesture_grant_refused`), before touching a domain.
- `needs_approval` is set and logged loudly for `external`/`destructive` verbs on gesture grants. It is a flag. It does not stop the invoke.

**Not decided.** The shape of the approval prompt and what a recorded approval looks like (Open questions).

## Open questions

- **Approval UI.** What the user sees for `needs_approval`, and whether the invoke is held until they answer. Phase 6.
- **Powerbox.** Gesture grants must come from a picker the user actually operated, not from a caller-supplied path. The registry cannot tell the difference today. Phase 6.
- **Shared coverage rule.** `contracts/manifest` keeps its path-coverage rule unexported, so the controller has its own in `controller/internal/invoke/grants.go`. It should move into the manifest package so the consent diff and the grant check cannot drift. Phase 6.
- **Gesture grant in the provenance chain.** Whether a later reader should treat output from a gesture-granted run differently. Phase 6.

## Consequences

- Provenance now says how access was obtained, so a reader can see when an app ran outside its consented paths.
- **Limitation:** the current launcher passes whatever paths the user typed or picked, so almost every grant is a gesture and enforcement is weak. Turning `--no-gesture-grants` on would break the launcher. The distinction is recorded and testable; it does not yet constrain a launcher the user already trusts.
- A caller that is not the user (any same-user bus client) can ask for any path and have it classed as a gesture. That stays true until the powerbox owns the picking.
- Output directories are granted whole, so an app can write other files there; the grant is as wide as the directory, and the classification is of the directory.
