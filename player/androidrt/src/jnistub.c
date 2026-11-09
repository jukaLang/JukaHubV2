/* jnistub.c — a JavaVM/JNIEnv with no Java behind it.
 *
 * NativeActivity hands every game activity->vm, ->env and ->clazz, and many
 * games reach for them early (locale, display metrics, the soft keyboard,
 * "is this a TV?"). With NULL there, the first `(*vm)->AttachCurrentThread`
 * is a segfault before the launch log says anything useful. This stub keeps
 * the run going and logs WHAT the game asked Java for — the measurement
 * Milestone 1 needs:
 *
 *   [androidrt][jni] FindClass("com/example/game/MainActivity") -> null
 *   [androidrt][jni] GetMethodID(getWindowManager, ()Landroid/view/WindowManager;) -> null
 *
 * Semantics: classes, methods and fields are never found (NULL), calls on
 * them return 0/NULL/false (float and double calls return 0.0 in the FP
 * register, not garbage), no exception is ever pending, references are
 * their own identity, strings made with NewStringUTF round-trip, and the
 * invoke interface (GetEnv, AttachCurrentThread, ...) succeeds. Every
 * function of the 229-entry JNI 1.6 table exists; each logs its first three
 * calls. ANDROIDPAK_JNI=off restores the old NULL vm/env/clazz.
 *
 * Since the real JNI layer (jni.h, jnienv.c) this file only builds the
 * Windows smoke subset, which has no ELF loader and no class packs: the
 * lifecycle hooks activity.c calls are no-ops here.
 */
#define _GNU_SOURCE
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "actbridge.h"

#define JNI_TAG "[androidrt][jni] "
#define JNI_SLOTS 233 /* reserved0..3 + GetVersion .. GetObjectRefType */
#define JNI_VERSION_1_6 0x00010006

