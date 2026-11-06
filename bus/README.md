# bus

The message bus: the spine of Aurora. A broker daemon (`busd`) and a CLI
(`bus`) that let apps, the shell, the compositor and agents talk through typed
requests and published events, with an append-only log that can be replayed.

Specification: [`contracts/decisions/0001-bus-envelope-and-capabilities.md`](../contracts/decisions/0001-bus-envelope-and-capabilities.md).
Plan: "Phase 3: The bus" in [`docs/toy-desktop-os-plan.md`](../docs/toy-desktop-os-plan.md).
This is a working MVP, not the finished phase. See "Implemented vs not built".

## MVP decision: newline-delimited JSON, not Cap'n Proto

The wire format is one JSON object per line over a Unix stream socket.
Decision 0001 leaves the IDL open (open question 1: Cap'n Proto or a custom
IDL, with the promise-pipelining versus replay and three-party-handoff
trade-offs). **That question still governs.** JSON was chosen only so the bus
is runnable and demoable now (capnp is not installed). Nothing here settles
the question, and the envelope's field set is the part meant to survive a
change of encoding. Unknown fields are preserved on a round trip, per 0001's
evolution rules.

## Envelope

```json
{"id":"...","ts":"<hlc>","from":"<domain>","to":"<target>","kind":"request|response|event|error",
 "verb":"name@v1","topic":"a.b.c","qos":"user-interactive|user-initiated|utility|background",
 "corr":"<request id>","payload":{},"caps":["<opaque handle>"],"ephemeral":false,
 "segments":[{"id":"frame","bytes":1048576,"sealed":true,"mime":"image/png","sha256":"sha256:..."}]}
```

- `id`, `ts`, `from` are **always set by the daemon**. A client-supplied `from`
  is overwritten. See "Identity" below for what `from` means on each socket
  (tested in `server/server_test.go` and `server/identity_test.go`).
- `ts` is a hybrid logical clock: `<wall ns, 19 digits>-<counter, 20 digits>`,
  monotonic per daemon, and string order equals time order. The counter is
  a uint64 (20 digits), so it cannot wrap in practice while the wall clock is
  stuck or stepping backwards (an earlier uint32 counter wrapped after 2^32
  stamps and produced a lexically smaller timestamp).
- `verb` must carry its major version (`resize@v1`).
- `qos` defaults to `user-initiated`.
- `caps` are capability handles minted by the daemon (see "Capabilities").
  Recipients get them. **The log stores only `cap_ids`** (`cap:` plus a
  truncated SHA-256), never the handle.
- `ephemeral: true` messages are delivered but never logged.
- `corr` ties a response or error to its request. On the requester's side it
  carries the `id` the requester sent. The log keeps the daemon's own ids.
- `origin` (`{id, from, ts}`) is only honoured by a daemon started with
  `--allow-replay`; otherwise it is stripped.

Patterns:

- **Request/response.** A client registers a service name (`bus.register@v1`).
  Requests with `to` set to that name go to it. It answers with `corr` set.
  Errors are typed (`no_such_service`, `service_gone`, `bad_message`, ...).
- **Pub/sub.** `bus.subscribe@v1` with a glob. `*` matches within one dotted
  segment (`window.*` matches `window.opened`, not `window.a.b`), and a whole
  segment `**` matches any number of segments.
- **Control verbs** go to the reserved target `bus`: `bus.ping@v1`,
  `bus.register@v1`, `bus.subscribe@v1`, `bus.unsubscribe@v1`,
  `bus.inspect@v1`, `bus.log@v1`, `bus.mint@v1`, `bus.transfer@v1`,
  `bus.revoke@v1`, `bus.domain.add@v1`, `bus.domain.remove@v1`. They are not
  logged.

## Identity (`from`)

busd listens on its session socket and, optionally, on one socket per domain.

- **Session socket:** `from` = `uid<uid>:<comm>` (SO_PEERCRED uid, `/proc/<pid>/comm`;
  override with `Config.Resolver`). Weak: same user plus same binary is one
  identity, and a process can rename itself. Unchanged.
- **Domain socket:** `from` = the identity registered for that socket, e.g.
  `domain:alpha:3d7a6e38a013ffaa` (what `domain exec-id` prints). The daemon
  decides it from *which listener accepted the connection*. Nothing the client
  sends is read for it, and a client-supplied `from` is overwritten as before.
  SO_PEERCRED is still read on every connection (uid/pid shown in `bus
  inspect`). Two processes in one domain share one identity; two domains
  never do (tested).
