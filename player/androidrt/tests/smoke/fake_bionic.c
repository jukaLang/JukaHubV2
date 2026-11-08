/* fake_bionic.c — an NDK-shaped game library for the bionic-loader smoke.
 *
 * run-smoke.sh links this against generated STUB libraries named libc.so,
 * libm.so, libdl.so, liblog.so and libandroid.so whose exports carry the
 * NDK's symbol versions (LIBC, LIBLOG, ...), exactly like the NDK's
 * platform stubs. The result has no glibc dependency at all: DT_NEEDED
 * libc.so etc. and versioned imports (memcpy@LIBC) — the shape glibc's
 * ld.so cannot bind, and the shape AndroidRT's loader must.
 *
 * No system header beyond the freestanding ones: every bionic type whose
 * layout differs from glibc's is declared here by hand (LP64), so the test
 * exercises the compat layer's ABI, not glibc's. Each check prints
 * "[bionic-test] ok|FAIL: ..." and a failure makes the library _exit(3)
 * before the lifecycle starts.
 */
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>

/* ── bionic LP64 ABI, by hand ─────────────────────────────────────────── */
#define BFILE_SIZE 152
#define CANARY 0x5ca1ab1e5ca1ab1eull

typedef struct { int32_t p[10]; } bmutex_t;               /* 40 bytes */
typedef struct { int32_t p[12]; } bcond_t;                /* 48 bytes */
typedef struct {
    uint32_t flags;
    void *stack_base;
    size_t stack_size;
    size_t guard_size;
    int32_t sched_policy;
    int32_t sched_priority;
    char reserved[16];
} battr_t;                                                /* 56 bytes */
typedef struct { unsigned int count; int reserved[3]; } bsem_t; /* 16 bytes */
typedef unsigned long bsigset_t;                          /*  8 bytes */
struct bsigaction {
    int sa_flags;
    void (*sa_handler)(int);
    bsigset_t sa_mask;
    void (*sa_restorer)(void);
};
#if defined(__x86_64__)
#define BJMP_LONGS 11 /* bionic x86_64 jmp_buf */
#else
#define BJMP_LONGS 32 /* bionic arm64 jmp_buf */
#endif
struct baddrinfo {
    int ai_flags, ai_family, ai_socktype, ai_protocol;
    uint32_t ai_addrlen;
    char *ai_canonname;
    unsigned char *ai_addr;
    struct baddrinfo *ai_next;
};
typedef struct {
    const char *dli_fname;
    void *dli_fbase;
    const char *dli_sname;
    void *dli_saddr;
} bdl_info;
struct bdl_phdr_info {
    uintptr_t dlpi_addr;
    const char *dlpi_name;
    const struct bphdr *dlpi_phdr;
    uint16_t dlpi_phnum;
};
struct bphdr {
    uint32_t p_type, p_flags;
    uint64_t p_offset, p_vaddr, p_paddr, p_filesz, p_memsz, p_align;
};
typedef unsigned long bpthread_t;

/* libc.so */
extern int *__errno(void);
extern char __sF[];
extern const char _ctype_[];
extern const short _tolower_tab_[];
extern uintptr_t __stack_chk_guard;
extern int fprintf(void *f, const char *fmt, ...);
extern int fputs(const char *s, void *f);
extern int fflush(void *f);
extern int fileno(void *f);
extern size_t fread(void *p, size_t s, size_t n, void *f);
extern int fclose(void *f);
extern void *tmpfile(void);
extern int fgetpos(void *f, long *pos);
extern void *funopen(const void *cookie, int (*rd)(void *, char *, int), int (*wr)(void *, const char *, int),
                     long (*sk)(void *, long, int), int (*cl)(void *));
extern int close(int fd);
extern long sysconf(int name);
extern int __system_property_get(const char *name, char *value);
extern const void *__system_property_find(const char *name);
extern void __system_property_read_callback(const void *pi, void (*cb)(void *, const char *, const char *, uint32_t),
                                            void *cookie);
