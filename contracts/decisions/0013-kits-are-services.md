# 0013. Kits are bus services with manifests, not linked libraries

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Aurora wants the thing macOS gets from CoreImage, CoreML, AVFoundation and
Vision: a coherent set of system capabilities that any app can reach, without
each app vendoring its own copy of the hard parts. The plan already names two
of these — the Media Kit (phase: PipeWire graph on the bus) and the
Intelligence Kit (on-device transcription and embeddings feeding the semantic
index) — and the Interface Kit already exists, but as something different: a
library compiled into the application process (0008).

So "kit" is currently used for two unlike things, and the next one built will
pick a shape by accident. This record picks it on purpose.

The forcing question is what a kit *is*: a library an app links, as on macOS
and as the Interface Kit is today, or a service an app calls.

A linked framework has real advantages — no IPC per call, no serialisation,
the compiler checks the call. It is also how the Interface Kit must work: a
`BView` drawing call cannot pay a round trip, and Haiku's own design already
splits it (the client buffers `AS_*` opcodes and app_server rasterises).

But for the compute kits — image, media, intelligence — a linked framework
gives up four things Aurora already has and has paid for.

## Decision

**A compute kit is a domain that serves typed verbs on the bus, described by
a manifest, exactly like an app.** It is not a library and apps do not link
it.

Specifically:

- A kit ships `apps/<id>/manifest.toml` with `[[verb]]` entries carrying
  `effect`, `accepts`, `summary`, and `default`/`group` where they apply
  (0002, 0012). It is loaded by the registry like any other app and appears in
  `registry.apps@v1`.
- A kit runs as a domain under the supervisor, with the restart policy, health
  probes and crash reports that already exist.
- Calling a kit is `registry.invoke@v1`, so it goes through the same grant and
  approval path as any other verb. There is no privileged side channel.
- A kit's identity on the bus is attested from kernel facts, like every other
  domain. An app cannot impersonate a kit, and a kit cannot impersonate the
  caller.
- Kit ids live under `org.aurora.kit.*`.

**The Interface Kit is explicitly not this** and stays as 0008 describes: real
Haiku source compiled in place, with an app_server analogue embedded in the
application process. Drawing is latency-bound and must not become IPC per
call. "Kit" therefore means two things in this project, and this record is the
place that says so rather than leaving it to be discovered.

What this buys that a linked framework cannot:

1. **Capability scope.** Calling the Intelligence Kit on a file is a grant,
   minted and recorded. The capability inspector can show "this app is using
   on-device transcription on this file" because the grant exists. A linked
   framework is invisible by construction.
2. **Provenance.** An embedding or a transcript carries which kit, which
   version, from which input — the same record a resize already carries. macOS
   cannot tell you which CoreML version produced a vector.
3. **Language independence.** Anything that speaks the bus gets every kit. No
   per-language SDK, which is the usual way a framework collection rots.
4. **Failure isolation.** A kit that crashes is restarted by the supervisor
   and publishes a crash report. A linked framework takes the calling app down
   with it.

## Open questions

- **The cost of IPC for bulk data.** Sealed-memfd segments exist (0001) and a
  frame or a model input should travel as a segment, not a payload. Unmeasured.
  The first kit built answers it.
- **Streaming verbs.** Every verb today is request/response. Transcription and
  video are streams. The Media Kit phase answers it; nothing here decides it.
- **GPU access from a domain.** A compute kit wants the GPU. Decision 0014
  makes `device:gpu` an enforced grant (the render node is in the namespace
  only under a grant, minted and recorded like a path grant). It does not make
  a compute kit work yet: on this machine the node is present but the domain's
  uid cannot open it, and the grant is far wider than a path grant (little
  isolation between GPU contexts, a kernel driver as attack surface). 0014
  states both. Related to the Wayland-socket problem in 0012.
- **Whether the Interface Kit should expose a bus surface too** for the parts
  that are not latency-bound (font enumeration, clipboard, system colours).
  Undecided.

## Consequences

Easier: a kit is testable the way every other app is — a demo that starts a
real stack and asserts decoded output. Agents get kits for free, because the
MCP gateway already generates tools from manifests, so on-device intelligence
becomes agent-callable with no extra work and with the destructive-call
approval policy of 0009 already applied.

Harder: every kit call costs a round trip and a serialisation. For anything
per-frame or per-glyph this decision is wrong, which is exactly why the
Interface Kit is excluded.

Changed: "write an Aurora app" and "write an Aurora kit" become the same task
with the same scaffold, the same manifest and the same test shape. That is the
point.
