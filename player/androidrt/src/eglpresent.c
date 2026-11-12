/* eglpresent.c — Milestone 3: the present layer.
 *
 * An NDK game draws with EGL on the device's glibc GPU userspace, but it
 * hands EGL an ANativeWindow* — a pointer that userspace has never heard of
 * — and expects eglGetDisplay(EGL_DEFAULT_DISPLAY) to mean "the screen". The
 * runner interposes exactly the EGL calls where that matters (through the
 * bionic loader's override table, so the game's imports bind here first)
 * and passes everything else (contexts, eglMakeCurrent, GL) straight to the
 * host libraries.
 *
 * Backends (ANDROIDPAK_PRESENT=auto|gbm|native|sdl|off, default auto):
 *   gbm     DRM/KMS + GBM (a mainline-style display driver: KNULLI's GE8300
 *           package, PowerVR "nulldrmws"). eglGetDisplay(DEFAULT) becomes the
 *           EGL display of a gbm device on the DRM card; the game's window
 *           becomes a gbm_surface of the panel mode in the visual format of
 *           the config it chose; every eglSwapBuffers locks the front buffer,
 *           wraps it in a KMS framebuffer (cached per buffer object), sets
 *           the CRTC on the first frame and page-flips afterwards (waiting
 *           for the flip event, so the game is vsync-paced), then releases
 *           the previous buffer. The CRTC the hub was showing is restored at
 *           exit. eglChooseConfig lists configs whose EGL_NATIVE_VISUAL_ID is
 *           a GBM format first, so the game's pick can scan out.
 *   native  the EGL platform owns the display (fbdev-era stacks: PowerVR
 *           null window system, Mali fbdev). The window handle is replaced
 *           by ANDROIDPAK_EGL_WINDOW: `null` or `fbdev` (a Mali style
 *           {uint16 width, height} struct); present = eglSwapBuffers.
 *   sdl     the firmware's own SDL2 (libSDL2-2.0.so.0, dlopen'ed) creates
 *           the fullscreen window, its EGL surface and the GLES context;
 *           the game gets exactly those EGL objects, eglMakeCurrent becomes
 *           SDL_GL_MakeCurrent and eglSwapBuffers SDL_GL_SwapWindow (see
 *           "the SDL-hosted backend" below for why and how).
 *   off     no interposition at all (diagnostics only).
 *   auto    gbm when a DRM card with a connected connector, libgbm and
 *           libdrm are all present; on the stock firmware of the PowerVR
 *           TrimUI models (see onPowerVrTrimui) sdl, or native when its SDL2
 *           does not start; native otherwise — and sdl takes over when the
 *           native backend fails: the game's eglInitialize of the default
 *           display fails (retried once after SDL's video driver ran), or
 *           EGL refuses its window surface.
 *
 * The stock TrimUI firmware (ANDROIDPAK_DEVICE=trimui-* from JukaHub, or its
 * /usr/trimui system folder) has no DRM card: its PowerVR userspace draws
 * straight to the panel, and the firmware's own SDL2 reaches it through
 * SDL's "mali" fbdev video driver, which hands eglCreateWindowSurface the
 * {uint16 width, height} struct. So there the firmware's /usr/lib/libEGL.so
 * is loaded first (ahead of anything LD_LIBRARY_PATH would find) and the
 * native window defaults to that struct; elsewhere it defaults to null.
 *
 * Penguin Dash v0.5.0 rendered through the SDL path on a stock Smart Pro at
 * 1280x720. The hub parks its own drawing while a game runs (gamesuspend.go).
 * The smoke test drives the whole gbm path against mock
 * libEGL/libgbm/libdrm (tests/smoke/mock_gpu.c) and the sdl backend against
 * a fake libSDL2 (tests/smoke/fake_sdl.c); `ANDROIDPAK_SURFACE=1` prints
 * the device's real answer.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "actbridge.h"
#include "bioniccompat.h"
#include "bionicld.h"
#include "device.h"
#include "dlport.h"
#include "eglhost.h"
#include "sdlhost.h"

#define PR_TAG "[androidrt][present] "

enum { BACKEND_OFF = 0, BACKEND_NATIVE = 1, BACKEND_GBM = 2, BACKEND_SDL = 3 };

typedef struct PresentSurface {
    EGLDisplay dpy;
    EGLSurface surf;
    void *gbmSurface;
    void *prevBo;
    int crtcSet;
    int flipBroken;
    unsigned frames;
    struct PresentSurface *next;
} PresentSurface;

typedef struct {
    uint32_t fb;
    int fd;
} BoFb;

static struct {
    pthread_mutex_t lock;
    int backend;        /* written under lock; read with backendNow() */
    int fallback;       /* auto chose native: sdl may take over        */
    int initTried;      /* the eglInitialize fallback ran (once)       */
    int rawContexts;    /* live contexts the game made while native    */
    int ready;          /* gbm device + display created               */
    ApakGpu gpu;
    int fd;
    void *gbmDevice;
    EGLDisplay gbmDisplay;
    uint32_t connectorId;
    uint32_t crtcId;
    ApakDrmModeInfo mode;
    int haveMode;
    ApakDrmCrtc *savedCrtc;
    int crtcTouched;
    PresentSurface *surfaces;
    struct {
        uint16_t width, height;
    } fbdevWindow;
    void *nativeWindowValue;
} P = { .lock = PTHREAD_MUTEX_INITIALIZER, .backend = BACKEND_OFF, .fd = -1 };

/* 1 the first time `bit` is asked for — atomic: swaps run on game threads */
static unsigned onceFlags;
#define ONCE(bit) (!(__atomic_fetch_or(&onceFlags, (unsigned)(bit), __ATOMIC_RELAXED) & (unsigned)(bit)))

/* P.backend and the SDL handles the swap and display paths read are written
 * under P.lock and read without it: release stores, acquire loads. */
#define LOAD(x) __atomic_load_n(&(x), __ATOMIC_ACQUIRE)
#define STORE(x, v) __atomic_store_n(&(x), (v), __ATOMIC_RELEASE)
static int backendNow(void) { return LOAD(P.backend); }
static void setBackendLocked(int b) { STORE(P.backend, b); }

static const char *backendName(int b) {
    return b == BACKEND_GBM ? "gbm" : b == BACKEND_NATIVE ? "native" : b == BACKEND_SDL ? "sdl" : "off";
}

static int isGbmVisual(EGLint v) {
    uint32_t f = (uint32_t)v;
    return f == GBM_FORMAT_XRGB8888 || f == GBM_FORMAT_ARGB8888 || f == GBM_FORMAT_XBGR8888 ||
           f == GBM_FORMAT_ABGR8888 || f == GBM_FORMAT_RGB565;
}

/* ── KMS discovery (no modeset) ─────────────────────────────────────── */

static int findOutputLocked(void) {
    ApakGpu *g = &P.gpu;
    ApakDrmRes *res = g->drmModeGetResources(P.fd);
    int i;
    if (!res) return 0;
    for (i = 0; i < res->count_connectors && !P.haveMode; i++) {
        ApakDrmConnector *c = g->drmModeGetConnector(P.fd, res->connectors[i]);
        int m, best = 0;
        if (!c) continue;
        if (c->connection == DRM_MODE_CONNECTED && c->count_modes > 0) {
            for (m = 0; m < c->count_modes; m++)
                if (c->modes[m].type & DRM_MODE_TYPE_PREFERRED) best = m;
            P.mode = c->modes[best];
            P.connectorId = c->connector_id;
            if (c->encoder_id) {
                ApakDrmEncoder *e = g->drmModeGetEncoder(P.fd, c->encoder_id);
                if (e) {
                    P.crtcId = e->crtc_id;
                    g->drmModeFreeEncoder(e);
                }
            }
            if (!P.crtcId) {
                /* no active encoder: the first CRTC any of its encoders can drive */
                int k, j;
                for (k = 0; k < c->count_encoders && !P.crtcId; k++) {
                    ApakDrmEncoder *e = g->drmModeGetEncoder(P.fd, c->encoders[k]);
                    if (!e) continue;
                    for (j = 0; j < res->count_crtcs; j++) {
                        if (e->possible_crtcs & (1u << j)) {
                            P.crtcId = res->crtcs[j];
                            break;
                        }
                    }
                    g->drmModeFreeEncoder(e);
                }
            }
            P.haveMode = P.crtcId != 0;
        }
        g->drmModeFreeConnector(c);
    }
    g->drmModeFreeResources(res);
    return P.haveMode;
}

