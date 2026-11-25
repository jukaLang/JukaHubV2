# RomM server

**Tools → RomM server** connects the handheld to a RomM server you run. Enter
its full `http://` or `https://` address and a RomM **Client API Token** with
read and write access under **Connection**, then choose **Connect and load games**.
The token is stored in JukaHub's local data folder, is hidden on the connection
screen, and is sent to your server as a bearer token.
With RomM 4.8 or later there is no token to copy: on the phone's **Sync** page
(the QR code at the top of **Tools → Sync**, see [Sync](SYNC.md)) type the
server's address and the code RomM shows under **Client API Tokens → Pair
device**; the handheld exchanges the code for the token itself. Give each
device its own token, since pairing a token again moves it to the new device.
**From your phone** (under the two fields) shows a code that opens the Phone
Companion's **RomM server** card: type the address (a bare `192.168.1.20:8080`
gets `http://` added) and paste the token on the phone. The handheld asks the
server for one game with them before saving — a refused token or an address
with no RomM behind it is sent back as an error — and never shows the token to
the page again, only its last four characters.
For a scoped token, enable `roms.read`, `platforms.read`, `firmware.read`,
`assets.read`, `assets.write`, `devices.read` and `devices.write`
(`platforms.read` is only used to match card games for automatic sync).

The **Games** tab lists the server's catalogue in pages. A game opens actions to
download its ROM and cover into the card's `Roms/<console>/` and
`data/game-art/` folders. Multi-file disc games are unpacked together into a
game folder. Once the game is on the card, A can fetch its cover again or
**Sync saves**. The Games Library sees new ROMs after its next rescan.

The **BIOS files** tab downloads firmware from the same server into the card's
`BIOS/` folder. A file already present is left alone.

**Sync saves** on a game's sheet syncs that one game's RetroArch save files
now. JukaHub sends the game's `.srm`, `.sav` and state files (`.state`,
`.stateN`, `.state.auto`) to RomM's sync negotiation endpoint and follows its
upload or download decisions. An existing local save is backed up beside it
(`<save>.romm-backup-<time>`, the newest three are kept) before a newer server
copy replaces it. A conflict is kept as a `.romm-conflict` file for review.
Server-requested deletions never remove a save from the card. Close the game
before syncing so RetroArch cannot write over a newly received save.

## Automatic save sync

**Connection → Automatic save sync** (on by default once an address and a
token are set) syncs a library game's saves when it starts and when it ends:

- **Start (pull):** before the emulator opens, saves and states that RomM says
  are newer are downloaded, with the backup above. Uploads wait for the end.
  The launch waits a few seconds at most; a slow server is skipped and the game
  starts with what is on the card.
- **End (push):** saves the game wrote are uploaded, and anything RomM says is
  newer is downloaded. When both sides changed since the last sync, the
  server's copy is first kept on the card as `<save>.romm-conflict` and then
  the save you just played is uploaded over it (RomM keeps its own version
  history of the slot). A manual sync never overwrites in a conflict.
- Only games that exist on the server are synced; everything else is left
  alone. Only RetroArch save and state folders are synced (the folders in
  `retroarch.cfg`, else `RetroArch/saves` and `RetroArch/states`). Standalone
  emulators (PPSSPP, DraStic, …) are not covered yet.

**Sync all saves now** (same tab) walks every game in the Games Library, two
at a time, and syncs the ones on the server. It keeps running when you leave
the page; the row and the header show progress, B asks whether to stop or to
keep syncing in the background, and a notification sums it up at the end.

### How a card file is matched to a RomM game

1. A game downloaded through this page remembers its RomM id.
2. Otherwise JukaHub searches the server once, inside the RomM platform that
   matches the console (by platform slug, e.g. `gba`, `snes`, `psx`,
   `genesis-slash-megadrive`; by platform name for consoles such as Atari or
   WonderSwan), for the file name (the folder name for a multi-file game in its
   own folder). A result counts only when the file name and the size match
   exactly and, when RomM published a CRC32, MD5 or SHA-1 for it and the file
   is not an archive (`.zip`, `.7z`, `.chd`, …) and is at most 256 MB, that hash
   matches the file on the card too.
3. A game that is not on the server is not searched again for 12 hours, or
   until the file changes size. A renamed file on either side is not found;
   download it again through this page, or rename it to match.

The links are kept in `romm-autosync.json` in JukaHub's data folder (at most
5,000 games); a damaged file is replaced by an empty list. Changing the server
address starts a new list for that server.

## Server versions

Save sync uses RomM's device sync protocol (`/api/devices`,
`/api/sync/negotiate`, `/api/saves`, `/api/saves/{id}/content`,
`/api/saves/{id}/downloaded`, `/api/sync/sessions/{id}/complete`), documented
for RomM 5.2 and later
([5.4 API](https://docs.romm.app/5.4.0/developers/api-authentication/),
[device sync protocol](https://docs.romm.app/5.4.0/developers/device-sync-protocol/)).
Save content hashes are MD5, as RomM computes them, so an unchanged save is a
no-op rather than a conflict. A server without that protocol (RomM 3.x and
4.x) answers 404; JukaHub then says so once, keeps browsing and downloads
working, and does not retry auto-sync for an hour. Matching uses `/api/platforms`
and `/api/roms?search_term=…` with both `platform_ids` (RomM 4+) and
`platform_id` (3.x).

**Unverified:** none of this has been run against a real RomM server or on a
handheld yet. The connection and transfer paths are covered by a local test
server only. Specifically unverified: that `fs_size_bytes` of a multi-file game
is the sum of its files; that the CRC32 RomM stores is the plain 8-digit hex of
the file; that `/downloaded` accepts `{device_id, content_hash}` as sent; and
that uploading with `overwrite=true` after a conflict is accepted by 5.2/5.3.
