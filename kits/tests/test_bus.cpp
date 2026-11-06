// Bus bridge tests. The first groups are pure (JSON, conversion). The rest start a REAL busd
// (path in argv[1]; skipped without it) and talk to it through the C++ client and bridge.
#include <BusBridge.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

static int failures, checks;
#define CHECK(c) do { checks++; if (!(c)) { failures++; \
	printf("  FAIL %s:%d: %s\n", __FILE__, __LINE__, #c); } } while (0)
#define CHECK_EQ(a, b) do { long long _a = (a), _b = (b); checks++; if (_a != _b) { \
	failures++; printf("  FAIL %s:%d: %s == %s (%lld vs %lld)\n", __FILE__, __LINE__, \
	#a, #b, _a, _b); } } while (0)
#define CHECK_STR(a, b) do { std::string _a = (a), _b = (b); checks++; if (_a != _b) { \
	failures++; printf("  FAIL %s:%d: \"%s\" vs \"%s\"\n", __FILE__, __LINE__, _a.c_str(), _b.c_str()); } } while (0)
#define RUN(t) do { printf("[ RUN ] %s\n", #t); int f0 = failures; t(); \
	printf("[ %s ] %s\n", failures == f0 ? " OK " : "FAIL", #t); } while (0)

static Json P(const char *text) { Json j; std::string e; if (!Json::Parse(text, &j, &e)) printf("  parse failed: %s: %s\n", text, e.c_str()); return j; }

static void test_json()
{
	// the envelope shapes the bus uses, each must survive parse -> dump -> parse
	const char *shapes[] = {
		"{\"id\":\"c1\",\"kind\":\"request\",\"to\":\"bus\",\"verb\":\"bus.register@v1\",\"payload\":{\"name\":\"x\"}}",
		"{\"id\":\"7\",\"ts\":\"1762400000000000000-00000000000000000001\",\"from\":\"uid1000:bus\",\"to\":\"haiku-ledger\",\"kind\":\"request\",\"verb\":\"add@v1\",\"qos\":\"user-initiated\",\"payload\":{\"label\":\"coffee\",\"cents\":350}}",
		"{\"kind\":\"response\",\"verb\":\"add@v1\",\"corr\":\"7\",\"payload\":{\"total\":350,\"ratio\":0.25,\"ok\":true,\"none\":null}}",
		"{\"kind\":\"error\",\"corr\":\"7\",\"payload\":{\"code\":\"no_such_service\",\"message\":\"no service \\\"x\\\" is registered\"}}",
		"{\"kind\":\"event\",\"topic\":\"ledger.entry.added\",\"ephemeral\":true,\"payload\":[1,2,[3,{\"a\":[]}]]}",
		"{\"kind\":\"event\",\"topic\":\"t\",\"segments\":[{\"id\":\"frame\",\"bytes\":1048576,\"sealed\":true,\"mime\":\"image/png\"}]}",
	};
	for (const char *s : shapes) {
		Json a, b; std::string e;
		CHECK(Json::Parse(s, &a, &e));
		CHECK(Json::Parse(a.Dump(), &b, &e));
		CHECK_STR(a.Dump(), b.Dump());
		BusEnvelope env; CHECK(BusEnvelope::FromJson(a, &env));
	}
	Json j = P("{\"a\":9223372036854775807,\"b\":-5,\"c\":1.0,\"d\":1e3,\"e\":99999999999999999999,\"s\":\"t\\u00e9\\n\\ud83d\\ude00\\\"\"}");
	CHECK(j.get("a")->type() == Json::Int); CHECK_EQ(j.get("a")->asInt(), INT64_MAX);
	CHECK(j.get("b")->type() == Json::Int); CHECK(j.get("c")->type() == Json::Double);
	CHECK(j.get("d")->type() == Json::Double); CHECK(j.get("e")->type() == Json::Double);
	CHECK_STR(j.get("s")->asString(), "t\xc3\xa9\n\xf0\x9f\x98\x80\"");
	CHECK_STR(P("1.0").Dump(), "1.0");                        // a double stays a double
	CHECK_STR(P("\"a\\u0001\"").Dump(), "\"a\\u0001\"");
	BusEnvelope e; e.kind = "request"; e.to = "svc"; e.verb = "v@v1"; e.payload = P("{\"k\":[1,2]}");
	CHECK_STR(e.ToJson().Dump(), "{\"to\":\"svc\",\"kind\":\"request\",\"verb\":\"v@v1\",\"payload\":{\"k\":[1,2]}}");
	// malformed input is rejected, never crashes
	const char *bad[] = { "", "{", "{\"a\":}", "[1,]", "{\"a\" 1}", "nul", "\"abc", "01", "1.", "-", "{\"a\":1}x",
		"\"\\ud800\"", "\"\\x\"", "{1:2}", "[\"a\nb\"]", "\xff\xfe" };
	for (const char *s : bad) { Json x; std::string err; CHECK(!Json::Parse(s, &x, &err)); CHECK(!err.empty()); }
	Json x; CHECK(!Json::Parse(std::string(100000, '['), &x));   // depth cap, no stack overflow
	BusEnvelope be; CHECK(!BusEnvelope::FromJson(P("[1]"), &be)); CHECK(!BusEnvelope::FromJson(P("{}"), &be));
}

static void test_convert()
{
	BMessage m; std::string err;
	CHECK_EQ(BusJsonToMessage(P("{\"s\":\"hi\",\"i\":42,\"big\":5000000000,\"neg\":-7,\"d\":2.5,\"t\":true,"
		"\"n\":null,\"o\":{\"x\":1,\"deep\":{\"y\":\"z\"}},\"ai\":[1,2,3],\"as\":[\"a\",\"b\"],\"ad\":[1,2.5],"
		"\"ab\":[1,5000000000],\"ao\":[{\"k\":1},{\"k\":2}],\"one\":[9],\"empty\":[]}"), &m, &err), B_OK);
	type_code t; int32 n;
	const char *s; int32 i; int64 l; double d; bool b;
	CHECK_EQ(m.FindString("s", &s), B_OK); CHECK_STR(s, "hi"); CHECK_EQ(m.GetInfo("s", &t), B_OK); CHECK_EQ((long long)t, (long long)B_STRING_TYPE);
	CHECK_EQ(m.FindInt32("i", &i), B_OK); CHECK_EQ(i, 42);
	m.GetInfo("big", &t); CHECK_EQ((long long)t, (long long)B_INT64_TYPE); m.FindInt64("big", &l); CHECK_EQ(l, 5000000000LL);
	m.FindInt32("neg", &i); CHECK_EQ(i, -7);
	m.GetInfo("d", &t); CHECK_EQ((long long)t, (long long)B_DOUBLE_TYPE); m.FindDouble("d", &d); CHECK(d == 2.5);
	m.FindBool("t", &b); CHECK(b);
	CHECK_EQ(m.GetInfo("n", &t), B_NAME_NOT_FOUND);                 // null: dropped
	BMessage o, deep; CHECK_EQ(m.FindMessage("o", &o), B_OK); o.FindInt32("x", &i); CHECK_EQ(i, 1);
	CHECK_EQ(o.FindMessage("deep", &deep), B_OK); deep.FindString("y", &s); CHECK_STR(s, "z");
	m.GetInfo("ai", &t, &n); CHECK_EQ((long long)t, (long long)B_INT32_TYPE); CHECK_EQ(n, 3);
	m.FindInt32("ai", 2, &i); CHECK_EQ(i, 3);
	m.GetInfo("as", &t, &n); CHECK_EQ(n, 2); m.FindString("as", 1, &s); CHECK_STR(s, "b");
	m.GetInfo("ad", &t, &n); CHECK_EQ((long long)t, (long long)B_DOUBLE_TYPE);   // int+double -> double
	m.FindDouble("ad", 0, &d); CHECK(d == 1.0);
	m.GetInfo("ab", &t, &n); CHECK_EQ((long long)t, (long long)B_INT64_TYPE);    // int32+int64 -> int64
	m.GetInfo("ao", &t, &n); CHECK_EQ((long long)t, (long long)B_MESSAGE_TYPE); CHECK_EQ(n, 2);
	BMessage k1; m.FindMessage("ao", 1, &k1); k1.FindInt32("k", &i); CHECK_EQ(i, 2);
	m.GetInfo("one", &t, &n); CHECK_EQ(n, 1);                                    // 1-item array == scalar
	CHECK_EQ(m.GetInfo("empty", &t), B_NAME_NOT_FOUND);                          // empty array: gone
	// unrepresentable payloads are refused with a reason
	const char *bad[] = { "{\"a\":[1,\"x\"]}", "{\"a\":[true,1]}", "{\"a\":[[1]]}", "{\"a\":[null]}", "{\"a\":[{\"k\":1},2]}",
		"{\"a\":{\"b\":[1,\"x\"]}}", "{\"\":1}" };
	for (const char *js : bad) { BMessage x; err.clear(); CHECK(BusJsonToMessage(P(js), &x, &err) != B_OK); CHECK(!err.empty()); }
	BMessage v; CHECK_EQ(BusJsonToMessage(P("\"bare\""), &v, &err), B_OK); v.FindString("value", &s); CHECK_STR(s, "bare");
	v.MakeEmpty(); v.what = 'KEEP'; BusJsonToMessage(P("{\"a\":1}"), &v, &err); CHECK_EQ((long long)v.what, (long long)'KEEP');

	// reverse direction
	BMessage r(B_REPLY); BMessage inner;
	inner.AddString("q", "w");
	r.AddInt32("total", 5); r.AddInt8("i8", -3); r.AddInt16("i16", 300); r.AddInt64("i64", 1LL << 40);
	r.AddBool("ok", true); r.AddFloat("f", 0.5f); r.AddDouble("d", 0.1); r.AddString("name", "x\"y");
	r.AddMessage("nest", &inner); r.AddInt32("many", 1); r.AddInt32("many", 2); r.AddInt32("lone", 7);
	BusMarkArray(&r, "lone"); r.AddString("bus:verb", "secret");
	const char raw[] = { 0, 1, 2, 3 }; r.AddData("blob", 'BLOB', raw, 4);
	CHECK_STR(BusMessageToJson(r).Dump(), "{\"total\":5,\"i8\":-3,\"i16\":300,\"i64\":1099511627776,\"ok\":true,"
		"\"f\":0.5,\"d\":0.10000000000000001,\"name\":\"x\\\"y\",\"nest\":{\"q\":\"w\"},\"many\":[1,2],\"lone\":[7],"
		"\"blob\":{\"$raw\":{\"type\":\"BLOB\",\"base64\":\"AAECAw==\"}}}");
	BMessage nan; nan.AddDouble("x", 0.0 / 0.0); CHECK_STR(BusMessageToJson(nan).Dump(), "{\"x\":null}");
	BMessage back; BusJsonToMessage(BusMessageToJson(r), &back, &err);          // round trip (typed widening only)
	back.FindInt32("i8", &i); CHECK_EQ(i, -3); CHECK_EQ(back.GetInfo("i16", &t), B_OK); CHECK_EQ((long long)t, (long long)B_INT32_TYPE);
}

// ---- a real busd ------------------------------------------------------------------------
static pid_t gBusd = -1; static std::string gDir, gSock; static const char *gBusdPath;

static bool start_busd()
{
	char tmpl[] = "/tmp/aurora-kits-bus.XXXXXX"; gDir = mkdtemp(tmpl); gSock = gDir + "/bus.sock";
	gBusd = fork();
	if (gBusd == 0) {
		int fd = open("/dev/null", 1); dup2(fd, 2);
		execl(gBusdPath, gBusdPath, "--socket", gSock.c_str(), "--data-dir", (gDir + "/log").c_str(), (char *)NULL);
		_exit(127);
	}
	for (int i = 0; i < 200; i++) {
		BusClient c;
		if (c.Connect(gSock.c_str()) == B_OK) return true;
		usleep(25000);
	}
	return false;
}
static void stop_busd()
{
	if (gBusd > 0) { kill(gBusd, SIGTERM); waitpid(gBusd, NULL, 0); }
	system(("rm -rf " + gDir).c_str());
}

enum { kAdd = 'ADD_', kEcho = 'ECHO', kSlow = 'SLOW', kSilent = 'SLNT', kSeq = 'SEQ_' };

struct Svc : BHandler {
	std::mutex mu; std::vector<BMessage> seen; std::vector<int32> seq; int events = 0;
	Svc() : BHandler("svc") {}
	void MessageReceived(BMessage *m) override {
		{ std::lock_guard<std::mutex> l(mu); seen.push_back(*m); }
		switch (m->what) {
		case kAdd: {
			int32 a = 0, b = 0;
			if (m->FindInt32("a", &a) != B_OK || m->FindInt32("b", &b) != B_OK) {
				BMessage r(B_MESSAGE_NOT_UNDERSTOOD); r.AddInt32("error", B_BAD_VALUE); m->SendReply(&r); break; }
			BMessage r(B_REPLY); r.AddInt32("sum", a + b); m->SendReply(&r); break; }
		case kEcho: { BMessage r(*m); r.what = B_REPLY; m->SendReply(&r); break; }
		case kSlow: snooze(600000); { BMessage r(B_REPLY); m->SendReply(&r); } break;
		case kSilent: break;
		case kSeq: { int32 n = 0; m->FindInt32("n", &n); { std::lock_guard<std::mutex> l(mu); seq.push_back(n); }
			BMessage r(B_REPLY); r.AddInt32("n", n); m->SendReply(&r); break; }
		case B_BUS_EVENT: { std::lock_guard<std::mutex> l(mu); events++; break; }
		default: BHandler::MessageReceived(m);
		}
	}
	BMessage last() { std::lock_guard<std::mutex> l(mu); return seen.back(); }
};

static status_t call(BusClient &c, const char *to, const char *verb, const char *json, BusEnvelope *r)
{
	BusEnvelope q; q.to = to; q.verb = verb; q.payload = P(json);
	return c.Call(q, r, 5000000);
}
static std::string code(const BusEnvelope &e) { return e.payload.getString("code"); }

struct Fixture {
	BLooper *looper; Svc *svc; BusBridge *bridge; BusClient client;
	Fixture(const char *service) {
		looper = new BLooper("svc"); svc = new Svc();
		looper->Lock(); looper->AddHandler(svc); looper->Unlock();
		looper->Run();
		bridge = new BusBridge(looper, svc, service);
		bridge->MapVerb("add@v1", kAdd); bridge->MapVerb("echo@v1", kEcho); bridge->MapVerb("slow@v1", kSlow);
		bridge->MapVerb("silent@v1", kSilent); bridge->MapVerb("seq@v1", kSeq);
		CHECK_EQ(bridge->Start(gSock.c_str()), B_OK);
		CHECK_EQ(client.Connect(gSock.c_str()), B_OK);
	}
	~Fixture() { client.Close(); delete bridge; looper->Lock(); looper->Quit(); }
};

static void test_request_reply()
{
	Fixture f("kitsvc"); BusEnvelope r;
	// request -> BMessage -> handler -> SendReply -> response
	CHECK_EQ(call(f.client, "kitsvc", "add@v1", "{\"a\":2,\"b\":40}", &r), B_OK);
	CHECK_STR(r.kind, "response"); CHECK_STR(r.verb, "add@v1"); CHECK_STR(r.payload.Dump(), "{\"sum\":42}");
	BMessage got = f.svc->last(); const char *s;
	CHECK_EQ((long long)got.what, (long long)kAdd);                   // verb became `what`
	CHECK_EQ(got.FindString("bus:verb", &s), B_OK); CHECK_STR(s, "add@v1");
	CHECK_EQ(got.FindString("bus:from", &s), B_OK); CHECK(strncmp(s, "uid", 3) == 0);   // the daemon's identity, not ours
	// a payload cannot forge the bridge's metadata fields
	CHECK_EQ(call(f.client, "kitsvc", "echo@v1", "{\"bus:from\":\"domain:evil\",\"bus:verb\":\"x\"}", &r), B_OK);
	got = f.svc->last(); int32 fc = 0; got.GetInfo("bus:from", NULL, &fc); CHECK_EQ(fc, 1);
	got.FindString("bus:from", &s); CHECK(strncmp(s, "uid", 3) == 0);
	// typed fields survive the whole trip, nesting and arrays included
	const char *in = "{\"n\":1,\"big\":5000000000,\"f\":2.5,\"t\":false,\"s\":\"x\",\"o\":{\"p\":{\"q\":[1,2]}},\"l\":[\"a\",\"b\"],\"w\":[{\"k\":1},{\"k\":2}]}";
	CHECK_EQ(call(f.client, "kitsvc", "echo@v1", in, &r), B_OK);
	Json out = r.payload; CHECK_STR(out.get("n")->Dump(), "1"); CHECK_STR(out.get("big")->Dump(), "5000000000");
	CHECK_STR(out.get("f")->Dump(), "2.5"); CHECK_STR(out.get("t")->Dump(), "false");
	CHECK_STR(out.get("o")->Dump(), "{\"p\":{\"q\":[1,2]}}"); CHECK_STR(out.get("l")->Dump(), "[\"a\",\"b\"]");
	CHECK_STR(out.get("w")->Dump(), "[{\"k\":1},{\"k\":2}]");
	got = f.svc->last(); int32 cnt; type_code t;
	CHECK_EQ(got.GetInfo("o", &t), B_OK); CHECK_EQ((long long)t, (long long)B_MESSAGE_TYPE);
	CHECK_EQ(got.GetInfo("w", &t, &cnt), B_OK); CHECK_EQ(cnt, 2);
	// a handler that says it did not understand -> a typed bus error
	CHECK_EQ(call(f.client, "kitsvc", "add@v1", "{\"a\":\"two\"}", &r), B_ERROR);
	CHECK_STR(r.kind, "error"); CHECK_STR(code(r), "not_understood");
	CHECK(r.payload.getString("message").find("error") != std::string::npos);
}

static void test_unhandled_and_malformed()
{
	Fixture f("kitsvc2"); BusEnvelope r;
	// an unmapped verb arrives as B_BUS_REQUEST; the handler's default sends it to the looper,
	// which answers not-understood. The requester gets an error, promptly.
	bigtime_t t0 = system_time();
	CHECK_EQ(call(f.client, "kitsvc2", "frobnicate@v1", "{\"x\":1}", &r), B_ERROR);
	CHECK_STR(code(r), "not_understood"); CHECK(system_time() - t0 < 1000000);
	CHECK_EQ((long long)f.svc->last().what, (long long)B_BUS_REQUEST);
	// a handler that never calls SendReply
	CHECK_EQ(call(f.client, "kitsvc2", "silent@v1", "{}", &r), B_ERROR); CHECK_STR(code(r), "no_reply");
	// malformed payloads: valid JSON that is not representable, wrong shapes
	size_t before = f.svc->seen.size();
	CHECK_EQ(call(f.client, "kitsvc2", "add@v1", "{\"a\":[1,\"x\"]}", &r), B_ERROR); CHECK_STR(code(r), "bad_payload");
	CHECK_EQ(call(f.client, "kitsvc2", "add@v1", "{\"a\":[[1]]}", &r), B_ERROR); CHECK_STR(code(r), "bad_payload");
	CHECK_EQ(f.svc->seen.size(), before);                                 // never reached the handler
	CHECK_EQ(call(f.client, "kitsvc2", "add@v1", "[1,2,3]", &r), B_ERROR); CHECK_STR(code(r), "not_understood");  // bare array -> "value"
	CHECK_EQ(call(f.client, "kitsvc2", "add@v1", "{\"a\":1,\"b\":2}", &r), B_OK);  // still alive
	// raw garbage straight at the socket: not JSON, truncated, wrong shape, binary. Nothing may crash.
	int fd = socket(AF_UNIX, SOCK_STREAM, 0); sockaddr_un a; memset(&a, 0, sizeof a); a.sun_family = AF_UNIX;
	strcpy(a.sun_path, gSock.c_str());
	CHECK(connect(fd, (sockaddr *)&a, sizeof a) == 0);
	const char junk[] = "not json\n{\"kind\":\n[1,2]\n{\"kind\":\"request\",\"to\":\"kitsvc2\",\"verb\":\"add@v1\",\"payload\":\"\xff\"}\n\x01\x02\n{}\n";
	send(fd, junk, sizeof junk - 1, 0); usleep(100000); close(fd);
	CHECK_EQ(call(f.client, "kitsvc2", "add@v1", "{\"a\":1,\"b\":2}", &r), B_OK);
	// a service that does not exist: the bus's own error, not a hang
	CHECK_EQ(call(f.client, "nobody", "add@v1", "{}", &r), B_ERROR); CHECK_STR(code(r), "no_such_service");
}

static void test_timeout_and_order()
{
	Fixture f("kitsvc3"); BusEnvelope r;
	f.bridge->SetReplyTimeout(200000);
	CHECK_EQ(call(f.client, "kitsvc3", "slow@v1", "{}", &r), B_ERROR); CHECK_STR(code(r), "timeout");
	f.bridge->SetReplyTimeout(10000000);
	CHECK_EQ(call(f.client, "kitsvc3", "add@v1", "{\"a\":1,\"b\":1}", &r), B_OK);   // recovered
	// ordering: requests from one connection reach the handler in the order sent
	for (int n = 0; n < 40; n++) {
		BusEnvelope q; q.kind = "request"; q.id = "o" + std::to_string(n); q.to = "kitsvc3"; q.verb = "seq@v1";
		q.payload = Json::MakeObject(); q.payload.set("n", Json::MakeInt(n)); f.client.Send(q);
	}
	CHECK_EQ(call(f.client, "kitsvc3", "seq@v1", "{\"n\":40}", &r), B_OK);
	std::lock_guard<std::mutex> l(f.svc->mu); CHECK_EQ((int)f.svc->seq.size(), 41);
	bool ordered = true; for (size_t i = 0; i < f.svc->seq.size(); i++) ordered &= f.svc->seq[i] == (int32)i;
	CHECK(ordered);
}

static void test_events_and_call()
{
	Fixture f("kitsvc4");
	// the app publishes; a subscriber receives, with typed payload
	BusClient sub; std::mutex mu; std::vector<BusEnvelope> got;
	sub.SetEventCallback([&](const BusEnvelope &e) { std::lock_guard<std::mutex> l(mu); got.push_back(e); });
	CHECK_EQ(sub.Connect(gSock.c_str()), B_OK); CHECK_EQ(sub.Subscribe("kit.**"), B_OK);
	BMessage ev; ev.AddString("label", "coffee"); ev.AddInt32("cents", 350);
	CHECK_EQ(f.bridge->Publish("kit.entry.added", ev), B_OK);
	BMessage ev2; ev2.AddInt32("x", 1); f.bridge->Publish("other.topic", ev2);      // not subscribed
	for (int i = 0; i < 100 && ({ std::lock_guard<std::mutex> l(mu); got.empty(); }); i++) usleep(20000);
	{ std::lock_guard<std::mutex> l(mu); CHECK_EQ((int)got.size(), 1);
	  if (!got.empty()) { CHECK_STR(got[0].topic, "kit.entry.added"); CHECK_STR(got[0].kind, "event");
	    CHECK_STR(got[0].payload.Dump(), "{\"label\":\"coffee\",\"cents\":350}"); CHECK(got[0].from.compare(0, 3, "uid") == 0); } }
	// and the other way: a bus event reaches the app's handler as B_BUS_EVENT
	CHECK_EQ(f.bridge->Subscribe("in.*"), B_OK);
	CHECK_EQ(f.client.Publish("in.ping", P("{\"v\":3}")), B_OK);
	for (int i = 0; i < 100 && f.svc->events == 0; i++) usleep(20000);
	CHECK_EQ(f.svc->events, 1);
	BMessage e = f.svc->last(); const char *s; int32 v;
	CHECK_EQ((long long)e.what, (long long)B_BUS_EVENT); e.FindString("bus:topic", &s); CHECK_STR(s, "in.ping");
	e.FindInt32("v", &v); CHECK_EQ(v, 3);
	// BMessenger-shaped call: a second Haiku looper calls the first one's verb through the bus
	Fixture g("kitsvc5");
	BMessage req, rep; req.AddInt32("a", 20); req.AddInt32("b", 22);
	CHECK_EQ(g.bridge->Call("kitsvc4", "add@v1", req, &rep), B_OK);
	CHECK_EQ((long long)rep.what, (long long)B_REPLY); rep.FindInt32("sum", &v); CHECK_EQ(v, 42);
	BMessage bad; bad.AddString("a", "x");
	CHECK_EQ(g.bridge->Call("kitsvc4", "add@v1", bad, &rep), B_ERROR);
	CHECK_EQ((long long)rep.what, (long long)B_BUS_ERROR); rep.FindString("code", &s); CHECK_STR(s, "not_understood");
	CHECK_EQ(g.bridge->Call("ghost", "add@v1", req, &rep), B_ERROR);
	// registering a name twice is refused by the bus; the second bridge reports it
	BusBridge dup(g.looper, g.svc, "kitsvc4"); CHECK(dup.Start(gSock.c_str()) != B_OK);
}

int main(int argc, char **argv)
{
	setenv("AURORA_KITS_NS", ("testbus" + std::to_string(getpid())).c_str(), 1);
	RUN(test_json); RUN(test_convert);
	gBusdPath = argc > 1 ? argv[1] : NULL;
	if (!gBusdPath) printf("[ SKIP ] real-busd tests: no busd path given (Go not available at configure time)\n");
	else if (!start_busd()) { printf("  FAIL could not start busd\n"); failures++; }
	else {
		RUN(test_request_reply); RUN(test_unhandled_and_malformed); RUN(test_timeout_and_order);
		RUN(test_events_and_call);
	}
	stop_busd();
	_kits_unlink_namespace();
	printf("%s: %d checks, %d failures\n", failures ? "FAILED" : "PASSED", checks, failures);
	return failures ? 1 : 0;
}
