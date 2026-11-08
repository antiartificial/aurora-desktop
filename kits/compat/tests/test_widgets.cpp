// The controls added after the first tests, in a BWindow with no compositor: BCheckBox, BRadioButton, BSlider,
// BTabView, BSpinner, BStatusBar, BTextControl, BOptionPopUp, BColorControl, BBox. Pointer events are injected at the
// server's pointer entry (the same internal event the wl_pointer callbacks post); each claim is a value read back from
// the control AND, where the control draws something different, a change in the frame's pixels.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Box.h>
#include <CheckBox.h>
#include <ColorControl.h>
#include <OptionPopUp.h>
#include <RadioButton.h>
#include <Slider.h>
#include <Spinner.h>
#include <StatusBar.h>
#include <TabView.h>
#include <TextControl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <atomic>
#include "server.h"

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)
enum { kCheck = 'chk_', kRadio1 = 'rd1_', kRadio2 = 'rd2_', kSlide = 'sld_', kSpin = 'spn_', kPick = 'pck_' };
static std::atomic<int> gMsgs(0);

struct W : BWindow {
	BCheckBox* check; BRadioButton *r1, *r2; BSlider* slider; BTabView* tabs; BSpinner* spin; BStatusBar* bar;
	BTextControl* text; BOptionPopUp* pick; BColorControl* colour; BBox* box;
	W() : BWindow(BRect(10, 10, 389, 309), "w", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		bg->AddChild(check = new BCheckBox(BRect(10, 10, 150, 30), "cb", "Check me", new BMessage(kCheck)));
		bg->AddChild(r1 = new BRadioButton(BRect(10, 40, 150, 60), "r1", "One", new BMessage(kRadio1)));
		bg->AddChild(r2 = new BRadioButton(BRect(10, 65, 150, 85), "r2", "Two", new BMessage(kRadio2)));
		r1->SetValue(B_CONTROL_ON);
		bg->AddChild(slider = new BSlider(BRect(10, 100, 300, 150), "sl", "Vol", new BMessage(kSlide), 0, 100));
		slider->SetValue(10);
		bg->AddChild(tabs = new BTabView(BRect(170, 10, 370, 90), "tabs"));
		tabs->AddTab(new BView(BRect(0, 0, 100, 40), "Alpha", 0, 0));
		tabs->AddTab(new BView(BRect(0, 0, 100, 40), "Beta", 0, 0));
		bg->AddChild(spin = new BSpinner(BRect(170, 100, 350, 125), "sp", "Count", new BMessage(kSpin)));
		spin->SetRange(0, 9);
		bg->AddChild(bar = new BStatusBar(BRect(10, 160, 300, 190), "bar", "Level", "of 100"));
		bar->SetMaxValue(100);
		bg->AddChild(text = new BTextControl(BRect(10, 200, 300, 225), "tc", "Name:", "", NULL));
		bg->AddChild(pick = new BOptionPopUp(BRect(10, 235, 200, 260), "op", "Pick:", new BMessage(kPick)));
		pick->AddOption("A", 1); pick->AddOption("B", 2); pick->AddOption("C", 3);
		bg->AddChild(colour = new BColorControl(BPoint(210, 150), B_CELLS_32x8, 4, "col"));
		bg->AddChild(box = new BBox(BRect(10, 270, 150, 295), "box"));
		box->SetLabel("A box");
	}
	void MessageReceived(BMessage* m) override
	{
		switch (m->what) { case kCheck: case kRadio1: case kRadio2: case kSlide: case kSpin: case kPick: gMsgs++; break; default: BWindow::MessageReceived(m); }
	}
};

static uint64_t hashRegion(const aurora::Frame& f, double s, int l, int t, int r, int b)
{
	uint64_t h = 1469598103934665603ull;
	for (int y = (int)(t * s); y < (int)(b * s); y++)
		for (int x = (int)(l * s); x < (int)(r * s); x++) { h ^= f.argb[(size_t)y * f.width + x]; h *= 1099511628211ull; }
	return h;
}
static uint64_t region(int l, int t, int r, int b)	// of the last finished frame, after the server has had a moment to repaint
{
	snooze(250000);
	aurora::Frame f; aurora::last_frame(0, &f);
	return hashRegion(f, aurora::output_scale(), l, t, r, b);
}
static void click(float x, float y) { aurora::inject_pointer(0, x, y, 1); snooze(40000); aurora::inject_pointer(0, x, y, 0); snooze(100000); }

