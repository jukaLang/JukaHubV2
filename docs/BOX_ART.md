# Box art sources

JukaHub finds a cover for each game in the library on its own (Settings →
Games & emulators → **Find box art automatically**, and the library's Fill
gaps). It asks up to three sources, in this order:

1. **ScreenScraper**, when you have signed in (below). It identifies the game
   by its checksum, size, console and file name and also fills the game's
   year, genre, developer, players and synopsis where they are empty. A
   description you wrote yourself is never replaced.
2. **The libretro thumbnail server** (thumbnails.libretro.com), matched by
   name: the file's name, the game database's name for its checksum, the
   closest title, a sister console, then title screens and in-game pictures.
3. **The LaunchBox Games Database** (gamesdb.launchbox-app.com) for a game the
   thumbnail server has nothing for: the title is searched on the game's
   console and the front box is taken in the region the file's name asks for
   (USA, Europe, Japan …).

A game none of them knows is not asked again for a while (two weeks for the
thumbnail server, a month for LaunchBox and ScreenScraper); deleting the
`data/game-art` folder starts every cover and every "not found" over. A cover
the firmware or you put on the card (an `Imgs` or `.res` folder) is always
shown as it is and never looked up.

## Choosing a cover by hand

Game options → **Choose cover** lists the thumbnail server's closest covers
and the pictures in the console's `Imgs` folder. Its last row, **Search
LaunchBox…**, asks for a title (the game's own is filled in), lists the
LaunchBox matches on the game's console, shows the highlighted one's picture,
and **A** saves its front box as the game's cover.

## ScreenScraper account

Settings → Games & emulators → **ScreenScraper account** asks for your user
name and then your password on a masked keyboard. The account is checked with
ScreenScraper before it is kept in `data/screenscraper.json` (readable only by
JukaHub's user); neither is ever written to a log. **Sign out of
ScreenScraper** forgets it. A free account at screenscraper.fr is enough; it
comes with a daily number of lookups, and when it is used up (or ScreenScraper
is busy) JukaHub waits and the other sources carry on meanwhile.

A build of JukaHub also needs ScreenScraper's developer key. Release builds
get it from the build variables below; a build without them shows the row as
**Not in this build**.

| Build variable           | Stamped into                    |
| ------------------------ | ------------------------------- |
| `JUKAHUB_SS_DEVID`       | `main.screenScraperDevID`       |
| `JUKAHUB_SS_DEVPASSWORD` | `main.screenScraperDevPassword` |

`build.bat`, `release.sh` and the handheld build (`.github/scripts/trimui-build.sh`,
and the workflow's repository secrets of the same names) pass them as
`-ldflags "-X main.screenScraperDevID=… -X main.screenScraperDevPassword=…"`,
like the Google and Microsoft sign-in clients.

## Games in .7z archives

A game kept in a `.7z` archive is treated like one in a `.zip`: its CRC32 is
read from the archive's directory (nothing is unpacked) for the cover match,
the game details and the ROM manager, and a Game Boy game in a `.7z` is
checked for the Game Boy Color flag by unpacking only its first 336 bytes.
This works for archives packed with LZMA, LZMA2 or no compression (what 7-Zip
uses by default); others are listed as before and simply not looked into.
