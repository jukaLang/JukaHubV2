/* looper.c — ALooper and AChoreographer with the NDK's real semantics.
 *
 * android_native_app_glue (inside nearly every NativeActivity game) runs
 * its game thread on an ALooper it prepares itself: it registers the read
 * end of its command pipe with ALooper_addFd(LOOPER_ID_MAIN, data =
 * &cmdPollSource), attaches the input queue (LOOPER_ID_INPUT, data =
 * &inputPollSource) and then loops on ALooper_pollAll, calling
 * source->process() for whatever `data` the poll hands back. The framework
 * side (activity.c, the main thread) writes APP_CMD_* bytes into that pipe
 * from onStart/onResume/onNativeWindowCreated/... and blocks until the game
 * thread has consumed them — so a looper that does not poll real fds
 * deadlocks every glue game in onStart.
 *
 * This is Android's Looper reduced to poll(2): per-thread loopers via
 * ALooper_prepare, fd registrations with ident/callback/data, a wake pipe,
 * pending responses returned one ident at a time, callbacks invoked inside
 * the poll (ALOOPER_POLL_CALLBACK), pollAll looping over callbacks. The
 * choreographer is driven by the same loop at a 60 Hz vsync cadence.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "actbridge.h"

#define ALOOPER_PREPARE_ALLOW_NON_CALLBACKS 1

#define ALOOPER_POLL_WAKE     (-1)
#define ALOOPER_POLL_CALLBACK (-2)
#define ALOOPER_POLL_TIMEOUT  (-3)
#define ALOOPER_POLL_ERROR    (-4)

#define ALOOPER_EVENT_INPUT   (1 << 0)
#define ALOOPER_EVENT_OUTPUT  (1 << 1)
#define ALOOPER_EVENT_ERROR   (1 << 2)
#define ALOOPER_EVENT_HANGUP  (1 << 3)
#define ALOOPER_EVENT_INVALID (1 << 4)

#define APAK_VSYNC_NS 16666667LL /* the Smart Pro panel refreshes at 60 Hz */

typedef void (*FrameCb32)(long frameTimeNanos, void *data);
typedef void (*FrameCb64)(int64_t frameTimeNanos, void *data);

#if !defined(_WIN32)
#include <poll.h>
#include <unistd.h>

typedef struct {
    int fd;
    int ident;
    int events;
    ALooper_callbackFunc callback;
    void *data;
    unsigned seq;
} LooperReq;

typedef struct {
    int events;
    LooperReq req;
} LooperResp;

typedef struct FrameReq {
    FrameCb32 cb32;
    FrameCb64 cb64;
    void *data;
    int64_t dueNs;
    struct FrameReq *next;
} FrameReq;

typedef struct ApakLooper {
    pthread_mutex_t lock;
    int refs;
    int allowNonCallbacks;
    int wakeRead;
    int wakeWrite;
    LooperReq *reqs;
    int nreq;
    int capReq;
    unsigned seq;
    LooperResp *resps;      /* owned by the polling thread               */
    int nresp;
    int capResp;
    int respIndex;
    FrameReq *frames;
    int64_t lastVsyncNs;
    pthread_t owner;        /* the thread that prepared (and polls) it   */
} ApakLooper;

/* With poll(2) a registration change only reaches a thread already blocked
 * in pollInner if it is woken to rebuild its fd set; the owner itself is not
 * blocked, and waking it would hand the game a spurious ALOOPER_POLL_WAKE. */
static void wakeIfOtherThread(ApakLooper *L) {
    if (!pthread_equal(L->owner, pthread_self())) ALooper_wake((ALooper *)L);
}

static pthread_key_t g_looperKey;
static pthread_once_t g_looperOnce = PTHREAD_ONCE_INIT;

static void looperFree(ApakLooper *L) {
    FrameReq *f = L->frames;
    while (f) {
        FrameReq *next = f->next;
        free(f);
        f = next;
    }
    if (L->wakeRead >= 0) close(L->wakeRead);
    if (L->wakeWrite >= 0) close(L->wakeWrite);
    free(L->reqs);
    free(L->resps);
    pthread_mutex_destroy(&L->lock);
    free(L);
}

