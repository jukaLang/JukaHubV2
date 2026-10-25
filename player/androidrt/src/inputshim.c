/* inputshim.c — the input shim: a faithful-enough AInputQueue.
 *
 * Pork soda: on Android, SurfaceFlinger/EventHub own the input pipeline and
 * the framework hands a filled AInputQueue to NativeActivity. Tier 1 does
 * it backwards: any producer (the evdev reader in evdev.c on the device, or
 * the smoke probe calling android_inputInjectKey directly) pushes events
 * into a fixed ring here, and the game's AInputQueue calls drain them —
 * with NDK semantics, so android_native_app_glue's input path runs
 * unmodified:
 *
 *   LOOPER_ID_INPUT wakes the glue thread (androidshim's ALooper asks
 *     android_inputHasEvents before sleeping);
 *   AInputQueue_getEvent pops the oldest queued event (the slot becomes
 *     HELD — later injections can never overwrite an event the app is
 *     reading, a crash the naive ring had);
 *   AInputQueue_preDispatchEvent returns 0 (no system-side key state);
 *   AInputQueue_finishEvent frees the slot.
 *
 * The event struct stays private and opaque to games exactly like the real
 * NDK's `struct AInputEvent`: games reach into it only through the accessor
 * functions below, which is why pointer-to-internal is binary-compatible.
 */

#define _GNU_SOURCE
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "actbridge.h"

/* ── android/input.h constants (NDK values) live in actbridge.h; the axis
 * slots this file tracks mirror AMotionEvent_AXIS_* up to AXIS_HAT_Y —
 * higher enum values (vscroll, the sensor axes) are off. ──────────── */

/* AMotionEvent_AXIS_* — indexed only up to AXIS_HAT_Y; higher enum values
 * (vscroll, the sensor axes) are off. MAX axis slot in evdev maps in here. */
#define INPUT_AXIS_SLOT_COUNT 19 /* axis slots 0..18 */
#define INPUT_AXIS_X 0
#define INPUT_AXIS_Y 1
#define INPUT_AXIS_RX 12
#define INPUT_AXIS_RY 13
#define INPUT_AXIS_Z 11
#define INPUT_AXIS_RZ 14
#define INPUT_AXIS_HAT_X 15
#define INPUT_AXIS_HAT_Y 16
#define INPUT_AXIS_LTRIGGER 17
#define INPUT_AXIS_RTRIGGER 18

/* ── events ─────────────────────────────────────────────────────────── */

struct AndroidInputEvent {
    int32_t type;        /* AINPUT_EVENT_TYPE_* */
    int32_t deviceId;
    int32_t source;      /* AINPUT_SOURCE_* of the producing device */
    int32_t state;       /* 1 = queued, 2 = held by the app, 0 = free */
    int32_t keyAction;   /* AMOTION_EVENT_ACTION / AKEY_EVENT_ACTION_* */
    int32_t keyCode;
    int32_t repeatCount;
    int32_t motionAction;
    float axes[INPUT_AXIS_SLOT_COUNT];
};

typedef struct AndroidInputQueue {
    pthread_mutex_t lock;
    struct AndroidInputEvent slots[128];
    int scanStart;
    int alive;
} AndroidInputQueue;

static AndroidInputQueue g_queue = {
    .lock = PTHREAD_MUTEX_INITIALIZER,
    .scanStart = 0,
};

/* Singleton queue: Android delivers exactly one per activity here — not,
 * but several distinct ones couldn't be distinguished anyway with a single
 * g_events. Cheap闭上, keep a real object so the smoke probe can run
 * create/destroy cycles and pollAll still returns a stable pointer. */

/* ── AInputEvent accessors (the game-facing ABI, ANDROIDRT_EXPORT) ──── */

ANDROIDRT_EXPORT int32_t AInputEvent_getType(const AInputEvent *event) {
    return event ? ((const struct AndroidInputEvent *)event)->type : 0;
}

