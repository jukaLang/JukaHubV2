/* mock_gpu.c — a fake glibc GPU userspace (libEGL + libgbm + libdrm in one
 * library) for the present-layer smoke test. The runner is pointed at it
 * with ANDROIDPAK_EGL_LIB / ANDROIDPAK_GBM_LIB / ANDROIDPAK_DRM_LIB and
 * ANDROIDPAK_DRM_NODE=/dev/null; every call the present layer makes is
 * logged as "[mockgpu] ..." so run-smoke.sh can assert the exact sequence:
 * platform-GBM display, config visual order, gbm surface in the chosen
 * visual, the ANativeWindow replaced by the gbm surface, one modeset then
 * page flips, framebuffers removed with their buffers, the CRTC restored.
 *
 * The panel: one connector (31) → encoder (51) → CRTC (41) showing the
 * "hub's" framebuffer 7, mode 1280x720@60.
 *
 * Contexts follow EGL's rules closely enough to catch a wrong pairing:
 * eglSwapBuffers fails (EGL_BAD_SURFACE) unless the surface is current with
 * a context. Knobs model a native EGL path that fails, for the sdl-backend
 * fallback tests (fake_sdl.c drives this same library):
 *   MOCKGPU_FBDEV_SIZE=WxH  the "framebuffer" only accepts an fbdev
 *                           {uint16 width, height} window of exactly WxH
 *                           (EGL_BAD_NATIVE_WINDOW otherwise);
 *   MOCKGPU_NEEDS_SDL=1     eglInitialize fails (EGL_NOT_INITIALIZED) until
 *                           the fake SDL's video init has run (it calls
 *                           mockgpu_sdlVideoUp) — a display only SDL's
 *                           driver brings up;
 *   MOCKGPU_DEFAULT_DOWN=1  the EGL_DEFAULT_DISPLAY display never
 *                           initialises; another native display (the fake
 *                           SDL's FAKESDL_DISPLAY=device) is a second
 *                           display that does.
 * A config asked for without EGL_WINDOW_BIT in EGL_SURFACE_TYPE is config 3,
 * pbuffer-only, with no alpha/depth/stencil (an engine's probe config).
 */
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "eglhost.h"

#define LOG(...)                                   \
    do {                                           \
        fprintf(stderr, "[mockgpu] " __VA_ARGS__); \
        fputc('\n', stderr);                       \
    } while (0)

#define EXPORT __attribute__((visibility("default")))
#define MOCK_EGL_PBUFFER_BIT 0x0001

static const char *fcc(uint32_t f, char out[5]) {
    memcpy(out, &f, 4);
    out[4] = '\0';
    return out;
}

/* ── EGL ─────────────────────────────────────────────────────────────── */
static EGLint g_err = EGL_SUCCESS;
static void *g_gbmSurface;
static int g_swaps;
static char g_mockSurfaceObj;
static char g_mockDisplayObj, g_mockOtherDisplayObj;
static char g_cfg[4]; /* 0-2 window configs, 3 the pbuffer-only probe config */
static char g_ctxObjs[4];
static int g_nextCtx;
static int g_sdlVideoUp;

static int cfgIndex(EGLConfig config) {
    int i;
    for (i = 0; i < 4; i++)
        if (config == &g_cfg[i]) return i;
    return 2;
}

/* the fake SDL's video init ran (MOCKGPU_NEEDS_SDL) */
EXPORT void mockgpu_sdlVideoUp(void) { g_sdlVideoUp = 1; }
static EGLint g_surfW, g_surfH;
static uint32_t g_gbmW, g_gbmH; /* the last gbm_surface_create */
/* the calling thread's current objects (the smoke games draw from one thread) */
static EGLDisplay g_curDpy;
static EGLSurface g_curSurf;
static EGLContext g_curCtx;

