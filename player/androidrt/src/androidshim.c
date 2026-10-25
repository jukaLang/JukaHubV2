/* androidshim.c — libandroid.so for Tier 1: the subset NativeActivity
 * NDK games actually touch from a native entry —
 *   ANativeActivity_finish / getWindow
 *   ANativeWindow_* queries and buffer geometry
 *   ALooper_prepare / pollAll / pollOnce / wake
 *   AAssetManager_* reading the staged assets/ tree
 * Everything camera/identity/privacy-shaped is deliberately absent.
 *
 * The window here is a handle, not a surface: games draw through EGL on the
 * device's PowerVR userspace (that part stays unshimmed), and a Tier-1
 * backend that hands games a real AHardwareBuffer-backed ANativeWindow is
 * the next milestone behind android_windowBackend in window.c.
 */

#define _GNU_SOURCE
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "actbridge.h"

/* ── window ──────────────────────────────────────────────────────────── */

/* AHardwareBuffer_format values games ask for via window_setBuffersGeometry. */
enum {
    WINDOW_FORMAT_RGBA_8888 = 1,
    WINDOW_FORMAT_RGBX_8888 = 2,
    WINDOW_FORMAT_RGB_565   = 4,
};

typedef struct NativeWindow {
    int32_t width;
    int32_t height;
    int32_t format;
    int32_t refcount;
} NativeWindow;

static NativeWindow *asWindow(ANativeWindow *w) { return (NativeWindow *)w; }

static NativeWindow *g_window; /* the one window this runner drives */

static pthread_mutex_t g_window_lock = PTHREAD_MUTEX_INITIALIZER;

/* activity→shim window plumbing (see actbridge.h) */
void android_activitySetWindow(ANativeActivity *activity, ANativeWindow *window) {
    /* activity->window is NDK tail-field ordering (see actbridge.h); games
     * read it directly, so it mirrors the shim's global. */
    if (activity) activity->window = window;
    pthread_mutex_lock(&g_window_lock);
    g_window = (NativeWindow *)window;
    pthread_mutex_unlock(&g_window_lock);
}

void android_activityClearWindow(ANativeActivity *activity, ANativeWindow *window) {
    (void)activity;
    if (!window) return;
    pthread_mutex_lock(&g_window_lock);
    if (g_window == (NativeWindow *)window) g_window = NULL;
    pthread_mutex_unlock(&g_window_lock);
}

ANativeWindow *android_currentWindow(void) {
    pthread_mutex_lock(&g_window_lock);
    ANativeWindow *w = (ANativeWindow *)g_window;
    pthread_mutex_unlock(&g_window_lock);
    return w;
}

