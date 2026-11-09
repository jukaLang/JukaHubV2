/* jni.h — AndroidRT's Java Native Interface (internal API).
 *
 * There is no Java VM in the runtime. jnienv.c implements the JavaVM and
 * JNIEnv function tables (JNI 1.6, the 233-slot table Android exposes) on a
 * small object model: classes registered BY NAME whose methods and fields
 * are implemented in C, objects behind reference-counted handles (strings,
 * primitive/object arrays, instances, class objects, direct buffers), local
 * reference frames per thread, global and weak references, pending
 * exceptions, RegisterNatives tables the runtime can call back into, and
 * AttachCurrentThread for game threads.
 *
 * What is NOT implemented is never faked silently:
 *   FindClass of a class nobody implements returns NULL (the APK's own DEX
 *   classes become "phantom" classes so their lookups still succeed);
 *   GetMethodID/GetFieldID of a member nobody implements returns a phantom
 *   member whose calls return 0/false/0.0/null; and every such name is
 *   logged ONCE — those [androidrt][jni] lines are the compatibility report.
 *
 * Class packs (jnipack_*.c) add the Java classes one game family needs and
 * are selected by the manifest's package name.
 */
#ifndef ANDROIDRT_JNI_H
#define ANDROIDRT_JNI_H

#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>

#define APAK_JNI_VERSION_1_6 0x00010006
#define APAK_JNI_OK          0
#define APAK_JNI_ERR         (-1)
#define APAK_JNI_EDETACHED   (-2)
#define APAK_JNI_EVERSION    (-3)
#define APAK_JNI_MAXARGS     32

/* jvalue: the same 8-byte union every JNI caller passes. */
typedef union ApakJValue {
    uint8_t z;
    int8_t b;
    uint16_t c;
    int16_t s;
    int32_t i;
    int64_t j;
    float f;
    double d;
    void *l;
} ApakJValue;

typedef struct ApakJObject ApakJObject;
typedef struct ApakJClass ApakJClass;
typedef struct ApakJMethod ApakJMethod;

/* One call into a C-implemented Java method. Objects in args are borrowed;
 * an object returned in ret.l becomes a local reference of the caller (the
 * implementation must not release it). */
typedef struct ApakJCall {
    void *env;                 /* the calling thread's JNIEnv*            */
    ApakJMethod *method;
    ApakJObject *self;         /* NULL for static methods                 */
    const ApakJValue *args;
    int nargs;
    ApakJValue ret;
} ApakJCall;

typedef void (*ApakJImpl)(ApakJCall *c);

typedef struct ApakJMethodDef {
    const char *name;
    const char *sig;           /* JNI descriptor, "(Ljava/lang/String;)I" */
    int isStatic;
    ApakJImpl impl;
} ApakJMethodDef;

typedef struct ApakJFieldDef {
    const char *name;
    const char *sig;
    int isStatic;
} ApakJFieldDef;

typedef struct ApakJClassDef {
    const char *name;               /* "com/mojang/minecraftpe/MainActivity" */
    const char *super;              /* NULL: java/lang/Object                */
    const ApakJMethodDef *methods;  /* {NULL}-terminated, may be NULL        */
    const ApakJFieldDef *fields;    /* {NULL}-terminated, may be NULL        */
} ApakJClassDef;

/* What the runtime tells the JNI layer about this launch. */
typedef struct ApakJniConfig {
    const char *packageName;   /* manifest package, "" when unknown       */
    const char *activityClass; /* launcher activity, dotted, "" if unknown */
    const char *versionName;
    const char *apkPath;
    const char *stagedRoot;
    const char *libDir;        /* staged lib/<abi>                       */
    const char *dataRoot;      /* ANDROIDPAK_DATA                        */
    const char *filesDir;      /* <data>/files    (internal storage)     */
    const char *externalDir;   /* <data>/external (external storage)     */
    const char *obbDir;
    const char *cacheDir;
    const char *model;         /* "TrimUI Smart Pro"                     */
    const char *board;         /* "tg5040"                               */
    int panelW, panelH;        /* the window the game draws into         */
    float dpi;                 /* physical dots per inch of the panel    */
    int sdkVersion;
    int gameActivity;          /* the entry is GameActivity, not NativeActivity */
    void *assetManager;        /* AAssetManager* behind AssetManager objects */
    /* exported symbol of the game's loaded libraries (Java_* natives) */
    void *(*resolveSymbol)(const char *name);
    /* 1 when the APK's DEX defines the class; *super = its superclass */
    int (*classInApk)(const char *name, const char **superName);
} ApakJniConfig;

