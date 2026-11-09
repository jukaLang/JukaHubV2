# The JukaHub logo

<p><img src="jukahub-logo-1024.png" width="160" height="160" alt="The JukaHub logo">
&nbsp;&nbsp;<img src="jukahub-mark.svg" width="64" height="64" alt="The JukaHub mark"></p>

The logo is the Home screen's glass design made into an app icon, so the icon
on the TrimUI launcher, the Windows taskbar and the web builder's tab looks like
the app it opens. Everything here is drawn by [`mklogo.py`](mklogo.py) from
one construction; the PNGs, the `.ico` and the compiled Windows resource are
its output, and the SVGs are vector exports of the same drawing.

## Construction

- **Tile.** A rounded square with 25% corners (Home's 12 px icon badge on a
  48 px tile), inset 5.5% of the canvas so it sits with other apps' icons. It is
  Home's backdrop: the navy diagonal with the cyan bloom upper left and the
  violet bloom lower right, and the boot screen's blue and lilac between them.
- **Glass pane.** A frosted pane over the middle of the tile, inset 16.5%. What
  shows through it is the backdrop magnified like a lens, softened and richer;
  a faint sheen sits in its upper-left corner and the lifted pane casts a short
  shadow.
- **Rim.** The pane's edge catches both lights: 1.4% of the canvas, running
  from the focus cyan on the upper-left edges to violet on the lower-right ones.
  It is the focus ring's colour without the solid ring, so the icon reads as
  glass, not as a button.
- **J.** White, set like the J of the Inter wordmark: a flat-topped stem and a
  semicircular hook ending in a round terminal, with a faint cyan glow.

Two drawings share it. The **logo** is everything above and is used from 40 px
up. The **mark** is the tile and a larger J only, for 32 px and below and for
favicons, where a pane and a rim would only be noise. Up to 64 px every edge is
snapped to whole pixels, so the stem and the rim are single sharp steps.

## Palette

| Role | Hex | Taken from |
|------|-----|------------|
| Navy, deep | `#070B14` | `mockups/home-16x9-and-4x3.html` `--navy-a` (the hub preset's background is `#070B12`) |
| Navy, lifted | `#0B1426` | the same sheet's `--navy-b` |
| Cyan bloom | `#22D3EE` | `--cyan` |
| Focus cyan (rim, glow) | `#55D6FF` | the hub preset's Info / focus colour |
| Violet bloom | `#7C3AED` | `--violet`, `themeDefaultBloom2` |
| Violet rim | `#8B7CFF` | `ColorAccentSecondary` |
| Blue, lilac | `#2563EB`, `#C084FC` | the boot screen's liquid (`bootliquid.go`) |
| J | `#FFFFFF` to `#DDF7FF` | white with a breath of the accent at its foot |

## Where each file goes

| File | Size | Used by |
|------|------|---------|
| `player/JukaPlayer.png` | 300 px | the running window's icon (`windowicon.go`, `go:embed`), the About screen (`brandlogo.go`), and the Apps-menu icon of a release folder (`player/config.json`) |
| `player/trimui/Apps/JukaHub/icon.png` | 300 px | the Apps-menu icon of the SD-card package (`package-sdcard.sh`) |
| `player/JukaHub.ico` | 16, 20, 24, 32 (mark); 40, 48, 64, 128, 256 (logo) | Explorer, the taskbar, Alt-Tab, the Start menu |
| `player/rsrc_windows_amd64.syso` | | the `.ico` compiled into the executable (see `JukaHub.rc`) |
| `favicon.svg`, `favicon.png` | vector, 48 px | the web builder's tab and app bar |
| `apple-touch-icon.png` | 180 px, opaque | a home-screen shortcut to the web builder |
| `docs/assets/jukahub-logo.png` | 256 px | the README |
| `docs/assets/jukahub-mark.svg` | vector | the docs site's header and tab |
| `branding/jukahub-logo-1024.png`, `*.svg` | 1024 px, vector | anything else: the full render and the vector exports |

## Changing it

Edit the constants at the top of `mklogo.py` and run it from anywhere:

```sh
python3 branding/mklogo.py                     # rewrites every asset that changed
python3 branding/mklogo.py --preview sheet.png # every icon size on dark, light and grey
```

It needs Python 3 with numpy and Pillow, and nothing else: the `.ico` and the
`.syso` are written by the script itself, laid out the way `windres` lays them
out, so no MinGW toolchain is needed (`build.bat` still recompiles the `.syso`
with `windres` when it has one). Do not make the `.ico` from `JukaPlayer.png`
with another tool: the small sizes are a different drawing, and a downscale
throws that away.

Keep the logo on its own tile; it brings its own background, so it reads on
light and dark surfaces alike. Do not recolour it per theme: the colours are
Home's own, and a theme preset changes the app around it, not its icon.
