// Aurora file browser, a BeOS application on Haiku's real Interface Kit.
//
// Shape (see README.md next to this file): a directory is ONE kind of view. The list is read straight from the
// filesystem (readdir + lstat), because indexd has no filesystem scan and only knows files Aurora itself made.
// The index is used for ENRICHMENT only: select a file and the right pane asks `aurora query show FILE --json`
// what made it, what it was derived from and what it was used in. It never decides what is listed.
//
// Enter / double-click: a directory is entered; a file is handed to Aurora's launcher (`aurora open FILE`, which
// picks a verb from the MIME type and the manifests' `accepts`). Backspace or Alt+Up (Command+Up) goes up.
//
// Environment (all optional): BROWSER_DIR start directory (or argv[1]); AURORA_BIN the launcher (default "aurora");
// AURORA_SOCKET passed to it as --socket; AURORA_TERMINAL / BROWSER_NO_TERMINAL see Open(); BROWSER_LOG=1 prints
// what the pane shows; BROWSER_SELECT=name selects that entry at start; HELLO_QUIT_AFTER_MS quits for scripted runs.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <ListView.h>
#include <StringItem.h>
#include <ScrollView.h>
#include <StringView.h>
#include <Font.h>
#include <Message.h>

#include <algorithm>
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <string>
#include <sys/stat.h>
#include <sys/wait.h>
#include <thread>
#include <time.h>
#include <unistd.h>
#include <vector>

