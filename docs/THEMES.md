# Theme files

Theme files let you add a palette without editing JukaHub. Save one JSON file
per theme in the installation's `themes/` directory, for example
`themes/paper.json`. The file name without `.json` is the theme ID; `name` is
the label shown in the gallery. Built-in IDs take precedence, and an invalid
file is skipped.

Every color field below is required. Colors accept `#RRGGBB` or `#RRGGBBAA`;
the final byte in the second form is alpha. The optional `bloom` and `bloom2`
fields accept only opaque `#RRGGBB` colors.

| JSON key | Purpose |
|----------|---------|
| `name` | Required display name. |
| `background` | Base scene color; also selects the light or dark glass tint. |
| `buttonColor` | Tint used by authored buttons. |
| `labelColor` | General label color. |
| `inputColor` | Text-entry field surface. |
| `surface` | Main panel and card surface. |
| `surfaceAlt` | Alternate surface for secondary rows and sections. |
| `surfaceRaised` | Raised surface for cards above a panel. |
| `textPrimary` | Main headings and important values. |
| `textSecondary` | Body text and ordinary labels. |
| `textTertiary` | Captions, hints and quiet status text. |
| `borderSubtle` | Low-emphasis dividers and resting edges. |
| `borderDefault` | Standard card and control outlines. |
| `borderFocus` | Focused control outline. |
| `success` | Positive status fill or icon. |
| `warning` | Caution status fill or icon. |
| `danger` | Error or destructive status fill or icon. |
| `info` | Informational status color and the accent for focus rings and highlights. |
| `overlay` | Scrim color behind modal panels. |
| `summary` | Optional short note shown for a total-conversion theme. |
| `arrangement` | Optional Home layout. `dashboard`, `grid`, `carousel` and `widgets` make a total conversion; blank, `custom` or an unknown value leaves the layout alone. |
| `bloom` | Optional color for the first glow in Home's backdrop. |
| `bloom2` | Optional color for the second glow in Home's backdrop. |
| `carouselStyle` | Optional shape of the Carousel layout's row: `shelf` (small tiles along the top-left, the focused one larger with its name beside it), `strip` (one row of large equal squares, name above the focused one, no dots), `switch` (Switch 2 Style's home screen: the strip led by your recent games, a line of round buttons under it, your picture in the corner and the console's footer) `hub` (PS5 Style's welcome hub: a row of small icon tiles under the header, then trophy and library pills and a board of info cards), or `text` (Minimal UI: no tiles at all, just a list of names walked with Up and Down; aliases `minimal`, `list`). Blank or unknown keeps the centred carousel. Only visible with `"arrangement": "carousel"`. |
| `tileRadius` | Optional corner radius of Home's tiles and the shared cards, in 1280x720 design pixels, `0` to `32`. `0` or missing keeps 20. |
| `focusRing` | Optional `#RRGGBB` color for the ring around a focused Home tile. Missing uses `info`. |
| `focusRing2` | Optional `#RRGGBB` color the ring pulses toward for about two seconds after each move. Needs `focusRing`; the pulse stays off with reduced motion. |

Example:

```json
{
  "name": "Paper",
  "background": "#f4efe4",
  "buttonColor": "#e2d8c5",
  "labelColor": "#2b2924",
  "inputColor": "#ebe5d8",
  "surface": "#faf8f2",
  "surfaceAlt": "#eee9de",
  "surfaceRaised": "#e5dfd1",
  "textPrimary": "#241f18",
  "textSecondary": "#484238",
  "textTertiary": "#625b50",
  "borderSubtle": "#00000012",
  "borderDefault": "#c4baa7",
  "borderFocus": "#245d68",
  "success": "#2f6b3f",
  "warning": "#7f5900",
  "danger": "#a23a2a",
  "info": "#245d68",
  "overlay": "#f4efe4cc",
  "summary": "A warm paper palette",
  "arrangement": "grid",
  "bloom": "#e6c77a",
  "bloom2": "#c78c66"
}
```

## Console styles

Theme presets has four tabs: Colour, Total conversion, Consoles and
Experience. The Consoles tab gathers the console-inspired looks; the Total
conversion tab keeps the rest. They use colors, tile shapes, the Home
arrangement and the focus treatment only, with no logos, fonts or artwork. Each
one also has a plain palette in the Colour tab, which repaints without moving
any tile.

