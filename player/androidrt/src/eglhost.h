/* eglhost.h — the slice of the EGL, GBM and libdrm ABIs AndroidRT calls,
 * declared header-free (the TSP SDK sysroot has no EGL/GBM/DRM headers and
 * the host smoke builds have none either). Every function is resolved with
 * dlsym from the device's glibc GPU userspace at run time.
 *
 * Shared by surface.c (the milestone-3 measurement probe) and eglpresent.c
 * (the present layer), so the probe measures exactly the path games take.
 * The values below are the Khronos/libdrm ones; get them wrong and the
 * device "measures" a stack that would work as broken:
 *   EGLint is int32_t (khrplatform.h) — NOT long: attribute lists are int32
 *   arrays even on aarch64;
 *   EGL_EXTENSIONS 0x3055; EGL_PLATFORM_GBM_KHR 0x31D7;
 *   gbm_surface_destroy(struct gbm_surface *) takes ONE argument.
 */
#ifndef ANDROIDRT_EGLHOST_H
#define ANDROIDRT_EGLHOST_H

#include <stddef.h>
#include <stdint.h>

/* ── EGL ─────────────────────────────────────────────────────────────── */
typedef int32_t EGLint;
typedef unsigned int EGLBoolean;
typedef unsigned int EGLenum;
typedef void *EGLDisplay;
typedef void *EGLConfig;
typedef void *EGLSurface;
typedef void *EGLContext;
typedef intptr_t EGLAttrib;

#define EGL_FALSE                 0
#define EGL_TRUE                  1
#define EGL_DEFAULT_DISPLAY       ((void *)0)
#define EGL_NO_DISPLAY            ((EGLDisplay)0)
#define EGL_NO_SURFACE            ((EGLSurface)0)
#define EGL_SUCCESS               0x3000
#define EGL_NOT_INITIALIZED       0x3001
#define EGL_BAD_ALLOC             0x3003
#define EGL_BAD_CONFIG            0x3005
#define EGL_BAD_DISPLAY           0x3008
#define EGL_BAD_MATCH             0x3009
#define EGL_BAD_NATIVE_WINDOW     0x300B
#define EGL_BAD_SURFACE           0x300D
#define EGL_ALPHA_SIZE            0x3021
#define EGL_BLUE_SIZE             0x3022
#define EGL_GREEN_SIZE            0x3023
#define EGL_RED_SIZE              0x3024
#define EGL_DEPTH_SIZE            0x3025
#define EGL_STENCIL_SIZE          0x3026
#define EGL_CONFIG_ID             0x3028
#define EGL_NATIVE_VISUAL_ID      0x302E
#define EGL_SURFACE_TYPE          0x3033
#define EGL_NONE                  0x3038
#define EGL_RENDERABLE_TYPE       0x3040
#define EGL_VENDOR                0x3053
#define EGL_VERSION               0x3054
#define EGL_EXTENSIONS            0x3055
#define EGL_HEIGHT                0x3056
#define EGL_WIDTH                 0x3057
#define EGL_CLIENT_APIS           0x308D
#define EGL_WINDOW_BIT            0x0004
#define EGL_OPENGL_ES_BIT         0x0001
#define EGL_OPENGL_ES2_BIT        0x0004
#define EGL_OPENGL_ES3_BIT_KHR    0x0040
#define EGL_PLATFORM_GBM_KHR      0x31D7
#define EGL_NO_CONTEXT            ((EGLContext)0)
#define EGL_BAD_CONTEXT           0x3006
#define EGL_SAMPLES               0x3031
#define EGL_SAMPLE_BUFFERS        0x3032
#define EGL_CONTEXT_CLIENT_VERSION    0x3098 /* = EGL_CONTEXT_MAJOR_VERSION_KHR */
#define EGL_CONTEXT_MINOR_VERSION_KHR 0x30FB

typedef EGLint (*PFN_eglGetError)(void);
typedef EGLDisplay (*PFN_eglGetDisplay)(void *nativeDisplay);
typedef EGLDisplay (*PFN_eglGetPlatformDisplayEXT)(EGLenum platform, void *nativeDisplay, const EGLint *attribs);
typedef EGLBoolean (*PFN_eglInitialize)(EGLDisplay dpy, EGLint *major, EGLint *minor);
typedef EGLBoolean (*PFN_eglTerminate)(EGLDisplay dpy);
typedef const char *(*PFN_eglQueryString)(EGLDisplay dpy, EGLint name);
typedef EGLBoolean (*PFN_eglChooseConfig)(EGLDisplay dpy, const EGLint *attribs, EGLConfig *configs, EGLint size,
                                           EGLint *num);
