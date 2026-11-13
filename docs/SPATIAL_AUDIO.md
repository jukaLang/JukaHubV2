# Spatial audio

**Spatial audio** is JukaHub's virtual surround. On headphones it places what
you hear around you — in front, to the sides and a little behind — instead of
inside your head. On the handheld's own speakers it widens the stereo picture.
It covers JukaHub's own videos and music and, on the handheld, the sound of
every game and app you start from JukaHub. It is off until you turn it on.

## Where to switch it

* **Settings → Sound & haptics → Spatial audio**: Off, Auto, Headphones or
  Speakers.
* **Tools → Audio** (the Audio panel, also **K** in the music player) has the
  same switch and three more rows:
  * **Spatial strength** — 25, 50, 75 (the default) or 100%. Low values are a
    light touch; 100% gives the full room and the rear speakers.
  * **Virtual room** — Small, Medium (default) or Large: the room the
    headphone mode plays in, from close and dry to a hall.
  * **Spatial audio in games & apps** — on by default: games and apps follow
    the mode too (below).

Changes are heard within a second (the sound already queued for the speakers
plays out first) without interrupting playback. The one exception: choosing a
mode while **Bit-perfect** is on switches Bit-perfect off, and the music player
then re-opens the current track at the same position, as it does whenever the
equaliser or Bit-perfect changes.

## The modes

| Mode | What it does |
|------|--------------|
| **Off** | The sound reaches the device exactly as it was decoded. |
| **Auto** | Headphones mode while headphones are in use — the 3.5 mm jack, or a Bluetooth headset or earbuds — and Speakers mode on the built-in speakers. A Bluetooth device whose name says it is a speaker (a soundbar, a SoundLink, a Sonos …) gets Speakers mode. The row shows what Auto chose, for example *Auto (headphones)*; a cable plugged in or a headset that connects is noticed within about ten seconds. |
| **Headphones** | Virtual surround: four virtual loudspeakers around you and a small virtual room. |
| **Speakers** | Wider stereo for a pair of speakers close together; bass stays centred. |

On a PC, Auto cannot see whether headphones are plugged in, so it uses the
Speakers mode there; pick **Headphones** by hand when you wear them.

## What it applies to

Everything JukaHub itself plays: videos, the music player, podcasts,
audiobooks, YouTube and the background music. Interface sounds, Juka's voice
and the alarm are left alone. With **Spatial audio in games & apps** on (the
default), games and apps follow too (below).

**Bit-perfect** (Tools → Audio) means nothing touches the decoded samples, so
spatial audio stands aside for everything JukaHub plays while it is on — videos
as well as music, because both reach the speakers through the same path — and
the Spatial audio row then says *bypassed*. Choosing a spatial mode switches
Bit-perfect off, the way turning on the equaliser does. Bit-perfect is a music
player switch: games and apps (below) keep the effect whatever it says.

### Games and apps

Games run in their own process with their own sound, so JukaHub's own media
path cannot reach them. For them the same DSP ships as a small native library,
`jukahub-spatial.so` ([player/spatialfx](../player/spatialfx/README.md)), which
the handheld package carries in `Apps/JukaHub/lib/`. The library reaches a game
two ways, and JukaHub picks one per launch.

**Through ALSA — every game and app on the handheld.** The library is also a
LADSPA plugin, and the handheld's sound library (ALSA) can run one in front of
the real output. A game, emulator, port or app started from JukaHub while
spatial audio is on gets two environment variables that only it sees —
`ALSA_CONFIG_PATH` (the system's `alsa.conf`, then
`/tmp/jukahub-audio/asound-spatial.conf`) and `AUDIODEV=jhspatial` — so its
sound goes *plugin → speakers* (or *plugin → Bluetooth headset* while one is
connected) whether it opens ALSA's default device (RetroArch, most native
programs) or the one SDL is told about (PPSSPP, DraStic and most other
standalone emulators). The mode, strength and room are those of the moment the
program starts. No firmware file is changed.

A broken route would leave a game silent, so before JukaHub uses it, it checks
it once per start, about 20 seconds after JukaHub opens (and again when the
Audio panel row is looked at): the system's ALSA library must have its LADSPA
support, and a copy of JukaHub started as `JukaHub --spatial-alsa-probe` must
open its sound through the whole chain by name, ending in ALSA's silent "null" device so
the speaker is never touched. Until the check has passed, games play as they
always did. When a system file redefines ALSA's default device itself (a
firmware `asound.conf`), JukaHub does not wrap it: SDL programs still get the
effect (through `AUDIODEV`), and RetroArch keeps the filter route below.

