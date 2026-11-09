// 8.7, component 2 of 12: a first-run note that explains one feature and then gets out of the way.
//
// It owns no "seen" flag. Which tips a person has read is the app's business, the same way a
// split ratio belongs to the app's persisted state: a widget that remembered would remember only
// for the life of the process, which is the wrong lifetime and invisible to a test. So the
// caller passes `show` (a Binding, so a State can turn it back on) and an `on_dismiss` callback
// that clears it.
//
// The panel is a note, not a dialog. It takes part in layout where it is placed rather than
// floating over a window, because a first-run explanation beside the thing it explains is read,
// and one over the thing is dismissed before it is read.
#pragma once

#include "../ui.h"

#include <functional>
#include <string>

namespace aurora::ui {

// `title` is bold and wraps with the body; `dismiss_label` names the one action, and that action
// is the only thing that changes `show`.
Desc TeachingTip(std::string title, std::string body, Binding<bool> show,
                 std::function<void()> on_dismiss,
                 std::string dismiss_label = "Got it");

}   // namespace aurora::ui
