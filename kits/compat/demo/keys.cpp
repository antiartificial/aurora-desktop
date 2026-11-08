// Keyboard demo. A focused BView receives B_KEY_DOWN through BWindow::_HandleKeyDown and BView::KeyDown, and
// draws what was typed. Special keys draw as <NAME>. KEYS_PRESET="text" starts with that text (used by the
// harness to render the expected picture without a keyboard). HELLO_QUIT_AFTER_MS quits for scripted runs.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Font.h>
#include <Message.h>
#include <stdio.h>
#include <stdlib.h>
#include <string>
#include <thread>

class KeyView : public BView {
public:
	KeyView(BRect frame) : BView(frame, "keys", B_FOLLOW_ALL, B_WILL_DRAW | B_NAVIGABLE)
	{
		SetViewColor(240, 240, 230);
		if (const char* p = getenv("KEYS_PRESET")) fText = p;
	}
	void AttachedToWindow() override { MakeFocus(true); }
	void KeyDown(const char* bytes, int32 n) override
	{
		BMessage* m = Window()->CurrentMessage();
		int32 mods = 0, key = 0, raw = 0;
		if (m) { m->FindInt32("modifiers", &mods); m->FindInt32("key", &key); m->FindInt32("raw_char", &raw); }
		printf("KEYDOWN n=%d byte0=0x%02x key=0x%02x raw=0x%02x mods=0x%x\n", (int)n, (unsigned char)bytes[0], key, raw, mods);
		fflush(stdout);
		fLast = bytes[0];
		switch (bytes[0]) {
			case B_BACKSPACE: if (!fText.empty()) fText.pop_back(); break;
			case B_ENTER: fText += "<CR>"; break;
			case B_UP_ARROW: fText += "<UP>"; break;
			case B_DOWN_ARROW: fText += "<DOWN>"; break;
			case B_LEFT_ARROW: fText += "<LEFT>"; break;
			case B_RIGHT_ARROW: fText += "<RIGHT>"; break;
			case B_ESCAPE: fText += "<ESC>"; break;
			case B_TAB: fText += "<TAB>"; break;
			default: fText.append(bytes, n);
		}
		Invalidate();
	}
	void WindowActivated(bool) {}
	void Draw(BRect) override
	{
		SetHighColor(0, 0, 0);
		StrokeRect(Bounds());
		BFont big(be_bold_font); big.SetSize(24);
		SetFont(&big);
		DrawString(fText.c_str(), BPoint(20, 60));
		if (getenv("HELLO_METRICS")) {
			font_height h; big.GetHeight(&h);
			printf("LEN %d:", (int)fText.size()); for (unsigned char c : fText) printf(" %02x", c); printf("\n");
			printf("TYPED \"%s\" x=20 baseline=60 width=%.4f ascent=%.4f descent=%.4f\n", fText.c_str(), big.StringWidth(fText.c_str()), h.ascent, h.descent);
			fflush(stdout);
		}
	}
	std::string fText;
	char fLast = 0;
};

class KeyWindow : public BWindow {
public:
	KeyWindow() : BWindow(BRect(100, 100, 419, 199), "Keys", B_TITLED_WINDOW, 0) { AddChild(new KeyView(Bounds())); }
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
};

class KeyApp : public BApplication {
public:
	KeyApp() : BApplication("application/x-vnd.aurora-keys") {}
	void ReadyToRun() override
	{
		(new KeyWindow)->Show();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main() { KeyApp app; app.Run(); return 0; }
