/* chiaki-stream - JukaHub's PS4/PS5 Remote Play streamer for TrimUI handhelds.
 *
 *   chiaki-stream SESSION_FILE
 *
 * JukaHub writes the session file (keys: player/native/README.md), quits and
 * hands the screen over; this helper deletes the file, streams with chiaki-ng's
 * libchiaki (FFmpeg decode, SDL2 display and audio) and writes a status file
 * JukaHub reads when it comes back. The controller mapping is stream_input.c,
 * the on-screen display stream_osd.c, the files/battery/motor stream_sys.c.
 *
 * Exit codes: 0 ok, 2 usage, 3 invalid session, 4 chiaki init, 5 SDL init,
 * 6 decoder init, 7 session init, 8 session start, 10 video failure,
 * 11 the session ended with an error, 12 login passcode not entered.
 *
 * Signals: SIGINT, SIGTERM, SIGHUP and SIGQUIT end the stream normally (status
 * file included), SIGPIPE is ignored, and a crash first switches the rumble
 * motor off (stream_sys.c ss_motor_crash_guard). */
#include <SDL2/SDL.h>
#include <chiaki/controller.h>
#include <chiaki/ffmpegdecoder.h>
#include <chiaki/log.h>
#include <chiaki/opusdecoder.h>
#include <chiaki/session.h>

#include <libavutil/frame.h>
#include <libavutil/imgutils.h>
#include <libavutil/pixfmt.h>
#include <libswscale/swscale.h>

#include <dlfcn.h>
#include <errno.h>
#include <signal.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "stream_input.h"
#include "stream_osd.h"
#include "stream_sys.h"

#define APP_NAME "JukaHub Remote Play"
#define SCREEN_WIDTH 1280
#define SCREEN_HEIGHT 720
/* Pads open at once: the built-in one plus Bluetooth and USB pads. As many as
 * stream_input.c merges: a pad it has no room for is not opened at all. */
#define MAX_PADS SI_MAX_PADS
/* JukaHub's own mapping for the firmware pad "TRIMUI Player1" (player/main.go
 * adds it at run time). This helper is another process and does not inherit
 * it: a firmware SDL without this GUID in its database would otherwise open the
 * built-in pad as a raw joystick, whose layout is different. */
#define TRIMUI_PAD_MAPPING "030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,"
/* While a pad rumbles, its rumble is sent again this often, well inside the
 * SS_MOTOR_SAFETY_MS duration each SDL rumble call is given. */
#define RUMBLE_RENEW_MS 1000u
/* The touchpad resolution the console expects, as chiaki-ng's client sends it. */
#define PS4_TOUCHPAD_MAX_X 1920
#define PS4_TOUCHPAD_MAX_Y 942
#define PS5_TOUCHPAD_MAX_X 1919
#define PS5_TOUCHPAD_MAX_Y 1079
/* A stick past this counts as the pad being played with (rumble follows it). */
#define PAD_ACTIVE_AXIS 8000
#define TOAST_SHORT_MS 1600u
#define TOAST_LONG_MS 5000u
#define HINT_MS 6000u
#define ERROR_CARD_MS 8000u
#define ERROR_CARD_GRACE_MS 400u
#define BATTERY_POLL_MS 30000u

_Static_assert(sizeof(((ChiakiConnectInfo *)0)->regist_key) == SS_REGIST_KEY_SIZE, "regist_key size");
_Static_assert(sizeof(((ChiakiConnectInfo *)0)->morning) == SS_MORNING_SIZE, "morning size");

typedef enum {
    ROW_RESUME = 0,
    ROW_QUIT,
    ROW_HOLD_PS,
    ROW_SELECT,
    ROW_DPAD,
    ROW_RUMBLE,
    ROW_VOLUME,
    ROW_STATS,
    ROW_CONTROLS,
    ROW_COUNT
} MenuRow;

/* One open pad. A pad SDL has a mapping for is a game controller; anything
 * else is a raw joystick read with the layout the old helper assumed. Every pad
 * feeds stream_input.c, which merges them, so the built-in pad and a Bluetooth
 * or USB pad work side by side and either can leave without a button staying
 * held on the console. */
typedef struct {
    bool used;
    SDL_JoystickID id;
    SDL_GameController *controller;
    SDL_Joystick *joystick; /* owned (closed here) only when controller is NULL */
    Uint8 hat;
    bool xpad_layout;       /* raw joystick with six axes: the X360 (xpad) order */
    bool builtin;           /* the handheld's own pad: its rumble is the GPIO motor */
    uint32_t last_input;    /* SDL_GetTicks of its last press or deliberate stick move */
    Uint16 rumble_low;
    Uint16 rumble_high;
    uint32_t rumble_at;
    char name[96];
} StreamPad;

typedef struct {
    ChiakiLog log;
    ChiakiSession session;
    ChiakiFfmpegDecoder decoder;
    ChiakiOpusDecoder opus;
    ChiakiControllerState controller_state;
    const SsConfig *config;
    SDL_Window *window;
    SDL_Renderer *renderer;
    SDL_Texture *texture;
    SDL_Rect destination;
    bool destination_ready;
    StreamPad pads[MAX_PADS];
    SDL_AudioDeviceID audio_device;
    struct SwsContext *sws;
    AVFrame *converted;
    atomic_bool frame_ready;
    atomic_bool running;
    atomic_bool connected;
    atomic_uint volume;
    atomic_bool quit_received;
    atomic_bool quit_error;
    char quit_reason[200];
    atomic_bool pin_requested;
    atomic_bool pin_incorrect;
    atomic_int rumble_left;
    atomic_int rumble_right;
    atomic_bool rumble_dirty;
    uint32_t rumble_renewed_at;
    bool session_initialized;
    bool session_started;
    bool decoder_initialized;
    bool opus_initialized;
    bool verbose_logs;
    uint64_t rendered_frames;
    uint64_t lost_frames;
    atomic_uint_fast64_t fec_failures;
    atomic_uint_fast64_t suppressed_logs;
    uint32_t last_stats_ticks;
    uint64_t stats_rendered_frames;
    uint64_t stats_lost_frames;
    uint64_t stats_fec_failures;
    uint64_t stats_suppressed_logs;
    uint32_t audio_channels;
    int video_w;
    int video_h;

    /* input */
    SiState input;
    SiOutput last_output;
    bool output_valid;
    bool touch_active[SI_MAX_TOUCHES];
    int8_t touch_ids[SI_MAX_TOUCHES];
    uint32_t touch_serials[SI_MAX_TOUCHES];
    bool modal;

    /* display */
    Osd osd;
    bool osd_ready;
    bool osd_dirty;
    uint32_t last_present;
    bool menu_open;
    bool menu_controls;
    int menu_cursor;
    uint32_t menu_second;
    bool pin_open;
    bool pin_wrong;
    bool pin_checking;
    bool pin_prompted;      /* the console asked for the passcode at least once */
    bool saved_pin_tried;
    bool saved_pin_rejected;
    char saved_pin[5];      /* the session file's login_pin, wiped once used */
    char pin_digits[4];
    int pin_cursor;
    char toast[160];
    OsdToastKind toast_kind;
    uint32_t toast_since;
    uint32_t toast_until;
    uint32_t hint_until;
    bool stats_visible;
    char stats_line[160];
    char status_line[120];

    /* session facts */
    uint32_t start_ticks;
    bool connected_seen;
    uint32_t connected_at;
    uint32_t first_frame_at;
    bool quit_handled;
    bool rumble_enabled;
    SsMotor motor;
    SsBattery battery;
    uint32_t battery_at;
    int battery_warned;

    /* the outcome, for the status file */
    int rc;
    const char *result;
    char message[300];
    char error_title[120];
    bool show_error;
} StreamApp;

static StreamApp g_app;
static StreamApp *signal_app;
/* Set by a stop signal. "running" is already false once a stream is over, so
 * the error card needs its own flag to end on SIGTERM as well. */
static volatile sig_atomic_t signalled;

/* snprintf for messages where a cut-off tail is acceptable, through a va_list so
 * -Wformat-truncation leaves the call alone. */
__attribute__((format(printf, 3, 4)))
static void textf(char *out, size_t size, const char *format, ...)
{
    va_list args;
    va_start(args, format);
    vsnprintf(out, size, format, args);
    va_end(args);
}

static void native_log(ChiakiLogLevel level, const char *message, void *user)
{
    StreamApp *app = user;
    if(app && !app->verbose_logs && message && (
            strstr(message, "Frame Processor received") ||
            strstr(message, "FEC failed") ||
            strstr(message, "FEC successful") ||
            strstr(message, "Missing unit") ||
            strstr(message, "Skipping P-frame") ||
            strstr(message, "missing or corrupt frame") ||
            strstr(message, "reporting corrupt frame") ||
            strstr(message, "could not flush frame") ||
            strstr(message, "waiting for IDR") ||
            strstr(message, "already waiting for requested IDR"))) {
        atomic_fetch_add(&app->suppressed_logs, 1);
        return;
    }
    const char *name = "INFO";
    if(level == CHIAKI_LOG_ERROR) name = "ERROR";
    else if(level == CHIAKI_LOG_WARNING) name = "WARN";
    else if(level == CHIAKI_LOG_DEBUG) name = "DEBUG";
    else if(level == CHIAKI_LOG_VERBOSE) name = "TRACE";
    fprintf(stdout, "[native] [%s] %s\n", name, message ? message : "");
    if(level == CHIAKI_LOG_ERROR) fflush(stdout);
}

static void stop_signal(int signal_number)
{
    (void)signal_number;
    signalled = 1;
    if(signal_app)
        atomic_store(&signal_app->running, false);
}

static const char *codec_name(const SsConfig *config)
{
    return config->codec == SS_CODEC_H265 ? "h265" : "h264";
}

static const char *codec_label(const SsConfig *config)
{
    return config->codec == SS_CODEC_H265 ? "H.265" : "H.264";
}

static void toast(StreamApp *app, const char *text, OsdToastKind kind, uint32_t duration)
{
    uint32_t now = SDL_GetTicks();
    textf(app->toast, sizeof(app->toast), "%s", text);
    app->toast_kind = kind;
    app->toast_since = now;
    app->toast_until = now + duration;
    app->osd_dirty = true;
}

static void set_status(StreamApp *app, const char *text)
{
    textf(app->status_line, sizeof(app->status_line), "%s", text);
    app->osd_dirty = true;
}

/* The one place the outcome is decided; the first decision wins. */
static void set_outcome(StreamApp *app, int rc, const char *result, const char *message)
{
    if(app->result) return;
    app->rc = rc;
    app->result = result;
    textf(app->message, sizeof(app->message), "%s", message ? message : "");
}

static void request_quit(StreamApp *app)
{
    set_outcome(app, 0, app->first_frame_at ? "ok" : "cancelled",
                app->first_frame_at ? "Remote Play ended." : "Remote Play was cancelled before the video started.");
    atomic_store(&app->running, false);
}

/* ---- chiaki callbacks (chiaki's threads) ---- */

