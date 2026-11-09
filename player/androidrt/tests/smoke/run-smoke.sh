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
#  4b. the JNI environment with the Minecraft class pack (fake_jnigame.c)
#  4c. the androidx GameActivity bridge (fake_gameact.c)
#   5. NDK-shaped games linked against versioned stub libc.so/liblog.so/...:
#      --scan report, the full compat suite, trap/zero/strict policies
#   6. the android_native_app_glue contract game (looper, input, choreographer)
#   7. the EGL present layer against a mock libEGL/libgbm/libdrm (gbm, native,
#      and the stock-TrimUI default: firmware libEGL + fbdev window), and the
#      SDL-hosted backend against a fake libSDL2 (explicit, context-first,
#      probe contexts, eglReleaseThread, both automatic fallbacks, SDL2
#      missing or failing, a default display that never comes up)
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
expect_count() { # expect_count <what> <log> <wanted count> <fixed string>
    _n=$(count_of "$2" "$4")
    if [ "$_n" = "$3" ]; then pass "$1"; else fail "$1 ($_n lines with \"$4\" in $2, wanted $3)"; fi
}

export ANDROIDPAK_APK="$STAGED/game.apk"
export ANDROIDPAK_STAGED="$STAGED"
export ANDROIDPAK_DATA="$WORK/data"
# A host with a real libSDL2 must not change the results: no case loads one
# unless it points ANDROIDPAK_SDL_LIB at the fake SDL2 itself.
export ANDROIDPAK_SDL_LIB="$WORK/no-libSDL2-2.0.so.0"

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
        "$SRC/apkzip.c" "$SRC/device.c" -o "$LOADER" -pthread
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
# The runtime's sources (build-tsp.sh compiles the same list for the device).
RUNTIME_SRCS="$SRC/runner.c $SRC/activity.c $SRC/androidshim.c $SRC/inputshim.c $SRC/looper.c \
$SRC/jnienv.c $SRC/jnibase.c $SRC/jnipack_minecraft.c $SRC/gameactivity.c $SRC/apkzip.c $SRC/apkmeta.c \
$SRC/device.c $SRC/logshim.c $SRC/openslessim.c $SRC/surface.c $SRC/eglpresent.c $SRC/dlport.c \
$SRC/elfdyn.c $SRC/bionicld.c $SRC/bioniccompat.c $SRC/bionicpthread.c $SRC/bionicjmp.S $SRC/sysprops.c"
LOADER="$WORK/loader"
echo "== building the runner =="
$CC -O1 -g -std=gnu11 $WARN -fvisibility=hidden -I"$SRC" \
    $RUNTIME_SRCS -o "$LOADER" -pthread -ldl -lm -Wl,--export-dynamic
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

# ── 4b. the Java side: a JNI-heavy game with the Minecraft class pack ─
echo "== JNI environment: Minecraft class pack =="
$CC -O0 -g -std=gnu11 $WARN -I"$SRC" -shared -fPIC "$HERE/fake_jnigame.c" -o "$LIBDIR/libjnigame.so"
run_to "$WORK/jni.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libjnigame.so ANDROIDPAK_PACKAGE=com.mojang.minecraftpe \
    ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "JNI game exits 0" 0
expect "the package selects the Minecraft class pack" "$WORK/jni.log" 'class pack "minecraft" for com.mojang.minecraftpe'
expect "JNI_OnLoad runs and RegisterNatives works" "$WORK/jni.log" "[jnigame] ok: JNI_OnLoad registered natives on MainActivity"
expect "store listener natives registered" "$WORK/jni.log" "[jnigame] ok: JNI_OnLoad registered the store listener natives"
expect "MainActivity.onCreate's nativeRegisterThis reaches the game" "$WORK/jni.log" "[jnigame] native: nativeRegisterThis(activity)"
expect "activity object is the pack's MainActivity" "$WORK/jni.log" "[jnigame] ok: the activity object is a MainActivity"
expect "Java method answers with the panel width" "$WORK/jni.log" "[jnigame] getScreenWidth() = 1280"
expect "storage path is the persistent data folder" "$WORK/jni.log" "[jnigame] external storage $WORK/data/external"
expect "unimplemented member answers the default" "$WORK/jni.log" "[jnigame] ok: an unimplemented method answers 0, twice"
if [ "$(count_of "$WORK/jni.log" 'MainActivity.smokeNoSuchMethod()I -> not implemented')" = 1 ] &&
    [ "$(count_of "$WORK/jni.log" 'called com/mojang/minecraftpe/MainActivity.smokeNoSuchMethod()I')" = 1 ]; then
    pass "an unimplemented member is logged once (lookup and call), not per call"
