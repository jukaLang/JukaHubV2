/* fake_jnigame.c — a JNI-heavy NativeActivity game for the smoke suite.
 *
 * It behaves like a title that leans on its Java side the way Minecraft's
 * libminecraftpe.so does: JNI_OnLoad registers natives on the activity class
 * and the store listener, onCreate looks the activity class up, calls its
 * Java methods through the function tables by slot index (exactly what
 * compiled JNI code does), round-trips strings and arrays, asks for a member
 * and a class nobody implements, and opens the store to see what it says.
 * Every result is one "[jnigame]" line on stderr that run-smoke.sh asserts.
 *
 * Built with the host compiler as a glibc shared library (host-loader path);
 * the runner is started with ANDROIDPAK_PACKAGE=com.mojang.minecraftpe so the
 * Minecraft class pack is the one selected.
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

/* JNINativeInterface slots (JNI 1.6 order). */
enum {
    S_GetVersion = 4,
    S_FindClass = 6,
    S_ExceptionCheck = 228,
    S_ExceptionClear = 17,
    S_NewGlobalRef = 21,
    S_DeleteGlobalRef = 22,
    S_DeleteLocalRef = 23,
    S_IsSameObject = 24,
    S_NewObject = 28,
    S_GetObjectClass = 31,
    S_IsInstanceOf = 32,
    S_GetMethodID = 33,
    S_CallObjectMethod = 34,
    S_CallBooleanMethod = 37,
    S_CallIntMethod = 49,
    S_CallVoidMethod = 61,
    S_GetStaticMethodID = 113,
    S_CallStaticObjectMethod = 114,
    S_GetStaticFieldID = 144,
    S_GetStaticObjectField = 145,
    S_GetStaticIntField = 150,
    S_GetStringLength = 164,
    S_NewStringUTF = 167,
    S_GetStringUTFLength = 168,
    S_GetStringUTFChars = 169,
    S_ReleaseStringUTFChars = 170,
    S_NewIntArray = 179,
    S_GetIntArrayElements = 187,
    S_ReleaseIntArrayElements = 195,
    S_SetIntArrayRegion = 211,
    S_RegisterNatives = 215,
};

typedef struct {
    const char *name;
    const char *signature;
    void *fnPtr;
} JNINativeMethod;

#define FN(env, slot, type) ((type)((*(void *const *const *)(env))[slot]))
#define MAIN "com/mojang/minecraftpe/MainActivity"
#define LISTENER "com/mojang/minecraftpe/store/NativeStoreListener"

static int g_failures;

static void say(const char *what) { fprintf(stderr, "[jnigame] %s\n", what); }

static void check(int ok, const char *what) {
    fprintf(stderr, "[jnigame] %s %s\n", ok ? "ok:" : "FAIL:", what);
    if (!ok) g_failures++;
}

/* ── natives the runtime calls back ─────────────────────────────────── */

static void JNICALL_pickCanceled(void *env, jobject self, jlong token) {
    (void)env;
    (void)self;
    fprintf(stderr, "[jnigame] native: image pick canceled, token %lld\n", (long long)token);
}

static void JNICALL_storeInitialized(void *env, jobject self, jlong ptr, jboolean available) {
    (void)env;
    (void)self;
    fprintf(stderr, "[jnigame] native: store initialized=%d listener=0x%llx\n", available, (unsigned long long)ptr);
}

static void JNICALL_registerThis(void *env, jobject self) {
    (void)env;
    fprintf(stderr, "[jnigame] native: nativeRegisterThis(%s)\n", self ? "activity" : "null");
}

/* System.loadLibrary half: the runtime calls this before onCreate. */
ANDROIDRT_EXPORT jint JNI_OnLoad(void *vm, void *reserved) {
    void *env = NULL;
    void *const *invoke = *(void *const *const *)vm;
    jobject cls;
    JNINativeMethod mainNatives[] = {
        { "nativeOnPickImageCanceled", "(J)V", (void *)JNICALL_pickCanceled },
        { "nativeRegisterThis", "()V", (void *)JNICALL_registerThis },
    };
    JNINativeMethod listenerNatives[] = {
        { "onStoreInitialized", "(JZ)V", (void *)JNICALL_storeInitialized },
    };
    (void)reserved;
    if (((jint(*)(void *, void **, jint))invoke[6])(vm, &env, 0x00010006) != 0 || !env) {
        say("FAIL: JNI_OnLoad could not GetEnv");
        return -1;
    }
    cls = FN(env, S_FindClass, jobject (*)(void *, const char *))(env, MAIN);
    check(cls != NULL && FN(env, S_RegisterNatives, jint (*)(void *, jobject, const JNINativeMethod *, jint))(
                             env, cls, mainNatives, 2) == 0,
          "JNI_OnLoad registered natives on MainActivity");
    cls = FN(env, S_FindClass, jobject (*)(void *, const char *))(env, LISTENER);
    check(cls != NULL && FN(env, S_RegisterNatives, jint (*)(void *, jobject, const JNINativeMethod *, jint))(
                             env, cls, listenerNatives, 1) == 0,
          "JNI_OnLoad registered the store listener natives");
    return 0x00010006;
}

