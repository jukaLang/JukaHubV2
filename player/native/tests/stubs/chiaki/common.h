/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
/* STUB of chiaki-ng's public API, only for syntax checks of chiaki-stream. */
#ifndef STUB_CHIAKI_COMMON_H
#define STUB_CHIAKI_COMMON_H
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#define CHIAKI_EXPORT
typedef enum { CHIAKI_ERR_SUCCESS = 0, CHIAKI_ERR_UNKNOWN } ChiakiErrorCode;
CHIAKI_EXPORT const char *chiaki_error_string(ChiakiErrorCode code);
CHIAKI_EXPORT ChiakiErrorCode chiaki_lib_init(void);
typedef enum { CHIAKI_CODEC_H264 = 0, CHIAKI_CODEC_H265 = 1, CHIAKI_CODEC_H265_HDR = 2 } ChiakiCodec;
typedef enum { CHIAKI_TARGET_PS4_UNKNOWN = 0, CHIAKI_TARGET_PS4_8 = 800, CHIAKI_TARGET_PS4_9 = 900,
               CHIAKI_TARGET_PS4_10 = 1000, CHIAKI_TARGET_PS5_UNKNOWN = 1000000, CHIAKI_TARGET_PS5_1 = 1000100 } ChiakiTarget;
#endif
