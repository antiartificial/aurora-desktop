# 0008. Running Haiku applications: source compatibility on the bus

- **Status:** proposed
- **Date:** 2026-11-06

## Context

The project owner likes the BeAPI and wants Aurora to eventually run a simple Haiku OS application. The plan lists this as an open question: "Haiku compatibility: port libbe onto the bus as a native API, or skip?" (`docs/toy-desktop-os-plan.md`, Open questions, marked Later). This record answers it: do not skip.

Record 0004 recommended retiring the BeAPI layer of the Vitruvian tree (VOS, `/home/arti/projects/Vitruvian`) as unneeded. That recommendation rested on the plan not needing a Be-compatible surface. The goal now requires one, so 0004's BeAPI item is superseded by this record. 0004's Nexus, GPU compositor and harness conclusions are unchanged.

Nothing described below has been built or run. The assessment comes from a read-only survey of the VOS tree on 2026-11-06 (branch `test/gbm-with-fixes`, HEAD `3145eee8`). Feasibility claims are marked as such.

## Findings

Scale:

- `src/kits` holds 635 `.cpp` and `.h` files, 307,966 lines: app, interface, storage, support, shared, locale, game, tracker, translation, textencoding, screensaver. `headers/os` has 260 headers, 35,198 lines. `src/servers/app` (app_server) is 270 files, 83,144 lines.
- Kits present: Application, Interface, Storage, Support. Translation is partial. The Net Kit is in the source but commented out of the build. The Media Kit is absent.

Messaging core (`src/kits/app`): `Message.cpp` 3437 lines, `Looper.cpp` 1514, `Handler.cpp` 883, `Messenger.cpp` 458.

- It has zero `AS_*` (app_server protocol) references and zero direct Nexus references.
- `Handler.cpp`, `MessageQueue.cpp`, `MessageAdapter.cpp`, `LooperList.cpp`, `TokenSpace.cpp` and `DirectMessageTarget.cpp` have zero kernel-primitive call sites.
- It does call the BeOS kernel primitive ABI. `Message.cpp` has 33 call sites (`create_port`, `read_port`, `write_port_etc`, `port_count`, `set_port_owner`, `create_area`, `get_area_info`, `set_area_protection`, `delete_area`, `get_port_info`, `delete_port`). `Looper.cpp` has 21 (`create_port`, `delete_port`, `port_count`, `create_sem`, `delete_sem`, `acquire_sem`, `release_sem`, `spawn_thread`, `resume_thread`, `find_thread`). `Messenger.cpp` has 1.
- In VOS those primitives come only from `src/system/libroot2` (13,623 lines), which calls `nexus_io()` at 49 sites. It is backed by the Nexus kernel module (`src/system/kernel/nexus`, a git submodule of `https://github.com/Numerio/Nexus.git` pinned at `140156e`, 4,769 lines, SPDX GPL-2.0, copyright Dario Casalinuovo). `libroot2`'s `port.cpp` is LGPL.

Drawing path:

- `View.cpp` has 396 `fLink` references and `Window.cpp` 164. 268 distinct `AS_*` codes appear across 17 files in `src/kits`.
- Drawing marshals `AS_*` messages through `ServerLink`/`LinkSender.cpp`, written to a port with `write_port_etc`. app_server rasterises with `Painter.cpp` (2,384 lines). There is no client-side rasteriser.
- `BApplication` (`Application.cpp`, 1709 lines) calls `_ConnectToServer`, does `find_port("a<app_server")`, sends `AS_CREATE_APP`, and registers with the registrar. It exits fatally if `be_roster` is invalid.

Provenance:

- This is upstream Haiku code, MIT licensed, synced as late as hrev59379. Of 895 files in `src/kits` plus `headers/os`, 640 carry the MIT "Distributed under" line and 564 mention Haiku in the first 15 lines.
- VOS's own edits are narrow. Commit `dc5a0f33` replaced `__HAIKU__` with `__VOS__`, leaving 60 `__VOS__` occurrences across 30 files. Six files carry VOS-specific headers: `src/kits/storage/MergedDirectory.cpp`, `headers/os/kernel/OS.h`, `headers/os/storage/Entry.h`, `headers/os/storage/Node.h`, `headers/os/storage/StorageDefs.h`, `headers/os/support/Errors.h`.

Not determined: whether a clean VOS build succeeds; whether libbe builds standalone (`src/kits/CMakeLists.txt` defines `AddOn(be SHARED)`, but the top-level build needs `BUILDTOOLS_DIR` and `buildtools/bin` is absent); whether the messaging tests pass at runtime; whether `libroot2` has a non-Nexus fallback; whether app_server can run headless.