- **Registration.** `busd --domain IDENTITY=SOCKET` (repeatable), the Go API
  `Server.AddDomain/RemoveDomain`, or the control verbs `bus.domain.add@v1` /
  `bus.domain.remove@v1` `{"identity","path"}`, which the controller calls over
  the session socket (a domain connection is refused with `forbidden`, so a
  domain cannot create domains). Chosen over watching the adapter's socket
  directory because it is explicit, needs no inotify or scan races, and the
  daemon never has to guess that a directory entry is a domain; the cost is
  that the controller must call it when it creates or destroys a domain.
  Identities must match `domain:<name>:<hex>`, which a session identity can
  never match, and each identity and path can be registered once. Removing a
  domain closes its socket, disconnects its clients and drops handles it holds.
- **What this does NOT guarantee.** All domains run as the same host uid, so the
  socket path IS the identity. Any same-uid host process that can open a
  domain's socket (it is mode 0600 in a 0700 directory, both owned by that
  uid) is that domain to the bus, and so is any other domain given that
  directory. `demo-identity.sh` step 8 shows it happening. SO_PEERCRED cannot
  help: it says the same uid for everyone. Fixes: per-domain uids (needs
  root/subuid), or the kernel adapter attesting the connecting process's
  domain (cgroup or native domain id) so the socket stops being the secret.
  The pid in SO_PEERCRED is not mapped back to a domain either.

## Capabilities

The daemon holds the only authoritative table: token -> {resource, minter,
holder, expiry, revoked}. A token is 256 random bits (`cap_` + 64 hex).

- `bus.mint@v1 {"resource":"photo:42","ttl_ms":0}` returns `{handle, id,
  resource, holder}`. The caller is minter and first holder.
- A message may carry `caps` only if the sender's identity currently holds
  each one. Otherwise it is rejected before routing and logging, with a
  specific error: `cap_unknown` (forged or never minted), `cap_not_held`
  (real handle, wrong domain: knowing the token is not enough), `cap_revoked`,
  `cap_expired`. A delivered message's caps were therefore held by `from` when
  it was sent.
- `bus.transfer@v1 {"handle","to":"<identity>"}`: the holder gives it to a
  domain that has a live connection. The holder row changes at once (the old
  holder is rejected from then on) and the daemon pushes the token to the new
  holder as a `bus.cap.granted` event (`caps:[token]`, payload `{id, resource,
  from}`), delivered whether or not subscribed.
- `bus.revoke@v1 {"handle"|"id"}`: only the minter, wherever the handle is now.
- `bus inspect` lists outstanding handles (id, resource, holder, minter,
  expiry). Never the token. `--json` has `handles` and `domains`.
- `busd` enforces by default. `--legacy-caps` (`Config.EnforceCaps=false`,
  the library default so old tests and clients still run) leaves caps as
  opaque, forgeable strings.
- Replay is untouched: replayed messages carry only `cap_ids`, the check
  compares grants by identity (tested with a minted and transferred handle).

**Minting policy (MVP):** any identity may mint a handle for any resource name
it states. The properties provided are holder tracking, transfer, expiry and
revocation. NOT provided: authorisation of minting. A domain can mint
`file:/etc/shadow` and nothing in the bus knows what that name means, because
the bus does not bind a handle to an object; a service that trusts a handle
must itself decide who may mint which resource (e.g. only trust handles whose
minter is the owning service, visible in `bus inspect`). Also not provided:
a service cannot yet ask the daemon "who holds this handle" other than via
the sender-held guarantee above; handles are not attenuable; handle state is in
memory only, so a daemon restart invalidates every handle (the log has only
hashes, by design); revoked and expired rows are kept for their specific
errors and never pruned; holders keyed by session `uid:comm` inherit that
identity's weakness.

## Segments: data off the bus (0001)

Bulk data does not go in `payload`. It travels as a sealed shared-memory
segment (a `memfd`) whose fd is passed with `SCM_RIGHTS`. The JSON line holds
only a descriptor; the fd rides out of band on the same `sendmsg`.

```go
e := proto.Envelope{Kind: proto.KindEvent, Topic: "image.ready"}
client.AttachBytes(&e, "frame", "image/png", pngBytes) // or AttachFile(&e, id, mime, f)
c.Send(e)                                              // Send closes the attached fds

// receiver
ev := <-c.Events()
defer ev.CloseAttachments()
seg, _ := client.OpenSegment(ev, "frame")  // verified: memfd, sealed, size matches
defer seg.Close()
m, _ := seg.Map()                          // read-only mmap; m.Unmap() when done
use(m.Bytes())                             // or io.Copy(w, seg.Reader())
```

