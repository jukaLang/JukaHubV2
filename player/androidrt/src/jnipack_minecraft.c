/* jnipack_minecraft.c — the Java side Minecraft: Bedrock Edition's native
 * library (libminecraftpe.so) calls on Android, for packages
 * com.mojang.minecraftpe / com.mojang.minecrafttrialpe.
 *
 * Class and method names are the public facts of the game's JNI surface as
 * documented by open launchers (minecraft-linux/mcpelauncher's fake JNI
 * classes, the class names in their launch logs); the implementations are
 * AndroidRT's own. Anything the game asks for beyond this file still works
 * the generic way: its APK classes become phantoms, every unimplemented
 * member is logged once, calls return defaults.
 *
 * What is real here: storage paths (ANDROIDPAK_DATA), screen size and
 * physical DPI, locale, memory figures, network link state, asset and file
 * reads, the clipboard, a per-install device id, a key-value store for
 * getSecureStorageKey (plain text: there is no keystore), and the APK's own
 * signing-certificate hash.
 *
 * What is honestly UNAVAILABLE, never faked:
 *   store / purchases / entitlements — the store never initialises
 *     (onStoreInitialized(false) when the game registered it), product and
 *     purchase queries fail, purchases are refused;
 *   license — hasVerifiedLicense() is false and receivedLicenseResponse()
 *     is false: no license server was asked, no answer is invented;
 *   Xbox Live — initializeXboxLive does nothing, sign-in cannot complete;
 *   the on-screen keyboard and URL opening — logged, no-ops.
 * The game's own checks run unmodified; what it does in that state is its
 * decision (see docs/ANDROID-MINECRAFT.md).
 */
#define _GNU_SOURCE
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>
#if !defined(_WIN32)
#include <sys/statvfs.h>
#endif

#include "actbridge.h"
#include "apkzip.h"
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

#define MAIN_ACTIVITY "com/mojang/minecraftpe/MainActivity"
#define STORE_LISTENER "com/mojang/minecraftpe/store/NativeStoreListener"

extern ANDROIDRT_EXPORT AAssetManager *AAssetManager_fromJava(void *env, void *assetManager);
extern void *AAssetManager_open(AAssetManager *mgr, const char *fileName, int mode);
extern int AAsset_read(void *asset, void *buf, size_t count);
extern off_t AAsset_getLength(void *asset);
extern void AAsset_close(void *asset);

static const ApakJniConfig *cfg(void) { return apak_jniConfig(); }

/* ── small state ───────────────────────────────────────────────────── */

static char g_clipboard[4096];
static char g_textbox[1024];

static void filesPath(const char *name, char *out, size_t cap) { snprintf(out, cap, "%s/%s", cfg()->filesDir, name); }

/* A random id made once per install (not a hardware identifier). */
static const char *deviceId(void) {
    static char id[40];
    char path[2048];
    FILE *f;
    if (id[0]) return id;
    filesPath("androidrt-device-id", path, sizeof path);
    f = fopen(path, "r");
    if (f) {
        if (!fgets(id, sizeof id, f)) id[0] = '\0';
        fclose(f);
        id[strcspn(id, "\r\n")] = '\0';
    }
    if (strlen(id) < 16) {
        unsigned char r[16];
        int i;
        FILE *u = fopen("/dev/urandom", "rb");
        if (!u || fread(r, 1, sizeof r, u) != sizeof r) {
            for (i = 0; i < 16; i++) r[i] = (unsigned char)(rand() ^ (int)time(NULL));
        }
        if (u) fclose(u);
        for (i = 0; i < 16; i++) snprintf(id + 2 * i, 3, "%02x", r[i]);
        f = fopen(path, "w");
        if (f) {
            fprintf(f, "%s\n", id);
            fclose(f);
        }
    }
    return id;
}

static void uuid4(char out[37]) {
    unsigned char r[16];
    FILE *u = fopen("/dev/urandom", "rb");
    int i;
    if (!u || fread(r, 1, sizeof r, u) != sizeof r)
        for (i = 0; i < 16; i++) r[i] = (unsigned char)rand();
    if (u) fclose(u);
    r[6] = (unsigned char)((r[6] & 0x0f) | 0x40);
    r[8] = (unsigned char)((r[8] & 0x3f) | 0x80);
    snprintf(out, 37, "%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x", r[0], r[1], r[2], r[3],
             r[4], r[5], r[6], r[7], r[8], r[9], r[10], r[11], r[12], r[13], r[14], r[15]);
}

/* getSecureStorageKey/setSecureStorageKey: one key=value per line. */
static void secureGet(const char *key, char *out, size_t cap) {
    char path[2048], line[2048];
    size_t klen;
    FILE *f;
    out[0] = '\0';
    if (!key) return;
    klen = strlen(key);
    filesPath("androidrt-secure-storage.txt", path, sizeof path);
    f = fopen(path, "r");
    if (!f) return;
    while (fgets(line, sizeof line, f)) {
        if (strncmp(line, key, klen) == 0 && line[klen] == '=') {
            snprintf(out, cap, "%s", line + klen + 1);
            out[strcspn(out, "\r\n")] = '\0';
            break;
        }
    }
    fclose(f);
}

