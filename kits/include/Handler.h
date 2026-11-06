/* BHandler: a message target owned by one BLooper. API shape per Haiku's Handler.h (subset). */
#ifndef _HANDLER_H
#define _HANDLER_H
#include <Message.h>

class BLooper;

class BHandler {
public:
	BHandler(const char *name = NULL);
	virtual ~BHandler();

	/* Default: forward to NextHandler(); at the end of the chain reply B_MESSAGE_NOT_UNDERSTOOD
	   to a waiting sender. */
	virtual void MessageReceived(BMessage *message);
	BLooper *Looper() const { return fLooper; }
	void SetName(const char *name);
	const char *Name() const { return fName.c_str(); }
	virtual void SetNextHandler(BHandler *handler) { fNext = handler; }
	BHandler *NextHandler() const { return fNext; }
	int32 Token() const { return fToken; }

private:
	friend class BLooper;
	BLooper *fLooper = NULL;
	BHandler *fNext = NULL;
	std::string fName;
	int32 fToken;
};
#endif
