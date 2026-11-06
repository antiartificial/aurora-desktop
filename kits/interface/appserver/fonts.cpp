/*
 * FreeType + HarfBuzz implementation of aurora::text::Backend. Written fresh.
 * Font discovery: scans AURORA_FONT_DIR (colon separated) or /usr/share/fonts/truetype, registers every
 * face FreeType can open under its (family, style) names. Plain/bold/fixed come from AURORA_FONT_PLAIN,
 * AURORA_FONT_BOLD, AURORA_FONT_FIXED (file paths) or the first of a short preference list that exists.
 */
#include "text_backend.h"

#include <ft2build.h>
#include FT_FREETYPE_H
#include FT_OUTLINE_H
#include FT_TRUETYPE_TABLES_H
#include <hb.h>
#include <hb-ft.h>

#include <dirent.h>
#include <math.h>
#include <string.h>
#include <sys/stat.h>
#include <stdio.h>
#include <stdlib.h>
#include <algorithm>
#include <strings.h>
#include <mutex>

namespace aurora {
namespace text {
namespace {

// Haiku face bits (Font.h): B_ITALIC_FACE 1, B_BOLD_FACE 0x20, B_REGULAR_FACE 0x40.
const uint16_t kItalic = 0x01, kBold = 0x20, kRegular = 0x40;

struct FaceEntry {
	std::string path, family, style;
	uint16_t haikuFace;
	FT_Face ft;		// opened lazily
	hb_font_t* hb;
	int upem;
};

struct Family {
	std::string name;
	std::vector<int> faces;		// indexes into FtBackend::fFaces; the style id is the position here
};

class FtBackend : public Backend {
public:
	FtBackend() : fLib(NULL)
	{
		FT_Init_FreeType(&fLib);
		const char* dirs = getenv("AURORA_FONT_DIR");
		std::string d = dirs && *dirs ? dirs : "/usr/share/fonts/truetype";
		size_t pos = 0;
		while (pos <= d.size()) {
			size_t e = d.find(':', pos);
			if (e == std::string::npos) e = d.size();
			if (e > pos) Scan(d.substr(pos, e - pos), 1);
			pos = e + 1;
		}
		ResolveSystem();
	}

	int FamilyCount() { return (int)fFamilies.size(); }
	bool FamilyName(int f, std::string* n)
	{
		if (f < 0 || f >= (int)fFamilies.size()) return false;
		*n = fFamilies[f].name; return true;
	}
	int StyleCount(int f) { return f >= 0 && f < (int)fFamilies.size() ? (int)fFamilies[f].faces.size() : 0; }
	bool StyleName(int f, int s, std::string* n, uint16_t* face)
	{
		FaceEntry* e = Entry(FontKey{(uint16_t)f, (uint16_t)s});
		if (!e) return false;
		*n = e->style; if (face) *face = e->haikuFace; return true;
	}
	bool Lookup(const char* family, const char* style, FontKey* key, uint16_t* face)
	{
		for (size_t f = 0; f < fFamilies.size(); f++) {
			if (family && *family && fFamilies[f].name != family) continue;
			for (size_t s = 0; s < fFamilies[f].faces.size(); s++) {
				FaceEntry& e = fFaces[fFamilies[f].faces[s]];
				if (style && *style && e.style != style) continue;
				*key = FontKey{(uint16_t)f, (uint16_t)s};
				if (face) *face = e.haikuFace;
				return true;
			}
		}
		return false;
	}
	const char* FilePath(FontKey k) { FaceEntry* e = Entry(k); return e ? e->path.c_str() : ""; }
	bool SystemFont(const char* which, FontKey* key, float* size)
	{
		if (!strcmp(which, "plain")) { *key = fPlain; *size = 12; }
		else if (!strcmp(which, "bold")) { *key = fBold; *size = 12; }
		else if (!strcmp(which, "fixed")) { *key = fFixed; *size = 12; }
		else return false;
		return true;
	}

	bool GetMetrics(FontKey k, float size, Metrics* m)
	{
		std::lock_guard<std::recursive_mutex> g(fLock);
		FaceEntry* e = Open(k);
		if (!e || size <= 0) return false;
		FT_Face f = e->ft;
		float u = size / (float)e->upem;
		m->ascent = f->ascender * u;
		m->descent = -f->descender * u;
		float line = f->height * u;
		m->leading = line - m->ascent - m->descent;
		if (m->leading < 0) m->leading = 0;
		m->underline_pos = -f->underline_position * u;
		m->underline_thickness = f->underline_thickness * u;
		m->units_per_em = (float)e->upem;
		return true;
	}

