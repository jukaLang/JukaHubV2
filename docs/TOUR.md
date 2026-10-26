# The ten-minute tour

What JukaHub feels like from a cold SD card to a handheld you do not want to
put down. Every step is controller-only; nothing here can break anything —
every screen is reachable with the D-pad, and leaving is one button hold.

This is a story, not a reference. The exact button tables live in
[CONTROLS.md](CONTROLS.md); the troubleshooting table lives in
[README.md](../README.md#something-is-wrong).

## Minute 1 — install

Download `JukaHub-Trimui.zip` from
[Releases](https://github.com/jukaLang/JukaHubV2/releases/latest), drop the SD
card in your computer, extract the zip into `Apps/JukaHub` on the card, put the
card back and power on. Your firmware, your themes, your other apps: untouched.
JukaHub is one folder; delete the folder and it is exactly as if it never
existed.

Open **Apps → JukaHub**. On first launch it fetches its two helper tools
(about a minute, once ever) and lands on the home screen: a grid of tiles, a
clock and weather in the bar, button hints in the footer.

## Minute 2 — YouTube with thumbs

**Media → Tube**, hold Select (the on-screen keyboard appears), and — while the
D-pad is slow — scan the QR that **Tools → Phone companion** shows next time:
your phone becomes a full keyboard over the same Wi-Fi, and typing a search is
typing. Pick a video; it plays at the resolution you set, and pressing **B**
puts it aside without losing your place. Save it to **Favorites** with **X**
and it is one press away forever.

## Minute 3 — your library, finally a library

**Emulators → Games Library** reads `Roms/` from the card console by console.
Titles get box art, play time is tracked, and launching a game hands the screen
to the emulator with everything configured. When you quit a game, its saves and
its progress land in the **Continue** row at the top of home — press **A** on
it next session and you are back exactly where you left off, emulator state and
all.

## Minute 4 — the parts you did not expect

Three things people do not expect a "media hub" to do, all built in:

* **PS Remote Play** — the PS4/PS5 panel pairs over your LAN in two minutes and
  streams through JukaHub without Python or a separate Chiaki install.
* **LAN play** — **X** on a game hosts or joins RetroArch netplay, so link-cable
  classics run over Wi-Fi.
* **Save that never dies** — **Start → Save Manager** snapshots every emulator
  save folder; restoring one snapshots your current saves first, so even a
  restore is undoable.

## Minute 5 — leave the room, keep the device

Hold Select + Start to quit; playtime and the Continue row are kept, and the
stock launcher is back as if nothing happened.

Power profiles from **Play options → Game Profiles** give a game a temporary
boost and cool the device down everywhere else; **Settings → playback
resolution** drops stutter on bad nights; battery saver throttles itself below
20 % until it is plugged in again.

## Minute 6-10 — own it

Everything from the first five minutes is one app on a card you can edit from
your PC: **Tools → FTP** opens the SD card over Wi-Fi, **Remote Cast** puts the
device's screen in your browser, and **Tools → Community & support** opens a
prefilled, redacted bug report if anything misbehaves.

Two more Tools rows are worth knowing on day one. **Tools → Toolbox** installs
standalone helpers — rclone, jq, ripgrep, fd, pandoc, age — from their official
releases and runs them with visible output, so a card-level job never waits for
a PC; the checksum-backed download and the per-tool arguments are described in
[TOOLBOX.md](TOOLBOX.md). **Tools → Security & trust** is a read-only page saying
what this build protects — encrypted secrets, the storage key in use, package
state, open LAN servers, the browser session — with [SECURITY.md](../SECURITY.md)
as the long form. The tour names both on its last step, and the controller
legend (Quick Settings → **?**) repeats them under **Find it in Tools**. Updates arrive as an
icon in the header, install with one press, and — if a build ever misbehaves at
startup — roll back to the previous one by themselves, so the card that worked
yesterday always comes back up.

Then make it yours: **Start → Appearance** has 25 themes, text scaling and the
colour-blind palettes, and the [web builder](https://generate.jukalang.com)
turns a layout idea into `jukaconfig.json` you drop next to the app.

## What to do first if this does not match reality

The tour above describes behaviour as shipped; if a step does not work on your
card, the fix is nearly always Wi-Fi or a missing helper tool — the table in
[README.md](../README.md#something-is-wrong) maps symptoms to one-liners, and
**Tools → Logs** sends itself as a file you can paste into
[Discord](https://discord.gg/R9qgJjh5jG).
