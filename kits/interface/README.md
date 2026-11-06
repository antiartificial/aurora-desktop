# interface: Haiku's real Interface Kit on the Aurora compositor

Run 2026-11-06. Everything below was run; what was not run is in "Not built, not verified".

**Result in one paragraph.** Haiku's own `View.cpp`, `Window.cpp`, `Font.cpp`, `Button.cpp`, `StringView.cpp`, `Control.cpp`, `HaikuControlLook.cpp` and 60 more Interface Kit files (Vitruvian fork, compiled in place, unmodified, nothing copied) run against a small `app_server` analogue that speaks Haiku's real link protocol over our ports, rasterises with FreeType + HarfBuzz and a small software canvas, and presents through `wl_shm` + `xdg_shell` to the Aurora compositor. A `BApplication`/`BWindow`/`BView`/`BFont`/`BButton` program appears as a real window; its pixels are verified in a decoded capture from the headless compositor. `BFont` answers from the font file. Scale is carried end to end.

Contents: [1 Investigation](#1-investigation) · [2 Decision](#2-decision-and-why) · [3 What was built](#3-what-was-built) · [4 Text and the seam](#4-text-the-narrow-interface-and-convergence-with-the-compositor) · [5 HiDPI](#5-hidpi-scale-is-carried-from-the-first-line) · [6 Run it](#6-run-it) · [7 Not built, not verified](#7-not-built-not-verified) · [10 The kitchen sink](#10-the-kitchen-sink-every-control-in-one-application)

## 1. Investigation

### 1.1 How the Interface Kit reaches app_server

Facts from the Vitruvian source (`/home/arti/projects/Vitruvian/src/kits/{app,interface}`, `src/servers/app`):

1. **Connect.** `BApplication::_ConnectToServer()` (`app/Application.cpp:1402`) calls `create_desktop_connection()` (`app/AppMisc.cpp`: finds app_server's main port, sends `AS_GET_DESKTOP`), then sends `AS_CREATE_APP` (receiver port, looper port, team, token, signature). app_server answers `B_OK` + the port of a per-application `ServerApp` thread + a read-only shared area (system colours) + its team. From then on the app talks to its `ServerApp`.
2. **Window.** `BWindow::_InitData()` (`interface/Window.cpp:2751`, the send is at `:2859`) makes a reply port `w<app_server`, sends `AS_CREATE_WINDOW` (frame, look, feel, flags, workspace, **its looper token, its looper port**, title) and reads back the port of a per-window `ServerWindow` thread plus the frame/limits that were enforced. `fLink` (a `BPrivate::PortLink`) now points at that window thread.
3. **Views.** `BView::_CreateSelf()` (`View.cpp:6328`) sends `AS_VIEW_CREATE_ROOT`/`AS_VIEW_CREATE` (token, name, frame, scroll offset, resize mode, flags, view colour, parent token) and the whole drawing state (`AS_VIEW_SET_STATE`, `AS_VIEW_SET_FONT_STATE`). A view is mirrored in the server by a token.
4. **Drawing is buffered commands, not pixels.** Every `BView` drawing call is `fOwner->fLink->StartMessage(AS_...)` + `Attach<>()`, flushed at `Flush()`/`Sync()`/end of update. `View.cpp` has 406 lines that reference the link, touching 121 distinct opcodes; `Window.cpp` 29 opcodes; `Font.cpp` 27; `InterfaceDefs.cpp` 36; `Bitmap.cpp` 4. `ServerProtocol.h` declares 282 `AS_*` names in all. Nine files account for essentially all link traffic (8+ references each): `View, Window, InterfaceDefs, Font, PrivateScreen, WindowStack, Bitmap, Picture, Dragger`; `Menu`, `Button`, `GraphicsDefs` have a handful. Everything else (every widget, every layout class) sits on the `BView` API.
5. **The server pushes `BMessage`s back** to the window's looper port (not over the link): `_UPDATE_` when something needs painting, `B_WINDOW_RESIZED`, `B_QUIT_REQUESTED`, mouse events (`screen_where`, `buttons`, `clicks`, `_view_token`; the *client* then derives `where`/`be:view_where`).
6. **The update handshake.** `BWindow::DispatchMessage(_UPDATE_)` (`Window.cpp:1183`): send `AS_BEGIN_UPDATE`, read back window origin, size, and a list of `(view token, update rect in screen coordinates)`; call `view->_Draw()` for each (which emits the drawing commands); send `AS_END_UPDATE`. **The server paints each view's background (view colour) itself before the client draws.**
7. **Text is entirely server-side.** `BFont` is a bag of ids (`fFamilyID`, `fStyleID`) and a size. Every metric is a query on the *application* link: `AS_GET_FONT_HEIGHT`, `AS_GET_STRING_WIDTHS`, `AS_GET_ESCAPEMENTS(_AS_FLOATS)`, `AS_GET_FAMILY_AND_STYLE(_IDS)`, `AS_GET_SYSTEM_FONTS` (at `BApplication` start, `_init_global_fonts_()`). `DrawString` is `AS_DRAW_STRING` with the UTF-8 bytes and a baseline origin; app_server shapes and rasterises it. The client holds no font file and no glyph.

### 1.2 The minimal opcode set

Observed (server trace) running the three programs below. Application link: `AS_CREATE_APP`, `AS_CREATE_WINDOW`, `AS_GET_SYSTEM_FONTS`, `AS_GET_FAMILY_AND_STYLE`, `AS_GET_FAMILY_AND_STYLE_IDS`, `AS_GET_FONT_HEIGHT`, `AS_GET_STRING_WIDTHS`, `AS_GET_ESCAPEMENTS_AS_FLOATS`. Window link, 38 opcodes:

```
window   AS_SHOW_OR_HIDE_WINDOW AS_ENABLE_UPDATES AS_BEGIN_UPDATE AS_END_UPDATE AS_SYNC AS_DELETE_WINDOW
views    AS_VIEW_CREATE_ROOT AS_VIEW_CREATE AS_SET_CURRENT_VIEW AS_VIEW_INVALIDATE_RECT AS_VIEW_CLIP_TO_RECT
state    AS_VIEW_SET_STATE AS_VIEW_GET_STATE AS_VIEW_SET_FONT_STATE AS_VIEW_PUSH_STATE AS_VIEW_POP_STATE
         AS_VIEW_GET_ORIGIN AS_VIEW_GET_SCALE AS_VIEW_GET_TRANSFORM AS_VIEW_GET_PARENT_COMPOSITE
         AS_VIEW_SET_/GET_{HIGH,LOW}_COLOR AS_VIEW_SET_{HIGH,LOW,VIEW}_UI_COLOR AS_VIEW_GET_{LOW,VIEW}_UI_COLOR
         AS_VIEW_SET_VIEW_COLOR AS_VIEW_SET_DRAWING_MODE AS_VIEW_GET_DRAWING_MODE AS_VIEW_SET_MOUSE_EVENT_MASK
draw     AS_STROKE_LINE AS_STROKE_LINEARRAY AS_STROKE_RECT AS_FILL_RECT AS_FILL_RECT_GRADIENT AS_DRAW_STRING
```

Implemented beyond what these programs hit (reasonable neighbours): resize/move, view move/resize/hide/show/delete, round rects, ellipses (fill), triangles (fill), linear-gradient round rects, pen/origin/scale, font name/default queries, escapement-as-points. About 100 `case` labels in `server.cpp`. The other ~200 (bitmaps, pictures, cursors, decorators, workspaces, screens, regions as clip, shapes, arcs, beziers, layers, direct windows, ...) answer `B_ERROR` if they want a reply and are counted (`aurora::unhandled_opcodes()`); the tests assert the count is 0 for what they use.

### 1.3 The 104 `.cpp` files of `interface/`: what is reusable

Compile survey with the exact flags of the build (non-`__VOS__`, `-w`): **103 of 104 compile unmodified.** The one failure, `ColumnTypes.cpp`, is a missing `<time.h>` in a header (`field 'fTime' has incomplete type 'tm'`), not a design problem. **Linked into a running binary: 62 of them plus `layouter/*` and `textview_support/*`** (list in `CMakeLists.txt`); the linker pulls only what the closure needs.

| Class | Files | What they assume |
|---|---|---|
| **No server contact. Reusable as is under any backend.** | `Rect Point Size Polygon Region RegionSupport Alignment AffineTransform Gradient* GraphicsDefs ColorConversion Layout* *Layout* TwoDimensionalLayout layouter/* LayoutUtils ListItem StringItem ...` | pure computation; `GraphicsDefs`/`InterfaceDefs` have a few `AppServerLink` queries |
| **Widgets on the BView API.** Reusable as is iff `BView`/`BWindow` work. | `Button StringView Control CheckBox RadioButton Box TabView Slider StatusBar TextControl ListView OutlineListView ScrollBar ScrollView Menu* TextView ...` | `BView` virtuals, `be_control_look`, `ui_color()`, `be_plain_font`, a `BLooper`-driven `BWindow`. `TextView` and `Menu` also need the clipboard/roster/`BBitmap` and were not exercised |
| **The seam: they emit link messages.** | `View.cpp` (7209 lines) `Window.cpp` (4144) `Font.cpp` (1559) `InterfaceDefs.cpp` `PrivateScreen.cpp` `Bitmap.cpp` `Picture.cpp` `WindowStack.cpp` | the `AS_*` protocol and the replies described above. They are the 13k lines you must either keep (a) or rewrite (b) |
| Not touched | `Deskbar Shelf Dragger Alert AboutWindow PrintJob Screen ...` | registrar, tracker, print server, screens. Compile (most) but mean nothing here |

### 1.4 The two shapes, with specifics

**(a) Keep `View/Window/Font.cpp`; provide a server for the link protocol.** What is reused: all of the table above, including the 13k-line seam files. What it needs: (i) a `BApplication` that does not need the registrar (Haiku's `Application.cpp` pulls `BRoster`, `BFile`, `BAppFileInfo`, `BMimeType`: the Storage Kit wall `haiku-compat/README.md` documents); (ii) `PortLink`/`LinkSender`/`LinkReceiver`/`AppServerLink` (real Haiku files, compile with a 12-line `syscalls.h` shim); (iii) a server: the opcodes of 1.2, a view tree, an update loop, a rasteriser, a font engine. Haiku's real app_server is 49k lines in `src/servers/app` + AGG painter + FreeType engine; we do not take it (see 2.3) and wrote about 1.2k lines for the subset.

**(b) Back `BView`/`BWindow` directly onto Wayland.** What breaks: `View.h` fixes `BView`'s data layout and ~150 virtual/non-virtual members; the widgets above compile against that header. Replacing `View.cpp` means re-implementing the whole `BView` (state stack, coordinate conversion, invalidation, clipping, transactions, scrolling, the "current view" protocol with its server round trips), plus `BWindow` (looper, shortcuts, focus, update loop, `Lock`/`Quit` semantics) and `BFont`: the 13k lines in the table. What survives is the widget layer and the layouts, which already sit on the `BView` API. You still write a software rasteriser and a font engine, because Wayland gives you a buffer, not a drawing API. So (b) saves the *server protocol* and costs the *client library*, and the thing it re-implements is the part of Haiku most tuned to BeOS behaviour.

## 2. Decision and why

### 2.1 Choice: (a), with the server embedded in the application

The tension is real but it is about *where the link ends*, not about whether the link exists. Wayland requires each client to render into its own buffer; the BeOS API requires only that `BView` calls reach something that executes them. Nothing says that something is another process. So the server is a **thread group inside the application** (`appserver/server.cpp`): the unmodified kit writes `AS_*` messages into real ports; the server threads read them, rasterise into a per-window canvas, and the presenter hands the finished frame to the compositor as a `wl_shm` buffer. In Wayland terms that is client-side rendering; in Haiku terms it is app_server.

Reasons, in order of weight:

1. **Reuse is measured, not guessed.** (a) costs ~2.4k fresh lines (`wc -l`: server 1221, canvas 205, fonts 273, present 242, applite 369, stubs 51); 103 of 104 Interface Kit files compile unmodified and 62 are linked into the demos; the first full link (before `applite.cpp`) had 34 undefined symbols, all of them Roster/Storage Kit/`BApplication` or small `OS.h` gaps. (b) would write ~13k lines to reproduce `View/Window/Font`.
2. **The seam is Haiku's own** (`ServerProtocol.h`, with wire formats you can read in `src/servers/app/ServerWindow.cpp`). Future Haiku code (more widgets, `BBitmap`, `BPicture`) keeps working as the server grows, with no client-side drift.
3. **The user's priority, fonts, lands in the right place.** `BFont` is already a query interface; the server is where HiDPI, hinting and shaping decisions belong (section 4 and 5).
4. **Reversible.** Because the link is real ports, the server can later move out of process (one shared app_server for all BeOS apps: window order, clipboard, drag and drop) without touching a line of kit code. What blocks that today is listed in section 7.

### 2.2 What this gives up

Server-side drawing's advantages (one rasteriser shared by all apps, cross-window composition, `BDirectWindow`) are not available; each app has its own glyph cache and canvas. That is exactly the Wayland trade, and it is acceptable here.

### 2.3 Why not compile Haiku's real app_server

It is possible in principle (it has a `HWInterface` abstraction; `BitmapHWInterface` is a precedent for a non-hardware backend, and it already contains the AGG painter and the FreeType font engine): that is "(a2)", the most faithful option, and it keeps exact BeOS rendering. It was not attempted: 49k lines of server plus Storage Kit, `BMessage` registrar and input-server dependencies would have meant a much larger first slice, and the user asked for something to iterate on. The protocol seam here is the same, so (a2) can replace `appserver/` later without client changes. Measure the unhandled-opcode counter against Haiku's list when deciding.

## 3. What was built

```
 your BeOS code     BApplication / BWindow / BView / BFont / BButton ...
 Haiku, real        View.cpp Window.cpp Font.cpp Button.cpp ... (compiled in place from $HAIKU_SRC, unmodified)
                    PortLink / LinkSender / LinkReceiver / BLooper / BMessage (real)
        |  AS_* over our ports (kits/src/port.c)           the seam: Haiku's own protocol
 appserver/server.cpp   ServerApp + ServerWindow threads, view tree, update protocol, font queries, drawing dispatch
 appserver/canvas.cpp   software canvas: fills, lines, round rects, linear gradients, glyph blits, scale-aware
 appserver/fonts.cpp    text::Backend over FreeType + HarfBuzz            <- the text seam (text_backend.h)
 appserver/present_wl.c wl_shm + xdg_toplevel, pointer, output scale     <- the presenter seam (present.h)
        |  wl_surface.attach / set_buffer_scale / commit
 Aurora compositor (xdg_shell)
```

Files (all under `kits/interface/`, written fresh; Haiku source is referenced by path, nothing copied):

- `CMakeLists.txt`: builds `haiku_iface_real` (62 Interface Kit files + `layouter/*` + `textview_support/*` + `app/{AppDefs,AppServerLink,Cursor,Invoker,LinkReceiver,LinkSender,MessageRunner,PortLink,ServerLink,ServerMemoryAllocator}` + `support/{PointerList,Referenceable}` + `locale/UnicodeChar`) on top of `haiku-compat`'s `haiku_app_real`; `aurora_appserver`; `aurora_wl_present` (only if `libwayland-client`, `wayland-scanner` and an `xdg-shell.xml` are found; the XML is read from the system, not vendored). Wired in by two lines in `kits/CMakeLists.txt`. Needs `freetype2`, `harfbuzz`, `icu-uc` (for `BUnicodeChar`).
- `applite.cpp`: `BApplication` against Haiku's real `Application.h`, without the registrar. Sets `be_app`, connects with `AS_CREATE_APP`, loads the global fonts, creates `HaikuControlLook`, runs the looper. It skips the rest of `_init_interface_kit_()` (palette, clipboard, menu bitmaps). `be_app->GetAppInfo`, `AppResources`, `RegisterLooper`, `SetPulseRate` are hollow.
- `stubs_iface.cpp`: tagged stubs. `BRoster::Launch/GetAppList/Private::SendTo` hollow (so `BMessageRunner` and a few `Window.cpp` shortcuts fail with `B_ERROR`); `send_data/receive_data` hollow (menu tracking); `rename_thread` no-op; `snooze_until` real. The ten Storage Kit symbols that `BArchivable::instantiate_object` names (`BEntry`, `BFile`, `BAppFileInfo`) are defined as jumps to an abort, because writing those classes means ~50 virtuals; they are reachable only after `load_add_on()` succeeds, and it never does.
- `shim/syscalls.h`: replaces Haiku's private `syscalls.h`, which needs the kernel info-struct zoo. Also three declarations added to `../haiku-compat/shim/OS.h` (`snooze_until`, `rename_thread`, `send_data`, `receive_data`, `has_data`, `B_CLONEABLE_AREA`, `B_SYSTEM_TIMEBASE`).
- `appserver/server.cpp` (+ `server.h`), `canvas.{h,cpp}`, `fonts.cpp`, `text_backend.h`, `present.h`, `present_wl.c`.
- `demo/hello.cpp` (rectangles, a line, text in three fonts), `demo/controls.cpp` (`BStringView` + `BButton`), `tests/test_font.cpp`, `tests/test_window.cpp`, `tests/test_controls.cpp`, and `../../demos/interface-kit/{run.sh,imgcheck.py}`.

### Behaviour worth knowing

- **Coordinates.** Window content origin is (0,0); `BWindow` frame is the content frame (no decorator, no tab: `B_TITLED_WINDOW` only sets the `xdg_toplevel` title). The compositor placed the 320x200 window at (0,0) of the 1280x800 headless output. BeOS rect semantics are kept (`BRect(0,0,9,9)` covers 10 pixels; a 1px line is centred on pixel centres).
- **Update model.** Server-side dirty region, one `_UPDATE_` message, background fill with the view colour per view, then the client draws; at `AS_END_UPDATE` the frame is copied to the presenter and the last frame is kept for tests (`aurora::last_frame`). Resize from `xdg_toplevel.configure` applies Haiku's follow modes (`_VIEW_*`) like `View::ParentResized` and sends `B_WINDOW_RESIZED`. **Resize was not exercised against the compositor.**
- **Input.** `wl_pointer` motion/button are turned into `B_MOUSE_MOVED/DOWN/UP` with `screen_where` and `_view_token` as app_server's EventDispatcher sends them; the real `BWindow` then derives `where`. The `BButton` click test injects at the server's internal pointer entry (the same event the Wayland callbacks post). **No pointer event went through the compositor**: the compositor has `--test-input` for keys only.

## 4. Text: the narrow interface, and convergence with the compositor

`BFont` is not a stub. Every number comes from `aurora::text::Backend` (`appserver/text_backend.h`), implemented by `fonts.cpp` over FreeType 26.6 and HarfBuzz 14.4:

| BFont call | Protocol | Source of the number |
|---|---|---|
| `GetHeight` | `AS_GET_FONT_HEIGHT` | the face's `hhea` ascender/descender/lineGap x size / unitsPerEm (`FT_Face`) |
| `StringWidth`, `GetStringWidths` | `AS_GET_STRING_WIDTHS` | sum of HarfBuzz-shaped advances in font units x size / unitsPerEm (so GPOS kerning counts) |
| `GetEscapements` | `AS_GET_ESCAPEMENTS(_AS_FLOATS)` | advance / size per UTF-8 character |
| `DrawString` | `AS_DRAW_STRING` | shaping, then per-glyph FreeType render (unhinted, 1/4-pixel horizontal subpixel positions, 8-bit coverage), blended by the canvas |
| family/style ids | `AS_GET_FAMILY_AND_STYLE(_IDS)`, `AS_GET_SYSTEM_FONTS` | a scan of `$AURORA_FONT_DIR` (default `/usr/share/fonts/truetype`) |

On this machine `be_plain_font` is **`/usr/share/fonts/truetype/NotoSans-Regular.ttf`** (family "Noto Sans", style "Regular", 12): ascent 12.8280, descent 3.5160, leading 0, `StringWidth("0000")` 27.4560. Bold is `NotoSans-Bold.ttf`, fixed is `DejaVuSansMono.ttf`. Override with `AURORA_FONT_PLAIN/BOLD/FIXED` (file paths).

**The seam is `text::Backend`**: `FamilyCount/FamilyName/StyleCount/StyleName/Lookup/SystemFont`, `GetMetrics(key, size)`, `Shape(key, size, utf8)` returning glyph ids + advances + clusters, `Rasterize(key, size, scale, glyph, subpixelX)` returning an A8 bitmap. All inputs and outputs are plain data. Metrics and shaping are in **layout units** and scale-independent; only `Rasterize` takes a scale and returns device pixels.

**Convergence with the compositor's cosmic-text stack** (not attempted; different process and language, and that code is being written now). Three ways, in increasing ambition, none needing a change to `Font.cpp`, the protocol, or the widgets:

1. **Same library, two callers.** Expose cosmic-text (shaping, metrics, glyph raster) as a C ABI staticlib with exactly the six calls above, and link it into this process in place of `fonts.cpp`. Both processes then agree on advances and glyph shapes, and fallback fonts/emoji come for free. The C++ `Backend` becomes a thin wrapper. `fonts.cpp` stays as the reference and the oracle for a conformance test (`test_font` already compares against raw FreeType).
2. **Shaped runs on the wire.** The server stops rasterising text: `AS_DRAW_STRING` becomes `{font key, size, glyph ids, positions in layout units, colour}` sent to the compositor (a new Wayland protocol or the trusted socket). The compositor, which already owns HiDPI and its glyph atlas, rasterises. This is app_server's original model for text and removes the per-app glyph cache. Only `Shape` and a "draw run" call cross the process boundary.
3. **Shared catalogue.** One font service owns family/style ids so ids are stable across processes (BeOS needs `BFont` ids to be meaningful to the server only; cross-process archiving of a font needs names).

Whatever is chosen, keep `Metrics`/`Shape` as the contract for layout and make `Rasterize` (or "draw run") the only place scale enters.

## 5. HiDPI: scale is carried from the first line

- **Layout units** are what every BeOS API speaks. The server owns one `scale` (device pixels per layout unit). Canvas size is `ceil(layout size x scale)`; every drawing call multiplies at the last moment (`Canvas::ToDevice`), line widths scale, rects round to device pixels, glyphs are rasterised at `size x scale` with 1/4-device-pixel positions.
- **Source of the scale:** `wl_output.scale` from the compositor (integer, currently 1), overridden by `AURORA_UI_SCALE` (any positive number). The compositor has no `wp_fractional_scale`/`wp_viewporter`, so with a fractional request the server renders at `ceil(scale)` and says so on stderr (a Wayland buffer scale must be an integer). The 1.45 host factor therefore renders at 2 through the compositor, but is exercised as a true 1.45 in the headless tests (no presenter involved).
- **Metrics do not depend on scale.** Unhinted, linear advances: `test_font` is run at scale 1, 1.45 and 2 and the processes print identical `ascent/descent/StringWidth` (`run.sh` compares the lines byte for byte).
- **Verified through the compositor:** a window rendered at scale 2 (640x400 buffer, `set_buffer_scale(2)`) occupies the same 320x200 logical rectangle in the capture as at scale 1 (view-colour pixels span x=1..318, y=1..198 in both). So the compositor honours `buffer_scale`.
- **Decision recorded:** text is **unhinted** at every scale so that layout is identical across scales. The cost is softer stems at 1x; light auto-hinting or snapping stems on integer scales is a later, backend-local change.

## 6. Run it

```
demos/interface-kit/run.sh          # builds, runs 45 checks, starts the compositor headless, captures, decodes
cd kits && cmake -S . -B build -G Ninja && ninja -C build && (cd build && ctest)    # 16 tests (ctest, run 2026-11-06)
demos/kitchen-sink/run.sh           # one application with every working control; 76 checks on decoded pixels (section 10)
kits/build/interface/iface_hello         # on a real Wayland session: the window of the demo
kits/build/interface/iface_controls      # BStringView + BButton; click the button
```

Environment: `WAYLAND_DISPLAY` (presenter), `AURORA_UI_SCALE`, `AURORA_APPSERVER_PRESENT=0` (no compositor; frames stay in memory), `AURORA_APPSERVER_DUMP=<dir>` (writes `window-N.ppm` at every finished update), `AURORA_APPSERVER_TRACE=1` (every opcode, and every unhandled one), `AURORA_FONT_*`.

What `run.sh` asserts (decoded pixels): the capture is 1280x800 with 3.5k distinct colours; the frame the app finished appears **verbatim** in the capture (64000/64000 pixels; found by pixel search, at (0,0)); the 1px frame is on all four corners; the red block fills `(20,20)-(99,59)` to its corners and not a pixel past; the three label strings put glyph pixels where `BFont` said (ink starts at the pen, ends within 1.5 px of pen + `StringWidth`, rows within baseline - ascent .. + descent, 6+ distinct ink rows, solid dark pixels); 24pt ink is 20 rows vs 12 for 12pt; scale 2 geometry. `imgcheck.py` decodes the PNG itself (stdlib `zlib`), and was checked to fail on a capture whose window area was painted over.

## 7. Not built, not verified

Rewritten 2026-11-06 from what the kitchen sink (section 10), `test_widgets`, `demos/interface-kit/run.sh` and `ctest` actually run. The first version of this section said menus, `BTextView`, scroll bars, layouts and the keyboard were never run. All of those run now, are covered by the tests above, and are described in sections 8, 9 and 10.

**Not built:**
- **Cross-process server.** `_kern_transfer_area` is the haiku-compat stub (returns `B_NOT_SUPPORTED`), so a link message above one port buffer (large regions, long strings) is expected to fail (not tested). The colour area is not shared: `ui_color()` uses Haiku's compiled-in defaults, there is no server-side colour table and no `B_COLORS_UPDATED`.
- **Bitmaps are in-process only** (`AS_CREATE_BITMAP` areas, 32/24/8-bit and CMAP8, nearest-neighbour scaling). Not built: `BPicture` playback to the server, `BDirectWindow`, drag and drop, a shared clipboard (it is per process). Cursors are acknowledged and ignored (`AS_SET_VIEW_CURSOR` answers `B_OK`; nothing here passes a `BCursor` on to the compositor). `BScreen` answers for one 32-bit screen the size of the output.
- **No decorator, and the window position is the compositor's.** `AS_GET_DECORATOR_SETTINGS` answers "no tab, no border". `BWindow::MoveTo` is not honoured by the compositor: the `BAlert` of the kitchen sink centres itself, and appears at (0,0) on top of the main window. `B_WINDOW_MOVED` is never sent.
- **Stock icons.** `BIconUtils::GetSystemIcon` is a stub that returns `B_ERROR` (the real one reads HVIF from Storage Kit resources and renders through libicon/agg), so a `BAlert` has no icon; the kitchen sink asks for `B_EMPTY_ALERT`.
- **Drawing gaps that remain** (none of them blocks a control in the kitchen sink, judged on the captures; the references in the demos come from the same server, so they would not show a wrong-but-consistent rendering): stroked arcs and beziers (shapes are flattened polylines), polygons other than filled triangles and shapes, non-linear gradients (`AS_FILL_*_GRADIENT` for radial/conic/diamond count as unhandled and draw nothing), patterns other than solid high/low, drawing modes other than copy/over/invert (all the rest draw as source-over with the high colour), user clip *regions* (only the bounding rect is honoured), a non-identity `SetTransform` (counted as unhandled; only `BTab`'s identity transform is accepted), view scale/origin beyond `(p+origin)*scale`, `B_DRAW_ON_CHILDREN`. Text: `escapement_delta` only for space/non-space, no rotation/shear, no fallback fonts (missing glyphs draw `.notdef`), no emoji; `AS_GET_EDGES`, `AS_GET_BOUNDINGBOXES_*`, `AS_GET_FONT_BOUNDING_BOX`, `AS_GET_HAS_GLYPHS`, `AS_GET_UNICODE_BLOCKS` are unimplemented (each answers `B_ERROR`).
- **Rough edges seen in the captures:** the Command-key symbol next to a menu shortcut draws as a small box; a `BMenu` pop-up is an `xdg_popup` and the compositor's xdg_shell handler says `TODO popup grabs` (read in `compositor/src/handlers/xdg_shell.rs`, not exercised).
- **Fractional scale** (no protocol in the compositor).
- **Registrar, Storage Kit:** `BEntry/BFile/BNode/BAppFileInfo` hollow, `BRoster` hollow; `BApplication` is single-process with no launch/quit coordination.
- **The `_init_interface_kit_()` remainder:** `PaletteConverter::InitializeDefault`, `MenuPrivate::CreateBitmaps` (`get_menu_info` is served by the server now): skipped, so 8-bit palette conversions are not available.
- **Compiled but never run:** `BDecimalSpinner` (it is in `IFACE_REAL` because `BSpinner` shares `BAbstractSpinner`), `BOptionControl`'s other subclasses, `SplitLayoutBuilder` (not compiled). Not compiled at all: `BChannelControl`/`BChannelSlider`, `BColorItem`/`BColumnListView`.

**Not verified:**
- **Resize and close through the compositor.** The code paths exist (`xdg_toplevel.configure` -> `B_WINDOW_RESIZED`; close -> `B_QUIT_REQUESTED`); nothing made the compositor send either. Wheel scrolling (`B_MOUSE_WHEEL_CHANGED`): wired, but `--test-input` has no wheel command.
- **Keyboard navigation inside an open menu**, submenus opened through the compositor (the Format submenu of `demo/menu.cpp` was only exercised with the server's internal pointer), dismissing a menu by clicking outside.
- **The winit (non-headless) compositor backend**, a real GPU session, and any compositor other than `aurora-compositor --headless`.
- **Interop with stock Haiku.** The source is the Vitruvian fork (see `haiku-compat/README.md`); stock `src/kits` was not tried.
- **Thread safety beyond what ran:** 160 consecutive headless test runs without failure in the earlier session (`test_font`, `test_iface_window` at 2 scales, `test_controls`, 40 times each), 3 consecutive passes of `demos/kitchen-sink/run.sh` (75 checks each, before the theme work of section 11), and `ctest` once (16 of 16). There is one global server mutex; the Wayland presenter has its own.
- **Memory:** frames are copied at every update (`last` for tests, a fresh `memfd` per present); glyph cache is unbounded. Fine for a demo; not a design.
- **Hinted rendering at 1x** (not offered): at scale 1 the text is anti-aliased and unhinted, so it is softer than hinted Haiku text. A judgement call, recorded in 5.
- **One tolerance in the kitchen-sink demo:** on the Extras page the capture is compared with a reference render pixel for pixel, except that one pixel column (the blinking caret of the colour control's focused field, B_PULSE at 500 ms) may be in the other phase. Everything else is exact.

## Next steps, if the direction is confirmed

1. Resize + close + keyboard through the compositor, with a compositor-side injection hook for pointer events, so the demo covers input end to end.
2. Share the colour area and `ServerMemoryAllocator` so `BBitmap`/`DrawBitmap` work; then move the server out of process (one app_server, many apps) behind the same ports.
3. Decide the text convergence (section 4) with the compositor team before more text features land in either place.
4. Remaining opcode families by demand: bitmaps, strokes of curves, regions as clip, patterns, `BPicture`.

## 8. Addendum: keyboard, scrolling, lists, a browser (same day, later)

What changed, and what the section 7 list above no longer says:

- **Keyboard.** `present_wl.c` binds `wl_keyboard`, compiles the compositor's own xkb keymap (a new `keymap` event replaces it, so hot reload works), tracks modifiers, and does client-side key repeat from `repeat_info`. `server.cpp` turns each key into the message app_server sends: `B_KEY_DOWN/UP` (`key` = BeOS key code from the evdev position, `modifiers`, `raw_char`, `byte`, `bytes`, `states`, `be:key_repeat`), `B_UNMAPPED_KEY_*` for keys that type nothing, `B_MODIFIERS_CHANGED`, `B_WINDOW_ACTIVATED` on keyboard enter/leave. Alt is Command and the logo key is Option (Haiku's default keymap). `input_shim.cpp` replaces `Input.cpp` (`_control_input_server_` only) so `modifiers()` and `get_click_speed()` answer.
- **Pointer** now survives the compositor: the listener table had NULL for `wl_pointer.frame`, which libwayland turns into an abort on the first real pointer event. Wheel (`B_MOUSE_WHEEL_CHANGED`) is wired but nothing could drive it (no wheel command in `--test-input`). The server counts clicks (`clicks` = 2 within 500 ms and 4 px).
- **Scrolling and shapes.** New opcodes: `AS_VIEW_SCROLL`, `AS_GET_SCROLLBAR_INFO`, `AS_STROKE_SHAPE`/`AS_FILL_SHAPE` (polylines; beziers flattened to 12 segments), needed by `BScrollBar` and its arrows. `BListView` needed none.
- **A bug fixed on the way.** `fonts.cpp` shaped through `hb_ft_font`, which reads advances from the FT face at whatever size `Rasterize` last set: a string's width depended on what had been drawn before it (the same 12-character string measured 177 or 264 px). It now shapes from the font file with HarfBuzz's own OpenType functions.
- ScrollView, ListView, ListItem, StringItem, OutlineListView are compiled in; `BTextView`, menus and layouts are still not run. `demos/browser/` is the first real application.

## 9. Addendum: BTextView, menus, layouts (later still)

Run 2026-11-06. `demos/interface-kit/run.sh` now has TEXT, MENUS and LAYOUTS sections (all decoded pixels, references from separate processes).

**BTextView runs.** Typing, Backspace, caret, blinking, selection, Shift+arrows, typing over a selection, Home. What it needed:
- `registrar.cpp`: the registrar slice `BMessageRunner` talks to (register/unregister/set-params/get-info) behind `BRoster::Private::SendTo`, with one timer thread. This is what makes B_PULSE (the 500 ms caret blink), tooltips and BTextView's auto-scroll runners exist. Real `MessageRunner.cpp` is unchanged.
- `clipboard.cpp`: an in-process `BClipboard` (process-global per name; not shared between processes). `stubs_iface.cpp`: `beep()` (hollow), `__swap_float/__swap_double`, `BCatalog::GetString` identity + `gSystemCatalog`, real `send_data/receive_data/has_data`.
- Server: `AS_VIEW_INVERT_RECT`, `AS_FILL_REGION` and `B_OP_INVERT` (caret and selection are inverted rects), `AS_GET_MOUSE`.
- Three kit-level bugs found: **B_WINDOW_ACTIVATED was sent to the preferred handler (the focus view), which BWindow drops**, so no view ever saw activation (this also explains the old "2 unexplained focus-ring pixels"); **drawing outside an update was never presented** (caret blinks draw that way); **updates were not clipped to the dirty region**, so any widget that fills its whole background in a partial repaint (BMenu) wiped its neighbours.
- Test hooks: `aurora::inject_special_key` (BackSpace/Delete/Home/End/PageUp/PageDown: --test-input has no names for them), env `AURORA_APPSERVER_ACTIVE=1` (activation without a seat), `AURORA_APPSERVER_CLICK=x,y`, `AURORA_APPSERVER_DUMP_SEQ=1`.

**Menus.** A BMenu pop-up is its own `BMenuWindow` (feel 1025), positioned in screen coordinates, so yes: window opcodes were needed. Served: `AS_SET_SIZE_LIMITS` reply (it blocked the tracking thread forever; the server now also grows a window to its minimum, which layouts need), `AS_GET_SCREEN_FRAME`, `AS_GET_SCREEN_ID_FROM_WINDOW`, `AS_GET_MENU_INFO/SET`, `AS_CREATE_BITMAP/DELETE_BITMAP/VIEW_DRAW_BITMAP` (in-process areas; B_CMAP8 through Haiku's own `kSystemPalette`; BMenu lays out with 5 bitmaps), `AS_GET_MOUSE` from a process-wide pointer. Menu windows are presented as `xdg_popup`s of the application's main window (`au_wl_popup_create`; geometry fixed at creation, so a moved or resized menu is re-created). The compositor already supports popups (no grab).

**Layouts** needed nothing from the BView API, only the window-size-limits reply above (and `GridLayout` added to the build list).

Not built / not verified: keyboard navigation of an open menu (keys do not reach the tracker); selecting an item through the compositor (a 1 ms press+release, the only click --test-input has, is not seen by BMenu's 16 ms polling; verified only with 80 ms clicks via the server's internal pointer events); submenus (Format) opened, popup grabs/dismissal on outside click, BPopUpMenu, menu fields; bitmaps other than 32/24/8-bit and CMAP8, scaling is nearest-neighbour; update clip is the bounding box of the dirty region, not the region; IME, drag and drop, undo of BTextView through menus, text on multiple lines/wrapping/scrolling are untested; the clipboard is per-process.

## 10. The kitchen sink: every control in one application

Run 2026-11-06. `kits/interface/demo/kitchensink.cpp` (one `BApplication`, one `BWindow`, 371 lines) and `demos/kitchen-sink/run.sh`. Screenshots taken by the compositor's own capture are in `demos/kitchen-sink/screenshots/` (cropped to the window, plus one full 1280x800 capture with the alert open).

```
demos/kitchen-sink/run.sh                      # builds, starts a headless compositor and the app, drives it, decodes the captures
KEEP_CAPTURES=/tmp/ks demos/kitchen-sink/run.sh  # keeps every capture and log of the run
kits/build/interface/iface_kitchensink         # on a real Wayland session
```

**What is on it.** A `BMenuBar` (File: Reset all, Show alert, Quit; Edit: Clear text, Next tab; with Command shortcuts) over a `BTabView` with four pages, laid out with `BLayoutBuilder`, and a status line. Every control is live: acting on it changes a label or another control, and prints `EV name value` on stdout.

| Page | Controls (Haiku's real classes) | What they do |
|---|---|---|
| Buttons | `BButton`, `BStringView`, `BBox` x2, `BCheckBox` x2, `BRadioButton` x3 | the button counts presses; the checkboxes switch a label's font to bold/italic; the radio group sets the size label; "Show alert..." opens a `BAlert` whose answer is written back |
| Input | `BTextControl`, `BOptionPopUp`, `BSlider`, `BStatusBar`, `BSpinner` | typing in the field edits a greeting; the pop-up picks a value; the slider drives the status bar; the spinner counts |
| Data | `BListView` + `BScrollView`, `BTextView` + `BScrollView`, `BSplitView` | picking a list item appends a line to the text view; the divider drags |
| Extras | `BColorControl`, `BCardLayout` (with `<` `>` buttons) | the colour control paints a swatch; the buttons turn the cards |

**What `run.sh` proves, with decoded pixels of the compositor's capture** (76 checks since section 11; 75 before). References are made by separate processes with no compositor, pointer or keyboard (`AURORA_APPSERVER_PRESENT=0`; `AURORA_APPSERVER_CLICK` for a clicked window), so "the app did something" and "this is what it should look like" meet only in pixels:

- The app's own last frame appears verbatim in the capture at (0,0); each page, reached by clicking its tab (or through the Edit menu), equals the headless reference of that page pixel for pixel (page 3 bar the caret, see section 7). Every control on every page has pixels of its own.
- **Checkbox:** a click changes 310 pixels, all inside the style box, equal to a reference render of a clicked window; a second click returns the capture to the first one, pixel for pixel. **Radio:** the accent-coloured dot (29 px) moves from Medium to Large, and nothing outside the box changes. **Button:** three clicks, the label grows from "Pressed 1 time" to "Pressed 3 times".
- **Slider:** the thumb is aimed at from the kit's own `ThumbFrame()` and its movement is decided from pixels alone, by no colour (the columns of its row that changed between two captures are two runs, the old thumb and the new one; this used to look for a face colour of 250,250,250, which is a property of one palette); the button is held with the new `press`/`release` test-input commands and dragged 80 px; the thumb moves 77 px (quantised to whole values), the slider reports 4 values ending at 44, and the status bar's fill, measured in pixels, is 44 % of its width.
- **Text control:** `key a`, `key b` through the compositor's keyboard put glyph pixels in the field and change the greeting label.
- **Spinner, pop-up:** the `+` arrow twice gives 2; holding the button on the option pop-up opens a real `xdg_popup` (two new item rows in the capture), dragging to the third item and releasing selects it and the pop-up disappears (the rows it covered equal the capture from before).
- **List, scroll bar, text view, split view:** five clicks on the scroll bar's arrow scroll the list (all changed pixels inside the list); clicking row 7 highlights it and appends a line in the text view; three typed characters land in the text view; a 60 px drag of the divider moves the right-hand view's edge by 60 px.
- **Colour control, card layout:** a click on the grey ramp reports `EV colour 6974058`, and the swatch's pixel in the capture is exactly (106,106,106); `>` replaces the card's text, `<` twice goes round to card 2.
- **Menus** (the tracker polls the button, so the button is held): File > Reset all returns the whole window, after everything above had been changed, to the first page's pixels (**0 differing pixels** against the headless reference of page 0); Edit > Next tab twice reaches the Data page; Edit > Clear text empties the text view (1234 ink pixels to 0); File > Quit ends the process.
- **BAlert:** "Show alert..." opens a second window; its own frame is found verbatim in the capture; the OK button (located by the app in the alert's coordinates) is clicked in the capture and the answer arrives; Command+A (`down alt`, `key a`) opens it again and Escape cancels it.
- **`aurora::unhandled_opcodes()` is 0** for the whole run (printed by the app when it quits, `UNHANDLED 0 (0 of them asked for a reply)`), and `ctest` has the same assertion headless (`iface_kitchensink`, every page drawn).

**What it took** (every item below was hit and fixed by running the app):

- **Compiled in place, nothing copied:** `CheckBox RadioButton Box TextControl AbstractSpinner Spinner DecimalSpinner OptionControl OptionPopUp StatusBar TabView Slider ColorControl Alert CardLayout SplitLayout SplitView` plus the files they pull in: `GroupView`, `SpaceLayoutItem` (`BLayoutBuilder` glue and `BAlert`), `TextInput` (`BTextControl`'s `_BTextInput_`) and `src/kits/shared/HSL.cpp` (`BTextControl` colours). `IFACE_REAL` now names 79 files. All of them compiled on the first attempt; the problems were at link and run time. **None had to be left out.**
- **Drawing gaps filled in `appserver/` (the ones the controls hit):** `AS_FILL_ELLIPSE_GRADIENT` (the radio button's circle was not drawn at all), stroked ellipses (it drew nothing) and stroked round rects (it drew a plain rect). The fill rasteriser takes several loops now, which is what a ring is. The user-clip-region, pattern and drawing-mode gaps of section 7 were **not** hit by any control, so they were not touched.
- **New opcodes:** `AS_VIEW_SET_TRANSFORM` (identity only: `BTab` sets and resets one around every label; the trace showed it hundreds of times on the Input page), `AS_VALID_SCREEN_ID`, `AS_SCREEN_GET_MODE` (`BScreen`), `AS_GET_DECORATOR_SETTINGS` (`BAlert` centres itself), `AS_SET_VIEW_CURSOR` (acknowledged; `BSplitView`), `AS_VIEW_SET_MOUSE_EVENT_MASK`.
- **Two bugs in the port, not in Haiku's code.** (1) `BSlider::UpdateTextChanged()` does `strdup(UpdateText())` and `UpdateText()` returns NULL by default: Haiku's libroot returns NULL, glibc crashes, so *a plain `BSlider` killed the process the moment it was attached to a window*. Fixed for the whole kit with a force-included header (`shim/strdup_null.h`, a `-include` on `haiku_iface_real`). (2) `BMenuField` (so `BOptionPopUp`) opens its menu from `MouseDown`, calls `SetMouseEventMask(B_POINTER_EVENTS)` and installs a filter that drops every `B_MOUSE_DOWN` until the field has seen the `B_MOUSE_UP`. The server ignored event masks, so the up went to the menu bar inside the field, the filter never came off, and **after choosing a pop-up item no click reached the window again**. The server now honours the mask (the view that asks becomes the pointer grab until the button is up).
- A view on a hidden tab page no longer draws (its clip is empty): without that, an outline the size of the Input page's spinner field appeared on the Buttons page when a second alert took the window's activation (seen in a capture; it is gone from every capture after the fix).
- `test_widgets` (17 checks) covers the new controls headless (values read back, pixels changed, a slider drag, a tab click, no unhandled opcodes); `ctest` in `kits/` is 16 of 16.

**Known rough edges of the showcase:** the alert has no icon and opens at (0,0) over the main window (section 7); menu shortcuts show their Command-key symbol as a small box; the colour control's text field shows a blinking caret when its page is first shown (which is why one pixel column is exempt in the check, section 7).

## 11. Aurora's colours and control look

Added 2026-11-06. Haiku apps used to render in Haiku's compiled-in greys and knew nothing of the desktop's design system. Two levers, both entirely in `kits/interface/`, nothing under `HAIKU_SRC` touched or copied:

**System colours: `aurora_theme.cpp`.** `applite.cpp` used to set `fServerReadOnlyMemory = NULL`, so `ui_color()` answered Haiku's defaults. It now points it at a `server_read_only_memory` (Haiku's own struct, `ServerReadOnlyMemory.h`) whose colour table is filled from `contracts/theme/aurora-glass.toml`, `[theme.colors]`. Every control already asks `ui_color(B_PANEL_BACKGROUND_COLOR)` and friends, so everything is recoloured with no per-widget work, and the server-side resolution of `SetViewUIColor` (`AS_VIEW_SET_*_UI_COLOR`, `server.cpp`) follows, because it calls the same `ui_color()`.

- **The rule** (the same as `compositor/src/fx/theme.rs`, `oklab_to_u8`, and the toml says so next to the tokens): Oklab token to linear sRGB by Ottosson's matrices, each channel clamped to 0..1, IEC 61966-2-1 encode, times 255, rounded to nearest, alpha 255; all in 32-bit float and in the same order of operations (`aurora_theme.cpp` is built with `-ffp-contract=off`, because a fused multiply-add changes bits Rust's separate multiply and add would not). A token that is also a compositor token (`background.top`, which is `B_DESKTOP_COLOR`) therefore has identical bytes in the shell and in a Haiku app.
- **Mapped** (33 `color_which` constants, listed in `aurora::ColorMappings()` and next to each token in the toml): panel, menu, document, list, control and tooltip backgrounds and their text; control border, highlight and mark; `B_KEYBOARD_NAVIGATION_COLOR` and `B_MENU_SELECTED_BORDER_COLOR` (the accent, which is the focus ring); list and menu selection; scroll thumb; the four link colours; status bar; success and failure; shine and shadow; `B_DESKTOP_COLOR`. **Not mapped:** the `B_WINDOW_*` tab and border colours (the compositor draws no decorations), which keep Haiku's defaults; a colour the theme does not map is left `B_TRANSPARENT_COLOR` in the table and `ui_color()` answers its default.
- **Where the file comes from:** the copy compiled in at build time (`contracts/theme/aurora-glass.toml` read by CMake into a raw string literal: the analogue of the compositor's `BUILTIN_THEME`), overlaid by `$AURORA_THEME_DIR/aurora-glass.toml` (`aurora-up` now exports it, pointing at the copy the compositor hot-reloads), else the repo's `contracts/theme/` when running from the build tree (the compositor's `default_theme_dir()`). A key the file lacks or cannot read keeps the earlier value, as in the compositor.
- **Not done: live re-theming.** A Haiku app reads the file once, at start. `B_COLORS_UPDATED` would need a poll of the file, a broadcast to every window, and the server re-resolving each view's stored semantic colour (`highWhich`/`lowWhich`/`viewWhich` are kept, so it is possible); that is more than a small addition. The compositor hot-reloads; apps pick the theme up when they next start.

**Control look: `aurora_controllook.cpp`.** `AuroraControlLook` is a subclass of `HaikuControlLook` (compiled in place), installed at `applite.cpp` in place of `new HaikuControlLook()`. It overrides only:

- `DrawButtonFrame`, `DrawButtonBackground` (radius-less forms): a button is a rounded rectangle, radius `corner_ratio` (0.28, the dock tile's rule) of two font sizes, clamped to 2..8 px; flat face (the control colour, darker on hover and pressed); border `B_CONTROL_BORDER_COLOR`; **a focused button's border is two pixels of the accent**; a default button's is two pixels, darker. Insets are Haiku's exactly (`GetFrameInsets` is not overridden; `test_theme` compares them), so no layout changes. Flat toolbar buttons and partial borders fall through to Haiku.
- `DrawGroupFrame` (`BBox`): one rounded line instead of three nested bevels, the same three pixels deep.
- `DrawTextControlBorder`: Haiku's, plus a second accent pixel on the outer edge when focused (two pixels of accent in all).
- `DrawScrollBarBackground`, `DrawScrollBarThumb`, `DrawScrollBarButton`: flat track, a pill-shaped thumb in `B_SCROLL_BAR_THUMB_COLOR` (>= 3:1 on the panel), flat arrow buttons. (`BScrollBar` passes the thumb its own colour as `base`, and the panel is the view colour.)
- Everything else (sliders, tabs, menus, menu fields, status bar, check boxes, radio buttons) is Haiku's, drawn in the new colours.
- **Why the buttons are drawn here and not by asking Haiku for a radius:** Haiku rounds a corner by drawing it and then excluding it from the clip with `BView::ClipToInverseRect`. This server clips to one rectangle (section 7), so Haiku's own radius draws square corners with stray arcs. Drawing rounded rectangles through `FillRoundRect`/`StrokeRoundRect` needed a real fix in `appserver/canvas.cpp`, found when the first rounded button drew only its four corner arcs: `RoundRectPath` listed its four corner centres in an order that did not match its angle sweep, so every round rect was a self-intersecting polygon (no control Haiku draws goes through it).
- **The switch:** `[theme.controls] look = "aurora" | "haiku"` in the theme file, or `AURORA_CONTROL_LOOK=haiku|aurora` in the environment (which wins). `haiku` is the stock `HaikuControlLook`, in the same theme colours. Colours have their own switch, independent of the look: `[theme.controls] colors = "haiku"` or `AURORA_COLORS=haiku` leaves `ui_color()` on Haiku's compiled-in greys (`iface_theme_haiku_colors` checks every mapped constant against `BPrivate::GetSystemColor`). Both switches off is the stock Haiku of before this section.

**How it is tested without freezing the palette:**

- `test_theme` (ctest `iface_theme`, `iface_theme_custom_palette`, `iface_theme_haiku_look`) states rules over whatever the theme says: every mapped `color_which` answers its token's bytes; a window painted with the panel colour has exactly those bytes in its frame; the contrast floors written in the toml (4.5:1 text, 3:1 non-text) hold for the palette in force; the parser overlays and ignores what it cannot read; frame and background insets equal Haiku's. `custom` writes a dark palette on the spot and runs the same rules: it passes only if nothing hard-codes the shipped palette.
- `demos/theme-drift/run.sh` pins the toolkit and the shell together: `harness/theme-oracle` includes `compositor/src/fx/theme.rs` unmodified and prints the bytes it derives for each token; the toolkit's reader and `ui_color()` must print the same (24 shipped tokens, then 400 random ones including out-of-gamut, bit for bit); a headless compositor draws a solid desktop of each token (hot-reloaded) and the decoded capture must show the oracle's bytes; the kitchen sink's panel pixel must equal the desktop pixel when the desktop colour is the panel token, under two palettes.
- `demos/kitchen-sink/run.sh` and `demos/interface-kit/run.sh` pass in both looks (`AURORA_CONTROL_LOOK=haiku` runs the stock one). Their references are renders by separate processes of the same binary, so they were already palette-independent; what was not, and now is: the slider thumb (found by colour 250,250,250) and the list demo's white background (`--band`), now the toolkit's `ui_color()` answer; and `test_controls` asked every look for a gradient, now only the stock one.
