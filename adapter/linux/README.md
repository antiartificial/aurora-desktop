# adapter/linux: Linux backend of the kernel adapter (contract 3)

A `domain` CLI and a small Go library (`internal/dom`) that implement the
unprivileged half of contract 3 on Linux with bubblewrap, user namespaces,
cgroups v2 and Btrfs. Pure standard library, no root, no dependencies.

    go build ./... && go test ./... && ./demo.sh

Requires `bwrap`, and for snapshots `btrfs-progs` on a btrfs data dir. The
data dir is `$AURORA_DOMAINS_DIR` or `${XDG_DATA_HOME:-~/.local/share}/aurora/domains`.

## Operations and their Linux mapping

| Contract 3 operation | Command | Linux mechanism | Status |
| --- | --- | --- | --- |
| Create / destroy domain | `create`, `destroy [--keep-data]` | dir tree with `config/ data/ cache/` as btrfs subvolumes (plain dirs elsewhere, said in the output); metadata in `domain.json` | implemented |
| Run in domain | `run <n> -- cmd` | `bwrap`: new user, pid, ipc, uts, cgroup (and net) namespaces, tmpfs root, caps dropped, new session, read-only `/usr` + merged-usr links, minimal `/etc` | implemented |
| Restrict filesystem and syscalls | (none) | the mount namespace is the filesystem restriction; Landlock and seccomp are **not** applied | partial (mount ns only) |
| Grant file / folder | `grant <n> <path> [--ro\|--rw]`, `revoke` | bind mount of exactly that path at the same absolute path | implemented |
| Grant net host | `grant <n> --net <host>` | any net grant skips `--unshare-net`, so the domain shares the host network | partial (coarse: host name recorded, not filtered) |
| Grant device | (none) | would be a `--dev-bind` of one node | not built |
| Snapshot / roll back | `snapshot [--tag]`, `rollback` | `btrfs subvolume snapshot` of all three subvolumes; rollback deletes the live subvolume and re-snapshots the snapshot (so it can be reused) | implemented on btrfs, clear error elsewhere |
| Resource budget | `limit --memory --pids --cpu` | cgroup2 files in a per-domain cgroup under the user's delegated `user@UID.service`; `run` starts the domain inside it (`CLONE_INTO_CGROUP`) | partial: see below |
| Identity | `exec-id`, `sockpath`, `cgroup`, `nsid` | see below | implemented (MVP) |
| Suspend / resume | (none) | cgroup freezer (`cgroup.freeze`) is in the same delegated tree and would work; not built | not built |
| Shared memory | (none) | memfd + seals; belongs to the bus work | not built |
| Show / list | `list`, `show` | reads metadata | implemented |

### Resource limits on this host

systemd delegates only the `pids` controller to the user manager here
(`cat /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers`
prints `pids`). So `--pids` is enforced and tested; `--memory` and `--cpu`
are recorded in the metadata but **cannot be enforced unprivileged**. `domain
limit` prints `NOT ENFORCED` per controller with the reason and exits 3, and
`domain run` prints a warning each time. Enabling them needs root once, e.g. a
drop-in `[Service] Delegate=memory cpu io pids` for `user@.service`. This was
not attempted (no sudo).

## Identity and SO_PEERCRED