static void frame_available(ChiakiFfmpegDecoder *decoder, void *user)
{
    (void)decoder;
    StreamApp *app = user;
    atomic_store(&app->frame_ready, true);
}

static void audio_settings(uint32_t channels, uint32_t rate, void *user)
{
    StreamApp *app = user;
    SDL_AudioSpec wanted;
    SDL_zero(wanted);
    wanted.freq = (int)rate;
    wanted.format = AUDIO_S16SYS;
    wanted.channels = (Uint8)channels;
    wanted.samples = 1024;
    if(app->audio_device) SDL_CloseAudioDevice(app->audio_device);
    app->audio_device = SDL_OpenAudioDevice(NULL, 0, &wanted, NULL, 0);
    if(!app->audio_device) {
        fprintf(stderr, "[native] SDL audio open failed: %s\n", SDL_GetError());
        return;
    }
    app->audio_channels = channels;
    SDL_PauseAudioDevice(app->audio_device, 0);
    fprintf(stdout, "[native] audio ready: %u channels %u Hz\n", channels, rate);
    fflush(stdout);
}

static void audio_frame(int16_t *samples, size_t samples_count, void *user)
{
    StreamApp *app = user;
    if(!app->audio_device || !samples_count) return;
    if(SDL_GetQueuedAudioSize(app->audio_device) > 48000) SDL_ClearQueuedAudio(app->audio_device);
    size_t count = samples_count * (app->audio_channels ? app->audio_channels : 2);
    unsigned int volume = atomic_load(&app->volume);
    if(volume < 100) {
        for(size_t index = 0; index < count; index++)
            samples[index] = (int16_t)(((int32_t)samples[index] * (int32_t)volume) / 100);
    }
    SDL_QueueAudio(app->audio_device, samples, (Uint32)(count * sizeof(int16_t)));
}

static void session_event(ChiakiEvent *event, void *user)
{
    StreamApp *app = user;
    switch(event->type) {
        case CHIAKI_EVENT_CONNECTED:
            atomic_store(&app->connected, true);
            fprintf(stdout, "[native] Remote Play connected\n");
            fflush(stdout);
            break;
        case CHIAKI_EVENT_LOGIN_PIN_REQUEST:
            fprintf(stdout, "[native] console asks for the 4-digit login passcode%s\n",
                    event->login_pin_request.pin_incorrect ? " again: the last one was wrong" : "");
            fflush(stdout);
            atomic_store(&app->pin_incorrect, event->login_pin_request.pin_incorrect);
            atomic_store(&app->pin_requested, true);
            break;
        case CHIAKI_EVENT_RUMBLE:
            atomic_store(&app->rumble_left, (int)event->rumble.left);
            atomic_store(&app->rumble_right, (int)event->rumble.right);
            atomic_store(&app->rumble_dirty, true);
            break;
        case CHIAKI_EVENT_QUIT:
            fprintf(stderr, "[native] session quit: %s%s%s\n",
                    chiaki_quit_reason_string(event->quit.reason),
                    event->quit.reason_str ? " - " : "",
                    event->quit.reason_str ? event->quit.reason_str : "");
            if(!atomic_load(&app->quit_received)) {
                textf(app->quit_reason, sizeof(app->quit_reason), "%s%s%s",
                         chiaki_quit_reason_string(event->quit.reason),
                         event->quit.reason_str ? " - " : "",
                         event->quit.reason_str ? event->quit.reason_str : "");
                atomic_store(&app->quit_error, chiaki_quit_reason_is_error(event->quit.reason));
                atomic_store(&app->quit_received, true);
            }
            break;
        case CHIAKI_EVENT_VIDEO_FEC_FAILURE: {
            uint64_t fec_failures = atomic_fetch_add(&app->fec_failures, 1) + 1;
            if(fec_failures <= 3 || fec_failures % 50 == 0) {
                fprintf(stderr, "[native] video FEC failure count=%llu frame=%d idr=%d\n",
                        (unsigned long long)fec_failures,
                        event->video_fec_failure.frame_index,
                        event->video_fec_failure.idr_request_sent);
            }
            break;
        }
        default:
            break;
    }
}

/* ---- controller state ---- */

static uint32_t to_chiaki_buttons(uint32_t buttons)
{
    uint32_t out = 0;
    if(buttons & SI_OUT_CROSS) out |= CHIAKI_CONTROLLER_BUTTON_CROSS;
    if(buttons & SI_OUT_MOON) out |= CHIAKI_CONTROLLER_BUTTON_MOON;
    if(buttons & SI_OUT_BOX) out |= CHIAKI_CONTROLLER_BUTTON_BOX;
    if(buttons & SI_OUT_PYRAMID) out |= CHIAKI_CONTROLLER_BUTTON_PYRAMID;
    if(buttons & SI_OUT_DPAD_LEFT) out |= CHIAKI_CONTROLLER_BUTTON_DPAD_LEFT;
    if(buttons & SI_OUT_DPAD_RIGHT) out |= CHIAKI_CONTROLLER_BUTTON_DPAD_RIGHT;
    if(buttons & SI_OUT_DPAD_UP) out |= CHIAKI_CONTROLLER_BUTTON_DPAD_UP;
    if(buttons & SI_OUT_DPAD_DOWN) out |= CHIAKI_CONTROLLER_BUTTON_DPAD_DOWN;
    if(buttons & SI_OUT_L1) out |= CHIAKI_CONTROLLER_BUTTON_L1;
    if(buttons & SI_OUT_R1) out |= CHIAKI_CONTROLLER_BUTTON_R1;
    if(buttons & SI_OUT_L3) out |= CHIAKI_CONTROLLER_BUTTON_L3;
    if(buttons & SI_OUT_R3) out |= CHIAKI_CONTROLLER_BUTTON_R3;
    if(buttons & SI_OUT_OPTIONS) out |= CHIAKI_CONTROLLER_BUTTON_OPTIONS;
    if(buttons & SI_OUT_SHARE) out |= CHIAKI_CONTROLLER_BUTTON_SHARE;
    if(buttons & SI_OUT_TOUCHPAD) out |= CHIAKI_CONTROLLER_BUTTON_TOUCHPAD;
    if(buttons & SI_OUT_PS) out |= CHIAKI_CONTROLLER_BUTTON_PS;
    return out;
}

static bool outputs_equal(const SiOutput *a, const SiOutput *b)
{
    if(a->buttons != b->buttons || a->l2 != b->l2 || a->r2 != b->r2 || a->lx != b->lx ||
       a->ly != b->ly || a->rx != b->rx || a->ry != b->ry)
        return false;
    for(int i = 0; i < SI_MAX_TOUCHES; i++) {
        const SiTouch *ta = &a->touch[i], *tb = &b->touch[i];
        if(ta->active != tb->active) return false;
        if(ta->active && (ta->x != tb->x || ta->y != tb->y || ta->serial != tb->serial)) return false;
    }
    return true;
}

/* Keeps chiaki's touches in step with the mapping's, matched by serial: a serial
 * that is gone lifts its finger, a new one puts a finger down, and a serial seen
 * before only moves — whichever output slot it is in, because the slots shift
 * when an earlier finger lifts, and a shift must not lift and re-touch the
 * finger that is still down (that ends a swipe on the console). */
static void sync_touches(StreamApp *app, const SiOutput *out)
{
    ChiakiControllerState *state = &app->controller_state;
    for(int i = 0; i < SI_MAX_TOUCHES; i++) {
        if(!app->touch_active[i]) continue;
        bool kept = false;
        for(int j = 0; j < SI_MAX_TOUCHES && !kept; j++)
            kept = out->touch[j].active && out->touch[j].serial == app->touch_serials[i];
        if(kept) continue;
        if(app->touch_ids[i] >= 0) chiaki_controller_state_stop_touch(state, (uint8_t)app->touch_ids[i]);
        app->touch_active[i] = false;
        app->touch_ids[i] = -1;
    }
    for(int j = 0; j < SI_MAX_TOUCHES; j++) {
        const SiTouch *touch = &out->touch[j];
        if(!touch->active) continue;
        int slot = -1;
        for(int i = 0; i < SI_MAX_TOUCHES && slot < 0; i++)
            if(app->touch_active[i] && app->touch_serials[i] == touch->serial) slot = i;
        if(slot >= 0) {
            if(app->touch_ids[slot] >= 0)
                chiaki_controller_state_set_touch_pos(state, (uint8_t)app->touch_ids[slot], touch->x, touch->y);
            continue;
        }
        for(int i = 0; i < SI_MAX_TOUCHES && slot < 0; i++)
            if(!app->touch_active[i]) slot = i;
        if(slot < 0) continue;
        app->touch_ids[slot] = chiaki_controller_state_start_touch(state, touch->x, touch->y);
        app->touch_active[slot] = true;
        app->touch_serials[slot] = touch->serial;
    }
}

static void push_controller(StreamApp *app, uint32_t now)
{
    SiOutput out;
    si_output(&app->input, now, &out);
    if(app->output_valid && outputs_equal(&out, &app->last_output)) return;
    app->last_output = out;
    app->output_valid = true;
    ChiakiControllerState *state = &app->controller_state;
    state->buttons = to_chiaki_buttons(out.buttons);
    state->l2_state = out.l2;
    state->r2_state = out.r2;
    state->left_x = out.lx;
    state->left_y = out.ly;
    state->right_x = out.rx;
    state->right_y = out.ry;
    sync_touches(app, &out);
    if(app->session_initialized)
        chiaki_session_set_controller_state(&app->session, state);
}

static void update_modal(StreamApp *app, uint32_t now)
{
    bool modal = app->menu_open || app->pin_open;
    if(modal != app->modal) {
        app->modal = modal;
        si_set_modal(&app->input, modal, now);
    }
    app->osd_dirty = true;
}

static StreamPad *rumble_target(StreamApp *app)
{
    StreamPad *best = NULL;
    for(int i = 0; i < MAX_PADS; i++) {
        StreamPad *pad = &app->pads[i];
        if(!pad->used) continue;
        if(!best || pad->last_input > best->last_input ||
           (pad->last_input == best->last_input && pad->builtin && !best->builtin))
            best = pad;
    }
    return best;
}

static void pad_rumble_send(StreamPad *pad, Uint16 low, Uint16 high, uint32_t now)
{
    pad->rumble_low = low;
    pad->rumble_high = high;
    pad->rumble_at = now;
    Uint32 duration = (low || high) ? SS_MOTOR_SAFETY_MS : 0;
    if(pad->controller) SDL_GameControllerRumble(pad->controller, low, high, duration);
#if SDL_VERSION_ATLEAST(2, 0, 9)
    else if(pad->joystick) SDL_JoystickRumble(pad->joystick, low, high, duration);
#endif
}

static void pad_rumble(StreamPad *pad, Uint16 low, Uint16 high, uint32_t now)
{
    bool same = pad->rumble_low == low && pad->rumble_high == high;
    if(same && (!(low || high) || now - pad->rumble_at < SS_MOTOR_SAFETY_MS / 2)) return;
    pad_rumble_send(pad, low, high, now);
}

