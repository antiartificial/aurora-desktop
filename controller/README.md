# controller

`controllerd` loads app manifests, registers one bus service named `registry`, and runs verbs inside isolated domains. `controller` is a thin CLI for driving it. Phase 5 of the plan (registry, provenance v0); Phase 8's supervisor is not here.

    cd controller && go build ./... && go test ./... && ./demo.sh

    controllerd --apps apps[,more/dirs] [--socket S] [--domain-bin P] [--exec id=path ...] [--require-isolation] [--no-gesture-grants] [--require-powerbox] [--powerbox-key F] [--timeout 1m] [--approval-timeout 60s] [--auto-approve]

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

    {"app":"org.aurora.lumen","verb":"resize@v1","input":"/abs/path/in.png","args":{"width":800},"output":"/abs/path/out.png",
     "ticket":"<optional powerbox ticket for the input>","output_ticket":"<optional, for the output>"}

Success (response envelope):

    {"ok":true,"output":"/abs/path/out.png","provenance":{"file":"/abs/path/out.png","app":"...","verb":"...","inputs":["..."],"args":{},"isolated":true,"ts":"..."},
     "isolated":true,"grants":[{"path":"/abs/path/in.png","mode":"ro","source":"gesture"},{"path":"/abs/path/out.png","mode":"rw","source":"gesture"}],
     "result":{...},"log":"..."}

`grants` says what the domain was given and how that was authorised (see "Ambient and gesture grants"). `approval` (see "Approval") appears for `external` and `destructive` verbs; `needs_approval:true` when approval was required and given. `isolated`, `result` (the app's stdout if it is JSON) and `log` (tail of the app's stderr) are additions; a launcher may ignore them. For a `read` verb (e.g. `histogram@v1`) there is no `output` or `provenance`; the data is in `result`, and passing `output` is an error.

Failure: an **error-kind envelope** whose payload carries both the bus's `code`/`message` and the registry's shape:

    {"ok":false,"error":"...","detail":"...","code":"no_such_app","message":"..."}

(`error` and `message` are the same text.) Codes: `no_such_app`, `no_such_verb`, `bad_request`, `bad_input`, `bad_output`, `unsupported_type`, `bad_args`, `not_implemented`, `app_failed`, `no_output`, `domain_error`, `isolation_unavailable`, `exec_error`, `approval_denied`, `approval_timeout`, `approval_unavailable`, `gesture_grant_refused`, `powerbox_required`, `ticket_unavailable`, `ticket_malformed`, `ticket_bad_signature`, `ticket_expired`, `ticket_reused`, `ticket_wrong_app`, `ticket_wrong_verb`, `ticket_wrong_mode`, `ticket_wrong_path`. The same shape is used for failures of the other three verbs. `bus` clients see it as a `RemoteError`; the payload is still readable.

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

## Bus identity of app domains

When controllerd ensures an app's domain it asks the adapter for `domain exec-id` and `domain sockpath` and registers the pair with busd (`bus.domain.add@v1`, over its session-socket connection). `domain run` binds that socket's directory into the domain at `/run/aurora` and sets `AURORA_BUS_SOCKET=/run/aurora/bus.sock`, so an app that dials it appears on the bus as `domain:<name>:<hex>` instead of `uid1000:<comm>` (`cmd/busprobe` and `demo.sh` show this with a real bwrap domain). Registration is once per domain per controllerd run; "already registered" counts as success. On SIGINT/SIGTERM controllerd unregisters what it registered (`Invoker.Close`/`Teardown`); domains themselves are never destroyed by the controller.

Failure is not fatal: busd refusing, the identity lookup failing or no bus all log a WARNING and the invoke proceeds. The record then has `domain_attested:false` (or no `domain`), and the app cannot reach the bus as its domain. The socket is the identity, with the weaknesses in `bus/README.md`.

## Provenance v0

For every verb that writes a file, after the file exists, controllerd publishes on `provenance.created`:

    {"file":"/abs/out.png","app":"org.aurora.lumen","domain":"domain:app-org-aurora-921cd3f28d8d99c9:4b6f615504be4fd5","domain_attested":true,"verb":"resize@v1","inputs":["/abs/in.png"],"args":{"width":800},"isolated":true,"grants":[{"path":"/abs/in.png","mode":"ro","source":"gesture"},{"path":"/abs/out.png","mode":"rw","source":"gesture"}],"approval":{"required":true,"route":"interactive","decision":"allow","request_id":"...","by":"uid1000:aurora","at":"<RFC3339>"},"ts":"<hlc>"}