static int openDrmLocked(void) {
    char node[256];
    if (P.fd >= 0) return 1;
    if (!apak_gpuDrmNode(node, sizeof node)) return 0;
    P.fd = open(node, O_RDWR | O_CLOEXEC);
    if (P.fd < 0) {
        fprintf(stderr, PR_TAG "cannot open %s: %s\n", node, strerror(errno));
        return 0;
    }
    if (!findOutputLocked())
        fprintf(stderr, PR_TAG "%s has no connected connector: rendering works, nothing is scanned out\n", node);
    else
        fprintf(stderr, PR_TAG "%s: connector %u, crtc %u, mode %s %ux%u@%u\n", node, P.connectorId, P.crtcId,
                P.mode.name, (unsigned)P.mode.hdisplay, (unsigned)P.mode.vdisplay, P.mode.vrefresh);
    return 1;
}

/* The gbm device and its EGL display, created on first use. */
static EGLDisplay gbmDisplayLocked(void) {
    ApakGpu *g = &P.gpu;
    const char *ext;
    if (P.ready) return P.gbmDisplay;
    P.ready = 1;
    if (!openDrmLocked()) return EGL_NO_DISPLAY;
    P.gbmDevice = g->gbm_create_device(P.fd);
    if (!P.gbmDevice) {
        fprintf(stderr, PR_TAG "gbm_create_device failed\n");
        return EGL_NO_DISPLAY;
    }
    ext = g->eglQueryString(EGL_NO_DISPLAY, EGL_EXTENSIONS);
    if (ext && strstr(ext, "platform_gbm") && g->eglGetPlatformDisplayEXT)
        P.gbmDisplay = g->eglGetPlatformDisplayEXT(EGL_PLATFORM_GBM_KHR, P.gbmDevice, NULL);
    if (!P.gbmDisplay) P.gbmDisplay = g->eglGetDisplay(P.gbmDevice);
    if (!P.gbmDisplay) fprintf(stderr, PR_TAG "no EGL display on the gbm device\n");
    return P.gbmDisplay;
}

/* ── scanout ─────────────────────────────────────────────────────────── */

static void boDestroy(void *bo, void *data) {
    BoFb *b = (BoFb *)data;
    (void)bo;
    if (!b) return;
    if (b->fb && P.gpu.drmModeRmFB) P.gpu.drmModeRmFB(b->fd, b->fb);
    free(b);
}

static uint32_t fbForBo(void *bo) {
    ApakGpu *g = &P.gpu;
    BoFb *b = (BoFb *)g->gbm_bo_get_user_data(bo);
    uint32_t w, h, stride, format, handle, fb = 0;
    uint32_t handles[4] = { 0 }, pitches[4] = { 0 }, offsets[4] = { 0 };
    int rc = -1;
    if (b) return b->fb;
    w = g->gbm_bo_get_width(bo);
    h = g->gbm_bo_get_height(bo);
    stride = g->gbm_bo_get_stride(bo);
    format = g->gbm_bo_get_format(bo);
    handle = (uint32_t)g->gbm_bo_get_handle(bo); /* union gbm_bo_handle .u32 */
    handles[0] = handle;
    pitches[0] = stride;
    if (g->drmModeAddFB2) rc = g->drmModeAddFB2(P.fd, w, h, format, handles, pitches, offsets, &fb, 0);
    if (rc && g->drmModeAddFB) {
        uint8_t depth = format == GBM_FORMAT_RGB565 ? 16 : (format == GBM_FORMAT_ARGB8888 || format == GBM_FORMAT_ABGR8888) ? 32 : 24;
        uint8_t bpp = format == GBM_FORMAT_RGB565 ? 16 : 32;
        rc = g->drmModeAddFB(P.fd, w, h, depth, bpp, stride, handle, &fb);
    }
    if (rc) {
        if (ONCE(1)) fprintf(stderr, PR_TAG "drmModeAddFB failed (%d): the buffer format cannot be scanned out\n", rc);
        return 0;
    }
    b = (BoFb *)malloc(sizeof *b);
    if (!b) {
        g->drmModeRmFB(P.fd, fb);
        return 0;
    }
    b->fb = fb;
    b->fd = P.fd;
    g->gbm_bo_set_user_data(bo, b, boDestroy);
    return fb;
}

static void onFlip(int fd, unsigned sequence, unsigned tvSec, unsigned tvUsec, void *data) {
    (void)fd;
    (void)sequence;
    (void)tvSec;
    (void)tvUsec;
    *(volatile int *)data = 0;
}

static void setCrtcLocked(PresentSurface *ps, uint32_t fb) {
    ApakGpu *g = &P.gpu;
    int rc;
    if (!P.savedCrtc && !P.crtcTouched) P.savedCrtc = g->drmModeGetCrtc(P.fd, P.crtcId);
    P.crtcTouched = 1;
    rc = g->drmModeSetCrtc(P.fd, P.crtcId, fb, 0, 0, &P.connectorId, 1, &P.mode);
    if (rc) {
        if (ONCE(2))
            fprintf(stderr, PR_TAG "drmModeSetCrtc failed (%d, %s) — another process holds DRM master "
                            "(the hub must stop presenting while the game runs)\n", rc, strerror(errno));
    } else {
        ps->crtcSet = 1;
    }
}

static void presentGbmLocked(PresentSurface *ps) {
    ApakGpu *g = &P.gpu;
    void *bo = g->gbm_surface_lock_front_buffer(ps->gbmSurface);
    uint32_t fb;
    if (!bo) {
        if (ONCE(4)) fprintf(stderr, PR_TAG "gbm_surface_lock_front_buffer returned no buffer\n");
        return;
    }
    fb = P.haveMode ? fbForBo(bo) : 0;
    if (fb) {
        if (!ps->crtcSet || ps->flipBroken || !g->drmModePageFlip || !g->drmHandleEvent) {
            setCrtcLocked(ps, fb);
        } else {
            volatile int waiting = 1;
            int rc = g->drmModePageFlip(P.fd, P.crtcId, fb, DRM_MODE_PAGE_FLIP_EVENT, (void *)&waiting);
            if (rc) {
                ps->flipBroken = 1;
                if (ONCE(8)) fprintf(stderr, PR_TAG "page flip failed (%d) — falling back to drmModeSetCrtc per frame\n", rc);
                setCrtcLocked(ps, fb);
            } else {
                ApakDrmEventContext ctx;
                memset(&ctx, 0, sizeof ctx);
                ctx.version = 2;
                ctx.page_flip_handler = onFlip;
                while (waiting) {
                    struct pollfd pfd;
                    int pr;
                    pfd.fd = P.fd;
                    pfd.events = POLLIN;
                    pfd.revents = 0;
                    pr = poll(&pfd, 1, 100);
                    if (pr <= 0) {
                        if (ONCE(16)) fprintf(stderr, PR_TAG "no page-flip event within 100 ms\n");
                        break;
                    }
                    g->drmHandleEvent(P.fd, &ctx);
                }
            }
        }
    }
    if (ps->prevBo) g->gbm_surface_release_buffer(ps->gbmSurface, ps->prevBo);
    ps->prevBo = bo;
    ps->frames++;
}

/* ── the SDL-hosted backend ──────────────────────────────────────────── */

/* A public report from the stock firmware: raw EGL contexts on the display
 * of SDL's "mali" fbdev driver stayed black, SDL-owned ones showed. So this
 * backend lets the firmware's own SDL2 build the window, its EGL surface and
 * the GLES context, and hands the game exactly those EGL objects:
 *   eglCreateWindowSurface(ANativeWindow*) -> SDL_CreateWindow (fullscreen;
 *       the game's config sizes become SDL's GL attributes) and
 *       SDL_GL_CreateContext; the game gets SDL's EGL surface, read back
 *       with eglGetCurrentSurface while SDL's context is current
 *   eglCreateContext -> SDL's context (made again when the game asks for
 *       another GLES version; a game that makes its context first builds
 *       SDL's window from that config); a second, shared context stays
 *       plain EGL, and so does one made before the window on a config
 *       without EGL_WINDOW_BIT (an engine's pbuffer probe must not fix
 *       SDL's window config)
 *   eglMakeCurrent   -> SDL_GL_MakeCurrent for SDL's surface+context, so
 *       SDL's own per-thread "current window" stays true — SDL_GL_SwapWindow
 *       silently ignores a window that is not current on the calling thread
 *   eglReleaseThread -> SDL forgets its per-thread record first (else a
 *       later SDL_GL_MakeCurrent is an "already current" no-op: black)
 *   eglSwapBuffers   -> SDL_GL_SwapWindow, which returns nothing: a failed
 *       swap (EGL_CONTEXT_LOST included) cannot reach the game
 *   eglDestroySurface / eglTerminate on SDL's objects -> kept until exit (a
 *       pausing game gets the same surface back)
 * When auto hands over at the window surface, a context the game already
 * made stays raw EGL (the game holds its handle): its frames go through
 * plain eglSwapBuffers, and the log names ANDROIDPAK_PRESENT=sdl, which
 * hands the game SDL's context from its first eglCreateContext.
 * SDL is pointed at the game's own libEGL (SDL_VIDEO_EGL_DRIVER) and the
 * GLES library beside it (SDL_VIDEO_GL_DRIVER), so both sides talk to one
 * EGL instance. SDL_NO_SIGNAL_HANDLERS keeps SIGTERM fatal (JukaHub stops
 * games with signals, nobody reads SDL's quit event) and
 * SDL_INPUT_LINUX_KEEP_KBD leaves the console keyboard alone (the runner
 * _exit()s without SDL_Quit); see sdlEnvDefaults(). State lives in S,
 * guarded by P.lock; dpy, surf and window are also read unlocked (LOAD). */