else
    fail "an unimplemented member is logged once"
fi
expect "unknown class is null" "$WORK/jni.log" "[jnigame] ok: FindClass of a class nobody has returns null"
expect "strings: modified UTF-8 <-> UTF-16" "$WORK/jni.log" "[jnigame] string lengths utf16=7 utf8=10"
expect "primitive arrays round-trip" "$WORK/jni.log" "[jnigame] int array sum 10"
expect "global references" "$WORK/jni.log" "[jnigame] ok: a global reference is the same object"
expect "the store never claims a license" "$WORK/jni.log" "[jnigame] store license verified=0 received=0"
expect "the store reports itself unavailable to the game" "$WORK/jni.log" "[jnigame] native: store initialized=0 listener=0x5eed"
expect "a posted Java callback reaches a registered native" "$WORK/jni.log" "[jnigame] native: image pick canceled, token 77"
expect "no JNI check failed" "$WORK/jni.log" "[jnigame] onCreate done, 0 failure(s)"
expect "android.os.Build describes the handheld" "$WORK/jni.log" "[jnigame] Build.MODEL=Smart Pro MANUFACTURER=TrimUI SDK_INT=28"
expect "exit prints the compatibility report" "$WORK/jni.log" "[androidrt][jni] report:"
if [ "$(count_of "$WORK/jni.log" 'FindClass ("com/example/SmokeNoSuchClass") -> null')" = 1 ]; then
    pass "a class asked for twice is logged once"
else
    fail "a class asked for twice is logged once"
fi
expect "a class asked for twice is reported once" "$WORK/jni.log" "[androidrt][jni] report: FindClass returned null for (1):"
if sed -n '/report: unimplemented members/{n;p;}' "$WORK/jni.log" |
    grep -qF 'report:   com/mojang/minecraftpe/MainActivity.smokeNoSuchMethod()I'; then
    pass "the report lists members in the order the game asked (lookup, then call)"
else
    fail "the report lists members in the order the game asked"
fi
run_to "$WORK/jni-off.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libjnigame.so ANDROIDPAK_JNI=off \
    ANDROIDRT_SMOKE_FRAMES=3 $TO "$LOADER"
expect_rc "ANDROIDPAK_JNI=off still runs (null env, like the old Tier 1)" 0
expect "ANDROIDPAK_JNI=off hands the game no env" "$WORK/jni-off.log" "[jnigame] FAIL: activity->env and ->clazz exist"

# ── 4c. an androidx GameActivity game (no ANativeActivity_onCreate) ──
echo "== GameActivity bridge =="
$CC -O0 -g -std=gnu11 $WARN -I"$SRC" -shared -fPIC "$HERE/fake_gameact.c" -o "$LIBDIR/libgameact.so"
run_to "$WORK/gameact.log" env ANDROIDPAK_MAINLIB=lib/arm64-v8a/libgameact.so ANDROIDRT_SMOKE_FRAMES=8 \
    ANDROIDRT_SMOKE_INPUT=1 $TO "$LOADER"