namespace {

enum { kSelected = 'bsel', kInvoked = 'binv', kGoUp = 'bup_', kProv = 'bprv' };

// ---- a small JSON reader, enough for `aurora query show --json` -------------------------------------------------
struct J {
	enum T { Null, Bool, Num, Str, Arr, Obj } t = Null;
	std::string s; double n = 0; bool b = false;
	std::vector<J> a; std::vector<std::pair<std::string, J> > o;
	const J* get(const char* k) const { for (size_t i = 0; i < o.size(); i++) if (o[i].first == k) return &o[i].second; return NULL; }
	std::string str(const char* k) const { const J* v = get(k); return v && v->t == Str ? v->s : std::string(); }
};
struct JP {
	const char* p; const char* e; bool ok = true;
	void ws() { while (p < e && (*p == ' ' || *p == '\n' || *p == '\t' || *p == '\r')) p++; }
	J val()
	{
		J v; ws();
		if (p >= e) { ok = false; return v; }
		if (*p == '{') {
			v.t = J::Obj; p++; ws();
			if (p < e && *p == '}') { p++; return v; }
			while (ok) {
				ws(); J k = val(); if (k.t != J::Str) { ok = false; break; }
				ws(); if (p >= e || *p != ':') { ok = false; break; } p++;
				J x = val(); v.o.push_back(std::make_pair(k.s, x)); ws();
				if (p < e && *p == ',') { p++; continue; }
				if (p < e && *p == '}') { p++; break; }
				ok = false;
			}
		} else if (*p == '[') {
			v.t = J::Arr; p++; ws();
			if (p < e && *p == ']') { p++; return v; }
			while (ok) {
				v.a.push_back(val()); ws();
				if (p < e && *p == ',') { p++; continue; }
				if (p < e && *p == ']') { p++; break; }
				ok = false;
			}
		} else if (*p == '"') {
			v.t = J::Str; p++;
			while (p < e && *p != '"') {
				if (*p == '\\' && p + 1 < e) {
					p++;
					switch (*p) { case 'n': v.s += '\n'; break; case 't': v.s += '\t'; break; case 'u': v.s += '?'; p += 4; break; default: v.s += *p; }
					p++;
				} else v.s += *p++;
			}
			if (p >= e) ok = false; else p++;
		} else if (!strncmp(p, "null", 4)) { p += 4; }
		else if (!strncmp(p, "true", 4)) { v.t = J::Bool; v.b = true; p += 4; }
		else if (!strncmp(p, "false", 5)) { v.t = J::Bool; p += 5; }
		else { char* end; v.n = strtod(p, &end); if (end == p) ok = false; v.t = J::Num; p = end; }
		return v;
	}
};

std::string Base(const std::string& p) { size_t i = p.rfind('/'); return i == std::string::npos ? p : p.substr(i + 1); }

// ---- the index, as enrichment ----------------------------------------------------------------------------------
enum ProvState { kProvNone, kProvUnavailable, kProvUnknown, kProvKnown };
struct Prov { ProvState state = kProvNone; std::vector<std::string> lines; };

std::string AuroraBin() { const char* b = getenv("AURORA_BIN"); return b && *b ? b : "aurora"; }

// Run argv with stdout and stderr captured (no shell: the path is an argv element, never parsed). 3 s ceiling.
bool Capture(const std::vector<std::string>& argv, std::string* out, std::string* err, int* status)
{
	int po[2], pe[2];
	if (pipe(po) || pipe(pe)) return false;
	pid_t pid = fork();
	if (pid < 0) return false;
	if (pid == 0) {
		dup2(po[1], 1); dup2(pe[1], 2); close(po[0]); close(pe[0]); close(po[1]); close(pe[1]);
		int nul = open("/dev/null", O_RDONLY); dup2(nul, 0);
		std::vector<char*> a; for (size_t i = 0; i < argv.size(); i++) a.push_back(const_cast<char*>(argv[i].c_str())); a.push_back(NULL);
		execvp(a[0], a.data());
		_exit(127);
	}
	close(po[1]); close(pe[1]);
	struct pollfd fds[2] = {{po[0], POLLIN, 0}, {pe[0], POLLIN, 0}};
	time_t deadline = time(NULL) + 3;
	char buf[4096];
	while ((fds[0].fd >= 0 || fds[1].fd >= 0) && time(NULL) < deadline) {
		if (poll(fds, 2, 200) < 0 && errno != EINTR) break;
		for (int i = 0; i < 2; i++) {
			if (fds[i].fd < 0 || !(fds[i].revents & (POLLIN | POLLHUP))) continue;
			ssize_t n = read(fds[i].fd, buf, sizeof(buf));
			if (n > 0) (i ? err : out)->append(buf, n); else { close(fds[i].fd); fds[i].fd = -1; }
		}
	}
	for (int i = 0; i < 2; i++) if (fds[i].fd >= 0) close(fds[i].fd);
	int st = 0;
	if (waitpid(pid, &st, WNOHANG) == 0) { kill(pid, SIGKILL); waitpid(pid, &st, 0); *status = -1; return true; }
	*status = WIFEXITED(st) ? WEXITSTATUS(st) : -1;
	return true;
}

// Ask indexd about one file. Three answers, kept distinct on purpose: it could not be asked, it was asked and
// knows nothing (which says nothing about whether Aurora made the file), or it knows.
Prov QueryIndex(const std::string& path)
{
	Prov r;
	// The launcher's flags (--socket, --json, --timeout) come after the subcommand, and the path is the one positional.
	std::vector<std::string> argv(1, AuroraBin());
	argv.push_back("query"); argv.push_back("show"); argv.push_back("--json"); argv.push_back("--timeout"); argv.push_back("2s");
	if (const char* s = getenv("AURORA_SOCKET")) { argv.push_back("--socket"); argv.push_back(s); }
	argv.push_back(path);
	std::string out, err; int st = 0;
	if (!Capture(argv, &out, &err, &st) || st == 127 || st == -1) {
		r.state = kProvUnavailable;
		r.lines.push_back("The index could not be asked.");
		r.lines.push_back(st == 127 ? "`" + AuroraBin() + "` was not found." : st == -1 ? "`aurora query show` timed out." : "could not run `aurora`.");
		return r;
	}
	JP jp{out.c_str(), out.c_str() + out.size()};
	J v = jp.val();
	const J* vers = v.t == J::Obj ? v.get("versions") : NULL;
	if (!jp.ok || v.t != J::Obj || !(vers == NULL || vers->t == J::Arr || vers->t == J::Null)) {
		r.state = kProvUnavailable;
		r.lines.push_back("The index could not be asked.");
		std::string line = err.substr(0, err.find('\n'));
		r.lines.push_back(line.empty() ? "aurora query show gave no answer (exit " + std::to_string(st) + ")." : line);
		return r;
	}
	if (!vers || vers->a.empty()) {
		r.state = kProvUnknown;
		r.lines.push_back("The index knows nothing about this file.");
		r.lines.push_back("That is not a statement about the file: indexd only");
		r.lines.push_back("knows files whose creation it saw on the bus.");
		const J* used = v.get("used_in");
		if (used && used->t == J::Arr && !used->a.empty()) {
			r.lines.push_back("");
			r.lines.push_back("Used as an input to:");
			for (size_t i = 0; i < used->a.size(); i++) r.lines.push_back("  " + Base(used->a[i].s));
		}
		return r;
	}
	const J& last = vers->a.back();
	r.state = kProvKnown;
	r.lines.push_back("Made by " + last.str("app") + " " + last.str("verb"));
	const J* in = last.get("inputs");
	if (in && in->t == J::Arr && !in->a.empty()) {
		r.lines.push_back("Derived from:");
		for (size_t i = 0; i < in->a.size(); i++) r.lines.push_back("  " + in->a[i].s);
	} else
		r.lines.push_back("Derived from: (none recorded)");
	std::string dom = last.str("domain");
	r.lines.push_back("Domain: " + (dom.empty() ? std::string("(none recorded)") : dom));
	r.lines.push_back("Indexed writes: " + std::to_string(vers->a.size()) + ", last seen " + last.str("observed_at").substr(0, 19));
	const J* used = v.get("used_in");
	if (used && used->t == J::Arr && !used->a.empty()) {
		r.lines.push_back("Used as an input to:");
		for (size_t i = 0; i < used->a.size(); i++) r.lines.push_back("  " + used->a[i].s);
	} else
		r.lines.push_back("Used in: nothing recorded");
	return r;
}

// ---- directory model --------------------------------------------------------------------------------------------
struct Entry { std::string name; bool dir; off_t size; time_t mtime; };

bool ReadDir(const std::string& path, std::vector<Entry>* out, std::string* err)
{
	DIR* d = opendir(path.c_str());
	if (!d) { *err = strerror(errno); return false; }
	while (dirent* e = readdir(d)) {
		if (!strcmp(e->d_name, ".") || !strcmp(e->d_name, "..")) continue;
		struct stat st;
		std::string full = path + (path == "/" ? "" : "/") + e->d_name;
		Entry x{e->d_name, false, 0, 0};
		if (lstat(full.c_str(), &st) == 0) { x.dir = S_ISDIR(st.st_mode); x.size = st.st_size; x.mtime = st.st_mtime; }
		out->push_back(x);
	}
	closedir(d);
	std::sort(out->begin(), out->end(), [](const Entry& a, const Entry& b) {
		if (a.dir != b.dir) return a.dir;
		return strcasecmp(a.name.c_str(), b.name.c_str()) < 0;
	});
	return true;
}

std::string HumanSize(off_t n)
{
	char b[32];
	if (n < 1024) snprintf(b, sizeof(b), "%lld bytes", (long long)n);
	else if (n < 1024 * 1024) snprintf(b, sizeof(b), "%.1f KiB", n / 1024.0);
	else snprintf(b, sizeof(b), "%.1f MiB", n / 1048576.0);
	return b;
}

// ---- views ------------------------------------------------------------------------------------------------------
class Pane : public BView {
public:
	Pane(BRect frame) : BView(frame, "pane", B_FOLLOW_ALL, B_WILL_DRAW) { SetViewColor(250, 250, 250); }
	void Set(const std::vector<std::pair<bool, std::string> >& l) { fLines = l; Invalidate(); }
	void Draw(BRect) override
	{
		SetHighColor(160, 160, 160);
		StrokeRect(Bounds());
		BFont plain(be_plain_font), bold(be_bold_font);
		float y = 20;
		for (size_t i = 0; i < fLines.size(); i++) {
			SetFont(fLines[i].first ? &bold : &plain);
			SetHighColor(fLines[i].first ? ui_color(B_PANEL_TEXT_COLOR) : ui_color(B_PANEL_TEXT_COLOR));
			DrawString(fLines[i].second.c_str(), BPoint(10, y));
			y += 17;
		}
	}
private:
	std::vector<std::pair<bool, std::string> > fLines;
};

class FileList : public BListView {
public:
	FileList(BRect f) : BListView(f, "files", B_SINGLE_SELECTION_LIST, B_FOLLOW_ALL) {}
	void KeyDown(const char* bytes, int32 n) override
	{
		if (bytes[0] == B_BACKSPACE) { Window()->PostMessage(kGoUp); return; }
		BListView::KeyDown(bytes, n);
	}
};

class BrowserWindow : public BWindow {
public:
	BrowserWindow(const std::string& dir)
		: BWindow(BRect(0, 0, 619, 379), "Aurora files", B_TITLED_WINDOW, 0), fSeq(0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewColor(ui_color(B_PANEL_BACKGROUND_COLOR));
		AddChild(bg);
		fPath = new BStringView(BRect(10, 8, 600, 26), "path", "");
		fPath->SetFont(be_bold_font);
		bg->AddChild(fPath);
		fList = new FileList(BRect(0, 0, 280, 335));
		fList->SetSelectionMessage(new BMessage(kSelected));
		fList->SetInvocationMessage(new BMessage(kInvoked));
		fList->SetTarget(this);
		BScrollView* sv = new BScrollView("sv", fList, B_FOLLOW_TOP_BOTTOM | B_FOLLOW_LEFT, 0, false, true, B_FANCY_BORDER);
		sv->MoveTo(10, 34);
		bg->AddChild(sv);
		fPane = new Pane(BRect(316, 34, 609, 369));
		bg->AddChild(fPane);
		AddShortcut(B_UP_ARROW, B_COMMAND_KEY, new BMessage(kGoUp));	// Command is Alt: Alt+Up = parent, as in Tracker
		Navigate(dir);
		fList->MakeFocus(true);
		if (const char* s = getenv("BROWSER_SELECT"))
			for (int i = 0; i < (int)fEntries.size(); i++)
				if (fEntries[i].first == s) { fList->Select(i); break; }
	}

