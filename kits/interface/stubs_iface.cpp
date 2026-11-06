/*
 * What the Interface Kit's closure references that has no counterpart here. Every item is tagged HOLLOW (links,
 * does not do the job) or REAL (does what the name says). Written fresh; compiled against Haiku's real headers.
 */
#include <OS.h>
#include <List.h>
#include <Roster.h>
#include <RosterPrivate.h>
#include <Message.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <pthread.h>
#include <time.h>
#include <string.h>
#include <stdint.h>
#include <algorithm>

extern "C" {
/* REAL: absolute-time sleep on the monotonic clock (system_time() is CLOCK_MONOTONIC based). */
status_t snooze_until(bigtime_t time, int)
{
	bigtime_t now = system_time();
	return time > now ? snooze(time - now) : B_OK;
}
/* HOLLOW: our thread layer has no rename. BWindow renames its thread to "w>title"; the name is only cosmetic. */
status_t rename_thread(thread_id, const char*) { return B_OK; }
/* REAL: thread-to-thread data passing (BMenuBar hands its tracking thread the menu bar this way). One queue per
   target thread; send_data copies and returns at once (as BeOS does for small buffers), receive_data blocks. */
}
#include <mutex>
#include <condition_variable>
#include <deque>
#include <map>
#include <vector>
namespace {
struct Pkt { thread_id from; int32 code; std::vector<char> data; };
std::mutex gDataMu; std::condition_variable gDataCv; std::map<thread_id, std::deque<Pkt>> gData;
}
extern "C" {
status_t send_data(thread_id to, int32 code, const void* buf, size_t size)
{
	std::lock_guard<std::mutex> g(gDataMu);
	Pkt p{find_thread(NULL), code, std::vector<char>((const char*)buf, (const char*)buf + (buf ? size : 0))};
	gData[to].push_back(std::move(p));
	gDataCv.notify_all();
	return B_OK;
}
int32 receive_data(thread_id* sender, void* buf, size_t size)
{
	thread_id me = find_thread(NULL);
	std::unique_lock<std::mutex> lk(gDataMu);
	gDataCv.wait(lk, [&] { return !gData[me].empty(); });
	Pkt p = std::move(gData[me].front()); gData[me].pop_front();
	if (sender) *sender = p.from;
	if (buf) memcpy(buf, p.data.data(), std::min(size, p.data.size()));
	return p.code;
}
bool has_data(thread_id t) { std::lock_guard<std::mutex> g(gDataMu); auto it = gData.find(t); return it != gData.end() && !it->second.empty(); }
}

/* REAL (a slice): BRoster::Private::SendTo is in registrar.cpp: it serves the message-runner requests, which
   is what BMessageRunner (B_PULSE, the caret blink, tooltips, auto-scroll) sends. Everything else is refused. */
status_t BRoster::Launch(const char*, BMessage*, team_id*) const { return B_ERROR; }
void BRoster::GetAppList(const char*, BList*) const {}

/* HOLLOW (aborts if reached): BArchivable's instantiate-by-name (Archivable.cpp: instantiate_object,
   check_signature) names BEntry, BFile and BAppFileInfo. Those are Storage Kit classes (polymorphic, ~50
   virtuals, 27+ _kern_* syscalls), so they are NOT built; defining the symbols directly avoids vtables. They
   are reachable only after load_add_on() succeeds, and load_add_on() is hollow and always fails, so the abort
   is unreachable today. Using the real Storage Kit instead is listed in README.md. */
extern "C" void aurora_hollow_storage(void)
{
	fprintf(stderr, "aurora: Storage Kit call reached (BEntry/BFile/BAppFileInfo are hollow); aborting\n");
	abort();
}
#define HOLLOW(sym) __asm__(".text\n.globl " #sym "\n.type " #sym ",@function\n" #sym ":\n\tjmp aurora_hollow_storage\n");
HOLLOW(_ZN12BAppFileInfoD1Ev) HOLLOW(_ZN12BAppFileInfoD2Ev) HOLLOW(_ZN12BAppFileInfoC1EP5BFile)
HOLLOW(_ZNK12BAppFileInfo12GetSignatureEPc)
HOLLOW(_ZN6BEntryD1Ev) HOLLOW(_ZN6BEntryD2Ev) HOLLOW(_ZN6BEntryC1Ev) HOLLOW(_ZNK6BEntry7GetPathEP5BPath)
HOLLOW(_ZN6BEntry5SetToEPK9entry_refb)
HOLLOW(_ZN5BFileD1Ev) HOLLOW(_ZN5BFileD2Ev) HOLLOW(_ZN5BFileC1EPKcj) HOLLOW(_ZNK5BNode9InitCheckEv)

/* HOLLOW: no sound. BTextView beeps on refused edits (e.g. Backspace in an empty view); that is all `beep()` is for. */
status_t beep() { return B_OK; }

/* REAL: libroot's byte-swap helpers (arch/*.S in Haiku); BTextView flattens run arrays with them. */
extern "C" float __swap_float(float f) { uint32_t v; memcpy(&v, &f, 4); v = __builtin_bswap32(v); memcpy(&f, &v, 4); return f; }
extern "C" double __swap_double(double f) { uint64_t v; memcpy(&v, &f, 8); v = __builtin_bswap64(v); memcpy(&f, &v, 8); return f; }

/* HOLLOW: no locale kit. BCatalog::GetString is the identity (English), which is what Haiku does with no catalog
   loaded. Menu.cpp and TextView's context menu name BPrivate::gSystemCatalog for their labels. */
#include <Catalog.h>
#include <SystemCatalog.h>
BCatalog::BCatalog() : fCatalogData(nullptr) {}
BCatalog::~BCatalog() {}
const char* BCatalog::GetString(const char* string, const char*, const char*) { return string; }
namespace BPrivate { BCatalog gSystemCatalog; }
