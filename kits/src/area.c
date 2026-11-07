#define _GNU_SOURCE
#include "kits_internal.h"
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#ifndef F_SEAL_SEAL
#include <linux/fcntl.h>
#endif

/* Local view of areas this process has mapped. Global view lives in K->areas. */
typedef struct larea { area_id id; int fd; void *addr; size_t size; uint32 prot; } larea;
static larea loc[K_MAX_AREAS];
static pthread_mutex_t loc_lock = PTHREAD_MUTEX_INITIALIZER;

static int to_prot(uint32 p)
{
	return (p & B_READ_AREA ? PROT_READ : 0) | (p & B_WRITE_AREA ? PROT_WRITE : 0)
		| (p & B_EXECUTE_AREA ? PROT_EXEC : 0);
}

static karea *klookup(area_id id)
{
	if (id <= 0) return NULL;
	karea *a = &K->areas[(id - 1) % K_MAX_AREAS];
	return a->id == id ? a : NULL;
}
static larea *llookup(area_id id)
{
	for (int i = 0; i < K_MAX_AREAS; i++)
		if (loc[i].id == id) return &loc[i];
	return NULL;
}

static void *map_it(void **addr, uint32 spec, size_t size, int prot, int fd)
{
	void *hint = NULL;
	int flags = MAP_SHARED;
	if (spec == B_EXACT_ADDRESS) { hint = *addr; flags |= MAP_FIXED_NOREPLACE; }
	else if (spec == B_BASE_ADDRESS) hint = *addr;
	return mmap(hint, size, prot ? prot : PROT_NONE, flags, fd, 0);
}

/* Register fd+mapping in both tables. Takes ownership of fd on success. */
static area_id adopt(const char *name, void **addr, uint32 spec, size_t size,
	uint32 protection, int fd)
{
	void *m = map_it(addr, spec, size, to_prot(protection), fd);
	if (m == MAP_FAILED) return errno == ENOMEM ? B_NO_MEMORY : B_BAD_VALUE;
	k_lock();
	int slot = -1;
	for (int pass = 0; pass < 2 && slot < 0; pass++)
		for (int i = 0; i < K_MAX_AREAS; i++) {
			karea *a = &K->areas[i];
			if (pass == 1 && a->id && k_owner_gone(a->team, a->team_start)) a->id = 0;
			if (!a->id) { slot = i; break; }
		}
	if (slot < 0) { k_unlock(); munmap(m, size); return B_NO_MORE_AREAS; }
	karea *a = &K->areas[slot];
	a->id = (int32)(K->area_gen++ % 4000000u) * K_MAX_AREAS + slot + 1;
	a->team = getpid(); a->team_start = k_pid_start(a->team); a->fd = fd; a->size = size; a->protection = protection;
	snprintf(a->name, sizeof a->name, "%s", name ? name : "unnamed area");
	area_id id = a->id;
	k_unlock();
	pthread_mutex_lock(&loc_lock);
	larea *l = &loc[slot];
	l->id = id; l->fd = fd; l->addr = m; l->size = size; l->prot = protection;
	pthread_mutex_unlock(&loc_lock);
	*addr = m;
	return id;
}

area_id create_area(const char *name, void **address, uint32 spec, size_t size,
	uint32 lock, uint32 protection)
{
	(void)lock; /* all memfd pages are demand-allocated; locking is a no-op */
	if (!address || size == 0) return B_BAD_VALUE;
	size = (size + B_PAGE_SIZE - 1) & ~(size_t)(B_PAGE_SIZE - 1);
	int fd = memfd_create(name ? name : "area", MFD_CLOEXEC | MFD_ALLOW_SEALING);
	if (fd < 0) return B_NO_MEMORY;
	if (ftruncate(fd, (off_t)size) < 0
		|| fcntl(fd, F_ADD_SEALS, F_SEAL_SHRINK | F_SEAL_GROW) < 0) {
		close(fd);
		return B_NO_MEMORY;
	}
	area_id id = adopt(name, address, spec, size, protection, fd);
	if (id < 0) close(fd);
	return id;
}