extern int pthread_mutex_lock(bmutex_t *m);
extern int pthread_mutex_unlock(bmutex_t *m);
extern int pthread_mutex_init(bmutex_t *m, const void *attr);
extern int pthread_mutex_destroy(bmutex_t *m);
extern int pthread_cond_wait(bcond_t *c, bmutex_t *m);
extern int pthread_cond_signal(bcond_t *c);
extern int pthread_attr_init(battr_t *a);
extern int pthread_attr_setstacksize(battr_t *a, size_t size);
extern int pthread_attr_getstacksize(const battr_t *a, size_t *size);
extern int pthread_attr_destroy(battr_t *a);
extern int pthread_create(bpthread_t *t, const battr_t *a, void *(*fn)(void *), void *arg);
extern int pthread_join(bpthread_t t, void **ret);
extern int sem_init(bsem_t *s, int pshared, unsigned value);
extern int sem_post(bsem_t *s);
extern int sem_wait(bsem_t *s);
extern int sem_getvalue(bsem_t *s, int *value);
extern int sem_destroy(bsem_t *s);
extern int sigaction(int sig, const struct bsigaction *act, struct bsigaction *old);
extern int sigemptyset(bsigset_t *s);
extern int sigaddset(bsigset_t *s, int sig);
extern int sigismember(const bsigset_t *s, int sig);
extern int sigprocmask(int how, const bsigset_t *set, bsigset_t *old);
extern int raise(int sig);
extern int setjmp(long *env) __attribute__((returns_twice));
extern void longjmp(long *env, int value) __attribute__((noreturn));
extern int sigsetjmp(long *env, int save) __attribute__((returns_twice));
extern void siglongjmp(long *env, int value) __attribute__((noreturn));
extern size_t strlcpy(char *dst, const char *src, size_t size);
extern int strcmp(const char *a, const char *b);
extern size_t strlen(const char *s);
extern char *basename(const char *path);
extern char *dirname(const char *path);
extern int strerror_r(int err, char *buf, size_t len);
extern uint32_t arc4random_uniform(uint32_t upper);
extern int gettid(void);
extern int __fpclassifyd(double x);
extern int getaddrinfo(const char *node, const char *service, const struct baddrinfo *hints, struct baddrinfo **res);
extern void freeaddrinfo(struct baddrinfo *res);
extern void *getenv(const char *name);
extern int usleep(unsigned usec);
extern void _exit(int code) __attribute__((noreturn));
/* libdl.so */
extern void *dlopen(const char *name, int flags);
extern void *dlsym(void *handle, const char *name);
extern char *dlerror(void);
extern int dladdr(const void *addr, bdl_info *info);
extern int dl_iterate_phdr(int (*cb)(struct bdl_phdr_info *, size_t, void *), void *data);
/* liblog.so / libandroid.so */
extern int __android_log_print(int prio, const char *tag, const char *fmt, ...);
extern void ANativeActivity_finish(void *activity);
/* libfake_dep.so (bundled) */
extern int fake_dep_answer(void);
extern const char *fake_dep_name(int i);
extern int fake_dep_ifunc(void);
extern int fake_dep_calls_shared(void);
extern int fake_dep_counter;
/* deliberately undefined: the loader must report these, bind the function
 * to a trap, the data to zeroed scratch, and the weak one to NULL */
extern void apak_test_missing_function(void);
extern int apak_test_missing_data;
extern void apak_test_weak_missing(void) __attribute__((weak));

#define STDOUT_F ((void *)(__sF + 1 * BFILE_SIZE))
#define STDERR_F ((void *)(__sF + 2 * BFILE_SIZE))

static int g_failures;
static int g_ctorArgc = -1;
static char **g_ctorEnvp;

static void check(int ok, const char *what) {
    fprintf(STDERR_F, "[bionic-test] %s: %s\n", ok ? "ok" : "FAIL", what);
    if (!ok) g_failures++;
}

static int streq(const char *a, const char *b) { return a && b && strcmp(a, b) == 0; }

static int endsWith(const char *s, const char *suffix) {
    size_t a, b;
    if (!s) return 0;
    a = strlen(s);
    b = strlen(suffix);
    return a >= b && strcmp(s + a - b, suffix) == 0;
}

/* bionic calls DT_INIT_ARRAY entries with (argc, argv, envp) */
__attribute__((constructor)) static void fakeInit(int argc, char **argv, char **envp) {
    (void)argv;
    g_ctorArgc = argc;
    g_ctorEnvp = envp;
}

/* interposition: libfake_dep.so defines this too; the root wins (BFS) */
__attribute__((visibility("default"))) int fake_shared_name(void) { return 1; }

