# 0011. Compositor-drawn powerbox: a second front end to the same tickets

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Decision 0007 makes a grant ticket the one thing that distinguishes a file the user chose (`source:"powerbox"`) from a path a caller merely asked for (`gesture`). It also says, in terms, that the powerbox is **not a trusted path**: it is an ordinary same-user process drawing on `/dev/tty`, a same-user process can run it under its own pty and answer for the user, and the ticket proves "a process holding the key signed this", not "a human chose this". Its Open questions name the missing piece: "the picker drawn by the compositor on its trusted UI path, with input the compositor attributes to the user, and the signing capability held by the compositor rather than a key file any same-user process can read."

The compositor now exists and already does this for approvals (0007's amendment, 0003): it draws the prompt above all clients, leaves it out of client captures, reads the answer as a keystroke no client received, and signs its reply with `approval.key`. It also now draws the dock, a context menu, a settings panel and, since the verb menu, the question "which verb" when a dock item is clicked. What is still a terminal program is the last step of that flow, choosing the file.

A dock click is therefore a compositor-drawn verb menu followed by a terminal file picker. That works, and the terminal step is the weak half twice over: it is the clumsier interface, and it is the one part of the grant flow that sits outside the trusted path.

## Decision

Add a compositor-drawn file picker as a **second front end** to the existing ticket mechanism. The terminal powerbox stays, reachable and unchanged. Nothing about tickets changes: the claims, the HMAC, the single use, the binding to path, mode, app and verb, `--require-powerbox` and the controller's checks are as 0007 describes.

What would be built (not built yet; see "What is built"):

- **Request.** The launcher, once the verb is resolved, asks the compositor over the trusted socket for a pick: `{app, verb, mode: ro|rw, kind: file|save, start, suggest}`. These are the fields `PickRequest` already carries. The launcher fixes everything except the choice of path.
- **Picker.** The compositor draws a directory listing with the menu and panel machinery (glass, ease-out appear, keyboard Up/Down/Enter/Backspace/Escape, pointer rows, HiDPI). It shows the app, the verb, its effect class and the access being granted (read-only, or read-write for a save), because that is what the grant is for. Text from a request is stripped of control characters before drawing, as the approval prompt does.
- **Gesture.** A ticket is minted only from a compositor input event that the picker consumed: an Enter or a click on a row while the picker is the keyboard and pointer owner. The compositor resolves symlinks before signing, as the terminal powerbox does. The reply is bound to the request id so it answers the one request that opened the picker.
- **Return.** The compositor returns `{path, ticket}`, the shape `ExecPicker` already parses, signed or carried like an approval reply. The launcher passes it to `registry.invoke@v1` unread, as it does today.
- **Test affordance.** `--path` stays exactly where it is: a flag of the terminal `powerbox` binary behind `AURORA_POWERBOX_ALLOW_PATH=1`, with its warning, documented as a bypass of the gesture. The compositor picker gets **no** `--path`. Headless tests drive it with `--test-input`, which exists only with `--headless` (no real input device, so there is no human to impersonate) and which `main` refuses with any other backend. A test that clicks a row is testing the gesture path; it is not a way to mint on a real machine.

### What gets stronger

- **The prompt can be believed.** A same-user process can no longer run the picker under its own pty and answer for the user, because the answer is an input event read by the compositor, not bytes on a terminal the process controls. Another process cannot draw over it or capture it (it is drawn in the trusted overlay, after captures are served).
- **A fake picker is harder to pass off.** The user is looking at the same surface that draws their approval prompts, instead of a terminal any process may write to.
- **One surface for the whole question.** The verb and the file are chosen in the same place, which makes the effect class shown at the verb step and the access shown at the file step the same information in the same style.

### What gets weaker, or does not improve

- **Forgery by key is unchanged.** A ticket is still `HMAC(key, claims)` and the key is still a 0600 file any same-user process can read. If the compositor signs with the existing `powerbox.key`, a process that reads that file can still mint a ticket for any path, with no picker involved at all. The picker becoming trusted does not close that: only the controller refusing every ticket not signed by a key that only the compositor holds does. That means a key the controller cannot read in the clear (a distinct uid, or the controller verifying over a socket the compositor answers) and is the same open item 0007 already lists. Until then `--require-powerbox` raises the cost of the attack and does not close it, as before. This record must not be read as having achieved "this grant came from the user".
- **The compositor is now a larger trusted base.** It reads the filesystem to list directories (as the user, same uid), parses names that an attacker may have chosen, and holds a signing key. A bug in directory listing is now a bug in the process that draws every approval prompt. The listing must be bounded (entry count, name length), must not follow a symlink out of the listing without showing that it did, and must not block a frame on a slow or hung directory (a network mount); it is read off the main thread, as the registry ask is.
- **It is a keystroke or click, not a human.** As 0007 says of approvals: it is evidence of the backend's input event. Only the headless backend, with injected input, has been run; a real-input backend has not.
- **A look-alike drawn by a client is not prevented**, and there is still no secret or secure-attention key by which the user tells the real picker from a fake one.
- **One more caller of the trusted socket.** Any same-user process may ask for a pick, as any may ask for an approval prompt. It cannot answer one, but it can open a picker on the user's screen with text of its choosing. The picker shows the request's app and verb as the compositor was told them; like the approval prompt, it does not authenticate them.
- **The text path remains the weaker one, deliberately.** Nothing here makes the terminal powerbox safer, and a same-user process can still use it. Keeping it is a usability and fallback decision (no compositor, a remote session, a script's `--path`), not a security one. A deployment that wants the property should run `controllerd --require-powerbox` with a key the terminal picker cannot reach, at which point the terminal powerbox stops being able to mint. That is not built either.

### Not decided

- Which key the compositor signs tickets with, and how the controller learns to trust only it. This is 0007's open item and the one that decides whether this record buys anything beyond the interface.
- Whether the compositor picker or the terminal powerbox is the default when both are available. The proposal is: compositor when the launcher can reach the trusted socket, terminal otherwise, with a flag to force the terminal.
- Whether a folder pick belongs here. The controller grants single regular files only, so none is consumed yet; the picker should not offer what nothing consumes.
- Save mode for a `write` verb: the terminal flow runs the picker a second time for the output and mints an `rw` ticket for the directory. How that appears as one flow in the compositor, and what it shows about the directory grant being as wide as the directory, is open.
- Whether the picker remembers the last folder per app. A memory is a convenience and also a record of what the user chose; it would want its own record.

## Amendment, 2026-11-07: a graphical picker is built, and it is a client

What this record proposed is a picker the COMPOSITOR draws. What was built
instead splits the job, and the reasoning belongs here because it diverges:

- **Browsing is an ordinary aurora-ui client.** `kits/interface/chooser.cpp`
  builds `aurora-chooser`, a Wayland client with the places sidebar, the file
  list, a breadcrumb and single selection, sharing Atlas's file-browsing model
  (`kits/aurora-ui/filebrowse.h`, extracted for the purpose). It holds no key and
  cannot mint. It prints one JSON line, `{"path": ...}`, and exits 0, 3 (cancel),
  2 (no display) or 64 (bad arguments).
- **`shell/cmd/powerbox` runs it** when `$WAYLAND_DISPLAY` is set and the binary
  is found, and keeps the terminal picker for everything else (`--tty` forces
  it). Its own stdout contract, `{path, mode, ticket}`, is unchanged, so
  `shell/launcher` needed no edit and `aurora open --pick` works as before.
- **The confirm on the trusted path exists and is OFF by default** (`--attest`,
  default `off`; `shell/picker/confirm.go`). It needs no compositor change: the
  existing `op:"approve"` request already carries `grants: [{path, mode, source}]`
  and the existing reply is already signed.

### Why the listing stayed out of the compositor

This record's own "What gets weaker" says it: the compositor would take on
directory listing, attacker-chosen filenames and threading, and a bug there
becomes a bug in the process that draws every approval prompt. The split keeps
that code where a compromise of it still cannot mint anything.

### Why the confirm ships off

Two independent adversarial reviews agreed, and each claim was checked in the
source before being accepted:

1. Against an unconfined same-uid process it buys nothing. That process reads
   `powerbox.key` and mints directly, as 0007 already states. Against a
   domain-confined app nothing changes either: it could never read the key nor
   reach the socket.
2. A pick carries no domain identity, so `verify.rs:84` returns `Unverified` and
   the panel draws `CLAIMED "" (NOT VERIFIED)` with the warning band on **every
   file open**. That band is the signal the real approval prompt depends on.
   Teaching the user to dismiss it daily is a net LOSS against the baseline.
   This is the finding that decided it.
3. The reply's MAC covers the request id and the decision, not the grant that
   was drawn, so a verified allow cannot be read as "the user saw this path".

The one property the split does buy, stated narrowly: the chooser is the newest
and most exposed code in the flow, and it holds no key, so a bug in it still has
to get a human to press a key on a surface it cannot draw or capture. That is an
integrity check on an honest chooser, not a boundary against a hostile one. It is
not a trust claim and must not be written as one.

### Defects this work found in code that predated it

- **The terminal powerbox showed one path and granted another.** `confirm`
  resolved only to test for an error, printed the path as typed, and `Mint`
  resolved a second time afterwards. A symlink swapped in between bound a file
  the user never saw, which the controller then re-resolved and agreed with.
  Fixed by resolving once and showing what was resolved;
  `TestTheShownPathIsThePathThatBinds` fails on the old code.
- **A cut path kept its tail**, so two files sharing a long suffix drew as one
  identical line. Paths now elide their middle (`draw::Elide`).
- **Display text was reduced to printable ASCII**, so every non-ASCII name drew
  as `?` and two paths in the user's own language were indistinguishable. The
  reason given was the 8x16 bitmap font, which the text stack stopped being.
  Controls and bidi characters are now named escapes and everything else is kept.
- **A prompt could be answered by the user's own typing.** `a` allowed on one
  press with no arming delay, so a prompt raised mid-sentence was allowed by the
  next `a`. There is now a 400ms window (`trusted::ARM`) and a queue cap
  (`MAX_QUEUED`), since nothing bounded the queue either.
- **`--path` skipped the confirm**, so `--attest=required` answered deny and
  still minted.

### Still open, and unchanged by this

The key question, which this record already calls the thing that decides whether
any of it buys more than an interface. A ticket is still `HMAC(key, claims)`
under a 0600 file any same-uid process can read. Both reviews converged on the
same next step, which is also this record's own "Not decided": a new op distinct
from `approve`, with its own panel wording and no domain row, where the
compositor resolves the path itself, signs the claims rather than a decision, and
holds a key the picker cannot read. `shell/picker/confirm.go` is a tested client
of this protocol and is the seam for that work.

Also still open: `clean` cannot help a name whose script has no installed font.
This machine has no CJK face at all, so two Chinese paths still draw as identical
boxes. Keeping Unicode is what makes installing one fix it.

## What is built

The verb menu only: a dock click shows the app's verbs with each one's summary and effect class, and choosing one runs `aurora open --pick --app ID --verb NAME` so that only the file grant is left to the terminal powerbox. That removes the numbered "pick a verb" prompt and changes nothing about trust. The picker, the trusted-socket request and any signing in the compositor are not built. The reason for stopping there is that the picker is not just a listing: it needs a new request on the trusted socket, a launcher change to prefer it, a scrolling listing with its own bounds and threading, a save flow, and the key question above, which is a decision and not code. It is better reviewed as a record first.

## Open questions

- The signing key and who trusts it (see "Not decided"). Phase 6 / Phase 10.
- Whether the verb menu's effect tag and the approval prompt's effect line should share one wording and one set of colours. They agree on classes today; the colours are constants in `compositor/src/shell/menu.rs` and not theme tokens.
- Authenticating what the picker displays (the app and verb come from the requester).

## Consequences

- If built as described, the user gets one coherent flow and a picker that a pty script cannot answer for them, and 0007's "Powerbox trusted path" question is half answered: the UI is on the trusted path, the signing is not.
- The terminal powerbox and its `--path` affordance keep working, and keep their warning. A reader of the controller's `source:"powerbox"` will still need to know which front end minted the ticket until the key question is settled; the ticket does not say.
- The compositor takes on filesystem listing, which it has not done before.
