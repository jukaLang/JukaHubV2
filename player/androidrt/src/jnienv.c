/* jnienv.c — the JavaVM and JNIEnv AndroidRT hands to games (see jni.h).
 *
 * Object model
 *   Every handle a game holds (jobject, jclass, jstring, jarray, jthrowable)
 *   is a pointer to an ApakJObject. Classes are objects too (their header is
 *   the first member of ApakJClass), so a jclass works wherever a jobject
 *   does. Lifetime is reference counting: a local reference in the calling
 *   thread's current frame, global/weak references, object-array slots and
 *   object fields each hold one count. An object whose count reaches zero is
 *   torn down but its memory parks in a quarantine ring (4096 entries) before
 *   it is freed, so a game that keeps using a reference it already deleted
 *   reads a dead object (logged once, treated as null) instead of freed
 *   memory. Classes, the activity object and interned constants are
 *   permanent.
 *
 * Threads
 *   JNIEnv is per thread (pthread key); the main thread's is the one
 *   ANativeActivity carries. GetEnv on a thread nobody attached attaches it
 *   (Android code calls GetEnv from threads it created itself and would
 *   otherwise crash on the JNI_EDETACHED path it never tested).
 *
 * Calls
 *   Call*Method(V/A) converts its arguments by the method descriptor (float
 *   varargs arrive as double), resolves virtual overrides by name+descriptor
 *   from the object's class upwards, and runs the C implementation, a native
 *   the game registered for that Java method, or — for phantom members — the
 *   descriptor's default. Natives the runtime itself calls (nativeRegisterThis,
 *   GameActivity's onStartNative ...) go through RegisterNatives tables first
 *   and the exported Java_<class>_<method> symbols second, invoked with
 *   integer and floating arguments in their AAPCS64 / SysV registers.
 */
#define _GNU_SOURCE
#include <ctype.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "actbridge.h"
#include "jni.h"

#define JNI_TAG "[androidrt][jni] "
#define JNI_SLOTS 233 /* reserved0..3 + GetVersion .. GetObjectRefType */

#define OBJ_MAGIC   0x4f4a5041u /* "APJO" */
#define DEAD_MAGIC  0x44414544u /* "DEAD" */
#define METH_MAGIC  0x4d4a5041u
#define FIELD_MAGIC 0x464a5041u
#define ENV_MAGIC   0x454a5041u

enum { K_INSTANCE = 1, K_CLASS, K_STRING, K_ARRAY, K_DBUF };
#define F_PERMANENT 1u

enum { CALL_VIRTUAL = 0, CALL_NONVIRTUAL, CALL_STATIC };

typedef struct ApakJField ApakJField;

typedef struct PhantomSlot {
    ApakJField *field;
    ApakJValue v;
    struct PhantomSlot *next;
} PhantomSlot;

struct ApakJObject {
    uint32_t magic;
    uint16_t kind;
    uint16_t flags;
    ApakJClass *cls;
    int32_t refs;
    int32_t globals;
    ApakJValue *fields;
    int32_t nfields;
    PhantomSlot *extra;
    void *payload;
    void (*freePayload)(void *);
    pthread_mutex_t *monitor;
    union {
        struct {
            uint16_t *chars;
            int32_t len;
            char *utf;
            int32_t utfLen;
        } s;
        struct {
            char elem;
            int32_t len;
            void *data;
        } a;
        struct {
            void *addr;
            int64_t cap;
        } d;
    } u;
};

typedef struct NativeReg {
    char *name;
    char *sig;
    void *fn;
} NativeReg;

struct ApakJClass {
    ApakJObject obj; /* MUST be first: a jclass is a jobject */
    char *name;
    char *superName;
    ApakJClass *super;
    int superResolved;
    int phantom;     /* nothing implements it                         */
    int fromApk;     /* the APK's DEX defines it                       */
    int stringKind;  /* instances are strings (java/lang/String)       */
    ApakJMethod **methods;
    size_t nmethods, capMethods;
    ApakJField **fields;
    size_t nfields, capFields;
    int linked;
    int32_t instanceFields;
    NativeReg *natives;
    size_t nnatives, capNatives;
    char arrayElem;  /* array classes: 'Z'..'D' or 'L'                  */
    ApakJClass *next;
};

struct ApakJMethod {
    uint32_t magic;
    ApakJClass *cls;
    char *name;
    char *sig;
    int isStatic;
    int phantom;
    char ret;
    int nargs;
    char args[APAK_JNI_MAXARGS];
    ApakJImpl impl;
    int calls;
};

struct ApakJField {
    uint32_t magic;
    ApakJClass *cls;
    char *name;
    char *sig;
    int isStatic;
    int phantom;
    char type;
    int32_t slot;      /* instance field slot, -1: stored per object in extra */
    ApakJValue value;  /* static value */
};

typedef struct ApakJEnv {
    const void *functions; /* MUST be first: JNIEnv* points at the table pointer */
    uint32_t magic;
    ApakJObject *pending;
    ApakJObject **locals;
    size_t nlocals, capLocals;
    size_t *frames;
    size_t nframes, capFrames;
    int isMain;
} ApakJEnv;

typedef struct {
    const char *name;
    const char *signature;
    void *fnPtr;
} JNINativeMethodAbi;

/* ── globals ─────────────────────────────────────────────────────────── */

static pthread_mutex_t g_lock;
static pthread_once_t g_lockOnce = PTHREAD_ONCE_INIT;
static pthread_key_t g_envKey;
static ApakJEnv *g_mainEnv;
static int g_ready;
static int g_trace;
static int g_unknownPhantom;

#define CLASS_BUCKETS 4096
static ApakJClass *g_classes[CLASS_BUCKETS];
static ApakJClass *g_objectClass, *g_classClass, *g_stringClass, *g_throwableClass;

static ApakJniConfig g_cfg;
static const ApakJniPack *g_packs[16];
static int g_npacks;
static const ApakJniPack *g_active[16];
static int g_nactive;
static ApakJObject *g_activity;

#define QUARANTINE 4096
static ApakJObject *g_dead[QUARANTINE];
static size_t g_deadPos;

static const void *g_table[JNI_SLOTS];
static const void *g_invoke[8];
static const void *g_vmPtr = g_invoke; /* JavaVM*: pointer to the invoke table pointer */

static void initLock(void) {
    pthread_mutexattr_t a;
    pthread_mutexattr_init(&a);
    pthread_mutexattr_settype(&a, PTHREAD_MUTEX_RECURSIVE);
    pthread_mutex_init(&g_lock, &a);
    pthread_mutexattr_destroy(&a);
}
static void lock(void) {
    pthread_once(&g_lockOnce, initLock);
    pthread_mutex_lock(&g_lock);
}
static void unlock(void) { pthread_mutex_unlock(&g_lock); }

/* ── logging: once per key ──────────────────────────────────────────── */

typedef struct LogKey {
    char *key;
    struct LogKey *next;
} LogKey;
#define LOG_BUCKETS 2048
static LogKey *g_logKeys[LOG_BUCKETS];
static size_t g_logCount;
static pthread_mutex_t g_logLock = PTHREAD_MUTEX_INITIALIZER;

static uint32_t strHash(const char *s) {
    uint32_t h = 2166136261u;
    while (*s) {
        h ^= (uint8_t)*s++;
        h *= 16777619u;
    }
    return h;
}

/* 1 the first time a key is seen. */
static int firstTime(const char *key) {
    uint32_t b = strHash(key) % LOG_BUCKETS;
    LogKey *k;
    int first = 0;
    pthread_mutex_lock(&g_logLock);
    for (k = g_logKeys[b]; k; k = k->next)
        if (strcmp(k->key, key) == 0) break;
    if (!k && g_logCount < 50000) {
        k = (LogKey *)malloc(sizeof *k);
        if (k) {
            k->key = strdup(key);
            k->next = g_logKeys[b];
            g_logKeys[b] = k;
            g_logCount++;
            first = 1;
        }
    }
    pthread_mutex_unlock(&g_logLock);
    return first;
}

void apak_jniLogOnce(const char *key, const char *fmt, ...) {
    va_list ap;
    if (!firstTime(key)) return;
    va_start(ap, fmt);
    fputs(JNI_TAG, stderr);
    vfprintf(stderr, fmt, ap);
    fputc('\n', stderr);
    va_end(ap);
}

/* ── the compatibility report ───────────────────────────────────────── */

typedef struct ReportItem {
    char *text;
    int called;
    struct ReportItem *next;
} ReportItem;
static ReportItem *g_unknownClasses, *g_apkClasses, *g_phantomMembers;
static size_t g_nUnknownClasses, g_nApkClasses, g_nPhantomMembers, g_nPhantomCalls;

static void reportAdd(ReportItem **list, size_t *count, const char *text) {
    ReportItem *r = (ReportItem *)calloc(1, sizeof *r);
    if (!r) return;
    r->text = strdup(text);
    r->next = *list;
    *list = r;
    (*count)++;
}

/* ── descriptors ─────────────────────────────────────────────────────── */

/* "(ILjava/lang/String;[F)V" → args "ILL", ret 'V'. -1 when malformed. */
static int parseSig(const char *sig, char *args, int *nargs, char *ret) {
    const char *p = sig;
    int n = 0;
    *nargs = 0;
    *ret = 'V';
    if (!p || *p != '(') return -1;
    p++;
    while (*p && *p != ')') {
        int isArray = *p == '[';
        char t;
        while (*p == '[') p++;
        if (*p == 'L') {
            const char *semi = strchr(p, ';');
            if (!semi) return -1;
            p = semi + 1;
            t = 'L';
        } else if (*p && strchr("ZBCSIJFD", *p)) {
            t = isArray ? 'L' : *p; /* arrays of anything are references */
            p++;
        } else {
            return -1;
        }
        if (n < APAK_JNI_MAXARGS) args[n] = t;
        n++;
    }
    if (*p != ')') return -1;
    p++;
    if (*p == '[' || *p == 'L') *ret = 'L';
    else if (*p && strchr("ZBCSIJFDV", *p)) *ret = *p;
    else return -1;
    if (n > APAK_JNI_MAXARGS) return -1;
    *nargs = n;
    return 0;
}

static char fieldType(const char *sig) {
    if (!sig || !*sig) return 'L';
    if (*sig == '[' || *sig == 'L') return 'L';
    return strchr("ZBCSIJFD", *sig) ? *sig : 'L';
}

static const char *defaultName(char t) {
    switch (t) {
        case 'V': return "nothing";
        case 'Z': return "false";
        case 'L': return "null";
        case 'F':
        case 'D': return "0.0";
        default: return "0";
    }
}

/* ── class registry ─────────────────────────────────────────────────── */

static void normalizeName(const char *in, char *out, size_t cap) {
    size_t n = 0;
    if (!in) in = "";
    while (*in && n + 1 < cap) {
        out[n++] = *in == '.' ? '/' : *in;
        in++;
    }
    out[n] = '\0';
}

static ApakJClass *registeredLocked(const char *name) {
    ApakJClass *c;
    for (c = g_classes[strHash(name) % CLASS_BUCKETS]; c; c = c->next)
        if (strcmp(c->name, name) == 0) return c;
    return NULL;
}

static ApakJClass *newClassLocked(const char *name, const char *superName) {
    ApakJClass *c = (ApakJClass *)calloc(1, sizeof *c);
    uint32_t b;
    if (!c) return NULL;
    c->obj.magic = OBJ_MAGIC;
    c->obj.kind = K_CLASS;
    c->obj.flags = F_PERMANENT;
    c->obj.cls = g_classClass;
    c->obj.refs = 1;
    c->name = strdup(name);
    c->superName = superName ? strdup(superName) : NULL;
    b = strHash(name) % CLASS_BUCKETS;
    c->next = g_classes[b];
    g_classes[b] = c;
    return c;
}

static ApakJClass *findClassLocked(const char *name, int allowPhantom);

static ApakJClass *superOfLocked(ApakJClass *c) {
    if (!c || c->superResolved) return c ? c->super : NULL;
    c->superResolved = 1;
    if (c == g_objectClass) {
        c->super = NULL;
    } else if (c->superName && strcmp(c->superName, c->name) != 0) {
        c->super = findClassLocked(c->superName, 1);
        if (!c->super) {
            char key[600];
            snprintf(key, sizeof key, "nosuper %s", c->superName);
            apak_jniLogOnce(key, "class %s extends %s, which nothing implements — linked to java/lang/Object",
                            c->name, c->superName);
            c->super = g_objectClass;
        }
    } else {
        c->super = g_objectClass;
    }
    /* never a cycle */
    {
        ApakJClass *k = c->super;
        int depth = 0;
        while (k && depth++ < 64) {
            if (k == c) {
                c->super = g_objectClass;
                break;
            }
            k = k->superResolved ? k->super : NULL;
        }
    }
    if (c->super && c->super->stringKind) c->stringKind = 1;
    return c->super;
}

static ApakJClass *arrayClassLocked(const char *name) {
    ApakJClass *c = registeredLocked(name);
    if (c) return c;
    c = newClassLocked(name, "java/lang/Object");
    if (!c) return NULL;
    c->arrayElem = (name[1] == 'L' || name[1] == '[') ? 'L' : name[1];
    return c;
}

/* The APK's DEX classes become phantom classes; everything else unknown is
 * NULL (or a phantom with ANDROIDPAK_JNI_UNKNOWN=phantom). */
static ApakJClass *findClassLocked(const char *rawName, int allowPhantom) {
    char name[512];
    ApakJClass *c;
    const char *super = NULL;
    normalizeName(rawName, name, sizeof name);
    if (!name[0]) return NULL;
    c = registeredLocked(name);
    if (c) return c;
    if (name[0] == '[') return arrayClassLocked(name);
    if (!allowPhantom) return NULL;
    if (g_cfg.classInApk && g_cfg.classInApk(name, &super)) {
        char key[600];
        c = newClassLocked(name, super ? super : "java/lang/Object");
        if (!c) return NULL;
        c->phantom = 1;
        c->fromApk = 1;
        reportAdd(&g_apkClasses, &g_nApkClasses, name);
        snprintf(key, sizeof key, "apk %s", name);
        apak_jniLogOnce(key, "class %s is the APK's Java code (extends %s) — no implementation here; its members "
                             "answer with defaults", name, super ? super : "java/lang/Object");
        return c;
    }
    if (g_unknownPhantom) {
        char key[600];
        c = newClassLocked(name, "java/lang/Object");
        if (!c) return NULL;
        c->phantom = 1;
        reportAdd(&g_apkClasses, &g_nApkClasses, name);
        snprintf(key, sizeof key, "phantom %s", name);
        apak_jniLogOnce(key, "class %s is unknown — phantom (ANDROIDPAK_JNI_UNKNOWN=phantom)", name);
        return c;
    }
    return NULL;
}