/* The console sends a rumble event only when its rumble changes, and every SDL
 * rumble call ends after SS_MOTOR_SAFETY_MS: a steady rumble on a pad is sent
 * again every RUMBLE_RENEW_MS so it lasts as long as the console asks. (The
 * handheld's motor keeps its own safety stop, ss_motor_tick.) */
static void renew_rumble(StreamApp *app, uint32_t now)
{
    if(now - app->rumble_renewed_at < RUMBLE_RENEW_MS) return;
    app->rumble_renewed_at = now;
    for(int i = 0; i < MAX_PADS; i++) {
        StreamPad *pad = &app->pads[i];
        if(pad->used && (pad->rumble_low || pad->rumble_high))
            pad_rumble_send(pad, pad->rumble_low, pad->rumble_high, now);
    }
}

/* Rumble goes where the player is: to the pad with the latest input, and to
 * the handheld's own motor while that is the built-in pad (or before anything
 * was pressed). */
static void apply_rumble(StreamApp *app, uint32_t now)
{
    int left = atomic_load(&app->rumble_left);
    int right = atomic_load(&app->rumble_right);
    if(!app->rumble_enabled) left = right = 0;
    StreamPad *target = rumble_target(app);
    bool handheld = !target || target->builtin || !target->last_input;
    int strongest = left > right ? left : right;
    ss_motor_set(&app->motor, handheld ? (float)strongest / 255.0f : 0.0f, now);
    for(int i = 0; i < MAX_PADS; i++) {
        StreamPad *pad = &app->pads[i];
        if(!pad->used) continue;
        if(pad == target) pad_rumble(pad, (Uint16)(left * 257), (Uint16)(right * 257), now);
        else pad_rumble(pad, 0, 0, now);
    }
}

/* ---- pads ---- */

static bool controller_input(Uint8 button, SiInput *input)
{
    switch(button) {
        case SDL_CONTROLLER_BUTTON_A: *input = SI_IN_SOUTH; return true;
        case SDL_CONTROLLER_BUTTON_B: *input = SI_IN_EAST; return true;
        case SDL_CONTROLLER_BUTTON_X: *input = SI_IN_WEST; return true;
        case SDL_CONTROLLER_BUTTON_Y: *input = SI_IN_NORTH; return true;
        case SDL_CONTROLLER_BUTTON_BACK: *input = SI_IN_BACK; return true;
        case SDL_CONTROLLER_BUTTON_GUIDE: *input = SI_IN_GUIDE; return true;
        case SDL_CONTROLLER_BUTTON_START: *input = SI_IN_START; return true;
        case SDL_CONTROLLER_BUTTON_LEFTSTICK: *input = SI_IN_L3; return true;
        case SDL_CONTROLLER_BUTTON_RIGHTSTICK: *input = SI_IN_R3; return true;
        case SDL_CONTROLLER_BUTTON_LEFTSHOULDER: *input = SI_IN_L1; return true;
        case SDL_CONTROLLER_BUTTON_RIGHTSHOULDER: *input = SI_IN_R1; return true;
        case SDL_CONTROLLER_BUTTON_DPAD_UP: *input = SI_IN_UP; return true;
        case SDL_CONTROLLER_BUTTON_DPAD_DOWN: *input = SI_IN_DOWN; return true;
        case SDL_CONTROLLER_BUTTON_DPAD_LEFT: *input = SI_IN_LEFT; return true;
        case SDL_CONTROLLER_BUTTON_DPAD_RIGHT: *input = SI_IN_RIGHT; return true;
#if SDL_VERSION_ATLEAST(2, 0, 14)
        case SDL_CONTROLLER_BUTTON_TOUCHPAD: *input = SI_IN_TOUCHPAD; return true;
#endif
        default: return false;
    }
}

static bool controller_axis(Uint8 axis, SiAxis *out)
{
    switch(axis) {
        case SDL_CONTROLLER_AXIS_LEFTX: *out = SI_AXIS_LX; return true;
        case SDL_CONTROLLER_AXIS_LEFTY: *out = SI_AXIS_LY; return true;
        case SDL_CONTROLLER_AXIS_RIGHTX: *out = SI_AXIS_RX; return true;
        case SDL_CONTROLLER_AXIS_RIGHTY: *out = SI_AXIS_RY; return true;
        case SDL_CONTROLLER_AXIS_TRIGGERLEFT: *out = SI_AXIS_L2; return true;
        case SDL_CONTROLLER_AXIS_TRIGGERRIGHT: *out = SI_AXIS_R2; return true;
        default: return false;
    }
}

/* Raw layouts for pads SDL has no mapping for. A six-axis pad reports the
 * X360 (xpad) order: buttons A B X Y LB RB Back Start Guide LS RS, axes left X/Y,
 * left trigger, right X/Y, right trigger — the order "TRIMUI Player1" uses too.
 * Anything else gets the layout the old helper assumed (sticks on axes 0-3,
 * L2/R2 as buttons 6/7). */
static bool joystick_input(Uint8 button, bool xpad, SiInput *input)
{
    if(xpad) {
        switch(button) {
            case 0: *input = SI_IN_SOUTH; return true;
            case 1: *input = SI_IN_EAST; return true;
            case 2: *input = SI_IN_WEST; return true;
            case 3: *input = SI_IN_NORTH; return true;
            case 4: *input = SI_IN_L1; return true;
            case 5: *input = SI_IN_R1; return true;
            case 6: *input = SI_IN_BACK; return true;
            case 7: *input = SI_IN_START; return true;
            case 8: *input = SI_IN_GUIDE; return true;
            case 9: *input = SI_IN_L3; return true;
            case 10: *input = SI_IN_R3; return true;
            default: return false;
        }
    }
    switch(button) {
        case 1: *input = SI_IN_SOUTH; return true;
        case 0: *input = SI_IN_EAST; return true;
        case 3: *input = SI_IN_WEST; return true;
        case 2: *input = SI_IN_NORTH; return true;
        case 4: *input = SI_IN_L1; return true;
        case 5: *input = SI_IN_R1; return true;
        case 8: *input = SI_IN_BACK; return true;
        case 9: *input = SI_IN_START; return true;
        case 10: *input = SI_IN_GUIDE; return true;
        case 11: *input = SI_IN_L3; return true;
        case 12: *input = SI_IN_R3; return true;
        default: return false;
    }
}

/* A raw axis as the mapping's axis. In the xpad order axes 2 and 5 are the
 * triggers, which rest at -32768: as a stick they pinned it to one side. */
static bool joystick_axis(Uint8 axis, bool xpad, SiAxis *out)
{
    static const SiAxis xpad_axes[6] = {SI_AXIS_LX, SI_AXIS_LY, SI_AXIS_L2, SI_AXIS_RX, SI_AXIS_RY, SI_AXIS_R2};
    static const SiAxis plain_axes[4] = {SI_AXIS_LX, SI_AXIS_LY, SI_AXIS_RX, SI_AXIS_RY};
    if(xpad) {
        if(axis >= 6) return false;
        *out = xpad_axes[axis];
        return true;
    }
    if(axis >= 4) return false;
    *out = plain_axes[axis];
    return true;
}

static StreamPad *pad_find(StreamApp *app, SDL_JoystickID id)
{
    for(int i = 0; i < MAX_PADS; i++)
        if(app->pads[i].used && app->pads[i].id == id) return &app->pads[i];
    return NULL;
}

/* Test seams (tests/test_stream_e2e.c): the root pad identification reads sysfs
 * under ("" = the device's own), and a stand-in for SDL_JoystickPath. */
const char *stream_sysfs_root = "";
const char *(*stream_joystick_path_hook)(SDL_Joystick *joystick) = NULL;

typedef const char *(*JoystickPathFn)(SDL_Joystick *joystick);
_Static_assert(sizeof(JoystickPathFn) == sizeof(void *), "dlsym result fits a function pointer");

/* A pad's event node ("/dev/input/eventN"), or NULL. SDL_JoystickPath is SDL
 * 2.24's, so it is looked up at run time: the helper still starts on a
 * firmware whose SDL2 is older, and identifies the built-in pad without it. */
static const char *joystick_path(SDL_Joystick *joystick)
{
    static bool looked_up;
    static JoystickPathFn path_fn;
    if(stream_joystick_path_hook) return stream_joystick_path_hook(joystick);
    if(!looked_up) {
        looked_up = true;
        void *self = dlopen(NULL, RTLD_LAZY);
        if(self) {
            void *symbol = dlsym(self, "SDL_JoystickPath");
            if(symbol) memcpy(&path_fn, &symbol, sizeof(path_fn));
            dlclose(self);
        }
    }
    return path_fn ? path_fn(joystick) : NULL;
}

#if SDL_VERSION_ATLEAST(2, 0, 12)
static bool sdl_linked_at_least(int major, int minor, int patch)
{
    SDL_version linked;
    SDL_GetVersion(&linked);
    return SDL_VERSIONNUM(linked.major, linked.minor, linked.patch) >= SDL_VERSIONNUM(major, minor, patch);
}
#endif

/* A DualShock 4 or DualSense brings its own touchpad. SDL names the pad's type
 * from 2.0.12 on; with an older SDL the pad gets the handheld's layout. */
static bool controller_has_touchpad(SDL_GameController *controller)
{
#if SDL_VERSION_ATLEAST(2, 0, 12)
    if(!controller || !sdl_linked_at_least(2, 0, 12)) return false;
    SDL_GameControllerType type = SDL_GameControllerGetType(controller);
    if(type == SDL_CONTROLLER_TYPE_PS4) return true;
#if SDL_VERSION_ATLEAST(2, 0, 14)
    if(type == SDL_CONTROLLER_TYPE_PS5) return true;
#endif
    return false;
#else
    (void)controller;
    return false;
#endif
}

static void pad_note_input(StreamPad *pad, uint32_t now)
{
    pad->last_input = now ? now : 1;
}

/* Opens the device at index unless it is open already: every pad attached
 * when the stream starts, and every one that connects during it. */
