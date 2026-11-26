/* Dolphin shim for RetroArch's EGL 1.4 proc-address callback.
 *
 * libretro/dolphin asks RetroArch for GLES functions through that callback.
 * The Smart Pro firmware's eglGetProcAddress does not return core GLES entry
 * points such as glGetIntegerv, although its matching libGLESv2 exports them.
 * Keep the vendor answer for every function it provides, and fill only
 * otherwise missing gl* symbols from that exact firmware library.
 *
 * The checked-in eglproc_arm64.bin was built with Zig 0.13.0:
 * zig cc -target aarch64-linux-gnu.2.31 -shared -fPIC -O2 -Wall -Wextra
 *   -Wl,-z,defs -o eglproc_arm64.bin egl_proc_fallback.c -ldl -pthread
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <pthread.h>
#include <stddef.h>

typedef void *(*egl_get_proc_address_fn)(const char *);

static pthread_once_t real_once = PTHREAD_ONCE_INIT;
static pthread_once_t gles_once = PTHREAD_ONCE_INIT;
static egl_get_proc_address_fn real_egl_get_proc_address;
static void *gles_handle;

static void find_real_egl(void)
{
    *(void **)&real_egl_get_proc_address = dlsym(RTLD_NEXT, "eglGetProcAddress");
}

static void open_firmware_gles(void)
{
    /* Resolve against the same firmware GLES implementation RetroArch uses. */
    gles_handle = dlopen("/usr/lib/libGLESv2.so.2", RTLD_NOW | RTLD_LOCAL);
}

__attribute__((visibility("default")))
void *eglGetProcAddress(const char *name)
{
    void *proc;

    if (name == NULL)
        return NULL;

    pthread_once(&real_once, find_real_egl);
    proc = real_egl_get_proc_address ? real_egl_get_proc_address(name) : NULL;
    if (proc != NULL)
        return proc;

    /* EGL functions and arbitrary names must retain the vendor's NULL. */
    if (name[0] != 'g' || name[1] != 'l' || name[2] == '\0')
        return NULL;

    pthread_once(&gles_once, open_firmware_gles);
    return gles_handle ? dlsym(gles_handle, name) : NULL;
}
