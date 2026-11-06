# contracts/plumb

The plumber: "I have this selected, what can the system do with it?" It lists. It never runs anything; running a verb stays behind `registry.invoke@v1`, its effect class, approval and grants.

- `rules.toml` is the rule set (data, embedded as the default; `controllerd --plumb-rules FILE` replaces it). Its header is the format reference.
- `registry.plumb@v1` serves it, next to `registry.verbs@v1`. `registry` is not a system namespace, so a domain may call it.
- `aurora plumb SELECTION [--path|--mime|--text] [--limit N] [--json]` is the CLI.

## Request and reply

```json
{"selection": {"kind": "path|mime|text", "value": "..."}, "limit": 20}
```

```json
{"selection": {"kind": "text", "value": "/home/a/x.png:12:3", "rule": "path-line-col",
               "object": "/home/a/x.png", "line": 12, "col": 3, "mime": "image/png"},
 "candidates": [
   {"app": "org.aurora.lumen", "verb": "histogram@v1", "summary": "...", "effect": "read", "default": true},
   {"app": "org.aurora.demo", "verb": "delete@v1", "summary": "...", "effect": "destructive", "default": false}],
 "truncated": false,
 "reason": "",
 "viewer": "session"}
```

- `candidates` is always an array. A selection that matches nothing is an empty array and a `reason`, not an error.
- `effect` is on every candidate, copied from the manifest: `read | write | destructive | external`, or `unknown` if a manifest somehow had none. It is the only authority signal. `destructive` and `external` verbs block for approval when invoked.
- `group` (optional) is presentation only. `default` is the app's manifest default for this type.
- `limit` defaults to 20 and is capped at 50; `truncated` says the cap cut the list.

## Ranking

1. verbs the manifest marks `default`, when they accept the type exactly or as `type/*` (a `*/*` default is not the default for this type);
2. the more specific `accepts` pattern: exact, `type/*`, `*/*`;
3. the lower effect class: read, write, destructive, external;
4. app id, then verb name. Verbs are deduplicated by app and name.

## Who sees what

The caller is the `From` busd stamped, never the payload (same classes as `index/access.go`).

- `session`: paths are checked and sniffed on disk.
- `domain`: the filesystem is not touched. A path's type comes from its name alone, so an existing file, a missing file and another domain's file get the same answer.
- anything else: an empty list.

The verbs listed are those `registry.verbs@v1` already shows every caller.