The bus derives the sender's identity from the connection, never from a
message. Chosen mechanism: **a per-domain socket directory**. `domain run`
creates `$XDG_RUNTIME_DIR/aurora/domains/<name>/` (mode 0700) and bind-mounts it
into the domain at `/run/aurora`; the domain gets
`AURORA_BUS_SOCKET=/run/aurora/bus.sock`. The bus listens at
`domain sockpath <name>` (`<dir>/bus.sock`) once per domain. A connection
accepted on that listener comes from a process that was given that directory,
i.e. from that domain; the bus labels it with `domain exec-id <name>`
(`domain:<name>:<16-hex-id>`) and reads `SO_PEERCRED` for pid/uid as extra
evidence (pid is translated into the bus's pid namespace). The bus side is
implemented: `busd --domain "$(domain exec-id N)=$(domain sockpath N)"` (or the
`bus.domain.add@v1` control verb, which the controller calls on create and
`bus.domain.remove@v1` on destroy). `bus/demo-identity.sh` runs it end to end
with two real domains; the bus README has the capability handles that hang
off that identity. The test suite
connects from inside a domain to a host-side listener on that socket.

### Kernel-attested membership (`domain cgroup`)

Every `domain run` now starts the sandbox inside the domain's own cgroup
(`<user@UID.service>/aurora-domains/<name>`, created if missing, with or
without limits, via `CLONE_INTO_CGROUP`). `domain cgroup <name>` prints that
path in the form `/proc/<pid>/cgroup` uses (not `/sys/fs/cgroup/...`), for the
bus to register: `busd --domain ID=SOCK --domain-cgroup ID=$(domain cgroup N)`
or `"cgroup"` in `bus.domain.add@v1`. The bus then reads SO_PEERCRED's pid and
`/proc/<pid>/cgroup` and refuses a connection from outside it
(`bus/README.md`, "Attestation"). No controller is needed, only a delegated
cgroup subtree; without one `domain cgroup` fails with the reason and `run`
warns `running WITHOUT a domain cgroup`. Tested (`TestRunPlacesProcessInDomainCgroup`):
a python process started by `Run` connects to a host listener and its
SO_PEERCRED pid's cgroup is the domain's; a host process's is not.

The cgroup is named after the domain only, so two stores on one host with a
domain of the same name share it. The path is as seen from the host cgroup
namespace; a bus daemon in another cgroup namespace would see different paths.

### Namespace identity (`domain nsid`)

A process in the cgroup is not necessarily the domain: the cgroup directory is
owned by the host uid, so a same-uid process can write its pid into
`cgroup.procs`. A process started by `domain run` is additionally in bwrap's
own pid, mount, ipc and uts namespaces, which the kernel names by the inode of
`/proc/<pid>/ns/<type>`. `domain run` starts bwrap with `--info-fd` (to learn
the sandbox init pid) and `--block-fd` (to hold the payload), reads
`/proc/<init>/ns/{pid,mnt,ipc,uts}`, records `{pid, start time, ids}` in
`domain.json` (`ns`), and, if `AURORA_BUS_SOCK` is set, sends
`bus.domain.ns@v1` to that daemon BEFORE releasing the payload, then removes
the registration when the run ends. `domain nsid <name>` prints the live run's
`type:inode` pairs and fails if the recorded init pid is gone (the identity is
PER RUN: every run makes new namespaces, and pinning them would need
CAP_SYS_ADMIN, so there is no per-domain namespace). The user namespace is not
recorded: bwrap swaps it after `--block-fd` releases (when it sets up `/dev`),
so its inode is not the payload's at that point. A registration failure is a
warning; a bus that attests namespaces then refuses the domain. Tested:
`TestNSIDMatchesTheRunningPayload`, `TestRunRegistersWithBus` (registration
precedes the payload, removal follows).

Weaknesses, stated plainly:

- Membership is attested, intent is not. A same-uid host process can no longer
  become the domain by writing to `cgroup.procs` alone (the bus also compares
  namespaces; `bus/demo-identity.sh` 8c refuses it). It CAN still become the
  domain by entering the sandbox's namespaces: it can open `/proc/<init>/ns/*`
  (same uid), `setns` into the owning user namespace (full capabilities in a
  namespace its uid created), then the pid/mnt/ipc/uts namespaces, join the
  cgroup and fork (`demo-identity.sh` 8f runs it and it works on this host).
  A different uid for the sandbox (subuid/root) or a policy that denies that
  access would close it; neither is available unprivileged.
- A passed file descriptor still lets the receiver speak as the domain.
- Any process the domain starts is in the cgroup and is that domain; the bus
  cannot tell the app from a shell it spawned.
- A domain given another domain's socket directory still cannot use it: its
  processes are in the wrong cgroup.
- `AURORA_DOMAIN_ID` in the environment is a convenience, not an attestation:
  the domain can read and forward it.
- Without cgroup delegation (no systemd user session) there is nothing to
  attest with and the bus falls back to socket identity (loudly; or refuses
  under `--attest-domains=require`).

## What is and is not isolated (honest caveats)

This is a toy. bwrap with user namespaces is real isolation, and the test suite
proves the default domain cannot read a file in `$HOME`, a granted file is
readable alone, `--ro` grants are not writable and there is no network by
default. Not done:

- No Landlock, no seccomp filter: the domain can make any syscall the kernel
  exposes to an unprivileged userns (the attack surface of userns itself,
  and kernel bugs, are in scope for a determined attacker).
- Net grants are all-or-nothing and share the host network, including
  abstract unix sockets and host loopback services.
- `/etc/passwd`, `group`, `nsswitch.conf`, `ld.so.cache` (plus resolv.conf,
  hosts and certificate stores with a net grant) and all of `/usr` are
  readable. `/usr` is not secret, but it is not nothing.
- Granting a directory exposes everything below it; a granted path is
  canonicalised (symlinks resolved) when granted. `grant --exact` is for a
  caller that already resolved and authorised the path (the controller): it
  binds the path as given and refuses it unless it is absolute, clean and
  already canonical, so a symlink swapped after the caller's check fails the
  grant instead of redirecting it. A swap between the grant and `run` (bwrap
  resolves again) is not closed; that needs fd-based binds.
- Running domains are not stopped by `destroy`; the data tree is removed from
  under them. Metadata writes are atomic but not locked across processes.
- Btrfs: unprivileged `btrfs subvolume delete` fails (EPERM) on non-empty
  subvolumes, so `destroy` and `rollback` empty then remove them. A nested
  subvolume created inside `data/` would need the `user_subvol_rm_allowed`
  mount option. Subvolume deletion of snapshots is the same.
- `/usr/sbin` is often not on an unprivileged PATH; the tool also probes
  `/usr/sbin/btrfs`.
