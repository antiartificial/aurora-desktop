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
  `bus.domain.remove@v1` `{"identity","path","cgroup"}` (cgroup optional), which the controller calls over
  the session socket (a domain connection is refused with `forbidden`, so a
  domain cannot create domains). Chosen over watching the adapter's socket
  directory because it is explicit, needs no inotify or scan races, and the
  daemon never has to guess that a directory entry is a domain; the cost is
  that the controller must call it when it creates or destroys a domain.
  Identities must match `domain:<name>:<hex>`, which a session identity can
  never match, and each identity and path can be registered once. Removing a
  domain closes its socket, disconnects its clients and drops handles it holds.
- **Attestation (kernel-checked membership).** A domain registered with a
  cgroup (`--domain-cgroup ID=CGROUP`, `AddDomainCgroup`, or `"cgroup"` in
  `bus.domain.add@v1`; the adapter's `domain cgroup <name>` prints it) is no
  longer "whoever opens the socket". On accept the daemon reads the peer pid
  from SO_PEERCRED, reads `/proc/<pid>/cgroup` (`0::` line) and requires it to
  equal the domain's cgroup or be below it. Otherwise the connection gets an
  `attestation_failed` error line and is closed, and the log says which pid,
  uid and comm, which cgroup it was in and which one the domain has.
  What the kernel attests: the pid that called `connect` (SO_PEERCRED) and the
  cgroup the kernel has that pid in. What is inferred: that a process in the
  domain's cgroup is the domain, which holds as long as only `domain run` puts
  processes there.
  `--attest-domains=require|prefer|off` (`Config.AttestDomains`, zero value
  prefer; mode is logged at startup and per domain):
  `require` refuses anything unattestable (no cgroup registered, `/proc` pid
  unreadable, no cgroup v2); `prefer` (default) attests whenever a cgroup is
  registered and refuses mismatches, and accepts only the two cases where
  attestation does not exist (no cgroup registered; host without cgroup v2),
  each with a `WARNING ... UNATTESTED` line per connection; an unreadable pid
  is refused even in prefer; `off` is the old socket-is-identity behaviour.
  Note a controller that registers domains without a cgroup is, under
  `prefer`, still unattested: pass the cgroup, or run `require`.
  Overlapping or nested domain cgroups, `/` and non-absolute paths are
  refused at registration.
- **Races, stated.** (1) PID reuse between the SO_PEERCRED read and `/proc`:
  the daemon also takes `SO_PEERPIDFD` (Linux 6.5+), which pins the exact
  process, and after reading `/proc` checks with `pidfd_send_signal(0)` that
  it is still there, so a recycled pid is refused. On kernels without
  SO_PEERPIDFD it compares `/proc/<pid>/stat` field 22 (start time) before and
  after the cgroup read, which leaves a residual: a reuse inside that window
  that matches start time to the tick. (2) A client that exits right after
  connecting can be gone (reaped) before the daemon reads `/proc`; it is
  refused (fail closed), so a very short-lived legitimate client can be
  turned away under load. (3) Leaving the cgroup after connecting: the daemon
  re-reads the original connector's cgroup at most once a second, on the next
  message, and disconnects it if it is alive and outside. Messages sent in
  the up-to-1s window before the next check are delivered. A connector that
  has exited is not treated as a violation (its connection may live on in a
  child that inherited it). (4) The connection is attested by its connector;
  a domain process can pass the connected fd on (SCM_RIGHTS) and the
  receiver speaks as the domain.
- **Namespace attestation (closes the join-the-cgroup attack).** Cgroup
  membership alone is forgeable: the cgroup directory is owned by the host uid,
  so a same-uid process can write its pid into `cgroup.procs` (this was
  `demo-identity.sh` 8c; it is now 8d, shown succeeding on a cgroup-only
  domain). A process started by `domain run` is also in bwrap's own pid, mount,
  ipc and uts namespaces, which the kernel names by the inode of
  `/proc/<pid>/ns/<type>`; a process that only joined the cgroup is still in
  the host's. A domain registered with namespace attestation (`--domain-ns ID`,
  `AddDomainNS`, or `"ns":true` in `bus.domain.add@v1`) therefore needs BOTH:
  cgroup membership AND all four namespace ids equal to those of a live
  registered run. Each connection's log line names both signals:
  `signals cgroup=ok namespaces=FAILED: peer namespaces (...) match none of the
  1 live runs`. A domain registered without it stays cgroup-only and logs
  `namespaces=NOT CHECKED` with a WARNING per connection.
  *Why these types.* `user` is NOT used: bwrap replaces the sandbox's user
  namespace after the point where the adapter can hold it (when it sets up
  `/dev`; observed: init's user ns inode changes after `--block-fd` is
  released), so it is unknown before the payload can connect. pid, mnt, ipc
  and uts are fixed by then. pid and mnt are the ones that matter; ipc/uts are
  free extras. *Per run, not per domain.* Every `domain run` makes fresh
  namespaces and they cannot be pinned without CAP_SYS_ADMIN in the host mount
  namespace (a bind mount of `/proc/<pid>/ns/*`), so identity is per run: the
  domain has a set of live runs. `domain run` registers its run
  (`bus.domain.ns@v1 {identity, pid, ns}`, pid = sandbox init) when
  `AURORA_BUS_SOCK` is set, BEFORE releasing the payload (bwrap `--block-fd`),
  and unregisters when it ends. The daemon reads the ids from `/proc` itself;
  ids a registrar sends are cross-checked, not trusted. A run lives exactly as
  long as its init pid (same start time, not a zombie): when it ends it is
  pruned, so a recycled namespace inode can never match, and a destroyed and
  recreated domain (new identity, new registration) knows no runs.
  Edge cases, tested: a peer whose `/proc/<pid>/ns/*` cannot be read (gone, or
  in a pid namespace the daemon cannot see) is refused in prefer and require;
  a registered pid in the daemon's own pid or mount namespace is rejected (it
  is not a sandbox, every host process would match), and so is one sharing
  pid/mnt with another domain's live run; a `require` or `prefer` daemon with
  a namespace domain and no live run refuses ("namespace ids unknown"); `off`
  still accepts everything. Namespaces are also re-read with the cgroup
  recheck (at most once a second) and a connector that changes them is
  disconnected (implemented; not covered by a test).
