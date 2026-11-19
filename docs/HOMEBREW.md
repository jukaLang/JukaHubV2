# Get Homebrew

**Start → Games → Get Homebrew** (also the Games tab, Home's Store folder, the
**More stores** strip on Packages and Home search: "homebrew", "itch",
"gb studio") brings new games for ten consoles from
[itch.io](https://itch.io) — where most of today's homebrew is released — and
puts them on their shelves in the [Games Library](GAME_LIBRARY.md), covers and
all.

| Console | Shelf | Files it installs |
|---------|-------|-------------------|
| Game Boy | Game Boy | `.gb` |
| Game Boy Color | Game Boy Color | `.gbc` |
| Game Boy Advance | Game Boy Advance | `.gba` |
| NES | NES | `.nes` |
| SNES | SNES | `.sfc`, `.smc` |
| Mega Drive / Genesis | Mega Drive | `.md`, `.gen`, `.smd`, `.bin` |
| Master System | Master System | `.sms` |
| Game Gear | Game Gear | `.gg` |
| PC Engine / TurboGrafx-16 | PC Engine | `.pce` |
| PICO-8 | PICO-8 | `.p8.png`, `.p8` carts |

## Browsing

Browsing needs no account. The list is itch.io's own browse pages for each
console (several tags each — "Game Boy", "made with GB Studio", "PICO-8" …),
merged so a game tagged twice shows once.

- **Console chips** — All, or one of the ten. **L1 / R1** step through them.
- **Sort** — **Popular**, **Newest** and **Top rated** are itch.io's own
  orders (the lists for Newest and Top rated load the first time you pick
  them); **Free** shows only free games; **Yours** shows the games you bought
  on itch.io and the ones you installed here.
- **Search** — narrows the loaded list by title, author and description as you
  type (**/**, **S** or **F** on a keyboard).

A card shows the cover, the title, the author and a pill: **Free**, the price,
**Bought**, **Installed** or **Update**. The lists are saved for 12 hours, so
opening the store again is instant; without Wi-Fi it shows the saved lists and
says from when. **Y → Reload lists** reads them again.

Itch.io's tags are chosen by the game makers, so a list can include a game
that was only made *in the style* of a console (a PC game with Game Boy
colours). The game page says so when its files have no cartridge.

## A game's page

**A** on a card opens its page: the cover, the author, the consoles it is
listed under, when it came out and was updated, the price line and the
description. With your key it also names the file it would install and its
size. Buttons:

- **Install** — free games, and paid games you bought.
- **Buy on itch.io** — a paid game you do not own: a code to its page. Buy it
  there with your account, then press **I bought it**; it installs like a free
  game. JukaHub never downloads a paid game you have not bought.
- **Play** — starts it with the console's emulator (the same choice of
  emulator as the library).
- **Update** — when itch.io has a newer file.
- **Delete** — removes the game file and the cover Get Homebrew saved, after
  you confirm. Saves and save states stay.
- **Page on itch.io** — a code to the game's page.

**L1 / R1** on the page step to the next game of the list. **X** on a card
installs (or updates) without opening it.

## Your itch.io key

itch.io serves a game's files only to an account — free games too — so
installing needs your own API key:

1. Sign in on itch.io and open **Settings → API keys**
   ([itch.io/user/settings/api-keys](https://itch.io/user/settings/api-keys)).
2. **Generate new API key** and copy it.
3. Send it to the handheld:
   - **From your phone** — the store's **Add itch.io key** chip (or the page's
     **Add itch.io key** button) shows a code that opens the Phone Companion
     page at its **itch.io** card. Paste the key
     there and tap **Save on handheld**.
   - **On the handheld** — **Type it** opens the on-screen keyboard. Nothing is
     saved until you press **Save**; the field is masked and the word strip
     stays off.

The handheld checks the key with itch.io before it saves it and shows whose it
is ("Signed in as …"). It is stored in `data/homebrew-store/itch-key.json`
(readable only by JukaHub, encrypted when the system allows), never logged and
never shown again — the phone page only learns whether one is saved, its last
four characters and the account name. **Remove key** on the key sheet deletes
it; you can also revoke it on itch.io at any time.

## What installs where

- **The cartridge.** JukaHub asks itch.io for the game's files and picks the
  one for the console: the console's own file type first, a `.zip` holding one
  next (only the cartridge is taken out of it). Windows, Mac, Linux and Android
  builds, web versions, soundtracks and pre-orders are skipped.
- **The folder.** The console's folder under `Roms` — the one your card
  already has for it (`Roms/GB`, CrossMix's names, NextUI's
  `Game Boy (GB)` …), else the firmware's name (`GB`, `GBC`, `GBA`, `FC`,
  `SFC`, `MD`, `MS`, `GG`, `PCE`, `PICO8`).
- **The name.** The game's title as a file name — `Tobu Tobu Girl.gbc` — with
  `(2)` added when another file already has that name.
- **The cover.** Saved as a PNG in `data/game-art/<console>/`, where the
  library looks first, so the game shows on its shelf with its cover. A cover
  you chose yourself is never replaced.

Downloads are shown on the status strip (its **X** stops one) and go one at a
time. Sizes are capped (a cartridge at 64 MB, a zip at 256 MB), free space is
checked first, and a file only takes its real name once it is complete — a
power cut mid-download leaves no half game on the shelf.

## Updates

Every install is remembered in `data/homebrew-store/installed.json`: the game,
the file itch.io served, its date, checksum and build, where it went and its
cover. JukaHub compares that with itch.io:

- when you open Get Homebrew (at most every half hour), and
- once a day in the background while the handheld is online (never during
  start-up).

A game with a newer file shows **Update** on its card, and **Update all**
appears beside the sort. An update replaces the file **under the same name**,
so your saves and save states — which follow the ROM's name — keep working.

## Files

| Path | What |
|------|------|
| `data/homebrew-store/feeds/` | Saved browse lists (12 hours) |
| `data/homebrew-store/covers/` | Card pictures (pruned past 30 MB) |
| `data/homebrew-store/games.json` | Game ids, authors and covers from itch.io |
| `data/homebrew-store/installed.json` | What was installed, for updates |
| `data/homebrew-store/itch-key.json` | Your API key (mode 0600) |
| `data/homebrew-store/incoming/` | A zip while its cartridge is taken out |

## Limits

- Search narrows what is loaded; it does not search all of itch.io.
- Games whose only download is a PC or web build cannot be installed: there
  is no cartridge to put on the shelf.
- itch.io's QR sign-in for handhelds needs an app registered with itch.io;
  until JukaHub has one, the API key is the way in.

See also: [Games Library](GAME_LIBRARY.md), [PortMaster and the Ports
Store](PORTMASTER.md), [RomM server](ROMM_SERVER.md).
