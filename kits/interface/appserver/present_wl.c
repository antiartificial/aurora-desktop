/*
 * Wayland presenter: xdg_toplevel + wl_shm, plus a pointer for the first control. Written fresh in C against
 * libwayland-client and the scanner-generated xdg-shell glue (generated at build time from xdg-shell.xml).
 * One connection, one event thread; every window shares it. All wl_* calls hold g->mu.
 */
#define _GNU_SOURCE
#include "present.h"
#include "xdg-shell-client-protocol.h"

#include <poll.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>
#include <wayland-client.h>
#include <xkbcommon/xkbcommon.h>
#include <sys/mman.h>

struct au_wl {
	struct wl_display* dpy;
	struct wl_registry* reg;
	struct wl_compositor* comp;
	struct wl_shm* shm;
	struct xdg_wm_base* wm;
	struct wl_seat* seat;
	struct wl_pointer* ptr;
	struct wl_keyboard* kbd;
	struct xkb_context* xkb;
	struct xkb_keymap* keymap;
	struct xkb_state* xstate;
	struct au_wl_win* kfocus;
	uint32_t mods;
	int rep_rate, rep_delay;			/* repeat_info: keys/s, ms */
	int rep_key;					/* xkb keycode being repeated, 0 = none */
	au_key rep;
	struct timespec rep_next;
	struct wl_output* out;
	int scale;
	pthread_mutex_t mu;
	pthread_t thread;
	int running;
	struct au_wl_win* focus;
	int nwins;
};

struct au_wl_win {
	au_wl* g;
	struct wl_surface* surf;
	struct xdg_surface* xs;
	struct xdg_toplevel* top;
	au_event_fn fn;
	au_key_fn kfn;
	void* user;
	int configured, lw, lh;
	int destroyed;
};

struct buf { struct wl_buffer* b; void* mem; size_t size; };

static void buffer_release(void* data, struct wl_buffer* b)
{
	struct buf* x = data;
	wl_buffer_destroy(b);
	munmap(x->mem, x->size);
	free(x);
}
static const struct wl_buffer_listener buffer_listener = { buffer_release };

static void wm_ping(void* d, struct xdg_wm_base* wm, uint32_t serial) { (void)d; xdg_wm_base_pong(wm, serial); }
static const struct xdg_wm_base_listener wm_listener = { wm_ping };

static void xs_configure(void* data, struct xdg_surface* xs, uint32_t serial)
{
	struct au_wl_win* w = data;
	xdg_surface_ack_configure(xs, serial);
	w->configured = 1;
}
static const struct xdg_surface_listener xs_listener = { xs_configure };

static void top_configure(void* data, struct xdg_toplevel* t, int32_t width, int32_t height, struct wl_array* states)
{
	struct au_wl_win* w = data; (void)t; (void)states;
	if (width > 0 && height > 0 && (width != w->lw || height != w->lh)) {
		w->lw = width; w->lh = height;
		if (w->fn && w->configured) w->fn(w->user, AU_EV_RESIZE, width, height);
	}
}
static void top_close(void* data, struct xdg_toplevel* t)
{
	struct au_wl_win* w = data; (void)t;
	if (w->fn) w->fn(w->user, AU_EV_CLOSE, 0, 0);
}
static const struct xdg_toplevel_listener top_listener = { top_configure, top_close, NULL, NULL };

static void out_geometry(void* d, struct wl_output* o, int32_t x, int32_t y, int32_t pw, int32_t ph, int32_t sp, const char* mk, const char* md, int32_t tr)
{ (void)d; (void)o; (void)x; (void)y; (void)pw; (void)ph; (void)sp; (void)mk; (void)md; (void)tr; }
static void out_mode(void* d, struct wl_output* o, uint32_t f, int32_t w, int32_t h, int32_t r) { (void)d; (void)o; (void)f; (void)w; (void)h; (void)r; }
static void out_done(void* d, struct wl_output* o) { (void)d; (void)o; }
static void out_scale(void* d, struct wl_output* o, int32_t f) { au_wl* g = d; (void)o; if (f >= 1) g->scale = f; }
static const struct wl_output_listener out_listener = { out_geometry, out_mode, out_done, out_scale };