	bool Shape(FontKey k, float size, const char* utf8, int len, std::vector<Glyph>* out)
	{
		std::lock_guard<std::recursive_mutex> g(fLock);
		FaceEntry* e = Open(k);
		if (!e || size <= 0 || !utf8 || len <= 0) return false;
		hb_buffer_t* buf = hb_buffer_create();
		hb_buffer_add_utf8(buf, utf8, len, 0, len);
		hb_buffer_guess_segment_properties(buf);
		hb_shape(e->hb, buf, NULL, 0);
		unsigned n;
		hb_glyph_info_t* gi = hb_buffer_get_glyph_infos(buf, &n);
		hb_glyph_position_t* gp = hb_buffer_get_glyph_positions(buf, &n);
		float u = size / (float)e->upem;	// hb font scale is set to upem, so values are font units
		out->clear();
		for (unsigned i = 0; i < n; i++) {
			Glyph gl;
			gl.id = gi[i].codepoint;
			gl.advance = gp[i].x_advance * u;
			gl.x_offset = gp[i].x_offset * u;
			gl.y_offset = gp[i].y_offset * u;
			gl.cluster = gi[i].cluster;
			out->push_back(gl);
		}
		hb_buffer_destroy(buf);
		return true;
	}

	bool Rasterize(FontKey k, float size, float scale, uint32_t glyph, float subx, GlyphBitmap* out)
	{
		std::lock_guard<std::recursive_mutex> g(fLock);
		FaceEntry* e = Open(k);
		if (!e || size <= 0 || scale <= 0) return false;
		FT_Face f = e->ft;
		// 26.6 pixel size; unhinted so the shape is the same outline at every scale.
		if (FT_Set_Char_Size(f, 0, (FT_F26Dot6)lroundf(size * scale * 64.0f), 72, 72)) return false;
		if (FT_Load_Glyph(f, glyph, FT_LOAD_NO_HINTING | FT_LOAD_NO_BITMAP)) return false;
		if (f->glyph->format == FT_GLYPH_FORMAT_OUTLINE)
			FT_Outline_Translate(&f->glyph->outline, (FT_Pos)lroundf(subx * 64.0f), 0);
		if (FT_Render_Glyph(f->glyph, FT_RENDER_MODE_NORMAL)) return false;
		FT_Bitmap& b = f->glyph->bitmap;
		out->width = (int)b.width; out->height = (int)b.rows; out->pitch = (int)b.width;
		out->left = f->glyph->bitmap_left; out->top = f->glyph->bitmap_top;
		out->alpha.assign((size_t)out->width * out->height, 0);
		for (int y = 0; y < out->height; y++)
			memcpy(&out->alpha[(size_t)y * out->width], b.buffer + (size_t)y * b.pitch, out->width);
		return true;
	}

private:
	FaceEntry* Entry(FontKey k)
	{
		if (k.family >= fFamilies.size() || k.style >= fFamilies[k.family].faces.size()) return NULL;
		return &fFaces[fFamilies[k.family].faces[k.style]];
	}
	FaceEntry* Open(FontKey k)
	{
		FaceEntry* e = Entry(k);
		if (!e) return NULL;
		if (!e->ft) {
			if (FT_New_Face(fLib, e->path.c_str(), 0, &e->ft)) { e->ft = NULL; return NULL; }
			e->upem = e->ft->units_per_EM ? e->ft->units_per_EM : 1000;
			// Shape from the font file itself (OpenType funcs), NOT hb_ft_font_create: that one reads advances
			// through the FT_Face at whatever size Rasterize last set, so a string's width depended on which
			// glyphs had been drawn before it. Here advances are pure font units, so metrics are stable.
			hb_blob_t* blob = hb_blob_create_from_file_or_fail(e->path.c_str());
			hb_face_t* face = blob ? hb_face_create(blob, 0) : hb_face_get_empty();
			if (blob) hb_blob_destroy(blob);
			hb_face_set_upem(face, e->upem);
			e->hb = hb_font_create(face);
			hb_face_destroy(face);
			hb_font_set_scale(e->hb, e->upem, e->upem);
			hb_font_set_ptem(e->hb, 0);
		}
		return e;
	}

