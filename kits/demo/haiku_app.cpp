// A small Haiku-style application on the Aurora kits: a BApplication that keeps a ledger,
// and a second process that finds it by signature and talks to it with BMessenger.
//   haiku_app            launcher: runs the server and the client as two processes
//   haiku_app --serve    the application (BApplication::Run blocks until Quit)
//   haiku_app --client   finds the application and sends it messages
#include <Application.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <sys/wait.h>
#include <unistd.h>

static const char *kSignature = "application/x-vnd.Aurora-Ledger";

enum {
	MSG_ADD_ENTRY = 'AdEn',
	MSG_GET_TOTAL = 'GtTl',
	MSG_LIST_ENTRIES = 'LsEn'
};

class LedgerHandler : public BHandler {
public:
	LedgerHandler() : BHandler("ledger"), fTotal(0) {}
	virtual void MessageReceived(BMessage *message);
private:
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
			fEntries.AddMessage("entry", &entry);
			fTotal += cents;
			printf("[ledger pid %d] entry \"%s\" %+d, total now %d\n", getpid(), label, cents, fTotal);

			BMessage reply(B_REPLY);
			reply.AddInt32("total", fTotal);
			message->SendReply(&reply);
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
			reply.AddInt32("total", fTotal);
			message->SendReply(&reply);
			break;
		}

		default:
			BHandler::MessageReceived(message);
			break;
	}
}

class LedgerApp : public BApplication {
public:
	LedgerApp() : BApplication(kSignature), fHandler(new LedgerHandler)
	{
		// The port exists from here on, so a client may send before Run(): wire up first.
		Lock();
		AddHandler(fHandler);
		SetPreferredHandler(fHandler);
		Unlock();
	}
	virtual void ReadyToRun()
	{
		printf("[ledger pid %d] ReadyToRun: serving as %s\n", getpid(), Signature());
	}
	virtual void ArgvReceived(int32 argc, char **argv)
	{
		for (int32 i = 1; i < argc; i++)
			printf("[ledger pid %d] ArgvReceived: argv[%d] = %s\n", getpid(), i, argv[i]);
	}
	virtual bool QuitRequested()
	{
		printf("[ledger pid %d] QuitRequested\n", getpid());
		return true;
	}
private:
	LedgerHandler *fHandler;
};

static int serve()
{
	LedgerApp app;
	if (app.InitCheck() != B_OK) return 1;
	app.Run();
	printf("[ledger pid %d] Run() returned; exiting\n", getpid());
	return 0;
}

static int client()
{
	BMessenger ledger;
	for (int i = 0; i < 100 && !ledger.IsValid(); i++) {
		ledger = BMessenger(kSignature);
		if (!ledger.IsValid()) snooze(50000);
	}
	if (!ledger.IsValid()) { printf("[client] ledger not found\n"); return 1; }
	printf("[client pid %d] found %s in team %d (local: %s)\n", getpid(), kSignature,
		ledger.Team(), ledger.IsTargetLocal() ? "yes" : "no");

	static const struct { const char *label; int32 cents; } items[] = {
		{ "coffee", 350 }, { "book", 1999 }, { "refund", -500 }
	};
	for (const auto &item : items) {
		BMessage add(MSG_ADD_ENTRY), reply;
		add.AddString("label", item.label);
		add.AddInt32("cents", item.cents);
		if (ledger.SendMessage(&add, &reply) != B_OK) return 2;
		int32 total = 0;
		reply.FindInt32("total", &total);
		printf("[client pid %d] added %s -> reply total %d\n", getpid(), item.label, total);
	}

	BMessage bad(MSG_ADD_ENTRY), reply;
	bad.AddString("label", "no amount");
	ledger.SendMessage(&bad, &reply);
	int32 err = 0;
	reply.FindInt32("error", &err);
	printf("[client pid %d] malformed add -> reply what '%.4s', error %d\n", getpid(),
		reply.what == B_MESSAGE_NOT_UNDERSTOOD ? "_MNU" : "????", err);

	BMessage list(MSG_LIST_ENTRIES);
	if (ledger.SendMessage(&list, &reply) != B_OK) return 3;
	int32 total = 0, count = 0;
	reply.FindInt32("total", &total);
	reply.GetInfo("entry", NULL, &count);
	printf("[client pid %d] list: %d entries, total %d\n", getpid(), count, total);
	for (int32 i = 0; i < count; i++) {
		BMessage entry;
		const char *label = "?";
		int32 cents = 0;
		reply.FindMessage("entry", i, &entry);
		entry.FindString("label", &label);
		entry.FindInt32("cents", &cents);
		printf("[client pid %d]   %-8s %6d\n", getpid(), label, cents);
	}

	BMessage unknown('Nope');
	ledger.SendMessage(&unknown, &reply);
	printf("[client pid %d] unknown message -> %s\n", getpid(),
		reply.what == B_MESSAGE_NOT_UNDERSTOOD ? "B_MESSAGE_NOT_UNDERSTOOD" : "other");

	int rc = total == 1849 && count == 3 ? 0 : 4;
	ledger.SendMessage(B_QUIT_REQUESTED);
	printf("[client pid %d] asked the ledger to quit; %s\n", getpid(), rc ? "WRONG TOTAL" : "totals check out");
	return rc;
}

static pid_t spawn(const char *mode, const char *extra)
{
	pid_t pid = fork();
	if (pid == 0) {
		execl("/proc/self/exe", "haiku_app", mode, extra, (char *)NULL);
		_exit(99);
	}
	return pid;
}

int main(int argc, char **argv)
{
	setvbuf(stdout, NULL, _IOLBF, 0);
	if (argc >= 2 && !strcmp(argv[1], "--serve")) return serve();
	if (argc >= 2 && !strcmp(argv[1], "--client")) return client();

	char ns[64];
	snprintf(ns, sizeof ns, "ledger-%d", getpid());
	setenv("AURORA_KITS_NS", ns, 1);
	pid_t server = spawn("--serve", "--verbose");
	pid_t cl = spawn("--client", "");
	int cs = 0, ss = 0;
	waitpid(cl, &cs, 0);
	pid_t done = 0;
	for (int i = 0; i < 60 && !(done = waitpid(server, &ss, WNOHANG)); i++) snooze(50000);
	if (!done) { kill(server, SIGKILL); waitpid(server, &ss, 0); printf("[launcher] server did not quit\n"); }
	_kits_unlink_namespace();
	int ok = WIFEXITED(cs) && WEXITSTATUS(cs) == 0 && WIFEXITED(ss) && WEXITSTATUS(ss) == 0;
	printf("[launcher] client exit %d, server exit %d: %s\n", WIFEXITED(cs) ? WEXITSTATUS(cs) : -1,
		WIFEXITED(ss) ? WEXITSTATUS(ss) : -1, ok ? "OK" : "FAILED");
	return ok ? 0 : 1;
}