static void p_enter(void* d, struct wl_pointer* p, uint32_t s, struct wl_surface* sf, wl_fixed_t x, wl_fixed_t y)
{
	au_wl* g = d; (void)p; (void)s;
	g->focus = wl_surface_get_user_data(sf);
	if (g->focus && g->focus->fn) g->focus->fn(g->focus->user, AU_EV_POINTER_MOVE, (int)(wl_fixed_to_double(x) * 256), (int)(wl_fixed_to_double(y) * 256));
}
static void p_leave(void* d, struct wl_pointer* p, uint32_t s, struct wl_surface* sf)
{
	au_wl* g = d; (void)p; (void)s; (void)sf;
	if (g->focus && g->focus->fn) g->focus->fn(g->focus->user, AU_EV_POINTER_LEAVE, 0, 0);
	g->focus = NULL;
}
static void p_motion(void* d, struct wl_pointer* p, uint32_t t, wl_fixed_t x, wl_fixed_t y)
{
	au_wl* g = d; (void)p; (void)t;
	if (g->focus && g->focus->fn) g->focus->fn(g->focus->user, AU_EV_POINTER_MOVE, (int)(wl_fixed_to_double(x) * 256), (int)(wl_fixed_to_double(y) * 256));
}
static void p_button(void* d, struct wl_pointer* p, uint32_t s, uint32_t t, uint32_t button, uint32_t state)
{
	au_wl* g = d; (void)p; (void)s; (void)t;
	if (g->focus && g->focus->fn && button == 0x110)	/* BTN_LEFT */
		g->focus->fn(g->focus->user, AU_EV_POINTER_BUTTON, 1, state == WL_POINTER_BUTTON_STATE_PRESSED);
}
/* Wheel: wl_pointer.axis value is in surface px (a notch is about 10-15). a = axis (0 vertical, 1 horizontal),
 * b = value * 256. The first version left every listener past `axis` NULL; libwayland aborts the client the
 * moment the compositor sends wl_pointer.frame, i.e. on the first real pointer event. */
static void p_axis(void* d, struct wl_pointer* p, uint32_t t, uint32_t a, wl_fixed_t v)
{
	au_wl* g = d; (void)p; (void)t;
	if (g->focus && g->focus->fn) g->focus->fn(g->focus->user, AU_EV_WHEEL, (int)a, (int)(wl_fixed_to_double(v) * 256));
}
static void p_frame(void* d, struct wl_pointer* p) { (void)d; (void)p; }
static void p_axis_source(void* d, struct wl_pointer* p, uint32_t s) { (void)d; (void)p; (void)s; }
static void p_axis_stop(void* d, struct wl_pointer* p, uint32_t t, uint32_t a) { (void)d; (void)p; (void)t; (void)a; }
static void p_axis_discrete(void* d, struct wl_pointer* p, uint32_t a, int32_t n) { (void)d; (void)p; (void)a; (void)n; }
static const struct wl_pointer_listener ptr_listener = { p_enter, p_leave, p_motion, p_button, p_axis, p_frame, p_axis_source, p_axis_stop, p_axis_discrete };

/* ---- keyboard: wl_keyboard + xkbcommon. The keymap is whatever the compositor sends (it is configurable and
 * hot-reloadable there; a new wl_keyboard.keymap event replaces ours). ---- */
