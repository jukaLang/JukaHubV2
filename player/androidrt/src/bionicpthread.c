/* bionicpthread.c — the pthread / semaphore / signal / setjmp half of the
 * bionic compatibility layer.
 *
 * LP64 mismatches this file exists for (bionic vs glibc aarch64):
 *   pthread_mutex_t      40 vs 48 bytes  -> heap glibc mutex behind a pointer
 *   pthread_cond_t       48 vs 48, but 4- vs 8-byte aligned (glibc does
 *                        64-bit atomics on it) -> heap glibc cond, pointer
 *   sem_t                16 vs 32 bytes  -> heap glibc sem behind a pointer
 *   pthread_attr_t       56 vs 64 bytes  -> bionic layout interpreted here,
 *                                            converted at pthread_create
 *   pthread_barrierattr_t 4 vs  8 bytes  -> flag kept in the bionic int
 *   sigset_t              8 vs 128 bytes -> every sigset_t* call converted
 *   struct sigaction  flags-first (32) vs handler-first (152)
 *   sigsetjmp            no such glibc symbol (a macro around __sigsetjmp)
 *                        -> own setjmp family sized for bionic's jmp_buf
 *                        (bionicjmp.S)
 * Types that match and only use 32-bit atomics (pthread_rwlock_t 56,
 * pthread_barrier_t 32, spinlocks, the 8-byte attr types, pthread_once_t,
 * keys) bind straight to glibc.
 *
 * Statically initialised bionic mutexes are all zero except bits 14-15 of
 * the first word (the type: 0 normal, 1 recursive, 2 errorcheck), so the
 * glibc mutex is created lazily on first use, race-free through a CAS on
 * the pointer slot.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <poll.h>
#include <pthread.h>
#include <semaphore.h>
#include <signal.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/select.h>
#include <sys/signalfd.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

#include "bioniccompat.h"

/* setjmp family, bionicjmp.S (bionic jmp_buf layout) */
extern int apak_setjmp(void *env);
extern int apak__setjmp(void *env);
extern int apak_sigsetjmp(void *env, int saveMask);
extern void apak_longjmp(void *env, int value);
extern void apak__longjmp(void *env, int value);
extern void apak_siglongjmp(void *env, int value);

/* ── the pointer slot inside a bionic object ───────────────────────────
 * bionic's objects are int32 arrays (4-byte aligned): the 8-byte slot sits
 * at the first 8-aligned address >= obj+4, i.e. obj+4 or obj+8 — always
 * inside the 16 bytes even sem_t has. Word 0 stays the bionic state word. */
static void **slotOf(void *obj) { return (void **)(((uintptr_t)obj + 4 + 7) & ~(uintptr_t)7); }

/* ── mutexes ─────────────────────────────────────────────────────────── */