static const char *ctxName(EGLContext c) {
    static char buf[4][16];
    static int n;
    char *b = buf[n++ & 3];
    if (!c) return "none";
    if ((char *)c >= g_ctxObjs && (char *)c < g_ctxObjs + sizeof g_ctxObjs)
        snprintf(b, 16, "ctx%d", (int)((char *)c - g_ctxObjs));
    else
        snprintf(b, 16, "FOREIGN");
    return b;
}

EXPORT EGLint eglGetError(void) {
    EGLint e = g_err;
    g_err = EGL_SUCCESS;
    return e;
}
EXPORT EGLDisplay eglGetDisplay(void *native) {
    LOG("eglGetDisplay native=%s", native ? "non-default" : "EGL_DEFAULT_DISPLAY");
    if (native && getenv("MOCKGPU_DEFAULT_DOWN")) return &g_mockOtherDisplayObj;
    return &g_mockDisplayObj;
}
EXPORT EGLDisplay eglGetPlatformDisplayEXT(EGLenum platform, void *native, const EGLint *attribs) {
    (void)native;
    (void)attribs;
    LOG("eglGetPlatformDisplayEXT platform=0x%x", platform);
    return platform == EGL_PLATFORM_GBM_KHR ? &g_mockDisplayObj : EGL_NO_DISPLAY;
}
EXPORT EGLBoolean eglInitialize(EGLDisplay dpy, EGLint *major, EGLint *minor) {
    if (getenv("MOCKGPU_NEEDS_SDL") && !g_sdlVideoUp) {
        LOG("eglInitialize: the display is not up (MOCKGPU_NEEDS_SDL, no SDL video yet)");
        g_err = EGL_NOT_INITIALIZED;
        return EGL_FALSE;
    }
    if (getenv("MOCKGPU_DEFAULT_DOWN") && dpy == &g_mockDisplayObj) {
        LOG("eglInitialize: the default display never comes up (MOCKGPU_DEFAULT_DOWN)");
        g_err = EGL_NOT_INITIALIZED;
        return EGL_FALSE;
    }
    LOG("eglInitialize %s display", dpy == &g_mockOtherDisplayObj ? "the other" : "the default");
    if (major) *major = 1;
    if (minor) *minor = 4;
    return EGL_TRUE;
}
EXPORT EGLBoolean eglTerminate(EGLDisplay dpy) {
    (void)dpy;
    LOG("eglTerminate");
    return EGL_TRUE;
}
EXPORT const char *eglQueryString(EGLDisplay dpy, EGLint name) {
    if (!dpy && name == EGL_EXTENSIONS) return "EGL_EXT_platform_base EGL_KHR_platform_gbm EGL_MESA_platform_gbm";
    if (name == EGL_VENDOR) return "MockGPU";
    return "";
}
/* Three configs in "EGL sort order": the first has no native visual, then
 * ARGB8888, then XRGB8888 — the present layer must not hand out the first. */
EXPORT EGLBoolean eglChooseConfig(EGLDisplay dpy, const EGLint *attribs, EGLConfig *configs, EGLint size, EGLint *num) {
    int i, pairs = 0, sane = 0, pbufferOnly = 0;
    (void)dpy;
    for (i = 0; attribs && i < 64; i += 2) {
        if (attribs[i] == EGL_NONE) {
            sane = 1;
            break;
        }
        if (attribs[i] < 0x3020 || attribs[i] > 0x3100) break; /* not an EGL attribute name */
        if (attribs[i] == EGL_SURFACE_TYPE && !(attribs[i + 1] & EGL_WINDOW_BIT)) pbufferOnly = 1;
        pairs++;
    }
    if (configs) LOG("eglChooseConfig attribs %s (%d int32 pairs)", sane ? "ok" : "MALFORMED", pairs);
    if (!configs) {
        if (num) *num = pbufferOnly ? 1 : 3;
        return EGL_TRUE;
    }
    if (pbufferOnly) {
        if (size > 0) configs[0] = &g_cfg[3];
        if (num) *num = size > 0;
        return EGL_TRUE;
    }
    for (i = 0; i < 3 && i < size; i++) configs[i] = &g_cfg[i];
    if (num) *num = i;
    return EGL_TRUE;
}
/* Window configs 0-2: RGB888, 24-bit depth, 8-bit stencil, ES2+ES3; only
 * config 1 (ARGB8888) has alpha. Config 3: pbuffer-only RGB888, no depth or
 * stencil. */
