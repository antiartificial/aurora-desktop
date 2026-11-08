// 8.2b: set() from a worker is thread-safe. The value (v_) is the loop thread's to write; an
// off-loop set() posts its apply to the window's deferred queue (thread-safe) instead of touching
// v_ or the damage list. This test runs workers calling set() while the loop thread calls get();
// under ThreadSanitizer it must report NO data race (before the fix this was a race on both v_ and
// the damage vector).
#include "ui.h"

#include <atomic>
#include <cstdio>
#include <cstdlib>
#include <thread>
#include <vector>

using namespace aurora::ui;

int main() {
    // Hermetic: no compositor. The window still exists and tracks its loop thread.
    setenv("WAYLAND_DISPLAY", "aurora-tsan-headless", 1);

    Window win("tsan", 200, 120);   // loop_tid_ = this thread
    State<int> cell(0);
    cell.bind(&win);

    std::atomic<bool> stop{false};
    std::vector<std::thread> workers;
    for (int t = 0; t < 4; ++t)
        workers.emplace_back([&cell, &stop, t] {
            for (int i = 0; i < 20000 && !stop.load(std::memory_order_relaxed); ++i)
                cell.set(t * 1000000 + i);   // off-loop: must defer, not write v_
        });

    // The loop thread reads the cell throughout. v_ is never written off-loop, so this is race-free.
    long acc = 0;
    for (int i = 0; i < 200000; ++i) acc += cell.get();

    stop.store(true, std::memory_order_relaxed);
    for (auto& w : workers) w.join();

    // The worker sets deferred, so the loop thread still sees its original value.
    int still = cell.get();
    // A loop-thread set applies immediately.
    cell.set(9);
    int applied = cell.get();

    printf("ok: acc=%ld deferred_read=%d loop_set=%d\n", acc, still, applied);
    return (still == 0 && applied == 9) ? 0 : 1;
}