expect_rc "GameActivity game exits 0" 0
expect "the glue's JNI_OnLoad registers its natives" "$WORK/gameact.log" "[gameact] JNI_OnLoad registered the GameActivity natives"
expect "initializeNativeCode gets the internal data path" "$WORK/gameact.log" "[gameact] initializeNativeCode internal $WORK/data/files"
expect "initializeNativeCode gets the external data path" "$WORK/gameact.log" "[gameact] initializeNativeCode external $WORK/data/external"
expect "initializeNativeCode gets an AssetManager and a Configuration" "$WORK/gameact.log" "[gameact] initializeNativeCode assets=yes config=yes"
expect "onStart with the native handle" "$WORK/gameact.log" "[gameact] onStartNative handle=ok"
expect "onResume" "$WORK/gameact.log" "[gameact] onResumeNative handle=ok"
expect "surface created" "$WORK/gameact.log" "[gameact] onSurfaceCreatedNative handle=ok surface=yes"
expect "surface sized to the panel" "$WORK/gameact.log" "[gameact] onSurfaceChangedNative 1280x720 format 1"
expect "window focus" "$WORK/gameact.log" "[gameact] onWindowFocusChangedNative 1"
expect "a button press reaches onKeyDownNative as a KeyEvent" "$WORK/gameact.log" "[gameact] onKeyDownNative keyCode=96"
expect "and its release onKeyUpNative" "$WORK/gameact.log" "[gameact] onKeyUpNative keyCode=96"
expect "teardown ends in terminateNativeCode" "$WORK/gameact.log" "[gameact] terminateNativeCode handle=ok"
expect_not "every native got the handle initializeNativeCode returned" "$WORK/gameact.log" "handle=WRONG"

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
expect "the JNI layer logs what the game asked Java for" "$WORK/bionic.log" "[androidrt][jni] FindClass (\"android/os/SmokeNoSuchClass\") -> null"
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
expect "gbm leaves no SDL_* variables to the game or its children" "$WORK/present.log" "[egl-test] SDL environment unset"

run_to "$WORK/present-native.log" env "$MOCK" ANDROIDPAK_PRESENT=native ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so \
    ANDROIDRT_SMOKE_FRAMES=300 $TO "$LOADER"
expect_rc "EGL game on the native backend exits cleanly" 0
expect "native backend hands EGL a null native window" "$WORK/present-native.log" "native=null"
expect_not "native backend never modesets" "$WORK/present-native.log" "drmModeSetCrtc"
expect "an explicit native backend sets no SDL_* variables" "$WORK/present-native.log" "[egl-test] SDL environment unset"
# The stock TrimUI firmware: no DRM card, the firmware's libEGL, SDL's mali
# fbdev window contract — chosen by auto without any ANDROIDPAK_PRESENT.
run_to "$WORK/present-trimui.log" env "$MOCK" ANDROIDPAK_DEVICE=trimui-smart-pro ANDROIDPAK_GBM_LIB=/nonexistent/libgbm.so \
    ANDROIDPAK_DRM_LIB=/nonexistent/libdrm.so ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so \
    ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "EGL game on the stock-firmware default exits cleanly" 0
expect "stock firmware picks the native backend with the fbdev window" "$WORK/present-trimui.log" \
    "backend native (EGL $WORK/libmockgpu.so, window: fbdev {width, height} struct)"
expect "the fbdev window carries the panel size" "$WORK/present-trimui.log" "native=OTHER(1280x720)"
if [ "$(count_of "$WORK/present-trimui.log" '[mockgpu] eglGetDisplay')" = 1 ]; then
    pass "auto touches no display at start (the game's eglGetDisplay is the only one)"
else
    fail "auto touches no display at start"
fi
expect "auto on native prepares SDL's environment for a fallback" "$WORK/present-trimui.log" "[egl-test] SDL environment set"
# An explicit ANDROIDPAK_EGL_LIB that does not load is final: the firmware's
# libEGL does not stand in for it, and the message names the override.
run_to "$WORK/present-badegl.log" env ANDROIDPAK_EGL_LIB=/nonexistent/libEGL.so ANDROIDPAK_DEVICE=trimui-smart-pro \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect "a failed ANDROIDPAK_EGL_LIB is named on the stock firmware" "$WORK/present-badegl.log" \
    "stock firmware (Smart Pro): ANDROIDPAK_EGL_LIB /nonexistent/libEGL.so not loadable:"
