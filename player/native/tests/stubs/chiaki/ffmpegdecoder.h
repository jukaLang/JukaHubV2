/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
#ifndef STUB_CHIAKI_FFMPEG_H
#define STUB_CHIAKI_FFMPEG_H
#include "session.h"
#include <libavutil/frame.h>
typedef struct chiaki_ffmpeg_decoder_t ChiakiFfmpegDecoder;
typedef void (*ChiakiFfmpegFrameAvailable)(ChiakiFfmpegDecoder *decoder, void *user);
struct chiaki_ffmpeg_decoder_t { char opaque[512]; };
typedef struct { AVFrame *frame; } ChiakiFfmpegFrame;
/* 8 parameters: the call chiaki-stream.c has always made against the pinned commit. */
CHIAKI_EXPORT ChiakiErrorCode chiaki_ffmpeg_decoder_init(ChiakiFfmpegDecoder *decoder, ChiakiLog *log, ChiakiCodec codec,
    unsigned int max_fps, const char *hw_decoder_name, void *hw_device_ctx, ChiakiFfmpegFrameAvailable frame_available_cb,
    void *frame_available_cb_user);
CHIAKI_EXPORT void chiaki_ffmpeg_decoder_fini(ChiakiFfmpegDecoder *decoder);
CHIAKI_EXPORT bool chiaki_ffmpeg_decoder_video_sample_cb(uint8_t *buf, size_t buf_size, int32_t frames_lost, bool frame_recovered, void *user);
CHIAKI_EXPORT ChiakiFfmpegFrame chiaki_ffmpeg_decoder_pull_frame(ChiakiFfmpegDecoder *decoder, int32_t *frames_lost);
#endif
