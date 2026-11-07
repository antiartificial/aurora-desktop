/*
 * The accessibility tree as a bus service (decision 0017).
 *
 * An application opts in by being launched with AURORA_A11Y_SERVICE=<name> (the launcher sets it; an app that does not
 * get it never joins the bus for this and builds nothing to publish). The service answers three verbs and nothing
 * else; it never publishes the tree, it is only asked:
 *
 *   tree@v1  {window?, root?, depth?, text?}   the windows, or one window, or one subtree
 *   node@v1  {window, token}                   one node, children as tokens
 *   text@v1  {window, token, deep?}            the text one view drew, or its subtree's, in reading order
 *
 * Who may ask is decided here, from the bus-stamped sender and the bus's own capability table, never from the payload:
 *   - an unrecognised identity: nothing.
 *   - the user's session (uid<N>:<comm>): everything this app has. A process that reaches the session socket is the
 *     session (see index/access.go, which makes the same trust statement).
 *   - a domain (domain:<name>:<id>): only its OWN app's tree (the identity this service registered under is the
 *     domain's), and only while the bus holds a live handle with resource "ui:read:<that identity>" that a SESSION
 *     identity minted. A domain can mint a handle for itself but the bus stamps it with the domain as minter, and a
 *     handle minted by a domain is not accepted. Any other domain, with or without a grant: nothing.
 * Everything fails closed: if the bus cannot be asked, the answer is no.
 */
#include "a11y_bus.h"
#include "appserver/server.h"

#include <BusClient.h>
#include <OS.h>

#include <algorithm>
#include <condition_variable>
#include <deque>
#include <math.h>
#include <mutex>
#include <set>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <thread>

