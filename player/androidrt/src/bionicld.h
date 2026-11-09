/* bionicld.h — AndroidRT's own ELF loader for bionic-linked libraries.
 *
 * glibc's ld.so cannot load a modern NDK library even when every symbol
 * exists: NDK stub libraries are versioned (memcpy@LIBC, ...), and glibc
 * rejects every GLIBC_2.x definition for a reference versioned LIBC. So the
 * runner maps the game's libraries itself (like bionic's linker, or the
 * so-loaders of the Vita/Switch ports), binds every import through the
 * compat resolver (bioniccompat.h) and prints the COMPLETE list of what is
 * still missing before a single line of game code runs:
 *
 *   [androidrt] undefined symbols: 3 (2 functions trapped, 1 data) ...
 *   [androidrt] undefined symbol: AChoreographer_getInstance (func, needed by libgame.so, from libandroid.so)
 *
 * A missing function is bound to a trap that names it when (if ever) it is
 * called — ANDROIDPAK_MISSING=trap (default: log + abort), =zero (log once,
 * return 0) or =strict (refuse to start when anything is missing).
 *
 * Linux only (mmap of ELF segments); Windows smoke builds keep dlport.c.
 */
#ifndef ANDROIDRT_BIONICLD_H
#define ANDROIDRT_BIONICLD_H

#include <stddef.h>

enum {
    APAK_MISSING_TRAP = 0,
    APAK_MISSING_ZERO = 1,
    APAK_MISSING_STRICT = 2,
};

/* Library search directories (colon-separated; the staged lib/<abi> dir)
 * and the missing-symbol policy. Call once before apak_ldOpen. */
void apak_ldConfigure(const char *libDirs, int missingPolicy);

/* argc/argv handed to every DT_INIT/DT_INIT_ARRAY function, as bionic does. */
void apak_ldSetArgs(int argc, char **argv);

/* Map, relocate and initialise a bionic library plus its bundled
 * dependencies. Returns an opaque handle, or NULL with apak_ldLastError(). */
void *apak_ldOpen(const char *nameOrPath);

/* An exported symbol of exactly this library (no dependency search). */
void *apak_ldSymSelf(void *handle, const char *name);

/* Dry run (`loader --scan`): parse the library and its bundled closure, bind
 * nothing, print the import report. Returns the number of strong undefined
 * symbols, or -1 when the library itself cannot be read. */
int apak_ldScan(const char *nameOrPath);

/* Find a library in the search dirs whose exports contain `symbol`. */
int apak_ldFindExporter(const char *symbol, char *out, size_t cap);

/* An export of any loaded game library (bundled or a glibc-built one). */
void *apak_ldSymBundled(const char *name);

/* Visit the game's libraries reachable from root, dependencies first. */
void apak_ldForEachInitOrder(void *root, void (*fn)(const char *name, void *handle, void *ud), void *ud);

/* Bind a system library name a game imports ("libEGL.so", "libGLESv2.so")
 * to an already opened host library — the present layer's GPU stack —
 * instead of the default soname lookup. Call before apak_ldOpen. */
void apak_ldSetHostLib(const char *virtualName, void *handle, const char *label);

const char *apak_ldLastError(void);

/* Missing-symbol counters of everything loaded so far (for the runner). */
size_t apak_ldMissingCount(void);

/* The bionic libdl ABI, bound through the override table. */
void *apak_bionic_dlopen(const char *name, int flags);
void *apak_bionic_android_dlopen_ext(const char *name, int flags, const void *extinfo);
void *apak_bionic_dlsym(void *handle, const char *name);
void *apak_bionic_dlvsym(void *handle, const char *name, const char *version);
int apak_bionic_dlclose(void *handle);
char *apak_bionic_dlerror(void);
int apak_bionic_dladdr(const void *addr, void *dlInfo);
int apak_bionic_dl_iterate_phdr(int (*cb)(void *info, size_t size, void *data), void *data);

#endif /* ANDROIDRT_BIONICLD_H */
