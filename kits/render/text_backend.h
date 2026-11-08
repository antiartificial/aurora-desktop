/*
 * THE TEXT SEAM. This is the whole interface between the app_server analogue and "something that knows about
 * fonts". The FreeType+HarfBuzz backend (fonts.cpp) is one implementation. It is deliberately small and
 * made of plain data so that a different backend (the compositor's cosmic-text stack, behind a C ABI or
 * across a socket) can replace it without touching the protocol code or Haiku's Font.cpp.
 *
 * Units. Everything a layout caller sees is in LAYOUT UNITS (Haiku's "pixels" at scale 1; what BView
 * coordinates, BFont::Size() and StringWidth() are measured in). Metrics and shaping are
 * SCALE-INDEPENDENT: no hinting, advances are linear (font units * size / unitsPerEm), so
 * StringWidth() at output scale 1.0 and 2.0 is bit-identical. Only Rasterize() takes a scale; it returns
 * DEVICE pixels. A caller positions glyphs in layout units, multiplies by scale, and blits.
 *
 * Written fresh for Aurora.
 */
#ifndef AURORA_TEXT_BACKEND_H
#define AURORA_TEXT_BACKEND_H

#include <stdint.h>
#include <string>
#include <vector>

namespace aurora {
namespace text {

struct FontKey {		// Haiku's own pair of ids: BFont::fFamilyID / fStyleID
	uint16_t family;
	uint16_t style;
};

struct Metrics {		// layout units, for the given size
	float ascent;		// positive, above the baseline
	float descent;		// positive, below the baseline
	float leading;		// extra line gap (hhea lineGap)
	float underline_pos, underline_thickness;
	float units_per_em;	// 0 if unknown
};

struct Glyph {
	uint32_t id;		// glyph index in the face, NOT a codepoint
	float advance;		// layout units
	float x_offset, y_offset;	// shaper offsets, layout units
	uint32_t cluster;	// byte offset into the utf8 input
};

struct GlyphBitmap {		// 8-bit coverage, row-major, device pixels
	int width, height, pitch;
	int left, top;		// bitmap origin relative to the pen point, device px (top is up-positive)
	std::vector<uint8_t> alpha;
};

class Backend {
public:
	virtual ~Backend() {}
	// Catalogue. Ids are dense and stable for the life of the process.
	virtual int FamilyCount() = 0;
	virtual bool FamilyName(int family, std::string* name) = 0;
	virtual int StyleCount(int family) = 0;
	virtual bool StyleName(int family, int style, std::string* name, uint16_t* haikuFace) = 0;
	virtual bool Lookup(const char* family, const char* style, FontKey* key, uint16_t* face) = 0;
	virtual const char* FilePath(FontKey key) = 0;	// for diagnostics and tests
	virtual bool SystemFont(const char* which, FontKey* key, float* size) = 0;	// plain|bold|fixed

	virtual bool GetMetrics(FontKey key, float size, Metrics* out) = 0;
	virtual bool Shape(FontKey key, float size, const char* utf8, int len, std::vector<Glyph>* out) = 0;
	virtual bool Rasterize(FontKey key, float size, float scale, uint32_t glyph, float subpixelX,
		GlyphBitmap* out) = 0;
};

Backend* DefaultBackend();	// process-wide FreeType+HarfBuzz instance, created on first use

}	// namespace text
}	// namespace aurora

#endif
