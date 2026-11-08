/* bioniccompat.c — the libc half of the bionic compatibility layer.
 *
 * Every function here exists because a bionic-linked caller and glibc
 * disagree about something binary: a struct layout (FILE via __sF, fpos_t,
 * struct addrinfo), a constant (sysconf names, AI_ and NI_ flags, EAI_
 * codes, FP_ classes), a calling convention (bionic's __fgets_chk/__fread_chk
 * argument order), a symbol glibc does not export (atexit, stat before 2.33,
 * strlcpy before 2.38, arc4random before 2.36, gettid before 2.30) or a
 * bionic-only name (__errno, __system_property_get, android_set_abort_message,
 * __strlen_chk, __FD_SET_chk, ...). Things that are identical in both C
 * libraries (memcpy, strlen, pthread_cond_signal, open, ...) are NOT
 * wrapped: the resolver binds them straight to glibc.
 *
 * See bioniccompat.h for the binding order; bionicpthread.c holds the
 * pthread/semaphore/signal/setjmp half.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <math.h>
#include <netdb.h>
#include <poll.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdio_ext.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>
#include <wchar.h>

#include "bioniccompat.h"

#define CT_TAG "[androidrt] "

extern int __xpg_strerror_r(int errnum, char *buf, size_t len);
extern int __register_atfork(void (*prepare)(void), void (*parent)(void), void (*child)(void), void *dso);
extern int __cxa_atexit(void (*fn)(void *), void *arg, void *dso);

/* ── per-thread state (the runner's only __thread object) ──────────────
 * Initialised data, so the TLS init image every new thread copies already
 * carries the stack-guard canary in slots[3]. Do not add another __thread
 * variable anywhere in the runner: on aarch64 this block must start the
 * executable's static TLS at tpidr_el0 + 16 (checked at startup). */
__thread ApakTls apak_tls = { .slots = { 0, 0, 0, APAK_STACK_GUARD, 0, 0 } };

int apak_tlsSlotCheck(void) {
#if defined(__aarch64__)
    uintptr_t tp;
    __asm__ volatile("mrs %0, tpidr_el0" : "=r"(tp));
    return (uintptr_t)&apak_tls.slots[3] == tp + 0x28 && apak_tls.slots[3] == APAK_STACK_GUARD;
#else
    return 1;
#endif
}

void apak_compatFatal(const char *fmt, ...) {
    va_list ap;
    fputs(CT_TAG "FATAL: ", stderr);
    va_start(ap, fmt);
    vfprintf(stderr, fmt, ap);
    va_end(ap);
    fputc('\n', stderr);
    fflush(stderr);
    abort();
}

/* ── errno ─────────────────────────────────────────────────────────── */

static int *bw___errno(void) { return &errno; }

static int bw___set_errno(int n) {
    errno = n;
    return -1;
}

static int *bw___get_h_errno(void) { return __h_errno_location(); }

/* ── stdio: bionic's __sF[3] ──────────────────────────────────────────
 * Games built against pre-API-23 headers say `stdout` and get &__sF[1]
 * (struct __sFILE is 152 bytes on LP64 bionic). The array is dummy
 * storage; every FILE*-taking wrapper below maps it onto glibc's streams. */
#define APAK_BFILE_SIZE 152
static unsigned char g_sF[3 * APAK_BFILE_SIZE] __attribute__((aligned(16)));

void *apak_bionicFile(void *file) {
    uintptr_t p = (uintptr_t)file, b = (uintptr_t)g_sF;
    if (p >= b && p < b + sizeof g_sF) {
        size_t i = (p - b) / APAK_BFILE_SIZE;
        return i == 0 ? (void *)stdin : i == 1 ? (void *)stdout : (void *)stderr;
    }
    return file;
}

#define BF(f) ((FILE *)apak_bionicFile((void *)(f)))

static void sFInit(void) {
    static const int32_t flags[3] = { 0x0004 /* __SRD */, 0x0008 /* __SWR */, 0x0008 | 0x0002 /* __SWR|__SNBF */ };
    int i;
    for (i = 0; i < 3; i++) {
        memcpy(g_sF + i * APAK_BFILE_SIZE + 16, &flags[i], 4); /* _flags (LP64 int) */
        memcpy(g_sF + i * APAK_BFILE_SIZE + 20, &i, 4);        /* _file              */
    }
}

static int bw_fclose(FILE *f) { return fclose(BF(f)); }
static int bw_fflush(FILE *f) { return fflush(f ? BF(f) : NULL); }
static int bw_fputc(int c, FILE *f) { return fputc(c, BF(f)); }
static int bw_putc(int c, FILE *f) { return putc(c, BF(f)); }
static int bw_putc_unlocked(int c, FILE *f) { return putc_unlocked(c, BF(f)); }
static int bw_fputs(const char *s, FILE *f) { return fputs(s, BF(f)); }
static size_t bw_fwrite(const void *p, size_t s, size_t n, FILE *f) { return fwrite(p, s, n, BF(f)); }
static size_t bw_fwrite_unlocked(const void *p, size_t s, size_t n, FILE *f) { return fwrite_unlocked(p, s, n, BF(f)); }
static int bw_vfprintf(FILE *f, const char *fmt, va_list ap) { return vfprintf(BF(f), fmt, ap); }
static int bw_fprintf(FILE *f, const char *fmt, ...) {
    va_list ap;
    int r;
    va_start(ap, fmt);
    r = vfprintf(BF(f), fmt, ap);
    va_end(ap);
    return r;
}
static int bw_vfscanf(FILE *f, const char *fmt, va_list ap) { return vfscanf(BF(f), fmt, ap); }
static int bw_fscanf(FILE *f, const char *fmt, ...) {
    va_list ap;
    int r;
    va_start(ap, fmt);
    r = vfscanf(BF(f), fmt, ap);
    va_end(ap);
    return r;
}
static int bw_fgetc(FILE *f) { return fgetc(BF(f)); }
static int bw_getc(FILE *f) { return getc(BF(f)); }
static int bw_getc_unlocked(FILE *f) { return getc_unlocked(BF(f)); }
static char *bw_fgets(char *s, int n, FILE *f) { return fgets(s, n, BF(f)); }
static size_t bw_fread(void *p, size_t s, size_t n, FILE *f) { return fread(p, s, n, BF(f)); }
static size_t bw_fread_unlocked(void *p, size_t s, size_t n, FILE *f) { return fread_unlocked(p, s, n, BF(f)); }
static int bw_ungetc(int c, FILE *f) { return ungetc(c, BF(f)); }
static int bw_fseek(FILE *f, long off, int wh) { return fseek(BF(f), off, wh); }
static int bw_fseeko(FILE *f, off_t off, int wh) { return fseeko(BF(f), off, wh); }
static long bw_ftell(FILE *f) { return ftell(BF(f)); }
static off_t bw_ftello(FILE *f) { return ftello(BF(f)); }
static void bw_rewind(FILE *f) { rewind(BF(f)); }
/* bionic fpos_t is an off_t (8 bytes); glibc's is 16: never let glibc write
 * it, go through ftello/fseeko instead. */
