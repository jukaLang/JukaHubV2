/* apkzip.h — read-only access to the APK itself (a zip archive).
 *
 * The runtime reads three things straight out of the APK on the SD card
 * instead of from the RAM staging dir: assets/ (a large game's assets do not
 * fit the 1 GB device's tmpfs — Minecraft ships several hundred MB),
 * AndroidManifest.xml (package and activity names) and classes*.dex (which
 * Java classes the app defines — the JNI layer's notion of "this class
 * exists"). Stored entries are read in place with pread; deflated ones are
 * inflated through the device's own zlib (dlopen("libz.so.1"), no headers
 * or link-time dependency). ZIP64 central directories are supported.
 */
#ifndef ANDROIDRT_APKZIP_H
#define ANDROIDRT_APKZIP_H

#include <stddef.h>
#include <stdint.h>

typedef struct ApakZip ApakZip;

typedef struct ApakZipEntry {
    const char *name;       /* NUL-terminated, inside the archive's name pool */
    uint32_t nameLen;
    uint16_t method;        /* 0 stored, 8 deflate */
    uint16_t flags;         /* general purpose bit flags (bit 0: encrypted) */
    uint64_t compSize;
    uint64_t size;          /* uncompressed */
    uint64_t localOffset;   /* local file header */
} ApakZipEntry;

/* Open and index the central directory. NULL with a reason in err. */
ApakZip *apak_zipOpen(const char *path, char *err, size_t cap);
void apak_zipClose(ApakZip *z);
const char *apak_zipPath(const ApakZip *z);

size_t apak_zipCount(const ApakZip *z);
const ApakZipEntry *apak_zipAt(const ApakZip *z, size_t index);
const ApakZipEntry *apak_zipFind(const ApakZip *z, const char *name);

/* Offset of the entry's data in the file (reads its local header). */
int apak_zipDataOffset(ApakZip *z, const ApakZipEntry *e, uint64_t *offset);

/* Bytes of the archive file itself (thread-safe; pread on POSIX). */
long apak_zipPread(ApakZip *z, void *buf, size_t n, uint64_t offset);

/* A descriptor of the archive file (POSIX: dup'ed, caller closes; -1 when
 * not available) — AAsset_openFileDescriptor hands this out for stored
 * assets with the entry's data offset, exactly like Android. */
int apak_zipDupFd(ApakZip *z);

/* The whole entry, inflated, in a malloc'd buffer (NUL-terminated beyond
 * *outLen for convenience). maxBytes caps the uncompressed size. */
void *apak_zipReadAll(ApakZip *z, const ApakZipEntry *e, size_t maxBytes, size_t *outLen, char *err, size_t cap);

/* Sequential reader with seek for one entry (stored or deflated). */
typedef struct ApakZipStream ApakZipStream;
ApakZipStream *apak_zipStreamOpen(ApakZip *z, const ApakZipEntry *e);
long apak_zipStreamRead(ApakZipStream *s, void *buf, size_t n);
/* whence: 0 SET, 1 CUR, 2 END. Returns the new position or -1. */
int64_t apak_zipStreamSeek(ApakZipStream *s, int64_t offset, int whence);
int64_t apak_zipStreamTell(const ApakZipStream *s);
void apak_zipStreamClose(ApakZipStream *s);

/* Which zlib the runtime inflates with ("" when none could be loaded). */
const char *apak_zipInflateLib(void);

#endif /* ANDROIDRT_APKZIP_H */
