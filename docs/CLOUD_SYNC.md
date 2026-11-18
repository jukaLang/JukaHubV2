# Cloud save sync (Google Drive)

JukaHub keeps your saves in your own Google Drive and brings them to every
JukaHub you sign in on — no computer, no Syncthing, no rclone. Sign in once on
each device by scanning a code with your phone; after that a library game's
saves come down before it starts and go up after it ends.

Open **Tools → Save sync**. The Google Drive card is at the top.

> Status: built 2026-11-17 and unverified on a device. The Drive calls follow
> Google's documentation; the first real runs are a [HW] task in
> `player/MODERNIZATION.md`.

## Signing in

1. **Sign in to Google Drive.** The page shows a short code, the address
   `google.com/device` and a QR code of that address. **Show the code full
   screen** enlarges the QR code and turns the backlight up for the camera.
2. On your phone or computer, open the address, enter the code, choose the
   Google account and allow JukaHub to "see, edit, create and delete only the
   specific Google Drive files you use with this app".
3. The page notices within a few seconds, shows the account and compares your
   saves with Drive. **Sync now** sends them up the first time.

Nothing is typed on the handheld. The code works for about 30 minutes; **Cancel
sign-in** stops waiting.

JukaHub asks only for the `drive.file` scope: it can see the files it made
itself and nothing else in your Drive. Signing out (**Sign out** on the card)
forgets the account on this device and revokes the token at Google; the saves
on Drive and on the device stay.

## What lands in Drive

```
My Drive/
  JukaHub Saves/
    retroarch-saves/        one folder per save folder Save Manager finds
      Zelda.srm
    retroarch-states/
      Zelda.state1
    ppsspp-savedata/
      ULUS10041/...
```

The folder names are Save sync's folder keys, the same on every JukaHub. Every
file carries its SHA-256 in Drive's app properties. You can move or rename the
folders in the Drive app; JukaHub finds them by those labels, not by name.

A file that changes is **updated in place**, so Drive keeps its earlier versions:
in the Drive app, right-click a save → **File information → Manage versions**
(Drive keeps them for 30 days, or forever if you tick "Keep forever").

## The rules

Drive is just another "other side" of Save sync (`player/ssync.go`), with one
difference: **the clock never decides.** A TrimUI can start with its clock in
1970 and sync before the network sets it, so only checksums and the record of
the last sync count:

- a save on one side only is copied to the other;
- identical saves are left alone;
- if one side still has the version both had after the last sync (the *base*,
  kept per Google account in `data/ssync/base-gdrive-*.json`), it takes the
  other side's change;
- otherwise — both changed, or the very first sync finds two different copies
  — **both are kept**: this device's copy keeps its name on both sides, and
  Drive's lands beside it as `Zelda (from Google Drive).srm`;
- nothing is ever deleted, on either side.

Before a sync replaces a save on the device, Save Manager takes a snapshot of
it ("before sync with Google Drive"), so every sync can be undone from **Start →
Save Manager**. Downloads go to a temp file, are checked against their SHA-256,
flushed to the card and renamed into place, so a dropped connection never
leaves half a save.

## Automatic sync

**Sync automatically when a game starts and ends** is on after signing in.

- **Before a library game starts**, that game's saves and states (files named
  like the ROM: `Zelda.srm`, `Zelda.state1`, `Zelda.state.auto`) are fetched,
  for at most **6 seconds** together with RomM's auto-sync
  ([ROMM_SERVER.md](ROMM_SERVER.md)), Drive first. Offline, signed out or
  switched off, the game starts at once. The launch waits on a background
  thread, never the screen, and starts the game when the saves are in.
- **After the game ends** (its whole process group is gone), its saves go up.
  Half a minute later, unless another game started, everything else is synced
  in the background (PSP save data, saves from other folders). A game starting
  stops that background sync at once; nothing syncs while a game holds the
  screen.
