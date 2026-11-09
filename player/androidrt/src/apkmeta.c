/* apkmeta.c — see apkmeta.h.
 *
 * AXML ("binary XML", what aapt/aapt2 compile AndroidManifest.xml into) is
 * a chunk stream: a string pool, a resource-id map that gives android:*
 * attribute names their framework ids (0x01010003 = android:name), then
 * start/end element chunks whose attributes carry typed values. Attributes
 * are matched by resource id first and by name second, so manifests whose
 * attribute-name strings were stripped by an obfuscator still parse.
 *
 * DEX: the header's string_ids/type_ids/class_defs tables are enough to name
 * every class an APK defines and its superclass; method bodies are never
 * looked at (nothing here executes Java).
 */
#define _GNU_SOURCE
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "apkmeta.h"

#define AXML_MAX_BYTES (8u << 20)
#define DEX_MAX_BYTES  (96u << 20)

static uint16_t rd16(const uint8_t *p) { return (uint16_t)(p[0] | (p[1] << 8)); }
static uint32_t rd32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

/* ── AXML ───────────────────────────────────────────────────────────── */

#define RES_STRING_POOL      0x0001
#define RES_XML              0x0003
#define RES_XML_START_NS     0x0100
#define RES_XML_END_NS       0x0101
#define RES_XML_START        0x0102
#define RES_XML_END          0x0103
#define RES_XML_RESOURCE_MAP 0x0180

#define ATTR_NAME            0x01010003u
#define ATTR_VALUE           0x01010024u
#define ATTR_VERSION_CODE    0x0101021bu
#define ATTR_VERSION_NAME    0x0101021cu
#define ATTR_MIN_SDK         0x0101020cu
#define ATTR_TARGET_SDK      0x01010270u
#define ATTR_TARGET_ACTIVITY 0x01010202u

#define TYPE_REFERENCE 0x01
#define TYPE_STRING    0x03
#define TYPE_INT_DEC   0x10
#define TYPE_INT_HEX   0x11

typedef struct {
    const uint8_t *base; /* string pool chunk */
    uint32_t size;
    uint32_t count;
    uint32_t stringsStart;
    int utf8;
    const uint32_t *ids; /* resource map (little-endian words) */
    uint32_t nids;
} Axml;

/* String #idx into out (UTF-8, truncated to cap). */
static int poolString(const Axml *a, uint32_t idx, char *out, size_t cap) {
    uint32_t off;
    const uint8_t *p, *end;
    size_t n = 0;
    if (!cap) return -1;
    out[0] = '\0';
    if (!a->base || idx >= a->count) return -1;
    off = rd32(a->base + 28 + 4u * idx);
    if ((uint64_t)a->stringsStart + off >= a->size) return -1;
    p = a->base + a->stringsStart + off;
    end = a->base + a->size;
    if (a->utf8) {
        uint32_t len;
        if (p >= end) return -1;
        p += (*p & 0x80) ? 2 : 1; /* UTF-16 length */
        if (p >= end) return -1;
        if (*p & 0x80) {
            if (p + 1 >= end) return -1;
            len = ((uint32_t)(p[0] & 0x7f) << 8) | p[1];
            p += 2;
        } else {
            len = *p++;
        }
        while (len-- && p < end && n + 1 < cap) out[n++] = (char)*p++;
    } else {
        uint32_t len;
        if (p + 2 > end) return -1;
        len = rd16(p);
        p += 2;
        if (len & 0x8000) {
            if (p + 2 > end) return -1;
            len = ((len & 0x7fff) << 16) | rd16(p);
            p += 2;
        }
        while (len-- && p + 2 <= end) {
            uint32_t c = rd16(p);
            p += 2;
            if (c >= 0xd800 && c < 0xdc00 && len && p + 2 <= end) { /* surrogate pair */
                uint32_t lo = rd16(p);
                if (lo >= 0xdc00 && lo < 0xe000) {
                    c = 0x10000 + ((c - 0xd800) << 10) + (lo - 0xdc00);
                    p += 2;
                    len--;
                }
            }
            if (c < 0x80) {
                if (n + 1 >= cap) break;
                out[n++] = (char)c;
            } else if (c < 0x800) {
                if (n + 2 >= cap) break;
                out[n++] = (char)(0xc0 | (c >> 6));
                out[n++] = (char)(0x80 | (c & 0x3f));
            } else if (c < 0x10000) {
                if (n + 3 >= cap) break;
                out[n++] = (char)(0xe0 | (c >> 12));
                out[n++] = (char)(0x80 | ((c >> 6) & 0x3f));
                out[n++] = (char)(0x80 | (c & 0x3f));
            } else {
                if (n + 4 >= cap) break;
                out[n++] = (char)(0xf0 | (c >> 18));
                out[n++] = (char)(0x80 | ((c >> 12) & 0x3f));
                out[n++] = (char)(0x80 | ((c >> 6) & 0x3f));
                out[n++] = (char)(0x80 | (c & 0x3f));
            }
        }
    }
    out[n] = '\0';
    return 0;
}