	void Scan(const std::string& dir, int depth)
	{
		DIR* d = opendir(dir.c_str());
		if (!d) return;
		std::vector<std::string> files;
		while (dirent* de = readdir(d)) {
			if (de->d_name[0] == '.') continue;
			std::string p = dir + "/" + de->d_name;
			struct stat st;
			if (stat(p.c_str(), &st)) continue;
			if (S_ISDIR(st.st_mode)) { if (depth > 0) Scan(p, depth - 1); continue; }
			size_t n = p.size();
			if (n > 4 && (!strcasecmp(p.c_str() + n - 4, ".ttf") || !strcasecmp(p.c_str() + n - 4, ".otf")))
				files.push_back(p);
		}
		closedir(d);
		std::sort(files.begin(), files.end());
		for (size_t i = 0; i < files.size(); i++) Register(files[i]);
	}

	void Register(const std::string& path)
	{
		FT_Face f;
		if (FT_New_Face(fLib, path.c_str(), 0, &f)) return;
		if (!f->family_name || !FT_IS_SCALABLE(f)) { FT_Done_Face(f); return; }
		FaceEntry e;
		e.path = path; e.family = f->family_name; e.style = f->style_name ? f->style_name : "Regular";
		e.haikuFace = 0;
		if (f->style_flags & FT_STYLE_FLAG_ITALIC) e.haikuFace |= kItalic;
		if (f->style_flags & FT_STYLE_FLAG_BOLD) e.haikuFace |= kBold;
		if (!(e.haikuFace & (kItalic | kBold))) e.haikuFace = kRegular;
		e.ft = NULL; e.hb = NULL; e.upem = 0;
		FT_Done_Face(f);
		// Skip variable-width cuts ("SemiCondensed Bold"): keep one entry per (family, style) name.
		size_t fam = 0;
		for (; fam < fFamilies.size(); fam++) if (fFamilies[fam].name == e.family) break;
		if (fam == fFamilies.size()) { Family nf; nf.name = e.family; fFamilies.push_back(nf); }
		for (size_t s = 0; s < fFamilies[fam].faces.size(); s++)
			if (fFaces[fFamilies[fam].faces[s]].style == e.style) return;
		fFaces.push_back(e);
		fFamilies[fam].faces.push_back((int)fFaces.size() - 1);
	}

	bool FindFile(const char* path, FontKey* key)
	{
		if (!path || !*path) return false;
		for (size_t f = 0; f < fFamilies.size(); f++)
			for (size_t s = 0; s < fFamilies[f].faces.size(); s++)
				if (fFaces[fFamilies[f].faces[s]].path == path) { *key = FontKey{(uint16_t)f, (uint16_t)s}; return true; }
		return false;
	}

	void ResolveSystem()
	{
		static const char* plain[] = { "/usr/share/fonts/truetype/NotoSans-Regular.ttf",
			"/usr/share/fonts/truetype/DejaVuSans.ttf", "/usr/share/fonts/truetype/LiberationSans-Regular.ttf", 0 };
		static const char* bold[] = { "/usr/share/fonts/truetype/NotoSans-Bold.ttf",
			"/usr/share/fonts/truetype/DejaVuSans-Bold.ttf", "/usr/share/fonts/truetype/LiberationSans-Bold.ttf", 0 };
		static const char* fixed[] = { "/usr/share/fonts/truetype/DejaVuSansMono.ttf",
			"/usr/share/fonts/truetype/LiberationMono-Regular.ttf", 0 };
		Pick("AURORA_FONT_PLAIN", plain, &fPlain, FontKey{0, 0});
		Pick("AURORA_FONT_BOLD", bold, &fBold, fPlain);
		Pick("AURORA_FONT_FIXED", fixed, &fFixed, fPlain);
	}
	void Pick(const char* env, const char** prefs, FontKey* out, FontKey fallback)
	{
		*out = fallback;
		if (FindFile(getenv(env), out)) return;
		for (int i = 0; prefs[i]; i++) if (FindFile(prefs[i], out)) return;
	}

	FT_Library fLib;
	std::vector<FaceEntry> fFaces;
	std::vector<Family> fFamilies;
	FontKey fPlain{0, 0}, fBold{0, 0}, fFixed{0, 0};
	std::recursive_mutex fLock;
};

}	// namespace

Backend* DefaultBackend()
{
	static FtBackend* b = new FtBackend;
	return b;
}

}	// namespace text
}	// namespace aurora
