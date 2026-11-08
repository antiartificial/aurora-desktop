// What a window's pixel buffer must do when the window is resized.
//
// Resizing reallocates the buffer, and the repaint that answers it is ASYNCHRONOUS: the
// app_server posts B_WINDOW_RESIZED and the client redraws whenever it gets round to it. So
// whatever the reallocation leaves behind is what is on screen until then. Canvas::Init leaves
// opaque white, which is the flash; Canvas::Resize keeps the pixels that still fit, so the
// stale frame is the previous CONTENT, a frame late rather than a different colour.
//
// The second claim is about HOW the overlap is kept. au_surface_resize used to copy it back row
// by row through DrawImage, whose destination is in layout units, so every device row was
// placed at y / scale and resampled. At scale 1 that is harmless and a test at scale 1 would
// not notice. At 1.45, which is what a real display here asks for, rows are duplicated and
// dropped and the kept frame comes back sheared. So the fractional scale is the case that
// matters and it is checked explicitly.
#include "canvas.h"

#include <stdio.h>

using aurora::Canvas;
using aurora::Color;
using aurora::FRect;

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)

// A pattern no resampling can reproduce by luck: every row a different value.
static void paint_rows(Canvas& c)
{
    for (int y = 0; y < c.Height(); y++)
        for (int x = 0; x < c.Width(); x++)
            const_cast<uint32_t*>(c.Pixels())[(size_t)y * c.Width() + x] = 0xFF000000u | (uint32_t)(y * 7 + 1);
}

static int row_value(const Canvas& c, int y)
{
    return (int)(c.Pixels()[(size_t)y * c.Width()] & 0xFFFFFFu);
}

int main()
{
    // ---- growing keeps what was there -------------------------------------------------------
    {
        Canvas c;
        c.Init(200, 100, 1.0);
        c.Clear(Color{10, 20, 30, 255});
        const uint32_t was = c.Pixels()[0];
        c.Resize(300, 180, 1.0);
        CHECK(c.Width() == 300 && c.Height() == 180, "grew to %dx%d, wanted 300x180", c.Width(), c.Height());
        CHECK(c.Pixels()[0] == was, "the top-left pixel changed on grow: %08x became %08x", was, c.Pixels()[0]);
        CHECK(c.Pixels()[(size_t)99 * 300 + 199] == was, "the far corner of the OLD area was not kept");
        // The area that did not exist before is the only part that may be white.
        CHECK(c.Pixels()[(size_t)179 * 300 + 299] == 0xFFFFFFFFu, "new area should be white, got %08x",
              c.Pixels()[(size_t)179 * 300 + 299]);
    }

    // ---- Init is still the white one, and that is what Resize exists to avoid ----------------
    {
        Canvas c;
        c.Init(200, 100, 1.0);
        c.Clear(Color{10, 20, 30, 255});
        c.Init(300, 180, 1.0);
        CHECK(c.Pixels()[0] == 0xFFFFFFFFu, "Init should fill white (it is what a NEW window wants)");
    }

    // ---- shrinking keeps the part that still fits --------------------------------------------
    {
        Canvas c;
        c.Init(200, 100, 1.0);
        c.Clear(Color{90, 80, 70, 255});
        const uint32_t was = c.Pixels()[0];
        c.Resize(120, 60, 1.0);
        CHECK(c.Width() == 120 && c.Height() == 60, "shrank to %dx%d, wanted 120x60", c.Width(), c.Height());
        CHECK(c.Pixels()[(size_t)59 * 120 + 119] == was, "the surviving corner was not kept on shrink");
    }

    // ---- at a FRACTIONAL scale, rows keep their position -------------------------------------
    // This is the one that catches resampling. Each row carries its own value, so a duplicated
    // or dropped row is visible as a row whose value is not the one it started with.
    {
        Canvas c;
        c.Init(200, 100, 1.45);
        paint_rows(c);
        const int h_before = c.Height();
        int want[400];
        for (int y = 0; y < h_before && y < 400; y++) want[y] = row_value(c, y);

        c.Resize(260, 150, 1.45);
        int moved = 0;
        for (int y = 0; y < h_before && y < 400; y++)
            if (row_value(c, y) != want[y]) moved++;
        CHECK(moved == 0, "%d of %d rows changed value at scale 1.45: the overlap was resampled, not copied",
              moved, h_before);
    }

    printf("%d checks, %d failed\n", checks, fails);
    return fails ? 1 : 0;
}