namespace aurora {
namespace {

struct Service {
	BusClient client;
	std::string name, self;	// self: the identity the bus stamped on this connection
	std::thread worker;
	std::mutex mu; std::condition_variable cv;
	std::deque<BusEnvelope> q; bool stop = false;
};
Service* gSvc = NULL;

// ---- who is asking ------------------------------------------------------------------------------------------------

std::string KindOf(const std::string& id)
{
	if (id.compare(0, 7, "domain:") == 0 && id.size() > 7) return "domain";
	if (id.compare(0, 3, "uid") == 0) {
		size_t i = 3;
		while (i < id.size() && isdigit((unsigned char)id[i])) i++;
		if (i > 3 && i + 1 < id.size() && id[i] == ':') return "session";
	}
	return "unknown";
}

struct Verdict {
	bool ok = false;
	std::string code, message, kind, rule;
};

// The live ui:read handles the bus knows of, as "<resource> <minter>" pairs. False when the bus cannot be asked.
bool LiveHandles(Service* s, std::vector<std::pair<std::string, std::string> >* out)
{
	BusEnvelope reply;
	if (s->client.Control("bus.inspect@v1", Json::MakeObject(), &reply) != B_OK) return false;
	const Json* hs = reply.payload.get("handles");
	if (!hs || !hs->isArray()) return false;
	for (const Json& h : hs->items()) out->push_back(std::make_pair(h.getString("resource"), h.getString("minter")));
	return true;
}

Verdict Authorize(Service* s, const std::string& caller)
{
	Verdict v;
	v.kind = KindOf(caller);
	if (v.kind == "session") {
		v.ok = true;
		v.rule = "you are the user's own session: every window of this application is shown.";
		return v;
	}
	if (v.kind != "domain") {
		v.code = "unknown_viewer";
		v.message = "your identity is not one this service recognises: nothing is shown";
		v.rule = v.message;
		return v;
	}
	// A domain. Its own application's tree and nobody else's: decided before the handle is looked at, so a domain learns
	// nothing about another domain's windows by what the refusal says.
	if (caller != s->self) {
		v.code = "other_domain";
		v.message = "this application is not yours; a domain may read only its own application's tree";
		v.rule = "you are a domain: you may read your own application's tree, and only while you hold ui:read.";
		return v;
	}
	std::vector<std::pair<std::string, std::string> > hs;
	if (!LiveHandles(s, &hs)) {
		v.code = "cannot_verify";
		v.message = "the bus could not be asked whether you hold ui:read; refused";
		v.rule = "fail closed.";
		return v;
	}
	const std::string want = "ui:read:" + caller;
	for (auto& h : hs)
		if (h.first == want && KindOf(h.second) == "session") {
			v.ok = true;
			v.rule = "you are a domain holding ui:read, minted by the user's session: your own application's tree is shown.";
			return v;
		}
	v.code = "no_grant";
	v.message = "no live ui:read grant for " + caller + ": a verb must list ui:read for the controller to mint one";
	v.rule = "you are a domain without ui:read: nothing is shown.";
	return v;
}

// ---- JSON ---------------------------------------------------------------------------------------------------------

Json Num(double d) { return (d == floor(d) && fabs(d) < 1e15) ? Json::MakeInt((int64_t)d) : Json::MakeDouble(d); }
Json Box(double l, double t, double r, double b)
{
	Json o = Json::MakeObject();
	o.set("l", Num(l)); o.set("t", Num(t)); o.set("r", Num(r)); o.set("b", Num(b));
	return o;
}

Json TextJson(const UiText& t)
{
	Json o = Json::MakeObject();
	o.set("text", Json::MakeString(t.text));
	o.set("x", Num(t.x)); o.set("y", Num(t.y)); o.set("width", Num(round(t.width * 100) / 100)); o.set("size", Num(t.size));
	o.set("box", Box(t.wl, t.wt, t.wr, t.wb));
	return o;
}

Json NodeJson(const UiNode& n, bool withText)
{
	Json o = Json::MakeObject();
	o.set("token", Json::MakeInt(n.token));
	o.set("parent", n.parent < 0 ? Json() : Json::MakeInt(n.parent));
	o.set("name", Json::MakeString(n.name));
	o.set("role", Json::MakeString(n.role));
	o.set("role_confidence", Json::MakeString(n.confidence));
	o.set("role_basis", Json::MakeString(n.basis));
	o.set("frame", Box(n.l, n.t, n.r, n.b));
	o.set("abs", Box(n.al, n.at, n.ar, n.ab));
	o.set("visible", n.clipped ? Json() : Box(n.vl, n.vt, n.vr, n.vb));
	Json st = Json::MakeObject();
	st.set("hidden", Json::MakeBool(n.hidden));
	st.set("effectively_hidden", Json::MakeBool(n.effectively_hidden));
	st.set("clipped", Json::MakeBool(n.clipped));
	st.set("navigable", Json::MakeBool(n.navigable));
	st.set("enabled", Json());	// null: the server is never told
	st.set("focused", Json());	// null: focus is client-side state, the server is never told
	o.set("state", st);
	Json d = Json::MakeObject();
	for (auto& kv : n.drew) d.set(kv.first, Json::MakeInt(kv.second));
	o.set("drew", d);
	if (withText) {
		Json ts = Json::MakeArray();
		for (const UiText& t : n.text) ts.push(TextJson(t));
		o.set("text", ts);
	}
	return o;
}

const UiNode* Find(const UiWindow& w, int32_t token)
{
	for (const UiNode& n : w.nodes) if (n.token == token) return &n;
	return NULL;
}

Json Subtree(const UiWindow& w, const UiNode& n, int depth, bool withText)
{
	Json o = NodeJson(n, withText);
	Json kids = Json::MakeArray();
	if (depth != 0)
		for (int32_t c : n.children)
			if (const UiNode* cn = Find(w, c)) kids.push(Subtree(w, *cn, depth > 0 ? depth - 1 : depth, withText));
	o.set("children", kids);
	if (depth == 0) o.set("child_tokens", [&] { Json a = Json::MakeArray(); for (int32_t c : n.children) a.push(Json::MakeInt(c)); return a; }());
	return o;
}

Json WindowJson(const UiWindow& w, const UiNode* root, int depth, bool withText)
{
	Json o = Json::MakeObject();
	o.set("index", Json::MakeInt(w.index));
	o.set("title", Json::MakeString(w.title));
	o.set("kind", Json::MakeString(w.kind));
	Json sz = Json::MakeObject(); sz.set("w", Json::MakeInt(w.width)); sz.set("h", Json::MakeInt(w.height));
	o.set("size", sz);
	o.set("visible", Json::MakeBool(w.visible));
	o.set("active", Json::MakeBool(w.active));
	if (!w.nodes.empty()) o.set("root", Subtree(w, root ? *root : w.nodes[0], depth, withText));
	return o;
}

// Reading order: rows by baseline (runs within 4 units share a row), left to right inside a row.
void Collect(const UiWindow& w, const UiNode& n, bool deep, std::vector<const UiText*>* out, std::vector<const UiNode*>* owners)
{
	if (n.effectively_hidden) return;
	for (const UiText& t : n.text) { out->push_back(&t); owners->push_back(&n); }
	if (deep)
		for (int32_t c : n.children) if (const UiNode* cn = Find(w, c)) Collect(w, *cn, deep, out, owners);
}

Json TextReply(const UiWindow& w, const UiNode& n, bool deep)
{
	std::vector<const UiText*> runs; std::vector<const UiNode*> owners;
	Collect(w, n, deep, &runs, &owners);
	std::vector<size_t> idx(runs.size());
	for (size_t i = 0; i < idx.size(); i++) idx[i] = i;
	std::stable_sort(idx.begin(), idx.end(), [&](size_t a, size_t b) {
		double ya = runs[a]->wb, yb = runs[b]->wb;
		if (fabs(ya - yb) > 4) return ya < yb;
		return runs[a]->wl < runs[b]->wl;
	});
	std::string joined; double lastY = 0; bool first = true;
	Json items = Json::MakeArray();
	for (size_t i : idx) {
		if (!first) joined += fabs(runs[i]->wb - lastY) > 4 ? "\n" : " ";
		joined += runs[i]->text; lastY = runs[i]->wb; first = false;
		Json it = TextJson(*runs[i]);
		it.set("view", Json::MakeInt(owners[i]->token));
		it.set("view_name", Json::MakeString(owners[i]->name));
		items.push(it);
	}
	Json o = Json::MakeObject();
	o.set("window", Json::MakeInt(w.index)); o.set("token", Json::MakeInt(n.token));
	o.set("deep", Json::MakeBool(deep));
	o.set("text", Json::MakeString(joined));
	o.set("runs", items);
	return o;
}

int IntOf(const Json& p, const char* k, int def)
{
	const Json* v = p.get(k);
	return v && (v->type() == Json::Int || v->type() == Json::Double) ? (int)v->asInt() : def;
}
bool BoolOf(const Json& p, const char* k, bool def)
{
	const Json* v = p.get(k);
	return v && v->type() == Json::Bool ? v->asBool() : def;
}

// ---- serving ------------------------------------------------------------------------------------------------------

void Audit(Service* s, const BusEnvelope& req, const char* op, int windows, size_t textBytes)
{
	// Only reads that were served are recorded: those are rare and are the thing an owner wants to see. A refusal is
	// answered to its caller and not logged, so a caller that cannot read cannot fill the log by asking.
	Json p = Json::MakeObject();
	p.set("by", Json::MakeString(req.from)); p.set("service", Json::MakeString(s->name));
	p.set("verb", Json::MakeString(op)); p.set("windows", Json::MakeInt(windows)); p.set("text_bytes", Json::MakeInt((int64_t)textBytes));
	s->client.Publish("ui.read.served", p, "utility");
}

void Handle(Service* s, const BusEnvelope& req)
{
	Verdict v = Authorize(s, req.from);
	if (!v.ok) { s->client.ReplyError(req, v.code.c_str(), v.message.c_str()); return; }
	Json viewer = Json::MakeObject();
	viewer.set("identity", Json::MakeString(req.from)); viewer.set("kind", Json::MakeString(v.kind)); viewer.set("rule", Json::MakeString(v.rule));

	std::vector<UiWindow> wins = ui_snapshot();
	// A domain's own app only: windows of this process are all there is, but say so rather than rely on it.
	const Json& p = req.payload;
	Json out = Json::MakeObject();
	out.set("schema", Json::MakeString("aurora.ui.tree/1"));
	out.set("viewer", viewer);
	out.set("service", Json::MakeString(s->name));
	out.set("owner", Json::MakeString(s->self));
	out.set("units", Json::MakeString("layout units; BeOS-inclusive rects (a 10 wide view has right = left + 9); 1 unit = scale device pixels"));
	out.set("scale", Num(output_scale()));
	int nwin = 0; size_t tbytes = 0;
	std::string verb = req.verb;
	if (verb == "tree@v1") {
		int depth = IntOf(p, "depth", -1); bool withText = BoolOf(p, "text", true);
		int want = IntOf(p, "window", -1); int rootTok = IntOf(p, "root", -1);
		Json arr = Json::MakeArray();
		for (const UiWindow& w : wins) {
			if (want >= 0 && w.index != want) continue;
			const UiNode* root = NULL;
			if (rootTok >= 0) { root = Find(w, rootTok); if (!root) continue; }
			arr.push(WindowJson(w, root, depth, withText)); nwin++;
			for (const UiNode& n : w.nodes) for (const UiText& t : n.text) tbytes += t.text.size();
		}
		if ((want >= 0 || rootTok >= 0) && nwin == 0) { s->client.ReplyError(req, "not_found", "no such window or node"); return; }
		out.set("windows", arr);
	} else if (verb == "node@v1" || verb == "text@v1") {
		int wi = IntOf(p, "window", -1), tok = IntOf(p, "token", -1);
		const UiWindow* win = NULL; const UiNode* node = NULL;
		for (const UiWindow& w : wins) if (w.index == wi) { win = &w; node = Find(w, tok); }
		if (!win || !node) { s->client.ReplyError(req, "not_found", "needs {\"window\":N,\"token\":T} naming a window and a view that exist"); return; }
		nwin = 1;
		if (verb == "node@v1") {
			Json n = NodeJson(*node, true);
			Json kids = Json::MakeArray(); for (int32_t c : node->children) kids.push(Json::MakeInt(c));
			n.set("children", kids);
			out.set("window", Json::MakeInt(wi)); out.set("node", n);
			for (const UiText& t : node->text) tbytes += t.text.size();
		} else {
			Json t = TextReply(*win, *node, BoolOf(p, "deep", false));
			for (auto& m : t.members()) out.set(m.first, m.second);
			tbytes = t.getString("text").size();
		}
	} else {
		s->client.ReplyError(req, "unknown_verb", "this service answers tree@v1, node@v1 and text@v1");
		return;
	}
	Audit(s, req, verb.c_str(), nwin, tbytes);
	s->client.Reply(req, out);
}

void Worker(Service* s)
{
	for (;;) {
		BusEnvelope req;
		{
			std::unique_lock<std::mutex> l(s->mu);
			s->cv.wait(l, [s] { return s->stop || !s->q.empty(); });
			if (s->stop) return;
			req = s->q.front(); s->q.pop_front();
		}
		Handle(s, req);
	}
}

void StopService(Service* s)
{
	{ std::lock_guard<std::mutex> l(s->mu); s->stop = true; s->cv.notify_all(); }
	s->client.Close();
	if (s->worker.joinable()) s->worker.join();
	delete s;
}

}	// namespace

status_t a11y_start(const char* service, const char* socketPath)
{
	if (gSvc) return B_ERROR;
	Service* s = new Service;
	s->name = service;
	s->client.SetRequestCallback([s](const BusEnvelope& e) {
		std::lock_guard<std::mutex> l(s->mu);
		if (s->q.size() < 64) s->q.push_back(e);	// a flood of requests is dropped, not queued without bound
		s->cv.notify_one();
	});
	status_t r = s->client.Connect(socketPath);
	if (r != B_OK) { delete s; return r; }
	s->worker = std::thread(Worker, s);
	BusEnvelope reply;
	Json reg = Json::MakeObject(); reg.set("name", Json::MakeString(service));
	r = s->client.Control("bus.register@v1", reg, &reply);
	if (r != B_OK) { StopService(s); return r; }
	s->self = reply.payload.getString("domain");
	if (s->self.empty()) { StopService(s); return B_ERROR; }
	gSvc = s;
	return B_OK;
}

void a11y_stop()
{
	if (!gSvc) return;
	Service* s = gSvc; gSvc = NULL;
	StopService(s);
}

const char* a11y_identity() { return gSvc ? gSvc->self.c_str() : ""; }

}	// namespace aurora
