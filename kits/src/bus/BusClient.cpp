#include <BusClient.h>
#include <chrono>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

static const size_t kMaxLine = 8u << 20;   // the daemon's own limit is 4 MiB

static void put(Json &o, const char *k, const std::string &v)
{
	if (!v.empty()) o.set(k, Json::MakeString(v));
}

Json BusEnvelope::ToJson() const
{
	Json o = Json::MakeObject();
	put(o, "id", id); put(o, "to", to); o.set("kind", Json::MakeString(kind));
	put(o, "verb", verb); put(o, "topic", topic); put(o, "qos", qos); put(o, "corr", corr);
	if (!payload.isNull()) o.set("payload", payload);
	if (ephemeral) o.set("ephemeral", Json::MakeBool(true));
	return o;   // id/ts/from are the daemon's to set; a client-supplied from is overwritten
}

bool BusEnvelope::FromJson(const Json &j, BusEnvelope *e)
{
	if (!j.isObject() || j.getString("kind").empty()) return false;
	e->id = j.getString("id"); e->ts = j.getString("ts"); e->from = j.getString("from");
	e->to = j.getString("to"); e->kind = j.getString("kind"); e->verb = j.getString("verb");
	e->topic = j.getString("topic"); e->qos = j.getString("qos"); e->corr = j.getString("corr");
	const Json *p = j.get("payload");
	e->payload = p ? *p : Json();
	const Json *eph = j.get("ephemeral");
	e->ephemeral = eph && eph->type() == Json::Bool && eph->asBool();
	return true;
}

status_t BusClient::Connect(const char *path)
{
	std::string p;
	if (path) p = path;
	else if (const char *s = getenv("AURORA_BUS_SOCK")) p = s;
	else if (const char *x = getenv("XDG_RUNTIME_DIR")) p = std::string(x) + "/aurora/bus.sock";
	sockaddr_un a; memset(&a, 0, sizeof a); a.sun_family = AF_UNIX;
	if (fFd >= 0) return B_ERROR;
	if (p.empty() || p.size() >= sizeof a.sun_path) return B_BAD_VALUE;
	strcpy(a.sun_path, p.c_str());
	int fd = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
	if (fd < 0) return B_ERROR;
	if (connect(fd, (sockaddr *)&a, sizeof a) < 0) { close(fd); return B_ERROR; }
	fFd = fd; fClosed = false;
	fReader = std::thread(&BusClient::ReadLoop, this);
	return B_OK;
}

void BusClient::Close()
{
	if (fFd < 0) return;
	{ std::lock_guard<std::mutex> l(fMu); fClosed = true; }
	shutdown(fFd, SHUT_RDWR);                 // wakes the reader
	if (fReader.joinable()) fReader.join();
	close(fFd); fFd = -1;
	fCv.notify_all();
}

void BusClient::ReadLoop()
{
	std::string buf; char tmp[65536];
	for (;;) {
		ssize_t n = recv(fFd, tmp, sizeof tmp, 0);
		if (n <= 0) break;
		buf.append(tmp, n);
		size_t nl;
		while ((nl = buf.find('\n')) != std::string::npos) {
			Dispatch(buf.substr(0, nl));
			buf.erase(0, nl + 1);
		}
		if (buf.size() > kMaxLine) break;      // a line with no end: give up the connection
	}
	{ std::lock_guard<std::mutex> l(fMu); fClosed = true; }
	fCv.notify_all();
	if (fOnClosed) fOnClosed();
}

void BusClient::Dispatch(const std::string &line)
{
	Json j; BusEnvelope e;
	if (line.empty()) return;
	if (!Json::Parse(line, &j) || !BusEnvelope::FromJson(j, &e)) return;   // garbage line: skipped
	if (e.kind == "response" || e.kind == "error") {
		std::lock_guard<std::mutex> l(fMu);
		auto it = fPending.find(e.corr);
		if (it != fPending.end()) { it->second->env = e; it->second->done = true; fCv.notify_all(); }
	} else if (e.kind == "request") {
		if (fOnRequest) fOnRequest(e);
	} else if (e.kind == "event") {
		if (fOnEvent) fOnEvent(e);
	}
}

status_t BusClient::Send(const BusEnvelope &e)
{
	if (!Connected()) return B_BAD_PORT_ID;
	std::string line = e.ToJson().Dump() + "\n";
	std::lock_guard<std::mutex> l(fWriteMu);
	for (size_t off = 0; off < line.size();) {
		ssize_t n = send(fFd, line.data() + off, line.size() - off, MSG_NOSIGNAL);
		if (n <= 0) return B_BAD_PORT_ID;
		off += n;
	}
	return B_OK;
}

status_t BusClient::Call(const BusEnvelope &req, BusEnvelope *reply, bigtime_t timeout)
{
	BusEnvelope r = req; Pending p;
	if (r.kind.empty()) r.kind = "request";
	{ std::lock_guard<std::mutex> l(fMu); r.id = "k" + std::to_string(++fNext); fPending[r.id] = &p; }
	status_t st = Send(r);
	std::unique_lock<std::mutex> l(fMu);
	if (st == B_OK) {
		auto dl = std::chrono::steady_clock::now() + std::chrono::microseconds(timeout);
		bool inf = timeout == B_INFINITE_TIMEOUT;
		while (!p.done && !fClosed) {
			if (inf) fCv.wait(l);
			else if (fCv.wait_until(l, dl) == std::cv_status::timeout) break;
		}
		st = p.done ? (p.env.kind == "error" ? B_ERROR : B_OK) : fClosed ? B_BAD_PORT_ID : B_TIMED_OUT;
	}
	fPending.erase(r.id);
	if (p.done && reply) *reply = p.env;
	return st;
}

status_t BusClient::Control(const char *verb, const Json &payload, BusEnvelope *reply)
{
	BusEnvelope r; r.to = "bus"; r.verb = verb; r.payload = payload;
	return Call(r, reply ? reply : &r);
}

status_t BusClient::Publish(const char *topic, const Json &payload, const char *qos, bool ephemeral)
{
	BusEnvelope e; e.kind = "event"; e.topic = topic; e.payload = payload; e.qos = qos; e.ephemeral = ephemeral;
	return Send(e);
}

status_t BusClient::Reply(const BusEnvelope &req, const Json &payload)
{
	BusEnvelope e; e.kind = "response"; e.verb = req.verb; e.corr = req.id; e.qos = req.qos;
	e.payload = payload;
	return Send(e);
}

status_t BusClient::ReplyError(const BusEnvelope &req, const char *code, const char *message)
{
	BusEnvelope e; e.kind = "error"; e.verb = req.verb; e.corr = req.id; e.qos = req.qos;
	e.payload = Json::MakeObject();
	e.payload.set("code", Json::MakeString(code)); e.payload.set("message", Json::MakeString(message));
	return Send(e);
}