EXPORT EGLBoolean eglGetConfigAttrib(EGLDisplay dpy, EGLConfig config, EGLint attribute, EGLint *value) {
    (void)dpy;
    if (config == &g_cfg[3] && (attribute == EGL_SURFACE_TYPE || attribute == EGL_DEPTH_SIZE ||
                                attribute == EGL_STENCIL_SIZE || attribute == EGL_NATIVE_VISUAL_ID)) {
        *value = attribute == EGL_SURFACE_TYPE ? MOCK_EGL_PBUFFER_BIT : 0;
        return EGL_TRUE;
    }
    switch (attribute) {
        case EGL_NATIVE_VISUAL_ID:
            *value = config == &g_cfg[0]   ? 0
                     : config == &g_cfg[1] ? (EGLint)GBM_FORMAT_ARGB8888
                                           : (EGLint)GBM_FORMAT_XRGB8888;
            break;
        case EGL_RED_SIZE:
        case EGL_GREEN_SIZE:
        case EGL_BLUE_SIZE: *value = 8; break;
        case EGL_ALPHA_SIZE: *value = config == &g_cfg[1] ? 8 : 0; break;
        case EGL_DEPTH_SIZE: *value = 24; break;
        case EGL_STENCIL_SIZE: *value = 8; break;
        case EGL_RENDERABLE_TYPE: *value = EGL_OPENGL_ES2_BIT | EGL_OPENGL_ES3_BIT_KHR; break;
        case EGL_SURFACE_TYPE: *value = EGL_WINDOW_BIT; break;
        default: *value = 0; break;
    }
    return EGL_TRUE;
}
EXPORT EGLSurface eglCreateWindowSurface(EGLDisplay dpy, EGLConfig config, void *win, const EGLint *attribs) {
    int cfg = cfgIndex(config);
    const char *fb = getenv("MOCKGPU_FBDEV_SIZE");
    (void)dpy;
    (void)attribs;
    if (win && win != g_gbmSurface) {
        /* the fbdev contract is a {uint16 width, height} struct */
        const unsigned short *wh = (const unsigned short *)win;
        unsigned fw = 0, fh = 0;
        if (fb && sscanf(fb, "%ux%u", &fw, &fh) == 2 && (wh[0] != fw || wh[1] != fh)) {
            LOG("eglCreateWindowSurface config=%d native=OTHER(%ux%u) refused: the framebuffer is %ux%u "
                "(EGL_BAD_NATIVE_WINDOW)", cfg, wh[0], wh[1], fw, fh);
            g_err = EGL_BAD_NATIVE_WINDOW;
            return EGL_NO_SURFACE;
        }
        LOG("eglCreateWindowSurface config=%d native=OTHER(%ux%u)", cfg, wh[0], wh[1]);
        g_surfW = wh[0];
        g_surfH = wh[1];
    } else {
        LOG("eglCreateWindowSurface config=%d native=%s", cfg, win ? "gbm-surface" : "null");
        g_surfW = win ? (EGLint)g_gbmW : 1280; /* the null window system is the whole panel */
        g_surfH = win ? (EGLint)g_gbmH : 720;
    }
    return &g_mockSurfaceObj;
}
EXPORT EGLBoolean eglDestroySurface(EGLDisplay dpy, EGLSurface surface) {
    (void)dpy;
    (void)surface;
    LOG("eglDestroySurface after %d swaps", g_swaps);
    return EGL_TRUE;
}
EXPORT EGLBoolean eglQuerySurface(EGLDisplay dpy, EGLSurface surface, EGLint attribute, EGLint *value) {
    (void)dpy;
    if (surface != &g_mockSurfaceObj || !value) {
        g_err = EGL_BAD_SURFACE;
        return EGL_FALSE;
    }
    *value = attribute == EGL_WIDTH ? g_surfW : attribute == EGL_HEIGHT ? g_surfH : 0;
    return EGL_TRUE;
}
EXPORT EGLContext eglCreateContext(EGLDisplay dpy, EGLConfig config, EGLContext share, const EGLint *attribs) {
    int i, version = 1, minor = 0;
    EGLContext ctx;
    (void)dpy;
    for (i = 0; attribs && i < 32 && attribs[i] != EGL_NONE; i += 2) {
        if (attribs[i] == EGL_CONTEXT_CLIENT_VERSION) version = attribs[i + 1];
        else if (attribs[i] == EGL_CONTEXT_MINOR_VERSION_KHR) minor = attribs[i + 1];
    }
    if (g_nextCtx >= (int)sizeof g_ctxObjs) {
        g_err = EGL_BAD_ALLOC;
        return EGL_NO_CONTEXT;
    }
    ctx = &g_ctxObjs[g_nextCtx++];
    LOG("eglCreateContext config=%d GLES %d.%d share=%s -> %s", cfgIndex(config), version, minor, ctxName(share),
        ctxName(ctx));
    return ctx;
}
EXPORT EGLBoolean eglDestroyContext(EGLDisplay dpy, EGLContext ctx) {
    (void)dpy;
    LOG("eglDestroyContext %s", ctxName(ctx));
    if (ctx == g_curCtx) g_curCtx = NULL; /* EGL defers it; the test only needs the name */
    return EGL_TRUE;
}
EXPORT EGLBoolean eglMakeCurrent(EGLDisplay dpy, EGLSurface draw, EGLSurface read, EGLContext ctx) {
    (void)read;
    if (ctx && strcmp(ctxName(ctx), "FOREIGN") == 0) {
        LOG("eglMakeCurrent with a context this EGL never made (EGL_BAD_CONTEXT)");
        g_err = EGL_BAD_CONTEXT;
        return EGL_FALSE;
    }
    g_curDpy = ctx ? dpy : NULL;
    g_curSurf = ctx ? draw : NULL;
    g_curCtx = ctx;
    LOG("eglMakeCurrent %s surface=%s", ctxName(ctx), draw ? "window" : "none");
    return EGL_TRUE;
}
EXPORT EGLBoolean eglReleaseThread(void) {
    LOG("eglReleaseThread (%s was current)", ctxName(g_curCtx));
    g_curDpy = NULL;
    g_curSurf = NULL;
    g_curCtx = NULL;
    return EGL_TRUE;
}
EXPORT EGLContext eglGetCurrentContext(void) { return g_curCtx; }
EXPORT EGLDisplay eglGetCurrentDisplay(void) { return g_curDpy; }
EXPORT EGLSurface eglGetCurrentSurface(EGLint readdraw) {
    (void)readdraw;
    return g_curSurf;
}
EXPORT EGLBoolean eglSwapBuffers(EGLDisplay dpy, EGLSurface surface) {
    (void)dpy;
    if (!g_curCtx || g_curSurf != surface) {
        LOG("eglSwapBuffers on a surface that is not current (EGL_BAD_SURFACE)");
        g_err = EGL_BAD_SURFACE;
        return EGL_FALSE;
    }
    g_swaps++;
    return EGL_TRUE;
}
EXPORT void *eglGetProcAddress(const char *name) {
    (void)name;
    return NULL;
}

