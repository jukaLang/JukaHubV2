# Pads: the sample sequencer

Pads is a 64-pad sample sequencer built into JukaHub. Put sounds on pads, tap
out a beat, chop a recording into slices, build it into patterns, chain the
patterns into a song and play the mix with effects on the analog sticks. It
sits next to the Synth ([controls](CONTROLS.md#synth)) under **Apps**, the
Tools hub and the Start menu, and Home search finds it as "pads", "sampler",
"mpc" or "drum pads".

Pads is JukaHub's own code (`player/pads*.go`). It can use what a GrainBrick
install (a granular instrument for TrimUI handhelds by another author) has
saved: it reads those sound files, nothing else of GrainBrick's.

## Pages

L1 / R1 (Tab on a keyboard) move between six pages. Start (Space) plays and
stops on every page.

| Page | What it does |
|------|--------------|
| **Play** | Sixteen pads of one bank (A–D, so 64 pads). D-pad walks them, **A** plays the focused pad, **Y** picks its sound, **X** opens it on the Pad page, L2 / R2 change bank (Up / Down past the edge does too). **Select** turns on finger drumming. |
| **Beat** | The pattern's grid for the bank's sixteen pads, sixteen steps at a time. **A** sets or clears a step; hold A and move left / right to paint the row. **Y / X** move a step's note a semitone up / down, **Select** accents it, L2 / R2 change the pattern. Up / Down past the edge go to the next bank. |
| **Pad** | The pad's sound and settings: volume, pitch (X / Y move an octave) and fine tune, a one-knob filter (left of the middle low-pass, right of it high-pass), start and end trim, fade in and out, reverse, play mode, choke group, pan, echo and reverb sends, lo-fi and mute. **Chop now** cuts the pad's sound into 4, 8 or 16 pads, in equal slices or at the hits it hears. Copy, paste and clear a pad. L2 / R2 step through all 64 pads. |
| **Song** | Whether Start plays the song or loops the pattern, the pattern being edited and its length (16 to 128 steps), copy to the next empty pattern, clear, a random beat on the bank's first four sounds, and the song: up to 64 slots played in order. On a slot Left / Right change its pattern, **Y** repeats it, **X** removes it and **A** opens its pattern on the Beat page. |
| **FX** | Which effect each of the eight stick directions plays, and "Hold" amounts that keep an effect on without a stick (for the Brick, which has none). |
| **Mix** | Tempo (A taps it), swing, master, compressor, echo time and feedback, reverb size, recording live hits, the starter kit on the bank, clear the bank, a new project, and **Export to WAV**. |

The strip at the bottom shows the eight patterns (lit while one plays) and the
output.

### Play modes and choke groups

A **one-shot** pad plays to the end of its trim. A **hold** pad sounds while its
button is down; in a pattern it sounds until the same pad's next hit (or the
pattern's end). A **loop** pad repeats its trimmed region the same way. Pads in
the same **choke group** cut each other off, like a closed hi-hat stopping an
open one (the starter kit's two hats share group 1).

### Finger drumming

On the Play page, **Select** gives eight pads to your thumbs: the D-pad plays
one row of four (Left, Up, Down, Right, from left to right) and **Y X B A** the
row under it. The pads show which button plays them. L2 / R2 move to the next
two rows, through all four banks; Select (or M on a keyboard) stops drumming.
With **Record live hits** on (Mix page), hits land on the nearest step of the
pattern while it plays.

On a keyboard, `1 2 3 4`, `Q W E R`, `A S D F` and `Z X C V` play the bank's
four rows.

### Stick effects

On the Play and FX pages both sticks play effects; elsewhere the left stick
still moves the focus and only the right stick plays them. How far a stick
leans is how much. Each of the eight directions can play: low-pass filter,
high-pass filter, tempo down, tempo up, fade out, echo, reverb, stutter (an
eighth, then a sixteenth, then a thirty-second as it leans further), crush,
auto-pan and tape stop. The defaults: left stick up high-pass, down low-pass,
left tape stop, right stutter; right stick up reverb, down echo, left crush,
right auto-pan. Stick effects are a performance: an export renders without
them.

## Where sounds come from

**Y** on the Play page, or **Sound** on the Pad page, opens the browser. **A**
puts a sound on the pad, **Y** listens first, B goes up a folder.

| Source | Folder |
|--------|--------|
| Built-in kit | Sixteen sounds rendered by the Synth's engine: kicks, snares, hats, claps, bass notes, leads and chords. New projects have them on bank A. |
| Record a new sample | Opens Voice Memos; back in Pads the recording is first under Voice Memos. |
| Voice Memos | `VoiceMemos` at the top of the SD card (newest first). |
| Samples | `Samples` at the top of the SD card (`Documents/JukaHub/Samples` on a PC), made on first use. Copy your own sounds here over USB, FTP or the phone Companion. |
| GrainBrick | Each GrainBrick install found in the card's ports or apps folders, or the folder in the `PP_GB_DIR` environment variable. Its recordings, samples and exports are listed as files. GrainBrick's slice markers are not read; use **Chop now**. |
| Music | Your music folder. |
| Downloads/music | Synth and Pads exports and downloaded music. |

WAV files open directly. MP3, OGG, FLAC, M4A, OPUS and AIFF go through the
bundled ffmpeg. A sound keeps its first 30 seconds; together the
pads hold about 96 MB of sound, and sounds no pad plays are let go first.

## Saving and exporting

The project saves itself to `pads.json` in JukaHub's data folder a couple of
seconds after a change and when the page closes; Backup & restore includes it.
Only the sounds' paths are saved, so a sound moved or deleted shows as
"Missing" on its pad.

**Export to WAV** renders the song once through (or, when Start plays the
pattern, four times the pattern) plus two seconds of tail to
`Downloads/music/Pads 001.wav`, where the Music player finds it.

## Not included

Pads has no MIDI in or out, and no audio input of its own: recording goes
through Voice Memos.
