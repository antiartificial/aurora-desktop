# 0026. The duration rule is a written-form check, and what would make it an AST check

- **Status:** proposed
- **Date:** 2026-11-08

## Context

P5's duration rule says: a duration used by the interface comes from the motion table, never from a
literal at the call site. The plan says to enforce it with an AST matcher, because a matcher cannot
be fooled by a line continuation, a comment, or a macro.

The matcher works. On a self-contained translation unit, this fires exactly once on
`std::chrono::milliseconds(150)` and not on `milliseconds(m.ms)`:

```
match cxxConstructExpr(
  hasType(hasDeclaration(namedDecl(matchesName("(nanoseconds|microseconds|milliseconds|seconds|minutes|hours)")))),
  isExpansionInMainFile(),
  hasAnyArgument(ignoringImplicit(integerLiteral())))
```

Two earlier attempts reported zero matches and concluded the argument was unreachable. Both
conclusions were wrong, and each was wrong for a different reason:

- The argument *is* reachable, but it is wrapped. `-Xclang -ast-dump` on the toy TU shows the
  shape: `CXXConstructExpr 'milliseconds' 'void (const int &)'` -> `MaterializeTemporaryExpr 'const int' lvalue`
  -> `ImplicitCastExpr 'const int' <NoOp>` -> `IntegerLiteral 'int' 150`. `hasAnyArgument(integerLiteral())`
  matches nothing because the direct child is the `MaterializeTemporaryExpr`;
  `hasAnyArgument(ignoringImplicit(...))` or `hasDescendant(integerLiteral())` matches. The parameter
  is `const int&`, so a temporary is materialised for every literal -- that is the "elided
  implicit conversion" the earlier attempts were chasing, and it is one matcher away, not a wall.
- Without `isExpansionInMainFile()` the query also matches the ~thousands of `chrono` constructions
  inside `<bits/chrono.h>`, and a reader who greps the head of that output sees only header noise.
  That is where "0 on a line that plainly reads `milliseconds(150)`" came from.

So the feature is not what blocks the AST check. The front end is. `clang++ -std=gnu++23
-fsyntax-only` rejects every translation unit that includes `ui.h`, and `clang-query` builds its
AST with that same front end, so it gets nothing to match on:

```
./ui.h:310:63: error: member access into incomplete type 'Node'
  310 | void attach(Node* n) const { if (d_) { d_->addReader(n); n->addCell(d_); } ...
```

`Binding<T>` is a class template, but `Node*` does not depend on `T`, so clang must have `Node`
complete where the body is parsed. GCC defers the whole template body and accepts it; clang does
not. 43 of 47 `.cpp` files under `kits/ui` are rejected for this one line (`au_draw.cpp` and the
three demos/tests that do not include `ui.h` are the four that parse). The files that matter --
`ui.cpp`, `filebrowse.h`, every `components/*.cpp` -- are all in the rejected set.

## Decision

The duration rule stays a written-form check: `kits/ui/tools/duration_check.py`, wired as the
`duration_rule` ctest target. A chrono unit constructed from a numeric literal in source is the
bug, and it is unambiguous in the text; the check strips comments and string literals first
(`task_capture_check.strip`) so a duration named in prose does not fire. `sleep_for`/`sleep_until`
lines are exempt, because a sleep is elapsed time, not interface motion.

This is not an approximation of the AST check that we will later replace. It is the only check
that runs at all: it needs no front end, so it covers all 63 files in `kits/ui`, including the 43
clang cannot parse. `kits/ui/tools/duration_ast_readiness.py` states the same thing mechanically:
it proves the matcher still fires on a known violation, then reports how many TUs clang accepts,
and exits 1 while any are rejected. It is a probe, deliberately not registered as a test -- wiring
it in today would fail CI on a fact about `ui.h`, not about durations.

What the AST form would buy once it can run, beyond what the regex does: the regex can only see a
literal. A matcher on a *named unit* with an argument that is not a reference into the motion table
would also catch `std::chrono::milliseconds(kSettleMs)` behind a `constexpr`. That is a stronger
rule than P5 asked for, and it should not be adopted until something can evaluate it.

## Open questions

- Who fixes `ui.h:310`? It is a three-line change -- declare `void attach(Node* n) const;` and
  define it after `class Node` -- and it unblocks clang tooling for the whole kit, not just this
  rule. P5's owner, or the `Binding` owner.
- Should `duration_ast_readiness.py` become a hard test the moment it returns 0? Yes, in the same
  commit that makes it return 0, otherwise it rots.

## Consequences

- The rule has one hole the AST form would not: a duration built from a `constexpr` name passes.
  Accepted, and recorded here rather than left for a reader to discover.
- The rule is textually contained: it cannot see through a macro that expands to a literal, and it
  fires on a literal duration in a file that is not part of the interface (a test's own timing).
  Both are visible in review, which is where the rule is enforced.
- Nothing in the build gained a clang dependency. `duration_check.py` needs `python3` only, which
  the `task_capture` test already requires.
- Switching to the AST form needs three things, in this order: (1) `ui.h` compiles under clang;
  (2) a `compile_commands.json` (`-DCMAKE_EXPORT_COMPILE_COMMANDS=ON`) or the flags from
  `kits/build/ui/CMakeFiles/aurora_ui_tk.dir/flags.make`, which `duration_ast_readiness.py` already
  reads; (3) `clang-query` installed on the build host -- an executable dependency, not a library
  one, which is why a clang-tidy plugin was not chosen: it would also need the source tree to be
  clang-clean, and that is item (1).

## Revision, same day: the AST check exists after all

Three of the findings above were wrong, and the record is corrected rather than left standing:

1. **`ui.h` was the blocker, and it was fixable.** `Binding<T>::attach` read a `Node` member while
   `Node` was still incomplete; GCC defers that, clang rejects it outright, so 43 of 47 TUs did not
   parse. The definition now lives after `Node`, the pattern `StateBase::attach` already used.
   `clang++ -fsyntax-only` on `ui.cpp` and on a component is clean.
2. **`isExpansionInMainFile()` is false for the call.** The chrono constructor is instantiated from
   `<chrono>`, so its expansion location is the header, not the call site. Adding that guard makes
   the matcher match *nothing* — the failure mode that looks like success.
3. **`matchesName` is unanchored on a qualified name.** The type is `chrono::milliseconds`, so
   `^(nanoseconds|...)$` never matched; the bare alternation does.

`kits/ui/tools/duration_ast.py` is therefore a real AST check: it matches a chrono unit constructed
with an integer literal, keeps only matches whose `root` location is inside the project (which is
what replaces the broken main-file guard), exempts `sleep_for`/`sleep_until`, and fails loudly if
the matcher itself does not parse — a check that matches nothing must not report green. It covers 47
files in ~21 s and is registered as `duration_ast`.

Both checks stay. The written-form one needs no front end and catches a literal anywhere, including
files clang cannot parse; the AST one can see a named unit (`milliseconds(kSettleMs)` behind a
`constexpr`), which is the stronger rule.

`duration_ast_readiness.py` stays as the probe for the day the front end regresses.