## Decision

Pursue source compatibility, not binary compatibility. Simple Haiku apps are recompiled against a libbe built on this project's own substrate. Unmodified Haiku binaries are not a goal. Nexus exists to give binary compatibility, and source compatibility does not need it.

Work is staged. Each stage is plausible from the survey and proven by nothing.

**Stage A: messaging core only.** `BMessage`, `BLooper`, `BHandler`, `BMessenger`, with the BeOS kernel primitives reimplemented in userspace on Linux, not in a kernel module:

- ports map onto this project's message bus **(not as built — see Stage A as implemented, below)**
- areas map onto memfd shared memory
- semaphores map onto POSIX semaphores
- threads map onto pthreads

The areas mapping depends on a known bus gap: 0001 specifies shared-memory handles passed as file descriptors (SCM_RIGHTS), and the bus does not implement fd passing yet.

Target: a non-drawing `BApplication` whose `BLooper`'s port is a bus endpoint. The survey shows the call sites for this layer are confined to `Message.cpp` (33), `Looper.cpp` (21) and `Messenger.cpp` (1), with no `AS_*` or Nexus coupling. That makes the layer bounded. It does not show it works. `BApplication` as surveyed also connects to app_server and the registrar, so a non-drawing variant needs those connections stubbed or replaced, which has not been scoped.

**Stage B: `BWindow` and `BView`.** This project's compositor plays app_server's role. Depends on Phase 4. This is the expensive half: 396 + 164 `fLink` references, 268 `AS_*` codes, 83,144 lines of app_server, and no client-side rasteriser. Either the `AS_*` protocol is served by something Aurora writes, or the kits are changed to draw another way. This record does not choose.

**Stage C: a Haiku app as a domain.** Its manifest is generated from the app's signature and scripting suite. Depends on Phase 6.

## Design intuitions, not validated

The following are reasons the fit looks good. None has been tested by running anything. Each could fail on contact with real code.

- `BMessage` is a typed, self-describing dictionary. The bus envelope (0001, 0006) carries a typed payload.
- `BLooper` and `BHandler` form a message loop with addressable targets. The bus has services and subscriptions.
- `BMessenger` resembles a capability handle to a target. 0001 defines handles as opaque, unforgeable and transferable. `BMessenger` is forgeable in Haiku if the port and token are known, so the resemblance may not carry over.
- Haiku scripting suites (apps exposing properties and actions over `BMessage`) closely resemble the typed-verb model in 0002.
- `replicants` is already a declared manifest section in 0002 and currently describes nothing.

## Stage A as implemented (2026-11-06)

The primitives layer is built in `kits/` and verified: 1594 checks pass from a
clean build, and two separate processes exchange a flattened message through a
port and share a buffer through an area.

Two divergences from the decision above, both deliberate, both recorded here
rather than quietly absorbed:

**Ports are NOT bus endpoints.** They are a standalone shared-memory ring in
`/dev/shm`, one file per namespace, with a process-shared robust mutex and
condvars. The reason is concrete: Haiku code passes bare `port_id` integers
between processes, so an id must be globally meaningful without a broker in the
path. A socket cannot provide `port_count`, nor a `delete_port` that wakes a
peer's blocked read; POSIX message queues cap out around 10 messages of 8 KiB.
Condvars give blocking, timed waits and delete-wakeups directly.

This means the Stage A target above — "a `BLooper`'s port is a bus endpoint" —
is **not met**. Whether it should be remains open. A `BLooper` on a private ring
is a faster path and a weaker claim: it does not demonstrate that the bus design
accommodates BeOS messaging, which was part of this record's reasoning for the
fit being good. Bridging ports to the bus, or running both, is unscoped.

**Semaphores are a mutex and condvar, not POSIX `sem_t`.** `sem_t` cannot do an
atomic multi-count acquire, wake waiters on deletion, or `B_RELEASE_ALL`, all of
which BeOS semantics require.

Matched exactly and tested: Haiku error codes and typedefs; blocking read on
empty and write on full; relative, absolute and infinite timeouts, with a zero
relative timeout returning `B_WOULD_BLOCK`; `delete_port` waking waiters with
`B_BAD_PORT_ID` while `close_port` lets readers drain; generation-tagged port
ids so a stale id fails; areas on sealed memfd with clones getting their own
area id; threads born suspended; `find_thread(NULL)` returning the kernel tid.

Not yet done: no `BMessage`, `BLooper`, `BHandler` or `BMessenger`. Nothing has
been compiled against real Haiku kit source, so the 21-call list comes from the
survey, not from a build, and gaps should be expected. The tests were written
alongside the implementation and have not been mutation-tested, so they show
these cases pass rather than that a regression would be caught.