typedef EGLBoolean (*PFN_eglGetConfigAttrib)(EGLDisplay dpy, EGLConfig config, EGLint attribute, EGLint *value);
typedef EGLSurface (*PFN_eglCreateWindowSurface)(EGLDisplay dpy, EGLConfig config, void *win, const EGLint *attribs);
typedef EGLBoolean (*PFN_eglDestroySurface)(EGLDisplay dpy, EGLSurface surface);
typedef EGLBoolean (*PFN_eglSwapBuffers)(EGLDisplay dpy, EGLSurface surface);
typedef EGLBoolean (*PFN_eglSwapBuffersWithDamage)(EGLDisplay dpy, EGLSurface surface, const EGLint *rects,
                                                    EGLint n);
typedef void *(*PFN_eglGetProcAddress)(const char *name);
typedef EGLDisplay (*PFN_eglGetCurrentDisplay)(void);
typedef EGLSurface (*PFN_eglGetCurrentSurface)(EGLint readdraw);
typedef EGLBoolean (*PFN_eglQuerySurface)(EGLDisplay dpy, EGLSurface surface, EGLint attribute, EGLint *value);
typedef EGLContext (*PFN_eglCreateContext)(EGLDisplay dpy, EGLConfig config, EGLContext share, const EGLint *attribs);
typedef EGLBoolean (*PFN_eglDestroyContext)(EGLDisplay dpy, EGLContext ctx);
typedef EGLBoolean (*PFN_eglMakeCurrent)(EGLDisplay dpy, EGLSurface draw, EGLSurface read, EGLContext ctx);
typedef EGLContext (*PFN_eglGetCurrentContext)(void);
typedef EGLBoolean (*PFN_eglReleaseThread)(void);

#define EGL_DRAW 0x3059
#define EGL_READ 0x305A

/* ── GBM ─────────────────────────────────────────────────────────────── */
#define APAK_FOURCC(a, b, c, d) \
    ((uint32_t)(a) | ((uint32_t)(b) << 8) | ((uint32_t)(c) << 16) | ((uint32_t)(d) << 24))
#define GBM_FORMAT_XRGB8888 APAK_FOURCC('X', 'R', '2', '4')
#define GBM_FORMAT_ARGB8888 APAK_FOURCC('A', 'R', '2', '4')
#define GBM_FORMAT_XBGR8888 APAK_FOURCC('X', 'B', '2', '4')
#define GBM_FORMAT_ABGR8888 APAK_FOURCC('A', 'B', '2', '4')
#define GBM_FORMAT_RGB565   APAK_FOURCC('R', 'G', '1', '6')
#define GBM_BO_USE_SCANOUT   (1u << 0)
#define GBM_BO_USE_RENDERING (1u << 2)

typedef void *(*PFN_gbm_create_device)(int fd);
typedef void (*PFN_gbm_device_destroy)(void *gbm);
typedef void *(*PFN_gbm_surface_create)(void *gbm, uint32_t width, uint32_t height, uint32_t format, uint32_t flags);
typedef void (*PFN_gbm_surface_destroy)(void *surface);
typedef void *(*PFN_gbm_surface_lock_front_buffer)(void *surface);
typedef void (*PFN_gbm_surface_release_buffer)(void *surface, void *bo);
typedef uint32_t (*PFN_gbm_bo_get_u32)(void *bo);
typedef uint64_t (*PFN_gbm_bo_get_handle)(void *bo); /* union gbm_bo_handle, 8 bytes, returned in x0/rax */
typedef void (*PFN_gbm_bo_set_user_data)(void *bo, void *data, void (*destroy)(void *bo, void *data));
typedef void *(*PFN_gbm_bo_get_user_data)(void *bo);

/* ── libdrm (xf86drmMode.h layouts, stable ABI) ─────────────────────── */
typedef struct {
    uint32_t clock;
    uint16_t hdisplay, hsync_start, hsync_end, htotal, hskew;
    uint16_t vdisplay, vsync_start, vsync_end, vtotal, vscan;
    uint32_t vrefresh;
    uint32_t flags;
    uint32_t type;
    char name[32];
} ApakDrmModeInfo;

