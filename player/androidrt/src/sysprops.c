/* sysprops.c — __system_property_* for bionic callers.
 *
 * Android games read build properties to pick code paths (SDK level,
 * device model, GLES version, ABI). There is no property service here, so
 * this is an in-process table: Smart Pro defaults that describe the device
 * honestly as an Android 9 (SDK 28) arm64 handheld with GLES 3.2, then
 * `key=value` lines from $ANDROIDPAK_PROPS or <AndroidRT>/props.txt (the
 * ANDROIDPAK_RT directory), then __system_property_set calls (process-local).
 */
#define _GNU_SOURCE
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "bioniccompat.h"

#define PROP_NAME_MAX  32
#define PROP_VALUE_MAX 92
#define APAK_MAX_PROPS 256

typedef struct ApakProp {
    char name[96];
    char value[PROP_VALUE_MAX];
    uint32_t serial;
} ApakProp;

static ApakProp g_props[APAK_MAX_PROPS];
static int g_nprops;
static pthread_mutex_t g_propLock = PTHREAD_MUTEX_INITIALIZER;
static int g_propsReady;

#if defined(__aarch64__)
#define APAK_ABI "arm64-v8a"
#elif defined(__x86_64__)
#define APAK_ABI "x86_64"
#else
#define APAK_ABI "unknown"
#endif

static const char *const kDefaults[][2] = {
    { "ro.build.version.sdk", "28" },
    { "ro.build.version.release", "9" },
    { "ro.build.version.codename", "REL" },
    { "ro.build.version.incremental", "androidrt" },
    { "ro.build.version.security_patch", "2019-08-05" },
    { "ro.build.id", "JUKAHUB" },
    { "ro.build.display.id", "JukaHub AndroidRT" },
    { "ro.build.type", "user" },
    { "ro.build.tags", "release-keys" },
    { "ro.build.fingerprint", "TrimUI/tg5040/tg5040:9/JUKAHUB/1:user/release-keys" },
    { "ro.build.characteristics", "default" },
    { "ro.product.manufacturer", "TrimUI" },
    { "ro.product.brand", "TrimUI" },
    { "ro.product.model", "Smart Pro" },
    { "ro.product.name", "tg5040" },
    { "ro.product.device", "tg5040" },
    { "ro.product.board", "tg5040" },
    { "ro.board.platform", "sun50iw10" },
    { "ro.hardware", "sun50iw10p1" },
    { "ro.arch", "arm64" },
    { "ro.product.cpu.abi", APAK_ABI },
    { "ro.product.cpu.abilist", APAK_ABI },
    { "ro.product.cpu.abilist64", APAK_ABI },
    { "ro.product.cpu.abilist32", "" },
    { "ro.product.locale", "en-US" },
    { "ro.product.locale.language", "en" },
    { "ro.product.locale.region", "US" },
    { "persist.sys.locale", "en-US" },
    { "persist.sys.language", "en" },
    { "persist.sys.country", "US" },
    { "persist.sys.timezone", "UTC" },
    { "ro.sf.lcd_density", "320" },
    { "ro.opengles.version", "196610" }, /* GLES 3.2 (0x00030002), the GE8300 */
    { "ro.kernel.qemu", "0" },
    { "ro.debuggable", "0" },
    { "ro.secure", "1" },
    { "ro.adb.secure", "1" },
    { "ro.serialno", "ANDROIDRT0001" },
    { "ro.boot.serialno", "ANDROIDRT0001" },
    { "ro.config.low_ram", "false" },
    { "ro.zygote", "zygote64" },
    { "dalvik.vm.heapsize", "256m" },
    { "dalvik.vm.heapgrowthlimit", "128m" },
    { "sys.boot_completed", "1" },
    { "init.svc.bootanim", "stopped" },
    { NULL, NULL },
};

static ApakProp *findLocked(const char *name) {
    int i;
    for (i = 0; i < g_nprops; i++)
        if (strcmp(g_props[i].name, name) == 0) return &g_props[i];
    return NULL;
}

static int setLocked(const char *name, const char *value) {
    ApakProp *p;
    if (!name || !*name || strlen(name) >= sizeof g_props[0].name || !value || strlen(value) >= PROP_VALUE_MAX)
        return -1;
    p = findLocked(name);
    if (!p) {
        if (g_nprops >= APAK_MAX_PROPS) return -1;
        p = &g_props[g_nprops++];
        snprintf(p->name, sizeof p->name, "%s", name);
    }
    snprintf(p->value, sizeof p->value, "%s", value);
    p->serial += 2; /* bionic serials are even when not being written */
    return 0;
}

static void loadFile(const char *path) {
    FILE *f;
    char line[256];
    if (!path || !*path) return;
    f = fopen(path, "r");
    if (!f) return;
    while (fgets(line, sizeof line, f)) {
        char *eq, *end, *key = line;
        while (*key == ' ' || *key == '\t') key++;
        if (*key == '#' || *key == '\n' || *key == '\0') continue;
        eq = strchr(key, '=');
        if (!eq) continue;
        *eq = '\0';
        end = eq;
        while (end > key && (end[-1] == ' ' || end[-1] == '\t')) *--end = '\0';
        end = eq + 1 + strlen(eq + 1);
        while (end > eq + 1 && (end[-1] == '\n' || end[-1] == '\r' || end[-1] == ' ')) *--end = '\0';
        if (setLocked(key, eq + 1) != 0)
            fprintf(stderr, "[androidrt] props: ignored %s (name or value too long)\n", key);
    }
    fclose(f);
}

