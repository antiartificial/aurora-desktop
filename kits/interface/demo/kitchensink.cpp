// Kitchen sink: every Interface Kit control that runs on Aurora, in one BApplication. Haiku's real BTabView,
// BMenuBar, BButton, BCheckBox, BRadioButton, BBox, BTextControl, BSlider, BSpinner, BOptionPopUp, BStatusBar,
// BListView, BTextView, BScrollView, BSplitView, BCardLayout, BColorControl and BAlert, laid out with BLayoutBuilder.
// Every control is live: acting on it changes a label (and prints "EV name value" so a harness can follow along).
// KS_REPORT=1 prints "CTL name l,t,r,b" (window coordinates) once the layout has settled.
// HELLO_QUIT_AFTER_MS quits for scripted runs.
#include <Alert.h>
#include <Application.h>
#include <Box.h>
#include <Button.h>
#include <CardLayout.h>
#include <CheckBox.h>
#include <ColorControl.h>
#include <GroupLayout.h>
#include <GroupView.h>
#include <Invoker.h>
#include <LayoutBuilder.h>
#include <ListView.h>
#include <Menu.h>
#include <MenuBar.h>
#include <MenuItem.h>
#include <OptionPopUp.h>
#include <RadioButton.h>
#include <ScrollView.h>
#include <Slider.h>
#include <Spinner.h>
#include <SplitView.h>
#include <StatusBar.h>
#include <StringItem.h>
#include <StringView.h>
#include <TabView.h>
#include <TextControl.h>
#include <TextView.h>
#include <Window.h>
#include <stdio.h>
#include <stdlib.h>
#include <string>
#include <thread>
#include <vector>
#include <server.h>

static const char* kIntro = "Type here. Selecting a list item appends a line.\n";

enum {
	kPress = 'prs_', kBold = 'bld_', kItalic = 'itl_', kSizeS = 'szS_', kSizeM = 'szM_', kSizeL = 'szL_',
	kAlert = 'alr_', kAlertDone = 'alD_', kName = 'nam_', kFlavour = 'flv_', kVolume = 'vol_', kCount = 'cnt_',
	kPicked = 'pck_', kColour = 'col_', kPrev = 'prv_', kNext = 'nxt_', kReset = 'rst_', kClear = 'clr_',
	kNextTab = 'ntb_', kQuit = 'quit'
};

class KSWindow : public BWindow {
public:
	KSWindow() : BWindow(BRect(0, 0, 100, 100), "Aurora Kitchen Sink", B_TITLED_WINDOW, B_AUTO_UPDATE_SIZE_LIMITS)
	{
		BMenuBar* bar = fMenuBar = _BuildMenus();
		fStatus = new BStringView("status", "Ready");
		fTabs = new BTabView("tabs", B_WIDTH_FROM_WIDEST);
		fPage = 0; fTabs->AddTab(_ButtonsPage());
		fPage = 1; fTabs->AddTab(_InputPage());
		fPage = 2; fTabs->AddTab(_DataPage());
		fPage = 3; fTabs->AddTab(_ExtrasPage());
		fPage = -1;
		if (const char* t = getenv("KS_TAB")) fTabs->Select(atoi(t));
		fTabs->SetExplicitMinSize(BSize(520, 330));
		BLayoutBuilder::Group<>(this, B_VERTICAL, 0)
			.Add(bar)
			.AddGroup(B_VERTICAL, 4).SetInsets(8, 8, 8, 4)
				.Add(fTabs)
				.Add(fStatus)
			.End();
		Track("tabs", fTabs); Track("status", fStatus);
	}