typedef struct {
    int count_fbs;
    uint32_t *fbs;
    int count_crtcs;
    uint32_t *crtcs;
    int count_connectors;
    uint32_t *connectors;
    int count_encoders;
    uint32_t *encoders;
    uint32_t min_width, max_width;
    uint32_t min_height, max_height;
} ApakDrmRes;

typedef struct {
    uint32_t connector_id;
    uint32_t encoder_id;
    uint32_t connector_type;
    uint32_t connector_type_id;
    int connection; /* 1 = DRM_MODE_CONNECTED */
    uint32_t mmWidth, mmHeight;
    int subpixel;
    int count_modes;
    ApakDrmModeInfo *modes;
    int count_props;
    uint32_t *props;
    uint64_t *prop_values;
    int count_encoders;
    uint32_t *encoders;
} ApakDrmConnector;

typedef struct {
    uint32_t encoder_id;
    uint32_t encoder_type;
    uint32_t crtc_id;
    uint32_t possible_crtcs;
    uint32_t possible_clones;
} ApakDrmEncoder;

typedef struct {
    uint32_t crtc_id;
    uint32_t buffer_id;
    uint32_t x, y;
    uint32_t width, height;
    int mode_valid;
    ApakDrmModeInfo mode;
    int gamma_size;
} ApakDrmCrtc;

typedef struct {
    int version; /* 2: vblank + page flip handlers */
    void (*vblank_handler)(int fd, unsigned sequence, unsigned tvSec, unsigned tvUsec, void *userData);
    void (*page_flip_handler)(int fd, unsigned sequence, unsigned tvSec, unsigned tvUsec, void *userData);
} ApakDrmEventContext;

#define DRM_MODE_CONNECTED       1
#define DRM_MODE_TYPE_PREFERRED  (1u << 3)
#define DRM_MODE_PAGE_FLIP_EVENT 0x01u

typedef ApakDrmRes *(*PFN_drmModeGetResources)(int fd);
typedef void (*PFN_drmModeFreeResources)(ApakDrmRes *res);
typedef ApakDrmConnector *(*PFN_drmModeGetConnector)(int fd, uint32_t id);
typedef void (*PFN_drmModeFreeConnector)(ApakDrmConnector *c);
typedef ApakDrmEncoder *(*PFN_drmModeGetEncoder)(int fd, uint32_t id);
typedef void (*PFN_drmModeFreeEncoder)(ApakDrmEncoder *e);
typedef ApakDrmCrtc *(*PFN_drmModeGetCrtc)(int fd, uint32_t id);
typedef void (*PFN_drmModeFreeCrtc)(ApakDrmCrtc *c);
typedef int (*PFN_drmModeAddFB)(int fd, uint32_t w, uint32_t h, uint8_t depth, uint8_t bpp, uint32_t pitch,
                                uint32_t handle, uint32_t *fb);
typedef int (*PFN_drmModeAddFB2)(int fd, uint32_t w, uint32_t h, uint32_t format, const uint32_t handles[4],
                                 const uint32_t pitches[4], const uint32_t offsets[4], uint32_t *fb, uint32_t flags);
typedef int (*PFN_drmModeRmFB)(int fd, uint32_t fb);
typedef int (*PFN_drmModeSetCrtc)(int fd, uint32_t crtc, uint32_t fb, uint32_t x, uint32_t y, uint32_t *connectors,
                                  int count, ApakDrmModeInfo *mode);
typedef int (*PFN_drmModePageFlip)(int fd, uint32_t crtc, uint32_t fb, uint32_t flags, void *userData);
typedef int (*PFN_drmHandleEvent)(int fd, ApakDrmEventContext *ctx);