static struct {
    ApakSdl sdl;
    int tried, up;          /* load + video init attempted / succeeded    */
    char why[512];          /* why SDL is not usable                      */
    char driver[32];        /* SDL_GetCurrentVideoDriver()                */
    EGLDisplay defaultDpy;  /* eglGetDisplay(EGL_DEFAULT_DISPLAY)         */
    ApakSdlWindow *window;
    int windowFailed;
    ApakSdlGLContext glctx;
    EGLDisplay dpy;         /* SDL's EGL objects, read back from EGL      */
    EGLSurface surf;
    EGLContext ctx;
    int ctxMajor, ctxMinor;
    int ctxGiven;           /* the game holds SDL's context               */
    EGLint w, h;
    unsigned frames;        /* through SDL_GL_SwapWindow (atomic)         */
    unsigned rawFrames;     /* plain eglSwapBuffers on SDL's surface      */
} S;

static int onStockTrimui(void);

static const char *sdlError(void) {
    const char *e = S.sdl.SDL_GetError ? S.sdl.SDL_GetError() : NULL;
    return e && *e ? e : "no SDL error text";
}

#define SDL_RESOLVE(field, required)                                    \
    do {                                                                \
        *(void **)&S.sdl.field = rt_dlsym(S.sdl.lib, #field);           \
        if ((required) && !S.sdl.field && !missing) missing = #field;   \
    } while (0)

/* The firmware's SDL2 first on the stock firmware (a copy another package
 * put on the library path may lack its video driver); ANDROIDPAK_SDL_LIB
 * overrides. Only libSDL2-2.0.so.0 names: a bare libSDL2.so on the path is
 * an SDL game's own bionic build, which glibc cannot load. */
static int sdlLoadLocked(void) {
    static const char *const stock[] = {
        "/usr/lib/libSDL2-2.0.so.0", "libSDL2-2.0.so.0", "/usr/trimui/lib/libSDL2-2.0.so.0", NULL,
    };
    static const char *const others[] = {
        "libSDL2-2.0.so.0", "/usr/lib/libSDL2-2.0.so.0", "/usr/lib/aarch64-linux-gnu/libSDL2-2.0.so.0", NULL,
    };
    const char *override = getenv("ANDROIDPAK_SDL_LIB");
    const char *const *paths = onStockTrimui() ? stock : others;
    const char *missing = NULL;
    size_t i = 0;
    if (S.sdl.lib) return 1;
    if (override && *override) {
        S.sdl.lib = rt_dlopen(override, 0);
        if (!S.sdl.lib) {
            snprintf(S.why, sizeof S.why, "%.200s not loadable: %.250s", override, rt_dlerror());
            return 0;
        }
        apak_strcpy(S.sdl.name, sizeof S.sdl.name, override);
    }
    for (i = 0; !S.sdl.lib && paths[i]; i++) {
        S.sdl.lib = rt_dlopen(paths[i], 0);
        if (S.sdl.lib) apak_strcpy(S.sdl.name, sizeof S.sdl.name, paths[i]);
    }
    if (!S.sdl.lib) {
        snprintf(S.why, sizeof S.why, "no libSDL2-2.0.so.0 (%zu places tried; ANDROIDPAK_SDL_LIB overrides)", i);
        return 0;
    }
    SDL_RESOLVE(SDL_InitSubSystem, 1);
    SDL_RESOLVE(SDL_GetError, 1);
    SDL_RESOLVE(SDL_GL_SetAttribute, 1);
    SDL_RESOLVE(SDL_CreateWindow, 1);
    SDL_RESOLVE(SDL_GL_CreateContext, 1);
    SDL_RESOLVE(SDL_GL_MakeCurrent, 1);
    SDL_RESOLVE(SDL_GL_SwapWindow, 1);
    SDL_RESOLVE(SDL_GL_DeleteContext, 1);
    SDL_RESOLVE(SDL_QuitSubSystem, 0);
    SDL_RESOLVE(SDL_GetVersion, 0);
    SDL_RESOLVE(SDL_GetCurrentVideoDriver, 0);
    SDL_RESOLVE(SDL_GL_LoadLibrary, 0);
    SDL_RESOLVE(SDL_DestroyWindow, 0);
    SDL_RESOLVE(SDL_GL_GetCurrentWindow, 0);
    if (missing) {
        snprintf(S.why, sizeof S.why, "%.250s lacks %s (not an SDL2 build?)", S.sdl.name, missing);
        rt_dlclose(S.sdl.lib);
        memset(&S.sdl, 0, sizeof S.sdl); /* no pointer into the closed library survives */
        return 0;
    }
    return 1;
}

/* The GLES library SDL loads before its EGL (SDL_VIDEO_GL_DRIVER):
 * ANDROIDPAK_GLES2_LIB when set — what bionicld.c binds the game's
 * libGLESv2.so to — else the one beside the chosen libEGL, versioned name
 * first like bionicld.c (the stock firmware ships only /usr/lib/libGLESv2.so,
 * and SDL's default libGLESv2.so.2 would not load there). NULL when libEGL
 * came from the library path: SDL's default searches that same path. */
static const char *sdlGlesLib(char *buf, size_t cap) {
    static const char *const names[] = { "libGLESv2.so.2", "libGLESv2.so" };
    const char *env = getenv("ANDROIDPAK_GLES2_LIB");
    const char *slash = strrchr(P.gpu.eglName, '/');
    size_t i;
    if (env && *env) return env;
    if (P.gpu.eglName[0] != '/' || !slash) return NULL;
    for (i = 0; i < sizeof names / sizeof names[0]; i++) {
        int n = snprintf(buf, cap, "%.*s/%s", (int)(slash - P.gpu.eglName), P.gpu.eglName, names[i]);
        if (n > 0 && (size_t)n < cap && access(buf, R_OK) == 0) return buf;
    }
    return NULL;
}

/* SDL reads these from the environment (older SDL2 releases only there), so
 * they are set here, at start, while the runner is still single-threaded — a
 * setenv racing a game thread's getenv can crash. Only when SDL may run
 * (ANDROIDPAK_PRESENT=sdl, or auto on the native backend): children and a
 * game's own libSDL2 inherit them. Defaults only: whatever the environment
 * already says wins. */
static void sdlEnvDefaults(void) {
    char gles[320];
    const char *glesLib = sdlGlesLib(gles, sizeof gles);
    setenv("SDL_VIDEO_EGL_DRIVER", P.gpu.eglName, 0);
    if (glesLib) setenv("SDL_VIDEO_GL_DRIVER", glesLib, 0);
    setenv("SDL_NO_SIGNAL_HANDLERS", "1", 0);
    setenv("SDL_INPUT_LINUX_KEEP_KBD", "1", 0);
}

/* Load SDL2 and start its video subsystem, once per process. 1 = up. */
static int sdlStartLocked(void) {
    ApakGpu *g = &P.gpu;
    ApakSdlVersion v = { 0, 0, 0 };
    const char *drv;
    if (S.up) return 1;
    if (S.tried) return 0;
    S.tried = 1;
    if (!g->eglGetCurrentDisplay || !g->eglGetCurrentSurface || !g->eglGetCurrentContext || !g->eglMakeCurrent ||
        !g->eglCreateContext) {
        snprintf(S.why, sizeof S.why, "%.250s lacks eglGetCurrent*/eglMakeCurrent/eglCreateContext", g->eglName);
        return 0;
    }
    if (!sdlLoadLocked()) return 0;
    /* the environment SDL reads was prepared at start (sdlEnvDefaults) */
    if (S.sdl.SDL_InitSubSystem(APAK_SDL_INIT_VIDEO) != 0) {
        snprintf(S.why, sizeof S.why, "SDL_InitSubSystem(VIDEO) failed: %.400s", sdlError());
        return 0;
    }
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_PROFILE_MASK, APAK_SDL_GL_CONTEXT_PROFILE_ES);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_MAJOR_VERSION, 2);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_MINOR_VERSION, 0);
    /* SDL's EGL display, initialised now: a fallback decided at start must
     * know that SDL's EGL really comes up. */
    if (S.sdl.SDL_GL_LoadLibrary && S.sdl.SDL_GL_LoadLibrary(NULL) != 0) {
        snprintf(S.why, sizeof S.why, "SDL_GL_LoadLibrary failed: %.400s", sdlError());
        if (S.sdl.SDL_QuitSubSystem) S.sdl.SDL_QuitSubSystem(APAK_SDL_INIT_VIDEO);
        return 0;
    }
    drv = S.sdl.SDL_GetCurrentVideoDriver ? S.sdl.SDL_GetCurrentVideoDriver() : NULL;
    apak_strcpy(S.driver, sizeof S.driver, drv ? drv : "?");
    if (S.sdl.SDL_GetVersion) S.sdl.SDL_GetVersion(&v);
    S.defaultDpy = g->eglGetDisplay(EGL_DEFAULT_DISPLAY);
    S.up = 1;
    fprintf(stderr, PR_TAG "SDL2 %s %u.%u.%u, video driver %s\n", S.sdl.name, (unsigned)v.major, (unsigned)v.minor,
            (unsigned)v.patch, S.driver);
    return 1;
}