static pthread_mutex_t *mutexReal(void *bm) {
    void **slot = slotOf(bm);
    pthread_mutex_t *m = (pthread_mutex_t *)__atomic_load_n(slot, __ATOMIC_ACQUIRE);
    pthread_mutexattr_t attr;
    void *expected = NULL;
    int type;
    if (m) return m;
    type = (int)(((uint32_t)__atomic_load_n((int32_t *)bm, __ATOMIC_RELAXED) >> 14) & 3u);
    m = (pthread_mutex_t *)malloc(sizeof *m);
    if (!m) apak_compatFatal("pthread_mutex: out of memory");
    pthread_mutexattr_init(&attr);
    pthread_mutexattr_settype(&attr, type == 1 ? PTHREAD_MUTEX_RECURSIVE
                                     : type == 2 ? PTHREAD_MUTEX_ERRORCHECK : PTHREAD_MUTEX_NORMAL);
    pthread_mutex_init(m, &attr);
    pthread_mutexattr_destroy(&attr);
    if (!__atomic_compare_exchange_n(slot, &expected, m, 0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
        pthread_mutex_destroy(m);
        free(m);
        return (pthread_mutex_t *)expected;
    }
    return m;
}

static int bw_pthread_mutex_init(void *bm, const pthread_mutexattr_t *attr) {
    pthread_mutex_t *m = (pthread_mutex_t *)malloc(sizeof *m);
    int type = PTHREAD_MUTEX_NORMAL, rc;
    int32_t state;
    if (!m) return ENOMEM;
    rc = pthread_mutex_init(m, attr);
    if (rc) {
        free(m);
        return rc;
    }
    if (attr) pthread_mutexattr_gettype(attr, &type);
    memset(bm, 0, 40);
    state = (int32_t)((type & 3) << 14);
    memcpy(bm, &state, sizeof state);
    __atomic_store_n(slotOf(bm), m, __ATOMIC_RELEASE);
    return 0;
}

static int bw_pthread_mutex_destroy(void *bm) {
    void **slot = slotOf(bm);
    pthread_mutex_t *m = (pthread_mutex_t *)__atomic_exchange_n(slot, NULL, __ATOMIC_ACQ_REL);
    int rc;
    if (!m) return 0;
    rc = pthread_mutex_destroy(m);
    if (rc == EBUSY) {
        __atomic_store_n(slot, m, __ATOMIC_RELEASE);
        return rc;
    }
    free(m);
    return 0;
}

static int bw_pthread_mutex_lock(void *bm) { return pthread_mutex_lock(mutexReal(bm)); }
static int bw_pthread_mutex_unlock(void *bm) { return pthread_mutex_unlock(mutexReal(bm)); }
static int bw_pthread_mutex_trylock(void *bm) { return pthread_mutex_trylock(mutexReal(bm)); }
static int bw_pthread_mutex_timedlock(void *bm, const struct timespec *ts) {
    return pthread_mutex_timedlock(mutexReal(bm), ts);
}

/* Convert an absolute deadline on `clock` into one on CLOCK_REALTIME. */
static struct timespec toRealtime(clockid_t clock, const struct timespec *abs) {
    struct timespec now, real, out;
    long long ns;
    if (clock == CLOCK_REALTIME) return *abs;
    clock_gettime(clock, &now);
    clock_gettime(CLOCK_REALTIME, &real);
    ns = (long long)(abs->tv_sec - now.tv_sec) * 1000000000LL + (abs->tv_nsec - now.tv_nsec);
    if (ns < 0) ns = 0;
    ns += (long long)real.tv_sec * 1000000000LL + real.tv_nsec;
    out.tv_sec = (time_t)(ns / 1000000000LL);
    out.tv_nsec = (long)(ns % 1000000000LL);
    return out;
}
static struct timespec relToRealtime(const struct timespec *rel) {
    struct timespec now, out;
    long long ns;
    clock_gettime(CLOCK_REALTIME, &now);
    ns = (long long)now.tv_sec * 1000000000LL + now.tv_nsec;
    if (rel) ns += (long long)rel->tv_sec * 1000000000LL + rel->tv_nsec;
    out.tv_sec = (time_t)(ns / 1000000000LL);
    out.tv_nsec = (long)(ns % 1000000000LL);
    return out;
}

static int bw_pthread_mutex_timedlock_monotonic_np(void *bm, const struct timespec *ts) {
    struct timespec real = toRealtime(CLOCK_MONOTONIC, ts);
    return pthread_mutex_timedlock(mutexReal(bm), &real);
}
static int bw_pthread_mutex_clocklock(void *bm, clockid_t clock, const struct timespec *ts) {
    struct timespec real = toRealtime(clock, ts);
    return pthread_mutex_timedlock(mutexReal(bm), &real);
}
static int bw_pthread_mutex_lock_timeout_np(void *bm, unsigned ms) {
    struct timespec rel = { (time_t)(ms / 1000), (long)(ms % 1000) * 1000000L };
    struct timespec real = relToRealtime(&rel);
    return pthread_mutex_timedlock(mutexReal(bm), &real);
}

/* ── condition variables ─────────────────────────────────────────────
 * Same 48 bytes as glibc's, but bionic's is an int32 array (4-byte
 * aligned) while glibc's condvar does 64-bit atomics on __wseq/__g1_start:
 * a bionic cond at an address = 4 mod 8 (after an int or bool member, the
 * usual std::condition_variable spot) would take an alignment fault on the
 * A53's exclusive loads. So it gets the mutex treatment: a heap glibc cond
 * behind a pointer slot, created lazily (PTHREAD_COND_INITIALIZER is all
 * zeros). */

static pthread_cond_t *condReal(void *bc) {
    void **slot = slotOf(bc);
    pthread_cond_t *c = (pthread_cond_t *)__atomic_load_n(slot, __ATOMIC_ACQUIRE);
    void *expected = NULL;
    if (c) return c;
    c = (pthread_cond_t *)malloc(sizeof *c);
    if (!c) apak_compatFatal("pthread_cond: out of memory");
    pthread_cond_init(c, NULL);
    if (!__atomic_compare_exchange_n(slot, &expected, c, 0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
        pthread_cond_destroy(c);
        free(c);
        return (pthread_cond_t *)expected;
    }
    return c;
}

static int bw_pthread_cond_init(void *bc, const pthread_condattr_t *attr) {
    pthread_cond_t *c = (pthread_cond_t *)malloc(sizeof *c);
    int rc;
    if (!c) return ENOMEM;
    rc = pthread_cond_init(c, attr);
    if (rc) {
        free(c);
        return rc;
    }
    memset(bc, 0, 48);
    __atomic_store_n(slotOf(bc), c, __ATOMIC_RELEASE);
    return 0;
}

static int bw_pthread_cond_destroy(void *bc) {
    pthread_cond_t *c = (pthread_cond_t *)__atomic_exchange_n(slotOf(bc), NULL, __ATOMIC_ACQ_REL);
    if (!c) return 0;
    pthread_cond_destroy(c);
    free(c);
    return 0;
}

static int bw_pthread_cond_signal(void *bc) { return pthread_cond_signal(condReal(bc)); }
static int bw_pthread_cond_broadcast(void *bc) { return pthread_cond_broadcast(condReal(bc)); }
static int bw_pthread_cond_wait(void *bc, void *bm) { return pthread_cond_wait(condReal(bc), mutexReal(bm)); }
static int bw_pthread_cond_timedwait(void *bc, void *bm, const struct timespec *ts) {
    return pthread_cond_timedwait(condReal(bc), mutexReal(bm), ts);
}

/* glibc >= 2.30 has pthread_cond_clockwait; resolve it at run time so the
 * runner still starts on an older device glibc (then: convert the deadline
 * and assume the cond's own clock is the default CLOCK_REALTIME). */
typedef int (*CondClockwait)(pthread_cond_t *, pthread_mutex_t *, clockid_t, const struct timespec *);
static CondClockwait condClockwait(void) {
    static CondClockwait fn;
    static int looked;
    if (!looked) {
        fn = (CondClockwait)dlsym(RTLD_DEFAULT, "pthread_cond_clockwait");
        looked = 1;
    }
    return fn;
}

static int bw_pthread_cond_clockwait(void *bc, void *bm, clockid_t clock, const struct timespec *ts) {
    CondClockwait fn = condClockwait();
    struct timespec real;
    if (fn) return fn(condReal(bc), mutexReal(bm), clock, ts);
    real = toRealtime(clock, ts);
    return pthread_cond_timedwait(condReal(bc), mutexReal(bm), &real);
}
static int bw_pthread_cond_timedwait_monotonic_np(void *c, void *bm, const struct timespec *ts) {
    return bw_pthread_cond_clockwait(c, bm, CLOCK_MONOTONIC, ts);
}
static int bw_pthread_cond_timedwait_relative_np(void *c, void *bm, const struct timespec *rel) {
    struct timespec real = relToRealtime(rel);
    return bw_pthread_cond_clockwait(c, bm, CLOCK_REALTIME, &real);
}
static int bw_pthread_cond_timeout_np(void *c, void *bm, unsigned ms) {
    struct timespec rel = { (time_t)(ms / 1000), (long)(ms % 1000) * 1000000L };
    return bw_pthread_cond_timedwait_relative_np(c, bm, &rel);
}

/* ── pthread_attr_t: bionic's own layout (56 bytes LP64) ───────────── */

typedef struct {
    uint32_t flags;
    void *stackBase;
    size_t stackSize;
    size_t guardSize;
    int32_t schedPolicy;
    int32_t schedPriority;
    char reserved[16];
} BAttr;

#define BATTR_DETACHED   0x1u
#define BATTR_USER_STACK 0x2u
#define BATTR_INHERIT    0x4u
#define BATTR_EXPLICIT   0x8u
#define BSTACK_DEFAULT   (1024u * 1024u)
#define BSTACK_MIN       16384u

static int bw_pthread_attr_init(BAttr *a) {
    memset(a, 0, sizeof *a);
    a->stackSize = BSTACK_DEFAULT;
    a->guardSize = (size_t)sysconf(_SC_PAGESIZE);
    a->schedPolicy = SCHED_OTHER;
    return 0;
}
static int bw_pthread_attr_destroy(BAttr *a) {
    memset(a, 0x42, sizeof *a);
    return 0;
}
static int bw_pthread_attr_setdetachstate(BAttr *a, int state) {
    if (state == PTHREAD_CREATE_DETACHED) a->flags |= BATTR_DETACHED;
    else if (state == PTHREAD_CREATE_JOINABLE) a->flags &= ~BATTR_DETACHED;
    else return EINVAL;
    return 0;
}
static int bw_pthread_attr_getdetachstate(const BAttr *a, int *state) {
    *state = (a->flags & BATTR_DETACHED) ? PTHREAD_CREATE_DETACHED : PTHREAD_CREATE_JOINABLE;
    return 0;
}
static int bw_pthread_attr_setschedpolicy(BAttr *a, int policy) {
    a->schedPolicy = policy;
    return 0;
}
static int bw_pthread_attr_getschedpolicy(const BAttr *a, int *policy) {
    *policy = a->schedPolicy;
    return 0;
}
static int bw_pthread_attr_setschedparam(BAttr *a, const struct sched_param *p) {
    a->schedPriority = p->sched_priority;
    return 0;
}
static int bw_pthread_attr_getschedparam(const BAttr *a, struct sched_param *p) {
    p->sched_priority = a->schedPriority;
    return 0;
}
static int bw_pthread_attr_setinheritsched(BAttr *a, int inherit) {
    a->flags &= ~(BATTR_INHERIT | BATTR_EXPLICIT);
    if (inherit == PTHREAD_INHERIT_SCHED) a->flags |= BATTR_INHERIT;
    else if (inherit == PTHREAD_EXPLICIT_SCHED) a->flags |= BATTR_EXPLICIT;
    else return EINVAL;
    return 0;
}
static int bw_pthread_attr_getinheritsched(const BAttr *a, int *inherit) {
    *inherit = (a->flags & BATTR_EXPLICIT) ? PTHREAD_EXPLICIT_SCHED : PTHREAD_INHERIT_SCHED;
    return 0;
}
static int bw_pthread_attr_setstacksize(BAttr *a, size_t size) {
    if (size < BSTACK_MIN) return EINVAL;
    a->stackSize = size;
    return 0;
}
static int bw_pthread_attr_getstacksize(const BAttr *a, size_t *size) {
    *size = a->stackSize;
    return 0;
}
static int bw_pthread_attr_setstack(BAttr *a, void *base, size_t size) {
    if (size < BSTACK_MIN || ((uintptr_t)base & 15) != 0) return EINVAL;
    a->stackBase = base;
    a->stackSize = size;
    a->flags |= BATTR_USER_STACK;
    return 0;
}
static int bw_pthread_attr_getstack(const BAttr *a, void **base, size_t *size) {
    *base = a->stackBase;
    *size = a->stackSize;
    return 0;
}
static int bw_pthread_attr_setguardsize(BAttr *a, size_t size) {
    a->guardSize = size;
    return 0;
}
static int bw_pthread_attr_getguardsize(const BAttr *a, size_t *size) {
    *size = a->guardSize;
    return 0;
}
static int bw_pthread_attr_setscope(BAttr *a, int scope) {
    (void)a;
    if (scope == PTHREAD_SCOPE_SYSTEM) return 0;
    return scope == PTHREAD_SCOPE_PROCESS ? ENOTSUP : EINVAL;
}
static int bw_pthread_attr_getscope(const BAttr *a, int *scope) {
    (void)a;
    *scope = PTHREAD_SCOPE_SYSTEM;
    return 0;
}

static int bw_pthread_getattr_np(pthread_t t, BAttr *out) {
    pthread_attr_t ga;
    int rc = pthread_getattr_np(t, &ga), state = 0, policy = 0, inherit = 0;
    struct sched_param param;
    void *base = NULL;
    size_t size = 0, guard = 0;
    if (rc) return rc;
    bw_pthread_attr_init(out);
    pthread_attr_getdetachstate(&ga, &state);
    pthread_attr_getstack(&ga, &base, &size);
    pthread_attr_getguardsize(&ga, &guard);
    pthread_attr_getschedpolicy(&ga, &policy);
    memset(&param, 0, sizeof param);
    pthread_attr_getschedparam(&ga, &param);
    pthread_attr_getinheritsched(&ga, &inherit);
    pthread_attr_destroy(&ga);
    bw_pthread_attr_setdetachstate(out, state);
    out->stackBase = base;
    out->stackSize = size;
    out->guardSize = guard;
    out->schedPolicy = policy;
    out->schedPriority = param.sched_priority;
    bw_pthread_attr_setinheritsched(out, inherit);
    return 0;
}

/* ── threads: create/exit/tid registry/cleanup handlers ─────────────── */

typedef struct {
    void *(*fn)(void *);
    void *arg;
} StartInfo;

#define APAK_TIDS 512
static struct {
    pthread_t thread;
    pid_t tid;
} g_tids[APAK_TIDS];
static pthread_mutex_t g_tidLock = PTHREAD_MUTEX_INITIALIZER;
static pthread_key_t g_tidKey;
static pthread_once_t g_tidOnce = PTHREAD_ONCE_INIT;

static void tidForget(void *unused) {
    pthread_t self = pthread_self();
    int i;
    (void)unused;
    pthread_mutex_lock(&g_tidLock);
    for (i = 0; i < APAK_TIDS; i++)
        if (g_tids[i].tid && pthread_equal(g_tids[i].thread, self)) g_tids[i].tid = 0;
    pthread_mutex_unlock(&g_tidLock);
}
static void tidKeyInit(void) { pthread_key_create(&g_tidKey, tidForget); }
static void tidRemember(void) {
    pid_t tid = (pid_t)syscall(SYS_gettid);
    int i;
    pthread_once(&g_tidOnce, tidKeyInit);
    pthread_mutex_lock(&g_tidLock);
    for (i = 0; i < APAK_TIDS; i++) {
        if (!g_tids[i].tid) {
            g_tids[i].thread = pthread_self();
            g_tids[i].tid = tid;
            break;
        }
    }
    pthread_mutex_unlock(&g_tidLock);
    pthread_setspecific(g_tidKey, (void *)1);
}

static void *threadStart(void *p) {
    StartInfo s = *(StartInfo *)p;
    free(p);
    tidRemember();
    return s.fn(s.arg);
}

static int bw_pthread_create(pthread_t *t, const BAttr *ba, void *(*fn)(void *), void *arg) {
    pthread_attr_t ga;
    StartInfo *s;
    int rc;
    s = (StartInfo *)malloc(sizeof *s);
    if (!s) return EAGAIN;
    s->fn = fn;
    s->arg = arg;
    pthread_attr_init(&ga);
    if (ba) {
        pthread_attr_setdetachstate(&ga, (ba->flags & BATTR_DETACHED) ? PTHREAD_CREATE_DETACHED
                                                                         : PTHREAD_CREATE_JOINABLE);
        if (ba->flags & BATTR_USER_STACK) {
            pthread_attr_setstack(&ga, ba->stackBase, ba->stackSize);
        } else {
            size_t size = ba->stackSize;
            if (size < (size_t)PTHREAD_STACK_MIN) size = (size_t)PTHREAD_STACK_MIN;
            pthread_attr_setstacksize(&ga, size);
            pthread_attr_setguardsize(&ga, ba->guardSize);
        }
        if (ba->flags & BATTR_EXPLICIT) {
            struct sched_param param;
            param.sched_priority = ba->schedPriority;
            pthread_attr_setinheritsched(&ga, PTHREAD_EXPLICIT_SCHED);
            pthread_attr_setschedpolicy(&ga, ba->schedPolicy);
            pthread_attr_setschedparam(&ga, &param);
        }
    }
    rc = pthread_create(t, &ga, threadStart, s);
    pthread_attr_destroy(&ga);
    if (rc) free(s);
    return rc;
}

static pid_t bw_pthread_gettid_np(pthread_t t) {
    int i;
    pid_t tid = -1;
    if (pthread_equal(t, pthread_self())) return (pid_t)syscall(SYS_gettid);
    pthread_mutex_lock(&g_tidLock);
    for (i = 0; i < APAK_TIDS; i++)
        if (g_tids[i].tid && pthread_equal(g_tids[i].thread, t)) tid = g_tids[i].tid;
    pthread_mutex_unlock(&g_tidLock);
    return tid;
}

/* bionic's pthread_cleanup_push/pop macros call these with a frame on the
 * caller's stack; the chain head lives in apak_tls. */
typedef struct BCleanup {
    struct BCleanup *prev;
    void (*routine)(void *);
    void *arg;
} BCleanup;

static void bw___pthread_cleanup_push(BCleanup *c, void (*routine)(void *), void *arg) {
    c->prev = (BCleanup *)apak_tls.cleanupHead;
    c->routine = routine;
    c->arg = arg;
    apak_tls.cleanupHead = c;
}
static void bw___pthread_cleanup_pop(BCleanup *c, int execute) {
    apak_tls.cleanupHead = c->prev;
    if (execute && c->routine) c->routine(c->arg);
}
static void bw_pthread_exit(void *ret) {
    while (apak_tls.cleanupHead) {
        BCleanup *c = (BCleanup *)apak_tls.cleanupHead;
        apak_tls.cleanupHead = c->prev;
        if (c->routine) c->routine(c->arg);
    }
    pthread_exit(ret);
}

/* ── barrierattr: bionic int (4 bytes) vs glibc 8 ───────────────────── */
static int bw_pthread_barrierattr_init(int32_t *a) {
    *a = 0;
    return 0;
}
static int bw_pthread_barrierattr_destroy(int32_t *a) {
    (void)a;
    return 0;
}
static int bw_pthread_barrierattr_setpshared(int32_t *a, int pshared) {
    if (pshared != PTHREAD_PROCESS_PRIVATE && pshared != PTHREAD_PROCESS_SHARED) return EINVAL;
    *a = pshared;
    return 0;
}
static int bw_pthread_barrierattr_getpshared(const int32_t *a, int *pshared) {
    *pshared = *a;
    return 0;
}
static int bw_pthread_barrier_init(pthread_barrier_t *b, const int32_t *ba, unsigned count) {
    pthread_barrierattr_t ga;
    int rc;
    if (!ba) return pthread_barrier_init(b, NULL, count);
    pthread_barrierattr_init(&ga);
    pthread_barrierattr_setpshared(&ga, *ba);
    rc = pthread_barrier_init(b, &ga, count);
    pthread_barrierattr_destroy(&ga);
    return rc;
}

/* ── semaphores: bionic sem_t is 16 bytes ───────────────────────────── */

static sem_t *semReal(void *bs) {
    sem_t *s = (sem_t *)__atomic_load_n(slotOf(bs), __ATOMIC_ACQUIRE);
    if (!s) apak_compatFatal("sem_*: semaphore %p used before sem_init", bs);
    return s;
}
static int bw_sem_init(void *bs, int pshared, unsigned value) {
    sem_t *s = (sem_t *)malloc(sizeof *s);
    if (!s) {
        errno = ENOSPC;
        return -1;
    }
    if (sem_init(s, pshared, value) != 0) {
        free(s);
        return -1;
    }
    memset(bs, 0, 16);
    __atomic_store_n(slotOf(bs), s, __ATOMIC_RELEASE);
    return 0;
}
static int bw_sem_destroy(void *bs) {
    sem_t *s = (sem_t *)__atomic_exchange_n(slotOf(bs), NULL, __ATOMIC_ACQ_REL);
    if (!s) return 0;
    sem_destroy(s);
    free(s);
    return 0;
}
static int bw_sem_wait(void *bs) { return sem_wait(semReal(bs)); }
static int bw_sem_trywait(void *bs) { return sem_trywait(semReal(bs)); }
static int bw_sem_post(void *bs) { return sem_post(semReal(bs)); }
static int bw_sem_getvalue(void *bs, int *v) { return sem_getvalue(semReal(bs), v); }
static int bw_sem_timedwait(void *bs, const struct timespec *ts) { return sem_timedwait(semReal(bs), ts); }
static int bw_sem_clockwait(void *bs, clockid_t clock, const struct timespec *ts) {
    struct timespec real = toRealtime(clock, ts);
    return sem_timedwait(semReal(bs), &real);
}
static int bw_sem_timedwait_monotonic_np(void *bs, const struct timespec *ts) {
    return bw_sem_clockwait(bs, CLOCK_MONOTONIC, ts);
}
/* named semaphores: wrap glibc's sem_t* in a bionic-sized handle */
static void *bw_sem_open(const char *name, int oflag, ...) {
    sem_t *s;
    void *handle;
    if (oflag & O_CREAT) {
        va_list ap;
        mode_t mode;
        unsigned value;
        va_start(ap, oflag);
        mode = (mode_t)va_arg(ap, unsigned);
        value = va_arg(ap, unsigned);
        va_end(ap);
        s = sem_open(name, oflag, mode, value);
    } else {
        s = sem_open(name, oflag);
    }
    if (s == SEM_FAILED) return SEM_FAILED;
    handle = calloc(1, 16);
    if (!handle) {
        sem_close(s);
        errno = ENOMEM;
        return SEM_FAILED;
    }
    __atomic_store_n(slotOf(handle), s, __ATOMIC_RELEASE);
    return handle;
}
static int bw_sem_close(void *bs) {
    sem_t *s = (sem_t *)__atomic_exchange_n(slotOf(bs), NULL, __ATOMIC_ACQ_REL);
    int rc = s ? sem_close(s) : 0;
    free(bs);
    return rc;
}

/* ── signals ─────────────────────────────────────────────────────────── */

void apak_bsetToHost(const ApakBSigset *in, void *hostSigset) {
    sigset_t *h = (sigset_t *)hostSigset;
    int sig;
    sigemptyset(h);
    if (!in) return;
    for (sig = 1; sig <= 64; sig++)
        if ((*in >> (sig - 1)) & 1UL) sigaddset(h, sig); /* glibc refuses its own 32/33: fine */
}

ApakBSigset apak_hostToBset(const void *hostSigset) {
    const sigset_t *h = (const sigset_t *)hostSigset;
    ApakBSigset out = 0;
    int sig;
    for (sig = 1; sig <= 64; sig++)
        if (sigismember(h, sig) == 1) out |= 1UL << (sig - 1);
    return out;
}

typedef struct {
    int saFlags;
    void *handler;           /* sa_handler / sa_sigaction union          */
    ApakBSigset saMask;
    void (*saRestorer)(void);
} BSigaction;

static int bw_sigaction(int sig, const BSigaction *act, BSigaction *old) {
    struct sigaction h, ho;
    int rc;
    memset(&h, 0, sizeof h);
    memset(&ho, 0, sizeof ho);
    if (act) {
        h.sa_flags = act->saFlags & ~0x04000000; /* SA_RESTORER: the C library's own business */
        h.sa_sigaction = (void (*)(int, siginfo_t *, void *))act->handler;
        apak_bsetToHost(&act->saMask, &h.sa_mask);
    }
    rc = sigaction(sig, act ? &h : NULL, old ? &ho : NULL);
    if (rc == 0 && old) {
        memset(old, 0, sizeof *old);
        old->saFlags = ho.sa_flags & ~0x04000000;
        old->handler = (void *)ho.sa_sigaction;
        old->saMask = apak_hostToBset(&ho.sa_mask);
    }
    return rc;
}

static int bw_sigemptyset(ApakBSigset *s) {
    if (!s) {
        errno = EINVAL;
        return -1;
    }
    *s = 0;
    return 0;
}
static int bw_sigfillset(ApakBSigset *s) {
    if (!s) {
        errno = EINVAL;
        return -1;
    }
    *s = ~0UL;
    return 0;
}
static int bw_sigaddset(ApakBSigset *s, int sig) {
    if (!s || sig < 1 || sig > 64) {
        errno = EINVAL;
        return -1;
    }
    *s |= 1UL << (sig - 1);
    return 0;
}
static int bw_sigdelset(ApakBSigset *s, int sig) {
    if (!s || sig < 1 || sig > 64) {
        errno = EINVAL;
        return -1;
    }
    *s &= ~(1UL << (sig - 1));
    return 0;
}
static int bw_sigismember(const ApakBSigset *s, int sig) {
    if (!s || sig < 1 || sig > 64) {
        errno = EINVAL;
        return -1;
    }
    return (int)((*s >> (sig - 1)) & 1UL);
}

static int bw_sigprocmask(int how, const ApakBSigset *set, ApakBSigset *old) {
    sigset_t h, ho;
    int rc;
    if (set) apak_bsetToHost(set, &h);
    rc = sigprocmask(how, set ? &h : NULL, old ? &ho : NULL);
    if (rc == 0 && old) *old = apak_hostToBset(&ho);
    return rc;
}
static int bw_pthread_sigmask(int how, const ApakBSigset *set, ApakBSigset *old) {
    sigset_t h, ho;
    int rc;
    if (set) apak_bsetToHost(set, &h);
    rc = pthread_sigmask(how, set ? &h : NULL, old ? &ho : NULL);
    if (rc == 0 && old) *old = apak_hostToBset(&ho);
    return rc;
}
static int bw_sigsuspend(const ApakBSigset *set) {
    sigset_t h;
    apak_bsetToHost(set, &h);
    return sigsuspend(&h);
}
static int bw_sigpending(ApakBSigset *set) {
    sigset_t h;
    int rc = sigpending(&h);
    if (rc == 0) *set = apak_hostToBset(&h);
    return rc;
}
static int bw_sigwait(const ApakBSigset *set, int *sig) {
    sigset_t h;
    apak_bsetToHost(set, &h);
    return sigwait(&h, sig);
}
static int bw_sigwaitinfo(const ApakBSigset *set, siginfo_t *info) {
    sigset_t h;
    apak_bsetToHost(set, &h);
    return sigwaitinfo(&h, info);
}
static int bw_sigtimedwait(const ApakBSigset *set, siginfo_t *info, const struct timespec *ts) {
    sigset_t h;
    apak_bsetToHost(set, &h);
    return sigtimedwait(&h, info, ts);
}
static int bw_signalfd(int fd, const ApakBSigset *set, int flags) {
    sigset_t h;
    apak_bsetToHost(set, &h);
    return signalfd(fd, &h, flags);
}
static int bw_ppoll(struct pollfd *fds, nfds_t n, const struct timespec *ts, const ApakBSigset *set) {
    sigset_t h;
    if (set) apak_bsetToHost(set, &h);
    return ppoll(fds, n, ts, set ? &h : NULL);
}
static int bw___ppoll_chk(struct pollfd *fds, nfds_t n, const struct timespec *ts, const ApakBSigset *set,
                          size_t fdsSize) {
    if (fdsSize / sizeof(struct pollfd) < n) apak_compatFatal("ppoll: pollfd array smaller than fd count");
    return bw_ppoll(fds, n, ts, set);
}
static int bw_pselect(int n, fd_set *r, fd_set *w, fd_set *e, const struct timespec *ts, const ApakBSigset *set) {
    sigset_t h;
    if (set) apak_bsetToHost(set, &h);
    return pselect(n, r, w, e, ts, set ? &h : NULL);
}
static int bw_epoll_pwait(int epfd, struct epoll_event *ev, int max, int timeout, const ApakBSigset *set) {
    sigset_t h;
    if (set) apak_bsetToHost(set, &h);
    return epoll_pwait(epfd, ev, max, timeout, set ? &h : NULL);
}

/* ── tables ──────────────────────────────────────────────────────────── */

#define SYM(name, fn) { name, (void *)(fn) }

const ApakSym apak_pthreadOverrides[] = {
    SYM("pthread_mutex_init", bw_pthread_mutex_init), SYM("pthread_mutex_destroy", bw_pthread_mutex_destroy),
    SYM("pthread_mutex_lock", bw_pthread_mutex_lock), SYM("pthread_mutex_unlock", bw_pthread_mutex_unlock),
    SYM("pthread_mutex_trylock", bw_pthread_mutex_trylock),
    SYM("pthread_mutex_timedlock", bw_pthread_mutex_timedlock),
    SYM("pthread_mutex_timedlock_monotonic_np", bw_pthread_mutex_timedlock_monotonic_np),
    SYM("pthread_mutex_clocklock", bw_pthread_mutex_clocklock),
    SYM("pthread_mutex_lock_timeout_np", bw_pthread_mutex_lock_timeout_np),
    SYM("pthread_cond_init", bw_pthread_cond_init), SYM("pthread_cond_destroy", bw_pthread_cond_destroy),
    SYM("pthread_cond_signal", bw_pthread_cond_signal), SYM("pthread_cond_broadcast", bw_pthread_cond_broadcast),
    SYM("pthread_cond_wait", bw_pthread_cond_wait), SYM("pthread_cond_timedwait", bw_pthread_cond_timedwait),
    SYM("pthread_cond_clockwait", bw_pthread_cond_clockwait),
    SYM("pthread_cond_timedwait_monotonic_np", bw_pthread_cond_timedwait_monotonic_np),
    SYM("pthread_cond_timedwait_monotonic", bw_pthread_cond_timedwait_monotonic_np),
    SYM("pthread_cond_timedwait_relative_np", bw_pthread_cond_timedwait_relative_np),
    SYM("pthread_cond_timeout_np", bw_pthread_cond_timeout_np),
    SYM("pthread_attr_init", bw_pthread_attr_init), SYM("pthread_attr_destroy", bw_pthread_attr_destroy),
    SYM("pthread_attr_setdetachstate", bw_pthread_attr_setdetachstate),
    SYM("pthread_attr_getdetachstate", bw_pthread_attr_getdetachstate),
    SYM("pthread_attr_setschedpolicy", bw_pthread_attr_setschedpolicy),
    SYM("pthread_attr_getschedpolicy", bw_pthread_attr_getschedpolicy),
    SYM("pthread_attr_setschedparam", bw_pthread_attr_setschedparam),
    SYM("pthread_attr_getschedparam", bw_pthread_attr_getschedparam),
    SYM("pthread_attr_setinheritsched", bw_pthread_attr_setinheritsched),
    SYM("pthread_attr_getinheritsched", bw_pthread_attr_getinheritsched),
    SYM("pthread_attr_setstacksize", bw_pthread_attr_setstacksize),
    SYM("pthread_attr_getstacksize", bw_pthread_attr_getstacksize),
    SYM("pthread_attr_setstack", bw_pthread_attr_setstack), SYM("pthread_attr_getstack", bw_pthread_attr_getstack),
    SYM("pthread_attr_setguardsize", bw_pthread_attr_setguardsize),
    SYM("pthread_attr_getguardsize", bw_pthread_attr_getguardsize),
    SYM("pthread_attr_setscope", bw_pthread_attr_setscope), SYM("pthread_attr_getscope", bw_pthread_attr_getscope),
    SYM("pthread_getattr_np", bw_pthread_getattr_np), SYM("pthread_create", bw_pthread_create),
    SYM("pthread_exit", bw_pthread_exit),
    SYM("__pthread_cleanup_push", bw___pthread_cleanup_push), SYM("__pthread_cleanup_pop", bw___pthread_cleanup_pop),
    SYM("pthread_barrierattr_init", bw_pthread_barrierattr_init),
    SYM("pthread_barrierattr_destroy", bw_pthread_barrierattr_destroy),
    SYM("pthread_barrierattr_setpshared", bw_pthread_barrierattr_setpshared),
    SYM("pthread_barrierattr_getpshared", bw_pthread_barrierattr_getpshared),
    SYM("pthread_barrier_init", bw_pthread_barrier_init),
    SYM("sem_init", bw_sem_init), SYM("sem_destroy", bw_sem_destroy), SYM("sem_wait", bw_sem_wait),
    SYM("sem_trywait", bw_sem_trywait), SYM("sem_post", bw_sem_post), SYM("sem_getvalue", bw_sem_getvalue),
    SYM("sem_timedwait", bw_sem_timedwait), SYM("sem_clockwait", bw_sem_clockwait),
    SYM("sem_timedwait_monotonic_np", bw_sem_timedwait_monotonic_np), SYM("sem_open", bw_sem_open),
    SYM("sem_close", bw_sem_close),
    /* signals (sigset_t is 8 bytes on LP64 bionic; the *64 variants are
     * the same type there) */
    SYM("sigaction", bw_sigaction), SYM("sigaction64", bw_sigaction),
    SYM("sigemptyset", bw_sigemptyset), SYM("sigemptyset64", bw_sigemptyset),
    SYM("sigfillset", bw_sigfillset), SYM("sigfillset64", bw_sigfillset),
    SYM("sigaddset", bw_sigaddset), SYM("sigaddset64", bw_sigaddset),
    SYM("sigdelset", bw_sigdelset), SYM("sigdelset64", bw_sigdelset),
    SYM("sigismember", bw_sigismember), SYM("sigismember64", bw_sigismember),
    SYM("sigprocmask", bw_sigprocmask), SYM("sigprocmask64", bw_sigprocmask),
    SYM("pthread_sigmask", bw_pthread_sigmask), SYM("pthread_sigmask64", bw_pthread_sigmask),
    SYM("sigsuspend", bw_sigsuspend), SYM("sigsuspend64", bw_sigsuspend),
    SYM("sigpending", bw_sigpending), SYM("sigpending64", bw_sigpending),
    SYM("sigwait", bw_sigwait), SYM("sigwait64", bw_sigwait),
    SYM("sigwaitinfo", bw_sigwaitinfo), SYM("sigwaitinfo64", bw_sigwaitinfo),
    SYM("sigtimedwait", bw_sigtimedwait), SYM("sigtimedwait64", bw_sigtimedwait),
    SYM("signalfd", bw_signalfd), SYM("signalfd64", bw_signalfd),
    SYM("ppoll", bw_ppoll), SYM("ppoll64", bw_ppoll), SYM("__ppoll_chk", bw___ppoll_chk),
    SYM("__ppoll64_chk", bw___ppoll_chk), SYM("pselect", bw_pselect), SYM("pselect64", bw_pselect),
    SYM("epoll_pwait", bw_epoll_pwait), SYM("epoll_pwait64", bw_epoll_pwait),
    /* setjmp family (bionic jmp_buf) */
    SYM("setjmp", apak_setjmp), SYM("_setjmp", apak__setjmp), SYM("sigsetjmp", apak_sigsetjmp),
    SYM("longjmp", apak_longjmp), SYM("_longjmp", apak__longjmp), SYM("siglongjmp", apak_siglongjmp),
    { NULL, NULL },
};

const ApakSym apak_pthreadFallbacks[] = {
    SYM("pthread_gettid_np", bw_pthread_gettid_np),
    { NULL, NULL },
};
