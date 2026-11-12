/* activity.c — the NativeActivity lifecycle driver.
 *
 * After runner.c has loaded the game's library and resolved
 * ANativeActivity_onCreate, this file owns everything that pretends to be
 * the Android framework: the ANativeActivity struct, callback ordering and
 * the main-thread loop around it.
 *
 * The callbacks table follows the NDK contract: the framework hands the game
 * a zeroed ANativeActivityCallbacks and the game fills in what it wants —
 * android_native_app_glue writes INTO activity->callbacks, others replace
 * the pointer. Either way dispatch goes through activity->callbacks, a NULL
 * entry is skipped, and the runner's own plumbing (the shim's current
 * window, the trace lines in the launch log) never depends on the table.
 *
 * Order, as Android's NativeActivity drives it:
 *   onCreate(savedState=NULL) → onStart → onResume → onInputQueueCreated →
 *   onNativeWindowCreated → onWindowFocusChanged(1) → ... game runs ... →
 *   onWindowFocusChanged(0) → onPause → onSaveInstanceState → onStop →
 *   onNativeWindowDestroyed → onInputQueueDestroyed → onDestroy
 * With the glue each callback blocks until the game thread has consumed the
 * matching APP_CMD_*, which is why looper.c must poll real fds.
 */

#define _POSIX_C_SOURCE 200809L
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "actbridge.h"
#include "device.h"
#include "jni.h"

/* Smoke hooks (host tests only; unset on the device):
 *   ANDROIDRT_SMOKE_FRAMES=N  end the run after N loop iterations;
 *   ANDROIDRT_SMOKE_INPUT=1   inject BUTTON_A down/up after the window is up,
 *                             through the same producer hooks evdev.c feeds. */

#define APP_EVENT_WINDOW_CREATE  1
#define APP_EVENT_WINDOW_RESIZE  2
#define APP_EVENT_WINDOW_DESTROY 3
#define APP_EVENT_EXIT           4

/* ── runner→main-loop queue ──────────────────────────────────────────── */

struct RunnerEvent {
    int event;
    struct RunnerEvent *next;
};

static struct {
    pthread_mutex_t lock;
    struct RunnerEvent *head, *tail;
} g_events = { PTHREAD_MUTEX_INITIALIZER, NULL, NULL };

static void runner_post(int event) {
    struct RunnerEvent *e = (struct RunnerEvent *)malloc(sizeof(*e));
    if (!e) return;
    e->event = event;
    e->next = NULL;
    pthread_mutex_lock(&g_events.lock);
    if (g_events.tail) g_events.tail->next = e;
    else g_events.head = e;
    g_events.tail = e;
    pthread_mutex_unlock(&g_events.lock);
    android_wakeLooper();
}

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

/* The GameActivity bridge (gameactivity.c) runs its own loop on this queue. */
int runner_nextEvent(void);
int runner_nextEvent(void) { return runner_poll(); }

/* ── dispatch ────────────────────────────────────────────────────────── */

/* The framework-owned table the game fills in (zeroed, like Android's). */
static ANativeActivityCallbacks g_callbacks;
static const ANativeActivityCallbacks g_none;

static const ANativeActivityCallbacks *cbs(const ANativeActivity *a) {
    return a->callbacks ? a->callbacks : &g_none;
}

static void trace(const char *what) { fprintf(stderr, "[androidrt] lifecycle: %s\n", what); }

static void deliverWindowCreated(ANativeActivity *activity, ANativeWindow *window) {
    if (!window) return;
    android_activitySetWindow(activity, window); /* plumbing first, always */
    fprintf(stderr, "[androidrt] lifecycle: onNativeWindowCreated %dx%d\n", ANativeWindow_getWidth(window),
            ANativeWindow_getHeight(window));
    if (cbs(activity)->onNativeWindowCreated) cbs(activity)->onNativeWindowCreated(activity, window);
    activity->contentRect.left = 0;
    activity->contentRect.top = 0;
    activity->contentRect.right = ANativeWindow_getWidth(window);
    activity->contentRect.bottom = ANativeWindow_getHeight(window);
    if (cbs(activity)->onContentRectChanged) cbs(activity)->onContentRectChanged(activity, &activity->contentRect);
}