/* Names of the table entries, in JNI specification order. */
static const char *const kJniNames[JNI_SLOTS] = {
    "reserved0", "reserved1", "reserved2", "reserved3", "GetVersion", "DefineClass", "FindClass",
    "FromReflectedMethod", "FromReflectedField", "ToReflectedMethod", "GetSuperclass", "IsAssignableFrom",
    "ToReflectedField", "Throw", "ThrowNew", "ExceptionOccurred", "ExceptionDescribe", "ExceptionClear",
    "FatalError", "PushLocalFrame", "PopLocalFrame", "NewGlobalRef", "DeleteGlobalRef", "DeleteLocalRef",
    "IsSameObject", "NewLocalRef", "EnsureLocalCapacity", "AllocObject", "NewObject", "NewObjectV", "NewObjectA",
    "GetObjectClass", "IsInstanceOf", "GetMethodID",
    "CallObjectMethod", "CallObjectMethodV", "CallObjectMethodA",
    "CallBooleanMethod", "CallBooleanMethodV", "CallBooleanMethodA",
    "CallByteMethod", "CallByteMethodV", "CallByteMethodA",
    "CallCharMethod", "CallCharMethodV", "CallCharMethodA",
    "CallShortMethod", "CallShortMethodV", "CallShortMethodA",
    "CallIntMethod", "CallIntMethodV", "CallIntMethodA",
    "CallLongMethod", "CallLongMethodV", "CallLongMethodA",
    "CallFloatMethod", "CallFloatMethodV", "CallFloatMethodA",
    "CallDoubleMethod", "CallDoubleMethodV", "CallDoubleMethodA",
    "CallVoidMethod", "CallVoidMethodV", "CallVoidMethodA",
    "CallNonvirtualObjectMethod", "CallNonvirtualObjectMethodV", "CallNonvirtualObjectMethodA",
    "CallNonvirtualBooleanMethod", "CallNonvirtualBooleanMethodV", "CallNonvirtualBooleanMethodA",
    "CallNonvirtualByteMethod", "CallNonvirtualByteMethodV", "CallNonvirtualByteMethodA",
    "CallNonvirtualCharMethod", "CallNonvirtualCharMethodV", "CallNonvirtualCharMethodA",
    "CallNonvirtualShortMethod", "CallNonvirtualShortMethodV", "CallNonvirtualShortMethodA",
    "CallNonvirtualIntMethod", "CallNonvirtualIntMethodV", "CallNonvirtualIntMethodA",
    "CallNonvirtualLongMethod", "CallNonvirtualLongMethodV", "CallNonvirtualLongMethodA",
    "CallNonvirtualFloatMethod", "CallNonvirtualFloatMethodV", "CallNonvirtualFloatMethodA",
    "CallNonvirtualDoubleMethod", "CallNonvirtualDoubleMethodV", "CallNonvirtualDoubleMethodA",
    "CallNonvirtualVoidMethod", "CallNonvirtualVoidMethodV", "CallNonvirtualVoidMethodA",
    "GetFieldID", "GetObjectField", "GetBooleanField", "GetByteField", "GetCharField", "GetShortField",
    "GetIntField", "GetLongField", "GetFloatField", "GetDoubleField", "SetObjectField", "SetBooleanField",
    "SetByteField", "SetCharField", "SetShortField", "SetIntField", "SetLongField", "SetFloatField",
    "SetDoubleField", "GetStaticMethodID",
    "CallStaticObjectMethod", "CallStaticObjectMethodV", "CallStaticObjectMethodA",
    "CallStaticBooleanMethod", "CallStaticBooleanMethodV", "CallStaticBooleanMethodA",
    "CallStaticByteMethod", "CallStaticByteMethodV", "CallStaticByteMethodA",
    "CallStaticCharMethod", "CallStaticCharMethodV", "CallStaticCharMethodA",
    "CallStaticShortMethod", "CallStaticShortMethodV", "CallStaticShortMethodA",
    "CallStaticIntMethod", "CallStaticIntMethodV", "CallStaticIntMethodA",
    "CallStaticLongMethod", "CallStaticLongMethodV", "CallStaticLongMethodA",
    "CallStaticFloatMethod", "CallStaticFloatMethodV", "CallStaticFloatMethodA",
    "CallStaticDoubleMethod", "CallStaticDoubleMethodV", "CallStaticDoubleMethodA",
    "CallStaticVoidMethod", "CallStaticVoidMethodV", "CallStaticVoidMethodA",
    "GetStaticFieldID", "GetStaticObjectField", "GetStaticBooleanField", "GetStaticByteField",
    "GetStaticCharField", "GetStaticShortField", "GetStaticIntField", "GetStaticLongField",
    "GetStaticFloatField", "GetStaticDoubleField", "SetStaticObjectField", "SetStaticBooleanField",
    "SetStaticByteField", "SetStaticCharField", "SetStaticShortField", "SetStaticIntField",
    "SetStaticLongField", "SetStaticFloatField", "SetStaticDoubleField", "NewString", "GetStringLength",
    "GetStringChars", "ReleaseStringChars", "NewStringUTF", "GetStringUTFLength", "GetStringUTFChars",
    "ReleaseStringUTFChars", "GetArrayLength", "NewObjectArray", "GetObjectArrayElement",
    "SetObjectArrayElement", "NewBooleanArray", "NewByteArray", "NewCharArray", "NewShortArray",
    "NewIntArray", "NewLongArray", "NewFloatArray", "NewDoubleArray", "GetBooleanArrayElements",
    "GetByteArrayElements", "GetCharArrayElements", "GetShortArrayElements", "GetIntArrayElements",
    "GetLongArrayElements", "GetFloatArrayElements", "GetDoubleArrayElements",
    "ReleaseBooleanArrayElements", "ReleaseByteArrayElements", "ReleaseCharArrayElements",
    "ReleaseShortArrayElements", "ReleaseIntArrayElements", "ReleaseLongArrayElements",
    "ReleaseFloatArrayElements", "ReleaseDoubleArrayElements", "GetBooleanArrayRegion",
    "GetByteArrayRegion", "GetCharArrayRegion", "GetShortArrayRegion", "GetIntArrayRegion",
    "GetLongArrayRegion", "GetFloatArrayRegion", "GetDoubleArrayRegion", "SetBooleanArrayRegion",
    "SetByteArrayRegion", "SetCharArrayRegion", "SetShortArrayRegion", "SetIntArrayRegion",
    "SetLongArrayRegion", "SetFloatArrayRegion", "SetDoubleArrayRegion", "RegisterNatives",
    "UnregisterNatives", "MonitorEnter", "MonitorExit", "GetJavaVM", "GetStringRegion", "GetStringUTFRegion",
    "GetPrimitiveArrayCritical", "ReleasePrimitiveArrayCritical", "GetStringCritical",
    "ReleaseStringCritical", "NewWeakGlobalRef", "DeleteWeakGlobalRef", "ExceptionCheck",
    "NewDirectByteBuffer", "GetDirectBufferAddress", "GetDirectBufferCapacity", "GetObjectRefType",
};

