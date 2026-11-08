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
 * Backends (ANDROIDPAK_PRESENT=auto|gbm|native|off, default auto):
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
 *           by ANDROIDPAK_EGL_WINDOW: `null` (default) or `fbdev` (a Mali
 *           style {uint16 width, height} struct); present = eglSwapBuffers.
 *   off     no interposition at all (diagnostics only).
 *   auto    gbm when a DRM card with a connected connector, libgbm and
 *           libdrm are all present; native otherwise.
 *
 * UNVERIFIED ON HARDWARE: which backend the stock TrimUI firmware needs (its
 * display may be fbdev-only) and whether the hub releases the display while
 * a game runs. The smoke test drives the whole gbm path against mock
 * libEGL/libgbm/libdrm (tests/smoke/mock_gpu.c); `ANDROIDPAK_SURFACE=1`
 * prints the device's real answer.
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
#include "eglhost.h"

#define PR_TAG "[androidrt][present] "

enum { BACKEND_OFF = 0, BACKEND_NATIVE = 1, BACKEND_GBM = 2 };

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
    int backend;
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

static int onceFlags;
#define ONCE(bit) (!(onceFlags & (bit)) && (onceFlags |= (bit)))

static const char *backendName(int b) {
    return b == BACKEND_GBM ? "gbm" : b == BACKEND_NATIVE ? "native" : "off";
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

/* ── the interposed EGL entry points ─────────────────────────────────── */

static PresentSurface *findSurfaceLocked(EGLSurface surf) {
    PresentSurface *ps;
    for (ps = P.surfaces; ps; ps = ps->next)
        if (ps->surf == surf) return ps;
    return NULL;
}

static EGLDisplay pr_eglGetDisplay(void *native) {
    EGLDisplay dpy;
    if (P.backend == BACKEND_GBM && native == EGL_DEFAULT_DISPLAY) {
        pthread_mutex_lock(&P.lock);
        dpy = gbmDisplayLocked();
        pthread_mutex_unlock(&P.lock);
        return dpy;
    }
    return P.gpu.eglGetDisplay ? P.gpu.eglGetDisplay(native) : EGL_NO_DISPLAY;
}

/* Configs whose native visual is a GBM format first (stable), so the game's
 * usual "take the first" choice can be scanned out. */
static EGLBoolean pr_eglChooseConfig(EGLDisplay dpy, const EGLint *attribs, EGLConfig *configs, EGLint size,
                                     EGLint *num) {
    ApakGpu *g = &P.gpu;
    EGLint total = 0, i, w = 0;
    EGLConfig *all;
    if (P.backend != BACKEND_GBM || dpy != P.gbmDisplay || !configs || size <= 0)
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
    if (!win || win != android_currentWindow()) return g->eglCreateWindowSurface(dpy, cfg, win, attribs);
    if (P.backend == BACKEND_NATIVE) {
        P.fbdevWindow.width = (uint16_t)ANativeWindow_getWidth(win);
        P.fbdevWindow.height = (uint16_t)ANativeWindow_getHeight(win);
        return g->eglCreateWindowSurface(dpy, cfg, P.nativeWindowValue, attribs);
    }
    if (P.backend == BACKEND_GBM) {
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
    if (P.backend != BACKEND_GBM) return;
    pthread_mutex_lock(&P.lock);
    ps = findSurfaceLocked(surf);
    if (ps) presentGbmLocked(ps);
    pthread_mutex_unlock(&P.lock);
}

static EGLBoolean pr_eglSwapBuffers(EGLDisplay dpy, EGLSurface surf) {
    EGLBoolean ok = P.gpu.eglSwapBuffers(dpy, surf);
    if (ok == EGL_TRUE) afterSwap(surf);
    return ok;
}

static EGLBoolean pr_eglSwapBuffersWithDamageKHR(EGLDisplay dpy, EGLSurface surf, const EGLint *rects, EGLint n) {
    EGLBoolean ok = P.gpu.eglSwapBuffersWithDamageKHR ? P.gpu.eglSwapBuffersWithDamageKHR(dpy, surf, rects, n)
                                                      : P.gpu.eglSwapBuffers(dpy, surf);
    if (ok == EGL_TRUE) afterSwap(surf);
    return ok;
}

static EGLBoolean pr_eglSwapBuffersWithDamageEXT(EGLDisplay dpy, EGLSurface surf, const EGLint *rects, EGLint n) {
    EGLBoolean ok = P.gpu.eglSwapBuffersWithDamageEXT ? P.gpu.eglSwapBuffersWithDamageEXT(dpy, surf, rects, n)
                                                      : P.gpu.eglSwapBuffers(dpy, surf);
    if (ok == EGL_TRUE) afterSwap(surf);
    return ok;
}

static EGLBoolean pr_eglDestroySurface(EGLDisplay dpy, EGLSurface surf) {
    ApakGpu *g = &P.gpu;
    PresentSurface **pp, *ps = NULL;
    EGLBoolean ok;
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
    SYM("eglChooseConfig", pr_eglChooseConfig),
    SYM("eglCreateWindowSurface", pr_eglCreateWindowSurface),
    SYM("eglSwapBuffers", pr_eglSwapBuffers),
    SYM("eglSwapBuffersWithDamageKHR", pr_eglSwapBuffersWithDamageKHR),
    SYM("eglSwapBuffersWithDamageEXT", pr_eglSwapBuffersWithDamageEXT),
    SYM("eglDestroySurface", pr_eglDestroySurface),
    SYM("eglGetProcAddress", pr_eglGetProcAddress),
    { NULL, NULL },
};

static void *pr_eglGetProcAddress(const char *name) {
    size_t i;
    for (i = 0; name && kEglOverrides[i].name; i++)
        if (strcmp(kEglOverrides[i].name, name) == 0) return kEglOverrides[i].addr;
    return P.gpu.eglGetProcAddress ? P.gpu.eglGetProcAddress(name) : NULL;
}

/* ── lifecycle ───────────────────────────────────────────────────────── */

void apak_presentInit(void);
void apak_presentShutdown(void);

void apak_presentInit(void) {
    const char *mode = getenv("ANDROIDPAK_PRESENT");
    const char *window = getenv("ANDROIDPAK_EGL_WINDOW");
    char why[512];
    int layers, want;

    if (mode && strcmp(mode, "off") == 0) {
        fprintf(stderr, PR_TAG "disabled (ANDROIDPAK_PRESENT=off): EGL calls go to the host unchanged\n");
        return;
    }
    want = !mode || !*mode || strcmp(mode, "auto") == 0 ? -1
           : strcmp(mode, "gbm") == 0                   ? BACKEND_GBM
           : strcmp(mode, "native") == 0                ? BACKEND_NATIVE
                                                        : -1;
    layers = apak_gpuLoad(&P.gpu, why, sizeof why);
    if (!(layers & APAK_GPU_EGL)) {
        fprintf(stderr, PR_TAG "no usable libEGL (%s) — a game that draws will fail at its first EGL call\n", why);
        return;
    }
    if (want == -1 || want == BACKEND_GBM) {
        int gbmOk = (layers & (APAK_GPU_GBM | APAK_GPU_DRM)) == (APAK_GPU_GBM | APAK_GPU_DRM);
        pthread_mutex_lock(&P.lock);
        if (gbmOk && openDrmLocked() && (P.haveMode || want == BACKEND_GBM)) P.backend = BACKEND_GBM;
        pthread_mutex_unlock(&P.lock);
        if (want == BACKEND_GBM && P.backend != BACKEND_GBM)
            fprintf(stderr, PR_TAG "ANDROIDPAK_PRESENT=gbm but the GBM/KMS stack is not usable (%s) — using native\n",
                    why[0] ? why : "no DRM card");
    }
    if (P.backend != BACKEND_GBM) {
        P.backend = BACKEND_NATIVE;
        P.nativeWindowValue = window && strcmp(window, "fbdev") == 0 ? (void *)&P.fbdevWindow : NULL;
    }
    apak_compatAddOverrides(kEglOverrides);
    fprintf(stderr, PR_TAG "backend %s (EGL %s%s%s)\n", backendName(P.backend), P.gpu.eglName,
            P.backend == BACKEND_GBM ? ", GBM " : "", P.backend == BACKEND_GBM ? P.gpu.gbmName : "");
}

/* Put back what the hub was showing before the game took the CRTC. */
void apak_presentShutdown(void) {
    pthread_mutex_lock(&P.lock);
    if (P.backend == BACKEND_GBM && P.crtcTouched && P.savedCrtc && P.savedCrtc->mode_valid) {
        P.gpu.drmModeSetCrtc(P.fd, P.savedCrtc->crtc_id, P.savedCrtc->buffer_id, P.savedCrtc->x, P.savedCrtc->y,
                             &P.connectorId, 1, &P.savedCrtc->mode);
        fprintf(stderr, PR_TAG "restored the previous CRTC framebuffer %u\n", P.savedCrtc->buffer_id);
    }
    pthread_mutex_unlock(&P.lock);
}
