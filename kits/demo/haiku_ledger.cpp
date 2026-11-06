// A Haiku-style application that is also a first-class bus service.
//
// Nothing below mentions JSON or sockets except in main()'s one-line bridge setup: the
// handler is ordinary BeOS code, `switch (message->what)`, FindString/FindInt32,
// SendReply. The BusBridge turns bus requests into BMessages and replies back.
//
//   bus send haiku-ledger add@v1 '{"label":"coffee","cents":350}'
//   bus send haiku-ledger list@v1 '{}'
//   bus sub 'ledger.**'
#include <Application.h>
#include <BusBridge.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const char *kSignature = "application/x-vnd.Aurora-HaikuLedger";

enum { MSG_ADD_ENTRY = 'AdEn', MSG_GET_TOTAL = 'GtTl', MSG_LIST_ENTRIES = 'LsEn' };

class LedgerApp;

class LedgerHandler : public BHandler {
public:
	LedgerHandler(BusBridge **bridge) : BHandler("ledger"), fBridge(bridge), fTotal(0) {}
	virtual void MessageReceived(BMessage *message);
private:
	BusBridge **fBridge;
	BMessage fEntries;
	int32 fTotal;
};

void LedgerHandler::MessageReceived(BMessage *message)
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
			reply.AddString("handled_by", "BHandler::MessageReceived in a BApplication");
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

class LedgerApp : public BApplication {
public:
	LedgerApp(const char *sock) : BApplication(kSignature), fSock(sock ? sock : ""), fBridge(NULL),
		fHandler(new LedgerHandler(&fBridge))
	{
		Lock(); AddHandler(fHandler); Unlock();   // the looper owns it and deletes it, as in Haiku
	}
	// The bridge is stopped here, after Run() returned: stopping it from QuitRequested could wait
	// on a request that is waiting on this very looper.
	~LedgerApp() { delete fBridge; }
	virtual void ReadyToRun()
	{
		fBridge = new BusBridge(this, fHandler, "haiku-ledger");
		fBridge->MapVerb("add@v1", MSG_ADD_ENTRY);
		fBridge->MapVerb("total@v1", MSG_GET_TOTAL);
		fBridge->MapVerb("list@v1", MSG_LIST_ENTRIES);
		status_t r = fBridge->Start(fSock.empty() ? NULL : fSock.c_str());
		if (r != B_OK) { fprintf(stderr, "haiku-ledger: cannot join the bus (%d)\n", (int)r); exit(1); }
		printf("READY haiku-ledger registered on the bus\n");
		fflush(stdout);
	}
private:
	std::string fSock;
	BusBridge *fBridge;
	LedgerHandler *fHandler;
};

int main(int argc, char **argv)
{
	const char *sock = argc > 2 && !strcmp(argv[1], "--socket") ? argv[2] : NULL;
	// SIGTERM/SIGINT -> a B_QUIT_REQUESTED, from a thread (a signal handler may not post).
	sigset_t set; sigemptyset(&set); sigaddset(&set, SIGTERM); sigaddset(&set, SIGINT);
	pthread_sigmask(SIG_BLOCK, &set, NULL);
	LedgerApp app(sock);
	std::thread([&set] { int s; sigwait(&set, &s); be_app->PostMessage(B_QUIT_REQUESTED); }).detach();
	app.Run();
	return 0;
}
