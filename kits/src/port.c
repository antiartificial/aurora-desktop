#define _GNU_SOURCE
#include "kits_internal.h"
#include <stdio.h>
#include <string.h>
#include <unistd.h>

/* Message record in the ring: int32 code, uint32 len, payload padded to 8. */
#define HDR 8u
static uint32 rec_size(size_t len) { return HDR + (uint32)((len + 7) & ~(size_t)7); }

static kport *lookup(port_id id)
{
	if (id <= 0) return NULL;
	kport *p = &K->ports[(id - 1) % K_MAX_PORTS];
	return p->id == id ? p : NULL;
}
static char *ring_of(kport *p) { return K_rings + (size_t)(p - K->ports) * K_RING; }

static void ring_copy_in(kport *p, uint32 off, const void *src, size_t n)
{
	char *r = ring_of(p);
	off %= K_RING;
	size_t first = K_RING - off < n ? K_RING - off : n;
	memcpy(r + off, src, first);
	if (n > first) memcpy(r, (const char *)src + first, n - first);
}
static void ring_copy_out(kport *p, uint32 off, void *dst, size_t n)
{
	char *r = ring_of(p);
	off %= K_RING;
	size_t first = K_RING - off < n ? K_RING - off : n;
	memcpy(dst, r + off, first);
	if (n > first) memcpy((char *)dst + first, r, n - first);
}

port_id create_port(int32 capacity, const char *name)
{
	if (capacity < 1 || capacity > 65535) return B_BAD_VALUE;
	k_lock();
	int slot = -1;
	for (int pass = 0; pass < 2 && slot < 0; pass++) {
		for (int i = 0; i < K_MAX_PORTS; i++) {
			kport *p = &K->ports[i];
			if (pass == 1 && p->id && k_owner_gone(p->team, p->team_start)) p->id = 0; /* reap */
			if (!p->id) { slot = i; break; }
		}
	}
	if (slot < 0) { k_unlock(); return B_NO_MORE_PORTS; }
	kport *p = &K->ports[slot];
	p->id = (int32)(K->port_gen++ % 16000000u) * K_MAX_PORTS + slot + 1;
	p->team = getpid();
	p->team_start = k_pid_start(p->team);
	snprintf(p->name, sizeof p->name, "%s", name ? name : "unnamed port");
	p->capacity = capacity;
	p->count = p->total = p->closed = 0;
	p->head = p->used = 0;
	port_id id = p->id;
	k_unlock();
	return id;
}

port_id find_port(const char *name)
{
	if (!name) return B_BAD_VALUE;
	k_lock();
	port_id found = B_NAME_NOT_FOUND;
	for (int i = 0; i < K_MAX_PORTS; i++)
		if (K->ports[i].id && !strncmp(K->ports[i].name, name, B_OS_NAME_LENGTH - 1)) {
			found = K->ports[i].id;
			break;
		}
	k_unlock();
	return found;
}

/* Wait (lock held) until a message is readable. Returns B_OK with p set. */
static status_t wait_readable(port_id id, uint32 flags, bigtime_t timeout, kport **out)
{
	struct timespec ts;
	int d = k_deadline(flags, timeout, &ts);
	for (;;) {
		kport *p = lookup(id);
		if (!p) return B_BAD_PORT_ID;
		if (p->count > 0) { *out = p; return B_OK; }
		if (p->closed) return B_BAD_PORT_ID;
		if (d < 0) return d;
		if (k_wait(&p->rd, &K->lock, d > 0, &ts) == B_TIMED_OUT) {
			p = lookup(id);
			if (!p) return B_BAD_PORT_ID;
			if (p->count > 0) { *out = p; return B_OK; }
			return p->closed ? B_BAD_PORT_ID : B_TIMED_OUT;
		}
	}
}

ssize_t read_port_etc(port_id id, int32 *code, void *buf, size_t size,
	uint32 flags, bigtime_t timeout)
{
	if (!code || (size && !buf)) return B_BAD_VALUE;
	k_lock();
	kport *p;
	status_t st = wait_readable(id, flags, timeout, &p);
	if (st != B_OK) { k_unlock(); return st; }
	int32 hdr[2];
	ring_copy_out(p, p->head, hdr, HDR);
	size_t len = (size_t)(uint32)hdr[1], n = len < size ? len : size;
	*code = hdr[0];
	if (n) ring_copy_out(p, p->head + HDR, buf, n);
	uint32 rs = rec_size(len);
	p->head = (p->head + rs) % K_RING;
	p->used -= rs;
	p->count--;
	p->total++;
	pthread_cond_broadcast(&p->wr);
	k_unlock();
	return (ssize_t)n; /* oversize messages are truncated and consumed */
}