	void MessageReceived(BMessage* m) override
	{
		char buf[96];
		switch (m->what) {
			case kPress:
				fPresses++;
				snprintf(buf, sizeof(buf), "Pressed %d time%s", fPresses, fPresses == 1 ? "" : "s");
				fPressLabel->SetText(buf); Event("button", fPresses); break;
			case kBold: case kItalic:
				_UpdateStyle(); Event(m->what == kBold ? "bold" : "italic", m->GetInt32("be:value", 0)); break;
			case kSizeS: case kSizeM: case kSizeL:
			{
				const char* n = m->what == kSizeS ? "Small" : m->what == kSizeM ? "Medium" : "Large";
				snprintf(buf, sizeof(buf), "Size: %s", n); fSizeLabel->SetText(buf);
				Event("size", m->what == kSizeS ? 0 : m->what == kSizeM ? 1 : 2); break;
			}
			case kAlert:
			{
				BAlert* a = new BAlert("Alert", "This is a real BAlert.\nChoose a button.", "Cancel", "OK", NULL,
					B_WIDTH_AS_USUAL, B_EMPTY_ALERT);
				a->SetShortcut(0, B_ESCAPE);		// Cancel answers to Escape; OK is the default button, so Return answers it
				a->Go(new BInvoker(new BMessage(kAlertDone), this));
				if (getenv("KS_REPORT") && a->Lock()) {		// where the alert's buttons are, in the alert's own window coordinates
					for (int i = 0; i < 2; i++) {
						BView* bt = a->ButtonAt(i); BRect f = bt->ConvertToScreen(bt->Bounds()); f.OffsetBy(-a->Frame().left, -a->Frame().top);
						printf("ALERT button%d %.0f,%.0f,%.0f,%.0f\n", i, f.left, f.top, f.right, f.bottom);
					}
					printf("ALERT size %.0fx%.0f\n", a->Bounds().Width() + 1, a->Bounds().Height() + 1); fflush(stdout);
					a->Unlock();
				}
				Event("alert", 1); break;
			}
			case kAlertDone:
				snprintf(buf, sizeof(buf), "Alert answered: %s", m->GetInt32("which", -1) == 1 ? "OK" : "Cancel");
				fAlertLabel->SetText(buf); SetStatus(buf); Event("alert-which", m->GetInt32("which", -1)); break;
			case kName:
				snprintf(buf, sizeof(buf), "Hello, %s!", fName->Text()); fHello->SetText(buf);
				Event("text", fName->TextView()->TextLength()); break;
			case kFlavour:
				snprintf(buf, sizeof(buf), "Flavour value %d", (int)m->GetInt32("be:value", -1)); fFlavourLabel->SetText(buf);
				Event("flavour", m->GetInt32("be:value", -1)); break;
			case kVolume:
				snprintf(buf, sizeof(buf), "Volume %d", (int)fSlider->Value()); fVolLabel->SetText(buf);
				fBar->SetTo(fSlider->Value()); Event("slider", fSlider->Value()); break;
			case kCount:
				snprintf(buf, sizeof(buf), "Count is %d", (int)fSpinner->Value()); fCountLabel->SetText(buf);
				Event("spinner", fSpinner->Value()); break;
			case kPicked:
			{
				int32 i = fList->CurrentSelection();
				if (i < 0) break;
				snprintf(buf, sizeof(buf), "Picked item %02d\n", (int)i); fText->Insert(buf);
				snprintf(buf, sizeof(buf), "List: item %02d", (int)i); SetStatus(buf); Event("list", i); break;
			}
			case kColour:
			{
				rgb_color c = fColour->ValueAsColor(); fSwatch->SetViewColor(c); fSwatch->Invalidate();
				Event("colour", (c.red << 16) | (c.green << 8) | c.blue); break;
			}
			case kPrev: case kNext:
			{
				int32 n = fCards->CountItems(), v = fCards->VisibleIndex();
				fCards->SetVisibleItem((v + (m->what == kNext ? 1 : n - 1)) % n); Event("card", fCards->VisibleIndex()); break;
			}
			case kReset: _Reset(); break;
			case kClear: fText->SetText(""); SetStatus("Text cleared"); Event("clear", 1); break;
			case kNextTab:
				fTabs->Select((fTabs->Selection() + 1) % fTabs->CountTabs()); SetStatus("Next tab");
				Event("tab", fTabs->Selection()); break;
			case kQuit: be_app->PostMessage(B_QUIT_REQUESTED); break;
			default: BWindow::MessageReceived(m);
		}
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }

	static void Event(const char* what, int64 v) { printf("EV %s %lld\n", what, (long long)v); fflush(stdout); }
	void SetStatus(const char* s) { fStatus->SetText(s); }

