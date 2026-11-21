# WASM-4 fantasy-console games

[WASM-4](https://wasm4.org) is a fantasy console: a 160×160 screen in four
colours, a D-pad and two buttons for up to four players, a mouse, and 1 KB of
"disk" for saves. Each game ("cart") is one small WebAssembly file, a `.wasm`
of at most 64 KB. JukaHub lists them in **Games** as a console of their own,
**WASM-4**, and plays them in RetroArch with the WASM-4 libretro core. They are
light work for every TrimUI handheld.

## Where the games go

Put the `.wasm` files in **`Roms/WASM4`** on the card (on a PC, the
`Roms/WASM4` folder beside JukaHub). Folders named `wasm-4` or `WASM 4` work too
(Batocera calls it `wasm4`), and so does a sub-folder inside one.

A `.wasm` counts as a game only in a WASM-4 folder. Anywhere else a `.wasm` is
part of a web page or a tool, so JukaHub never lists those as games.

The **Retro formats** plugin used to teach the library WASM-4 files; WASM-4 is
built in now and the plugin teaches the other five formats
([Plugins](PLUGINS.md)).

## Getting carts

**Get WASM-4 carts** lists the games published on wasm4.org. Open it from the
Start menu's Games section, or from **More** on the page of any WASM-4 game.

- **A** installs the highlighted cart into the WASM-4 folder under its title,
  with its screenshot as its cover, or plays it once it is installed.
- **Y** shows its details: its author and a code to its page on wasm4.org (on
  Windows, a button opens the page in the browser).
- **X** deletes a cart this screen installed, and the cover it saved. Its saves
  stay, so installing it again picks up where you left off.
- **L1 / R1** (PgUp / PgDn) page through the list; **B** goes back.

Each cart is its author's work. Its page on wasm4.org has its manual and its
licence; JukaHub installs the cart unchanged for your own play.

The list comes from wasm4.org's `carts.json`, else from the carts folder of the
[WASM-4 repository](https://github.com/aduros/wasm4/tree/main/site/static/carts)
on GitHub. JukaHub keeps the last list it read, so the screen works offline;
only installing needs the internet.

## The first start

The first time you start a WASM-4 game, JukaHub installs the WASM-4 core (once,
under 1 MB), with the install card up, and then the game starts by itself
(unless you started something else meanwhile; then the message says it is
ready). After that a start is immediate. Offline, the first start says to
connect first.

On the handheld the core comes from JukaHub's own aarch64 build in its release
[`cores-aarch64`](https://github.com/jukaLang/JukaHubV2/releases/tag/cores-aarch64),
which loads on the stock firmware. Until that release has it, JukaHub takes the
libretro buildbot's nightly instead. Every build goes through the same checks
as any core install: the index's checksum, and a load check against the
firmware before anything is installed. A nightly that needs a newer C library
than the firmware's runs on JukaHub's private one when **RetroArch's C library**
is on Automatic ([Private C runtime](PRIVATE_GLIBC.md)).

## Controls

- The D-pad moves, and WASM-4's two buttons (X and Z on a keyboard) are on the
  pad's face buttons. RetroArch's **Quick Menu → Controls** shows which ones
  and can move them.
- Up to four players: each pad JukaHub gives RetroArch is one player.
- **Games that use the mouse**: turn on **Settings → Controls → Mouse in games
  and apps**, start the game, and hold **Select + L1**: the pad becomes a mouse
  for the game ([Controls](CONTROLS.md#mouse-in-games-and-apps)). Hold the same
  two buttons again to give the pad back.
- Leave a game the way you leave any RetroArch game (RetroArch's menu → Quit,
  or JukaHub's in-game menu).

## Saves and the picture

- A game's 1 KB disk is the core's battery save: a `.srm` named after the cart
  in RetroArch's saves folder. The save tools (Save Manager, Save sync) find it
  by that name like any console's.
- The picture is square. JukaHub has RetroArch scale it by whole pixels for the
  game (4× on a 720-line screen), so the pixels stay sharp, and puts your
  RetroArch setting back when the game ends. A picture mode you chose yourself
  in Settings or in the game's options (Sharp, Smooth, a look) is used instead.

## On a PC

On Windows JukaHub uses the RetroArch the PC already has (the one the Emulators
page finds or installed): the first start puts the buildbot's Windows
`wasm4_libretro.dll` in that RetroArch's `cores` folder. With no RetroArch on
the PC, the first start says to install it on the Emulators page. On a Linux
desktop JukaHub downloads nothing: get the core in RetroArch's Online Updater
(Core Downloader → WASM-4).

## When a game does not start

- **"… need the WASM-4 core first … connect to the internet …"**: the first
  start needs the internet once.
- **"no WASM-4 core is published for this device yet …"**: neither JukaHub's
  release nor the buildbot had a build; try again later.
- **"… cannot load here …"**: the build needs libraries this firmware does not
  have and was not installed; the next run of the cores workflow brings one that
  loads.
- **"WASM-4 games play in RetroArch …"** (PC): install RetroArch on the
  Emulators page.
- A game that quits at once gets the usual diagnosis card with RetroArch's log.

## How the handheld build is made

JukaHub's [RetroArch cores workflow](https://github.com/jukaLang/JukaHubV2/blob/main/.github/workflows/retroarch-cores.yml)
builds WASM-4 with the other cores (`player/native/build-cores.sh`): upstream's
`runtimes/native` CMake project for its `wasm4_libretro` target, the libretro
buildbot's own recipe, in an Ubuntu 20.04 arm64 root (glibc 2.31), checked to
load with nothing newer before it is published in `cores-aarch64`. To build
only this core, run the workflow by hand with `wasm4` in the cores box. The
code is `player/wasm4.go`, `player/wasm4_install.go` and
`player/wasm4_carts*.go`.
