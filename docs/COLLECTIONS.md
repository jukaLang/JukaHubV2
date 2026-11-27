# Automatic collections

JukaHub keeps a few game lists up to date by itself: the games you played
last, the ones you play most, every game in one list, the games for two
players, and the three lists from your game statuses — Backlog, Playing now
and Beaten this year. They are folders on the **Collections** tab of the
Tabs Home ([The Tabs Home](HOME_TABS.md)). They are new and **not yet checked on
a TrimUI**.

## The folders

| Folder | What is in it | On by default |
| --- | --- | --- |
| **Last played** | The 30 games you started last, newest first. | Yes |
| **Most played** | The 30 games with the most hours. | Yes |
| **All games** | Every game of every console in one list, A to Z. | Yes |
| **Recently added** | The 30 games that turned up on the card last. | Yes |
| **Never played** | Up to 120 games you have not started yet, A to Z. | Yes |
| **Backlog** | Games tagged Backlog on their game page, waiting longest first. | Yes |
| **Playing now** | Games tagged Playing, most recently played first. | Yes |
| **Beaten this year** | Games tagged Completed this year, newest first. | Yes |
| **Multiplayer** | Games for two players or more. | No |
| **Genres** | One folder per genre with at least three games (Platform, Racing…), up to 24. | No |

They sit after *New collection* and before your own collections, with an
*Auto* badge. They are read-only: they cannot be renamed, deleted or edited, but
each remembers its own view (grid, row, list…) like any collection, and its
**X** sheet can add it to your favourites. Hidden games are left out unless the library shows
hidden games, and in Kid mode only the kids' games are listed
([Kid mode](KID_MODE.md)).

**Multiplayer** and the **genre** folders come from a game's details: the genre
and player count JukaHub looks up in the libretro database when you open the
game's page online ([Game library](GAME_LIBRARY.md)). A game whose page was
never opened online has no details yet, so these folders grow as you browse.

**Backlog**, **Playing now** and **Beaten this year** read the tags you set on
the game page (its Tags button). They need no download and no scan: marking a
game Backlog or Playing puts it in the folder at once. **Beaten this year**
uses the date the game was first tagged Completed, so a game you finished last
year stays out until the new year. Each folder holds at most 30 games.

Games Library → **Select → Next up from your backlog** opens the backlog game
that has waited longest, so the Backlog folder doubles as a queue. The menu
row names the suggestion and its wait; a tie is broken by title. The status is
also shown on the library's list rows and grid cards, beside the console or
the file name.

The game page's **Tags** button offers six statuses: Favorite, Playing,
Completed, Backlog, Wishlist and Dropped. When a game carries more than one,
the card shows the most active one — Playing, Backlog, Wishlist, Dropped,
Completed, then Favorite. Only Backlog and Playing feed a folder; Wishlist and
Dropped are for your own record.

The phone's **Games** card can set the same status, a 1–5 rating and a note:
tap a game and use the Journal fields. Both ends read and write the game
page's own record, so a note typed on the phone is on the handheld at once.
The phone routes (`/api/games/journal`) sit behind the companion PIN like the
rest of that card.

## Turning them on and off

Settings → Games & emulators → **Automatic collections** opens a list with one
row per folder; **A** turns the row's folder on or off, **B** closes the list.
**Genres** stands for every genre folder at once. A folder you turn off leaves
the tab; if you had favourited it, the favourite still opens it.

## Why the order does not trust the clock

A TrimUI handheld starts before it knows the time (it sets its clock from the
internet), and a card filled from a PC keeps the PC's file dates. So:

- **Last played** follows a counter JukaHub moves on every launch, not the time
  of the launch. Games you played before this existed follow, in the order of
  the Continue list.
- **Recently added** follows JukaHub's own library scans: each game is filed
  under the scan that first found it. The games already on the card the first
  time are the starting point and never count as recent. A game is recognised
  by its contents, so renaming or moving a file does not make it new again.

Both live in `game-seen.json` in JukaHub's data folder. Deleting the file only
starts both lists over (the games on the card become the new starting point).

## Play count

A game's page shows **Played 5 times** (or *Played once*) in its details
column: how many times it was started from JukaHub, the same count the page's
session history keeps.

## The Games library

The Games library already has these views: its *Continue* row is the last
played games, and its **Library sort** (in a game's options sheet) offers Last
played, Playtime and Recently added per console, so the automatic folders live
on the Tabs Home only.

## NextUI collections

NextUI (and MinUI) keep a collection as a text file, `Collections/<name>.txt`
on the card, one game per line as a path from the card root
(`/Roms/Game Boy (GB)/Tetris.gb`). **Settings → Games & emulators → Share
collections with NextUI** (on by itself where the card has a `Collections`
folder or NextUI) makes them the same lists in both
(`player/collections_nextui.go`):

- each `.txt` is a collection here, named after the file, in the file's order;
  a line whose game is not on the card is not shown but is kept at its place,
  so nothing NextUI listed is lost when JukaHub writes the file back;
- a change here writes the file back through a temp file and a rename, in the
  file's own line endings and path style;
- a collection made here gets its own `.txt`;
- a file changed outside JukaHub (in NextUI, on a PC) is read again rather
  than overwritten: its size, time and SHA-256 are kept in
  `collections-nextui.json` in the data folder;
- deleting a collection here moves its file to `Collections/.deleted`;
  deleting the file elsewhere only unlinks the collection, which stays here
  and is not written back to the folder until you change it here.

Apps, consoles and games off the card stay in the JukaHub collection only, and
the automatic collections above are never written out. Device Sync merges
collections between two paired handhelds ([DEVICE_SYNC.md](DEVICE_SYNC.md)).
