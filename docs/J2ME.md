# J2ME phone games

Before smartphones, most phones ran small Java games: J2ME (Java ME) "MIDlets",
one `.jar` file each (some Japanese phones used `.kjx`). JukaHub lists them in
**Games** as a console of their own, **J2ME**, and plays them in RetroArch with
the [FreeJ2ME-Plus](https://github.com/TASEmulators/freej2me-plus) core.

## Where the games go

Put the `.jar` (or `.kjx`) files in **`Roms/J2ME`** on the card (on a PC, the
`Roms/J2ME` folder beside JukaHub). Folders named `j2me`, `javame`, `Java ME` or
`Mobile - J2ME` work too, and so does a sub-folder inside one of them.

A `.jar` counts as a game only in a J2ME folder. Anywhere else a `.jar` is a
Java program (a PortMaster port's, Minecraft's, a tool you downloaded), so
JukaHub never lists those as phone games.

Opened from **Files**, a phone game outside a J2ME folder still plays where it
is; the message then says to move it into `Roms/J2ME` so Games lists it.

## The first start

A J2ME game needs three things: Java, the FreeJ2ME-Plus core, and the core's
jar (the Java half of the emulator, which goes in RetroArch's system folder).
The first time you start a J2ME game, JukaHub sets up what is missing, in this
order:

1. **Java.** With no Java on the device, **Tools → Java** opens and offers to
   install it (Eclipse Temurin, see the Java page there). The game goes on
   starting once it is installed. Any Java 8 or newer works.
2. **The core and its jar** (handheld only, a few MB, once). JukaHub downloads
   its own aarch64 build of the pair from its release
   [`cores-aarch64`](https://github.com/jukaLang/JukaHubV2/releases/tag/cores-aarch64),
   checks both against the checksums in that release's index, puts the core in
   RetroArch's cores folder and the jar in RetroArch's system folder (the one
   RetroArch's config names), and records the pair in JukaHub's data folder
   (`j2me/install.json`). The install card shows the progress.
3. **The game** then starts by itself, unless you started something else
   meanwhile (then the message says it is ready; start it again).

After that a start is immediate. JukaHub checks the pair at every start
without the internet; when either half was changed or removed, the next start
installs the pair again. A FreeJ2ME core and jar you put there yourself are
used as they are.

Offline, the first start says to connect to the internet first. Until the
`cores-aarch64` release has the J2ME core (the workflow below builds it), step
2 stops with "JukaHub has not published its J2ME core for this device yet" and
nothing of the core or the jar is installed (Java, once installed, stays).

## Controls

FreeJ2ME-Plus maps the phone's keys onto the pad. The defaults (they can
change between core versions; RetroArch's **Quick Menu → Controls** shows the
ones in use):

| Pad | Phone |
|-----|-------|
| D-pad | Arrow keys (also 2, 4, 6, 8 in most games) |
| A | OK / Fire (the middle key) |
| Select | Left soft key (often "Menu" or "OK") |
| Start | Right soft key (often "Back") |
| B, X, Y, L1, R1, L2, R2 | The other number keys and `*` / `#` |
| Left stick | 2, 4, 6, 8 |

Games written for one phone maker may expect its layout: RetroArch's **Quick
Menu → Core Options → Phone Key Layout** picks another maker's (Motorola,
Siemens, LG, Sagem and others), and **Quick Menu → Controls** shows and changes
every button. Leave a game the way you leave any RetroArch game (RetroArch's menu →
Quit, or JukaHub's in-game menu).

## What works and what does not

- Most 2D games play well. The screen is the phone's (240×320 by default; the
  core options change it).
- 3D games (M3G, Mascot Capsule) work only in part, and many do not start.
- Games that need a touch screen or a camera are mostly not playable.
- FreeJ2ME keeps a game's progress itself, the way the phone did. Do not count
  on RetroArch save states with this core.
- A JVM starts with every game: the first seconds of a start are slower than
  for a cartridge console.
- The sound is played by Java itself, not by RetroArch, so RetroArch's audio
  settings do not change it.

## On a PC

JukaHub does not download the core on Windows or a Linux desktop. Get the
FreeJ2ME-Plus libretro core and its jar from the
[FreeJ2ME-Plus releases](https://github.com/TASEmulators/freej2me-plus/releases)
(the libretro zip has the core for Windows and Linux; the base zip has the
`-lr.jar`). Use a core and a jar from the same download:

- the core (`freej2me_plus_libretro.dll`, or `freej2me_libretro.dll` from an
  older build) into the `cores` folder of the RetroArch JukaHub uses;
- the `-lr.jar` into that RetroArch's `system` folder.

Java comes from **Tools → Java** as on the handheld. A RetroArch whose system
folder path has a space in it (such as `C:\Program Files\RetroArch`) can stop
FreeJ2ME from finding its jar: a RetroArch in a folder without spaces avoids it.
Until the core is there, starting a J2ME game says what is missing.

## When a game does not start

- **"J2ME games need Java …"**: install Java in **Tools → Java**.
- **"JukaHub is still looking for Java …"**: right after power-on the first
  look for Java may still be running; start the game again a moment later.
- **"JukaHub has not published its J2ME core for this device yet …"**: the
  `cores-aarch64` release has no FreeJ2ME-Plus build yet; it comes with the
  next run of the cores workflow.
- **"… its jar is not …"**: the core is there but its jar is not in RetroArch's
  system folder; the message names the folder.
- **"RetroArch reads firmware from each game's own folder …"**: RetroArch's
  System/BIOS folder is set to "default"; set a folder in RetroArch → Settings →
  Directory.
- A game that quits at once gets the usual diagnosis card with RetroArch's log.

## How the handheld build is made

Nobody publishes an aarch64 build of FreeJ2ME-Plus, so JukaHub's
[RetroArch cores workflow](https://github.com/jukaLang/JukaHubV2/blob/main/.github/workflows/retroarch-cores.yml) builds it
with the other cores (`player/native/build-cores.sh`): the C bridge in
`src/libretro` in an Ubuntu 20.04 arm64 root (glibc 2.31, so the stock firmware
loads it), and the jar from the same commit with JDK 8 and Ant, checked to
need nothing newer than Java 8. Both are published in the `cores-aarch64`
release. The core has the usual index line (`<date> <crc32> <core zip>`); the
jar has a line of its own, `<date> <sha256> <jar> <core>`, and the handheld
installs a core and a jar of the same date only. The workflow runs weekly, on
a push to `main` that changes it or `build-cores.sh`, and by hand (Actions →
"RetroArch cores for TrimUI" → Run workflow; `freej2me_plus` in the cores box
builds only this one). The code is `player/j2me.go` and
`player/j2me_install.go`.
