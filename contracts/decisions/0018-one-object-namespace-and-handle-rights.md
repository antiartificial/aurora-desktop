# 0018. One object namespace, and rights on the handle

Status: proposed. Supersedes the resource naming in `0001` and the ad-hoc names
in `0014` (devices) and `0015` (hosts); those records keep their numbers and
their reasoning, only the spelling of a resource changes.

## What was there

`bus/server/caps.go` minted a handle for a `resource`, a string checked against
`^[A-Za-z0-9_.:/-]{1,128}$` and **never parsed by anything**. Five families grew
up by convention:

| family | built by | names |
| --- | --- | --- |
| `file:<path>:<mode>` | `prov.Resource` | a path, plus its mode |
| `filehash:<sha256>:<mode>` | `prov.Resource`, when the path would not fit the charset | the same, hashed |
| `device:<class>` | `prov.Grant.Resource` | a device class (0014) |
| `net:<host>` | Customs (0015) | a network peer |
| `controller:epoch` | `capbus` | the daemon's lifetime marker |

Two things were wrong with this, and the second is the one that mattered.

**There was no grammar.** A misspelled class minted successfully, because
there was nothing for it to be wrong against. `device:gpu`, `device:GPU` and
`devise:gpu` were three unrelated objects and the daemon was content with all
three.

**Authority was inside identity.** `prov.Resource` returned `file:/x:ro` and
`file:/x:rw`. As strings those are unrelated, so the system could not say that
one is the weaker form of the other, could not derive the weak one from the
strong one, and could not answer "what does anyone hold over `/x`" at all. Any
design for rights reduction or capability routing has to start by undoing this,
because reducing a handle's rights meant naming a different object.

## What was decided

`contracts/object` owns one grammar. A wire form is `class:name` with exactly
one colon, and the name is hashed as `class:#<sha256>` when it cannot be spelled
in the charset.

Six classes, a closed set: `file`, `device`, `host`, `service`, `window`,
`session`. Adding one is a decision, recorded, not a string a caller invents.
`net:` becomes `host:` because what Customs decides on is a host, not a subnet
(0015 rejected IP filtering for that reason, so the old name described something
the system had already decided not to do). `filehash:` disappears as a family:
hashing is now how any class spells an awkward name, not a namespace of its own.
`controller:epoch` becomes `session:epoch`, alongside `session:clipboard` and
`session:seat` — the epoch was never the controller's, it was the session's.

**Rights move onto the handle**, as a set: `read`, `write`, `use`, `enumerate`,
`destroy`, `grant`. Identity answers *which object*, rights answer *what may be
done to it*, and the same file under two authorities is now one object.

Three of those six deserve their reasoning written down.

- **`use` is not `read`.** A domain granted `device:gpu` may open the render
  node; it may not read the object's contents, because a device class has no
  contents. `use` is the honest description of what a device grant is, and
  saying `read|write` for it would have overstated it in every log line.
- **`destroy` is not implied by `write`.** An app allowed to rewrite a file is
  not therefore allowed to unlink it. The verb effect classes already draw this
  line (`write` versus `destructive`, 0009); the rights were the half that could
  not express it.
- **`grant` is the one that makes routing possible.** Without it a handle is a
  leaf: the holder may act, and may pass nothing on. `Rights.Reduce` refuses for
  a handle that lacks it, so a leaf is enforced rather than hoped for.

`Rights.Reduce` is the single invariant: **a derived handle is never stronger
than its source, and only a holder of `grant` derives at all.** It is exhaustively
tested over every subset of the six rights.

## Rights are not effect classes

Deliberately a separate axis, for the same reason 0013 kept intent separate from
authority. An *effect* (`read | write | destructive | external`) is what a verb
says it intends and drives approval. *Rights* are what a handle permits and
drive enforcement. A verb with effect `destructive` holding a handle without
`destroy` is an attempt to be refused — and that refusal only means something
because the two were decided by different parties at different times. Collapsing
them would make the second check a restatement of the first.

## What this cost busd

The resource charset gains `@` and `#`. Two characters, because a verb object is
`service:files/open@v1` and a hashed name is `file:#<sha256>`; without them
those objects cannot be minted, so a capability over them cannot exist. The
daemon still does not parse the name — it bounds it, so a name stays loggable,
greppable and of fixed maximum width. `TestResourceCharsetAdmitsTheObjectNamespace`
mints each form against a real daemon, so the coupling fails loudly in busd's
own suite rather than quietly at runtime.

