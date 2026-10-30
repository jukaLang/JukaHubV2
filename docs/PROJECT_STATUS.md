# Project status and improvement priorities

**Reviewed:** 2026-10-29  
**Purpose:** one quick, readable view of what is partly complete, what needs work,
and what still requires a real device. This page summarizes the detailed plans;
it is not a schedule, and no owner or delivery date is implied.

## At a glance

JukaHub has a substantial working player and web builder. Important foundations
already in place include configuration validation and recovery, bounded caches,
controller focus/navigation, structured diagnostics, and broad automated tests.
The remaining work is mainly about protecting user data, finishing lifecycle and
handheld usability work, and validating behavior on actual devices.

For a short answer: **protect save data first; then finish cancellation and
handheld usability; measure the remaining device-only risks before optimizing.**

## Partly complete

| Area | Current state | What remains |
|---|---|---|
| Handheld touch usability | 44 px hit targets are used for the Exit chip and browser chrome. | Extend touch targets to other controls, review safe-area spacing, and verify on the device. |
| Background work and cancellation | Scene cancellation and bounded work are in place for several paths. | Pass cancellation through older feature network calls; review screen-specific polling and resource cleanup. |
| Web builder | Core editing, preview, undo/redo, autosave and export work. | Split the large script into modules; finish the planned accessibility and performance checks. |
| Project quality workflow | CI and a documentation-site scaffold exist. | Complete the remaining CI/process work listed under project maintenance below. |

## Highest-priority improvements

These are ranked recommendations, not scheduled commitments.

1. **Protect save data during restore.** Before changing live save folders,
   validate every snapshot path, file, size and SHA-256 digest. A corrupt or
   incomplete snapshot must fail before any live file is changed. Add tests for
   missing, corrupt and unsafe paths.
2. **Finish cancellation for feature network work.** Thread request contexts
   through remaining older fetches so leaving a screen stops obsolete work,
   rather than merely hiding its result.
3. **Complete handheld touch and safe-area coverage.** Apply the existing
   hit-target approach consistently, keep visible and clickable geometry aligned,
   and check the rounded screen edges on hardware.
4. **Validate the device-only paths.** Test Wi-Fi on the supported stock images,
   LAN Play between two machines, browser input/touch behavior, and the actual
   render backend on the handheld. Desktop or dummy-renderer tests do not settle
   these questions.
5. **Profile before expanding performance work.** Background polling is already
   paused in key hidden states and several long lists draw only visible rows.
   Measure remaining list preparation, polling and scene-resource costs first;
   optimize only where measurements show a user-visible problem.
6. **Consider a small media “Up Next” queue.** This is a useful, service-free
   feature, but it follows the data-safety and reliability work above.

## Device checks still needed

These need physical hardware; a passing desktop test is not a substitute:

- Confirm the Wi-Fi interface, tools and DHCP behavior on each supported stock
  handheld image.
- Complete a real two-machine LAN Play session.
- Read the startup render-pipeline diagnostic on the device and confirm whether
  the app is using hardware acceleration or software rendering.
- Check touch targets, browser navigation, startup, frame pacing and idle memory
  on the handheld. Keep measured results with the relevant detailed plan.

The AndroidPak runtime is also experimental: source and host-side smoke tests are
present, but real-game execution has not been demonstrated on hardware. See
[Android engine](ANDROID.md) and the [AndroidRT notes](../player/androidrt/README.md).

## Project-maintenance work

Separate from player features, the repository roadmap still lists maintenance
items that need follow-through: finish the accessibility audit by filing
screen-specific findings, deploy the existing documentation-site scaffold, and
resolve remaining contributor/CI chores such as coverage change reporting,
formatter adoption, flaky-test triage, maintainer labels and starter issues.
The detailed acceptance criteria and dependencies are in the
[project roadmap](../ROADMAP.md).

## Ideas, not commitments

The [feature backlog](FEATURE-BACKLOG.md) and
[ranked feature slate](FEATURE-SLATE.md) contain unadopted proposals. Their
rankings are not promises or active work. Promote an idea here only after
choosing to take it on and giving it a status and a clear way to verify it.

## Detailed references

- [Player modernization plan](../player/MODERNIZATION.md) — implementation
  details, evidence, phase checklists and audit history.
- [Project roadmap](../ROADMAP.md) — repository/process work and definitions
  of done.
- [Feature backlog](FEATURE-BACKLOG.md) — designed feature gaps and constraints.
- [Feature slate](FEATURE-SLATE.md) — broader, unadopted product ideas.
- [Checkpoint design](CHECKPOINTS.md) — a detailed design for one unimplemented
  milestone.
