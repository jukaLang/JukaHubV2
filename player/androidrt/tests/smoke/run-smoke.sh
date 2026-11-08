#!/bin/sh
# run-smoke.sh — AndroidRT smoke tests on the host: no device, no cross
# toolchain, no GPU. Everything is built with the host compiler into
# $SMOKE_WORK (default tests/smoke/tmp, git-ignored) and driven headlessly.
#
# Linux (the full suite):
#   1. runner build + export hygiene (only the Android ABI is exported)
#   2. ELF reader vs binutils readelf on real host libraries
#   3. APS2 (Android packed relocation) decoder unit test
#   4. the glibc-linked lifecycle game (host-loader path)
#   5. NDK-shaped games linked against versioned stub libc.so/liblog.so/...:
#      --scan report, the full compat suite, trap/zero/strict policies
#   6. the android_native_app_glue contract game (looper, input, choreographer)
#   7. the EGL present layer against a mock libEGL/libgbm/libdrm
#   8. surface probe (host as is, then against the mock GPU)
#   9. ALooper + AInputQueue probe
#  10. OpenSL ES audio probe
# Windows (MinGW/MSYS): the legacy subset (dlopen lifecycle, surface and
# audio probes) — there is no ELF loader to test there.
#
# Exit 0 when every assertion passed.
set -eu

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SRC="$HERE/../../src"
WORK="${SMOKE_WORK:-$HERE/tmp}"
STAGED="$WORK/staged"
LIBDIR="$STAGED/lib/arm64-v8a"
CC="${CC:-gcc}"
WARN="-Wall -Wextra -Wno-unused-parameter"
TO=""
command -v timeout >/dev/null 2>&1 && TO="timeout 120"

rm -rf "$WORK"
mkdir -p "$LIBDIR" "$STAGED/assets" "$WORK/stubs"
printf 'hello androidrt\n' > "$STAGED/assets/hello.txt"

PASSED=0
FAILED=0
pass() { echo "PASS: $1"; PASSED=$((PASSED + 1)); }
fail() { echo "FAIL: $1"; FAILED=$((FAILED + 1)); }
# run_to <log> <command...>: run it, keep its output, put the status in RC
run_to() {
    _log="$1"
    shift
    set +e
    "$@" > "$_log" 2>&1
    RC=$?
    set -e
}
# expect <what> <log> <fixed string>
expect() {
    if grep -qF -- "$3" "$2"; then pass "$1"; else fail "$1 (no \"$3\" in $2)"; fi
}
expect_not() {
    if grep -qF -- "$3" "$2"; then fail "$1 (unexpected \"$3\" in $2)"; else pass "$1"; fi
}
expect_rc() { # expect_rc <what> <wanted> — against RC
    if [ "$RC" = "$2" ]; then pass "$1"; else fail "$1 (exit $RC, wanted $2)"; fi
}
count_of() { grep -cF -- "$2" "$1" || true; }

export ANDROIDPAK_APK="$STAGED/game.apk"
export ANDROIDPAK_STAGED="$STAGED"
export ANDROIDPAK_DATA="$WORK/data"

case "$(uname -s)" in
    *MINGW* | *MSYS* | CYGWIN*) PLATFORM=windows ;;
    *) PLATFORM=linux ;;
esac

