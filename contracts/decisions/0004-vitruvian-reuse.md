# 0004. What the Vitruvian OS tree contributes to Aurora

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Vitruvian OS (VOS) is a separate, existing project by the same author at `/home/arti/projects/Vitruvian`. It is a BeOS/Haiku-API desktop on a Linux kernel. Its tree contains a DRM/GBM display backend that runs on virtio-gpu, a software cursor, partial region updates, a fallback chain, a GLES2 compositor, and a custom kernel module set called Nexus.

The plan (`docs/toy-desktop-os-plan.md`, Phase 0) asks for a decision on each part: harvest it, port it behind the new kernel adapter, or retire it. The new project uses Wayland with Smithay for the compositor, a Go bus, and Linux isolation primitives (bubblewrap, namespaces, cgroups v2, Landlock, seccomp, Btrfs). Phase 2 builds the kernel adapter. Phase 7 targets real Linux apps, not BeAPI binaries.

## Findings

Scope and layout (read-only inspection, branch `test/gbm-with-fixes`, HEAD `3145eee8`):

- Git history has 976 commits. Tracked files number 12,187. Tracked C, C++ and headers number about 5,500 files and 1.2 million lines. The tree has no Go or Rust. Shell is 20 files and CMake is 13.
- Top-level subsystems under `src/`: `kits` (about 308k lines, the BeAPI libraries), `servers/app` (about 84k lines, app_server), `apps`, `add-ons`, `preferences`, `tests`, `system` (including `libroot2`, 14k lines), and `libs`. Public headers live in `headers/os` (about 35k lines of Be API headers).
- Working tree: `git status` shows two modified tracked files (`build-gbm-chroot.sh`, `data/debian/postinst`) and several untracked top-level directories (`apps/`, `add-ons/`, and others). They look like build output of the CMake tree, but I did not verify that. The Nexus directory is reported as untracked.

Display backend (`src/servers/app/drawing/interface/linux/`, 4,228 lines in `gbm/`, `drm/`, `fbdev/`):

- `gbm/GBMHWInterface.cpp` (985 lines) and `drm/DrmHWInterface.cpp` (514 lines) subclass `HWInterface`, the app_server contract. They use `display_mode`, `BRect`, `BLocker`, `BAutolock`, `status_t`, `sem_id`, `thread_id` and `OS.h`. They are coupled to app_server by inheritance.
- `drm/modeset.c` (731 lines) includes only libdrm and libc headers and uses no Be types. The KMS code is separable.
- `gbm/GLCompositor.cpp` (551 lines) has 112 EGL, GL and GBM calls and no Be types in its own body. It takes its inputs through `CompositorLayer` and `RenderingBuffer` from the base classes.
- `gbm/GBMBuffer.cpp` carries a virgl-specific workaround. It always creates buffer objects with the WRITE flag and flushes with `gbm_bo_write`, because mapped LINEAR buffers did not reach GPU memory on virtio-gpu (commit `967a09fb`).
- `LibInputEventStream` and `GBMHWInterface` obtain device access through `libseat` (`fSeat`). That is a session-based fd grant, which is close to the capability model in Phase 2.

The GPU path is not a working asset. The code and its history show it is unproven:

- `GBMHWInterface::CopyBackToFront` wraps the GL compositor initialization in `#if 0` (lines 803–808). The live path is the software path: base-class partial copy, software cursor, then a CRTC flip.
- Per-window compositing is off unless `VOS_PER_WINDOW_COMPOSITING` is set. Commit `3145eee8` says it "causes crashes when windows dragged off-screen", and the GPU compositor is "wired but disabled -- virgl glReadPixels returns zeros intermittently". Commit `2c27e877` had enabled it by default, and `3145eee8` reverted that.
- Commit `967a09fb` records that `eglSwapBuffers` fails with `EGL_BAD_SURFACE` on virtio-gpu, which disabled the GL path at that time.
- Per-window logic sits in `src/servers/app/Window.cpp` (61 BeAPI references) and `Desktop.cpp` (64). It is tightly bound to app_server.
- `src/demos/es2gears` uses `BWindow`/`BApplication`. Its commit message describes it as "software-rendered", so it is not a GPU workload.

Nexus (`src/system/kernel/nexus`, submodule from `github.com/Numerio/Nexus`):

- 11 C and header files, about 4,800 lines, in `nexus/`. Packaged as `nexus-dkms`, which builds out-of-tree against the running kernel.
- Provides `/dev/nexus`: BeOS ports, threads, semaphores, named areas. The `nexus_vref` module and `node_monitor.c` translate fsnotify events into BeOS node-monitor messages.
- Its primitives correspond to the Linux mechanisms the plan already chose: ports to the Go bus, areas to memfd, node_monitor to a Phase 9 concern.

Test and VM harness (`test-harness/`, 6 files, 189 lines):

