/* fake_glue.c — a NativeActivity game built on the android_native_app_glue
 * contract, for the smoke test.
 *
 * Nearly every NDK game embeds android_native_app_glue.c. This file
 * re-implements its behaviour faithfully (same struct roles, same command
 * protocol, same blocking handshakes) so the runner is tested against what
 * real games do, not against a cooperative fake:
 *   - ANativeActivity_onCreate fills in activity->callbacks IN PLACE and
 *     starts the game thread, which ALooper_prepare()s and registers the
 *     read end of a command pipe with ALooper_addFd(LOOPER_ID_MAIN,
 *     data = &cmdPollSource);
 *   - every framework callback writes an APP_CMD_* byte and onStart/
 *     onResume/onPause/onStop, the window and the input queue BLOCK until
 *     the game thread has consumed it;
 *   - the input queue is attached to the game thread's looper with
 *     LOOPER_ID_INPUT and data = &inputPollSource;
 *   - onDestroy waits until android_main has returned.
 * android_main itself counts frames, checks the window, the configuration,
 * an AChoreographer frame callback and the injected BUTTON_A down/up, then
 * calls ANativeActivity_finish.
 *
 * Linked NDK-style against stub libc.so/liblog.so/libandroid.so like
 * fake_bionic.c, with bionic's LP64 pthread sizes declared by hand.
 */
#include <stddef.h>
#include <stdint.h>

typedef struct { int32_t p[10]; } bmutex_t;
typedef struct { int32_t p[12]; } bcond_t;
typedef unsigned long bpthread_t;
typedef struct {
    uint32_t flags;
    void *stack_base;
    size_t stack_size;
    size_t guard_size;
    int32_t sched_policy;
    int32_t sched_priority;
    char reserved[16];
} battr_t;
typedef struct { int32_t left, top, right, bottom; } ARect;

typedef struct ANativeActivityCallbacks ANativeActivityCallbacks;
typedef struct ANativeActivity {
    ANativeActivityCallbacks *callbacks;
    void *vm, *env, *clazz;
    const char *internalDataPath, *externalDataPath;
    int32_t sdkVersion;
    void *instance;
    void *assetManager;
    const char *obbPath;
} ANativeActivity;
struct ANativeActivityCallbacks {
    void (*onStart)(ANativeActivity *);
    void (*onResume)(ANativeActivity *);
    void *(*onSaveInstanceState)(ANativeActivity *, size_t *);
    void (*onPause)(ANativeActivity *);
    void (*onStop)(ANativeActivity *);
    void (*onDestroy)(ANativeActivity *);
    void (*onWindowFocusChanged)(ANativeActivity *, int);
    void (*onNativeWindowCreated)(ANativeActivity *, void *);
    void (*onNativeWindowResized)(ANativeActivity *, void *);
    void (*onNativeWindowRedrawNeeded)(ANativeActivity *, void *);
    void (*onNativeWindowDestroyed)(ANativeActivity *, void *);
    void (*onInputQueueCreated)(ANativeActivity *, void *);
    void (*onInputQueueDestroyed)(ANativeActivity *, void *);
    void (*onContentRectChanged)(ANativeActivity *, const ARect *);
    void (*onConfigurationChanged)(ANativeActivity *);
    void (*onLowMemory)(ANativeActivity *);
};

