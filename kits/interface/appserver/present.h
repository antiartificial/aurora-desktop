/* Presenter seam: where a finished frame goes. Wayland (present_wl.c) or nothing (tests read last_frame()). */
#ifndef AURORA_PRESENT_H
#define AURORA_PRESENT_H
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif

enum { AU_EV_CLOSE = 1, AU_EV_RESIZE = 2, AU_EV_POINTER_MOVE = 3, AU_EV_POINTER_BUTTON = 4, AU_EV_POINTER_LEAVE = 5 };
/* a,b meaning: RESIZE: logical w,h. MOVE: x*256,y*256 logical. BUTTON: button(1=left), pressed(0/1). */
typedef void (*au_event_fn)(void* user, int kind, int a, int b);

typedef struct au_wl au_wl;
typedef struct au_wl_win au_wl_win;

au_wl* au_wl_connect(void);					/* NULL when there is no compositor */
int au_wl_output_scale(au_wl*);				/* integer wl_output scale, >= 1 */
au_wl_win* au_wl_win_create(au_wl*, const char* title, int logical_w, int logical_h, au_event_fn fn, void* user);
/* argb is px_w*px_h; buffer_scale tells the compositor how many device px make one logical px */
int au_wl_win_present(au_wl_win*, const uint32_t* argb, int px_w, int px_h, int buffer_scale);
void au_wl_win_destroy(au_wl_win*);

#ifdef __cplusplus
}
#endif
#endif
