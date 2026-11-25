# 5. Overlays are folders on the card, turned into RetroArch configs at launch

- **Status:** accepted
- **Date:** 2026-11-20 (recorded 2026-11-24)
- **Deciders:** maintainers

## Context

RetroArch draws an overlay over a game, but its overlays are `.cfg` files
pointing at pictures, kept in RetroArch's own folder and picked three menus
deep. Other firmwares for these handhelds let a player drop a picture into a
folder named after the console. JukaHub already had an Overlay choice per
console (Settings → Emulation), and a card is often shared with NextUI, which
keeps overlays its own way.

## Decision

`player/useroverlays.go` makes `Overlays/<Console>/` beside the Roms folder
for every console with games (once: a folder the player deletes stays
deleted; a short name such as `Overlays/GBA` counts as the console's too).
A screen-sized PNG with a see-through hole, or a RetroArch overlay `.cfg` with
its pictures, goes in. The console's Overlay choice lists them as "Your
overlay · <name>"; left at Default, an overlay in the console's own folder is
used. A bare picture gets a one-layer `.cfg` written into JukaHub's data
folder at launch, and the launch points `input_overlay` at it through the
same scoped, one-launch config edit as every other RetroArch setting.

## Alternatives considered

- **Copy pictures into RetroArch's overlay folder.** Two copies to keep in
  step, and RetroArch's folder differs per firmware and per install.
- **Edit `retroarch.cfg` for good.** Changes every game and outlives JukaHub's
  launch; the scoped edit is put back when the game exits.
- **A picker screen only.** Does not answer "where do I put my border?", which
  is the question players ask; the folder is the answer, and the picker stays
  for choosing among several.

## Consequences

- Dropping a picture is the whole job, over FTP or the phone's Files card too.
- NextUI's short-named folders are listed but only play when picked, so a
  shared card does not change how NextUI's games look.
- Programs that draw the whole screen themselves (the native PICO-8, the
  standalone emulators) get no overlay; only RetroArch launches do.