/* libc */
extern int pipe(int fds[2]);
extern long read(int fd, void *buf, size_t n);
extern long write(int fd, const void *buf, size_t n);
extern int close(int fd);
extern void *malloc(size_t n);
extern void *calloc(size_t n, size_t s);
extern void free(void *p);
extern void *memset(void *p, int c, size_t n);
extern int usleep(unsigned usec);
extern void _exit(int code) __attribute__((noreturn));
extern int pthread_mutex_init(bmutex_t *m, const void *attr);
extern int pthread_mutex_lock(bmutex_t *m);
extern int pthread_mutex_unlock(bmutex_t *m);
extern int pthread_mutex_destroy(bmutex_t *m);
extern int pthread_cond_init(bcond_t *c, const void *attr);
extern int pthread_cond_wait(bcond_t *c, bmutex_t *m);
extern int pthread_cond_broadcast(bcond_t *c);
extern int pthread_cond_destroy(bcond_t *c);
extern int pthread_attr_init(battr_t *a);
extern int pthread_attr_setdetachstate(battr_t *a, int state);
extern int pthread_create(bpthread_t *t, const battr_t *a, void *(*fn)(void *), void *arg);
/* liblog / libandroid */
extern int __android_log_print(int prio, const char *tag, const char *fmt, ...);
extern void *ALooper_prepare(int opts);
extern int ALooper_addFd(void *looper, int fd, int ident, int events, int (*cb)(int, int, void *), void *data);
extern int ALooper_pollAll(int timeout, int *outFd, int *outEvents, void **outData);
extern void AInputQueue_attachLooper(void *queue, void *looper, int ident, int (*cb)(int, int, void *), void *data);
extern void AInputQueue_detachLooper(void *queue);
extern int32_t AInputQueue_getEvent(void *queue, void **outEvent);
extern int32_t AInputQueue_preDispatchEvent(void *queue, void *event);
extern void AInputQueue_finishEvent(void *queue, void *event, int handled);
extern int32_t AInputEvent_getType(const void *event);
extern int32_t AKeyEvent_getAction(const void *event);
extern int32_t AKeyEvent_getKeyCode(const void *event);
extern void *AConfiguration_new(void);
extern void AConfiguration_delete(void *config);
extern void AConfiguration_fromAssetManager(void *config, void *am);
extern void AConfiguration_getLanguage(void *config, char *out);
extern int32_t AConfiguration_getOrientation(void *config);
extern int32_t AConfiguration_getDensity(void *config);
extern int32_t AConfiguration_getSdkVersion(void *config);
extern int32_t AConfiguration_getScreenWidthDp(void *config);
extern int32_t AConfiguration_getNavigation(void *config);
extern int32_t ANativeWindow_getWidth(void *window);
extern int32_t ANativeWindow_getHeight(void *window);
extern void *AChoreographer_getInstance(void);
extern void AChoreographer_postFrameCallback64(void *c, void (*cb)(int64_t, void *), void *data);
extern void ANativeActivity_finish(ANativeActivity *activity);

#define LOGI(...) __android_log_print(4, "GlueTest", __VA_ARGS__)

enum { LOOPER_ID_MAIN = 1, LOOPER_ID_INPUT = 2 };
enum {
    APP_CMD_INPUT_CHANGED, APP_CMD_INIT_WINDOW, APP_CMD_TERM_WINDOW, APP_CMD_WINDOW_RESIZED,
    APP_CMD_WINDOW_REDRAW_NEEDED, APP_CMD_CONTENT_RECT_CHANGED, APP_CMD_GAINED_FOCUS, APP_CMD_LOST_FOCUS,
    APP_CMD_CONFIG_CHANGED, APP_CMD_LOW_MEMORY, APP_CMD_START, APP_CMD_RESUME, APP_CMD_SAVE_STATE,
    APP_CMD_PAUSE, APP_CMD_STOP, APP_CMD_DESTROY,
};

struct android_app;
struct android_poll_source {
    int32_t id;
    struct android_app *app;
    void (*process)(struct android_app *app, struct android_poll_source *source);
};

struct android_app {
    void (*onAppCmd)(struct android_app *app, int32_t cmd);
    int32_t (*onInputEvent)(struct android_app *app, void *event);
    ANativeActivity *activity;
    void *config;
    void *looper;
    void *inputQueue;
    void *window;
    ARect contentRect;
    int activityState;
    int destroyRequested;
    bmutex_t mutex;
    bcond_t cond;
    int msgread, msgwrite;
    bpthread_t thread;
    struct android_poll_source cmdPollSource;
    struct android_poll_source inputPollSource;
    int running, stateSaved, destroyed;
    void *pendingInputQueue;
    void *pendingWindow;
    ARect pendingContentRect;
};

/* ── the glue (behaviour of android_native_app_glue.c) ─────────────── */

static int8_t readCmd(struct android_app *app) {
    int8_t cmd;
    if (read(app->msgread, &cmd, sizeof cmd) == sizeof cmd) return cmd;
    LOGI("No data on command pipe!");
    return -1;
}

