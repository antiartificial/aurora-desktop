# The surface seam

How a window tells the OS what it offers, and how the OS draws and polices it. Four sides, one wire
shape (`contracts/surface`), and a set of gaps stated at the end rather than discovered in use.

## The four sides

| Side | Owns | Entry point |
|---|---|---|
| App (C++) | naming its actions, publishing | `kits/ui/surface.{h,cpp}`, `SurfaceJson`, `surface.publish@v1` |
| Contract (Go) | the shape, validation, canonical bytes | `contracts/surface` |
| Controller (Go) | holding published surfaces, gating actions, minting receipts | `registry.describe@v1`, `surface.publish@v1`, `surface.list@v1`, `ui.act@v1` |
| Shell (Rust) | drawing the band, running its actions, showing receipts | `compositor/src/shell/header.rs` |

## What an app writes

A node named `act:<verb>`, `act:<verb>=<label>`, or either plus `#<effect>` is an action
(`act:save#write`). The verb becomes the id, the label defaults to the verb, the effect defaults to
`nominal`. `SurfaceOf` is the single parser; the older `Describe` delegates to it.

Navigation and view preferences are deliberately **not** actions: Back, sorting or hiding files
change what you are looking at, not what the app does to anything.

Atlas names five: `open#read`, `copy-path#read`, `rename#write`, `new-folder#write`,
`move-to-trash#write`.

## What the person sees

A band above the app's window: title, the action labels in canonical id order, and a grants chip.
Clicking an action posts `ui.act@v1` and the outcome appears on the band — a receipt
(`Open: /tmp/holiday-open.png`), the effect when there is no artifact, or the controller's refusal in
grey. A receipt is never drawn for a refusal.

## What the tests actually cover

- Contract: validation, canonical encoding, `shell_chrome` round-trip, empty actions as `[]`.
- Cross-language: C++ and Go encode the same document to **byte-identical** JSON (`cmp`).
- Controller: publish/list round-trip, republish replaces, invalid refused, a domain publishing
  another app's surface refused, `ui.act` refuses an ungranted action without touching the domain.
- Shell: band appears from the bus with no fixture, moves with its window, disappears when the
  publication is withdrawn, a bad descriptor is refused and never drawn, a click maps to the action
  under it, an allowed action shows a receipt, a refused one shows the message.

## Known gaps — read these before trusting the band

1. **`ui.act` cannot run a published-only surface.** It starts from the manifest registry and demands
   a manifest verb, so an app with no manifest (Atlas) has every action refused with `no_such_app`.
   The header draws controls that are dead for exactly the apps `surface.publish` exists for.
   Fixing it means deciding whether the descriptor or the manifest is authoritative for actions, and
   where a manifest-less app's grants come from. That is a trust decision, not a patch.
2. **`shell_chrome` is parsed by all three sides and honoured by none.** The obvious gate (draw the
   band only when it is true) is wrong until (1) is settled, because a band also carries grants that
   are not opt-out-able, and describe-derived descriptors default the flag to false.
3. **`describe@v1` gained a caller gate** under an "additive" banner. Additive keys do not get to
   carry breaking authz; either bump the verb or accept the break explicitly.
4. **Publications are never removed.** An app that quits leaves its descriptor in `surface.list@v1`,
   so the next process with that app id inherits a stale surface.
5. **The relay polls every 2 s** (the bus session wire is request/reply), and a band binds to the
   first window of an app, on one output.

## Seeing it run

```
cd ~/projects/aurora && ./harness/aurora-up
WAYLAND_DISPLAY=<printed> AURORA_BUS_SOCK=/run/user/1000/aurora/bus.sock ./kits/build/ui/atlas
```

The compositor log then shows the chain: `surface: bus -> N surface(s)`, `surface: header published`,
`surface: header layout`.
