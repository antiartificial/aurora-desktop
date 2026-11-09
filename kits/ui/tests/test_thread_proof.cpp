// 8.6/P5: a get() off the loop thread is counted when the audit is on. The compile-time form (a
// token every read must hold) is a change to every read site; this is the check that exists.
#include "ui.h"

#include <cstdio>
#include <cstdlib>
#include <thread>

using namespace aurora::ui;

int main() {
    setenv("WAYLAND_DISPLAY", "aurora-audit-headless", 1);

    Window w("audit", 200, 120);
    State<int> s(7);
    s.bind(&w);
    g_thread_audit = true;

    const int n0 = w.off_loop_reads();
    (void)s.get();
    const int n1 = w.off_loop_reads();

    std::thread t([&] { (void)s.get(); (void)s.get(); });
    t.join();
    const int n2 = w.off_loop_reads();

    std::printf("thread-proof: on-loop +%d, off-loop +%d\n", n1 - n0, n2 - n1);
    const bool ok = (n1 - n0) == 0 && (n2 - n1) == 2;
    std::printf(ok ? "thread-proof: ok\n" : "thread-proof: FAILED\n");
    return ok ? 0 : 1;
}