static void secureSet(const char *key, const char *value) {
    char path[2048], tmp[2100], line[2048];
    size_t klen;
    FILE *in, *out;
    if (!key || strchr(key, '=') || strchr(key, '\n')) return;
    klen = strlen(key);
    filesPath("androidrt-secure-storage.txt", path, sizeof path);
    snprintf(tmp, sizeof tmp, "%s.tmp", path);
    out = fopen(tmp, "w");
    if (!out) return;
    in = fopen(path, "r");
    while (in && fgets(line, sizeof line, in))
        if (!(strncmp(line, key, klen) == 0 && line[klen] == '=')) fputs(line, out);
    if (in) fclose(in);
    if (value && !strchr(value, '\n')) fprintf(out, "%s=%s\n", key, value);
    fclose(out);
    rename(tmp, path);
}

/* /proc/cpuinfo "Key : value" */
static void cpuinfo(const char *key, char *out, size_t cap) {
    FILE *f = fopen("/proc/cpuinfo", "r");
    char line[1024];
    size_t klen = strlen(key);
    out[0] = '\0';
    if (!f) return;
    while (fgets(line, sizeof line, f)) {
        char *colon;
        if (strncmp(line, key, klen) != 0) continue;
        colon = strchr(line, ':');
        if (!colon) continue;
        colon++;
        while (*colon == ' ' || *colon == '\t') colon++;
        snprintf(out, cap, "%s", colon);
        out[strcspn(out, "\r\n")] = '\0';
        break;
    }
    fclose(f);
}

static int cpuCount(void) {
    long n = sysconf(_SC_NPROCESSORS_ONLN);
    return n > 0 ? (int)n : 1;
}

/* ── the APK's signing certificate (PackageManager's Signature) ───── */

typedef struct {
    const uint8_t *p, *end;
} Der;

/* Read one DER TLV; *val and *vlen = contents, returns the whole TLV length. */
static size_t derNext(Der *d, uint8_t *tag, const uint8_t **val, size_t *vlen) {
    const uint8_t *s = d->p;
    size_t len;
    if (d->p + 2 > d->end) return 0;
    *tag = *d->p++;
    len = *d->p++;
    if (len & 0x80) {
        int n = (int)(len & 0x7f);
        if (n == 0 || n > 4 || d->p + n > d->end) return 0;
        len = 0;
        while (n--) len = (len << 8) | *d->p++;
    }
    if (len > (size_t)(d->end - d->p)) return 0;
    *val = d->p;
    *vlen = len;
    d->p += len;
    return (size_t)(d->p - s);
}

/* PKCS#7 ContentInfo → SignedData → certificates[0], whole DER. */
static int pkcs7FirstCert(const uint8_t *data, size_t len, const uint8_t **cert, size_t *certLen) {
    Der d = { data, data + len }, sd, inner;
    uint8_t tag;
    const uint8_t *v;
    size_t vl;
    if (!derNext(&d, &tag, &v, &vl) || tag != 0x30) return -1; /* ContentInfo */
    d.p = v;
    d.end = v + vl;
    if (!derNext(&d, &tag, &v, &vl) || tag != 0x06) return -1; /* contentType */
    if (!derNext(&d, &tag, &v, &vl) || tag != 0xa0) return -1; /* [0] EXPLICIT */
    sd.p = v;
    sd.end = v + vl;
    if (!derNext(&sd, &tag, &v, &vl) || tag != 0x30) return -1; /* SignedData */
    inner.p = v;
    inner.end = v + vl;
    while (inner.p < inner.end) {
        const uint8_t *start = inner.p;
        if (!derNext(&inner, &tag, &v, &vl)) return -1;
        (void)start;
        if (tag == 0xa0) { /* [0] IMPLICIT certificates */
            Der certs = { v, v + vl };
            const uint8_t *cstart = certs.p;
            size_t whole = derNext(&certs, &tag, &v, &vl);
            if (!whole || tag != 0x30) return -1;
            *cert = cstart;
            *certLen = whole;
            return 0;
        }
    }
    return -1;
}

static uint64_t le64(const uint8_t *p) {
    uint64_t v = 0;
    int i;
    for (i = 7; i >= 0; i--) v = (v << 8) | p[i];
    return v;
}
static uint32_t le32(const uint8_t *p) { return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24); }

/* APK Signature Scheme v2/v3 block: signers[0] → signed data →
 * certificates[0]. cdOffset is the central directory's file offset. */
