# Card check

Is the SD card with your games healthy? **Tools → System → Card check** (or
**Settings → About → Card check**) looks at the card that holds the games and
writes a plain report. It is on the handheld only; a computer's drives have their own system's tools.

It is new and **not yet checked on a TrimUI**: the device names, the kernel
messages it looks for and the fsck programs it finds come from the Linux
kernel and the file system specifications, not from a run on the handheld.

## What it checks

Press **Start the check**. Each step can be stopped, and each has its line in
the results; **A** on a result shows its details.

| Step | What it does | How long |
|---|---|---|
| Card and partition | Which partition holds the games (from the list of mounted drives), its file system, size and free space, and the card's name, maker and date as the card reports them | a moment |
| Health flags | The file system's own record, read from the partition's first sectors and never written: FAT32's dirty bit and its clean-shutdown and hard-error bits, exFAT's VolumeDirty and MediaFailure flags. Also what the kernel said about the card since the handheld started | a moment |
| Read every file | Reads each file on the card to its end and lists the ones the card cannot read. On by default | about a minute per 2 GB used |
| Fake card test | Writes test files into free space, reads them back and deletes them. Off by default: **1 GB** or **All free space** | minutes to hours |
| File system check | Runs `fsck.vfat`, `fsck.fat`, `dosfsck` or `fsck.exfat` read-only (`-n`) when the system has one | a minute or two |

The read test runs at a capped speed (32 MB/s) on a background-priority
thread, so the menus stay smooth. Other drives mounted inside the card,
links and a swap file on the card are skipped. While a check runs the
handheld does not go to sleep by itself; you can leave the screen, and a toast
says when it is done.

## Reading the results

- **Dirty bit / VolumeDirty "set — normal while the card is in use"**: Linux
  marks the card dirty while it has it mounted and clears the mark when it
  lets go, so on a running handheld this means nothing by itself.
- **"Linux found it was not properly unmounted"**: when the handheld started,
  the card still carried the mark from last time — a power cut, a crash, or
  the card pulled out while on. Some files may be damaged: repair it.
- **Clean-shutdown bit clear**: the last computer or camera that used the card
  did not finish with it cleanly (Windows keeps this bit; Linux does not).
- **Hard-error bit / MediaFailure set**, **files the card could not read**, or
  **read or write errors** in the kernel messages: the card itself is failing.
  Copy your saves off and replace it.
- **Fake card test: did not come back**: the card stores less than it claims
  (a counterfeit — common with cheap large cards) or is failing. The report
  says roughly how much it really holds. Do not keep games on it.
- **fsck found problems**: the card is in use while it is checked, so some
  complaints can be false alarms; a check on a computer is the one to trust.

## The fake card test

Counterfeit cards report a size they do not have: writing past what they
really hold silently loses data or overwrites older files. The test writes
files whose every 4 KB block carries its own position and a pattern made from
a random seed, so no block can pass for another, then empties the system's
caches and reads everything back from the card.

- **1 GB** checks quickly that writing and reading work, but a fake card
  usually only shows itself near the end, which only the full test reaches.
- **All free space** fills the card except a margin (1% of its size, at
  least 256 MB, at most 2 GB). On a large card this takes hours: plug in the
  charger. It needs the charger or 30% battery to start and stops by itself
  below 10% without a charger.
- **Copy your saves to a computer first.** On a fake card, writing past what
  it really holds can damage files already on it — the same thing that would
  happen the day you fill it with games.
- Games cannot start while the test files are on the card.
- The test files live in `.jukahub-cardcheck` at the top of the card and are
  always deleted. If the handheld loses power during the test, the next start
  deletes them (a note in JukaHub's data folder, `card-check-fill.json`,
  remembers where they are).

## Repairing a card

JukaHub does not repair the card it runs from: a repair needs the card to
itself, and none of the handheld's start-up paths can check the card before
they use it. Turn the handheld off, take the card out and put it in a computer:

- **Windows**: open Command Prompt as administrator and type `chkdsk X: /f`
  (X is the card's drive letter).
- **Mac**: Disk Utility → select the card → **First Aid**.
- **Linux**: unmount it, then `sudo fsck.vfat -a /dev/sdX1` (FAT32) or
  `sudo fsck.exfat -y /dev/sdX1` (exFAT).

A card with read errors or a failed fake card test should be replaced, not
repaired: **Tools → Your setup → Move to a new SD card** copies everything to
a new one and checks every file.

## The report

Every run writes `card-check.txt` into JukaHub's data folder (on the handheld
`Apps/JukaHub/data` on the card), and **Report** on the screen shows it. It
names the partition, the file system and the steps' results, and ends with
the repair steps above.
