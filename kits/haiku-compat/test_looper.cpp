// Real Haiku BLooper/BHandler/BMessenger (Looper.cpp, Handler.cpp, Messenger.cpp) on our ports/threads.
#include <Looper.h>
#include <Handler.h>
#include <Messenger.h>
#include <Message.h>
#include <OS.h>
#include <MessagePrivate.h>
#include <stdio.h>

struct H : BHandler {
	int32 got = 0;
	void MessageReceived(BMessage* m) override {
		if (m->what == 'ping') {
			got++;
			BMessage r('pong'); r.AddInt32("n", got);
			m->SendReply(&r);
		} else BHandler::MessageReceived(m);
	}
};

int main()
{
	// InitTerminateLibBe.cpp (a constructor) would do this; it is not linked, so do it by hand.
	BMessage::Private::StaticInit();
	BLooper* l = new BLooper("real-looper");
	H* h = new H;
	l->Lock(); l->AddHandler(h); l->SetPreferredHandler(h); l->Unlock();
	printf("Run() ...\n"); fflush(stdout);
	thread_id t = l->Run();
	printf("Run() -> thread %d\n", (int)t); fflush(stdout);
	BMessenger me(h, l);
	printf("IsValid=%d\n", me.IsValid()); fflush(stdout);
	BMessage req('ping'), reply;
	status_t st = me.SendMessage(&req, &reply, 2000000, 2000000);
	printf("SendMessage(sync) -> %d (%s), reply.what=%.4s\n", (int)st, st == B_OK ? "B_OK" : "error", (char*)&reply.what);
	int32 n = -1; reply.FindInt32("n", &n); printf("reply n=%d, handler got=%d\n", (int)n, (int)h->got);
	l->Lock(); l->Quit();
	return st == B_OK ? 0 : 1;
}
