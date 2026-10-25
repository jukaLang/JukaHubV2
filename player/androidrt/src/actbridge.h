/* actbridge.h — the subset of the Android NDK ABI AndroidRT implements,
 * plus the runner↔shim bridge declared here once for both sides.
 *
 * Field order in ANativeActivity is not style: NDK games access these
 * structs through compiled-in offsets, so the layout must stay
 * byte-compatible with the NDK headers. Do not reorder.
 */
#ifndef JUKAHUB_ACTBRIDGE_H
#define JUKAHUB_ACTBRIDGE_H

#include <stddef.h>
#include <stdint.h>

#define ANDROIDRT_SDK_VERSION 28 /* plays as Android 9: covers GLES3 assets */

/* Symbol export for shim .so builds (they compile with
 * -fvisibility=hidden and everything not marked leaks nothing).  On Windows
 * smoke builds of the fake game this becomes __declspec(dllexport). */
#if defined(_WIN32)
#define ANDROIDRT_EXPORT __declspec(dllexport)
#else
#define ANDROIDRT_EXPORT __attribute__((visibility("default")))
#endif

typedef void ANativeWindow;
typedef void AInputQueue;
typedef void AAssetManager;
typedef void AConfiguration;

/* AInputEvent stays opaque exactly like the NDK: games reach events only
 * through the inputshim.c accessors, so pointer-to-internal keeps games
 * compiled against the real header binary-compatible. */
typedef void AInputEvent;
typedef void ALooper;
typedef void (*ALooper_callbackFunc)(int fd, int events, void *data);

/* ARect is a value type in the real NDK (four int32s) — the layout, not just
 * the pointer, matters because ANativeActivity embeds one. */
typedef struct ARect {
    int32_t left;
    int32_t top;
    int32_t right;
    int32_t bottom;
} ARect;

/* ANativeActivity — layout fixed by the NDK (android/native_activity.h). */
typedef struct ANativeActivity {
    struct ANativeActivityCallbacks *callbacks;
    void *vm;                     /* JavaVM*  — always NULL here          */
    void *env;                    /* JNIEnv*  — always NULL here          */
    void *clazz;                  /* activity object — NULL here          */
    const char *internalDataPath;
    const char *externalDataPath;
    int32_t sdkVersion;
    void *instance;               /* the runner's own state               */
    AAssetManager *assetManager;
    const char *obbPath;          /* unused, ""                            */
    /* Everything below is a byte-exact append of the NDK's tail fields, in
     * NDK order. Games built against the real header reach these by offset,
     * so appending (not interleaving) is what keeps older-compiled callers
     * aligned. */
    AInputQueue *inputQueue;      /* owned by the input shim (inputshim.c) */
    ANativeWindow *window;        /* the current window, as Android keeps   */
    ARect contentRect;            /* only the changed callback uses it      */
} ANativeActivity;

typedef struct ANativeActivityCallbacks {
    void (*onStart)(ANativeActivity *activity);
    void (*onResume)(ANativeActivity *activity);
    void *(*onSaveInstanceState)(ANativeActivity *activity, size_t *outSize);
    void (*onPause)(ANativeActivity *activity);
    void (*onStop)(ANativeActivity *activity);
    void (*onDestroy)(ANativeActivity *activity);
    void (*onWindowFocusChanged)(ANativeActivity *activity, int hasFocus);
    void (*onNativeWindowCreated)(ANativeActivity *activity, ANativeWindow *window);
    void (*onNativeWindowResized)(ANativeActivity *activity, ANativeWindow *window);
    void (*onNativeWindowRedrawNeeded)(ANativeActivity *activity, ANativeWindow *window);
    void (*onNativeWindowDestroyed)(ANativeActivity *activity, ANativeWindow *window);
    void (*onInputQueueCreated)(ANativeActivity *activity, AInputQueue *queue);
    void (*onInputQueueDestroyed)(ANativeActivity *activity, AInputQueue *queue);
    void (*onContentRectChanged)(ANativeActivity *activity, const ARect *rect);
    void (*onConfigurationChanged)(ANativeActivity *activity);
    void (*onLowMemory)(ANativeActivity *activity);
} ANativeActivityCallbacks;

/* window WxH helpers, resolved by activity.c from the android shim */
int32_t ANativeWindow_getWidth(ANativeWindow *window);
int32_t ANativeWindow_getHeight(ANativeWindow *window);

/* Entry point every NativeActivity game exports from lib/<abi>.so. */
typedef void (*ANativeActivity_onCreate_t)(ANativeActivity *activity,
                                           void *savedState,
                                           size_t savedStateSize);

/* The runner's own loop state, handed to games as ANativeActivity.instance. */
typedef struct ActivityRunner {
    volatile int running;    /* cleared when the app should stop          */
    int exitCode;            /* filled on shutdown                        */
    const char *stagedRoot;  /* ANDROIDPAK_STAGED                         */
    const char *mainLib;     /* ANDROIDPAK_MAINLIB                        */
    const char *apkPath;     /* ANDROIDPAK_APK                            */
} ActivityRunner;

/* ── activity.c exports (used by the android shim and the runner) ────── */
void runOneActivity(ActivityRunner *runner, void *onCreate, void *gameHandle);
void runner_requestExit(ActivityRunner *runner, int code);

