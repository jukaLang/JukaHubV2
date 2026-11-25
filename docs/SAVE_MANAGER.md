# Save Manager

Most people's progress on a handheld lives in exactly one place: a folder on the
SD card. The Save Manager copies those folders into versioned snapshots, puts
them back when something goes wrong, and produces the two things needed to get
the same saves onto a PC or a phone — a built-in Syncthing and a resume zip.

Open **Start → Save Manager**.

## What it finds

The panel scans the usual emulator save locations and only lists folders that
actually exist:

| Emulator | Folders |
|----------|---------|
| RetroArch | `savefile_directory` and `savestate_directory` from `RetroArch/retroarch.cfg`, else `RetroArch/saves`, `RetroArch/states`, `RetroArch/.retroarch/…`, `retroarch/…` |
| PPSSPP | `PSP/SAVEDATA`, `PSP/PPSSPP_STATE`, plus the same under `ppsspp/`, `Apps/PPSSPP/` and `Emus/PPSSPP/` |
| DraStic | `DraStic/backup`, `DraStic/savestates` (and the lowercase folders) |
| Flycast | `Flycast/vmu`, `Flycast/saves` |
| NextUI / MinUI | `Saves` (every `Saves/<TAG>`), and each `.userdata/shared/<TAG>-<core>` state folder; only on a card with `.system` or `.userdata` |

It also checks `~/.config/retroarch`, `~/.config/ppsspp` and
`~/.local/share/ppsspp` for a desktop install. A folder referenced by
`retroarch.cfg` wins over the built-in guesses; `:` (RetroArch's
config-directory prefix), `~` and relative paths are all resolved the way
RetroArch resolves them. The Save Manager's own data folder is skipped, so a
portable build cannot snapshot its own snapshots.

NextUI's `.userdata/shared/.minui` (its auto-resume marker) and the per-device
settings folders are deliberately left out: synced to a second handheld, they
would make it resume the first one's game.

Nothing is created in an emulator's folder until **Syncthing** is switched on.

## Snapshots

**Snapshot now** copies every discovered folder into:

```text
<data>/save-manager/snapshots/<id>/
├── manifest.json
└── files/
    ├── retroarch-saves/
    ├── retroarch-states/
    └── ppsspp-savedata/
```

`<id>` is a local timestamp such as `20261021-142530`, and the manifest keeps
the source path, the relative path, size and SHA-256 of every file. The snapshot
is a plain directory of ordinary files: it can be inspected, copied to a PC or
restored by hand without JukaHub.

The newest **10** snapshots are kept; older ones are pruned after each new one.
Empty save folders are not snapshotted. A single file over 256 MB or a snapshot
over 1 GB is refused, because that is almost certainly an ISO or a video sitting
in a save folder rather than a save. Syncthing markers, OS metadata and
`.tmp`/`.part` files are skipped, as are symlinks.

The copy goes into a staging folder first and is renamed into place only after
the manifest is written, so a full card or a yanked cable leaves no half
snapshot behind.

## Restoring

**Restore latest** and **Browse snapshots** both restore. Restoring always takes
a new `before restore` snapshot first, so the wrong pick can itself be undone.
Only files present in the snapshot are written; extra files already in the save
folder are left alone. A snapshot captured on a different card is matched to the
live folders by its original path, falling back to its label only when that
label is unique — the panel refuses rather than guessing when two folders share
a name.

Quit the game before restoring. Emulators hold save files open and will write
their in-memory state back over a restored file.

## Google Drive

**Tools → Save sync → Google Drive** keeps the same save folders in a "JukaHub
Saves" folder in your Google Drive, with no computer involved: sign in once by
scanning a code with your phone, and every library game's saves come down
before it starts and go up after it ends. Every sync from Drive that replaces a
save takes a snapshot here first. How it works and what the person building
JukaHub must set up in Google Cloud: [CLOUD_SYNC.md](CLOUD_SYNC.md).

## Syncthing

[Sync](SYNC.md) turns Syncthing on and accepts its devices from the phone too
(the QR code at the top of **Tools → Sync**), and lists it under **Connected**
with the handheld's ID as a QR code. This page has every setting.

**Syncthing** opens a page where JukaHub runs Syncthing for you. The first time
you switch it **On**, it downloads the official release from
github.com/syncthing/syncthing for this device: linux-arm64 on the handheld,
windows-amd64 on a PC (about 12 MB). The archive is checked against the SHA-256
that the release lists in `sha256sum.txt.asc`. If the hash is missing or
different, nothing is installed. (The PGP signature on that list is not checked;
the download relies on TLS to github.com.)

This is JukaHub's own Syncthing. It keeps its settings in `<data>/syncthing` and
its web interface on `127.0.0.1:8386` (`syncthingGuiPort` in the config changes
the port; 8384 is refused). That interface answers only to an API key that
JukaHub keeps in a 0600 file there and hands to Syncthing through its
environment, never on the command line, so `ps` cannot show it. The browser GUI
has a random password nobody knows. A Syncthing you installed yourself (port
8384) is never touched. If one is running, the page says so, and JukaHub syncs
on port 22100 so the two do not clash.

Pairing a phone or PC:

1. On the handheld, open **This device** to show its ID as a QR code.
2. On the other side, add the device. On Android use the Syncthing-Fork app; on
   a PC or Mac use Syncthing.