static EGLint cfgAttr(EGLDisplay dpy, EGLConfig cfg, EGLint attr, EGLint fallback) {
    EGLint v = 0;
    if (!dpy || !cfg || P.gpu.eglGetConfigAttrib(dpy, cfg, attr, &v) != EGL_TRUE) return fallback;
    return v;
}

/* SDL_GL_CreateContext makes the new context current on this thread: read
 * SDL's EGL objects back, then give the thread back what it had — through
 * SDL first, so SDL's per-thread record never names a context EGL no longer
 * has current here. */
static int sdlContextLocked(int major, int minor) {
    ApakGpu *g = &P.gpu;
    EGLDisplay prevDpy = g->eglGetCurrentDisplay();
    EGLSurface prevDraw = g->eglGetCurrentSurface(EGL_DRAW), prevRead = g->eglGetCurrentSurface(EGL_READ);
    EGLContext prevCtx = g->eglGetCurrentContext();
    ApakSdlGLContext glctx;
    EGLDisplay dpy;
    EGLSurface surf;
    EGLContext ctx;
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_PROFILE_MASK, APAK_SDL_GL_CONTEXT_PROFILE_ES);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_MAJOR_VERSION, major);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_MINOR_VERSION, minor);
    glctx = S.sdl.SDL_GL_CreateContext(S.window);
    if (!glctx) {
        fprintf(stderr, PR_TAG "SDL_GL_CreateContext (GLES %d.%d) failed: %s\n", major, minor, sdlError());
        return 0;
    }
    dpy = g->eglGetCurrentDisplay();
    surf = g->eglGetCurrentSurface(EGL_DRAW);
    ctx = g->eglGetCurrentContext();
    S.sdl.SDL_GL_MakeCurrent(NULL, NULL);
    if (prevCtx != EGL_NO_CONTEXT) g->eglMakeCurrent(prevDpy, prevDraw, prevRead, prevCtx);
    if (!dpy || !surf || !ctx) {
        fprintf(stderr, PR_TAG "SDL's context is not current in %s: SDL drives another EGL (point SDL_VIDEO_EGL_DRIVER "
                        "and ANDROIDPAK_EGL_LIB at one library)\n", g->eglName);
        S.sdl.SDL_GL_DeleteContext(glctx);
        return 0;
    }
    STORE(S.dpy, dpy);
    STORE(S.surf, surf);
    S.ctx = ctx;
    S.glctx = glctx;
    S.ctxMajor = major;
    S.ctxMinor = minor;
    S.ctxGiven = 0;
    return 1;
}

/* SDL's window, EGL surface and a first context, created once from the
 * game's config: its sizes become SDL's GL attributes, so SDL picks the
 * closest config, ES3-capable when the game's is. major 0 = the game has not
 * said: GLES 2 until its eglCreateContext asks for another version. */
static int sdlWindowLocked(EGLDisplay dpy, EGLConfig cfg, int major, int minor) {
    ApakGpu *g = &P.gpu;
    ApakSdlWindow *window;
    int w = 0, h = 0, es3;
    EGLint r, gr, b, a, depth, stencil, samples;
    if (S.window) return 1;
    if (S.windowFailed || !sdlStartLocked()) return 0;
    r = cfgAttr(dpy, cfg, EGL_RED_SIZE, 8);
    gr = cfgAttr(dpy, cfg, EGL_GREEN_SIZE, 8);
    b = cfgAttr(dpy, cfg, EGL_BLUE_SIZE, 8);
    a = cfgAttr(dpy, cfg, EGL_ALPHA_SIZE, 0);
    depth = cfgAttr(dpy, cfg, EGL_DEPTH_SIZE, 24);
    stencil = cfgAttr(dpy, cfg, EGL_STENCIL_SIZE, 8);
    samples = cfgAttr(dpy, cfg, EGL_SAMPLE_BUFFERS, 0) > 0 ? cfgAttr(dpy, cfg, EGL_SAMPLES, 0) : 0;
    es3 = (cfgAttr(dpy, cfg, EGL_RENDERABLE_TYPE, 0) & EGL_OPENGL_ES3_BIT_KHR) != 0 || major >= 3;
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_RED_SIZE, r);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_GREEN_SIZE, gr);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_BLUE_SIZE, b);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_ALPHA_SIZE, a);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_DEPTH_SIZE, depth);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_STENCIL_SIZE, stencil);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_DOUBLEBUFFER, 1);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_MULTISAMPLEBUFFERS, samples > 0);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_MULTISAMPLESAMPLES, samples > 0 ? samples : 0);
    /* SDL chooses its config with the version set at window creation */
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_PROFILE_MASK, APAK_SDL_GL_CONTEXT_PROFILE_ES);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_MAJOR_VERSION, es3 ? 3 : 2);
    S.sdl.SDL_GL_SetAttribute(APAK_SDL_GL_CONTEXT_MINOR_VERSION, 0);
    apak_devicePanel(&w, &h);
    window = S.sdl.SDL_CreateWindow("AndroidRT", APAK_SDL_WINDOWPOS_UNDEFINED, APAK_SDL_WINDOWPOS_UNDEFINED, w, h,
                                    APAK_SDL_WINDOW_OPENGL | APAK_SDL_WINDOW_FULLSCREEN_DESKTOP |
                                        APAK_SDL_WINDOW_SHOWN);
    if (!window) {
        S.windowFailed = 1;
        fprintf(stderr, PR_TAG "SDL_CreateWindow failed: %s\n", sdlError());
        return 0;
    }
    STORE(S.window, window);
    if (!sdlContextLocked(major > 0 ? major : 2, major > 0 ? minor : 0)) {
        STORE(S.window, (ApakSdlWindow *)NULL);
        if (S.sdl.SDL_DestroyWindow) S.sdl.SDL_DestroyWindow(window);
        S.windowFailed = 1;
        return 0;
    }
    if (!g->eglQuerySurface || g->eglQuerySurface(S.dpy, S.surf, EGL_WIDTH, &S.w) != EGL_TRUE ||
        g->eglQuerySurface(S.dpy, S.surf, EGL_HEIGHT, &S.h) != EGL_TRUE || S.w <= 0 || S.h <= 0) {
        S.w = w;
        S.h = h;
    }
    fprintf(stderr, PR_TAG "SDL window %dx%d (video driver %s): config rgba %d%d%d%d depth %d stencil %d%s, GLES %d.%d "
                    "context\n", (int)S.w, (int)S.h, S.driver, (int)r, (int)gr, (int)b, (int)a, (int)depth, (int)stencil,
            samples > 0 ? ", multisampled" : "", S.ctxMajor, S.ctxMinor);
    if (S.defaultDpy && S.dpy != S.defaultDpy)
        fprintf(stderr, PR_TAG "SDL's EGL display is not eglGetDisplay(EGL_DEFAULT_DISPLAY) — the game's own calls on "
                        "its display may miss SDL's surface (on a DRM device use ANDROIDPAK_PRESENT=gbm)\n");
    return 1;
}

/* The game's ANativeWindow becomes SDL's window surface. `fallback` says
 * why the native backend hands over to SDL (NULL: SDL is the backend). */
