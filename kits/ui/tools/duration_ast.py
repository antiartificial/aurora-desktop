#!/usr/bin/env python3
"""The duration rule, as a real AST check.

History, because the obvious matcher is wrong twice over:

- `hasAnyArgument(integerLiteral())` returns 0 on `milliseconds(150)`: the argument is
  MaterializeTemporaryExpr -> ImplicitCastExpr<NoOp> -> IntegerLiteral, and clang 23 has no
  `ignoringTemporaryExpr` matcher to cross it. `hasDescendant(integerLiteral())` does.
- `isExpansionInMainFile()` returns 0 for the call itself: the chrono constructor is instantiated
  from <chrono>, so its expansion location is the header. Adding that guard silently makes the
  check match nothing, which is worse than not having it.

So: match everywhere, then keep only the matches whose `root` location is inside the project roots.
That is an AST matcher doing the work, with the scope filter done on source locations.

Usage: duration_ast.py ROOT [ROOT...]
Exit 1 if a literal duration appears in project source.
"""
import re
import subprocess
import sys
from pathlib import Path

MATCHER = (
    "cxxConstructExpr(hasType(hasDeclaration(namedDecl(matchesName("
    '"nanoseconds|microseconds|milliseconds|seconds|minutes|hours")))), '
    "hasAnyArgument(hasDescendant(integerLiteral())))"
)
LOC = re.compile(r"^(\S+?):(\d+):(\d+): note:")
FLAGS = ["-std=c++23"]


def project_flags(root: Path) -> list[str]:
    """The include paths a TU in this tree needs. Derived from the layout, not hardcoded per file."""
    kits = root
    return ["-I" + str(kits / "ui"), "-I" + str(kits / "ui" / "components"),
            "-I" + str(kits / "render"), "-I" + str(kits.parent / "include")]


def check(root: Path, files: list[Path]) -> list[str]:
    bad = []
    for f in files:
        try:
            r = subprocess.run(
                ["clang-query", "-c", "match " + MATCHER, str(f), "--"] + FLAGS + project_flags(root),
                capture_output=True, text=True, timeout=120)
        except (subprocess.TimeoutExpired, FileNotFoundError) as e:
            bad.append(f"{f}: could not run clang-query: {e}")
            continue
        if "Matcher not found" in r.stderr or "Error parsing" in r.stderr:
            bad.append(f"{f}: the matcher did not parse: {r.stderr.strip().splitlines()[0]}")
            continue
        out = r.stdout
        for line in out.splitlines():
            m = LOC.match(line)
            if not m:
                continue
            loc = Path(m.group(1))
            try:
                loc.resolve().relative_to(root.resolve())
            except ValueError:
                continue          # a header's own construction, not ours
            # A sleep is time, not a design duration: motion comes from the table, but waiting for
            # a thread is a duration by nature. Same exclusion the written-form check makes.
            try:
                text = loc.read_text(encoding="utf-8", errors="replace").splitlines()[int(m.group(2)) - 1]
            except (OSError, IndexError):
                text = ""
            if "sleep_for" in text or "sleep_until" in text:
                continue
            bad.append(f"{m.group(1)}:{m.group(2)}: literal duration at a call site")
    return bad


def main(argv: list[str]) -> int:
    root = Path(argv[1]) if len(argv) > 1 else Path(".")
    files = sorted(p for p in root.rglob("*.cpp") if "build" not in p.parts)
    if not files:
        print("duration_ast: no sources under", root)
        return 2
    problems = check(root, files)
    for p in problems:
        print(p)
    if problems:
        print(f"duration_ast: {len(problems)} violation(s)")
        return 1
    print(f"duration_ast: ok ({len(files)} files, AST-checked)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