expect_not "and nothing else stands in for it" "$WORK/present-badegl.log" "searching the library path"
run_to "$WORK/present-brick.log" env "$MOCK" ANDROIDPAK_DEVICE=trimui-brick ANDROIDPAK_GBM_LIB=/nonexistent/libgbm.so \
    ANDROIDPAK_DRM_LIB=/nonexistent/libdrm.so ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so \
    ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect "the Brick's window is its 1024x768 panel" "$WORK/present-brick.log" "native=OTHER(1024x768)"
# The SDL-hosted backend: the firmware's SDL2 (fake_sdl.c drives the same
# mock EGL the way SDL's mali driver does) owns the window, its EGL surface
# and the context; the game gets those objects and every frame goes through
# SDL_GL_SwapWindow.
$CC -O1 -g -std=gnu11 $WARN -fPIC -shared -fvisibility=hidden -I"$SRC" "$HERE/fake_sdl.c" -o "$WORK/libfakesdl.so" \
    -ldl -pthread
FAKESDL="ANDROIDPAK_SDL_LIB=$WORK/libfakesdl.so"
STOCK_NO_KMS="ANDROIDPAK_DEVICE=trimui-smart-pro ANDROIDPAK_GBM_LIB=/nonexistent/libgbm.so ANDROIDPAK_DRM_LIB=/nonexistent/libdrm.so"
run_to "$WORK/present-sdl.log" env "$MOCK" "$FAKESDL" ANDROIDPAK_PRESENT=sdl $STOCK_NO_KMS \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "EGL game on the sdl backend exits cleanly" 0
expect "ANDROIDPAK_PRESENT=sdl selects the SDL-hosted backend" "$WORK/present-sdl.log" \
    "[androidrt][present] backend sdl (EGL $WORK/libmockgpu.so, SDL $WORK/libfakesdl.so, video driver mali)"
expect "SDL is pointed at the game's own libEGL" "$WORK/present-sdl.log" \
    "[fakesdl] SDL_InitSubSystem(VIDEO): driver mali, EGL from $WORK/libmockgpu.so"
expect "SDL leaves SIGTERM fatal and the console keyboard alone" "$WORK/present-sdl.log" \
    "[fakesdl] hints: SDL_NO_SIGNAL_HANDLERS=1 SDL_INPUT_LINUX_KEEP_KBD=1"
expect "no GLES library beside libEGL: SDL keeps its default" "$WORK/present-sdl.log" \
    "[fakesdl] SDL_GL_LoadLibrary: GLES library SDL's default libGLESv2.so.2"
expect "SDL's window: fullscreen GL with the game's config sizes" "$WORK/present-sdl.log" \
    '[fakesdl] SDL_CreateWindow "AndroidRT" 1280x720 flags=0x1007 -> fbdev 1280x720, config rgba 8880 depth 24 stencil 8, ES3 config'
expect "SDL's fbdev window reaches EGL" "$WORK/present-sdl.log" "eglCreateWindowSurface config=0 native=OTHER(1280x720)"
expect "the game's window surface is SDL's" "$WORK/present-sdl.log" "[androidrt][present] window surface 1280x720 from SDL"
expect "the game's context is SDL's" "$WORK/present-sdl.log" "[androidrt][present] the game's GLES 2.0 context is SDL's"
if [ "$(count_of "$WORK/present-sdl.log" '[mockgpu] eglCreateContext')" = 1 ]; then
    pass "one GLES context in all: SDL's"
else
    fail "one GLES context in all: SDL's"
fi
if [ "$(count_of "$WORK/present-sdl.log" '[fakesdl] SDL_GL_MakeCurrent window')" = 2 ]; then
    pass "the game's eglMakeCurrent goes through SDL_GL_MakeCurrent"
else
    fail "the game's eglMakeCurrent goes through SDL_GL_MakeCurrent"
