# Pick up and play, every time

A handheld earns trust when it does the same thing every time you pick it up:
the game starts, the buttons and sound work, sleep doesn't lose anything, and
an update never leaves you with a black screen. JukaHub has a very large
feature set; this page is about the parts that make it dependable on **your**
model of TrimUI, day after day:

- [Does it work on my handheld?](#does-it-work-on-my-handheld) — what was
  actually seen to work on this model, per game, port, app and emulator.
- [Your progress is safe](#your-progress-is-safe) — every save is checked, and
  the last good one is shown.
- [Updates fix themselves](#updates-fix-themselves) — a new version that
  doesn't start properly is replaced by the one you had.
- [Check your device](#check-your-device) — a guided hands-on test with a
  report.

## Does it work on my handheld?

A compatibility list says a game *should* run well. JukaHub also keeps track of
what *really* worked on your model of handheld. Each game, port, app and
emulator gets five checks:

| Check | What it means |
|---|---|
| **Starts** | it started and kept running |
| **Controls** | the handheld's buttons drove it |
| **Sound** | it played sound |
| **Sleep and wake** | it was still running after the power key's sleep and wake |
| **Quits back** | quitting it brought you back to JukaHub with nothing left running |

You don't have to do anything: just play, and JukaHub notes what works. Nothing
is sent anywhere.

**On a game's page** a small label tells you at a glance: **Verified on Smart
Pro** when all five checks passed, **3 of 5 checks on Smart Pro** when some
have been seen so far, or **Known issue** when something did not work. A game
nobody has tested yet shows no label. Ports have the same label on their page.

**More → Tested on this device** opens the details: each check with a tick, a
cross or a dash; where the result came from (**Seen by JukaHub** while you
played, **Marked by you**, or **From a shared list** that came with JukaHub);
and the JukaHub release and emulator it was seen with. Press **A** on a check
to mark it yourself (Works → Doesn't work → Not checked). **Describe a
problem** lets you write in a few words what goes wrong; it shows on the
game's page until you **Clear the problem**. **Forget this game's results**
starts the game over on this device (it asks first).

**Settings → About → Tested on this device** lists everything with its result.
Switch between Games, Ports, Apps and Emulators with **L1/R1**, press **A** on
a row for its details, and use **Export results** (**Y**) to save this
device's results as a file in JukaHub's `data/verified` folder. Someone with
the same model can copy that file into their `data/verified/import` folder to
see your results beside their own.

Results belong to the model: move your SD card to a different handheld and it
starts fresh there; move it back and the old results are still there.

### What "seen by JukaHub" means

- **Starts** — the game was still running 20 seconds after you started it. If
  it closed by itself within a few seconds, or crashed, JukaHub notes that it
  did not start, and why (the same rule as the card that explains a game that
  quits at once).
- **Controls** — you played for at least a minute and pressed the handheld's
  buttons at least 15 times. JukaHub never marks controls as a problem on its
  own: only you can say a button did not work.
- **Sound** — the game opened the handheld's speaker or headphone output and
  played through it. Sound through a Bluetooth headset is not seen, so JukaHub
  never marks sound as a problem on its own.
- **Sleep and wake** — you put the handheld to sleep for at least a few
  seconds during the game, and it was still running 10 seconds after waking.
  If it closed right after waking, that is noted.
- **Quits back** — when you left the game (from its own menu or JukaHub's
  in-game menu), JukaHub came back with nothing left running. Needing to hold
  Select + Start to get out, a game that ignored Quit, programs it left behind,
  or a crash count as a problem. Sleep's automatic power off, low battery and
  quitting JukaHub don't count either way.

When a game passes a check, its emulator gets the same result ("seen with
…"), so the Emulators tab shows what works too. Your own answers always count
more than what JukaHub saw, until JukaHub or the emulator is updated.

## Your progress is safe

Every time a game you started from JukaHub saves — the game's own save, a save
state, or the save RetroArch makes when you quit — JukaHub reads the file back
to make sure it really reached the card, complete and readable.

**See your last save.** On a game's page, the label under its details says
when the last good save was, like **Saved 4 min ago**, **Saved today** or
**Saved before power off**. If the game crashed, or the power went while you
were playing, and nothing was saved, it says **Not saved since it closed
unexpectedly**, so you know to check. **More → Saves** lists the last few
saves: what kind of save, when, and what led to it (while playing, before
sleep, when the battery ran low, before power off, when the game closed).

**Sleep without worry.** When you wake the handheld in a RetroArch game, a
short line on the game's screen says your progress is safe and when the last
save was. JukaHub never makes a save state on its own when you just put the
handheld to sleep, so your own save slots are never overwritten.

**Low battery.** If the battery is at 25 % or less when you put the handheld to
sleep with a game running, it wakes itself within 2 hours (30 minutes at 15 %
or less, 10 minutes at 5 % or less), saves the game and powers off — even if
**Power off after sleeping** is Off — so the game isn't lost when the battery
runs out asleep. At the next power-on, **Resume game at power on** brings the
game back, and RetroArch says "Back where you left off".

**Any game, not only RetroArch.** At the battery's last 3 %, a game whose state
can be saved is saved, and comes back at the next power-on even if the battery
runs out after the save. A game nothing can save — a port, a Windows program,
an Android game — is held asleep exactly where it is instead of being closed:
plug in and press power to carry on. PSP games resume exactly in JukaHub's own
PPSSPP build. See [Never lose a session](SESSION_SAFETY.md#universal-instant-resume).

**Start in.** Settings → Power & battery → **Start in** picks where JukaHub
opens when you switch the handheld on: Home (the default), the Game switcher,
a random game, or the last game you played (resumed in the emulator it last
ran in). It acts on the first start of each boot only — not when JukaHub comes
back from a game or restarts after an update — and a game that Resume game at
power on is bringing back always comes first. Hold **B** as Home appears to
stay on Home.

**In-game saves reach the card quickly.** RetroArch games started from JukaHub
write a changed in-game save to the card every 10 seconds, so a crash or a
flat battery loses seconds, not the whole session. Your own RetroArch setting
comes back when the game ends. To change the interval, set `saveFlushSeconds`
([configuration](CONFIGURATION.md); 0 leaves RetroArch's setting alone).

Times like "4 min ago" are shown only when JukaHub can be sure of them; after a
restart without the internet the page says "Saved before sleep" or "Last save:
in-game save" instead of guessing.

Ports and standalone emulators: the save list covers games in the library that
run in an emulator. How standalone emulators are closed safely at low battery
and power off is described in [Never lose a session](SESSION_SAFETY.md).

## Updates fix themselves

When JukaHub installs an update, it keeps a copy of the version you had. The
first time the new version starts, JukaHub's launcher checks that it really
works on your handheld: the screen has to come up and the buttons have to
respond.

If the new version gets stuck on a black screen, closes by itself, or doesn't
react to the buttons, JukaHub waits up to two minutes, then puts the previous
version back and starts it for you. This works even when the new version fails
before any of its own code runs. You don't need a computer or a card reader,
and nothing of yours changes: your games, saves and settings stay exactly as
they were.

You'll see a short note, **Update undone**, saying which update didn't start
and that the old version is back. Press **OK** to carry on, or **Send report**
to tell us what happened (you see the report first, and nothing is sent until
you press Submit). That update isn't offered again; the next newer one is.

If you turn the handheld off while the new version is stuck, that's fine: the
next time you open JukaHub it checks again, a little faster, and puts the old
version back if needed.

Good to know:

- The watching starts with the update *after* the one that brought this
  feature: the launcher that is running during an update is the one that
  watches it.
- The separate JukaHub OS SD-card image starts JukaHub with its own launcher
  and does not watch updates yet.
- Recovery by hand (the recovery chord, Version history) still works as
  described in [Recovery](RECOVERY.md).

## Check your device

**Device check** walks you through a quick hands-on test of your handheld and
writes down what it found. Open **Settings → About → Device check** (or
**Tools → Device check**). Each check tells you what to do in a sentence or
two; JukaHub measures what it can and you answer the rest:

1. **Buttons and sticks** — press every button once and push both sticks all
   the way round in the controller test, then hold Select to come back.
2. **Speaker** — play the test sound and say whether you heard it.
3. **Headphones** — plug them in; once JukaHub sees them, play the sound again.
4. **Sleep and wake** — press the power key, wait ten seconds, press it again.
   JukaHub checks that the handheld really slept and that Wi-Fi and sound came
   back.
5. **Bluetooth** — search for nearby devices (this switches Bluetooth on for
   the search, and off again if it was off). Pairing is up to you.
6. **Wi-Fi** — check the connection and the internet.
7. **USB DAC** — only if you have one: plug it in and play the sound.
   Otherwise skip.
8. **Game launch and return** — play the suggested game for a minute and quit
   it; say whether it came back to JukaHub cleanly. Your answer is also saved
   on that game's **Tested on this device** card.
9. **Update self-check** — shows this JukaHub release and whether it started
   fine after its last update.

Press **X** to add a note to any check and **L1/R1** to move between checks.
If you stop halfway, the check picks up where you left it next time. When
you're done, press **Y** to save a report: it goes into the **JukaHub reports**
folder on your SD card, where you can open it over FTP, with a card reader or
from the phone companion's Files card. The report never includes Wi-Fi or
Bluetooth names, addresses or the time of day — only the date. Its format
matches the [hardware validation log](HARDWARE_VALIDATION.md), so it can be
pasted there as it is.

## Files JukaHub keeps for this

| File | Holds |
|---|---|
| `data/verified/verified.json` | what was seen to work, per model (a damaged copy is kept as `.damaged`) |
| `data/verified/import/*.json` | results shared from other devices of the same model |
| `data/verified/export-*.json` | your exported results |
| `data/verified/device-check.json` | the last ten Device check runs |
| `data/save-ledger.json` | the last confirmed saves of each game (up to 2000 games × 5 saves) |
| `JukaHub reports/device-check-*.md` (SD card) | saved Device check reports |
| `.update-probation`, `.start-check`, `.update-rescued` (app folder) | the update check's state while a new version is on probation |

## For maintainers

**Shipping verified results.** `player/verified_seed.json` is built into
JukaHub and starts empty. After a hands-on pass, export the results on the
device (Tested on this device → Export results), review the file, and merge
its `records` into the seed; they show as **From a shared list** for every
player with that model, and anyone's own results still win on their device.

**Testing the update check on a device.** A file named `.test-update-hang` in
the app folder containing `hang`, `crash` or `noinput` makes the *next
updated* build misbehave that way during its probation (it is ignored, and
deleted, on any start without an update on probation). Start `launch.sh` by
hand with `JUKAHUB_PROBATION_LIMIT=30` for quicker runs. The steps are in the
backlog task UPDFIX-HW-1.
