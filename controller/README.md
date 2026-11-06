# controller

`controllerd` loads app manifests, registers one bus service named `registry`, and runs verbs inside isolated domains. `controller` is a thin CLI for driving it. Phase 5 of the plan (registry, provenance v0); Phase 8's supervisor is not here.

    cd controller && go build ./... && go test ./... && ./demo.sh

    controllerd --apps apps[,more/dirs] [--socket S] [--domain-bin P] [--exec id=path ...] [--require-isolation] [--no-gesture-grants] [--timeout 1m]

Manifests are read from `<dir>/<app>/manifest.toml` and validated with `contracts/manifest`. An invalid or duplicate manifest is logged as `SKIPPED <path>: <reason>` and skipped.

## Registry protocol

Requests go to service `registry` with the verb in the envelope's `verb` field. Payloads are JSON.

`registry.apps@v1`, request `{}`:

    {"apps":[{"id":"org.aurora.lumen","name":"Lumen","version":"0.1.0","verbs":["resize@v1","export@v1","histogram@v1"]}]}

`registry.verbs@v1`, request `{"accepts":"image/png"}` (`accepts` optional; omitted means all verbs of all apps):

    {"verbs":[{"name":"resize@v1","app":"org.aurora.lumen","effect":"write","accepts":["image/png","image/jpeg"],"summary":"..."}]}

`registry.describe@v1`, request `{"app":"org.aurora.lumen","verb":"resize@v1"}`:

    {"app":"org.aurora.lumen","verb":"resize@v1","effect":"write","summary":"...","accepts":["image/png","image/jpeg"],
     "input":{"width":"int","height":"int?"},"output":{"image":"image/*"},"capabilities":["path:~/Pictures"]}

`input`/`output` are the manifest's field-to-type maps; a `?` suffix means optional.

`registry.invoke@v1`, request:

    {"app":"org.aurora.lumen","verb":"resize@v1","input":"/abs/path/in.png","args":{"width":800},"output":"/abs/path/out.png"}

Success (response envelope):

    {"ok":true,"output":"/abs/path/out.png","provenance":{"file":"/abs/path/out.png","app":"...","verb":"...","inputs":["..."],"args":{},"isolated":true,"ts":"..."},
     "isolated":true,"grants":[{"path":"/abs/path/in.png","mode":"ro","source":"gesture"},{"path":"/abs/path/out.png","mode":"rw","source":"gesture"}],
     "result":{...},"log":"..."}

`grants` says what the domain was given and how that was authorised (see "Ambient and gesture grants"). `needs_approval:true` appears when an `external` or `destructive` verb ran on a gesture grant. `isolated`, `result` (the app's stdout if it is JSON) and `log` (tail of the app's stderr) are additions; a launcher may ignore them. For a `read` verb (e.g. `histogram@v1`) there is no `output` or `provenance`; the data is in `result`, and passing `output` is an error.

Failure: an **error-kind envelope** whose payload carries both the bus's `code`/`message` and the registry's shape:

    {"ok":false,"error":"...","detail":"...","code":"no_such_app","message":"..."}

(`error` and `message` are the same text.) Codes: `no_such_app`, `no_such_verb`, `bad_request`, `bad_input`, `bad_output`, `unsupported_type`, `bad_args`, `not_implemented`, `app_failed`, `no_output`, `domain_error`, `isolation_unavailable`, `exec_error`, `gesture_grant_refused`. The same shape is used for failures of the other three verbs. `bus` clients see it as a `RemoteError`; the payload is still readable.

Validation before anything runs: `input` and `output` absolute, input an existing regular file, output directory exists, output differs from input, the input's type (sniffed bytes, then extension) matches the verb's `accepts`, and `args` match the verb's `input` schema (no unknown keys, required keys present, `string`/`path`/`int`/`float`/`bool` types checked).

## How a verb runs