static int bw_fgetpos(FILE *f, off_t *pos) {
    off_t p = ftello(BF(f));
    if (p < 0) return -1;
    *pos = p;
    return 0;
}
static int bw_fsetpos(FILE *f, const off_t *pos) { return fseeko(BF(f), *pos, SEEK_SET); }
static int bw_feof(FILE *f) { return feof(BF(f)); }
static int bw_ferror(FILE *f) { return ferror(BF(f)); }
static void bw_clearerr(FILE *f) { clearerr(BF(f)); }
static int bw_feof_unlocked(FILE *f) { return feof_unlocked(BF(f)); }
static int bw_ferror_unlocked(FILE *f) { return ferror_unlocked(BF(f)); }
static void bw_clearerr_unlocked(FILE *f) { clearerr_unlocked(BF(f)); }
static int bw_fileno(FILE *f) { return fileno(BF(f)); }
static int bw_fileno_unlocked(FILE *f) { return fileno_unlocked(BF(f)); }
static int bw_setvbuf(FILE *f, char *buf, int mode, size_t size) { return setvbuf(BF(f), buf, mode, size); }
static void bw_setbuf(FILE *f, char *buf) { setbuf(BF(f), buf); }
static void bw_setbuffer(FILE *f, char *buf, int size) { setbuffer(BF(f), buf, (size_t)size); }
static int bw_setlinebuf(FILE *f) {
    setlinebuf(BF(f));
    return 0;
}
static FILE *bw_freopen(const char *path, const char *mode, FILE *f) { return freopen(path, mode, BF(f)); }
static void bw_flockfile(FILE *f) { flockfile(BF(f)); }
static void bw_funlockfile(FILE *f) { funlockfile(BF(f)); }
static int bw_ftrylockfile(FILE *f) { return ftrylockfile(BF(f)); }
static ssize_t bw_getline(char **line, size_t *n, FILE *f) { return getline(line, n, BF(f)); }
static ssize_t bw_getdelim(char **line, size_t *n, int d, FILE *f) { return getdelim(line, n, d, BF(f)); }
static int bw_fpurge(FILE *f) {
    __fpurge(BF(f));
    return 0;
}
static void bw___fpurge(FILE *f) { __fpurge(BF(f)); }
static size_t bw___fbufsize(FILE *f) { return __fbufsize(BF(f)); }
static int bw___freading(FILE *f) { return __freading(BF(f)); }
static int bw___fwriting(FILE *f) { return __fwriting(BF(f)); }
static int bw___freadable(FILE *f) { return __freadable(BF(f)); }
static int bw___fwritable(FILE *f) { return __fwritable(BF(f)); }
static int bw___flbf(FILE *f) { return __flbf(BF(f)); }
static size_t bw___fpending(FILE *f) { return __fpending(BF(f)); }
static int bw___fsetlocking(FILE *f, int type) { return __fsetlocking(BF(f), type); }
static wint_t bw_fgetwc(FILE *f) { return fgetwc(BF(f)); }
static wint_t bw_getwc(FILE *f) { return getwc(BF(f)); }
static wint_t bw_fputwc(wchar_t c, FILE *f) { return fputwc(c, BF(f)); }
static wint_t bw_putwc(wchar_t c, FILE *f) { return putwc(c, BF(f)); }
static wint_t bw_ungetwc(wint_t c, FILE *f) { return ungetwc(c, BF(f)); }
static int bw_fputws(const wchar_t *s, FILE *f) { return fputws(s, BF(f)); }
static wchar_t *bw_fgetws(wchar_t *s, int n, FILE *f) { return fgetws(s, n, BF(f)); }
static int bw_fwide(FILE *f, int mode) { return fwide(BF(f), mode); }
static int bw_vfwprintf(FILE *f, const wchar_t *fmt, va_list ap) { return vfwprintf(BF(f), fmt, ap); }
static int bw_fwprintf(FILE *f, const wchar_t *fmt, ...) {
    va_list ap;
    int r;
    va_start(ap, fmt);
    r = vfwprintf(BF(f), fmt, ap);
    va_end(ap);
    return r;
}

/* bionic's _chk stdio entry points differ from glibc's in argument order. */
static char *bw___fgets_chk(char *dst, int size, FILE *f, size_t dstLen) {
    if (size < 0) apak_compatFatal("fgets: buffer size %d < 0", size);
    if ((size_t)size > dstLen) apak_compatFatal("fgets: prevented %d-byte write into %zu-byte buffer", size, dstLen);
    return fgets(dst, size, BF(f));
}
static size_t bw___fread_chk(void *buf, size_t size, size_t count, FILE *f, size_t bufSize) {
    size_t total;
    if (__builtin_mul_overflow(size, count, &total) || total > bufSize)
        apak_compatFatal("fread: prevented write past the end of a %zu-byte buffer", bufSize);
    return fread(buf, size, count, BF(f));
}
static size_t bw___fwrite_chk(const void *buf, size_t size, size_t count, FILE *f, size_t bufSize) {
    size_t total;
    if (__builtin_mul_overflow(size, count, &total) || total > bufSize)
        apak_compatFatal("fwrite: prevented read past the end of a %zu-byte buffer", bufSize);
    return fwrite(buf, size, count, BF(f));
}

/* funopen / funopen64: BSD cookie streams, bionic-only. The classic NDK
 * "android_fopen" wraps an AAsset in one of these. */
typedef struct {
    void *cookie;
    int (*rd)(void *, char *, int);
    int (*wr)(void *, const char *, int);
    long (*sk)(void *, long, int);
    int (*cl)(void *);
} ApakFunCookie;

static ssize_t funRead(void *c, char *buf, size_t n) {
    ApakFunCookie *f = (ApakFunCookie *)c;
    if (!f->rd) {
        errno = EBADF;
        return -1;
    }
    return f->rd(f->cookie, buf, n > INT_MAX ? INT_MAX : (int)n);
}
static ssize_t funWrite(void *c, const char *buf, size_t n) {
    ApakFunCookie *f = (ApakFunCookie *)c;
    if (!f->wr) {
        errno = EBADF;
        return -1;
    }
    return f->wr(f->cookie, buf, n > INT_MAX ? INT_MAX : (int)n);
}
static int funSeek(void *c, off64_t *pos, int whence) {
    ApakFunCookie *f = (ApakFunCookie *)c;
    long r;
    if (!f->sk) {
        errno = ESPIPE;
        return -1;
    }
    r = f->sk(f->cookie, (long)*pos, whence);
    if (r < 0) return -1;
    *pos = r;
    return 0;
}
static int funClose(void *c) {
    ApakFunCookie *f = (ApakFunCookie *)c;
    int r = f->cl ? f->cl(f->cookie) : 0;
    free(f);
    return r;
}
static FILE *bw_funopen(const void *cookie, int (*rd)(void *, char *, int), int (*wr)(void *, const char *, int),
                        long (*sk)(void *, long, int), int (*cl)(void *)) {
    cookie_io_functions_t io;
    ApakFunCookie *f;
    FILE *fp;
    if (!rd && !wr) {
        errno = EINVAL;
        return NULL;
    }
    f = (ApakFunCookie *)calloc(1, sizeof *f);
    if (!f) return NULL;
    f->cookie = (void *)cookie;
    f->rd = rd;
    f->wr = wr;
    f->sk = sk;
    f->cl = cl;
    io.read = rd ? funRead : NULL;
    io.write = wr ? funWrite : NULL;
    io.seek = funSeek;
    io.close = funClose;
    fp = fopencookie(f, rd && wr ? "r+" : rd ? "r" : "w", io);
    if (!fp) free(f);
    return fp;
}

/* ── ctype: bionic's (OpenBSD) table-driven macros ──────────────────── */
static char g_ctype[257];
static short g_tolower[257], g_toupper[257];