static void deliverWindowDestroyed(ANativeActivity *activity, ANativeWindow *window) {
    if (!window) return;
    trace("onNativeWindowDestroyed");
    if (cbs(activity)->onNativeWindowDestroyed) cbs(activity)->onNativeWindowDestroyed(activity, window);
    android_activityClearWindow(activity, window);
}

static int envInt(const char *name) {
    const char *v = getenv(name);
    return v && *v ? atoi(v) : 0;
}

void runOneActivity(ActivityRunner *runner, void *onCreateVoid, void *onFrameVoid) {
    typedef void (*frame_hook_t)(ANativeActivity *);
    ANativeActivity_onCreate_t onCreate = (ANativeActivity_onCreate_t)onCreateVoid;
    frame_hook_t onFrame = (frame_hook_t)onFrameVoid;
    int smokeLimit = envInt("ANDROIDRT_SMOKE_FRAMES");
    int smokeInput = envInt("ANDROIDRT_SMOKE_INPUT");
    int iteration = 0, focused = 0;
    ANativeActivity activity;
    AInputQueue *queue;
    int winW = 1280, winH = 720;

    /* Android's UI thread already owns an ALooper before NativeActivity's
     * onCreate. Rust's android-activity and other NDK glue query it with
     * ALooper_forThread during onCreate and abort when it is absent. */
    if (!ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS)) {
        fprintf(stderr, "[androidrt] lifecycle: could not prepare the main thread looper\n");
        runner->exitCode = 3;
        return;
    }

    memset(&g_callbacks, 0, sizeof g_callbacks);
    memset(&activity, 0, sizeof(activity));
    activity.callbacks = &g_callbacks;
    /* No JVM in Tier 1, but a JNI environment (jnienv.c): the activity
     * object is the manifest's activity class (or a class pack's), and every
     * Java member nobody implements answers with a default and a log line
     * (ANDROIDPAK_JNI=off restores NULL). */
    activity.vm = apak_jniVM();
    activity.env = apak_jniEnv();
    activity.clazz = apak_jniActivityObject();
    activity.internalDataPath = runner->internalDataPath ? runner->internalDataPath : runner->stagedRoot;
    activity.externalDataPath = runner->externalDataPath ? runner->externalDataPath : runner->stagedRoot;
    activity.sdkVersion = ANDROIDRT_SDK_VERSION;
    activity.instance = NULL; /* the game's own pointer slot */
    activity.assetManager = android_openAssetManager(runner->stagedRoot);
    activity.obbPath = runner->obbPath ? runner->obbPath : "";

    android_activityInit(&activity, runner);

    apak_jniEvent(APAK_JNI_EV_BEFORE_ONCREATE);
    trace("onCreate");
#if defined(__linux__)
    if (runner->finishOnPrimaryThreadExit) apak_bionicCapturePrimaryThread(runner);
#endif
    onCreate(&activity, NULL, 0);
#if defined(__linux__)
    if (runner->finishOnPrimaryThreadExit) apak_bionicEndPrimaryCapture();
