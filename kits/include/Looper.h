/*
 * BLooper: a port, a thread, a handler list. API shape per Haiku's Looper.h (subset).
 * The port is one of this library's ports (OS.h), reachable from other processes.
 */
#ifndef _LOOPER_H
#define _LOOPER_H
#include <Handler.h>
#include <atomic>
#include <pthread.h>

#define B_LOOPER_PORT_DEFAULT_CAPACITY 200

class BLooper : public BHandler {
public:
	BLooper(const char *name = NULL, int32 priority = B_NORMAL_PRIORITY,
		int32 portCapacity = B_LOOPER_PORT_DEFAULT_CAPACITY);
	virtual ~BLooper();

	/* Spawns the loop thread. Returns its id, or an error. */
	virtual thread_id Run();
	/* From the loop thread: finish the current message, stop, delete this.
	   From another thread: stop the loop, wait for the thread, delete this. */
	virtual void Quit();
	virtual bool QuitRequested();
	virtual void DispatchMessage(BMessage *message, BHandler *handler);
	virtual void MessageReceived(BMessage *message);

	/* Call with Lock() held. The looper deletes added handlers when it is deleted. */
	void AddHandler(BHandler *handler);
	bool RemoveHandler(BHandler *handler);
	int32 CountHandlers() const { return (int32)fHandlers.size(); }
	BHandler *HandlerAt(int32 index) const;
	void SetPreferredHandler(BHandler *handler) { fPreferred = handler; }
	BHandler *PreferredHandler() const { return fPreferred; }

	status_t PostMessage(uint32 what);
	status_t PostMessage(BMessage *message);
	status_t PostMessage(uint32 what, BHandler *handler, BHandler *replyTo = NULL);
	status_t PostMessage(BMessage *message, BHandler *handler, BHandler *replyTo = NULL);

	/* Recursive lock; the loop holds it while dispatching. */
	bool Lock();
	void Unlock();
	bool IsLocked() const;

	thread_id Thread() const { return fThread; }
	team_id Team() const { return getpid(); }
	BMessage *CurrentMessage() const { return fCurrent; }
	static BLooper *LooperForThread(thread_id thread);
	/* Aurora extension: the looper's port (Haiku keeps it private). */
	port_id Port() const { return fPort; }
	bool IsQuitting() const { return fTerminating; }

protected:
	BLooper(const char *name, const char *portName, int32 priority, int32 portCapacity);
	void RunLoop();            // the message loop on the calling thread
	void FinishLoop();         // drain queue, answering waiting senders
	// Written by whoever asks the loop to stop, read by the loop thread between messages.
	std::atomic<bool> fTerminating{false};
	bool fRunCalled = false;
	thread_id fThread = -1;

private:
	friend class BMessenger;
	void Init(const char *portName, int32 priority, int32 portCapacity);
	BHandler *HandlerForToken(int32 token) const;
	static status_t ThreadEntry(void *self);
	static BLooper *ForPort(port_id port);

	port_id fPort = -1;
	int32 fPriority;
	std::vector<BHandler *> fHandlers;
	BHandler *fPreferred = NULL;
	BMessage *fCurrent = NULL;
	bool fDeleteOnExit = false;
	pthread_mutex_t fLock;
	thread_id fOwner = -1; int fLockCount = 0;
};
#endif
