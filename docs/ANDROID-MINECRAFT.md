# Minecraft: Bedrock Edition on AndroidRT

**Status: experimental, never run on hardware.** The pieces below are built and
pass the host smoke suite with a fake JNI-heavy game; nobody has started a real
Minecraft APK on a TrimUI handheld yet. This page says what exists, what the game
will meet, and what to send back from the first device run.

## What AndroidRT provides

Minecraft's Android build is a native library (`libminecraftpe.so`, plus
`libfmod.so` and `libc++_shared.so`) whose `MainActivity` Java class it calls
constantly through JNI. AndroidRT has no Java VM; its JNI environment
([ANDROID.md](ANDROID.md#the-java-side-androidrts-jni-environment)) answers
those calls, and the Minecraft class pack
([`player/androidrt/src/jnipack_minecraft.c`](../player/androidrt/src/jnipack_minecraft.c))
implements the Java methods the game uses, for the packages
`com.mojang.minecraftpe` and `com.mojang.minecrafttrialpe`:

* **Real answers:** storage paths (the profile's persistent data folder, so
  worlds survive the tmpfs stage), screen size and physical DPI of the panel,
  locale, memory figures, network link state, asset and file reads straight
  from the APK, the clipboard, a random per-install device id (not a hardware
  identifier), a plain-text key/value store for `getSecureStorageKey` (there
  is no keystore), CPU facts, and the APK's own signing-certificate hash.
* **`MainActivity`'s superclass** comes from the APK's DEX index: older
  builds extend `NativeActivity`, newer ones androidx `GameActivity`, which
  AndroidRT drives through its natives (`src/gameactivity.c`, host-tested
  with a fake GameActivity glue, never with the real game).
* **Audio:** FMOD is told AAudio is unavailable, so it uses OpenSL ES, which
  AndroidRT implements over ALSA (`src/openslessim.c`).
* **Input:** the evdev reader turns the handheld's buttons into Android
  gamepad key and motion events; Bedrock supports controllers.

## What is honestly unavailable

AndroidRT never reports a license, purchase or sign-in that did not happen,
and it never modifies or skips the game's own checks:

* **Store and purchases:** the store never initialises
  (`onStoreInitialized(false)`), product and purchase queries fail, purchases
  are refused.
* **License:** `hasVerifiedLicense()` and `receivedLicenseResponse()` are
  `false` — no license server was asked and no answer is invented. A Google
  Play build is expected to show its license error in that state; the
  launcher projects for desktop Linux require owning the game on Google Play
  for the same reason. What the game does is its own decision.
* **Xbox Live:** sign-in cannot complete; the game stays signed out.
* **On-screen keyboard, URL opening, image picker, sharing:** logged; the
  picker answers "canceled".

## What to expect from the hardware

* **The 64-bit build is required.** Triage refuses an APK that carries only
  `armeabi-v7a` libraries.
* **OpenGL ES 3.0+** is needed by current versions; the GE8300 reports
  OpenGL ES 3.2.
* **Memory is the hard limit.** Bedrock targets phones with 2 GB or more; the
  Smart Pro has 1 GB shared with the hub. Expect to need the lowest render
  distance, and expect older versions to fare better.
* **The APK is large** (hundreds of MB of assets). AndroidRT reads assets
  straight from the APK on the SD card instead of staging them in RAM.

## Running it (when you want to try)

1. Use an APK of a copy you own (JukaHub does not download APKs).
2. Build and install AndroidRT (see [ANDROID.md](ANDROID.md#trying-a-game-on-the-device)),
   create an `androidpak` game profile for the APK and launch it.
3. Send back the launch log. The lines that matter:
   * `[androidrt] manifest:` — package, version and activity the runtime read;
   * `[androidrt][jni] minecraft:` — which superclass and store state it chose;
   * `[androidrt][present] backend` — which display path the firmware took;
   * `undefined symbol` / `trap:` — a bionic function the loader still lacks;
   * `[androidrt][jni] report:` — every Java class and member the game asked
     for that AndroidRT does not implement yet. That list is the work queue.

## Known gaps before the title screen

* GameActivity builds: the bridge has only been driven by the fake glue in
  the smoke suite.
* The Java surface is built from public knowledge of the game's JNI calls; a
  real run will name more members (the report lists them).
* FMOD's Java side is minimal (sample rate, block size, asset manager).
* No text input yet: `showKeyboard` should open JukaHub's on-screen keyboard.