void apak_propsInit(void) {
    int i;
    pthread_mutex_lock(&g_propLock);
    if (!g_propsReady) {
        const char *file = getenv("ANDROIDPAK_PROPS");
        const char *rt = getenv("ANDROIDPAK_RT");
        for (i = 0; kDefaults[i][0]; i++) setLocked(kDefaults[i][0], kDefaults[i][1]);
        if (rt && *rt) {
            char path[1024];
            snprintf(path, sizeof path, "%s/props.txt", rt);
            loadFile(path);
        }
        loadFile(file);
        g_propsReady = 1;
    }
    pthread_mutex_unlock(&g_propLock);
}

int apak_propGet(const char *name, char *value, size_t cap) {
    ApakProp *p;
    int len = 0;
    if (!value || !cap) return 0;
    value[0] = '\0';
    if (!name) return 0;
    pthread_mutex_lock(&g_propLock);
    p = findLocked(name);
    if (p) {
        snprintf(value, cap, "%s", p->value);
        len = (int)strlen(value);
    }
    pthread_mutex_unlock(&g_propLock);
    return len;
}

/* ── the bionic ABI ─────────────────────────────────────────────────── */

static int bw___system_property_get(const char *name, char *value) {
    return apak_propGet(name, value, PROP_VALUE_MAX);
}

static const void *bw___system_property_find(const char *name) {
    ApakProp *p;
    if (!name) return NULL;
    pthread_mutex_lock(&g_propLock);
    p = findLocked(name);
    pthread_mutex_unlock(&g_propLock);
    return p;
}

static const void *bw___system_property_find_nth(unsigned n) {
    return (int)n < g_nprops ? &g_props[n] : NULL;
}

static int bw___system_property_read(const void *pi, char *name, char *value) {
    const ApakProp *p = (const ApakProp *)pi;
    if (!p) return 0;
    if (name) apak_strcpy(name, PROP_NAME_MAX, p->name);
    if (value) apak_strcpy(value, PROP_VALUE_MAX, p->value);
    return (int)strlen(p->value);
}

static void bw___system_property_read_callback(const void *pi,
                                               void (*cb)(void *cookie, const char *name, const char *value,
                                                          uint32_t serial),
                                               void *cookie) {
    const ApakProp *p = (const ApakProp *)pi;
    char value[PROP_VALUE_MAX];
    char name[sizeof p->name];
    uint32_t serial;
    if (!p || !cb) return;
    pthread_mutex_lock(&g_propLock);
    snprintf(name, sizeof name, "%s", p->name);
    snprintf(value, sizeof value, "%s", p->value);
    serial = p->serial;
    pthread_mutex_unlock(&g_propLock);
    cb(cookie, name, value, serial);
}

static int bw___system_property_foreach(void (*fn)(const void *pi, void *cookie), void *cookie) {
    int i, n;
    if (!fn) return -1;
    pthread_mutex_lock(&g_propLock);
    n = g_nprops;
    pthread_mutex_unlock(&g_propLock);
    for (i = 0; i < n; i++) fn(&g_props[i], cookie);
    return 0;
}

static uint32_t bw___system_property_serial(const void *pi) {
    return pi ? ((const ApakProp *)pi)->serial : 0;
}

static uint32_t bw___system_property_area_serial(void) { return 2; }

static int bw___system_property_set(const char *name, const char *value) {
    int rc;
    pthread_mutex_lock(&g_propLock);
    rc = setLocked(name, value ? value : "");
    pthread_mutex_unlock(&g_propLock);
    return rc;
}

/* Nothing ever changes a property behind the game's back: a bounded wait
 * sleeps its timeout and reports "no change"; an unbounded one returns at
 * once instead of hanging the game forever. */
static int bw___system_property_wait(const void *pi, uint32_t oldSerial, uint32_t *newSerial,
                                     const struct timespec *relTimeout) {
    (void)pi;
    if (newSerial) *newSerial = oldSerial;
    if (relTimeout) nanosleep(relTimeout, NULL);
    return 0;
}

static uint32_t bw___system_property_wait_any(uint32_t oldSerial) { return oldSerial; }

#define SYM(name, fn) { name, (void *)(fn) }

const ApakSym apak_propFallbacks[] = {
    SYM("__system_property_get", bw___system_property_get),
    SYM("__system_property_find", bw___system_property_find),
    SYM("__system_property_find_nth", bw___system_property_find_nth),
    SYM("__system_property_read", bw___system_property_read),
    SYM("__system_property_read_callback", bw___system_property_read_callback),
    SYM("__system_property_foreach", bw___system_property_foreach),
    SYM("__system_property_serial", bw___system_property_serial),
    SYM("__system_property_area_serial", bw___system_property_area_serial),
    SYM("__system_property_set", bw___system_property_set),
    SYM("__system_property_wait", bw___system_property_wait),
    SYM("__system_property_wait_any", bw___system_property_wait_any),
    { NULL, NULL },
};
