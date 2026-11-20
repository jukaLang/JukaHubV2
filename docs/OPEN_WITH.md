# Open with and default apps

A in **Files** opens a file in JukaHub's own viewer for its type: pictures in
the image viewer, music in the music player, PDF and EPUB in the reader, a game
in the library's emulator, an archive in its sheet, and anything else as text
(or, when it is not text, as a read-only hex view).

A handheld card often holds better tools for some files. KOReader reads MOBI,
DjVu, FB2 and comic archives that JukaHub cannot show, and reads a PDF with
reflow and dictionaries; other apps in the card's `Apps` folder play music or
video their own way. **Open with** lets you pick any of them for a file, once
or for good.

## Opening a file with another app

In Files, highlight a file, press **Y** for the actions and choose
**Open with…**. The same row is in the text page's menu and in the reader's
menu. The sheet lists:

- **JukaHub's viewers** that really handle the file. The one A uses today is
  marked *JukaHub's choice*; a type's saved default is marked *Default*; when
  an app takes the type without a default (see below), its row is marked
  *Opens now*;
- **Open with Windows** on a PC (the app Windows associates with the type);
- **installed apps** that open the type. An app the App Center's catalogue
  documents for the type says *catalogue: opens .epub*; an app that only
  declares the type in its own `config.json` says so, and any other app says
  *Not tested with files*. An app whose launch script does not pass a file on
  says that too;
- **Get <App>** — on a TrimUI handheld only — for an app the catalogue documents
  for the type that is not on the card yet. It opens the App Center on that app;
- **Another app…**, which lists every other installed app. JukaHub hands it the
  file the way the TrimUI menu hands a game to an emulator (the file is the
  launch script's first argument); an app that ignores it simply starts.

| Pad | Keyboard | What it does |
|---|---|---|
| A | Enter | Open the file with the highlighted app, this time only |
| X | X | **Always use**: make it the default for this extension, then open |
| Y | Y | Forget the type's default (shown when one is set) |
| L1 / R1 | PgUp / PgDn | Page through a long list |
| B | Esc | Cancel |

An app opens full screen; JukaHub waits underneath and comes back when the app
quits. A script in Downloads (it may have come from the chat) runs on a second
press, as it does in Files.

## Defaults

With a default saved, A in Files opens every file with that extension in the
chosen app. Defaults are kept per extension (`.epub`, `.cbz`, `.mobi` …) on
this device.

- **Games always play.** A ROM in a console folder (or a file only one console
  uses) starts in the library's emulator; Game options → Emulator chooses that.
  Open with still offers the other apps for a one-off.
- **An app that was removed** is not used. A toast says once per session that
  it is not available for that type any more, and the file opens as if no
  default were saved (below). The default stays saved, so reinstalling the app
  brings it back; **Default apps…** shows it as *not on the card*.
- **The text page.** Choosing one of JukaHub's viewers, or **Get <App>**, from
  the text page's menu while it has unsaved changes is refused with a toast:
  save or discard them first. Another app or Windows opens over the page and
  leaves it as it is.

### Formats JukaHub cannot show

For e-books, comics and documents JukaHub has no viewer for — `.mobi`, `.azw`,
`.azw3`, `.prc`, `.pdb`, `.fb2`, `.fbz`, `.djvu`, `.djv`, `.chm`, `.cbz`,
`.cbr`, `.cb7`, `.cbt`, `.xps`, `.oxps`, `.doc`, `.docx` and `.odt` — A opens
the file in the one verified app on the card that reads it, without asking. A
toast names the app and how to change it. With two such apps (or none), the
KOReader on the card reads it; without KOReader A shows the hex view as before.
(`.rtf` is not one of these: the text page shows it.)

### "Open books with" (Settings → Media & playback)

While KOReader is on the card, Settings → Media & playback → **Open books
with** can send every PDF and EPUB to KOReader instead of JukaHub's reader.
That setting applies only while the type has no default. Choosing JukaHub's
**Reader** in Open with (Always use) for `.pdf` or `.epub` then saves it as the
default, so A opens JukaHub's reader for that type even with "Open books with:
KOReader"; the same goes for **Hex view** on a format KOReader or a verified app
would otherwise take. **Y** (*No default*) gives the type back to the setting.

### Reviewing them

**Default apps…** (in the Files actions once a default exists, or search Home
for "Default apps") lists every saved type. **A** chooses a different app for
it; **X** resets it, and the toast says where the type opens now.

## A reading app beside the reader

The reader's menu (**Y**, or the Options chip, in the PDF and EPUB reader)
shows **Open in KOReader** when KOReader is on the card; the page you reached
is saved first, so coming back to JukaHub's reader continues where you were.
Without KOReader it shows **Open in <App>** for another verified reading app on
the card, or **Get <App>** (App Center, handheld only) for one that is not
installed, above **Open with…**. When JukaHub's reader cannot show a document at
all, **Y** (or the "Open with…" chip) offers the other apps straight away.

## For app authors: declaring file types

The App Center keeps the list of verified apps and how each takes a file. An
app it does not know can still offer itself for file types in its own
`config.json` (the TrimUI app manifest), next to `label` and `launch`:

```json
{
  "label": "My Reader",
  "launch": "launch.sh",
  "jukahub": { "opens": [".epub", ".pdf", ".cbz"] }
}
```

JukaHub runs `launch.sh` with the file's full path as its first argument
(`$1`). Such an app is listed for those types with that note until the App
Center's catalogue documents it.

## Where things are kept

The defaults are one entry, `openWithDefaults`, in `jukauser.json`'s Custom
variables. Its value is a string holding a JSON object of extension → handler,
for example:

```json
"openWithDefaults": "{\".epub\":\"tsp:koreader\",\".pdf\":\"jh:reader\"}"
```

`jh:` names one of JukaHub's viewers (`jh:reader`, `jh:images`, `jh:music`,
`jh:video`, `jh:text`, `jh:hex`, `jh:archive`, `jh:web`, `jh:script`,
`jh:java`), `win:system` the Windows association, and `tsp:<folder>` an app in
the card's `Apps` folder (the folder's name in lower case). A hand-written
object instead of the string is read too; extensions without the dot and upper
case are accepted. JukaHub's choice for a type is stored as no entry at all,
unless KOReader or a verified app would take the type without one (as above).
Deleting the entry resets every type.
