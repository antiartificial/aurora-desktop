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

struct au_wl {
	struct wl_display* dpy;
	struct wl_registry* reg;
	struct wl_compositor* comp;
	struct wl_shm* shm;
	struct xdg_wm_base* wm;
	struct wl_seat* seat;
	struct wl_pointer* ptr;
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
static void p_axis(void* d, struct wl_pointer* p, uint32_t t, uint32_t a, wl_fixed_t v) { (void)d; (void)p; (void)t; (void)a; (void)v; }
static const struct wl_pointer_listener ptr_listener = { p_enter, p_leave, p_motion, p_button, p_axis, NULL, NULL, NULL, NULL, NULL, NULL };

static void seat_caps(void* d, struct wl_seat* s, uint32_t caps)
{
	au_wl* g = d;
	if ((caps & WL_SEAT_CAPABILITY_POINTER) && !g->ptr) {
		g->ptr = wl_seat_get_pointer(s);
		wl_pointer_add_listener(g->ptr, &ptr_listener, g);
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
		int r = poll(&pfd, 1, 50);
		pthread_mutex_lock(&g->mu);
		if (r > 0 && (pfd.revents & POLLIN)) wl_display_read_events(g->dpy); else wl_display_cancel_read(g->dpy);
		wl_display_dispatch_pending(g->dpy);
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

void au_wl_win_destroy(au_wl_win* w)
{
	au_wl* g = w->g;
	pthread_mutex_lock(&g->mu);
	w->destroyed = 1;
	if (g->focus == w) g->focus = NULL;
	xdg_toplevel_destroy(w->top);
	xdg_surface_destroy(w->xs);
	wl_surface_destroy(w->surf);
	wl_display_flush(g->dpy);
	pthread_mutex_unlock(&g->mu);
}
