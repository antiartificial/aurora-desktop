/*
 * app_server analogue. See server.h and ../README.md. Written fresh; the wire formats below were read from
 * Haiku's src/servers/app/{ServerApp,ServerWindow,DrawState}.cpp and cross-checked against the emitting
 * client code (Font.cpp, View.cpp, Window.cpp).
 *
 * Threads: one for the main port (AS_CREATE_APP), one per ServerApp (fonts, window creation), one per
 * ServerWindow (view tree, drawing, update protocol). One global recursive mutex guards all shared state.
 */
#include "server.h"
#include "canvas.h"
#include "present.h"
#include "text_backend.h"

#include <AppDefs.h>
#include <Font.h>
#include <Gradient.h>
#include <GradientLinear.h>
#include <GraphicsDefs.h>
#include <InterfaceDefs.h>
#include <LinkReceiver.h>
#include <Message.h>
#include <MessagePrivate.h>
#include <PortLink.h>
#include <Region.h>
#include <Shape.h>
#include <ServerProtocol.h>
#include <Menu.h>
#include <Accelerant.h>
#include <Palette.h>
#include <ServerProtocolStructs.h>
#include <View.h>
#include <Window.h>

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <algorithm>
#include <initializer_list>
#include <map>
#include <memory>
#include <mutex>
#include <thread>

using BPrivate::LinkReceiver;
using BPrivate::PortLink;
namespace text = aurora::text;

