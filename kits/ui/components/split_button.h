// 8.7, component 1 of 12: a verb plus its ranked alternatives.
//
// The default action is a real Button; the alternatives are behind the adjacent menu. That is the
// whole point of the component -- the plumber's list as one control instead of a form -- and neither
// half exists in a toolkit with no verbs underneath it.
//
// One file per component: the twelve land in parallel, so a component owns its own header, source
// and test and no two of them edit a shared file.
#pragma once

#include "../ui.h"

#include <functional>
#include <string>
#include <vector>

namespace aurora::ui {

Desc SplitButton(std::string label, std::function<void()> primary,
                 std::vector<MenuItem> alternatives, Binding<bool> enabled = Binding<bool>(true));

}   // namespace aurora::ui
