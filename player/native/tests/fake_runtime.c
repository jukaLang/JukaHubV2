/* Scripted fakes of SDL2 / chiaki / FFmpeg so chiaki-stream.c's whole main()
 * runs on a host: a fake clock, scripted SDL events and chiaki callbacks, and a
 * record of everything the helper sent to the "console". */
#include <SDL2/SDL.h>
#include <chiaki/controller.h>
#include <chiaki/ffmpegdecoder.h>
#include <chiaki/opusdecoder.h>
#include <chiaki/session.h>
#include <libswscale/swscale.h>

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "fake_runtime.h"

/* ---- clock and script ---- */
uint32_t fake_now;
static FakeAction script[4096];
static int script_count, script_next;
static uint32_t time_limit;
static int pending_frames;
static int pending_w = 1280, pending_h = 720;
static bool any_open;
static ChiakiEventCallback event_cb;
static void *event_user;
static ChiakiFfmpegFrameAvailable frame_cb;
static void *frame_user;
static ChiakiFfmpegDecoder *frame_decoder;

FakeRecord fake_record;

typedef struct { bool gc; const char *name; SDL_JoystickID id; int refs; int buttons; Uint16 vendor, product; int type; int axes; } FakeDevice;
static FakeDevice devices[8];
static int device_count;

void fake_reset(uint32_t limit)
{
    fake_now = 1000;
    script_count = script_next = 0;
    time_limit = limit;
    pending_frames = 0;
    pending_w = 1280;
    pending_h = 720;
    any_open = false;
    event_cb = NULL;
    frame_cb = NULL;
    memset(&fake_record, 0, sizeof(fake_record));
    memset(devices, 0, sizeof(devices));
    device_count = 0;
}

int fake_add_device(bool gc, const char *name, SDL_JoystickID id)
{
    devices[device_count] = (FakeDevice){gc, name, id, 0, 13, 0, 0, 0, 4};
    return device_count++;
}

static FakeAction *add(uint32_t at)
{
    FakeAction *a = &script[script_count++];
    memset(a, 0, sizeof(*a));
    a->at = at;
    return a;
}

void fake_sdl_event(uint32_t at, SDL_Event event)
{
    FakeAction *a = add(at);
    a->kind = FAKE_SDL_EVENT;
    a->sdl = event;
}

void fake_button(uint32_t at, SDL_JoystickID which, int button, bool down)
{
    SDL_Event e;
    memset(&e, 0, sizeof(e));
    e.type = down ? SDL_CONTROLLERBUTTONDOWN : SDL_CONTROLLERBUTTONUP;
    e.cbutton.which = which;
    e.cbutton.button = (Uint8)button;
    fake_sdl_event(at, e);
}

void fake_axis(uint32_t at, SDL_JoystickID which, int axis, int value)
{
    SDL_Event e;
    memset(&e, 0, sizeof(e));
    e.type = SDL_CONTROLLERAXISMOTION;
    e.caxis.which = which;
    e.caxis.axis = (Uint8)axis;
    e.caxis.value = (Sint16)value;
    fake_sdl_event(at, e);
}

void fake_device_added(uint32_t at, int index)
{
    SDL_Event e;
    memset(&e, 0, sizeof(e));
    e.type = SDL_CONTROLLERDEVICEADDED;
    e.cdevice.which = index;
    fake_sdl_event(at, e);
}

void fake_device_removed(uint32_t at, SDL_JoystickID id)
{
    SDL_Event e;
    memset(&e, 0, sizeof(e));
    e.type = SDL_CONTROLLERDEVICEREMOVED;
    e.cdevice.which = id;
    fake_sdl_event(at, e);
}