/* ── threads ─────────────────────────────────────────────────────────── */
static struct {
    uint64_t pre;
    bmutex_t m; /* static zero init = bionic PTHREAD_MUTEX_INITIALIZER */
    uint64_t post;
} g_lock = { CANARY, { { 0 } }, CANARY };
static int g_counter;

static void *worker(void *arg) {
    int i;
    (void)arg;
    for (i = 0; i < 20000; i++) {
        pthread_mutex_lock(&g_lock.m);
        g_counter++;
        pthread_mutex_unlock(&g_lock.m);
    }
    return (void *)(uintptr_t)gettid();
}

/* A static cond at 4 mod 8 — where std::condition_variable lands after an
 * int or bool member. glibc's condvar does 64-bit atomics, so the compat
 * layer must never hand it this address. */
static struct {
    int32_t pad;
    bcond_t cond;
    uint64_t post;
} g_cv = { 0, { { 0 } }, CANARY };
static bmutex_t g_cvLock;
static volatile int g_cvFlag;

static void *cvWaiter(void *arg) {
    (void)arg;
    pthread_mutex_lock(&g_cvLock);
    while (!g_cvFlag) pthread_cond_wait(&g_cv.cond, &g_cvLock);
    pthread_mutex_unlock(&g_cvLock);
    return (void *)1;
}

static void testThreads(void) {
    struct {
        battr_t a;
        uint64_t post;
    } attr = { { 0 }, CANARY };
    struct {
        bmutex_t m;
        uint64_t post;
    } rec = { { { 0x4000 } }, CANARY }; /* PTHREAD_RECURSIVE_MUTEX_INITIALIZER_NP */
    bpthread_t t1 = 0, t2 = 0;
    size_t stack = 0;
    void *r1 = 0, *r2 = 0;

    check(pthread_attr_init(&attr.a) == 0 && attr.post == CANARY, "pthread_attr_init stays inside bionic's 56 bytes");
    check(pthread_attr_setstacksize(&attr.a, 256 * 1024) == 0 && pthread_attr_getstacksize(&attr.a, &stack) == 0 &&
              stack == 256 * 1024 && attr.a.stack_size == 256 * 1024,
          "pthread_attr stack size lives in the bionic field");
    check(pthread_create(&t1, &attr.a, worker, 0) == 0 && pthread_create(&t2, 0, worker, 0) == 0,
          "pthread_create with a bionic attr");
    pthread_join(t1, &r1);
    pthread_join(t2, &r2);
    pthread_attr_destroy(&attr.a);
    check(g_counter == 40000, "a statically initialised 40-byte mutex serialises two threads");
    check(g_lock.pre == CANARY && g_lock.post == CANARY, "the mutex never writes past bionic's 40 bytes");
    check((uintptr_t)r1 > 0 && (uintptr_t)r2 > 0 && r1 != r2, "gettid() in threads");
    check(pthread_mutex_lock(&rec.m) == 0 && pthread_mutex_lock(&rec.m) == 0 && pthread_mutex_unlock(&rec.m) == 0 &&
              pthread_mutex_unlock(&rec.m) == 0 && rec.post == CANARY,
          "PTHREAD_RECURSIVE_MUTEX_INITIALIZER_NP (0x4000) relocks");

    {
        bpthread_t waiter = 0;
        void *ret = 0;
        check(((uintptr_t)&g_cv.cond & 7) == 4, "test cond sits at 4 mod 8");
        pthread_create(&waiter, 0, cvWaiter, 0);
        usleep(20000);
        pthread_mutex_lock(&g_cvLock);
        g_cvFlag = 1;
        pthread_cond_signal(&g_cv.cond);
        pthread_mutex_unlock(&g_cvLock);
        pthread_join(waiter, &ret);
        check(ret == (void *)1 && g_cv.post == CANARY, "a misaligned static cond waits and wakes (glibc cond behind a pointer)");
    }
}

static void testSemaphore(void) {
    struct {
        bsem_t s;
        uint64_t post;
    } sem = { { 0, { 0 } }, CANARY };
    int v = -1;
    check(sem_init(&sem.s, 0, 1) == 0 && sem_post(&sem.s) == 0 && sem_wait(&sem.s) == 0 &&
              sem_getvalue(&sem.s, &v) == 0 && v == 1 && sem.post == CANARY,
          "sem_t fits bionic's 16 bytes");
    sem_destroy(&sem.s);
}

