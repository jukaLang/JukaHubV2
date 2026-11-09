# Changelog

Notable changes to JukaHub. The format is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow the
`Version` field in [`player/jukaconfig.json`](player/jukaconfig.json), which is
what the About screen and the updater read.

This file starts at the point it was added, so it does not backfill history.
For anything older, the git log and the
[GitHub releases](https://github.com/jukaLang/JukaHubV2/releases) are the record.

## [Unreleased]

### Added

- **Settings → Date & time.** One place for the clock: *Set time
  automatically*, *Time zone*, *12-hour clock* (moved here from Display &
  theme, same setting), *Date format* (Auto from your region, 9 Nov 2026,
  Nov 9, 2026, 2026-11-09 or 09.11.2026) and *Clock last set*, which says when
  and how JukaHub last set the clock. On Windows, macOS and Linux desktops the
  zone and clock rows show what the system uses and open its own settings.
  Home search finds it under "date", "time zone" or "12 hour"
  ([player/datetime.go](player/datetime.go),
  [player/setmodel.go](player/setmodel.go),
  [player/clockformat.go](player/clockformat.go)).

- **A time-zone picker on the handheld.** The IANA time zones grouped by region
  with their current UTC offset, the zone in force ticked, and search by city,
  country or an old name (Calcutta, Kiev) — type on a keyboard, or press Y for
  the on-screen keyboard; L1/R1 jump between regions. *Set automatically*
  takes the zone from the same location lookup the weather uses, and never
  overrides a zone you picked while it was looking. The zone applies to
  JukaHub at once and is kept on the card (`jukatimezone`); `launch.sh`
  exports it as `TZ`, so games started from JukaHub use it from the next
  start. The zone database is built into the Linux binary, so this works on
  images without `/usr/share/zoneinfo`
  ([player/tzpicker.go](player/tzpicker.go),
  [player/tzzones.go](player/tzzones.go),
  [player/tzdata_linux.go](player/tzdata_linux.go),
  [player/launch.sh](player/launch.sh)).

- **The clock sets itself from the network.** Each time Wi-Fi comes online,
  and hourly while it stays, JukaHub asks an internet time server
  (pool.ntp.org, then Cloudflare and Google; the HTTPS date of the
  connectivity check if none answers) and sets the clock when it is more than
  two seconds off — only on the handheld, where nothing else does. A
  correction of more than ten minutes (counted with the unconfirmed ones
  before it, of which ten minutes a day are forgiven) is made only when the
  HTTPS date confirms it, so a forged time-server answer on the Wi-Fi cannot
  move the clock far; on a firmware with no CA certificates at all, where
  HTTPS cannot be checked, a clock still dated before this release is set from
  the time server alone. A failed attempt is retried after 1, 2, 5, 10 and 30
  minutes.
  Windows, macOS and Linux PCs keep their own time; JukaHub never asks a time
  server there by itself. Nothing is sent offline, in airplane mode or behind
  a captive portal. After the clock or the zone changes, a snoozed alarm moves
  with the clock and still rings, and playtime recorded under a wrong date
  moves to the right day (never more than the change can have misdated)
  ([player/ntpsync.go](player/ntpsync.go),
  [player/clockjump.go](player/clockjump.go),
  [player/netmon.go](player/netmon.go)).

- **JukaHub saves a crash report and offers it at the next start.** Every
  session hands a crash file to the Go runtime, so a panic on any goroutine or
  a fatal error is written down next to the logs (TrimUI: the app folder beside
  `errors.txt`; Windows: `%LOCALAPPDATA%\JukaHub\logs`). The next start files it
  as `crash-<time>.txt` (the newest five are kept, and the newest is never
  pruned even when the clock is behind) and shows "JukaHub closed
  unexpectedly" with Send report (Report a bug, prefilled; nothing is sent
  until you press Submit), Restart in safe mode and Recovery options
  ([player/crashreport.go](player/crashreport.go),
  [player/recoverycard.go](player/recoverycard.go)).

