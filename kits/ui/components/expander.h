// 8.7, component: a disclosure header that reveals a section of content.
//
// The open/closed state is the CALLER's Binding<bool>, not a field: the toolkit rule is that a
// widget owns no state. A saved layout, a second control and a test can then all drive one
// expander without asking it what it thinks, and the tree can be rebuilt without losing what was
// open.
#pragma once

#include "../ui.h"

#include <string>

namespace aurora::ui {

// `title` is the section's name, `content` is built once and kept (the reveal animates it, it is
// never re-created). `enabled` follows Button's rule: off, the header dims, ignores the pointer
// and the keyboard, and stays in the tree, because hiding what is unavailable teaches the wrong
// model.
//
// The whole header is the target -- chevron and title -- rather than an IconButton beside a label:
// one disclosure is one control, and two tab stops for one decision is how a keyboard user ends up
// pressing a chevron that does not mean anything on its own.
Desc Expander(std::string title, Binding<bool> open, Desc content,
              Binding<bool> enabled = Binding<bool>(true));

}   // namespace aurora::ui
