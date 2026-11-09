/* androidshim.c — libandroid for Tier 1: the subset NativeActivity NDK
 * games touch from a native entry —
 *   ANativeActivity_finish / getWindow / window-flag calls
 *   ANativeWindow_* queries, buffer geometry, software lock/post
 *   AAssetManager_* / AAsset_* / AAssetDir_* over the staged assets/ tree
 *   AConfiguration_* describing the Smart Pro (1280x720 landscape, xhdpi,
 *     d-pad navigation, no touch, SDK 28)
 *   ASensorManager_* (a device without sensors), ATrace_* (no tracing)
 * ALooper/AChoreographer live in looper.c, AInputQueue in inputshim.c.
 * Everything camera/identity/privacy-shaped is deliberately absent.
 *
 * The window is a handle, not a surface: games draw through EGL on the
 * device's glibc GPU userspace, and the present layer (eglpresent.c) turns
 * this handle into the native window that userspace understands.
 */

#define _GNU_SOURCE
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "actbridge.h"
#include "apkzip.h"
#include "device.h"

#ifndef O_CLOEXEC
#define O_CLOEXEC 0 /* MinGW smoke builds */
#endif

/* ── window ──────────────────────────────────────────────────────────── */

enum {
    WINDOW_FORMAT_RGBA_8888 = 1,
    WINDOW_FORMAT_RGBX_8888 = 2,
    WINDOW_FORMAT_RGB_565   = 4,
};

typedef struct NativeWindow {
    int32_t width;          /* buffer size (setBuffersGeometry, else panel) */
    int32_t height;
    int32_t format;
    int32_t refcount;
    int32_t panelWidth;     /* the window itself                          */
    int32_t panelHeight;
    void *swBits;           /* ANativeWindow_lock buffer                  */
    int32_t swStride;
    int32_t swFormat;
} NativeWindow;

typedef struct ANativeWindow_Buffer {
    int32_t width;
    int32_t height;
    int32_t stride;
    int32_t format;
    void *bits;
    uint32_t reserved[6];
} ANativeWindow_Buffer;

static NativeWindow *asWindow(ANativeWindow *w) { return (NativeWindow *)w; }

static NativeWindow *g_window; /* the one window this runner drives */
static pthread_mutex_t g_window_lock = PTHREAD_MUTEX_INITIALIZER;

/* Software-rendering present hook (eglpresent.c installs one when it can
 * scan out a CPU buffer); NULL = lock/unlockAndPost work but show nothing. */
typedef int (*SoftwarePresentFn)(const void *bits, int width, int height, int stride, int format);
static SoftwarePresentFn g_swPresent;
void android_setSoftwarePresent(SoftwarePresentFn fn);
void android_setSoftwarePresent(SoftwarePresentFn fn) { g_swPresent = fn; }

void android_activitySetWindow(ANativeActivity *activity, ANativeWindow *window) {
    if (activity) activity->window = window;
    pthread_mutex_lock(&g_window_lock);
    g_window = (NativeWindow *)window;
    pthread_mutex_unlock(&g_window_lock);
}

void android_activityClearWindow(ANativeActivity *activity, ANativeWindow *window) {
    if (activity && activity->window == window) activity->window = NULL;
    if (!window) return;
    pthread_mutex_lock(&g_window_lock);
    if (g_window == (NativeWindow *)window) g_window = NULL;
    pthread_mutex_unlock(&g_window_lock);
}

ANativeWindow *android_currentWindow(void) {
    ANativeWindow *w;
    pthread_mutex_lock(&g_window_lock);
    w = (ANativeWindow *)g_window;
    pthread_mutex_unlock(&g_window_lock);
    return w;
}