ANDROIDRT_EXPORT void ANativeActivity_onCreate(ANativeActivity *activity, void *savedState, size_t savedStateSize) {
    void *env = activity->env;
    jobject self = activity->clazz, cls, s, arr, global;
    jmethodID mid;
    const char *chars;
    char line[512];
    (void)savedState;
    (void)savedStateSize;

    if (!env || !self) {
        check(0, "activity->env and ->clazz exist");
        return;
    }
    check(FN(env, S_GetVersion, jint (*)(void *))(env) == 0x00010006, "GetVersion is JNI 1.6");

    cls = FN(env, S_FindClass, jobject (*)(void *, const char *))(env, MAIN);
    check(cls != NULL, "FindClass finds MainActivity");
    check(FN(env, S_IsInstanceOf, jboolean (*)(void *, jobject, jobject))(env, self, cls) == 1,
          "the activity object is a MainActivity");

    mid = FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(env, cls, "getScreenWidth",
                                                                                             "()I");
    snprintf(line, sizeof line, "getScreenWidth() = %d",
             mid ? FN(env, S_CallIntMethod, jint (*)(void *, jobject, jmethodID, ...))(env, self, mid) : -1);
    say(line);

    mid = FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(
        env, cls, "getExternalStoragePath", "()Ljava/lang/String;");
    s = mid ? FN(env, S_CallObjectMethod, jobject (*)(void *, jobject, jmethodID, ...))(env, self, mid) : NULL;
    chars = s ? FN(env, S_GetStringUTFChars, const char *(*)(void *, jobject, jboolean *))(env, s, NULL) : NULL;
    snprintf(line, sizeof line, "external storage %s", chars ? chars : "(null)");
    say(line);
    if (chars) FN(env, S_ReleaseStringUTFChars, void (*)(void *, jobject, const char *))(env, s, chars);

    /* a member nobody implements: an ID anyway, calls answer the default */
    mid = FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(
        env, cls, "smokeNoSuchMethod", "()I");
    if (mid) {
        jint a = FN(env, S_CallIntMethod, jint (*)(void *, jobject, jmethodID, ...))(env, self, mid);
        jint b = FN(env, S_CallIntMethod, jint (*)(void *, jobject, jmethodID, ...))(env, self, mid);
        check(a == 0 && b == 0, "an unimplemented method answers 0, twice");
    } else {
        check(0, "an unimplemented method still gets a jmethodID");
    }
    check(FN(env, S_FindClass, jobject (*)(void *, const char *))(env, "com/example/SmokeNoSuchClass") == NULL,
          "FindClass of a class nobody has returns null");
    if (FN(env, S_ExceptionCheck, jboolean (*)(void *))(env)) FN(env, S_ExceptionClear, void (*)(void *))(env);
    /* asked again, as games do every frame: no second log line or report entry */
    FN(env, S_FindClass, jobject (*)(void *, const char *))(env, "com/example/SmokeNoSuchClass");
    if (FN(env, S_ExceptionCheck, jboolean (*)(void *))(env)) FN(env, S_ExceptionClear, void (*)(void *))(env);

    /* strings: modified UTF-8 in, UTF-16 length out */
    s = FN(env, S_NewStringUTF, jobject (*)(void *, const char *))(env, "h\xc3\xa9llo \xe2\x82\xac");
    snprintf(line, sizeof line, "string lengths utf16=%d utf8=%d",
             s ? FN(env, S_GetStringLength, jint (*)(void *, jobject))(env, s) : -1,
             s ? FN(env, S_GetStringUTFLength, jint (*)(void *, jobject))(env, s) : -1);
    say(line);

    /* arrays */
    arr = FN(env, S_NewIntArray, jobject (*)(void *, jint))(env, 4);
    if (arr) {
        static const jint values[4] = { 1, 2, 3, 4 };
        jint *e, sum = 0, i;
        FN(env, S_SetIntArrayRegion, void (*)(void *, jobject, jint, jint, const jint *))(env, arr, 0, 4, values);
        e = FN(env, S_GetIntArrayElements, jint *(*)(void *, jobject, jboolean *))(env, arr, NULL);
        for (i = 0; e && i < 4; i++) sum += e[i];
        if (e) FN(env, S_ReleaseIntArrayElements, void (*)(void *, jobject, jint *, jint))(env, arr, e, 0);
        snprintf(line, sizeof line, "int array sum %d", sum);
        say(line);
    }

    /* global references survive the local frame */
    global = FN(env, S_NewGlobalRef, jobject (*)(void *, jobject))(env, cls);
    check(global && FN(env, S_IsSameObject, jboolean (*)(void *, jobject, jobject))(env, global, cls),
          "a global reference is the same object");
    FN(env, S_DeleteGlobalRef, void (*)(void *, jobject))(env, global);

    /* the store: what does it say about the license? */
    {
        jobject factory = FN(env, S_FindClass, jobject (*)(void *, const char *))(
            env, "com/mojang/minecraftpe/store/StoreFactory");
        jobject lcls = FN(env, S_FindClass, jobject (*)(void *, const char *))(env, LISTENER);
        jmethodID create = factory ? FN(env, S_GetStaticMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(
                                         env, factory, "createGooglePlayStore",
                                         "(Ljava/lang/String;Lcom/mojang/minecraftpe/store/StoreListener;)"
                                         "Lcom/mojang/minecraftpe/store/Store;")
                                   : NULL;
        jmethodID ctor = lcls ? FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(
                                    env, lcls, "<init>", "(J)V")
                              : NULL;
        jobject listener = ctor ? FN(env, S_NewObject, jobject (*)(void *, jobject, jmethodID, ...))(
                                      env, lcls, ctor, (jlong)0x5eed)
                                : NULL;
        jobject key = FN(env, S_NewStringUTF, jobject (*)(void *, const char *))(env, "smoke-key");
        jobject store = create ? FN(env, S_CallStaticObjectMethod, jobject (*)(void *, jobject, jmethodID, ...))(
                                     env, factory, create, key, listener)
                               : NULL;
        if (store) {
            jobject scls = FN(env, S_GetObjectClass, jobject (*)(void *, jobject))(env, store);
            jmethodID verified = FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(
                env, scls, "hasVerifiedLicense", "()Z");
            jmethodID received = FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(
                env, scls, "receivedLicenseResponse", "()Z");
            snprintf(line, sizeof line, "store license verified=%d received=%d",
                     FN(env, S_CallBooleanMethod, jboolean (*)(void *, jobject, jmethodID, ...))(env, store, verified),
                     FN(env, S_CallBooleanMethod, jboolean (*)(void *, jobject, jmethodID, ...))(env, store, received));
            say(line);
        } else {
            check(0, "StoreFactory.createGooglePlayStore returns a store object");
        }
    }

    /* android.os.Build: the static fields games read to describe the device */
    {
        jobject build = FN(env, S_FindClass, jobject (*)(void *, const char *))(env, "android/os/Build");
        jobject version = FN(env, S_FindClass, jobject (*)(void *, const char *))(env, "android/os/Build$VERSION");
        void *(*getStaticFieldID)(void *, jobject, const char *, const char *) =
            FN(env, S_GetStaticFieldID, void *(*)(void *, jobject, const char *, const char *));
        jobject model = build ? FN(env, S_GetStaticObjectField, jobject (*)(void *, jobject, void *))(
                                    env, build, getStaticFieldID(env, build, "MODEL", "Ljava/lang/String;"))
                              : NULL;
        jobject maker = build ? FN(env, S_GetStaticObjectField, jobject (*)(void *, jobject, void *))(
                                    env, build, getStaticFieldID(env, build, "MANUFACTURER", "Ljava/lang/String;"))
                              : NULL;
        jint sdk = version ? FN(env, S_GetStaticIntField, jint (*)(void *, jobject, void *))(
                                 env, version, getStaticFieldID(env, version, "SDK_INT", "I"))
                           : -1;
        const char *m = model ? FN(env, S_GetStringUTFChars, const char *(*)(void *, jobject, jboolean *))(env, model, NULL)
                              : NULL;
        const char *k = maker ? FN(env, S_GetStringUTFChars, const char *(*)(void *, jobject, jboolean *))(env, maker, NULL)
                              : NULL;
        snprintf(line, sizeof line, "Build.MODEL=%s MANUFACTURER=%s SDK_INT=%d", m ? m : "(null)", k ? k : "(null)", sdk);
        say(line);
        if (m) FN(env, S_ReleaseStringUTFChars, void (*)(void *, jobject, const char *))(env, model, m);
        if (k) FN(env, S_ReleaseStringUTFChars, void (*)(void *, jobject, const char *))(env, maker, k);
    }

    /* a Java call that answers later, through a registered native */
    mid = FN(env, S_GetMethodID, jmethodID (*)(void *, jobject, const char *, const char *))(env, cls, "pickImage",
                                                                                             "(J)V");
    if (mid) FN(env, S_CallVoidMethod, void (*)(void *, jobject, jmethodID, ...))(env, self, mid, (jlong)77);

    snprintf(line, sizeof line, "onCreate done, %d failure(s)", g_failures);
    say(line);
}
