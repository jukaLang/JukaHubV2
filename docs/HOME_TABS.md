# The Tabs Home

The Tabs Home is JukaHub's default Home (JukaOS v2). The top bar keeps the
JukaHub wordmark on its left and the system controls — brightness, volume,
Bluetooth, Wi-Fi, weather, clock, trophies, Report a bug, notifications — on
its right. Between them, in the middle, sits a strip of tabs; both of its ends
fade out so it melts into any wallpaper or liquid background. Each tab is a
page of apps or games:

| Tab | What it holds |
| --- | --- |
| **Recent** | What Continue resumes, the games you played last (with their covers), then the apps you opened last. |
| **Main** | The games you pinned to Home (with a *Pinned* badge), then Settings, Trophies (JukaHub's own achievements), RetroAchievements (where supported), Chat, Browser, the **Store**, Tools, Files, Bluetooth and Wi-Fi where the device has them, and Phone (the companion's pairing code) while it is running. |
| **Games** | Your consoles (with covers and game counts), JukaHub's own games, PS / Xbox Remote Play, Steam, Minecraft, PortMaster, Windows Apps, the Emulators page and the whole Games library. |
| **Entertainment** | YouTube, Shorts, Live TV, Downloaded (saved videos and music), Music, Podcasts and Game clips. |
| **Favourites** | The games and apps you marked as favourites. |
| **Collections** | Your own collections of games and apps. |
| **Tools** (off until switched on) | Every screen, tool and plugin button the Start menu lists — Files, Settings, Music, YouTube, Notes, the Terminal, Downloads, Network tools, Screen Share… — opened the way the Start menu opens them. Switch it on in the arranging sheet (L2 → Y on Tools). |

The **Store** in Main is a folder: Android apps (APK Store), Packages,
PortMaster (not on Windows), Windows Apps, Downloads, Themes and Wallpapers.
A collection is a folder too.

Users on a layout they picked before (Standard, Hero grid, Carousel, Widgets,
or a console theme such as PS5 Style) keep it; Settings → Theme & look →
Home layout switches to Tabs and back.

## Controls

| Pad | Keyboard | Does |
| --- | --- | --- |
| L1 / R1 | Shift+Tab / Tab, PgUp / PgDn | Previous / next tab (wraps round) |
| D-pad | Arrows | Move through the page; Up from the top row reaches the tab strip |
| Left / Right on the strip | Arrows | Change tab |
| A | Enter | Open the item; a folder opens as a page of its own |
| A on the strip | Enter | Change the page's view |
| B | Esc | Leave a folder for the tab it is in |
| X | X | The item's options: favourite, add to a collection… |
| Y | / | Search |
| L2 | Q | Arrange the tabs |
| R2 | W | Liquid background level |
| Start | F10 | Start menu |

With a mouse or a touch screen: tap a tab to open it (tap the active one to
change its view), tap a tile to open it, right-click a tile for its options,
and the wheel walks the page.

Each tab remembers where its focus was, and Home opens on the tab you left it
on (Settings → Theme & look → Remember last tab).

## Views

Every tab — and every folder, each collection on its own — has a view:

- **Grid** — larger square tiles that center when a page has only a few items;
  fuller pages scroll up and down (the default).
- **Rows** — large tiles in one or two rows, scrolls left and right (Recent's
  default, with its first item as a large hero).
- **List** — a line per item, the focused one large beside the list.
- **Carousel** — one big tile in the middle, its neighbours smaller.
- **Compact** — small icons, many per row.
- **Backdrop** — a full background: the selected game fills the
  screen behind Home, dimmed (its screenshot from the art folder's `.snap`,
  else its newest save-state picture — where you left it — else its box art),
  with its box art large and its name beside it, over a slim row of its
  neighbours that Left / Right walk. An app or a console keeps Home's own
  background. The picture is looked up once the selection rests on a game for
  a moment, and fades in over the last one.
- **Minimal** — big names only, one per line, no art; the selected name in the
  accent colour.

A on the strip (or a tap on the active tab) steps the page's view; the
arranging sheet (L2) sets each tab's.

**Grid selection ring** (Settings → Theme & look, and the arranging sheet; on
by default) draws the ring and glow round the selected tile of the tile views.
Off, the selected tile stands out by its lift and — with Dim unselected art —
by being the one cover in full colour.

## Games, Favourites and Collections

**Games** shows every console of your game library as a tile — its name, how
many games, and a cover from it — with the consoles you played most recently
first and the rest in the library's usual order. A opens the Games library
straight on that console's games. After the consoles come JukaGames (the games
built into JukaHub), Remote Play for PlayStation and Xbox, Steam, Minecraft,
PortMaster (handheld), Windows Apps, the Emulators page and the whole Games
library.

