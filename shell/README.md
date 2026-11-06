# shell

The launcher: the Quicksilver/Raycast-style thing a person drives. Plan: "Phase 5",
*pick an object, pick a verb filtered by type, fill typed arguments, run; chains of
two verbs.* The rest of the shell (dock, notification area, layer-shell UI) is
Phase 8 and is not here. This is a CLI, `aurora`, that talks to the registry the
controller serves on the bus (`registry.apps@v1`, `registry.verbs@v1`,
`registry.describe@v1`, `registry.invoke@v1`) using `bus/client`.

Needs `busd` running, then `controllerd`. If either is missing the CLI says which
to start and exits 1. Every bus call has a timeout (`--timeout`, default 10s).

```sh
cd shell
GOWORK=off go build -o ./out/aurora ./cmd/aurora   # GOWORK=off: shell is not in go.work yet
GOWORK=off go test ./...
./demo.sh
```

## The flow (`aurora open`)

1. MIME of the file: content is sniffed with `http.DetectContentType`. Sniffing wins
   unless it gives one of its two generic answers (`text/plain`,
   `application/octet-stream`), in which case a small built-in extension table
   decides (so `.md` is `text/markdown`). A PNG named `.jpg` is `image/png`.
2. `registry.verbs@v1 {"accepts": MIME}`, filtered again client-side (wildcards like
   `image/*` understood).
3. A numbered list: name, app, effect class, summary. **destructive and external
   verbs are marked `!!` with a note** (the plan will require approval for them;
   today the launcher only warns).
4. Pick by number or name, or `--verb NAME` to skip the picker.
5. `registry.describe@v1` gives the argument schema; each argument is prompted with
   its type and default. `--arg k=v` supplies values and skips all prompting.
6. `registry.invoke@v1`; the result, output path, `result` (read verbs), isolation
   flag and provenance record are printed. If you give no `--output`, write verbs
   get one next to the input (`photo.png` + `resize@v1` -> `photo.resize.png`; a
   string `format` argument changes the extension).

## Commands

```sh
aurora apps
aurora verbs --accepts image/png

aurora open photo.png                                  # picker + prompts
aurora open photo.png --verb resize --arg width=800    # no prompts at all
aurora open photo.png --verb export --app org.aurora.lumen \
        --arg format=jpeg --output ~/Pictures/photo.jpg

aurora chain photo.png --verb resize --verb export \
        --arg 1:width=800 --arg 2:format=jpeg          # A's output becomes B's input
aurora watch                                           # live provenance log
aurora watch --count 1                                 # exit after one event
```

`--verb` accepts `resize` or `resize@v1`. If two apps offer the same verb, use
`--app`. In `chain`, `--arg 1:k=v` / `2:k=v` target a step; a bare `k=v` goes to
whichever step declares `k` and is an error if neither does. Step 2's verb is
resolved against the real MIME of step 1's output file. Without any `--arg`, each
step prompts. `--output` applies to the last step; the intermediate file is
derived next to the input and is left on disk.

`aurora watch` prints `time  app  verb  -> output  <- inputs`:

```
01:41:33  org.aurora.demo  copy@v1  -> /home/me/sample.copy.png  <- /home/me/sample.png
```

Exit codes: 0 ok, 1 failure (bus/registry down, verb failed, bad input), 2 usage.

## Protocol assumptions

Read against the real `controller/internal/service` as of this writing. The launcher
reads `describe`'s `input` as a map of name to a type string (`"int"`, `"int?"`,
manifest notation) or an object `{type, default, description, optional}`. For
`provenance.created` it accepts `file` or `output`, `inputs` (or `input`), `app`,
`verb`, and a time in `ts`/`time` (bus hybrid-clock stamps are converted).

## Tests

`launcher/launcher_test.go` uses a fake `Backend`, no live bus: MIME detection
(png, jpeg, text, lying extension), verb filtering, non-interactive `--verb`/`--arg`
(stdin is empty, so any prompt would fail), interactive pick and arg prompting,
chain wiring, chain stopping on step-1 failure, registry absent / bus down messages,
destructive/external marking, watch formatting, `-h`.

## Not implemented

- Objects other than files: no windows, no selections, no clipboard, no "current
  selection" from the compositor.
- No graphical launcher: no layer-shell window, fuzzy search or keyboard-driven UI.
  It is a terminal prompt.
- No approval UI. Destructive and external verbs are marked and the user is told
  approval will be needed, but nothing blocks or asks; running the verb is the consent.
- No recipes or saved chains; chains are exactly two verbs, no general pipeline.
- No argument validation beyond type coercion (no enums, ranges, path pickers).
- Chains are not atomic: if step 2 fails, step 1's output stays.
- Verb ranking/history: the list is in registry order.
- A multi-input verb (inputs beyond the first file) cannot be expressed.
- Not tested against Lumen: the controller's fixture app (`org.aurora.demo`) was the
  only real app available when this was written; Lumen had a manifest but no `run`.
- `aurora` is not wired into `go.work` or a root build; build with `GOWORK=off`.