fi
if [ "$(count_of "$WORK/present-sdl.log" '[fakesdl] SDL_GL_SwapWindow frame')" = 5 ]; then
    pass "every frame is presented through SDL_GL_SwapWindow (5 of 5)"
else
    fail "every frame is presented through SDL_GL_SwapWindow"
fi
expect_not "no frame meets a surface that is not current" "$WORK/present-sdl.log" "not current"
expect "eglTerminate leaves SDL's display alone" "$WORK/present-sdl.log" "eglTerminate ignored: SDL owns the display until exit"
expect "EGL game checks (sdl)" "$WORK/present-sdl.log" "[egl-test] all checks passed"
# GLSurfaceView's order, context before window: the context still builds
# SDL's window and is SDL's.
run_to "$WORK/present-sdl-ctxfirst.log" env "$MOCK" "$FAKESDL" ANDROIDPAK_PRESENT=sdl $STOCK_NO_KMS \
    EGLTEST_CONTEXT_FIRST=1 ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "a context-first game on the sdl backend exits cleanly" 0
expect "a context made before the window is SDL's" "$WORK/present-sdl-ctxfirst.log" \
    "[androidrt][present] the game's GLES 2.0 context is SDL's"
expect_count "context first: one GLES context in all, SDL's" "$WORK/present-sdl-ctxfirst.log" 1 "[mockgpu] eglCreateContext"
expect_count "context first: every frame through SDL_GL_SwapWindow" "$WORK/present-sdl-ctxfirst.log" 5 \
    "[fakesdl] SDL_GL_SwapWindow frame"
expect "EGL game checks (sdl, context first)" "$WORK/present-sdl-ctxfirst.log" "[egl-test] all checks passed"
# Context first, and SDL cannot create its window (its 640x360 framebuffer
# window is refused): the context is plain EGL and the game runs native.
run_to "$WORK/present-sdl-nowindow.log" env "$MOCK" "$FAKESDL" ANDROIDPAK_PRESENT=sdl $STOCK_NO_KMS \
    EGLTEST_CONTEXT_FIRST=1 FAKESDL_FB=640x360 MOCKGPU_FBDEV_SIZE=1280x720 \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "SDL without a window: a context-first game still runs" 0
expect "SDL without a window: the context is plain EGL" "$WORK/present-sdl-nowindow.log" \
    "SDL could not create the window — the game's context is plain EGL (native)"
expect_not "SDL without a window: every frame is presented" "$WORK/present-sdl-nowindow.log" "EGL_BAD_SURFACE"
expect "EGL game checks (sdl without a window)" "$WORK/present-sdl-nowindow.log" "[egl-test] all checks passed"
# An engine's probe context on a pbuffer-only config (no depth/stencil)
# before the window: plain EGL, and SDL's window still gets the window config.
run_to "$WORK/present-sdl-probe.log" env "$MOCK" "$FAKESDL" ANDROIDPAK_PRESENT=sdl $STOCK_NO_KMS \
    EGLTEST_PROBE_CONTEXT=1 ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "a game with a probe context on the sdl backend exits cleanly" 0
expect "the probe context is plain EGL on its own config" "$WORK/present-sdl-probe.log" \
    "[mockgpu] eglCreateContext config=3 GLES 2.0 share=none"
expect "the probe does not build SDL's window" "$WORK/present-sdl-probe.log" \
    "is plain EGL; SDL's window waits for a window config"
expect "SDL's window still gets the game's window config" "$WORK/present-sdl-probe.log" \
    'fbdev 1280x720, config rgba 8880 depth 24 stencil 8, ES3 config'
expect_count "with a probe: every frame through SDL_GL_SwapWindow" "$WORK/present-sdl-probe.log" 5 \
    "[fakesdl] SDL_GL_SwapWindow frame"