area_id clone_area_fd(const char *name, void **address, uint32 spec,
	uint32 protection, int memfd)
{
	if (!address || memfd < 0) return B_BAD_VALUE;
	int seals = fcntl(memfd, F_GET_SEALS);
	struct stat st;
	if (seals < 0 || (seals & (F_SEAL_GROW | F_SEAL_SHRINK)) != (F_SEAL_GROW | F_SEAL_SHRINK)
		|| fstat(memfd, &st) < 0 || st.st_size <= 0)
		return B_BAD_VALUE; /* not a sealed memfd: refuse, as the bus does */
	int fd = fcntl(memfd, F_DUPFD_CLOEXEC, 3);
	if (fd < 0) return B_NO_MEMORY;
	area_id id = adopt(name, address, spec, (size_t)st.st_size, protection, fd);
	if (id < 0) close(fd);
	return id;
}

area_id clone_area(const char *name, void **address, uint32 spec,
	uint32 protection, area_id source)
{
	k_init();
	k_lock();
	karea *a = klookup(source);
	team_id team = a ? a->team : 0;
	int32 fd = a ? a->fd : -1;
	k_unlock();
	if (!a) return B_BAD_VALUE;
	char path[64];
	snprintf(path, sizeof path, "/proc/%d/fd/%d", team, fd);
	/* Reopening the memfd through procfs reaches the same inode (and seals)
	 * from an unrelated process of the same uid. */
	int nfd = open(path, O_RDWR | O_CLOEXEC);
	if (nfd < 0) return B_BAD_VALUE;
	area_id id = clone_area_fd(name, address, spec, protection, nfd);
	close(nfd);
	return id;
}

int area_export_fd(area_id id)
{
	pthread_mutex_lock(&loc_lock);
	larea *l = llookup(id);
	int fd = l ? fcntl(l->fd, F_DUPFD_CLOEXEC, 3) : -1;
	pthread_mutex_unlock(&loc_lock);
	return fd;
}

status_t delete_area(area_id id)
{
	pthread_mutex_lock(&loc_lock);
	larea *l = llookup(id);
	if (!l) { pthread_mutex_unlock(&loc_lock); return B_BAD_VALUE; }
	larea copy = *l;
	l->id = 0;
	pthread_mutex_unlock(&loc_lock);
	munmap(copy.addr, copy.size);
	close(copy.fd);
	k_lock();
	karea *a = klookup(id);
	if (a) a->id = 0;
	k_unlock();
	return B_OK;
}

status_t set_area_protection(area_id id, uint32 prot)
{
	pthread_mutex_lock(&loc_lock);
	larea *l = llookup(id);
	status_t st = B_BAD_VALUE; /* only this process's mapping can change */
	if (l && mprotect(l->addr, l->size, to_prot(prot) ? to_prot(prot) : PROT_NONE) == 0) {
		l->prot = prot;
		st = B_OK;
	}
	pthread_mutex_unlock(&loc_lock);
	if (st == B_OK) {
		k_lock();
		karea *a = klookup(id);
		if (a) a->protection = prot;
		k_unlock();
	}
	return st;
}

status_t _get_area_info(area_id id, area_info *info, size_t size)
{
	if (!info || size < sizeof *info) return B_BAD_VALUE;
	k_init();
	k_lock();
	karea *a = klookup(id);
	if (!a) { k_unlock(); return B_BAD_VALUE; }
	memset(info, 0, sizeof *info);
	info->area = id; info->team = a->team; info->size = a->size;
	info->protection = a->protection; info->lock = B_NO_LOCK;
	snprintf(info->name, sizeof info->name, "%s", a->name);
	k_unlock();
	pthread_mutex_lock(&loc_lock);
	larea *l = llookup(id);
	if (l) { info->address = l->addr; info->protection = l->prot; }
	pthread_mutex_unlock(&loc_lock);
	return B_OK;
}