void fake_chiaki(uint32_t at, ChiakiEventType type, int value)
{
    FakeAction *a = add(at);
    a->kind = FAKE_CHIAKI_EVENT;
    a->chiaki.type = type;
    if(type == CHIAKI_EVENT_LOGIN_PIN_REQUEST) a->chiaki.login_pin_request.pin_incorrect = value != 0;
    if(type == CHIAKI_EVENT_QUIT) a->chiaki.quit.reason = (ChiakiQuitReason)value;
    if(type == CHIAKI_EVENT_RUMBLE) { a->chiaki.rumble.left = (uint8_t)value; a->chiaki.rumble.right = (uint8_t)(value / 2); }
}

void fake_device_axes(int index, int axes) { devices[index].axes = axes; }

void fake_frames_sized(uint32_t from, uint32_t to, uint32_t every, int w, int h)
{
    for(uint32_t t = from; t <= to; t += every) {
        FakeAction *a = add(t);
        a->kind = FAKE_FRAME;
        a->w = w;
        a->h = h;
    }
}

void fake_frames(uint32_t from, uint32_t to, uint32_t every)
{
    fake_frames_sized(from, to, every, 1280, 720);
}

void fake_joy_axis(uint32_t at, SDL_JoystickID which, int axis, int value)
{
    SDL_Event e;
    memset(&e, 0, sizeof(e));
    e.type = SDL_JOYAXISMOTION;
    e.jaxis.which = which;
    e.jaxis.axis = (Uint8)axis;
    e.jaxis.value = (Sint16)value;
    fake_sdl_event(at, e);
}

static int by_time(const void *a, const void *b)
{
    const FakeAction *x = a, *y = b;
    if(x->at != y->at) return x->at < y->at ? -1 : 1;
    return x->seq < y->seq ? -1 : x->seq > y->seq;
}

void fake_sort(void)
{
    for(int i = 0; i < script_count; i++) script[i].seq = i; /* equal times keep script order */
    qsort(script, (size_t)script_count, sizeof(script[0]), by_time);
}

static void run_due_callbacks(void)
{
    while(script_next < script_count && script[script_next].at <= fake_now && script[script_next].kind != FAKE_SDL_EVENT) {
        FakeAction *a = &script[script_next++];
        if(a->kind == FAKE_CHIAKI_EVENT && event_cb) {
            if(a->chiaki.type == CHIAKI_EVENT_QUIT) a->chiaki.quit.reason_str = "fake detail";
            event_cb(&a->chiaki, event_user);
        } else if(a->kind == FAKE_FRAME && frame_cb) {
            pending_frames++;
            pending_w = a->w;
            pending_h = a->h;
            frame_cb(frame_decoder, frame_user);
        }
    }
}

int SDL_PollEvent(SDL_Event *event)
{
    if(fake_now > time_limit) {
        fake_record.timed_out = true;
        raise(SIGTERM);
    }
    run_due_callbacks();
    if(script_next < script_count && script[script_next].at <= fake_now && script[script_next].kind == FAKE_SDL_EVENT) {
        *event = script[script_next++].sdl;
        run_due_callbacks();
        return 1;
    }
    return 0;
}

Uint32 SDL_GetTicks(void) { return fake_now; }
void SDL_Delay(Uint32 ms) { fake_now += ms; }

