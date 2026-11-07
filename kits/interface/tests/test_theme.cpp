// The Interface Kit's colours come from contracts/theme/aurora-glass.toml, by one documented rule, and the control look
// is the one the theme (or AURORA_CONTROL_LOOK) asks for. Everything here is stated as a RULE over whatever the theme
// says, not as a pixel value, so a palette change cannot break it unless it breaks the rule:
//   * every colour_which the theme maps answers ui_color() with the token converted by the Oklab-to-sRGB rule;
//   * a colour_which it does not map answers Haiku's own default;
//   * a window painted with the panel colour has exactly those bytes in its frame;
//   * the contrast floors the theme file promises hold for its own tokens (WCAG 2: 4.5:1 text, 3:1 non-text);
//   * the parser overlays the file on the built-in copy, ignores what it cannot read, and does not mistake a '#' in a
//     string for a comment.
// Run with the argument "custom" it first points AURORA_THEME_DIR at a different palette (a dark one), written on the
// spot, and asserts the same rules: that is the proof that nothing here hard-codes the shipped palette.
// The cross-check against the compositor's own arithmetic is demos/theme-drift/run.sh.
#include <Application.h>
#include <Window.h>
#include <View.h>
#include <ControlLook.h>
#include <HaikuControlLook.h>
#include <DefaultColors.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <unistd.h>
#include <string>
#include <fstream>
#include "server.h"
#include "aurora_theme.h"
#include "aurora_controllook.h"

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)

static bool same(rgb_color a, rgb_color b) { return a.red == b.red && a.green == b.green && a.blue == b.blue && a.alpha == b.alpha; }

// WCAG 2 relative luminance and contrast of two 8-bit sRGB colours.
static double luminance(rgb_color c)
{
	auto lin = [](uint8 v) { double s = v / 255.0; return s <= 0.04045 ? s / 12.92 : pow((s + 0.055) / 1.055, 2.4); };
	return 0.2126 * lin(c.red) + 0.7152 * lin(c.green) + 0.0722 * lin(c.blue);
}
static double contrast(rgb_color a, rgb_color b)
{
	double la = luminance(a), lb = luminance(b);
	if (la < lb) { double t = la; la = lb; lb = t; }
	return (la + 0.05) / (lb + 0.05);
}

static rgb_color token(const char* name)
{
	rgb_color c = make_color(1, 2, 3);
	std::string key = std::string("theme.colors.") + name;
	CHECK(aurora::Theme::Current().Color(key.c_str(), &c), "theme has no colour token %s", key.c_str());
	return c;
}

struct W : BWindow {
	W() : BWindow(BRect(10, 10, 109, 109), "t", B_TITLED_WINDOW, 0)
	{
		BView* bg = new BView(Bounds(), "bg", B_FOLLOW_ALL, B_WILL_DRAW);
		bg->SetViewUIColor(B_PANEL_BACKGROUND_COLOR);
		AddChild(bg);
	}
};

