# Manifest schema v0 and validator

Implements decision 0002 (`contracts/decisions/0002-app-manifest.md`): a Go package (`manifest`), and a `manifest` CLI. TOML on disk, parsed with BurntSushi/toml. Example manifests are in `apps/`.

```
go test ./...
go run ./cmd/manifest <command>
```

## Schema

Unknown keys are a parse error. The field names are this package's v0 choices: 0002 names the sections but not the fields (its open question 3).

| Section | TOML | Fields |
| --- | --- | --- |
| 1 Identity | `[app]` | `id`, `name`, `version`, `image_hash` |
| 2 Runtimes | `[[runtime]]` | `name`, `version` (exact pin) |
| 3 Capabilities | `[capabilities]`, `[[capabilities.paths]]` | `paths` (`path`, `access` = read or write), `network` (hosts), `devices` |
| 4 Types | `[types]` | `opens`, `creates` (MIME) |
| 5 Verbs | `[[verb]]` | `name`, `effect`, `capabilities`, `input`, `output`, `summary`, `accepts` |
| 6 Events | `[[event]]` | `name`, `payload` |
| 7 Settings | `[[setting]]` | `key`, `type`, `default`, `description` |
| 8 Replicants | `[[replicant]]` | `name`, `title` |
| 9 Migrations | `[[migration]]` | `from`, `to`, `description` |

Verb `capabilities` entries reference app-level requests as `path:<path>`, `net:<host>` or `device:<name>`. `devices` is enforced for two classes, `gpu` (the DRM render nodes) and `wayland` (the compositor socket), and only for a verb that lists `device:<class>`; declaring one grants nothing, and any other class fails the invoke (decision 0014). `input`, `output` and event `payload` map field names to a type: `string`, `int`, `float`, `bool`, `path`, `bytes`, or a MIME type or pattern, with an optional `?` suffix for optional.

**Additions beyond 0002's text** (needed to make the launcher query and the diff work): verb `accepts` (MIME patterns, `image/*` allowed; answers "which verbs accept this object?"), verb `capabilities` references, the `?` optional marker, `summary`/`description`/`title` strings, and event `payload`. Paths are treated as directories granted recursively.

## Validation rules

- `app.id` dotted lowercase (`org.example.app`); `app.version` semver; `app.image_hash` is `sha256:` plus 64 hex digits.
- Verb names carry a major version: `resize@v1` (0001's evolution rule). `resize`, `resize@v0` and `resize@1` fail.
- `effect` is one of `read`, `write`, `destructive`, `external`. Anything else is an error.
- A `destructive` or `external` verb must list at least one capability. Every capability a verb lists must also appear in the app's requested capabilities.
- Paths are absolute or `~/`-rooted, with no `.`, `..` or empty segments, and not `/` or `~/` alone. Access is `read` or `write`.
- Network entries are bare `host[:port]`, optionally `*.domain`, not URLs.
- MIME types are lowercase `type/subtype` (verb `accepts` also allows `type/*` and `*/*`).
- Duplicate verb, event, setting, replicant or runtime names are errors. `resize@v1` and `resize@v2` are different verbs.
- Setting defaults must match the declared type. Migrations need semver `from` and `to` that differ.

Errors name the field and the source line, for example `line 27: verb[0] (resize).name: "resize" is not a valid verb name; ...`. Line numbers come from a text scan, so they are best-effort. All errors are reported at once.

## Consent diff

`manifest diff old new` implements 0002's consent rule: an update that changes requested capabilities or effect classes requires fresh consent. 0002 does not say how a change is detected (its open question 5), so this package decides, and chooses **broadening only**:

Requires consent:
- a path not covered by an old path (same path or a parent, with at least the same access), or a path whose access goes read to write;
- a network host not covered by an old host (an old `*.example.com` covers subdomains but not the apex; an old host with no port covers any port; an old port must match);
- a newly requested device;
- a verb whose effect class widens (read, write, then destructive or external); changing between destructive and external counts, because neither is narrower;
- a new verb above `read` (a renamed major version is a new verb);
- a different `app.id`.

Does not require consent: version, image hash or other metadata changes; removing or narrowing a capability; narrowing an effect class; removing a verb; a new `read` verb.

Exit status: 0 no consent needed, 3 consent required, 1 invalid input, 2 usage.

## CLI

```
manifest validate <file.toml>
manifest show <file.toml>
manifest verbs <file.toml> --accepts image/png
manifest diff <old.toml> <new.toml>
```

`verbs` prints `name<TAB>effect`, one per line, in manifest order.

## Not implemented from 0002

- Compiling manifests to the bus IDL at install time.
- A published schema file in `contracts/`. The Go types and validator are the schema.
- Consent enforcement. `diff` computes the answer; the install flow (Phase 6) must call it.
- Field-level shape for settings, replicants and migrations beyond the minimal fields above. Replicants v0 is Phase 8.
- Checking what the `image_hash` covers, or the hash itself (open question 6). The hash is only format-checked.
- Signatures, registry loading and verb registration on the bus.
- Verifying effect classes. For a wrapped CLI tool the manifest is only a declaration; enforcement is the Phase 5 domain grants.
- Whether a verb's capabilities match its effect (a `write` verb is not required to reference a write path), and whether `accepts` is a subset of `types.opens`.
- Pinned runtimes are only format-checked; nothing resolves them.
- Per-verb capability changes (a verb newly using an already-granted capability) are not a consent event.
- Launcher argument collection, recipes, and Flatpak mapping (open questions 1, 2, 7).
