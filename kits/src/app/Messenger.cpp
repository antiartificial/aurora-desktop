#include <Messenger.h>
#include "msgwire.h"
#include <string.h>
#include <stdio.h>

void app_port_name(const char *sig, char out[B_OS_NAME_LENGTH])
{
	uint64 h = 1469598103934665603ULL;
	for (const char *p = sig; p && *p; p++) { h ^= (uint8)*p; h *= 1099511628211ULL; }
	snprintf(out, B_OS_NAME_LENGTH, "A:%016llx", (unsigned long long)h);
}

status_t wire_send(port_id port, int32 token, const BMessage *m, port_id replyPort,
	int32 replyToken, bool wantsReply, bigtime_t timeout)
{
	ssize_t flat = m->FlattenedSize();
	std::string buf(sizeof(WireHeader) + (size_t)flat, '\0');
	WireHeader h = { (uint32)kWireMagic, token, replyPort, replyToken,
		wantsReply ? kWireWantsReply : 0, (int32)getpid(), (uint32)flat };
	memcpy(&buf[0], &h, sizeof h);
	status_t r = m->Flatten(&buf[sizeof h], flat);
	if (r != B_OK) return r;
	return write_port_etc(port, kPortCodeMessage, buf.data(), buf.size(),
		timeout == B_INFINITE_TIMEOUT ? 0 : B_RELATIVE_TIMEOUT, timeout);
}

status_t wire_receive(port_id port, bigtime_t timeout, BMessage *out, int32 *token, bool *quit)
{
	uint32 flags = timeout == B_INFINITE_TIMEOUT ? 0 : B_RELATIVE_TIMEOUT;
	ssize_t size = port_buffer_size_etc(port, flags, timeout);
	if (size < 0) return (status_t)size;
	std::string buf((size_t)size, '\0');
	int32 code = 0;
	ssize_t n = read_port_etc(port, &code, &buf[0], buf.size(), B_RELATIVE_TIMEOUT, 0);
	if (n < 0) return (status_t)n;
	if (quit) *quit = code == kPortCodeQuit;
	if (code == kPortCodeQuit) return B_OK;
	WireHeader h;
	if (code != kPortCodeMessage || (size_t)n < sizeof h) return B_BAD_DATA;
	memcpy(&h, buf.data(), sizeof h);
	if (h.magic != (uint32)kWireMagic || sizeof h + h.flatSize != (size_t)n) return B_BAD_DATA;
	status_t r = out->Unflatten(buf.data() + sizeof h, (ssize_t)h.flatSize);
	if (r != B_OK) return r;
	MessageAccess::setDelivery(*out, h.replyPort, h.replyToken, h.flags & kWireWantsReply, h.senderTeam);
	if (token) *token = h.token;
	return B_OK;
}

status_t BMessage::SendReply(BMessage *reply, BHandler *, bigtime_t timeout)
{
	if (fReplyPort < 0 || !reply) return B_BAD_REPLY;
	status_t r = wire_send(fReplyPort, fReplyToken, reply, -1, -1, false, timeout);
	if (r == B_OK) fReplied = true;
	return r;
}

status_t BMessage::SendReply(uint32 command, BHandler *replyTo)
{
	BMessage m(command);
	return SendReply(&m, replyTo);
}

BMessenger BMessage::ReturnAddress() const { return BMessenger(fReplyPort, fReplyToken); }

BMessenger::BMessenger() {}
BMessenger::BMessenger(port_id port, int32 token) : fPort(port), fToken(token) {}

BMessenger::BMessenger(const char *signature, team_id, status_t *result)
{
	char name[B_OS_NAME_LENGTH]; app_port_name(signature ? signature : "", name);
	port_id p = signature ? find_port(name) : B_BAD_VALUE;
	if (p >= 0) fPort = p;
	if (result) *result = p >= 0 ? B_OK : B_BAD_VALUE;
}

BMessenger::BMessenger(const BHandler *handler, const BLooper *looper, status_t *result)
{
	status_t r = B_BAD_VALUE;
	const BLooper *l = handler ? handler->Looper() : looper;
	if (handler && looper && handler->Looper() != looper) r = B_MISMATCHED_VALUES;
	else if (l) {
		fPort = l->Port(); fToken = handler ? handler->Token() : -1;   // no handler: the preferred one
		r = B_OK;
	}
	if (result) *result = r;
}

bool BMessenger::IsValid() const { return fPort >= 0 && port_count(fPort) >= 0; }
bool BMessenger::IsTargetLocal() const { return fPort >= 0 && BLooper::ForPort(fPort) != NULL; }

BHandler *BMessenger::Target(BLooper **looper) const
{
	BLooper *l = BLooper::ForPort(fPort);
	if (looper) *looper = l;
	if (!l) return NULL;
	return l->HandlerForToken(fToken);
}

bool BMessenger::LockTarget() const
{
	BLooper *l = BLooper::ForPort(fPort);
	return l && l->Lock();
}

team_id BMessenger::Team() const
{
	port_info pi;
	return fPort >= 0 && get_port_info(fPort, &pi) == B_OK ? pi.team : B_BAD_PORT_ID;
}

status_t BMessenger::SendMessage(uint32 command, BHandler *replyTo) const
{
	BMessage m(command);
	return SendMessage(&m, replyTo);
}

status_t BMessenger::SendMessage(BMessage *message, BHandler *replyTo, bigtime_t timeout) const
{
	BMessenger rt;
	if (replyTo) {
		if (!replyTo->Looper()) return B_MISMATCHED_VALUES;
		rt = BMessenger(replyTo, NULL);
	}
	return SendMessage(message, rt, timeout);
}

status_t BMessenger::SendMessage(BMessage *message, BMessenger replyTo, bigtime_t timeout) const
{
	if (!message || fPort < 0) return B_BAD_PORT_ID;
	return wire_send(fPort, fToken, message, replyTo.fPort, replyTo.fToken, false, timeout);
}

status_t BMessenger::SendMessage(uint32 command, BMessage *reply) const
{
	BMessage m(command);
	return SendMessage(&m, reply);
}

status_t BMessenger::SendMessage(BMessage *message, BMessage *reply, bigtime_t deliveryTimeout,
	bigtime_t replyTimeout) const
{
	if (!message || !reply || fPort < 0) return B_BAD_VALUE;
	BLooper *local = BLooper::ForPort(fPort);
	if (local && local->Thread() == find_thread(NULL)) return B_ERROR; // would wait on itself
	port_id rp = create_port(1, "reply");
	if (rp < 0) return rp;
	status_t r = wire_send(fPort, fToken, message, rp, -1, true, deliveryTimeout);
	if (r == B_OK) r = wire_receive(rp, replyTimeout, reply, NULL, NULL);
	delete_port(rp);
	return r;
}
