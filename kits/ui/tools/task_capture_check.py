#!/usr/bin/env python3
"""P5's Task-capture rule, checkable without clang.

The design asks for an AST matcher that rejects a Task closure holding something it must not. No
clang is installed here, so this enforces the mechanical core of it: a Task closure must NAME what
it captures. A default capture ([&] or [=]) is how a closure silently keeps a reference to a local
that dies before the worker runs -- the bug class the matcher exists for -- and naming a capture is
a one-line fix that makes the mistake visible in review.

Not an AST matcher: it tokenizes (comments and strings stripped), finds Task( calls, brace-matches
each, and looks at the lambda capture lists inside. Narrow and honest beats a regex that lies.
"""
import re
import sys
from pathlib import Path

SUFFIXES = {".cpp", ".cc", ".h", ".hpp"}


def strip(src: str) -> str:
    """Blank out comments and string/char literals, keeping offsets and newlines intact."""
    out = list(src)
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c == "/" and i + 1 < n and src[i + 1] == "/":
            while i < n and src[i] != "\n":
                out[i] = " "
                i += 1
        elif c == "/" and i + 1 < n and src[i + 1] == "*":
            while i < n and not (src[i] == "*" and i + 1 < n and src[i + 1] == "/"):
                if src[i] != "\n":
                    out[i] = " "
                i += 1
            for j in (i, i + 1):
                if j < n:
                    out[j] = " "
            i += 2
        elif c == "R" and i + 1 < n and src[i + 1] == '"':
            j = src.find('(', i)
            if j < 0:
                i += 1
                continue
            k = src.find(")" + src[i + 2:j] + '"', j)
            end = (k + len(src[i + 2:j]) + 2) if k >= 0 else n
            for p in range(i, min(end, n)):
                if out[p] != "\n":
                    out[p] = " "
            i = end
        elif c in ('"', "'"):
            q = c
            i += 1
            while i < n and src[i] != q:
                if src[i] == "\\":
                    i += 1
                if i < n and out[i] != "\n":
                    out[i] = " "
                i += 1
            i += 1
        else:
            i += 1
    return "".join(out)


def match_paren(s: str, start: int, open_c: str, close_c: str) -> int:
    """Index of the matching close for the open at `start` (already s[start] == open_c)."""
    depth = 0
    for i in range(start, len(s)):
        if s[i] == open_c:
            depth += 1
        elif s[i] == close_c:
            depth -= 1
            if depth == 0:
                return i
    return -1


def split_top(inner: str) -> list[str]:
    parts, depth, cur = [], 0, []
    for ch in inner:
        if ch in "([{<":
            depth += 1
        elif ch in ")]}>":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append("".join(cur))
            cur = []
        else:
            cur.append(ch)
    parts.append("".join(cur))
    return parts


def check(path: Path) -> list[str]:
    src = strip(path.read_text(encoding="utf-8", errors="replace"))
    bad = []
    for m in re.finditer(r"\bTask\s*\(", src):
        open_i = src.index("(", m.start())
        close_i = match_paren(src, open_i, "(", ")")
        if close_i < 0:
            continue
        call = src[open_i + 1:close_i]
        for lm in re.finditer(r"\[", call):
            close_b = match_paren(call, lm.start(), "[", "]")
            if close_b < 0:
                continue
            caps = call[lm.start() + 1:close_b].strip()
            for part in split_top(caps):
                if part.strip() in ("&", "="):
                    line = src.count("\n", 0, open_i + 1 + lm.start()) + 1
                    bad.append(f"{path}:{line}: Task closure uses a default capture "
                               f"[{caps}] -- name what it captures")
                    break
    return bad


def main(argv: list[str]) -> int:
    roots = [Path(a) for a in argv[1:]] or [Path(".")]
    files = []
    for r in roots:
        files.extend([r] if r.is_file() else sorted(
            p for p in r.rglob("*") if p.suffix in SUFFIXES))
    problems = []
    for f in files:
        problems.extend(check(f))
    for p in problems:
        print(p)
    if problems:
        print(f"task-capture: {len(problems)} violation(s)")
        return 1
    print(f"task-capture: ok ({len(files)} files)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
