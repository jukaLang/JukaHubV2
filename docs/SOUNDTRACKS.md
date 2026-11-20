# Game soundtracks

The music player plays the music files of old games and computers: NES, SNES,
Game Boy, Sega, PC Engine, MSX, ZX Spectrum, Atari 8-bit and Commodore 64
tunes, and the tracker modules of the demo scene. They are not recordings but
the game's own music program, played by an emulated sound chip, so a whole
soundtrack is often a file of a few kilobytes.

They play like any other song: the same playlist, shuffle, repeat, queue,
favorites, sleep timer, equaliser and speed, the same Now Playing screen, and
the same **screen-off** mode and background playback under games (see
[MUSIC_PLAYER.md](MUSIC_PLAYER.md#screen-off)). With the screen off the
handheld only runs the sound chip, which is the low-power way to listen.

## Formats

| File | System | Songs per file | Played by |
|------|--------|----------------|-----------|
| `.nsf`, `.nsfe` | NES / Famicom | many | ffmpeg (libgme) |
| `.spc` | SNES | one | ffmpeg (libgme) |
| `.gbs` | Game Boy | many | ffmpeg (libgme) |
| `.vgm`, `.vgz`, `.gym` | Mega Drive, Master System, Game Gear | one | ffmpeg (libgme) |
| `.hes` | PC Engine | one (the file's first song) | ffmpeg (libgme) |
| `.kss` | MSX | one (the file's first song) | ffmpeg (libgme) |
| `.ay` | ZX Spectrum | many | ffmpeg (libgme) |
| `.sap` | Atari 8-bit | many | ffmpeg (libgme) |
| `.sid` (PSID) | Commodore 64 | many | JukaHub's own SID player |
| `.mod`, `.xm`, `.s3m`, `.it`, `.mptm` | tracker modules | one | ffmpeg (libopenmpt) |

Put the files anywhere Files can browse, for example `Music/Soundtracks/<game>`.

## Playing them

- **Files → a soundtrack file.** A file with many songs (an NSF holds a game's
  whole soundtrack) opens as its own album: one row per song, starting at the
  song the file names first. A file with one song (an SPC, a VGM) plays its
  folder, like any song. In a very big folder (an HVSC directory of hundreds
  of SIDs) the songs of the files the player could not read in a moment stay
  one row each, playing the song the file starts with; open such a file on
  its own to list all its songs.
- **Music → Albums.** The Media index lists a soundtrack under its game, with
  the console as the album artist, so an SPC folder is one album and
  **Artists** reads NES, SNES, C64… Opening the album lists every song.
- Songs are named from the file's own tags (NSFe, GBS, SPC ID666, VGM GD3, AY,
  SAP, SID headers). A file that names no songs shows "<game> · Song 3".

Now Playing shows the console, the song's place in the file and the game:
"SNES · Chrono Trigger · Yasunori Mitsuda", "NES · Song 3 of 24 · Mega Man 2".
The artwork tile shows the game's cover from the **Games library** when the
library has downloaded one (matched by the game's title), else the folder's own
picture (`cover.jpg`, `folder.png`…); the lock screen and the dock use the same
picture.

## Song lengths

Game music usually loops for ever. Each song plays for the length its file
gives (NSFe, SPC, VGM, AY and SAP files usually have one) and then fades out
over a few seconds; a song with no length plays for 2:30, the fade included.
A VGM that does not loop simply ends. Seeking works: the chip is run quickly up
to the new place, so a long jump into a song can take a moment.

## Commodore 64 (SID)

ffmpeg cannot play SID tunes, so JukaHub carries a small C64 of its own: a 6510
processor (with the undocumented instructions music drivers use) running the
tune's code, and a model of the SID chip with its three voices, four waveforms,
ring modulation, sync, envelopes and filter. It is built to be cheap rather than
exact:

- PSID tunes play; **RSID** tunes need a whole C64 (interrupts, the KERNAL ROM)
  and are refused with a message, as are old Sidplayer (MUS) files.
- Combined waveforms are approximated, and 4-bit sample playback through the
  volume register ("digis") is not reproduced.
- A tune for two or three SID chips plays its first chip only (the parts
  written for the other chips are left out).
- The chip model follows the tune (6581 or 8580). To force one, set
  `"chiptune_sid_model": "6581"` (or `"8580"`) in `jukaconfig.json`.

## Playlists

A saved or exported playlist names one song of a soundtrack as the file plus
`#` and the song number, for example `Mega Man 2.nsf#3`. JukaHub reads these
back from its own M3U8 exports; other players do not know the form.

## When a format does not play

The console formats need an ffmpeg built with **libgme**, the modules one with
**libopenmpt**. JukaHub's own ffmpeg (the BtbN build it downloads into its
tools folder, see [DEPENDENCIES.md](DEPENDENCIES.md)) should have both; the
TrimUI firmware's own copy on the system path may have neither. JukaHub asks
the ffmpeg it uses once and, when one is missing, says so plainly ("This ffmpeg
cannot play NSF/SPC/VGM music") and skips those songs instead of going quiet.
Neither has been verified on a handheld yet.

libgme plays the Sega chips of VGM files (SN76489, YM2612, YM2413); VGMs from
arcade boards and other chips are not part of it and play silent or partly.

The code is in `player/chiptune*.go` (formats, tags, decoder input, covers) and
`player/sidplay_*.go` (the SID player).