ANDROIDRT_EXPORT void ALooper_release(ALooper *looper) {
    ApakLooper *L = (ApakLooper *)looper;
    int left;
    if (!L) return;
    pthread_mutex_lock(&L->lock);
    left = --L->refs;
    pthread_mutex_unlock(&L->lock);
    if (left == 0) looperFree(L);
}

static void looperKeyDtor(void *p) { ALooper_release((ALooper *)p); }
static void looperKeyInit(void) { pthread_key_create(&g_looperKey, looperKeyDtor); }

static ApakLooper *looperNew(int allowNonCallbacks) {
    ApakLooper *L = (ApakLooper *)calloc(1, sizeof *L);
    int fds[2];
    if (!L) return NULL;
    pthread_mutex_init(&L->lock, NULL);
    L->refs = 1;
    L->allowNonCallbacks = allowNonCallbacks;
    L->wakeRead = L->wakeWrite = -1;
    if (pipe2(fds, O_NONBLOCK | O_CLOEXEC) != 0) {
        fprintf(stderr, "[androidrt] looper: cannot create wake pipe: %s\n", strerror(errno));
        looperFree(L);
        return NULL;
    }
    L->wakeRead = fds[0];
    L->wakeWrite = fds[1];
    return L;
}

ANDROIDRT_EXPORT ALooper *ALooper_forThread(void) {
    pthread_once(&g_looperOnce, looperKeyInit);
    return (ALooper *)pthread_getspecific(g_looperKey);
}

ANDROIDRT_EXPORT ALooper *ALooper_prepare(int opts) {
    ApakLooper *L = (ApakLooper *)ALooper_forThread();
    if (L) return (ALooper *)L;
    L = looperNew((opts & ALOOPER_PREPARE_ALLOW_NON_CALLBACKS) != 0);
    if (L) {
        L->owner = pthread_self();
        pthread_setspecific(g_looperKey, L); /* the thread holds the first reference */
    }
    return (ALooper *)L;
}

ANDROIDRT_EXPORT void ALooper_acquire(ALooper *looper) {
    ApakLooper *L = (ApakLooper *)looper;
    if (!L) return;
    pthread_mutex_lock(&L->lock);
    L->refs++;
    pthread_mutex_unlock(&L->lock);
}

ANDROIDRT_EXPORT void ALooper_wake(ALooper *looper) {
    ApakLooper *L = (ApakLooper *)looper;
    ssize_t w;
    if (!L) return;
    w = write(L->wakeWrite, "W", 1); /* EAGAIN = a wake is already pending */
    (void)w;
}

ANDROIDRT_EXPORT int ALooper_addFd(ALooper *looper, int fd, int ident, int events,
                                   ALooper_callbackFunc callback, void *data) {
    ApakLooper *L = (ApakLooper *)looper;
    int i;
    if (!L || fd < 0) return -1;
    if (!callback) {
        if (!L->allowNonCallbacks) {
            fprintf(stderr, "[androidrt] looper: ALooper_addFd without a callback on a looper prepared without ALOOPER_PREPARE_ALLOW_NON_CALLBACKS\n");
            return -1;
        }
        if (ident < 0) return -1;
    } else {
        ident = ALOOPER_POLL_CALLBACK;
    }
    pthread_mutex_lock(&L->lock);
    for (i = 0; i < L->nreq; i++)
        if (L->reqs[i].fd == fd) break;
    if (i == L->nreq) {
        if (L->nreq == L->capReq) {
            int cap = L->capReq ? L->capReq * 2 : 8;
            LooperReq *grown = (LooperReq *)realloc(L->reqs, (size_t)cap * sizeof *grown);
            if (!grown) {
                pthread_mutex_unlock(&L->lock);
                return -1;
            }
            L->reqs = grown;
            L->capReq = cap;
        }
        L->nreq++;
    }
    L->reqs[i].fd = fd;
    L->reqs[i].ident = ident;
    L->reqs[i].events = events;
    L->reqs[i].callback = callback;
    L->reqs[i].data = data;
    L->reqs[i].seq = ++L->seq;
    pthread_mutex_unlock(&L->lock);
    wakeIfOtherThread(L);
    return 1;
}

