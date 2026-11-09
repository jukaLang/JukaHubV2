/* apkzip.c — see apkzip.h.
 *
 * The central directory is read once and reduced to an entry table plus a
 * name pool and an open-addressing hash, so AAssetManager_open on a 40k
 * entry APK is one hash probe. Nothing is ever written to the archive.
 * POSIX only (pread on a shared descriptor keeps concurrent asset reads from
 * several game threads independent); the Windows smoke build gets a stub
 * that reports "not supported" and the staged assets/ tree stays the only
 * source there.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#if !defined(_WIN32)
#include <unistd.h>
#endif

#include "apkzip.h"
#include "dlport.h"

#ifndef O_CLOEXEC
#define O_CLOEXEC 0
#endif

#define ZIP_MAX_ENTRIES   2000000u
#define ZIP_MAX_CD_BYTES  (256u << 20)
#define ZIP_CHUNK         (64u << 10)

/* ── zlib, resolved at run time ─────────────────────────────────────── */

/* z_stream (zlib.h) on LP64: uInt is 32-bit, uLong 64-bit — 112 bytes.
 * inflateInit2_ refuses a stream whose size differs, so a layout slip is a
 * clean error, never memory corruption. */
typedef struct {
    const uint8_t *next_in;
    unsigned avail_in;
    unsigned long total_in;
    uint8_t *next_out;
    unsigned avail_out;
    unsigned long total_out;
    const char *msg;
    void *state;
    void *zalloc;
    void *zfree;
    void *opaque;
    int data_type;
    unsigned long adler;
    unsigned long reserved;
} ApakZStream;

#define APAK_Z_OK         0
#define APAK_Z_STREAM_END 1
#define APAK_Z_BUF_ERROR  (-5)
#define APAK_Z_NO_FLUSH   0

static struct {
    pthread_once_t once;
    int ok;
    char name[128];
    int (*inflateInit2_)(ApakZStream *, int, const char *, int);
    int (*inflate)(ApakZStream *, int);
    int (*inflateEnd)(ApakZStream *);
    int (*inflateReset)(ApakZStream *);
} Z = { PTHREAD_ONCE_INIT, 0, "", NULL, NULL, NULL, NULL };

static void zLoad(void) {
    static const char *const names[] = { "libz.so.1", "libz.so", "/usr/lib/libz.so.1", "/lib/libz.so.1",
                                         "zlib1.dll", NULL };
    const char *override = getenv("ANDROIDPAK_ZLIB");
    void *h = NULL;
    int i;
    if (override && *override) {
        h = rt_dlopen(override, 0);
        if (h) snprintf(Z.name, sizeof Z.name, "%s", override);
    }
    for (i = 0; !h && names[i]; i++) {
        h = rt_dlopen(names[i], 0);
        if (h) snprintf(Z.name, sizeof Z.name, "%s", names[i]);
    }
    if (!h) return;
    *(void **)&Z.inflateInit2_ = rt_dlsym(h, "inflateInit2_");
    *(void **)&Z.inflate = rt_dlsym(h, "inflate");
    *(void **)&Z.inflateEnd = rt_dlsym(h, "inflateEnd");
    *(void **)&Z.inflateReset = rt_dlsym(h, "inflateReset");
    Z.ok = Z.inflateInit2_ && Z.inflate && Z.inflateEnd && Z.inflateReset;
}

static int zReady(void) {
    pthread_once(&Z.once, zLoad);
    return Z.ok;
}

const char *apak_zipInflateLib(void) { return zReady() ? Z.name : ""; }

#if !defined(_WIN32)

static int zInit(ApakZStream *s) {
    memset(s, 0, sizeof *s);
    /* raw deflate (no zlib header), the zip format's method 8 */
    return Z.inflateInit2_(s, -15, "1.2.11", (int)sizeof *s);
}

struct ApakZip {
    int fd;
    uint64_t fileSize;
    ApakZipEntry *entries;
    size_t count;
    char *names;
    uint32_t *table; /* entry index + 1; 0 = empty */
    size_t tableMask;
    char path[1024];
};

