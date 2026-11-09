/* gameactivity.c — androidx GameActivity games without Java.
 *
 * A GameActivity game (Minecraft since 1.21.80, most AGDK titles) has no
 * ANativeActivity_onCreate. Its native library embeds GameActivity's C++
 * glue, which is driven by the Java class com.google.androidgamesdk.
 * GameActivity through natives: initializeNativeCode (which runs the game's
 * GameActivity_onCreate and starts android_main), onStartNative,
 * onSurfaceCreatedNative, onKeyDownNative ... The glue then builds its own
 * GameActivity / GameActivityKeyEvent structs from the Java objects it is
 * handed, by calling KeyEvent.getKeyCode() & co. through JNI.
 *
 * So this file plays the Java side, exactly the "official entry point"
 * approach: it never allocates GameActivity structs itself (their layout
 * changes between GameActivity releases). Natives are found in the game's
 * RegisterNatives tables or as exported Java_com_google_androidgamesdk_
 * GameActivity_* symbols; a native whose descriptor this runtime does not
 * know is not called (logged), and passing extra trailing arguments to an
 * older, shorter native is harmless in both supported ABIs.
 *
 * UNVERIFIED with a real GameActivity game: host-tested against a fake glue
 * (tests/smoke/fake_gameact.c) that registers the natives in JNI_OnLoad and
 * reads KeyEvents back through JNI, the way the real glue does.
 */
#define _GNU_SOURCE
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "actbridge.h"
#include "device.h"
#include "jni.h"

#define GA "com/google/androidgamesdk/GameActivity"
#define GA_TAG "[androidrt][gameactivity] "

/* activity.c */
int runner_nextEvent(void);

/* ── the Java classes the glue calls back into ─────────────────────── */

#define IMPL(name) static void name(ApakJCall *c)

static ApakJObject *insets(void) {
    ApakJObject *o = apak_jniNewInstance("androidx/core/graphics/Insets");
    return o; /* all four edges 0: a full-screen panel without cutouts */
}
IMPL(ga_insets) { c->ret.l = insets(); }
IMPL(ga_noop) { (void)c; }
IMPL(ins_of) {
    ApakJObject *o = apak_jniNewInstance("androidx/core/graphics/Insets");
    const char *names[4] = { "left", "top", "right", "bottom" };
    int i;
    for (i = 0; o && i < 4; i++) {
        ApakJValue v;
        v.j = 0;
        v.i = c->args[i].i;
        apak_jniFieldSet(o, names[i], v);
    }
    c->ret.l = o;
}

static const ApakJMethodDef kGameActivity[] = {
    { "getWindowInsets", "(I)Landroidx/core/graphics/Insets;", 0, ga_insets },
    { "getWaterfallInsets", "()Landroidx/core/graphics/Insets;", 0, ga_insets },
    { "setWindowFlags", "(II)V", 0, ga_noop },
    { "setImeEditorInfoFields", "(III)V", 0, ga_noop },
    { NULL, NULL, 0, NULL },
};
static const ApakJFieldDef kInsetsFields[] = {
    { "left", "I", 0 }, { "top", "I", 0 }, { "right", "I", 0 }, { "bottom", "I", 0 }, { NULL, NULL, 0 },
};
static const ApakJMethodDef kInsets[] = {
    { "of", "(IIII)Landroidx/core/graphics/Insets;", 1, ins_of },
    { NULL, NULL, 0, NULL },
};

void apak_gameActivityDefine(void);
void apak_gameActivityDefine(void) {
    static const ApakJClassDef defs[] = {
        { GA, "android/app/Activity", kGameActivity, NULL },
        { "androidx/core/graphics/Insets", "java/lang/Object", kInsets, kInsetsFields },
        { NULL, NULL, NULL, NULL },
    };
    apak_jniDefineAll(defs);
}

/* ── calling the glue ──────────────────────────────────────────────── */

static int64_t g_handle;

/* Call GameActivity.<name> on the activity with the descriptor the game
 * registered when it matches one of `known`, else the first known form. */
static int callNative(const char *name, const char *const *known, const ApakJValue *args, ApakJValue *ret) {
    const char *reg = apak_jniNativeSig(GA, name);
    const char *sig = known[0];
    int i;
    if (reg[0]) {
        for (i = 0; known[i]; i++)
            if (strcmp(reg, known[i]) == 0) sig = known[i];
        if (strcmp(reg, sig) != 0) {
            char key[96];
            snprintf(key, sizeof key, "ga-sig %s", name);
            apak_jniLogOnce(key, "GameActivity.%s is registered as %s — a form this runtime does not drive; skipped",
                            name, reg);
            return -1;
        }
    }
    return apak_jniCallNative(apak_jniMainEnv(), apak_jniActivity(), GA, name, sig, args, ret);
}

