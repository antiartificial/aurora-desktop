# 0014. Device grants: the GPU render node and the Wayland socket, enforced from the domain side

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Two things a domain cannot have today are the same problem.

- **The GPU.** `[capabilities] devices = ["gpu"]` appears in `apps/lumen/manifest.toml` and nothing enforced it. Decision 0013 named this as the open question that blocks GPU compute kits.
- **The compositor.** Decision 0012 records that windowed apps run unsandboxed, outside the domain machinery that is this project's whole security argument, because a domain cannot reach the compositor.

Both are "a thing a domain should get only by grant". A domain's `/dev` is a minimal tmpfs (`bwrap --dev`) and its `/run` holds only its own bus directory, so both are absent by construction. What was missing was a way to put one there on purpose, as a grant the controller mints, records and revokes like a path grant.

This record covers the adapter, manifest and controller half. The compositor half is not done (see Open questions) and nothing here should be read as having done it.

## Decision

### The capability name and grammar

`device:<class>`, the grammar the manifest already had: verb `capabilities` entries are `path:<p>`, `net:<h>` or `device:<d>` (`contracts/manifest/validate.go`, `capDeclared`), and `[capabilities] devices = [...]` is the declaration. No new kind was invented. Two classes are composed:

| Class | What it puts in the domain's namespace |
|---|---|
| `gpu` | every `/dev/dri/renderD*` node (`--dev-bind`), and nothing else under `/dev/dri` |
| `wayland` | the compositor's Wayland socket, alone, at `/run/aurora-display/wayland-0`, with `WAYLAND_DISPLAY` set to that absolute path |

`wayland` is a stretch of the word "device": it is a socket, not a node. It was named `device:wayland` in 0012 and keeping that name keeps one grammar. The alternative, a separate `display` kind, would be a second capability kind for one case.

Declaration is a request and a verb's `capabilities` entry is the ask. A manifest that declares `devices = ["gpu"]` gets nothing until a verb lists `device:gpu`; the controller then grants it for that invoke. The manifest validator already refuses a verb that lists a device the manifest does not declare. Lumen declares `gpu` and no verb lists it, so Lumen still gets no GPU, which matches its README ("scaling is CPU-only").

### How it is enforced

- **Adapter (`adapter/linux/internal/dom/devices.go`).** A `Grant` has `kind = "device"` and a `device` class. `domain grant <name> --device gpu|wayland` records it, `domain revoke <name> --device <class>` removes it, and `BwrapArgs` composes the namespace from the recorded grants and only from them. Any other class is refused (`unknown device class`), so a manifest cannot claim a device that nothing composes: the grant fails and so does the invoke. The card node is not a class.
- **Controller (`controller/internal/invoke`).** `planDevices` turns a verb's `device:` capabilities into grants with `source = ambient`, `mode = use`, `path = device:<class>`. They are minted as bus handles with resource `device:<class>`, passed through `Runner.GrantDevice`, recorded in provenance, verified by `controller provenance --verify`, and revoked after the run in reverse order, exactly as path grants are.
- **The Wayland socket's host path** is `AURORA_WAYLAND`, else the name in `$XDG_RUNTIME_DIR/aurora/display`, which `aurora-up` writes once the compositor has chosen its socket. `WAYLAND_DISPLAY` is deliberately not consulted: in a user's shell it names the host's compositor, and falling back to it would hand a domain the one compositor Aurora is not. The path must be a unix socket or it is not bound.

### Render node, not card node

`/dev/dri/card*` is the KMS node. It can set modes, program scanout and become DRM master, which means driving the display. A domain must not hold that: it would be able to take the screen from the compositor. `/dev/dri/renderD*` is the unprivileged node for rendering and compute; it has no modesetting and no master rights. The demo proves the card node is absent even with the grant, and that `card0` is not a grantable class.

### The socket, not its directory

