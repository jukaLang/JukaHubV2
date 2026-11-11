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