typedef struct {
    uint32_t id;      /* resource id of the attribute name, 0 if none */
    char name[64];    /* attribute name string */
    uint8_t type;     /* Res_value dataType */
    uint32_t data;
    uint32_t raw;     /* raw string index or 0xffffffff */
} Attr;

static int attrIs(const Attr *at, uint32_t id, const char *name) {
    return (id && at->id == id) || strcmp(at->name, name) == 0;
}

/* String value of an attribute ("" for references and non-strings). */
static void attrString(const Axml *a, const Attr *at, char *out, size_t cap) {
    out[0] = '\0';
    if (at->type == TYPE_STRING) poolString(a, at->data, out, cap);
    else if (at->raw != 0xffffffffu) poolString(a, at->raw, out, cap);
    else if (at->type == TYPE_INT_DEC || at->type == TYPE_INT_HEX) snprintf(out, cap, "%d", (int32_t)at->data);
}

static int64_t attrInt(const Axml *a, const Attr *at) {
    char buf[32];
    if (at->type == TYPE_INT_DEC || at->type == TYPE_INT_HEX) return (int32_t)at->data;
    attrString(a, at, buf, sizeof buf);
    return buf[0] ? strtoll(buf, NULL, 10) : 0;
}

/* The fully qualified class name; "" (unknown) when it does not fit — a cut
 * name would name a class the APK does not have. */
static void qualify(const char *pkg, const char *name, char *out, size_t cap) {
    int n;
    if (name[0] == '.') n = snprintf(out, cap, "%s%s", pkg, name);
    else if (!strchr(name, '.')) n = snprintf(out, cap, "%s.%s", pkg, name);
    else n = snprintf(out, cap, "%s", name);
    if (n < 0 || (size_t)n >= cap) out[0] = '\0';
}