static void pad_open(StreamApp *app, int index, bool announce)
{
    SDL_GameController *controller = NULL;
    SDL_Joystick *joystick = NULL;
    const char *name = NULL;
    if(SDL_IsGameController(index)) {
        controller = SDL_GameControllerOpen(index);
        if(!controller) {
            fprintf(stderr, "[native] cannot open controller %d: %s\n", index, SDL_GetError());
            return;
        }
        joystick = SDL_GameControllerGetJoystick(controller);
        name = SDL_GameControllerName(controller);
    } else {
        joystick = SDL_JoystickOpen(index);
        if(!joystick) {
            fprintf(stderr, "[native] cannot open joystick %d: %s\n", index, SDL_GetError());
            return;
        }
        name = SDL_JoystickName(joystick);
    }
    SDL_JoystickID id = SDL_JoystickInstanceID(joystick);
    /* SDL counts opens: a pad already in the table (SDL also announces the pads
     * that were open at start) gives this one straight back. */
    if(pad_find(app, id)) {
        if(controller) SDL_GameControllerClose(controller);
        else SDL_JoystickClose(joystick);
        return;
    }
    if(!controller && SDL_JoystickNumButtons(joystick) < 4) {
        /* A sensor can be listed as a joystick too, and its axes would win the
         * stick merge. A pad has buttons. */
        fprintf(stdout, "[native] joystick ignored, too few buttons: %s\n", name && *name ? name : "unnamed");
        fflush(stdout);
        SDL_JoystickClose(joystick);
        return;
    }
    StreamPad *slot = NULL;
    for(int i = 0; i < MAX_PADS && !slot; i++)
        if(!app->pads[i].used) slot = &app->pads[i];
    if(!slot) {
        fprintf(stdout, "[native] controller ignored, %d are open already\n", MAX_PADS);
        fflush(stdout);
        if(controller) SDL_GameControllerClose(controller);
        else SDL_JoystickClose(joystick);
        return;
    }
    memset(slot, 0, sizeof(*slot));
    slot->used = true;
    slot->id = id;
    slot->controller = controller;
    slot->joystick = controller ? NULL : joystick;
    slot->xpad_layout = !controller && SDL_JoystickNumAxes(joystick) >= 6;
    snprintf(slot->name, sizeof(slot->name), "%s", name && *name ? name : "unnamed controller");
    /* SDL calls the built-in pad "Xbox 360 Controller", after its USB id: the
     * id and the kernel's name for it tell it apart (stream_sys.h). */
    slot->builtin = ss_builtin_pad(stream_sysfs_root, slot->name, SDL_JoystickGetVendor(joystick),
                                   SDL_JoystickGetProduct(joystick), joystick_path(joystick));
    bool touchpad = controller_has_touchpad(controller);
    uint32_t flags = (slot->builtin ? SI_PAD_BUILTIN : 0u) | (touchpad ? SI_PAD_TOUCHPAD : 0u);
    if(!si_pad_attach(&app->input, id, flags)) {
        /* No room in the merge: a pad whose input goes nowhere must not take
         * the rumble either. */
        fprintf(stdout, "[native] controller ignored, %d are open already: %s\n", MAX_PADS, slot->name);
        fflush(stdout);
        if(controller) SDL_GameControllerClose(controller);
        else SDL_JoystickClose(joystick);
        memset(slot, 0, sizeof(*slot));
        return;
    }
    fprintf(stdout, "[native] controller added: %s (%s%s%s)\n", slot->name,
            controller ? "game controller" : slot->xpad_layout ? "joystick, X360 order" : "joystick",
            slot->builtin ? ", built-in" : "", touchpad ? ", touchpad" : "");
    fflush(stdout);
    if(announce) toast(app, "Controller connected", OSD_TOAST_GOOD, TOAST_SHORT_MS);
}

/* Lets a pad go; whatever it held is released on the console at once. */
static void pad_close(StreamApp *app, StreamPad *pad, uint32_t now, bool live)
{
    if(!pad || !pad->used) return;
    si_pad_detach(&app->input, pad->id, now);
    if(!live) pad_rumble(pad, 0, 0, now);
    if(pad->controller) SDL_GameControllerClose(pad->controller);
    else if(pad->joystick) SDL_JoystickClose(pad->joystick);
    if(live) {
        fprintf(stdout, "[native] controller removed: %s\n", pad->name);
        fflush(stdout);
        toast(app, "Controller disconnected", OSD_TOAST_WARN, TOAST_SHORT_MS);
    }
    memset(pad, 0, sizeof(*pad));
}

/* ---- the modal screens ---- */

static void menu_value(const StreamApp *app, MenuRow row, char *out, size_t size)
{
    out[0] = '\0';
    switch(row) {
        case ROW_SELECT: snprintf(out, size, "%s", app->input.select_mode == SI_SELECT_CREATE ? "Create" : "Touchpad"); break;
        case ROW_DPAD: snprintf(out, size, "%s", app->input.dpad_stick ? "Left stick" : "D-pad"); break;
        case ROW_RUMBLE: snprintf(out, size, "%s", app->rumble_enabled ? "On" : "Off"); break;
        case ROW_VOLUME: snprintf(out, size, "%u%%", atomic_load(&app->volume)); break;
        case ROW_STATS: snprintf(out, size, "%s", app->stats_visible ? "On" : "Off"); break;
        default: break;
    }
}

static const char *menu_label(MenuRow row)
{
    switch(row) {
        case ROW_RESUME: return "Resume";
        case ROW_QUIT: return "Quit Remote Play";
        case ROW_HOLD_PS: return "Hold PS (Home)";
        case ROW_SELECT: return "Select button";
        case ROW_DPAD: return "D-pad works as";
        case ROW_RUMBLE: return "Rumble";
        case ROW_VOLUME: return "Stream volume";
        case ROW_STATS: return "Stream stats";
        case ROW_CONTROLS: return "Controls";
        default: return "";
    }
}

static void set_rumble(StreamApp *app, bool on, uint32_t now)
{
    app->rumble_enabled = on;
    apply_rumble(app, now);
}

static void menu_change(StreamApp *app, int delta, uint32_t now)
{
    switch((MenuRow)app->menu_cursor) {
        case ROW_SELECT:
            si_set_select_mode(&app->input, app->input.select_mode == SI_SELECT_CREATE ? SI_SELECT_TOUCHPAD : SI_SELECT_CREATE);
            break;
        case ROW_DPAD:
            si_set_dpad_stick(&app->input, !app->input.dpad_stick);
            break;
        case ROW_RUMBLE:
            set_rumble(app, !app->rumble_enabled, now);
            break;
        case ROW_VOLUME: {
            int volume = (int)atomic_load(&app->volume) + delta * 10;
            if(volume < 0) volume = 0;
            if(volume > 100) volume = 100;
            atomic_store(&app->volume, (unsigned int)volume);
            break;
        }
        case ROW_STATS:
            app->stats_visible = !app->stats_visible;
            break;
        default:
            break;
    }
    app->osd_dirty = true;
}

static void menu_open(StreamApp *app, uint32_t now)
{
    if(app->pin_open || app->show_error) return;
    app->menu_open = true;
    app->menu_controls = false;
    app->menu_cursor = ROW_RESUME;
    update_modal(app, now);
}

static void menu_close(StreamApp *app, uint32_t now)
{
    app->menu_open = false;
    app->menu_controls = false;
    update_modal(app, now);
}

static void menu_nav(StreamApp *app, SiInput input, uint32_t now)
{
    if(app->menu_controls) {
        if(input == SI_IN_SOUTH || input == SI_IN_EAST || input == SI_IN_GUIDE) {
            app->menu_controls = false;
            app->osd_dirty = true;
        }
        return;
    }
    switch(input) {
        case SI_IN_UP:
            app->menu_cursor = (app->menu_cursor + ROW_COUNT - 1) % ROW_COUNT;
            break;
        case SI_IN_DOWN:
            app->menu_cursor = (app->menu_cursor + 1) % ROW_COUNT;
            break;
        case SI_IN_LEFT:
            menu_change(app, -1, now);
            break;
        case SI_IN_RIGHT:
            menu_change(app, 1, now);
            break;
        case SI_IN_EAST: /* TrimUI "A" */
            switch((MenuRow)app->menu_cursor) {
                case ROW_RESUME: menu_close(app, now); return;
                case ROW_QUIT: menu_close(app, now); request_quit(app); return;
                case ROW_HOLD_PS:
                    /* A Menu tap is a short PS press; the long one (the PS5's
                     * home screen, the PS4's quick menu) is only reachable here.
                     * The menu closes first: a modal screen sends an idle pad. */
                    menu_close(app, now);
                    si_hold_ps(&app->input, now, SI_PS_HOLD_MS);
                    toast(app, "Holding the PS button", OSD_TOAST_INFO, TOAST_SHORT_MS);
                    fprintf(stdout, "[native] PS button held from the stream menu\n");
                    fflush(stdout);
                    return;
                case ROW_CONTROLS: app->menu_controls = true; break;
                case ROW_VOLUME: menu_change(app, 1, now); break;
                default: menu_change(app, 1, now); break;
            }
            break;
        case SI_IN_SOUTH: /* TrimUI "B" */
        case SI_IN_GUIDE:
            menu_close(app, now);
            return;
        default:
            return;
    }
    app->osd_dirty = true;
}

static void pin_submit(StreamApp *app)
{
    uint8_t pin[4];
    for(int i = 0; i < 4; i++) pin[i] = (uint8_t)app->pin_digits[i];
    if(app->session_initialized) chiaki_session_set_login_pin(&app->session, pin, sizeof(pin));
    ss_wipe(pin, sizeof(pin));
    app->pin_checking = true;
    app->pin_wrong = false;
    fprintf(stdout, "[native] login passcode entered on screen\n");
    fflush(stdout);
    set_status(app, "Checking the passcode");
}

/* B on the passcode screen, or Start + Select held: the stream ends with rc 12. */
static void pin_cancel(StreamApp *app, uint32_t now)
{
    char text[200];
    textf(text, sizeof(text), "%s asked for its login passcode and it was not entered.", app->config->name);
    set_outcome(app, 12, "login_pin_cancelled", text);
    app->pin_open = false;
    update_modal(app, now);
    atomic_store(&app->running, false);
}

static void pin_nav(StreamApp *app, SiInput input, uint32_t now)
{
    /* While the console checks the passcode only B (give up) does anything:
     * the digits are on their way. */
    if(app->pin_checking) {
        if(input == SI_IN_SOUTH) pin_cancel(app, now);
        return;
    }
    char *digit = &app->pin_digits[app->pin_cursor];
    switch(input) {
        case SI_IN_UP: *digit = (char)(*digit >= '9' ? '0' : *digit + 1); break;
        case SI_IN_DOWN: *digit = (char)(*digit <= '0' ? '9' : *digit - 1); break;
        case SI_IN_LEFT: app->pin_cursor = (app->pin_cursor + 3) % 4; break;
        case SI_IN_RIGHT: app->pin_cursor = (app->pin_cursor + 1) % 4; break;
        case SI_IN_EAST: /* TrimUI "A" only: Start is half of the Start + Select cancel */
            pin_submit(app);
            break;
        case SI_IN_SOUTH: /* TrimUI "B" */
            pin_cancel(app, now);
            return;
        default:
            return;
    }
    app->osd_dirty = true;
}

static void modal_nav(StreamApp *app, SiInput input, uint32_t now)
{
    if(app->pin_open) pin_nav(app, input, now);
    else if(app->menu_open) menu_nav(app, input, now);
}

/* ---- events ---- */

static void pad_button(StreamApp *app, int32_t id, SiInput input, bool down, uint32_t now)
{
    bool was_modal = app->modal;
    si_pad_button(&app->input, id, input, down, now);
    if(down && was_modal) modal_nav(app, input, now);
}