static uint8_t *v2FirstCert(ApakZip *apk, size_t *certLen) {
    uint8_t eocd[22], foot[24], *block = NULL, *out = NULL;
    uint64_t fileSize, cdOffset, blockSize, at;
    long i;
    const ApakZipEntry *last;
    (void)last;
    /* the end of central directory sits at the very end of an APK (APKs carry no comment) */
    {
        struct stat st;
        if (stat(apak_zipPath(apk), &st) != 0) return NULL;
        fileSize = (uint64_t)st.st_size;
    }
    if (fileSize < 22 || apak_zipPread(apk, eocd, 22, fileSize - 22) != 22 || le32(eocd) != 0x06054b50u) return NULL;
    cdOffset = le32(eocd + 16);
    if (cdOffset < 24 || apak_zipPread(apk, foot, 24, cdOffset - 24) != 24 || memcmp(foot + 8, "APK Sig Block 42", 16) != 0)
        return NULL;
    blockSize = le64(foot);
    if (blockSize < 32 || blockSize > (16u << 20) || blockSize + 8 > cdOffset) return NULL;
    block = (uint8_t *)malloc((size_t)blockSize);
    if (!block) return NULL;
    at = cdOffset - blockSize; /* the pairs, after the leading size field */
    if (apak_zipPread(apk, block, (size_t)blockSize, at) != (long)blockSize) goto done;
    for (i = 0; (uint64_t)i + 12 <= blockSize - 24;) {
        uint64_t plen = le64(block + i);
        uint32_t id = le32(block + i + 8);
        const uint8_t *val = block + i + 12;
        if (plen < 4 || (uint64_t)i + 8 + plen > blockSize - 24) break;
        if (id == 0x7109871au || id == 0xf05368c0u) { /* v2, v3 */
            /* signers → signer → signed data → digests (skipped) →
             * certificates → certificate #0; every level length-prefixed */
            const uint8_t *p = val, *end = val + (plen - 4);
            uint32_t digestsLen, cLen;
            if (p + 16 > end) break;
            p += 12; /* signers length, signer length, signed-data length */
            digestsLen = le32(p);
            p += 4;
            if (digestsLen > (uint32_t)(end - p) || (uint32_t)(end - p) - digestsLen < 8) break;
            p += digestsLen + 4; /* certificates length */
            cLen = le32(p);
            p += 4;
            if (cLen <= (uint32_t)(end - p) && cLen > 0 && cLen < (1u << 20)) {
                out = (uint8_t *)malloc(cLen);
                if (out) {
                    memcpy(out, p, cLen);
                    *certLen = cLen;
                }
            }
            break;
        }
        i += (long)(8 + plen);
    }
done:
    free(block);
    return out;
}

/* Signature.hashCode() == Arrays.hashCode(certificate DER). 0 = unknown. */
static int32_t signatureHash(void) {
    static int done;
    static int32_t hash;
    ApakZip *apk = android_apkArchive();
    uint8_t *der = NULL;
    const uint8_t *cert = NULL;
    size_t certLen = 0, i;
    char err[120];
    if (done) return hash;
    done = 1;
    if (!apk) return 0;
    for (i = 0; i < apak_zipCount(apk) && !cert; i++) {
        const ApakZipEntry *e = apak_zipAt(apk, i);
        size_t n = e->nameLen, len = 0;
        if (strncmp(e->name, "META-INF/", 9) != 0 || n < 13) continue;
        if (strcmp(e->name + n - 4, ".RSA") != 0 && strcmp(e->name + n - 4, ".DSA") != 0 &&
            strcmp(e->name + n - 3, ".EC") != 0)
            continue;
        der = (uint8_t *)apak_zipReadAll(apk, e, 1u << 20, &len, err, sizeof err);
        if (der && pkcs7FirstCert(der, len, &cert, &certLen) != 0) {
            free(der);
            der = NULL;
            cert = NULL;
        }
    }
    if (!cert) {
        der = v2FirstCert(apk, &certLen);
        cert = der;
    }
    if (cert) {
        uint32_t h = 1;
        for (i = 0; i < certLen; i++) h = 31 * h + (uint32_t)(int32_t)(int8_t)cert[i];
        hash = (int32_t)h;
        fprintf(stderr, "[androidrt][jni] minecraft: APK signing certificate hash 0x%08x (%zu-byte certificate)\n",
                (unsigned)hash, certLen);
    } else {
        fprintf(stderr, "[androidrt][jni] minecraft: no signing certificate found in the APK — reporting 0\n");
    }
    free(der);
    return hash;
}

/* ── MainActivity ──────────────────────────────────────────────────── */