static void ctypeInit(void) {
    int c;
    g_ctype[0] = 0;
    g_tolower[0] = g_toupper[0] = -1; /* EOF */
    for (c = 0; c < 256; c++) {
        int f = 0;
        if (c < 128) {
            if (c >= 'A' && c <= 'Z') f |= 0x01;                          /* _U */
            if (c >= 'a' && c <= 'z') f |= 0x02;                          /* _L */
            if (c >= '0' && c <= '9') f |= 0x04;                          /* _N */
            if (c == ' ' || (c >= '\t' && c <= '\r')) f |= 0x08;          /* _S */
            if (c > ' ' && c < 127 && !(f & 0x07)) f |= 0x10;             /* _P */
            if (c < ' ' || c == 127) f |= 0x20;                           /* _C */
            if ((c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) f |= 0x40; /* _X */
            if (c == ' ') f |= 0x80;                                      /* _B */
        }
        g_ctype[c + 1] = (char)f;
        g_tolower[c + 1] = (short)((c >= 'A' && c <= 'Z') ? c + 32 : c);
        g_toupper[c + 1] = (short)((c >= 'a' && c <= 'z') ? c - 32 : c);
    }
}

/* ── sysconf: bionic numbers its _SC_ names differently ─────────────── */
static const struct {
    int bionic;
    int host;
} kSysconf[] = {
    { 0x0000, _SC_ARG_MAX }, { 0x0001, _SC_BC_BASE_MAX }, { 0x0002, _SC_BC_DIM_MAX },
    { 0x0003, _SC_BC_SCALE_MAX }, { 0x0004, _SC_BC_STRING_MAX }, { 0x0005, _SC_CHILD_MAX },
    { 0x0006, _SC_CLK_TCK }, { 0x0007, _SC_COLL_WEIGHTS_MAX }, { 0x0008, _SC_EXPR_NEST_MAX },
    { 0x0009, _SC_LINE_MAX }, { 0x000a, _SC_NGROUPS_MAX }, { 0x000b, _SC_OPEN_MAX },
    { 0x000c, _SC_PASS_MAX }, { 0x000d, _SC_2_C_BIND }, { 0x000e, _SC_2_C_DEV },
    { 0x000f, _SC_2_C_VERSION },
    { 0x0010, _SC_2_CHAR_TERM }, { 0x0011, _SC_2_FORT_DEV }, { 0x0012, _SC_2_FORT_RUN },
    { 0x0013, _SC_2_LOCALEDEF }, { 0x0014, _SC_2_SW_DEV }, { 0x0015, _SC_2_UPE },
    { 0x0016, _SC_2_VERSION }, { 0x0017, _SC_JOB_CONTROL }, { 0x0018, _SC_SAVED_IDS },
    { 0x0019, _SC_VERSION }, { 0x001a, _SC_RE_DUP_MAX }, { 0x001b, _SC_STREAM_MAX },
    { 0x001c, _SC_TZNAME_MAX }, { 0x001d, _SC_XOPEN_CRYPT }, { 0x001e, _SC_XOPEN_ENH_I18N },
    { 0x001f, _SC_XOPEN_SHM }, { 0x0020, _SC_XOPEN_VERSION }, { 0x0021, _SC_XOPEN_XCU_VERSION },
    { 0x0022, _SC_XOPEN_REALTIME }, { 0x0023, _SC_XOPEN_REALTIME_THREADS }, { 0x0024, _SC_XOPEN_LEGACY },
    { 0x0025, _SC_ATEXIT_MAX }, { 0x0026, _SC_IOV_MAX }, { 0x0027, _SC_PAGESIZE },
    { 0x0028, _SC_PAGE_SIZE }, { 0x0029, _SC_XOPEN_UNIX }, { 0x002a, _SC_XBS5_ILP32_OFF32 },
    { 0x002b, _SC_XBS5_ILP32_OFFBIG }, { 0x002c, _SC_XBS5_LP64_OFF64 }, { 0x002d, _SC_XBS5_LPBIG_OFFBIG },
    { 0x002e, _SC_AIO_LISTIO_MAX },
    { 0x002f, _SC_AIO_MAX }, { 0x0030, _SC_AIO_PRIO_DELTA_MAX }, { 0x0031, _SC_DELAYTIMER_MAX },
    { 0x0032, _SC_MQ_OPEN_MAX }, { 0x0033, _SC_MQ_PRIO_MAX }, { 0x0034, _SC_RTSIG_MAX },
    { 0x0035, _SC_SEM_NSEMS_MAX }, { 0x0036, _SC_SEM_VALUE_MAX }, { 0x0037, _SC_SIGQUEUE_MAX },
    { 0x0038, _SC_TIMER_MAX }, { 0x0039, _SC_ASYNCHRONOUS_IO }, { 0x003a, _SC_FSYNC },
    { 0x003b, _SC_MAPPED_FILES }, { 0x003c, _SC_MEMLOCK }, { 0x003d, _SC_MEMLOCK_RANGE },
    { 0x003e, _SC_MEMORY_PROTECTION }, { 0x003f, _SC_MESSAGE_PASSING }, { 0x0040, _SC_PRIORITIZED_IO },
    { 0x0041, _SC_PRIORITY_SCHEDULING }, { 0x0042, _SC_REALTIME_SIGNALS }, { 0x0043, _SC_SEMAPHORES },
    { 0x0044, _SC_SHARED_MEMORY_OBJECTS }, { 0x0045, _SC_SYNCHRONIZED_IO }, { 0x0046, _SC_TIMERS },
    { 0x0047, _SC_GETGR_R_SIZE_MAX }, { 0x0048, _SC_GETPW_R_SIZE_MAX }, { 0x0049, _SC_LOGIN_NAME_MAX },
    { 0x004a, _SC_THREAD_DESTRUCTOR_ITERATIONS }, { 0x004b, _SC_THREAD_KEYS_MAX },
    { 0x004c, _SC_THREAD_STACK_MIN }, { 0x004d, _SC_THREAD_THREADS_MAX }, { 0x004e, _SC_TTY_NAME_MAX },
    { 0x004f, _SC_THREADS }, { 0x0050, _SC_THREAD_ATTR_STACKADDR }, { 0x0051, _SC_THREAD_ATTR_STACKSIZE },
    { 0x0052, _SC_THREAD_PRIORITY_SCHEDULING }, { 0x0053, _SC_THREAD_PRIO_INHERIT },
    { 0x0054, _SC_THREAD_PRIO_PROTECT }, { 0x0055, _SC_THREAD_SAFE_FUNCTIONS },
    { 0x0060, _SC_NPROCESSORS_CONF }, { 0x0061, _SC_NPROCESSORS_ONLN }, { 0x0062, _SC_PHYS_PAGES },
    { 0x0063, _SC_AVPHYS_PAGES }, { 0x0064, _SC_MONOTONIC_CLOCK }, { 0x0065, _SC_2_PBS },
    { 0x0066, _SC_2_PBS_ACCOUNTING }, { 0x0067, _SC_2_PBS_CHECKPOINT }, { 0x0068, _SC_2_PBS_LOCATE },
    { 0x0069, _SC_2_PBS_MESSAGE }, { 0x006a, _SC_2_PBS_TRACK }, { 0x006b, _SC_ADVISORY_INFO },
    { 0x006c, _SC_BARRIERS }, { 0x006d, _SC_CLOCK_SELECTION }, { 0x006e, _SC_CPUTIME },
    { 0x006f, _SC_HOST_NAME_MAX }, { 0x0070, _SC_IPV6 }, { 0x0071, _SC_RAW_SOCKETS },
    { 0x0072, _SC_READER_WRITER_LOCKS }, { 0x0073, _SC_REGEXP }, { 0x0074, _SC_SHELL },
    { 0x0075, _SC_SPAWN }, { 0x0076, _SC_SPIN_LOCKS }, { 0x0077, _SC_SPORADIC_SERVER },
    { 0x0078, _SC_SS_REPL_MAX }, { 0x0079, _SC_SYMLOOP_MAX }, { 0x007a, _SC_THREAD_CPUTIME },
    { 0x007b, _SC_THREAD_PROCESS_SHARED }, { 0x007c, _SC_THREAD_ROBUST_PRIO_INHERIT },
    { 0x007d, _SC_THREAD_ROBUST_PRIO_PROTECT }, { 0x007e, _SC_THREAD_SPORADIC_SERVER },
    { 0x007f, _SC_TIMEOUTS }, { 0x0080, _SC_TRACE }, { 0x0081, _SC_TRACE_EVENT_FILTER },
    { 0x0082, _SC_TRACE_EVENT_NAME_MAX }, { 0x0083, _SC_TRACE_INHERIT }, { 0x0084, _SC_TRACE_LOG },
    { 0x0085, _SC_TRACE_NAME_MAX }, { 0x0086, _SC_TRACE_SYS_MAX }, { 0x0087, _SC_TRACE_USER_EVENT_MAX },
    { 0x0088, _SC_TYPED_MEMORY_OBJECTS }, { 0x0089, _SC_V7_ILP32_OFF32 }, { 0x008a, _SC_V7_ILP32_OFFBIG },
    { 0x008b, _SC_V7_LP64_OFF64 }, { 0x008c, _SC_V7_LPBIG_OFFBIG }, { 0x008d, _SC_XOPEN_STREAMS },
    { 0x008f, _SC_LEVEL1_ICACHE_SIZE }, { 0x0090, _SC_LEVEL1_ICACHE_ASSOC },
    { 0x0091, _SC_LEVEL1_ICACHE_LINESIZE }, { 0x0092, _SC_LEVEL1_DCACHE_SIZE },
    { 0x0093, _SC_LEVEL1_DCACHE_ASSOC }, { 0x0094, _SC_LEVEL1_DCACHE_LINESIZE },
    { 0x0095, _SC_LEVEL2_CACHE_SIZE }, { 0x0096, _SC_LEVEL2_CACHE_ASSOC },
    { 0x0097, _SC_LEVEL2_CACHE_LINESIZE }, { 0x0098, _SC_LEVEL3_CACHE_SIZE },
    { 0x0099, _SC_LEVEL3_CACHE_ASSOC }, { 0x009a, _SC_LEVEL3_CACHE_LINESIZE },
    { 0x009b, _SC_LEVEL4_CACHE_SIZE }, { 0x009c, _SC_LEVEL4_CACHE_ASSOC },
    { 0x009d, _SC_LEVEL4_CACHE_LINESIZE },
};

static long bw_sysconf(int name) {
    size_t i;
    for (i = 0; i < sizeof kSysconf / sizeof kSysconf[0]; i++)
        if (kSysconf[i].bionic == name) return sysconf(kSysconf[i].host);
    errno = EINVAL;
    return -1;
}

static int bw_getpagesize(void) { return (int)sysconf(_SC_PAGESIZE); }

/* ── FORTIFY entry points only bionic has ───────────────────────────── */
static size_t bw___strlen_chk(const char *s, size_t sLen) {
    size_t n = strlen(s);
    if (n >= sLen) apak_compatFatal("strlen: prevented read past the end of a %zu-byte buffer", sLen);
    return n;
}
static char *bw___strchr_chk(const char *p, int ch, size_t sLen) {
    for (;; ++p, sLen--) {
        if (sLen == 0) apak_compatFatal("strchr: prevented read past the end of the buffer");
        if (*p == (char)ch) return (char *)p;
        if (*p == '\0') return NULL;
    }
}
static char *bw___strrchr_chk(const char *p, int ch, size_t sLen) {
    const char *save = NULL;
    for (;; ++p, sLen--) {
        if (sLen == 0) apak_compatFatal("strrchr: prevented read past the end of the buffer");
        if (*p == (char)ch) save = p;
        if (*p == '\0') return (char *)save;
    }
}
static size_t bw_strlcpy(char *dst, const char *src, size_t size) {
    size_t len = strlen(src);
    if (size) {
        size_t n = len < size - 1 ? len : size - 1;
        memcpy(dst, src, n);
        dst[n] = '\0';
    }
    return len;
}
static size_t bw_strlcat(char *dst, const char *src, size_t size) {
    size_t dlen = strnlen(dst, size);
    if (dlen == size) return size + strlen(src);
    return dlen + bw_strlcpy(dst + dlen, src, size - dlen);
}
static size_t bw___strlcpy_chk(char *dst, const char *src, size_t supplied, size_t dstLen) {
    if (supplied > dstLen) apak_compatFatal("strlcpy: prevented %zu-byte write into %zu-byte buffer", supplied, dstLen);
    return bw_strlcpy(dst, src, supplied);
}
static size_t bw___strlcat_chk(char *dst, const char *src, size_t supplied, size_t dstLen) {
    if (supplied > dstLen) apak_compatFatal("strlcat: prevented %zu-byte write into %zu-byte buffer", supplied, dstLen);
    return bw_strlcat(dst, src, supplied);
}
static char *bw___strncpy_chk2(char *dst, const char *src, size_t n, size_t dstLen, size_t srcLen) {
    (void)srcLen;
    if (n > dstLen) apak_compatFatal("strncpy: prevented %zu-byte write into %zu-byte buffer", n, dstLen);
    return strncpy(dst, src, n);
}
static char *bw___stpncpy_chk2(char *dst, const char *src, size_t n, size_t dstLen, size_t srcLen) {
    (void)srcLen;
    if (n > dstLen) apak_compatFatal("stpncpy: prevented %zu-byte write into %zu-byte buffer", n, dstLen);
    return stpncpy(dst, src, n);
}
static void *bw___memchr_chk(const void *s, int c, size_t n, size_t bufSize) {
    if (n > bufSize) apak_compatFatal("memchr: prevented read past the end of a %zu-byte buffer", bufSize);
    return memchr(s, c, n);
}
static void *bw___memrchr_chk(const void *s, int c, size_t n, size_t bufSize) {
    if (n > bufSize) apak_compatFatal("memrchr: prevented read past the end of a %zu-byte buffer", bufSize);
    return memrchr(s, c, n);
}
static mode_t bw___umask_chk(mode_t mode) {
    if (mode & ~(mode_t)0777) apak_compatFatal("umask: called with invalid mode %o", (unsigned)mode);
    return umask(mode);
}
static void fdChk(const char *what, int fd, size_t setSize) {
    if (fd < 0) apak_compatFatal("%s: file descriptor %d < 0", what, fd);
    if (fd >= FD_SETSIZE) apak_compatFatal("%s: file descriptor %d >= FD_SETSIZE", what, fd);
    if (setSize < sizeof(fd_set)) apak_compatFatal("%s: set is too small", what);
}
static int bw___FD_ISSET_chk(int fd, const fd_set *set, size_t setSize) {
    fdChk("FD_ISSET", fd, setSize);
    return FD_ISSET(fd, set);
}
static void bw___FD_CLR_chk(int fd, fd_set *set, size_t setSize) {
    fdChk("FD_CLR", fd, setSize);
    FD_CLR(fd, set);
}
static void bw___FD_SET_chk(int fd, fd_set *set, size_t setSize) {
    fdChk("FD_SET", fd, setSize);
    FD_SET(fd, set);
}
static int bw___poll_chk(struct pollfd *fds, nfds_t n, int timeout, size_t fdsSize) {
    if (fdsSize / sizeof(struct pollfd) < n) apak_compatFatal("poll: pollfd array smaller than fd count");
    return poll(fds, n, timeout);
}
static ssize_t bw___pread_chk(int fd, void *buf, size_t count, off_t offset, size_t bufSize) {
    if (count > bufSize) apak_compatFatal("pread: prevented write past the end of a %zu-byte buffer", bufSize);
    return pread(fd, buf, count, offset);
}
static ssize_t bw___pwrite_chk(int fd, const void *buf, size_t count, off_t offset, size_t bufSize) {
    if (count > bufSize) apak_compatFatal("pwrite: prevented read past the end of a %zu-byte buffer", bufSize);
    return pwrite(fd, buf, count, offset);
}
static ssize_t bw___read_chk(int fd, void *buf, size_t count, size_t bufSize) {
    if (count > bufSize) apak_compatFatal("read: prevented write past the end of a %zu-byte buffer", bufSize);
    return read(fd, buf, count);
}
static ssize_t bw___write_chk(int fd, const void *buf, size_t count, size_t bufSize) {
    if (count > bufSize) apak_compatFatal("write: prevented read past the end of a %zu-byte buffer", bufSize);
    return write(fd, buf, count);
}
static ssize_t bw___readlink_chk(const char *path, char *buf, size_t size, size_t bufSize) {
    if (size > bufSize) apak_compatFatal("readlink: prevented write past the end of a %zu-byte buffer", bufSize);
    return readlink(path, buf, size);
}
static ssize_t bw___readlinkat_chk(int dirfd, const char *path, char *buf, size_t size, size_t bufSize) {
    if (size > bufSize) apak_compatFatal("readlinkat: prevented write past the end of a %zu-byte buffer", bufSize);
    return readlinkat(dirfd, path, buf, size);
}
static ssize_t bw___recvfrom_chk(int s, void *buf, size_t len, size_t bufSize, int flags, struct sockaddr *a,
                                 socklen_t *al) {
    if (len > bufSize) apak_compatFatal("recvfrom: prevented write past the end of a %zu-byte buffer", bufSize);
    return recvfrom(s, buf, len, flags, a, al);
}
static ssize_t bw___sendto_chk(int s, const void *buf, size_t len, size_t bufSize, int flags,
                               const struct sockaddr *a, socklen_t al) {
    if (len > bufSize) apak_compatFatal("sendto: prevented read past the end of a %zu-byte buffer", bufSize);
    return sendto(s, buf, len, flags, a, al);
}
static char *bw___getcwd_chk(char *buf, size_t len, size_t actual) {
    if (buf && len > actual) apak_compatFatal("getcwd: prevented write past the end of a %zu-byte buffer", actual);
    return getcwd(buf, len);
}
static int bw___open_2(const char *path, int flags) { return open(path, flags); }
static int bw___openat_2(int dirfd, const char *path, int flags) { return openat(dirfd, path, flags); }

/* ── stat family straight to the kernel ───────────────────────────────
 * glibc before 2.33 does not export stat/fstat/lstat/fstatat at all (they
 * were inline __xstat wrappers), and bionic's struct stat IS the kernel's
 * on arm64 and x86_64, so the syscalls fill the caller's buffer as is. */
static int bw_stat(const char *path, struct stat *st) {
    return (int)syscall(SYS_newfstatat, AT_FDCWD, path, st, 0);
}
static int bw_lstat(const char *path, struct stat *st) {
    return (int)syscall(SYS_newfstatat, AT_FDCWD, path, st, AT_SYMLINK_NOFOLLOW);
}
static int bw_fstat(int fd, struct stat *st) { return (int)syscall(SYS_fstat, fd, st); }
static int bw_fstatat(int dirfd, const char *path, struct stat *st, int flags) {
    return (int)syscall(SYS_newfstatat, dirfd, path, st, flags);
}
static int bw_mknod(const char *path, mode_t mode, dev_t dev) {
    return (int)syscall(SYS_mknodat, AT_FDCWD, path, mode, dev);
}
static int bw_mknodat(int dirfd, const char *path, mode_t mode, dev_t dev) {
    return (int)syscall(SYS_mknodat, dirfd, path, mode, dev);
}

/* ── strings with different semantics ─────────────────────────────── */

/* bionic's basename/dirname are the POSIX libgen ones (trailing slashes
 * stripped, input untouched, result in thread-local storage); glibc's
 * exported `basename` is the GNU string.h one. */
static char *bw_basename(const char *path) {
    char *out = apak_tls.pathBuf;
    size_t cap = sizeof apak_tls.pathBuf;
    const char *end, *start;
    size_t len;
    if (!path || !*path) {
        snprintf(out, cap, ".");
        return out;
    }
    end = path + strlen(path) - 1;
    while (end > path && *end == '/') end--;
    if (end == path && *end == '/') {
        snprintf(out, cap, "/");
        return out;
    }
    start = end;
    while (start > path && start[-1] != '/') start--;
    len = (size_t)(end - start + 1);
    if (len >= cap) {
        errno = ENAMETOOLONG;
        return NULL;
    }
    memcpy(out, start, len);
    out[len] = '\0';
    return out;
}
static char *bw_dirname(const char *path) {
    char *out = apak_tls.pathBuf;
    size_t cap = sizeof apak_tls.pathBuf;
    const char *end;
    size_t len;
    if (!path || !*path) {
        snprintf(out, cap, ".");
        return out;
    }
    end = path + strlen(path) - 1;
    while (end > path && *end == '/') end--;
    while (end > path && *end != '/') end--;
    if (end == path) {
        snprintf(out, cap, "%s", *end == '/' ? "/" : ".");
        return out;
    }
    do {
        end--;
    } while (end > path && *end == '/');
    len = (size_t)(end - path + 1);
    if (len >= cap) {
        errno = ENAMETOOLONG;
        return NULL;
    }
    memcpy(out, path, len);
    out[len] = '\0';
    return out;
}
static char *bw___gnu_basename(const char *path) {
    const char *slash = strrchr(path, '/');
    return (char *)(slash ? slash + 1 : path);
}
/* bionic's strerror_r is the POSIX (int-returning) one. */
static int bw_strerror_r(int err, char *buf, size_t len) { return __xpg_strerror_r(err, buf, len); }
static char *bw___gnu_strerror_r(int err, char *buf, size_t len) { return strerror_r(err, buf, len); }

/* ── randomness, ids, small bionic-only calls ───────────────────────── */

static int randomBytes(void *buf, size_t n) {
    unsigned char *p = (unsigned char *)buf;
    while (n) {
        long got = -1;
#ifdef SYS_getrandom
        got = syscall(SYS_getrandom, p, n, 0);
#endif
        if (got <= 0) {
            int fd = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
            if (fd < 0) return -1;
            got = read(fd, p, n);
            close(fd);
            if (got <= 0) return -1;
        }
        p += got;
        n -= (size_t)got;
    }
    return 0;
}

static void bw_arc4random_buf(void *buf, size_t n) {
    if (randomBytes(buf, n) != 0) {
        /* No entropy source at all: never return silently-zero bytes. */
        static uint64_t s = 0x9e3779b97f4a7c15ull;
        unsigned char *p = (unsigned char *)buf;
        size_t i;
        s ^= (uint64_t)time(NULL) ^ (uint64_t)(uintptr_t)buf;
        for (i = 0; i < n; i++) {
            s ^= s << 13;
            s ^= s >> 7;
            s ^= s << 17;
            p[i] = (unsigned char)s;
        }
    }
}
static uint32_t bw_arc4random(void) {
    uint32_t v;
    bw_arc4random_buf(&v, sizeof v);
    return v;
}
static uint32_t bw_arc4random_uniform(uint32_t upper) {
    uint32_t min, r;
    if (upper < 2) return 0;
    min = (uint32_t)(-upper) % upper;
    do {
        r = bw_arc4random();
    } while (r < min);
    return r % upper;
}
static ssize_t bw_getrandom(void *buf, size_t n, unsigned flags) {
#ifdef SYS_getrandom
    return (ssize_t)syscall(SYS_getrandom, buf, n, flags);
#else
    (void)flags;
    return randomBytes(buf, n) == 0 ? (ssize_t)n : -1;
#endif
}
static int bw_getentropy(void *buf, size_t n) {
    if (n > 256) {
        errno = EIO;
        return -1;
    }
    return randomBytes(buf, n);
}
static pid_t bw_gettid(void) { return (pid_t)syscall(SYS_gettid); }
static int bw_tgkill(int tgid, int tid, int sig) { return (int)syscall(SYS_tgkill, tgid, tid, sig); }
static int bw_tkill(int tid, int sig) { return (int)syscall(SYS_tkill, tid, sig); }
static const char *bw_getprogname(void) { return program_invocation_short_name; }
static void *bw_reallocarray(void *p, size_t n, size_t size) {
    size_t total;
    if (__builtin_mul_overflow(n, size, &total)) {
        errno = ENOMEM;
        return NULL;
    }
    return realloc(p, total);
}
static int bw_memfd_create(const char *name, unsigned flags) {
#ifdef SYS_memfd_create
    return (int)syscall(SYS_memfd_create, name, flags);
#else
    (void)name;
    (void)flags;
    errno = ENOSYS;
    return -1;
#endif
}

static char *g_abortMessage;
static void bw_android_set_abort_message(const char *msg) {
    if (!msg) return;
    free(g_abortMessage);
    g_abortMessage = strdup(msg);
    fprintf(stderr, CT_TAG "abort message: %s\n", msg);
}
static int bw_android_get_application_target_sdk_version(void) { return 28; }
static int bw_android_get_device_api_level(void) { return 28; }

static void bw___assert(const char *file, int line, const char *msg) {
    apak_compatFatal("assertion \"%s\" failed: file \"%s\", line %d", msg ? msg : "?", file ? file : "?", line);
}
static void bw___assert2(const char *file, int line, const char *func, const char *msg) {
    apak_compatFatal("assertion \"%s\" failed: file \"%s\", line %d, function \"%s\"", msg ? msg : "?",
                     file ? file : "?", line, func ? func : "?");
}

/* atexit/pthread_atfork live in glibc's libc_nonshared.a, never in
 * libc.so.6: a bionic import of them has nothing to bind to otherwise. */
static int bw_atexit(void (*fn)(void)) { return __cxa_atexit((void (*)(void *))fn, NULL, NULL); }
static int bw_pthread_atfork(void (*prepare)(void), void (*parent)(void), void (*child)(void)) {
    return __register_atfork(prepare, parent, child, NULL);
}

/* The canary for GCC-era NDK libraries, which read a global instead of the
 * TLS slot (see apak_tls). Any value works as long as it never changes. */
static uintptr_t g_stackChkGuard = APAK_STACK_GUARD;
static void bw___stack_chk_fail(void) { apak_compatFatal("stack corruption detected (-fstack-protector)"); }

static int g_isthreaded = 1;
static unsigned g_pageSizeVar = 4096;
static unsigned g_pageShift = 12;

/* android_fdsan (API 29): ownership tags are advisory here. */
static uint64_t bw_android_fdsan_create_owner_tag(int type, uint64_t tag) {
    return ((uint64_t)(type & 0xff) << 56) | (tag & 0x00ffffffffffffffull);
}
static void bw_android_fdsan_exchange_owner_tag(int fd, uint64_t expected, uint64_t newTag) {
    (void)fd;
    (void)expected;
    (void)newTag;
}
static int bw_android_fdsan_close_with_tag(int fd, uint64_t tag) {
    (void)tag;
    return close(fd);
}
static uint64_t bw_android_fdsan_get_owner_tag(int fd) {
    (void)fd;
    return 0;
}
static const char *bw_android_fdsan_get_tag_type(uint64_t tag) {
    (void)tag;
    return "unowned";
}
static uint64_t bw_android_fdsan_get_tag_value(uint64_t tag) { return tag & 0x00ffffffffffffffull; }
static int bw_android_fdsan_get_error_level(void) { return 0; }
static int bw_android_fdsan_set_error_level(int level) {
    (void)level;
    return 0;
}

/* ── math: bionic's FP_* classes are the BSD bit values ─────────────── */
#define BFP_INFINITE  0x01
#define BFP_NAN       0x02
#define BFP_NORMAL    0x04
#define BFP_SUBNORMAL 0x08
#define BFP_ZERO      0x10

static int bionicClass(int hostClass) {
    switch (hostClass) {
        case FP_INFINITE: return BFP_INFINITE;
        case FP_NAN: return BFP_NAN;
        case FP_NORMAL: return BFP_NORMAL;
        case FP_SUBNORMAL: return BFP_SUBNORMAL;
        default: return BFP_ZERO;
    }
}
static int bw___fpclassifyd(double x) { return bionicClass(fpclassify(x)); }
static int bw___fpclassifyf(float x) { return bionicClass(fpclassify(x)); }
static int bw___fpclassifyl(long double x) { return bionicClass(fpclassify(x)); }
static int bw___isfinite(double x) { return isfinite(x); }
static int bw___isfinitef(float x) { return isfinite(x); }
static int bw___isfinitel(long double x) { return isfinite(x); }
static int bw___isnormal(double x) { return isnormal(x); }
static int bw___isnormalf(float x) { return isnormal(x); }
static int bw___isnormall(long double x) { return isnormal(x); }
static int bw___isinfl(long double x) { return isinf(x); }
static int bw___isnanl(long double x) { return isnan(x); }

/* ── netdb: bionic's struct addrinfo is the BSD one (ai_canonname before
 * ai_addr) and its AI_/NI_/EAI_ numbers are BSD numbers ───────────── */
typedef struct BAddrinfo {
    int ai_flags;
    int ai_family;
    int ai_socktype;
    int ai_protocol;
    socklen_t ai_addrlen;
    char *ai_canonname;
    struct sockaddr *ai_addr;
    struct BAddrinfo *ai_next;
} BAddrinfo;

static const struct {
    int bionic;
    int host;
} kAiFlags[] = {
    { 0x0001, AI_PASSIVE }, { 0x0002, AI_CANONNAME }, { 0x0004, AI_NUMERICHOST },
    { 0x0008, AI_NUMERICSERV }, { 0x0100, AI_ALL }, { 0x0400, AI_ADDRCONFIG }, { 0x0800, AI_V4MAPPED },
}, kNiFlags[] = {
    { 0x0001, NI_NOFQDN }, { 0x0002, NI_NUMERICHOST }, { 0x0004, NI_NAMEREQD },
    { 0x0008, NI_NUMERICSERV }, { 0x0010, NI_DGRAM },
}, kEai[] = {
    { 1, EAI_ADDRFAMILY }, { 2, EAI_AGAIN }, { 3, EAI_BADFLAGS }, { 4, EAI_FAIL }, { 5, EAI_FAMILY },
    { 6, EAI_MEMORY }, { 7, EAI_NODATA }, { 8, EAI_NONAME }, { 9, EAI_SERVICE }, { 10, EAI_SOCKTYPE },
    { 11, EAI_SYSTEM }, { 14, EAI_OVERFLOW },
};

static int mapFlags(int in, int toHost, const void *tablev, size_t n) {
    const struct { int bionic; int host; } *t = tablev;
    int out = 0;
    size_t i;
    for (i = 0; i < n; i++)
        if (in & (toHost ? t[i].bionic : t[i].host)) out |= toHost ? t[i].host : t[i].bionic;
    return out;
}
static int eaiToBionic(int rc) {
    size_t i;
    if (rc == 0) return 0;
    for (i = 0; i < sizeof kEai / sizeof kEai[0]; i++)
        if (kEai[i].host == rc) return kEai[i].bionic;
    return 4; /* EAI_FAIL */
}
static int eaiToHost(int rc) {
    size_t i;
    for (i = 0; i < sizeof kEai / sizeof kEai[0]; i++)
        if (kEai[i].bionic == rc) return kEai[i].host;
    return EAI_FAIL;
}
static void bw_freeaddrinfo(BAddrinfo *ai) {
    while (ai) {
        BAddrinfo *next = ai->ai_next;
        free(ai->ai_canonname);
        free(ai); /* ai_addr shares the node's allocation */
        ai = next;
    }
}
static int bw_getaddrinfo(const char *node, const char *service, const BAddrinfo *bh, BAddrinfo **res) {
    struct addrinfo hints, *gres = NULL, *g;
    BAddrinfo *head = NULL, **tail = &head;
    int rc;
    if (!res) return 3; /* EAI_BADFLAGS */
    *res = NULL;
    memset(&hints, 0, sizeof hints);
    if (bh) {
        memset(&hints, 0, sizeof hints);
        hints.ai_flags = mapFlags(bh->ai_flags, 1, kAiFlags, sizeof kAiFlags / sizeof kAiFlags[0]);
        hints.ai_family = bh->ai_family;
        hints.ai_socktype = bh->ai_socktype;
        hints.ai_protocol = bh->ai_protocol;
    }
    rc = getaddrinfo(node, service, bh ? &hints : NULL, &gres);
    if (rc != 0) return eaiToBionic(rc);
    for (g = gres; g; g = g->ai_next) {
        BAddrinfo *b = (BAddrinfo *)calloc(1, sizeof *b + g->ai_addrlen);
        if (!b) {
            freeaddrinfo(gres);
            bw_freeaddrinfo(head);
            return 6; /* EAI_MEMORY */
        }
        b->ai_flags = mapFlags(g->ai_flags, 0, kAiFlags, sizeof kAiFlags / sizeof kAiFlags[0]);
        b->ai_family = g->ai_family;
        b->ai_socktype = g->ai_socktype;
        b->ai_protocol = g->ai_protocol;
        b->ai_addrlen = g->ai_addrlen;
        if (g->ai_addr && g->ai_addrlen) {
            b->ai_addr = (struct sockaddr *)(b + 1);
            memcpy(b->ai_addr, g->ai_addr, g->ai_addrlen);
        }
        if (g->ai_canonname) b->ai_canonname = strdup(g->ai_canonname);
        *tail = b;
        tail = &b->ai_next;
    }
    freeaddrinfo(gres);
    *res = head;
    return 0;
}
static int bw_getnameinfo(const struct sockaddr *sa, socklen_t salen, char *host, socklen_t hostlen, char *serv,
                          socklen_t servlen, int flags) {
    int rc = getnameinfo(sa, salen, host, hostlen, serv, servlen,
                         mapFlags(flags, 1, kNiFlags, sizeof kNiFlags / sizeof kNiFlags[0]));
    return eaiToBionic(rc);
}
static const char *bw_gai_strerror(int code) { return gai_strerror(eaiToHost(code)); }

/* ── C++ runtime for games linked against the system libstdc++.so ─────
 * (fallbacks: a bundled libc++_shared.so always wins) */
static void *bw_operator_new(size_t n) {
    void *p = malloc(n ? n : 1);
    if (!p) apak_compatFatal("operator new(%zu): out of memory", n);
    return p;
}
static void *bw_operator_new_nothrow(size_t n, const void *tag) {
    (void)tag;
    return malloc(n ? n : 1);
}
static void *bw_operator_new_aligned(size_t n, size_t align) {
    void *p = NULL;
    if (align < sizeof(void *)) align = sizeof(void *);
    if (posix_memalign(&p, align, n ? n : 1) != 0) apak_compatFatal("aligned operator new(%zu): out of memory", n);
    return p;
}
static void bw_operator_delete(void *p) { free(p); }
static void bw_operator_delete_sized(void *p, size_t n) {
    (void)n;
    free(p);
}
static void bw_operator_delete_nothrow(void *p, const void *tag) {
    (void)tag;
    free(p);
}
static void bw_operator_delete_aligned(void *p, size_t align) {
    (void)align;
    free(p);
}

static pthread_mutex_t g_guardLock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t g_guardCond = PTHREAD_COND_INITIALIZER;
/* Itanium C++ ABI guard: byte 0 = initialised, byte 1 = in progress. */
static int bw___cxa_guard_acquire(uint64_t *guard) {
    uint8_t *g = (uint8_t *)guard;
    if (__atomic_load_n(&g[0], __ATOMIC_ACQUIRE)) return 0;
    pthread_mutex_lock(&g_guardLock);
    for (;;) {
        if (g[0]) {
            pthread_mutex_unlock(&g_guardLock);
            return 0;
        }
        if (!g[1]) {
            g[1] = 1;
            pthread_mutex_unlock(&g_guardLock);
            return 1;
        }
        pthread_cond_wait(&g_guardCond, &g_guardLock);
    }
}
static void bw___cxa_guard_release(uint64_t *guard) {
    uint8_t *g = (uint8_t *)guard;
    pthread_mutex_lock(&g_guardLock);
    g[1] = 0;
    __atomic_store_n(&g[0], 1, __ATOMIC_RELEASE);
    pthread_cond_broadcast(&g_guardCond);
    pthread_mutex_unlock(&g_guardLock);
}
static void bw___cxa_guard_abort(uint64_t *guard) {
    uint8_t *g = (uint8_t *)guard;
    pthread_mutex_lock(&g_guardLock);
    g[1] = 0;
    pthread_cond_broadcast(&g_guardCond);
    pthread_mutex_unlock(&g_guardLock);
}
static void bw___cxa_pure_virtual(void) { apak_compatFatal("pure virtual function called"); }
static void bw___cxa_deleted_virtual(void) { apak_compatFatal("deleted virtual function called"); }

/* JIT engines (LuaJIT, Mono) flush the instruction cache through this
 * compiler-runtime helper; glibc does not export it (it is static libgcc). */
static void bw___clear_cache(char *begin, char *end) { __builtin___clear_cache(begin, end); }

/* ── the tables ─────────────────────────────────────────────────────── */

#define SYM(name, fn) { name, (void *)(fn) }

const ApakSym apak_libcOverrides[] = {
    SYM("__errno", bw___errno),
    SYM("__set_errno", bw___set_errno),
    SYM("__get_h_errno", bw___get_h_errno),
    /* stdio + __sF */
    SYM("__sF", g_sF),
    SYM("fclose", bw_fclose), SYM("fflush", bw_fflush), SYM("fputc", bw_fputc), SYM("putc", bw_putc),
    SYM("putc_unlocked", bw_putc_unlocked), SYM("fputs", bw_fputs), SYM("fwrite", bw_fwrite),
    SYM("fwrite_unlocked", bw_fwrite_unlocked), SYM("fprintf", bw_fprintf), SYM("vfprintf", bw_vfprintf),
    SYM("fscanf", bw_fscanf), SYM("vfscanf", bw_vfscanf), SYM("fgetc", bw_fgetc), SYM("getc", bw_getc),
    SYM("getc_unlocked", bw_getc_unlocked), SYM("fgets", bw_fgets), SYM("fread", bw_fread),
    SYM("fread_unlocked", bw_fread_unlocked), SYM("ungetc", bw_ungetc), SYM("fseek", bw_fseek),
    SYM("fseeko", bw_fseeko), SYM("fseeko64", bw_fseeko), SYM("ftell", bw_ftell), SYM("ftello", bw_ftello),
    SYM("ftello64", bw_ftello), SYM("rewind", bw_rewind), SYM("fgetpos", bw_fgetpos),
    SYM("fsetpos", bw_fsetpos), SYM("fgetpos64", bw_fgetpos), SYM("fsetpos64", bw_fsetpos),
    SYM("feof", bw_feof), SYM("ferror", bw_ferror), SYM("clearerr", bw_clearerr),
    SYM("feof_unlocked", bw_feof_unlocked), SYM("ferror_unlocked", bw_ferror_unlocked),
    SYM("clearerr_unlocked", bw_clearerr_unlocked), SYM("fileno", bw_fileno),
    SYM("fileno_unlocked", bw_fileno_unlocked), SYM("setvbuf", bw_setvbuf), SYM("setbuf", bw_setbuf),
    SYM("setbuffer", bw_setbuffer), SYM("setlinebuf", bw_setlinebuf), SYM("freopen", bw_freopen),
    SYM("freopen64", bw_freopen), SYM("flockfile", bw_flockfile), SYM("funlockfile", bw_funlockfile),
    SYM("ftrylockfile", bw_ftrylockfile), SYM("getline", bw_getline), SYM("getdelim", bw_getdelim),
    SYM("fpurge", bw_fpurge), SYM("__fpurge", bw___fpurge), SYM("__fbufsize", bw___fbufsize),
    SYM("__freading", bw___freading), SYM("__fwriting", bw___fwriting), SYM("__freadable", bw___freadable),
    SYM("__fwritable", bw___fwritable), SYM("__flbf", bw___flbf), SYM("__fpending", bw___fpending),
    SYM("__fsetlocking", bw___fsetlocking), SYM("fgetwc", bw_fgetwc), SYM("getwc", bw_getwc),
    SYM("fputwc", bw_fputwc), SYM("putwc", bw_putwc), SYM("ungetwc", bw_ungetwc), SYM("fputws", bw_fputws),
    SYM("fgetws", bw_fgetws), SYM("fwide", bw_fwide), SYM("fwprintf", bw_fwprintf),
    SYM("vfwprintf", bw_vfwprintf), SYM("__fgets_chk", bw___fgets_chk), SYM("__fread_chk", bw___fread_chk),
    SYM("__fwrite_chk", bw___fwrite_chk), SYM("funopen", bw_funopen), SYM("funopen64", bw_funopen),
    /* ctype tables */
    SYM("_ctype_", g_ctype), SYM("_tolower_tab_", g_tolower), SYM("_toupper_tab_", g_toupper),
    /* constants that differ */
    SYM("sysconf", bw_sysconf), SYM("getpagesize", bw_getpagesize),
    SYM("strerror_r", bw_strerror_r), SYM("__gnu_strerror_r", bw___gnu_strerror_r),
    SYM("basename", bw_basename), SYM("dirname", bw_dirname), SYM("__gnu_basename", bw___gnu_basename),
    /* FORTIFY */
    SYM("__strlen_chk", bw___strlen_chk), SYM("__strchr_chk", bw___strchr_chk),
    SYM("__strrchr_chk", bw___strrchr_chk), SYM("__strlcpy_chk", bw___strlcpy_chk),
    SYM("__strlcat_chk", bw___strlcat_chk), SYM("__strncpy_chk2", bw___strncpy_chk2),
    SYM("__stpncpy_chk2", bw___stpncpy_chk2), SYM("__memchr_chk", bw___memchr_chk),
    SYM("__memrchr_chk", bw___memrchr_chk), SYM("__umask_chk", bw___umask_chk),
    SYM("__FD_ISSET_chk", bw___FD_ISSET_chk), SYM("__FD_CLR_chk", bw___FD_CLR_chk),
    SYM("__FD_SET_chk", bw___FD_SET_chk), SYM("__poll_chk", bw___poll_chk),
    SYM("__pread_chk", bw___pread_chk), SYM("__pread64_chk", bw___pread_chk),
    SYM("__pwrite_chk", bw___pwrite_chk), SYM("__pwrite64_chk", bw___pwrite_chk),
    SYM("__read_chk", bw___read_chk), SYM("__write_chk", bw___write_chk),
    SYM("__readlink_chk", bw___readlink_chk), SYM("__readlinkat_chk", bw___readlinkat_chk),
    SYM("__recvfrom_chk", bw___recvfrom_chk), SYM("__sendto_chk", bw___sendto_chk),
    SYM("__getcwd_chk", bw___getcwd_chk), SYM("__open_2", bw___open_2), SYM("__openat_2", bw___openat_2),
    /* stat family (glibc < 2.33 has no exported stat) */
    SYM("stat", bw_stat), SYM("stat64", bw_stat), SYM("lstat", bw_lstat), SYM("lstat64", bw_lstat),
    SYM("fstat", bw_fstat), SYM("fstat64", bw_fstat), SYM("fstatat", bw_fstatat),
    SYM("fstatat64", bw_fstatat), SYM("mknod", bw_mknod), SYM("mknodat", bw_mknodat),
    /* libc_nonshared-only in glibc */
    SYM("atexit", bw_atexit), SYM("pthread_atfork", bw_pthread_atfork),
    /* stack protector */
    SYM("__stack_chk_guard", &g_stackChkGuard), SYM("__stack_chk_fail", bw___stack_chk_fail),
    /* bionic FP classes */
    SYM("__fpclassify", bw___fpclassifyd), SYM("__fpclassifyd", bw___fpclassifyd),
    SYM("__fpclassifyf", bw___fpclassifyf), SYM("__fpclassifyl", bw___fpclassifyl),
    /* netdb */
    SYM("getaddrinfo", bw_getaddrinfo), SYM("freeaddrinfo", bw_freeaddrinfo),
    SYM("getnameinfo", bw_getnameinfo), SYM("gai_strerror", bw_gai_strerror),
    /* assert / abort plumbing */
    SYM("__assert", bw___assert), SYM("__assert2", bw___assert2),
    SYM("android_set_abort_message", bw_android_set_abort_message),
    { NULL, NULL },
};

const ApakSym apak_libcFallbacks[] = {
    SYM("strlcpy", bw_strlcpy), SYM("strlcat", bw_strlcat),
    SYM("arc4random", bw_arc4random), SYM("arc4random_buf", bw_arc4random_buf),
    SYM("arc4random_uniform", bw_arc4random_uniform), SYM("getrandom", bw_getrandom),
    SYM("getentropy", bw_getentropy), SYM("gettid", bw_gettid), SYM("tgkill", bw_tgkill),
    SYM("tkill", bw_tkill), SYM("getprogname", bw_getprogname), SYM("reallocarray", bw_reallocarray),
    SYM("memfd_create", bw_memfd_create),
    SYM("android_get_application_target_sdk_version", bw_android_get_application_target_sdk_version),
    SYM("android_get_device_api_level", bw_android_get_device_api_level),
    SYM("__isthreaded", &g_isthreaded), SYM("__page_size", &g_pageSizeVar), SYM("__page_shift", &g_pageShift),
    SYM("android_fdsan_create_owner_tag", bw_android_fdsan_create_owner_tag),
    SYM("android_fdsan_exchange_owner_tag", bw_android_fdsan_exchange_owner_tag),
    SYM("android_fdsan_close_with_tag", bw_android_fdsan_close_with_tag),
    SYM("android_fdsan_get_owner_tag", bw_android_fdsan_get_owner_tag),
    SYM("android_fdsan_get_tag_type", bw_android_fdsan_get_tag_type),
    SYM("android_fdsan_get_tag_value", bw_android_fdsan_get_tag_value),
    SYM("android_fdsan_get_error_level", bw_android_fdsan_get_error_level),
    SYM("android_fdsan_set_error_level", bw_android_fdsan_set_error_level),
    SYM("__isfinite", bw___isfinite), SYM("__isfinitef", bw___isfinitef), SYM("__isfinitel", bw___isfinitel),
    SYM("__isnormal", bw___isnormal), SYM("__isnormalf", bw___isnormalf), SYM("__isnormall", bw___isnormall),
    SYM("__isinfl", bw___isinfl), SYM("__isnanl", bw___isnanl),
    /* operator new/delete & friends (system libstdc++.so) */
    SYM("_Znwm", bw_operator_new), SYM("_Znam", bw_operator_new),
    SYM("_ZnwmRKSt9nothrow_t", bw_operator_new_nothrow), SYM("_ZnamRKSt9nothrow_t", bw_operator_new_nothrow),
    SYM("_ZnwmSt11align_val_t", bw_operator_new_aligned), SYM("_ZnamSt11align_val_t", bw_operator_new_aligned),
    SYM("_ZdlPv", bw_operator_delete), SYM("_ZdaPv", bw_operator_delete),
    SYM("_ZdlPvm", bw_operator_delete_sized), SYM("_ZdaPvm", bw_operator_delete_sized),
    SYM("_ZdlPvRKSt9nothrow_t", bw_operator_delete_nothrow), SYM("_ZdaPvRKSt9nothrow_t", bw_operator_delete_nothrow),
    SYM("_ZdlPvSt11align_val_t", bw_operator_delete_aligned), SYM("_ZdaPvSt11align_val_t", bw_operator_delete_aligned),
    SYM("__cxa_guard_acquire", bw___cxa_guard_acquire), SYM("__cxa_guard_release", bw___cxa_guard_release),
    SYM("__cxa_guard_abort", bw___cxa_guard_abort), SYM("__cxa_pure_virtual", bw___cxa_pure_virtual),
    SYM("__cxa_deleted_virtual", bw___cxa_deleted_virtual), SYM("__clear_cache", bw___clear_cache),
    { NULL, NULL },
};

/* ── table plumbing ─────────────────────────────────────────────────── */

static ApakSym *g_over, *g_fall;
static size_t g_nover, g_nfall;
static const ApakSym *g_extra[8];
static int g_nextra;
static pthread_mutex_t g_tableLock = PTHREAD_MUTEX_INITIALIZER;
static int g_tablesBuilt;

static int symCmp(const void *a, const void *b) {
    return strcmp(((const ApakSym *)a)->name, ((const ApakSym *)b)->name);
}

static ApakSym *mergeTables(const ApakSym *const *tables, int count, size_t *outN) {
    size_t total = 0, k = 0, i, w;
    ApakSym *all;
    int t;
    for (t = 0; t < count; t++)
        for (i = 0; tables[t] && tables[t][i].name; i++) total++;
    all = (ApakSym *)malloc((total ? total : 1) * sizeof *all);
    if (!all) apak_compatFatal("out of memory building the compat tables");
    for (t = 0; t < count; t++)
        for (i = 0; tables[t] && tables[t][i].name; i++) all[k++] = tables[t][i];
    qsort(all, total, sizeof *all, symCmp);
    /* drop duplicate names (a table bug; keep the first after sorting) */
    for (i = 0, w = 0; i < total; i++) {
        if (w && strcmp(all[w - 1].name, all[i].name) == 0) {
            if (getenv("ANDROIDPAK_DEBUG")) fprintf(stderr, CT_TAG "compat: duplicate entry %s\n", all[i].name);
            continue;
        }
        all[w++] = all[i];
    }
    *outN = w;
    return all;
}

static void buildTablesLocked(void) {
    const ApakSym *over[3 + 8];
    const ApakSym *fall[3];
    int n = 0, i;
    over[n++] = apak_libcOverrides;
    over[n++] = apak_pthreadOverrides;
    over[n++] = apak_dlOverrides;
    for (i = 0; i < g_nextra; i++) over[n++] = g_extra[i];
    fall[0] = apak_libcFallbacks;
    fall[1] = apak_pthreadFallbacks;
    fall[2] = apak_propFallbacks;
    free(g_over);
    free(g_fall);
    g_over = mergeTables(over, n, &g_nover);
    g_fall = mergeTables(fall, 3, &g_nfall);
}

void apak_compatInit(void) {
    pthread_mutex_lock(&g_tableLock);
    if (!g_tablesBuilt) {
        ctypeInit();
        sFInit();
        g_pageSizeVar = (unsigned)sysconf(_SC_PAGESIZE);
        g_pageShift = (unsigned)__builtin_ctz(g_pageSizeVar);
        apak_propsInit();
        buildTablesLocked();
        g_tablesBuilt = 1;
    }
    pthread_mutex_unlock(&g_tableLock);
}

void apak_compatAddOverrides(const ApakSym *table) {
    pthread_mutex_lock(&g_tableLock);
    if (g_nextra < (int)(sizeof g_extra / sizeof g_extra[0])) g_extra[g_nextra++] = table;
    if (g_tablesBuilt) buildTablesLocked();
    pthread_mutex_unlock(&g_tableLock);
}

static void *findIn(const ApakSym *tab, size_t n, const char *name) {
    ApakSym key;
    const ApakSym *hit;
    if (!tab || !n) return NULL;
    key.name = name;
    key.addr = NULL;
    hit = (const ApakSym *)bsearch(&key, tab, n, sizeof key, symCmp);
    return hit ? hit->addr : NULL;
}

void *apak_compatOverride(const char *name) { return findIn(g_over, g_nover, name); }
void *apak_compatFallback(const char *name) { return findIn(g_fall, g_nfall, name); }
size_t apak_compatOverrideCount(void) { return g_nover; }
size_t apak_compatFallbackCount(void) { return g_nfall; }