static void computeLayoutLocked(ApakJClass *c) {
    ApakJClass *s;
    int32_t base;
    size_t i;
    if (!c || c->linked) return;
    s = superOfLocked(c);
    if (s) computeLayoutLocked(s);
    base = s ? s->instanceFields : 0;
    for (i = 0; i < c->nfields; i++) {
        ApakJField *f = c->fields[i];
        if (!f->isStatic && !f->phantom) f->slot = base++;
    }
    c->instanceFields = base;
    c->linked = 1;
}

static ApakJMethod *newMethodLocked(ApakJClass *c, const char *name, const char *sig, int isStatic, ApakJImpl impl,
                                    int phantom) {
    ApakJMethod *m;
    if (c->nmethods == c->capMethods) {
        size_t cap = c->capMethods ? c->capMethods * 2 : 8;
        ApakJMethod **grown = (ApakJMethod **)realloc(c->methods, cap * sizeof *grown);
        if (!grown) return NULL;
        c->methods = grown;
        c->capMethods = cap;
    }
    m = (ApakJMethod *)calloc(1, sizeof *m);
    if (!m) return NULL;
    m->magic = METH_MAGIC;
    m->cls = c;
    m->name = strdup(name);
    m->sig = strdup(sig);
    m->isStatic = isStatic;
    m->phantom = phantom;
    m->impl = impl;
    if (parseSig(sig, m->args, &m->nargs, &m->ret) != 0) {
        m->nargs = 0;
        m->ret = 'V';
        apak_jniLogOnce(sig, "malformed method descriptor %s.%s%s — treated as ()V", c->name, name, sig);
    }
    c->methods[c->nmethods++] = m;
    return m;
}

static ApakJField *newFieldLocked(ApakJClass *c, const char *name, const char *sig, int isStatic, int phantom) {
    ApakJField *f;
    if (c->nfields == c->capFields) {
        size_t cap = c->capFields ? c->capFields * 2 : 8;
        ApakJField **grown = (ApakJField **)realloc(c->fields, cap * sizeof *grown);
        if (!grown) return NULL;
        c->fields = grown;
        c->capFields = cap;
    }
    f = (ApakJField *)calloc(1, sizeof *f);
    if (!f) return NULL;
    f->magic = FIELD_MAGIC;
    f->cls = c;
    f->name = strdup(name);
    f->sig = strdup(sig);
    f->isStatic = isStatic;
    f->phantom = phantom;
    f->type = fieldType(sig);
    f->slot = -1;
    c->fields[c->nfields++] = f;
    return f;
}

void apak_jniDefine(const ApakJClassDef *def) {
    ApakJClass *c;
    size_t i;
    if (!def || !def->name) return;
    lock();
    c = registeredLocked(def->name);
    if (!c) c = newClassLocked(def->name, def->super);
    if (!c) {
        unlock();
        return;
    }
    c->phantom = 0;
    if (def->super) {
        free(c->superName);
        c->superName = strdup(def->super);
        c->superResolved = 0;
        c->super = NULL;
    }
    for (i = 0; def->methods && def->methods[i].name; i++) {
        const ApakJMethodDef *md = &def->methods[i];
        size_t k;
        ApakJMethod *existing = NULL;
        for (k = 0; k < c->nmethods; k++) {
            ApakJMethod *m = c->methods[k];
            if (m->isStatic == md->isStatic && strcmp(m->name, md->name) == 0 && strcmp(m->sig, md->sig) == 0)
                existing = m;
        }
        if (existing) {
            existing->impl = md->impl;
            existing->phantom = 0;
        } else {
            newMethodLocked(c, md->name, md->sig, md->isStatic, md->impl, 0);
        }
    }
    for (i = 0; def->fields && def->fields[i].name; i++) {
        const ApakJFieldDef *fd = &def->fields[i];
        size_t k;
        int found = 0;
        for (k = 0; k < c->nfields; k++)
            if (c->fields[k]->isStatic == fd->isStatic && strcmp(c->fields[k]->name, fd->name) == 0) found = 1;
        if (!found) newFieldLocked(c, fd->name, fd->sig, fd->isStatic, 0);
    }
    unlock();
}

void apak_jniDefineAll(const ApakJClassDef *defs) {
    size_t i;
    for (i = 0; defs && defs[i].name; i++) apak_jniDefine(&defs[i]);
}

/* ── UTF-16 ⇄ modified UTF-8 ────────────────────────────────────────── */

static int32_t utf8ToUtf16(const char *in, size_t len, uint16_t *out) {
    const uint8_t *p = (const uint8_t *)in, *end = p + len;
    int32_t n = 0;
    while (p < end) {
        uint32_t c = *p;
        if (c < 0x80) {
            p++;
        } else if ((c & 0xe0) == 0xc0 && p + 1 < end && (p[1] & 0xc0) == 0x80) {
            c = ((c & 0x1f) << 6) | (p[1] & 0x3f);
            p += 2;
        } else if ((c & 0xf0) == 0xe0 && p + 2 < end && (p[1] & 0xc0) == 0x80 && (p[2] & 0xc0) == 0x80) {
            c = ((c & 0x0f) << 12) | ((uint32_t)(p[1] & 0x3f) << 6) | (p[2] & 0x3f);
            p += 3;
        } else if ((c & 0xf8) == 0xf0 && p + 3 < end && (p[1] & 0xc0) == 0x80 && (p[2] & 0xc0) == 0x80 &&
                   (p[3] & 0xc0) == 0x80) {
            c = ((c & 0x07) << 18) | ((uint32_t)(p[1] & 0x3f) << 12) | ((uint32_t)(p[2] & 0x3f) << 6) | (p[3] & 0x3f);
            p += 4;
            if (c >= 0x10000) {
                c -= 0x10000;
                if (out) {
                    out[n] = (uint16_t)(0xd800 + (c >> 10));
                    out[n + 1] = (uint16_t)(0xdc00 + (c & 0x3ff));
                }
                n += 2;
                continue;
            }
        } else {
            c = 0xfffd;
            p++;
        }
        if (out) out[n] = (uint16_t)c;
        n++;
    }
    return n;
}

static size_t mutf8Len(const uint16_t *s, int32_t len) {
    size_t n = 0;
    int32_t i;
    for (i = 0; i < len; i++) n += (s[i] != 0 && s[i] < 0x80) ? 1 : s[i] < 0x800 ? 2 : 3;
    return n;
}

static size_t toMutf8(const uint16_t *s, int32_t len, char *out) {
    size_t n = 0;
    int32_t i;
    for (i = 0; i < len; i++) {
        uint16_t c = s[i];
        if (c != 0 && c < 0x80) {
            out[n++] = (char)c;
        } else if (c < 0x800) {
            out[n++] = (char)(0xc0 | (c >> 6));
            out[n++] = (char)(0x80 | (c & 0x3f));
        } else {
            out[n++] = (char)(0xe0 | (c >> 12));
            out[n++] = (char)(0x80 | ((c >> 6) & 0x3f));
            out[n++] = (char)(0x80 | (c & 0x3f));
        }
    }
    out[n] = '\0';
    return n;
}

/* ── objects ─────────────────────────────────────────────────────────── */

static ApakJObject *allocObject(ApakJClass *cls, int kind) {
    ApakJObject *o = (ApakJObject *)calloc(1, sizeof *o);
    if (!o) return NULL;
    o->magic = OBJ_MAGIC;
    o->kind = (uint16_t)kind;
    o->cls = cls;
    return o;
}

static ApakJObject *newStringObj(const uint16_t *chars, int32_t len) {
    ApakJObject *o = allocObject(g_stringClass, K_STRING);
    size_t ulen;
    if (!o) return NULL;
    if (len < 0) len = 0;
    o->u.s.chars = (uint16_t *)malloc(((size_t)len + 1) * sizeof(uint16_t));
    ulen = mutf8Len(chars, len);
    o->u.s.utf = (char *)malloc(ulen + 1);
    if (!o->u.s.chars || !o->u.s.utf) {
        free(o->u.s.chars);
        free(o->u.s.utf);
        free(o);
        return NULL;
    }
    if (len) memcpy(o->u.s.chars, chars, (size_t)len * sizeof(uint16_t));
    o->u.s.chars[len] = 0;
    o->u.s.len = len;
    o->u.s.utfLen = (int32_t)toMutf8(o->u.s.chars, len, o->u.s.utf);
    return o;
}

static void stringReplace(ApakJObject *o, const uint16_t *chars, int32_t len) {
    ApakJObject *fresh = newStringObj(chars, len);
    if (!fresh) return;
    free(o->u.s.chars);
    free(o->u.s.utf);
    o->u.s = fresh->u.s;
    free(fresh);
}

static ApakJObject *newStringUtf8(const char *utf8) {
    size_t len;
    int32_t n;
    uint16_t *buf;
    ApakJObject *o;
    if (!utf8) return NULL;
    len = strlen(utf8);
    n = utf8ToUtf16(utf8, len, NULL);
    buf = (uint16_t *)malloc(((size_t)n + 1) * sizeof *buf);
    if (!buf) return NULL;
    utf8ToUtf16(utf8, len, buf);
    o = newStringObj(buf, n);
    free(buf);
    return o;
}

static size_t elemSize(char t) {
    switch (t) {
        case 'Z':
        case 'B': return 1;
        case 'C':
        case 'S': return 2;
        case 'I':
        case 'F': return 4;
        case 'J':
        case 'D': return 8;
        default: return sizeof(void *);
    }
}

static ApakJObject *newArrayLocked(char elem, int32_t len, const char *elemClass) {
    char name[512];
    ApakJClass *cls;
    ApakJObject *o;
    if (len < 0) return NULL;
    if (elem == 'L') {
        const char *ec = elemClass && *elemClass ? elemClass : "java/lang/Object";
        if (ec[0] == '[') snprintf(name, sizeof name, "[%s", ec);
        else snprintf(name, sizeof name, "[L%s;", ec);
    } else {
        snprintf(name, sizeof name, "[%c", elem);
    }
    cls = arrayClassLocked(name);
    o = allocObject(cls, K_ARRAY);
    if (!o) return NULL;
    o->u.a.elem = elem;
    o->u.a.len = len;
    o->u.a.data = calloc(len ? (size_t)len : 1, elemSize(elem));
    if (!o->u.a.data) {
        free(o);
        return NULL;
    }
    return o;
}

static int isStringClass(ApakJClass *c) {
    int depth = 0;
    while (c && depth++ < 64) {
        if (c == g_stringClass || c->stringKind) return 1;
        c = superOfLocked(c);
    }
    return 0;
}

static ApakJObject *newInstanceLocked(ApakJClass *cls) {
    ApakJObject *o;
    if (!cls) return NULL;
    if (isStringClass(cls)) return newStringObj(NULL, 0);
    computeLayoutLocked(cls);
    o = allocObject(cls, K_INSTANCE);
    if (!o) return NULL;
    if (cls->instanceFields > 0) {
        o->fields = (ApakJValue *)calloc((size_t)cls->instanceFields, sizeof *o->fields);
        if (!o->fields) {
            free(o);
            return NULL;
        }
        o->nfields = cls->instanceFields;
    }
    return o;
}

static void releaseLocked(ApakJObject *o);

static void finalFree(ApakJObject *o) {
    PhantomSlot *s = o->extra;
    while (s) {
        PhantomSlot *next = s->next;
        free(s);
        s = next;
    }
    if (o->kind == K_STRING) {
        free(o->u.s.chars);
        free(o->u.s.utf);
    } else if (o->kind == K_ARRAY) {
        free(o->u.a.data);
    }
    free(o->fields);
    if (o->monitor) {
        pthread_mutex_destroy(o->monitor);
        free(o->monitor);
    }
    free(o);
}

static void destroyLocked(ApakJObject *o) {
    ApakJClass *k;
    PhantomSlot *s;
    int depth = 0;
    if (o->kind == K_ARRAY && o->u.a.elem == 'L') {
        ApakJObject **items = (ApakJObject **)o->u.a.data;
        int32_t i;
        for (i = 0; i < o->u.a.len; i++) {
            ApakJObject *v = items[i];
            items[i] = NULL;
            if (v) releaseLocked(v);
        }
    }
    for (k = o->kind == K_INSTANCE ? o->cls : NULL; k && depth++ < 64; k = k->superResolved ? k->super : NULL) {
        size_t i;
        for (i = 0; i < k->nfields; i++) {
            ApakJField *f = k->fields[i];
            if (!f->isStatic && f->type == 'L' && f->slot >= 0 && f->slot < o->nfields && o->fields[f->slot].l) {
                ApakJObject *v = (ApakJObject *)o->fields[f->slot].l;
                o->fields[f->slot].l = NULL;
                releaseLocked(v);
            }
        }
    }
    for (s = o->extra; s; s = s->next) {
        if (s->field->type == 'L' && s->v.l) {
            ApakJObject *v = (ApakJObject *)s->v.l;
            s->v.l = NULL;
            releaseLocked(v);
        }
    }
    if (o->freePayload && o->payload) o->freePayload(o->payload);
    o->payload = NULL;
    o->magic = DEAD_MAGIC;
    if (g_dead[g_deadPos]) finalFree(g_dead[g_deadPos]);
    g_dead[g_deadPos] = o;
    g_deadPos = (g_deadPos + 1) % QUARANTINE;
}

static void retainLocked(ApakJObject *o) {
    if (o && o->magic == OBJ_MAGIC && !(o->flags & F_PERMANENT)) o->refs++;
}

static void releaseLocked(ApakJObject *o) {
    if (!o || o->magic != OBJ_MAGIC || (o->flags & F_PERMANENT)) return;
    if (--o->refs <= 0) {
        o->refs = 0;
        destroyLocked(o);
    }
}

/* Validate a handle coming from the game. */
static ApakJObject *obj(const void *p) {
    ApakJObject *o = (ApakJObject *)p;
    if (!o) return NULL;
    if (o->magic == OBJ_MAGIC) return o;
    if (o->magic == DEAD_MAGIC) {
        apak_jniLogOnce("dead-ref", "the game used a JNI reference after deleting it — treated as null");
        return NULL;
    }
    apak_jniLogOnce("bad-ref", "the game passed %p, which is not a JNI reference — treated as null", p);
    return NULL;
}