static uint32_t mods_now(au_wl* g)
{
	uint32_t m = 0;
	if (!g->xstate) return 0;
	if (xkb_state_mod_name_is_active(g->xstate, XKB_MOD_NAME_SHIFT, XKB_STATE_MODS_EFFECTIVE) > 0) m |= AU_MOD_SHIFT;
	if (xkb_state_mod_name_is_active(g->xstate, XKB_MOD_NAME_CTRL, XKB_STATE_MODS_EFFECTIVE) > 0) m |= AU_MOD_CTRL;
	if (xkb_state_mod_name_is_active(g->xstate, XKB_MOD_NAME_ALT, XKB_STATE_MODS_EFFECTIVE) > 0) m |= AU_MOD_ALT;
	if (xkb_state_mod_name_is_active(g->xstate, XKB_MOD_NAME_LOGO, XKB_STATE_MODS_EFFECTIVE) > 0) m |= AU_MOD_LOGO;
	if (xkb_state_mod_name_is_active(g->xstate, XKB_MOD_NAME_CAPS, XKB_STATE_MODS_EFFECTIVE) > 0) m |= AU_MOD_CAPS;
	if (xkb_state_mod_name_is_active(g->xstate, XKB_MOD_NAME_NUM, XKB_STATE_MODS_EFFECTIVE) > 0) m |= AU_MOD_NUM;
	return m;
}

/* BeOS character for the keys that are not text: the arrows, Home, Enter, ... (InterfaceDefs.h). 0 = none. */
static uint32_t special_char(xkb_keysym_t s)
{
	switch (s) {
	case XKB_KEY_Left: return 0x1c;  case XKB_KEY_Right: return 0x1d;
	case XKB_KEY_Up: return 0x1e;    case XKB_KEY_Down: return 0x1f;
	case XKB_KEY_Return: case XKB_KEY_KP_Enter: case XKB_KEY_ISO_Enter: return 0x0a;
	case XKB_KEY_BackSpace: return 0x08;
	case XKB_KEY_Tab: case XKB_KEY_ISO_Left_Tab: return 0x09;
	case XKB_KEY_Escape: return 0x1b;
	case XKB_KEY_Delete: return 0x7f;
	case XKB_KEY_Home: return 0x01;  case XKB_KEY_End: return 0x04;
	case XKB_KEY_Insert: return 0x05;
	case XKB_KEY_Page_Up: return 0x0b; case XKB_KEY_Page_Down: return 0x0c;
	}
	if (s >= XKB_KEY_F1 && s <= XKB_KEY_F12) return 0x10;	/* B_FUNCTION_KEY; the key code says which */
	return 0;
}

static void emit_key(au_wl* g, uint32_t evdev, int pressed, int repeat, au_key* out)
{
	xkb_keycode_t kc = evdev + 8;
	memset(out, 0, sizeof(*out));
	out->evdev = evdev; out->pressed = pressed; out->repeat = repeat; out->mods = g->mods;
	if (!g->xstate) return;
	xkb_keysym_t sym = xkb_state_key_get_one_sym(g->xstate, kc);
	uint32_t sp = special_char(sym);
	if (sp) {
		out->utf8[0] = (char)sp;
	} else {
		xkb_state_key_get_utf8(g->xstate, kc, out->utf8, sizeof(out->utf8));
		/* modifier keys and dead keys type nothing */
		if ((unsigned char)out->utf8[0] < 0x20 && out->utf8[0] != 0 && !(g->mods & AU_MOD_CTRL)) out->utf8[0] = 0;
	}
	/* raw_char: the symbol at level 0 of this layout (no shift/caps/ctrl) */
	xkb_layout_index_t lay = xkb_state_key_get_layout(g->xstate, kc);
	const xkb_keysym_t* syms; int n = xkb_keymap_key_get_syms_by_level(g->keymap, kc, lay, 0, &syms);
	out->raw_char = 0;
	if (n > 0) {
		uint32_t s2 = special_char(syms[0]);
		out->raw_char = s2 ? s2 : xkb_keysym_to_utf32(syms[0]);
	}
}

static void deliver_key(au_wl* g, const au_key* k)
{
	if (g->kfocus && g->kfocus->kfn) g->kfocus->kfn(g->kfocus->user, k);
}

