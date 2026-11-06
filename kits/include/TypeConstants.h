/* Type codes as Haiku's TypeConstants.h names them (four-character constants). */
#ifndef _TYPE_CONSTANTS_H
#define _TYPE_CONSTANTS_H
#include <SupportDefs.h>
typedef uint32 type_code;
enum {
	B_ANY_TYPE = 'ANYT', B_BOOL_TYPE = 'BOOL', B_DOUBLE_TYPE = 'DBLE',
	B_FLOAT_TYPE = 'FLOT', B_INT8_TYPE = 'BYTE', B_INT16_TYPE = 'SHRT',
	B_INT32_TYPE = 'LONG', B_INT64_TYPE = 'LLNG', B_MESSAGE_TYPE = 'MSGG',
	B_RAW_TYPE = 'RAWT', B_STRING_TYPE = 'CSTR', B_UINT32_TYPE = 'ULNG'
};
#endif
