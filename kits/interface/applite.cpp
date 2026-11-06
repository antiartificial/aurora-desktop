/*
 * BApplication without the registrar and the Storage Kit. Written fresh against Haiku's REAL Application.h
 * (so be_app's layout, BApplication::Private, and every vtable slot match what View.cpp/Window.cpp expect).
 * Haiku's own Application.cpp is NOT compiled: its _InitData needs BRoster (a registrar process), BFile,
 * BAppFileInfo and BMimeType, none of which exist here. What it does that the Interface Kit needs is:
 * set be_app, connect to the app_server with AS_CREATE_APP, sync fonts, run the looper. That is what this does.
 *
 * Everything that is a stub says HOLLOW. The server is in-process (appserver/server.cpp); see README.md.
 */
#include <Application.h>
#include <ApplicationPrivate.h>
#include <AppServerLink.h>
#include <InterfacePrivate.h>
#include <AppMisc.h>
#include <Cursor.h>
#include <Handler.h>
#include <Looper.h>
#include <LooperList.h>
#include <Menu.h>
#include <MenuWindow.h>
#include <Messenger.h>
#include <PortLink.h>
#include <ServerProtocol.h>
#include <Window.h>
#include <AutoLocker.h>
#include <ControlLook.h>
#include <HaikuControlLook.h>
#include <WidthBuffer.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "server.h"

using namespace BPrivate;


static const char* kDefaultLooperName = "AppLooperPort";

BResources* BApplication::sAppResources = NULL;

namespace BPrivate {
/* The connection step Haiku does with a message to the "application/x-vnd.Haiku-app_server" team. Here the
   server is a thread of this process and its main port is a variable. */
status_t
create_desktop_connection(ServerLink* link, const char* name, int32 capacity)
{
	port_id serverPort = aurora::appserver_port();
	if (serverPort < 0)
		return serverPort;
	port_id clientPort = create_port(capacity, name);
	if (clientPort < 0)
		return clientPort;
	link->SetTo(serverPort, clientPort);
	return B_OK;
}
}

BApplication::BApplication(const char* signature)
	: BLooper(kDefaultLooperName)
{
	_InitData(signature, true, NULL);
}

BApplication::BApplication(const char* signature, status_t* _error)
	: BLooper(kDefaultLooperName)
{
	_InitData(signature, true, _error);
}

BApplication::BApplication(const char* signature, const char* looperName, port_id port, bool initGUI,
	status_t* _error)
	: BLooper(B_NORMAL_PRIORITY + 1, port, looperName != NULL ? looperName : kDefaultLooperName)
{
	_InitData(signature, initGUI, _error);
}

BApplication::BApplication(BMessage* data)
	: BLooper(kDefaultLooperName)
{
	const char* signature = NULL;
	data->FindString("mime_sig", &signature);
	_InitData(signature, true, NULL);
}

BApplication::BApplication(const BApplication&) {}
BApplication& BApplication::operator=(const BApplication&) { return *this; }

BApplication::~BApplication()
{
	Lock();
	_QuitAllWindows(true);
	if (be_app != NULL && fServerLink != NULL) {
		BPrivate::AppServerLink link;
		link.StartMessage(B_QUIT_REQUESTED);
		link.Flush();
	}
	if (fServerLink != NULL) {
		delete_port(fServerLink->ReceiverPort());
		delete fServerLink;
	}
	free(const_cast<char*>(fAppName));
	be_app = NULL;
}

void
BApplication::_InitData(const char* signature, bool initGUI, status_t* _error)
{
	if (be_app != NULL)
		debugger("2 BApplication objects were created. Only one is allowed.");

	fServerLink = new BPrivate::PortLink(-1, -1);
	fServerAllocator = NULL;
	fServerReadOnlyMemory = NULL;	// ui_color() falls back to Haiku's compiled-in defaults
	fInitialWorkspace = 0;
	fReadyToRunCalled = false;
	fPulseRunner = NULL;
	fPulseRate = 0;
	fCursorData = NULL;
	fInitError = B_OK;
	fAppName = strdup(signature != NULL ? signature : "application/x-vnd.unnamed");

	be_app = this;
	be_app_messenger = BMessenger(NULL, this);
	SetName(fAppName);
	PostMessage(B_READY_TO_RUN, this);	// as Haiku's RUN_WITHOUT_REGISTRAR path does

	if (initGUI)
		fInitError = _InitGUIContext();

	if (_error != NULL)
		*_error = fInitError;
	else if (fInitError != B_OK) {
		fprintf(stderr, "BApplication: GUI init failed (%d)\n", (int)fInitError);
		exit(1);
	}
}

