// Replaces Haiku's Input.cpp, which the Interface Kit reaches through exactly one function: _control_input_server_(),
// a BMessage round trip to the input_server. There is no input_server here, and when it is unreachable
// get_click_speed() returns an error WITHOUT setting its output, so BListView compared a click's age against an
// uninitialised double-click interval. This answers the few commands the kit's widgets and BWindow use, from what
// the server knows (the keyboard modifiers it was told about). Everything else is refused. BInputDevice (also in
// Input.cpp) is not provided: nothing in the widgets uses it.
#include <Message.h>
#include <OS.h>
#include <SupportDefs.h>
#include <input_globals.h>
#include <InputServerTypes.h>
#include "appserver/server.h"

status_t
_control_input_server_(BMessage* command, BMessage* reply)
{
	switch (command->what) {
		case IS_GET_CLICK_SPEED:
			reply->AddInt64("speed", 500000);		// the double-click interval app_server counts clicks with
			reply->AddInt32("status", B_OK);
			return B_OK;
		case IS_GET_MODIFIERS:
			reply->AddInt32("modifiers", (int32)aurora::current_modifiers());
			reply->AddInt32("status", B_OK);
			return B_OK;
		default:
			return B_ERROR;
	}
}