static ApakJClass *cls(const void *p) {
    ApakJObject *o = obj(p);
    if (o && o->kind == K_CLASS) return (ApakJClass *)o;
    if (o) apak_jniLogOnce("not-class", "a jobject was passed where a jclass belongs — treated as null");
    return NULL;
}

static ApakJMethod *meth(const void *p) {
    ApakJMethod *m = (ApakJMethod *)p;
    if (!m) return NULL;
    if (m->magic == METH_MAGIC) return m;
    apak_jniLogOnce("bad-mid", "the game passed %p, which is not a jmethodID — call ignored", p);
    return NULL;
}

static ApakJField *fld(const void *p) {
    ApakJField *f = (ApakJField *)p;
    if (!f) return NULL;
    if (f->magic == FIELD_MAGIC) return f;
    apak_jniLogOnce("bad-fid", "the game passed %p, which is not a jfieldID — access ignored", p);
    return NULL;
}

/* ── environments, threads, local references ───────────────────────── */

static void envDestroy(void *p);

static void keyInit(void) {
    pthread_once(&g_lockOnce, initLock);
    pthread_key_create(&g_envKey, envDestroy);
}
static pthread_once_t g_keyOnce = PTHREAD_ONCE_INIT;

static ApakJEnv *newEnv(void) {
    ApakJEnv *e = (ApakJEnv *)calloc(1, sizeof *e);
    if (!e) return NULL;
    e->functions = g_table;
    e->magic = ENV_MAGIC;
    return e;
}

static void envClearLocked(ApakJEnv *e) {
    size_t i;
    for (i = e->nlocals; i > 0; i--) releaseLocked(e->locals[i - 1]);
    e->nlocals = 0;
    e->nframes = 0;
    if (e->pending) releaseLocked(e->pending);
    e->pending = NULL;
}

static void envDestroy(void *p) {
    ApakJEnv *e = (ApakJEnv *)p;
    if (!e || e == g_mainEnv) return;
    lock();
    envClearLocked(e);
    unlock();
    e->magic = 0;
    free(e->locals);
    free(e->frames);
    free(e);
}

static ApakJEnv *threadEnv(int create) {
    ApakJEnv *e;
    pthread_once(&g_keyOnce, keyInit);
    e = (ApakJEnv *)pthread_getspecific(g_envKey);
    if (!e && create) {
        e = newEnv();
        if (e) pthread_setspecific(g_envKey, e);
    }
    return e;
}

static ApakJEnv *E(void *env) {
    ApakJEnv *e = (ApakJEnv *)env;
    if (e && e->magic == ENV_MAGIC) return e;
    return threadEnv(1);
}

static void addLocalLocked(ApakJEnv *e, ApakJObject *o) {
    if (!e || !o || o->magic != OBJ_MAGIC || (o->flags & F_PERMANENT)) return;
    if (e->nlocals == e->capLocals) {
        size_t cap = e->capLocals ? e->capLocals * 2 : 64;
        ApakJObject **grown = (ApakJObject **)realloc(e->locals, cap * sizeof *grown);
        if (!grown) return; /* the object stays floating: never freed, never dangling */
        e->locals = grown;
        e->capLocals = cap;
    }
    e->locals[e->nlocals++] = o;
    o->refs++;
    if (e->nlocals % 16384 == 0)
        fprintf(stderr, JNI_TAG "warning: a thread holds %zu local references (the game never deletes them)\n",
                e->nlocals);
}

static void *localRef(ApakJEnv *e, ApakJObject *o) {
    if (!o) return NULL;
    lock();
    addLocalLocked(e, o);
    unlock();
    return o;
}

static int pushFrame(ApakJEnv *e) {
    lock();
    if (e->nframes == e->capFrames) {
        size_t cap = e->capFrames ? e->capFrames * 2 : 16;
        size_t *grown = (size_t *)realloc(e->frames, cap * sizeof *grown);
        if (!grown) {
            unlock();
            return -1;
        }
        e->frames = grown;
        e->capFrames = cap;
    }
    e->frames[e->nframes++] = e->nlocals;
    unlock();
    return 0;
}

static void popFrameLocked(ApakJEnv *e) {
    size_t start;
    if (!e->nframes) return;
    start = e->frames[--e->nframes];
    while (e->nlocals > start) releaseLocked(e->locals[--e->nlocals]);
}

/* ── exceptions ─────────────────────────────────────────────────────── */

static void setPending(ApakJEnv *e, ApakJObject *t) {
    lock();
    retainLocked(t);
    if (e->pending) releaseLocked(e->pending);
    e->pending = t;
    unlock();
}

static ApakJObject *newThrowableLocked(ApakJClass *c, const char *message) {
    ApakJObject *t;
    if (!c) c = g_throwableClass;
    t = newInstanceLocked(c);
    if (t && message) {
        t->payload = strdup(message);
        t->freePayload = free;
    }
    return t;
}

void apak_jniThrow(void *env, const char *className, const char *message) {
    ApakJEnv *e = E(env);
    ApakJObject *t;
    lock();
    t = newThrowableLocked(findClassLocked(className, 1), message);
    unlock();
    if (t) setPending(e, t);
}

int apak_jniExceptionPending(void *env) {
    ApakJEnv *e = E(env);
    return e && e->pending != NULL;
}

static void describe(ApakJObject *t, const char *where) {
    const char *msg = t ? (const char *)t->payload : NULL;
    fprintf(stderr, JNI_TAG "exception%s%s: %s%s%s\n", where ? " in " : "", where ? where : "",
            t && t->cls ? t->cls->name : "?", msg ? ": " : "", msg ? msg : "");
}

void apak_jniExceptionReport(void *env, const char *where) {
    ApakJEnv *e = E(env);
    ApakJObject *t;
    lock();
    t = e->pending;
    e->pending = NULL;
    unlock();
    if (!t) return;
    describe(t, where);
    lock();
    releaseLocked(t);
    unlock();
}

/* ── members ─────────────────────────────────────────────────────────── */

static ApakJMethod *findMethodLocked(ApakJClass *c, const char *name, const char *sig, int isStatic) {
    int depth = 0;
    while (c && depth++ < 64) {
        size_t i;
        for (i = 0; i < c->nmethods; i++) {
            ApakJMethod *m = c->methods[i];
            if (m->isStatic == isStatic && strcmp(m->name, name) == 0 && strcmp(m->sig, sig) == 0) return m;
        }
        c = superOfLocked(c);
    }
    return NULL;
}

static ApakJMethod *lookupMethod(ApakJClass *c, const char *name, const char *sig, int isStatic) {
    ApakJMethod *m;
    if (!c || !name || !sig) return NULL;
    lock();
    m = findMethodLocked(c, name, sig, isStatic);
    if (!m) {
        char key[640];
        m = newMethodLocked(c, name, sig, isStatic, NULL, 1);
        snprintf(key, sizeof key, "%s.%s%s", c->name, name, sig);
        reportAdd(&g_phantomMembers, &g_nPhantomMembers, key);
        if (firstTime(key))
            fprintf(stderr, JNI_TAG "%s %s.%s%s -> not implemented; calls return %s\n",
                    isStatic ? "GetStaticMethodID" : "GetMethodID", c->name, name, sig,
                    defaultName(m ? m->ret : 'V'));
    }
    unlock();
    if (g_trace) fprintf(stderr, JNI_TAG "trace: %s %s.%s%s\n", isStatic ? "GetStaticMethodID" : "GetMethodID",
                         c->name, name, sig);
    return m;
}

static ApakJField *findFieldLocked(ApakJClass *c, const char *name, const char *sig, int isStatic) {
    int depth = 0;
    while (c && depth++ < 64) {
        size_t i;
        for (i = 0; i < c->nfields; i++) {
            ApakJField *f = c->fields[i];
            if (f->isStatic == isStatic && strcmp(f->name, name) == 0 && (!sig || strcmp(f->sig, sig) == 0)) return f;
        }
        c = superOfLocked(c);
    }
    return NULL;
}

static ApakJField *lookupField(ApakJClass *c, const char *name, const char *sig, int isStatic) {
    ApakJField *f;
    if (!c || !name || !sig) return NULL;
    lock();
    computeLayoutLocked(c);
    f = findFieldLocked(c, name, sig, isStatic);
    if (!f) {
        char key[640];
        f = newFieldLocked(c, name, sig, isStatic, 1);
        snprintf(key, sizeof key, "%s.%s:%s", c->name, name, sig);
        reportAdd(&g_phantomMembers, &g_nPhantomMembers, key);
        if (firstTime(key))
            fprintf(stderr, JNI_TAG "%s %s.%s %s -> not implemented; reads return %s until the game stores a value\n",
                    isStatic ? "GetStaticFieldID" : "GetFieldID", c->name, name, sig, defaultName(fieldType(sig)));
    }
    unlock();
    return f;
}

static ApakJValue *slotFor(ApakJObject *o, ApakJField *f, int create) {
    PhantomSlot *s;
    if (f->slot >= 0 && f->slot < o->nfields) return &o->fields[f->slot];
    for (s = o->extra; s; s = s->next)
        if (s->field == f) return &s->v;
    if (!create) return NULL;
    s = (PhantomSlot *)calloc(1, sizeof *s);
    if (!s) return NULL;
    s->field = f;
    s->next = o->extra;
    o->extra = s;
    return &s->v;
}

/* ── natives (registered or exported) ───────────────────────────────── */

static void mangle(const char *s, char *out, size_t cap, int isSig) {
    size_t n = 0;
    const uint8_t *p = (const uint8_t *)s;
    while (*p && n + 7 < cap) {
        uint8_t c = *p;
        if (isSig && (c == '(')) {
            p++;
            continue;
        }
        if (isSig && c == ')') break;
        if (isalnum(c)) out[n++] = (char)c;
        else if (c == '/') out[n++] = '_';
        else if (c == '_') {
            out[n++] = '_';
            out[n++] = '1';
        } else if (c == ';') {
            out[n++] = '_';
            out[n++] = '2';
        } else if (c == '[') {
            out[n++] = '_';
            out[n++] = '3';
        } else {
            n += (size_t)snprintf(out + n, cap - n, "_0%04x", c);
        }
        p++;
    }
    out[n] = '\0';
}

static void *exportedNative(const char *className, const char *name, const char *sig) {
    char mc[512], mm[256], ms[512], sym[1400];
    void *fn;
    if (!g_cfg.resolveSymbol) return NULL;
    mangle(className, mc, sizeof mc, 0);
    mangle(name, mm, sizeof mm, 0);
    snprintf(sym, sizeof sym, "Java_%s_%s", mc, mm);
    fn = g_cfg.resolveSymbol(sym);
    if (fn || !sig) return fn;
    mangle(sig, ms, sizeof ms, 1);
    snprintf(sym, sizeof sym, "Java_%s_%s__%s", mc, mm, ms);
    return g_cfg.resolveSymbol(sym);
}

/* The native for (class chain, name, sig): RegisterNatives first. */
static void *findNativeLocked(ApakJClass *c, const char *name, const char *sig, ApakJClass **owner) {
    int depth = 0;
    ApakJClass *k;
    for (k = c; k && depth++ < 64; k = superOfLocked(k)) {
        size_t i;
        for (i = 0; i < k->nnatives; i++)
            if (strcmp(k->natives[i].name, name) == 0 && strcmp(k->natives[i].sig, sig) == 0) {
                if (owner) *owner = k;
                return k->natives[i].fn;
            }
    }
    depth = 0;
    for (k = c; k && depth++ < 64; k = superOfLocked(k)) {
        void *fn = exportedNative(k->name, name, sig);
        if (fn) {
            if (owner) *owner = k;
            return fn;
        }
    }
    return NULL;
}

typedef uint64_t (*FfiInt)(uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, double,
                           double, double, double, double, double, double, double);
typedef float (*FfiFloat)(uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, double,
                          double, double, double, double, double, double, double);
typedef double (*FfiDouble)(uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, double,
                            double, double, double, double, double, double, double);

/* Call fn(env, thisOrClass, args...) with each argument in the register the
 * ABI assigns it: integer-class values in x0-x7 / rdi..r9 (+2 stack slots),
 * float/double in v0-v7 / xmm0-7 (a float travels in the low 32 bits). */
static int ffiCall(void *fn, ApakJEnv *e, void *thisOrClass, const char *types, int nargs, const ApakJValue *args,
                   char ret, ApakJValue *out) {
    uint64_t gp[8] = { 0 };
    double fp[8] = { 0 };
    int ngp = 0, nfp = 0, i;
    gp[ngp++] = (uint64_t)(uintptr_t)e;
    gp[ngp++] = (uint64_t)(uintptr_t)thisOrClass;
    for (i = 0; i < nargs; i++) {
        char t = types[i];
        if (t == 'F' || t == 'D') {
            union {
                double d;
                float f[2];
                uint64_t u;
            } u;
            if (nfp >= 8) return -1;
            u.u = 0;
            if (t == 'F') u.f[0] = args[i].f;
            else u.d = args[i].d;
            fp[nfp++] = u.d;
        } else {
            uint64_t v;
            if (ngp >= 8) return -1;
            switch (t) {
                case 'Z': v = args[i].z; break;
                case 'B': v = (uint64_t)(int64_t)args[i].b; break;
                case 'C': v = args[i].c; break;
                case 'S': v = (uint64_t)(int64_t)args[i].s; break;
                case 'I': v = (uint64_t)(int64_t)args[i].i; break;
                case 'J': v = (uint64_t)args[i].j; break;
                default: v = (uint64_t)(uintptr_t)args[i].l; break;
            }
            gp[ngp++] = v;
        }
    }
    memset(out, 0, sizeof *out);
    if (ret == 'F') {
        out->f = ((FfiFloat)fn)(gp[0], gp[1], gp[2], gp[3], gp[4], gp[5], gp[6], gp[7], fp[0], fp[1], fp[2], fp[3],
                                fp[4], fp[5], fp[6], fp[7]);
    } else if (ret == 'D') {
        out->d = ((FfiDouble)fn)(gp[0], gp[1], gp[2], gp[3], gp[4], gp[5], gp[6], gp[7], fp[0], fp[1], fp[2], fp[3],
                                 fp[4], fp[5], fp[6], fp[7]);
    } else {
        uint64_t r = ((FfiInt)fn)(gp[0], gp[1], gp[2], gp[3], gp[4], gp[5], gp[6], gp[7], fp[0], fp[1], fp[2], fp[3],
                                  fp[4], fp[5], fp[6], fp[7]);
        switch (ret) {
            case 'Z': out->z = (uint8_t)(r & 0xff) ? 1 : 0; break;
            case 'B': out->b = (int8_t)r; break;
            case 'C': out->c = (uint16_t)r; break;
            case 'S': out->s = (int16_t)r; break;
            case 'I': out->i = (int32_t)r; break;
            case 'J': out->j = (int64_t)r; break;
            case 'L': out->l = (void *)(uintptr_t)r; break;
            default: break;
        }
    }
    return 0;
}

