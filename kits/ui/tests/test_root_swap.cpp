// ADR 0009 / docs 00 §8.0: a tree swap frees the old nodes, and every cell that read one still
// held a raw Node*. Before the symmetric-detach fix this aborts inside State::set with a
// heap-use-after-free under ASan. It builds a node from a live cell, swaps the root twice, writes
// the cell, and must exit clean.
//
// State<T>::set is a header template, so it instantiates HERE and its read of a freed node's
// `frame` is instrumented by ASan even though the node itself is freed in the (uninstrumented)
// library -- allocation and free are intercepted globally by the ASan runtime.
#include "ui.h"

#include <cstdio>
#include <cstdlib>
#include <string>

static int checks = 0, fails = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)

using namespace aurora::ui;

int main() {
    // Hermetic: point WAYLAND_DISPLAY at a socket that cannot exist so au_wl_connect() returns
    // null and the Window never opens a real surface. SetRoot and Invalidate still run -- the
    // freed-Node* path is walked there regardless of whether a compositor is present.
    setenv("WAYLAND_DISPLAY", "aurora-root-swap-headless", 1);

    // (1) The mechanism in isolation: a node built from a cell records the cell, and destroying
    // the node detaches it. No window needed.
    {
        State<std::string> cell("a");
        CHECK(cell.bound_count() == 0, "a fresh cell has no bound nodes, got %zu", cell.bound_count());
        {
            Desc node = Label(&cell);
            CHECK(cell.bound_count() == 1, "a Label built from a cell attaches it, got %zu",
                  cell.bound_count());
        }
        CHECK(cell.bound_count() == 0, "destroying the node detaches it, got %zu", cell.bound_count());
    }

    // (2) The use-after-free SetRoot used to cause. Headless: au_wl_connect returns null, so the
    // window never maps, but SetRoot and Invalidate still run -- and that is the path a freed
    // Node* is walked on. With the fix the old tree detaches as it is freed, so set() finds no
    // nodes and touches only the window. Without it, this aborts here under ASan.
    {
        Window win("root-swap", 200, 120);
        State<std::string> cell("first");
        cell.bind(&win);

        win.SetRoot(Label(&cell));                     // the node attaches to cell in its constructor
        CHECK(cell.bound_count() == 1, "the first root bound the cell, got %zu", cell.bound_count());

        win.SetRoot(Label(std::string("second")));     // dropping the first tree frees its node
        CHECK(cell.bound_count() == 0, "swapping the root detached the freed node, got %zu",
              cell.bound_count());

        cell.set(std::string("third"));                // walks nodes_: empty now, no freed read
        win.SetRoot(Spacer());
        cell.set(std::string("fourth"));
    }

    // (3) Reverse order: a State destroyed before the node that bound it. Build a node from a
    // heap cell, delete the cell (it must forget itself in the node), then destroy the node --
    // ~Node must find no dangling cell to call.
    {
        auto* cell = new State<std::string>("x");
        Desc node = Label(cell);
        CHECK(cell->bound_count() == 1, "the node bound the heap cell, got %zu", cell->bound_count());
        delete cell;      // ~State removes itself from node->cells_
        node.reset();     // ~Node: cells_ is empty, nothing dangling
    }

    printf("%s: %d checks, %d failed\n", fails ? "FAIL" : "ok", checks, fails);
    return fails ? 1 : 0;
}