namespace aurora {

void WlEvent(void* user, int kind, int a, int b);
void WlKey(void* user, const au_key* k);

namespace {

std::recursive_mutex gMu;
bool gTrace = false;
int gUnhandled = 0, gUnhandledReply = 0;
// The view that gets every pointer event until the button goes up: the one pressed on, or the one that called
// SetMouseEventMask(B_POINTER_EVENTS) from its MouseDown (BMenuField does, so that it, not the menu bar inside it, sees the up).
int32 gPtrGrab = B_NULL_TOKEN;
double gScale = 1.0;
uint32 gMods = 0;		// last modifier state seen on any window
au_wl* gWl = NULL;
std::vector<struct SWindow*> gWindows;	// creation order, never shrinks (index = last_frame index)
BPoint gPtr(0, 0);	// last pointer position in screen coordinates, from whichever of our surfaces got the event (menus poll it)
int gPtrButtons = 0;
const int32 kInternalEvent = 0x61755f01;	// our own opcode, never on the wire from a client

Color ToColor(rgb_color c) { return Color{c.red, c.green, c.blue, c.alpha}; }

struct DrawState {
	BPoint pen{0, 0};
	float penSize = 1;
	rgb_color high{0, 0, 0, 255}, low{255, 255, 255, 255};
	color_which highWhich = B_NO_COLOR, lowWhich = B_NO_COLOR;
	float highTint = B_NO_TINT, lowTint = B_NO_TINT;
	::pattern pat = B_SOLID_HIGH;
	drawing_mode mode = B_OP_COPY;
	BPoint origin{0, 0};
	float scale = 1;
	text::FontKey font{0, 0};
	float fontSize = 12;
	uint16 face = B_REGULAR_FACE;
	uint8 spacing = B_BITMAP_SPACING;
	float shear = 90, rotation = 0;
	bool hasClip = false;
	BRect clip;			// bounding rect of the user clipping region (view coords); gap: not a true region
};

struct SView {
	int32 token = -1;
	std::string name;
	BRect frame;			// in parent coordinates, BeOS-inclusive
	BPoint scroll;			// Bounds().LeftTop()
	uint32 resize = 0, flags = 0;
	bool hidden = false;
	rgb_color viewColor{255, 255, 255, 255};
	color_which viewWhich = B_NO_COLOR; float viewTint = B_NO_TINT;
	SView* parent = NULL;
	std::vector<SView*> children;
	DrawState st;
	std::vector<DrawState> stack;
	// ---- accessibility tree (decision 0017): what this view drew, kept so a reader can be told. Nothing is published.
	struct Run { std::string text; BPoint at; float width; float size; BRect win; };	// win: bounding box in window coordinates when it was drawn: where the pixels are, and what a repaint of that place erases
	std::vector<Run> runs; size_t runBytes = 0;
	struct { int strings = 0, ellipses = 0, roundrects = 0, gradients = 0, rects = 0, lines = 0, bitmaps = 0, shapes = 0; } ev;
};

struct SBitmap {
	area_id area = -1;
	uint8* base = NULL;
	BRect bounds;
	color_space cs = B_NO_COLOR_SPACE;
	int32 bpr = 0;
};
std::map<int32, SBitmap> gBitmaps;	// by token; the client clones `area` into its own mapping (same process, same memfd)
int32 gNextBitmap = 1;

struct SApp;

struct SWindow {
	SApp* app = NULL;
	int index = 0;
	port_id port = -1;
	PortLink link;
	port_id clientLooperPort = -1;
	int32 clientToken = -1;
	BRect frame;
	std::string title;
	uint32 flags = 0, feel = 0;
	int popX = 0, popY = 0, popW = 0, popH = 0;	// geometry the live xdg_popup was created with
	bool visible = false, updatesEnabled = true, updatePending = false, inUpdate = false;
	std::map<int32, SView*> views;
	SView* top = NULL;
	SView* cur = NULL;
	BRegion dirty;			// window coordinates
	Canvas canvas;
	au_wl_win* wl = NULL;
	Frame last;
	bool haveLast = false;
	bool quit = false;
	int32 offscreenBitmap = -1;	// >= 0: this window draws into BBitmap `token` (AS_CREATE_OFFSCREEN_WINDOW), and is never shown
	bigtime_t lastClickTime = 0; BPoint lastClickWhere{0, 0}; int32 clicks = 0;
	int buttons = 0;		// mouse buttons held (B_PRIMARY_MOUSE_BUTTON ...)
	BPoint ptrWhere{0, 0};		// last pointer position in window coordinates
	uint32 mods = 0;		// Haiku modifier bits currently held (what B_MODIFIERS_CHANGED reports)
	uint8 keyStates[16] = {0};	// bitmap of Haiku key codes held, as the "states" field of key messages
	bool active = false;
	std::map<int32, BRect> updRect;	// per view token: the window-coordinate rect (bounding box of the dirty region) being repainted
	bool directDraw = false;	// pixels changed outside BeginUpdate/EndUpdate and not yet presented
};

struct SApp {
	port_id port = -1;
	PortLink link;
	team_id team = 0;
	port_id looperPort = -1;
	std::string sig;
};

// BMenuWindow's feel (WindowPrivate.h kMenuWindowFeel). Haiku draws these as their own screen-positioned windows; here
// they are xdg_popups of the application's main window, since a Wayland client cannot place a toplevel.
bool IsMenu(const SWindow* w) { return w->feel == 1025; }

// B_MODAL_APP_WINDOW_FEEL (1), B_MODAL_SUBSET_WINDOW_FEEL (2), B_MODAL_ALL_WINDOW_FEEL (3): headers/os/interface/Window.h.
bool IsModal(const SWindow* w) { return w->feel >= 1 && w->feel <= 3; }

SWindow* PopupParent(SWindow* menu)
{
	SWindow* best = NULL;
	for (SWindow* o : gWindows)
		if (o != menu && o->app == menu->app && !IsMenu(o) && !IsModal(o) && o->visible && o->wl && (!best || o->active)) best = o;
	return best;
}

// ------------------------------------------------------------------------------------------------ view math

BPoint WindowOffset(const SView* v)	// window = local + offset
{
	BPoint o(0, 0);
	for (const SView* c = v; c; c = c->parent) {
		o.x += c->frame.left - c->scroll.x;
		o.y += c->frame.top - c->scroll.y;
		if (!c->parent) { o.x -= c->frame.left - 0; o.y -= c->frame.top - 0; }	// top view sits at 0,0
	}
	return o;
}

BRect VisibleRect(const SView* v)	// window coordinates, clipped by ancestors; may be invalid
{
	BRect r(0, 0, 32767, 32767);
	for (const SView* c = v; c; c = c->parent) {
		BPoint po = c->parent ? WindowOffset(c->parent) : BPoint(0, 0);
		BRect f = c->frame.OffsetByCopy(po);
		if (!c->parent) f = c->frame.OffsetToCopy(0, 0);
		r = r & f;
	}
	return r;
}

bool EffectivelyHidden(const SView* v)
{
	for (; v; v = v->parent) if (v->hidden) return true;
	return false;
}

void CollectDFS(SView* v, std::vector<SView*>& out)
{
	if (!v || v->hidden) return;
	out.push_back(v);
	for (size_t i = 0; i < v->children.size(); i++) CollectDFS(v->children[i], out);
}

void DestroyView(SWindow* w, SView* v)
{
	for (size_t i = 0; i < v->children.size(); i++) DestroyView(w, v->children[i]);
	w->views.erase(v->token);
	if (w->cur == v) w->cur = v->parent;
	delete v;
}

// ------------------------------------------------------------------------------------------------ fonts

text::Backend* TB() { return text::DefaultBackend(); }

void ReplyFontNames(PortLink& l, text::FontKey k)
{
	std::string f, s;
	TB()->FamilyName(k.family, &f);
	TB()->StyleName(k.family, k.style, &s, NULL);
	l.AttachString(f.c_str());
	l.AttachString(s.c_str());
}

// ------------------------------------------------------------------------------------------------ accessibility tree

// What a view has drawn, by kind: the only evidence the server has about what a view IS.
void NoteDraw(SView* v, int32 code)
{
	switch (code) {
		case AS_DRAW_STRING: case AS_DRAW_STRING_WITH_DELTA: v->ev.strings++; break;
		case AS_STROKE_ELLIPSE: case AS_FILL_ELLIPSE: v->ev.ellipses++; break;
		case AS_STROKE_ROUNDRECT: case AS_FILL_ROUNDRECT: v->ev.roundrects++; break;
		case AS_FILL_RECT_GRADIENT: case AS_FILL_ROUNDRECT_GRADIENT: case AS_FILL_ELLIPSE_GRADIENT: v->ev.gradients++; break;
		case AS_STROKE_RECT: case AS_FILL_RECT: v->ev.rects++; break;
		case AS_STROKE_LINE: case AS_STROKE_LINEARRAY: v->ev.lines++; break;
		case AS_VIEW_DRAW_BITMAP: v->ev.bitmaps++; break;
		case AS_STROKE_SHAPE: case AS_FILL_SHAPE: case AS_FILL_TRIANGLE: case AS_STROKE_TRIANGLE: v->ev.shapes++; break;
		default: break;
	}
}

// A view repainted over `win` (window coordinates): the runs it had there are being redrawn, or are gone.
void DropRuns(SView* v, const BRect& win)
{
	if (v->runs.empty()) return;
	size_t keep = 0;
	for (size_t i = 0; i < v->runs.size(); i++) {
		if (v->runs[i].win.Intersects(win)) { v->runBytes -= v->runs[i].text.size(); continue; }
		if (keep != i) v->runs[keep] = std::move(v->runs[i]);
		keep++;
	}
	v->runs.resize(keep);
}

const size_t kMaxRuns = 512, kMaxRunBytes = 64 * 1024;	// per view: a reader does not get to make the server grow without bound

void RecordRun(SView* v, const char* s, BPoint at, BPoint startWin, BPoint endWin)
{
	if (EffectivelyHidden(v)) return;	// a hidden page draws nothing, and has no text to read
	text::Metrics m;
	float asc = 0, desc = 0;
	if (TB()->GetMetrics(v->st.font, v->st.fontSize, &m)) { asc = m.ascent * v->st.scale; desc = m.descent * v->st.scale; }
	BRect loc(startWin.x, startWin.y - asc, std::max(endWin.x, startWin.x + 1), startWin.y + desc);
	// The same text drawn again at the same place (a repaint that did not go through an update) replaces the old run.
	for (size_t i = 0; i < v->runs.size(); i++)
		if (v->runs[i].at == at && v->runs[i].size == v->st.fontSize) { v->runBytes -= v->runs[i].text.size(); v->runs.erase(v->runs.begin() + i); break; }
	if (v->runs.size() >= kMaxRuns || v->runBytes + strlen(s) > kMaxRunBytes) return;
	SView::Run r{s, at, (float)((endWin.x - startWin.x) / (v->st.scale ? v->st.scale : 1)), v->st.fontSize, loc};
	v->runBytes += r.text.size();
	v->runs.push_back(std::move(r));
}

// Role, from what the protocol carries and nothing else. The server never sees a C++ type; it sees a view's name, its
// flags, its children and what it drew. Rules, strongest first; each states its basis so a reader can discount it.
void DeriveRole(const SView* v, UiNode* n)
{
	bool nav = (v->flags & B_NAVIGABLE) != 0;
	if (!v->parent) { n->role = "window-root"; n->basis = "AS_VIEW_CREATE_ROOT"; n->confidence = "protocol"; return; }
	if (v->name == "_VSB_" || v->name == "_HSB_") {
		n->role = "scrollbar"; n->confidence = "convention";
		n->basis = "its name is the one Haiku's BScrollBar gives itself; any application can name a view the same";
		return;
	}
	if (nav) {
		n->role = "control"; n->confidence = "inferred";
		n->basis = "B_NAVIGABLE is set: the view takes keyboard focus (BControl, BTextView and BListView do; BStringView, BBox and BView do not). Which control it is, the server cannot tell";
		return;
	}
	if (!v->children.empty() && v->ev.strings == 0) {
		n->role = "group"; n->confidence = "structural";
		n->basis = "it has child views and drew no text of its own";
		return;
	}
	const auto& e = v->ev;
	bool onlyText = e.strings > 0 && !e.ellipses && !e.roundrects && !e.gradients && !e.rects && !e.lines && !e.bitmaps && !e.shapes;
	if (onlyText && v->children.empty()) {
		n->role = "text"; n->confidence = "inferred";
		n->basis = "it drew text and nothing else (no bevel, line, box or bitmap), has no children and does not take focus: a BStringView, or a label a larger widget drew";
		return;
	}
	n->role = "unknown"; n->confidence = "none";
	n->basis = v->children.empty() ? "it has no children and drew no text" : "it has children and drew text itself";
}

bool ResolveFont(const char* family, const char* style, uint16 familyID, uint16 styleID, uint16 face,
	text::FontKey* key, uint16* outFace)
{
	text::Backend* b = TB();
	int fam = -1;
	if (family && *family) {
		for (int i = 0; i < b->FamilyCount(); i++) {
			std::string n; b->FamilyName(i, &n);
			if (n == family) { fam = i; break; }
		}
		if (fam < 0) return false;
	} else if (!(style && *style) && familyID == 0xffff) {
		return false;
	} else {
		fam = familyID;
		if (fam >= b->FamilyCount()) return false;
	}
	int sty = -1;
	if (style && *style) {
		for (int i = 0; i < b->StyleCount(fam); i++) {
			std::string n; b->StyleName(fam, i, &n, NULL);
			if (n == style) { sty = i; break; }
		}
		if (sty < 0) return false;
	} else if ((family && *family) || (style && *style) || styleID == 0xffff) {
		// pick the style that matches the wanted face, else the first
		sty = 0;
		for (int i = 0; i < b->StyleCount(fam); i++) {
			std::string n; uint16 f = 0; b->StyleName(fam, i, &n, &f);
			if ((f & (B_BOLD_FACE | B_ITALIC_FACE | B_REGULAR_FACE)) == (face & (B_BOLD_FACE | B_ITALIC_FACE | B_REGULAR_FACE)))
				{ sty = i; break; }
		}
	} else {
		sty = styleID;
		if (sty >= b->StyleCount(fam)) return false;
	}
	*key = text::FontKey{(uint16_t)fam, (uint16_t)sty};
	std::string n; uint16 f = 0; b->StyleName(fam, sty, &n, &f);
	*outFace = f;
	return true;
}

// Advance of the whole string in layout units, with HarfBuzz shaping (kerning, ligatures).
float StringAdvance(text::FontKey k, float size, const char* s, int len)
{
	std::vector<text::Glyph> g;
	if (!TB()->Shape(k, size, s, len, &g)) return 0;
	float w = 0;
	for (size_t i = 0; i < g.size(); i++) w += g[i].advance;
	return w;
}

int Utf8Len(unsigned char c) { return c < 0x80 ? 1 : (c >> 5) == 6 ? 2 : (c >> 4) == 14 ? 3 : (c >> 3) == 30 ? 4 : 1; }

void HandleFontOp(SApp* app, int32 code, LinkReceiver& link, PortLink& reply, bool* handled)
{
	*handled = true;
	text::Backend* b = TB();
	switch (code) {
		case AS_CREATE_BITMAP:
		{
			// BBitmap's server side, in process: an area the client then clones. Reply: token, flags, area, offset.
			BRect bounds; color_space cs; uint32 flags; int32 bpr, screen;
			link.Read<BRect>(&bounds); link.Read<color_space>(&cs); link.Read<uint32>(&flags); link.Read<int32>(&bpr); link.Read<int32>(&screen);
			int32 h = bounds.IntegerHeight() + 1;
			size_t size = (size_t)bpr * h;
			void* addr = NULL;
			area_id a = size ? create_area("bitmap", &addr, B_ANY_ADDRESS, size, B_NO_LOCK, B_READ_AREA | B_WRITE_AREA) : B_BAD_VALUE;
			if (a < 0) { reply.StartMessage(a < 0 ? a : B_NO_MEMORY); reply.Flush(); return; }
			SBitmap bm; bm.area = a; bm.base = (uint8*)addr; bm.bounds = bounds; bm.cs = cs; bm.bpr = bpr;
			int32 token = gNextBitmap++;
			gBitmaps[token] = bm;
			reply.StartMessage(B_OK);
			reply.Attach<int32>(token); reply.Attach<uint8>(0); reply.Attach<area_id>(a); reply.Attach<int32>(0);
			reply.Flush();
			return;
		}
		case AS_DELETE_BITMAP:
		{
			int32 token; link.Read<int32>(&token);
			auto it = gBitmaps.find(token);
			if (it != gBitmaps.end()) { delete_area(it->second.area); gBitmaps.erase(it); }
			return;
		}
		case AS_SET_VIEW_CURSOR:
		{
			// Acknowledged, not shown: the cursor is the compositor's, and there is no protocol yet to pass a BCursor's bitmap on.
			// BSplitView sets one (a resize cursor) as the pointer crosses its divider, and asks for the answer.
			if (link.NeedsReply()) { reply.StartMessage(B_OK); reply.Flush(); }
			return;
		}
		case AS_GET_SCREEN_FRAME:
		{
			// The output's logical size (wl_output.mode / scale); 1280x800 with no compositor. One screen, id 0.
			int sw = 1280, sh = 800;
			if (gWl) au_wl_output_size(gWl, &sw, &sh);
			reply.StartMessage(B_OK);
			reply.Attach<BRect>(BRect(0, 0, sw - 1, sh - 1));
			reply.Flush();
			return;
		}
		case AS_VALID_SCREEN_ID:
		{
			int32 id = -1; link.Read<int32>(&id);
			reply.StartMessage(id == 0 ? B_OK : B_BAD_VALUE);	// one screen, id 0
			reply.Flush();
			return;
		}
		case AS_SCREEN_GET_MODE:
		{
			// One 32-bit screen the size of the output; no timings (there is no CRTC).
			int32 id = -1; uint32 workspace; link.Read<int32>(&id); link.Read<uint32>(&workspace);
			if (id != 0) { reply.StartMessage(B_BAD_VALUE); reply.Flush(); return; }
			int sw = 1280, sh = 800;
			if (gWl) au_wl_output_size(gWl, &sw, &sh);
			display_mode m; memset(&m, 0, sizeof(m));
			m.virtual_width = m.timing.h_display = sw; m.virtual_height = m.timing.v_display = sh;
			m.space = B_RGB32;
			reply.StartMessage(B_OK); reply.Attach(&m, sizeof(m)); reply.Flush();
			return;
		}
		case AS_GET_SCREEN_ID_FROM_WINDOW:
		{
			reply.StartMessage(B_OK);
			reply.Attach<int32>(0);
			reply.Flush();
			return;
		}
		case AS_GET_MENU_INFO:
		{
			// What Haiku's app_server answers by default: the plain font, a panel-coloured menu, click to open.
			static menu_info mi; static bool init = false;
			if (!init) {
				init = true; memset(&mi, 0, sizeof(mi));
				text::FontKey k; float size = 12; b->SystemFont("plain", &k, &size);
				std::string fam, sty; uint16 face = 0; b->FamilyName(k.family, &fam); b->StyleName(k.family, k.style, &sty, &face);
				mi.font_size = size;
				strlcpy(mi.f_family, fam.c_str(), sizeof(mi.f_family)); strlcpy(mi.f_style, sty.c_str(), sizeof(mi.f_style));
				mi.background_color = (rgb_color){216, 216, 216, 255};
				mi.separator = 0; mi.click_to_open = true; mi.triggers_always_shown = false;
			}
			reply.StartMessage(B_OK);
			reply.Attach<menu_info>(mi);
			reply.Flush();
			return;
		}
		case AS_SET_MENU_INFO:
		{
			reply.StartMessage(B_OK);	// the client keeps its own copy (set_menu_info); one process, nothing to share
			reply.Flush();
			return;
		}
		case AS_GET_SCROLLBAR_INFO:
		{
			// Haiku's defaults: proportional thumb, one arrow at each end, knob style 0, 15 px minimum.
			scroll_bar_info i; i.proportional = true; i.double_arrows = false; i.knob = 0; i.min_knob_size = 15;
			reply.StartMessage(B_OK);
			reply.Attach<scroll_bar_info>(i);
			reply.Flush();
			return;
		}
		case AS_GET_SYSTEM_FONTS:
		{
			reply.StartMessage(B_OK);
			static const char* names[3] = {"plain", "bold", "fixed"};
			for (int i = 0; i < 3; i++) {
				text::FontKey k; float size = 12;
				b->SystemFont(names[i], &k, &size);
				std::string st; uint16 face = B_REGULAR_FACE;
				b->StyleName(k.family, k.style, &st, &face);
				reply.AttachString(names[i]);
				reply.Attach<uint16>(k.family);
				reply.Attach<uint16>(k.style);
				reply.Attach<float>(size);
				reply.Attach<uint16>(face);
				reply.Attach<uint32>(0);	// font flags
			}
			reply.Flush();
			break;
		}
		case AS_GET_SYSTEM_DEFAULT_FONT:
		{
			char which[B_OS_NAME_LENGTH];
			link.ReadString(which, sizeof(which));
			text::FontKey k; float size;
			if (!b->SystemFont(which, &k, &size)) { reply.StartMessage(B_BAD_VALUE); reply.Flush(); break; }
			reply.StartMessage(B_OK);
			ReplyFontNames(reply, k);
			reply.Attach<float>(size);
			reply.Flush();
			break;
		}
		case AS_GET_FONT_LIST_REVISION:
			reply.StartMessage(B_OK); reply.Attach<uint32>(1); reply.Flush();
			break;
		case AS_GET_FAMILY_AND_STYLE_IDS:
		{
			font_family fam; font_style sty; uint16 fid, sid, face;
			if (link.ReadString(fam, sizeof(fam)) == B_OK && link.ReadString(sty, sizeof(sty)) == B_OK
				&& link.Read<uint16>(&fid) == B_OK && link.Read<uint16>(&sid) == B_OK && link.Read<uint16>(&face) == B_OK) {
				text::FontKey k; uint16 outFace;
				if (ResolveFont(fam, sty, fid, sid, face, &k, &outFace)) {
					reply.StartMessage(B_OK);
					reply.Attach<uint16>(k.family);
					reply.Attach<uint16>(k.style);
					reply.Attach<uint16>(outFace);
				} else
					reply.StartMessage(B_NAME_NOT_FOUND);
			} else
				reply.StartMessage(B_BAD_VALUE);
			reply.Flush();
			break;
		}
		case AS_GET_FAMILY_AND_STYLE:
		{
			uint16 fid, sid;
			link.Read<uint16>(&fid); link.Read<uint16>(&sid);
			text::FontKey k{fid, sid};
			std::string n;
			if (!b->StyleName(fid, sid, &n, NULL)) { reply.StartMessage(B_BAD_VALUE); reply.Flush(); break; }
			reply.StartMessage(B_OK);
			ReplyFontNames(reply, k);
			reply.Flush();
			break;
		}
		case AS_GET_EXTRA_FONT_FLAGS:
			reply.StartMessage(B_OK); reply.Attach<uint32>(0); reply.Flush();
			break;
		case AS_GET_FONT_HEIGHT:
		{
			uint16 fid, sid; float size;
			link.Read<uint16>(&fid); link.Read<uint16>(&sid); link.Read<float>(&size);
			text::Metrics m;
			if (!b->GetMetrics(text::FontKey{fid, sid}, size, &m)) { reply.StartMessage(B_BAD_VALUE); reply.Flush(); break; }
			font_height h = {m.ascent, m.descent, m.leading};
			reply.StartMessage(B_OK);
			reply.Attach<font_height>(h);
			reply.Flush();
			break;
		}
		case AS_GET_STRING_WIDTHS:
		{
			uint16 fid, sid; float size; uint8 spacing; int32 n = 0;
			link.Read<uint16>(&fid); link.Read<uint16>(&sid); link.Read<float>(&size); link.Read<uint8>(&spacing);
			if (link.Read<int32>(&n) != B_OK) n = 0;
			std::vector<float> widths(n > 0 ? n : 0);
			for (int32 i = 0; i < n; i++) {
				char* s = NULL; size_t len = 0;
				link.ReadString(&s, &len);
				widths[i] = (s && len) ? StringAdvance(text::FontKey{fid, sid}, size, s, (int)len) : 0;
				free(s);
			}
			if (n <= 0 || size <= 0) { reply.StartMessage(B_BAD_VALUE); reply.Flush(); break; }
			reply.StartMessage(B_OK);
			reply.Attach(widths.data(), n * sizeof(float));
			reply.Flush();
			break;
		}
		case AS_GET_ESCAPEMENTS_AS_FLOATS:
		case AS_GET_ESCAPEMENTS:
		{
			uint16 fid, sid; float size, rot; uint8 spacing; uint32 flags; escapement_delta d; bool offsets = false;
			int32 numChars, numBytes;
			link.Read<uint16>(&fid); link.Read<uint16>(&sid); link.Read<float>(&size); link.Read<uint8>(&spacing);
			link.Read<float>(&rot); link.Read<uint32>(&flags);
			link.Read<float>(&d.nonspace); link.Read<float>(&d.space);
			if (code == AS_GET_ESCAPEMENTS) link.Read<bool>(&offsets);
			link.Read<int32>(&numChars); link.Read<int32>(&numBytes);
			std::vector<char> buf(numBytes > 0 ? numBytes : 1);
			if (numBytes > 0) link.Read(buf.data(), numBytes);
			if (numChars < 1 || size <= 0) { reply.StartMessage(B_BAD_VALUE); reply.Flush(); break; }
			// Escapement = advance / size: a fraction of the em, so it does not depend on the size or scale.
			std::vector<float> esc(numChars, 0.0f);
			int pos = 0;
			for (int32 i = 0; i < numChars && pos < numBytes; i++) {
				int n = std::min(Utf8Len((unsigned char)buf[pos]), numBytes - pos);
				esc[i] = StringAdvance(text::FontKey{fid, sid}, size, &buf[pos], n) / size;
				pos += n;
			}
			reply.StartMessage(B_OK);
			if (code == AS_GET_ESCAPEMENTS_AS_FLOATS)
				reply.Attach(esc.data(), numChars * sizeof(float));
			else {
				for (int32 i = 0; i < numChars; i++) { BPoint p(esc[i], 0); reply.Attach<BPoint>(p); }
				if (offsets) for (int32 i = 0; i < numChars; i++) { BPoint p(0, 0); reply.Attach<BPoint>(p); }
			}
			reply.Flush();
			break;
		}
		default:
			*handled = false;
	}
	(void)app;
}

// ------------------------------------------------------------------------------------------------ painting

struct GlyphKey {
	uint64_t k;
	bool operator<(const GlyphKey& o) const { return k < o.k; }
};
std::map<GlyphKey, std::shared_ptr<text::GlyphBitmap> > gGlyphCache;

std::shared_ptr<text::GlyphBitmap> GetGlyph(text::FontKey fk, float size, float scale, uint32_t gid, int subq)
{
	uint64_t sz = (uint64_t)lroundf(size * scale * 64.0f) & 0xFFFFF;
	GlyphKey key{((uint64_t)fk.family << 56) ^ ((uint64_t)fk.style << 48) ^ (sz << 24) ^ ((uint64_t)subq << 20) ^ ((uint64_t)gid * 2654435761u)};
	std::map<GlyphKey, std::shared_ptr<text::GlyphBitmap> >::iterator it = gGlyphCache.find(key);
	if (it != gGlyphCache.end()) return it->second;
	std::shared_ptr<text::GlyphBitmap> g(new text::GlyphBitmap);
	if (!TB()->Rasterize(fk, size, scale, gid, subq / 4.0f, g.get())) g.reset();
	gGlyphCache[key] = g;
	return g;
}

// Draws `s` with the baseline origin at window-space `p` (layout units); returns the end pen in window space.
BPoint DrawText(SWindow* w, const DrawState& st, const char* s, int len, BPoint p, const escapement_delta* delta)
{
	float size = st.fontSize * st.scale;
	std::vector<text::Glyph> gl;
	if (!TB()->Shape(st.font, size, s, len, &gl)) return p;
	Color c = ToColor(st.high);
	float x = p.x;
	for (size_t i = 0; i < gl.size(); i++) {
		float gx = x + gl[i].x_offset, gy = p.y - gl[i].y_offset;
		float dev = gx * (float)w->canvas.Scale();
		int whole = (int)floorf(dev);
		int subq = (int)floorf((dev - whole) * 4.0f);
		std::shared_ptr<text::GlyphBitmap> g = GetGlyph(st.font, size, (float)w->canvas.Scale(), gl[i].id, subq);
		if (g && g->width > 0) w->canvas.DrawGlyph(*g, (float)whole / (float)w->canvas.Scale(), gy, c);
		float adv = gl[i].advance;
		if (delta && gl[i].cluster < (uint32_t)len)
			adv += s[gl[i].cluster] == ' ' ? delta->space : delta->nonspace;
		x += adv;
	}
	return BPoint(x, p.y);
}

// ------------------------------------------------------------------------------------------------ presenting

void PresentWindow(SWindow* w)
{
	w->last.width = w->canvas.Width();
	w->last.height = w->canvas.Height();
	w->last.scale = w->canvas.Scale();
	w->last.title = w->title;
	w->last.argb.assign(w->canvas.Pixels(), w->canvas.Pixels() + (size_t)w->last.width * w->last.height);
	w->haveLast = true;
	if (IsMenu(w) && gWl && w->visible) {
		int x = (int)lroundf(w->frame.left), y = (int)lroundf(w->frame.top), lw = (int)w->frame.IntegerWidth() + 1, lh = (int)w->frame.IntegerHeight() + 1;
		if (w->wl && (x != w->popX || y != w->popY || lw != w->popW || lh != w->popH)) { au_wl_win_destroy(w->wl); w->wl = NULL; }	// moved or resized: a popup's geometry is fixed at creation
		if (!w->wl) {
			if (SWindow* parent = PopupParent(w)) {
				w->wl = au_wl_popup_create(gWl, parent->wl, x - (int)lroundf(parent->frame.left), y - (int)lroundf(parent->frame.top), lw, lh, WlEvent, w);
				w->popX = x; w->popY = y; w->popW = lw; w->popH = lh;
			}
		}
	}
	if (w->wl)
		au_wl_win_present(w->wl, w->canvas.Pixels(), w->canvas.Width(), w->canvas.Height(), (int)ceil(w->canvas.Scale()));
	if (const char* dir = getenv("AURORA_APPSERVER_DUMP")) {
		// window-N.ppm is always the last frame; with AURORA_APPSERVER_DUMP_SEQ=1 every frame is also kept as window-N-K.ppm
		static std::map<int, int> seq;
		char path[512], path2[512]; path2[0] = 0;
		snprintf(path, sizeof(path), "%s/window-%d.ppm", dir, w->index);
		if (getenv("AURORA_APPSERVER_DUMP_SEQ")) snprintf(path2, sizeof(path2), "%s/window-%d-%03d.ppm", dir, w->index, seq[w->index]++);
		for (const char* p : std::initializer_list<const char*>{path, path2}) {
			if (!p[0]) continue;
			if (FILE* f = fopen(p, "wb")) {
				fprintf(f, "P6\n%d %d\n255\n", w->last.width, w->last.height);
				for (size_t i = 0; i < w->last.argb.size(); i++) {
					uint32_t v = w->last.argb[i];
					unsigned char rgb[3] = {(unsigned char)(v >> 16), (unsigned char)(v >> 8), (unsigned char)v};
					fwrite(rgb, 3, 1, f);
				}
				fclose(f);
			}
		}
	}
}

void RequestUpdate(SWindow* w)
{
	if (!w->visible || !w->updatesEnabled || w->updatePending || w->inUpdate || w->dirty.CountRects() == 0) return;
	w->updatePending = true;
	BMessage msg(_UPDATE_);
	BMessenger reply;
	BMessage::Private(&msg).SendMessage(w->clientLooperPort, w->app->team, w->clientToken, 0, false, reply);
}

void InvalidateWindowRect(SWindow* w, BRect r)	// window coordinates
{
	BRect all(0, 0, w->frame.IntegerWidth(), w->frame.IntegerHeight());
	r = r & all;
	if (!r.IsValid()) return;
	w->dirty.Include(r);
	RequestUpdate(w);
}

void PostInternal(SWindow* w, int kind, int a, int b);

void Show(SWindow* w, bool show)
{
	if (gTrace) fprintf(stderr, "appserver[%s]: %s (frame %.0f,%.0f,%.0f,%.0f)\n", w->title.c_str(), show ? "SHOW" : "HIDE", w->frame.left, w->frame.top, w->frame.right, w->frame.bottom);
	if (show == w->visible) return;
	w->visible = show;
	if (!show && IsMenu(w) && w->wl) { au_wl_win_destroy(w->wl); w->wl = NULL; }	// a menu is an xdg_popup for as long as it is shown
	if (show) {
		if (!w->wl && gWl && !IsMenu(w)) {
			int lw = (int)w->frame.IntegerWidth() + 1, lh = (int)w->frame.IntegerHeight() + 1;
			w->wl = au_wl_win_create(gWl, w->title.c_str(), lw, lh, WlEvent, w);
			if (w->wl) au_wl_win_set_key_fn(w->wl, WlKey);
			// A modal window (a BAlert is B_MODAL_APP_WINDOW_FEEL) is a dialog of the application's main window: say so,
			// and the compositor centres it on that window instead of cascading it like a new document.
			if (w->wl && IsModal(w)) if (SWindow* p = PopupParent(w)) au_wl_win_set_parent(w->wl, p->wl);
		}
		// No compositor means no keyboard focus, so no B_WINDOW_ACTIVATED. AURORA_APPSERVER_ACTIVE=1 sends it, so a
		// reference render of a focused window (blue focus ring, caret, selection) can be made without a seat.
		if (!gWl && getenv("AURORA_APPSERVER_ACTIVE")) PostInternal(w, AU_EV_ACTIVATE, 1, 0);
		// AURORA_APPSERVER_CLICK="x,y": press and release the left button there 500 ms later, through the same internal
		// pointer event the Wayland callbacks post. A reference render of a *clicked* window needs this: a click makes
		// BScrollView redraw its focus frame around the scroll bar too, which a program that merely selects an item does not.
		const char* click = gWl ? NULL : getenv("AURORA_APPSERVER_CLICK");
		float cx, cy;
		if (click && sscanf(click, "%f,%f", &cx, &cy) == 2) {
			std::thread([w, cx, cy] {
				snooze(500000);
				PostInternal(w, AU_EV_POINTER_MOVE, (int)(cx * 256), (int)(cy * 256));
				PostInternal(w, AU_EV_POINTER_BUTTON, 1, 1);
				PostInternal(w, AU_EV_POINTER_BUTTON, 1, 0);
			}).detach();
		}
		InvalidateWindowRect(w, BRect(0, 0, w->frame.IntegerWidth(), w->frame.IntegerHeight()));
	}
}

void BeginUpdate(SWindow* w)
{
	std::vector<SView*> order;
	CollectDFS(w->top, order);
	// Background: the server fills each view's dirty part with its view colour (B_TRANSPARENT_COLOR: skip).
	struct Item { SView* v; BRect r; };
	std::vector<Item> items;
	w->updRect.clear();
	for (size_t i = 0; i < order.size(); i++) {
		SView* v = order[i];
		BRect vis = VisibleRect(v);
		if (!vis.IsValid()) continue;
		BRegion part(vis);
		part.IntersectWith(&w->dirty);
		if (part.CountRects() == 0) continue;
		BRect fr = part.Frame();
		if (v->viewColor.alpha != 0) {
			for (int32 k = 0; k < part.CountRects(); k++) {
				BRect rr = part.RectAt(k);
				w->canvas.SetClip(DRect{0, 0, w->canvas.Width(), w->canvas.Height()});
				w->canvas.FillRect(FRect{rr.left, rr.top, rr.right, rr.bottom}, ToColor(v->viewColor));
			}
		}
		if (gTrace) fprintf(stderr, "appserver[%s]: update view '%s' rect %.0f,%.0f,%.0f,%.0f (dirty rects %d)\n", w->title.c_str(), v->name.c_str(), fr.left, fr.top, fr.right, fr.bottom, (int)part.CountRects());
		w->updRect[v->token] = fr;
		DropRuns(v, fr);
		items.push_back(Item{v, fr});
	}
	w->dirty.MakeEmpty();
	w->updatePending = false;
	w->inUpdate = true;

	w->link.StartMessage(B_OK);
	w->link.Attach<BPoint>(w->frame.LeftTop());
	w->link.Attach<float>(w->frame.Width());
	w->link.Attach<float>(w->frame.Height());
	for (size_t i = 0; i < items.size(); i++) {
		BRect scr = items[i].r.OffsetByCopy(w->frame.LeftTop());	// client wants screen coordinates
		w->link.Attach<int32>(items[i].v->token);
		w->link.Attach<BRect>(scr);
	}
	w->link.Attach<int32>(B_NULL_TOKEN);
	w->link.Flush();
}

void EndUpdate(SWindow* w)
{
	w->inUpdate = false;
	PresentWindow(w);
	RequestUpdate(w);	// something invalidated while drawing
}

// Transform a view-local point to window space through the view state (origin, scale) and tree offsets.
BPoint ToWindow(const SView* v, BPoint p)
{
	BPoint o = WindowOffset(v);
	return BPoint((p.x + v->st.origin.x) * v->st.scale + o.x, (p.y + v->st.origin.y) * v->st.scale + o.y);
}
BRect ToWindow(const SView* v, BRect r)
{
	BPoint a = ToWindow(v, r.LeftTop()), b = ToWindow(v, r.RightBottom());
	return BRect(a.x, a.y, b.x, b.y);
}

void SetupClip(SWindow* w, SView* v)
{
	if (EffectivelyHidden(v)) { w->canvas.SetClip(DRect{0, 0, 0, 0}); return; }	// a view on a hidden tab page draws nothing (BAbstractSpinner repaints its focus ring when the window deactivates)
	if (!w->inUpdate) w->directDraw = true;	// drawing outside an update (BTextView's caret): present it, see WindowThread
	BRect vis = VisibleRect(v);
	BRect c = vis;
	if (w->inUpdate) { auto it = w->updRect.find(v->token); c = it != w->updRect.end() ? (c & it->second) : BRect(); }	// app_server clips an update to the region it asked for: BMenu fills its whole background and relies on that
	if (v->st.hasClip) c = c & v->st.clip.OffsetByCopy(WindowOffset(v));
	if (!c.IsValid()) { w->canvas.SetClip(DRect{0, 0, 0, 0}); return; }
	w->canvas.SetClip(w->canvas.ToDevice(FRect{c.left, c.top, c.right, c.bottom}));
}

bool ReadPaint(SView* v, LinkReceiver& link, Paint* out)	// reads a BGradient from the link; linear only
{
	BGradient* g = NULL;
	if (link.ReadGradient(&g) != B_OK || !g) return false;
	bool ok = g->GetType() == BGradient::TYPE_LINEAR;
	if (ok) {
		BGradientLinear* l = (BGradientLinear*)g;
		BPoint a = ToWindow(v, l->Start()), b = ToWindow(v, l->End());
		out->gradient = true; out->x0 = a.x; out->y0 = a.y; out->x1 = b.x; out->y1 = b.y;
		g->SortColorStopsByOffset();
		for (int32 i = 0; i < g->CountColorStops(); i++) {
			BGradient::ColorStop* st = g->ColorStopAt(i);
			out->stops.push_back(Stop{st->offset / 255.0f, Color{st->color.red, st->color.green, st->color.blue, st->color.alpha}});
		}
	}
	delete g;
	return ok;
}

Color DrawColor(const DrawState& st) { return ToColor(st.pat == B_SOLID_LOW ? st.low : st.high); }

// ------------------------------------------------------------------------------------------------ window dispatch

void HandleViewState(SWindow* w, SView* v, LinkReceiver& link)
{
	(void)w;
	ViewSetStateInfo info;
	link.Read<ViewSetStateInfo>(&info);
	double transform[6];
	link.Read<double[6]>(&transform);
	DrawState& s = v->st;
	s.pen = info.penLocation; s.penSize = info.penSize; s.high = info.highColor; s.low = info.lowColor;
	s.pat = info.pattern; s.mode = info.drawingMode; s.origin = info.origin; s.scale = info.scale;
	bool hasClip = false;
	link.Read<bool>(&hasClip);
	s.hasClip = false;
	if (hasClip) {
		BRegion r;
		link.ReadRegion(&r);
		s.hasClip = true; s.clip = r.Frame();
	}
}

void HandleFontState(SView* v, LinkReceiver& link)
{
	uint16 mask = 0;
	link.Read<uint16>(&mask);
	DrawState& s = v->st;
	if (mask & B_FONT_FAMILY_AND_STYLE) {
		uint32 id; link.Read<uint32>(&id);
		s.font = text::FontKey{(uint16_t)(id >> 16), (uint16_t)(id & 0xffff)};
	}
	if (mask & B_FONT_SIZE) link.Read<float>(&s.fontSize);
	if (mask & B_FONT_SHEAR) link.Read<float>(&s.shear);
	if (mask & B_FONT_ROTATION) link.Read<float>(&s.rotation);
	if (mask & B_FONT_FALSE_BOLD_WIDTH) { float f; link.Read<float>(&f); }
	if (mask & B_FONT_SPACING) link.Read<uint8>(&s.spacing);
	if (mask & B_FONT_ENCODING) { uint8 e; link.Read<uint8>(&e); }
	if (mask & B_FONT_FACE) link.Read<uint16>(&s.face);
	if (mask & B_FONT_FLAGS) { uint32 f; link.Read<uint32>(&f); }
}

// BShape -> polylines in window coordinates. Beziers are flattened to 12 segments; arcs are not in BShape.
struct ShapeFlattener : public BShapeIterator {
	const SView* view;
	std::vector<std::vector<BPoint> > paths;
	std::vector<bool> closed;
	explicit ShapeFlattener(const SView* v) : view(v) {}
	status_t IterateMoveTo(BPoint* p) override { paths.push_back(std::vector<BPoint>(1, ToWindow(view, *p))); closed.push_back(false); return B_OK; }
	status_t IterateLineTo(int32 n, BPoint* p) override
	{
		if (paths.empty()) return B_OK;
		for (int32 i = 0; i < n; i++) paths.back().push_back(ToWindow(view, p[i]));
		return B_OK;
	}
	status_t IterateBezierTo(int32 n, BPoint* p) override
	{
		if (paths.empty()) return B_OK;
		for (int32 b = 0; b + 2 < n; b += 3) {
			BPoint p0 = paths.back().back(), p1 = ToWindow(view, p[b]), p2 = ToWindow(view, p[b + 1]), p3 = ToWindow(view, p[b + 2]);
			for (int k = 1; k <= 12; k++) {
				float t = k / 12.0f, u = 1 - t;
				paths.back().push_back(BPoint(u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x,
					u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y));
			}
		}
		return B_OK;
	}
	status_t IterateClose() override { if (!closed.empty()) closed.back() = true; return B_OK; }
};

SView* CreateView(SWindow* w, LinkReceiver& link, SView** parentOut)
{
	int32 token, parentToken; BRect frame; BPoint scroll; uint32 resize, em, eo, flags; bool hidden; rgb_color vc;
	char* name = NULL;
	link.Read<int32>(&token);
	link.ReadString(&name);
	link.Read<BRect>(&frame); link.Read<BPoint>(&scroll);
	link.Read<uint32>(&resize); link.Read<uint32>(&em); link.Read<uint32>(&eo); link.Read<uint32>(&flags);
	link.Read<bool>(&hidden); link.Read<rgb_color>(&vc); link.Read<int32>(&parentToken);
	SView* v = new SView;
	v->token = token; v->name = name ? name : ""; free(name);
	v->frame = frame; v->scroll = scroll; v->resize = resize; v->flags = flags; v->hidden = hidden; v->viewColor = vc;
	text::FontKey k; float size = 12;
	TB()->SystemFont("plain", &k, &size);
	v->st.font = k; v->st.fontSize = size;
	w->views[token] = v;
	if (parentOut) {
		std::map<int32, SView*>::iterator it = w->views.find(parentToken);
		*parentOut = it != w->views.end() ? it->second : NULL;
	}
	return v;
}

void ApplyWindowResize(SWindow* w, int lw, int lh)
{
	float dw = (float)lw - (float)(w->frame.IntegerWidth() + 1), dh = (float)lh - (float)(w->frame.IntegerHeight() + 1);
	if (dw == 0 && dh == 0) return;
	if (gTrace) fprintf(stderr, "appserver[%s]: RESIZE to %dx%d\n", w->title.c_str(), lw, lh);
	w->frame.right = w->frame.left + lw - 1;
	w->frame.bottom = w->frame.top + lh - 1;
	// Resize, NOT Init: Init fills the buffer opaque white, and the repaint that answers this
	// is asynchronous (B_WINDOW_RESIZED below, answered by the client whenever it gets round to
	// it), so every frame presented in between showed a white window. That was the flash.
	w->canvas.Resize(lw, lh, gScale);
	// follow modes, as View::ParentResized does
	struct R { static void Go(SView* v, int dx, int dy) {
		BRect f = v->frame;
		uint32 m = v->resize & 0xffff;
		if ((m & 0x0F00U) == _VIEW_RIGHT_ << 8) f.left += dx; else if ((m & 0x0F00U) == _VIEW_CENTER_ << 8) f.left += dx / 2;
		if ((m & 0x000FU) == _VIEW_RIGHT_) f.right += dx; else if ((m & 0x000FU) == _VIEW_CENTER_) f.right += dx / 2;
		if ((m & 0xF000U) == _VIEW_BOTTOM_ << 12) f.top += dy; else if ((m & 0xF000U) == _VIEW_CENTER_ << 12) f.top += dy / 2;
		if ((m & 0x00F0U) == _VIEW_BOTTOM_ << 4) f.bottom += dy; else if ((m & 0x00F0U) == _VIEW_CENTER_ << 4) f.bottom += dy / 2;
		int ndx = (int)((f.Width() - v->frame.Width())), ndy = (int)((f.Height() - v->frame.Height()));
		v->frame = f;
		for (size_t i = 0; i < v->children.size(); i++) Go(v->children[i], ndx, ndy);
	} };
	if (w->top) {
		w->top->frame = BRect(0, 0, lw - 1, lh - 1);
		for (size_t i = 0; i < w->top->children.size(); i++) R::Go(w->top->children[i], (int)dw, (int)dh);
	}
	BMessage m(B_WINDOW_RESIZED);
	m.AddInt64("when", system_time());
	m.AddInt32("width", lw - 1);
	m.AddInt32("height", lh - 1);
	BMessenger reply;
	BMessage::Private(&m).SendMessage(w->clientLooperPort, w->app->team, w->clientToken, 0, false, reply);
	w->dirty.MakeEmpty();
	w->dirty.Include(BRect(0, 0, lw - 1, lh - 1));
	RequestUpdate(w);
}

void SendInput(SWindow* w, BMessage& m)
{
	BMessenger reply;
	BMessage::Private(&m).SendMessage(w->clientLooperPort, w->app->team, B_PREFERRED_TOKEN, 0, false, reply);
}

struct Internal { int kind, a, b; au_key key; };

// evdev (linux input) code -> BeOS key code, US layout positions (InterfaceDefs.h, Haiku's default keymap).
// 0 = no BeOS equivalent. A key's *code* is the position; its *bytes* come from the compositor's xkb keymap.
int32 HaikuKeyCode(uint32 ev)
{
	static const uint8 qrow[10] = {0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30};
	static const uint8 arow[9] = {0x3c, 0x3d, 0x3e, 0x3f, 0x40, 0x41, 0x42, 0x43, 0x44};
	static const uint8 zrow[7] = {0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52};
	if (ev == 1) return 0x01;
	if (ev >= 2 && ev <= 10) return 0x12 + (ev - 2);		// KEY_1..KEY_9
	if (ev == 11) return 0x1b;							// KEY_0
	if (ev == 12) return 0x1c; if (ev == 13) return 0x1d; if (ev == 14) return 0x1e; if (ev == 15) return 0x26;
	if (ev >= 16 && ev <= 25) return qrow[ev - 16];
	if (ev == 26) return 0x31; if (ev == 27) return 0x32; if (ev == 28) return 0x47; if (ev == 29) return 0x5c;
	if (ev >= 30 && ev <= 38) return arow[ev - 30];
	if (ev == 39) return 0x45; if (ev == 40) return 0x46; if (ev == 41) return 0x11; if (ev == 42) return 0x4b;
	if (ev == 43) return 0x33;
	if (ev >= 44 && ev <= 50) return zrow[ev - 44];
	if (ev == 51) return 0x53; if (ev == 52) return 0x54; if (ev == 53) return 0x55; if (ev == 54) return 0x56;
	if (ev == 56) return 0x5d; if (ev == 57) return 0x5e; if (ev == 58) return 0x3b; if (ev == 97) return 0x60;
	if (ev == 100) return 0x5f;
	if (ev >= 59 && ev <= 68) return 0x02 + (ev - 59);			// F1..F10
	if (ev == 87) return 0x0c; if (ev == 88) return 0x0d;			// F11, F12
	if (ev == 102) return 0x20; if (ev == 103) return 0x57; if (ev == 104) return 0x21; if (ev == 105) return 0x61;
	if (ev == 106) return 0x63; if (ev == 107) return 0x35; if (ev == 108) return 0x62; if (ev == 109) return 0x36;
	if (ev == 110) return 0x1f; if (ev == 111) return 0x34;
	return 0;
}

uint32 HaikuModifiers(uint32 m)
{
	// Alt is Command (as on Haiku's default keymap) and the logo key is Option.
	uint32 r = 0;
	if (m & AU_MOD_SHIFT) r |= B_SHIFT_KEY | B_LEFT_SHIFT_KEY;
	if (m & AU_MOD_CTRL) r |= B_CONTROL_KEY | B_LEFT_CONTROL_KEY;
	if (m & AU_MOD_ALT) r |= B_COMMAND_KEY | B_LEFT_COMMAND_KEY;
	if (m & AU_MOD_LOGO) r |= B_OPTION_KEY | B_LEFT_OPTION_KEY;
	if (m & AU_MOD_CAPS) r |= B_CAPS_LOCK;
	if (m & AU_MOD_NUM) r |= B_NUM_LOCK;
	return r;
}

void HandleKey(SWindow* w, const au_key& k)
{
	int32 code = HaikuKeyCode(k.evdev);
	if (code > 0 && code < 128) {
		if (k.pressed) w->keyStates[code >> 3] |= 1 << (7 - (code & 7));
		else w->keyStates[code >> 3] &= ~(1 << (7 - (code & 7)));
	}
	uint32 mods = HaikuModifiers(k.mods);
	size_t len = strlen(k.utf8);
	// A key with nothing to type (a modifier, a dead key) is B_UNMAPPED_KEY_*; the rest is B_KEY_*.
	uint32 what = len ? (k.pressed ? B_KEY_DOWN : B_KEY_UP) : (k.pressed ? B_UNMAPPED_KEY_DOWN : B_UNMAPPED_KEY_UP);
	BMessage m(what);
	m.AddInt64("when", system_time());
	m.AddInt32("key", code);
	m.AddInt32("modifiers", mods);
	if (len) {
		m.AddInt32("raw_char", (int32)k.raw_char);
		for (size_t i = 0; i < len; i++) m.AddInt8("byte", (int8)k.utf8[i]);
		m.AddString("bytes", k.utf8);
	}
	m.AddData("states", B_UINT8_TYPE, w->keyStates, sizeof(w->keyStates), false);
	if (k.repeat) m.AddInt32("be:key_repeat", 1);
	SendInput(w, m);
}

void HandleInternal(SWindow* w, const Internal& e)
{
	switch (e.kind) {
		case AU_EV_CLOSE:
		{
			BMessage m(B_QUIT_REQUESTED);
			m.AddInt64("when", system_time());
			BMessenger reply;
			BMessage::Private(&m).SendMessage(w->clientLooperPort, w->app->team, w->clientToken, 0, false, reply);
			break;
		}
		case AU_EV_RESIZE:
			ApplyWindowResize(w, e.a, e.b);
			break;
		case AU_EV_KEY:
			HandleKey(w, e.key);
			break;
		case AU_EV_ACTIVATE:
		{
			w->active = e.a != 0;
			BMessage m(B_WINDOW_ACTIVATED);
			m.AddInt64("when", system_time());
			m.AddBool("active", w->active);
			BMessenger reply;
			// To the window's own token, not B_PREFERRED_TOKEN: the preferred handler is the focus view, and
			// BWindow::DispatchMessage forwards a B_WINDOW_ACTIVATED whose target is not the window to that
			// view's MessageReceived, which drops it. (Found with BTextView: its caret only exists while active.)
			BMessage::Private(&m).SendMessage(w->clientLooperPort, w->app->team, w->clientToken, 0, false, reply);
			break;
		}
		case AU_EV_MODS:
		{
			w->mods = gMods = HaikuModifiers((uint32)e.a);
			BMessage m(B_MODIFIERS_CHANGED);
			m.AddInt64("when", system_time());
			m.AddInt32("modifiers", w->mods);
			m.AddInt32("be:old_modifiers", HaikuModifiers((uint32)e.b));
			m.AddData("states", B_UINT8_TYPE, w->keyStates, sizeof(w->keyStates), false);
			SendInput(w, m);
			break;
		}
		case AU_EV_WHEEL:
		{
			// B_MOUSE_WHEEL_CHANGED as app_server sends it: deltas in "notches" (wayland px / 10), positive = down/right.
			float d = e.b / 256.0f / 10.0f;
			SView* hit = w->top;
			for (bool found = true; found && hit;) {
				found = false;
				for (size_t i = hit->children.size(); i-- > 0;) {
					SView* c = hit->children[i];
					if (!c->hidden && VisibleRect(c).Contains(w->ptrWhere)) { hit = c; found = true; break; }
				}
			}
			BMessage m(B_MOUSE_WHEEL_CHANGED);
			m.AddInt64("when", system_time());
			m.AddFloat("be:wheel_delta_x", e.a == 1 ? d : 0);
			m.AddFloat("be:wheel_delta_y", e.a == 0 ? d : 0);
			if (hit) m.AddInt32("_view_token", hit->token);
			BMessenger reply;
			BMessage::Private(&m).SendMessage(w->clientLooperPort, w->app->team, B_PREFERRED_TOKEN, 0, false, reply);
			break;
		}
		case AU_EV_POINTER_MOVE:
		case AU_EV_POINTER_BUTTON:
		{
			// Same fields app_server's EventDispatcher sends. The client's BWindow adds "where" and
			// "be:view_where" itself from "screen_where".
			static int buttons = 0;
			static BPoint where;
			if (e.kind == AU_EV_POINTER_MOVE)
				where = BPoint(floorf(e.a / 256.0f), floorf(e.b / 256.0f));
			else
				buttons = e.b ? B_PRIMARY_MOUSE_BUTTON : 0;
			w->ptrWhere = where;
			w->buttons = buttons;
			gPtr = BPoint(where.x + w->frame.left, where.y + w->frame.top); gPtrButtons = buttons;
			SView* hit = w->top;
			bool found = true;
			while (found && hit) {
				found = false;
				for (size_t i = hit->children.size(); i-- > 0;) {
					SView* c = hit->children[i];
					if (!c->hidden && VisibleRect(c).Contains(where)) { hit = c; found = true; break; }
				}
			}
			if (e.kind == AU_EV_POINTER_BUTTON && e.b) gPtrGrab = hit ? hit->token : B_NULL_TOKEN;
			int32 token = gPtrGrab != B_NULL_TOKEN ? gPtrGrab : (hit ? hit->token : B_NULL_TOKEN);
			BMessage m(e.kind == AU_EV_POINTER_MOVE ? B_MOUSE_MOVED : (e.b ? B_MOUSE_DOWN : B_MOUSE_UP));
			m.AddInt64("when", system_time());
			m.AddPoint("screen_where", BPoint(where.x + w->frame.left, where.y + w->frame.top));
			m.AddInt32("buttons", buttons);
			m.AddInt32("modifiers", 0);
			if (e.kind == AU_EV_POINTER_BUTTON && e.b) {
				// app_server counts clicks: same place (4 px), within the double-click interval (500 ms).
				bigtime_t now = system_time();
				if (w->lastClickTime && now - w->lastClickTime < 500000 && fabsf(where.x - w->lastClickWhere.x) <= 4 && fabsf(where.y - w->lastClickWhere.y) <= 4)
					w->clicks++;
				else
					w->clicks = 1;
				w->lastClickTime = now; w->lastClickWhere = where;
				m.AddInt32("clicks", w->clicks);
			}
			if (token != B_NULL_TOKEN) m.AddInt32("_view_token", token);
			if (e.kind == AU_EV_POINTER_BUTTON && !e.b) gPtrGrab = B_NULL_TOKEN;
			BMessenger reply;
			BMessage::Private(&m).SendMessage(w->clientLooperPort, w->app->team, B_PREFERRED_TOKEN, 0, false, reply);
			break;
		}
	}
}

void WindowDispatch(SWindow* w, int32 code, LinkReceiver& link)
{
	PortLink& reply = w->link;
	switch (code) {
		case AS_SHOW_OR_HIDE_WINDOW:
		{
			int32 level;
			if (link.Read<int32>(&level) == B_OK) Show(w, level <= 0);
			return;
		}
		case AS_SET_WINDOW_TITLE:
		{
			char* t = NULL; link.ReadString(&t); w->title = t ? t : ""; free(t);
			return;
		}
		case AS_SYNC:
			reply.StartMessage(B_OK); reply.Flush();
			return;
		case AS_GET_MOUSE:
			// Last pointer position (screen coordinates, = window coordinates + the window's frame origin) and buttons.
			// View.cpp reads a BPoint and a uint32 (ServerWindow replies with an int32; same four bytes).
			reply.StartMessage(B_OK);
			reply.Attach<BPoint>(gPtr);
			reply.Attach<uint32>((uint32)gPtrButtons);
			reply.Flush();
			return;
		case AS_BEGIN_UPDATE: BeginUpdate(w); return;
		case AS_END_UPDATE: EndUpdate(w); return;
		case AS_ENABLE_UPDATES: w->updatesEnabled = true; RequestUpdate(w); return;
		case AS_DISABLE_UPDATES: w->updatesEnabled = false; return;
		case AS_NEEDS_UPDATE:
			reply.StartMessage(w->dirty.CountRects() ? B_OK : B_ERROR); reply.Flush();
			return;
		case AS_ACTIVATE_WINDOW:
		case AS_SET_LOOK: case AS_SET_FEEL: case AS_SET_FLAGS: case AS_SET_WORKSPACES:
		case AS_GET_DECORATOR_SETTINGS:
		{
			// There is no decorator (the compositor draws the window's frame), so: no tab and no border. BAlert asks, to
			// centre itself on the screen by its decorated frame.
			BMessage settings;
			settings.AddRect("tab frame", BRect(0, 0, 0, 0));
			settings.AddFloat("border width", 0.0f);
			ssize_t size = settings.FlattenedSize();
			std::vector<char> buf(size);
			if (settings.Flatten(buf.data(), size) != B_OK) { reply.StartMessage(B_ERROR); reply.Flush(); return; }
			reply.StartMessage(B_OK); reply.Attach<int32>((int32)size); reply.Attach(buf.data(), size); reply.Flush();
			return;
		}
		case AS_SET_SIZE_LIMITS:
		{
			// Needs a reply (BWindow::SetSizeLimits blocks on it): the limits as enforced, and the frame. BMenuWindow's
			// constructor is the first caller, from the menu tracking thread.
			float a, b, c, d; link.Read<float>(&a); link.Read<float>(&b); link.Read<float>(&c); link.Read<float>(&d);
			a = std::max(a, 0.f); c = std::max(c, 0.f); b = std::max(b, a); d = std::max(d, c);
			// A frame outside the limits is brought inside them (BWindow adopts the frame we send back); layouts rely on
			// this: B_AUTO_UPDATE_SIZE_LIMITS sets the minimum to the layout's, and the window must grow to it.
			{
				float fw = w->frame.Width() + 1, fh = w->frame.Height() + 1;
				float nw = std::min(std::max(fw, a), b), nh = std::min(std::max(fh, c), d);
				if (nw != fw || nh != fh) ApplyWindowResize(w, (int)ceilf(nw), (int)ceilf(nh));
			}
			reply.StartMessage(B_OK);
			reply.Attach<BRect>(w->frame);
			reply.Attach<float>(a); reply.Attach<float>(b); reply.Attach<float>(c); reply.Attach<float>(d);
			reply.Flush();
			return;
		}
		case AS_MINIMIZE_WINDOW: case AS_SEND_BEHIND: case AS_ADD_TO_SUBSET:
		case AS_REMOVE_FROM_SUBSET: case AS_SYSTEM_FONT_CHANGED:
			if (code == AS_SEND_BEHIND) { reply.StartMessage(B_OK); reply.Flush(); }
			return;
		case AS_IS_FRONT_WINDOW:
			reply.StartMessage(B_OK); reply.Flush();
			return;
		case AS_WINDOW_MOVE: case AS_WINDOW_RESIZE:
		{
			float x, y; link.Read<float>(&x); link.Read<float>(&y);
			if (code == AS_WINDOW_RESIZE) ApplyWindowResize(w, (int)x + 1, (int)y + 1);
			else w->frame.OffsetTo(x, y);
			reply.StartMessage(B_OK); reply.Flush();
			return;
		}
		case AS_VIEW_CREATE_ROOT:
		{
			SView* v = CreateView(w, link, NULL);
			w->top = v; w->cur = v;
			return;
		}
		case AS_VIEW_CREATE:
		{
			SView* parent = NULL;
			SView* v = CreateView(w, link, &parent);
			if (parent) { v->parent = parent; parent->children.push_back(v); }
			else { w->views.erase(v->token); delete v; }
			if (w->visible && v && parent) InvalidateWindowRect(w, VisibleRect(v));
			return;
		}
		case AS_SET_CURRENT_VIEW:
		{
			int32 t; link.Read<int32>(&t);
			std::map<int32, SView*>::iterator it = w->views.find(t);
			w->cur = it != w->views.end() ? it->second : NULL;
			return;
		}
		case AS_VIEW_DELETE:
		{
			int32 t; link.Read<int32>(&t);
			std::map<int32, SView*>::iterator it = w->views.find(t);
			if (it != w->views.end() && it->second->parent) {
				SView* v = it->second; SView* p = v->parent;
				BRect vis = VisibleRect(v);
				for (size_t i = 0; i < p->children.size(); i++) if (p->children[i] == v) { p->children.erase(p->children.begin() + i); break; }
				DestroyView(w, v);
				if (w->visible) InvalidateWindowRect(w, vis);
			}
			return;
		}
		default:
			break;
	}

	// everything below needs a current view
	SView* v = w->cur;
	if (!v) { if (gTrace) fprintf(stderr, "appserver: op %d with no current view\n", (int)code); gUnhandled++; return; }

	NoteDraw(v, code);
	switch (code) {
		case AS_VIEW_SET_STATE: HandleViewState(w, v, link); return;
		case AS_VIEW_SET_FONT_STATE: HandleFontState(v, link); return;
		case AS_VIEW_GET_STATE:
		{
			ViewGetStateInfo i; memset(&i, 0, sizeof(i));
			i.fontID = (v->st.font.family << 16) | v->st.font.style; i.fontSize = v->st.fontSize;
			i.fontShear = v->st.shear; i.fontRotation = v->st.rotation; i.fontSpacing = v->st.spacing; i.fontFace = v->st.face;
			i.viewStateInfo.penLocation = v->st.pen; i.viewStateInfo.penSize = v->st.penSize;
			i.viewStateInfo.highColor = v->st.high; i.viewStateInfo.lowColor = v->st.low;
			i.viewStateInfo.pattern = v->st.pat; i.viewStateInfo.drawingMode = v->st.mode;
			i.viewStateInfo.origin = v->st.origin; i.viewStateInfo.scale = v->st.scale;
			i.viewStateInfo.miterLimit = B_DEFAULT_MITER_LIMIT; i.viewStateInfo.fontAntialiasing = true;
			reply.StartMessage(B_OK);
			reply.Attach<ViewGetStateInfo>(i);
			double t[6] = {1, 0, 0, 1, 0, 0};
			reply.Attach<double[6]>(t);
			reply.Attach<bool>(false);
			reply.Flush();
			return;
		}
		case AS_VIEW_SET_EVENT_MASK: return;
		case AS_VIEW_SET_MOUSE_EVENT_MASK:
		{
			uint32 mask = 0; link.Read<uint32>(&mask);
			if ((mask & B_POINTER_EVENTS) && gPtrGrab != B_NULL_TOKEN) gPtrGrab = v->token;
			return;
		}
		case AS_VIEW_GET_COORD:
			reply.StartMessage(B_OK);
			reply.Attach<BPoint>(v->frame.LeftTop());
			reply.Attach<BRect>(BRect(v->scroll.x, v->scroll.y, v->scroll.x + v->frame.Width(), v->scroll.y + v->frame.Height()));
			reply.Flush();
			return;
		case AS_VIEW_MOVE_TO:
		{
			float x, y; link.Read<float>(&x); link.Read<float>(&y);
			v->frame.OffsetTo(x, y);
			if (w->visible) InvalidateWindowRect(w, VisibleRect(v->parent ? v->parent : v));
			return;
		}
		case AS_VIEW_RESIZE_TO:
		{
			float x, y; link.Read<float>(&x); link.Read<float>(&y);
			v->frame.right = v->frame.left + x; v->frame.bottom = v->frame.top + y;
			if (w->visible) InvalidateWindowRect(w, VisibleRect(v->parent ? v->parent : v));
			return;
		}
		case AS_VIEW_SCROLL:
		{
			// BView::ScrollTo: the client already clamped to its scroll bars and moved fBounds; we move the
			// content. Haiku's app_server blits the still-visible part and invalidates the rest; we
			// invalidate the whole visible area (correct, not minimal).
			float dx, dy; link.Read<float>(&dx); link.Read<float>(&dy);
			v->scroll.x += dx; v->scroll.y += dy;
			if (w->visible) InvalidateWindowRect(w, VisibleRect(v));
			return;
		}
		case AS_VIEW_SET_ORIGIN: { float x, y; link.Read<float>(&x); link.Read<float>(&y); v->st.origin = BPoint(x, y); return; }
		case AS_VIEW_GET_ORIGIN: reply.StartMessage(B_OK); reply.Attach<BPoint>(v->st.origin); reply.Flush(); return;
		case AS_VIEW_RESIZE_MODE: link.Read<uint32>(&v->resize); return;
		case AS_VIEW_SET_FLAGS: link.Read<uint32>(&v->flags); return;
		case AS_VIEW_HIDE: v->hidden = true; if (w->visible && v->parent) InvalidateWindowRect(w, VisibleRect(v->parent)); return;
		case AS_VIEW_SHOW: v->hidden = false; if (w->visible) InvalidateWindowRect(w, VisibleRect(v)); return;
		case AS_VIEW_PUSH_STATE: v->stack.push_back(v->st); return;
		case AS_VIEW_POP_STATE: if (!v->stack.empty()) { v->st = v->stack.back(); v->stack.pop_back(); } return;
		case AS_VIEW_SET_SCALE: link.Read<float>(&v->st.scale); return;
		case AS_VIEW_SET_PEN_LOC: link.Read<BPoint>(&v->st.pen); return;
		case AS_VIEW_GET_PEN_LOC: reply.StartMessage(B_OK); reply.Attach<BPoint>(v->st.pen); reply.Flush(); return;
		case AS_VIEW_SET_PEN_SIZE: link.Read<float>(&v->st.penSize); return;
		case AS_VIEW_GET_PEN_SIZE: reply.StartMessage(B_OK); reply.Attach<float>(v->st.penSize); reply.Flush(); return;
		case AS_VIEW_SET_VIEW_COLOR: link.Read(&v->viewColor, sizeof(rgb_color)); v->viewWhich = B_NO_COLOR; return;
		case AS_VIEW_GET_VIEW_COLOR: reply.StartMessage(B_OK); reply.Attach<rgb_color>(v->viewColor); reply.Flush(); return;
		case AS_VIEW_SET_HIGH_COLOR: link.Read(&v->st.high, sizeof(rgb_color)); v->st.highWhich = B_NO_COLOR; return;
		case AS_VIEW_SET_LOW_COLOR: link.Read(&v->st.low, sizeof(rgb_color)); v->st.lowWhich = B_NO_COLOR; return;
		case AS_VIEW_SET_HIGH_UI_COLOR: case AS_VIEW_SET_LOW_UI_COLOR: case AS_VIEW_SET_VIEW_UI_COLOR:
		{
			// The client sent a semantic colour (color_which) plus a tint. In-process we can resolve it with
			// the Interface Kit's own ui_color(); a separate-process server would read the colour table.
			color_which which = B_NO_COLOR; float tint = B_NO_TINT;
			link.Read<color_which>(&which); link.Read<float>(&tint);
			if (which == B_NO_COLOR) return;	// "forget the semantic colour"; the literal colour stays
			rgb_color c = tint_color(ui_color(which), tint);
			if (code == AS_VIEW_SET_HIGH_UI_COLOR) { v->st.high = c; v->st.highWhich = which; v->st.highTint = tint; }
			else if (code == AS_VIEW_SET_LOW_UI_COLOR) { v->st.low = c; v->st.lowWhich = which; v->st.lowTint = tint; }
			else { v->viewColor = c; v->viewWhich = which; v->viewTint = tint; }
			return;
		}
		case AS_VIEW_GET_HIGH_COLOR: reply.StartMessage(B_OK); reply.Attach<rgb_color>(v->st.high); reply.Flush(); return;
		case AS_VIEW_GET_LOW_COLOR: reply.StartMessage(B_OK); reply.Attach<rgb_color>(v->st.low); reply.Flush(); return;
		case AS_VIEW_SET_PATTERN: link.Read<::pattern>(&v->st.pat); return;
		case AS_VIEW_SET_DRAWING_MODE: { int8 m; link.Read<int8>(&m); v->st.mode = (drawing_mode)m; return; }
		case AS_VIEW_SET_BLENDING_MODE: case AS_VIEW_SET_LINE_MODE: case AS_VIEW_SET_FILL_RULE: return;
		case AS_VIEW_COPY_BITS:
		{
			BRect s, d; link.Read<BRect>(&s); if (link.Read<BRect>(&d) != B_OK) return;
			SetupClip(w, v);
			BRect ws = ToWindow(v, s), wd = ToWindow(v, d);
			w->canvas.CopyBits(FRect{ws.left, ws.top, ws.right, ws.bottom}, FRect{wd.left, wd.top, wd.right, wd.bottom});
			return;
		}
		case AS_VIEW_GET_CLIP_REGION:
		{
			// What the view may draw into, in view coordinates: its visible rect (ancestors' clipping included) cut by
			// the user clip. Only a bounding rectangle is kept for either (SetupClip), so the region is one rectangle.
			BRegion region;
			BRect c = EffectivelyHidden(v) ? BRect() : VisibleRect(v);
			BPoint o = WindowOffset(v);
			if (c.IsValid() && v->st.hasClip) c = c & v->st.clip.OffsetByCopy(o);
			if (c.IsValid()) region.Set(c.OffsetByCopy(-o.x, -o.y));
			reply.StartMessage(B_OK); reply.AttachRegion(region); reply.Flush();
			return;
		}
		case AS_VIEW_SET_CLIP_REGION:
		{
			bool has = false; link.Read<bool>(&has);
			v->st.hasClip = false;
			if (has) { BRegion r; link.ReadRegion(&r); v->st.hasClip = true; v->st.clip = r.Frame(); }
			return;
		}
		case AS_VIEW_CLIP_TO_RECT:
		{
			bool inverse; BRect r; link.Read<bool>(&inverse); link.Read<BRect>(&r);
			if (!inverse) { v->st.clip = v->st.hasClip ? (v->st.clip & r) : r; v->st.hasClip = true; }
			return;
		}
		case AS_VIEW_INVALIDATE_RECT:
		{
			BRect r;
			if (link.Read<BRect>(&r) == B_OK) {
				BPoint o = WindowOffset(v);
				InvalidateWindowRect(w, r.OffsetByCopy(o) & VisibleRect(v));
			}
			return;
		}
		case AS_VIEW_INVALIDATE_REGION:
		{
			BRegion r;
			if (link.ReadRegion(&r) == B_OK) {
				BPoint o = WindowOffset(v);
				r.OffsetBy(o.x, o.y);
				for (int32 i = 0; i < r.CountRects(); i++) InvalidateWindowRect(w, r.RectAt(i) & VisibleRect(v));
			}
			return;
		}

		// ---- drawing ----
		case AS_STROKE_LINE:
		{
			ViewStrokeLineInfo i; if (link.Read<ViewStrokeLineInfo>(&i) != B_OK) return;
			SetupClip(w, v);
			BPoint a = ToWindow(v, i.startPoint), b = ToWindow(v, i.endPoint);
			w->canvas.StrokeLine(a.x, a.y, b.x, b.y, DrawColor(v->st), v->st.penSize * v->st.scale);
			v->st.pen = i.endPoint;
			return;
		}
		case AS_VIEW_DRAW_BITMAP:
		{
			ViewDrawBitmapInfo i; if (link.Read<ViewDrawBitmapInfo>(&i) != B_OK) return;
			auto it = gBitmaps.find(i.bitmapToken);
			if (it == gBitmaps.end()) return;
			const SBitmap& bm = it->second;
			int bw = bm.bounds.IntegerWidth() + 1, bh = bm.bounds.IntegerHeight() + 1;
			std::vector<uint32_t> px((size_t)bw * bh, 0);
			bool transparentOk = v->st.mode != B_OP_COPY;
			bool alphaOk = v->st.mode == B_OP_ALPHA || bm.cs == B_RGBA32;
			for (int y = 0; y < bh; y++) for (int x = 0; x < bw; x++) {
				const uint8* p = bm.base + (size_t)y * bm.bpr;
				uint32_t o = 0xFF000000u;
				switch (bm.cs) {
					case B_RGB32: case B_RGBA32: { const uint8* q = p + x * 4; o = ((alphaOk ? q[3] : 255u) << 24) | (q[2] << 16) | (q[1] << 8) | q[0]; break; }
					case B_RGB24: { const uint8* q = p + x * 3; o |= (q[2] << 16) | (q[1] << 8) | q[0]; break; }
					case B_GRAY8: o |= p[x] * 0x010101u; break;
					case B_CMAP8: {
						if (p[x] == B_TRANSPARENT_MAGIC_CMAP8 && transparentOk) { o = 0; break; }
						rgb_color c = kSystemPalette[p[x]]; o |= (c.red << 16) | (c.green << 8) | c.blue; break;
					}
					default: gUnhandled++; return;
				}
				px[(size_t)y * bw + x] = o;
			}
			SetupClip(w, v);
			BRect dst = ToWindow(v, i.viewRect);
			w->canvas.DrawImage(px.data(), bw, bh, FRect{i.bitmapRect.left - bm.bounds.left, i.bitmapRect.top - bm.bounds.top, i.bitmapRect.right - bm.bounds.left, i.bitmapRect.bottom - bm.bounds.top}, FRect{dst.left, dst.top, dst.right, dst.bottom});
			return;
		}
		case AS_FILL_REGION:
		{
			// Rects of the region, in the view's coordinates. B_OP_INVERT is how BTextView paints its selection.
			BRegion r; if (link.ReadRegion(&r) != B_OK) return;
			SetupClip(w, v);
			for (int32 i = 0; i < r.CountRects(); i++) {
				BRect wr = ToWindow(v, r.RectAt(i));
				FRect f{wr.left, wr.top, wr.right, wr.bottom};
				if (v->st.mode == B_OP_INVERT) w->canvas.InvertRect(f);
				else w->canvas.FillRect(f, DrawColor(v->st));
			}
			return;
		}
		case AS_VIEW_INVERT_RECT:
		{
			BRect r; if (link.Read<BRect>(&r) != B_OK) return;
			SetupClip(w, v);
			BRect wr = ToWindow(v, r);
			w->canvas.InvertRect(FRect{wr.left, wr.top, wr.right, wr.bottom});
			return;
		}
		case AS_STROKE_RECT: case AS_FILL_RECT:
		{
			BRect r; if (link.Read<BRect>(&r) != B_OK) return;
			SetupClip(w, v);
			BRect wr = ToWindow(v, r);
			FRect f{wr.left, wr.top, wr.right, wr.bottom};
			if (code == AS_FILL_RECT && v->st.mode == B_OP_INVERT) w->canvas.InvertRect(f);
			else if (code == AS_FILL_RECT) w->canvas.FillRect(f, DrawColor(v->st));
			else w->canvas.StrokeRect(f, DrawColor(v->st), v->st.penSize * v->st.scale);
			return;
		}
		case AS_STROKE_ROUNDRECT: case AS_FILL_ROUNDRECT:
		{
			BRect r; float rx, ry; link.Read<BRect>(&r); link.Read<float>(&rx); if (link.Read<float>(&ry) != B_OK) return;
			SetupClip(w, v);
			BRect wr = ToWindow(v, r);
			FRect f{wr.left, wr.top, wr.right, wr.bottom};
			if (code == AS_FILL_ROUNDRECT) w->canvas.FillRoundRect(f, rx, ry, DrawColor(v->st));
			else w->canvas.StrokeRoundRect(f, rx, ry, DrawColor(v->st), v->st.penSize * v->st.scale);
			return;
		}
		case AS_FILL_RECT_GRADIENT: case AS_FILL_ROUNDRECT_GRADIENT:
		{
			BRect r; float rx = 0, ry = 0;
			link.Read<BRect>(&r);
			if (code == AS_FILL_ROUNDRECT_GRADIENT) { link.Read<float>(&rx); link.Read<float>(&ry); }
			Paint p;
			if (!ReadPaint(v, link, &p)) { gUnhandled++; return; }	// non-linear gradients are not drawn
			SetupClip(w, v);
			BRect wr = ToWindow(v, r);
			FRect f{wr.left, wr.top, wr.right, wr.bottom};
			if (code == AS_FILL_RECT_GRADIENT) w->canvas.FillRect(f, p); else w->canvas.FillRoundRect(f, rx, ry, p);
			return;
		}
		case AS_STROKE_LINEARRAY:
		{
			int32 n = 0;
			if (link.Read<int32>(&n) != B_OK || n <= 0) return;
			std::vector<ViewLineArrayInfo> lines(n);
			if (link.Read(lines.data(), n * sizeof(ViewLineArrayInfo)) != B_OK) return;
			SetupClip(w, v);
			for (int32 k = 0; k < n; k++) {
				BPoint a = ToWindow(v, lines[k].startPoint), b = ToWindow(v, lines[k].endPoint);
				w->canvas.StrokeLine(a.x, a.y, b.x, b.y, ToColor(lines[k].color), v->st.penSize * v->st.scale);
			}
			return;
		}
		case AS_VIEW_GET_SCALE: reply.StartMessage(B_OK); reply.Attach<float>(v->st.scale); reply.Flush(); return;
		case AS_VIEW_GET_TRANSFORM:
		{
			double t[6] = {1, 0, 0, 1, 0, 0};
			reply.StartMessage(B_OK); reply.Attach(t, sizeof(t)); reply.Flush();
			return;
		}
		case AS_VIEW_GET_PARENT_COMPOSITE:
		{
			double t[6] = {1, 0, 0, 1, 0, 0};
			reply.StartMessage(B_OK); reply.Attach(t, sizeof(t)); reply.Attach<float>(1.0f);
			reply.Attach<BPoint>(BPoint(0, 0)); reply.Flush();
			return;
		}
		case AS_VIEW_GET_VIEW_UI_COLOR: case AS_VIEW_GET_HIGH_UI_COLOR: case AS_VIEW_GET_LOW_UI_COLOR:
		{
			color_which wh = v->viewWhich; float t = v->viewTint; rgb_color c = v->viewColor;
			if (code == AS_VIEW_GET_HIGH_UI_COLOR) { wh = v->st.highWhich; t = v->st.highTint; c = v->st.high; }
			if (code == AS_VIEW_GET_LOW_UI_COLOR) { wh = v->st.lowWhich; t = v->st.lowTint; c = v->st.low; }
			reply.StartMessage(B_OK);
			reply.Attach<color_which>(wh); reply.Attach<float>(t); reply.Attach<rgb_color>(c);
			reply.Flush();
			return;
		}
		case AS_VIEW_GET_DRAWING_MODE: { int8 m = (int8)v->st.mode; reply.StartMessage(B_OK); reply.Attach<int8>(m); reply.Flush(); return; }
		case AS_STROKE_ELLIPSE: case AS_FILL_ELLIPSE:
		{
			BRect r; if (link.Read<BRect>(&r) != B_OK) return;
			SetupClip(w, v);
			BRect wr = ToWindow(v, r);
			FRect f{wr.left, wr.top, wr.right, wr.bottom};
			if (code == AS_FILL_ELLIPSE) w->canvas.FillEllipse(f, DrawColor(v->st));
			else w->canvas.StrokeEllipse(f, DrawColor(v->st), v->st.penSize * v->st.scale);
			return;
		}
		case AS_FILL_ELLIPSE_GRADIENT:
		{
			BRect r; link.Read<BRect>(&r);
			Paint p;
			if (!ReadPaint(v, link, &p)) { gUnhandled++; return; }
			SetupClip(w, v);
			BRect wr = ToWindow(v, r);
			w->canvas.FillEllipse(FRect{wr.left, wr.top, wr.right, wr.bottom}, p);
			return;
		}
		case AS_VIEW_SET_TRANSFORM:
		{
			// Only the identity is honoured (BTab sets and resets one around every label; nothing else here sets a real one).
			double t[6];
			if (link.Read(t, sizeof(t)) != B_OK) return;
			bool identity = t[0] == 1 && t[1] == 0 && t[2] == 0 && t[3] == 1 && t[4] == 0 && t[5] == 0;
			if (!identity) gUnhandled++;
			return;
		}
		case AS_STROKE_SHAPE: case AS_FILL_SHAPE:
		{
			BRect bounds; BShape shape;
			if (link.Read<BRect>(&bounds) != B_OK || link.ReadShape(&shape) != B_OK) return;
			SetupClip(w, v);
			ShapeFlattener f(v);
			f.Iterate(&shape);
			for (size_t i = 0; i < f.paths.size(); i++) {
				std::vector<BPoint>& p = f.paths[i];
				if (code == AS_FILL_SHAPE) {
					if (p.size() < 3) continue;
					std::vector<float> xy;
					for (size_t k = 0; k < p.size(); k++) { xy.push_back(p[k].x + 0.5f); xy.push_back(p[k].y + 0.5f); }
					w->canvas.FillPolygon(xy.data(), (int)p.size(), DrawColor(v->st));
				} else {
					if (f.closed[i] && p.size() > 1) p.push_back(p[0]);
					for (size_t k = 1; k < p.size(); k++)
						w->canvas.StrokeLine(p[k - 1].x, p[k - 1].y, p[k].x, p[k].y, DrawColor(v->st), v->st.penSize * v->st.scale);
				}
			}
			return;
		}
		case AS_FILL_TRIANGLE: case AS_STROKE_TRIANGLE:
		{
			BPoint p[3]; BRect bounds;
			link.Read(p, sizeof(p)); link.Read<BRect>(&bounds);
			SetupClip(w, v);
			float xy[6];
			for (int k = 0; k < 3; k++) { BPoint q = ToWindow(v, p[k]); xy[2 * k] = q.x + 0.5f; xy[2 * k + 1] = q.y + 0.5f; }
			if (code == AS_FILL_TRIANGLE) w->canvas.FillPolygon(xy, 3, DrawColor(v->st));
			return;
		}
		case AS_DRAW_STRING: case AS_DRAW_STRING_WITH_DELTA:
		{
			ViewDrawStringInfo i;
			if (link.Read<ViewDrawStringInfo>(&i) != B_OK || i.stringLength <= 0) return;
			std::vector<char> s(i.stringLength + 1, 0);
			if (link.Read(s.data(), i.stringLength) != B_OK) return;
			SetupClip(w, v);
			if (gTrace) fprintf(stderr, "appserver: draw string \"%s\" font=%d/%d size=%.2f at %.1f,%.1f\n", s.data(), (int)v->st.font.family, (int)v->st.font.style, v->st.fontSize, i.location.x, i.location.y);
			BPoint start = ToWindow(v, i.location);
			BPoint end = DrawText(w, v->st, s.data(), i.stringLength, start, code == AS_DRAW_STRING_WITH_DELTA ? &i.delta : NULL);
			RecordRun(v, s.data(), i.location, start, end);
			BPoint o = WindowOffset(v);
			v->st.pen = BPoint((end.x - o.x) / v->st.scale - v->st.origin.x, (end.y - o.y) / v->st.scale - v->st.origin.y);
			return;
		}
		default:
			break;
	}
	gUnhandled++;
	if (link.NeedsReply()) { gUnhandledReply++; reply.StartMessage(B_ERROR); reply.Flush(); }
	if (gTrace) fprintf(stderr, "appserver: window op %d not handled%s\n", (int)code, link.NeedsReply() ? " (reply sent: B_ERROR)" : "");
}

// An offscreen window's pixels into its bitmap (32-bit ARGB/xRGB: the canvas's own layout), row by row.
void SyncOffscreen(SWindow* w)
{
	auto it = gBitmaps.find(w->offscreenBitmap);
	if (it == gBitmaps.end()) return;
	const SBitmap& bm = it->second;
	if (bm.cs != B_RGB32 && bm.cs != B_RGBA32) return;
	int rows = std::min(w->canvas.Height(), (int)bm.bounds.IntegerHeight() + 1), cols = std::min(w->canvas.Width(), (int)bm.bounds.IntegerWidth() + 1);
	for (int y = 0; y < rows; y++)
		memcpy(bm.base + (size_t)y * bm.bpr, w->canvas.Pixels() + (size_t)y * w->canvas.Width(), (size_t)cols * 4);
}

void WindowThread(SWindow* w)
{
	LinkReceiver& link = w->link.Receiver();
	while (true) {
		int32 code;
		if (link.GetNextMessage(code) != B_OK) break;
		std::lock_guard<std::recursive_mutex> g(gMu);
		if (gTrace) fprintf(stderr, "appserver[%s]: op %d\n", w->title.c_str(), (int)code);
		if (code == AS_DELETE_WINDOW || code == B_QUIT_REQUESTED) {
			if (code == AS_DELETE_WINDOW) { w->link.StartMessage(B_OK); w->link.Flush(); }
			w->visible = false; w->quit = true;
			if (w->wl) { au_wl_win_destroy(w->wl); w->wl = NULL; }
			break;
		}
		if (code == kInternalEvent) {
			Internal e; link.Read(&e, sizeof(e));
			HandleInternal(w, e);
			continue;
		}
		WindowDispatch(w, code, link);
		if (w->offscreenBitmap >= 0) SyncOffscreen(w);
		// A real app_server draws to the screen at once. Ours presents at EndUpdate, so a view that draws outside an
		// update (BTextView inverts its caret that way, on every blink) would only show up at the next repaint.
		// Present when the client has nothing more queued.
		if (w->directDraw && !w->inUpdate && w->visible && port_count(w->port) <= 0) { w->directDraw = false; PresentWindow(w); }
	}
}

void PostInternal(SWindow* w, int kind, int a, int b)
{
	PortLink l(w->port, -1);
	l.StartMessage(kInternalEvent);
	Internal e{kind, a, b, {}};
	l.Attach(&e, sizeof(e));
	l.Flush();
}

void PostKey(SWindow* w, const au_key& k)
{
	PortLink l(w->port, -1);
	l.StartMessage(kInternalEvent);
	Internal e{AU_EV_KEY, 0, 0, k};
	l.Attach(&e, sizeof(e));
	l.Flush();
}

}	// namespace

void WlEvent(void* user, int kind, int a, int b)
{
	PostInternal((SWindow*)user, kind, a, b);
}

void WlKey(void* user, const au_key* k)
{
	PostKey((SWindow*)user, *k);
}

namespace {

// ------------------------------------------------------------------------------------------------ apps

void CreateWindow(SApp* app, LinkReceiver& link, PortLink& reply, bool offscreen = false)
{
	int32 bitmapToken = -1;
	if (offscreen) link.Read<int32>(&bitmapToken);
	BRect frame; uint32 look, feel, flags, workspace; int32 token; port_id replyPort, looperPort; char* title = NULL;
	link.Read<BRect>(&frame); link.Read<uint32>(&look); link.Read<uint32>(&feel); link.Read<uint32>(&flags);
	link.Read<uint32>(&workspace); link.Read<int32>(&token);
	link.Read<port_id>(&replyPort); link.Read<port_id>(&looperPort); link.ReadString(&title);

	SWindow* w = new SWindow;
	w->app = app; w->clientToken = token; w->clientLooperPort = looperPort;
	w->frame = frame; w->title = title ? title : ""; free(title); w->flags = flags; w->feel = feel;
	w->port = create_port(100, "w<app_server_win");
	w->link.SetTo(replyPort, w->port);
	if (offscreen) {
		// A BBitmap that accepts views: BColumnListView double-buffers into one. The window is not listed (it has no frame
		// to capture or dump and is never presented); its canvas is the bitmap's size at scale 1, and WindowThread copies
		// it into the bitmap's memory after every message, which is what BView::DrawBitmap then reads.
		w->index = -1; w->offscreenBitmap = bitmapToken;
		auto it = gBitmaps.find(bitmapToken);
		if (it != gBitmaps.end()) { frame = it->second.bounds; w->frame = frame; }
		w->canvas.Init((int)frame.IntegerWidth() + 1, (int)frame.IntegerHeight() + 1, 1.0);
	} else {
		{
			std::lock_guard<std::recursive_mutex> g(gMu);
			w->index = (int)gWindows.size();
			gWindows.push_back(w);
		}
		w->canvas.Init((int)frame.IntegerWidth() + 1, (int)frame.IntegerHeight() + 1, gScale);
	}
	std::thread(WindowThread, w).detach();

	PortLink r(replyPort, -1);
	r.StartMessage(B_OK);
	r.Attach<port_id>(w->port);
	r.Attach<BRect>(frame);
	r.Attach<float>(1.0f); r.Attach<float>(32768.0f); r.Attach<float>(1.0f); r.Attach<float>(32768.0f);
	r.Flush();
	(void)reply;
}

void AppThread(SApp* app)
{
	LinkReceiver& link = app->link.Receiver();
	while (true) {
		int32 code;
		if (link.GetNextMessage(code) != B_OK) break;
		std::lock_guard<std::recursive_mutex> g(gMu);
		if (gTrace) fprintf(stderr, "appserver[app %s]: op %d\n", app->sig.c_str(), (int)code);
		if (code == B_QUIT_REQUESTED || code == AS_QUIT_APP) break;
		if (code == AS_CREATE_WINDOW) { CreateWindow(app, link, app->link); continue; }
		if (code == AS_CREATE_OFFSCREEN_WINDOW) { CreateWindow(app, link, app->link, true); continue; }
		bool handled;
		HandleFontOp(app, code, link, app->link, &handled);
		if (handled) continue;
		gUnhandled++;
		if (link.NeedsReply()) { gUnhandledReply++; app->link.StartMessage(B_ERROR); app->link.Flush(); }
		if (gTrace) fprintf(stderr, "appserver: app op %d not handled%s\n", (int)code, link.NeedsReply() ? " (B_ERROR reply)" : "");
	}
	delete_port(app->port);
}

void MainThread(port_id mainPort)
{
	PortLink main(-1, mainPort);
	while (true) {
		int32 code;
		if (main.Receiver().GetNextMessage(code) != B_OK) break;
		if (code != AS_CREATE_APP) { gUnhandled++; continue; }
		SApp* app = new SApp;
		port_id replyPort;
		main.Receiver().Read<port_id>(&replyPort);
		main.Receiver().Read<port_id>(&app->looperPort);
		main.Receiver().Read<team_id>(&app->team);
		int32 token; main.Receiver().Read<int32>(&token);
		char* sig = NULL; main.Receiver().ReadString(&sig);
		app->sig = sig ? sig : ""; free(sig);
		app->port = create_port(100, "a<app_server_app");
		app->link.SetTo(replyPort, app->port);
		std::thread(AppThread, app).detach();
		PortLink r(replyPort, -1);
		r.StartMessage(B_OK);
		r.Attach<port_id>(app->port);
		r.Attach<area_id>(-1);		// read-only colour area: not provided, ui_color() uses Haiku's defaults
		r.Attach<team_id>(getpid());
		r.Flush();
	}
}

port_id gMainPort = -1;
std::once_flag gOnce;

void StartServer()
{
	gTrace = getenv("AURORA_APPSERVER_TRACE") != NULL;
	double scale = 1.0;
	if (const char* s = getenv("AURORA_UI_SCALE")) { double v = atof(s); if (v > 0) scale = v; }
	bool wantWl = getenv("WAYLAND_DISPLAY") && !(getenv("AURORA_APPSERVER_PRESENT") && !strcmp(getenv("AURORA_APPSERVER_PRESENT"), "0"));
	if (wantWl) {
		gWl = au_wl_connect();
		if (gWl) {
			if (!getenv("AURORA_UI_SCALE")) scale = au_wl_output_scale(gWl);
			// The compositor takes integer buffer scales only: a fractional request renders at the next integer.
			if (scale != floor(scale)) {
				fprintf(stderr, "appserver: scale %.3f is fractional; no fractional-scale protocol, rendering at %d\n", scale, (int)ceil(scale));
				scale = ceil(scale);
			}
		} else
			fprintf(stderr, "appserver: no Wayland compositor reachable; frames stay in memory\n");
	}
	gScale = scale;
	gMainPort = create_port(100, "a<app_server");
	std::thread(MainThread, gMainPort).detach();
}

}	// namespace

port_id appserver_port()
{
	std::call_once(gOnce, StartServer);
	return gMainPort;
}

bool last_frame(int index, Frame* out)
{
	std::lock_guard<std::recursive_mutex> g(gMu);
	if (index < 0 || index >= (int)gWindows.size() || !gWindows[index]->haveLast) return false;
	*out = gWindows[index]->last;
	return true;
}

int unhandled_opcodes(int* withReply)
{
	std::lock_guard<std::recursive_mutex> g(gMu);
	if (withReply) *withReply = gUnhandledReply;
	return gUnhandled;
}

static void Snap(const SView* v, UiWindow* uw)
{
	UiNode n;
	n.token = v->token; n.parent = v->parent ? v->parent->token : -1; n.name = v->name;
	n.l = v->frame.left; n.t = v->frame.top; n.r = v->frame.right; n.b = v->frame.bottom;
	BPoint po = v->parent ? WindowOffset(v->parent) : BPoint(0, 0);
	BRect abs = v->parent ? v->frame.OffsetByCopy(po) : v->frame.OffsetToCopy(0, 0);
	n.al = abs.left; n.at = abs.top; n.ar = abs.right; n.ab = abs.bottom;
	BRect vis = VisibleRect(v);
	n.clipped = !vis.IsValid();
	if (!n.clipped) { n.vl = vis.left; n.vt = vis.top; n.vr = vis.right; n.vb = vis.bottom; }
	n.hidden = v->hidden; n.effectively_hidden = EffectivelyHidden(v);
	n.flags = v->flags; n.navigable = (v->flags & B_NAVIGABLE) != 0;
	DeriveRole(v, &n);
	n.drew["strings"] = v->ev.strings; n.drew["ellipses"] = v->ev.ellipses; n.drew["roundrects"] = v->ev.roundrects;
	n.drew["gradients"] = v->ev.gradients; n.drew["rects"] = v->ev.rects; n.drew["lines"] = v->ev.lines;
	n.drew["bitmaps"] = v->ev.bitmaps; n.drew["shapes"] = v->ev.shapes;
	for (const SView::Run& r : v->runs) {
		UiText t; t.text = r.text; t.x = r.at.x; t.y = r.at.y; t.width = r.width; t.size = r.size;
		t.wl = r.win.left; t.wt = r.win.top; t.wr = r.win.right; t.wb = r.win.bottom;
		n.text.push_back(std::move(t));
	}
	for (const SView* c : v->children) n.children.push_back(c->token);
	uw->nodes.push_back(std::move(n));
	for (const SView* c : v->children) Snap(c, uw);
}

std::vector<UiWindow> ui_snapshot()
{
	std::lock_guard<std::recursive_mutex> g(gMu);
	std::vector<UiWindow> out;
	for (SWindow* w : gWindows) {
		if (w->quit || !w->top) continue;
		UiWindow uw;
		uw.index = w->index; uw.app = w->app ? w->app->sig : ""; uw.title = w->title;
		uw.kind = IsMenu(w) ? "menu" : IsModal(w) ? "modal" : "window";
		uw.width = (int)w->frame.IntegerWidth() + 1; uw.height = (int)w->frame.IntegerHeight() + 1;
		uw.visible = w->visible; uw.active = w->active;
		Snap(w->top, &uw);
		out.push_back(std::move(uw));
	}
	return out;
}

double output_scale() { return gScale; }
uint32_t current_modifiers() { return gMods; }

bool inject_special_key(int window, const char* name, bool shift)
{
	static const struct { const char* n; uint32_t ev; char ch; } k[] = {
		{"BackSpace", 14, 0x08}, {"Delete", 111, 0x7f}, {"Home", 102, 0x01}, {"End", 107, 0x04}, {"PageUp", 104, 0x0b}, {"PageDown", 109, 0x0c}};
	SWindow* w;
	{ std::lock_guard<std::recursive_mutex> g(gMu); if (window < 0 || window >= (int)gWindows.size()) return false; w = gWindows[window]; }
	for (auto& e : k) {
		if (strcmp(e.n, name)) continue;
		for (int pressed = 1; pressed >= 0; pressed--) {
			au_key key; memset(&key, 0, sizeof(key));
			key.evdev = e.ev; key.pressed = pressed; key.mods = shift ? AU_MOD_SHIFT : 0; key.raw_char = (uint32_t)e.ch; key.utf8[0] = e.ch;
			PostKey(w, key);
		}
		return true;
	}
	return false;
}

// Test hook: the same internal event the Wayland pointer callbacks post, so a test can click without a seat.
void inject_pointer(int window, float x, float y, int button)	// button: -1 move only, 0 release, 1 press
{
	SWindow* w;
	{ std::lock_guard<std::recursive_mutex> g(gMu); if (window < 0 || window >= (int)gWindows.size()) return; w = gWindows[window]; }
	PostInternal(w, AU_EV_POINTER_MOVE, (int)(x * 256), (int)(y * 256));
	if (button >= 0) PostInternal(w, AU_EV_POINTER_BUTTON, 1, button);
}

}	// namespace aurora