/* ── signals + setjmp ────────────────────────────────────────────────── */
static volatile int g_sigusr1;
static void onUsr1(int sig) { g_sigusr1 = sig; }

static void testSignals(void) {
    struct {
        struct bsigaction act;
        uint64_t post;
    } act = { { 0, 0, 0, 0 }, CANARY }, old = { { 0, 0, 0, 0 }, CANARY };
    struct {
        bsigset_t set;
        uint64_t post;
    } set = { 0, CANARY };
    check(sigemptyset(&set.set) == 0 && sigaddset(&set.set, 12) == 0 && sigismember(&set.set, 12) == 1 &&
              sigismember(&set.set, 10) == 0 && set.set == (1ul << 11) && set.post == CANARY,
          "sigset_t is bionic's 8 bytes");
    act.act.sa_handler = onUsr1;
    act.act.sa_mask = 1ul << 11; /* SIGUSR2 */
    act.act.sa_flags = 0x10000000; /* SA_RESTART */
    check(sigaction(10, &act.act, 0) == 0, "sigaction with bionic's flags-first layout");
    raise(10);
    check(g_sigusr1 == 10, "the handler runs");
    check(sigaction(10, 0, &old.act) == 0 && old.act.sa_handler == onUsr1 && (old.act.sa_mask & (1ul << 11)) &&
              (old.act.sa_flags & 0x10000000) && old.post == CANARY,
          "sigaction reads back in bionic layout");
}

static void testSetjmp(void) {
    struct {
        long env[BJMP_LONGS];
        uint64_t post;
    } jb;
    volatile int pass = 0;
    int rc;
    bsigset_t blocked = 1ul << 11, cur = 0;
    jb.post = CANARY;
    rc = setjmp(jb.env);
    if (rc == 0) {
        pass = 1;
        longjmp(jb.env, 7);
    }
    check(rc == 7 && pass == 1 && jb.post == CANARY, "setjmp/longjmp inside bionic's jmp_buf");

    pass = 0;
    rc = sigsetjmp(jb.env, 1);
    if (rc == 0) {
        pass = 1;
        sigprocmask(0 /* SIG_BLOCK */, &blocked, 0);
        siglongjmp(jb.env, 0);
    }
    sigprocmask(0, 0, &cur);
    check(rc == 1 && pass == 1 && !(cur & (1ul << 11)) && jb.post == CANARY,
          "siglongjmp(env, 0) returns 1 and restores the saved mask");
}

/* ── stdio, libc odds and ends ──────────────────────────────────────── */
static int funRead(void *cookie, char *buf, int n) {
    int *calls = (int *)cookie;
    if ((*calls)++) return 0;
    if (n < 3) return -1;
    buf[0] = 'a';
    buf[1] = 'b';
    buf[2] = 'c';
    return 3;
}
static int g_funClosed;
static int funClose(void *cookie) {
    (void)cookie;
    g_funClosed = 1;
    return 0;
}

static void propCb(void *cookie, const char *name, const char *value, uint32_t serial) {
    (void)serial;
    *(int *)cookie = streq(name, "ro.build.version.sdk") && streq(value, "28");
}

