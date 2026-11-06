#define _GNU_SOURCE
#include "kits_internal.h"
#include <stdio.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

#define MAX_THREADS 512
typedef struct kthread {
	int used, started, done;
	thread_id tid;
	status_t ret;
	thread_func fn; void *arg;
	char name[B_OS_NAME_LENGTH];
} kthread;
static kthread th[MAX_THREADS];
static pthread_mutex_t tl = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t tc = PTHREAD_COND_INITIALIZER;

static thread_id mytid(void) { return (thread_id)syscall(SYS_gettid); }

static void *entry(void *p)
{
	kthread *t = p;
	pthread_mutex_lock(&tl);
	t->tid = mytid();
	pthread_cond_broadcast(&tc);
	while (!t->started) pthread_cond_wait(&tc, &tl); /* BeOS: born suspended */
	pthread_mutex_unlock(&tl);
	status_t r = t->fn(t->arg);
	pthread_mutex_lock(&tl);
	t->ret = r; t->done = 1;
	pthread_cond_broadcast(&tc);
	pthread_mutex_unlock(&tl);
	return NULL;
}

thread_id spawn_thread(thread_func fn, const char *name, int32 prio, void *data)
{
	(void)prio; /* priorities are not mapped to the scheduler */
	if (!fn) return B_BAD_VALUE;
	pthread_mutex_lock(&tl);
	kthread *t = NULL;
	for (int i = 0; i < MAX_THREADS && !t; i++) if (!th[i].used) t = &th[i];
	for (int i = 0; i < MAX_THREADS && !t; i++) if (th[i].done) t = &th[i]; /* reap unwaited */
	if (!t) { pthread_mutex_unlock(&tl); return B_NO_MORE_THREADS; }
	memset(t, 0, sizeof *t);
	t->used = 1; t->fn = fn; t->arg = data;
	snprintf(t->name, sizeof t->name, "%s", name ? name : "unnamed thread");
	pthread_t pt;
	pthread_attr_t a;
	pthread_attr_init(&a);
	pthread_attr_setdetachstate(&a, PTHREAD_CREATE_DETACHED);
	int rc = pthread_create(&pt, &a, entry, t);
	pthread_attr_destroy(&a);
	if (rc) { t->used = 0; pthread_mutex_unlock(&tl); return B_NO_MEMORY; }
	pthread_setname_np(pt, t->name[0] ? t->name : "thread");
	while (!t->tid) pthread_cond_wait(&tc, &tl); /* ids are kernel tids */
	thread_id id = t->tid;
	pthread_mutex_unlock(&tl);
	return id;
}

static kthread *lookup(thread_id id)
{
	for (int i = 0; i < MAX_THREADS; i++)
		if (th[i].used && th[i].tid == id) return &th[i];
	return NULL;
}

status_t resume_thread(thread_id id)
{
	pthread_mutex_lock(&tl);
	kthread *t = lookup(id);
	if (t) { t->started = 1; pthread_cond_broadcast(&tc); }
	pthread_mutex_unlock(&tl);
	return t ? B_OK : B_BAD_THREAD_ID;
}

thread_id find_thread(const char *name)
{
	if (!name) return mytid();
	pthread_mutex_lock(&tl);
	thread_id r = B_NAME_NOT_FOUND;
	for (int i = 0; i < MAX_THREADS; i++)
		if (th[i].used && !th[i].done && !strncmp(th[i].name, name, B_OS_NAME_LENGTH - 1)) {
			r = th[i].tid;
			break;
		}
	pthread_mutex_unlock(&tl);
	return r;
}

status_t wait_for_thread(thread_id id, status_t *ret)
{
	pthread_mutex_lock(&tl);
	kthread *t = lookup(id);
	if (!t) { pthread_mutex_unlock(&tl); return B_BAD_THREAD_ID; }
	while (!t->done) pthread_cond_wait(&tc, &tl);
	if (ret) *ret = t->ret;
	t->used = 0;
	pthread_mutex_unlock(&tl);
	return B_OK;
}
