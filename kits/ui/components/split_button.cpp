#include "split_button.h"

namespace aurora::ui {

Desc SplitButton(std::string label, std::function<void()> primary,
                 std::vector<MenuItem> alternatives, Binding<bool> enabled) {
    Desc main = Button(std::move(label), std::move(primary), std::move(enabled)).weight(1.0f);
    Desc more = MenuButton(Glyph::More, [alts = std::move(alternatives)] { return alts; });
    return WithRole(HStack({main, more}, 0.25f), "splitbutton");
}

}   // namespace aurora::ui
