# contracts

The interfaces that are expensive to change later. Each contract gets a decision record before its first line of code. Everything else in the repo is free to churn.

## What lives here

- **IDL.** The schema for every bus message type, generated into Go and Rust. Not written yet. Cap'n Proto or a custom IDL is still open (Phase 3, see `decisions/0001-bus-envelope-and-capabilities.md`).
- **Manifest schema.** The schema that app manifests are validated against. Written in Phase 5 (manifest schema v0 and validator). The manifest is TOML on disk, compiled to the bus IDL at install time (see `decisions/0002-app-manifest.md`).
- **Decision records.** `decisions/`. One page per decision. How they work is in `decisions/README.md`.

## The four contracts

| # | Contract | Record | Status |
| --- | --- | --- | --- |
| 1 | Bus messages and capabilities | `decisions/0001-bus-envelope-and-capabilities.md` | proposed |
| 2 | App manifest | `decisions/0002-app-manifest.md` | proposed |
| 3 | Kernel adapter | Written in Phase 2, against working code | not yet written |
| 4 | Compositor protocol | `decisions/0003-compositor-protocol.md` | proposed |

The kernel adapter has no record yet on purpose. The plan defers it to Phase 2, because what a capability concretely is can only be answered by building it.