	// Prints every tracked control's frame, page by page (a hidden page has no meaningful frames, so each tab is
	// selected in turn), then goes back to the tab it started on.
	void Report()
	{
		Lock(); int start = fTabs->Selection(); printf("WINDOW %.0fx%.0f\n", Bounds().Width() + 1, Bounds().Height() + 1); Unlock();
		for (int pg = -1; pg < fTabs->CountTabs(); pg++) {
			if (pg >= 0) { Lock(); fTabs->Select(pg); Unlock(); snooze(300000); }
			Lock();
			for (size_t i = 0; i < fNames.size(); i++) {
				if (fNames[i].page != pg) continue;
				BView* v = fNames[i].view; BRect f = v->ConvertToScreen(v->Bounds());
				f.OffsetBy(-Frame().left, -Frame().top);
				printf("CTL %s %.0f,%.0f,%.0f,%.0f\n", fNames[i].name.c_str(), f.left, f.top, f.right, f.bottom);
			}
			if (pg == 1 && fSlider != NULL) {		// where the slider's thumb is, as the kit lays it out: an AIM for the drag, never an assertion
				BRect f = fSlider->ConvertToScreen(fSlider->ThumbFrame()); f.OffsetBy(-Frame().left, -Frame().top);
				printf("CTL thumb %.0f,%.0f,%.0f,%.0f\n", f.left, f.top, f.right, f.bottom);
			}
			if (pg < 0) {
				for (int i = 0; i < fMenuBar->CountItems(); i++) {		// the menu bar's titles, in window coordinates (the bar sits at 0,0)
					BRect f = fMenuBar->ItemAt(i)->Frame();
					printf("CTL menu%d %.0f,%.0f,%.0f,%.0f\n", i, f.left, f.top, f.right, f.bottom);
				}
				for (int i = 0; i < fTabs->CountTabs(); i++) {
					BRect f = fTabs->ConvertToScreen(fTabs->TabFrame(i)); f.OffsetBy(-Frame().left, -Frame().top);
					printf("CTL tab%d %.0f,%.0f,%.0f,%.0f\n", i, f.left, f.top, f.right, f.bottom);
				}
			}
			Unlock();
		}
		Lock(); fTabs->Select(start); Unlock(); snooze(300000); printf("REPORT done\n"); fflush(stdout);
	}
	void Track(const char* n, BView* v) { Tracked t = {n, v, fPage}; fNames.push_back(t); }

private:
	BMenuBar* _BuildMenus()
	{
		BMenuBar* bar = new BMenuBar("bar");
		BMenu* file = new BMenu("File");
		file->AddItem(new BMenuItem("Reset all", new BMessage(kReset), 'R'));
		file->AddItem(new BMenuItem("Show alert", new BMessage(kAlert), 'A'));
		file->AddSeparatorItem();
		file->AddItem(new BMenuItem("Quit", new BMessage(kQuit), 'Q'));
		bar->AddItem(file);
		BMenu* edit = new BMenu("Edit");
		edit->AddItem(new BMenuItem("Clear text", new BMessage(kClear)));
		edit->AddItem(new BMenuItem("Next tab", new BMessage(kNextTab), 'T'));
		bar->AddItem(edit);
		Track("bar", bar);
		return bar;
	}

	// File > Reset all: every control back to how the window first looked. Setting a control's value from code does not
	// send its message, so the labels that follow the controls are set here too.
	void _Reset()
	{
		fPresses = 0; fPressLabel->SetText("Pressed 0 times");
		fBold->SetValue(0); fItalic->SetValue(0); _UpdateStyle();
		fMedium->SetValue(B_CONTROL_ON); fSizeLabel->SetText("Size: Medium");
		fAlertLabel->SetText("No alert shown yet");
		fName->SetText(""); fHello->SetText("Hello, !");
		fFlavour->SetValue(1); fFlavourLabel->SetText("Flavour value 1");
		fSlider->SetValue(25); fVolLabel->SetText("Volume 25"); fBar->SetTo(25);
		fSpinner->SetValue(0); fCountLabel->SetText("Count is 0");
		fList->DeselectAll(); fList->ScrollTo(0, 0);
		fText->SetText(kIntro); fText->ScrollTo(0, 0);
		fSplit->SetItemWeight(0, 1.0f, false); fSplit->SetItemWeight(1, 2.0f, true);
		fColour->SetValue((rgb_color){60, 120, 200, 255}); fSwatch->SetViewColor(60, 120, 200); fSwatch->Invalidate();
		fCards->SetVisibleItem(0);
		fTabs->Select(0);
		if (BView* f = CurrentFocus()) f->MakeFocus(false);
		SetStatus("Ready"); Event("reset", 1);
	}

	void _UpdateStyle()
	{
		BFont f(be_plain_font);
		bool b = fBold->Value() != 0, i = fItalic->Value() != 0;
		f.SetFace(b ? B_BOLD_FACE : i ? B_ITALIC_FACE : B_REGULAR_FACE);
		fStyleLabel->SetFont(&f);
		fStyleLabel->SetText(b ? (i ? "Bold italic" : "Bold") : i ? "Italic" : "Plain");
	}

