/* surface.c — the Tier-1 surface stack: shared GPU-library loading plus the
 * milestone-3 measurement probe.
 *
 * The candidate stack (see docs/ANDROID-GAMMAOS.md) is the *glibc* GE8300
 * userspace — KNULLI's ge8300-drivers package or the stock firmware's own
 * GLES blobs — because the Android/Bionic blobs cannot load from a glibc
 * process. Everything is resolved through dlsym (eglhost.h), so this file
 * builds with no EGL/GBM/DRM headers and no GPU present.
 *
 * android_surfaceProbe(width, height, *works) walks the whole chain once and
 * prints each layer — libEGL/libgbm/libdrm → EGL client extensions → DRM
 * node → KMS connector/mode/CRTC → gbm device → EGL display (platform GBM)
 * → eglInitialize → an ES2 window config whose EGL_NATIVE_VISUAL_ID is a
 * GBM format → gbm surface → eglCreateWindowSurface — then the fbdev panel
 * and whether a *native* (non-GBM) EGL display initialises, which is the
 * present layer's other backend. It tears everything down and returns 0
 * when the measurement completed (*works = the GBM path produced a window
 * surface); 1 is reserved for an internal surprise.
 *
 * The runner asks for it with ANDROIDPAK_SURFACE=1; tests/smoke builds the
 * standalone probe binary (-DSURFACE_PROBE_MAIN).
 */

#define _GNU_SOURCE
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#if defined(__linux__)
#include <sys/ioctl.h>
#endif

#ifndef O_CLOEXEC
#define O_CLOEXEC 0
#endif

#include "dlport.h"
#include "eglhost.h"

#define SURFACE_TAG "[androidrt][surface] "

/* ── shared loading (eglhost.h) ──────────────────────────────────────── */

static void *openFirst(const char *envName, const char *const *candidates, char *name, size_t cap) {
    const char *override = getenv(envName);
    size_t i;
    if (override && *override) {
        snprintf(name, cap, "%s", override);
        return rt_dlopen(override, 0);
    }
    for (i = 0; candidates[i]; i++) {
        void *h = rt_dlopen(candidates[i], 0);
        if (h) {
            snprintf(name, cap, "%s", candidates[i]);
            return h;
        }
    }
    /* Not found anywhere: say so (the last dlerror only names the last path). */
    snprintf(name, cap, "%s (not found in %zu locations; %s overrides)", candidates[0], i, envName);
    return NULL;
}

/* "not found in N locations" already says it all; an explicit override that
 * failed gets the loader's own reason. */
static void notLoadable(char *why, size_t cap, const char *name) {
    if (strstr(name, "not found in")) snprintf(why, cap, "%s", name);
    else snprintf(why, cap, "%s not loadable: %s", name, rt_dlerror());
}

#define RESOLVE(lib, field, sym) (*(void **)&g->field = rt_dlsym((lib), (sym)))

