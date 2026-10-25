/* See dlport.h.  POSIX builds sit directly on <dlfcn.h>; Windows builds use
 * LoadLibrary/GetProcAddress/FreeLibrary with FormatMessage error strings. */
#include "dlport.h"

#include <stdio.h>
#include <string.h>

#ifdef _WIN32
#include <windows.h>

static char g_err[256];

const char *rt_dlerror(void) { return g_err[0] ? g_err : "no error"; }

void *rt_dlopen(const char *path, int isGlobal) {
    (void)isGlobal;
    SetLastError(0);
    HMODULE mod = LoadLibraryA(path);
    if (!mod) {
        DWORD code = GetLastError();
        snprintf(g_err, sizeof g_err, "LoadLibrary(%s) failed (%lu)",
                 path, (unsigned long)code);
        return NULL;
    }
    g_err[0] = '\0';
    return (void *)mod;
}

void *rt_dlsym(void *handle, const char *name) {
    HMODULE mod = (HMODULE)handle;
    SetLastError(0);
    void *sym = (void *)GetProcAddress(mod, name);
    if (!sym)
        snprintf(g_err, sizeof g_err, "GetProcAddress(%s) failed", name);
    else
        g_err[0] = '\0';
    return sym;
}

int rt_dlclose(void *handle) {
    g_err[0] = '\0';
    return FreeLibrary((HMODULE)handle) ? 0 : -1;
}

#else /* POSIX */
#include <dlfcn.h>

static char g_err[256];

const char *rt_dlerror(void) { return g_err[0] ? g_err : "no error"; }

void *rt_dlopen(const char *path, int isGlobal) {
    int flags = isGlobal ? RTLD_GLOBAL : RTLD_LOCAL;
    void *h = dlopen(path, flags);
    if (!h) {
        const char *msg = dlerror();
        snprintf(g_err, sizeof g_err, "%s", msg ? msg : "dlopen failed");
        return NULL;
    }
    g_err[0] = '\0';
    return h;
}

void *rt_dlsym(void *handle, const char *name) {
    dlerror();
    void *sym = dlsym(handle, name);
    if (!sym) {
        const char *msg = dlerror();
        snprintf(g_err, sizeof g_err, "%s",
                 msg ? msg : "symbol not found");
    } else {
        g_err[0] = '\0';
    }
    return sym;
}

int rt_dlclose(void *handle) {
    dlerror();
    int r = dlclose(handle);
    if (r != 0) {
        const char *msg = dlerror();
        snprintf(g_err, sizeof g_err, "%s",
                 msg ? msg : "dlclose failed");
    } else {
        g_err[0] = '\0';
    }
    return r;
}
#endif