ANDROIDRT_EXPORT int32_t AInputEvent_getDeviceId(const AInputEvent *event) {
    return event ? ((const struct AndroidInputEvent *)event)->deviceId : 0;
}

ANDROIDRT_EXPORT int32_t AInputEvent_getSource(const AInputEvent *event) {
    return event ? ((const struct AndroidInputEvent *)event)->source : 0;
}

ANDROIDRT_EXPORT int32_t AKeyEvent_getAction(const AInputEvent *event) {
    if (event && ((const struct AndroidInputEvent *)event)->type == AINPUT_EVENT_TYPE_KEY)
        return ((const struct AndroidInputEvent *)event)->keyAction;
    return -1;
}

ANDROIDRT_EXPORT int32_t AKeyEvent_getKeyCode(const AInputEvent *event) {
    if (event && ((const struct AndroidInputEvent *)event)->type == AINPUT_EVENT_TYPE_KEY)
        return ((const struct AndroidInputEvent *)event)->keyCode;
    return -1;
}

ANDROIDRT_EXPORT int32_t AKeyEvent_getRepeatCount(const AInputEvent *event) {
    if (event && ((const struct AndroidInputEvent *)event)->type == AINPUT_EVENT_TYPE_KEY)
        return ((const struct AndroidInputEvent *)event)->repeatCount;
    return 0;
}

ANDROIDRT_EXPORT int32_t AKeyEvent_getMetaState(const AInputEvent *event) {
    (void)event;
    return 0; /* no modifier tracking on Tier 1 */
}

ANDROIDRT_EXPORT int32_t AMotionEvent_getAction(const AInputEvent *event) {
    if (event && ((const struct AndroidInputEvent *)event)->type == AINPUT_EVENT_TYPE_MOTION)
        return ((const struct AndroidInputEvent *)event)->motionAction;
    return -1;
}

ANDROIDRT_EXPORT size_t AMotionEvent_getPointerCount(const AInputEvent *event) {
    if (event && ((const struct AndroidInputEvent *)event)->type == AINPUT_EVENT_TYPE_MOTION)
        return 1; /* gamepad always appears as a single virtual finger */
    return 0;
}

ANDROIDRT_EXPORT int32_t AMotionEvent_getPointerId(const AInputEvent *event, size_t pointerIndex) {
    return pointerIndex == 0 ? 0 : -1;
}

ANDROIDRT_EXPORT float AMotionEvent_getAxisValue(const AInputEvent *event, int32_t axis, size_t pointerIndex) {
    const struct AndroidInputEvent *e = (const struct AndroidInputEvent *)event;
    if (!e || e->type != AINPUT_EVENT_TYPE_MOTION) return 0.0f;
    if (pointerIndex != 0) return 0.0f;
    if (axis < 0 || axis >= INPUT_AXIS_SLOT_COUNT) return 0.0f;
    return e->axes[axis];
}

ANDROIDRT_EXPORT float AMotionEvent_getX(const AInputEvent *event, size_t pointerIndex) {
    return AMotionEvent_getAxisValue(event, INPUT_AXIS_X, pointerIndex);
}

ANDROIDRT_EXPORT float AMotionEvent_getY(const AInputEvent *event, size_t pointerIndex) {
    return AMotionEvent_getAxisValue(event, INPUT_AXIS_Y, pointerIndex);
}

ANDROIDRT_EXPORT float AMotionEvent_getPressure(const AInputEvent *event, size_t pointerIndex) {
    return pointerIndex == 0 ? 1.0f : 0.0f; /* touch games expect a firm touch */
}

ANDROIDRT_EXPORT float AMotionEvent_getSize(const AInputEvent *event, size_t pointerIndex) {
    (void)event; (void)pointerIndex;
    return 0.0f;
}

ANDROIDRT_EXPORT int64_t AMotionEvent_getDownTime(const AInputEvent *event) {
    (void)event;
    return 0;
}

ANDROIDRT_EXPORT size_t AMotionEvent_getHistorySize(const AInputEvent *event) {
    (void)event;
    return 0; /* no coalesced history batches */
}