- `run-vm.sh` starts QEMU with `virtio-gpu-pci` (drm mode) or `virtio-gpu-gl` with `-display gtk,gl=on` (gbm mode). It exposes a QEMU monitor socket and a serial log.
- `screenshot.sh` takes a screendump through the monitor. `wait-ssh.sh`, `vm-ssh.sh` and `collect-logs.sh` cover the rest.
- The harness boots the VOS ISO, not Alpine on Ginux. The directory `test-harness/logs` is empty, so the repo holds no run logs.
- No Venus configuration appears in the harness. A grep of tracked files for "venus" matched only unrelated data files (catalogs, PCI and USB ID lists).

Hypothesis check: the working assumption was that BeAPI coupling blocks reuse and that GBM/DRM knowledge and the harness are reusable. That holds in part. The BeAPI coupling is real and structural at the `HWInterface` boundary. The lowest layers (`modeset.c`, the GBM buffer flush rule, EGL platform setup, the libseat fd grant) are not coupled. The GPU compositor is not evidence that the approach works on virtio-gpu, so its value is mostly the negative results above. The 4090 path is not covered by this tree: I did not search the whole tree for NVIDIA-specific code.

## Decision

Per subsystem:

1. **Retire the BeAPI layer** (`src/kits`, `src/servers/app`, `src/system/libroot2`, `headers/os`, the Be apps and preferences). The plan's compositor and bus do not need a Be-compatible API surface. Carrying it would import roughly 400k lines that the plan does not use.
2. **Retire Nexus.** The plan's kernel adapter is built on namespaces, fd passing, memfd and peer credentials. A DKMS module is an extra kernel dependency the plan does not want. Keep `node_monitor.c` as a reading reference for Phase 9 only.
3. **Port the kernel-facing display code behind the kernel adapter, as reference, not as a copied backend.** Take `drm/modeset.c` (KMS mode setting, pure libdrm), the buffer-flush rule from `GBMBuffer.cpp`, the EGL-on-GBM setup from `GLCompositor::Init`, and the `libseat` session pattern. Do not port `GBMHWInterface` or `DrmHWInterface`; they implement the BeAPI contract. Before Phase 4 starts, check whether Smithay's own DRM and GBM backends cover this. That check has not been done here, and if they do, the port shrinks to notes.
4. **Retire the GL compositor and per-window compositing code**, and record the failure modes (virgl `glReadPixels` zeros, `EGL_BAD_SURFACE` on `eglSwapBuffers`, off-screen crash). The Phase 4 renderer choice should not assume this code works.
5. **Adopt the VM harness pattern** from `test-harness/`: serial log capture, a QEMU monitor socket for screen capture, and log collection (`run-vm.sh`, `screenshot.sh`, `wait-ssh.sh`, `collect-logs.sh`). The scripts have not been copied into Aurora. The pattern is to be adapted for `harness/ginux-up` when the Ginux source lands: the boot target changes to Alpine on Ginux, and Venus, which the scripts lack, is added.
6. **Retire es2gears** as a GPU test. Write a new GLES2 workload for the new compositor instead.

## Consequences

- The Aurora repo takes no code from VOS in Phase 0. Phase 2 and Phase 4 work starts from the plan's own design.
- The display knowledge is reused as notes and as small ports. Those ports need their own tests on the virtio-gpu VM. Their correctness is not inherited from VOS.
- Any harness pattern taken from VOS is adapted, not linked. VOS remains a separate project and keeps its own history.
- Nothing from `src/kits` or `src/servers/app` is needed for Phase 1 or Phase 4.

## What would change this

- **Harvest more of the GPU path:** a reproducible run log on virtio-gpu-gl showing the GL compositor producing correct frames, with no zeros and no `EGL_BAD_SURFACE`, across repeated runs. Also a run of the software or DRM path on the 4090.
- **Port the GBM backend whole:** Smithay's DRM and GBM support is shown not to cover the 4090 or virtio-gpu, and VOS's `GBMHWInterface` can be split from `HWInterface` with a small diff.
- **Revisit Nexus:** a measured bus latency or throughput on Unix sockets that misses the plan's interactive target, where kernel ports would fix it. The plan's bus is Go over sockets, so this would need evidence.
- **Revisit the BeAPI retirement:** a requirement that BeAPI binaries run unmodified. That would change Phase 7 and bring the kits back into scope.
- **Revisit the libseat pattern:** a finding that the Phase 2 adapter cannot grant DRM fds through a session, which would make the VOS device-access code more relevant.

## Open checks

- Whether Smithay's DRM and GBM backends fit the 4090 and virtio-gpu (not checked here).
- Whether the NVIDIA path appears anywhere in the VOS tree beyond the GBM directories (not checked).
- Whether the untracked top-level directories in the VOS working tree are build output (not verified).
- Whether Nexus's submodule pointer is committed in the superproject (`git status` and `git ls-files` disagree; not resolved).