The parent of the compositor's socket is `$XDG_RUNTIME_DIR`, and `$XDG_RUNTIME_DIR/aurora/` holds `approval.key` and `powerbox.key`. That the domain's namespace does not contain them is why they are safe from domains today, so the grant binds the socket file only. `demos/device-grants/run.sh` tests it: with a wayland grant `/run/aurora-display` lists exactly `wayland-0`, `/run` holds only the domain's own directory and that one, `..` traversal and the host paths read nothing, a control `cat` of a file that exists reads it, and no 32-byte file anywhere in the domain has either key's SHA-256.

## What is now enforced

- A domain without the grant has no `/dev/dri` and no compositor socket. The demo lists the directory and stats the path: they are not there to be refused. It does not rely on an error string.
- With the grant the node is the very device (character special file, host's major:minor, `e2:80` here) and the socket is a unix socket a client in the domain connects through. A `testclient` inside a domain mapped a window on Aurora; the compositor frame changed by 90000 pixels; the client's cgroup was `aurora-domains/<name>` and its uid the domain's subordinate uid. Without the grant, the same client exits 101 and maps nothing.
- Both grants are minted, appear in provenance with a cap id, verify, and are gone after the run. Revoking and running again shows the node absent again.

## What a device grant actually permits

**`device:gpu` is not equivalent in strength to a read-only file grant, and must not be read as such.** A path grant names one file and one mode. A render node is an interface to a kernel driver that parses a large, driver-specific command stream from the domain.

- It gives wide access to the GPU: submit work, allocate GPU memory, read back its own buffers. On most drivers there is little isolation between GPU contexts, so another process's use of the GPU, and in the worst case residual GPU memory, is reachable by driver bugs and sometimes by design. We have not measured this for the driver on this machine and do not claim isolation.
- It is a kernel attack surface. The driver runs in the kernel and the domain feeds it untrusted input. A driver bug is a path out of the domain that no bubblewrap flag closes. The user-namespace, seccomp-free domain does not filter the ioctls.
- `--dev-bind` is a bind of the host's device node; the domain's own user namespace does not own it, and the grant is for the node, not for any quota. There is no GPU time, memory or power limit; the cgroup limits cover CPU, memory and pids only.
- It is not a display grant. Rendering to a buffer is not the same as showing it; a domain still needs `device:wayland` for that.

**`device:wayland` gives a domain everything the Wayland protocol allows a client.** It can create windows of any size and content, including ones that look like other apps. It can send and receive clipboard offers on the standard `wl_data_device` interfaces, receive keyboard and pointer events for its own surfaces, bind every global the compositor advertises, and hold any resource a client can hold. It can draw a convincing imitation of an approval dialog in its own window; the approval prompt is drawn by the compositor above all clients and cannot be captured, but a user cannot tell a look-alike apart (0007 names this). It is also a path to the compositor's own bugs: the compositor parses what the domain sends.

## What it does not isolate

- **The GPU from other GPU users.** See above. Not isolated, not claimed.
- **The domain from the compositor's parser.** The compositor treats a granted client as any client. A bug in Smithay, in a protocol handler or in Aurora's handlers is reachable from the domain.
- **One domain's windows from another's.** Nothing in the compositor yet knows which client is which domain, apart from the clipboard rule. Two domains with the grant are two clients to the compositor; they can see only what the protocol lets any client see, but that is not a per-domain policy, only the absence of one.
- **Window identity.** The title, app id and decorations a granted client presents are whatever it says. The compositor does not attribute a surface to a domain, so the title bar cannot yet be trusted to say which app it is.
- **`wl_shm` and dmabuf.** Shared memory the client allocates is mapped by the compositor; a client that truncates it can fault the compositor in some implementations. Not tested here.
- **The GPU node's permissions.** See next section: on this machine the node is bound and not openable.

### The weakest point: the node is present and open() fails here

This is the weakest part of the GPU half and is not fixed.

`/dev/dri/renderD128` here is `crw-rw----+ root:render` with an ACL entry for the login user. The domain's payload runs as its own subordinate uid (`uid.go`), which is neither that user nor in the render group, and `CAP_DAC_OVERRIDE` inside a user namespace does not apply to an inode whose owner is not mapped in that namespace. So with `device:gpu` granted the node is in the namespace and `open(O_RDWR)` fails with `EACCES`. The demo reports that as `UNPROVEN`, not as a pass, and `--strict` makes it a failure. `domain run` prints a warning when it binds a node whose mode excludes the domain's uid.

What it would take, none done, each with a cost:

- A root helper or a udev rule that grants the domain's subordinate uid range access to render nodes. Needs root and a policy on which uids.
- Opening the node in the controller and passing the file descriptor over the domain boundary. Mesa and Vulkan loaders open by path; this works only for clients written for it.
- Running the payload as the login uid for a GPU domain. That breaks the bus's uid attestation (`bus/server/attest.go` refuses a uid mismatch) and the property that no domain shares the host user's uid, so it is not offered.

Until one is chosen, `device:gpu` is enforced as to presence and absent as to usability on a host like this one. On a host where render nodes are mode 0666 it should work; that was not tested.

## Not proven here

- That any compute workload runs. Mesa and the Vulkan loaders usually read `/sys/dev/char` and `/sys/class/drm` to find a device, and a domain has no `/sys`. That is from how those libraries are generally built, not from a run: whether Vulkan or EGL enumerate the device inside a domain was not tried, and could not be while `open()` fails.
- NVIDIA's proprietary driver uses `/dev/nvidiactl` and `/dev/nvidia*`, not a render node. Those are not composed. `device:gpu` here means DRM render nodes.
- Multiple render nodes: all are bound. Choosing one (a discrete versus integrated GPU) is not expressed.
- A real third-party windowed app in a domain: only the compositor's `testclient` was run. The shipped Haiku apps are not launched as domains and still run unsandboxed (0012 still describes them correctly).
- `--no-gesture-grants`, `--require-powerbox` and the approval gate treat device grants as `ambient`. A verb that lists a device is not gated by approval for that reason alone.

## Open questions

- **The compositor half (deferred).** The compositor must recognise a connecting client as a particular domain and apply per-domain policy. The shape exists: the clipboard refusal decides at connect from `SO_PEERCRED` and the cgroup (`compositor/src/clipboard/mod.rs`, `trusted::socket::Allowlist`). What it needs: record the client's domain at connect (cgroup component under `aurora-domains`, the subordinate uid), carry it on the surface so decorations and the approval overlay can show it, and decide per-domain which globals a client gets (it can already be refused `ext_data_control`; it should also be refused `zwlr_screencopy`, input-method and virtual-input globals, and anything else a sandboxed client should not bind). Until then every granted client is as trusted as any other client except for the clipboard.
- **Making the GPU node usable by a domain's uid.** See the weakest point above.
- **Whether `windowed = true` should imply `device:wayland`.** 0012 left it open. Implying it makes a presentation flag security-relevant, so it is still explicit: the app must list the capability. The dock and `launcher` still launch windowed apps as host processes; moving a launch into a domain is controller and shell work, not done here.
- **Whether a GPU grant should ever be ambient.** Today a declared device the verb asks for is ambient, like a declared path. Given the width of the grant, it may deserve a prompt on first use or a consent diff entry of its own (`diffDevices` already treats a newly requested device as consent-requiring on a manifest update).
- **A `device:` class for the camera, the microphone and input devices.** Not composed; the adapter refuses them by name.

## Consequences

- `devices = ["gpu"]` in a manifest now means something, and a class the adapter does not know fails the invoke instead of being a silent claim.
- Aurora can describe a sandboxed GUI app: `device:wayland` plus its path grants. Nothing launches one that way yet.
- The security argument of 0012 stands: windowed apps launch unsandboxed today. This record makes the sandboxed form possible and says exactly how weak the display half still is until the compositor can tell domains apart.
- `aurora-up` writes `$XDG_RUNTIME_DIR/aurora/display`. A stack not started with it has no socket for a `device:wayland` grant and `domain run` says so.