IMPL(ma_androidVersion) { RETI(cfg()->sdkVersion); }
IMPL(ma_deviceModel) {
    char b[160];
    snprintf(b, sizeof b, "TrimUI %s", cfg()->model);
    RETS(b);
}
IMPL(ma_locale) {
    char loc[32];
    apak_jniLocale(loc, sizeof loc);
    RETS(loc);
}
IMPL(ma_external) { RETS(cfg()->externalDir); }
IMPL(ma_internal) { RETS(cfg()->dataRoot); }
IMPL(ma_true) { RETZ(1); }
IMPL(ma_false) { RETZ(0); }
IMPL(ma_noop) { (void)c; }
IMPL(ma_ppmm) { RETF(cfg()->dpi / 25.4f); }
IMPL(ma_width) { RETI(cfg()->panelW); }
IMPL(ma_height) { RETI(cfg()->panelH); }
IMPL(ma_zero) { RETI(0); }
IMPL(ma_requestStorage) {
    (void)c;
    apak_jniLogOnce("mc-storage", "minecraft: storage permission requested — the data folder is always writable here");
}
IMPL(ma_showKeyboard) {
    const char *t = ARGS(0);
    snprintf(g_textbox, sizeof g_textbox, "%s", t ? t : "");
    apak_jniLogOnce("mc-keyboard", "minecraft: the game asked for the on-screen keyboard — text input is not "
                                   "available in AndroidRT yet (follow-up: JukaHub's keyboard)");
}
IMPL(ma_updateText) {
    const char *t = ARGS(0);
    snprintf(g_textbox, sizeof g_textbox, "%s", t ? t : "");
}
IMPL(ma_cursor) { RETI((int32_t)strlen(g_textbox)); }
IMPL(ma_setClipboard) {
    const char *t = ARGS(0);
    snprintf(g_clipboard, sizeof g_clipboard, "%s", t ? t : "");
}
IMPL(ma_getClipboard) { RETS(g_clipboard); }
IMPL(ma_launchUri) {
    const char *u = ARGS(0);
    fprintf(stderr, "[androidrt][jni] minecraft: the game asked to open %s — there is no browser on the handheld\n",
            u ? u : "(null)");
}
IMPL(ma_share) {
    (void)c;
    apak_jniLogOnce("mc-share", "minecraft: share sheet requested — no other apps here; ignored");
}
IMPL(ma_fileDataBytes) {
    const char *path = ARGS(0);
    void *asset;
    unsigned char *buf;
    long len;
    if (!path) return;
    if (path[0] == '/') {
        FILE *f = fopen(path, "rb");
        if (!f) return;
        if (fseek(f, 0, SEEK_END) == 0 && (len = ftell(f)) >= 0 && len < (256L << 20) && fseek(f, 0, SEEK_SET) == 0) {
            buf = (unsigned char *)malloc(len ? (size_t)len : 1);
            if (buf && fread(buf, 1, (size_t)len, f) == (size_t)len) RETO(apak_jniNewByteArray(buf, (size_t)len));
            free(buf);
        }
        fclose(f);
        return;
    }
    asset = AAssetManager_open(AAssetManager_fromJava(c->env, NULL), path, 2 /* AASSET_MODE_BUFFER */);
    if (!asset) return; /* MainActivity.getFileDataBytes answers null for a missing file */
    len = (long)AAsset_getLength(asset);
    buf = (unsigned char *)malloc(len > 0 ? (size_t)len : 1);
    if (buf && AAsset_read(asset, buf, (size_t)len) == (int)len) RETO(apak_jniNewByteArray(buf, (size_t)len));
    free(buf);
    AAsset_close(asset);
}
IMPL(ma_totalMemory) { RETJ(apak_jniMemInfo("MemTotal") > 0 ? apak_jniMemInfo("MemTotal") : 0); }
IMPL(ma_usedMemory) { RETJ(apak_jniProcessRss() > 0 ? apak_jniProcessRss() : 0); }
IMPL(ma_freeMemory) { RETJ(apak_jniMemInfo("MemAvailable") > 0 ? apak_jniMemInfo("MemAvailable") : 0); }
IMPL(ma_platformString) {
    char b[160];
    if (c->args[0].i == 0) {
        snprintf(b, sizeof b, "TrimUI %s", cfg()->model);
        RETS(b);
    } else {
        RETS("");
    }
}
IMPL(ma_dateString) {
    time_t t = (time_t)c->args[0].i;
    struct tm tmv;
    char b[64];
#if defined(_WIN32)
    tmv = *localtime(&t);
#else
    localtime_r(&t, &tmv);
#endif
    strftime(b, sizeof b, "%Y-%m-%d %H:%M", &tmv);
    RETS(b);
}
IMPL(ma_uuid) {
    char u[37];
    uuid4(u);
    RETS(u);
}
IMPL(ma_network) { RETZ(apak_jniNetworkUp()); }
IMPL(ma_deviceId) { RETS(deviceId()); }
IMPL(ma_secureGet) {
    char v[2048];
    secureGet(ARGS(0), v, sizeof v);
    if (v[0]) RETS(v);
}
IMPL(ma_secureSet) { secureSet(ARGS(0), ARGS(1)); }
IMPL(ma_quit) {
    (void)c;
    fprintf(stderr, "[androidrt][jni] minecraft: quit requested by the game\n");
    apak_jniFinish();
}
IMPL(ma_empty) { RETS(""); }
IMPL(ma_xboxInit) {
    (void)c;
    apak_jniLogOnce("mc-xbox", "minecraft: initializeXboxLive — Xbox Live is unavailable on AndroidRT; sign-in cannot "
                               "complete and the game stays signed out");
}
IMPL(ma_apiVersion) { RETI(cfg()->sdkVersion); }
IMPL(ma_diskFree) {
#if !defined(_WIN32)
    struct statvfs v;
    const char *p = ARGS(0);
    if (statvfs(p && *p ? p : cfg()->dataRoot, &v) == 0 || statvfs(cfg()->dataRoot, &v) == 0)
        RETJ((int64_t)v.f_bavail * (int64_t)v.f_frsize);
#else
    (void)c;
#endif
}
static void pickCanceled(void *arg) {
    ApakJValue a[1];
    a[0].j = (int64_t)(intptr_t)arg;
    apak_jniCallNative(apak_jniMainEnv(), apak_jniActivity(), MAIN_ACTIVITY, "nativeOnPickImageCanceled", "(J)V", a, NULL);
}
IMPL(ma_pickImage) {
    fprintf(stderr, "[androidrt][jni] minecraft: image picker requested — none on the handheld, answering canceled\n");
    if (apak_jniHasNative(MAIN_ACTIVITY, "nativeOnPickImageCanceled", "(J)V"))
        apak_jniPost(pickCanceled, (void *)(intptr_t)c->args[0].j);
}

