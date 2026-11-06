/* Force-included. On Haiku, <sys/types.h> (headers/posix) pulls in config/types.h; glibc's does not. */
#include <config/types.h>
/* Haiku's headers/posix/sys/types.h supplies these; glibc does not. */
typedef __haiku_addr_t addr_t;
typedef __haiku_saddr_t saddr_t;
/* VOS's StorageDefs.h uses XATTR_NAME_MAX, defined by linux/limits.h. */
#include <linux/limits.h>
/* Roster.h uses B_MIME_TYPE_LENGTH without including StorageDefs.h; on Haiku an earlier include supplies it. */
#include <StorageDefs.h>