static void joystick_hat(StreamApp *app, StreamPad *pad, Uint8 value, uint32_t now)
{
    static const struct { Uint8 mask; SiInput input; } dirs[] = {
        {SDL_HAT_UP, SI_IN_UP}, {SDL_HAT_DOWN, SI_IN_DOWN}, {SDL_HAT_LEFT, SI_IN_LEFT}, {SDL_HAT_RIGHT, SI_IN_RIGHT},
    };
    for(size_t i = 0; i < sizeof(dirs) / sizeof(dirs[0]); i++) {
        bool before = (pad->hat & dirs[i].mask) != 0;
        bool after = (value & dirs[i].mask) != 0;
        if(before != after) pad_button(app, pad->id, dirs[i].input, after, now);
    }
    pad->hat = value;
}

static void handle_event(StreamApp *app, const SDL_Event *event, uint32_t now, bool sdl_touchpad)
{
    (void)sdl_touchpad; /* touchpad events exist from SDL 2.0.14 on */
    switch(event->type) {
        case SDL_QUIT:
            request_quit(app);
            break;
        case SDL_KEYDOWN:
            if(event->key.keysym.sym == SDLK_ESCAPE) request_quit(app);
            break;
        case SDL_CONTROLLERDEVICEADDED:
            /* "which" is a device index here; a pad already open is skipped. */
            pad_open(app, event->cdevice.which, now - app->start_ticks > 2000u);
            break;
        case SDL_JOYDEVICEADDED:
            /* A game controller arrives as both events; its controller event
             * opens it with its mapping. */
            if(!SDL_IsGameController(event->jdevice.which))
                pad_open(app, event->jdevice.which, now - app->start_ticks > 2000u);
            break;
        case SDL_CONTROLLERDEVICEREMOVED:
            pad_close(app, pad_find(app, (SDL_JoystickID)event->cdevice.which), now, true);
            break;
        case SDL_JOYDEVICEREMOVED:
            pad_close(app, pad_find(app, (SDL_JoystickID)event->jdevice.which), now, true);
            break;
        case SDL_CONTROLLERBUTTONDOWN:
        case SDL_CONTROLLERBUTTONUP: {
            StreamPad *pad = pad_find(app, event->cbutton.which);
            SiInput input;
            if(!pad || !pad->controller || !controller_input(event->cbutton.button, &input)) break;
            bool down = event->type == SDL_CONTROLLERBUTTONDOWN;
            if(down) pad_note_input(pad, now);
            pad_button(app, pad->id, input, down, now);
            break;
        }
        case SDL_CONTROLLERAXISMOTION: {
            StreamPad *pad = pad_find(app, event->caxis.which);
            SiAxis axis;
            if(!pad || !pad->controller || !controller_axis(event->caxis.axis, &axis)) break;
            if(event->caxis.value > PAD_ACTIVE_AXIS || event->caxis.value < -PAD_ACTIVE_AXIS) pad_note_input(pad, now);
            si_pad_axis(&app->input, pad->id, axis, event->caxis.value, now);
            break;
        }
#if SDL_VERSION_ATLEAST(2, 0, 14)
        case SDL_CONTROLLERTOUCHPADDOWN:
        case SDL_CONTROLLERTOUCHPADMOTION:
        case SDL_CONTROLLERTOUCHPADUP: {
            StreamPad *pad = pad_find(app, event->ctouchpad.which);
            if(!sdl_touchpad || !pad || !pad->controller || event->ctouchpad.touchpad != 0) break;
            pad_note_input(pad, now);
            si_pad_touch(&app->input, pad->id, event->ctouchpad.finger, event->type != SDL_CONTROLLERTOUCHPADUP,
                         event->ctouchpad.x, event->ctouchpad.y, now);
            break;
        }
#endif
        /* Raw joystick events only count for pads without a mapping: a game
         * controller reports every press twice (as itself and as its raw
         * joystick), and the raw copy of Menu and L3 used to drive the quit
         * combo. */
        case SDL_JOYBUTTONDOWN:
        case SDL_JOYBUTTONUP: {
            StreamPad *pad = pad_find(app, event->jbutton.which);
            if(!pad || pad->controller) break;
            bool down = event->type == SDL_JOYBUTTONDOWN;
            SiInput input;
            if(down) pad_note_input(pad, now);
            if(!pad->xpad_layout && event->jbutton.button == 6) si_pad_axis(&app->input, pad->id, SI_AXIS_L2, down ? 32767 : 0, now);
            else if(!pad->xpad_layout && event->jbutton.button == 7) si_pad_axis(&app->input, pad->id, SI_AXIS_R2, down ? 32767 : 0, now);
            else if(joystick_input(event->jbutton.button, pad->xpad_layout, &input)) pad_button(app, pad->id, input, down, now);
            break;
        }
        case SDL_JOYAXISMOTION: {
            StreamPad *pad = pad_find(app, event->jaxis.which);
            SiAxis axis;
            if(!pad || pad->controller || !joystick_axis(event->jaxis.axis, pad->xpad_layout, &axis)) break;
            int value = event->jaxis.value;
            /* A raw trigger runs -32768 (released) .. 32767 (pressed). */
            if(axis == SI_AXIS_L2 || axis == SI_AXIS_R2) value = (value + 32768) / 2;
            if(value > PAD_ACTIVE_AXIS || value < -PAD_ACTIVE_AXIS) pad_note_input(pad, now);
            si_pad_axis(&app->input, pad->id, axis, (int16_t)value, now);
            break;
        }
        case SDL_JOYHATMOTION: {
            StreamPad *pad = pad_find(app, event->jhat.which);
            if(!pad || pad->controller || event->jhat.hat != 0) break;
            if(event->jhat.value) pad_note_input(pad, now);
            joystick_hat(app, pad, event->jhat.value, now);
            break;
        }
        default:
            break;
    }
}

static void handle_input_events(StreamApp *app, uint32_t now)
{
    SiEvent event;
    while(si_poll_event(&app->input, &event)) {
        switch(event.type) {
            case SI_EV_OPEN_MENU:
                menu_open(app, now);
                break;
            case SI_EV_QUIT:
                if(app->pin_open) {
                    pin_cancel(app, now);
                } else {
                    fprintf(stdout, "[native] exit requested by START+SELECT\n");
                    request_quit(app);
                }
                break;
            case SI_EV_DPAD_STICK:
                toast(app, event.value ? "D-pad now moves the left stick" : "D-pad is the D-pad again", OSD_TOAST_INFO, TOAST_SHORT_MS);
                break;
            case SI_EV_SWIPE: {
                static const char *const names[] = {"Swipe up", "Swipe down", "Swipe left", "Swipe right"};
                if(event.value >= 0 && event.value < 4) toast(app, names[event.value], OSD_TOAST_INFO, TOAST_SHORT_MS);
                break;
            }
            case SI_EV_ALT_BUTTON:
                toast(app, event.value == (int)SI_OUT_SHARE ? "Create" : "Touchpad click", OSD_TOAST_INFO, TOAST_SHORT_MS);
                break;
            case SI_EV_TOUCH_CLICK:
                toast(app, "Touchpad click", OSD_TOAST_INFO, TOAST_SHORT_MS);
                break;
            default:
                break;
        }
    }
}

static void connect_message(StreamApp *app, char *out, size_t size)
{
    const SsConfig *config = app->config;
    if(config->remote)
        textf(out, size, "%s did not answer at %s (%s). Check that it is on and that the internet address and the router's port forwarding are right.",
                 config->name, config->host, app->quit_reason);
    else
        textf(out, size, "%s did not answer at %s (%s). Check that it is on, Remote Play is enabled on it, and this handheld is on the same network.",
                 config->name, config->host, app->quit_reason);
}

static void handle_session(StreamApp *app, uint32_t now)
{
    if(!app->connected_seen && atomic_load(&app->connected)) {
        app->connected_seen = true;
        app->connected_at = now;
        if(app->pin_open) {
            app->pin_open = false;
            update_modal(app, now);
        }
        set_status(app, "Connected, waiting for the video");
    }
    if(atomic_exchange(&app->pin_requested, false)) {
        bool incorrect = atomic_load(&app->pin_incorrect);
        app->pin_prompted = true;
        if(app->saved_pin[0] && !app->saved_pin_tried && !incorrect) {
            uint8_t pin[4];
            app->saved_pin_tried = true;
            memcpy(pin, app->saved_pin, 4);
            ss_wipe(app->saved_pin, sizeof(app->saved_pin)); /* tried once, never again */
            if(app->session_initialized) chiaki_session_set_login_pin(&app->session, pin, sizeof(pin));
            ss_wipe(pin, sizeof(pin));
            fprintf(stdout, "[native] entered the saved login passcode\n");
            fflush(stdout);
            set_status(app, "Entering the saved login passcode");
        } else {
            if(!app->pin_open) {
                app->pin_open = true;
                app->pin_cursor = 0;
                if(!app->pin_digits[0]) memcpy(app->pin_digits, "0000", 4);
                app->menu_open = false; /* the passcode screen replaces the menu */
                app->menu_controls = false;
            }
            app->pin_checking = false;
            app->pin_wrong = incorrect || app->saved_pin_tried;
            if(app->saved_pin_tried) app->saved_pin_rejected = true;
            set_status(app, "Waiting for the login passcode");
            update_modal(app, now);
        }
    }
    if(atomic_exchange(&app->rumble_dirty, false)) apply_rumble(app, now);
    if(atomic_load(&app->quit_received) && !app->quit_handled) {
        app->quit_handled = true;
        bool error = atomic_load(&app->quit_error);
        if(!app->result) {
            char text[300];
            if(!app->connected_seen && !app->first_frame_at && app->pin_prompted) {
                /* It did answer: it asked for the passcode, then ended the
                 * connection. "Did not answer" would send the user to check
                 * the network instead of the passcode. */
                textf(text, sizeof(text), "%s ended the connection at the login passcode step (%s). Check the 4-digit "
                      "passcode of the PlayStation profile that plays on it.", app->config->name, app->quit_reason);
                set_outcome(app, 11, "login_pin_failed", text);
                textf(app->error_title, sizeof(app->error_title), "%s refused the login", app->config->name);
                app->show_error = true;
            } else if(!app->connected_seen && !app->first_frame_at) {
                connect_message(app, text, sizeof(text));
                set_outcome(app, 11, "connect_failed", text);
                textf(app->error_title, sizeof(app->error_title), "Couldn't connect to %s", app->config->name);
                app->show_error = true;
            } else {
                textf(text, sizeof(text), "%s ended the stream: %s.", app->config->name, app->quit_reason);
                set_outcome(app, error ? 11 : 0, "console_quit", text);
                textf(app->error_title, sizeof(app->error_title), "The stream ended");
                app->show_error = error;
            }
        }
        atomic_store(&app->running, false);
    }
}

