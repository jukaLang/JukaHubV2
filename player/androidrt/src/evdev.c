/* evdev.c — the device-side input producer for AndroidRT.
 *
 * The Smart Pro's controller shows up as /dev/input/event*: plain evdev,
 * exactly the layer the Linux kernel hands to Android's EventHub. Android
 * itself then classifies each device (gamepad vs keyboard vs touchscreen)
 * and maps Linux codes onto Android keycodes and AMotionEvent axes. This
 * file is that layer for Tier 1, minus everything the Smart Pro lacks —
 * native Android gamepad mapping plus a keyboard-control mode for ports such
 * as Penguin Dash:
 *
 *   EV_KEY  BTN_*        → Android gamepad buttons (or keyboard controls)
 *   EV_ABS  ABS_X/Y/...  → calibrated Android joystick axes; the D-pad hat
 *                          also becomes DPAD key events on every controller
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
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <time.h>
#include <unistd.h>

#include "actbridge.h"
#include "gamepadmap.h"

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

/* Axis order matches gamepadmap.c. Query the controller's actual min/max:
 * Smart Pro triggers are 0..255 while its sticks are -32767..32767. */
static const unsigned short g_axisCodes[8] = {0, 1, 2, 3, 4, 5, 0x10, 0x11};
struct apak_absinfo { int value, minimum, maximum, fuzz, flat, resolution; };
#define APAK_EVIOCGABS(axis) ((2u << 30) | ((unsigned)sizeof(struct apak_absinfo) << 16) | ('E' << 8) | (0x40u + (axis)))
struct axisRange { int min, max; };

/* ── the reader ────────────────────────────────────────────────────── */

static int g_devfds[16];
static int g_devnodes[16]; /* N of /dev/input/eventN: the Android deviceId */
static struct axisRange g_ranges[16][8];
static ApakGamepadMap g_maps[16];
static int g_devcount;
static ApakGamepadMode g_mode;
static int g_traceInput;

/* EVIOCGNAME(len): _IOC(_IOC_READ, 'E', 0x06, len) */
#define APAK_EVIOCGNAME(len) ((2u << 30) | ((unsigned)(len) << 16) | ('E' << 8) | 0x06u)
#define APAK_EVIOCGBIT(type, len) ((2u << 30) | ((unsigned)(len) << 16) | ('E' << 8) | (0x20u + (type)))

static void openEvdevDevices(void) {
    static const int defaultMin[8] = {-32767, -32767, 0, -32767, -32767, 0, -1, -1};
    static const int defaultMax[8] = {32767, 32767, 255, 32767, 32767, 255, 1, 1};
    int i;
    char path[64];
    g_devcount = 0;
    for (i = 0; i < (int)(sizeof(g_devfds) / sizeof(g_devfds[0])); i++) {
        char name[64] = "";
        snprintf(path, sizeof(path), "/dev/input/event%d", i);
        /* A node the process cannot read (no input group, kiosk sandbox)
         * shows up at open(); that is a device condition, not an error of
         * this launch, so each miss is silent and the summary below is the
         * only honest verdict. */
        int fd = open(path, O_RDONLY | O_NONBLOCK | O_CLOEXEC);
        if (fd < 0) continue;
        /* A keyboard or power switch is not an Android gamepad. Ignore it
         * so InputDevice enumeration contains only real controllers. */
        {
            unsigned char keys[64] = {0};
            struct apak_absinfo stick = {0};
            int hasButtons = ioctl(fd, APAK_EVIOCGBIT(EV_KEY, sizeof keys), keys) >= 0 &&
                             (keys[0x130 / 8] & (1u << (0x130 % 8)));
            int hasStick = ioctl(fd, APAK_EVIOCGABS(0), &stick) >= 0 && stick.maximum > stick.minimum;
            if (!hasButtons && !hasStick) { close(fd); continue; }
        }
        if (ioctl(fd, APAK_EVIOCGNAME(sizeof name - 1), name) < 0) name[0] = '\0';
        int slot = g_devcount;
        g_devnodes[g_devcount] = i;
        g_devfds[g_devcount++] = fd;
        apak_gamepadInit(&g_maps[slot], g_mode);
        apak_gamepadSetSwapAB(&g_maps[slot], strncmp(name, "TRIMUI Player", 13) == 0);
        for (int a = 0; a < 8; a++) {
            struct apak_absinfo info = {0};
            g_ranges[slot][a].min = defaultMin[a];
            g_ranges[slot][a].max = defaultMax[a];
            if (ioctl(fd, APAK_EVIOCGABS(g_axisCodes[a]), &info) >= 0 && info.maximum > info.minimum) {
                g_ranges[slot][a].min = info.minimum;
                g_ranges[slot][a].max = info.maximum;
            }
        }
        android_inputRegisterDevice(i, name[0] ? name : "TrimUI Gamepad");
        fprintf(stderr, "[androidrt] input: reading %s (%s) as device %d, map %s, A/B %s\n", path,
                name[0] ? name : "unnamed", i, apak_gamepadModeName(g_mode),
                g_maps[slot].swapAB ? "TrimUI" : "standard");
    }
    if (!g_devcount)
        fprintf(stderr, "[androidrt] input: no /dev/input/event* readable — "
                "controller input unavailable, granting the process the "
                "input group would fix that\n");
}

static void emitKey(void *ctx, int keyCode, int action) {
    int deviceId = *(int *)ctx;
    if (g_traceInput)
        fprintf(stderr, "[androidrt] input: device %d key %d %s\n", deviceId, keyCode,
                action ? "up" : "down");
    android_inputInjectKey(deviceId, g_mode == APAK_GAMEPAD_NATIVE ? AINPUT_SOURCE_GAMEPAD : AINPUT_SOURCE_KEYBOARD,
                           action, keyCode, 0);
}

static void emitAxis(void *ctx, int axis, float value) {
    int deviceId = *(int *)ctx;
    if (g_traceInput)
        fprintf(stderr, "[androidrt] input: device %d axis %d %.3f\n", deviceId, axis, value);
    android_inputInjectAxis(deviceId, 0, axis, value);
}

static void feed(int slot, unsigned short type, unsigned short code, int value) {
    ApakGamepadMap *map = &g_maps[slot];
    void *ctx = &g_devnodes[slot];
    if (type == EV_SYN || type == EV_MSC) return;

    if (type == EV_KEY) {
        apak_gamepadFeedKey(map, code, value, emitKey, ctx);
        return;
    }

    if (type == EV_ABS) {
        for (int a = 0; a < 8; a++) {
            if (g_axisCodes[a] == code) {
                float normalized = apak_gamepadNormalize(code, value, g_ranges[slot][a].min,
                                                         g_ranges[slot][a].max);
                apak_gamepadFeedAxis(map, code, normalized, emitKey, emitAxis, ctx);
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
                    feed(i, buf[e].type, buf[e].code, buf[e].value);
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

void android_inputStartEvdev(const char *packageName) {
    pthread_t thread;
    const char *option = getenv("ANDROIDPAK_GAMEPAD_MAP");
    const char *traceInput = getenv("ANDROIDPAK_INPUT_TRACE");
    g_mode = apak_gamepadMode(packageName, option);
    g_traceInput = traceInput && strcmp(traceInput, "1") == 0;
    fprintf(stderr, "[androidrt] input: starting evdev reader (map %s)\n", apak_gamepadModeName(g_mode));
    if (pthread_create(&thread, NULL, evdevThread, NULL) != 0) {
        fprintf(stderr, "[androidrt] input: could not start evdev reader thread\n");
        return;
    }
    pthread_detach(thread);
}
#endif /* __linux__ */