/* ---- SDL basics ---- */
int SDL_Init(Uint32 flags) { (void)flags; return 0; }
void SDL_Quit(void) {}
const char *SDL_GetError(void) { return "fake error"; }
SDL_bool SDL_SetHint(const char *name, const char *value) { (void)name; (void)value; return SDL_TRUE; }
int SDL_ShowCursor(int toggle) { (void)toggle; return 0; }
void SDL_GetVersion(SDL_version *ver) { ver->major = 2; ver->minor = 0; ver->patch = 14; }
SDL_Window *SDL_CreateWindow(const char *title, int x, int y, int w, int h, Uint32 flags)
{
    (void)title; (void)x; (void)y; (void)w; (void)h; (void)flags;
    return (SDL_Window *)&fake_record;
}
void SDL_DestroyWindow(SDL_Window *window) { (void)window; }
SDL_Renderer *fake_renderer(int w, int h);
SDL_Renderer *SDL_CreateRenderer(SDL_Window *window, int index, Uint32 flags) { (void)window; (void)index; (void)flags; return fake_renderer(1280, 720); }
void SDL_DestroyRenderer(SDL_Renderer *renderer) { (void)renderer; }
int SDL_GetRendererInfo(SDL_Renderer *renderer, SDL_RendererInfo *info) { (void)renderer; memset(info, 0, sizeof(*info)); info->name = "fake"; return 0; }
int SDL_RenderClear(SDL_Renderer *renderer) { (void)renderer; return 0; }
void SDL_RenderPresent(SDL_Renderer *renderer) { (void)renderer; fake_record.presents++; }
int SDL_UpdateYUVTexture(SDL_Texture *texture, const SDL_Rect *rect, const Uint8 *Yplane, int Ypitch,
                         const Uint8 *Uplane, int Upitch, const Uint8 *Vplane, int Vpitch)
{
    (void)texture; (void)rect; (void)Yplane; (void)Ypitch; (void)Uplane; (void)Upitch; (void)Vplane; (void)Vpitch;
    fake_record.updates++;
    if(fake_record.fail_update_after > 0 && fake_record.updates > fake_record.fail_update_after) return -1;
    return 0;
}
SDL_AudioDeviceID SDL_OpenAudioDevice(const char *device, int iscapture, const SDL_AudioSpec *desired, SDL_AudioSpec *obtained, int allowed)
{
    (void)device; (void)iscapture; (void)desired; (void)obtained; (void)allowed;
    return 1;
}
void SDL_CloseAudioDevice(SDL_AudioDeviceID dev) { (void)dev; }
void SDL_PauseAudioDevice(SDL_AudioDeviceID dev, int pause_on)
{
    (void)dev;
    fake_record.audio_paused = pause_on;
    if(pause_on && !fake_record.pause_at) {
        /* The error card's first call: what does the world look like now? */
        fake_record.pause_at = fake_now;
        if(fake_record.watch_path[0]) {
            FILE *f = fopen(fake_record.watch_path, "r");
            if(f) {
                size_t n = fread(fake_record.watch_at_pause, 1, sizeof(fake_record.watch_at_pause) - 1, f);
                fake_record.watch_at_pause[n] = '\0';
                fclose(f);
            }
        }
    }
}
Uint32 SDL_GetQueuedAudioSize(SDL_AudioDeviceID dev) { (void)dev; return 0; }
void SDL_ClearQueuedAudio(SDL_AudioDeviceID dev) { (void)dev; }
int SDL_QueueAudio(SDL_AudioDeviceID dev, const void *data, Uint32 len) { (void)dev; (void)data; (void)len; return 0; }