static void battery_tick(StreamApp *app, uint32_t now)
{
    if(app->battery_at && now - app->battery_at < BATTERY_POLL_MS) return;
    app->battery_at = now ? now : 1;
    if(!ss_battery_read("", &app->battery) || !app->battery.valid) return;
    if(app->battery.on_power) {
        app->battery_warned = 101;
        return;
    }
    static const int thresholds[] = {15, 10, 5};
    for(size_t i = 0; i < sizeof(thresholds) / sizeof(thresholds[0]); i++) {
        int level = thresholds[i];
        if(app->battery.percent <= level && app->battery_warned > level) {
            char text[96];
            app->battery_warned = level;
            textf(text, sizeof(text), "Battery %d%% - charge soon", app->battery.percent);
            toast(app, text, OSD_TOAST_WARN, TOAST_LONG_MS);
        }
    }
}

/* ---- video and drawing ---- */

static bool update_video(StreamApp *app, bool *new_frame)
{
    int32_t frames_lost = 0;
    ChiakiFfmpegFrame decoded = chiaki_ffmpeg_decoder_pull_frame(&app->decoder, &frames_lost);
    if(!decoded.frame) return true;
    app->lost_frames += frames_lost > 0 ? (uint64_t)frames_lost : 0;
    AVFrame *frame = decoded.frame;
    AVFrame *display = frame;

    if(frame->format != AV_PIX_FMT_YUV420P && frame->format != AV_PIX_FMT_YUVJ420P) {
        app->sws = sws_getCachedContext(app->sws, frame->width, frame->height, frame->format,
                                       frame->width, frame->height, AV_PIX_FMT_YUV420P,
                                       SWS_FAST_BILINEAR, NULL, NULL, NULL);
        if(!app->sws) {
            fprintf(stderr, "[native] cannot convert video pixel format %d\n", frame->format);
            av_frame_free(&frame);
            return false;
        }
        /* The buffer the conversion writes into is the frame's size: a stream
         * that changes resolution gets a new one (the old one would overflow). */
        if(app->converted && (app->converted->width != frame->width || app->converted->height != frame->height))
            av_frame_free(&app->converted);
        if(!app->converted) {
            app->converted = av_frame_alloc();
            if(!app->converted) {
                fprintf(stderr, "[native] cannot allocate converted frame\n");
                av_frame_free(&frame);
                return false;
            }
            app->converted->format = AV_PIX_FMT_YUV420P;
            app->converted->width = frame->width;
            app->converted->height = frame->height;
            if(av_frame_get_buffer(app->converted, 32) < 0) {
                fprintf(stderr, "[native] cannot allocate converted frame\n");
                av_frame_free(&app->converted);
                av_frame_free(&frame);
                return false;
            }
        }
        av_frame_make_writable(app->converted);
        sws_scale(app->sws, (const uint8_t * const *)frame->data, frame->linesize, 0, frame->height,
                  app->converted->data, app->converted->linesize);
        display = app->converted;
    }

    /* The texture is the picture's size too. A console that changes the
     * resolution mid-stream (its auto-downgrade) would otherwise have a smaller
     * frame over-read or a larger one cut into the old texture. */
    if(app->texture && (display->width != app->video_w || display->height != app->video_h)) {
        fprintf(stdout, "[native] video size changed: %dx%d -> %dx%d\n", app->video_w, app->video_h,
                display->width, display->height);
        fflush(stdout);
        SDL_DestroyTexture(app->texture);
        app->texture = NULL;
        app->destination_ready = false;
    }
    if(!app->texture) {
        app->texture = SDL_CreateTexture(app->renderer, SDL_PIXELFORMAT_IYUV,
                                         SDL_TEXTUREACCESS_STREAMING, display->width, display->height);
        if(!app->texture) {
            fprintf(stderr, "[native] SDL texture failed: %s\n", SDL_GetError());
            av_frame_free(&frame);
            return false;
        }
        int output_w = 0;
        int output_h = 0;
        SDL_GetRendererOutputSize(app->renderer, &output_w, &output_h);
        app->destination = (SDL_Rect){0, 0, output_w, output_h};
        double source_ratio = (double)display->width / display->height;
        double output_ratio = (double)output_w / output_h;
        if(output_ratio > source_ratio) {
            app->destination.w = (int)(output_h * source_ratio);
            app->destination.x = (output_w - app->destination.w) / 2;
        } else if(output_ratio < source_ratio) {
            app->destination.h = (int)(output_w / source_ratio);
            app->destination.y = (output_h - app->destination.h) / 2;
        }
        app->destination_ready = true;
        app->video_w = display->width;
        app->video_h = display->height;
        fprintf(stdout, "[native] first video frame: %dx%d format=%d\n", display->width, display->height, frame->format);
        fflush(stdout);
    }
    if(SDL_UpdateYUVTexture(app->texture, NULL,
                            display->data[0], display->linesize[0],
                            display->data[1], display->linesize[1],
                            display->data[2], display->linesize[2]) != 0) {
        fprintf(stderr, "[native] texture update failed: %s\n", SDL_GetError());
        av_frame_free(&frame);
        return false;
    }
    app->rendered_frames++;
    av_frame_free(&frame);
    *new_frame = true;
    return true;
}

static float fade_alpha(uint32_t now, uint32_t until, uint32_t fade_ms)
{
    if((int32_t)(now - until) >= 0) return 0.0f;
    uint32_t left = until - now;
    return left >= fade_ms ? 1.0f : (float)left / (float)fade_ms;
}

static void draw_menu(StreamApp *app, uint32_t now)
{
    OsdMenuView view;
    char line1[160], line2[160];
    memset(&view, 0, sizeof(view));
    view.title = app->config->name;
    unsigned long seconds = app->first_frame_at ? (unsigned long)(now - app->first_frame_at) / 1000ul : 0;
    char battery[48] = "";
    if(app->battery.valid)
        textf(battery, sizeof(battery), " - Battery %d%%%s", app->battery.percent, app->battery.on_power ? " (charging)" : "");
    textf(line1, sizeof(line1), "Streaming %lu:%02lu%s", seconds / 60, seconds % 60, battery);
    if(app->video_w > 0)
        textf(line2, sizeof(line2), "%s - %dx%d - %u fps - %u kb/s", codec_label(app->config), app->video_w, app->video_h,
                 app->config->fps, app->config->bitrate);
    else
        textf(line2, sizeof(line2), "%s - %s", codec_label(app->config), app->config->host);
    view.line1 = line1;
    view.line2 = line2;
    view.count = ROW_COUNT;
    view.cursor = app->menu_cursor;
    view.show_controls = app->menu_controls;
    view.select_create = app->input.select_mode == SI_SELECT_CREATE;
    view.dpad_stick = app->input.dpad_stick;
    for(int i = 0; i < ROW_COUNT; i++) {
        snprintf(view.rows[i].label, sizeof(view.rows[i].label), "%s", menu_label((MenuRow)i));
        menu_value(app, (MenuRow)i, view.rows[i].value, sizeof(view.rows[i].value));
    }
    osd_draw_menu(&app->osd, &view);
}

static void draw_overlays(StreamApp *app, uint32_t now)
{
    Osd *osd = &app->osd;
    if(!app->osd_ready) return;
    if(!app->first_frame_at && !app->pin_open) {
        char title[160], detail[300];
        textf(title, sizeof(title), "Connecting to %s", app->config->name);
        if(app->config->remote) textf(detail, sizeof(detail), "Internet address %s - %s", app->config->host, codec_label(app->config));
        else textf(detail, sizeof(detail), "%s - %s - %ux%u", app->config->host, codec_label(app->config), app->config->width, app->config->height);
        OsdConnectView view = {title, detail, app->status_line, (now - app->start_ticks) / 1000u, now,
                               "Hold START + SELECT to cancel"};
        osd_draw_connect(osd, &view);
    }
    if(app->first_frame_at && app->stats_visible) osd_draw_stats(osd, app->stats_line);
    uint16_t tx, ty;
    if(si_synthetic_touch(&app->input, now, &tx, &ty))
        osd_draw_touch(osd, tx, ty, app->input.touch_max_x, app->input.touch_max_y);
    if(app->hint_until && !app->modal) {
        float alpha = fade_alpha(now, app->hint_until, 500);
        if(alpha > 0.0f) osd_draw_hint(osd, app->input.select_mode == SI_SELECT_CREATE, alpha);
        else app->hint_until = 0;
    }
    if(app->toast_until) {
        float alpha = fade_alpha(now, app->toast_until, 300);
        if(alpha > 0.0f) osd_draw_toast(osd, app->toast, app->toast_kind, alpha);
        else app->toast_until = 0;
    }
    float quit = si_quit_progress(&app->input, now);
    if(quit > 0.0f) osd_draw_quit(osd, quit);
    if(app->menu_open) draw_menu(app, now);
    if(app->pin_open) {
        OsdPinView view;
        memset(&view, 0, sizeof(view));
        view.name = app->config->name;
        memcpy(view.digits, app->pin_digits, 4);
        view.cursor = app->pin_cursor;
        view.wrong = app->pin_wrong;
        view.checking = app->pin_checking;
        osd_draw_pin(osd, &view);
    }
}

static void present(StreamApp *app, uint32_t now)
{
    bool fills_output = app->texture && app->destination_ready && app->destination.x == 0 &&
                        app->destination.y == 0 && app->destination.w == app->osd.out_w &&
                        app->destination.h == app->osd.out_h;
    if(!fills_output) {
        SDL_SetRenderDrawColor(app->renderer, 0, 0, 0, 255);
        SDL_RenderClear(app->renderer);
    }
    if(app->texture) SDL_RenderCopy(app->renderer, app->texture, NULL, &app->destination);
    draw_overlays(app, now);
    SDL_RenderPresent(app->renderer);
    app->osd_dirty = false;
    app->last_present = now;
}

static bool animating(const StreamApp *app, uint32_t now)
{
    uint16_t x, y;
    return !app->first_frame_at || app->toast_until || app->hint_until ||
           si_quit_progress(&app->input, now) > 0.0f || si_synthetic_touch(&app->input, now, &x, &y);
}

/* A failure the user should read before JukaHub comes back. The session has
 * been stopped and joined first (stop_session), so nothing else runs: this is
 * its own small loop. A stop signal ends it like a button does. */
static void run_error_card(StreamApp *app)
{
    if(!app->osd_ready || !app->renderer) return;
    if(app->audio_device) SDL_PauseAudioDevice(app->audio_device, 1);
    uint32_t start = SDL_GetTicks();
    for(;;) {
        uint32_t now = SDL_GetTicks();
        uint32_t shown = now - start;
        if(shown >= ERROR_CARD_MS || signalled) break;
        bool dismissed = false;
        SDL_Event event;
        while(SDL_PollEvent(&event)) {
            if(event.type == SDL_QUIT) dismissed = true;
            bool press = event.type == SDL_CONTROLLERBUTTONDOWN || event.type == SDL_JOYBUTTONDOWN || event.type == SDL_KEYDOWN;
            if(press && shown >= ERROR_CARD_GRACE_MS) dismissed = true;
        }
        if(dismissed) break;
        char footer[64];
        textf(footer, sizeof(footer), "Press any button to go back to JukaHub (%u s)",
                 (unsigned int)((ERROR_CARD_MS - shown + 999u) / 1000u));
        SDL_SetRenderDrawColor(app->renderer, 0, 0, 0, 255);
        SDL_RenderClear(app->renderer);
        if(app->texture) SDL_RenderCopy(app->renderer, app->texture, NULL, &app->destination);
        OsdErrorView view = {app->error_title, app->message, footer};
        osd_draw_error(&app->osd, &view);
        SDL_RenderPresent(app->renderer);
        SDL_Delay(30);
    }
}

