# Save Manager

Most people's progress on a handheld lives in exactly one place: a folder on the
SD card. The Save Manager copies those folders into versioned snapshots, puts
them back when something goes wrong, and produces the two things needed to get
the same saves onto a PC or a phone — a Syncthing folder list and a resume zip.

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

It also checks `~/.config/retroarch`, `~/.config/ppsspp` and
`~/.local/share/ppsspp` for a desktop install. A folder referenced by
`retroarch.cfg` wins over the built-in guesses; `:` (RetroArch's
config-directory prefix), `~` and relative paths are all resolved the way
RetroArch resolves them. The Save Manager's own data folder is skipped, so a
portable build cannot snapshot its own snapshots.

Nothing is created in an emulator's folder until **Syncthing setup** is used.

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

## Syncthing

**Syncthing setup** prepares both sides of a sync:

- writes `.stfolder` and `.stignore` into every save folder, which is what
  Syncthing uses to recognise a folder and to skip metadata;
- writes `data/save-manager/sync/folders.json` and a readable `README.txt` with
  one **Folder ID** per folder, such as `jukahub-retroarch-saves`.

JukaHub does not install, start or reconfigure Syncthing. Device IDs and
accepting a shared folder stay in Syncthing's own UI, on both the handheld and
the PC or Steam Deck. Add the listed paths with the listed Folder IDs, then share
them with each other.

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
