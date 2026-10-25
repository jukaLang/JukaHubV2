/* surface.c — the Tier-1 surface stack probe (EGL + GBM, header-free).
 *
 * Milestone 3 of the AndroidRT README ("Surface — the PowerVR EGL backend")
 * starts with a measurement, not an implementation: can a game process on
 * this firmware's Linux side open a GBM-backed EGL surface at all? The
 * candidate stack (see docs/ANDROID-GAMMAOS.md) is the *glibc* GE8300
 * userspace — KNULLI's ge8300-drivers package or the stock firmware's own
 * GLES blobs — because the Android/Bionic blobs cannot load from a glibc
 * process. Everything here is resolved with dlsym function pointers, so
 * this file builds with no EGL/GBM/DRM headers and no GPU present: it
 * compiles everywhere and measures on the device.
 *
 * android_surfaceProbe(width, height, *works) runs the whole chain once —
 *   libEGL → client extensions → gbm device from the right DRM node →
 *   eglGetPlatformDisplayEXT(EGL_PLATFORM_GBM_KHR) → eglInitialize →
 *   eglChooseConfig → GBM surface → eglCreateWindowSurface
 * — prints each step, tears everything down and returns:
 *      0  the measurement completed, whatever it found (*works says whether
 *         the stack produced a scanout window surface) — every "missing
 *         layer" print is part of the completed measurement
 *      1  an internal surprise during the measurement (not expected: a bug here)
 *
 * Calling it is a probe, never an assertion: the runner asks for one when
 * ANDROIDPAK_SURFACE=1 (androidshim.c android_windowBackend), and a game
 * keeps working around an unanswered probe exactly as it does today. Set
 * ANDROIDPAK_EGL_LIB / ANDROIDPAK_GBM_LIB to point at explicit libraries
 * when this firmware put the glibc GE8300 userspace somewhere the usual
 * names do not cover.
 */

#define _GNU_SOURCE
#define _POSIX_C_SOURCE 200809L
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

/* Host platforms without it (MinGW smoke builds) compile the probe fine
 * with the flag elided; the device builds carry it for real. */
#ifndef O_CLOEXEC
#define O_CLOEXEC 0
#endif

#include "dlport.h"

#define SURFACE_TAG "[androidrt][surface] "

/* EGL types are all opaque handles or ints; nothing needs the real header. */
typedef void *EGLDisplay;
typedef void *EGLConfig;
typedef void *EGLSurface;
typedef long EGLint;

#define EGL_DEFAULT_DISPLAY ((void *)0)
#define EGL_NO_DISPLAY      ((EGLDisplay)0)
#define EGL_TRUE            0x1
#define EGL_SUCCESS         0x3000
#define EGL_EXTENSIONS      0x3081
#define EGL_NONE            0x3038
#define EGL_ALPHA_SIZE      0x3021
#define EGL_BLUE_SIZE       0x3022
#define EGL_GREEN_SIZE      0x3023
#define EGL_RED_SIZE        0x3024
#define EGL_DEPTH_SIZE      0x3025
#define EGL_STENCIL_SIZE    0x3026
#define EGL_SURFACE_TYPE    0x3033
#define EGL_WINDOW_BIT      0x0004
#define EGL_RENDERABLE_TYPE 0x3040
#define EGL_OPENGL_ES2_BIT  0x0004
#define EGL_PLATFORM_GBM_KHR 0x31D6

/* The GBM pieces the probe uses. FourCC for XRGB8888, the scanout-safe
 * format on essentially every DRM driver, expanded directly: 'X','R','2','4'. */
#define GBM_FORMAT_XRGB8888 0x34325258
#define GBM_SURFACE_USE_SCANOUT (1u << 0)

typedef EGLint (*eglGetError_fn)(void);
typedef EGLDisplay (*eglGetDisplay_fn)(void *native);
typedef EGLDisplay (*eglGetPlatformDisplayEXT_fn)(EGLint platform, void *native, const EGLint *attribs);
typedef unsigned (*eglInitialize_fn)(EGLDisplay dpy, EGLint *maj, EGLint *min);
typedef unsigned (*eglTerminate_fn)(EGLDisplay dpy);
typedef unsigned (*eglDestroySurface_fn)(EGLDisplay dpy, EGLSurface s);
typedef const char *(*eglQueryString_fn)(EGLDisplay dpy, EGLint name);
typedef unsigned (*eglChooseConfig_fn)(EGLDisplay dpy, const EGLint *attribs, EGLConfig *out, EGLint count, EGLint *got);
typedef EGLSurface (*eglCreateWindowSurface_fn)(EGLDisplay dpy, EGLConfig cfg, void *native, const EGLint *attribs);

