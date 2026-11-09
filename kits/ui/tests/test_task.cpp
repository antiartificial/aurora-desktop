// 8.2c: Task<F,G> lifecycle. With no compositor there is no loop to drain, so this pins the two
// properties that do not need one: the worker is joined at teardown (ASan would catch a post into a
// destroyed queue), and `done` does NOT run once the window has closed (the lifetime token).
#include "ui.h"

#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <thread>

using namespace aurora::ui;

int main() {
    setenv("WAYLAND_DISPLAY", "aurora-task-headless", 1);

    std::atomic<int> got{-1};
    {
        Window w("task", 200, 120);
        // The worker outlives this scope by design; ~Window must join it and the token must stop
        // done() from running on a closed window.
        Task(w, [] { std::this_thread::sleep_for(std::chrono::milliseconds(50)); return 42; },
             [&got](int v) { got.store(v); });
    }   // ~Window: lifetime_ = false, then join

    std::printf("task: after close got=%d (want -1, done must not run)\n", got.load());
    const bool ok = got.load() == -1;
    std::printf(ok ? "task: ok\n" : "task: FAILED\n");
    return ok ? 0 : 1;
}