	void MessageReceived(BMessage* m) override
	{
		switch (m->what) {
			case kSelected: Selected(fList->CurrentSelection()); break;
			case kInvoked: Invoke(m->GetInt32("index", -1)); break;
			case kGoUp: GoUp(); break;
			case kProv: ShowProv(m); break;
			default: BWindow::MessageReceived(m);
		}
	}
	bool QuitRequested() override { be_app->PostMessage(B_QUIT_REQUESTED); return true; }

private:
	std::string Full(int i) const { return fDir + (fDir == "/" ? "" : "/") + fEntries[i].first; }

	void Navigate(const std::string& dir)
	{
		char* rp = realpath(dir.c_str(), NULL);
		std::string path = rp ? rp : dir; free(rp);
		std::vector<Entry> es; std::string err;
		fList->MakeEmpty();
		fEntries.clear(); fMeta.clear();
		fDir = path;
		fPath->SetText(path.c_str());
		if (path != "/") { fList->AddItem(new BStringItem("../")); fEntries.push_back(std::make_pair(std::string(".."), true)); fMeta.push_back(Entry{"..", true, 0, 0}); }
		if (!ReadDir(path, &es, &err)) {
			fList->AddItem(new BStringItem(("(cannot read: " + err + ")").c_str()));
			fEntries.push_back(std::make_pair(std::string(), false)); fMeta.push_back(Entry{"", false, 0, 0});
		}
		for (size_t i = 0; i < es.size(); i++) {
			fList->AddItem(new BStringItem((es[i].name + (es[i].dir ? "/" : "")).c_str()));
			fEntries.push_back(std::make_pair(es[i].name, es[i].dir)); fMeta.push_back(es[i]);
		}
		std::vector<std::pair<bool, std::string> > l;
		l.push_back(std::make_pair(true, std::to_string(es.size()) + " items"));
		fPane->Set(l);
		if (getenv("BROWSER_LOG")) { printf("DIR %s (%d items)\n", path.c_str(), (int)es.size()); fflush(stdout); }
	}