int apak_manifestParse(const uint8_t *data, size_t len, ApakManifest *out, char *err, size_t cap) {
    Axml a;
    size_t pos;
    int depth = 0, inActivity = 0, activityDepth = 0, haveLauncher = 0, sawMain = 0, sawLauncher = 0;
    char curActivity[512] = "", curLib[128] = "", curFunc[128] = "";
    char firstActivity[512] = "", firstLib[128] = "", firstFunc[128] = "";
    char libActivity[512] = "", libLib[128] = "", libFunc[128] = "";
    char localErr[96];
    if (!err || !cap) {
        err = localErr;
        cap = sizeof localErr;
    }
    memset(out, 0, sizeof *out);
    memset(&a, 0, sizeof a);
    if (!data || len < 8 || rd16(data) != RES_XML) {
        snprintf(err, cap, "not a binary XML manifest");
        return -1;
    }
    pos = rd16(data + 2);
    while (pos + 8 <= len) {
        const uint8_t *c = data + pos;
        uint16_t type = rd16(c), hsize = rd16(c + 2);
        uint32_t size = rd32(c + 4);
        if (size < 8 || pos + size > len || hsize > size) break;
        if (type == RES_STRING_POOL && size >= 28) {
            a.base = c;
            a.size = size;
            a.count = rd32(c + 8);
            a.utf8 = (rd32(c + 16) & 0x100) != 0;
            a.stringsStart = rd32(c + 20);
            if (28u + 4ull * a.count > size) a.count = 0;
        } else if (type == RES_XML_RESOURCE_MAP) {
            a.ids = (const uint32_t *)(c + hsize);
            a.nids = (size - hsize) / 4;
        } else if (type == RES_XML_START && size >= 36) {
            const uint8_t *ext = c + hsize;
            char elem[64];
            uint16_t attrStart = rd16(ext + 8), attrSize = rd16(ext + 10), attrCount = rd16(ext + 12);
            Attr attrs[24];
            int n = 0, i;
            depth++;
            poolString(&a, rd32(ext + 4), elem, sizeof elem);
            for (i = 0; i < attrCount && n < 24; i++) {
                const uint8_t *at = ext + attrStart + (size_t)i * attrSize;
                uint32_t nameIdx;
                if (attrSize < 20 || at + 20 > c + size) break;
                nameIdx = rd32(at + 4);
                memset(&attrs[n], 0, sizeof attrs[n]);
                attrs[n].id = (a.ids && nameIdx < a.nids) ? rd32((const uint8_t *)&a.ids[nameIdx]) : 0;
                poolString(&a, nameIdx, attrs[n].name, sizeof attrs[n].name);
                attrs[n].raw = rd32(at + 8);
                attrs[n].type = at[15];
                attrs[n].data = rd32(at + 16);
                n++;
            }
            if (strcmp(elem, "manifest") == 0) {
                for (i = 0; i < n; i++) {
                    if (attrIs(&attrs[i], 0, "package")) attrString(&a, &attrs[i], out->package, sizeof out->package);
                    else if (attrIs(&attrs[i], ATTR_VERSION_CODE, "versionCode")) out->versionCode = attrInt(&a, &attrs[i]);
                    else if (attrIs(&attrs[i], ATTR_VERSION_NAME, "versionName"))
                        attrString(&a, &attrs[i], out->versionName, sizeof out->versionName);
                }
            } else if (strcmp(elem, "uses-sdk") == 0) {
                for (i = 0; i < n; i++) {
                    if (attrIs(&attrs[i], ATTR_MIN_SDK, "minSdkVersion")) out->minSdk = (int)attrInt(&a, &attrs[i]);
                    else if (attrIs(&attrs[i], ATTR_TARGET_SDK, "targetSdkVersion"))
                        out->targetSdk = (int)attrInt(&a, &attrs[i]);
                }
            } else if (!inActivity && (strcmp(elem, "activity") == 0 || strcmp(elem, "activity-alias") == 0)) {
                char name[512] = "", target[512] = "";
                int alias = elem[8] == '-';
                for (i = 0; i < n; i++) {
                    if (attrIs(&attrs[i], ATTR_NAME, "name")) attrString(&a, &attrs[i], name, sizeof name);
                    else if (attrIs(&attrs[i], ATTR_TARGET_ACTIVITY, "targetActivity"))
                        attrString(&a, &attrs[i], target, sizeof target);
                }
                inActivity = 1;
                activityDepth = depth;
                sawMain = sawLauncher = 0;
                curLib[0] = curFunc[0] = '\0';
                qualify(out->package, alias && target[0] ? target : name, curActivity, sizeof curActivity);
            } else if (inActivity && (strcmp(elem, "action") == 0 || strcmp(elem, "category") == 0)) {
                for (i = 0; i < n; i++) {
                    char v[128];
                    if (!attrIs(&attrs[i], ATTR_NAME, "name")) continue;
                    attrString(&a, &attrs[i], v, sizeof v);
                    if (strcmp(v, "android.intent.action.MAIN") == 0) sawMain = 1;
                    if (strcmp(v, "android.intent.category.LAUNCHER") == 0 ||
                        strcmp(v, "android.intent.category.LEANBACK_LAUNCHER") == 0)
                        sawLauncher = 1;
                }
            } else if (inActivity && strcmp(elem, "meta-data") == 0) {
                char key[128] = "", value[128] = "";
                for (i = 0; i < n; i++) {
                    if (attrIs(&attrs[i], ATTR_NAME, "name")) attrString(&a, &attrs[i], key, sizeof key);
                    else if (attrIs(&attrs[i], ATTR_VALUE, "value")) attrString(&a, &attrs[i], value, sizeof value);
                }
                if (strcmp(key, "android.app.lib_name") == 0) snprintf(curLib, sizeof curLib, "%s", value);
                else if (strcmp(key, "android.app.func_name") == 0) snprintf(curFunc, sizeof curFunc, "%s", value);
            }
        } else if (type == RES_XML_END) {
            if (inActivity && depth == activityDepth) {
                inActivity = 0;
                if (!firstActivity[0]) {
                    snprintf(firstActivity, sizeof firstActivity, "%s", curActivity);
                    snprintf(firstLib, sizeof firstLib, "%s", curLib);
                    snprintf(firstFunc, sizeof firstFunc, "%s", curFunc);
                }
                if (!libActivity[0] && curLib[0]) {
                    snprintf(libActivity, sizeof libActivity, "%s", curActivity);
                    snprintf(libLib, sizeof libLib, "%s", curLib);
                    snprintf(libFunc, sizeof libFunc, "%s", curFunc);
                }
                if (!haveLauncher && sawMain && sawLauncher) {
                    haveLauncher = 1;
                    snprintf(out->activity, sizeof out->activity, "%s", curActivity);
                    snprintf(out->libName, sizeof out->libName, "%s", curLib);
                    snprintf(out->funcName, sizeof out->funcName, "%s", curFunc);
                }
            }
            if (depth > 0) depth--;
        }
        pos += size;
    }
    if (!out->package[0]) {
        snprintf(err, cap, "manifest names no package");
        return -1;
    }
    if (!out->activity[0]) {
        const char *act = libActivity[0] ? libActivity : firstActivity;
        snprintf(out->activity, sizeof out->activity, "%s", act);
        snprintf(out->libName, sizeof out->libName, "%s", libActivity[0] ? libLib : firstLib);
        snprintf(out->funcName, sizeof out->funcName, "%s", libActivity[0] ? libFunc : firstFunc);
    }
    return 0;
}

