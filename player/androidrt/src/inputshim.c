/* inputshim.c — the input shim: a faithful-enough AInputQueue.
 *
 * On Android, InputDispatcher owns the input pipeline and hands a filled
 * AInputQueue to NativeActivity. Tier 1 does it backwards: any producer (the
 * evdev reader in evdev.c on the device, the runner's smoke hook, or the
 * input probe calling android_inputInjectKey directly) pushes events into a
 * fixed ring here, and the game's AInputQueue calls drain them — with NDK
 * semantics, so android_native_app_glue's input path runs unmodified:
 *
 *   the queue owns a pipe that is readable exactly while events are queued;
 *     AInputQueue_attachLooper registers it on the game thread's ALooper
 *     (looper.c) with the glue's ident and data, so ALooper_pollAll returns
 *     LOOPER_ID_INPUT with &android_app->inputPollSource — the real contract;
 *   AInputQueue_getEvent pops the oldest queued event (the slot becomes
 *     HELD — later injections never overwrite an event the app is reading);
 *   AInputQueue_preDispatchEvent returns 0 (no system-side key state);
 *   AInputQueue_finishEvent frees the slot.
 *
 * The event struct stays private and opaque to games exactly like the real
 * NDK's `struct AInputEvent`: games reach into it only through the accessor
 * functions below, which is why pointer-to-internal is binary-compatible.
 */

#define _GNU_SOURCE
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#if !defined(_WIN32)
#include <unistd.h>
#endif

#include "actbridge.h"

/* AMotionEvent_AXIS_* slots tracked here (0..18 covers X/Y, the second
 * stick, triggers and the hat). */
#define INPUT_AXIS_SLOT_COUNT 19
#define INPUT_AXIS_X 0
#define INPUT_AXIS_Y 1

/* ── events ─────────────────────────────────────────────────────────── */

struct AndroidInputEvent {
    int32_t type;        /* AINPUT_EVENT_TYPE_* */
    int32_t deviceId;
    int32_t source;      /* AINPUT_SOURCE_* of the producing device */
    int32_t state;       /* 1 = queued, 2 = held by the app, 0 = free */
    int32_t keyAction;   /* AKEY_EVENT_ACTION_* */
    int32_t keyCode;
    int32_t repeatCount;
    int32_t motionAction;
    int64_t eventTime;   /* CLOCK_MONOTONIC ns, like Android's uptime */
    float axes[INPUT_AXIS_SLOT_COUNT];
};

typedef struct AndroidInputQueue {
    pthread_mutex_t lock;
    struct AndroidInputEvent slots[128];
    int scanStart;
    int alive;
    int fds[2];          /* readable while an event is queued (state 1)  */
    int signaled;
    ALooper *looper;     /* where AInputQueue_attachLooper registered fds[0] */
} AndroidInputQueue;

static AndroidInputQueue g_queue = {
    .lock = PTHREAD_MUTEX_INITIALIZER,
    .scanStart = 0,
    .fds = { -1, -1 },
};

/* The devices behind the events (android.view.InputDevice), each with the
 * current value of every axis: Android's joystick MotionEvents carry the
 * whole stick state, so a MOVE for the Y axis still reports X. Guarded by
 * g_queue.lock. */
#define INPUT_MAX_DEVICES 32
static struct {
    int used;
    int32_t id;
    char name[64];
    float axes[INPUT_AXIS_SLOT_COUNT];
} g_devices[INPUT_MAX_DEVICES];

static int deviceLocked(int32_t id) {
    int i, freeSlot = -1;
    for (i = 0; i < INPUT_MAX_DEVICES; i++) {
        if (g_devices[i].used && g_devices[i].id == id) return i;
        if (!g_devices[i].used && freeSlot < 0) freeSlot = i;
    }
    if (freeSlot < 0) return -1;
    memset(&g_devices[freeSlot], 0, sizeof g_devices[freeSlot]);
    g_devices[freeSlot].used = 1;
    g_devices[freeSlot].id = id;
    snprintf(g_devices[freeSlot].name, sizeof g_devices[freeSlot].name, "TrimUI Gamepad");
    return freeSlot;
}

void android_inputRegisterDevice(int32_t deviceId, const char *name) {
    int d;
    pthread_mutex_lock(&g_queue.lock);
    d = deviceLocked(deviceId);
    if (d >= 0 && name && *name) snprintf(g_devices[d].name, sizeof g_devices[d].name, "%s", name);
    pthread_mutex_unlock(&g_queue.lock);
}

