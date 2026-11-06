/*
 * The slice of Haiku's registrar that BMessageRunner talks to, in-process. Written fresh.
 *
 * BMessageRunner (real, compiled in place from MessageRunner.cpp) does not run a timer: it sends a
 * B_REG_REGISTER_MESSAGE_RUNNER request through BRoster::Private::SendTo and the registrar's
 * MessageRunnerManager delivers the message every `interval`. BWindow::SetPulseRate uses one for B_PULSE, which
 * is what blinks BTextView's caret (Pulse() flips it after 500 ms); tooltips and BTextView's auto-scroll and
 * double-click-drag runners use it too. Without this SendTo the runner token is -1 and none of that ever fires.
 *
 * Served: REGISTER, UNREGISTER, SET_PARAMS, GET_INFO. Every other request still fails with B_ERROR (no registrar).
 * One timer thread, started on the first runner; it never exits (a detached daemon, like the registrar).
 */
#include <Message.h>
#include <Messenger.h>
#include <Roster.h>
#include <RosterPrivate.h>
#include <RegistrarDefs.h>
#include <OS.h>
#include <condition_variable>
#include <mutex>
#include <thread>
#include <vector>
#include <algorithm>
using namespace BPrivate;

namespace {
struct Runner {
	int32 token;
	BMessenger target, replyTo;
	BMessage message;
	bigtime_t interval, next;
	int32 count;   // < 0 forever
};
std::mutex gMu;
std::condition_variable gCv;
std::vector<Runner*> gRunners;
int32 gNextToken = 1;
bool gStarted = false;

Runner* find(int32 t) { for (auto* r : gRunners) if (r->token == t) return r; return nullptr; }

void timerLoop()
{
	std::unique_lock<std::mutex> lk(gMu);
	for (;;) {
		bigtime_t now = system_time(), soonest = now + 1000000;
		std::vector<Runner*> due;
		for (auto* r : gRunners) {
			if (r->next <= now) due.push_back(r);
			else soonest = std::min(soonest, r->next);
		}
		// copy what is needed to send, so the lock is not held while a looper's port may block
		struct Job { BMessenger t, rt; BMessage m; };
		std::vector<Job> jobs;
		for (auto* r : due) {
			jobs.push_back({r->target, r->replyTo, r->message});
			r->next = now + r->interval;
			if (r->count > 0 && --r->count == 0) {
				gRunners.erase(std::find(gRunners.begin(), gRunners.end(), r));
				delete r;
			}
		}
		lk.unlock();
		for (auto& j : jobs) j.t.SendMessage(&j.m, j.rt, 100000);
		lk.lock();
		if (jobs.empty())
			gCv.wait_for(lk, std::chrono::microseconds(std::max<bigtime_t>(soonest - system_time(), 1000)));
	}
}
}

status_t BRoster::Private::SendTo(BMessage* req, BMessage* reply, bool)
{
	if (!req || !reply) return B_BAD_VALUE;
	auto fail = [&](status_t e) { reply->what = B_REG_ERROR; reply->AddInt32("error", e); return B_OK; };
	std::lock_guard<std::mutex> lk(gMu);
	switch (req->what) {
		case B_REG_REGISTER_MESSAGE_RUNNER: {
			Runner* r = new Runner;
			int64 interval = 0; int32 count = 0;
			BMessage m;
			if (req->FindMessenger("target", &r->target) != B_OK || req->FindMessage("message", &m) != B_OK
				|| req->FindInt64("interval", &interval) != B_OK || req->FindInt32("count", &count) != B_OK) {
				delete r; return fail(B_BAD_VALUE);
			}
			req->FindMessenger("reply_target", &r->replyTo);
			r->message = m; r->interval = std::max<int64>(interval, 1000); r->count = count;
			r->next = system_time() + r->interval; r->token = gNextToken++;
			gRunners.push_back(r);
			if (!gStarted) { gStarted = true; std::thread(timerLoop).detach(); }
			gCv.notify_all();
			reply->what = B_REG_SUCCESS; reply->AddInt32("token", r->token);
			return B_OK;
		}
		case B_REG_UNREGISTER_MESSAGE_RUNNER: {
			int32 t; if (req->FindInt32("token", &t) != B_OK) return fail(B_BAD_VALUE);
			if (Runner* r = find(t)) { gRunners.erase(std::find(gRunners.begin(), gRunners.end(), r)); delete r; }
			reply->what = B_REG_SUCCESS; return B_OK;
		}
		case B_REG_SET_MESSAGE_RUNNER_PARAMS: {
			int32 t; if (req->FindInt32("token", &t) != B_OK) return fail(B_BAD_VALUE);
			Runner* r = find(t); if (!r) return fail(B_BAD_VALUE);
			int64 iv; int32 c;
			if (req->FindInt64("interval", &iv) == B_OK) { r->interval = std::max<int64>(iv, 1000); r->next = system_time() + r->interval; }
			if (req->FindInt32("count", &c) == B_OK) r->count = c;
			gCv.notify_all(); reply->what = B_REG_SUCCESS; return B_OK;
		}
		case B_REG_GET_MESSAGE_RUNNER_INFO: {
			int32 t; if (req->FindInt32("token", &t) != B_OK) return fail(B_BAD_VALUE);
			Runner* r = find(t); if (!r) return fail(B_BAD_VALUE);
			reply->what = B_REG_SUCCESS; reply->AddInt32("count", r->count); reply->AddInt64("interval", r->interval);
			return B_OK;
		}
	}
	return B_ERROR;
}
