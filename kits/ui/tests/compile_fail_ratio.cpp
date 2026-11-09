// This file MUST NOT compile: a design size is a ratio, not a number. Kept as a ctest with
// WILL_FAIL so the rule is checked by the compiler rather than trusted to a review.
#include "ui.h"

void probe() {
    aurora::ui::TextStyle st{.size = 14};   // must be Ratio(14) or a multiplier
    (void)st;
}
