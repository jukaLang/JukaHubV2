# Support

This page is for how to get help. Feature requests go through the issue
templates; this page is about the paths that answer questions and unbreak
installs.

## Where to ask what

| What you need | Where | Why there |
|---------------|-------|-----------|
| "How do I …?" build/config/controls questions | [Q&A in Discussions](https://github.com/jukaLang/JukaHubV2/discussions/categories/q-a) | Searchable, doesn't spam the issue tracker |
| An idea or "wouldn't it be nice" | [Ideas in Discussions](https://github.com/jukaLang/JukaHubV2/discussions/categories/ideas) | Ideas graduate into issues once scoped |
| Show anything you built | [Show and tell](https://github.com/jukaLang/JukaHubV2/discussions/categories/show-and-tell) | Screen designs, configs, themes |
| A reproducible bug | [Bug issue template](https://github.com/jukaLang/JukaHubV2/issues/new?template=bug_report.yml) | Gets the reproduction fields so fixers don't guess |
| Security reports | Privately, per [SECURITY.md](SECURITY.md) | Never in a public issue |
| Community chat | [Discord](https://discord.gg/R9qgJjh5jG) | Fast, synchronous, answers get distilled into docs or issues afterwards |

## Before filing a bug

1. **Check the latest release** — many reported problems are already fixed
   ([releases](https://github.com/jukaLang/JukaHubV2/releases/latest), known
   issues in the notes).
2. **Search the issue tracker and Discussions** for the error text, not the
   symptom word.
3. **Read the matching doc**: configuration
   ([CONFIGURATION.md](docs/CONFIGURATION.md)), controls
   ([CONTROLS.md](docs/CONTROLS.md)), recovery
   ([RECOVERY.md](docs/RECOVERY.md)), troubleshooting follows the
   [bug template](https://github.com/jukaLang/JukaHubV2/issues/new?template=bug_report.yml).
4. **Update the config warning**: jukaconfigs older than the version that
   shipped warning messages can produce one-time errors that are gone after
   the first save; a startup warning is not necessarily a bug you need to
   report.

## What to expect

- This is a small project maintained by volunteers; first response to an
  issue or PR is usually quick, but fixes follow triage (see the roadmap's
  KPI table in [ROADMAP.md](ROADMAP.md)).
- Bugs you cannot reproduce on latest main or latest release get the
  `needs reproduction` label — add the missing detail and it moves again.
- Feature work is scoped through Discussions/Ideas first so nobody builds a
  big patch into a closed door.

## Maintainers looking for help with triage

The label set (`area/*`, `priority/*`, `flaky`, `needs reproduction`) plus
CODEOWNERS handle the mechanical part; the working agreement is
[CONTRIBUTING.md](CONTRIBUTING.md). If you want to help triage, say so in the
Development discussion category.
