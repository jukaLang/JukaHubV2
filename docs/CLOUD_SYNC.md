# Cloud save sync

JukaHub keeps your saves in a cloud you own and brings them to every JukaHub
you connect: a library game's saves come down before it starts and go up after
it ends. There are four ways to get there; pick the first one that fits.

| Route | Needs | Setup on the handheld |
|---|---|---|
| [Google Drive, built-in sign-in](#google-drive-with-the-builds-sign-in) | a release built with the project's Google client | scan a code, approve on the phone |
| [Google Drive, your own client](#google-drive-with-your-own-client) | a free Google Cloud project (10 minutes, once) | send one file from the phone |
| [WebDAV](#webdav-nextcloud-owncloud-koofr-a-nas) | Nextcloud, ownCloud, Koofr, InfiniCLOUD or a NAS | type the address and an app password on the phone |
| [Google Drive through a computer](#google-drive-through-a-computer) | a PC or Mac with Google Drive for desktop | Syncthing, no sign-in |

Everything is in **Tools → Save sync**: the Google Drive card at the top, then
**WebDAV and other clouds**. The phone parts happen on the Phone Companion's
**Cloud save sync** page (the handheld shows a QR code that opens it; the
companion's main page links to it too).

> Status: Google Drive built 2026-11-17, phone setup and WebDAV 2026-11-18; none
> of it has run on a device yet. The first real runs are [HW] tasks in
> `player/MODERNIZATION.md`.

## Google Drive with the build's sign-in

When the release was built with the project's Google client (see
[For whoever builds JukaHub](#for-whoever-builds-jukahub)), the card says
**Sign in to Google Drive**:

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
itself and nothing else in your Drive. **Sign out** forgets the account on this
device and revokes the token at Google; the saves on Drive and on the device
stay.

## Google Drive with your own client

Google lets a sign-in happen only through an *OAuth client* that belongs to a
Google Cloud project, one per app. A build made without the project's client
(or a player who would rather use their own) shows **Set up Google sign-in**.
Making a client is free and takes about ten minutes, once; every JukaHub you
own can use the same one.

1. On the handheld, **Tools → Save sync → Set up Google sign-in**. Scan the
   code: the phone opens the **Cloud save sync** page at its Google card.
2. On a computer, open <https://console.cloud.google.com/> and create a project
   (for example "JukaHub").
3. **APIs & Services → Library → Google Drive API → Enable.**
4. **Google Auth Platform → Branding**: app name "JukaHub", your email as the
   support and the developer contact. No logo (a logo means Google must review
   the branding).
5. **Audience**: user type **External**, then **Publish app**. Left in
   *Testing*, only the test users you list can sign in, and Google signs them
   out every **7 days** (JukaHub then says "sign in again"). With only
   `drive.file` and no logo, publishing needs no review.
6. **Data Access → Add or remove scopes**: add
   `https://www.googleapis.com/auth/drive.file` and save.
7. **Clients → Create client**: application type **TVs and Limited Input
   devices**, name "JukaHub", **Create**, then **Download JSON**
   (`client_secret_….json`).
8. On the phone page, pick that file (send it to the phone first, or open the
   page on the computer — it is on your home network) and tap **Save on
   handheld**. Pasting the **Client ID** and **Client secret** works too.

The handheld checks the client with Google at once by asking for a sign-in
code with it. A *Desktop* or *Web application* client, a service-account key,
or a client that was deleted is refused right there with a sentence saying
what to make instead. When Google accepts it, the phone page shows the sign-in
code and a link to `google.com/device` — approve it on the phone you are
holding — and the handheld shows the same code and waits, as in the route
above.

The client is kept in the config as `google_client_id` (plain; it is not a
secret, it appears in every sign-in address) and `google_client_secret`
(encrypted with the device's key). Neither is carried by a settings export:
the secret never leaves the device, and the ID alone would make a client that
cannot sign in. **Your own Google client → Change** on the handheld opens the
phone page again; **Remove this client** there empties both keys and, when the
current sign-in was made with that client, signs out too (a sign-in only works
with the client that made it).

Order of precedence, checked at every sign-in: `JUKAHUB_GOOGLE_CLIENT_ID` and
`_SECRET` in the environment, then the config keys (your own client), then the
client stamped into the build. The phone page says so when the environment
wins.

## WebDAV (Nextcloud, ownCloud, Koofr, a NAS)

No Google project at all: any server that speaks WebDAV.

1. **Tools → Save sync → Connect a WebDAV server**, scan the code.
2. On the phone page's WebDAV card, enter the server's WebDAV address, your user
   name and an **app password**, and tap **Save on handheld**.

| Server | Address | Password |
|---|---|---|
| Nextcloud, ownCloud | `https://SERVER/remote.php/dav/files/USER/` (typing just `https://SERVER` is enough: JukaHub finds this folder itself) | an app password: Settings → Security → Devices & sessions → Create new app password |
| Koofr | `https://app.koofr.net/dav/Koofr/` | an app password: Preferences → Password → App passwords |
| InfiniCLOUD (TeraCLOUD) | the WebDAV address on My Page | the Apps password on My Page |
| A NAS (Synology, QNAP, TrueNAS, OpenMediaVault…) | its WebDAV address, e.g. `http://192.168.1.20:5005/home/` | your NAS account |

The handheld checks the address and the password with the server (a PROPFIND
of the folder) before it saves them; a refused password, a folder that is not
there or a web page that is not WebDAV is said on the phone. The password is
stored encrypted in `data/webdav.json` (0600) and never shown again. Leave it
empty later to keep the saved one (only for the same server and account).

**HTTPS is required unless the server is on your home network.** WebDAV signs
in with Basic auth, which sends the password with every request: over plain
`http://` anyone on the way — a café's Wi-Fi, the provider — could read it, and
the saves. A server at home may use `http://` (many NAS boxes have no
certificate a handheld would trust): a private address (`192.168.x.x`,
`10.x.x.x`, `172.16–31.x.x`), a Tailscale address (`100.64–127.x.x`, already
encrypted by WireGuard) or a home-network name (`nas`, `nas.local`, `.lan`,
`.home.arpa`). Even then JukaHub refuses to connect unless the name really
resolves to such an address, uses no proxy for it, and follows no redirect
anywhere (with HTTPS too: a redirect would carry the password to an address
nobody typed). A NAS with a self-signed certificate: use its `http://` address
at home. Only Basic auth is spoken; a server that offers Digest alone says so.

What lands on the server:

```
<your address>/
  JukaHub Saves/
    .jukahub-manifest.json     JukaHub's own record of every file
    retroarch-saves/Zelda.srm  the same folders as on Google Drive
    ppsspp-savedata/ULUS10041/...
```

A WebDAV server knows sizes, ETags and dates, none of which is a checksum, so
JukaHub keeps `.jukahub-manifest.json` beside the saves: each file's SHA-256,
size, its own modification time (it travels, it is never compared), and the
ETag and Last-Modified the server gave that upload. A file whose size or ETag no
longer matches — replaced in the server's web page, or uploaded by a device
whose manifest write lost a race — is downloaded and hashed again (up to 400
files or 128 MB per comparison; the rest next time), so the rules never act on
a stale checksum. Manifest writes re-read and merge the copy on the server,
with `If-Match`. Every upload carries `If-Match` (or `If-None-Match: *` for a
new file), so a save that changed on the server since the comparison is
refused, not overwritten; the next comparison decides it.

Listing tries one `Depth: infinity` PROPFIND and goes folder by folder when the
server refuses it (Nextcloud and Apache usually do; JukaHub remembers). The
per-game pull before a launch lists only the folders the manifest says hold
that game's saves, to fit in the 6 seconds.

Google Drive has no WebDAV, so this card cannot reach it — use one of the
other routes.

**Forget this server** removes the address and password from the device; the
saves on the server stay.

## Google Drive through a computer

No Google project and no sign-in on the handheld: a PC or Mac carries the saves.

1. On the computer, install **Google Drive for desktop** and **Syncthing**
   (<https://syncthing.net>).
2. On the handheld, **Start → Save Manager → Syncthing**: switch it on, show
   "This device" and add it in Syncthing on the computer.
3. On the computer, accept the save folders Syncthing offers and put them
   inside the Google Drive folder (for example `G:\My Drive\JukaHub Saves\`).

Saves move whenever the handheld and the computer see each other (same Wi-Fi,
or online through Syncthing's relays) and the computer uploads them to Drive.
The computer must be on. **Tools → Save sync → Google Drive through a
computer** shows these steps on the handheld.

## The rules

Every cloud is just another "other side" of Save sync (`player/ssync.go`), with
one difference from a paired device: **the clock never decides.** A TrimUI can
start with its clock in 1970 and sync before the network sets it, so only
checksums and the record of the last sync count:

- a save on one side only is copied to the other;
- identical saves are left alone;
- if one side still has the version both had after the last sync (the *base*,
  kept per Google account in `data/ssync/base-gdrive-*.json` and per WebDAV
  server and account in `data/ssync/base-webdav-*.json`), it takes the other
  side's change;
- otherwise — both changed, or the very first sync finds two different copies
  — **both are kept**: this device's copy keeps its name on both sides, and
  the cloud's lands beside it as `Zelda (from Google Drive).srm` or
  `Zelda (from WebDAV).srm`;
- nothing is ever deleted, on either side.

Before a sync replaces a save on the device, Save Manager takes a snapshot of
it ("before sync with Google Drive", "before sync with WebDAV"), so every sync
can be undone from **Start → Save Manager**. Downloads go to a temp file, are
checked against their SHA-256, flushed to the card and renamed into place, so a
dropped connection never leaves half a save. The only cloud copy a sync
replaces is the version both sides had at the last sync; Google Drive keeps the
earlier version in its version history (30 days), and Nextcloud and ownCloud
keep versions too (the Versions tab of a file).

Only one cloud sync runs at a time, whichever cloud it is with.

## Automatic sync

**Sync automatically when a game starts and ends** is on after signing in to
Google Drive or connecting a WebDAV server. Only one cloud does the automatic
syncs: Google Drive while it is signed in with its switch on, otherwise the
WebDAV server. (Two clouds would play the same saves against each other; with
both connected, WebDAV syncs when you press Compare.)

- **Before a library game starts**, that game's saves and states (files named
  like the ROM: `Zelda.srm`, `Zelda.state1`, `Zelda.state.auto`) are fetched,
  for at most **6 seconds** together with RomM's auto-sync
  ([ROMM_SERVER.md](ROMM_SERVER.md)), the cloud first. Offline, signed out or
  switched off, the game starts at once. The launch waits on a background
  thread, never the screen, and starts the game when the saves are in.
- **After the game ends** (its whole process group is gone), its saves go up.
  Half a minute later, unless another game started, everything else is synced
  in the background (PSP save data, saves from other folders). A game starting
  stops that background sync at once; nothing syncs while a game holds the
  screen.
- A toast says what happened when something did ("Saves updated from Google
  Drive", "both kept"), and nothing for a sync that had nothing to do. A failure
  shows at most once every ten minutes; an expired Google sign-in or a refused
  WebDAV password always shows, and stops the automatic syncs until it is fixed
  from the phone.

Games started from somewhere other than the library (a firmware's own menu,
Ports, Android apps) are covered by the background sync and **Sync now**, not
by the per-game pull.

Only saves sync this way. Screenshots and other files are not part of it: the
rules and Save Manager's safety snapshots treat every synced folder as saves
(1 GB per sync at most), and every device's screenshots would land in one
folder. Syncthing (above) can carry any folder.

## For whoever builds JukaHub

With the project's own Google client stamped into release builds, every player
just signs in — no Google Cloud steps for anyone. Make the client once (steps
2–7 of [your own client](#google-drive-with-your-own-client); publish the app,
or every player is signed out after 7 days), then give it to the build:

| Where | How |
|---|---|
| GitHub releases | two **repository secrets**, `JUKAHUB_GOOGLE_CLIENT_ID` and `JUKAHUB_GOOGLE_CLIENT_SECRET` (Settings → Secrets and variables → Actions). The release workflow (`.github/workflows/docker-image.yml`, both the TrimUI and the Windows "Build Package" steps) passes them to the build. Builds from forks and pull requests have no secrets and come out without Google sign-in (their players can still use their own client). |
| Local builds | set `JUKAHUB_GOOGLE_CLIENT_ID`/`_SECRET` while running `player\build.bat`, `player/release.sh` or `.github/scripts/trimui-build.sh`: they stamp `-X main.googleOAuthClientID=… -X main.googleOAuthClientSecret=…` in |
| One device, at run time | the same two variables in the environment (they win over everything) |

For this client type Google treats the secret as not confidential (it ships
inside the app), but keep it out of the public repository anyway: GitHub's
secret scanning reports Google client secrets.

A refresh token only works with the client that made it, so each device keeps
the client it signed in with (in `gdrive.json`). Changing the client later means
signing in again on each device.

## Where the data lives

| Path | What |
|---|---|
| `data/gdrive.json` | the Google account, the client it signed in with, the refresh token (encrypted with the device's key), the automatic-sync switch and the last result; 0600 |
| `data/webdav.json` | the WebDAV address, user name, password (encrypted), the automatic-sync switch, whether the server refused the password or a whole-tree listing, and the last result; 0600 |
| config `google_client_id`, `google_client_secret` | your own Google client, when set from the phone (the secret encrypted) |
| `data/ssync/base-gdrive-<hash>.json`, `base-webdav-<hash>.json` | what this device and the cloud both held after the last sync |
| `data/ssync/hashes.json` | the checksum cache shared with Save sync |

Tokens and passwords never appear in the log, and never go back to the phone
page (it learns where the Google client comes from and its ID, the account, a
waiting sign-in code, and the WebDAV address and user name).

## Limits

- Saves larger than Save Manager's per-file limit (256 MB) are left out.
  Google Drive: files up to 5 MB go up in one request, larger ones through a
  resumable upload that restarts (rather than resumes) if it is cut off.
  WebDAV: every file goes up in one PUT.
- Drive labels are limited to 124 bytes, so a save folder nested very deep
  (a folder path longer than about 115 characters) is not uploaded to Drive.
- The per-game pull matches saves by the ROM's file name, so a ROM with another
  name on another device shares saves only through **Sync now** after renaming.
  PSP save data (`ULUS10041/…`) is not named after the ROM and comes with the
  background sync instead. On WebDAV, the per-game pull sees only the files a
  JukaHub manifest already lists; a save copied onto the server by hand comes
  with the next whole sync.
- A save that changed on both sides keeps both copies; choosing one is done in
  the save folder (rename the copy you want) or with Save Manager's restore.
