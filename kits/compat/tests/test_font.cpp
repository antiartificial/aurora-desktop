// BFont against the real font file. Real Haiku Font.cpp -> link -> aurora app_server analogue -> FreeType/HarfBuzz.
// The oracle is FreeType read DIRECTLY in this file (no app_server code), so a stub returning plausible
// numbers cannot pass: every expected value is recomputed from the .ttf named at the top.
#include <Application.h>
#include <Font.h>
#include <InterfaceDefs.h>
#include <stdio.h>
#include <string.h>
#include <math.h>
#include <string>
#include <vector>
#include <ft2build.h>
#include FT_FREETYPE_H
#include "server.h"
#include "text_backend.h"

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)
static bool near(double a, double b, double tol) { return fabs(a - b) <= tol; }

int main()
{
	setenv("AURORA_APPSERVER_PRESENT", "0", 1);	// no compositor needed: this is a metrics test
	BApplication app("application/x-vnd.aurora-test-font");
	const BFont* f = be_plain_font;

	// Which file is this? Ask the backend (diagnostic only), then open that file ourselves.
	uint32 id = f->FamilyAndStyle();
	aurora::text::FontKey key{(uint16_t)(id >> 16), (uint16_t)(id & 0xffff)};
	const char* path = aurora::text::DefaultBackend()->FilePath(key);
	printf("be_plain_font -> %s  size %.1f\n", path, f->Size());
	CHECK(path && *path, "no font file behind be_plain_font");

	font_family fam; font_style sty;
	f->GetFamilyAndStyle(&fam, &sty);
	printf("family '%s' style '%s'\n", fam, sty);
	CHECK(strlen(fam) > 0 && strlen(sty) > 0, "family/style empty");

	FT_Library lib; FT_Face face;
	if (FT_Init_FreeType(&lib) || FT_New_Face(lib, path, 0, &face)) { printf("cannot open %s\n", path); return 2; }
	double upem = face->units_per_EM;

	// ---- GetHeight vs the file's own hhea table
	font_height h; f->GetHeight(&h);
	double size = f->Size();
	double wantA = face->ascender * size / upem, wantD = -face->descender * size / upem;
	double wantL = face->height * size / upem - wantA - wantD; if (wantL < 0) wantL = 0;
	printf("GetHeight: ascent %.4f descent %.4f leading %.4f   (file: %.4f %.4f %.4f)\n", h.ascent, h.descent, h.leading, wantA, wantD, wantL);
	CHECK(near(h.ascent, wantA, 1e-3), "ascent %.4f != %.4f", h.ascent, wantA);
	CHECK(near(h.descent, wantD, 1e-3), "descent %.4f != %.4f", h.descent, wantD);
	CHECK(near(h.leading, wantL, 1e-3), "leading %.4f != %.4f", h.leading, wantL);
	CHECK(h.ascent > 5 && h.ascent < 20 && h.descent > 0, "implausible height at size 12");

	// ---- StringWidth: digits are tabular (no kerning); compare exactly with the sum of file advances
	auto fileAdvance = [&](unsigned cp, double sz) {
		FT_Load_Char(face, cp, FT_LOAD_NO_SCALE | FT_LOAD_NO_HINTING);
		return face->glyph->metrics.horiAdvance * sz / upem; };
	double w0000 = f->StringWidth("0000"), want0000 = 4 * fileAdvance('0', size);
	printf("StringWidth(\"0000\") = %.4f  (file advances: %.4f)\n", w0000, want0000);
	CHECK(near(w0000, want0000, 1e-3), "digits width %.4f != %.4f", w0000, want0000);
	double wHello = f->StringWidth("Hello");
	double wantHello = 0; for (const char* p = "Hello"; *p; p++) wantHello += fileAdvance(*p, size);
	printf("StringWidth(\"Hello\") = %.4f  (unkerned file advances: %.4f, diff %.4f = GPOS kerning)\n", wHello, wantHello, wHello - wantHello);
	CHECK(near(wHello, wantHello, wantHello * 0.04), "Hello width %.4f too far from %.4f", wHello, wantHello);
	CHECK(f->StringWidth("W") > f->StringWidth("i"), "W not wider than i: proportional font expected");
	CHECK(f->StringWidth("") == 0, "empty string width");

	// ---- GetEscapements: fraction of the em; times size must equal the advance
	float esc[5];
	f->GetEscapements("Hello", 5, esc);
	double sum = 0; for (int i = 0; i < 5; i++) sum += esc[i] * size;
	printf("GetEscapements(Hello) * size: %.3f %.3f %.3f %.3f %.3f  sum %.4f\n", esc[0]*size, esc[1]*size, esc[2]*size, esc[3]*size, esc[4]*size, sum);
	for (int i = 0; i < 5; i++) CHECK(near(esc[i] * size, fileAdvance("Hello"[i], size), 1e-3), "escapement[%d] %.4f != %.4f", i, esc[i] * size, fileAdvance("Hello"[i], size));

	// ---- size is linear: no hinting, so 24 is exactly twice 12
	BFont big(*f); big.SetSize(24);
	font_height h2; big.GetHeight(&h2);
	CHECK(near(h2.ascent, 2 * h.ascent, 2e-3), "ascent at 24 (%.4f) != 2x ascent at 12 (%.4f)", h2.ascent, 2 * h.ascent);
	CHECK(near(big.StringWidth("0000"), 2 * w0000, 2e-3), "width not linear in size");

	// ---- a different face really is a different file
	const BFont* b = be_bold_font;
	uint32 bid = b->FamilyAndStyle();
	const char* bpath = aurora::text::DefaultBackend()->FilePath({(uint16_t)(bid >> 16), (uint16_t)(bid & 0xffff)});
	printf("be_bold_font  -> %s\n", bpath);
	CHECK(strcmp(path, bpath) != 0, "bold and plain are the same file");
	CHECK(b->StringWidth("Hello") > wHello, "bold Hello not wider than regular");
	const BFont* x = be_fixed_font;
	CHECK(near(x->StringWidth("i"), x->StringWidth("W"), 1e-3), "fixed font: i and W differ in width");

	// ---- UTF-8: two-byte sequence is one character
	float e2[2]; f->GetEscapements("\xc3\xa9x", 2, e2);	// "ex" with accent: 2 chars, 3 bytes
	CHECK(e2[0] > 0 && e2[1] > 0, "utf-8 escapements");
	CHECK(f->StringWidth("\xc3\xa9") > 0, "e-acute has no width");

	// ---- CJK: the plain face has no Han glyphs, so every character used to come back as its .notdef box
	// and two different Chinese file names drew identically. Measured as pixels, through the backend:
	// each string is shaped and every glyph rasterised, and the result is compared with other strings.
	{
		aurora::text::Backend* be = aurora::text::DefaultBackend();
		auto ink = [&](const char* s, double* adv) {
			std::vector<aurora::text::Glyph> gl;
			be->Shape(key, 24, s, (int)strlen(s), &gl);
			std::string px; *adv = 0;
			for (auto& g : gl) {
				aurora::text::GlyphBitmap bm;
				*adv += g.advance;
				if (be->Rasterize(key, 24, 1, g.id, 0, &bm)) {
					px.append((const char*)&bm.width, sizeof bm.width);
					px.append((const char*)bm.alpha.data(), bm.alpha.size());
				}
			}
			return px;
		};
		double a1, a2, a3, a4;
		std::string zhA = ink("\xe6\x96\x87\xe4\xbb\xb6\xe5\xa4\xb9", &a1);	// 文件夹
		std::string zhB = ink("\xe9\x9f\xb3\xe4\xb9\x90\xe5\xba\x93", &a2);	// 音乐库
		std::string tofu = ink("\xcd\xb8\xcd\xb8\xcd\xb8", &a3);		// U+0378 x3: in no font
		std::string kana = ink("\xe3\x83\x89\xe3\x82\xad", &a4);		// ドキ
		printf("CJK ink bytes: zhA %zu zhB %zu tofu %zu kana %zu; advances %.1f %.1f %.1f %.1f\n", zhA.size(), zhB.size(), tofu.size(), kana.size(), a1, a2, a3, a4);
		CHECK(zhA != zhB, "two different Chinese names rasterise identically (tofu)");
		CHECK(zhA != tofu && zhB != tofu, "a Chinese name rasterises as the missing-glyph box");
		CHECK(kana != tofu && kana != zhA, "kana rasterises as the missing-glyph box");
		CHECK(a1 > 3 * 12 && near(a1, 3 * 24, 3.0), "three Han characters should advance about 3 em, got %.2f", a1);
	}

	printf("scale reported by the server: %.3f\n", aurora::output_scale());
	// Machine-readable line for the scale-invariance check across processes.
	printf("INVARIANT size=%.3f w0000=%.6f wHello=%.6f ascent=%.6f descent=%.6f\n", size, w0000, wHello, h.ascent, h.descent);
	int unh, unhr; unh = aurora::unhandled_opcodes(&unhr);
	printf("unhandled opcodes: %d (with reply: %d)\n", unh, unhr);
	printf("%d checks, %d failed\n", checks, fails);
	return fails ? 1 : 0;
}
