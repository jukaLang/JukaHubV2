/* moonlight-stream - JukaHub's Moonlight (GameStream) streamer for TrimUI handhelds.
 *
 *   moonlight-stream SESSION_FILE
 *
 * JukaHub pairs with the PC (Vibepollo, Apollo, Sunshine or GeForce Experience),
 * launches the app over HTTPS, writes the session file (keys: README.md,
 * "Moonlight helper"), quits and hands the screen over. This helper deletes the
 * file, streams with moonlight-common-c (FFmpeg decode, Opus audio, SDL2 display
 * and sound) and writes a status file JukaHub reads when it comes back. The pads
 * go through stream_input.c (the TrimUI layout, sent to the PC as one Xbox pad),
 * the on-screen display is stream_osd.c, the files, battery and motor
 * stream_sys.c: the same modules as chiaki-stream.
 *
 * With self_test=1 in the session file it checks the handheld instead, no PC
 * needed (run_self_test: the screen, a decode benchmark of a clip built into
 * the binary, sound, Opus, moonlight-common-c, the controllers) and writes
 * the measurements to the status file.
 *
 * Exit codes: 0 ok / cancelled / host_ended, 2 usage, 3 invalid session,
 * 4 init, 5 SDL init, 6 decoder init, 8 connection start failed, 10 video
 * failure, 11 the connection ended with an error. A self-test exits 0, 6
 * (decoding failed, or no clip) or 10 (no video).
 *
 * Threads: the main thread owns SDL, drawing, input and the outcome; a connect
 * thread only runs LiStartConnection, which blocks through its stages (Start +
 * Select or a stop signal interrupts it); moonlight-common-c's own threads call
 * dr_submit (video decode), ar_decode (audio) and the connection callbacks,
 * which only hand atomics and one frame slot to the main thread.
 * LiStopConnection runs once, at the end, on the main thread.
 *
 * Signals: SIGINT, SIGTERM, SIGHUP and SIGQUIT end the stream normally (status
 * file included), SIGPIPE is ignored, and a crash first switches the rumble
 * motor off (stream_sys.c ss_motor_crash_guard). */
#include <SDL2/SDL.h>
#include <Limelight.h>
#include <opus_multistream.h>

#include <libavcodec/avcodec.h>
#include <libavutil/frame.h>
#include <libavutil/pixfmt.h>
#include <libswscale/swscale.h>

#include <ctype.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <math.h>
#include <pthread.h>
#include <signal.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <time.h>
#include <unistd.h>

#include "stream_input.h"
#include "stream_osd.h"
#include "stream_sys.h"

/* ---- the self-test clip ----
 * moonlight-selftest.h264 (README.md, "Self-test"), built into the binary:
 * CMake defines ML_SELFTEST_CLIP as its absolute path, quoted, only when the
 * file exists. The assembler copies it into .rodata between two symbols, and
 * ml_selftest_clip_size holds its length (one section, so the assembler
 * works it out). Without the define the self-test reports st_decode=0. */
#ifdef ML_SELFTEST_CLIP
__asm__(".pushsection .rodata.ml_selftest_clip,\"a\",%progbits\n"
        ".balign 16\n"
        ".global ml_selftest_clip_start\n"
        ".hidden ml_selftest_clip_start\n"
        ".type ml_selftest_clip_start, %object\n"
        "ml_selftest_clip_start:\n"
        ".incbin \"" ML_SELFTEST_CLIP "\"\n"
        ".global ml_selftest_clip_end\n"
        ".hidden ml_selftest_clip_end\n"
        "ml_selftest_clip_end:\n"
        ".size ml_selftest_clip_start, ml_selftest_clip_end - ml_selftest_clip_start\n"
        ".balign 4\n"
        ".global ml_selftest_clip_size\n"
        ".hidden ml_selftest_clip_size\n"
        ".type ml_selftest_clip_size, %object\n"
        "ml_selftest_clip_size:\n"
        ".4byte ml_selftest_clip_end - ml_selftest_clip_start\n"
        ".size ml_selftest_clip_size, 4\n"
        ".popsection\n");
extern const uint8_t ml_selftest_clip_start[];
extern const uint32_t ml_selftest_clip_size;
#endif

/* ---- moonlight-common-c names that changed over the years ----
 * Everything below is in current moonlight-common-c; the fallbacks only keep an
 * older or newer Limelight.h from breaking the build over a value that has not
 * changed on the wire. */
#ifndef VIDEO_FORMAT_MASK_H264
#define VIDEO_FORMAT_MASK_H264 0x000F
#endif
#ifndef VIDEO_FORMAT_MASK_H265
#define VIDEO_FORMAT_MASK_H265 0x0F00
#endif
#ifndef CAPABILITY_SLICES_PER_FRAME
#define CAPABILITY_SLICES_PER_FRAME(x) (((unsigned char)(x)) << 24)
#endif
#ifndef ML_ERROR_GRACEFUL_TERMINATION
#define ML_ERROR_GRACEFUL_TERMINATION 0
#endif
#ifndef ML_ERROR_NO_VIDEO_TRAFFIC
#define ML_ERROR_NO_VIDEO_TRAFFIC -100
#endif
#ifndef ML_ERROR_NO_VIDEO_FRAME
#define ML_ERROR_NO_VIDEO_FRAME -101
#endif
#ifndef ML_ERROR_UNEXPECTED_EARLY_TERMINATION
#define ML_ERROR_UNEXPECTED_EARLY_TERMINATION -102
#endif
#ifndef CONN_STATUS_POOR
#define CONN_STATUS_POOR 1
#endif
#ifndef MODIFIER_ALT
#define MODIFIER_ALT 0x04
#endif
#ifndef MODIFIER_META
#define MODIFIER_META 0x08
#endif

#define APP_NAME "JukaHub Moonlight"
#define SCREEN_WIDTH 1280
#define SCREEN_HEIGHT 720
#define MAX_PADS SI_MAX_PADS
/* JukaHub's own mapping for the firmware pad "TRIMUI Player1", added before any
 * pad is opened (the same string as chiaki-stream.c and player/main.go): a
 * firmware SDL without this GUID would open the built-in pad as a raw joystick. */
#define TRIMUI_PAD_MAPPING "030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,"
#define RUMBLE_RENEW_MS 1000u
#define PAD_ACTIVE_AXIS 8000
#define TOAST_SHORT_MS 1600u
#define TOAST_LONG_MS 5000u
#define HINT_MS 6000u
#define ERROR_CARD_MS 10000u
#define ERROR_CARD_GRACE_MS 400u
#define BATTERY_POLL_MS 30000u
#define STATS_MS 1000u
#define STATS_LOG_MS 5000u
#define POOR_TOAST_MS 15000u
/* A sound packet that would take the queue past this is dropped (ar_decode), so
 * a hiccup never leaves the picture and the sound apart for the rest of the
 * stream. */
#define AUDIO_QUEUE_MS 80u
/* Opus's longest frame, 120 ms at 48 kHz, per channel. */
#define PCM_MAX_SAMPLES 5760
/* This many decode errors in a row (5 s at 60 fps) end the stream. */
#define DECODE_FAIL_LIMIT 300
/* Mouse mode: pointer updates at most this often, and the stick's dead zone. */
#define MOUSE_TICK_MS 8u
#define MOUSE_DEADZONE 4000.0f
/* How long a stop at the connect stage may take before the helper gives up
 * waiting for the connect thread and leaves. */
#define INTERRUPT_WAIT_MS 15000u

/* Windows virtual-key codes, as Moonlight clients send them. */
#define ML_VK_TAB 0x09
#define ML_VK_RETURN 0x0D
#define ML_VK_ESCAPE 0x1B
#define ML_VK_LEFT 0x25
#define ML_VK_UP 0x26
#define ML_VK_RIGHT 0x27
#define ML_VK_DOWN 0x28
#define ML_VK_LWIN 0x5B
#define ML_VK_LMENU 0xA4

typedef enum {
    ML_CODEC_H264 = 0,
    ML_CODEC_H265,
    ML_CODEC_AUTO
} MlCodec;

/* The session file (README.md, "Moonlight helper"). */
typedef struct {
    char host[256];
    char app_version[64];
    char gfe_version[96];
    char rtsp_url[512];
    int codec_support;
    uint8_t ri_key[16];
    int32_t ri_key_id;
    unsigned int width;
    unsigned int height;
    unsigned int fps;
    unsigned int bitrate;
    MlCodec codec;
    bool remote;
    char name[96];      /* printable ASCII; "PC" when absent */
    char app_name[128]; /* printable ASCII; "" when absent */
    unsigned int volume;
    bool rumble;
    bool hints;
    bool dpad_stick;
    bool verbose;
    char motor_gpio[256];
    char motor_level[256];
    long motor_level_min;
    long motor_level_max;
    bool motor_range_set;
    char status_file[512];
    bool self_test; /* run_self_test instead of a stream */
} MlConfig;

typedef enum {
    ROW_RESUME = 0,
    ROW_MOUSE,
    ROW_STATS,
    ROW_RUMBLE,
    ROW_VOLUME,
    ROW_DPAD,
    ROW_SEND,
    ROW_CONTROLS,
    ROW_DISCONNECT,
    ROW_QUIT_APP,
    ROW_COUNT
} MenuRow;
_Static_assert(ROW_COUNT <= OSD_MENU_MAX_ROWS, "the stream menu has room for OSD_MENU_MAX_ROWS rows");

/* What the menu's "Send to the PC" row sends. */
static const char *const send_names[] = {"Alt+Tab", "Win key"};
#define SEND_COUNT ((int)(sizeof(send_names) / sizeof(send_names[0])))

/* One open pad, as in chiaki-stream.c (no motion: the PC gets an Xbox pad). */
typedef struct {
    bool used;
    SDL_JoystickID id;
    SDL_GameController *controller;
    SDL_Joystick *joystick; /* owned (closed here) only when controller is NULL */
    Uint8 hat;
    bool xpad_layout;
    bool builtin;
    uint32_t last_input;
    Uint16 rumble_low;
    Uint16 rumble_high;
    uint32_t rumble_at;
    char name[96];
} StreamPad;

/* Mouse mode: what the PC holds right now, so leaving the mode lets go of it. */
typedef struct {
    bool left;
    bool right;
    uint8_t arrows;          /* bit i: arrow key i held */
    uint32_t prev_buttons;   /* SI_OUT_ bits of the last update */
    float acc_x, acc_y, acc_scroll;
    uint32_t at;
    bool idle_sent;
} MlMouse;

typedef struct {
    const MlConfig *config;
    SDL_Window *window;
    SDL_Renderer *renderer;
    SDL_Texture *texture;
    SDL_Rect destination;
    bool destination_ready;
    StreamPad pads[MAX_PADS];
    struct SwsContext *sws;
    AVFrame *converted;
    AVFrame *render_frame;
    int video_w;
    int video_h;
    atomic_bool running;
    atomic_uint volume;

    /* moonlight-common-c */
    SERVER_INFORMATION server;
    STREAM_CONFIGURATION stream;
    CONNECTION_LISTENER_CALLBACKS listener;
    DECODER_RENDERER_CALLBACKS video_callbacks;
    AUDIO_RENDERER_CALLBACKS audio_callbacks;
    pthread_t start_thread;
    bool start_thread_live; /* created and not joined yet */
    bool start_abandoned;   /* a start that would not stop: its decoder and sound stay as they are */
    atomic_bool start_done;
    atomic_int start_rc;
    bool connection_live;   /* LiStartConnection returned 0; LiStopConnection still due */
    atomic_int stage;
    int shown_stage;
    atomic_bool stage_failed;
    atomic_int failed_stage;
    atomic_int failed_error;
    atomic_bool connected;
    atomic_bool terminated;
    atomic_int terminate_code;
    bool terminate_handled;
    atomic_int conn_status;
    atomic_bool conn_status_dirty;
    uint32_t poor_toast_at;
    atomic_int rumble_low;
    atomic_int rumble_high;
    atomic_bool rumble_dirty;
    uint32_t rumble_renewed_at;
    atomic_uint_fast64_t log_lines;

    /* video decoder (moonlight-common-c's decoder thread, after dr_setup) */
    AVCodecContext *codec;
    AVPacket *packet;
    AVFrame *decoded;
    uint8_t *bitstream;
    size_t bitstream_size;
    atomic_int video_format;
    atomic_bool decoder_failed;
    pthread_mutex_t frame_lock;
    AVFrame *slot;          /* the newest decoded frame, under frame_lock */
    bool slot_ready;
    atomic_uint_fast64_t decoded_frames;
    atomic_uint_fast64_t decode_us;
    atomic_uint_fast64_t dropped_frames;
    atomic_uint_fast64_t decode_errors;
    atomic_int errors_in_row;

    /* audio (moonlight-common-c's audio thread, after ar_init) */
    OpusMSDecoder *opus;
    int16_t *pcm;
    int pcm_capacity;       /* samples per channel the buffer holds */
    int samples_per_frame;
    int audio_channels;
    SDL_AudioDeviceID audio_device;
    Uint32 audio_queue_limit;
    atomic_uint_fast64_t audio_dropped; /* packets left out: the queue was at its bound */
    atomic_bool audio_failed;

    /* input */
    SiState input;
    SiOutput last_output;
    bool output_valid;
    bool modal;
    bool mouse_mode;
    MlMouse mouse;

    /* display */
    Osd osd;
    bool osd_ready;
    bool osd_dirty;
    uint32_t last_present;
    bool menu_open;
    bool menu_controls;
    int menu_cursor;
    int send_choice;
    uint32_t menu_second;
    char toast[160];
    OsdToastKind toast_kind;
    uint32_t toast_until;
    uint32_t hint_until;
    bool stats_visible;
    char stats_line[200];
    char status_line[120];
    uint32_t stats_at;
    uint32_t stats_log_at;
    uint64_t stats_rendered;
    uint64_t stats_decoded;
    uint64_t stats_decode_us;
    uint64_t stats_dropped;

    /* session facts */
    uint32_t start_ticks;
    uint32_t first_frame_at;
    uint64_t rendered_frames;
    bool rumble_enabled;
    bool quit_app;
    SsMotor motor;
    SsBattery battery;
    uint32_t battery_at;
    int battery_warned;

    /* the outcome, for the status file */
    int rc;
    const char *result;
    char message[400];
    char error_title[120];
    bool show_error;
    char fail_stage[64];
    int error_code;
    char port_flags[160];
} MlApp;

static MlApp g_app;
static MlApp *signal_app;
static volatile sig_atomic_t signalled;

__attribute__((format(printf, 3, 4)))
static void textf(char *out, size_t size, const char *format, ...)
{
    va_list args;
    va_start(args, format);
    vsnprintf(out, size, format, args);
    va_end(args);
}

static void stop_signal(int signal_number)
{
    (void)signal_number;
    signalled = 1;
    if(signal_app)
        atomic_store(&signal_app->running, false);
}

/* ---- the session file ---- */

static void trim(char *text)
{
    char *start = text;
    while(*start == ' ' || *start == '\t' || *start == '\r' || *start == '\n') start++;
    if(start != text) memmove(text, start, strlen(start) + 1);
    size_t length = strlen(text);
    while(length && (text[length - 1] == ' ' || text[length - 1] == '\t' ||
                     text[length - 1] == '\r' || text[length - 1] == '\n'))
        text[--length] = '\0';
}

static bool parse_uint(const char *text, unsigned long minimum, unsigned long maximum, unsigned int *value)
{
    char *end = NULL;
    errno = 0;
    if(!*text || *text == '-' || *text == '+') return false;
    unsigned long parsed = strtoul(text, &end, 10);
    if(errno || !end || *end || parsed < minimum || parsed > maximum) return false;
    *value = (unsigned int)parsed;
    return true;
}

static bool parse_long(const char *text, long minimum, long maximum, long *value)
{
    char *end = NULL;
    errno = 0;
    if(!*text) return false;
    long parsed = strtol(text, &end, 10);
    if(errno || !end || *end || parsed < minimum || parsed > maximum) return false;
    *value = parsed;
    return true;
}

static bool parse_bool(const char *text, bool *value)
{
    if(!strcmp(text, "1") || !strcasecmp(text, "true") || !strcasecmp(text, "on") || !strcasecmp(text, "yes")) {
        *value = true;
        return true;
    }
    if(!strcmp(text, "0") || !strcasecmp(text, "false") || !strcasecmp(text, "off") || !strcasecmp(text, "no")) {
        *value = false;
        return true;
    }
    return false;
}

static bool absolute_path(const char *value, size_t limit)
{
    return value[0] == '/' && strlen(value) < limit && !strstr(value, "/../");
}

