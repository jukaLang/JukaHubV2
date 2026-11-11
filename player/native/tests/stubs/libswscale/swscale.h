/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
#ifndef STUB_SWSCALE_H
#define STUB_SWSCALE_H
#include <stdint.h>
#include "../libavutil/pixfmt.h"
#define SWS_FAST_BILINEAR 1
struct SwsContext;
typedef struct SwsFilter SwsFilter;
struct SwsContext *sws_getCachedContext(struct SwsContext *context, int srcW, int srcH, enum AVPixelFormat srcFormat,
                                        int dstW, int dstH, enum AVPixelFormat dstFormat, int flags,
                                        SwsFilter *srcFilter, SwsFilter *dstFilter, const double *param);
int sws_scale(struct SwsContext *c, const uint8_t *const srcSlice[], const int srcStride[], int srcSliceY, int srcSliceH,
              uint8_t *const dst[], const int dstStride[]);
void sws_freeContext(struct SwsContext *swsContext);
#endif
