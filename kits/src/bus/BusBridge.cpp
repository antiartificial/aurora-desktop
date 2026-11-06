#include <BusBridge.h>
#include <string.h>

// "bus:*" fields are the bridge's own metadata. A payload must not be able to forge them
// (a JSON key "bus:from" would otherwise be item 0 of the field the handler reads).
static void stripMeta(BMessage *m)
{
	std::vector<std::string> names; char *n; type_code t;
	for (int32 i = 0; m->GetInfo(B_ANY_TYPE, i, &n, &t) == B_OK; i++)
		if (strncmp(n, "bus:", 4) == 0) names.push_back(n);
	for (auto &x : names) m->RemoveName(x.c_str());
}

BusBridge::BusBridge(BLooper *looper, BHandler *target, const char *service)
	: fLooper(looper), fTarget(target), fService(service) {}

status_t BusBridge::Start(const char *socketPath)
{
	if (fStarted) return B_ERROR;
	fTo = BMessenger(fTarget, fLooper);
	if (!fTo.IsValid()) return B_BAD_VALUE;
	// Requests are queued by the reader thread and served by a separate thread: serving
	// blocks until the looper replies, and the reader must stay free to read responses
	// to Call()s that handlers make.
	fClient.SetRequestCallback([this](const BusEnvelope &e) {
		std::lock_guard<std::mutex> l(fMu); fQueue.push_back(e); fCv.notify_one();
	});
	fClient.SetEventCallback([this](const BusEnvelope &e) { OnEvent(e); });
	status_t r = fClient.Connect(socketPath);
	if (r != B_OK) return r;
	fStarted = true; fStop = false;
	fServer = std::thread(&BusBridge::Serve, this);
	r = fClient.Register(fService.c_str());
	if (r != B_OK) { Stop(); return r; }
	return B_OK;
}

void BusBridge::Stop()
{
	if (!fStarted) return;
	{ std::lock_guard<std::mutex> l(fMu); fStop = true; fCv.notify_all(); }
	fClient.Close();
	if (fServer.joinable()) fServer.join();
	fStarted = false;
}

void BusBridge::Serve()
{
	for (;;) {
		BusEnvelope req;
		{
			std::unique_lock<std::mutex> l(fMu);
			fCv.wait(l, [this] { return fStop || !fQueue.empty(); });
			if (fStop) return;
			req = fQueue.front(); fQueue.pop_front();
		}
		HandleRequest(req);          // one at a time: bus requests reach the looper in arrival order
	}
}

void BusBridge::HandleRequest(const BusEnvelope &req)
{
	BMessage msg, reply; std::string err;
	if (BusJsonToMessage(req.payload, &msg, &err) != B_OK) {
		fClient.ReplyError(req, "bad_payload", err.c_str());
		return;
	}
	stripMeta(&msg);
	auto vm = fVerbs.find(req.verb);
	msg.what = vm != fVerbs.end() ? vm->second : (uint32)B_BUS_REQUEST;
	msg.AddString("bus:verb", req.verb.c_str());
	msg.AddString("bus:from", req.from.c_str());
	msg.AddString("bus:qos", req.qos.empty() ? "user-initiated" : req.qos.c_str());

	status_t r = fTo.SendMessage(&msg, &reply, 5000000, fReplyTimeout);
	if (r == B_TIMED_OUT) { fClient.ReplyError(req, "timeout", "handler did not reply in time"); return; }
	if (r != B_OK) { fClient.ReplyError(req, "deliver_failed", "could not deliver to the looper"); return; }
	if (reply.what == B_NO_REPLY) {
		// The handler returned without SendReply. A BeOS sender gets B_NO_REPLY and moves on;
		// a bus requester would wait forever, so it becomes an error.
		fClient.ReplyError(req, "no_reply", "handler did not reply");
	} else if (reply.what == B_MESSAGE_NOT_UNDERSTOOD) {
		int32 e = 0; reply.FindInt32("error", &e);
		std::string m = "handler did not understand " + req.verb;
		if (e) m += " (error " + std::to_string(e) + ")";
		fClient.ReplyError(req, "not_understood", m.c_str());
	} else {
		fClient.Reply(req, BusMessageToJson(reply));
	}
}

void BusBridge::OnEvent(const BusEnvelope &ev)
{
	BMessage msg; std::string err;
	if (BusJsonToMessage(ev.payload, &msg, &err) != B_OK) return;   // unrepresentable event: dropped
	stripMeta(&msg);
	msg.what = B_BUS_EVENT;
	msg.AddString("bus:topic", ev.topic.c_str());
	msg.AddString("bus:from", ev.from.c_str());
	fLooper->PostMessage(&msg, fTarget);
}

status_t BusBridge::Publish(const char *topic, const BMessage &msg, const char *qos, bool ephemeral)
{
	return fClient.Publish(topic, BusMessageToJson(msg), qos, ephemeral);
}

status_t BusBridge::Call(const char *service, const char *verb, const BMessage &request,
	BMessage *reply, bigtime_t timeout)
{
	BusEnvelope req, resp; req.to = service; req.verb = verb;
	req.payload = BusMessageToJson(request);
	status_t r = fClient.Call(req, &resp, timeout);
	reply->MakeEmpty();
	if (r == B_OK) {
		reply->what = B_REPLY;
		std::string err;
		if (BusJsonToMessage(resp.payload, reply, &err) != B_OK) return B_BAD_VALUE;
	} else if (r == B_ERROR) {
		reply->what = B_BUS_ERROR;
		reply->AddString("code", resp.payload.getString("code").c_str());
		reply->AddString("message", resp.payload.getString("message").c_str());
	}
	return r;
}