static void preExecCmd(struct android_app *app, int8_t cmd) {
    switch (cmd) {
        case APP_CMD_INPUT_CHANGED:
            pthread_mutex_lock(&app->mutex);
            if (app->inputQueue) AInputQueue_detachLooper(app->inputQueue);
            app->inputQueue = app->pendingInputQueue;
            if (app->inputQueue)
                AInputQueue_attachLooper(app->inputQueue, app->looper, LOOPER_ID_INPUT, 0, &app->inputPollSource);
            pthread_cond_broadcast(&app->cond);
            pthread_mutex_unlock(&app->mutex);
            break;
        case APP_CMD_INIT_WINDOW:
            pthread_mutex_lock(&app->mutex);
            app->window = app->pendingWindow;
            pthread_cond_broadcast(&app->cond);
            pthread_mutex_unlock(&app->mutex);
            break;
        case APP_CMD_TERM_WINDOW:
            pthread_cond_broadcast(&app->cond);
            break;
        case APP_CMD_RESUME:
        case APP_CMD_START:
        case APP_CMD_PAUSE:
        case APP_CMD_STOP:
            pthread_mutex_lock(&app->mutex);
            app->activityState = cmd;
            pthread_cond_broadcast(&app->cond);
            pthread_mutex_unlock(&app->mutex);
            break;
        case APP_CMD_CONFIG_CHANGED:
            AConfiguration_fromAssetManager(app->config, app->activity->assetManager);
            break;
        case APP_CMD_DESTROY:
            app->destroyRequested = 1;
            break;
        default:
            break;
    }
}

static void postExecCmd(struct android_app *app, int8_t cmd) {
    switch (cmd) {
        case APP_CMD_TERM_WINDOW:
            pthread_mutex_lock(&app->mutex);
            app->window = 0;
            pthread_cond_broadcast(&app->cond);
            pthread_mutex_unlock(&app->mutex);
            break;
        case APP_CMD_SAVE_STATE:
            pthread_mutex_lock(&app->mutex);
            app->stateSaved = 1;
            pthread_cond_broadcast(&app->cond);
            pthread_mutex_unlock(&app->mutex);
            break;
        default:
            break;
    }
}

static void processCmd(struct android_app *app, struct android_poll_source *source) {
    int8_t cmd = readCmd(app);
    (void)source;
    preExecCmd(app, cmd);
    if (app->onAppCmd) app->onAppCmd(app, cmd);
    postExecCmd(app, cmd);
}

static void processInput(struct android_app *app, struct android_poll_source *source) {
    void *event = 0;
    (void)source;
    while (AInputQueue_getEvent(app->inputQueue, &event) >= 0) {
        int32_t handled = 0;
        if (AInputQueue_preDispatchEvent(app->inputQueue, event)) continue;
        if (app->onInputEvent) handled = app->onInputEvent(app, event);
        AInputQueue_finishEvent(app->inputQueue, event, handled);
    }
}

static void android_main(struct android_app *app);

static void *appEntry(void *param) {
    struct android_app *app = (struct android_app *)param;
    app->config = AConfiguration_new();
    AConfiguration_fromAssetManager(app->config, app->activity->assetManager);
    app->cmdPollSource.id = LOOPER_ID_MAIN;
    app->cmdPollSource.app = app;
    app->cmdPollSource.process = processCmd;
    app->inputPollSource.id = LOOPER_ID_INPUT;
    app->inputPollSource.app = app;
    app->inputPollSource.process = processInput;
    app->looper = ALooper_prepare(1 /* ALOOPER_PREPARE_ALLOW_NON_CALLBACKS */);
    ALooper_addFd(app->looper, app->msgread, LOOPER_ID_MAIN, 1 /* INPUT */, 0, &app->cmdPollSource);
    pthread_mutex_lock(&app->mutex);
    app->running = 1;
    pthread_cond_broadcast(&app->cond);
    pthread_mutex_unlock(&app->mutex);

    android_main(app);

    pthread_mutex_lock(&app->mutex);
    if (app->inputQueue) AInputQueue_detachLooper(app->inputQueue);
    AConfiguration_delete(app->config);
    app->destroyed = 1;
    pthread_cond_broadcast(&app->cond);
    pthread_mutex_unlock(&app->mutex);
    return 0;
}

static void writeCmd(struct android_app *app, int8_t cmd) {
    if (write(app->msgwrite, &cmd, sizeof cmd) != sizeof cmd) LOGI("Failure writing android_app cmd");
}

static void setInput(struct android_app *app, void *queue) {
    pthread_mutex_lock(&app->mutex);
    app->pendingInputQueue = queue;
    writeCmd(app, APP_CMD_INPUT_CHANGED);
    while (app->inputQueue != app->pendingInputQueue) pthread_cond_wait(&app->cond, &app->mutex);
    pthread_mutex_unlock(&app->mutex);
}