### Stage A runs an application

Added after the messaging core landed in `kits/` (`libkitsapp.a`). On top of the primitives there is now a from-scratch reimplementation of the app-kit API surface: `BMessage` (typed named multi-item fields, nesting, flatten/unflatten), `BHandler`, `BLooper` (its port is one of our ports; the loop runs on a `spawn_thread` thread), `BMessenger` (async and synchronous send, local and cross-process) and `BApplication` (no app_server, no registrar; see `kits/README.md` for what is stubbed). It follows Haiku's MIT public API shapes; no Haiku source is used. This is not Haiku's own `BMessage`/`BLooper`.

What runs: `kits/demo/haiku_app.cpp`, a `BApplication` subclass with a `BHandler` that keeps a ledger. A second process finds it by signature, sends `BMessage`s with `BMessenger::SendMessage(msg, &reply)` and gets synchronous replies, including a nested-message reply, a not-understood reply, and a quit request. `kits/tests/test_app.cpp` (151 checks) covers the same paths, plus a flattened message round trip through a port between two processes.

Still open, so real Haiku source cannot yet compile against this:

- Haiku's own `BMessage` is not used. Not implemented: scripting and specifiers, `BArchivable`, `BString`, `BList`, `BMessageFilter`/`BMessageQueue`/`BMessageRunner`/`BInvoker`, `BRoster`, most `Add*`/`Find*`/`Replace*` types (pointers, refs, rects, points, messengers, flat objects), `ForEach`, drop messages, and Haiku's flat format (ours is different and unversioned).
- Haiku's own `BLooper` behaviours not implemented: message filters, per-looper message queue and `PreviousMessage`/`DetachCurrentMessage`, `LockLooper*` helpers, `WaitForQuit`, the reply-to/preferred-handler plumbing in `BMessage`, lock-ordering diagnostics, and enforcement of the "hold `Lock()` while changing handlers" rule.
- No registrar: `BMessenger(signature)` finds the app by a hashed port name. There is no single-launch, no team registry and no `be_roster`. `BApplication` reads its argv from `/proc/self/cmdline`.
- Ports are still not bus endpoints, so the bus-fits-BeOS-messaging claim remains untested; latency (open question 7) is unmeasured.
- Open questions 1, 2, 4 and 5 are untouched. Question 6 is answered for the non-drawing case by the stubs above, and not at all for a drawing application.
- The first demo run had an intermittent failure: a client could send between the port's creation and `ReadyToRun`, and hit a handler that was not yet added. It was found by repeating the test and fixed in the demo, not in the kit; Haiku's registrar is what hides this window on a real system.

## Result: compiling Haiku's own messaging source (2026-11-06)

Run in `kits/haiku-compat/` (full detail and gap inventory in its README). Haiku's real `Message.cpp`, `Handler.cpp`, `Looper.cpp` and `Messenger.cpp` were compiled in place (not copied) against our `libkitsos` primitives. Source used: the Vitruvian fork, not stock Haiku.

- **They compile, link and run.** 26 Haiku files build after about 165 lines of shim and stubs. A real `BMessage` flattens (`HMF1`, 381 bytes for a six-field message), unflattens, re-flattens byte-identically and survives one of our ports. A real `BLooper`/`BHandler`/`BMessenger` completes a synchronous send and reply in one process, 30 of 30 runs.
- **The earlier expectation was too pessimistic.** The list of "not implemented" in the previous section is mostly about our reimplementation, not about what Haiku's code needs. Haiku's code needs a small number of kernel calls, and our primitives cover the ones it exercises.
- **Headline blockers, none of them in the messaging core:** (1) our `OS.h` has 21 calls, Haiku's has 86 declarations, and everything that failed to compile was in that difference; (2) the storage kit (`BPath`, `BEntry`, `BFile`, `BNode`) rests on `_kern_*` syscalls (27 in `Entry.cpp` alone), so 14 symbols stay unresolved and calling any crashes; (3) no registrar, so `BRoster` is hollow; (4) `BApplication` still reaches for app_server; (5) vrefs, VOS's fd-carrying handles, and `_kern_transfer_area` are hollow, so no message can carry an fd or a large area.
- **Hollow, marked in `stubs.cpp`:** vrefs, port sender identity (`_get_port_message_info_etc` reports size only), `get_thread_info`, `exit_thread`, image loading, the roster. A test passing does not exercise these.
- **Caveats that limit the claim.** The VOS fork is already a Linux/glibc port with its own delta (`__VOS__`, vref fields in `entry_ref`), which is why this went well; stock Haiku was not tried. The flat-format check is a self-round-trip: no bytes from a real Haiku system were compared, so wire interop is unproven. The looper test is in-process only.
- **Effect on staging.** Open question 4 (track the Haiku ABI so upstream source compiles) looks affordable for the messaging core: replace our `kits/src/app` with Haiku's rather than extend it. Question 5 is partly answered (the messaging files build standalone, without `BUILDTOOLS_DIR`; the full libbe was not tried). Stage A's remaining cost sits in the storage minimum, a registrar and the `BApplication` server cut, estimated at 2 to 3 weeks (see README). The bus-endpoint claim from "Stage A as implemented" is still not met: this experiment used our shared-memory ports. Stage B is unaffected.

