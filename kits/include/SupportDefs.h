/* Typedefs matching Haiku's public SupportDefs.h names (own implementation). */
#ifndef _SUPPORT_DEFS_H
#define _SUPPORT_DEFS_H
#include <stdint.h>
#include <stddef.h>
#include <sys/types.h>

typedef int8_t int8;   typedef uint8_t uint8;
typedef int16_t int16; typedef uint16_t uint16;
typedef int32_t int32; typedef uint32_t uint32;
typedef int64_t int64; typedef uint64_t uint64;

typedef int32 status_t;
typedef int64 bigtime_t;
typedef int64 nanotime_t;
typedef int32 area_id;
typedef int32 port_id;
typedef int32 sem_id;
typedef int32 team_id;
typedef int32 thread_id;
#endif