expect "EGL game checks (sdl, probe context)" "$WORK/present-sdl-probe.log" "[egl-test] all checks passed"
# eglReleaseThread, then eglMakeCurrent again: SDL must forget its record, or
# its "already current" shortcut leaves the 6th frame with nothing current.
# ANDROIDPAK_GLES2_LIB names SDL's GLES library too.
run_to "$WORK/present-sdl-release.log" env "$MOCK" "$FAKESDL" ANDROIDPAK_PRESENT=sdl $STOCK_NO_KMS \
    EGLTEST_RELEASE_THREAD=1 ANDROIDPAK_GLES2_LIB=/opt/gles/libGLESv2.so.2 \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "eglReleaseThread on the sdl backend: the game exits cleanly" 0
expect "eglReleaseThread: SDL lets go first, then EGL's own call" "$WORK/present-sdl-release.log" \
    "[mockgpu] eglReleaseThread (none was current)"
expect_count "eglReleaseThread: the frame after it is presented too (6 of 6)" "$WORK/present-sdl-release.log" 6 \
    "[fakesdl] SDL_GL_SwapWindow frame"
expect_not "eglReleaseThread: no swap with nothing current" "$WORK/present-sdl-release.log" "eglSwapBuffers failed"
expect "ANDROIDPAK_GLES2_LIB is SDL's GLES library" "$WORK/present-sdl-release.log" \
    "[fakesdl] SDL_GL_LoadLibrary: GLES library /opt/gles/libGLESv2.so.2"
expect "EGL game checks (sdl, eglReleaseThread)" "$WORK/present-sdl-release.log" "[egl-test] all checks passed"
# The firmware ships only an unversioned libGLESv2.so beside its libEGL: SDL
# is pointed at that file (its own default libGLESv2.so.2 would not load).
mkdir -p "$WORK/gpu"
ln -sf ../libmockgpu.so "$WORK/gpu/libEGL.so"
: > "$WORK/gpu/libGLESv2.so"
run_to "$WORK/present-sdl-gles.log" env ANDROIDPAK_EGL_LIB="$WORK/gpu/libEGL.so" "$FAKESDL" ANDROIDPAK_PRESENT=sdl \
    $STOCK_NO_KMS ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect "SDL loads the GLES library beside the chosen libEGL" "$WORK/present-sdl-gles.log" \
    "[fakesdl] SDL_GL_LoadLibrary: GLES library $WORK/gpu/libGLESv2.so"
expect "EGL game checks (sdl, firmware GLES name)" "$WORK/present-sdl-gles.log" "[egl-test] all checks passed"
# An import scan draws nothing: ANDROIDPAK_PRESENT=sdl does not start SDL.
run_to "$WORK/present-sdl-scan.log" env "$MOCK" "$FAKESDL" ANDROIDPAK_PRESENT=sdl $STOCK_NO_KMS ANDROIDPAK_SCAN=1 \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so $TO "$LOADER"
expect "a scan run says SDL2 is not started" "$WORK/present-sdl-scan.log" \
    "SDL2 is not started for an ANDROIDPAK_SCAN run"
expect_not "a scan run does not start SDL" "$WORK/present-sdl-scan.log" "[fakesdl]"
expect_not "a scan run touches no EGL display" "$WORK/present-sdl-scan.log" "[mockgpu] eglGetDisplay"
# auto on the stock firmware, but the raw fbdev window is refused (the mock's
# framebuffer only takes its own 1280x720; the game asked for 640x360
# buffers): SDL takes over at the game's window surface.
run_to "$WORK/present-fallback.log" env "$MOCK" "$FAKESDL" $STOCK_NO_KMS MOCKGPU_FBDEV_SIZE=1280x720 \
    EGLTEST_BUFFERS=640x360 ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "a refused native window falls back to SDL and the game runs" 0
expect "auto starts on the native backend" "$WORK/present-fallback.log" \
    "backend native (EGL $WORK/libmockgpu.so, window: fbdev {width, height} struct)"
expect "the raw 640x360 window is refused" "$WORK/present-fallback.log" "native=OTHER(640x360) refused"
expect "SDL takes over at the window surface" "$WORK/present-fallback.log" \
    "[androidrt][present] backend sdl (fallback: the native window surface was refused (0x300b);"
