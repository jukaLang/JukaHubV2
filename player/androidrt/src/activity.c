/* activity.c — the NativeActivity lifecycle driver.
 *
 * After runner.c has dlopen'ed the game's .so and resolved
 * ANativeActivity_onCreate, this file owns everything that pretends to be
 * the Android framework: the ANativeActivity struct, callback ordering, and
 * the event loop around it. The event loop matters because NDK glue
 * (android_native_app_glue, which nearly every NativeActivity game embeds)
 * calls ALooper_pollAll in a tight loop and treats ALOOPER_POLL_WAKEUP /
 * CALLBACK / TIMEOUT specially — so the ALooper shim (see androidshim.c)
 * and this loop must agree on who waits.
 *
 * Lifecycle driven here, in the only order Android guarantees:
 *   onCreate(savedState=NULL) → onStart → onResume →
 *   focused → windowCreated → (app loop) → windowDestroyed →
 *   unfocused → onPause → onSaveInstanceState → onStop → onDestroy
 */

#define _POSIX_C_SOURCE 200809L
#include <pthread.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "actbridge.h"
#include "dlport.h"

/* Optional smoke hook: ANDROIDRT_SMOKE_FRAMES=N makes the loop exit after N
 * iterations without needing the game to call ANativeActivity_finish. Set by
 * the smoke test host-side; unset on the device in normal play. */

/* Window events the app loop polls (see actbridge.h). */
#define APP_EVENT_WINDOW_CREATE 1
#define APP_EVENT_WINDOW_RESIZE 2
#define APP_EVENT_WINDOW_DESTROY 3
#define APP_EVENT_EXIT 4

/* ── runner→app queue ────────────────────────────────────────────────── */

struct RunnerEvent {
    int event;
    struct RunnerEvent *next;
};

static struct {
    pthread_mutex_t lock;
    pthread_cond_t cond;
    struct RunnerEvent *head, *tail;
} g_events;

static void runner_queue_init(void) {
    pthread_mutex_init(&g_events.lock, NULL);
    pthread_cond_init(&g_events.cond, NULL);
}

static void runner_post(int event) {
    struct RunnerEvent *e = malloc(sizeof(*e));
    if (!e) return;
    e->event = event;
    pthread_mutex_lock(&g_events.lock);
    if (g_events.tail) g_events.tail->next = e;
    else g_events.head = e;
    g_events.tail = e;
    e->next = NULL;
    pthread_cond_signal(&g_events.cond);
    pthread_mutex_unlock(&g_events.lock);
}

/* non-blocking pull; returns 0 when empty */
static int runner_poll(void) {
    int event = 0;
    pthread_mutex_lock(&g_events.lock);
    if (g_events.head) {
        struct RunnerEvent *e = g_events.head;
        g_events.head = e->next;
        if (!g_events.head) g_events.tail = NULL;
        event = e->event;
        free(e);
    }
    pthread_mutex_unlock(&g_events.lock);
    return event;
}

/* ── callbacks structs ───────────────────────────────────────────────── */

/* Lifecycle trace lines land in the profile's stderr log, the same place
 * the game's __android_log_* output goes, so a failed launch documents
 * exactly how far the framework got. */
#define LIFECYCLE_TRACE 1

static void cb_onStart(ANativeActivity *a) {
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onStart\n");
#endif
    (void)a;
}
static void cb_onResume(ANativeActivity *a) {
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onResume\n");
#endif
    (void)a;
}
static void *cb_onSaveInstanceState(ANativeActivity *a, size_t *outSize) {
    (void)a;
    if (outSize) *outSize = 0; /* no state to save yet */
    return NULL;
}
static void cb_onPause(ANativeActivity *a) {
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onPause\n");
#endif
    (void)a;
}
static void cb_onStop(ANativeActivity *a) {
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onStop\n");
#endif
    (void)a;
}
static void cb_onDestroy(ANativeActivity *a) {
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onDestroy\n");
#endif
    (void)a;
}
static void cb_onWindowFocus(ANativeActivity *a, int hasFocus) { (void)a; (void)hasFocus; }
static void cb_onWindowCreated(ANativeActivity *a, ANativeWindow *w) {
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onNativeWindowCreated %dx%d\n",
            ANativeWindow_getWidth(w), ANativeWindow_getHeight(w));