/* ── dispatch ────────────────────────────────────────────────────────── */

static ApakJMethod *resolveVirtualLocked(ApakJObject *self, ApakJMethod *m) {
    ApakJClass *k;
    int depth = 0;
    if (!self || m->isStatic) return m;
    for (k = self->cls; k && depth++ < 64; k = superOfLocked(k)) {
        size_t i;
        for (i = 0; i < k->nmethods; i++) {
            ApakJMethod *c = k->methods[i];
            if (!c->isStatic && !c->phantom && c->impl && strcmp(c->name, m->name) == 0 && strcmp(c->sig, m->sig) == 0)
                return c;
        }
        if (findNativeLocked(k, m->name, m->sig, NULL)) return m; /* a game native on the way up */
        if (k == m->cls) break;
    }
    return m;
}

static void invoke(ApakJEnv *e, int mode, ApakJObject *self, ApakJClass *c, ApakJMethod *m, const ApakJValue *args,
                   ApakJValue *ret) {
    ApakJMethod *target = m;
    void *native = NULL;
    ApakJClass *owner = NULL;
    memset(ret, 0, sizeof *ret);
    if (!m) return;
    lock();
    if (mode == CALL_VIRTUAL) target = resolveVirtualLocked(self, m);
    if (!target->impl) {
        ApakJClass *from = mode == CALL_STATIC ? (c ? c : m->cls) : (mode == CALL_VIRTUAL && self) ? self->cls : target->cls;
        native = findNativeLocked(from, target->name, target->sig, &owner);
    }
    target->calls++;
    unlock();
    if (g_trace)
        fprintf(stderr, JNI_TAG "trace: call %s.%s%s%s\n", target->cls->name, target->name, target->sig,
                target->impl ? "" : native ? " (game native)" : " (not implemented)");
    if (!target->isStatic && !self && mode != CALL_STATIC) {
        char key[640];
        snprintf(key, sizeof key, "null-this %s.%s%s", target->cls->name, target->name, target->sig);
        apak_jniLogOnce(key, "%s.%s%s called on a null object — returned %s", target->cls->name, target->name,
                        target->sig, defaultName(target->ret));
        return;
    }
    if (target->impl) {
        ApakJCall call;
        memset(&call, 0, sizeof call);
        call.env = e;
        call.method = target;
        call.self = self;
        call.args = args;
        call.nargs = target->nargs;
        target->impl(&call);
        *ret = call.ret;
        if (target->ret == 'L' && ret->l) localRef(e, (ApakJObject *)ret->l);
        return;
    }
    if (native) {
        ffiCall(native, e, mode == CALL_STATIC ? (void *)(c ? c : owner) : (void *)self, target->args, target->nargs,
                args, target->ret, ret);
        return;
    }
    {
        char key[640];
        snprintf(key, sizeof key, "call %s.%s%s", target->cls->name, target->name, target->sig);
        if (firstTime(key)) {
            lock();
            g_nPhantomCalls++; /* the report reads both counters under the lock */
            reportAdd(&g_phantomMembers, &g_nPhantomMembers, key);
            unlock();
            fprintf(stderr, JNI_TAG "called %s.%s%s — not implemented, returned %s\n", target->cls->name, target->name,
                    target->sig, defaultName(target->ret));
        }
    }
}

static void argsFromVa(const ApakJMethod *m, va_list ap, ApakJValue *out) {
    int i;
    for (i = 0; i < m->nargs; i++) {
        out[i].j = 0;
        switch (m->args[i]) {
            case 'Z': out[i].z = (uint8_t)va_arg(ap, int); break;
            case 'B': out[i].b = (int8_t)va_arg(ap, int); break;
            case 'C': out[i].c = (uint16_t)va_arg(ap, int); break;
            case 'S': out[i].s = (int16_t)va_arg(ap, int); break;
            case 'I': out[i].i = va_arg(ap, int32_t); break;
            case 'J': out[i].j = va_arg(ap, int64_t); break;
            case 'F': out[i].f = (float)va_arg(ap, double); break;
            case 'D': out[i].d = va_arg(ap, double); break;
            default: out[i].l = va_arg(ap, void *); break;
        }
    }
}

static void argsFromA(const ApakJMethod *m, const ApakJValue *in, ApakJValue *out) {
    int i;
    for (i = 0; i < m->nargs; i++) {
        out[i].j = 0;
        if (!in) continue;
        switch (m->args[i]) {
            case 'Z': out[i].z = in[i].z; break;
            case 'B': out[i].b = in[i].b; break;
            case 'C': out[i].c = in[i].c; break;
            case 'S': out[i].s = in[i].s; break;
            case 'I': out[i].i = in[i].i; break;
            case 'F': out[i].f = in[i].f; break;
            default: out[i] = in[i]; break;
        }
    }
}

static ApakJValue callV(ApakJEnv *e, int mode, void *o, void *c, void *mid, va_list ap) {
    ApakJValue args[APAK_JNI_MAXARGS], r;
    ApakJMethod *m = meth(mid);
    memset(&r, 0, sizeof r);
    if (!m) {
        apak_jniLogOnce("null-mid", "a Call*Method with a null jmethodID — returned 0/null");
        return r;
    }
    argsFromVa(m, ap, args);
    invoke(e, mode, obj(o), cls(c), m, args, &r);
    return r;
}

static ApakJValue callA(ApakJEnv *e, int mode, void *o, void *c, void *mid, const ApakJValue *a) {
    ApakJValue args[APAK_JNI_MAXARGS], r;
    ApakJMethod *m = meth(mid);
    memset(&r, 0, sizeof r);
    if (!m) {
        apak_jniLogOnce("null-mid", "a Call*Method with a null jmethodID — returned 0/null");
        return r;
    }
    argsFromA(m, a, args);
    invoke(e, mode, obj(o), cls(c), m, args, &r);
    return r;
}

/* ── the JNI functions ──────────────────────────────────────────────── */

static int32_t jni_GetVersion(void *env) {
    (void)env;
    return APAK_JNI_VERSION_1_6;
}

static void *jni_DefineClass(void *env, const char *name, void *loader, const int8_t *buf, int32_t len) {
    (void)env;
    (void)loader;
    (void)buf;
    apak_jniLogOnce("DefineClass", "DefineClass(\"%s\", %d bytes) -> null: there is no bytecode interpreter",
                    name ? name : "(null)", (int)len);
    return NULL;
}

static void *jni_FindClass(void *env, const char *name) {
    ApakJClass *c;
    (void)env;
    lock();
    c = findClassLocked(name, 1);
    unlock();
    if (g_trace) fprintf(stderr, JNI_TAG "trace: FindClass %s -> %s\n", name ? name : "(null)", c ? "ok" : "null");
    if (!c) {
        char key[600];
        snprintf(key, sizeof key, "FindClass %s", name ? name : "(null)");
        /* Once per name: a game asking every frame neither floods the log
         * nor grows the report (it used to gain an entry per call). */
        if (firstTime(key)) {
            if (name) {
                lock();
                reportAdd(&g_unknownClasses, &g_nUnknownClasses, name);
                unlock();
            }
            fprintf(stderr, JNI_TAG "FindClass (\"%.500s\") -> null (no implementation, not in the APK)\n",
                    name ? name : "(null)");
        }
    }
    return c;
}

static void *jni_FromReflectedMethod(void *env, void *method) {
    ApakJObject *o = obj(method);
    (void)env;
    return o && o->kind == K_INSTANCE && o->cls && strcmp(o->cls->name, "java/lang/reflect/Method") == 0 ? o->payload
                                                                                                       : NULL;
}

static void *jni_FromReflectedField(void *env, void *field) {
    ApakJObject *o = obj(field);
    (void)env;
    return o && o->kind == K_INSTANCE && o->cls && strcmp(o->cls->name, "java/lang/reflect/Field") == 0 ? o->payload
                                                                                                      : NULL;
}

static void *reflected(ApakJEnv *e, const char *className, void *member) {
    ApakJObject *o;
    lock();
    o = newInstanceLocked(findClassLocked(className, 1));
    if (o) o->payload = member;
    unlock();
    return localRef(e, o);
}

static void *jni_ToReflectedMethod(void *env, void *c, void *mid, uint8_t isStatic) {
    (void)c;
    (void)isStatic;
    return meth(mid) ? reflected(E(env), "java/lang/reflect/Method", mid) : NULL;
}

static void *jni_ToReflectedField(void *env, void *c, void *fid, uint8_t isStatic) {
    (void)c;
    (void)isStatic;
    return fld(fid) ? reflected(E(env), "java/lang/reflect/Field", fid) : NULL;
}

static void *jni_GetSuperclass(void *env, void *c) {
    ApakJClass *k = cls(c), *s;
    (void)env;
    if (!k) return NULL;
    lock();
    s = superOfLocked(k);
    unlock();
    return s;
}

static int isSubclassLocked(ApakJClass *sub, ApakJClass *sup) {
    int depth = 0;
    if (!sub || !sup) return 0;
    if (sup == g_objectClass) return 1;
    while (sub && depth++ < 64) {
        if (sub == sup) return 1;
        sub = superOfLocked(sub);
    }
    return 0;
}

static uint8_t jni_IsAssignableFrom(void *env, void *a, void *b) {
    int r;
    (void)env;
    lock();
    r = isSubclassLocked(cls(a), cls(b));
    unlock();
    return (uint8_t)r;
}

static int32_t jni_Throw(void *env, void *t) {
    ApakJObject *o = obj(t);
    if (!o) return APAK_JNI_ERR;
    setPending(E(env), o);
    return APAK_JNI_OK;
}

static int32_t jni_ThrowNew(void *env, void *c, const char *message) {
    ApakJClass *k = cls(c);
    ApakJObject *t;
    lock();
    t = newThrowableLocked(k, message);
    unlock();
    if (!t) return APAK_JNI_ERR;
    setPending(E(env), t);
    return APAK_JNI_OK;
}

static void *jni_ExceptionOccurred(void *env) {
    ApakJEnv *e = E(env);
    ApakJObject *t;
    lock();
    t = e->pending;
    if (t) addLocalLocked(e, t);
    unlock();
    return t;
}

static void jni_ExceptionDescribe(void *env) { apak_jniExceptionReport(env, NULL); }

static void jni_ExceptionClear(void *env) {
    ApakJEnv *e = E(env);
    lock();
    if (e->pending) releaseLocked(e->pending);
    e->pending = NULL;
    unlock();
}

static void jni_FatalError(void *env, const char *msg) {
    (void)env;
    fprintf(stderr, JNI_TAG "FatalError(\"%s\")\n", msg ? msg : "");
    fflush(stderr);
    abort();
}

static int32_t jni_PushLocalFrame(void *env, int32_t capacity) {
    (void)capacity;
    return pushFrame(E(env)) == 0 ? APAK_JNI_OK : APAK_JNI_ERR;
}

static void *jni_PopLocalFrame(void *env, void *result) {
    ApakJEnv *e = E(env);
    ApakJObject *r = obj(result);
    lock();
    retainLocked(r);
    popFrameLocked(e);
    if (r) {
        addLocalLocked(e, r);
        releaseLocked(r);
    }
    unlock();
    return r;
}

static void *jni_NewGlobalRef(void *env, void *ref) {
    ApakJObject *o = obj(ref);
    (void)env;
    if (!o) return NULL;
    lock();
    retainLocked(o);
    o->globals++;
    unlock();
    return o;
}

static void jni_DeleteGlobalRef(void *env, void *ref) {
    ApakJObject *o = obj(ref);
    (void)env;
    if (!o) return;
    lock();
    if (o->globals > 0) {
        o->globals--;
        releaseLocked(o);
    } else if (!(o->flags & F_PERMANENT)) {
        apak_jniLogOnce("del-global", "DeleteGlobalRef of a reference that is not global — ignored");
    }
    unlock();
}

static void jni_DeleteLocalRef(void *env, void *ref) {
    ApakJEnv *e = E(env);
    ApakJObject *o = obj(ref);
    size_t i;
    if (!o || (o->flags & F_PERMANENT)) return;
    lock();
    for (i = e->nlocals; i > 0; i--) {
        if (e->locals[i - 1] == o) {
            memmove(&e->locals[i - 1], &e->locals[i], (e->nlocals - i) * sizeof *e->locals);
            e->nlocals--;
            releaseLocked(o);
            unlock();
            return;
        }
    }
    unlock();
    /* a global ref deleted as local, or a local of another thread: harmless */
}

static uint8_t jni_IsSameObject(void *env, void *a, void *b) {
    (void)env;
    return a == b;
}

static void *jni_NewLocalRef(void *env, void *ref) { return localRef(E(env), obj(ref)); }

static int32_t jni_EnsureLocalCapacity(void *env, int32_t capacity) {
    (void)env;
    (void)capacity;
    return APAK_JNI_OK;
}

static void *jni_AllocObject(void *env, void *c) {
    ApakJClass *k = cls(c);
    ApakJObject *o;
    if (!k) return NULL;
    lock();
    o = newInstanceLocked(k);
    unlock();
    return localRef(E(env), o);
}

static void *newObjectWith(ApakJEnv *e, void *c, void *mid, const ApakJValue *args) {
    ApakJClass *k = cls(c);
    ApakJMethod *m = meth(mid);
    ApakJObject *o;
    ApakJValue r;
    if (!k) return NULL;
    lock();
    o = newInstanceLocked(k);
    retainLocked(o); /* survive the constructor */
    unlock();
    if (!o) return NULL;
    if (m) invoke(e, CALL_NONVIRTUAL, o, k, m, args, &r);
    lock();
    if (e->pending) {
        releaseLocked(o); /* the constructor threw */
        unlock();
        return NULL;
    }
    addLocalLocked(e, o);
    releaseLocked(o);
    unlock();
    return o;
}

static void *jni_NewObjectV(void *env, void *c, void *mid, va_list ap) {
    ApakJValue args[APAK_JNI_MAXARGS];
    ApakJMethod *m = meth(mid);
    memset(args, 0, sizeof args);
    if (m) argsFromVa(m, ap, args);
    return newObjectWith(E(env), c, mid, args);
}

