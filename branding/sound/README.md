# The JukaHub chimes

JukaHub plays a short chime when it starts and another when it closes, in the
spirit of a console boot sound. Both are synthesised by the player at run time
([`player/bootchime.go`](../../player/bootchime.go)); there is no audio file in
the app. This folder holds the tools they were designed with.

## The sound

- **Startup (3.3 s)** follows the glass splash. A breath of filtered air rises
  with the pane; a warm D♭ major-ninth pad (D♭ A♭ C E♭ F) swells under it; four
  glass bells climb A♭ D♭ E♭ A♭ from left to right as the wordmark comes into
  focus; a three-note sparkle runs across as the glint does; a soft stereo room
  carries the tail.
- **Shutdown (2.1 s)** is the mirror: three bells fall A♭ E♭ D♭ back to the
  root, right to left, over a pad that closes.

Bells are two-operator FM with a harmonic 3:1 ratio, so they ring like glass
rather than bronze, plus a faint struck-bar partial for the strike. Everything
sits above 110 Hz, where the handheld's speakers play, and both chimes peak
near −5 dBFS before the interface-sound level is applied.

## Settings

Settings → Sound & haptics → **Startup & shutdown sound** (on by default).
The chimes follow **Interface sounds**: they are silent when it is off and play
at the **Interface sound level**. A relaunch within ten minutes skips the
splash and its chime, and a shutdown the firmware asks for (power button) is
silent so it stays quick.

## Changing them

[`chime_reference.c`](chime_reference.c) is a C twin of `bootchime.go`: same
scores, constants and block-streaming structure. Edit both together.

```sh
cc -O2 -o chime chime_reference.c -lm
./chime start 48000 startup.wav 1.55
./chime stop  48000 shutdown.wav 1.50
python3 analyze.py startup.wav shutdown.wav   # levels, spectrum, spectrogram PNG
```

The program prints the gain that would put the peak at 0.56 of full scale;
copy it into `chimeStartGain` / `chimeStopGain`. The player's
`TestChimesRenderCleanly` checks length, peak, silent edges and NaNs at 48 and
44.1 kHz.
