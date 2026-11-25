# 4. Translate where text becomes pixels, keyed by the English string

- **Status:** accepted
- **Date:** 2026-11-12 (recorded 2026-11-24)
- **Deciders:** maintainers

## Context

The interface is written in English as string literals at several thousand
call sites. Translating it meant either touching most files of the package or
finding a narrower seam. The handheld renders every frame on an A133P, so
whatever translates must cost nothing for English and next to nothing for
other languages.

## Decision

`player/i18n.go` translates in the few helpers every label goes through to be
measured, clipped, wrapped and drawn (`measureText`, `fitTextCached`,
`renderText`, `wrapTextLines`, `drawText`). Measuring and drawing translate
the same way, and clipping translates before it shortens.

The catalogue is gettext-style: the key is the exact English string, one JSON
file per language in `player/i18n/`, embedded in the binary. A key with `%d`
or `%s` is a pattern whose captures may be reordered (`%[2]s`) and are
themselves looked up; "Label: value" and "part · part" are joined from known
parts. English is one atomic load and a nil check; another language is one
map read, with a small bounded cache for strings that are not keys.

## Alternatives considered

- **Wrap every literal in `T("…")`.** The usual way, and the most precise, but
  a change to most files, repeated by every screen written afterwards, and
  easy to forget.
- **Message IDs instead of English keys.** Stable across wording changes, but
  every call site would need an ID and every English edit two files; with
  English keys an unchanged string keeps its translation and a changed one
  falls back to English, which is visible and harmless.

## Consequences

- New screens are translatable without doing anything, as long as they draw
  through the shared helpers; content (web pages, chat, file names, Notes)
  goes through the raw paths and stays as written.
- Text measured with `font.SizeUTF8` directly is laid out at its English width
  while the translation is drawn; those sites move to `measureText` as screens
  are touched.
- Exact-string keys mean a game called "Settings" is translated in a list:
  rare, and the price of not touching every call site.
- Plural rules beyond `%d` patterns, and a check that lists untranslated
  strings per language, are still open in the backlog.
