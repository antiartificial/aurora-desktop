# 0005. Target Ginux main, not the bootable-kernel-final tag

- **Status:** proposed
- **Date:** 2026-11-06

## Context

The plan's Ginux track (`docs/toy-desktop-os-plan.md`, "Ginux track (parallel)") assumes Ginux is an independent Rust kernel that boots as `GINUX.EFI` under QEMU/KVM with OVMF. The Abstract describes it as "an independent Rust kernel with a Linux personality and domains as a kernel primitive, first in QEMU with virtio-gpu and Venus". Phase 0 asks for `harness/ginux-up`, which builds `GINUX.EFI`, boots it, starts an Alpine domain and opens a serial console. Its exit demo is "dropping into an Alpine shell on Ginux". Track goals G2, G3 and G5 are all built on the UEFI kernel: virtio-gpu 2D modesetting, virtio-gpu 3D through Venus, and a GPU driver in a userspace driver domain.

Upstream has changed direction. The submodule at `ginux/upstream` (from `https://github.com/gmkurtzer/ginux.git`) is pinned at commit `3f2fe7dfadd4b40e235d982a6a3d386dc6490177`, which `git describe` renders as `bootable-kernel-final-177-g3f2fe7df`. The pin is 177 commits past the final UEFI tag, so the superproject already points at post-UEFI history. Its README says:

> Ginux began as an independent Rust kernel that booted directly through UEFI and ran a Rocky Linux 10 userspace. That research phase is complete.

Current description: "Ginux is a configurable userspace kernel for isolated Linux compatibility domains. It runs as an ordinary Linux process and places an explicit, versioned contract between managed workloads and the host kernel."

The plan was written against the UEFI kernel. Two targets were possible: build the track on the frozen `bootable-kernel-final` tag, or on `main`. The project owner chose `main`.

The project owner intends to build on bare metal eventually. The frozen UEFI kernel is the existing starting point on that route. This retarget postpones the bare-metal work; it does not drop it. The Ginux track still targets upstream `main`, the userspace kernel, and the items lost under Costs are deferred until the bare-metal goal brings them back.

## Findings

Read from `ginux/upstream` (README, Makefile, workspace layout, file listing). Nothing was built, run or tested for this record.

- `Makefile` sets `X86_TARGET := x86_64-unknown-linux-gnu` and `GINUX := target/$(X86_TARGET)/release/ginux`. The `build` target cargo-builds `ginux-init`, `managed-payload`, `baseline-runner`, `runtime-fixture` and `process-native-fixture`. There is no GINUX.EFI target on `main`.
- Workspace crates: `ginux-core`, `ginux-domain`, `ginux-devices`, `ginux-init`, `ginux-kernel-views`, `ginux-process`, `ginux-runtime`, `ginux-vfs`, `ginux-vfs-index`.
- Domain images are Rocky Linux OCI images, built by `fetch-rocky-oci.sh`, `prepare-rocky-domain.sh` and `with-rocky-host.sh`. The plan's "Alpine domain" does not match upstream.
- `docs/` holds `gate-1-foundation.md` through `gate-7-process-runtime.md`, design documents for the userspace kernel. Their contents were not read for this record.
- The UEFI source is kept. The `bootable-kernel-final` tag and the remote-tracking ref `origin/bootable-kernel` are both in the local clone. There is no local `bootable-kernel` branch; the only local branch is `main`.
- The Rust target `x86_64-unknown-uefi` is installed on the project owner's machine. The userspace track on `main` does not need it. It stays relevant to the bare-metal route back, which runs through the UEFI kernel, so this record does not recommend removing it.

## Decision

The Ginux track targets upstream `main` through the `ginux/upstream` submodule. It does not target `bootable-kernel-final`. The UEFI kernel is kept as a frozen reference for the bare-metal route back. No Aurora build step depends on it.

The replacement shape for the Phase 0 Ginux deliverable:

1. Build Ginux with its own Makefile (`make build`). The binary is `target/x86_64-unknown-linux-gnu/release/ginux`, named by the `GINUX` variable.
2. Run it as an ordinary host process on Linux. QEMU, KVM and OVMF are not part of this path.
3. Start a managed domain from it. The exact invocation is not confirmed here. It must be read from upstream's README and Makefile when the harness is written.