if [ "$PLATFORM" = windows ]; then
    echo "== Windows host: legacy subset (no ELF loader here) =="
    LOADER="$WORK/loader.exe"
    $CC -O0 -g0 -std=gnu11 $WARN -I"$SRC" -shared "$HERE/fake_game.c" -o "$LIBDIR/fake_game.dll"
    $CC -O0 -g0 -std=gnu11 $WARN -I"$SRC" \
        "$SRC/runner.c" "$SRC/activity.c" "$SRC/androidshim.c" "$SRC/inputshim.c" "$SRC/looper.c" \
        "$SRC/jnistub.c" "$SRC/logshim.c" "$SRC/openslessim.c" "$SRC/surface.c" "$SRC/dlport.c" \
        -o "$LOADER" -pthread
    run_to "$WORK/legacy.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/fake_game.dll ANDROIDRT_SMOKE_FRAMES=5 "$LOADER"
    expect_rc "legacy lifecycle (LoadLibrary)" 0
    $CC -O0 -g0 -std=gnu11 $WARN -I"$SRC" -DSURFACE_PROBE_MAIN "$SRC/surface.c" "$SRC/dlport.c" -o "$WORK/probe.exe"
    run_to "$WORK/probe.log" "$WORK/probe.exe"
    expect_rc "surface probe completes" 0
    $CC -O0 -g0 -std=gnu11 $WARN -I"$SRC" "$HERE/fake_audio.c" "$SRC/openslessim.c" "$SRC/dlport.c" \
        -o "$WORK/audio_probe.exe" -pthread
    run_to "$WORK/audio.log" "$WORK/audio_probe.exe"
    expect_rc "audio probe" 0
    echo "SKIP: bionic loader, looper/input and present-layer suites need Linux"
    echo "== smoke: $PASSED passed, $FAILED failed =="
    [ "$FAILED" -eq 0 ]
    exit $?
fi

# ── 1. the runner ─────────────────────────────────────────────────────
LOADER="$WORK/loader"
echo "== building the runner =="
$CC -O1 -g -std=gnu11 $WARN -fvisibility=hidden -I"$SRC" \
    "$SRC/runner.c" "$SRC/activity.c" "$SRC/androidshim.c" "$SRC/inputshim.c" "$SRC/looper.c" \
    "$SRC/jnistub.c" "$SRC/logshim.c" "$SRC/openslessim.c" "$SRC/surface.c" "$SRC/eglpresent.c" \
    "$SRC/dlport.c" "$SRC/elfdyn.c" "$SRC/bionicld.c" "$SRC/bioniccompat.c" "$SRC/bionicpthread.c" \
    "$SRC/bionicjmp.S" "$SRC/sysprops.c" -o "$LOADER" -pthread -ldl -lm -Wl,--export-dynamic
nm -D --defined-only "$LOADER" | awk '{print $3}' > "$WORK/exports.txt"
for sym in ANativeActivity_finish __android_log_print slCreateEngine SL_IID_ENGINE ALooper_addFd \
           AConfiguration_new AAssetManager_openDir AChoreographer_postFrameCallback64; do
    if grep -qx "$sym" "$WORK/exports.txt"; then pass "runner exports $sym"; else fail "runner exports $sym"; fi
done
if grep -qE '^(apak_|bw_|android_inputInject|runner_requestExit)' "$WORK/exports.txt"; then
    fail "runner keeps its internals hidden ($(grep -E '^(apak_|bw_)' "$WORK/exports.txt" | head -3 | tr '\n' ' '))"
else
    pass "runner keeps its internals hidden"
fi

# ── 2. the ELF reader against binutils ────────────────────────────────
echo "== ELF reader vs readelf =="
$CC -O1 -g -std=gnu11 $WARN -I"$SRC" "$HERE/elf_list.c" "$SRC/elfdyn.c" -o "$WORK/elf_list"
canon() {
    readelf --dyn-syms -W "$1" | awk 'NR > 3 && $1 ~ /^[0-9]+:$/ && $1 != "0:" { print $4, $5, ($7 == "UND" ? "UND" : "DEF"), $8 }'
}
if command -v readelf >/dev/null 2>&1; then
    for lib in /lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libm.so.6 /lib/x86_64-linux-gnu/libz.so.1 \
               /lib64/libc.so.6 /usr/lib/libc.so.6; do
        [ -f "$lib" ] || continue
        canon "$lib" > "$WORK/elf_want.txt"
        "$WORK/elf_list" "$lib" > "$WORK/elf_got.txt"
        if cmp -s "$WORK/elf_want.txt" "$WORK/elf_got.txt"; then
            pass "dynsym/versions of $(basename "$lib") match readelf ($(wc -l < "$WORK/elf_got.txt") symbols)"
        else
            fail "dynsym/versions of $lib differ from readelf"
        fi
        want=$(readelf -r -W "$lib" | awk '/contains [0-9]+ entr/ { s += $(NF - 1) } END { print s + 0 }')
        got=$("$WORK/elf_list" -s "$lib" | awk '/^RELOCS/ { print $2 }')
        if [ "$want" = "$got" ]; then pass "relocation count of $(basename "$lib") ($got)"; else fail "relocations of $lib: $got vs readelf $want"; fi
        if "$WORK/elf_list" -s "$lib" | grep -q 'fail=0'; then pass "hash lookup finds every export of $(basename "$lib")"; else fail "hash lookup on $lib"; fi
    done
