# 0012. Windowed apps: launched, not invoked, and not yet sandboxed

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Every app in `apps/` before this record is a document transformer: it takes a file and a verb, runs inside a bubblewrap domain as `apps/<app>/run --verb NAME` with a request JSON on stdin, and prints a result JSON on stdout (`apps/magick/run`). The dock lists those apps and nothing else.

The Haiku Interface Kit programs in `kits/interface/` (`iface_controls`, `iface_text`, `iface_list`, `iface_menu`, `iface_layout`, `iface_browser`, and the kitchen sink) are the GUI apps, and the dock knew nothing about them. They are CMake executables, not manifests.

Decision 0008 left open how a Haiku app joins the desktop. This record answers the packaging half: how a windowed app is declared, found and listed.

## Decision

### A windowed app is not a verb

`[app] windowed = true` marks an app that opens its own window and is launched directly. `[app] exec = "<path>"` names the executable; a relative path resolves against the app directory (`apps/<id>/../../kits/build/interface/iface_text` for the Haiku apps), an absolute path is used as written. The schema refuses `windowed = true` without `exec`, and refuses `exec` without `windowed = true`. A windowed app may also declare `[[verb]]` entries.

It is not modelled as a verb because the verb contract does not describe it:

- A verb is one request and one reply. A window runs for minutes and ends when the user closes it. There is no result to return.
- A verb takes a document and produces a document, and that is what the provenance index records. A window that edits nothing and exports nothing has no output to record.
- A verb is invoked with its inputs already granted. A windowed app has no inputs at launch, so `accepts` and `input` have nothing to say.
- A verb's lifetime is bounded by the controller, which can time it out and tear the domain down. A window's lifetime belongs to the user.

Forcing a window into `run --verb` would mean a verb whose "result" is an exit code minutes later. That makes the contract lie about every other verb.

`registry.apps@v1` now returns `windowed`, `exec` (the resolved path) and `exec_problem` for each app. `exec_problem` is non-empty when the file is missing or not executable. The Haiku targets are skipped when `HAIKU_SRC` is absent, so a manifest can exist for a binary that was never built. The registry reports that as `not built: <path> does not exist`, `aurora apps` prints `windowed: UNAVAILABLE, ...`, and `aurora-up` prints a count. A client must not offer such an app as launchable.

### Windowed apps launch unsandboxed

**A windowed app launches as a plain host process, outside the domain machinery that is Aurora's entire security argument.** It has the user's full filesystem access, the user's network, the user's environment and the user's process rights. It is not in a bubblewrap domain, it has no cgroup, no mount namespace, no capability grant and no approval gate. The manifest's `[capabilities]` cannot restrict it, which is why the windowed Haiku manifests leave `[capabilities]` empty: a declared path nobody enforces would read as a promise. `iface_browser` can read everything the user can read, and runs `aurora query` and `aurora open` as child processes.

This is the same posture as the desktop menu's "New Terminal" item, which spawns a terminal with `WAYLAND_DISPLAY` pointing at Aurora and nothing else. Making GUI apps first-class in the dock widens the set of things launched that way. It does not make them safe, and a dock icon for a windowed app must not be read as saying so.

The Haiku apps carry this risk now because they are demonstrations built from source in this repository. That is not an argument for any third-party windowed app.

### The fix: make the Wayland socket a grantable capability

A GUI app can run as a domain once the compositor connection is something a domain can be granted instead of something a process inherits. That requires:

1. **A capability kind in the manifest**, for example `device:wayland` or a new `display` kind, with `capDeclared` and the consent diff (`Diff`) treating it as a grant that needs fresh consent when added.
2. **The adapter binding exactly one socket into the domain.** The domain would get the compositor's Wayland socket path and nothing else of `XDG_RUNTIME_DIR`, so a domain cannot reach the host's own compositor or other apps' sockets.
3. **A compositor that knows which client is which.** Today a window's identity is whatever the client says. For a domain, the compositor must learn from the socket (one per domain, or `SO_PEERCRED` against the domain's init) which app a surface belongs to, so the approval overlay and title bars cannot be spoofed by a granted client.
4. **Shared memory and GPU handles.** `wl_shm` buffers and `dmabuf` need file descriptors that cross the boundary. The domain needs the fonts, `libwayland` and GL or Vulkan device nodes it uses, which is a larger closure than `magick`'s and has to be proven the way `closure.sh` proves the current one.
5. **A lifecycle in the controller.** A launched domain has no deadline, and teardown on window close or compositor crash is a new case next to the crash-survival work.
6. **An answer for input.** A granted client receives keyboard and pointer events for its own surface only. The compositor already routes by focus; the open question is whether a domain may ask for a global grab.

