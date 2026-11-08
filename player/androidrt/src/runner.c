/* runner.c — the AndroidRT "loader" executable.
 *
 *   loader <apk> [args]            run a staged game (ANDROIDPAK_* env, set by
 *                                  the AndroidPak profile runtime)
 *   loader --scan <lib.so> [dir]   print the import report of a bionic library
 *                                  and its bundled dependencies, run nothing
 *                                  (exit 0 = everything resolves, 2 = undefined
 *                                  symbols listed, 1 = unreadable)
 *
 * Profile mode, Linux (the device):
 *   1. validates the ANDROIDPAK_* environment and prepares the persistent
 *      data directory (ANDROIDPAK_DATA: files/, external/, obb/, cache/);
 *   2. loads the game's main library with the runner's own bionic loader
 *      (bionicld.c) — every import bound through the compat resolver, the
 *      complete undefined-symbol list printed before any game code runs;
 *   3. resolves ANativeActivity_onCreate (or $ANDROIDPAK_ENTRY), searching
 *      the other staged libraries when the main one does not export it;
 *   4. drives the lifecycle (activity.c) and _exit()s with its status —
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

#if defined(__linux__)
#define ANDROIDRT_BIONIC_LOADER 1
#include "bioniccompat.h"
#include "bionicld.h"
/* eglpresent.c */
void apak_presentInit(void);
void apak_presentShutdown(void);
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
    char libDir[4096], dataRoot[3072], cacheDir[4096];
    const char *data;
#endif

#if ANDROIDRT_BIONIC_LOADER
    if (argc >= 3 && strcmp(argv[1], "--scan") == 0) return scanMain(argc, argv);
#endif
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
    if (!onCreate) {
        char other[4096];
        if (apak_ldFindExporter(entry, other, sizeof other) && strcmp(other, libPath) != 0) {
            fprintf(stderr, "[androidrt] %s exports no %s; %s does — loading it as the entry library\n",
                    runner.mainLib, entry, other);
            game = apak_ldOpen(other);
            onCreate = game ? apak_ldSymSelf(game, entry) : NULL;
        }
    }
    if (game) onFrame = apak_ldSymSelf(game, "ANativeActivity_onFrame");
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
        if (!inputOpt || strcmp(inputOpt, "off") != 0) android_inputStartEvdev();
        else fprintf(stderr, "[androidrt] input: reader disabled by ANDROIDPAK_INPUT=off\n");
    }
#endif

    runner.running = 1;
    runOneActivity(&runner, onCreate, onFrame);
    fprintf(stderr, "[androidrt] lifecycle: done, exit status %d\n", runner.exitCode);
#if ANDROIDRT_BIONIC_LOADER
    apak_presentShutdown();
    fflush(stdout);
    fflush(stderr);
    _exit(runner.exitCode);
#else
    return runner.exitCode;
#endif
}