typedef const void *JniTable[JNI_SLOTS];
static JniTable g_table;
static const void *g_envPtr = g_table;   /* JNIEnv: pointer to the table pointer */
static const void *g_invoke[8];
static const void *g_vmPtr = g_invoke;   /* JavaVM: pointer to the invoke table pointer */
static unsigned char g_calls[JNI_SLOTS];
static pthread_mutex_t g_jniLock = PTHREAD_MUTEX_INITIALIZER;

/* Handles the stub hands out: objects/classes the game can pass back. */
#define JNI_MAGIC 0x4a4e4953u /* "JNIS" */
typedef struct {
    uint32_t magic;
    int kind; /* 1 activity object, 2 class, 3 string */
    char *utf;
} JniHandle;
static JniHandle g_activityObject = { JNI_MAGIC, 1, NULL };
static JniHandle g_objectClass = { JNI_MAGIC, 2, NULL };

static int counted(int slot) {
    int first;
    pthread_mutex_lock(&g_jniLock);
    first = g_calls[slot] < 3;
    if (g_calls[slot] < 255) g_calls[slot]++;
    pthread_mutex_unlock(&g_jniLock);
    return first;
}

static void logCall(int slot, const char *detail) {
    if (counted(slot)) fprintf(stderr, JNI_TAG "%s%s%s\n", kJniNames[slot], detail ? " " : "", detail ? detail : "");
}

static int isHandle(const void *p) {
    return p && ((const JniHandle *)p)->magic == JNI_MAGIC;
}

/* ── generic stubs: one per slot, so the log can name the function ──── */
static long jniGeneric(int slot) {
    logCall(slot, "-> 0 (no Java in Tier 1)");
    return 0;
}
#define JG(n) \
    static long jg_##n(void) { return jniGeneric(n); }
#define JG8(b) JG(b##0) JG(b##1) JG(b##2) JG(b##3) JG(b##4) JG(b##5) JG(b##6) JG(b##7)
#define JG10(b) JG(b##0) JG(b##1) JG(b##2) JG(b##3) JG(b##4) JG(b##5) JG(b##6) JG(b##7) JG(b##8) JG(b##9)
JG(0) JG(1) JG(2) JG(3) JG(4) JG(5) JG(6) JG(7) JG(8) JG(9)
JG10(1) JG10(2) JG10(3) JG10(4) JG10(5) JG10(6) JG10(7) JG10(8) JG10(9)
JG10(10) JG10(11) JG10(12) JG10(13) JG10(14) JG10(15) JG10(16) JG10(17) JG10(18) JG10(19)
JG10(20) JG10(21) JG(220) JG(221) JG(222) JG(223) JG(224) JG(225) JG(226) JG(227) JG(228) JG(229)
JG(230) JG(231) JG(232)

#define JR(n) (const void *)jg_##n
#define JR10(b) JR(b##0), JR(b##1), JR(b##2), JR(b##3), JR(b##4), JR(b##5), JR(b##6), JR(b##7), JR(b##8), JR(b##9)
static const void *const kGeneric[JNI_SLOTS] = {
    JR(0), JR(1), JR(2), JR(3), JR(4), JR(5), JR(6), JR(7), JR(8), JR(9),
    JR10(1), JR10(2), JR10(3), JR10(4), JR10(5), JR10(6), JR10(7), JR10(8), JR10(9),
    JR10(10), JR10(11), JR10(12), JR10(13), JR10(14), JR10(15), JR10(16), JR10(17), JR10(18), JR10(19),
    JR10(20), JR10(21), JR(220), JR(221), JR(222), JR(223), JR(224), JR(225), JR(226), JR(227), JR(228),
    JR(229), JR(230), JR(231), JR(232),
};

/* float/double results live in the FP register: a long-returning stub
 * would leave garbage there */
static float jniFloat(void) { return 0.0f; }
static double jniDouble(void) { return 0.0; }