- **uid attestation (per-domain subordinate uids).** `domain run` puts a
  domain's processes under their own subuid (`adapter/linux/README.md`, "Per-domain
  uid"), so SO_PEERCRED's uid names the domain. Register it with
  `busd --domain-uid ID=$(domain uid N)`, `SetDomainUID`, or `"uid":N` in
  `bus.domain.add@v1`; it is a third signal in the same `--attest-domains`
  modes (checked first; off ignores it; two domains cannot register one uid).
  A mismatch is refused: `signals uid=MISMATCH (peer uid 1000, domain uid is
  105539) cgroup=not checked namespaces=not checked`; other lines end `uid=ok`
  or `uid=not-registered`. A domain registered with a uid and no cgroup is,
  under `prefer`, accepted when the uid matches (still logged unattested for the
  rest); `require` still needs the cgroup. Not re-checked after admission.
  *Measured (`demo-identity.sh` 8f to 8j, tests `TestUID*`):* a host process
  with the real uid is refused on uid alone (8a, `TestUIDRefusesRealUIDHostProcessOnUIDAlone`);
  the cgroup-joiner is refused (8c); the setns attacker that keeps its own uid
  is refused (8f); a plain `setuid` to the domain's uid fails with EPERM (8h);
  a user namespace of the attacker's own, mapped over the user's subuid range,
  forges the uid and is ACCEPTED when uid is the only signal (8i), and refused
  by the namespaces under the full stack (8j). **Before the ancestry signal
  (below), the setns attacker that also takes the domain's uid was ACCEPTED
  (8g).** It is now refused (`TestUIDSetnsAttacker`). The
  hypothesis that its uid "stays its own" is wrong: the sandbox's user namespace
  is created by, and so owned by, the host user, which holds every capability
  in it (and in anything nested); inside it the domain's uid is mapped and the
  attacker may take it. Note a first run of 8g was refused for an accidental
  reason: changing euid makes the attacker non-dumpable, so the daemon (not
  owner of the attacker's mm) could not read its `/proc/<pid>/ns`; calling
  `prctl(PR_SET_DUMPABLE, 1)` removes that, so do not rely on it.
- **Process ancestry (fourth signal, namespace-attested domains).** The peer
  must descend, by real parent (`/proc/<pid>/stat` field 4), from the sandbox
  init pid of the live run whose namespaces it matched (`server/ancestry.go`).
  The walk is capped at 64 steps, each parent must have started no later than
  its child (a recycled pid starts later), and the init pid must still have its
  registered start time. A chain that reaches pid 1 without passing the init
  pid is refused with `ancestry=FAILED`. Measured: real `domain run` processes
  are accepted (every step 8 line shows `ancestry=ok`); the 8g attacker (setns
  into everything, take the domain's uid, fork) passes cgroup, namespaces and
  uid and is refused on ancestry alone, because it forked after `setns`, so its
  parent chain ends in the host (`TestUIDSetnsAttacker`, demo 8g). Regression
  risk checked: a legitimate process whose parent exited is reparented to the
  sandbox init (pid 1 of its pid namespace) and is still accepted
  (`TestAncestryOrphanInsideSandboxStillDescends`); host processes, children
  of the test process and a wrong init start time are refused
  (`TestAncestryHostProcessAndPidReuseRefused`). NOT tested, so not claimed:
  injection. A same-uid process that `ptrace`s a legitimate domain process and
  makes it connect acts through a real descendant, which ancestry accepts by
  construction; I did not build that experiment and I expect it to defeat this
  signal. Ancestry removes the cheap-to-script attacker (setns then connect from
  a process of its own), not an attacker willing to hijack a real process. Also
  untested: a daemon that is not in the host pid namespace.
- **What this does NOT guarantee.** Membership, not intent. (1) Namespace and
  uid attestation alone do NOT stop a same-user host process that enters the
  domain's namespaces and takes the domain's uid (8g); only the ancestry
  signal does, and ancestry does not stop injection into a real descendant. It opens
  `/proc/<init>/ns/*`, takes the owning user namespace with
  `ioctl(NS_GET_USERNS)`, `setns` into it, then ipc, uts, mnt and pid, joins the
  cgroup, takes the uid and forks: the child matches every signal. So the cheap
  attacks (write one file; connect with the real uid; setns without the uid)
  are closed; a determined same-user attacker is not. Closing it needs a uid
  the user does not control (a namespace created by a privileged helper) or a
  policy that denies `setns`/`/proc/<pid>/ns` to the user's other processes;
  neither is available unprivileged. The same power lets it ptrace or inject
  into the domain's real processes. (2) The session
  socket (uid:comm) is trusted at the same-uid level: a same-uid client can
  call `bus.domain.ns@v1` and register a sandbox of its own making, or
  remove/re-add a domain. (3) Any process the domain itself starts is the
  domain. (4) A connected fd passed on (SCM_RIGHTS) lets the receiver speak as
  the domain. (5) A run registered through the adapter exists only while
  `AURORA_BUS_SOCK` was set when `domain run` started. (6) The daemon must see
  the domain's pids in `/proc` (same pid namespace) and use the same cgroup
  namespace as the one the registered path was computed in. The session
  socket is unchanged. Verified here on cgroup v2 with a systemd-delegated
  `user@1000.service` subtree; the "no cgroup v2" degrade branch is code only.

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
- **dma-buf segments.** A segment may be a dma-buf instead of a sealed memfd
  (descriptor `kind:"dmabuf"`; `kind` is absent for a memfd). The daemon
  classifies the fd itself (`shm.DetectKind`): a dma-buf is an fd on the
  kernel's dma-buf filesystem (`fstatfs` magic `DMAB`) that also answers
  `DMA_BUF_IOCTL_SYNC` with flags 0 as a dma-buf does (EINVAL; other files give
  ENOTTY). The sender's declared kind must equal the detected one, else
  `bad_segments`; pipes, `/dev/null`, regular files and unsealed memfds are
  rejected. **What differs from a memfd, and what is recorded.** A dma-buf
  cannot be sealed and every holder (and the device) can keep writing, so the
  daemon records `sealed:false`, NO `sha256`, the size from
  `lseek(fd,0,SEEK_END)` (checked against `bytes`, capped by
  `MaxSegmentBytes`) and `ino`, the kernel's inode number of the buffer, to
  correlate the same buffer across messages. The log therefore proves "a
  dma-buf of N bytes with this identity was sent", NOT what it held; the memfd
  guarantee (immutable contents, content hash in the log) does not exist for
  it, and a receiver sees later writes by the sender (tested). Never treat a
  dma-buf as a snapshot. API: `client.AttachDmabuf`, `client.OpenDmabuf`
  (`OpenSegment` refuses a dma-buf), `shm.IsDmabuf`, `shm.AdoptDmabuf`.
  **Tested on this host with a real dma-buf** made via `/dev/udmabuf`
  (`shm.CreateUdmabuf`, no GPU; tests skip where it is not accessible):
  detection, descriptor, mutability, log shape, kind mismatches and impostors,
  no fd leak. **Untested:** dma-bufs from a GPU driver or dma-heap (different
  exporters, device-local memory, fences; detection should hold as it relies on
  the filesystem, not the exporter, but this is unproven), importing the fd
  into a GPU API, and dma-bufs across `bus replay` end to end (only the
  descriptor signature is unit-tested).
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
rejected, then a host process impersonating a domain: refused with attestation
(8a), accepted with `--attest-domains=off` (8b); the join-the-cgroup attack
refused with namespaces attested (8c), accepted on a cgroup-only domain (8d, the
old hole) and with attestation off (8e); then against per-domain subuids: the
setns attacker with its own uid is refused (8f), and one that also takes the
domain's uid is refused by process ancestry (8g; it passed every other signal
before ancestry existed), with a real domain process shown accepted (8g'); a plain setuid to the domain's uid fails
(8h); a forged uid in the attacker's own user namespace passes when uid is the
only signal (8i) and is refused by the namespaces under the full stack (8j).
The attacker code is `demo-attacks.py`.

## Tests

`go test ./...`. Covered: attestation (`server/attest_test.go`: a process started
inside a real cgroup is accepted, a host process is refused with a detailed log,
`off` restores the old behaviour, `require` refuses without a cgroup, leaving the
cgroup drops the connection; tests skip without a delegated cgroup subtree;
`server/ns_test.go`, `ns_internal_test.go`: a real bwrap process is accepted, a
host process that joins the cgroup is refused (and is accepted by a cgroup-only
domain, the control), ordinary host refused, `off` permissive, `require`/`prefer`
refuse when namespace ids are unknown, a recreated domain inherits no runs, dead
runs are pruned, registration rules);
SO_PEERCRED identity beats a spoofed `from` (also in
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
- **Any fd other than a sealed memfd or a dma-buf.** Both kinds travel as
  segments (see "Segments"); dma-bufs carry no seal and no content hash. Nothing forces a client
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
