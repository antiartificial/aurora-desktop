# haiku-compat: does Haiku's own messaging source build against our kits?

Experiment for decision 0008 ("Stage A", open questions 1, 4, 5). Run 2026-11-06. Everything below was run; nothing is inferred.

**Headline: it is much closer than 0008 assumed.** Haiku's real `Message.cpp`, `Handler.cpp`, `Looper.cpp` and `Messenger.cpp` (plus 22 support files) compile and link against our `libkitsos` primitives after roughly 165 lines of shim and stubs. Two programs run:

- `test_flat`: a real Haiku `BMessage` is built, flattened, unflattened, re-flattened (byte-identical), and carried through one of our ports.
- `test_looper`: a real `BLooper` + `BHandler` + `BMessenger` do a synchronous send and get a reply, on our ports and threads. 30/30 runs pass.

What this does **not** show is in "Caveats". The most important: the source is the **Vitruvian (VOS) fork**, which is already a Linux/glibc port, not stock Haiku.

## Provenance and licence

Haiku sources are **not copied into this repo**. `CMakeLists.txt` and `try.sh` reference them in place under `HAIKU_SRC` (default `/home/arti/projects/Vitruvian`, read-only): `src/kits/{app,support,interface}`, `src/system/libroot2/KMessage.cpp` and `headers/`. They are MIT licensed, Copyright Haiku, Inc. and (for the fork's changes) The Vitruvian Project. Everything under this directory was written fresh: `shim/`, `stubs.cpp`, the two tests, the build files. The shim headers re-declare a few Haiku/VOS public API names (`thread_info`, `team_info`, `vref_id`, ...), which are MIT public interface.

## Run it

```
cd kits/haiku-compat
cmake -S . -B build -G Ninja && cmake --build build     # 37 steps, clean
build/test_flat ; build/test_looper
./try.sh <Haiku .cpp>      # syntax-only compile of one file with the same flags (MODE=-c OUT=x.o for an object)
```
Standalone project; it is not wired into `kits/CMakeLists.txt`. The existing suite still passes (`ctest`: 4/4).

## Output

`test_flat` (real Haiku `BMessage`, 6 fields incl. nested message and 5-byte blob):
```
FlattenedSize = 381
flat bytes (first 64 of 381):
  48 4d 46 31 74 73 65 74 01 00 00 00 ff ff ff ff
  ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff
  ff ff ff ff a9 00 00 00 06 00 00 00 05 00 00 00
  03 00 00 00 ff ff ff ff 00 00 00 00 01 00 00 00
magic = HMF1
round trip OK (0 failures)
```
Header reading: `HMF1` is Haiku's native message format magic (`MESSAGE_FORMAT_HAIKU`, '1FMH' little-endian); `74 73 65 74` is `what = 'test'`; `01 00 00 00` is flags (`MESSAGE_FLAG_VALID`); the `ff` runs are unset target/reply tokens and ports; `a9 00 00 00` is the data size (169); `06 00 00 00` is the field count.

`test_looper`:
```
Run() ...
Run() -> thread 2404802
IsValid=1
SendMessage(sync) -> 0 (B_OK), reply.what=gnop     # 'pong' printed as raw little-endian bytes
reply n=1, handler got=1
```

## What compiled

| Group | Files | Status |
|---|---|---|
| Priority four | `app/Message.cpp`, `Handler.cpp`, `Looper.cpp`, `Messenger.cpp` | compile, link, run |
| App support | `MessageAdapter`, `LooperList`, `TokenSpace`, `MessageQueue`, `DirectMessageTarget`, `MessageFilter`, `MessageUtils`, `PropertyInfo` | compile, link |
| Support kit | `String`, `List`, `Archivable`, `ArchivingManagers`, `ByteOrder`, `BlockCache`, `DataIO`, `Locker`, `StringList`, `Flattenable` | compile, link |
| Interface | `Rect`, `Point` | compile, link |
| System | `src/system/libroot2/KMessage.cpp` | compile, link |
| Compile only, not linked | `app/Application`, `AppMisc`, `Roster`, `Notification`, `InitTerminateLibBe` (needs `-D__VOS__`), plus the priority four with `-D__VOS__` | syntax-only clean after the second shim batch |
| Do not compile | `app/LinkSender`, `ServerMemoryAllocator` (23-24 errors: `team_usage_info`, `topologyInfos`, i.e. kernel info structs), all of `src/kits/storage` (see below) | BLOCKED |

26 objects link into `libhaiku_app_real.a`. Both `__VOS__` and non-`__VOS__` configurations compile; only the non-`__VOS__` one was linked and run.

## First contact (real errors, in order)

1. Our reduced headers alone: `Message.cpp` stops at `fatal error: config/HaikuConfig.h` (Haiku's `BeBuild.h`), then `type_code`, `vref_id`, `port_message_info`, `addr_t` have no definition. Count at that point: 140 errors, but only 4 distinct causes.
2. Putting `kits/include` on the include path **shadows Haiku's own `Message.h`** with ours, giving hundreds of `no declaration matches 'BMessage::GetString(...)'` errors. Our `Message.h`, `Handler.h`, ... must not be visible. `kits/include` is therefore PRIVATE to `kitsos`; compiled code sees only a shim `OS.h` wrapping ours.
3. Ours `SupportDefs.h` and `Errors.h` are reduced and not usable. Haiku's real ones are pure typedefs/constants and drop in; they are used instead.

## Gap inventory

Counted by hand from the compiler/linker logs; treat as +/- a few. STUB = trivial shim written. IMPLEMENT = small real code written. HOLLOW = links but does not work. BLOCKED = not attempted, structural.

### A. Compile time (headers, types, include paths): 31 items, all STUB

| Item | Where it bit | Fix |
|---|---|---|
| `config/types.h` (`__haiku_int8` ...; on Haiku `sys/types.h` includes it, glibc's does not) | `SupportDefs.h` | `shim/haiku_prelude.h`, force-included |
| `addr_t`, `saddr_t` | `Looper.h` | prelude |
| `area_id port_id sem_id team_id thread_id` (Haiku's `OS.h` owns them; ours keeps them in `SupportDefs.h`) | `OS.h` | `shim/OS.h` |
| `XATTR_NAME_MAX` | VOS `StorageDefs.h` | prelude includes `linux/limits.h` |
| `B_MIME_TYPE_LENGTH` (`Roster.h` relies on an earlier include) | `Messenger.cpp` | prelude includes `StorageDefs.h` |
| `vref_id` + 6 `*_vref` calls (VOS extension) | `Node.h`, `Entry.h`, `Message.cpp` | `shim/OS.h`, hollow bodies |
| `port_message_info`, `_get_port_message_info_etc` | `KMessage.h` | `shim/OS.h`, partly real |
| `B_SYSTEM_TEAM`, `thread_state`, `thread_info`, `get_thread_info`, `exit_thread`, `debugger` | `Handler.cpp`, `Looper.cpp`, `MessageAdapter.cpp` | `shim/OS.h` |
| `team_info`, `B_CURRENT_TEAM`, `get_team_info`, `kill_thread`, `debug_printf`, `UNIMPLEMENTED` | `Application.cpp`, `AppMisc.cpp`, `Roster.cpp` | `shim/OS.h`, second batch |
| 9 missing include directories: `config_headers/` (`tracing_config.h`), `private/interface` (`utf8_functions.h`), `private/system` (`syscalls.h`), `private/system/arch/x86_64` (`arch_config.h`), `private/storage` (`storage_support.h`), `os/drivers` (`KernelExport.h`), `os/interface` (`Rect.h`), `os/locale`, `headers/` (`config/HaikuConfig.h`) | various | `-I` list in `CMakeLists.txt` |

The pattern: Haiku's source needs the **full** `OS.h`. Our `OS.h` has 21 calls; Haiku's has 86 `extern` declarations. Everything in this section is the difference. Easiest real fix: adopt Haiku's `os/kernel/OS.h` as the header and implement or hollow-stub what it declares, instead of maintaining our own subset.

### B. Link time: about 36 symbols stubbed or implemented, 14 unresolved

| Symbol(s) | Class | Notes |
|---|---|---|
| `entry_ref` ctor/dtor/`operator=` (2 ctor forms), `node_ref` (4 members) | IMPLEMENT (reduced) | no vref, no team/real_device handling. Real ones live in `storage/Entry.cpp` which cannot build. |
| `BPrivate::current_team` | IMPLEMENT | `getpid()` |
| `debugger` | IMPLEMENT | prints and aborts |
| `_get_port_message_info_etc` | HOLLOW | size only; our ports record no sender (uid/gid/team all 0). Needed for the security-relevant sender identity in KMessage. |
| `get_vref_dev`, `acquire_vref`, `release_vref` | HOLLOW | `get_vref_dev` returns `B_INVALID_DEV`, which switches every vref path in `Message.cpp` off. No message can carry an fd. |
| `exit_thread` | HOLLOW | `pthread_exit`; unverified against our thread layer |
| `get_thread_info` | HOLLOW | always `B_ERROR`; only the looper's thread-name lookup uses it |
| `_kern_transfer_area` | HOLLOW / BLOCKED | `B_NOT_SUPPORTED`; used for cross-team area handover of large messages |
| `load_add_on`, `get_image_symbol`, `_get_next_image_info` | HOLLOW | `BArchivable` instantiate-by-name cannot work |
| `be_app`, `be_app_messenger`, `be_roster` (globals) | IMPLEMENT | plain globals; `be_app` is `NULL` |
| `BRoster` (ctor, dtor, `GetAppInfo`, `GetRunningAppInfo`, `FindApp`), `app_info`, `BLaunchRoster` (3) | HOLLOW / BLOCKED | registrar not ported; every call returns `B_ERROR` |
| 14 unresolved: `BPath` (4), `BEntry` (4), `BFile` (2), `BNode` (1), `BAppFileInfo` (3) | BLOCKED | polymorphic, so a stub needs ~50 virtuals. Referenced from `Archivable.cpp` (instantiate-by-name) and `Message.cpp` (`BPath` for refs). Linked with `-Wl,--unresolved-symbols=ignore-all`, so a call into any of them **crashes**. Neither test touches them. |
| Real source added to satisfy a link: `ArchivingManagers.cpp` (16 symbols), `ByteOrder.cpp` (`swap_data`), `PropertyInfo.cpp` | not a gap | compiled in place |

### C. Structural walls (not attempted)

1. **Storage kit** (`BPath`, `BEntry`, `BFile`, `BNode`, `BDirectory`, node monitor). `Entry.cpp` has 27 `_kern_*` calls, `Node.cpp` 22, `File.cpp` 12. `syscalls.h` needs `object_wait_info` from the full `OS.h`; `File.cpp` needs `umask.h`. This is the Haiku syscall layer; VOS maps it somewhere (libroot2) that was not examined.
2. **Registrar** (`BRoster`, `BLaunchRoster`, `be_roster`, `BApplication` launch/lookup). `Roster.cpp` compiles but would talk to a registrar that does not exist here. This is the single-launch/team-registry piece 0008 already lists.
3. **app_server link** (`BApplication` ctor connects to it; `AppServerLink`, `ServerMemoryAllocator`, `LinkSender` do not compile: `team_usage_info`, `topologyInfos`). Not on the path of a non-drawing app, but `Application.cpp` references it.
4. **`InitTerminateLibBe.cpp`**: a constructor that initialises the message cache, looper list, token space and roster. Not linked; tests call `BMessage::Private::StaticInit()` by hand. Forgetting it gives a segfault in `BBlockCache::Get` on the first heap `BMessage` (seen).
5. **fd-passing / vrefs**: Haiku's cross-team handles become VOS vrefs here. Stage A's bus fd-passing requirement (0001) has no counterpart in this path yet.

## Caveats

- **The source is the VOS fork, not stock Haiku.** `headers/posix` is nearly empty (glibc is used), storage defs use Linux constants, `entry_ref` carries `team`/`real_device` fields, `Message.cpp` has `_HandleMessageVRefs`, and `__VOS__` forks about 20 places in the files examined. That is why it compiled this easily. Stock Haiku's `src/kits` against our headers was **not** tested; expect a worse result there (this resolves 0008 question 1 only in the sense that the VOS fork is the realistic source, and its delta is not fully understood).
- **The flat-format result is a self-round-trip.** Haiku's code reads what Haiku's code wrote, on our ports. No bytes produced by a real Haiku/VOS system were compared, so interop is unproven. The header decodes as the Haiku format by inspection only.
- **Loopers and messengers: one in-process sync round trip.** Not tested: cross-process, `BApplication`, quit/lock ordering, message filters, scripting, big messages (area transfer is hollow), drop/reply-to plumbing beyond `SendReply`. `exit_thread` and `get_thread_info` are hollow, so `Quit()` from non-looper threads is the only exit path exercised.
- `-w` silences warnings in Haiku files (many `-Waddress-of-packed-member`: Haiku's packed message header taken by pointer; x86-64 only, benign here, UB-adjacent elsewhere).
- Our existing reimplemented `libkitsapp` is unaffected and unused here.

## Realistic estimate to finish

Goal: replace `kits/src/app/*` with Haiku's real files for a non-drawing `BApplication` that runs `kits/demo/haiku_app.cpp`.

| Step | Work | Estimate |
|---|---|---|
| Replace our `OS.h` with Haiku's declarations; hollow or real bodies for the ~65 extras (`get_team_info`, `get_thread_info`, `kill_thread`, `suspend_thread`, `get_next_*_info`, sem/port info, `on_exit_thread`) | mostly mechanical; port/sem/thread info needs our shared table extended | 1-2 days |
| Port sender identity in ports (`_get_port_message_info_etc`) | extend the ring entry header with uid/gid/team | 0.5 day |
| `InitTerminateLibBe` as a real constructor; link `Application.cpp`, `AppMisc.cpp` | small; fix `get_team_info`-based team lookups | 1 day |
| Non-drawing `BApplication`: cut the app_server connection (a build flag or a replacement `_ConnectToServer`) and a minimal registrar/`BRoster` | the real design work; 0008 question 6 | 3-5 days |
| Storage kit minimum (`BEntry`, `BPath`, `entry_ref`, `BFile` for `BAppFileInfo`, `BNode::InitCheck`) | either port `_kern_*` to POSIX or write a thin version; `BPath`/`entry_ref` are easy, `BNode` attributes are not | 2-5 days |
| `BMessage` over big payloads (`_kern_transfer_area` -> our `area_export_fd`/`clone_area_fd`) and vrefs -> bus fd-passing | needs the bus fd-passing from 0001 | 3-5 days, depends on bus work |
| Run Haiku's own test suite for the app kit (`src/tests/kits/app`) | unknown; likely more gaps | open |

About 2-3 weeks for the non-drawing path on VOS source, with the registrar and storage decisions as the big risks. This is an estimate from one afternoon's evidence, not a measurement. Stage B (drawing) is untouched by this experiment.