/* ── the functions with real behaviour ──────────────────────────────── */
static int32_t jni_GetVersion(void *env) {
    (void)env;
    return JNI_VERSION_1_6;
}
static void *jni_FindClass(void *env, const char *name) {
    char d[300];
    (void)env;
    snprintf(d, sizeof d, "(\"%.250s\") -> null", name ? name : "(null)");
    logCall(6, d);
    return NULL;
}
static void *jni_ExceptionOccurred(void *env) {
    (void)env;
    return NULL;
}
static void jni_ExceptionVoid(void *env) { (void)env; }
static void jni_FatalError(void *env, const char *msg) {
    (void)env;
    fprintf(stderr, JNI_TAG "FatalError(\"%s\")\n", msg ? msg : "");
    fflush(stderr);
    abort();
}
static int32_t jni_Zero(void *env) {
    (void)env;
    return 0;
}
static void *jni_PopLocalFrame(void *env, void *result) {
    (void)env;
    return result;
}
static void *jni_Identity(void *env, void *ref) {
    (void)env;
    return ref;
}
static void jni_DeleteRef(void *env, void *ref) {
    (void)env;
    (void)ref;
}
static uint8_t jni_IsSameObject(void *env, void *a, void *b) {
    (void)env;
    return a == b;
}
static void *jni_GetObjectClass(void *env, void *obj) {
    (void)env;
    return isHandle(obj) ? (void *)&g_objectClass : NULL;
}
static void *jni_GetMember(int slot, void *cls, const char *name, const char *sig) {
    char d[400];
    snprintf(d, sizeof d, "(%s%.120s, %.200s) -> null", cls ? "" : "class=null, ", name ? name : "(null)",
             sig ? sig : "(null)");
    logCall(slot, d);
    return NULL;
}
static void *jni_GetMethodID(void *env, void *cls, const char *n, const char *s) {
    (void)env;
    return jni_GetMember(33, cls, n, s);
}
static void *jni_GetFieldID(void *env, void *cls, const char *n, const char *s) {
    (void)env;
    return jni_GetMember(94, cls, n, s);
}
static void *jni_GetStaticMethodID(void *env, void *cls, const char *n, const char *s) {
    (void)env;
    return jni_GetMember(113, cls, n, s);
}
static void *jni_GetStaticFieldID(void *env, void *cls, const char *n, const char *s) {
    (void)env;
    return jni_GetMember(144, cls, n, s);
}
static void *jni_NewStringUTF(void *env, const char *utf) {
    JniHandle *h;
    (void)env;
    if (!utf) return NULL;
    h = (JniHandle *)calloc(1, sizeof *h);
    if (!h) return NULL;
    h->magic = JNI_MAGIC;
    h->kind = 3;
    h->utf = strdup(utf);
    return h;
}
static const char *jni_GetStringUTFChars(void *env, void *str, uint8_t *isCopy) {
    (void)env;
    if (isCopy) *isCopy = 0;
    if (!str) return NULL;
    if (isHandle(str) && ((JniHandle *)str)->kind == 3) return ((JniHandle *)str)->utf;
    return "";
}
static int32_t jni_GetStringUTFLength(void *env, void *str) {
    const char *s = jni_GetStringUTFChars(env, str, NULL);
    return s ? (int32_t)strlen(s) : 0;
}
static void jni_ReleaseStringUTFChars(void *env, void *str, const char *chars) {
    (void)env;
    (void)str;
    (void)chars;
}
static int32_t jni_RegisterNatives(void *env, void *cls, const void *methods, int32_t n) {
    char d[64];
    (void)env;
    (void)cls;
    (void)methods;
    snprintf(d, sizeof d, "(%d methods) -> ok, never called", (int)n);
    logCall(215, d);
    return 0;
}
static int32_t jni_GetJavaVM(void *env, void **vm) {
    (void)env;
    if (vm) *vm = (void *)&g_vmPtr;
    return 0;
}

/* ── JavaVM ───────────────────────────────────────────────────────────── */
static int32_t vm_DestroyJavaVM(void *vm) {
    (void)vm;
    return 0;
}
static int32_t vm_Attach(void *vm, void **env, void *args) {
    (void)vm;
    (void)args;
    if (env) *env = (void *)&g_envPtr;
    return 0;
}
static int32_t vm_Detach(void *vm) {
    (void)vm;
    return 0;
}
static int32_t vm_GetEnv(void *vm, void **env, int32_t version) {
    (void)vm;
    (void)version;
    if (env) *env = (void *)&g_envPtr;
    return 0;
}

static pthread_once_t g_jniOnce = PTHREAD_ONCE_INIT;

