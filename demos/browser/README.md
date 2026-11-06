# browser: Aurora's file browser (a BeOS app on Haiku's real Interface Kit)

A directory is one kind of view. The list is read from the filesystem (`readdir` + `lstat`), because indexd has
no filesystem scan: it only knows files whose `provenance.created` it saw. The index is used for **enrichment**:
select a file and the right pane runs `aurora query show FILE --json` and shows what made it, what it was
derived from, and what it was used in.

The pane keeps three answers apart:

| state | meaning | shown |
|---|---|---|
| could not be asked | no bus, no `aurora`, timeout | the launcher's own error line |
| asked, knows nothing | indexd has no write for this file | "The index knows nothing about this file. That is not a statement about the file..." plus `used_in` if the file is an input of something |
| knows | at least one indexed write | app + verb, inputs (derived-from), domain, write count, `used_in` |

Keys and mouse: click selects; double-click or Enter opens (a directory is entered; a file is handed to
`aurora open FILE`, run in a terminal because the launcher's verb picker is a tty prompt); Backspace or Alt+Up
goes to the parent.

Not built: no `--verb`/`--app` actions (a bare verb such as `resize@v1` is ambiguous across apps, so any verb
invocation must pass `--app` too; the browser never picks a verb), no queries as views (only the directory view),
no sort/column options, no rename/delete/copy, no live refresh when the directory changes, no icons, no menus.
Build: `cd kits && cmake -S . -B build -G Ninja && ninja -C build iface_browser`; run on a Wayland session with
`kits/build/interface/iface_browser [DIR]`. Checked by `demos/interface-kit/run.sh` (section BROWSER).