## Open questions

1. **Sourcing: upstream Haiku or the VOS fork.** The kits are upstream Haiku MIT code, so they could be taken from Haiku directly. VOS's distinctive contribution is the narrow port delta: 60 `__VOS__` marks across 30 files and six VOS-specific headers. That is a map of what must change to build Haiku's kits off Haiku, and it may be worth more than the code. The choice is undecided.
2. **Licence mixing.** Kits are MIT, Nexus is GPL-2.0, `libroot2`'s `port.cpp` is LGPL. Whether avoiding Nexus entirely keeps Aurora clear of GPL obligations, and what any `libroot2` reuse would bring in, is unchecked.
3. **Headless app_server as a Stage B shortcut.** Whether app_server can run without a display, so Stage B could proxy its output into the compositor. Not determined.
4. **ABI target.** Whether to track the Haiku ABI closely enough that upstream Haiku source compiles unmodified, or to accept a dialect.
5. **Build.** Whether libbe builds standalone at all, given the `BUILDTOOLS_DIR` requirement. Whether the messaging tests pass.
6. **Non-drawing `BApplication`.** How to satisfy the app_server and registrar connections in Stage A.
7. **Latency.** Whether mapping every port write onto a bus message meets `BLooper`'s expected cost. Unmeasured.

## What would change this

- Stage A fails to run a non-drawing `BApplication` on the bus, or needs more than the primitives listed above: the staging is wrong, and source compatibility needs reassessing.
- Licence review shows the kits cannot be combined with the project's code as planned.
- The Stage B cost is judged too high after the headless app_server check: stop at Stage A and treat Haiku support as a messaging-only library.

## Consequences

- 0004's BeAPI retirement is superseded. The Nexus retirement stands and is reinforced: this plan avoids Nexus by reimplementing the primitives in userspace.
- Stage A adds a requirement on the bus: fd passing, which 0001 specifies and the bus lacks.
- Phase 4 and Phase 6 gain later dependents. Neither phase's scope changes now.
- The plan's open question on Haiku compatibility is answered. The plan text has not been edited by this record.
- No code from the VOS tree enters Aurora until the sourcing question is settled.

## Result: does BeOS messaging fit the bus? (2026-11-06)

0008 claimed the bus accommodates BeOS messaging, and listed the reasons as untested intuitions. `kits/src/bus` tests it by connecting a `BLooper` to a real `busd`. A `BApplication` subclass (`kits/demo/haiku_ledger.cpp`) with an ordinary `BHandler` and `switch (message->what)` serves `add@v1`, `list@v1` and `total@v1`; the Go `bus send haiku-ledger add@v1 '{"label":"coffee","cents":350}'` returns the handler's reply (`{"total":350,...}`), and the handler's `Publish` reaches `bus sub`. Design, type mapping and lossy cases: `kits/README.md` "Bus bridge". `kits/tests/test_bus.cpp` (206 checks, real busd) passes; the demo passed 10 of 10 runs.

