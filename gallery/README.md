# Community gallery index

`community.json` is one file with two audiences. It is published with the
builder, so it is served at `https://generate.jukalang.com/gallery/community.json`.

* `themes` — read by the handheld app's community theme library
  (**C** in Settings → Appearance, or **Y** on a gamepad). Installing an entry
  writes `themes/<id>.json` on the device, the same format a hand-made theme
  file uses, and applies it.
* `layouts` — read by the web builder's Gallery, and merged into the bundled
  layouts on open. Fork copies a layout into the editor; Apply also pushes it to
  a paired handheld.

Contributing is a pull request against this one file: no code changes, no
rebuild.

## Themes

An entry's `theme` object is exactly a theme file, so the app validates a
contribution with the loader that reads `themes/*.json`:

```json
{
  "id": "terminal-amber",
  "name": "Terminal Amber",
  "author": "@pixel_rune",
  "description": "Amber phosphor on near-black.",
  "tags": ["Dark", "Retro"],
  "theme": {
    "name": "Terminal Amber",
    "background": "#0a0805",
    "buttonColor": "#1c1508",
    "labelColor": "#ffb000",
    "inputColor": "#14100a",
    "surface": "#0d0a06",
    "surfaceAlt": "#171207",
    "surfaceRaised": "#241b0a",
    "textPrimary": "#ffc65c",
    "textSecondary": "#d79a3a",
    "textTertiary": "#96703a",
    "borderSubtle": "#ffb00018",
    "borderDefault": "#ffb00033",
    "borderFocus": "#ffb000",
    "success": "#6ed47a",
    "warning": "#ffc65c",
    "danger": "#e05252",
    "info": "#59b6e8",
    "overlay": "#0a0805cc"
  }
}
```

Every colour is `#rrggbb` or `#rrggbbaa`; a missing or malformed one makes the
app skip that entry (the rest of the library still loads, and the skip is logged
at startup). `summary` and `arrangement` are optional: naming a known
arrangement turns the theme into a total conversion, which appears in the
gallery's conversion tab instead of the colour tab.

`id` is a file name on the device: lowercase letters, digits and dashes, unique
within `themes` and never equal to a built-in palette id (for example `dark`,
`light`, `oled`). An entry that reuses a built-in id is refused, so a
contribution can never shadow a shipped palette.

## Layouts

A layout is a complete `jukaconfig.json` plus gallery metadata, the same shape
as the entries in [`layouts.js`](layouts.js):

```json
{
  "id": "sunset-dock",
  "name": "Sunset Dock",
  "author": "@mika",
  "description": "A single dock row over a warm gradient.",
  "tags": ["Home", "Dock"],
  "accent": "#e08b5f",
  "config": { "AppName": "JukaHub", "Width": 1280, "Height": 720, "scenes": [] }
}
```

Keep `config` at 1280×720 and under roughly 20 elements so the thumbnail reads
well. The builder validates a layout before forking it, so a broken config is
reported in the Gallery instead of replacing your work.

`id` must be unique among `layouts` **and** the bundled ids in `layouts.js`
(`aurora-bento`, `midnight-arcade`, `paper-mono`, `media-deck`, `cabinet`);
duplicates are ignored, with the bundled layout winning.

## Checks

`node --check` covers `layouts.js`. For `community.json`, run the app's own
parser in CI (`go test -run TestCommunityGalleryIndex` in `player/`), which
fails on a theme the loader would reject or an id that collides with a built-in.
