// BButton and BStringView (real Haiku classes) respond to a pointer click and redraw. The click is injected at
// the server's pointer entry point (the same internal event the Wayland wl_pointer callbacks post): there is no
// pointer injection into the compositor, so this does NOT prove wl_pointer delivery. Judged on frame pixels.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Button.h>
#include <StringView.h>
#include <stdio.h>
#include <stdlib.h>
#include <atomic>
#include "server.h"
#include "aurora_theme.h"

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)
enum { kClicked = 'clck' };
static std::atomic<int> gClicks(0);

struct W : BWindow {
	BStringView* label;
	W() : BWindow(BRect(10, 10, 259, 129), "c", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		label = new BStringView(BRect(20, 20, 229, 40), "l", "Clicks: 0"); bg->AddChild(label);
		bg->AddChild(new BButton(BRect(20, 60, 139, 90), "b", "Click me", new BMessage(kClicked)));
	}
	void MessageReceived(BMessage* m) override
	{
		if (m->what == kClicked) { gClicks++; char b[32]; snprintf(b, sizeof(b), "Clicks: %d", (int)gClicks); label->SetText(b); }
		else BWindow::MessageReceived(m);
	}
};

static uint64_t hashRegion(const aurora::Frame& f, double s, int l, int t, int r, int b)
{
	uint64_t h = 1469598103934665603ull;
	for (int y = (int)(t * s); y < (int)(b * s); y++)
		for (int x = (int)(l * s); x < (int)(r * s); x++) { h ^= f.argb[(size_t)y * f.width + x]; h *= 1099511628211ull; }
	return h;
}
static bool waitFrame(aurora::Frame* f, uint64_t* prev, double s, int l, int t, int r, int b)
{
	for (int i = 0; i < 300; i++) {
		if (aurora::last_frame(0, f) && hashRegion(*f, s, l, t, r, b) != *prev) { *prev = hashRegion(*f, s, l, t, r, b); return true; }
		snooze(10000);
	}
	return false;
}

int main()
{
	setenv("AURORA_APPSERVER_PRESENT", "0", 1);
	BApplication app("application/x-vnd.aurora-test-controls");
	W* w = new W; w->Show();
	double s = aurora::output_scale();
	aurora::Frame f; uint64_t btn = 0, lab = 0;
	CHECK(waitFrame(&f, &btn, s, 20, 60, 140, 91), "no first frame");
	lab = hashRegion(f, s, 20, 20, 230, 41);
	// The button has a frame and a face that is not the panel colour. Stock Haiku also draws the face as a gradient; Aurora's
	// look is flat on purpose (aurora_controllook.h), so "not flat" is asked only of the look that promises it.
	int distinct = 0; uint32_t last = 0; bool first = true;
	for (int y = (int)(60 * s); y < (int)(91 * s); y++) { uint32_t v = f.argb[(size_t)y * f.width + (int)(30 * s)]; if (first || v != last) distinct++; last = v; first = false; }
	rgb_color panel = ui_color(B_PANEL_BACKGROUND_COLOR);
	uint32_t face = f.argb[(size_t)(int)(75 * s) * f.width + (int)(30 * s)];
	bool aurora = aurora::UseAuroraControlLook();
	printf("button column: %d distinct colours down its height (%s look)\n", distinct, aurora ? "aurora" : "haiku");
	CHECK(distinct >= (aurora ? 3 : 4), "button face is flat or frameless (%d colours)", distinct);
	CHECK((face & 0xffffff) != (uint32_t)((panel.red << 16) | (panel.green << 8) | panel.blue), "button face is the panel colour: no button");

	uint64_t before = btn;
	aurora::inject_pointer(0, 80, 75, 1);			// press in the middle of the button
	CHECK(waitFrame(&f, &btn, s, 20, 60, 140, 91), "button did not redraw on press");
	printf("press: button region hash %016llx -> %016llx\n", (unsigned long long)before, (unsigned long long)btn);
	CHECK(gClicks == 0, "message sent before release");
	aurora::inject_pointer(0, 80, 75, 0);			// release inside: the click
	for (int i = 0; i < 300 && gClicks == 0; i++) snooze(10000);
	CHECK(gClicks == 1, "BButton did not invoke its message on click (%d)", (int)gClicks);
	uint64_t beforeLabel = lab;
	CHECK(waitFrame(&f, &lab, s, 20, 20, 230, 41), "label did not redraw after the click");
	printf("label region hash %016llx -> %016llx (\"Clicks: 0\" -> \"Clicks: 1\")\n", (unsigned long long)beforeLabel, (unsigned long long)lab);
	CHECK(lab != beforeLabel, "label pixels unchanged");

	// a press that is released OUTSIDE the button must not click
	aurora::inject_pointer(0, 80, 75, 1);
	aurora::inject_pointer(0, 200, 110, -1);
	aurora::inject_pointer(0, 200, 110, 0);
	snooze(300000);
	CHECK(gClicks == 1, "release outside the button still clicked (%d)", (int)gClicks);

	int unhr, unh = aurora::unhandled_opcodes(&unhr);
	printf("unhandled opcodes: %d (with reply: %d)\n", unh, unhr);
	CHECK(unh == 0, "server saw opcodes it does not implement");
	printf("%d checks, %d failed\n", checks, fails);
	w->Lock(); w->Quit();
	return fails ? 1 : 0;
}
