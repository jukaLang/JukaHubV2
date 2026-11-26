# Music player

JukaHub's audio player opens from **Files → a song**, the **Music** entry in
the Start menu (**Scenes → Music**), or the `MusicPlayer` scene. The Music entry
opens shelves for albums, artists, audiobooks, history and radio. Opening a file still plays
its folder. Audio keeps playing while you browse the rest of the app, and its
settings live next to the other app data.

This page describes what it does, how it compares with the handheld music
players people know (NXT Retro Music in particular), and the order in which the
remaining gaps are worth closing.

## What ships today

| Area | Behaviour |
|------|-----------|
| Formats | MP3, WAV, OGG, Opus, M4A/AAC, FLAC, WMA, AIFF, DSF and DFF, decoded through the bundled ffmpeg; DSD is converted to PCM for playback |
| Library | Albums, artists and audiobooks from the Media index; an opened file plays its folder. Audiobooks resume at the saved chapter and show Finished when the last chapter ends. |
| Album art | Folder and embedded covers first; missing music covers are looked up through MusicBrainz and Cover Art Archive when online. Set `music_art_online: false` to turn lookup off. |
| Transport | Play/pause, next/previous, stop, seek ±10 s, seek by tapping the rail, volume ±10 %. On a pad or keyboard, Up from the top of the playlist steps onto these controls: Left/Right choose one, A (Enter) presses it, Down goes back. On an internet radio station Play/pause is Play/stop |
| Modes | Shuffle (every track once before wrapping), repeat off / all / one, a play-next queue |
| Favorites | Toggled per track, marked in the list, stored in `musicfavorites.json` |
| Long form | 1/16×–2× speed (pitch kept; the position and the saved resume point count in the track's own time), sleep timer (15/30/60 min, exits playback at zero), resume for files of 8 minutes or more. On a pad **Start** in the player opens them, with lyrics, shuffle, repeat and the full-screen visualizer: the Audio panel's *Now playing* rows |
| Lyrics | A `.lrc` file next to the track; the active line and its neighbours are drawn in the artwork square. Without a timed `.lrc`, the lyrics stored inside the file (ID3 USLT, a FLAC/Vorbis `LYRICS` or `UNSYNCEDLYRICS` comment, MP4 `©lyr`) are used: timed when they carry LRC timestamps, otherwise spread evenly over the song. An `.lrc` without timestamps scrolls the same way |
| Radio | The **Radio** shelf: the free Radio Browser directory (top stations, by country, by genre, search) and your saved stations (see [Radio](#radio)) |
| Visuals | The real spectrum of what is heard in the artwork tile, and ten full-screen visualizers drawn from a 1024-point FFT in four Bayer-dithered shades (see [Visualizers](#visualizers)) |
| Layouts | Auralis Pro (artwork card), StockOS (list first) and **iPod**: click-wheel menus over every source, 38 four-shade themes, six screen transitions, dithered album art and the wheel's tick (see [iPod layout](#ipod-layout)) |
| Sound | 31-band PEQ with JukaHub's and the iPod's presets, AutoEQ headphone profiles (see [AutoEQ](#autoeq)), Tape Mode and stereo effects (see [Sound effects](#sound-effects)) |
| Servers | Navidrome (Subsonic) and Jellyfin: browse, stream through a cache, Download Offline, Jellyfin films and series; Last.fm and ListenBrainz scrobbling with an offline queue ([MUSIC_SERVERS.md](MUSIC_SERVERS.md)) |
| Library tags | Genres and composers (the index reads the tags again once after the update) |
| Games | Music Quiz and Sound Racer (Extras › Games on the iPod screen), Brick Breaker on the Games screen |
| Background | Audio keeps playing when you leave the screen and, by default, under a game; starting a video or a Watch Party stream stops it |
| Transitions | Gapless: the next track starts without a gap, and the last second of a track is no longer cut off |
| USB DAC | Sound moves to a plugged-in USB DAC; Bit-perfect plays lossless tracks at their own rate and bit depth |
| Game soundtracks | NSF, SPC, GBS, VGM/VGZ, GYM, HES, KSS, AY, SAP and C64 SID tunes, plus MOD/XM/S3M/IT modules: one row per song, the game's cover, the console and the song number in Now Playing (see [Game soundtracks](#game-soundtracks)) |

Controls are in [CONTROLS.md](CONTROLS.md#music-player).

## iPod layout

**Settings → Audio → Now Playing layout → iPod** (or Settings › Layout on the
iPod screen to go back) turns the player into a click-wheel iPod: a stack of
menus — Music (Playlists, Artists, Albums, Songs, Genres, Composers,
Favorites, Recently Added, Search), Videos (Continue Watching, Movies, TV
Shows), Podcasts, Audiobooks, Extras, Settings, Shuffle Songs and Now
Playing. **Settings › Source** picks what Music and Videos list: this device,
Navidrome, Jellyfin, or **All sources**, where a song or album on the card and
on a server is one row and plays from the card first.

| Button | Lists | Now Playing |
|--------|-------|-------------|
| Up / Down | Scroll (the wheel; it ticks with Clicker on) | Volume (the bar replaces the progress bar for a moment) |
| A / Right | Open or play | Play / pause (Right: next) |
| B / Left | Back; at the top menu, leave the player | Back (Left: previous) |
| X | Context menu: Play, Shuffle, Play Next, Add to On-The-Go, Download Offline, Go to Album/Artist, Favorite, Info, Rename File…, Delete File | Shuffle, Repeat, Speed, Sleep Timer, Sound Effects, Equalizer, On-The-Go, Favorite, Lyrics, Info, Download Offline |
| Y | Jump to Now Playing | Cover → visualizer → lyrics |
| L1 / R1 | Previous / next song | Previous / next song |
| L2 / R2 | A page up / down | Seek −10 / +10 s |
| Start | Play / pause | Play / pause |
| Select | Screen off (playback continues) | Screen off |
| Menu | Back to the top menu | Back to the top menu |

A keyboard has the same keys plus Enter, Esc, Space, PgUp/PgDn, `/` (search)
and `K` (Audio settings). A song list starts with **Shuffle**; an artist's
albums start with **All Songs**. **On-The-Go** is a saved playlist like any
other (its context menu saves it under a name), so the other layouts and the
M3U8 export see it.

Settings on the iPod screen: Source, Music Servers, Theme (38 four-shade
palettes: Classic, the iPod mini colours, DMG and Pocket, phosphor and amber
monitors, Kyiv…), Transition (Push, Slide, Fade, Zoom, Dither, None), Clicker,
Shuffle, Repeat, EQ (Off and every preset), Edit EQ Curve, AutoEQ Headphones,
Sound Effects, Speed, Sleep Timer, Visualizer, Dithering, Scrobbling, Offline
Cache (size cap and Clear Cache), Downloads, Background Play, Language, Layout
and About. Extras holds the visualizers, the games (Music Quiz, Sound Racer,
Brick Breaker, Tetris, Snake, 2048), the Stopwatch and **Browse Files** (Files
at the Music folder: rename, copy, move, delete and file info).

## Sound effects

**R2** (or **X** on a keyboard) in the player, Settings → Audio → Sound
effects, or Sound Effects on the iPod screen. Each row changes with
Left/Right; **A** puts a row back, **Y** steps through the presets (Walkman,
Worn cassette, Concert hall, Space echo, Dream, Jet, Slow + reverb, Off).

- **Tape Mode**: wow and flutter (a 0.6 Hz wow, 7 and 13 Hz flutter and a slow
  random drift of the capstan), tape hiss, soft saturation, high-frequency
  roll-off (20 kHz down to about 2.5 kHz) and the tape speed (0.50×–1.50×,
  pitch and tempo together like a deck's trimmer).
- **Reverb** (a Freeverb-style room, with its size), **ping-pong delay** (each
  echo jumps to the other ear; time and feedback), **chorus** and **flanger**.
- **Playback speed**: 1/16× to 2× with the pitch kept; audiobooks keep their
  own.

The effects run on the decoded sound in JukaHub itself and a change restarts
the decoder where you are, so it is heard at once. Bit-perfect output bypasses
them, like the equaliser.

## AutoEQ

**Settings → Audio → AutoEQ headphones** (or AutoEQ Headphones on the iPod
screen) corrects a headphone to neutral with a profile from the
[AutoEq](https://github.com/jaakkopasanen/AutoEq) project. The list of every
measured headphone is downloaded once and kept, so the search works offline
afterwards; a profile is downloaded the first time it is picked and kept too.
`ParametricEQ.txt` or `FixedBandEQ.txt` files you put in `Music/AutoEQ` are
listed first. The profile's preamp and filters run before the 31-band PEQ, so
the two stack. Pick **Off** to stop it.

The PEQ itself (L2 in the player, or Settings → Audio → 31-band PEQ → A) now
steps through the iPod's presets as well: Acoustic, Bass Booster, Bass
Reducer, Classical, Dance, Deep, Electronic, Hip-Hop, Jazz, Latin, Loudness,
Lounge, Piano, Pop, R&B, Small Speakers, Spoken Word, Treble Booster, Treble
Reducer and Vocal Booster.

## Visualizers

**Start** (or **V**) in Auralis Pro and StockOS, Y on the iPod's Now Playing,
or Extras › Visualizers. Left/Right change the visualizer, **Y** (or **D**)
the dithering. All ten read the sound the speaker is playing (not the decoder
a second ahead), through a 1024-point FFT folded into 48 bands, the VU level
and peak of each channel, the waveform and a beat detector:

Cassette Deck (turning reels and a 28-segment LED meter), Stereo VU Meter,
Sonic Vortex, Phosphor Scope (an X-Y trace), Spectrum Matrix, Cyber Horizon,
Liquid Metal, Chladni Plate, Star Warp and LCD Plasma. They are drawn at 320
pixels wide and scaled up square; with dithering on they use the theme's four
shades in a Bayer pattern, like an old LCD.

## Music games

- **Music Quiz**: ten rounds, fifteen seconds from the middle of a random
  song, four titles to pick from; a right answer scores 100 plus 10 a second
  left. The best score is kept.
- **Sound Racer**: a racing game whose road is the song. The song is listened
  to once (game soundtracks too) and turned into a map — bends from the bass
  and treble, a gem on every onset in the lane its pitch suggests, an obstacle
  on the heaviest bass hits — which is kept in `racer-maps/`, so the next race
  on that song starts at once.
- **Brick Breaker** joins Snake, Tetris and the other arcade games on the
  Games screen.

## USB DAC

Plug a USB-C DAC (a dongle or a desktop DAC) into the handheld. Within about
two seconds the header shows "USB DAC · <name>", a toast says so, and every
sound JukaHub plays moves to it. A DAC comes before a Bluetooth headset, which
comes before the speaker, so a headset left connected does not take the sound
back. Games and apps you start get the DAC as their output too. Pull the DAC out
and playback pauses ("USB DAC disconnected — playback paused").

The volume keys move the handheld's own speaker chip, which the DAC never hears,
so on the DAC JukaHub applies the header volume itself (like a Bluetooth
headset). Games on the DAC play at full digital level; turn down the DAC or the
headphones.

If the firmware's kernel has no USB audio driver (`snd-usb-audio`), JukaHub
tries to load the module once when the kernel ships it, and otherwise says so
plainly: "USB DAC found, but this firmware's kernel has no USB audio driver
(snd-usb-audio)". No app can play to a DAC on such a kernel. A DAC that keeps
failing to open is left alone, with a toast, until it is plugged in again.

**Bit-perfect** (Start → Audio → **Bit-perfect DAC output**). With a USB DAC
connected, local lossless stereo tracks (FLAC, ALAC, WAV/AIFF and other PCM,
WavPack, TTA, APE) play at their own sample rate and bit depth, unprocessed and
unscaled, straight to the DAC: no equaliser, tempo, spatial audio, resampling
or volume. Now Playing shows what the DAC receives, e.g. "FLAC 24/96 → DAC
96 kHz". Use the DAC's or the headphones' own volume. UI sounds are muted while
a track holds the DAC; they come back after about five seconds of pause, or
when you stop. The DAC is let go at once when a game, a video or an alarm
starts. Tracks the DAC cannot take at their own rate, mono and multichannel
files, DSD, lossy files and streams play through the normal 48 kHz path, shown
as "DAC 48 kHz (mixed)".

## Music in games

**Start → Audio → Keep music playing in games** (on by default). Music that is
playing when you start a game keeps playing under it, mixed with the game's
sound on the speaker or the USB DAC (ALSA `dmix`). **Music volume in games**
(10–100 %, 60 % by default) keeps the game's own sound on top. With no music
playing at launch, the game starts exactly as before.

The music pauses instead, with a toast saying why, when:

- the sound goes to a Bluetooth headset (it plays one app at a time);
- the sound card cannot be shared on this firmware (JukaHub tries it before
  the game starts; the game's own sound never depends on it);
- the game is a 32-bit program, or brings its own audio configuration.

In a game, hold **Select + L2** for the in-game menu: the **Music** row shows the
track and **Left / Right** change track (a Bluetooth pad's D-pad works too);
**A** opens a small now-playing panel with the song and artist, **Left / Right**
previous / next, **A** play / pause and **B** back to the menu. Music can only
sound under a game that started while music was playing (the shared mix); under
any other game the panel says so instead of starting music that would fall
silent when the game resumes.

**Quiet the game while music plays** (Audio settings, on by default): music
started from that panel over a RetroArch game mutes the game's own sound
(RetroArch's MUTE command, sent once). The game gets its sound back exactly once
when the music is paused or ends. A RetroArch already muted in its own config is
left alone, and if the game ends while muted, `audio_mute_enable` is put back to
false in `retroarch.cfg` once RetroArch has quit, so later games do not start
silent.

Before this, music left playing when a game started ran through the whole
playlist in silence during the game.

## Audiobook speed

The listening speed (0.75×–2×, `[` / `]` in the player) is remembered **per
audiobook**: a book plays at the speed you last chose in it. Music, podcasts and
books you never changed use the global speed, which is also what the speed was
before this version.

## Tidy the Music folder

**Audio settings → Tidy the Music folder** (on by default). After a library
scan has read the tags, the top level of the card's Music folder is tidied in
the background (on the handheld only; a PC's Music folder is left alone):

- a song loose in `Music/` moves to `Music/Singles/` (a `.lrc` or picture of
  the same name goes with it);
- an album folder dropped straight into `Music/` — songs only, no folders
  inside, every song tagged with the same (album) artist and album — moves to
  `Music/<Artist>/<Album>/`, covers, `.cue`, `.lrc` and `.m3u` included.

Nothing is ever overwritten (a taken name is skipped and logged). Artist
folders, folders named after their artist, mixed folders and compilations,
`Audiobooks/`, files still being copied (they wait for the next scan) and songs
of the music queue are left alone. Saved
playlists and favourites follow the moved songs; `.m3u` files elsewhere are not
rewritten.

## Audio channels

**Settings → Accessibility → Audio channels**: Stereo, Mono, Mono left or Mono
right (it replaced the Mono audio switch; Mono audio on became Mono). Mono left
and right put both channels into one ear on headphones, Bluetooth or a USB DAC,
and behave as Mono on the built-in speaker. It applies live to video and music
and, from their next start, to the games JukaHub launches: their ALSA "default"
goes through a `route` plugin (`jhchannels`) in front of the card, the shared
music mix, the USB DAC or the Bluetooth headset. Stereo adds nothing. Programs
that open a Bluetooth headset through `AUDIODEV` directly (some SDL ports) and
firmwares that define their own ALSA "default" keep their stereo.

## Screen off

The player's own lock screen is **Select** (see
[CONTROLS.md](CONTROLS.md#screen-off-the-lock-screen)). On the handheld the
**power button** does the same while music plays: Settings → Power & battery →
**Power button while music plays** is **Screen off** by default (**Sleep**
sleeps and pauses the music, the old behaviour). With the screen off the
backlight is off, nothing is drawn and the CPU drops to the music profile. The
buttons follow Start → Audio → Screen-off controls, except that *Any key* counts
as *Controls* here, so a press in a pocket does not light the screen: **X**
plays or pauses, **R1 / L1** change track, **Left / Right** seek 10 s and
**Select** wakes; the volume keys keep working. The power button wakes the
screen at once. If the music stops, or stays paused, for two minutes, the
handheld goes to sleep as usual. This works while you are not in a game; in a
game the power button sleeps as before.

## Radio

The fifth shelf of **Music** is **Radio**. Its first row, **Radio directory**,
opens the free [Radio Browser](https://www.radio-browser.info) directory, about
50 000 internet radio stations, in four tabs:

- **Top** — the most voted stations;
- **Country** — the stations of the handheld's country (the one the weather's
  location found, else the Wi-Fi country); **A** on the country row picks
  another from the list;
- **Genre** — a list of genres (jazz, classical, news, lofi…), then that
  genre's stations;
- **Search** — type part of a station's name.

**A** plays the station in the music player, with the rest of the list after it
for Next / Previous; a station that does not answer is skipped for the next.
The player opens with the pad on Play, which shows a Stop mark: a station has
no place to pause at, so Play / Pause is Play / Stop on it (**A** there, **X**,
Space, the dock's Play) and Play tunes in again at the live edge. A station
cannot be seeked. While something plays, the directory's tabs and the Radio
shelf show it on a **Now playing** row on top: **A** stops it (or tunes in
again), **X** opens the player; **A** on the playing station's own row, which
says Playing or Stopped, does the same.
**X** keeps the station in your stations (again to take it out). **Y** reloads
the list (on Search it types a new name). **L1 / R1** or **Left / Right**
switch tabs, **B** steps back from a genre or the country list, then leaves.

Your stations are the IPStream library (`ipstreams.json`), in its **Radio**
group: they are listed under the directory row on the Radio shelf, show up in
the IPStream panel's Radio tab, and the idle screen's internet radio starts with
a few of them. The last lists you opened are kept in `radiodir.json` in the data folder,
so the directory still shows them offline.

Only `https` requests go to `*.api.radio-browser.info` (de1 first, then the
servers listed under `all.api.radio-browser.info`), with a User-Agent that
names JukaHub; at most 200 stations a list. Station names are cut to one line
and only http(s) stream links are kept (a `.pls` / `.m3u` playlist link is
dropped: ffmpeg cannot follow it).

## Game soundtracks

Game music files play in the same player: NES (NSF, NSFE), SNES (SPC), Game
Boy (GBS), Sega (VGM, VGZ, GYM), PC Engine (HES), MSX (KSS), ZX Spectrum (AY),
Atari 8-bit (SAP) and Commodore 64 (SID) tunes, and tracker modules (MOD, XM,
S3M, IT). A file that holds a whole soundtrack opens as an album with one row
per song; Now Playing shows the console, "Song 3 of 24" and the game, and the
artwork tile shows the game's cover from the Games library or the folder's
picture. Looping songs play for the length the file gives (else 2:30) and fade
out. The console formats go through ffmpeg's libgme and the modules through
libopenmpt; an ffmpeg without them says so once. SID tunes play on JukaHub's
own small C64 emulation, cheap enough to run with the screen off. Formats,
limits and the SID details are in [SOUNDTRACKS.md](SOUNDTRACKS.md).

## Compared with UIPod

[UIPod](https://famiclone.dev/UIPod/) is an iPod-style player for the TrimUI
Brick (NextUI) and Android handhelds. What it has and where JukaHub has it:

| UIPod | JukaHub |
|-------|---------|
| iPod Mini-style UI, 38 four-shade themes, 6 transitions, dithered album art, click-wheel sound | The iPod layout (above) |
| Local music, podcasts, audiobooks (chapters, resume), videos | Music, Podcasts, Audiobooks and Videos menus over the media library |
| Navidrome: streaming, caching, scrobbling, Download offline | Navidrome source, the song cache, Download Offline, scrobbling ([MUSIC_SERVERS.md](MUSIC_SERVERS.md)) |
| Jellyfin: movies, series, music; direct stream or server transcode; resume sync; download | Jellyfin source: the same, with Quick Connect sign-in |
| One deduplicated library across the three | Source → All sources |
| Last.fm QR sign-in, offline scrobble cache | Last.fm (QR), ListenBrainz, the servers' own counts; `scrobbles.json` waits for a connection |
| 10-band EQ, offline AutoEQ database | 31-band PEQ with the iPod presets; AutoEQ with the kept index |
| Speed 1/16×–1.5×, Walkman Tape Mode, reverb, ping-pong delay, chorus, flanger | Sound effects (above), speed 1/16×–2× |
| 10 Bayer-dithered visualizers on a 1024-point FFT | The same ten (above) |
| File manager: delete, rename, copy, move, info | Browse Files (the Files screen) and the context menu's Rename, Delete and Info |
| Video subtitles and audio track switching | The video player's subtitle and audio track pickers |
| USB file transfer while running | Settings → Network → USB file transfer (MTP) |
| English and Ukrainian | Ukrainian is a JukaHub language (menus and the player; long help texts stay English for now) |
| Plugins: Music Quiz, Sound Racer, Tetris, Snake, Brick Breaker, 2048, Stopwatch, LCD Plasma, Text Viewer | Extras › Games, the Stopwatch app, LCD Plasma among the visualizers, the text viewer in Files; JukaHub's own plugins (docs/PLUGINS.md) instead of `.uipod` files |
| Android app, browser simulator | Not applicable: JukaHub runs on the handheld and on Windows |

## Compared with NXT Retro Music

NXT is a dedicated Brick-family player with a much larger surface. The table is
about capability, not code: nothing here is copied from it.

| Area | NXT | JukaHub today | Gap worth closing |
|------|-----|---------------|-------------------|
| Browsing | All / folders / artists / albums, search, sort, saved index | Album, artist and audiobook shelves over the saved Media index; folder playback from Files | All-tracks and folder views remain |
| Queue | Add, play next, reorder | Play next + count in the status line | Medium — reordering and a queue view |
| Favorites | Toggle, list, use as a playlist | Toggle + row marker | Medium — a favorites view |
| Playlists | Create, rename, reorder, delete, M3U/M3U8 import/export | Named lists saved as JSON, played from Audio settings | M3U8 import/export, rename and reorder |
| Modes | Shuffle, repeat off/one/all | Same | Done |
| Transitions | Gapless, crossfade with duration | Gapless (the next decoder starts while the last second still plays) | Low — crossfade |
| Lyrics | LRC, bilingual translation, word timing, offset, full screen | Line-level LRC in the artwork tile; lyrics inside the file | Medium — offset and a full-screen view are local; translation needs a provider |
| EQ / DSP | 12 presets, 10/15/31-band graphic, 6 parametric filters, limiter | none | **High** — a 5-band biquad in the PCM path is contained and testable |
| USB DAC / DSD | Bit-perfect PCM, Native DSD, DoP | USB DAC output, bit-perfect PCM for lossless stereo; DSF/DFF decode to PCM | Native DSD/DoP output |
| Online | Spotify Premium, SoundCloud, Drive, radio, podcasts | YouTube through yt-dlp, IP radio streams, the Radio Browser directory, podcasts | Medium — one provider at a time |
| Personalization | 51 themes, 60 fonts, wallpaper, live editor | App-wide themes (25), player follows the theme | Low priority; the app already has a theme system |
| Spectrum / LED | 21 palettes, 5 styles, reactive LEDs | Fixed bars, two layouts | Low — palettes are easy, LED writes are ROM-specific |
| Profiles / stats | 16 listeners, listening time, top artists | none | Medium — a single local profile with a small history file |
| Sleep / screen-off | Timer + mapped controls while dark | Timer, Select or the power button darkens with mapped controls | Done |
| Transfer / updates | Wi-Fi transfer, OTA | FTP/SFTP, phone companion, signed OTA | Done |

## Roadmap

Work is ordered by value per day, and each phase is independently shippable.

1. **Library browsing** — album, artist and audiobook shelves use the saved
   Media index. Folder and all-tracks views, library search and natural sort
   remain possible next additions.
2. **Playlists and favorites view** — named playlists in one JSON file are done
   (**Playlists** in Audio settings; **X** saves the queue under a name). What
   remains is M3U8 import/export (the format every other player already writes)
   and a favorites filter on the current list.
3. **EQ** — a five-band biquad equalizer applied to the PCM blocks the player
   already mixes before queueing them, with presets and an on/off that is
   remembered. Keep a "bypass" path so a bug in the filter cannot silence
   playback.
4. **Lyrics** — full-screen view, per-file offset, and a translation slot
   behind an optional provider key (never enabled by default).
5. **Listening history** — plays, time, top artists, resumable "continue
   listening" over the library index.
6. **Spectrum palettes** — a handful of styles on the existing bar data.

Deliberately out of scope: native DSD/DoP USB DAC output (DSD is decoded to PCM),
Spotify (needs Premium plus a Web API app), and ROM-specific LED control.

## Notes on provenance

The features above are standard player capabilities. No code, theme, font,
asset, icon, layout or user-facing string from NXT Retro Music is used here;
the comparisons exist to decide what to build next.