expect "the game renders at SDL's full size" "$WORK/present-fallback.log" "[egl-test] drawing to a 1280x720 surface"
if [ "$(count_of "$WORK/present-fallback.log" '[fakesdl] SDL_GL_SwapWindow frame')" = 5 ]; then
    pass "after the fallback every frame goes through SDL (5 of 5)"
else
    fail "after the fallback every frame goes through SDL"
fi
expect "EGL game checks (fallback at the window)" "$WORK/present-fallback.log" "[egl-test] all checks passed"
# The same, but the game made its context before the window (native, raw):
# it keeps that context, the log says so and names ANDROIDPAK_PRESENT=sdl,
# and its frames are counted as plain eglSwapBuffers, not SDL's.
run_to "$WORK/present-fallback-ctxfirst.log" env "$MOCK" "$FAKESDL" $STOCK_NO_KMS MOCKGPU_FBDEV_SIZE=1280x720 \
    EGLTEST_BUFFERS=640x360 EGLTEST_CONTEXT_FIRST=1 ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so \
    ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "a context-first game survives the window fallback" 0
expect "the raw context is named, with the way out" "$WORK/present-fallback-ctxfirst.log" \
    "the game made its GLES context before its window — it stays raw EGL"
expect "the way out is ANDROIDPAK_PRESENT=sdl" "$WORK/present-fallback-ctxfirst.log" \
    "if the panel is black set ANDROIDPAK_PRESENT=sdl"
expect_count "the raw context's frames are not SDL's" "$WORK/present-fallback-ctxfirst.log" 0 \
    "[fakesdl] SDL_GL_SwapWindow frame"
expect "the exit line counts them as plain eglSwapBuffers" "$WORK/present-fallback-ctxfirst.log" \
    "SDL presented 0 frames (5 more went through plain eglSwapBuffers on its surface)"
expect "EGL game checks (fallback at the window, context first)" "$WORK/present-fallback-ctxfirst.log" \
    "[egl-test] all checks passed"
# The window is refused and SDL's video init fails too: both reasons are
# named, and the game reads EGL's own error for its window, not EGL_SUCCESS.
run_to "$WORK/present-fallback-sdlfail.log" env "$MOCK" "$FAKESDL" $STOCK_NO_KMS MOCKGPU_FBDEV_SIZE=1280x720 \
    EGLTEST_BUFFERS=640x360 FAKESDL_FAIL_INIT=1 ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so \
    ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "no window anywhere: the game fails its own check" 3
expect "a failing SDL video init is named after the refused window" "$WORK/present-fallback-sdlfail.log" \
    "the native window surface was refused (0x300b), and the SDL fallback is not usable (SDL_InitSubSystem(VIDEO) failed: mali-fbdev: Could not open framebuffer device)"
expect "the game reads EGL_BAD_NATIVE_WINDOW, not EGL_SUCCESS" "$WORK/present-fallback-sdlfail.log" \
    "[egl-test] eglCreateWindowSurface failed: 0x300b"
# The same without any SDL2 (auto, nothing at ANDROIDPAK_SDL_LIB).
run_to "$WORK/present-fallback-nosdl.log" env "$MOCK" $STOCK_NO_KMS MOCKGPU_FBDEV_SIZE=1280x720 \
    EGLTEST_BUFFERS=640x360 ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect "auto without SDL2 names it at the refused window" "$WORK/present-fallback-nosdl.log" \
    "the native window surface was refused (0x300b), and the SDL fallback is not usable ($WORK/no-libSDL2-2.0.so.0 not loadable"
expect "auto without SDL2: the game reads EGL_BAD_NATIVE_WINDOW" "$WORK/present-fallback-nosdl.log" \
    "[egl-test] eglCreateWindowSurface failed: 0x300b"
# auto, and the native display cannot even initialise before SDL's video
# driver has run (MOCKGPU_NEEDS_SDL): the game's own eglInitialize fails, SDL
# takes over there and the call is made once more.
run_to "$WORK/present-fallback-init.log" env "$MOCK" "$FAKESDL" $STOCK_NO_KMS MOCKGPU_NEEDS_SDL=1 \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "a native display that does not initialise falls back to SDL" 0
expect "auto still starts native (nothing is decided before the game asks)" "$WORK/present-fallback-init.log" \
    "backend native (EGL $WORK/libmockgpu.so, window: fbdev {width, height} struct)"
