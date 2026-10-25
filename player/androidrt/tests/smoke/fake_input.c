/* fake_input.c — the smoke-test probe for AndroidRT's input shim.
 *
 * No /dev/input required: it drives android_inputInject* — the same
 * producer hooks the device's evdev reader (evdev.c) feeds — and asserts
 * the queue semantics the NDK contract promises a NativeActivity game:
 *
 *   a fresh queue is empty (getEvent = -EWOULDBLOCK, hasEvents = 0)
 *   a key event arrives with action/code/source intact
 *   preDispatchEvent never swallows, finishEvent frees the slot
 *   joystick reads coalesce while pending (one MOVE per app poll)
 *   an event the app holds is never overwritten by later injections
 *   ALooper_pollAll reports LOOPER_ID_INPUT with the queue as user data
 *     while input is pending, and stops reporting when it is drained
 *
 * PASS means every assertion held; any mismatch prints and fails the run.
 */

#include <stdio.h>
#include <string.h>

#include "actbridge.h"

/* ANativeActivity_finish in androidshim.c calls runner_requestExit; the
 * probe's main replaces the runner, so a stub stands in for it. */
void runner_requestExit(ActivityRunner *runner, int code) { (void)runner; (void)code; }

static int g_failures;

static void check(int ok, const char *what, float got, float want) {
    if (ok)
        fprintf(stderr, "[input-probe] ok: %s\n", what);
    else {
        fprintf(stderr, "[input-probe] FAIL: %s (got %f, want %f)\n", what, got, want);
        g_failures++;
    }
}

#define CHECK(cond, what) check(!!(cond), (what), 0, 0)
#define CHECKF(expr, want, what) do { \
    float _v = (expr); check(_v == (want), (what), _v, (float)(want)); \
} while (0)

int main(void) {
    AInputQueue *q = android_inputQueueCreate();
    AInputEvent *ev = NULL;
    void *looperData = NULL;
    int ident, rc;

    CHECK(q != NULL, "queue created");
    CHECK(AInputQueue_hasEvents(q) == 0, "fresh queue is empty");
    CHECK(AInputQueue_getEvent(q, &ev) == -11, "empty queue: getEvent = -EWOULDBLOCK");

    /* ── key path: a gamepad A press (AKEY_EVENT_ACTION_DOWN = 0,
     *    AKEYCODE_BUTTON_A = 96, AINPUT_SOURCE_GAMEPAD = 0x401) ────── */
    android_inputInjectKey(0, 0x00000401, 0 /*down*/, 96, 0);
    CHECK(AInputQueue_hasEvents(q) == 1, "key injection queues an event");

    ident = ALooper_pollAll(0, NULL, NULL, &looperData);
    CHECK(ident == 2 /* LOOPER_ID_INPUT */, "pollAll reports LOOPER_ID_INPUT");
    CHECK(looperData == q, "pollAll delivers the queue as user data");

    rc = AInputQueue_getEvent(q, &ev);
    CHECKF(AInputEvent_getType(ev), 1 /* AINPUT_EVENT_TYPE_KEY */, "event type is KEY");
    CHECKF(AKeyEvent_getAction(ev), 0, "key action is down");
    CHECKF(AKeyEvent_getKeyCode(ev), 96, "key code is BUTTON_A");
    CHECKF(AInputEvent_getSource(ev), 0x00000401, "source is gamepad");
    CHECK(AInputQueue_preDispatchEvent(q, ev) == 0, "dispatch not swallowed");
    AInputQueue_finishEvent(q, ev, 1);
    CHECK(AInputQueue_hasEvents(q) == 0, "finishEvent frees the slot");

    ident = ALooper_pollAll(0, NULL, NULL, &looperData);
    CHECK(ident != 2, "drained queue leaves LOOPER_ID_INPUT");

    /* ── motion path: a joystick push (AMotionEvent coalescing while
     *    pending; source defaults to AINPUT_SOURCE_JOYSTICK = 0x01000101) */
    android_inputInjectAxis(0, 0, 0 /*AXIS_X*/, 0.5f);
    android_inputInjectAxis(0, 0, 0, 0.9f); /* cadence before the app polls */
    rc = AInputQueue_getEvent(q, &ev);
    CHECK(rc == 0, "axis update queues a motion event");
    CHECKF(AInputEvent_getType(ev), 2 /* AINPUT_EVENT_TYPE_MOTION */, "event type is MOTION");
    CHECKF(AMotionEvent_getAxisValue(ev, 0, 0), 0.9f, "pending axis updates coalesce");
    CHECKF(AMotionEvent_getPointerCount(ev), 1, "gamepad reads as one pointer");
    CHECKF(AMotionEvent_getX(ev, 0), 0.9f, "getX mirrors AXIS_X");
    CHECKF(AInputEvent_getSource(ev), 0x01000101, "source is joystick");
    AInputQueue_finishEvent(q, ev, 1);

    /* ── hold invariant: an event the app is reading never mutates ── */
    android_inputInjectAxis(0, 0, 1 /*AXIS_Y*/, -0.5f);
    rc = AInputQueue_getEvent(q, &ev); /* app holds it now */
    CHECK(rc == 0, "held event delivered");
    android_inputInjectAxis(0, 0, 1, 1.0f); /* must land in a new slot */
    CHECKF(AMotionEvent_getAxisValue(ev, 1, 0), -0.5f, "held event never overwritten");
    AInputQueue_finishEvent(q, ev, 1);

    /* ── teardown no-ops that glue calls every launch ──────────────── */
    AInputQueue_preDispatchEvent(q, NULL);
    AInputQueue_finishEvent(q, NULL, 0);
    AInputQueue_attachLooper(q, NULL, 2, NULL, NULL);
    AInputQueue_detachLooper(q);
    AInputQueue_hasEvents(NULL);
    AInputQueue_getEvent(NULL, NULL);
    fprintf(stderr, "[input-probe] edge calls (NULL args, attach/detach) survived\n");

    android_inputQueueDestroy(q);
    fprintf(stderr, "[input-probe] %s\n", g_failures ? "FAILED" : "all assertions held");
    return g_failures ? 1 : 0;
}