static void *jni_NewObject(void *env, void *c, void *mid, ...) {
    va_list ap;
    void *o;
    va_start(ap, mid);
    o = jni_NewObjectV(env, c, mid, ap);
    va_end(ap);
    return o;
}

static void *jni_NewObjectA(void *env, void *c, void *mid, const ApakJValue *a) {
    ApakJValue args[APAK_JNI_MAXARGS];
    ApakJMethod *m = meth(mid);
    memset(args, 0, sizeof args);
    if (m) argsFromA(m, a, args);
    return newObjectWith(E(env), c, mid, args);
}

static void *jni_GetObjectClass(void *env, void *o) {
    ApakJObject *p = obj(o);
    (void)env;
    return p ? p->cls : NULL;
}

static uint8_t jni_IsInstanceOf(void *env, void *o, void *c) {
    ApakJObject *p = obj(o);
    int r;
    (void)env;
    if (!p) return 1; /* null is an instance of every class */
    lock();
    r = isSubclassLocked(p->cls, cls(c));
    unlock();
    return (uint8_t)r;
}

static void *jni_GetMethodID(void *env, void *c, const char *name, const char *sig) {
    ApakJClass *k = cls(c);
    (void)env;
    if (!k) {
        char key[400];
        snprintf(key, sizeof key, "GetMethodID-null %s%s", name ? name : "", sig ? sig : "");
        apak_jniLogOnce(key, "GetMethodID(class=null, %.120s, %.200s) -> null", name ? name : "(null)",
                        sig ? sig : "(null)");
        return NULL;
    }
    return lookupMethod(k, name, sig, 0);
}

static void *jni_GetStaticMethodID(void *env, void *c, const char *name, const char *sig) {
    ApakJClass *k = cls(c);
    (void)env;
    if (!k) {
        char key[400];
        snprintf(key, sizeof key, "GetStaticMethodID-null %s%s", name ? name : "", sig ? sig : "");
        apak_jniLogOnce(key, "GetStaticMethodID(class=null, %.120s, %.200s) -> null", name ? name : "(null)",
                        sig ? sig : "(null)");
        return NULL;
    }
    return lookupMethod(k, name, sig, 1);
}

static void *jni_GetFieldID(void *env, void *c, const char *name, const char *sig) {
    ApakJClass *k = cls(c);
    (void)env;
    if (!k) {
        apak_jniLogOnce("GetFieldID-null", "GetFieldID(class=null, %.120s) -> null", name ? name : "(null)");
        return NULL;
    }
    return lookupField(k, name, sig, 0);
}

static void *jni_GetStaticFieldID(void *env, void *c, const char *name, const char *sig) {
    ApakJClass *k = cls(c);
    (void)env;
    if (!k) {
        apak_jniLogOnce("GetStaticFieldID-null", "GetStaticFieldID(class=null, %.120s) -> null", name ? name : "(null)");
        return NULL;
    }
    return lookupField(k, name, sig, 1);
}

/* Call<Type>Method / CallNonvirtual<Type>Method / CallStatic<Type>Method */
#define CALLS(Name, CType, member)                                                                                    \
    static CType jni_Call##Name##MethodV(void *env, void *o, void *m, va_list ap) {                                 \
        ApakJValue r = callV(E(env), CALL_VIRTUAL, o, NULL, m, ap);                                                  \
        return (CType)r.member;                                                                                      \
    }                                                                                                                \
    static CType jni_Call##Name##Method(void *env, void *o, void *m, ...) {                                         \
        va_list ap;                                                                                                  \
        CType v;                                                                                                     \
        va_start(ap, m);                                                                                             \
        v = jni_Call##Name##MethodV(env, o, m, ap);                                                                  \
        va_end(ap);                                                                                                  \
        return v;                                                                                                    \
    }                                                                                                                \
    static CType jni_Call##Name##MethodA(void *env, void *o, void *m, const ApakJValue *a) {                        \
        ApakJValue r = callA(E(env), CALL_VIRTUAL, o, NULL, m, a);                                                   \
        return (CType)r.member;                                                                                      \
    }                                                                                                                \
    static CType jni_CallNonvirtual##Name##MethodV(void *env, void *o, void *c, void *m, va_list ap) {              \
        ApakJValue r = callV(E(env), CALL_NONVIRTUAL, o, c, m, ap);                                                  \
        return (CType)r.member;                                                                                      \
    }                                                                                                                \
    static CType jni_CallNonvirtual##Name##Method(void *env, void *o, void *c, void *m, ...) {                      \
        va_list ap;                                                                                                  \
        CType v;                                                                                                     \
        va_start(ap, m);                                                                                             \
        v = jni_CallNonvirtual##Name##MethodV(env, o, c, m, ap);                                                     \
        va_end(ap);                                                                                                  \
        return v;                                                                                                    \
    }                                                                                                                \
    static CType jni_CallNonvirtual##Name##MethodA(void *env, void *o, void *c, void *m, const ApakJValue *a) {     \
        ApakJValue r = callA(E(env), CALL_NONVIRTUAL, o, c, m, a);                                                   \
        return (CType)r.member;                                                                                      \
    }                                                                                                                \
    static CType jni_CallStatic##Name##MethodV(void *env, void *c, void *m, va_list ap) {                           \
        ApakJValue r = callV(E(env), CALL_STATIC, NULL, c, m, ap);                                                   \
        return (CType)r.member;                                                                                      \
    }                                                                                                                \
    static CType jni_CallStatic##Name##Method(void *env, void *c, void *m, ...) {                                   \
        va_list ap;                                                                                                  \
        CType v;                                                                                                     \
        va_start(ap, m);                                                                                             \
        v = jni_CallStatic##Name##MethodV(env, c, m, ap);                                                            \
        va_end(ap);                                                                                                  \
        return v;                                                                                                    \
    }                                                                                                                \
    static CType jni_CallStatic##Name##MethodA(void *env, void *c, void *m, const ApakJValue *a) {                  \
        ApakJValue r = callA(E(env), CALL_STATIC, NULL, c, m, a);                                                    \
        return (CType)r.member;                                                                                      \
    }

CALLS(Object, void *, l)
CALLS(Boolean, uint8_t, z)
CALLS(Byte, int8_t, b)
CALLS(Char, uint16_t, c)
CALLS(Short, int16_t, s)
CALLS(Int, int32_t, i)
CALLS(Long, int64_t, j)
CALLS(Float, float, f)
CALLS(Double, double, d)

static void jni_CallVoidMethodV(void *env, void *o, void *m, va_list ap) { callV(E(env), CALL_VIRTUAL, o, NULL, m, ap); }
static void jni_CallVoidMethod(void *env, void *o, void *m, ...) {
    va_list ap;
    va_start(ap, m);
    jni_CallVoidMethodV(env, o, m, ap);
    va_end(ap);
}
static void jni_CallVoidMethodA(void *env, void *o, void *m, const ApakJValue *a) {
    callA(E(env), CALL_VIRTUAL, o, NULL, m, a);
}
static void jni_CallNonvirtualVoidMethodV(void *env, void *o, void *c, void *m, va_list ap) {
    callV(E(env), CALL_NONVIRTUAL, o, c, m, ap);
}
static void jni_CallNonvirtualVoidMethod(void *env, void *o, void *c, void *m, ...) {
    va_list ap;
    va_start(ap, m);
    jni_CallNonvirtualVoidMethodV(env, o, c, m, ap);
    va_end(ap);
}
static void jni_CallNonvirtualVoidMethodA(void *env, void *o, void *c, void *m, const ApakJValue *a) {
    callA(E(env), CALL_NONVIRTUAL, o, c, m, a);
}
static void jni_CallStaticVoidMethodV(void *env, void *c, void *m, va_list ap) {
    callV(E(env), CALL_STATIC, NULL, c, m, ap);
}
static void jni_CallStaticVoidMethod(void *env, void *c, void *m, ...) {
    va_list ap;
    va_start(ap, m);
    jni_CallStaticVoidMethodV(env, c, m, ap);
    va_end(ap);
}
static void jni_CallStaticVoidMethodA(void *env, void *c, void *m, const ApakJValue *a) {
    callA(E(env), CALL_STATIC, NULL, c, m, a);
}

/* ── fields ──────────────────────────────────────────────────────────── */

static ApakJValue fieldGet(ApakJEnv *e, void *o, void *fid) {
    ApakJValue v;
    ApakJObject *p = obj(o);
    ApakJField *f = fld(fid);
    v.j = 0;
    if (!p || !f) {
        if (!p && f) apak_jniLogOnce("field-null", "Get*Field on a null object — returned 0/null");
        return v;
    }
    lock();
    {
        ApakJValue *slot = slotFor(p, f, 0);
        if (slot) v = *slot;
        if (f->type == 'L' && v.l) addLocalLocked(e, (ApakJObject *)v.l);
    }
    unlock();
    return v;
}

static void storeTyped(ApakJValue *slot, char type, ApakJValue v) {
    ApakJValue n;
    n.j = 0;
    switch (type) {
        case 'Z': n.z = v.z; break;
        case 'B': n.b = v.b; break;
        case 'C': n.c = v.c; break;
        case 'S': n.s = v.s; break;
        case 'I': n.i = v.i; break;
        case 'F': n.f = v.f; break;
        default: n = v; break;
    }
    *slot = n;
}

static void fieldSet(void *o, void *fid, ApakJValue v) {
    ApakJObject *p = obj(o);
    ApakJField *f = fld(fid);
    ApakJValue *slot;
    if (!p || !f) {
        if (!p && f) apak_jniLogOnce("field-null-set", "Set*Field on a null object — ignored");
        return;
    }
    lock();
    slot = slotFor(p, f, 1);
    if (slot) {
        if (f->type == 'L') {
            ApakJObject *old = (ApakJObject *)slot->l, *nv = obj(v.l);
            retainLocked(nv);
            slot->l = nv;
            releaseLocked(old);
        } else {
            storeTyped(slot, f->type, v);
        }
    }
    unlock();
}

static ApakJValue staticGet(ApakJEnv *e, void *fid) {
    ApakJValue v;
    ApakJField *f = fld(fid);
    v.j = 0;
    if (!f) return v;
    lock();
    v = f->value;
    if (f->type == 'L' && v.l) addLocalLocked(e, (ApakJObject *)v.l);
    unlock();
    return v;
}

static void staticSet(void *fid, ApakJValue v) {
    ApakJField *f = fld(fid);
    if (!f) return;
    lock();
    if (f->type == 'L') {
        ApakJObject *old = (ApakJObject *)f->value.l, *nv = obj(v.l);
        retainLocked(nv);
        f->value.l = nv;
        releaseLocked(old);
    } else {
        storeTyped(&f->value, f->type, v);
    }
    unlock();
}

#define FIELDS(Name, CType, member)                                                                                   \
    static CType jni_Get##Name##Field(void *env, void *o, void *f) { return (CType)fieldGet(E(env), o, f).member; }  \
    static void jni_Set##Name##Field(void *env, void *o, void *f, CType val) {                                      \
        ApakJValue v;                                                                                                \
        (void)env;                                                                                                   \
        v.j = 0;                                                                                                     \
        v.member = val;                                                                                              \
        fieldSet(o, f, v);                                                                                           \
    }                                                                                                                \
    static CType jni_GetStatic##Name##Field(void *env, void *c, void *f) {                                          \
        (void)c;                                                                                                     \
        return (CType)staticGet(E(env), f).member;                                                                   \
    }                                                                                                                \
    static void jni_SetStatic##Name##Field(void *env, void *c, void *f, CType val) {                                \
        ApakJValue v;                                                                                                \
        (void)env;                                                                                                   \
        (void)c;                                                                                                     \
        v.j = 0;                                                                                                     \
        v.member = val;                                                                                              \
        staticSet(f, v);                                                                                             \
    }

FIELDS(Object, void *, l)
FIELDS(Boolean, uint8_t, z)
FIELDS(Byte, int8_t, b)
FIELDS(Char, uint16_t, c)
FIELDS(Short, int16_t, s)
FIELDS(Int, int32_t, i)
FIELDS(Long, int64_t, j)
FIELDS(Float, float, f)
FIELDS(Double, double, d)

/* ── strings ─────────────────────────────────────────────────────────── */

static const uint16_t kEmptyU16[1] = { 0 };

static ApakJObject *str(void *s, const char *what) {
    ApakJObject *o = obj(s);
    if (o && o->kind == K_STRING) return o;
    if (!o) {
        char key[96];
        snprintf(key, sizeof key, "%s-null", what);
        apak_jniLogOnce(key, "%s(null) — answered with an empty string", what);
    } else {
        apak_jniLogOnce("not-string", "%s on a %s, not a string — answered with an empty string", what,
                        o->cls ? o->cls->name : "?");
    }
    return NULL;
}

static void *jni_NewString(void *env, const uint16_t *chars, int32_t len) {
    ApakJObject *o;
    if (!chars && len > 0) return NULL;
    o = newStringObj(chars, len);
    return localRef(E(env), o);
}

static int32_t jni_GetStringLength(void *env, void *s) {
    ApakJObject *o = str(s, "GetStringLength");
    (void)env;
    return o ? o->u.s.len : 0;
}

static const uint16_t *jni_GetStringChars(void *env, void *s, uint8_t *isCopy) {
    ApakJObject *o = str(s, "GetStringChars");
    (void)env;
    if (isCopy) *isCopy = 0;
    return o ? o->u.s.chars : kEmptyU16;
}

static void jni_ReleaseStringChars(void *env, void *s, const uint16_t *chars) {
    (void)env;
    (void)s;
    (void)chars;
}

static void *jni_NewStringUTF(void *env, const char *utf) {
    if (!utf) return NULL;
    return localRef(E(env), newStringUtf8(utf));
}

static int32_t jni_GetStringUTFLength(void *env, void *s) {
    ApakJObject *o = str(s, "GetStringUTFLength");
    (void)env;
    return o ? o->u.s.utfLen : 0;
}

static const char *jni_GetStringUTFChars(void *env, void *s, uint8_t *isCopy) {
    ApakJObject *o = str(s, "GetStringUTFChars");
    (void)env;
    if (isCopy) *isCopy = 0;
    return o ? o->u.s.utf : "";
}

static void jni_ReleaseStringUTFChars(void *env, void *s, const char *utf) {
    (void)env;
    (void)s;
    (void)utf;
}

