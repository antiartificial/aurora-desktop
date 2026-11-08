# 0025. Programs that run with your authority, and the launcher that starts them

- **Status:** proposed
- **Date:** 2026-11-08

## Context

The dock is the only launcher Aurora has, and it cannot start Atlas at all.

`shell/mod.rs rebuild_items` builds one pill per `registry.apps` entry, flat, in id order, and
`shell/layout.rs` then draws one pill per item that fits. So the dock shows a truncated list of
everything installed. That is not a dock. A dock holds what you keep.

Atlas has no manifest, and it should not get one. `apps/browser/manifest.toml` already records
what a windowed manifest produces for a file manager. In its domain "$HOME is the domain's own
empty data directory ... so today it browses an empty tree". That manifest then refuses to
declare a path, because "declaring $HOME would hand an app the whole home directory, which is
the thing a domain exists to avoid". Section 9 of `docs/atlas-design.md` says Atlas "runs as a
plain process with the user's authority". A manifest for Atlas ships a file manager with no files.

Six of the thirteen manifests in `apps/` are verb-only. You do not open ImageMagick. You run
`convert@v1` on a file. A row that offers to launch one promises something that cannot happen.

The compositor already starts a host program for "New Terminal". So the class of program this
record names is not new. It is unnamed.

## Decision

### A program can run with your authority, drawn on aurora::ui

Atlas, the terminal, and the launcher are one class. The compositor resolves each binary at
startup and starts it as a host process. None of them gets a manifest, because a manifest means
a domain, and a domain means an empty filesystem.

The alternative was a path capability per app. `apps/browser/manifest.toml` argues against it in
its own comments, and this record follows that argument rather than reopening it.

### What a row is, is derived. Where it sorts, is declared.

`category` is a new optional key in the manifest's `[app]` table. It is presentation only, under
the same clause as `Verb.Group`: no policy reads it. The vocabulary is one closed list in
`contracts/`, validated when the manifest loads, where `parse.go` already rejects unknown keys.

The controller sends each app's category token and the vocabulary in display order, each entry
carrying its token and its label. The compositor reads only the tokens, so it orders rows without
depending on any label text. The labels travel for the launcher, which is what draws them. To add
a category, edit one file in `contracts/`. No Rust changes. A mirrored enum on the Rust side would
be two lists to keep in step.

What a row can do is derived from the fields `registry.apps@v1` already carries, and never
declared. A windowed app opens. A verb-only app runs its verbs and is never launched. An
installed app with nothing to offer is greyed with the registry's own words, which is the
precedent `rebuild_items` argues for. So a manifest places its own row and cannot claim an
action it does not have.

### The launcher is an app, and the compositor holds the door

The launcher is an aurora::ui app, on the primitives Atlas proved. It calls `registry.launch@v1`,
so it holds the right to start any app. That is weaker than the right to read every window title,
which 0021 refused to give any client, and the launcher need not keep running.

The launcher has to be started by something, so the compositor keeps one row and one key chord
that start it, plus rows for Atlas and the terminal. A row whose binary is missing is greyed and
says "not built", so shipping a row can never leave a dead one.

No submenus. An earlier draft of this record turned `Chrome::menu` into a stack of menus so a
category could cascade. That work pays for itself only if the compositor draws the whole
inventory, and it does not. The launcher does.

### The dock becomes what you keep and what is running

`Item` carries `{ id, label, clickable, problem }` and no running state, so the dock cannot show
what is running. The data is already there. `input.rs:210` calls `app_window_ids` on a secondary
press, under a comment about "a secondary press on a running app".

The dock gains running state, and its feed changes from the registry to favourites you pick,
persisted through `persist.rs`. Three surfaces then have three jobs. The dock shows what you keep
and what runs. The Deskbar shows every window and what it costs. The launcher shows everything
installed.

## What is not built, and why

- **No manifest for Atlas, and no path capability for it.** See Context.
- **No submenu variant and no menu stack.** The launcher draws the inventory.
- **No new theme table.** A table for one key string is a surface the parser, the reference theme,
  and a drift test carry forever.
- **No icons.** The compositor's menu draws text. An icon pipeline belongs to the launcher and to
  `atlas-design.md` 4.6.
- **No search, recents, or favourites inside the launcher yet.** Thirteen apps and no index.
- **No Deskbar keyboard focus.** A popup takes keys while it is up and gives them back, so 0021's
  open question stays open.
- **No `registry.catalog@v1`.** Replacing `registry.apps@v1` and `registry.verbs@v1` with one call
  is the right end state. It is not this change. See Weakest points 1.

## Weakest points