- A toast says what happened when something did ("Saves updated from Google
  Drive", "both kept"), and nothing for a sync that had nothing to do. A failure
  shows at most once every ten minutes; an expired sign-in always shows.

Games started from somewhere other than the library (a firmware's own menu,
Ports, Android apps) are covered by the background sync and **Sync now**, not
by the per-game pull.

## Setting it up (for whoever builds JukaHub)

Google sign-in needs an OAuth client that belongs to the person or project
publishing the build. Without one the card says "Google Drive sign-in is not
set up in this build" and everything else in Save sync still works.

### 1. Google Cloud project

1. Open <https://console.cloud.google.com/> and create a project (for example
   "JukaHub").
2. **APIs & Services → Library → Google Drive API → Enable.**

### 2. OAuth consent screen (Google Auth Platform)

1. **Google Auth Platform → Branding**: app name "JukaHub", support email, and
   the developer contact email. A logo is optional (adding one means Google
   must verify the branding).
2. **Audience**: user type **External**.
3. **Data Access → Add or remove scopes**: add
   `https://www.googleapis.com/auth/drive.file` ("See, edit, create and delete
   only the specific Google Drive files you use with this app"). It is a
   non-sensitive scope, so no security assessment is needed.
4. **Testing vs. production.** While the app is in **Testing**, only the test
   users you list can sign in, and Google expires their sign-in after **7
   days** (JukaHub then shows "sign in again"). For real users, **Audience →
   Publish app**. With only `drive.file` and no logo, publishing needs no
   review.

### 3. The client

1. **Google Auth Platform → Clients → Create client.**
2. Application type: **TVs and Limited Input devices**. Name: "JukaHub".
3. Copy the **Client ID** (`…apps.googleusercontent.com`) and the **Client
   secret**. For this client type Google treats the secret as not confidential
   (it ships inside the app), but keep it out of the public repository anyway:
   GitHub's secret scanning reports Google client secrets.

### 4. Giving the client to JukaHub

Any one of these, checked in this order on every sign-in:

| Where | How |
|---|---|
| Environment at run time | `JUKAHUB_GOOGLE_CLIENT_ID` and `JUKAHUB_GOOGLE_CLIENT_SECRET` |
| Config | `google_client_id` and `google_client_secret` in the config's variables (the secret may be stored `ENC:`-encrypted like other keys) |
| Build | `player\build.bat`, `player/release.sh` and `.github/scripts/trimui-build.sh` stamp `-X main.googleOAuthClientID=… -X main.googleOAuthClientSecret=…` in when `JUKAHUB_GOOGLE_CLIENT_ID`/`_SECRET` are set while building |

For release builds on GitHub, add two **repository secrets** named
`JUKAHUB_GOOGLE_CLIENT_ID` and `JUKAHUB_GOOGLE_CLIENT_SECRET` (Settings →
Secrets and variables → Actions). The release workflow
(`.github/workflows/docker-image.yml`, both the TrimUI and the Windows
"Build Package" steps) passes them to the build. Builds from forks and pull
requests have no secrets and come out without Google sign-in.

A refresh token only works with the client that made it, so each device keeps
the client it signed in with (in `gdrive.json`). Changing the client later means
signing in again on each device.

## Where the data lives

| Path | What |
|---|---|
| `data/gdrive.json` | the account, the client, the refresh token (encrypted with the player's key), the automatic-sync switch and the last result; 0600 |
| `data/ssync/base-gdrive-<hash>.json` | what this device and Drive both held after the last sync |
| `data/ssync/hashes.json` | the checksum cache shared with Save sync |

Tokens never appear in the log.

## Limits

- Saves larger than Save Manager's per-file limit (256 MB) are left out.
  Files up to 5 MB go up in one request; larger ones through a resumable
  upload that restarts (rather than resumes) if it is cut off.
- Drive labels are limited to 124 bytes, so a save folder nested very deep
  (a folder path longer than about 115 characters) is not uploaded.
- The per-game pull matches saves by the ROM's file name, so a ROM with another
  name on another device shares saves only through **Sync now** after renaming.
  PSP save data (`ULUS10041/…`) is not named after the ROM and comes with the
  background sync instead.
- A save that changed on both sides keeps both copies; choosing one is done in
  the save folder (rename the copy you want) or with Save Manager's restore.