/* ── GBM ─────────────────────────────────────────────────────────────── */
typedef struct {
    uint32_t w, h, stride, format, handle;
    void *user;
    void (*destroy)(void *bo, void *data);
} MockBo;
static MockBo g_bos[3];
static int g_nextBo, g_locked, g_released;
static char g_dev, g_surf;

EXPORT void *gbm_create_device(int fd) {
    (void)fd;
    LOG("gbm_create_device");
    return &g_dev;
}
EXPORT void gbm_device_destroy(void *dev) { (void)dev; }
EXPORT void *gbm_surface_create(void *dev, uint32_t w, uint32_t h, uint32_t format, uint32_t flags) {
    char f[5];
    int i;
    (void)dev;
    LOG("gbm_surface_create %ux%u format=%s flags=0x%x", w, h, fcc(format, f), flags);
    g_gbmW = w;
    g_gbmH = h;
    for (i = 0; i < 3; i++) {
        memset(&g_bos[i], 0, sizeof g_bos[i]);
        g_bos[i].w = w;
        g_bos[i].h = h;
        g_bos[i].stride = w * 4;
        g_bos[i].format = format;
        g_bos[i].handle = 10u + (uint32_t)i;
    }
    g_gbmSurface = &g_surf;
    return &g_surf;
}
EXPORT void gbm_surface_destroy(void *surface) {
    int i;
    (void)surface;
    LOG("gbm_surface_destroy (locked %d, released %d)", g_locked, g_released);
    for (i = 0; i < 3; i++)
        if (g_bos[i].destroy) g_bos[i].destroy(&g_bos[i], g_bos[i].user);
}
EXPORT void *gbm_surface_lock_front_buffer(void *surface) {
    MockBo *bo = &g_bos[g_nextBo];
    (void)surface;
    g_nextBo = (g_nextBo + 1) % 3;
    g_locked++;
    return bo;
}
EXPORT void gbm_surface_release_buffer(void *surface, void *bo) {
    (void)surface;
    (void)bo;
    g_released++;
}
EXPORT uint32_t gbm_bo_get_width(void *bo) { return ((MockBo *)bo)->w; }
EXPORT uint32_t gbm_bo_get_height(void *bo) { return ((MockBo *)bo)->h; }
EXPORT uint32_t gbm_bo_get_stride(void *bo) { return ((MockBo *)bo)->stride; }
EXPORT uint32_t gbm_bo_get_format(void *bo) { return ((MockBo *)bo)->format; }
EXPORT uint64_t gbm_bo_get_handle(void *bo) { return ((MockBo *)bo)->handle; }
EXPORT void gbm_bo_set_user_data(void *bo, void *data, void (*destroy)(void *bo, void *data)) {
    ((MockBo *)bo)->user = data;
    ((MockBo *)bo)->destroy = destroy;
}
EXPORT void *gbm_bo_get_user_data(void *bo) { return ((MockBo *)bo)->user; }