static void testLibc(void) {
    char buf[96];
    int calls = 0, propOk = 0;
    void *fp;
    struct {
        long pos;
        uint64_t post;
    } pos = { -1, CANARY };
    struct baddrinfo hints = { 0x4 | 0x8 /* bionic AI_NUMERICHOST|AI_NUMERICSERV */, 2, 1, 0, 0, 0, 0, 0 }, *res = 0;

    check(close(-1) == -1 && *__errno() == 9, "__errno() is the thread's errno (EBADF)");
    check(fileno(STDOUT_F) == 1 && fileno(STDERR_F) == 2, "&__sF[n] maps to the host stdio streams");
    fputs("[bionic-test] ok: fputs through &__sF[2]\n", STDERR_F);
    check(sysconf(0x27) >= 4096 && sysconf(0x61) >= 1, "sysconf speaks bionic's _SC_PAGESIZE/_SC_NPROCESSORS_ONLN");
    check(__system_property_get("ro.build.version.sdk", buf) == 2 && streq(buf, "28"),
          "__system_property_get(ro.build.version.sdk) = 28");
    check(__system_property_get("ro.product.model", buf) > 0 && streq(buf, "Smart Pro"), "ro.product.model");
    __system_property_read_callback(__system_property_find("ro.build.version.sdk"), propCb, &propOk);
    check(propOk, "__system_property_find + read_callback");
    check((_ctype_ + 1)['A'] & 0x01 && (_ctype_ + 1)['7'] & 0x04 && !((_ctype_ + 1)['7'] & 0x01) &&
              _tolower_tab_[1 + 'Q'] == 'q',
          "bionic _ctype_/_tolower_tab_ tables");
    check(__stack_chk_guard != 0, "__stack_chk_guard is a stable non-zero canary");
    check(strlcpy(buf, "hello world", 6) == 11 && streq(buf, "hello"), "strlcpy");
    check(streq(basename("/a/b/c/"), "c") && streq(dirname("/a/b/c/"), "/a/b"), "POSIX basename/dirname");
    check(strerror_r(22, buf, sizeof buf) == 0 && buf[0], "strerror_r is the int-returning POSIX one");
    check(arc4random_uniform(10) < 10, "arc4random_uniform");
    check(__fpclassifyd(1.0) == 0x04 && __fpclassifyd(0.0) == 0x10, "__fpclassifyd returns bionic FP_* values");
    fp = funopen(&calls, funRead, 0, 0, funClose);
    check(fp && fread(buf, 1, 3, fp) == 3 && buf[0] == 'a' && buf[2] == 'c', "funopen cookie stream (android_fopen)");
    if (fp) fclose(fp);
    check(g_funClosed, "funopen close function runs");
    fp = tmpfile();
    if (fp) {
        fputs("12345", fp);
        check(fgetpos(fp, &pos.pos) == 0 && pos.pos == 5 && pos.post == CANARY, "fgetpos writes an 8-byte fpos_t");
        fclose(fp);
    }
    check(getaddrinfo("127.0.0.1", "80", &hints, &res) == 0 && res && res->ai_addr && res->ai_addr[0] == 2 &&
              res->ai_addr[2] == 0 && res->ai_addr[3] == 80 && res->ai_family == 2,
          "getaddrinfo returns BSD-layout addrinfo (ai_canonname before ai_addr)");
    if (res) freeaddrinfo(res);
    check(g_ctorArgc >= 1 && g_ctorEnvp != 0, "DT_INIT_ARRAY got (argc, argv, envp)");
}

/* ── the loader's own libdl ─────────────────────────────────────────── */
static int phdrCb(struct bdl_phdr_info *info, size_t size, void *data) {
    int i;
    uintptr_t want = (uintptr_t)data;
    (void)size;
    if (!endsWith(info->dlpi_name, "libfake_bionic.so")) return 0;
    for (i = 0; i < info->dlpi_phnum; i++) {
        const struct bphdr *p = &info->dlpi_phdr[i];
        if (p->p_type == 1 && want >= info->dlpi_addr + p->p_vaddr && want < info->dlpi_addr + p->p_vaddr + p->p_memsz) {
            int j;
            for (j = 0; j < info->dlpi_phnum; j++)
                if (info->dlpi_phdr[j].p_type == 0x6474e550u) return 2; /* PT_GNU_EH_FRAME found */
            return 1;
        }
    }
    return 0;
}

extern void ANativeActivity_onCreate(void *activity, void *saved, size_t size);

static void testDl(void) {
    void *libc = dlopen("libc.so", 2);
    size_t (*fnStrlen)(const char *) = libc ? (size_t (*)(const char *))dlsym(libc, "strlen") : 0;
    bdl_info info;
    check(fnStrlen && fnStrlen("four") == 4, "dlopen(\"libc.so\") + dlsym");
    check(dlsym(0 /* RTLD_DEFAULT */, "fake_dep_answer") == (void *)fake_dep_answer, "dlsym(RTLD_DEFAULT) finds bundled exports");
    check(dlopen("libdefinitelynothere.so", 2) == 0 && dlerror() != 0, "dlopen of a missing library fails with dlerror");
    check(dladdr((void *)ANativeActivity_onCreate, &info) && endsWith(info.dli_fname, "libfake_bionic.so") &&
              streq(info.dli_sname, "ANativeActivity_onCreate"),
          "dladdr names this library and symbol");
    check(dl_iterate_phdr(phdrCb, (void *)ANativeActivity_onCreate) == 2,
          "dl_iterate_phdr reports this library with its PT_GNU_EH_FRAME (what unwinders need)");
}