static uint16_t rd16(const uint8_t *p) { return (uint16_t)(p[0] | (p[1] << 8)); }
static uint32_t rd32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}
static uint64_t rd64(const uint8_t *p) { return (uint64_t)rd32(p) | ((uint64_t)rd32(p + 4) << 32); }

static uint32_t nameHash(const char *s, size_t n) {
    uint32_t h = 2166136261u;
    size_t i;
    for (i = 0; i < n; i++) {
        h ^= (uint8_t)s[i];
        h *= 16777619u;
    }
    return h;
}

long apak_zipPread(ApakZip *z, void *buf, size_t n, uint64_t offset) {
    size_t done = 0;
    if (!z) return -1;
    while (done < n) {
        ssize_t r = pread(z->fd, (char *)buf + done, n - done, (off_t)(offset + done));
        if (r < 0) {
            if (errno == EINTR) continue;
            return done ? (long)done : -1;
        }
        if (r == 0) break;
        done += (size_t)r;
    }
    return (long)done;
}

int apak_zipDupFd(ApakZip *z) {
    if (!z) return -1;
    return fcntl(z->fd, F_DUPFD_CLOEXEC, 0);
}

const char *apak_zipPath(const ApakZip *z) { return z ? z->path : ""; }
size_t apak_zipCount(const ApakZip *z) { return z ? z->count : 0; }
const ApakZipEntry *apak_zipAt(const ApakZip *z, size_t index) {
    return z && index < z->count ? &z->entries[index] : NULL;
}

const ApakZipEntry *apak_zipFind(const ApakZip *z, const char *name) {
    size_t n, slot;
    if (!z || !name || !z->table) return NULL;
    n = strlen(name);
    slot = nameHash(name, n) & z->tableMask;
    for (;;) {
        uint32_t v = z->table[slot];
        const ApakZipEntry *e;
        if (!v) return NULL;
        e = &z->entries[v - 1];
        if (e->nameLen == n && memcmp(e->name, name, n) == 0) return e;
        slot = (slot + 1) & z->tableMask;
    }
}

/* Locate the end of central directory (and its ZIP64 twin). */
static int findDirectory(ApakZip *z, uint64_t *cdOffset, uint64_t *cdSize, uint64_t *entries, char *err, size_t cap) {
    uint8_t *tail;
    size_t want = z->fileSize < 65557 ? (size_t)z->fileSize : 65557;
    long got, i;
    if (want < 22) {
        snprintf(err, cap, "too small to be a zip archive");
        return -1;
    }
    tail = (uint8_t *)malloc(want);
    if (!tail) {
        snprintf(err, cap, "out of memory");
        return -1;
    }
    got = apak_zipPread(z, tail, want, z->fileSize - want);
    if (got != (long)want) {
        free(tail);
        snprintf(err, cap, "cannot read the archive tail");
        return -1;
    }
    for (i = (long)want - 22; i >= 0; i--) {
        if (rd32(tail + i) != 0x06054b50u) continue;
        *entries = rd16(tail + i + 10);
        *cdSize = rd32(tail + i + 12);
        *cdOffset = rd32(tail + i + 16);
        if ((*entries == 0xffffu || *cdSize == 0xffffffffu || *cdOffset == 0xffffffffu) && i >= 20 &&
            rd32(tail + i - 20) == 0x07064b50u) {
            uint8_t e64[56];
            uint64_t at = rd64(tail + i - 20 + 8);
            if (apak_zipPread(z, e64, sizeof e64, at) != (long)sizeof e64 || rd32(e64) != 0x06064b50u) {
                free(tail);
                snprintf(err, cap, "broken ZIP64 end of central directory");
                return -1;
            }
            *entries = rd64(e64 + 32);
            *cdSize = rd64(e64 + 40);
            *cdOffset = rd64(e64 + 48);
        }
        free(tail);
        if (*cdOffset > z->fileSize || *cdSize > z->fileSize - *cdOffset) {
            snprintf(err, cap, "central directory outside the file");
            return -1;
        }
        return 0;
    }
    free(tail);
    snprintf(err, cap, "no end of central directory record (not a zip/APK?)");
    return -1;
}

