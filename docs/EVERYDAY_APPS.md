# Everyday apps

JukaHub carries a handful of small apps for the things a handheld is handy
for between games: **Notes**, **Tasks**, **Voice Memos**, **Paint**,
**Stopwatch**, the **Calculator**, **Alarms** and the **Pomodoro** timer, plus
**Solitaire** among the games and **Create ZIP** in Files.

Open them from **Tools → Everyday apps**, the **Apps** screen, the **Start
menu** or Home search ("paint", "todo", "memo", "timer", "solitaire" …).
Solitaire is also on the **Games** screen and in the Games library. Every pad
control has a keyboard key, and a mouse works too; the full key list is in
[Controls → Everyday apps](CONTROLS.md#everyday-apps).

## Tasks & reminders

Quick checklists that know about your calendar and your alarms. (Not to be
confused with **Scheduled tasks** in Tools, which lists timed jobs.)

- **Lists** sit on the left: **Today** (everything due today or earlier, from
  every list, plus what you finished today), then your lists with their open
  counts — **Inbox** and **Shopping** to start — then **New list** and **From
  your phone**.
- **Add** a task with **Y** (N on a keyboard) or the **Add a task** row; it
  goes into the list on screen (from Today: into your first list, due today).
  **A** ticks it off; finished tasks fold under **Done**.
- **X** opens a task's sheet: edit the text, set **Due** (quick picks Today /
  Tomorrow / Next week / No date, then the day and the time in five-minute
  steps), **Remind me**, **Ring as alarm**, **Repeat** (every day / every
  week), move it to another **List**, or **Delete** it (press A twice). On a
  list, X renames it, clears its done tasks or deletes it with its tasks.
- **Reminders:** a task with a time reminds you at that time (on by default
  once you give it a time); a task with only a date can remind you at 9:00
  that day. You get a toast (held in Quiet Mode), an entry in the notification
  bell, and a message on RetroArch's own line during a game. Reminders wait
  until the clock is trusted and are never replayed after the device was off —
  overdue tasks show in the warning colour instead.
- **Ring as alarm** adds a one-time alarm with the task's name to **Alarms**;
  finishing, deleting or moving the task updates or removes it.
- **Repeating** tasks move to their next date when you tick them off.
- **Calendar:** days with open tasks wear a small box in the clock's
  calendar, and the day's agenda lists them with a checkbox under its events.
- **From your phone:** choose **From your phone** and scan the code, or open
  the Phone Companion and use its **Tasks** card: type a task, pick the list
  and optionally a day and time, and it is on the handheld at once; tick off
  or delete tasks there too.

Tasks are kept in `tasks.json` in JukaHub's data folder (crash-safe writes with
a `.bak`), and **Backup & restore** carries them.

## Voice Memos

Record, name, replay and send short recordings.

- **First visit — Microphone test.** The test opens the microphone (it names
  the device it used), asks you to say something, records a 3-second sample
  and plays it back. It ends with one result: *works* (or *works, but quiet*),
  *opens but hears nothing*, or *no microphone found*. You can skip it; it
  stays in the X menu and on the empty list. Details go to the log under
  "mic".
- **Record** with **Y** (R on a keyboard) or **A** on the record card. The card
  shows the time, a live waveform and the level; **X** (Space) pauses and
  resumes; **A** or **B** stops and saves. A memo is saved as "Memo 1",
  "Memo 2" … and the keyboard then offers a better name (B keeps the default).
  A recording stops by itself at 30 minutes, when the card has under 20 MB
  free, when the handheld sleeps, or when you leave the app — always saved.
- **Play:** **A** plays the focused memo and pauses it again; **L1 / R1** or
  **Left / Right** jump 5 seconds; **B** stops. Clicking the progress bar
  seeks.
- **X — Options:** Play, Rename, Send to phone, Delete (asks first),
  Microphone test, and where the memos are kept.
- **Send to phone** shows a QR code: scan it with the phone's camera on the
  same Wi-Fi and the phone downloads the WAV from the handheld through the
  Phone Companion (protected by its PIN). On a PC the memos are outside the
  companion's shared folder, so the app says where the file is instead.
