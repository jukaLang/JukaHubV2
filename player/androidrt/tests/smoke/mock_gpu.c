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
 */
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "eglhost.h"

#define LOG(...)                                   \
    do {                                           \
        fprintf(stderr, "[mockgpu] " __VA_ARGS__); \
        fputc('\n', stderr);                       \
    } while (0)

#define EXPORT __attribute__((visibility("default")))

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
static char g_mockDisplayObj;
static char g_cfg[3];

EXPORT EGLint eglGetError(void) {
    EGLint e = g_err;
    g_err = EGL_SUCCESS;
    return e;
}
EXPORT EGLDisplay eglGetDisplay(void *native) {
    LOG("eglGetDisplay native=%s", native ? "non-default" : "EGL_DEFAULT_DISPLAY");
    return &g_mockDisplayObj;
}
EXPORT EGLDisplay eglGetPlatformDisplayEXT(EGLenum platform, void *native, const EGLint *attribs) {
    (void)native;
    (void)attribs;
    LOG("eglGetPlatformDisplayEXT platform=0x%x", platform);
    return platform == EGL_PLATFORM_GBM_KHR ? &g_mockDisplayObj : EGL_NO_DISPLAY;
}
EXPORT EGLBoolean eglInitialize(EGLDisplay dpy, EGLint *major, EGLint *minor) {
    (void)dpy;
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
    int i, pairs = 0, sane = 0;
    (void)dpy;
    for (i = 0; attribs && i < 64; i += 2) {
        if (attribs[i] == EGL_NONE) {
            sane = 1;
            break;
        }
        if (attribs[i] < 0x3020 || attribs[i] > 0x3100) break; /* not an EGL attribute name */
        pairs++;
    }
    if (configs) LOG("eglChooseConfig attribs %s (%d int32 pairs)", sane ? "ok" : "MALFORMED", pairs);
    if (!configs) {
        if (num) *num = 3;
        return EGL_TRUE;
    }
    for (i = 0; i < 3 && i < size; i++) configs[i] = &g_cfg[i];
    if (num) *num = i;
    return EGL_TRUE;
}
EXPORT EGLBoolean eglGetConfigAttrib(EGLDisplay dpy, EGLConfig config, EGLint attribute, EGLint *value) {
    (void)dpy;
    if (attribute == EGL_NATIVE_VISUAL_ID) {
        *value = config == &g_cfg[0] ? 0 : config == &g_cfg[1] ? (EGLint)GBM_FORMAT_ARGB8888 : (EGLint)GBM_FORMAT_XRGB8888;
        return EGL_TRUE;
    }
    *value = 0;
    return EGL_TRUE;
}
EXPORT EGLSurface eglCreateWindowSurface(EGLDisplay dpy, EGLConfig config, void *win, const EGLint *attribs) {
    (void)dpy;
    (void)attribs;
    LOG("eglCreateWindowSurface config=%d native=%s", config == &g_cfg[0] ? 0 : config == &g_cfg[1] ? 1 : 2,
        win && win == g_gbmSurface ? "gbm-surface" : win ? "OTHER" : "null");
    return &g_mockSurfaceObj;
}
EXPORT EGLBoolean eglDestroySurface(EGLDisplay dpy, EGLSurface surface) {
    (void)dpy;
    (void)surface;
    LOG("eglDestroySurface after %d swaps", g_swaps);
    return EGL_TRUE;
}
EXPORT EGLBoolean eglSwapBuffers(EGLDisplay dpy, EGLSurface surface) {
    (void)dpy;
    (void)surface;
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