static void jni_GetStringRegion(void *env, void *s, int32_t start, int32_t len, uint16_t *buf) {
    ApakJObject *o = str(s, "GetStringRegion");
    if (!o || !buf) return;
    if (start < 0 || len < 0 || start > o->u.s.len - len) {
        apak_jniThrow(env, "java/lang/StringIndexOutOfBoundsException", "GetStringRegion");
        return;
    }
    memcpy(buf, o->u.s.chars + start, (size_t)len * sizeof *buf);
}

static void jni_GetStringUTFRegion(void *env, void *s, int32_t start, int32_t len, char *buf) {
    ApakJObject *o = str(s, "GetStringUTFRegion");
    if (!o || !buf) return;
    if (start < 0 || len < 0 || start > o->u.s.len - len) {
        apak_jniThrow(env, "java/lang/StringIndexOutOfBoundsException", "GetStringUTFRegion");
        return;
    }
    toMutf8(o->u.s.chars + start, len, buf);
}

static const uint16_t *jni_GetStringCritical(void *env, void *s, uint8_t *isCopy) {
    return jni_GetStringChars(env, s, isCopy);
}

static void jni_ReleaseStringCritical(void *env, void *s, const uint16_t *chars) {
    (void)env;
    (void)s;
    (void)chars;
}

/* ── arrays ──────────────────────────────────────────────────────────── */

static ApakJObject *arr(void *a, const char *what) {
    ApakJObject *o = obj(a);
    if (o && o->kind == K_ARRAY) return o;
    if (!o) {
        char key[96];
        snprintf(key, sizeof key, "%s-null", what);
        apak_jniLogOnce(key, "%s(null) — ignored", what);
    }
    return NULL;
}

static int32_t jni_GetArrayLength(void *env, void *a) {
    ApakJObject *o = arr(a, "GetArrayLength");
    (void)env;
    return o ? o->u.a.len : 0;
}

static void *jni_NewObjectArray(void *env, int32_t len, void *elemClass, void *init) {
    ApakJClass *ec = cls(elemClass);
    ApakJObject *o, *iv = obj(init);
    if (len < 0) {
        apak_jniThrow(env, "java/lang/NegativeArraySizeException", "NewObjectArray");
        return NULL;
    }
    lock();
    o = newArrayLocked('L', len, ec ? ec->name : "java/lang/Object");
    if (o && iv) {
        ApakJObject **items = (ApakJObject **)o->u.a.data;
        int32_t i;
        for (i = 0; i < len; i++) {
            items[i] = iv;
            retainLocked(iv);
        }
    }
    unlock();
    return localRef(E(env), o);
}

static void *jni_GetObjectArrayElement(void *env, void *a, int32_t i) {
    ApakJObject *o = arr(a, "GetObjectArrayElement"), *v;
    if (!o || o->u.a.elem != 'L') return NULL;
    if (i < 0 || i >= o->u.a.len) {
        apak_jniThrow(env, "java/lang/ArrayIndexOutOfBoundsException", "GetObjectArrayElement");
        return NULL;
    }
    v = ((ApakJObject **)o->u.a.data)[i];
    return localRef(E(env), v);
}

static void jni_SetObjectArrayElement(void *env, void *a, int32_t i, void *value) {
    ApakJObject *o = arr(a, "SetObjectArrayElement"), *v = obj(value), *old;
    if (!o || o->u.a.elem != 'L') return;
    if (i < 0 || i >= o->u.a.len) {
        apak_jniThrow(env, "java/lang/ArrayIndexOutOfBoundsException", "SetObjectArrayElement");
        return;
    }
    lock();
    old = ((ApakJObject **)o->u.a.data)[i];
    retainLocked(v);
    ((ApakJObject **)o->u.a.data)[i] = v;
    releaseLocked(old);
    unlock();
}

static void *newPrimArray(void *env, char t, int32_t len) {
    ApakJObject *o;
    if (len < 0) {
        apak_jniThrow(env, "java/lang/NegativeArraySizeException", "New<Type>Array");
        return NULL;
    }
    lock();
    o = newArrayLocked(t, len, NULL);
    unlock();
    return localRef(E(env), o);
}

static void *primData(void *a, char t, uint8_t *isCopy) {
    ApakJObject *o = arr(a, "Get<Type>ArrayElements");
    if (isCopy) *isCopy = 0;
    if (!o) return NULL;
    if (o->u.a.elem != t) {
        char key[64];
        snprintf(key, sizeof key, "array-type %c %c", o->u.a.elem, t);
        apak_jniLogOnce(key, "Get<Type>ArrayElements: a [%c array read as [%c", o->u.a.elem, t);
    }
    return o->u.a.data;
}

static void primRegion(void *env, void *a, char t, int32_t start, int32_t len, void *buf, int write) {
    ApakJObject *o = arr(a, write ? "Set<Type>ArrayRegion" : "Get<Type>ArrayRegion");
    size_t es = elemSize(t);
    if (!o || !buf) return;
    if (start < 0 || len < 0 || start > o->u.a.len - len) {
        apak_jniThrow(env, "java/lang/ArrayIndexOutOfBoundsException", write ? "Set<Type>ArrayRegion" : "Get<Type>ArrayRegion");
        return;
    }
    if (write) memcpy((char *)o->u.a.data + (size_t)start * es, buf, (size_t)len * es);
    else memcpy(buf, (char *)o->u.a.data + (size_t)start * es, (size_t)len * es);
}

#define ARRAYS(Name, CType, T)                                                                                        \
    static void *jni_New##Name##Array(void *env, int32_t len) { return newPrimArray(env, T, len); }                  \
    static CType *jni_Get##Name##ArrayElements(void *env, void *a, uint8_t *isCopy) {                               \
        (void)env;                                                                                                   \
        return (CType *)primData(a, T, isCopy);                                                                      \
    }                                                                                                                \
    static void jni_Release##Name##ArrayElements(void *env, void *a, CType *elems, int32_t mode) {                  \
        (void)env;                                                                                                   \
        (void)a;                                                                                                     \
        (void)elems;                                                                                                 \
        (void)mode;                                                                                                  \
    }                                                                                                                \
    static void jni_Get##Name##ArrayRegion(void *env, void *a, int32_t start, int32_t len, CType *buf) {            \
        primRegion(env, a, T, start, len, buf, 0);                                                                   \
    }                                                                                                                \
    static void jni_Set##Name##ArrayRegion(void *env, void *a, int32_t start, int32_t len, const CType *buf) {      \
        primRegion(env, a, T, start, len, (void *)buf, 1);                                                           \
    }

ARRAYS(Boolean, uint8_t, 'Z')
ARRAYS(Byte, int8_t, 'B')
ARRAYS(Char, uint16_t, 'C')
ARRAYS(Short, int16_t, 'S')
ARRAYS(Int, int32_t, 'I')
ARRAYS(Long, int64_t, 'J')
ARRAYS(Float, float, 'F')
ARRAYS(Double, double, 'D')

static void *jni_GetPrimitiveArrayCritical(void *env, void *a, uint8_t *isCopy) {
    ApakJObject *o = arr(a, "GetPrimitiveArrayCritical");
    (void)env;
    if (isCopy) *isCopy = 0;
    return o ? o->u.a.data : NULL;
}

static void jni_ReleasePrimitiveArrayCritical(void *env, void *a, void *carray, int32_t mode) {
    (void)env;
    (void)a;
    (void)carray;
    (void)mode;
}

/* ── natives, monitors, VM, buffers ─────────────────────────────────── */

static int32_t jni_RegisterNatives(void *env, void *c, const JNINativeMethodAbi *methods, int32_t n) {
    ApakJClass *k = cls(c);
    int32_t i;
    (void)env;
    if (!k || !methods || n < 0) {
        apak_jniLogOnce("RegisterNatives-null", "RegisterNatives on a null class — JNI_ERR");
        return APAK_JNI_ERR;
    }
    lock();
    for (i = 0; i < n; i++) {
        size_t j;
        int replaced = 0;
        if (!methods[i].name || !methods[i].signature || !methods[i].fnPtr) continue;
        for (j = 0; j < k->nnatives; j++) {
            if (strcmp(k->natives[j].name, methods[i].name) == 0 && strcmp(k->natives[j].sig, methods[i].signature) == 0) {
                k->natives[j].fn = methods[i].fnPtr;
                replaced = 1;
            }
        }
        if (replaced) continue;
        if (k->nnatives == k->capNatives) {
            size_t cap = k->capNatives ? k->capNatives * 2 : 16;
            NativeReg *grown = (NativeReg *)realloc(k->natives, cap * sizeof *grown);
            if (!grown) break;
            k->natives = grown;
            k->capNatives = cap;
        }
        k->natives[k->nnatives].name = strdup(methods[i].name);
        k->natives[k->nnatives].sig = strdup(methods[i].signature);
        k->natives[k->nnatives].fn = methods[i].fnPtr;
        k->nnatives++;
        if (getenv("ANDROIDPAK_DEBUG"))
            fprintf(stderr, JNI_TAG "RegisterNatives %s.%s%s\n", k->name, methods[i].name, methods[i].signature);
    }
    unlock();
    fprintf(stderr, JNI_TAG "RegisterNatives %s: %d method%s\n", k->name, (int)n, n == 1 ? "" : "s");
    return APAK_JNI_OK;
}

static int32_t jni_UnregisterNatives(void *env, void *c) {
    ApakJClass *k = cls(c);
    size_t i;
    (void)env;
    if (!k) return APAK_JNI_ERR;
    lock();
    for (i = 0; i < k->nnatives; i++) {
        free(k->natives[i].name);
        free(k->natives[i].sig);
    }
    k->nnatives = 0;
    unlock();
    return APAK_JNI_OK;
}

static pthread_mutex_t *monitorOf(ApakJObject *o) {
    lock();
    if (!o->monitor) {
        pthread_mutexattr_t a;
        o->monitor = (pthread_mutex_t *)malloc(sizeof *o->monitor);
        if (o->monitor) {
            pthread_mutexattr_init(&a);
            pthread_mutexattr_settype(&a, PTHREAD_MUTEX_RECURSIVE);
            pthread_mutex_init(o->monitor, &a);
            pthread_mutexattr_destroy(&a);
        }
    }
    unlock();
    return o->monitor;
}

static int32_t jni_MonitorEnter(void *env, void *o) {
    ApakJObject *p = obj(o);
    pthread_mutex_t *m;
    (void)env;
    if (!p || !(m = monitorOf(p))) return APAK_JNI_ERR;
    pthread_mutex_lock(m);
    return APAK_JNI_OK;
}

static int32_t jni_MonitorExit(void *env, void *o) {
    ApakJObject *p = obj(o);
    (void)env;
    if (!p || !p->monitor) return APAK_JNI_ERR;
    pthread_mutex_unlock(p->monitor);
    return APAK_JNI_OK;
}

static int32_t jni_GetJavaVM(void *env, void **vm) {
    (void)env;
    if (vm) *vm = (void *)&g_vmPtr;
    return APAK_JNI_OK;
}

static void *jni_NewWeakGlobalRef(void *env, void *ref) { return jni_NewGlobalRef(env, ref); }
static void jni_DeleteWeakGlobalRef(void *env, void *ref) { jni_DeleteGlobalRef(env, ref); }

static uint8_t jni_ExceptionCheck(void *env) { return E(env)->pending != NULL; }

static void *jni_NewDirectByteBuffer(void *env, void *addr, int64_t cap) {
    ApakJObject *o;
    lock();
    o = allocObject(findClassLocked("java/nio/DirectByteBuffer", 0), K_DBUF);
    if (o) {
        if (!o->cls) o->cls = g_objectClass;
        o->u.d.addr = addr;
        o->u.d.cap = cap;
    }
    unlock();
    return localRef(E(env), o);
}

static void *jni_GetDirectBufferAddress(void *env, void *b) {
    ApakJObject *o = obj(b);
    (void)env;
    return o && o->kind == K_DBUF ? o->u.d.addr : NULL;
}

static int64_t jni_GetDirectBufferCapacity(void *env, void *b) {
    ApakJObject *o = obj(b);
    (void)env;
    return o && o->kind == K_DBUF ? o->u.d.cap : -1;
}

static int32_t jni_GetObjectRefType(void *env, void *ref) {
    ApakJObject *o = (ApakJObject *)ref;
    (void)env;
    if (!o || o->magic != OBJ_MAGIC) return 0;              /* JNIInvalidRefType   */
    return (o->globals > 0 || (o->flags & F_PERMANENT)) ? 2 /* JNIGlobalRefType    */
                                                        : 1; /* JNILocalRefType     */
}

/* ── JavaVM ──────────────────────────────────────────────────────────── */

static int32_t vm_DestroyJavaVM(void *vm) {
    (void)vm;
    return APAK_JNI_OK;
}

static int32_t vm_Attach(void *vm, void **env, void *args) {
    ApakJEnv *e;
    (void)vm;
    (void)args;
    e = threadEnv(1);
    if (env) *env = e;
    return e ? APAK_JNI_OK : APAK_JNI_ERR;
}

static int32_t vm_Detach(void *vm) {
    ApakJEnv *e;
    (void)vm;
    e = threadEnv(0);
    if (!e || e == g_mainEnv) return APAK_JNI_OK;
    pthread_setspecific(g_envKey, NULL);
    envDestroy(e);
    return APAK_JNI_OK;
}

static int32_t vm_GetEnv(void *vm, void **env, int32_t version) {
    (void)vm;
    if (version != 0x00010001 && version != 0x00010002 && version != 0x00010004 && version != APAK_JNI_VERSION_1_6) {
        if (env) *env = NULL;
        apak_jniLogOnce("GetEnv-version", "GetEnv(version 0x%x) -> JNI_EVERSION (Android supports up to 1.6)",
                        (unsigned)version);
        return APAK_JNI_EVERSION;
    }
    if (env) *env = threadEnv(1);
    return APAK_JNI_OK;
}

/* Exported for libraries that ask the invocation API (libnativehelper). */
int32_t apak_jniGetCreatedJavaVMs(void **vms, int32_t cap, int32_t *count);
int32_t apak_jniGetCreatedJavaVMs(void **vms, int32_t cap, int32_t *count) {
    if (count) *count = 1;
    if (vms && cap > 0) vms[0] = (void *)&g_vmPtr;
    return APAK_JNI_OK;
}

/* ── the tables ──────────────────────────────────────────────────────── */

#define T(i, f) g_table[i] = (const void *)(f)
#define T3(i, Name) T(i, jni_##Name), T((i) + 1, jni_##Name##V), T((i) + 2, jni_##Name##A)

