/* evdev.c — the device-side input producer for AndroidRT.
 *
 * The Smart Pro's controller shows up as /dev/input/event*: plain evdev,
 * exactly the layer the Linux kernel hands to Android's EventHub. Android
 * itself then classifies each device (gamepad vs keyboard vs touchscreen)
 * and maps Linux codes onto Android keycodes and AMotionEvent axes. This
 * file is that layer for Tier 1, minus everything the Smart Pro lacks —
 * one fixed mapping, latent devices skipped with a one-line note, and the
 * parking lot stays honest:
 *
 *   EV_KEY  BTN_*        → AKeyEvent_* with AKEYCODE_BUTTON_/DPAD_*
 *   EV_ABS  ABS_X/Y/...  → AMotionEvent_* axis slots at the same value
 *                          (Linux sticks/trimmers are ±full-scale int32;
 *                          Android joysticks are ±1 floats)
 *   EV_SYN, EV_MSC       → ignored; Freebuff read events are already
 *                          complete states
 *
 * Compiled only for the device cross build (JUKAHUB_EVDEV). Threads start
 * through android_inputStartEvdev from runner.c once shims are up; the
 * process kills them with exit, as it does for every reader the runner
 * starts.
 */

#define _GNU_SOURCE
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "actbridge.h"

/* This file is the device build's producer: Linux-only paths, POSIX open
 * flags, aarch64 struct layout. Guarded so it compiles (as a no-op file)
 * on any host toolchain; the runner's call site is compiled out alongside
 * (see runner.c's JUKAHUB_EVDEV block), and the cross build compiles the
 * real reader. */
#if defined(__linux__)

/* ── linux/input-event-codes values, kept header-free like the rest of
 * AndroidRT's dlsym probes (the device GCC has linux/input.h, but pinning
 * the constants here is what keeps a rootfs swap from breaking the build) */
#define EV_SYN 0x00
#define EV_KEY 0x01
#define EV_ABS 0x03
#define EV_MSC 0x04

/* struct input_event, aarch64 layout (timeval = 2×int64) */
struct input_event64 {
    long long sec;
    long long usec;
    unsigned short type;
    unsigned short code;
    int value;
};

/* ── EV_KEY → AKEYCODE_* (android/keycodes.h values) ───────────────── */
struct keyMap {
    unsigned short code; /* BTN_* evdev code */
    int keyCode;         /* AKEYCODE_* */
    const char *name;    /* for the probe printout */
};

static const struct keyMap g_keys[] = {
    /* Linux gamepad spec buttons; C (0x132) duplicates X because the spec
     * deprecates it and Android has no KEYCODE_BUTTON_C. */
    { 0x130,  96, "SOUTH→A" },   /* AKEYCODE_BUTTON_A */
    { 0x131,  97, "EAST→B" },    /* AKEYCODE_BUTTON_B */
    { 0x132,  99, "C→X" },       /* AKEYCODE_BUTTON_X */
    { 0x133, 100, "NORTH→Y" },   /* AKEYCODE_BUTTON_Y */
    { 0x134,  99, "WEST→X" },    /* AKEYCODE_BUTTON_X */
    { 0x136, 102, "TL→L1" },     /* AKEYCODE_BUTTON_L1 */
    { 0x137, 103, "TR→R1" },     /* AKEYCODE_BUTTON_R1 */
    { 0x138, 104, "TL2→L2" },    /* AKEYCODE_BUTTON_L2 */
    { 0x139, 105, "TR2→R2" },    /* AKEYCODE_BUTTON_R2 */
    { 0x13a, 109, "SELECT" },    /* AKEYCODE_BUTTON_SELECT */
    { 0x13b, 108, "START" },     /* AKEYCODE_BUTTON_START */
    { 0x13d, 106, "THUMBL" },    /* AKEYCODE_BUTTON_THUMBL */
    { 0x13e, 107, "THUMBR" },    /* AKEYCODE_BUTTON_THUMBR */
    /* D-pad hard buttons of the gamepad spec */
    { 0x220,  19, "DPAD_UP" },   /* AKEYCODE_DPAD_UP */
    { 0x221,  20, "DPAD_DOWN" }, /* AKEYCODE_DPAD_DOWN */
    { 0x222,  21, "DPAD_LEFT" }, /* AKEYCODE_DPAD_LEFT */
    { 0x223,  22, "DPAD_RIGHT" },/* AKEYCODE_DPAD_RIGHT */
};

/* ── EV_ABS → AMotionEvent_AXIS_* ──────────────────────────────────── */
struct axisMap {
    unsigned short code; /* ABS_* */
    int axis;            /* AMotionEvent_AXIS_* (enumerated in inputshim.c) */
    int fullScale;       /* ±full-scale magnitude; 0 = hat, already ±1 */
};

