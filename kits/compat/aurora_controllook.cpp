#include "aurora_controllook.h"

#include "aurora_theme.h"

#include <Font.h>
#include <View.h>

#include <math.h>
#include <stdio.h>

namespace aurora {

typedef BPrivate::HaikuControlLook Stock;

AuroraControlLook::AuroraControlLook()
	:
	fRatio(ControlCornerRatio())
{
}

AuroraControlLook::~AuroraControlLook()
{
}

float AuroraControlLook::Radius() const
{
	float r = roundf(fRatio * 2.0f * be_plain_font->Size());
	return r < 2.0f ? 2.0f : r > 8.0f ? 8.0f : r;
}

// ---- buttons ------------------------------------------------------------------------------------------------------
// Haiku rounds a button by drawing each corner, then excluding it from the clip (BView::ClipToInverseRect) before
// the straight edges go on. Aurora's server clips to one rectangle (appserver/server.cpp: "not a true region"), so
// that never rounds anything here. This look draws a button's outline and fill as rounded rectangles instead,
// inside the very same insets Haiku's frame (1 px edge + 1 px frame, 5 for a default button) and background (1 px)
// use, so a BButton's label sits where it always did and no layout changes.
// Flat buttons (toolbar style, idle) and partial borders (tab edges, split buttons) are Haiku's, untouched.

static bool IsFlatIdle(uint32 flags)
{
	return (flags & BControlLook::B_FLAT) != 0
		&& (flags & (BControlLook::B_ACTIVATED | BControlLook::B_PARTIALLY_ACTIVATED)) == 0
		&& ((flags & (BControlLook::B_HOVER | BControlLook::B_FOCUSED)) == 0 || (flags & BControlLook::B_DISABLED) != 0);
}

// A button's face: the control colour, a shade darker under the pointer, darker again pressed; disabled is the panel.
static rgb_color FaceColor(const rgb_color& base, const rgb_color& background, uint32 flags)
{
	if ((flags & BControlLook::B_DISABLED) != 0)
		return tint_color(background, 0.99f);
	if ((flags & (BControlLook::B_ACTIVATED | BControlLook::B_CLICKED)) != 0)
		return tint_color(base, 1.10f);
	if ((flags & BControlLook::B_HOVER) != 0)
		return tint_color(base, 1.04f);
	return base;
}

void AuroraControlLook::DrawButtonFrame(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
	const rgb_color& background, uint32 flags, uint32 borders)
{
	if (IsFlatIdle(flags) || borders != B_ALL_BORDERS || !rect.IsValid()) {
		Stock::DrawButtonFrame(view, rect, updateRect, base, background, flags, borders);
		return;
	}
	float radius = Radius();
	view->PushState();
	view->ClipToRect(rect);
	view->SetHighColor(background);
	view->FillRect(rect);
	BRect outline = rect;
	bool isDefault = (flags & B_DEFAULT_BUTTON) != 0;
	if (isDefault)
		outline.InsetBy(1, 1);		// a 1 px margin of the panel around a default button's heavier border, as Haiku leaves
	view->SetHighColor(FaceColor(base, background, flags));
	view->FillRoundRect(outline, radius, radius);
	rgb_color border = ui_color(B_CONTROL_BORDER_COLOR);
	float pen = 1.0f;
	if ((flags & B_DISABLED) != 0)
		border = disable_color(border, background);
	else if ((flags & B_FOCUSED) != 0) {
		border = ui_color(B_KEYBOARD_NAVIGATION_COLOR);
		pen = 2.0f;
	} else if (isDefault) {
		border = tint_color(border, B_DARKEN_2_TINT);
		pen = 2.0f;
	}
	view->SetHighColor(border);
	view->SetPenSize(pen);
	BRect ring = outline;
	if (pen > 1.0f)
		ring.InsetBy(0.5f, 0.5f);
	view->StrokeRoundRect(ring, radius, radius);
	view->PopState();
	rect.InsetBy(isDefault ? 5 : 2, isDefault ? 5 : 2);
}

void AuroraControlLook::DrawButtonBackground(BView* view, BRect& rect, const BRect& updateRect,
	const rgb_color& base, uint32 flags, uint32 borders, orientation orientation)
{
	if (IsFlatIdle(flags) || borders != B_ALL_BORDERS || !rect.IsValid()) {
		Stock::DrawButtonBackground(view, rect, updateRect, base, flags, borders, orientation);
		return;
	}
	float radius = Radius() - 2.0f < 0.0f ? 0.0f : Radius() - 2.0f;
	view->PushState();
	view->ClipToRect(rect);
	view->SetHighColor(FaceColor(base, base, flags));
	view->FillRoundRect(rect, radius, radius);
	view->PopState();
	rect.InsetBy(1, 1);
}

// ---- boxes --------------------------------------------------------------------------------------------------------
// BBox's group frame: one rounded line in place of Haiku's three nested bevels, the same three pixels deep.

void AuroraControlLook::DrawGroupFrame(BView* view, BRect& rect, const BRect& updateRect, const rgb_color& base,
	uint32 borders)
{
	if (borders != B_ALL_BORDERS || !rect.IsValid()) {
		Stock::DrawGroupFrame(view, rect, updateRect, base, borders);
		return;
	}
	float radius = Radius();
	view->PushState();
	view->ClipToRect(rect);
	view->SetHighColor(tint_color(base, 1.16f));
	view->SetPenSize(1.0f);
	view->StrokeRoundRect(rect.InsetByCopy(1, 1), radius, radius);
	view->PopState();
	rect.InsetBy(3, 3);
}

// ---- focus --------------------------------------------------------------------------------------------------------
// Haiku marks focus by colouring its one-pixel frame B_KEYBOARD_NAVIGATION_COLOR. A pixel of a mid colour is the
// thing a 3:1 non-text contrast rule is hardest on, so Aurora makes it two: a button's rounded border is two pixels
// of the accent when focused (above), and a text control gets a second pixel on its outer edge (below).

void AuroraControlLook::DrawTextControlBorder(BView* view, BRect& rect, const BRect& updateRect,
	const rgb_color& base, uint32 flags, uint32 borders)
{
	BRect outer = rect;
	Stock::DrawTextControlBorder(view, rect, updateRect, base, flags, borders);
	// The text view sits right inside the two pixels of border, so the second ring pixel goes on the outer edge,
	// over the stock code's recessed highlight. Square: the text view inside is.
	if ((flags & B_FOCUSED) != 0 && (flags & B_DISABLED) == 0 && (flags & B_INVALID) == 0
		&& borders == B_ALL_BORDERS && ShouldDraw(view, outer, updateRect)) {
		view->PushState();
		view->SetHighColor(ui_color(B_KEYBOARD_NAVIGATION_COLOR));
		view->StrokeRect(outer);
		view->PopState();
	}
}

// ---- scroll bar ---------------------------------------------------------------------------------------------------
// Flat. The track is the panel a shade darker; the thumb is a pill in B_SCROLL_BAR_THUMB_COLOR (>= 3:1 on the panel);
// the arrow buttons are the track with an arrow on it. Haiku's gradient and bevel are not drawn.

rgb_color AuroraControlLook::TrackColor(const rgb_color& base)
{
	return tint_color(base, 1.04f);
}

void AuroraControlLook::DrawScrollBarBackground(BView* view, BRect& rect, const BRect& updateRect,
	const rgb_color& base, uint32 flags, orientation orientation)
{
	if (!ShouldDraw(view, rect, updateRect))
		return;
	view->PushState();
	view->ClipToRect(rect);
	view->SetHighColor(TrackColor(base));
	view->FillRect(rect);
	view->PopState();
}

void AuroraControlLook::DrawScrollBarThumb(BView* view, BRect& rect, const BRect& updateRect,
	const rgb_color& base, uint32 flags, orientation orientation, uint32 knobStyle)
{
	if (!ShouldDraw(view, rect, updateRect))
		return;
	view->PushState();
	view->ClipToRect(rect);
	// BScrollBar hands the thumb ITS colour as `base`; the track's colour is the panel, which is the view colour.
	view->SetHighColor(TrackColor(view->ViewColor()));
	view->FillRect(rect);
	if ((flags & B_DISABLED) == 0) {
		rgb_color thumb = base;
		if ((flags & B_CLICKED) != 0)
			thumb = tint_color(thumb, B_DARKEN_2_TINT);
		else if ((flags & B_HOVER) != 0)
			thumb = tint_color(thumb, B_DARKEN_1_TINT);
		// Inset across the bar so the pill floats in the track; a pixel along it so adjacent arrows do not touch.
		BRect pill = rect;
		if (orientation == B_VERTICAL)
			pill.InsetBy(3.0f, 1.0f);
		else
			pill.InsetBy(1.0f, 3.0f);
		if (pill.IsValid()) {
			float r = floorf(fminf(pill.Width(), pill.Height()) / 2.0f);
			view->SetHighColor(thumb);
			view->FillRoundRect(pill, r, r);
		}
	}
	view->PopState();
}

void AuroraControlLook::DrawScrollBarButton(BView* view, BRect rect, const BRect& updateRect,
	const rgb_color& base, const rgb_color& text, uint32 flags, int32 direction, orientation orientation,
	bool down)
{
	if (!ShouldDraw(view, rect, updateRect))
		return;
	view->PushState();
	view->ClipToRect(rect);
	rgb_color track = TrackColor(base);
	if (down)
		track = tint_color(track, B_DARKEN_1_TINT);
	view->SetHighColor(track);
	view->FillRect(rect);
	rect.InsetBy(-1, -1);
	bool enabled = (flags & B_DISABLED) == 0;
	DrawArrowShape(view, rect, updateRect, enabled ? text : tint_color(text, B_LIGHTEN_1_TINT), direction, flags, 1);
	view->PopState();
}

}	// namespace aurora
