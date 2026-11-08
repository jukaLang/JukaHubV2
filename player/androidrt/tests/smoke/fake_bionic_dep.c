/* fake_bionic_dep.c — libfake_dep.so, the bundled DT_NEEDED of the
 * bionic-loader smoke game (fake_bionic.c). Linked like an NDK library
 * against the generated stub libc.so, and with -z pack-relative-relocs when
 * the host linker supports it, so the loader's DT_RELR path runs:
 *
 *   fake_dep_name()        reads a pointer table relocated through RELR
 *   fake_dep_ifunc         an STT_GNU_IFUNC export (resolver picks the body)
 *   fake_dep_calls_shared  calls fake_shared_name() through its PLT; the
 *                          root library defines it too and must win (the
 *                          BFS lookup order bionic's linker uses)
 *   fake_dep_counter       a data export the root imports (GLOB_DAT)
 */
#include <stddef.h>

__attribute__((visibility("default"))) int fake_dep_counter = 5;

static const char *const kNames[] = { "one", "two", "three" };

__attribute__((visibility("default"))) int fake_dep_answer(void) { return 42; }

__attribute__((visibility("default"))) const char *fake_dep_name(int i) {
    return i >= 0 && i < 3 ? kNames[i] : NULL;
}

static int ifuncImpl(void) { return 7; }
static int (*resolveFakeIfunc(void))(void) { return ifuncImpl; }
__attribute__((visibility("default"))) int fake_dep_ifunc(void) __attribute__((ifunc("resolveFakeIfunc")));

__attribute__((visibility("default"))) int fake_shared_name(void) { return 2; }

__attribute__((visibility("default"))) int fake_dep_calls_shared(void) { return fake_shared_name(); }