expect "the fallback line names the failed eglInitialize" "$WORK/present-fallback-init.log" \
    "[androidrt][present] backend sdl (fallback: the native EGL display did not initialise (0x3001); EGL $WORK/libmockgpu.so, SDL $WORK/libfakesdl.so, video driver mali)"
expect "the game's eglInitialize is retried once SDL's driver ran" "$WORK/present-fallback-init.log" \
    "the game's eglInitialize succeeded once SDL's video driver ran"
expect_count "after the eglInitialize fallback every frame goes through SDL (5 of 5)" "$WORK/present-fallback-init.log" 5 \
    "[fakesdl] SDL_GL_SwapWindow frame"
expect "EGL game checks (fallback at eglInitialize)" "$WORK/present-fallback-init.log" "[egl-test] all checks passed"
# SDL's EGL display is another one (a KMSDRM SDL's own device) and the
# default display never comes up: said plainly; the game reads EGL's error.
run_to "$WORK/present-default-down.log" env "$MOCK" "$FAKESDL" $STOCK_NO_KMS MOCKGPU_DEFAULT_DOWN=1 FAKESDL_DISPLAY=device \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "a default display that never comes up fails the game's check" 3
expect "SDL is tried at the failed eglInitialize" "$WORK/present-default-down.log" \
    "[androidrt][present] backend sdl (fallback: the native EGL display did not initialise (0x3001);"
expect "a display SDL cannot help with is said plainly" "$WORK/present-default-down.log" \
    "SDL2 is up, but eglInitialize of the default display still fails"
expect "the game reads EGL_NOT_INITIALIZED, not EGL_SUCCESS" "$WORK/present-default-down.log" \
    "[egl-test] eglInitialize failed: 0x3001"
# Asked for, but no SDL2: the reason is named and the backend is chosen like
# auto — native on the stock firmware, gbm where KMS has a panel.
run_to "$WORK/present-nosdl.log" env "$MOCK" ANDROIDPAK_PRESENT=sdl ANDROIDPAK_SDL_LIB=/nonexistent/libSDL2-2.0.so.0 \
    $STOCK_NO_KMS ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect_rc "ANDROIDPAK_PRESENT=sdl without SDL2 still runs the game" 0
expect "a missing SDL2 is named, the backend is chosen like auto" "$WORK/present-nosdl.log" \
    "ANDROIDPAK_PRESENT=sdl but SDL2 is not usable (/nonexistent/libSDL2-2.0.so.0 not loadable"
expect "EGL game checks (sdl asked, native used)" "$WORK/present-nosdl.log" "[egl-test] all checks passed"
run_to "$WORK/present-nosdl-gbm.log" env "$MOCK" ANDROIDPAK_PRESENT=sdl ANDROIDPAK_GBM_LIB="$WORK/libmockgpu.so" \
    ANDROIDPAK_DRM_LIB="$WORK/libmockgpu.so" ANDROIDPAK_DRM_NODE=/dev/null EGLTEST_EXPECT_SCANOUT_VISUAL=1 \
    ANDROIDPAK_MAINLIB=lib/arm64-v8a/libfake_egl.so ANDROIDRT_SMOKE_FRAMES=5 $TO "$LOADER"
expect "a failed ANDROIDPAK_PRESENT=sdl still finds gbm" "$WORK/present-nosdl-gbm.log" "[androidrt][present] backend gbm"
expect "EGL game checks (sdl asked, gbm used)" "$WORK/present-nosdl-gbm.log" "[egl-test] all checks passed"

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
    "$SRC/looper.c" "$SRC/surface.c" "$SRC/dlport.c" "$SRC/apkzip.c" "$SRC/device.c" -o "$WORK/input_probe" \
    -pthread -ldl
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