static void jniBuild(void) {
    static const int floatSlots[] = { 55, 56, 57, 85, 86, 87, 102, 135, 136, 137, 152 };
    static const int doubleSlots[] = { 58, 59, 60, 88, 89, 90, 103, 138, 139, 140, 153 };
    size_t i;
    for (i = 0; i < JNI_SLOTS; i++) g_table[i] = kGeneric[i];
    for (i = 0; i < sizeof floatSlots / sizeof floatSlots[0]; i++) g_table[floatSlots[i]] = (const void *)jniFloat;
    for (i = 0; i < sizeof doubleSlots / sizeof doubleSlots[0]; i++) g_table[doubleSlots[i]] = (const void *)jniDouble;
    for (i = 0; i < 4; i++) g_table[i] = NULL; /* reserved */
    g_table[4] = (const void *)jni_GetVersion;
    g_table[6] = (const void *)jni_FindClass;
    g_table[15] = (const void *)jni_ExceptionOccurred;
    g_table[16] = (const void *)jni_ExceptionVoid;
    g_table[17] = (const void *)jni_ExceptionVoid;
    g_table[18] = (const void *)jni_FatalError;
    g_table[19] = (const void *)jni_Zero;          /* PushLocalFrame */
    g_table[20] = (const void *)jni_PopLocalFrame;
    g_table[21] = (const void *)jni_Identity;      /* NewGlobalRef */
    g_table[22] = (const void *)jni_DeleteRef;     /* DeleteGlobalRef */
    g_table[23] = (const void *)jni_DeleteRef;     /* DeleteLocalRef */
    g_table[24] = (const void *)jni_IsSameObject;
    g_table[25] = (const void *)jni_Identity;      /* NewLocalRef */
    g_table[26] = (const void *)jni_Zero;          /* EnsureLocalCapacity */
    g_table[31] = (const void *)jni_GetObjectClass;
    g_table[33] = (const void *)jni_GetMethodID;
    g_table[94] = (const void *)jni_GetFieldID;
    g_table[113] = (const void *)jni_GetStaticMethodID;
    g_table[144] = (const void *)jni_GetStaticFieldID;
    g_table[167] = (const void *)jni_NewStringUTF;
    g_table[168] = (const void *)jni_GetStringUTFLength;
    g_table[169] = (const void *)jni_GetStringUTFChars;
    g_table[170] = (const void *)jni_ReleaseStringUTFChars;
    g_table[215] = (const void *)jni_RegisterNatives;
    g_table[216] = (const void *)jni_Zero;         /* UnregisterNatives */
    g_table[217] = (const void *)jni_Zero;         /* MonitorEnter */
    g_table[218] = (const void *)jni_Zero;         /* MonitorExit */
    g_table[219] = (const void *)jni_GetJavaVM;
    g_table[226] = (const void *)jni_Identity;     /* NewWeakGlobalRef */
    g_table[227] = (const void *)jni_DeleteRef;    /* DeleteWeakGlobalRef */
    g_table[228] = (const void *)jni_Zero;         /* ExceptionCheck: never pending */
    g_invoke[3] = (const void *)vm_DestroyJavaVM;
    g_invoke[4] = (const void *)vm_Attach;         /* AttachCurrentThread */
    g_invoke[5] = (const void *)vm_Detach;
    g_invoke[6] = (const void *)vm_GetEnv;
    g_invoke[7] = (const void *)vm_Attach;         /* AttachCurrentThreadAsDaemon */
}

static int jniEnabled(void) {
    const char *v = getenv("ANDROIDPAK_JNI");
    return !(v && strcmp(v, "off") == 0);
}

void *apak_jniVM(void) {
    if (!jniEnabled()) return NULL;
    pthread_once(&g_jniOnce, jniBuild);
    return (void *)&g_vmPtr;
}

void *apak_jniEnv(void) {
    if (!jniEnabled()) return NULL;
    pthread_once(&g_jniOnce, jniBuild);
    return (void *)&g_envPtr;
}

void *apak_jniActivityObject(void) { return jniEnabled() ? (void *)&g_activityObject : NULL; }

/* ── lifecycle hooks (jni.h) — nothing to tell without class packs ───── */
void apak_jniEvent(int event);
void apak_jniPump(void);
void apak_jniExceptionReport(void *env, const char *where);
void apak_jniEvent(int event) { (void)event; }
void apak_jniPump(void) {}
void apak_jniExceptionReport(void *env, const char *where) {
    (void)env;
    (void)where;
}
