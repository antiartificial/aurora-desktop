#include <Messenger.h>
#include "msgwire.h"
#include <algorithm>
#include <map>
#include <string.h>

// Process-wide registry so BMessenger can tell a local target from a remote one.
static pthread_mutex_t sRegLock = PTHREAD_MUTEX_INITIALIZER;
static std::map<port_id, BLooper *> sByPort;
static std::map<thread_id, BLooper *> sByThread;

BLooper::BLooper(const char *name, int32 priority, int32 portCapacity)
	: BHandler(name ? name : "BLooper")
{
	Init(NULL, priority, portCapacity);
}

BLooper::BLooper(const char *name, const char *portName, int32 priority, int32 portCapacity)
	: BHandler(name)
{
	Init(portName, priority, portCapacity);
}

void BLooper::Init(const char *portName, int32 priority, int32 portCapacity)
{
	fPriority = priority;
	pthread_mutexattr_t a; pthread_mutexattr_init(&a);
	pthread_mutexattr_settype(&a, PTHREAD_MUTEX_RECURSIVE);
	pthread_mutex_init(&fLock, &a); pthread_mutexattr_destroy(&a);
	char pn[B_OS_NAME_LENGTH];
	snprintf(pn, sizeof pn, "%s", portName ? portName : Name());
	fPort = create_port(portCapacity, pn);
	fLooper = this;
	if (fPort >= 0) {
		pthread_mutex_lock(&sRegLock); sByPort[fPort] = this; pthread_mutex_unlock(&sRegLock);
	}
}

BLooper::~BLooper()
{
	if (fRunCalled && fThread >= 0 && fThread != find_thread(NULL) && !fTerminating) {
		write_port_etc(fPort, kPortCodeQuit, NULL, 0, B_RELATIVE_TIMEOUT, 0);
		status_t rv; wait_for_thread(fThread, &rv);
	}
	pthread_mutex_lock(&sRegLock);
	sByPort.erase(fPort); sByThread.erase(fThread);
	pthread_mutex_unlock(&sRegLock);
	if (fPort >= 0) delete_port(fPort);
	for (BHandler *h : fHandlers) delete h;
	pthread_mutex_destroy(&fLock);
}

BLooper *BLooper::ForPort(port_id port)
{
	pthread_mutex_lock(&sRegLock);
	auto it = sByPort.find(port); BLooper *l = it == sByPort.end() ? NULL : it->second;
	pthread_mutex_unlock(&sRegLock);
	return l;
}

BLooper *BLooper::LooperForThread(thread_id t)
{
	pthread_mutex_lock(&sRegLock);
	auto it = sByThread.find(t); BLooper *l = it == sByThread.end() ? NULL : it->second;
	pthread_mutex_unlock(&sRegLock);
	return l;
}

bool BLooper::Lock()
{
	pthread_mutex_lock(&fLock);
	fOwner = find_thread(NULL); fLockCount++;
	return true;
}

void BLooper::Unlock()
{
	if (!IsLocked()) return;
	if (--fLockCount == 0) fOwner = -1;
	pthread_mutex_unlock(&fLock);
}

bool BLooper::IsLocked() const { return fOwner == find_thread(NULL) && fLockCount > 0; }

void BLooper::AddHandler(BHandler *h)
{
	if (!h || h->fLooper) return;
	h->fLooper = this; h->SetNextHandler(this);
	fHandlers.push_back(h);
}

bool BLooper::RemoveHandler(BHandler *h)
{
	auto it = std::find(fHandlers.begin(), fHandlers.end(), h);
	if (it == fHandlers.end()) return false;
	fHandlers.erase(it);
	h->fLooper = NULL; h->SetNextHandler(NULL);
	if (fPreferred == h) fPreferred = NULL;
	return true;
}

BHandler *BLooper::HandlerAt(int32 i) const
{
	return i >= 0 && (size_t)i < fHandlers.size() ? fHandlers[i] : NULL;
}

