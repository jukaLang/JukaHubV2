/* runner.c — the AndroidRT "loader" executable.
 *
 * This is the binary the AndroidPak game-profile runtime launches as
 *   <AndroidRT>/loader <apk>
 * with the staged tree and triage result handed over through ANDROIDPAK_*
 * environment variables (see ../ .. in the parent Go tree).
 *
 * What it does:
 *   1. validates the ANDROIDPAK_* environment,
 *   2. dlopens every shim in <AndroidRT>/shims so the game's DT_NEEDED
 *      entries resolve to liblog.so / libandroid.so / libOpenSLES.so,
 *   3. dlopens the game's main .so from the staged tree,
 *   4. resolves ANativeActivity_onCreate and drives the lifecycle:
 *      onCreate → onStart → onResume → onNativeWindowCreated →
 *      ... game loop ... → onDestroy → exit.
 *
 * It is intentionally the simplest honest harness: a bionic-linked game .so
 * will usually fail dlopen on glibc with an undefined symbol. When it does,
 * this prints the exact missing name — which is precisely what shims are
 * built against: add the symbol to a shim, rebuild, retry. That measurement
 * loop, not a full bionic-translation port, is this file's purpose today.
 */

#define _GNU_SOURCE
#include <dirent.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "actbridge.h"
#include "dlport.h"

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

/* dlopen every .so in the shims directory with RTLD_GLOBAL. */
static void preload_shims(void) {
    const char *shimDir = getenv("ANDROIDPAK_SHIMS");
    if (!shimDir || !*shimDir) {
        fprintf(stderr, "[androidrt] warn: ANDROIDPAK_SHIMS not set — no shims preloaded\n");
        return;
    }
    DIR *dir = opendir(shimDir);
    if (!dir) {
        fprintf(stderr, "[androidrt] warn: shims dir not readable: %s\n", shimDir);
        return;
    }
    struct dirent *entry;
    while ((entry = readdir(dir)) != NULL) {
        size_t len = strlen(entry->d_name);
        if (len <= 3 || strcmp(entry->d_name + len - 3, ".so") != 0) {
            continue;
        }
        char path[4096];
        snprintf(path, sizeof(path), "%s/%s", shimDir, entry->d_name);
        void *handle = rt_dlopen(path, 1);
        if (!handle) {
            fprintf(stderr, "[androidrt] shim %s did not load: %s\n", path, rt_dlerror());
        } else {
            fprintf(stderr, "[androidrt] shim loaded: %s\n", path);
        }
    }
    closedir(dir);
}

int main(int argc, char **argv) {
    ActivityRunner runner;
    memset(&runner, 0, sizeof(runner));

    runner.apkPath = require_env("ANDROIDPAK_APK");
    runner.stagedRoot = require_env("ANDROIDPAK_STAGED");
    runner.mainLib = require_env("ANDROIDPAK_MAINLIB");

    if (getenv("ANDROIDPAK_DEBUG")) {
        fprintf(stderr, "[androidrt] apk=%s staged=%s mainlib=%s\n",
                runner.apkPath, runner.stagedRoot, runner.mainLib);
    }

    preload_shims();

#ifdef JUKAHUB_EVDEV
    /* Controller input: the evdev reader (evdev.c) starts once the shims
     * are up so events can never arrive into an uncreated queue. OFF via
     * ANDROIDPAK_INPUT=off, mirroring the mem/surface opt-outs. */
    {
        const char *inputOpt = getenv("ANDROIDPAK_INPUT");
        if (!inputOpt || strcmp(inputOpt, "off") != 0) android_inputStartEvdev();
        else fprintf(stderr, "[androidrt] input: reader disabled by ANDROIDPAK_INPUT=off\n");
    }
#endif

    /* The game library lives at <staged>/<ANDROIDPAK_MAINLIB>. */
    char libPath[4096];
    snprintf(libPath, sizeof(libPath), "%s/%s", runner.stagedRoot, runner.mainLib);

    void *game = rt_dlopen(libPath, 1);
    if (!game) {
        /* The useful failure: the exact missing symbol is the next shim. */
        const char *err = rt_dlerror();
        fprintf(stderr,
                "[androidrt] could not load %s: %s\n"
                "[androidrt] hint: undefined bionic symbols belong in a shim "
                "(shims/*.so), rebuilt with build-tsp.sh\n",
                libPath, err ? err : "unknown dlerror");
        return 2;
    }

    void *onCreate = rt_dlsym(game, "ANativeActivity_onCreate");
    if (!onCreate) {
        fprintf(stderr,
                "[androidrt] %s exports no ANativeActivity_onCreate\n"
                "[androidrt] hint: a libgame.so without it is a JNI library "
                "for dex code, not a NativeActivity entry — out of Tier 1 "
                "scope\n",
                libPath);
        return 3;
    }

    runner.running = 1;
    runOneActivity(&runner, onCreate, game);
    return runner.exitCode;
}
