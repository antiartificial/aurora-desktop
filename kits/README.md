# kits: BeOS kernel primitives on Linux userspace (Haiku Stage A, foundation)

Earlier this file was a one-line placeholder for a future Rust Interface Kit. That plan is unchanged and unrelated to this directory's current contents.

## What this is

A small C library, `libkitsos.a`, linkable from C++, that implements the BeOS/Haiku kernel-kit calls the Haiku messaging core (`src/kits/app`) makes. Decision 0008 (Stage A) lists them: ports, areas, semaphores and threads, reimplemented in userspace rather than in a kernel module.

**This is the primitives layer only.** There is no `BMessage`, no `BLooper`, no `BHandler`, no `BMessenger`, no `BApplication` and no libbe. Nothing here has been compiled against Haiku's kits. The Stage A claim it supports is narrow: the primitives those kits call can exist, work between processes, and pass their own tests.

Signatures follow Haiku's MIT-licensed public API (`headers/os/kernel/OS.h`, `support/Errors.h`, `support/SupportDefs.h`). The implementation is original. No Haiku or Vitruvian source was copied or linked, and `include/*.h` are our own reduced headers using Haiku's names and values.

## Layout

- `include/` `OS.h`, `Errors.h`, `SupportDefs.h`
- `src/` `shm.c` (shared state, deadlines, time), `port.c`, `area.c`, `sem.c`, `thread.c`
- `tests/test_kits.cpp` 10 test groups, 1594 checks, written in C++
- `demo/demo_two_process.cpp` the two-process proof

Build and run: `cmake -S . -B build -G Ninja && cmake --build build && ctest --test-dir build --output-on-failure`, or run `build/demo_two_process`.

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

## Next step

Build Haiku's `Message.cpp`, `Looper.cpp`, `Handler.cpp` and `Messenger.cpp` (taken per 0008's open question 1 on sourcing, which is still undecided) against these headers, and run Haiku's own messaging tests. Expect gaps: the survey found call sites by name, and this library has not been run under real kit code. After that comes the non-drawing `BApplication` question (app_server and registrar stubs, 0008 open question 6), then deciding whether ports should move onto the bus.
