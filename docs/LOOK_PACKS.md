# Look packs, console icon packs, console names and the power-off screen

Everything here is in **Settings → Theme & look**, and nothing changes until you choose
it: the defaults are JukaHub as it always looked.

| Row | What it does |
| --- | --- |
| **Theme presets → Experience** | The packs gallery: one press applies a whole look, Y (U on a keyboard) takes it back. |
| **Console names** | International (Mega Drive, PC Engine, SNES), North America (Genesis, TurboGrafx-16, Super Nintendo), Japan (Super Famicom, Famicom, Mega-CD) or Short (MD, PCE, GBA). |
| **Console icons** | JukaHub's vector glyphs, or an icon pack from the card's `Icons` folder. |
| **Start on** | (Tabs Home) the tab Home opens on: the last used one, or always the same tab. |
| **Power-off screen** / **Power-off picture** | Glass (the shutdown card fades to black) or a picture held as the device turns off. |

## Look packs

A look pack is an **Experience pack** (the Themes gallery's Experience tab,
[THEMES.md](THEMES.md)) — the same `pack.json`, the same gallery, the same
Undo, the same `.jkpack` export — with a few extra parts.
Packs are read from JukaHub's data folder (`packs/`) and from the card:

```
<SD>/Themes/Packs/<Pack name>/
    pack.json        required
    preview.png      optional: shown on the gallery card instead of the drawn miniature
    wallpaper.jpg    optional, named by "wallpaper"
    bootlogo.png     optional, named by "bootLogo"
    theme.json       optional, named by "themeFile"
```

`pack.json` (every field is optional except `id`, which defaults to the folder
name):

| Field | Example | Meaning |
| --- | --- | --- |
| `id`, `name`, `description`, `author`, `version` | `"burst"` | Who the pack is. |
| `theme` | `"convert-snes"` | A built-in or installed theme preset id; `"default"` is JukaHub's own palette. |
| `themeFile` | `"theme.json"` | A theme file in the pack (the `themes/*.json` format). |
| `homeLayout` | `"tabs"` | `custom`, `dashboard`, `grid`, `carousel`, `widgets` or `tabs`. |
| `wallpaper` | `"wallpaper.jpg"` | A picture in the pack (PNG, JPEG, WebP, BMP, up to 12 MB), or `"none"`. |
| `sounds` | `{"enabled": true, "level": 60}` | Interface sounds. |
| `startupSound` | `false` | The start-up and shutdown chime. |
| `menuColour` (or `menuColor`) | `"coral"` | One of Menu colour's names (sky, mint, lime, amber, coral, rose, violet, indigo, teal, white) or `"theme"`. |
| `consoleNames` | `"japan"` | `international`, `na`, `japan` or `short` (`us`, `jp` are accepted). |
| `iconPack` | `"Burst!"` | A folder name in `<SD>/Icons`, or `"jukahub"` for the vector glyphs. |
| `bootLogo` | `"bootlogo.png"` | A PNG or JPEG in the pack (up to 8 MB, 4096×4096). See below. |
| `preview` | `"card.png"` | The card picture, when it is not `preview.png`. |
| `startScene`, `pinned`, `favorites`, `inputProfile`, `screensaver`, `scenes` | `"Games"` | The screen JukaHub opens on, Start-menu pins (up to 8), favourites (up to 16 streams or pack files), a control profile by name, the idle screen (`dock` or `stars`) and up to 4 extra screens. |

A field that does not check out — a file outside the pack, an unknown name, a
picture that is too big — is dropped and listed on the pack's card; the rest of
the pack still applies. The card lists, line by line, what applying it changes.

**Applying** sets every present field through the setting's own path and keeps
what it replaced; **Undo** puts all of it back, after a restart too.

**Boot logo.** Applying a pack never writes the boot logo. It stages the pack's
picture; **Settings → Boot logo** then says "A twice installs the look pack's
logo" and writes it through the same backed-up, verified path as the JukaHub
logo, fitted to the logo's size on black. If the JukaHub logo is installed,
restore the previous logo there first. Undo does not rewrite the boot logo.

Built-in packs (made only of built-in themes): **Super Famicom** (Super
Nintendo style, Japanese names), **Genesis** (Mega Drive style, North American
names) and **Pocket Tags** (OLED black, lime accent, short names). The built-in
**Default** pack also puts the names, glyphs and menu colour back to JukaHub's.

## Console icon packs

```
<SD>/Icons/<Pack name>/
    pack.json        optional: {"name": "Burst!", "author": "…"} (16 KB max)
    preview.png      optional
    Emus/            CrossMix's layout; a flat folder of pictures works too
        MD.png  FC.png  SFC.png  GBA.png  PS.png  …
```

A picture is named after its console in any of the usual ways, in any case:
the TrimUI `Emus` folder name (`FC`, `SFC`, `MD`, `GBA`, `PS`), JukaHub's name
(`Mega Drive.png`, `Game Boy Advance.png`), a short name (`gba.png`) or an
emulator (`mupen64plus.png`). PNG or JPEG, up to 4 MB and 4096×4096 each.
Consoles that share one glyph in JukaHub (Mega Drive, Sega CD and 32X; NES and
Famicom Disk System) show the main console's picture (`MD`, `FC`). A console
the pack has no picture for keeps its vector glyph. Pictures are scaled once
to the size they are drawn at, away from the screen's thread.

## Console names

Only the words on screen change: Games (console buttons, page header, game
rows), the Home tabs (console tiles, game tiles, Recent), search results and
the game options title. Folders, `jukauser.json` keys, per-console settings and
the Pixel names lettering keep the canonical names. Searching the library finds
a console by either name.

## Power-off screen

**Picture** ends a power-off or restart from Home's power card on one of:

- **See you soon** — the JukaHub wordmark and a goodbye by your name;
- **Clock and battery** — the time and the charge left;
- a PNG or JPEG of your own in `<SD>/Themes/Shutdown/` (screen-sized looks best;
  up to 4096×4096 and 8 MB).

The picture fades in after the shutdown card and is held about 1.2 s inside
the shutdown's time budget. Your own picture is decoded while the shutdown
runs; if it is not ready or cannot be read, See you soon is shown instead.
Quitting to the stock menu always ends on black.

## Settings keys

| Key | Default | Meaning |
| --- | --- | --- |
| `consoleNames` | `"international"` | `international`, `na`, `japan`, `short`. |
| `iconPack` | `""` | A folder in `<SD>/Icons`; empty is the vector glyphs. |
| `homeTabsStart` | unset | The tab Home opens on while `homeTabsRemember` is off (HOME_TABS.md). |
| `shutdownScreen` | `"glass"` | `glass` or `picture`. |
| `shutdownPicture` | `"builtin:farewell"` | `builtin:farewell`, `builtin:battery` or a file name in `<SD>/Themes/Shutdown`. |
| `lookpackBootLogo` | unset | Bookkeeping: the boot logo the applied pack staged. |
