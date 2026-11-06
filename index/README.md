# indexd: Phase 9 slice (saved queries, live updates, derived-from)

A thin first slice of "Storage: attributes, queries and history". It is the
spine that makes the exit demo run, not the phase.

## What it is

`indexd` subscribes to `provenance.created` on the bus and keeps an index of
the files those events name: app, verb, inputs (the `derived from` edge),
grants, the sender busd stamped, and the time indexd saw the event. Saved
queries are files; a client long-polls `index.next@v1` and gets each
new matching file it is allowed to see as it is indexed. `aurora query` is the client
(`shell/launcher/query.go`). Demo: `demos/live-query/run.sh`.

The bus events are the source, not fanotify: the plan wants relationships "built
from the events Phase 5 already emits rather than inferred after the fact".

## Read this before trusting it

- **Who sees what, in one sentence:** a caller that is a domain sees only the
  entries whose recorded domain is exactly that domain's bus-stamped identity;
  the user's own session sees everything; any other identity sees nothing.
  Details, and what it rests on, are in "Who sees what" below.
- **The filter covers the index, not the bus.** `provenance.created`, the event
  indexd learns from, is still an ordinary broadcast topic: a domain that
  subscribes to it (or to `**`) receives every record in full: file, app,
  domain, inputs, grants. The demo shows a domain's `**` subscription receiving
  another domain's record. The index cannot change that; closing it needs busd
  to refuse domain-socket subscriptions to that topic and to every wildcard that
  matches it. That bus change was not made. Until it is, the filter keeps the
  index honest and does not keep a domain from reading the log of what was made.
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

## Who sees what

- **Identity.** The `From` busd stamped on the request envelope. On a per-domain
  socket that is `domain:<name>:<id>`, a fact about which socket (and, with
  attestation on, which cgroup, namespaces and uid) the connection came through;
  on the session socket it is `uid<N>:<process name>`. Nothing in the payload is
  read: there is no field a caller can set to ask for someone else's results,
  and a forged `from` in the envelope is overwritten by busd (the demo tries
  both). An identity that is neither shape sees nothing.
- **Rule.** A domain sees an entry iff the entry's `domain` equals its own
  identity. `run` and `next` judge each file by its NEWEST write, so a file
  another domain has since overwritten is no longer yours; `show` returns only
  your own writes and omits `exists_now` (size, mtime) unless the newest write is
  yours; `used_in` lists only visible outputs; the stats count only what you can
  see. A file you cannot see is indistinguishable from one never indexed.
- **Why not "plus paths the domain holds a grant for".** The controller revokes
  every grant when an invoke ends, so between invokes no domain holds one and the
  index cannot know whether one is held now. Reading grants out of old records
  would hand out access the domain no longer has. The cost: a domain does not see
  another domain's file it was once granted as an input.
- **Live updates** are a long poll, `index.next@v1 {name, after, wait_ms}`, not a
  topic. indexd answers with the matching writes after `after` that the asker may
  see, or holds the request until one arrives or `wait_ms` (max 25 s) passes.
  Each answer is a reply to the asker's own request, so no other connection is
  sent it. indexd publishes **no** `index.match.*` event; a domain subscribed to
  `index.**` receives nothing from indexd. Latency was re-measured after the
  change: about 1.4 ms from the controller's record timestamp to the watcher's
  print, as before (the demo asserts under 1000 ms).
- **What it trusts.** The `domain` text of the controller's record (the sender is
  checked to be the controller; the text is not verified), and that a process on
  the session socket is the user. Records with no domain (unisolated runs) are
  visible to the session only.
- **Residual leaks to a domain caller, all metadata:** `seq` and `as_of_seq` are
  global, so gaps show that entries it cannot see exist (not what they are);
  saved-query names, titles and parse errors are the user's and are listed to
  every caller; a domain can run any saved query by name (filtered).

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

Service `index`: `index.queries@v1`, `index.run@v1 {name}` (returns `as_of_seq`),
`index.show@v1 {file}`, `index.next@v1 {name, after, wait_ms}`. Every reply
carries `viewer` (the identity indexd filtered for, its kind, the rule) and the
notes. A watcher runs, then polls `index.next@v1` with `after` = the `as_of_seq`
it was given and then each reply's `as_of_seq`; the cursor is indexd's own
sequence number, so nothing is missed or doubled, and entries the caller may not
see still advance it. indexd accepts `provenance.created` only from a sender named
`...:controllerd` and counts the rest.
