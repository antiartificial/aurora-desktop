# Decision records

One page per decision about a contract. A record says what was decided, why, what is still open, and what follows from it. A second reader should be able to pick up the work from the record alone.

## Numbering

- Four digits, sequential, zero-padded: `0001`, `0002`, and so on.
- File name: `NNNN-short-slug.md`.
- Numbers are never reused. A superseded record keeps its number.
- `0004` records the Vitruvian reuse decision, a Phase 0 record that is not one of the four contracts.
- The kernel adapter record takes the next free number when Phase 2 writes it.

## Statuses

- **proposed.** Written, not yet built against. Open to change.
- **accepted.** The contract is in use. A change needs a new record that supersedes this one. The plan says contracts freeze only after two phases have used them, so a record moves to accepted at that point.
- **superseded.** Replaced by a later record. The record names its successor and stays in the repo.

## When a record is written

Before building each contract, write a one-page record. This is the rule from the plan's "Principles and method".

Phase 0 writes three contract records, all `proposed`: `0001` (bus envelope and capabilities), `0002` (app manifest), `0003` (compositor protocol). The Vitruvian reuse record, `0004`, is also a Phase 0 record and is `proposed`.

### The exception: the kernel adapter

The kernel adapter record is written in Phase 2, not Phase 0. The reason is in the plan. The hardest question for this contract is what a capability is: a passed fd, a bind mount, or a network namespace rule. Phase 2 answers that by building the Linux backend. A record written first, with nothing to constrain it, is the "contracts designed too early" risk.

Phase 2 writes the adapter record against the working Linux backend, status `proposed`. The record describes what was built. The Ginux column stays a sketch until G4. Phase 2 also writes the capability representation record in the same phase.

## Format

Use `TEMPLATE.md`. Each record has at least these sections: Status, Date, Context, Decision, Open questions, Consequences. TEMPLATE.md gives their default order. A record may add sections where its nature calls for them, and may reorder sections to suit its argument. An investigation record that reports findings may add a Findings section and a What would change this section, and may call its Open questions section Open checks. Record 0004 is an example.
