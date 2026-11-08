#!/bin/sh
# mkstubs.sh <outdir> <object.o>... — NDK-style stub libraries for the
# undefined symbols of the given objects.
#
# The NDK links games against "stub" libc.so/libm.so/libdl.so/liblog.so/
# libandroid.so whose exports carry version nodes (LIBC, LIBLOG, ...). A game
# linked that way has DT_NEEDED libc.so and imports like memcpy@LIBC — no
# glibc anywhere. These stubs reproduce exactly that, so the smoke games are
# shaped like real NDK output and only AndroidRT's own loader can bind them.
# The stub bodies are never executed.
set -eu

OUT="$1"
shift
mkdir -p "$OUT"
CC="${CC:-gcc}"

# Imports that are data, not functions (defined as arrays in the stubs).
DATA=" __sF _ctype_ _tolower_tab_ _toupper_tab_ __stack_chk_guard __isthreaded __page_size stdin stdout stderr environ optarg optind opterr apak_test_missing_data "

for lib in c m dl log android EGL GLESv2; do
    : > "$OUT/stub_$lib.c"
done

nm -u --format=posix "$@" | awk '$2 == "U" { print $1 }' | sort -u | while read -r sym; do
    case "$sym" in
        fake_*) continue ;; # provided by the bundled libfake_dep.so
        _GLOBAL_OFFSET_TABLE_|_DYNAMIC|__dso_handle|__ehdr_start|_ITM_*|__gmon_start__) continue ;; # the linker's own
        __android_log_*) lib=log ;;
        ANativeActivity_*|ANativeWindow_*|ALooper_*|AInputQueue_*|AInputEvent_*|AKeyEvent_*|AMotionEvent_*|\
        AAsset*|AConfiguration_*|AChoreographer_*|ASensor*|ATrace_*) lib=android ;;
        dlopen|dlsym|dlvsym|dlerror|dlclose|dladdr|dl_iterate_phdr|android_dlopen_ext) lib=dl ;;
        egl*) lib=EGL ;;
        gl[A-Z]*) lib=GLESv2 ;;
        sin|sinf|cos|cosf|tan|tanf|sqrt|sqrtf|pow|powf|exp|expf|log|logf|floor|floorf|ceil|ceilf|\
        fabs|fabsf|fmod|fmodf|atan2|atan2f) lib=m ;;
        *) lib=c ;;
    esac
    case "$DATA" in
        *" $sym "*) printf 'char %s[512];\n' "$sym" >> "$OUT/stub_$lib.c" ;;
        *) printf 'void %s(void) {}\n' "$sym" >> "$OUT/stub_$lib.c" ;;
    esac
done

stub() { # stub <lib> <version node>
    printf '%s { global: *; };\n' "$2" > "$OUT/$1.map"
    "$CC" -w -fno-builtin -fPIC -shared -nostdlib -o "$OUT/$1.so" "$OUT/stub_${1#lib}.c" \
        "-Wl,-soname,$1.so" "-Wl,--version-script=$OUT/$1.map"
}
stub libc LIBC
stub libm LIBC
stub libdl LIBC
stub liblog LIBLOG
stub libandroid LIBANDROID
stub libEGL LIBEGL
stub libGLESv2 LIBGLESV2