- **Files:** 16 kHz mono WAV in `VoiceMemos` at the top of the SD card (also
  shared over FTP and in the Phone Companion's Files card), or
  `Documents/JukaHub/VoiceMemos` on a PC. A recording in progress is a hidden
  `.recording-….wav` in the same folder, so a crash or a flat battery loses
  nothing: the next visit turns a cut-off recording into "Recovered memo".
- While a memo records or plays, the music player, a playing video and the
  menu music pause, and they come back afterwards.

## Paint

Pixel art, sketches and screenshot markup.

- **Start screen:** **Continue** (your last drawing, autosaved), new pixel-art
  canvases of 16×16, 32×32, 64×64 and 128×128, a white **sketch** the size of
  the screen, and **Open a screenshot or drawing** — the newest pictures from
  JukaHub's screenshots, the card's Screenshots folder and Paint's own
  drawings, with a preview. In the picture viewer, **Start** (M on a
  keyboard, or the *Mark up* chip) opens the picture on screen in Paint.
- **Drawing:** the D-pad moves the cursor one pixel (hold it and it speeds
  up); the left stick glides it — the further you push, the faster. **A**
  draws while held with the Pencil or the Eraser. The Line, Rectangle,
  Circle and Arrow tools take two presses — one corner, move, the other corner
  — and show the shape while you move (**B** cancels it). Fill paints an area
  of one colour; the Eyedropper picks a colour from the picture.
- **X** opens the tools (Left / Right there change the brush: 1, 2, 4 or 8
  pixels), **Y** the colours (PICO-8's sixteen, white, and bright markup red
  and yellow), **L1 / R1** undo and redo, **L2 / R2** zoom. Pixel art zooms in
  whole steps and shows a grid from 6×. On a screenshot the eraser brings back
  the original pixels, so a mistake simply disappears.
- **Select** opens **File**: **Save** / **Save as…** writes a PNG to
  `Pictures/Paint` (on the handheld `/mnt/SDCARD/Pictures/Paint`, visible over
  FTP and in the Phone Companion's Files; on a PC
  `Documents/JukaHub/Pictures/Paint`). A marked-up screenshot is saved as
  "‹screenshot› markup.png" — the screenshot itself is never changed.
- **Use as wallpaper** saves a screen-sized copy next to the drawing and sets
  it. **Use as game cover** lets you pick a game — the game a screenshot came
  from is preselected when JukaHub can tell — and saves the cover where the
  cover chooser does. Pixel art is enlarged without blur for both.
- Work in progress is saved when you leave Paint, when JukaHub closes, and
  every 45 seconds while you draw; **Continue** brings it back.

## Stopwatch

Two tabs; **L1 / R1** (Tab) switch them, and the app reopens on the last one.

- **Stopwatch:** **A** starts and pauses, **X** takes a lap while it runs,
  **Y** clears it once paused. Laps are listed newest first with their own
  time and the running total; the fastest and the slowest are marked. Up to
  200 laps are kept.
- **Countdown:** while it is being set, **Left / Right** pick hours, minutes or
  seconds and **Up / Down** change them (hold to go faster), or **Y** steps
  through 1, 3, 5, 10, 15, 25 and 60 minutes. **A** starts and pauses, **Y**
  resets. At zero it chimes, shows "Time's up" and files a notification;
  Quiet Mode keeps it silent (the notification is still filed).
- **Over a game:** both clocks keep running anywhere in JukaHub and while you
  play. During a RetroArch game started from the library, RetroArch's own
  message line shows "Time's up" and warnings at 5 minutes, 1 minute and 10
  seconds left. A started clock gets a row in the in-game menu (hold
  **Select + L2**): **A** starts or pauses it, **Right** takes a lap.
- **Quick Settings** has a **Stopwatch** button that shows the running time and
  opens the app.
- Closing JukaHub saves a running clock as paused, with its time. The clocks
  are immune to the handheld's clock jumping when network time arrives; time
  spent asleep is not counted.

## Solitaire

Klondike and FreeCell, with undo and a game you can leave and resume at any
moment. The game's **Start** menu has a **Game** row that switches between them;
each keeps its own deal and its own statistics.

- **Goal:** build the four foundations up by suit from ace to king. In the
  seven columns, build down in alternating colours; only a king goes into an
  empty column. A face-down card turns over when it is uncovered.
- **Playing:** move the focus with the D-pad. **A** lifts the focused card and
  **A** again puts it down where the focus is; in a column, **Up / Down**
  choose how many face-up cards to lift. **B** puts held cards back. **X**
  turns the stock from anywhere (one card, or three with *Draw 3*); when the
  stock runs out, X turns the waste back over — as often as you like. **Y** is
  the smart move: the card goes up to its foundation if it can, otherwise to
  the best column. **L1 / R1** undo and redo, all the way back to the deal.
- **Auto-move** (on by default) sends cards that are safe to play to the
  foundations by themselves: aces and twos, and higher cards once both cards
  of the other colour one rank lower are up. When the stock and the waste are
  empty and every card is face up, the game finishes itself (with auto-move
  off, press **X** *Finish*).
- **FreeCell:** eight columns dealt 7/7/7/7/6/6/6/6 face up, four cells and
  four foundations. Numbered deals are the published Microsoft ones, so a
  deal number means the same cards here as elsewhere. Any card may fill an
  empty column. A run can move only as many cards as the free cells and empty
  columns allow: (cells + 1) × 2^empty columns, halved onto an empty column
  the run itself fills. **Y** uses a free cell only when no foundation or
  column will take the card. There is no stock to draw.
- **Menu (Start, or M):** Resume, New game, Restart this deal, Draw 1 / Draw 3,
  Auto-move on / off, Game, Statistics, Reset statistics, Quit. New game,
  Restart, Game and Reset ask for a second A while a game is in progress.
  Changing Draw applies at once if you have not moved yet, otherwise from the
  next deal.
- **Saved automatically:** the game, its undo history, the clock and your
  statistics are saved after every move and whenever you leave, in
  `solitaire.json` in JukaHub's data folder. Open Solitaire again and you are
  exactly where you stopped. The clock only runs while the table is on screen.
- **Statistics:** games played and won, win rate, current and best streak,
  best time and fewest moves.
- With **Reduce motion** on, cards jump instead of flying and a win shows its
  result straight away.

## Create ZIP (Files)

In **Files**, mark files and folders with **X** (or stand on one), press **Y**
and choose **Create ZIP**. Name the ZIP (it starts as the item's or the
folder's name) and press **Create**. The ZIP is made in the background in the
same folder; the "Zipping" row shows how far it is, and pressing it twice
stops it (nothing is left behind). JukaHub checks there is room on the card
first, never follows links, keeps each file's date, and stores
already-compressed files (pictures, music, video, archives, CHD/CSO, APKs) as
they are while compressing the rest quickly, so the handheld stays usable. A
name that is taken gets " (2)", and when the ZIP is done the cursor lands on
it. **A** on the ZIP extracts it again.

## Notes

**Notes** keeps notes in folders, with search (`notes.json` beside JukaHub).
A note is light Markdown: a line starting with `# ` is a heading, `## ` a
coloured subheading, `- ` a bullet, `> ` a quote, and ```` ``` ```` fences a
block of code. Long lines wrap.

**Typing on a handheld.** With no keyboard, the on-screen keyboard types the
note. **Y** makes a new note with the keyboard already on its title ("New
Note" selected, so the first letter replaces it); its **Next** key moves on to
the body. In an open note, **Up**/**Down** move between the title and the body
(and scroll a long body), **A** opens the keyboard on the one in focus,
**L1**/**R1** move the note to another folder, and **B** saves and goes back
to the list. On the body the keyboard's action key is **New line**, and
**Start** (or **Y**) is **Done**; the note stays readable above the keyboard,
scrolled so the caret is in view, and the keyboard's own field shows the line
the caret is on. However the keyboard closes, the note is saved. Select all,
Cut and Clear are off on a note's body: one press would take the whole note
away (Copy still copies it).

With a keyboard, type straight into the note: **Enter** or **Tab** moves from
the title to the body, **Ctrl+Up** back, **Ctrl+S** saves, **Ctrl+M** opens
the note's menu, **Esc** saves and goes back (see [CONTROLS.md](CONTROLS.md)).

**Files.** In an open note, **Y** (or **Select**) → **Save as file** writes it
as plain UTF-8 text to `Notes/<folder>/<title>.txt` at the root of the SD
card (beside JukaHub.exe, in its `shared` folder, on a PC) — where a PC
reading the card, the FTP server and the phone's Files card find it. Saving
the same note again updates the same file, renamed with the note when its
title or folder changed; two notes with the same title get `Title (2).txt`,
and a file JukaHub did not write is never replaced. On the list, **Select** →
**Every note as a file** writes them all (Ctrl+S with a keyboard).

**Your phone.** **Select** → **Open on phone** on the list (or **Open on
phone** in a note's menu) shows a QR code that opens the Phone Companion's
**Notes** page — also reachable from the companion page's **Notes** card.
There you can read every note, write new ones and finish old ones with the
phone's keyboard, add `.txt` files as notes, and download a note as a `.txt`
or all of them as a `.zip`. Changes appear on the handheld at once. Nothing is
overwritten unseen: a note changed on the handheld while you edited it on the
phone is not saved over (the page offers to keep both), and a note changed on
the phone while it was open on the handheld keeps both versions too — the
handheld's typing on the note, the phone's as "… (phone copy)". The page sits
behind the companion's PIN like every other card.

**X** on the list deletes the highlighted note after asking; so does
**Delete note** in a note's menu. A file saved from the note stays.

## Calculator, Alarms and Pomodoro

- **Calculator** is a scientific calculator: the usual precedence (`−2²` is
  −4, `2^3^2` is 512), implicit multiplication (`2π`, `3(4)`), n!, `%` the way
  phone calculators add it (`200 + 10%` is 220), degrees or radians, **2nd**
  for inverse trig, eˣ, 10ˣ and roots, and **Ans**. Answers have twelve
  significant digits (0.1 + 0.2 is 0.3); an error names itself and leaves the
  sum to fix. The last 30 sums are kept (`calculator.json` beside JukaHub):
  Right from the keypad walks into them, **A** uses an answer and **X**
  brings a sum back. **Y** draws `y = f(x)` (±360° in degrees, ±10 in
  radians). With a keyboard, type the sum and press **Enter** (see
  [CONTROLS.md](CONTROLS.md#calculator)).
- **Alarms** wake you with a tone or your music; the clock in the status bar
  opens them too.
- **Pomodoro** times 25-minute focus sessions with short and long breaks.
