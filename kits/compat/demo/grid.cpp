// BColumnListView (Haiku's ColumnListView.cpp and ColumnTypes.cpp, compiled in place): a data grid with typed columns
// (string, integer, size), a title row with sort indicators, resizable columns, and a nested row (a parent with
// children). GRID_SORT=N sorts by column N at start (the harness renders its expectation with it).
// GRID_DESC=1 sorts descending, GRID_W0=N makes column 0 N wide, GRID_EXPAND=1 opens the nested row (each is how the harness
// renders the expectation of a click, a drag or an expander without any pointer).
// GRID_REPORT=1 prints "GRID name l,t,r,b" (window coordinates) and "ROW title y" once laid out.
#include <time.h>
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <ColumnListView.h>
#include <ColumnTypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <thread>
#include "server.h"

class GridWindow : public BWindow {
public:
	GridWindow() : BWindow(BRect(0, 0, 459, 239), "Grid", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewUIColor(B_PANEL_BACKGROUND_COLOR);
		AddChild(bg);
		BRect r = Bounds(); r.InsetBy(10, 10);
		fGrid = new BColumnListView(r, "grid", B_FOLLOW_ALL, B_WILL_DRAW | B_NAVIGABLE, B_FANCY_BORDER, true);
		fGrid->AddColumn(new BStringColumn("Name", 150, 40, 300, B_TRUNCATE_END), 0);
		fGrid->AddColumn(new BIntegerColumn("Count", 70, 30, 150, B_ALIGN_RIGHT), 1);
		fGrid->AddColumn(new BSizeColumn("Size", 90, 30, 200, B_ALIGN_RIGHT), 2);
		static const struct { const char* n; int c; int64 s; } rows[] = {
			{ "kernel", 12, 4096 }, { "interface", 340, 1048576 }, { "app", 7, 123 }, { "support", 89, 7340032 }, { "storage", 21, 65536 },
		};
		for (auto& d : rows) {
			BRow* row = new BRow();
			row->SetField(new BStringField(d.n), 0);
			row->SetField(new BIntegerField(d.c), 1);
			row->SetField(new BSizeField(d.s), 2);
			fGrid->AddRow(row);
			if (!strcmp(d.n, "interface")) {		// a nested row: the parent gets an expander triangle
				BRow* child = new BRow();
				child->SetField(new BStringField("Button.cpp"), 0);
				child->SetField(new BIntegerField(1), 1);
				child->SetField(new BSizeField(9000), 2);
				fGrid->AddRow(child, row);
				fParent = row;
			}
		}
		bg->AddChild(fGrid);
		if (const char* s = getenv("GRID_W0")) fGrid->ColumnAt(0)->SetWidth(atof(s));
		if (const char* s = getenv("GRID_SORT")) fGrid->SetSortColumn(fGrid->ColumnAt(atoi(s)), false, getenv("GRID_DESC") == NULL);
		if (getenv("GRID_EXPAND")) fGrid->ExpandOrCollapse(fParent, true);
		fGrid->MakeFocus(true);
	}
	void Report()
	{
		Lock();
		BRect f = fGrid->Frame(); printf("GRID grid %.0f,%.0f,%.0f,%.0f\n", f.left, f.top, f.right, f.bottom);
		for (int i = 0; i < 3; i++) printf("COL %d %.0f\n", i, fGrid->ColumnAt(i)->Width());
		fflush(stdout); Unlock();
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }
	BColumnListView* fGrid; BRow* fParent;
};

class GridApp : public BApplication {
public:
	GridApp() : BApplication("application/x-vnd.aurora-grid") {}
	void ReadyToRun() override
	{
		GridWindow* w = new GridWindow; w->Show();
		if (getenv("GRID_REPORT")) std::thread([w] { snooze(700000); w->Report(); }).detach();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
};

int main()
{
	GridApp app;
	app.Run();
	int replies = 0, n = aurora::unhandled_opcodes(&replies);
	printf("UNHANDLED %d (%d of them asked for a reply)\n", n, replies); fflush(stdout);
	return 0;
}