| Theme | Palette in the Colour tab | Home | Look |
|-------|---------------------------|------|------|
| PS5 Style | PS5 Night | Carousel, `hub` | The welcome hub on a near-black page. "Games  Media" heads Home (a tap on Media opens it). Your Home tiles are one row of small rounded icon tiles; the focused one stands larger at the left with its name beside it, and the row ends at both sides. Under it, two pills (trophy progress, the game library) and a board of flat graphite cards: battery, Continue with its artwork, Chat, Trophies by tier, the last game played, Packages, Storage, Now playing and today's playtime. Down enters the board, A opens what a card shows, white focus ring, 10 px corners. Edit Home arranges the row and hides the board while open. |
| PS4 Style | PS4 Night | Carousel, `shelf` | Near-black navy with a deep-blue glow. Small 14 px rounded tiles run along the top-left; the focused tile is larger, with a white ring and its name and description beside it. (This was PS5 Style before the welcome hub.) |
| PS1 Style | PS1 Grey | Hero grid | The console's light grey; a cross-blue focus ring that pulses toward square pink, 6 px tiles. |
| PS2 Style | PS2 Black | Carousel | Black and deep indigo under a blue glow, 10 px tiles. |
| PSP Style | PSP Waves | Carousel, `shelf` | The handheld menu's blue with a lighter wave glow, white ring, 12 px tiles. |
| Switch 2 Style | Switch Light | Carousel, `switch` | The console's home screen on light grey. Your picture sits in the top-left corner (A opens Trophies), the time, signal and battery in the top-right. One row of square tiles with 6 px corners: the games you played last come first, each its box art edge to edge, then your Home tiles as colour squares. The row scrolls only at its edges; the focused tile's name sits above it in cyan and a cyan outline pulses after each move. Under the row is a centred line of round buttons: Games, Chat (with its unread count), Store, Search, PlayStation Remote Play (on the handheld), Xbox Remote Play, Phone Companion, Settings and Power. A hairline, a controller glyph and the Start and A hints close the screen. The greeting, search pill, Remote Play pills, pairing code and power pill give way to the round buttons. Edit Home edits the tiles with its usual strip. |
| Switch 2 Style Dark | Switch Dark | Carousel, `switch` | The same home screen on charcoal grey. |
| Super Nintendo Style | Super Nintendo | Hero grid | Lavender grey with purple buttons; the Japanese pad's four colours as status colours. |
| Nintendo 64 Style | Nintendo 64 | Widgets | Charcoal with the four emblem colours; a yellow ring pulsing to green, 6 px tiles. |
| GameCube Style | GameCube Indigo | Dashboard | Deep indigo, 14 px tiles. |
| Game Boy | Game Boy | Hero grid | The four DMG greens. |
| Xbox Style | Xbox Green | Widgets | Dark charcoal with green highlights and glow. The Widgets grid uses 8 px rounded rectangles with a white focus ring. |
| Xbox One Style | Xbox One | Hero grid | The 2013 Metro dashboard: near-black backdrop, flat square tiles (2 px corners), lime-green glow and a white focus ring. |
| Minimal UI | Minimal | Carousel (`text`) | Just text: black page, Home as a plain list of names, the focused one white with a thin bar before it and its description after it. No tiles, art or glows. |
| Mega Drive Style | Mega Drive | Dashboard | Black, red and gold, square 4 px tiles. |
| Dreamcast Style | Dreamcast | Carousel, `strip` | White with the swirl's orange, 16 px tiles. |
| Sega Blue | Sega Blue | Hero grid | Late-90s console blue with orange. |

The fourth tab, Experience, is the Experience packs screen: a pack switches the
theme, Home, sounds and start screen together, with Undo. That screen shows the
same tabs, so Tab, L1/R1 or a click moves back to the presets.

To get the same look in your own theme file, set the matching
`arrangement`, `carouselStyle`, `tileRadius`, `focusRing` and `focusRing2`
keys from the table above.

## Glass style

Settings → Display & theme → **Glass** chooses how JukaHub's glass looks.
*Classic* (the default) is the clear frosted pane with a hairline edge.
*Realistic* mimics Apple's Liquid Glass: what is behind a card or button is
blurred and slightly magnified, the top of the pane catches a sheen, a bright
rim follows the light from the top left (with a softer glint on the opposite
corner), and Home cards float on a soft shadow. Overlay sheets, small chips and
pills, and panes over videos or games get the light and rim but not the blur.
Realistic is off while Reduce Transparency or High contrast is on. It is stored
as `"glassStyle": "classic" | "realistic"` in the custom variables.

## Glass rules

Glass is derived from the theme's `background` and `info` accent rather than
copied directly from the surface colors. On a dark background, controls use a
translucent white tint; on a light background they use deep slate with a
stronger alpha. The light or dark choice comes from background luminance.

The shared glass controls keep fixed strengths: resting fill is 13% tint, hover
17%, focused 20% and pressed 26%. The resting edge is 20% tint and the hover
edge is 40%; disabled controls use an 8% fill and 14% edge. A focused control
gets a three-pixel accent ring and lifts by three pixels. Light themes use a
deep-slate tint at 1.5 times the alpha (capped at full opacity); dark themes use
white. The `info` accent falls back to a legible focus color if it is too close
to the background.

Modal sheets are opaque blends of the background, 7.5% glass tint and 3.5%
accent, so content behind a sheet cannot show through its text. Recessed and
raised sheets use related mixes: the recessed sheet uses less tint, while a
raised card adds a light glass layer. Theme files do not set these glass
strengths. Choose text and surface colors with enough contrast for both regular
and derived glass surfaces. JukaHub derives readable text variants for semantic
status colors; their fills remain the authored colors.

For the rest of the configuration format, see
[Configuration](CONFIGURATION.md).