static EGLSurface sdlWindowSurface(EGLDisplay dpy, EGLConfig cfg, void *win, const char *fallback) {
    EGLSurface surf = EGL_NO_SURFACE;
    pthread_mutex_lock(&P.lock);
    if (sdlWindowLocked(dpy, cfg, 0, 0)) {
        if (fallback) {
            setBackendLocked(BACKEND_SDL);
            fprintf(stderr, PR_TAG "backend sdl (fallback: %s; EGL %s, SDL %s, video driver %s)\n", fallback,
                    P.gpu.eglName, S.sdl.name, S.driver);
            if (P.rawContexts > 0)
                fprintf(stderr, PR_TAG "the game made its GLES context before its window — it stays raw EGL (its frames "
                                "go through plain eglSwapBuffers, not SDL); if the panel is black set "
                                "ANDROIDPAK_PRESENT=sdl\n");
        }
        if (ANativeWindow_getWidth(win) != S.w || ANativeWindow_getHeight(win) != S.h)
            fprintf(stderr, PR_TAG "the game asked for %dx%d buffers; SDL's window is %dx%d — the game renders at "
                            "SDL's size (eglQuerySurface says so)\n", ANativeWindow_getWidth(win),
                    ANativeWindow_getHeight(win), (int)S.w, (int)S.h);
        fprintf(stderr, PR_TAG "window surface %dx%d from SDL\n", (int)S.w, (int)S.h);
        surf = S.surf;
    } else if (fallback && ONCE(1024)) {
        fprintf(stderr, PR_TAG "%s, and the SDL fallback is not usable (%s)\n", fallback,
                S.why[0] ? S.why : "see above");
    }
    pthread_mutex_unlock(&P.lock);
    return surf;
}

/* 1 when surf is SDL's and the frame went through SDL_GL_SwapWindow. That
 * returns nothing, so the game gets EGL_TRUE even for a swap that failed
 * (EGL_CONTEXT_LOST cannot reach it on this backend). */
static int sdlSwap(EGLSurface surf) {
    ApakSdlWindow *window;
    if (backendNow() != BACKEND_SDL || !surf || surf != LOAD(S.surf)) return 0;
    window = LOAD(S.window);
    if (S.sdl.SDL_GL_GetCurrentWindow && S.sdl.SDL_GL_GetCurrentWindow() != window) {
        /* SDL would drop the frame: SDL's context is not current here */
        __atomic_fetch_add(&S.rawFrames, 1u, __ATOMIC_RELAXED);
        if (ONCE(32))
            fprintf(stderr, PR_TAG "eglSwapBuffers on SDL's surface while SDL's context is not current on this thread "
                            "(the game made its own context current, or draws from another thread) — plain "
                            "eglSwapBuffers, not presented by SDL\n");
        return 0;
    }
    S.sdl.SDL_GL_SwapWindow(window);
    if (__atomic_fetch_add(&S.frames, 1u, __ATOMIC_RELAXED) == 0)
        fprintf(stderr, PR_TAG "first frame presented through SDL_GL_SwapWindow\n");
    return 1;
}

/* ── the interposed EGL entry points ─────────────────────────────────── */

static PresentSurface *findSurfaceLocked(EGLSurface surf) {
    PresentSurface *ps;
    for (ps = P.surfaces; ps; ps = ps->next)
        if (ps->surf == surf) return ps;
    return NULL;
}

static EGLDisplay pr_eglGetDisplay(void *native) {
    int backend = backendNow();
    EGLDisplay dpy = LOAD(S.dpy);
    if (backend == BACKEND_SDL && native == EGL_DEFAULT_DISPLAY && dpy) return dpy;
    if (backend == BACKEND_GBM && native == EGL_DEFAULT_DISPLAY) {
        pthread_mutex_lock(&P.lock);
        dpy = gbmDisplayLocked();
        pthread_mutex_unlock(&P.lock);
        return dpy;
    }
    return P.gpu.eglGetDisplay ? P.gpu.eglGetDisplay(native) : EGL_NO_DISPLAY;
}

/* auto on the native backend: a default display that does not initialise
 * hands over to SDL — its video driver may be what brings the panel's EGL
 * up, and on the mali driver SDL's display is eglGetDisplay(EGL_DEFAULT_
 * DISPLAY) — and the game's call is made once more. Decided here, at the
 * game's own eglInitialize, not at start: a run that never draws
 * (ANDROIDPAK_SCAN, a game that fails earlier) never touches the display,
 * and a display the hub releases late is not misjudged. The second call
 * also gives the game EGL's own error back (reading it for the log cleared
 * it; EGL has no way to set it). */
static EGLBoolean pr_eglInitialize(EGLDisplay dpy, EGLint *major, EGLint *minor) {
    ApakGpu *g = &P.gpu;
    char why[96];
    int sdl = 0;
    if (g->eglInitialize(dpy, major, minor) == EGL_TRUE) return EGL_TRUE;
    if (!P.fallback || dpy == EGL_NO_DISPLAY || backendNow() != BACKEND_NATIVE) return EGL_FALSE; /* EGL's error */
    snprintf(why, sizeof why, "the native EGL display did not initialise (0x%x)", (unsigned)g->eglGetError());
    pthread_mutex_lock(&P.lock);
    if (!P.initTried && P.backend == BACKEND_NATIVE && dpy == g->eglGetDisplay(EGL_DEFAULT_DISPLAY)) {
        P.initTried = 1;
        if (sdlStartLocked()) {
            setBackendLocked(BACKEND_SDL);
            sdl = 1;
            fprintf(stderr, PR_TAG "backend sdl (fallback: %s; EGL %s, SDL %s, video driver %s)\n", why, g->eglName,
                    S.sdl.name, S.driver);
        } else {
            fprintf(stderr, PR_TAG "%s, and SDL2 is not usable either (%s) — staying native\n", why, S.why);
        }
    }
    pthread_mutex_unlock(&P.lock);
    if (g->eglInitialize(dpy, major, minor) == EGL_TRUE) {
        if (sdl) fprintf(stderr, PR_TAG "the game's eglInitialize succeeded once SDL's video driver ran\n");
        return EGL_TRUE;
    }
    if (sdl)
        fprintf(stderr, PR_TAG "SDL2 is up, but eglInitialize of the default display still fails — SDL's EGL display is "
                        "another one (a KMSDRM SDL?) that the game cannot reach; on a DRM device use "
                        "ANDROIDPAK_PRESENT=gbm\n");
    return EGL_FALSE;
}

/* Configs whose native visual is a GBM format first (stable), so the game's
 * usual "take the first" choice can be scanned out. */
static EGLBoolean pr_eglChooseConfig(EGLDisplay dpy, const EGLint *attribs, EGLConfig *configs, EGLint size,
                                     EGLint *num) {
    ApakGpu *g = &P.gpu;
    EGLint total = 0, i, w = 0;
    EGLConfig *all;
    if (backendNow() != BACKEND_GBM || dpy != P.gbmDisplay || !configs || size <= 0)
        return g->eglChooseConfig(dpy, attribs, configs, size, num);
    if (g->eglChooseConfig(dpy, attribs, NULL, 0, &total) != EGL_TRUE) return EGL_FALSE;
    if (total <= 0) {
        if (num) *num = 0;
        return EGL_TRUE;
    }
    all = (EGLConfig *)calloc((size_t)total, sizeof *all);
    if (!all) return g->eglChooseConfig(dpy, attribs, configs, size, num);
    if (g->eglChooseConfig(dpy, attribs, all, total, &total) != EGL_TRUE) {
        free(all);
        return EGL_FALSE;
    }
    for (i = 0; i < total && w < size; i++) {
        EGLint v = 0;
        if (g->eglGetConfigAttrib(dpy, all[i], EGL_NATIVE_VISUAL_ID, &v) == EGL_TRUE && isGbmVisual(v)) configs[w++] = all[i];
    }
    for (i = 0; i < total && w < size; i++) {
        EGLint v = 0;
        if (!(g->eglGetConfigAttrib(dpy, all[i], EGL_NATIVE_VISUAL_ID, &v) == EGL_TRUE && isGbmVisual(v)))
            configs[w++] = all[i];
    }
    free(all);
    if (num) *num = w;
    return EGL_TRUE;
}

