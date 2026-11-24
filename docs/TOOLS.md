# Tools: search, pins and quick checks

The Tools screen (**Start → Tools**) holds everything that is not a game or a
video, in sections: Together, Everyday apps, Toolkits, Connectivity, System,
Your setup, and Updates & support. This page is about getting to a tool fast,
and about five small tools that answer everyday questions: why the internet
does not work, what fills the card, whether a download is intact, whether the
screen has a dead pixel, and how to get a link onto a phone.

They are new (2026-11-23) and **not yet checked on a TrimUI**: everything below
was built and checked on the code, and the device steps are in the backlog
(TOOLS-HW-1 in [MODERNIZATION.md](../player/MODERNIZATION.md)).

## Search and pins

- **Y** (keyboard: **Y**, **/** or **Ctrl+F**) opens the keyboard, and the page
  filters as you type. A tool is found by its name, its line, its section and
  the words people use for it — *storage*, *internet*, *checksum*, *ftp*,
  *dead pixel*. Names that start with what you typed come first. The Network
  tools page's tools (FTP server, Ping, DNS lookup…) are found here too, and
  open that page on the tool.
- **Search** on the keyboard keeps the filter; the page bar says how many tools
  match. **B** clears the search, and **B** again leaves Tools. Opening Tools
  afresh always shows every tool.
- **X** pins the focused tool. Pinned tools lead the page in a **Pinned**
  section, in the order you pinned them (twelve at most), and keep a star on
  their own tile. **X** on either copy unpins it.
- The pins are kept in `tools-pins.json` in JukaHub's data folder, so **Backup
  & restore** carries them to another device.

## Connection check

**Tools → Connectivity → Connection check** (also first under **Network tools →
Probes**, and in Home search as *connection check* or *no internet*). It needs
no network — that is when it is wanted — and runs as soon as it opens:

| Step | What it asks |
|---|---|
| Wi-Fi (*Network* on a computer) | Does this device have an address, on which network, how strong is the signal (below -75 dBm is a warning)? With no address it says why: the radio is off, the router gave no address, or no network is joined |
| Router | The default route's router, asked for a connection on the ports a home router serves (53, 80, 443) — a refusal is an answer too — and, failing that, the neighbour table |
| Name lookups (DNS) | One lookup through the system's resolver, the path every download takes |
| Internet | The connectivity probe, which also notices a hotel or café sign-in page; if it fails, 1.1.1.1 and 8.8.8.8 by address, which needs no lookup |
| Secure sites (HTTPS) | One secure request with the certificate list JukaHub uses: names a wrong clock, or a network that intercepts secure connections |

The card at the top says where the connection fails — the first step that
fails, in order — and what to do. The steps are read together: a router that
ignores the tests while the internet answers is not the problem, and an
internet that answers by address while names fail is a DNS fault, not "no
internet".

Under the card: **Open Wi-Fi** (no network, the radio off, a sign-in page) or
**Date & time** (a wrong clock) when that is the fix, then **Run again**
(**X**), **Copy report** (**Y**) and **Network tools**. **A** on a step shows
what it found. Leaving the screen stops a check; coming back runs it again —
so after joining a network on the Wi-Fi page, **B** brings you back to a fresh
check. Screenshot mode shows stand-in names and addresses.

## What's using space

**Tools → System → What's using space** (also **A** on the **Storage** screen,
and in Home search). The Storage screen says how full each drive is; this says
why.

- On the handheld the card is scanned as soon as the screen opens: seconds to a
  minute. On a computer, press **A** on the drive — a system drive can hold
  millions of files. **Left / Right** on the drive row picks another drive;
  **A** on it scans again, or stops a scan under way.
- The list is the folder on screen: its folders and its largest files, biggest
  first, each with its size and a bar for its share of the folder. A folder's
  smaller files are summed in one row. **A** opens a folder, **B** climbs back
  up, **X** shows any row in Files, and **Y** swaps the folder for the drive's
  sixty largest files, wherever they are.
- Sizes are the files' own sizes, as Files shows them. What the drive reports
  as used beyond them is one row at the top: **Other used space** — the file
  system's own records, the slack of large clusters (on a big exFAT card every
  small file takes a whole cluster), and folders that could not be read — or
  **Outside this folder** when the scan was of a folder rather than a drive.
- The scan does not follow links, does not count another drive mounted inside
  this one, and waits while a game is running. Leaving the screen does not stop
  it; the result is kept until the next scan.

## Verify a file

**Tools → System → Verify a file**, or in **Files**: **Y** on a file →
**Verify checksum**. From Tools it opens on a file picker (the places Files
starts from, then folders); **Y** picks another file later.

- One read gives four checksums: **SHA-256**, what download pages list, and
  **SHA-1**, **MD5** and **CRC32**, what the No-Intro and Redump game lists use.
  **A** on one copies it, **X** copies the SHA-256, and **Copy all four** adds the
  file's name and size.
- **Compare with a checksum**: **Paste** takes it off the clipboard, **Type**
  opens the keyboard. Which of the four it is is told by its length, and a line
  like `<checksum>  name.zip` works too. The row then says **The file matches**
  or **The file does not match** (a different or damaged copy). A checksum given
  before the read ends is checked when it does.
- A large file takes a while (a 4 GB disc image, a minute or two on a card):
  the row shows the speed and the time left. Leaving the screen does not stop
  the read; **Stop** or another file does, and it waits while a game runs.

## Screen test

**Tools → System → Screen test** fills the whole screen — no bars, nothing that
could hide a pixel — with test cards, each with what to look for: **White**
(dead pixels, pressure marks), **Black** (stuck pixels, backlight bleed),
**Red**, **Green** and **Blue** (stuck sub-pixels), **Grey** (uneven backlight),
**Gradients** (banding), **Colour bars**, **Fine lines** (one-pixel lines:
sharpness, shimmer), **Grid** (a 16-pixel grid with the edges marked: is the
whole picture on screen, on a TV too?) and **Motion** (a white block crossing
the screen: ghosting).

| Pad | Keyboard | Action |
|---|---|---|
| A, Right, Down, R1, a tap | Enter, Space, Right, Down, PgDn | Next card |
| Left, Up, L1 | Left, Up, PgUp | Previous card |
| — | 1–9 | Card 1 to 9 |
| Y | Y | Auto: a new card every three seconds (Y again stops) |
| X | X or H | Hide the caption at once (it fades by itself after 3.5 s) |
| B | Esc | Leave |

Nothing flashes: Auto changes the card every three seconds, never faster.

## QR maker

**Tools → Everyday apps → QR maker**: type or paste text — a link, a Wi-Fi
password, a note — and **Show** fills the screen with it as a QR code, on the
same card the pairing codes use: the largest square the screen allows, a proper
margin, and the backlight at full while it is up. **A** or **B** closes it.
The keyboard's tool row pastes, and it opens with the last text, so a second
code is an edit. Up to 1,200 characters; past 300 the code uses lower error
correction, which keeps its squares larger. The text is kept only until
JukaHub closes.