static void fail_with_card(StreamApp *app, int rc, const char *result, const char *title, const char *message)
{
    set_outcome(app, rc, result, message);
    textf(app->error_title, sizeof(app->error_title), "%s", title);
    app->show_error = true;
}

/* The stream is over: no rumble may outlive it, on the handheld's motor or on
 * any pad, whatever card or cleanup comes next. */
static void stop_rumble(StreamApp *app)
{
    app->rumble_enabled = false;
    apply_rumble(app, SDL_GetTicks());
    ss_motor_off(&app->motor);
}

/* Lets the console go: an idle pad first (nothing stays pressed while the
 * session winds down), then stop and join. After this no chiaki thread runs, so
 * quit_reason and the audio device can be read safely. Safe to call twice. */
static void stop_session(StreamApp *app)
{
    if(!app->session_started) return;
    chiaki_controller_state_set_idle(&app->controller_state);
    for(int i = 0; i < SI_MAX_TOUCHES; i++) {
        app->touch_active[i] = false;
        app->touch_ids[i] = -1;
    }
    if(app->session_initialized) chiaki_session_set_controller_state(&app->session, &app->controller_state);
    chiaki_session_stop(&app->session);
    chiaki_session_join(&app->session);
    app->session_started = false;
}

/* ---- setup ---- */

static bool init_sdl(StreamApp *app)
{
    /* Positional face buttons for Nintendo-style pads too, and rumble for
     * DualShock 4 / DualSense over Bluetooth. Unknown hints are ignored. */
    SDL_SetHint("SDL_GAMECONTROLLER_USE_BUTTON_LABELS", "0");
    SDL_SetHint("SDL_JOYSTICK_HIDAPI_PS4_RUMBLE", "1");
    SDL_SetHint("SDL_JOYSTICK_HIDAPI_PS5_RUMBLE", "1");
    SDL_SetHint(SDL_HINT_JOYSTICK_ALLOW_BACKGROUND_EVENTS, "1");
    if(SDL_Init(SDL_INIT_VIDEO | SDL_INIT_AUDIO | SDL_INIT_JOYSTICK | SDL_INIT_GAMECONTROLLER | SDL_INIT_HAPTIC) != 0) {
        fprintf(stderr, "[native] SDL init failed: %s\n", SDL_GetError());
        return false;
    }
    /* Before any pad is opened, so SDL_IsGameController sees it. */
    if(SDL_GameControllerAddMapping(TRIMUI_PAD_MAPPING) < 0)
        fprintf(stderr, "[native] could not add the TrimUI pad mapping: %s\n", SDL_GetError());
    SDL_SetHint(SDL_HINT_RENDER_SCALE_QUALITY, "linear");
    app->window = SDL_CreateWindow(APP_NAME, SDL_WINDOWPOS_UNDEFINED, SDL_WINDOWPOS_UNDEFINED,
                                   SCREEN_WIDTH, SCREEN_HEIGHT, SDL_WINDOW_FULLSCREEN_DESKTOP | SDL_WINDOW_SHOWN);
    if(!app->window) {
        fprintf(stderr, "[native] SDL window failed: %s\n", SDL_GetError());
        return false;
    }
    app->renderer = SDL_CreateRenderer(app->window, -1, SDL_RENDERER_ACCELERATED | SDL_RENDERER_PRESENTVSYNC);
    if(!app->renderer) app->renderer = SDL_CreateRenderer(app->window, -1, SDL_RENDERER_SOFTWARE);
    if(!app->renderer) {
        fprintf(stderr, "[native] SDL renderer failed: %s\n", SDL_GetError());
        return false;
    }
    SDL_RendererInfo renderer_info;
    if(SDL_GetRendererInfo(app->renderer, &renderer_info) == 0) {
        fprintf(stdout, "[native] renderer=%s accelerated=%d vsync=%d max_texture=%dx%d\n",
                renderer_info.name ? renderer_info.name : "unknown",
                !!(renderer_info.flags & SDL_RENDERER_ACCELERATED),
                !!(renderer_info.flags & SDL_RENDERER_PRESENTVSYNC),
                renderer_info.max_texture_width, renderer_info.max_texture_height);
    }
    SDL_ShowCursor(SDL_DISABLE);
    app->osd_ready = osd_init(&app->osd, app->renderer);
    if(!app->osd_ready) fprintf(stderr, "[native] on-screen display unavailable: %s\n", SDL_GetError());
    SDL_SetRenderDrawColor(app->renderer, 0, 0, 0, 255);
    SDL_RenderClear(app->renderer);
    SDL_RenderPresent(app->renderer);

    int count = SDL_NumJoysticks();
    for(int index = 0; index < count; index++) pad_open(app, index, false);
    bool any = false;
    for(int i = 0; i < MAX_PADS; i++) any = any || app->pads[i].used;
    if(!any) fprintf(stdout, "[native] no controller yet; one that connects during the stream is used\n");
    fflush(stdout);
    return true;
}

static void cleanup(StreamApp *app)
{
    stop_session(app);
    if(app->session_initialized) chiaki_session_fini(&app->session);
    app->session_initialized = false;
    if(app->opus_initialized) chiaki_opus_decoder_fini(&app->opus);
    if(app->decoder_initialized) chiaki_ffmpeg_decoder_fini(&app->decoder);
    ss_motor_off(&app->motor);
    ss_wipe(app->saved_pin, sizeof(app->saved_pin));
    ss_wipe(app->pin_digits, sizeof(app->pin_digits));
    if(app->audio_device) SDL_CloseAudioDevice(app->audio_device);
    if(app->converted) av_frame_free(&app->converted);
    if(app->sws) sws_freeContext(app->sws);
    if(app->texture) SDL_DestroyTexture(app->texture);
    if(app->osd_ready) osd_fini(&app->osd);
    for(int i = 0; i < MAX_PADS; i++) pad_close(app, &app->pads[i], SDL_GetTicks(), false);
    if(app->renderer) SDL_DestroyRenderer(app->renderer);
    if(app->window) SDL_DestroyWindow(app->window);
    SDL_Quit();
}

static void finish(StreamApp *app, const SsConfig *config)
{
    if(!app->result) request_quit(app);
    unsigned long seconds = 0;
    if(app->first_frame_at) seconds = (unsigned long)(SDL_GetTicks() - app->first_frame_at) / 1000ul;
    char ended[96];
    if(app->result && !strcmp(app->result, "ok")) {
        textf(ended, sizeof(ended), "Streamed for %lu min %lu s.", seconds / 60, seconds % 60);
        textf(app->message, sizeof(app->message), "%s", ended);
    }
    SsStatus status;
    memset(&status, 0, sizeof(status));
    status.rc = app->rc;
    status.result = app->result;
    status.message = app->message;
    status.connected = app->connected_seen || app->first_frame_at;
    status.seconds = seconds;
    status.frames = (unsigned long long)app->rendered_frames;
    status.quit_reason = app->quit_reason;
    status.host = config->host;
    status.codec = codec_name(config);
    status.saved_pin_rejected = app->saved_pin_rejected;
    if(config->status_file[0] && !ss_write_status(config->status_file, &status))
        fprintf(stderr, "[native] could not write the status file %s: %s\n", config->status_file, strerror(errno));
    fprintf(stdout, "[native] result=%s rc=%d connected=%d seconds=%lu frames=%llu\n", app->result, app->rc,
            status.connected ? 1 : 0, seconds, (unsigned long long)app->rendered_frames);
    fflush(stdout);
}

static void early_status(const SsConfig *config, int rc, const char *result, const char *message)
{
    if(!config->status_file[0]) return;
    SsStatus status;
    memset(&status, 0, sizeof(status));
    status.rc = rc;
    status.result = result;
    status.message = message;
    status.host = config->host;
    status.codec = codec_name(config);
    ss_write_status(config->status_file, &status);
    fprintf(stdout, "[native] result=%s rc=%d connected=0 seconds=0 frames=0\n", result, rc);
    fflush(stdout);
}

