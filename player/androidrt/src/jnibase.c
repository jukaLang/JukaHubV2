/* jnibase.c — the framework classes every JNI-using game reaches for,
 * implemented in C on top of jnienv.c's object model:
 *
 *   java.lang   Object Class ClassLoader String Throwable (+ the common
 *               exceptions) Integer Long Boolean Float Double Thread System
 *               Runtime; java.util.Locale; java.io.File; java.nio buffers
 *   android     Context/ContextWrapper/Activity/NativeActivity/Application,
 *               ApplicationInfo, AssetManager, Resources, Configuration,
 *               DisplayMetrics, WindowManager/Display/Window/View, Intent,
 *               Uri, Process, SystemClock, util.Log, KeyEvent, MotionEvent,
 *               InputDevice, Surface, Point/Rect
 *
 * Values describe the handheld truthfully (panel size, DPI, memory, locale,
 * the persistent data folder JukaHub passes as ANDROIDPAK_DATA). Services
 * the device does not have answer as absent (getSystemService → null for
 * unknown names, permissions for camera/microphone/location denied).
 * android.os.Build is deliberately NOT here: generic games see it as an
 * unknown class in the compatibility report.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#if !defined(_WIN32)
#include <dirent.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <sched.h>
#include <sys/statvfs.h>
#include <sys/syscall.h>
#include <unistd.h>
#endif

#include "actbridge.h"
#include "device.h"
#include "jni.h"

#define IMPL(name) static void name(ApakJCall *c)
#define ARG(i) ((ApakJObject *)c->args[i].l)
#define ARGS(i) apak_jniStringUtf(ARG(i))
#define RETS(s) (c->ret.l = apak_jniNewString(s))
#define RETO(o) (c->ret.l = (o))
#define RETI(v) (c->ret.i = (int32_t)(v))
#define RETJ(v) (c->ret.j = (int64_t)(v))
#define RETZ(v) (c->ret.z = (v) ? 1 : 0)
#define RETF(v) (c->ret.f = (float)(v))
#define RETD(v) (c->ret.d = (double)(v))

extern void ANativeActivity_finish(ANativeActivity *activity);
extern int __android_log_write(int prio, const char *tag, const char *text);

static const ApakJniConfig *cfg(void) { return apak_jniConfig(); }

/* Objects that live for the whole run (the loader, the asset manager ...). */
static ApakJObject *keep(ApakJObject **slot, const char *className) {
    if (!*slot) *slot = apak_jniRetain(apak_jniNewInstance(className));
    return *slot;
}

static void setInt(ApakJObject *o, const char *name, int32_t v) {
    ApakJValue x;
    x.j = 0;
    x.i = v;
    apak_jniFieldSet(o, name, x);
}
static void setFloat(ApakJObject *o, const char *name, float v) {
    ApakJValue x;
    x.j = 0;
    x.f = v;
    apak_jniFieldSet(o, name, x);
}
static void setStr(ApakJObject *o, const char *name, const char *s) {
    ApakJValue x;
    x.j = 0;
    x.l = apak_jniNewString(s);
    apak_jniFieldSet(o, name, x);
}
static int32_t getInt(ApakJObject *o, const char *name) {
    ApakJValue v;
    return apak_jniFieldGet(o, name, &v) == 0 ? v.i : 0;
}

/* ── device facts ──────────────────────────────────────────────────── */

int64_t apak_jniMemInfo(const char *key) {
#if !defined(_WIN32)
    FILE *f = fopen("/proc/meminfo", "r");
    char line[160];
    size_t klen = strlen(key);
    int64_t v = -1;
    if (!f) return -1;
    while (fgets(line, sizeof line, f)) {
        if (strncmp(line, key, klen) == 0 && line[klen] == ':') {
            v = strtoll(line + klen + 1, NULL, 10) * 1024;
            break;
        }
    }
    fclose(f);
    return v;
#else
    (void)key;
    return -1;
#endif
}

int64_t apak_jniProcessRss(void) {
#if !defined(_WIN32)
    FILE *f = fopen("/proc/self/statm", "r");
    long pages = 0, rss = 0;
    if (!f) return -1;
    if (fscanf(f, "%ld %ld", &pages, &rss) != 2) rss = -1;
    fclose(f);
    return rss < 0 ? -1 : (int64_t)rss * (int64_t)sysconf(_SC_PAGESIZE);
#else
    return -1;
#endif
}

void apak_jniLocale(char *out, size_t cap) {
    const char *v = getenv("LC_ALL");
    size_t n = 0;
    if (!v || !*v) v = getenv("LANG");
    if (!v || !*v || strcmp(v, "C") == 0 || strncmp(v, "C.", 2) == 0 || strcmp(v, "POSIX") == 0) v = "en_US";
    while (v[n] && v[n] != '.' && v[n] != '@' && n + 1 < cap) {
        out[n] = v[n] == '-' ? '_' : v[n];
        n++;
    }
    out[n] = '\0';
}

int apak_jniNetworkUp(void) {
#if !defined(_WIN32)
    struct ifaddrs *list, *i;
    int up = 0;
    const char *force = getenv("ANDROIDPAK_OFFLINE");
    if (force && strcmp(force, "0") != 0) return 0;
    if (getifaddrs(&list) != 0) return 0;
    for (i = list; i; i = i->ifa_next)
        if (i->ifa_addr && (i->ifa_flags & IFF_UP) && (i->ifa_flags & IFF_RUNNING) && !(i->ifa_flags & IFF_LOOPBACK) &&
            (i->ifa_addr->sa_family == 2 || i->ifa_addr->sa_family == 10))
            up = 1;
    freeifaddrs(list);
    return up;
#else
    return 0;
#endif
}

void apak_jniFinish(void) { ANativeActivity_finish(NULL); }

