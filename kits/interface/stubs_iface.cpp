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

extern "C" {
/* REAL: absolute-time sleep on the monotonic clock (system_time() is CLOCK_MONOTONIC based). */
status_t snooze_until(bigtime_t time, int)
{
	bigtime_t now = system_time();
	return time > now ? snooze(time - now) : B_OK;
}
/* HOLLOW: our thread layer has no rename. BWindow renames its thread to "w>title"; the name is only cosmetic. */
status_t rename_thread(thread_id, const char*) { return B_OK; }
/* HOLLOW: thread-to-thread data passing, used by MenuBar's menu tracking. Menus are out of scope. */
status_t send_data(thread_id, int32, const void*, size_t) { return B_ERROR; }
int32 receive_data(thread_id*, void*, size_t) { return B_ERROR; }
bool has_data(thread_id) { return false; }
}

/* HOLLOW: no registrar. BMessageRunner (pulse, tooltips) and a few Window.cpp shortcuts ask it. */
status_t BRoster::Private::SendTo(BMessage*, BMessage*, bool) { return B_ERROR; }
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