3. Back on the handheld, a **Pairing request** row appears. Press **A** to accept
   it, or **X** to dismiss it. Every save folder is shared with that device at
   once.
4. On the other side, accept the offered folders and choose where they go.

Two JukaHub devices on the same Wi-Fi see each other under **Nearby Syncthing**.
Press A on one, then accept the request on the other. Their folders have the
same IDs (`jukahub-retroarch-saves`, …), so they connect with no further step.
A paired device shows Connected or Offline; X twice unpairs it.

What it shares: every folder the Save Manager finds (NextUI's `Saves` and state
folders included), under the Folder IDs in `sync/folders.json`, the same IDs the
old manual setup used. The `.stfolder` and `.stignore` markers are written into
each folder. Replaced files are kept for 30 days under
`<data>/syncthing/versions/`, not inside the save folder. When both sides
changed a save, Syncthing keeps both copies: the losing one is renamed
`*.sync-conflict-*`, and the page shows a **Conflicts** count. Keep the right
copy in Files. A folder that disappears (a swapped card) is never unshared.

When it syncs:

- Syncing pauses the moment a game starts. When you are back in JukaHub it
  resumes and rescans, so a save made in-game goes out right away.
- It also pauses below 15 % battery (unless charging; it resumes at 20 %) and
  without Wi-Fi.
- On the handheld it does not start without Wi-Fi, and it stops after five
  minutes without it.
- It runs at the lowest CPU priority. When JukaHub exits, the whole Syncthing
  process tree is stopped (Syncthing runs as a monitor plus the real instance)
  and stays stopped. After a JukaHub crash it may keep running; the next
  JukaHub adopts it.

**LAN only** limits syncing to the same network: no relays and no global
discovery. Off (the default) also works over the internet, for example with a
phone on mobile data. **Remove Syncthing** (A twice) deletes the program and
this device's ID; your saves stay. There is no in-place update: auto-upgrade is
off, so a newer Syncthing means Remove and switch On again (which also gives the
handheld a new device ID).

### More than saves

**Also sync · Screenshots / Recordings / Music / Books / Roms** (each off by
default) share one more folder each: the screenshots folder, the gameplay
recordings folder, `Music` and `Books` on the card (`Music` and
`Documents/Books` in your home folder on a PC) and the Roms folder JukaHub reads
games from. They get the IDs `jukahub-extra-screenshots`, …, the same settings
as the save folders (FAT-friendly times, versions kept 30 days under
`<data>/syncthing/versions/`), the same pauses, and go to every paired device, so
two JukaHubs match them up by themselves. A folder that is missing is made.
Turning one off takes it out of Syncthing; its files stay. Roms can be many GB:
the first sync takes hours and needs that much room on the other side.

**Web GUI on this network** (off by default) opens Syncthing's own web page to
the Wi-Fi, so a PC's browser can add any folder. Syncthing
restarts listening on `0.0.0.0:8386` with the user `jukahub` and a password
JukaHub makes up (kept in `<data>/syncthing/jukahub-webgui-password`). The
**Open the web GUI** row shows the address and the password, and A shows the
address as a QR code. Anyone on the same Wi-Fi who has the password can change
what Syncthing shares: leave it off on public networks. Off again, Syncthing
restarts on `127.0.0.1` with a new password nobody knows.

Power tools → Folder sync → **Syncthing status** reports the same state, plus
any other Syncthing on port 8384.

Do not sync the same folders with both Syncthing and **Tools → Save sync**
between two devices: the two are not coordinated. Pick one.

## Device Sync

**Tools → Save sync** with a paired device can also carry save states (or
leave them out), BIOS files, collections, recents and play time, missing ROMs
and your settings: see [DEVICE_SYNC.md](DEVICE_SYNC.md).

## Phone resume bundle

**Phone resume bundle** zips the newest snapshot into:

```text
<data>/save-manager/resume/JukaHub-resume-<id>.zip
```

The zip contains `manifest.json`, a short `RESUME.txt`, and the `files/` tree
with the same folder slugs as the snapshot. It is meant for the existing
**Companion** file transfer or the FTP server: send the zip to the phone, unzip
it, and drop each folder's contents into the matching emulator folder there.

## Where the data lives

| Path | Contents |
|------|----------|
| `Apps/JukaHub/data/save-manager/snapshots/` | snapshots, newest first in the panel |
| `Apps/JukaHub/data/save-manager/sync/` | `folders.json` and `README.txt` |
| `Apps/JukaHub/data/save-manager/resume/` | resume zips |
| `Apps/JukaHub/data/syncthing/` | JukaHub's Syncthing: config, database, API key, log, old versions |

On Windows the data folder is under `%APPDATA%\JukaHub`. Deleting a snapshot from
the panel (or the folder) is permanent; snapshots are not uploaded anywhere.

## Controls

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through rows |
| L1 / R1 | Page by five rows |
| A | Activate the row; press again to confirm a restore |
| X | Delete a snapshot (press again to confirm) |
| B / Esc | Back to the main list, then close |

Tapping a row activates it; tapping outside closes the panel.

## Limits

- Snapshots are local. Nothing is uploaded, and there is no automatic snapshot
  when a game exits yet — press **Snapshot now** before an update or a long
  break.
- The panel lists what it can find, but it does not edit emulator settings. If
  saves live somewhere unusual, set `savefile_directory` in `retroarch.cfg` so
  RetroArch and the Save Manager agree.
- Restores copy files; they do not merge save states from different emulator
  versions.