- **Package `shm`** is `memfd_create` + seals: `Create`, `WriteAt`, `Seal`
  (`F_SEAL_SHRINK|GROW|WRITE|SEAL`), `Map`/`Unmap`, `Reader`, `Adopt`
  (verify a received fd), `Supported` (clear error on kernels without memfd
  sealing). Write with `pwrite`, not a writable mmap: sealing fails while a
  writable mapping exists. Small JSON payloads are unchanged.
- **Descriptor.** `id`, `bytes`, `sealed`, `mime`, `sha256`. At most 16 per
  message. The daemon fstats each fd, requires it to be a memfd sealed
  against shrink, grow and write, requires the declared size to match,
  hashes the (immutable) contents, and **overwrites** the sender's `sha256`
  and `sealed`. A non-memfd, an unsealed memfd or a size lie is rejected with
  `bad_segments`. One segment is capped at 256 MiB (`Config.MaxSegmentBytes`),
  because the daemon hashes every one. Control verbs take no segments.
- **Delivery.** Fan-out gives each subscriber its own dup of the fd onto the
  same memory. The receiver's fd is shared with every other holder, so it
  uses `pread`/`mmap` (`Reader`, `Map`), never the shared file offset. A
  receiver cannot write the segment: write, truncate and a writable mmap all
  fail (tested).
