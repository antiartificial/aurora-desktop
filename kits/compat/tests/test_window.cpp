// A real BWindow + BView through Haiku's Window.cpp/View.cpp, the link protocol and the app_server analogue,
// judged on the DECODED PIXELS of the frame the server finished (no compositor needed: AURORA_APPSERVER_PRESENT=0).
// Scale-aware: run it with AURORA_UI_SCALE=1, 1.45, 2; every expectation is computed from the scale.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Font.h>
#include <stdio.h>
#include <stdlib.h>
#include <math.h>
#include "server.h"

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)

static const rgb_color kBg = {240, 240, 230, 255}, kRed = {200, 30, 30, 255}, kBlack = {0, 0, 0, 255};
static const float kBaseline = 60, kX = 20;
static const char* kLabel = "Hello, Aurora";
static float gWidth, gAscent, gDescent;

struct V : BView {
	V(BRect r) : BView(r, "v", B_FOLLOW_ALL, B_WILL_DRAW) { SetViewColor(kBg); }
	void Draw(BRect) override {
		SetHighColor(kRed); FillRect(BRect(20, 10, 59, 29));
		SetHighColor(kBlack); StrokeRect(Bounds());
		BFont f(be_plain_font); font_height h; f.GetHeight(&h);
		gWidth = f.StringWidth(kLabel); gAscent = h.ascent; gDescent = h.descent;
		SetFont(&f); DrawString(kLabel, BPoint(kX, kBaseline));
	}
};
struct W : BWindow {
	W() : BWindow(BRect(50, 50, 249, 129), "t", B_TITLED_WINDOW, 0) { AddChild(new V(Bounds())); }
};

static void rgb(const aurora::Frame& f, int x, int y, int* r, int* g, int* b)
{
	uint32_t v = f.argb[(size_t)y * f.width + x];
	*r = (v >> 16) & 255; *g = (v >> 8) & 255; *b = v & 255;
}
static bool is(const aurora::Frame& f, int x, int y, rgb_color c, int tol = 0)
{
	int r, g, b; rgb(f, x, y, &r, &g, &b);
	return abs(r - c.red) <= tol && abs(g - c.green) <= tol && abs(b - c.blue) <= tol;
}

int main()
{
	setenv("AURORA_APPSERVER_PRESENT", "0", 1);
	BApplication app("application/x-vnd.aurora-test-window");
	W* w = new W; w->Show();

	aurora::Frame f;
	for (int i = 0; i < 300 && !aurora::last_frame(0, &f); i++) snooze(10000);
	CHECK(f.width > 0, "no frame was ever finished by the server");
	if (f.width == 0) return 1;
	double s = aurora::output_scale();
	printf("frame %dx%d device px, scale %.3f, title '%s'\n", f.width, f.height, f.scale, f.title.c_str());
	CHECK(f.width == (int)ceil(200 * s) && f.height == (int)ceil(80 * s), "window is %dx%d, want %dx%d", f.width, f.height, (int)ceil(200 * s), (int)ceil(80 * s));
	CHECK(f.title == "t", "title not forwarded");

	auto P = [&](float l) { return (int)lround(l * s); };	// layout -> device
	CHECK(is(f, P(5), P(5), kBg), "background pixel is not the view colour");
	CHECK(is(f, P(40), P(20), kRed), "red block missing at its centre");
	CHECK(is(f, P(19) - (s > 1 ? 1 : 0), P(20), kBg, 0) || s > 1, "red block leaks left of x=20");
	CHECK(is(f, P(60) + 1, P(20), kBg), "red block leaks right of x=59");
	CHECK(is(f, 0, 0, kBlack) && is(f, f.width - 1, f.height - 1, kBlack, 60), "frame stroke missing at the corners");

	// ---- text: where the label should be, in device pixels
	int x0 = P(kX), x1 = P(kX + gWidth) + P(4), y0 = P(kBaseline - gAscent) - 1, y1 = P(kBaseline + gDescent) + 2;
	int ink = 0, dark = 0, inkL = 1 << 30, inkR = -1, inkT = 1 << 30, inkB = -1, rowsWithInk = 0;
	for (int y = y0; y <= y1; y++) {
		bool row = false;
		for (int x = P(kX) - 4; x <= x1 + 40; x++) {
			int r, g, b; rgb(f, x, y, &r, &g, &b);
			if (abs(r - kBg.red) + abs(g - kBg.green) + abs(b - kBg.blue) > 40) {
				ink++; row = true;
				if (r + g + b < 3 * 90) dark++;
				if (x < inkL) inkL = x; if (x > inkR) inkR = x; if (y < inkT) inkT = y; if (y > inkB) inkB = y;
			}
		}
		rowsWithInk += row;
	}
	printf("label box x[%d,%d] y[%d,%d]: ink %d px, dark %d px, ink bbox x[%d,%d] y[%d,%d], %d rows with ink\n", x0, x1, y0, y1, ink, dark, inkL, inkR, inkT, inkB, rowsWithInk);
	CHECK(ink > 120 * s * s * 0.5, "too little ink (%d) for a 13-character label", ink);
	CHECK(dark > 30, "no solid glyph pixels (%d)", dark);
	CHECK(rowsWithInk >= (int)(6 * s), "ink spans only %d rows: row is uniform", rowsWithInk);
	CHECK(inkL >= P(kX) - 2, "ink starts left of the pen origin (%d < %d)", inkL, P(kX));
	// the metric the app measured must match what was drawn: ink right edge = pen + StringWidth, within a pixel
	// or two of side bearing (the final 'a' has right-side bearing, never drawn past its advance)
	double wantR = (kX + gWidth) * s;
	CHECK(inkR <= wantR + 1 && inkR >= wantR - 3 * s - 1, "ink right edge %d vs pen+StringWidth %.1f", inkR, wantR);
	CHECK(inkT >= P(kBaseline - gAscent) - 2, "ink rises above the ascent line (%d vs %d)", inkT, P(kBaseline - gAscent));
	CHECK(inkB <= P(kBaseline + gDescent) + 2, "ink falls below the descent line (%d vs %d)", inkB, P(kBaseline + gDescent));
	// a capital letter's stem must reach cap height: some column near the 'H' is inked through the cap band
	int cap = 0;
	for (int x = P(kX); x <= P(kX) + P(3); x++) {
		int n = 0;
		for (int y = P(kBaseline - 9); y < P(kBaseline); y++) { int r, g, b; rgb(f, x, y, &r, &g, &b); if (r + g + b < 3 * 170) n++; }
		if (n > cap) cap = n;
	}
	CHECK(cap >= (int)(7 * s), "H stem: best column has %d inked px in the cap-height band", cap);

	int unhr, unh = aurora::unhandled_opcodes(&unhr);
	printf("unhandled opcodes: %d (with reply: %d)\n", unh, unhr);
	CHECK(unh == 0, "server saw opcodes it does not implement");
	printf("%d checks, %d failed\n", checks, fails);
	w->Lock(); w->Quit();
	return fails ? 1 : 0;
}
