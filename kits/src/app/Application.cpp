#include <Application.h>
#include "msgwire.h"
#include <stdio.h>
#include <string.h>

BApplication *be_app = NULL;
BMessenger be_app_messenger;

static std::string portNameFor(const char *sig)
{
	char buf[B_OS_NAME_LENGTH];
	app_port_name(sig ? sig : "", buf);
	return buf;
}

BApplication::BApplication(const char *signature, status_t *error)
	: BLooper(signature ? signature : "application", portNameFor(signature).c_str(),
		B_NORMAL_PRIORITY, B_LOOPER_PORT_DEFAULT_CAPACITY),
	  fSignature(signature ? signature : "")
{
	if (!signature) fInitError = B_BAD_VALUE;
	else if (be_app) fInitError = B_ERROR;       // one application per process
	else if (Port() < 0) fInitError = Port();
	if (fInitError == B_OK) {
		be_app = this;
		be_app_messenger = BMessenger(Port(), -1);
	}
	if (error) *error = fInitError;
}

BApplication::~BApplication()
{
	if (be_app == this) { be_app = NULL; be_app_messenger = BMessenger(); }
}

bool BApplication::QuitRequested() { return true; }
void BApplication::ReadyToRun() {}
void BApplication::ArgvReceived(int32, char **) {}
void BApplication::MessageReceived(BMessage *m) { BLooper::MessageReceived(m); }

// Haiku gets argv from the registrar's launch message; with no registrar we read our own.
static void postArgv(BApplication *app)
{
	FILE *f = fopen("/proc/self/cmdline", "r");
	if (!f) return;
	BMessage m(B_ARGV_RECEIVED); int32 argc = 0; char buf[4096]; size_t n = fread(buf, 1, sizeof buf - 1, f);
	fclose(f); buf[n] = 0;
	for (size_t i = 0; i < n; i += strlen(buf + i) + 1) { m.AddString("argv", buf + i); argc++; }
	if (argc > 1) { m.AddInt32("argc", argc); app->PostMessage(&m, app); }
}

thread_id BApplication::Run()
{
	if (fInitError != B_OK) return fInitError;
	if (fRunCalled) return B_BAD_THREAD_STATE;
	fRunCalled = true;
	fRunThread = fThread = find_thread(NULL);
	postArgv(this);
	PostMessage(B_READY_TO_RUN, this);   // to the application itself, never the preferred handler
	RunLoop();
	FinishLoop();
	thread_id t = fThread;
	fThread = -1;
	return t;
}

void BApplication::Quit()
{
	if (!fRunCalled || fRunThread == find_thread(NULL)) fTerminating = true;
	else PostMessage(B_QUIT_REQUESTED, this);
}

void BApplication::DispatchMessage(BMessage *message, BHandler *handler)
{
	if (handler != this) { handler->MessageReceived(message); return; }
	switch (message->what) {
		case B_ARGV_RECEIVED: {
			int32 argc = 0; message->FindInt32("argc", &argc);
			std::vector<char *> argv;
			const char *s;
			for (int32 i = 0; i < argc && message->FindString("argv", i, &s) == B_OK; i++)
				argv.push_back(const_cast<char *>(s));
			ArgvReceived((int32)argv.size(), argv.data());
			break;
		}
		case B_READY_TO_RUN: ReadyToRun(); break;
		default: MessageReceived(message);
	}
}