/* ---- pads ---- */
struct _SDL_GameController { int index; };
struct _SDL_Joystick { int index; };
static struct _SDL_GameController controllers[8];
static struct _SDL_Joystick joysticks[8];
int SDL_NumJoysticks(void) { return device_count; }
SDL_bool SDL_IsGameController(int i) { return i >= 0 && i < device_count && devices[i].gc ? SDL_TRUE : SDL_FALSE; }
int SDL_GameControllerAddMapping(const char *mapping)
{
    fake_record.mappings++;
    fake_record.mapping_before_open = !any_open;
    snprintf(fake_record.mapping, sizeof(fake_record.mapping), "%s", mapping);
    return 1;
}
SDL_GameController *SDL_GameControllerOpen(int i)
{
    if(i < 0 || i >= device_count || !devices[i].gc) return NULL;
    devices[i].refs++;
    controllers[i].index = i;
    fake_record.opens++;
    any_open = true;
    return &controllers[i];
}
void SDL_GameControllerClose(SDL_GameController *c) { devices[c->index].refs--; }
const char *SDL_GameControllerName(SDL_GameController *c) { return devices[c->index].name; }
SDL_Joystick *SDL_GameControllerGetJoystick(SDL_GameController *c) { joysticks[c->index].index = c->index; return &joysticks[c->index]; }
int SDL_GameControllerRumble(SDL_GameController *c, Uint16 low, Uint16 high, Uint32 ms)
{
    if(low || high) fake_record.rumble_last_at[c->index] = fake_now;
    if(low > fake_record.rumble_low_by[c->index]) fake_record.rumble_low_by[c->index] = low;
    fake_record.rumble_last_low_by[c->index] = low;
    if(!low && !high) fake_record.rumble_zero_at = fake_now;
    if(low > fake_record.rumble_low) fake_record.rumble_low = low;   /* the strongest seen: */
    if(high > fake_record.rumble_high) fake_record.rumble_high = high; /* exit sends 0 last */
    if(ms > fake_record.rumble_ms) fake_record.rumble_ms = ms;
    fake_record.rumble_calls++;
    return 0;
}
SDL_Joystick *SDL_JoystickOpen(int i) { joysticks[i].index = i; devices[i].refs++; any_open = true; return &joysticks[i]; }
void SDL_JoystickClose(SDL_Joystick *j) { devices[j->index].refs--; }
const char *SDL_JoystickName(SDL_Joystick *j) { return devices[j->index].name; }
SDL_JoystickID SDL_JoystickInstanceID(SDL_Joystick *j) { return devices[j->index].id; }
int SDL_JoystickNumButtons(SDL_Joystick *j) { return devices[j->index].buttons; }
int SDL_JoystickNumAxes(SDL_Joystick *j) { return devices[j->index].axes; }
int SDL_JoystickRumble(SDL_Joystick *j, Uint16 low, Uint16 high, Uint32 ms)
{
    (void)ms;
    if(low > fake_record.rumble_low_by[j->index]) fake_record.rumble_low_by[j->index] = low;
    fake_record.rumble_last_low_by[j->index] = low;
    if(!low && !high) fake_record.rumble_zero_at = fake_now;
    return 0;
}
Uint16 SDL_JoystickGetVendor(SDL_Joystick *j) { return devices[j->index].vendor; }
Uint16 SDL_JoystickGetProduct(SDL_Joystick *j) { return devices[j->index].product; }
#if SDL_VERSION_ATLEAST(2, 0, 12)
SDL_GameControllerType SDL_GameControllerGetType(SDL_GameController *c) { return (SDL_GameControllerType)devices[c->index].type; }
#endif
int fake_device_refs(int index) { return devices[index].refs; }
void fake_device_buttons(int index, int buttons) { devices[index].buttons = buttons; }
void fake_device_ids(int index, unsigned vendor, unsigned product)
{
    devices[index].vendor = (Uint16)vendor;
    devices[index].product = (Uint16)product;
}
void fake_device_type(int index, int type) { devices[index].type = type; }
int fake_device_index(SDL_Joystick *j) { return j ? j->index : -1; }

