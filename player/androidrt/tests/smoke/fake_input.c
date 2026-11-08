/* fake_input.c — the smoke probe for AndroidRT's input queue and ALooper.
 *
 * No /dev/input required: it drives android_inputInject* — the same
 * producer hooks the device's evdev reader (evdev.c) feeds — and asserts the
 * NDK contract a NativeActivity game relies on:
 *
 *   ALooper: addFd with ident+data returns that ident/fd/events/data from
 *     pollOnce; removeFd; callbacks run inside the poll and pollAll keeps
 *     going; ALooper_wake from another thread returns ALOOPER_POLL_WAKE;
 *     timeouts return ALOOPER_POLL_TIMEOUT (-3);
 *   AInputQueue: empty = -EWOULDBLOCK; attachLooper makes pollAll report
 *     the attached ident with the attached DATA while events are queued
 *     (android_native_app_glue's &inputPollSource) and stop once drained;
 *     key fields intact; axis updates coalesce while unread; an event the
 *     app holds is never overwritten; detachLooper stops the wakeups.
 */
#define _GNU_SOURCE
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "actbridge.h"

/* androidshim.c's ANativeActivity_finish calls runner_requestExit; the
 * probe replaces the runner, so a stub stands in for it. */
void runner_requestExit(ActivityRunner *runner, int code) {
    (void)runner;
    (void)code;
}

#define ALOOPER_POLL_WAKE     (-1)
#define ALOOPER_POLL_CALLBACK (-2)
#define ALOOPER_POLL_TIMEOUT  (-3)

static int g_failures;

static void check(int ok, const char *what) {
    fprintf(stderr, "[input-probe] %s: %s\n", ok ? "ok" : "FAIL", what);
    if (!ok) g_failures++;
}

static int g_callbacks;
static int onFdReady(int fd, int events, void *data) {
    char c;
    (void)events;
    (void)data;
    if (read(fd, &c, 1) == 1) g_callbacks++;
    return 1; /* keep */
}

static void *waker(void *looper) {
    struct timespec ts = { 0, 30 * 1000 * 1000 };
    nanosleep(&ts, NULL);
    ALooper_wake((ALooper *)looper);
    return NULL;
}

static void testLooper(void) {
    ALooper *L = ALooper_prepare(1 /* ALLOW_NON_CALLBACKS */);
    int fds[2], cbfds[2];
    int outFd = -1, outEvents = 0, ident;
    void *outData = NULL;
    int marker = 0;
    pthread_t t;

    check(L != NULL && ALooper_forThread() == L, "ALooper_prepare returns the thread's looper");
    check(ALooper_pollOnce(0, NULL, NULL, NULL) == ALOOPER_POLL_TIMEOUT, "an idle poll times out with -3");
    if (pipe(fds) != 0 || pipe(cbfds) != 0) {
        check(0, "pipe");
        return;
    }
    check(ALooper_addFd(L, fds[0], 5, 1 /* INPUT */, NULL, &marker) == 1, "addFd with ident 5");
    check(write(fds[1], "x", 1) == 1, "write to the registered fd");
    ident = ALooper_pollOnce(100, &outFd, &outEvents, &outData);
    check(ident == 5 && outFd == fds[0] && (outEvents & 1) && outData == &marker,
          "pollOnce returns the fd's ident, fd, events and data");
    {
        char c;
        check(read(fds[0], &c, 1) == 1, "drain");
    }
    check(ALooper_removeFd(L, fds[0]) == 1 && ALooper_removeFd(L, fds[0]) == 0, "removeFd: 1 then 0");

    check(ALooper_addFd(L, cbfds[0], 99, 1, onFdReady, NULL) == 1, "addFd with a callback");
    check(write(cbfds[1], "y", 1) == 1, "write to the callback fd");
    check(ALooper_pollAll(50, NULL, NULL, NULL) == ALOOPER_POLL_TIMEOUT && g_callbacks == 1,
          "pollAll runs the callback inside the poll and keeps waiting");
    ALooper_removeFd(L, cbfds[0]);

    pthread_create(&t, NULL, waker, L);
    check(ALooper_pollOnce(-1, NULL, NULL, NULL) == ALOOPER_POLL_WAKE, "ALooper_wake from another thread");
    pthread_join(t, NULL);
    close(fds[0]);
    close(fds[1]);
    close(cbfds[0]);
    close(cbfds[1]);
}