static int indexDirectory(ApakZip *z, const uint8_t *cd, uint64_t cdSize, uint64_t declared, char *err, size_t cap) {
    uint64_t pos = 0, poolBytes = 0;
    size_t n = 0, i, cap2;
    char *pool;
    /* pass 1: count and size the name pool */
    while (pos + 46 <= cdSize && rd32(cd + pos) == 0x02014b50u) {
        uint16_t nl = rd16(cd + pos + 28), xl = rd16(cd + pos + 30), cl = rd16(cd + pos + 32);
        if (pos + 46 + nl + xl + cl > cdSize) break;
        poolBytes += (uint64_t)nl + 1;
        n++;
        if (n > ZIP_MAX_ENTRIES) {
            snprintf(err, cap, "more than %u entries", ZIP_MAX_ENTRIES);
            return -1;
        }
        pos += 46u + nl + xl + cl;
    }
    if (n == 0 && declared != 0) {
        snprintf(err, cap, "unreadable central directory");
        return -1;
    }
    z->entries = (ApakZipEntry *)calloc(n ? n : 1, sizeof *z->entries);
    z->names = (char *)malloc(poolBytes ? (size_t)poolBytes : 1);
    for (cap2 = 16; cap2 < n * 2; cap2 <<= 1) {
    }
    z->table = (uint32_t *)calloc(cap2, sizeof *z->table);
    if (!z->entries || !z->names || !z->table) {
        snprintf(err, cap, "out of memory indexing %zu entries", n);
        return -1;
    }
    z->tableMask = cap2 - 1;
    pool = z->names;
    pos = 0;
    for (i = 0; i < n; i++) {
        const uint8_t *h = cd + pos;
        ApakZipEntry *e = &z->entries[i];
        uint16_t nl = rd16(h + 28), xl = rd16(h + 30), cl = rd16(h + 32);
        uint64_t comp = rd32(h + 20), size = rd32(h + 24), local = rd32(h + 42);
        const uint8_t *x = h + 46 + nl, *xend = x + xl;
        size_t slot;
        /* ZIP64 extended information: only the fields saturated above */
        while (x + 4 <= xend) {
            uint16_t id = rd16(x), len = rd16(x + 2);
            const uint8_t *p = x + 4;
            if (p + len > xend) break;
            if (id == 0x0001) {
                const uint8_t *q = p, *qend = p + len;
                if (size == 0xffffffffu && q + 8 <= qend) {
                    size = rd64(q);
                    q += 8;
                }
                if (comp == 0xffffffffu && q + 8 <= qend) {
                    comp = rd64(q);
                    q += 8;
                }
                if (local == 0xffffffffu && q + 8 <= qend) local = rd64(q);
            }
            x = p + len;
        }
        memcpy(pool, h + 46, nl);
        pool[nl] = '\0';
        e->name = pool;
        e->nameLen = nl;
        e->method = rd16(h + 10);
        e->flags = rd16(h + 8);
        e->compSize = comp;
        e->size = size;
        e->localOffset = local;
        pool += nl + 1;
        slot = nameHash(e->name, nl) & z->tableMask;
        while (z->table[slot]) slot = (slot + 1) & z->tableMask;
        z->table[slot] = (uint32_t)(i + 1);
        pos += 46u + nl + xl + cl;
    }
    z->count = n;
    return 0;
}