static int hex_value(int c)
{
    if(c >= '0' && c <= '9') return c - '0';
    if(c >= 'a' && c <= 'f') return c - 'a' + 10;
    if(c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

/* Exactly 32 hex digits -> 16 bytes. */
static bool parse_key(const char *text, uint8_t out[16])
{
    if(strlen(text) != 32) return false;
    for(int i = 0; i < 16; i++) {
        int hi = hex_value((unsigned char)text[2 * i]);
        int lo = hex_value((unsigned char)text[2 * i + 1]);
        if(hi < 0 || lo < 0) {
            ss_wipe(out, 16);
            return false;
        }
        out[i] = (uint8_t)(hi << 4 | lo);
    }
    return true;
}

/* The whole file through read(): no stdio buffer is left holding the key.
 * -1 (errno set) when it cannot be opened, 0 for a file bigger than the
 * buffer, 1 when *length bytes were read. */
static int read_session(const char *path, char *buffer, size_t capacity, size_t *length)
{
    int descriptor = open(path, O_RDONLY | O_CLOEXEC);
    if(descriptor < 0) return -1;
    size_t total = 0;
    int result = 1;
    for(;;) {
        if(total == capacity) {
            char extra;
            ssize_t more = read(descriptor, &extra, 1);
            if(more > 0) result = 0;
            break;
        }
        ssize_t got = read(descriptor, buffer + total, capacity - total);
        if(got < 0 && errno == EINTR) continue;
        if(got <= 0) break;
        total += (size_t)got;
    }
    close(descriptor);
    *length = total;
    return result;
}

/* Reads and deletes the session file, with the rules of stream_sys.c's
 * ss_load_config: at most SS_SESSION_MAX bytes, a line longer than SS_LINE_MAX
 * skipped whole, the file deleted whatever it holds, the key wiped from every
 * buffer it passed through. */
static bool load_config(const char *path, MlConfig *config, char *error, size_t error_size)
{
    memset(config, 0, sizeof(*config));
    config->codec = ML_CODEC_H264;
    config->volume = 80;
    config->rumble = true;
    config->hints = true;
    snprintf(config->name, sizeof(config->name), "PC");

    static char buffer[SS_SESSION_MAX];
    size_t length = 0;
    int read_result = read_session(path, buffer, sizeof(buffer), &length);
    if(read_result < 0) {
        textf(error, error_size, "cannot open session file: %s", strerror(errno));
        return false;
    }
    unlink(path); /* the key never stays on disk, whatever the file held */
    if(read_result == 0) {
        ss_wipe(buffer, sizeof(buffer));
        textf(error, error_size, "session file larger than %d bytes", SS_SESSION_MAX);
        return false;
    }
    char line[SS_LINE_MAX + 1];
    char name[96] = {0};
    char app_name[128] = {0};
    bool have_key = false, have_key_id = false, have_width = false, have_height = false;
    bool have_fps = false, have_bitrate = false;
    size_t start = 0;
    while(start < length) {
        const char *newline = memchr(buffer + start, '\n', length - start);
        size_t end = newline ? (size_t)(newline - buffer) : length;
        size_t line_length = end - start;
        size_t next = newline ? end + 1 : length;
        if(line_length > SS_LINE_MAX) {
            start = next; /* too long for any key: skipped whole */
            continue;
        }
        memcpy(line, buffer + start, line_length);
        line[line_length] = '\0';
        start = next;
        char *separator = strchr(line, '=');
        if(!separator) continue;
        *separator = '\0';
        char *value = separator + 1;
        trim(line);
        trim(value);
        bool flag = false;
        long number = 0;
        if(!strcmp(line, "host")) {
            if(strlen(value) < sizeof(config->host)) snprintf(config->host, sizeof(config->host), "%s", value);
        } else if(!strcmp(line, "app_version")) {
            if(strlen(value) < sizeof(config->app_version))
                snprintf(config->app_version, sizeof(config->app_version), "%s", value);
        } else if(!strcmp(line, "gfe_version")) {
            if(strlen(value) < sizeof(config->gfe_version))
                snprintf(config->gfe_version, sizeof(config->gfe_version), "%s", value);
        } else if(!strcmp(line, "rtsp_url")) {
            if(strlen(value) < sizeof(config->rtsp_url)) snprintf(config->rtsp_url, sizeof(config->rtsp_url), "%s", value);
        } else if(!strcmp(line, "codec_support")) {
            if(parse_long(value, 0, INT_MAX, &number)) config->codec_support = (int)number;
        } else if(!strcmp(line, "ri_key")) {
            have_key = parse_key(value, config->ri_key);
        } else if(!strcmp(line, "ri_key_id")) {
            if(parse_long(value, INT32_MIN, INT32_MAX, &number)) {
                config->ri_key_id = (int32_t)number;
                have_key_id = true;
            }
        } else if(!strcmp(line, "width")) {
            have_width = parse_uint(value, 256, 7680, &config->width);
        } else if(!strcmp(line, "height")) {
            have_height = parse_uint(value, 144, 4320, &config->height);
        } else if(!strcmp(line, "fps")) {
            have_fps = parse_uint(value, 10, 240, &config->fps);
        } else if(!strcmp(line, "bitrate")) {
            have_bitrate = parse_uint(value, 500, 500000, &config->bitrate);
        } else if(!strcmp(line, "codec")) {
            if(!strcasecmp(value, "h264") || !strcasecmp(value, "avc")) config->codec = ML_CODEC_H264;
            else if(!strcasecmp(value, "h265") || !strcasecmp(value, "hevc")) config->codec = ML_CODEC_H265;
            else if(!strcasecmp(value, "auto")) config->codec = ML_CODEC_AUTO;
        } else if(!strcmp(line, "remote")) {
            if(parse_bool(value, &flag)) config->remote = flag;
        } else if(!strcmp(line, "name")) {
            snprintf(name, sizeof(name), "%s", value);
        } else if(!strcmp(line, "app_name")) {
            snprintf(app_name, sizeof(app_name), "%s", value);
        } else if(!strcmp(line, "volume")) {
            parse_uint(value, 0, 100, &config->volume);
        } else if(!strcmp(line, "rumble")) {
            if(parse_bool(value, &flag)) config->rumble = flag;
        } else if(!strcmp(line, "hints")) {
            if(parse_bool(value, &flag)) config->hints = flag;
        } else if(!strcmp(line, "dpad_stick")) {
            if(parse_bool(value, &flag)) config->dpad_stick = flag;
        } else if(!strcmp(line, "verbose")) {
            if(parse_bool(value, &flag)) config->verbose = flag;
        } else if(!strcmp(line, "motor_gpio")) {
            if(absolute_path(value, sizeof(config->motor_gpio)))
                snprintf(config->motor_gpio, sizeof(config->motor_gpio), "%s", value);
        } else if(!strcmp(line, "motor_level")) {
            if(absolute_path(value, sizeof(config->motor_level)))
                snprintf(config->motor_level, sizeof(config->motor_level), "%s", value);
        } else if(!strcmp(line, "motor_level_min")) {
            if(parse_long(value, 0, 100000000L, &config->motor_level_min)) config->motor_range_set = true;
        } else if(!strcmp(line, "motor_level_max")) {
            if(parse_long(value, 1, 100000000L, &config->motor_level_max)) config->motor_range_set = true;
        } else if(!strcmp(line, "self_test")) {
            if(parse_bool(value, &flag)) config->self_test = flag;
        } else if(!strcmp(line, "status_file")) {
            if(absolute_path(value, sizeof(config->status_file)))
                snprintf(config->status_file, sizeof(config->status_file), "%s", value);
        }
    }
    ss_wipe(line, sizeof(line));
    ss_wipe(buffer, sizeof(buffer));

    if(name[0]) {
        char ascii[96];
        ss_ascii_name(name, ascii, sizeof(ascii));
        if(ascii[0]) snprintf(config->name, sizeof(config->name), "%s", ascii);
    }
    if(app_name[0]) ss_ascii_name(app_name, config->app_name, sizeof(config->app_name));
    if(config->motor_range_set && config->motor_level_max <= config->motor_level_min)
        config->motor_range_set = false;
    /* A self-test needs no PC, so none of the stream's keys (README.md,
     * "Self-test"); volume, hints, verbose, motor_* and status_file apply. */
    if(config->self_test) {
        ss_wipe(config->ri_key, sizeof(config->ri_key));
        return true;
    }

    bool valid = config->host[0] && config->app_version[0] && have_key && have_key_id && have_width &&
                 have_height && have_fps && have_bitrate;
    if(!valid) {
        ss_wipe(config->ri_key, sizeof(config->ri_key));
        textf(error, error_size, "invalid or incomplete session file (host=%d app_version=%d ri_key=%d ri_key_id=%d mode=%d%d%d bitrate=%d)",
              config->host[0] != 0, config->app_version[0] != 0, have_key, have_key_id, have_width, have_height,
              have_fps, have_bitrate);
        return false;
    }
    return true;
}

/* ---- the status file ---- */

typedef struct {
    int rc;
    const char *result;
    const char *message;
    bool connected;
    unsigned long seconds;
    unsigned long long frames;
    const char *codec;
    bool quit_app;
    const char *stage;
    int error_code;
    const char *port_flags;
    double decode_ms;
    double fps_shown;
} MlStatus;

/* The contract's keys, in its order (version, rc, result, message, connected,
 * seconds, frames, host, codec, quit_app, stage, error_code, port_flags,
 * decode_ms, fps_shown), written atomically with mode 0600. */
static bool write_status(const char *path, const char *host, const MlStatus *status)
{
    char rc[16], seconds[32], frames[32], error_code[16], decode_ms[32], fps_shown[32];
    snprintf(rc, sizeof(rc), "%d", status->rc);
    snprintf(seconds, sizeof(seconds), "%lu", status->seconds);
    snprintf(frames, sizeof(frames), "%llu", status->frames);
    snprintf(error_code, sizeof(error_code), "%d", status->error_code);
    snprintf(decode_ms, sizeof(decode_ms), "%.1f", status->decode_ms);
    snprintf(fps_shown, sizeof(fps_shown), "%.1f", status->fps_shown);
    const SsPair pairs[] = {
        {"version", "1"},
        {"rc", rc},
        {"result", status->result ? status->result : "init_failed"},
        {"message", status->message},
        {"connected", status->connected ? "1" : "0"},
        {"seconds", seconds},
        {"frames", frames},
        {"host", host},
        {"codec", status->codec},
        {"quit_app", status->quit_app ? "1" : "0"},
        {"stage", status->stage},
        {"error_code", error_code},
        {"port_flags", status->port_flags},
        {"decode_ms", decode_ms},
        {"fps_shown", fps_shown},
    };
    return ss_write_pairs(path, pairs, sizeof(pairs) / sizeof(pairs[0]));
}

static const char *requested_codec_name(const MlConfig *config)
{
    switch(config->codec) {
        case ML_CODEC_H265: return "h265";
        case ML_CODEC_AUTO: return "auto";
        default: return "h264";
    }
}

/* The codec the PC chose, once the video stream is set up. */
static const char *codec_name(MlApp *app)
{
    int format = atomic_load(&app->video_format);
    if(format & VIDEO_FORMAT_MASK_H265) return "h265";
    if(format & VIDEO_FORMAT_MASK_H264) return "h264";
    return requested_codec_name(app->config);
}

static const char *codec_label(MlApp *app)
{
    const char *name = codec_name(app);
    if(!strcmp(name, "h265")) return "H.265";
    if(!strcmp(name, "auto")) return "H.264/H.265";
    return "H.264";
}

/* ---- the outcome ---- */

static void toast(MlApp *app, const char *text, OsdToastKind kind, uint32_t duration)
{
    textf(app->toast, sizeof(app->toast), "%s", text);
    app->toast_kind = kind;
    app->toast_until = SDL_GetTicks() + duration;
    app->osd_dirty = true;
}

static void set_status(MlApp *app, const char *text)
{
    textf(app->status_line, sizeof(app->status_line), "%s", text);
    app->osd_dirty = true;
}

/* The one place the outcome is decided; the first decision wins. */
static void set_outcome(MlApp *app, int rc, const char *result, const char *message)
{
    if(app->result) return;
    app->rc = rc;
    app->result = result;
    textf(app->message, sizeof(app->message), "%s", message ? message : "");
}

static void fail_with_card(MlApp *app, int rc, const char *result, const char *title, const char *message)
{
    if(app->result) return;
    set_outcome(app, rc, result, message);
    textf(app->error_title, sizeof(app->error_title), "%s", title);
    app->show_error = true;
}

static void request_quit(MlApp *app)
{
    set_outcome(app, 0, app->first_frame_at ? "ok" : "cancelled",
                app->first_frame_at ? "The stream ended." : "The stream was cancelled before the video started.");
    atomic_store(&app->running, false);
}

/* ---- moonlight-common-c callbacks (its threads) ---- */

static void cl_stage_starting(int stage)
{
    atomic_store(&g_app.stage, stage);
    fprintf(stdout, "[native] stage: %s\n", LiGetStageName(stage));
    fflush(stdout);
}

static void cl_stage_complete(int stage)
{
    (void)stage;
}

static void cl_stage_failed(int stage, int error_code)
{
    MlApp *app = &g_app;
    if(!atomic_load(&app->stage_failed)) {
        atomic_store(&app->failed_stage, stage);
        atomic_store(&app->failed_error, error_code);
        atomic_store(&app->stage_failed, true);
    }
    fprintf(stderr, "[native] stage failed: %s (error %d)\n", LiGetStageName(stage), error_code);
}

static void cl_connection_started(void)
{
    atomic_store(&g_app.connected, true);
    fprintf(stdout, "[native] Moonlight connected\n");
    fflush(stdout);
}

static void cl_connection_terminated(int error_code)
{
    MlApp *app = &g_app;
    if(atomic_load(&app->terminated)) return;
    atomic_store(&app->terminate_code, error_code);
    atomic_store(&app->terminated, true);
    fprintf(stderr, "[native] connection terminated: error %d\n", error_code);
}

/* moonlight-common-c's log, prefixed. Without verbose=1 a long stream's
 * repeated lines are thinned out after the first few hundred. */
__attribute__((format(printf, 1, 2)))
static void cl_log(const char *format, ...)
{
    MlApp *app = &g_app;
    uint64_t count = atomic_fetch_add(&app->log_lines, 1) + 1;
    if(app->config && !app->config->verbose && count > 400 && count % 100 != 0) return;
    va_list args;
    flockfile(stdout);
    fputs("[moonlight] ", stdout);
    va_start(args, format);
    vfprintf(stdout, format, args);
    va_end(args);
    size_t length = strlen(format);
    if(!length || format[length - 1] != '\n') fputc('\n', stdout);
    funlockfile(stdout);
}

static void cl_rumble(unsigned short controller, unsigned short low, unsigned short high)
{
    if(controller != 0) return; /* every pad is merged into controller 0 */
    atomic_store(&g_app.rumble_low, (int)low);
    atomic_store(&g_app.rumble_high, (int)high);
    atomic_store(&g_app.rumble_dirty, true);
}

static void cl_status_update(int status)
{
    atomic_store(&g_app.conn_status, status);
    atomic_store(&g_app.conn_status_dirty, true);
}

static uint64_t mono_us(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000u + (uint64_t)ts.tv_nsec / 1000u;
}

static void dr_cleanup(void)
{
    MlApp *app = &g_app;
    if(app->codec) avcodec_free_context(&app->codec);
    if(app->packet) av_packet_free(&app->packet);
    if(app->decoded) av_frame_free(&app->decoded);
    free(app->bitstream);
    app->bitstream = NULL;
    app->bitstream_size = 0;
}

/* The stream's decoder settings, for dr_setup and the self-test alike: low
 * delay, the fast flags, slice threads only. H.264: 4 threads, the PC was
 * asked for 4 slices a frame (CAPABILITY_SLICES_PER_FRAME), so FFmpeg decodes
 * them on the A133's four cores at once. HEVC: FFmpeg's decoder runs only WPP
 * rows in parallel and decodes ordinary slices one after another, so it gets
 * one thread. Never frame threads: each one is a frame of delay. NULL when it
 * cannot be opened. */
static AVCodecContext *open_video_decoder(const AVCodec *decoder, int width, int height)
{
    AVCodecContext *decoder_context = avcodec_alloc_context3(decoder);
    if(!decoder_context) return NULL;
    decoder_context->flags |= AV_CODEC_FLAG_LOW_DELAY;
    decoder_context->flags2 |= AV_CODEC_FLAG2_FAST;
    decoder_context->thread_type = FF_THREAD_SLICE;
    decoder_context->thread_count = decoder->id == AV_CODEC_ID_HEVC ? 1 : 4;
    decoder_context->width = width;
    decoder_context->height = height;
    if(avcodec_open2(decoder_context, decoder, NULL) < 0) {
        fprintf(stderr, "[native] cannot open the %s decoder\n", decoder->name);
        avcodec_free_context(&decoder_context);
        return NULL;
    }
    return decoder_context;
}

/* The stream's codec and size, from the connect thread. */
static int dr_setup(int video_format, int width, int height, int redraw_rate, void *context, int dr_flags)
{
    (void)context;
    (void)dr_flags;
    MlApp *app = &g_app;
    atomic_store(&app->video_format, video_format); /* what the PC chose, for the card and the status file */
    enum AVCodecID id = AV_CODEC_ID_NONE;
    if(video_format & VIDEO_FORMAT_MASK_H265) id = AV_CODEC_ID_HEVC;
    else if(video_format & VIDEO_FORMAT_MASK_H264) id = AV_CODEC_ID_H264;
    const AVCodec *decoder = id != AV_CODEC_ID_NONE ? avcodec_find_decoder(id) : NULL;
    if(!decoder) {
        fprintf(stderr, "[native] no decoder for video format 0x%x\n", (unsigned)video_format);
        atomic_store(&app->decoder_failed, true);
        return -1;
    }
    bool hevc = id == AV_CODEC_ID_HEVC;
    AVCodecContext *decoder_context = open_video_decoder(decoder, width, height);
    if(!decoder_context) {
        atomic_store(&app->decoder_failed, true);
        return -1;
    }
    app->codec = decoder_context;
    app->packet = av_packet_alloc();
    app->decoded = av_frame_alloc();
    if(!app->packet || !app->decoded) {
        dr_cleanup();
        atomic_store(&app->decoder_failed, true);
        return -1;
    }
    atomic_store(&app->video_format, video_format);
    fprintf(stdout, "[native] video: %s %dx%d, %d fps, %s\n", hevc ? "HEVC" : "H.264", width, height, redraw_rate,
            hevc ? "1 decode thread (FFmpeg decodes HEVC slices one after another)" : "4 slice threads");
    fflush(stdout);
    return 0;
}

/* The newest frame goes to the main thread's slot; one it has not drawn yet is
 * dropped (late is worse than missing for a game stream). */
static void publish_frame(MlApp *app)
{
    pthread_mutex_lock(&app->frame_lock);
    if(app->slot_ready) {
        av_frame_unref(app->slot);
        atomic_fetch_add(&app->dropped_frames, 1);
    }
    av_frame_move_ref(app->slot, app->decoded);
    app->slot_ready = true;
    pthread_mutex_unlock(&app->frame_lock);
}

static int decode_failed(MlApp *app, int error, const char *what)
{
    uint64_t errors = atomic_fetch_add(&app->decode_errors, 1) + 1;
    atomic_fetch_add(&app->errors_in_row, 1);
    if(errors <= 5 || errors % 100 == 0) {
        char text[AV_ERROR_MAX_STRING_SIZE] = {0};
        av_strerror(error, text, sizeof(text));
        fprintf(stderr, "[native] %s failed (%s), asking for a key frame; errors so far %llu\n", what, text,
                (unsigned long long)errors);
    }
    return DR_NEED_IDR;
}

/* moonlight-common-c's decoder thread: one frame's NAL units, as a list of
 * buffers. They are joined into one padded buffer (FFmpeg's parsers read past
 * the end) and decoded at once. */
static int dr_submit(PDECODE_UNIT unit)
{
    MlApp *app = &g_app;
    if(!app->codec || !unit || unit->fullLength <= 0) return DR_OK;
    size_t need = (size_t)unit->fullLength + AV_INPUT_BUFFER_PADDING_SIZE;
    if(need > app->bitstream_size) {
        size_t size = need + need / 2;
        uint8_t *grown = realloc(app->bitstream, size);
        if(!grown) return decode_failed(app, AVERROR(ENOMEM), "buffer allocation");
        app->bitstream = grown;
        app->bitstream_size = size;
    }
    size_t offset = 0;
    for(PLENTRY entry = unit->bufferList; entry; entry = entry->next) {
        if(entry->length <= 0) continue;
        if(offset + (size_t)entry->length > (size_t)unit->fullLength) break; /* never past the buffer */
        memcpy(app->bitstream + offset, entry->data, (size_t)entry->length);
        offset += (size_t)entry->length;
    }
    memset(app->bitstream + offset, 0, AV_INPUT_BUFFER_PADDING_SIZE);
    app->packet->data = app->bitstream;
    app->packet->size = (int)offset;
    app->packet->flags = unit->frameType == FRAME_TYPE_IDR ? AV_PKT_FLAG_KEY : 0;
    uint64_t started = mono_us();
    int error = avcodec_send_packet(app->codec, app->packet);
    bool got = false;
    if(error == AVERROR(EAGAIN)) {
        /* The decoder still holds a frame: take it, then send again. */
        while(avcodec_receive_frame(app->codec, app->decoded) == 0) {
            publish_frame(app);
            got = true;
        }
        error = avcodec_send_packet(app->codec, app->packet);
    }
    if(error < 0) return decode_failed(app, error, "decode");
    for(;;) {
        error = avcodec_receive_frame(app->codec, app->decoded);
        if(error == AVERROR(EAGAIN) || error == AVERROR_EOF) break;
        if(error < 0) return decode_failed(app, error, "decode");
        publish_frame(app);
        got = true;
    }
    if(got) {
        atomic_fetch_add(&app->decode_us, mono_us() - started);
        atomic_fetch_add(&app->decoded_frames, 1);
        atomic_store(&app->errors_in_row, 0);
    }
    return DR_OK;
}

static void ar_cleanup(void)
{
    MlApp *app = &g_app;
    if(app->audio_device) SDL_CloseAudioDevice(app->audio_device);
    app->audio_device = 0;
    if(app->opus) opus_multistream_decoder_destroy(app->opus);
    app->opus = NULL;
    free(app->pcm);
    app->pcm = NULL;
}

/* The Opus layout the PC sends, from the connect thread. No sound device is
 * not fatal: the stream plays silent rather than not at all. */
static int ar_init(int audio_configuration, const POPUS_MULTISTREAM_CONFIGURATION opus_config, void *context, int ar_flags)
{
    (void)audio_configuration;
    (void)context;
    (void)ar_flags;
    MlApp *app = &g_app;
    int error = 0;
    if(!opus_config || opus_config->channelCount < 1 || opus_config->channelCount > 8) {
        atomic_store(&app->audio_failed, true);
        return -1;
    }
    app->opus = opus_multistream_decoder_create(opus_config->sampleRate, opus_config->channelCount,
                                                opus_config->streams, opus_config->coupledStreams,
                                                opus_config->mapping, &error);
    if(!app->opus) {
        fprintf(stderr, "[native] Opus decoder failed: %s\n", opus_strerror(error));
        atomic_store(&app->audio_failed, true);
        return -1;
    }
    app->audio_channels = opus_config->channelCount;
    app->samples_per_frame = opus_config->samplesPerFrame > 0 ? opus_config->samplesPerFrame : 240;
    app->pcm_capacity = PCM_MAX_SAMPLES;
    app->pcm = calloc((size_t)app->pcm_capacity * (size_t)app->audio_channels, sizeof(int16_t));
    if(!app->pcm) {
        ar_cleanup();
        atomic_store(&app->audio_failed, true);
        return -1;
    }
    SDL_AudioSpec wanted;
    SDL_zero(wanted);
    wanted.freq = opus_config->sampleRate;
    wanted.format = AUDIO_S16SYS;
    wanted.channels = (Uint8)opus_config->channelCount;
    wanted.samples = 1024;
    app->audio_device = SDL_OpenAudioDevice(NULL, 0, &wanted, NULL, 0);
    if(!app->audio_device) {
        fprintf(stderr, "[native] SDL audio open failed, streaming without sound: %s\n", SDL_GetError());
        return 0;
    }
    app->audio_queue_limit = (Uint32)((uint64_t)opus_config->sampleRate * (uint64_t)app->audio_channels *
                                      sizeof(int16_t) * AUDIO_QUEUE_MS / 1000u);
    SDL_PauseAudioDevice(app->audio_device, 0);
    fprintf(stdout, "[native] audio ready: %d channels %d Hz, %d samples a frame\n", app->audio_channels,
            opus_config->sampleRate, app->samples_per_frame);
    fflush(stdout);
    return 0;
}

/* moonlight-common-c's audio thread. NULL data: a lost packet, which Opus
 * covers with its loss concealment. */
static void ar_decode(char *data, int length)
{
    MlApp *app = &g_app;
    if(!app->opus || !app->pcm) return;
    int samples = opus_multistream_decode(app->opus, (const unsigned char *)data, data ? length : 0, app->pcm,
                                          data ? app->pcm_capacity : app->samples_per_frame, 0);
    if(samples <= 0 || !app->audio_device) return;
    size_t count = (size_t)samples * (size_t)app->audio_channels;
    Uint32 bytes = (Uint32)(count * sizeof(int16_t));
    /* A packet that would take the queue past the bound is dropped, never the
     * queue (as Moonlight PC does): a burst after a Wi-Fi hiccup costs only the
     * excess, a few ms at a time, not an 80 ms hole and a click. */
    if((uint64_t)SDL_GetQueuedAudioSize(app->audio_device) + bytes > app->audio_queue_limit) {
        uint64_t dropped = atomic_fetch_add(&app->audio_dropped, 1) + 1;
        if(app->config && app->config->verbose && (dropped <= 5 || dropped % 100 == 0)) {
            fprintf(stderr, "[native] sound queue full (%u ms): packet dropped; dropped so far %llu\n", AUDIO_QUEUE_MS,
                    (unsigned long long)dropped);
        }
        return;
    }
    unsigned int volume = atomic_load(&app->volume);
    if(volume < 100) {
        for(size_t i = 0; i < count; i++) app->pcm[i] = (int16_t)(((int32_t)app->pcm[i] * (int32_t)volume) / 100);
    }
    SDL_QueueAudio(app->audio_device, app->pcm, bytes);
}

/* ---- the controller the PC sees ---- */

static int to_ml_buttons(uint32_t buttons)
{
    int out = 0;
    if(buttons & SI_OUT_CROSS) out |= A_FLAG; /* bottom, by position */
    if(buttons & SI_OUT_MOON) out |= B_FLAG;
    if(buttons & SI_OUT_BOX) out |= X_FLAG;
    if(buttons & SI_OUT_PYRAMID) out |= Y_FLAG;
    if(buttons & SI_OUT_DPAD_UP) out |= UP_FLAG;
    if(buttons & SI_OUT_DPAD_DOWN) out |= DOWN_FLAG;
    if(buttons & SI_OUT_DPAD_LEFT) out |= LEFT_FLAG;
    if(buttons & SI_OUT_DPAD_RIGHT) out |= RIGHT_FLAG;
    if(buttons & SI_OUT_L1) out |= LB_FLAG;
    if(buttons & SI_OUT_R1) out |= RB_FLAG;
    if(buttons & SI_OUT_L3) out |= LS_CLK_FLAG;
    if(buttons & SI_OUT_R3) out |= RS_CLK_FLAG;
    if(buttons & SI_OUT_OPTIONS) out |= PLAY_FLAG;
    if(buttons & SI_OUT_SHARE) out |= BACK_FLAG;
    if(buttons & SI_OUT_PS) out |= SPECIAL_FLAG; /* a Menu tap: the Xbox Guide button */
    /* SI_OUT_TOUCHPAD (Menu + Select, Menu + R3, a PS pad's touchpad click) goes
     * nowhere: an Xbox pad has no touchpad, and send_arrival does not list one. */
    return out;
}

/* SDL's stick Y grows downwards, XInput's (and so the PC's) upwards. Negated
 * with a clamp: -(-32768) does not fit a short. */
static int16_t flip_axis(int16_t value)
{
    return value == INT16_MIN ? INT16_MAX : (int16_t)-value;
}

/* Touches are left out: an Xbox pad has no touchpad, so si's Menu finger and
 * swipes never reach the PC. */
static bool outputs_equal(const SiOutput *a, const SiOutput *b)
{
    return a->buttons == b->buttons && a->l2 == b->l2 && a->r2 == b->r2 && a->lx == b->lx && a->ly == b->ly &&
           a->rx == b->rx && a->ry == b->ry;
}

/* Tells a Sunshine-family host what kind of pad is coming, before its first
 * state: an Xbox pad with analog triggers and rumble. Older hosts ignore it. */
static void send_arrival(MlApp *app)
{
#ifdef LI_CTYPE_XBOX
    uint32_t supported = (uint32_t)(A_FLAG | B_FLAG | X_FLAG | Y_FLAG | UP_FLAG | DOWN_FLAG | LEFT_FLAG | RIGHT_FLAG |
                                    LB_FLAG | RB_FLAG | PLAY_FLAG | BACK_FLAG | LS_CLK_FLAG | RS_CLK_FLAG | SPECIAL_FLAG);
    int error = LiSendControllerArrivalEvent(0, 0x1, LI_CTYPE_XBOX, supported, LI_CCAP_ANALOG_TRIGGERS | LI_CCAP_RUMBLE);
    if(error != 0) fprintf(stderr, "[native] controller arrival not sent (%d)\n", error);
#endif
    app->output_valid = false; /* the first state goes out on the next loop */
}

static void send_key(uint8_t vk, char action, char modifiers)
{
    /* The high byte marks a virtual-key code, as Moonlight PC sends it. */
    short code = (short)(uint16_t)(0x8000u | vk);
    LiSendKeyboardEvent(code, action, modifiers);
}

static void key_tap(uint8_t vk)
{
    send_key(vk, KEY_ACTION_DOWN, 0);
    send_key(vk, KEY_ACTION_UP, 0);
}

static const struct {
    uint32_t bit;
    uint8_t vk;
} arrow_keys[4] = {
    {SI_OUT_DPAD_UP, ML_VK_UP},
    {SI_OUT_DPAD_DOWN, ML_VK_DOWN},
    {SI_OUT_DPAD_LEFT, ML_VK_LEFT},
    {SI_OUT_DPAD_RIGHT, ML_VK_RIGHT},
};

/* Lets go of every mouse button and key mouse mode holds on the PC. */
static void mouse_release_all(MlApp *app)
{
    MlMouse *mouse = &app->mouse;
    if(app->connection_live) {
        if(mouse->left) LiSendMouseButtonEvent(BUTTON_ACTION_RELEASE, BUTTON_LEFT);
        if(mouse->right) LiSendMouseButtonEvent(BUTTON_ACTION_RELEASE, BUTTON_RIGHT);
        for(int i = 0; i < 4; i++)
            if(mouse->arrows & (1u << i)) send_key(arrow_keys[i].vk, KEY_ACTION_UP, 0);
    }
    memset(mouse, 0, sizeof(*mouse));
}

static void mouse_button(bool down, bool *held, int button)
{
    if(down == *held) return;
    *held = down;
    LiSendMouseButtonEvent(down ? BUTTON_ACTION_PRESS : BUTTON_ACTION_RELEASE, button);
}

/* 0 inside the dead zone, else 0..1 of the way from it to full tilt. */
static float stick_amount(float magnitude)
{
    if(magnitude <= MOUSE_DEADZONE) return 0.0f;
    float amount = (magnitude - MOUSE_DEADZONE) / (32767.0f - MOUSE_DEADZONE);
    return amount > 1.0f ? 1.0f : amount;
}

/* Mouse mode, for the PC's desktop: left stick moves the pointer (slow near
 * the centre for aiming, fast at full tilt), right stick scrolls, bottom =
 * left click, right = right click, D-pad = arrow keys, Start = Enter and
 * Select = Esc (on release, so the Start + Select quit sends neither). The PC's
 * pad rests meanwhile. */
static void push_mouse(MlApp *app, const SiOutput *out, uint32_t now)
{
    MlMouse *mouse = &app->mouse;
    if(!mouse->idle_sent) {
        LiSendMultiControllerEvent(0, 0x1, 0, 0, 0, 0, 0, 0, 0);
        mouse->idle_sent = true;
        mouse->at = now;
    }
    mouse_button((out->buttons & SI_OUT_CROSS) != 0, &mouse->left, BUTTON_LEFT);
    mouse_button((out->buttons & SI_OUT_MOON) != 0, &mouse->right, BUTTON_RIGHT);
    for(int i = 0; i < 4; i++) {
        bool down = (out->buttons & arrow_keys[i].bit) != 0;
        bool was = (mouse->arrows & (1u << i)) != 0;
        if(down == was) continue;
        send_key(arrow_keys[i].vk, down ? KEY_ACTION_DOWN : KEY_ACTION_UP, 0);
        if(down) mouse->arrows = (uint8_t)(mouse->arrows | (1u << i));
        else mouse->arrows = (uint8_t)(mouse->arrows & ~(1u << i));
    }
    /* A bit that went away while its button is still held was latched by the
     * Start + Select quit (or a menu opened): not a tap. */
    uint32_t released = mouse->prev_buttons & ~out->buttons;
    if((released & SI_OUT_OPTIONS) && !si_held(&app->input, SI_IN_START)) key_tap(ML_VK_RETURN);
    if((released & SI_OUT_SHARE) && !si_held(&app->input, SI_IN_BACK) && !si_held(&app->input, SI_IN_CREATE))
        key_tap(ML_VK_ESCAPE);
    mouse->prev_buttons = out->buttons;

    uint32_t elapsed = now - mouse->at;
    if(elapsed < MOUSE_TICK_MS) return;
    if(elapsed > 100u) elapsed = 100u;
    mouse->at = now;
    float dt = (float)elapsed / 1000.0f;
    float lx = out->lx, ly = out->ly;
    float magnitude = sqrtf(lx * lx + ly * ly);
    float amount = stick_amount(magnitude);
    if(amount > 0.0f) {
        float speed = 60.0f + 1500.0f * amount * amount; /* pixels a second */
        mouse->acc_x += lx / magnitude * speed * dt;
        mouse->acc_y += ly / magnitude * speed * dt;
        int dx = (int)mouse->acc_x, dy = (int)mouse->acc_y;
        if(dx || dy) {
            mouse->acc_x -= (float)dx;
            mouse->acc_y -= (float)dy;
            LiSendMouseMoveEvent((short)dx, (short)dy);
        }
    } else {
        mouse->acc_x = mouse->acc_y = 0.0f;
    }
    float ry = out->ry;
    float scroll = stick_amount(fabsf(ry));
    if(scroll > 0.0f) {
        float clicks_per_second = 2.0f + 18.0f * scroll * scroll;
        mouse->acc_scroll += (ry > 0.0f ? -1.0f : 1.0f) * clicks_per_second * dt; /* stick up = scroll up */
        int clicks = (int)mouse->acc_scroll;
        if(clicks) {
            mouse->acc_scroll -= (float)clicks;
            LiSendScrollEvent((signed char)clicks);
        }
    } else {
        mouse->acc_scroll = 0.0f;
    }
}

static void push_input(MlApp *app, uint32_t now)
{
    if(!app->connection_live) return;
    SiOutput out;
    si_output(&app->input, now, &out);
    if(app->mouse_mode) {
        push_mouse(app, &out, now);
        return;
    }
    if(app->output_valid && outputs_equal(&out, &app->last_output)) return;
    int error = LiSendMultiControllerEvent(0, 0x1, to_ml_buttons(out.buttons), out.l2, out.r2, out.lx,
                                           flip_axis(out.ly), out.rx, flip_axis(out.ry));
    app->last_output = out;
    app->output_valid = error == 0; /* a state that did not go out is sent again */
}

static void set_mouse_mode(MlApp *app, bool on, uint32_t now)
{
    if(on == app->mouse_mode) return;
    mouse_release_all(app);
    app->mouse_mode = on;
    app->mouse.at = now;
    app->output_valid = false;
    fprintf(stdout, "[native] mouse mode %s\n", on ? "on" : "off");
    fflush(stdout);
}

static void send_combo(MlApp *app)
{
    if(!app->connection_live) {
        toast(app, "Not connected yet", OSD_TOAST_WARN, TOAST_SHORT_MS);
        return;
    }
    char text[64];
    if(app->send_choice == 0) {
        send_key(ML_VK_LMENU, KEY_ACTION_DOWN, MODIFIER_ALT);
        send_key(ML_VK_TAB, KEY_ACTION_DOWN, MODIFIER_ALT);
        send_key(ML_VK_TAB, KEY_ACTION_UP, MODIFIER_ALT);
        send_key(ML_VK_LMENU, KEY_ACTION_UP, 0);
    } else {
        send_key(ML_VK_LWIN, KEY_ACTION_DOWN, MODIFIER_META);
        send_key(ML_VK_LWIN, KEY_ACTION_UP, 0);
    }
    textf(text, sizeof(text), "Sent %s", send_names[app->send_choice]);
    toast(app, text, OSD_TOAST_INFO, TOAST_SHORT_MS);
}

static void update_modal(MlApp *app, uint32_t now)
{
    bool modal = app->menu_open;
    if(modal != app->modal) {
        app->modal = modal;
        si_set_modal(&app->input, modal, now);
    }
    app->osd_dirty = true;
}

/* ---- rumble (as chiaki-stream.c: the pad in use, the motor for the handheld) ---- */

static StreamPad *rumble_target(MlApp *app)
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
    else if(pad->joystick) SDL_JoystickRumble(pad->joystick, low, high, duration);
}

static void pad_rumble(StreamPad *pad, Uint16 low, Uint16 high, uint32_t now)
{
    bool same = pad->rumble_low == low && pad->rumble_high == high;
    if(same && (!(low || high) || now - pad->rumble_at < SS_MOTOR_SAFETY_MS / 2)) return;
    pad_rumble_send(pad, low, high, now);
}

static void renew_rumble(MlApp *app, uint32_t now)
{
    if(now - app->rumble_renewed_at < RUMBLE_RENEW_MS) return;
    app->rumble_renewed_at = now;
    for(int i = 0; i < MAX_PADS; i++) {
        StreamPad *pad = &app->pads[i];
        if(pad->used && (pad->rumble_low || pad->rumble_high))
            pad_rumble_send(pad, pad->rumble_low, pad->rumble_high, now);
    }
}

static void apply_rumble(MlApp *app, uint32_t now)
{
    int low = atomic_load(&app->rumble_low);
    int high = atomic_load(&app->rumble_high);
    if(!app->rumble_enabled) low = high = 0;
    StreamPad *target = rumble_target(app);
    bool handheld = !target || target->builtin || !target->last_input;
    int strongest = low > high ? low : high;
    ss_motor_set(&app->motor, handheld ? (float)strongest / 65535.0f : 0.0f, now);
    for(int i = 0; i < MAX_PADS; i++) {
        StreamPad *pad = &app->pads[i];
        if(!pad->used) continue;
        if(pad == target) pad_rumble(pad, (Uint16)low, (Uint16)high, now);
        else pad_rumble(pad, 0, 0, now);
    }
}

static void stop_rumble(MlApp *app)
{
    app->rumble_enabled = false;
    apply_rumble(app, SDL_GetTicks());
    ss_motor_off(&app->motor);
}

/* ---- pads (as chiaki-stream.c, without motion and touchpad surfaces) ---- */

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

static StreamPad *pad_find(MlApp *app, SDL_JoystickID id)
{
    for(int i = 0; i < MAX_PADS; i++)
        if(app->pads[i].used && app->pads[i].id == id) return &app->pads[i];
    return NULL;
}

typedef const char *(*JoystickPathFn)(SDL_Joystick *joystick);
_Static_assert(sizeof(JoystickPathFn) == sizeof(void *), "dlsym result fits a function pointer");

/* SDL_JoystickPath is SDL 2.24's: looked up at run time, so an older firmware
 * SDL still loads the helper. */
static const char *joystick_path(SDL_Joystick *joystick)
{
    static bool looked_up;
    static JoystickPathFn path_fn;
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

static void pad_note_input(StreamPad *pad, uint32_t now)
{
    pad->last_input = now ? now : 1;
}

static bool sdl_has_2_0_14(void)
{
#if SDL_VERSION_ATLEAST(2, 0, 14)
    SDL_version linked;
    SDL_GetVersion(&linked);
    return SDL_VERSIONNUM(linked.major, linked.minor, linked.patch) >= SDL_VERSIONNUM(2, 0, 14);
#else
    return false;
#endif
}

/* A DualShock 4 or DualSense SDL has no mapping for gets one for its GUID
 * (stream_sys.h ss_sony_mapping), so it opens as a game controller. */
static void sony_prepare(MlApp *app, int index)
{
    if(SDL_IsGameController(index)) return;
    SDL_Joystick *joystick = SDL_JoystickOpen(index);
    if(!joystick) return;
    int model = ss_sony_model(SDL_JoystickGetVendor(joystick), SDL_JoystickGetProduct(joystick));
    if(model && !pad_find(app, SDL_JoystickInstanceID(joystick))) {
        int buttons = SDL_JoystickNumButtons(joystick), axes = SDL_JoystickNumAxes(joystick);
        int hats = SDL_JoystickNumHats(joystick);
        SsSonyLayout layout = ss_sony_layout(buttons, axes, hats);
        char guid[33];
        char mapping[512];
        SDL_JoystickGetGUIDString(SDL_JoystickGetGUID(joystick), guid, sizeof(guid));
        if(!ss_sony_mapping(mapping, sizeof(mapping), guid, model, layout, sdl_has_2_0_14()))
            fprintf(stdout, "[native] PS%d pad with an unknown layout, read raw\n", model);
        else if(SDL_GameControllerAddMapping(mapping) < 0)
            fprintf(stderr, "[native] could not add the PS%d pad mapping: %s\n", model, SDL_GetError());
        else
            fprintf(stdout, "[native] PS%d pad mapped from the kernel's %s\n", model, ss_sony_layout_name(layout));
        fflush(stdout);
    }
    SDL_JoystickClose(joystick);
}

static void pad_open(MlApp *app, int index, bool announce)
{
    SDL_GameController *controller = NULL;
    SDL_Joystick *joystick = NULL;
    const char *name = NULL;
    sony_prepare(app, index);
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
    if(pad_find(app, id)) {
        if(controller) SDL_GameControllerClose(controller);
        else SDL_JoystickClose(joystick);
        return;
    }
    if(!controller && SDL_JoystickNumButtons(joystick) < 4) {
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
    slot->builtin = ss_builtin_pad("", slot->name, SDL_JoystickGetVendor(joystick), SDL_JoystickGetProduct(joystick),
                                   joystick_path(joystick));
    if(!si_pad_attach(&app->input, id, slot->builtin ? SI_PAD_BUILTIN : 0u)) {
        fprintf(stdout, "[native] controller ignored, %d are open already: %s\n", MAX_PADS, slot->name);
        fflush(stdout);
        if(controller) SDL_GameControllerClose(controller);
        else SDL_JoystickClose(joystick);
        memset(slot, 0, sizeof(*slot));
        return;
    }
    fprintf(stdout, "[native] controller added: %s (%s%s)\n", slot->name,
            controller ? "game controller" : slot->xpad_layout ? "joystick, X360 order" : "joystick",
            slot->builtin ? ", built-in" : "");
    fflush(stdout);
    if(announce) toast(app, "Controller connected", OSD_TOAST_GOOD, TOAST_SHORT_MS);
}

static void pad_close(MlApp *app, StreamPad *pad, uint32_t now, bool live)
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

/* ---- the stream menu ---- */

static const char *menu_label(MenuRow row)
{
    switch(row) {
        case ROW_RESUME: return "Resume";
        case ROW_MOUSE: return "Mouse mode (desktop)";
        case ROW_STATS: return "Stream stats";
        case ROW_RUMBLE: return "Rumble";
        case ROW_VOLUME: return "Stream volume";
        case ROW_DPAD: return "D-pad works as";
        case ROW_SEND: return "Send to the PC";
        case ROW_CONTROLS: return "Controls";
        case ROW_DISCONNECT: return "Disconnect (game keeps running)";
        case ROW_QUIT_APP: return "Quit the game on the PC";
        default: return "";
    }
}

static void menu_value(MlApp *app, MenuRow row, char *out, size_t size)
{
    out[0] = '\0';
    switch(row) {
        case ROW_MOUSE: textf(out, size, "%s", app->mouse_mode ? "On" : "Off"); break;
        case ROW_STATS: textf(out, size, "%s", app->stats_visible ? "On" : "Off"); break;
        case ROW_RUMBLE: textf(out, size, "%s", app->rumble_enabled ? "On" : "Off"); break;
        case ROW_VOLUME: textf(out, size, "%u%%", atomic_load(&app->volume)); break;
        case ROW_DPAD: textf(out, size, "%s", app->input.dpad_stick ? "Left stick" : "D-pad"); break;
        case ROW_SEND: textf(out, size, "%s", send_names[app->send_choice]); break;
        default: break;
    }
}

static void menu_change(MlApp *app, int delta, uint32_t now)
{
    switch((MenuRow)app->menu_cursor) {
        case ROW_MOUSE:
            set_mouse_mode(app, !app->mouse_mode, now);
            break;
        case ROW_STATS:
            app->stats_visible = !app->stats_visible;
            break;
        case ROW_RUMBLE:
            app->rumble_enabled = !app->rumble_enabled;
            apply_rumble(app, now);
            break;
        case ROW_VOLUME: {
            int volume = (int)atomic_load(&app->volume) + delta * 10;
            if(volume < 0) volume = 0;
            if(volume > 100) volume = 100;
            atomic_store(&app->volume, (unsigned int)volume);
            break;
        }
        case ROW_DPAD:
            si_set_dpad_stick(&app->input, !app->input.dpad_stick);
            break;
        case ROW_SEND:
            app->send_choice = (app->send_choice + (delta < 0 ? SEND_COUNT - 1 : 1)) % SEND_COUNT;
            break;
        default:
            break;
    }
    app->osd_dirty = true;
}

static void menu_open(MlApp *app, uint32_t now)
{
    app->menu_open = true;
    app->menu_controls = false;
    app->menu_cursor = ROW_RESUME;
    update_modal(app, now);
}

static void menu_close(MlApp *app, uint32_t now)
{
    app->menu_open = false;
    app->menu_controls = false;
    update_modal(app, now);
}

static void menu_nav(MlApp *app, SiInput input, uint32_t now)
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
                case ROW_RESUME:
                    menu_close(app, now);
                    return;
                case ROW_SEND:
                    /* The menu closes first: the keys go to the PC, not to a
                     * pad that a modal screen keeps idle. */
                    menu_close(app, now);
                    send_combo(app);
                    return;
                case ROW_CONTROLS:
                    app->menu_controls = true;
                    break;
                case ROW_DISCONNECT:
                    menu_close(app, now);
                    fprintf(stdout, "[native] disconnect from the stream menu\n");
                    request_quit(app);
                    return;
                case ROW_QUIT_APP:
                    menu_close(app, now);
                    app->quit_app = true;
                    fprintf(stdout, "[native] quit the game on the PC from the stream menu\n");
                    request_quit(app);
                    return;
                case ROW_MOUSE:
                    menu_change(app, 1, now);
                    toast(app, app->mouse_mode ? "Mouse mode: left stick moves, B clicks, A right-clicks"
                                               : "Mouse mode off: the controller is a gamepad again",
                          OSD_TOAST_INFO, TOAST_LONG_MS);
                    menu_close(app, now);
                    return;
                default:
                    menu_change(app, 1, now);
                    break;
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

/* ---- events ---- */

static void pad_button(MlApp *app, int32_t id, SiInput input, bool down, uint32_t now)
{
    bool was_modal = app->modal;
    si_pad_button(&app->input, id, input, down, now);
    if(down && was_modal && app->menu_open) menu_nav(app, input, now);
}

static void joystick_hat(MlApp *app, StreamPad *pad, Uint8 value, uint32_t now)
{
    static const struct {
        Uint8 mask;
        SiInput input;
    } dirs[] = {
        {SDL_HAT_UP, SI_IN_UP}, {SDL_HAT_DOWN, SI_IN_DOWN}, {SDL_HAT_LEFT, SI_IN_LEFT}, {SDL_HAT_RIGHT, SI_IN_RIGHT},
    };
    for(size_t i = 0; i < sizeof(dirs) / sizeof(dirs[0]); i++) {
        bool before = (pad->hat & dirs[i].mask) != 0;
        bool after = (value & dirs[i].mask) != 0;
        if(before != after) pad_button(app, pad->id, dirs[i].input, after, now);
    }
    pad->hat = value;
}

static void handle_event(MlApp *app, const SDL_Event *event, uint32_t now)
{
    switch(event->type) {
        case SDL_QUIT:
            request_quit(app);
            break;
        case SDL_KEYDOWN:
            if(event->key.keysym.sym == SDLK_ESCAPE) request_quit(app);
            break;
        case SDL_CONTROLLERDEVICEADDED:
            pad_open(app, event->cdevice.which, now - app->start_ticks > 2000u);
            break;
        case SDL_JOYDEVICEADDED:
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

static void handle_input_events(MlApp *app, uint32_t now)
{
    SiEvent event;
    while(si_poll_event(&app->input, &event)) {
        switch(event.type) {
            case SI_EV_OPEN_MENU:
                menu_open(app, now);
                break;
            case SI_EV_QUIT:
                fprintf(stdout, "[native] exit requested by START+SELECT\n");
                request_quit(app);
                break;
            case SI_EV_DPAD_STICK:
                toast(app, event.value ? "D-pad now moves the left stick" : "D-pad is the D-pad again", OSD_TOAST_INFO,
                      TOAST_SHORT_MS);
                break;
            default:
                /* Swipes, the Menu finger and the touchpad combos are the
                 * PlayStation layout's; an Xbox pad has no touchpad. */
                break;
        }
    }
}

/* ---- the connection's outcome ---- */

static void ports_text(MlApp *app, unsigned int flags)
{
    app->port_flags[0] = '\0';
    if(flags) LiStringifyPortFlags(flags, ", ", app->port_flags, (int)sizeof(app->port_flags));
}

static void start_failed(MlApp *app)
{
    const MlConfig *config = app->config;
    char text[400], title[120];
    if(atomic_load(&app->decoder_failed)) {
        if(atomic_load(&app->video_format) & VIDEO_FORMAT_MASK_H264)
            textf(text, sizeof(text), "The H.264 video decoder could not start. Restart the handheld and try again.");
        else
            textf(text, sizeof(text), "The %s video decoder could not start. Choose H.264 in JukaHub's Moonlight settings for %s.",
                  codec_label(app), config->name);
        fail_with_card(app, 6, "decoder_failed", "Video problem", text);
        return;
    }
    if(atomic_load(&app->audio_failed)) {
        fail_with_card(app, 4, "init_failed", "Sound problem", "The stream's sound could not start (Opus decoder).");
        return;
    }
    int stage = atomic_load(&app->stage_failed) ? atomic_load(&app->failed_stage) : atomic_load(&app->stage);
    app->error_code = atomic_load(&app->stage_failed) ? atomic_load(&app->failed_error) : atomic_load(&app->start_rc);
    textf(app->fail_stage, sizeof(app->fail_stage), "%s", LiGetStageName(stage));
    ports_text(app, LiGetPortFlagsFromStage(stage));
    if(stage == STAGE_RTSP_HANDSHAKE && app->error_code >= 400 && app->error_code < 600) {
        /* The PC answered with an RTSP error status (moonlight-common-c passes
         * it on as the code), so ports are not the problem: Sunshine's 503 or
         * 404 for a launch that failed, expired or went to another client. */
        textf(text, sizeof(text), "%s refused the stream (RTSP %d). Check Vibepollo / Sunshine's log on it: the game may "
              "have failed to start. Then start it again from JukaHub.", config->name, app->error_code);
        textf(title, sizeof(title), "%s refused the stream", config->name);
        fail_with_card(app, 8, "connect_failed", title, text);
        return;
    }
    if(app->port_flags[0] && config->remote)
        textf(text, sizeof(text), "%s did not answer at %s (%s failed, error %d). Check that Vibepollo / Sunshine is running "
              "on it and that the router forwards %s to it.", config->name, config->host, app->fail_stage,
              app->error_code, app->port_flags);
    else if(app->port_flags[0])
        textf(text, sizeof(text), "%s did not answer at %s (%s failed, error %d). Check that Vibepollo / Sunshine is running "
              "on it and that its firewall allows %s.", config->name, config->host, app->fail_stage, app->error_code,
              app->port_flags);
    else
        textf(text, sizeof(text), "%s did not answer at %s (%s failed, error %d). Check that Vibepollo / Sunshine is running "
              "on it and that this handheld is on the same network.", config->name, config->host, app->fail_stage,
              app->error_code);
    textf(title, sizeof(title), "Couldn't connect to %s", config->name);
    fail_with_card(app, 8, "connect_failed", title, text);
}

static void handle_termination(MlApp *app)
{
    const MlConfig *config = app->config;
    int code = atomic_load(&app->terminate_code);
    char text[400], title[120];
    if(app->result) return; /* the user left first */
    app->error_code = code;
    if(code == ML_ERROR_GRACEFUL_TERMINATION) {
        if(config->app_name[0]) textf(text, sizeof(text), "%s was closed on %s.", config->app_name, config->name);
        else textf(text, sizeof(text), "%s ended the stream.", config->name);
        set_outcome(app, 0, "host_ended", text);
        return;
    }
    ports_text(app, LiGetPortFlagsFromTerminationErrorCode(code));
    if(code == ML_ERROR_NO_VIDEO_TRAFFIC || code == ML_ERROR_NO_VIDEO_FRAME) {
        if(app->port_flags[0])
            textf(text, sizeof(text), "No video arrived from %s (error %d). Check that its firewall allows %s, or try a lower "
                  "quality in JukaHub's Moonlight settings.", config->name, code, app->port_flags);
        else
            textf(text, sizeof(text), "No video arrived from %s (error %d). Try a lower quality in JukaHub's Moonlight settings.",
                  config->name, code);
        textf(title, sizeof(title), "No video from %s", config->name);
        fail_with_card(app, 11, "no_video", title, text);
        return;
    }
    if(code == ML_ERROR_UNEXPECTED_EARLY_TERMINATION)
        textf(text, sizeof(text), "%s ended the stream right after it started (error %d). Check that the game starts on the "
              "PC, then try again.", config->name, code);
    else if(app->port_flags[0])
        textf(text, sizeof(text), "The connection to %s was lost (error %d). Check the Wi-Fi signal and that the firewall "
              "allows %s.", config->name, code, app->port_flags);
    else
        textf(text, sizeof(text), "The connection to %s was lost (error %d). Check the Wi-Fi signal.", config->name, code);
    fail_with_card(app, 11, "connection_lost", "Connection lost", text);
}

static void handle_connection(MlApp *app, uint32_t now)
{
    if(app->start_thread_live && atomic_load(&app->start_done)) {
        pthread_join(app->start_thread, NULL);
        app->start_thread_live = false;
        if(atomic_load(&app->start_rc) == 0) {
            app->connection_live = true;
            set_status(app, "Connected, waiting for the video");
            send_arrival(app);
        } else if(!app->result) {
            fprintf(stderr, "[native] connection start failed (%d)\n", atomic_load(&app->start_rc));
            start_failed(app);
            atomic_store(&app->running, false);
        }
    }
    if(!app->connection_live && app->start_thread_live) {
        int stage = atomic_load(&app->stage);
        if(stage > 0 && stage != app->shown_stage) {
            app->shown_stage = stage;
            set_status(app, LiGetStageName(stage));
        }
    }
    if(atomic_exchange(&app->rumble_dirty, false)) apply_rumble(app, now);
    if(atomic_exchange(&app->conn_status_dirty, false) && atomic_load(&app->conn_status) == CONN_STATUS_POOR &&
       (!app->poor_toast_at || now - app->poor_toast_at >= POOR_TOAST_MS)) {
        char text[160];
        app->poor_toast_at = now ? now : 1;
        textf(text, sizeof(text), "Weak connection to %s", app->config->name);
        toast(app, text, OSD_TOAST_WARN, TOAST_LONG_MS);
    }
    if(atomic_load(&app->terminated) && !app->terminate_handled) {
        app->terminate_handled = true;
        handle_termination(app);
        atomic_store(&app->running, false);
    }
    if(atomic_load(&app->errors_in_row) >= DECODE_FAIL_LIMIT && !app->result) {
        fail_with_card(app, 10, "video_failed", "Video problem",
                       "The video stopped decoding. Try H.264 or a lower quality in JukaHub's Moonlight settings.");
        atomic_store(&app->running, false);
    }
}

static void battery_tick(MlApp *app, uint32_t now)
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

/* The streaming IYUV texture for a picture of this size and its aspect-fit
 * place on the screen: made when there is none, made again when the size
 * changed. The self-test makes it up front, as its screen check. */
static bool ensure_texture(MlApp *app, int width, int height, int format)
{
    if(app->texture && (width != app->video_w || height != app->video_h)) {
        fprintf(stdout, "[native] video size changed: %dx%d -> %dx%d\n", app->video_w, app->video_h, width, height);
        fflush(stdout);
        SDL_DestroyTexture(app->texture);
        app->texture = NULL;
        app->destination_ready = false;
    }
    if(app->texture) return true;
    app->texture = SDL_CreateTexture(app->renderer, SDL_PIXELFORMAT_IYUV, SDL_TEXTUREACCESS_STREAMING, width, height);
    if(!app->texture) {
        fprintf(stderr, "[native] SDL texture failed: %s\n", SDL_GetError());
        return false;
    }
    /* Aspect-fit; JukaHub asks for the panel's own size, so normally 1:1. */
    int output_w = 0, output_h = 0;
    SDL_GetRendererOutputSize(app->renderer, &output_w, &output_h);
    if(output_w <= 0 || output_h <= 0) {
        output_w = width;
        output_h = height;
    }
    app->destination = (SDL_Rect){0, 0, output_w, output_h};
    double source_ratio = (double)width / height;
    double output_ratio = (double)output_w / output_h;
    if(output_ratio > source_ratio) {
        app->destination.w = (int)(output_h * source_ratio);
        app->destination.x = (output_w - app->destination.w) / 2;
    } else if(output_ratio < source_ratio) {
        app->destination.h = (int)(output_w / source_ratio);
        app->destination.y = (output_h - app->destination.h) / 2;
    }
    app->destination_ready = true;
    app->video_w = width;
    app->video_h = height;
    fprintf(stdout, "[native] video frame: %dx%d format=%d on %dx%d\n", width, height, format, output_w, output_h);
    fflush(stdout);
    return true;
}

static bool update_video(MlApp *app, bool *new_frame)
{
    bool have = false;
    pthread_mutex_lock(&app->frame_lock);
    if(app->slot_ready) {
        av_frame_unref(app->render_frame);
        av_frame_move_ref(app->render_frame, app->slot);
        app->slot_ready = false;
        have = true;
    }
    pthread_mutex_unlock(&app->frame_lock);
    if(!have) return true;
    AVFrame *frame = app->render_frame;
    AVFrame *display = frame;

    if(frame->format != AV_PIX_FMT_YUV420P && frame->format != AV_PIX_FMT_YUVJ420P) {
        app->sws = sws_getCachedContext(app->sws, frame->width, frame->height, frame->format, frame->width,
                                       frame->height, AV_PIX_FMT_YUV420P, SWS_FAST_BILINEAR, NULL, NULL, NULL);
        if(!app->sws) {
            fprintf(stderr, "[native] cannot convert video pixel format %d\n", frame->format);
            av_frame_unref(frame);
            return false;
        }
        if(app->converted && (app->converted->width != frame->width || app->converted->height != frame->height))
            av_frame_free(&app->converted);
        if(!app->converted) {
            app->converted = av_frame_alloc();
            if(!app->converted) {
                av_frame_unref(frame);
                return false;
            }
            app->converted->format = AV_PIX_FMT_YUV420P;
            app->converted->width = frame->width;
            app->converted->height = frame->height;
            if(av_frame_get_buffer(app->converted, 32) < 0) {
                av_frame_free(&app->converted);
                av_frame_unref(frame);
                return false;
            }
        }
        av_frame_make_writable(app->converted);
        sws_scale(app->sws, (const uint8_t *const *)frame->data, frame->linesize, 0, frame->height,
                  app->converted->data, app->converted->linesize);
        display = app->converted;
    }

    if(!ensure_texture(app, display->width, display->height, frame->format)) {
        av_frame_unref(frame);
        return false;
    }
    if(SDL_UpdateYUVTexture(app->texture, NULL, display->data[0], display->linesize[0], display->data[1],
                            display->linesize[1], display->data[2], display->linesize[2]) != 0) {
        fprintf(stderr, "[native] texture update failed: %s\n", SDL_GetError());
        av_frame_unref(frame);
        return false;
    }
    av_frame_unref(frame);
    app->rendered_frames++;
    *new_frame = true;
    return true;
}

static void update_stats(MlApp *app, uint32_t now)
{
    if(now - app->stats_at < STATS_MS) return;
    double seconds = (double)(now - app->stats_at) / 1000.0;
    uint64_t decoded = atomic_load(&app->decoded_frames);
    uint64_t decode_us = atomic_load(&app->decode_us);
    uint64_t dropped = atomic_load(&app->dropped_frames);
    uint64_t rendered_delta = app->rendered_frames - app->stats_rendered;
    uint64_t decoded_delta = decoded - app->stats_decoded;
    double fps = seconds > 0.0 ? (double)rendered_delta / seconds : 0.0;
    double decode_ms = decoded_delta ? (double)(decode_us - app->stats_decode_us) / (double)decoded_delta / 1000.0 : 0.0;
    uint32_t rtt = 0, variance = 0;
    bool have_rtt = app->connection_live && LiGetEstimatedRttInfo(&rtt, &variance);
    int queued = app->connection_live ? LiGetPendingVideoFrames() : 0;
    char ping[32] = "";
    if(have_rtt) textf(ping, sizeof(ping), " - ping %u ms", (unsigned)rtt);
    textf(app->stats_line, sizeof(app->stats_line), "%.1f fps - decode %.1f ms%s - %dx%d %s - dropped %llu - queue %d", fps,
          decode_ms, ping, app->video_w, app->video_h, codec_label(app), (unsigned long long)(dropped - app->stats_dropped),
          queued);
    if(app->stats_visible) app->osd_dirty = true;
    if(now - app->stats_log_at >= STATS_LOG_MS) {
        app->stats_log_at = now;
        fprintf(stdout, "[native] quality: %s - errors %llu - sound packets dropped %llu\n", app->stats_line,
                (unsigned long long)atomic_load(&app->decode_errors), (unsigned long long)atomic_load(&app->audio_dropped));
        fflush(stdout);
    }
    app->stats_at = now;
    app->stats_rendered = app->rendered_frames;
    app->stats_decoded = decoded;
    app->stats_decode_us = decode_us;
    app->stats_dropped = dropped;
}

static const SDL_Color ML_PANEL = {16, 20, 30, 230};
static const SDL_Color ML_TEXT = {242, 245, 250, 255};
static const SDL_Color ML_TEXT2 = {178, 188, 204, 255};
static const SDL_Color ML_TEXT3 = {130, 140, 158, 255};
static const SDL_Color ML_ACCENT = {72, 132, 255, 255};

static int px(const Osd *osd, float value)
{
    float scaled = value * osd->ui;
    return (int)(scaled < 0.0f ? scaled - 0.5f : scaled + 0.5f);
}

static SDL_Color faded(SDL_Color color, float alpha)
{
    if(alpha < 0.0f) alpha = 0.0f;
    if(alpha > 1.0f) alpha = 1.0f;
    color.a = (Uint8)((float)color.a * alpha + 0.5f);
    return color;
}

static void text_centered(Osd *osd, const char *text, int cx, int y, float size, SDL_Color color)
{
    osd_text(osd, text, cx - osd_text_width(osd, text, size) / 2, y, size, color);
}

/* The two-line hint when the video starts, in the Xbox layout's words. */
static void draw_hint(MlApp *app, float alpha)
{
    Osd *osd = &app->osd;
    const char *first = "Menu: Xbox Guide    Hold Menu: stream menu    Hold Start + Select: disconnect";
    const char *second = "Desktop: hold Menu > Mouse mode    Menu + L3: D-pad as left stick";
    float size = 20;
    int lh = osd_line_height(osd, size);
    int pad = px(osd, 24);
    int w1 = osd_text_width(osd, first, size), w2 = osd_text_width(osd, second, size);
    int w = (w1 > w2 ? w1 : w2) + 2 * pad;
    if(w > osd->out_w - px(osd, 24)) w = osd->out_w - px(osd, 24);
    int h = 2 * lh + 2 * px(osd, 14);
    int x = (osd->out_w - w) / 2;
    int y = osd->out_h - h - px(osd, 34);
    char line[200];
    osd_fill(osd, x, y, w, h, px(osd, 16), faded(ML_PANEL, alpha));
    osd_fit(osd, first, size, w - 2 * pad, line, sizeof(line));
    text_centered(osd, line, x + w / 2, y + px(osd, 12), size, faded(ML_TEXT, alpha));
    osd_fit(osd, second, size, w - 2 * pad, line, sizeof(line));
    text_centered(osd, line, x + w / 2, y + px(osd, 12) + lh, size, faded(ML_TEXT2, alpha));
}

/* The controls table (stream menu -> Controls). Labels are a TrimUI's; the
 * buttons map by position, so the bottom one is the PC pad's A. */
static const char *const controls_rows[][2] = {
    {"B / A / Y / X", "A / B / X / Y (by position)"},
    {"Start / Select", "Start (Menu) / Back (View)"},
    {"Menu", "Xbox Guide button"},
    {"Menu + L3", NULL},
    {"Hold Menu", "This menu"},
    {"Hold Start + Select", "Disconnect"},
    {"In mouse mode", ""},
    {"Left / right stick", "Pointer / scroll"},
    {"B / A", "Left click / right click"},
    {"D-pad, Start, Select", "Arrow keys, Enter, Esc"},
};
#define CONTROLS_ROW_COUNT ((int)(sizeof(controls_rows) / sizeof(controls_rows[0])))

static void draw_controls(MlApp *app)
{
    Osd *osd = &app->osd;
    float size = 20;
    int lh = osd_line_height(osd, size);
    int w = px(osd, 680);
    if(w > osd->out_w - px(osd, 40)) w = osd->out_w - px(osd, 40);
    int pad = px(osd, 34);
    int h = CONTROLS_ROW_COUNT * lh + px(osd, 16) + px(osd, 130);
    int x = (osd->out_w - w) / 2;
    int y = (osd->out_h - h) / 2;
    if(y < px(osd, 8)) y = px(osd, 8);
    SDL_Color shade = {0, 0, 0, 150};
    osd_fill(osd, 0, 0, osd->out_w, osd->out_h, 0, shade);
    osd_fill(osd, x, y, w, h, px(osd, 18), ML_PANEL);
    osd_fill(osd, x + px(osd, 28), y, w - px(osd, 56), px(osd, 3) > 2 ? px(osd, 3) : 2, 1, ML_ACCENT);
    osd_text(osd, "Controls", x + pad, y + px(osd, 20), 28, ML_TEXT);
    int split = x + pad + px(osd, 220);
    int top = y + px(osd, 70);
    char line[160];
    for(int i = 0; i < CONTROLS_ROW_COUNT; i++) {
        const char *what = controls_rows[i][1];
        if(i == 3) what = app->input.dpad_stick ? "D-pad back to D-pad (now: left stick)" : "D-pad as left stick (now: D-pad)";
        bool heading = what && !what[0];
        osd_text(osd, controls_rows[i][0], x + pad, top + i * lh, size, heading ? ML_TEXT : ML_TEXT2);
        if(heading || !what) continue;
        osd_fit(osd, what, size, x + w - pad - split, line, sizeof(line));
        osd_text(osd, line, split, top + i * lh, size, ML_TEXT);
    }
    osd_text(osd, "B  back", x + pad, y + h - px(osd, 42), 18, ML_TEXT3);
}

static void draw_mouse_badge(MlApp *app)
{
    Osd *osd = &app->osd;
    const char *text = "Mouse mode";
    float size = 18;
    int w = osd_text_width(osd, text, size) + px(osd, 28);
    int h = osd_line_height(osd, size) + px(osd, 10);
    int x = osd->out_w - w - px(osd, 12);
    int y = px(osd, 12);
    SDL_Color shade = {0, 0, 0, 150};
    osd_fill(osd, x, y, w, h, h / 2, shade);
    osd_text(osd, text, x + px(osd, 14), y + px(osd, 5), size, ML_ACCENT);
}

static float fade_alpha(uint32_t now, uint32_t until, uint32_t fade_ms)
{
    if((int32_t)(now - until) >= 0) return 0.0f;
    uint32_t left = until - now;
    return left >= fade_ms ? 1.0f : (float)left / (float)fade_ms;
}

static void draw_menu(MlApp *app, uint32_t now)
{
    if(app->menu_controls) {
        draw_controls(app);
        return;
    }
    OsdMenuView view;
    char line1[200], line2[160], battery[48] = "", played[40];
    memset(&view, 0, sizeof(view));
    view.title = app->config->name;
    unsigned long seconds = app->first_frame_at ? (unsigned long)(now - app->first_frame_at) / 1000ul : 0;
    if(app->battery.valid)
        textf(battery, sizeof(battery), " - Battery %d%%%s", app->battery.percent, app->battery.on_power ? " (charging)" : "");
    textf(played, sizeof(played), "Streaming %lu:%02lu", seconds / 60, seconds % 60);
    if(app->config->app_name[0]) textf(line1, sizeof(line1), "%s - %s%s", app->config->app_name, played, battery);
    else textf(line1, sizeof(line1), "%s%s", played, battery);
    if(app->video_w > 0)
        textf(line2, sizeof(line2), "%s - %dx%d - %u fps - %u kb/s", codec_label(app), app->video_w, app->video_h,
              app->config->fps, app->config->bitrate);
    else
        textf(line2, sizeof(line2), "%s - %s", codec_label(app), app->config->host);
    view.line1 = line1;
    view.line2 = line2;
    view.count = ROW_COUNT;
    view.cursor = app->menu_cursor;
    for(int i = 0; i < ROW_COUNT; i++) {
        snprintf(view.rows[i].label, sizeof(view.rows[i].label), "%s", menu_label((MenuRow)i));
        menu_value(app, (MenuRow)i, view.rows[i].value, sizeof(view.rows[i].value));
    }
    osd_draw_menu(&app->osd, &view);
}

static void draw_overlays(MlApp *app, uint32_t now)
{
    Osd *osd = &app->osd;
    if(!app->osd_ready) return;
    const MlConfig *config = app->config;
    if(!app->first_frame_at) {
        char title[160], detail[300];
        textf(title, sizeof(title), "Connecting to %s", config->name);
        if(config->app_name[0])
            textf(detail, sizeof(detail), "%s - %s - %ux%u %u fps", config->host, config->app_name, config->width,
                  config->height, config->fps);
        else
            textf(detail, sizeof(detail), "%s - %ux%u %u fps", config->host, config->width, config->height, config->fps);
        OsdConnectView view = {title, detail, app->status_line, (now - app->start_ticks) / 1000u, now,
                               "Hold START + SELECT to cancel"};
        osd_draw_connect(osd, &view);
    }
    if(app->first_frame_at && app->stats_visible) osd_draw_stats(osd, app->stats_line);
    if(app->mouse_mode && !app->modal) draw_mouse_badge(app);
    if(app->hint_until && !app->modal) {
        float alpha = fade_alpha(now, app->hint_until, 500);
        if(alpha > 0.0f) draw_hint(app, alpha);
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
}

static void present(MlApp *app, uint32_t now)
{
    bool fills_output = app->texture && app->destination_ready && app->destination.x == 0 && app->destination.y == 0 &&
                        app->destination.w == app->osd.out_w && app->destination.h == app->osd.out_h;
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

static bool animating(const MlApp *app, uint32_t now)
{
    return !app->first_frame_at || app->toast_until || app->hint_until || si_quit_progress(&app->input, now) > 0.0f;
}

/* A failure the user should read before JukaHub comes back; the connection is
 * stopped already. A stop signal ends it like a button does. */
static void run_error_card(MlApp *app)
{
    if(!app->osd_ready || !app->renderer) return;
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

/* ---- the connect thread ---- */

static void *connect_thread(void *arg)
{
    MlApp *app = arg;
    int rc = LiStartConnection(&app->server, &app->stream, &app->listener, &app->video_callbacks,
                               &app->audio_callbacks, NULL, 0, NULL, 0);
    /* moonlight-common-c keeps its own copy of the stream configuration. */
    ss_wipe(app->stream.remoteInputAesKey, sizeof(app->stream.remoteInputAesKey));
    atomic_store(&app->start_rc, rc);
    atomic_store(&app->start_done, true);
    return NULL;
}

/* Lets the PC go: mouse buttons and keys released and an idle pad first, so
 * nothing stays held, then the connection stopped. A start still under way is
 * interrupted and waited for. Safe to call twice. */
static void stop_connection(MlApp *app)
{
    if(app->start_thread_live) {
        uint32_t waited_from = SDL_GetTicks();
        while(!atomic_load(&app->start_done) && SDL_GetTicks() - waited_from < INTERRUPT_WAIT_MS) {
            LiInterruptConnection();
            SDL_Delay(20);
        }
        if(!atomic_load(&app->start_done)) {
            fprintf(stderr, "[native] the connect stage did not stop in time; leaving it\n");
            fflush(stderr);
            app->start_thread_live = false; /* never joined: the process ends */
            app->start_abandoned = true;
            return;
        }
        pthread_join(app->start_thread, NULL);
        app->start_thread_live = false;
        if(atomic_load(&app->start_rc) == 0) app->connection_live = true;
    }
    if(!app->connection_live) return;
    mouse_release_all(app);
    LiSendMultiControllerEvent(0, 0x1, 0, 0, 0, 0, 0, 0, 0);
    LiStopConnection();
    app->connection_live = false;
}

/* ---- setup ---- */

static bool init_sdl(MlApp *app)
{
    SDL_SetHint("SDL_GAMECONTROLLER_USE_BUTTON_LABELS", "0");
    SDL_SetHint("SDL_JOYSTICK_HIDAPI_PS4_RUMBLE", "1");
    SDL_SetHint("SDL_JOYSTICK_HIDAPI_PS5_RUMBLE", "1");
    SDL_SetHint(SDL_HINT_JOYSTICK_ALLOW_BACKGROUND_EVENTS, "1");
    if(SDL_Init(SDL_INIT_VIDEO | SDL_INIT_AUDIO | SDL_INIT_JOYSTICK | SDL_INIT_GAMECONTROLLER | SDL_INIT_HAPTIC) != 0) {
        fprintf(stderr, "[native] SDL init failed: %s\n", SDL_GetError());
        return false;
    }
    /* The PC encodes BT.709, limited range (configure_stream). SDL's automatic
     * mode would use BT.601 for a texture of 576 lines or fewer (the light
     * preset: 960x540, 768x576), which shifts reds and greens. SDL 2.0.8+. */
    SDL_SetYUVConversionMode(SDL_YUV_CONVERSION_BT709);
    /* Before any pad is opened, so SDL_IsGameController sees it. */
    if(SDL_GameControllerAddMapping(TRIMUI_PAD_MAPPING) < 0)
        fprintf(stderr, "[native] could not add the TrimUI pad mapping: %s\n", SDL_GetError());
    SDL_SetHint(SDL_HINT_RENDER_SCALE_QUALITY, "linear");
    app->window = SDL_CreateWindow(APP_NAME, SDL_WINDOWPOS_UNDEFINED, SDL_WINDOWPOS_UNDEFINED, SCREEN_WIDTH, SCREEN_HEIGHT,
                                   SDL_WINDOW_FULLSCREEN_DESKTOP | SDL_WINDOW_SHOWN);
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
    SDL_RendererInfo info;
    if(SDL_GetRendererInfo(app->renderer, &info) == 0) {
        fprintf(stdout, "[native] renderer=%s accelerated=%d vsync=%d max_texture=%dx%d\n", info.name ? info.name : "unknown",
                !!(info.flags & SDL_RENDERER_ACCELERATED), !!(info.flags & SDL_RENDERER_PRESENTVSYNC),
                info.max_texture_width, info.max_texture_height);
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

/* The display's refresh rate for the PC (frame pacing), 60 when SDL cannot say. */
static int refresh_x100(void)
{
    SDL_DisplayMode mode;
    if(SDL_GetCurrentDisplayMode(0, &mode) == 0 && mode.refresh_rate > 0 && mode.refresh_rate <= 240)
        return mode.refresh_rate * 100;
    return 6000;
}

/* The formats to offer: what the session asks for, among the decoders this
 * build has. H.265 and auto both offer H.264 too, so a PC without HEVC still
 * streams (moonlight-common-c picks the best one both sides have). */
static int video_formats(const MlConfig *config)
{
    bool h264 = avcodec_find_decoder(AV_CODEC_ID_H264) != NULL;
    bool hevc = avcodec_find_decoder(AV_CODEC_ID_HEVC) != NULL;
    int formats = 0;
    if(h264) formats |= VIDEO_FORMAT_H264;
    if(hevc && config->codec != ML_CODEC_H264) formats |= VIDEO_FORMAT_H265;
    if(config->codec != ML_CODEC_H264 && !hevc)
        fprintf(stderr, "[native] this build has no HEVC decoder; offering H.264 only\n");
    return formats;
}

static void configure_stream(MlApp *app, const MlConfig *config, int formats)
{
    LiInitializeServerInformation(&app->server);
    app->server.address = config->host;
    app->server.serverInfoAppVersion = config->app_version;
    app->server.serverInfoGfeVersion = config->gfe_version[0] ? config->gfe_version : NULL;
    app->server.rtspSessionUrl = config->rtsp_url[0] ? config->rtsp_url : NULL;
    app->server.serverCodecModeSupport = config->codec_support;

    LiInitializeStreamConfiguration(&app->stream);
    app->stream.width = (int)config->width;
    app->stream.height = (int)config->height;
    app->stream.fps = (int)config->fps;
    app->stream.bitrate = (int)config->bitrate;
    app->stream.packetSize = config->remote ? 1024 : 1392;
    app->stream.streamingRemotely = config->remote ? STREAM_CFG_REMOTE : STREAM_CFG_LOCAL;
    app->stream.audioConfiguration = AUDIO_CONFIGURATION_STEREO;
    app->stream.supportedVideoFormats = formats;
    app->stream.clientRefreshRateX100 = refresh_x100();
    /* init_sdl sets SDL's YUV conversion to match (BT.709 at every size). */
    app->stream.colorSpace = COLORSPACE_REC_709;
    app->stream.colorRange = COLOR_RANGE_LIMITED;
#ifdef ENCFLG_ALL
    app->stream.encryptionFlags = config->remote ? ENCFLG_ALL : ENCFLG_AUDIO;
#endif
    memcpy(app->stream.remoteInputAesKey, config->ri_key, sizeof(app->stream.remoteInputAesKey));
    /* The IV is rikeyid's four bytes, big-endian, then zeros. */
    memset(app->stream.remoteInputAesIv, 0, sizeof(app->stream.remoteInputAesIv));
    uint32_t id = (uint32_t)config->ri_key_id;
    app->stream.remoteInputAesIv[0] = (char)(id >> 24);
    app->stream.remoteInputAesIv[1] = (char)(id >> 16);
    app->stream.remoteInputAesIv[2] = (char)(id >> 8);
    app->stream.remoteInputAesIv[3] = (char)id;

    LiInitializeConnectionCallbacks(&app->listener);
    app->listener.stageStarting = cl_stage_starting;
    app->listener.stageComplete = cl_stage_complete;
    app->listener.stageFailed = cl_stage_failed;
    app->listener.connectionStarted = cl_connection_started;
    app->listener.connectionTerminated = cl_connection_terminated;
    app->listener.logMessage = cl_log;
    app->listener.rumble = cl_rumble;
    app->listener.connectionStatusUpdate = cl_status_update;

    LiInitializeVideoCallbacks(&app->video_callbacks);
    app->video_callbacks.setup = dr_setup;
    app->video_callbacks.cleanup = dr_cleanup;
    app->video_callbacks.submitDecodeUnit = dr_submit;
    app->video_callbacks.capabilities = CAPABILITY_SLICES_PER_FRAME(4);

    LiInitializeAudioCallbacks(&app->audio_callbacks);
    app->audio_callbacks.init = ar_init;
    app->audio_callbacks.cleanup = ar_cleanup;
    app->audio_callbacks.decodeAndPlaySample = ar_decode;
}

static void cleanup(MlApp *app)
{
    stop_connection(app);
    /* Whatever the library's own cleanup left (a start that failed half way). */
    if(!app->start_thread_live && !app->start_abandoned) {
        dr_cleanup();
        ar_cleanup();
    }
    ss_motor_off(&app->motor);
    ss_wipe(app->stream.remoteInputAesKey, sizeof(app->stream.remoteInputAesKey));
    if(app->converted) av_frame_free(&app->converted);
    if(app->sws) sws_freeContext(app->sws);
    if(app->slot) av_frame_free(&app->slot);
    if(app->render_frame) av_frame_free(&app->render_frame);
    if(app->texture) SDL_DestroyTexture(app->texture);
    if(app->osd_ready) osd_fini(&app->osd);
    for(int i = 0; i < MAX_PADS; i++) pad_close(app, &app->pads[i], SDL_GetTicks(), false);
    if(app->renderer) SDL_DestroyRenderer(app->renderer);
    if(app->window) SDL_DestroyWindow(app->window);
    SDL_Quit();
}

static void finish(MlApp *app, const MlConfig *config)
{
    if(!app->result) request_quit(app);
    unsigned long seconds = 0;
    double fps_shown = 0.0;
    if(app->first_frame_at) {
        uint32_t ms = SDL_GetTicks() - app->first_frame_at;
        seconds = (unsigned long)ms / 1000ul;
        if(ms >= 1000u) fps_shown = (double)app->rendered_frames * 1000.0 / (double)ms;
    }
    uint64_t decoded = atomic_load(&app->decoded_frames);
    double decode_ms = decoded ? (double)atomic_load(&app->decode_us) / (double)decoded / 1000.0 : 0.0;
    if(!strcmp(app->result, "ok"))
        textf(app->message, sizeof(app->message), "Streamed for %lu min %lu s.", seconds / 60, seconds % 60);
    MlStatus status;
    memset(&status, 0, sizeof(status));
    status.rc = app->rc;
    status.result = app->result;
    status.message = app->message;
    status.connected = atomic_load(&app->connected) || app->first_frame_at;
    status.seconds = seconds;
    status.frames = (unsigned long long)app->rendered_frames;
    status.codec = codec_name(app);
    status.quit_app = app->quit_app;
    status.stage = app->fail_stage;
    status.error_code = app->error_code;
    status.port_flags = app->port_flags;
    status.decode_ms = decode_ms;
    status.fps_shown = fps_shown;
    if(config->status_file[0] && !write_status(config->status_file, config->host, &status))
        fprintf(stderr, "[native] could not write the status file %s: %s\n", config->status_file, strerror(errno));
    fprintf(stdout, "[native] result=%s rc=%d connected=%d seconds=%lu frames=%llu decode_ms=%.1f fps=%.1f quit_app=%d\n",
            app->result, app->rc, status.connected ? 1 : 0, seconds, (unsigned long long)app->rendered_frames, decode_ms,
            fps_shown, app->quit_app ? 1 : 0);
    fflush(stdout);
}

static void early_status(const MlConfig *config, int rc, const char *result, const char *message)
{
    if(!config->status_file[0]) return;
    MlStatus status;
    memset(&status, 0, sizeof(status));
    status.rc = rc;
    status.result = result;
    status.message = message;
    status.codec = requested_codec_name(config);
    status.stage = "";
    status.port_flags = "";
    write_status(config->status_file, config->host, &status);
    fprintf(stdout, "[native] result=%s rc=%d connected=0 seconds=0 frames=0\n", result, rc);
    fflush(stdout);
}

/* ---- the self-test (session key self_test=1) ----
 *
 * JukaHub's handheld check, no PC or network needed. After the same SDL, OSD
 * and pad set-up as a stream, a card fills in line by line:
 *   Screen          renderer, acceleration, vsync, output size, and the IYUV
 *                   streaming texture a stream draws into
 *   Video decoding  the embedded 1280x720 clip, split into frames by FFmpeg's
 *                   parser and decoded in passes by a decoder opened exactly
 *                   as a stream's (open_video_decoder); each frame is then
 *                   uploaded and shown as a stream does
 *   Picture upload  SDL_UpdateYUVTexture + SDL_RenderCopy per frame
 *   Sound           a short chime through a device opened with the stream's
 *                   spec (48 kHz, S16, stereo, 1024 samples) at the session volume
 *   Opus audio      a stereo multistream decoder like the stream's
 *   Moonlight core  a moonlight-common-c call, so the link is real
 *   Controllers     the pads open, the built-in one, a short motor buzz
 *   Buttons         "Press A to finish": any face button or Start, 20 s
 * The status file holds the contract's st_* keys (README.md, "Self-test").
 * Exit 0 when the video and decoding work, 10 when the video failed, 6 when
 * decoding failed or this build has no clip. */

#define ST_BENCH_MS 3000u /* decode passes run this long, at least 2 and at most ST_MAX_PASSES */
#define ST_MAX_PASSES 12
#define ST_MAX_PACKETS 32
#define ST_PRESS_MS 20000u
#define ST_RESULT_MS 1500u
#define ST_BUZZ_MS 200u
#define ST_CHIME_RATE 48000
#define ST_CHIME_FRAMES (ST_CHIME_RATE * 55 / 100) /* 0.55 s */
#define ST_DRAIN_MS 1500u
#define ST_TAIL_MS 150u
#define ST_CLIP_W 1280
#define ST_CLIP_H 720
/* Decode + upload a frame may take at 60 fps and leave room for the rest of
 * the frame (the rule JukaHub picks the default quality by, contract 5c). */
#define ST_FRAME_BUDGET_MS 11.0
#define ST_CPU0_FREQ "/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq"
#define ST_CPU0_GOVERNOR "/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"

typedef enum {
    ST_ROW_SCREEN = 0,
    ST_ROW_DECODE,
    ST_ROW_UPLOAD,
    ST_ROW_SOUND,
    ST_ROW_OPUS,
    ST_ROW_CORE,
    ST_ROW_PADS,
    ST_ROW_PRESS,
    ST_ROW_COUNT
} StRow;

static const char *const st_labels[ST_ROW_COUNT] = {
    "Screen", "Video decoding", "Picture upload", "Sound", "Opus audio", "Moonlight core", "Controllers", "Buttons",
};

typedef enum {
    ST_PENDING = 0,
    ST_RUNNING,
    ST_OK,
    ST_WARN,
    ST_FAIL,
    ST_SKIP
} StMark;

/* One line of the card: the font has no check mark, so the mark is a
 * coloured pill with a word in it. */
typedef struct {
    StMark mark;
    const char *word; /* "OK", "SLOW", "NONE", "FAIL", "--" */
    char detail[160];
} StLine;

typedef struct {
    StLine lines[ST_ROW_COUNT];
    bool prompt; /* "Press A to finish" is up, until prompt_until */
    uint32_t prompt_until;
    /* the status file's st_* values */
    bool video;        /* window, renderer, IYUV texture and every upload worked */
    char renderer[48];
    bool accel;
    bool vsync;
    int out_w;
    int out_h;
    bool clip;         /* this build has the clip */
    bool decode;       /* at least one whole pass decoded without an error */
    bool decode_error; /* the decoder did not start, or a frame failed */
    unsigned int frames;
    double idr_ms;
    double p_ms;
    double p_max_ms;
    double upload_ms;
    bool audio;
    bool opus;
    bool core;
    int pads;
    bool builtin;
    bool input;
    bool motor;
    char governor[32];
    long cpu_mhz;
    bool stopped;      /* Start + Select or a stop signal */
    char video_error[160];
    int rc;
    char message[300];
} SelfTest;

/* One frame of the clip as FFmpeg's parser cut it. */
typedef struct {
    uint8_t *data; /* size bytes, then AV_INPUT_BUFFER_PADDING_SIZE zeros */
    int size;
    bool key;
} StPacket;

/* Decode and upload times of the first pass, or of the passes after it. */
typedef struct {
    double idr_sum, p_sum, p_max, upload_sum;
    unsigned int idr_count, p_count, upload_count;
} StTimes;

static const SDL_Color ST_PANEL_COLOR = {16, 20, 30, 222};
static const SDL_Color ST_GREEN = {52, 199, 89, 255};
static const SDL_Color ST_AMBER = {255, 179, 64, 255};
static const SDL_Color ST_RED = {255, 95, 82, 255};
static const SDL_Color ST_GREY = {74, 82, 98, 255};
static const SDL_Color ST_INK = {16, 20, 30, 255};

__attribute__((format(printf, 5, 6)))
static void st_line(SelfTest *st, StRow row, StMark mark, const char *word, const char *format, ...)
{
    StLine *line = &st->lines[row];
    line->mark = mark;
    line->word = word;
    va_list args;
    va_start(args, format);
    vsnprintf(line->detail, sizeof(line->detail), format, args);
    va_end(args);
}

static bool st_stopped(MlApp *app)
{
    return !atomic_load(&app->running);
}

static const char *st_bit(bool value)
{
    return value ? "1" : "0";
}

/* The first line of a small sysfs file, trimmed; false when unreadable. */
static bool read_small_file(const char *path, char *out, size_t size)
{
    out[0] = '\0';
    int descriptor = open(path, O_RDONLY | O_CLOEXEC);
    if(descriptor < 0) return false;
    ssize_t got;
    for(;;) {
        got = read(descriptor, out, size - 1);
        if(got >= 0 || errno != EINTR) break;
    }
    close(descriptor);
    if(got <= 0) {
        out[0] = '\0';
        return false;
    }
    out[got] = '\0';
    char *newline = strchr(out, '\n');
    if(newline) *newline = '\0';
    trim(out);
    return out[0] != '\0';
}

/* cpu0's clock right now (the highest seen is kept), MHz. */
static void st_sample_cpu(SelfTest *st)
{
    char text[32];
    long khz = 0;
    if(read_small_file(ST_CPU0_FREQ, text, sizeof(text)) && parse_long(text, 1, 100000000L, &khz) &&
       khz / 1000 > st->cpu_mhz)
        st->cpu_mhz = khz / 1000;
}

/* What a 60 fps frame costs on this screen: decode + upload, scaled from the
 * 720p clip to the output's pixel count (contract 5c). */
static double st_frame_ms(const SelfTest *st)
{
    double scale = 1.0;
    if(st->out_w > 0 && st->out_h > 0) scale = (double)st->out_w * (double)st->out_h / (double)(ST_CLIP_W * ST_CLIP_H);
    return (st->p_ms + st->upload_ms) * scale;
}

/* ---- the self-test card ---- */

/* Black behind the picture, unless the last frame covers the whole output. */
static void st_clear(MlApp *app)
{
    bool covered = app->texture && app->rendered_frames && app->destination_ready && app->destination.x == 0 &&
                   app->destination.y == 0 && app->destination.w == app->osd.out_w && app->destination.h == app->osd.out_h;
    if(covered) return;
    SDL_SetRenderDrawColor(app->renderer, 0, 0, 0, 255);
    SDL_RenderClear(app->renderer);
}

/* Greedy word wrap into at most max_lines lines; the last one takes the rest,
 * cut with "..." when it does not fit. */
static void st_wrap(Osd *osd, const char *text, int x, int y, float size, int max_w, int max_lines, SDL_Color color)
{
    int lh = osd_line_height(osd, size);
    char line[320], fitted[320];
    int lines = 0;
    while(*text == ' ') text++;
    while(*text && lines < max_lines) {
        size_t length = strlen(text);
        size_t take = length;
        if(lines < max_lines - 1 && osd_text_width(osd, text, size) > max_w) {
            size_t best = 0;
            for(size_t i = 1; i < length && i < sizeof(line); i++) {
                if(text[i] != ' ') continue;
                memcpy(line, text, i);
                line[i] = '\0';
                if(osd_text_width(osd, line, size) > max_w) break;
                best = i;
            }
            if(best) take = best;
        }
        if(take >= sizeof(line)) take = sizeof(line) - 1;
        memcpy(line, text, take);
        line[take] = '\0';
        osd_fit(osd, line, size, max_w, fitted, sizeof(fitted));
        osd_text(osd, fitted, x, y + lines * lh, size, color);
        lines++;
        text += take;
        while(*text == ' ') text++;
    }
}

static void st_draw_card(MlApp *app, const SelfTest *st, uint32_t now)
{
    Osd *osd = &app->osd;
    const float size = 20.0f, mark_size = 16.0f, message_size = 21.0f, footer_size = 18.0f;
    static const char *const dots[3] = {".", "..", "..."};
    int lh = osd_line_height(osd, size);
    int row_h = px(osd, 38);
    int pad = px(osd, 32);
    int w = px(osd, 860);
    if(w > osd->out_w - px(osd, 24)) w = osd->out_w - px(osd, 24);
    int head = px(osd, 92);
    int message_h = 2 * osd_line_height(osd, message_size);
    int h = head + ST_ROW_COUNT * row_h + px(osd, 14) + message_h + px(osd, 12) + osd_line_height(osd, footer_size) +
            px(osd, 22);
    int x = (osd->out_w - w) / 2;
    int y = (osd->out_h - h) / 2;
    if(y < px(osd, 6)) y = px(osd, 6);
    osd_fill(osd, x, y, w, h, px(osd, 18), ST_PANEL_COLOR);
    osd_fill(osd, x + px(osd, 28), y, w - px(osd, 56), px(osd, 3) > 2 ? px(osd, 3) : 2, 1, ML_ACCENT);
    osd_text(osd, "Moonlight self-test", x + pad, y + px(osd, 18), 28, ML_TEXT);
    osd_text(osd, "Checks this handheld for streaming - no PC needed", x + pad, y + px(osd, 56), 18, ML_TEXT3);
    int mark_w = px(osd, 64), mark_h = px(osd, 26);
    int label_x = x + pad + mark_w + px(osd, 16);
    int detail_x = label_x + px(osd, 170);
    int detail_w = x + w - pad - detail_x;
    char fitted[200];
    for(int i = 0; i < ST_ROW_COUNT; i++) {
        const StLine *line = &st->lines[i];
        int row_y = y + head + i * row_h;
        SDL_Color fill = ST_GREY, ink = ML_TEXT;
        const char *word = line->word;
        switch(line->mark) {
            case ST_OK: fill = ST_GREEN; ink = ST_INK; break;
            case ST_WARN: fill = ST_AMBER; ink = ST_INK; break;
            case ST_FAIL: fill = ST_RED; ink = ST_INK; break;
            case ST_RUNNING: fill = ML_ACCENT; word = dots[(now / 300u) % 3u]; break;
            case ST_SKIP: ink = ML_TEXT2; break;
            default: fill = faded(ST_GREY, 0.45f); word = NULL; break;
        }
        int mark_y = row_y + (row_h - mark_h) / 2;
        osd_fill(osd, x + pad, mark_y, mark_w, mark_h, mark_h / 2, fill);
        if(word && word[0])
            text_centered(osd, word, x + pad + mark_w / 2, mark_y + (mark_h - osd_line_height(osd, mark_size)) / 2,
                          mark_size, ink);
        int text_y = row_y + (row_h - lh) / 2;
        bool pending = line->mark == ST_PENDING;
        osd_text(osd, st_labels[i], label_x, text_y, size, pending ? ML_TEXT3 : ML_TEXT2);
        if(line->detail[0] && detail_w > 0) {
            osd_fit(osd, line->detail, size, detail_w, fitted, sizeof(fitted));
            osd_text(osd, fitted, detail_x, text_y, size, pending ? ML_TEXT3 : ML_TEXT);
        }
    }
    int below = y + head + ST_ROW_COUNT * row_h + px(osd, 14);
    if(st->message[0]) st_wrap(osd, st->message, x + pad, below, message_size, w - 2 * pad, 2, ML_TEXT);
    int footer_y = below + message_h + px(osd, 12);
    if(st->prompt) {
        char footer[64];
        uint32_t left = (int32_t)(st->prompt_until - now) > 0 ? st->prompt_until - now : 0u;
        textf(footer, sizeof(footer), "Press A to finish (%u s)", (unsigned int)((left + 999u) / 1000u));
        osd_text(osd, footer, x + pad, footer_y, footer_size, ML_ACCENT);
    } else if(app->config->hints) {
        osd_text(osd, "Hold START + SELECT to stop", x + pad, footer_y, footer_size, ML_TEXT3);
    }
}

/* One frame of the self-test screen. video_drawn: the caller cleared and
 * copied the picture already (the decode benchmark times that part). */
static void st_draw(MlApp *app, const SelfTest *st, uint32_t now, bool video_drawn)
{
    if(!app->renderer) return;
    if(!video_drawn) {
        st_clear(app);
        if(app->texture && app->rendered_frames) SDL_RenderCopy(app->renderer, app->texture, NULL, &app->destination);
    }
    if(app->osd_ready) {
        st_draw_card(app, st, now);
        if(app->toast_until) {
            float alpha = fade_alpha(now, app->toast_until, 300);
            if(alpha > 0.0f) osd_draw_toast(&app->osd, app->toast, app->toast_kind, alpha);
            else app->toast_until = 0;
        }
        float quit = si_quit_progress(&app->input, now);
        if(quit > 0.0f) osd_draw_quit(&app->osd, quit);
    }
    SDL_RenderPresent(app->renderer);
}

/* Events: pads that come and go, and the Start + Select hold (which stops
 * the test). True when a face button or Start went down: the press the last
 * step waits for. */
static bool st_pump(MlApp *app, uint32_t now)
{
    bool pressed = false;
    SDL_Event event;
    while(SDL_PollEvent(&event)) {
        if(event.type == SDL_CONTROLLERBUTTONDOWN) {
            Uint8 button = event.cbutton.button;
            if(button == SDL_CONTROLLER_BUTTON_A || button == SDL_CONTROLLER_BUTTON_B || button == SDL_CONTROLLER_BUTTON_X ||
               button == SDL_CONTROLLER_BUTTON_Y || button == SDL_CONTROLLER_BUTTON_START)
                pressed = true;
        } else if(event.type == SDL_JOYBUTTONDOWN) {
            StreamPad *pad = pad_find(app, event.jbutton.which);
            SiInput input;
            if(pad && !pad->controller && joystick_input(event.jbutton.button, pad->xpad_layout, &input) &&
               (input == SI_IN_SOUTH || input == SI_IN_EAST || input == SI_IN_WEST || input == SI_IN_NORTH ||
                input == SI_IN_START))
                pressed = true;
        } else if(event.type == SDL_KEYDOWN) {
            if(event.key.keysym.sym == SDLK_RETURN || event.key.keysym.sym == SDLK_SPACE) pressed = true;
        }
        handle_event(app, &event, now);
    }
    si_tick(&app->input, now);
    SiEvent input_event;
    while(si_poll_event(&app->input, &input_event)) {
        if(input_event.type == SI_EV_QUIT) {
            fprintf(stdout, "[native] self-test stopped by START+SELECT\n");
            request_quit(app);
        }
        /* Hold Menu and the D-pad toggle mean nothing here. */
    }
    ss_motor_tick(&app->motor, now);
    return pressed;
}

/* The card for ms, events pumped; false when the test was stopped. */
static bool st_wait(MlApp *app, const SelfTest *st, uint32_t ms)
{
    uint32_t start = SDL_GetTicks();
    for(;;) {
        uint32_t now = SDL_GetTicks();
        st_pump(app, now);
        if(st_stopped(app)) return false;
        if(now - start >= ms) return true;
        st_draw(app, st, now, false);
        SDL_Delay(app->renderer ? 4 : 15);
    }
}

/* ---- the self-test steps ---- */

static void st_screen(MlApp *app, SelfTest *st, bool sdl_ok)
{
    if(!sdl_ok || !app->renderer) {
        const char *error = SDL_GetError();
        textf(st->video_error, sizeof(st->video_error), "%s", error && error[0] ? error : "no renderer");
        st_line(st, ST_ROW_SCREEN, ST_FAIL, "FAIL", "No screen: %s", st->video_error);
        fprintf(stderr, "[native] screen check failed: %s\n", st->video_error);
        return;
    }
    SDL_RendererInfo info;
    if(SDL_GetRendererInfo(app->renderer, &info) == 0) {
        textf(st->renderer, sizeof(st->renderer), "%s", info.name ? info.name : "unknown");
        st->accel = (info.flags & SDL_RENDERER_ACCELERATED) != 0;
        st->vsync = (info.flags & SDL_RENDERER_PRESENTVSYNC) != 0;
    }
    if(SDL_GetRendererOutputSize(app->renderer, &st->out_w, &st->out_h) != 0) st->out_w = st->out_h = 0;
    st->video = ensure_texture(app, ST_CLIP_W, ST_CLIP_H, AV_PIX_FMT_YUV420P);
    if(!st->video) {
        textf(st->video_error, sizeof(st->video_error), "no video texture: %s", SDL_GetError());
        st_line(st, ST_ROW_SCREEN, ST_FAIL, "FAIL", "%s - %s", st->renderer[0] ? st->renderer : "renderer",
                st->video_error);
        return;
    }
    st_line(st, ST_ROW_SCREEN, ST_OK, "OK", "%s, %s, %s, %dx%d", st->renderer[0] ? st->renderer : "unknown",
            st->accel ? "GPU" : "software", st->vsync ? "vsync" : "no vsync", st->out_w, st->out_h);
}

#ifdef ML_SELFTEST_CLIP
/* The embedded clip cut into frames by FFmpeg's H.264 parser, one access unit
 * each (SPS and PPS go with the IDR), as a stream's frames arrive. Returns
 * how many; 0 when it could not. */
static int st_split_clip(const AVCodec *decoder, StPacket *packets, int capacity)
{
    size_t clip_size = (size_t)ml_selftest_clip_size;
    if(!clip_size || clip_size > 64u * 1024u * 1024u) return 0;
    uint8_t *input = malloc(clip_size + AV_INPUT_BUFFER_PADDING_SIZE);
    AVCodecParserContext *parser = av_parser_init(AV_CODEC_ID_H264);
    AVCodecContext *context = avcodec_alloc_context3(decoder); /* the parser's own, never opened */
    int count = 0, keys = 0;
    bool failed = !input || !parser || !context;
    if(!failed) {
        memcpy(input, ml_selftest_clip_start, clip_size);
        memset(input + clip_size, 0, AV_INPUT_BUFFER_PADDING_SIZE); /* the parser reads past the end */
        const uint8_t *at = input;
        int left = (int)clip_size;
        /* The whole clip, then empty calls until the last frame is out. */
        for(int call = 0; call < 100000; call++) {
            uint8_t *out = NULL;
            int out_size = 0;
            bool flushing = left == 0; /* the call that eats the last bytes may still hold a frame */
            int used = av_parser_parse2(parser, context, &out, &out_size, flushing ? NULL : at, left, AV_NOPTS_VALUE,
                                        AV_NOPTS_VALUE, 0);
            if(used < 0 || used > left || (!flushing && !used && out_size <= 0)) {
                failed = true;
                break;
            }
            at += used;
            left -= used;
            if(out_size > 0 && count < capacity) {
                StPacket *packet = &packets[count];
                packet->data = malloc((size_t)out_size + AV_INPUT_BUFFER_PADDING_SIZE);
                if(!packet->data) {
                    failed = true;
                    break;
                }
                memcpy(packet->data, out, (size_t)out_size);
                memset(packet->data + out_size, 0, AV_INPUT_BUFFER_PADDING_SIZE);
                packet->size = out_size;
                packet->key = parser->key_frame == 1 || count == 0;
                if(packet->key) keys++;
                count++;
            }
            if(flushing && out_size <= 0) break;
        }
    }
    if(parser) av_parser_close(parser);
    avcodec_free_context(&context);
    free(input);
    if(failed) {
        for(int i = 0; i < count; i++) free(packets[i].data);
        memset(packets, 0, sizeof(*packets) * (size_t)capacity);
        return 0;
    }
    fprintf(stdout, "[native] test video: %zu bytes, %d frames (%d key frames)\n", clip_size, count, keys);
    fflush(stdout);
    return count;
}

/* The passes: each frame timed from send to the last receive; each decoded
 * frame then uploaded (timed) and shown under the card. */
static void st_benchmark(MlApp *app, SelfTest *st, AVCodecContext *context, const StPacket *packets, int count,
                         AVPacket *packet, AVFrame *frame, AVFrame *held)
{
    StTimes pass_times[2];
    memset(pass_times, 0, sizeof(pass_times));
    read_small_file(ST_CPU0_GOVERNOR, st->governor, sizeof(st->governor));
    int passes = 0;
    bool failed = false, upload_failed = false;
    char failure[AV_ERROR_MAX_STRING_SIZE + 48] = "";
    uint32_t started_at = SDL_GetTicks();
    while(!failed && !st_stopped(app) && passes < ST_MAX_PASSES &&
          (passes < 2 || SDL_GetTicks() - started_at < ST_BENCH_MS)) {
        StTimes *group = &pass_times[passes > 0 ? 1 : 0];
        int frame_index = 0;
        for(; frame_index < count; frame_index++) {
            uint32_t now = SDL_GetTicks();
            st_pump(app, now);
            if(st_stopped(app)) break;
            packet->data = packets[frame_index].data;
            packet->size = packets[frame_index].size;
            packet->flags = packets[frame_index].key ? AV_PKT_FLAG_KEY : 0;
            int got = 0;
            uint64_t decode_started = mono_us();
            int error = avcodec_send_packet(context, packet);
            if(error == AVERROR(EAGAIN)) {
                /* Not expected with low delay: frames still held are taken
                 * (the newest is kept for the screen), then the packet is sent again. */
                while(avcodec_receive_frame(context, frame) == 0) {
                    av_frame_unref(held);
                    av_frame_move_ref(held, frame);
                    got++;
                }
                error = avcodec_send_packet(context, packet);
            }
            while(error >= 0) {
                error = avcodec_receive_frame(context, frame);
                if(error < 0) break;
                av_frame_unref(held);
                av_frame_move_ref(held, frame);
                got++;
            }
            double decode_ms = (double)(mono_us() - decode_started) / 1000.0;
            if(error != AVERROR(EAGAIN) && error != AVERROR_EOF) {
                char text[AV_ERROR_MAX_STRING_SIZE] = {0};
                av_strerror(error, text, sizeof(text));
                textf(failure, sizeof(failure), "frame %d: %s", frame_index + 1, text);
                failed = true;
                av_frame_unref(held);
                break;
            }
            if(packets[frame_index].key) {
                group->idr_sum += decode_ms;
                group->idr_count++;
            } else {
                group->p_sum += decode_ms;
                group->p_count++;
                if(decode_ms > group->p_max) group->p_max = decode_ms;
            }
            st->frames += (unsigned int)got;
            st_sample_cpu(st);
            bool drawn = false;
            if(got && st->video) {
                st_clear(app);
                pthread_mutex_lock(&app->frame_lock);
                av_frame_unref(app->slot);
                av_frame_move_ref(app->slot, held);
                app->slot_ready = true;
                pthread_mutex_unlock(&app->frame_lock);
                bool new_frame = false;
                uint64_t upload_started = mono_us();
                bool shown = update_video(app, &new_frame);
                if(shown) SDL_RenderCopy(app->renderer, app->texture, NULL, &app->destination);
                double upload_ms = (double)(mono_us() - upload_started) / 1000.0;
                if(shown) {
                    group->upload_sum += upload_ms;
                    group->upload_count++;
                    drawn = true;
                } else {
                    upload_failed = true;
                    st->video = false;
                    textf(st->video_error, sizeof(st->video_error), "the picture could not be shown: %s", SDL_GetError());
                    st_line(st, ST_ROW_SCREEN, ST_FAIL, "FAIL", "Showing the picture failed: %s", SDL_GetError());
                }
            } else {
                av_frame_unref(held);
            }
            st_line(st, ST_ROW_DECODE, ST_RUNNING, NULL, "Pass %d of up to %d - %u frames", passes + 1, ST_MAX_PASSES,
                    st->frames);
            st_draw(app, st, now, drawn);
        }
        if(failed || frame_index < count) break; /* a failure, or the test was stopped */
        passes++;
        if(app->config->verbose) {
            fprintf(stdout, "[native] decode pass %d: %u frames so far, cpu0 %ld MHz\n", passes, st->frames, st->cpu_mhz);
            fflush(stdout);
        }
    }
    /* The first pass warms the decoder up (buffers, threads): the means come
     * from the passes after it when there are any. */
    const StTimes *first = &pass_times[0], *later = &pass_times[1];
    if(later->idr_count) st->idr_ms = later->idr_sum / later->idr_count;
    else if(first->idr_count) st->idr_ms = first->idr_sum / first->idr_count;
    if(later->p_count) {
        st->p_ms = later->p_sum / later->p_count;
        st->p_max_ms = later->p_max;
    } else if(first->p_count) {
        st->p_ms = first->p_sum / first->p_count;
        st->p_max_ms = first->p_max;
    }
    if(later->upload_count) st->upload_ms = later->upload_sum / later->upload_count;
    else if(first->upload_count) st->upload_ms = first->upload_sum / first->upload_count;
    st->decode = !failed && passes >= 1;
    st->decode_error = failed;
    fprintf(stdout, "[native] decode benchmark: passes=%d frames=%u idr_ms=%.1f p_ms=%.1f p_max_ms=%.1f upload_ms=%.1f "
            "cpu_mhz=%ld governor=%s%s%s\n", passes, st->frames, st->idr_ms, st->p_ms, st->p_max_ms, st->upload_ms,
            st->cpu_mhz, st->governor[0] ? st->governor : "-", failed ? " failed: " : "", failure);
    fflush(stdout);

    if(failed) {
        st_line(st, ST_ROW_DECODE, ST_FAIL, "FAIL", "The test video did not decode (%s)", failure);
    } else if(!st->decode) {
        st_line(st, ST_ROW_DECODE, ST_SKIP, "--", "Stopped before the first pass ended");
    } else {
        char cpu[40] = "";
        if(st->cpu_mhz > 0) textf(cpu, sizeof(cpu), " - CPU %ld MHz", st->cpu_mhz);
        bool fast = st_frame_ms(st) <= ST_FRAME_BUDGET_MS;
        st_line(st, ST_ROW_DECODE, fast ? ST_OK : ST_WARN, fast ? "OK" : "SLOW",
                "%.1f ms a frame (max %.1f), key frame %.1f%s", st->p_ms, st->p_max_ms, st->idr_ms, cpu);
    }
    if(upload_failed)
        st_line(st, ST_ROW_UPLOAD, ST_FAIL, "FAIL", "The picture could not be shown");
    else if(!app->renderer || !st->video)
        st_line(st, ST_ROW_UPLOAD, ST_SKIP, "--", "Not checked: no screen");
    else if(first->upload_count || later->upload_count)
        st_line(st, ST_ROW_UPLOAD, ST_OK, "OK", "%.1f ms a frame (to the GPU and drawn)", st->upload_ms);
    else
        st_line(st, ST_ROW_UPLOAD, ST_SKIP, "--", "Not checked: nothing decoded");
}
#endif

static void st_decode(MlApp *app, SelfTest *st)
{
    if(st_stopped(app)) {
        st_line(st, ST_ROW_DECODE, ST_SKIP, "--", "Not run: the test was stopped");
        st_line(st, ST_ROW_UPLOAD, ST_SKIP, "--", "Not run: the test was stopped");
        return;
    }
#ifndef ML_SELFTEST_CLIP
    st->clip = false;
    st_line(st, ST_ROW_DECODE, ST_FAIL, "--", "Not checked: this build has no test video");
    st_line(st, ST_ROW_UPLOAD, ST_SKIP, "--", "Not checked: this build has no test video");
    read_small_file(ST_CPU0_GOVERNOR, st->governor, sizeof(st->governor));
    st_sample_cpu(st);
    fprintf(stderr, "[native] decode benchmark skipped: built without the test video (ML_SELFTEST_CLIP, "
            "moonlight-selftest.h264)\n");
#else
    st->clip = true;
    st_line(st, ST_ROW_DECODE, ST_RUNNING, NULL, "Getting the test video ready");
    st_draw(app, st, SDL_GetTicks(), false);
    StPacket packets[ST_MAX_PACKETS];
    memset(packets, 0, sizeof(packets));
    const AVCodec *decoder = avcodec_find_decoder(AV_CODEC_ID_H264);
    int count = decoder ? st_split_clip(decoder, packets, ST_MAX_PACKETS) : 0;
    AVCodecContext *context = count > 0 ? open_video_decoder(decoder, ST_CLIP_W, ST_CLIP_H) : NULL;
    AVPacket *packet = av_packet_alloc();
    AVFrame *frame = av_frame_alloc();
    AVFrame *held = av_frame_alloc();
    if(!app->slot) app->slot = av_frame_alloc();
    if(!app->render_frame) app->render_frame = av_frame_alloc();
    if(context && packet && frame && held && app->slot && app->render_frame) {
        fprintf(stdout, "[native] decode benchmark: H.264 %dx%d, %s, up to %d passes / %u ms\n", ST_CLIP_W, ST_CLIP_H,
                "4 slice threads, low delay", ST_MAX_PASSES, ST_BENCH_MS);
        fflush(stdout);
        st_benchmark(app, st, context, packets, count, packet, frame, held);
    } else {
        st->decode_error = true;
        st_line(st, ST_ROW_DECODE, ST_FAIL, "FAIL", "%s",
                !decoder ? "This build has no H.264 decoder"
                : count <= 0 ? "The test video could not be cut into frames"
                : "The H.264 decoder could not start");
        st_line(st, ST_ROW_UPLOAD, ST_SKIP, "--", "Not checked: nothing decoded");
        fprintf(stderr, "[native] decode benchmark: %s\n", st->lines[ST_ROW_DECODE].detail);
    }
    if(context) avcodec_free_context(&context);
    if(packet) av_packet_free(&packet);
    if(frame) av_frame_free(&frame);
    if(held) av_frame_free(&held);
    for(int i = 0; i < count; i++) free(packets[i].data);
#endif
}

/* Two notes, E5 then B5, each a sine with a soft attack, a gentle decay and
 * a fade to zero at its end; interleaved stereo at the session volume. */
static int16_t *st_chime(unsigned int volume, size_t *frames)
{
    static const struct {
        double hz, start, length;
    } notes[2] = {{659.25, 0.0, 0.30}, {987.77, 0.16, 0.39}};
    const double two_pi = 6.283185307179586;
    size_t count = (size_t)ST_CHIME_FRAMES;
    int16_t *pcm = calloc(count * 2u, sizeof(*pcm));
    if(!pcm) return NULL;
    double gain = 0.30 * 32767.0 * (double)volume / 100.0;
    for(size_t i = 0; i < count; i++) {
        double t = (double)i / (double)ST_CHIME_RATE;
        double value = 0.0;
        for(size_t n = 0; n < sizeof(notes) / sizeof(notes[0]); n++) {
            double local = t - notes[n].start;
            double left = notes[n].length - local;
            if(local < 0.0 || left <= 0.0) continue;
            double envelope = exp(-4.0 * local);
            if(local < 0.008) envelope *= local / 0.008;
            if(left < 0.05) envelope *= left / 0.05;
            value += 0.6 * envelope * sin(two_pi * notes[n].hz * local);
        }
        double scaled = value * gain;
        if(scaled > 32767.0) scaled = 32767.0;
        if(scaled < -32768.0) scaled = -32768.0;
        int16_t sample = (int16_t)lrint(scaled);
        pcm[2 * i] = sample;
        pcm[2 * i + 1] = sample;
    }
    *frames = count;
    return pcm;
}

static void st_sound(MlApp *app, SelfTest *st, const MlConfig *config)
{
    if(st_stopped(app)) {
        st_line(st, ST_ROW_SOUND, ST_SKIP, "--", "Not run: the test was stopped");
        return;
    }
    st_line(st, ST_ROW_SOUND, ST_RUNNING, NULL, "Playing a chime");
    st_draw(app, st, SDL_GetTicks(), false);
    if(!SDL_WasInit(SDL_INIT_AUDIO)) {
        st_line(st, ST_ROW_SOUND, ST_FAIL, "FAIL", "No sound system: %s", SDL_GetError());
        fprintf(stderr, "[native] chime: no SDL audio: %s\n", SDL_GetError());
        return;
    }
    /* The stream's spec (ar_init): 48 kHz, S16, stereo, 1024 samples. */
    SDL_AudioSpec wanted;
    SDL_zero(wanted);
    wanted.freq = ST_CHIME_RATE;
    wanted.format = AUDIO_S16SYS;
    wanted.channels = 2;
    wanted.samples = 1024;
    SDL_AudioDeviceID device = SDL_OpenAudioDevice(NULL, 0, &wanted, NULL, 0);
    if(!device) {
        st_line(st, ST_ROW_SOUND, ST_FAIL, "FAIL", "No sound device: %s", SDL_GetError());
        fprintf(stderr, "[native] chime: SDL audio open failed: %s\n", SDL_GetError());
        return;
    }
    size_t frames = 0;
    int16_t *pcm = st_chime(config->volume, &frames);
    bool queued = pcm && SDL_QueueAudio(device, pcm, (Uint32)(frames * 2u * sizeof(int16_t))) == 0;
    free(pcm);
    SDL_PauseAudioDevice(device, 0);
    bool drained = false;
    uint32_t started_at = SDL_GetTicks();
    while(queued) {
        uint32_t now = SDL_GetTicks();
        st_pump(app, now);
        if(st_stopped(app)) break;
        if(SDL_GetQueuedAudioSize(device) == 0) {
            drained = true;
            break;
        }
        if(now - started_at >= ST_DRAIN_MS) break;
        st_draw(app, st, now, false);
        SDL_Delay(5);
    }
    if(drained) st_wait(app, st, ST_TAIL_MS); /* the device's own buffer plays out */
    SDL_CloseAudioDevice(device);
    st->audio = queued && drained;
    fprintf(stdout, "[native] chime: queued=%d played=%d volume=%u\n", queued, drained, config->volume);
    fflush(stdout);
    if(st->audio && config->volume)
        st_line(st, ST_ROW_SOUND, ST_OK, "OK", "Chime played at %u%% volume", config->volume);
    else if(st->audio)
        st_line(st, ST_ROW_SOUND, ST_OK, "OK", "Chime played silently (volume %u%%)", config->volume);
    else if(!queued)
        st_line(st, ST_ROW_SOUND, ST_FAIL, "FAIL", "The chime could not be queued: %s", SDL_GetError());
    else if(st_stopped(app))
        st_line(st, ST_ROW_SOUND, ST_SKIP, "--", "Stopped while the chime played");
    else
        st_line(st, ST_ROW_SOUND, ST_FAIL, "FAIL", "The sound device did not play it within %u ms", ST_DRAIN_MS);
}

static void st_opus_check(MlApp *app, SelfTest *st)
{
    if(st_stopped(app)) {
        st_line(st, ST_ROW_OPUS, ST_SKIP, "--", "Not run: the test was stopped");
        return;
    }
    /* The stream's stereo layout: one coupled stream, channels 0 and 1. */
    static const unsigned char mapping[2] = {0, 1};
    int error = 0;
    OpusMSDecoder *decoder = opus_multistream_decoder_create(48000, 2, 1, 1, mapping, &error);
    st->opus = decoder != NULL;
    if(decoder) opus_multistream_decoder_destroy(decoder);
    if(st->opus) st_line(st, ST_ROW_OPUS, ST_OK, "OK", "Stereo decoder ready (48 kHz)");
    else st_line(st, ST_ROW_OPUS, ST_FAIL, "FAIL", "Decoder failed: %s", opus_strerror(error));
}

static void st_core_check(MlApp *app, SelfTest *st)
{
    if(st_stopped(app)) {
        st_line(st, ST_ROW_CORE, ST_SKIP, "--", "Not run: the test was stopped");
        return;
    }
    const char *name = LiGetStageName(STAGE_RTSP_HANDSHAKE);
    st->core = name != NULL;
    if(st->core) st_line(st, ST_ROW_CORE, ST_OK, "OK", "moonlight-common-c ready (\"%s\")", name);
    else st_line(st, ST_ROW_CORE, ST_FAIL, "FAIL", "moonlight-common-c did not answer");
}

/* The pads open right now (st_pads, st_builtin). */
static void st_count_pads(MlApp *app, SelfTest *st)
{
    st->pads = 0;
    st->builtin = false;
    for(int i = 0; i < MAX_PADS; i++) {
        if(!app->pads[i].used) continue;
        st->pads++;
        if(app->pads[i].builtin) st->builtin = true;
    }
}

static void st_pads_line(MlApp *app, SelfTest *st)
{
    st_count_pads(app, st);
    const char *motor = st->motor ? "motor buzzed" : app->motor.available ? "motor did not start" : "no rumble motor";
    if(st->pads > 0)
        st_line(st, ST_ROW_PADS, ST_OK, "OK", "%d connected%s - %s", st->pads,
                st->builtin ? ", built-in pad" : ", no built-in pad", motor);
    else
        st_line(st, ST_ROW_PADS, ST_WARN, "NONE", "None found - %s", motor);
}

static void st_verdict(MlApp *app, SelfTest *st);

static void st_controls(MlApp *app, SelfTest *st)
{
    if(st_stopped(app)) {
        st_line(st, ST_ROW_PADS, ST_SKIP, "--", "Not run: the test was stopped");
        st_line(st, ST_ROW_PRESS, ST_SKIP, "--", "Not run: the test was stopped");
        return;
    }
    st_line(st, ST_ROW_PADS, ST_RUNNING, NULL, "Looking for controllers and the rumble motor");
    if(app->motor.available) {
        ss_motor_set(&app->motor, 1.0f, SDL_GetTicks());
        st->motor = app->motor.on;
        st_wait(app, st, ST_BUZZ_MS);
        ss_motor_off(&app->motor);
    }
    st_pads_line(app, st);
    fprintf(stdout, "[native] controllers: %d open, built-in=%d, motor=%s\n", st->pads, st->builtin,
            st->motor ? app->motor.gpio : app->motor.available ? "did not start" : "none");
    fflush(stdout);
    if(st_stopped(app)) {
        st_line(st, ST_ROW_PRESS, ST_SKIP, "--", "Not run: the test was stopped");
        return;
    }
    if(!app->osd_ready) {
        st_line(st, ST_ROW_PRESS, ST_SKIP, "--", "Not checked: nothing to show the prompt on");
        return;
    }
    st_verdict(app, st);
    st->prompt = true;
    st->prompt_until = SDL_GetTicks() + ST_PRESS_MS;
    st_line(st, ST_ROW_PRESS, ST_RUNNING, NULL, "Press A to finish");
    for(;;) {
        uint32_t now = SDL_GetTicks();
        bool pressed = st_pump(app, now);
        if(st_stopped(app)) break;
        if(pressed) {
            st->input = true;
            break;
        }
        if((int32_t)(now - st->prompt_until) >= 0) break;
        st_draw(app, st, now, false);
        SDL_Delay(4);
    }
    st->prompt = false;
    st_pads_line(app, st); /* the pads open at the end */
    if(st->input) st_line(st, ST_ROW_PRESS, ST_OK, "OK", "A button press arrived");
    else if(st_stopped(app)) st_line(st, ST_ROW_PRESS, ST_SKIP, "--", "Stopped");
    else st_line(st, ST_ROW_PRESS, ST_WARN, "--", "No button press in %u s", ST_PRESS_MS / 1000u);
}

/* The one-sentence verdict (the status file's message) and the exit code. */
static void st_verdict(MlApp *app, SelfTest *st)
{
    st->stopped = st_stopped(app);
    const char *items[5];
    int count = 0;
    if(st->lines[ST_ROW_SOUND].mark == ST_FAIL) items[count++] = "no sound";
    if(st->lines[ST_ROW_OPUS].mark == ST_FAIL) items[count++] = "no Opus decoder";
    if(st->lines[ST_ROW_CORE].mark == ST_FAIL) items[count++] = "moonlight-common-c did not answer";
    if(st->lines[ST_ROW_PADS].mark == ST_WARN) items[count++] = "no controller found";
    if(st->stopped) items[count++] = "the test was stopped early";
    char problems[200] = "";
    for(int i = 0; i < count; i++) {
        size_t used = strlen(problems);
        snprintf(problems + used, sizeof(problems) - used, "%s%s", i ? ", " : "", items[i]);
    }
    if(!st->video) {
        st->rc = 10;
        textf(st->message, sizeof(st->message), "The screen could not show video (%s), so streaming will not work.",
              st->video_error[0] ? st->video_error : "unknown error");
    } else if(!st->clip) {
        st->rc = 6;
        textf(st->message, sizeof(st->message), "Video decoding was not checked: this build has no test video.");
    } else if(st->decode_error) {
        st->rc = 6;
        textf(st->message, sizeof(st->message), "The test video did not decode, so streaming will not work.");
    } else if(!st->decode) {
        st->rc = 0;
        textf(st->message, sizeof(st->message), "The self-test was stopped before video decoding finished.");
    } else {
        st->rc = 0;
        double frame_ms = st_frame_ms(st);
        const char *verdict;
        if(frame_ms <= ST_FRAME_BUDGET_MS) verdict = "Ready to stream: decoding keeps up with 60 fps at full resolution";
        else if(frame_ms * 0.5625 <= ST_FRAME_BUDGET_MS) verdict = "Ready to stream: 60 fps needs the Light preset here";
        else verdict = "Ready to stream: decoding is slow here, so Steady 30 is the best choice";
        textf(st->message, sizeof(st->message), "%s%s%s.", verdict, problems[0] ? "; " : "", problems);
    }
}

/* The contract's keys (section 5b), in its order, atomically with mode 0600. */
static bool write_self_test_status(const char *path, const SelfTest *st)
{
    char rc[16], out[32], frames[16], idr[32], p[32], p_max[32], upload[32], pads[16], mhz[24];
    snprintf(rc, sizeof(rc), "%d", st->rc);
    snprintf(out, sizeof(out), "%dx%d", st->out_w, st->out_h);
    snprintf(frames, sizeof(frames), "%u", st->frames);
    snprintf(idr, sizeof(idr), "%.2f", st->idr_ms);
    snprintf(p, sizeof(p), "%.2f", st->p_ms);
    snprintf(p_max, sizeof(p_max), "%.2f", st->p_max_ms);
    snprintf(upload, sizeof(upload), "%.2f", st->upload_ms);
    snprintf(pads, sizeof(pads), "%d", st->pads);
    snprintf(mhz, sizeof(mhz), "%ld", st->cpu_mhz);
    const SsPair pairs[] = {
        {"version", "1"},
        {"rc", rc},
        {"result", "self_test"},
        {"message", st->message},
        {"st_video", st_bit(st->video)},
        {"st_renderer", st->renderer},
        {"st_accel", st_bit(st->accel)},
        {"st_vsync", st_bit(st->vsync)},
        {"st_out", out},
        {"st_decode", st_bit(st->decode)},
        {"st_frames", frames},
        {"st_idr_ms", idr},
        {"st_p_ms", p},
        {"st_p_max_ms", p_max},
        {"st_upload_ms", upload},
        {"st_audio", st_bit(st->audio)},
        {"st_opus", st_bit(st->opus)},
        {"st_core", st_bit(st->core)},
        {"st_pads", pads},
        {"st_builtin", st_bit(st->builtin)},
        {"st_input", st_bit(st->input)},
        {"st_motor", st_bit(st->motor)},
        {"st_governor", st->governor},
        {"st_cpu_mhz", mhz},
    };
    return ss_write_pairs(path, pairs, sizeof(pairs) / sizeof(pairs[0]));
}

static int run_self_test(MlApp *app, const MlConfig *config)
{
    static SelfTest test;
    SelfTest *st = &test;
    memset(st, 0, sizeof(*st));
    fprintf(stdout, "[native] self-test starting: volume=%u hints=%d\n", config->volume, config->hints);
    fflush(stdout);
    bool sdl_ok = init_sdl(app);
    app->start_ticks = SDL_GetTicks();
    st_screen(app, st, sdl_ok);
    st_draw(app, st, SDL_GetTicks(), false);
    if(app->motor.available) fprintf(stdout, "[native] rumble motor: %s\n", app->motor.gpio);
    st_decode(app, st);
    st_sound(app, st, config);
    st_opus_check(app, st);
    st_core_check(app, st);
    st_draw(app, st, SDL_GetTicks(), false);
    st_controls(app, st);
    st_count_pads(app, st); /* the pads open at the end, even when the test stopped first */
    st_verdict(app, st);
    fprintf(stdout, "[native] self-test: rc=%d video=%d renderer=%s accel=%d vsync=%d out=%dx%d decode=%d frames=%u "
            "idr_ms=%.1f p_ms=%.1f p_max_ms=%.1f upload_ms=%.1f audio=%d opus=%d core=%d pads=%d builtin=%d input=%d "
            "motor=%d governor=%s cpu_mhz=%ld - %s\n",
            st->rc, st->video, st->renderer[0] ? st->renderer : "-", st->accel, st->vsync, st->out_w, st->out_h,
            st->decode, st->frames, st->idr_ms, st->p_ms, st->p_max_ms, st->upload_ms, st->audio, st->opus, st->core,
            st->pads, st->builtin, st->input, st->motor, st->governor[0] ? st->governor : "-", st->cpu_mhz, st->message);
    fflush(stdout);
    if(config->status_file[0] && !write_self_test_status(config->status_file, st))
        fprintf(stderr, "[native] could not write the status file %s: %s\n", config->status_file, strerror(errno));
    if(!st->stopped && app->renderer) st_wait(app, st, ST_RESULT_MS); /* the result stays up a moment */
    return st->rc;
}

int main(int argc, char **argv)
{
    setvbuf(stdout, NULL, _IOFBF, 64 * 1024);
    if(argc != 2) {
        fprintf(stderr, "usage: moonlight-stream SESSION_FILE\n");
        return 2;
    }
    static MlConfig config;
    char load_error[200] = {0};
    if(!load_config(argv[1], &config, load_error, sizeof(load_error))) {
        fprintf(stderr, "[native] %s\n", load_error);
        early_status(&config, 3, "invalid_session", "The stream details from JukaHub were incomplete. Start the stream again.");
        return 3;
    }

    MlApp *app = &g_app;
    memset(app, 0, sizeof(*app));
    signalled = 0;
    app->config = &config;
    atomic_init(&app->running, true);
    atomic_init(&app->volume, config.volume);
    atomic_init(&app->start_done, false);
    atomic_init(&app->start_rc, 0);
    atomic_init(&app->stage, 0);
    atomic_init(&app->stage_failed, false);
    atomic_init(&app->failed_stage, 0);
    atomic_init(&app->failed_error, 0);
    atomic_init(&app->connected, false);
    atomic_init(&app->terminated, false);
    atomic_init(&app->terminate_code, 0);
    atomic_init(&app->conn_status, 0);
    atomic_init(&app->conn_status_dirty, false);
    atomic_init(&app->rumble_low, 0);
    atomic_init(&app->rumble_high, 0);
    atomic_init(&app->rumble_dirty, false);
    atomic_init(&app->log_lines, 0);
    atomic_init(&app->video_format, 0);
    atomic_init(&app->decoder_failed, false);
    atomic_init(&app->decoded_frames, 0);
    atomic_init(&app->decode_us, 0);
    atomic_init(&app->dropped_frames, 0);
    atomic_init(&app->decode_errors, 0);
    atomic_init(&app->errors_in_row, 0);
    atomic_init(&app->audio_dropped, 0);
    atomic_init(&app->audio_failed, false);
    pthread_mutex_init(&app->frame_lock, NULL);
    app->shown_stage = -1;
    app->rumble_enabled = config.rumble;
    app->battery_warned = 101;
    si_init(&app->input, SI_SELECT_CREATE, config.dpad_stick, 1919, 1079);
    ss_motor_open(&app->motor, "", config.motor_gpio, config.motor_level, config.motor_level_min, config.motor_level_max,
                  config.motor_range_set);
    ss_motor_crash_guard(&app->motor);
    set_status(app, "Starting");
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
    signal(SIGPIPE, SIG_IGN);
#endif

    if(config.self_test) {
        int rc = run_self_test(app, &config);
        stop_rumble(app);
        cleanup(app);
        signal_app = NULL;
        return rc;
    }

    if(!init_sdl(app)) {
        char text[200];
        textf(text, sizeof(text), "The screen or sound could not start: %s", SDL_GetError());
        set_outcome(app, 5, "sdl_failed", text);
        ss_wipe(config.ri_key, sizeof(config.ri_key));
        finish(app, &config);
        cleanup(app);
        return 5;
    }
    app->start_ticks = SDL_GetTicks();
    app->stats_at = app->start_ticks;
    app->stats_log_at = app->start_ticks;
    present(app, app->start_ticks);
    if(app->motor.available) fprintf(stdout, "[native] rumble motor: %s\n", app->motor.gpio);

    int formats = video_formats(&config);
    app->slot = av_frame_alloc();
    app->render_frame = av_frame_alloc();
    if(!formats || !app->slot || !app->render_frame) {
        char text[200];
        textf(text, sizeof(text), "The video decoder could not start. Choose H.264 in JukaHub's Moonlight settings for %s.",
              config.name);
        ss_wipe(config.ri_key, sizeof(config.ri_key));
        fail_with_card(app, 6, "decoder_failed", "Video problem", text);
        finish(app, &config);
        run_error_card(app);
        cleanup(app);
        return 6;
    }

    configure_stream(app, &config, formats);
    ss_wipe(config.ri_key, sizeof(config.ri_key));
    fprintf(stdout, "[native] starting stream host=%s app=\"%s\" remote=%d mode=%ux%u@%u %ukbps codec=%s formats=0x%x "
            "appversion=%s gfe=%s rtsp=%s codec_support=0x%x\n",
            config.host, config.app_name, config.remote, config.width, config.height, config.fps, config.bitrate,
            requested_codec_name(&config), (unsigned)formats, config.app_version,
            config.gfe_version[0] ? config.gfe_version : "-", config.rtsp_url[0] ? config.rtsp_url : "-",
            (unsigned)config.codec_support);
    fprintf(stdout, "[native] mapping: Xbox pad, select=back dpad_stick=%d rumble=%d\n", config.dpad_stick, config.rumble);
    fflush(stdout);
    int thread_error = pthread_create(&app->start_thread, NULL, connect_thread, app);
    if(thread_error != 0) {
        fprintf(stderr, "[native] cannot start the connect thread: %s\n", strerror(thread_error));
        ss_wipe(app->stream.remoteInputAesKey, sizeof(app->stream.remoteInputAesKey));
        fail_with_card(app, 4, "init_failed", "Moonlight couldn't start", "The stream could not start a thread of its own.");
        finish(app, &config);
        run_error_card(app);
        cleanup(app);
        return 4;
    }
    app->start_thread_live = true;
    battery_tick(app, SDL_GetTicks());

    SDL_Event event;
    while(atomic_load(&app->running)) {
        uint32_t now = SDL_GetTicks();
        while(SDL_PollEvent(&event)) handle_event(app, &event, now);
        si_tick(&app->input, now);
        handle_input_events(app, now);
        handle_connection(app, now);
        if(!atomic_load(&app->running)) break;
        renew_rumble(app, now);
        push_input(app, now);
        battery_tick(app, now);
        ss_motor_tick(&app->motor, now);
        bool new_frame = false;
        if(!update_video(app, &new_frame)) {
            fail_with_card(app, 10, "video_failed", "Video problem",
                           "The video could not be shown. Try H.264 or a lower quality in JukaHub's Moonlight settings.");
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
        update_stats(app, now);
        if(new_frame || app->osd_dirty || (animating(app, now) && now - app->last_present >= 33u)) present(app, now);
        SDL_Delay(2);
    }

    fprintf(stdout, "[native] stopping; connected=%d rendered=%llu decoded=%llu dropped=%llu errors=%llu audio_dropped=%llu\n",
            atomic_load(&app->connected), (unsigned long long)app->rendered_frames,
            (unsigned long long)atomic_load(&app->decoded_frames), (unsigned long long)atomic_load(&app->dropped_frames),
            (unsigned long long)atomic_load(&app->decode_errors), (unsigned long long)atomic_load(&app->audio_dropped));
    fflush(stdout);
    /* Rumble off and the PC let go (keys released, an idle pad, the connection
     * stopped) before anything is reported or shown. */
    stop_rumble(app);
    stop_connection(app);
    finish(app, &config);
    if(app->show_error) run_error_card(app);
    int rc = app->rc;
    if(app->start_abandoned) {
        /* LiStartConnection never returned: moonlight-common-c may still run a
         * stage, its decoder and sound threads included. Freeing the frame slot
         * or quitting SDL under them would crash and lose rc; the status file is
         * written (finish), so the process just ends. */
        fprintf(stdout, "[native] leaving without cleanup (the connect stage is still running)\n");
        ss_motor_off(&app->motor);
        fflush(NULL);
        _exit(rc);
    }
    cleanup(app);
    signal_app = NULL;
    return rc;
}
