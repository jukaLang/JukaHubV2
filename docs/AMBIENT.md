# Dock & ambient mode

When the handheld is plugged in — or simply left alone — JukaHub can turn it
into a dock display instead of leaving the launcher on screen: a large clock,
today's weather, the track that is playing, and a slideshow of your photos.

Open it from **Start → Ambient**, or let it appear by itself:

| Situation | When the screen appears |
|-----------|-------------------------|
| Charging | after 45 seconds without input |
| On battery | after the normal idle timeout (`screensaverTimeout`, 25 seconds by default) |

Any input wakes a darkened panel. **B** (or Escape) leaves the screen; the ambient
screen never opens over a modal, a game or a full-screen video.

## What it shows

- **Clock and date** in the top-left, plus the next alarm when one is enabled.
- **Weather** in the top-right. It reads the same forecast as the header chip
  but does not depend on it: a minimal top bar can still show weather on the
  dock, and `ambientWeather: false` hides it here. Until the first fetch answers
  it reads "Weather — loading…".
- **Now playing** in the bottom-left: track name, play/pause state, position and
  a progress bar. With nothing loaded it says "Nothing playing" and **A** starts
  the music folder.
- **Battery**, charging state, the sleep-timer countdown and the photo counter in
  the bottom-right.
- **New chat messages** in a card above the now-playing panel, the same card the
  starfield screensaver shows.
- **Photos** behind it all, fading from one to the next every 20 seconds.

## Photos

The slideshow reads the first folder that exists from:

1. `ambientPhotos` in `jukauser.json` (or `variables.custom`), if set;
2. `/mnt/SDCARD/Photos`;
3. `/mnt/SDCARD/Pictures`;
4. `~/Pictures`, then `~/Pictures/JukaHub`.

PNG, JPEG, BMP and — when the runtime's SDL_image can decode it — WebP are
listed. Subfolders and hidden files are skipped. The folder is rescanned every
five minutes, and every 30 seconds while it is empty, so copying a photo onto
the card shows up without a restart. A file that fails to decode is skipped
rather than retried on every frame.

Photos are drawn cover-fit over the screen with a dark scrim, so portraits do
not letterbox and bright pictures do not wash out the text. With no photos the
screen falls back to the theme gradient — the clock, weather and now-playing
cards still work.

## Controls

| Button | Action |
|--------|--------|
| A / Enter / Space | Play / pause the current track (starts the music folder when idle) |
| Left / Right (L1 / R1) | Previous / next track |
| Up / Down | Volume up / down |
| Y | Cycle the sleep timer: off → 15 → 30 → 60 → 90 minutes → off |
| X | Screen off — audio keeps playing |
| N | Next photo |
| Any key / tap | Wake a darked panel |
| B / Start / Escape | Leave ambient mode |

Every action is also a chip along the bottom of the screen, so a touchscreen or
mouse can do the same thing without a controller. The pad in the Phone
Companion web UI presses the same buttons, including waking a darked panel.

## Sleep timer and screen-off audio

The sleep timer is the one you want at night: when it fires it **stops
playback** and blanks the panel. It is cancelled by leaving ambient mode, so an
armed timer cannot black out the screen while you are using the device.

**X** is different: it blanks the panel immediately and *keeps the music
playing*. In that state the render loop draws nothing at all — it sleeps in
200 ms steps and only keeps the alarm check running — so the panel is dark and
the only CPU work left is audio decoding. Any key, controller press or tap
brings the screen back. An alarm firing wakes the screen too, since a darked
ambient display is exactly how the device gets used overnight.

## Settings

Ambient mode is on by default and replaces the starfield screensaver as the idle
screen. Both keys live in `jukauser.json` (or `variables.custom` in the
config):

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `ambientMode` | bool | `true` | `false` restores the classic screensaver and disables auto-entry |
| `ambientWeather` | bool | `true` | show the forecast on the dock (independent of the header chip) |
| `ambientPhotos` | string | detected | folder for the slideshow; an absolute path is best |

```json
{
  "variables": {
    "custom": {
      "ambientMode": true,
      "ambientWeather": true,
      "ambientPhotos": "/mnt/SDCARD/Photos/Dock"
    }
  }
}
```

With `ambientMode: false` the starfield screensaver is the idle screen and its
`screensaverTimeout` applies; the dock still opens by hand from Start → Ambient.
The timeout can be changed in Settings → General → **Screensaver after** or from
Quick Settings, in steps from 15 seconds up (25 by default). Start → Ambient still opens the dock screen manually in that case.

## Related

- [Controls](CONTROLS.md) — the rest of the device, screen by screen
- [Configuration](CONFIGURATION.md) — where these keys live
- [Music player](CONTROLS.md#music-player) — the playback the screen controls
- [Alarm](CONTROLS.md#alarm) — alarms that wake the darkened panel
