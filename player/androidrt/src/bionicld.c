/* bionicld.c — see bionicld.h.
 *
 * Load order for one apak_ldOpen():
 *   1. scan  — read the root and every bundled DT_NEEDED (breadth first)
 *              through elfdyn.c, classify system names (virtual providers);
 *   2. map   — reserve the whole span, mmap each PT_LOAD at its offset;
 *   3. bind  — apply every relocation table, resolving symbols through the
 *              compat resolver; unresolved strong imports get traps;
 *   4. report— one block listing every undefined symbol of the batch;
 *   5. init  — DT_INIT + DT_INIT_ARRAY, dependencies first, with
 *              (argc, argv, envp) like bionic.
 * Libraries are never unmapped (dlclose only drops a reference), which is
 * what bionic effectively does for app libraries too.
 */
#define _GNU_SOURCE
#include <dirent.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <link.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/auxv.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#include "bioniccompat.h"
#include "bionicld.h"
#include "elfdyn.h"

#define LD_TAG "[androidrt] "

#if defined(__aarch64__)
#define APAK_HOST_MACHINE APAK_EM_AARCH64
#define RT_NONE       0u
#define RT_ABS64      257u
#define RT_COPY       1024u
#define RT_GLOB_DAT   1025u
#define RT_JUMP_SLOT  1026u
#define RT_RELATIVE   1027u
#define RT_TLS_DTPMOD 1028u
#define RT_TLS_DTPREL 1029u
#define RT_TLS_TPREL  1030u
#define RT_TLSDESC    1031u
#define RT_IRELATIVE  1032u
#elif defined(__x86_64__)
#define APAK_HOST_MACHINE APAK_EM_X86_64
#define RT_NONE       0u
#define RT_ABS64      1u
#define RT_COPY       5u
#define RT_GLOB_DAT   6u
#define RT_JUMP_SLOT  7u
#define RT_RELATIVE   8u
#define RT_TLS_DTPMOD 16u
#define RT_TLS_DTPREL 17u
#define RT_TLS_TPREL  18u
#define RT_TLSDESC    36u
#define RT_IRELATIVE  37u
#else
#error "bionicld.c supports aarch64 (the device) and x86_64 (host smoke tests)"
#endif

/* ── objects ─────────────────────────────────────────────────────────── */

enum { OBJ_BUNDLED = 1, OBJ_VIRTUAL = 2, OBJ_SELF = 3 };
enum { VK_NONE = 0, VK_BIONIC, VK_RUNNER, VK_HOST, VK_MISSING };

#define APAK_OBJ_MAGIC 0x4b415041u /* "APAK" */

typedef struct ApakObj ApakObj;
struct ApakObj {
    uint32_t magic;
    int kind;
    int vkind;
    char name[256];          /* DT_SONAME, else the file name            */
    char path[PATH_MAX];
    ApakElf elf;             /* file view, kept mapped for the lifetime  */
    void *fileMap;
    size_t fileSize;
    uint8_t *mapStart;
    size_t mapSize;
    uintptr_t bias;
    const void *phdrMem;
    ApakObj **deps;          /* every DT_NEEDED, bundled or virtual      */
    size_t ndeps;
    int scanned;             /* deps resolved                             */
    int mapped;
    int relocated;
    int dryDone;             /* reported by a --scan pass                 */
    unsigned batchGen;       /* last batch this object joined             */
    int initState;           /* 0 none, 1 running, 2 done                 */
    int refs;
    void *host;              /* VK_HOST: the host dlopen handle           */
    char hostName[PATH_MAX];
    ApakObj *next;
};

typedef struct {
    const char *name;
    int vkind;
    const char *host;
    const char *hostEnv;
} VirtualLib;

/* System libraries a game names in DT_NEEDED. libc/libm/libdl/libstdc++ are
 * the compat layer plus glibc; liblog/libandroid/OpenSL ES are the runner's
 * own exported implementations (one copy of their state); EGL/GLES/zlib are
 * the device's glibc userspace. */
static const VirtualLib kVirtual[] = {
    { "libc.so", VK_BIONIC, NULL, NULL },
    /* -lm may be dropped by --as-needed when the runner itself has no math
     * calls. Resolve an APK's libm imports from the firmware's libm explicitly. */
    { "libm.so", VK_HOST, "libm.so.6", NULL },
    { "libdl.so", VK_BIONIC, NULL, NULL },
    { "libstdc++.so", VK_BIONIC, NULL, NULL },
    { "liblog.so", VK_RUNNER, NULL, NULL },
    { "libandroid.so", VK_RUNNER, NULL, NULL },
    { "libOpenSLES.so", VK_RUNNER, NULL, NULL },
    { "libjnigraphics.so", VK_RUNNER, NULL, NULL },
    { "libnativewindow.so", VK_RUNNER, NULL, NULL },
    { "libEGL.so", VK_HOST, "libEGL.so.1", "ANDROIDPAK_EGL_LIB" },
    { "libGLESv1_CM.so", VK_HOST, "libGLESv1_CM.so.1", "ANDROIDPAK_GLES1_LIB" },
    { "libGLESv2.so", VK_HOST, "libGLESv2.so.2", "ANDROIDPAK_GLES2_LIB" },
    { "libGLESv3.so", VK_HOST, "libGLESv2.so.2", "ANDROIDPAK_GLES2_LIB" },
    { "libz.so", VK_HOST, "libz.so.1", NULL },
    { "libvulkan.so", VK_HOST, "libvulkan.so.1", NULL },
};

static pthread_mutex_t g_ldLock = PTHREAD_RECURSIVE_MUTEX_INITIALIZER_NP;
static ApakObj *g_objs, *g_objsTail;
static ApakObj g_self = { .magic = APAK_OBJ_MAGIC, .kind = OBJ_SELF, .name = "(main program)" };
static unsigned g_batchGen;
static char g_libDirs[8][PATH_MAX];
static int g_nLibDirs;
static int g_policy = APAK_MISSING_TRAP;
static char g_err[768];
static int g_argc;
static char **g_argv;
static unsigned long long g_adds;
static size_t g_pageSize;

/* bind statistics of the current batch, for the report */
static struct {
    size_t imports, override, bundled, fallback, host, weakNull, missing;
} g_stat;

/* ── errors ──────────────────────────────────────────────────────────── */