int main(int argc, char **argv)
{
    setvbuf(stdout, NULL, _IOFBF, 64 * 1024);
    if(argc != 2) {
        fprintf(stderr, "usage: chiaki-stream SESSION_FILE\n");
        return 2;
    }
    static SsConfig config;
    char load_error[160] = {0};
    if(!ss_load_config(argv[1], &config, load_error, sizeof(load_error))) {
        fprintf(stderr, "[native] %s\n", load_error);
        early_status(&config, 3, "invalid_session", "The pairing data is incomplete. Pair the console again in JukaHub.");
        return 3;
    }
    /* A PS5 is target 1000000 or newer (chiaki derives the rest from ps5=1);
     * anything lower is a PS4 firmware generation. */
    if((config.ps5 && config.target < 1000000) || (!config.ps5 && config.target >= 1000000)) {
        fprintf(stderr, "[native] protocol/target mismatch ps5=%d target=%u\n", config.ps5, config.target);
        early_status(&config, 3, "invalid_session", "The saved console type does not match its pairing. Pair the console again in JukaHub.");
        return 3;
    }

    StreamApp *app = &g_app;
    memset(app, 0, sizeof(*app));
    signalled = 0;
    app->config = &config;
    /* The saved passcode lives in one place, wiped once it has been tried. */
    memcpy(app->saved_pin, config.login_pin, sizeof(app->saved_pin));
    ss_wipe(config.login_pin, sizeof(config.login_pin));
    atomic_init(&app->frame_ready, false);
    atomic_init(&app->running, true);
    atomic_init(&app->connected, false);
    atomic_init(&app->volume, config.volume);
    atomic_init(&app->quit_received, false);
    atomic_init(&app->quit_error, false);
    atomic_init(&app->pin_requested, false);
    atomic_init(&app->pin_incorrect, false);
    atomic_init(&app->rumble_left, 0);
    atomic_init(&app->rumble_right, 0);
    atomic_init(&app->rumble_dirty, false);
    atomic_init(&app->fec_failures, 0);
    atomic_init(&app->suppressed_logs, 0);
    app->verbose_logs = config.verbose;
    app->rumble_enabled = config.rumble;
    app->battery_warned = 101;
    for(int i = 0; i < SI_MAX_TOUCHES; i++) app->touch_ids[i] = -1;
    si_init(&app->input, config.select_create ? SI_SELECT_CREATE : SI_SELECT_TOUCHPAD, config.dpad_stick,
            config.ps5 ? PS5_TOUCHPAD_MAX_X : PS4_TOUCHPAD_MAX_X, config.ps5 ? PS5_TOUCHPAD_MAX_Y : PS4_TOUCHPAD_MAX_Y);
    ss_motor_open(&app->motor, "", config.motor_gpio, config.motor_level, config.motor_level_min,
                  config.motor_level_max, config.motor_range_set);
    ss_motor_crash_guard(&app->motor);
    set_status(app, "Starting Remote Play");
    signal_app = app;
    signal(SIGINT, stop_signal);
    signal(SIGTERM, stop_signal);
#ifdef SIGHUP
    signal(SIGHUP, stop_signal);
#endif
#ifdef SIGQUIT
    signal(SIGQUIT, stop_signal);
#endif
#ifdef SIGPIPE
    /* A send on a socket the console closed must end in an error code chiaki
     * handles, not in a silent death without a status file. */
    signal(SIGPIPE, SIG_IGN);
#endif

    ChiakiErrorCode error = chiaki_lib_init();
    if(error != CHIAKI_ERR_SUCCESS) {
        fprintf(stderr, "[native] chiaki init failed: %s\n", chiaki_error_string(error));
        early_status(&config, 4, "init_failed", "Remote Play could not start its network library.");
        return 4;
    }
    ChiakiLogLevel log_mask = config.verbose
        ? CHIAKI_LOG_ALL
        : CHIAKI_LOG_INFO | CHIAKI_LOG_WARNING | CHIAKI_LOG_ERROR;
    chiaki_log_init(&app->log, log_mask, native_log, app);
    if(!init_sdl(app)) {
        char text[200];
        textf(text, sizeof(text), "The screen or sound could not start: %s", SDL_GetError());
        set_outcome(app, 5, "sdl_failed", text);
        finish(app, &config);
        cleanup(app);
        return 5;
    }
    app->start_ticks = SDL_GetTicks();
    app->last_stats_ticks = app->start_ticks;
    present(app, app->start_ticks);
    if(app->motor.available) fprintf(stdout, "[native] rumble motor: %s\n", app->motor.gpio);
    fprintf(stdout, "[native] mapping: select=%s dpad_stick=%d rumble=%d\n",
            config.select_create ? "create" : "touchpad", config.dpad_stick, config.rumble);

    ChiakiCodec codec = config.codec == SS_CODEC_H265 ? CHIAKI_CODEC_H265 : CHIAKI_CODEC_H264;
    error = chiaki_ffmpeg_decoder_init(&app->decoder, &app->log, codec, config.fps, NULL, NULL, frame_available, app);
    if(error != CHIAKI_ERR_SUCCESS) {
        char text[200];
        fprintf(stderr, "[native] FFmpeg decoder init failed: %s\n", chiaki_error_string(error));
        textf(text, sizeof(text), "The %s video decoder could not start (%s). Try the other video codec in JukaHub's Remote Play settings.",
                 codec_label(&config), chiaki_error_string(error));
        fail_with_card(app, 6, "decoder_failed", "Video problem", text);
        finish(app, &config);
        run_error_card(app);
        cleanup(app);
        return 6;
    }
    app->decoder_initialized = true;

    chiaki_opus_decoder_init(&app->opus, &app->log);
    app->opus_initialized = true;
    chiaki_opus_decoder_set_cb(&app->opus, audio_settings, audio_frame, app);

    ChiakiConnectInfo connect_info;
    memset(&connect_info, 0, sizeof(connect_info));
    connect_info.ps5 = config.ps5;
    connect_info.host = config.host;
    memcpy(connect_info.regist_key, config.regist_key, sizeof(connect_info.regist_key));
    memcpy(connect_info.morning, config.morning, sizeof(connect_info.morning));
    connect_info.video_profile.width = config.width;
    connect_info.video_profile.height = config.height;
    connect_info.video_profile.max_fps = config.fps;
    connect_info.video_profile.bitrate = config.bitrate;
    connect_info.video_profile.codec = codec;
    connect_info.video_profile_auto_downgrade = true;
    connect_info.packet_loss_max = 0.1;
    connect_info.enable_idr_on_fec_failure = true;

    fprintf(stdout, "[native] starting stream host=%s remote=%d profile=%ux%u@%u %ukbps codec=%s target=%u rp_version=%s\n",
            config.host, config.remote, config.width, config.height, config.fps, config.bitrate,
            config.codec == SS_CODEC_H265 ? "H265" : "H264", config.target,
            config.target == 800 ? "8.0" :
            config.target == 900 ? "9.0" :
            config.target >= 1000000 ? "1.0" : "10.0");
    fflush(stdout);
    ss_wipe(config.regist_key, sizeof(config.regist_key));
    ss_wipe(config.morning, sizeof(config.morning));

    error = chiaki_session_init(&app->session, &connect_info, &app->log);
    ss_wipe(connect_info.regist_key, sizeof(connect_info.regist_key));
    ss_wipe(connect_info.morning, sizeof(connect_info.morning));
    if(error != CHIAKI_ERR_SUCCESS) {
        char text[200];
        fprintf(stderr, "[native] session init failed: %s\n", chiaki_error_string(error));
        textf(text, sizeof(text), "Remote Play could not start: %s.", chiaki_error_string(error));
        fail_with_card(app, 7, "init_failed", "Remote Play couldn't start", text);
        finish(app, &config);
        run_error_card(app);
        cleanup(app);
        return 7;
    }
    if(!config.ps5 && (config.target == 800 || config.target == 900 || config.target == 1000)) {
        app->session.target = (ChiakiTarget)config.target;
        fprintf(stdout, "[native] session target=%u rp_version=%s\n",
                config.target,
                config.target == 800 ? "8.0" :
                config.target == 900 ? "9.0" : "10.0");
        fflush(stdout);
    }
    app->session_initialized = true;
    ChiakiAudioSink audio_sink;
    chiaki_opus_decoder_get_sink(&app->opus, &audio_sink);
    chiaki_session_set_audio_sink(&app->session, &audio_sink);
    chiaki_session_set_video_sample_cb(&app->session, chiaki_ffmpeg_decoder_video_sample_cb, &app->decoder);
    chiaki_session_set_event_cb(&app->session, session_event, app);
    chiaki_controller_state_set_idle(&app->controller_state);

    error = chiaki_session_start(&app->session);
    if(error != CHIAKI_ERR_SUCCESS) {
        char text[200];
        fprintf(stderr, "[native] session start failed: %s\n", chiaki_error_string(error));
        textf(text, sizeof(text), "Remote Play could not start: %s.", chiaki_error_string(error));
        fail_with_card(app, 8, "init_failed", "Remote Play couldn't start", text);
        finish(app, &config);
        run_error_card(app);
        cleanup(app);
        return 8;
    }
    app->session_started = true;
    battery_tick(app, SDL_GetTicks());

    bool sdl_touchpad = false;
#if SDL_VERSION_ATLEAST(2, 0, 14)
    {
        SDL_version linked;
        SDL_GetVersion(&linked);
        sdl_touchpad = SDL_VERSIONNUM(linked.major, linked.minor, linked.patch) >= SDL_VERSIONNUM(2, 0, 14);
    }
#endif

    SDL_Event event;
    while(atomic_load(&app->running)) {
        uint32_t now = SDL_GetTicks();
        while(SDL_PollEvent(&event)) handle_event(app, &event, now, sdl_touchpad);
        si_tick(&app->input, now);
        handle_input_events(app, now);
        handle_session(app, now);
        renew_rumble(app, now);
        push_controller(app, now);
        battery_tick(app, now);
        ss_motor_tick(&app->motor, now);
        bool new_frame = false;
        if(atomic_exchange(&app->frame_ready, false) && !update_video(app, &new_frame)) {
            fail_with_card(app, 10, "video_failed", "Video problem",
                           "The video stopped decoding. Try a lower quality or the other video codec in JukaHub's Remote Play settings.");
            atomic_store(&app->running, false);
            break;
        }
        if(new_frame && !app->first_frame_at) {
            app->first_frame_at = now ? now : 1;
            if(config.hints) app->hint_until = now + HINT_MS;
            set_status(app, "Streaming");
        }
        if(app->menu_open) {
            uint32_t second = (now - app->first_frame_at) / 1000u;
            if(second != app->menu_second) {
                app->menu_second = second;
                app->osd_dirty = true;
            }
        }
        if(new_frame || app->osd_dirty || (animating(app, now) && now - app->last_present >= 33u))
            present(app, now);
        if(now - app->last_stats_ticks >= 5000) {
            uint64_t rendered_delta = app->rendered_frames - app->stats_rendered_frames;
            uint64_t lost_delta = app->lost_frames - app->stats_lost_frames;
            uint64_t fec_total = atomic_load(&app->fec_failures);
            uint64_t suppressed_total = atomic_load(&app->suppressed_logs);
            uint64_t fec_delta = fec_total - app->stats_fec_failures;
            uint64_t suppressed_delta = suppressed_total - app->stats_suppressed_logs;
            double interval_seconds = (double)(now - app->last_stats_ticks) / 1000.0;
            double fps = interval_seconds > 0.0 ? (double)rendered_delta / interval_seconds : 0.0;
            fprintf(stdout, "[native] quality: rendered=%llu lost=%llu fec=%llu fps=%.1f suppressed=%llu totals=%llu/%llu/%llu\n",
                    (unsigned long long)rendered_delta,
                    (unsigned long long)lost_delta,
                    (unsigned long long)fec_delta,
                    fps,
                    (unsigned long long)suppressed_delta,
                    (unsigned long long)app->rendered_frames,
                    (unsigned long long)app->lost_frames,
                    (unsigned long long)fec_total);
            fflush(stdout);
            textf(app->stats_line, sizeof(app->stats_line), "%.1f fps - lost %llu - fec %llu - %dx%d %s",
                     fps, (unsigned long long)lost_delta, (unsigned long long)fec_delta, app->video_w, app->video_h,
                     codec_label(&config));
            if(app->stats_visible) app->osd_dirty = true;
            app->last_stats_ticks = now;
            app->stats_rendered_frames = app->rendered_frames;
            app->stats_lost_frames = app->lost_frames;
            app->stats_fec_failures = fec_total;
            app->stats_suppressed_logs = suppressed_total;
        }
        /* Polling with a 2 ms sleep, as the old helper did: ~500 wake-ups a
         * second even when nothing happens. A wait woken by the decoder's
         * frame callback would idle better (backlog; unchanged here). */
        SDL_Delay(2);
    }

    fprintf(stdout, "[native] stopping; connected=%d rendered=%llu lost=%llu\n",
            atomic_load(&app->connected), (unsigned long long)app->rendered_frames,
            (unsigned long long)app->lost_frames);
    fflush(stdout);
    /* In this order: rumble off and the session stopped (an idle pad, then stop
     * and join) before anything is reported or shown — an error card must not
     * keep the console holding the last buttons or the handheld buzzing, and
     * after the join no chiaki thread touches quit_reason or the audio device. */
    stop_rumble(app);
    stop_session(app);
    finish(app, &config);
    if(app->show_error) run_error_card(app);
    int rc = app->rc;
    cleanup(app);
    signal_app = NULL;
    return rc;
}
