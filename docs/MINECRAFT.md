# Minecraft

**Start → Games → Minecraft** (or search "minecraft") gathers every way to play
Minecraft on the machine in hand. JukaHub never downloads the game itself, never
signs in for you and never pretends a purchase or licence happened: each route
uses either your own copy or a free game.

## On the handheld

| Row | What it is | What A does |
|-----|------------|-------------|
| **Minecraft: Java Edition** | PortMaster's Minecraft port (Java 17 + WestonPack, installed by PortMaster) running **your own** game files | Get PortMaster → Install → *Needs game files* (how to bring them) → Play |
| **Copy *version* from *PC*** | One row per version in a paired PC's `.minecraft` | Copies that version onto the handheld |
| **Minecraft: Bedrock Edition** | Your own arm64 APK through AndroidRT ([ANDROID-MINECRAFT.md](ANDROID-MINECRAFT.md)) | Opens the APK Store, or explains how to get the APK |
| **ClassiCube** | Free Minecraft Classic client (PortMaster port) | Install → Play |
| **Luanti · Mineclonia** | Free Minecraft-like survival game (PortMaster port) | Install → Play |

**Y** on a port row shows what it is, where it lives on the card and the port's
own README. **X** looks again (a port PortMaster just installed turns into
**Play** on its own once you come back from PortMaster).

### How a port gets installed

PortMaster stays in charge of ports. When you press **Install**, JukaHub reads
PortMaster's own catalogue (`ports.json` from the
[PortMaster-New releases](https://github.com/PortsMaster/PortMaster-New/releases)),
downloads the port's zip, checks it against the MD5 the catalogue lists, puts it
in PortMaster's `autoinstall` folder and opens PortMaster, which installs it with
the runtimes it declares. Close PortMaster to come back. If you left the screen
while it downloaded, PortMaster does not pop up over what you are doing: the row
says **Open PortMaster** until it has installed the port. If the catalogue
cannot be read, or the port is not in it under its own name, PortMaster opens
and the toast says what to search for. One Minecraft transfer runs at a time,
and none starts while another download holds the install card.

Without PortMaster, the first press installs it (about 24 MB), the same as
**Emulators → PortMaster Ports**.

### Java Edition game files

The port is the engine; the game is your copy — the `versions/`, `libraries/`
and `assets/` that the official Minecraft Launcher (or Prism Launcher) downloads
on a PC where you own the game. PortMaster's port runs 1.7.10 and 1.12.2 best
(its start files, `*.start` in the port's folder, list what it knows; the copy
rows say whether a version has one).

**In one press, from a PC with JukaHub:**

1. On the PC, start the version once in the launcher, so its files are there.
2. Pair the PC and the handheld (**Settings → Network → Paired devices**) and
   keep **Nearby devices** on on both.
3. On the handheld, open Minecraft: every version on the PC shows as
   **Copy *version* from *PC***. A, then **Copy**.

The handheld asks the PC for the version's file list (its json and jar, the
version it is built on for Forge/Fabric, the libraries for Linux, the asset index
and its objects), checks free space, then copies each file into the port's
`.minecraft`, verifying every SHA-1 the launcher's json lists. A copy that stops
resumes: complete files are kept. Progress is in the status strip.

Only paired devices can ask, and the PC serves nothing outside `versions/`,
`libraries/` and `assets/`: your account (`launcher_accounts.json`), worlds and
settings never leave it.

**By hand:** copy `versions/<version>`, `libraries/` and `assets/` from
`%APPDATA%\.minecraft` (Windows) or `~/.minecraft` (Linux) into the port's
`.minecraft` folder (shown under **Y**) with the Files screen, FTP or the card.

### Limits

- The Java port needs two analog sticks: PortMaster lists it for the Smart Pro,
  not the Brick.
- 1 GB of RAM is tight for Java Edition; keep render distance low.
- Bedrock is experimental and has never been run on a handheld
  ([ANDROID-MINECRAFT.md](ANDROID-MINECRAFT.md)).

## On a PC

The screen lists the launchers it finds — **Minecraft Launcher** (installer or
Microsoft Store), **Minecraft for Windows** (Bedrock, via its `minecraft:` link)
and **Prism Launcher** — and A starts one. On Linux it looks for
`minecraft-launcher`, `prismlauncher` and their Flatpaks. With none, **Get
Minecraft** opens minecraft.net.

**Java Edition files** shows the versions in this PC's `.minecraft` that a paired
handheld can copy, and whether Nearby devices is on.

## Unverified

Written without a device or Go toolchain at hand (2026-11-13): the PortMaster
catalogue's exact layout (the reader accepts the dict and list forms seen over
the years), the port's folder layout (`.minecraft` inside the port folder, or
`versions/` directly in it), and the autoinstall hand-off on stock TrimUI are
checked on the first device run (AND-20 in `player/MODERNIZATION.md`).
