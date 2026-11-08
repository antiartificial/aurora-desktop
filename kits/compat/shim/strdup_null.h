// Force-included into every Interface Kit source (see CMakeLists.txt). Haiku's libroot answers strdup(NULL) with NULL;
// glibc's strdup crashes on it. BSlider::UpdateTextChanged() does strdup(UpdateText()) and UpdateText() returns NULL by
// default, so a plain BSlider took the whole process down the moment it was attached to a window.
#ifndef AURORA_STRDUP_NULL_H
#define AURORA_STRDUP_NULL_H
#include <string.h>
#ifdef __cplusplus
static inline char* aurora_strdup_null(const char* s) { return s ? strdup(s) : (char*)0; }
#define strdup(s) aurora_strdup_null(s)
#endif
#endif