port_id BApplication::_GetPort(const char*) { return -1; }

BArchivable* BApplication::Instantiate(BMessage*) { return NULL; }
status_t BApplication::Archive(BMessage* data, bool deep) const { return BLooper::Archive(data, deep); }
status_t BApplication::InitCheck() const { return fInitError; }

thread_id
BApplication::Run()
{
	if (fInitError != B_OK)
		return fInitError;
	Loop();
	return fThread;
}

void
BApplication::Quit()
{
	bool unlock = false;
	if (!IsLocked()) {
		unlock = true;
		if (!Lock())
			return;
	}
	if (!fRunCalled)
		delete this;
	else if (find_thread(NULL) != fThread) {
		while (PostMessage(_QUIT_, this) == B_WOULD_BLOCK)
			snooze(10000);
	} else
		fTerminating = true;
	if (unlock)
		Unlock();
}

bool BApplication::QuitRequested() { return _QuitAllWindows(false); }
void BApplication::Pulse() {}
void BApplication::ReadyToRun() {}
void BApplication::MessageReceived(BMessage* message) { BLooper::MessageReceived(message); }
void BApplication::ArgvReceived(int32, char**) {}
void BApplication::AppActivated(bool) {}
void BApplication::RefsReceived(BMessage*) {}
void BApplication::AboutRequested() {}
BHandler* BApplication::ResolveSpecifier(BMessage* m, int32 i, BMessage* s, int32 f, const char* p)
	{ return BLooper::ResolveSpecifier(m, i, s, f, p); }
bool BApplication::ScriptReceived(BMessage*, int32, BMessage*, int32, const char*) { return false; }

void BApplication::ShowCursor() {}
void BApplication::HideCursor() {}
void BApplication::ObscureCursor() {}
bool BApplication::IsCursorHidden() const { return false; }
void BApplication::SetCursor(const void*) {}
void BApplication::SetCursor(const BCursor*, bool) {}
void BApplication::BeginRectTracking(BRect, bool) {}
void BApplication::EndRectTracking() {}

int32 BApplication::CountWindows() const { return _CountWindows(false); }
BWindow* BApplication::WindowAt(int32 index) const { return _WindowAt(index, false); }

int32
BApplication::CountLoopers() const
{
	AutoLocker<BLooperList> l(gLooperList);
	return l.IsLocked() ? gLooperList.CountLoopers() : B_ERROR;
}

BLooper*
BApplication::LooperAt(int32 index) const
{
	AutoLocker<BLooperList> l(gLooperList);
	return l.IsLocked() ? gLooperList.LooperAt(index) : NULL;
}

bool BApplication::IsLaunching() const { return !fReadyToRunCalled; }
const char* BApplication::Signature() const { return fAppName; }
status_t BApplication::GetAppInfo(app_info*) const { return B_ERROR; }	/* HOLLOW: no registrar */
BResources* BApplication::AppResources() { return NULL; }			/* HOLLOW */
status_t BApplication::RegisterLooper(BLooper*) { return B_ERROR; }		/* HOLLOW */
status_t BApplication::UnregisterLooper(BLooper*) { return B_ERROR; }	/* HOLLOW */
void BApplication::SetPulseRate(bigtime_t rate) { fPulseRate = rate; }		/* HOLLOW: no BMessageRunner */
status_t BApplication::GetSupportedSuites(BMessage* data) { return BLooper::GetSupportedSuites(data); }
status_t BApplication::Perform(perform_code d, void* arg) { return BLooper::Perform(d, arg); }
void BApplication::_ReservedApplication1() {}
void BApplication::_ReservedApplication2() {}
void BApplication::_ReservedApplication3() {}
void BApplication::_ReservedApplication4() {}
void BApplication::_ReservedApplication5() {}
void BApplication::_ReservedApplication6() {}
void BApplication::_ReservedApplication7() {}
void BApplication::_ReservedApplication8() {}
uint32 BApplication::InitialWorkspace() { return fInitialWorkspace; }
void BApplication::_InitAppResources() {}
status_t BApplication::_SetupServerAllocator() { return B_OK; }
void BApplication::_ReconnectToServer() {}
void BApplication::_ArgvReceived(BMessage*) {}