static NativeWindow *windowCreate(int32_t width, int32_t height, int32_t format) {
    NativeWindow *w = calloc(1, sizeof(*w));
    if (w) {
        w->width = width;
        w->height = height;
        w->format = format ? format : WINDOW_FORMAT_RGBA_8888;
        w->refcount = 1;
    }
    return w;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_getWidth(ANativeWindow *window) {
    pthread_mutex_lock(&g_window_lock);
    int32_t width = window && asWindow(window) ? asWindow(window)->width : 0;
    pthread_mutex_unlock(&g_window_lock);
    return width;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_getHeight(ANativeWindow *window) {
    pthread_mutex_lock(&g_window_lock);
    int32_t height = window && asWindow(window) ? asWindow(window)->height : 0;
    pthread_mutex_unlock(&g_window_lock);
    return height;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_getFormat(ANativeWindow *window) {
    pthread_mutex_lock(&g_window_lock);
    int32_t format = window && asWindow(window) ? asWindow(window)->format : 0;
    pthread_mutex_unlock(&g_window_lock);
    return format;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_setBuffersGeometry(ANativeWindow *window, int32_t width, int32_t height, int32_t format) {
    if (!window) return -22; /* the NDK's -EINVAL failure code */

    pthread_mutex_lock(&g_window_lock);
    NativeWindow *w = asWindow(window);
    if (width > 0)  w->width = width;
    if (height > 0) w->height = height;
    if (format > 0) w->format = format;
    pthread_mutex_unlock(&g_window_lock);
    return 0; /* API's 0 = success */
}

ANDROIDRT_EXPORT void ANativeWindow_acquire(ANativeWindow *window) {
    if (!window) return;
    pthread_mutex_lock(&g_window_lock);
    asWindow(window)->refcount++;
    pthread_mutex_unlock(&g_window_lock);
}

ANDROIDRT_EXPORT void ANativeWindow_release(ANativeWindow *window) {
    if (!window) return;
    pthread_mutex_lock(&g_window_lock);
    if (--asWindow(window)->refcount == 0) {
        asWindow(window)->refcount = 0;
        free(window);
    }
    pthread_mutex_unlock(&g_window_lock);
}

/* The backend hook: creates the actual surface + EGL on the device. Until
 * window.c implements it, games see a surface-less stub window; their own
 * code paths still run, which is enough for the symbol-measurement loop
 * the runner prints. */
ANDROIDRT_EXPORT int android_windowBackend(int width, int height) {
    NativeWindow *w = windowCreate(width, height, WINDOW_FORMAT_RGBA_8888);
    pthread_mutex_lock(&g_window_lock);
    ANativeWindow_release((ANativeWindow *)g_window);
    g_window = w;
    pthread_mutex_unlock(&g_window_lock);
    fprintf(stderr,
            "[androidrt] window backend issued %dx%d RGBA (surface-less "
            "stub; the EGL stack itself is measured by android_surfaceProbe)\n",
            width, height);
#ifndef JUKAHUB_SHIM_SO
    /* ANDROIDPAK_SURFACE triggers the surface.c measurement — the
     * milestone-3 question of whether this firmware's glibc GPU stack can
     * hand a game a real EGL window surface. A probe failure never breaks
     * the run: the window stub stays, only the measurement prints. */
    const char *surfaceProbe = getenv("ANDROIDPAK_SURFACE");
    if (surfaceProbe && strcmp(surfaceProbe, "0") != 0) {
        extern int android_surfaceProbe(int width, int height, int *works);
        int works = 0;
        if (android_surfaceProbe(width, height, &works) != 0) {
            fprintf(stderr, "[androidrt][surface] probe could not complete — the stub window stays, the log above says which layer died\n");
        } else if (!works) {
            fprintf(stderr, "[androidrt][surface] probe completed: stack not usable yet (see log above)\n");
        }
    }
#endif /* !JUKAHUB_SHIM_SO */
    return 0;
}

/* ── activity ────────────────────────────────────────────────────────── */

static struct {
    ANativeActivity *activity;
    ActivityRunner *runner;
} g_activity;

void android_activityInit(ANativeActivity *activity, ActivityRunner *runner) {
    g_activity.activity = activity;
    g_activity.runner = runner;
}

ANDROIDRT_EXPORT ANativeWindow *ANativeActivity_getWindow(ANativeActivity *activity) {
    (void)activity;
    pthread_mutex_lock(&g_window_lock);
    ANativeWindow *w = (ANativeWindow *)g_window;
    pthread_mutex_unlock(&g_window_lock);
    return w;
}

ANDROIDRT_EXPORT void ANativeActivity_finish(ANativeActivity *activity) {
    (void)activity;
    runner_requestExit(g_activity.runner, 0);
}

/* ── looper ──────────────────────────────────────────────────────────── */

/* NDK glue calls ALooper_pollAll(-1, &fd, &events, (void**)&source) with a
 * timeout of -1 forever. Our queue is the androidshim's condvar; events the
 * runner delivers in activity.c arrive through android_deliverEvent. */
static pthread_mutex_t g_looper_lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t g_looper_cond = PTHREAD_COND_INITIALIZER;
static int g_looper_pending;

enum {
    ALOOPER_POLL_WAKE   = -1,
    ALOOPER_POLL_TIMEOUT = -2,
    ALOOPER_POLL_ERROR  = -3,
};

/* Wake polling glue asks for with pollAll. */
int android_pollOnce(int timeoutMs) {
    pthread_mutex_lock(&g_looper_lock);
    if (g_looper_pending) {
        g_looper_pending = 0;
        pthread_mutex_unlock(&g_looper_lock);
        return ALOOPER_POLL_WAKE;
    }
    struct timespec deadline;
    clock_gettime(CLOCK_REALTIME, &deadline);
    deadline.tv_sec += timeoutMs >= 0 ? timeoutMs / 1000 : 0;
    deadline.tv_nsec += (int64_t)(timeoutMs >= 0 ? (timeoutMs % 1000) * 1000000LL : 16 /* small tick */ * 1000000LL);
    if (deadline.tv_nsec >= 1000000000LL) {
        deadline.tv_sec++;
        deadline.tv_nsec -= 1000000000LL;
    }
    int rc = pthread_cond_timedwait(&g_looper_cond, &g_looper_lock, &deadline);
    int woke = g_looper_pending;
    g_looper_pending = 0;
    pthread_mutex_unlock(&g_looper_lock);
    if (woke) return ALOOPER_POLL_WAKE;
    if (rc == 0) return ALOOPER_POLL_WAKE;
    return ALOOPER_POLL_TIMEOUT;
}

void android_wakeLooper(void) {
    pthread_mutex_lock(&g_looper_lock);
    g_looper_pending = 1;
    pthread_cond_signal(&g_looper_cond);
    pthread_mutex_unlock(&g_looper_lock);
}

ANDROIDRT_EXPORT int ALooper_pollOnce(int timeoutMs, int *outFd, int *outEvents, void **outData) {
    if (outFd) *outFd = -1;
    if (outEvents) *outEvents = 0;
    if (outData) *outData = NULL;
    /* Input queues ride the ident, not an fd: returning LOOPER_ID_INPUT
     * with the queue as user data is the exact shape android_native_app_glue
     * dispatches on (computed from AInputQueue_attachLooper's registration
     * on Android; here the pending-set check in inputshim.c stands in). */
    if (android_inputHasEvents()) {
        if (outData) *outData = android_inputQueueCurrent();
        return 2 /* LOOPER_ID_INPUT, android/looper.h */;
    }
    int rc = android_pollOnce(timeoutMs);
    if (rc != ALOOPER_POLL_WAKE) return rc;
    if (android_inputHasEvents()) {
        if (outData) *outData = android_inputQueueCurrent();
        return 2; /* the producer woke us mid-wait */
    }
    return ALOOPER_POLL_WAKE;
}

ANDROIDRT_EXPORT int ALooper_pollAll(int timeoutMs, int *outFd, int *outEvents, void **outData) {
    return ALooper_pollOnce(timeoutMs, outFd, outEvents, outData);
}

ANDROIDRT_EXPORT void *ALooper_prepare(int opts) { (void)opts; return NULL; }
ANDROIDRT_EXPORT void ALooper_wake(void) { android_wakeLooper(); }
ANDROIDRT_EXPORT void ALooper_acquire(void *looper) { (void)looper; }
ANDROIDRT_EXPORT void ALooper_release(void *looper) { (void)looper; }

/* ── assets ──────────────────────────────────────────────────────────── */

/* AAsset is a stdio FILE* over the staged tree — no mmap games need yet. */
typedef struct AAsset {
    FILE *fp;
    long size;
} AAsset;

ANDROIDRT_EXPORT AAssetManager *AAssetManager_fromJava(void *env, void *assetManager) {
    (void)env;
    (void)assetManager;
    return android_openAssetManager(getenv("ANDROIDPAK_STAGED"));
}

struct AssetFileStream {
    FILE *fp;
    long size;
    long offset;
};

typedef struct AssetFileStream AssetFile;
typedef struct AssetManager {
    char root[1024];
} AssetManager;

AAssetManager *android_openAssetManager(const char *stagedRoot) {
    static AssetManager mgr;
    snprintf(mgr.root, sizeof(mgr.root), "%s/assets", stagedRoot ? stagedRoot : ".");
    return (AAssetManager *)&mgr;
}

ANDROIDRT_EXPORT AAsset *AAssetManager_open(AAssetManager *mgr, const char *fileName, int mode) {
    (void)mode;
    AssetManager *m = (AssetManager *)mgr;
    char path[2048];
    snprintf(path, sizeof(path), "%s/%s", m->root, fileName ? fileName : "");
    FILE *fp = fopen(path, "rb");
    if (!fp) return NULL;
    AssetFile *asset = calloc(1, sizeof(*asset));
    if (!asset) {
        fclose(fp);
        return NULL;
    }
    fseek(fp, 0, SEEK_END);
    asset->size = ftell(fp);
    fseek(fp, 0, SEEK_SET);
    asset->fp = fp;
    return (AAsset *)asset;
}

ANDROIDRT_EXPORT int AAsset_read(AAsset *asset, void *buf, size_t count) {
    if (!asset || !buf) return 0;
    return (int)fread(buf, 1, count, ((AssetFile *)asset)->fp);
}

ANDROIDRT_EXPORT int AAsset_seek(AAsset *asset, off64_t offset, int whence) {
    if (!asset) return -1;
    return fseek(((AssetFile *)asset)->fp, (long)offset, whence) == 0 ? 0 : -1;
}

ANDROIDRT_EXPORT off64_t AAsset_getLength(AAsset *asset) {
    return asset ? ((AssetFile *)asset)->size : 0;
}

ANDROIDRT_EXPORT off64_t AAsset_getRemainingLength(AAsset *asset) {
    if (!asset) return 0;
    AssetFile *a = (AssetFile *)asset;
    long cur = ftell(a->fp);
    return a->size - cur;
}

ANDROIDRT_EXPORT void AAsset_close(AAsset *asset) {
    if (!asset) return;
    fclose(((AssetFile *)asset)->fp);
    free(asset);
}

ANDROIDRT_EXPORT const void *AAsset_getBuffer(AAsset *asset) {
    (void)asset;
    return NULL; /* mmap support comes with the window backend */
}

ANDROIDRT_EXPORT int AAsset_openFileDescriptor(AAsset *asset, off64_t *outStart, off64_t *outLength) {
    (void)asset;
    (void)outStart;
    (void)outLength;
    return -1; /* AASSET_MODE_UNKNOWN fds unused */
}

/* ── misc ────────────────────────────────────────────────────────────── */

ANDROIDRT_EXPORT void AConfiguration_fromAssetManager(AAssetManager *mgr, AConfiguration *config) { (void)mgr; (void)config; }
ANDROIDRT_EXPORT void AConfiguration_delete(AConfiguration *config) { (void)config; }
ANDROIDRT_EXPORT int32_t AConfiguration_getDensity(AConfiguration *config) { (void)config; return 320; }
ANDROIDRT_EXPORT int32_t AConfiguration_getScreenWidthDp(AConfiguration *config) { (void)config; return 790; }
ANDROIDRT_EXPORT int32_t AConfiguration_getScreenHeightDp(AConfiguration *config) { (void)config; return 300; }
ANDROIDRT_EXPORT int32_t ANativeActivity_nativeWindowFormat(ANativeActivity *activity) {
    (void)activity;
    return WINDOW_FORMAT_RGBA_8888;
}
