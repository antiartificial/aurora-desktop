# 0019. Leases, and where a lease cannot exist

Status: proposed. Follows `0018`, which made rights a thing a handle can drop.

## What a lease is

Hubris does not pass memory capabilities between tasks. A caller passes a
*lease*: a borrow of a region, read or write, valid for the duration of one IPC,
enforced by the kernel, which the callee **cannot retain past the reply**. The
callee never holds the memory; the kernel copies on its behalf while the lease is
live, and refuses once it is not.

Aurora looked close to this. A grant's handle carries a TTL and is revoked when
the invoke returns (`capSet.release`). `0018` added rights, so "a handle whose
rights drop at reply" became expressible. The remaining question was whether the
drop can be *enforced*.

## What was measured

Aurora composes a file grant into a domain as a bind mount. So: does ending the
grant end the access? Run under `unshare -rm`, holding an fd across the revoke
the way an app would:

```
plain umount:  umount: .../mnt/f: target is busy.
lazy umount:   ok (detached)
path now:      ''
held fd still: 'secret-v1'
via held fd:   'secret-v2-written-after-revoke'
```

Four findings, in order of how bad they are.

1. A plain `umount` **fails** while the holder has the file open. Revocation has
   to be lazy (`MNT_DETACH`) or it does not happen at all.
2. After the lazy unmount the path is gone. The namespace looks clean.
3. The holder's retained fd still reads the file.
4. **The retained fd reads bytes written after the revoke.** The holder did not
   merely keep a snapshot; it kept a live channel to the object, indefinitely.

Point 4 is the one that settles the design. A Linux file descriptor cannot be
revoked — there is no call that takes one back — so the holder's access is
bounded by nothing, not even by the state of the file at the time of the call.

Points 2 and 3 together are worse than a plain failure to revoke, because the
failure is **invisible from the outside**. An auditor inspecting the domain's
mount table after revocation sees no mount and concludes there is no access.
`bus inspect` shows the handle gone. Both are telling the truth about what they
can see, and both are wrong about the question being asked. A revocation that
looks successful and is not is a worse artifact than one that reports an error.

## What was decided

**A lease is enforceable exactly when access is mediated**, and Aurora will say
which of three kinds each grant is rather than calling all of them leases.

**Tier 1 — mediated, enforceable, to be built.** `service:` and `session:`
objects: a kit call, a bus verb, the clipboard. The app holds a connection, not
the object. Rights are checked per request against the daemon's table; at reply
the lease's rights drop to none; a later request is refused. This is Hubris's
actual shape — the holder never has the thing, and something it does not control
does the work while the lease is live. The clipboard is the first case worth
having: *you may read the clipboard during this call, and not after* is a
sentence the system can currently neither say nor enforce.

**Tier 2 — FUSE-backed files, enforceable, not built.** If the controller serves
the inode, every read is a request to us, so a retained fd returns `EPERM` after
reply and finding 4 goes away. `/dev/fuse` and `libfuse3` are present on this
host, so the mechanism is available rather than hypothetical. Not verified here:
no FUSE server was written, and the claim rests on where the reads go, not on a
measurement. The costs are real and are why this is deferred — a FUSE server in
the controller, per-read latency on every file an app touches, and the controller
becomes a liveness dependency of the app's file I/O, so a controller crash stops
being survivable in the way crash-survival currently proves it is.

**Tier 3 — bind-mounted files, NOT enforceable.** Left as it is, and labelled.
The grant is a mount for the life of the domain. The handle's expiry is
bookkeeping.

## The rule that follows, and it is the point of this record

**A tier 3 grant must never be recorded as a lease.** Provenance has to
distinguish *leased* from *mounted*, and `bus inspect` has to show which a handle
is, because a reader of either one will otherwise draw finding 2's conclusion.
The implementation is cheap; getting it wrong makes the audit log worse than
having no log, since a log that overstates its own authority is read as if it
did not.

This is also why the tiers are named in the contract rather than left as an
implementation detail. An app author asking "can this be taken away from me
mid-run" deserves an answer that is true for the specific grant they hold.

## What was considered and rejected

**Passing an fd instead of bind-mounting.** The obvious move, and it fixes
nothing: an fd is exactly the thing finding 4 is about. It is strictly worse than
the mount, because it removes even the appearance of a revocation point.

**seccomp user notification on `open`.** Intercept the syscall, refuse it after
the lease ends. Rejected for the same reason: it governs *acquisition*, and every
finding above is about retention. An app that opened the file during the lease
keeps it. This would have been easy to adopt and would have produced a system
that passes a test of the wrong property.

**Bounding the lease to call-time state** — accept retention, but argue the
holder only ever sees the file as it was during the call, so a lease still means
something. Finding 4 disproves it outright. This was the fallback position before
the experiment, and it is wrong; it is written down because it is the plausible
thing to believe without running it.

**Doing nothing, on the grounds that a domain is already a sandbox.** The
sandbox bounds *which* objects, which is `0014`'s and `0015`'s job and it does it.
A lease is about *for how long*, and nothing currently answers that question for
a file. The two are not substitutes.

## What follows

1. **Done.** `bus.lease@v1` is a short-lived derived handle that binds to one
   service request, ends at the reply with rights dropped to none, emits
   `bus.cap.ended`, and is refused afterwards. A lease over anything not mediated
   is refused outright rather than issued with a caveat, so "a lease is
   enforceable" is true of every lease that exists.

   **But this record was missing something, found while building it.** Above it
   says rights are "checked per request against the daemon's table". There is no
   way for a callee to ask. The callee receives the lease token without being its
   holder, so it cannot present it, and nothing in busd checks what a lease
   *permits* -- only who may start one and when it ends. A verb along the lines of
   `bus.lease.check@v1` ("does this lease permit `read` on `session:clipboard`
   right now?") is required before the clipboard case can work, and it is the next
   piece of tier 1 rather than a detail of it.
2. The clipboard as the first tier-1 lease, once the compositor's clipboard work
   is settled.
3. **Done, with different vocabulary than this record proposed.** Not
   `leased` versus `mounted`, but an `access` label of `mounted` | `mediated` |
   `unclassified` with a separate `lease` flag. The reason: a long-lived mediated
   handle over `session:clipboard` is enforceable and is *not* a lease, so
   `leased` would have mislabelled it. The label says where the object lives,
   which is what the guarantee actually depends on.
4. Tier 2 stays unbuilt until something needs it badly enough to pay the
   latency and the liveness coupling. Revisit if a file grant to an untrusted app
   ever becomes the common case rather than the exception.