`harness/ginux-up` changes from "build GINUX.EFI, boot under QEMU/KVM with OVMF, start an Alpine domain" to "build Ginux, run it as a host process, start a managed domain". Its name and its exact interface are not decided here.

The Phase 0 exit demo, "a terminal typing inside the nested compositor, then `harness/ginux-up` dropping into an Alpine shell on Ginux", is no longer valid. This record does not define its replacement. The Phase 0 exit demo must be restated before Phase 0 closes.

G1 survives in changed form: run the Go bus binaries (`busd`, `bus`) inside a Ginux domain on Linux, with no QEMU in the loop, and log every unimplemented or partial syscall, as the plan intends.

## Costs

These are deferred, by their plan names. None is being worked on now. Each returns with the bare-metal goal, which is the route back. Nothing here is dropped, and the deferral is recorded here rather than left implicit.

- **G2 (Pixels on screen).** virtio-gpu 2D, DRM/KMS ioctls, dumb buffers, modesetting, and `/dev/dri/card0` with its `/sys/dev/char` entries. Deferred. Not built on `main`, because a userspace kernel mediates syscalls and does not own `/dev/dri`. The Pixman compositor-in-a-domain exit waits with it. Bare metal returns it, because a kernel that owns the device can expose `/dev/dri` directly.
- **G3 (GPU acceleration with Venus).** The virtio-gpu 3D ioctls, PRIME, syncobjs and sync_file fences, and the milestone chain `vulkaninfo` lists the 4090, `vkcube` spins, `glxgears` through Zink, the compositor with glass. Deferred. Not being worked on. The plan already gave G3 an abort condition and said the track's value is G4, not GPU acceleration in a VM. This decision takes that conclusion as settled for the Ginux track. It does not close GPU acceleration on bare metal, which is the route back to it.
- **G5 (Drivers as servers).** The virtio-gpu driver moved into a userspace driver domain with device reset and a TDR-style fence watchdog, and its demo "kill the GPU driver mid-frame; the compositor flickers and recovers". Deferred. It returns with bare metal, which needs a kernel that owns the device to reset it and run the fence watchdog.
- **After that.** The LinuxKPI shim, Nvidia's open kernel module on bare metal, and ARM64 and Apple Silicon via Asahi. Deferred. These also need a kernel that owns the hardware, so they return with the bare-metal goal. The ARM64 and Asahi item may need its own decision when it is resumed.
- **Abstract framing.** "one design runs from the kernel to the pixels" no longer holds on the current track. Linux remains the kernel there. Ginux is a contract layer inside a Linux process, and the drivers, the GPU and the kernel under the desktop stay Linux. Bare metal is the route back to that sentence.

These survive or improve:

- **G1** survives and gets simpler, as described under Decision.
- **G4 (Ginux kernel adapter)** survives and improves. Ginux's stated premise is isolated domains behind an explicit, versioned contract. That is a more direct second backend for contract 3 than the bootable kernel was. Crate names suggest where each contract 3 operation might live. The crate APIs were not read, so this is only a starting map:

  | Contract 3 operation | Candidate in upstream | Status |
  | --- | --- | --- |
  | Create / destroy domain | `ginux-domain` | name only |
  | Restrict filesystem | `ginux-vfs`, `ginux-vfs-index` | name only |
  | Restrict syscalls | `ginux-kernel-views` | name only |
  | Grant capability (device) | `ginux-devices`, `ginux-domain` | name only |
  | Snapshot / clone / roll back | none evident | gap or unseen |
  | Shared memory | none evident | gap or unseen |
  | Suspend / resume | none evident | gap or unseen |
  | Resource budget | none evident | gap or unseen |

- **Syscall-trace diffing** still applies and is better here. The Ginux side and the native Linux side run on the same host, so the same tracing tools see both. The first differing line is still the bug.
- **Reversibility.** The UEFI source stays in the clone, so the track can return to it. The refs and constraints that keep the bare-metal route open are listed under Preserving the route back.

## Preserving the route back

These must hold for the bare-metal goal to stay reachable:

- The `bootable-kernel-final` tag and the remote-tracking ref `origin/bootable-kernel` must stay reachable. Both are in the local clone today; there is no local `bootable-kernel` branch, only `main`. The submodule pin must not be allowed to drop them, and any re-clone must avoid `--depth` and `--single-branch`, either of which would omit the tag, the branch, or both.
- The Rust target `x86_64-unknown-uefi` stays installed. It is the target the bootable-kernel route would build for, so it is not surplus.
- Contract 3 (the kernel adapter contract) must not assume that its second backend is a userspace process on Linux. If a bare-metal backend is still intended, every contract 3 operation must stay expressible by a kernel that owns the hardware. No operation may be defined only as something a Linux host provides, such as a host-process mechanism or a host syscall filter. The Phase 2 kernel adapter record, which the plan defers, must state this constraint and check each candidate operation in the G4 table against it. This record does not write that check.

## Open questions

- Is Ginux's domain contract rich enough to back contract 3's seven operations? It is unverified, and nobody has run it. Four operations have no obvious home in the crate list (snapshot, shared memory, suspend/resume, budgets). The question is answered by a Ginux spike. The phase that owns that spike is not yet assigned, because the Ginux track section has not been rewritten.
- Are Rocky Linux 10 OCI images acceptable in place of Alpine for domain images? Upstream uses Rocky. The plan says Alpine. The question affects the Phase 0 harness and the Phase 6 image-format question ("OCI layers, or a Haiku-style single mountable image?").
- How do `gate-1-foundation.md` through `gate-7-process-runtime.md` map onto the plan's phases? The gate documents were not read for this record. They must be read before the Ginux track is rewritten.
- Should the project track Ginux `main` as a moving target, or pin and bump deliberately? The submodule is pinned at `3f2fe7df` today. No bump policy exists, and none is decided here.
- What is the replacement Phase 0 exit demo? Not defined here.
- Is bare metal reached by reviving the frozen Ginux bootable kernel, by a different kernel, or by running the plan's userland directly on Linux on the target machine? These are materially different routes. Reviving the bootable kernel brings back the UEFI boot path, whose state at the tag was not assessed for this record. A different kernel needs a new contract 3 backend. Running the userland on Linux keeps Linux as the kernel on that machine. The record does not assume one. The phase that answers it is not yet assigned.

## Consequences

Follow-up work, none of it done by this record:

- **The plan needs rewriting.** The Abstract's Ginux sentence, the Phase 0 goal, deliverables, toolchain list (QEMU 10+ with KVM, virglrenderer), exit demo and task list, the "Ginux track (parallel)" section (G1 to G5 and "After that"), the Non-goals entry on bare-metal Ginux, and the Risks rows for Venus. This record supersedes the Ginux track section until it is rewritten.
- **The Later table needs review.** Its Runtimes row says Wine/Proton "needs `ntsync` on Ginux" and Waydroid "needs binder". Under this decision both are host Linux kernel features, not Ginux features.
- **Record 0004 needs an edit.** It is still `proposed`, so it can be changed in place. Its item 5 says the VM harness pattern is adapted for `harness/ginux-up` with the boot target changed to "Alpine on Ginux" and Venus added. Both statements are now wrong.
- **The kernel adapter record moves to 0006.** The README says the adapter record takes the next free number when Phase 2 writes it. 0005 is now this record, so the adapter record is 0006. Its Ginux column is filled from upstream's contract surface, not from a native kernel.
- **The portability claim narrows.** Contract 3 says "nothing above the kernel adapter knows which kernel it runs on". Under this decision the Ginux backend is still a Linux process, so the seam separates the userland from a Linux-hosted contract. It no longer separates two kernels on the current track. The contract 3 constraint under Preserving the route back keeps the seam able to separate them when bare metal returns.
- **Pin policy.** Until the open question is decided, the superproject's pin of `ginux/upstream` at `3f2fe7df` is what the Ginux track builds against.

## What would change this

- A Ginux spike shows that the domain contract cannot run `busd` and `bus` as domains (G1). The track's first exit fails on `main`.
- Upstream drops the userspace direction on `main`. The track then falls back to `bootable-kernel-final`, which remains in the clone.
- The project owner decides to restore GPU acceleration as a goal. That needs a kernel-level design, and it would reopen G2, G3 and G5.
- The bare-metal route resumes, with the route chosen under Open questions, when either of these holds. First, a contract 3 operation is shown to be impossible for the userspace kernel on `main`, not merely unimplemented; the gap rows of the G4 table are the candidates. Second, the desktop on the current track is good enough that owning the hardware becomes the next real goal. The project owner decides which.