/* ── HardwareInformation ──────────────────────────────────────────── */

IMPL(hw_model) {
    char b[160];
    snprintf(b, sizeof b, "TrimUI %s", cfg()->model);
    RETS(b);
}
IMPL(hw_android) { RETS("9"); }
IMPL(hw_cpuType) {
#if defined(__aarch64__)
    RETS("arm64-v8a");
#elif defined(__x86_64__)
    RETS("x86_64");
#else
    RETS("unknown");
#endif
}
IMPL(hw_cpuName) {
    char b[256];
    cpuinfo("Hardware", b, sizeof b);
    if (!b[0]) cpuinfo("model name", b, sizeof b);
    RETS(b[0] ? b : "unknown");
}
IMPL(hw_cpuFeatures) {
    char b[512];
    cpuinfo("Features", b, sizeof b);
    if (!b[0]) cpuinfo("flags", b, sizeof b);
    RETS(b);
}
IMPL(hw_cores) { RETI(cpuCount()); }
IMPL(hw_serial) { RETS("unknown"); /* Android 9 answers "unknown" without READ_PHONE_STATE */ }
IMPL(hw_board) { RETS(cfg()->board); }
IMPL(hw_installer) { (void)c; /* null: not installed by a store */ }
IMPL(hw_signature) { RETI(signatureHash()); }
IMPL(hw_rooted) {
#if !defined(_WIN32)
    RETZ(getuid() == 0);
#else
    RETZ(0);
#endif
}

/* ── NetworkMonitor ───────────────────────────────────────────────── */

static void networkStatus(void *arg) {
    ApakJObject *mon = (ApakJObject *)arg;
    const char *sig = apak_jniNativeSig("com/mojang/minecraftpe/NetworkMonitor", "nativeUpdateNetworkStatus");
    int up = apak_jniNetworkUp();
    if (strcmp(sig, "(ZZZ)V") == 0) {
        ApakJValue a[3];
        memset(a, 0, sizeof a);
        a[0].z = (uint8_t)up; /* connected      */
        a[1].z = (uint8_t)up; /* Wi-Fi: the only network these handhelds have */
        a[2].z = 0;           /* cellular       */
        apak_jniCallNative(apak_jniMainEnv(), mon, "com/mojang/minecraftpe/NetworkMonitor", "nativeUpdateNetworkStatus",
                           sig, a, NULL);
    } else if (sig[0]) {
        apak_jniLogOnce("mc-netsig", "minecraft: NetworkMonitor.nativeUpdateNetworkStatus is %s — not called", sig);
    }
    apak_jniRelease(mon);
}
IMPL(net_init) {
    ApakJValue v;
    v.j = 0;
    v.j = c->args[1].j;
    apak_jniFieldSet(c->self, "mNativePtr", v);
    apak_jniPost(networkStatus, apak_jniRetain(c->self));
}

/* ── the store: never available, never pretends ───────────────────── */

typedef struct {
    ApakJObject *listener;
    int amazon;
} StoreState;

static void storeFree(void *p) {
    StoreState *s = (StoreState *)p;
    if (s && s->listener) apak_jniRelease(s->listener);
    free(s);
}

static int64_t listenerPtr(ApakJObject *listener) {
    ApakJValue v;
    if (listener && apak_jniFieldGet(listener, "mStoreListener", &v) == 0) return v.j;
    return 0;
}

/* Tell the listener with whichever failure callback the game registered. */
static void listenerFail(ApakJObject *listener, const char *name) {
    const char *sig = apak_jniNativeSig(STORE_LISTENER, name);
    ApakJValue a[2];
    char key[96];
    memset(a, 0, sizeof a);
    a[0].j = listenerPtr(listener);
    if (strcmp(sig, "(J)V") == 0) {
        apak_jniCallNative(apak_jniMainEnv(), listener, STORE_LISTENER, name, sig, a, NULL);
    } else if (strcmp(sig, "(JZ)V") == 0) {
        a[1].z = 0;
        apak_jniCallNative(apak_jniMainEnv(), listener, STORE_LISTENER, name, sig, a, NULL);
    } else if (strcmp(sig, "(JLjava/lang/String;)V") == 0) {
        a[1].l = apak_jniNewString("");
        apak_jniCallNative(apak_jniMainEnv(), listener, STORE_LISTENER, name, sig, a, NULL);
    } else {
        snprintf(key, sizeof key, "mc-store-%s", name);
        apak_jniLogOnce(key, "minecraft: store callback %s %s — not called (the game waits or times out)", name,
                        sig[0] ? sig : "is not registered");
    }
}

typedef struct {
    ApakJObject *listener;
    const char *callback;
} StoreTask;

static void storeTask(void *arg) {
    StoreTask *t = (StoreTask *)arg;
    listenerFail(t->listener, t->callback);
    apak_jniRelease(t->listener);
    free(t);
}