static void kb_keymap(void* d, struct wl_keyboard* kb, uint32_t fmt, int32_t fd, uint32_t size)
{
	au_wl* g = d; (void)kb;
	if (fmt != WL_KEYBOARD_KEYMAP_FORMAT_XKB_V1) { close(fd); return; }
	char* map = mmap(NULL, size, PROT_READ, MAP_PRIVATE, fd, 0);
	close(fd);
	if (map == MAP_FAILED) return;
	struct xkb_keymap* km = xkb_keymap_new_from_string(g->xkb, map, XKB_KEYMAP_FORMAT_TEXT_V1, XKB_KEYMAP_COMPILE_NO_FLAGS);
	munmap(map, size);
	if (!km) { fprintf(stderr, "present_wl: compositor keymap did not compile\n"); return; }
	if (g->xstate) xkb_state_unref(g->xstate);
	if (g->keymap) xkb_keymap_unref(g->keymap);
	g->keymap = km; g->xstate = xkb_state_new(km); g->mods = 0; g->rep_key = 0;
}
static void kb_enter(void* d, struct wl_keyboard* kb, uint32_t s, struct wl_surface* sf, struct wl_array* keys)
{
	au_wl* g = d; (void)kb; (void)s; (void)keys;
	g->kfocus = wl_surface_get_user_data(sf);
	if (g->kfocus && g->kfocus->fn) g->kfocus->fn(g->kfocus->user, AU_EV_ACTIVATE, 1, 0);
}
static void kb_leave(void* d, struct wl_keyboard* kb, uint32_t s, struct wl_surface* sf)
{
	au_wl* g = d; (void)kb; (void)s; (void)sf;
	g->rep_key = 0;
	if (g->kfocus && g->kfocus->fn) g->kfocus->fn(g->kfocus->user, AU_EV_ACTIVATE, 0, 0);
	g->kfocus = NULL;
}
static void kb_key(void* d, struct wl_keyboard* kb, uint32_t s, uint32_t t, uint32_t key, uint32_t state)
{
	au_wl* g = d; (void)kb; (void)s; (void)t;
	if (!g->xstate) return;
	int pressed = state == WL_KEYBOARD_KEY_STATE_PRESSED;
	au_key k; emit_key(g, key, pressed, 0, &k);
	deliver_key(g, &k);
	if (pressed && g->rep_rate > 0 && xkb_keymap_key_repeats(g->keymap, key + 8)) {
		g->rep_key = key; g->rep = k;
		clock_gettime(CLOCK_MONOTONIC, &g->rep_next);
		long ms = g->rep_delay; g->rep_next.tv_sec += ms / 1000; g->rep_next.tv_nsec += (ms % 1000) * 1000000L;
		if (g->rep_next.tv_nsec >= 1000000000L) { g->rep_next.tv_sec++; g->rep_next.tv_nsec -= 1000000000L; }
	} else if (!pressed && (int)key == g->rep_key)
		g->rep_key = 0;
}
static void kb_mods(void* d, struct wl_keyboard* kb, uint32_t s, uint32_t dep, uint32_t lat, uint32_t lock, uint32_t grp)
{
	au_wl* g = d; (void)kb; (void)s;
	if (!g->xstate) return;
	xkb_state_update_mask(g->xstate, dep, lat, lock, 0, 0, grp);
	uint32_t old = g->mods, now = mods_now(g);
	g->mods = now;
	if (now != old && g->kfocus && g->kfocus->fn) g->kfocus->fn(g->kfocus->user, AU_EV_MODS, (int)now, (int)old);
}
static void kb_repeat(void* d, struct wl_keyboard* kb, int32_t rate, int32_t delay)
{
	au_wl* g = d; (void)kb;
	g->rep_rate = rate; g->rep_delay = delay;
}
static const struct wl_keyboard_listener kb_listener = { kb_keymap, kb_enter, kb_leave, kb_key, kb_mods, kb_repeat };

/* Client-side key repeat: the compositor only tells us the rate (repeat_info) and expects us to generate it. */
static void repeat_tick(au_wl* g)
{
	if (!g->rep_key || g->rep_rate <= 0) return;
	struct timespec now; clock_gettime(CLOCK_MONOTONIC, &now);
	while (g->rep_key && (now.tv_sec > g->rep_next.tv_sec || (now.tv_sec == g->rep_next.tv_sec && now.tv_nsec >= g->rep_next.tv_nsec))) {
		au_key k = g->rep; k.repeat = 1; k.pressed = 1;
		deliver_key(g, &k);
		long step = 1000000000L / g->rep_rate;
		g->rep_next.tv_nsec += step;
		while (g->rep_next.tv_nsec >= 1000000000L) { g->rep_next.tv_sec++; g->rep_next.tv_nsec -= 1000000000L; }
	}
}

