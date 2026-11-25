# 3. Control hints name the keys the user has, decided in one layer

- **Status:** accepted
- **Date:** 2026-11-09 (recorded 2026-11-24)
- **Deciders:** maintainers (the rule is the owner's, 2026-11-09)

## Context

Every footer badge, legend row and hint line was written in the pad's words
("A Open", "B Back", "L1/R1 Page"). A Windows PC without a gamepad, or a TrimUI
handheld driven from a USB or Bluetooth keyboard, showed buttons nobody was
holding. Several hundred call sites draw hints, and screens keep being added,
so a fix that lives at each call site would drift the day after it lands.

## Decision

`player/inputhints.go` decides one vocabulary for the whole interface and
translates pad names into keyboard keys where hints are drawn. The rule: no
controller with a D-pad means keyboard keys; an external keyboard on a TrimUI
means keyboard keys; anything else means pad buttons. With both present the
device pressed last wins, and a hotplug puts the rule back in charge.
Settings → Controls → Button hints (Auto / Controller / Keyboard) overrides it.

Hints keep being written in pad words. The translation is conservative: a name
is replaced only where its keyboard twin is the same on every screen (A is
Enter, B is Esc, the D-pad is the arrows) or where the hint's own label says
what a shoulder or Start does there (PgUp/PgDn, Home/End, Tab, F10). Anything
else is written `"<pad> / <key>"` at the call site. Prose goes through
`hintSentence`, and wording branches on `hintsUseKeyboard()`, never on
`P().IsHandheld()`. Pad names follow the accessibility remap (ADR 1), so a
swapped A/B is named as the button that now does the job.

## Alternatives considered

- **Branch at every call site.** The state before: each screen picked words
  with `IsHandheld()`, which is the wrong question (a handheld can have a
  keyboard, a PC can have a pad) and was forgotten by every new screen.
- **Always show both vocabularies.** Doubles the width of every footer on a
  1024-pixel screen and still names a pad on a PC that has none.
- **Name keys through SDL's key names.** They are layout-dependent and not
  stable words for a legend ("Return", "Escape"), and they say nothing about
  what a shoulder means on a given screen.

## Consequences

- New screens get keyboard hints for free as long as they draw through the
  shared helpers (`FooterHint`, `planFooterHints`, `drawGlassHintBadge`,
  `hintLine`).
- A key binding added for the pad needs a keyboard twin, or its hint must be
  written `"<pad> / <key>"`; the "Keyboard keys, screen by screen" table in
  [docs/CONTROLS.md](../CONTROLS.md) is where that is checked.
- The rule reads `/proc/bus/input/devices` on Linux to tell a real keyboard
  from virtual ones, which has to be re-checked on a firmware that names its
  input devices differently.
