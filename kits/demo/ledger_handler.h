// The ledger's BHandler, shared by haiku_ledger (our kits) and haiku_ledger_real (Haiku's real
// BMessage/BHandler/BLooper). It uses only public Haiku API, so the same source compiles against both.
#ifndef LEDGER_HANDLER_H
#define LEDGER_HANDLER_H
#include <AppDefs.h>
#include <Handler.h>
#include <Message.h>
#include <BusBridge.h>
#include <stdio.h>
#include <unistd.h>

enum { MSG_ADD_ENTRY = 'AdEn', MSG_GET_TOTAL = 'GtTl', MSG_LIST_ENTRIES = 'LsEn' };

class LedgerHandler : public BHandler {
public:
	LedgerHandler(BusBridge **bridge, const char *via)
		: BHandler("ledger"), fBridge(bridge), fVia(via), fTotal(0) {}
	virtual void MessageReceived(BMessage *message);
private:
	BusBridge **fBridge;
	const char *fVia;
	BMessage fEntries;
	int32 fTotal;
};

inline void LedgerHandler::MessageReceived(BMessage *message)
{
	switch (message->what) {
		case MSG_ADD_ENTRY:
		{
			const char *label;
			int32 cents;
			if (message->FindString("label", &label) != B_OK
				|| message->FindInt32("cents", &cents) != B_OK) {
				BMessage reply(B_MESSAGE_NOT_UNDERSTOOD);
				reply.AddInt32("error", B_BAD_VALUE);
				message->SendReply(&reply);
				break;
			}
			BMessage entry;
			entry.AddString("label", label);
			entry.AddInt32("cents", cents);
			fEntries.AddMessage("entries", &entry);
			fTotal += cents;
			const char *from = "?";
			message->FindString("bus:from", &from);
			printf("[haiku-ledger pid %d] BHandler got MSG_ADD_ENTRY \"%s\" %+d from %s, total %d\n",
				getpid(), label, cents, from, fTotal);
			fflush(stdout);

			BMessage reply(B_REPLY);
			reply.AddInt32("total", fTotal);
			reply.AddString("handled_by", fVia);
			message->SendReply(&reply);

			// Tell the rest of the system. Same BMessage shape; becomes a bus event.
			BMessage event;
			event.AddString("label", label);
			event.AddInt32("cents", cents);
			event.AddInt32("total", fTotal);
			if (*fBridge) (*fBridge)->Publish("ledger.entry.added", event);
			break;
		}

		case MSG_GET_TOTAL:
		{
			BMessage reply(B_REPLY);
			reply.AddInt32("total", fTotal);
			message->SendReply(&reply);
			break;
		}

		case MSG_LIST_ENTRIES:
		{
			BMessage reply(fEntries);
			reply.what = B_REPLY;
			BusMarkArray(&reply, "entries");   // a one-entry ledger is still a JSON array
			reply.AddInt32("total", fTotal);
			message->SendReply(&reply);
			break;
		}

		default:
			BHandler::MessageReceived(message);   // unknown verb: the bus requester gets an error
			break;
	}
}

#endif