## What was considered and rejected

**Keeping rights in the name and adding a `derived_from` field.** This is the
smaller diff: no new charset, no migration. Rejected because the question "what
is held over `/x`" stays unanswerable — you would have to enumerate the cross
product of path and mode spellings to ask it, and every future right multiplies
that set again.

**A free-form string with a documented convention.** That is precisely what was
there. It failed in the ordinary way: nothing checked it, so the five families
drifted and one of them (`filehash:`) existed only because another one could not
represent its own inputs.

**seL4-style index capabilities** — userspace holds a CNode slot number, never a
bit pattern, so a capability is unforgeable by construction. Not adopted: the
property it buys is already held by other means here. Handles are
unguessable tokens kept in the daemon's table, and busd stamps the acting
identity from `SO_PEERCRED` rather than believing a client's claim, so a client
cannot name a handle it was not given. An index space would add a per-client
table and buy no authority we do not have.

**seL4's untyped-memory retype**, where all authority including memory is a
capability, is a kernel design. Aurora is hosted on Linux and the kernel's
memory is not ours to hand out. Nothing to take.

**seL4's derivation tree is worth taking.** Flat revocation is wrong the moment
routing exists: if A holds `file:/x [read,write,grant]` and passes
`file:/x [read]` to B, revoking A's handle must kill B's. This record first said
the gap was open and that a routed handle outlives its parent. **It is now
closed**: `bus.derive@v1` records a `parent` id and `bus.revoke@v1` cascades
transitively, tested over a three-deep chain and over revoking a middle handle,
which spares its parent and its siblings. A derived handle also inherits its
parent's expiry, so routing a handle through yourself cannot extend a TTL.

## Hubris leases

Hubris does not pass memory capabilities between tasks. A caller passes a
*lease*: a borrow of a region, read or write, valid only for the duration of one
IPC, enforced by the kernel, which the callee cannot retain past the reply.

Aurora already has most of this by accident of design. A grant's handle carries
a TTL (`capGrace` past the invoke timeout) and is revoked when the invoke
returns (`capSet.release`). What is missing is the part Hubris is actually strict
about: **non-retainability**. A grant becomes a bind-mount in the domain's
namespace for the life of the domain, so a run that holds `/x` for one verb can
keep reading `/x` for as long as the process lives, whatever the handle says. The
handle expires; the mount does not.

This is a lifetime mismatch, not a naming problem, and closing it means
call-scoped access to the object rather than run-scoped — a passed fd, or a
mount that is unmounted at reply. It is a separate decision and will get its own
record. It is noted here because `use` and the rights set are what make it
*expressible*: a lease is a handle whose rights are dropped at reply, and before
this record there was no "rights" for a lease to drop.

## What follows

1. Migrate every mint through `contracts/object`. `prov.Resource` and
   `prov.Grant.Resource` go away; a `Cap` is carried instead of a string.
   Provenance records written before the migration name objects in the old
   spelling, so the verifier must read both — the old spellings are
   unambiguous and parse one way only, which is why the new form forbids a
   second colon.
2. A Rust mirror of the grammar in the compositor, with the same
   round-trip test over the same table, so the two cannot drift.
3. Rights enforcement at use sites. Minting a handle with `use` changes nothing
   until something refuses to act without it; until then this record buys
   naming and nothing more.
4. Capability routing in manifests, then leases (`0019`). The derivation tree
   that both of those need is done.
5. `capbus.Mint` sends no `rights`, so every controller-minted handle currently
   records `none`. That is harmless while nothing enforces rights at use sites,
   and becomes a fail-closed outage the moment something does. Pass the rights
   with the mint (`use` for a device grant, `read` or `read,write` for a file)
   in the same change that adds the first enforcement, not before and not after.
6. Only the minting domain may revoke. A root's minter can revoke the root and
   everything beneath it, but cannot directly revoke a child some other domain
   derived. That is the existing `cap_not_minter` rule applied unchanged, and it
   is the right default; noted because it is the kind of thing a reader assumes
   the other way.