/* Lifecycle points the runtime reports to class packs. */
enum {
    APAK_JNI_EV_BEFORE_ONCREATE = 1, /* JNI_OnLoad done, activity object exists */
    APAK_JNI_EV_AFTER_ONCREATE,
    APAK_JNI_EV_RESUME,
    APAK_JNI_EV_PAUSE,
    APAK_JNI_EV_DESTROY,
};

typedef struct ApakJniPack {
    const char *name;                 /* "minecraft"                       */
    const char *const *packages;      /* NULL-terminated package names    */
    void (*define)(const ApakJniConfig *cfg);
    const char *activityClass;        /* JNI name of the activity class it implements */
    void (*event)(int event);
} ApakJniPack;

/* ── runner side ─────────────────────────────────────────────────────── */
void apak_jniAddPack(const ApakJniPack *pack);      /* before apak_jniInit */
void apak_jniInit(const ApakJniConfig *cfg);         /* main thread, once  */
const ApakJniConfig *apak_jniConfig(void);
int apak_jniEnabled(void);
/* System.loadLibrary's half: call a library's JNI_OnLoad. */
int apak_jniCallOnLoad(void *onLoad, const char *libName);
void apak_jniEvent(int event);
void apak_jniPump(void);                              /* main loop tick */
void apak_jniReport(void);                            /* at exit        */

/* ── definitions (class packs) ───────────────────────────────────────── */
void apak_jniDefine(const ApakJClassDef *def);
void apak_jniDefineAll(const ApakJClassDef *defs);   /* {NULL}-terminated */
void apak_jniDefineBase(void);                        /* jnibase.c       */

/* ── helpers for method implementations ─────────────────────────────── */
void *apak_jniMainEnv(void);
void *apak_jniThreadEnv(void);            /* this thread's env (attaches) */
ApakJObject *apak_jniActivity(void);

/* New objects are "floating" (no reference yet): return them in ret.l,
 * store them in an array/field, or apak_jniRetain them. */
ApakJObject *apak_jniNewString(const char *utf8);
ApakJObject *apak_jniNewStringUtf16(const uint16_t *chars, int32_t len);
ApakJObject *apak_jniNewInstance(const char *className);
ApakJObject *apak_jniNewArray(char elemType, int32_t len, const char *elemClass);
ApakJObject *apak_jniNewByteArray(const void *data, size_t len);
ApakJObject *apak_jniNewStringArray(const char *const *items, size_t n);
int apak_jniArraySet(ApakJObject *array, int32_t index, ApakJObject *value);
void *apak_jniArrayData(ApakJObject *array, int32_t *len, char *elemType);
const char *apak_jniStringUtf(ApakJObject *str);   /* modified UTF-8, NULL if not a string */
int32_t apak_jniStringLength(ApakJObject *str);
const uint16_t *apak_jniStringChars(ApakJObject *str);
/* Replace a string object's contents (java/lang/String constructors). */
void apak_jniStringSet(ApakJObject *str, const uint16_t *chars, int32_t len);
void apak_jniStringSetUtf8(ApakJObject *str, const char *utf8, size_t len);
/* Class.forName / ClassLoader.loadClass ("com.foo.Bar" or "com/foo/Bar"). */
ApakJClass *apak_jniLoadClass(const char *name);
/* The invocation API's JNI_GetCreatedJavaVMs, for libraries that ask it. */
int32_t apak_jniGetCreatedJavaVMs(void **vms, int32_t cap, int32_t *count);
ApakJObject *apak_jniRetain(ApakJObject *o);
void apak_jniRelease(ApakJObject *o);