- **The recovery chord opens a recovery card.** Holding L + R + Start
  (Ctrl + Alt + R on a keyboard) while JukaHub starts no longer replaces the
  binary on its own; it offers the previous version (the one released before
  this build in Version history; on older installs the build an update backup
  kept, with Restart now), the last good settings (what the last healthy start
  read, then the settings' `.bak` or the newest weekly backup), staying in
  safe mode, and Reset settings (keep games and saves). The settings choices
  ask first and write a safety backup before anything changes, a crash report
  filed at that start can be sent from the same card, and holding the chord
  keeps the automatic rollback away: the card says the last starts failed and
  offers the previous version first. It works with the pad, a keyboard and a
  tap ([player/safeboot.go](player/safeboot.go),
  [player/recoverycard.go](player/recoverycard.go),
  [player/recoverysettings.go](player/recoverysettings.go)).

- **Three failed starts in a row start safe mode by themselves**, whatever the
  update history, with a card that says why and how to leave it. A start
  counts as healthy once it has drawn its first frame and run for 90 seconds,
  or quit cleanly, and a start that was asked to stop is not counted; the
  check runs before the settings load, so a crash
  while loading them counts too ([player/bootguard.go](player/bootguard.go)).

- **A healthy start keeps the settings it started with.** A start that runs
  90 seconds outside safe mode keeps the settings files it read at boot in
  `patch/lastgood/`; *Last good settings* on the recovery card offers them
  first, because the `.bak` beside each file is rewritten by every save
  ([player/bootguard.go](player/bootguard.go),
  [player/recoverysettings.go](player/recoverysettings.go)).

- **Roll back a package.** The previous version of an updated package is kept,
  and its page offers **Roll back to v…**, which swaps the two; the button then
  reads **Switch back to v…**. Files the app or you wrote into its folder
  follow the installed version — if some cannot move, the next update says
  which, and after three tries keeps that old copy whole in
  `packages/.jukahub-rollback/` rather than block or delete it (as it also
  does when the installed version's list of its files cannot be read) — and an
  install cut short by a power loss is finished or undone the next time
  Packages opens. Removing
  a package moves every copy into the trash by rename before deleting it
  ([player/pkgstage.go](player/pkgstage.go),
  [player/pkgstate.go](player/pkgstate.go), [player/pkgui.go](player/pkgui.go)).

- **Save states made with another core build are flagged.** JukaHub now
  records which build of each core was active when, and a game's page says in
  one line beside **SAVE STATES** when some states were written by a build
  other than the installed one — the cue to roll the core back with **R1**.
  Times from a clock that was wrong (before 2020, as the handheld reads before
  it reaches the network) are never used
  ([player/corebuilds_history.go](player/corebuilds_history.go),
  [player/gmeta_ui.go](player/gmeta_ui.go)).

- **See who is connected to FTP and SFTP.** The Network tools page lists every
  connected computer, since when and what it is uploading or downloading, and
  the tiles show "· 2 connected"
  ([player/nettoolsscreen.go](player/nettoolsscreen.go)).

- **Android games can present through the firmware's own SDL2.** A public
  report from the stock TrimUI firmware says raw EGL contexts on SDL's `mali`
  display stayed black while SDL-created ones showed, so AndroidRT has a new
  present backend, `ANDROIDPAK_PRESENT=sdl`: the firmware's
  `libSDL2-2.0.so.0` (loaded at run time, never linked; `ANDROIDPAK_SDL_LIB`
  overrides) creates the fullscreen window, its EGL surface and the GLES
  context, and the game gets exactly those — `eglMakeCurrent` becomes
  `SDL_GL_MakeCurrent`, every `eglSwapBuffers` an `SDL_GL_SwapWindow`, and SDL
  is pointed at the game's own libEGL and the GLES library beside it, with its
  signal handlers off so JukaHub can still stop the game. `auto` switches to
  it by itself when the native backend fails — the game's own `eglInitialize`
  of the default display fails (SDL starts there and the call is made once
  more) or EGL refuses the window surface — and logs `backend sdl (fallback:
  …)`; a game that made its GLES context before that window keeps it as a raw
  EGL context, and the log names `ANDROIDPAK_PRESENT=sdl` for it.
  Mock-tested with a fake firmware SDL2 (186 smoke assertions); not yet run on
  a device ([player/androidrt/src/eglpresent.c](player/androidrt/src/eglpresent.c),
  [player/androidrt/src/sdlhost.h](player/androidrt/src/sdlhost.h),
  [docs/ANDROID.md](docs/ANDROID.md)).

- **Hints follow Accessibility's button remaps.** With the Nintendo layout, an
  A/B or X/Y swap or a per-button remap, every hint now names the physical
  button that does the job ("B Open" when A and B are swapped) instead of the
  button that now does something else. An action no button sends any more
  (its button switched off, or moved to another job with nothing in its
  place) reads "—", and an alternative with no button is dropped ("B / Start"
  with Start switched off reads "B"). Screens that keep hint text between
  frames (keyboard languages, clipboard history, the on-screen keyboard, the
  Wi-Fi and Network tools sheets, the recovery card) rebuild it when the remap
  changes. Keyboard keys, the Select + Start quit chord and the controller
  test (which read physical buttons) are left as printed
  ([player/inputhints.go](player/inputhints.go),
  [player/inputhints_test.go](player/inputhints_test.go)).

- **Sentences that name a button speak the hints' words.** A new
  `hintSentence` helper writes prose such as "Press A to retry" as "Press
  Enter to retry" on a keyboard and follows a remap; the empty states and
  status lines of Media, Live TV, Files, Downloads, the arcade, Alarm,
  Accessibility, Compatibility, Game profiles, Curation, Patch, the browser's
  link, saved-page and cookie pickers and site info now use it
  ([player/inputhints.go](player/inputhints.go) and the screens' files).

### Changed

- **A panic no longer exits as a normal quit.** It exits with status 2 after
  the cleanup still ran (the running game stopped, LEDs and CPU governor put
  back), bounded to 5 seconds — the deadline is armed before anything is
  written — so a crash cannot leave a frozen frame, and `launch.sh` writes
  "JukaHub CRASHED" (or the startup failure, or the signal that ended it) into
  `errors.txt` instead of a bare exit code, leaves that status in
  `.last-exit` for the boot guard, and does not relaunch after a crash ([player/crashreport.go](player/crashreport.go),
  [player/main.go](player/main.go), [player/exitguard.go](player/exitguard.go),
  [player/launch.sh](player/launch.sh)).

- **Core updates ask before they install.** Settings → Games & emulators →
  **Core updates** is now Off / Notify me / Install automatically, default
  Notify me: the daily check says once which cores have a different build and
  installs nothing. An explicit off stays off and an explicit on becomes
  Install automatically; the setting is stored once at start-up, with the old
  switch beside it. Each fetched build's SHA-256 is recorded beside it; the
  buildbot publishes only CRC32 checksums and no stable per-core tree for this
  device, which the docs now say plainly
  ([player/emulatorautoupdate.go](player/emulatorautoupdate.go),
  [player/setmodel.go](player/setmodel.go),
  [player/corebuilds_ui.go](player/corebuilds_ui.go),
  [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md)).

- **A failed Android game's toast names the Java piece it was missing.**
  After a run that ended badly, JukaHub already named the first missing bionic
  symbol; when the game got as far as running, the toast now names the first
  Java method it called that AndroidRT does not implement (else the first
  class it could not get, else the first member it looked up), read from the
  runtime's `[androidrt][jni] report:` — or, when the game crashed before
  printing it, from the first-seen log lines. Loader traps and load failures
  still come first; table-tested
  ([player/androidpak.go](player/androidpak.go),
  [player/androidpak_report_test.go](player/androidpak_report_test.go)).

- **AndroidRT builds without a single compiler warning.** The
  `-Wformat-truncation` spots now check what `snprintf` wrote: an over-long
  asset name or library path is "not found" instead of a cut name that could
  match another file, and an activity name that does not fit is "unknown"
  ([player/androidrt/src/androidshim.c](player/androidrt/src/androidshim.c),
  [player/androidrt/src/apkmeta.c](player/androidrt/src/apkmeta.c),
  [player/androidrt/src/bionicld.c](player/androidrt/src/bionicld.c)).

- **Dates follow one format.** The alarm clock screen and the idle clock dock
  write the date in the order chosen in Settings → Date & time instead of two
  different hard-coded layouts; Network tools' clock check uses the same
  four-timestamp SNTP client as the clock sync, and its Device line is the
  device time of the exchange that answered
  ([player/clockformat.go](player/clockformat.go),
  [player/saver_status.go](player/saver_status.go),
  [player/nettools2.go](player/nettools2.go)).

- **The controls guide lists the keyboard keys screen by screen.**
  `docs/CONTROLS.md` gains a "Keyboard keys, screen by screen" table (pad
  buttons → keyboard keys for about 40 screens) and a paragraph on keyboard
  twins and Accessibility remaps (an action no button sends shows "—"); the
  in-app keyboard sheet lists the new browser, video and alarm keys
  ([docs/CONTROLS.md](docs/CONTROLS.md), [player/shortcuts.go](player/shortcuts.go)).

- **A damaged config no longer stops JukaHub when its backup is good.**
  `LoadLastKnownGood` loaded the `.bak` and still returned an error, and
  startup treated that as fatal before safe mode or the recovery chord could
  run. The backup is now a successful launch: the broken file is kept as
  `jukaconfig.json.corrupt-<time>`, a toast says the settings were restored,
  and a test feeds a truncated config plus a good backup
  ([player/config_health.go](player/config_health.go),
  [player/main.go](player/main.go)).

- **macOS and Windows metadata no longer appear as games, albums or files.**
  Hidden entries (`.DS_Store`, the `._Name` AppleDouble companions),
  `__MACOSX` and `desktop.ini` are skipped by the game library, the media
  index and the text browser through one shared filter
  ([player/filejunk.go](player/filejunk.go)).

- **Scene-change toasts name the screen, not its internal ID** — switching to
  General no longer flashes "SettingsGeneral"
  ([player/core.go](player/core.go)). The trophy card's caption asks for the
  Caption role's size instead of 12 px, which the font helper lifted anyway
  ([player/trophy_celebrate.go](player/trophy_celebrate.go)).

- **The remaining package-level regexps compile on first use.** The 32
  patterns in `compat.go`, `core.go`, `media_paging.go`,
  `nettoolsscreen.go`, `podcasts.go`, `plugx.go`, `sectrustdoc.go`,
  `toolssheet.go` and `webscript.go` now sit behind `sync.OnceValue`, so
  startup does not pay for screens and features that may never open.

- **Synthetic input events are safe on every path.** Events the app pushes
  into SDL itself — the phone Companion's buttons, the left stick's D-pad
  presses, held-direction repeats and touch taps — were bare Go structs that
  `SDL_PushEvent` read as 56-byte `SDL_Event`s, over-reading 20–40 bytes, and
  presses arrived with `State` 0 (a release). Every push now goes through one
  padded constructor that sets the state (`player/sdlevents.go`), the four
  call sites use it, and the race job exercises the queue
  ([player/sdlevents_test.go](player/sdlevents_test.go)).

### Removed

- **The invented "social notifications" feed is gone.** With
  `socialNotifs` on, the app generated fake "Storage 80% full" and "New
  follower" alerts in the real notification centre. The generator, its
  settings row, its panel and its `socialNotifs` key are deleted; older
  configs drop the key instead of re-saving it
  ([player/social.go](player/social.go) removed).

### Fixed

- **The boot guard can now roll back a bad update on a handheld without a
  clock.** It counts starts instead of comparing saved clock times, treats a
  build as new for its first ten starts, and runs the rollback through Version
  history after the update state is loaded — before, it read the update time
  before that state was loaded and could never fire. A rollback needs at least
  one real crash among the failed starts — a crash report, or the exit status
  `launch.sh` now leaves for the next start in `.last-exit` (a crash, a fatal
  error, an abort, a segfault or an out-of-memory kill; without it, a start
  that died before its first frame) — so power-offs and quits in a working
  session never roll an update back; a start that was asked to stop (power
  button, stop signal, Quit) is not a failed start even when the exit watchdog
  has to force it during a slow splash; a loop gets one automatic rollback (a restored build
  that fails too gets safe mode); the target is the version released before the
  running one, not the newest saved; and a quit is recorded as soon as the main
  loop ends, before the teardown ([player/bootguard.go](player/bootguard.go)).

- **Rolling back to a backed-up executable works while JukaHub is running.**
  The copy lands beside the binary and is renamed over it, instead of writing
  into a running executable, which Linux and Windows refuse; on Windows the
  replaced build, moved aside as `.old`, is deleted at the next start
  ([player/safeboot.go](player/safeboot.go)).

- **A safety backup can no longer be deleted right after it is written.** A
  backup made before a recovery restore, a reset, a Backup & restore or the
  weekly backup was pruned with the others by file time, so on a handheld
  whose clock was behind the new archive looked oldest and went first; the
  prune now always keeps the one just written ([player/bkup.go](player/bkup.go)).

- **First-run "Connect to Wi-Fi" and "Set your time zone" work on the
  handheld.** Both buttons tried `gnome-control-center` /
  `nm-connection-editor`, which do not exist on TrimUI, so on a new device they
  were dead (Wi-Fi showed a raw `exec … not found` toast). They now open
  JukaHub's Wi-Fi page and the time-zone picker, and the tour waits until you
  come back; desktop tools are used only on a real Linux desktop
  ([player/onboarding.go](player/onboarding.go)).

- **An alarm can no longer be silenced by a clock that ran ahead.** A wrong
  clock could mark an alarm done for a day that had not come yet; after
  network time corrects the clock, such alarms are re-armed
  ([player/clockjump.go](player/clockjump.go), [player/alarm.go](player/alarm.go)).

- **On a Linux computer the file servers no longer take your home folder for
  the SD card.** JukaHub run from `~/Apps/JukaHub` made FTP share
  `~/Documents` and `~/Downloads`; the card layout is now trusted only on an
  identified TrimUI device ([player/fileserver_guard.go](player/fileserver_guard.go)).

- **FTP folders show as folders.** Directory listings marked every folder as a
  plain file, so FTP clients offered to download folders instead of opening
  them; old files now show their year
  ([player/ftp_server.go](player/ftp_server.go)).

- **An endless FTP command line can no longer exhaust memory.** The command
  reader collected a line until its newline, which anyone on the network could
  withhold before signing in ([player/ftp_server.go](player/ftp_server.go)).

- **A failed reinstall no longer removes the package.** Reinstall used to empty
  the folder before downloading; it is now an ordinary staged install, so the
  installed copy keeps working if the download fails
  ([player/pkgstate.go](player/pkgstate.go)).

- **Replacing an emulator core can no longer leave it missing or empty.** The
  old binary was deleted before the new one was renamed in, and the new one
  was written without a sync; it is now fsynced and renamed over the old one
  in one step, its backup is fsynced too, and a backup that failed is deleted
  instead of being offered as a rollback ([player/compat.go](player/compat.go)).

- **A buildbot index with a size column no longer reads as empty.** Lines with
  a fourth (size) field are accepted, on either side of the file name
  ([player/emulatorupdates.go](player/emulatorupdates.go)).

- **A game asking for a missing Java class every frame no longer grows
  memory.** `FindClass` added a report entry on every miss, so such a game
  leaked a little per call and its exit report repeated one class thousands of
  times; misses are now recorded once per name, and the report lists entries
  in the order the game asked
  ([player/androidrt/src/jnienv.c](player/androidrt/src/jnienv.c)).

- **Android games use exactly the EGL library the present layer chose.** The
  game's `libEGL.so` was resolved again through the library search path, which
  could pick another copy than the firmware's `/usr/lib/libEGL.so` (or none,
  for a GPU package outside the path); it is now bound to the present layer's
  own handle ([player/androidrt/src/eglpresent.c](player/androidrt/src/eglpresent.c)).

- **The browser's Search works from a keyboard.** Y or / opens the web search
  (the pad's Y), and the footer names "/" on a keyboard; the zoom hint names the
  minus and plus keys. Keys that open a keyboard (/, Y, F for Find, Space or A
  on the address bar) no longer type their own character into the field they
  opened, and a held one opens it once without typing its repeats; a key that
  types nothing (a Ctrl, Alt or Win chord, a Phone Companion or control-profile
  key) no longer swallows the next character typed
  ([player/browser.go](player/browser.go), [player/core.go](player/core.go),
  [player/extras.go](player/extras.go), [player/brw_offline.go](player/brw_offline.go)).

- **Keyboard X and Y in the video player do what the pad's X and Y do.** X
  toggles captions and Y the settings sheet (C and S still work); X used to
  fall through to the Media screen's search behind the playing video. Holding
  X, Y or F acts once instead of flickering captions, the settings sheet or
  full screen ([player/videoplayer.go](player/videoplayer.go)).

- **Footer hints that named only pad buttons now name their keys.** Playtime
  and the config inspector switch views with Tab, the image viewer zooms with
  the minus and plus keys and steps with PgUp / PgDn, the PDF reader steps
  chapters with [ ] and zooms with minus and plus, game details open options
  with O, Local play moves players with PgUp / PgDn, keyboard languages move
  with [ ], the Tools pages jump sections with PgUp / PgDn, and Patch's
  "Hold A" reads "Hold Enter"
  ([player/inputhints.go](player/inputhints.go),
  [player/trophy_room.go](player/trophy_room.go),
  [player/cfgdocui.go](player/cfgdocui.go),
  [player/imageviewer.go](player/imageviewer.go),
  [player/pdfreader.go](player/pdfreader.go),
  [player/gmeta_ui.go](player/gmeta_ui.go),
  [player/hotseat_ui.go](player/hotseat_ui.go),
  [player/vksettings.go](player/vksettings.go)).

- **Missing keyboard twins added.** Delete removes an alarm (the pad's L1, two
  separate presses: holding Delete does not count twice), Ctrl+Enter sends a
  chat draft (the pad's Start), X ends a Local play game and Y removes one
  player per press, X removes one keyboard language per press and Y adds one,
  and Left / Right page the Settings option sheet like the D-pad
  ([player/alarm.go](player/alarm.go), [player/chatinput.go](player/chatinput.go),
  [player/hotseat_ui.go](player/hotseat_ui.go),
  [player/vksettings.go](player/vksettings.go),
  [player/setsheet.go](player/setsheet.go)).

- **Holding X on the alarm list no longer flips the alarm on and off**
  ([player/alarm.go](player/alarm.go)).

- **The controller test speaks keyboard on a keyboard** ("Reset (Enter)", "Esc
  Close"); on the pad the chip reads "Reset" instead of "Reset (A)" (A only
  registers a press there; click or tap Reset), and its footer is translated
  with no remap, so its pad names stay physical
  ([player/ctrldiag.go](player/ctrldiag.go)).

- **A Wi-Fi login page no longer looks like internet.** The connectivity
  probe was a TCP dial to `1.1.1.1:80`, which a hotel or airport portal
  answers happily: the app said online while every request failed. It now
  requests an HTTP `generate_204` endpoint and requires a 204 with no body;
  a redirect or a page is reported as a captive portal and its sign-in URL
  is kept. The Wi-Fi page then shows a **Sign in** control (and
  "Connected · Sign in required") that opens the portal in the in-app
  Browser, while downloads and updates stay paused until the probe passes
  ([player/netmon.go](player/netmon.go),
  [player/portalsignin.go](player/portalsignin.go)).
- **Opening a link can no longer run a command on Windows.** A config
  trigger reached the shell as `cmd /c start "" <url>`, so `&`, `|` and `^`
  in the URL were parsed as cmd syntax and ordinary query strings were cut at
  the first `&`. Links now go through one allowlist (http, https, mailto) and
  `rundll32 url.dll,FileProtocolHandler`, with a test for
  `https://x/?a=1&calc` ([player/openurl.go](player/openurl.go)). The platform
  file opener uses the same shell-free handler.

- **An Xbox game with no launcher names the fix.** The Games Library now
  says "Install hakuX: Emulators → Xbox → X" where the device is offered the
  port, and the model's own reason on a Smart Pro S or Brick
  ([player/hakuxpage.go](player/hakuxpage.go)).

- **The metadata filter covers every scanner it was meant to.** The
  downloads library, package list, save snapshots, ambient photo slideshow
  and hakuX's Xbox folder now use the shared hidden/junk filter, so
  `._Name`, `__MACOSX` and `desktop.ini` no longer appear as content
  ([player/filejunk.go](player/filejunk.go)).

- **Links opened in the browser from another screen load.** A link from Chat,
  Home search, a paired device or a package page opened the Browser on
  "Loading …" and stopped there: the flag that keeps the start page from
  replacing the link also kept the link from loading once the screen arrived
  ([player/browser.go](player/browser.go)).

- **The video player's settings sheet answers taps where it is drawn.** The
  windowed player drew the sheet inside its own frame but hit-tested it as if
  it filled the screen, so a tap could change a setting it was not on; in full
  screen the sheet no longer fades out while the pad is still using it
  ([player/videochrome.go](player/videochrome.go), [player/videoplayer.go](player/videoplayer.go)).

- **A click no longer seeks a video that is not on screen.** The seek bar's
  last position stayed live after the player shrank to the corner window, so a
  click on the screen underneath could seek the floating video
  ([player/main.go](player/main.go)).

- **Ctrl+Q no longer switches the screen too.** The Q/E screen keys ignored
  modifiers, so the Quick Settings chord also stepped back one screen
  ([player/main.go](player/main.go)).

- **Pomodoro's keyboard matches its footer and the pad.** X skips the phase and
  Esc goes back; Esc used to skip, which left no key to leave the timer by
  ([player/pomodoro.go](player/pomodoro.go)). Likewise Esc now leaves the
  Calculator, which consumed every key and had no way out on a keyboard
  ([player/calculator.go](player/calculator.go)).

- **Notes' shortcut letters stay out of the text.** N (and now Y) start a new
  note and S opens search; the letter's own text event used to follow and land in
  the new title or the query ([player/notes.go](player/notes.go)).

- **Navigation runs at the frame rate it promises.** The main loop slept the
  whole frame budget *after* drawing, so the handheld's 30 fps profile ran at
  about 20 fps while navigating (17 in Low Power). The loop now subtracts the
  frame's own cost while something moves (`FrameRateController.Pace`), and a
  playing video keeps the active cadence even in Low Power instead of dropping to
  ~17 fps after 3 s and ~12 fps after 10 s without input
  ([player/adaptive.go](player/adaptive.go), [player/main.go](player/main.go)).

- **SDL batches draw calls on the device again.** Naming the `opengles2` render
  driver silently turns SDL's batching off, so every blit was its own GL draw
  call on the PowerVR GPU. Batching is requested explicitly on the handheld
  (Windows is unchanged) ([player/main.go](player/main.go)).

- **The live-builder pairing code is no longer in the repository.** The six-digit
  code that guards pushing a design over Wi-Fi was committed, so every clone
  shared it. It is generated per install, as the code always allowed, and is
  git-ignored now ([player/livebuilder.go](player/livebuilder.go)).

- **An EPUB without a title no longer crashes the app.** The reader indexed the
  title match without checking it, so a package document with no `<dc:title>`
  killed JukaHub; the file name stands in now ([player/reader.go](player/reader.go)).

- **The Hero grid fits the Brick.** That arrangement scaled its cards to the
  960-wide canvas twice, leaving a quarter of the screen empty and the gutters
  too thin for the focus ring; on the Hero grid (and on a Home whose tiles the
  owner moved) the D-pad now steps by the drawn geometry, and Up from the top
  row goes to the header instead of sideways ([player/homelayout.go](player/homelayout.go)).

- **An x86-only Android package is refused up front.** It used to be staged
  and launched, then fail inside the runner; it now gets the "no ABI this
  device can run" answer ([player/androidpak.go](player/androidpak.go)).

- **`errors.txt` stops growing on the card.** Every log line was mirrored to
  stderr, which `launch.sh` appends to `errors.txt` forever. Only warnings and
  errors reach it now, and the launcher rotates it to `errors.txt.1` past 1 MiB
  ([player/logging.go](player/logging.go), [player/launch.sh](player/launch.sh)).

- **The handheld's Appearance page has the clock row again.** The desktop
  config grew a Clock label and the 12-hour toggle, and the TrimUI config did
  not, so the handheld shipped an Appearance page two rows shorter and no way to
  switch the clock format ([player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json)).

- **The gallery dialog uses the screen.** `.gallery-content` asked for 1080px
  but inherited `.guide-content`'s 620px cap, so every layout stacked in a single
  column on a desktop and the thumbnails grew taller than the panel. It is
  1080px now (96vw on a small screen) and the grid lays out three cards per row
  ([style.css](style.css)).

- **Home's grid matches the cards again.** Left from Packages stepped over the
  Files card to the Continue hero, and Down from the full-width Media card
  skipped Files for Packages, so the tile that opens the file browser was the
  hardest thing to reach. Up/Down/Left/Right now follow the drawn rows, the
  Continue card's focus ring is four pixels outside a dark hairline so it reads
  over its own thumbnail, and a test pins the graph to the geometry
  ([player/homefocus.go](player/homefocus.go),
  [player/homelayout.go](player/homelayout.go);
  `player/homefocus_grid_test.go`).

- **B leaves Apps and Favorites.** The launcher grid and the favorites list take
  the pad while they are focused, and neither answered B, so on a handheld the
  only way out was a key the pad cannot send. `b` now works on a desktop
  keyboard too, where the grid has no text field to type into
  ([player/appgrid.go](player/appgrid.go), [player/favorites.go](player/favorites.go)).

### Security

- **The live builder can no longer be driven by a web page, a guess or a
  tricky design.** On the handheld it is off until Settings → Network → Live
  Builder turns it on; on a PC it listens on that computer only unless
  `JUKAHUB_LIVE_LAN=1`; everywhere it turns itself off after 30 minutes
  without use. The pairing code is shown on the device only (never on the
  status page, in a log or the notification history), travels in a header
  only, and is replaced from Settings or automatically when wrong codes pile
  up; five wrong codes lock a sender out (an IPv6 sender by its /64). Only the
  builder's own origins and host names get answers, which also stops DNS
  rebinding. A design whose field names differ only in letter case is refused,
  and a design that can do more than the device's current one — run a
  program, a command, a yt-dlp command line or a plugin action, start a file
  server, bind or change a protected setting, move where tools run, change the
  chat channel or load from a network share — waits in
  `jukaconfig.json.pending` until the owner applies it on the device, where
  the card says to apply only one's own design and lists every reason whole
  (Keep the current design is the default); nothing else lands while it
  waits. Pulled designs leave secrets on the device, pushing them back keeps
  them, and no answer to a push quotes one; an install that still holds the
  published code gets a new one at start ([player/livebuilder.go](player/livebuilder.go),
  [builder-device.js](builder-device.js), [SECURITY.md](SECURITY.md)).

- **The live builder's pairing code file is out of the repository.** An
  earlier change git-ignored `player/.livebuilder-code`, but the file stayed
  tracked, so every clone still shared one code; it is now removed from git
  (the removal is staged; the file each install generates stays ignored) and an
  install still holding the published code gets a new one
  ([player/.gitignore](player/.gitignore),
  [player/livebuilder.go](player/livebuilder.go)).

- **FTP now asks for a password and shares only your games, saves and
  media.** The FTP server used to accept any user name and password and serve
  the whole SD card. It now signs in as `juka` with a password made up for
  each start and shown on the Network tools page, waits two seconds after a
  wrong password, closes the connection after three failed sign-ins, locks an
  address out for ten minutes after ten, and drops a connection that has not
  signed in a minute after it connected; there is no anonymous login. The top
  level is a read-only list of the card's user folders (Roms, BIOS, Saves,
  screenshots, media), JukaHub's downloads and PhoneDrop (so phone uploads can
  be collected), each opened as an `os.Root` so `..` and symlinks cannot leave
  it; Apps, Emus, RetroArch and System are never shared, and `Roms/Ports`,
  whose PortMaster scripts the Ports panel runs, and the folders of game
  profiles stay read-only over FTP (change them over SFTP). A data connection
  is only accepted from the signed-in computer, and stopping the server also
  ends transfers in flight ([player/ftp_server.go](player/ftp_server.go)).

- **SFTP shares the SD card, not the whole system.** On the handheld the SFTP
  server's root is the card (your home folder on a computer); the whole file
  system is shared only after **Share whole system…** on its panel is pressed
  twice, for that run, or with `ssh_full_root` / `JUKAHUB_SSH_FULL_ROOT=1`. On
  the handheld the SFTP tile opens that panel first, so the choice can be made
  before the server starts ([player/ssh_server.go](player/ssh_server.go),
  [player/nettoolsscreen.go](player/nettoolsscreen.go)).

- **The file servers switch themselves off.** FTP and SFTP stop after 15
  minutes nobody uses them and when the handheld joins another Wi-Fi network or
  loses its address, with a toast that says why; keepalives (NOOP, PWD,
  TYPE…) do not count as use, and if the check itself fails the servers stop;
  the idle time runs on the device's uptime clock, so the clock being set from
  the network neither stops the servers early nor keeps them on longer
  ([player/fileserver_guard.go](player/fileserver_guard.go)).

- **Batch files start without cmd.exe reading their names as commands.**
  Apps, emulators and games launched through `.bat`/`.cmd` files get a command
  line quoted for cmd.exe (`/d /v:off /s /c`), so the launcher, the ROM path
  and per-game arguments with `&`, `|` or parentheses reach the right file — a
  ROM named `Tom&calc&.sfc` no longer runs a command — and a name cmd.exe
  cannot take literally (`%`, quotes) is refused with a message instead of
  being run ([player/winbatch.go](player/winbatch.go),
  [player/apps.go](player/apps.go), [player/emulators.go](player/emulators.go),
  [player/game_library.go](player/game_library.go)).

- **Package installs are checked, staged and swapped whole.** A registry entry
  may now publish `"sha256"` (and an exact `"size"`); a download that does not
  match is refused before anything is unpacked, and an entry whose checksum is
  malformed does not install at all. Entries without a hash still install but
  the package page says **Not verified** and the log records the download's
  own SHA-256. Downloads stream to a file beside the package instead of a
  64 MiB memory buffer, archives unpack through an `os.Root` into a staging
  folder (absolute paths, `..`, links, devices, duplicate names and reserved
  Windows names are refused), and only a complete copy replaces the installed
  one, by rename. Every step is on the card before the next one relies on it,
  so a power cut leaves the old version or the new one, never a copy of empty
  files; a swap that fails and cannot be undone is finished when the package
  is installed again, or when Packages opens after a restart;
  and a download is refused up front when the card (Windows included) lacks
  room for it plus 64 MiB ([player/pkgverify.go](player/pkgverify.go),
  [player/pkgstage.go](player/pkgstage.go),
  [player/packages.go](player/packages.go), [docs/PACKAGES.md](docs/PACKAGES.md)).

### Changed

- **The video player has new controls.** A frosted bar across the bottom of the
  picture holds a thick seek track with a handle and the time at both ends
  (time left counts down), a centred transport — rewind and fast-forward 10 s, a
  round accent play button, and previous / next when the video came from a
  list — the volume (a mute button, a level bar you can click, the percentage)
  and speed, captions, settings and full screen. Every button is a vector icon
  that lights up under the pointer and wears Home's focus ring under the D-pad;
  captions and full screen stay tinted while on. A chip and the title sit
  top-left, minimise and close top-right; a paused picture dims behind a large
  play button; a skip flashes "−10 s" / "+10 s" on its side of the picture; a
  live stream says LIVE instead of showing a track; the settings sheet lists
  each setting with its value. The chrome no longer takes the theme's text
  colour, so a light theme cannot draw dark icons over the video. The music
  player's transport (previous, play, next, stop, volume) draws the same icons
  instead of "|<", ">|" and "[]" ([player/videochrome.go](player/videochrome.go),
  [player/videoplayer.go](player/videoplayer.go), [player/icondefs.go](player/icondefs.go),
  [player/musicplayer.go](player/musicplayer.go)).

- **The Home pairing QR code is bigger.** The tile grew from 72 px to the full
  height between the status corner and the cards (about 100 px), and the code
  now fills it: the bitmap's own four-module margin used to be drawn inside the
  tile at one pixel per module, so the 72 px tile held a 37 px code. A pairing
  URL now draws with 3 px modules — 87 px of code — in one batched draw call
  ([player/companionui.go](player/companionui.go), [player/homelayout.go](player/homelayout.go)).

- **One log, with real levels.** The standard-library `[legacy]` log channel is
  gone: its 484 call sites now go through levelled helpers
  (`logDebug/logInfo/logWarn/logError`) into one slog handler that feeds the
  in-app Logs screen and the diagnostics bundle, formats a debug line only when
  debug is on, and colours the Logs screen by the line's real level instead of
  guessing from its words. `JUKAHUB_LOG_LEVEL` and `JUKAHUB_DEBUG=1` still work
  ([player/logging.go](player/logging.go), [player/systool_logs.go](player/systool_logs.go)).

- **Home puts games first and reads at arm's length** (the UX review's first
  fixes, see [docs/UX_REVIEW.md](docs/UX_REVIEW.md)). A **Games** card sits beside
  the hero and opens the game library; a new device shows a "Get started" card
  instead of an empty "Nothing played recently"; the version left the header;
  the greeting knows it is night after midnight; captions are at least 14 px
  (12 px on the Brick canvas); the focus ring stands off the card as a halo; the
  bell counts only what needs attention and caps at "9+"; the Exit pill left the
  footer (quit from the Start menu, or Select+Start); the PS4/PS5 pills read
  "Remote Play"; the Start menu draws real icons; the Exit screen asks "Quit
  JukaHub?" with Back focused, and its misleading Save button is gone. Untouched
  0.4.0 and early-November layouts migrate automatically
  ([player/homelayout.go](player/homelayout.go), [player/homefocus.go](player/homefocus.go),
  [player/home_migrate.go](player/home_migrate.go), [player/design.go](player/design.go),
  [player/startmenu.go](player/startmenu.go), [player/notifications.go](player/notifications.go),
  [player/game_library_ui.go](player/game_library_ui.go)).

- **Faster start on the handheld.** 42 regular expressions compile on first use
  instead of before `main`, a dead package registry no longer reads a directory
  at program start, five folder-setup steps moved off the path before the window,
  work deferred past the first frame runs in ~8 ms slices instead of freezing the
  first interactive second, and the minigame ticker no longer wakes 60 times a
  second when no game is open. `JUKAHUB_BENCH_INITTRACE=1 ./startup-bench.sh`
  shows package-init costs ([player/startup.go](player/startup.go),
  [player/startup-bench.sh](player/startup-bench.sh)).

- **A lean test suite.** 402 test files (about 3,000 tests, 113k lines) that
  rendered pixels, pinned layout numbers, audited the docs or slept were deleted;
  73 files and 469 fast tests remain and guard saved data, config migration,
  security checks, the updater and installers, and the parsers. No test opens an
  SDL window now, so `go test` finishes in seconds and `make test` uses Go's test
  cache ([CONTRIBUTING.md](CONTRIBUTING.md#tests-for-what-you-changed)).

- **About 5,300 lines of dead code removed.** A reference scan found ~380
  functions, types and variables nothing used — among them the old Waydroid
  Android host, an analog clock that was never drawn, a `TaskGroup` with no
  users, layout constants from the first Home, and "compile-time marker"
  variables — plus a test that lived in a production file (it never ran and
  linked the `testing` package into the release binary).

- **One engineering backlog.** `player/MODERNIZATION.md` is now the single open
  task list — about 100 tasks with priority, size, files, "done when" and how to verify,
  a hardware validation queue and an ideas parking lot — and the completed
  checklists and audit logs left it. `FEATURE-SLATE`, `FEATURE-BACKLOG`,
  `IMPROVEMENTS`, `PROJECT_STATUS` and `ROADMAP` were folded into it and deleted
  ([player/MODERNIZATION.md](player/MODERNIZATION.md)).

- **The builder forgets Font Awesome.** The migration that redrew icons in
  autosaves from the Font Awesome era is removed along with its test; icons are
  the builder's own SVGs ([script.js](script.js)).

- **A new logo, in the glass design.** The metallic circuit-board J did not
  belong to the glass app it opened. The logo is now Home made into an icon: the
  navy backdrop with its cyan and violet blooms, a pane of glass whose rim
  catches both lights, and a white J set like the Inter wordmark. It replaces
  the old artwork everywhere: the TrimUI Apps-menu icon (the SD-card package's
  copy had a grey checkerboard painted in where its transparency should have
  been), the Windows executable, window and taskbar icon, the web builder's
  favicon and app bar, the README and the docs site; and it now stands beside
  the wordmark on the About screen. One script renders every copy from one
  construction. The small Windows sizes (16-32 px, plus new 20 and 40 px entries
  for 125% display scaling) are a simpler mark drawn for their pixel grid
  rather than a shrunk 300 px picture, and the script writes the compiled icon
  resource itself, so changing the logo needs Python with numpy and Pillow and
  no MinGW. `mkwinres.sh` no longer remakes the `.ico` from `JukaPlayer.png`,
  which would throw that hinting away
  ([branding/mklogo.py](branding/mklogo.py), [branding/README.md](branding/README.md),
  [player/brandlogo.go](player/brandlogo.go), [player/about.go](player/about.go)).

- **The goodbye scene's leftovers are gone.** The farewell animation was already
  replaced by the glass teardown, but the exit request still lived in
  `goodbye.go` and the README and controls docs still promised a "goodbye
  screen". The request now sits with the signal guard in
  [player/exitguard.go](player/exitguard.go), that file is deleted, and every
  comment and doc describes the teardown card that actually runs.

- **Startup and shutdown are one glass card each.** The boot splash was a PS2
  starfield that looked like a different app, and its bar filled on the
  animation's own clock. It is Home's glass sheet now, and the bar is the boot
  queue: every row is a step that actually ran, with its own result — including a
  write probe on the card and the helper-tool inventory, whose missing tools
  start downloading during the splash instead of after the first frame. The card
  ends as soon as the queue is empty, so a fast boot gets a shorter splash. The
  farewell animation is gone with it: shutdown draws the teardown while it
  performs it — children and playback stopped, playtime, favorites, notes and the
  pending settings flushed, the CPU governor and frequency ceiling put back the
  way the firmware had them, and the shared cache and texture caches closed
  ([player/splash.go](player/splash.go), [player/stageui.go](player/stageui.go),
  [player/bootwork.go](player/bootwork.go), [player/bootchecks.go](player/bootchecks.go),
  [player/shutdown.go](player/shutdown.go), [player/powerprofile.go](player/powerprofile.go);
  tests in `player/bootstage_test.go`,
  `player/shutdown_test.go` and
  `player/powerprofile_boot_test.go`).

- **Button sounds are a soft thump.** Navigation, activate, back and toggle were
  flat beeps (880 Hz and up) with a linear envelope, which the handheld speaker
  rendered as a hard click. They are now short falling-pitch thumps with an
  exponential decay, so a tap reads as a body knock rather than a beep
  ([player/sound.go](player/sound.go)).

- **The legacy log is silent unless asked for.** Every colour pushed while a
  theme applies, and every parsed page, wrote a `[legacy] ... [DEBUG]` line to
  stderr, where it buried the boot log. Set `JUKAHUB_DEBUG=1` for a support
  session to get it back ([player/logging.go](player/logging.go)).

### Added

- **YouTube links in the browser play in Media.** Following a link to a YouTube
  video — a tap, A on a focused link, the link picker, a play card, a typed or
  pasted address, a bookmark, a shortener that redirects to a watch page, or a
  link another screen opens in the browser — now plays it on the Media screen
  instead of loading a page the reader cannot show. DuckDuckGo's and Google's
  click-tracking links and YouTube's EU consent page are seen through, a `t=`
  start time is kept, a watch link's playlist is dropped (yt-dlp would fetch the
  whole list into one file), and the result's text becomes the video's title.
  Back returns to the page the link was on, where it was scrolled, with the
  video in the corner window ([player/browser_youtube.go](player/browser_youtube.go)).

- **The power button is back on Home.** The Exit pill returns to the left end of
  the footer with a power glyph; Favorites drops down to it. It now asks first:
  an "Exit JukaHub?" card opens above it with Exit focused, so quitting is still
  two presses of A, but a stray press, a double click or a held key no longer
  quits by accident ([player/home_power.go](player/home_power.go)).

- **Hints name the keys you actually have.** On a PC without a game controller
  every hint (the footer, the Controls card, the on-screen keyboard, the hint
  lines in panels) now names keyboard keys: Enter instead of A, Esc instead of
  B, the arrow keys instead of the D-pad, F10 for the Start menu, PgUp/PgDn,
  Home/End and Tab where the shoulders page, jump or switch tabs. On a TrimUI the
  same happens when a USB or Bluetooth keyboard is plugged in (the device's own
  power and volume keys and key mappers do not count). With both connected, the
  one pressed last wins; a short message says why the words changed. Shoulders
  whose job differs on a screen keep their pad names rather than promise a key
  that does something else. Settings → Controls → Button hints overrides it
  (Auto / Controller / Keyboard) ([player/inputhints.go](player/inputhints.go),
  [docs/CONTROLS.md](docs/CONTROLS.md#keyboard-or-controller-what-the-hints-name)).

- **F10 opens the Start menu from the keyboard,** and closes it. Before, a
  keyboard could only reach the menu by walking to the top bar's pill. PgUp/PgDn
  and Home/End now page the Start menu, the notification centre and Quick
  Settings; Ctrl+Q closes Quick Settings as well as opening it; Tab switches the
  Downloads tabs; Y works as the "New" and "Graph" keys in Notes and the
  Calculator, as their footers say ([player/startmenu.go](player/startmenu.go),
  [player/core.go](player/core.go), [player/quicksettings.go](player/quicksettings.go)).

- **F1 shows the keys for the screen you are on.** It opens the Controls card,
  written in keyboard keys, and F1 again opens the full list, which was
  rewritten: every row of a category used to be drawn on the same line, its Home
  entries described keys that had changed, and what did not fit was cut off (it
  scrolls now). F1, F2 (search), F12 (screenshot) and Ctrl+Q now work on every
  screen, including those that take every key (Notes, Calculator, Pomodoro) or
  have nothing to focus, where they used to do nothing; the music player keeps
  F2 for its library search
  ([player/shortcuts.go](player/shortcuts.go), [player/controlslegend.go](player/controlslegend.go)).

- **Startup and shutdown chimes.** JukaHub now starts and closes with a sound,
  in the spirit of a console boot: as the glass splash comes up, a breath of air,
  a warm D♭ major-ninth pad and four glass bells climbing left to right, then a
  sparkle as the glint crosses the wordmark; closing plays the mirror, three bells
  falling back home. Both are synthesised (no audio files), streamed a block
  ahead of the speaker on their own stereo device so the first sound leaves within
  milliseconds, and follow the interface-sound level. **Settings → Sound &
  haptics → Startup & shutdown sound** turns them off (a toggle that previews the
  chime); a warm relaunch and a firmware power-off stay silent
  ([player/bootchime.go](player/bootchime.go),
  [branding/sound/README.md](branding/sound/README.md)).

- **Xbox on the Smart Pro, through hakuX.** The "xemu — not supported" tile is now
  an installer for the experimental hakuX port for the TG5040: X installs or
  updates it from its GitHub releases (digest-checked, SHA-256 recorded), unpacked
  on the card and merged into `Data/` and `Emus/` without touching files the
  package does not ship; R1 rolls back the last update, Y shows a setup sheet
  with the in-game controls, and Xbox games in `Roms/XBOX` launch from the Games
  library. The Brick Pro gets the same offer marked untested; Brick, Hammer and
  Smart Pro S say why they cannot; Windows keeps xemu
  ([player/hakux.go](player/hakux.go), [player/hakuxpage.go](player/hakuxpage.go),
  [docs/CONTROLS.md](docs/CONTROLS.md)).

- **AndroidRT grows a Java side.** NDK games now get a real JNI environment
  (classes, methods, strings, arrays, references, exceptions, `RegisterNatives`)
  where anything unimplemented returns a safe default and is reported once, so a
  game's first run lists exactly what it is missing. A Minecraft Bedrock class
  pack answers the platform questions the game asks — store, licence and Xbox
  Live honestly report "unavailable", never a fake success — GameActivity games
  (recent Minecraft builds) start through their own glue, and on TrimUI firmware
  the present layer uses the stock PowerVR `libEGL` first. Minecraft needs its
  64-bit build; Terraria, a Xamarin/Mono app, is refused with the reason and the
  PC route. Host smoke suite: 103 checks. Nothing has run on a device yet
  ([docs/ANDROID.md](docs/ANDROID.md), [docs/ANDROID-MINECRAFT.md](docs/ANDROID-MINECRAFT.md)).

- **A UX review.** [docs/UX_REVIEW.md](docs/UX_REVIEW.md) is an expert critique of
  every main screen for a 5-inch handheld at arm's length: principles, 30
  findings rated P0–P3, and which ones shipped.

- **Screens that need the internet say so before the press.** Launched before
  WiFi is joined, Live TV, Podcasts, Shorts, Tickers, video search and the
  internet diagnostics used to open and then fail somewhere inside with a raw
  dial error. Those tiles are now drawn dimmed with an **Offline** tag, the Start
  menu marks its network scenes the same way, and pressing one explains itself
  ("Offline — Live TV needs WiFi") instead of navigating. Local apps are
  untouched — JukaLand still plays, the Browser still shows cached and local
  pages, and FTP/SSH/companion/netplay stay live because they only need the LAN,
  not the internet. Separately, resolver and route failures are rewritten to
  "No internet connection — connect WiFi and try again" in the Browser error
  card, the JSON browser, the Downloads panel and the network tools, instead of
  "dial tcp: lookup … no such host"
  ([player/offlineaware.go](player/offlineaware.go),
  [player/menutiles.go](player/menutiles.go), [player/startmenu.go](player/startmenu.go),
  [player/core.go](player/core.go); tests in
  `player/offlineaware_test.go`).

- **A ringing alarm takes over the screen.** The alarm was a three-second
  arpeggio and a toast that any open panel or game could paint over, which is
  not a clock you can sleep through. A due alarm now owns the screen: a red wash
  blinking over everything, the time it is ringing for, its label and ringtone,
  and two buttons — **Snooze 9 min** and **Dismiss** — that are the only things
  the pad, the keyboard and a finger can press while it rings (A/X snooze, B/Esc
  dismiss, left/right choose, S and D shortcuts, taps on the buttons). The sound
  is a loop with a louder peak than any UI sound and keeps ringing until one of
  the buttons is pressed; snooze re-arms the same alarm without touching
  tomorrow's schedule, and dismiss clears the snooze. Each alarm also picks its
  own synthesized ringtone (Classic Chime, Brass Bell, Digital Pulse, Sunrise,
  Loud Buzz, Morning Birds) on a new **Ringtone** row in the editor, previewed as
  it cycles ([player/alarmring.go](player/alarmring.go),
  [player/alarm.go](player/alarm.go), [player/sound.go](player/sound.go);
  tests in `player/alarm_ring_test.go`).

- **Community theme library.** The theme gallery on Settings → Appearance has a
  **C** shortcut (**Y** on a gamepad) now: it opens a browser of the community
  themes published in the generator's gallery index. The index is fetched in the
  background, cached
  beside the other app data so the list still browses offline, and **A** installs
  an entry as `themes/<id>.json` — the same format a hand-made theme uses — then
  applies it. Bad entries are skipped instead of hiding the library, and an entry
  whose id matches a built-in palette is refused
  ([player/themecatalog.go](player/themecatalog.go),
  [player/themecommunity.go](player/themecommunity.go)).

- **The web gallery reads a shared community index.** `gallery/community.json`
  now carries community layouts as well as themes, and the builder merges its
  `layouts` into the bundled ones when the Gallery opens — so publishing a design
  is a pull request against one JSON file, not an edit to `layouts.js`
  ([gallery/README.md](gallery/README.md)).

- **Continue now includes podcasts.** Starting an episode puts it on the same
  timeline as videos and games, so the Continue card answers "what was I in the
  middle of?" across every media type; opening it returns to the Podcasts player
  with the saved position. Episodes de-duplicate by audio link and the row is
  labelled as a Podcast, not a video
  ([player/favorites.go](player/favorites.go), [player/recent.go](player/recent.go);
  tests in `player/recent_podcast_test.go`).

- **Saved playlists for music.** Audio Settings has a **Playlists** row: X names
  and saves the current queue ("Road Trip", "Relaxing"), left/right picks a
  saved list and A plays it. Names are case-insensitive, saving the same name
  replaces the list, and the store is plain JSON next to the other app data with
  atomic writes and a 40-playlist / 500-track bound
  ([player/musicplaylists.go](player/musicplaylists.go); tests in
  `player/musicplaylists_test.go`).

- **In-game OSD presets.** Settings → Smart Widgets has an **In-game OSD** row
that cycles *leave as is* (the default), *quiet*, *notifications* and
*notifications + stats*, writing RetroArch's own display switches — the message
font, menu widgets, FPS/framecount/memory counters — before a RetroArch launch,
with the original config kept once as `retroarch.cfg.jukahub-backup`. It never
touches a hotkey: those are numeric button codes that differ per firmware, so a
guess would break controls ([player/osdpreset.go](player/osdpreset.go); tests in
`player/osdpreset_test.go`).

- **Box art finds more of your library.** A cover lookup now also tries the same
title spelled the other way: accents folded (`Pokémon` → `Pokemon`), Roman
numerals as numbers and back (`Zelda II` ↔ `Zelda 2`), `&` for `and`, a colon
where the folder used a dash, and no trailing period — plus a small alias table
for the names that differ by more than spelling, starting with `Zelda II` →
`Zelda II - The Adventure of Link`. The exact name is still asked for first, and
the guesses come after every structural one
([player/romsetup.go](player/romsetup.go); tests in
`player/artwork_aliases_test.go`).

- **A 12-hour clock, if you want one.** Settings → Appearance has a **12-hour
  clock** switch: the status cluster, the dock clock, the saver, the clock app
  and the alarm list then read `3:04 PM` (and `12:05 AM` at midnight) instead of
  `15:04`, and every clock keeps working as the button it already was — the
  header still finds the clock and opens the calendar. Unset, everything stays
  24-hour, so existing configs and screenshots do not change
  ([player/clockformat.go](player/clockformat.go),
  [player/config.go](player/config.go); tests in
  `player/clockformat_test.go`).

- **The web builder ships its own fonts.** Inter and the Font Awesome faces the
  markup uses now live in `fonts/` instead of a CDN, so the builder opens on
  a handheld's own hotspot and never waits on a third-party request; the shipped
  policy no longer has to allow those hosts (`index.html`, `_headers`;
  `tests/web/selfhost.test.mjs`).

- **The design check lints for the handheld panel.** Saving a design that will
  run at 1280×720 on a five-inch screen now warns about text under 14 px, a
  control inside the rounded-corner margin the panel clips, and a scene with more
  than 24 controls that will scroll — alongside the existing contrast and
  tap-target checks (`script.js`; `tests/web/builder.test.mjs`).

- **The idle screen can take its own backdrop photo away.** The starfield sleep
  screen is the only place the photo is drawn, so it is also where it can be
  forgotten: **Y** (pad or keyboard) offers **Y clear photo** above the wake
  hint, and a second **Y** inside five seconds drops the backdrop without
  waking the panel or opening Files. The first press only arms it — every other
  input on this screen means "wake up", and an ordinary wake must not throw the
  photo away — so the line turns amber as **Y again to clear**, then green as
  **Backdrop cleared · any key wakes** for a moment. The picture on the card is
  untouched; only the setting is forgotten, and a default saver with no photo
  shows no line and wakes on **Y** like any other key
  ([player/screensaver.go](player/screensaver.go),
  [player/main.go](player/main.go); tests in
  `player/screensaver_backdrop_test.go`).

- **The Benchmark screen's Run button says it is running.** While a measurement
  is in flight — and the screen starts one as it opens, so on a slow card that
  is the whole first visit — the screen's one Run button is drawn disabled and
  reads **Running…** instead of **Run**, so it no longer looks like a press
  would do something. A press that lands mid-run is refused at the control: no
  activate tap, no press dip, and no second 100 MB pass over the first, which
  the panel's progress line already answers. The button takes its configured
  width back as soon as the run ends, and the busy label is fitted to that width
  rather than ellipsized
  ([player/core.go](player/core.go),
  [player/morefeatures.go](player/morefeatures.go); test in
  `player/benchmark_test.go`).

- **Picture and video thumbnails in Files.** A row whose file is a photo or a
  video now shows the picture itself in the leading chip instead of **IMG** or
  **VID**: 44×28, cropped from the centre to fill rather than squashed, with a
  play mark on a video frame so a still and a photo are told apart. The chip's
  footprint is unchanged, so names, sizes and dates stay in their columns.
  Frames are made off the render thread — a photo is decoded and averaged down
  by the Go image decoders, and one ffmpeg grab pulls a video's frame a second
  in, falling back to the opening frame for a clip shorter than that — and both
  the bytes and the decoded texture are cached, so a folder is decoded once and
  again only when the file changes. A row keeps its type chip when there is nothing to show: no ffmpeg
  installed, a codec the tool cannot read, or a picture past the
  24-megapixel decode ceiling
  ([player/filethumb.go](player/filethumb.go),
  [player/files_manager.go](player/files_manager.go); tests in
  `player/filethumb_test.go`).

- **A playback queue: results can be lined up, and each one starts when the last
  finishes.** The **Queue** pill on a search-result row adds that video to Up
  Next and reads *Queued* while it is waiting; pressing it again takes it back
  out. The list survives leaving the results screen, the player overlay names
  what comes next (`Up next: <title> (+3)`), and a video that reaches its
  natural end starts the head of the list by itself — stopping one by hand
  never does. The player's settings sheet gained an **Up Next: N queued** row
  that empties the list once the rows it was filled from are gone, and the
  Shorts feed keeps its own hand-off while it is armed. Entries are
  deduplicated by link and capped at 50, and the list is session-only on
  purpose ([player/playbackqueue.go](player/playbackqueue.go),
  [player/videoplayer.go](player/videoplayer.go),
  [player/result_actions.go](player/result_actions.go); tests in
  `player/playbackqueue_test.go`).

- **Vibration strength: Soft, Normal or Strong.** A new row in the Accessibility
  panel scales the drive level of every pulse — navigation ticks, confirmations,
  errors — so the motor can be quiet at night without switching feedback off
  entirely, which the Vibration switch in Settings still does. The pulse length
  is left alone: a coin motor needs tens of milliseconds to spin up, so a
  shorter pulse would read as unreliable rather than gentle. The step is stored
  as `hapticsIntensity` beside the switch and a Strong error pulse is capped at
  full scale ([player/haptics.go](player/haptics.go),
  [player/a11y.go](player/a11y.go); tests in
  `player/haptics_test.go`).

- **Box art for ROM names that do not match the reference title.** The lookup
  now asks for the exact name first, then the underscored twin, the name without
  dump tags (`(USA)`, `(Rev 1)`, `[!]`), the part before the subtitle separator,
  and finally the article-stripped form — each of them spaced and underscored,
  capped at eight requests, and a hit stops the guesses so a full library fetch
  is not multiplied by the variant count. The not-found cache is stamped with
  the matcher version, so titles the older lookup gave up on are retried once
  instead of being hidden for the month-long window
  ([player/romsetup.go](player/romsetup.go); tests in
  `player/artwork_match_test.go`).

- **Files is a file manager, and it moves files between two consoles.** The
  screen opens on a breadcrumb bar where every folder in the path is its own
  chip — one press to any ancestor instead of five **B** presses — carrying the
  folder's item count, its size, the card's free space and a sort chip that
  cycles name / size / newest. Rows gained a type chip (**DIR**, **IMG**,
  **AUD**, **VID**, **PDF**, **ZIP**, **BOOK**, **TXT**, **UP**), the size and the
  date, and the parent row names the folder it goes back to. **X** marks rows
  (a marked row is tinted and its chip becomes a tick), and **Y** opens one
  action sheet: new folder, rename, copy, cut, paste, delete, select all, sort,
  connect and remote files. Actions apply to the marked rows or, with nothing
  marked, to the row under the cursor; a paste never overwrites, landing as
  `name (2).ext`; delete arms on the first press and acts on the second, and
  every path is still resolved against the Files root before it is touched.
  **Connect to device** takes `user@host:port/folder`, and **Remote files**
  browses the other console over SFTP — download into the open folder, upload
  the marked files to the remote one — as one background job per transfer with
  its progress in the panel's subtitle. The first connection to a device pins
  its host key and a later change is refused
  ([player/files_manager.go](player/files_manager.go),
  [player/filetransfer.go](player/filetransfer.go),
  [player/features.go](player/features.go), [player/main.go](player/main.go);
  tests in [player/files_manager_test.go](player/files_manager_test.go)).

- **The lock-screen image can be cleared again, and clearing it never touches
  the photo.** The picture a Files viewer handed to the idle screen stays
  reachable: opened in the viewer, the same action now reads **Clear lock
  screen** and puts the starfield idle screen back — the button, the **L** key
  and the pad's **X** are one toggle, so the second press undoes the first. Only
  the `screenlockImage` setting is forgotten, so the file stays on the card and
  can be set again from the same place; the decoded picture is dropped in the
  same call, so the default look returns on the next frame instead of after the
  reload window, and clearing a screen that already has no picture is a quiet
  no-op that writes nothing
  ([player/screenlock.go](player/screenlock.go),
  [player/imageviewer.go](player/imageviewer.go),
  [player/controller.go](player/controller.go); tests in
  `player/screenlock_test.go` pin the restored default, the untouched file, the
  saved state after a reload and the toggle's remote-image guard).

- **The Wi-Fi diagnostics sheet can be copied, and it is in the support
  bundle.** A **Copy** pill (also `c` and pad **Y**) puts the sheet's report on
  the clipboard — header, collection time and every row, aligned — and writes a
  diagnostics bundle on the card in the same press, so the radio's own answer
  reaches a support thread however it gets shared. The two destinations are
  attempted independently: a platform without a clipboard still gets the file,
  and a full card still gets the paste. The sheet's last collection also joins
  every bundle, redacted like the log, and a session that never opened the sheet
  says so instead of reading like a device with no Wi-Fi facts
  ([player/wifi_diag.go](player/wifi_diag.go),
  [player/diagnostics_bundle.go](player/diagnostics_bundle.go),
  [player/wifi_ui.go](player/wifi_ui.go); tests in
  `player/wifi_diag_test.go`,
  `player/diagnostics_bundle_test.go`).

- **Podcasts are a subscription library, not a URL box.** The Podcasts screen
  now keeps a saved list of feeds in `podcasts.json` next to the app's other
  data, gives each feed its show title, author and episodes, and opens on a
  two-column library: shows on the left (with an **+ Add a feed** row) and that
  show's episodes on the right. Episodes keep their published date and feed
  duration, sort newest first, carry the resume position and speed the audio
  player already stores for long-form listening, and play as a one-show
  playlist through the music player's streaming path — the same path the URL
  box used, but with a title and a next episode instead of a bare URL. Feeds are
  fetched in parallel, four curated shows seed a fresh install, and subscribing
  or removing a feed is written immediately, so an offline boot opens the saved
  library instead of an empty panel. The `podcast_panel` action and the screen's
  own Library button open it; the pad drives the two columns (Left/Right
  switches, A opens a show or plays an episode, X adds a feed, Y refreshes,
  Start deletes a subscription, B closes), and the keyboard and pointer use the
  same row map
  ([player/podcasts.go](player/podcasts.go), [player/core.go](player/core.go),
  [player/main.go](player/main.go), [player/controller.go](player/controller.go);
  tests in [player/podcasts_test.go](player/podcasts_test.go)).

- **The reader opens EPUB books, and PDF pages are page-accurate.** The old PDF
  extractor scanned every `BT…ET` block in the file and handed the whole
  document back for each page, so page one showed the entire book and page two
  showed it again. The reader now walks the PDF page tree from the catalog,
  decompresses each page's content streams, and extracts only that page's text
  (with `TJ` kerning above the word-space threshold becoming spaces and `Td`,
  `T*` and `Tm` becoming line breaks); a file whose page tree cannot be read
  still falls back to the old whole-document scan rather than showing an empty
  sheet. EPUB support is new: container → OPF → spine, one chapter per spine
  document with its `<title>` or first heading, paginated to about 1,200
  characters a page, entities decoded and paragraphs kept apart. The scene keeps
  its `PDFReader` name so favourites, playtime and saved configs still resolve,
  but it is **Reader** everywhere in the UI: the title line shows the format and
  page count, a book adds a chapter line, L1/R1 turn chapters (or pages for a
  PDF), `[`/`]` do the same on a keyboard, and Files accepts `.epub` as well as
  `.pdf`. Entering the scene no longer wipes the document that Files just
  opened, which was why an opened PDF used to show "No document open"
  ([player/reader.go](player/reader.go), [player/pdfreader.go](player/pdfreader.go),
  [player/features.go](player/features.go), [player/extras.go](player/extras.go),
  [player/core.go](player/core.go); tests in
  [player/reader_test.go](player/reader_test.go)).

- **An LED Style panel for mode, effect, colour and brightness.** The LED sync
  could already drive `multi_intensity`, `color` and decorative brightness LEDs,
  but only the policy was reachable from the UI and the rest meant editing
  `jukauser.json`. **Quick Settings → LED Style** now opens a panel with Mode
  (Audio, Battery, Charging, Temperature, Custom), Effect (Solid, Pulse,
  Rainbow), a hue strip with a live swatch and hex value, a 10–100% brightness
  trim, and the LED Sync switch itself. Every change is written to the
  debounced user config, so it survives a reboot, and the animation runs while
  the panel is open so the lights show the change on the next frame; closing the
  panel stops a preview it started, leaving the saved setting as it was.
  Brightness is applied to the colour (so it dims every LED mode the same way)
  and `rainbow` is a new custom effect. The panel is modal for the pad, the
  keyboard and the pointer, and its row is dimmed with a reason on a device with
  no addressable LED
  ([player/ledpanel.go](player/ledpanel.go), [player/ledsync.go](player/ledsync.go),
  [player/quicksettings.go](player/quicksettings.go), [player/main.go](player/main.go),
  [player/controller.go](player/controller.go); tests in
  `player/ledpanel_test.go`).

- **Favorites is a library, not a flat list of links.** The page now opens on
  **Continue** and has four tabs with their counts on the strip — Continue
  (videos started but not finished), Videos (everything saved from a result,
  grouped by channel), Streams (the channels kept on the IP Stream screen) and
  Recent (grouped into Today / Yesterday / This week / Earlier). Every row shows
  a 16:9 frame or a drawn tile, the title, the channel or stream host, a
  duration badge and a resume line, with a hairline along the card's foot for
  how far in the video is; a row without artwork still reads as a video because
  the frame gets a border and a play glyph. The list scrolls only as far as the
  focused row needs and a group heading is never scrolled over its row. Empty
  tabs say what they hold and how to fill them. Saved videos inherit the resume
  offset recorded in Recent, so Continue and Videos agree on where playback
  starts. The pad drives the whole page (Left/Right tabs, Up/Down rows with the
  Play / Remove buttons reachable at the ends, A plays, X removes, B backs out,
  L1/R1 and L2/R2 jump), the keyboard and the pointer use the same row map the
  renderer records, and the scene's controller block now delegates to one
  handler instead of its own copy of the old two-tab logic. Results gained a
  fourth pill: **Save** keeps a video in the library and reads **Saved** when it
  is already there, then takes it back out on the next press
  ([player/favorites.go](player/favorites.go), [player/result_actions.go](player/result_actions.go),
  [player/core.go](player/core.go), [player/main.go](player/main.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  `player/favorites_library_test.go`,
  `player/result_actions_test.go`).

- **Files can keep the picture it opens as the lock-screen image.** The
  full-screen viewer in Files now has a **Set lock screen** action — the button,
  the **L** key or the pad's **X** — which saves the picture and paints it across
  the idle screen under the clock, quote and chat card, cropped to fill and
  dimmed so that text stays legible. The saved setting is a path, so it is
  treated as best-effort: a photo that was deleted, left on an unplugged card or
  no longer decodable simply leaves the default starfield idle screen in place.
  The miss is checked (and logged) once, not once per frame, and a replaced file
  is reloaded instead of the saver keeping a stale picture; a remote chat image
  cannot set it, because that file lives in the download cache
  ([player/screenlock.go](player/screenlock.go),
  [player/imageviewer.go](player/imageviewer.go),
  [player/screensaver.go](player/screensaver.go); tests in
  `player/screenlock_test.go` pin the validation, storage, the drawn frame and
  the missing-file fallback).

- **The Streams button opens a menu that can always start content.** The IP
  stream screen was a single URL box, and the Media screen's button (labelled
  "IPStream") plus the screen's own "Library" button opened a list of saved
  links that is empty on a fresh install — a dead end on a handheld, where the
  button is the only way in. Both now open one menu with two rows that are
  always there — **Add a stream link** and **Browse channels** (the public IPTV
  playlist import) — above the saved channels, so there is never a press with
  nothing behind it. The D-pad walks every row including the action rows, A or
  Enter opens or plays the highlighted one, X saves the URL box as a channel, Y
  imports, Start or Delete removes, and B or Esc closes. The two buttons are
  named the way the screens name themselves (**Streams** on Media, **Channels**
  on the stream screen), the hint row now gives each pad button its keyboard
  twin (`A / Enter`, `B / Esc`) the way the rest of the overlays do, and the
  injected Media button is a real focus target: a test walks the D-pad from
  Shorts to it on the compact Brick canvas and checks that confirming it opens
  the menu ([player/ipstreams.go](player/ipstreams.go); tests in
  `player/ipstreams_test.go`).

- **Earned badges show their unlock date.** The unlock time was already saved
  with each badge (RFC3339, from the moment the check passed) and never shown,
  so the list could say what you earned but not when. Each earned badge now
  carries "Unlocked 2 Mar 2026" on a third line under its description, in the
  accent colour; locked badges leave the line empty, and a save whose timestamp
  cannot be read shows nothing rather than a zero date. Badge rows are 74px so
  the third line cannot be clipped by the row below ([player/playtime.go](player/playtime.go);
  `TestPlaytimeUnlockLabel` and `TestEarnedBadgesShowTheirUnlockDate` in
  `player/playtime_achievements_test.go`).

- **Playtime has 100 badges on a page that scrolls, earned badges first.**
  The screen shipped six badges in a grid that stopped drawing once the card was
  full, so a badge set of any real size would have been invisible. It now has
  exactly 100 — lifetime time, days used, streaks, single-day records, weekly
  totals, screens explored, 57 per-screen milestones (games, music, video,
  browser, files, settings, notes, terminal, emulators and the rest) and ten
  that count the badges you already earned — and the whole page scrolls under a
  pinned title band with a scrollbar, driven by the D-pad, the wheel, Page
  Up/Down or L1/R1, with Home/End jumping to either end. Earned badges are
  listed **first** under an "Achieved first" tag and keep their fixed number
  wherever they sit, so nothing jumps around as you unlock it. Every check reads
  the saved totals, so a screen you have not used cannot unlock anything, and
  the badge count in the read-out is pinned to the list by a test. Two layout
  defects the bigger page exposed are fixed with it: the MOST USED value used to
  be drawn on top of its own bar, and on the compact (Brick) canvas the insight
  stats ran off the card edge. ([player/playtime.go](player/playtime.go),
  [player/main.go](player/main.go), [docs/CONTROLS.md](docs/CONTROLS.md); tests
  in `player/playtime_achievements_test.go`).

- **About now shows the device identification evidence, not just the log.**
  `[device] …` was the only place the model, the source that named it, the panel
  and the firmware's own model strings were visible, and a handheld has no
  shell to read it. About has a **Device** card with those rows: the model and
  the source that decided it (`launcher env`, `device tree`, `cpuinfo`,
  `hostname`, `firmware name`, `trimui firmware + panel`, `override` or an
  honest `not identified`), the panel the image reports, the model names found
  in the firmware (longest match only, so a Pro S does not read as two models),
  and the kernel's device-tree model and compatible list. Empty sources are
  omitted rather than printed as blank labels. The four About cards now share
  two measured rows, so the new card cannot push the licence list into the
  footer band on either the wide or the compact canvas, and a card too short
  for the whole list says `+N more` instead of clipping it. ([player/about.go](player/about.go);
  tests in `player/about_device_test.go`).

- **A Wi-Fi diagnostics sheet, for a handheld that cannot connect and has no
  shell.** The picker could only say "no networks", which is the same line for
  a missing radio, a supplicant that is not running, a control socket in a
  directory nothing looked in, and a permission error — and the device that
  hits it has nowhere to type `wpa_cli status`. **L1** (or `i`, or the new
  **Diagnostics** pill) opens a sheet that names every deciding fact: the
  interface and which probe found it, the backend (`wpa_cli` or the supplicant
  socket), the control directory and whether its socket is actually there, the
  directories that were searched, the supplicant config, whether JukaHub started
  a supplicant and what happened, the radio's state and its switch, the station's
  `wpa_state`/SSID/IP, and the last error with the phase it came from and its
  age. It opens even when the list cannot load — that is the state it exists for
  — refreshes itself while open, and **B** steps back to the list instead of
  closing the picker. The last error is recorded at the single function every
  command failure already passes through, so the sheet and the panel cannot
  disagree. ([player/wifi_diag.go](player/wifi_diag.go),
  [player/wifi_ui.go](player/wifi_ui.go), [player/wifi.go](player/wifi.go),
  [docs/WIFI.md](docs/WIFI.md); tests in
  `player/wifi_diag_test.go`).

- **Playtime now answers "have I been consistent?" instead of "what did I use?"**
  The second card on the Playtime screen was seven bars for the past week and
  nothing else, even though the tracker already keeps 90 days of daily totals.
  It now draws a Monday-aligned 13-week heatmap — one cell per day, four accent
  levels by how long that day ran, unfilled slots for the rest of the current
  week — with the current streak, the longest streak in the window, the best day,
  the daily average over active days and the active-day count beside it. The
  current streak survives an empty today (the day is still in progress) and a
  gap ends it; the longest streak comes from the same single pass over the
  window, and days after today are never counted. This is the "Playtime Wrapped"
  entry from the modernization plan, minus a separate PNG export: the phone
  companion already serves this screen through its screenshot endpoint, so the
  card is what the phone sees. The card is also the first Playtime surface
  checked on real pixels — a fully played window must light it, an empty one
  must not, and neither may spill into the gap under the card — and the
  "start exploring" empty state in the neighbouring card is now ellipsized
  instead of running under it. ([player/wrapped.go](player/wrapped.go),
  [player/playtime.go](player/playtime.go); tests in
  `player/wrapped_test.go`,
  `player/wrapped_render_test.go`).

- **The Wi-Fi picker now works on Windows.** The backend was written against the
  handheld's `wpa_supplicant` — `wpa_cli`, `/sys/class/net`,
  `/proc/net/wireless` — so on a desktop every probe answered "No wireless
  network interface on this device" and nothing in the panel could connect.
  Windows drives the same operations through `netsh`, and
  [player/wifi_windows.go](player/wifi_windows.go) is that backend behind the
  unchanged panel: the adapter, the scan, the saved profiles, the signal meter
  and join, disconnect, reconnect and forget. A join stages a WLAN profile XML
  in a private temp file, adds it with `netsh wlan add profile`, connects by
  name and deletes the file, so the key is never an argument and never logged;
  the meter maps Windows' quality percentage onto dBm and uses the exact `Rssi`
  Windows 11 prints when it is there. `netsh` is localized, so association and
  signal strength are read from structure — a BSSID line, a percentage — with an
  English label used only for the adapter's name. A radio Windows has not
  brought up yet (a driver still loading at boot, or a dongle plugged in after
  the app started) is no longer remembered as "no interface": that answer is
  re-checked within seconds, and opening the picker asks Windows again.
  ([player/wifi.go](player/wifi.go), [docs/WIFI.md](docs/WIFI.md); tests in
  `player/wifi_windows_test.go`).

- **A persistent activity strip now shows background work on every scene.** While
  a tool or core install, the app's own update, an offline audio/video download
  or a package install runs, a small stack of chips is pinned to the right edge
  under the status bar — each chip names the operation and carries its own mini
  progress rail (an indeterminate shimmer when the source has no byte size), and
  the chips appear on any screen, so a download started in the video player or
  an install started in Emulators stays visible over the Home screen, the
  Terminal or wherever the user navigates. Active download rows show
  individually and queued rows fold into one "N waiting in queue" line; four
  chips show at a time and the rest collapse into an overflow line, and when
  nothing runs in the background the strip draws nothing at all.
  Package installs switched to the shared installer card (the same channel
  tools, core updates and the app's own update use) instead of an opaque
  scene-level spinner, so an install now names its package, walks through the
  same Resolving → Downloading → Installing phases and reports success or
  failure once — and the strip reads the same state, so there is only one
  source of truth to aggregate.
  ([player/activity.go](player/activity.go), [player/main.go](player/main.go),
  [player/packages.go](player/packages.go); tests in
  `player/activity_test.go`).

- **Frame cost is now measured on the device instead of inferred from a desktop.**
  The main loop times each iteration from the top of the loop to just after
  `Present` — input, scene logic, drawing and the flip, not the idle delay — and
  keeps a histogram in buckets that follow the hardware's budgets (≤8 ms is half
  a 60 Hz frame, ≤17 ms a whole one, ≤33 ms 30 fps, ≤50 ms a hitch, ≤100 ms a
  stall, then overflow). One line a minute goes to the log, and therefore into
  the redacted log tail of a diagnostics bundle:
  `[frame] last 1m: 3595 frames (59.9 fps), avg 7.8ms, p50 ≤8ms, p99 ≤8ms, max 42ms, over 17ms 0.1%`.
  `Diagnostics` gains a `Frame time` line with the session average and percentiles
  plus a `Frame share` table, so a stutter report carries the device's own
  numbers. The line describes the window that just ended (a slow boot does not
  colour the rest of the session); `JUKAHUB_FRAMESTATS=1` shortens the window to
  five seconds for a focused measurement. Frames are not recorded while a game
  owns the screen — those iterations present nothing. Percentiles are reported
  as bucket edges (`p50 ≤8ms`), which is honest about the resolution of a
  histogram and still tells which side of a vsync a frame landed on.
  ([player/framestats.go](player/framestats.go),
  [player/main.go](player/main.go), [player/patch_engine.go](player/patch_engine.go),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)).

- **The Quality row now says what the stream actually got.** It used to name only
  the ceiling — `Auto (720p)`, or a pin for this video — which is a request, not
  a fact: YouTube hands over a 480p rendition whenever no 720p one exists, and
  the row then described a picture that was never on screen. The row now carries
  the resolution the player is really decoding, read from the probe the decoder
  already does (`embeddedPlayer.sourceHeight`), so a capped stream reads
  `Auto (720p) · got 480p`. The second half is omitted when it matches the
  ceiling (repeating the same number is noise) and when nothing is playing, so
  the row never claims a stream that is not there; a cancelled quality switch
  still reverts the ceiling as before. The settings sheet is wider to hold the
  longest row (`Quality: 1080p (this video) · got 720p`), and the toast that
  announces a switch still names only the new ceiling, because at that instant
  the delivered figure is still the previous stream's.
  ([player/videoplayer.go](player/videoplayer.go),
  `player/videoquality_delivered_test.go`).

- **A long chat and browser session is now measured, not assumed.** The texture
  budgets were sized from reasoning about a 1 GB device; the new long-session
  probe spends a session in the two panels that fill them — 300 messages with
  14 pictures and 24 senders, then 24 illustrated articles read page by page —
  through the real decode-and-cache paths, with every ceiling pinned to the
  handheld branch, and prints the device's own `[tex]` line beside each cap. On
  a desktop at the card's ceilings the session holds 24,576 KB of chat pictures
  (full: it is a sliding window), 256 KB of avatars, 140 KB of cached pages,
  57,600 KB of page images of 64 MB, 8,240 KB of text of 24 MB, no home labels
  (chat and reader draw through `renderText`), 11,684 KB of shapes of 12 MB, and
  88 MB in total. The figures are in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)
  with the read of them: page images are the largest item and the shape cache
  the tightest fit. The probe fails if the session caches nothing, if the
  picture set never crosses its budget, or if any cache ends above its ceiling;
  it also caught a fixture defect worth knowing about — a browser element with
  no source renders the plain-text reader and silently draws no images at all.
  (`player/session_memory_test.go`,
  [player/thumbstore.go](player/thumbstore.go),
  [player/cachebudget.go](player/cachebudget.go),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)).

- **The scene-navigation memory harness now measures the shipping ceilings for
  every cache it prints, not just box art.** Rendered text, home labels and
  shapes each pick a handheld or a desktop budget, and the harness left three of
  them to the host: a desktop run reported 64/48/48 MB for those columns while
  the card ships 24/8/12 MB, so the table looked like the device's numbers with
  most of it describing the machine it ran on. One seam (`sceneBudgetForce`)
  now pins all three, the live singletons are retuned to match (`TextCache.
  SetBudget`, `homeTextCacheApplyBudget`, and a shape budget read at eviction
  time rather than captured at construction), and the harness fails if any pin
  did not take. The shape cache's branch also stops being "Windows or not",
  which gave a Linux desktop the handheld ceiling and CI a different number from
  the machine the work was done on. `TestSceneBudgetBranches` pins both sides
  and the derivation, the way the box-art budgets already were.
  ([player/cachebudget.go](player/cachebudget.go),
  [player/textcache.go](player/textcache.go),
  [player/shapeCache.go](player/shapeCache.go),
  [player/homelayout.go](player/homelayout.go); tests in
  `player/memory_growth_test.go`,
  `player/cachebudget_test.go`).

- **An emulator update can be rolled back from the Emulators page.** Every
  update already kept the binary it replaced as `<core>_libretro.so.jukahub-
  <stamp>.bak`, but there was no way to put it back without pulling the card and
  a file manager, which is exactly what the backup was for when a nightly build
  runs worse. **R1** on a tile whose core has a kept build restores the newest
  one; **R** does the same from a keyboard. The press is itself reversible: the
  build it replaces is kept as its own backup, so a second R1 goes forward
  again, and the restored file is deleted only after the new core has landed, so
  a failed rollback leaves the card untouched. The footer names the date of the
  kept build (`GBA · Available · backup 2026-10-20`) and the R1 hint appears
  only when one exists, so the key never offers a restore that would do nothing.
  Only this build's own `<core>_libretro.<ext>.jukahub-<stamp>.bak` names parse
  — `.part` downloads, `.jukahub.tmp` staging files and other tools' `.bak`
  files are ignored — and the target is re-checked against RetroArch's cores
  folders at the point that writes, because the path comes from a scan. The
  action is a local file copy, so unlike an install it is offered on the Windows
  build too.
  ([player/emulatorinstall.go](player/emulatorinstall.go),
  [player/emulators.go](player/emulators.go),
  [player/emutiles.go](player/emutiles.go),
  [docs/CONTROLS.md](docs/CONTROLS.md),
  [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md); tests in
  [player/emulatorinstall_test.go](player/emulatorinstall_test.go)).

- **Every texture and byte cache is now bounded in bytes, not just entries.**
  An entry cap is a byte cap only when every entry is the same size, and the
  launcher's icon cache (64 icons of up to 256×256), the chat image cache (16
  decoded photos, 48 MB each as RGBA), the avatar cache, the downloaded
  thumbnail payloads (512 entries of whatever the URL served) and the feed
  bodies kept for conditional requests (64 × 1 MiB) were all bounded by count
  alone. Each now carries a byte budget beside its count, checked in the same
  insertion-order eviction, and a replaced or evicted texture is destroyed
  exactly once. The three texture caches are reported by
  `JUKAHUB_TEXSTATS`, and a critical-memory sweep clears all five together with
  the caches it already cleared, so nothing grows straight back to its ceiling
  after RAM is freed. The budgets are 8/32 MB for launcher icons, 24/64 MB for
  chat images, 4 MB for avatars, 16/48 MB for thumbnail payloads and 8/16 MB
  for feed bodies (handheld/desktop).
  ([player/cachebudget.go](player/cachebudget.go),
  [player/appicons.go](player/appicons.go), [player/chat.go](player/chat.go),
  [player/thumbstore.go](player/thumbstore.go),
  [player/httpcache.go](player/httpcache.go),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md); tests in
  `player/cachebudget_test.go`).

