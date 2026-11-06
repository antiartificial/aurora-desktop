// Messaging-core tests: BMessage, BHandler, BLooper, BMessenger, BApplication.
// Cross-process tests re-exec this binary in "--child" mode, sharing only the port namespace.
#include <Application.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

static int failures, checks;
#define CHECK(c) do { checks++; if (!(c)) { failures++; \
	printf("  FAIL %s:%d: %s\n", __FILE__, __LINE__, #c); } } while (0)
#define CHECK_EQ(a, b) do { long long _a = (a), _b = (b); checks++; if (_a != _b) { \
	failures++; printf("  FAIL %s:%d: %s == %s (%lld vs %lld)\n", __FILE__, __LINE__, \
	#a, #b, _a, _b); } } while (0)
#define RUN(t) do { printf("[ RUN ] %s\n", #t); int f0 = failures; t(); \
	printf("[ %s ] %s\n", failures == f0 ? " OK " : "FAIL", #t); } while (0)

enum { kAdd = 'ADD_', kSum = 'SUM_', kEcho = 'ECHO', kFlip = 'FLIP', kSilent = 'SLNT', kPing = 'PING',
	kQuitSelf = 'QSLF', kBlob = 'BLOB' };

// ---- a handler that answers requests, used locally and across processes
struct Server : BHandler {
	int32 received = 0; bool *deleted;
	Server(bool *d = NULL) : BHandler("server"), deleted(d) {}
	~Server() { if (deleted) *deleted = true; }
	void MessageReceived(BMessage *m) override {
		received++;
		switch (m->what) {
		case kAdd: {
			int32 a = 0, b = 0; m->FindInt32("a", &a); m->FindInt32("b", &b);
			BMessage r(kSum); r.AddInt32("sum", a + b); m->SendReply(&r); break; }
		case kEcho: { BMessage r(*m); r.what = kEcho; m->SendReply(&r); break; }
		case kSilent: break;                       // never replies
		default: BHandler::MessageReceived(m);
		}
	}
};

static void fill(BMessage &m)
{
	m.AddInt8("i8", -8); m.AddInt16("i16", -1600); m.AddInt32("i32", -320000);
	m.AddInt64("i64", -6400000000000LL); m.AddBool("t", true); m.AddBool("f", false);
	m.AddFloat("fl", 1.5f); m.AddDouble("db", -2.25); m.AddString("s", "hello");
	m.AddString("s", "world");
	const uint8 raw[5] = { 0, 1, 0xff, 0, 7 };
	m.AddData("raw", B_RAW_TYPE, raw, sizeof raw);
	BMessage inner('INNR'); inner.AddString("k", "v"); inner.AddInt32("n", 5);
	BMessage deep('DEEP'); deep.AddMessage("inner", &inner);
	m.AddMessage("nest", &deep);
}

// ---- child mode (separate process image)
static int child_roundtrip(char **argv)
{   // read a flattened BMessage from a port, check it, send the bytes back re-flattened
	port_id in = atoi(argv[2]), out = atoi(argv[3]);
	static char buf[4096]; int32 code;
	ssize_t n = read_port_etc(in, &code, buf, sizeof buf, B_RELATIVE_TIMEOUT, 5000000);
	if (n < 0) return 10;
	BMessage m, expect('ROUN'); fill(expect);
	if (m.Unflatten(buf, n) != B_OK) return 11;
	if (!m.Equals(expect)) return 12;
	BMessage deep, inner; const char *k;
	if (m.FindMessage("nest", &deep) != B_OK || deep.FindMessage("inner", &inner) != B_OK) return 13;
	if (inner.FindString("k", &k) != B_OK || strcmp(k, "v")) return 14;
	static char back[4096]; ssize_t fs = m.FlattenedSize();
	if (m.Flatten(back, fs) != B_OK) return 15;
	return write_port(out, 1, back, fs) == B_OK ? 0 : 16;
}

static int child_messenger(char **argv)
{   // no looper here: only a BMessenger aimed at the parent's looper
	BMessenger target(atoi(argv[2]), atoi(argv[3]));
	if (!target.IsValid() || target.IsTargetLocal()) return 20;
	for (int32 i = 0; i < 20; i++) {
		BMessage req(kAdd), reply; req.AddInt32("a", i); req.AddInt32("b", 1000);
		if (target.SendMessage(&req, &reply) != B_OK) return 21;
		int32 sum = 0;
		if (reply.what != kSum || reply.FindInt32("sum", &sum) != B_OK || sum != i + 1000) return 22;
	}
	BMessage big(kEcho), reply; fill(big);                       // whole message through the looper and back
	if (target.SendMessage(&big, &reply) != B_OK || reply.what != kEcho) return 23;
	BMessage expect; fill(expect); expect.what = kEcho;
	if (!reply.Equals(expect)) return 24;
	BMessage silent(kSilent), nr;                                // handler does not reply
	if (target.SendMessage(&silent, &nr) != B_OK || nr.what != B_NO_REPLY) return 25;
	BMessage odd('WHAT'), mnu;                                   // handler does not understand
	if (target.SendMessage(&odd, &mnu) != B_OK || mnu.what != B_MESSAGE_NOT_UNDERSTOOD) return 26;
	return 0;
}

static int child_main(char **argv)
{
	if (!strcmp(argv[1], "--roundtrip")) return child_roundtrip(argv);
	if (!strcmp(argv[1], "--messenger")) return child_messenger(argv);
	return 99;
}

static int run_child(const char *mode, port_id a, port_id b)
{
	char s0[16], s1[16]; snprintf(s0, 16, "%d", a); snprintf(s1, 16, "%d", b);
	pid_t pid = fork();
	if (pid == 0) { execl("/proc/self/exe", "test_app", mode, s0, s1, (char *)NULL); _exit(98); }
	int st = 0; waitpid(pid, &st, 0);
	return WIFEXITED(st) ? WEXITSTATUS(st) : 100 + WTERMSIG(st);
}

// ---- BMessage
static void test_message_types()
{
	BMessage m('TEST'); fill(m);
	int8 i8; int16 i16; int32 i32; int64 i64; bool t = false, f = true; float fl; double db;
	CHECK_EQ(m.FindInt8("i8", &i8), B_OK); CHECK_EQ(i8, -8);
	CHECK_EQ(m.FindInt16("i16", &i16), B_OK); CHECK_EQ(i16, -1600);
	CHECK_EQ(m.FindInt32("i32", &i32), B_OK); CHECK_EQ(i32, -320000);
	CHECK_EQ(m.FindInt64("i64", &i64), B_OK); CHECK_EQ(i64, -6400000000000LL);
	CHECK_EQ(m.FindBool("t", &t), B_OK); CHECK(t);
	CHECK_EQ(m.FindBool("f", &f), B_OK); CHECK(!f);
	CHECK_EQ(m.FindFloat("fl", &fl), B_OK); CHECK(fl == 1.5f);
	CHECK_EQ(m.FindDouble("db", &db), B_OK); CHECK(db == -2.25);
	const char *s; CHECK_EQ(m.FindString("s", &s), B_OK); CHECK(!strcmp(s, "hello"));
	const void *d; ssize_t dn; CHECK_EQ(m.FindData("raw", B_RAW_TYPE, &d, &dn), B_OK);
	CHECK_EQ(dn, 5); CHECK(((const uint8 *)d)[2] == 0xff && ((const uint8 *)d)[3] == 0);
	BMessage deep, inner; const char *k;
	CHECK_EQ(m.FindMessage("nest", &deep), B_OK); CHECK_EQ(deep.what, 'DEEP');
	CHECK_EQ(deep.FindMessage("inner", &inner), B_OK); CHECK_EQ(inner.what, 'INNR');
	CHECK_EQ(inner.FindString("k", &k), B_OK); CHECK(!strcmp(k, "v"));
	type_code tc; int32 cnt;
	CHECK_EQ(m.GetInfo("s", &tc, &cnt), B_OK); CHECK_EQ(tc, B_STRING_TYPE); CHECK_EQ(cnt, 2);
	CHECK_EQ(m.GetInfo("nest", &tc, &cnt), B_OK); CHECK_EQ(tc, B_MESSAGE_TYPE);
	CHECK_EQ(m.CountNames(B_ANY_TYPE), 11); CHECK_EQ(m.CountNames(B_STRING_TYPE), 1);
	CHECK_EQ(m.RemoveName("i8"), B_OK); CHECK_EQ(m.FindInt8("i8", &i8), B_NAME_NOT_FOUND);
	CHECK_EQ(m.RemoveName("i8"), B_NAME_NOT_FOUND);
	CHECK(m.what == 'TEST' && 'TEST' == 0x54455354);   // four-character constants
}

static void test_message_errors()
{
	BMessage m; m.AddInt32("n", 1); m.AddString("s", "x");
	int32 i; int64 l; const char *s; bool b; BMessage sub;
	CHECK_EQ(m.FindInt64("n", &l), B_BAD_TYPE);
	CHECK_EQ(m.FindString("n", &s), B_BAD_TYPE);
	CHECK_EQ(m.FindInt32("s", &i), B_BAD_TYPE);
	CHECK_EQ(m.FindBool("n", &b), B_BAD_TYPE);
	CHECK_EQ(m.FindMessage("s", &sub), B_BAD_TYPE);
	CHECK_EQ(m.FindInt32("missing", &i), B_NAME_NOT_FOUND);
	CHECK_EQ(m.FindString("missing", &s), B_NAME_NOT_FOUND);
	CHECK_EQ(m.FindInt32("n", 1, &i), B_BAD_INDEX);
	CHECK_EQ(m.AddString("n", "mix"), B_BAD_TYPE);          // a name holds one type
	CHECK_EQ(m.AddString(NULL, "x"), B_BAD_VALUE);
	CHECK_EQ(m.AddData("d", B_RAW_TYPE, NULL, 4), B_BAD_VALUE);
	CHECK_EQ(m.GetInfo("missing", NULL, NULL), B_NAME_NOT_FOUND);
	// multiple items per name, in order, by index
	BMessage q; for (int32 k = 0; k < 50; k++) q.AddInt32("v", k * k);
	bool ok = true; for (int32 k = 0; k < 50; k++) ok &= q.FindInt32("v", k, &i) == B_OK && i == k * k;
	CHECK(ok); int32 cnt; q.GetInfo("v", NULL, &cnt); CHECK_EQ(cnt, 50);
	CHECK_EQ(q.FindInt32("v", 50, &i), B_BAD_INDEX);
	// malformed flat data is rejected, never crashes
	BMessage good('GOOD'); fill(good); std::string flat(good.FlattenedSize(), 0);
	good.Flatten(&flat[0], flat.size());
	int rejected = 0;
	for (size_t cut = 0; cut < flat.size(); cut += 7) { BMessage x; rejected += x.Unflatten(flat.data(), cut) != B_OK; }
	CHECK_EQ(rejected, (int)((flat.size() + 6) / 7));
	flat[0] = 'X'; BMessage bad; CHECK_EQ(bad.Unflatten(flat.data(), flat.size()), B_BAD_DATA);
	CHECK_EQ(good.Flatten(&flat[0], 10), B_BAD_VALUE);
}

static void test_flatten_roundtrip()
{
	BMessage m('FLAT'); fill(m);
	ssize_t n = m.FlattenedSize(); CHECK(n > 100);
	std::string a(n, 0), b(n, 0);
	CHECK_EQ(m.Flatten(&a[0], n), B_OK);
	BMessage u; CHECK_EQ(u.Unflatten(a.data(), n), B_OK);
	CHECK(u.Equals(m)); CHECK_EQ(u.what, 'FLAT');
	CHECK_EQ(u.FlattenedSize(), n); CHECK_EQ(u.Flatten(&b[0], n), B_OK);
	CHECK(a == b);                                           // byte-identical
	BMessage e; std::string ef(e.FlattenedSize(), 0); e.Flatten(&ef[0], ef.size());
	BMessage e2('XXXX'); CHECK_EQ(e2.Unflatten(ef.data(), ef.size()), B_OK); CHECK(e2.Equals(e));
	BMessage c(m); CHECK(c.Equals(m)); c.AddInt32("extra", 1); CHECK(!c.Equals(m));
}

// ---- messages through ports
static void test_port_roundtrip_between_processes()
{
	port_id to = create_port(4, "rt-to"), from = create_port(4, "rt-from");
	BMessage m('ROUN'); fill(m);
	std::string flat(m.FlattenedSize(), 0); m.Flatten(&flat[0], flat.size());
	CHECK_EQ(write_port(to, 'FLAT', flat.data(), flat.size()), B_OK);
	CHECK_EQ(run_child("--roundtrip", to, from), 0);
	static char back[4096]; int32 code;
	ssize_t n = read_port_etc(from, &code, back, sizeof back, B_RELATIVE_TIMEOUT, 1000000);
	CHECK_EQ(n, (long long)flat.size());
	CHECK(n == (ssize_t)flat.size() && memcmp(back, flat.data(), n) == 0);
	BMessage r; CHECK_EQ(r.Unflatten(back, n), B_OK); CHECK(r.Equals(m));
	delete_port(to); delete_port(from);
}

// ---- BLooper / BHandler
struct Recorder : BLooper {
	int32 defaultHits = 0; uint32 lastWhat = 0;
	Recorder() : BLooper("recorder") {}
	void MessageReceived(BMessage *m) override { defaultHits++; lastWhat = m->what; BLooper::MessageReceived(m); }
};
struct Counter : BHandler {
	int32 hits = 0; sem_id sem;
	Counter(sem_id s) : BHandler("counter"), sem(s) {}
	void MessageReceived(BMessage *m) override {
		if (m->what == kPing) { hits++; release_sem(sem); } else BHandler::MessageReceived(m);
	}
};

static void test_looper_dispatch()
{
	sem_id sem = create_sem(0, "done");
	Recorder *l = new Recorder; Counter *a = new Counter(sem), *b = new Counter(sem);
	l->Lock(); l->AddHandler(a); l->AddHandler(b); l->Unlock();
	CHECK_EQ(l->CountHandlers(), 2); CHECK(a->Looper() == l && a->NextHandler() == l);
	CHECK(l->Port() >= 0);
	port_info pi; CHECK_EQ(get_port_info(l->Port(), &pi), B_OK);   // the looper's port is a kits port
	CHECK(l->Run() >= 0);
	CHECK_EQ(l->PostMessage(kPing, a), B_OK); CHECK_EQ(l->PostMessage(kPing, a), B_OK);
	CHECK_EQ(l->PostMessage(kPing, b), B_OK);
	for (int i = 0; i < 3; i++) CHECK_EQ(acquire_sem_etc(sem, 1, B_RELATIVE_TIMEOUT, 2000000), B_OK);
	CHECK_EQ(a->hits, 2); CHECK_EQ(b->hits, 1); CHECK_EQ(l->defaultHits, 0);
	// unmatched: a handler that does not know the code, and a message with no handler
	BMessage unk('UNKN');
	CHECK_EQ(l->PostMessage(&unk, a), B_OK); CHECK_EQ(l->PostMessage('NOHN'), B_OK);
	BMessenger ghost(l->Port(), 987654);                      // token that names no handler
	CHECK_EQ(ghost.SendMessage('GHST'), B_OK);
	BMessage sync('SYNC'), reply; BMessenger self(l, NULL);
	CHECK_EQ(self.SendMessage(&sync, &reply), B_OK);          // barrier: all earlier ones are done
	CHECK_EQ(reply.what, B_MESSAGE_NOT_UNDERSTOOD);
	CHECK_EQ(l->defaultHits, 4); CHECK_EQ(l->lastWhat, 'SYNC');
	// preferred handler receives messages sent to the looper's own address
	l->Lock(); l->SetPreferredHandler(b); l->Unlock();
	CHECK_EQ(l->PostMessage(kPing), B_OK); CHECK_EQ(acquire_sem_etc(sem, 1, B_RELATIVE_TIMEOUT, 2000000), B_OK);
	CHECK_EQ(b->hits, 2);
	BHandler stranger; CHECK_EQ(l->PostMessage(kPing, &stranger), B_MISMATCHED_VALUES);
	l->Quit(); delete_sem(sem);
}

struct Catcher : BHandler {
	uint32 lastWhat = 0; BMessage lastMsg; sem_id sem;
	Catcher(sem_id s) : BHandler("catcher"), sem(s) {}
	void MessageReceived(BMessage *m) override { lastWhat = m->what; lastMsg = *m; release_sem(sem); }
};
struct SelfSender : BHandler {
	BMessenger self;
	SelfSender(BMessenger m) : BHandler("selfsend"), self(m) {}
	void MessageReceived(BMessage *m) override {
		BMessage r('SELF'), rep; r.AddInt32("status", self.SendMessage(&r, &rep));   // would wait on itself
		m->SendReply(&r);
	}
};

static void test_messenger_local()
{
	Server *s = new Server; BLooper *l = new BLooper("srv");
	l->Lock(); l->AddHandler(s); l->Unlock(); l->Run();
	BMessenger m(s, l); CHECK(m.IsValid()); CHECK(m.IsTargetLocal()); CHECK_EQ(m.Team(), getpid());
	BLooper *tl = NULL; CHECK(m.Target(&tl) == s && tl == l);
	BMessage req(kAdd), reply; req.AddInt32("a", 40); req.AddInt32("b", 2);
	CHECK_EQ(m.SendMessage(&req, &reply), B_OK); CHECK_EQ(reply.what, kSum);
	int32 sum = 0; CHECK_EQ(reply.FindInt32("sum", &sum), B_OK); CHECK_EQ(sum, 42);
	BMessage silent(kSilent), nr; CHECK_EQ(m.SendMessage(&silent, &nr), B_OK); CHECK_EQ(nr.what, B_NO_REPLY);
	BMessage dead(kSilent), none;
	BMessenger bogus(123456789, -1); CHECK(!bogus.IsValid());
	CHECK_EQ(bogus.SendMessage(&dead, &none), B_BAD_PORT_ID);
	// async send with a reply address: the reply lands in the named handler
	sem_id sem = create_sem(0, "c"); Catcher *catcher = new Catcher(sem);
	BLooper *l2 = new BLooper("catch"); l2->Lock(); l2->AddHandler(catcher); l2->Unlock(); l2->Run();
	BMessage sreq(kAdd); sreq.AddInt32("a", 1); sreq.AddInt32("b", 2);
	CHECK_EQ(m.SendMessage(&sreq, catcher), B_OK);
	CHECK_EQ(acquire_sem_etc(sem, 1, B_RELATIVE_TIMEOUT, 2000000), B_OK);
	int32 got = 0; CHECK_EQ(catcher->lastWhat, kSum); CHECK_EQ(catcher->lastMsg.FindInt32("sum", &got), B_OK); CHECK_EQ(got, 3);
	// a synchronous send from the target looper's own thread must fail, not deadlock
	l->Lock(); l->AddHandler(new SelfSender(m)); l->Unlock();
	BMessage poke('POKE'), pr; BMessenger toSelf(l->HandlerAt(1), l);
	CHECK_EQ(toSelf.SendMessage(&poke, &pr), B_OK); int32 inner = 0; pr.FindInt32("status", &inner);
	CHECK_EQ(inner, B_ERROR);
	l->Quit(); l2->Quit(); delete_sem(sem);
	CHECK(!m.IsValid());
	(void)got;
}

struct Lifeline : BHandler {
	bool *gone; Lifeline(bool *g) : BHandler("life"), gone(g) {}
	~Lifeline() { *gone = true; }
	void MessageReceived(BMessage *m) override {
		if (m->what == kQuitSelf) Looper()->Quit();      // Quit from inside the loop thread
		else BHandler::MessageReceived(m);
	}
};

static void test_looper_quit()
{
	bool gone = false;
	BLooper *l = new BLooper("q1"); Lifeline *h = new Lifeline(&gone);
	l->Lock(); l->AddHandler(h); l->Unlock();
	thread_id t = l->Run(); port_id p = l->Port(); CHECK(t >= 0);
	CHECK_EQ(find_thread("q1"), t); CHECK_EQ(port_count(p), 0);
	l->Lock(); CHECK(l->IsLocked()); l->Unlock(); CHECK(!l->IsLocked());
	l->Quit();                                                // from main thread: joins and deletes
	CHECK(gone); CHECK(port_count(p) < 0); CHECK(!BMessenger(p, -1).IsValid());
	// self-quit: handler calls Quit() while dispatching
	gone = false; l = new BLooper("q2"); h = new Lifeline(&gone);
	l->Lock(); l->AddHandler(h); l->Unlock();
	t = l->Run(); p = l->Port();
	CHECK_EQ(l->PostMessage(kQuitSelf, h), B_OK);
	status_t rv = -1; CHECK_EQ(wait_for_thread(t, &rv), B_OK);
	CHECK(gone); CHECK(port_count(p) < 0);
	// B_QUIT_REQUESTED goes through QuitRequested() then Quit()
	gone = false; l = new BLooper("q3"); h = new Lifeline(&gone);
	l->Lock(); l->AddHandler(h); l->Unlock();
	t = l->Run(); CHECK_EQ(l->PostMessage(B_QUIT_REQUESTED, h), B_OK);
	CHECK_EQ(wait_for_thread(t, &rv), B_OK); CHECK(gone);
	// a sender blocked on a reply is released when the loop dies (B_NO_REPLY)
	l = new BLooper("q4"); l->Run(); BMessenger m(l, NULL);
	BMessage x('XXXX'), r; CHECK_EQ(m.SendMessage(&x, &r), B_OK); CHECK_EQ(r.what, B_MESSAGE_NOT_UNDERSTOOD);
	l->Quit();
	l = new BLooper("never-run"); p = l->Port(); l->Quit(); CHECK(port_count(p) < 0);
}

static void test_messenger_cross_process()
{
	Server *s = new Server; BLooper *l = new BLooper("xsrv");
	l->Lock(); l->AddHandler(s); l->Unlock(); l->Run();
	int rc = run_child("--messenger", l->Port(), s->Token());
	CHECK_EQ(rc, 0);
	l->Lock(); int32 got = s->received; l->Unlock();
	CHECK_EQ(got, 20 + 1 + 1 + 1);                           // 20 adds, echo, silent, unknown
	l->Quit();
}

// ---- BApplication
struct TestApp : BApplication {
	std::string log; BMessenger found; status_t findStatus = -1;
	TestApp() : BApplication("application/x-vnd.aurora-test") {
		BHandler *h = new BHandler("pref"); AddHandler(h); SetPreferredHandler(h); }   // must not swallow lifecycle messages
	void ArgvReceived(int32 argc, char **argv) override {
		log += "A"; for (int32 i = 1; i < argc; i++) { log += ":"; log += argv[i]; } }
	void ReadyToRun() override {
		log += "R"; found = BMessenger("application/x-vnd.aurora-test", -1, &findStatus);
		PostMessage(kPing); }
	void MessageReceived(BMessage *m) override {
		if (m->what == kPing) { log += "P"; Quit(); } else BApplication::MessageReceived(m); }
	bool QuitRequested() override { log += "Q"; return BApplication::QuitRequested(); }
};

static status_t poster(void *app) { snooze(100000); ((BApplication *)app)->PostMessage(B_QUIT_REQUESTED); return 0; }

static void test_application()
{
	TestApp *app = new TestApp;
	CHECK_EQ(app->InitCheck(), B_OK); CHECK(be_app == app); CHECK(!strcmp(app->Signature(), "application/x-vnd.aurora-test"));
	BMessage argv(B_ARGV_RECEIVED); argv.AddInt32("argc", 3); argv.AddString("argv", "prog");
	argv.AddString("argv", "one"); argv.AddString("argv", "two");
	app->PostMessage(&argv, app);                                  // queued before Run: delivered first
	status_t err; BApplication second("application/x-vnd.second", &err); CHECK_EQ(err, B_ERROR);
	CHECK(be_app == app);
	app->Run();
	CHECK(app->log == "AonetwoRP" || app->log == "A:one:twoRP");
	CHECK_EQ(app->findStatus, B_OK); CHECK(app->found.IsTargetLocal()); CHECK(app->found.Port() == app->Port());
	delete app; CHECK(be_app == NULL);
	// quitting from another thread goes through B_QUIT_REQUESTED -> QuitRequested -> Quit
	struct Quietest : BApplication { std::string log; Quietest() : BApplication("application/x-vnd.aurora-test2") {}
		void ReadyToRun() override { log += "R"; }
		bool QuitRequested() override { log += "Q"; return true; } };
	Quietest *q = new Quietest;
	thread_id t = spawn_thread(poster, "poster", B_NORMAL_PRIORITY, q); resume_thread(t);
	q->Run(); CHECK(q->log == "RQ");
	status_t rv; wait_for_thread(t, &rv);
	delete q;
	// a vetoed quit keeps running
	struct Stubborn : BApplication { int asked = 0; Stubborn() : BApplication("application/x-vnd.aurora-test3") {}
		bool QuitRequested() override { return ++asked >= 2; }
		void ReadyToRun() override { PostMessage(B_QUIT_REQUESTED); PostMessage(B_QUIT_REQUESTED); } };
	Stubborn *sb = new Stubborn; sb->Run(); CHECK_EQ(sb->asked, 2); delete sb;
}

int main(int argc, char **argv)
{
	if (argc >= 2 && !strncmp(argv[1], "--", 2)) return child_main(argv);
	char ns[64]; snprintf(ns, sizeof ns, "testapp-%d", getpid()); setenv("AURORA_KITS_NS", ns, 1);
	RUN(test_message_types); RUN(test_message_errors); RUN(test_flatten_roundtrip);
	RUN(test_port_roundtrip_between_processes);
	RUN(test_looper_dispatch); RUN(test_messenger_local); RUN(test_looper_quit);
	RUN(test_messenger_cross_process); RUN(test_application);
	_kits_unlink_namespace();
	printf("%d checks, %d failures\n", checks, failures);
	return failures ? 1 : 0;
}
