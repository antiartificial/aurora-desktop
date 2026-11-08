// BScrollView + BScrollBar (Haiku's ScrollView.cpp / ScrollBar.cpp) around a view taller than its frame.
// 30 rows of 20 px (600 px of content) in a window that shows about 160 px. Row i is a bar of colour
// (8i, 200-5i, 100) with the label "Row %02d", so the harness can compute from a decoded pixel which row is
// at which window position. Up/Down/PageUp/PageDown/Home/End scroll. SCROLL_PRESET=N starts scrolled to N
// (the harness renders the expectation with it, without a compositor).
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <ScrollView.h>
#include <ScrollBar.h>
#include <Font.h>
#include <stdio.h>
#include <stdlib.h>
#include <thread>

enum { kRows = 30, kRowH = 20 };

class Tall : public BView {
public:
	Tall(BRect frame) : BView(frame, "tall", B_FOLLOW_ALL, B_WILL_DRAW | B_FRAME_EVENTS | B_NAVIGABLE)
	{
		SetViewColor(250, 250, 250);
	}
	void Fixup()
	{
		BScrollBar* v = ScrollBar(B_VERTICAL);
		if (!v) return;
		float total = kRows * kRowH, h = Bounds().Height() + 1;
		v->SetRange(0, total > h ? total - h : 0);
		v->SetProportion(h / total);
		v->SetSteps(13, h - kRowH);	// 13: deliberately not a multiple of the 20 px row
	}
	void AttachedToWindow() override
	{
		Fixup();
		MakeFocus(true);
		if (const char* p = getenv("SCROLL_PRESET")) ScrollBar(B_VERTICAL)->SetValue(atof(p));
	}
	void FrameResized(float, float) override { Fixup(); }
	void KeyDown(const char* bytes, int32 n) override
	{
		BScrollBar* v = ScrollBar(B_VERTICAL);
		float step, big;
		v->GetSteps(&step, &big);
		float cur = v->Value(), lo, hi;
		v->GetRange(&lo, &hi);
		switch (bytes[0]) {
			case B_DOWN_ARROW: v->SetValue(cur + step); break;
			case B_UP_ARROW: v->SetValue(cur - step); break;
			case B_PAGE_DOWN: v->SetValue(cur + big); break;
			case B_PAGE_UP: v->SetValue(cur - big); break;
			case B_HOME: v->SetValue(lo); break;
			case B_END: v->SetValue(hi); break;
			default: BView::KeyDown(bytes, n);
		}
		printf("SCROLL value=%.0f bounds.top=%.0f\n", v->Value(), Bounds().top);
		fflush(stdout);
	}
	void MouseDown(BPoint) override { MakeFocus(true); }
	void Draw(BRect update) override
	{
		BFont f(be_plain_font);
		SetFont(&f);
		int first = (int)(update.top / kRowH), last = (int)(update.bottom / kRowH);
		if (first < 0) first = 0;
		if (last >= kRows) last = kRows - 1;
		for (int i = first; i <= last; i++) {
			float y = i * kRowH;
			SetHighColor(8 * i, 200 - 5 * i, 100);
			FillRect(BRect(0, y, Bounds().right, y + kRowH - 3));
			SetHighColor(255, 255, 255);
			char b[16]; snprintf(b, sizeof(b), "Row %02d", i);
			DrawString(b, BPoint(6, y + 14));
		}
		if (getenv("HELLO_METRICS")) {
			printf("DRAW update=%.0f..%.0f bounds.top=%.0f\n", update.top, update.bottom, Bounds().top);
			fflush(stdout);
		}
	}
};

class ScrollWindow : public BWindow {
public:
	ScrollWindow() : BWindow(BRect(0, 0, 259, 179), "Scroll", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		Tall* t = new Tall(BRect(0, 0, 220, 159));
		BScrollView* sv = new BScrollView("sv", t, B_FOLLOW_ALL, 0, false, true, B_FANCY_BORDER);
		sv->MoveTo(10, 10);
		bg->AddChild(sv);
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
};

class ScrollApp : public BApplication {
public:
	ScrollApp() : BApplication("application/x-vnd.aurora-scroll") {}
	void ReadyToRun() override
	{
		(new ScrollWindow)->Show();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main() { ScrollApp app; app.Run(); return 0; }