static void postFail(ApakJObject *store, const char *callback) {
    StoreState *s = (StoreState *)apak_jniPayload(store);
    StoreTask *t;
    if (!s || !s->listener) return;
    t = (StoreTask *)malloc(sizeof *t);
    if (!t) return;
    t->listener = apak_jniRetain(s->listener);
    t->callback = callback;
    apak_jniPost(storeTask, t);
}

static ApakJObject *newStore(ApakJObject *listener, int amazon) {
    ApakJObject *store = apak_jniNewInstance("com/mojang/minecraftpe/store/Store");
    StoreState *s;
    if (!store) return NULL;
    s = (StoreState *)calloc(1, sizeof *s);
    if (s) {
        s->listener = listener ? apak_jniRetain(listener) : NULL;
        s->amazon = amazon;
    }
    apak_jniSetPayload(store, s, storeFree);
    fprintf(stderr, "[androidrt][jni] minecraft: %s store requested — the store is unavailable on AndroidRT "
                    "(no purchases, no entitlements, no license check)\n", amazon ? "Amazon" : "Google Play");
    postFail(store, "onStoreInitialized");
    return store;
}

IMPL(sf_googlePlay) { RETO(newStore(ARG(1), 0)); }
IMPL(sf_amazon) { RETO(newStore(ARG(0), 1)); }
IMPL(st_id) {
    StoreState *s = (StoreState *)apak_jniPayload(c->self);
    RETS(s && s->amazon ? "android.amazonappstore" : "android.googleplay");
}
IMPL(st_empty) { RETS(""); }
IMPL(st_false) { RETZ(0); }
IMPL(st_extra) { RETO(apak_jniNewInstance("com/mojang/minecraftpe/store/ExtraLicenseResponseData")); }
IMPL(st_queryProducts) { postFail(c->self, "onQueryProductsFail"); }
IMPL(st_queryPurchases) { postFail(c->self, "onQueryPurchasesFail"); }
IMPL(st_purchase) {
    const char *sku = ARGS(0);
    fprintf(stderr, "[androidrt][jni] minecraft: purchase of %s refused — no store on AndroidRT\n", sku ? sku : "?");
    postFail(c->self, "onPurchaseFailed");
}
IMPL(st_noop) { (void)c; }
IMPL(el_zero) { RETJ(0); }
IMPL(nsl_init) {
    ApakJValue v;
    v.j = c->args[0].j;
    apak_jniFieldSet(c->self, "mStoreListener", v);
}

/* ── FMOD's Java helper ───────────────────────────────────────────── */

IMPL(fmod_true) { RETZ(1); }
IMPL(fmod_false) { RETZ(0); }
IMPL(fmod_rate) { RETI(48000); }
IMPL(fmod_block) { RETI(256); }
IMPL(fmod_assets) {
    ApakJValue r;
    apak_jniCallMethod(c->env, apak_jniActivity(), "getAssets", "()Landroid/content/res/AssetManager;", NULL, &r);
    RETO((ApakJObject *)r.l);
}

/* ── Xbox Live helper (pre-XAL builds): storage and config, no sign-in ─ */

IMPL(xbl_storage) { RETS(cfg()->filesDir); }
IMPL(xbl_locale) {
    char loc[32];
    char *u;
    apak_jniLocale(loc, sizeof loc);
    u = strchr(loc, '_');
    if (u) *u = '-';
    RETS(loc);
}
IMPL(xbl_config) {
    ApakZip *apk = android_apkArchive();
    const ApakZipEntry *e = apk ? apak_zipFind(apk, "res/raw/xboxservices.config") : NULL;
    char err[120];
    size_t len = 0;
    char *text = e ? (char *)apak_zipReadAll(apk, e, 1u << 20, &len, err, sizeof err) : NULL;
    RETS(text ? text : "");
    free(text);
}

/* ── the table ────────────────────────────────────────────────────── */

#define M(n, s, f) { n, s, 0, f }
#define S(n, s, f) { n, s, 1, f }
#define END { NULL, NULL, 0, NULL }