static const struct axisMap g_axes[] = {
    /* 1:1 by name, exactly how Android's generic axis mapping does it:
     * ABS_Z/RZ are the trimmers on the gamepad spec, not a second stick. */
    { 0x00,  0, 32767 }, /* ABS_X    → AXIS_X (left stick)  */
    { 0x01,  1, 32767 }, /* ABS_Y    → AXIS_Y               */
    { 0x02, 11, 32767 }, /* ABS_Z    → AXIS_Z (L trigger)   */
    { 0x03, 12, 32767 }, /* ABS_RX   → AXIS_RX (right stick) */
    { 0x04, 13, 32767 }, /* ABS_RY   → AXIS_RY              */
    { 0x05, 14, 32767 }, /* ABS_RZ   → AXIS_RZ (R trigger)  */
    { 0x10, 15, 0 },     /* ABS_HAT0X → AXIS_HAT_X          */
    { 0x11, 16, 0 },     /* ABS_HAT0Y → AXIS_HAT_Y          */
};

/* ── the reader ────────────────────────────────────────────────────── */

static int g_devfds[16];
static int g_devcount;

static void openEvdevDevices(void) {
    int i;
    char path[64];
    g_devcount = 0;
    for (i = 0; i < (int)(sizeof(g_devfds) / sizeof(g_devfds[0])); i++) {
        snprintf(path, sizeof(path), "/dev/input/event%d", i);
        /* A node the process cannot read (no input group, kiosk sandbox)
         * shows up at open(); that is a device condition, not an error of
         * this launch, so each miss is silent and the summary below is the
         * only honest verdict. */
        int fd = open(path, O_RDONLY | O_NONBLOCK | O_CLOEXEC);
        if (fd < 0) continue;
        g_devfds[g_devcount++] = fd;
        fprintf(stderr, "[androidrt] input: reading %s\n", path);
    }
    if (!g_devcount)
        fprintf(stderr, "[androidrt] input: no /dev/input/event* readable — "
                "controller input unavailable, granting the process the "
                "input group would fix that\n");
}

/* feed: keys always look like a gamepad to games; source 0 lets the input
 * shim apply its default (SOURCE_GAMEPAD for keys, SOURCE_JOYSTICK for
 * axes), which is what Android's classifier would have concluded. */
static void feed(unsigned short type, unsigned short code, int value) {
    int i;
    if (type == EV_SYN || type == EV_MSC) return;

    if (type == EV_KEY) {
        for (i = 0; i < (int)(sizeof(g_keys) / sizeof(g_keys[0])); i++) {
            if (g_keys[i].code == code) {
                android_inputInjectKey(i /* deviceId = evdev index, stable */,
                                       0, /* default gamepad source */
                                       value ? 0 /* AKEY_EVENT_ACTION_DOWN */ : 1,
                                       g_keys[i].keyCode, 0);
                return;
            }
        }
        return; /* other buttons (system keys) stay with the hub's SDL loop */
    }

    if (type == EV_ABS) {
        for (i = 0; i < (int)(sizeof(g_axes) / sizeof(g_axes[0])); i++) {
            if (g_axes[i].code == code) {
                float v;
                if (g_axes[i].fullScale)
                    v = value / (float)g_axes[i].fullScale;
                else
                    v = (float)value; /* hats are ±1 already */
                if (v > 1.0f) v = 1.0f;
                if (v < -1.0f) v = -1.0f;
                android_inputInjectAxis(i /* deviceId = evdev index, stable */,
                                        0, /* default joystick source */
                                        g_axes[i].axis, v);
                return;
            }
        }
    }
}

static void *evdevThread(void *arg) {
    struct input_event64 buf[32];
    (void)arg;
    openEvdevDevices();
    for (;;) {
        int any = 0;
        int i;
        for (i = 0; i < g_devcount; i++) {
            ssize_t n;
            n = read(g_devfds[i], buf, sizeof(buf));
            if (n > 0) {
                size_t count = (size_t)n / sizeof(buf[0]);
                size_t e;
                any = 1;
                for (e = 0; e < count; e++)
                    feed(buf[e].type, buf[e].code, buf[e].value);
            }
            /* n < 0 with EAGAIN just means "no events now"; anything else is
             * a device that went away mid-read (unplug): leak nothing, but
             * don't chase reopens either — relaunching the game gets it. */
        }
        if (!any) {
            struct timespec ts = { 0, 4000 * 1000 }; /* 4 ms poll; games get input within a frame */
            nanosleep(&ts, NULL);
        }
    }
    return NULL;
}

void android_inputStartEvdev(void) {
    pthread_t thread;
    fprintf(stderr, "[androidrt] input: starting evdev reader\n");
    if (pthread_create(&thread, NULL, evdevThread, NULL) != 0) {
        fprintf(stderr, "[androidrt] input: could not start evdev reader thread\n");
        return;
    }
    pthread_detach(thread);
}
#endif /* __linux__ */