/* ── queue helpers ──────────────────────────────────────────────────── */

static AndroidInputQueue *asQueue(AInputQueue *queue) {
    return queue ? (AndroidInputQueue *)queue : &g_queue;
}

/* find the oldest slot matching a pending state; start from scanStart so
 * the ring actually advances instead of always scanning slot 0 first.
 * Negative type/deviceId are wildcards: getEvent drains any event, while
 * the axis-merge lookup pins the type and the stick's device id.
 * LOCKING CALLER: the caller must already hold queue->lock — the inject
 * calls search under their own lock, and a recursive lock would deadlock. */
static struct AndroidInputEvent *findSlotLocked(AndroidInputQueue *queue, int state, int type, int deviceId) {
    int count = (int)(sizeof(queue->slots) / sizeof(queue->slots[0]));
    int found = -1;
    int i;

    for (i = 0; i < count; i++) {
        struct AndroidInputEvent *slot = &queue->slots[(queue->scanStart + i) % count];
        if (slot->state == state &&
            (type < 0 || slot->type == type) &&
            (deviceId < 0 || slot->deviceId == deviceId)) {
            found = (queue->scanStart + i) % count;
            queue->scanStart = (found + 1) % count;
            return found >= 0 ? &queue->slots[found] : NULL;
        }
    }
    /* scan again from zero in case scanStart wrapped past an older match */
    for (i = 0; i < count; i++) {
        struct AndroidInputEvent *slot = &queue->slots[i];
        if (slot->state == state &&
            (type < 0 || slot->type == type) &&
            (deviceId < 0 || slot->deviceId == deviceId)) {
            found = i;
            queue->scanStart = (found + 1) % count;
            return &queue->slots[found];
        }
    }
    return NULL;
}

static struct AndroidInputEvent *takeFreeSlot(AndroidInputQueue *queue) {
    int count = (int)(sizeof(queue->slots) / sizeof(queue->slots[0]));
    static int warned;
    int i;
    for (i = 0; i < count; i++) {
        struct AndroidInputEvent *slot = &queue->slots[(queue->scanStart + i) % count];
        if (slot->state == 0) {
            queue->scanStart = ((queue->scanStart + i) + 1) % count;
            return slot;
        }
    }
    /* Ring full. Slots the app holds are untouchable (it is mid-read), so
     * the only honest move is to drop the newest event; the warning fires
     * once per incident — a stuck stick reads as a missing axis, never as
     * a crash. */
    if (!warned) {
        warned = 1;
        fprintf(stderr, "[androidrt] input: event ring full — dropping new events\n");
    }
    return NULL;
}

/* ── shipping the AInputQueue struct (opaque to games) ──────────────── */

ANDROIDRT_EXPORT AInputQueue *android_inputQueueCreate(void) {
    pthread_mutex_lock(&g_queue.lock);
    g_queue.alive = 1;
    pthread_mutex_unlock(&g_queue.lock);
    return (AInputQueue *)&g_queue;
}

void android_inputQueueDestroy(AInputQueue *queue) {
    AndroidInputQueue *q = asQueue(queue);
    pthread_mutex_lock(&q->lock);
    memset(q->slots, 0, sizeof(q->slots));
    q->alive = 0;
    pthread_mutex_unlock(&q->lock);
}
int android_inputHasEvents(void) {
    int count = (int)(sizeof(g_queue.slots) / sizeof(g_queue.slots[0]));
    int found = 0;
    int i;
    if (!g_queue.alive) return 0;
    pthread_mutex_lock(&g_queue.lock);
    for (i = 0; i < count; i++) {
        if (g_queue.slots[i].state == 1) { found = 1; break; }
    }
    pthread_mutex_unlock(&g_queue.lock);
    return found;
}

AInputQueue *android_inputQueueCurrent(void) {
    return g_queue.alive ? (AInputQueue *)&g_queue : NULL;
}

/* ── the queue API (game-facing) ────────────────────────────────────── */