static void seat_caps(void* d, struct wl_seat* s, uint32_t caps)
{
	au_wl* g = d;
	if ((caps & WL_SEAT_CAPABILITY_POINTER) && !g->ptr) {
		g->ptr = wl_seat_get_pointer(s);
		wl_pointer_add_listener(g->ptr, &ptr_listener, g);
	}
	if ((caps & WL_SEAT_CAPABILITY_KEYBOARD) && !g->kbd) {
		g->kbd = wl_seat_get_keyboard(s);
		wl_keyboard_add_listener(g->kbd, &kb_listener, g);
	}
}
static void seat_name(void* d, struct wl_seat* s, const char* n) { (void)d; (void)s; (void)n; }
static const struct wl_seat_listener seat_listener = { seat_caps, seat_name };

static void reg_global(void* data, struct wl_registry* r, uint32_t name, const char* iface, uint32_t ver)
{
	au_wl* g = data;
	if (!strcmp(iface, "wl_compositor")) g->comp = wl_registry_bind(r, name, &wl_compositor_interface, ver < 4 ? ver : 4);
	else if (!strcmp(iface, "wl_shm")) g->shm = wl_registry_bind(r, name, &wl_shm_interface, 1);
	else if (!strcmp(iface, "xdg_wm_base")) { g->wm = wl_registry_bind(r, name, &xdg_wm_base_interface, 1); xdg_wm_base_add_listener(g->wm, &wm_listener, g); }
	else if (!strcmp(iface, "wl_seat")) { g->seat = wl_registry_bind(r, name, &wl_seat_interface, ver < 5 ? ver : 5); wl_seat_add_listener(g->seat, &seat_listener, g); }
	else if (!strcmp(iface, "wl_output") && !g->out) { g->out = wl_registry_bind(r, name, &wl_output_interface, ver < 2 ? ver : 2); wl_output_add_listener(g->out, &out_listener, g); }
}
static void reg_remove(void* d, struct wl_registry* r, uint32_t n) { (void)d; (void)r; (void)n; }
static const struct wl_registry_listener reg_listener = { reg_global, reg_remove };

static void* event_thread(void* arg)
{
	au_wl* g = arg;
	while (g->running) {
		pthread_mutex_lock(&g->mu);
		while (wl_display_prepare_read(g->dpy) != 0) wl_display_dispatch_pending(g->dpy);
		wl_display_flush(g->dpy);
		pthread_mutex_unlock(&g->mu);
		struct pollfd pfd = { wl_display_get_fd(g->dpy), POLLIN, 0 };
		int r = poll(&pfd, 1, g->rep_key ? 5 : 50);
		pthread_mutex_lock(&g->mu);
		if (r > 0 && (pfd.revents & POLLIN)) wl_display_read_events(g->dpy); else wl_display_cancel_read(g->dpy);
		wl_display_dispatch_pending(g->dpy);
		repeat_tick(g);
		pthread_mutex_unlock(&g->mu);
	}
	return NULL;
}

au_wl* au_wl_connect(void)
{
	au_wl* g = calloc(1, sizeof(*g));
	g->dpy = wl_display_connect(NULL);
	if (!g->dpy) { free(g); return NULL; }
	g->scale = 1;
	g->xkb = xkb_context_new(XKB_CONTEXT_NO_FLAGS);
	g->rep_rate = 25; g->rep_delay = 400;
	pthread_mutex_init(&g->mu, NULL);
	g->reg = wl_display_get_registry(g->dpy);
	wl_registry_add_listener(g->reg, &reg_listener, g);
	wl_display_roundtrip(g->dpy);
	wl_display_roundtrip(g->dpy);	/* seat caps and output scale arrive after the binds */
	if (!g->comp || !g->shm || !g->wm) {
		fprintf(stderr, "present_wl: compositor lacks wl_compositor/wl_shm/xdg_wm_base\n");
		wl_display_disconnect(g->dpy); free(g); return NULL;
	}
	g->running = 1;
	pthread_create(&g->thread, NULL, event_thread, g);
	return g;
}

