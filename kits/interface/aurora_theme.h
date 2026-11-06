// The Interface Kit's reader for contracts/theme/aurora-glass.toml: the system colour table behind ui_color(),
// and the settings AuroraControlLook reads. Ours, written fresh; no Haiku source is copied here.
//
// Two readers of one file is the risk, so this one is small and says what it agrees with:
//   * token names and meaning are the file's (see the comments in [theme.colors]);
//   * an Oklab token becomes sRGB bytes by the compositor's own rule (compositor/src/fx/theme.rs, oklab_to_u8),
//     in 32-bit float, evaluated in the same order, so the same token gives the same bytes in both.
//     demos/theme-drift/run.sh and tests/test_theme.cpp pin that.
#ifndef AURORA_THEME_H
#define AURORA_THEME_H

#include <GraphicsDefs.h>
#include <InterfaceDefs.h>

#include <map>
#include <string>
#include <vector>

namespace aurora {

// Oklab [L, a, b] to the 8-bit sRGB of an rgb_color (alpha 255). theme.rs: oklab_to_u8.
rgb_color OklabToRgb(const float lab[3]);

// The parsed theme: the compiled-in copy of the file, overlaid by the file found on disk (if any). A key the
// file lacks or spells wrongly keeps the compiled-in value, as in the compositor ("missing keys take the defaults").
class Theme {
public:
	// text is a whole file; later keys replace earlier ones. Lines that are not "key = value" or "[table]" are skipped.
	void Parse(const char* text);
	bool Oklab(const char* key, float out[3]) const;			// key is "table.key", e.g. "theme.colors.panel"
	bool Number(const char* key, double* out) const;
	bool String(const char* key, std::string* out) const;
	// Every key that starts with prefix (e.g. "theme.colors."), sorted.
	std::vector<std::string> Keys(const char* prefix) const;
	// rgb of a colour token; false when the token is absent.
	bool Color(const char* key, rgb_color* out) const;

	// Built-in theme, then $AURORA_THEME_DIR/aurora-glass.toml, then the repo's contracts/theme/ when this binary
	// runs from its build tree (the compositor's default_theme_dir() does the same).
	static const Theme& Current();
	static std::string FilePath();	// the file that was read, or "" when only the built-in copy is in force

private:
	// Every value a key was given, oldest first: a lookup uses the newest one it can read.
	std::map<std::string, std::vector<std::string> > fValues;
};

// A malloc'd server_read_only_memory (ServerReadOnlyMemory.h) whose colour table is the theme's, for
// BApplication::fServerReadOnlyMemory. Colours the theme does not map are B_TRANSPARENT_COLOR, which
// ui_color() reads as "use Haiku's default".
void* NewSystemColorTable();

// The color_which constants the theme maps, and the token each reads: for tests and documentation.
struct ColorMapping { color_which which; const char* token; };
const ColorMapping* ColorMappings(int* count);

// Whether the system colour table is the theme's: true unless the theme ([theme.controls] colors = "haiku") or
// $AURORA_COLORS=haiku says to leave ui_color() on Haiku's own defaults.
bool UseAuroraColors();
// Which control look to install: "aurora" unless the theme or $AURORA_CONTROL_LOOK says "haiku".
bool UseAuroraControlLook();
// [theme.controls] corner_ratio.
float ControlCornerRatio();

}	// namespace aurora

#endif