ApakZip *apak_zipOpen(const char *path, char *err, size_t cap) {
    ApakZip *z;
    struct stat st;
    uint64_t cdOffset = 0, cdSize = 0, entries = 0;
    uint8_t *cd;
    char localErr[160];
    if (!err || !cap) {
        err = localErr;
        cap = sizeof localErr;
    }
    err[0] = '\0';
    if (!path || !*path) {
        snprintf(err, cap, "no path");
        return NULL;
    }
    z = (ApakZip *)calloc(1, sizeof *z);
    if (!z) {
        snprintf(err, cap, "out of memory");
        return NULL;
    }
    snprintf(z->path, sizeof z->path, "%s", path);
    z->fd = open(path, O_RDONLY | O_CLOEXEC);
    if (z->fd < 0) {
        snprintf(err, cap, "%s", strerror(errno));
        free(z);
        return NULL;
    }
    if (fstat(z->fd, &st) != 0 || !S_ISREG(st.st_mode)) {
        snprintf(err, cap, "not a regular file");
        apak_zipClose(z);
        return NULL;
    }
    z->fileSize = (uint64_t)st.st_size;
    if (findDirectory(z, &cdOffset, &cdSize, &entries, err, cap) != 0) {
        apak_zipClose(z);
        return NULL;
    }
    if (cdSize > ZIP_MAX_CD_BYTES) {
        snprintf(err, cap, "central directory of %llu bytes is too large", (unsigned long long)cdSize);
        apak_zipClose(z);
        return NULL;
    }
    cd = (uint8_t *)malloc(cdSize ? (size_t)cdSize : 1);
    if (!cd || apak_zipPread(z, cd, (size_t)cdSize, cdOffset) != (long)cdSize) {
        free(cd);
        snprintf(err, cap, "cannot read the central directory");
        apak_zipClose(z);
        return NULL;
    }
    if (indexDirectory(z, cd, cdSize, entries, err, cap) != 0) {
        free(cd);
        apak_zipClose(z);
        return NULL;
    }
    free(cd);
    return z;
}

void apak_zipClose(ApakZip *z) {
    if (!z) return;
    if (z->fd >= 0) close(z->fd);
    free(z->entries);
    free(z->names);
    free(z->table);
    free(z);
}

int apak_zipDataOffset(ApakZip *z, const ApakZipEntry *e, uint64_t *offset) {
    uint8_t h[30];
    if (!z || !e) return -1;
    if (apak_zipPread(z, h, sizeof h, e->localOffset) != (long)sizeof h || rd32(h) != 0x04034b50u) return -1;
    *offset = e->localOffset + 30u + rd16(h + 26) + rd16(h + 28);
    if (*offset > z->fileSize || e->compSize > z->fileSize - *offset) return -1;
    return 0;
}

/* ── whole-entry reads ─────────────────────────────────────────────── */

void *apak_zipReadAll(ApakZip *z, const ApakZipEntry *e, size_t maxBytes, size_t *outLen, char *err, size_t cap) {
    uint64_t data;
    uint8_t *out;
    char localErr[160];
    if (!err || !cap) {
        err = localErr;
        cap = sizeof localErr;
    }
    if (outLen) *outLen = 0;
    if (!z || !e) {
        snprintf(err, cap, "no entry");
        return NULL;
    }
    if (e->flags & 1u) {
        snprintf(err, cap, "%s is encrypted", e->name);
        return NULL;
    }
    if (e->size > maxBytes) {
        snprintf(err, cap, "%s is %llu bytes (limit %zu)", e->name, (unsigned long long)e->size, maxBytes);
        return NULL;
    }
    if (apak_zipDataOffset(z, e, &data) != 0) {
        snprintf(err, cap, "%s: bad local header", e->name);
        return NULL;
    }
    out = (uint8_t *)malloc((size_t)e->size + 1);
    if (!out) {
        snprintf(err, cap, "out of memory for %s", e->name);
        return NULL;
    }
    if (e->method == 0) {
        if (e->compSize != e->size || apak_zipPread(z, out, (size_t)e->size, data) != (long)e->size) {
            free(out);
            snprintf(err, cap, "%s: short read", e->name);
            return NULL;
        }
    } else if (e->method == 8) {
        ApakZStream s;
        uint8_t *in;
        uint64_t left = e->compSize, at = data;
        int rc = APAK_Z_OK;
        if (!zReady()) {
            free(out);
            snprintf(err, cap, "%s is deflated and no zlib could be loaded (ANDROIDPAK_ZLIB)", e->name);
            return NULL;
        }
        in = (uint8_t *)malloc(ZIP_CHUNK);
        if (!in || zInit(&s) != APAK_Z_OK) {
            free(in);
            free(out);
            snprintf(err, cap, "inflate init failed");
            return NULL;
        }
        s.next_out = out;
        s.avail_out = (unsigned)e->size;
        while (rc == APAK_Z_OK) {
            if (s.avail_in == 0 && left) {
                size_t chunk = left > ZIP_CHUNK ? ZIP_CHUNK : (size_t)left;
                if (apak_zipPread(z, in, chunk, at) != (long)chunk) break;
                at += chunk;
                left -= chunk;
                s.next_in = in;
                s.avail_in = (unsigned)chunk;
            }
            rc = Z.inflate(&s, APAK_Z_NO_FLUSH);
            if (rc == APAK_Z_BUF_ERROR && s.avail_out == 0) break;
        }
        Z.inflateEnd(&s);
        free(in);
        if (s.total_out != e->size) {
            free(out);
            snprintf(err, cap, "%s: inflated %lu of %llu bytes", e->name, s.total_out, (unsigned long long)e->size);
            return NULL;
        }
    } else {
        free(out);
        snprintf(err, cap, "%s uses compression method %u", e->name, (unsigned)e->method);
        return NULL;
    }
    out[e->size] = 0;
    if (outLen) *outLen = (size_t)e->size;
    return out;
}