and writes the same JSON to the output file's `user.aurora.provenance` xattr (a failure logs a warning; the event is still sent). `ts` is a hybrid logical clock from the bus's `hlc` package, local to controllerd. `domain` is the app's domain identity, the name the bus can attest, beside `app` (a manifest id). The event itself is still published by the controller, so its envelope `from` stays `uid1000:controllerd`: that is the truthful actor (the controller ran the verb; the app is not the publisher and never touches the bus for this). The app's identity belongs in the payload. `domain_attested` says busd had that domain registered when the verb ran. A failed invoke publishes nothing. `controller provenance <file>` reads the xattr back. Paths are canonical (symlinks resolved).

## Ambient, gesture and powerbox grants

Every filesystem grant (the input file `ro`, the output file `rw`) is classified before the domain is touched, and the class is recorded in the reply, the bus event and the xattr.

- **ambient**: covered by a `capabilities.paths` entry in the app's manifest. The user consented to it at install time (decision 0002). The grant must be the declared path or below it, and access is honoured: a `read` declaration covers only `ro` grants, `write` covers both.
- **gesture**: not covered. Legitimate only because the caller asked for it for this one invocation, which is the plan's powerbox model: picking a file grants that file and nothing more. The user's action is the authority, not the manifest.

- **powerbox**: not covered by the manifest, but the request carries a valid grant ticket minted by the powerbox picker (`shell/cmd/powerbox`) for exactly this path, mode, app and verb. A valid ticket outranks a manifest declaration for the grant it names.

Tickets (`controller/ticket`): HMAC-SHA256 over `{path, mode, app, verb, nonce, exp}`, single use, 60s by default. `ticket` authorises the input grant (`ro`), `output_ticket` the output grant (`rw`). A bad ticket is refused with its own code and never downgraded to a gesture; a request refused for any other reason does not spend its tickets. The key is a 0600 file, `$XDG_RUNTIME_DIR/aurora/powerbox.key` (`--powerbox-key`, `$AURORA_POWERBOX_KEY`), shared with the picker and created by whichever starts first; controllerd refuses a key file that is not 0600 and owned by it, and without a usable key refuses every ticket (`ticket_unavailable`).

**What this is not.** The ticket proves a process holding the key signed it, not that a human chose the file. Any same-user process that can read the key file can mint tickets, and the picker is an ordinary terminal process, not a compositor-drawn trusted path. `--require-powerbox` therefore raises the cost of a same-user bus client asking for an arbitrary path; it does not stop one that reads the key or drives the picker. The spent-nonce ledger is in memory and a restart forgets it. Full statement: decision 0007.

Enforced today:

