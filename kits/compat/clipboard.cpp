/*
 * BClipboard, in-process. Written fresh against Haiku's real Clipboard.h.
 *
 * Haiku's Clipboard.cpp talks to the registrar's clipboard handler. There is one process here, so "the system
 * clipboard" is a process-global BMessage per name: Commit() publishes a copy of fData, Lock() refreshes from it.
 * That is enough for BTextView's Cut/Copy/Paste inside one application; it is NOT a clipboard shared between
 * processes, and StartWatching/StopWatching (B_CLIPBOARD_CHANGED) are refused.
 */
#include <Clipboard.h>
#include <Message.h>
#include <map>
#include <mutex>
#include <string>
#include <string.h>
#include <stdlib.h>

namespace {
std::mutex gMu;
struct Slot { BMessage data; uint32 count = 0; };
std::map<std::string, Slot>& slots() { static std::map<std::string, Slot> m; return m; }
}

BClipboard* be_clipboard = new BClipboard("system");

BClipboard::BClipboard(const char* name, bool) : _reserved0(0), fData(new BMessage), fLock("clipboard"), fCount(0), fName(strdup(name ? name : "system"))
{
	std::lock_guard<std::mutex> g(gMu);
	slots()[fName];
}
BClipboard::~BClipboard() { delete fData; free(fName); }
const char* BClipboard::Name() const { return fName; }
uint32 BClipboard::LocalCount() const { return fCount; }
uint32 BClipboard::SystemCount() const { std::lock_guard<std::mutex> g(gMu); return slots()[fName].count; }
status_t BClipboard::StartWatching(BMessenger) { return B_NOT_SUPPORTED; }
status_t BClipboard::StopWatching(BMessenger) { return B_NOT_SUPPORTED; }
bool BClipboard::Lock() { if (!fLock.Lock()) return false; _DownloadFromSystem(); return true; }
void BClipboard::Unlock() { fLock.Unlock(); }
bool BClipboard::IsLocked() const { return fLock.IsLocked(); }
status_t BClipboard::Clear() { if (!_AssertLocked()) return B_NOT_ALLOWED; fData->MakeEmpty(); return B_OK; }
status_t BClipboard::Commit() { return Commit(false); }
status_t BClipboard::Commit(bool failIfChanged)
{
	if (!_AssertLocked()) return B_NOT_ALLOWED;
	std::lock_guard<std::mutex> g(gMu);
	Slot& s = slots()[fName];
	if (failIfChanged && s.count != fCount) return B_ERROR;
	s.data = *fData; fCount = ++s.count;
	return B_OK;
}
status_t BClipboard::Revert() { if (!_AssertLocked()) return B_NOT_ALLOWED; return _DownloadFromSystem(true); }
BMessenger BClipboard::DataSource() const { return fDataSource; }
BMessage* BClipboard::Data() const { return _AssertLocked() ? fData : nullptr; }
void BClipboard::_ReservedClipboard1() {}
void BClipboard::_ReservedClipboard2() {}
void BClipboard::_ReservedClipboard3() {}
bool BClipboard::_AssertLocked() const { return fLock.IsLocked(); }
status_t BClipboard::_DownloadFromSystem(bool force)
{
	std::lock_guard<std::mutex> g(gMu);
	Slot& s = slots()[fName];
	if (force || s.count != fCount) { *fData = s.data; fCount = s.count; }
	return B_OK;
}
status_t BClipboard::_UploadToSystem() { return Commit(); }
