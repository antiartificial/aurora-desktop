/*
 * Library initialisation for a running app on Haiku's real app kit.
 *
 * Haiku does this in src/kits/app/InitTerminateLibBe.cpp, a pair of ELF constructors in libbe. That file
 * cannot be used as is: initialize_before() also calls BRoster::Private::InitBeRoster() (the registrar)
 * and initialize_after() the locale kit. This file does the parts a non-drawing app needs, in the same
 * order, and is always linked into the executable's own sources (a constructor in a static library
 * is dropped by the linker unless something references its object). Written fresh; mirrors the above.
 * Without it the first heap BMessage segfaults in BBlockCache::Get (sMsgCache is NULL).
 */
#include <pthread.h>
#include <LooperList.h>
#include <MessagePrivate.h>
#include <TokenSpace.h>

static void child_after_fork()
{
	BMessage::Private::StaticReInitForkedChild();
	BPrivate::gLooperList.InitAfterFork();
	BPrivate::gDefaultTokens.InitAfterFork();
}

// Priority 101: before every ordinary static constructor, as libbe's constructor(2) is.
static void __attribute__((constructor(101))) real_kits_init()
{
	BMessage::Private::StaticInit();
	pthread_atfork(NULL, NULL, child_after_fork);
}

static void __attribute__((destructor(101))) real_kits_term()
{
	BMessage::Private::StaticCleanup();
	BMessage::Private::StaticCacheCleanup();
}
