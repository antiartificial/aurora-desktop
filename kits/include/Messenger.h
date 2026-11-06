/* BMessenger: addresses a looper/handler by (port, token), in this or another process.
   API shape per Haiku's Messenger.h (subset). */
#ifndef _MESSENGER_H
#define _MESSENGER_H
#include <Looper.h>

class BMessenger {
public:
	BMessenger();
	/* By application signature. Haiku asks the registrar; here a well-known port name
	   derived from the signature is looked up with find_port(). */
	BMessenger(const char *signature, team_id team = -1, status_t *result = NULL);
	BMessenger(const BHandler *handler, const BLooper *looper = NULL, status_t *result = NULL);
	/* Aurora extension: address a raw port and token (what crosses a process boundary). */
	BMessenger(port_id port, int32 token);

	bool IsValid() const;
	bool IsTargetLocal() const;
	BHandler *Target(BLooper **looper) const;
	bool LockTarget() const;
	team_id Team() const;
	port_id Port() const { return fPort; }
	int32 Token() const { return fToken; }

	status_t SendMessage(uint32 command, BHandler *replyTo = NULL) const;
	status_t SendMessage(BMessage *message, BHandler *replyTo = NULL,
		bigtime_t timeout = B_INFINITE_TIMEOUT) const;
	status_t SendMessage(BMessage *message, BMessenger replyTo,
		bigtime_t timeout = B_INFINITE_TIMEOUT) const;
	status_t SendMessage(uint32 command, BMessage *reply) const;
	/* Synchronous: blocks until the target replies (reply->what == B_NO_REPLY if its handler
	   did not), or replyTimeout expires (B_TIMED_OUT). */
	status_t SendMessage(BMessage *message, BMessage *reply,
		bigtime_t deliveryTimeout = B_INFINITE_TIMEOUT,
		bigtime_t replyTimeout = B_INFINITE_TIMEOUT) const;

	bool operator==(const BMessenger &o) const { return fPort == o.fPort && fToken == o.fToken; }
	bool operator!=(const BMessenger &o) const { return !(*this == o); }

private:
	port_id fPort = -1;
	int32 fToken = -1;
};
#endif
