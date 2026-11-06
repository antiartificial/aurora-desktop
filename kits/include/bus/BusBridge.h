/* Bridge between the Aurora bus and a BLooper.
   A bus request addressed to `service` becomes a BMessage posted to a BHandler; the
   handler's SendReply() becomes the bus response. See kits/README.md "Bus bridge" for
   the type mapping and every lossy case. */
#ifndef _BUS_BRIDGE_H
#define _BUS_BRIDGE_H
#include <BusClient.h>
#include <Looper.h>
#include <Messenger.h>
#include <condition_variable>
#include <deque>

enum {
	B_BUS_REQUEST = '_BRQ',   // `what` of a request whose verb has no MapVerb()
	B_BUS_EVENT = '_BEV',     // `what` of a subscribed event
	B_BUS_ERROR = '_BER'      // `what` of the reply Call() builds from a bus error
};

/* JSON <-> BMessage. Pure functions, no bus needed. Fields named "bus:*" are metadata. */
status_t BusJsonToMessage(const Json &payload, BMessage *out, std::string *err);
Json BusMessageToJson(const BMessage &msg);
/* Mark a field so it is written as a JSON array even with one item (a one-item field and
   a scalar are otherwise indistinguishable in a BMessage). Consumed on conversion. */
void BusMarkArray(BMessage *msg, const char *name);

class BusBridge {
public:
	/* target: the handler requests are posted to (NULL: the looper's preferred handler). The
	   target must already be added to the looper when Start() is called. Written to compile
	   against both our BLooper and Haiku's real one (haiku-compat/): public API only. */
	BusBridge(BLooper *looper, BHandler *target, const char *service);
	~BusBridge() { Stop(); }

	/* Verb -> `what`. Call before Start(). Unmapped verbs arrive as B_BUS_REQUEST with the
	   verb in the "bus:verb" string field (every request has it). */
	void MapVerb(const char *verb, uint32 what) { fVerbs[verb] = what; }
	void SetReplyTimeout(bigtime_t t) { fReplyTimeout = t; }
	/* Connect, register `service`, start serving. B_OK, or why not. */
	status_t Start(const char *socketPath = NULL);
	void Stop();

	/* Events matching the glob are posted to the target as B_BUS_EVENT ("bus:topic"). */
	status_t Subscribe(const char *topic) { return fClient.Subscribe(topic); }
	status_t Publish(const char *topic, const BMessage &msg, const char *qos = "", bool ephemeral = false);
	/* The BMessenger-shaped direction: call a bus verb synchronously. On B_OK *reply
	   (what B_REPLY) holds the response. On B_ERROR *reply has what B_BUS_ERROR and string
	   fields "code" and "message". Also B_TIMED_OUT, B_BAD_PORT_ID. Do not call from a
	   handler of the service being called (the looper would wait on itself). */
	status_t Call(const char *service, const char *verb, const BMessage &request, BMessage *reply,
		bigtime_t timeout = 10000000);
	BusClient &Client() { return fClient; }

private:
	void Serve();
	void HandleRequest(const BusEnvelope &req);
	void OnEvent(const BusEnvelope &ev);

	BLooper *fLooper; BHandler *fTarget; std::string fService;
	BMessenger fTo;   // built in Start(): the portable way (handler, looper) to address a looper
	std::map<std::string, uint32> fVerbs;
	bigtime_t fReplyTimeout = 10000000;
	BusClient fClient;
	std::thread fServer;
	std::mutex fMu; std::condition_variable fCv;
	std::deque<BusEnvelope> fQueue; bool fStop = false, fStarted = false;
};
#endif
