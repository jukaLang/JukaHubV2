## What this changes

<!-- One or two sentences on the user-visible effect, not the mechanics. -->

Closes #

## Type

- [ ] Bug fix
- [ ] Feature
- [ ] Documentation
- [ ] Build, CI or packaging
- [ ] Refactor with no behaviour change

## Mode

Check the one that applies. The first two need a migration plan before review;
they are not quick wins and should not ride along with something else.

- [ ] **Quick win** — one concern, under ~400 lines changed
- [ ] **Deep change** — touches `jukaconfig.json`, the package format, the plugin
      contract, the render loop, the boot path or a save format. Linked design
      note or issue:
- [ ] **Docs only**

## Definition of done

- [ ] Tests added, or the gap explained below
- [ ] Docs touched if behaviour is user-visible
- [ ] `CHANGELOG.md` entry under `Unreleased`
- [ ] No new unbounded network call, and no new blocking call on boot
- [ ] Verified on device if it affects the UI (say which device, or say
      "desktop only")
- [ ] Screenshot or GIF attached if the change is visual
- [ ] Reversible without a migration

## Verification

<!--
The commands you ran and what they proved. `make check` covers gofmt, vet,
builder syntax, the player tests and the jsdom tests; add `make bench` for a
boot-path change and `make e2e` for a builder change. Paste the failing output
if something failed.
-->

```
```

## Known flakes

`TestVideoFullscreenHidesControlBar` crashes intermittently inside
`SDL_RenderReadPixels`; `TestDecryptWithAuthRoundTrip` and
`TestKeyboardCachedFrameIsCheaperThanFullRedraw` have each failed once without a
clear cause. If one of those is your only failure, say so and re-run rather than
digging — see [CONTRIBUTING.md](../CONTRIBUTING.md).
