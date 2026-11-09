# The twelve components

The vocabulary `docs/03-ergonomics.md` in `aurora-ui-design` asks for, as it exists in the toolkit.
Twelve were named; five already existed inside `ui.h`, the other seven are one file pair each under
`kits/ui/components/` — which is also what let them be written in parallel.

| Component | Role | Lives in | What it is |
|---|---|---|---|
| `Sidebar` | `navigation` | `components/sidebar.h` | A list with sections; the NavigationView-lite a file manager needs |
| `Toolbar` | `toolbar` | `components/toolbar.h` | Leading / location / trailing clusters, 44 px, hairline |
| `SplitButton` | `splitbutton` | `components/split_button.h` | A verb plus its ranked alternatives: default action + menu |
| `NumberBox` | `spinbutton` | `components/number_box.h` | A typed verb argument: min/max/step, typed and clamped |
| `InfoBar` | `status` | `components/info_bar.h` | Inline info/warning/error strip with an optional action |
| `Expander` | `group` | `components/expander.h` | A disclosure header that reveals content |
| `ContentDialog` | `dialog` | `components/content_dialog.h` | Modal confirmation — **non-destructive only, by type** |
| `TeachingTip` | `note` | `components/teaching_tip.h` | A first-run explanation, shown while the caller says so |
| `BreadcrumbBar` | `breadcrumb` | `ui.h` (`Breadcrumb`, `CrumbNode`) | The path, with per-segment buttons |
| `ItemsRepeater` | `list` | `ui.h` (`ListView`, `IconView`) | Rows and tiles over a `Rows` source |
| `Menu` | — | `ui.h` (`Menu`, `MenuButton`) | The context menu and its button |
| `TreeView` | `tree` | `ui.h` (`TreeView`, `TreeNode`) | Expandable, demand-driven tree |

## The rules the twelve share

1. **A widget owns no state.** Anything that changes is a `Binding<T>`; a widget that must remember
   something takes a cell, it does not keep a member.
2. **State colours come from one table.** `ResolveSkin(Env, VState)` answers all six states; a
   component that reports flags to `row_state()` and draws from the table inherits the three
   selection pictures (selected+focused, selected+unfocused, cursor only) rather than re-deriving
   them.
3. **A role better than `control`.** `aurora-uitree --check` fails if a component root still answers
   `control`.

## How they reach the OS

A widget's action label and the command it names have to stay in step, and today that link is a
comment: nothing under `components/` emits an `act:` name — only apps do (`demo/atlas.cpp`). The
twelve-component seam and the surface seam still meet in the app rather than in the component. See
`docs/surface-seam.md` for what that costs and what it would take to move it.

## Seeing them

`aurora-preview` renders a state set per component (20 states × 3 viewports = 60 goldens), and
`aurora-uitree --check` fails if a component root answers only `control`. `aurora-gallery` is a
Photo Library that uses all twelve in one believable app — sidebar sections,
a toolbar with SplitButton and NumberBox, a list of photos, a Details Expander, InfoBars for empty
scopes, a TeachingTip on first run, and a ContentDialog behind Copy path. It is the review surface;
`aurora-preview` is the regression surface.
