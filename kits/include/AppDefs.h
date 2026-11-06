/* Message codes used by the app kit (names from Haiku's AppDefs.h). */
#ifndef _APP_DEFS_H
#define _APP_DEFS_H
#include <SupportDefs.h>
enum {
	B_ABOUT_REQUESTED = '_ABR', B_ARGV_RECEIVED = '_ARG', B_QUIT_REQUESTED = '_QRQ',
	B_READY_TO_RUN = '_RTR', B_REPLY = 'RPLY', B_NO_REPLY = '_NRP',
	B_MESSAGE_NOT_UNDERSTOOD = '_MNU'
};
#endif
