/* fake_sdl.c — a fake firmware libSDL2-2.0.so.0 for the smoke test of the
 * SDL-hosted present backend (eglpresent.c, ANDROIDPAK_PRESENT=sdl). It
 * exports the SDL2 entry points that backend resolves and implements them
 * the way SDL's "mali" fbdev video driver does:
 *
 *   video init     loads its own EGL like SDL_EGL_LoadLibrary: the library
 *                  named by SDL_VIDEO_EGL_DRIVER, else libEGL.so.1 (which a
 *                  GPU-less host does not have — so the runner must point it
 *                  at the game's EGL, here the mock GPU)
 *   CreateWindow   eglChooseConfig from the GL attributes, then
 *                  eglCreateWindowSurface with a {uint16 width, height}
 *                  fbdev window of the "framebuffer" size (FAKESDL_FB=WxH,
 *                  default 1280x720) — never the size the caller asked for
 *   CreateContext  eglCreateContext + eglMakeCurrent on the calling thread;
 *                  the SDL_GLContext is the EGLContext, as on SDL's EGL drivers
 *   MakeCurrent    SDL's per-thread record, with SDL's "already current"
 *                  shortcut
 *   SwapWindow     SDL's rule: a window that is not current on the calling
 *                  thread is not swapped (SDL only sets an error)
 *
 * Every call is logged as "[fakesdl] ..." for run-smoke.sh. FAKESDL_FAIL_INIT=1
 * makes the video init fail; a successful one tells the mock GPU
 * (mockgpu_sdlVideoUp, for its MOCKGPU_NEEDS_SDL knob — no setenv on a game
 * thread). FAKESDL_DISPLAY=device makes SDL's EGL display one of its own
 * native device (a KMSDRM SDL's gbm device) instead of EGL_DEFAULT_DISPLAY.
 * SDL_GL_LoadLibrary logs the GLES library SDL_VIDEO_GL_DRIVER names.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "eglhost.h"
#include "sdlhost.h"

#define LOG(...)                                   \
    do {                                           \
        fprintf(stderr, "[fakesdl] " __VA_ARGS__); \
        fputc('\n', stderr);                       \
    } while (0)

#define EXPORT __attribute__((visibility("default")))

typedef EGLContext (*PFN_create)(EGLDisplay, EGLConfig, EGLContext, const EGLint *);

struct ApakSdlWindow {
    uint32_t magic;
    EGLSurface surf;
    struct {
        uint16_t width, height;
    } fb;
};

static struct {
    int videoUp, glLoaded;
    void *egl;
    char eglPath[256];
    PFN_eglGetDisplay getDisplay;
    PFN_eglInitialize initialize;
    PFN_eglChooseConfig chooseConfig;
    PFN_eglCreateWindowSurface createWindowSurface;
    PFN_eglDestroySurface destroySurface;
    PFN_create createContext;
    PFN_eglDestroyContext destroyContext;
    PFN_eglMakeCurrent makeCurrent;
    PFN_eglSwapBuffers swapBuffers;
    EGLDisplay dpy;
    EGLConfig cfg;
    int attr[APAK_SDL_GL_ATTR_COUNT];
    struct ApakSdlWindow window;
    int windowLive;
    int swaps;
    char device; /* FAKESDL_DISPLAY=device: the native display SDL opens */
    char error[256];
} F;

/* SDL's per-thread current window and context */
static pthread_key_t g_curWindowKey, g_curCtxKey;
static pthread_once_t g_keysOnce = PTHREAD_ONCE_INIT;
static void makeKeys(void) {
    pthread_key_create(&g_curWindowKey, NULL);
    pthread_key_create(&g_curCtxKey, NULL);
}
static void *curWindow(void) {
    pthread_once(&g_keysOnce, makeKeys);
    return pthread_getspecific(g_curWindowKey);
}
static void *curCtx(void) {
    pthread_once(&g_keysOnce, makeKeys);
    return pthread_getspecific(g_curCtxKey);
}
static void setCurrent(void *window, void *ctx) {
    pthread_once(&g_keysOnce, makeKeys);
    pthread_setspecific(g_curWindowKey, window);
    pthread_setspecific(g_curCtxKey, ctx);
}

