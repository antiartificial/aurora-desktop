# The surface contract

`contracts/surface` is the wire form of a window's presentation: **what it offers, what it is
allowed to do, and what it did**. It exists as its own Go module because three sides must agree on
it and none of them may depend on another's code:

| Side | Uses it for |
|---|---|
| `kits/ui/surface.{h,cpp}` (C++) | emits a descriptor from a `Desc` tree |
| `controller/internal/service` (Go) | relays `registry.describe@v1`, gates `ui.act@v1`, mints `Receipt`s |
| `compositor/src/shell/header.rs` (Rust) | draws the header band |

## Shape

```
Descriptor { app, title, actions[Action], grants[], shell_chrome? }
Action     { id, label, effect, grants[] }
Receipt    { action, artifact, effect, detail }
effect     = "read" | "write" | "nominal"
```

`grants` are **declared, never inferred**. A descriptor is a claim about access; a receipt is the
evidence of what happened.

## Two rules that make it usable

1. **Canonical encoding.** `Marshal` sorts actions by id and every grants list, so the same tree
   produces the same bytes. That is what makes a golden diff meaningful and what lets the C++ and
   Go encoders be compared with `cmp` — they are byte-identical for the same surface, and that is
   tested rather than assumed.
2. **Validate is part of parsing.** `Unmarshal` validates; there is no path that reads a descriptor
   without checking it. An unknown `effect` is an unpoliced action, so it is refused rather than
   defaulted to `nominal`.

## How an app names an action

A node named `act:<verb>`, `act:<verb>=<label>`, or either plus `#<effect>` (e.g.
`act:save#write`) is an action. The verb becomes the action id, the label defaults to the verb, and
the effect defaults to `nominal`. `SurfaceOf` in `kits/ui/surface.cpp` is the single parser; the
older `Describe` delegates to it, so the two cannot disagree about the same node.

Navigation and view preferences are deliberately **not** actions: `Back`, `Forward`, sorting or
hiding files change what you are looking at, not what the app does to anything.

## How it travels

`registry.describe@v1` carries a manifest-derived descriptor for an app that has verbs. That covers
manifest apps and nothing else -- an app with no manifest has no verbs to describe.

So an app can also publish its own:

| Verb | Payload | Reply |
|---|---|---|
| `surface.publish@v1` | `{"surface": <Descriptor>}` | `{"ok": true}` |
| `surface.list@v1` | `{}` | `{"surfaces": [<Descriptor>, ...]}` |

The compositor's relay asks `apps@v1` + `describe@v1` for manifest apps and `surface.list@v1` for
published ones, merging the two: a publication wins on the same app id, because it is the app's own
claim about itself while `describe` is the manifest's.

## Where it is enforced

- `registry.describe@v1` and `ui.act@v1` share one caller gate (`surfaceCaller`): the app's own
  domain, or the session. A domain cannot describe another domain's surface.
- `ui.act@v1` runs an action only when the app's manifest declares every grant the action needs;
  refusals never reach the domain, and successes carry a receipt.

## Known limits

- The relay polls every 2 s rather than subscribing: the bus session wire is request/reply.
- A header binds to the first window of an app, on one output.
- Published descriptors live in the controller's memory; a restart forgets them until the app
  republishes.
