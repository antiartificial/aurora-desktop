# Lumen

Toy image app for the Aurora desktop. Implements the verbs in `manifest.toml` with the Go standard library plus `golang.org/x/image` (Catmull-Rom scaling). It processes real PNG and JPEG files.

```
go build -o lumen ./cmd/lumen
go test ./...
./demo.sh        # standalone; no bus or controller
```

## Verbs

| Verb | Effect | Does |
| --- | --- | --- |
| `resize@v1` | write | Scale to `width` (aspect kept) or explicit `width` + `height`. `height` alone also works. Output format follows the output extension (`.png`, `.jpg`; default PNG). |
| `export@v1` | write | Re-encode to `format` (`png` or `jpeg`; falls back to the output extension), with `quality` 1-100 (default 90, JPEG only). The result carries an `export.finished` event object. |
| `histogram@v1` | read | 256-bin counts for red, green, blue and alpha (non-premultiplied 8-bit). Returned in the result; writes nothing. |

## Invocation

Subcommand form:

```
lumen resize    --input in.png --output out.png --width 800 [--height 600]
lumen export    --input in.png --output out.jpg --format jpeg --quality 85
lumen histogram --input in.png
```

JSON-on-stdin form (what the controller uses):

```
echo '{"input":"/in/a.png","output":"/out/a.jpg","args":{"format":"jpeg","quality":85}}' \
  | lumen --verb export@v1
```

Request: `{"input": path, "output": path, "args": {"width","height","format","quality"}}`. `output` is not needed for `histogram`.

Success, on stdout, exit 0:

```
{"ok":true,"output":"/out/small.png","width":300,"height":200,"format":"png","source_width":1200,"source_height":800}
```

Failure, on stdout, exit 1; the human-readable message also goes to stderr:

```
{"ok":false,"error":"unsupported format: avif (...)","detail":"supported formats: png, jpeg"}
```

`histogram` returns `red`, `green`, `blue`, `alpha` as 256-element int arrays, plus `width`, `height`, `pixels`.

## Domain constraints

Lumen runs in a domain granted only its input file (read-only) and its output directory (read-write). It touches nothing else: no config, no `$HOME`, no temp files. Output is written straight to the requested path (a partial file is removed on failure). `histogram` opens the input and nothing more.

## Manifest versus implementation

- **AVIF is not implemented.** The manifest lists `image/avif` in `types.creates`, and the `default_quality` setting mentions AVIF. There is no pure-Go AVIF encoder without system packages, so `export` to `avif` returns `unsupported format` and writes no file. The manifest overstates what exists.
- `histogram` output is declared `bytes` for red, green and blue. On the JSON wire these are 256-element integer count arrays (counts exceed one byte). An extra `alpha` array is included.
- `resize` and `export` take `input`/`output` file paths at the CLI level; the manifest's `output = { image = ... }` and `{ file = "path" }` are met by the `output` path in the result.
- The `export.finished` event is returned as an `event` field in the export result; the controller is expected to publish it. Lumen has no bus connection.
- Manifest `capabilities.devices = ["gpu"]` is unused; scaling is CPU-only.
- Settings, replicants and migrations are not implemented (`quality` defaults to 90 in code, not read from settings).