static void testDep(void) {
    check(fake_dep_answer() == 42, "call into a bundled DT_NEEDED library");
    check(streq(fake_dep_name(1), "two"), "the dependency's DT_RELR pointer table is relocated");
    check(fake_dep_ifunc() == 7, "STT_GNU_IFUNC import resolved through its resolver");
    check(fake_dep_calls_shared() == 1, "interposition: the root library's definition wins (BFS order)");
    check(fake_dep_counter == 5, "data import from the dependency (R_*_GLOB_DAT)");
}

/* activity->vm / env / clazz: the runner's JNI stub, called through the
 * function tables by index exactly like compiled JNI code does
 * ((*env)->FindClass is slot 6, (*vm)->GetEnv slot 6, ...). */
static void testJni(void *activity) {
    void *const *act = (void *const *)activity; /* callbacks, vm, env, clazz */
    void *vm = act[1], *env = act[2], *clazz = act[3], *env2 = 0, *str;
    const void *const *invoke, *const *fn;
    if (!vm || !env) {
        check(0, "activity->vm and ->env are a JNI stub, not NULL");
        return;
    }
    invoke = *(const void *const *const *)vm;
    fn = *(const void *const *const *)env;
    check(clazz != 0, "activity->clazz is a stub object");
    check(((int32_t (*)(void *, void **, int32_t))invoke[6])(vm, &env2, 0x10006) == 0 && env2 == env, "(*vm)->GetEnv");
    check(((int32_t (*)(void *))fn[4])(env) == 0x10006, "(*env)->GetVersion is JNI 1.6");
    check(((void *(*)(void *, const char *))fn[6])(env, "android/os/Build") == 0, "(*env)->FindClass logs and finds nothing");
    check(((void *(*)(void *, void *, const char *, const char *))fn[33])(env, 0, "getWindow", "()Landroid/view/Window;") == 0,
          "(*env)->GetMethodID logs and finds nothing");
    check(((uint8_t (*)(void *))fn[228])(env) == 0, "(*env)->ExceptionCheck: never pending");
    str = ((void *(*)(void *, const char *))fn[167])(env, "hi");
    check(str && strcmp(((const char *(*)(void *, void *, uint8_t *))fn[169])(env, str, 0), "hi") == 0,
          "NewStringUTF/GetStringUTFChars round-trip");
    check(((float (*)(void *, void *, void *, ...))fn[55])(env, clazz, 0) == 0.0f &&
              ((double (*)(void *, void *, void *, ...))fn[138])(env, 0, 0) == 0.0,
          "float/double calls return 0.0 in the FP register");
    check(((int32_t (*)(void *, void *, void *, ...))fn[49])(env, clazz, 0) == 0, "an unknown call returns 0");
}

static void testMissing(void) {
    check(&apak_test_weak_missing == 0, "weak undefined import binds to NULL");
    check(apak_test_missing_data == 0, "undefined data import reads zero-filled scratch");
    check((void *)apak_test_missing_function != 0, "undefined function import is bound to a trap");
    if (getenv("FAKE_CALL_MISSING")) {
        __android_log_print(4, "BionicTest", "calling the missing function on purpose");
        apak_test_missing_function();
        __android_log_print(4, "BionicTest", "the trap returned (ANDROIDPAK_MISSING=zero)");
    }
}

__attribute__((visibility("default"))) void ANativeActivity_onCreate(void *activity, void *saved, size_t size) {
    (void)saved;
    (void)size;
    __android_log_print(4, "BionicTest", "onCreate in an NDK-shaped library (libc.so/LIBC imports)");
    testLibc();
    testThreads();
    testSemaphore();
    testSignals();
    testSetjmp();
    testDl();
    testDep();
    testJni(activity);
    testMissing();
    fprintf(STDERR_F, "[bionic-test] %s (%d failures)\n", g_failures ? "FAILED" : "all checks passed", g_failures);
    fflush(STDERR_F);
    if (g_failures) _exit(3);
    ANativeActivity_finish(activity);
}
