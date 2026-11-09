// 8.7, component 2 of 12: the command bar -- a fixed-height chrome band of controls with one
// flexible slot for the thing being commanded on.
//
// It is a CONTAINER, not a widget that owns a list of buttons. The design's bar holds a
// breadcrumb, a path field and a menu button as well as glyph buttons (6.9), and a bar that
// could only hold its own button type could not be that bar. So the groups take Descs and the
// one thing the bar decides is the arrangement: 8 px ends, 4 px inside a cluster, 12 px between
// clusters, 44 px tall, never wrapping and never scrolling.
//
// Stated rather than discovered later:
//  - The toolkit has no per-child minimum width, so the location slot shrinks to zero once the
//    clusters and the gaps have eaten the bar. Elision belongs to what sits in the slot (the
//    Breadcrumb elides its own middle), and 6.9's collapse order -- the filter field first --
//    is the caller's Either/sized decision, because only the app knows which control is the
//    filter.
//  - IconButton has no persistent pressed state, so a toggle such as the preview button draws
//    as a plain button. "Raised while open" needs a toggle kind, not a flag this bar can fake.
#pragma once

#include "../ui.h"

#include <functional>
#include <initializer_list>
#include <string>
#include <vector>

namespace aurora::ui {

// A run of controls sharing the narrow gap. Two groups of buttons read as two clusters, which
// is how navigation separates from the things that act on the current selection.
struct ToolbarGroup {
    std::vector<Desc> items;

    ToolbarGroup() = default;
    // So a caller writes Toolbar({{{back, forward}}, {{view}}}): a cluster is two braces, not a
    // named struct repeated, because brace elision stops at an initializer_list element and the
    // list-of-lists form does not compile without this.
    ToolbarGroup(std::initializer_list<Desc> d) : items(d) {}
};

struct ToolbarOpts {
    float height = 44;       // logical px, fixed by 6.9
    float pad = 1.0f;        // units at each end (8 px)
    float item_gap = 0.5f;   // units between controls in one group (4 px)
    float group_gap = 1.5f;  // units between groups, and either side of the slot (12 px)
    bool hairline = true;    // the 1 px rule under the bar that separates it from the content
};

// `leading` sits left of the flexible slot and `trailing` right of it; `location` takes the width
// they leave and is where the breadcrumb or the path field goes (6.10). A null location is
// allowed -- a bar of commands only -- and then a Spacer holds the slot so `trailing` keeps the
// right edge it would have had with a breadcrumb in place.
Desc Toolbar(std::vector<ToolbarGroup> leading, Desc location,
             std::vector<ToolbarGroup> trailing, ToolbarOpts opts = ToolbarOpts{});

// The bar's unit of command: a glyph button with a NAME. The name is the point -- a glyph has no
// text for the accessibility tree, `aurora ui tree` or FocusNamed to read, so a bar of unnamed
// buttons is a bar that a test and an agent cannot address.
Desc ToolbarButton(Glyph g, std::string name, std::function<void()> run,
                   Binding<bool> enabled = Binding<bool>(true));

}   // namespace aurora::ui
