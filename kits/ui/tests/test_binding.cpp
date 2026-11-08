// 8.2 foundation: Binding<T> is the one value type a widget takes. Proves it converts from a live
// State<T>& and from a literal, reads and writes through, and — under ASan — that a Binding which
// outlives its cell reads its own constant instead of dereferencing freed memory (Decision 1).
#include "ui.h"

#include <cstdio>
#include <cstdlib>
#include <string>

static int checks = 0, fails = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)

using namespace aurora::ui;

int main() {
    // Live: implicit from State<T>&, reads and writes through.
    {
        State<int> cell(3);
        Binding<int> b = cell;                 // one user-defined conversion from State<int>&
        CHECK(b.get() == 3, "a live binding reads the cell, got %d", b.get());
        CHECK(b.writable(), "a binding from a live cell is writable");
        b.set(7);
        CHECK(cell.get() == 7, "the binding writes through to the cell, got %d", cell.get());
        cell.set(9);
        CHECK(b.get() == 9, "the binding reads the cell's current value, got %d", b.get());
    }

    // Constant: implicit from a non-string literal, read-only, writes its own copy.
    {
        Binding<int> b = 42;
        CHECK(b.get() == 42, "a constant binding reads its literal, got %d", b.get());
        CHECK(!b.writable(), "a constant binding is not writable");
        b.set(5);
        CHECK(b.get() == 5, "a constant binding writes its own copy, got %d", b.get());
    }

    // String: a const char* literal does NOT reach a Binding implicitly (two UDCs), so it is
    // constructed directly -- which is the intended shape (static text keeps Label(std::string)).
    {
        Binding<std::string> b(std::string("static"));
        CHECK(b.get() == "static", "a directly-built constant binding reads its literal");
    }

    // Lifetime: a binding that outlives its cell must not dereference freed memory. ~State flips
    // the shared liveness flag, so get() falls back to the constant. Under ASan, a missing flag
    // would be a heap-use-after-free here.
    {
        auto* cell = new State<std::string>(std::string("live"));
        Binding<std::string> b = *cell;
        CHECK(b.alive(), "a binding over a live cell is alive");
        CHECK(b.get() == "live", "it reads the live value");
        delete cell;                            // ~State: *alive_ = false
        CHECK(!b.alive(), "after the cell dies the binding is not alive");
        CHECK(b.get() == std::string(), "it returns its own constant, not a freed read");
    }

    printf("%s: %d checks, %d failed\n", fails ? "FAIL" : "ok", checks, fails);
    return fails ? 1 : 0;
}