static void buildTables(void) {
    T(4, jni_GetVersion);
    T(5, jni_DefineClass);
    T(6, jni_FindClass);
    T(7, jni_FromReflectedMethod);
    T(8, jni_FromReflectedField);
    T(9, jni_ToReflectedMethod);
    T(10, jni_GetSuperclass);
    T(11, jni_IsAssignableFrom);
    T(12, jni_ToReflectedField);
    T(13, jni_Throw);
    T(14, jni_ThrowNew);
    T(15, jni_ExceptionOccurred);
    T(16, jni_ExceptionDescribe);
    T(17, jni_ExceptionClear);
    T(18, jni_FatalError);
    T(19, jni_PushLocalFrame);
    T(20, jni_PopLocalFrame);
    T(21, jni_NewGlobalRef);
    T(22, jni_DeleteGlobalRef);
    T(23, jni_DeleteLocalRef);
    T(24, jni_IsSameObject);
    T(25, jni_NewLocalRef);
    T(26, jni_EnsureLocalCapacity);
    T(27, jni_AllocObject);
    T3(28, NewObject);
    T(31, jni_GetObjectClass);
    T(32, jni_IsInstanceOf);
    T(33, jni_GetMethodID);
    T3(34, CallObjectMethod);
    T3(37, CallBooleanMethod);
    T3(40, CallByteMethod);
    T3(43, CallCharMethod);
    T3(46, CallShortMethod);
    T3(49, CallIntMethod);
    T3(52, CallLongMethod);
    T3(55, CallFloatMethod);
    T3(58, CallDoubleMethod);
    T3(61, CallVoidMethod);
    T3(64, CallNonvirtualObjectMethod);
    T3(67, CallNonvirtualBooleanMethod);
    T3(70, CallNonvirtualByteMethod);
    T3(73, CallNonvirtualCharMethod);
    T3(76, CallNonvirtualShortMethod);
    T3(79, CallNonvirtualIntMethod);
    T3(82, CallNonvirtualLongMethod);
    T3(85, CallNonvirtualFloatMethod);
    T3(88, CallNonvirtualDoubleMethod);
    T3(91, CallNonvirtualVoidMethod);
    T(94, jni_GetFieldID);
    T(95, jni_GetObjectField);
    T(96, jni_GetBooleanField);
    T(97, jni_GetByteField);
    T(98, jni_GetCharField);
    T(99, jni_GetShortField);
    T(100, jni_GetIntField);
    T(101, jni_GetLongField);
    T(102, jni_GetFloatField);
    T(103, jni_GetDoubleField);
    T(104, jni_SetObjectField);
    T(105, jni_SetBooleanField);
    T(106, jni_SetByteField);
    T(107, jni_SetCharField);
    T(108, jni_SetShortField);
    T(109, jni_SetIntField);
    T(110, jni_SetLongField);
    T(111, jni_SetFloatField);
    T(112, jni_SetDoubleField);
    T(113, jni_GetStaticMethodID);
    T3(114, CallStaticObjectMethod);
    T3(117, CallStaticBooleanMethod);
    T3(120, CallStaticByteMethod);
    T3(123, CallStaticCharMethod);
    T3(126, CallStaticShortMethod);
    T3(129, CallStaticIntMethod);
    T3(132, CallStaticLongMethod);
    T3(135, CallStaticFloatMethod);
    T3(138, CallStaticDoubleMethod);
    T3(141, CallStaticVoidMethod);
    T(144, jni_GetStaticFieldID);
    T(145, jni_GetStaticObjectField);
    T(146, jni_GetStaticBooleanField);
    T(147, jni_GetStaticByteField);
    T(148, jni_GetStaticCharField);
    T(149, jni_GetStaticShortField);
    T(150, jni_GetStaticIntField);
    T(151, jni_GetStaticLongField);
    T(152, jni_GetStaticFloatField);
    T(153, jni_GetStaticDoubleField);
    T(154, jni_SetStaticObjectField);
    T(155, jni_SetStaticBooleanField);
    T(156, jni_SetStaticByteField);
    T(157, jni_SetStaticCharField);
    T(158, jni_SetStaticShortField);
    T(159, jni_SetStaticIntField);
    T(160, jni_SetStaticLongField);
    T(161, jni_SetStaticFloatField);
    T(162, jni_SetStaticDoubleField);
    T(163, jni_NewString);
    T(164, jni_GetStringLength);
    T(165, jni_GetStringChars);
    T(166, jni_ReleaseStringChars);
    T(167, jni_NewStringUTF);
    T(168, jni_GetStringUTFLength);
    T(169, jni_GetStringUTFChars);
    T(170, jni_ReleaseStringUTFChars);
    T(171, jni_GetArrayLength);
    T(172, jni_NewObjectArray);
    T(173, jni_GetObjectArrayElement);
    T(174, jni_SetObjectArrayElement);
    T(175, jni_NewBooleanArray);
    T(176, jni_NewByteArray);
    T(177, jni_NewCharArray);
    T(178, jni_NewShortArray);
    T(179, jni_NewIntArray);
    T(180, jni_NewLongArray);
    T(181, jni_NewFloatArray);
    T(182, jni_NewDoubleArray);
    T(183, jni_GetBooleanArrayElements);
    T(184, jni_GetByteArrayElements);
    T(185, jni_GetCharArrayElements);
    T(186, jni_GetShortArrayElements);
    T(187, jni_GetIntArrayElements);
    T(188, jni_GetLongArrayElements);
    T(189, jni_GetFloatArrayElements);
    T(190, jni_GetDoubleArrayElements);
    T(191, jni_ReleaseBooleanArrayElements);
    T(192, jni_ReleaseByteArrayElements);
    T(193, jni_ReleaseCharArrayElements);
    T(194, jni_ReleaseShortArrayElements);
    T(195, jni_ReleaseIntArrayElements);
    T(196, jni_ReleaseLongArrayElements);
    T(197, jni_ReleaseFloatArrayElements);
    T(198, jni_ReleaseDoubleArrayElements);
    T(199, jni_GetBooleanArrayRegion);
    T(200, jni_GetByteArrayRegion);
    T(201, jni_GetCharArrayRegion);
    T(202, jni_GetShortArrayRegion);
    T(203, jni_GetIntArrayRegion);
    T(204, jni_GetLongArrayRegion);
    T(205, jni_GetFloatArrayRegion);
    T(206, jni_GetDoubleArrayRegion);
    T(207, jni_SetBooleanArrayRegion);
    T(208, jni_SetByteArrayRegion);
    T(209, jni_SetCharArrayRegion);
    T(210, jni_SetShortArrayRegion);
    T(211, jni_SetIntArrayRegion);
    T(212, jni_SetLongArrayRegion);
    T(213, jni_SetFloatArrayRegion);
    T(214, jni_SetDoubleArrayRegion);
    T(215, jni_RegisterNatives);
    T(216, jni_UnregisterNatives);
    T(217, jni_MonitorEnter);
    T(218, jni_MonitorExit);
    T(219, jni_GetJavaVM);
    T(220, jni_GetStringRegion);
    T(221, jni_GetStringUTFRegion);
    T(222, jni_GetPrimitiveArrayCritical);
    T(223, jni_ReleasePrimitiveArrayCritical);
    T(224, jni_GetStringCritical);
    T(225, jni_ReleaseStringCritical);
    T(226, jni_NewWeakGlobalRef);
    T(227, jni_DeleteWeakGlobalRef);
    T(228, jni_ExceptionCheck);
    T(229, jni_NewDirectByteBuffer);
    T(230, jni_GetDirectBufferAddress);
    T(231, jni_GetDirectBufferCapacity);
    T(232, jni_GetObjectRefType);
    g_invoke[3] = (const void *)vm_DestroyJavaVM;
    g_invoke[4] = (const void *)vm_Attach;
    g_invoke[5] = (const void *)vm_Detach;
    g_invoke[6] = (const void *)vm_GetEnv;
    g_invoke[7] = (const void *)vm_Attach; /* AttachCurrentThreadAsDaemon */
}

/* ── bootstrap and the runner API ───────────────────────────────────── */

static char *dupOr(const char *s, const char *dflt) { return strdup(s ? s : dflt); }

int apak_jniEnabled(void) {
    const char *v = getenv("ANDROIDPAK_JNI");
    return !(v && strcmp(v, "off") == 0);
}

void apak_jniAddPack(const ApakJniPack *pack) {
    if (pack && g_npacks < (int)(sizeof g_packs / sizeof g_packs[0])) g_packs[g_npacks++] = pack;
}

static void bootstrapLocked(void) {
    ApakJClass *c;
    g_objectClass = newClassLocked("java/lang/Object", NULL);
    g_classClass = newClassLocked("java/lang/Class", "java/lang/Object");
    g_objectClass->obj.cls = g_classClass;
    g_classClass->obj.cls = g_classClass;
    g_stringClass = newClassLocked("java/lang/String", "java/lang/Object");
    g_stringClass->stringKind = 1;
    g_throwableClass = newClassLocked("java/lang/Throwable", "java/lang/Object");
    c = g_objectClass;
    c->superResolved = 1;
}

static const char *activityClassFor(char *buf, size_t cap) {
    int i;
    for (i = 0; i < g_nactive; i++)
        if (g_active[i]->activityClass) return g_active[i]->activityClass;
    if (g_cfg.activityClass && *g_cfg.activityClass) {
        normalizeName(g_cfg.activityClass, buf, cap);
        return buf;
    }
    return g_cfg.gameActivity ? "com/google/androidgamesdk/GameActivity" : "android/app/NativeActivity";
}

static void initOnce(const ApakJniConfig *cfg) {
    static ApakJniConfig empty;
    char actName[512];
    const char *act;
    ApakJClass *ac;
    int i;
    const char *unknown = getenv("ANDROIDPAK_JNI_UNKNOWN");
    if (g_ready) return;
    if (!cfg) cfg = &empty;
    g_trace = getenv("ANDROIDPAK_JNI_TRACE") && strcmp(getenv("ANDROIDPAK_JNI_TRACE"), "0") != 0;
    g_unknownPhantom = unknown && strcmp(unknown, "phantom") == 0;
    g_cfg = *cfg;
    g_cfg.packageName = dupOr(cfg->packageName, "");
    g_cfg.activityClass = dupOr(cfg->activityClass, "");
    g_cfg.versionName = dupOr(cfg->versionName, "");
    g_cfg.apkPath = dupOr(cfg->apkPath, "");
    g_cfg.stagedRoot = dupOr(cfg->stagedRoot, ".");
    g_cfg.libDir = dupOr(cfg->libDir, "");
    g_cfg.dataRoot = dupOr(cfg->dataRoot, ".");
    g_cfg.filesDir = dupOr(cfg->filesDir, g_cfg.dataRoot);
    g_cfg.externalDir = dupOr(cfg->externalDir, g_cfg.dataRoot);
    g_cfg.obbDir = dupOr(cfg->obbDir, g_cfg.dataRoot);
    g_cfg.cacheDir = dupOr(cfg->cacheDir, g_cfg.dataRoot);
    g_cfg.model = dupOr(cfg->model, "TrimUI Smart Pro");
    g_cfg.board = dupOr(cfg->board, "tg5040");
    if (g_cfg.panelW <= 0 || g_cfg.panelH <= 0) {
        g_cfg.panelW = 1280;
        g_cfg.panelH = 720;
    }
    if (g_cfg.dpi <= 0.0f) g_cfg.dpi = 296.0f;
    if (g_cfg.sdkVersion <= 0) g_cfg.sdkVersion = ANDROIDRT_SDK_VERSION;

    buildTables();
    pthread_once(&g_keyOnce, keyInit);
    g_mainEnv = threadEnv(1);
    if (g_mainEnv) g_mainEnv->isMain = 1;
    lock();
    bootstrapLocked();
    unlock();
    apak_jniDefineBase();
    g_nactive = 0;
    for (i = 0; i < g_npacks; i++) {
        int k;
        for (k = 0; g_packs[i]->packages && g_packs[i]->packages[k]; k++) {
            if (strcmp(g_packs[i]->packages[k], g_cfg.packageName) == 0) {
                g_active[g_nactive++] = g_packs[i];
                break;
            }
        }
    }
    for (i = 0; i < g_nactive; i++) {
        if (g_active[i]->define) g_active[i]->define(&g_cfg);
        fprintf(stderr, JNI_TAG "class pack \"%s\" for %s\n", g_active[i]->name, g_cfg.packageName);
    }

    act = activityClassFor(actName, sizeof actName);
    lock();
    ac = findClassLocked(act, 1);
    if (!ac) {
        /* the manifest names an activity no DEX index confirms: model it as
         * the framework class the entry point implies */
        ac = newClassLocked(act, g_cfg.gameActivity ? "com/google/androidgamesdk/GameActivity"
                                                    : "android/app/NativeActivity");
        if (ac) ac->phantom = 1;
    }
    g_activity = ac ? newInstanceLocked(ac) : NULL;
    if (g_activity) g_activity->flags |= F_PERMANENT;
    unlock();
    g_ready = 1;
    fprintf(stderr, JNI_TAG "Java environment: activity %s%s, JNI 1.6, %d class pack%s\n", ac ? ac->name : "?",
            ac && ac->phantom ? " (no implementation: defaults)" : "", g_nactive, g_nactive == 1 ? "" : "s");
}

void apak_jniInit(const ApakJniConfig *cfg) {
    if (!apak_jniEnabled()) return;
    initOnce(cfg);
}

const ApakJniConfig *apak_jniConfig(void) { return &g_cfg; }

/* The original actbridge.h entry points (ANDROIDPAK_JNI=off: NULL). */
void *apak_jniVM(void) {
    if (!apak_jniEnabled()) return NULL;
    initOnce(NULL);
    return (void *)&g_vmPtr;
}

void *apak_jniEnv(void) {
    if (!apak_jniEnabled()) return NULL;
    initOnce(NULL);
    return g_mainEnv;
}

void *apak_jniActivityObject(void) {
    if (!apak_jniEnabled()) return NULL;
    initOnce(NULL);
    return g_activity;
}

void *apak_jniMainEnv(void) { return g_mainEnv; }
void *apak_jniThreadEnv(void) { return threadEnv(1); }
ApakJObject *apak_jniActivity(void) { return g_activity; }

