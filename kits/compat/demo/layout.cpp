// BGroupLayout / BGridLayout / BLayoutBuilder (Haiku's GroupLayout.cpp, GridLayout.cpp, TwoDimensionalLayout.cpp,
// ComplexLayouter.cpp, SimpleLayouter.cpp ...). Three buttons in a row above a 2x2 grid; the window sizes itself from
// the layout. LAYOUT_REPORT=1 prints each button's frame so the harness can compare them with the pixels.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Button.h>
#include <StringView.h>
#include <GroupLayout.h>
#include <GridLayout.h>
#include <LayoutBuilder.h>
#include <stdio.h>
#include <stdlib.h>
#include <thread>

class LWindow : public BWindow {
public:
	LWindow() : BWindow(BRect(0, 0, 100, 100), "Layout", B_TITLED_WINDOW, B_AUTO_UPDATE_SIZE_LIMITS)
	{
		fA = new BButton("a", "Alpha", new BMessage('a'));
		fB = new BButton("b", "Beta", new BMessage('b'));
		fC = new BButton("c", "Gamma is longer", new BMessage('c'));
		fS1 = new BStringView("s1", "one"); fS2 = new BStringView("s2", "two");
		fS3 = new BStringView("s3", "three"); fS4 = new BStringView("s4", "four, wider");
		BLayoutBuilder::Group<>(this, B_VERTICAL, 8)
			.SetInsets(10, 10, 10, 10)
			.AddGroup(B_HORIZONTAL, 6)
				.Add(fA).Add(fB).Add(fC)
			.End()
			.AddGrid(6, 4)
				.Add(fS1, 0, 0).Add(fS2, 1, 0).Add(fS3, 0, 1).Add(fS4, 1, 1)
			.End();
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
	BView* fA; BView* fB; BView* fC; BView* fS1; BView* fS2; BView* fS3; BView* fS4;
};

class LApp : public BApplication {
public:
	LApp() : BApplication("application/x-vnd.aurora-layout") {}
	void ReadyToRun() override
	{
		LWindow* w = new LWindow;
		w->Show();
		if (getenv("LAYOUT_REPORT")) {
			snooze(400000);
			w->Lock();
			printf("WINDOW %.0fx%.0f\n", w->Bounds().Width() + 1, w->Bounds().Height() + 1);
			BView* v[] = {w->fA, w->fB, w->fC, w->fS1, w->fS2, w->fS3, w->fS4};
			for (BView* x : v) { BRect f = x->Frame(); printf("VIEW %s %.0f,%.0f,%.0f,%.0f\n", x->Name(), f.left, f.top, f.right, f.bottom); }
			w->Unlock(); fflush(stdout);
		}
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main() { LApp app; app.Run(); return 0; }