static const ApakJMethodDef kMainActivity[] = {
    M("getAndroidVersion", "()I", ma_androidVersion), M("getDeviceModel", "()Ljava/lang/String;", ma_deviceModel),
    M("getLocale", "()Ljava/lang/String;", ma_locale), M("getExternalStoragePath", "()Ljava/lang/String;", ma_external),
    M("getInternalStoragePath", "()Ljava/lang/String;", ma_internal),
    M("hasWriteExternalStoragePermission", "()Z", ma_true), M("requestStoragePermission", "(I)V", ma_requestStorage),
    M("isTablet", "()Z", ma_false), M("getPixelsPerMillimeter", "()F", ma_ppmm), M("getScreenWidth", "()I", ma_width),
    M("getScreenHeight", "()I", ma_height), M("getDisplayWidth", "()I", ma_width),
    M("getDisplayHeight", "()I", ma_height), M("getKeyboardHeight", "()I", ma_zero),
    M("showKeyboard", "(Ljava/lang/String;IZZZ)V", ma_showKeyboard), M("hideKeyboard", "()V", ma_noop),
    M("updateTextboxText", "(Ljava/lang/String;)V", ma_updateText), M("getCursorPosition", "()I", ma_cursor),
    M("setClipboard", "(Ljava/lang/String;)V", ma_setClipboard),
    M("getClipboardText", "()Ljava/lang/String;", ma_getClipboard),
    M("launchUri", "(Ljava/lang/String;)V", ma_launchUri), M("openURL", "(Ljava/lang/String;)V", ma_launchUri),
    M("share", "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;)V", ma_share),
    M("getFileDataBytes", "(Ljava/lang/String;)[B", ma_fileDataBytes), M("getTotalMemory", "()J", ma_totalMemory),
    M("getMemoryLimit", "()J", ma_totalMemory), M("getUsedMemory", "()J", ma_usedMemory),
    M("getFreeMemory", "()J", ma_freeMemory), M("getAvailableMemory", "()J", ma_freeMemory),
    M("getPlatformStringVar", "(I)Ljava/lang/String;", ma_platformString),
    M("getDateString", "(I)Ljava/lang/String;", ma_dateString), M("createUUID", "()Ljava/lang/String;", ma_uuid),
    M("isFirstSnooperStart", "()Z", ma_false), M("hasHardwareChanged", "()Z", ma_false),
    M("isNetworkEnabled", "(Z)Z", ma_network), M("isOnWifi", "()Z", ma_network),
    M("getDeviceId", "()Ljava/lang/String;", ma_deviceId), M("getLegacyDeviceID", "()Ljava/lang/String;", ma_deviceId),
    M("getSecureStorageKey", "(Ljava/lang/String;)Ljava/lang/String;", ma_secureGet),
    M("setSecureStorageKey", "(Ljava/lang/String;Ljava/lang/String;)V", ma_secureSet), M("tick", "()V", ma_noop),
    M("quit", "()V", ma_quit), M("getAccessToken", "()Ljava/lang/String;", ma_empty),
    M("getClientId", "()Ljava/lang/String;", ma_empty), M("getProfileId", "()Ljava/lang/String;", ma_empty),
    M("getProfileName", "()Ljava/lang/String;", ma_empty), M("setSession", "(Ljava/lang/String;)V", ma_noop),
    M("setRefreshToken", "(Ljava/lang/String;)V", ma_noop), M("initializeXboxLive", "(JJ)V", ma_xboxInit),
    M("getAPIVersion", "(Ljava/lang/String;)I", ma_apiVersion),
    M("calculateAvailableDiskFreeSpace", "(Ljava/lang/String;)J", ma_diskFree), M("vibrate", "(I)V", ma_noop),
    M("lockCursor", "()V", ma_noop), M("unlockCursor", "()V", ma_noop), M("isChromebook", "()Z", ma_false),
    M("pickImage", "(J)V", ma_pickImage), END,
};

/* Both forms: the game reaches these statically or through an instance. */
#define HW(n, s, f) M(n, s, f), S(n, s, f)
static const ApakJMethodDef kHardware[] = {
    M("<init>", "(Landroid/content/Context;)V", ma_noop), HW("getDeviceModelName", "()Ljava/lang/String;", hw_model),
    HW("getAndroidVersion", "()Ljava/lang/String;", hw_android), HW("getCPUType", "()Ljava/lang/String;", hw_cpuType),
    HW("getCPUName", "()Ljava/lang/String;", hw_cpuName), HW("getCPUFeatures", "()Ljava/lang/String;", hw_cpuFeatures),
    HW("getNumCores", "()I", hw_cores), HW("getSecureId", "()Ljava/lang/String;", ma_deviceId),
    HW("getSerialNumber", "()Ljava/lang/String;", hw_serial), HW("getBoard", "()Ljava/lang/String;", hw_board),
    HW("getInstallerPackageName", "()Ljava/lang/String;", hw_installer),
    HW("getSignaturesHashCode", "()I", hw_signature), HW("getIsRooted", "()Z", hw_rooted), END,
};
static const ApakJFieldDef kNetFields[] = { { "mNativePtr", "J", 0 }, { NULL, NULL, 0 } };
static const ApakJMethodDef kNetwork[] = { M("<init>", "(Landroid/content/Context;J)V", net_init), END };
static const ApakJMethodDef kStoreFactory[] = {
    S("createGooglePlayStore", "(Ljava/lang/String;Lcom/mojang/minecraftpe/store/StoreListener;)"
                               "Lcom/mojang/minecraftpe/store/Store;", sf_googlePlay),
    S("createAmazonAppStore", "(Lcom/mojang/minecraftpe/store/StoreListener;Z)Lcom/mojang/minecraftpe/store/Store;",
      sf_amazon), END,
};
static const ApakJMethodDef kStore[] = {
    M("getStoreId", "()Ljava/lang/String;", st_id), M("getProductSkuPrefix", "()Ljava/lang/String;", st_empty),
    M("getRealmsSkuPrefix", "()Ljava/lang/String;", st_empty), M("hasVerifiedLicense", "()Z", st_false),
    M("receivedLicenseResponse", "()Z", st_false),
    M("getExtraLicenseData", "()Lcom/mojang/minecraftpe/store/ExtraLicenseResponseData;", st_extra),
    M("queryProducts", "([Ljava/lang/String;)V", st_queryProducts),
    M("purchase", "(Ljava/lang/String;ZLjava/lang/String;)V", st_purchase),
    M("acknowledgePurchase", "(Ljava/lang/String;Ljava/lang/String;)V", st_noop),
    M("queryPurchases", "()V", st_queryPurchases), M("restorePurchases", "()V", st_queryPurchases),
    M("purchaseGame", "()V", st_purchase), M("destructor", "()V", st_noop), END,
};
static const ApakJMethodDef kExtraLicense[] = {
    M("getValidationTime", "()J", el_zero), M("getRetryUntilTime", "()J", el_zero),
    M("getRetryAttempts", "()J", el_zero), END,
};
static const ApakJFieldDef kListenerFields[] = { { "mStoreListener", "J", 0 }, { NULL, NULL, 0 } };
static const ApakJMethodDef kListener[] = { M("<init>", "(J)V", nsl_init), END };
static const ApakJMethodDef kFmod[] = {
    S("checkInit", "()Z", fmod_true), S("supportsLowLatency", "()Z", fmod_false),
    S("supportsAAudio", "()Z", fmod_false), S("getOutputSampleRate", "()I", fmod_rate),
    S("getOutputBlockSize", "()I", fmod_block), S("isBluetoothOn", "()Z", fmod_false),
    S("getAssetManager", "()Landroid/content/res/AssetManager;", fmod_assets),
    S("init", "(Landroid/content/Context;)V", ma_noop), S("close", "()V", ma_noop), END,
};
static const ApakJMethodDef kXblInterop[] = {
    S("GetLocalStoragePath", "(Landroid/content/Context;)Ljava/lang/String;", xbl_storage),
    S("getLocalStoragePath", "(Landroid/content/Context;)Ljava/lang/String;", xbl_storage),
    S("getSystemProxy", "()Ljava/lang/String;", ma_empty), S("getLocale", "()Ljava/lang/String;", xbl_locale),
    S("ReadConfigFile", "(Landroid/content/Context;)Ljava/lang/String;", xbl_config), END,
};

