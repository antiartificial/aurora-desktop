#ifndef KITS_INTERNAL_H
#define KITS_INTERNAL_H
#include <pthread.h>
#include <time.h>
#include <OS.h>

/* Cross-process state: one file in /dev/shm per namespace. */
#define K_MAX_PORTS 128
#define K_MAX_AREAS 512
#define K_RING (256 * 1024) /* per-port byte ring; bounds one message */

typedef struct kport {
	int32 id;                 /* 0 = free */
	team_id team;
	char name[B_OS_NAME_LENGTH];
	int32 capacity, count, total, closed;
	uint32 head, used;        /* ring offsets, bytes */
	pthread_cond_t rd, wr;
} kport;

typedef struct karea {
	int32 id;                 /* 0 = free */
	team_id team;             /* process holding fd */
	int32 fd;                 /* fd number inside that process */
	char name[B_OS_NAME_LENGTH];
	uint64 size;
	uint32 protection;
} karea;

typedef struct kshm {
	uint32 magic;
	pthread_mutex_t lock;
	uint32 port_gen, area_gen;
	kport ports[K_MAX_PORTS];
	karea areas[K_MAX_AREAS];
} kshm;

extern kshm *K;
extern char *K_rings;         /* K_MAX_PORTS rings of K_RING bytes */

void k_init(void);            /* idempotent; aborts on failure */
void k_lock(void);
void k_unlock(void);

/* deadline helper shared by ports and semaphores.
 * returns 0 = wait forever, 1 = *ts holds an absolute CLOCK_MONOTONIC
 * deadline, or a negative status (B_WOULD_BLOCK / B_TIMED_OUT) if the call
 * must not wait at all. */
int  k_deadline(uint32 flags, bigtime_t timeout, struct timespec *ts);
/* wait on c with m held; B_OK on wake, B_TIMED_OUT on deadline. */
status_t k_wait(pthread_cond_t *c, pthread_mutex_t *m, int has, const struct timespec *ts);
void k_cond_init(pthread_cond_t *c);
int  k_pid_dead(team_id team);
#endif