#endif
    android_activitySetWindow(a, w);
}
static void cb_onWindowResized(ANativeActivity *a, ANativeWindow *w) { (void)a; (void)w; }
static void cb_onWindowRedrawNeeded(ANativeActivity *a, ANativeWindow *w) { (void)a; (void)w; }
static void cb_onWindowDestroyed(ANativeActivity *a, ANativeWindow *w) {
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onNativeWindowDestroyed\n");
#endif
    android_activityClearWindow(a, w);
}
static void cb_onInputQueueCreated(ANativeActivity *a, AInputQueue *q) { (void)a; (void)q; }
static void cb_onInputQueueDestroyed(ANativeActivity *a, AInputQueue *q) { (void)a; (void)q; }
static void cb_onContentRectChanged(ANativeActivity *a, const ARect *r) { (void)a; (void)r; }
static void cb_onConfigurationChanged(ANativeActivity *a) { (void)a; }
static void cb_onLowMemory(ANativeActivity *a) { (void)a; }static ANativeActivityCallbacks g_callbacks = {
    .onStart                   = cb_onStart,
    .onResume                  = cb_onResume,
    .onSaveInstanceState       = cb_onSaveInstanceState,
    .onPause                   = cb_onPause,
    .onStop                    = cb_onStop,
    .onDestroy                 = cb_onDestroy,
    .onWindowFocusChanged      = cb_onWindowFocus,
    .onNativeWindowCreated     = cb_onWindowCreated,
    .onNativeWindowResized     = cb_onWindowResized,
    .onNativeWindowRedrawNeeded = cb_onWindowRedrawNeeded,
    .onNativeWindowDestroyed   = cb_onWindowDestroyed,
    .onInputQueueCreated       = cb_onInputQueueCreated,
    .onInputQueueDestroyed     = cb_onInputQueueDestroyed,
    .onContentRectChanged      = cb_onContentRectChanged,
    .onConfigurationChanged    = cb_onConfigurationChanged,
    .onLowMemory               = cb_onLowMemory,
};

/* android_native_app_glue (and most games) replace activity->callbacks
 * with their own table inside onCreate — the NDK contract is that
 * dispatch goes through the table the activity carries, not the runner's
 * stubs. liveCallbacks is that rule: the game's table when one is
 * installed, the runner's otherwise. Entries the game left NULL are
 * skipped on dispatch, exactly as the real framework skips them. */
static struct ANativeActivityCallbacks *liveCallbacks(ANativeActivity *activity) {
    if (activity->callbacks && activity->callbacks != &g_callbacks)
        return activity->callbacks;
    return &g_callbacks;
}

/* Window delivery carries runner plumbing (the shim's global window and
 * its trace line) no matter whose table is live — the shim's tracking
 * must not depend on a real game re-implementing the framework. */
static void deliverWindowCreated(ANativeActivity *activity, ANativeWindow *window) {
    struct ANativeActivityCallbacks *cb = liveCallbacks(activity);
    if (cb == &g_callbacks) {
        /* the runner's own stub already traces and plumbs */
        cb->onNativeWindowCreated(activity, window);
        return;
    }
    android_activitySetWindow(activity, window); /* plumb regardless */
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onNativeWindowCreated dispatched to game callbacks\n");
#endif
    if (cb->onNativeWindowCreated) cb->onNativeWindowCreated(activity, window);
}

static void deliverWindowDestroyed(ANativeActivity *activity, ANativeWindow *window) {
    struct ANativeActivityCallbacks *cb = liveCallbacks(activity);
    if (cb == &g_callbacks) {
        cb->onNativeWindowDestroyed(activity, window); /* stub traces and clears */
        return;
    }
#ifdef LIFECYCLE_TRACE
    fprintf(stderr, "[androidrt] lifecycle: onNativeWindowDestroyed\n");
#endif
    if (!window) return;
    android_activityClearWindow(activity, window);
    if (cb->onNativeWindowDestroyed) cb->onNativeWindowDestroyed(activity, window);
}

/* runOneActivity calls the game's ANativeActivity_onCreate, walks the
 * lifecycle, and pumps the runner event queue until the app asks to exit
 * (ActivityFinish from the android shim) or the window goes away. */
static int smokeFrameLimit(void) {
    const char *v = getenv("ANDROIDRT_SMOKE_FRAMES");
    return v && *v ? atoi(v) : 0;
}