- A verb with effect `read` takes no `output`, so it can only ever receive the `ro` input grant (pinned by a test).
- A verb's `capabilities = ["path:..."]` must name a declared capability. This is checked by the manifest validator at load; the app is skipped with the reason (`SKIPPED <path>: ... "path:~/X" is not among the app's requested capabilities`).
- `external` and `destructive` verbs that need any grant that is not `ambient` (gesture or powerbox) are **blocked until approved**; see "Approval".
- `controllerd --no-gesture-grants` refuses any invoke needing a gesture grant with `gesture_grant_refused`, before any domain call. Default off.
- `controllerd --require-powerbox` refuses any grant that is neither ambient nor powerbox with `powerbox_required`, before any domain call and before tickets are spent. Default off. `--no-gesture-grants` still refuses only gesture grants, so it lets powerbox grants through too; `--require-powerbox` is the stricter-in-intent setting.
- Approval is required for gesture and powerbox grants alike: a picked file is still a grant the manifest never declared, and the picker's ticket proves a key was held, not that the user meant a destructive act.

The coverage rule lives in `internal/invoke/grants.go` (`classify`); `contracts/manifest` does not export one. `~/` expands from `$HOME`, symlinks are resolved on both sides. Decision record: `contracts/decisions/0007-ambient-and-gesture-grants.md`.

Limitation: plain `aurora open <file>` passes whatever path was typed, so it is a gesture; with `--require-powerbox` on, use `aurora open --pick`. Without it (the default) the manifest still narrows nothing by itself. The trusted path needs the compositor.

## Approval

A verb whose effect class is `destructive` or `external` and whose invoke needs any grant that is not `ambient` does not run until someone allows it. Order inside `Invoke`: validation, grant planning and ticket checks, `--no-gesture-grants` / `--require-powerbox`, **approval**, then ticket redemption, the domain lock, the output file, grants and the run. A deny, timeout or cancel therefore leaves no output file, no domain call, no spent ticket and no provenance.

1. The controller publishes `approval.requested`: `{request_id, app, domain, domain_name, domain_attested, verb, effect, grants:[{path,mode,source}], args, timeout_seconds, expires_at}`. These are the values it will run, taken from the same variables, not re-read later.
2. It waits for a decision, `--approval-timeout` (default 60s). No decision means **deny**: `approval_timeout`, "no decision within 60s, so the answer is deny". It publishes `approval.decided` `{request_id, decision, by, reason}` either way.
3. A decision is a request to the `registry` service: `approval.decide@v1 {"request_id","decision":"allow"|"deny"}` (`controller decide <id> allow|deny`). **Who may decide:** busd stamps `from` from SO_PEERCRED, and the controller accepts only `uid<its own uid>:<process>`. An app's domain connects as `domain:<name>:<hex>` and gets `not_permitted`; so does any other uid. The request id is random, unknown and already-decided ids fail (`no_such_request`), and the first decision wins.
4. A denial returns `approval_denied` ("... was denied by uid1000:aurora").
5. Outcome: the reply and the provenance (event and xattr) carry `approval:{required, route, decision, request_id, by, at}`. Routes: `interactive` (a decision arrived on the session socket), `auto-approve`, and `ambient` (destructive/external but every grant declared in the manifest, `required:false`: consent happened at install). Write and read verbs carry no `approval` field.
6. With no `Approver` (an `Invoker` built without one) such verbs are refused with `approval_unavailable`. It fails closed.

`--auto-approve` is a **test and scripting affordance that defeats the point of approval.** It allows every such invoke immediately, logs `WARNING: AUTO-APPROVED` with the verb, grants and arguments each time, and records route `auto-approve`, `by:"controllerd"`.

What this guarantees: a confined app cannot approve itself or anyone else; a blocked verb has done nothing; the user is shown the real verb, grants and arguments; the answer to silence is deny; the record says how it was approved.

What it does not: anything of the same uid that is not in a domain connects as `uid<N>:<comm>` and can decide, or publish a fake `approval.requested`; the decision is not bound to who or what the user saw. The prompt is drawn by a launcher on `/dev/tty`, which any process that can write to the terminal can imitate. This is not the compositor-drawn trusted approval of plan Phase 10. See decision 0007. `domain_attested:false` on the first invoke of a domain just means busd had not registered it yet.

The fixture app's `delete@v1` (`testdata/apps/demo`, effect `destructive`; it writes a tombstone file, it deletes nothing) exists to test this. `demo.sh` ends with the block, deny, allow, timeout and ambient cases against a real busd, controllerd and bwrap domain, decided by `controller decide` (a script cannot drive `/dev/tty`).

## CLI

    controller [--socket S] apps
    controller verbs [--accepts MIME]
    controller describe <app> <verb>
    controller invoke <app> <verb> --input F [--output F] [--ticket T] [--output-ticket T] [--arg k=v ...]   # v is parsed as JSON, else a string
    controller decide <request_id> allow|deny
    controller provenance <file>

## Not implemented

- Capability handles: not wired. A handle minted for the input would be held by the controller, and `bus.transfer@v1` needs the domain to have a live connection at invoke time, which a one-shot app does not have yet. A recorded handle that nothing checks would be decoration, so provenance does not name one.

- The manifest's `capabilities.paths` are consulted only to classify grants (ambient or gesture). By default a gesture grant is given on request, so a verb declaring `~/Pictures` can still be asked to write anywhere the caller names; only `--no-gesture-grants` refuses that. The powerbox v0 tickets make a gesture distinguishable from a bare request but are not a trusted path (see above), and the approval behind destructive/external verbs is a terminal prompt, not a trusted path (see "Approval"). The `net`/`device` capabilities are never granted.
- No consent, no installation step, no signature or `image_hash` check, no hot reload of manifests: apps are read once at start.
- The domain has no Landlock/seccomp, no enforced memory/CPU limits and no btrfs snapshot per invoke (all adapter limits, see `adapter/linux/README.md`).
- Grants are persisted by the domain CLI: if controllerd is killed mid-invoke the grants stay until the next invoke of that app re-grants and revokes them. Nothing sweeps stale grants. Domains are never destroyed.
- Granting the input as a single file means a verb with several inputs is not supported; `inputs` in provenance is always one entry. Provenance does not chain to the input's own record, and an app that wants to write its output atomically (temp file, then rename) cannot: the output is a bind-mounted file and a rename over it fails inside the domain, so apps must write it in place (Lumen does, via `os.Create`). If the invoke fails, an output file the controller created is removed; one that already existed has already been truncated. A path swapped between the grant check and `domain run` (bwrap resolves again at start) is not closed, only the controller-to-grant gap is.
- The provenance event's envelope `from` is the controller's MVP `uid:comm` identity, so any same-user process can publish a fake `provenance.created`; the xattr is unauthenticated and can be edited or lost on copy. There is no event log of provenance beyond the bus log.
- Effect classes: read versus non-read decides whether an output is granted; `destructive` and `external` additionally require approval when a grant is not ambient.
- Output size, stdout size and CPU are not capped beyond the timeout; stdout and stderr are buffered whole in memory.
- Tests: the real-domain test skips itself when `bwrap` is absent or the domain CLI cannot be built; Lumen itself is not exercised by `go test` (the demo does that).
