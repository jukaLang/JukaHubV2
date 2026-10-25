# 1. One physical → logical keymap, applied before dispatch

- **Status:** accepted
- **Date:** 2026-10-25
- **Deciders:** maintainers

## Context

The accessibility layer could swap A/B and X/Y, and nothing else. Every further
remap people asked for — Nintendo face layout, Select where Start is, a button
switched off for someone who keeps hitting it by accident — had no home: the
swaps were two booleans consulted by the handlers that cared, so a new remap
touched scene code in as many places as there were handlers.

The second problem was ordering. Swapping after a handler had already decided
what a press meant produced panels where a footer said "A: Play" but the press
that arrived was B.

## Decision

`player/a11y.go` keeps one general map, physical SDL button → logical SDL
button, loaded from `a11y_keymap` (`select:start,start:select`, `select:none`).
`a11yResolveButton` is the only place a physical press becomes a logical one: it
applies the general map first, then the two preset swaps, and reports whether the
press must be swallowed. The disabled sentinel in that map is what makes
"switched off" a data value rather than a special case.

The A/B and X/Y swaps stay as flags on top because they are preset behaviour the
panel toggles, and the Nintendo layout sets both.

## Alternatives considered

- **Remap inside each handler.** The state before this record. Every new button
  is a patch in every scene, and the footer hints drift from the behaviour.
- **Rewrite the controller mapping at the SDL level.** SDL's mapping API can
  express some of this, but it is device-global, cannot express a UI-level
  "swallow this button", and would have to be reconciled with the existing
  swaps rather than composed with them.
- **A keymap type with named actions.** Cleaner on paper, but it needs an action
  enum that every scene agrees on; the button ids are already that vocabulary,
  and the layer is small enough to stay a map.

## Consequences

- A new remappable button is a row in the accessibility panel, not a scene
  change; the panel grows without touching input handlers.
- Every handler, overlay and on-screen-keyboard lookup sees the same logical
  button, so a footer hint and the press can no longer disagree.
- The rule is pinned by `player/a11y_keymap_test.go`: parsing, round-trip,
  drop-before-dispatch, and the Nintendo preset setting both swaps.
- Start keeps its menu role by construction: disabling Select swallows Select
  only.