- **The Emulators page installs emulators, not just lists them.** A `Roms/`
  folder with no emulator was a dead end: the tile read "no emulator found" and
  the only fix was copying a folder onto the card by hand. The buildbot channel
  the update checker already reads publishes one aarch64 archive per libretro
  core for this device, so a missing emulator is now an offer: **A** on a
  missing or unusable tile installs the core that runs its system, **X**
  does the same for any tile while also checking an installed core for a newer
  build, and **L1** brings every installed core forward in one pass. A download
  is checked against the CRC32 the index states before anything is written, the
  build lands in the core pool with the URL it came from, and the binary it
  replaces stays beside it as `<core>_libretro.so.jukahub-<date>.bak` — an
  update is undone by renaming one file. Nothing is written outside RetroArch's
  cores folder: `Emus/<system>/` launcher scripts belong to the firmware, and
  the standalone emulators upstream publishes are desktop builds that cannot
  start there. Once a core is on the card, the `Roms/` folder it covers is no
  longer reported as unplayable — the core's own tile is the row from then on —
  and the page's A/X/L1 hints follow what the highlighted tile can really do, on
  both the Smart Pro and the Brick. The Windows build does not offer the two
  core actions: a Linux shared object is not a file a desktop RetroArch loads.
  ([player/emulatorinstall.go](player/emulatorinstall.go),
  [player/emulators.go](player/emulators.go),
  [player/emutiles.go](player/emutiles.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  [player/emulatorinstall_test.go](player/emulatorinstall_test.go)).

- **The browser can fill in and send a form.** The reader dropped every
  `<input>` as a void tag, so a search page arrived with no box to type into and
  the one thing the browser is most often opened for was impossible. A form is
  now content: a text field draws as a filled chip showing its value, its
  placeholder or **Tap to type**, and A (or a tap) opens the keyboard on it; a
  `<textarea>`
  does the same with the text between its tags; a `<select>` steps through its
  options in place, wrapping, since a handheld has no dropdown and needs none; a
  checkbox or radio draws its box and toggles in place, a radio clearing the rest
  of its group; a submit button sends the form. Every control is a cursor stop
  among the page's links, so the D-pad and the link picker reach it. A submission
  posts nothing — the reader cannot POST — so the form's fields go out as a GET
  to its own action, which is the shape a search box accepts; a form is sent
  with every named control it holds, not just the one that was typed into, so a
  text box plus a filter select produces one working address. A submit on an empty
  form opens the first field instead of sending a blank search. A field opens
  holding the page's own value selected, the way the address bar does, so
  correcting a prefilled search is one press rather than a hold-to-delete. A
  submission also carries the form's hidden inputs (the `ie=utf-8`, site-key and
  locale constants a search handler expects), and two forms that share one action
  stay separate, so one press cannot send the other form's fields.
  ([player/webpage.go](player/webpage.go), [player/browser.go](player/browser.go),
  [player/vkeyboard.go](player/vkeyboard.go), [docs/BROWSER.md](docs/BROWSER.md);
  tests in `player/browserforms_test.go`).