int apak_gpuLoad(ApakGpu *g, char *why, size_t cap) {
    static const char *const eglNames[] = {
        "libEGL.so.1", "libEGL.so", "/usr/lib/libEGL.so.1", "/usr/lib/aarch64-linux-gnu/libEGL.so.1",
        "/mnt/SDCARD/Apps/ge8300-drivers/glibc/lib/libEGL.so.1", NULL,
    };
    static const char *const gbmNames[] = {
        "libgbm.so.1", "libgbm.so", "/usr/lib/libgbm.so.1", "/usr/lib/aarch64-linux-gnu/libgbm.so.1",
        "/mnt/SDCARD/Apps/ge8300-drivers/glibc/lib/libgbm.so.1", NULL,
    };
    static const char *const drmNames[] = {
        "libdrm.so.2", "libdrm.so", "/usr/lib/libdrm.so.2", "/usr/lib/aarch64-linux-gnu/libdrm.so.2", NULL,
    };
    int layers = 0;
    if (why && cap) why[0] = '\0';

    if (!g->egl) g->egl = openFirst("ANDROIDPAK_EGL_LIB", eglNames, g->eglName, sizeof g->eglName);
    if (g->egl) {
        RESOLVE(g->egl, eglGetError, "eglGetError");
        RESOLVE(g->egl, eglGetDisplay, "eglGetDisplay");
        RESOLVE(g->egl, eglGetPlatformDisplayEXT, "eglGetPlatformDisplayEXT");
        RESOLVE(g->egl, eglInitialize, "eglInitialize");
        RESOLVE(g->egl, eglTerminate, "eglTerminate");
        RESOLVE(g->egl, eglQueryString, "eglQueryString");
        RESOLVE(g->egl, eglChooseConfig, "eglChooseConfig");
        RESOLVE(g->egl, eglGetConfigAttrib, "eglGetConfigAttrib");
        RESOLVE(g->egl, eglCreateWindowSurface, "eglCreateWindowSurface");
        RESOLVE(g->egl, eglDestroySurface, "eglDestroySurface");
        RESOLVE(g->egl, eglSwapBuffers, "eglSwapBuffers");
        RESOLVE(g->egl, eglSwapBuffersWithDamageKHR, "eglSwapBuffersWithDamageKHR");
        RESOLVE(g->egl, eglSwapBuffersWithDamageEXT, "eglSwapBuffersWithDamageEXT");
        RESOLVE(g->egl, eglGetProcAddress, "eglGetProcAddress");
        if (!g->eglGetPlatformDisplayEXT && g->eglGetProcAddress)
            *(void **)&g->eglGetPlatformDisplayEXT = g->eglGetProcAddress("eglGetPlatformDisplayEXT");
        if (g->eglGetError && g->eglGetDisplay && g->eglInitialize && g->eglTerminate && g->eglQueryString &&
            g->eglChooseConfig && g->eglGetConfigAttrib && g->eglCreateWindowSurface && g->eglDestroySurface &&
            g->eglSwapBuffers)
            layers |= APAK_GPU_EGL;
        else if (why && !why[0])
            snprintf(why, cap, "%s lacks core EGL entry points (bionic build? wrong glibc?)", g->eglName);
    } else if (why && !why[0]) {
        notLoadable(why, cap, g->eglName);
    }

    if (!g->gbm) g->gbm = openFirst("ANDROIDPAK_GBM_LIB", gbmNames, g->gbmName, sizeof g->gbmName);
    if (g->gbm) {
        RESOLVE(g->gbm, gbm_create_device, "gbm_create_device");
        RESOLVE(g->gbm, gbm_device_destroy, "gbm_device_destroy");
        RESOLVE(g->gbm, gbm_surface_create, "gbm_surface_create");
        RESOLVE(g->gbm, gbm_surface_destroy, "gbm_surface_destroy");
        RESOLVE(g->gbm, gbm_surface_lock_front_buffer, "gbm_surface_lock_front_buffer");
        RESOLVE(g->gbm, gbm_surface_release_buffer, "gbm_surface_release_buffer");
        RESOLVE(g->gbm, gbm_bo_get_width, "gbm_bo_get_width");
        RESOLVE(g->gbm, gbm_bo_get_height, "gbm_bo_get_height");
        RESOLVE(g->gbm, gbm_bo_get_stride, "gbm_bo_get_stride");
        RESOLVE(g->gbm, gbm_bo_get_format, "gbm_bo_get_format");
        RESOLVE(g->gbm, gbm_bo_get_handle, "gbm_bo_get_handle");
        RESOLVE(g->gbm, gbm_bo_set_user_data, "gbm_bo_set_user_data");
        RESOLVE(g->gbm, gbm_bo_get_user_data, "gbm_bo_get_user_data");
        if (g->gbm_create_device && g->gbm_device_destroy && g->gbm_surface_create && g->gbm_surface_destroy &&
            g->gbm_surface_lock_front_buffer && g->gbm_surface_release_buffer && g->gbm_bo_get_width &&
            g->gbm_bo_get_height && g->gbm_bo_get_stride && g->gbm_bo_get_format && g->gbm_bo_get_handle &&
            g->gbm_bo_set_user_data && g->gbm_bo_get_user_data)
            layers |= APAK_GPU_GBM;
        else if (why && !why[0])
            snprintf(why, cap, "%s lacks the gbm surface/bo entry points", g->gbmName);
    } else if (why && !why[0]) {
        notLoadable(why, cap, g->gbmName);
    }

    if (!g->drm) g->drm = openFirst("ANDROIDPAK_DRM_LIB", drmNames, g->drmName, sizeof g->drmName);
    if (g->drm) {
        RESOLVE(g->drm, drmModeGetResources, "drmModeGetResources");
        RESOLVE(g->drm, drmModeFreeResources, "drmModeFreeResources");
        RESOLVE(g->drm, drmModeGetConnector, "drmModeGetConnector");
        RESOLVE(g->drm, drmModeFreeConnector, "drmModeFreeConnector");
        RESOLVE(g->drm, drmModeGetEncoder, "drmModeGetEncoder");
        RESOLVE(g->drm, drmModeFreeEncoder, "drmModeFreeEncoder");
        RESOLVE(g->drm, drmModeGetCrtc, "drmModeGetCrtc");
        RESOLVE(g->drm, drmModeFreeCrtc, "drmModeFreeCrtc");
        RESOLVE(g->drm, drmModeAddFB, "drmModeAddFB");
        RESOLVE(g->drm, drmModeAddFB2, "drmModeAddFB2");
        RESOLVE(g->drm, drmModeRmFB, "drmModeRmFB");
        RESOLVE(g->drm, drmModeSetCrtc, "drmModeSetCrtc");
        RESOLVE(g->drm, drmModePageFlip, "drmModePageFlip");
        RESOLVE(g->drm, drmHandleEvent, "drmHandleEvent");
        if (g->drmModeGetResources && g->drmModeFreeResources && g->drmModeGetConnector && g->drmModeFreeConnector &&
            g->drmModeGetEncoder && g->drmModeFreeEncoder && g->drmModeGetCrtc && g->drmModeFreeCrtc &&
            (g->drmModeAddFB2 || g->drmModeAddFB) && g->drmModeRmFB && g->drmModeSetCrtc)
            layers |= APAK_GPU_DRM;
        else if (why && !why[0])
            snprintf(why, cap, "%s lacks the KMS entry points", g->drmName);
    } else if (why && !why[0]) {
        notLoadable(why, cap, g->drmName);
    }
    return layers;
}

