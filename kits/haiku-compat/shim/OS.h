/* Wraps kits/include/OS.h. Haiku's real OS.h owns the id typedefs; ours keeps them in SupportDefs.h. */
#ifndef _AURORA_COMPAT_OS_H
#define _AURORA_COMPAT_OS_H
#include <SupportDefs.h>
typedef int32 area_id;
typedef int32 port_id;
typedef int32 sem_id;
typedef int32 team_id;
typedef int32 thread_id;
#include "prim/OS.h"
#include <sys/types.h>
#ifdef __cplusplus
extern "C" {
#endif
/* --- VOS extension (Vitruvian OS.h): vrefs, i.e. fds passed with messages. HOLLOW: see stubs.cpp --- */
typedef int32 vref_id;
extern vref_id create_vref(int fd);
extern status_t acquire_vref_etc(vref_id id, int* fd);
extern status_t acquire_vref(vref_id id);
extern int open_vref(vref_id id);
extern status_t release_vref(vref_id id);
extern dev_t get_vref_dev();
/* --- Haiku OS.h: per-message sender info. HOLLOW: our ports record no sender --- */
typedef struct port_message_info {
	size_t size; uid_t sender; gid_t sender_group; team_id sender_team;
} port_message_info;
extern status_t _get_port_message_info_etc(port_id port, port_message_info *info,
	size_t infoSize, uint32 flags, bigtime_t timeout);
#define get_port_message_info_etc(port, info, flags, timeout) \
	_get_port_message_info_etc((port), (info), sizeof(*(info)), flags, timeout)
#ifdef __cplusplus
}
#endif
#ifdef __cplusplus
extern "C" {
#endif
/* --- Haiku OS.h pieces our reduced OS.h lacks. Declarations; stubs.cpp says which are HOLLOW --- */
#define B_SYSTEM_TEAM 1
typedef enum { B_THREAD_RUNNING = 1, B_THREAD_READY, B_THREAD_RECEIVING, B_THREAD_ASLEEP,
	B_THREAD_SUSPENDED, B_THREAD_WAITING, B_THREAD_SPAWNED } thread_state;
typedef struct {
	thread_id thread; team_id team; char name[B_OS_NAME_LENGTH]; thread_state state;
	int32 priority; sem_id sem; bigtime_t user_time; bigtime_t kernel_time;
	void *stack_base; void *stack_end;
} thread_info;
extern status_t get_thread_info(thread_id id, thread_info *info);
extern void exit_thread(status_t status);
extern void debugger(const char *message);
#ifdef __cplusplus
}
#endif
/* --- second batch: only needed to COMPILE Application.cpp / AppMisc.cpp / Roster.cpp (not linked) --- */
#ifdef __cplusplus
extern "C" {
#endif
#define B_CURRENT_TEAM 0
typedef struct {
	team_id team; int32 thread_count; int32 image_count; int32 area_count; thread_id debugger_nub_thread;
	port_id debugger_nub_port; int32 argc; char args[64]; uid_t uid; gid_t gid;
} team_info;
extern status_t _get_team_info(team_id id, team_info *info, size_t size);
#define get_team_info(id, info) _get_team_info((id), (info), sizeof(*(info)))
extern status_t kill_thread(thread_id thread);
extern void debug_printf(const char *format, ...);
#ifdef __cplusplus
}
#endif
/* --- third batch (Interface Kit): Haiku OS.h pieces View.cpp/Window.cpp/MenuBar.cpp/ScrollBar.cpp name. See kits/interface/ --- */
#ifdef __cplusplus
extern "C" {
#endif
#define B_SYSTEM_TIMEBASE 0
#ifndef B_CLONEABLE_AREA
#define B_CLONEABLE_AREA (1 << 8)
#endif
extern status_t snooze_until(bigtime_t time, int timeBase);
extern status_t rename_thread(thread_id thread, const char *newName);
extern status_t send_data(thread_id thread, int32 code, const void *buffer, size_t bufferSize);
extern int32 receive_data(thread_id *sender, void *buffer, size_t bufferSize);
extern bool has_data(thread_id thread);
#ifdef __cplusplus
}
#endif
#include <stdio.h>
#define UNIMPLEMENTED() printf("UNIMPLEMENTED %s\n", __PRETTY_FUNCTION__)  /* Vitruvian: headers/build/LinuxBuildCompatibility.h */
#endif