- **One page header across every screen.** The Emulators page drew its own title,
  summary and count by hand, so it read as a bespoke panel next to Tools and
  About; it now opens with the same `UIPageHeader` those pages build — a tinted
  glyph well, the title, a summary under it and a read-out opposite. The
  full-screen panels that still laid the band out by hand (Games, Save Manager,
  Downloads, Curation, Community and its What's new / BIOS Doctor / Diagnostics
  sub-pages, Compatibility, Windows Apps, IP streams, Game profiles, Power Tools,
  RetroAchievements, Game options, Random game, LAN Play, and the browser's
  Links / Saved pages / Saved cookies overlays) go through it too, via
  `UIPanelHeader`/`UIPanelHeaderBand`, so the band a panel reserves and the bar
  that fills it are one measurement rather than two that can drift apart. A
  sub-page that started its body at a guessed offset now starts under the
  measured band, and the Power Tools output viewer and its scroll counter moved
  with it — the count sits beside the footer rather than over the subtitle.
  A subtitle colour was added to the component so a panel that warned in amber
  keeps warning in amber.
  ([player/emutiles.go](player/emutiles.go),
  [player/components.go](player/components.go); tests in
  `player/emulators_test.go` and
  `player/page_header_consistency_test.go`).
- **The browser remembers where you were reading.** Following a link and
  pressing Back (or Forward, or Reload) returns you to the paragraph you were
  on instead of the top of the page. Positions are kept per address for the
  session, bounded so a long browse cannot grow them without limit; a fresh
  navigation — a typed address, a link, Home — still starts at the top, because
  that is a new place, not a return. The restore is applied on the render thread
  once the page is laid out and clamped to its length, so a page that changed
  since it was last read is never scrolled out of range.
  ([player/browserscroll.go](player/browserscroll.go); tests in
  `player/browserscroll_test.go`).
- **Text zoom in the browser.** The reader's page can be scaled 75%–160% with
  L2/R2 on a pad or `-`/`+` on a keyboard, independent of the global
  accessibility text size. The whole styled page is re-measured from the new
  body size, so the wrap, row height and headings scale together; the toolbar
  chip names the scale whenever it is off 100%, and the choice is remembered across
  pages and relaunches. On a 5-inch panel the size of the words is a first-order
  control, so the web page's L2/R2 moved from jump-to-top/bottom (still one held
  D-pad away) to zoom.
  ([player/browserzoom.go](player/browserzoom.go); test in
  `player/browserzoom_test.go`).
- **A zoom chip on the browser toolbar.** The reader's text scale now has a
  home in the UI, not just the keyboard's `-`/`+` and the L2/R2 triggers: a
  measured chip beside the mode chip shows the current scale, lights up when it
  is off 100%, and steps up one notch on a tap — wrapping, so a tap always does
  something. On a gamepad-only handheld this is the discoverable way to reach
  the zoom. It gives up its spot first on a narrow panel, before Search.
  ([player/textbrowser.go](player/textbrowser.go)).
- **The named Power Mode is now in the Accessibility panel.** Battery Saver /
  Balanced / Performance / Default was only reachable from Quick Settings; it
  now has its own Accessibility row that steps through the same helper, so a
  player picks a battery-vs-speed trade-off without learning two separate
  toggles. ([player/a11y.go](player/a11y.go);
  test in `player/a11y_powermode_test.go`).
- **Dropping a file on the window plays it (desktop).** A video or audio file
  dragged onto JukaHub plays through the embedded player, an image opens in the
  viewer, a folder points at Files, and an unknown extension says so instead of
  silently ignoring the drop; a path that does not exist is refused up front.
  The handheld has no pointer, so this never fires there.
  ([player/dropfile.go](player/dropfile.go); test in
  `player/dropfile_test.go`).
- **Podcast feeds survive an offline boot.** The last-known-good copy of each
  feed is kept on the card (30-day window, 1 MiB cap) and served only when the
  network fetch fails outright — a live response, a 304 or an HTTP error status
  all win first, so a feed that is genuinely gone is never replayed as if it
  were live. ([player/feedcache.go](player/feedcache.go); tests in
  `player/feedcache_test.go`).
- **A bounded image-decode pool and a general semaphore.** Image decoding runs
  under a shared cap (2 jobs on the handheld, 4 on desktop) so a fan-out of
  decodes cannot open one RGBA buffer per item on a 1 GB device, and a weighted
  context-aware semaphore (`Acquire`/`TryAcquire`/`Release`) is available for
  every other concurrency path.
  ([player/bounded.go](player/bounded.go); tests in
  [player/bounded_test.go](player/bounded_test.go)).
- **A finger-sized hit area for compact controls.** `growTouchTarget` grows a
  drawn control's tap area to a 44px minimum, centred on what is drawn, so the
  36px Exit pill absorbs a near-miss press without its drawn geometry moving.
  ([player/touchtarget.go](player/touchtarget.go); tests in
  `player/touchtarget_test.go`).
- **The web builder stamps `schemaVersion`.** A config the builder exports now
  carries the format version the player checks on load instead of none, keeping
  an imported design's own version when it has one.
  ([script.js](script.js); the 17 web-builder tests still pass).

- **The builder's canvas now draws the shipped design the way the device does.**
  Tile buttons were plain coloured boxes with centred text, a `recent` element
  was a dark rectangle carrying its own (near-black) label colour, and the
  device surface was the designer's grey instead of the design's `Background`,
  so `player/jukaconfig.json` looked nothing like the handheld it was made for.
  The canvas now paints `Background` under the background image, draws a
  `style: "tile"` button as the player's launcher tile — 16 px radius, the glyph
  named by the config's `icon` at 44–64 px centred at 36% of the height, the
  label 14 px off the bottom edge — and draws a `recent` element as the Continue
  card (card surface, heading at 18 px, the icon-and-two-lines empty state),
  ignoring the colour fields the player ignores too. An `input` is now the
  device's 8 px rounded field with its text against the left edge and its
  placeholder in the muted colour, not a centred bold "Input", and generic
  elements follow the player's `RadiusMD` of 12 px instead of 6.
  ([script.js](script.js), [style.css](style.css); the 28 web-builder unit tests
  and 17 Playwright tests still pass).

- **Every tool screen draws the app's top bar.** Notes and the PDF reader
  painted a 44px strip with a title of their own, the alarm clock drew a
  bespoke "< Back" pill at 24,24, the Pomodoro timer drew no chrome at all and
  the calculator had no header — five screens, four different ideas of a
  header. All five
  now draw `renderStatusBar`/`renderFooter` like every other module scene, with
  their own read-outs moved under the bar (the note count on the search row, a
  PDF's title and page number on one quiet line, the alarm's clock and
  calendar) and their bodies laid out between `headerBarHeight()` and
  `footerBarHeight()`. The calculator's key grid and history column are centred
  in that band, which also keeps the history column on the Brick's 960px canvas
  instead of past its right edge, and the alarm's calendar does the same. The
  footer now describes each screen's real bindings. Two overlap defects the work
  exposed went with it: the notes list printed its title, preview and metadata
  through each other (three 30px lines in a 64px row; the preview and metadata
  now use a 16px face in an 80px row), and the PDF reader scrolled its text at a
  fixed 18px under a 30px font, with a line count taken from the card height
  rather than the clipped one.
  ([player/notes.go](player/notes.go), [player/calculator.go](player/calculator.go),
  [player/pdfreader.go](player/pdfreader.go), [player/alarm.go](player/alarm.go),
  [player/pomodoro.go](player/pomodoro.go), [player/extras.go](player/extras.go);
  tests in `player/header_test.go` and
  `player/calendar_popover_test.go`).

- **The top-bar calendar is a month view you can page through.** The clock's
  popover borrowed the alarm screen's corner widget: 28×22 cells drawn with the
  24px body font, so a day numeral was taller than its own row and two-digit
  days nearly touched, and it could only ever show the current month — there was
  no way to look at last week or at next month. The popover now has a card of its
  own with 46×38 cells, a month header (`‹ October 2026 ›`) and a fixed six-row
  grid, so the arrows do not move when a month needs five rows. The arrows are
  tap targets; Left/Right (or PageUp/PageDown, the D-pad, the shoulders) step a
  month and roll the year, and the month title — tinted while you are away from
  it — returns to today, as does A or `t`. The alarm's compact corner widget is
  unchanged. ([player/calendarpopover.go](player/calendarpopover.go),
  [player/home.go](player/home.go), [player/main.go](player/main.go),
  [player/controller.go](player/controller.go); tests in
  `player/calendarpopover_test.go`).

### Fixed

- **Whole-program audit, November 2026.** Ten reviewers went through every
  subsystem and about a hundred defects were fixed; the entries below are the
  ones a user could run into. Each fix carries a regression test where the
  failure can be reproduced off the device.

- **Brightness and the stick LEDs were never found on the Smart Pro or the
  Brick.** Every entry in `/sys/class/backlight` and `/sys/class/leds` is a
  kernel symlink, and the discovery kept only real directories, so Quick
  Settings had no brightness row and the LED sync had nothing to drive. Both
  now follow the class links ([player/systemcontrol.go](player/systemcontrol.go),
  [player/ledsync.go](player/ledsync.go)). The LED loop also stopped reading
  the live settings map from its own goroutine — a fatal concurrent map access
  once the LEDs were actually found.

- **Joining one Wi-Fi network disabled every other saved network.**
  `select_network` disables the rest, and the join saved the config straight
  after it, so the handheld never rejoined home after one visit elsewhere —
  and a failed join left everything disabled too. The other networks are now
  enabled again after the join either way, and the config is saved only once
  the join worked. Passwords and names with `"` or `\` were escaped into
  something else (wpa_supplicant reads quoted values verbatim), non-ASCII names
  shown as `\xNN` could not be joined at all, and a 64-digit raw key was always
  refused; names now travel as hex, passwords exactly as typed. The control
  socket used on images without `wpa_cli` could never connect (it is a
  datagram socket) and sent commands the supplicant does not accept; it now
  binds its own address and speaks the real protocol
  ([player/wifi.go](player/wifi.go), [player/wifi_ctrl.go](player/wifi_ctrl.go)).

- **Performance mode never switched the CPU — and its restore would have left
  the hub at the lowest clock.** The governor list was read from a file no
  kernel has, so every launch said "no performance governor". With that fixed,
  the way back after a game is now the governor the hub was running before it,
  not `powersave` ([player/powerprofile.go](player/powerprofile.go)). The
  low-battery guard now recognises TrimUI's own RetroArch (`ra64.trimui`) and
  its `.retroarch` config ([player/lowbattery.go](player/lowbattery.go)).

- **The self-update could kill its own helper.** The script that swaps the new
  binary in was started on a context cancelled a moment later, so it could die
  before the swap; it now runs detached. The Windows helper broke on paths with
  spaces, a half-extracted update was reused on the next try, and JukaHub could
  offer the release it was already running ([player/patch_swap.go](player/patch_swap.go),
  [player/autoupdate.go](player/autoupdate.go)). Patch journals overwrote one
  backup with another of the same name and never cleaned up after themselves,
  large downloads were cut off by a 25-second total timeout, and one install
  could cancel — or paint its error over — another install's card
  ([player/patch_engine.go](player/patch_engine.go),
  [player/installbar.go](player/installbar.go), [player/toolbox.go](player/toolbox.go)).

- **Settings came back after Settings → Save, and some never saved at all.**
  The hot reload that a save triggers merged the settings read at boot, undoing
  every change made since; it also ran off the main thread. Every config load
  wrote design values over the user's settings, Power Mode was never saved, a
  handheld boot forced Reduced Motion and Low Power on, and a setting changed in
  the second before quitting was lost. The debounced save marshalled live maps
  on a timer goroutine — a fatal crash waiting to happen — and web-form values
  (passwords included) and page text went into `jukauser.json`
  ([player/hotreload.go](player/hotreload.go), [player/extras.go](player/extras.go),
  [player/config.go](player/config.go), [player/main.go](player/main.go)).

- **Files: Delete on the ".." row deleted the parent folder.** Delete, Cut and
  the X mark now skip that row and refuse anything that holds the folder being
  browsed, a symlink is removed as a link instead of emptying what it points
  at (in Files and over SFTP), and a rename that only changes letter case
  works ([player/files_manager.go](player/files_manager.go),
  [player/sftp_root.go](player/sftp_root.go)). Restoring a save snapshot could
  prune away the very snapshot being restored
  ([player/savemanager.go](player/savemanager.go)).

- **Transfers and the phone companion.** An FTP resume wrote the client's tail
  over byte 0 of a truncated file, and long transfers were cut at 30 minutes.
  Companion uploads were buffered in RAM-backed `/tmp` and now stream to the
  card; repeated wrong PINs lock out for longer each time, and idle
  connections are closed. Expired cache entries are finally deleted
  ([player/ftp_server.go](player/ftp_server.go), [player/companion.go](player/companion.go),
  [player/cache.go](player/cache.go)).

- **Pausing and seeking a video could freeze the whole UI.** The audio device
  was paused and never resumed while the queue waited on it holding a lock.
  Muted playlists raced through, a one-track list repeated forever, a decoder
  that failed half way through counted as a finished track (and forgot the
  resume point), podcast episodes never started, downloads could adopt the
  wrong file, temporary video files were never removed, and Live TV froze on
  entry while it fetched its playlist ([player/sound.go](player/sound.go),
  [player/videoplayer.go](player/videoplayer.go), [player/musicplayer.go](player/musicplayer.go),
  [player/podcasts.go](player/podcasts.go), [player/downloads.go](player/downloads.go),
  [player/tvchannels.go](player/tvchannels.go)).

- **Input.** The dock's "screen off" was switched straight back on by the
  release of the button that turned it off, and a press that woke the screen
  also acted on it (B left the dock). A press now only wakes, and a drifting
  stick no longer keeps the screen awake. L2/R2 reached the screen under
  overlays, the controller test could not be closed once SELECT was remapped,
  closing the window during the splash froze it, the on-screen keyboard
  ignored quit and exit requests, a pad plugged in after launch did nothing,
  and typed text leaked into the note or terminal behind an open panel
  ([player/main.go](player/main.go), [player/screensaver.go](player/screensaver.go),
  [player/gamepad_hotplug.go](player/gamepad_hotplug.go), [player/splash.go](player/splash.go),
  [player/core.go](player/core.go), [player/shoulders.go](player/shoulders.go)).

- **Games.** A video snap kept decoding at full speed — and kept running
  under the game it launched; it now plays at its own pace and stops when a
  game starts. CD games were listed once per track and multi-disc `.m3u` sets
  not at all, an emulator was matched by substring ("nes" inside "snes"),
  Boost overwrote the user's own per-game options and revert deleted them, the
  BIOS doctor hashed hundreds of megabytes on the UI thread, the Apps grid
  listed JukaHub's own launch script, and relaunching an AndroidPak game within
  ten seconds had its files deleted under it
  ([player/snappreview.go](player/snappreview.go), [player/romsetup.go](player/romsetup.go),
  [player/game_library.go](player/game_library.go), [player/compat.go](player/compat.go),
  [player/biosdoctor.go](player/biosdoctor.go), [player/apps.go](player/apps.go),
  [player/androidpak.go](player/androidpak.go)).

- **Browser.** A page in a legacy encoding could crash the reader (and with it
  the hub); scanners now keep byte offsets intact and a parser panic shows an
  error page. Every page leaked the decoded pixels of its images, the 4 MiB
  image budget never held, a site could set a cookie for every `.com` site,
  and a sign-in form would have put the password in the address. Scripts are
  capped in output, recursion, memory and total time; deeply nested CSS and
  exponential `var()` chains are bounded. Find opened on the second match,
  zoom was never saved, the first rule after `@charset` was dropped, the last
  line hid behind the cookie strip, and Forget left signed-in pages in the
  Back cache ([player/textutil.go](player/textutil.go), [player/webpage.go](player/webpage.go),
  [player/webpage_image.go](player/webpage_image.go), [player/browserpsl.go](player/browserpsl.go),
  [player/webscript.go](player/webscript.go), [player/browserfind.go](player/browserfind.go),
  [player/browserzoom.go](player/browserzoom.go)).

- **Chat, Watch Party and Smart Home.** A chat image could declare a size
  that ran the handheld out of memory, Watch Party polled the network on the
  UI thread, Discord connections raced and leaked sessions, Smart Home toggles
  froze the UI for up to twelve seconds, Pi-hole sessions leaked, and AI chat
  replies were mirrored to Discord ([player/chat.go](player/chat.go),
  [player/partynet.go](player/partynet.go), [player/smart_home.go](player/smart_home.go),
  [player/smart_home_ui.go](player/smart_home_ui.go)).

- **Apps.** Daily alarms rang once and never again; a missed minute is now
  caught up, Y on the pad makes a new alarm, and L1 deletes only after a
  confirming second press. The terminal's `cd` moved the whole app — every
  relative save then went to the folder the user had wandered into — and an
  external command froze the UI until it ended; the terminal now keeps its own
  directory, runs commands in the background with a timeout, and Ctrl+C (or B)
  stops them. The dictionary closed the shared cache database and fetched
  suggestions over the network on every frame. Pomodoro resumed the wrong
  phase, the calculator cut π to six decimals and misread `2pi` and printed
  exponents, the forecast labelled yesterday "Today", a note open in the editor
  was lost on exit, and saved tickers vanished after a restart
  ([player/alarm.go](player/alarm.go), [player/terminal.go](player/terminal.go),
  [player/dictionary.go](player/dictionary.go), [player/pomodoro.go](player/pomodoro.go),
  [player/calculator.go](player/calculator.go), [player/weather_cache.go](player/weather_cache.go),
  [player/notes.go](player/notes.go), [player/morefeatures.go](player/morefeatures.go)).

- **The handheld config opened the new Home on the old grid.**
  `player/trimui/jukaconfig.device.json`, which the Smart Pro and Brick builds
  ship, still carried the 0.4.0 Home tile geometry, so the redesigned Home drew
  its cards in the old rectangles there. Its Main scene now matches the desktop
  config. Because `jukaconfig.json` is kept across updates, a config whose
  twelve Home tiles still sit exactly on the old geometry is also moved to the
  new grid in memory when it loads; a layout with any tile moved or resized is
  left alone ([player/home_migrate.go](player/home_migrate.go); tests in
  `player/home_migrate_test.go`).

- **Colour blends towards a darker colour came back unchanged.** `lerpColor`
  took each channel difference in `uint8`, which wraps whenever the target is
  darker, so a blend towards black returned the start colour: every Home
  glyph's shade and deep shade were plain white, and the parcel, folder and
  gamepad icons lost the planes they are drawn with
  ([player/core.go](player/core.go)).

- **A wallpaper that failed to load was retried on every frame.** Each frame
  reopened the file, attempted a decode and logged the failure. A failed path
  now waits 15 seconds before its next attempt, which still lets a wallpaper on
  a card that mounts late appear on its own ([player/core.go](player/core.go)).

- **Smaller fixes found on the way.** A toggle drawn before its font loaded
  dereferenced a nil font. White **GO** text on the browser's cyan button was
  about 1.7:1 and now takes the accent's contrast ink. The loading card's square
  outline was drawn straight across its rounded corners, the text browser's
  scrollbar ran down the side of its toolbar, and the unit converter's result
  rule poked out of its rounded corners. The scrollbar, the Files folder colour
  and the search spinner were dark-theme-only colours that vanished or clashed
  on light presets ([player/core.go](player/core.go),
  [player/textbrowser.go](player/textbrowser.go),
  [player/loadingcard.go](player/loadingcard.go),
  [player/unit.go](player/unit.go), [player/features.go](player/features.go)).

- **An SFTP client's `stat` of a folder described one of its children.** The
  server answered stat from the same listing it uses to browse, and the protocol
  reads only the first entry, so a folder looked like whatever file sorted first
  inside it and an empty folder looked missing — enough to make "create
  directory" fail in a client that calls `mkdir -p` underneath. Stat now answers
  with the entry itself, and a loopback test drives the console's own transfer
  client against its own server, covering connect, host-key pinning, download
  and upload ([player/sftp_root.go](player/sftp_root.go); tests in
  `player/filetransfer_loopback_test.go`).

- **A damaged snapshot can no longer half-overwrite live saves.** A restore
  copied the snapshot's files one at a time, so a truncated copy, a flipped byte
  or a file the card lost was only noticed after earlier saves had already been
  replaced. The whole snapshot is now planned and verified first — the folders
  are matched by path or by a unique name, no entry may escape its folder, a
  save subfolder may not be a link out of the tree, and every file must still
  match the size and SHA-256 recorded at capture time — and a snapshot that
  fails any of that is refused as a whole with "nothing was changed", before the
  safety snapshot is taken and before a live file is touched. Each file the
  restore does write is re-hashed, so a card that fills up part way through is
  caught there instead of at the next boot
  ([player/savemanager.go](player/savemanager.go); tests in
  [player/savemanager_test.go](player/savemanager_test.go)).

- **An archive cannot write outside its folder through a link it shipped.** The
  Windows-runtime extractor used to create the archive's symlinks while members
  were still being written, so a later member could be written *through* a link
  the archive itself supplied. Links are now collected during extraction and
  built once the tree is complete, with both ends re-checked against the
  extraction root, and every member name and link text now also passes the same
  prefix test the tool-download extractor uses — the check the analyser can see
  without following the helper — on top of the existing resolution guard. On the
  FAT/exFAT card, where links are impossible, the alias is still copied as
  before ([player/windows_runtime_install.go](player/windows_runtime_install.go);
  fuzz targets in `player/fuzz_test.go`).

- **The search log can no longer be derived from the API key.** The tool name a
  refused search command reports is now taken from the scene's own command text,
  before the config-provided `--extractor-args youtube:api_key=…` and
  `--cookies` arguments are appended to the argument list, so the string that
  reaches the log has nothing to do with the key. The cache key and the
  remembered paging command still describe the whole invocation, so the next
  page is built exactly as before ([player/core.go](player/core.go)). Three
  allocations stopped sizing themselves from a sum of lengths — merged search
  results, the yt-dlp argument vector and the MQTT length-prefixed string — and
  the battery reader now works in one integer type, which also keeps its charge
  counters from overflowing the percentage math on a 32-bit build
  ([player/media_paging.go](player/media_paging.go),
  [player/streaming.go](player/streaming.go),
  [player/smart_home.go](player/smart_home.go),
  [player/widgets.go](player/widgets.go)).

- **The parsers are fuzzed instead of only reviewed.**
  `player/fuzz_test.go` adds native Go fuzz targets for the
  archive entry and symlink names and for the transfer address field: each
  asserts an invariant (a name either gets refused or resolves below the
  extraction root; an address either gets refused or round-trips through its own
  canonical form), and `go test` runs every seed as an ordinary test. The nightly
  fuzz job now lists its targets per package — `-fuzz` accepts one package at a
  time — and installs the SDL headers the player package needs to build
  ([.github/workflows/nightly.yml](.github/workflows/nightly.yml),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#fuzzing)).

- **The browser follows a redirect, and the address bar says where it landed.**
  A `<meta http-equiv="refresh">` stub — "this page has moved" — arrives with a
  200 and a body, so nothing followed it automatically, and the reader sat on the
  stub with the link that would have continued stripped along with the page's
  scripts. A load now follows up to three in-page redirects with the same client
  and cookie jar, resolving the target against the page (quoted, unquoted,
  relative, `URL=` in any case) and refusing a non-web scheme, a target that
  names nothing, or a page that only asks to be reloaded on a longer timer; a
  loop stops at the limit. HTTP redirects were already followed by the client,
  but the toolbar and Back still named the address the reader typed, so they now
  move to the URL the load actually ended on — from the render pass, never while
  the address bar is being edited, and never for a page the reader has already
  navigated past ([player/textbrowser.go](player/textbrowser.go),
  [player/browser.go](player/browser.go); tests in
  `player/browser_redirect_test.go`).

- **The dark blue palette now separates, and its text is readable.** The shipped
  dark blue was one flat field: panels sat 1.11:1 above the page, rows 1.09:1
  above a panel and a focused row 1.27:1 above a resting one, so on the Brick's
  panel every card, row and button read as the same colour as the page behind
  it, and tertiary text (hints, captions, placeholders) sat at 3.4:1 on a card —
  under WCAG AA. The ramp is rebuilt so each step is a measured step (1.20:1,
  1.34:1 and 1.43:1), borders and hairlines rise with the surfaces they sit on,
  secondary and tertiary text clear 4.5:1 on the brightest surface, and the
  shipped configs' `$buttonColor`/`$inputColor` — the fill a scene button and an
  input field actually use — moved from `#1E293B`/`#0F1420` to
  `#2E4061`/`#1C2839`, where they read at 1.85:1 and 1.29:1 instead of 1.31:1 and
  1.04:1. Two defects the lighter buttons exposed are fixed with it: the
  label-colour chooser used a weighted 8-bit sum whose crossover sat at a 23.5%
  mean channel, so a mid-dark fill was given near-black text (~1.7:1) instead of
  white, and a focused tile was tinted with the raw accent — which is the
  config's button colour — so a dark accent painted the focused card darker
  than its neighbours, a hole instead of the brightest state on the page
  ([player/design.go](player/design.go), [player/themes.go](player/themes.go),
  [player/components.go](player/components.go), [player/appgrid.go](player/appgrid.go),
  [player/emutiles.go](player/emutiles.go), [player/toolsui.go](player/toolsui.go),
  [player/jukaconfig.json](player/jukaconfig.json),
  [player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json),
  [script.js](script.js), [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md); tests in
  `player/themes_test.go`).

