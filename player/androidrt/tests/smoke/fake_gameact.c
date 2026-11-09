/* fake_gameact.c — an androidx GameActivity game for the smoke suite.
 *
 * A GameActivity game has no ANativeActivity_onCreate. Its native library
 * carries GameActivity's C++ glue, whose JNI_OnLoad registers natives on
 * com.google.androidgamesdk.GameActivity; the Java class then drives them:
 * initializeNativeCode (paths, AssetManager, saved state, Configuration →
 * a native handle), onStartNative, onResumeNative, onSurfaceCreatedNative,
 * onSurfaceChangedNative, onWindowFocusChangedNative, key/touch events, and
 * the teardown. This fake glue registers those natives, reads the Java
 * objects it is handed back through JNI (like the real glue builds its
 * structs from KeyEvent.getKeyCode() & co.) and prints one "[gameact]" line
 * per call for run-smoke.sh. It also exports the
 * Java_com_google_androidgamesdk_GameActivity_initializeNativeCode symbol
 * real glue exports, which is how the runner tells a GameActivity game
 * without a manifest.
 */
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "actbridge.h"

typedef void *jobject;
typedef void *jmethodID;
typedef int32_t jint;
typedef int64_t jlong;
typedef uint8_t jboolean;

enum {
    S_FindClass = 6,
    S_GetObjectClass = 31,
    S_GetMethodID = 33,
    S_CallIntMethod = 49,
    S_GetStringUTFChars = 169,
    S_ReleaseStringUTFChars = 170,
    S_RegisterNatives = 215,
};

typedef struct {
    const char *name;
    const char *signature;
    void *fnPtr;
} JNINativeMethod;

#define FN(env, slot, type) ((type)((*(void *const *const *)(env))[slot]))
#define GA "com/google/androidgamesdk/GameActivity"
#define HANDLE ((jlong)0x6a6a)

static void say(const char *what) { fprintf(stderr, "[gameact] %s\n", what); }

static void printString(void *env, const char *label, jobject s) {
    const char *chars = s ? FN(env, S_GetStringUTFChars, const char *(*)(void *, jobject, jboolean *))(env, s, NULL)
                          : NULL;
    fprintf(stderr, "[gameact] %s %s\n", label, chars ? chars : "(null)");
    if (chars) FN(env, S_ReleaseStringUTFChars, void (*)(void *, jobject, const char *))(env, s, chars);
}

static jlong initNative(void *env, jobject self, jobject internal, jobject obb, jobject external, jobject assets,
                        jobject saved, jobject config) {
    (void)self;
    (void)saved;
    printString(env, "initializeNativeCode internal", internal);
    printString(env, "initializeNativeCode obb", obb);
    printString(env, "initializeNativeCode external", external);
    fprintf(stderr, "[gameact] initializeNativeCode assets=%s config=%s\n", assets ? "yes" : "no",
            config ? "yes" : "no");
    return HANDLE;
}

ANDROIDRT_EXPORT jlong Java_com_google_androidgamesdk_GameActivity_initializeNativeCode(
    void *env, jobject self, jobject internal, jobject obb, jobject external, jobject assets, jobject saved,
    jobject config) {
    return initNative(env, self, internal, obb, external, assets, saved, config);
}

static void handleCall(const char *what, jlong handle) {
    fprintf(stderr, "[gameact] %s handle=%s\n", what, handle == HANDLE ? "ok" : "WRONG");
}
static void onStart(void *env, jobject self, jlong h) { handleCall("onStartNative", h); }
static void onResume(void *env, jobject self, jlong h) { handleCall("onResumeNative", h); }
static void onPause(void *env, jobject self, jlong h) { handleCall("onPauseNative", h); }
static void onStop(void *env, jobject self, jlong h) { handleCall("onStopNative", h); }
static void onSurfaceDestroyed(void *env, jobject self, jlong h) { handleCall("onSurfaceDestroyedNative", h); }
static void terminate(void *env, jobject self, jlong h) { handleCall("terminateNativeCode", h); }

static void onSurfaceCreated(void *env, jobject self, jlong h, jobject surface) {
    fprintf(stderr, "[gameact] onSurfaceCreatedNative handle=%s surface=%s\n", h == HANDLE ? "ok" : "WRONG",
            surface ? "yes" : "no");
}

static void onSurfaceChanged(void *env, jobject self, jlong h, jobject surface, jint format, jint w, jint hgt) {
    fprintf(stderr, "[gameact] onSurfaceChangedNative %dx%d format %d\n", w, hgt, format);
}

static void onFocus(void *env, jobject self, jlong h, jboolean focused) {
    fprintf(stderr, "[gameact] onWindowFocusChangedNative %d\n", focused);
}

/* The real glue reads the event's fields back through JNI. */
static jboolean onKey(void *env, jobject self, jlong h, jobject event, const char *what) {
    jobject cls = event ? FN(env, S_GetObjectClass, jobject (*)(void *, jobject))(env, event) : NULL;
    jmethodID keyCode = cls ? FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(
                                  env, cls, "getKeyCode", "()I")
                            : NULL;
    jint code = keyCode ? FN(env, S_CallIntMethod, jint (*)(void *, jobject, jmethodID, ...))(env, event, keyCode) : -1;
    fprintf(stderr, "[gameact] %s keyCode=%d\n", what, code);
    return 1;
}
static jboolean onKeyDown(void *env, jobject self, jlong h, jobject e) { return onKey(env, self, h, e, "onKeyDownNative"); }
static jboolean onKeyUp(void *env, jobject self, jlong h, jobject e) { return onKey(env, self, h, e, "onKeyUpNative"); }

ANDROIDRT_EXPORT jint JNI_OnLoad(void *vm, void *reserved) {
    void *env = NULL;
    void *const *invoke = *(void *const *const *)vm;
    jobject cls;
    static const JNINativeMethod natives[] = {
        { "initializeNativeCode",
          "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;Landroid/content/res/AssetManager;[B"
          "Landroid/content/res/Configuration;)J",
          (void *)initNative },
        { "onStartNative", "(J)V", (void *)onStart },
        { "onResumeNative", "(J)V", (void *)onResume },
        { "onPauseNative", "(J)V", (void *)onPause },
        { "onStopNative", "(J)V", (void *)onStop },
        { "onSurfaceCreatedNative", "(JLandroid/view/Surface;)V", (void *)onSurfaceCreated },
        { "onSurfaceChangedNative", "(JLandroid/view/Surface;III)V", (void *)onSurfaceChanged },
        { "onSurfaceDestroyedNative", "(J)V", (void *)onSurfaceDestroyed },
        { "onWindowFocusChangedNative", "(JZ)V", (void *)onFocus },
        { "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z", (void *)onKeyDown },
        { "onKeyUpNative", "(JLandroid/view/KeyEvent;)Z", (void *)onKeyUp },
        { "terminateNativeCode", "(J)V", (void *)terminate },
    };
    (void)reserved;
    if (((jint(*)(void *, void **, jint))invoke[6])(vm, &env, 0x00010006) != 0 || !env) return -1;
    cls = FN(env, S_FindClass, jobject (*)(void *, const char *))(env, GA);
    if (!cls) {
        say("FAIL: FindClass(GameActivity) is null");
        return -1;
    }
    if (FN(env, S_RegisterNatives, jint (*)(void *, jobject, const JNINativeMethod *, jint))(
            env, cls, natives, (jint)(sizeof natives / sizeof natives[0])) != 0) {
        say("FAIL: RegisterNatives on GameActivity");
        return -1;
    }
    say("JNI_OnLoad registered the GameActivity natives");
    return 0x00010006;
}
