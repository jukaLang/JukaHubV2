/* bioniccompat.h — the runner-internal API of the bionic compatibility
 * layer (Milestone 2, "Fill").
 *
 * A bionic library's imports are bound by the runner's own loader
 * (bionicld.c), never by glibc's ld.so, through this order:
 *
 *   1. OVERRIDE table — names glibc also exports but with a different ABI
 *      for a bionic caller (pthread_mutex_t is 40 bytes on bionic arm64 and
 *      48 on glibc; struct sigaction, sigset_t, jmp_buf, FILE/__sF, fpos_t,
 *      struct addrinfo, sysconf constants, FP_* values, the libdl family ...)
 *      plus the bionic-only names whose glibc twin must never win;
 *   2. the game's own bundled libraries (the lib/<abi> directory), load order;
 *   3. FALLBACK table — bionic-only names glibc may lack (strlcpy, arc4random,
 *      __system_property_*, gettid, operator new for the old system
 *      libstdc++.so, ...): only used when no bundled library defines them;
 *   4. the host: libraries opened for a game's DT_NEEDED (libEGL.so.1,
 *      libGLESv2.so.2, libz.so.1) and finally dlsym(RTLD_DEFAULT) — glibc and
 *      the runner's own exported Android ABI (liblog/libandroid/OpenSL ES).
 *
 * Every wrapper is static to its file; only the tables are shared.
 */
#ifndef ANDROIDRT_BIONICCOMPAT_H
#define ANDROIDRT_BIONICCOMPAT_H

#include <stddef.h>
#include <stdint.h>

typedef struct ApakSym {
    const char *name;
    void *addr;
} ApakSym;

/* NULL-terminated contributions, one per source file. */
extern const ApakSym apak_libcOverrides[];   /* bioniccompat.c  */
extern const ApakSym apak_libcFallbacks[];   /* bioniccompat.c  */
extern const ApakSym apak_pthreadOverrides[]; /* bionicpthread.c */
extern const ApakSym apak_pthreadFallbacks[]; /* bionicpthread.c */
extern const ApakSym apak_dlOverrides[];     /* bionicld.c      */
extern const ApakSym apak_propFallbacks[];   /* sysprops.c      */

/* Build the sorted lookup tables (idempotent). */
void apak_compatInit(void);
/* Extra overrides registered before the first library loads (the EGL
 * present layer adds its eglGetDisplay/eglSwapBuffers/... wrappers). */
void apak_compatAddOverrides(const ApakSym *table);
void *apak_compatOverride(const char *name);
void *apak_compatFallback(const char *name);
size_t apak_compatOverrideCount(void);
size_t apak_compatFallbackCount(void);

/* ── per-thread state ────────────────────────────────────────────────
 * The runner's ONLY __thread object (keep it that way: see bioniccompat.c).
 * On aarch64 the executable's static TLS block starts at tpidr_el0 + 16, so
 * slots[] overlays bionic TLS slots 2..7 and slots[3] is bionic's
 * TLS_SLOT_STACK_GUARD at tpidr_el0 + 0x28 — the stack-protector canary
 * every NDK clang build reads in its function prologue/epilogue. */
typedef struct ApakTls {
    uintptr_t slots[6];
    void *cleanupHead;          /* bionic __pthread_cleanup_push chain    */
    int dlerrorSet;
    char dlerror[512];
    char pathBuf[1024];         /* POSIX basename()/dirname() result      */
} ApakTls;

extern __thread ApakTls apak_tls;

#define APAK_STACK_GUARD ((uintptr_t)0x00a5c0ffee5a7e00ull)

/* aarch64: 1 when apak_tls.slots[3] really is tpidr_el0 + 0x28; elsewhere
 * always 1 (x86_64 bionic and glibc share %fs:0x28). */
int apak_tlsSlotCheck(void);

/* ── sysprops.c ──────────────────────────────────────────────────────── */
void apak_propsInit(void);
int apak_propGet(const char *name, char *value, size_t cap);

/* ── bioniccompat.c helpers other files use ─────────────────────────── */
/* Translate a bionic FILE* (possibly &__sF[n]) into glibc's. */
void *apak_bionicFile(void *file);
/* Fatal FORTIFY-style failure: prints and aborts. */
void apak_compatFatal(const char *fmt, ...) __attribute__((noreturn, format(printf, 1, 2)));

/* bionic LP64 sigset_t is one unsigned long; glibc's is 128 bytes. */
typedef unsigned long ApakBSigset;
void apak_bsetToHost(const ApakBSigset *in, void *hostSigset);
ApakBSigset apak_hostToBset(const void *hostSigset);

/* Bounded string copy that always terminates (truncation is intended). */
static inline void apak_strcpy(char *dst, size_t cap, const char *src) {
    size_t n = 0;
    if (!cap) return;
    if (src)
        while (n + 1 < cap && src[n]) n++;
    for (size_t i = 0; i < n; i++) dst[i] = src[i];
    dst[n] = '\0';
}

#endif /* ANDROIDRT_BIONICCOMPAT_H */