int apak_jniFieldSet(ApakJObject *obj, const char *name, ApakJValue v);
int apak_jniFieldGet(ApakJObject *obj, const char *name, ApakJValue *v);
int apak_jniStaticSet(const char *className, const char *name, ApakJValue v);
void apak_jniSetPayload(ApakJObject *obj, void *payload, void (*freeFn)(void *));
void *apak_jniPayload(ApakJObject *obj);

ApakJClass *apak_jniFindClass(const char *name);     /* registered or APK class */
ApakJClass *apak_jniClassOf(ApakJObject *obj);
ApakJClass *apak_jniAsClass(ApakJObject *obj);       /* a class object as its class */
ApakJClass *apak_jniSuperclass(ApakJClass *cls);
const char *apak_jniClassName(const ApakJClass *cls);
int apak_jniIsInstance(ApakJObject *obj, const char *className);
/* Invoke a Java method of self by name (virtual dispatch, phantoms
 * included). 0 when a method was invoked. */
int apak_jniCallMethod(void *env, ApakJObject *self, const char *name, const char *sig, const ApakJValue *args,
                       ApakJValue *ret);

/* ── device facts shared by the class packs (jnibase.c) ─────────────── */
int64_t apak_jniMemInfo(const char *key);  /* /proc/meminfo field in bytes, -1 unknown */
int64_t apak_jniProcessRss(void);          /* this process's resident set, bytes */
void apak_jniLocale(char *out, size_t cap); /* "en_US" from LANG/LC_ALL */
int apak_jniNetworkUp(void);                /* a non-loopback interface is up */
void apak_jniFinish(void);                  /* Activity.finish(): end the run */

/* Input events as android.view.KeyEvent / MotionEvent objects (the
 * GameActivity bridge hands these to the game's natives). Times are ns. */
typedef struct ApakKeyEventData {
    int32_t deviceId, source, action, keyCode, repeatCount, metaState, flags, scanCode;
    int64_t eventTimeNs, downTimeNs;
} ApakKeyEventData;

#define APAK_MOTION_AXES 48
typedef struct ApakMotionEventData {
    int32_t deviceId, source, action, buttonState, metaState, flags;
    int64_t eventTimeNs, downTimeNs;
    float axes[APAK_MOTION_AXES];
} ApakMotionEventData;

ApakJObject *apak_jniNewKeyEvent(const ApakKeyEventData *d);
ApakJObject *apak_jniNewMotionEvent(const ApakMotionEventData *d);

void apak_jniThrow(void *env, const char *className, const char *message);
int apak_jniExceptionPending(void *env);
/* Describe and clear a pending exception (runtime-initiated calls). */
void apak_jniExceptionReport(void *env, const char *where);

/* Call a native method of the game: RegisterNatives entries first, then the
 * exported Java_<class>_<method> symbols. self NULL = static. Returns 0 when
 * a native was called, -1 when none exists (logged once). An object result
 * is retained for the caller (apak_jniRelease it). */
int apak_jniCallNative(void *env, ApakJObject *self, const char *className, const char *name, const char *sig,
                       const ApakJValue *args, ApakJValue *ret);
int apak_jniHasNative(const char *className, const char *name, const char *sig);
/* The descriptor a native was registered with ("" when unknown). */
const char *apak_jniNativeSig(const char *className, const char *name);

/* Run fn(arg) on the main loop (Android's Handler.post). */
void apak_jniPost(void (*fn)(void *arg), void *arg);

/* One [androidrt][jni] line per key for the whole run. */
void apak_jniLogOnce(const char *key, const char *fmt, ...) __attribute__((format(printf, 2, 3)));

#endif /* ANDROIDRT_JNI_H */