ssize_t read_port(port_id id, int32 *code, void *buf, size_t size)
{
	return read_port_etc(id, code, buf, size, 0, 0);
}

ssize_t port_buffer_size_etc(port_id id, uint32 flags, bigtime_t timeout)
{
	k_lock();
	kport *p;
	status_t st = wait_readable(id, flags, timeout, &p);
	if (st != B_OK) { k_unlock(); return st; }
	int32 hdr[2];
	ring_copy_out(p, p->head, hdr, HDR);
	k_unlock();
	return (ssize_t)(uint32)hdr[1];
}
ssize_t port_buffer_size(port_id id) { return port_buffer_size_etc(id, 0, 0); }

status_t write_port_etc(port_id id, int32 code, const void *buf, size_t size,
	uint32 flags, bigtime_t timeout)
{
	if (size && !buf) return B_BAD_VALUE;
	if (size > K_RING - HDR - 8) return B_BAD_VALUE;
	uint32 rs = rec_size(size);
	struct timespec ts;
	k_lock();
	int d = k_deadline(flags, timeout, &ts);
	for (;;) {
		kport *p = lookup(id);
		if (!p || p->closed) { k_unlock(); return B_BAD_PORT_ID; }
		if (p->count < p->capacity && K_RING - p->used >= rs) {
			int32 hdr[2] = { code, (int32)size };
			uint32 at = p->head + p->used;
			ring_copy_in(p, at, hdr, HDR);
			if (size) ring_copy_in(p, at + HDR, buf, size);
			p->used += rs;
			p->count++;
			pthread_cond_broadcast(&p->rd);
			k_unlock();
			return B_OK;
		}
		if (d < 0) { k_unlock(); return d; }
		if (k_wait(&p->wr, &K->lock, d > 0, &ts) == B_TIMED_OUT) {
			p = lookup(id);
			if (!p || p->closed) { k_unlock(); return B_BAD_PORT_ID; }
			if (p->count < p->capacity && K_RING - p->used >= rs) continue;
			k_unlock();
			return B_TIMED_OUT;
		}
	}
}

status_t write_port(port_id id, int32 code, const void *buf, size_t size)
{
	return write_port_etc(id, code, buf, size, 0, 0);
}

status_t close_port(port_id id)
{
	k_lock();
	kport *p = lookup(id);
	if (!p) { k_unlock(); return B_BAD_PORT_ID; }
	p->closed = 1;
	pthread_cond_broadcast(&p->rd);
	pthread_cond_broadcast(&p->wr);
	k_unlock();
	return B_OK;
}

status_t delete_port(port_id id)
{
	k_lock();
	kport *p = lookup(id);
	if (!p) { k_unlock(); return B_BAD_PORT_ID; }
	p->id = 0; /* waiters re-lookup by id and see it gone */
	p->count = 0; p->used = 0;
	pthread_cond_broadcast(&p->rd);
	pthread_cond_broadcast(&p->wr);
	k_unlock();
	return B_OK;
}

ssize_t port_count(port_id id)
{
	k_lock();
	kport *p = lookup(id);
	ssize_t n = p ? p->count : B_BAD_PORT_ID;
	k_unlock();
	return n;
}

status_t set_port_owner(port_id id, team_id team)
{
	if (team <= 0 || k_pid_dead(team)) return B_BAD_TEAM_ID;
	k_lock();
	kport *p = lookup(id);
	if (p) p->team = team;
	k_unlock();
	return p ? B_OK : B_BAD_PORT_ID;
}

status_t _get_port_info(port_id id, port_info *info, size_t size)
{
	if (!info || size < sizeof *info) return B_BAD_VALUE;
	k_lock();
	kport *p = lookup(id);
	if (!p) { k_unlock(); return B_BAD_PORT_ID; }
	info->port = id; info->team = p->team;
	snprintf(info->name, sizeof info->name, "%s", p->name);
	info->capacity = p->capacity; info->queue_count = p->count;
	info->total_count = p->total;
	k_unlock();
	return B_OK;
}
