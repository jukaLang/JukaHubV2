# Games on a network drive

Keep your games on a NAS or a computer at home and play them on the
handheld: **Settings → Network → Network drives** adds the drive, its games
appear in the **Games library** beside the ones on the card, and pressing
**A** on one copies it to the card and starts it.

| Kind | For | What you type |
|---|---|---|
| Shared folder (SMB) | Windows shares and most NAS boxes (Synology, QNAP, TrueNAS, Unraid, a router's USB disk) | the server (`nas.local`, `192.168.1.20` or a whole `\\NAS\Games` path), the shared folder, a user and password — or none to sign in as a guest |
| WebDAV | Nextcloud, a NAS's WebDAV server | the WebDAV address (`https://nas.local:5006/`), user, password |
| SFTP | a NAS or computer with SSH switched on, a Raspberry Pi | the server (`user@nas.local:2222/games` works), user, password |
| Folder (PC only) | any folder Windows reaches, `\\NAS\Games` or a mapped drive included | the folder; Windows signs in with its own saved password |

JukaHub only **reads** from the drive: it lists folders and copies games to
the card. Nothing on the drive is changed or deleted.

## Adding a drive

1. **Settings → Network → Network drives → Find shared folders on this
   network** lists the NAS boxes and PCs that announce shared folders. Pick
   yours, or choose **Add a network drive** and type its address.
2. **Shared folder** asks the server for its shares; pick the one with your
   games (or type its name if the server does not list them).
3. **Games folder** opens a folder picker: go to the folder that holds the
   console folders and press **X** (or **Use this folder**).
4. **Test connection** signs in and names the console folders it found.
5. **Save**. The drive's games are listed in the background.

The password is kept encrypted in JukaHub's data folder and is never shown
again; type a new one to change it. Leave the user name empty to sign in to a
share as a guest. A Windows domain user is typed as `DOMAIN\user`.

### SFTP and the server's key

The first **Test connection** to an SFTP server shows its key fingerprint
(`SHA256:…`). Compare it with the server's own (`ssh-keygen -lf
/etc/ssh/ssh_host_ed25519_key.pub` on the server) and choose **Trust**. From
then on JukaHub talks to that drive only while it shows that key. If the key
ever changes, nothing is copied until you look at both fingerprints and
choose **Trust the new key** — do that only when you know the server was
reinstalled.

## Console folders

JukaHub reads the drive's folders the way it reads the card's `Roms` folder:
`GBA`, `SFC`, `PS`, `MD`, `Game Boy Advance (GBA)`… each become that
console's shelf. **Consoles** on the drive's page shows what it found. A
folder with another name can be given a console there: **Left/Right** steps
through the consoles (and **Not games**), **A** lets you type one (`gba`,
`psx`, `Nintendo 64`). If the games folder itself is one console's folder
(`\\NAS\Games\GBA`), choose its console on the first row, **This folder
itself**.

A game is what the card would count as one: a `.cue` with its tracks, an
`.m3u` with its discs, a `.gdi` with its tracks are one game each, and on DOS,
ScummVM and EasyRPG every game folder is one game. PortMaster ports are not
listed; copy those with **Files**.

## In the library

Network games sit on their console's shelf with a **NAS** badge on the cover
and "On <drive>" under the title. A game you already have on the card shows
once, as the card's copy. The list of what is on each drive is remembered, so
the shelves appear at once and also when you are away from home; it is
refreshed in the background when you open the library (at most every 15
minutes), or with **Refresh game lists** on the Network drives page.

When a drive does not answer — you are not on your home Wi-Fi, or it is
switched off — its games are dimmed and marked **Offline**, and **A** says
"<drive> is not reachable — connect to your home Wi-Fi" with **Try again**.

## Playing a network game

**A** on a network game opens its page with **Download and play**: the
game's size and the card's free space are shown first. The copy runs with its
progress on the sheet and on the game's row; **B** keeps it going in the
background, **Cancel download** stops it. A stopped or interrupted copy
continues where it left off the next time. When it is done the game starts
like any other.

The copy stays on the card. On the game's page, **More → Remove downloaded
copy** frees the space again: only the files that were copied go — saves,
save states and anything the game wrote stay, and the game goes back to its
network shelf.

## Good to know

- One game copies at a time. A copy whose drive stops sending for a minute
  (Wi-Fi gone) stops by itself; **Try again** continues it.
- Network games show in the Games library only, not on Home's tabs.
- A file you already have on the card under the same name is never replaced:
  if it is the same size JukaHub uses it (and Remove downloaded copy leaves
  it), if it differs the copy stops and says which file is in the way.
- A plain `http://` WebDAV address must be on your home network; anything on
  the internet needs `https://`.
- On the handheld, a name like `nas.local` may not resolve: **Find shared
  folders** fills in the address, or type the NAS's IP address.
- Where the files go: `Roms/<console>/` on the card, in the console folder the
  card already has for that console.
- What JukaHub keeps: `nas.json` (the drives, passwords encrypted),
  `nas-cache.json` (what is on them) and `nas-downloads.json` (what was copied)
  in its data folder.
