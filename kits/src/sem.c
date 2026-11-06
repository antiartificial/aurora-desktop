#include "kits_internal.h"
#include <stdio.h>
#include <string.h>

/* Process-local, like the sem users in Looper.cpp. One lock, a cond per sem. */
#define MAX_SEMS 4096
typedef struct ksem {
	sem_id id; int32 count, waiters, want;
	pthread_cond_t c; char name[B_OS_NAME_LENGTH];
} ksem;
static ksem sems[MAX_SEMS];
static pthread_mutex_t sl = PTHREAD_MUTEX_INITIALIZER;
static uint32 gen;

static ksem *lookup(sem_id id)
{
	if (id <= 0) return NULL;
	ksem *s = &sems[(id - 1) % MAX_SEMS];
	return s->id == id ? s : NULL;
}

sem_id create_sem(int32 count, const char *name)
{
	if (count < 0) return B_BAD_VALUE;
	pthread_mutex_lock(&sl);
	for (int i = 0; i < MAX_SEMS; i++) {
		ksem *s = &sems[i];
		if (s->id) continue;
		static int inited[MAX_SEMS];
		if (!inited[i]) {
			pthread_condattr_t a;
			pthread_condattr_init(&a);
			pthread_condattr_setclock(&a, CLOCK_MONOTONIC);
			pthread_cond_init(&s->c, &a);
			pthread_condattr_destroy(&a);
			inited[i] = 1;
		}
		s->id = (int32)(gen++ % 500000u) * MAX_SEMS + i + 1;
		s->count = count; s->waiters = s->want = 0;
		snprintf(s->name, sizeof s->name, "%s", name ? name : "unnamed sem");
		sem_id id = s->id;
		pthread_mutex_unlock(&sl);
		return id;
	}
	pthread_mutex_unlock(&sl);
	return B_NO_MORE_SEMS;
}

status_t delete_sem(sem_id id)
{
	pthread_mutex_lock(&sl);
	ksem *s = lookup(id);
	if (s) { s->id = 0; pthread_cond_broadcast(&s->c); }
	pthread_mutex_unlock(&sl);
	return s ? B_OK : B_BAD_SEM_ID;
}

status_t acquire_sem_etc(sem_id id, int32 n, uint32 flags, bigtime_t timeout)
{
	if (n < 1) return B_BAD_VALUE;
	struct timespec ts;
	pthread_mutex_lock(&sl);
	int d = k_deadline(flags, timeout, &ts);
	ksem *s = lookup(id);
	status_t st = B_OK;
	if (!s) st = B_BAD_SEM_ID;
	else {
		s->waiters++; s->want += n;
		for (;;) {
			if (s->count >= n) { s->count -= n; break; }
			if (d < 0) { st = d; break; }
			int rc = d > 0 ? pthread_cond_timedwait(&s->c, &sl, &ts)
			               : pthread_cond_wait(&s->c, &sl);
			if (s->id != id) { st = B_BAD_SEM_ID; break; }
			if (s->count >= n) { s->count -= n; break; }
			if (rc) { st = B_TIMED_OUT; break; }
		}
		s->waiters--; s->want -= n;
	}
	pthread_mutex_unlock(&sl);
	return st;
}

status_t acquire_sem(sem_id id) { return acquire_sem_etc(id, 1, 0, 0); }

status_t release_sem_etc(sem_id id, int32 n, uint32 flags)
{
	if (n < 1 && !(flags & B_RELEASE_ALL)) return B_BAD_VALUE;
	pthread_mutex_lock(&sl);
	ksem *s = lookup(id);
	if (!s) { pthread_mutex_unlock(&sl); return B_BAD_SEM_ID; }
	if ((flags & B_RELEASE_IF_WAITING_ONLY) && s->waiters == 0) {
		pthread_mutex_unlock(&sl);
		return B_OK;
	}
	/* approximation: RELEASE_ALL grants exactly what current waiters want */
	s->count += (flags & B_RELEASE_ALL) ? s->want : n;
	pthread_cond_broadcast(&s->c);
	pthread_mutex_unlock(&sl);
	return B_OK;
}

status_t release_sem(sem_id id) { return release_sem_etc(id, 1, 0); }

status_t get_sem_count(sem_id id, int32 *out)
{
	pthread_mutex_lock(&sl);
	ksem *s = lookup(id);
	if (s && out) *out = s->count;
	pthread_mutex_unlock(&sl);
	return s ? B_OK : B_BAD_SEM_ID;
}
