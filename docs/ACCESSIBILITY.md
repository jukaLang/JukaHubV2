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
geometry. A button that cannot grow fits its label by stepping the text down a
pixel at a time instead of cutting it to "Play…" — but never below the size the
same label has at 100%, so the word stays whole wherever the layout allows and
the accessibility size is only given up where there is no room at all. A label
that still does not fit at that floor is ellipsised. The scale is a readability
aid, not a zoom.

The two checks that keep this honest are in `ui_access_audit_test.go`: at 145%
every button label in both shipped configs must draw in full, and the fitted
size may not drop below normal. They run on both real canvases — 1280×720 for
the 16:9 handhelds and 960×720 for the 4:3 Brick family — together with a
minimum tap size for every pressable control and a bounds check on the compact
canvas.

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

A physical → logical keymap, applied once per controller event before any
overlay or scene sees it, so the whole app agrees on what "select" means. The
panel edits it two ways, and the last one you touch wins:

* **Button layout** — a whole-state preset: **Standard** or **Nintendo** (A/B
  and X/Y both swapped). Applying one clears the per-button rows below, so a
  preset is never half-true; any individual remap makes the row read **Custom**.
* **A**, **B**, **X**, **Y**, **L1**, **R1** — one row per button, showing the
  action it currently sends (a preset already applied is included in that
  answer). Left and right walk the list of actions: the button itself, then the
  others, then **Disabled** last, and back. Choosing an action for A or B folds
  the Nintendo preset back into a plain remap rather than leaving two rules for
  the same button.
* **Select button** — **Default**, **Swapped** (Select sends Start and Start
  sends Select, so both stay reachable) or **Disabled** (Select is swallowed;
  Start still opens the menu). Select and Start share one row on purpose: two
  rows free to fight over them could leave no way into the menu.

The rows write the stored keymap (`a11y_keymap` in `jukauser.json`, e.g.
`a:b,select:none`), and the same layer is what a new remappable button would
use: no scene or overlay needs to change. The d-pad is deliberately never
remapped — it drives focus everywhere, and swapping it would make navigation
impossible to learn.

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
* The chord output still goes through the button remaps, so one-handed mode,
  the layout presets and the per-button rows compose.
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