/* ---- chiaki ---- */
const char *chiaki_error_string(ChiakiErrorCode code) { (void)code; return "fake chiaki error"; }
ChiakiErrorCode chiaki_lib_init(void) { return CHIAKI_ERR_SUCCESS; }
void chiaki_log_init(ChiakiLog *log, uint32_t level_mask, ChiakiLogCb cb, void *user) { log->level_mask = level_mask; log->cb = cb; log->user = user; }
const char *chiaki_quit_reason_string(ChiakiQuitReason reason) { return reason == CHIAKI_QUIT_REASON_STOPPED ? "Stopped" : "Connection refused"; }
bool chiaki_quit_reason_is_error(ChiakiQuitReason reason) { return reason != CHIAKI_QUIT_REASON_STOPPED && reason != CHIAKI_QUIT_REASON_NONE; }
ChiakiErrorCode chiaki_session_init(ChiakiSession *session, ChiakiConnectInfo *info, ChiakiLog *log)
{
    (void)session; (void)log;
    snprintf(fake_record.host, sizeof(fake_record.host), "%s", info->host);
    fake_record.codec = (int)info->video_profile.codec;
    fake_record.ps5 = info->ps5;
    memcpy(fake_record.regist_key, info->regist_key, sizeof(fake_record.regist_key));
    return CHIAKI_ERR_SUCCESS;
}
void chiaki_session_fini(ChiakiSession *session) { (void)session; fake_record.fini++; }
ChiakiErrorCode chiaki_session_start(ChiakiSession *session) { (void)session; fake_record.started++; return CHIAKI_ERR_SUCCESS; }
ChiakiErrorCode chiaki_session_stop(ChiakiSession *session)
{
    (void)session;
    fake_record.stopped++;
    if(!fake_record.stop_at) fake_record.stop_at = fake_now;
    return CHIAKI_ERR_SUCCESS;
}
ChiakiErrorCode chiaki_session_join(ChiakiSession *session) { (void)session; return CHIAKI_ERR_SUCCESS; }
ChiakiErrorCode chiaki_session_set_controller_state(ChiakiSession *session, ChiakiControllerState *state)
{
    (void)session;
    if(fake_record.states < FAKE_MAX_STATES) {
        fake_record.state[fake_record.states] = *state;
        fake_record.state_at[fake_record.states] = fake_now;
        fake_record.states++;
    }
    return CHIAKI_ERR_SUCCESS;
}
ChiakiErrorCode chiaki_session_set_login_pin(ChiakiSession *session, const uint8_t *pin, size_t size)
{
    (void)session;
    size_t n = size < sizeof(fake_record.pins[0]) - 1 ? size : sizeof(fake_record.pins[0]) - 1;
    if(fake_record.pin_count < 8) {
        memcpy(fake_record.pins[fake_record.pin_count], pin, n);
        fake_record.pins[fake_record.pin_count][n] = '\0';
        fake_record.pin_count++;
    }
    return CHIAKI_ERR_SUCCESS;
}
void chiaki_session_set_event_cb(ChiakiSession *session, ChiakiEventCallback cb, void *user) { (void)session; event_cb = cb; event_user = user; }
void chiaki_session_set_video_sample_cb(ChiakiSession *session, ChiakiVideoSampleCallback cb, void *user) { (void)session; (void)cb; (void)user; }
void chiaki_session_set_audio_sink(ChiakiSession *session, ChiakiAudioSink *sink) { (void)session; (void)sink; }
void chiaki_controller_state_set_idle(ChiakiControllerState *state)
{
    memset(state, 0, sizeof(*state));
    for(int i = 0; i < CHIAKI_CONTROLLER_TOUCHES_MAX; i++) state->touches[i].id = -1;
    state->orient_w = 1.0f;
}
int8_t chiaki_controller_state_start_touch(ChiakiControllerState *state, uint16_t x, uint16_t y)
{
    for(int i = 0; i < CHIAKI_CONTROLLER_TOUCHES_MAX; i++) {
        if(state->touches[i].id < 0) {
            state->touches[i].id = (int8_t)(state->touch_id_next & 0x7f);
            state->touch_id_next = (uint8_t)((state->touch_id_next + 1) & 0x7f);
            state->touches[i].x = x;
            state->touches[i].y = y;
            fake_record.touch_starts++;
            return state->touches[i].id;
        }
    }
    return -1;
}
void chiaki_controller_state_stop_touch(ChiakiControllerState *state, uint8_t id)
{
    for(int i = 0; i < CHIAKI_CONTROLLER_TOUCHES_MAX; i++)
        if(state->touches[i].id == (int8_t)id) { state->touches[i].id = -1; fake_record.touch_stops++; }
}
void chiaki_controller_state_set_touch_pos(ChiakiControllerState *state, uint8_t id, uint16_t x, uint16_t y)
{
    for(int i = 0; i < CHIAKI_CONTROLLER_TOUCHES_MAX; i++)
        if(state->touches[i].id == (int8_t)id) { state->touches[i].x = x; state->touches[i].y = y; fake_record.touch_moves++; }
}
ChiakiErrorCode chiaki_ffmpeg_decoder_init(ChiakiFfmpegDecoder *decoder, ChiakiLog *log, ChiakiCodec codec, unsigned int max_fps,
                                           const char *hw, void *ctx, ChiakiFfmpegFrameAvailable cb, void *user)
{
    (void)log; (void)codec; (void)max_fps; (void)hw; (void)ctx;
    frame_decoder = decoder;
    frame_cb = cb;
    frame_user = user;
    return fake_record.fail_decoder ? CHIAKI_ERR_UNKNOWN : CHIAKI_ERR_SUCCESS;
}
void chiaki_ffmpeg_decoder_fini(ChiakiFfmpegDecoder *decoder) { (void)decoder; }
bool chiaki_ffmpeg_decoder_video_sample_cb(uint8_t *buf, size_t size, int32_t lost, bool recovered, void *user)
{
    (void)buf; (void)size; (void)lost; (void)recovered; (void)user;
    return true;
}
static uint8_t plane[1280 * 720];
ChiakiFfmpegFrame chiaki_ffmpeg_decoder_pull_frame(ChiakiFfmpegDecoder *decoder, int32_t *frames_lost)
{
    (void)decoder;
    ChiakiFfmpegFrame out = {NULL};
    *frames_lost = 0;
    if(pending_frames <= 0) return out;
    pending_frames = 0;
    AVFrame *f = calloc(1, sizeof(*f));
    f->format = AV_PIX_FMT_YUV420P;
    f->width = pending_w;
    f->height = pending_h;
    f->data[0] = f->data[1] = f->data[2] = plane; /* never read: the fake texture update is a no-op */
    f->linesize[0] = pending_w;
    f->linesize[1] = f->linesize[2] = pending_w / 2;
    out.frame = f;
    fake_record.frames_pulled++;
    return out;
}
void chiaki_opus_decoder_init(ChiakiOpusDecoder *d, ChiakiLog *log) { (void)d; (void)log; }
void chiaki_opus_decoder_fini(ChiakiOpusDecoder *d) { (void)d; }
void chiaki_opus_decoder_set_cb(ChiakiOpusDecoder *d, ChiakiOpusDecoderSettingsCallback s, ChiakiOpusDecoderFrameCallback f, void *u)
{
    (void)d; (void)f;
    s(2, 48000, u); /* opens the audio device like a real stream header would */
}
void chiaki_opus_decoder_get_sink(ChiakiOpusDecoder *d, ChiakiAudioSink *sink) { (void)d; memset(sink, 0, sizeof(*sink)); }

/* ---- FFmpeg ---- */
AVFrame *av_frame_alloc(void) { return calloc(1, sizeof(AVFrame)); }
void av_frame_free(AVFrame **frame) { if(frame && *frame) { free(*frame); *frame = NULL; } }
int av_frame_get_buffer(AVFrame *frame, int align) { (void)frame; (void)align; return 0; }
int av_frame_make_writable(AVFrame *frame) { (void)frame; return 0; }
struct SwsContext *sws_getCachedContext(struct SwsContext *c, int sw, int sh, enum AVPixelFormat sf, int dw, int dh,
                                        enum AVPixelFormat df, int flags, SwsFilter *a, SwsFilter *b, const double *p)
{
    (void)c; (void)sw; (void)sh; (void)sf; (void)dw; (void)dh; (void)df; (void)flags; (void)a; (void)b; (void)p;
    return NULL;
}
int sws_scale(struct SwsContext *c, const uint8_t *const s[], const int ss[], int y, int h, uint8_t *const d[], const int ds[])
{
    (void)c; (void)s; (void)ss; (void)y; (void)h; (void)d; (void)ds;
    return 0;
}
void sws_freeContext(struct SwsContext *c) { (void)c; }
