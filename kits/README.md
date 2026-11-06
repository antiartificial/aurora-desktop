# kits: BeOS primitives and a messaging core on Linux userspace (Haiku Stage A)

Earlier this file was a one-line placeholder for a future Rust Interface Kit. That plan is unchanged and unrelated to this directory's current contents.

## What this is

A small C library, `libkitsos.a`, linkable from C++, that implements the BeOS/Haiku kernel-kit calls the Haiku messaging core (`src/kits/app`) makes. Decision 0008 (Stage A) lists them: ports, areas, semaphores and threads, reimplemented in userspace rather than in a kernel module.

**Two layers.** `libkitsos.a` is the primitives layer. `libkitsapp.a` sits on it and is a messaging core: `BMessage`, `BHandler`, `BLooper`, `BMessenger`, `BApplication`. **This is a reimplementation of the API surface, written from scratch. It is not Haiku's kits and not libbe.** Nothing here has been compiled against Haiku's `src/kits/app`; that is a later step and will expose gaps. The claim it supports is narrow: a Haiku-shaped non-drawing application can run on the primitives, and two processes can talk through its looper with synchronous replies.

The API follows Haiku's MIT-licensed public interface (`headers/os/app/*`, `support/*`). The implementation and the flat message format are original; no Haiku or Vitruvian source was copied. The flat format is not Haiku's, so flattened messages are not exchangeable with a real Haiku system.

Signatures follow Haiku's MIT-licensed public API (`headers/os/kernel/OS.h`, `support/Errors.h`, `support/SupportDefs.h`). The implementation is original. No Haiku or Vitruvian source was copied or linked, and `include/*.h` are our own reduced headers using Haiku's names and values.

## Layout