static void setErr(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
static void setErr(const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(g_err, sizeof g_err, fmt, ap);
    va_end(ap);
    apak_strcpy(apak_tls.dlerror, sizeof apak_tls.dlerror, g_err);
    apak_tls.dlerrorSet = 1;
}

const char *apak_ldLastError(void) { return g_err[0] ? g_err : "no error"; }

static const char *baseName(const char *path) {
    const char *slash = strrchr(path, '/');
    return slash ? slash + 1 : path;
}

void apak_ldConfigure(const char *libDirs, int missingPolicy) {
    pthread_mutex_lock(&g_ldLock);
    g_policy = missingPolicy;
    g_nLibDirs = 0;
    if (libDirs) {
        const char *p = libDirs;
        while (*p && g_nLibDirs < (int)(sizeof g_libDirs / sizeof g_libDirs[0])) {
            const char *colon = strchr(p, ':');
            size_t len = colon ? (size_t)(colon - p) : strlen(p);
            if (len && len < PATH_MAX) {
                memcpy(g_libDirs[g_nLibDirs], p, len);
                g_libDirs[g_nLibDirs][len] = '\0';
                g_nLibDirs++;
            }
            if (!colon) break;
            p = colon + 1;
        }
    }
    pthread_mutex_unlock(&g_ldLock);
}

void apak_ldSetArgs(int argc, char **argv) {
    g_argc = argc;
    g_argv = argv;
}

/* ── trap stubs for missing functions ───────────────────────────────── */

typedef struct {
    char name[160];
    char needer[96];
    char from[96];
    char version[48];
    int type;
    int trap;
    int warned;
} ApakMissing;

static ApakMissing *g_missing;
static size_t g_nmissing, g_capMissing;
static size_t g_batchMissingStart;

/* Slots 0..510 each name one missing function; slot 511 is shared by every
 * function past that (the report still lists them all by name). */
#define APAK_TRAPS 512
static int g_trapMissing[APAK_TRAPS];
static int g_trapsUsed;
static size_t g_trapOverflow;

static long apak_trapHit(int slot) {
    ApakMissing *m = NULL;
    if (slot >= 0 && slot < g_trapsUsed && slot < APAK_TRAPS - 1) m = &g_missing[g_trapMissing[slot]];
    if (g_policy == APAK_MISSING_ZERO) {
        if (m && !m->warned) {
            m->warned = 1;
            fprintf(stderr, LD_TAG "trap: %s called unimplemented %s — returning 0 (ANDROIDPAK_MISSING=zero)\n",
                    m->needer, m->name);
        }
        return 0;
    }
    if (!m)
        fprintf(stderr, LD_TAG "trap: the game called one of %zu unimplemented functions past the named-trap table — aborting\n",
                g_trapOverflow);
    else
        fprintf(stderr, LD_TAG "trap: %s called unimplemented %s — aborting (ANDROIDPAK_MISSING=zero returns 0 instead)\n",
                m->needer, m->name);
    fflush(stderr);
    abort();
}

#define APAK_TRAP_FN(id, idx) \
    static long apak_trap_##id(void) { return apak_trapHit(idx); }
#define APAK_TRAP8(p, b) \
    APAK_TRAP_FN(p##0, (b) + 0) APAK_TRAP_FN(p##1, (b) + 1) APAK_TRAP_FN(p##2, (b) + 2) \
    APAK_TRAP_FN(p##3, (b) + 3) APAK_TRAP_FN(p##4, (b) + 4) APAK_TRAP_FN(p##5, (b) + 5) \
    APAK_TRAP_FN(p##6, (b) + 6) APAK_TRAP_FN(p##7, (b) + 7)
#define APAK_TRAP64(p, b) \
    APAK_TRAP8(p##0, (b) + 0) APAK_TRAP8(p##1, (b) + 8) APAK_TRAP8(p##2, (b) + 16) \
    APAK_TRAP8(p##3, (b) + 24) APAK_TRAP8(p##4, (b) + 32) APAK_TRAP8(p##5, (b) + 40) \
    APAK_TRAP8(p##6, (b) + 48) APAK_TRAP8(p##7, (b) + 56)
APAK_TRAP64(t0, 0)
APAK_TRAP64(t1, 64)
APAK_TRAP64(t2, 128)
APAK_TRAP64(t3, 192)
APAK_TRAP64(t4, 256)
APAK_TRAP64(t5, 320)
APAK_TRAP64(t6, 384)
APAK_TRAP64(t7, 448)

#define APAK_REF8(p) \
    apak_trap_##p##0, apak_trap_##p##1, apak_trap_##p##2, apak_trap_##p##3, \
    apak_trap_##p##4, apak_trap_##p##5, apak_trap_##p##6, apak_trap_##p##7
#define APAK_REF64(p) \
    APAK_REF8(p##0), APAK_REF8(p##1), APAK_REF8(p##2), APAK_REF8(p##3), \
    APAK_REF8(p##4), APAK_REF8(p##5), APAK_REF8(p##6), APAK_REF8(p##7)

static long (*const kTraps[APAK_TRAPS])(void) = {
    APAK_REF64(t0), APAK_REF64(t1), APAK_REF64(t2), APAK_REF64(t3),
    APAK_REF64(t4), APAK_REF64(t5), APAK_REF64(t6), APAK_REF64(t7),
};

/* Zero-filled scratch handed to missing DATA symbols (reads see 0, writes
 * land nowhere that matters). */
static unsigned char *g_scratch;
static size_t g_scratchUsed;
#define APAK_SCRATCH_BYTES (1u << 20)

static void *missingData(size_t size) {
    void *p;
    if (size < 256) size = 256;
    if (size > 65536) size = 65536;
    size = (size + 15) & ~(size_t)15;
    if (!g_scratch) {
        void *m = mmap(NULL, APAK_SCRATCH_BYTES, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
        if (m == MAP_FAILED) return NULL;
        g_scratch = (unsigned char *)m;
    }
    if (g_scratchUsed + size > APAK_SCRATCH_BYTES) return g_scratch; /* share the first block */
    p = g_scratch + g_scratchUsed;
    g_scratchUsed += size;
    return p;
}

static ApakMissing *recordMissing(const char *name, const char *needer, const char *from,
                                  const char *version, int type) {
    size_t i;
    ApakMissing *m;
    for (i = 0; i < g_nmissing; i++)
        if (strcmp(g_missing[i].name, name) == 0 && strcmp(g_missing[i].needer, needer) == 0)
            return &g_missing[i];
    if (g_nmissing == g_capMissing) {
        size_t cap = g_capMissing ? g_capMissing * 2 : 64;
        ApakMissing *grown = (ApakMissing *)realloc(g_missing, cap * sizeof *grown);
        if (!grown) return NULL;
        g_missing = grown;
        g_capMissing = cap;
    }
    m = &g_missing[g_nmissing];
    memset(m, 0, sizeof *m);
    apak_strcpy(m->name, sizeof m->name, name);
    apak_strcpy(m->needer, sizeof m->needer, needer);
    apak_strcpy(m->from, sizeof m->from, from);
    apak_strcpy(m->version, sizeof m->version, version);
    m->type = type;
    m->trap = -1;
    g_nmissing++;
    return m;
}

size_t apak_ldMissingCount(void) { return g_nmissing; }

/* ── object bookkeeping ─────────────────────────────────────────────── */

static void objAppend(ApakObj *o) {
    o->next = NULL;
    if (g_objsTail) g_objsTail->next = o;
    else g_objs = o;
    g_objsTail = o;
}

static ApakObj *objFind(const char *name) {
    const char *base = baseName(name);
    ApakObj *o;
    for (o = g_objs; o; o = o->next) {
        if (strcmp(o->name, base) == 0) return o;
        if (o->kind == OBJ_BUNDLED && strcmp(baseName(o->path), base) == 0) return o;
        if (o->kind == OBJ_BUNDLED && strchr(name, '/') && strcmp(o->path, name) == 0) return o;
    }
    return NULL;
}

static int isValidObj(const void *handle) {
    const ApakObj *o;
    if (!handle || handle == RTLD_NEXT) return 0;
    if (handle == &g_self) return 1;
    pthread_mutex_lock(&g_ldLock);
    for (o = g_objs; o; o = o->next) {
        if (o == handle) {
            pthread_mutex_unlock(&g_ldLock);
            return 1;
        }
    }
    pthread_mutex_unlock(&g_ldLock);
    return 0;
}

static int regularFile(const char *path) {
    struct stat st;
    return stat(path, &st) == 0 && S_ISREG(st.st_mode);
}

static int locateBundled(const char *name, char *out, size_t cap) {
    int i;
    if (strchr(name, '/')) {
        if (regularFile(name) && snprintf(out, cap, "%s", name) < (int)cap) return 1;
        name = baseName(name); /* an Android path (/data/app/...) that does not exist here */
    }
    for (i = 0; i < g_nLibDirs; i++) {
        int n = snprintf(out, cap, "%s/%s", g_libDirs[i], name);
        if (n < 0 || (size_t)n >= cap) continue; /* a cut path could name another file */
        if (regularFile(out)) return 1;
    }
    return 0;
}

static const VirtualLib *virtualFor(const char *name) {
    size_t i;
    const char *base = baseName(name);
    for (i = 0; i < sizeof kVirtual / sizeof kVirtual[0]; i++)
        if (strcmp(kVirtual[i].name, base) == 0) return &kVirtual[i];
    return NULL;
}

/* The core Android system names are always virtual: a copy inside an APK
 * would be a bionic system library that cannot work here anyway. Only
 * zlib/vulkan may be bundled by a game and then win over the host. */
static int coreVirtual(const VirtualLib *v) {
    if (!v) return 0;
    return strcmp(v->name, "libz.so") != 0 && strcmp(v->name, "libvulkan.so") != 0;
}

/* Host libraries chosen by another layer (the EGL present layer binds the
 * game's libEGL.so/libGLESv2.so to the exact stack it drives). */
static struct {
    char name[64];
    void *handle;
    char label[PATH_MAX];
} g_hostLibs[8];
static int g_nhostLibs;

void apak_ldSetHostLib(const char *virtualName, void *handle, const char *label) {
    int i;
    if (!virtualName || !handle) return;
    pthread_mutex_lock(&g_ldLock);
    for (i = 0; i < g_nhostLibs; i++)
        if (strcmp(g_hostLibs[i].name, virtualName) == 0) break;
    if (i == g_nhostLibs && g_nhostLibs < (int)(sizeof g_hostLibs / sizeof g_hostLibs[0])) g_nhostLibs++;
    if (i < g_nhostLibs) {
        apak_strcpy(g_hostLibs[i].name, sizeof g_hostLibs[i].name, virtualName);
        g_hostLibs[i].handle = handle;
        apak_strcpy(g_hostLibs[i].label, sizeof g_hostLibs[i].label, label ? label : virtualName);
    }
    pthread_mutex_unlock(&g_ldLock);
}

static void *registeredHostLib(const char *virtualName, char *label, size_t cap) {
    int i;
    for (i = 0; i < g_nhostLibs; i++)
        if (strcmp(g_hostLibs[i].name, virtualName) == 0) {
            apak_strcpy(label, cap, g_hostLibs[i].label);
            return g_hostLibs[i].handle;
        }
    return NULL;
}

static ApakObj *newVirtual(const char *name, const VirtualLib *v) {
    ApakObj *o = (ApakObj *)calloc(1, sizeof *o);
    if (!o) return NULL;
    o->magic = APAK_OBJ_MAGIC;
    o->kind = OBJ_VIRTUAL;
    o->scanned = 1;
    snprintf(o->name, sizeof o->name, "%s", baseName(name));
    if (v) {
        o->vkind = v->vkind;
        if (v->vkind == VK_HOST && (o->host = registeredHostLib(v->name, o->hostName, sizeof o->hostName)) != NULL) {
            /* bound to the present layer's stack */
        } else if (v->vkind == VK_HOST) {
            const char *host = v->hostEnv ? getenv(v->hostEnv) : NULL;
            if (!host || !*host) host = v->host;
            snprintf(o->hostName, sizeof o->hostName, "%s", host);
            o->host = dlopen(host, RTLD_NOW | RTLD_LOCAL);
            if (!o->host && strchr(host, '.') && !getenv(v->hostEnv ? v->hostEnv : "")) {
                /* stock firmwares ship unversioned names (/usr/lib/libGLESv2.so) */
                char plain[PATH_MAX];
                snprintf(plain, sizeof plain, "%s", v->name);
                o->host = dlopen(plain, RTLD_NOW | RTLD_LOCAL);
                if (o->host) snprintf(o->hostName, sizeof o->hostName, "%s", plain);
            }
            if (!o->host) {
                const char *why = dlerror();
                o->vkind = VK_MISSING;
                snprintf(o->path, sizeof o->path, "host %s not loadable: %s", host, why ? why : "?");
            }
        }
    } else {
        /* Not an Android system name and not bundled: maybe a host library
         * (libc.so.6, libasound.so.2 ... for glibc-linked helper libs). */
        snprintf(o->hostName, sizeof o->hostName, "%s", name);
        o->host = dlopen(name, RTLD_NOW | RTLD_LOCAL);
        o->vkind = o->host ? VK_HOST : VK_MISSING;
        if (!o->host) snprintf(o->path, sizeof o->path, "not bundled and not a system library this runner provides");
    }
    objAppend(o);
    return o;
}

static int fileNeedsGlibc(const ApakElf *e) {
    size_t i, n = apak_elfNeededCount(e);
    for (i = 0; i < n; i++) {
        const char *need = apak_elfNeeded(e, i);
        if (need && (strncmp(need, "libc.so.6", 9) == 0 || strncmp(need, "ld-linux", 8) == 0)) return 1;
    }
    return 0;
}

/* Read (mmap) and parse a bundled library. */
static ApakObj *scanFile(const char *path) {
    ApakObj *o;
    struct stat st;
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    void *map;
    if (fd < 0) {
        setErr("%s: %s", path, strerror(errno));
        return NULL;
    }
    if (fstat(fd, &st) != 0 || st.st_size <= 0) {
        setErr("%s: cannot stat or empty", path);
        close(fd);
        return NULL;
    }
    map = mmap(NULL, (size_t)st.st_size, PROT_READ, MAP_PRIVATE, fd, 0);
    close(fd);
    if (map == MAP_FAILED) {
        setErr("%s: mmap: %s", path, strerror(errno));
        return NULL;
    }
    o = (ApakObj *)calloc(1, sizeof *o);
    if (!o) {
        munmap(map, (size_t)st.st_size);
        setErr("out of memory");
        return NULL;
    }
    o->magic = APAK_OBJ_MAGIC;
    o->kind = OBJ_BUNDLED;
    o->fileMap = map;
    o->fileSize = (size_t)st.st_size;
    snprintf(o->path, sizeof o->path, "%s", path);
    if (apak_elfParse(&o->elf, map, o->fileSize) != 0) {
        setErr("%s: %s", baseName(path), o->elf.err);
        munmap(map, o->fileSize);
        free(o);
        return NULL;
    }
    if (o->elf.eh->e_machine != APAK_HOST_MACHINE) {
        setErr("%s is built for %s; this runner loads %s libraries", baseName(path),
               apak_elfMachineName(o->elf.eh->e_machine), apak_elfMachineName(APAK_HOST_MACHINE));
        munmap(map, o->fileSize);
        free(o);
        return NULL;
    }
    if (o->elf.tls) {
        setErr("%s uses ELF TLS (PT_TLS: built for Android 10+ thread_local) — not supported by this runner yet",
               baseName(path));
        munmap(map, o->fileSize);
        free(o);
        return NULL;
    }
    apak_strcpy(o->name, sizeof o->name, o->elf.soname && *o->elf.soname ? o->elf.soname : baseName(path));
    return o;
}

/* Find or create the object for a DT_NEEDED/dlopen name. *isNew is set for
 * a freshly scanned bundled library (it still needs map/bind/init). */
static ApakObj *objAcquire(const char *name, int *isNew) {
    char path[PATH_MAX];
    const VirtualLib *v = virtualFor(name);
    ApakObj *o = objFind(name);
    *isNew = 0;
    if (o) return o;
    if (!coreVirtual(v) && locateBundled(name, path, sizeof path)) {
        ApakObj *loaded = objFind(path);
        if (loaded) return loaded;
        o = scanFile(path);
        if (!o) return NULL;
        if (fileNeedsGlibc(&o->elf)) {
            /* A glibc-linked library (a host helper, or the host-built smoke
             * game): glibc's own loader is the right tool for it. */
            ApakObj *h = (ApakObj *)calloc(1, sizeof *h);
            if (!h) return NULL;
            h->magic = APAK_OBJ_MAGIC;
            h->kind = OBJ_VIRTUAL;
            h->scanned = 1;
            snprintf(h->name, sizeof h->name, "%s", o->name);
            snprintf(h->path, sizeof h->path, "%s", path);
            snprintf(h->hostName, sizeof h->hostName, "%s", path);
            munmap(o->fileMap, o->fileSize);
            free(o);
            h->host = dlopen(path, RTLD_NOW | RTLD_LOCAL);
            if (!h->host) {
                setErr("%s", dlerror());
                free(h);
                return NULL;
            }
            h->vkind = VK_HOST;
            objAppend(h);
            return h;
        }
        objAppend(o);
        *isNew = 1;
        return o;
    }
    return newVirtual(name, v);
}

/* ── symbol resolution ──────────────────────────────────────────────── */

enum { SRC_NONE = 0, SRC_SELF, SRC_OVERRIDE, SRC_BUNDLED, SRC_FALLBACK, SRC_HOST };

static void *symAddr(const ApakObj *o, size_t index, int dry) {
    const ApakElfSym *s = &o->elf.syms[index];
    uintptr_t addr;
    if (dry || !o->mapped) return (void *)(uintptr_t)(0x10000u + s->st_value);
    addr = o->bias + (uintptr_t)s->st_value;
    if (APAK_ST_TYPE(s->st_info) == APAK_STT_GNU_IFUNC) {
        typedef void *(*Resolver)(unsigned long);
        addr = (uintptr_t)((Resolver)addr)(getauxval(AT_HWCAP));
    }
    return (void *)addr;
}

/* What a provider (virtual library) exports. */
static void *providerSym(const ApakObj *v, const char *name) {
    void *p;
    switch (v->vkind) {
        case VK_BIONIC:
            if ((p = apak_compatOverride(name)) != NULL) return p;
            if ((p = apak_compatFallback(name)) != NULL) return p;
            return dlsym(RTLD_DEFAULT, name);
        case VK_RUNNER:
            if ((p = apak_compatOverride(name)) != NULL) return p;
            return dlsym(RTLD_DEFAULT, name);
        case VK_HOST:
            if ((p = apak_compatOverride(name)) != NULL) return p;
            return v->host ? dlsym(v->host, name) : NULL;
        default:
            return NULL;
    }
}

/* The global binding order (see bioniccompat.h). `skip` excludes one object
 * (RTLD_NEXT); `dry` reports existence for not-yet-mapped objects. */
static void *resolveGlobal(const char *name, const ApakObj *skip, int dry, int *source) {
    ApakObj *o;
    void *p;
    if ((p = apak_compatOverride(name)) != NULL) {
        *source = SRC_OVERRIDE;
        return p;
    }
    for (o = g_objs; o; o = o->next) {
        long i;
        if (o->kind != OBJ_BUNDLED || o == skip) continue;
        i = apak_elfLookup(&o->elf, name);
        if (i >= 0 && APAK_ST_TYPE(o->elf.syms[i].st_info) != APAK_STT_TLS) {
            *source = SRC_BUNDLED;
            return symAddr(o, (size_t)i, dry);
        }
    }
    if ((p = apak_compatFallback(name)) != NULL) {
        *source = SRC_FALLBACK;
        return p;
    }
    for (o = g_objs; o; o = o->next) {
        if (o->kind != OBJ_VIRTUAL || o->vkind != VK_HOST || !o->host || o == skip) continue;
        if ((p = dlsym(o->host, name)) != NULL) {
            *source = SRC_HOST;
            return p;
        }
    }
    if ((p = dlsym(RTLD_DEFAULT, name)) != NULL) {
        *source = SRC_HOST;
        return p;
    }
    *source = SRC_NONE;
    return NULL;
}

/* ── mapping ────────────────────────────────────────────────────────── */

static int protOf(uint32_t flags) {
    int prot = 0;
    if (flags & APAK_PF_R) prot |= PROT_READ;
    if (flags & APAK_PF_W) prot |= PROT_WRITE;
    if (flags & APAK_PF_X) prot |= PROT_EXEC;
    return prot;
}

static uintptr_t pageDown(uintptr_t v) { return v & ~(uintptr_t)(g_pageSize - 1); }
static uintptr_t pageUp(uintptr_t v) { return (v + g_pageSize - 1) & ~(uintptr_t)(g_pageSize - 1); }

static int mapObject(ApakObj *o) {
    const ApakElf *e = &o->elf;
    uintptr_t lo = pageDown((uintptr_t)e->loadMin), hi = pageUp((uintptr_t)e->loadMax);
    void *start;
    unsigned i;
    int fd;

    if (hi <= lo) {
        setErr("%s: empty load span", o->name);
        return -1;
    }
    start = mmap(NULL, hi - lo, PROT_NONE, MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE, -1, 0);
    if (start == MAP_FAILED) {
        setErr("%s: cannot reserve %zu bytes: %s", o->name, (size_t)(hi - lo), strerror(errno));
        return -1;
    }
    o->mapStart = (uint8_t *)start;
    o->mapSize = hi - lo;
    o->bias = (uintptr_t)start - lo;

    fd = open(o->path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        setErr("%s: %s", o->path, strerror(errno));
        return -1;
    }
    for (i = 0; i < e->phnum; i++) {
        const ApakElfPhdr *p = &e->ph[i];
        uintptr_t segStart, segEnd, pageStart, fileEnd;
        int prot;
        if (p->p_type != APAK_PT_LOAD || p->p_memsz == 0) continue;
        if ((p->p_vaddr - p->p_offset) % g_pageSize != 0) {
            close(fd);
            setErr("%s: PT_LOAD offset/vaddr not congruent modulo the page size", o->name);
            return -1;
        }
        segStart = o->bias + (uintptr_t)p->p_vaddr;
        segEnd = segStart + (uintptr_t)p->p_memsz;
        pageStart = pageDown(segStart);
        fileEnd = segStart + (uintptr_t)p->p_filesz;
        prot = protOf(p->p_flags);
        if (p->p_filesz) {
            void *m = mmap((void *)pageStart, fileEnd - pageStart, prot, MAP_PRIVATE | MAP_FIXED, fd,
                           (off_t)pageDown((uintptr_t)p->p_offset));
            if (m == MAP_FAILED) {
                close(fd);
                setErr("%s: mmap segment: %s", o->name, strerror(errno));
                return -1;
            }
        }
        if (p->p_memsz > p->p_filesz) {
            uintptr_t zeroPageEnd = pageUp(fileEnd), anonStart = p->p_filesz ? zeroPageEnd : pageStart;
            uintptr_t anonEnd = pageUp(segEnd);
            if (p->p_filesz && zeroPageEnd > fileEnd) {
                if (!(prot & PROT_WRITE)) mprotect((void *)pageDown(fileEnd), g_pageSize, prot | PROT_WRITE);
                memset((void *)fileEnd, 0, zeroPageEnd - fileEnd);
                if (!(prot & PROT_WRITE)) mprotect((void *)pageDown(fileEnd), g_pageSize, prot);
            }
            if (anonEnd > anonStart) {
                void *m = mmap((void *)anonStart, anonEnd - anonStart, prot,
                               MAP_PRIVATE | MAP_FIXED | MAP_ANONYMOUS, -1, 0);
                if (m == MAP_FAILED) {
                    close(fd);
                    setErr("%s: mmap bss: %s", o->name, strerror(errno));
                    return -1;
                }
            }
        }
    }
    close(fd);

    /* Program headers in memory for dl_iterate_phdr (the unwinder reads
     * PT_GNU_EH_FRAME through them). */
    o->phdrMem = e->ph;
    for (i = 0; i < e->phnum; i++) {
        if (e->ph[i].p_type == APAK_PT_PHDR) {
            o->phdrMem = (const void *)(o->bias + (uintptr_t)e->ph[i].p_vaddr);
            break;
        }
        if (e->ph[i].p_type == APAK_PT_LOAD && e->ph[i].p_offset == 0 &&
            e->ph[i].p_filesz >= e->eh->e_phoff + (uint64_t)e->phnum * sizeof(ApakElfPhdr)) {
            o->phdrMem = (const void *)(o->bias + (uintptr_t)e->ph[i].p_vaddr + (uintptr_t)e->eh->e_phoff);
            break;
        }
    }
    o->mapped = 1;
    g_adds++;
    return 0;
}

static void protectSegments(ApakObj *o, int writable) {
    unsigned i;
    for (i = 0; i < o->elf.phnum; i++) {
        const ApakElfPhdr *p = &o->elf.ph[i];
        uintptr_t s, e;
        if (p->p_type != APAK_PT_LOAD) continue;
        s = pageDown(o->bias + (uintptr_t)p->p_vaddr);
        e = pageUp(o->bias + (uintptr_t)p->p_vaddr + (uintptr_t)p->p_memsz);
        mprotect((void *)s, e - s, writable ? (protOf(p->p_flags) | PROT_WRITE) : protOf(p->p_flags));
    }
}

/* ── binding ────────────────────────────────────────────────────────── */

typedef struct {
    ApakObj *o;
    int dry;
    uintptr_t *val;
    uint8_t *state;          /* 0 unresolved, 1 resolved                 */
    int fatal;
} BindCtx;

static int symbolValue(BindCtx *c, uint32_t si, uintptr_t *out) {
    ApakObj *o = c->o;
    const ApakElfSym *s;
    const char *name;
    unsigned bind, vis, type;
    int source = SRC_NONE;
    void *p;

    if (si == 0) {
        *out = 0;
        return 0;
    }
    if (si >= o->elf.nsyms) {
        setErr("%s: relocation names symbol %u of %zu", o->name, si, o->elf.nsyms);
        return -1;
    }
    if (c->state[si]) {
        *out = c->val[si];
        return 0;
    }
    s = &o->elf.syms[si];
    name = apak_elfSymName(&o->elf, si);
    bind = APAK_ST_BIND(s->st_info);
    vis = APAK_ST_VIS(s->st_other);
    type = APAK_ST_TYPE(s->st_info);
    if (!name) {
        setErr("%s: symbol %u has a bad name", o->name, si);
        return -1;
    }
    if (type == APAK_STT_TLS) {
        setErr("%s: TLS symbol %s (ELF TLS is not supported yet)", o->name, name);
        return -1;
    }
    if (s->st_shndx != APAK_SHN_UNDEF && (bind == APAK_STB_LOCAL || vis != APAK_STV_DEFAULT)) {
        p = symAddr(o, si, c->dry);
        source = SRC_SELF;
    } else {
        p = resolveGlobal(name, NULL, c->dry, &source);
    }
    if (s->st_shndx == APAK_SHN_UNDEF) {
        g_stat.imports++;
        switch (source) {
            case SRC_OVERRIDE: g_stat.override++; break;
            case SRC_BUNDLED: g_stat.bundled++; break;
            case SRC_FALLBACK: g_stat.fallback++; break;
            case SRC_HOST: g_stat.host++; break;
            default: break;
        }
    }
    if (!p) {
        if (bind == APAK_STB_WEAK) {
            g_stat.weakNull++;
            p = NULL;
        } else {
            const char *from = NULL;
            const char *ver = apak_elfSymVersion(&o->elf, si, &from, NULL);
            ApakMissing *m = recordMissing(name, o->name, from, ver, (int)type);
            g_stat.missing++;
            if (!m) return -1;
            if (type == APAK_STT_OBJECT || type == APAK_STT_COMMON) {
                p = c->dry ? (void *)1 : missingData((size_t)s->st_size);
            } else {
                if (m->trap < 0) {
                    if (g_trapsUsed < APAK_TRAPS - 1) {
                        m->trap = g_trapsUsed;
                        g_trapMissing[g_trapsUsed++] = (int)(m - g_missing);
                    } else {
                        m->trap = APAK_TRAPS - 1;
                        g_trapOverflow++;
                    }
                }
                p = (void *)kTraps[m->trap];
            }
        }
    }
    c->val[si] = (uintptr_t)p;
    c->state[si] = 1;
    *out = (uintptr_t)p;
    return 0;
}

static int bindOne(void *ctx, const ApakReloc *r) {
    BindCtx *c = (BindCtx *)ctx;
    ApakObj *o = c->o;
    uint32_t type = r->relr ? RT_RELATIVE : APAK_R_TYPE(r->info);
    uint32_t si = r->relr ? 0 : APAK_R_SYM(r->info);
    uintptr_t *where = NULL, S = 0;

    if (!c->dry) {
        if (r->offset < pageDown((uintptr_t)o->elf.loadMin) || r->offset + sizeof(uintptr_t) > o->elf.loadMax) {
            setErr("%s: relocation target 0x%llx outside the image", o->name, (unsigned long long)r->offset);
            return -1;
        }
        where = (uintptr_t *)(o->bias + (uintptr_t)r->offset);
    }
    switch (type) {
        case RT_NONE:
            return 0;
        case RT_RELATIVE:
            if (c->dry) return 0;
            if (r->hasAddend) *where = o->bias + (uintptr_t)r->addend;
            else *where += o->bias;
            return 0;
        case RT_IRELATIVE: {
            typedef uintptr_t (*Resolver)(unsigned long);
            uintptr_t fn;
            if (c->dry) return 0;
            fn = o->bias + (uintptr_t)(r->hasAddend ? r->addend : (int64_t)*where);
            *where = ((Resolver)fn)(getauxval(AT_HWCAP));
            return 0;
        }
        case RT_GLOB_DAT:
        case RT_JUMP_SLOT:
        case RT_ABS64: {
            int64_t addend = 0;
            if (symbolValue(c, si, &S) != 0) return -1;
            if (c->dry) return 0;
            if (r->hasAddend) addend = r->addend;
            else if (type == RT_ABS64) addend = (int64_t)*where;
            *where = S + (uintptr_t)addend;
            return 0;
        }
        case RT_COPY:
            setErr("%s: R_COPY relocation in a shared library", o->name);
            return -1;
        case RT_TLS_DTPMOD:
        case RT_TLS_DTPREL:
        case RT_TLS_TPREL:
        case RT_TLSDESC:
            setErr("%s: ELF TLS relocations (built for Android 10+ thread_local) are not supported yet", o->name);
            return -1;
        default:
            setErr("%s: unsupported relocation type %u", o->name, type);
            return -1;
    }
}

static int bindObject(ApakObj *o, int dry) {
    BindCtx c;
    int rc;
    memset(&c, 0, sizeof c);
    c.o = o;
    c.dry = dry;
    c.val = (uintptr_t *)calloc(o->elf.nsyms ? o->elf.nsyms : 1, sizeof *c.val);
    c.state = (uint8_t *)calloc(o->elf.nsyms ? o->elf.nsyms : 1, 1);
    if (!c.val || !c.state) {
        free(c.val);
        free(c.state);
        setErr("out of memory binding %s", o->name);
        return -1;
    }
    if (!dry && o->elf.textrel) protectSegments(o, 1);
    rc = apak_elfForEachReloc(&o->elf, bindOne, &c);
    if (rc < 0 && o->elf.err[0] && !g_err[0]) setErr("%s: %s", o->name, o->elf.err);
    if (!dry && o->elf.textrel) protectSegments(o, 0);
    if (rc == 0 && !dry && o->elf.relro) {
        uintptr_t s = pageDown(o->bias + (uintptr_t)o->elf.relro->p_vaddr);
        uintptr_t e = pageDown(o->bias + (uintptr_t)o->elf.relro->p_vaddr + (uintptr_t)o->elf.relro->p_memsz);
        if (e > s) mprotect((void *)s, e - s, PROT_READ);
    }
    free(c.val);
    free(c.state);
    if (rc == 0) o->relocated = 1;
    return rc == 0 ? 0 : -1;
}

/* ── init ───────────────────────────────────────────────────────────── */

typedef void (*InitFn)(int, char **, char **);

static void runInit(ApakObj *o) {
    size_t i;
    if (o->kind != OBJ_BUNDLED || o->initState) return;
    o->initState = 1;
    for (i = 0; i < o->ndeps; i++) runInit(o->deps[i]);
    if (o->elf.init) ((InitFn)(o->bias + (uintptr_t)o->elf.init))(g_argc, g_argv, environ);
    if (o->elf.initArray) {
        const uintptr_t *arr = (const uintptr_t *)(o->bias + (uintptr_t)o->elf.initArray);
        for (i = 0; i < o->elf.initArrayCount; i++) {
            uintptr_t fn = arr[i];
            if (fn == 0 || fn == (uintptr_t)-1) continue;
            ((InitFn)fn)(g_argc, g_argv, environ);
        }
    }
    o->initState = 2;
}

/* ── the report ─────────────────────────────────────────────────────── */

static const char *vkindName(int vk) {
    switch (vk) {
        case VK_BIONIC: return "bionic compat";
        case VK_RUNNER: return "AndroidRT";
        case VK_HOST: return "host";
        case VK_MISSING: return "MISSING";
        default: return "?";
    }
}

static void printReport(ApakObj **batch, size_t n, const char *verb) {
    size_t i, j, funcs = 0, data = 0, shown = 0;
    ApakObj *root = n ? batch[0] : NULL;
    if (!root) return;
    fprintf(stderr, LD_TAG "loader: %s %s (%s) with %zu bundled librar%s\n", verb, root->name,
            apak_elfMachineName(root->elf.eh->e_machine), n - 1, n - 1 == 1 ? "y" : "ies");
    for (i = 0; i < n; i++) {
        for (j = 0; j < batch[i]->ndeps; j++) {
            ApakObj *d = batch[i]->deps[j];
            if (d->kind != OBJ_VIRTUAL) continue;
            if (d->vkind == VK_MISSING)
                fprintf(stderr, LD_TAG "missing library: %s (needed by %s; %s) — its symbols are reported below\n",
                        d->name, batch[i]->name, d->path[0] ? d->path : "not found");
            else if (getenv("ANDROIDPAK_DEBUG"))
                fprintf(stderr, LD_TAG "  %s needs %s -> %s%s%s\n", batch[i]->name, d->name, vkindName(d->vkind),
                        d->hostName[0] ? " " : "", d->hostName);
        }
    }
    for (i = g_batchMissingStart; i < g_nmissing; i++) {
        if (g_missing[i].type == APAK_STT_OBJECT || g_missing[i].type == APAK_STT_COMMON) data++;
        else funcs++;
    }
    fprintf(stderr, LD_TAG "bound %zu imports: %zu compat, %zu bundled, %zu fallback, %zu host, %zu weak-null\n",
            g_stat.imports, g_stat.override, g_stat.bundled, g_stat.fallback, g_stat.host, g_stat.weakNull);
    if (funcs + data == 0) {
        fprintf(stderr, LD_TAG "undefined symbols: 0 — every import resolved\n");
        return;
    }
    fprintf(stderr, LD_TAG "undefined symbols: %zu (%zu functions %s, %zu data zero-filled)\n", funcs + data, funcs,
            g_policy == APAK_MISSING_ZERO ? "return 0 when called" : "abort when called", data);
    for (i = g_batchMissingStart; i < g_nmissing; i++) {
        const ApakMissing *m = &g_missing[i];
        if (shown++ >= 400) {
            fprintf(stderr, LD_TAG "... and %zu more undefined symbols\n", g_nmissing - i);
            break;
        }
        fprintf(stderr, LD_TAG "undefined symbol: %s (%s, needed by %s%s%s%s%s)\n", m->name,
                (m->type == APAK_STT_OBJECT || m->type == APAK_STT_COMMON) ? "data" : "func", m->needer,
                m->from[0] ? ", from " : "", m->from, m->version[0] ? "@" : "", m->version);
    }
}

/* ── open ───────────────────────────────────────────────────────────── */

static int loadBatch(ApakObj *root, int dry) {
    ApakObj **batch = NULL;
    size_t n = 0, cap = 0, qi, i;
    int rc = 0;

    if (!g_pageSize) g_pageSize = (size_t)sysconf(_SC_PAGESIZE);
    memset(&g_stat, 0, sizeof g_stat);
    g_batchMissingStart = g_nmissing;

    /* 1. breadth-first discovery of every bundled library this batch still
     *    has to map (dry: still has to report) */
    g_batchGen++;
    cap = 16;
    batch = (ApakObj **)malloc(cap * sizeof *batch);
    if (!batch) return -1;
    batch[n++] = root;
    root->batchGen = g_batchGen;
    for (qi = 0; qi < n; qi++) {
        ApakObj *o = batch[qi];
        size_t k;
        if (!o->scanned) {
            size_t need = apak_elfNeededCount(&o->elf);
            o->deps = (ApakObj **)calloc(need ? need : 1, sizeof *o->deps);
            if (!o->deps) {
                rc = -1;
                goto out;
            }
            for (k = 0; k < need; k++) {
                const char *dn = apak_elfNeeded(&o->elf, k);
                int isNew = 0;
                ApakObj *d;
                if (!dn) continue;
                d = objAcquire(dn, &isNew);
                if (!d) {
                    /* An unreadable bundled dependency is fatal (bionic agrees). */
                    fprintf(stderr, LD_TAG "could not load dependency %s of %s: %s\n", dn, o->name, g_err);
                    rc = -1;
                    goto out;
                }
                o->deps[o->ndeps++] = d;
            }
            o->scanned = 1;
        }
        for (k = 0; k < o->ndeps; k++) {
            ApakObj *d = o->deps[k];
            if (d->kind != OBJ_BUNDLED || d->batchGen == g_batchGen) continue;
            if (dry ? d->dryDone : d->mapped) continue;
            if (n == cap) {
                ApakObj **grown = (ApakObj **)realloc(batch, (cap *= 2) * sizeof *batch);
                if (!grown) {
                    rc = -1;
                    goto out;
                }
                batch = grown;
            }
            d->batchGen = g_batchGen;
            batch[n++] = d;
        }
    }

    /* 2. map */
    if (!dry) {
        for (i = 0; i < n; i++) {
            if (mapObject(batch[i]) != 0) {
                rc = -1;
                goto out;
            }
        }
    }
    /* 3. bind */
    for (i = 0; i < n; i++) {
        if (bindObject(batch[i], dry) != 0) {
            fprintf(stderr, LD_TAG "could not bind %s: %s\n", batch[i]->name, g_err);
            rc = -1;
            goto out;
        }
        if (dry) batch[i]->dryDone = 1;
    }
    /* 4. report */
    printReport(batch, n, dry ? "scanned" : "loaded");
    if (!dry && g_policy == APAK_MISSING_STRICT && g_nmissing > g_batchMissingStart) {
        setErr("%zu undefined symbols and ANDROIDPAK_MISSING=strict", g_nmissing - g_batchMissingStart);
        fprintf(stderr, LD_TAG "refusing to start: %s\n", g_err);
        rc = -1;
        goto out;
    }
    /* 5. init */
    if (!dry) runInit(root);
out:
    free(batch);
    return rc;
}

static ApakObj *openLocked(const char *name, int dry, int noload) {
    int isNew = 0;
    ApakObj *o;
    g_err[0] = '\0';
    if (noload) return objFind(name);
    o = objAcquire(name, &isNew);
    if (!o) return NULL;
    if (o->kind == OBJ_BUNDLED && (dry ? !o->dryDone : !o->mapped)) {
        if (loadBatch(o, dry) != 0) {
            if (!g_err[0]) setErr("could not load %s", name);
            return NULL;
        }
    } else if (o->kind == OBJ_VIRTUAL && o->vkind == VK_MISSING) {
        setErr("library \"%s\" not found (not bundled, not an Android system library this runner provides)", name);
        return NULL;
    }
    o->refs++;
    return o;
}

void *apak_ldOpen(const char *nameOrPath) {
    ApakObj *o;
    apak_compatInit();
    pthread_mutex_lock(&g_ldLock);
    o = openLocked(nameOrPath, 0, 0);
    pthread_mutex_unlock(&g_ldLock);
    return o;
}

int apak_ldScan(const char *nameOrPath) {
    ApakObj *o;
    int missing;
    apak_compatInit();
    pthread_mutex_lock(&g_ldLock);
    o = openLocked(nameOrPath, 1, 0);
    missing = o ? (int)(g_nmissing - g_batchMissingStart) : -1;
    pthread_mutex_unlock(&g_ldLock);
    return missing;
}

void *apak_ldSymSelf(void *handle, const char *name) {
    ApakObj *o = (ApakObj *)handle;
    long i;
    if (!isValidObj(o) || o->kind == OBJ_SELF) return NULL;
    if (o->kind == OBJ_VIRTUAL) return o->host ? dlsym(o->host, name) : NULL;
    i = apak_elfLookup(&o->elf, name);
    return i >= 0 ? symAddr(o, (size_t)i, 0) : NULL;
}

/* An export of any loaded game library (Java_* natives, JNI_OnLoad ...):
 * bundled bionic objects first, then glibc-built game libraries. */
void *apak_ldSymBundled(const char *name) {
    ApakObj *o;
    void *p = NULL;
    if (!name) return NULL;
    pthread_mutex_lock(&g_ldLock);
    for (o = g_objs; o && !p; o = o->next) {
        long i;
        if (o->kind != OBJ_BUNDLED || !o->mapped) continue;
        i = apak_elfLookup(&o->elf, name);
        if (i >= 0 && APAK_ST_TYPE(o->elf.syms[i].st_info) != APAK_STT_TLS) p = symAddr(o, (size_t)i, 0);
    }
    for (o = g_objs; o && !p; o = o->next)
        if (o->kind == OBJ_VIRTUAL && o->vkind == VK_HOST && o->host && o->path[0] == '/') p = dlsym(o->host, name);
    pthread_mutex_unlock(&g_ldLock);
    return p;
}

static void visitInitOrder(ApakObj *o, unsigned gen, void (*fn)(const char *, void *, void *), void *ud, int depth) {
    size_t i;
    if (!o || depth > 64 || o->batchGen == gen) return;
    o->batchGen = gen;
    for (i = 0; i < o->ndeps; i++) visitInitOrder(o->deps[i], gen, fn, ud, depth + 1);
    if (o->kind == OBJ_BUNDLED || (o->kind == OBJ_VIRTUAL && o->vkind == VK_HOST && o->path[0] == '/'))
        fn(o->name, o, ud);
}

/* The game's own libraries reachable from root, dependencies first (the
 * order System.loadLibrary would have run their JNI_OnLoad). */
void apak_ldForEachInitOrder(void *root, void (*fn)(const char *name, void *handle, void *ud), void *ud) {
    if (!isValidObj(root) || !fn) return;
    pthread_mutex_lock(&g_ldLock);
    g_batchGen++;
    visitInitOrder((ApakObj *)root, g_batchGen, fn, ud, 0);
    pthread_mutex_unlock(&g_ldLock);
}

int apak_ldFindExporter(const char *symbol, char *out, size_t cap) {
    int d;
    for (d = 0; d < g_nLibDirs; d++) {
        DIR *dir = opendir(g_libDirs[d]);
        struct dirent *ent;
        if (!dir) continue;
        while ((ent = readdir(dir)) != NULL) {
            char path[PATH_MAX];
            struct stat st;
            size_t len = strlen(ent->d_name);
            void *map;
            int fd, found = 0;
            ApakElf e;
            if (len < 4 || strcmp(ent->d_name + len - 3, ".so") != 0) continue;
            if (snprintf(path, sizeof path, "%s/%s", g_libDirs[d], ent->d_name) >= (int)sizeof path) continue;
            fd = open(path, O_RDONLY | O_CLOEXEC);
            if (fd < 0) continue;
            if (fstat(fd, &st) != 0 || st.st_size <= 0) {
                close(fd);
                continue;
            }
            map = mmap(NULL, (size_t)st.st_size, PROT_READ, MAP_PRIVATE, fd, 0);
            close(fd);
            if (map == MAP_FAILED) continue;
            if (apak_elfParse(&e, map, (size_t)st.st_size) == 0 && apak_elfLookup(&e, symbol) >= 0) found = 1;
            munmap(map, (size_t)st.st_size);
            if (found) {
                snprintf(out, cap, "%s", path);
                closedir(dir);
                return 1;
            }
        }
        closedir(dir);
    }
    return 0;
}

/* ── the bionic libdl ABI ───────────────────────────────────────────── */

static void clearDlerror(void) { apak_tls.dlerrorSet = 0; }

void *apak_bionic_dlopen(const char *name, int flags) {
    ApakObj *o;
    clearDlerror();
    if (!name) return &g_self;
    apak_compatInit();
    pthread_mutex_lock(&g_ldLock);
    o = openLocked(name, 0, (flags & RTLD_NOLOAD) != 0);
    if (!o && !(flags & RTLD_NOLOAD) && getenv("ANDROIDPAK_DEBUG"))
        fprintf(stderr, LD_TAG "dlopen(\"%s\") failed: %s\n", name, g_err);
    if (!o && (flags & RTLD_NOLOAD)) setErr("dlopen(\"%s\", RTLD_NOLOAD): not loaded", name);
    pthread_mutex_unlock(&g_ldLock);
    return o;
}

void *apak_bionic_android_dlopen_ext(const char *name, int flags, const void *extinfo) {
    (void)extinfo;
    return apak_bionic_dlopen(name, flags);
}

static void *handleSym(ApakObj *o, const char *name, int depth) {
    size_t i;
    void *p;
    if (depth > 32) return NULL;
    if (o->kind == OBJ_VIRTUAL) return providerSym(o, name);
    {
        long idx = apak_elfLookup(&o->elf, name);
        if (idx >= 0) return symAddr(o, (size_t)idx, 0);
    }
    for (i = 0; i < o->ndeps; i++)
        if ((p = handleSym(o->deps[i], name, depth + 1)) != NULL) return p;
    return NULL;
}

static const ApakObj *objForAddr(const void *addr) {
    const ApakObj *o;
    uintptr_t a = (uintptr_t)addr;
    for (o = g_objs; o; o = o->next)
        if (o->kind == OBJ_BUNDLED && o->mapped && a >= (uintptr_t)o->mapStart && a < (uintptr_t)o->mapStart + o->mapSize)
            return o;
    return NULL;
}

void *apak_bionic_dlsym(void *handle, const char *name) {
    void *p = NULL;
    int source;
    clearDlerror();
    if (!name) {
        setErr("dlsym: NULL symbol name");
        return NULL;
    }
    pthread_mutex_lock(&g_ldLock);
    if (handle == RTLD_DEFAULT || handle == &g_self) {
        p = resolveGlobal(name, NULL, 0, &source);
    } else if (handle == RTLD_NEXT) {
        p = resolveGlobal(name, objForAddr(__builtin_return_address(0)), 0, &source);
    } else if (isValidObj(handle)) {
        p = handleSym((ApakObj *)handle, name, 0);
    } else {
        setErr("dlsym: invalid handle %p", handle);
        pthread_mutex_unlock(&g_ldLock);
        return NULL;
    }
    if (!p) {
        setErr("undefined symbol: %s", name);
        if (getenv("ANDROIDPAK_DEBUG"))
            fprintf(stderr, LD_TAG "dlsym(%s, \"%s\") -> not found\n",
                    handle == RTLD_DEFAULT ? "RTLD_DEFAULT" : handle == RTLD_NEXT ? "RTLD_NEXT"
                    : ((ApakObj *)handle)->name, name);
    }
    pthread_mutex_unlock(&g_ldLock);
    return p;
}

void *apak_bionic_dlvsym(void *handle, const char *name, const char *version) {
    (void)version;
    return apak_bionic_dlsym(handle, name);
}

int apak_bionic_dlclose(void *handle) {
    clearDlerror();
    if (!isValidObj(handle)) {
        setErr("dlclose: invalid handle");
        return -1;
    }
    pthread_mutex_lock(&g_ldLock);
    if (((ApakObj *)handle)->refs > 0) ((ApakObj *)handle)->refs--;
    pthread_mutex_unlock(&g_ldLock);
    return 0; /* never unmapped, like bionic for app libraries in practice */
}

char *apak_bionic_dlerror(void) {
    if (!apak_tls.dlerrorSet) return NULL;
    apak_tls.dlerrorSet = 0;
    return apak_tls.dlerror;
}

int apak_bionic_dladdr(const void *addr, void *dlInfo) {
    Dl_info *info = (Dl_info *)dlInfo;
    const ApakObj *o;
    const ApakElfSym *best = NULL;
    size_t i;
    uintptr_t off;
    pthread_mutex_lock(&g_ldLock);
    o = objForAddr(addr);
    if (!o) {
        pthread_mutex_unlock(&g_ldLock);
        return dladdr(addr, info);
    }
    off = (uintptr_t)addr - o->bias;
    info->dli_fname = o->path;
    info->dli_fbase = (void *)(o->bias + pageDown((uintptr_t)o->elf.loadMin));
    info->dli_sname = NULL;
    info->dli_saddr = NULL;
    for (i = 1; i < o->elf.nsyms; i++) {
        const ApakElfSym *s = &o->elf.syms[i];
        unsigned type = APAK_ST_TYPE(s->st_info);
        if (s->st_shndx == APAK_SHN_UNDEF || s->st_value > off) continue;
        if (type != APAK_STT_FUNC && type != APAK_STT_OBJECT && type != APAK_STT_GNU_IFUNC) continue;
        if (s->st_size && off >= s->st_value + s->st_size) continue;
        if (!best || s->st_value > best->st_value) best = s;
    }
    if (best) {
        info->dli_sname = apak_elfStr(&o->elf, best->st_name);
        info->dli_saddr = (void *)(o->bias + (uintptr_t)best->st_value);
    }
    pthread_mutex_unlock(&g_ldLock);
    return 1;
}

typedef int (*PhdrCb)(struct dl_phdr_info *, size_t, void *);

int apak_bionic_dl_iterate_phdr(int (*cb)(void *info, size_t size, void *data), void *data) {
    const ApakObj *o;
    int rc = 0;
    pthread_mutex_lock(&g_ldLock);
    for (o = g_objs; o && rc == 0; o = o->next) {
        struct dl_phdr_info info;
        if (o->kind != OBJ_BUNDLED || !o->mapped) continue;
        memset(&info, 0, sizeof info);
        info.dlpi_addr = (ElfW(Addr))o->bias;
        info.dlpi_name = o->path;
        info.dlpi_phdr = (const ElfW(Phdr) *)o->phdrMem;
        info.dlpi_phnum = (ElfW(Half))o->elf.phnum;
        info.dlpi_adds = g_adds;
        info.dlpi_subs = 0;
        rc = cb(&info, sizeof info, data);
    }
    pthread_mutex_unlock(&g_ldLock);
    if (rc) return rc;
    return dl_iterate_phdr((PhdrCb)cb, data);
}

static void apak_bionic_android_get_LD_LIBRARY_PATH(char *buf, size_t size) {
    size_t used = 0;
    int i;
    if (!buf || !size) return;
    buf[0] = '\0';
    for (i = 0; i < g_nLibDirs; i++) {
        int w = snprintf(buf + used, size - used, "%s%s", i ? ":" : "", g_libDirs[i]);
        if (w < 0 || (size_t)w >= size - used) break;
        used += (size_t)w;
    }
}

static void apak_bionic_android_update_LD_LIBRARY_PATH(const char *path) { (void)path; }

static int apak_bionic_android_set_application_target_sdk_version(int v) {
    (void)v;
    return 0;
}

const ApakSym apak_dlOverrides[] = {
    { "dlopen", (void *)apak_bionic_dlopen },
    { "android_dlopen_ext", (void *)apak_bionic_android_dlopen_ext },
    { "dlsym", (void *)apak_bionic_dlsym },
    { "dlvsym", (void *)apak_bionic_dlvsym },
    { "dlclose", (void *)apak_bionic_dlclose },
    { "dlerror", (void *)apak_bionic_dlerror },
    { "dladdr", (void *)apak_bionic_dladdr },
    { "dl_iterate_phdr", (void *)apak_bionic_dl_iterate_phdr },
    { "android_get_LD_LIBRARY_PATH", (void *)apak_bionic_android_get_LD_LIBRARY_PATH },
    { "android_update_LD_LIBRARY_PATH", (void *)apak_bionic_android_update_LD_LIBRARY_PATH },
    { "android_set_application_target_sdk_version", (void *)apak_bionic_android_set_application_target_sdk_version },
    { NULL, NULL },
};