	BView* _ButtonsPage()
	{
		BView* page = new BView("Buttons", B_WILL_DRAW);
		fPressLabel = new BStringView("presses", "Pressed 0 times");
		BButton* press = new BButton("press", "Press me", new BMessage(kPress));
		BButton* alert = new BButton("alertbtn", "Show alert...", new BMessage(kAlert));
		fAlertLabel = new BStringView("alertlabel", "No alert shown yet");
		fBold = new BCheckBox("bold", "Bold", new BMessage(kBold));
		fItalic = new BCheckBox("italic", "Italic", new BMessage(kItalic));
		fStyleLabel = new BStringView("style", "Plain");
		BRadioButton* s = new BRadioButton("small", "Small", new BMessage(kSizeS));
		BRadioButton* m = fMedium = new BRadioButton("medium", "Medium", new BMessage(kSizeM));
		BRadioButton* l = new BRadioButton("large", "Large", new BMessage(kSizeL));
		m->SetValue(B_CONTROL_ON);
		fSizeLabel = new BStringView("sizelabel", "Size: Medium");
		BBox* styleBox = new BBox("stylebox"); styleBox->SetLabel("Style (BCheckBox)");
		BGroupView* sg = new BGroupView(B_VERTICAL, 4);
		BLayoutBuilder::Group<>(sg).SetInsets(10, 14, 10, 10).Add(fBold).Add(fItalic).Add(fStyleLabel);
		styleBox->AddChild(sg);
		BBox* sizeBox = new BBox("sizebox"); sizeBox->SetLabel("Size (BRadioButton)");
		BGroupView* zg = new BGroupView(B_VERTICAL, 4);
		BLayoutBuilder::Group<>(zg).SetInsets(10, 14, 10, 10).Add(s).Add(m).Add(l).Add(fSizeLabel);
		sizeBox->AddChild(zg);
		BLayoutBuilder::Group<>(page, B_VERTICAL, 10).SetInsets(10, 10, 10, 10)
			.AddGroup(B_HORIZONTAL, 8).Add(press).Add(fPressLabel).AddGlue().End()
			.AddGroup(B_HORIZONTAL, 10).Add(styleBox).Add(sizeBox).End()
			.AddGroup(B_HORIZONTAL, 8).Add(alert).Add(fAlertLabel).AddGlue().End()
			.AddGlue();
		Track("press", press); Track("pressed", fPressLabel); Track("bold", fBold); Track("italic", fItalic);
		Track("style", fStyleLabel); Track("small", s); Track("medium", m); Track("large", l);
		Track("sizelabel", fSizeLabel); Track("stylebox", styleBox); Track("sizebox", sizeBox);
		Track("alertbtn", alert); Track("alertlabel", fAlertLabel);
		return page;
	}

	BView* _InputPage()
	{
		BView* page = new BView("Input", B_WILL_DRAW);
		fName = new BTextControl("name", "Name:", "", new BMessage(kName));
		fName->SetModificationMessage(new BMessage(kName));
		fHello = new BStringView("hello", "Hello, !");
		BOptionPopUp* flavour = fFlavour = new BOptionPopUp("flavour", "Flavour:", new BMessage(kFlavour));
		flavour->AddOption("Vanilla", 1); flavour->AddOption("Chocolate", 2); flavour->AddOption("Mint", 3);
		fFlavourLabel = new BStringView("flavourlabel", "Flavour value 1");
		fSlider = new BSlider("volume", "Volume", new BMessage(kVolume), 0, 100, B_HORIZONTAL);
		fSlider->SetModificationMessage(new BMessage(kVolume));
		fSlider->SetValue(25);
		fSlider->SetLimitLabels("0", "100");
		fVolLabel = new BStringView("vollabel", "Volume 25");
		fBar = new BStatusBar("level", "Level", "of 100"); fBar->SetMaxValue(100); fBar->SetTo(25);
		fSpinner = new BSpinner("count", "Count:", new BMessage(kCount));
		fSpinner->SetRange(0, 20);
		fCountLabel = new BStringView("countlabel", "Count is 0");
		BLayoutBuilder::Group<>(page, B_VERTICAL, 8).SetInsets(10, 10, 10, 10)
			.AddGroup(B_HORIZONTAL, 8).Add(fName).Add(fHello).End()
			.AddGroup(B_HORIZONTAL, 8).Add(flavour).Add(fFlavourLabel).AddGlue().End()
			.AddGroup(B_HORIZONTAL, 8).Add(fSlider).Add(fVolLabel).End()
			.Add(fBar)
			.AddGroup(B_HORIZONTAL, 8).Add(fSpinner).Add(fCountLabel).AddGlue().End()
			.AddGlue();
		Track("name", fName); Track("hello", fHello); Track("flavour", flavour); Track("flavourlabel", fFlavourLabel);
		Track("slider", fSlider); Track("vollabel", fVolLabel); Track("statusbar", fBar); Track("spinner", fSpinner);
		Track("countlabel", fCountLabel);
		return page;
	}

