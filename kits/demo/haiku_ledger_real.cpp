// haiku_ledger_real: the ledger on Haiku's OWN kit code. BMessage, BHandler, BLooper and BMessenger
// here are Vitruvian's src/kits/app/{Message,Handler,Looper,Messenger}.cpp (MIT, Haiku Inc.), not the
// reimplementation in kits/src/app. There is no BApplication: a BLooper plus a BHandler is enough for a
// bus service, so app_server, the registrar and the storage kit are never involved.
//   bus send haiku-ledger-real add@v1 '{"label":"coffee","cents":350}'
#include <Looper.h>
#include <BusBridge.h>
#include "ledger_handler.h"
#include <pthread.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>

static BusBridge *gBridge = NULL;

int main(int argc, char **argv)
{
	const char *sock = argc > 2 && !strcmp(argv[1], "--socket") ? argv[2] : NULL;
	sigset_t set; sigemptyset(&set); sigaddset(&set, SIGTERM); sigaddset(&set, SIGINT);
	pthread_sigmask(SIG_BLOCK, &set, NULL);

	BLooper *looper = new BLooper("ledger-real");              // Haiku's Looper.cpp
	LedgerHandler *handler = new LedgerHandler(&gBridge,
		"Haiku BHandler::MessageReceived on Haiku's real BLooper (no BApplication)");
	looper->Lock(); looper->AddHandler(handler); looper->SetPreferredHandler(handler); looper->Unlock();
	looper->Run();

	gBridge = new BusBridge(looper, handler, "haiku-ledger-real");
	gBridge->MapVerb("add@v1", MSG_ADD_ENTRY);
	gBridge->MapVerb("total@v1", MSG_GET_TOTAL);
	gBridge->MapVerb("list@v1", MSG_LIST_ENTRIES);
	status_t r = gBridge->Start(sock);
	if (r != B_OK) { fprintf(stderr, "haiku-ledger-real: cannot join the bus (%d)\n", (int)r); return 1; }
	printf("READY haiku-ledger-real registered on the bus (real Haiku BMessage/BLooper/BHandler)\n");
	fflush(stdout);

	int sig; sigwait(&set, &sig);
	BusBridge *b = gBridge; gBridge = NULL; delete b;          // stop serving before the looper goes
	looper->Lock(); looper->Quit();                            // deletes the looper and its handlers
	return 0;
}