static int fail(const char *why) {
    snprintf(F.error, sizeof F.error, "%s", why);
    return -1;
}

EXPORT const char *SDL_GetError(void) { return F.error; }

EXPORT void SDL_GetVersion(ApakSdlVersion *v) {
    if (!v) return;
    v->major = 2;
    v->minor = 0;
    v->patch = 0;
}

EXPORT const char *SDL_GetCurrentVideoDriver(void) { return F.videoUp ? "mali" : NULL; }

#define EGLSYM(field, name)                                            \
    do {                                                               \
        *(void **)&F.field = dlsym(F.egl, name);                       \
        if (!F.field) return fail("EGL library lacks " name);          \
    } while (0)

EXPORT int SDL_InitSubSystem(uint32_t flags) {
    const char *path = getenv("SDL_VIDEO_EGL_DRIVER");
    void (*videoUp)(void);
    if (!(flags & APAK_SDL_INIT_VIDEO)) return 0;
    if (F.videoUp) return 0;
    LOG("hints: SDL_NO_SIGNAL_HANDLERS=%s SDL_INPUT_LINUX_KEEP_KBD=%s",
        getenv("SDL_NO_SIGNAL_HANDLERS") ? getenv("SDL_NO_SIGNAL_HANDLERS") : "(unset)",
        getenv("SDL_INPUT_LINUX_KEEP_KBD") ? getenv("SDL_INPUT_LINUX_KEEP_KBD") : "(unset)");
    if (getenv("FAKESDL_FAIL_INIT")) {
        LOG("SDL_InitSubSystem(VIDEO) failed (FAKESDL_FAIL_INIT)");
        return fail("mali-fbdev: Could not open framebuffer device");
    }
    if (!path || !*path) path = "libEGL.so.1"; /* SDL's default on Linux */
    snprintf(F.eglPath, sizeof F.eglPath, "%s", path);
    F.egl = dlopen(path, RTLD_NOW | RTLD_LOCAL);
    if (!F.egl) {
        LOG("SDL_InitSubSystem(VIDEO): cannot load EGL %s", path);
        return fail("Could not load EGL library");
    }
    EGLSYM(getDisplay, "eglGetDisplay");
    EGLSYM(initialize, "eglInitialize");
    EGLSYM(chooseConfig, "eglChooseConfig");
    EGLSYM(createWindowSurface, "eglCreateWindowSurface");
    EGLSYM(destroySurface, "eglDestroySurface");
    EGLSYM(createContext, "eglCreateContext");
    EGLSYM(destroyContext, "eglDestroyContext");
    EGLSYM(makeCurrent, "eglMakeCurrent");
    EGLSYM(swapBuffers, "eglSwapBuffers");
    F.videoUp = 1;
    *(void **)&videoUp = dlsym(F.egl, "mockgpu_sdlVideoUp");
    if (videoUp) videoUp();
    /* SDL_VideoInit's GLES defaults on a GLES-only build */
    F.attr[APAK_SDL_GL_RED_SIZE] = 3;
    F.attr[APAK_SDL_GL_GREEN_SIZE] = 3;
    F.attr[APAK_SDL_GL_BLUE_SIZE] = 2;
    F.attr[APAK_SDL_GL_DEPTH_SIZE] = 16;
    F.attr[APAK_SDL_GL_DOUBLEBUFFER] = 1;
    F.attr[APAK_SDL_GL_CONTEXT_MAJOR_VERSION] = 2;
    F.attr[APAK_SDL_GL_CONTEXT_PROFILE_MASK] = APAK_SDL_GL_CONTEXT_PROFILE_ES;
    LOG("SDL_InitSubSystem(VIDEO): driver mali, EGL from %s", path);
    return 0;
}

EXPORT void SDL_QuitSubSystem(uint32_t flags) {
    if (flags & APAK_SDL_INIT_VIDEO) LOG("SDL_QuitSubSystem(VIDEO)");
}

EXPORT int SDL_GL_SetAttribute(int attr, int value) {
    if (attr < 0 || attr >= APAK_SDL_GL_ATTR_COUNT) return fail("Unknown OpenGL attribute");
    F.attr[attr] = value;
    return 0;
}