int apak_manifestLoad(const char *stagedRoot, ApakZip *apk, ApakManifest *out, char *err, size_t cap) {
    uint8_t *data = NULL;
    size_t len = 0;
    int rc;
    char localErr[160];
    if (!err || !cap) {
        err = localErr;
        cap = sizeof localErr;
    }
    err[0] = '\0';
    if (stagedRoot && *stagedRoot) {
        char path[2048];
        FILE *f;
        snprintf(path, sizeof path, "%s/AndroidManifest.xml", stagedRoot);
        f = fopen(path, "rb");
        if (f) {
            long size;
            if (fseek(f, 0, SEEK_END) == 0 && (size = ftell(f)) > 0 && size < (long)AXML_MAX_BYTES &&
                fseek(f, 0, SEEK_SET) == 0) {
                data = (uint8_t *)malloc((size_t)size);
                if (data && fread(data, 1, (size_t)size, f) == (size_t)size) len = (size_t)size;
                else {
                    free(data);
                    data = NULL;
                }
            }
            fclose(f);
        }
    }
    if (!data && apk) {
        const ApakZipEntry *e = apak_zipFind(apk, "AndroidManifest.xml");
        if (e) data = (uint8_t *)apak_zipReadAll(apk, e, AXML_MAX_BYTES, &len, err, cap);
    }
    if (!data) {
        if (!err[0]) snprintf(err, cap, "no AndroidManifest.xml (staged or in the APK)");
        return -1;
    }
    rc = apak_manifestParse(data, len, out, err, cap);
    free(data);
    return rc;
}

/* ── DEX class index ────────────────────────────────────────────────── */

typedef struct {
    const char *name;
    const char *super; /* NULL: java/lang/Object or none */
} DexClass;

struct ApakDexIndex {
    DexClass *classes;
    size_t count, cap;
    char **pools;      /* 64 KiB name chunks */
    size_t npools;
    size_t poolUsed;   /* bytes used in the last chunk */
    size_t files;
};

#define DEX_POOL_CHUNK (64u << 10)

static int classCmp(const void *a, const void *b) {
    return strcmp(((const DexClass *)a)->name, ((const DexClass *)b)->name);
}

static char *poolAlloc(ApakDexIndex *ix, size_t n) {
    char *chunk;
    if (n > DEX_POOL_CHUNK) return NULL;
    if (!ix->npools || ix->poolUsed + n > DEX_POOL_CHUNK) {
        char **grown = (char **)realloc(ix->pools, (ix->npools + 1) * sizeof *grown);
        if (!grown) return NULL;
        ix->pools = grown;
        chunk = (char *)malloc(DEX_POOL_CHUNK);
        if (!chunk) return NULL;
        ix->pools[ix->npools++] = chunk;
        ix->poolUsed = 0;
    }
    chunk = ix->pools[ix->npools - 1] + ix->poolUsed;
    ix->poolUsed += n;
    return chunk;
}