Until that exists, the honest statement is: verbs are sandboxed, windows are not.

### Two schema additions made alongside

Verbs gain two fields that belong with the same change.

**`[[verb]] default = true`** marks the obvious thing an app does to a file it accepts. Without it every open has to ask which verb was meant. With it, a double-click can run the default and the verb menu becomes the override. An app has at most one default per accepted MIME type: `resize@v1` and `export@v1` may both accept `image/png`, and only one may be the default for it. The validator names both verbs on a collision. A default verb must have a non-empty `accepts`, since a verb that takes no document has nothing to be the default for. A default is a convenience and not a grant: effect class, capabilities and approval apply exactly as they do to any other invocation of the verb. In the shipped manifests `default = true` is set only on read-only verbs that need no arguments (`magick`'s `identify@v1`, `lumen`'s `histogram@v1`), so that opening a file can run it with no question and no side effect. `ffmpeg` and `imagemagick` have no verb that qualifies and are left alone.

**`[[verb]] group = "<string>"`** is for organising a long verb menu. It is advisory and presentation-only. It is not semantic and not security-relevant, and nothing may make an access, approval or other policy decision from it. It must be non-empty if present, at most 40 characters, and free of control characters, because it reaches the UI and is untrusted display text.

The reason there are two axes and not one richer taxonomy: **authority** and **intent** do different jobs. `effect` (read, write, destructive, external) is coarse, total, and policy-bearing. Every verb has exactly one value, and the approval gate and consent diff are written against it. Intent, meaning what a verb is called, what its `summary` says and how `group` files it, is fine-grained and written for people. Folding them together yields a taxonomy that is too coarse to help a person find a verb and too soft to hang policy on, and it invites someone to write a rule against a label that a manifest author chose for presentation. `effect` stays the only authority signal and is not widened here.

## Open questions

- **What the dock does with a windowed app that also has verbs.** The schema allows both. Whether a click launches the window, opens the verb picker, or offers both is a client decision not made here.
- **How a windowed app is stopped.** The launcher gets a pid. Whether Aurora tracks it, restarts it, or reaps it on compositor exit is undecided.
- **How an app says it needs the display.** Whether the Wayland grant is a `device:` class, a new capability kind, or implied by `windowed = true`. Implying it is attractive and makes `windowed` a security-relevant flag, which is the opposite of the presentation fields above.
- **Per-app runtime pins.** The Haiku manifests declare no `[[runtime]]`: their dependencies are system libraries, not a pinned interpreter. Whether a native C++ app should pin something is open.
- **Which apps deserve a domain first.** `iface_browser` does the most (it reads the filesystem and shells out) and is the strongest case for the capability work.
- **Windowed verbs.** `haiku_ledger` serves bus verbs from a long-running `BApplication` with no window. Its manifest therefore declares verbs and no `exec`, and `registry.invoke@v1` cannot run them because there is no `run` script. Whether a "service app" deserves its own manifest marker is undecided.

## Consequences

- The dock can list the Haiku apps, and the registry says truthfully which ones can be launched. A missing binary is a visible condition and not a dead icon.
- The manifest says more about an app than the sandbox enforces, for windowed apps only. That asymmetry is deliberate and written down here. Reviewers should not treat a windowed app's empty `[capabilities]` as least privilege. It is no enforcement at all.
- The consent diff treats a change to `windowed` or `exec` as needing fresh consent, since it changes what the system runs outside any domain.
- Any future policy that treats "windowed app" as a safer or lower-privilege class would be wrong. Nothing in the schema should be read as saying so.