/* SDL_EGL_LoadLibrary: the GLES library (SDL_VIDEO_GL_DRIVER, else
 * libGLESv2.so.2 from the library path), then the display of
 * EGL_DEFAULT_DISPLAY (mali) or of SDL's own device, initialised */
EXPORT int SDL_GL_LoadLibrary(const char *path) {
    const char *gles = getenv("SDL_VIDEO_GL_DRIVER");
    (void)path;
    if (!F.videoUp) return fail("Video subsystem has not been initialized");
    if (F.glLoaded) return 0;
    LOG("SDL_GL_LoadLibrary: GLES library %s", gles && *gles ? gles : "SDL's default libGLESv2.so.2");
    F.dpy = F.getDisplay(getenv("FAKESDL_DISPLAY") ? (void *)&F.device : EGL_DEFAULT_DISPLAY);
    if (!F.dpy || F.initialize(F.dpy, NULL, NULL) != EGL_TRUE) {
        LOG("SDL_GL_LoadLibrary: could not initialize EGL");
        return fail("Could not initialize EGL");
    }
    F.glLoaded = 1;
    LOG("SDL_GL_LoadLibrary: EGL display initialised");
    return 0;
}

EXPORT ApakSdlWindow *SDL_CreateWindow(const char *title, int x, int y, int w, int h, uint32_t flags) {
    const char *fb = getenv("FAKESDL_FB");
    unsigned fw = 1280, fh = 720;
    EGLint attribs[32], n = 0, i = 0;
    int es = F.attr[APAK_SDL_GL_CONTEXT_MAJOR_VERSION] >= 3 ? EGL_OPENGL_ES3_BIT_KHR : EGL_OPENGL_ES2_BIT;
    (void)x;
    (void)y;
    if (!(flags & APAK_SDL_WINDOW_OPENGL)) {
        fail("mali-fbdev windows are OpenGL ES windows");
        return NULL;
    }
    if (F.windowLive) {
        fail("mali-fbdev: one window only");
        return NULL;
    }
    if (SDL_GL_LoadLibrary(NULL) != 0) return NULL;
    if (fb) sscanf(fb, "%ux%u", &fw, &fh);
    attribs[i++] = EGL_RED_SIZE;
    attribs[i++] = F.attr[APAK_SDL_GL_RED_SIZE];
    attribs[i++] = EGL_GREEN_SIZE;
    attribs[i++] = F.attr[APAK_SDL_GL_GREEN_SIZE];
    attribs[i++] = EGL_BLUE_SIZE;
    attribs[i++] = F.attr[APAK_SDL_GL_BLUE_SIZE];
    attribs[i++] = EGL_DEPTH_SIZE;
    attribs[i++] = F.attr[APAK_SDL_GL_DEPTH_SIZE];
    attribs[i++] = EGL_STENCIL_SIZE;
    attribs[i++] = F.attr[APAK_SDL_GL_STENCIL_SIZE];
    attribs[i++] = EGL_RENDERABLE_TYPE;
    attribs[i++] = es;
    attribs[i++] = EGL_SURFACE_TYPE;
    attribs[i++] = EGL_WINDOW_BIT;
    attribs[i++] = EGL_NONE;
    if (F.chooseConfig(F.dpy, attribs, &F.cfg, 1, &n) != EGL_TRUE || n < 1) {
        fail("Couldn't find matching EGL config");
        return NULL;
    }
    memset(&F.window, 0, sizeof F.window);
    F.window.magic = 0x5d15c0deu;
    F.window.fb.width = (uint16_t)fw; /* the whole framebuffer, whatever was asked */
    F.window.fb.height = (uint16_t)fh;
    F.window.surf = F.createWindowSurface(F.dpy, F.cfg, &F.window.fb, NULL);
    if (!F.window.surf) {
        fail("mali-fbdev: Can't create EGL window surface");
        return NULL;
    }
    F.windowLive = 1;
    LOG("SDL_CreateWindow \"%s\" %dx%d flags=0x%x -> fbdev %ux%u, config rgba %d%d%d%d depth %d stencil %d, %s",
        title ? title : "", w, h, flags, fw, fh, F.attr[APAK_SDL_GL_RED_SIZE], F.attr[APAK_SDL_GL_GREEN_SIZE],
        F.attr[APAK_SDL_GL_BLUE_SIZE], F.attr[APAK_SDL_GL_ALPHA_SIZE], F.attr[APAK_SDL_GL_DEPTH_SIZE],
        F.attr[APAK_SDL_GL_STENCIL_SIZE], es == EGL_OPENGL_ES3_BIT_KHR ? "ES3 config" : "ES2 config");
    return &F.window;
}