typedef void *(*gbm_create_device_fn)(int fd);
typedef void (*gbm_device_destroy_fn)(void *dev);
typedef void *(*gbm_surface_create_fn)(void *dev, unsigned w, unsigned h, unsigned fmt, unsigned flags);
typedef void (*gbm_surface_destroy_fn)(void *dev, void *surf);

/* chooseDrmNode finds the DRM device the GPU userspace drives. On the Smart
 * Pro the PowerVR kernel driver is named "pvr" in the card's uevent; the
 * first card in a lean /dev/dri is the fallback. Returns 0 if no node, 1 for
 * a plain fallback and 2 for the pvr card. */
static int chooseDrmNode(char *node, size_t cap) {
    DIR *dir = opendir("/sys/class/drm");
    if (!dir) {
        return 0;
    }
    char fallback[128];
    fallback[0] = '\0';
    int pvr = 0;
    struct dirent *e;
    while ((e = readdir(dir)) != NULL && !pvr) {
        if (strncmp(e->d_name, "card", 4) != 0) {
            continue;
        }
        char path[256];
        snprintf(path, sizeof(path), "/sys/class/drm/%.60s/device/uevent", e->d_name);
        FILE *f = fopen(path, "r");
        if (!f) {
            continue;
        }
        char line[256];
        while (fgets(line, sizeof(line), f)) {
            char driver[64];
            if (sscanf(line, "DRIVER=%63s", driver) == 1 && strcmp(driver, "pvr") == 0) {
                snprintf(node, cap, "/dev/dri/%.60s", e->d_name);
                pvr = 1;
                break;
            }
        }
        fclose(f);
        if (!pvr && fallback[0] == '\0') {
            snprintf(fallback, sizeof(fallback), "/dev/dri/%.60s", e->d_name);
        }
    }
    closedir(dir);
    if (pvr) {
        return 2;
    }
    if (fallback[0] != '\0') {
        snprintf(node, cap, "%s", fallback);
        return 1;
    }
    return 0;
}

static int firstExistingPath(const char *const *candidates, char *out, size_t cap) {
    for (size_t i = 0; candidates[i]; i++) {
        if (access(candidates[i], R_OK) == 0) {
            snprintf(out, cap, "%s", candidates[i]);
            return 1;
        }
    }
    return 0;
}

/* openLibrary resolves override or well-known candidates, and says in
 * libDesc what was actually used, so the fatal print is actionable. */
static void *openLibrary(const char *overrideEnv, const char *const *candidates,
                         const char *defaultName, const char *fallbackDirHint,
                         char *libDesc, size_t cap) {
    void *handle = NULL;
    libDesc[0] = '\0';
    if (overrideEnv && *overrideEnv) {
        handle = rt_dlopen(overrideEnv, 1);
        snprintf(libDesc, cap, "%s (ANDROIDPAK override)", overrideEnv);
        return handle;
    }
    if (firstExistingPath(candidates, libDesc, cap)) {
        handle = rt_dlopen(libDesc, 1);
    } else {
        handle = rt_dlopen(defaultName, 1); /* let the loader's search run */
        snprintf(libDesc, cap, "%s (loader search)", defaultName);
    }
    if (!handle && fallbackDirHint) {
        fprintf(stderr, SURFACE_TAG "%s: not loadable (candidates searched: %s, %s and loader search) — a glibc GE8300 userspace under %s would be found with ANDROIDPAK_EGL_LIB\n",
                defaultName, candidates[1] ? candidates[1] : candidates[0], fallbackDirHint, fallbackDirHint);
    }
    return handle;
}

