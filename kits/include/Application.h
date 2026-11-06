/*
 * BApplication: a BLooper with a signature whose Run() blocks on the calling thread.
 * Non-drawing only; no app_server, no registrar (see kits/README.md for what is stubbed).
 */
#ifndef _APPLICATION_H
#define _APPLICATION_H
#include <Messenger.h>

class BApplication : public BLooper {
public:
	BApplication(const char *signature, status_t *error = NULL);
	virtual ~BApplication();

	/* Posts B_ARGV_RECEIVED (if argc > 1) and B_READY_TO_RUN to itself, then runs the loop
	   on this thread until Quit(). */
	virtual thread_id Run();
	/* On the loop thread: stop after the current message. From another thread: posts
	   B_QUIT_REQUESTED. Never deletes the application. */
	virtual void Quit();
	virtual bool QuitRequested();
	virtual void ReadyToRun();
	virtual void ArgvReceived(int32 argc, char **argv);
	virtual void DispatchMessage(BMessage *message, BHandler *handler);
	virtual void MessageReceived(BMessage *message);

	const char *Signature() const { return fSignature.c_str(); }
	status_t InitCheck() const { return fInitError; }

private:
	std::string fSignature;
	status_t fInitError = B_OK;
	thread_id fRunThread = -1;
};

extern BApplication *be_app;
extern BMessenger be_app_messenger;
#endif
