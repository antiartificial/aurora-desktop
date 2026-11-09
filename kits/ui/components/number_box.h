// 8.7, component 2 of 12: a numeric field with steppers and a range.
//
// One NODE rather than a field plus two IconButtons, because a spinbutton is ONE tab stop: a
// composite would put the two steppers in the keyboard path as separate controls, and a user
// tabbing through a form would stop three times on a single number. The steppers are drawn and
// hit-tested here for that reason, and the whole control keeps one focus ring.
//
// Clamping and stepping are free functions, not private arithmetic: the range rule is the thing
// worth checking (a step from max must stay at max, and a NaN must be refused rather than
// propagated into the binding), and a test can prove it without a compositor.
#pragma once

#include "../ui.h"

#include <functional>

namespace aurora::ui {

// `v` forced into [lo, hi]. A non-finite value is REFUSED -- it resolves to the range floor
// instead of poisoning the cell, because a NaN that reaches a Binding makes every later
// comparison false and the control stops responding without saying why. Reversed bounds are
// read as a range rather than taken literally.
double ClampNumber(double v, double lo, double hi);

// `dir` steps of `step` (positive = up, negative = down, magnitude = how many), clamped.
// A non-finite value is treated as the range floor; a non-positive or non-finite step, or a
// zero direction, returns the clamped value unchanged -- "no movement" is not an error.
double StepNumber(double v, double step, double lo, double hi, int dir);

// The control. `min`/`max` are the range and `step` is one arrow press; `changed` fires only
// when the committed value actually differs, so an app can persist on it without deduping.
Desc NumberBox(Binding<double> value, double min, double max, double step = 1.0,
               std::function<void(double)> changed = nullptr);

}   // namespace aurora::ui