static EGLSurface pr_eglCreateWindowSurface(EGLDisplay dpy, EGLConfig cfg, void *win, const EGLint *attribs) {
    ApakGpu *g = &P.gpu;
    EGLSurface surf = EGL_NO_SURFACE;
    int backend;
    if (!win || win != android_currentWindow()) return g->eglCreateWindowSurface(dpy, cfg, win, attribs);
    backend = backendNow();
    if (backend == BACKEND_SDL) {
        surf = sdlWindowSurface(dpy, cfg, win, NULL);
        if (surf != EGL_NO_SURFACE || !S.windowFailed) return surf;
        /* SDL started but cannot make a window: the raw window, last chance */
        fprintf(stderr, PR_TAG "SDL could not create the window — trying the native backend\n");
        pthread_mutex_lock(&P.lock);
        setBackendLocked(BACKEND_NATIVE);
        pthread_mutex_unlock(&P.lock);
        backend = BACKEND_NATIVE;
    }
    if (backend == BACKEND_NATIVE) {
        char reason[96];
        P.fbdevWindow.width = (uint16_t)ANativeWindow_getWidth(win);
        P.fbdevWindow.height = (uint16_t)ANativeWindow_getHeight(win);
        surf = g->eglCreateWindowSurface(dpy, cfg, P.nativeWindowValue, attribs);
        if (surf != EGL_NO_SURFACE || !P.fallback) return surf;
        snprintf(reason, sizeof reason, "the native window surface was refused (0x%x)", (unsigned)g->eglGetError());
        surf = sdlWindowSurface(dpy, cfg, win, reason);
        /* SDL is not usable either: the native call once more, so the game
         * reads EGL's own error (reading it for the reason cleared it) */
        if (surf == EGL_NO_SURFACE) surf = g->eglCreateWindowSurface(dpy, cfg, P.nativeWindowValue, attribs);
        return surf;
    }
    if (backend == BACKEND_GBM) {
        EGLint visual = 0;
        uint32_t w, h;
        void *gs;
        PresentSurface *ps;
        pthread_mutex_lock(&P.lock);
        gbmDisplayLocked(); /* the gbm device, even if the game got its display elsewhere */
        if (g->eglGetConfigAttrib(dpy, cfg, EGL_NATIVE_VISUAL_ID, &visual) != EGL_TRUE || !isGbmVisual(visual))
            visual = (EGLint)GBM_FORMAT_XRGB8888;
        w = P.haveMode ? P.mode.hdisplay : (uint32_t)ANativeWindow_getWidth(win);
        h = P.haveMode ? P.mode.vdisplay : (uint32_t)ANativeWindow_getHeight(win);
        if ((int32_t)w != ANativeWindow_getWidth(win) || (int32_t)h != ANativeWindow_getHeight(win))
            fprintf(stderr, PR_TAG "the game asked for %dx%d buffers; the panel is %ux%u and KMS cannot scale yet — "
                            "rendering at panel size\n", ANativeWindow_getWidth(win), ANativeWindow_getHeight(win), w, h);
        gs = P.gbmDevice ? g->gbm_surface_create(P.gbmDevice, w, h, (uint32_t)visual,
                                                 GBM_BO_USE_SCANOUT | GBM_BO_USE_RENDERING)
                         : NULL;
        if (gs) surf = g->eglCreateWindowSurface(dpy, cfg, gs, attribs);
        if (!surf) {
            fprintf(stderr, PR_TAG "eglCreateWindowSurface on a %ux%u gbm surface failed (0x%x)\n", w, h,
                    (unsigned)g->eglGetError());
            if (gs) g->gbm_surface_destroy(gs);
            pthread_mutex_unlock(&P.lock);
            return EGL_NO_SURFACE;
        }
        ps = (PresentSurface *)calloc(1, sizeof *ps);
        if (ps) {
            ps->dpy = dpy;
            ps->surf = surf;
            ps->gbmSurface = gs;
            ps->next = P.surfaces;
            P.surfaces = ps;
        }
        fprintf(stderr, PR_TAG "window surface %ux%u on gbm (visual 0x%08x)\n", w, h, (unsigned)visual);
        pthread_mutex_unlock(&P.lock);
        return surf;
    }
    return g->eglCreateWindowSurface(dpy, cfg, win, attribs);
}

static void afterSwap(EGLSurface surf) {
    PresentSurface *ps;
    if (backendNow() != BACKEND_GBM) return;
    pthread_mutex_lock(&P.lock);
    ps = findSurfaceLocked(surf);
    if (ps) presentGbmLocked(ps);
    pthread_mutex_unlock(&P.lock);
}

static EGLBoolean pr_eglSwapBuffers(EGLDisplay dpy, EGLSurface surf) {
    EGLBoolean ok;
    if (sdlSwap(surf)) return EGL_TRUE;
    ok = P.gpu.eglSwapBuffers(dpy, surf);
    if (ok == EGL_TRUE) afterSwap(surf);
    return ok;
}

static EGLBoolean pr_eglSwapBuffersWithDamageKHR(EGLDisplay dpy, EGLSurface surf, const EGLint *rects, EGLint n) {
    EGLBoolean ok;
    if (sdlSwap(surf)) return EGL_TRUE; /* SDL presents the whole frame */
    ok = P.gpu.eglSwapBuffersWithDamageKHR ? P.gpu.eglSwapBuffersWithDamageKHR(dpy, surf, rects, n)
                                           : P.gpu.eglSwapBuffers(dpy, surf);
    if (ok == EGL_TRUE) afterSwap(surf);
    return ok;
}

static EGLBoolean pr_eglSwapBuffersWithDamageEXT(EGLDisplay dpy, EGLSurface surf, const EGLint *rects, EGLint n) {
    EGLBoolean ok;
    if (sdlSwap(surf)) return EGL_TRUE;
    ok = P.gpu.eglSwapBuffersWithDamageEXT ? P.gpu.eglSwapBuffersWithDamageEXT(dpy, surf, rects, n)
                                           : P.gpu.eglSwapBuffers(dpy, surf);
    if (ok == EGL_TRUE) afterSwap(surf);
    return ok;
}

/* GLES contexts pass straight through, except on the sdl backend: the
 * game's window context is SDL's (see "the SDL-hosted backend"). */
static EGLContext pr_eglCreateContext(EGLDisplay dpy, EGLConfig cfg, EGLContext share, const EGLint *attribs) {
    ApakGpu *g = &P.gpu;
    EGLContext ctx;
    int major = 1, minor = 0, i; /* EGL_CONTEXT_CLIENT_VERSION defaults to 1 */
    if (!g->eglCreateContext) return EGL_NO_CONTEXT;
    if (backendNow() != BACKEND_SDL) {
        ctx = g->eglCreateContext(dpy, cfg, share, attribs);
        if (ctx != EGL_NO_CONTEXT && P.fallback) {
            pthread_mutex_lock(&P.lock);
            if (P.backend == BACKEND_NATIVE) P.rawContexts++; /* stays raw should SDL take over at the window */
            pthread_mutex_unlock(&P.lock);
        }
        return ctx;
    }
    for (i = 0; attribs && i < 64 && attribs[i] != EGL_NONE; i += 2) {
        if (attribs[i] == EGL_CONTEXT_CLIENT_VERSION) major = attribs[i + 1];
        else if (attribs[i] == EGL_CONTEXT_MINOR_VERSION_KHR) minor = attribs[i + 1];
    }
    pthread_mutex_lock(&P.lock);
    if (!S.window &&
        (share != EGL_NO_CONTEXT || !(cfgAttr(dpy, cfg, EGL_SURFACE_TYPE, EGL_WINDOW_BIT) & EGL_WINDOW_BIT))) {
        /* before the window: an engine's probe on a pbuffer-only config, or
         * a context sharing one — SDL's window waits for a window config */
        pthread_mutex_unlock(&P.lock);
        if (ONCE(2048))
            fprintf(stderr, PR_TAG "a GLES context made before the window on a config without EGL_WINDOW_BIT (or "
                            "sharing another) is plain EGL; SDL's window waits for a window config\n");
        return g->eglCreateContext(dpy, cfg, share, attribs);
    }
    if (!sdlWindowLocked(dpy, cfg, major, minor)) {
        /* like pr_eglCreateWindowSurface: the native backend, last chance */
        setBackendLocked(BACKEND_NATIVE);
        pthread_mutex_unlock(&P.lock);
        fprintf(stderr, PR_TAG "SDL could not create the window — the game's context is plain EGL (native)\n");
        return g->eglCreateContext(dpy, cfg, share, attribs);
    }
    if (S.ctxGiven || share != EGL_NO_CONTEXT) {
        /* a loader thread's context: plain EGL on SDL's display, sharing */
        if (ONCE(64)) fprintf(stderr, PR_TAG "a second GLES context is a plain EGL context on SDL's display\n");
        ctx = g->eglCreateContext(S.dpy, cfg, share, attribs);
        pthread_mutex_unlock(&P.lock);
        return ctx;
    }
    if (S.ctx && (S.ctxMajor != major || S.ctxMinor != minor)) {
        S.sdl.SDL_GL_DeleteContext(S.glctx); /* made for another version */
        S.glctx = NULL;
        S.ctx = EGL_NO_CONTEXT;
    }
    if (!S.ctx && !sdlContextLocked(major, minor)) {
        pthread_mutex_unlock(&P.lock);
        return EGL_NO_CONTEXT;
    }
    S.ctxGiven = 1;
    ctx = S.ctx;
    pthread_mutex_unlock(&P.lock);
    fprintf(stderr, PR_TAG "the game's GLES %d.%d context is SDL's\n", major, minor);
    return ctx;
}

