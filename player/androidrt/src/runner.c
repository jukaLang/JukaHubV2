/* runner.c — the AndroidRT "loader" executable.
 *
 *   loader <apk> [args]            run a staged game (ANDROIDPAK_* env, set by
 *                                  the AndroidPak profile runtime)
 *   loader --scan <lib.so> [dir]   print the import report of a bionic library
 *                                  and its bundled dependencies, run nothing
 *                                  (exit 0 = everything resolves, 2 = undefined
 *                                  symbols listed, 1 = unreadable)
 *   loader --version               print one "AndroidRT loader ..." line and
 *                                  exit 0: JukaHub runs it on a downloaded
 *                                  runtime before installing it, to prove the
 *                                  binary starts on this device (CPU, glibc)
 *
 * Profile mode, Linux (the device):
 *   1. validates the ANDROIDPAK_* environment and prepares the persistent
 *      data directory (ANDROIDPAK_DATA: files/, external/, obb/, cache/);
 *   2. loads the game's main library with the runner's own bionic loader
 *      (bionicld.c) — every import bound through the compat resolver, the
 *      complete undefined-symbol list printed before any game code runs;
 *   3. builds the Java side the game expects (jni.h): the manifest and the
 *      DEX class index say which activity class it is and what it extends,
 *      class packs (jnipack_*.c) implement a game family's Java methods, and
 *      every loaded library's JNI_OnLoad runs, dependencies first — what
 *      System.loadLibrary would have done;
 *   4. resolves ANativeActivity_onCreate (or $ANDROIDPAK_ENTRY), searching
 *      the other staged libraries when the main one does not export it, or
 *      drives an androidx GameActivity game through its natives
 *      (gameactivity.c) when it has no NativeActivity entry;
 *   5. drives the lifecycle (activity.c) and _exit()s with its status —
 *      Android kills app processes, it never runs their static destructors.
 *
 * Windows smoke builds keep the original dlopen/LoadLibrary path (dlport.c):
 * there is no ELF to load there.
 */

#define _GNU_SOURCE
#include <dirent.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#if !defined(_WIN32)
#include <unistd.h>
#endif

#include "actbridge.h"
#include "dlport.h"

void apak_logInit(void);

#if defined(__linux__)
#define ANDROIDRT_BIONIC_LOADER 1
#include "apkmeta.h"
#include "apkzip.h"
#include "bioniccompat.h"
#include "bionicld.h"
#include "device.h"
#include "jni.h"
/* eglpresent.c */
void apak_presentInit(void);
void apak_presentShutdown(void);
/* class packs and the GameActivity bridge */
extern const ApakJniPack apak_packMinecraft;
void apak_gameActivityDefine(void);
#endif

/* What `loader --version` prints. A build may stamp its release with
 * -DANDROIDRT_VERSION='"v1.2"'; the CPU comes from the compiler. */
#ifndef ANDROIDRT_VERSION
#define ANDROIDRT_VERSION "dev"
#endif
#if defined(__aarch64__)
#define RUNNER_CPU "aarch64"
#elif defined(__x86_64__)
#define RUNNER_CPU "x86_64"
#elif defined(__arm__)
#define RUNNER_CPU "arm"
#else
#define RUNNER_CPU "other CPU"
#endif

static const char *require_env(const char *name) {
    const char *value = getenv(name);
    if (!value || !*value) {
        fprintf(stderr,
                "[androidrt] error: %s is not set — this runner must be launched by an AndroidPak profile\n",
                name);
        exit(1);
    }
    return value;
}

#if ANDROIDRT_BIONIC_LOADER

static void dirName(const char *path, char *out, size_t cap) {
    const char *slash = strrchr(path, '/');
    if (!slash) {
        snprintf(out, cap, ".");
        return;
    }
    if ((size_t)(slash - path) >= cap) {
        out[0] = '\0';
        return;
    }
    memcpy(out, path, (size_t)(slash - path));
    out[slash - path] = '\0';
    if (!out[0]) snprintf(out, cap, "/");
}

static int missingPolicy(void) {
    const char *v = getenv("ANDROIDPAK_MISSING");
    if (!v || !*v || strcmp(v, "trap") == 0) return APAK_MISSING_TRAP;
    if (strcmp(v, "zero") == 0) return APAK_MISSING_ZERO;
    if (strcmp(v, "strict") == 0) return APAK_MISSING_STRICT;
    fprintf(stderr, "[androidrt] warn: ANDROIDPAK_MISSING=%s is not trap|zero|strict — using trap\n", v);
    return APAK_MISSING_TRAP;
}

