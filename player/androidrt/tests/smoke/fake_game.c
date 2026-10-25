/* fake_game.c — the smoke-test game for AndroidRT's runner.
 *
 * Built as a shared library (libgame.so on Linux/aarch64, fake_game.dll on
 * the Windows host), it exports ANativeActivity_onCreate and mimics the
 * entry every NativeActivity game has. On Linux it additionally calls two
 * shim symbols — __android_log_print (liblog.so) and ANativeActivity_finish
 * (libandroid.so) — proving that key shim symbols resolve at load time from
 * the runner's globally preloaded shims. On Windows LoadLibrary cannot
 * resolve symbols into the EXE, so those calls are compiled out and the
 * runner's ANDROIDRT_SMOKE_FRAMES hook ends the run instead.
 */

#include <stdio.h>
#include <string.h>

#include "actbridge.h"

#ifdef _WIN32
#define GAME_LOG(line) fprintf(stderr, "[game][smoke][I] %s\n", line)
#else
enum { FAKE_LOG_INFO = 4 };
extern int __android_log_print(int prio, const char *tag, const char *fmt, ...);
extern void ANativeActivity_finish(ANativeActivity *activity);
#define GAME_LOG(line) __android_log_print(FAKE_LOG_INFO, "JukaHubSmoke", "%s", line)
#endif

static ANativeActivity *g_activity;
static int g_framesCalled;

ANDROIDRT_EXPORT void ANativeActivity_onCreate(ANativeActivity *activity,
                                               void *savedState,
                                               size_t savedStateSize) {
    (void)savedState;
    (void)savedStateSize;
    g_activity = activity;
    g_framesCalled = 0;
    GAME_LOG("onCreate served — fake game is live");
    GAME_LOG(g_activity->internalDataPath ? "paths handed over correctly"
                                          : "FAIL: internalDataPath is NULL");
    GAME_LOG(activity->assetManager ? "asset manager present" : "no asset manager");
}

/* Called once per runner iteration via the callback the runner invokes for
 * each frame's bookkeeping. The runner has no per-frame game entry point,
 * so this hook exists only to count iterations and proves loop liveness. */
ANDROIDRT_EXPORT void ANativeActivity_onFrame(ANativeActivity *activity) {
    (void)activity;
    g_framesCalled++;
    if (g_framesCalled == 3) {
        char msg[64];
        snprintf(msg, sizeof msg, "loop alive after %d frames", g_framesCalled);
        GAME_LOG(msg);
    }
}
