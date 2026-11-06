#define _GNU_SOURCE
#include "kits_internal.h"
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#define K_MAGIC 0x41555231u
kshm *K;
char *K_rings;
static pthread_once_t once = PTHREAD_ONCE_INIT;
static char path[256];

static size_t hdr_size(void) { return (sizeof(kshm) + 4095) & ~(size_t)4095; }

static void make_path(void)
{
	const char *dir = access("/dev/shm", W_OK) == 0 ? "/dev/shm" : "/tmp";
	const char *ns = getenv("AURORA_KITS_NS");
	snprintf(path, sizeof path, "%s/aurora-kits-%u-%s", dir, (unsigned)getuid(),
		ns && *ns ? ns : "default");
}

void k_cond_init(pthread_cond_t *c)
{
	pthread_condattr_t a;
	pthread_condattr_init(&a);
	pthread_condattr_setpshared(&a, PTHREAD_PROCESS_SHARED);
	pthread_condattr_setclock(&a, CLOCK_MONOTONIC);
	pthread_cond_init(c, &a);
	pthread_condattr_destroy(&a);
}

static void do_init(void)
{
	make_path();
	int fd = open(path, O_RDWR | O_CREAT | O_CLOEXEC, 0600);
	if (fd < 0) { perror("kits: open shm"); abort(); }
	flock(fd, LOCK_EX); /* serialises first-time initialisation */
	size_t total = hdr_size() + (size_t)K_MAX_PORTS * K_RING;
	struct stat st;
	fstat(fd, &st);
	int fresh = st.st_size == 0;
	if (fresh && ftruncate(fd, (off_t)total) < 0) { perror("kits: ftruncate"); abort(); }
	void *m = mmap(NULL, total, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (m == MAP_FAILED) { perror("kits: mmap"); abort(); }
	K = m;
	K_rings = (char *)m + hdr_size();
	if (fresh || K->magic != K_MAGIC) {
		memset(K, 0, sizeof *K);
		pthread_mutexattr_t a;
		pthread_mutexattr_init(&a);
		pthread_mutexattr_setpshared(&a, PTHREAD_PROCESS_SHARED);
		pthread_mutexattr_setrobust(&a, PTHREAD_MUTEX_ROBUST);
		pthread_mutex_init(&K->lock, &a);
		pthread_mutexattr_destroy(&a);
		for (int i = 0; i < K_MAX_PORTS; i++) {
			k_cond_init(&K->ports[i].rd);
			k_cond_init(&K->ports[i].wr);
		}
		K->magic = K_MAGIC;
	}
	flock(fd, LOCK_UN);
	close(fd); /* mapping keeps the file alive */
}

void k_init(void) { pthread_once(&once, do_init); }

void k_lock(void)
{
	k_init();
	int rc = pthread_mutex_lock(&K->lock);
	if (rc == EOWNERDEAD) pthread_mutex_consistent(&K->lock);
}
void k_unlock(void) { pthread_mutex_unlock(&K->lock); }

void _kits_unlink_namespace(void) { k_init(); unlink(path); }

int k_pid_dead(team_id team)
{
	return team > 0 && kill(team, 0) < 0 && errno == ESRCH;
}

bigtime_t system_time(void)
{
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return (bigtime_t)t.tv_sec * 1000000 + t.tv_nsec / 1000;
}

status_t snooze(bigtime_t us)
{
	if (us <= 0) return B_OK;
	struct timespec t = { us / 1000000, (us % 1000000) * 1000 };
	while (clock_nanosleep(CLOCK_MONOTONIC, 0, &t, &t) == EINTR) {}
	return B_OK;
}

int k_deadline(uint32 flags, bigtime_t timeout, struct timespec *ts)
{
	if (!(flags & (B_RELATIVE_TIMEOUT | B_ABSOLUTE_TIMEOUT))
		|| timeout == B_INFINITE_TIMEOUT)
		return 0;
	bigtime_t when;
	if (flags & B_ABSOLUTE_TIMEOUT) {
		if (flags & B_TIMEOUT_REAL_TIME_BASE) { /* real-time base -> monotonic */
			struct timespec r;
			clock_gettime(CLOCK_REALTIME, &r);
			when = timeout - ((bigtime_t)r.tv_sec * 1000000 + r.tv_nsec / 1000)
				+ system_time();
		} else
			when = timeout;
		if (when <= system_time()) return B_TIMED_OUT;
	} else {
		if (timeout <= 0) return B_WOULD_BLOCK;
		when = system_time() + timeout;
	}
	ts->tv_sec = when / 1000000;
	ts->tv_nsec = (when % 1000000) * 1000;
	return 1;
}

status_t k_wait(pthread_cond_t *c, pthread_mutex_t *m, int has, const struct timespec *ts)
{
	int rc = has ? pthread_cond_timedwait(c, m, ts) : pthread_cond_wait(c, m);
	if (rc == EOWNERDEAD) { pthread_mutex_consistent(m); rc = 0; }
	return rc == ETIMEDOUT ? B_TIMED_OUT : B_OK;
}