**Verdict: mixed. The serving case fits. The claim as 0008 stated it (a looper's port is a bus endpoint) does not hold, and I did not make it hold.**

What I built is an adapter, not ports on the bus. The `BLooper` still owns its shared-memory port. A reader thread and a serving thread sit in front of it and do a synchronous `BMessenger::SendMessage` into that port per bus request, with a private reply port created and deleted each time. A bus request therefore crosses a socket, two threads and a ring buffer before the handler sees it. The port stays reachable by any same-uid process, so nothing the bus enforces (identity, capabilities, the log) covers messages that bypass the bridge. Latency was not measured.

**Where it fit well**

- *Typed, self-describing fields.* `BMessage` maps onto a JSON payload without contortion for strings, integers, floats, bools, nested messages and repeated fields. The handler uses `FindString`/`FindInt32` and never sees JSON.
- *`BHandler` as a dispatch target.* A bus service name maps to a handler token; `MapVerb` turns a verb into a `what`, and the handler is unmodified. A verb nobody handles falls through `BHandler::MessageReceived` to the looper's default and returns a typed bus error immediately instead of hanging.
- *Request and reply.* `SendReply` maps onto a bus response with `corr`, and `B_MESSAGE_NOT_UNDERSTOOD` onto a typed error.
- *Publish and subscribe.* A `BMessage` published becomes an event and an event becomes a `B_BUS_EVENT` message. Not a BeOS idiom (BeOS used `StartWatching`) but cheap.
- *Identity.* The handler learns who called from the daemon (`uid1000:bus`), not from anything the sender wrote. I found and fixed a hole while writing this: a payload key `bus:from` was item 0 of the identity field the handler reads. Metadata now has the prefix stripped from payloads first (tested).

**Where it fit badly**

- *Ports and `BMessenger` are not capabilities here.* A `BMessenger` is a (port, token) pair meaningful only inside a namespace, and it is forgeable. Nothing in it maps to a bus service name or capability handle, which was the 0008 intuition. The only addressing that crosses the bus is a service name string plus a verb, and every verb must be mapped by hand.
- *The return address does not persist.* A handler that remembers `message->ReturnAddress()` and messages the sender later (a common BeOS pattern) cannot work: the reply port is private to one request and gone after the reply. Server push has to be a topic, which is a different program.
- *Replies must be synchronous within `MessageReceived`.* Our looper answers `B_NO_REPLY` when the handler returns without replying, and the bridge turns that into an error. A bus service may reply late, and Haiku lets a handler detach a message and reply later (`DetachCurrentMessage`, not implemented here). That is a gap in the kit as much as in the mapping, but a deferred-reply handler cannot be bridged today.
- *Timeouts mean different things.* BeOS timeouts are per call on the sender (`SendMessage(..., replyTimeout)`); the bus has no deadline field and the service side has no notion of one. The bridge invents a service-side reply timeout (default 10 s). When it fires the handler is still running, its late `SendReply` goes to a deleted port, and the work is not cancelled.
- *Head-of-line blocking.* The bridge serves one request at a time because a looper is single-threaded anyway. A slow handler stalls every other bus caller for as long as it runs. This follows from the serial design; the `slow@v1` test exercises the timeout, not a second concurrent caller. BeOS had the same property inside one team, but bus callers are independent processes that did not agree to share a queue.
- *Ordering.* Requests from one connection reached the handler in order (41 sent, 41 in order, tested). Across connections order is arrival order, as with a port. The bus outbox is strict priority across QoS classes (bus README), so a `user-interactive` request will overtake an earlier `background` one to the same service. BeOS ports are FIFO with no classes. I did not test this; it follows from the bus's documented delivery policy.
- *QoS has no BeOS equivalent.* The bridge hands the class to the handler as a string field (`bus:qos`) and then ignores it: the looper port is FIFO. Priority is advisory only.
- *Types.* JSON cannot carry null, empty arrays distinct from absent fields, mixed-type arrays, int8/16 vs int32, `uint*`, pointers, refs, messengers, or a message's `what`. Mixed arrays are rejected (`bad_payload`); null and empty arrays vanish; a one-item array looks like a scalar unless the handler calls `BusMarkArray`. A `BMessage` full of `BRect`, `BPoint` and refs (nothing drawing-related exists in our kits yet) would be mostly `$raw` base64.
- *Scale of the per-request cost.* One port created and deleted per request in a 128-port table, plus two extra threads per bridged app.
- *Not carried at all:* segments (bus fd passing; the C++ client does not do `SCM_RIGHTS`, so areas and bus segments are still unconnected), capability handles, replay.
- *Startup race.* A bridge started before `ReadyToRun` can deliver into a half-wired application (the same window the Stage A demo hit). The demo starts it in `ReadyToRun`.

**What this does and does not establish.** It shows that a Haiku handler can serve and publish on the bus with correct typed replies and clean errors, from a real `busd`, repeatedly. It does not show that BeOS messaging in general maps onto the bus: scripting suites, `BMessenger` persistence, deferred replies, `BMessageRunner`, `BRoster` and the registrar were not touched, and the bridge ran against our reimplemented `BLooper`, not Haiku's. The cost of the "ports are bus endpoints" design, as opposed to this edge adapter, is still unknown; this result argues that it would have to give up either port semantics (blocking, timed waits, `port_count`, delete-wakeups) or the bus's guarantees, because they differ in kind. Open question 7 (latency) is still open.
