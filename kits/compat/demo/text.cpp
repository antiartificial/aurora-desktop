// BTextView (Haiku's TextView.cpp + textview_support/) in a BWindow: typing, backspace, caret, selection.
// TEXT_PRESET="text" starts with that text, and TEXT_SELECT="a,b" with that selection, so the harness can render the
// expected picture in a separate process with no compositor and no keyboard. Every key event is also logged
// (TEXT line) after BTextView has handled it. Blinking is the real B_PULSE path.
// AURORA_APPSERVER_ACTIVE=1 (server) makes a compositor-less render behave as a focused window.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <TextView.h>
#include <Font.h>
#include <stdio.h>
#include <stdlib.h>
#include <string>
#include <string.h>
#include <thread>
#include <server.h>

class TView : public BTextView {
public:
	TView(BRect f, BRect t) : BTextView(f, "text", t, B_FOLLOW_ALL, B_WILL_DRAW | B_NAVIGABLE | B_PULSE_NEEDED) {}
	void KeyDown(const char* b, int32 n) override
	{
		BTextView::KeyDown(b, n);
		report("KEY");
	}
	void report(const char* tag)
	{
		int32 s, e; GetSelection(&s, &e);
		printf("%s text=\"", tag);
		for (const char* p = Text(); *p; p++) { if (*p == '\n') printf("\\n"); else putchar(*p); }
		printf("\" len=%d sel=%d,%d lines=%d\n", (int)TextLength(), (int)s, (int)e, (int)CountLines());
		// GEOM, in window coordinates (this view sits at (10,10)): where BTextView says each character boundary is
		// (pt, from PointAt) and where BFont says it is (fw, from StringWidth of the prefix, in the run's font).
		// The harness compares both with the pixels.
		BFont f; GetFontAndColor(0, &f);
		font_height fh; f.GetHeight(&fh);
		BPoint p0 = PointAt(0); float lh = 0; BPoint pc = PointAt(s, &lh);
		printf("GEOM text=\"%s\" left=%.3f top=%.3f lineh=%.0f n=%d caret=%.3f sel=%d,%d size=%.1f asc=%.4f desc=%.4f pt=", Text(), Frame().left + TextRect().left, Frame().top + p0.y, lh,
			(int)TextLength(), Frame().left + pc.x, (int)s, (int)e, f.Size(), fh.ascent, fh.descent);
		for (int i = 0; i <= TextLength(); i++) printf("%s%.3f", i ? "," : "", Frame().left + PointAt(i).x);
		printf(" fw=");
		std::string t = Text();
		for (int i = 0; i <= TextLength(); i++) printf("%s%.3f", i ? "," : "", Frame().left + TextRect().left + f.StringWidth(t.substr(0, i).c_str()));
		printf("\n"); fflush(stdout);
	}
	void Pulse() override { BTextView::Pulse(); if (getenv("HELLO_METRICS")) { printf("PULSE t=%lld\n", (long long)(system_time() / 1000)); fflush(stdout); } }
	void WindowActivated(bool a) override { BTextView::WindowActivated(a); if (getenv("TEXT_TRACE")) { printf("ACTIVATED %d focus=%d\n", a, IsFocus()); fflush(stdout); } }
	void MakeFocus(bool f) override { BTextView::MakeFocus(f); if (getenv("TEXT_TRACE")) { printf("FOCUS %d\n", f); fflush(stdout); } }
};

class TWindow : public BWindow {
public:
	TWindow() : BWindow(BRect(0, 0, 319, 119), "Text", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		BRect f(10, 10, 309, 109), t = f; t.OffsetTo(B_ORIGIN); t.InsetBy(4, 4);
		fText = new TView(f, t);
		fText->SetViewColor(255, 255, 255);
		BFont fnt(be_plain_font); fnt.SetSize(14);
		fText->SetFontAndColor(&fnt);
		fText->MakeEditable(true);
		bg->AddChild(fText);
		if (const char* p = getenv("TEXT_PRESET")) fText->SetText(p);
		if (const char* s = getenv("TEXT_SELECT")) { int a = 0, b = 0; sscanf(s, "%d,%d", &a, &b); fText->Select(a, b); }
		fText->MakeFocus(true);
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
	void WindowActivated(bool a) override { if (getenv("TEXT_TRACE")) { printf("WINDOW ACTIVATED %d\n", a); fflush(stdout); } }
	TView* fText;
};

class TApp : public BApplication {
public:
	TApp() : BApplication("application/x-vnd.aurora-text") {}
	void ReadyToRun() override
	{
		TWindow* w = new TWindow;
		w->Show();
		if (getenv("HELLO_METRICS")) { w->Lock(); w->fText->report("INIT"); w->Unlock(); }
		if (const char* f = getenv("TEXT_CMD_FIFO")) {
			// "key BackSpace", "shift Home", ...: keys the compositor's test socket has no name for (see inject_special_key).
			std::string path = f;
			std::thread([path] {
				FILE* in = fopen(path.c_str(), "r");
				char line[128];
				while (in && fgets(line, sizeof(line), in)) {
					char verb[16] = "", name[32] = "";
					if (sscanf(line, "%15s %31s", verb, name) < 2) continue;
					bool ok = aurora::inject_special_key(0, name, !strcmp(verb, "shift"));
					printf("INJECT %s %s %s\n", verb, name, ok ? "ok" : "REFUSED"); fflush(stdout);
				}
			}).detach();
		}
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main() { TApp app; app.Run(); return 0; }