/* ── streams ───────────────────────────────────────────────────────── */

struct ApakZipStream {
    ApakZip *z;
    const ApakZipEntry *e;
    uint64_t data;     /* file offset of the entry data */
    uint64_t pos;      /* uncompressed position */
    /* deflate state */
    int inflating;
    ApakZStream s;
    uint8_t *in;
    uint64_t compRead; /* compressed bytes consumed from the file */
    int ended;
};

ApakZipStream *apak_zipStreamOpen(ApakZip *z, const ApakZipEntry *e) {
    ApakZipStream *st;
    uint64_t data;
    if (!z || !e || (e->flags & 1u) || (e->method != 0 && e->method != 8)) return NULL;
    if (e->method == 0 && e->compSize != e->size) return NULL;
    if (apak_zipDataOffset(z, e, &data) != 0) return NULL;
    if (e->method == 8 && !zReady()) return NULL;
    st = (ApakZipStream *)calloc(1, sizeof *st);
    if (!st) return NULL;
    st->z = z;
    st->e = e;
    st->data = data;
    return st;
}

static int streamRestart(ApakZipStream *st) {
    if (st->inflating) Z.inflateEnd(&st->s);
    st->inflating = 0;
    st->pos = 0;
    st->compRead = 0;
    st->ended = 0;
    if (!st->in) st->in = (uint8_t *)malloc(ZIP_CHUNK);
    if (!st->in || zInit(&st->s) != APAK_Z_OK) return -1;
    st->inflating = 1;
    return 0;
}

static long inflateSome(ApakZipStream *st, uint8_t *buf, size_t n) {
    size_t produced = 0;
    if (!st->inflating && streamRestart(st) != 0) return -1;
    while (produced < n && !st->ended) {
        int rc;
        unsigned before;
        if (st->s.avail_in == 0 && st->compRead < st->e->compSize) {
            uint64_t left = st->e->compSize - st->compRead;
            size_t chunk = left > ZIP_CHUNK ? ZIP_CHUNK : (size_t)left;
            if (apak_zipPread(st->z, st->in, chunk, st->data + st->compRead) != (long)chunk) return -1;
            st->compRead += chunk;
            st->s.next_in = st->in;
            st->s.avail_in = (unsigned)chunk;
        }
        st->s.next_out = buf + produced;
        st->s.avail_out = (unsigned)((n - produced) > 0x40000000u ? 0x40000000u : (n - produced));
        before = st->s.avail_out;
        rc = Z.inflate(&st->s, APAK_Z_NO_FLUSH);
        produced += before - st->s.avail_out;
        if (rc == APAK_Z_STREAM_END) st->ended = 1;
        else if (rc != APAK_Z_OK && rc != APAK_Z_BUF_ERROR) return -1;
        else if (before == st->s.avail_out && st->s.avail_in == 0 && st->compRead >= st->e->compSize) st->ended = 1;
    }
    st->pos += produced;
    return (long)produced;
}

