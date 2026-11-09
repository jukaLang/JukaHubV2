/* apkmeta.h — what the runtime needs to know about an APK besides its
 * native code: the binary AndroidManifest.xml (package, version, launcher
 * activity, the NativeActivity lib_name/func_name meta-data, SDK levels)
 * and the class index of classes*.dex (every Java class the app defines and
 * its superclass) — the JNI layer answers FindClass for app classes from it
 * instead of guessing.
 */
#ifndef ANDROIDRT_APKMETA_H
#define ANDROIDRT_APKMETA_H

#include <stddef.h>
#include <stdint.h>

#include "apkzip.h"

typedef struct ApakManifest {
    char package[256];
    char versionName[96];
    int64_t versionCode;
    int minSdk;
    int targetSdk;
    char activity[512];  /* launcher activity, fully qualified, dotted */
    char libName[128];   /* <meta-data android.app.lib_name>, "" if absent */
    char funcName[128];  /* <meta-data android.app.func_name> */
} ApakManifest;

/* Parse a binary (AXML) manifest. 0 on success. */
int apak_manifestParse(const uint8_t *data, size_t len, ApakManifest *out, char *err, size_t cap);

/* <stagedRoot>/AndroidManifest.xml, else the APK's own entry. */
int apak_manifestLoad(const char *stagedRoot, ApakZip *apk, ApakManifest *out, char *err, size_t cap);

typedef struct ApakDexIndex ApakDexIndex;

/* Index classes.dex, classes2.dex, ... of the APK. NULL when the APK has no
 * DEX (a pure-native APK) or it cannot be read; err says which. */
ApakDexIndex *apak_dexIndexBuild(ApakZip *apk, char *err, size_t cap);
/* 1 when the APK defines className ("com/foo/Bar"); *superName is its
 * superclass ("android/app/NativeActivity") or NULL for java/lang/Object. */
int apak_dexLookup(const ApakDexIndex *ix, const char *className, const char **superName);
size_t apak_dexCount(const ApakDexIndex *ix);
size_t apak_dexFiles(const ApakDexIndex *ix);
void apak_dexFree(ApakDexIndex *ix);

#endif /* ANDROIDRT_APKMETA_H */
