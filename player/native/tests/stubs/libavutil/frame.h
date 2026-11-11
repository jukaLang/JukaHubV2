/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
#ifndef STUB_AVFRAME_H
#define STUB_AVFRAME_H
#include <stdint.h>
#define AV_NUM_DATA_POINTERS 8
typedef struct AVFrame { uint8_t *data[AV_NUM_DATA_POINTERS]; int linesize[AV_NUM_DATA_POINTERS]; int width, height; int format; } AVFrame;
AVFrame *av_frame_alloc(void);
void av_frame_free(AVFrame **frame);
int av_frame_get_buffer(AVFrame *frame, int align);
int av_frame_make_writable(AVFrame *frame);
#endif