/* ── libdrm ─────────────────────────────────────────────────────────── */
static ApakDrmModeInfo g_mode = { 74250, 1280, 1390, 1430, 1650, 0, 720, 725, 730, 750, 0, 60, 0,
                                  DRM_MODE_TYPE_PREFERRED, "1280x720" };
static uint32_t g_crtcIds[1] = { 41 }, g_connIds[1] = { 31 }, g_encIds[1] = { 51 };
static uint32_t g_nextFb = 100;
static void *g_pendingFlip;

EXPORT ApakDrmRes *drmModeGetResources(int fd) {
    static ApakDrmRes res;
    (void)fd;
    memset(&res, 0, sizeof res);
    res.count_crtcs = 1;
    res.crtcs = g_crtcIds;
    res.count_connectors = 1;
    res.connectors = g_connIds;
    res.count_encoders = 1;
    res.encoders = g_encIds;
    return &res;
}
EXPORT void drmModeFreeResources(ApakDrmRes *res) { (void)res; }
EXPORT ApakDrmConnector *drmModeGetConnector(int fd, uint32_t id) {
    static ApakDrmConnector c;
    (void)fd;
    if (id != 31) return NULL;
    memset(&c, 0, sizeof c);
    c.connector_id = 31;
    c.encoder_id = 51;
    c.connection = DRM_MODE_CONNECTED;
    c.count_modes = 1;
    c.modes = &g_mode;
    c.count_encoders = 1;
    c.encoders = g_encIds;
    return &c;
}
EXPORT void drmModeFreeConnector(ApakDrmConnector *c) { (void)c; }
EXPORT ApakDrmEncoder *drmModeGetEncoder(int fd, uint32_t id) {
    static ApakDrmEncoder e;
    (void)fd;
    if (id != 51) return NULL;
    memset(&e, 0, sizeof e);
    e.encoder_id = 51;
    e.crtc_id = 41;
    e.possible_crtcs = 1;
    return &e;
}
EXPORT void drmModeFreeEncoder(ApakDrmEncoder *e) { (void)e; }
EXPORT ApakDrmCrtc *drmModeGetCrtc(int fd, uint32_t id) {
    static ApakDrmCrtc crtc;
    (void)fd;
    LOG("drmModeGetCrtc crtc=%u (saving what the hub shows)", id);
    memset(&crtc, 0, sizeof crtc);
    crtc.crtc_id = id;
    crtc.buffer_id = 7;
    crtc.width = 1280;
    crtc.height = 720;
    crtc.mode_valid = 1;
    crtc.mode = g_mode;
    return &crtc;
}
EXPORT void drmModeFreeCrtc(ApakDrmCrtc *c) { (void)c; }
EXPORT int drmModeAddFB2(int fd, uint32_t w, uint32_t h, uint32_t format, const uint32_t handles[4],
                         const uint32_t pitches[4], const uint32_t offsets[4], uint32_t *fb, uint32_t flags) {
    char f[5];
    (void)fd;
    (void)offsets;
    (void)flags;
    *fb = g_nextFb++;
    LOG("drmModeAddFB2 %ux%u format=%s handle=%u pitch=%u -> fb %u", w, h, fcc(format, f), handles[0], pitches[0], *fb);
    return 0;
}
EXPORT int drmModeAddFB(int fd, uint32_t w, uint32_t h, uint8_t depth, uint8_t bpp, uint32_t pitch, uint32_t handle,
                        uint32_t *fb) {
    (void)fd;
    *fb = g_nextFb++;
    LOG("drmModeAddFB %ux%u depth=%u bpp=%u pitch=%u handle=%u -> fb %u", w, h, depth, bpp, pitch, handle, *fb);
    return 0;
}
EXPORT int drmModeRmFB(int fd, uint32_t fb) {
    (void)fd;
    LOG("drmModeRmFB fb=%u", fb);
    return 0;
}
EXPORT int drmModeSetCrtc(int fd, uint32_t crtc, uint32_t fb, uint32_t x, uint32_t y, uint32_t *connectors, int count,
                          ApakDrmModeInfo *mode) {
    (void)fd;
    (void)x;
    (void)y;
    LOG("drmModeSetCrtc crtc=%u fb=%u connector=%u count=%d mode=%s", crtc, fb, connectors ? connectors[0] : 0, count,
        mode ? mode->name : "(none)");
    return 0;
}
EXPORT int drmModePageFlip(int fd, uint32_t crtc, uint32_t fb, uint32_t flags, void *userData) {
    (void)fd;
    LOG("drmModePageFlip crtc=%u fb=%u flags=%u", crtc, fb, flags);
    g_pendingFlip = userData;
    return 0;
}
EXPORT int drmHandleEvent(int fd, ApakDrmEventContext *ctx) {
    void *data = g_pendingFlip;
    g_pendingFlip = NULL;
    if (data && ctx && ctx->version >= 2 && ctx->page_flip_handler) ctx->page_flip_handler(fd, 0, 0, 0, data);
    return 0;
}
