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
 "corr":"<request id>","payload":{},"caps":["<opaque handle>"],"ephemeral":false}
```

- `id`, `ts`, `from` are **always set by the daemon**. A client-supplied `from`
  is overwritten. The sender's domain is read from `SO_PEERCRED` on the socket
  (tested in `server/server_test.go`). The MVP domain string is
  `uid<uid>:<process name>` (kernel-reported uid, `/proc/<pid>/comm`); Phase 2's
  attested domain id replaces it via `Config.Resolver`.
- `ts` is a hybrid logical clock: `<wall ns, 19 digits>-<counter, 10 digits>`,
  monotonic per daemon, and string order equals time order.
- `verb` must carry its major version (`resize@v1`).
- `qos` defaults to `user-initiated`.
- `caps` are opaque handles. Recipients get them. **The log stores only
  `cap_ids`** (`cap:` plus a truncated SHA-256), never the handle.
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
  `bus.inspect@v1`, `bus.log@v1`. They are not logged.

QoS: every connection has an outbox with four FIFO queues, one per class, and
the writer always serves the most urgent non-empty class. Routing never blocks
on a slow subscriber. `server/load_test.go` shows it: with 8000 background
messages backed up behind a stalled subscriber, the last of 100
user-interactive messages arrives at position about 1200 instead of 8099.

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
`--allow-replay`. It creates the socket directory (0700), refuses to start if
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
2. final state: per topic (event count, last payload) as received by the
   probe subscriber, and per service (requests handled, last request);
3. the set of capability grants (sender, target, capability identity).

Exit status 0 means pass, 1 means fail. Byte diffing is deliberately absent.

`bus send/pub/sub/serve/log/inspect/replay` all take `-h`, print errors to
stderr and exit non-zero on failure. Flags may appear before or after
positional arguments.

The end-to-end demo, which leaves no process behind, is `./demo.sh`.

## Tests

`go test ./...`. Covered: SO_PEERCRED identity beats a spoofed `from` (also in
the log), request/response, typed errors, glob match and non-match, QoS
ordering (unit and under load), ephemeral delivered but not logged, log query
by topic and time (with segment rotation), handles never appear in the log,
replay invariant check passes on a recorded session and fails on tampered ones,
HLC monotonic and sortable, unknown envelope fields preserved.

## Implemented vs not built

Implemented: JSON envelope; peer-credential sender identity; request/response;
pub/sub with globs; service registry; HLC; four-class QoS outboxes; segmented
append-only log with time/topic queries; ephemeral skip; handle identity
hashing in the log; `busd`; the `bus` CLI; invariant-based replay check.

0001 specifies, but this MVP does **not** do:

- **Cap'n Proto or any final IDL.** Open question 1 is untouched. There is no
  schema, no typed payload validation, no promise pipelining, no level-3
  handoff. "Typed" today means the verb is versioned and errors are typed.
- **Real domain identity.** Phase 2 does not exist yet. `from` is
  `uid:comm`, so two processes of one user running the same binary share a
  domain, and a process can rename itself. Anything that needs real isolation
  is not provided here.
- **Capability semantics.** Handles are opaque strings that are passed along
  and hashed for the log. There is no minting, unforgeability, transfer
  control, revocation or check that a sender holds what it sends (open
  question 3, Phase 2). A client can invent any handle string.
- **Shared-memory handles and fd passing (SCM_RIGHTS).** No memfd or dma-buf
  transfer. Bulk data is not separated from payload bytes, and nothing stops a
  client putting large data in a payload (the line limit is 4 MiB).
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