else
    echo "SKIP: readelf not installed"
fi

# ── 3. APS2 decoder ───────────────────────────────────────────────────
$CC -O1 -g -std=gnu11 $WARN -I"$SRC" "$HERE/aps2_test.c" "$SRC/elfdyn.c" -o "$WORK/aps2_test"
run_to "$WORK/aps2.log" "$WORK/aps2_test"
expect_rc "APS2 packed-relocation decoder" 0

# ── 4. glibc-linked lifecycle game (host loader path) ─────────────────
echo "== lifecycle: glibc-linked fake game =="
$CC -O0 -g -std=gnu11 $WARN -I"$SRC" -shared -fPIC "$HERE/fake_game.c" -o "$LIBDIR/libgame.so"
run_to "$WORK/lifecycle.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libgame.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "lifecycle exits 0" 0
expect "onCreate served" "$WORK/lifecycle.log" "onCreate served"
expect "Android callback order" "$WORK/lifecycle.log" "lifecycle: onPause"
if [ -d "$WORK/data/files" ] && [ -d "$WORK/data/obb" ]; then pass "persistent data dirs created"; else fail "persistent data dirs created"; fi

# ── 5. NDK-shaped games ───────────────────────────────────────────────
echo "== NDK-shaped games (versioned stub libc.so etc.) =="
GFL="-O1 -g -fPIC -fno-builtin -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0 $WARN"
for f in fake_bionic_dep fake_bionic fake_glue fake_egl; do
    $CC $GFL -c "$HERE/$f.c" -o "$WORK/$f.o"
done
sh "$HERE/mkstubs.sh" "$WORK/stubs" "$WORK/fake_bionic_dep.o" "$WORK/fake_bionic.o" "$WORK/fake_glue.o" "$WORK/fake_egl.o"
NDKLINK="-shared -nostdlib -L$WORK/stubs -L$LIBDIR -Wl,--no-as-needed -Wl,--no-undefined"
RELR=""
if $CC $NDKLINK -Wl,-z,pack-relative-relocs "$WORK/fake_bionic_dep.o" -lc -o "$WORK/relr-probe.so" 2>/dev/null &&
    readelf -d "$WORK/relr-probe.so" 2>/dev/null | grep -q RELR; then
    RELR="-Wl,-z,pack-relative-relocs"
fi
$CC $NDKLINK $RELR "$WORK/fake_bionic_dep.o" -lc -Wl,-soname,libfake_dep.so -o "$LIBDIR/libfake_dep.so"
$CC $NDKLINK "$WORK/fake_bionic.o" -lfake_dep -lc -lm -ldl -llog -landroid -Wl,-soname,libfake_bionic.so \
    -o "$LIBDIR/libfake_bionic.so"
$CC $NDKLINK "$WORK/fake_glue.o" -lc -llog -landroid -Wl,-soname,libfake_glue.so -o "$LIBDIR/libfake_glue.so"
$CC $NDKLINK "$WORK/fake_egl.o" -lEGL -lc -llog -landroid -Wl,-soname,libfake_egl.so -o "$LIBDIR/libfake_egl.so"
[ -n "$RELR" ] && pass "dependency linked with DT_RELR" || echo "SKIP: linker without -z pack-relative-relocs"
if readelf -d "$LIBDIR/libfake_bionic.so" | grep -q 'Shared library: \[libc.so\]' &&
    readelf --dyn-syms -W "$LIBDIR/libfake_bionic.so" | grep -q 'pthread_mutex_lock@LIBC'; then
    pass "fake game is NDK-shaped (DT_NEEDED libc.so, imports versioned @LIBC)"