/* "Lcom/foo/Bar;" → "com/foo/Bar", copied into the index's pool. */
static const char *dexTypeName(ApakDexIndex *ix, const uint8_t *dex, size_t len, uint32_t typeIdx) {
    uint32_t typeIdsSize = rd32(dex + 0x40), typeIdsOff = rd32(dex + 0x44);
    uint32_t stringIdsSize = rd32(dex + 0x38), stringIdsOff = rd32(dex + 0x3c);
    uint32_t descIdx, strOff;
    const uint8_t *p, *q, *end = dex + len;
    char *out;
    size_t n;
    if (typeIdx >= typeIdsSize || (uint64_t)typeIdsOff + 4ull * typeIdx + 4 > len) return NULL;
    descIdx = rd32(dex + typeIdsOff + 4u * typeIdx);
    if (descIdx >= stringIdsSize || (uint64_t)stringIdsOff + 4ull * descIdx + 4 > len) return NULL;
    strOff = rd32(dex + stringIdsOff + 4u * descIdx);
    if (strOff >= len) return NULL;
    p = dex + strOff;
    while (p < end && (*p & 0x80)) p++; /* skip the uleb128 utf16 length */
    p++;
    if (p >= end || *p != 'L') return NULL; /* arrays and primitives are not classes */
    p++;
    for (q = p; q < end && *q && *q != ';'; q++) {
    }
    n = (size_t)(q - p);
    out = poolAlloc(ix, n + 1);
    if (!out) return NULL;
    memcpy(out, p, n);
    out[n] = '\0';
    return out;
}

static int indexDex(ApakDexIndex *ix, const uint8_t *dex, size_t len) {
    uint32_t count, off, i;
    if (len < 0x70 || memcmp(dex, "dex\n", 4) != 0) return -1;
    count = rd32(dex + 0x60);
    off = rd32(dex + 0x64);
    if ((uint64_t)off + 32ull * count > len) return -1;
    for (i = 0; i < count; i++) {
        const uint8_t *def = dex + off + 32u * i;
        uint32_t superIdx = rd32(def + 8);
        const char *name = dexTypeName(ix, dex, len, rd32(def));
        const char *super = superIdx == 0xffffffffu ? NULL : dexTypeName(ix, dex, len, superIdx);
        if (!name) continue;
        if (super && strcmp(super, "java/lang/Object") == 0) super = NULL;
        if (ix->count == ix->cap) {
            size_t ncap = ix->cap ? ix->cap * 2 : 1024;
            DexClass *grown = (DexClass *)realloc(ix->classes, ncap * sizeof *grown);
            if (!grown) return -1;
            ix->classes = grown;
            ix->cap = ncap;
        }
        ix->classes[ix->count].name = name;
        ix->classes[ix->count].super = super;
        ix->count++;
    }
    ix->files++;
    return 0;
}

ApakDexIndex *apak_dexIndexBuild(ApakZip *apk, char *err, size_t cap) {
    ApakDexIndex *ix;
    int i;
    char localErr[160];
    if (!err || !cap) {
        err = localErr;
        cap = sizeof localErr;
    }
    err[0] = '\0';
    if (!apk) {
        snprintf(err, cap, "no APK");
        return NULL;
    }
    ix = (ApakDexIndex *)calloc(1, sizeof *ix);
    if (!ix) return NULL;
    for (i = 1; i < 64; i++) {
        char name[32];
        const ApakZipEntry *e;
        uint8_t *dex;
        size_t len = 0;
        if (i == 1) snprintf(name, sizeof name, "classes.dex");
        else snprintf(name, sizeof name, "classes%d.dex", i);
        e = apak_zipFind(apk, name);
        if (!e) break;
        dex = (uint8_t *)apak_zipReadAll(apk, e, DEX_MAX_BYTES, &len, err, cap);
        if (!dex) break;
        if (indexDex(ix, dex, len) != 0 && !err[0]) snprintf(err, cap, "%s is not a DEX file", name);
        free(dex);
    }
    if (!ix->count) {
        if (!err[0]) snprintf(err, cap, "the APK has no classes.dex");
        apak_dexFree(ix);
        return NULL;
    }
    qsort(ix->classes, ix->count, sizeof *ix->classes, classCmp);
    return ix;
}

int apak_dexLookup(const ApakDexIndex *ix, const char *className, const char **superName) {
    DexClass key, *hit;
    if (superName) *superName = NULL;
    if (!ix || !className) return 0;
    key.name = className;
    key.super = NULL;
    hit = (DexClass *)bsearch(&key, ix->classes, ix->count, sizeof key, classCmp);
    if (!hit) return 0;
    if (superName) *superName = hit->super;
    return 1;
}

size_t apak_dexCount(const ApakDexIndex *ix) { return ix ? ix->count : 0; }
size_t apak_dexFiles(const ApakDexIndex *ix) { return ix ? ix->files : 0; }

void apak_dexFree(ApakDexIndex *ix) {
    size_t i;
    if (!ix) return;
    for (i = 0; i < ix->npools; i++) free(ix->pools[i]);
    free(ix->pools);
    free(ix->classes);
    free(ix);
}
