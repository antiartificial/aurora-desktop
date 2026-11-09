#!/usr/bin/env python3
"""Can clang-query decide the duration rule yet? (ADR 0026)

The rule wants: "no literal duration at a call site". The matcher that expresses it works --
`cxxConstructExpr(hasType(... name of a chrono unit ...), hasAnyArgument(ignoringImplicit(
integerLiteral())))` fires on every `std::chrono::milliseconds(150)` in a self-contained TU
(attempt #1 of B2). What does not work is the tree: `clang++ -fsyntax-only` rejects EVERY
translation unit that includes ui.h, so clang-query never gets an AST to match in the files the
rule is about. `hasAnyArgument(integerLiteral())` returning 0 is a symptom of the same thing --
the argument is wrapped (MaterializeTemporaryExpr -> ImplicitCastExpr <NoOp> -> IntegerLiteral),
which `ignoringImplicit` unwraps, but a rejected TU has no nodes at all.

So this script answers one question, and does not pretend to be the rule:
  1. does the matcher still fire on a known violation?           (matcher probe, must pass)
  2. does clang accept the translation units in kits/ui?          (front-end probe)

Exit 0  -- every TU parses: the AST check can be switched on; replace duration_check.py.
Exit 1  -- clang rejects at least one TU: keep the written-form check (tools/duration_check.py).
"""
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

MATCHER = (
    'match cxxConstructExpr('
    'hasType(hasDeclaration(namedDecl(matchesName('
    '"(nanoseconds|microseconds|milliseconds|seconds|minutes|hours)")))), '
    'isExpansionInMainFile(), '
    'hasAnyArgument(ignoringImplicit(integerLiteral())))'
)

PROBE_TU = """#include <chrono>
void probe() { auto d = std::chrono::milliseconds(150); (void)d; }
"""

FALLBACK_FLAGS = [
    "-std=gnu++23", "-DAURORA_HAVE_WAYLAND=1",
    "-isystem", "/usr/include/freetype2", "-isystem", "/usr/include/harfbuzz",
    "-isystem", "/usr/include/glib-2.0", "-isystem", "/usr/lib64/glib-2.0/include",
    "-isystem", "/usr/include/wayland", "-isystem", "/usr/include/libxkbcommon",
]


def flags(root: Path) -> list[str]:
    """The flags CMake actually compiles aurora_ui_tk with, if the kits build tree is there."""
    for mk in root.parent.glob("build/ui/CMakeFiles/aurora_ui_tk.dir/flags.make"):
        out = []
        for line in mk.read_text().splitlines():
            if line.startswith(("CXX_DEFINES =", "CXX_INCLUDES =", "CXX_FLAGS =")):
                out += line.split("=", 1)[1].split()
        if out:
            return out
    return [f"-I{root}", f"-I{root.parent / 'render'}", *FALLBACK_FLAGS]


def clang_query(path: Path, extra: list[str]) -> tuple[int, str]:
    q = path.with_suffix(".rq")
    q.write_text(MATCHER + "\n")
    try:
        p = subprocess.run(
            ["clang-query", "--extra-arg-before=-x", "--extra-arg-before=c++",
             *[f"--extra-arg={a}" for a in extra], str(path), "-f", str(q)],
            capture_output=True, text=True)
    finally:
        q.unlink()
    return p.returncode, p.stdout + p.stderr


def matcher_probe(tmp: Path, extra: list[str]) -> bool:
    if not shutil.which("clang-query"):
        print("clang-query: not installed")
        return False
    tu = tmp / "duration_probe.cpp"
    tu.write_text(PROBE_TU)
    code, out = clang_query(tu, extra)
    n = out.count("Match #")
    print(f"matcher probe: {'fires' if n else 'does NOT fire'} on `milliseconds(150)` ({n} match)")
    return n == 1 and code == 0


def front_end(root: Path, extra: list[str]) -> list[tuple[Path, str]]:
    bad = []
    tus = sorted(p for p in root.rglob("*.cpp"))
    for tu in tus:
        p = subprocess.run(["clang++", "-fsyntax-only", *extra, str(tu)],
                           capture_output=True, text=True)
        if p.returncode != 0:
            first = next((l for l in p.stderr.splitlines() if "error:" in l), "no diagnostic")
            bad.append((tu, first.strip()))
    print(f"front end: {len(tus) - len(bad)}/{len(tus)} translation units parse under clang")
    for tu, why in bad[:3]:
        print(f"  blocked: {tu.relative_to(root)}: {why}")
    if len(bad) > 3:
        print(f"  ... and {len(bad) - 3} more")
    return bad


def main(argv: list[str]) -> int:
    root = Path(argv[1]).resolve() if len(argv) > 1 else Path(__file__).resolve().parent.parent
    extra = flags(root)
    with tempfile.TemporaryDirectory() as d:
        if not matcher_probe(Path(d), extra):
            print("duration-ast: matcher is broken -- fix tools/duration_ast_readiness.py")
            return 1
    if front_end(root, extra):
        print("duration-ast: NOT ready -- clang cannot see these TUs, so the rule stays a "
              "written-form check (tools/duration_check.py). See "
              "contracts/decisions/0026-proof-of-thread-and-durations.md")
        return 1
    print("duration-ast: ready -- clang parses the tree; switch the rule to the matcher above")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