else
    fail "fake game is NDK-shaped"
fi

run_to "$WORK/scan.log" "$LOADER" --scan "$LIBDIR/libfake_bionic.so"
expect_rc "--scan exits 2 when symbols are missing" 2
expect "--scan lists the missing function" "$WORK/scan.log" "undefined symbol: apak_test_missing_function (func, needed by libfake_bionic.so, from libc.so@LIBC)"
expect "--scan lists the missing data" "$WORK/scan.log" "undefined symbol: apak_test_missing_data (data"
expect_not "--scan ignores weak undefined imports" "$WORK/scan.log" "apak_test_weak_missing"

run_to "$WORK/bionic.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_bionic.so ANDROIDRT_SMOKE_FRAMES=200 $TO "$LOADER"
expect_rc "NDK-shaped game runs to a clean exit" 0
expect "bionic compat suite" "$WORK/bionic.log" "[bionic-test] all checks passed"
expect "the JNI stub logs what the game asked Java for" "$WORK/bionic.log" "[androidrt][jni] FindClass (\"android/os/Build\") -> null"
if grep -q '\[bionic-test\] FAIL' "$WORK/bionic.log"; then grep '\[bionic-test\] FAIL' "$WORK/bionic.log"; fi

run_to "$WORK/trap.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_bionic.so FAKE_CALL_MISSING=1 $TO "$LOADER"
if [ "$RC" -ne 0 ]; then pass "a called missing function stops the game"; else fail "a called missing function stops the game"; fi
expect "the trap names the function" "$WORK/trap.log" "trap: libfake_bionic.so called unimplemented apak_test_missing_function"

run_to "$WORK/zero.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_bionic.so FAKE_CALL_MISSING=1 \
    ANDROIDPAK_MISSING=zero ANDROIDRT_SMOKE_FRAMES=200 $TO "$LOADER"
expect_rc "ANDROIDPAK_MISSING=zero keeps going" 0
expect "zero policy logs once" "$WORK/zero.log" "returning 0 (ANDROIDPAK_MISSING=zero)"

run_to "$WORK/strict.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_bionic.so ANDROIDPAK_MISSING=strict $TO "$LOADER"
expect_rc "ANDROIDPAK_MISSING=strict refuses to start" 2
expect "strict names the reason" "$WORK/strict.log" "refusing to start"

# ── 6. android_native_app_glue contract ───────────────────────────────
echo "== android_native_app_glue contract =="
run_to "$WORK/glue.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_glue.so ANDROIDRT_SMOKE_INPUT=1 \
    ANDROIDRT_SMOKE_FRAMES=3000 $TO "$LOADER"
expect_rc "glue game exits cleanly" 0
expect "glue game checks" "$WORK/glue.log" "[glue-test] all checks passed"
expect "onDestroy waited for android_main" "$WORK/glue.log" "onDestroy: android_main has returned"

# ── 7. EGL present layer against the mock GPU ─────────────────────────
echo "== present layer (mock libEGL/libgbm/libdrm) =="
$CC -O1 -g -std=gnu11 $WARN -fPIC -shared -fvisibility=hidden -I"$SRC" "$HERE/mock_gpu.c" -o "$WORK/libmockgpu.so"
MOCK="ANDROIDPAK_EGL_LIB=$WORK/libmockgpu.so"
run_to "$WORK/present.log" env "$MOCK" ANDROIDPAK_GBM_LIB="$WORK/libmockgpu.so" ANDROIDPAK_DRM_LIB="$WORK/libmockgpu.so" \
    ANDROIDPAK_DRM_NODE=/dev/null EGLTEST_EXPECT_SCANOUT_VISUAL=1 ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so \
    ANDROIDRT_SMOKE_FRAMES=300 $TO "$LOADER"
