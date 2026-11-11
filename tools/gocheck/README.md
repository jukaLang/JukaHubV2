# gocheck — static checks without a Go toolchain

The three scripts here are the shipped copy of the checker this project uses
when a Go toolchain is not available (a fresh checkout before `build.bat`, a
review host, an agent sandbox). They need Python 3 and nothing else: no Go, no
build, no network, and they only read source, so they work on a tree that does
not compile yet.

They complement `gofmt`/`go vet`; they do not replace them. Once the toolchain
exists, run `make check` and treat these as the fallback.

## gocheck.py

A Go syntax and scope checker modelled on `go/parser` (including the
composite-literal/block ambiguity rules). It resolves identifiers through block
scopes and reports the errors `gc` reports without type information:

- syntax errors (missing commas, unbalanced braces, bad statements)
- undefined identifiers (package scope is every file of the package)
- local variables declared and not used
- imports not used, or a package used without an import
- `:=` with no new variables on the left
- wrong argument counts for calls to package-level functions
- unknown keys in `sdl.Rect` / `sdl.Color` / `sdl.Point` literals

```
python3 tools/gocheck/gocheck.py --tests player/    # every package under player/
python3 tools/gocheck/gocheck.py player/chat.go     # only that file's findings
```

`--tests` includes `_test.go` files. Exit status is 1 when a problem is found,
so it can gate a script (`gocheck: N problem(s)` is the last line).

## alignchk.py

`gocheck.py` checks key/value alignment in composite literals; this covers the
other constructs `gofmt` aligns with its tabwriter: the columns of a
parenthesized `const`/`var` group and of a struct type. Blank lines, comment
lines and multi-line entries end a section; sections it cannot model exactly
(struct tags) are skipped rather than guessed.

```
python3 tools/gocheck/alignchk.py player/*.go
```

## importchk.py

Within each blank-line-separated group of an import block, specs must be
sorted by path (the order `gofmt` writes).

```
python3 tools/gocheck/importchk.py player/*.go
```

## Notes

- `gofmt -l` stays the authority on formatting; these catch what still needs
  fixing where `gofmt` cannot run.
- Each prints one line per problem and a `name: N problem(s)` total.
- `tests/scripts/docs-links.py` is the same idea for Markdown: it fails on a
  broken relative link, and `make check` runs it.