#endif
    apak_jniExceptionReport(activity.env, "onCreate");
    apak_jniEvent(APAK_JNI_EV_AFTER_ONCREATE);

    trace("onStart");
    if (cbs(&activity)->onStart) cbs(&activity)->onStart(&activity);
    trace("onResume");
    if (cbs(&activity)->onResume) cbs(&activity)->onResume(&activity);
    apak_jniEvent(APAK_JNI_EV_RESUME);

    /* The queue exists before the window, the order the glue relies on (it
     * attaches the queue to its looper when APP_CMD_INPUT_CHANGED arrives). */
    queue = android_inputQueueCreate();
    if (queue) {
        activity.inputQueue = queue;
        trace("onInputQueueCreated");
        if (cbs(&activity)->onInputQueueCreated) cbs(&activity)->onInputQueueCreated(&activity, queue);
    }

    apak_devicePanel(&winW, &winH); /* ANDROIDRT_WINDOW, ANDROIDPAK_PANEL, the measured panel */
    fprintf(stderr, "[androidrt] lifecycle: window %dx%d\n", winW, winH);
    android_windowBackend(winW, winH);
    runner_post(APP_EVENT_WINDOW_CREATE);

    while (runner->running) {
        int event;
        if (smokeLimit > 0 && ++iteration > smokeLimit) {
            fprintf(stderr, "[androidrt] smoke: %d frames driven, ending run\n", smokeLimit);
            runner_requestExit(runner, 0);
            smokeLimit = 0;
        }
        if (smokeInput && iteration == 4) {
            fprintf(stderr, "[androidrt] smoke: injecting BUTTON_A down/up\n");
            android_inputInjectKey(0, AINPUT_SOURCE_GAMEPAD, AKEY_EVENT_ACTION_DOWN, AKEYCODE_BUTTON_A, 0);
            android_inputInjectKey(0, AINPUT_SOURCE_GAMEPAD, AKEY_EVENT_ACTION_UP, AKEYCODE_BUTTON_A, 0);
            smokeInput = 0;
        }
        apak_jniPump(); /* Java-side callbacks posted to the main thread */
        event = runner_poll();
        switch (event) {
            case APP_EVENT_WINDOW_CREATE:
                deliverWindowCreated(&activity, android_currentWindow());
                if (!focused) {
                    trace("onWindowFocusChanged(1)");
                    if (cbs(&activity)->onWindowFocusChanged) cbs(&activity)->onWindowFocusChanged(&activity, 1);
                    focused = 1;
                }
                break;
            case APP_EVENT_WINDOW_RESIZE:
                if (cbs(&activity)->onNativeWindowResized)
                    cbs(&activity)->onNativeWindowResized(&activity, android_currentWindow());
                break;
            case APP_EVENT_WINDOW_DESTROY:
                deliverWindowDestroyed(&activity, android_currentWindow());
                break;
            case APP_EVENT_EXIT:
                runner->running = 0;
                break;
            default:
                /* Nothing for the framework thread to do: sleep until woken
                 * (finish, a window event) or the next smoke tick. */
                android_pollOnce(16);
                break;
        }
        if (onFrame) onFrame(&activity);
    }

#if defined(__linux__)
    if (runner->finishOnPrimaryThreadExit) apak_bionicStopWatchingPrimaryThread();
#endif
    if (__atomic_load_n(&runner->primaryThreadExited, __ATOMIC_ACQUIRE)) {
        /* The game's android_main has returned. NDK glue lifecycle callbacks
         * post commands to that thread and wait for it, so calling them now
         * would hang forever. The runner exits and the OS reclaims resources. */
        fprintf(stderr, "[androidrt] lifecycle: game thread ended; returning to launcher\n");
        return;
    }

    if (focused) {
        trace("onWindowFocusChanged(0)");
        if (cbs(&activity)->onWindowFocusChanged) cbs(&activity)->onWindowFocusChanged(&activity, 0);
    }
    if (onFrame) onFrame(&activity); /* final frame */
    trace("onPause");
    if (cbs(&activity)->onPause) cbs(&activity)->onPause(&activity);
    apak_jniEvent(APAK_JNI_EV_PAUSE);
    if (cbs(&activity)->onSaveInstanceState) {
        size_t size = 0;
        void *saved = cbs(&activity)->onSaveInstanceState(&activity, &size);
        free(saved); /* no process restarts to hand it back to */
    }
    trace("onStop");
    if (cbs(&activity)->onStop) cbs(&activity)->onStop(&activity);
    deliverWindowDestroyed(&activity, android_currentWindow());
    if (activity.inputQueue) {
        trace("onInputQueueDestroyed");
        if (cbs(&activity)->onInputQueueDestroyed) cbs(&activity)->onInputQueueDestroyed(&activity, activity.inputQueue);
        android_inputQueueDestroy(activity.inputQueue);
        activity.inputQueue = NULL;
    }
    apak_jniEvent(APAK_JNI_EV_DESTROY);
    trace("onDestroy");
    if (cbs(&activity)->onDestroy) cbs(&activity)->onDestroy(&activity);
}

/* ANativeActivity_finish (androidshim.c), the smoke frame limit and the
 * runner's own shutdown all end the run here. */
void runner_requestExit(ActivityRunner *runner, int code) {
    if (runner) {
        runner->exitCode = code;
        runner_post(APP_EVENT_EXIT);
    }
}
