# Recordings

The emulator can already record: RetroArch's own **Recording** toggle writes a
clip to the folder its `recording_output_directory` names. What was missing was
the other half — finding that clip again on a device with no file manager in
reach. **Recordings** lists the captures and plays them in the hub's own video
player, so a clip is one press from the screen it was recorded on.

Open it from **Apps → Recordings**. It is a built-in applet rather than a
button on the Media screen because that row is already full: the IPStream chip
is injected into it at runtime and needs the last slot.

## Where the folder is

JukaHub and RetroArch have to agree on one folder, so before a RetroArch launch
the hub sets `recording_output_directory` in `retroarch.cfg`. The order is:

1. `recordingsDir` in `jukaconfig.json` (or `jukauser.json`), absolute or
   relative to the card. This always wins.
2. A folder `retroarch.cfg` already names — read, honoured, and never rewritten.
   `:captures` (next to the config) and `~/…` are resolved the way RetroArch
   resolves them.
3. Otherwise `Recordings` on the card, created on the next RetroArch launch.

Nothing else is managed. The hotkey that starts a recording stays whatever
RetroArch and the firmware set it to: the binds are numeric button codes, and
writing a guess would break the controls on any device whose mapping differs
from the guess. The hub only decides where the file lands.

The edit uses the same crash-safe writer as the save-state switches: one
`key = value` line changes, the original is kept once as
`retroarch.cfg.jukahub-backup`, and the file is replaced through a temporary
file so a power loss cannot truncate it. `recordings: false` stops the hub from
touching `retroarch.cfg` at all — the screen still lists whatever is in the
folder.

## Recording something

Recording is RetroArch's feature, not the hub's:

1. Launch a game from the Game Library or Continue.
2. Open RetroArch's Quick Menu and use its **Recording** entry, or the hotkey the
   firmware binds for it.
3. Leave the game. The clip is written when RetroArch closes the file, so it
   appears in **Recordings** after the emulator exits.

Standalone emulators (mGBA, PPSSPP, DraStic) are untouched: they write captures
their own way, and the hub does not rewrite their configs. If one of them
already writes into the same folder, its clips show up too.

## Controls

| Input | Action |
|-------|--------|
| D-pad Up / Down | Walk the list |
| A / Enter | Play the highlighted capture |
| Touch / mouse | Tap a row to play it |
| **Rescan** button | Re-read the folder (a capture finished while the screen was open) |
| **Media** button, B | Leave the screen |

Clips play in the embedded player, so the normal video controls apply: Left /
Right move along the control bar, Up / Down change the volume, L1 / R1 skip ten
seconds, B closes full screen and then the player.

## What it cannot do

- **No in-game overlay.** The emulator owns the framebuffer and the hub is
  blocked until it exits, so a hub-drawn "REC" badge or menu over a running game
  would never appear. RetroArch's own OSD is the honest answer, and its overlay
  art is a separate, per-device job.
- **No trimming, deleting or renaming.** The screen lists and plays; editing a
  capture is a job for a PC, and a mis-typed delete on a handheld is not
  recoverable.
- **No thumbnails.** A capture is decoded only when it is played, so a card full
  of 4 GB clips costs nothing to browse.
- **No capture started from the hub.** The record toggle lives inside the
  emulator's menu; starting one is that menu's job.