static NativeWindow *windowCreate(int32_t width, int32_t height, int32_t format) {
    NativeWindow *w = (NativeWindow *)calloc(1, sizeof(*w));
    if (w) {
        w->width = w->panelWidth = width;
        w->height = w->panelHeight = height;
        w->format = format ? format : WINDOW_FORMAT_RGBA_8888;
        w->refcount = 1;
    }
    return w;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_getWidth(ANativeWindow *window) {
    int32_t width;
    pthread_mutex_lock(&g_window_lock);
    width = window ? asWindow(window)->width : 0;
    pthread_mutex_unlock(&g_window_lock);
    return width;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_getHeight(ANativeWindow *window) {
    int32_t height;
    pthread_mutex_lock(&g_window_lock);
    height = window ? asWindow(window)->height : 0;
    pthread_mutex_unlock(&g_window_lock);
    return height;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_getFormat(ANativeWindow *window) {
    int32_t format;
    pthread_mutex_lock(&g_window_lock);
    format = window ? asWindow(window)->format : 0;
    pthread_mutex_unlock(&g_window_lock);
    return format;
}

/* width/height 0 = back to the window size; format 0 = keep. The format a
 * game passes is often EGL_NATIVE_VISUAL_ID (a DRM fourcc on GBM stacks),
 * not a WINDOW_FORMAT_*: it is recorded as is. */
ANDROIDRT_EXPORT int32_t ANativeWindow_setBuffersGeometry(ANativeWindow *window, int32_t width, int32_t height,
                                                         int32_t format) {
    NativeWindow *w = asWindow(window);
    if (!w || width < 0 || height < 0 || (width == 0) != (height == 0)) return -22; /* -EINVAL */
    pthread_mutex_lock(&g_window_lock);
    w->width = width ? width : w->panelWidth;
    w->height = height ? height : w->panelHeight;
    if (format) w->format = format;
    pthread_mutex_unlock(&g_window_lock);
    return 0;
}

ANDROIDRT_EXPORT void ANativeWindow_acquire(ANativeWindow *window) {
    if (!window) return;
    pthread_mutex_lock(&g_window_lock);
    asWindow(window)->refcount++;
    pthread_mutex_unlock(&g_window_lock);
}

static void windowReleaseLocked(NativeWindow *w) {
    if (!w) return;
    if (--w->refcount <= 0) {
        free(w->swBits);
        free(w);
    }
}

ANDROIDRT_EXPORT void ANativeWindow_release(ANativeWindow *window) {
    if (!window) return;
    pthread_mutex_lock(&g_window_lock);
    windowReleaseLocked(asWindow(window));
    pthread_mutex_unlock(&g_window_lock);
}

ANDROIDRT_EXPORT int32_t ANativeWindow_lock(ANativeWindow *window, ANativeWindow_Buffer *out, ARect *dirty) {
    NativeWindow *w = asWindow(window);
    int bpp;
    if (!w || !out) return -22;
    pthread_mutex_lock(&g_window_lock);
    bpp = w->format == WINDOW_FORMAT_RGB_565 ? 2 : 4;
    if (!w->swBits || w->swStride != w->width || w->swFormat != w->format) {
        free(w->swBits);
        w->swBits = calloc((size_t)w->width * (size_t)w->height, (size_t)bpp);
        w->swStride = w->width;
        w->swFormat = w->format;
    }
    out->width = w->width;
    out->height = w->height;
    out->stride = w->swStride;
    out->format = w->format == WINDOW_FORMAT_RGB_565 ? WINDOW_FORMAT_RGB_565 : WINDOW_FORMAT_RGBA_8888;
    out->bits = w->swBits;
    memset(out->reserved, 0, sizeof out->reserved);
    pthread_mutex_unlock(&g_window_lock);
    if (dirty) {
        dirty->left = dirty->top = 0;
        dirty->right = out->width;
        dirty->bottom = out->height;
    }
    return out->bits ? 0 : -12; /* -ENOMEM */
}

ANDROIDRT_EXPORT int32_t ANativeWindow_unlockAndPost(ANativeWindow *window) {
    NativeWindow *w = asWindow(window);
    static int warned;
    if (!w) return -22;
    if (g_swPresent) return g_swPresent(w->swBits, w->width, w->height, w->swStride, w->swFormat);
    if (!warned) {
        warned = 1;
        fprintf(stderr, "[androidrt] window: software rendering (ANativeWindow_lock/unlockAndPost) has no scanout path yet — frames are dropped\n");
    }
    return 0;
}

ANDROIDRT_EXPORT int32_t ANativeWindow_setBuffersTransform(ANativeWindow *window, int32_t transform) {
    (void)window;
    (void)transform;
    return 0;
}
ANDROIDRT_EXPORT int32_t ANativeWindow_setBuffersDataSpace(ANativeWindow *window, int32_t dataSpace) {
    (void)window;
    (void)dataSpace;
    return 0;
}
ANDROIDRT_EXPORT int32_t ANativeWindow_getBuffersDataSpace(ANativeWindow *window) {
    (void)window;
    return 0;
}
ANDROIDRT_EXPORT int32_t ANativeWindow_setFrameRate(ANativeWindow *window, float frameRate, int8_t compatibility) {
    (void)window;
    (void)frameRate;
    (void)compatibility;
    return 0;
}
ANDROIDRT_EXPORT void ANativeWindow_tryAllocateBuffers(ANativeWindow *window) { (void)window; }
ANDROIDRT_EXPORT ANativeWindow *ANativeWindow_fromSurface(void *env, void *surface) {
    ANativeWindow *w = android_currentWindow();
    (void)env;
    (void)surface;
    if (w) ANativeWindow_acquire(w);
    return w;
}

/* The backend hook: issues the window the lifecycle hands the game. The
 * EGL present layer (eglpresent.c) turns it into a real scanout surface
 * when the game creates its EGL window surface. */
ANDROIDRT_EXPORT int android_windowBackend(int width, int height) {
    NativeWindow *w = windowCreate(width, height, WINDOW_FORMAT_RGBA_8888);
    pthread_mutex_lock(&g_window_lock);
    windowReleaseLocked(g_window);
    g_window = w;
    pthread_mutex_unlock(&g_window_lock);
    fprintf(stderr, "[androidrt] window backend issued %dx%d\n", width, height);
#ifndef JUKAHUB_SHIM_SO
    /* ANDROIDPAK_SURFACE=1 also runs the surface.c measurement: which layer
     * of this firmware's GPU stack works. A probe failure never breaks the
     * run — only the measurement prints. */
    {
        const char *surfaceProbe = getenv("ANDROIDPAK_SURFACE");
        if (surfaceProbe && strcmp(surfaceProbe, "0") != 0) {
            extern int android_surfaceProbe(int width, int height, int *works);
            int works = 0;
            if (android_surfaceProbe(width, height, &works) != 0)
                fprintf(stderr, "[androidrt][surface] probe could not complete — the log above says which layer died\n");
            else if (!works)
                fprintf(stderr, "[androidrt][surface] probe completed: GBM stack not usable (see log above)\n");
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
    return android_currentWindow();
}

ANDROIDRT_EXPORT void ANativeActivity_finish(ANativeActivity *activity) {
    (void)activity;
    fprintf(stderr, "[androidrt] lifecycle: ANativeActivity_finish requested by the game\n");
    runner_requestExit(g_activity.runner, 0);
}

ANDROIDRT_EXPORT void ANativeActivity_setWindowFormat(ANativeActivity *activity, int32_t format) {
    (void)activity;
    (void)format;
}
ANDROIDRT_EXPORT void ANativeActivity_setWindowFlags(ANativeActivity *activity, uint32_t addFlags, uint32_t removeFlags) {
    (void)activity;
    (void)addFlags;
    (void)removeFlags;
}
ANDROIDRT_EXPORT void ANativeActivity_showSoftInput(ANativeActivity *activity, uint32_t flags) {
    (void)activity;
    (void)flags;
}
ANDROIDRT_EXPORT void ANativeActivity_hideSoftInput(ANativeActivity *activity, uint32_t flags) {
    (void)activity;
    (void)flags;
}
ANDROIDRT_EXPORT int32_t ANativeActivity_nativeWindowFormat(ANativeActivity *activity) {
    (void)activity;
    return WINDOW_FORMAT_RGBA_8888;
}

/* ── the runner's main-thread wait (not a game looper: looper.c) ────── */

static pthread_mutex_t g_looper_lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t g_looper_cond = PTHREAD_COND_INITIALIZER;
static int g_looper_pending;

/* Sleep up to timeoutMs (16 ms when negative) unless woken. 1 = woken. */
int android_pollOnce(int timeoutMs) {
    struct timespec deadline;
    int woke;
    pthread_mutex_lock(&g_looper_lock);
    if (g_looper_pending) {
        g_looper_pending = 0;
        pthread_mutex_unlock(&g_looper_lock);
        return 1;
    }
    if (timeoutMs < 0) timeoutMs = 16;
    clock_gettime(CLOCK_REALTIME, &deadline);
    deadline.tv_sec += timeoutMs / 1000;
    deadline.tv_nsec += (long)(timeoutMs % 1000) * 1000000L;
    if (deadline.tv_nsec >= 1000000000L) {
        deadline.tv_sec++;
        deadline.tv_nsec -= 1000000000L;
    }
    while (!g_looper_pending) {
        if (pthread_cond_timedwait(&g_looper_cond, &g_looper_lock, &deadline) != 0) break;
    }
    woke = g_looper_pending;
    g_looper_pending = 0;
    pthread_mutex_unlock(&g_looper_lock);
    return woke;
}

void android_wakeLooper(void) {
    pthread_mutex_lock(&g_looper_lock);
    g_looper_pending = 1;
    pthread_cond_signal(&g_looper_cond);
    pthread_mutex_unlock(&g_looper_lock);
}

/* ── assets ──────────────────────────────────────────────────────────────
 * Two sources, in this order: the staged assets/ tree (small games: JukaHub
 * extracted them to tmpfs) and the APK itself on the SD card (large games:
 * JukaHub stages only lib/<abi>; stored entries are read in place, deflated
 * ones inflated through the device's zlib). AAsset_openFileDescriptor on a
 * stored APK entry returns the APK's descriptor with the entry's offset —
 * exactly what Android does — and -1 for a compressed one. */

#define ASSET_INFLATE_WHOLE (8L << 20) /* deflated assets up to this are inflated on open */

typedef struct AssetManager {
    char root[1024];
} AssetManager;

typedef struct AAsset {
    FILE *fp;              /* staged file                                */
    ApakZipStream *zs;     /* APK entry read incrementally              */
    const ApakZipEntry *ze;
    int stored;            /* APK entry stored uncompressed             */
    long size;
    void *buffer;          /* AAsset_getBuffer / an inflated APK entry  */
    int fromBuffer;        /* reads are served from buffer at pos       */
    long pos;
    char path[2048];
} AssetFile;

typedef struct AAssetDir {
    DIR *dir;              /* staged directory, when it exists          */
    char path[2048];
    char prefix[1024];     /* "assets/<dir>/" inside the APK            */
    size_t zipNext;
    char current[512];
} AssetDir;

static pthread_once_t g_apkOnce = PTHREAD_ONCE_INIT;
static ApakZip *g_apk;

static void apkOpen(void) {
    const char *path = getenv("ANDROIDPAK_APK");
    char err[200];
    struct stat st;
    if (!path || !*path || stat(path, &st) != 0) return;
    g_apk = apak_zipOpen(path, err, sizeof err);
    if (!g_apk)
        fprintf(stderr, "[androidrt] assets: cannot index the APK %s (%s) — only staged assets are visible\n", path, err);
    else
        fprintf(stderr, "[androidrt] assets: APK indexed (%zu entries; inflate via %s)\n", apak_zipCount(g_apk),
                apak_zipInflateLib()[0] ? apak_zipInflateLib() : "nothing — deflated assets unreadable");
}

struct ApakZip *android_apkArchive(void) {
    pthread_once(&g_apkOnce, apkOpen);
    return g_apk;
}

AAssetManager *android_openAssetManager(const char *stagedRoot) {
    static AssetManager mgr;
    snprintf(mgr.root, sizeof(mgr.root), "%s/assets", stagedRoot ? stagedRoot : ".");
    return (AAssetManager *)&mgr;
}

ANDROIDRT_EXPORT AAssetManager *AAssetManager_fromJava(void *env, void *assetManager) {
    (void)env;
    (void)assetManager;
    return android_openAssetManager(getenv("ANDROIDPAK_STAGED"));
}

/* Asset names are relative ("textures/a.png"); tolerate "./" and "/". */
static const char *assetName(const char *name) {
    if (!name) return "";
    while (name[0] == '.' && name[1] == '/') name += 2;
    while (name[0] == '/') name++;
    return name;
}

static AssetFile *openFromApk(const char *name) {
    char entryName[2048];
    const ApakZipEntry *e;
    AssetFile *asset;
    ApakZip *apk = android_apkArchive();
    if (!apk) return NULL;
    /* A name that does not fit is no entry: a cut one could name another. */
    if (snprintf(entryName, sizeof entryName, "assets/%s", name) >= (int)sizeof entryName) return NULL;
    e = apak_zipFind(apk, entryName);
    if (!e || e->size > 0x7fffffffu) return NULL;
    asset = (AssetFile *)calloc(1, sizeof(*asset));
    if (!asset) return NULL;
    asset->ze = e;
    asset->size = (long)e->size;
    asset->stored = e->method == 0;
    /* "<apk>!/assets/<name>" is only a label (APK entries are never opened
     * by path); behind an over-long APK path the entry name alone. */
    if (snprintf(asset->path, sizeof asset->path, "%s!/%s", apak_zipPath(apk), entryName) >= (int)sizeof asset->path)
        snprintf(asset->path, sizeof asset->path, "%s", entryName);
    if (!asset->stored && asset->size <= ASSET_INFLATE_WHOLE) {
        char err[160];
        size_t len = 0;
        asset->buffer = apak_zipReadAll(apk, e, ASSET_INFLATE_WHOLE, &len, err, sizeof err);
        if (!asset->buffer) {
            fprintf(stderr, "[androidrt] assets: %s: %s\n", entryName, err);
            free(asset);
            return NULL;
        }
        asset->fromBuffer = 1;
        return asset;
    }
    asset->zs = apak_zipStreamOpen(apk, e);
    if (!asset->zs) {
        fprintf(stderr, "[androidrt] assets: cannot read %s from the APK\n", entryName);
        free(asset);
        return NULL;
    }
    return asset;
}

ANDROIDRT_EXPORT AssetFile *AAssetManager_open(AAssetManager *mgr, const char *fileName, int mode) {
    AssetManager *m = (AssetManager *)mgr;
    AssetFile *asset;
    FILE *fp;
    struct stat st;
    char path[2048];
    (void)mode;
    if (!m) return NULL;
    snprintf(path, sizeof(path), "%s/%s", m->root, assetName(fileName));
    if (stat(path, &st) != 0 || !S_ISREG(st.st_mode)) return openFromApk(assetName(fileName));
    fp = fopen(path, "rb");
    if (!fp) return NULL;
    asset = (AssetFile *)calloc(1, sizeof(*asset));
    if (!asset) {
        fclose(fp);
        return NULL;
    }
    asset->fp = fp;
    asset->size = (long)st.st_size;
    snprintf(asset->path, sizeof asset->path, "%s", path);
    return asset;
}

static long assetTell(AssetFile *asset) {
    if (asset->fp) return (long)ftello(asset->fp);
    if (asset->fromBuffer) return asset->pos;
    return (long)apak_zipStreamTell(asset->zs);
}

ANDROIDRT_EXPORT int AAsset_read(AssetFile *asset, void *buf, size_t count) {
    if (!asset || !buf) return -1;
    if (asset->fp) return (int)fread(buf, 1, count, asset->fp);
    if (asset->fromBuffer) {
        long left = asset->size - asset->pos;
        if (left <= 0) return 0;
        if ((long)count > left) count = (size_t)left;
        memcpy(buf, (const char *)asset->buffer + asset->pos, count);
        asset->pos += (long)count;
        return (int)count;
    }
    return (int)apak_zipStreamRead(asset->zs, buf, count);
}

/* NDK: returns the new position, or (off_t)-1 on error. */
ANDROIDRT_EXPORT off_t AAsset_seek(AssetFile *asset, off_t offset, int whence) {
    if (!asset) return (off_t)-1;
    if (asset->fp) {
        if (fseeko(asset->fp, offset, whence) != 0) return (off_t)-1;
        return ftello(asset->fp);
    }
    if (asset->fromBuffer) {
        long target = whence == SEEK_SET ? (long)offset : whence == SEEK_CUR ? asset->pos + (long)offset
                                                                             : asset->size + (long)offset;
        if (target < 0 || target > asset->size) return (off_t)-1;
        asset->pos = target;
        return (off_t)target;
    }
    return (off_t)apak_zipStreamSeek(asset->zs, (int64_t)offset, whence == SEEK_SET ? 0 : whence == SEEK_CUR ? 1 : 2);
}
ANDROIDRT_EXPORT off64_t AAsset_seek64(AssetFile *asset, off64_t offset, int whence) {
    return (off64_t)AAsset_seek(asset, (off_t)offset, whence);
}
ANDROIDRT_EXPORT off_t AAsset_getLength(AssetFile *asset) { return asset ? (off_t)asset->size : 0; }
ANDROIDRT_EXPORT off64_t AAsset_getLength64(AssetFile *asset) { return asset ? (off64_t)asset->size : 0; }
ANDROIDRT_EXPORT off_t AAsset_getRemainingLength(AssetFile *asset) {
    long cur;
    if (!asset) return 0;
    cur = assetTell(asset);
    return cur < 0 ? 0 : (off_t)(asset->size - cur);
}
ANDROIDRT_EXPORT off64_t AAsset_getRemainingLength64(AssetFile *asset) {
    return (off64_t)AAsset_getRemainingLength(asset);
}

ANDROIDRT_EXPORT void AAsset_close(AssetFile *asset) {
    if (!asset) return;
    if (asset->fp) fclose(asset->fp);
    if (asset->zs) apak_zipStreamClose(asset->zs);
    free(asset->buffer);
    free(asset);
}

/* The whole asset in RAM, read on first request (engines map shaders and
 * small files this way). The position is left where it was. */
ANDROIDRT_EXPORT const void *AAsset_getBuffer(AssetFile *asset) {
    off_t pos;
    if (!asset) return NULL;
    if (asset->buffer) return asset->buffer;
    if (!asset->fp) {
        char err[160];
        size_t len = 0;
        asset->buffer = asset->ze ? apak_zipReadAll(android_apkArchive(), asset->ze, (size_t)asset->size, &len, err,
                                                    sizeof err)
                                  : NULL;
        return asset->buffer;
    }
    asset->buffer = malloc(asset->size > 0 ? (size_t)asset->size : 1);
    if (!asset->buffer) return NULL;
    pos = ftello(asset->fp);
    if (fseeko(asset->fp, 0, SEEK_SET) != 0 ||
        fread(asset->buffer, 1, (size_t)asset->size, asset->fp) != (size_t)asset->size) {
        free(asset->buffer);
        asset->buffer = NULL;
    }
    fseeko(asset->fp, pos < 0 ? 0 : pos, SEEK_SET);
    return asset->buffer;
}

ANDROIDRT_EXPORT int AAsset_isAllocated(AssetFile *asset) { return asset && asset->buffer ? 1 : 0; }

/* Staged assets are plain files, so a real descriptor works (FMOD and media
 * players read assets this way): start 0, the whole file. A stored APK entry
 * is the APK's own descriptor at the entry's offset; a compressed one has no
 * descriptor (Android says the same: -1). */
ANDROIDRT_EXPORT int AAsset_openFileDescriptor(AssetFile *asset, off_t *outStart, off_t *outLength) {
    int fd;
    if (!asset) return -1;
    if (!asset->fp) {
        uint64_t offset = 0;
        if (!asset->stored || !asset->ze || apak_zipDataOffset(android_apkArchive(), asset->ze, &offset) != 0) return -1;
        fd = apak_zipDupFd(android_apkArchive());
        if (fd < 0) return -1;
        if (outStart) *outStart = (off_t)offset;
        if (outLength) *outLength = (off_t)asset->size;
        return fd;
    }
    fd = open(asset->path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) return -1;
    if (outStart) *outStart = 0;
    if (outLength) *outLength = (off_t)asset->size;
    return fd;
}
ANDROIDRT_EXPORT int AAsset_openFileDescriptor64(AssetFile *asset, off64_t *outStart, off64_t *outLength) {
    off_t s = 0, l = 0;
    int fd = AAsset_openFileDescriptor(asset, &s, &l);
    if (fd >= 0) {
        if (outStart) *outStart = s;
        if (outLength) *outLength = l;
    }
    return fd;
}

/* "assets/<dir>/" (or "assets/" for the root) — the APK prefix of a dir. */
static void apkPrefix(const char *dirName, char *out, size_t cap) {
    const char *n = assetName(dirName);
    size_t len = strlen(n);
    while (len && n[len - 1] == '/') len--;
    if (len) snprintf(out, cap, "assets/%.*s/", (int)len, n);
    else snprintf(out, cap, "assets/");
}

/* AAssetDir lists the FILES of one asset directory (never subdirectories,
 * like Android's): the staged directory when it exists, else the APK's. */
ANDROIDRT_EXPORT AssetDir *AAssetManager_openDir(AAssetManager *mgr, const char *dirName) {
    AssetManager *m = (AssetManager *)mgr;
    AssetDir *d;
    if (!m) return NULL;
    d = (AssetDir *)calloc(1, sizeof(*d));
    if (!d) return NULL;
    snprintf(d->path, sizeof d->path, "%s/%s", m->root, assetName(dirName));
    d->dir = opendir(d->path);
    apkPrefix(dirName, d->prefix, sizeof d->prefix);
    return d; /* Android returns an empty dir, not NULL, for a missing path */
}

ANDROIDRT_EXPORT const char *AAssetDir_getNextFileName(AssetDir *d) {
    struct dirent *e;
    ApakZip *apk;
    size_t plen;
    if (!d) return NULL;
    if (d->dir) {
        while ((e = readdir(d->dir)) != NULL) {
            char full[2600];
            struct stat st;
            if (e->d_name[0] == '.' && (e->d_name[1] == '\0' || (e->d_name[1] == '.' && e->d_name[2] == '\0')))
                continue;
            snprintf(full, sizeof full, "%s/%s", d->path, e->d_name);
            if (stat(full, &st) != 0 || !S_ISREG(st.st_mode)) continue;
            snprintf(d->current, sizeof d->current, "%s", e->d_name);
            return d->current;
        }
        return NULL;
    }
    apk = android_apkArchive();
    if (!apk) return NULL;
    plen = strlen(d->prefix);
    while (d->zipNext < apak_zipCount(apk)) {
        const ApakZipEntry *z = apak_zipAt(apk, d->zipNext++);
        const char *rest;
        if (z->nameLen <= plen || strncmp(z->name, d->prefix, plen) != 0) continue;
        rest = z->name + plen;
        if (!*rest || strchr(rest, '/')) continue;
        snprintf(d->current, sizeof d->current, "%s", rest);
        return d->current;
    }
    return NULL;
}

ANDROIDRT_EXPORT void AAssetDir_rewind(AssetDir *d) {
    if (!d) return;
    if (d->dir) rewinddir(d->dir);
    d->zipNext = 0;
}

static int nameCmp(const void *a, const void *b) { return strcmp(*(char *const *)a, *(char *const *)b); }

static char *dupPrefix(const char *s, size_t n) {
    char *d = (char *)malloc(n + 1);
    if (d) {
        memcpy(d, s, n);
        d[n] = '\0';
    }
    return d;
}

/* AssetManager.list(dir): names of files and directories directly below. */
size_t android_assetList(const char *dir, char ***namesOut) {
    char **names = NULL;
    size_t n = 0, cap = 0, i, w;
    char path[2048], prefix[1024];
    const char *stagedRoot = getenv("ANDROIDPAK_STAGED");
    DIR *dd;
    ApakZip *apk;
    *namesOut = NULL;
    snprintf(path, sizeof path, "%s/assets/%s", stagedRoot && *stagedRoot ? stagedRoot : ".", assetName(dir));
    dd = opendir(path);
    if (dd) {
        struct dirent *e;
        while ((e = readdir(dd)) != NULL) {
            if (e->d_name[0] == '.' && (e->d_name[1] == '\0' || (e->d_name[1] == '.' && e->d_name[2] == '\0')))
                continue;
            if (n == cap) {
                char **grown = (char **)realloc(names, (cap = cap ? cap * 2 : 32) * sizeof *grown);
                if (!grown) break;
                names = grown;
            }
            names[n++] = strdup(e->d_name);
        }
        closedir(dd);
    } else if ((apk = android_apkArchive()) != NULL) {
        size_t plen;
        apkPrefix(dir, prefix, sizeof prefix);
        plen = strlen(prefix);
        for (i = 0; i < apak_zipCount(apk); i++) {
            const ApakZipEntry *z = apak_zipAt(apk, i);
            const char *rest, *slash;
            if (z->nameLen <= plen || strncmp(z->name, prefix, plen) != 0) continue;
            rest = z->name + plen;
            slash = strchr(rest, '/');
            if (slash == rest || !*rest) continue;
            if (n == cap) {
                char **grown = (char **)realloc(names, (cap = cap ? cap * 2 : 32) * sizeof *grown);
                if (!grown) break;
                names = grown;
            }
            names[n++] = dupPrefix(rest, slash ? (size_t)(slash - rest) : strlen(rest));
        }
    }
    if (!n) {
        free(names);
        return 0;
    }
    qsort(names, n, sizeof *names, nameCmp);
    for (i = 1, w = 1; i < n; i++) {
        if (names[i] && names[w - 1] && strcmp(names[i], names[w - 1]) == 0) {
            free(names[i]);
            continue;
        }
        names[w++] = names[i];
    }
    *namesOut = names;
    return w;
}

void android_assetListFree(char **names, size_t n) {
    size_t i;
    for (i = 0; names && i < n; i++) free(names[i]);
    free(names);
}

ANDROIDRT_EXPORT void AAssetDir_close(AssetDir *d) {
    if (!d) return;
    if (d->dir) closedir(d->dir);
    free(d);
}

/* ── AConfiguration: the Smart Pro as Android would describe it ──────── */

typedef struct ApakConfig {
    int32_t mcc, mnc;
    char language[2], country[2];
    int32_t orientation, touchscreen, density, keyboard, navigation, keysHidden, navHidden;
    int32_t sdkVersion, screenSize, screenLong, screenRound, uiModeType, uiModeNight;
    int32_t screenWidthDp, screenHeightDp, smallestScreenWidthDp, layoutDirection;
} ApakConfig;

static void configDefaults(ApakConfig *c) {
    int w = 1280, h = 720;
    const char *lang = getenv("LC_ALL");
    apak_devicePanel(&w, &h);
    memset(c, 0, sizeof *c);
    memcpy(c->language, "en", 2);
    memcpy(c->country, "US", 2);
    if (!lang || !*lang) lang = getenv("LANG");
    /* LANG=de_DE.UTF-8 → language "de", country "DE" (C/POSIX stay en-US) */
    if (lang && strlen(lang) >= 5 && lang[2] == '_' && lang[0] >= 'a' && lang[0] <= 'z' && lang[1] >= 'a' &&
        lang[1] <= 'z' && lang[3] >= 'A' && lang[3] <= 'Z' && lang[4] >= 'A' && lang[4] <= 'Z') {
        memcpy(c->language, lang, 2);
        memcpy(c->country, lang + 3, 2);
    }
    c->orientation = w >= h ? 2 : 1;  /* ACONFIGURATION_ORIENTATION_LAND/PORT */
    c->touchscreen = 1;               /* NOTOUCH: buttons and sticks only     */
    c->density = apak_deviceDensity(); /* DENSITY_XHIGH                       */
    c->keyboard = 1;                  /* NOKEYS                               */
    c->navigation = 2;                /* DPAD                                 */
    c->keysHidden = 2;                /* YES (no hardware keyboard)           */
    c->navHidden = 1;                 /* NO: the d-pad is always there        */
    c->sdkVersion = ANDROIDRT_SDK_VERSION;
    c->screenSize = 2;                /* NORMAL                               */
    c->screenLong = 2;                /* YES (16:9)                           */
    c->screenRound = 1;               /* NO                                   */
    c->uiModeType = 1;                /* NORMAL                               */
    c->uiModeNight = 1;               /* NO                                   */
    c->screenWidthDp = w * 160 / c->density;
    c->screenHeightDp = h * 160 / c->density;
    c->smallestScreenWidthDp = (w < h ? w : h) * 160 / c->density;
    c->layoutDirection = 1;           /* LTR                                  */
}

#define CFG(c) ((ApakConfig *)(c))

ANDROIDRT_EXPORT AConfiguration *AConfiguration_new(void) {
    ApakConfig *c = (ApakConfig *)calloc(1, sizeof *c);
    if (c) configDefaults(c);
    return c;
}
ANDROIDRT_EXPORT void AConfiguration_delete(AConfiguration *config) { free(config); }
ANDROIDRT_EXPORT void AConfiguration_fromAssetManager(AConfiguration *out, AAssetManager *am) {
    (void)am;
    if (out) configDefaults(CFG(out));
}
ANDROIDRT_EXPORT void AConfiguration_copy(AConfiguration *dest, AConfiguration *src) {
    if (dest && src) memcpy(dest, src, sizeof(ApakConfig));
}
ANDROIDRT_EXPORT void AConfiguration_getLanguage(AConfiguration *config, char *out) {
    if (config && out) memcpy(out, CFG(config)->language, 2);
}
ANDROIDRT_EXPORT void AConfiguration_setLanguage(AConfiguration *config, const char *language) {
    if (config && language) memcpy(CFG(config)->language, language, 2);
}
ANDROIDRT_EXPORT void AConfiguration_getCountry(AConfiguration *config, char *out) {
    if (config && out) memcpy(out, CFG(config)->country, 2);
}
ANDROIDRT_EXPORT void AConfiguration_setCountry(AConfiguration *config, const char *country) {
    if (config && country) memcpy(CFG(config)->country, country, 2);
}

#define CONFIG_FIELD(Name, field) \
    ANDROIDRT_EXPORT int32_t AConfiguration_get##Name(AConfiguration *config) { \
        return config ? CFG(config)->field : 0; \
    } \
    ANDROIDRT_EXPORT void AConfiguration_set##Name(AConfiguration *config, int32_t value) { \
        if (config) CFG(config)->field = value; \
    }

CONFIG_FIELD(Mcc, mcc)
CONFIG_FIELD(Mnc, mnc)
CONFIG_FIELD(Orientation, orientation)
CONFIG_FIELD(Touchscreen, touchscreen)
CONFIG_FIELD(Density, density)
CONFIG_FIELD(Keyboard, keyboard)
CONFIG_FIELD(Navigation, navigation)
CONFIG_FIELD(KeysHidden, keysHidden)
CONFIG_FIELD(NavHidden, navHidden)
CONFIG_FIELD(SdkVersion, sdkVersion)
CONFIG_FIELD(ScreenSize, screenSize)
CONFIG_FIELD(ScreenLong, screenLong)
CONFIG_FIELD(ScreenRound, screenRound)
CONFIG_FIELD(UiModeType, uiModeType)
CONFIG_FIELD(UiModeNight, uiModeNight)
CONFIG_FIELD(ScreenWidthDp, screenWidthDp)
CONFIG_FIELD(ScreenHeightDp, screenHeightDp)
CONFIG_FIELD(SmallestScreenWidthDp, smallestScreenWidthDp)
CONFIG_FIELD(LayoutDirection, layoutDirection)

ANDROIDRT_EXPORT int32_t AConfiguration_diff(AConfiguration *a, AConfiguration *b) {
    return (a && b && memcmp(a, b, sizeof(ApakConfig)) != 0) ? 0x7fffffff : 0;
}
ANDROIDRT_EXPORT int32_t AConfiguration_match(AConfiguration *base, AConfiguration *requested) {
    (void)base;
    (void)requested;
    return 1;
}
ANDROIDRT_EXPORT int32_t AConfiguration_isBetterThan(AConfiguration *base, AConfiguration *test, AConfiguration *req) {
    (void)base;
    (void)test;
    (void)req;
    return 0;
}

/* ── sensors: the Smart Pro reports none ─────────────────────────────── */

static int g_sensorManager, g_sensorQueue;
static const void *const g_noSensors[1] = { NULL };

ANDROIDRT_EXPORT void *ASensorManager_getInstance(void) { return &g_sensorManager; }
ANDROIDRT_EXPORT void *ASensorManager_getInstanceForPackage(const char *packageName) {
    (void)packageName;
    return &g_sensorManager;
}
ANDROIDRT_EXPORT int ASensorManager_getSensorList(void *manager, const void *const **list) {
    (void)manager;
    if (list) *list = g_noSensors;
    return 0;
}
ANDROIDRT_EXPORT const void *ASensorManager_getDefaultSensor(void *manager, int type) {
    (void)manager;
    (void)type;
    return NULL;
}
ANDROIDRT_EXPORT const void *ASensorManager_getDefaultSensorEx(void *manager, int type, int wakeUp) {
    (void)manager;
    (void)type;
    (void)wakeUp;
    return NULL;
}
ANDROIDRT_EXPORT void *ASensorManager_createEventQueue(void *manager, ALooper *looper, int ident,
                                                       ALooper_callbackFunc callback, void *data) {
    (void)manager;
    (void)looper;
    (void)ident;
    (void)callback;
    (void)data;
    return &g_sensorQueue;
}
ANDROIDRT_EXPORT int ASensorManager_destroyEventQueue(void *manager, void *queue) {
    (void)manager;
    (void)queue;
    return 0;
}
ANDROIDRT_EXPORT int ASensorEventQueue_enableSensor(void *queue, const void *sensor) {
    (void)queue;
    return sensor ? 0 : -22;
}
ANDROIDRT_EXPORT int ASensorEventQueue_disableSensor(void *queue, const void *sensor) {
    (void)queue;
    (void)sensor;
    return 0;
}
ANDROIDRT_EXPORT int ASensorEventQueue_setEventRate(void *queue, const void *sensor, int32_t usec) {
    (void)queue;
    (void)sensor;
    (void)usec;
    return 0;
}
ANDROIDRT_EXPORT int ASensorEventQueue_registerSensor(void *queue, const void *sensor, int32_t period,
                                                      int64_t latency) {
    (void)queue;
    (void)period;
    (void)latency;
    return sensor ? 0 : -22;
}
ANDROIDRT_EXPORT int ASensorEventQueue_hasEvents(void *queue) {
    (void)queue;
    return 0;
}
ANDROIDRT_EXPORT ssize_t ASensorEventQueue_getEvents(void *queue, void *events, size_t count) {
    (void)queue;
    (void)events;
    (void)count;
    return 0;
}
ANDROIDRT_EXPORT const char *ASensor_getName(const void *sensor) {
    (void)sensor;
    return "none";
}
ANDROIDRT_EXPORT const char *ASensor_getVendor(const void *sensor) {
    (void)sensor;
    return "AndroidRT";
}
ANDROIDRT_EXPORT int ASensor_getType(const void *sensor) {
    (void)sensor;
    return 0;
}
ANDROIDRT_EXPORT float ASensor_getResolution(const void *sensor) {
    (void)sensor;
    return 0.0f;
}
ANDROIDRT_EXPORT int ASensor_getMinDelay(const void *sensor) {
    (void)sensor;
    return 0;
}

/* ── ATrace: no systrace here ────────────────────────────────────────── */

ANDROIDRT_EXPORT void ATrace_beginSection(const char *name) { (void)name; }
ANDROIDRT_EXPORT void ATrace_endSection(void) {}
ANDROIDRT_EXPORT int ATrace_isEnabled(void) { return 0; }
ANDROIDRT_EXPORT void ATrace_beginAsyncSection(const char *name, int32_t cookie) {
    (void)name;
    (void)cookie;
}
ANDROIDRT_EXPORT void ATrace_endAsyncSection(const char *name, int32_t cookie) {
    (void)name;
    (void)cookie;
}
ANDROIDRT_EXPORT void ATrace_setCounter(const char *name, int64_t value) {
    (void)name;
    (void)value;
}