static void setWindow(struct android_app *app, void *window) {
    pthread_mutex_lock(&app->mutex);
    if (app->pendingWindow) {
        writeCmd(app, APP_CMD_TERM_WINDOW);
        app->pendingWindow = 0;
    }
    app->pendingWindow = window;
    if (window) writeCmd(app, APP_CMD_INIT_WINDOW);
    while (app->window != app->pendingWindow) pthread_cond_wait(&app->cond, &app->mutex);
    pthread_mutex_unlock(&app->mutex);
}

static void setActivityState(struct android_app *app, int8_t cmd) {
    pthread_mutex_lock(&app->mutex);
    writeCmd(app, cmd);
    while (app->activityState != cmd) pthread_cond_wait(&app->cond, &app->mutex);
    pthread_mutex_unlock(&app->mutex);
}

static struct android_app *appOf(ANativeActivity *a) { return (struct android_app *)a->instance; }

static void onDestroy(ANativeActivity *a) {
    struct android_app *app = appOf(a);
    pthread_mutex_lock(&app->mutex);
    writeCmd(app, APP_CMD_DESTROY);
    while (!app->destroyed) pthread_cond_wait(&app->cond, &app->mutex);
    pthread_mutex_unlock(&app->mutex);
    close(app->msgread);
    close(app->msgwrite);
    LOGI("onDestroy: android_main has returned");
}
static void onStart(ANativeActivity *a) { setActivityState(appOf(a), APP_CMD_START); }
static void onResume(ANativeActivity *a) { setActivityState(appOf(a), APP_CMD_RESUME); }
static void *onSaveInstanceState(ANativeActivity *a, size_t *outLen) {
    struct android_app *app = appOf(a);
    pthread_mutex_lock(&app->mutex);
    app->stateSaved = 0;
    writeCmd(app, APP_CMD_SAVE_STATE);
    while (!app->stateSaved) pthread_cond_wait(&app->cond, &app->mutex);
    pthread_mutex_unlock(&app->mutex);
    *outLen = 0;
    return 0;
}
static void onPause(ANativeActivity *a) { setActivityState(appOf(a), APP_CMD_PAUSE); }
static void onStop(ANativeActivity *a) { setActivityState(appOf(a), APP_CMD_STOP); }
static void onConfigurationChanged(ANativeActivity *a) { writeCmd(appOf(a), APP_CMD_CONFIG_CHANGED); }
static void onLowMemory(ANativeActivity *a) { writeCmd(appOf(a), APP_CMD_LOW_MEMORY); }
static void onWindowFocusChanged(ANativeActivity *a, int focused) {
    writeCmd(appOf(a), focused ? APP_CMD_GAINED_FOCUS : APP_CMD_LOST_FOCUS);
}
static void onNativeWindowCreated(ANativeActivity *a, void *window) { setWindow(appOf(a), window); }
static void onNativeWindowDestroyed(ANativeActivity *a, void *window) {
    (void)window;
    setWindow(appOf(a), 0);
}
static void onInputQueueCreated(ANativeActivity *a, void *queue) { setInput(appOf(a), queue); }
static void onInputQueueDestroyed(ANativeActivity *a, void *queue) {
    (void)queue;
    setInput(appOf(a), 0);
}

__attribute__((visibility("default"))) void ANativeActivity_onCreate(ANativeActivity *activity, void *savedState,
                                                                    size_t savedStateSize) {
    struct android_app *app;
    battr_t attr;
    int fds[2];
    (void)savedState;
    (void)savedStateSize;
    /* the glue writes INTO the framework's table */
    activity->callbacks->onDestroy = onDestroy;
    activity->callbacks->onStart = onStart;
    activity->callbacks->onResume = onResume;
    activity->callbacks->onSaveInstanceState = onSaveInstanceState;
    activity->callbacks->onPause = onPause;
    activity->callbacks->onStop = onStop;
    activity->callbacks->onConfigurationChanged = onConfigurationChanged;
    activity->callbacks->onLowMemory = onLowMemory;
    activity->callbacks->onWindowFocusChanged = onWindowFocusChanged;
    activity->callbacks->onNativeWindowCreated = onNativeWindowCreated;
    activity->callbacks->onNativeWindowDestroyed = onNativeWindowDestroyed;
    activity->callbacks->onInputQueueCreated = onInputQueueCreated;
    activity->callbacks->onInputQueueDestroyed = onInputQueueDestroyed;

    app = (struct android_app *)calloc(1, sizeof *app);
    app->activity = activity;
    pthread_mutex_init(&app->mutex, 0);
    pthread_cond_init(&app->cond, 0);
    if (pipe(fds)) {
        LOGI("could not create pipe");
        _exit(3);
    }
    app->msgread = fds[0];
    app->msgwrite = fds[1];
    pthread_attr_init(&attr);
    pthread_attr_setdetachstate(&attr, 1 /* PTHREAD_CREATE_DETACHED */);
    pthread_create(&app->thread, &attr, appEntry, app);
    pthread_mutex_lock(&app->mutex);
    while (!app->running) pthread_cond_wait(&app->cond, &app->mutex);
    pthread_mutex_unlock(&app->mutex);
    activity->instance = app;
}

