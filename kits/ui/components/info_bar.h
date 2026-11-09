// 8.7, component: an inline status strip.
//
// A message that belongs in the flow of a pane -- "3 files could not be moved", "this folder is
// read only" -- rather than a dialog that interrupts or a toast that leaves. It carries at most
// ONE action, because a strip with two buttons is a form and a form is a different thing.
//
// Severity is a TYPE and not a colour argument. A caller says what the message MEANS and the
// component picks the token, so a warning and an error cannot be handed each other's colour by
// a typo, and a new severity is one case here rather than a search for every literal.
#pragma once

#include "../ui.h"

#include <cstdint>
#include <functional>
#include <string>

namespace aurora::ui {

enum class InfoSeverity : uint8_t { Info, Warning, Error };

// The name the accessibility tree and a test find a severity by. A function for the same reason
// VStateName is one: a string spelled at a call site is a second source of truth.
const char* InfoSeverityName(InfoSeverity s);

// The token a severity draws with. Free and surface-free so a theme and a test can ask what a
// strip will look like without building a window around it.
//
// Info is NOT success: a strip that says "2 items skipped" has succeeded at nothing, and painting
// it green would tell a person the opposite of what the words say. Success is for an outcome,
// which is a different component's job.
au_color InfoSeverityInk(const Env& e, InfoSeverity s);

// The message alone.
Desc InfoBar(InfoSeverity severity, std::string message);

// The message plus one action, drawn at the trailing edge.
Desc InfoBar(InfoSeverity severity, std::string message, std::string action_label,
             std::function<void()> on_action);

}   // namespace aurora::ui
