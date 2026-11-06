#include <Handler.h>
#include <atomic>
#include <stdio.h>

static std::atomic<int32> sNextToken(1);

BHandler::BHandler(const char *name) : fName(name ? name : ""), fToken(sNextToken++) {}
BHandler::~BHandler() {}

void BHandler::SetName(const char *name) { fName = name ? name : ""; }

void BHandler::MessageReceived(BMessage *message)
{
	if (fNext && fNext != this) { fNext->MessageReceived(message); return; }
	if (message->IsSourceWaiting()) {
		BMessage reply(B_MESSAGE_NOT_UNDERSTOOD);
		reply.AddInt32("error", B_ERROR);
		char text[64]; snprintf(text, sizeof text, "unhandled what 0x%08x", message->what);
		reply.AddString("message", text);
		message->SendReply(&reply);
	}
}
