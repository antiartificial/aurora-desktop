The Ginux kernel, an independent Rust kernel with a Linux personality, built in the parallel Ginux track.

`upstream/` is the pinned git submodule of https://github.com/gmkurtzer/ginux.git, at commit `3f2fe7df`. Ginux is now a userspace kernel: upstream describes it as a configurable userspace kernel for isolated Linux compatibility domains, and it runs as an ordinary Linux process.

There is no GINUX.EFI on `main`. The bootable UEFI kernel is frozen at the tag `bootable-kernel-final`. It stays reachable there because bare metal is still a project goal. See `contracts/decisions/0005-ginux-retarget.md`.

`harness/ginux-up` builds the userspace kernel from `upstream/`. It does not boot anything under QEMU.