static EGLBoolean pr_eglMakeCurrent(EGLDisplay dpy, EGLSurface draw, EGLSurface read, EGLContext ctx) {
    ApakGpu *g = &P.gpu;
    if (backendNow() == BACKEND_SDL && LOAD(S.window)) {
        int rc;
        pthread_mutex_lock(&P.lock);
        if (ctx != EGL_NO_CONTEXT && ctx == S.ctx && draw == S.surf && read == S.surf) {
            rc = S.sdl.SDL_GL_MakeCurrent(S.window, S.glctx);
            pthread_mutex_unlock(&P.lock);
            if (rc == 0) return EGL_TRUE;
            if (ONCE(128)) fprintf(stderr, PR_TAG "SDL_GL_MakeCurrent failed: %s\n", sdlError());
            return EGL_FALSE;
        }
        /* anything else is plain EGL, once SDL forgets what it had here */
        S.sdl.SDL_GL_MakeCurrent(NULL, NULL);
        pthread_mutex_unlock(&P.lock);
    }
    return g->eglMakeCurrent ? g->eglMakeCurrent(dpy, draw, read, ctx) : EGL_FALSE;
}

/* SDL keeps its own per-thread "current" record: it must forget it with
 * EGL's, or a later eglMakeCurrent of SDL's objects meets SDL's "already
 * current" shortcut and the thread draws with nothing current (black). */
static EGLBoolean pr_eglReleaseThread(void) {
    if (backendNow() == BACKEND_SDL && LOAD(S.window)) {
        pthread_mutex_lock(&P.lock);
        S.sdl.SDL_GL_MakeCurrent(NULL, NULL);
        pthread_mutex_unlock(&P.lock);
    }
    return P.gpu.eglReleaseThread ? P.gpu.eglReleaseThread() : EGL_TRUE;
}

static EGLBoolean pr_eglDestroyContext(EGLDisplay dpy, EGLContext ctx) {
    ApakGpu *g = &P.gpu;
    int backend = backendNow();
    EGLBoolean ok;
    if (backend == BACKEND_SDL && ctx != EGL_NO_CONTEXT) {
        pthread_mutex_lock(&P.lock);
        if (ctx == S.ctx) {
            S.sdl.SDL_GL_DeleteContext(S.glctx); /* SDL releases it first when it is current here */
            S.glctx = NULL;
            S.ctx = EGL_NO_CONTEXT;
            S.ctxGiven = 0;
            pthread_mutex_unlock(&P.lock);
            fprintf(stderr, PR_TAG "SDL's context deleted with the game's\n");
            return EGL_TRUE;
        }
        pthread_mutex_unlock(&P.lock);
    }
    ok = g->eglDestroyContext ? g->eglDestroyContext(dpy, ctx) : EGL_FALSE;
    if (ok == EGL_TRUE && backend == BACKEND_NATIVE && P.fallback) {
        pthread_mutex_lock(&P.lock);
        if (P.rawContexts > 0) P.rawContexts--; /* gone before any window: nothing stays raw */
        pthread_mutex_unlock(&P.lock);
    }
    return ok;
}

/* SDL's display must outlive a game that terminates EGL on pause. */
static EGLBoolean pr_eglTerminate(EGLDisplay dpy) {
    if (backendNow() == BACKEND_SDL && dpy && (dpy == LOAD(S.dpy) || dpy == S.defaultDpy)) {
        if (ONCE(256)) fprintf(stderr, PR_TAG "eglTerminate ignored: SDL owns the display until exit\n");
        return EGL_TRUE;
    }
    return P.gpu.eglTerminate(dpy);
}

static EGLBoolean pr_eglDestroySurface(EGLDisplay dpy, EGLSurface surf) {
    ApakGpu *g = &P.gpu;
    PresentSurface **pp, *ps = NULL;
    EGLBoolean ok;
    if (backendNow() == BACKEND_SDL && surf && surf == LOAD(S.surf)) {
        if (ONCE(512))
            fprintf(stderr, PR_TAG "eglDestroySurface on SDL's window surface: kept — SDL owns it until exit and the "
                            "next eglCreateWindowSurface gets it back\n");
        return EGL_TRUE;
    }
    pthread_mutex_lock(&P.lock);
    for (pp = &P.surfaces; *pp; pp = &(*pp)->next) {
        if ((*pp)->surf == surf) {
            ps = *pp;
            *pp = ps->next;
            break;
        }
    }
    ok = g->eglDestroySurface(dpy, surf);
    if (ps) {
        if (ps->prevBo) g->gbm_surface_release_buffer(ps->gbmSurface, ps->prevBo);
        g->gbm_surface_destroy(ps->gbmSurface); /* frees the bos: boDestroy removes their framebuffers */
        fprintf(stderr, PR_TAG "window surface destroyed after %u frames\n", ps->frames);
        free(ps);
    }
    pthread_mutex_unlock(&P.lock);
    return ok;
}

static void *pr_eglGetProcAddress(const char *name);

#define SYM(name, fn) { name, (void *)(fn) }
static const ApakSym kEglOverrides[] = {
    SYM("eglGetDisplay", pr_eglGetDisplay),
    SYM("eglInitialize", pr_eglInitialize),
    SYM("eglChooseConfig", pr_eglChooseConfig),
    SYM("eglCreateWindowSurface", pr_eglCreateWindowSurface),
    SYM("eglSwapBuffers", pr_eglSwapBuffers),
    SYM("eglSwapBuffersWithDamageKHR", pr_eglSwapBuffersWithDamageKHR),
    SYM("eglSwapBuffersWithDamageEXT", pr_eglSwapBuffersWithDamageEXT),
    SYM("eglDestroySurface", pr_eglDestroySurface),
    SYM("eglGetProcAddress", pr_eglGetProcAddress),
    SYM("eglCreateContext", pr_eglCreateContext),
    SYM("eglMakeCurrent", pr_eglMakeCurrent),
    SYM("eglReleaseThread", pr_eglReleaseThread),
    SYM("eglDestroyContext", pr_eglDestroyContext),
    SYM("eglTerminate", pr_eglTerminate),
    { NULL, NULL },
};

static pthread_once_t g_glesProcsOnce = PTHREAD_ONCE_INIT;
static void *g_glesProcs;

static void openGlesProcs(void) {
    char path[320];
    const char *lib = sdlGlesLib(path, sizeof path);
    if (!lib) lib = "libGLESv2.so.2";
    g_glesProcs = rt_dlopen(lib, 0);
}

static void *pr_eglGetProcAddress(const char *name) {
    size_t i;
    void *p;
    for (i = 0; name && kEglOverrides[i].name; i++)
        if (strcmp(kEglOverrides[i].name, name) == 0) return kEglOverrides[i].addr;
    if (!name) return NULL;
    p = P.gpu.eglGetProcAddress ? P.gpu.eglGetProcAddress(name) : NULL;
    if (p) return p;
    /* EGL 1.4 need not return GLES core functions from eglGetProcAddress.
     * Android NDK loaders commonly ask it for glGetString and friends anyway;
     * the firmware's matching GLES library exports those by name. */
    pthread_once(&g_glesProcsOnce, openGlesProcs);
    return g_glesProcs ? rt_dlsym(g_glesProcs, name) : NULL;
}

/* ── lifecycle ───────────────────────────────────────────────────────── */

void apak_presentInit(void);
void apak_presentShutdown(void);

/* The stock TrimUI firmware: JukaHub says so, or its system folder is here. */
static int onStockTrimui(void) {
    const char *dev = getenv("ANDROIDPAK_DEVICE");
    if (dev && *dev) return strncmp(dev, "trimui", 6) == 0;
    return access("/usr/trimui", F_OK) == 0;
}

/* The stock firmware of the PowerVR GE8300 models (Smart Pro, Brick, Brick
 * Pro, Hammer — every board but the Smart Pro S, tg5050, whose GPU really is
 * a Mali). Their SDL2 calls its only real video driver "mali" (SDL's generic
 * fbdev-EGL driver over PowerVR's fbdev EGL), and two ports on this firmware
 * found the same thing: GLES contexts made with raw EGL draw nothing (a black
 * panel, sound playing, no error) while SDL-owned ones show (NextOS ports
 * issue #1, Horizon Chase on the Brick Pro: HC_PURE_SDL_CONTEXTS=1; nx-redux
 * PR #152: keep the contexts SDL-owned on tg5040). A black panel without an
 * error is the one failure auto's native-to-sdl fallback cannot see, so on
 * these boards auto starts on the SDL-hosted backend. */
static int onPowerVrTrimui(void) {
    const ApakDevice *dev;
    if (!onStockTrimui()) return 0;
    dev = apak_device();
    return !(dev && dev->board && strcmp(dev->board, "tg5050") == 0);
}