/* ── the resolved stack ──────────────────────────────────────────────── */
typedef struct ApakGpu {
    void *egl, *gbm, *drm;
    char eglName[256], gbmName[256], drmName[256];
    PFN_eglGetError eglGetError;
    PFN_eglGetDisplay eglGetDisplay;
    PFN_eglGetPlatformDisplayEXT eglGetPlatformDisplayEXT;
    PFN_eglInitialize eglInitialize;
    PFN_eglTerminate eglTerminate;
    PFN_eglQueryString eglQueryString;
    PFN_eglChooseConfig eglChooseConfig;
    PFN_eglGetConfigAttrib eglGetConfigAttrib;
    PFN_eglCreateWindowSurface eglCreateWindowSurface;
    PFN_eglDestroySurface eglDestroySurface;
    PFN_eglSwapBuffers eglSwapBuffers;
    PFN_eglSwapBuffersWithDamage eglSwapBuffersWithDamageKHR;
    PFN_eglSwapBuffersWithDamage eglSwapBuffersWithDamageEXT;
    PFN_eglGetProcAddress eglGetProcAddress;
    PFN_eglGetCurrentDisplay eglGetCurrentDisplay; /* optional (SDL-hosted window) */
    PFN_eglGetCurrentSurface eglGetCurrentSurface;
    PFN_eglQuerySurface eglQuerySurface;
    PFN_eglCreateContext eglCreateContext; /* optional: the present layer passes contexts through */
    PFN_eglDestroyContext eglDestroyContext;
    PFN_eglMakeCurrent eglMakeCurrent;
    PFN_eglGetCurrentContext eglGetCurrentContext;
    PFN_eglReleaseThread eglReleaseThread; /* optional (EGL 1.2) */
    PFN_gbm_create_device gbm_create_device;
    PFN_gbm_device_destroy gbm_device_destroy;
    PFN_gbm_surface_create gbm_surface_create;
    PFN_gbm_surface_destroy gbm_surface_destroy;
    PFN_gbm_surface_lock_front_buffer gbm_surface_lock_front_buffer;
    PFN_gbm_surface_release_buffer gbm_surface_release_buffer;
    PFN_gbm_bo_get_u32 gbm_bo_get_width, gbm_bo_get_height, gbm_bo_get_stride, gbm_bo_get_format;
    PFN_gbm_bo_get_handle gbm_bo_get_handle;
    PFN_gbm_bo_set_user_data gbm_bo_set_user_data;
    PFN_gbm_bo_get_user_data gbm_bo_get_user_data;
    PFN_drmModeGetResources drmModeGetResources;
    PFN_drmModeFreeResources drmModeFreeResources;
    PFN_drmModeGetConnector drmModeGetConnector;
    PFN_drmModeFreeConnector drmModeFreeConnector;
    PFN_drmModeGetEncoder drmModeGetEncoder;
    PFN_drmModeFreeEncoder drmModeFreeEncoder;
    PFN_drmModeGetCrtc drmModeGetCrtc;
    PFN_drmModeFreeCrtc drmModeFreeCrtc;
    PFN_drmModeAddFB drmModeAddFB;
    PFN_drmModeAddFB2 drmModeAddFB2;
    PFN_drmModeRmFB drmModeRmFB;
    PFN_drmModeSetCrtc drmModeSetCrtc;
    PFN_drmModePageFlip drmModePageFlip;
    PFN_drmHandleEvent drmHandleEvent;
} ApakGpu;

/* Open libEGL (ANDROIDPAK_EGL_LIB), libgbm (ANDROIDPAK_GBM_LIB) and libdrm
 * (ANDROIDPAK_DRM_LIB) and resolve every entry point. Returns a bitmask of
 * the layers that loaded completely (1 EGL, 2 GBM, 4 DRM); `why` names the
 * first missing piece. Idempotent per struct. */
#define APAK_GPU_EGL 1
#define APAK_GPU_GBM 2
#define APAK_GPU_DRM 4
int apak_gpuLoad(ApakGpu *g, char *why, size_t cap);

/* EGL only, from an explicit list of paths (the stock firmware's own
 * /usr/lib/libEGL.so, ahead of anything LD_LIBRARY_PATH would find);
 * ANDROIDPAK_EGL_LIB still overrides, and like apak_gpuLoad an override
 * that does not load is final ("X not loadable: ..."), never replaced by
 * one of the paths. APAK_GPU_EGL or 0. */
int apak_gpuLoadEglFrom(ApakGpu *g, const char *const *paths, char *why, size_t cap);

/* The DRM card the GPU userspace should modeset: ANDROIDPAK_DRM_NODE, else
 * the first /dev/dri/card* whose driver is "pvr", else the first card.
 * Returns 0 none, 1 fallback card, 2 pvr/override. */
int apak_gpuDrmNode(char *node, size_t cap);

#endif /* ANDROIDRT_EGLHOST_H */
