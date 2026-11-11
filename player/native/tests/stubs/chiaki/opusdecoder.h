/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
#ifndef STUB_CHIAKI_OPUS_H
#define STUB_CHIAKI_OPUS_H
#include "session.h"
typedef void (*ChiakiOpusDecoderSettingsCallback)(uint32_t channels, uint32_t rate, void *user);
typedef void (*ChiakiOpusDecoderFrameCallback)(int16_t *buf, size_t samples_count, void *user);
typedef struct { ChiakiLog *log; char opaque[256]; } ChiakiOpusDecoder;
CHIAKI_EXPORT void chiaki_opus_decoder_init(ChiakiOpusDecoder *decoder, ChiakiLog *log);
CHIAKI_EXPORT void chiaki_opus_decoder_fini(ChiakiOpusDecoder *decoder);
CHIAKI_EXPORT void chiaki_opus_decoder_set_cb(ChiakiOpusDecoder *decoder, ChiakiOpusDecoderSettingsCallback settings_cb,
                                              ChiakiOpusDecoderFrameCallback frame_cb, void *user);
CHIAKI_EXPORT void chiaki_opus_decoder_get_sink(ChiakiOpusDecoder *decoder, ChiakiAudioSink *sink);
#endif