static void define(const ApakJniConfig *k) {
    const char *super = NULL;
    ApakJClassDef defs[] = {
        { MAIN_ACTIVITY, NULL, kMainActivity, NULL },
        { "com/mojang/minecraftpe/HardwareInformation", "java/lang/Object", kHardware, NULL },
        { "com/mojang/minecraftpe/NetworkMonitor", "java/lang/Object", kNetwork, kNetFields },
        { "com/mojang/minecraftpe/store/StoreFactory", "java/lang/Object", kStoreFactory, NULL },
        { "com/mojang/minecraftpe/store/Store", "java/lang/Object", kStore, NULL },
        { "com/mojang/minecraftpe/store/StoreListener", "java/lang/Object", NULL, NULL },
        { STORE_LISTENER, "com/mojang/minecraftpe/store/StoreListener", kListener, kListenerFields },
        { "com/mojang/minecraftpe/store/ExtraLicenseResponseData", "java/lang/Object", kExtraLicense, NULL },
        { "org/fmod/FMOD", "java/lang/Object", kFmod, NULL },
        { "com/microsoft/xbox/idp/interop/Interop", "java/lang/Object", kXblInterop, NULL },
        { NULL, NULL, NULL, NULL },
    };
    /* MainActivity extends whatever the APK says (NativeActivity before
     * 1.21.80, GameActivity since); without a DEX the entry point decides. */
    if (!(k->classInApk && k->classInApk(MAIN_ACTIVITY, &super) && super))
        super = k->gameActivity ? "com/google/androidgamesdk/GameActivity" : "android/app/NativeActivity";
    defs[0].super = super;
    apak_jniDefineAll(defs);
    fprintf(stderr, "[androidrt][jni] minecraft: %s%s%s: MainActivity extends %s; store, license and Xbox Live "
                    "answer as unavailable\n", k->packageName, k->versionName && *k->versionName ? " " : "",
            k->versionName ? k->versionName : "", super);
}

static void event(int ev) {
    if (ev == APAK_JNI_EV_BEFORE_ONCREATE) {
        /* MainActivity.onCreate registers itself with the native side
         * before NativeActivity's own onCreate runs the native entry. */
        apak_jniCallNative(apak_jniMainEnv(), apak_jniActivity(), MAIN_ACTIVITY, "nativeRegisterThis", "()V", NULL,
                           NULL);
    } else if (ev == APAK_JNI_EV_DESTROY) {
        if (apak_jniHasNative(MAIN_ACTIVITY, "nativeUnregisterThis", "()V"))
            apak_jniCallNative(apak_jniMainEnv(), apak_jniActivity(), MAIN_ACTIVITY, "nativeUnregisterThis", "()V",
                               NULL, NULL);
    }
}

static const char *const kPackages[] = { "com.mojang.minecraftpe", "com.mojang.minecrafttrialpe", NULL };

const ApakJniPack apak_packMinecraft = {
    "minecraft", kPackages, define, MAIN_ACTIVITY, event,
};
