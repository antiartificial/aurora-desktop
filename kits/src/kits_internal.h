#ifndef KITS_INTERNAL_H
#define KITS_INTERNAL_H
#include <pthread.h>
#include <time.h>
#include <OS.h>

/* Cross-process state: one file in /dev/shm per namespace. */
/* Ports are per-BWindow and per-looper, and a BApplication with a menu takes about ten, so 128
 * was a ceiling of TWELVE apps open at once. The thirteenth did not fail politely: Haiku's own
 * Window.cpp calls debugger() when it cannot make a port, so it aborted with a core dump and a
 * message about BWindow that says nothing about ports. Twelve is an ordinary number of windows
 * to have open.
 *
 * Raising it is nearly free. The region is ftruncate'd and mmap'd, so the rings are sparse and
 * cost real memory only where they are touched: measured at 152 KB for one app and +92 KB for
 * each one after, against an apparent size of 33 MB. At 512 the apparent size is 134 MB of
 * mostly-untouched address space, the physical cost per app is unchanged, and the ceiling is
 * about 51 apps. */
#define K_MAX_PORTS 512
#define K_MAX_AREAS 512
#define K_RING (256 * 1024) /* per-port byte ring; bounds one message */

typedef struct kport {
	int32 id;                 /* 0 = free */
	team_id team;
	uint64 team_start;        /* the owner's start time: tells a dead owner from a reused pid */
	char name[B_OS_NAME_LENGTH];
	int32 capacity, count, total, closed;
	uint32 head, used;        /* ring offsets, bytes */
	pthread_cond_t rd, wr;
} kport;

typedef struct karea {
	int32 id;                 /* 0 = free */
	team_id team;             /* process holding fd */
	uint64 team_start;        /* see kport.team_start */
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
/* The owner's start time, from /proc/<pid>/stat, or 0 when it cannot be read. A pid alone does
 * not identify a process: pids are reused, and a reused one makes a dead owner's slot look
 * occupied forever. */
uint64 k_pid_start(team_id team);
/* True when the process that took this slot is gone, INCLUDING the case where its pid now
 * belongs to something else. A slot whose start token was never readable falls back to the pid
 * test, which is what the code did before. */
int  k_owner_gone(team_id team, uint64 start);
#endif