- **The Emulators page is the same page as every other overlay.** It drew the
  shared title bar inside a card that ran out to the screen edge (20px of
  margin, a 22px radius and a hand-reserved 96px band), so its header sat in the
  corner at a different inset from Tools, Downloads and Compatibility and the
  band below it was a guess. The page now uses the shared full-page card —
  centred, 1040px wide (screen-clamped), 20px radius, the same 18px hairline and
  the same 24px body padding — and reserves the strip above the grid with
  `UIPanelHeaderBand`, the measurement the header itself is drawn from, placed
  at `UIPanelHeaderInset` like the rest. Because the card is narrower than the
  old edge-to-edge one, the widest control line (a core to update, a build to
  roll back to and the L1/R2/Y page keys all live) no longer fits: the page now
  sheds whole hints by measurement instead of letting the line be ellipsized,
  dropping the D-pad line first and always keeping Back
  ([player/emutiles.go](player/emutiles.go); tests in
  `player/emulators_test.go`,
  [player/emulatorinstall_test.go](player/emulatorinstall_test.go) and
  `player/page_header_consistency_test.go`).

- **Play Link asks for a link and plays it.** The Media screen's Play Link button
  only acted on a link that was already typed into the URL field, so with nothing
  highlighted it showed a toast and did nothing — and reaching that small field
  first is the very thing a handheld pad makes awkward. The button now opens the
  field on the on-screen keyboard with an empty buffer and the hint "Paste a video
  link, then Save"; Save plays what was typed instead of searching for it, an
  empty Save plays nothing, and dismissing the prompt disarms it so the next
  search still searches. A highlighted result still plays first, and a link
  already in the field still plays without asking again
  ([player/features.go](player/features.go), [player/core.go](player/core.go),
  [player/vkeyboard.go](player/vkeyboard.go); tests in
  `player/media_playlink_test.go`).

- **The Playtime badge list scrolls with the D-pad and the arrow keys.** The
  screen has no config elements, so its only focus nodes were the top-bar
  buttons: focus started on the Back pill, `handleChromeKey` claimed every arrow
  press, and the page's own key handler was never reached, so only the wheel,
  Page Up/Down, L1/R1 and Home/End moved the page while the D-pad and the arrow
  keys did nothing. A scene can now declare that it owns the page keys
  (`claimsPageKeys`), and the focus graph leaves the top bar out for it, so no
  arrow can be consumed by a button the screen is not offering. The bar stays a
  tap target there and is reachable through the Start menu ([player/focus.go](player/focus.go),
  [player/scene.go](player/scene.go); pinned by `TestPlaytimePageKeepsThePadFromTheTopBar`).

- **The Files, Packages and Emulator cards draw real pictures.** The Emulator
  card asked for an `emulator` glyph and the painter only knew `emulators`, so it
  fell through to the unknown-icon ring — a bare circle on the home screen.
  Files and Packages were each drawn in one flat tone (a folder whose tab and
  body were both white, a cube whose faces were 16 and 30 shades apart, which on
  a white glyph is still white), so at the 32px tile both read as blank
  rectangles with a detached tab or seam. Files is now a folder in three planes —
  a dark tab and back panel, a light sheet with a folded corner and two ruled
  lines, and a front pocket whose seam crosses the paper — and Packages is a
  taped parcel: shaded lid, tape band down the middle, shipping label off to one
  side. ([player/homelayout.go](player/homelayout.go); tests in
  `player/homelayout_icon_test.go` pin both the
  missing-glyph fallback and that the two redrawn glyphs paint three or more
  tones).

- **A Brick is identified even when its firmware names no model.** Identification
  trusted a model code in the device tree and a hostname that said "TrimUI",
  and a Brick image is not obliged to provide either: its kernel can describe
  only the SoC (`sun50iw10`, `allwinner,sun50i-a100`), and then the board stayed
  unnamed — no front F1/F2 keys, none of the handheld budgets, and the 4:3 canvas
  only because the panel fallback guessed it. Detection now reads every source
  the image offers and classifies the union: the launcher's exported `DEVICE`
  before its `PLATFORM` code (NextUI reports a Brick as `tg5040` plus
  `DEVICE=brick`), a model code in the device tree, `/proc/cpuinfo` or the
  hostname, the model names in `/etc/os-release` and the vendor launcher's own
  string table (the source community launchers use), and finally the panel —
  1024x768 for the Brick family, 1280x720 for the Smart Pro family — when the
  image is demonstrably TrimUI but nothing names a model. The startup log and the
  new `Device id` diagnostics line print the evidence, so an unidentified device
  reports what its firmware actually exposed instead of leaving it a guess.
  ([player/device.go](player/device.go), [player/device_probe.go](player/device_probe.go),
  [player/device_detect_first_run.go](player/device_detect_first_run.go),
  [docs/DEVICE_TUNING.md](docs/DEVICE_TUNING.md); tests in
  [player/device_probe_test.go](player/device_probe_test.go)).

