// BListView (Haiku's ListView.cpp, ListItem.cpp, StringItem.cpp) in a BScrollView. Mouse click selects, Up/Down
// move the selection (BListView::KeyDown), Enter invokes. 40 items "Item 00".. so the list scrolls.
// LIST_PRESET=N selects item N at start (the harness renders the expectation with it, without input).
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <ListView.h>
#include <StringItem.h>
#include <ScrollView.h>
#include <stdio.h>
#include <stdlib.h>
#include <thread>

enum { kSelected = 'sel!', kInvoked = 'inv!' };

class ListWindow : public BWindow {
public:
	ListWindow() : BWindow(BRect(0, 0, 259, 179), "List", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		fList = new BListView(BRect(0, 0, 220, 159), "list", B_SINGLE_SELECTION_LIST);
		for (int i = 0; i < 40; i++) {
			char b[32]; snprintf(b, sizeof(b), "Item %02d", i);
			fList->AddItem(new BStringItem(b));
		}
		fList->SetSelectionMessage(new BMessage(kSelected));
		fList->SetInvocationMessage(new BMessage(kInvoked));
		fList->SetTarget(this);
		BScrollView* sv = new BScrollView("sv", fList, B_FOLLOW_ALL, 0, false, true, B_FANCY_BORDER);
		sv->MoveTo(10, 10);
		bg->AddChild(sv);
		fList->MakeFocus(true);
		if (const char* p = getenv("LIST_PRESET")) fList->Select(atoi(p));
	}
	void MessageReceived(BMessage* m) override
	{
		switch (m->what) {
			case kSelected: printf("SELECTED %d\n", fList->CurrentSelection()); fflush(stdout); break;
			case kInvoked: printf("INVOKED %d\n", m->GetInt32("index", -1)); fflush(stdout); break;
			default: BWindow::MessageReceived(m);
		}
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
	BListView* fList;
};

class ListApp : public BApplication {
public:
	ListApp() : BApplication("application/x-vnd.aurora-list") {}
	void ReadyToRun() override
	{
		(new ListWindow)->Show();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main() { ListApp app; app.Run(); return 0; }
