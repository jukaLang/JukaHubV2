/* fake_egl.c — an NDK-shaped game that renders through EGL the way the
 * NDK's native-activity sample does, for the present-layer smoke test:
 *
 *   onNativeWindowCreated:
 *     eglGetDisplay(EGL_DEFAULT_DISPLAY) → eglInitialize →
 *     eglChooseConfig(ES2 window, RGB888) → eglGetConfigAttrib(
 *     EGL_NATIVE_VISUAL_ID) → ANativeWindow_setBuffersGeometry(window, 0, 0,
 *     visual) → eglCreateWindowSurface(display, config, ANativeWindow*) →
 *     eglCreateContext(ES 2) → eglMakeCurrent → eglQuerySurface(size) →
 *     5 × eglSwapBuffers → eglMakeCurrent(none) → eglDestroyContext →
 *     eglDestroySurface → eglTerminate → finish
 *
 * EGLTEST_BUFFERS=WxH asks for smaller buffers in setBuffersGeometry (the
 * way games lower their render size); EGLTEST_CONTEXT_FIRST=1 creates the
 * context before the window surface (GLSurfaceView's order);
 * EGLTEST_PROBE_CONTEXT=1 first makes and destroys a context on a
 * pbuffer-only config (an engine's capability probe);
 * EGLTEST_RELEASE_THREAD=1 ends with eglReleaseThread, eglMakeCurrent again
 * and a 6th frame. A failed eglInitialize or eglCreateWindowSurface logs the
 * eglGetError() code the game reads. Linked NDK-style against stub
 * libEGL.so/libandroid.so/liblog.so; the runner binds the EGL imports to its
 * present layer (eglpresent.c), which drives the mock GPU userspace
 * (mock_gpu.c), directly or through the fake SDL2 (fake_sdl.c).
 */
#include <stddef.h>
#include <stdint.h>

typedef struct ANativeActivity ANativeActivity;
typedef struct {
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
    void (*onContentRectChanged)(ANativeActivity *, const void *);
    void (*onConfigurationChanged)(ANativeActivity *);
    void (*onLowMemory)(ANativeActivity *);
} ANativeActivityCallbacks;
struct ANativeActivity {
    ANativeActivityCallbacks *callbacks;
};

extern void *eglGetDisplay(void *native);
extern unsigned eglInitialize(void *dpy, int32_t *major, int32_t *minor);
extern unsigned eglChooseConfig(void *dpy, const int32_t *attribs, void **configs, int32_t size, int32_t *num);
extern unsigned eglGetConfigAttrib(void *dpy, void *config, int32_t attribute, int32_t *value);
extern void *eglCreateWindowSurface(void *dpy, void *config, void *window, const int32_t *attribs);
extern void *eglCreateContext(void *dpy, void *config, void *share, const int32_t *attribs);
extern unsigned eglMakeCurrent(void *dpy, void *draw, void *read, void *context);
extern unsigned eglQuerySurface(void *dpy, void *surface, int32_t attribute, int32_t *value);
extern unsigned eglDestroyContext(void *dpy, void *context);
extern unsigned eglSwapBuffers(void *dpy, void *surface);
extern unsigned eglDestroySurface(void *dpy, void *surface);
extern unsigned eglTerminate(void *dpy);
extern unsigned eglReleaseThread(void);
extern int32_t eglGetError(void);
extern int32_t ANativeWindow_setBuffersGeometry(void *window, int32_t w, int32_t h, int32_t format);
extern void ANativeActivity_finish(ANativeActivity *activity);
extern int __android_log_print(int prio, const char *tag, const char *fmt, ...);
extern void _exit(int code) __attribute__((noreturn));
extern char *getenv(const char *name);

static int g_failures;

static void check(int ok, const char *what) {
    __android_log_print(ok ? 4 : 6, "EglTest", "[egl-test] %s: %s", ok ? "ok" : "FAIL", what);
    if (!ok) g_failures++;
}

/* "640x360" -> 640, 360; anything else leaves both 0 */
static void parseSize(const char *s, int32_t *w, int32_t *h) {
    int32_t a = 0, b = 0;
    if (!s) return;
    while (*s >= '0' && *s <= '9') a = a * 10 + (*s++ - '0');
    if (*s++ != 'x') return;
    while (*s >= '0' && *s <= '9') b = b * 10 + (*s++ - '0');
    if (*s == '\0' && a > 0 && b > 0) {
        *w = a;
        *h = b;
    }
}

