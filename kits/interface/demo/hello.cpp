// A BeOS program, written the BeOS way: BApplication, BWindow subclass, BView subclass with Draw().
// Nothing in it knows about Wayland. The window shows rectangles, a line, and text in three sizes.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Font.h>
#include <StringView.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <thread>

enum { kFrameW = 320, kFrameH = 200 };

class HelloView : public BView {
public:
	HelloView(BRect frame) : BView(frame, "hello", B_FOLLOW_ALL, B_WILL_DRAW | B_FULL_UPDATE_ON_RESIZE)
	{
		SetViewColor(240, 240, 230);
	}
	void Draw(BRect) override
	{
		BRect b = Bounds();
		SetHighColor(200, 30, 30);  FillRect(BRect(20, 20, 99, 59));	// red 80x40
		SetHighColor(30, 160, 60);  FillRect(BRect(110, 20, 189, 59));	// green
		SetHighColor(40, 70, 210);  FillRect(BRect(200, 20, 279, 59));	// blue
		SetHighColor(0, 0, 0);
		StrokeRect(b);								// 1px frame around the whole view
		StrokeLine(BPoint(20, 70), BPoint(279, 70));				// rule
		font_height fh;
		BFont f(be_plain_font);
		f.GetHeight(&fh);
		SetFont(&f);
		DrawString("Hello, Aurora", BPoint(20, 100));				// baseline y = 100
		BFont big(be_bold_font); big.SetSize(24);
		SetFont(&big);
		DrawString("BeOS toolkit", BPoint(20, 140));
		BFont mono(be_fixed_font); mono.SetSize(12);
		SetFont(&mono);
		DrawString("0123456789 {fixed}", BPoint(20, 170));
		if (getenv("HELLO_METRICS")) {	// what BFont said about each string: the harness checks the pixels against it
			static const struct { const char* s; const BFont* f; float y; const char* tag; } L[] = {
				{"Hello, Aurora", &f, 100, "plain"}, {"BeOS toolkit", &big, 140, "bold24"}, {"0123456789 {fixed}", &mono, 170, "fixed"}};
			for (int i = 0; i < 3; i++) {
				font_height h; L[i].f->GetHeight(&h);
				printf("METRIC %s x=20 baseline=%.0f width=%.4f ascent=%.4f descent=%.4f\n", L[i].tag, L[i].y, L[i].f->StringWidth(L[i].s), h.ascent, h.descent);
			}
			fflush(stdout);
		}
	}
};

class HelloWindow : public BWindow {
public:
	HelloWindow() : BWindow(BRect(100, 100, 100 + kFrameW - 1, 100 + kFrameH - 1), "Hello from Haiku's Interface Kit",
		B_TITLED_WINDOW, B_QUIT_ON_WINDOW_CLOSE)
	{
		AddChild(new HelloView(Bounds()));
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
};

class HelloApp : public BApplication {
public:
	HelloApp() : BApplication("application/x-vnd.aurora-hello") {}
	void ReadyToRun() override
	{
		(new HelloWindow)->Show();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {	// for scripted runs
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main()
{
	HelloApp app;
	app.Run();
	return 0;
}
