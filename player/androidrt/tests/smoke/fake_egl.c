/* fake_egl.c — an NDK-shaped game that renders through EGL the way the
 * NDK's native-activity sample does, for the present-layer smoke test:
 *
 *   onNativeWindowCreated:
 *     eglGetDisplay(EGL_DEFAULT_DISPLAY) → eglInitialize →
 *     eglChooseConfig(ES2 window, RGB888) → eglGetConfigAttrib(
 *     EGL_NATIVE_VISUAL_ID) → ANativeWindow_setBuffersGeometry(window, 0, 0,
 *     visual) → eglCreateWindowSurface(display, config, ANativeWindow*) →
 *     5 × eglSwapBuffers → eglDestroySurface → eglTerminate → finish
 *
 * Linked NDK-style against stub libEGL.so/libandroid.so/liblog.so; the
 * runner binds the EGL imports to its present layer (eglpresent.c), which
 * drives the mock GPU userspace (mock_gpu.c).
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
extern unsigned eglSwapBuffers(void *dpy, void *surface);
extern unsigned eglDestroySurface(void *dpy, void *surface);
extern unsigned eglTerminate(void *dpy);
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

static void onWindowCreated(ANativeActivity *activity, void *window) {
    static const int32_t attribs[] = {
        0x3033 /* EGL_SURFACE_TYPE */, 0x0004 /* EGL_WINDOW_BIT */,
        0x3040 /* EGL_RENDERABLE_TYPE */, 0x0004 /* EGL_OPENGL_ES2_BIT */,
        0x3022 /* BLUE */, 8, 0x3023 /* GREEN */, 8, 0x3024 /* RED */, 8,
        0x3038 /* EGL_NONE */,
    };
    void *dpy = eglGetDisplay((void *)0), *config = 0, *surface;
    int32_t major = 0, minor = 0, num = 0, visual = 0;
    int i, swaps = 0;

    check(dpy != 0, "eglGetDisplay(EGL_DEFAULT_DISPLAY) gives a display");
    check(eglInitialize(dpy, &major, &minor) == 1, "eglInitialize");
    check(eglChooseConfig(dpy, attribs, &config, 1, &num) == 1 && num == 1, "eglChooseConfig picks one config");
    check(eglGetConfigAttrib(dpy, config, 0x302E /* EGL_NATIVE_VISUAL_ID */, &visual) == 1 &&
              (visual != 0 || !getenv("EGLTEST_EXPECT_SCANOUT_VISUAL")),
          "the first config offered has a scanout visual (gbm backend)");
    check(ANativeWindow_setBuffersGeometry(window, 0, 0, visual) == 0, "ANativeWindow_setBuffersGeometry(0, 0, visual)");
    surface = eglCreateWindowSurface(dpy, config, window, 0);
    check(surface != 0, "eglCreateWindowSurface(ANativeWindow*)");
    for (i = 0; i < 5; i++) swaps += eglSwapBuffers(dpy, surface) == 1;
    check(swaps == 5, "5 frames presented");
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
