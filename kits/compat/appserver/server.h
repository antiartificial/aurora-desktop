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
#include <map>
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

// ---- the accessibility tree (decision 0017) -------------------------------------------------------------------------
// What the server already receives, kept per window: the view hierarchy, names, geometry and the text each view drew.
// Nothing here is published; ui_snapshot() copies it on request. Coordinates are layout units, BeOS-inclusive.
struct UiText {
	std::string text;
	double x = 0, y = 0;		// baseline origin in the drawing view's own coordinates, as the client sent it
	double width = 0;		// advance of the run in layout units (what DrawString moved the pen by)
	float size = 0;
	double wl = 0, wt = 0, wr = 0, wb = 0;	// bounding box in window coordinates at snapshot time
};
struct UiNode {
	int32_t token = -1, parent = -1;	// parent -1: the window's root view
	std::string name;
	std::string role;		// "window-root" | "scrollbar" | "control" | "group" | "text" | "unknown"; see decision 0017
	std::string basis;		// the evidence the role rests on, so a reader can judge it
	std::string confidence;	// "protocol" | "structural" | "convention" | "inferred" | "none"
	double l = 0, t = 0, r = 0, b = 0;		// frame in the parent's coordinates
	double al = 0, at = 0, ar = 0, ab = 0;		// frame in window coordinates
	double vl = 0, vt = 0, vr = 0, vb = 0;		// the part of it actually visible (clipped by ancestors)
	bool clipped = false;		// nothing of it is visible (clipped away, or zero area)
	bool hidden = false;		// hidden itself
	bool effectively_hidden = false;	// hidden, or an ancestor is
	bool navigable = false;		// B_NAVIGABLE: it takes keyboard focus
	uint32_t flags = 0;
	std::map<std::string, int> drew;	// how many drawing calls of each kind this view has made (the evidence for role)
	std::vector<UiText> text;
	std::vector<int32_t> children;
};
struct UiWindow {
	int index = 0;			// creation order, the same index last_frame() takes
	std::string app;		// BApplication signature
	std::string title, kind;	// kind: "window" | "modal" | "menu"
	int width = 0, height = 0;	// content size in layout units
	bool visible = false, active = false;
	std::vector<UiNode> nodes;	// depth first, root first
};
std::vector<UiWindow> ui_snapshot();

}	// namespace aurora

#endif
