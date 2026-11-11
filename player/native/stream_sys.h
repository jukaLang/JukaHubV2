/* stream_sys.h - chiaki-stream's files: the session file JukaHub writes, the
 * status file JukaHub reads back, the battery and the TrimUI rumble motor.
 * No SDL and no chiaki here, so all of it is testable on a host
 * (tests/test_stream_sys.c); every sysfs path takes a root for that. */
#ifndef JUKAHUB_STREAM_SYS_H
#define JUKAHUB_STREAM_SYS_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define SS_REGIST_KEY_SIZE 16 /* CHIAKI_SESSION_AUTH_SIZE */
#define SS_MORNING_SIZE 16

typedef enum {
    SS_CODEC_H264 = 0,
    SS_CODEC_H265 = 1
} SsCodec;

typedef struct {
    /* Keys every JukaHub build writes. */
    char host[256];
    char regist_key[SS_REGIST_KEY_SIZE];
    uint8_t morning[SS_MORNING_SIZE];
    bool ps5;
    bool verbose;
    unsigned int target;
    unsigned int width;
    unsigned int height;
    unsigned int fps;
    unsigned int bitrate;
    unsigned int volume;

    /* Optional keys (docs: player/native/README.md). Absent = today's behaviour. */
    char name[96];          /* printable ASCII; "PlayStation" when absent */
    SsCodec codec;          /* resolved: a PS4 is always H.264 */
    char login_pin[5];      /* "" or exactly 4 digits */
    int select_create;      /* 0 Select = touchpad, 1 Select = Create */
    bool dpad_stick;
    bool rumble;
    bool hints;
    bool remote;
    char motor_gpio[256];
    char motor_level[256];
    long motor_level_min;
    long motor_level_max;
    bool motor_range_set;
    char status_file[512];
} SsConfig;

/* Longest session-file line; a longer one is skipped whole (never split, so its
 * tail cannot read as another key). The file itself may hold SS_SESSION_MAX
 * bytes; a bigger one is refused. */
#define SS_LINE_MAX 767
#define SS_SESSION_MAX 16384

/* Reads and deletes the session file. Returns false (with a reason in error)
 * for a file the helper cannot stream with; secrets are wiped on the way. */
bool ss_load_config(const char *path, SsConfig *config, char *error, size_t error_size);
/* Zeroes memory holding a secret in a way the compiler cannot leave out. */
void ss_wipe(void *p, size_t n);
/* Standard Base64 (with padding). Returns false unless it decodes cleanly. */
bool ss_base64_decode(const char *text, uint8_t *out, size_t *out_size);
/* The name as the display can draw it: printable ASCII, a UTF-8 sequence -> '?'. */
void ss_ascii_name(const char *in, char *out, size_t out_size);

typedef struct {
    int rc;
    const char *result;   /* spec token: ok, cancelled, connect_failed, ... */
    const char *message;  /* one sentence for the user */
    bool connected;
    unsigned long seconds;
    unsigned long long frames;
    const char *quit_reason;
    const char *host;
    const char *codec;    /* "h264" / "h265" */
    bool saved_pin_rejected; /* the console refused the login_pin JukaHub saved */
} SsStatus;

/* Atomically writes the status file (tmp + rename, mode 0600). */
bool ss_write_status(const char *path, const SsStatus *status);

typedef struct {
    bool valid;
    int percent;
    bool on_power; /* charging, or a charger is online */
} SsBattery;

/* root is "" (or NULL) on the device, a fake sysfs tree in tests. */
bool ss_battery_read(const char *root, SsBattery *battery);

/* The handheld's own pad, "TRIMUI Player1", is a uinput device with the USB id
 * of a wired Xbox 360 pad, so SDL names it "Xbox 360 Controller" like a real
 * one. ss_builtin_pad says whether a pad SDL opened is that pad: SDL's name
 * says TRIMUI, or the pad has that id and the kernel's name for its event node
 * (devnode: SDL_JoystickPath's "/dev/input/eventN", NULL when the linked SDL
 * cannot tell) says TRIMUI. Without a node, a pad with that id counts while a
 * TRIMUI input device with that id exists: a real Xbox 360 pad on the handheld
 * is rare, and taking the handheld's pad for an external one would cost it its
 * motor rumble and its D-pad mode. root as for ss_battery_read. */
#define SS_BUILTIN_PAD_VENDOR 0x045eu
#define SS_BUILTIN_PAD_PRODUCT 0x028eu
bool ss_builtin_pad(const char *root, const char *name, unsigned vendor, unsigned product, const char *devnode);

typedef struct {
    bool available;
    char gpio[512];
    char level[512];
    long level_min;
    long level_max;
    bool level_blocked;
    bool on;
    long last_level;
    uint32_t last_event;
} SsMotor;

#define SS_MOTOR_SAFETY_MS 10000u

/* Uses the given nodes, else probes the TG5040 (gpio227 + motor/voltage) and
 * TG5050 (gpio236 + motor/level) layouts under root, exporting the GPIO only
 * when the vendor strength node exists. A motor that is found is switched off
 * first: an earlier run that died mid-rumble may have left it running. */
void ss_motor_open(SsMotor *motor, const char *root, const char *gpio, const char *level,
                   long level_min, long level_max, bool range_set);
/* The last resort for a crash (SIGSEGV, SIGBUS, SIGABRT, SIGFPE, SIGILL): the
 * handler only writes "0" to the motor's GPIO (open/write/close, all
 * async-signal-safe) and lets the signal kill the process as before. */
void ss_motor_crash_guard(const SsMotor *motor);
/* strength 0..1; under 5% switches the motor off. */
void ss_motor_set(SsMotor *motor, float strength, uint32_t now);
/* Switches a motor off that has had no rumble event for SS_MOTOR_SAFETY_MS. */
void ss_motor_tick(SsMotor *motor, uint32_t now);
void ss_motor_off(SsMotor *motor);

#endif
