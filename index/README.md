# indexd: Phase 9 slice (saved queries, live updates, derived-from)

A thin first slice of "Storage: attributes, queries and history". It is the
spine that makes the exit demo run, not the phase.

## What it is

`indexd` subscribes to `provenance.created` on the bus and keeps an index of
the files those events name: app, verb, inputs (the `derived from` edge),
grants, the sender busd stamped, and the time indexd saw the event. Saved
queries are files; a client subscribes to `index.match.<name>` and gets each
new matching file as it is indexed. `aurora query` is the client
(`shell/launcher/query.go`). Demo: `demos/live-query/run.sh`.

The bus events are the source, not fanotify: the plan wants relationships "built
from the events Phase 5 already emits rather than inferred after the fact".

## Read this before trusting it

- **Results are NOT filtered by capability.** The plan says a query from an app
  or agent returns only files that domain can see. That is not implemented.
  Every caller of the `index` service sees every indexed file, and
  `index.match.*` is an ordinary topic any subscriber receives. Every reply and
  every `aurora query` run says so. (The paths and apps are already broadcast by
  `provenance.created` itself; the index adds aggregation and persistence.)
- **The index only knows what indexd saw.** Files created outside Aurora, before
  indexd first ran, or while it was down are not in it. Deletions and renames
  are not tracked: `present`/`MISSING` is an `lstat` made when the answer is
  built. There is no backfill from the bus log.
- **Nothing is verified.** App, verb, inputs and domain are the controller's
  published text. `domain_attested` is the controller's claim; indexd does not
  check it. Only the sender (`uid1000:controllerd`) is the bus's own word, and
  that name is a process name a same-user process can choose.
- **Type is from the extension.** indexd never opens indexed files, so a PNG
  named `.jpg` is `image/jpeg` here.
- No xattr attributes, schema registry, btrfs history, vector search or GUI.
  "History" is just every indexed write of a path, oldest first.

## Layout and storage

- `index.jsonl` in `--data-dir` (default `$XDG_DATA_HOME/aurora/index`): one
  JSON entry per accepted event, append-only, fsynced, replayed into memory on
  start. A torn or hand-edited line is skipped and counted, not fatal. The file
  is not tamper-evident (the bus log is).
- Queries in `--queries` (default `<data-dir>/queries`), `NAME.query`.
- Memory: every entry, the versions of each file, and the input-to-output edge
  map. Queries match each file's latest write.

## Saved-query format

```
# comment
title: Images exported this week
mime: image/*          # type/subtype or type/*, from the extension
ext: png
app: org.aurora.magick
verb: convert          # "convert" matches convert@v1; "convert@v1" is exact
since: this-week       # today | this-week | this-month | 90m | 24h | 7d
under: /home/me/Pictures
derived-from: /home/me/Pictures/photo.png
```

Different keys are ANDed, a repeated key is ORed. `since` is measured against
indexd's own clock when it saw the event (not the record's text), calendar
terms use local time with Monday as the week start. Parsing is strict: unknown
keys, a repeated `since`, bad values and an empty query are errors, and a broken
query is listed as broken rather than skipped. The name is the file name.

## Bus surface

Service `index`: `index.queries@v1`, `index.run@v1 {name}`, `index.show@v1
{file}`. Live: event `index.match.<name>` `{query, hit}`. A watcher subscribes
first, then runs, then drops live events already in the snapshot, so nothing is
missed or doubled. indexd accepts `provenance.created` only from a sender named
`...:controllerd` and counts the rest.