The app contract (what the controller expects of an app, chosen to match Lumen's JSON-on-stdin form): an executable `run`, `<last id segment>` (`lumen`) or `bin/<last id segment>` in the app directory, or one given with `--exec id=path`. It is called as `<exe> --verb NAME` with `{"input":F,"output":F,"args":{...}}` on stdin and answers JSON on stdout; exit 0 is success.

Per invoke, in a domain named `app-<up to 11 chars of the id, sanitised>-<16 hex of sha256(full app id)>` (for example `app-org-aurora-921cd3f28d8d99c9`). The readable part is lossy (`.` and `-` both become `-`); the hash of the whole id is always appended, so distinct valid ids such as `org.aurora.lumen` and `org.aurora-lumen` can never share a domain, its `config/`, `cache/`, `data/` or its `data/app` image. (Injective up to a 64-bit hash collision; a 32-character name cannot do better.) Invokes are serialised per domain name, so the lock matches what is shared by construction.

1. `domain create` if the domain does not exist.
2. Copy the app directory (and the `--exec` binary) into the domain's own `data/app`. The app's code is part of the domain, not a grant.
3. The controller creates the single output file (empty; an existing one is truncated; a symlink or non-regular file at that name is refused), then `domain grant <d> <input> --ro --exact` and `domain grant <d> <output file> --rw --exact`. The output's directory is not granted: siblings of the output file are neither readable nor writable, and the directory is not listed inside the domain beyond the granted files. Nothing else is granted. A `read` verb gets only the input. `--exact`: the controller resolves symlinks once, classifies that path and hands those same bytes to the domain, which binds them as given and refuses the grant if the path is not already canonical (a symlink changed since the check), rather than resolving again.
4. `domain run <d> -- /domain/data/app/<exe> --verb ...` with the request on stdin, under the `--timeout`.
5. `domain revoke` both grants, also when the app failed.

So the domain sees `/usr` read-only, its own `/domain/*`, the input file and the output file (nothing around them), and no network. This is what makes decision 0002's claim about wrapped CLI tools true: the tool is run in a domain granted exactly its declared input and output. `controller/internal/service` has tests that run a real bwrap domain: one checks the grants are gone afterwards and an ungranted file is unreadable; the other probes from inside the domain during the invoke that a sibling of the output file is unreadable and unwritable and the output file is writable. Invokes sharing a domain are serialised; different domains run in parallel.

**Finding the domain CLI:** `--domain-bin`, then `$AURORA_DOMAIN_BIN`, then `domain` on `PATH`, then `adapter/linux/out/domain` found upwards from the working directory. Build it with `cd adapter/linux && go build -o out/domain ./cmd/domain`. The domain data dir follows the CLI's (`$AURORA_DOMAINS_DIR`).

**Fallback:** if the CLI or `bwrap` is missing, controllerd warns at start and at every invoke (`WARNING: running ... UNISOLATED on the host`), runs the app directly, and reports `"isolated":false` in the reply, the event and the xattr. `--require-isolation` turns this into an `isolation_unavailable` error instead.

## Provenance v0

For every verb that writes a file, after the file exists, controllerd publishes on `provenance.created`:

    {"file":"/abs/out.png","app":"org.aurora.lumen","verb":"resize@v1","inputs":["/abs/in.png"],"args":{"width":800},"isolated":true,"grants":[{"path":"/abs/in.png","mode":"ro","source":"gesture"},{"path":"/abs/out.png","mode":"rw","source":"gesture"}],"ts":"<hlc>"}

and writes the same JSON to the output file's `user.aurora.provenance` xattr (a failure logs a warning; the event is still sent). `ts` is a hybrid logical clock from the bus's `hlc` package, local to controllerd. A failed invoke publishes nothing. `controller provenance <file>` reads the xattr back. Paths are canonical (symlinks resolved).

## Ambient and gesture grants

Every filesystem grant (the input file `ro`, the output file `rw`) is classified before the domain is touched, and the class is recorded in the reply, the bus event and the xattr.

- **ambient**: covered by a `capabilities.paths` entry in the app's manifest. The user consented to it at install time (decision 0002). The grant must be the declared path or below it, and access is honoured: a `read` declaration covers only `ro` grants, `write` covers both.
- **gesture**: not covered. Legitimate only because the caller asked for it for this one invocation, which is the plan's powerbox model: picking a file grants that file and nothing more. The user's action is the authority, not the manifest.

Enforced today:

- A verb with effect `read` takes no `output`, so it can only ever receive the `ro` input grant (pinned by a test).
- A verb's `capabilities = ["path:..."]` must name a declared capability. This is checked by the manifest validator at load; the app is skipped with the reason (`SKIPPED <path>: ... "path:~/X" is not among the app's requested capabilities`).
- `external` and `destructive` verbs that run on a gesture grant get `"needs_approval":true` in the reply and a `NEEDS APPROVAL` warning in the log. There is no approval UI; the invoke still proceeds.
- `controllerd --no-gesture-grants` refuses any invoke needing a gesture grant with `gesture_grant_refused`, before any domain call. Default off.

The coverage rule lives in `internal/invoke/grants.go` (`classify`); `contracts/manifest` does not export one. `~/` expands from `$HOME`, symlinks are resolved on both sides. Decision record: `contracts/decisions/0007-ambient-and-gesture-grants.md`.

Limitation: the launcher passes whatever paths the user typed or picked, so today almost every grant is a gesture and the manifest does not narrow anything by default. `--no-gesture-grants` shows the distinction is real, but it would break the launcher. Real enforcement waits for the Phase 6 powerbox.

## CLI

    controller [--socket S] apps
    controller verbs [--accepts MIME]
    controller describe <app> <verb>
    controller invoke <app> <verb> --input F [--output F] [--arg k=v ...]   # v is parsed as JSON, else a string
    controller provenance <file>

## Not implemented

- The manifest's `capabilities.paths` are consulted only to classify grants (ambient or gesture). By default a gesture grant is given on request, so a verb declaring `~/Pictures` can still be asked to write anywhere the caller names; only `--no-gesture-grants` refuses that. There is no powerbox to make a gesture a real user action, and no approval UI behind `needs_approval`. The `net`/`device` capabilities are never granted.
- No consent, no installation step, no signature or `image_hash` check, no hot reload of manifests: apps are read once at start.
- The domain has no Landlock/seccomp, no enforced memory/CPU limits and no btrfs snapshot per invoke (all adapter limits, see `adapter/linux/README.md`).
- Grants are persisted by the domain CLI: if controllerd is killed mid-invoke the grants stay until the next invoke of that app re-grants and revokes them. Nothing sweeps stale grants. Domains are never destroyed.
- Granting the input as a single file means a verb with several inputs is not supported; `inputs` in provenance is always one entry. Provenance does not chain to the input's own record, and an app that wants to write its output atomically (temp file, then rename) cannot: the output is a bind-mounted file and a rename over it fails inside the domain, so apps must write it in place (Lumen does, via `os.Create`). If the invoke fails, an output file the controller created is removed; one that already existed has already been truncated. A path swapped between the grant check and `domain run` (bwrap resolves again at start) is not closed, only the controller-to-grant gap is.
- Provenance on the bus is attributed by the bus's MVP `uid:comm` identity, so any same-user process can publish a fake `provenance.created`; the xattr is unauthenticated and can be edited or lost on copy. There is no event log of provenance beyond the bus log.
- Effect classes: read versus non-read decides whether an output is granted; `destructive` and `external` only add the `needs_approval` flag on gesture grants.
- Output size, stdout size and CPU are not capped beyond the timeout; stdout and stderr are buffered whole in memory.
- Tests: the real-domain test skips itself when `bwrap` is absent or the domain CLI cannot be built; Lumen itself is not exercised by `go test` (the demo does that).
