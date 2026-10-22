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

## One-handed mode

Playable with one hand: the d-pad does the navigating *and* the confirming, so
the thumb never has to leave it. Turn on **One-handed mode**, then **tap the
chord button** (L1 by default) and the next d-pad press becomes a face button:

| Direction | Becomes |
|-----------|---------|
| D-pad Up | **A** (open, confirm) |
| D-pad Down | **B** (back, cancel) |
| D-pad Left | **X** |
| D-pad Right | **Y** |

* Tap the chord button once to arm the next press. The arm is one-shot: after
  that press the d-pad is a d-pad again.
* Tap it a second time to cancel. Holding it works the same as tapping once and
  never leaves the latched state behind.
* The chord output still goes through Swap A/B and Swap X/Y, so the two settings
  compose.
* **Chord button** chooses which shoulder or Back acts as the modifier, so it can
  be the one your other hand reaches. The chosen button stops doing its own
  action while the mode is on (L1 normally pages a list, for example).
* The mode applies everywhere the app reads the pad, including the on-screen
  keyboard and every panel.

This is an in-app aid. JukaHub launches emulators as child processes and does
not see their input, so the chord cannot press buttons inside a running game;
for in-game one-handed play, RetroArch's own hotkey remapping is the tool, and
standalone emulators each need their own configuration.

## Slower key repeat

**Key repeat → Slow** doubles the delay before a held direction starts repeating
on the on-screen keyboard, and doubles the repeat interval. The press itself is
unchanged, so typing a single character still feels immediate; it is only the
runaway after a long hold that goes away.

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
| `a11y_one_handed` | bool | one-handed chord mode on |
| `a11y_chord_button` | string | `l1`, `r1`, `back` or `l3` |
| `a11y_slow_repeat` | bool | keyboard repeat delay and interval doubled |
| `a11y_prev_theme` | string | theme restored when the palettes are switched off |

Because they are plain config values, they can be set from a PC before the
device ever boots, and they survive updates like every other setting.

## Limitations

* Text scaling does not shrink the layout to match: at the largest step, dense
  list rows show less text because labels are ellipsised.
* The colour-blind palettes replace the whole theme. Re-selecting a theme in
  Settings → Theme restores ordinary colours and turns the palette off.
* Remapping covers the controller only. Keyboard bindings are unchanged.
* One-handed mode changes what the app itself does with the pad. A game that is
  already running is outside it — see the note above.
* The slow key repeat applies to the on-screen keyboard, which is the only place
  the app repeats a held direction itself; SDL's own key repeat for a physical
  keyboard is left at the system default.
