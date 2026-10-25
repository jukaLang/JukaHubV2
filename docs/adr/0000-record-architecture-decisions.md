# 0. Record architecture decisions

- **Status:** accepted
- **Date:** 2026-10-25
- **Deciders:** maintainers

## Context

JukaHub is a single-binary SDL2 app with a small number of load-bearing
decisions that are easy to undo by accident: how input is routed, who owns
renderer resources, what a diagnostic report may contain. Those decisions have
been argued in pull requests and then survived only in code comments, so the
next person re-opens the same question without the earlier measurements.

The project is also deliberately small (see
[Appendix D of the roadmap](../../ROADMAP.md#appendix-d-keeping-it-small)). An
RFC process for every change would be process outrunning contributors.

## Decision

Keep a short Architecture Decision Record directory, `docs/adr/`, following the
format on this page. One file per decision, numbered, immutable once accepted:
a change of mind is a new record that supersedes the old one.

An ADR is expected when a change:

- introduces an indirection that other code must go through (a keymap layer, a
  renderer-owned cache, a launch handover), or
- removes an option others may expect to exist (a mode, a config key, a
  platform), or
- spends a hardware budget other features compete for (RAM, SD writes, frames
  per second on the A133P).

Bug fixes, refactors that keep the same decisions, and documentation do not
need one. When in doubt, a paragraph in the pull request is enough.

## Consequences

- The reasoning survives the pull request, including the option that was
  rejected and why.
- A reader can tell a deliberate constraint from an accident.
- The directory adds a file to review only when a decision is genuinely made;
  there is no required template in the pull-request checklist.

## Template

```markdown
# N. Short imperative title

- **Status:** proposed | accepted | superseded by ADR-N
- **Date:** YYYY-MM-DD
- **Deciders:** who agreed

## Context

The forces at play: the device constraints, the bug or requirement, and the
measurements or tests that made the problem concrete.

## Decision

What was decided, in one or two sentences, then the mechanics that matter.

## Alternatives considered

Each option, and the reason it lost. This is the part future readers need most.

## Consequences

What becomes easier, what becomes harder, and what must be re-checked on
hardware.
```