1. **Launchability is still derived in three places.** `dock_click` reads `windowed && launchable`.
   `verb_entries` reads `!launchable`, then `verbs.is_empty()`. `run_verb` re-checks `!launchable`
   under a comment admitting the menu might have lied. This record adds a fourth reader rather
   than collapsing the three, because collapsing them would fail as a dock that quietly stopped
   offering something, not as a compile error. The collapse is its own change, landed after a test
   asserts the old and new paths agree.
2. **Two ways to start a program, behind one list.** Atlas and the terminal run with your
   authority. Manifest apps run in domains. At the moment you click, the section heading is the
   only difference you can see. That is what the terminal already is and what both specs
   separately ask for, and it is still the first thing a reviewer should attack.
3. **The key chord is unchecked.** No binding is reserved yet, and no check against client use has
   run.
4. **The stat-then-launch race stays open.** The controller stats the filesystem for
   `Launchable()`, and `registry.launch@v1` runs later. A row can be right when drawn and wrong
   when clicked.
5. **Grouping is only as good as the manifests.** A closed vocabulary stops a misspelling from
   minting a section. Nothing stops an author filing an app under the wrong one.
6. **Two manifests both call themselves "ImageMagick"** (`org.imagemagick.imagemagick` and
   `org.aurora.magick`). Each row carries its id as a note so the two are distinguishable. The
   duplication is a manifest cleanup, not a launcher concern.

## Verification

The contract unit has landed. Items 1 and 2 are done. Items 3 and 4 are outstanding, because no
launcher and no System rows exist yet.

1. **Done.** `contracts/manifest`: `TestCategoryTableIsUniqueAndNeverSystem`,
   `TestCategoryAcceptedOnWindowedAndVerbOnlyApps` and `TestUnknownCategoryNamesTheKnownOnes`
   pass. The last one checks that `system`, `Files`, `games` and the empty string each fail with
   `app.category` and the known values. `controller`:
   `TestCategoryLoadsOrSkipsWithKnownValues` puts an unknown category in `Skipped` with a reason
   naming it, and `TestAppsReplyCarriesCategoryAndOrder` checks the per-app token and the order
   `files, media, development, utilities, haiku` over a real bus. `go build ./...` and
   `go test ./...` are green in `contracts/manifest`, `controller` and `shell`.
2. **Done.** `compositor`: `parses_category_and_order`,
   `an_older_registry_without_either_field_still_parses` and `caps_bound_an_oversized_reply`
   pass. 7 of 7 in `shell::apps`. The older-reply test yields no category, so a controller
   predating this change still works.
3. Outstanding. A headless compositor test that the System rows start a host process, that a
   missing binary greys its row, and that a verb-only app never gets a launch row.
4. Outstanding. A demo under `demos/` that starts Atlas from the launcher against the real stack
   and asserts a window arrives.

The full suite is 388 passed, 2 failed, 2 ignored. The two failures are
`a_client_in_a_real_domain_cgroup_gets_the_figures` in `tests/deskbar.rs` and
`a_fullscreen_client_cannot_cover_it` in `tests/indicator.rs`, and both fail identically with this
change stashed, so neither is caused by it. That run and that stash check were made by the agent
that wrote the unit, not reproduced here. 0021's figure of 277 is two days old and no longer the
baseline: the suite has grown since it was written.

## Open questions

- Which key chord, and is it free?
- Does the launcher raise a running Atlas or start a second one? `registry.apps@v1` does not say
  what is running, and 0022's "already running" question is open.
- Does the terminal lift Haiku's engine from `Vitruvian/src/apps/terminal`? The engine is about
  6,042 of its 20,033 lines, and the BeAPI it needs is already compiled in this tree. That is its
  own record.

## Consequences

- `contracts/manifest` gains a `Category` field and one closed vocabulary file.
- `registry.apps@v1` gains a per-app `category` token and a top-level `categories` list, each
  entry carrying a token and a label. Both are additive, so an older compositor and the `aurora`
  CLI keep working.
- `MAX_APPS` is 256 and a new `MAX_CATEGORIES` is 32. Category tokens are bounded to 32
  characters.
- `MAX_APPS` in `shell/apps.rs` rises from 24, as a bound on an untrusted reply. The dock is
  unaffected, because `layout.rs` already draws one pill per item that fits. No second constant.
- Thirteen files under `apps/` gain one `category` line. `apps/browser` is renamed "Haiku
  Browser", so two rows never both read "Files".
- `files` and `utilities` start empty, by design. The vocabulary is the taxonomy this system
  means to have, not a census of what is installed. Atlas is the file manager and it appears in
  the System section, because it has no manifest, so nothing populates `files` until something
  file-shaped ships with one.
- `harness/aurora-up` loses the paragraph that tells the reader to start Atlas by hand.
- `fx/theme.rs` is untouched.