int apak_gpuDrmNode(char *node, size_t cap) {
    const char *override = getenv("ANDROIDPAK_DRM_NODE");
    char fallback[128];
    int pvr = 0;
    DIR *dir;
    struct dirent *e;
    if (override && *override) {
        snprintf(node, cap, "%s", override);
        return 2;
    }
    dir = opendir("/sys/class/drm");
    if (!dir) return 0;
    fallback[0] = '\0';
    while ((e = readdir(dir)) != NULL && !pvr) {
        char path[256], line[256];
        FILE *f;
        if (strncmp(e->d_name, "card", 4) != 0 || strchr(e->d_name, '-')) continue; /* card0, not card0-HDMI-A-1 */
        snprintf(path, sizeof(path), "/sys/class/drm/%.60s/device/uevent", e->d_name);
        f = fopen(path, "r");
        if (!f) continue;
        while (fgets(line, sizeof(line), f)) {
            char driver[64];
            if (sscanf(line, "DRIVER=%63s", driver) == 1 && strcmp(driver, "pvr") == 0) {
                snprintf(node, cap, "/dev/dri/%.60s", e->d_name);
                pvr = 1;
                break;
            }
        }
        fclose(f);
        if (!pvr && fallback[0] == '\0') snprintf(fallback, sizeof(fallback), "/dev/dri/%.60s", e->d_name);
    }
    closedir(dir);
    if (pvr) return 2;
    if (fallback[0]) {
        snprintf(node, cap, "%s", fallback);
        return 1;
    }
    return 0;
}

/* ── the probe ───────────────────────────────────────────────────────── */

static const char *fourccName(uint32_t f, char out[5]) {
    int i;
    for (i = 0; i < 4; i++) {
        char c = (char)((f >> (8 * i)) & 0xff);
        out[i] = (c >= 32 && c < 127) ? c : '?';
    }
    out[4] = '\0';
    return out;
}

static int isGbmFormat(EGLint v) {
    uint32_t f = (uint32_t)v;
    return f == GBM_FORMAT_XRGB8888 || f == GBM_FORMAT_ARGB8888 || f == GBM_FORMAT_XBGR8888 ||
           f == GBM_FORMAT_ABGR8888 || f == GBM_FORMAT_RGB565;
}

static void probeFbdev(void) {
#if defined(__linux__)
    uint32_t var[40];
    int fd = open("/dev/fb0", O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        fprintf(stderr, SURFACE_TAG "fbdev: no /dev/fb0 (%s)\n", strerror(errno));
        return;
    }
    memset(var, 0, sizeof var);
    if (ioctl(fd, 0x4600 /* FBIOGET_VSCREENINFO */, var) == 0)
        fprintf(stderr, SURFACE_TAG "fbdev: /dev/fb0 %ux%u (virtual %ux%u) %u bpp\n", var[0], var[1], var[2], var[3],
                var[6]);
    else
        fprintf(stderr, SURFACE_TAG "fbdev: /dev/fb0 present, FBIOGET_VSCREENINFO failed: %s\n", strerror(errno));
    close(fd);
#endif
}

