/* A minimal C++ client for the Aurora bus (bus/README.md): newline-delimited JSON
   envelopes over a Unix stream socket. Connect, register a service, serve requests,
   call, publish, subscribe. Not supported: segments (fd passing), capabilities (the
   `caps` field is not carried), replay origin. Unknown envelope fields are dropped. */
#ifndef _BUS_CLIENT_H
#define _BUS_CLIENT_H
#include <BusJson.h>
#include <OS.h>
#include <atomic>
#include <condition_variable>
#include <functional>
#include <map>
#include <mutex>
#include <thread>

struct BusEnvelope {
	std::string id, ts, from, to, kind, verb, topic, qos, corr;
	Json payload;               // Null when absent
	bool ephemeral = false;
	Json ToJson() const;
	static bool FromJson(const Json &j, BusEnvelope *out);   // false if not an object with kind
};

class BusClient {
public:
	typedef std::function<void(const BusEnvelope &)> Callback;
	BusClient() {}
	~BusClient() { Close(); }

	/* path NULL: $AURORA_BUS_SOCK, else $XDG_RUNTIME_DIR/aurora/bus.sock. Set callbacks first.
	   Callbacks run on the reader thread and must not call Call(). */
	status_t Connect(const char *path = NULL);
	void Close();
	bool Connected() const { return fFd >= 0 && !fClosed; }
	void SetRequestCallback(Callback cb) { fOnRequest = cb; }
	void SetEventCallback(Callback cb) { fOnEvent = cb; }
	/* Called once, from the reader thread, when the connection drops. */
	void SetClosedCallback(std::function<void()> cb) { fOnClosed = cb; }

	status_t Send(const BusEnvelope &e);
	/* Sends a request (or control verb) and waits. B_OK: *reply is the response.
	   B_ERROR: the bus or service answered with an error envelope (in *reply, payload
	   {"code","message"}). B_TIMED_OUT, or B_BAD_PORT_ID if the connection is gone. */
	status_t Call(const BusEnvelope &req, BusEnvelope *reply, bigtime_t timeout = 10000000);
	status_t Control(const char *verb, const Json &payload, BusEnvelope *reply = NULL);
	status_t Register(const char *name) { return Control("bus.register@v1", NameObj("name", name)); }
	status_t Subscribe(const char *topic) { return Control("bus.subscribe@v1", NameObj("topic", topic)); }
	status_t Publish(const char *topic, const Json &payload, const char *qos = "", bool ephemeral = false);
	status_t Reply(const BusEnvelope &req, const Json &payload);
	status_t ReplyError(const BusEnvelope &req, const char *code, const char *message);

private:
	struct Pending { bool done = false; BusEnvelope env; };
	static Json NameObj(const char *k, const char *v) { Json o = Json::MakeObject(); o.set(k, Json::MakeString(v)); return o; }
	void ReadLoop();
	void Dispatch(const std::string &line);

	int fFd = -1;
	std::atomic<bool> fClosed{false};
	std::thread fReader;
	std::mutex fWriteMu, fMu;
	std::condition_variable fCv;
	std::map<std::string, Pending *> fPending;
	unsigned long fNext = 0;
	Callback fOnRequest, fOnEvent;
	std::function<void()> fOnClosed;
};
#endif