int android_surfaceProbe(int width, int height, int *works) {
    /* 0 = the measurement itself ran to completion (see *works); 1 is
     * reserved for a surprise internal error (currently unused — every
     * "stack not present" outcome is a valid completed measurement that
     * prints which layer died and what exists for that layer's fix). */
    if (works) *works = 0;

    const char *eglOverride = getenv("ANDROIDPAK_EGL_LIB");
    static const char *const eglCandidates[] = {
        "libEGL.so.1", "libEGL.so",
        "/usr/lib/libEGL.so.1", "/usr/lib/libEGL.so",
        "/usr/lib/aarch64-linux-gnu/libEGL.so.1",
        "/mnt/SDCARD/Apps/ge8300-drivers/glibc/lib/libEGL.so.1",
        NULL,
    };
    char eglLibDesc[256];
    void *egl = openLibrary(eglOverride, eglCandidates, "libEGL.so.1",
                            "/mnt/SDCARD/Apps/ge8300-drivers", eglLibDesc, sizeof(eglLibDesc));
    if (!egl) {
        fprintf(stderr, SURFACE_TAG "status: NO, failed opening libEGL (%s) — install the ge8300-drivers glibc package or set ANDROIDPAK_EGL_LIB\n",
                rt_dlerror());
        return 0;
    }

    eglGetError_fn eglGetError = (eglGetError_fn)rt_dlsym(egl, "eglGetError");
    eglGetDisplay_fn eglGetDisplay = (eglGetDisplay_fn)rt_dlsym(egl, "eglGetDisplay");
    eglInitialize_fn eglInitialize = (eglInitialize_fn)rt_dlsym(egl, "eglInitialize");
    eglTerminate_fn eglTerminate = (eglTerminate_fn)rt_dlsym(egl, "eglTerminate");
    eglDestroySurface_fn eglDestroySurface = (eglDestroySurface_fn)rt_dlsym(egl, "eglDestroySurface");
    eglQueryString_fn eglQueryString = (eglQueryString_fn)rt_dlsym(egl, "eglQueryString");
    eglChooseConfig_fn eglChooseConfig = (eglChooseConfig_fn)rt_dlsym(egl, "eglChooseConfig");
    eglCreateWindowSurface_fn eglCreateWindowSurface = (eglCreateWindowSurface_fn)rt_dlsym(egl, "eglCreateWindowSurface");
    eglGetPlatformDisplayEXT_fn eglGetPlatformDisplayEXT = (eglGetPlatformDisplayEXT_fn)rt_dlsym(egl, "eglGetPlatformDisplayEXT");
    if (!eglGetError || !eglGetDisplay || !eglInitialize || !eglTerminate ||
        !eglDestroySurface || !eglQueryString || !eglChooseConfig || !eglCreateWindowSurface) {
        fprintf(stderr, SURFACE_TAG "status: NO, libEGL opened but core entry points are missing — wrong build (bionic? different glibc?) for this device\n");
        return 0;
    }

    const char *clientExts = eglQueryString(EGL_NO_DISPLAY, EGL_EXTENSIONS);
    int havePlatformBase = clientExts && strstr(clientExts, "EGL_EXT_platform_base");
    fprintf(stderr, SURFACE_TAG "EGL client extensions: %s\n",
            clientExts ? clientExts : "(none)");

    static const char *const gbmCandidates[] = {
        "libgbm.so.1", "libgbm.so",
        "/usr/lib/libgbm.so.1", "/usr/lib/aarch64-linux-gnu/libgbm.so.1",
        "/mnt/SDCARD/Apps/ge8300-drivers/glibc/lib/libgbm.so.1",
        NULL,
    };
    char gbmLibDesc[256];
    void *gbmLib = openLibrary(getenv("ANDROIDPAK_GBM_LIB"), gbmCandidates,
                               "libgbm.so.1", NULL, gbmLibDesc, sizeof(gbmLibDesc));
    if (!gbmLib) {
        fprintf(stderr, SURFACE_TAG "status: NO, failed opening libgbm (%s) — GBM allocates the scanout buffers the pvr stack renders into\n",
                rt_dlerror());
        return 0;
    }
    gbm_create_device_fn gbm_create_device = (gbm_create_device_fn)rt_dlsym(gbmLib, "gbm_create_device");
    gbm_device_destroy_fn gbm_device_destroy = (gbm_device_destroy_fn)rt_dlsym(gbmLib, "gbm_device_destroy");
    gbm_surface_create_fn gbm_surface_create = (gbm_surface_create_fn)rt_dlsym(gbmLib, "gbm_surface_create");
    gbm_surface_destroy_fn gbm_surface_destroy = (gbm_surface_destroy_fn)rt_dlsym(gbmLib, "gbm_surface_destroy");
    if (!gbm_create_device || !gbm_device_destroy || !gbm_surface_create || !gbm_surface_destroy) {
        fprintf(stderr, SURFACE_TAG "status: NO, libgbm opened but surface entry points are missing — wrong build for this device\n");
        return 0;
    }

    char node[128];
    int picked = chooseDrmNode(node, sizeof(node));
    if (!picked) {
        fprintf(stderr, SURFACE_TAG "status: NO — no /dev/dri/card* DRM node found\n");
        return 0;
    }
    fprintf(stderr, SURFACE_TAG "DRM node: %s%s\n", node,
            picked == 2 ? " (pvr driver)" : " (fallback card — check this is the GPU when a game fails here)");
    int fd = open(node, O_RDWR | O_CLOEXEC);
    if (fd < 0) {
        fprintf(stderr, SURFACE_TAG "status: NO — cannot open %s (needs DRM device access: %s)\n",
                node, strerror(errno));
        return 0;
    }

    void *gbm = gbm_create_device(fd);
    if (!gbm) {
        fprintf(stderr, SURFACE_TAG "status: NO — gbm_create_device failed on %s\n", node);
        close(fd);
        return 0;
    }

    /* ── EGL display ── */
    EGLDisplay dpy = NULL;
    if (havePlatformBase && eglGetPlatformDisplayEXT) {
        dpy = eglGetPlatformDisplayEXT(EGL_PLATFORM_GBM_KHR, gbm, NULL);
        if (dpy) {
            fprintf(stderr, SURFACE_TAG "EGL display over GBM platform\n");
        }
    }
    if (!dpy) {
        dpy = eglGetDisplay(EGL_DEFAULT_DISPLAY);
        if (dpy) {
            fprintf(stderr, SURFACE_TAG "fell back to the default display — platform-GBM was not offered\n");
        }
    }
    if (!dpy) {
        fprintf(stderr, SURFACE_TAG "status: NO — no EGL display of either platform\n");
        gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }

    EGLint maj = 0, min = 0;
    if (eglInitialize(dpy, &maj, &min) != EGL_TRUE) {
        fprintf(stderr, SURFACE_TAG "status: NO — eglInitialize failed (EGL error 0x%lx); usually the wrong EGL build for this GPU\n",
                (unsigned long)eglGetError());
        eglTerminate(dpy);
        gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }
    fprintf(stderr, SURFACE_TAG "EGL %ld.%ld initialized\n", (long)maj, (long)min);

    EGLint cfgAttribs[] = {
        EGL_RED_SIZE, 5,
        EGL_GREEN_SIZE, 6,
        EGL_BLUE_SIZE, 5,
        EGL_DEPTH_SIZE, 16,
        EGL_STENCIL_SIZE, 0,
        EGL_SURFACE_TYPE, EGL_WINDOW_BIT,
        EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT,
        EGL_NONE,
    };
    EGLConfig cfg[4];
    EGLint got = 0;
    if (eglChooseConfig(dpy, cfgAttribs, cfg, 4, &got) != EGL_TRUE || got < 1) {
        fprintf(stderr, SURFACE_TAG "status: NO — eglChooseConfig found no RGB565/ES2 window config\n");
        eglTerminate(dpy);
        gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }

    void *gbmSurf = gbm_surface_create(gbm, (unsigned)width, (unsigned)height,
                                       GBM_FORMAT_XRGB8888, GBM_SURFACE_USE_SCANOUT);
    if (!gbmSurf) {
        fprintf(stderr, SURFACE_TAG "status: NO — gbm_surface_create failed %dx%d\n", width, height);
        eglTerminate(dpy);
        gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }

    EGLSurface surf = eglCreateWindowSurface(dpy, cfg[0], gbmSurf, NULL);
    if (!surf || eglGetError() != EGL_SUCCESS) {
        fprintf(stderr, SURFACE_TAG "status: NO — eglCreateWindowSurface failed (EGL error 0x%lx); the config/format pair offered does not match the GBM allocation the pvr stack uses\n",
                (unsigned long)eglGetError());
        gbm_surface_destroy(gbm, gbmSurf);
        eglTerminate(dpy);
        gbm_device_destroy(gbm);
        close(fd);
        return 0;
    }
    fprintf(stderr, SURFACE_TAG "status: YES %dx%d — a game process can build its own context here\n",
            width, height);
    if (works) *works = 1;

    eglDestroySurface(dpy, surf);
    gbm_surface_destroy(gbm, gbmSurf);
    eglTerminate(dpy);
    gbm_device_destroy(gbm);
    close(fd);
    return 0;
}

#ifdef SURFACE_PROBE_MAIN
/* A standalone probe binary: the measurement loop without a game. Exit 0
 * means the measurement completed (works or honestly did not); nonzero is
 * an internal failure worth copying from the log. */
int main(int argc, char **argv) {
    (void)argc;
    (void)argv;
    int w = 1280, h = 720;
    const char *winEnv = getenv("ANDROIDRT_WINDOW");
    if (winEnv) {
        int pw, ph;
        if (sscanf(winEnv, "%dx%d", &pw, &ph) == 2 && pw > 0 && ph > 0) {
            w = pw;
            h = ph;
        }
    }
    int works = 0;
    return android_surfaceProbe(w, h, &works);
}
#endif /* SURFACE_PROBE_MAIN */