int main(int argc, char** argv)
{
	if (argc > 1 && strcmp(argv[1], "--dump") == 0) {
		// For demos/theme-drift/run.sh: what this toolkit makes of the theme, in the oracle's format. "tok" lines are the
		// reader's own conversion of every [theme.colors] token and background.top; "ui" lines are what ui_color() answers
		// for each colour_which the theme maps, through the real colour table.
		setenv("AURORA_APPSERVER_PRESENT", "0", 1);
		BApplication dump("application/x-vnd.aurora-theme-dump");
		const aurora::Theme& th = aurora::Theme::Current();
		std::vector<std::string> keys = th.Keys("theme.colors.");
		keys.push_back("background.top");
		for (const std::string& k : keys) { rgb_color c; if (th.Color(k.c_str(), &c)) printf("tok %s %d %d %d\n", k.c_str(), c.red, c.green, c.blue); }
		int cnt = 0; const aurora::ColorMapping* mm = aurora::ColorMappings(&cnt);
		for (int i = 0; i < cnt; i++) { rgb_color c = ui_color(mm[i].which); printf("ui %s %d %d %d\n", mm[i].token, c.red, c.green, c.blue); }
		return 0;
	}
	bool custom = argc > 1 && strcmp(argv[1], "custom") == 0;
	char dir[] = "/tmp/aurora-theme-test.XXXXXX";
	if (custom) {
		if (mkdtemp(dir) == NULL) { printf("mkdtemp failed\n"); return 2; }
		std::ofstream(std::string(dir) + "/aurora-glass.toml")
			<< "[theme.colors]\npanel = [0.28, 0.01, -0.03]\npanel_text = [0.95, 0.0, 0.0]\ndocument = [0.20, 0.0, 0.0]\n"
			   "document_text = [0.96, 0.0, 0.0]\ncontrol = [0.34, 0.01, -0.03]\ncontrol_border = [0.62, 0.0, -0.03]\n"
			   "accent = [0.78, 0.05, -0.12]\nscroll_thumb = [0.62, 0.0, -0.03]\nlink = [0.80, 0.04, -0.12]\nmenu = [0.30, 0.01, -0.03]\n"
			   "menu_selected = [0.42, 0.03, -0.08]\nlist_selected = [0.40, 0.03, -0.08]\n";
		setenv("AURORA_THEME_DIR", dir, 1);
	}
	setenv("AURORA_APPSERVER_PRESENT", "0", 1);
	BApplication app("application/x-vnd.aurora-test-theme");

	printf("theme file in force: %s\n", aurora::Theme::FilePath().empty() ? "(built-in copy only)" : aurora::Theme::FilePath().c_str());

	// -- the rule: Oklab -> sRGB bytes. A few vectors that do not depend on any palette.
	{
		float white[3] = { 1, 0, 0 }, black[3] = { 0, 0, 0 }, grey[3] = { 0.6f, 0, 0 };
		CHECK(same(aurora::OklabToRgb(white), make_color(255, 255, 255, 255)), "white");
		CHECK(same(aurora::OklabToRgb(black), make_color(0, 0, 0, 255)), "black");
		rgb_color g = aurora::OklabToRgb(grey);	// Oklab L 0.6 is linear 0.216, which encodes to code 128 (theme.rs: linear_and_encoded_tokens_agree)
		CHECK(g.red == 128 && g.green == 128 && g.blue == 128, "mid grey is %d %d %d", g.red, g.green, g.blue);
		float wild[3] = { 0.9f, 0.5f, 0.5f };	// out of gamut: clamped per channel in linear light, not wrapped
		rgb_color w = aurora::OklabToRgb(wild);
		CHECK(w.red == 255, "an out-of-gamut token clamps (red %d)", w.red);
	}

	bool themed = aurora::UseAuroraColors();
	if (!themed) {		// the stock-colours switch: every colour is Haiku's own
		int cnt = 0; const aurora::ColorMapping* mm = aurora::ColorMappings(&cnt);
		for (int i = 0; i < cnt; i++)
			CHECK(same(ui_color(mm[i].which), BPrivate::GetSystemColor(mm[i].which, false)), "colour %d is not Haiku's default with AURORA_COLORS=haiku", (int)mm[i].which);
		printf("AURORA_COLORS=haiku: %d colours checked against Haiku's defaults\n", cnt);
		printf("%d checks, %d failed\n", checks, fails);
		return fails ? 1 : 0;
	}
	// -- every mapped colour_which answers with its token's bytes
	int n = 0; const aurora::ColorMapping* m = aurora::ColorMappings(&n);
	int mapped = 0;
	for (int i = 0; i < n; i++) {
		rgb_color want;
		if (!aurora::Theme::Current().Color(m[i].token, &want)) { CHECK(false, "mapping %d (%s) has no token", (int)m[i].which, m[i].token); continue; }
		rgb_color got = ui_color(m[i].which);
		CHECK(same(got, want), "%s: ui_color(%d) = %d,%d,%d, token = %d,%d,%d", m[i].token, (int)m[i].which, got.red, got.green, got.blue, want.red, want.green, want.blue);
		mapped++;
	}
	printf("%d color_which constants mapped to theme tokens\n", mapped);
	CHECK(mapped == n && n >= 30, "mappings: %d of %d", mapped, n);
	// unmapped ones are Haiku's
	color_which unmapped[] = { B_WINDOW_TAB_COLOR, B_WINDOW_BORDER_COLOR, B_WINDOW_INACTIVE_TAB_COLOR };
	for (color_which w : unmapped) {
		bool isMapped = false; for (int i = 0; i < n; i++) isMapped |= m[i].which == w;
		CHECK(!isMapped && same(ui_color(w), BPrivate::GetSystemColor(w, false)), "colour %d should be Haiku's default", (int)w);
	}

	// -- a window painted with the panel colour: its pixels are the token's bytes
	W* win = new W; win->Show();
	aurora::Frame f; double s = aurora::output_scale();
	bool got = false;
	for (int i = 0; i < 300 && !got; i++) { got = aurora::last_frame(0, &f) && f.argb.size() > 0; if (!got) snooze(10000); }
	CHECK(got, "no frame");
	if (got) {
		uint32_t px = f.argb[(size_t)(int)(50 * s) * f.width + (int)(50 * s)];
		rgb_color p = token("panel");
		CHECK((px & 0xffffff) == (uint32_t)((p.red << 16) | (p.green << 8) | p.blue), "panel pixel is %06x, the panel token is %02x%02x%02x", px & 0xffffff, p.red, p.green, p.blue);
		printf("panel background in the frame: %06x (token panel -> %d,%d,%d)\n", px & 0xffffff, p.red, p.green, p.blue);
	}

	// -- the contrast floors the theme file promises, over whatever palette is in force
	struct { const char* fg; const char* bg; double floor; } pairs[] = {
		{ "panel_text", "panel", 4.5 }, { "panel_text", "control", 4.5 }, { "panel_text", "menu", 4.5 }, { "panel_text", "menu_selected", 4.5 },
		{ "document_text", "document", 4.5 }, { "document_text", "list_selected", 4.5 }, { "link", "document", 4.5 },
		{ "accent", "panel", 3.0 }, { "accent", "document", 3.0 }, { "control_border", "panel", 3.0 }, { "scroll_thumb", "panel", 3.0 },
	};
	for (auto& pr : pairs) {
		double c = contrast(token(pr.fg), token(pr.bg));
		CHECK(c >= pr.floor, "%s on %s is %.2f:1, floor %.1f", pr.fg, pr.bg, c, pr.floor);
	}
	printf("%d contrast floors checked\n", (int)(sizeof pairs / sizeof pairs[0]));

	// -- the parser
	{
		aurora::Theme t;
		t.Parse("# comment\n[a]\nx = [0.1, -0.2, 0.3]   # trailing\ns = \"a # b\"\nn = 2.5\n[a.b]\ny = [1, 2, 3]\nbad = [1, 2]\ninline = { oklab = [0.5, 0, 0], k = 1 }\n[[arr]]\nz = [9, 9, 9]\n");
		float v[3]; std::string str; double num = 0;
		CHECK(t.Oklab("a.x", v) && v[0] == 0.1f && v[1] == -0.2f && v[2] == 0.3f, "array with a trailing comment");
		CHECK(t.String("a.s", &str) && str == "a # b", "a # inside a string is not a comment ('%s')", str.c_str());
		CHECK(t.Number("a.n", &num) && num == 2.5, "number");
		CHECK(t.Oklab("a.b.y", v) && v[2] == 3.0f, "dotted table");
		CHECK(!t.Oklab("a.b.bad", v), "two numbers are not a colour");
		CHECK(!t.Oklab("a.b.inline", v), "an inline table is not a colour token");
		CHECK(!t.Oklab("arr.z", v), "array-of-tables keys are skipped");
		t.Parse("[a]\nx = [0.7, 0.0, 0.0]\n");
		CHECK(t.Oklab("a.x", v) && v[0] == 0.7f, "a later file overlays an earlier one");
		t.Parse("[a]\nx = [nonsense]\n");
		CHECK(t.Oklab("a.x", v) && v[0] == 0.7f, "an unreadable value keeps the previous one");
	}

	// -- the control look is the one that was asked for
	bool wantAurora = aurora::UseAuroraControlLook();
	bool isAurora = dynamic_cast<aurora::AuroraControlLook*>(be_control_look) != NULL;
	bool isHaiku = dynamic_cast<BPrivate::HaikuControlLook*>(be_control_look) != NULL;
	printf("control look: %s (theme/env ask for %s)\n", isAurora ? "AuroraControlLook" : "HaikuControlLook", wantAurora ? "aurora" : "haiku");
	CHECK(isHaiku && isAurora == wantAurora, "installed look does not match what was asked for");
	const char* env = getenv("AURORA_CONTROL_LOOK");
	if (env != NULL && *env != '\0')
		CHECK(isAurora == (strcmp(env, "haiku") != 0), "AURORA_CONTROL_LOOK=%s ignored", env);

	// Layout must not depend on the look: the frame and background insets are Haiku's.
	{
		BPrivate::HaikuControlLook stock;
		for (uint32 flags : { 0u, (uint32)BControlLook::B_DEFAULT_BUTTON, (uint32)BControlLook::B_FOCUSED }) {
			float a[4], b[4];
			stock.GetFrameInsets(BControlLook::B_BUTTON_FRAME, flags, a[0], a[1], a[2], a[3]);
			be_control_look->GetFrameInsets(BControlLook::B_BUTTON_FRAME, flags, b[0], b[1], b[2], b[3]);
			CHECK(memcmp(a, b, sizeof a) == 0, "button frame insets differ from Haiku's (flags %x)", flags);
			stock.GetBackgroundInsets(BControlLook::B_BUTTON_BACKGROUND, flags, a[0], a[1], a[2], a[3]);
			be_control_look->GetBackgroundInsets(BControlLook::B_BUTTON_BACKGROUND, flags, b[0], b[1], b[2], b[3]);
			CHECK(memcmp(a, b, sizeof a) == 0, "button background insets differ from Haiku's (flags %x)", flags);
		}
		CHECK(be_control_look->DefaultLabelSpacing() == stock.DefaultLabelSpacing() && be_control_look->DefaultItemSpacing() == stock.DefaultItemSpacing(), "spacings differ from Haiku's");
	}

	int unhr, unh = aurora::unhandled_opcodes(&unhr);
	CHECK(unh == 0, "server saw opcodes it does not implement (%d)", unh);
	printf("%d checks, %d failed\n", checks, fails);
	win->Lock(); win->Quit();
	if (custom) { unlink((std::string(dir) + "/aurora-glass.toml").c_str()); rmdir(dir); }
	return fails ? 1 : 0;
}