int apak_jniCallOnLoad(void *onLoad, const char *libName) {
    typedef int32_t (*OnLoad)(void *vm, void *reserved);
    int32_t v;
    if (!onLoad || !g_ready) return -1;
    fprintf(stderr, JNI_TAG "JNI_OnLoad of %s\n", libName ? libName : "?");
    pushFrame(g_mainEnv);
    v = ((OnLoad)onLoad)((void *)&g_vmPtr, NULL);
    lock();
    popFrameLocked(g_mainEnv);
    unlock();
    apak_jniExceptionReport(g_mainEnv, "JNI_OnLoad");
    if (v != 0x00010001 && v != 0x00010002 && v != 0x00010004 && v != APAK_JNI_VERSION_1_6)
        fprintf(stderr, JNI_TAG "%s: JNI_OnLoad returned version 0x%x — Android would refuse to load this library\n",
                libName ? libName : "?", (unsigned)v);
    return (int)v;
}

void apak_jniEvent(int event) {
    int i;
    for (i = 0; i < g_nactive; i++)
        if (g_active[i]->event) g_active[i]->event(event);
}

typedef struct Task {
    void (*fn)(void *);
    void *arg;
    struct Task *next;
} Task;
static Task *g_tasks, *g_tasksTail;
static pthread_mutex_t g_taskLock = PTHREAD_MUTEX_INITIALIZER;

void apak_jniPost(void (*fn)(void *arg), void *arg) {
    Task *t = (Task *)calloc(1, sizeof *t);
    if (!t) return;
    t->fn = fn;
    t->arg = arg;
    pthread_mutex_lock(&g_taskLock);
    if (g_tasksTail) g_tasksTail->next = t;
    else g_tasks = t;
    g_tasksTail = t;
    pthread_mutex_unlock(&g_taskLock);
}

void apak_jniPump(void) {
    for (;;) {
        Task *t;
        pthread_mutex_lock(&g_taskLock);
        t = g_tasks;
        if (t) {
            g_tasks = t->next;
            if (!g_tasks) g_tasksTail = NULL;
        }
        pthread_mutex_unlock(&g_taskLock);
        if (!t) break;
        if (g_mainEnv) pushFrame(g_mainEnv);
        t->fn(t->arg);
        if (g_mainEnv) {
            lock();
            popFrameLocked(g_mainEnv);
            unlock();
            apak_jniExceptionReport(g_mainEnv, "a posted callback");
        }
        free(t);
    }
}

/* In the order the game asked (the list is built newest-first): the first
 * entries are what stops a game first, and JukaHub's launch toast names the
 * first one (androidpak.go). */
static void printList(const char *title, ReportItem *list, size_t count) {
    ReportItem *r, **order;
    size_t shown = 0, n = 0;
    if (!count) return;
    fprintf(stderr, JNI_TAG "report: %s (%zu):\n", title, count);
    order = (ReportItem **)malloc(count * sizeof *order);
    if (order) {
        for (r = list; r && n < count; r = r->next) order[n++] = r;
        for (; shown < n && shown < 60; shown++) fprintf(stderr, JNI_TAG "report:   %s\n", order[n - 1 - shown]->text);
        free(order);
    } else {
        for (r = list; r && shown < 60; r = r->next, shown++) fprintf(stderr, JNI_TAG "report:   %s\n", r->text);
    }
    if (count > shown) fprintf(stderr, JNI_TAG "report:   ... and %zu more (each was logged when first seen)\n", count - shown);
}

void apak_jniReport(void) {
    if (!g_ready) return;
    lock();
    fprintf(stderr, JNI_TAG "report: %zu unknown classes, %zu APK classes without implementation, %zu unimplemented "
                    "members (%zu called)\n",
            g_nUnknownClasses, g_nApkClasses, g_nPhantomMembers - g_nPhantomCalls, g_nPhantomCalls);
    printList("FindClass returned null for", g_unknownClasses, g_nUnknownClasses);
    printList("unimplemented members (\"call ...\" = the game called it)", g_phantomMembers, g_nPhantomMembers);
    unlock();
}

/* ── helpers for implementations ────────────────────────────────────── */

ApakJObject *apak_jniNewString(const char *utf8) { return newStringUtf8(utf8); }
ApakJObject *apak_jniNewStringUtf16(const uint16_t *chars, int32_t len) { return newStringObj(chars, len); }

ApakJObject *apak_jniNewInstance(const char *className) {
    ApakJObject *o;
    lock();
    o = newInstanceLocked(findClassLocked(className, 1));
    unlock();
    return o;
}

ApakJObject *apak_jniNewArray(char elemType, int32_t len, const char *elemClass) {
    ApakJObject *o;
    lock();
    o = newArrayLocked(elemType, len, elemClass);
    unlock();
    return o;
}

ApakJObject *apak_jniNewByteArray(const void *data, size_t len) {
    ApakJObject *o;
    if (len > 0x7fffffff) return NULL;
    o = apak_jniNewArray('B', (int32_t)len, NULL);
    if (o && len && data) memcpy(o->u.a.data, data, len);
    return o;
}

ApakJObject *apak_jniNewStringArray(const char *const *items, size_t n) {
    ApakJObject *o;
    size_t i;
    if (n > 0x7fffffff) return NULL;
    o = apak_jniNewArray('L', (int32_t)n, "java/lang/String");
    for (i = 0; o && i < n; i++) apak_jniArraySet(o, (int32_t)i, apak_jniNewString(items[i]));
    return o;
}

int apak_jniArraySet(ApakJObject *array, int32_t index, ApakJObject *value) {
    ApakJObject *old;
    if (!array || array->kind != K_ARRAY || array->u.a.elem != 'L' || index < 0 || index >= array->u.a.len) return -1;
    lock();
    old = ((ApakJObject **)array->u.a.data)[index];
    retainLocked(value);
    ((ApakJObject **)array->u.a.data)[index] = value;
    releaseLocked(old);
    unlock();
    return 0;
}

void *apak_jniArrayData(ApakJObject *array, int32_t *len, char *elemType) {
    if (!array || array->magic != OBJ_MAGIC || array->kind != K_ARRAY) {
        if (len) *len = 0;
        return NULL;
    }
    if (len) *len = array->u.a.len;
    if (elemType) *elemType = array->u.a.elem;
    return array->u.a.data;
}

const char *apak_jniStringUtf(ApakJObject *s) {
    return s && s->magic == OBJ_MAGIC && s->kind == K_STRING ? s->u.s.utf : NULL;
}

int32_t apak_jniStringLength(ApakJObject *s) {
    return s && s->magic == OBJ_MAGIC && s->kind == K_STRING ? s->u.s.len : 0;
}

/* String contents of an existing string object (String constructors). */
void apak_jniStringSet(ApakJObject *s, const uint16_t *chars, int32_t len);
void apak_jniStringSet(ApakJObject *s, const uint16_t *chars, int32_t len) {
    if (!s || s->magic != OBJ_MAGIC || s->kind != K_STRING) return;
    lock();
    stringReplace(s, chars, len);
    unlock();
}

void apak_jniStringSetUtf8(ApakJObject *s, const char *utf8, size_t len);
void apak_jniStringSetUtf8(ApakJObject *s, const char *utf8, size_t len) {
    int32_t n;
    uint16_t *buf;
    if (!s || !utf8) return;
    n = utf8ToUtf16(utf8, len, NULL);
    buf = (uint16_t *)malloc(((size_t)n + 1) * sizeof *buf);
    if (!buf) return;
    utf8ToUtf16(utf8, len, buf);
    apak_jniStringSet(s, buf, n);
    free(buf);
}

const uint16_t *apak_jniStringChars(ApakJObject *s);
const uint16_t *apak_jniStringChars(ApakJObject *s) {
    return s && s->magic == OBJ_MAGIC && s->kind == K_STRING ? s->u.s.chars : NULL;
}

ApakJObject *apak_jniRetain(ApakJObject *o) {
    lock();
    retainLocked(o);
    unlock();
    return o;
}

void apak_jniRelease(ApakJObject *o) {
    lock();
    releaseLocked(o);
    unlock();
}

static ApakJField *fieldByName(ApakJObject *o, const char *name, int isStatic, ApakJClass *c) {
    ApakJField *f;
    lock();
    if (!c && o) c = o->cls;
    if (c) computeLayoutLocked(c);
    f = c ? findFieldLocked(c, name, NULL, isStatic) : NULL;
    unlock();
    return f;
}

int apak_jniFieldSet(ApakJObject *o, const char *name, ApakJValue v) {
    ApakJField *f;
    if (!o || o->magic != OBJ_MAGIC) return -1;
    f = fieldByName(o, name, 0, NULL);
    if (!f) return -1;
    fieldSet(o, f, v);
    return 0;
}

int apak_jniFieldGet(ApakJObject *o, const char *name, ApakJValue *v) {
    ApakJField *f;
    ApakJValue *slot;
    if (!o || o->magic != OBJ_MAGIC || !v) return -1;
    f = fieldByName(o, name, 0, NULL);
    if (!f) return -1;
    lock();
    slot = slotFor(o, f, 0);
    v->j = 0;
    if (slot) *v = *slot;
    unlock();
    return 0;
}

int apak_jniStaticSet(const char *className, const char *name, ApakJValue v) {
    ApakJClass *c;
    ApakJField *f;
    lock();
    c = findClassLocked(className, 0);
    unlock();
    if (!c) return -1;
    f = fieldByName(NULL, name, 1, c);
    if (!f) return -1;
    staticSet(f, v);
    return 0;
}

void apak_jniSetPayload(ApakJObject *o, void *payload, void (*freeFn)(void *)) {
    if (!o || o->magic != OBJ_MAGIC) return;
    lock();
    if (o->freePayload && o->payload && o->payload != payload) o->freePayload(o->payload);
    o->payload = payload;
    o->freePayload = freeFn;
    unlock();
}

void *apak_jniPayload(ApakJObject *o) { return o && o->magic == OBJ_MAGIC ? o->payload : NULL; }

ApakJClass *apak_jniFindClass(const char *name) {
    ApakJClass *c;
    lock();
    c = findClassLocked(name, 1);
    unlock();
    return c;
}

ApakJClass *apak_jniClassOf(ApakJObject *o) { return o && o->magic == OBJ_MAGIC ? o->cls : NULL; }
const char *apak_jniClassName(const ApakJClass *c) { return c ? c->name : ""; }

ApakJClass *apak_jniAsClass(ApakJObject *o) {
    return o && o->magic == OBJ_MAGIC && o->kind == K_CLASS ? (ApakJClass *)o : NULL;
}

ApakJClass *apak_jniSuperclass(ApakJClass *c) {
    ApakJClass *s;
    lock();
    s = superOfLocked(c);
    unlock();
    return s;
}

int apak_jniCallMethod(void *env, ApakJObject *self, const char *name, const char *sig, const ApakJValue *args,
                       ApakJValue *ret) {
    ApakJValue local, conv[APAK_JNI_MAXARGS];
    ApakJMethod *m;
    if (!ret) ret = &local;
    memset(ret, 0, sizeof *ret);
    if (!self || self->magic != OBJ_MAGIC || !name || !sig) return -1;
    m = lookupMethod(self->cls, name, sig, 0);
    if (!m) return -1;
    argsFromA(m, args, conv);
    invoke(E(env ? env : g_mainEnv), CALL_VIRTUAL, self, NULL, m, conv, ret);
    if (m->ret == 'L' && ret->l) {
        /* invoke() made it a local of env; the caller treats it as borrowed */
    }
    return 0;
}

int apak_jniIsInstance(ApakJObject *o, const char *className) {
    ApakJClass *c;
    int r;
    if (!o || o->magic != OBJ_MAGIC) return 0;
    lock();
    c = findClassLocked(className, 0);
    r = c && isSubclassLocked(o->cls, c);
    unlock();
    return r;
}

/* The class object behind a ClassLoader/Class.forName request. */
ApakJClass *apak_jniLoadClass(const char *dottedOrSlashed);
ApakJClass *apak_jniLoadClass(const char *name) { return apak_jniFindClass(name); }

int apak_jniHasNative(const char *className, const char *name, const char *sig) {
    ApakJClass *c;
    void *fn;
    lock();
    c = findClassLocked(className, 1);
    fn = c ? findNativeLocked(c, name, sig, NULL) : NULL;
    unlock();
    return fn != NULL;
}

const char *apak_jniNativeSig(const char *className, const char *name) {
    ApakJClass *c;
    const char *sig = "";
    size_t i;
    lock();
    c = findClassLocked(className, 1);
    for (i = 0; c && i < c->nnatives; i++)
        if (strcmp(c->natives[i].name, name) == 0) {
            sig = c->natives[i].sig;
            break;
        }
    unlock();
    return sig;
}

int apak_jniCallNative(void *env, ApakJObject *self, const char *className, const char *name, const char *sig,
                       const ApakJValue *args, ApakJValue *ret) {
    ApakJEnv *e = E(env ? env : g_mainEnv);
    ApakJClass *c, *owner = NULL;
    void *fn;
    char types[APAK_JNI_MAXARGS], rt;
    int nargs, rc;
    ApakJValue local;
    if (!ret) ret = &local;
    memset(ret, 0, sizeof *ret);
    if (parseSig(sig, types, &nargs, &rt) != 0) return -1;
    lock();
    c = className ? findClassLocked(className, 1) : self ? self->cls : NULL;
    fn = c ? findNativeLocked(c, name, sig, &owner) : NULL;
    unlock();
    if (!fn) {
        char key[640];
        const char *registered = c ? apak_jniNativeSig(c->name, name) : "";
        snprintf(key, sizeof key, "no-native %s.%s%s", className ? className : "?", name, sig);
        if (registered[0])
            apak_jniLogOnce(key, "native %s.%s is registered as %s, the runtime expected %s — not called",
                            className ? className : "?", name, registered, sig);
        else
            apak_jniLogOnce(key, "the game has no native %s.%s%s (neither registered nor exported) — skipped",
                            className ? className : "?", name, sig);
        return -1;
    }
    fprintf(stderr, JNI_TAG "-> native %s.%s%s\n", owner ? owner->name : className ? className : "?", name, sig);
    pushFrame(e);
    rc = ffiCall(fn, e, self ? (void *)self : (void *)c, types, nargs, args, rt, ret);
    lock();
    if (rt == 'L' && ret->l) retainLocked((ApakJObject *)ret->l);
    popFrameLocked(e);
    unlock();
    if (rc != 0) {
        apak_jniLogOnce(name, "native %s%s has more register arguments than AndroidRT passes — not called", name, sig);
        return -1;
    }
    return 0;
}
