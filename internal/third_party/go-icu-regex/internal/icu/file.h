#include "unicode/regex.h"

#ifdef __cplusplus
extern "C" {
#endif

UChar* replace(URegularExpression* regexp, UChar* replacement, int replacementLen, UChar* original, int originalSize, int start, int occurrence, int* returnSize);


#ifdef __cplusplus
} // extern "C"
#endif