static void onWindowCreated(ANativeActivity *activity, void *window) {
    static const int32_t attribs[] = {
        0x3033 /* EGL_SURFACE_TYPE */, 0x0004 /* EGL_WINDOW_BIT */,
        0x3040 /* EGL_RENDERABLE_TYPE */, 0x0004 /* EGL_OPENGL_ES2_BIT */,
        0x3022 /* BLUE */, 8, 0x3023 /* GREEN */, 8, 0x3024 /* RED */, 8,
        0x3038 /* EGL_NONE */,
    };
    static const int32_t pbufferAttribs[] = {
        0x3033 /* EGL_SURFACE_TYPE */, 0x0001 /* EGL_PBUFFER_BIT */,
        0x3040 /* EGL_RENDERABLE_TYPE */, 0x0004 /* EGL_OPENGL_ES2_BIT */, 0x3038 /* EGL_NONE */,
    };
    static const int32_t ctxAttribs[] = { 0x3098 /* EGL_CONTEXT_CLIENT_VERSION */, 2, 0x3038 /* EGL_NONE */ };
    void *dpy = eglGetDisplay((void *)0), *config = 0, *surface, *context = 0;
    int32_t major = 0, minor = 0, num = 0, visual = 0, bw = 0, bh = 0, w = 0, h = 0;
    int i, swaps = 0, contextFirst = getenv("EGLTEST_CONTEXT_FIRST") != 0, initOk;

    /* what a game's own libSDL2 or a child process would inherit */
    __android_log_print(4, "EglTest", "[egl-test] SDL environment %s",
                        getenv("SDL_VIDEO_EGL_DRIVER") || getenv("SDL_NO_SIGNAL_HANDLERS") ? "set" : "unset");
    check(dpy != 0, "eglGetDisplay(EGL_DEFAULT_DISPLAY) gives a display");
    initOk = eglInitialize(dpy, &major, &minor) == 1;
    if (!initOk) __android_log_print(6, "EglTest", "[egl-test] eglInitialize failed: 0x%x", (unsigned)eglGetError());
    check(initOk, "eglInitialize");
    if (getenv("EGLTEST_PROBE_CONTEXT")) {
        void *probeConfig = 0, *probe;
        check(eglChooseConfig(dpy, pbufferAttribs, &probeConfig, 1, &num) == 1 && num == 1,
              "eglChooseConfig gives the probe a pbuffer config");
        probe = eglCreateContext(dpy, probeConfig, 0, ctxAttribs);
        check(probe != 0, "a probe context on the pbuffer config");
        check(eglDestroyContext(dpy, probe) == 1, "the probe context is destroyed before the window");
    }
    check(eglChooseConfig(dpy, attribs, &config, 1, &num) == 1 && num == 1, "eglChooseConfig picks one config");
    check(eglGetConfigAttrib(dpy, config, 0x302E /* EGL_NATIVE_VISUAL_ID */, &visual) == 1 &&
              (visual != 0 || !getenv("EGLTEST_EXPECT_SCANOUT_VISUAL")),
          "the first config offered has a scanout visual (gbm backend)");
    parseSize(getenv("EGLTEST_BUFFERS"), &bw, &bh);
    check(ANativeWindow_setBuffersGeometry(window, bw, bh, visual) == 0, "ANativeWindow_setBuffersGeometry(w, h, visual)");
    if (contextFirst) {
        context = eglCreateContext(dpy, config, 0, ctxAttribs);
        check(context != 0, "eglCreateContext(GLES 2) before the window surface");
    }
    surface = eglCreateWindowSurface(dpy, config, window, 0);
    if (!surface)
        __android_log_print(6, "EglTest", "[egl-test] eglCreateWindowSurface failed: 0x%x", (unsigned)eglGetError());
    check(surface != 0, "eglCreateWindowSurface(ANativeWindow*)");
    if (!contextFirst) {
        context = eglCreateContext(dpy, config, 0, ctxAttribs);
        check(context != 0, "eglCreateContext(GLES 2)");
    }
    check(eglMakeCurrent(dpy, surface, surface, context) == 1, "eglMakeCurrent(window, context)");
    check(eglQuerySurface(dpy, surface, 0x3057 /* EGL_WIDTH */, &w) == 1 &&
              eglQuerySurface(dpy, surface, 0x3056 /* EGL_HEIGHT */, &h) == 1 && w > 0 && h > 0,
          "eglQuerySurface gives the surface size");
    __android_log_print(4, "EglTest", "[egl-test] drawing to a %dx%d surface", (int)w, (int)h);
    for (i = 0; i < 5; i++) swaps += eglSwapBuffers(dpy, surface) == 1;
    check(swaps == 5, "5 frames presented");
    if (getenv("EGLTEST_RELEASE_THREAD")) {
        /* a render thread handing its context back, then taking it again */
        check(eglReleaseThread() == 1, "eglReleaseThread");
        check(eglMakeCurrent(dpy, surface, surface, context) == 1, "eglMakeCurrent again after eglReleaseThread");
        check(eglSwapBuffers(dpy, surface) == 1, "a 6th frame after eglReleaseThread");
    }
    check(eglMakeCurrent(dpy, 0, 0, 0) == 1, "eglMakeCurrent(none)");
    check(eglDestroyContext(dpy, context) == 1, "eglDestroyContext");
    check(eglDestroySurface(dpy, surface) == 1, "eglDestroySurface");
    eglTerminate(dpy);
    __android_log_print(4, "EglTest", "[egl-test] %s (%d failures)", g_failures ? "FAILED" : "all checks passed",
                        g_failures);
    if (g_failures) _exit(3);
    ANativeActivity_finish(activity);
}

__attribute__((visibility("default"))) void ANativeActivity_onCreate(ANativeActivity *activity, void *saved,
                                                                    size_t size) {
    (void)saved;
    (void)size;
    activity->callbacks->onNativeWindowCreated = onWindowCreated;
}
