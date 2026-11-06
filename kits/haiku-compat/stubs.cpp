/*
 * Stubs for what Haiku's app/support sources need that this project does not have.
 * Every function is tagged:
 *   REAL   - does what the name says (small, written here)
 *   HOLLOW - links, but does NOT do the job. Calling it returns an error or aborts.
 * Compiled against the shim headers + Haiku's real MIT headers; written fresh, no Haiku source copied.
 */
#include <OS.h>
#include <image.h>
#include <Application.h>
#include <Roster.h>
#include <Entry.h>
#include <Node.h>
#include <Path.h>
#include <File.h>
#include <AppFileInfo.h>
#include <LaunchRoster.h>
#include <AppMisc.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <pthread.h>

/* ---- kernel kit (OS.h) ---- */
extern "C" {
/* HOLLOW: vrefs are VOS's fd-carrying handle. get_vref_dev()==B_INVALID_DEV turns every vref path in
   Message.cpp off, so no message ever carries an fd. */
dev_t get_vref_dev() { return (dev_t)-1; }
status_t acquire_vref(vref_id) { return B_ERROR; }
status_t release_vref(vref_id) { return B_ERROR; }
/* REAL: abort with the message, as debugger() does with no debugger attached. */
void debugger(const char* m) { fprintf(stderr, "debugger(): %s\n", m); abort(); }
/* HOLLOW: only reached by BLooper::Quit() from the looper thread. Wrong for our thread layer. */
void exit_thread(status_t) { pthread_exit(NULL); }
/* HOLLOW: our primitives have no thread table query. */
status_t get_thread_info(thread_id, thread_info*) { return B_ERROR; }
/* HOLLOW: our ports record no sender; reports size only, sender fields zero. */
status_t _get_port_message_info_etc(port_id p, port_message_info* i, size_t, uint32 f, bigtime_t t)
{
	ssize_t s = port_buffer_size_etc(p, f, t);
	if (s < 0) return (status_t)s;
	memset(i, 0, sizeof(*i)); i->size = (size_t)s; return B_OK;
}
/* HOLLOW/BLOCKED: BMessage::_SendMessage uses an area transfer between teams for big messages. */
status_t _kern_transfer_area(area_id, void**, uint32, team_id) { return B_NOT_SUPPORTED; }
/* HOLLOW: add-on / image loading; only BArchivable::instantiate_object by name uses it. */
image_id load_add_on(const char*) { return B_ERROR; }
status_t get_image_symbol(image_id, const char*, int32, void**) { return B_ERROR; }
status_t _get_next_image_info(team_id, int32*, image_info*, size_t) { return B_ERROR; }
}
namespace BPrivate { team_id current_team() { return getpid(); } }  /* REAL */

/* ---- storage kit: BLOCKED (src/kits/storage needs _kern_* syscalls). entry_ref/node_ref are REAL
   but reduced: no vref, no team/real_device handling. ---- */
entry_ref::entry_ref() : device((dev_t)-1), directory((ino_t)-1), name(NULL), real_device((dev_t)-1),
	real_directory((ino_t)-1), team(-1) {}
entry_ref::entry_ref(dev_t d, ino_t dir, const char* n) : device(d), directory(dir),
	name(n ? strdup(n) : NULL), real_device(d), real_directory(dir), team(-1) {}
entry_ref::~entry_ref() { free(name); }
entry_ref& entry_ref::operator=(const entry_ref& r)
{
	if (this != &r) { free(name); device = r.device; directory = r.directory;
		name = r.name ? strdup(r.name) : NULL; }
	return *this;
}
node_ref::node_ref() : device((dev_t)-1), node((ino_t)-1) {}
node_ref::node_ref(dev_t d, ino_t n) : device(d), node(n) {}
node_ref::~node_ref() {}
node_ref& node_ref::operator=(const node_ref& r) { device = r.device; node = r.node; return *this; }

/* BEntry, BFile, BNode, BAppFileInfo are deliberately NOT stubbed (~50 hollow virtuals). Only Archivable.cpp's
   instantiate-by-name uses them; the link uses --gc-sections, so they are dropped, and a link error (not a crash)
   reports it if a reachable path ever needs one. BPath is below. */

/* ---- registrar / roster: BLOCKED. The registrar is not ported; these always fail. ---- */
app_info::app_info() {} app_info::~app_info() {}
BRoster::BRoster() {} BRoster::~BRoster() {}
status_t BRoster::GetAppInfo(const char*, app_info*) const { return B_ERROR; }
status_t BRoster::GetRunningAppInfo(team_id, app_info*) const { return B_ERROR; }
status_t BRoster::FindApp(const char*, entry_ref*) const { return B_ERROR; }
BLaunchRoster::BLaunchRoster() {} BLaunchRoster::~BLaunchRoster() {}
status_t BLaunchRoster::GetData(const char*, BMessage&) { return B_ERROR; }
static BRoster sRoster;
const BRoster* be_roster = &sRoster;
BApplication* be_app = NULL;
BMessenger be_app_messenger;

/* ---- BPath: REAL for plain-string paths, HOLLOW for entry_ref/BEntry/BDirectory (needs the storage kit).
   Haiku's own Path.cpp cannot be built (it pulls syscalls.h and BEntry). Message.cpp references BPath only in
   BMessage::PrintToStream() for a B_REF_TYPE field. A BPath built from an entry_ref stays B_NO_INIT and
   Path() is NULL (printf shows "(null)"); it does not crash. ---- */
BPath::BPath() : fName(NULL), fCStatus(B_NO_INIT) {}
BPath::BPath(const entry_ref*) : fName(NULL), fCStatus(B_NO_INIT) {}
BPath::~BPath() { free(fName); }
const char* BPath::Path() const { return fCStatus == B_OK ? fName : NULL; }
bool BPath::IsFixedSize() const { return false; }
type_code BPath::TypeCode() const { return B_REF_TYPE; }
ssize_t BPath::FlattenedSize() const { return B_NOT_SUPPORTED; }
status_t BPath::Flatten(void*, ssize_t) const { return B_NOT_SUPPORTED; }
bool BPath::AllowsTypeCode(type_code c) const { return c == B_REF_TYPE; }
status_t BPath::Unflatten(type_code, const void*, ssize_t) { return B_NOT_SUPPORTED; }
void BPath::_WarPath1() {} void BPath::_WarPath2() {} void BPath::_WarPath3() {}
