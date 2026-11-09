// A modal confirmation, and ONLY the non-destructive kind.
//
// The authority design (decisions 0009 and 0011) puts approval of a destructive or external verb
// on the trusted path: the compositor's own surface, drawn by code the app's process cannot reach
// and cannot paint over, where the effect class, the grants and the request id are shown before
// the ticket is minted. This is ordinary client UI -- it lives in the app's tree, so anything in
// that process can cover it, capture it or synthesise a click on it -- and therefore it must not
// be able to ask for something that loses data. Its primary action is a confirmation the user can
// take back, and a destructive one is not expressible here at all:
//
//   * The primary is an `Affirm`, a closed set with no destructive member, and the LABEL is read
//     from AffirmLabel() instead of from the caller. "Delete" and "Move to Trash" cannot be put
//     in the primary position, because the call site has nowhere to write them.
//   * The obvious escape hatch -- passing the label yourself -- is a deleted overload, so it is a
//     compile error naming the rule rather than a silent way around it.
//
// That is a real constraint and not a naming convention: adding a destructive member to Affirm
// means deleting this paragraph first, and the test fails if any label in the table ever names a
// destructive verb. A dialog that must confirm a delete belongs to `shell/picker/confirm.go`,
// which runs on the trusted path; a toplevel can be parented to the app's window with
// au_wl_win_set_parent when it needs to look like one of its dialogs.
//
// What this does NOT check is the body of on_confirm: no type can see what a closure does with its
// hands. The guard is on what the dialog ASKS the user to approve -- the verb it names and the
// yes/no shape -- and an app that wires a destructive command to it is misusing the component, not
// escaping it silently. The effect register (decision 0009) is where that call is caught, because
// there the verb carries its class and the approval gate reads it.
//
// Showing and hiding: the tree is built once and has no reconciler, so a dialog that comes and
// goes is two built branches and a cell --
//
//   Either(open, ContentDialog(...), page)
//
// `open` is a State<bool>; the dialog branch is measured, drawn and hit-tested only while it is
// live, so a closed dialog costs nothing and keeps no focus.
//
// The node fills the space it is given and paints a scrim over it, which is what makes it modal
// for the POINTER: a click outside the plate lands on this node and goes nowhere. Keyboard
// modality is narrower on purpose -- Tab order belongs to the Window and a widget that swallowed
// Tab would trap a keyboard user -- so the dialog asks for focus on its primary button when it is
// attached (safe precisely because that button cannot be destructive), and answers Escape and
// Return for keys that reach it. An app that must catch Escape wherever focus is uses its own
// Window::SetKeyHook; that is the same answer the chooser reached.
#pragma once

#include "../ui.h"

#include <cstdint>
#include <functional>
#include <string>

namespace aurora::ui {

// The primary action a ContentDialog may offer. A CLOSED set, and there is deliberately no
// `Delete`, `Replace` or `MoveToTrash`: the type is the policy. Each verb names something the
// user asked for and can undo by doing it again -- applying a setting, saving a copy, opening a
// view -- so the worst outcome of a mis-click here is a second click.
enum class Affirm : std::uint8_t { Accept, Apply, Save, Open, Continue, Retry };
constexpr int kAffirmCount = 6;          // exhaustive, so a test can walk the table

// The label the primary button draws. A table and not a parameter: see the header comment.
// Returns nullptr for a value outside Affirm, which no caller can spell but a cast can.
const char* AffirmLabel(Affirm a);

// The names the two buttons carry, for Window::FocusNamed and for a test. Unique while a dialog
// is on screen, which is what "modal" means: two of them at once would collide.
inline constexpr const char* kDialogPrimaryName = "dialog:primary";
inline constexpr const char* kDialogCancelName = "dialog:cancel";

// title: the question, one line, drawn bold.
// body:  the consequence, wrapped to the plate and clipped rather than scrolled -- a confirmation
//        that needs a scrollbar is a page wearing a dialog's clothes.
// on_cancel is optional; the Cancel button is always drawn, because a confirmation whose only
// button is "yes" is a notification with a click tax.
Desc ContentDialog(std::string title, std::string body, Affirm primary,
                   std::function<void()> on_confirm,
                   std::function<void()> on_cancel = nullptr);

// Deleted on purpose: a caller-supplied primary label is exactly how a destructive verb would get
// into this component. Kept declared so the error names the rule instead of "no matching call".
Desc ContentDialog(std::string title, std::string body, const char* primary_label,
                   std::function<void()> on_confirm,
                   std::function<void()> on_cancel = nullptr) = delete;

}   // namespace aurora::ui
