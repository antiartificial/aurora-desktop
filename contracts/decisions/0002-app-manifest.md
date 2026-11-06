# 0002. App manifest

- **Status:** proposed
- **Date:** 2026-11-06

## Context

One manifest file drives permissions, the launcher, agent tools, settings UI, widgets and updates. It is contract 2 in the plan. Phase 5 writes manifest schema v0 and its validator. Phase 6 enforces the consent rule.

## Decision

**Format.** TOML on disk. Validated against a schema published in `contracts/`. Compiled to the bus IDL at install time.

**Sections.**

1. App identity and image hash
2. Pinned runtimes
3. Requested capabilities
4. Types opened and created
5. Verbs, each with input and output schemas and an effect class
6. Emitted events
7. Typed settings schema
8. Replicants
9. Config migrations

**Effect classes.** Each verb declares one of: read, write, destructive, external.

**Versioning.** Manifests carry versions and migrations. Verb major versions are part of the verb name (see 0001).

**Consent.** An update that changes requested capabilities or effect classes requires fresh consent.

**Enforcement limit.** For a native app, the manifest is a contract. For a wrapped CLI tool, it is not verifiable by the registry alone. The real effect class of ffmpeg is "write wherever the arguments point", and the declaration cannot show otherwise. The declaration becomes enforceable because Phase 5 runs wrapped tools in domains granted exactly their declared inputs and outputs. A tool that reaches past its manifest fails. This record does not claim the manifest enforces anything for wrapped tools on its own.

## Open questions

1. **Launcher argument collection.** How the launcher collects many typed parameters without becoming a form. Phase 5.
2. **Recipes.** Whether chains of verbs are saved as reusable recipes in Phase 5 or later. Phase 5.
3. **Field-level schema.** The plan names the sections but not their fields. Manifest schema v0 defines them. Phase 5.
4. **Shape of replicants and settings.** Beyond their names, neither section is specified. Replicants v0 is Phase 8. Typed settings stores are in the Later table.
5. **Diff rule for consent.** The rule says a change to capabilities or effect classes needs fresh consent. The plan does not say how a change is detected. Phase 6.
6. **Image format.** Whether app images are OCI layers or a single mountable image. This decides what the manifest's image hash covers. Phase 6.
7. **Flatpak mapping.** Which Flatpak permissions have no adapter equivalent, and what happens to the apps that need them. Phase 7.

## Consequences

- The bus IDL gets its verb definitions from manifests at install time, so manifest errors surface at install.
- Agent tools (Phase 10) are generated from manifest verbs and effect classes. Effect classes are what the agent policy keys on.
- An update that broadens capabilities or effects prompts the user again. Updates are a consent event.
- Wrapped tools are safe only through the Phase 5 domain grants. The manifest alone does not make them safe.
