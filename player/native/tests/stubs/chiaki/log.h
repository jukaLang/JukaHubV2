/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
#ifndef STUB_CHIAKI_LOG_H
#define STUB_CHIAKI_LOG_H
#include "common.h"
typedef enum { CHIAKI_LOG_DEBUG = 1, CHIAKI_LOG_VERBOSE = 2, CHIAKI_LOG_INFO = 4, CHIAKI_LOG_WARNING = 8, CHIAKI_LOG_ERROR = 16 } ChiakiLogLevel;
#define CHIAKI_LOG_ALL ((1 << 5) - 1)
typedef void (*ChiakiLogCb)(ChiakiLogLevel level, const char *msg, void *user);
typedef struct chiaki_log_t { uint32_t level_mask; ChiakiLogCb cb; void *user; } ChiakiLog;
CHIAKI_EXPORT void chiaki_log_init(ChiakiLog *log, uint32_t level_mask, ChiakiLogCb cb, void *user);
#endif
