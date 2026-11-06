# 0006. MVP wire format: newline-delimited JSON

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Decision 0001 specifies the bus envelope but deliberately leaves the IDL open
until Phase 3: Cap'n Proto versus a custom IDL, with two live trade-offs
recorded there (promise pipelining versus an append-only log, and the absence
of RPC level 3 versus the later federation item).

Building a runnable MVP needs a wire format now. Two further facts bear on it:
`capnp` is not installed on the development host, and installing it would
pre-commit the very decision 0001 defers.

The project goal at this point is a prototype to get hands on with, not a
production contract.

## Decision

The bus speaks **newline-delimited JSON**, one envelope object per line, over a
Unix socket. Field names and semantics are exactly those of decision 0001:
`id`, `ts` (hybrid logical clock), `from`, `to`, `kind`, `verb`, `topic`,
`qos`, `corr`, `caps`, `payload`, `ephemeral`.

This is an encoding choice only. It does not answer 0001's open question, and
it is expected to be replaced. Because the envelope's field set and rules come
from 0001 rather than from the encoding, swapping the encoding later should not
change the envelope.

Supporting build decision: each component is its own Go module
(`bus/`, `adapter/linux/`, `contracts/manifest/`) rather than one module at the
repo root. This was chosen so that components could be built in parallel
without contending on a single `go.mod`. A `go.work` ties them together. This
is a build-layout convenience, not an architectural statement, and collapsing
to a single module later is expected to be cheap.

## Open questions

- 0001's IDL question stands, unchanged and unanswered. This record must not be
  read as having settled it.
- Whether the JSON envelope should be frozen as a debugging/interop format once
  a binary IDL arrives, or removed entirely.
- Whether `caps` as opaque strings survives contact with the real capability
  representation, which decision 0005's constraint and the deferred kernel
  adapter record both bear on.

## Consequences

- The bus is buildable and demoable today with only the Go standard library.
- Messages are human-readable, so `bus log` and replay debugging are legible
  without tooling. For a prototype meant for experimentation this is worth
  real money.
- Performance is not a consideration at this stage and has not been measured.
  Any later claim that JSON is or is not fast enough needs a benchmark, per the
  plan's cross-cutting rule on explaining numbers.
- The log holds handle identity, never handle value, exactly as 0001 requires.
  JSON does not weaken that; it makes violating it more visible.
