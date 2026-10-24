# Music player

JukaHub's audio player is the screen **Files → a song**, the **Music** entry in
the Start menu (**Scenes → Music**), or the `MusicPlayer` scene. It plays the
folder the file came from, keeps playing while you browse the rest of the app,
and stores its own settings next to the other app data.

This page describes what it does, how it compares with the handheld music
players people know (NXT Retro Music in particular), and the order in which the
remaining gaps are worth closing.

## What ships today

| Area | Behaviour |
|------|-----------|
| Formats | MP3, WAV, OGG, Opus, M4A/AAC, FLAC, WMA, decoded through the bundled ffmpeg |
| Library | The folder of the file you opened, sorted by name; Up/Down to browse, A to play |
| Transport | Play/pause, next/previous, stop, seek ±10 s, seek by tapping the rail, volume ±10 % |
| Modes | Shuffle (every track once before wrapping), repeat off / all / one, a play-next queue |
| Favorites | Toggled per track, marked in the list, stored in `musicfavorites.json` |
| Long form | 0.75×–2× speed, sleep timer (15/30/60 min, exits playback at zero), resume for files of 8 minutes or more |
| Lyrics | A `.lrc` file next to the track; the active line and its neighbours are drawn in the artwork square |
| Visuals | 32-bar smoothed spectrum in the artwork tile and a full-screen visualizer |
| Background | Audio keeps playing when you leave the screen; starting a video or a Watch Party stream stops it |

Controls are in [CONTROLS.md](CONTROLS.md#music-player).

## Compared with NXT Retro Music

NXT is a dedicated Brick-family player with a much larger surface. The table is
about capability, not code: nothing here is copied from it.

| Area | NXT | JukaHub today | Gap worth closing |
|------|-----|---------------|-------------------|
| Browsing | All / folders / artists / albums, search, sort, saved index | One folder at a time | **High** — a library index with metadata is the biggest missing piece |
| Queue | Add, play next, reorder | Play next + count in the status line | Medium — reordering and a queue view |
| Favorites | Toggle, list, use as a playlist | Toggle + row marker | Medium — a favorites view |
| Playlists | Create, rename, reorder, delete, M3U/M3U8 import/export | none | **High** — M3U8 read/write is cheap and portable |
| Modes | Shuffle, repeat off/one/all | Same | Done |
| Transitions | Gapless, crossfade with duration | Track-by-track decoder start | Low — gapless needs a decoder that keeps the PCM stream open |
| Lyrics | LRC, bilingual translation, word timing, offset, full screen | Line-level LRC in the artwork tile | Medium — offset and a full-screen view are local; translation needs a provider |
| EQ / DSP | 12 presets, 10/15/31-band graphic, 6 parametric filters, limiter | none | **High** — a 5-band biquad in the PCM path is contained and testable |
| USB DAC / DSD | Bit-perfect PCM, Native DSD, DoP | SDL PCM only | Out of scope unless a DAC is a target |
| Online | Spotify Premium, SoundCloud, Drive, radio, podcasts | YouTube through yt-dlp, IP radio streams | Medium — one provider at a time |
| Personalization | 51 themes, 60 fonts, wallpaper, live editor | App-wide themes (25), player follows the theme | Low priority; the app already has a theme system |
| Spectrum / LED | 21 palettes, 5 styles, reactive LEDs | Fixed bars, two layouts | Low — palettes are easy, LED writes are ROM-specific |
| Profiles / stats | 16 listeners, listening time, top artists | none | Medium — a single local profile with a small history file |
| Sleep / screen-off | Timer + mapped controls while dark | Timer | Low |
| Transfer / updates | Wi-Fi transfer, OTA | FTP/SFTP, phone companion, signed OTA | Done |

## Roadmap

Work is ordered by value per day, and each phase is independently shippable.

1. **Library index** — scan a music root once, cache path, size, mtime, title,
   artist, album and duration; incremental refresh; browse by folder, artist,
   album and all tracks; search and natural sort. The cache belongs in the app
   data folder, not in the config.
2. **Playlists and favorites view** — M3U8 import/export (the format every
   other player already writes), named playlists in one JSON file, and a
   favorites filter on the current list.
3. **EQ** — a five-band biquad equalizer applied to the PCM blocks the player
   already mixes before queueing them, with presets and an on/off that is
   remembered. Keep a "bypass" path so a bug in the filter cannot silence
   playback.
4. **Lyrics** — full-screen view, per-file offset, and a translation slot
   behind an optional provider key (never enabled by default).
5. **Listening history** — plays, time, top artists, resumable "continue
   listening" over the library index.
6. **Spectrum palettes** — a handful of styles on the existing bar data.

Deliberately out of scope: DSD/USB DAC output (the player mixes through SDL),
Spotify (needs Premium plus a Web API app), and ROM-specific LED control.

## Notes on provenance

The features above are standard player capabilities. No code, theme, font,
asset, icon, layout or user-facing string from NXT Retro Music is used here;
the comparisons exist to decide what to build next.
