# Time-lapse

Every game launch can leave a short film behind: one still every twenty seconds
of play, assembled into an MP4 when you quit the game. Recording needs no
emulator cooperation. The framebuffer path is available on the stock Smart Pro;
capturing actual game frames still needs a hands-on check for each emulator.

## Where the films go

```text
<data folder>/timelapses/timelapse_<game>_<date>-<time>.mp4
```

On the handheld that is the JukaHub data folder next to the app; on a desktop
build it is the same data folder the recents and save snapshots use. The name
comes from the game title, so `Chrono Trigger` becomes
`timelapse_chrono-trigger_20261022-160509.mp4`.

Frames are captured into a dot-prefixed session folder and removed once the film
exists, so a broken session never looks like a finished time-lapse. If the
encoder fails, the frames are kept and the toast says so — that folder is the
only copy, and a later build can assemble it.

## How it records

The emulator owns the panel while it runs, and JukaHub is only waiting for the
process to exit, so the film cannot come from JukaHub's own renderer. Each grab
is a single `ffmpeg -f fbdev -i /dev/fb0` JPEG frame, downscaled to 640 px
wide. The same ffmpeg assembles the frames at 24 fps into an MPEG-4 Part 2 MP4
with `+faststart`. Stock Smart Pro firmware 1.1.0 has the MJPEG and MPEG-4
encoders. It has no PNG or libx264 encoder; its H.264 V4L2 wrapper has no
`/dev/video*` device to use. The app uses an installed ffmpeg, including the
firmware's `/usr/bin/ffmpeg` when no copy is bundled.

| Limit | Value | Why |
|---|---|---|
| Interval | 20 s (configurable 3 s – 5 min) | Cheap enough to run for an hour on the handheld's CPU |
| Still size | 640 px wide | ~25 KB per frame instead of a full screenshot |
| Frames per film | 240 | 80 minutes of play, ~6 MB, 10 seconds of video |
| Minimum film | 4 frames | A game opened and closed in under a minute leaves nothing |
| Grab timeout | 15 s | A wedged capture can never hold the session open |

The first still is taken immediately, so even a short session gets the title
screen, and the film stops capturing at the frame cap while the game keeps
running — you get one complete film instead of several pieces.

## Settings

Both are plain custom keys in `jukaconfig.json`:

| Key | Default | Meaning |
|---|---|---|
| `timelapse` | `true` | Record a film for each game launch |
| `timelapse_interval` | `20` | Seconds between stills; clamped to 3 – 300 |

```json
"variables": {
  "custom": {
    "timelapse": true,
    "timelapse_interval": 30
  }
}
```

Recording is on by default and uses a little CPU and card space during play.
The feature switches itself off where it cannot work. It records
the panel, not the emulator, so it also captures a launcher screen or a crash.

## When it does not record

- **Desktop builds.** There is no `/dev/fb0`; the recorder logs
  `[timelapse] not recording: …` once at launch and the launch is unchanged.
- **A firmware without a readable framebuffer.** Same path: one log line, no
  ticker, no files.
- **ffmpeg missing.** The encoder is resolved without downloading, so a launch
  never waits on the network; a missing ffmpeg means no capture and no film.

Nothing is written outside `<data folder>/timelapses/`.

## Troubleshooting

| Symptom | Check |
|---|---|
| No folder after a session | The game ran under a minute (under four frames), or `/dev/fb0` is unreadable. `grep timelapse jukahub.log` says which. |
| Black or striped frames | The panel is drawn by a compositor that does not expose the framebuffer. Nothing to fix from the hub; the emulator's own recording is the fallback. |
| Film shows a launcher or black frame | Check whether `/dev/fb0` contains that emulator's game picture on this firmware. |
| Encode toast | Check `ffmpeg` and its MJPEG and MPEG-4 encoders. The frames stay in the dot-folder next to where the film would have been. |