/* ── android shim exports (libandroid.so) used by the runner ─────────── */
void android_activityInit(ANativeActivity *activity, ActivityRunner *runner);
void android_activitySetWindow(ANativeActivity *activity, ANativeWindow *window);
void android_activityClearWindow(ANativeActivity *activity, ANativeWindow *window);
ANativeWindow *android_currentWindow(void);
AAssetManager *android_openAssetManager(const char *stagedRoot);
int android_pollOnce(int ms);
void android_wakeLooper(void);
int android_windowBackend(int width, int height);

/* Device-side input producer (evdev.c): reads /dev/input/event*, translates
 * BTN/ABS evdev codes to Android keycodes/axes (linux/input-event-codes.h)
 * and feeds android_inputInject*. Guarded by JUKAHUB_EVDEV: the device
 * cross build compiles it and only the runner calls this; the host smoke
 * build never does (no /dev/input on Windows). */
void android_inputStartEvdev(void);

/* ── input shim bridge (inputshim.c; compiled into the runner build and
 * into shims/libandroid.so alike — the AInputEvent/AInputQueue functions
 * themselves are ANDROIDRT_EXPORT, i.e. the game-facing ABI) ────────── */
AInputQueue *android_inputQueueCreate(void);
void android_inputQueueDestroy(AInputQueue *queue);
int android_inputHasEvents(void);
AInputQueue *android_inputQueueCurrent(void);
void android_inputInjectKey(int32_t deviceId, int32_t source, int32_t action,
                            int32_t keyCode, int32_t repeat);
void android_inputInjectAxis(int32_t deviceId, int32_t source, int32_t axis, float value);

/* ── the game-facing input ABI (androidshim.c + inputshim.c exports;
 * values are the NDK's) ───────────────────────────────────────────── */
enum {
    AINPUT_EVENT_TYPE_KEY    = 1,
    AINPUT_EVENT_TYPE_MOTION = 2,
    AINPUT_SOURCE_GAMEPAD  = 0x00000401,
    AINPUT_SOURCE_JOYSTICK = 0x01000101,
    AKEY_EVENT_ACTION_DOWN = 0,
    AKEY_EVENT_ACTION_UP   = 1,
    AKEYCODE_DPAD_UP       = 19,
    AKEYCODE_DPAD_DOWN     = 20,
    AKEYCODE_DPAD_LEFT     = 21,
    AKEYCODE_DPAD_RIGHT    = 22,
    AKEYCODE_BUTTON_A      = 96,
    AKEYCODE_BUTTON_B      = 97,
    AKEYCODE_BUTTON_X      = 99,
    AKEYCODE_BUTTON_Y      = 100,
    AKEYCODE_BUTTON_L1     = 102,
    AKEYCODE_BUTTON_R1     = 103,
    AKEYCODE_BUTTON_L2     = 104,
    AKEYCODE_BUTTON_R2     = 105,
    AKEYCODE_BUTTON_THUMBL = 106,
    AKEYCODE_BUTTON_THUMBR = 107,
    AKEYCODE_BUTTON_START  = 108,
    AKEYCODE_BUTTON_SELECT = 109,
    AKEYCODE_BUTTON_MODE   = 110,
};

int32_t AInputEvent_getType(const AInputEvent *event);
int32_t AInputEvent_getDeviceId(const AInputEvent *event);
int32_t AInputEvent_getSource(const AInputEvent *event);
int32_t AKeyEvent_getAction(const AInputEvent *event);
int32_t AKeyEvent_getKeyCode(const AInputEvent *event);
int32_t AKeyEvent_getRepeatCount(const AInputEvent *event);
int32_t AKeyEvent_getMetaState(const AInputEvent *event);
int32_t AMotionEvent_getAction(const AInputEvent *event);
size_t AMotionEvent_getPointerCount(const AInputEvent *event);
int32_t AMotionEvent_getPointerId(const AInputEvent *event, size_t pointerIndex);
float AMotionEvent_getAxisValue(const AInputEvent *event, int32_t axis, size_t pointerIndex);
float AMotionEvent_getX(const AInputEvent *event, size_t pointerIndex);
float AMotionEvent_getY(const AInputEvent *event, size_t pointerIndex);
float AMotionEvent_getPressure(const AInputEvent *event, size_t pointerIndex);
float AMotionEvent_getSize(const AInputEvent *event, size_t pointerIndex);
int64_t AMotionEvent_getDownTime(const AInputEvent *event);
size_t AMotionEvent_getHistorySize(const AInputEvent *event);

int32_t AInputQueue_getEvent(AInputQueue *queue, AInputEvent **outEvent);
int32_t AInputQueue_hasEvents(AInputQueue *queue);
int32_t AInputQueue_preDispatchEvent(AInputQueue *queue, AInputEvent *event);
void AInputQueue_finishEvent(AInputQueue *queue, AInputEvent *event, int handled);
void AInputQueue_attachLooper(AInputQueue *queue, ALooper *looper, int ident,
                              ALooper_callbackFunc callback, void *data);
void AInputQueue_detachLooper(AInputQueue *queue);

/* ALooper surface the glue path uses alongside the queue (androidshim.c). */
int ALooper_pollOnce(int timeoutMs, int *outFd, int *outEvents, void **outData);
int ALooper_pollAll(int timeoutMs, int *outFd, int *outEvents, void **outData);

#endif /* JUKAHUB_ACTBRIDGE_H */