int android_inputDeviceIds(int32_t *ids, int cap) {
    int i, n = 0;
    pthread_mutex_lock(&g_queue.lock);
    for (i = 0; i < INPUT_MAX_DEVICES && n < cap; i++)
        if (g_devices[i].used) ids[n++] = g_devices[i].id;
    pthread_mutex_unlock(&g_queue.lock);
    return n;
}

const char *android_inputDeviceName(int32_t deviceId) {
    static char name[64];
    int i;
    snprintf(name, sizeof name, "TrimUI Gamepad");
    pthread_mutex_lock(&g_queue.lock);
    for (i = 0; i < INPUT_MAX_DEVICES; i++)
        if (g_devices[i].used && g_devices[i].id == deviceId) snprintf(name, sizeof name, "%s", g_devices[i].name);
    pthread_mutex_unlock(&g_queue.lock);
    return name;
}

static int64_t nowNs(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

#define EV(e) ((const struct AndroidInputEvent *)(e))
#define IS_KEY(e) ((e) && EV(e)->type == AINPUT_EVENT_TYPE_KEY)
#define IS_MOTION(e) ((e) && EV(e)->type == AINPUT_EVENT_TYPE_MOTION)

/* ── AInputEvent accessors (the game-facing ABI) ────────────────────── */

ANDROIDRT_EXPORT int32_t AInputEvent_getType(const AInputEvent *event) { return event ? EV(event)->type : 0; }
ANDROIDRT_EXPORT int32_t AInputEvent_getDeviceId(const AInputEvent *event) { return event ? EV(event)->deviceId : 0; }
ANDROIDRT_EXPORT int32_t AInputEvent_getSource(const AInputEvent *event) { return event ? EV(event)->source : 0; }
ANDROIDRT_EXPORT void AInputEvent_release(const AInputEvent *event) { (void)event; }

ANDROIDRT_EXPORT int32_t AKeyEvent_getAction(const AInputEvent *event) { return IS_KEY(event) ? EV(event)->keyAction : -1; }
ANDROIDRT_EXPORT int32_t AKeyEvent_getKeyCode(const AInputEvent *event) { return IS_KEY(event) ? EV(event)->keyCode : -1; }
ANDROIDRT_EXPORT int32_t AKeyEvent_getRepeatCount(const AInputEvent *event) { return IS_KEY(event) ? EV(event)->repeatCount : 0; }
ANDROIDRT_EXPORT int32_t AKeyEvent_getMetaState(const AInputEvent *event) {
    (void)event;
    return 0; /* no modifier tracking on Tier 1 */
}
ANDROIDRT_EXPORT int32_t AKeyEvent_getFlags(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT int32_t AKeyEvent_getScanCode(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT int64_t AKeyEvent_getDownTime(const AInputEvent *event) { return event ? EV(event)->eventTime : 0; }
ANDROIDRT_EXPORT int64_t AKeyEvent_getEventTime(const AInputEvent *event) { return event ? EV(event)->eventTime : 0; }

ANDROIDRT_EXPORT int32_t AMotionEvent_getAction(const AInputEvent *event) { return IS_MOTION(event) ? EV(event)->motionAction : -1; }
ANDROIDRT_EXPORT size_t AMotionEvent_getPointerCount(const AInputEvent *event) {
    return IS_MOTION(event) ? 1 : 0; /* a gamepad reads as one virtual pointer */
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getPointerId(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    return pointerIndex == 0 ? 0 : -1;
}
ANDROIDRT_EXPORT float AMotionEvent_getAxisValue(const AInputEvent *event, int32_t axis, size_t pointerIndex) {
    if (!IS_MOTION(event) || pointerIndex != 0) return 0.0f;
    if (axis < 0 || axis >= INPUT_AXIS_SLOT_COUNT) return 0.0f;
    return EV(event)->axes[axis];
}
ANDROIDRT_EXPORT float AMotionEvent_getX(const AInputEvent *event, size_t pointerIndex) {
    return AMotionEvent_getAxisValue(event, INPUT_AXIS_X, pointerIndex);
}
ANDROIDRT_EXPORT float AMotionEvent_getY(const AInputEvent *event, size_t pointerIndex) {
    return AMotionEvent_getAxisValue(event, INPUT_AXIS_Y, pointerIndex);
}
ANDROIDRT_EXPORT float AMotionEvent_getRawX(const AInputEvent *event, size_t pointerIndex) {
    return AMotionEvent_getX(event, pointerIndex);
}
ANDROIDRT_EXPORT float AMotionEvent_getRawY(const AInputEvent *event, size_t pointerIndex) {
    return AMotionEvent_getY(event, pointerIndex);
}
ANDROIDRT_EXPORT float AMotionEvent_getPressure(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    return pointerIndex == 0 ? 1.0f : 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getSize(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    (void)pointerIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT int64_t AMotionEvent_getDownTime(const AInputEvent *event) { return event ? EV(event)->eventTime : 0; }
ANDROIDRT_EXPORT int64_t AMotionEvent_getEventTime(const AInputEvent *event) { return event ? EV(event)->eventTime : 0; }
ANDROIDRT_EXPORT size_t AMotionEvent_getHistorySize(const AInputEvent *event) {
    (void)event;
    return 0; /* no coalesced history batches */
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getFlags(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getMetaState(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getButtonState(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getEdgeFlags(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getActionButton(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getClassification(const AInputEvent *event) {
    (void)event;
    return 0;
}
ANDROIDRT_EXPORT float AMotionEvent_getXOffset(const AInputEvent *event) {
    (void)event;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getYOffset(const AInputEvent *event) {
    (void)event;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getXPrecision(const AInputEvent *event) {
    (void)event;
    return 1.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getYPrecision(const AInputEvent *event) {
    (void)event;
    return 1.0f;
}
ANDROIDRT_EXPORT int32_t AMotionEvent_getToolType(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    (void)pointerIndex;
    return 0; /* AMOTION_EVENT_TOOL_TYPE_UNKNOWN: a stick, not a finger */
}
ANDROIDRT_EXPORT float AMotionEvent_getTouchMajor(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    (void)pointerIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getTouchMinor(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    (void)pointerIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getToolMajor(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    (void)pointerIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getToolMinor(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    (void)pointerIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getOrientation(const AInputEvent *event, size_t pointerIndex) {
    (void)event;
    (void)pointerIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getHistoricalAxisValue(const AInputEvent *event, int32_t axis,
                                                           size_t pointerIndex, size_t historyIndex) {
    (void)event;
    (void)axis;
    (void)pointerIndex;
    (void)historyIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getHistoricalX(const AInputEvent *event, size_t pointerIndex, size_t historyIndex) {
    (void)event;
    (void)pointerIndex;
    (void)historyIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT float AMotionEvent_getHistoricalY(const AInputEvent *event, size_t pointerIndex, size_t historyIndex) {
    (void)event;
    (void)pointerIndex;
    (void)historyIndex;
    return 0.0f;
}
ANDROIDRT_EXPORT int64_t AMotionEvent_getHistoricalEventTime(const AInputEvent *event, size_t historyIndex) {
    (void)event;
    (void)historyIndex;
    return 0;
}

/* ── queue helpers ──────────────────────────────────────────────────── */

static AndroidInputQueue *asQueue(AInputQueue *queue) {
    return queue ? (AndroidInputQueue *)queue : &g_queue;
}

/* Oldest slot in a given state, scanning from scanStart so the ring
 * advances. Negative type/deviceId are wildcards. Caller holds the lock. */
static struct AndroidInputEvent *findSlotLocked(AndroidInputQueue *queue, int state, int type, int deviceId) {
    int count = (int)(sizeof(queue->slots) / sizeof(queue->slots[0]));
    int i;
    for (i = 0; i < count; i++) {
        int idx = (queue->scanStart + i) % count;
        struct AndroidInputEvent *slot = &queue->slots[idx];
        if (slot->state == state && (type < 0 || slot->type == type) &&
            (deviceId < 0 || slot->deviceId == deviceId)) {
            queue->scanStart = (idx + 1) % count;
            return slot;
        }
    }
    return NULL;
}

static int pendingLocked(const AndroidInputQueue *queue) {
    int count = (int)(sizeof(queue->slots) / sizeof(queue->slots[0]));
    int i;
    for (i = 0; i < count; i++)
        if (queue->slots[i].state == 1) return 1;
    return 0;
}

/* Keep fds[0] readable exactly while events are queued (level-triggered,
 * like Android's input channel). Caller holds the lock. */
static void signalLocked(AndroidInputQueue *queue) {
#if !defined(_WIN32)
    int pending = pendingLocked(queue);
    if (queue->fds[1] < 0) return;
    if (pending && !queue->signaled) {
        ssize_t w = write(queue->fds[1], "I", 1);
        (void)w;
        queue->signaled = 1;
    } else if (!pending && queue->signaled) {
        char buf[16];
        while (read(queue->fds[0], buf, sizeof buf) > 0) {
        }
        queue->signaled = 0;
    }
#else
    (void)queue;
#endif
}

static struct AndroidInputEvent *takeFreeSlot(AndroidInputQueue *queue) {
    int count = (int)(sizeof(queue->slots) / sizeof(queue->slots[0]));
    static int warned;
    int i;
    for (i = 0; i < count; i++) {
        struct AndroidInputEvent *slot = &queue->slots[(queue->scanStart + i) % count];
        if (slot->state == 0) return slot;
    }
    /* Ring full. Slots the app holds are untouchable (it is mid-read), so
     * the only honest move is to drop the newest event, once-logged. */
    if (!warned) {
        warned = 1;
        fprintf(stderr, "[androidrt] input: event ring full — dropping new events\n");
    }
    return NULL;
}

/* ── the AInputQueue object ─────────────────────────────────────────── */

AInputQueue *android_inputQueueCreate(void) {
    pthread_mutex_lock(&g_queue.lock);
#if !defined(_WIN32)
    if (g_queue.fds[0] < 0) {
        int fds[2];
        if (pipe2(fds, O_NONBLOCK | O_CLOEXEC) == 0) {
            g_queue.fds[0] = fds[0];
            g_queue.fds[1] = fds[1];
        } else {
            fprintf(stderr, "[androidrt] input: no wake pipe — LOOPER_ID_INPUT will never fire\n");
        }
    }
#endif
    g_queue.alive = 1;
    pthread_mutex_unlock(&g_queue.lock);
    return (AInputQueue *)&g_queue;
}

void android_inputQueueDestroy(AInputQueue *queue) {
    AndroidInputQueue *q = asQueue(queue);
    AInputQueue_detachLooper(queue);
    pthread_mutex_lock(&q->lock);
    memset(q->slots, 0, sizeof(q->slots));
    q->alive = 0;
    signalLocked(q);
    pthread_mutex_unlock(&q->lock);
}

int android_inputHasEvents(void) {
    int found;
    if (!g_queue.alive) return 0;
    pthread_mutex_lock(&g_queue.lock);
    found = pendingLocked(&g_queue);
    pthread_mutex_unlock(&g_queue.lock);
    return found;
}

AInputQueue *android_inputQueueCurrent(void) { return g_queue.alive ? (AInputQueue *)&g_queue : NULL; }

/* ── the queue API (game-facing) ────────────────────────────────────── */

ANDROIDRT_EXPORT int32_t AInputQueue_getEvent(AInputQueue *queue, AInputEvent **outEvent) {
    AndroidInputQueue *q = asQueue(queue);
    struct AndroidInputEvent *slot;
    pthread_mutex_lock(&q->lock);
    slot = findSlotLocked(q, 1, -1, -1);
    if (slot) slot->state = 2; /* held — the app now owns this event */
    signalLocked(q);
    pthread_mutex_unlock(&q->lock);
    if (!slot) return -11; /* -EWOULDBLOCK, same shape as the NDK */
    if (outEvent) *outEvent = (AInputEvent *)slot;
    return 0;
}

ANDROIDRT_EXPORT int32_t AInputQueue_hasEvents(AInputQueue *queue) {
    (void)queue;
    return android_inputHasEvents();
}

ANDROIDRT_EXPORT int32_t AInputQueue_preDispatchEvent(AInputQueue *queue, AInputEvent *event) {
    (void)queue;
    (void)event;
    return 0; /* nothing on Tier 1 swallows an event before the game */
}

ANDROIDRT_EXPORT void AInputQueue_finishEvent(AInputQueue *queue, AInputEvent *event, int handled) {
    AndroidInputQueue *q = asQueue(queue);
    struct AndroidInputEvent *e = (struct AndroidInputEvent *)event;
    (void)handled;
    if (!e) return;
    pthread_mutex_lock(&q->lock);
    if (e >= q->slots && e < q->slots + (int)(sizeof(q->slots) / sizeof(q->slots[0]))) memset(e, 0, sizeof(*e));
    pthread_mutex_unlock(&q->lock);
}

ANDROIDRT_EXPORT void AInputQueue_attachLooper(AInputQueue *queue, ALooper *looper, int ident,
                                               ALooper_callbackFunc callback, void *data) {
    AndroidInputQueue *q = asQueue(queue);
    ALooper *old;
    int fd;
    pthread_mutex_lock(&q->lock);
    old = q->looper;
    fd = q->fds[0];
    q->looper = looper;
    pthread_mutex_unlock(&q->lock);
    if (fd < 0 || !looper) return;
    if (old && old != looper) ALooper_removeFd(old, fd);
    /* events = ALOOPER_EVENT_INPUT */
    if (ALooper_addFd(looper, fd, ident, 1, callback, data) != 1)
        fprintf(stderr, "[androidrt] input: AInputQueue_attachLooper could not register the queue\n");
}

ANDROIDRT_EXPORT void AInputQueue_detachLooper(AInputQueue *queue) {
    AndroidInputQueue *q = asQueue(queue);
    ALooper *old;
    int fd;
    pthread_mutex_lock(&q->lock);
    old = q->looper;
    fd = q->fds[0];
    q->looper = NULL;
    pthread_mutex_unlock(&q->lock);
    if (old && fd >= 0) ALooper_removeFd(old, fd);
}

/* ── producer hooks (evdev.c on-device; probes and smoke hooks) ─────── */

void android_inputInjectKey(int32_t deviceId, int32_t source, int32_t action, int32_t keyCode, int32_t repeat) {
    struct AndroidInputEvent *slot;
    if (!g_queue.alive) return;
    pthread_mutex_lock(&g_queue.lock);
    deviceLocked(deviceId);
    slot = takeFreeSlot(&g_queue);
    if (slot) {
        memset(slot, 0, sizeof(*slot));
        slot->type = AINPUT_EVENT_TYPE_KEY;
        slot->deviceId = deviceId;
        slot->source = source ? source : AINPUT_SOURCE_GAMEPAD;
        slot->state = 1;
        slot->keyAction = action;
        slot->keyCode = keyCode;
        slot->repeatCount = repeat;
        slot->eventTime = nowNs();
    }
    signalLocked(&g_queue);
    pthread_mutex_unlock(&g_queue.lock);
}

void android_inputInjectAxis(int32_t deviceId, int32_t source, int32_t axis, float value) {
    struct AndroidInputEvent *pending;
    int dev;
    if (!g_queue.alive) return;
    if (axis < 0 || axis >= INPUT_AXIS_SLOT_COUNT) return;
    pthread_mutex_lock(&g_queue.lock);
    dev = deviceLocked(deviceId);
    if (dev >= 0) g_devices[dev].axes[axis] = value;
    /* Merge into an already-queued-but-unread motion event of the same
     * stick — how Android batches joystick updates so a 60 Hz reader never
     * buries the queue in per-axis MOVE events. Every event carries the
     * device's whole current axis state. */
    pending = findSlotLocked(&g_queue, 1, AINPUT_EVENT_TYPE_MOTION, deviceId);
    if (pending) {
        if (dev >= 0) memcpy(pending->axes, g_devices[dev].axes, sizeof pending->axes);
        else pending->axes[axis] = value;
        pending->eventTime = nowNs();
    } else {
        struct AndroidInputEvent *slot = takeFreeSlot(&g_queue);
        if (slot) {
            memset(slot, 0, sizeof(*slot));
            slot->type = AINPUT_EVENT_TYPE_MOTION;
            slot->deviceId = deviceId;
            slot->source = source ? source : AINPUT_SOURCE_JOYSTICK;
            slot->state = 1;
            slot->motionAction = 2; /* AMOTION_EVENT_ACTION_MOVE */
            if (dev >= 0) memcpy(slot->axes, g_devices[dev].axes, sizeof slot->axes);
            else slot->axes[axis] = value;
            slot->eventTime = nowNs();
        }
    }
    signalLocked(&g_queue);
    pthread_mutex_unlock(&g_queue.lock);
}
