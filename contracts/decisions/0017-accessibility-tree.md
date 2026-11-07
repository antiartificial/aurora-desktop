# 0017. The accessibility tree: what the app_server already knows, asked for under `ui:read`

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Haiku's Interface Kit has no accessibility API. Aurora does not need one for the reason that matters here: the embedded app_server (`kits/interface/appserver/server.cpp`) is sent the whole semantic structure of every window. `AS_VIEW_CREATE(_ROOT)` carries a view's token, name, frame, resize mode, flags, colour and parent; `AS_VIEW_MOVE_TO`, `RESIZE_TO`, `HIDE`, `SHOW`, `SET_FLAGS` and deletion keep that current; `AS_DRAW_STRING` carries every string with its baseline origin. The tree was flowing through the server and nothing read it.

This is the substrate for screen readers, for agents that drive a UI, and for "give me the data from this view". Reading every window's text is also reading a password manager, so the read is a capability, in the grammar 0014 set (`device:<class>`: granted only when a verb lists it, composed fail closed, recorded in provenance, revoked on exit).

## Decision

### What the tree holds

Per window, kept incrementally as opcodes arrive and published never (no event, no topic, no polling). Nothing runs for a reader until a reader asks.

- Per view: token, parent, name, `frame` (parent coordinates), `abs` (window coordinates), `visible` (what ancestors do not clip, or null), flags, `hidden` and `effectively_hidden`, `clipped`, `navigable` (B_NAVIGABLE), and the counts of drawing calls it made by kind (`drew`).
- Per view, the text it drew: each `DrawString` run with its string, its origin in the view, its advance, its size and its box in window coordinates. A repaint replaces the runs it covers: when the server paints a view's dirty rect, the runs whose draw-time box intersects it are dropped, and the client's redraw re-adds what is still there. So typing, a label changing and scrolling a list show the current text, not a log of every text ever drawn. Bounded at 512 runs and 64 KiB per view.
- Per window: title, kind (`window`, `modal`, `menu`), size, active, visible. Units are layout units, BeOS-inclusive (a 10 wide view has `right = left + 9`).
- `enabled` and `focused` are **null**. Neither is ever sent to the server (focus is `BWindow` state; a disabled control is just drawn greyer). They are in the schema as null so a consumer cannot mistake absence for false.

### Role: derived from what the protocol carries, and only that

The server sees a name, flags, children and draw calls, never a C++ type. Rules, strongest first; every node carries `role`, `role_confidence` and `role_basis` (the evidence in words) so a reader can discount it.

| Role | Rule | Confidence |
|---|---|---|
| `window-root` | created by `AS_VIEW_CREATE_ROOT` | `protocol` |
| `scrollbar` | named `_VSB_` or `_HSB_`, which is what `BScrollBar` names itself | `convention` (an app can imitate the name) |
| `control` | `B_NAVIGABLE` is set: it takes keyboard focus (BControl, BTextView, BListView, the text field inside a BTextControl) | `inferred` |
| `group` | has children, drew no text | `structural` |
| `text` | drew text and **nothing else** (no bevel, line, box, gradient, bitmap), no children, not focusable | `inferred` |
| `unknown` | everything else | `none` |

There is no `button`, `checkbox` or `radio`. A button draws rounded rects and a label, a checkbox draws a box and a label, a selected radio button draws an ellipse and an unselected one does not; that is a fingerprint of `be_control_look`, which an app or the theme can change, not a signal. A wrong role is worse than an absent one, so a `BButton` and a `BCheckBox` are both `control`, and the name and text (`press` / `Press me`) say the rest. Run against the kitchen sink the first draft of the `text` rule called the menu bar `text` (it drew only strings, lines and gradients, and does not take focus); the rule now requires that nothing but strings was drawn, and `bar` is `unknown`. `demos/a11y-tree/run.sh` asserts that.

### Where it is served, and the JSON

By the application itself, on the bus, as a service it registers when launched with `AURORA_A11Y_SERVICE=<name>` (the launcher sets it; without it the app never joins the bus for this and the code path never runs). This is the `haiku_ledger` pattern: a `BApplication` that is a bus service, over the existing `BusClient`; no second channel. Three verbs, `kits/interface/a11y_bus.cpp`:

- `tree@v1 {window?, root?, depth?, text?}`: every window, or one, or the subtree under a view; `depth` -1 is all, 0 is just the start; `text:false` leaves the text out.
- `node@v1 {window, token}`: one view, its children as tokens.
- `text@v1 {window, token, deep?}`: the text of one view, or with `deep` its descendants, in reading order (rows by baseline, left to right), as one string and as runs with their owning view.

Reply of `tree@v1` (shape; `aurora ui tree --json` prints it as is):

```
{"schema":"aurora.ui.tree/1",
 "viewer":{"identity":"uid1000:bus","kind":"session","rule":"..."},
 "service":"ui.kitchensink", "owner":"uid1000:iface_kitchensi", "units":"...", "scale":1,
 "windows":[{"index":0,"title":"Aurora Kitchen Sink","kind":"window","size":{"w":536,"h":386},"visible":true,"active":true,
   "root":{"token":3,"parent":null,"name":"fTopView","role":"window-root","role_confidence":"protocol","role_basis":"AS_VIEW_CREATE_ROOT",
     "frame":{"l":0,"t":0,"r":535,"b":385},"abs":{...},"visible":{...}|null,
     "state":{"hidden":false,"effectively_hidden":false,"clipped":false,"navigable":false,"enabled":null,"focused":null},
     "drew":{"strings":0,"gradients":0,...},
     "text":[{"text":"Press me","x":..,"y":..,"width":..,"size":12,"box":{"l":..,"t":..,"r":..,"b":..}}],
     "children":[ ...same shape... ]}}]}
```

`viewer` echoes the sender the bus stamped and the rule applied, as the index does. The text is decoded in no way by the service: a Rust compositor, a Go CLI and an MCP tool read plain JSON. The CLI is `aurora ui tree|node|text APP` (`shell/launcher/ui.go`); every string in a reply goes through the same `clean()` as other untrusted text there, so a window cannot put an escape sequence on the terminal.

### Who may read: the capability

`ui:read`, in 0014's grammar: a manifest declares `[capabilities] ui = ["read"]`; a verb lists `ui:read` in its `capabilities`; the validator refuses a verb that lists a class the manifest did not declare; a newly requested class is a consent-requiring diff. `contracts/manifest` knows one class, `read`; anything else is refused by name.

