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
- **Cinematic and Roll boot styles** push the startup chime back behind their
  entrance (at most 2.5 s) and add a knock on every landing: a low sine
  falling to half its pitch under a short burst of muffled noise
  (`chimeThudNote`). Cinematic knocks three times as the logo lands and hops
  (190, 180 and 175 Hz, each quieter); Roll knocks at 420 Hz on every
  quarter-turn that lands on screen, the last a little louder.

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
./chime cinematic 48000 cinematic.wav 1.55        # add "lift" for a power-on from the firmware's logo
./chime roll 48000 roll.wav 1.55                  # Smart Pro; "1024 768" lays the roll out for the Brick
python3 analyze.py startup.wav shutdown.wav cinematic.wav roll.wav   # levels, spectrum, spectrogram PNG
```

The program prints the gain that would put the peak at 0.56 of full scale;
copy it into `chimeStartGain` / `chimeStopGain`. For the boot styles it also
prints the lead, how far the bells moved and each knock's time, pitch and
level; the knock timings mirror `prepareDrop` / `prepareRoll` in
`player/bootstyles.go` and the logo layout in `bootlogo_layout.go`, so change
them together too. With the knocks the styles' peaks stay under the start
chime's (the gain stays 1.55). Listen to the result (and run `analyze.py`): no
Go test checks the chimes. Whether the knocks are audible on the handheld's
speakers is a device check (HW-1 step 3). Rendered WAVs are not kept in the
repo.