static int mkdirs(const char *path) {
    char buf[4096];
    size_t i, len = strlen(path);
    if (len == 0 || len >= sizeof buf) return -1;
    memcpy(buf, path, len + 1);
    for (i = 1; i <= len; i++) {
        if (buf[i] == '/' || buf[i] == '\0') {
            char saved = buf[i];
            buf[i] = '\0';
            if (mkdir(buf, 0755) != 0 && errno != EEXIST) return -1;
            buf[i] = saved;
        }
    }
    return 0;
}

/* ── the Java side: manifest, DEX index, class packs ──────────────────── */

#define GAME_ACTIVITY_CLASS "com/google/androidgamesdk/GameActivity"

static ApakDexIndex *g_dex;

static int classInApk(const char *name, const char **superName) {
    return g_dex ? apak_dexLookup(g_dex, name, superName) : 0;
}

/* 1 when the manifest's activity is (a subclass of) androidx GameActivity,
 * 0 when it is a NativeActivity or unknown. */
static int manifestGameActivity(const ApakManifest *man) {
    char name[512];
    const char *cur = name, *super = NULL;
    size_t i;
    int depth;
    if (!man->activity[0]) return 0;
    snprintf(name, sizeof name, "%s", man->activity);
    for (i = 0; name[i]; i++)
        if (name[i] == '.') name[i] = '/';
    for (depth = 0; depth < 16 && cur; depth++) {
        if (strcmp(cur, GAME_ACTIVITY_CLASS) == 0) return 1;
        if (strcmp(cur, "android/app/NativeActivity") == 0) return 0;
        super = NULL;
        if (!classInApk(cur, &super)) return 0;
        cur = super;
    }
    return 0;
}

/* System.loadLibrary's second half for every library already loaded. */
static void callOnLoad(const char *name, void *handle, void *ud) {
    void *onLoad = apak_ldSymSelf(handle, "JNI_OnLoad");
    (void)ud;
    if (onLoad) apak_jniCallOnLoad(onLoad, name);
}

static int scanMain(int argc, char **argv) {
    const char *lib = argv[2];
    char dir[4096];
    int missing;
    if (argc >= 4) snprintf(dir, sizeof dir, "%s", argv[3]);
    else dirName(lib, dir, sizeof dir);
    apak_compatInit();
    apak_ldConfigure(dir, APAK_MISSING_TRAP);
    missing = apak_ldScan(lib);
    if (missing < 0) {
        fprintf(stderr, "[androidrt] could not load %s: %s\n", lib, apak_ldLastError());
        return 1;
    }
    return missing ? 2 : 0;
}

#endif /* ANDROIDRT_BIONIC_LOADER */