static void probeNativeDisplay(ApakGpu *g) {
    EGLDisplay dpy = g->eglGetDisplay(EGL_DEFAULT_DISPLAY);
    EGLint maj = 0, min = 0;
    if (!dpy) {
        fprintf(stderr, SURFACE_TAG "native EGL: eglGetDisplay(EGL_DEFAULT_DISPLAY) = EGL_NO_DISPLAY\n");
        return;
    }
    if (g->eglInitialize(dpy, &maj, &min) != EGL_TRUE) {
        fprintf(stderr, SURFACE_TAG "native EGL: eglInitialize failed (0x%x) — present backend `native` will not work\n",
                (unsigned)g->eglGetError());
        return;
    }
    fprintf(stderr, SURFACE_TAG "native EGL: default display %d.%d (%s) — present backend `native` can start\n",
            (int)maj, (int)min, g->eglQueryString(dpy, EGL_VENDOR) ? g->eglQueryString(dpy, EGL_VENDOR) : "?");
    g->eglTerminate(dpy);
}

int android_surfaceProbe(int width, int height, int *works) {
    static ApakGpu gpu;
    ApakGpu *g = &gpu;
    char why[512], node[128], fcc[5];
    int layers, fd, picked, i, vis = 0;
    ApakDrmRes *res;
    ApakDrmModeInfo mode;
    uint32_t connId = 0;
    void *gbm, *gsurf;
    EGLDisplay dpy = NULL;
    EGLint maj = 0, min = 0, n = 0, err;
    EGLConfig configs[64], chosen = NULL;
    EGLSurface surf;
    const char *clientExts;
    static const EGLint attribs[] = {
        EGL_SURFACE_TYPE, EGL_WINDOW_BIT, EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT,
        EGL_RED_SIZE, 5, EGL_GREEN_SIZE, 6, EGL_BLUE_SIZE, 5, EGL_NONE,
    };

    if (works) *works = 0;
    layers = apak_gpuLoad(g, why, sizeof why);
    fprintf(stderr, SURFACE_TAG "libraries: EGL %s (%s), GBM %s (%s), DRM %s (%s)\n",
            (layers & APAK_GPU_EGL) ? "ok" : "NO", g->eglName, (layers & APAK_GPU_GBM) ? "ok" : "NO", g->gbmName,
            (layers & APAK_GPU_DRM) ? "ok" : "NO", g->drmName);
    probeFbdev();
    if (!(layers & APAK_GPU_EGL)) {
        fprintf(stderr, SURFACE_TAG "status: NO — %s; install the ge8300-drivers glibc package or set ANDROIDPAK_EGL_LIB\n",
                why);
        return 0;
    }
    clientExts = g->eglQueryString(EGL_NO_DISPLAY, EGL_EXTENSIONS);
    fprintf(stderr, SURFACE_TAG "EGL client extensions: %s\n", clientExts ? clientExts : "(none: EGL 1.4 without client extensions)");
    probeNativeDisplay(g);
    if ((layers & (APAK_GPU_GBM | APAK_GPU_DRM)) != (APAK_GPU_GBM | APAK_GPU_DRM)) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — %s\n", why);
        return 0;
    }

    picked = apak_gpuDrmNode(node, sizeof node);
    if (!picked) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — no /dev/dri/card* node (an fbdev-only display)\n");
        return 0;
    }
    fd = open(node, O_RDWR | O_CLOEXEC);
    if (fd < 0) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — cannot open %s: %s\n", node, strerror(errno));
        return 0;
    }
    fprintf(stderr, SURFACE_TAG "DRM node: %s%s\n", node, picked == 2 ? "" : " (first card; not the pvr driver)");

    memset(&mode, 0, sizeof mode);
    res = g->drmModeGetResources(fd);
    if (!res) {
        fprintf(stderr, SURFACE_TAG "KMS: drmModeGetResources failed (%s) — a render-only node, no display\n",
                strerror(errno));
    } else {
        for (i = 0; i < res->count_connectors && !connId; i++) {
            ApakDrmConnector *c = g->drmModeGetConnector(fd, res->connectors[i]);
            if (!c) continue;
            if (c->connection == DRM_MODE_CONNECTED && c->count_modes > 0) {
                int m, best = 0;
                for (m = 0; m < c->count_modes; m++)
                    if (c->modes[m].type & DRM_MODE_TYPE_PREFERRED) best = m;
                mode = c->modes[best];
                connId = c->connector_id;
            }
            g->drmModeFreeConnector(c);
        }
        fprintf(stderr, SURFACE_TAG "KMS: %d connector(s), %d crtc(s); %s\n", res->count_connectors, res->count_crtcs,
                connId ? "connected panel found" : "no connected connector");
        if (connId)
            fprintf(stderr, SURFACE_TAG "KMS: connector %u mode %s %ux%u@%u\n", connId, mode.name,
                    (unsigned)mode.hdisplay, (unsigned)mode.vdisplay, mode.vrefresh);
        g->drmModeFreeResources(res);
    }

    gbm = g->gbm_create_device(fd);
    if (!gbm) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — gbm_create_device failed on %s\n", node);
        close(fd);
        return 0;
    }
    if (clientExts && strstr(clientExts, "platform_gbm") && g->eglGetPlatformDisplayEXT) {
        dpy = g->eglGetPlatformDisplayEXT(EGL_PLATFORM_GBM_KHR, gbm, NULL);
        if (dpy) fprintf(stderr, SURFACE_TAG "EGL display over EGL_PLATFORM_GBM_KHR\n");
    }
    if (!dpy) {
        dpy = g->eglGetDisplay(gbm); /* Mesa/PVR accept the gbm device as native display */
        if (dpy) fprintf(stderr, SURFACE_TAG "EGL display from eglGetDisplay(gbm_device)\n");
    }
    if (!dpy || g->eglInitialize(dpy, &maj, &min) != EGL_TRUE) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — no initialisable EGL display on the gbm device (0x%x)\n",
                (unsigned)g->eglGetError());
        g->gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }
    fprintf(stderr, SURFACE_TAG "EGL %d.%d initialised on GBM (%s)\n", (int)maj, (int)min,
            g->eglQueryString(dpy, EGL_VENDOR) ? g->eglQueryString(dpy, EGL_VENDOR) : "?");

    if (g->eglChooseConfig(dpy, attribs, configs, 64, &n) != EGL_TRUE || n < 1) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — no ES2 window config\n");
        g->eglTerminate(dpy);
        g->gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }
    for (i = 0; i < n && !chosen; i++) {
        EGLint v = 0;
        if (g->eglGetConfigAttrib(dpy, configs[i], EGL_NATIVE_VISUAL_ID, &v) == EGL_TRUE && isGbmFormat(v)) {
            chosen = configs[i];
            vis = v;
        }
    }
    if (!chosen) {
        chosen = configs[0];
        vis = (EGLint)GBM_FORMAT_XRGB8888;
        fprintf(stderr, SURFACE_TAG "no config reports a GBM visual; trying XRGB8888 with the first of %d\n", (int)n);
    } else {
        fprintf(stderr, SURFACE_TAG "%d ES2 window configs; using visual %s\n", (int)n, fourccName((uint32_t)vis, fcc));
    }
    if (connId) {
        width = mode.hdisplay;
        height = mode.vdisplay;
    }
    gsurf = g->gbm_surface_create(gbm, (uint32_t)width, (uint32_t)height, (uint32_t)vis,
                                  GBM_BO_USE_SCANOUT | GBM_BO_USE_RENDERING);
    if (!gsurf) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — gbm_surface_create %dx%d %s failed\n", width, height,
                fourccName((uint32_t)vis, fcc));
        g->eglTerminate(dpy);
        g->gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }
    surf = g->eglCreateWindowSurface(dpy, chosen, gsurf, NULL);
    err = g->eglGetError();
    if (!surf) {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path NO — eglCreateWindowSurface failed (0x%x)\n", (unsigned)err);
    } else {
        fprintf(stderr, SURFACE_TAG "status: GBM/KMS path YES %dx%d%s — the present layer can drive this firmware\n",
                width, height, connId ? "" : " (no connected connector: rendering works, scanout unproven)");
        if (works) *works = 1;
        g->eglDestroySurface(dpy, surf);
    }
    g->gbm_surface_destroy(gsurf);
    g->eglTerminate(dpy);
    g->gbm_device_destroy(gbm);
    close(fd);
    return 0;
}

#ifdef SURFACE_PROBE_MAIN
/* The standalone probe binary: the measurement without a game. Exit 0 means
 * the measurement completed (works or honestly did not). */
int main(int argc, char **argv) {
    int w = 1280, h = 720, works = 0;
    const char *winEnv = getenv("ANDROIDRT_WINDOW");
    (void)argc;
    (void)argv;
    if (winEnv) {
        int pw, ph;
        if (sscanf(winEnv, "%dx%d", &pw, &ph) == 2 && pw > 0 && ph > 0) {
            w = pw;
            h = ph;
        }
    }
    return android_surfaceProbe(w, h, &works);
}
#endif /* SURFACE_PROBE_MAIN */