int main()
{
	setenv("AURORA_APPSERVER_PRESENT", "0", 1);
	BApplication app("application/x-vnd.aurora-test-widgets");
	W* w = new W; w->Show();
	snooze(500000);

	// BCheckBox
	uint64_t h0 = region(10, 10, 151, 31);
	click(20, 20);
	w->Lock(); int32 v = w->check->Value(); w->Unlock();
	CHECK(v == B_CONTROL_ON && gMsgs == 1, "BCheckBox value %d after a click, %d messages", (int)v, (int)gMsgs);
	uint64_t h1 = region(10, 10, 151, 31);
	CHECK(h1 != h0, "BCheckBox pixels did not change when it was checked");
	click(20, 20);
	w->Lock(); v = w->check->Value(); w->Unlock();
	CHECK(v == B_CONTROL_OFF && region(10, 10, 151, 31) == h0, "BCheckBox did not return to its first look when unchecked (value %d)", (int)v);

	// BRadioButton: choosing Two turns One off
	uint64_t rh = region(10, 40, 151, 86);
	click(20, 75);
	w->Lock(); int32 a = w->r1->Value(), b = w->r2->Value(); w->Unlock();
	CHECK(a == B_CONTROL_OFF && b == B_CONTROL_ON, "radio group after clicking Two: One=%d Two=%d", (int)a, (int)b);
	CHECK(region(10, 40, 151, 86) != rh, "radio pixels unchanged");

	// BSlider: press on the thumb, drag 60 px right, release
	w->Lock(); BRect th = w->slider->ThumbFrame(); BRect sf = w->slider->Frame(); int32 before = w->slider->Value(); w->Unlock();
	float tx = sf.left + (th.left + th.right) / 2, ty = sf.top + (th.top + th.bottom) / 2;
	uint64_t sh = region(10, 100, 301, 151);
	aurora::inject_pointer(0, tx, ty, 1); snooze(60000);
	for (int i = 1; i <= 6; i++) { aurora::inject_pointer(0, tx + i * 10, ty, -1); snooze(40000); }
	aurora::inject_pointer(0, tx + 60, ty, 0); snooze(150000);
	w->Lock(); int32 after = w->slider->Value(); w->Unlock();
	CHECK(after >= before + 15, "BSlider value %d -> %d after dragging the thumb 60 px of a 290 px track", (int)before, (int)after);
	CHECK(region(10, 100, 301, 151) != sh, "slider pixels unchanged");

	// BTabView: click the second tab
	w->Lock(); BRect t1 = w->tabs->TabFrame(1); BRect tf = w->tabs->Frame(); w->Unlock();
	uint64_t tabh = region(170, 10, 371, 91);
	click(tf.left + (t1.left + t1.right) / 2, tf.top + (t1.top + t1.bottom) / 2);
	w->Lock(); int32 sel = w->tabs->Selection(); w->Unlock();
	CHECK(sel == 1, "BTabView selection %d after clicking tab 1", (int)sel);
	CHECK(region(170, 10, 371, 91) != tabh, "tab pixels unchanged");

	// BSpinner, BStatusBar, BTextControl, BOptionPopUp, BColorControl: state set from code reads back, and draws
	w->Lock(); w->spin->Increment(); w->spin->Increment(); int32 sp = w->spin->Value(); w->Unlock();
	CHECK(sp == 2, "BSpinner value %d after two Increment() calls", (int)sp);
	uint64_t bh = region(10, 160, 301, 191);
	w->Lock(); w->bar->Update(40); float cur = w->bar->CurrentValue(); w->Unlock();
	CHECK(cur > 39.9f && cur < 40.1f, "BStatusBar current value %.1f after Update(40)", cur);
	CHECK(region(10, 160, 301, 191) != bh, "status bar pixels unchanged");
	uint64_t xh = region(10, 200, 301, 226);
	w->Lock(); w->text->SetText("hello"); bool same = strcmp(w->text->Text(), "hello") == 0; w->Unlock();
	CHECK(same, "BTextControl text");
	CHECK(region(10, 200, 301, 226) != xh, "text control pixels unchanged");
	w->Lock(); w->pick->SetValue(3); int32 pv = w->pick->Value(); w->Unlock();
	CHECK(pv == 3, "BOptionPopUp value %d", (int)pv);
	w->Lock(); w->colour->SetValue((rgb_color){10, 20, 30, 255}); rgb_color c = w->colour->ValueAsColor(); w->Unlock();
	CHECK(c.red == 10 && c.green == 20 && c.blue == 30, "BColorControl %d,%d,%d", c.red, c.green, c.blue);

	int unhr, unh = aurora::unhandled_opcodes(&unhr);
	printf("unhandled opcodes: %d (with reply: %d)\n", unh, unhr);
	CHECK(unh == 0, "server saw opcodes it does not implement");
	printf("%d checks, %d failed\n", checks, fails);
	w->Lock(); w->Quit();
	return fails ? 1 : 0;
}
