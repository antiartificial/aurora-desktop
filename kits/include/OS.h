/*
 * Kernel-kit primitives on Linux userspace.
 * Signatures follow Haiku's MIT-licensed public API (headers/os/kernel/OS.h);
 * the implementation is original. Only the subset in kits/README.md exists.
 */
#ifndef _OS_H
#define _OS_H
#include <SupportDefs.h>
#include <Errors.h>

#ifdef __cplusplus
extern "C" {
#endif

#define B_OS_NAME_LENGTH   32
#define B_PAGE_SIZE        4096
#define B_INFINITE_TIMEOUT (9223372036854775807LL)

enum {
	B_TIMEOUT = 0x8, B_RELATIVE_TIMEOUT = 0x8, B_ABSOLUTE_TIMEOUT = 0x10,
	B_TIMEOUT_REAL_TIME_BASE = 0x40,
	B_ABSOLUTE_REAL_TIME_TIMEOUT = B_ABSOLUTE_TIMEOUT | B_TIMEOUT_REAL_TIME_BASE
};

/* time */
bigtime_t system_time(void);
status_t  snooze(bigtime_t microseconds);

/* areas */
typedef struct area_info {
	area_id area; char name[B_OS_NAME_LENGTH]; size_t size; uint32 lock;
	uint32 protection; team_id team; uint32 ram_size; uint32 copy_count;
	uint32 in_count; uint32 out_count; void *address;
} area_info;

#define B_NO_LOCK 0
#define B_LAZY_LOCK 1
#define B_FULL_LOCK 2
#define B_ANY_ADDRESS 0
#define B_EXACT_ADDRESS 1
#define B_BASE_ADDRESS 2
#define B_CLONE_ADDRESS 3
#define B_READ_AREA (1 << 0)
#define B_WRITE_AREA (1 << 1)
#define B_EXECUTE_AREA (1 << 2)

area_id  create_area(const char *name, void **address, uint32 addressSpec,
             size_t size, uint32 lock, uint32 protection);
area_id  clone_area(const char *name, void **address, uint32 addressSpec,
             uint32 protection, area_id sourceArea);
status_t delete_area(area_id area);
status_t set_area_protection(area_id area, uint32 newProtection);
status_t _get_area_info(area_id area, area_info *info, size_t size);
#define get_area_info(a, i) _get_area_info((a), (i), sizeof(*(i)))

/* Aurora extensions: the bus's SCM_RIGHTS path (see kits/README.md). */
int      area_export_fd(area_id area);       /* dup'd memfd, caller closes */
area_id  clone_area_fd(const char *name, void **address, uint32 addressSpec,
             uint32 protection, int memfd);  /* requires GROW|SHRINK seals */

/* ports */
typedef struct port_info {
	port_id port; team_id team; char name[B_OS_NAME_LENGTH];
	int32 capacity; int32 queue_count; int32 total_count;
} port_info;

port_id  create_port(int32 capacity, const char *name);
port_id  find_port(const char *name);
ssize_t  read_port(port_id port, int32 *code, void *buffer, size_t bufferSize);
ssize_t  read_port_etc(port_id port, int32 *code, void *buffer,
             size_t bufferSize, uint32 flags, bigtime_t timeout);
status_t write_port(port_id port, int32 code, const void *buffer, size_t size);
status_t write_port_etc(port_id port, int32 code, const void *buffer,
             size_t bufferSize, uint32 flags, bigtime_t timeout);
status_t close_port(port_id port);
status_t delete_port(port_id port);
ssize_t  port_buffer_size(port_id port);
ssize_t  port_buffer_size_etc(port_id port, uint32 flags, bigtime_t timeout);
ssize_t  port_count(port_id port);
status_t set_port_owner(port_id port, team_id team);
status_t _get_port_info(port_id port, port_info *info, size_t size);
#define get_port_info(p, i) _get_port_info((p), (i), sizeof(*(i)))

/* semaphores */
enum { B_CAN_INTERRUPT = 0x01, B_DO_NOT_RESCHEDULE = 0x02,
       B_RELEASE_ALL = 0x08, B_RELEASE_IF_WAITING_ONLY = 0x10 };

sem_id   create_sem(int32 threadCount, const char *name);
status_t delete_sem(sem_id sem);
status_t acquire_sem(sem_id sem);
status_t acquire_sem_etc(sem_id sem, int32 count, uint32 flags,
             bigtime_t timeout);
status_t release_sem(sem_id sem);
status_t release_sem_etc(sem_id sem, int32 count, uint32 flags);
status_t get_sem_count(sem_id sem, int32 *threadCount);

/* threads */
typedef status_t (*thread_func)(void *);
#define B_LOW_PRIORITY 5
#define B_NORMAL_PRIORITY 10
#define B_DISPLAY_PRIORITY 15

thread_id spawn_thread(thread_func func, const char *name, int32 priority,
              void *data);
status_t  resume_thread(thread_id thread);
thread_id find_thread(const char *name);   /* NULL = calling thread */
status_t  wait_for_thread(thread_id thread, status_t *returnValue);

/* Aurora extension: remove this process's namespace file (tests/demos). */
void _kits_unlink_namespace(void);

#ifdef __cplusplus
}
#endif
#endif
