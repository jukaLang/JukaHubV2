/* rt_dlopen / rt_dlsym / rt_dlclose / rt_dlerror — the dlopen interface the
 * runner and the OpenSL ES shim need, ported to Windows (LoadLibrary) so the
 * exact device sources can be compiled and smoke-tested on the host.
 *
 * API is small because that is all AndroidRT uses: opening the game main
 * library, looking up ANativeActivity_onCreate, closing it and printing the
 * failure reason.  Global loading (RTLD_GLOBAL) matters on Android only, so
 * Windows ignores the flag. */
#ifndef ANDROIDRT_DLPORT_H
#define ANDROIDRT_DLPORT_H

/* Open a shared library.  isGlobal requests global symbol visibility
 * (RTLD_GLOBAL); ignored on Windows.  Returns NULL on failure — call
 * rt_dlerror() for the reason. */
void *rt_dlopen(const char *path, int isGlobal);

/* Look up a symbol in a library opened with rt_dlopen. */
void *rt_dlsym(void *handle, const char *name);

/* Unload a library.  Returns 0 on success. */
int rt_dlclose(void *handle);

/* Last error string, valid until the next rt_dlopen/rt_dlsym/rt_dlclose. */
const char *rt_dlerror(void);

#endif /* ANDROIDRT_DLPORT_H */
