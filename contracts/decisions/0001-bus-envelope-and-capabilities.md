# 0001. Bus envelope and capabilities

- **Status:** proposed
- **Date:** 2026-11-06

## Context

The bus is the one way apps, the shell, the compositor and agents talk to each other. It is contract 1 in the plan and is built in Phase 3, on the domain and handle primitives that Phase 2 builds. This record covers the envelope, the capability model at the level the bus needs, the log, and the evolution rules.

## Decision

**Envelope.** Every message carries:

- message id
- sender domain
- target
- verb or topic
- QoS class: user-interactive, user-initiated, utility or background
- hybrid logical clock timestamp
- optional capability handles
- optional shared-memory handles

**Sender identity.** The sender domain is the attested domain id from Phase 2, read by the server from socket peer credentials. It is never read from the message.

**Capabilities.** A capability is an unforgeable handle passed inside a message. There are no ambient names. A domain reaches an object only through a handle it was given. This record treats a handle as opaque, unforgeable and transferable, and nothing more. What a handle is on Linux is decided in Phase 2. The model is Cap'n Proto RPC's object capabilities.

**Data off the bus.** Messages carry commands, events and handles. Bulk data moves as memfd or dma-buf handles passed as file descriptors (SCM_RIGHTS), never as payload bytes.

**Log.**

- Append-only and segmented. Replayable.
- Every message is durable unless marked ephemeral.
- Ephemeral traffic (pointer motion, frame timing) skips the log.
- The log stores a handle's identity, never its value.

**Delivery.** The QoS class is in the envelope from Phase 3, before the scheduler uses it. User-interactive traffic is delivered first and never queues behind background traffic.

**Replay.** Replay is checked on invariants: causal order, final state per subscriber, and the set of capability grants. Byte equality is not a check. Byte diffs are a debugging tool and never a CI gate. Hybrid logical clocks and file descriptors prevent a byte-identical result across runs.

**Evolution.**

- Fields are only added. They are never renumbered or retyped.
- Unknown fields are preserved.
- A verb's major version is part of its name.

## Open questions

1. **IDL: Cap'n Proto or a custom IDL.** Phase 3. The trade-offs this record has to settle:
   - Promise pipelining lets a caller chain calls on results that do not exist yet, so intermediate results never materialise. The log wants every message materialised so it can be replayed. The two pull in opposite directions.
   - Three-party capability handoff (Cap'n Proto RPC level 3) is what the later federation item needs. No implementation provides it. Federation may need to be built whichever IDL is chosen.
2. **Topology: one broker, or peer-to-peer with a broker only for discovery and logging.** Phase 3.
3. **Capability representation.** What a handle is on the wire, how it transfers between domains, how it is revoked, and what the log stores in its place. Phase 2 record.

## Consequences

- No service trusts a sender field in a message. Every server reads the sender from peer credentials.
- Phase 3 cannot start until Phase 2 supplies domain identity and handle primitives.
- The log holds handle identities, not values. A replayed session is checked against the grant set, not against handle values.
- Ephemeral messages are never logged, so pointer motion and frame timing cannot be replayed.
- The evolution rules rule out removing or retyping a field. Deprecated fields stay in the schema.
- Hybrid logical clocks in the envelope mean federation does not need a format change later.

## Addendum: handles on the controller's grant path

The controller now mints a handle (`bus.mint@v1`) per grant of an invoke, naming `file:<path>:<mode>`, presents them in `caps` so the daemon's holder check runs, records the ids in the provenance event and xattr, and revokes them when the invoke ends. `controller provenance --verify` checks the ids against the daemon's table. This is handles as unforgeable daemon-issued names for a grant, not "passed in messages" to the app: the app holds nothing, and the controller, which performs the access, is the holder.

It proves that the daemon issued the id to the controller's identity for that resource (independently only while the handle is live; after revocation the daemon cannot list it, so the resource comparison rests on the controller's own ledger) and it is an exact answer to "was this id ever minted by this daemon's current life". It does not prove the access happened, stop a controller lying about what it did, or authorise minting (any identity may mint any resource name, per the MVP minting policy). Handles are in memory: after a daemon restart verification is inconclusive, not negative. Details: `controller/README.md`.