BHandler *BLooper::HandlerForToken(int32 token) const
{
	if (token == -1) return fPreferred ? fPreferred : const_cast<BLooper *>(this);
	if (token == Token()) return const_cast<BLooper *>(this);
	for (BHandler *h : fHandlers) if (h->Token() == token) return h;
	return const_cast<BLooper *>(this);   // unknown target: the looper's default handler
}

status_t BLooper::PostMessage(uint32 what) { BMessage m(what); return PostMessage(&m); }
status_t BLooper::PostMessage(BMessage *m) { return PostMessage(m, (BHandler *)NULL, NULL); }
status_t BLooper::PostMessage(uint32 what, BHandler *h, BHandler *replyTo)
{
	BMessage m(what); return PostMessage(&m, h, replyTo);
}

status_t BLooper::PostMessage(BMessage *m, BHandler *h, BHandler *replyTo)
{
	if (!m) return B_BAD_VALUE;
	if (h && h->Looper() != this) return B_MISMATCHED_VALUES;
	BMessenger target(h, this), rt;
	if (replyTo) {
		if (!replyTo->Looper()) return B_MISMATCHED_VALUES;
		rt = BMessenger(replyTo, NULL);
	}
	return target.SendMessage(m, rt);
}

void BLooper::DispatchMessage(BMessage *message, BHandler *handler)
{
	handler->MessageReceived(message);
}

// End of every handler chain: B_QUIT_REQUESTED sent to any handler of this looper lands here.
void BLooper::MessageReceived(BMessage *message)
{
	if (message->what == B_QUIT_REQUESTED) { if (QuitRequested()) Quit(); return; }
	BHandler::MessageReceived(message);
}
bool BLooper::QuitRequested() { return true; }

void BLooper::RunLoop()
{
	while (!fTerminating) {
		BMessage msg; int32 token = -1; bool quit = false;
		status_t r = wire_receive(fPort, B_INFINITE_TIMEOUT, &msg, &token, &quit);
		if (quit || r == B_BAD_PORT_ID) break;
		if (r != B_OK) continue;                  // malformed message: drop it
		Lock();
		BHandler *h = HandlerForToken(token);
		fCurrent = &msg;
		DispatchMessage(&msg, h);
		if (msg.IsSourceWaiting()) {              // handler never replied: say so
			BMessage nr(B_NO_REPLY);
			msg.SendReply(&nr);
		}
		fCurrent = NULL;
		Unlock();
	}
	fTerminating = true;
}

void BLooper::FinishLoop()
{
	while (IsLocked()) Unlock();
	for (;;) {                                    // wake senders blocked on a reply
		BMessage msg; int32 token; bool quit;
		if (wire_receive(fPort, 0, &msg, &token, &quit) != B_OK) break;
		if (!quit && msg.IsSourceWaiting()) { BMessage nr(B_NO_REPLY); msg.SendReply(&nr, NULL, 0); }
	}
}

status_t BLooper::ThreadEntry(void *self)
{
	BLooper *l = (BLooper *)self;
	l->RunLoop();
	l->FinishLoop();
	if (l->fDeleteOnExit) delete l;
	return B_OK;
}

thread_id BLooper::Run()
{
	if (fRunCalled) return B_BAD_THREAD_STATE;
	if (fPort < 0) return B_BAD_PORT_ID;
	fRunCalled = true;
	char tn[B_OS_NAME_LENGTH]; snprintf(tn, sizeof tn, "%s", Name());
	fThread = spawn_thread(ThreadEntry, tn, fPriority, this);
	if (fThread < 0) { fRunCalled = false; return fThread; }
	pthread_mutex_lock(&sRegLock); sByThread[fThread] = this; pthread_mutex_unlock(&sRegLock);
	resume_thread(fThread);
	return fThread;
}

void BLooper::Quit()
{
	if (!fRunCalled) { delete this; return; }
	if (fThread == find_thread(NULL)) {           // from inside the loop
		fTerminating = true; fDeleteOnExit = true;
		return;
	}
	while (IsLocked()) Unlock();                  // the loop must be able to proceed
	fTerminating = true;
	write_port_etc(fPort, kPortCodeQuit, NULL, 0, B_RELATIVE_TIMEOUT, 0);
	status_t rv; wait_for_thread(fThread, &rv);
	delete this;
}
