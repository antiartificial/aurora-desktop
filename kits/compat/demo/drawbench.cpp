// A measurement, not a demo: how much drawing throughput does the app_server get from having one
// thread per window?
//
// BeOS's reputation rests on a thread per window. Aurora has them on both sides -- BLooper::Run
// spawns one per looper, and appserver/server.cpp spawns one per window. But WindowThread takes a
// single process-wide `gMu` around the whole of WindowDispatch, so the question is whether those
// server threads can actually draw at the same time or only take turns.
//
// Each window gets its own driver thread that invalidates and then Syncs (a real round trip to the
// server), so N windows push N streams of drawing concurrently. Draw() is deliberately dominated by
// DrawString, because glyph rasterisation is the expensive thing that happens server-side, inside
// the lock.
//
//   BENCH_WINDOWS  how many windows draw at once (default 2)
//   BENCH_FRAMES   repaints per window (default 40)
//   BENCH_STRINGS  DrawString calls per repaint (default 120)
//
// It prints one BENCH line: the wall time of the parallel phase and the total repaints, so a run
// with N windows can be compared against N times a one-window run. Perfect scaling keeps the wall
// time flat as N grows; perfect serialisation makes it grow linearly.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Font.h>
#include <stdio.h>
#include <stdlib.h>
#include <atomic>
#include <chrono>
#include <string>
#include <thread>
#include <vector>

static int envi(const char* k, int d) { const char* s = getenv(k); return s && *s ? atoi(s) : d; }

static int gStrings = 120;

class BenchView : public BView {
public:
	BenchView(BRect f) : BView(f, "bench", B_FOLLOW_ALL, B_WILL_DRAW) { SetViewColor(250, 250, 250); }
	void Draw(BRect) override
	{
		BFont font(be_plain_font);
		font.SetSize(12);
		SetFont(&font);
		SetHighColor(20, 20, 20);
		// Vary the text per frame so nothing downstream can cache the whole repaint away.
		for (int i = 0; i < gStrings; i++) {
			char buf[48];
			snprintf(buf, sizeof buf, "row %03d frame %04d glyphs", i, fFrame);
			DrawString(buf, BPoint(8.0f, 14.0f + (float)(i % 28) * 13.0f));
		}
		fFrame++;
	}
	int fFrame = 0;
};

class BenchWindow : public BWindow {
public:
	BenchWindow(int n) : BWindow(BRect(20 + n * 12, 20 + n * 12, 20 + n * 12 + 399, 20 + n * 12 + 399),
		("bench" + std::to_string(n)).c_str(), B_TITLED_WINDOW, 0)
	{
		fView = new BenchView(Bounds());
		AddChild(fView);
	}
	BenchView* fView;
};

int main()
{
	const int nwin = envi("BENCH_WINDOWS", 2), frames = envi("BENCH_FRAMES", 40);
	gStrings = envi("BENCH_STRINGS", 120);

	BApplication app("application/x-vnd.aurora-drawbench");
	std::vector<BenchWindow*> wins;
	for (int i = 0; i < nwin; i++) {
		BenchWindow* w = new BenchWindow(i);
		w->Show();
		wins.push_back(w);
	}
	// Let every window finish its first paint, so startup is not inside the measurement.
	for (auto* w : wins) w->Sync();

	std::atomic<long> done{0};
	auto t0 = std::chrono::steady_clock::now();
	std::vector<std::thread> drivers;
	for (auto* w : wins) {
		drivers.emplace_back([w, frames, &done] {
			for (int k = 0; k < frames; k++) {
				if (w->LockLooper()) { w->fView->Invalidate(); w->UnlockLooper(); }
				w->Sync();	// round trip: returns once the server has processed this window's queue
				done.fetch_add(1, std::memory_order_relaxed);
			}
		});
	}
	for (auto& t : drivers) t.join();
	double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();

	long total = done.load();
	printf("BENCH windows=%d frames=%d strings=%d repaints=%ld wall_ms=%.1f ms_per_repaint=%.3f\n",
		nwin, frames, gStrings, total, ms, ms / (double)total);
	fflush(stdout);
	return 0;	// no app.Run(): the drivers did the work and nothing waits on the looper
}
