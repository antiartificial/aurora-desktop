#!/usr/bin/env python3
"""P5's duration rule, as a token check.

The design wants an AST matcher for a literal duration at a call site. clang-query locates the
chrono construction but not its argument (the value arrives through an elided/impilcit conversion,
so hasAnyArgument/hasDescendant both miss it), so this checks the written form instead: a chrono
unit constructed from a number IS the bug, and it is unambiguous in source.

A duration that comes from the motion table never looks like this.
"""
import re
import sys
from pathlib import Path

from task_capture_check import SUFFIXES, strip

UNITS = "nanoseconds|microseconds|milliseconds|seconds|minutes|hours"
LITERAL_DURATION = re.compile(
    r"(?:\bstd::chrono::)?\b(?:%s)\s*\(\s*[-+]?(?:\d+(?:\.\d*)?|\.\d+)[fFlLuU]*\s*\)" % UNITS)


def check(path: Path) -> list[str]:
    src = strip(path.read_text(encoding="utf-8", errors="replace"))
    out = []
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    for m in LITERAL_DURATION.finditer(src):
        line = src.count("\n", 0, m.start()) + 1
        text = lines[line - 1] if line - 1 < len(lines) else ""
        # A sleep is time, not a design duration: the motion rule is about how the interface moves.
        if "duration-literal-ok" in text or "sleep_for" in text or "sleep_until" in text:
            continue
        out.append(f"{path}:{line}: literal duration `{m.group(0)}` -- take it from the motion table")
    return out


def main(argv: list[str]) -> int:
    roots = [Path(a) for a in argv[1:]] or [Path(".")]
    files = []
    for r in roots:
        files.extend([r] if r.is_file() else sorted(p for p in r.rglob("*") if p.suffix in SUFFIXES))
    problems = []
    for f in files:
        problems.extend(check(f))
    for p in problems:
        print(p)
    if problems:
        print(f"duration: {len(problems)} violation(s)")
        return 1
    print(f"duration: ok ({len(files)} files)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
