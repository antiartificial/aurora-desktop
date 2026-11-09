#!/usr/bin/env python3
"""Rewrite the free Desc modifiers into method calls: named(d, "x") -> (d).name("x").

Nested calls mean a regex cannot do this: weight(named(Label("a"), "x"), 2) has to become
((Label("a")).name("x")).weight(2) inside-out. This walks balanced parens on a comment/string-
stripped copy (same offsets as the original), splits top-level arguments, and rewrites the
inner-most calls first by iterating until nothing matches.

Skips definitions and declarations (preceded by a return type) and existing member calls
(preceded by . or ->). Prints nothing it cannot rewrite.
"""
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from task_capture_check import SUFFIXES, strip  # noqa: E402

MAP = {"named": "name", "weight": "weight", "styled": "style", "padding": "padding"}
CALL = re.compile(r"\b(named|weight|styled|padding)\s*\(")


def match_paren(s: str, start: int) -> int:
    depth = 0
    for i in range(start, len(s)):
        if s[i] == "(":
            depth += 1
        elif s[i] == ")":
            depth -= 1
            if depth == 0:
                return i
    return -1


def split_args(inner: str) -> list[tuple[int, int]]:
    """Offsets of each top-level argument inside `inner`, respecting (), {}, [] and <>."""
    spans, depth, start = [], 0, 0
    for i, ch in enumerate(inner):
        if ch in "([{<":
            depth += 1
        elif ch in ")]}>":
            depth -= 1
        elif ch == "," and depth == 0:
            spans.append((start, i))
            start = i + 1
    spans.append((start, len(inner)))
    return spans


def is_definition(text: str, at: int) -> bool:
    """`Desc padding(` / `Desc styled(` is a definition or declaration, not a call."""
    before = text[:at].rstrip()
    return before.endswith("Desc") or before.endswith("void") or before.endswith("std::string")


def rewrite(src: str) -> tuple[str, int]:
    s = strip(src)
    out, applied = src, 0
    while True:
        progress = False
        for m in CALL.finditer(strip(out)):
            name = m.group(1)
            open_i = m.end() - 1
            if is_definition(strip(out), m.start()):
                continue
            before = strip(out)[:m.start()].rstrip()
            if before.endswith(".") or before.endswith("->"):
                continue
            close_i = match_paren(strip(out), open_i)
            if close_i < 0:
                continue
            inner_start = open_i + 1
            spans = split_args(strip(out)[inner_start:close_i])
            if len(spans) != 2:
                continue
            a = out[inner_start + spans[0][0]:inner_start + spans[0][1]].strip()
            b = out[inner_start + spans[1][0]:inner_start + spans[1][1]].strip()
            if not a:
                continue
            out = out[:m.start()] + f"({a}).{MAP[name]}({b})" + out[close_i + 1:]
            applied += 1
            progress = True
            break   # indices moved; rescan
        if not progress:
            return out, applied


def main(argv: list[str]) -> int:
    roots = [Path(a) for a in argv[1:]] or [Path(".")]
    files = []
    for r in roots:
        files.extend([r] if r.is_file() else sorted(p for p in r.rglob("*") if p.suffix in SUFFIXES))
    total = 0
    for f in files:
        src = f.read_text(encoding="utf-8")
        new, n = rewrite(src)
        if n:
            f.write_text(new, encoding="utf-8")
            print(f"{f}: {n}")
            total += n
    print(f"rewrote {total} call(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