int au_wl_output_scale(au_wl* g) { return g->scale; }

au_wl_win* au_wl_win_create(au_wl* g, const char* title, int lw, int lh, au_event_fn fn, void* user)
{
	struct au_wl_win* w = calloc(1, sizeof(*w));
	w->g = g; w->fn = fn; w->user = user; w->lw = lw; w->lh = lh;
	pthread_mutex_lock(&g->mu);
	w->surf = wl_compositor_create_surface(g->comp);
	wl_surface_set_user_data(w->surf, w);
	w->xs = xdg_wm_base_get_xdg_surface(g->wm, w->surf);
	xdg_surface_add_listener(w->xs, &xs_listener, w);
	w->top = xdg_surface_get_toplevel(w->xs);
	xdg_toplevel_add_listener(w->top, &top_listener, w);
	xdg_toplevel_set_title(w->top, title && *title ? title : "Aurora");
	xdg_toplevel_set_app_id(w->top, "org.aurora.interface-kit");
	wl_surface_commit(w->surf);
	wl_display_flush(g->dpy);
	pthread_mutex_unlock(&g->mu);
	/* the event thread dispatches the configure; wait for it (the first present needs the ack) */
	for (int i = 0; i < 200 && !w->configured; i++) { struct timespec ts = {0, 10 * 1000 * 1000}; nanosleep(&ts, NULL); }
	if (!w->configured) fprintf(stderr, "present_wl: no xdg_surface.configure within 2 s\n");
	g->nwins++;
	return w;
}

int au_wl_win_present(au_wl_win* w, const uint32_t* argb, int pw, int ph, int bscale)
{
	au_wl* g = w->g;
	if (w->destroyed || !w->configured) return -1;
	size_t stride = (size_t)pw * 4, size = stride * ph;
	int fd = memfd_create("aurora-frame", MFD_CLOEXEC);
	if (fd < 0 || ftruncate(fd, size) < 0) return -1;
	void* mem = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (mem == MAP_FAILED) { close(fd); return -1; }
	memcpy(mem, argb, size);
	pthread_mutex_lock(&g->mu);
	struct wl_shm_pool* pool = wl_shm_create_pool(g->shm, fd, (int32_t)size);
	struct wl_buffer* b = wl_shm_pool_create_buffer(pool, 0, pw, ph, (int32_t)stride, WL_SHM_FORMAT_XRGB8888);
	wl_shm_pool_destroy(pool);
	struct buf* x = malloc(sizeof(*x));
	x->b = b; x->mem = mem; x->size = size;
	wl_buffer_add_listener(b, &buffer_listener, x);
	wl_surface_set_buffer_scale(w->surf, bscale);
	wl_surface_attach(w->surf, b, 0, 0);
	wl_surface_damage_buffer(w->surf, 0, 0, pw, ph);
	wl_surface_commit(w->surf);
	wl_display_flush(g->dpy);
	pthread_mutex_unlock(&g->mu);
	close(fd);
	return 0;
}

void au_wl_win_set_key_fn(au_wl_win* w, au_key_fn fn) { w->kfn = fn; }

void au_wl_win_destroy(au_wl_win* w)
{
	au_wl* g = w->g;
	pthread_mutex_lock(&g->mu);
	w->destroyed = 1;
	if (g->focus == w) g->focus = NULL;
	if (g->kfocus == w) { g->kfocus = NULL; g->rep_key = 0; }
	xdg_toplevel_destroy(w->top);
	xdg_surface_destroy(w->xs);
	wl_surface_destroy(w->surf);
	wl_display_flush(g->dpy);
	pthread_mutex_unlock(&g->mu);
}
