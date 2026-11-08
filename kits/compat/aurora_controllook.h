// AuroraControlLook: Aurora's widget geometry and affordances over Haiku's real HaikuControlLook.
//
// A subclass, not a fork. HaikuControlLook is compiled in place from HAIKU_SRC and everything this class does not
// override is HaikuControlLook's. What it changes (docs/visual-system.md and decision 0010 ask for a quiet, legible
// desktop, three nesting levels of radius, and a 3:1 contrast floor for non-text parts):
//   * corners: button, menu-field frames and backgrounds with no radius of their own get one, from
//     [theme.controls] corner_ratio (the dock tile's 0.28 rule) of a typical control height;
//   * the focus ring: two pixels in the theme's accent (B_KEYBOARD_NAVIGATION_COLOR), where Haiku draws one;
//   * the scroll bar: a flat track, a rounded thumb, flat arrow buttons, in place of Haiku's bevelled ones.
// Frame and background insets are Haiku's (GetFrameInsets is not overridden), so layout, and every size a BLayout
// computes, are identical to the stock look: only pixels inside the same boxes change.
#ifndef AURORA_CONTROL_LOOK_H
#define AURORA_CONTROL_LOOK_H

#include <HaikuControlLook.h>

namespace aurora {

class AuroraControlLook : public BPrivate::HaikuControlLook {
public:
	AuroraControlLook();
	virtual ~AuroraControlLook();

	// The radius used where the caller gave none: ratio * 2 * the plain font's size, clamped to 2..8 px.
	// (A control is about two font sizes tall; the ratio is a fraction of its shorter side.)
	float Radius() const;

	using BPrivate::HaikuControlLook::DrawButtonFrame;
	using BPrivate::HaikuControlLook::DrawButtonBackground;
	using BPrivate::HaikuControlLook::DrawGroupFrame;

	virtual void DrawButtonFrame(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
		const rgb_color& background, uint32 flags = 0, uint32 borders = B_ALL_BORDERS);
	virtual void DrawButtonBackground(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
		uint32 flags = 0, uint32 borders = B_ALL_BORDERS, orientation orientation = B_HORIZONTAL);
	virtual void DrawGroupFrame(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
		uint32 borders = B_ALL_BORDERS);

	virtual void DrawTextControlBorder(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
		uint32 flags = 0, uint32 borders = B_ALL_BORDERS);

	virtual void DrawScrollBarButton(BView* view, BRect rect, const BRect& updateRect, const rgb_color& base,
		const rgb_color& text, uint32 flags, int32 direction, orientation orientation, bool down = false);
	using BPrivate::HaikuControlLook::DrawScrollBarBackground;
	virtual void DrawScrollBarBackground(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
		uint32 flags, orientation orientation);
	virtual void DrawScrollBarThumb(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
		uint32 flags, orientation orientation, uint32 knobStyle = B_KNOB_NONE);

private:
	static rgb_color TrackColor(const rgb_color& base);
	float fRatio;
};

}	// namespace aurora

#endif