- **Transport (`wire` package).** Sender: one `sendmsg` with the line and
  the fds; a short write is finished with plain writes. Receiver: `recvmsg`
  with a control buffer for 253 fds (the kernel's `SCM_MAX_FD`) and
  `MSG_CMSG_CLOEXEC`. Handled and tested: lines and fds reassembled across
  partial reads (a 300 KiB line with an fd); fds arriving in the same read as
  earlier lines (claimed by byte position, in order); **more fds than
  declared** (the message is rejected with `bad_segments` and all its fds
  closed, `/proc/self/fd` counted before and after); **fewer than declared**
  (rejected, partial fds closed, stream recovers); a peer that sends fds
  nobody declares (more than 64 unclaimed: connection ended, fds closed);
  stale fds from an earlier read (closed); a receiver that never reads (the
  daemon holds at most 256 queued fds per connection, drops and counts the
  rest, and closes them all when that connection goes away).
  Not handled: two fd-carrying lines in one `sendmsg` (the second sees
  fewer fds than declared and is rejected); no global cap on fds queued
  across all connections; a client that receives segments and never closes
  them holds them until it exits (Go finalizers close an unreachable
  `*os.File`).
- **Line limit.** Unchanged at 4 MiB for the daemon's read of one JSON line.
  The point is that big data no longer needs the line, so the limit now
  only bounds the memory a line can take; lowering it would break existing
  clients that put sizeable data in `payload`, so that is left as is.
- **Log.** The log never holds contents and never holds a live fd: only the
  descriptor and its content hash, like capability identity (`cap_ids`).
  Tested by grepping the log bytes for a marker written into the segment.
- **Replay.** Segment **contents are not replayable** (they are not in the
  log, 0001's "never the value" rule). `bus replay` re-sends each message
  with its descriptors, marked `absent`, to the replay daemon, which records
  and delivers them without fds. The checker compares descriptors and hashes,
  never bytes: a `segment descriptors` check (id, size, seal, mime, sha256
  per message, in the replay log and as delivered to the probe), and the
  per-topic hash chain includes each message's descriptors. A segment's
  hash changing in the reference log fails the check (tested). A receiver
  that calls `OpenSegment` on a replayed message gets an error.
- **CLI.** `bus send|pub --segment FILE[=mime]` (repeatable; the file is
  copied into a sealed memfd). A `bus serve` handler gets each segment as a
  read-only fd 3, 4, ... (a fresh open file description, own offset) plus
  `BUS_SEGMENTS` (descriptor JSON) and `BUS_SEGMENT_FDS`. `bus sub
  --segment-dir DIR` saves received segments. `bus inspect` shows per client
  the fds busd holds open for it, bytes, delivered and received counts;
  `bus log` shows `seg=` descriptors. Demo: `./demo-segments.sh`.

QoS: every connection has an outbox with four FIFO queues, one per class, and
the writer always serves the most urgent non-empty class. Routing never blocks
on a slow subscriber. `server/load_test.go` shows it: with 8000 background
messages backed up behind a stalled subscriber, the last of 100
user-interactive messages arrives at position about 1200 instead of 8099.

**Known design consequence: background traffic can starve.** The outbox is
strict priority with no aging, as 0001 requires ("user-interactive never
queues behind background"). So if a connection's recipient is continuously
fed user-interactive, user-initiated or utility messages faster than it
drains them, its queued background messages are never sent at all, for as
long as that load lasts (they wait until the higher classes are empty, or
until the 100,000 per-class cap drops newer ones). This is distinct from the
drop cap below. If background progress ever matters, the fix is aging
(promote a message after it has waited some bound); that changes the delivery
policy and the QoS test that asserts it, so it has not been done.
Observability only: `bus inspect` prints, per client, each class with a
backlog (queued count and age of its oldest undelivered message) and the drop
count; `bus inspect --json` has the same as `outbox` (indexed by rank, 0
user-interactive to 3 background) and `dropped`. A background `oldest_age_ms`
that keeps growing beside higher-class traffic is starvation.

Log: `<data-dir>/seg-000001.jsonl`, `seg-000002.jsonl`, ... one JSON message per
line, rotated at 4 MiB. Each segment's min/max time is kept in memory (rebuilt
by a scan on open), so `--since` skips old segments. Topic filtering scans.

## Run it

```sh
cd bus
go build -o ./out/ ./cmd/busd ./cmd/bus      # or: go run ./cmd/busd
./out/busd                                    # default socket $XDG_RUNTIME_DIR/aurora/bus.sock
```

`busd` flags: `--socket` (env `AURORA_BUS_SOCK`), `--data-dir` (env
`AURORA_BUS_DATA`, default `~/.local/share/aurora/bus/log`), `--segment-bytes`,
`--allow-replay`, `--domain IDENTITY=SOCKET` (repeatable), `--legacy-caps`. It creates the socket directory (0700), refuses to start if
another daemon owns the socket, removes a stale socket, and shuts down cleanly
on SIGINT/SIGTERM. The `bus` CLI honours `--socket` / `AURORA_BUS_SOCK` too.

## Worked example

```sh
export AURORA_BUS_SOCK=/tmp/demo/bus.sock
busd --socket $AURORA_BUS_SOCK --data-dir /tmp/demo/log &

# Terminal A: a service. Each request's payload is on stdin; stdout is the reply.
bus serve upper "tr a-z A-Z | sed 's/.*/\"&\"/'"      # (a JSON string reply)

# Terminal B: stream events
bus sub 'window.*'

# Terminal C
bus send upper shout@v1 '"hello"'                     # prints "HELLO"
bus pub window.opened '{"id":7}'                      # B prints the envelope
bus pub --ephemeral window.moved '{"x":1}'            # B prints it; never logged
bus log --since 10m --topic 'window.*'                # shows window.opened only
bus inspect                                           # clients, services, subscriptions
bus replay --check /tmp/demo/log                      # PASS/FAIL on invariants
```

`bus replay <log>` without `--check` re-delivers the recorded messages to the
daemon at `--socket` (start that one with `--allow-replay` to keep original
ids and senders as `origin`). `--speed 4` paces by recorded timestamps.
`bus replay --check` starts a fresh in-process daemon, replays into it while a
probe subscribes to `**`, and compares **invariants, never bytes**:

1. causal order: every message present once, per-sender order and
   request-before-response preserved;
2. final state: per topic (event count and an order-sensitive hash chain
   over every event payload, 128-bit hashes) as received by the probe
   subscriber, and per service (requests handled, chain over every request).
   Altering, dropping, adding or reordering any event on a topic changes the
   chain, and the failure names the diverging topic. (An earlier version
   hashed only the last payload and missed tampering with earlier events);
3. the set of capability grants (sender, target, capability identity);
4. segment descriptors (id, size, seal, mime, content hash) of every
   message that carried segments. Contents are not replayable, so this
   compares hashes, not bytes.

Exit status 0 means pass, 1 means fail. Byte diffing is deliberately absent.

`bus send/pub/sub/serve/log/inspect/replay` all take `-h`, print errors to
stderr and exit non-zero on failure. Flags may appear before or after
positional arguments.

The end-to-end demo, which leaves no process behind, is `./demo.sh`.
`./demo-identity.sh` runs two real bwrap domains (needs `bwrap`, builds
`adapter/linux`'s `domain`): distinct identities, mint, transfer, forgery
rejected, and the same-uid impersonation weakness.

## Tests

`go test ./...`. Covered: SO_PEERCRED identity beats a spoofed `from` (also in
the log); domain-socket identity beats a spoofed `from`, two domains differ and
one domain is stable, registration rules; invented, held, not-held,
transferred, revoked and expired handles, inspect, log holds only ids,
minted handles pass the replay check; request/response, typed errors, glob match and non-match, QoS
ordering (unit and under load), ephemeral delivered but not logged, log query
by topic and time (with segment rotation), handles never appear in the log,
replay invariant check passes on a recorded session and fails on tampered ones
(including middle-event payload edits and same-topic reordering),
HLC monotonic and sortable (including across the old 2^32 counter wrap),
outbox starvation stats, unknown envelope fields preserved. Segments: 16 MiB
round trip byte-identical, line stays small, log has descriptor and hash but
not contents, sealed segments are read-only to the receiver, surplus and
missing fds rejected with no fd leak, unsealed/mislabelled segments rejected,
no fd growth over 400 send/receive cycles, a never-reading receiver is
bounded, replay with segments passes and detects tampered hashes (`go test
-race ./...` too).

## Implemented vs not built

Implemented: JSON envelope; peer-credential sender identity; request/response;
pub/sub with globs; service registry; HLC; four-class QoS outboxes; segmented
append-only log with time/topic queries; ephemeral skip; handle identity
hashing in the log; `busd`; the `bus` CLI; invariant-based replay check;
sealed memfd segments passed by SCM_RIGHTS, logged as descriptor plus hash;
per-domain sockets with socket-derived identity; a daemon-held capability
table with mint, holder checks, transfer, revoke and expiry.

0001 specifies, but this MVP does **not** do:

- **Cap'n Proto or any final IDL.** Open question 1 is untouched. There is no
  schema, no typed payload validation, no promise pipelining, no level-3
  handoff. "Typed" today means the verb is versioned and errors are typed.
- **Unforgeable identity on a single-uid host.** Domain identity is the
  socket path (see "Identity"). Same-uid host processes, and any domain granted
  another's socket directory, can impersonate a domain. Session-socket
  identity is still `uid:comm`.
- **Capability authorisation and durability.** Handles are unforgeable and
  holder-checked, but minting is unauthorised (any identity, any resource
  name), handles are not bound to real objects or attenuable, the table is
  in memory (a restart invalidates all handles), tombstones are never pruned,
  and with `--legacy-caps` or the library default, caps are plain strings.
  A stolen token is useless without holding the row, but a holder
  who passes the token out of band is not stopped from telling others the value
  (they still cannot present it).
- **dma-buf, and any fd other than a sealed memfd.** memfd segments with
  SCM_RIGHTS are implemented (see "Segments"). dma-buf handles (GPU buffers,
  no seals, different size and trust rules) are not. Nothing forces a client
  to use segments: large data can still go in a payload up to the 4 MiB
  line limit. Also not done: segments inside `bus.log` replies as fds
  (descriptors only), a global cap on fds queued across connections, a
  per-sender byte quota for segment memory, and fd passing over any
  transport other than the Unix socket.
- **Authorisation.** Any local process of the same user that can open the
  socket (mode 0600) can register any free service name, subscribe to any
  topic and read the log via `bus.log@v1`. 0001 does not require more in
  Phase 3 but a real system will.
- **Topology question.** It is a single broker (open question 2); there is no
  peer-to-peer mode and no federation.
- **Streams** for long-running work (plan mentions them); only request/reply
  and pub/sub exist.
- **Log:** no fsync, no retention policy per topic, no sender index (only
  time per segment; topic and sender queries scan), no corruption recovery
  beyond skipping a torn final line, and `bus log` does not filter by sender.
- **Replay fidelity:** the log does not record subscriptions or service
  registrations, so "final state per subscriber" is computed per topic and per
  service rather than per actual subscriber. Replay does not re-run services:
  responses are re-delivered from the recording. Ephemeral traffic cannot be
  replayed, as 0001 says.
- **Backpressure:** a client that stops reading has up to 100,000 queued
  messages per QoS class; beyond that new messages for it are dropped
  (counted, not reported).
- **Bridges** (D-Bus, portals) and `bus inspect <domain>` filtering.
- **Delivery guarantees:** no acks, no redelivery. A message to a client that
  disconnects is lost (only the log has it). Events are delivered to a
  publisher's own subscriptions too.
