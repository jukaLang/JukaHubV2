# RomM server

**Tools → RomM server** connects the handheld to a RomM server you run. Enter
its full `http://` or `https://` address and a RomM **Client API Token** with
read and write access under **Connection**, then choose **Connect and load games**.
The token is stored in JukaHub's local data folder, is hidden on the connection
screen, and is sent to your server as a bearer token.
For a scoped token, enable `roms.read`, `firmware.read`, `assets.read`,
`assets.write`, `devices.read` and `devices.write`.

The **Games** tab lists the server's catalogue in pages. A game opens actions to
download its ROM and cover into the card's `Roms/<console>/` and
`data/game-art/` folders. Multi-file disc games are unpacked together into a
game folder. Once the game is on the card, A can fetch its cover again or
**Sync saves**. The Games Library sees new ROMs after its next rescan.

The **BIOS files** tab downloads firmware from the same server into the card's
`BIOS/` folder. A file already present is left alone.

**Sync saves** is a manual, per-game action for RetroArch save files. JukaHub
sends the selected game's `.srm`, `.sav` and state files to RomM's sync
negotiation endpoint and follows its upload or download decisions. An existing
local save is backed up beside it before a newer server copy replaces it. A
conflict is kept as a `.romm-conflict` file for review. Server-requested
deletions never remove a save from the card. Close the game before syncing so
RetroArch cannot write over a newly received save.

RomM 5.4's [API](https://docs.romm.app/5.4.0/developers/api-authentication/)
defines the client token and its
[device sync protocol](https://docs.romm.app/5.4.0/developers/device-sync-protocol/).
The connection and transfer paths are covered by a local test server; they
still need a real server and a Brick for end-to-end verification.