static void testInput(void) {
    ALooper *L = ALooper_forThread();
    AInputQueue *q = android_inputQueueCreate();
    AInputEvent *ev = NULL;
    void *outData = NULL;
    int source = 0, ident, rc;

    check(q != NULL, "queue created");
    check(AInputQueue_hasEvents(q) == 0, "fresh queue is empty");
    check(AInputQueue_getEvent(q, &ev) == -11, "empty queue: getEvent = -EWOULDBLOCK");
    AInputQueue_attachLooper(q, L, 2 /* LOOPER_ID_INPUT */, NULL, &source);

    /* key path: a gamepad A press (ACTION_DOWN 0, BUTTON_A 96, SOURCE_GAMEPAD) */
    android_inputInjectKey(0, 0x00000401, 0, 96, 0);
    check(AInputQueue_hasEvents(q) == 1, "key injection queues an event");
    ident = ALooper_pollAll(0, NULL, NULL, &outData);
    check(ident == 2 && outData == &source, "pollAll reports LOOPER_ID_INPUT with the attached data");
    rc = AInputQueue_getEvent(q, &ev);
    check(rc == 0 && AInputEvent_getType(ev) == 1 && AKeyEvent_getAction(ev) == 0 && AKeyEvent_getKeyCode(ev) == 96 &&
              AInputEvent_getSource(ev) == 0x00000401,
          "key event fields intact");
    check(AInputQueue_preDispatchEvent(q, ev) == 0, "dispatch not swallowed");
    AInputQueue_finishEvent(q, ev, 1);
    check(AInputQueue_hasEvents(q) == 0, "finishEvent frees the slot");
    check(ALooper_pollAll(0, NULL, NULL, &outData) == ALOOPER_POLL_TIMEOUT, "a drained queue stops waking the looper");

    /* motion path: joystick pushes coalesce while unread */
    android_inputInjectAxis(0, 0, 0 /* AXIS_X */, 0.5f);
    android_inputInjectAxis(0, 0, 0, 0.9f);
    rc = AInputQueue_getEvent(q, &ev);
    check(rc == 0 && AInputEvent_getType(ev) == 2 && AMotionEvent_getAxisValue(ev, 0, 0) == 0.9f &&
              AMotionEvent_getPointerCount(ev) == 1 && AMotionEvent_getX(ev, 0) == 0.9f &&
              AInputEvent_getSource(ev) == 0x01000101,
          "axis updates coalesce into one joystick MOVE");
    AInputQueue_finishEvent(q, ev, 1);

    /* hold invariant: an event the app is reading never mutates */
    android_inputInjectAxis(0, 0, 1 /* AXIS_Y */, -0.5f);
    rc = AInputQueue_getEvent(q, &ev);
    android_inputInjectAxis(0, 0, 1, 1.0f);
    check(rc == 0 && AMotionEvent_getAxisValue(ev, 1, 0) == -0.5f, "a held event is never overwritten");
    AInputQueue_finishEvent(q, ev, 1);
    rc = AInputQueue_getEvent(q, &ev);
    check(rc == 0 && AMotionEvent_getAxisValue(ev, 1, 0) == 1.0f, "the later update arrived as its own event");
    AInputQueue_finishEvent(q, ev, 1);

    /* detach: events still queue, the looper is no longer woken */
    AInputQueue_detachLooper(q);
    android_inputInjectKey(0, 0, 1, 97, 0);
    check(AInputQueue_hasEvents(q) == 1 && ALooper_pollAll(0, NULL, NULL, NULL) == ALOOPER_POLL_TIMEOUT,
          "detachLooper stops LOOPER_ID_INPUT wakeups");
    AInputQueue_getEvent(q, &ev);
    AInputQueue_finishEvent(q, ev, 1);

    /* edge calls the glue makes */
    AInputQueue_preDispatchEvent(q, NULL);
    AInputQueue_finishEvent(q, NULL, 0);
    AInputQueue_hasEvents(NULL);
    AInputQueue_getEvent(NULL, NULL);
    android_inputQueueDestroy(q);
    check(1, "edge calls (NULL args, detach, destroy) survived");
}

int main(void) {
    testLooper();
    testInput();
    fprintf(stderr, "[input-probe] %s\n", g_failures ? "FAILED" : "all assertions held");
    return g_failures ? 1 : 0;
}
