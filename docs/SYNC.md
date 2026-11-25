# Sync

**Tools → Sync** (also Settings → Network → Sync, or search "sync") is the one
place where your saves follow you: to your other JukaHubs, Google Drive, a
WebDAV server (Nextcloud, ownCloud, Koofr, a NAS), a RomM server and Syncthing.
It used to be five pages with five setups; now one QR code sets up any of them
from your phone, and once something is connected there is nothing more to do.

> Status: built 2026-11-24 (`player/synchub.go`, `synchub_peers.go`,
> `companion_sync.go`); not yet run on a device. The checks to make are listed
> in [hardware validation](HARDWARE_VALIDATION.md#regression-checks-without-separate-task-ids).

## Set up: one code

The page opens with a QR code. Scan it with your phone's camera: the phone opens
**Sync** on the handheld's phone page (the same address and PIN as the
[phone companion](GETTING_STARTED.md), on your Wi-Fi). It has one card per kind
of place, and you can use as many as you like:

| Card | What you do on the phone |
|---|---|
| Another JukaHub | Open Sync on the other JukaHub (same Wi-Fi). It appears on the phone; tap **Pair**. The phone and the other JukaHub show the same six-digit code: choose **Codes match** on the other JukaHub and tap it on the phone. |
| Google Drive | **Sign in with Google**: the phone shows Google's code and a link to google.com/device; approve it there. A build without a Google client links to the [one-time client setup](CLOUD_SYNC.md#google-drive-with-your-own-client). |
| WebDAV | The server's WebDAV address, your user name and an app password ([which address](CLOUD_SYNC.md#webdav-nextcloud-owncloud-koofr-a-nas)). |
| RomM server | The server's address and the code from RomM's **Client API Tokens → Pair device** (RomM 4.8 or later). A client API token can still be pasted instead. |
| Syncthing | **Turn on Syncthing** (the first time downloads it), then add the handheld's ID in Syncthing on your computer or Syncthing-Fork on Android, and accept the request on the phone. An ID pasted on the phone adds a device from this side. |

Everything you connect shows on the handheld under **Connected** as it
arrives; the big code closes as soon as the phone opens the page. **A** on the
code shows it full screen and brighter. Without Wi-Fi the card says so and A
opens Wi-Fi.

No phone at hand? **Set up on this screen instead** (bottom of the page): two
JukaHubs that both have Sync open list each other under **Found on this Wi-Fi**
— pick one and compare the code on both screens; Google Drive signs in with a
code shown on the handheld; the RomM page takes an address and a token; and
Syncthing's own page shows the handheld's ID as a code a Syncthing app can
scan.

## What happens by itself

With **Automatic sync** on (the default), every connected place takes part:

- **Before a game starts**, its saves come down from Google Drive (or the
  WebDAV server), from RomM, and from a paired JukaHub on this Wi-Fi that has
  them newer — within six seconds all told. Offline, or with nothing newer,
  the game starts at once.
- **After it ends**, its saves go up, and go to the paired JukaHubs on this
  Wi-Fi where they are newer here.
- **Half a minute later**, unless another game started, everything else syncs
  in the background: the clouds, and Save sync's whole comparison with each
  paired JukaHub on this Wi-Fi.
- **Syncthing** keeps its folders in step on its own whenever it runs. It has
  its own switch (its sheet); Automatic sync does not stop it.

Between two JukaHubs a game's saves only move by themselves when the last sync
between the two recorded which version both had, so the decision never rests
on the handhelds' clocks (a TrimUI can boot with its clock hours off). Saves
that changed on **both** devices are never chosen for you: a toast says so,
and **Sync › the device › Compare** shows both and lets you pick. The first
background sync after pairing records that version for every save the two
already share.

Nothing ever syncs while a game holds the screen, on either device: a JukaHub
in a game refuses every write and is simply left for next time. Before any
save is replaced, the device that loses a version takes a
[Save Manager](SAVE_MANAGER.md) snapshot, and nothing is ever deleted.

**Automatic sync** off: nothing moves by itself; **Sync everything now** and
each place's **Sync now** still work.

## The page

- **Sync everything now**: every paired JukaHub on this Wi-Fi, then Google
  Drive and the WebDAV server, one after another (B stops it); RomM's "Sync
  all saves" and a Syncthing rescan start beside it.
- **Connected**: one row per place. **A** opens its sheet: **Sync now**,
  **Compare** (file by file, with the choice for saves changed on both — for a
  paired JukaHub also collections, recents, BIOS and ROMs, see
  [Device Sync](DEVICE_SYNC.md)), its own **Auto** switch, and **Forget** or
  **Sign out**. Syncthing's sheet shows the handheld's ID as a QR code.
- **Found on this Wi-Fi**: JukaHubs that are not paired yet (**Pair**),
  Syncthing devices asking to sync (**Accept**) and Syncthing devices nearby.
- **What syncs with a paired device**: [Device Sync's](DEVICE_SYNC.md)
  switches.
- **Without a network**: **Export encrypted saves** writes every save into one
  passphrase-protected `.jukasaves` file to carry on a card or over SFTP;
  **Import a saves file** merges one on the other device with the same rules.

## Privacy and security

- A JukaHub is visible on the Wi-Fi only while Sync is open, while the phone
  page is in use or a pairing runs — or for good once it has paired with
  another JukaHub, because the two must keep finding each other: pairing
  switches **Settings → Network → Nearby devices** on. It answers with its
  name and model only; "Share what I'm playing" stays off unless you turn it
  on.
- Pairing is the same as before: the code is computed from both devices' keys
  and a random value from each, one committed before the other is revealed,
  so a device in the middle cannot make the two screens agree. The phone
  stands in for the handheld that showed it the QR code (it holds that
  handheld's PIN); the other JukaHub still confirms on its own screen.
- Every call from the phone needs the companion PIN, which only the QR code on
  the handheld's screen carries. The page never receives a password, token or
  key: only names, addresses and states.
- RomM's pairing code is exchanged for a token by the handheld itself
  (`POST /api/client-tokens/exchange`); it works once, for five minutes.
  Give each device its own RomM token: pairing a token again moves it to the
  new device.
- Sync between JukaHubs runs over TLS 1.3 with both keys pinned (port 8322);
  two devices never sync each other in both directions at once.

## Where it keeps things

| What | Where |
|---|---|
| Automatic sync | `syncAutomatic` in `jukauser.json` (on unless off) |
| Paired JukaHubs, Nearby devices | `data/peer/`, `peerNearbyDevices` in `jukauser.json` |
| Google Drive, WebDAV | `gdrive.json`, `webdav.json` in the data folder (secrets encrypted) |
| RomM | `romm-server.json` in the data folder (the token stays on the device) |
| Syncthing | `<data>/syncthing`, `syncthingEnabled` in `jukauser.json` |
