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
#include "ledger_handler.h"
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const char *kSignature = "application/x-vnd.Aurora-HaikuLedger";

class LedgerApp : public BApplication {
public:
	LedgerApp(const char *sock) : BApplication(kSignature), fSock(sock ? sock : ""), fBridge(NULL),
		fHandler(new LedgerHandler(&fBridge, "BHandler::MessageReceived in a BApplication"))
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
