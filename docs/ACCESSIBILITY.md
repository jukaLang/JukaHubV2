# Accessibility

JukaHub has a single panel that collects the settings that make the handheld
usable for people who are not the default player. It is in the Start menu as
**Accessibility**, and every change applies to the whole app immediately — no
restart, no per-screen configuration.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move between rows |
| D-pad Left / Right | Change the focused value |
| A | Change the focused value (or reset, on the reset row) |
| B / Back / Esc | Close the panel |
| Mouse / touch | Tap a row to change it; tap outside the card to close |

## Text size

Steps through **85%, 100%, 115%, 130% and 145%**. The scale is applied inside
`getCachedFont`, the one function every label in the app resolves its font
through, so scenes, overlays, toasts, the on-screen keyboard and the panels all
resize together. A change drops the font and label caches and the next frame is
drawn at the new size.

What it does not do: reflow layouts. Cards and buttons keep their configured
geometry, so at 145% long labels are ellipsised sooner. The scale is a
readability aid, not a zoom.

## High contrast

Switches to the **High Contrast** theme: black surfaces, white text, yellow
focus ring and much heavier borders. It is the same preset that appears in the
theme gallery, so it can also be chosen there.

## Colour-blind palettes

Three palettes built from the Okabe–Ito set, chosen so that the status colours
(success / warning / error) stay distinguishable under each dichromacy:

| Mode | Accent | Notes |
|------|--------|-------|
| Deuteranopia | sky blue `#56b4e9` | red/green status colours replaced by blue/orange |
| Protanopia | yellow `#f0e442` | error moves to magenta, success to blue |
| Tritanopia | orange `#e69f00` | blue/yellow pairs removed; info becomes magenta |

They are approximations of a whole design discipline, not a substitute for one.
A colour-blind palette takes priority over High Contrast; turn one off before
the other to see it.

## Button remapping

Two swaps, applied once per controller event before any overlay or scene sees
it, so the whole app agrees on what "select" means:

* **Swap A / B** — the Japanese button order, and the usual fix for a player who
  keeps pressing the wrong button out of muscle memory.
* **Swap X / Y** — moves the secondary and tertiary actions.

Only the four face buttons are remapped. The d-pad drives focus everywhere, so
swapping it would make navigation impossible to learn; L1/R1, Start and Back are
left alone for the same reason.

## Where the settings live

Everything is stored in `Apps/JukaHub/jukauser.json` under
`variables.Custom`:

| Key | Type | Meaning |
|-----|------|---------|
| `a11y_font_scale` | number | 0.85 – 1.45, `1.0` is the shipped size |
| `a11y_high_contrast` | bool | high-contrast palette on |
| `a11y_colorblind` | string | `""`, `deuteranopia`, `protanopia`, `tritanopia` |
| `a11y_swap_ab` | bool | face buttons A/B swapped |
| `a11y_swap_xy` | bool | face buttons X/Y swapped |
| `a11y_prev_theme` | string | theme restored when the palettes are switched off |

Because they are plain config values, they can be set from a PC before the
device ever boots, and they survive updates like every other setting.

## Limitations

* Text scaling does not shrink the layout to match: at the largest step, dense
  list rows show less text because labels are ellipsised.
* The colour-blind palettes replace the whole theme. Re-selecting a theme in
  Settings → Theme restores ordinary colours and turns the palette off.
* Remapping covers the controller only. Keyboard bindings are unchanged.