ANDROIDRT_EXPORT int32_t AInputQueue_getEvent(AInputQueue *queue, AInputEvent **outEvent) {
    AndroidInputQueue *q = asQueue(queue);
    struct AndroidInputEvent *slot;
    pthread_mutex_lock(&q->lock);
    slot = findSlotLocked(q, 1, -1, -1);
    if (slot) slot->state = 2; /* held — the app now owns this event */
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
    (void)queue; (void)event;
    /* real Android hands hot-plug/classifier-controlled keys here; nothing
     * on Tier 1 needs to swallow an event before the game sees it. */
    return 0;
}

ANDROIDRT_EXPORT void AInputQueue_finishEvent(AInputQueue *queue, AInputEvent *event, int handled) {
    AndroidInputQueue *q = asQueue(queue);
    struct AndroidInputEvent *e = (struct AndroidInputEvent *)event;
    (void)handled;
    if (!e) return;
    pthread_mutex_lock(&q->lock);
    if (e >= g_queue.slots && e < g_queue.slots +
        (int)(sizeof(g_queue.slots) / sizeof(g_queue.slots[0]))) {
        memset(e, 0, sizeof(*e));
    }
    pthread_mutex_unlock(&q->lock);
}

ANDROIDRT_EXPORT void AInputQueue_attachLooper(AInputQueue *queue, ALooper *looper, int ident,
                                               ALooper_callbackFunc callback, void *data) {
    (void)queue; (void)looper; (void)ident; (void)callback; (void)data;
    /* The glue calls this to receive an input fd on its looper; the Tier 1
     * ALooper instead checks android_inputHasEvents before sleeping
     * (androidshim.c), so nothing to attach. */
}

ANDROIDRT_EXPORT void AInputQueue_detachLooper(AInputQueue *queue) {
    (void)queue;
}

/* ── producer hooks (evdev.c on-device; probe in tests/smoke) ───────── */

void android_inputInjectKey(int32_t deviceId, int32_t source, int32_t action,
                            int32_t keyCode, int32_t repeat) {
    struct AndroidInputEvent *slot;
    if (!g_queue.alive) return;
    pthread_mutex_lock(&g_queue.lock);
    slot = takeFreeSlot(&g_queue);
    if (slot) {
        memset(slot, 0, sizeof(*slot));
        slot->type = AINPUT_EVENT_TYPE_KEY;
        slot->deviceId = deviceId;
        slot->source = source ? source : 0x00000401 /* SOURCE_GAMEPAD */;
        slot->state = 1;
        slot->keyAction = action;
        slot->keyCode = keyCode;
        slot->repeatCount = repeat;
    }
    pthread_mutex_unlock(&g_queue.lock);
    android_wakeLooper();
}

void android_inputInjectAxis(int32_t deviceId, int32_t source, int32_t axis, float value) {
    struct AndroidInputEvent *pending;
    if (!g_queue.alive) return;
    if (axis < 0 || axis >= INPUT_AXIS_SLOT_COUNT) return;
    pthread_mutex_lock(&g_queue.lock);
    /* Merge into an already-queued-but-unread motion event of the same
     * stick — exactly how Android coalesces joystick updates so a 60 Hz
     * reader never buries the queue in per-axis MOVE events. */
    pending = findSlotLocked(&g_queue, 1, AINPUT_EVENT_TYPE_MOTION, deviceId);
    if (pending) {
        pending->axes[axis] = value;
    } else {
        struct AndroidInputEvent *slot = takeFreeSlot(&g_queue);
        if (slot) {
            memset(slot, 0, sizeof(*slot));
            slot->type = AINPUT_EVENT_TYPE_MOTION;
            slot->deviceId = deviceId;
            slot->source = source ? source : 0x01000101 /* SOURCE_JOYSTICK */;
            slot->state = 1;
            slot->motionAction = 2 /* AMOTION_EVENT_ACTION_MOVE */;
            slot->axes[axis] = value;
        }
    }
    pthread_mutex_unlock(&g_queue.lock);
    android_wakeLooper();
}
