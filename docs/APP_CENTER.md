# App Center

The App Center is a short catalogue of TrimUI apps (the folders MainUI lists
from the card's `Apps` folder) that says, for each one, where it comes from,
how it fares on the stock firmware and on CrossMix, what it needs, what it
opens, and lets you install, update, roll back, remove and start it from the
handheld. Open it from **Apps → App Center** or Home search ("app center").

The catalogue is compiled into JukaHub (`player/appcenter.go`); nothing is
fetched to decide what is listed. Only the newest release of an app is looked
up on GitHub, when you open the page (answers are kept for 30 minutes, errors
too) or press **Y** / choose **Look for new releases** (at most once a minute:
GitHub allows 60 unsigned lookups an hour).

## Honesty rules

- **Source.** Every entry names its GitHub repository and the release asset
  the installer takes, or the firmware it comes with. An app whose download
  could not be traced to a published release has no download: the page says
  where to get it. Asset names that were not seen in a release are never
  claimed; where the asset name is unknown the installer takes the release's
  one `.zip`, refuses a release with more than one, and refuses the zip unless
  it holds an app folder.
- **Status.** `Checked` means the app's own source (its README, release notes
  or the firmware's wiki) documents what the entry says: how it starts, what it
  opens, where it writes. A detail that only a third-party guide gives keeps the
  entry `Untested with JukaHub`, like everything else.
- **Firmware lines.** Each entry has a line for the stock firmware and one for
  CrossMix: `Works`, `Needs more` (what is missing is said), `Not supported` or
  `Untested`. `Works` is only written when a source says so. The row shows the
  line for *this* handheld: "Works here", "Needs CrossMix", "Untested here".
- **Dependencies** are checked live where that is possible (a file on the card,
  a Bluetooth controller, the network). What lives on a PC or a phone says
  "Can't check".
- **Opening files.** An app is listed under *Open with* for a type only when its
  source says it opens that type, or when its own `config.json` declares it
  (below). It is marked *verified* only when its source also says how the file
  is passed **and** the installed launch script passes it on. That is read from
  the script itself: it refers to `$1`, `$@` or `$*` outside single quotes, on a
  line that is not a comment or an `echo` (the same reading as KOReader's own
  "Open in KOReader"). The sheet says when it does not. A KOReader whose script
  ignores its arguments still gets the book: JukaHub runs KOReader's own
  `reader.lua` with it (`player/koreader.go`), or says to open it from
  KOReader's file browser when it cannot.

## The catalogue

| App | Source | Stock firmware | CrossMix | Needs | Opens | Status |
|---|---|---|---|---|---|---|
| KOReader | comes with CrossMix-OS CE v1.4.0-ce.1 (no download) | Needs more: ships inside CrossMix CE | Needs more: CE edition only (CrossMix 1.3.0 does not ship it) | — | epub pdf djvu cbz cbt fb2 mobi txt html rtf chm doc, as `$1` (reader.lua's argument) | Untested with JukaHub |
| Ritder (KOReader-based) | [nomiz1734/Ritder](https://github.com/nomiz1734/Ritder), `Ritder-stock.zip`, AGPL-3.0; updates itself | Untested: made for the Brick Pro's stock OS (TG5040 base) | Untested | Books folder | pdf cbz cbr epub djvu fb2 mobi (passing a file is not documented, so never *verified*) | Untested with JukaHub |
| Screen Recorder | comes with CrossMix-OS (no download); folder `ScreenRecorder` or `ScreencapTK` | Needs more: part of CrossMix | Works: CrossMix Apps wiki | output folder (from a third-party guide) | — | Untested with JukaHub |
| Xbox Pad BT | [AleWiccan/TSP-Apps](https://github.com/AleWiccan/TSP-Apps/releases/tag/GamepadBT1.4), newest `GamepadBT…` release, its `.zip` | Untested: made for the Smart Pro | Untested | Bluetooth; a phone or PC to pair (can't check) | — | Untested with JukaHub |
| Gamepad a PC | [AleWiccan/TSP-Apps](https://github.com/AleWiccan/TSP-Apps) README only (no download) | Untested | Untested | network; a PC program (not documented, can't check) | — | Untested with JukaHub |
| TrimUI Drop | [nhhuan90-ux/trimui-drop](https://github.com/nhhuan90-ux/trimui-drop/releases), `TrimUI-Drop-*.zip` into `Apps/SendFile` | Works: release notes name the Smart Pro and Brick | Untested | a network (Wi-Fi, or its own hotspot); a phone or PC browser | — | Checked |

The sources these lines were checked against:

- KOReader in CrossMix-OS CE: <https://github.com/SrScorpio/CrossMix-OS-SE/releases/tag/v1.4.0-ce.1>;
  KOReader's `reader.lua` opens a file given as its argument:
  <https://github.com/koreader/koreader/blob/master/reader.lua>. The original
  CrossMix 1.3.0 ships a different "Ebook Reader":
  <https://github.com/cizia64/CrossMix-OS/releases/tag/v1.3.0>.
- Ritder: <https://github.com/nomiz1734/Ritder> and its releases (v0.1.0 to v0.2.0).
- Screen Recorder: <https://github.com/cizia64/CrossMix-OS/wiki/apps> (start on
  launch, L1 + R1 or launch again to stop); the output folder
  `Apps/ScreenRecorder/output` comes only from a third-party install guide,
  <https://destructiveburn.com/trimui-smart-pro-crossmix-os/>, which is why the
  entry is not `Checked`.
- Xbox Pad BT: <https://github.com/AleWiccan/TSP-Apps/releases/tag/GamepadBT1.4>.
- Gamepad a PC: <https://github.com/AleWiccan/TSP-Apps> (README list).
- TrimUI Drop: <https://github.com/nhhuan90-ux/trimui-drop/releases/tag/v1.0.0>
  and `/v1.1.0`.

Not listed on purpose: PortMaster, Moonlight, Syncthing and scrapers (JukaHub
has them built in), ROM downloaders, and the stock firmware's own Reader,
Music and Picture apps (firmware 1.1.0 keeps them outside the card's `Apps`
folder).

## Install, update, roll back, remove

**Get** and **Update** ask GitHub for the newest release (of the entry's tag
family when a repository publishes several apps), download the asset the entry
names — only from that repository's own release downloads
(`https://github.com/<owner>/<repo>/releases/download/…`) — and check it against
the size and the SHA-256 GitHub publishes for it (the toast says when GitHub
publishes no checksum). The zip is unpacked with the Packages store's guard (no
links, nothing outside the folder, bounded size) and must hold exactly one app
folder: a `config.json` whose launch script lies inside the folder. Only then is
the card changed, by renames:

```
Apps/.jukahub-staging/   the download, the unpacked archive, the new version
Apps/<Folder>/           the installed version
Apps/.jukahub-rollback/  the version before it (Roll back)
Apps/.jukahub-trash/     copies being deleted
```

MainUI reads only `Apps/<folder>/config.json`, and JukaHub's own Apps scan
skips the three `.jukahub-` folders, so a kept copy never shows up as a second
app.

- The app goes into the folder its archive names, exactly. A versioned archive
  folder (`trimui-drop-1.1.0`), one whose name the installer cannot keep safe,
  or an archive without a folder goes into the entry's own folder instead
  (`Apps/SendFile` for TrimUI Drop), and the toast says so. A name is never
  quietly altered.
- Files you or the app wrote into the folder (settings, saves) move into the new
  version. A file the release ships replaces yours; your copy stays in the
  rollback folder.
- A swap that fails is undone at once. If the new version does not read as an
  app once it is in place, the previous one goes back and the failed one is
  deleted, so Roll back never offers it. A swap a power cut stopped halfway is
  finished or undone the next time the Apps screen, the App Center or Files'
  *Open with* scans the card.
- **Roll back** swaps the kept version back in; pressing it again goes forward.
- **Remove** moves the app folder, its kept version and any older copy of its
  files kept aside into the trash, then deletes them, settings and saves
  included; the sheet lists what goes, and for an app that comes with a
  firmware (KOReader, the Screen Recorder) says that the App Center cannot
  install it again.
- An app you installed by hand is never changed unless you press **Update** or
  **Remove** on it; the sheet says it was installed by hand first.
- A fresh install never replaces JukaHub's folder or a folder holding another
  app.
- The installed folder carries `.jukahub-appcenter.json`: the app, the release
  tag, the asset, its SHA-256, whether it was verified, the source and the time.
  The row's pill compares that tag with the newest release: "Update <tag>". An
  app that updates itself (Ritder, TrimUI Drop) may be newer than the tag the
  App Center wrote, and its version cannot be read, so its row says only
  "Installed", or "Newer release <tag>" rather than claiming an update.

Without a network the header says "Offline · browsing", the page still browses
and Get says it needs one. On a PC, on NextUI (which keeps tools as `.pak`
folders) and on spruceOS (whose apps it does not manage) the page browses only,
and Get and Open say where they work.

## Starting an app and coming back

Open (or **X** on the list) starts the app the way the Apps screen does: its
launch script, with the firmware's environment, and JukaHub hands the screen,
the sound device and (on the Smart Pro S) DRM master over until everything the
app started has exited. As for every launch, the voice assistant's CPU boost
and *Auto performance*'s lowered menu ceiling give the CPU back first. Then the
App Center remembers the CPU governor, the speed ceiling (only with Auto
performance off: with it on, Auto follows the app as its own session, one per
app whatever file it opened, and puts its ceiling back itself) and whether
TrimUI's input daemon (`trimui_inputd`) runs; after, it puts back what changed
and starts the input daemon again, in its own process group, if the app stopped
it. If the app's script starts MainUI as it exits (or ends in `exec MainUI`),
that MainUI is stopped and JukaHub takes the screen back; so is an input daemon
the app restarted with `&` in its own group, once nothing else of the app runs
(it is started again on its own). JukaHub looks for both for three seconds
after the app's script ends. The log gets one line per exit:
`appcenter: <app> exited (code N, S s)`, plus what was put back; a quick
failure or anything put back is also a toast.

The Screen Recorder is the exception: it starts a recording helper and returns,
so JukaHub starts it without handing the screen over, and what it records is
JukaHub.

## Places

- **Books** — Files' *Open with* lists KOReader and Ritder for their types
  (on the stock firmware and CrossMix), and suggests getting one when neither
  is installed.
- **Recordings** — JukaHub's own *Record screen* (Quick Settings, the in-game
  menu) is the recorder wherever it can read the panel, and the Recordings page
  lists the Screen Recorder app's clips beside its own. On a handheld where it
  cannot, the Recordings page gains a *Record the screen — Screen Recorder*
  button (*Get a screen recorder* when none is installed) that opens the App
  Center on the app.
- **Device** — Controller Mode lists the controller apps beside its own links.
  The App Center sheet of a controller app that is not installed has a
  *Controller Mode* button (an installed one's sheet has no room beside Remove,
  Roll back, Update and Open).

## Declaring the files an app opens

Any app can tell JukaHub which files it opens in its own `config.json`:

```json
{
  "label": "My Reader",
  "launch": "launch.sh",
  "jukahub": { "opens": [".epub", ".pdf"] }
}
```

Each value is an extension with or without the dot (a dot and up to ten letters
or digits; anything else is ignored). Files' *Open with* then offers the app for
those types (not marked *verified*: the catalogue does not know it) and starts
it with the file's absolute path as the launch script's first argument
(`"$1"`). The sheet notes when the launch script never refers to its arguments.

## Proposing an app

Open an issue or a pull request that adds an entry to `acCatalogue` in
`player/appcenter.go` with:

1. where it comes from: the GitHub repository and the exact release asset name
   (or the firmware that ships it), and its licence;
2. the page that documents what it does, how it starts and, if it opens files,
   how the file is passed;
3. what it was tried on (model, firmware, version) — without that its lines stay
   `Untested`;
4. what it needs, and how JukaHub could check each need on the device.

Apps that download ROMs or other copyrighted content, and apps JukaHub already
has built in, are not added.