expect_rc "EGL game on the gbm backend exits cleanly" 0
expect "auto picks gbm when KMS has a panel" "$WORK/present.log" "[androidrt][present] backend gbm"
expect "EGL display over EGL_PLATFORM_GBM_KHR (0x31d7)" "$WORK/present.log" "eglGetPlatformDisplayEXT platform=0x31d7"
expect "the game's attribute list arrives as int32 pairs" "$WORK/present.log" "eglChooseConfig attribs ok"
expect "gbm surface in the chosen config's visual" "$WORK/present.log" "gbm_surface_create 1280x720 format=AR24 flags=0x5"
expect "the ANativeWindow became the gbm surface" "$WORK/present.log" "eglCreateWindowSurface config=1 native=gbm-surface"
expect "first frame sets the CRTC" "$WORK/present.log" "drmModeSetCrtc crtc=41 fb=100 connector=31"
if [ "$(count_of "$WORK/present.log" 'drmModePageFlip crtc=41')" = 4 ]; then pass "later frames page-flip (4 of 5)"; else fail "later frames page-flip"; fi
if [ "$(count_of "$WORK/present.log" 'drmModeAddFB2')" = 3 ]; then pass "one framebuffer per buffer object, cached"; else fail "framebuffer caching"; fi
expect "buffers all returned" "$WORK/present.log" "gbm_surface_destroy (locked 5, released 5)"
if [ "$(count_of "$WORK/present.log" 'drmModeRmFB')" = 3 ]; then pass "framebuffers removed with their buffers"; else fail "framebuffers removed"; fi
expect "the hub's CRTC is restored at exit" "$WORK/present.log" "drmModeSetCrtc crtc=41 fb=7"
expect "EGL game checks (gbm)" "$WORK/present.log" "[egl-test] all checks passed"

run_to "$WORK/present-native.log" env "$MOCK" ANDROIDPAK_PRESENT=native ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so \
    ANDROIDRT_SMOKE_FRAMES=300 $TO "$LOADER"
expect_rc "EGL game on the native backend exits cleanly" 0
expect "native backend hands EGL a null native window" "$WORK/present-native.log" "native=null"
expect_not "native backend never modesets" "$WORK/present-native.log" "drmModeSetCrtc"

# ── 8. surface probe ──────────────────────────────────────────────────
echo "== surface probe =="
$CC -O1 -g -std=gnu11 $WARN -I"$SRC" -DSURFACE_PROBE_MAIN "$SRC/surface.c" "$SRC/dlport.c" -o "$WORK/probe" -ldl
run_to "$WORK/probe-host.log" "$WORK/probe"
expect_rc "probe completes on a GPU-less host" 0
run_to "$WORK/probe-mock.log" env "$MOCK" ANDROIDPAK_GBM_LIB="$WORK/libmockgpu.so" ANDROIDPAK_DRM_LIB="$WORK/libmockgpu.so" \
    ANDROIDPAK_DRM_NODE=/dev/null "$WORK/probe"
expect_rc "probe completes against the mock GPU" 0
expect "probe reports the GBM/KMS path" "$WORK/probe-mock.log" "status: GBM/KMS path YES 1280x720"
expect "probe finds the panel mode" "$WORK/probe-mock.log" "KMS: connector 31 mode 1280x720 1280x720@60"

# ── 9. ALooper + AInputQueue probe ────────────────────────────────────
echo "== looper/input probe =="
$CC -O1 -g -std=gnu11 $WARN -I"$SRC" "$HERE/fake_input.c" "$SRC/androidshim.c" "$SRC/inputshim.c" \
    "$SRC/looper.c" "$SRC/surface.c" "$SRC/dlport.c" -o "$WORK/input_probe" -pthread -ldl
run_to "$WORK/input.log" $TO "$WORK/input_probe"
expect_rc "looper + input queue contract" 0

# ── 10. OpenSL ES audio probe ─────────────────────────────────────────
echo "== audio probe =="
$CC -O1 -g -std=gnu11 $WARN -I"$SRC" "$HERE/fake_audio.c" "$SRC/openslessim.c" "$SRC/dlport.c" \
    -o "$WORK/audio_probe" -pthread -ldl
run_to "$WORK/audio.log" $TO "$WORK/audio_probe"
expect_rc "OpenSL ES path (silent pacing without ALSA)" 0

echo "== smoke: $PASSED passed, $FAILED failed (logs in $WORK) =="
[ "$FAILED" -eq 0 ]