long apak_zipStreamRead(ApakZipStream *st, void *buf, size_t n) {
    if (!st || !buf) return -1;
    if (st->pos >= st->e->size) return 0;
    if (n > st->e->size - st->pos) n = (size_t)(st->e->size - st->pos);
    if (st->e->method == 0) {
        long got = apak_zipPread(st->z, buf, n, st->data + st->pos);
        if (got > 0) st->pos += (uint64_t)got;
        return got;
    }
    return inflateSome(st, (uint8_t *)buf, n);
}

int64_t apak_zipStreamSeek(ApakZipStream *st, int64_t offset, int whence) {
    int64_t target;
    if (!st) return -1;
    target = whence == 0 ? offset : whence == 1 ? (int64_t)st->pos + offset : (int64_t)st->e->size + offset;
    if (target < 0 || (uint64_t)target > st->e->size) return -1;
    if (st->e->method == 0) {
        st->pos = (uint64_t)target;
        return target;
    }
    if ((uint64_t)target < st->pos && streamRestart(st) != 0) return -1;
    while (st->pos < (uint64_t)target) {
        uint8_t skip[4096];
        uint64_t want = (uint64_t)target - st->pos;
        long got = inflateSome(st, skip, want > sizeof skip ? sizeof skip : (size_t)want);
        if (got <= 0) return -1;
    }
    return (int64_t)st->pos;
}

int64_t apak_zipStreamTell(const ApakZipStream *st) { return st ? (int64_t)st->pos : -1; }

void apak_zipStreamClose(ApakZipStream *st) {
    if (!st) return;
    if (st->inflating) Z.inflateEnd(&st->s);
    free(st->in);
    free(st);
}

#else /* _WIN32: the legacy smoke subset reads staged files only */

ApakZip *apak_zipOpen(const char *path, char *err, size_t cap) {
    (void)path;
    if (err && cap) snprintf(err, cap, "reading the APK directly is not supported on this build");
    return NULL;
}
void apak_zipClose(ApakZip *z) { (void)z; }
const char *apak_zipPath(const ApakZip *z) { (void)z; return ""; }
size_t apak_zipCount(const ApakZip *z) { (void)z; return 0; }
const ApakZipEntry *apak_zipAt(const ApakZip *z, size_t index) { (void)z; (void)index; return NULL; }
const ApakZipEntry *apak_zipFind(const ApakZip *z, const char *name) { (void)z; (void)name; return NULL; }
int apak_zipDataOffset(ApakZip *z, const ApakZipEntry *e, uint64_t *offset) {
    (void)z; (void)e; (void)offset;
    return -1;
}
long apak_zipPread(ApakZip *z, void *buf, size_t n, uint64_t offset) {
    (void)z; (void)buf; (void)n; (void)offset;
    return -1;
}
int apak_zipDupFd(ApakZip *z) { (void)z; return -1; }
void *apak_zipReadAll(ApakZip *z, const ApakZipEntry *e, size_t maxBytes, size_t *outLen, char *err, size_t cap) {
    (void)z; (void)e; (void)maxBytes;
    if (outLen) *outLen = 0;
    if (err && cap) snprintf(err, cap, "not supported on this build");
    return NULL;
}
ApakZipStream *apak_zipStreamOpen(ApakZip *z, const ApakZipEntry *e) { (void)z; (void)e; return NULL; }
long apak_zipStreamRead(ApakZipStream *s, void *buf, size_t n) { (void)s; (void)buf; (void)n; return -1; }
int64_t apak_zipStreamSeek(ApakZipStream *s, int64_t offset, int whence) {
    (void)s; (void)offset; (void)whence;
    return -1;
}
int64_t apak_zipStreamTell(const ApakZipStream *s) { (void)s; return -1; }
void apak_zipStreamClose(ApakZipStream *s) { (void)s; }

#endif /* !_WIN32 */