	void GoUp() { if (fDir != "/") { std::string old = Base(fDir); Navigate(fDir.substr(0, fDir.rfind('/') == 0 ? 1 : fDir.rfind('/'))); Reselect(old); } }
	void Reselect(const std::string& name) { for (int i = 0; i < (int)fEntries.size(); i++) if (fEntries[i].first == name) { fList->Select(i); fList->ScrollToSelection(); break; } }

	void Selected(int i)
	{
		if (i < 0 || i >= (int)fEntries.size() || fEntries[i].first.empty()) return;
		const Entry& e = fMeta[i];
		if (getenv("BROWSER_LOG")) { printf("SEL %s\n", e.name.c_str()); fflush(stdout); }	// lets a scripted run wait for the selection to land
		std::vector<std::pair<bool, std::string> > l;
		if (e.name == "..") { l.push_back(std::make_pair(true, "Parent directory")); fPane->Set(l); return; }
		std::string full = Full(i);
		char t[32]; struct tm tmv; localtime_r(&e.mtime, &tmv); strftime(t, sizeof(t), "%Y-%m-%d %H:%M", &tmv);
		l.push_back(std::make_pair(true, e.name + (e.dir ? "/" : "")));
		l.push_back(std::make_pair(false, e.dir ? "Directory" : "Size: " + HumanSize(e.size)));
		l.push_back(std::make_pair(false, std::string("Modified: ") + t));
		l.push_back(std::make_pair(false, ""));
		if (e.dir) {
			l.push_back(std::make_pair(false, "Press Enter to open."));
			fPane->Set(l);
			return;
		}
		l.push_back(std::make_pair(true, "Provenance"));
		l.push_back(std::make_pair(false, "asking the index..."));
		fPane->Set(l);
		fHeader = l; fHeader.pop_back(); fHeader.pop_back();
		int seq = ++fSeq;
		BMessenger me(this);
		std::thread([me, full, seq] {
			Prov p = QueryIndex(full);
			BMessage m(kProv);
			m.AddInt32("seq", seq); m.AddInt32("state", p.state); m.AddString("path", full.c_str());
			for (size_t k = 0; k < p.lines.size(); k++) m.AddString("line", p.lines[k].c_str());
			BMessenger(me).SendMessage(&m);
		}).detach();
	}