int main(int argc, char **argv) {
    ActivityRunner runner;
    char libPath[4096];
    void *game, *onCreate, *onFrame = NULL;
    const char *entry = getenv("ANDROIDPAK_ENTRY");
#if ANDROIDRT_BIONIC_LOADER
    static char dataFiles[4096], dataExternal[4096], dataObb[4096];
    static char libDir[4096], dataRoot[3072], cacheDir[4096];
    static ApakManifest manifest;
    const char *data;
    int gameActivity = 0;
#endif

    /* Harmless on purpose: no environment, no files, no GPU — it only proves
     * that this binary starts here (the right CPU, a new enough glibc). */
    if (argc >= 2 && strcmp(argv[1], "--version") == 0) {
#if ANDROIDRT_BIONIC_LOADER
        printf("AndroidRT loader %s (%s, bionic loader)\n", ANDROIDRT_VERSION, RUNNER_CPU);
#else
        printf("AndroidRT loader %s (%s, platform loader)\n", ANDROIDRT_VERSION, RUNNER_CPU);
#endif
        return 0;
    }
#if ANDROIDRT_BIONIC_LOADER
    if (argc >= 3 && strcmp(argv[1], "--scan") == 0) return scanMain(argc, argv);
#endif
    apak_logInit();
    if (!entry || !*entry) entry = "ANativeActivity_onCreate";

    memset(&runner, 0, sizeof(runner));
    runner.apkPath = require_env("ANDROIDPAK_APK");
    runner.stagedRoot = require_env("ANDROIDPAK_STAGED");
    runner.mainLib = require_env("ANDROIDPAK_MAINLIB");

    if (getenv("ANDROIDPAK_DEBUG"))
        fprintf(stderr, "[androidrt] apk=%s staged=%s mainlib=%s\n", runner.apkPath, runner.stagedRoot,
                runner.mainLib);

    /* The game library lives at <staged>/<ANDROIDPAK_MAINLIB>. */
    snprintf(libPath, sizeof(libPath), "%s/%s", runner.stagedRoot, runner.mainLib);

#if ANDROIDRT_BIONIC_LOADER
    /* Saves must outlive the tmpfs stage, which JukaHub deletes after exit. */
    data = getenv("ANDROIDPAK_DATA");
    if (data && *data) {
        snprintf(dataRoot, sizeof dataRoot, "%s", data);
    } else {
        snprintf(dataRoot, sizeof dataRoot, "%s/data", runner.stagedRoot);
        fprintf(stderr, "[androidrt] warn: ANDROIDPAK_DATA not set — game data goes to %s and is lost when the stage is cleaned\n",
                dataRoot);
    }
    snprintf(dataFiles, sizeof dataFiles, "%s/files", dataRoot);
    snprintf(dataExternal, sizeof dataExternal, "%s/external", dataRoot);
    snprintf(dataObb, sizeof dataObb, "%s/obb", dataRoot);
    snprintf(cacheDir, sizeof cacheDir, "%s/cache", dataRoot);
    if (mkdirs(dataFiles) != 0 || mkdirs(dataExternal) != 0 || mkdirs(dataObb) != 0 || mkdirs(cacheDir) != 0)
        fprintf(stderr, "[androidrt] warn: cannot create the data directories under %s: %s\n", dataRoot,
                strerror(errno));
    runner.internalDataPath = dataFiles;
    runner.externalDataPath = dataExternal;
    runner.obbPath = dataObb;
    if (!getenv("TMPDIR")) setenv("TMPDIR", cacheDir, 1);

    if (!apak_tlsSlotCheck())
        fprintf(stderr, "[androidrt] WARNING: the stack-guard TLS slot is not at tpidr_el0+0x28 — NDK code built "
                        "with -fstack-protector will abort; another __thread variable was linked before apak_tls\n");

    dirName(libPath, libDir, sizeof libDir);
    apak_compatInit();
    apak_ldConfigure(libDir, missingPolicy());
    apak_ldSetArgs(argc, argv);
    /* Before the game loads: its EGL imports must bind to the present layer. */
    apak_presentInit();

    /* What the APK says about itself: package, launcher activity and its
     * superclass chain (classes*.dex), read straight from the APK. */
    {
        char err[256];
        ApakZip *apk = android_apkArchive();
        err[0] = '\0';
        if (apak_manifestLoad(runner.stagedRoot, apk, &manifest, err, sizeof err) != 0)
            fprintf(stderr, "[androidrt] manifest: %s — package and activity unknown\n", err[0] ? err : "unreadable");
        else
            fprintf(stderr, "[androidrt] manifest: %s %s, activity %s (minSdk %d, targetSdk %d)\n", manifest.package,
                    manifest.versionName, manifest.activity[0] ? manifest.activity : "?", manifest.minSdk,
                    manifest.targetSdk);
        if (!manifest.package[0] && getenv("ANDROIDPAK_PACKAGE"))
            snprintf(manifest.package, sizeof manifest.package, "%s", getenv("ANDROIDPAK_PACKAGE"));
        err[0] = '\0';
        g_dex = apk ? apak_dexIndexBuild(apk, err, sizeof err) : NULL;
        if (g_dex)
            fprintf(stderr, "[androidrt] dex: %zu classes in %zu file(s)\n", apak_dexCount(g_dex), apak_dexFiles(g_dex));
        else if (err[0])
            fprintf(stderr, "[androidrt] dex: %s\n", err);
        gameActivity = manifestGameActivity(&manifest);
        runner.finishOnPrimaryThreadExit =
            strcmp(manifest.package, "com.anathemasit.penguindash") == 0 ||
            (getenv("ANDROIDPAK_EXIT_ON_MAIN_THREAD") &&
             strcmp(getenv("ANDROIDPAK_EXIT_ON_MAIN_THREAD"), "1") == 0);
    }

    /* The Java environment exists before any game code runs: a library's
     * constructor may already look for its JavaVM. */
    if (apak_jniEnabled()) {
        ApakJniConfig jcfg;
        const ApakDevice *dev = apak_device();
        memset(&jcfg, 0, sizeof jcfg);
        jcfg.packageName = manifest.package;
        jcfg.activityClass = manifest.activity;
        jcfg.versionName = manifest.versionName;
        jcfg.apkPath = runner.apkPath;
        jcfg.stagedRoot = runner.stagedRoot;
        jcfg.libDir = libDir;
        jcfg.dataRoot = dataRoot;
        jcfg.filesDir = dataFiles;
        jcfg.externalDir = dataExternal;
        jcfg.obbDir = dataObb;
        jcfg.cacheDir = cacheDir;
        jcfg.model = dev->model;
        jcfg.board = dev->board;
        apak_devicePanel(&jcfg.panelW, &jcfg.panelH);
        jcfg.dpi = apak_deviceDpi();
        jcfg.gameActivity = gameActivity;
        jcfg.assetManager = android_openAssetManager(runner.stagedRoot);
        jcfg.resolveSymbol = apak_ldSymBundled;
        jcfg.classInApk = classInApk;
        apak_jniAddPack(&apak_packMinecraft);
        apak_jniInit(&jcfg);
        if (gameActivity) apak_gameActivityDefine();
    }

    if (getenv("ANDROIDPAK_SCAN") && strcmp(getenv("ANDROIDPAK_SCAN"), "0") != 0) {
        int missing = apak_ldScan(libPath);
        if (missing < 0) fprintf(stderr, "[androidrt] could not load %s: %s\n", libPath, apak_ldLastError());
        return missing < 0 ? 2 : missing ? 2 : 0;
    }

    game = apak_ldOpen(libPath);
    if (!game) {
        fprintf(stderr, "[androidrt] could not load %s: %s\n", libPath, apak_ldLastError());
        return 2;
    }
    onCreate = apak_ldSymSelf(game, entry);
    if (!onCreate && !gameActivity &&
        apak_ldSymBundled("Java_com_google_androidgamesdk_GameActivity_initializeNativeCode")) {
        /* No manifest said so, but the library carries GameActivity's glue:
         * that is the entry, not some other library's NativeActivity. */
        gameActivity = 1;
        if (apak_jniEnabled()) apak_gameActivityDefine();
    }
    if (!onCreate && !gameActivity) {
        char other[4096];
        if (apak_ldFindExporter(entry, other, sizeof other) && strcmp(other, libPath) != 0) {
            fprintf(stderr, "[androidrt] %s exports no %s; %s does — loading it as the entry library\n",
                    runner.mainLib, entry, other);
            game = apak_ldOpen(other);
            onCreate = game ? apak_ldSymSelf(game, entry) : NULL;
        }
    }
    if (game) onFrame = apak_ldSymSelf(game, "ANativeActivity_onFrame");
    /* What System.loadLibrary would have done before the activity starts:
     * every library's JNI_OnLoad, dependencies first. GameActivity games
     * register their natives here. */
    if (game && apak_jniEnabled()) apak_ldForEachInitOrder(game, callOnLoad, NULL);
#else
    /* Legacy path (Windows smoke builds): the platform loader. */
    {
        const char *shimDir = getenv("ANDROIDPAK_SHIMS");
        (void)shimDir;
    }
    game = rt_dlopen(libPath, 1);
    if (!game) {
        const char *err = rt_dlerror();
        fprintf(stderr, "[androidrt] could not load %s: %s\n", libPath, err ? err : "unknown dlerror");
        return 2;
    }
    onCreate = rt_dlsym(game, entry);
    onFrame = rt_dlsym(game, "ANativeActivity_onFrame");
#endif

#if ANDROIDRT_BIONIC_LOADER
    if (!onCreate && gameActivity) {
        int status;
        if (!apak_jniEnabled()) {
            fprintf(stderr, "[androidrt] %s is a GameActivity game, which needs the Java environment — "
                            "ANDROIDPAK_JNI=off cannot run it\n", runner.mainLib);
            return 3;
        }
#ifdef JUKAHUB_EVDEV
        {
            const char *inputOpt = getenv("ANDROIDPAK_INPUT");
            if (!inputOpt || strcmp(inputOpt, "off") != 0) android_inputStartEvdev(manifest.package);
        }
#endif
        runner.running = 1;
        status = runGameActivity(&runner);
        fprintf(stderr, "[androidrt] lifecycle: done, exit status %d\n", status);
        apak_jniReport();
        apak_presentShutdown();
        fflush(stdout);
        fflush(stderr);
        _exit(status);
    }
#endif
    if (!onCreate) {
        fprintf(stderr,
                "[androidrt] %s exports no %s\n"
                "[androidrt] hint: a library without it is a JNI library for dex code, not a NativeActivity "
                "entry — out of Tier 1 scope\n",
                libPath, entry);
        return 3;
    }

#ifdef JUKAHUB_EVDEV
    /* Controller input: the evdev reader (evdev.c) starts once the game is
     * loaded, so events never arrive into an uncreated queue. OFF via
     * ANDROIDPAK_INPUT=off. */
    {
        const char *inputOpt = getenv("ANDROIDPAK_INPUT");
        if (!inputOpt || strcmp(inputOpt, "off") != 0) android_inputStartEvdev(manifest.package);
        else fprintf(stderr, "[androidrt] input: reader disabled by ANDROIDPAK_INPUT=off\n");
    }
#endif

    runner.running = 1;
    runOneActivity(&runner, onCreate, onFrame);
    fprintf(stderr, "[androidrt] lifecycle: done, exit status %d\n", runner.exitCode);
#if ANDROIDRT_BIONIC_LOADER
    apak_jniReport();
    apak_presentShutdown();
    fflush(stdout);
    fflush(stderr);
    _exit(runner.exitCode);
#else
    return runner.exitCode;
#endif
}