Unlike a path or a device it has **no effect in the domain's namespace**. The grant is a bus handle. The controller (`controller/internal/invoke/uigrant.go`) mints, per invoke, a handle whose resource is `ui:read:<the domain's bus identity>`, records it in provenance (`ui`, `ui_for`, `cap`, so `controller provenance --verify` recomputes the resource), and revokes it when the invoke ends. It is refused, not dropped, when it could not be enforced: no capability client, domain tooling unavailable, or the domain not attested by the bus (a handle naming an identity the bus does not stand behind is a grant the record claims and nothing honours).

The tree service decides each request from two things, both the bus's and neither the payload's: the sender busd stamped, and busd's own handle table (`bus.inspect@v1`). Fail closed at each step:

1. An identity that is neither a `domain:` nor a `uid<N>:` session identity: nothing.
2. A **session** (`uid<N>:<comm>`): the whole application, with no handle. The user's own session sees everything of theirs, as in `index/access.go`.
3. A **domain**: only its **own** application's tree (the identity this service registered under is the domain's), decided before the handle is looked at, so a refusal tells another domain nothing; and only while the table holds a live `ui:read:<its identity>` **whose minter is a session identity**. A domain may mint any resource name on the bus, but busd stamps the minter, and a handle minted by a domain is not accepted.
4. If busd cannot be asked: no.

A refusal is answered to its caller and **not logged**, so a caller that cannot read cannot fill the append-only log by asking (the lesson of the supervisor's liveness pings). A served read is published as `ui.read.served` (who, which verb, how many windows, how many bytes of text; never the text). **Replies are `ephemeral`**: a reply holds every string a window drew, and the log is kept and replayed. `run.sh` greps the log bytes for text it just read and finds none. Before this was found by running it the replies were logged, and the text of every read was in the log.

### What it proves, run

`demos/a11y-tree/run.sh`, 84 checks, against the real kitchen sink on a headless compositor and a real busd, then two real attested domains (bwrap, cgroups, `--attest-domains=require`):

- The tree names the four tabs, the buttons, the checkbox and radio labels, the group-box titles; the text field's content after typing `ab` through the compositor's keyboard (`Name: ab`, label `Hello, ab!`); the text of a `BTextView` after typing `xyz`, as a second line while the first stays once.
- A node's frame equals the kit's own `CTL` report for 11 controls exactly, the tab labels fall inside `BTabView::TabFrame`, and a text box encloses the glyph pixels measured in the compositor's capture (`pix.py dark`) for four labels, before and after a click changed one, for the text field and for the text view.
- Nothing is on the bus until asked: no log message from the app after launch and a full layout walk.
- Capability, both directions: beta without `ui:read` is refused `no_grant` on `tree`, `node` and `text`; with the controller-style mint it reads its own tree; alpha is refused `other_domain` without a grant and with its own grant while beta's is outstanding; beta holding `ui:read` cannot read the host's application; a handle beta minted for itself is refused (minter is beta); after revoke beta is refused again; the user's session reads each.
- `controller/internal/invoke/uigrant_test.go` against an in-process busd that enforces capabilities: a verb listing `ui:read` gets exactly one handle for the attested identity, minted by the session identity, present while the app runs and gone after; an unlisted verb gets none; an unattested domain or a missing capability client fails the invoke before the run.

## The weakest point

**The read is only as closed as the session socket, and every windowed app today is on it.** A process that reaches the session socket is "the session" (the same trust statement as `index/access.go`), and 0012 records that windowed apps run unsandboxed. So any same-uid process, which is every host app, can ask any host app that opted in for its whole tree and gets it with no handle. `ui:read` closes a *domain* from other domains and from the user's apps; it does not close the user's apps from each other, and an agent whose tool runs as a session process (the MCP gateway does) reads what the session reads unless the gateway withholds the tool, which is policy outside this record. The opt-in (`AURORA_A11Y_SERVICE`) is the only thing between a password manager and that, and it is chosen by whoever launches the app.

Three weaker points that are properties of the tree, not the gate, and each was seen running:

- **It is what was drawn, not what the app holds.** The server sees drawn text and not off-screen model state: a `BListView` of 40 items has 14 in the tree until it is scrolled, a hidden tab page keeps the text it last drew (and can be stale, since a hidden view draws nothing), a `BCardLayout` card never shown has none. An app that draws text into a bitmap, or draws its own widgets with `FillShape`, is a rectangle with no words. This is the price of reading from the pipe instead of from the objects.
- **Roles are inferred**, and mostly `control` or `unknown`. A screen reader built on this cannot say "button".
- **The enforcer is the app being read.** The service runs in the application's process, so a host app in the session is trusted to apply the rule to domains correctly, and a domain's app enforces for itself. A bug in `a11y_bus.cpp` is a hole; busd does not gate verbs.

## Not proven here

- The compositor's capability indicator (0016) matches exact names as the bus carries them. This grant's resource is `ui:read:<identity>`, which a `ui:read` entry in `[indicator] classes` will not match. A prefix match is a compositor change not made here.
- A real controller invoke of an app that then reads its own tree. The controller half is proved against a fake runner and a real busd; the demo mints with `bus.mint@v1` from the session, which is what `capbus.Mint` sends. No app was launched by the controller with `AURORA_A11Y_SERVICE` set: the launcher does not set it yet.
- Any consumer other than the CLI: no MCP tool, no Rust reader.
- Fractional scale, a resized window, and a window moved by the compositor were not exercised against the tree.
- The attested identity of a domain depends on 0014/bus attestation, which the demo ran in `require` mode; with it off the socket is the identity.

## Open questions

- A screen reader is a domain that should read **other** apps. That needs a stronger class than `ui:read` (`ui:read:any`? scoped by app?) and an answer to what, besides a manifest line, makes the user mean it. Not composed; the service refuses it by construction.
- Whether `enabled` and `focused` can be had without a protocol change: a focus message from `BWindow` to the server would be an addition to Haiku's protocol, which this project otherwise keeps.
- Whether the tree should be asked of the compositor instead, so an app that does not opt in is still readable by the user's assistive tool. That moves the trust the other way.
- Whether session callers should hold a handle too, so that reading is always an act the bus log records.

## Consequences

- Any app on Aurora's Interface Kit is readable, by name, geometry and text, with no code of its own, once it is launched to serve.
- `ui:read` is the first capability that is only a bus handle. Its enforcement lives in the thing being read, not in the sandbox: a different shape from 0014, and its weaknesses are different.
- Costs nothing when unread: a few counters per drawing call and the run list per view, and no bus traffic.
- Replies being ephemeral means a read cannot be replayed from the log; only that it happened can.
