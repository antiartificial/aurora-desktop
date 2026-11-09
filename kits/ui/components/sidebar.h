// 8.7: the navigation sidebar -- destinations grouped into sections, one of them selected.
//
// NavigationView-lite, and deliberately not a new drawing. The toolkit's tree widget is already
// the sidebar of docs/atlas-design.md 6.11: it paints the pane, draws the non-selectable section
// headings, answers the four selection pictures from ResolveSkin, and owns the keyboard (Up,
// Down, Enter, Page, Home). What an app holds instead is a description -- sections of
// destinations -- so this component turns that into a flat SectionListSource and hands it to a
// TreeView. That is also how "Places" and "Filesystem" end up in ONE scrolling pane, which two
// stacked widgets could not do: they would scroll apart.
//
// The selection is an id and not an index. The sidebar is shown from whatever the app holds, and
// an index would silently point at a different destination after an insertion or a reorder --
// the same reason the tree remembers expansion by id.
//
// Stated rather than found later:
//  - No icon per destination. TreeItem has no icon field and the tree draws none, so a row is
//    its label. Drawing one here would be a second drawing of the pane that disagrees with the
//    tree's own fills and insets.
//  - Nothing expands. A destination is a leaf with `flat` set: no chevron, no children asked
//    for, no expansion remembered.
//  - An empty section draws nothing. A heading over no destinations is noise, and the app does
//    not have to filter its own list to avoid it.
#pragma once

#include "../ui.h"

#include <functional>
#include <string>
#include <vector>

namespace aurora::ui {

struct SidebarItem {
    std::string id;             // what `selected` holds and what `navigated` is called with
    std::string label;
    bool enabled = true;        // shown dim and unselectable: a destination that cannot open
    std::string note;           // right-aligned at the caption size, e.g. a count; empty = none
};

struct SidebarSection {
    std::string key;            // the heading's stable identity; empty falls back to its position
    std::string title;
    std::vector<SidebarItem> items;
};

// `selected` is live: the tree writes the destination the keyboard or a click landed on, so the
// app reads the selection as a value and a breadcrumb can be bound to the same cell.
// `navigated` fires on Enter and on a double-click, with the destination's id.
Desc Sidebar(std::vector<SidebarSection> sections, Binding<std::string> selected,
             std::function<void(const std::string&)> navigated = nullptr);

// The pane itself, for the verbs only a tree answers: RevealInTree (keep the sidebar in step with
// navigation in the content pane) and OnTreeDrop (a drop onto a destination). Empty when handed
// something that is not a Sidebar, so a caller gets a no-op rather than a wrong node.
Desc SidebarTree(const Desc& sidebar);

// Rebind activation after the tree is built, so a caller holding the Desc can change what a
// destination does without rebuilding the pane -- the same seam OnTreeActivate gives a tree.
void OnSidebarNavigate(const Desc& sidebar, std::function<void(const std::string&)> f);

}   // namespace aurora::ui