void runOneActivity(ActivityRunner *runner, void *onCreateVoid, void *gameHandle) {
    int smokeLimit = smokeFrameLimit();
    int iteration = 0;
    (void)gameHandle;
    ANativeActivity_onCreate_t onCreate = (ANativeActivity_onCreate_t)onCreateVoid;
    struct ANativeActivityCallbacks *cb;

    ANativeActivity activity;
    memset(&activity, 0, sizeof(activity));
    activity.callbacks = &g_callbacks;
    activity.vm = NULL;          /* honest: there is no JVM in Tier 1 */
    activity.env = NULL;
    activity.clazz = NULL;
    activity.internalDataPath = runner->stagedRoot;
    activity.externalDataPath = runner->stagedRoot;
    activity.sdkVersion = ANDROIDRT_SDK_VERSION;
    activity.instance = runner;
    activity.assetManager = android_openAssetManager(runner->stagedRoot);
    activity.obbPath = "";

    runner_queue_init();
    android_activityInit(&activity, runner);

    onCreate(&activity, NULL, 0);

    /* Optional per-frame hook: only the fake smoke game defines it, real
     * games drive their own loop through ALooper_pollAll instead. */
    typedef void (*frame_hook_t)(ANativeActivity *);
    frame_hook_t onFrame = (frame_hook_t)rt_dlsym(gameHandle, "ANativeActivity_onFrame");

    cb = liveCallbacks(&activity);
    if (cb->onStart) cb->onStart(&activity);
    cb = liveCallbacks(&activity);
    if (cb->onResume) cb->onResume(&activity);

    /* The queue exists by the time the game is resumed, which is the
     * ordering android_native_app_glue relies on: it attaches (our
     * no-op attach) and then drains through ALooper ident 2. Creation
     * above the game's first frame also means activity->inputQueue is
     * never NULL for games that read the field directly. */
    AInputQueue *queue = android_inputQueueCreate();
    if (queue) {
        activity.inputQueue = queue;
        cb = liveCallbacks(&activity);
        if (cb->onInputQueueCreated) cb->onInputQueueCreated(&activity, queue);
    }

    if (g_callbacks.onWindowFocusChanged) g_callbacks.onWindowFocusChanged(&activity, 1);

    /* The window is the biggest open question of Tier 1 (surface via the
     * PowerVR EGL path). Between now and its implementation the game is
     * driven without a surface, which is already enough to measure which
     * bionic symbols the library needs. */
    if (g_callbacks.onWindowFocusChanged) g_callbacks.onWindowFocusChanged(&activity, 1);

    /* The window budget of Tier 1 is one quad-sized stub from
     * android_windowBackend (PowerVR EGL is the next milestone). Size
     * comes from ANDROIDRT_WINDOW=WxH, defaulting to the device panel. */
    int winW = 1280, winH = 720;
    const char *winEnv = getenv("ANDROIDRT_WINDOW");
    int parsedW = 0, parsedH = 0;
    if (winEnv && sscanf(winEnv, "%dx%d", &parsedW, &parsedH) == 2 &&
        parsedW > 0 && parsedH > 0) {
        winW = parsedW;
        winH = parsedH;
    }
    fprintf(stderr, "[androidrt] lifecycle: window %dx%d\n", winW, winH);
    android_windowBackend(winW, winH);
    runner_post(APP_EVENT_WINDOW_CREATE);

    while (runner->running) {
        if (smokeLimit > 0 && ++iteration > smokeLimit) {
            fprintf(stderr, "[androidrt] smoke: %d frames driven, ending run\n", smokeLimit);
            runner_requestExit(runner, 0);
        }
        int event = runner_poll();
        switch (event) {
            case APP_EVENT_WINDOW_CREATE:
                deliverWindowCreated(&activity, android_currentWindow());
                break;
            case APP_EVENT_WINDOW_RESIZE:
                cb = liveCallbacks(&activity);
                if (cb->onNativeWindowResized)
                    cb->onNativeWindowResized(&activity, android_currentWindow());
                break;
            case APP_EVENT_WINDOW_DESTROY:
                deliverWindowDestroyed(&activity, android_currentWindow());
                break;
            case APP_EVENT_EXIT:
                runner->running = 0;
                runner->exitCode = 0;
                break;
            default:
                /* No runner event: let the ALooper shim wait briefly so a
                 * thread-affected game does not spin. */
                android_pollOnce(16 /*ms*/);
                break;
        }
        if (onFrame) onFrame(&activity);
    }

    cb = liveCallbacks(&activity);
    if (cb->onWindowFocusChanged) cb->onWindowFocusChanged(&activity, 0);
    if (onFrame) onFrame(&activity); /* final frame */
    deliverWindowDestroyed(&activity, android_currentWindow());
    /* The queue dies with the pause, in the NDK's pause order: window
     * surface, then input queue, then the app is told it is backgrounded. */
    if (activity.inputQueue) {
        cb = liveCallbacks(&activity);
        if (cb->onInputQueueDestroyed)
            cb->onInputQueueDestroyed(&activity, activity.inputQueue);
        android_inputQueueDestroy(activity.inputQueue);
        activity.inputQueue = NULL;
    }
    cb = liveCallbacks(&activity);
    if (cb->onPause) cb->onPause(&activity);
    cb = liveCallbacks(&activity);
    if (cb->onStop) cb->onStop(&activity);
    cb = liveCallbacks(&activity);
    if (cb->onDestroy) cb->onDestroy(&activity);
}

/* ActivityFinish and friends, called from the game via the android shim. */
void runner_requestExit(ActivityRunner *runner, int code) {
    if (runner) {
        runner->exitCode = code;
        runner_post(APP_EVENT_EXIT);
    }
}

void runner_deliverWindow(ActivityRunner *runner, int create) {
    runner_post(create ? APP_EVENT_WINDOW_CREATE : APP_EVENT_WINDOW_DESTROY);
}
