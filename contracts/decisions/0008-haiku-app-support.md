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
