/* Error codes with the same names and values as Haiku's public Errors.h. */
#ifndef _ERRORS_H
#define _ERRORS_H
#include <errno.h>
#include <limits.h>

#define B_GENERAL_ERROR_BASE INT_MIN
#define B_OS_ERROR_BASE      (B_GENERAL_ERROR_BASE + 0x2000)

#define B_NO_ERROR        0
#define B_OK              0
#define B_ERROR           (-1)

#define B_NO_MEMORY       (-ENOMEM)
#define B_BAD_VALUE       (-EINVAL)
#define B_TIMED_OUT       (-ETIMEDOUT)
#define B_INTERRUPTED     (-EINTR)
#define B_WOULD_BLOCK     (-EAGAIN)
#define B_NO_INIT         (-ENXIO)
#define B_BAD_ADDRESS     (-EFAULT)
#define B_BAD_TEAM_ID     (-ESRCH)
#define B_NO_MORE_PORTS   (-ENFILE)
#define B_NAME_NOT_FOUND  (B_GENERAL_ERROR_BASE + 9)
#define B_BAD_INDEX       (B_GENERAL_ERROR_BASE + 5)
#define B_BAD_TYPE        (B_GENERAL_ERROR_BASE + 6)
#define B_MISMATCHED_VALUES (B_GENERAL_ERROR_BASE + 8)
#define B_NAME_IN_USE     (B_GENERAL_ERROR_BASE + 10)
#define B_NOT_ALLOWED     (B_GENERAL_ERROR_BASE + 16)
#define B_BAD_DATA        (-EILSEQ)
#define B_BAD_REPLY       (-EBADMSG)

#define B_BAD_SEM_ID      (B_OS_ERROR_BASE + 1)
#define B_NO_MORE_SEMS    (B_OS_ERROR_BASE + 2)
#define B_BAD_THREAD_ID   (B_OS_ERROR_BASE + 3)
#define B_NO_MORE_THREADS (B_OS_ERROR_BASE + 4)
#define B_BAD_THREAD_STATE (B_OS_ERROR_BASE + 5)
#define B_BAD_PORT_ID     (B_OS_ERROR_BASE + 8)
#define B_NO_MORE_AREAS   (-ENOSPC)
#endif