**As RetroArch's audio filter.** The library is also a RetroArch audio DSP
filter. When the ALSA route is not available, or the installed RetroArch is
proven to load filter libraries, every RetroArch game started from JukaHub's
library gets a preset,
`<data>/spatial-audio/JukaHub-Spatial.dsp`, with the mode, strength and room
of that moment (Auto is resolved when the game starts, and stays for the whole
game), and RetroArch's `audio_dsp_plugin` points at it for that launch only.
When the game exits, the launch journal puts your own value back, and an audio
filter you picked in RetroArch yourself is never replaced.

A launch never uses both: a game whose RetroArch got the filter is not also
sent through ALSA.

The switch only matters while a mode is chosen: with the mode Off, games play
as they always did. The row reads *Checking…* for a moment the first time,
while JukaHub checks the routes, *On · RetroArch only* when the ALSA route is
not available here but RetroArch's filter is, and *Not available* when neither
can work — no plugin (the Windows build has none yet), or no route; pressing it
then says why, and the log (`spatial`) has the check's own words. A switch left
on after the plugin went away reads *On · not available* and can still be
turned off. A program started with its own `ALSA_CONFIG_PATH` or `AUDIODEV` is
left alone. None of this has run on a handheld yet: the ALSA chain itself was
checked against a real alsa-lib on a PC, sample for sample.

## How it works

The headphone mode is a *parametric* model — nothing is measured or
downloaded:

* The stereo input plays through four virtual loudspeakers: a front pair at
  ±30° fed with left and right, and a rear pair at ±110° fed with the
  ambience (what differs between left and right), band-limited, decorrelated
  and delayed 15 ms so voices stay in front.
* Each loudspeaker reaches each ear through a model of a head (Brown & Duda,
  1998): the head's shadow on the far ear, the extra path to it (up to about
  0.6 ms), and a gentle extra low-pass on the far side.
* Six early reflections per ear and a short reverb (about 0.25 s, 0.4 s or
  0.7 s for the three rooms) give the sense of a room, which is what moves the
  sound out of the head.
* The result is loudness-matched to the original, and a soft limiter keeps
  every sample just inside full scale (it is capped below 1.0), so turning it
  on never makes the sound clip.

The Speakers mode raises the side signal above 300 Hz: binaural cues do not
survive two speakers a few centimetres apart, but a wider image does.

By an estimate (not yet measured on the device) it costs about two per cent
of one of the handheld's four cores, and nothing at all while it is off.

## Limits — what to know

* The head model is generic. It cannot match your own ears the way a measured
  personal HRTF can, so the effect is a wider, more open sound, not a precise
  3D placement, and how strong it feels differs from person to person.
* It has not had a listening test on the handheld yet. If something sounds
  wrong — too much room, a hollow centre, a level jump — say so in a bug report
  (**Tools → Report a bug**) with the mode, strength and room you used.
* Sources with 5.1 or 7.1 audio are mixed down to stereo before the effect,
  so their rear channels arrive as ambience, not as discrete rear speakers.
* On Windows only JukaHub's own media is spatialised: the game filter is not in
  the Windows build yet.
* A game that talks to the sound chip directly (tinyalsa, OSS), bypassing
  ALSA's library, is out of reach of the ALSA route, and so is a 32-bit program
  (an early firmware's `ra32.trimui` RetroArch): the plugin is 64-bit, so
  JukaHub leaves such a launch alone rather than risk its sound.

## Settings keys

| Key | Values | Default |
|-----|--------|---------|
| `spatialMode` | `off`, `auto`, `headphones`, `speakers` | `off` |
| `spatialStrength` | 0–100 (percent; the rows step 25/50/75/100) | `75` |
| `spatialRoom` | `small`, `medium`, `large` | `medium` |
| `spatialGames` | `true` / `false` — games and apps too, while a mode is on | `true` |

## For developers

The DSP's specification is a Python reference,
[player/spatialfx/jh_spatial_ref.py](../player/spatialfx/jh_spatial_ref.py): every
constant and the order of every operation is normative. It writes golden
vectors into `player/testdata/spatial/`; the Go port
([player/spatial_dsp.go](../player/spatial_dsp.go)) follows them, and the C
build for games is checked against them (`make test-spatialfx`). The
media hook is one call in `QueueVideoAudio` ([player/sound.go](../player/sound.go)),
which every JukaHub player feeds; the settings, the Auto rule and the live
snapshot the decoders read are in [player/spatial.go](../player/spatial.go), the
rows in [player/spatial_ui.go](../player/spatial_ui.go), RetroArch's filter in
[player/spatial_games.go](../player/spatial_games.go) and the ALSA route, its
check and the file it writes in [player/spatial_alsa.go](../player/spatial_alsa.go).
