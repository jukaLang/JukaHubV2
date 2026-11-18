# Juka AI's device tools

Juka AI (Chat → **Juka AI**) is more than a chatbot: it can look at the
handheld to answer, and — when you say OK on screen — fix things. Ask "Why
won't my last game start?", "Free up space" or "Make games run smoother" and
Juka checks the device first instead of guessing.

Code: `player/aitools*.go` (the tools), `player/aiagent.go` (the loop),
`player/aiagent_view.go` (chips, the confirmation card, the voice orb).
Unverified on device as of 2026-11-17: built and checked statically only.

## What Juka can use

| Tool | What it does | Risk |
|---|---|---|
| `device_status` | Model, OS, JukaHub version, battery and charging, temperature, CPU clock and governor, power mode, memory, storage per card, Wi-Fi and Bluetooth radio | read |
| `read_logs` | JukaHub's own log, `errors.txt`, or the last game's RetroArch log; filter by text or subsystem | read |
| `network_check` | Online state, Wi-Fi signal (never the network's name), DNS, one HTTPS request, Bluetooth radio | read |
| `list_processes` | The processes using the most memory | read |
| `diagnose_game` | The "why won't it start?" checks from the game's options sheet: game file, emulator and core, BIOS, last launch's log. No arguments = the game launched last | read |
| `list_games` | Consoles and game counts, or games matching a title | read |
| `list_dir`, `find_files`, `disk_usage` | Folders, name search, identical copies (duplicate ROMs), what fills a folder | read |
| `read_text_file` | A text file (configs, logs, scripts); binary files and files with keys or passwords are refused | read |
| `search_settings`, `get_setting` | Settings rows, their values and choices | read |
| `retroarch_get` | RetroArch options, globally or in a core's override file | read |
| `performance_get` | Power mode, automatic CPU speed, clock, temperature | read |
| `mail_inbox` | The newest messages of the mail accounts in Email & calendar, unread first: sender, subject, date, a 200-character snippet ([EMAIL_CALENDAR.md](EMAIL_CALENDAR.md)) | read |
| `calendar_agenda` | The synced calendars' events for the coming days (1-31): time, title, place, calendar | read |
| `open_screen` | Adds an "Open …" button under the answer (a Settings row or a screen) | read |
| `set_setting` | One Settings switch or choice, through the row itself | change |
| `performance_set` | Power mode and/or automatic CPU speed | change |
| `retroarch_set` | One RetroArch option, globally or for one core; a backup of `retroarch.cfg` is kept | change |
| `move_files`, `rename_file` | Move or rename on the same card | change |
| `delete_files` | Moves into the card's trash folder `.jukahub-trash/<time>/` (frees nothing until emptied) | change |
| `delete_files` with `permanent` | Deletes for good (also how the trash is emptied) | destructive |
| `stop_process` | Stops a process (system processes and JukaHub are refused) | destructive |
| `run_command` | One shell command, 20 s limit, output capped — **off** unless you turn it on | destructive |

## Asking first

- **read** tools run without asking. Each shows as a short line in the
  answer ("Checked storage · Read 40 log lines").
- **change** tools stop on a card in the chat: **A Allow**, **B Deny**,
  **Y Allow for this conversation** (Enter / Esc / Y on a keyboard, or click).
  "This conversation" ends at **New chat** (Y on the tab) or when the Juka AI
  history is cleared; a card still up then counts as no.
- **destructive** steps always ask, one at a time, even after Y; the card
  says it can't be undone and shows the exact command for `run_command`.
- A card nobody answers in 3 minutes counts as no. Juka is told what you
  decided; it never claims a change it did not make.
- Never changed by Juka: API keys, tokens, passwords, resets and erases, the
  three switches below, RetroArch's folders, paths, drivers and accounts.

Settings → **Voice & AI**:

- **Juka can use device tools** (on) — off makes Juka a plain chatbot again.
- **Let Juka change settings and files** (on) — off = Juka only looks.
- **Let Juka run terminal commands** (off) — for experts.

## Where files can be reached

The SD card (on a PC: your home folder), the Files root unless it is a whole
drive, the ROM folders and JukaHub's data folder. Never a system folder
(`/etc`, `/usr`, `/proc`, `C:\Windows`, …), and links are judged by where they
point. Moving or deleting is refused for a storage root, its top-level
folders (Roms, BIOS, Apps…), JukaHub's own program and data, any folder that
holds one of those, and key files (compared ignoring upper/lower case). Nothing
is moved into JukaHub's program or data folder. `run_command` takes one line
of at most 160 characters, so the card shows all of it.

## Privacy: what leaves the device

With a Groq key, your question, the conversation so far and **the results of
the tools Juka used** go to Groq (api.groq.com) to write the answer — e.g.
file names and sizes, log lines, setting values. Before anything is sent,
results are scrubbed: stored keys and tokens (the Groq key, the RomM and
Discord tokens, saved passwords), `key=value` credentials, bearer tokens,
Wi-Fi `psk` lines, URL passwords and your home folder's account name. The
status and network checks leave out the Wi-Fi network's name and the device's
IP address; log lines and file lists can still name games, folders and
networks. Turn off **Juka can use device tools** to send only what you type.

## Offline: "lite" mode

The handheld's own model (SmolLM2-135M, 384-token context — see
[LOCAL_AI.md](LOCAL_AI.md)) is too small to call tools or read their results.
Without a key, or offline with a device question, Juka picks the read-only
tools by keywords (storage, Wi-Fi, battery, a game that won't start,
duplicates, errors, settings), runs them, and shows what they found under
**"Offline — Juka can look but not change things."** Other questions offline
wait for the connection as before (or get one sentence from the offline
model when there is no key).

## Using it without typing

- Chips above the composer: six starters, then after each answer up to three
  follow-ups the model suggests, plus "Open …" when Juka points at a screen.
  Up from the composer reaches them; Left/Right; A asks.
- **R2** (keyboard **M**) on the Juka AI tab opens the keyboard already
  listening: speak, then Start sends.
- The voice orb sends device questions ("why won't my game start", "what's
  filling my card") through the same tools, **read-only**: nothing is changed
  from the orb. The in-game assistant stays read-only and has no tools.
- Other screens can ask through `aiAgentAskFromDeck(prompt)`, which opens
  Chat on Juka AI and sends the question.
