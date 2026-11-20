# Move to a new SD card

Everything on a TrimUI handheld lives on its SD card: games, saves, BIOS
files, ports, artwork, emulators, JukaHub and all its settings. When the card
is full, or starts to fail, **Move to a new SD card** copies all of it onto a
new card, checks every copy, and after you swap the cards checks once more
that the new card is complete before you reuse the old one.

It is on the handheld only: **Settings → About → Move to a new SD card**, or
the **Move to a new SD card** tile under **Tools → Your setup**.

## What you need

- The new card, in a **USB card reader** plugged into the handheld's USB-C
  port (an OTG adapter if the reader has a USB-A plug). The handheld has one
  card slot, so the new card cannot go in beside the old one. A handheld or
  firmware with a second slot (`/mnt/SDCARD2`, `/mnt/sdcard2`, `/mnt/mmc`) can
  use that instead.
- The charger, or at least 40 % battery. Copying a whole card can take an
  hour or more: a USB 2 card reader writes roughly 15–30 MB/s.
- Best: a new card formatted as **exFAT**. A FAT32 card cannot hold a file of
  4 GB or more (large disc images); the move lists such files and explains.

## How to move

1. Open **Move to a new SD card**. JukaHub looks at your card and sums it up
   by category.
2. Under **To**, choose the new card. A reader that is not mounted yet is
   mounted when you choose it (the same mount **Tools → USB & OTG** uses).
3. The plan shows every category — **Games**, **Saves**, **BIOS files**,
   **Ports**, **Artwork & covers**, **Emulators & apps**, **JukaHub &
   settings**, **Other files** — with its size; press **A** on one to leave it
   out or bring it back. Below are the checks; press **A** on any of them for
   the full explanation:
   - the new card is not the card JukaHub runs from;
   - it can be written (a test file is written, read back and removed);
   - there is enough room — counted in the card's real block size, which on a
     big exFAT card adds up for thousands of small files. If it does not fit,
     a **Leave out Games** button (then artwork, other files, ports,
     emulators) shows what would make it fit. Saves, BIOS files and JukaHub
     are never suggested. Nothing is ever left out without you choosing it;
   - files too big for a FAT32 card are listed: format the new card as exFAT,
     or choose **Copy without them** to leave them on the old card;
   - the battery (charging, or 40 %);
   - no game is running.
4. **Start the move.** The progress card shows the file, the speed and the time
   left. You can leave the page: the move goes on in the background, waits
   while a game runs, and copies that game's saves at the end. **Stop the
   move** (or **B**) asks first; nothing copied is lost, and starting again
   continues where it stopped.
5. When it says **The new card is ready**: press **Eject the new card**, power
   the handheld off, take the old card out of the slot, put the new card in,
   and power on. Do not play in between: a game saved then is only on the old
   card (if you did, run the move again — it copies only what changed).

## How the copy is checked

- Every file is written under a temporary name, flushed to the card, given
  the original's date and only then renamed into place, so a cut-off copy
  never looks finished.
- While a file is read from the old card, its SHA-256 checksum is computed.
- When everything is copied, JukaHub drops its memory cache and reads every
  copy back **from the new card**, comparing checksums. A copy that does not
  match is made once more and read back again; a card that returns wrong data
  twice is reported (this is also how a fake-capacity card shows up).
- Finally the old card is looked at once more, and anything that changed or
  appeared during the move (a game played meanwhile, JukaHub's settings) is
  copied and checked too.

A file the **old** card cannot read (a failing card) does not stop the move:
it is listed, tried once more at the end, and the rest is copied. If the new
card fills up, turns read-only or is unplugged, the move stops and continues
from there next time.

Left alone on purpose, and listed: JukaHub's logs, swap files, unfinished
copies, links (they are never followed), drives mounted inside the card, and
the folders a computer keeps for itself (`System Volume Information`,
`$RECYCLE.BIN`, `.Trashes`, `.Spotlight-V100`, `.fseventsd`, `lost+found`,
`LOST.DIR`). Settings are always copied.

## After the swap

On the first start from the new card, JukaHub checks it against the move's
record in the background: first that every file is there with the right size
(seconds), then every file's contents (as long as reading the whole card
takes, at the lowest priority; it pauses while you play and continues after a
restart). It then tells you one of:

- **New card checked: all N files are here. Your old card is a complete
  backup — you can reuse it.**
- that all copied files are there, but something was left out or could not be
  copied, so the old card should be kept; or
- which files are missing or differ, with the advice to keep the old card and
  run the move again.

Files you change on the new card after the swap (a game saved, a setting) are
recognised by their newer date and are not counted as damage. The result
stays on the move page (**This card → New card check**), with **Pause** and
**Check again**.

## A card that starts the handheld by itself

A JukaHub OS image (or a similar system card) has a boot area and a system
partition besides the folder JukaHub runs from. A file copy cannot recreate
those. The move notices such a card and stops with an explanation. Write the
same JukaHub OS image to the new card on a computer first. If the new card
then shows no **JUKAHUB** partition in the reader, start the handheld from the
new card once (the first start makes it), power off and put the old card back.
Then run the move with the new card in the reader and choose its **JUKAHUB**
partition: JukaHub copies only your data partition (JukaHub, games, saves and
settings) into it. Any other partition, a plain card, or another system's card
is refused.

## Files on the new card

The move keeps its record in `.jukahub-move/` at the top of the new card:

| File | What it is |
| --- | --- |
| `manifest.json` | every copied file's path, size, date and SHA-256; what was left out, could not be read or did not copy; the source device and when it was written (as the handheld's clock showed it) |
| `journal.jsonl` | progress of a move that has not finished (folded into the manifest when it ends) |
| `check.json` | the result of the check after the swap |

You can delete the folder once the check has said the new card is complete.
