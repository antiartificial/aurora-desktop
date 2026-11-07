/* Presenter seam: where a finished frame goes. Wayland (present_wl.c) or nothing (tests read last_frame()). */
#ifndef AURORA_PRESENT_H
#define AURORA_PRESENT_H
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif

enum { AU_EV_CLOSE = 1, AU_EV_RESIZE = 2, AU_EV_POINTER_MOVE = 3, AU_EV_POINTER_BUTTON = 4, AU_EV_POINTER_LEAVE = 5, AU_EV_KEY = 8, AU_EV_WHEEL = 9 };
/* a,b meaning: WHEEL: axis (0 vertical, 1 horizontal), value*256 in px. RESIZE: logical w,h. MOVE: x*256,y*256 logical. BUTTON: button(1=left), pressed(0/1). */
typedef void (*au_event_fn)(void* user, int kind, int a, int b);
/* Keyboard. AU_EV_ACTIVATE: a = 1 when the window gained keyboard focus, 0 when it lost it.
 * AU_EV_MODS: a = new modifier bits (AU_MOD_*), b = old ones. Keys arrive on the au_key_fn. */
enum { AU_EV_ACTIVATE = 6, AU_EV_MODS = 7 };
enum { AU_MOD_SHIFT = 1, AU_MOD_CTRL = 2, AU_MOD_ALT = 4, AU_MOD_LOGO = 8, AU_MOD_CAPS = 16, AU_MOD_NUM = 32 };
typedef struct {
	uint32_t evdev;		/* linux input code (KEY_*) */
	int pressed, repeat;
	uint32_t mods;		/* AU_MOD_* effective when the key went down/up */
	uint32_t raw_char;	/* the character without modifiers, as BeOS "raw_char" (special keys already mapped) */
	char utf8[8];		/* what the key types now (shift, caps and ctrl applied), NUL terminated; empty if nothing */
} au_key;
typedef void (*au_key_fn)(void* user, const au_key* k);

typedef struct au_wl au_wl;
typedef struct au_wl_win au_wl_win;

au_wl* au_wl_connect(void);					/* NULL when there is no compositor */
int au_wl_output_scale(au_wl*);
int au_wl_output_size(au_wl*, int* w, int* h);		/* logical size of the output; 0 if the compositor sent no mode */				/* integer wl_output scale, >= 1 */
au_wl_win* au_wl_win_create(au_wl*, const char* title, int logical_w, int logical_h, au_event_fn fn, void* user);
/* argb is px_w*px_h; buffer_scale tells the compositor how many device px make one logical px */
int au_wl_win_present(au_wl_win*, const uint32_t* argb, int px_w, int px_h, int buffer_scale);
au_wl_win* au_wl_popup_create(au_wl*, au_wl_win* parent, int x, int y, int logical_w, int logical_h, au_event_fn fn, void* user);
/* Make `child` a dialog of `parent` (xdg_toplevel.set_parent): the compositor centres it on its parent. */
void au_wl_win_set_parent(au_wl_win* child, au_wl_win* parent);
void au_wl_win_set_key_fn(au_wl_win*, au_key_fn fn);
void au_wl_win_destroy(au_wl_win*);

/* ---- additions for aurora::ui ------------------------------------------------------------
 * The app_server does not use these and its behaviour is unchanged.
 *
 * au_wl_wait: sleep until the event thread has dispatched something, or until timeout_ms.
 * Events already arrive on present_wl's own thread, so a toolkit's main loop needs to WAIT,
 * not spin. Returns 1 if woken by an event, 0 on timeout.
 *
 * au_wl_win_present_damage: like au_wl_win_present, but it reuses the window's shm buffers
 * instead of creating a memfd, pool and wl_buffer per frame, and it tells the compositor which
 * rectangles actually changed. The per-frame allocation is the expensive part at size: a
 * 1920x1200 window at scale 1.45 copies 19 MB every frame whether one pixel changed or not.
 * rects is nrects quadruples of {x, y, w, h} in BUFFER (device) pixels; nrects 0 damages all. */
int au_wl_wait(au_wl*, int timeout_ms);
int au_wl_win_present_damage(au_wl_win*, const uint32_t* argb, int px_w, int px_h,
                             int buffer_scale, const int* rects, int nrects);

#ifdef __cplusplus
}
#endif
#endif
