//! Text input and input method: the compositor side of `zwp_text_input_v3` and `zwp_input_method_v2`,
//! proved by the protocol traffic two real clients see. `examples/testclient --text-input` is the text
//! field (it logs every `ti ...` event); `examples/imeclient` is the input method (it logs `im ...`).
//! The compositor draws nothing for the preedit: the text-input client does.

mod common;
use common::*;

fn lines(log: &std::path::Path) -> Vec<String> { read(log).lines().map(str::to_string).collect() }

fn has(log: &std::path::Path, what: &str) -> bool { read(log).lines().any(|l| l == what) }

fn count(log: &std::path::Path, what: &str) -> usize { read(log).lines().filter(|l| *l == what).count() }

/// Index of the first line equal to `what` at or after `from`.
fn pos(log: &std::path::Path, what: &str, from: usize) -> usize {
    lines(log).iter().skip(from).position(|l| l == what).map(|i| i + from).unwrap_or_else(|| panic!("`{what}` not found after line {from} in:\n{}", read(log)))
}

/// Index of the first line starting with `prefix` at or after `from`.
fn pos_prefix(log: &std::path::Path, prefix: &str, from: usize) -> usize {
    lines(log).iter().skip(from).position(|l| l.starts_with(prefix)).map(|i| i + from).unwrap_or_else(|| panic!("no `{prefix}...` after line {from} in:\n{}", read(log)))
}

#[test]
fn both_globals_are_advertised() {
    let c = Comp::start(&[]);
    let g = c.globals();
    assert!(g.contains("zwp_text_input_manager_v3"), "{g}");
    assert!(g.contains("zwp_input_method_manager_v2"), "{g}");
}

#[test]
fn preedit_and_commit_reach_the_focused_text_input() {
    let mut c = Comp::start(&[]);
    let im = c.example("imeclient", &["--preedit", "ni", "--commit", "\u{4f60}"]);
    wait_for("input method bound", || has(&im, "im bound"));
    let ti = c.client_args(&["--size", "300x200", "--text-input"]);
    wait_for("text input entered", || has(&ti, "ti enter"));
    wait_for("input method activated and sent", || has(&im, "im sent"));
    wait_for("commit reached the client", || read(&ti).contains("ti commit"));
    println!("TEXT INPUT LOG:\n{}\nINPUT METHOD LOG:\n{}", read(&ti), read(&im));
    // The input method was activated and had said `done` before it sent anything.
    let a = pos(&im, "im activate", 0);
    // (a first, empty `done` precedes the activation: the state before any field is focused)
    let d = pos(&im, "im done 2", a);
    let sent = pos(&im, "im sent", d);
    assert!(a < d && d < sent);
    // The text input saw: enter, then the preedit, then done, then the commit, then done.
    let e = pos(&ti, "ti enter", 0);
    let p = pos(&ti, "ti preedit \"ni\" 0 2", e);
    let d1 = pos_prefix(&ti, "ti done", p);
    let c1 = pos(&ti, "ti commit \"\u{4f60}\"", d1);
    let d2 = pos_prefix(&ti, "ti done", c1);
    assert!(e < p && p < d1 && d1 < c1 && c1 < d2);
}

#[test]
fn focus_enter_and_leave_follow_the_keyboard_and_drive_the_input_method() {
    let mut c = Comp::start(&[]);
    let im = c.example("imeclient", &["--preedit", "ka", "--commit", "k"]);
    wait_for("input method bound", || has(&im, "im bound"));
    // A: a text field, 400x300 at the origin.
    let a = c.client_args(&["--size", "400x300", "--text-input"]);
    wait_for("A entered", || has(&a, "ti enter"));
    wait_for("im activated for A", || has(&im, "im activate"));
    wait_for("A got the commit", || read(&a).contains("ti commit \"k\""));
    assert_eq!(count(&im, "im activate"), 1);

    // B: a small window mapped on top takes the keyboard: A's text input leaves, the input method is deactivated.
    let b = c.client_args(&["--size", "100x100", "--rgb", "20,200,20"]);
    wait_for("B has the keyboard", || has(&b, "enter"));
    wait_for("A left", || has(&a, "ti leave"));
    wait_for("input method deactivated", || has(&im, "im deactivate"));
    assert_eq!(count(&a, "ti enter"), 1, "no second enter while B has focus: {}", read(&a));

    // A click on A (outside B's 100x100) moves the keyboard back: enter again, and the method is activated again.
    c.inject("pointer 300 250");
    c.inject("click");
    wait_for("A entered again", || count(&a, "ti enter") == 2);
    wait_for("im activated again", || count(&im, "im activate") == 2);
    wait_for("A got a second commit", || read(&a).matches("ti commit \"k\"").count() == 2);
    let leave = pos(&a, "ti leave", 0);
    let enter2 = lines(&a).iter().rposition(|l| l == "ti enter").unwrap();
    assert!(leave < enter2);
    println!("A:\n{}\nIM:\n{}", read(&a), read(&im));
}

#[test]
fn an_input_method_that_binds_after_the_field_is_focused_is_activated_too() {
    let mut c = Comp::start(&[]);
    let ti = c.client_args(&["--size", "300x200", "--text-input"]);
    assert!(!has(&ti, "ti enter"), "no input method yet, so the field is not entered: {}", read(&ti));
    let im = c.example("imeclient", &["--preedit", "x", "--commit", "y"]);
    wait_for("text input entered", || has(&ti, "ti enter"));
    wait_for("input method activated", || has(&im, "im activate"));
    wait_for("commit reached the client", || read(&ti).contains("ti commit \"y\""));
}
