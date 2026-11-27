# ROM Audit

Part of the [Games Library](GAME_LIBRARY.md). The library can check the ROMs on
the card against a **No-Intro** or **Redump** DAT: which releases are present,
which are missing, which files are bad dumps, which are duplicates, and which
have a name the DAT does not use.

Open it from **Games Library → Select → Audit the library (DAT)**. The audit
runs in the background; the page fills in when it finishes. **Y** scans again,
**X** saves the full report, and **B** goes back to the library.

## Where the DATs go

Put the `.dat` files in JukaHub's data folder:

```
Apps/JukaHub/data/rom-audit/dat/          on the handheld
%APPDATA%\JukaHub\rom-audit\dat\          on Windows
```

A DAT dropped straight into `rom-audit/` is read too. Nothing is downloaded:
the audit works offline, and the DATs are the ones you choose. No-Intro
publishes its sets at [datomatic.no-intro.org](https://datomatic.no-intro.org/)
and Redump at [redump.org/dats](http://redump.org/dats/).

## What the verdicts mean

| Verdict | Meaning |
|---|---|
| **Verified** | The file's CRC32 is in the DAT and its name matches the release. |
| **Rename** | The CRC32 matches a release, but the file has another name. The report gives the name to use, extension included. |
| **Bad dump** | The name matches a release, but the CRC32 does not: a different revision, a patched ROM, or a damaged file. |
| **Missing** | The DAT lists the release and no file on the card matches it by CRC32 or by name. |
| **Duplicate** | Two files have the same CRC32. The first keeps its verdict; the later copy is reported against it. |
| **Not in DAT** | Neither the CRC32 nor the name is in the DAT: a hack, a translation, a homebrew game, or a set the DAT does not cover. |
| **Name only** | The file cannot be compared byte-for-byte (a `.cue`, `.gdi`, `.m3u`, `.chd` or a file over the size cap), so only its name was checked. |

## How matching works

- A DAT is matched to a library console through the same alias table the
  library uses (`player/romsetup.go`), so `Nintendo - Game Boy Advance` finds
  the `GBA` folder. A DAT for a console with no games on the card is skipped
  instead of printing hundreds of missing games you never had.
- Only the consoles with a DAT are read; games for other consoles are left
  alone. If two DATs name the same console, the first one is used and the
  second is reported as skipped. Put the set you want first in the folder.
- Cartridges are hashed with CRC32: the whole file, and the file without an
  iNES header or SNES copier header. A `.zip` or `.7z` gives its single ROM's
  CRC from its directory, so it does not have to be unpacked.
- Files over **256 MB** are not hashed. Disc sheets and compressed disc images
  are matched by name only, because a Redump DAT lists the tracks inside a
  disc and a `.chd` does not contain those tracks byte-for-byte.
- The library index's background CRC pass is reused when it already knows a
  file, so a second audit is much faster than the first.
- Nothing is renamed, moved or deleted. A rename is a suggestion; the file is
  left exactly where it is.

## The saved report

**X** writes `rom-audit-YYYYMMDD-HHMM.txt` to `Apps/JukaHub/data/rom-audit/`.
The screen caps each problem list at twelve rows; the saved file lists every
missing release, bad dump, duplicate and rename. It is plain text, so it can be
copied to a PC or attached to a bug report.

## Limits

- DATs larger than 24 MB are skipped. No-Intro's cartridge sets are far below
  that; a full MAME set is not what this page is for.
- A DAT that is not a clrmamepro list (a plain text list of names, for example)
  has no CRCs to match and is reported as unreadable.
- The audit reads the card, so a large library takes a while on a handheld.
  It runs off the event thread and the library stays usable behind it.
- Disc systems are the weakest case: use the DAT's name for the `.cue`/`.chd`
  so the release is at least found by name, and expect "name only" rather than
  a CRC verdict.