JukaHub tidies the consoles for you: a console with no games is not shown, and
a console that has games but no emulator to run them is dimmed with an
**Install** badge — A opens the Emulators page on it, where X installs it. To
see every console the device can play, empty or not, turn off Settings → Games
& emulators → **Hide emulators without games**.

**X on any item** (any tab) opens its sheet: Play / Open, Add to (or Remove
from) Favourites, Add to collection…, on a game Pin to Home / Unpin from Home
and — while it is a recent game — Remove from recents, and on a collection:
Move earlier / later and Remove from the collection; on a collection's folder:
Rename and Delete. B goes back, X closes. Keyboard: arrows, Enter, Esc.

The same game rows are in the Games library's game options (Start on a game:
**Pin to Home**, **Add to collection…**, **Remove from recents**) and in the
game page's **More** sheet. A pinned game leads the Main tab here and is a
cover tile on the Carousel and Widgets Homes (`game:<path>` in `homeTiles`,
[CONFIGURATION.md](CONFIGURATION.md#home-tiles-hometiles)); A starts it the
way the library does. Renaming the game's file moves its pin, deleting the
game removes it. Remove from recents takes it off Recent, the Continue card
and the game switcher; its saves and states stay.

**Favourites** lists your favourite games A–Z, then everything else you
favourited in the order you added it, then *Saved videos* (the media Favorites
screen) when it has anything. A game's favourite is its Favorite tag, so it is
the same favourite in the Games library (L3 → Favorite).

**Collections** starts with *New collection*. Each collection is a folder; open
it with A and pick its view (grid, row, list…) like any page — every
collection remembers its own. Items that have since disappeared stay in the
list, dimmed as *Missing*, until you remove them. Before your collections come
the automatic, read-only ones — *Last played*, *Most played*, *All games*,
*Recently added* and *Never played* by default, and *Multiplayer* and one per
genre when switched on in Settings → Games & emulators → Automatic collections
([Automatic collections](COLLECTIONS.md)). On a short page, a heading explains
that these lists update automatically.

## Arranging the tabs

L2 (Q), Start → Device → Edit Home or Settings → Theme & look → Tabs open
a sheet over Home:

- Up / Down choose a tab; A picks it up and Up / Down carry it to another
  place; A or B puts it down.
- Left / Right change the tab's view.
- Y (Delete) hides the tab or shows it again; one tab always stays. Tools is
  listed hidden until you show it.
- The rows after the tabs switch Remember last tab, Tabs position, Dim
  unselected art, List edge fade, Grid selection ring and the Home layout.
- B or L2 (Esc or Q) close the sheet.

Changes are saved at once.

## Settings keys

Stored in `jukauser.json` (the `Custom` variables):

| Key | Example | Meaning |
| --- | --- | --- |
| `homeArrangement` | `"tabs"` | The Home layout. Unset means Tabs; `"custom"` is Standard. `"jukaos"` and `"v2"` also mean Tabs. |
| `homeTabViews` | `"games=row;main=grid;col:abc=list;store=compact"` | The view of each tab (by tab ID) and folder (`store`, `col:<id>`). Views: `grid`, `row`, `list`, `carousel`, `compact`, `backdrop`, `minimal`. |
| `homeTabsShown` | `"recent,main,games,-entertainment,favourites,collections,tools"` | The tabs in their order; `-` hides one. A tab the value does not name is added at the end — shown, except `tools`, which stays hidden until named. |
| `homeTabsGridRing` | `"false"` | Grid selection ring: the ring round the selected tile (default on). |
| `homeTabsRemember` | `"true"` | Open on the last tab (default on). Off, Home opens on the `homeTabsStart` tab, else Main. |
| `homeTabsStart` | `"games"` | Settings → Theme & look → Start on: the tab Home opens on while `homeTabsRemember` is off. Choosing "Last used tab" there turns `homeTabsRemember` on; choosing a tab turns it off. Unset means Main (the old behaviour). |
| `homeTabLast` | `"games"` | The tab to come back to (bookkeeping, not exported as a setting). |
| `homeTabsEmuClean` | `"auto"` | Games tab: `auto` (default) hides consoles with no games and badges the ones without an emulator; `all` lists every console with an emulator. Settings → Games & emulators → Hide emulators without games. |

Tab IDs: `recent`, `main`, `games`, `entertainment`, `favourites`,
`collections`, `tools`.

Collections, and favourites that are not games, are kept in
`collections.json` in the data folder, written atomically with a `.bak`
(format and limits in
[CONFIGURATION.md](CONFIGURATION.md#other-json-stores)).
