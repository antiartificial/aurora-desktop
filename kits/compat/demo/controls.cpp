// BStringView and BButton, the real Haiku classes (StringView.cpp, Button.cpp, Control.cpp, HaikuControlLook.cpp),
// in a window. Clicking the button changes the string. HELLO_QUIT_AFTER_MS quits for scripted runs.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Button.h>
#include <StringView.h>
#include <stdio.h>
#include <stdlib.h>
#include <thread>

enum { kClicked = 'clck' };

class CtlWindow : public BWindow {
public:
	CtlWindow() : BWindow(BRect(80, 80, 339, 199), "Controls", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		fLabel = new BStringView(BRect(20, 20, 239, 40), "label", "Clicks: 0");
		bg->AddChild(fLabel);
		fButton = new BButton(BRect(20, 60, 139, 90), "go", "Click me", new BMessage(kClicked));
		bg->AddChild(fButton);
	}
	void MessageReceived(BMessage* m) override
	{
		if (m->what == kClicked) {
			char buf[32]; snprintf(buf, sizeof(buf), "Clicks: %d", ++fCount);
			fLabel->SetText(buf);
			printf("CLICK %d\n", fCount); fflush(stdout);
		} else
			BWindow::MessageReceived(m);
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
	BStringView* fLabel; BButton* fButton; int fCount = 0;
};

class CtlApp : public BApplication {
public:
	CtlApp() : BApplication("application/x-vnd.aurora-controls") {}
	void ReadyToRun() override
	{
		(new CtlWindow)->Show();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main() { CtlApp app; app.Run(); return 0; }