void apak_presentInit(void) {
    static const char *const firmwareEgl[] = {
        "/usr/lib/libEGL.so", "/usr/lib/libEGL.so.1", "/usr/trimui/lib/libEGL.so", NULL,
    };
    const char *mode = getenv("ANDROIDPAK_PRESENT");
    const char *window = getenv("ANDROIDPAK_EGL_WINDOW");
    const char *scan = getenv("ANDROIDPAK_SCAN");
    const char *eglOverride = getenv("ANDROIDPAK_EGL_LIB");
    char why[512];
    int layers, want, trimui = onStockTrimui(), fbdev, backend;
    int sdlAsked = 0, autoSdl = 0, scanRun = scan && *scan && strcmp(scan, "0") != 0;

    if (mode && strcmp(mode, "off") == 0) {
        fprintf(stderr, PR_TAG "disabled (ANDROIDPAK_PRESENT=off): EGL calls go to the host unchanged\n");
        return;
    }
    want = !mode || !*mode || strcmp(mode, "auto") == 0 ? -1
           : strcmp(mode, "gbm") == 0                   ? BACKEND_GBM
           : strcmp(mode, "native") == 0                ? BACKEND_NATIVE
           : strcmp(mode, "sdl") == 0                   ? BACKEND_SDL
                                                        : -2;
    if (want == -2) {
        fprintf(stderr, PR_TAG "ANDROIDPAK_PRESENT=%s is not auto|gbm|native|sdl|off — using auto\n", mode);
        want = -1;
    }
    if (trimui) {
        /* The firmware's PowerVR libEGL first: a copy some other package put
         * on LD_LIBRARY_PATH may not know this panel. */
        char ewhy[256];
        if (apak_gpuLoadEglFrom(&P.gpu, firmwareEgl, ewhy, sizeof ewhy) & APAK_GPU_EGL)
            fprintf(stderr, PR_TAG "stock firmware (%s): EGL from %s\n", apak_device()->model, P.gpu.eglName);
        else if (eglOverride && *eglOverride) /* an explicit library never falls back to another */
            fprintf(stderr, PR_TAG "stock firmware (%s): ANDROIDPAK_EGL_LIB %s\n", apak_device()->model, ewhy);
        else
            fprintf(stderr, PR_TAG "stock firmware (%s): no EGL at the firmware's paths (%s) — searching the library "
                            "path\n", apak_device()->model, ewhy[0] ? ewhy : "not found");
    }
    layers = apak_gpuLoad(&P.gpu, why, sizeof why);
    if (!(layers & APAK_GPU_EGL)) {
        fprintf(stderr, PR_TAG "no usable libEGL (%s) — a game that draws will fail at its first EGL call\n", why);
        return;
    }
    /* The game's libEGL.so is this exact library (not whatever the search
     * path would find): the interposed calls and the rest share one EGL. */
    apak_ldSetHostLib("libEGL.so", P.gpu.egl, P.gpu.eglName);
    if (want == BACKEND_SDL && scan && *scan && strcmp(scan, "0") != 0) {
        /* an import scan draws nothing: SDL's video init (framebuffer,
         * input devices) is not worth it */
        fprintf(stderr, PR_TAG "ANDROIDPAK_PRESENT=sdl: SDL2 is not started for an ANDROIDPAK_SCAN run\n");
        want = BACKEND_NATIVE;
    }
    if (want == BACKEND_SDL) {
        sdlAsked = 1;
        sdlEnvDefaults(); /* SDL reads them in SDL_InitSubSystem, next */
        pthread_mutex_lock(&P.lock);
        if (sdlStartLocked()) setBackendLocked(BACKEND_SDL);
        pthread_mutex_unlock(&P.lock);
        if (backendNow() != BACKEND_SDL) {
            fprintf(stderr, PR_TAG "ANDROIDPAK_PRESENT=sdl but SDL2 is not usable (%s) — choosing the backend like "
                            "auto\n", S.why);
            want = -1; /* gbm when the device has it, else native */
        }
    }
    if (want == -1 || want == BACKEND_GBM) {
        int gbmOk = (layers & (APAK_GPU_GBM | APAK_GPU_DRM)) == (APAK_GPU_GBM | APAK_GPU_DRM);
        pthread_mutex_lock(&P.lock);
        if (gbmOk && openDrmLocked() && (P.haveMode || want == BACKEND_GBM)) setBackendLocked(BACKEND_GBM);
        pthread_mutex_unlock(&P.lock);
        if (want == BACKEND_GBM && backendNow() != BACKEND_GBM)
            fprintf(stderr, PR_TAG "ANDROIDPAK_PRESENT=gbm but the GBM/KMS stack is not usable (%s) — using native\n",
                    why[0] ? why : "no DRM card");
    }
    /* auto on the PowerVR stock firmware with no KMS panel: SDL-owned
     * contexts first (see onPowerVrTrimui), native when SDL2 does not start.
     * An import scan draws nothing and starts no SDL. */
    if (want == -1 && !sdlAsked && !scanRun && backendNow() != BACKEND_GBM && onPowerVrTrimui()) {
        sdlEnvDefaults(); /* SDL reads them in SDL_InitSubSystem, next */
        pthread_mutex_lock(&P.lock);
        if (sdlStartLocked()) setBackendLocked(BACKEND_SDL);
        pthread_mutex_unlock(&P.lock);
        if (backendNow() == BACKEND_SDL)
            autoSdl = 1;
        else
            fprintf(stderr, PR_TAG "stock firmware (%s): SDL2 is not usable (%s) — using the native backend\n",
                    apak_device()->model, S.why);
    }
    fbdev = trimui; /* the firmware's window contract; ANDROIDPAK_EGL_WINDOW decides when set */
    if (window && strcmp(window, "fbdev") == 0) fbdev = 1;
    else if (window && strcmp(window, "null") == 0) fbdev = 0;
    P.nativeWindowValue = fbdev ? (void *)&P.fbdevWindow : NULL; /* also the sdl backend's last resort */
    pthread_mutex_lock(&P.lock);
    if (P.backend != BACKEND_GBM && P.backend != BACKEND_SDL) {
        setBackendLocked(BACKEND_NATIVE);
        /* auto: SDL takes over when the native backend fails — at the
         * game's eglInitialize of the default display (pr_eglInitialize) or
         * at its window surface (pr_eglCreateWindowSurface) */
        P.fallback = want == -1;
    }
    backend = P.backend;
    pthread_mutex_unlock(&P.lock);
    if (P.fallback) sdlEnvDefaults(); /* SDL may start later, on a game thread: set while single-threaded */
    apak_compatAddOverrides(kEglOverrides);
    if (backend == BACKEND_SDL) {
        if (autoSdl)
            fprintf(stderr, PR_TAG "stock firmware (%s): auto chose the SDL-hosted backend — raw EGL contexts on this "
                            "firmware's mali fbdev display stay black (ANDROIDPAK_PRESENT=native forces them)\n",
                    apak_device()->model);
        fprintf(stderr, PR_TAG "backend sdl (EGL %s, SDL %s, video driver %s)\n", P.gpu.eglName, S.sdl.name, S.driver);
        return;
    }
    fprintf(stderr, PR_TAG "backend %s (EGL %s%s%s%s)\n", backendName(backend), P.gpu.eglName,
            backend == BACKEND_GBM ? ", GBM " : "", backend == BACKEND_GBM ? P.gpu.gbmName : "",
            backend != BACKEND_NATIVE ? ""
            : fbdev                   ? ", window: fbdev {width, height} struct"
                                      : ", window: null");
}

/* Put back what the hub was showing before the game took the CRTC. SDL's
 * window is left to the process exit that follows: game threads may still
 * be drawing into it. */
void apak_presentShutdown(void) {
    pthread_mutex_lock(&P.lock);
    if (P.backend == BACKEND_GBM && P.crtcTouched && P.savedCrtc && P.savedCrtc->mode_valid) {
        P.gpu.drmModeSetCrtc(P.fd, P.savedCrtc->crtc_id, P.savedCrtc->buffer_id, P.savedCrtc->x, P.savedCrtc->y,
                             &P.connectorId, 1, &P.savedCrtc->mode);
        fprintf(stderr, PR_TAG "restored the previous CRTC framebuffer %u\n", P.savedCrtc->buffer_id);
    }
    if (P.backend == BACKEND_SDL && S.window) {
        unsigned frames = __atomic_load_n(&S.frames, __ATOMIC_RELAXED);
        unsigned raw = __atomic_load_n(&S.rawFrames, __ATOMIC_RELAXED);
        if (raw)
            fprintf(stderr, PR_TAG "SDL presented %u frames (%u more went through plain eglSwapBuffers on its surface)\n",
                    frames, raw);
        else
            fprintf(stderr, PR_TAG "SDL presented %u frames\n", frames);
    }
    pthread_mutex_unlock(&P.lock);
}
