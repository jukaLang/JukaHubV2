# JukaHub Spatial — the game-audio plugin

Virtual surround for games. JukaHub's own media (videos, music, YouTube) is
spatialised inside the player (`player/spatial_dsp.go`); games are separate
processes, so for them the same DSP ships as a small native library that the
emulator loads itself.

One library, `jukahub-spatial.so` (`jukahub-spatial.dll` on Windows), is two
plugins at once:

* a **RetroArch audio DSP filter** — RetroArch's own `audio_dsp_plugin`
  mechanism, which JukaHub turns on for one launch at a time
  (`player/spatial_games.go`);
* a **LADSPA plugin** (label `jukahub_spatial`), for ALSA's `ladspa` PCM or
  any LADSPA host — JukaHub's route for every game and app on the handheld
  (`player/spatial_alsa.go`, below).

## The DSP

[`jh_spatial_ref.py`](jh_spatial_ref.py) is the specification: every
constant, formula and the order of every operation. `jukahub_spatial.c` is a
line-by-line port (float samples, every coefficient computed in double the
way the reference computes it), and the host tests compare it with the golden
vectors the reference writes, `player/testdata/spatial/*.txt`, to 5e-6 (it
matches to about 5e-7; the tolerance is tight enough that a constant off by a
little — a small-room RT60 of 0.27 instead of 0.25, or 23 loudness points
instead of 24 — fails).

* **Headphones** (mode 1): four virtual loudspeakers — L and R at ±30°, the
  band-limited, allpass-decorrelated side signal at ±110° 15 ms later — each
  reaching each ear through a spherical-head model (Brown & Duda 1998:
  head-shadow filter and path delay, plus a low-pass on the far ear); six
  early reflections per ear and a 4-line feedback delay network for a small,
  medium or large room; loudness normalised so switching it on does not
  jump; a soft limiter whose output stays strictly inside (−1, 1) — its
  tanh curve is capped at 0.99999994, the largest float below 1.
* **Speakers** (mode 2): the side signal above 300 Hz is widened; bass stays
  centred.
* **Off** (mode 0) or strength 0: the samples are not touched.

Settings: `mode` 0/1/2, `strength` 0–1 (default 0.75), `room` 0/1/2 (small,
medium, large; default medium). Any sample rate from 8 kHz to 384 kHz.

Robustness rules, from the reference: an input sample that is not a finite
number (NaN, ±inf — a LADSPA chain can deliver one; RetroArch's int16 path
cannot) is processed as 0 before it reaches any state, so it never turns
into a full-scale burst; a NaN strength is 0, i.e. off; and a state that
still runs away (absurd finite input) is reset at the end of the block.

## Build

```sh
sh player/spatialfx/build.sh                              # this machine
CROSS=aarch64-linux-gnu- sh player/spatialfx/build.sh     # TrimUI (Ubuntu cross gcc)
CC="aarch64-linux-gnu-gcc --sysroot=$SYSROOT" \
  CROSS=aarch64-linux-gnu- sh player/spatialfx/build.sh   # the TrimUI toolchain image
TARGET=windows CROSS=x86_64-w64-mingw32- sh player/spatialfx/build.sh
```

Output goes to `OUT` (default `player/spatialfx/build/`, git-ignored). The
library is built, stripped and checked in a private temporary folder and only
then moved into `OUT` (an older copy there is removed first), so a failed
build or a missing check tool never leaves an unchecked library where a
package would pick it up. The script **fails** when:

* the library needs anything but `libc`, `libm` and the dynamic loader
  (`ld-linux*.so.*` — a compiler that turns the stack protector on by default,
  like Ubuntu's aarch64 cross gcc, links the canary `__stack_chk_guard` from
  `ld-linux-aarch64.so.1`; the loader is in every dynamic process anyway);
* a glibc symbol version is newer than `MAX_GLIBC` (default 2.17, the first
  aarch64 glibc — the Smart Pro has 2.33);
