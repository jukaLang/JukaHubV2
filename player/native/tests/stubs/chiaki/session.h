/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
#ifndef STUB_CHIAKI_SESSION_H
#define STUB_CHIAKI_SESSION_H
#include "common.h"
#include "controller.h"
#include "log.h"
#define CHIAKI_SESSION_AUTH_SIZE 0x10
typedef enum { CHIAKI_QUIT_REASON_NONE, CHIAKI_QUIT_REASON_STOPPED } ChiakiQuitReason;
CHIAKI_EXPORT const char *chiaki_quit_reason_string(ChiakiQuitReason reason);
CHIAKI_EXPORT bool chiaki_quit_reason_is_error(ChiakiQuitReason reason);
typedef struct { unsigned int width, height, max_fps, bitrate; ChiakiCodec codec; } ChiakiConnectVideoProfile;
typedef struct chiaki_connect_info_t {
    bool ps5; const char *host; char regist_key[CHIAKI_SESSION_AUTH_SIZE]; uint8_t morning[0x10];
    ChiakiConnectVideoProfile video_profile; bool video_profile_auto_downgrade; bool enable_keyboard;
    bool enable_dualsense; double packet_loss_max; bool enable_idr_on_fec_failure;
} ChiakiConnectInfo;
typedef struct { ChiakiQuitReason reason; const char *reason_str; } ChiakiQuitEvent;
typedef struct { bool pin_incorrect; } ChiakiEventLoginPinRequest;
typedef struct { uint8_t unknown; uint8_t left; uint8_t right; } ChiakiEventRumble;
typedef struct { int32_t frame_index; bool idr_request_sent; } ChiakiEventVideoFecFailure;
typedef enum { CHIAKI_EVENT_CONNECTED, CHIAKI_EVENT_LOGIN_PIN_REQUEST, CHIAKI_EVENT_KEYBOARD_OPEN,
               CHIAKI_EVENT_RUMBLE, CHIAKI_EVENT_QUIT, CHIAKI_EVENT_VIDEO_FEC_FAILURE } ChiakiEventType;
typedef struct chiaki_event_t {
    ChiakiEventType type;
    union { ChiakiQuitEvent quit; ChiakiEventLoginPinRequest login_pin_request; ChiakiEventRumble rumble;
            ChiakiEventVideoFecFailure video_fec_failure; };
} ChiakiEvent;
typedef void (*ChiakiEventCallback)(ChiakiEvent *event, void *user);
typedef bool (*ChiakiVideoSampleCallback)(uint8_t *buf, size_t buf_size, int32_t frames_lost, bool frame_recovered, void *user);
typedef struct { void *user; void (*header_cb)(void *, void *); void (*frame_cb)(uint8_t *, size_t, void *); } ChiakiAudioSink;
typedef struct chiaki_session_t { ChiakiTarget target; char opaque[4096]; } ChiakiSession;
CHIAKI_EXPORT ChiakiErrorCode chiaki_session_init(ChiakiSession *session, ChiakiConnectInfo *connect_info, ChiakiLog *log);
CHIAKI_EXPORT void chiaki_session_fini(ChiakiSession *session);
CHIAKI_EXPORT ChiakiErrorCode chiaki_session_start(ChiakiSession *session);
CHIAKI_EXPORT ChiakiErrorCode chiaki_session_stop(ChiakiSession *session);
CHIAKI_EXPORT ChiakiErrorCode chiaki_session_join(ChiakiSession *session);
CHIAKI_EXPORT ChiakiErrorCode chiaki_session_set_controller_state(ChiakiSession *session, ChiakiControllerState *state);
CHIAKI_EXPORT ChiakiErrorCode chiaki_session_set_login_pin(ChiakiSession *session, const uint8_t *pin, size_t pin_size);
CHIAKI_EXPORT void chiaki_session_set_event_cb(ChiakiSession *session, ChiakiEventCallback cb, void *user);
CHIAKI_EXPORT void chiaki_session_set_video_sample_cb(ChiakiSession *session, ChiakiVideoSampleCallback cb, void *user);
CHIAKI_EXPORT void chiaki_session_set_audio_sink(ChiakiSession *session, ChiakiAudioSink *sink);
#endif
