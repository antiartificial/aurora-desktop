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
#include <ServerProtocol.h>
#include <ServerProtocolStructs.h>
#include <View.h>
#include <Window.h>

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <map>
#include <memory>
#include <mutex>
#include <thread>

using BPrivate::LinkReceiver;
using BPrivate::PortLink;
namespace text = aurora::text;

namespace aurora {

void WlEvent(void* user, int kind, int a, int b);

namespace {

std::recursive_mutex gMu;
bool gTrace = false;
int gUnhandled = 0, gUnhandledReply = 0;
double gScale = 1.0;
au_wl* gWl = NULL;
std::vector<struct SWindow*> gWindows;	// creation order, never shrinks (index = last_frame index)
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
};

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
	uint32 flags = 0;
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
};

struct SApp {
	port_id port = -1;
	PortLink link;
	team_id team = 0;
	port_id looperPort = -1;
	std::string sig;
};

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
	if (w->wl)
		au_wl_win_present(w->wl, w->canvas.Pixels(), w->canvas.Width(), w->canvas.Height(), (int)ceil(w->canvas.Scale()));
	if (const char* dir = getenv("AURORA_APPSERVER_DUMP")) {
		char path[512];
		snprintf(path, sizeof(path), "%s/window-%d.ppm", dir, w->index);
		if (FILE* f = fopen(path, "wb")) {
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

void Show(SWindow* w, bool show)
{
	if (show == w->visible) return;
	w->visible = show;
	if (show) {
		if (!w->wl && gWl) {
			int lw = (int)w->frame.IntegerWidth() + 1, lh = (int)w->frame.IntegerHeight() + 1;
			w->wl = au_wl_win_create(gWl, w->title.c_str(), lw, lh, WlEvent, w);
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
	BRect vis = VisibleRect(v);
	BRect c = vis;
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
	w->frame.right = w->frame.left + lw - 1;
	w->frame.bottom = w->frame.top + lh - 1;
	w->canvas.Init(lw, lh, gScale);
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

struct Internal { int kind, a, b; };

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
		case AU_EV_POINTER_MOVE:
		case AU_EV_POINTER_BUTTON:
		{
			// Same fields app_server's EventDispatcher sends. The client's BWindow adds "where" and
			// "be:view_where" itself from "screen_where".
			static int buttons = 0;
			static int32 grab = B_NULL_TOKEN;
			static BPoint where;
			if (e.kind == AU_EV_POINTER_MOVE)
				where = BPoint(floorf(e.a / 256.0f), floorf(e.b / 256.0f));
			else
				buttons = e.b ? B_PRIMARY_MOUSE_BUTTON : 0;
			SView* hit = w->top;
			bool found = true;
			while (found && hit) {
				found = false;
				for (size_t i = hit->children.size(); i-- > 0;) {
					SView* c = hit->children[i];
					if (!c->hidden && VisibleRect(c).Contains(where)) { hit = c; found = true; break; }
				}
			}
			if (e.kind == AU_EV_POINTER_BUTTON && e.b) grab = hit ? hit->token : B_NULL_TOKEN;
			int32 token = grab != B_NULL_TOKEN ? grab : (hit ? hit->token : B_NULL_TOKEN);
			BMessage m(e.kind == AU_EV_POINTER_MOVE ? B_MOUSE_MOVED : (e.b ? B_MOUSE_DOWN : B_MOUSE_UP));
			m.AddInt64("when", system_time());
			m.AddPoint("screen_where", BPoint(where.x + w->frame.left, where.y + w->frame.top));
			m.AddInt32("buttons", buttons);
			m.AddInt32("modifiers", 0);
			if (e.kind == AU_EV_POINTER_BUTTON && e.b) m.AddInt32("clicks", 1);
			if (token != B_NULL_TOKEN) m.AddInt32("_view_token", token);
			if (e.kind == AU_EV_POINTER_BUTTON && !e.b) grab = B_NULL_TOKEN;
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
		case AS_BEGIN_UPDATE: BeginUpdate(w); return;
		case AS_END_UPDATE: EndUpdate(w); return;
		case AS_ENABLE_UPDATES: w->updatesEnabled = true; RequestUpdate(w); return;
		case AS_DISABLE_UPDATES: w->updatesEnabled = false; return;
		case AS_NEEDS_UPDATE:
			reply.StartMessage(w->dirty.CountRects() ? B_OK : B_ERROR); reply.Flush();
			return;
		case AS_ACTIVATE_WINDOW:
		case AS_SET_LOOK: case AS_SET_FEEL: case AS_SET_FLAGS: case AS_SET_WORKSPACES:
		case AS_MINIMIZE_WINDOW: case AS_SEND_BEHIND: case AS_SET_SIZE_LIMITS: case AS_ADD_TO_SUBSET:
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
		case AS_VIEW_SET_EVENT_MASK: case AS_VIEW_SET_MOUSE_EVENT_MASK: return;
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
		case AS_STROKE_RECT: case AS_FILL_RECT:
		{
			BRect r; if (link.Read<BRect>(&r) != B_OK) return;
			SetupClip(w, v);
			BRect wr = ToWindow(v, r);
			FRect f{wr.left, wr.top, wr.right, wr.bottom};
			if (code == AS_FILL_RECT) w->canvas.FillRect(f, DrawColor(v->st));
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
			else w->canvas.StrokeRect(f, DrawColor(v->st), v->st.penSize * v->st.scale);	// gap: not rounded
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
			if (code == AS_FILL_ELLIPSE) w->canvas.FillEllipse(FRect{wr.left, wr.top, wr.right, wr.bottom}, DrawColor(v->st));
			return;	// gap: stroked ellipse not drawn
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
			BPoint start = ToWindow(v, i.location);
			BPoint end = DrawText(w, v->st, s.data(), i.stringLength, start, code == AS_DRAW_STRING_WITH_DELTA ? &i.delta : NULL);
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
	}
}

void PostInternal(SWindow* w, int kind, int a, int b)
{
	PortLink l(w->port, -1);
	l.StartMessage(kInternalEvent);
	Internal e{kind, a, b};
	l.Attach(&e, sizeof(e));
	l.Flush();
}

}	// namespace

void WlEvent(void* user, int kind, int a, int b)
{
	PostInternal((SWindow*)user, kind, a, b);
}

namespace {

// ------------------------------------------------------------------------------------------------ apps

void CreateWindow(SApp* app, LinkReceiver& link, PortLink& reply)
{
	BRect frame; uint32 look, feel, flags, workspace; int32 token; port_id replyPort, looperPort; char* title = NULL;
	link.Read<BRect>(&frame); link.Read<uint32>(&look); link.Read<uint32>(&feel); link.Read<uint32>(&flags);
	link.Read<uint32>(&workspace); link.Read<int32>(&token);
	link.Read<port_id>(&replyPort); link.Read<port_id>(&looperPort); link.ReadString(&title);

	SWindow* w = new SWindow;
	w->app = app; w->clientToken = token; w->clientLooperPort = looperPort;
	w->frame = frame; w->title = title ? title : ""; free(title); w->flags = flags;
	w->port = create_port(100, "w<app_server_win");
	w->link.SetTo(replyPort, w->port);
	{
		std::lock_guard<std::recursive_mutex> g(gMu);
		w->index = (int)gWindows.size();
		gWindows.push_back(w);
	}
	w->canvas.Init((int)frame.IntegerWidth() + 1, (int)frame.IntegerHeight() + 1, gScale);
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

double output_scale() { return gScale; }

// Test hook: the same internal event the Wayland pointer callbacks post, so a test can click without a seat.
void inject_pointer(int window, float x, float y, int button)	// button: -1 move only, 0 release, 1 press
{
	SWindow* w;
	{ std::lock_guard<std::recursive_mutex> g(gMu); if (window < 0 || window >= (int)gWindows.size()) return; w = gWindows[window]; }
	PostInternal(w, AU_EV_POINTER_MOVE, (int)(x * 256), (int)(y * 256));
	if (button >= 0) PostInternal(w, AU_EV_POINTER_BUTTON, 1, button);
}

}	// namespace aurora
