// BMenuBar / BMenu / BMenuItem (Haiku's MenuBar.cpp, Menu.cpp, MenuItem.cpp, MenuWindow.cpp). A File menu with three
// items and a submenu. Every selected item prints "ITEM what" and quits on Quit. HELLO_QUIT_AFTER_MS quits for scripted runs.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <Menu.h>
#include <MenuBar.h>
#include <MenuItem.h>
#include <PopUpMenu.h>
#include <Font.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <thread>
#include <server.h>

enum { kNew = 'new_', kOpen = 'open', kSave = 'save', kQuit = 'quit', kCopy = 'copy', kPaste = 'past', kFmt = 'fmt_' };

class MWindow : public BWindow {
public:
	MWindow() : BWindow(BRect(0, 0, 319, 199), "Menus", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		fBar = new BMenuBar(BRect(0, 0, 319, 20), "bar");
		BMenu* file = new BMenu("File");
		file->AddItem(new BMenuItem("New", new BMessage(kNew), 'N'));
		file->AddItem(new BMenuItem("Open", new BMessage(kOpen), 'O'));
		file->AddSeparatorItem();
		file->AddItem(new BMenuItem("Save", new BMessage(kSave), 'S'));
		BMenu* fmt = new BMenu("Format");
		fmt->AddItem(new BMenuItem("Plain", new BMessage(kFmt)));
		fmt->AddItem(new BMenuItem("Rich", new BMessage(kFmt)));
		file->AddItem(fmt);
		file->AddSeparatorItem();
		file->AddItem(new BMenuItem("Quit", new BMessage(kQuit), 'Q'));
		fBar->AddItem(file);
		BMenu* edit = new BMenu("Edit");
		edit->AddItem(new BMenuItem("Copy", new BMessage(kCopy), 'C'));
		edit->AddItem(new BMenuItem("Paste", new BMessage(kPaste), 'V'));
		fBar->AddItem(edit);
		AddChild(fBar);
	}
	void MessageReceived(BMessage* m) override
	{
		switch (m->what) {
			case kNew: case kOpen: case kSave: case kCopy: case kPaste: case kFmt:
			{
				const char* label = NULL; BMenuItem* it = NULL;
				m->FindPointer("source", (void**)&it);
				if (it) label = it->Label();
				char tag[5] = {(char)(m->what >> 24), (char)(m->what >> 16), (char)(m->what >> 8), (char)m->what, 0};
				printf("ITEM %s %s\n", tag, label ? label : "?");
				fflush(stdout); break;
			}
			case kQuit: printf("ITEM quit\n"); fflush(stdout); be_app->PostMessage(B_QUIT_REQUESTED); break;
			default: BWindow::MessageReceived(m);
		}
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
	BMenuBar* fBar;
};

class MApp : public BApplication {
public:
	MApp() : BApplication("application/x-vnd.aurora-menu") {}
	void ReadyToRun() override
	{
		MWindow* w = new MWindow;
		w->Show();
		if (getenv("HELLO_METRICS")) {
			w->Lock();
			for (int i = 0; i < w->fBar->CountItems(); i++) {
				BRect f = w->fBar->ItemAt(i)->Frame();
				printf("BARITEM %s frame=%.0f,%.0f,%.0f,%.0f\n", w->fBar->ItemAt(i)->Label(), f.left, f.top, f.right, f.bottom);
			}
			printf("BAR frame=%.0f,%.0f,%.0f,%.0f\n", w->fBar->Frame().left, w->fBar->Frame().top, w->fBar->Frame().right, w->fBar->Frame().bottom);
			w->Unlock(); fflush(stdout);
		}
		if (const char* sc = getenv("MENU_SCRIPT")) {
			// compositor-less driver: "w:x,y[:hold_ms]" steps separated by ';' = move to (x,y) of window w, then a click that
			// holds the button hold_ms (default 80, a human click; negative = move only). Goes through the server's internal pointer event.
			std::string script = sc;
			std::thread([script] {
				snooze(600000);
				size_t pos = 0;
				while (pos < script.size()) {
					size_t e = script.find(';', pos); if (e == std::string::npos) e = script.size();
					int win = 0, hold = 80; float x = 0, y = 0;
					sscanf(script.substr(pos, e - pos).c_str(), "%d:%f,%f:%d", &win, &x, &y, &hold);
					aurora::inject_pointer(win, x, y, -1); snooze(150000);
					if (hold >= 0) { aurora::inject_pointer(win, x, y, 1); snooze(hold * 1000LL); aurora::inject_pointer(win, x, y, 0); }
					snooze(500000);
					pos = e + 1;
				}
			}).detach();
		}
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main() { MApp app; app.Run(); return 0; }