static int64_t monoMs(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static int cpuCount(void) {
#if !defined(_WIN32)
    long n = sysconf(_SC_NPROCESSORS_ONLN);
    return n > 0 ? (int)n : 1;
#else
    return 4;
#endif
}

static int mkdirs(const char *path) {
    char buf[1024];
    size_t i, len = strlen(path);
    if (!len || len >= sizeof buf) return -1;
    memcpy(buf, path, len + 1);
    for (i = 1; i <= len; i++) {
        if (buf[i] == '/' || buf[i] == '\0') {
            char saved = buf[i];
            buf[i] = '\0';
#if defined(_WIN32)
            mkdir(buf);
#else
            mkdir(buf, 0755);
#endif
            buf[i] = saved;
        }
    }
    return 0;
}

/* ── java.lang.Object / Class / ClassLoader ────────────────────────── */

static ApakJObject *g_loader;

static void dotted(const char *in, char *out, size_t cap) {
    size_t n = 0;
    while (in && *in && n + 1 < cap) {
        out[n++] = *in == '/' ? '.' : *in;
        in++;
    }
    out[n] = '\0';
}

IMPL(obj_init) { (void)c; }
IMPL(obj_getClass) { RETO((ApakJObject *)apak_jniClassOf(c->self)); }
IMPL(obj_hashCode) { RETI((int32_t)((uintptr_t)c->self >> 4)); }
IMPL(obj_equals) { RETZ(c->self == ARG(0)); }
IMPL(obj_toString) {
    char name[400], buf[512];
    dotted(apak_jniClassName(apak_jniClassOf(c->self)), name, sizeof name);
    snprintf(buf, sizeof buf, "%s@%x", name, (unsigned)((uintptr_t)c->self >> 4));
    RETS(buf);
}
IMPL(obj_noop) { (void)c; }
IMPL(obj_wait) {
    (void)c;
    apak_jniLogOnce("Object.wait", "Object.wait() — nothing can notify it here; returned at once");
}

IMPL(class_getName) {
    char buf[512];
    dotted(apak_jniClassName(apak_jniAsClass(c->self)), buf, sizeof buf);
    RETS(buf);
}
IMPL(class_getSimpleName) {
    const char *n = apak_jniClassName(apak_jniAsClass(c->self));
    const char *s = strrchr(n, '/'), *d;
    s = s ? s + 1 : n;
    d = strrchr(s, '$');
    RETS(d ? d + 1 : s);
}
IMPL(class_getClassLoader) { RETO(keep(&g_loader, "java/lang/ClassLoader")); }
static void loadClassImpl(ApakJCall *c, const char *name) {
    ApakJClass *k = name ? apak_jniLoadClass(name) : NULL;
    if (!k) {
        apak_jniThrow(c->env, "java/lang/ClassNotFoundException", name ? name : "(null)");
        return;
    }
    RETO((ApakJObject *)k);
}
IMPL(class_forName) { loadClassImpl(c, ARGS(0)); }
IMPL(class_isInstance) { RETZ(apak_jniIsInstance(ARG(0), apak_jniClassName(apak_jniAsClass(c->self)))); }
IMPL(class_isArray) { RETZ(apak_jniClassName(apak_jniAsClass(c->self))[0] == '['); }
IMPL(class_false) { RETZ(0); }
IMPL(class_getSuperclass) {
    ApakJClass *k = apak_jniAsClass(c->self);
    RETO(k ? (ApakJObject *)apak_jniSuperclass(k) : NULL);
}
IMPL(loader_loadClass) { loadClassImpl(c, ARGS(0)); }
IMPL(loader_system) { RETO(keep(&g_loader, "java/lang/ClassLoader")); }
IMPL(ret_null) { (void)c; }

/* ── java.lang.String ─────────────────────────────────────────────── */

/* UTF-16 → standard UTF-8 (String.getBytes: surrogate pairs become 4 bytes). */
static size_t toUtf8(const uint16_t *s, int32_t len, char *out) {
    size_t n = 0;
    int32_t i;
    for (i = 0; i < len; i++) {
        uint32_t ch = s[i];
        if (ch >= 0xd800 && ch < 0xdc00 && i + 1 < len && s[i + 1] >= 0xdc00 && s[i + 1] < 0xe000) {
            ch = 0x10000 + ((ch - 0xd800) << 10) + (s[i + 1] - 0xdc00);
            i++;
        }
        if (ch < 0x80) {
            if (out) out[n] = (char)ch;
            n++;
        } else if (ch < 0x800) {
            if (out) {
                out[n] = (char)(0xc0 | (ch >> 6));
                out[n + 1] = (char)(0x80 | (ch & 0x3f));
            }
            n += 2;
        } else if (ch < 0x10000) {
            if (out) {
                out[n] = (char)(0xe0 | (ch >> 12));
                out[n + 1] = (char)(0x80 | ((ch >> 6) & 0x3f));
                out[n + 2] = (char)(0x80 | (ch & 0x3f));
            }
            n += 3;
        } else {
            if (out) {
                out[n] = (char)(0xf0 | (ch >> 18));
                out[n + 1] = (char)(0x80 | ((ch >> 12) & 0x3f));
                out[n + 2] = (char)(0x80 | ((ch >> 6) & 0x3f));
                out[n + 3] = (char)(0x80 | (ch & 0x3f));
            }
            n += 4;
        }
    }
    return n;
}

static int isLatin1(const char *charset) {
    return charset && (strcasecmp(charset, "ISO-8859-1") == 0 || strcasecmp(charset, "US-ASCII") == 0 ||
                       strcasecmp(charset, "latin1") == 0 || strcasecmp(charset, "ASCII") == 0);
}

static void stringFromBytes(ApakJCall *c, ApakJObject *bytes, int32_t off, int32_t len, const char *charset) {
    int32_t total;
    char type;
    const char *data = (const char *)apak_jniArrayData(bytes, &total, &type);
    if (!data) {
        apak_jniThrow(c->env, "java/lang/NullPointerException", "new String(null)");
        return;
    }
    if (len < 0) len = total - off;
    if (off < 0 || len < 0 || off > total - len) {
        apak_jniThrow(c->env, "java/lang/StringIndexOutOfBoundsException", "new String(bytes, off, len)");
        return;
    }
    if (isLatin1(charset)) {
        uint16_t *u = (uint16_t *)malloc(((size_t)len + 1) * 2);
        int32_t i;
        if (!u) return;
        for (i = 0; i < len; i++) u[i] = (uint8_t)data[off + i];
        apak_jniStringSet(c->self, u, len);
        free(u);
    } else {
        apak_jniStringSetUtf8(c->self, data + off, (size_t)len);
    }
}

IMPL(str_initEmpty) { apak_jniStringSet(c->self, NULL, 0); }
IMPL(str_initStr) {
    ApakJObject *s = ARG(0);
    apak_jniStringSet(c->self, apak_jniStringChars(s), apak_jniStringLength(s));
}
IMPL(str_initBytes) { stringFromBytes(c, ARG(0), 0, -1, NULL); }
IMPL(str_initBytesCs) { stringFromBytes(c, ARG(0), 0, -1, ARGS(1)); }
IMPL(str_initBytesRange) { stringFromBytes(c, ARG(0), c->args[1].i, c->args[2].i, NULL); }
IMPL(str_initBytesRangeCs) { stringFromBytes(c, ARG(0), c->args[1].i, c->args[2].i, ARGS(3)); }
IMPL(str_initChars) {
    int32_t len;
    char t;
    const uint16_t *d = (const uint16_t *)apak_jniArrayData(ARG(0), &len, &t);
    apak_jniStringSet(c->self, d, d ? len : 0);
}
IMPL(str_initCharsRange) {
    int32_t len, off = c->args[1].i, n = c->args[2].i;
    char t;
    const uint16_t *d = (const uint16_t *)apak_jniArrayData(ARG(0), &len, &t);
    if (!d || off < 0 || n < 0 || off > len - n) {
        apak_jniThrow(c->env, "java/lang/StringIndexOutOfBoundsException", "new String(chars, off, len)");
        return;
    }
    apak_jniStringSet(c->self, d + off, n);
}
IMPL(str_length) { RETI(apak_jniStringLength(c->self)); }
IMPL(str_isEmpty) { RETZ(apak_jniStringLength(c->self) == 0); }
IMPL(str_charAt) {
    int32_t i = c->args[0].i;
    if (i < 0 || i >= apak_jniStringLength(c->self)) {
        apak_jniThrow(c->env, "java/lang/StringIndexOutOfBoundsException", "charAt");
        return;
    }
    c->ret.c = apak_jniStringChars(c->self)[i];
}
static void getBytesImpl(ApakJCall *c, const char *charset) {
    const uint16_t *s = apak_jniStringChars(c->self);
    int32_t len = apak_jniStringLength(c->self), i;
    char *buf;
    size_t n;
    if (isLatin1(charset)) {
        buf = (char *)malloc((size_t)len + 1);
        if (!buf) return;
        for (i = 0; i < len; i++) buf[i] = s[i] < 0x100 ? (char)s[i] : '?';
        n = (size_t)len;
    } else {
        n = toUtf8(s, len, NULL);
        buf = (char *)malloc(n + 1);
        if (!buf) return;
        toUtf8(s, len, buf);
    }
    RETO(apak_jniNewByteArray(buf, n));
    free(buf);
}
IMPL(str_getBytes) { getBytesImpl(c, NULL); }
IMPL(str_getBytesCs) { getBytesImpl(c, ARGS(0)); }
IMPL(str_equals) {
    ApakJObject *o = ARG(0);
    int32_t n = apak_jniStringLength(c->self);
    RETZ(o && apak_jniStringChars(o) && apak_jniStringLength(o) == n &&
         memcmp(apak_jniStringChars(o), apak_jniStringChars(c->self), (size_t)n * 2) == 0);
}
IMPL(str_hashCode) {
    const uint16_t *s = apak_jniStringChars(c->self);
    int32_t n = apak_jniStringLength(c->self), i;
    uint32_t h = 0;
    for (i = 0; i < n; i++) h = 31 * h + s[i];
    RETI((int32_t)h);
}
IMPL(str_self) { RETO(c->self); }
static void caseImpl(ApakJCall *c, int upper) {
    int32_t n = apak_jniStringLength(c->self), i;
    uint16_t *u = (uint16_t *)malloc(((size_t)n + 1) * 2);
    if (!u) return;
    memcpy(u, apak_jniStringChars(c->self), (size_t)n * 2);
    for (i = 0; i < n; i++) {
        if (upper && u[i] >= 'a' && u[i] <= 'z') u[i] = (uint16_t)(u[i] - 32);
        if (!upper && u[i] >= 'A' && u[i] <= 'Z') u[i] = (uint16_t)(u[i] + 32);
    }
    RETO(apak_jniNewStringUtf16(u, n));
    free(u);
}
IMPL(str_lower) { caseImpl(c, 0); }
IMPL(str_upper) { caseImpl(c, 1); }
IMPL(str_trim) {
    const uint16_t *s = apak_jniStringChars(c->self);
    int32_t a = 0, b = apak_jniStringLength(c->self);
    while (a < b && s[a] <= ' ') a++;
    while (b > a && s[b - 1] <= ' ') b--;
    RETO(apak_jniNewStringUtf16(s + a, b - a));
}
static int32_t indexOfImpl(ApakJObject *hay, ApakJObject *needle) {
    const uint16_t *h = apak_jniStringChars(hay), *n = apak_jniStringChars(needle);
    int32_t hl = apak_jniStringLength(hay), nl = apak_jniStringLength(needle), i;
    if (!h || !n) return -1;
    for (i = 0; i + nl <= hl; i++)
        if (memcmp(h + i, n, (size_t)nl * 2) == 0) return i;
    return -1;
}
IMPL(str_indexOf) { RETI(indexOfImpl(c->self, ARG(0))); }
IMPL(str_contains) { RETZ(indexOfImpl(c->self, ARG(0)) >= 0); }
IMPL(str_startsWith) {
    ApakJObject *p = ARG(0);
    int32_t pl = apak_jniStringLength(p);
    RETZ(p && pl <= apak_jniStringLength(c->self) &&
         memcmp(apak_jniStringChars(c->self), apak_jniStringChars(p), (size_t)pl * 2) == 0);
}
IMPL(str_endsWith) {
    ApakJObject *p = ARG(0);
    int32_t pl = apak_jniStringLength(p), sl = apak_jniStringLength(c->self);
    RETZ(p && pl <= sl && memcmp(apak_jniStringChars(c->self) + (sl - pl), apak_jniStringChars(p), (size_t)pl * 2) == 0);
}
static void substringImpl(ApakJCall *c, int32_t a, int32_t b) {
    int32_t n = apak_jniStringLength(c->self);
    if (a < 0 || b > n || a > b) {
        apak_jniThrow(c->env, "java/lang/StringIndexOutOfBoundsException", "substring");
        return;
    }
    RETO(apak_jniNewStringUtf16(apak_jniStringChars(c->self) + a, b - a));
}
IMPL(str_substring1) { substringImpl(c, c->args[0].i, apak_jniStringLength(c->self)); }
IMPL(str_substring2) { substringImpl(c, c->args[0].i, c->args[1].i); }
IMPL(str_valueOfI) {
    char b[32];
    snprintf(b, sizeof b, "%d", c->args[0].i);
    RETS(b);
}
IMPL(str_valueOfJ) {
    char b[32];
    snprintf(b, sizeof b, "%lld", (long long)c->args[0].j);
    RETS(b);
}
IMPL(str_valueOfF) {
    char b[48];
    snprintf(b, sizeof b, "%g", (double)c->args[0].f);
    RETS(b);
}
IMPL(str_valueOfD) {
    char b[48];
    snprintf(b, sizeof b, "%g", c->args[0].d);
    RETS(b);
}
IMPL(str_valueOfZ) { RETS(c->args[0].z ? "true" : "false"); }
IMPL(str_valueOfObj) {
    ApakJObject *o = ARG(0);
    ApakJValue r;
    if (!o) {
        RETS("null");
        return;
    }
    if (apak_jniStringUtf(o)) {
        RETO(o);
        return;
    }
    apak_jniCallMethod(c->env, o, "toString", "()Ljava/lang/String;", NULL, &r);
    RETO((ApakJObject *)r.l);
}

/* ── java.lang.Throwable ──────────────────────────────────────────── */

IMPL(thr_init) { (void)c; }
IMPL(thr_initMsg) {
    const char *m = ARGS(0);
    apak_jniSetPayload(c->self, m ? strdup(m) : NULL, free);
}
IMPL(thr_initMsgCause) {
    const char *m = ARGS(0);
    apak_jniSetPayload(c->self, m ? strdup(m) : NULL, free);
}
IMPL(thr_getMessage) {
    const char *m = (const char *)apak_jniPayload(c->self);
    if (m) RETS(m);
}
IMPL(thr_toString) {
    char name[300], buf[1024];
    const char *m = (const char *)apak_jniPayload(c->self);
    dotted(apak_jniClassName(apak_jniClassOf(c->self)), name, sizeof name);
    snprintf(buf, sizeof buf, "%s%s%s", name, m ? ": " : "", m ? m : "");
    RETS(buf);
}
IMPL(thr_print) {
    const char *m = (const char *)apak_jniPayload(c->self);
    fprintf(stderr, "[androidrt][jni] Throwable.printStackTrace: %s%s%s\n",
            apak_jniClassName(apak_jniClassOf(c->self)), m ? ": " : "", m ? m : "");
}
IMPL(thr_stack) { RETO(apak_jniNewArray('L', 0, "java/lang/StackTraceElement")); }

/* ── boxed values ─────────────────────────────────────────────────── */

static ApakJObject *box(const char *cls, ApakJValue v) {
    ApakJObject *o = apak_jniNewInstance(cls);
    if (o) apak_jniFieldSet(o, "value", v);
    return o;
}
static ApakJValue unbox(ApakJObject *o) {
    ApakJValue v;
    v.j = 0;
    if (o) apak_jniFieldGet(o, "value", &v);
    return v;
}

IMPL(int_valueOf) { RETO(box("java/lang/Integer", c->args[0])); }
IMPL(int_init) { apak_jniFieldSet(c->self, "value", c->args[0]); }
IMPL(int_intValue) { RETI(unbox(c->self).i); }
IMPL(int_longValue) { RETJ(unbox(c->self).i); }
IMPL(int_floatValue) { RETF(unbox(c->self).i); }
IMPL(int_doubleValue) { RETD(unbox(c->self).i); }
static int parseLongStrict(const char *s, int radix, long long *out) {
    char *end;
    if (!s || !*s) return -1;
    errno = 0;
    *out = strtoll(s, &end, radix);
    return (*end || errno) ? -1 : 0;
}
static void parseIntImpl(ApakJCall *c, int radix, int wide) {
    long long v;
    const char *s = ARGS(0);
    if (parseLongStrict(s, radix, &v) != 0 || (!wide && (v < INT32_MIN || v > INT32_MAX))) {
        char msg[160];
        snprintf(msg, sizeof msg, "For input string: \"%.120s\"", s ? s : "null");
        apak_jniThrow(c->env, "java/lang/NumberFormatException", msg);
        return;
    }
    if (wide) RETJ(v);
    else RETI(v);
}
IMPL(int_parse) { parseIntImpl(c, 10, 0); }
IMPL(int_parseRadix) { parseIntImpl(c, c->args[1].i, 0); }
IMPL(int_toString) {
    char b[32];
    snprintf(b, sizeof b, "%d", unbox(c->self).i);
    RETS(b);
}
IMPL(long_valueOf) { RETO(box("java/lang/Long", c->args[0])); }
IMPL(long_longValue) { RETJ(unbox(c->self).j); }
IMPL(long_intValue) { RETI((int32_t)unbox(c->self).j); }
IMPL(long_parse) { parseIntImpl(c, 10, 1); }
IMPL(bool_valueOf) { RETO(box("java/lang/Boolean", c->args[0])); }
IMPL(bool_value) { RETZ(unbox(c->self).z); }
IMPL(bool_parse) {
    const char *s = ARGS(0);
    RETZ(s && strcasecmp(s, "true") == 0);
}
IMPL(float_valueOf) { RETO(box("java/lang/Float", c->args[0])); }
IMPL(float_value) { RETF(unbox(c->self).f); }
IMPL(float_parse) {
    const char *s = ARGS(0);
    char *end;
    double v = s ? strtod(s, &end) : 0;
    if (!s || !*s || *end) {
        apak_jniThrow(c->env, "java/lang/NumberFormatException", s ? s : "null");
        return;
    }
    RETF(v);
}
IMPL(double_valueOf) { RETO(box("java/lang/Double", c->args[0])); }
IMPL(double_value) { RETD(unbox(c->self).d); }
IMPL(double_parse) {
    const char *s = ARGS(0);
    char *end;
    double v = s ? strtod(s, &end) : 0;
    if (!s || !*s || *end) {
        apak_jniThrow(c->env, "java/lang/NumberFormatException", s ? s : "null");
        return;
    }
    RETD(v);
}

/* ── Thread / System / Runtime ─────────────────────────────────────── */

IMPL(thread_current) { RETO(apak_jniNewInstance("java/lang/Thread")); }
IMPL(thread_getName) { RETS(c->env == apak_jniMainEnv() ? "main" : "Thread-native"); }
IMPL(thread_getId) { RETJ(c->env == apak_jniMainEnv() ? 1 : 2); }
IMPL(thread_setName) {
#if defined(__linux__)
    const char *n = ARGS(0);
    char buf[16];
    if (n) {
        snprintf(buf, sizeof buf, "%s", n);
        pthread_setname_np(pthread_self(), buf);
    }
#else
    (void)c;
#endif
}
IMPL(thread_sleep) {
    struct timespec ts;
    int64_t ms = c->args[0].j;
    if (ms <= 0) return;
    ts.tv_sec = (time_t)(ms / 1000);
    ts.tv_nsec = (long)(ms % 1000) * 1000000L;
    nanosleep(&ts, NULL);
}
IMPL(thread_yield) {
    (void)c;
#if !defined(_WIN32)
    sched_yield();
#endif
}

IMPL(sys_millis) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    RETJ((int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000);
}
IMPL(sys_nanos) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    RETJ((int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec);
}
static const char *sysProp(const char *k, char *buf, size_t cap) {
    char loc[32];
    if (!k) return NULL;
    if (strcmp(k, "os.arch") == 0) {
#if defined(__aarch64__)
        return "aarch64";
#elif defined(__x86_64__)
        return "x86_64";
#else
        return "unknown";
#endif
    }
    if (strcmp(k, "os.name") == 0) return "Linux";
    if (strcmp(k, "java.vm.name") == 0) return "Dalvik";
    if (strcmp(k, "java.vm.version") == 0) return "2.1.0";
    if (strcmp(k, "java.vendor") == 0) return "The Android Project";
    if (strcmp(k, "java.version") == 0) return "0";
    if (strcmp(k, "line.separator") == 0) return "\n";
    if (strcmp(k, "file.separator") == 0) return "/";
    if (strcmp(k, "path.separator") == 0) return ":";
    if (strcmp(k, "user.home") == 0) return cfg()->filesDir;
    if (strcmp(k, "java.io.tmpdir") == 0) return cfg()->cacheDir;
    if (strcmp(k, "user.dir") == 0) return "/";
    if (strcmp(k, "file.encoding") == 0) return "UTF-8";
    if (strcmp(k, "http.agent") == 0) {
        snprintf(buf, cap, "Dalvik/2.1.0 (Linux; U; Android 9; %s Build/JUKAHUB)", cfg()->model);
        return buf;
    }
    if (strcmp(k, "user.language") == 0 || strcmp(k, "user.region") == 0) {
        char *u;
        apak_jniLocale(loc, sizeof loc);
        u = strchr(loc, '_');
        if (k[5] == 'l') {
            if (u) *u = '\0';
            snprintf(buf, cap, "%s", loc);
        } else {
            snprintf(buf, cap, "%s", u ? u + 1 : "");
        }
        return buf;
    }
    return NULL;
}
IMPL(sys_getProperty) {
    char buf[256];
    const char *v = sysProp(ARGS(0), buf, sizeof buf);
    if (v) RETS(v);
}
IMPL(sys_getPropertyDefault) {
    char buf[256];
    const char *v = sysProp(ARGS(0), buf, sizeof buf);
    if (v) RETS(v);
    else RETO(ARG(1));
}
IMPL(sys_loadLibrary) {
    const char *n = ARGS(0);
    char key[160];
    snprintf(key, sizeof key, "loadLibrary %s", n ? n : "");
    apak_jniLogOnce(key, "System.loadLibrary(\"%s\") — the runtime loads the game's libraries itself", n ? n : "");
}
IMPL(sys_exit) {
    fprintf(stderr, "[androidrt][jni] System.exit(%d) requested by the game\n", c->args[0].i);
    apak_jniFinish();
}
IMPL(sys_identityHash) { RETI((int32_t)((uintptr_t)ARG(0) >> 4)); }
IMPL(sys_arraycopy) {
    ApakJObject *src = ARG(0), *dst = ARG(2);
    int32_t sp = c->args[1].i, dp = c->args[3].i, n = c->args[4].i, sl, dl, i;
    char st, dt;
    char *s = (char *)apak_jniArrayData(src, &sl, &st), *d = (char *)apak_jniArrayData(dst, &dl, &dt);
    if (!s || !d) {
        apak_jniThrow(c->env, "java/lang/NullPointerException", "arraycopy");
        return;
    }
    if (st != dt || n < 0 || sp < 0 || dp < 0 || sp > sl - n || dp > dl - n) {
        apak_jniThrow(c->env, "java/lang/ArrayIndexOutOfBoundsException", "arraycopy");
        return;
    }
    if (st == 'L') {
        ApakJObject **so = (ApakJObject **)s;
        if (src == dst && sp < dp)
            for (i = n - 1; i >= 0; i--) apak_jniArraySet(dst, dp + i, so[sp + i]);
        else
            for (i = 0; i < n; i++) apak_jniArraySet(dst, dp + i, so[sp + i]);
    } else {
        size_t es = st == 'Z' || st == 'B' ? 1 : st == 'C' || st == 'S' ? 2 : st == 'I' || st == 'F' ? 4 : 8;
        memmove(d + (size_t)dp * es, s + (size_t)sp * es, (size_t)n * es);
    }
}

static ApakJObject *g_runtime;
IMPL(rt_get) { RETO(keep(&g_runtime, "java/lang/Runtime")); }
IMPL(rt_cpus) { RETI(cpuCount()); }
#define HEAP_LIMIT (256LL << 20) /* dalvik.vm.heapsize in sysprops.c */
IMPL(rt_max) { RETJ(HEAP_LIMIT); }
IMPL(rt_total) {
    int64_t rss = apak_jniProcessRss();
    RETJ(rss > 0 && rss < HEAP_LIMIT ? rss : HEAP_LIMIT);
}
IMPL(rt_free) {
    int64_t rss = apak_jniProcessRss();
    RETJ(rss > 0 && rss < HEAP_LIMIT ? HEAP_LIMIT - rss : 0);
}

/* ── java.util.Locale ─────────────────────────────────────────────── */

static ApakJObject *newLocale(const char *tag) {
    ApakJObject *o = apak_jniNewInstance("java/util/Locale");
    if (o) apak_jniSetPayload(o, strdup(tag ? tag : "en_US"), free);
    return o;
}
IMPL(loc_default) {
    char loc[32];
    apak_jniLocale(loc, sizeof loc);
    RETO(newLocale(loc));
}
IMPL(loc_init1) { apak_jniSetPayload(c->self, strdup(ARGS(0) ? ARGS(0) : ""), free); }
IMPL(loc_init2) {
    char buf[64];
    snprintf(buf, sizeof buf, "%s_%s", ARGS(0) ? ARGS(0) : "", ARGS(1) ? ARGS(1) : "");
    apak_jniSetPayload(c->self, strdup(buf), free);
}
static void localePart(ApakJCall *c, int country, char sep) {
    const char *tag = (const char *)apak_jniPayload(c->self);
    char buf[64];
    const char *u;
    if (!tag) tag = "en_US";
    u = strchr(tag, '_');
    if (country == 2) {
        snprintf(buf, sizeof buf, "%s", tag);
        if (sep != '_' && strchr(buf, '_')) *strchr(buf, '_') = sep;
    } else if (country) {
        snprintf(buf, sizeof buf, "%s", u ? u + 1 : "");
    } else {
        snprintf(buf, sizeof buf, "%.*s", u ? (int)(u - tag) : (int)strlen(tag), tag);
    }
    RETS(buf);
}
IMPL(loc_language) { localePart(c, 0, '_'); }
IMPL(loc_country) { localePart(c, 1, '_'); }
IMPL(loc_toString) { localePart(c, 2, '_'); }
IMPL(loc_tag) { localePart(c, 2, '-'); }

/* ── java.io.File ─────────────────────────────────────────────────── */

static ApakJObject *newFile(const char *path) {
    ApakJObject *o = apak_jniNewInstance("java/io/File");
    if (o) apak_jniSetPayload(o, strdup(path ? path : ""), free);
    return o;
}
static const char *filePath(ApakJObject *o) {
    const char *p = (const char *)apak_jniPayload(o);
    return p ? p : "";
}
IMPL(file_init) { apak_jniSetPayload(c->self, strdup(ARGS(0) ? ARGS(0) : ""), free); }
IMPL(file_initParent) {
    char buf[2048];
    snprintf(buf, sizeof buf, "%s/%s", ARG(0) ? filePath(ARG(0)) : "", ARGS(1) ? ARGS(1) : "");
    apak_jniSetPayload(c->self, strdup(buf), free);
}
IMPL(file_initParentStr) {
    char buf[2048];
    snprintf(buf, sizeof buf, "%s/%s", ARGS(0) ? ARGS(0) : "", ARGS(1) ? ARGS(1) : "");
    apak_jniSetPayload(c->self, strdup(buf), free);
}
IMPL(file_path) { RETS(filePath(c->self)); }
IMPL(file_name) {
    const char *p = filePath(c->self), *s = strrchr(p, '/');
    RETS(s ? s + 1 : p);
}
IMPL(file_parent) {
    const char *p = filePath(c->self), *s = strrchr(p, '/');
    char buf[2048];
    if (!s) return;
    snprintf(buf, sizeof buf, "%.*s", (int)(s - p), p);
    RETS(buf[0] ? buf : "/");
}
IMPL(file_parentFile) {
    const char *p = filePath(c->self), *s = strrchr(p, '/');
    char buf[2048];
    if (!s) return;
    snprintf(buf, sizeof buf, "%.*s", (int)(s - p), p);
    RETO(newFile(buf[0] ? buf : "/"));
}
IMPL(file_exists) {
    struct stat st;
    RETZ(stat(filePath(c->self), &st) == 0);
}
IMPL(file_isDir) {
    struct stat st;
    RETZ(stat(filePath(c->self), &st) == 0 && S_ISDIR(st.st_mode));
}
IMPL(file_isFile) {
    struct stat st;
    RETZ(stat(filePath(c->self), &st) == 0 && S_ISREG(st.st_mode));
}
IMPL(file_length) {
    struct stat st;
    RETJ(stat(filePath(c->self), &st) == 0 ? (int64_t)st.st_size : 0);
}
IMPL(file_mkdirs) {
    struct stat st;
    mkdirs(filePath(c->self));
    RETZ(stat(filePath(c->self), &st) == 0 && S_ISDIR(st.st_mode));
}
IMPL(file_delete) { RETZ(remove(filePath(c->self)) == 0); }
IMPL(file_list) {
#if !defined(_WIN32)
    DIR *d = opendir(filePath(c->self));
    struct dirent *e;
    const char *names[512];
    size_t n = 0;
    if (!d) return;
    while ((e = readdir(d)) != NULL && n < 512) {
        if (strcmp(e->d_name, ".") == 0 || strcmp(e->d_name, "..") == 0) continue;
        names[n++] = strdup(e->d_name);
    }
    closedir(d);
    RETO(apak_jniNewStringArray(names, n));
    while (n) free((void *)names[--n]);
#else
    (void)c;
#endif
}
static void spaceImpl(ApakJCall *c, int which) {
#if !defined(_WIN32)
    struct statvfs v;
    if (statvfs(filePath(c->self), &v) != 0) return;
    if (which == 0) RETJ((int64_t)v.f_blocks * (int64_t)v.f_frsize);
    else if (which == 1) RETJ((int64_t)v.f_bfree * (int64_t)v.f_frsize);
    else RETJ((int64_t)v.f_bavail * (int64_t)v.f_frsize);
#else
    (void)c;
    (void)which;
#endif
}
IMPL(file_total) { spaceImpl(c, 0); }
IMPL(file_free) { spaceImpl(c, 1); }
IMPL(file_usable) { spaceImpl(c, 2); }
IMPL(buf_capacity) {
    (void)c; /* GetDirectBufferCapacity is the JNI path; Java-side capacity is rare */
}

/* ── android.content ──────────────────────────────────────────────── */

static ApakJObject *g_assets, *g_resources, *g_application, *g_window, *g_view, *g_wm, *g_display, *g_intent,
    *g_activityManager, *g_audioManager;

static ApakJObject *assetsObject(void) {
    ApakJObject *o = keep(&g_assets, "android/content/res/AssetManager");
    if (o && !apak_jniPayload(o)) apak_jniSetPayload(o, cfg()->assetManager, NULL);
    return o;
}

static ApakJObject *dirFile(const char *path, const char *sub) {
    char buf[2048];
    if (sub && *sub) snprintf(buf, sizeof buf, "%s/%s", path, sub);
    else snprintf(buf, sizeof buf, "%s", path);
    mkdirs(buf);
    return newFile(buf);
}

IMPL(ctx_filesDir) { RETO(dirFile(cfg()->filesDir, NULL)); }
IMPL(ctx_cacheDir) { RETO(dirFile(cfg()->cacheDir, NULL)); }
IMPL(ctx_dataDir) { RETO(dirFile(cfg()->dataRoot, NULL)); }
IMPL(ctx_obbDir) { RETO(dirFile(cfg()->obbDir, NULL)); }
IMPL(ctx_externalFilesDir) { RETO(dirFile(cfg()->externalDir, ARGS(0))); }
IMPL(ctx_externalFilesDirs) {
    ApakJObject *a = apak_jniNewArray('L', 1, "java/io/File");
    apak_jniArraySet(a, 0, dirFile(cfg()->externalDir, ARGS(0)));
    RETO(a);
}
IMPL(ctx_obbDirs) {
    ApakJObject *a = apak_jniNewArray('L', 1, "java/io/File");
    apak_jniArraySet(a, 0, dirFile(cfg()->obbDir, NULL));
    RETO(a);
}
IMPL(ctx_externalCacheDir) { RETO(dirFile(cfg()->externalDir, "cache")); }
IMPL(ctx_getDir) {
    char sub[256];
    snprintf(sub, sizeof sub, "app_%s", ARGS(0) ? ARGS(0) : "dir");
    RETO(dirFile(cfg()->dataRoot, sub));
}
IMPL(ctx_databasePath) {
    char buf[2048];
    snprintf(buf, sizeof buf, "%s/databases", cfg()->dataRoot);
    mkdirs(buf);
    snprintf(buf, sizeof buf, "%s/databases/%s", cfg()->dataRoot, ARGS(0) ? ARGS(0) : "db");
    RETO(newFile(buf));
}
IMPL(ctx_packageName) { RETS(cfg()->packageName); }
IMPL(ctx_packageCodePath) { RETS(cfg()->apkPath); }
IMPL(ctx_appContext) { RETO(apak_jniActivity()); }
IMPL(ctx_application) { RETO(keep(&g_application, "android/app/Application")); }
IMPL(ctx_assets) { RETO(assetsObject()); }
IMPL(ctx_resources) { RETO(keep(&g_resources, "android/content/res/Resources")); }
IMPL(ctx_classLoader) { RETO(keep(&g_loader, "java/lang/ClassLoader")); }
IMPL(ctx_appInfo) {
    ApakJObject *o = apak_jniNewInstance("android/content/pm/ApplicationInfo");
    if (!o) return;
    setStr(o, "sourceDir", cfg()->apkPath);
    setStr(o, "publicSourceDir", cfg()->apkPath);
    setStr(o, "nativeLibraryDir", cfg()->libDir);
    setStr(o, "dataDir", cfg()->dataRoot);
    setStr(o, "packageName", cfg()->packageName);
    setInt(o, "targetSdkVersion", cfg()->sdkVersion);
    setInt(o, "minSdkVersion", 17);
    RETO(o);
}
IMPL(ctx_systemService) {
    const char *n = ARGS(0);
    char key[128];
    if (!n) return;
    if (strcmp(n, "window") == 0) {
        RETO(keep(&g_wm, "android/view/WindowManager"));
    } else if (strcmp(n, "activity") == 0) {
        RETO(keep(&g_activityManager, "android/app/ActivityManager"));
    } else if (strcmp(n, "audio") == 0) {
        RETO(keep(&g_audioManager, "android/media/AudioManager"));
    } else {
        snprintf(key, sizeof key, "service %s", n);
        apak_jniLogOnce(key, "getSystemService(\"%s\") -> null: the handheld has no such Android service", n);
    }
}
IMPL(ctx_checkPermission) {
    const char *p = ARGS(0);
    /* storage and network are what the runtime really gives every game */
    int granted = p && (strstr(p, "STORAGE") || strstr(p, "INTERNET") || strstr(p, "NETWORK_STATE") ||
                        strstr(p, "WIFI_STATE") || strstr(p, "WAKE_LOCK") || strstr(p, "VIBRATE"));
    RETI(granted ? 0 : -1);
}
IMPL(ctx_startActivity) {
    (void)c;
    apak_jniLogOnce("startActivity", "startActivity — there are no other Android apps on the handheld; ignored");
}

/* Activity */
IMPL(act_window) { RETO(keep(&g_window, "android/view/Window")); }
IMPL(act_windowManager) { RETO(keep(&g_wm, "android/view/WindowManager")); }
IMPL(act_finish) {
    (void)c;
    fprintf(stderr, "[androidrt][jni] Activity.finish() called by the game\n");
    apak_jniFinish();
}
IMPL(act_intent) { RETO(keep(&g_intent, "android/content/Intent")); }
IMPL(act_orientation) { RETI(0); /* SCREEN_ORIENTATION_LANDSCAPE */ }
IMPL(act_true) { RETZ(1); }
IMPL(act_false) { RETZ(0); }
static void runRunnable(void *arg) {
    ApakJObject *r = (ApakJObject *)arg;
    apak_jniCallMethod(apak_jniMainEnv(), r, "run", "()V", NULL, NULL);
    apak_jniRelease(r);
}
IMPL(act_runOnUiThread) {
    ApakJObject *r = ARG(0);
    if (r) apak_jniPost(runRunnable, apak_jniRetain(r));
}

/* ActivityManager / AudioManager */
IMPL(am_memoryInfo) {
    ApakJObject *mi = ARG(0);
    ApakJValue v;
    int64_t avail = apak_jniMemInfo("MemAvailable"), total = apak_jniMemInfo("MemTotal");
    if (!mi) return;
    v.j = avail > 0 ? avail : 0;
    apak_jniFieldSet(mi, "availMem", v);
    v.j = total > 0 ? total : 0;
    apak_jniFieldSet(mi, "totalMem", v);
    v.j = total > 0 ? total / 16 : 0;
    apak_jniFieldSet(mi, "threshold", v);
    v.j = 0;
    v.z = avail > 0 && total > 0 && avail < total / 16;
    apak_jniFieldSet(mi, "lowMemory", v);
}
IMPL(am_memoryClass) { RETI(128); /* dalvik.vm.heapgrowthlimit */ }
IMPL(am_largeMemoryClass) { RETI(256); /* dalvik.vm.heapsize */ }
IMPL(am_isLowRam) { RETZ(apak_jniMemInfo("MemTotal") > 0 && apak_jniMemInfo("MemTotal") <= (1LL << 30)); }
IMPL(audio_property) {
    const char *k = ARGS(0);
    if (!k) return;
    if (strstr(k, "OUTPUT_SAMPLE_RATE")) RETS("48000");
    else if (strstr(k, "OUTPUT_FRAMES_PER_BUFFER")) RETS("256");
}
IMPL(audio_volume) { RETI(10); }
IMPL(audio_maxVolume) { RETI(15); }

/* ── resources, configuration, display ────────────────────────────── */

static void fillMetrics(ApakJObject *m) {
    const ApakJniConfig *k = cfg();
    int bucket = apak_deviceDensity(); /* logical, like AConfiguration */
    if (!m) return;
    setInt(m, "widthPixels", k->panelW);
    setInt(m, "heightPixels", k->panelH);
    setInt(m, "densityDpi", bucket);
    setFloat(m, "density", (float)bucket / 160.0f);
    setFloat(m, "scaledDensity", (float)bucket / 160.0f);
    setFloat(m, "xdpi", k->dpi);
    setFloat(m, "ydpi", k->dpi);
}

static void fillConfiguration(ApakJObject *o) {
    const ApakJniConfig *k = cfg();
    int bucket = apak_deviceDensity(); /* logical, like AConfiguration */
    ApakJValue v;
    char loc[32];
    if (!o) return;
    setInt(o, "orientation", k->panelW >= k->panelH ? 2 : 1);
    setInt(o, "touchscreen", 1);         /* TOUCHSCREEN_NOTOUCH */
    setInt(o, "keyboard", 1);            /* KEYBOARD_NOKEYS */
    setInt(o, "keyboardHidden", 2);      /* KEYBOARDHIDDEN_YES */
    setInt(o, "hardKeyboardHidden", 2);
    setInt(o, "navigation", 2);          /* NAVIGATION_DPAD */
    setInt(o, "navigationHidden", 1);    /* NAVIGATIONHIDDEN_NO */
    setInt(o, "screenLayout", 0x12);     /* SIZE_NORMAL | LONG_YES */
    setInt(o, "uiMode", 0x11);           /* TYPE_NORMAL | NIGHT_NO */
    setInt(o, "densityDpi", bucket);
    setInt(o, "screenWidthDp", k->panelW * 160 / bucket);
    setInt(o, "screenHeightDp", k->panelH * 160 / bucket);
    setInt(o, "smallestScreenWidthDp", (k->panelW < k->panelH ? k->panelW : k->panelH) * 160 / bucket);
    setFloat(o, "fontScale", 1.0f);
    apak_jniLocale(loc, sizeof loc);
    v.j = 0;
    v.l = newLocale(loc);
    apak_jniFieldSet(o, "locale", v);
}

IMPL(res_configuration) {
    ApakJObject *o = apak_jniNewInstance("android/content/res/Configuration");
    fillConfiguration(o);
    RETO(o);
}
IMPL(res_metrics) {
    ApakJObject *o = apak_jniNewInstance("android/util/DisplayMetrics");
    fillMetrics(o);
    RETO(o);
}
IMPL(res_identifier) { RETI(0); }
IMPL(cfg_init) { fillConfiguration(c->self); }
IMPL(dm_init) { (void)c; }
IMPL(dm_setToDefaults) { fillMetrics(c->self); }
IMPL(wm_display) { RETO(keep(&g_display, "android/view/Display")); }
IMPL(disp_width) { RETI(cfg()->panelW); }
IMPL(disp_height) { RETI(cfg()->panelH); }
IMPL(disp_zero) { RETI(0); }
IMPL(disp_refresh) { RETF(60.0f); }
IMPL(disp_metrics) { fillMetrics(ARG(0)); }
IMPL(disp_size) {
    ApakJObject *p = ARG(0);
    if (!p) return;
    setInt(p, "x", cfg()->panelW);
    setInt(p, "y", cfg()->panelH);
}
IMPL(disp_name) { RETS("Built-in Screen"); }
IMPL(disp_rates) {
    ApakJObject *a = apak_jniNewArray('F', 1, NULL);
    float *d = (float *)apak_jniArrayData(a, NULL, NULL);
    if (d) d[0] = 60.0f;
    RETO(a);
}
IMPL(point_init2) {
    setInt(c->self, "x", c->args[0].i);
    setInt(c->self, "y", c->args[1].i);
}
IMPL(rect_init4) {
    setInt(c->self, "left", c->args[0].i);
    setInt(c->self, "top", c->args[1].i);
    setInt(c->self, "right", c->args[2].i);
    setInt(c->self, "bottom", c->args[3].i);
}
IMPL(rect_width) { RETI(getInt(c->self, "right") - getInt(c->self, "left")); }
IMPL(rect_height) { RETI(getInt(c->self, "bottom") - getInt(c->self, "top")); }
IMPL(window_view) { RETO(keep(&g_view, "android/view/View")); }
IMPL(view_width) { RETI(cfg()->panelW); }
IMPL(view_height) { RETI(cfg()->panelH); }
IMPL(view_self) { RETO(c->self); }
IMPL(view_context) { RETO(apak_jniActivity()); }
IMPL(view_post) {
    ApakJObject *r = ARG(0);
    if (r) apak_jniPost(runRunnable, apak_jniRetain(r));
    RETZ(r != NULL);
}
IMPL(intent_action) { RETS("android.intent.action.MAIN"); }
IMPL(intent_intDefault) { RETI(c->args[1].i); }
IMPL(intent_boolDefault) { RETZ(c->args[1].z); }
IMPL(intent_longDefault) { RETJ(c->args[1].j); }
IMPL(uri_parse) {
    ApakJObject *o = apak_jniNewInstance("android/net/Uri");
    if (o) apak_jniSetPayload(o, strdup(ARGS(0) ? ARGS(0) : ""), free);
    RETO(o);
}
IMPL(uri_toString) {
    const char *s = (const char *)apak_jniPayload(c->self);
    RETS(s ? s : "");
}
IMPL(assets_list) {
    const char *dir = ARGS(0);
    char **names = NULL;
    size_t n = android_assetList(dir ? dir : "", &names);
    RETO(apak_jniNewStringArray((const char *const *)names, n));
    android_assetListFree(names, n);
}

/* ── android.os / android.util ────────────────────────────────────── */

IMPL(proc_pid) {
#if !defined(_WIN32)
    RETI(getpid());
#else
    RETI(1);
#endif
}
IMPL(proc_tid) {
#if defined(__linux__)
    RETI(syscall(SYS_gettid));
#else
    RETI(1);
#endif
}
IMPL(clock_uptime) { RETJ(monoMs()); }
IMPL(clock_elapsed) {
    struct timespec ts;
#if defined(CLOCK_BOOTTIME)
    clock_gettime(CLOCK_BOOTTIME, &ts);
#else
    clock_gettime(CLOCK_MONOTONIC, &ts);
#endif
    RETJ((int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000);
}
IMPL(clock_elapsedNanos) {
    struct timespec ts;
#if defined(CLOCK_BOOTTIME)
    clock_gettime(CLOCK_BOOTTIME, &ts);
#else
    clock_gettime(CLOCK_MONOTONIC, &ts);
#endif
    RETJ((int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec);
}
static void logImpl(ApakJCall *c, int prio) {
    __android_log_write(prio, ARGS(0) ? ARGS(0) : "java", ARGS(1) ? ARGS(1) : "");
    RETI(0);
}
IMPL(log_v) { logImpl(c, 2); }
IMPL(log_d) { logImpl(c, 3); }
IMPL(log_i) { logImpl(c, 4); }
IMPL(log_w) { logImpl(c, 5); }
IMPL(log_e) { logImpl(c, 6); }

/* ── input events ─────────────────────────────────────────────────── */

ApakJObject *apak_jniNewKeyEvent(const ApakKeyEventData *d) {
    ApakJObject *o = apak_jniNewInstance("android/view/KeyEvent");
    ApakKeyEventData *copy;
    if (!o) return NULL;
    copy = (ApakKeyEventData *)malloc(sizeof *copy);
    if (copy) *copy = *d;
    apak_jniSetPayload(o, copy, free);
    return o;
}

ApakJObject *apak_jniNewMotionEvent(const ApakMotionEventData *d) {
    ApakJObject *o = apak_jniNewInstance("android/view/MotionEvent");
    ApakMotionEventData *copy;
    if (!o) return NULL;
    copy = (ApakMotionEventData *)malloc(sizeof *copy);
    if (copy) *copy = *d;
    apak_jniSetPayload(o, copy, free);
    return o;
}

static const ApakKeyEventData *keyData(ApakJCall *c) {
    static const ApakKeyEventData none;
    const ApakKeyEventData *d = (const ApakKeyEventData *)apak_jniPayload(c->self);
    return d ? d : &none;
}
IMPL(key_init) {
    ApakKeyEventData *d = (ApakKeyEventData *)calloc(1, sizeof *d);
    if (!d) return;
    d->action = c->args[0].i;
    d->keyCode = c->args[1].i;
    d->eventTimeNs = d->downTimeNs = monoMs() * 1000000LL;
    apak_jniSetPayload(c->self, d, free);
}
IMPL(key_action) { RETI(keyData(c)->action); }
IMPL(key_code) { RETI(keyData(c)->keyCode); }
IMPL(key_repeat) { RETI(keyData(c)->repeatCount); }
IMPL(key_meta) { RETI(keyData(c)->metaState); }
IMPL(key_flags) { RETI(keyData(c)->flags); }
IMPL(key_device) { RETI(keyData(c)->deviceId); }
IMPL(key_source) { RETI(keyData(c)->source); }
IMPL(key_scan) { RETI(keyData(c)->scanCode); }
IMPL(key_eventTime) { RETJ(keyData(c)->eventTimeNs / 1000000); }
IMPL(key_downTime) { RETJ(keyData(c)->downTimeNs / 1000000); }
IMPL(key_unicode) { RETI(0); }
IMPL(key_false) { RETZ(0); }

static const ApakMotionEventData *motionData(ApakJCall *c) {
    static const ApakMotionEventData none;
    const ApakMotionEventData *d = (const ApakMotionEventData *)apak_jniPayload(c->self);
    return d ? d : &none;
}
static float axisOf(ApakJCall *c, int32_t axis) {
    return axis >= 0 && axis < APAK_MOTION_AXES ? motionData(c)->axes[axis] : 0.0f;
}
IMPL(mot_action) { RETI(motionData(c)->action); }
IMPL(mot_actionMasked) { RETI(motionData(c)->action & 0xff); }
IMPL(mot_actionIndex) { RETI((motionData(c)->action >> 8) & 0xff); }
IMPL(mot_one) { RETI(1); }
IMPL(mot_zeroI) { RETI(0); }
IMPL(mot_zeroF) { RETF(0.0f); }
IMPL(mot_oneF) { RETF(1.0f); }
IMPL(mot_pointerId) { RETI(0); }
IMPL(mot_toolType) { RETI(0); /* TOOL_TYPE_UNKNOWN: a stick, not a finger */ }
IMPL(mot_x) { RETF(axisOf(c, 0)); }
IMPL(mot_y) { RETF(axisOf(c, 1)); }
IMPL(mot_axis1) { RETF(axisOf(c, c->args[0].i)); }
IMPL(mot_axis2) { RETF(c->args[1].i == 0 ? axisOf(c, c->args[0].i) : 0.0f); }
IMPL(mot_device) { RETI(motionData(c)->deviceId); }
IMPL(mot_source) { RETI(motionData(c)->source); }
IMPL(mot_eventTime) { RETJ(motionData(c)->eventTimeNs / 1000000); }
IMPL(mot_downTime) { RETJ(motionData(c)->downTimeNs / 1000000); }
IMPL(mot_flags) { RETI(motionData(c)->flags); }
IMPL(mot_meta) { RETI(motionData(c)->metaState); }
IMPL(mot_buttons) { RETI(motionData(c)->buttonState); }

/* InputDevice: every evdev node the runtime reads is a gamepad */
#define SRC_GAMEPAD_ALL 0x01000611 /* SOURCE_GAMEPAD | SOURCE_JOYSTICK | SOURCE_DPAD */
static ApakJObject *newDevice(int32_t id) {
    ApakJObject *o = apak_jniNewInstance("android/view/InputDevice");
    if (o) setInt(o, "mId", id);
    return o;
}
IMPL(dev_get) { RETO(c->args[0].i >= 0 ? newDevice(c->args[0].i) : NULL); }
IMPL(dev_ids) {
    int32_t ids[32];
    int n = android_inputDeviceIds(ids, 32), i;
    ApakJObject *a = apak_jniNewArray('I', n, NULL);
    int32_t *d = (int32_t *)apak_jniArrayData(a, NULL, NULL);
    for (i = 0; d && i < n; i++) d[i] = ids[i];
    RETO(a);
}
IMPL(dev_id) { RETI(getInt(c->self, "mId")); }
IMPL(dev_name) { RETS(android_inputDeviceName(getInt(c->self, "mId"))); }
IMPL(dev_descriptor) {
    char b[48];
    snprintf(b, sizeof b, "androidrt-evdev-%d", getInt(c->self, "mId"));
    RETS(b);
}
IMPL(dev_sources) { RETI(SRC_GAMEPAD_ALL); }
IMPL(dev_supports) { RETZ((c->args[0].i & SRC_GAMEPAD_ALL) == c->args[0].i); }
IMPL(dev_zero) { RETI(0); }
IMPL(dev_false) { RETZ(0); }
IMPL(dev_kbdType) { RETI(1); /* KEYBOARD_TYPE_NON_ALPHABETIC */ }
IMPL(dev_controller) { RETI(1); }
IMPL(dev_range) {
    ApakJObject *o = apak_jniNewInstance("android/view/InputDevice$MotionRange");
    if (o) setInt(o, "mAxis", c->args[0].i);
    RETO(o);
}
IMPL(range_axis) { RETI(getInt(c->self, "mAxis")); }
IMPL(range_min) {
    int32_t a = getInt(c->self, "mAxis");
    RETF(a == 17 || a == 18 || a == 22 || a == 23 ? 0.0f : -1.0f); /* triggers 0..1 */
}
IMPL(range_max) { RETF(1.0f); }
IMPL(range_flat) { RETF(0.1f); }
IMPL(range_fuzz) { RETF(0.0f); }
IMPL(range_range) {
    int32_t a = getInt(c->self, "mAxis");
    RETF(a == 17 || a == 18 || a == 22 || a == 23 ? 1.0f : 2.0f);
}
IMPL(range_source) { RETI(0x01000010); }
IMPL(surface_valid) { RETZ(1); }

/* ── the class table ──────────────────────────────────────────────── */

#define M(n, s, f) { n, s, 0, f }
#define S(n, s, f) { n, s, 1, f }
#define END { NULL, NULL, 0, NULL }
#define F(n, s) { n, s, 0 }
#define FEND { NULL, NULL, 0 }

static const ApakJMethodDef kObject[] = {
    M("<init>", "()V", obj_init), M("getClass", "()Ljava/lang/Class;", obj_getClass),
    M("hashCode", "()I", obj_hashCode), M("equals", "(Ljava/lang/Object;)Z", obj_equals),
    M("toString", "()Ljava/lang/String;", obj_toString), M("notify", "()V", obj_noop),
    M("notifyAll", "()V", obj_noop), M("wait", "()V", obj_wait), M("wait", "(J)V", obj_wait), END,
};
static const ApakJMethodDef kClass[] = {
    M("getName", "()Ljava/lang/String;", class_getName),
    M("getSimpleName", "()Ljava/lang/String;", class_getSimpleName),
    M("getCanonicalName", "()Ljava/lang/String;", class_getName),
    M("getClassLoader", "()Ljava/lang/ClassLoader;", class_getClassLoader),
    S("forName", "(Ljava/lang/String;)Ljava/lang/Class;", class_forName),
    S("forName", "(Ljava/lang/String;ZLjava/lang/ClassLoader;)Ljava/lang/Class;", class_forName),
    M("isInstance", "(Ljava/lang/Object;)Z", class_isInstance), M("isArray", "()Z", class_isArray),
    M("isInterface", "()Z", class_false), M("desiredAssertionStatus", "()Z", class_false),
    M("getSuperclass", "()Ljava/lang/Class;", class_getSuperclass), END,
};
static const ApakJMethodDef kLoader[] = {
    M("loadClass", "(Ljava/lang/String;)Ljava/lang/Class;", loader_loadClass),
    M("loadClass", "(Ljava/lang/String;Z)Ljava/lang/Class;", loader_loadClass),
    M("findClass", "(Ljava/lang/String;)Ljava/lang/Class;", loader_loadClass),
    S("getSystemClassLoader", "()Ljava/lang/ClassLoader;", loader_system),
    M("getParent", "()Ljava/lang/ClassLoader;", ret_null), END,
};
static const ApakJMethodDef kString[] = {
    M("<init>", "()V", str_initEmpty), M("<init>", "(Ljava/lang/String;)V", str_initStr),
    M("<init>", "([B)V", str_initBytes), M("<init>", "([BLjava/lang/String;)V", str_initBytesCs),
    M("<init>", "([BII)V", str_initBytesRange), M("<init>", "([BIILjava/lang/String;)V", str_initBytesRangeCs),
    M("<init>", "([C)V", str_initChars), M("<init>", "([CII)V", str_initCharsRange),
    M("length", "()I", str_length), M("isEmpty", "()Z", str_isEmpty), M("charAt", "(I)C", str_charAt),
    M("getBytes", "()[B", str_getBytes), M("getBytes", "(Ljava/lang/String;)[B", str_getBytesCs),
    M("equals", "(Ljava/lang/Object;)Z", str_equals), M("hashCode", "()I", str_hashCode),
    M("toString", "()Ljava/lang/String;", str_self), M("intern", "()Ljava/lang/String;", str_self),
    M("toLowerCase", "()Ljava/lang/String;", str_lower), M("toUpperCase", "()Ljava/lang/String;", str_upper),
    M("trim", "()Ljava/lang/String;", str_trim), M("indexOf", "(Ljava/lang/String;)I", str_indexOf),
    M("contains", "(Ljava/lang/CharSequence;)Z", str_contains),
    M("startsWith", "(Ljava/lang/String;)Z", str_startsWith), M("endsWith", "(Ljava/lang/String;)Z", str_endsWith),
    M("substring", "(I)Ljava/lang/String;", str_substring1),
    M("substring", "(II)Ljava/lang/String;", str_substring2),
    S("valueOf", "(I)Ljava/lang/String;", str_valueOfI), S("valueOf", "(J)Ljava/lang/String;", str_valueOfJ),
    S("valueOf", "(F)Ljava/lang/String;", str_valueOfF), S("valueOf", "(D)Ljava/lang/String;", str_valueOfD),
    S("valueOf", "(Z)Ljava/lang/String;", str_valueOfZ),
    S("valueOf", "(Ljava/lang/Object;)Ljava/lang/String;", str_valueOfObj), END,
};
static const ApakJMethodDef kThrowable[] = {
    M("<init>", "()V", thr_init), M("<init>", "(Ljava/lang/String;)V", thr_initMsg),
    M("<init>", "(Ljava/lang/String;Ljava/lang/Throwable;)V", thr_initMsgCause),
    M("<init>", "(Ljava/lang/Throwable;)V", thr_init), M("getMessage", "()Ljava/lang/String;", thr_getMessage),
    M("getLocalizedMessage", "()Ljava/lang/String;", thr_getMessage),
    M("toString", "()Ljava/lang/String;", thr_toString), M("printStackTrace", "()V", thr_print),
    M("getCause", "()Ljava/lang/Throwable;", ret_null),
    M("getStackTrace", "()[Ljava/lang/StackTraceElement;", thr_stack), END,
};
static const ApakJFieldDef kValueI[] = { F("value", "I"), FEND };
static const ApakJFieldDef kValueJ[] = { F("value", "J"), FEND };
static const ApakJFieldDef kValueZ[] = { F("value", "Z"), FEND };
static const ApakJFieldDef kValueF[] = { F("value", "F"), FEND };
static const ApakJFieldDef kValueD[] = { F("value", "D"), FEND };
static const ApakJMethodDef kInteger[] = {
    S("valueOf", "(I)Ljava/lang/Integer;", int_valueOf), M("<init>", "(I)V", int_init),
    M("intValue", "()I", int_intValue), M("longValue", "()J", int_longValue),
    M("floatValue", "()F", int_floatValue), M("doubleValue", "()D", int_doubleValue),
    S("parseInt", "(Ljava/lang/String;)I", int_parse), S("parseInt", "(Ljava/lang/String;I)I", int_parseRadix),
    M("toString", "()Ljava/lang/String;", int_toString), S("toString", "(I)Ljava/lang/String;", str_valueOfI), END,
};
static const ApakJMethodDef kLong[] = {
    S("valueOf", "(J)Ljava/lang/Long;", long_valueOf), M("<init>", "(J)V", int_init),
    M("longValue", "()J", long_longValue), M("intValue", "()I", long_intValue),
    S("parseLong", "(Ljava/lang/String;)J", long_parse), S("toString", "(J)Ljava/lang/String;", str_valueOfJ), END,
};
static const ApakJMethodDef kBoolean[] = {
    S("valueOf", "(Z)Ljava/lang/Boolean;", bool_valueOf), M("<init>", "(Z)V", int_init),
    M("booleanValue", "()Z", bool_value), S("parseBoolean", "(Ljava/lang/String;)Z", bool_parse), END,
};
static const ApakJMethodDef kFloat[] = {
    S("valueOf", "(F)Ljava/lang/Float;", float_valueOf), M("<init>", "(F)V", int_init),
    M("floatValue", "()F", float_value), S("parseFloat", "(Ljava/lang/String;)F", float_parse), END,
};
static const ApakJMethodDef kDouble[] = {
    S("valueOf", "(D)Ljava/lang/Double;", double_valueOf), M("<init>", "(D)V", int_init),
    M("doubleValue", "()D", double_value), S("parseDouble", "(Ljava/lang/String;)D", double_parse), END,
};
static const ApakJMethodDef kThread[] = {
    S("currentThread", "()Ljava/lang/Thread;", thread_current), M("getName", "()Ljava/lang/String;", thread_getName),
    M("getId", "()J", thread_getId), M("setName", "(Ljava/lang/String;)V", thread_setName),
    M("getContextClassLoader", "()Ljava/lang/ClassLoader;", ctx_classLoader),
    M("setPriority", "(I)V", obj_noop), M("setDaemon", "(Z)V", obj_noop), M("interrupt", "()V", obj_noop),
    M("isInterrupted", "()Z", class_false), S("sleep", "(J)V", thread_sleep), S("yield", "()V", thread_yield), END,
};
static const ApakJMethodDef kSystem[] = {
    S("currentTimeMillis", "()J", sys_millis), S("nanoTime", "()J", sys_nanos),
    S("getProperty", "(Ljava/lang/String;)Ljava/lang/String;", sys_getProperty),
    S("getProperty", "(Ljava/lang/String;Ljava/lang/String;)Ljava/lang/String;", sys_getPropertyDefault),
    S("loadLibrary", "(Ljava/lang/String;)V", sys_loadLibrary), S("load", "(Ljava/lang/String;)V", sys_loadLibrary),
    S("gc", "()V", obj_noop), S("exit", "(I)V", sys_exit),
    S("identityHashCode", "(Ljava/lang/Object;)I", sys_identityHash),
    S("arraycopy", "(Ljava/lang/Object;ILjava/lang/Object;II)V", sys_arraycopy), END,
};
static const ApakJMethodDef kRuntime[] = {
    S("getRuntime", "()Ljava/lang/Runtime;", rt_get), M("availableProcessors", "()I", rt_cpus),
    M("maxMemory", "()J", rt_max), M("totalMemory", "()J", rt_total), M("freeMemory", "()J", rt_free),
    M("gc", "()V", obj_noop), END,
};
static const ApakJMethodDef kLocale[] = {
    S("getDefault", "()Ljava/util/Locale;", loc_default), M("<init>", "(Ljava/lang/String;)V", loc_init1),
    M("<init>", "(Ljava/lang/String;Ljava/lang/String;)V", loc_init2),
    M("getLanguage", "()Ljava/lang/String;", loc_language), M("getCountry", "()Ljava/lang/String;", loc_country),
    M("toString", "()Ljava/lang/String;", loc_toString), M("toLanguageTag", "()Ljava/lang/String;", loc_tag),
    M("getDisplayLanguage", "()Ljava/lang/String;", loc_language), END,
};
static const ApakJMethodDef kFile[] = {
    M("<init>", "(Ljava/lang/String;)V", file_init), M("<init>", "(Ljava/io/File;Ljava/lang/String;)V", file_initParent),
    M("<init>", "(Ljava/lang/String;Ljava/lang/String;)V", file_initParentStr),
    M("getAbsolutePath", "()Ljava/lang/String;", file_path), M("getPath", "()Ljava/lang/String;", file_path),
    M("getCanonicalPath", "()Ljava/lang/String;", file_path), M("toString", "()Ljava/lang/String;", file_path),
    M("getAbsoluteFile", "()Ljava/io/File;", view_self), M("getName", "()Ljava/lang/String;", file_name),
    M("getParent", "()Ljava/lang/String;", file_parent), M("getParentFile", "()Ljava/io/File;", file_parentFile),
    M("exists", "()Z", file_exists), M("isDirectory", "()Z", file_isDir), M("isFile", "()Z", file_isFile),
    M("canRead", "()Z", file_exists), M("canWrite", "()Z", file_exists), M("length", "()J", file_length),
    M("mkdirs", "()Z", file_mkdirs), M("mkdir", "()Z", file_mkdirs), M("delete", "()Z", file_delete),
    M("list", "()[Ljava/lang/String;", file_list), M("getTotalSpace", "()J", file_total),
    M("getFreeSpace", "()J", file_free), M("getUsableSpace", "()J", file_usable), END,
};
static const ApakJMethodDef kBuffer[] = { M("capacity", "()I", buf_capacity), M("isDirect", "()Z", act_true), END };

static const ApakJMethodDef kContext[] = {
    M("getFilesDir", "()Ljava/io/File;", ctx_filesDir), M("getCacheDir", "()Ljava/io/File;", ctx_cacheDir),
    M("getCodeCacheDir", "()Ljava/io/File;", ctx_cacheDir), M("getNoBackupFilesDir", "()Ljava/io/File;", ctx_filesDir),
    M("getDataDir", "()Ljava/io/File;", ctx_dataDir), M("getObbDir", "()Ljava/io/File;", ctx_obbDir),
    M("getObbDirs", "()[Ljava/io/File;", ctx_obbDirs),
    M("getExternalFilesDir", "(Ljava/lang/String;)Ljava/io/File;", ctx_externalFilesDir),
    M("getExternalFilesDirs", "(Ljava/lang/String;)[Ljava/io/File;", ctx_externalFilesDirs),
    M("getExternalCacheDir", "()Ljava/io/File;", ctx_externalCacheDir),
    M("getDir", "(Ljava/lang/String;I)Ljava/io/File;", ctx_getDir),
    M("getDatabasePath", "(Ljava/lang/String;)Ljava/io/File;", ctx_databasePath),
    M("getPackageName", "()Ljava/lang/String;", ctx_packageName),
    M("getPackageCodePath", "()Ljava/lang/String;", ctx_packageCodePath),
    M("getPackageResourcePath", "()Ljava/lang/String;", ctx_packageCodePath),
    M("getApplicationContext", "()Landroid/content/Context;", ctx_appContext),
    M("getApplicationInfo", "()Landroid/content/pm/ApplicationInfo;", ctx_appInfo),
    M("getAssets", "()Landroid/content/res/AssetManager;", ctx_assets),
    M("getResources", "()Landroid/content/res/Resources;", ctx_resources),
    M("getClassLoader", "()Ljava/lang/ClassLoader;", ctx_classLoader),
    M("getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", ctx_systemService),
    M("checkSelfPermission", "(Ljava/lang/String;)I", ctx_checkPermission),
    M("checkCallingOrSelfPermission", "(Ljava/lang/String;)I", ctx_checkPermission),
    M("startActivity", "(Landroid/content/Intent;)V", ctx_startActivity), END,
};
static const ApakJMethodDef kActivity[] = {
    M("getWindow", "()Landroid/view/Window;", act_window),
    M("getWindowManager", "()Landroid/view/WindowManager;", act_windowManager), M("finish", "()V", act_finish),
    M("finishAffinity", "()V", act_finish), M("finishAndRemoveTask", "()V", act_finish),
    M("isFinishing", "()Z", act_false), M("isDestroyed", "()Z", act_false), M("hasWindowFocus", "()Z", act_true),
    M("getIntent", "()Landroid/content/Intent;", act_intent), M("setIntent", "(Landroid/content/Intent;)V", obj_noop),
    M("setRequestedOrientation", "(I)V", obj_noop), M("getRequestedOrientation", "()I", act_orientation),
    M("runOnUiThread", "(Ljava/lang/Runnable;)V", act_runOnUiThread),
    M("getApplication", "()Landroid/app/Application;", ctx_application),
    M("moveTaskToBack", "(Z)Z", act_true), END,
};
static const ApakJFieldDef kAppInfoFields[] = {
    F("sourceDir", "Ljava/lang/String;"), F("publicSourceDir", "Ljava/lang/String;"),
    F("nativeLibraryDir", "Ljava/lang/String;"), F("dataDir", "Ljava/lang/String;"),
    F("packageName", "Ljava/lang/String;"), F("targetSdkVersion", "I"), F("minSdkVersion", "I"), F("flags", "I"),
    FEND,
};
static const ApakJMethodDef kActivityManager[] = {
    M("getMemoryInfo", "(Landroid/app/ActivityManager$MemoryInfo;)V", am_memoryInfo),
    M("getMemoryClass", "()I", am_memoryClass), M("getLargeMemoryClass", "()I", am_largeMemoryClass),
    M("isLowRamDevice", "()Z", am_isLowRam), END,
};
static const ApakJFieldDef kMemoryInfoFields[] = {
    F("availMem", "J"), F("totalMem", "J"), F("threshold", "J"), F("lowMemory", "Z"), FEND,
};
static const ApakJMethodDef kAudioManager[] = {
    M("getProperty", "(Ljava/lang/String;)Ljava/lang/String;", audio_property),
    M("getStreamVolume", "(I)I", audio_volume), M("getStreamMaxVolume", "(I)I", audio_maxVolume), END,
};
static const ApakJMethodDef kAssets[] = {
    M("list", "(Ljava/lang/String;)[Ljava/lang/String;", assets_list), M("close", "()V", obj_noop), END,
};
static const ApakJMethodDef kResources[] = {
    M("getConfiguration", "()Landroid/content/res/Configuration;", res_configuration),
    M("getDisplayMetrics", "()Landroid/util/DisplayMetrics;", res_metrics),
    M("getIdentifier", "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;)I", res_identifier), END,
};
static const ApakJMethodDef kConfiguration[] = { M("<init>", "()V", cfg_init), END };
static const ApakJFieldDef kConfigurationFields[] = {
    F("orientation", "I"), F("touchscreen", "I"), F("keyboard", "I"), F("keyboardHidden", "I"),
    F("hardKeyboardHidden", "I"), F("navigation", "I"), F("navigationHidden", "I"), F("screenLayout", "I"),
    F("uiMode", "I"), F("densityDpi", "I"), F("screenWidthDp", "I"), F("screenHeightDp", "I"),
    F("smallestScreenWidthDp", "I"), F("fontScale", "F"), F("mcc", "I"), F("mnc", "I"), F("colorMode", "I"),
    F("fontWeightAdjustment", "I"), F("locale", "Ljava/util/Locale;"), FEND,
};
static const ApakJMethodDef kMetrics[] = { M("<init>", "()V", dm_init), M("setToDefaults", "()V", dm_setToDefaults), END };
static const ApakJFieldDef kMetricsFields[] = {
    F("widthPixels", "I"), F("heightPixels", "I"), F("densityDpi", "I"), F("density", "F"),
    F("scaledDensity", "F"), F("xdpi", "F"), F("ydpi", "F"), FEND,
};
static const ApakJMethodDef kWindowManager[] = { M("getDefaultDisplay", "()Landroid/view/Display;", wm_display), END };
static const ApakJMethodDef kDisplay[] = {
    M("getWidth", "()I", disp_width), M("getHeight", "()I", disp_height), M("getRotation", "()I", disp_zero),
    M("getOrientation", "()I", disp_zero), M("getDisplayId", "()I", disp_zero),
    M("getRefreshRate", "()F", disp_refresh), M("getName", "()Ljava/lang/String;", disp_name),
    M("getMetrics", "(Landroid/util/DisplayMetrics;)V", disp_metrics),
    M("getRealMetrics", "(Landroid/util/DisplayMetrics;)V", disp_metrics),
    M("getSize", "(Landroid/graphics/Point;)V", disp_size), M("getRealSize", "(Landroid/graphics/Point;)V", disp_size),
    M("getSupportedRefreshRates", "()[F", disp_rates), END,
};
static const ApakJMethodDef kPoint[] = { M("<init>", "()V", obj_noop), M("<init>", "(II)V", point_init2), END };
static const ApakJFieldDef kPointFields[] = { F("x", "I"), F("y", "I"), FEND };
static const ApakJMethodDef kRect[] = {
    M("<init>", "()V", obj_noop), M("<init>", "(IIII)V", rect_init4), M("width", "()I", rect_width),
    M("height", "()I", rect_height), END,
};
static const ApakJFieldDef kRectFields[] = { F("left", "I"), F("top", "I"), F("right", "I"), F("bottom", "I"), FEND };
static const ApakJMethodDef kWindow[] = {
    M("getDecorView", "()Landroid/view/View;", window_view), M("addFlags", "(I)V", obj_noop),
    M("clearFlags", "(I)V", obj_noop), M("setFlags", "(II)V", obj_noop), M("setSoftInputMode", "(I)V", obj_noop),
    END,
};
static const ApakJMethodDef kView[] = {
    M("setSystemUiVisibility", "(I)V", obj_noop), M("getSystemUiVisibility", "()I", disp_zero),
    M("getWidth", "()I", view_width), M("getHeight", "()I", view_height),
    M("getRootView", "()Landroid/view/View;", view_self), M("requestFocus", "()Z", act_true),
    M("getContext", "()Landroid/content/Context;", view_context), M("post", "(Ljava/lang/Runnable;)Z", view_post),
    M("setKeepScreenOn", "(Z)V", obj_noop), M("getWindowToken", "()Landroid/os/IBinder;", ret_null), END,
};
static const ApakJMethodDef kIntent[] = {
    M("<init>", "()V", obj_noop), M("<init>", "(Ljava/lang/String;)V", obj_noop),
    M("<init>", "(Ljava/lang/String;Landroid/net/Uri;)V", obj_noop),
    M("getAction", "()Ljava/lang/String;", intent_action),
    M("getStringExtra", "(Ljava/lang/String;)Ljava/lang/String;", ret_null),
    M("getIntExtra", "(Ljava/lang/String;I)I", intent_intDefault),
    M("getBooleanExtra", "(Ljava/lang/String;Z)Z", intent_boolDefault),
    M("getLongExtra", "(Ljava/lang/String;J)J", intent_longDefault),
    M("hasExtra", "(Ljava/lang/String;)Z", act_false), M("getData", "()Landroid/net/Uri;", ret_null),
    M("getDataString", "()Ljava/lang/String;", ret_null), M("getExtras", "()Landroid/os/Bundle;", ret_null), END,
};
static const ApakJMethodDef kUri[] = {
    S("parse", "(Ljava/lang/String;)Landroid/net/Uri;", uri_parse), M("toString", "()Ljava/lang/String;", uri_toString),
    END,
};
static const ApakJMethodDef kProcess[] = {
    S("myPid", "()I", proc_pid), S("myTid", "()I", proc_tid), S("setThreadPriority", "(I)V", obj_noop),
    S("setThreadPriority", "(II)V", obj_noop), END,
};
static const ApakJMethodDef kClock[] = {
    S("uptimeMillis", "()J", clock_uptime), S("elapsedRealtime", "()J", clock_elapsed),
    S("elapsedRealtimeNanos", "()J", clock_elapsedNanos), S("sleep", "(J)V", thread_sleep), END,
};
static const ApakJMethodDef kLog[] = {
    S("v", "(Ljava/lang/String;Ljava/lang/String;)I", log_v), S("d", "(Ljava/lang/String;Ljava/lang/String;)I", log_d),
    S("i", "(Ljava/lang/String;Ljava/lang/String;)I", log_i), S("w", "(Ljava/lang/String;Ljava/lang/String;)I", log_w),
    S("e", "(Ljava/lang/String;Ljava/lang/String;)I", log_e), END,
};
static const ApakJMethodDef kInputEvent[] = {
    M("getDeviceId", "()I", key_device), M("getSource", "()I", key_source), M("getEventTime", "()J", key_eventTime),
    END,
};
static const ApakJMethodDef kKeyEvent[] = {
    M("<init>", "(II)V", key_init), M("getAction", "()I", key_action), M("getKeyCode", "()I", key_code),
    M("getRepeatCount", "()I", key_repeat), M("getMetaState", "()I", key_meta), M("getModifiers", "()I", key_meta),
    M("getFlags", "()I", key_flags), M("getDeviceId", "()I", key_device), M("getSource", "()I", key_source),
    M("getScanCode", "()I", key_scan), M("getEventTime", "()J", key_eventTime), M("getDownTime", "()J", key_downTime),
    M("getUnicodeChar", "()I", key_unicode), M("getUnicodeChar", "(I)I", key_unicode),
    M("isShiftPressed", "()Z", key_false), M("isAltPressed", "()Z", key_false), M("isCtrlPressed", "()Z", key_false),
    M("isSystem", "()Z", key_false), M("getCharacters", "()Ljava/lang/String;", ret_null), END,
};
static const ApakJMethodDef kMotionEvent[] = {
    M("getAction", "()I", mot_action), M("getActionMasked", "()I", mot_actionMasked),
    M("getActionIndex", "()I", mot_actionIndex), M("getPointerCount", "()I", mot_one),
    M("getPointerId", "(I)I", mot_pointerId), M("getToolType", "(I)I", mot_toolType), M("getX", "()F", mot_x),
    M("getY", "()F", mot_y), M("getX", "(I)F", mot_x), M("getY", "(I)F", mot_y), M("getRawX", "()F", mot_x),
    M("getRawY", "()F", mot_y), M("getRawX", "(I)F", mot_x), M("getRawY", "(I)F", mot_y),
    M("getAxisValue", "(I)F", mot_axis1), M("getAxisValue", "(II)F", mot_axis2), M("getHistorySize", "()I", mot_zeroI),
    M("getHistoricalAxisValue", "(III)F", mot_zeroF), M("getHistoricalEventTime", "(I)J", mot_eventTime),
    M("getDeviceId", "()I", mot_device), M("getSource", "()I", mot_source), M("getEventTime", "()J", mot_eventTime),
    M("getDownTime", "()J", mot_downTime), M("getFlags", "()I", mot_flags), M("getMetaState", "()I", mot_meta),
    M("getButtonState", "()I", mot_buttons), M("getActionButton", "()I", mot_zeroI),
    M("getClassification", "()I", mot_zeroI), M("getEdgeFlags", "()I", mot_zeroI),
    M("getXPrecision", "()F", mot_oneF), M("getYPrecision", "()F", mot_oneF), M("getPressure", "(I)F", mot_oneF),
    M("getSize", "(I)F", mot_zeroF), M("getTouchMajor", "(I)F", mot_zeroF), M("getTouchMinor", "(I)F", mot_zeroF),
    M("getToolMajor", "(I)F", mot_zeroF), M("getToolMinor", "(I)F", mot_zeroF),
    M("getOrientation", "(I)F", mot_zeroF), END,
};
static const ApakJMethodDef kInputDevice[] = {
    S("getDevice", "(I)Landroid/view/InputDevice;", dev_get), S("getDeviceIds", "()[I", dev_ids),
    M("getId", "()I", dev_id), M("getName", "()Ljava/lang/String;", dev_name),
    M("getDescriptor", "()Ljava/lang/String;", dev_descriptor), M("getSources", "()I", dev_sources),
    M("supportsSource", "(I)Z", dev_supports), M("getVendorId", "()I", dev_zero), M("getProductId", "()I", dev_zero),
    M("isVirtual", "()Z", dev_false), M("isExternal", "()Z", dev_false), M("getKeyboardType", "()I", dev_kbdType),
    M("getControllerNumber", "()I", dev_controller),
    M("getMotionRange", "(I)Landroid/view/InputDevice$MotionRange;", dev_range),
    M("getMotionRange", "(II)Landroid/view/InputDevice$MotionRange;", dev_range), END,
};
static const ApakJFieldDef kInputDeviceFields[] = { F("mId", "I"), FEND };
static const ApakJMethodDef kMotionRange[] = {
    M("getAxis", "()I", range_axis), M("getMin", "()F", range_min), M("getMax", "()F", range_max),
    M("getFlat", "()F", range_flat), M("getFuzz", "()F", range_fuzz), M("getRange", "()F", range_range),
    M("getResolution", "()F", range_fuzz), M("getSource", "()I", range_source), END,
};
static const ApakJFieldDef kMotionRangeFields[] = { F("mAxis", "I"), FEND };
static const ApakJMethodDef kSurface[] = { M("isValid", "()Z", surface_valid), M("release", "()V", obj_noop), END };

/* android.os.Build: static fields only, filled in by defineBuild(). */
#define SF(n, s) { n, s, 1 }
static const ApakJFieldDef kBuildFields[] = {
    SF("MANUFACTURER", "Ljava/lang/String;"), SF("BRAND", "Ljava/lang/String;"), SF("MODEL", "Ljava/lang/String;"),
    SF("DEVICE", "Ljava/lang/String;"), SF("PRODUCT", "Ljava/lang/String;"), SF("BOARD", "Ljava/lang/String;"),
    SF("HARDWARE", "Ljava/lang/String;"), SF("FINGERPRINT", "Ljava/lang/String;"), SF("ID", "Ljava/lang/String;"),
    SF("DISPLAY", "Ljava/lang/String;"), SF("TAGS", "Ljava/lang/String;"), SF("TYPE", "Ljava/lang/String;"),
    SF("SERIAL", "Ljava/lang/String;"), SF("CPU_ABI", "Ljava/lang/String;"), SF("CPU_ABI2", "Ljava/lang/String;"),
    SF("SUPPORTED_ABIS", "[Ljava/lang/String;"), SF("SUPPORTED_64_BIT_ABIS", "[Ljava/lang/String;"),
    SF("SUPPORTED_32_BIT_ABIS", "[Ljava/lang/String;"), FEND,
};
static const ApakJFieldDef kBuildVersionFields[] = {
    SF("SDK_INT", "I"), SF("RELEASE", "Ljava/lang/String;"), SF("SDK", "Ljava/lang/String;"),
    SF("CODENAME", "Ljava/lang/String;"), SF("INCREMENTAL", "Ljava/lang/String;"), FEND,
};

static const ApakJClassDef kBase[] = {
    { "java/lang/Object", NULL, kObject, NULL },
    { "java/lang/Class", "java/lang/Object", kClass, NULL },
    { "java/lang/ClassLoader", "java/lang/Object", kLoader, NULL },
    { "java/lang/String", "java/lang/Object", kString, NULL },
    { "java/lang/Throwable", "java/lang/Object", kThrowable, NULL },
    { "java/lang/Exception", "java/lang/Throwable", NULL, NULL },
    { "java/lang/Error", "java/lang/Throwable", NULL, NULL },
    { "java/lang/RuntimeException", "java/lang/Exception", NULL, NULL },
    { "java/lang/IllegalArgumentException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/NumberFormatException", "java/lang/IllegalArgumentException", NULL, NULL },
    { "java/lang/IllegalStateException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/NullPointerException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/UnsupportedOperationException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/IndexOutOfBoundsException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/ArrayIndexOutOfBoundsException", "java/lang/IndexOutOfBoundsException", NULL, NULL },
    { "java/lang/StringIndexOutOfBoundsException", "java/lang/IndexOutOfBoundsException", NULL, NULL },
    { "java/lang/NegativeArraySizeException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/ClassCastException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/ArithmeticException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/SecurityException", "java/lang/RuntimeException", NULL, NULL },
    { "java/lang/ReflectiveOperationException", "java/lang/Exception", NULL, NULL },
    { "java/lang/ClassNotFoundException", "java/lang/ReflectiveOperationException", NULL, NULL },
    { "java/lang/InterruptedException", "java/lang/Exception", NULL, NULL },
    { "java/lang/LinkageError", "java/lang/Error", NULL, NULL },
    { "java/lang/NoClassDefFoundError", "java/lang/LinkageError", NULL, NULL },
    { "java/lang/UnsatisfiedLinkError", "java/lang/LinkageError", NULL, NULL },
    { "java/lang/IncompatibleClassChangeError", "java/lang/LinkageError", NULL, NULL },
    { "java/lang/NoSuchMethodError", "java/lang/IncompatibleClassChangeError", NULL, NULL },
    { "java/lang/NoSuchFieldError", "java/lang/IncompatibleClassChangeError", NULL, NULL },
    { "java/lang/VirtualMachineError", "java/lang/Error", NULL, NULL },
    { "java/lang/OutOfMemoryError", "java/lang/VirtualMachineError", NULL, NULL },
    { "java/io/IOException", "java/lang/Exception", NULL, NULL },
    { "java/io/FileNotFoundException", "java/io/IOException", NULL, NULL },
    { "java/lang/StackTraceElement", "java/lang/Object", NULL, NULL },
    { "java/lang/Number", "java/lang/Object", NULL, NULL },
    { "java/lang/Integer", "java/lang/Number", kInteger, kValueI },
    { "java/lang/Long", "java/lang/Number", kLong, kValueJ },
    { "java/lang/Boolean", "java/lang/Object", kBoolean, kValueZ },
    { "java/lang/Float", "java/lang/Number", kFloat, kValueF },
    { "java/lang/Double", "java/lang/Number", kDouble, kValueD },
    { "java/lang/Thread", "java/lang/Object", kThread, NULL },
    { "java/lang/System", "java/lang/Object", kSystem, NULL },
    { "java/lang/Runtime", "java/lang/Object", kRuntime, NULL },
    { "java/lang/reflect/Method", "java/lang/Object", NULL, NULL },
    { "java/lang/reflect/Field", "java/lang/Object", NULL, NULL },
    { "java/util/Locale", "java/lang/Object", kLocale, NULL },
    { "java/io/File", "java/lang/Object", kFile, NULL },
    { "java/nio/Buffer", "java/lang/Object", NULL, NULL },
    { "java/nio/ByteBuffer", "java/nio/Buffer", kBuffer, NULL },
    { "java/nio/DirectByteBuffer", "java/nio/ByteBuffer", NULL, NULL },
    { "android/content/Context", "java/lang/Object", kContext, NULL },
    { "android/content/ContextWrapper", "android/content/Context", NULL, NULL },
    { "android/view/ContextThemeWrapper", "android/content/ContextWrapper", NULL, NULL },
    { "android/app/Activity", "android/view/ContextThemeWrapper", kActivity, NULL },
    { "android/app/NativeActivity", "android/app/Activity", NULL, NULL },
    { "android/app/Application", "android/content/ContextWrapper", NULL, NULL },
    { "android/content/pm/ApplicationInfo", "java/lang/Object", NULL, kAppInfoFields },
    { "android/app/ActivityManager", "java/lang/Object", kActivityManager, NULL },
    { "android/app/ActivityManager$MemoryInfo", "java/lang/Object", NULL, kMemoryInfoFields },
    { "android/media/AudioManager", "java/lang/Object", kAudioManager, NULL },
    { "android/content/res/AssetManager", "java/lang/Object", kAssets, NULL },
    { "android/content/res/Resources", "java/lang/Object", kResources, NULL },
    { "android/content/res/Configuration", "java/lang/Object", kConfiguration, kConfigurationFields },
    { "android/util/DisplayMetrics", "java/lang/Object", kMetrics, kMetricsFields },
    { "android/view/WindowManager", "java/lang/Object", kWindowManager, NULL },
    { "android/view/Display", "java/lang/Object", kDisplay, NULL },
    { "android/graphics/Point", "java/lang/Object", kPoint, kPointFields },
    { "android/graphics/Rect", "java/lang/Object", kRect, kRectFields },
    { "android/view/Window", "java/lang/Object", kWindow, NULL },
    { "android/view/View", "java/lang/Object", kView, NULL },
    { "android/content/Intent", "java/lang/Object", kIntent, NULL },
    { "android/net/Uri", "java/lang/Object", kUri, NULL },
    { "android/os/Process", "java/lang/Object", kProcess, NULL },
    { "android/os/SystemClock", "java/lang/Object", kClock, NULL },
    { "android/util/Log", "java/lang/Object", kLog, NULL },
    { "android/view/InputEvent", "java/lang/Object", kInputEvent, NULL },
    { "android/view/KeyEvent", "android/view/InputEvent", kKeyEvent, NULL },
    { "android/view/MotionEvent", "android/view/InputEvent", kMotionEvent, NULL },
    { "android/view/InputDevice", "java/lang/Object", kInputDevice, kInputDeviceFields },
    { "android/view/InputDevice$MotionRange", "java/lang/Object", kMotionRange, kMotionRangeFields },
    { "android/view/Surface", "java/lang/Object", kSurface, NULL },
    { "android/os/Build", "java/lang/Object", NULL, kBuildFields },
    { "android/os/Build$VERSION", "java/lang/Object", NULL, kBuildVersionFields },
    { NULL, NULL, NULL, NULL },
};

static void buildString(const char *cls, const char *name, const char *value) {
    ApakJValue v;
    v.j = 0;
    v.l = apak_jniNewString(value);
    apak_jniStaticSet(cls, name, v);
}

/* The Build constants a game reads to describe the device: the TrimUI
 * handheld as JukaHub detected it, playing Android ANDROIDRT_SDK_VERSION. */
static void defineBuild(void) {
    const ApakDevice *d = apak_device();
    const char *board = cfg()->board && *cfg()->board ? cfg()->board : d->board;
    char fingerprint[256], release[16], sdk[16];
    ApakJValue v;
#if defined(__aarch64__)
    static const char *const abis[] = { "arm64-v8a" };
#elif defined(__x86_64__)
    static const char *const abis[] = { "x86_64" };
#else
    static const char *const abis[] = { "unknown" };
#endif
    int api = cfg()->sdkVersion > 0 ? cfg()->sdkVersion : ANDROIDRT_SDK_VERSION;
    snprintf(release, sizeof release, "%d", api >= 33 ? api - 20 : api >= 29 ? api - 19 : 9);
    snprintf(sdk, sizeof sdk, "%d", api);
    snprintf(fingerprint, sizeof fingerprint, "%s/%s/%s:%s/JUKAHUB/1:user/release-keys", d->manufacturer, board, board,
             release);
    buildString("android/os/Build", "MANUFACTURER", d->manufacturer);
    buildString("android/os/Build", "BRAND", d->manufacturer);
    buildString("android/os/Build", "MODEL", cfg()->model && *cfg()->model ? cfg()->model : d->model);
    buildString("android/os/Build", "DEVICE", board);
    buildString("android/os/Build", "PRODUCT", board);
    buildString("android/os/Build", "BOARD", board);
    buildString("android/os/Build", "HARDWARE", board);
    buildString("android/os/Build", "FINGERPRINT", fingerprint);
    buildString("android/os/Build", "ID", "JUKAHUB");
    buildString("android/os/Build", "DISPLAY", "JUKAHUB");
    buildString("android/os/Build", "TAGS", "release-keys");
    buildString("android/os/Build", "TYPE", "user");
    buildString("android/os/Build", "SERIAL", "unknown");
    buildString("android/os/Build", "CPU_ABI", abis[0]);
    buildString("android/os/Build", "CPU_ABI2", "");
    v.j = 0;
    v.l = apak_jniNewStringArray(abis, 1);
    apak_jniStaticSet("android/os/Build", "SUPPORTED_ABIS", v);
    v.l = apak_jniNewStringArray(abis, 1);
    apak_jniStaticSet("android/os/Build", "SUPPORTED_64_BIT_ABIS", v);
    v.l = apak_jniNewStringArray(abis, 0);
    apak_jniStaticSet("android/os/Build", "SUPPORTED_32_BIT_ABIS", v);
    v.j = 0;
    v.i = api;
    apak_jniStaticSet("android/os/Build$VERSION", "SDK_INT", v);
    buildString("android/os/Build$VERSION", "RELEASE", release);
    buildString("android/os/Build$VERSION", "SDK", sdk);
    buildString("android/os/Build$VERSION", "CODENAME", "REL");
    buildString("android/os/Build$VERSION", "INCREMENTAL", "1");
}

void apak_jniDefineBase(void) {
    apak_jniDefineAll(kBase);
    defineBuild();
}