	void ShowProv(BMessage* m)
	{
		if (m->GetInt32("seq", 0) != fSeq) return;	// the selection moved on
		std::vector<std::pair<bool, std::string> > l = fHeader;
		l.push_back(std::make_pair(true, "Provenance"));
		const char* s; int32 state = m->GetInt32("state", 0);
		for (int32 k = 0; m->FindString("line", k, &s) == B_OK; k++) l.push_back(std::make_pair(false, std::string(s)));
		fPane->Set(l);
		if (getenv("BROWSER_LOG")) {
			printf("PROV state=%d path=%s\n", (int)state, m->GetString("path", ""));
			for (size_t k = 0; k < l.size(); k++) printf("PANE %s\n", l[k].second.c_str());
			printf("PANE-END\n"); fflush(stdout);
		}
	}

	void Invoke(int i)
	{
		if (i < 0 || i >= (int)fEntries.size() || fEntries[i].first.empty()) return;
		if (fEntries[i].second) {
			if (fEntries[i].first == "..") GoUp(); else Navigate(Full(i));
		} else
			Open(Full(i));
	}

	// Hand a file to Aurora's launcher. `aurora open FILE` shows a verb picker on a terminal, so it runs in one:
	// $AURORA_TERMINAL, else the first of ghostty foot kitty alacritty konsole xterm on PATH. BROWSER_NO_TERMINAL=1 runs
	// the launcher directly (scripted runs). A verb is never chosen here: if one is wanted it needs --app too, because
	// a bare verb name such as resize@v1 is offered by more than one app.
	void Open(const std::string& file)
	{
		std::vector<std::string> argv;
		if (!getenv("BROWSER_NO_TERMINAL")) {
			std::string term = getenv("AURORA_TERMINAL") ? getenv("AURORA_TERMINAL") : "";
			if (term.empty()) {
				static const char* c[] = {"ghostty", "foot", "kitty", "alacritty", "konsole", "xterm"};
				for (size_t k = 0; k < sizeof(c) / sizeof(c[0]) && term.empty(); k++) {
					std::string path = getenv("PATH") ? getenv("PATH") : "";
					for (size_t a = 0; a <= path.size();) {
						size_t b = path.find(':', a); if (b == std::string::npos) b = path.size();
						std::string cand = path.substr(a, b - a) + "/" + c[k];
						if (access(cand.c_str(), X_OK) == 0) { term = cand; break; }
						a = b + 1;
					}
				}
			}
			if (!term.empty()) { argv.push_back(term); argv.push_back("-e"); }
		}
		argv.push_back(AuroraBin());
		argv.push_back("open");
		if (const char* s = getenv("AURORA_SOCKET")) { argv.push_back("--socket"); argv.push_back(s); }
		argv.push_back(file);
		printf("OPEN");
		for (size_t k = 0; k < argv.size(); k++) printf(" %s", argv[k].c_str());
		printf("\n"); fflush(stdout);
		pid_t pid = fork();
		if (pid == 0) {
			setsid();
			if (getenv("BROWSER_NO_TERMINAL")) { /* keep stdio so a script can see the launcher */ }
			else { int nul = open("/dev/null", O_RDWR); dup2(nul, 0); dup2(nul, 1); dup2(nul, 2); }
			std::vector<char*> a; for (size_t k = 0; k < argv.size(); k++) a.push_back(const_cast<char*>(argv[k].c_str())); a.push_back(NULL);
			execvp(a[0], a.data());
			_exit(127);
		}
		if (pid > 0) std::thread([pid] { int st; waitpid(pid, &st, 0); }).detach();
	}

	BStringView* fPath; FileList* fList; Pane* fPane;
	std::string fDir;
	std::vector<std::pair<std::string, bool> > fEntries;
	std::vector<Entry> fMeta;
	std::vector<std::pair<bool, std::string> > fHeader;
	int fSeq;
};

class BrowserApp : public BApplication {
public:
	BrowserApp(const std::string& dir) : BApplication("application/x-vnd.aurora-browser"), fDir(dir) {}
	void ReadyToRun() override
	{
		(new BrowserWindow(fDir))->Show();
		if (const char* s = getenv("HELLO_QUIT_AFTER_MS")) {
			long ms = atol(s);
			std::thread([ms] { snooze(ms * 1000LL); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
		}
	}
private:
	std::string fDir;
};

}	// namespace

int main(int argc, char** argv)
{
	std::string dir = argc > 1 ? argv[1] : getenv("BROWSER_DIR") ? getenv("BROWSER_DIR") : getenv("HOME") ? getenv("HOME") : "/";
	signal(SIGPIPE, SIG_IGN);
	BrowserApp app(dir);
	app.Run();
	return 0;
}