- `include/` `OS.h`, `Errors.h`, `SupportDefs.h`
- `src/` `shm.c` (shared state, deadlines, time), `port.c`, `area.c`, `sem.c`, `thread.c`
- `src/app/` `Message.cpp`, `Handler.cpp`, `Looper.cpp`, `Messenger.cpp` (also the port wire format and `BMessage::SendReply`), `Application.cpp`; `include/` also has `Message.h Handler.h Looper.h Messenger.h Application.h AppDefs.h TypeConstants.h`
- `tests/test_kits.cpp` 10 test groups, 1594 checks, primitives
- `tests/test_app.cpp` 9 test groups, 151 checks, messaging core (includes two re-exec'd child processes)
- `demo/demo_two_process.cpp` the primitives proof
- `demo/haiku_app.cpp` the Haiku-style two-process application (below)
- `src/bus/`, `include/Bus*.h` the bus client, JSON, and the BLooper-to-bus bridge ("Bus bridge" below); `demo/haiku_ledger.cpp`, `demo/bus_demo.sh`, `tests/test_bus.cpp`

Build and run: `cmake -S . -B build -G Ninja && cmake --build build && ctest --test-dir build --output-on-failure`, or run `build/demo_two_process`, `build/test_app`, `build/haiku_app`.

## Mapping

| BeOS primitive | Linux substrate |
|---|---|
| port | One shared file (`/dev/shm/aurora-kits-<uid>-<ns>`, namespace from `AURORA_KITS_NS`, default `default`) mapped `MAP_SHARED` by every process. It holds a port table with a process-shared robust mutex, two process-shared condvars per port, and a 256 KiB byte ring per port. |
| area | `memfd_create` with `F_SEAL_GROW \| F_SEAL_SHRINK`, `mmap(MAP_SHARED)`. A registry in the shared file records owner pid and fd number. |
| semaphore | Process-local counter, mutex and condvar (see deviations). |
| thread | `pthread` (detached), id = kernel tid, parked on a gate until `resume_thread`. |

### Why shared memory plus a mutex and condvars for ports

The candidates were Unix sockets, POSIX message queues and shared memory with futex-style sync.

- Haiku code passes bare `port_id` integers around, and a child or an unrelated process uses the same integer. Sockets would need an fd per port, which does not survive that without a broker daemon. A shared table makes the id itself global, as in BeOS, with no daemon.
- Ports need `port_count`, `get_port_info`, blocked-writer-on-full, and "delete wakes every blocked reader". A condvar on a shared table gives all of these directly. With sockets each is awkward or impossible: `port_count` needs `FIONREAD` and does not count messages, and a delete cannot wake a peer's `read`.
- POSIX message queues give fixed global limits (`msg_max`, `msgsize_max`, typically 10 messages of 8 KiB by default) and their per-user queue limit is too small for loopers.
- `pthread_mutex_t` and `pthread_cond_t` with `PTHREAD_PROCESS_SHARED` and `CLOCK_MONOTONIC` are futex-backed on Linux, so timed waits are exact. The mutex is `ROBUST`, so a process killed while holding it does not wedge the table.

Cost: all ports share one lock (fine at looper rates, unmeasured), and a port is visible to every process of the same uid and namespace. That is the same weakness 0008 flags for `BMessenger`. The Stage A bus mapping (ports as bus endpoints) is a different substrate and is not built.

## Haiku semantics implemented

Matched, and exercised by tests:

- `create_port(capacity, name)`; `write_port_etc` blocks when the queue is full; `read_port_etc` blocks when empty. Messages carry `int32 code` plus a payload.
- Timeouts: `B_RELATIVE_TIMEOUT`/`B_TIMEOUT` and `B_ABSOLUTE_TIMEOUT` give `B_TIMED_OUT`. A relative timeout of 0 gives `B_WOULD_BLOCK`. `B_INFINITE_TIMEOUT` waits forever.
- `delete_port` wakes blocked readers and writers with `B_BAD_PORT_ID`. `close_port` fails later writes with `B_BAD_PORT_ID`, lets readers drain the queue, then returns `B_BAD_PORT_ID`.
- `port_count` (queued messages), `get_port_info` (capacity, queue_count, total_count read, name, team), `set_port_owner`, `find_port`, `port_buffer_size[_etc]`.
- Port ids carry a generation, so a deleted port's id does not alias a new port. Stale ids give `B_BAD_PORT_ID`.
- `create_area` returns an id and address, sealed memfd, page-rounded size. `get_area_info` reports size, protection, team, address (address only for areas mapped in this process). `set_area_protection` calls `mprotect` (tested with a real SIGSEGV).
- Areas are shareable between unrelated processes: `clone_area(name, &addr, spec, prot, source_id)` reopens the owner's memfd via `/proc/<pid>/fd/<n>` (same uid), which reaches the same inode and seals. Tested through fork+exec.
- Semaphores: counting, atomic multi-count acquire, `B_RELATIVE_TIMEOUT`/`B_ABSOLUTE_TIMEOUT`, `B_WOULD_BLOCK`, `B_BAD_SEM_ID` on a deleted sem (and for waiters when it is deleted).
- Threads are born suspended and run only after `resume_thread`. `find_thread(NULL)` is the kernel tid, stable per thread, and equals the id `spawn_thread` returned. `find_thread(name)` and `wait_for_thread` work.
- Errors and typedefs: Haiku names and values (`B_BAD_PORT_ID = INT_MIN + 0x2000 + 8`, `B_TIMED_OUT = -ETIMEDOUT`, and so on).

### Approximated or simplified

- **Semaphores are not `sem_t`.** They use mutex plus condvar, because `sem_t` cannot do atomic multi-count acquire, wake waiters on delete with `B_BAD_SEM_ID`, or `B_RELEASE_ALL`. Not FIFO-fair as Haiku's are. `B_RELEASE_ALL` grants exactly what current waiters want. They are process-local; Haiku's are system-wide.
- **Message size and truncation.** One message is limited to about 256 KiB (the ring size) or `write_port` returns `B_BAD_VALUE`. Capacity is limited to 65535. An oversized read truncates, consumes the message, and returns the bytes copied; Haiku's return value in that case was not checked.
- **Port table is finite**: 128 ports, 512 areas, 512 live threads, 4096 semaphores. Slots owned by dead processes are reclaimed (`kill(pid, 0)`) only when the table is full. A crash leaves ports in `/dev/shm` until reuse or `_kits_unlink_namespace()`.
- **Area protection is per mapping.** `set_area_protection` changes only the calling process's mapping, and returns `B_BAD_VALUE` for an area it has not mapped. `lock` is ignored. `B_EXACT_ADDRESS` uses `MAP_FIXED_NOREPLACE`; `B_BASE_ADDRESS` is a hint; `B_CLONE_ADDRESS` is treated as any.
- **`delete_area`** releases the caller's mapping and registry entry. Clones are separate ids, as in Haiku. The memory lives until every mapping and fd is gone.
- **`clone_area` by id needs the owner process alive** (its fd is what is reopened) and same uid. If `ptrace_scope` or LSM policy ever blocks `/proc/<pid>/fd` opens, `clone_area` fails; the fd-passing route below does not depend on it.
- Priorities and `B_CAN_INTERRUPT` are accepted and ignored; no signal interruption (`B_INTERRUPTED` is never returned).
- Thread `used`-slots for threads never passed to `wait_for_thread` are recycled once finished.

### Extensions (not Haiku API)

- `area_export_fd(area)` and `clone_area_fd(name, &addr, spec, prot, memfd)`: the memfd route the bus uses (0001, `bus/README.md` "Segments", `SCM_RIGHTS`). `clone_area_fd` refuses an fd without `F_SEAL_GROW|F_SEAL_SHRINK`, matching the bus's sealed-only rule. Tested by exporting an area fd and mapping it again. Sending that fd over a real bus socket was not done.
- `_kits_unlink_namespace()` for tests and demos.

## Not implemented

`get_next_port_info`, `get_next_area_info`, `area_for`, `find_area`, `suspend_thread`, `kill_thread`, `exit_thread`, `get_thread_info`, `rename_thread`, `set_thread_priority`, `snooze_until`, `acquire_sem` interruption, `set_sem_owner`, `get_sem_info`, `find_sem`, `read_port` flags beyond timeouts, port message sender info (`_get_port_message_info_etc`), teams (`team_id` is just a pid), `B_CLONE_ADDRESS` semantics, resource limits. Anything libbe needs that is not in the 21 calls listed in the task is also absent.

## Relation to decision 0008, Stage A

0008 says Stage A maps ports onto the message bus, areas onto memfd, semaphores onto POSIX semaphores and threads onto pthreads. This delivers the primitives with the right signatures and the multi-process behaviour, but differs from 0008 in two places:

1. **Ports are not on the bus.** They are a standalone shared-memory queue. The 0008 target, "a `BLooper`'s port is a bus endpoint", is not met; a later layer could put a bus-backed port behind the same signatures. 0008 open question 7 (latency of mapping every port write onto a bus message) is untouched. No latency was measured here.
2. **Semaphores use condvars, not `sem_t`**, for the reasons above.

Areas do match 0008's memfd mapping, including sealing.

## Messaging core above the primitives

- **`BMessage`**: `what`, typed named fields with several items per name: int8/16/32/64, bool, float, double, string, raw data (any `type_code`), nested `BMessage`. `Add*`/`Find*` return Haiku `status_t` (`B_NAME_NOT_FOUND`, `B_BAD_TYPE`, `B_BAD_INDEX`). `Flatten`/`Unflatten` to a buffer; `Unflatten` bounds-checks and rejects truncated or corrupt input. `Equals`. Four-character `what` codes (`-Wno-multichar`).
- **Wire**: a message crosses a port as a small header (handler token, reply port, reply token, sender pid) plus the flat `BMessage`. Port codes `_PMS` (message) and `_QIT` (loop quit).
- **`BHandler`**: name, owning looper, token, `SetNextHandler`. Default `MessageReceived` forwards along the chain; `AddHandler` links each handler to its looper, so an unhandled message ends at the looper, whose default replies `B_MESSAGE_NOT_UNDERSTOOD` to a waiting sender.
- **`BLooper`**: creates one of our ports (capacity 200), runs the loop on a `spawn_thread` thread, holds a recursive lock while dispatching, `AddHandler`/`RemoveHandler`, `SetPreferredHandler`, `PostMessage` (with handler and reply-to), `Quit` (self: finish message, stop, delete; other thread: stop, join, delete), `B_QUIT_REQUESTED` through `QuitRequested`. A handler that never replied to a waiting sender gets an automatic `B_NO_REPLY` reply; messages still queued at shutdown get one too. Looper deletion deletes its handlers, as Haiku does.
- **`BMessenger`**: (port, token). Built from a handler/looper, from a signature, or from a raw (port, token) pair. `SendMessage` async, with a reply address, and synchronous (`SendMessage(msg, &reply, deliveryTimeout, replyTimeout)`: a private one-slot reply port per call). A synchronous send from the target looper's own thread returns `B_ERROR` instead of deadlocking. `IsTargetLocal`, `Target`, `LockTarget`, `Team`.
- **`BApplication`**: a `BLooper` with a signature, `Run()` on the calling thread, `ReadyToRun`, `ArgvReceived`, `QuitRequested`, `Quit`, `be_app`, `be_app_messenger`. One per process.

### What is stubbed so `BApplication` needs no app_server and no registrar

- **No registrar.** There is no team registry, no `BRoster`, no single-launch, no `be_roster`. `BMessenger(signature)` finds the app by a port name derived from the signature (`A:` plus the FNV-1a hash in hex; Haiku port names are 31 characters, signatures are longer), via `find_port`. Consequences: the first matching live port wins, two instances of one signature are not prevented, and a port left by a crashed process is found until its slot is reused.
- **No app_server.** `BApplication` opens no connection and creates no server-side app. Nothing draws; there are no windows, views, cursors, or `BBitmap`s.
- **argv without a launcher.** Haiku delivers `B_ARGV_RECEIVED` from the registrar's launch message. Here `Run()` reads `/proc/self/cmdline` and posts it to itself if `argc > 1`, then posts `B_READY_TO_RUN`.
- **Lifecycle messages go to the application itself**, never the preferred handler (a bug found when the demo set one). **Early messages.** The port exists from the constructor, so a peer may send before `Run()`. Such messages are dispatched before `ReadyToRun`. Wire handlers up in the constructor. (The demo hit this as an intermittent failure and now does so.)

### Not implemented, versus real libbe

- No scripting (`B_GET_PROPERTY`, `ResolveSpecifier`, suites), no `BArchivable`/`BString`/`BList`, no `BMessageFilter`, `BMessageQueue`, `BMessageRunner`, `BInvoker`, `BClipboard`, `BRoster`.
- `BMessage` lacks: `Replace*`, `Has*`, `Find*Pointer/Ref/Point/Rect`, `FindInt8`-style `uint*` variants beyond the ones listed, `AddFlat`, `AddMessenger`, `AddRef`, `AddPointer`, `GetInfo` by index, `Rename`, `Flatten(BDataIO*)`, `ForEach`, drop-message fields, `SendReply` with a reply-to-reply wait, `PreviousMessage`, `DetachCurrentMessage`/`DetachSpecifiers`. The flat format is ours; it has no versioning, no byte-order handling, no compression, no small-message fast path. Fields are `std::vector`/`std::string`; a name holds one type; field order is insertion order.
- `BLooper`: no `LockLooper*`/`BAutolock` helpers, no `SetCommonFilterList`, no `MessageQueue`, no `Loop`-per-thread bookkeeping beyond a registry, no `WaitForQuit`, no deadlock debugging, no thread-name/priority change after creation. Messages are always dispatched from the port; `PostMessage` to oneself takes the port round trip, not a shortcut queue. Handler-list changes need the caller to hold `Lock()`, and nothing enforces it. The looper deleting a handler mid-dispatch is not guarded.
- `BMessenger`: no `SetTo`, no team-targeted signature lookup (`team` is ignored), no `BMessenger(const BHandler*)` from another process, no delivery priority, no `B_TIMED_OUT` mapping for a dead looper whose port slot is reused. `LockTarget` and `Target` race with looper deletion (registry lookup, no reference).
- Capacity limits are the primitives' limits: 128 ports total in a namespace, so each concurrent synchronous send holds one for its duration, and a message is limited to about 256 KiB.
- Ports are not on the bus (see the primitives section); a looper's port is reachable by any same-uid process in the namespace.
- Haiku's `BMessage` and `BLooper` run under `app_server`-era assumptions: the team/thread-per-looper model, `BMessage` ownership and reply-wait semantics (`fReplyTo`, `fPreferred`, `_set_reply_to`), and `BHandler` filters. None of that was ported; only the observable behaviour listed above is tested.

### Demo: `haiku_app`

One binary, three modes. With no arguments it forks two processes: `--serve` is `LedgerApp : BApplication` with `LedgerHandler : BHandler` keeping a ledger; `--client` finds it with `BMessenger(signature)` and sends `MSG_ADD_ENTRY`, `MSG_LIST_ENTRIES`, a malformed message and an unknown one, each as a synchronous send with `BMessage reply`, then `B_QUIT_REQUESTED`. The server prints each entry; the client checks the replies (total 1849, three entries nested in the list reply) and the launcher checks both exit codes.

## Next step

Compile Haiku's own `Message.cpp`, `Looper.cpp`, `Handler.cpp` and `Messenger.cpp` (sourcing question, 0008 open question 1, is still undecided) against the primitives, in place of this reimplementation, and run Haiku's own messaging tests. Expect gaps. Then deciding whether ports should move onto the bus, and the app_server question for Stage B.

## Bus bridge: a Haiku application on the Aurora bus

`libkitsbus.a` (`src/bus/`, headers `BusJson.h BusClient.h BusBridge.h`) makes a `BLooper` a bus participant. It tests the claim in decision 0008 that the bus accommodates BeOS messaging. Findings are in 0008's section "Result: does BeOS messaging fit the bus?". Short version: the serving direction works with an unmodified idiomatic handler; ports are still not bus endpoints, and the bridge is an adapter at the edge.

- **`BusJson`** (288 lines): a JSON value, reader and writer. Int64 stays int, `1.0` stays double, depth cap 64, `\u` escapes with surrogates. Rejects malformed input with a message.
- **`BusClient`**: Unix socket, a reader thread, `Connect`, `Register`, `Subscribe`, `Publish`, `Call` (blocking with timeout, error envelopes reported as `B_ERROR`), `Reply`, `ReplyError`. Not supported: segments (`SCM_RIGHTS`), `caps`, replay `origin`; unknown envelope fields are dropped; an fd the daemon passes would be left unread.
- **`BusBridge(looper, handler, service)`**: `MapVerb("add@v1", MSG_ADD)`, `Start()`, `Publish(topic, msg)`, `Subscribe(glob)`, `Call(service, verb, msg, &reply)` (the other direction, a BMessenger-shaped synchronous call; works, tested), `Stop()`.

### How a request flows

1. The reader thread parses the line and queues a request. A second thread serves the queue **one request at a time**, so requests reach the looper in arrival order. (A separate thread so the reader stays free to read responses to `Call()`s made by handlers.)
2. The payload becomes a `BMessage` (mapping below). `what` is the `MapVerb` code, or `B_BUS_REQUEST` for an unmapped verb. Fields `bus:verb`, `bus:from` (the daemon's identity of the caller, not forgeable by the payload: tested) and `bus:qos` are added.
3. The serving thread does `BMessenger(looper port, handler token).SendMessage(msg, &reply, ...)`: the existing synchronous send with a private reply port per request. The looper dispatches to `BHandler::MessageReceived` on its own thread, and `SendReply` goes back through that reply port.
4. The reply becomes a response envelope. `B_MESSAGE_NOT_UNDERSTOOD` (including the looper's automatic answer to an unhandled `what`) becomes error `not_understood`; `B_NO_REPLY` (handler returned without replying) becomes error `no_reply`; no reply within `SetReplyTimeout` (default 10 s) becomes `timeout`; an unrepresentable payload becomes `bad_payload` without reaching the handler.

So the real path is bus -> reader thread -> queue -> serving thread -> **shared-memory port** -> looper thread. The looper's port is untouched and still reachable by any same-uid process in the namespace, around the bus.

### JSON to BMessage

| JSON | BMessage field |
|---|---|
| string | `B_STRING_TYPE` |
| integer that fits int32 | `int32`; otherwise `int64` (anything past int64 becomes a double) |
| number with `.`/`e` | `double` |
| `true`/`false` | `bool` |
| object | nested `BMessage` (`what` 0) |
| array | repeated field of one name. Numbers: all ints, widest of int32/int64; any double makes all doubles. Strings, bools, objects: one type each. |
| top-level non-object payload | field `value` |
| absent payload | empty message |

BMessage to JSON: int8/16/32/64 to integer, float/double to number, bool, string, nested message to object, a field with more than one item to an array, anything else (raw data, unknown types) to `{"$raw":{"type":"BLOB","base64":"..."}}`. Fields named `bus:*` never leave. `BusMarkArray(&msg, "name")` writes a one-item field as an array.

### Lossy cases (all of them I know of)

- **Null** is dropped: a BMessage cannot hold it, so `{"a":null}` is the same as `{}`. A `NaN`/`Inf` double leaves as `null`.
- **Empty arrays** vanish. **One-item arrays** arrive indistinguishable from scalars (inbound there is no marker; outbound `BusMarkArray` is the handler's job).
- **Mixed arrays are refused** (`bad_payload`): `[1,"a"]`, `[true,1]`, `[null]`, nested arrays, `[{...},2]`. BMessage has one type per name. Int and double mix is promoted to double.
- **int8/int16/int32 are one JSON number**: a reply field sent as int8 returns as int32 if it round-trips. `float` goes out as the double nearest the float (`0.1f` prints 0.10000000149011612).
- **Integers past 2^53** are exact through this bridge but not through consumers that parse JSON numbers as doubles.
- **A reply's `what` is lost** apart from the three special values above. A nested message's `what` is lost. Field order is kept; duplicate JSON keys keep the last.
- **Embedded NUL in a string** truncates it. Unflattened BMessage types we do not have (pointer, ref, messenger, point, rect) cannot be added; `AddData` with an unknown type is the only escape and goes out as `$raw`.
- **Ugly compromises, stated:** the verb table (`MapVerb`) must be written by hand, because a verb is a string and `what` is a uint32; unmapped verbs share one `what` plus a string field. Metadata rides in-band as `bus:*` fields. `B_NO_REPLY` becomes an error because a bus requester would otherwise wait forever. The reply timeout is a policy this bridge made up; neither side specifies one.
- `BusBridge::Call` from a handler of the service being called deadlocks until the timeout (the serving thread waits on the looper, the looper on the response). `Subscribe` posts events from the reader thread with `PostMessage`, which can block if the looper's port is full.

### Run it

`cmake --build build && ctest --test-dir build` builds `busd` and `bus` with Go into the build directory (skipped if Go is absent: the `bus_bridge` test then runs only the pure groups). `demo/bus_demo.sh [build-dir]` starts busd, starts `haiku_ledger` (a `BApplication` subclass with a `BHandler`, idiomatic `switch (message->what)`), drives it with `bus send haiku-ledger add@v1 '{"label":"coffee","cents":350}'` and `bus sub 'ledger.**'`, checks the replies and events, and cleans up. `tests/test_bus.cpp` has 206 checks: JSON round trips and malformed input, conversion in both directions, and, against a real busd, request/reply, unhandled `what`, `no_reply`, `timeout`, malformed payloads and raw garbage on the socket, ordering of 41 requests, publish to a subscriber, event to the handler, `Call`, and a duplicate service name refused.

Not done: latency is unmeasured; concurrent callers are serialised (a slow handler blocks every bus caller; by design, not measured); segments and caps are not carried; the bridge was exercised with our `BLooper`, not Haiku's own.

## Real Haiku kit code in the running stack (`haiku-compat/`)

`build/haiku-compat/haiku_ledger_real` is the ledger bus service on Haiku's OWN `BMessage`, `BHandler`, `BLooper` and `BMessenger` (Vitruvian `src/kits/app/*.cpp`, MIT, compiled in place and never copied). It uses no `BApplication`: a `BLooper` plus a `BHandler` and the bridge. Same handler source as `haiku_ledger` (`demo/ledger_handler.h`), same bridge source (`src/bus/`, compiled twice, once per kit). `demo/bus_demo_real.sh` drives it with the Go `bus` CLI (ctest `bus_demo_real`).

```
bus send haiku-ledger-real add@v1 '{"label":"coffee","cents":350}'
{"total":350,"handled_by":"Haiku BHandler::MessageReceived on Haiku's real BLooper (no BApplication)"}
```

- Linked into the binary: Haiku `app/{Message,Handler,Looper,Messenger,MessageAdapter,LooperList,TokenSpace,MessageQueue,DirectMessageTarget,MessageFilter,MessageUtils,PropertyInfo}`, `support/{String,List,Archivable,ArchivingManagers,ByteOrder,BlockCache,DataIO,Locker,StringList,Flattenable}`, `interface/{Rect,Point}`, `system/libroot2/KMessage` (25 files), plus our `haiku-compat/stubs.cpp` and `real_init.cpp` and `libkitsos`.
- No `--unresolved-symbols=ignore-all`. `BPath` is defined (real for string paths, hollow for `entry_ref`). `BEntry`, `BFile`, `BNode` and `BAppFileInfo` are referenced only by `BArchivable` instantiate-by-name; objects are built with `-ffunction-sections` and linked with `--gc-sections`, so those functions are dropped and a link error, not a crash, appears if something reachable ever needs them.
- `InitTerminateLibBe.cpp` is not used (it also starts the registrar and locale kit). `real_init.cpp` is a constructor(101) in the executable that calls `BMessage::Private::StaticInit()` and registers the fork handlers, as libbe does.
- Difference from our kit: Haiku's default `BHandler::MessageReceived` does not answer an unknown verb, so the bus error is `no_reply`, not `not_understood`.

Remaining before a real `BApplication`: see `haiku-compat/README.md` and decision 0008 "Update: Haiku's own kit code now runs in the live stack".
