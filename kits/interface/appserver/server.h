/*
 * The app_server analogue: an in-process server that speaks Haiku's real link protocol (the AS_* opcodes
 * that Window.cpp, View.cpp and Font.cpp emit) over our ports, keeps the server-side view tree, rasterises
 * with the software canvas and hands finished frames to a presenter (Wayland wl_shm, or a PPM dump).
 * Written fresh; wire formats were read from Haiku's src/servers/app (ServerApp.cpp, ServerWindow.cpp,
 * DrawState.cpp) and from the client side that emits them.
 */
#ifndef AURORA_APPSERVER_H
#define AURORA_APPSERVER_H

#include <OS.h>
#include <stdint.h>
#include <string>
#include <vector>

namespace aurora {

// Main port of the server; starts it (threads, optional Wayland connection) on first call.
port_id appserver_port();

struct Frame {
	int width = 0, height = 0;	// device pixels
	double scale = 1.0;		// device pixels per layout unit
	std::string title;
	std::vector<uint32_t> argb;	// 0xAARRGGBB, row-major
};
// Copy of the last finished frame of window `index` (creation order). False if there is none yet.
bool last_frame(int index, Frame* out);
// Number of AS_* opcodes the server did not understand (and how many of those wanted a reply).
int unhandled_opcodes(int* withReply);
void inject_pointer(int window, float x, float y, int button);	// -1 move, 0 up, 1 down; window coords
// Test hook for the keys the compositor's --test-input cannot name (BackSpace, Delete, Home, End, PageUp/Down).
// Posts the same event the Wayland key callback does, with the char xkb gives those keys, so everything above the
// presenter (HandleKey, B_KEY_DOWN, BWindow, the focus view) is the real path. It does NOT go through the compositor
// or libxkbcommon. name: BackSpace Delete Home End PageUp PageDown. shift: hold Shift (B_SHIFT_KEY) for this key.
bool inject_special_key(int window, const char* name, bool shift);
uint32_t current_modifiers();	// Haiku modifier bits as of the last B_MODIFIERS_CHANGED / key event (modifiers())
double output_scale();		// the scale the server renders at

}	// namespace aurora

#endif