	BView* _DataPage()
	{
		BView* page = new BView("Data", B_WILL_DRAW);
		fList = new BListView("list", B_SINGLE_SELECTION_LIST);
		for (int i = 0; i < 40; i++) { char b[32]; snprintf(b, sizeof(b), "Item %02d", i); fList->AddItem(new BStringItem(b)); }
		fList->SetSelectionMessage(new BMessage(kPicked));
		BScrollView* ls = new BScrollView("listscroll", fList, 0, false, true, B_FANCY_BORDER);
		fText = new BTextView("text", B_WILL_DRAW | B_NAVIGABLE);
		fText->SetViewColor(255, 255, 255);
		fText->SetText(kIntro);
		BScrollView* ts = new BScrollView("textscroll", fText, 0, false, true, B_FANCY_BORDER);
		fSplit = new BSplitView(B_HORIZONTAL, 6);
		fSplit->AddChild(ls, 1.0f); fSplit->AddChild(ts, 2.0f);
		BLayoutBuilder::Group<>(page, B_VERTICAL, 0).SetInsets(10, 10, 10, 10).Add(fSplit);
		Track("list", fList); Track("text", fText); Track("listscroll", ls); Track("textscroll", ts);
		return page;
	}

	BView* _ExtrasPage()
	{
		BView* page = new BView("Extras", B_WILL_DRAW);
		fColour = new BColorControl(B_ORIGIN, B_CELLS_32x8, 5, "colour", new BMessage(kColour));
		fColour->SetValue((rgb_color){60, 120, 200, 255});
		fSwatch = new BView("swatch", B_WILL_DRAW); fSwatch->SetViewColor(60, 120, 200);
		fSwatch->SetExplicitSize(BSize(96, 40));
		BButton* prev = new BButton("prev", "<", new BMessage(kPrev));
		BButton* next = new BButton("next", ">", new BMessage(kNext));
		BView* cardHost = new BView("cardhost", B_WILL_DRAW);
		fCards = new BCardLayout();
		cardHost->SetLayout(fCards);
		cardHost->AddChild(new BStringView("c0", "Card 0 of a BCardLayout"));
		cardHost->AddChild(new BStringView("c1", "Card 1: the second card"));
		cardHost->AddChild(new BStringView("c2", "Card 2: and the third"));
		BLayoutBuilder::Group<>(page, B_VERTICAL, 8).SetInsets(10, 10, 10, 10)
			.AddGroup(B_HORIZONTAL, 8).Add(fColour).Add(fSwatch).AddGlue().End()
			.AddGroup(B_HORIZONTAL, 8).Add(prev).Add(cardHost).Add(next).AddGlue().End()
			.AddGlue();
		Track("colour", fColour); Track("swatch", fSwatch); Track("prev", prev); Track("next", next);
		Track("cardhost", cardHost);
		return page;
	}

	BMenuBar* fMenuBar; BTabView* fTabs; BStringView* fStatus; BRadioButton* fMedium; BOptionPopUp* fFlavour; BSplitView* fSplit;
	int fPresses = 0;
	BStringView *fPressLabel, *fAlertLabel, *fStyleLabel, *fSizeLabel, *fHello, *fFlavourLabel, *fVolLabel, *fCountLabel;
	BCheckBox *fBold, *fItalic; BTextControl* fName; BSlider* fSlider; BStatusBar* fBar; BSpinner* fSpinner;
	BListView* fList; BTextView* fText; BColorControl* fColour; BView* fSwatch; BCardLayout* fCards;
	struct Tracked { std::string name; BView* view; int page; };
	std::vector<Tracked> fNames;
	int fPage = -1;		// the tab the controls being built live on (-1: the window itself)
};

class KSApp : public BApplication {
public:
	KSApp() : BApplication("application/x-vnd.aurora-kitchensink") {}
	void ReadyToRun() override
	{
		KSWindow* w = new KSWindow;
		w->Show();
		if (getenv("KS_REPORT")) std::thread([w] { snooze(700000); w->Report(); }).detach();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

// The server counts every opcode it did not serve (and every one that was answered B_ERROR). A showcase should say 0.
int main()
{
	KSApp app; app.Run();
	int replies = 0, n = aurora::unhandled_opcodes(&replies);
	printf("UNHANDLED %d (%d of them asked for a reply)\n", n, replies); fflush(stdout);
	return 0;
}
