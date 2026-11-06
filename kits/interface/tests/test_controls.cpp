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
	// the button face is not the panel colour and not flat: it is a gradient with a frame
	int distinct = 0; uint32_t last = 0; bool first = true;
	for (int y = (int)(62 * s); y < (int)(88 * s); y++) { uint32_t v = f.argb[(size_t)y * f.width + (int)(30 * s)]; if (first || v != last) distinct++; last = v; first = false; }
	printf("button column: %d distinct colours down its height (gradient)\n", distinct);
	CHECK(distinct >= 4, "button face is flat (%d colours): gradient not drawn", distinct);

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
