# Quick menu

The Quick menu is the menu on the TrimUI button: one full
screen with big tiles for where you go most and small round buttons for the
switches you need most.

- **Top row (tiles):** Recents, Collections, Games, Tools.
- **Bottom row (buttons):** Wi-Fi, Sleep, Restart, Power off, Settings.

| Button | Action |
|--------|--------|
| D-pad | Move between tiles and buttons (Up / Down change rows) |
| L1 / R1 | First / last entry of the row |
| A | Open the tile, or press the button |
| B | Close (back to where you were) |

## What each entry opens

| Entry | Opens |
|-------|-------|
| Recents | The [Game switcher](CONTROLS.md#game-switcher): your recent games, each where you left it. Only shown while Settings → Games & emulators → Game switcher is on |
| Collections | Home on its Collections tab ([Tabs Home](HOME_TABS.md), [Collections](COLLECTIONS.md)). Only shown when Home uses the Tabs layout, the Collections tab is shown and you have made at least one collection |
| Games | The games library |
| Tools | The Tools hub |
| Wi-Fi | Switches Wi-Fi on or off (the same switch as Home's Wi-Fi tile). The button glows while Wi-Fi is on; focused, its label names the network |
| Sleep | Sleeps the handheld at once |
| Restart / Power off | Restart or switch off the handheld. While Settings → Power & battery → **Ask before power off** is on (the default) a question comes first |
| Settings | Settings |

Screens the Quick menu opens sit on top of it: B on them comes back to the
Quick menu. On a computer there is no Sleep, Restart or Power off; an **Exit**
button opens the power card (Cancel / Exit) instead.

## Ways to open it

- Start → Device → **Quick menu** (and so also a Home tile from Edit Home, and
  Home search).
- Settings → Controls → **Menu button on Home** → *Quick menu*: a tap of Menu
  on Home opens it and a tap on the Quick menu closes it. Holding Menu still
  talks to Juka while **Menu button: voice** is on.
- The Brick's **F1 key** / **F2 key** and the Smart Pro S's **Home key** (Settings
  → Controls) can be set to *Quick menu*.
- Settings → Power & battery → **Start in** → *Quick menu* opens it when the
  handheld is switched on.
- A control profile can map any button or chord to `action:quick_menu`.

## Theme pictures

JukaHub reads NextUI's Quick menu pictures, so a NextUI theme's files work as
they are. The focused entry's picture fills the screen; an entry without one
keeps JukaHub's own background.

Put the pictures in `.media` at the root of the SD card (NextUI's place), or in
JukaHub's data folder under `quickmenu/` for pictures only JukaHub should use.
The card's `.media` wins when both have one.

| Entry | File |
|-------|------|
| Recents | `quick_Recents.png` |
| Collections | `quick_Collections.png` |
| Games | `quick_Games.png` |
| Tools | `quick_Tools.png` |
| Wi-Fi | `quick_Wifi.png` |
| Sleep | `quick_Sleep.png` |
| Restart | `quick_Reboot.png` |
| Power off (and Exit) | `quick_Poweroff.png` |
| Settings | `quick_Settings.png` |

`.jpg` / `.jpeg` work too, and the name may be all lower case
(`quick_games.png`). Pictures are scaled to cover the screen and cropped at the
edges; one the size of the screen (1024 × 768 on a Brick, 1280 × 720 on a Smart
Pro) looks best.

Settings → Controls → **Quick menu: Backgrounds only** hides the tiles and
buttons for every entry that has a picture and shows just the picture with a
small label naming the entry (for themes whose pictures draw the menu
themselves). Entries without a picture keep the tiles.

## Busy chips

While Wi-Fi or Bluetooth is coming on — from the Quick menu, Quick Settings,
Home's tiles or the Wi-Fi page — the activity strip at the top right shows
"Turning on Wi-Fi…" or "Turning on Bluetooth…" with a spinner, until the radio
is up (Wi-Fi: until it has joined a network, at most about 12 seconds).