static void callHandle(const char *name) {
    static const char *const sig[] = { "(J)V", NULL };
    ApakJValue a[1];
    a[0].j = g_handle;
    callNative(name, sig, a, NULL);
}

static void callHandleBool(const char *name, int value) {
    static const char *const sig[] = { "(JZ)V", NULL };
    ApakJValue a[2];
    memset(a, 0, sizeof a);
    a[0].j = g_handle;
    a[1].z = (uint8_t)(value ? 1 : 0);
    callNative(name, sig, a, NULL);
}

static int64_t nowNs(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

/* One queued input event → onKeyDownNative/onKeyUpNative/onTouchEventNative. */
static void deliverInput(AInputQueue *queue) {
    static const char *const keySig[] = { "(JLandroid/view/KeyEvent;)Z", NULL };
    static const char *const touchSig[] = { "(JLandroid/view/MotionEvent;)Z", NULL };
    AInputEvent *ev = NULL;
    while (AInputQueue_getEvent(queue, &ev) >= 0 && ev) {
        ApakJValue a[2], r;
        ApakJObject *obj = NULL;
        memset(a, 0, sizeof a);
        a[0].j = g_handle;
        if (AInputEvent_getType(ev) == AINPUT_EVENT_TYPE_KEY) {
            ApakKeyEventData k;
            memset(&k, 0, sizeof k);
            k.deviceId = AInputEvent_getDeviceId(ev);
            k.source = AInputEvent_getSource(ev);
            k.action = AKeyEvent_getAction(ev);
            k.keyCode = AKeyEvent_getKeyCode(ev);
            k.repeatCount = AKeyEvent_getRepeatCount(ev);
            k.eventTimeNs = k.downTimeNs = nowNs();
            obj = apak_jniRetain(apak_jniNewKeyEvent(&k));
            a[1].l = obj;
            callNative(k.action == AKEY_EVENT_ACTION_DOWN ? "onKeyDownNative" : "onKeyUpNative", keySig, a, &r);
        } else {
            ApakMotionEventData m;
            int axis;
            memset(&m, 0, sizeof m);
            m.deviceId = AInputEvent_getDeviceId(ev);
            m.source = AInputEvent_getSource(ev);
            m.action = AMotionEvent_getAction(ev);
            m.eventTimeNs = m.downTimeNs = nowNs();
            for (axis = 0; axis < 19 && axis < APAK_MOTION_AXES; axis++) m.axes[axis] = AMotionEvent_getAxisValue(ev, axis, 0);
            obj = apak_jniRetain(apak_jniNewMotionEvent(&m));
            a[1].l = obj;
            callNative("onTouchEventNative", touchSig, a, &r);
        }
        if (obj) apak_jniRelease(obj);
        AInputQueue_finishEvent(queue, ev, 1);
        ev = NULL;
    }
}

static int envInt(const char *name) {
    const char *v = getenv(name);
    return v && *v ? atoi(v) : 0;
}

int runGameActivity(ActivityRunner *runner) {
    static const char *const initSig[] = {
        /* 2.x+: with the Configuration; 1.x without it (an extra trailing
         * argument to the older form is ignored by its callee) */
        "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;Landroid/content/res/AssetManager;[B"
        "Landroid/content/res/Configuration;)J",
        "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;Landroid/content/res/AssetManager;[B)J",
        NULL,
    };
    static const char *const surfaceSig[] = { "(JLandroid/view/Surface;)V", NULL };
    static const char *const changedSig[] = { "(JLandroid/view/Surface;III)V", NULL };
    ANativeActivity shell; /* plumbing only: the shim's window and finish() */
    ApakJValue a[8], r;
    ApakJObject *files, *obb, *ext, *assets, *config, *surface;
    AInputQueue *queue;
    int w = 1280, h = 720, iteration = 0;
    int smokeLimit = envInt("ANDROIDRT_SMOKE_FRAMES"), smokeInput = envInt("ANDROIDRT_SMOKE_INPUT");
    const ApakJniConfig *cfg = apak_jniConfig();

    memset(&shell, 0, sizeof shell);
    shell.vm = apak_jniVM();
    shell.env = apak_jniEnv();
    shell.clazz = apak_jniActivityObject();
    android_activityInit(&shell, runner);

    apak_jniEvent(APAK_JNI_EV_BEFORE_ONCREATE);
    fprintf(stderr, "[androidrt] lifecycle: GameActivity.onCreate (initializeNativeCode)\n");
    files = apak_jniRetain(apak_jniNewString(cfg->filesDir));
    obb = apak_jniRetain(apak_jniNewString(cfg->obbDir));
    ext = apak_jniRetain(apak_jniNewString(cfg->externalDir));
    apak_jniCallMethod(apak_jniMainEnv(), apak_jniActivity(), "getAssets", "()Landroid/content/res/AssetManager;", NULL,
                       &r);
    assets = apak_jniRetain((ApakJObject *)r.l);
    config = apak_jniRetain(apak_jniNewInstance("android/content/res/Configuration"));
    apak_jniCallMethod(apak_jniMainEnv(), config, "<init>", "()V", NULL, NULL);
    memset(a, 0, sizeof a);
    a[0].l = files;
    a[1].l = obb;
    a[2].l = ext;
    a[3].l = assets;
    a[4].l = NULL; /* no saved state: every launch is a fresh process */
    a[5].l = config;
    if (callNative("initializeNativeCode", initSig, a, &r) != 0) {
        fprintf(stderr, GA_TAG "the game has no GameActivity.initializeNativeCode (not registered, not exported) — "
                        "cannot start it\n");
        return 4;
    }
    g_handle = r.j;
    apak_jniRelease(files);
    apak_jniRelease(obb);
    apak_jniRelease(ext);
    apak_jniRelease(config);
    apak_jniExceptionReport(apak_jniMainEnv(), "initializeNativeCode");
    if (!g_handle) {
        fprintf(stderr, GA_TAG "initializeNativeCode returned no handle — the game refused to start\n");
        return 4;
    }
    fprintf(stderr, GA_TAG "native code initialised (handle 0x%llx)\n", (unsigned long long)g_handle);
    apak_jniEvent(APAK_JNI_EV_AFTER_ONCREATE);

    fprintf(stderr, "[androidrt] lifecycle: onStart\n");
    callHandle("onStartNative");
    fprintf(stderr, "[androidrt] lifecycle: onResume\n");
    callHandle("onResumeNative");
    apak_jniEvent(APAK_JNI_EV_RESUME);

    queue = android_inputQueueCreate();
    apak_devicePanel(&w, &h);
    fprintf(stderr, "[androidrt] lifecycle: window %dx%d\n", w, h);
    android_windowBackend(w, h);
    android_activitySetWindow(&shell, android_currentWindow());
    surface = apak_jniRetain(apak_jniNewInstance("android/view/Surface"));
    memset(a, 0, sizeof a);
    a[0].j = g_handle;
    a[1].l = surface;
    fprintf(stderr, "[androidrt] lifecycle: onSurfaceCreated %dx%d\n", w, h);
    callNative("onSurfaceCreatedNative", surfaceSig, a, NULL);
    a[2].i = 1; /* PixelFormat.RGBA_8888 */
    a[3].i = w;
    a[4].i = h;
    callNative("onSurfaceChangedNative", changedSig, a, NULL);
    callHandleBool("onWindowFocusChangedNative", 1);

    while (runner->running) {
        int event;
        if (smokeLimit > 0 && ++iteration > smokeLimit) {
            fprintf(stderr, "[androidrt] smoke: %d frames driven, ending run\n", smokeLimit);
            runner_requestExit(runner, 0);
            smokeLimit = 0;
        }
        if (smokeInput && iteration == 4) {
            fprintf(stderr, "[androidrt] smoke: injecting BUTTON_A down/up\n");
            android_inputInjectKey(0, AINPUT_SOURCE_GAMEPAD, AKEY_EVENT_ACTION_DOWN, AKEYCODE_BUTTON_A, 0);
            android_inputInjectKey(0, AINPUT_SOURCE_GAMEPAD, AKEY_EVENT_ACTION_UP, AKEYCODE_BUTTON_A, 0);
            smokeInput = 0;
        }
        if (queue) deliverInput(queue);
        apak_jniPump();
        event = runner_nextEvent();
        if (event == 4 /* APP_EVENT_EXIT */) runner->running = 0;
        else if (!event) android_pollOnce(16);
    }

    callHandleBool("onWindowFocusChangedNative", 0);
    fprintf(stderr, "[androidrt] lifecycle: onPause\n");
    callHandle("onPauseNative");
    apak_jniEvent(APAK_JNI_EV_PAUSE);
    fprintf(stderr, "[androidrt] lifecycle: onStop\n");
    callHandle("onStopNative");
    fprintf(stderr, "[androidrt] lifecycle: onSurfaceDestroyed\n");
    callHandle("onSurfaceDestroyedNative");
    android_activityClearWindow(&shell, android_currentWindow());
    if (queue) android_inputQueueDestroy(queue);
    apak_jniEvent(APAK_JNI_EV_DESTROY);
    fprintf(stderr, "[androidrt] lifecycle: onDestroy\n");
    callHandle("terminateNativeCode");
    apak_jniRelease(surface);
    apak_jniRelease(assets);
    return runner->exitCode;
}
