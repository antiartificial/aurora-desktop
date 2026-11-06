# apps/

App manifests for aurora. Each subdirectory holds one `manifest.toml`, written against manifest schema v0 (`contracts/manifest/`, decision 0002).

| App | Kind | Verbs |
| --- | --- | --- |
| `lumen/` | toy native image app | `resize@v1`, `export@v1`, `histogram@v1` |
| `ffmpeg/` | wrapped CLI tool | `transcode@v1`, `thumbnail@v1` |
| `imagemagick/` | wrapped CLI tool | `convert@v1` |

These are manifests only. The toy app's code (Lumen) comes later, in Phase 5. The `image_hash` values are all-zero placeholders because no app images exist yet.

For wrapped tools the manifest is a declaration, not a guarantee. It is enforceable only because the tool runs in a domain granted exactly its declared inputs and outputs (0002).

Check them with:

```
cd contracts/manifest
go run ./cmd/manifest validate ../../apps/lumen/manifest.toml
```