- **Close and back are two buttons on the video player, and the windowed player
  answers taps.** Backing out of a playing video was one X that only meant
  "step down a size": it floated the video into the mini window and nothing on
  the overlay could end the session, so closing a windowed video meant leaving it
  playing or finding Stop at the far end of the bar. The corner now has Close,
  which ends playback where it stands, and the chevron beside it keeps the old
  behaviour (full screen → the scene's window → the floating window). Both are in
  the D-pad ring, so a pad user reaches either one. The windowed player's controls
  also take taps again: they were drawn over the video element but the hit test
  only ran in full screen, so touches in the window did nothing, and the bound is
  now the laid-out window itself, leaving the rest of the scene to the scene.
  ([player/videoplayer.go](player/videoplayer.go); tests in
  `player/videoplayer_close_test.go`,
  `player/videoplayer_controls_test.go`).

- **The Wi-Fi switch no longer turns the radio off.** `rfkill`'s verbs are
  `block`/`unblock`, and `runRfkill` had them the wrong way round: asking for
  Wi-Fi *on* ran `rfkill block wifi`, and *off* unblocked it. On an image whose
  only radio control is rfkill — every TrimUI handheld, since none ships
  `nmcli` — the Quick Settings switch and the picker's "press A to turn Wi-Fi
  on" both did the opposite of what they said, which is why the list stayed
  empty after turning Wi-Fi on. ([player/systemcontrol.go](player/systemcontrol.go);
  test in `player/wifi_supplicant_start_test.go`).

- **The Brick's Wi-Fi picker finds (and if needed starts) its supplicant.** Three
  gaps kept the picker from ever opening on the Brick. The supplicant's
  `ctrl_interface` was read when building a command but not when looking for the
  socket that *names the interface*, so discovery and the command path could
  disagree about the same device; P2P-device and `_global` sockets sort ahead of
  `wlan0` and were taken as the interface; and the TrimUI images start
  `wpa_supplicant` from the firmware's own Wi-Fi menu, so a device that had never
  had Wi-Fi on there had a radio and no socket — discovery then answered "no
  wireless interface" and the start menu dropped the Wi-Fi row, taking with it
  the only control that could create the socket. Discovery now reads the config's
  own directory first, skips P2P/global sockets, prefers a station name, and the
  picker starts the supplicant itself (once per interface, radio on only) and
  re-probes. The row and the Quick Settings link stay reachable while the radio is
  simply off, opening the picker in "turn Wi-Fi on" mode instead of turning the
  user away. ([player/wifi.go](player/wifi.go), [player/wifi_ui.go](player/wifi_ui.go),
  [player/startmenu.go](player/startmenu.go), [player/quicksettings.go](player/quicksettings.go),
  [docs/WIFI.md](docs/WIFI.md); tests in `player/wifi_supplicant_start_test.go`).

- **A Brick is identified by its model code, not only by its panel.** TrimUI
  images do not all stamp a name into `/etc/hostname`, so the Brick fell through
  to the 4:3 panel fallback: the canvas was compact while the board stayed
  unnamed, which left its front F1 / F2 keys dead and kept it on the desktop
  budgets. Detection now reads the kernel's own board description —
  `/proc/device-tree/model` and `/proc/device-tree/compatible`, with the sysfs
  mirror as a fallback — and accepts the `TG3040`-style model code found there as
  the device, so the Brick family gets its keys and handheld caps. A marketing
  name or an explicit override still works, conflicting codes are refused, and
  the result is cached instead of re-read every frame. ([player/device.go](player/device.go),
  [docs/DEVICE_TUNING.md](docs/DEVICE_TUNING.md); test in
  [player/patch_engine_test.go](player/patch_engine_test.go)).

- **The header stops offering an update the device already has.** The "Update"
  chip and the boot notification read a release recorded in patch state, and
  only a successful GitHub check cleared it — so a Brick without working Wi-Fi,
  or an install that replaced the files without going through the swap helper,
  kept showing the offer for a build it was already running. The advertisement
  is now compared with the running build's own stamp at startup and again
  before it is drawn, so "Update" appears only while it is a real update, and the
  stale entry is dropped from state instead of lingering. ([player/autoupdate.go](player/autoupdate.go),
  [player/updateprompt.go](player/updateprompt.go), [player/patch.go](player/patch.go),
  [player/toolsui.go](player/toolsui.go); test in
  [player/autoupdate_test.go](player/autoupdate_test.go)).

- **Wi-Fi works on a TrimUI Brick, and on any image that ships no `wpa_cli`.**
  The picker drives `wpa_supplicant`, but every command went through `wpa_cli`,
  and the start menu deletes the Wi-Fi row when the backend is unavailable — so
  a Brick that was online and downloading showed no Wi-Fi entry at all. TrimUI's
  TG5040 images (Brick and Smart Pro) run the supplicant with
  `ctrl_interface=/etc/wifi/sockets` and do not all include the client, so
  JukaHub now speaks the supplicant's own control socket when the CLI is absent:
  one command per write, the same parsers, the same password rules, and no
  process per poll (the status bar's signal read gets cheaper too).
  `/etc/wifi/sockets` is part of interface discovery now, and the unavailable
  message names both routes instead of claiming a missing `wpa_cli` that the
  image never had. ([player/wifi_ctrl.go](player/wifi_ctrl.go),
  [player/wifi.go](player/wifi.go); tests in
  `player/wifi_ctrl_test.go`).

- **A 4:3 panel fills the screen even when the hardware cannot be named.** The
  shipped design is 1280x720 and a 4:3 display letterboxes it into a strip with
  bars above and below — exactly what a Brick whose hostname and device tree
  name no TrimUI model showed, with the whole UI sitting in the middle 75% of
  the panel. The compact 960x720 canvas used to be reachable only through device
  detection; now the panel's own aspect picks it, and the startup log names the
  panel it chose. A design that is already 4:3, and the 16:9 Smart Pro panels,
  are left alone. ([player/uiprofile.go](player/uiprofile.go),
  [player/main.go](player/main.go); tests in
  `player/uiprofile_test.go`).

- **"Could not install ffplay" no longer fails on every attempt on the TrimUI
  Brick.** The handheld FFmpeg bundle is a ~130 MB `.tar.xz`, and JukaHub
  unpacked it with the system `tar`/`busybox`/`xz`. The Brick firmware cannot do
  that (no xz in its busybox; a tar that can still writes the whole ~400 MB tree
  to the card and, as root, trips over chown on exFAT), so the unpack failed
  every time and the card only showed the release URL. Each retry, and again
  each of ffplay, ffmpeg and ffprobe, downloaded the full 130 MB again. The
  bundle is now decoded in-process with the already-vendored xz reader, streams
  out only the binaries it needs, and reports unpack progress. A finished
  download is kept until it has been unpacked, so a retry after a full card or a
  cancel does not download it again; a damaged one is deleted so the retry
  fetches a fresh copy. Leftover partial downloads and unpack stages from a
  killed install are cleared, a card too full for the install is caught before
  the download starts, and the card names the cause instead of a URL.
  ([player/tools.go](player/tools.go), [player/patch_engine.go](player/patch_engine.go);
  tests in [player/tools_unpack_test.go](player/tools_unpack_test.go)).

- **`ffplay` is gone from every download.** Nothing has run it since playback
  moved to the embedded player (which decodes with `ffmpeg`), and BtbN's build
  needs X11, which the handhelds do not have, yet it was still fetched first on
  every launch and copied into the Smart Pro SD-card package. It is no longer an
  installable helper tool (asking for it by name is refused), the SD-card
  package ships only `ffmpeg` and `ffprobe`, and the FFmpeg bundle's copy is
  never unpacked. The **Settings → General → Audio Backend** row now says
  `ffmpeg (built in)` instead of `ffplay`. Copies already on a card are left
  alone and simply unused. ([player/patch_engine.go](player/patch_engine.go),
  [player/netmon.go](player/netmon.go),
  [player/package-sdcard.sh](player/package-sdcard.sh); tests in
  [player/tools_unpack_test.go](player/tools_unpack_test.go) and
  `player/mpv_removal_test.go`).

- **The Brick's front F1 / F2 keys do something.** The firmware reports them as
  L3 / R3, which almost nothing listened for, and the Controller card told Brick
  users to press "F1" for keyboard shortcuts. On a Brick (or Hammer) F1 now opens
  the Controller card for the current screen and F2 opens Quick Settings; the
  same key closes either. The audio player and the web browser keep their own
  L3 / R3 bindings. The card lists the Brick keys, and the Home footer names
  **Start** (the menu with every other screen) instead of a **B Back** that did
  nothing on Home. ([player/brickkeys.go](player/brickkeys.go),
  [player/controlslegend.go](player/controlslegend.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  `player/brickkeys_test.go`).

- **The header's "Update" pill can be selected from where it is drawn.** On Home
  the pill sits in the header's system corner, immediately left of the
  notification bell, but the home focus graph still had it where the chip used to
  live — left of the Wi-Fi meter, on the other side of the bar. The result was an
  offer that was visible and pressable with a mouse, yet impossible to reach with
  the D-pad: pressing **Left**/**Right** beside it walked past it (bell →
  now-playing chip → clock), and the only path the graph knew was pressing
  **Left** on the meter, which moved focus rightward, away from the pill. The
  graph now follows the drawn order — clock → now-playing → update → bell — so
  **Right** from the clock, and **Left** from the bell, land on it, while a
  hidden now-playing chip or an unadvertised release still falls through to the
  next control instead of trapping the pad.
  ([player/homefocus.go](player/homefocus.go), [docs/CONTROLS.md](docs/CONTROLS.md);
  tests in `player/updatechip_test.go`).

- **The Podcasts list no longer extends past the screen bottom on the Brick.**
The curated-feed scene's `dynamiclist` element carried the root config's portrait
geometry (x=24, y=148, width=1232, height=1672) in both shipped configs, on a
device whose logical canvas is 720×576 with a header bar and a footer row below
it: the 1232-wide, 1672-tall list rendered entirely off the screen, so podcast
entries were drawn but invisible, and the scene looked empty with no scroll to
reach them. The element now uses the landscape geometry every other list
element in the config uses — x=25, y=82, width=670, height=508 on both configs,
top-anchored under the header and ending above the footer — so the entries fill
the content band and are reachable with the same navigation as the rest of the
scene. ([player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json),
[player/jukaconfig.json](player/jukaconfig.json); regression test in
`player/layout_bounds_test.go`).

- **Opening the Wi-Fi picker no longer answers `exit status 255`.** On an image
  whose supplicant keeps its control socket somewhere `wpa_cli` does not look by
  default, every call failed with `Failed to connect to non-global ctrl_ifname`
  and a non-zero exit — and the panel showed Go's error text instead of the
  tool's, with an empty list under it. Three things changed. The control
  directory is now resolved once (`ctrl_interface=DIR=…` from the supplicant's
  own config first, then the conventional directories, verified by finding the
  interface's socket) and passed as `-p` to every call, so the tool reaches the
  supplicant wherever the image put it. A failed call reports what `wpa_cli`
  printed — `FAIL` as-is, an unreachable socket as *Wi-Fi control is
  unreachable — wpa_supplicant is not answering*, a permission error as *Wi-Fi is
  not permitted for this app* — instead of a number that names no cause and no
  fix. And `FAIL-BUSY` (the supplicant is already scanning, which the stock
  firmware causes routinely) is no longer a failure at all: the arriving results
  are used, and a scan that really fails now costs the list rather than the
  panel, which keeps showing the connected network and the saved entries with
  the reason in its header. ([player/wifi.go](player/wifi.go),
  [player/wifi_ui.go](player/wifi_ui.go), [docs/WIFI.md](docs/WIFI.md)).

- **A cancelled quality switch puts the Quality row back on the stream that is
  actually playing.** The switch pins its new ceiling before the re-resolve
  starts, because the yt-dlp format selector reads it at that moment; B cancels
  that resolve on purpose so the video already on screen keeps playing, but the
  row kept the cancelled ceiling — it named 1080p while 360p was on screen. The
  ceiling a stream is started at is now recorded as playback begins (by
  `startEmbeddedPlayback`, the one path every load reaches), and a switch that
  never gets there — cancelled, or failed while resolving — restores it, falling
  back to the value the press replaced when the stream was started outside the
  player's own load path. A late finish cannot rewrite the row once a newer play
  or switch has started, or once playback has moved to another video.
  ([player/streaming.go](player/streaming.go),
  [player/videoplayer.go](player/videoplayer.go); tests in
  `player/videoquality_revert_test.go`).
- **Discovery asks the supplicant which interface it drives when nothing on the
  card names it.** Some images expose the radio in neither `/sys/class/net` (no
  `wireless`/`phy80211` marker), nor `/proc/net/wireless`, nor any of the known
  control directories, and their netdev name is not one the naming convention
  knows — there discovery gave up and the picker said there was no radio. A
  fifth source now asks `wpa_cli interface` which netdev it is driving, falling
  back to `wpa_cli -i <netdev> status` over at most four candidates when the
  listing is empty or unusable. It is the
  last source before the name convention, and its answer is cached for a minute
  — including “nothing found” — because discovery is read while the status bar
  draws and an uncached probe would fork a process per frame. A probe never runs
  when an earlier source answered, and the cache is reset when a caller knows the
  hardware changed. ([player/wifi.go](player/wifi.go), [docs/WIFI.md](docs/WIFI.md);
  tests in `player/wifi_interface_test.go`).
- **Play is modal from the press, and B cancels all of it.** Pressing Play on a
  cold card spends the first seconds downloading yt-dlp/ffmpeg, and the loading
  state only began after those tools were resolved — so the install card was up
  while the results grid stayed live underneath it: the D-pad kept scrolling and
  the wait could not be dismissed. The prepare state now begins on the press,
  before any tool resolution, so the grid is frozen for the whole wait; the
  download honours that state's context, so a cancel stops the transfer and
  removes the partial file, and the install card is hidden rather than left
  showing a paused progress bar. The card also draws its way out as a control:
  a **B** badge and its **Cancel** label in the bottom band, in place of the
  grey "B / Esc cancels" line. The Quality row goes through the same load path,
  so re-resolving a stream at a new ceiling is a modal wait too, and cancelling
  it leaves the video that was already playing untouched. ([player/core.go](player/core.go),
  [player/streaming.go](player/streaming.go),
  [player/videoplayer.go](player/videoplayer.go),
  [player/patch_engine.go](player/patch_engine.go), [player/tools.go](player/tools.go),
  [player/installbar.go](player/installbar.go),
  [player/loadingcard.go](player/loadingcard.go); tests in
  `player/playprepare_cancel_test.go`).
- **The Wi-Fi picker finds the radio on a device that is online.** It reported `No wireless network interface on this device` while the handheld was connected. Interface discovery listed `/sys/class/net` and kept only entries that `DirEntry.IsDir` called a directory — but every entry there is a symlink into the device tree, so the link itself is never a directory and every real radio was filtered out. The probe now resolves the entry before testing it, accepts both the old `wireless` marker and the cfg80211 `phy80211` one, and falls back to `/proc/net/wireless`, to the supplicant's control sockets, and finally to a Wi-Fi interface name when a vendor driver exposes none of those. The status meter's "interface is up" check reads the same list, so a cfg80211 image no longer shows the no-link icon while connected. ([player/wifi.go](player/wifi.go), [player/statusbar_status.go](player/statusbar_status.go); tests in `player/wifi_interface_test.go`).
- **Every JSON store that holds your work now survives a power cut and a damaged file.** Notes, alarms and the IPTV list got this first; an audit of the rest found the same two defects in sixteen more stores (fourteen on both halves, two only when reading). Each was written with a temp file + rename (atomic, but with no fsync and no backup), so a card pulled at the wrong moment could still leave a zero-length file, and each loader returned an empty store on a parse error with at most one log line — a favourites list, a set of per-game overrides, a saved browser session or a PS4 pairing all read as "never existed". They now go through `AtomicWrite` (temp file, fsync, rename, previous version kept as `.bak`) and `readJSONWithBackup`, which recovers the backup and says so in the log. `favorites.json` was worse still: it was truncated by a bare `os.WriteFile` on every save. The stores are `favorites.json`, `game-options.json`, `game-recents.json`, `musicfavorites.json`, `audioresume.json`, `ipstreams.json`, `tvfavorites.json`, `browser-cookies.json`, `browser-bookmarks.json`, `browser-block.json`, `partyfriends.json`, `partyhistory.json`, `psremote.json`, `startup-state.json`, `save-sync/folders.json`, the curation profiles and the Toolbox's `power-scripts.json`; the `secret` command writes `jukauser.json` the same way now instead of skipping the backup. A store the user deletes — a cleared bookmark list, the browser's Forget (whole jar, one site, or an expiry prune) and a deleted curation profile — deletes its `.bak` in the same step, because data the user removed must not survive on the card. Caches (weather, RetroAchievements, artwork misses, the patch database) are deliberately left regenerable, and a file the app only reads (`game-launchers.json`) still falls back to its built-in default with a log rather than looking for a backup that could never exist. ([player/config_health.go](player/config_health.go), [player/browsercookies.go](player/browsercookies.go), [docs/CONFIGURATION.md](docs/CONFIGURATION.md); tests in [player/store_durability_test.go](player/store_durability_test.go), [player/store_durability_library_test.go](player/store_durability_library_test.go), [player/store_durability_media_test.go](player/store_durability_media_test.go) and [player/store_durability_social_test.go](player/store_durability_social_test.go)).
- **The game library releases box art it is done with.** The preview loads one
  cover per focused row, and the texture cache behind it was an unbounded map:
  scrolling through a few hundred games kept every cover for the life of the
  process — a 200×270 cover is ~216 KB, so a large collection pinned tens of
  megabytes of texture memory on a 1 GB card, exactly what the video decoder
  and the rest of the UI need. The cache is now bounded by bytes (32 MB on the
  handheld, 96 MB on desktop) and by count (256 entries, which also covers
  covers that are missing and cached as a miss), evicting the oldest first; the
  cover on screen is always the newest entry, so it is never the one evicted.
  ([player/game_library_ui.go](player/game_library_ui.go); tests in
  [player/game_library_test.go](player/game_library_test.go)).
- **The rendered-text texture cache has a byte budget.** It was bounded only by
  entry count (512), but that cache is where a wrapped web-page paragraph, a PDF
  line, a terminal row and a chat bubble land — a single one of those can be a
  megabyte, so the count alone was a multi-hundred-megabyte worst case. It now
  also honours 24 MB on the handheld and 64 MB on desktop, evicting least
  recently used entries and always keeping the label being drawn; the texture
  accounting line (`JUKAHUB_TEXSTATS=1`) prints usage against the budget.
  ([player/textcache.go](player/textcache.go), [player/perf.go](player/perf.go);
  tests in `player/textcache_test.go`).
- **A launcher full of broken icons no longer grows the icon cache.** An icon
  that fails to decode is cached as a miss so the file is not re-read every
  frame, but the miss skipped the eviction order: the count cap only ever
  counted icons that loaded, while the map grew with every missing image. Misses
  now go through the same bound. ([player/appicons.go](player/appicons.go); test
  in `player/appicons_test.go`).
- **Notes, alarms and the IPTV list survive a power cut.** All three stores were
  written with a bare `os.WriteFile`, which truncates the file before writing the
  new bytes: pulling the power, or filling the card mid-save, left a half-written
  document, and the loaders then started empty without a word — a reader's notes
  simply disappeared. They now save through the same temp+fsync+rename path as
  the config, keep a `.bak` of the previous version, and a store that cannot be
  parsed comes back from that backup with a line in the log instead of reading as
  deleted. The IPTV save had even documented itself as atomic.
  ([player/notes.go](player/notes.go), [player/alarm.go](player/alarm.go),
  [player/ipstreams.go](player/ipstreams.go),
  [player/config_health.go](player/config_health.go); tests in
  [player/store_durability_test.go](player/store_durability_test.go)).
- **The reader's form controls draw glyphs the bundled font has.** The first cut
  of the form work drew the tick box with U+2610/U+2611, the radio with U+25C9
  and the select arrow with U+25BE — none of which Inter carries, so the font
  sanitiser replaced each with an ellipsis and a checkbox read as "…". They draw
  with □ ■ ○ ● ▼ now, and a test walks every control's drawn text past
  `fontCoversRune` so a symbol that is not in the face cannot come back.
- **An unclosed form control no longer swallows the rest of the page.** A
  `<textarea>` or `<select>` whose closing tag never arrived collected every
  following block as its own value, so the page below it vanished into one field.
  It now ends at the next element that starts a new context (a paragraph, a
  table, the next field), while its own `<option>`s still belong to it.
- **An empty field's prompt reads as a prompt.** The reader has one face and
  never painted `webStyle.Italic`, so a placeholder looked exactly like a value;
  it is now dimmed toward the paper, and stops being dimmed the moment the field
  holds something.
- **Typing into a page's field no longer overwrites the page.** The keyboard's
  text went to the browser element's own variable, so filling a form control put
  the half-typed query in place of the page behind the keyboard; it now goes to
  the field's own buffer and the page stays on screen
  ([player/core.go](player/core.go)).
- **The browser opens on its start page, not on whatever the last run was
  reading.** The content and the address are saved with the rest of the config,
  and the first-open check read a filled content variable as a page this run had
  opened — so every launch came back to yesterday's page (a news home page in the
  report that prompted this) behind a stale address bar, with no request going
  out at all. The first open now always loads DuckDuckGo Lite; returning to a
  browser already used in the same run still keeps the page on screen, and a
  link from chat or the Home card still claims the session first.
  ([player/browser.go](player/browser.go); test in
  `player/browser_test.go`).
- **The browser's bottom chips fit their labels again.** Thirteen actions now
  share the row, and equal shares sized it by the count alone, so the widest
  label ("Forward") overflowed its chip on every panel and "Search" did the same
  at 960×544 and below — a control whose word is clipped is one the user has to
  guess at. A chip is now as wide as its own word needs to be at that panel's
  size, and the column's surplus is shared out pro rata, which fits with room to
  spare at 1280×720, 960×544 and 800×480.
  ([player/browser.go](player/browser.go); guard in
  `player/browsercookies_site_test.go`).
- **Five ways a "Wi-Fi stopped working" report turned out to be code.** A
  launcher's PATH can miss the sbin folders where firmware images keep
  `wpa_cli` and the DHCP clients, so the picker claimed they were not
  installed — tools are now resolved on PATH and then in the standard system
  directories. A soft-blocked radio surfaced as wpa_cli's bare `FAIL`; the
  scan now says "Wi-Fi is turned off". WPA3-personal routers could not be
  joined at all (the default key management is WPA-PSK-only) — secured joins
  offer `WPA-PSK SAE` and optional management-frame protection, falling back
  to the old defaults when the supplicant rejects either. A stored password
  the router no longer accepted was unfixable from the panel (the row retried
  the same key forever) — it now opens the passphrase keyboard, and the new
  key replaces the stale supplicant entry only after it joins. An image whose
  only DHCP client is `dhclient` never got a lease attempt.
  ([player/wifi.go](player/wifi.go), [player/wifi_ui.go](player/wifi_ui.go);
  tests in `player/wifi_resilience_test.go`,
  docs in [docs/WIFI.md](docs/WIFI.md)).
- **A tap in the Media gallery plays the row it lands on.** The list layout
  draws 100px rows at a 114px pitch, but the click path always used the
  grid's cell math — 81px cells against the shipped six-row list — so a tap
  on a visible row addressed the wrong result, and both layouts stopped
  hit-testing at the first screenful (`cols*rows`), which made every row
  after the sixth untappable once paging had grown the list. The renderer
  and the pointer now share one row measure (`searchResultIndexAt`), a grid
  or empty frame clears the list's Audio/Video/Chat pill rects instead of
  leaving them to answer taps aimed at nothing, and the results box rejects
  presses aimed at the controls above it
  ([player/core.go](player/core.go), [player/main.go](player/main.go),
  [player/media_paging.go](player/media_paging.go); tests in
  `player/searchresults_click_test.go`).
- **The browser's toolbar and cookie strip are full-size tap targets.** The
  drawn chips are 32px tall and the cookie strip is a font height — fine for
  a D-pad, short for a fingertip on the Brick's panel. The click path now
  grows every chrome hit area to the 44px handheld touch minimum, centred on
  the drawn rect and with its horizontal geometry untouched, so neighbours'
  gaps still guard against fat-finger overlaps while the drawn chrome looks
  exactly as before. This is the browser's slice of the touch-target audit in
  the modernization plan
  ([player/textbrowser.go](player/textbrowser.go),
  [player/main.go](player/main.go),
  [player/browsercookies_picker.go](player/browsercookies_picker.go)).
- **The Internet Browser's toolbar chips fit their labels on both panels.**
  SEARCH and SCROLL drew 97px and 94px of text inside fixed 84px and 80px
  chips at the Smart Pro's 1280 canvas — the words ran past the chip's
  border — and on the Brick those fixed widths left the address field short
  enough to truncate a URL that fits on the Smart Pro. Every chip now sizes
  itself from the same `small` font the frame draws with (label + padding,
  re-clamped so the row always fits the panel), the ad-filter chip takes
  what is left rather than a fraction of the panel, and the address field
  gets the remainder. The chips' click rects come from that same layout, and
  a two-canvas render test now fails if any chip's label is wider than the
  chip
  ([player/textbrowser.go](player/textbrowser.go),
  `player/browser_devices_render_test.go`).
- **The screensaver's hint line no longer runs under the Cookies row in
  Settings → General.** The injected helper ("Idle time before the screensaver
  starts. Tap to change.") draws 620px wide; at y=576 its tail ran beneath the
  retention row's band (556..604) in the right column and was cut mid-word by
  the button. It now sits at y=612 — under its own button, 8px below the row
  and above the panel's bottom edge — and a geometry assertion in the row's
  render test keeps the two boxes apart on every canvas
  ([player/widgets.go](player/widgets.go)).
- **The toolbox scan-gate test no longer races the tool it launches.** The
  second press on a scanner row runs the tool on a goroutine that resolves the
  install through the tools-dir override, while the test's cleanup restored
  that override the moment the assertions passed — under `-race` the detector
  saw the cleanup write against the runner's read, and outside it the runner's
  view of the fake install was a coin flip. The test now waits the async
  runner out before any cleanup can land on it
  (`player/toolbox_scan_gate_test.go`, `player/powertools_ui_test.go`).
- **The screensaver auto-tick tests no longer depend on what earlier tests
  left behind.** `screensaverAutoTick` reads the panels behind
  `ambientEntryBlocked`, the dock screen, the embedded player, the install
  bar, the patch label and the music state — none of which the tick tests
  owned, so a predecessor that left one of them raised held the saver off
  silently and the test failed for a reason it never touched. Both tests now
  pin that whole gate (save → clear → restore), and a failure names the
  blocking input instead of reporting a bare "did not start"
  (`player/screensaver_test.go`).
- **The first-run tour's controls hint no longer runs across the card.** The
  line was anchored 18px above the card's bottom edge, but the band under the
  buttons is only 28px deep and the line is 22px tall on the Brick's compact
  canvas (30px at 1280): a first-run photo showed it drawn through the card's
  bottom border and against the Next button. It now sits centred in the dimmed
  band below the card, clamped to stay on screen on short panels. The step
  body was also cut by pixel count rather than at word boundaries — "then"
  broke as "th" / "n shows" on the Brick, and a "…" was appended even when
  nothing was cut; it is word-wrapped to the same two lines
  ([player/onboarding.go](player/onboarding.go), `player/onboarding_render_test.go`).
- **Buttons now measure the label they draw, so a row's derived value is
  neither clipped nor printed raw.** `UIButton` ellipsized the label template
  (`Screensaver · $screensaverTimeoutLabel`) and then drew the *substituted*
  text: a value longer than its template overran the button, and a template
  longer than the value was cut mid-placeholder, so the literal `$screens…`
  reached the screen. Every Settings row that shows a live value was affected —
  **Screensaver**, **Saved cookies** and **Video snaps**. The substitution now
  happens once, before the fit, so the measurement and the drawing agree
  ([player/components.go](player/components.go)).
- **A video snap ffmpeg cannot decode is now remembered, instead of being
  restarted on the next frame.** The session memo was written under the clip's
  path and read under the game's ROM path, so the two never matched: a broken
  clip was marked, the mark was then looked up under a name nothing wrote, and
  the library panel spawned a new ffmpeg for it again immediately — the process
  storm the memo exists to prevent, on the one device with a single core to
  spare. Both reads now use the clip path the decoder was handed, and the
  `TestSnapPreviewBrokenClipIsNotRetried` test asserts the armed state rather
  than counting goroutines, which is what made it flaky in both directions
  ([player/snappreview.go](player/snappreview.go), `player/snappreview_test.go`).

### Changed

- **Every screen now wears Home's design.** Home's frosted glass — the navy
  backdrop with its cyan and violet blooms, translucent tiles with a hairline
  edge, the 3px cyan focus ring that lifts a control with a soft bloom behind
  it, and the inset footer band with glass button badges — is now one shared
  recipe in [player/ui_glass.go](player/ui_glass.go) that every other screen
  draws with, instead of the older slate recipe that made leaving Home look
  like opening a different app. Scenes stand on Home's backdrop rather than the
  stock wallpaper behind an opaque panel; the status row has no bar chrome; the
  footer is Home's inset band with right-aligned hint badges at Home's 16px
  type; and buttons, inputs, toggles, list rows, page headers, category tiles,
  overlay sheets and their dim, the Tools and Settings tile pages, Quick
  Settings, the Start menu, notifications, the calendar and weather cards,
  Files, Apps, Emulators, Downloads, Favorites, Chat, the on-screen keyboard,
  the browser's toolbar and panels, the unit converter, Shorts, Live TV, the
  disk chart, the loading card and the module screens (Calculator, Notes,
  Alarm, Pomodoro, Dictionary, PDF reader, Terminal, Music, Dashboard, About,
  Playtime, Patches, Games) all draw those surfaces
  ([player/components.go](player/components.go), [player/core.go](player/core.go),
  [player/extras.go](player/extras.go), [player/header.go](player/header.go)
  and the screens themselves). The glass is theme-aware: on a light preset it
  is mixed from a deep slate instead of white, and text that was hard-coded
  light now takes the theme's inks, so the six light presets get visible cards
  and readable type.

- **Focus, pills and highlights use the theme's accent.** The shell's accent
  was `$buttonColor`, a surface colour (slate by default), so every scene's
  focus ring and selection was a navy that barely showed beside Home's cyan. It
  is now the active theme's accent, and a config reload applies it after the
  preset instead of overwriting the preset's accent with the button colour.
  `$buttonColor` stays as a light tint under scene buttons
  ([player/main.go](player/main.go), [player/ui_glass.go](player/ui_glass.go)).

- **The dark palette's surfaces are Home's blue-grey.** The opaque panel, card,
  raised and focus surfaces were a saturated slate that clashed with the glass;
  they are now the neutral blue-grey white glass makes over the navy
  (#1B2029, #242B37, #2D3644, #404D62) at the same luminance, so every contrast
  ratio the palette tests pin is unchanged
  ([player/design.go](player/design.go), [player/themes.go](player/themes.go)).

- **Lighter frames on the handheld.** The stock 2560x1440 `background.jpg` —
  about 15 MB of texture — is no longer loaded, because nothing draws it. A
  wallpaper the user picks is scaled once to the canvas (1280x720, or 960x720
  on the Brick) and kept as an opaque static texture, so a frame copies a
  canvas-sized image with blending off instead of minifying a full-size photo.
  The scene panel's 1232x590 rounded texture, its three stacked shadows, sheen
  lines and corner dots, and the drop shadows under buttons, chips and cards
  are gone as well ([player/core.go](player/core.go),
  [player/ui_glass.go](player/ui_glass.go)).

- **The release offer is a quiet system chip, not a warning banner.** It sat in
  the same corner as the notification bell but at the labelled buttons' full
  height, filled with a warning tint and outlined at 200 alpha — the loudest
  thing in the bar, next to a bell that is a plain surface chip. It now takes the
  bell's height (the shared button height minus the same step), the same
  `ColorIconSurface` fill with no outline, a secondary-colour label and a
  smaller, dimmer arrow, so an available release reads as one more system
  control. Geometry still comes from one helper, so Home and every scene draw
  the same pill, and the focused state is the same accent ring the bell uses
  ([player/updateprompt.go](player/updateprompt.go); pinned by
  `TestReleaseOfferWearsTheBellsSurface`, `TestSceneHeaderUsesTheSharedButtonHeight`
  and `TestHomeUpdateChipSitsLeftOfTheBell`).

- **The Benchmark screen has one Run button that measures both phases.** Run and
  Random were two buttons running two tests, so pressing Run reported half of what
  the screen measures and recorded the board entry from the sequential pass alone;
  the random pass happened only if someone remembered the second button, and its
  numbers then replaced the first pass's text. One press now runs the sequential
  50 MB stream and then the 10 MB scattered 4 KiB test automatically, reports all
  four numbers in one panel (or which phase failed, and the other phase still
  runs), keeps recording the board entry from the sequential pass, and still
  stops on leaving the screen. The old `benchmark_random` trigger is left handled
  for hand-edited configs ([player/morefeatures.go](player/morefeatures.go),
  [player/jukaconfig.json](player/jukaconfig.json),
  [player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json);
  tests in `player/benchmark_test.go`).

- **The notification bell is a header button, not a headline.** It was sized off
  the status font and came out a 54×40 capsule with a 32px glyph on the 1280
  canvas — taller than the Back pill and nearly twice the height of the caption
  text beside it, with an unread badge that hung over the bar's top edge. It now
  takes the shared header button height minus a step (a square 32px chip), the
  glyph is inset to 20px so it reads with the clock, and the count is drawn on
  its own small font inside the corner. Home and the scene bar draw it through
  one sizing rule, so the corner is identical on both ([player/notifications.go](player/notifications.go),
  [player/homelayout.go](player/homelayout.go); pinned by `TestBellChipMatchesHeaderMetrics`
  in `player/notifications_test.go`).

- **Background polling stops while JukaHub is not on screen.** The weather ticker,
  the connectivity probe, the audio-output probe and the LED animation ran for
  the whole life of the process, so a handheld sitting in a game — or a desktop
  window minimised in the background — kept forking `bluetoothctl`, dialling a
  probe host, fetching a geolocated forecast and writing LED sysfs thirty times
  a second for a panel it could not see. They now wait through one gate,
  `backgroundPaused` ([player/backgroundwork.go](player/backgroundwork.go)),
  which is true while a launched game/emulator/port owns the framebuffer or the
  window is minimised; the disk pie's existing per-scene skip is the same rule.
  The tick is kept, so returning costs one interval at most, and the transition
  back to the foreground — the last screen-taking child exiting, or a
  store/restore of the window — broadcasts a wake that refreshes the slow
  readings at once: a forecast on a ten-minute timer no longer stays stale for
  the rest of the session after a game
  ([player/gamesuspend.go](player/gamesuspend.go), [player/main.go](player/main.go);
  tests in `player/backgroundwork_test.go`).

- **The handheld keeps its emulator cores current by itself, and stops piling
  up the builds it replaces.** Updating every installed core meant opening
  Emulators and pressing Update (or L1), so a card could sit on a nightly from
  months ago with no sign on screen that anything was stale. `startCoreAutoUpdate`
  now runs the same pass once a day on the Trimui — after boot, only where
  cores are installable, only online, and taking the one patch slot so it can
  never overlap a manual install — with `JUKAHUB_NO_CORE_AUTOUPDATE` as the
  opt-out. The download is visible while it runs: the install card grew an
  optional title, so it reads "Updating emulator cores" and draws over whatever
  screen is up, with the core's name and the count of cores in its detail line.
  A pass that could not read the index does not record a check, so an offline
  boot tries again next launch instead of waiting a day. Each replacement now
  keeps at most five `<core>.jukahub-<stamp>.bak` builds and deletes the older
  ones: every install, activation, rollback and automatic update goes through
  `installCoreFile`, so the limit lives in one place, and pruning reuses the
  parser that lists backups for the rollback action, so a `.bak` from another
  tool is never touched. The downloaded builds behind them are bounded the same
  way, at five dated folders per core, because a self-updating card would
  otherwise collect one per core per day; the build just fetched is never a
  pruning candidate. Rollback is unchanged — the newest build is always the one
  it offers
  ([player/emulatorautoupdate.go](player/emulatorautoupdate.go),
  [player/emulatorinstall.go](player/emulatorinstall.go),
  [player/installbar.go](player/installbar.go),
  [player/compat.go](player/compat.go); tests in
  [player/emulatorautoupdate_test.go](player/emulatorautoupdate_test.go)).
- **A home frame no longer pays for work it covers up, and the focus lift no
  longer mints half a megabyte of texture per step.** Profiling the 720p home
  frame turned up two costs that were not the drawing anyone sees. The home path
  painted the backdrop image or theme gradient and then a full-screen blended
  overlay, and the layout's own backdrop filled the screen opaquely on top, so
  those pixels were never shown — and `renderScene` ran the same block again
  before calling the home renderer, which made three passes and one visible. The
  backdrop is now drawn only for the config-driven scenes. The second was
  texture churn: the focused card's hover ease grows it a pixel at a time and
  the shape cache keys textures by size, so each step created a fresh card
  surface and focus ring (485 KB each at 720p) and pushed them into a 12 MB
  cache the handheld budgets for the whole screen — every D-pad move evicted the
  rest of the screen's chrome. The cache now takes a base geometry
  (`fillRoundedRectAt`, `strokeRoundedRectAt`, `DrawHomeCardSurfaceAt`) and
  blits into the grown rect, so one texture per card serves the whole animation.
  Measured on the desktop proxy: a warm home frame 110–124 µs → 46–53 µs, a
  resting frame creating zero new shape textures, and one full lift of the
  480×258 Continue card 2,972 KB → under 64 KB of shape memory
  ([player/shapeCache.go](player/shapeCache.go), [player/core.go](player/core.go),
  [player/homelayout.go](player/homelayout.go), [player/home.go](player/home.go),
  `player/shape_cache_animation_test.go`).
- **The scene-navigation memory harness measures the box-art ceiling that
  actually ships.** Its library phase sizes its synthetic card from the art
  budget, but it read whichever branch the host takes, so a desktop run measured
  the 96 MB ceiling while the Linux CI runner and the device measured 32 MB: the
  budget on the card went unverified on the machine the work was done on. The
  harness now pins the handheld branch (`libraryArtBudgetForce`, an atomic seam
  so the eviction path can read it from the render loop), asserts the pinned
  budget is the 32 MB one, and therefore walks the handheld fixture — 26 covers
  against 32 MB, settling at 17 — under the `-race` CI job as well as locally.
  `TestLibraryArtBudgetBranches` pins both ceilings and the derivation, so
  forcing one branch does not leave the other untested
  ([player/game_library_ui.go](player/game_library_ui.go),
  `player/memory_growth_test.go`,
  [player/game_library_test.go](player/game_library_test.go)).
- **One top bar on every screen, and a Back button you can find at arm's
  length.** Home and every scene already drew the same band, but the music
  player and the terminal still drew their own strips: the player had the words
  "Audio Player" and a `B Back` hint where the bar should be, and the terminal a
  `< BACK` text pill, so two of the app's screens had a header the rest did not.
  Both now draw the shared bar — wordmark and version, the scene in the caption,
  the same Back and Menu pills, the release offer and the system cluster — with
  the player's StockOS layout tag moved into its now-playing strip and the
  terminal's Keyboard chip and working directory kept beside the app menu. The
  Back pill is a step taller than the bar's other buttons and is drawn in a 20px
  face twice, one pixel apart (the bundled Inter has no bold face, and on a
  5-inch panel weight is what makes it read as the primary control); its chevron
  points the way the control moves instead of the opposite way, and its colour
  changes with focus: accent-tinted with accent type while idle, solid accent
  with dark type while the D-pad holds it, and a ring around it in that state.
  The release offer now keeps the same corner gap to the bell on every screen,
  so the pill no longer sits 16px from the bell in a scene and 10px on Home.
  `TestEveryScreenDrawsTheSharedBar` renders every scene in the shipped config
  and fails if one of them is missing the bar, the Back pill, the offer or the
  bell ([player/header.go](player/header.go),
  [player/musicplayer.go](player/musicplayer.go),
  [player/musicskins.go](player/musicskins.go),
  [player/terminal.go](player/terminal.go), [player/extras.go](player/extras.go);
  tests in `player/header_test.go`).

- **Text is measured once instead of on every frame, which is what the handheld
  was spending its frame on.** Profiling a warm home frame put ~40% of it inside
  `ellipsize` and another ~20% in the font-coverage walk, because every card,
  chip, footer hint and status-bar label re-measured the same unchanged string
  every frame — and each measurement is a `TTF_SizeUTF8` call through cgo into
  FreeType, which on the Smart Pro's CPU costs several times what it does on a
  desktop. Three bounded caches now sit on those paths: the fitted label, the
  plain width and the character substitution, all keyed by face + string and
  cleared by the `ClearTextCache` hook every theme and font-size change already
  calls. On the desktop proxy the home frame went from ~342 µs to ~90–125 µs and
  a launcher page from ~300 µs to ~86 µs, with ~40 fewer allocations per frame;
  the clipped, measured and substituted text is identical — each cache is
  pinned to the uncached function by `textcache_fit_test.go`
  ([player/textcache.go](player/textcache.go), [player/homelayout.go](player/homelayout.go),
  [player/extras.go](player/extras.go), [player/statusbar_status.go](player/statusbar_status.go),
  [player/ps4tile.go](player/ps4tile.go), [player/ps5tile.go](player/ps5tile.go)).

### Added

- **`render_bench_test.go` also benches a config-driven page.** The existing
  benchmarks measured the home screen alone, but most of a session is spent on
  launcher and list pages, which build their header, section labels, tiles and
  footer through a different path. `BenchmarkMenuPageFrame720p` renders the
  Settings launcher page as the user opens it, so a change to a shared
  component shows up in the frame that actually draws it.

- **The diagnostics bundle carries the cookie session's date and retention
  window.** A bug report about being signed out arrived without the two
  numbers that decide it. The bundle now has a `Saved cookies` section between
  the report and the log — `Session start: Oct 12 2026 (40 days ago)` from the
  oldest first-seen stamp still on the card, and `Retention    : 30 days` (or
  `Keep`) from the setting — so a retention contradiction ("30-day window,
  40-day session") is visible at a glance. A jar with no cookies says
  `none stored` instead of a zero date, and a stamp ahead of the device's
  clock says so — handhelds with a reset clock are a real support case
  ([player/diagnostics_bundle.go](player/diagnostics_bundle.go),
  `player/diagnostics_bundle_test.go`).
- **Each site's row in the saved-cookie picker carries its own session start.**
  The Forget chip's count said how much would go, but two sites signed in on
  different days looked the same age — the one date on the Settings row is the
  whole jar's oldest stamp. A picker row now reads `2 cookies · since Oct 12`,
  the oldest first-seen stamp among that host's cookies alone, so the age of
  one site's login is visible before the second press that drops it. The site
  name's budget shrinks by the label's real width so the two right-aligned
  strings can never be drawn through each other, a host with no stamp shows
  its count alone, and the forget-all row keeps no date because it is not a
  site
  ([player/browsercookies.go](player/browsercookies.go),
  [player/browsercookies_picker.go](player/browsercookies_picker.go),
  `player/browsercookies_picker_age_test.go`).
- **The Games Library header carries the clip count, the same figure Settings
  reports.** The Video snaps row could say `On · 12`, but the screen where the
  previews actually play said only how many games and covers it held. The
  status line under the title now appends `· 12 clip(s)` — or `· no clips` on
  a card with none, and nothing at all until the first count lands, exactly
  the row's own rule — read from the same census over `gameSnapPath`, so the
  two can never disagree. Opening the library panel is what asks for the
  count: one scene-work scan, reused for 30 seconds
  ([player/game_library_ui.go](player/game_library_ui.go),
  `player/snappreview_count_test.go`).
- **Pressing an installed package row offers Remove or Reinstall instead of
  running Install again.** A press always started a fresh download, even on a
  row already marked ✓ on the card. A press on an installed row now opens a
  menu over the details pane — **Reinstall** (wipe the old copy, then fetch a
  fresh one; the row's mark goes with the wipe so a failed download cannot
  leave a tick on an empty directory), **Remove** (take the directory off the
  card and unmark the row), **Cancel** — on the pad, the keyboard, and by
  tapping the focused row, which is how a touch user presses a row at all.
  Uninstalled rows still install in one press
  ([player/packages.go](player/packages.go), `player/packages_test.go`).
- **The Settings cookie row says when the saved session was first stored.**
  `Saved cookies · 30 days` answered "30 days from when?" with nothing to point
  at. The row now carries the reference point — `Cookies · 30d · since Oct 12` —
  the oldest first-seen stamp still on the card, re-read when Settings → General
  is entered and after a prune that drops it, and cleared with the session by
  **Forget now**. The window is shortened to `30d` because the date and the full
  `30 days` cannot both fit the row's cell on the 800px panel
  ([player/browsercookies_retention.go](player/browsercookies_retention.go),
  [player/widgets.go](player/widgets.go), `player/browsercookies_age_test.go`).
- **The Video snaps row says how many games on the card actually have a clip.**
  The switch could read **On** on a card holding no clips at all, which is a
  setting that changes nothing and no way to find out without opening the
  library. The row's value now carries the count — `Video snaps · On · 12` — and
  a card with none reads `On · none`. It counts the same `gameSnapPath` the
  preview plays, so a clip left behind for a ROM that is gone is not counted,
  and the row's state stays first because the count describes what the switch
  has to work with, not whether it is on. The scan runs off the render thread
  when **Settings → General** opens — one library walk, reused for 30 seconds —
  so the page never blocks on the card and the figure lands on the row as soon
  as it is known ([player/snappreview.go](player/snappreview.go),
  [player/snappreview_count.go](player/snappreview_count.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#video-snaps),
  `player/snappreview_count_test.go`).
- **The Browser can forget one site's cookies instead of the whole jar.**
  Signing one site out meant dropping every other login with it and retyping
  them on a D-pad. The toolbar gains **Sites** — also the controller route into
  the picker — and the strip under the page opens the same panel when tapped,
  since that line is already the one naming the hosts. The panel lists each host
  with its cookie count plus a **Forget all** row; a row asks twice, the first
  press naming what would go in the header line, and a confirmed press drops that
  site's records, rewrites `data/browser-cookies.json` with what is left (or
  deletes it when that was the last site) and reloads the page. The jar is
  rebuilt from the remaining records, because the standard jar has no way to
  delete a single cookie: dropping the record alone would leave the jar still
  sending it. An arm belongs to one row, and **Forget all** shares the chip's
  confirm window ([player/browsercookies.go](player/browsercookies.go),
  [player/browsercookies_picker.go](player/browsercookies_picker.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-one-site),
  `player/browsercookies_site_test.go`).
- **The Package Manager list marks what is already on the card.** Installed state
  lived only in the details pane's button, so finding out what was still missing
  meant stepping the cursor down every row and reading it. A row whose package
  directory exists now carries the app's check glyph in a measured left gutter,
  in the success colour — the same ✓ the install bar ends on — and names line up
  whether or not a package is installed. The flags come from one in-memory set
  refreshed with a single directory scan when the screen loads, rather than a
  stat per row per frame, and the install path, the installed check and the mark
  now share one name sanitiser so they can never disagree about which directory
  a package means ([player/packages.go](player/packages.go),
  [docs/CONFIGURATION.md](docs/CONFIGURATION.md), `player/packages_test.go`).

- **Clearing the saved browser session is one press from Settings.** The
  retention row explained how long a cookie is kept but the only way to drop one
  now was to open the Browser, find the toolbar chip and confirm a reload.
  **Settings → General** now pairs it with **Forget now**, in the bottom-right
  cluster above **Save**: the same two-press drop, the same toast, the same
  deletion of `data/browser-cookies.json`, and the same shared arm, so a press in
  either place can be finished in the other. The two cannot share one row — at
  this font `Saved cookies · 30 days` is 275px and `Forget now` 127px, so
  splitting the 360px band would ellipsize both ([player/widgets.go](player/widgets.go),
  [player/browsercookies_retention.go](player/browsercookies_retention.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-them),
  `player/browsercookies_forgetrow_test.go`).
- **Every page resolves D-pad direction the way its arrows were drawn.** The
  focus graph's fallback — the pass that guarantees each control has at least
  one neighbour per direction — classified an edge by the *sign* of its offset
  rather than by the axis it was built along. A control off to the side and
  slightly higher therefore counted as the neighbour above, and the control that
  really was directly above became unreachable: on Settings → General, the
  "Replay tour" button in the top-right corner stole `Up` from the Groq API key
  field, stranding **Weather** behind it. Edges are now classified by their
  dominant axis, which is the same rule that decided they existed
  ([player/focus.go](player/focus.go), `player/dpad_audit_test.go`).

### Changed

- **The Browser's saved-cookie count now says which sites it belongs to.**
  **Forget 3** on the toolbar told the user that a session was remembered but
  not where, and the hosts only appeared in a toast after pressing. The strip
  under the page panel names them — **Saved cookie: youtube.com**, **Saved
  cookies: youtube.com, github.com**, with **+N more** after the first three —
  built from the same jar records the count is, sorted by cookie count with ties
  by name. A cookie scoped to a domain is named by that domain (a `.google.com`
  cookie reads `google.com`) and a `www.` prefix is dropped. An empty jar says
  **No saved cookies** instead of leaving the strip blank, and the strip's height
  is reserved for the session so a page does not reflow when a login arrives
  ([player/browsercookies.go](player/browsercookies.go),
  [player/browser.go](player/browser.go),
  [player/textbrowser.go](player/textbrowser.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-them),
  `player/browser_cookie_sites_test.go`).
- **The Package Manager's rows are half their old height.** Each row drew the
  name on one line and version · author on a second one beneath it, which
  doubled the pitch and still left the right half of the row empty. A row is now
  one measured line — name on the left, version · author right-aligned and
  capped at two fifths of the row so a long author cannot squeeze the name out —
  so the 516px pane holds roughly twice as many packages (13 instead of 7 with
  the default metrics) and no longer wastes a row's worth of width. The pitch
  still comes off the loaded fonts and the click handler shares it, so a tap
  cannot land on a differently paced row ([player/packages.go](player/packages.go),
  `player/packages_test.go`).
- **The Wi-Fi picker's connected row now reads the same signal chain as the
  corner meter.** Every row used to draw from the scan table's RSSI, which for
  the network the device is actually on can be missing, stale, or a driver's
  `0` — one thinly-filled bar on an image without `/proc/net/wireless`, while
  the meter beside it showed a real level. The **CONNECTED** row now draws the
  meter's reading (the `/proc/net/wireless` → `wpa_cli signal_poll` chain, via
  the same `statusWiFiSignal` seam), and the other rows keep their own scan
  values, because they are not the link the device is on. A meter that reports
  "unknown" or "no link" leaves the scan value in place rather than drawing a
  state the row has no room to explain ([player/wifi.go](player/wifi.go),
  [player/wifi_ui.go](player/wifi_ui.go),
  [docs/WIFI.md](docs/WIFI.md#where-the-number-comes-from),
  `player/wifi_row_bars_test.go`).

### Added

- **Video snaps can be switched off from Settings.** The library's preview box
  was gated by the `gameSnaps` key in `jukauser.json` and nothing wrote it, so
  the only way to stop the preview moving was to hand-edit the file. Settings →
  General now carries **Video snaps · On/Off** beside the Swap A/B toggle: a
  press flips the key, saves it through the usual debounced writer and stops a
  preview that is already playing on the next frame. The row is a button rather
  than a toggle element because the feature is on by default — a toggle would
  have shown "off" for an unset key while previews were playing. An explicit
  `"false"` string (what a hand-edited file or a toggle element writes) is now
  read as off instead of being ignored, and the newly injected row also moved
  **Replay tour**, which sat exactly on top of the screensaver row
  ([player/snappreview.go](player/snappreview.go), [player/core.go](player/core.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#video-snaps),
  `player/snappreview_setting_test.go`).
- **The first-run tour and the controller legend now point at the Toolbox and
  Security & trust pages.** Both live behind Tools, which is fine once you know
  that and invisible if you do not. The tour gains a last step that says what
  each page is for, reports the live install count and posture summary beside
  it, and whose button opens the Tools panel — the tour steps aside while the
  panel is up and returns to the same step when it closes. The controller legend
  (shown once when the tour finishes, and any time from Quick Settings → **?**)
  gains a **Find it in Tools** section with the same two pages, because neither
  has a key of its own to list as a binding ([player/onboarding.go](player/onboarding.go),
  [player/controlslegend.go](player/controlslegend.go),
  [docs/TOUR.md](docs/TOUR.md), `player/onboarding_test.go`,
  `player/controlslegend_test.go`).
- **Three network scanners in the Toolbox, behind a one-time prompt.**
  `naabu` (open TCP ports), `httpx` (which hosts answer HTTP, with status,
  title and server) and `dnsx` (DNS records) install and run like the rest of
  the catalog — official releases, allowlisted repo, typed arguments, no shell —
  and are the only entries whose job is to send traffic to other machines. The
  first run of any of them does not start: a toast says to scan only networks
  you own or may test, and a second press records `networkScanAuthorized` in
  `jukauser.json` and runs it. After that every scanning tool runs immediately,
  and deleting the key brings the prompt back. Their rows always carry the
  warning, and their default run is `-version` because `httpx` and `dnsx` read
  stdin when given no arguments. Installing is not gated — only running
  ([player/toolbox.go](player/toolbox.go),
  [player/powertools.go](player/powertools.go),
  [docs/TOOLBOX.md](docs/TOOLBOX.md#network-scanning),
  `player/toolbox_scan_gate_test.go`).
- **Saved browser cookies can expire on their own.** Keeping the session is the
  point of `data/browser-cookies.json` — the launcher restarts every time the
  handheld sleeps — but keeping it forever also keeps a consent gate you cleared
  once for a page you will never open again. Settings → General → **Saved
  cookies** now cycles **Keep** (the default), 7, 14, 30 and 90 days, and a
  cookie first stored longer ago than the window is not restored. Choosing a
  window prunes what it already makes stale straight away, so the count on the
  Browser's Forget chip never promises more than the setting keeps, and the
  press reports what it removed. Age is measured from a per-cookie `firstSeen`,
  not from the last refresh, so a site that re-issues its token every visit does
  not look new forever; a file written before that field existed keeps
  everything on the first launch and starts its clock there. The row lives on
  the Settings page in memory only, like the screensaver row beside it, so no
  shipped config is rewritten ([player/browsercookies_retention.go](player/browsercookies_retention.go),
  [player/widgets.go](player/widgets.go),
  [docs/BROWSER.md](docs/BROWSER.md#dropping-them-after-a-while),
  `player/browsercookies_retention_test.go`).
- The Settings row for the screensaver timeout now renders its value. Its label
  was `$screensaverTimeoutLabel`, but substitutions only read the custom
  variable block, so the page showed the placeholder itself; the derived label
  is now seeded there at injection and refreshed by every change
  ([player/widgets.go](player/widgets.go), [player/screensaver.go](player/screensaver.go)).
- **The Browser toolbar now says how many saved cookies are on the card.**
  The count only appeared in the toast after pressing **Forget**, so the one
  fact that answers "am I still signed in somewhere?" was hidden behind the
  action that discards it. The chip reads **Forget 3** while three cookies are
  saved (capped at **Forget 99+** so the label always fits its share of the
  row), falls back to plain **Forget** when the jar is empty, and is refreshed
  as the page is drawn — so a login that just set a cookie shows up without
  leaving the screen. Nothing new is read from the card: it is the same jar the
  browser already holds ([player/browser.go](player/browser.go),
  [player/textbrowser.go](player/textbrowser.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-them),
  `player/browserforgetchip_test.go`).
- **The Games Library can play a video snap for the focused game.** Box art
  tells you what a game is; a few seconds of motion tells you whether you want
  it. Drop clips in `data/game-snaps/<System>/<rom name>.mp4` (or `.webm`) —
  the same tree shape as `data/game-art/`, so an existing collection copies
  straight across — and the preview box plays one while the highlight rests on
  that game. Nothing is downloaded and no scraper account is involved. The
  delay (about a third of a second) is the feature: holding **Down** through a
  long list must not start a decoder per row. The decode is its own ffmpeg
  process at 320×180 and 10fps, muted and looped, so it never touches the video
  player's session, the queue or the now-playing dock; closing the panel,
  moving the focus or opening the player stops it. The clip replaces the cover
  in the same box and carries a **SNAP** chip so it is not mistaken for
  animated art, and the cover comes back when there is no frame yet or the clip
  belongs to another game. A clip that produces no frames is remembered for the
  session and never retried. `gameSnaps: false` turns it off and
  `gameSnapsDir` points at a collection stored elsewhere
  ([player/snappreview.go](player/snappreview.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#video-snaps),
  `player/snappreview_test.go`, `player/snappreview_render_test.go`).
- **The Browser keeps a session across a relaunch**, and there is one press to
  drop it. A page that handed out a cookie — a consent gate, an age gate, a
  redirect that only sticks once the session is sent back — used to start signed
  out every time the launcher restarted, which on this device is every sleep.
  The jar is now written to `data/browser-cookies.json` (`0600`, one
  `schemaVersion`, one `savedAt`): per cookie only the host, optional `Domain`,
  path, name, value, the `Secure`/`HttpOnly` flags and the expiry — no
  addresses, no titles, no visit times. Session cookies are saved on purpose,
  because that is the thing being fixed; expired cookies are never written and
  are dropped on load; a page's own sign-out removes it from the file; the list
  is capped at 256 records, sorted so the truncation is deterministic; and a
  corrupt file costs the session and nothing else. The new **Forget** chip in
  the Browser answers "how do I get rid of this": first press arms it and a
  toast names the count, a second press within five seconds empties the jar,
  deletes the file and reloads the page, and an empty jar deletes the file
  instead of leaving it. Clearing the page cache still leaves cookies alone
  ([player/browsercookies.go](player/browsercookies.go),
  `player/browsercookies_persist_test.go`).
- The **Wi-Fi meter in the status corner now tells the truth about a missing
  link**. Four hollow bars were the "no Wi-Fi" state, and hollow bars read as a
  *weak signal* — the opposite of what they meant. The meter has three states
  now: solid bars for a measured link quality, hollow bars outlined in the accent
  for a link whose strength the kernel does not report (connected, unknown), and
  hollow bars with a slash through them when there is no wireless link at all.
  The slash is warning-coloured when nothing else is keeping the device online
  and muted when something is, because the mark is about Wi-Fi while the `!
  Offline` text beside it is about the internet. The reader also stopped
  inventing a level: an interface that is up without a readable quality used to
  report two bars on every link nobody could measure, and now reports *unknown*
  ([player/statusbar_status.go](player/statusbar_status.go),
  `player/statusbar_status_test.go`).
- The Browser reads more of the CSS real sheets are written in, and draws the
  parts it read correctly. **Borders now belong to the edge the sheet named**:
  `border-right` used to set the *left* flag, so a sidebar divider appeared on
  the wrong side, and `border-left` painted a fixed grey 3px bar regardless of
  `border-left: 4px solid #c00`. Each edge now keeps its own colour (including
  the colour carried by a shorthand), its declared weight (`1px` stays a
  hairline, `4px` stays heavy, `border-width` and the per-edge widths count), and
  `border-right` has a bar of its own. An `<hr>` keeps its styling instead of
  always drawing the reader's grey rule. The **`font` shorthand** is read
  (`font: italic 700 18px/1.8 Georgia, serif` sets the size, weight, style and
  leading at once; a bare number is a weight, never a size), **vertical and
  logical spacing** reaches the layout (`padding-top`/`padding-bottom`,
  `margin-inline`, `padding-block`, `margin-inline-start`, ... — `padding-top:
  24px` used to do nothing at all), and **stylesheets are applied for the right
  medium**: a width or feature `@media` block still applies, a block that only
  targets `print` or `speech` no longer does (a print sheet hides navigation
  with `display: none`, so the page arrived with its header missing), and
  `@supports`/`@layer` blocks now count, because this reader implements the
  properties they gate ([player/webpage.go](player/webpage.go),
  `player/webpage_css_test.go`).
- The **Settings** menu is a launcher page, the same treatment the Tools menu
  got. It was the last screen still drawn as the fixed grid it was authored in —
  four wide buttons with no icons, no title and no grouping — next to a Home
  grid, an Apps grid and a Tools page that all read as one shell. It now opens
  with the shared page header (Settings glyph, title, subtitle) and groups its
  rows by what they change: APPEARANCE, SOUND, CONNECTIVITY, SYSTEM, each with
  its own glyph chip. Rather than a second copy of the layout, a
  `menuPageProfile` now describes a menu — its sections, how a row is sorted
  into one, the glyph each row and heading gets, the subtitle and the header's
  status read-out — so Tools and Settings are two profiles over one layout, and
  the next menu is a profile too. Two measuring fixes came with it: columns are
  capped at the widest section (a sparse page no longer spreads two tiles across
  four columns with half of every row empty), and the exact section and gap
  height is counted instead of approximated, which is what lets a short page be
  centred in its panel rather than heaped at the top while a tall one still
  cannot run over the footer
  ([player/menutiles.go](player/menutiles.go), `player/menutiles_test.go`,
  `player/launcher.go`).
- Two shared components, and the three screens that were drawing them by hand
  moved onto them. `UIPageHeader` is the page bar — optional glyph well, title,
  subtitle and a right-aligned status read-out, with a centred form for the
  About brand block — and `uiPageHeaderMetricsFor` is the one place that says
  how tall the band is, so a layout reserving room for the header and the draw
  filling it cannot disagree. The Tools overlay, the Tools page and About each
  had their own copy with their own offsets, which is why the two Tools titles
  differed by 4 px. `UICategoryTile` is the tile recipe (card, tinted icon well,
  two-line fitted label, status dot, hover and focus ring, press dip) and has a
  raised-surface variant for tiles standing on a panel; the Tools overlay and
  the Tools page both use it. The section heading the two Tools screens
  duplicated is now `UISectionLabel`
  ([player/components.go](player/components.go), `components_test.go`).
- CSS gradients in the text-browser are painted as real multi-stop ramps. Every
  gradient used to collapse to the average of its stops, which turned a header's
  light-to-dark sweep into one flat band and made a body gradient invisible.
  `linear-gradient()` is now parsed into positioned stops — `#1e3a8a 0%,
  #3b82f6 100%`, evenly spaced three-stop lists, `to top`/`to left` mirroring,
  and `deg`/`turn`/`grad` angles — and drawn band by band (one band per row on a
  small box, at most 96 on a tall one) for block backgrounds, absolutely
  positioned cards and the page's own paper. A radial or conic gradient is not a
  ramp and keeps the averaged fill, the average is still stored for text
  contrast and opacity, and inline run backgrounds stay flat
  ([player/webpage_css.go](player/webpage_css.go), `player/webpage.go`).
- A **SCROLL** / **LINKS** chip in the web browser's toolbar. Switching the D-pad
  between scrolling and walking links only told you about it in a toast, so once
  that faded the page gave no sign of which mode the D-pad was in. The chip reads
  the live mode, turns accent-coloured while links are the target, and is a tap
  target of its own for touch. On a narrow panel it is the last control to be
  dropped, after Search and the filter counter, because it is the only one that
  explains the D-pad
  ([player/textbrowser.go](player/textbrowser.go), `player/browser_test.go`).
- Per-button remapping in the Accessibility panel. The physical → logical keymap
  has been applied before dispatch for a while, but only the layout preset and
  the Select row could reach it: remapping L1, R1 or a face button meant editing
  `a11y_keymap` by hand on the card. A, B, X, Y, L1 and R1 now each have a row
  that shows the action the button currently sends — preset included — and walks
  the list left and right, ending at **Disabled**, which swallows the press. The
  last thing touched wins: changing a face button folds the Nintendo preset into
  an explicit entry so one rule describes it, and applying a preset clears the
  individual rows. Select and Start keep their single paired row on purpose, so
  they cannot be stranded
  ([player/a11y.go](player/a11y.go), `player/a11y_keymap_test.go`).
- Hold-to-repeat on the D-pad outside the on-screen keyboard. The keyboard had
  polled the pad for a while, but every other screen needed one press per row,
  so walking a long library was one tap per game. `navrepeat.go` now watches the
  held direction and, after 320 ms, re-queues the same D-pad press through SDL at
  a 70 ms cadence — the existing router still decides what a repeat means, so
  overlays, modals, the video player and the link picker all behave as if a
  person tapped repeatedly. The keyboard keeps its own highlight repeat, the
  analog stick's repeat is untouched, and the accessibility slow-repeat setting
  doubles both intervals (`navrepeat_test.go`).
- More of the CSS a real page ships, in the text browser: `line-height`
  (unitless, percentage and length), `border-radius`, `opacity`, gradients
  (`background-image`/`background`), `list-style-type`
  (`decimal`, `lower/upper-alpha`, `lower/upper-roman`, `disc`, `circle`,
  `square`, `none`, including on the list element so a nav menu's bullets
  really disappear), and the `margin`/`padding` shorthands now fill all four
  sides instead of only the vertical ones
  ([player/webpage_css.go](player/webpage_css.go)). Properties the row grid
  cannot honour are approximated rather than dropped: a gradient is drawn as
  the average of its stops, `opacity` is blended into the painted colours,
  `border-radius` is clamped to half the box, and `line-height` becomes extra
  row slots. See [docs/BROWSER.md](docs/BROWSER.md#what-it-can-do).
- Cookies in the text browser. Every browser request — the page, its
  stylesheets, images and scripts, and each redirect hop inside a load — now
  shares one in-memory cookie jar, so a consent answer, an age gate or a
  redirect that only sticks with a session cookie actually works
  ([player/browsercookies.go](player/browsercookies.go)). The jar is scoped by
  host, path, `Secure` and expiry like a browser's, is never offered to another
  site, and is never written to the card: the reader cannot submit a form, so a
  cookie file would outlive its usefulness and become a privacy surface. Feeds,
  update checks and the podcast fetcher keep their own clients, so a page's
  cookie cannot ride along on an unrelated request. See
  [docs/BROWSER.md](docs/BROWSER.md#cookies).
- A **Recordings** screen for the captures an emulator writes. Before a
  RetroArch launch the hub now points `recording_output_directory` at a folder
  it knows (`Recordings` on the card, or `recordingsDir` in the config) through
  the same crash-safe writer the save-state switches use, so the emulator's own
  record toggle and the hub agree on one folder. A folder the user set in
  `retroarch.cfg` is honoured and never rewritten, `recordings: false` keeps the
  hub out of the file, and the hotkey itself stays RetroArch's — guessing button
  codes would break controls on a device whose mapping we have not measured.
  The screen opens from **Apps → Recordings** (the Media button row is full —
  the IPStream chip is injected into it at runtime), lists the clips newest
  first, and plays the highlighted one with A in the existing video player
  ([player/recordings.go](player/recordings.go)).
- Conditional requests for the shared fetchers: `httpcache.go` keeps the last
  successful body next to its ETag / Last-Modified (bounded at 64 URLs and
  1 MiB per body, process-lifetime by design) and the release-manifest fetch
  plus the podcast/ticker text fetch now send those validators, answering an
  unchanged resource from the cache on a 304 instead of downloading it again.
  A server that answers 304 for a URL this process holds no copy of is reported
  as an error rather than being mistaken for an empty document, a newer body
  always replaces the cached one, and `Cache-Control: no-store` is honoured
  (`httpcache_test.go`).
- One-file support export: **Patch → Export diagnostics** now writes the
  redacted report *and* the last few hundred log lines to
  `reports/diagnostics-<timestamp>.txt`, which is the file to attach to a bug
  report ([player/diagnostics_bundle.go](player/diagnostics_bundle.go)).
  Credentials (tokens, API keys, passwords, bearer headers, URL userinfo) and
  home-directory account names are scrubbed from the log first, and the bundle
  says what was removed and that the user should read it before sharing. The QR
  and prefilled-issue flows keep the short report, which has to fit in a URL.
- Architecture decision records: [docs/adr/](docs/adr/) now holds the decisions
  other code has to live with, with the process itself
  ([0000](docs/adr/0000-record-architecture-decisions.md)), the
  physical → logical keymap ([0001](docs/adr/0001-physical-logical-keymap.md))
  and renderer-owned texture caches
  ([0002](docs/adr/0002-renderer-owned-caches.md)); CONTRIBUTING says when one
  is expected.
- A documentation link check in the test suite
  (`player/docs_links_test.go`): every relative
  Markdown link in the checkout must resolve, so a renamed page fails the build
  instead of shipping a dead pointer. It found four broken links on the way in,
  including links to an Android-on-A133P study that was later removed; current
  engine status is documented in [docs/ANDROID.md](docs/ANDROID.md).
- Component versions in Patch (and in the diagnostics report): a new row lists
  the running app, launcher script, design config, helper tools and installed
  packages with their SHA-256 digests, so a bug report names exact builds.
  "Copy report" puts the full 64-character hashes on the clipboard; the
  exported diagnostics carries the same section
  ([player/componentreport.go](player/componentreport.go)).
- A real button-remapping layer. The accessibility panel still has the A/B and
  X/Y swaps, plus a "Button layout" preset (Nintendo order sets both) and a
  "Select button" row that can move Select to Start or disable Select while
  Start keeps opening the menu. Under the rows is a general physical → logical
  keymap stored as `a11y_keymap` (`select:start,start:select`, `select:none`),
  applied once before dispatch, so the next remappable button is data rather
  than a scene change ([player/a11y.go](player/a11y.go)).
- LED policies: the LED sync is no longer audio-only. Quick Settings' "LED
  Mode" cycles Audio / Battery / Charging / Temperature / Custom; battery runs
  red → green across 10–80%, charging breathes and goes dark unplugged,
  temperature runs green → red across 45–80 °C, and Custom uses
  `led_color` (`#rrggbb`) with an optional `led_effect` of `pulse`
  ([player/ledsync.go](player/ledsync.go)).
- LAN Play has a switch. Quick Settings' "LAN Play" row writes the previously
  dead `netPlayEnabled` flag, and the Game Library explains where to turn it on
  instead of opening a disabled panel. A config from a build without the switch
  keeps the shipped default (on) because the user copy is only written when the
  row is used ([player/netplay_ui.go](player/netplay_ui.go)).

- Controller legend ([player/controlslegend.go](player/controlslegend.go)): a
  first-run card that appears once the setup tour is completed, and stays
  reachable from a new "Controller Legend" row in Quick Settings. Its rows are
  generated live from the `footerHints` registry — the same audited bindings
  the footers draw — for the scene it opened over, plus the three global pad
  facts (Start menu, the Select + Start quit chord, F1). B closes it; F1 hands
  over to the full keyboard shortcuts reference.

- AndroidPak loader wiring for a real NDK APK's first run. JukaHub now hands
  the AndroidRT loader form what `launch.sh` used to arrange only for
  itself: `ANDROIDPAK_SHIMS` (the runtime's `shims/` directory, preloaded
  `RTLD_GLOBAL` by the runner) and a staged-first `LD_LIBRARY_PATH`
  (`<staged>/lib/<abi>` then `shims/`, the device's own GL userspace still
  last so the PowerVR EGL wins over a stray copy in the APK). In
  `player/androidrt/build-tsp.sh` every shim is now linked with
  `-Wl,-soname,<name>` so glibc satisfies a game's `DT_NEEDED`
  (`liblog.so`, `libandroid.so`, `libOpenSLES.so`) out of the preloaded
  shims instead of hunting the search path for a different bionic copy.
  The first run's failure is captured: when an AndroidPak profile exits
  non-zero, the launch log (`data/game-profiles/logs/<id>.log`) is scanned
  for the runner's "undefined symbol" line and the symbol is named in a
  toast and the JukaHub log — the next-shim answer without opening the log
  by hand. Go tests cover the env handoff and the capture.
- OpenSL ES shim audio path for AndroidRT: `player/androidrt/src/openslessim.c`
  now plays — real `Enqueue`/`SetPlayState` streaming of S16 1/2-channel
  BufferQueue sources through ALSA `default` (`snd_pcm_*` resolved from
  `libasound.so.2` via dlopen only; no header and no link-time ALSA on the
  device build). Per-buffer completion callbacks fire from the shim's worker
  thread like native Android delivers them, `GetPosition` tracks frames
  consumed, `SetPlayState(STOPPED)` drops queued content without late
  callbacks, and `Destroy` from inside a buffer-queue callback is safe (the
  worker frees its own object). When libasound or the playback device is
  missing, playback degrades to a wall-clock silent pacer at the game's
  sample rate — callback order and timing stay truthful, launch never fails
  for lack of audio. The ABI is verbatim 1.0.1: real `SL_RESULT_*` values,
  wilhelm interface UUIDs, per-interface vtable member order, and the three
  create functions return the `SLObjectItf` handle the header promises. The
  smoke harness gains a host audio probe asserting the whole chain, including
  the callback-destroy case.
- Input shim for AndroidRT: NativeActivity games get a real `AInputQueue`
  instead of the silent no-op. `player/androidrt/src/inputshim.c` implements
  the NDK queue contract — `AInputQueue_getEvent`/`hasEvents`,
  pre-dispatch no-swallow, `finishEvent`, joystick axis coalescing while
  pending, an event the app holds is never overwritten, and
  `ALooper_pollAll` reporting `LOOPER_ID_INPUT` with the queue as user data
  while events are pending, so `android_native_app_glue`'s input loop runs
  unchanged. On the device `player/androidrt/src/evdev.c` reads
  `/dev/input/event*` and maps the Linux gamepad spec (aarch64 layout
  pinned header-free) onto Android keycodes/axes; once per launch it prints
  which nodes opened, and an unreadable node set names the missing input
  group instead of failing silently. The runner's lifecycle now dispatches
  through the game's own callbacks table (the NDK contract `onCreate`
  implies) with the runner stubs as fallback, and the smoke harness adds an
  input probe asserting the whole semantics chain host-side. Disabled via
  `ANDROIDPAK_INPUT=off`. Run smoke green end-to-end; on-hardware
  verification of the TSP's evdev nodes is the open question.- Android layer, launch-focused: the AndroidPak profile runtime now runs a
  memory pre-flight before staging an APK launch (`androidPakPreflight` in
  player/androidpak.go) — staged bytes + 192 MiB runtime budget + 96 MiB hub
  reserve versus `MemAvailable`, with a refusal that names the numbers on the
  1 GB device and a `JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB` opt-out. The AndroidRT
  C side gains the surface-stack probe (`player/androidrt/src/surface.c`):
  header-free dlsym of the glibc GE8300 GPU userspace through its whole
  GBM+EGL chain, printing exactly which layer holds per launch with
  `ANDROIDPAK_SURFACE=1`; smoke builds a standalone probe binary host-side.
  Run smoke and AndroidPak suites verified; the probe's on-device verdict is
  still to be taken (no GPU stack on this desk).

- Android engine work: AndroidPak launches gained a memory pre-flight, and
  AndroidRT gained a host-testable probe for the GBM+EGL surface stack
  (`player/androidpak.go`, `player/androidrt/src/surface.c`). Current scope and
  hardware-validation status are in [docs/ANDROID.md](docs/ANDROID.md) and
  [player/androidrt/README.md](player/androidrt/README.md).

- Hardware decode investigation doc: docs/VIDEO_VPU_OFFLOAD.md records the desk
  pass over offloading the video player's decoder to the A133P's Cedar VPU —
  silicon capabilities per codec, why the current ffmpeg rawvideo pipe bypasses
  them, why mainline Cedrus is not a stock-firmware option, and the recommended
  CedarX→stdout bridge design with its five open on-device probe questions.

- Icon atlas for the shared rendered glyph set: every home card's icon tile,
  tool/emulator tile and launcher empty-state glyph used to be built from
  filled primitives per frame — on the GLES2 renderer one cached-shape blit
  (texture bind + draw call) each, hundreds of small binds per frame. Each
  (name, focused) glyph is now drawn once at the canonical 32px into one
  1024×64 strip and every later occurrence is a single Copy of its sub-rect,
  with the same primitives painting the strip, off-32 callers still rendered
  natively, unknown icon names still drawing the fallback ring, and automatic
  direct-paint fallback on renderers without render targets. Cleared wherever
  shape textures are cleared (theme change, memory pressure, a11y font
  reload, shutdown) and dropped per renderer like every other texture cache.

- "While you were playing": background work that finishes while a game, port
  or Wine app owns the screen — saved-track downloads, helper-tool and
  PortMaster installs, achievements — is now collected into one summary toast
  shown on the first frame after the game hands the screen back, instead of
  each finisher's own toast piling up unseen and only the last one flashing
  by when you return.

- Config-driven home cards: every menu card on the home screen (Continue,
  Media, Files, Packages, Apps, Favorites, Browser, Chat, Tools, Emulator,
  Settings, Exit) now takes its position, size, title, subtitle and icon from
  the `Main` scene's `style: "tile"` buttons in jukaconfig.json, instead of
  being hardcoded while the config's stale tile grid was ignored. Both the
  desktop and handheld configs ship the real card set and geometry, an edit
  or config hot-reload reaches the screen on the next frame, and geometry
  outside the canvas falls back to the built-in card. The header cluster
  (network, weather, clock, update, notifications) stays system chrome.

- Embedded YouTube videos in the reader show the video's real thumbnail on
  its play card: the embed's video id maps to a `mqdefault.jpg` URL on the
  image CDN, which the same pass that fetches `<img>` bytes picks up under
  the same size/count budget. The artwork is center-cropped to the card and
  the play glyph and title draw on top; Vimeo, direct files and failed
  fetches keep the dark chrome card as before.

- Per-video playback quality: the video settings sheet's new **Quality** row
  (cycling Auto → 360p → 480p → 720p → 1080p) pins a yt-dlp format ceiling to
  the video on screen and restarts it from the current position, so one long
  video can stream at 480p without touching the global *Playback resolution*
  setting. The pin follows the video's own stream cache entry, applies to the
  temp-file download fallback too, and is forgotten when a different video
  starts.

- **AndroidPak** — the Tier-1 path for running Android games *without* the
  Android system image, aimed at NDK-only games on the 1 GB Smart Pro. A new
  `androidpak` game profile runtime triages an APK read-only (shipped ABIs,
  `arm64-v8a` preferred to match the A133P, the native entry `.so` declared or
  auto-detected, pure-Java packages refused with a reason), stages the native
  subset (`lib/<abi>`, `assets/`, `res/`, manifest, `resources.arsc` — never
  `classes.dex` or signatures) into a bounded tmpfs staging dir so gameplay
  never re-reads the SD card, and launches the user-installed bionic
  translation runtime (`loader` or its `launch.sh`) with the staged tree and
  ABI in `ANDROIDPAK_*` environment variables. Schedule it with
  `JUKAHUB_ANDROIDRT`; staging lands in `$TMPDIR/jukahub-android` unless
  `JUKAHUB_ANDROID_STAGE` says otherwise. Profiles without the runtime list as
  Unavailable with the exact reason, and the staged tree is deleted after the
  game exits. See [docs/ANDROID.md](docs/ANDROID.md). The runtime skeleton
  itself ships as source: `player/androidrt/` cross-builds `loader` plus
  `shims/{liblog,libandroid,libOpenSLES}.so` for the A133P, and a host-only
  smoke check (`tests/smoke/run-smoke.sh`) drives loader → onCreate → window
  → frames → clean teardown with no device and no Android system image.
- `Makefile` as the single entry point for the developer loop: `make dev`,
  `build`, `run`, `test`, `test-web`, `e2e`, `vet`, `fmt`, `lint`, `bench`,
  `web`, `release`, `clean`, and `make check` for everything a pull request
  must pass locally.
- Issue forms for bug reports and feature requests, with the version, device and
  redacted diagnostics report asked for in the form, plus contact links to the
  documentation and the security policy.
- Pull request template that records the definition of done and separates a
  quick win from a change that needs a migration.
- `.editorconfig` and an optional `.pre-commit-config.yaml` (gofmt, `go vet`,
  `node --check` on the builder scripts) so formatting noise stops reaching
  review.
- The game power profile now lifts the CPU frequency ceiling, not just the
  governor: the TrimUI Smart Pro advertises a 2 GHz top OPP while some firmware
  boots capped at 1.8 GHz, and `performance` can only reach what
  `scaling_max_freq` allows. A launch now raises the ceiling to the highest
  frequency the policy offers, logs the clock it applied, and restores the
  firmware value with the battery profile on return. `powerProfileMaxFreq`
  caps it (e.g. `1800` for a cooler 1.8 GHz). See
  [docs/DEVICE_TUNING.md](docs/DEVICE_TUNING.md).
- The browser's toolbar reports how many ad and tracker requests a page load
  skipped, and a **Filter** chip turns filtering off for one site when a page
  breaks without a request the filter drops. The exception is per host and
  saved in `data/browser-block.json`, so it survives a restart; pressing
  **Filter** again restores filtering. See
  [docs/BROWSER.md](docs/BROWSER.md#ads-and-trackers).
- **Audio**, **Video** and **Chat** buttons on every video result. The pills sit
  at the right edge of the row: **Audio** queues the track in the Downloads
  panel as an m4a, **Video** queues the best picture and sound merged into an
  MP4, and **Chat** posts the link as a chat message. The two downloads are
  separate because a track is small enough to keep a library on the card and a
  video is not. Left/Right on a focused row cycles row → Audio → Video → Chat →
  row so **A** presses the highlighted one, and a tap presses the pill it lands
  on. Video rows open in the embedded player from the Downloads panel. See
  [docs/CONTROLS.md](docs/CONTROLS.md#media).
- Per-game launch options in the Games Library. **Start** (or **O**) opens a
  sheet for the highlighted game with a CPU ceiling (Auto / 2.0 / 1.8 / 1.6 /
  1.4 / 1.2 GHz) and extra launcher arguments, stored in
  `data/game-options.json` by ROM path. The cap applies to that game's launch
  only and the firmware ceiling comes back when it exits; a capped game shows
  its clock in the list. See
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#per-game-launch-options).
- Backing out of a video no longer stops it. Back steps down one size instead:
  full screen returns to the scene's player, and the in-scene player shrinks
  into a floating window in the bottom-right corner while the previous screen
  opens. The session keeps its position and audio; a tap on the window returns
  to the player and its close button (or the Stop control, or `q`) still ends
  it. Only the dock/screensaver cover the window. See
  [docs/CONTROLS.md](docs/CONTROLS.md).
- The now-playing title in the top bar is a button: a click or tap, or **A**
  with the controller on the chip, opens what it names — the audio player for a
  track, the video scene (and its floating window, if the video was left in
  one) for a video. The chip joins the header focus order between the clock and
  the bell, so **Right** from the clock reaches it on Home and **Up** reaches it
  from the content on every other screen. Nothing playing means no chip and no
  focus stop. The chip also shows its state at a glance: the headphone glyph
  becomes two pause bars while paused, and a hairline under the title fills in
  with the played part once the track's or video's length is known. With nothing
  playing the same chip names the output instead — the connected Bluetooth
  device's own name (only devices that accept audio) or **Headphones** when the
  jack has a cable in — refreshed in the background every few seconds. That idle
  name is a reading, so it stays out of the focus order and a tap on it does
  nothing. See [docs/CONTROLS.md](docs/CONTROLS.md).
- A random game picker in the Games Library. **R** (or a click of the right
  stick, **R3**) asks whether to roll from the console on screen or from every
  console, jumps the library to the game it picks — console, row and scroll
  position — so **A** plays it and another **R** rolls again. The roll never
  repeats the game already highlighted while the scope has another candidate,
  and it follows an active search so the jump cannot land on a filtered row.
  See [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#random-pick).
- Browser pages in the reader no longer lose their videos: a resolvable
  `<video>` element or video-bearing embed (YouTube, Vimeo, Dailymotion, Twitch,
or a direct media file) reserves a 16:9 play card on the image row grid, with a
play glyph and the video's title, and joins the page's links so the D-pad
reaches it like an anchor. Activating it never navigates — the URL goes to
`playSmartURL`, which streams media files directly and hands everything else to
yt-dlp in the embedded player, the same path a YouTube search result uses. A
structurally non-video iframe and an element with no resolvable source still
skipped as before. See [docs/BROWSER.md](docs/BROWSER.md#videos-and-yt-dlp-playback).
- Scene-scoped background work: leaving a screen now cancels what it started.
  yt-dlp media searches — the results screen, follow-up page fetches, trending
  and the Shorts feed — were still plain goroutines, so leaving while a search
  loaded left the extractor running behind the home screen and let its results
  land on a screen the user had already left. They run in `runSceneTask` with
  the scene's context now (`executeYouTubeSearchContext`), so the cancellation
  kills the process and a cancelled run publishes nothing, and a dead scope
  refuses to start a run at all.
  The podcast feeds, package registry, weather screen, crypto/stock board, speed
  test and both benchmarks take the screen's context, so a 100 MB download or a
  50 MB benchmark stops (and removes its temp file) instead of running behind
  the next screen, and a cancelled fetch publishes nothing. The board's
  independent endpoints also share one round trip now instead of three serial
  ones. Process-wide chrome (network monitor, weather ticker, audio output)
  keeps running. See
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#scene-scoped-background-work).
- `Ctrl+L` opens the log viewer from any scene. It stands aside in the terminal,
  where the key keeps clearing the output, and it is only consumed when the
  config actually has the LogExporter scene. See
  [docs/CONTROLS.md](docs/CONTROLS.md#desktop-keyboard).
- Log exports no longer destroy the previous file: an export moves `logs.txt` to
  `logs.txt.1` and writes the new one atomically, so exporting after a crash
  keeps the log from before it. The exported file is capped at 2 MiB and the
  in-memory ring at 512 KiB as well as 2000 lines, so one huge line cannot grow
  either without bound.
- 720p frame budgets for the home screen: `go test -run '^$' -bench
  BenchmarkHome` reports a warm frame, a cold-text frame (theme switch) and one
  D-pad step, so layout and text changes can be compared against a number.
- Shorts feed cells and search-result cards in the Media browser now fall back
  to real artwork when yt-dlp ships none: a YouTube entry without a thumbnail
  field (usual for flat-playlist Shorts rows) maps its video URL onto the image
  CDN — the same yt-dlp-free derivation the reader's play cards use — and
  paints the actual frame through the shared disk-backed cache and decode
  budget. Metadata thumbnails keep priority, the derived URL only trails as a
  fallback, and non-YouTube entries keep the placeholder. The Shorts grid also
  prefetches artwork like the results list so entering the feed does not paint
  grey tiles.

### Added

- **Tools → Toolbox: install popular command-line tools on demand, and run them
  from the app.** `rclone` (cloud/SSH sync), `jq` (JSON), `ripgrep` and `fd`
  (search), `pandoc` (document conversion) and `age` (encryption) are fetched
  from each project's own GitHub releases into `required/`, then run with
  visible output: **A** installs the first time and runs afterwards, **X** opens
  the on-screen keyboard for arguments (quotes group words; there is no shell,
  so `;`, `|`, `*` and `$()` are literal), **Y** copies the installed path.
  Installed tools are also on `PATH` for Power tools → Script snippets, so any
  of them can become a button. A download can only come from a repo listed in
  `allowedToolRepos`, assets are matched on stable name parts (never a version)
  and checksum/signature side-cars are skipped, so a token match cannot fetch a
  `.sha256` by mistake
  ([player/toolbox.go](player/toolbox.go),
  [player/tools.go](player/tools.go)).
- **Tools → Security & trust** reports the device's posture without a PC:
  plaintext secrets, the storage key in use, package count, listening FTP/SFTP
  servers, the browser session on the card and telemetry state — each row naming
  the mechanism behind it, with [SECURITY.md](SECURITY.md) as the long form. The
  Tools row carries an amber dot while something is worth reviewing
  ([player/toolbox.go](player/toolbox.go)).

### Fixed

- **The Wi-Fi picker reads the right adapter when Windows reports several.**
  `netsh wlan show interfaces` lists the physical NIC alongside the software
  adapters that share the radio — the Wi-Fi Direct one arrives as "Local Area
  Connection* 10" — and their order is not stable across a resume or a driver
  reload, so taking the first block could report "no network" on a machine that
  was already connected, and point the status meter at an adapter that never
  associates. Selection is now explicit (`pickNetshIface`): the linked adapter
  wins, a real NIC beats a virtual one, the stronger signal breaks a tie, and the
  first block is only the last resort. The status and signal reads re-read the
  report for a fresh association and match it back by adapter name, so the panel
  cannot start reading a different adapter mid-session when the ranking between
  two connected adapters flips. ([player/wifi_windows.go](player/wifi_windows.go),
  [docs/WIFI.md](docs/WIFI.md); tests in
  `player/wifi_windows_test.go`).

- **The Package Manager shows which package is selected.** The cursor was a
  50-alpha accent wash plus a 3px spine, and the list's pane border was drawn in
  that same accent — so on a dim panel the screen read as "one card is slightly
  lighter" with an accent frame around everything. The focused row now carries
  the same focus ring the rest of the app's overlay panels draw, in the colour
  that survives a theme whose accent sits close to the row surface, with its
  version/author line stepped up a tone; the two panes take the neutral border
  every other panel uses, so the ring is the only accent outline on the screen.
  Rows are also clipped to the list column, and the shared row renderer
  ([player/components.go](player/components.go)) gained the ring for every list
  that uses it ([player/packages.go](player/packages.go),
  `player/packages_test.go`).
- **The Wi-Fi meter shows a real level on images without `/proc/net/wireless`.**
  A stock kernel built without wireless extensions — or a driver that lists an
  interface with quality `0` while associated — left the corner meter stuck on
  *unknown* for the whole session. It now falls back to the supplicant's own
  `wpa_cli signal_poll` RSSI and maps it onto the same four bars
  ([player/wifi.go](player/wifi.go),
  [player/statusbar_status.go](player/statusbar_status.go)). The poll runs off
  the render thread and caches its answer for the same four seconds as the
  kernel read, so a hung supplicant can never freeze the UI; a non-negative
  `RSSI` (the `0` a disconnected supplicant prints) is not treated as a
  measurement, and a failed poll clears the cached level instead of leaving a
  stale one on screen. **Tools → Wi-Fi signal** reports through the same chain
  and names the source it used.
- **The plaintext-secret warning never fired.** `isPlaceholderSecret` kept an
  empty string in its placeholder list, and `strings.Contains(s, "")` is true
  for every string — so every API key looked like a placeholder and
  `ConfigValidator.SecurityCheck` reported no plaintext secret at all. The
  posture page surfaced it; the empty value is now handled before the substring
  loop, and `TestPlaceholderSecretKeepsTheWarningAlive` pins both directions.
- **The handheld build no longer loses apps to a stale scene list.**
  `trimui/make-config.ps1` carried a hand-maintained whitelist of "scenes that
  stay on the handheld build". It had gone bad in both directions: it named
  `Emulator`, which no longer exists, so the script threw before writing
  anything; and it omitted `JukaLand`, `LiveTV`, `Podcasts`, `Tickers`,
  `Weather`, `UnitConverter`, `CanvasSandbox`, `IPStream` and `MusicPlayer`. The
  committed device config was also a hand edit, not the script's output: its
  Main carried the old six tiles *and* the eight new ones stacked on each other,
  and it kept a Settings row the script was supposed to remove. Those nine
  scenes are rows in the Apps launcher (`builtinApplets`), and
  `builtinAppEntries` hides a row whose scene the config does not ship — so the
  handheld silently lost nine working apps with no error anywhere. The script
  now keeps every scene verbatim and applies only the device defaults; trimming
  is an explicit `-Drop` list that refuses to remove a scene a kept scene still
  opens, the whole run fails if any `change_scene:` target is missing, and
  `TestDeviceConfigShipsEveryBuiltinApplet` guards the committed file
  ([player/trimui/make-config.ps1](player/trimui/make-config.ps1),
  [player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json)).
- Config writes are crash-safe now. `AtomicWrite` previously renamed the live
  file to `.bak` *before* writing the replacement and never fsynced, so a power
  cut (the normal way a handheld stops) between those steps could leave the
  config missing or empty. It now copies the previous content to `.bak`, writes
  the temp file with an fsync, renames it over the live path and flushes the
  directory ([player/config_health.go](player/config_health.go)). A damaged
  `jukauser.json` is also recovered from `.bak` and written back, with a one-time
  toast explaining what happened, instead of silently starting from defaults and
  dropping the theme, keymap and every other preference
  ([player/extras.go](player/extras.go)).
- A LAN Play launch that cannot start RetroArch now keeps the underlying cause,
  so the failure dialog suggests reinstalling the launcher instead of falling
  back to the generic hint. Errors are wrapped with `%w` throughout the player
  (netplay, tools, crypto, system control, the Windows runtime installer and
  the patch resolver), and
  `player/errorwrap_audit_test.go` fails on a
  new `%v` wrap instead of relying on review.
- The intermittent `SDL_RenderReadPixels` crash in the test suite is fixed at
  its cause. The static-layer caches (on-screen keyboard, chat, video overlay)
  kept textures after their renderer was destroyed; a later probe then reused
  or freed a dangling pointer and corrupted SDL's allocator. The caches now
  record their owning renderer, drop a foreign texture without touching it, and
  the test renderer releases them before destroying its renderer
  ([player/vkeyboard.go](player/vkeyboard.go),
  `player/renderer_cache_owner_test.go`).

- One touch tap on a video card in the browser resolved the video twice.
  SDL synthesizes its own mouse press at finger-down, so a page's play card
  was hit by that ghost click plus the app's replayed tap at finger-up — two
  yt-dlp resolves for one tap (and a virtual-keyboard touch inserted a
  character twice). SDL's `SDL_TOUCH_MOUSE_EVENTS` mapping is now disabled;
  the app's own tap-to-click replay (`pushTapClick`) remains the single
  touch-to-mouse path.
- The `player-race` CI job is green again. The browser's fetch seam was a bare
  package variable, so a test that swapped it to a stub while a page load was
  still on the wire raced with the load goroutine reading it — reported as a
  data race in `TestBrowserToggleAdFilterIsPerSite`. The seam is now read once
  per load under a lock (`currentBrowserPageFetcher`), tests swap and restore it
  through `setBrowserPageFetcherForTest`, and a browser reset joins the loads in
  flight, so a late fetch cannot publish into the next test either.
- A CPU cap below the ceiling the firmware booted with was ignored:
  `raiseFrequencyCeiling` only ever wrote a higher `scaling_max_freq`, so a
  device already running at 2 GHz kept 2 GHz despite `powerProfileMaxFreq:
  1800` (and a per-game cap would have had no effect at all). The launch
  profile now writes the requested ceiling in both directions and restores the
  firmware value on exit.
- The Wi-Fi picker is usable by touch from the status bar: the signal meter's
  tap target now spans the top bar and is at least 44px wide (it was the ~34px
  icon, a miss for a fingertip), and the panel has tappable **Rescan** /
  **Reconnect** / **Disconnect** / **Forget** / **Close** pills instead of only
  the pad's footer hints. Tapping the row already marked **CONNECTED** now
  reconnects it (drop the association, re-join with the saved key) rather than
  answering "Already connected" and nothing else. See [docs/WIFI.md](docs/WIFI.md).
- The app could appear locked after the TrimUI's power button: the stop signal
  killed the process before any cleanup, a running game kept the framebuffer and
  the stock launcher stayed dead, so the last frame froze with no input able to
  leave it. Stop signals now take the normal exit path, a running game (and any
  music/video decoder) is stopped before the display is handed back, and a
  6-second watchdog writes the goroutine stacks to `errors.txt` and exits if a
  cleanup step blocks. `launch.sh` catches the same signal and still restores
  the launcher. See [docs/RECOVERY.md](docs/RECOVERY.md).
- Launching a library game crashed on Linux when no config was loaded: the
  time-lapse probe reached `getToolPath` with a nil config, which dereferenced
  it and took the whole test binary down (`TestLaunchLibraryGamePassesROMAndRecords`).
  Tool resolution now falls through to the platform directory and `PATH`
  instead, and a test pins that contract.
- `beginGamePowerProfile` no longer logs "no cpufreq governors on this device"
  and shows a "Performance mode unavailable" toast on machines that have no
  cpufreq at all (desktops, CI runners). A handheld whose governor files exist
  but refuse the write still gets the one actionable warning.
- The browser address-bar test queues SDL events through union-sized wrappers,
  so `go test -race` no longer dies in checkptr with "converted pointer
  straddles multiple allocations".
- Two staticcheck findings in tests: a tautological tint comparison (`SA4000`)
  and a parsed base URL that was immediately overwritten (`SA4006`).

### Changed

- The classic Tools menu (the config's `Misc` scene) is a launcher page instead
  of a wall of wide buttons: a titled header with the Wi-Fi address, section
  headings (SYSTEM, DIAGNOSTICS, UPDATES & TRANSFER), and icon tiles sized to
  the content panel. The buttons are grouped by what they do, so a custom menu
  still lands in a sensible section, and each tile gets its own glyph
  (terminal, clock, gauge, drive, log, bin, update, transfer). The element
  order and triggers are untouched, so focus memory, D-pad navigation, hover
  and clicks all follow the tiles
  ([player/menutiles.go](player/menutiles.go)). The layout is generated in
  memory at scene entry and detached again for a config save, so
  `jukaconfig.json` keeps the geometry its author placed; a panel too short for
  the page (for example 960×544) keeps the authored buttons rather than drawing
  tiles over the footer.
- The disk pie chart only refreshes while its screen is up. The poller used to
  statfs the card every five seconds for the whole session, keeping the CPU
  awake for a chart nobody was looking at; it now skips while the active scene
  has no `piechart` element and the scene takes a fresh reading on entry
  (`hardware.go`, `hardware_poll_test.go`).
- Element `width`/`height` values with surrounding whitespace are accepted, so a
  padded number from the XML dialect behaves like the same number in JSON. A
  `px` suffix, a fraction and a negative size still fall back to the element's
  default, and [docs/CONFIGURATION.md](docs/CONFIGURATION.md#elements) says so
  instead of leaving it to be discovered ([player/core.go](player/core.go)).
- The shell now prefers the device's GLES2 GPU renderer on handheld targets
  (the A133P's PowerVR GE8300 where the vendor stack provides it) and logs the
  active video driver, renderer name, acceleration mode and maximum texture
  size once at startup, so a software fallback is visible instead of assumed.
  The snapshot now lives in one testable place (`renderPipelineInfo` in
  `adaptive.go`) covered by `renderpipeline_test.go`, which pins the exact
  "software fallback" values (including SDL's max-texture `0x0`) the device
  build would print if the GE8300 stack is ever bypassed.
- Rounded cards, rings and circles are drawn from a cached render-target
  texture instead of repeating every scanline fill every frame
  (`shapeCache.go`); the wallpaper readability scrim and day-cycle backdrop
  gradients got the same treatment in `dynamicbg.go`, replacing 80 resp. 24
  per-frame band fills with one blended Copy each. Both caches are
  renderer-local, budget-bounded on the handheld, and pixel-equal to the
  direct paths in the render tests.
- **B** on the video player no longer stops playback: it closes the settings
  sheet, then leaves full screen, then floats the video into the mini window
  described above. Stop is the square button, `q` on a keyboard.
- [CONTRIBUTING.md](CONTRIBUTING.md) points setup and the pre-pull-request
  checks at the make targets, and keeps the raw commands for anyone without
  make.
- The Emulators page states availability instead of hinting at it: each
  emulator tile carries an **Available** / **Unavailable** / **Not installed**
  pill at its bottom edge, and an unusable tile's picture is dimmed. The 4px
  corner dot it replaces was unreadable at handheld distance. The grid keeps
  sorting by availability (usable, then installed-but-unusable, then absent,
  alphabetical within each group), now covered by a test and described in
  [docs/CONTROLS.md](docs/CONTROLS.md).
- The launcher app grid is now row-virtualized: each frame visits only the
  rows the panel can show (plus one slack row for the partially visible row
  that hangs past the bottom edge), so scrolling a hundreds-of-apps launcher
  costs the same tile work no matter how far the list has scrolled. A row
  straddling the panel edge is now drawn clipped instead of hidden — partial
  cards read as the panel edge cut on the tile — with the scrollbar still
  indicating what is below (`appgrid.go`, pinned by `appgrid_window_test.go`).
- Thumbnail decode budget: a freshly scrolled grid used to start a JPEG decode
  and texture upload for every visible row in the same presented frame — 24
  rows could stack all of their decode cost onto one ~16 ms frame. The render
  path now issues at most 2 decodes per frame on the handheld (8 on desktop);
  the rest of an entering screenful is queued through the prefetch workers and
  fills in over the following frames instead of one frame stuttering
  (`thumbstore.go`, covered in `thumbdecode_test.go`).
- The Tools panel reads like the rest of the shell: its header opens with the
  same tools glyph as the Home tile, and each section heading ("FTP & IP",
  "Watch together", "System & updates") leads with a tinted chip carrying that
  group's glyph, so the grid can be scanned by shape before the small-caps
  label is read. A group the build has not seen yet falls back to the Tools
  glyph instead of rendering a bare heading. A tile row cut by the panel edge
  is drawn clipped like the Apps grid instead of hidden, which used to leave a
  blank band at the top of a scrolled panel, and a heading is never left as a
  fragment peeking above the footer (`toolsui.go`, pinned by
  `toolsui_render_test.go`).
- The browser's D-pad model is reachable on real gamepads and follows the page:
  **L3** (left stick click) cycles scroll ⇄ link mode alongside Tab, the toggle
  toast names both keys, and Up/Down in link mode now walk the nearest link on
  the adjacent visual row instead of flat anchor order, so a two-column page no
  longer throws the cursor across the screen. Left/Right keep reading order and
  a one-row page stays put (`browserLinkRowStep` in `browser.go`, pinned by
  `browser_test.go`).
- Power tools reads as a list instead of a stack of labels. Each row now leads
  with the tool's name in the larger face, the value ("20 snapshots", the sync
  target) sits quietly on the right, and the explanation runs underneath; the
  old menu put a small name above a large description, which inverted the
  hierarchy. The per-row `A Open` pills, the ▲/▼ scroll marks and the
  `Close`/`Back` rows are gone: the footer names what the focused row does, a
  thin scrollbar shows the way down, and **B** is the only way out. The panel is
  now sized to the rows it actually shows, so the capped height no longer left
  a dead band under the last row (`powertools.go`, pinned by
  `powertools_ui_test.go`).

### Fixed

- **Chat overlays let go of the pad when you leave the chat scene.** The
  message action bar, the member list and the profile card are all drawn inside
  the chat panel, but their input guards only asked whether they were open — so
  a bar or a card left behind by a scene change kept eating the next press on
  the screen you were actually looking at (the floating video's **X** on Home
  was one of them). Each now checks that the panel that draws it is the scene on
  screen and drops itself when it is not, so the press lands where it looks like
  it will ([player/chatactions.go](player/chatactions.go),
  [player/chatprofile.go](player/chatprofile.go),
  [player/controller.go](player/controller.go), [player/main.go](player/main.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  `player/chat_profile_test.go`).