/* ── the "game" ──────────────────────────────────────────────────────── */

static struct {
    int focused;
    int frames;
    int keyDown, keyUp;
    int choreoFrames;
    int windowW, windowH;
    int sawStart, sawResume;
    int finishing;
    int failures;
} g;

static void check(int ok, const char *what) {
    __android_log_print(ok ? 4 : 6, "GlueTest", "[glue-test] %s: %s", ok ? "ok" : "FAIL", what);
    if (!ok) g.failures++;
}

static void onChoreoFrame(int64_t frameTimeNanos, void *data) {
    (void)data;
    if (frameTimeNanos > 0) g.choreoFrames++;
    if (g.choreoFrames < 3) AChoreographer_postFrameCallback64(AChoreographer_getInstance(), onChoreoFrame, 0);
}

static void handleCmd(struct android_app *app, int32_t cmd) {
    switch (cmd) {
        case APP_CMD_START: g.sawStart = 1; break;
        case APP_CMD_RESUME: g.sawResume = 1; break;
        case APP_CMD_INIT_WINDOW:
            g.windowW = ANativeWindow_getWidth(app->window);
            g.windowH = ANativeWindow_getHeight(app->window);
            AChoreographer_postFrameCallback64(AChoreographer_getInstance(), onChoreoFrame, 0);
            break;
        case APP_CMD_GAINED_FOCUS: g.focused = 1; break;
        case APP_CMD_LOST_FOCUS: g.focused = 0; break;
        default: break;
    }
}

static int32_t handleInput(struct android_app *app, void *event) {
    (void)app;
    if (AInputEvent_getType(event) == 1 && AKeyEvent_getKeyCode(event) == 96) {
        if (AKeyEvent_getAction(event) == 0) g.keyDown++;
        else if (AKeyEvent_getAction(event) == 1) g.keyUp++;
        return 1;
    }
    return 0;
}

static void android_main(struct android_app *app) {
    char lang[3] = { 0, 0, 0 };
    app->onAppCmd = handleCmd;
    app->onInputEvent = handleInput;
    AConfiguration_getLanguage(app->config, lang);
    check(lang[0] == 'e' && lang[1] == 'n' && AConfiguration_getOrientation(app->config) == 2 &&
              AConfiguration_getDensity(app->config) == 320 && AConfiguration_getSdkVersion(app->config) == 28 &&
              AConfiguration_getScreenWidthDp(app->config) == 640 && AConfiguration_getNavigation(app->config) == 2,
          "AConfiguration describes a landscape xhdpi d-pad device on SDK 28");
    for (;;) {
        int events = 0;
        struct android_poll_source *source = 0;
        int animating = app->window && g.focused && !g.finishing;
        while (ALooper_pollAll(animating ? 0 : -1, 0, &events, (void **)&source) >= 0) {
            if (source) source->process(app, source);
            if (app->destroyRequested) {
                check(g.sawStart && g.sawResume, "APP_CMD_START and APP_CMD_RESUME reached the game thread");
                check(g.windowW == 1280 && g.windowH == 720, "APP_CMD_INIT_WINDOW carried a 1280x720 window");
                check(g.frames >= 30, "the game rendered frames while focused");
                check(g.keyDown == 1 && g.keyUp == 1, "BUTTON_A down+up arrived through LOOPER_ID_INPUT");
                check(g.choreoFrames >= 3, "AChoreographer frame callbacks fired on the game looper");
                __android_log_print(4, "GlueTest", "[glue-test] %s (%d failures)",
                                    g.failures ? "FAILED" : "all checks passed", g.failures);
                if (g.failures) _exit(3);
                return;
            }
            animating = app->window && g.focused && !g.finishing;
        }
        if (animating) {
            g.frames++;
            usleep(4000); /* "draw" */
            if ((g.frames >= 30 && g.keyUp && g.choreoFrames >= 3) || g.frames > 600) {
                g.finishing = 1;
                LOGI("finishing after %d frames", g.frames);
                ANativeActivity_finish(app->activity);
            }
        }
    }
}