* the ELF (or PE) is for another CPU than the compiler line names
  (`aarch64` → AArch64, else the host's; `EXPECT_MACHINE=AArch64` overrides) —
  a mis-set `CC` cannot ship an x86-64 library in a handheld package;
* `strip` exists but fails, no `readelf`/`objdump` can read the result, or one
  of the two entry points is not exported.

The source avoids `exp`/`pow`/`log` on purpose: glibc 2.29 gave them new
symbol versions, so a library built on a current distribution would otherwise
not load on an older handheld. From libm it uses `cos`, `sin` (GCC merges a
cos/sin pair into `sincos`), `floor`, `sqrt` and `tanhf`.

CI: `docker-image.yml` builds it with the player's own cross compiler and
sysroot and ships it as `Apps/JukaHub/lib/jukahub-spatial.so`
(continue-on-error: a failed build ships the archive without it, and
Settings then says the game plugin is not installed). `player/release.sh` and
`player/package-sdcard.sh` (the release and SD-card archives) build it the
same way when an aarch64 cross gcc is installed and put it in every TrimUI
archive's `lib/`. The weekly checks (`.github/workflows/weekly.yml`) run the
host tests below.

## Test

```sh
sh player/spatialfx/tests/run-tests.sh      # or: make test-spatialfx (Linux)
```

Builds the library with `build.sh` (so the link checks run too), then
`tests/test_spatial.c`:

1. the C API against all seven golden vectors, in odd-sized chunks;
2. the built `.so` through `dlopen` exactly as RetroArch drives it
   (`dspfilter_get_implementation` → `init` with a config → in-place
   `process` → `free`);
3. the same `.so` as a LADSPA host drives it (descriptor → instantiate →
   connect → activate → run), with separate and in-place buffers;
4. off, strength 0 and a NaN strength bit-exact on all three paths;
5. robustness: the limiter's bound at full scale and its exact ceiling far
   over it, NaN/±inf input processed exactly as 0 (both modes, interleaved and
   planar), runtime room and mode changes, rates from 8 kHz to 192 kHz (and
   32040.5 Hz), refused rates, clamping, reset determinism;
6. a speed figure (about 100× real time on a desktop core; expect roughly
   15–25× on the A133P's Cortex-A53, a few percent of one core).

`SPATIAL_WORK=/tmp/x` keeps the build; ASan/UBSan (the library itself must
be a normal build — `build.sh` refuses a sanitizer runtime in `NEEDED`):
`OUT=/tmp/x sh build.sh && gcc -std=c99 -O1 -g -fsanitize=address,undefined tests/test_spatial.c jukahub_spatial.c -lm -ldl -o /tmp/x/t && /tmp/x/t ../testdata/spatial /tmp/x/jukahub-spatial.so`.

## How JukaHub uses it

`spatial_games.go`, at each RetroArch launch from the library when
**Spatial audio in games** is on and the effective mode is not off:

1. finds the library (`JUKAHUB_SPATIAL_PLUGIN`, else `lib/` beside the
   JukaHub binary);
2. checks that this RetroArch can load DSP filter libraries (the binary holds
   the strings `audio_dsp_plugin` and `dspfilter_get_implementation`; the
   answer is cached per binary path, size and time). Only "the setting is
   there but the loader is not" is a confident no; a binary that holds
   neither string (a wrapper script, a packed binary) is unknown and the
   launch tries — `ra64.trimui`/`ra32.trimui` are probed before a file named
   `retroarch`;
3. copies the library into `<data>/spatial-audio/` beside a freshly written
   `JukaHub-Spatial.dsp` — RetroArch loads the filter libraries from the
   preset's own folder:

   ```
   filters = 1
   filter0 = jhspatial
   jhspatial_mode = "1"
   jhspatial_strength = "0.75"
   jhspatial_room = "1"
   ```
4. sets `audio_dsp_plugin` to that preset for this launch only, through the
   launch journal (`racfgscope.go`), which puts the user's value back when
   the game exits. A filter the user chose in RetroArch is left alone (and the
   Audio panel's row says so). A JukaHub preset that a lost journal left in
   `retroarch.cfg` is not the user's: it is removed before the launch's own
   value is recorded, and removed at every launch while the setting is off.

By hand: put the library and a preset like the one above in one folder and
point `audio_dsp_plugin` (Settings → Audio → DSP Plugin) at the preset.

### Through ALSA (every program, the handheld)

`spatial_alsa.go` gives every screen-taking child JukaHub starts (games,
standalone emulators, ports, apps) `ALSA_CONFIG_PATH=<system alsa.conf>:/tmp/jukahub-audio/asound-spatial.conf`
and `AUDIODEV=jhspatial`, once a probe has proven the route on the device. The
file (rewritten at every launch, mode/strength/room of the moment):

```
pcm.jhspatial_out { type plug; slave.pcm "sysdefault" }   # or the BlueALSA headset PCM
pcm.jhspatial_fx {
	type ladspa
	slave.pcm "jhspatial_out"
	path "<data>/spatial-audio"        # a folder holding only this library
	channels 2
	playback_plugins [ {             # recording passes straight through
		label "jukahub_spatial"
		policy none
		input.bindings.0 "Left in"
		input.bindings.1 "Right in"
		output.bindings.0 "Left out"
		output.bindings.1 "Right out"
		input.controls [ 1 0.75 1 ]      # mode, strength, room (control-port order)
	} ]
}
pcm.jhspatial { type plug; slave.pcm "jhspatial_fx" }
@hooks.1 { func load; files [ "/tmp/jukahub-audio/asound-spatial-default.conf" ]; errors false }
```

The hook file makes `default` an `asym` PCM: playback through `jhspatial`,
capture from `sysdefault` as before. alsa-lib reads every
`ALSA_CONFIG_PATH` file before it runs `alsa.conf`'s hook 0 (`/etc/asound.conf`,
`~/.asoundrc`), so a `default` written into the path file itself would lose to
the firmware's; hook 1 runs after it. The hook is left out when a system file
defines `default` (and no headset is routed): the chain then ends in that
`default`, and only SDL programs (AUDIODEV) get it. Both orders were checked
against the real alsa-lib with an `.asoundrc` redefining `default`.

The plugs convert to and from the float samples LADSPA works in (a mono or
non-48 kHz program included). The ladspa PCM opens every `.so` in `path`, so
the library sits alone in `<data>/spatial-audio/` (the RetroArch preset there
is not a `.so`). An unloadable plugin or a libasound without the ladspa PCM
fails the program's open — no sound at all — so the probe
(`JukaHub --spatial-alsa-probe`: SDL audio through the same chain into ALSA's
`null` PCM) must pass first. Checked on a PC against the system's real alsa-lib
(Ubuntu 22.04): the chain opens at 22.05/32/44.1/48 kHz, mono and stereo, and
its output matches the plugin driven directly to one 16-bit step; a wrong label
or folder fails the open, as the probe expects.

## ABI notes — verify before trusting a release

Both ABIs are declared in `jukahub_spatial.c` from their public descriptions,
written from memory because the sandbox this was built in cannot fetch the
upstream headers. Before relying on a release, diff them against:

* libretro-common `include/libretro_dspfilter.h`:
  `dspfilter_info {float input_rate}`, `dspfilter_input/output {float
  *samples; unsigned frames}`, `dspfilter_config` (the plugin reads only its
  first member, `get_float(userdata, key, &value, default)`),
  `dspfilter_implementation {init, process, free, unsigned api_version,
  const char *ident, const char *short_ident}`, `DSPFILTER_API_VERSION` 1,
  and `dspfilter_get_implementation(dspfilter_simd_mask_t)`;
* `ladspa.h` 1.1: `LADSPA_Descriptor` field order, the port and hint bit
  values.

The host test drives both ABIs through `dlopen`, so a layout mismatch with
the test's own copy is caught; a mismatch of both with upstream is not. The
LADSPA side has since been driven by a real host — alsa-lib's ladspa PCM, built
against its own copy of `ladspa.h` — and processed audio correctly (above).

Newer RetroArch master adds an optional int16 path: plugins that declare API
version 2 may export a `process_i16`, and the host only reads that field from
version-2 plugins (it checks `api_version < 2` first; seen in search excerpts
of `dsp_filter.c`, not in the full source). This plugin deliberately stays at
version 1 with the float `process`, which old builds load and new ones still
chain (they only lose the int16 fast path). Check that a current loader still
accepts version 1 when the real header can be read; a version-2
`process_i16` is the follow-up.

## Limits

* **Nothing has run on a handheld yet.** Whether the TrimUI firmware's
  RetroArch (`ra64.trimui`) was built with dynamic DSP filters
  (`HAVE_DSP_FILTER` with `HAVE_DYLIB`, not `HAVE_FILTERS_BUILTIN`) is
  unverified; the string probe is the guard, and an unsupported build only
  logs `[DSP]: Failed to initialize DSP filter` and plays normally.
* RetroArch runs the filter at the core's own rate, before its resampler
  (32040.5 Hz for some cores); every coefficient is derived from that rate.
* The mode is fixed for the game session: `auto` is resolved when the game
  starts (headphones in → headphones mode).
* The ALSA route needs the firmware's alsa-lib to carry the ladspa PCM, which
  the probe checks on the device. The library is 64-bit only: a launch that
  names a 32-bit ARM program (its binary, or one its script names; "ra32"
  anywhere in the script) is left alone, since the program could not load the
  plugin and its sound would not open; a program that bypasses alsa-lib (tinyalsa,
  OSS) is not reached, and one started with its own `ALSA_CONFIG_PATH` or
  `AUDIODEV` is left alone.
* The LADSPA unique ID (7310) is not registered with ladspa.org; hosts select
  the plugin by label.