EXPORT void SDL_DestroyWindow(ApakSdlWindow *window) {
    if (window != &F.window || !F.windowLive) return;
    F.destroySurface(F.dpy, F.window.surf);
    F.windowLive = 0;
    LOG("SDL_DestroyWindow");
}

EXPORT int SDL_GL_MakeCurrent(ApakSdlWindow *window, ApakSdlGLContext ctx) {
    if (!F.videoUp) return fail("Video subsystem has not been initialized");
    if (!ctx) window = NULL;
    if (window == curWindow() && ctx == curCtx()) return 0; /* SDL: "already current" */
    if (window && (window != &F.window || !F.windowLive)) return fail("Invalid window");
    if (F.makeCurrent(F.dpy, window ? F.window.surf : EGL_NO_SURFACE, window ? F.window.surf : EGL_NO_SURFACE,
                      ctx ? (EGLContext)ctx : EGL_NO_CONTEXT) != EGL_TRUE)
        return fail("Unable to make EGL context current");
    setCurrent(window, ctx);
    LOG("SDL_GL_MakeCurrent %s", window ? "window" : "none");
    return 0;
}

EXPORT ApakSdlGLContext SDL_GL_CreateContext(ApakSdlWindow *window) {
    EGLint attribs[8], i = 0;
    EGLContext ctx;
    if (window != &F.window || !F.windowLive) {
        fail("Invalid window");
        return NULL;
    }
    attribs[i++] = EGL_CONTEXT_CLIENT_VERSION;
    attribs[i++] = F.attr[APAK_SDL_GL_CONTEXT_MAJOR_VERSION] > 0 ? F.attr[APAK_SDL_GL_CONTEXT_MAJOR_VERSION] : 1;
    if (F.attr[APAK_SDL_GL_CONTEXT_MINOR_VERSION] > 0) {
        attribs[i++] = EGL_CONTEXT_MINOR_VERSION_KHR;
        attribs[i++] = F.attr[APAK_SDL_GL_CONTEXT_MINOR_VERSION];
    }
    attribs[i++] = EGL_NONE;
    ctx = F.createContext(F.dpy, F.cfg, EGL_NO_CONTEXT, attribs);
    if (!ctx) {
        fail("Could not create EGL context");
        return NULL;
    }
    LOG("SDL_GL_CreateContext GLES %d.%d", F.attr[APAK_SDL_GL_CONTEXT_MAJOR_VERSION],
        F.attr[APAK_SDL_GL_CONTEXT_MINOR_VERSION]);
    if (SDL_GL_MakeCurrent(window, ctx) != 0) {
        F.destroyContext(F.dpy, ctx);
        return NULL;
    }
    return ctx;
}

EXPORT void SDL_GL_DeleteContext(ApakSdlGLContext ctx) {
    if (!ctx) return;
    if (curCtx() == ctx) SDL_GL_MakeCurrent(NULL, NULL);
    F.destroyContext(F.dpy, (EGLContext)ctx);
    LOG("SDL_GL_DeleteContext");
}

EXPORT ApakSdlWindow *SDL_GL_GetCurrentWindow(void) { return (ApakSdlWindow *)curWindow(); }

EXPORT void SDL_GL_SwapWindow(ApakSdlWindow *window) {
    if (window != &F.window || !F.windowLive) {
        fail("Invalid window");
        return;
    }
    if (curWindow() != window) {
        LOG("SDL_GL_SwapWindow: the window is not current on this thread — ignored (SDL's rule)");
        fail("The specified window has not been made current");
        return;
    }
    if (F.swapBuffers(F.dpy, F.window.surf) != EGL_TRUE) {
        LOG("SDL_GL_SwapWindow: eglSwapBuffers failed");
        return;
    }
    F.swaps++;
    LOG("SDL_GL_SwapWindow frame %d", F.swaps);
}
