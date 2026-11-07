/* A client that forces the shm buffer pool in present_wl.c to use its SECOND buffer, and
 * presents a pattern whose correctness is decidable from one screenshot.
 *
 * Why it exists. `au_wl_win_present_damage` keeps two shm buffers and copies only the damaged
 * rectangles into whichever one the compositor has handed back. That is correct only if the
 * buffer being written already holds every earlier frame. It does not:
 *
 *   - a buffer allocated this frame (the first use of the second slot, or any slot after a
 *     resize invalidated it) is a fresh memfd, which is ZERO, and only the damage rects get
 *     copied into it, so the rest of the window is black
 *   - a buffer written two frames ago never received last frame's damage, so the regions that
 *     changed last frame are stale in it
 *
 * Neither shows while the compositor releases a buffer before the next frame, because then slot
 * 0 is always free and slot 1 is never allocated. A resize is exactly when that stops being
 * true: buffers are invalidated by the size change and the compositor is busy, so the second
 * slot comes into use. That is the flicker.
 *
 * The sequence below presents without waiting for a release, which puts slot 1 in play
 * deterministically rather than hoping for load:
 *
 *   frame 1  whole canvas RED,   full damage          -> slot 0
 *   frame 2  a GREEN square,     damage = that square -> slot 1 (slot 0 still on screen)
 *   frame 3  a BLUE square,      damage = that square -> slot 0 or 1
 *
 * A correct pool shows red with a green square and a blue square. The bugs show as a black
 * window, or a missing green square, depending on which slot frame 3 landed in.
 *
 * POOL_STEPS=n stops after n frames. POOL_RESIZE=WxH asks for a resize before frame 2.
 */
#include "../../kits/interface/appserver/present.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#define W 400
#define H 300

static uint32_t px[W * H];
static volatile int closed = 0;
static volatile int cfg_w = W, cfg_h = H;

static void ev(void* user, int kind, int a, int b)
{
	(void)user;
	if (kind == AU_EV_CLOSE) closed = 1;
	if (kind == AU_EV_RESIZE && a > 0 && b > 0) { cfg_w = a; cfg_h = b; }
}

static void fill(uint32_t c)
{
	for (int i = 0; i < W * H; i++) px[i] = c;
}

static void square(int x, int y, int w, int h, uint32_t c)
{
	for (int yy = y; yy < y + h && yy < H; yy++)
		for (int xx = x; xx < x + w && xx < W; xx++)
			px[yy * W + xx] = c;
}

int main(void)
{
	const uint32_t RED = 0xffff0000, GREEN = 0xff00ff00, BLUE = 0xff0000ff;
	const int steps = getenv("POOL_STEPS") ? atoi(getenv("POOL_STEPS")) : 3;

	au_wl* wl = au_wl_connect();
	if (!wl) { fprintf(stderr, "pool: no compositor\n"); return 2; }
	au_wl_win* win = au_wl_win_create(wl, "PoolTest", W, H, ev, NULL);
	if (!win) { fprintf(stderr, "pool: no window\n"); return 2; }

	/* frame 1: the whole window, full damage. This is the only frame that is honest today. */
	fill(RED);
	au_wl_win_present_damage(win, px, W, H, 1, NULL, 0);
	printf("frame 1 red full\n");
	fflush(stdout);

	/* No wait. The point is to present again while the compositor still holds frame 1, which
	 * is what puts the second pool slot in play. */
	if (steps >= 2) {
		int g[4] = { 40, 40, 80, 80 };
		square(g[0], g[1], g[2], g[3], GREEN);
		au_wl_win_present_damage(win, px, W, H, 1, g, 1);
		printf("frame 2 green rect %d,%d %dx%d partial\n", g[0], g[1], g[2], g[3]);
		fflush(stdout);
	}
	/* POOL_SETTLE: let the compositor hand both slots back before the last frame. Slot 0 is
	 * then free again and gets picked, and slot 0 never received frame 2 -- so this is the
	 * backlog half of the defect, where the first slot is a frame behind rather than fresh. */
	if (getenv("POOL_SETTLE")) {
		for (int i = 0; i < 12; i++) au_wl_wait(wl, 50);
		printf("settled: both slots released\n");
		fflush(stdout);
	}
	if (steps >= 3) {
		int b[4] = { 240, 40, 80, 80 };
		square(b[0], b[1], b[2], b[3], BLUE);
		au_wl_win_present_damage(win, px, W, H, 1, b, 1);
		printf("frame 3 blue rect %d,%d %dx%d partial\n", b[0], b[1], b[2], b[3]);
		fflush(stdout);
	}

	printf("presented; holding\n");
	fflush(stdout);
	for (int i = 0; i < 2000 && !closed; i++) au_wl_wait(wl, 50);
	au_wl_win_destroy(win);
	return 0;
}