void
BApplication::DispatchMessage(BMessage* message, BHandler* handler)
{
	if (handler != this) {
		BLooper::DispatchMessage(message, handler);
		return;
	}
	switch (message->what) {
		case B_READY_TO_RUN:
			if (!fReadyToRunCalled) {
				ReadyToRun();
				fReadyToRunCalled = true;
			}
			break;
		case B_ABOUT_REQUESTED: AboutRequested(); break;
		case B_PULSE: Pulse(); break;
		case B_ARGV_RECEIVED: _ArgvReceived(message); break;
		default: BLooper::DispatchMessage(message, handler); break;
	}
}

/* Real logic, as in Haiku's Application.cpp. */
bool
BApplication::_WindowQuitLoop(bool quitFilePanels, bool force)
{
	int32 index = 0;
	while (true) {
		BWindow* window = WindowAt(index);
		if (window == NULL)
			break;
		if (!window->Lock())
			continue;
		if (!quitFilePanels && window->IsFilePanel()) {
			window->Unlock();
			index++;
			continue;
		}
		if (!force && !window->QuitRequested() && !(quitFilePanels && window->IsFilePanel())) {
			window->Unlock();
			return false;
		}
		if (window->Lock())
			window->Quit();
		index = 0;
	}
	return true;
}

bool
BApplication::_QuitAllWindows(bool force)
{
	AssertLocked();
	Unlock();
	bool quit = _WindowQuitLoop(false, force);
	if (quit)
		quit = _WindowQuitLoop(true, force);
	Lock();
	return quit;
}

int32
BApplication::_CountWindows(bool includeMenus) const
{
	uint32 count = 0;
	for (int32 i = 0; i < gLooperList.CountLoopers(); i++) {
		BWindow* window = dynamic_cast<BWindow*>(gLooperList.LooperAt(i));
		if (window != NULL && !window->fOffscreen && (includeMenus || dynamic_cast<BMenuWindow*>(window) == NULL))
			count++;
	}
	return count;
}

BWindow*
BApplication::_WindowAt(uint32 index, bool includeMenus) const
{
	AutoLocker<BLooperList> listLock(gLooperList);
	if (!listLock.IsLocked())
		return NULL;
	uint32 seen = 0;
	for (int32 i = 0; i < gLooperList.CountLoopers(); i++) {
		BWindow* window = dynamic_cast<BWindow*>(gLooperList.LooperAt(i));
		if (window == NULL || window->fOffscreen || (!includeMenus && dynamic_cast<BMenuWindow*>(window) != NULL))
			continue;
		if (seen++ == index)
			return window;
	}
	return NULL;
}

status_t
BApplication::_InitGUIContext()
{
	status_t error = _ConnectToServer();
	if (error != B_OK)
		return error;
	/* The part of Haiku's _init_interface_kit_() that matters without a registrar, a clipboard, BBitmap
	   menus and a palette: global fonts (from the server), the control look, the text width cache. */
	_init_global_fonts_();
	if (be_control_look == NULL)
		be_control_look = new HaikuControlLook();
	BPrivate::gWidthBuffer = new BPrivate::WidthBuffer;
	return B_OK;
}

status_t
BApplication::_ConnectToServer()
{
	status_t status = create_desktop_connection(fServerLink, "a<app_server", 100);
	if (status != B_OK)
		return status;

	fServerLink->StartMessage(AS_CREATE_APP);
	fServerLink->Attach<port_id>(fServerLink->ReceiverPort());
	fServerLink->Attach<port_id>(_get_looper_port_(this));
	fServerLink->Attach<team_id>(Team());
	fServerLink->Attach<int32>(_get_object_token_(this));
	fServerLink->AttachString(fAppName);

	area_id sharedReadOnlyArea;
	team_id serverTeam;
	port_id serverPort;
	int32 code;
	if (fServerLink->FlushWithReply(code) == B_OK && code == B_OK) {
		fServerLink->Read<port_id>(&serverPort);
		fServerLink->Read<area_id>(&sharedReadOnlyArea);
		fServerLink->Read<team_id>(&serverTeam);
	} else {
		fServerLink->SetSenderPort(-1);
		return B_ERROR;
	}
	fServerLink->SetTargetTeam(serverTeam);
	fServerLink->SetSenderPort(serverPort);
	return B_OK;
}
