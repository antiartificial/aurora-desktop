// 8.8/P6: emit the presentation contract (contracts/surface/surface.go) from a Desc tree.
//
// The contract is the wire form three sides agree on -- toolkit emits, controller relays, shell
// draws -- so this file's job is not "turn a tree into some JSON" but "produce THE canonical
// bytes": actions sorted by id, grants sorted, one effect per action that is read|write|nominal
// and nothing else. A surface that reorders between runs makes a golden diff useless, and an
// effect outside the three is an action the controller cannot police, so both are refused here
// instead of emitted hopefully.
//
// Grants are the caller's. Nothing in a widget can know which capability a click will need, so the
// toolkit takes them as an argument and never guesses from a name -- the same rule the contract
// states, kept at the point where the descriptor is built.
#pragma once

#include "ui.h"

#include <string>
#include <vector>

namespace aurora::ui {

// The contract's Validate(), on the toolkit side. True when d is drawable and policed: an app is
// named, action ids are non-empty and unique, and every effect is read/write/nominal. Fills *why
// (when given) with the first reason it is not.
bool SurfaceOk(const SurfaceDescriptor& d, std::string* why = nullptr);

// Fills *out from the tree. A node named "act:<verb>[=<label>][#<effect>]" offers that action;
// the effect suffix defaults to nominal because a widget cannot know better, and the label
// defaults to the verb. Grants are the caller's, copied in as given -- the surface-level set only;
// per-action grants stay empty here because they are a separate claim and the contract allows a
// caller to set them on the returned descriptor before encoding.
//
// False with *why when the tree breaks the naming rule: an empty verb, an effect outside
// read/write/nominal, or two actions claiming one id (the contract rejects duplicates, and
// emitting them would make the shell's action lookup ambiguous).
bool SurfaceOf(const Desc& root, std::string app, std::string title,
               std::vector<std::string> grants, SurfaceDescriptor* out,
               std::string* why = nullptr);

// The canonical encoding of d, byte-compatible with the Go Marshal: actions sorted by id, grants
// sorted, an empty grants list omitted (omitempty), and strings escaped the way encoding/json
// escapes them -- quote, backslash, \b \f \n \r \t, other control characters as \u00XX, and
// < > & as \u003c \u003e \u0026, which is what Go's encoder does by default.
bool SurfaceJson(const SurfaceDescriptor& d, std::string* out, std::string* why = nullptr);

// Tree straight to bytes, for the usual case with no per-action grants.
//
// Deliberately not a single std::string return: the toolkit does not use exceptions and an
// invalid descriptor must not silently become one empty string that a caller pipes to the bus.
// The bool is the answer; *out is untouched when it is false.
bool SurfaceJson(const Desc& root, std::string app, std::string title,
                 std::vector<std::string> grants, std::string* out,
                 std::string* why = nullptr);

}   // namespace aurora::ui