static int removeFdLocked(ApakLooper *L, int fd, unsigned seq) {
    int i;
    for (i = 0; i < L->nreq; i++) {
        if (L->reqs[i].fd != fd) continue;
        if (seq && L->reqs[i].seq != seq) return 0; /* re-registered meanwhile */
        memmove(&L->reqs[i], &L->reqs[i + 1], (size_t)(L->nreq - i - 1) * sizeof L->reqs[0]);
        L->nreq--;
        return 1;
    }
    return 0;
}

ANDROIDRT_EXPORT int ALooper_removeFd(ALooper *looper, int fd) {
    ApakLooper *L = (ApakLooper *)looper;
    int removed;
    if (!L) return -1;
    pthread_mutex_lock(&L->lock);
    removed = removeFdLocked(L, fd, 0);
    pthread_mutex_unlock(&L->lock);
    if (removed) wakeIfOtherThread(L);
    return removed;
}

static int64_t monoNs(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

/* When the earliest pending frame callback may run: its own due time, but
 * never sooner than one vsync after the previous frame. -1 = none pending. */
static int64_t nextFrameNsLocked(const ApakLooper *L) {
    const FrameReq *f;
    int64_t due = -1;
    for (f = L->frames; f; f = f->next)
        if (due < 0 || f->dueNs < due) due = f->dueNs;
    if (due >= 0 && L->lastVsyncNs && due < L->lastVsyncNs + APAK_VSYNC_NS) due = L->lastVsyncNs + APAK_VSYNC_NS;
    return due;
}

static int pushResp(ApakLooper *L, const LooperReq *req, int events) {
    if (L->nresp == L->capResp) {
        int cap = L->capResp ? L->capResp * 2 : 8;
        LooperResp *grown = (LooperResp *)realloc(L->resps, (size_t)cap * sizeof *grown);
        if (!grown) return -1;
        L->resps = grown;
        L->capResp = cap;
    }
    L->resps[L->nresp].events = events;
    L->resps[L->nresp].req = *req;
    L->nresp++;
    return 0;
}

static int pollInner(ApakLooper *L, int timeoutMs) {
    struct pollfd stackFds[16];
    LooperReq stackReqs[16];
    struct pollfd *fds = stackFds;
    LooperReq *reqs = stackReqs;
    int n, i, count, result = ALOOPER_POLL_WAKE;
    int64_t frameDue;

    L->nresp = 0;
    L->respIndex = 0;

    pthread_mutex_lock(&L->lock);
    count = L->nreq + 1;
    if (count > 16) {
        fds = (struct pollfd *)malloc((size_t)count * sizeof *fds);
        reqs = (LooperReq *)malloc((size_t)count * sizeof *reqs);
        if (!fds || !reqs) {
            pthread_mutex_unlock(&L->lock);
            free(fds == stackFds ? NULL : fds);
            free(reqs == stackReqs ? NULL : reqs);
            return ALOOPER_POLL_ERROR;
        }
    }
    fds[0].fd = L->wakeRead;
    fds[0].events = POLLIN;
    fds[0].revents = 0;
    for (i = 0; i < L->nreq; i++) {
        reqs[i + 1] = L->reqs[i];
        fds[i + 1].fd = L->reqs[i].fd;
        fds[i + 1].events = (short)(((L->reqs[i].events & ALOOPER_EVENT_INPUT) ? POLLIN : 0) |
                                    ((L->reqs[i].events & ALOOPER_EVENT_OUTPUT) ? POLLOUT : 0));
        fds[i + 1].revents = 0;
    }
    frameDue = nextFrameNsLocked(L);
    pthread_mutex_unlock(&L->lock);

    if (frameDue >= 0) {
        int64_t waitNs = frameDue - monoNs();
        int waitMs = waitNs <= 0 ? 0 : (int)((waitNs + 999999) / 1000000);
        if (timeoutMs < 0 || waitMs < timeoutMs) timeoutMs = waitMs;
    }

    n = poll(fds, (nfds_t)count, timeoutMs);
    if (n < 0) {
        result = errno == EINTR ? ALOOPER_POLL_WAKE : ALOOPER_POLL_ERROR;
    } else if (n == 0) {
        result = ALOOPER_POLL_TIMEOUT;
    } else {
        if (fds[0].revents) {
            char buf[64];
            while (read(L->wakeRead, buf, sizeof buf) > 0) {
            }
        }
        for (i = 1; i < count; i++) {
            int ev = 0;
            short r = fds[i].revents;
            if (!r) continue;
            if (r & POLLIN) ev |= ALOOPER_EVENT_INPUT;
            if (r & POLLOUT) ev |= ALOOPER_EVENT_OUTPUT;
            if (r & POLLERR) ev |= ALOOPER_EVENT_ERROR;
            if (r & POLLHUP) ev |= ALOOPER_EVENT_HANGUP;
            if (r & POLLNVAL) ev |= ALOOPER_EVENT_INVALID;
            pushResp(L, &reqs[i], ev);
        }
    }
    if (fds != stackFds) free(fds);
    if (reqs != stackReqs) free(reqs);

    /* Callbacks run inside the poll, exactly like Android's Looper. */
    for (i = 0; i < L->nresp; i++) {
        LooperResp *r = &L->resps[i];
        if (r->req.ident == ALOOPER_POLL_CALLBACK && r->req.callback) {
            int keep = r->req.callback(r->req.fd, r->events, r->req.data);
            if (!keep) {
                pthread_mutex_lock(&L->lock);
                removeFdLocked(L, r->req.fd, r->req.seq);
                pthread_mutex_unlock(&L->lock);
            }
            r->req.callback = NULL;
            result = ALOOPER_POLL_CALLBACK;
        }
    }

    /* Choreographer frames whose time has come. */
    {
        int64_t now = monoNs();
        FrameReq *due = NULL, **tail = &due, **pp;
        pthread_mutex_lock(&L->lock);
        frameDue = nextFrameNsLocked(L);
        if (frameDue >= 0 && frameDue <= now) {
            for (pp = &L->frames; *pp;) {
                FrameReq *f = *pp;
                if (f->dueNs <= now) {
                    *pp = f->next;
                    f->next = NULL;
                    *tail = f;
                    tail = &f->next;
                } else {
                    pp = &f->next;
                }
            }
            L->lastVsyncNs = now;
        }
        pthread_mutex_unlock(&L->lock);
        while (due) {
            FrameReq *next = due->next;
            if (due->cb64) due->cb64(now, due->data);
            else if (due->cb32) due->cb32((long)now, due->data);
            free(due);
            due = next;
            result = ALOOPER_POLL_CALLBACK;
        }
    }
    return result;
}

static ApakLooper *threadLooper(void) {
    ApakLooper *L = (ApakLooper *)ALooper_forThread();
    if (!L) {
        static int warned;
        if (!warned) {
            warned = 1;
            fprintf(stderr, "[androidrt] looper: ALooper_poll* on a thread without ALooper_prepare — preparing one\n");
        }
        L = (ApakLooper *)ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
    }
    return L;
}

ANDROIDRT_EXPORT int ALooper_pollOnce(int timeoutMillis, int *outFd, int *outEvents, void **outData) {
    ApakLooper *L = threadLooper();
    int result = 0;
    if (!L) return ALOOPER_POLL_ERROR;
    for (;;) {
        while (L->respIndex < L->nresp) {
            const LooperResp *r = &L->resps[L->respIndex++];
            if (r->req.ident >= 0) {
                if (outFd) *outFd = r->req.fd;
                if (outEvents) *outEvents = r->events;
                if (outData) *outData = r->req.data;
                return r->req.ident;
            }
        }
        if (result != 0) {
            if (outFd) *outFd = 0;
            if (outEvents) *outEvents = 0;
            if (outData) *outData = NULL;
            return result;
        }
        result = pollInner(L, timeoutMillis);
    }
}

ANDROIDRT_EXPORT int ALooper_pollAll(int timeoutMillis, int *outFd, int *outEvents, void **outData) {
    int result;
    if (timeoutMillis <= 0) {
        do {
            result = ALooper_pollOnce(timeoutMillis, outFd, outEvents, outData);
        } while (result == ALOOPER_POLL_CALLBACK);
        return result;
    } else {
        int64_t end = monoNs() + (int64_t)timeoutMillis * 1000000LL;
        for (;;) {
            int64_t left;
            result = ALooper_pollOnce(timeoutMillis, outFd, outEvents, outData);
            if (result != ALOOPER_POLL_CALLBACK) return result;
            left = end - monoNs();
            if (left <= 0) return ALOOPER_POLL_TIMEOUT;
            timeoutMillis = (int)((left + 999999) / 1000000);
        }
    }
}

/* ── AChoreographer (API 24+): frame callbacks on the calling thread's
 * looper, paced to the panel's 60 Hz ───────────────────────────────── */

static void postFrame(void *choreographer, FrameCb32 cb32, FrameCb64 cb64, void *data, int64_t delayMs) {
    ApakLooper *L = (ApakLooper *)choreographer;
    FrameReq *f, **pp;
    if (!L || (!cb32 && !cb64)) return;
    f = (FrameReq *)calloc(1, sizeof *f);
    if (!f) return;
    f->cb32 = cb32;
    f->cb64 = cb64;
    f->data = data;
    f->dueNs = monoNs() + (delayMs > 0 ? delayMs * 1000000LL : 0);
    pthread_mutex_lock(&L->lock);
    for (pp = &L->frames; *pp; pp = &(*pp)->next) {
    }
    *pp = f;
    pthread_mutex_unlock(&L->lock);
    wakeIfOtherThread(L);
}

ANDROIDRT_EXPORT void *AChoreographer_getInstance(void) { return ALooper_forThread(); }

ANDROIDRT_EXPORT void AChoreographer_postFrameCallback(void *c, FrameCb32 cb, void *data) {
    postFrame(c, cb, NULL, data, 0);
}
ANDROIDRT_EXPORT void AChoreographer_postFrameCallbackDelayed(void *c, FrameCb32 cb, void *data, long delayMillis) {
    postFrame(c, cb, NULL, data, delayMillis);
}
ANDROIDRT_EXPORT void AChoreographer_postFrameCallback64(void *c, FrameCb64 cb, void *data) {
    postFrame(c, NULL, cb, data, 0);
}
ANDROIDRT_EXPORT void AChoreographer_postFrameCallbackDelayed64(void *c, FrameCb64 cb, void *data, uint32_t delayMillis) {
    postFrame(c, NULL, cb, data, (int64_t)delayMillis);
}
ANDROIDRT_EXPORT void AChoreographer_registerRefreshRateCallback(void *c, void *cb, void *data) {
    (void)c;
    (void)cb;
    (void)data;
}
ANDROIDRT_EXPORT void AChoreographer_unregisterRefreshRateCallback(void *c, void *cb, void *data) {
    (void)c;
    (void)cb;
    (void)data;
}

#else /* _WIN32: the host smoke build only needs the calls to exist */

#include <windows.h>

static int g_dummyLooper;

ANDROIDRT_EXPORT ALooper *ALooper_forThread(void) { return &g_dummyLooper; }
ANDROIDRT_EXPORT ALooper *ALooper_prepare(int opts) {
    (void)opts;
    return &g_dummyLooper;
}
ANDROIDRT_EXPORT void ALooper_acquire(ALooper *looper) { (void)looper; }
ANDROIDRT_EXPORT void ALooper_release(ALooper *looper) { (void)looper; }
ANDROIDRT_EXPORT void ALooper_wake(ALooper *looper) { (void)looper; }
ANDROIDRT_EXPORT int ALooper_addFd(ALooper *looper, int fd, int ident, int events, ALooper_callbackFunc cb, void *data) {
    (void)looper; (void)fd; (void)ident; (void)events; (void)cb; (void)data;
    return -1;
}
ANDROIDRT_EXPORT int ALooper_removeFd(ALooper *looper, int fd) {
    (void)looper; (void)fd;
    return 0;
}
ANDROIDRT_EXPORT int ALooper_pollOnce(int timeoutMillis, int *outFd, int *outEvents, void **outData) {
    if (outFd) *outFd = 0;
    if (outEvents) *outEvents = 0;
    if (outData) *outData = NULL;
    Sleep(timeoutMillis < 0 ? 16 : (DWORD)timeoutMillis);
    return ALOOPER_POLL_TIMEOUT;
}
ANDROIDRT_EXPORT int ALooper_pollAll(int timeoutMillis, int *outFd, int *outEvents, void **outData) {
    return ALooper_pollOnce(timeoutMillis, outFd, outEvents, outData);
}
ANDROIDRT_EXPORT void *AChoreographer_getInstance(void) { return NULL; }

#endif /* !_WIN32 */
