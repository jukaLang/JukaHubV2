# JukaHub engineering backlog

Open tasks: **401** (audited 2026-11-19).

This file tracks open work for the player, web builder and AndroidRT, plus a
small ideas parking lot.
Completed work belongs in the [changelog](../CHANGELOG.md) and feature docs.
The [hardware validation log](../docs/HARDWARE_VALIDATION.md) records what the
stock Smart Pro actually showed; the [release checklist](../docs/RELEASE_CHECKLIST.md)
is the repeatable device walk. Start with [P0](#p0--build-and-first-hardware-run),
then the [hardware queue](#hardware-validation-queue) or a subsystem below.

## How to use this file

1. Pick the highest-priority task you can verify. **[WIN]** needs a Windows
   build, **[HW]** a TrimUI device, **[NET]** a network service or download,
   and **[OWNER]** a product decision. A probe is partial evidence when the
   task also requires a live screen, input, audio or a second model.
2. Read the named files and docs. Finish only when the *Done when* outcome and
   *Verify* step hold. Remove the completed task in the same change, update
   `[Unreleased]` in the changelog, and add a specific follow-up if needed.
3. Use P0 for release blockers, P1 for current user pain, P2 for planned value,
   P3 for lower-priority improvements. S is at most half a day, M at most two
   days, L at most a week; split XL tasks before implementation.

Shared constraints: keep the 1 GiB Smart Pro responsive; avoid blocking or
allocating per frame; use the shared design and input hint helpers; use
`logDebug`/`logInfo`/`logWarn`/`logError` rather than the standard `log`
package. Add focused tests for data integrity, security, installers and
external parsers; keep UI and timing tests lean. Do not fake licences or ship
copyrighted game assets. See [development](../docs/DEVELOPMENT.md) and
[contributing](../CONTRIBUTING.md) for implementation and test details.

---

## P0 — build and first hardware run

### BUILD-1 — Windows build and `make check` green on one revision (P0, S) [WIN]
**Why.** Juka builds on Windows daily, but `make check` has not been shown to
pass on the same revision as a build since the latest no-compiler passes landed.
**Where.** `player/build.bat`, `Makefile`.
**Done when.** `player\build.bat nolaunch` and `make check` (vet, player tests,
web tests, workflow checks, backlog check) pass on the same source revision.
**Verify.** The clean `make check` tail.

### BUILD-6 — The Box64 pack reaches a release (P1, S) [NET]
**Why.** `wine-runtime.yml` builds the Windows-app runtime's Box64 pack and
`attach-wine-runtime` attaches it to the push's release, but no release is known
to carry `JukaHub-Wine-linux-arm64.zip` yet (`ALLOWED_NEEDED` later had to
admit `libresolv`); APP-17 waits for one.
**Where.** `.github/workflows/wine-runtime.yml`,
`.github/workflows/docker-image.yml` (`build-wine-runtime`,
`attach-wine-runtime`), `player/native/build-runtimes.sh` (`wine`,
`ALLOWED_NEEDED`), `.github/scripts/build-root.sh`.
**Done when.** A push's release carries `JukaHub-Wine-linux-arm64.zip` whose
BUILDINFO shows only `ALLOWED_NEEDED` libraries, no symbol newer than GLIBC 2.31
and `box64 --version` naming 0.4.4; the advisory step prints `wine-10.0` or its
log names the next fix; the next push with unchanged inputs says "Reused … from
run N".
**Verify.** The run's "Box64 (the Windows-app runtime)" step summary and the
release's asset list.

### HW-1 — Device checklist: boot, exit, display, Home and themes (P0, M) [HW]
**Why.** These behaviours have not been seen on a handheld. The release
checklist covers the basic walk (cold boot, buttons, quitting by Start menu and
chord); the renderer line (GPU-1), boot logo (SYS-17) and design walk (UX-7)
have their own [HW] tasks.
**Where.** Smart Pro and Brick on stock 1.1.x and CrossMix; `errors.txt`,
`autostart.log`, `framestats`.
**Done when.** Each step holds, or a FIX task names what differs:
1. Quitting from the power card, and from a JukaHub started over SSH, logs
   "MainUI is back after N s", leaves exactly one MainUI within 10 s and no
   display-holder warning (the firmware's OSD daemon is ignored).
2. With Start at login on, a power-on goes stock menu → black → JukaHub with no
   flash, and `autostart.log` has one "closed the stock menu" line.
3. Boot styles: Cinematic's fall from the screen stays smooth while the logo is
   several times its size, its crack layer draws on the GE8300 at the UI frame
   rate, it buzzes and knocks on impact and its ground fills the Brick's 4:3
   canvas; Roll is smooth at 30 fps; None goes straight to Home; Reduce motion
   and Battery Saver show the still logo; an app launch plays the short version.
4. Tabs Home: a fresh card opens on Tabs and an existing arrangement is kept;
   the strip fits the Brick with a full top bar; the D-pad moves between strip,
   page, top bar and power pill; L1/R1 switch tabs; the first
   Games/Favourites/Collections visit does not freeze Home while the library
   scans.
5. L2 opens and closes Edit Home (the arranging sheet on Tabs) on every layout,
   Q does on a keyboard and the footers say so; on Widgets, X + D-pad grows a
   tile to 3 × 3 and back with the grid making room, the cells look square on
   both panels and no widget draws outside its tile.
6. Glass → Realistic draws the blurred backdrop, sheen and rim on Home cards,
   Settings rows and buttons with no seams or square corners and never through
   pictures, at Home's usual frame rate; Classic looks as before.
7. Theme presets show Colour · Styles · Experience. The first-run tour offers
   five Home layout previews and saves the chosen layout; duplicate palette and
   layout pairs are hidden while saved IDs still load. Experience opens
   the packs with the same tabs and B never bounces between the two.
8. Settings: Y opens Search and "wifi", "dark mode" and "volume" land on their
   rows; More settings unfolds without the focus jumping.
9. Home search finds "emulators" and "voice shortcuts"; Xbox Remote Play and
   Steam open from Start → Games, a favourite and a collection.
10. Weather rain and snow move smoothly and still wallpapers are smoothly
    filtered.
11. The startup chime plays with the splash (first sound within about 100 ms)
    and the shutdown chime with the teardown, comfortable at the default UI
    sound level and without clicks; a gain change goes into `bootchime.go` and
    `branding/sound/chime_reference.c` together.
12. Daily challenge: on a day with a trusted clock, the first look at Home
    offers one library game, on the Continue card (Standard, Hero, Widgets)
    and on the Tabs Home as Recent's first item with its cover and minutes
    left, beside the Continue item, where A starts it; the same game after a
    restart; 15 minutes of play show "Daily challenge met" and award Challenge
    Accepted; anything played today brings Continue back; before the clock is
    confirmed no challenge shows.
13. Settings → About → Reset, on a Smart Pro and on Windows: every choice asks
    twice with Cancel focused; Erase everything writes its undo file
    (`JukaHub-erased-….jkbackup` in JukaHub Backups, Downloads on a PC), turns
    the VPN off (one UAC prompt on Windows), restarts into the first-run tour
    with an empty library, leaves ROMs, saves and Version history intact, and
    `.erased-trash` is gone about 30 s later; pulling power during the erase lets
    the next start finish it; deleting the undo file before the restart gives
    "Nothing was erased: its undo file … is gone"; B on "Close JukaHub to finish"
    calls it off; Reset → Undo an erase brings library, notes, trophies and
    sign-ins back; on Windows the erase starts only after the old process ended.
14. Settings → About → Controls on a Smart Pro shows its first page within
    300 ms of the press with no wrapped line clipped at the column edge, Y's
    contents and the shoulders' paging work, and Quick start opens with Wi-Fi
    off. Home's tip of the day sits above the footer on Standard, Hero and Tabs
    without covering a focused tile, on the Brick's 4:3 canvas too; Show tips
    off hides it and Hide today's tip hides it until tomorrow.
15. [WIN] Home's tips on a PC: with only a keyboard they name keys (F10,
    PgUp / PgDn, Alt+F4) and never a stick, a pad chord or the Power button;
    with a pad they name its buttons and say nothing about Power or sleep.
16. In Spanish and then Russian, Home's Clock, Calendar and Weather widgets,
    the calendars' weekday letters, the clock sheet's Day row and Chat's day
    separators switch at once to that language's day and month names; backup
    rows and note times follow Settings → Date & time's order and 12-hour
    clock; sizes read "1,5 MB".
17. Quick Settings (Select on Home, 2026-11-18): Tiles opens with every switch
    showing its real state and colour and the grid scrolls to the focus;
    Compact fits on one screen on both panels; A on Volume then Left/Right
    moves the level and the header's speaker follows; L2 opens Edit, L1/R1
    switch the three layouts, X + D-pad grows Brightness to 4×2 and back, Y
    hides LAN Play (gone after Done, back with Y), and the arrangement
    survives a restart; Power opens the power card from another screen.
**Verify.** Results, firmware and CrossMix versions in
`docs/HARDWARE_VALIDATION.md`.

### HW-2 — Device checklist: input, controllers and Bluetooth (P1, S) [HW]
**Why.** Mouse mode, the games mouse and the Bluetooth reconnect and pad code
have not run on a handheld. The release checklist covers buttons and a USB
keyboard; keyboard detection, pads in a stream and pairing persistence have
their own [HW] tasks.
**Where.** Smart Pro and Brick; a DualSense or DualShock 4 and a Bluetooth
headset; `/proc/bus/input/devices`, Bluetooth diagnostics.
**Done when.** Each step holds, or a FIX task names what differs:
1. Holding Select + L1 turns the pointer on, the D-pad moves it (Brick too) and
   Select's release does not go Back.
2. With **Mouse in games and apps** on, ScummVM, DOSBox or a port lists "JukaHub
   Mouse" in `/proc/bus/input/devices`; the chord, Menu and game exit give the
   pad back; record whether an SDL app sees a mouse that appears mid-run (INP-17
   acts on it).
3. Paired pads and a headset reconnect after a start, a wake and the radio
   coming on, and stay off after a manual disconnect; a headset dropping pauses
   video and music; speaker and headset keep their own volumes.
4. A Bluetooth pad's battery reads on the TSP kernel
   (`/sys/class/power_supply`); Identify rumbles and blinks it; a headset's
   codec line shows SBC or AAC.
5. [WIN] On a Windows touch screen one tap is one press and one release (a
   Home tile opens once, a slider follows the tap); a sideways swipe on the
   Tabs Home changes tab with the L1/R1 slide (at once under Reduce motion)
   and an up or down swipe scrolls it; a swipe over Settings, the Browser or
   Chat does nothing and no screen jumps to another; the picture viewer pans
   and pinch-zooms; a touchpad only moves the pointer; the first touch on the
   screensaver only wakes it; a mouse click is never doubled.
6. On the Smart Pro and the Brick, in a RetroArch, a PPSSPP and a port game,
   Select + R1 saves one `game_<time>.png` per press showing the game (no
   emulator menu), the motor buzzes when it is written and "While you were
   playing" counts it; record any emulator that also acts on Select + R1.
7. Mouse mode on, hands off for 10 s: the pointer disappears and mouse mode
   stays on; a D-pad nudge brings it back where it was; with it hidden the first
   A only shows it and the second clicks; Quick Settings opened and closed while
   it is hidden leaves it hidden. The right-stick pointer goes after 10 s. A USB
   mouse on the TSP (if the firmware's SDL shows a cursor at all) and the
   Windows cursor hide after 10 s still and come back on the first move; no
   flicker or stall on the TSP, the Brick or the Smart Pro S about 10 s after
   start (the one `SDL_ShowCursor(0)` call).
**Verify.** Results and firmware versions in `docs/HARDWARE_VALIDATION.md`.

### HW-3 — Device checklist: sound, voice and Juka (P1, M) [HW] [NET]
**Why.** Juka's voice, offline answers, the wake word, voice typing, dmix and
the USB DAC path have not run on a handheld. The mixer, spatial audio, stick
lights and microphone proof have their own [HW] tasks; the chimes are HW-1
step 11.
**Where.** Smart Pro on stock firmware (Brick and Smart Pro S where named); a
Groq key; Wi-Fi on and off; a USB-C DAC; a Bluetooth headset; `logs.txt` with
`JUKAHUB_LOG_LEVEL=debug`.
**Done when.** Each step holds, or a FIX task names what differs:
1. The offline speech and offline-AI packages install and update once by
   themselves; with Wi-Fi off an answer's first words come within about 4 s of
   the end of the question, and the CPU boost ends after it.
2. Juka's voice: the delay from an orb answer to its first word is recorded
   (Groq and offline; SND-14 acts on it); no clicks with the liquid background
   on (else raise `ttsAheadMs` to 120); B silences it at once; music ducks and
   comes back; an alarm takes over.
3. On a Groq account that has not accepted the speech model's terms, Hear Juka
   shows **Turn on Groq's voice**, whose QR opens Groq's page on a phone, and
   Juka speaks once they are accepted.
4. Hey Juka opens the orb on Home and the game assistant in RetroArch and
   PPSSPP; ten minutes of talk, TV and a loud game give no false wake; the mic
   still works for the orb, voice typing and the stick lights.
5. "Turn on dark mode", "turn off background music" and "set screen timeout to 5
   minutes" work; "is Wi-Fi on?" changes nothing.
6. A word taught on Voice shortcuts is still recognised days later; "RetroArch
   menu" and "open PPSSPP" open their menus on stock and CrossMix.
7. R2 on the on-screen keyboard starts voice typing in Chat and the Browser's
   address bar, also with the pointer up; auto-start voice typing ignores the
   chime, stops after 7 s of quiet and never starts on the Wi-Fi password field.
8. With a Groq key, a portrait phone photo, a WebP (on stock firmware too)
   and a 60 s phone video sent to Juka arrive upright and get answers; the
   "Watching the video…" time is recorded and the work folder is gone
   afterwards.
9. With a Groq key, "what's this?" on Settings and on a game list describes the
   screen, with the orb out of the picture and the pairing code blanked; Off
   sends nothing.
10. Juka tools on Groq: "why won't my last game start", "what's filling my
    card", "make games smoother" and a move to trash run through their cards;
    offline lite answers device questions.
11. Sleeping 30 s in the middle of an answer gives no error, and the answer
    arrives once Wi-Fi is back.
12. Music under a RetroArch game opens `dmix` at 48 kHz on Smart Pro, Brick and
    Smart Pro S (log "music under the game"); if RetroArch with audio sync lags
    through dmix, JukaHub's first open gets its own dmix definition with small
    periods.
13. A USB-C DAC shows "USB DAC · <name>"; a 24/96 FLAC with Bit-perfect shows
    96000 and S32_LE or S24_3LE in `/proc/asound/cardN/pcm0p/sub0/hw_params`;
    unplugging pauses.
14. A Bluetooth headset connected before and after start takes Home sounds,
    music, a video, a RetroArch and a PPSSPP game, and a disconnect returns
    everything to the speaker without a pop; Bluetooth diagnostics show
    BlueALSA on D-Bus, its streams and the route. A bluealsa stopped with
    `kill -STOP` is continued, and one that stops answering D-Bus is restarted
    within about 10 s and the headset plays again; a headset JukaHub cannot
    open toasts the reason after 45 s (2026-11-22).
15. A question to Juka with a picture or a video, asked just before a
    one-minute sleep, is asked again soon after the wake once Wi-Fi is back
    (no six-minute wait); the reply is whole, never half an answer cut by the
    sleep, and no ffmpeg is left running.
**Verify.** Results and timings in `docs/HARDWARE_VALIDATION.md` (voice timings
also in `docs/VOICE.md`).

### HW-4 — Device checklist: network, sync and the phone Companion (P1, M) [HW] [NET]
**Why.** Wi-Fi after a wake, captive portals, phone hand-offs and the save-sync
services have not run on a handheld. The release checklist covers joining Wi-Fi
and a one-minute sleep; the Wi-Fi stack, network time and the VPN have their own
[HW] tasks.
**Where.** Smart Pro on stock and CrossMix, a second handheld, a phone; a Google
account, a RomM 5.2+ server, Syncthing-Fork; `logs.txt` with
`JUKAHUB_LOG_LEVEL=debug`.
**Done when.** Each step holds, or a FIX task names what differs:
1. After a one-minute sleep on Wi-Fi with Chat open: `wifi: back online N s
   after wake` with N under 10, no Offline or Network restored toasts, and Chat
   goes Reconnecting… → Live by itself; a wake with Wi-Fi off does nothing; the
   `wifi: after wake:` debug lines go into `docs/WIFI.md`.
2. Behind a real captive portal, Sign in opens the portal in the Browser, its
   links and form load, and leaving after signing in shows online within about a
   second; joining another network shows "checking…", never the old Sign in.
3. Groq key → **Paste it from your phone**: the phone opens the companion at the
   Groq card; Save closes the QR and the row reads "Saved · ••••abcd"; the VPN's
   phone import opens its card the same way.
4. Google Drive (a "TVs and Limited Input devices" client in the build): the QR
   sign-in completes; a save made on one handheld is on the other after one
   launch there; a conflict keeps both; Sign out revokes; if `about?fields=user`
   is refused with `drive.file` alone, add the `email` scope.
5. RomM 5.2+: a downloaded and a card-only game both link; launch pulls, exit
   pushes; Sync all finishes; a 4.x server shows the one-time message; states
   move to `/api/states` if the server keeps them apart from saves.
6. Syncthing: the v2 linux-arm64 release runs from the card (SQLite on
   FAT/exFAT), accepts `serve --home --no-browser --no-restart --no-upgrade
   --gui-address`, honours `STGUIAPIKEY` and `STNODEFAULTFOLDER` (no default
   "Sync" folder), pairs with Syncthing-Fork by QR, and leaves no `syncthing`
   process after JukaHub quits.
7. FTP (FileZilla) on stock and CrossMix: uploads to ordinary ROM folders (GBA,
   PSP, PS, NDS) work; Roms/PORTS, any scripts or apps system and Roms/PC answer
   "…use SFTP"; a game profile imported while FTP runs is read-only within a
   command or two; on an ext4 card a symlink `Roms/x` → Ports refuses STOR, MKD
   and RNTO; a session starts without a stall; no ROM folder of a RetroArch or
   standalone system is locked because its launch.sh uses a helper function or
   `set --`. A system locked by mistake (or missed) gets a FIX task with its
   launch script.
8. Wi-Fi off and on from Quick Settings, the Home Wi-Fi tile and airplane mode:
   the header's network chip shows "checking…" at once and the new verdict
   within about a second of the switch ending (with the radio back on, online
   once the handheld has rejoined).
**Verify.** Results and versions in `docs/HARDWARE_VALIDATION.md`.

### HW-5 — Device checklist: games, ports, Remote Play, Android and Windows apps (P1, M) [HW]
**Why.** These launch paths have not been seen on a handheld. The release
checklist covers one RetroArch game, one standalone emulator, one port and one
app; CrossMix's RetroArch, emulator updates, Remote Play, AndroidRT, hakuX and
Wine have their own [HW] tasks.
**Where.** Smart Pro on stock 1.1.x and CrossMix (Brick, Smart Pro S and a
NextUI card where named); PortMaster; a PS5; `errors.txt`,
`/proc/<pid>/environ`, `/proc/mounts`.
**Done when.** Each step holds, or a FIX task names what differs:
1. A GBA game started from JukaHub and from MainUI has the same
   `LD_LIBRARY_PATH`, `PATH` and `HOME`; after the JukaHub game the
   `savestate_auto*` values in `retroarch.cfg` are the pre-launch ones and
   `data/retroarch-launch-scope.json` is gone.
2. PSP → Install PPSSPP installs the managed `ppsspp-tg5040` build (or falls
   back to the firmware's PPSSPP); a game starts, saves are shared with the
   firmware's PPSSPP, `/proc/mounts` keeps no bind after the game or a crash,
   the pause-menu exit returns to JukaHub; Update all and Go back to the
   previous build work; the aspect is right on the Brick.
3. The Games tab and Which emulator?: Install badges and the recommended
   emulator match what CrossMix's `Emus` scripts launch; Install lands on the
   right Emulators tile; the collection keyboard names, renames and deletes; a
   console whose games are only pinned per game loses its Install badge within
   30 s.
4. A fresh card with about 100 ROMs on the charger gets covers about 2 minutes
   after Home, with one "Found box art for N games" toast and no refetch loop.
5. The game assistant (hold Select + R2) over RetroArch, PPSSPP and a port shows
   over the paused game without flicker, and the game redraws after B; the game
   sees none of its presses and nothing stays held (hold R2 through the close);
   chime, voice and mic work while the game holds ALSA; Select + R2 is free in
   CrossMix's RetroArch, DraStic and PPSSPP; `kill -9` with it up makes the next
   start ask the paused game to quit.
6. PortMaster on stock, CrossMix and JukaHub OS: a native SDL2, a GameMaker, a
   Mono/FNA, a Godot and a Weston port each start with sound and controls, quit
   with Select + Start leaving no gptokeyb or weston, and show in Games with
   cover and playtime; Update, Uninstall and "Why didn't it start?" behave; a
   port's options sheet shows only its CPU rows.
7. PS Remote Play: Play shows "Starting the stream…" on its row without a stall
   and B stops it; a stream ends back in JukaHub (`screen handed to helper pid`
   in `errors.txt`), and the power key during a stream hands the screen back.
   Where the power key sleeps the handheld instead: woken after 10 s and after
   2 minutes, the stream either carries on or shows Reconnecting and streams
   again (`nothing ran for`, `reconnected to` in `psremote.log`); with the
   console put in rest mode meanwhile it gives up after about a minute with the
   Couldn't reconnect card and JukaHub's "could not be reconnected" toast.
8. On a NextUI card, Tools → JukaHub opens JukaHub and quitting returns to
   NextUI's menu; a `Name (TAG)` game starts through its pak with saves in
   `Saves/<TAG>`; minarch started outside NextUI's loop has volume and
   brightness keys. Game options → Cheats on such a game writes
   `Cheats/<TAG>/<ROM file>.cht` for the launch only; the switched-on cheats
   are active when the game starts (if minarch ignores `cheatN_enable`, the
   sheet's note must say to switch them on in Options → Cheats).
9. `androidrt/surface-probe` prints its `SDL:` verdict on each model (the Smart
   Pro S's line goes into `docs/ANDROID.md`); a game with `ANDROIDPAK_SURFACE=1`
   logs its GLES libraries bound beside its libEGL and still presents.
10. A Wine profile with the time-lapse on leaves a film that shows the game
    (else skip the capture for WestonPack sessions with one log line).
11. A Windows launcher that starts another program and exits keeps WestonPack
    and the pad until that program exits; quitting a plain game returns to
    JukaHub within about 6 s with no buzz, and the app log shows
    `jukahub: the program exited (0)` (proof that `westonwrap.sh` passed the
    `sh -c` script through intact — if every launch fails instead, run the
    script from a file); a launcher whose leftover tool never exits buzzes about
    10 s after the launcher exits; Select + Start during the wait returns to
    JukaHub and leaves no wine or box64 process (`ps`).
12. With RetroArch signed in to RetroAchievements, one PSP launch through
    JukaHub's PPSSPP shows that account signed in under PPSSPP's Achievements
    settings with hardcore off, and a game's achievement list loads. Record
    whether PPSSPP really reads the `[Achievements]` keys
    `AchievementsEnable`, `AchievementsChallengeMode` and
    `AchievementsUserName` and the token file
    `PSP/SYSTEM/ppsspp_retroachievements.dat` (written from memory; fix a
    name that differs in `ppsspp_managed_ra.go`). An account signed in inside
    PPSSPP survives the next launch; with the card's switch off nothing is
    written; `errors.txt` has "PPSSPP signed in to RetroAchievements" and no
    token.
13. Game options → Choose cover on a game with a wrong cover lists server
    names and `Imgs` pictures, each preview appears within a few seconds, and
    A replaces the cover in the library and on Home; Name for covers on a
    mis-named ROM writes `data/artwork-aliases.json` and the re-match finds its
    cover; the chooser fits the Brick's 4:3 canvas.
14. On a Smart Pro with about 5 000 ROMs the log shows an automatic box-art
    pass that says "the library's scan" right after the library was opened and
    one that says "scanned the card" otherwise, with the time each took from
    the pass's start to its first batch; a first start with Wi-Fi but no
    network time yet logs "waiting for network time to set the clock" until
    the sync (at most 30 min, then "the first pass runs anyway").
15. With a newer Windows-app runtime pack published and Emulator updates on
    Notify me, the Windows Apps setup page's runtime row names the new build
    after the daily check, and A updates it with phase and bytes on the install
    card; a running Windows program or another install refuses it with a
    toast.
16. A homebrew ROM with no box art, played for three minutes in RetroArch (no
    Menu press in the last 30 s), comes back with "1 cover taken from the
    game" and its tile shows a trimmed picture of the game in the library,
    Continue and Home; `data/game-art/.gameplay/<System>/` holds it. A
    standalone emulator (PPSSPP, DraStic) and a game left on its pause screen
    from minute one behave too (the second keeps its tile until minute four);
    on a model whose emulator draws past fb0 (KMSDRM) the log shows no
    picture kept and no retry storm. On Windows a RetroArch game in a window
    gets one; switching to another window at the two-minute mark gets none.
17. Game options on the FAT card: **Rename file** on a PlayStation `.cue`
    game with a same-name `.bin`, a save state and an `Imgs` cover renames all
    of them, the cue's FILE line names the new `.bin`, the game starts and
    loads its state; a case-only rename (`tetris` → `Tetris`) works; a name
    already taken is refused with nothing moved. **Remove cover** leaves a
    lettered tile that L1 and a played session do not refill, and **Restore
    cover** brings it back. **Delete game… → Move to the trash** on a disc
    game empties its folder into `data/reset-trash/<date>-deleted/`. On
    NextUI a renamed game keeps its `Saves/<TAG>/` save and states; a DS
    game's DraStic save follows (check what DraStic names its states:
    `<rom>_N.dss` would not follow yet).
**Verify.** Results, firmware and CrossMix versions in
`docs/HARDWARE_VALIDATION.md`.

### HW-6 — Device checklist: media, Browser and YouTube (P1, S) [HW] [NET]
**Why.** These playback and hand-off paths have not run on a handheld. The
release checklist covers a local video; yt-dlp, the stream fallback, subtitles
and Save offline have their own [HW] tasks.
**Where.** Stock Smart Pro and a second handheld or a PC; `errors.txt`, `free
-m`.
**Done when.** Each step holds, or a FIX task names what differs:
1. A video keeps 30 fps past 10 s without input (the idle cadence leaves
   playback alone).
2. When a YouTube stream falls back to downloading, the way that won (picture
   and sound, HLS, single file, native) and why the others lost are recorded; B
   mid-download leaves no `jukahub-video-*` file and no yt-dlp process.
3. A YouTube video queued in Downloads arrives as H.264 at or below the playback
   ceiling.
4. Watch Party: a YouTube room plays on a guest at the room's position; B on the
   guest's loading card leaves the video until Join; a new pick by the host
   while a guest still loads switches the guest.
5. The Browser's Select menu → **Open on your phone** shows a QR code a phone
   camera opens on the same page; on a card that still has `Apps/WPE`, Home →
   Browser and a chat link open the built-in browser, never WPE.
6. Thirty minutes each of a YouTube video, a direct .mp4 link and a Live TV
   channel on the handheld's Wi-Fi, with a 5-minute pause and a walk to the
   edge of the signal, never log "stream stalled" or stop early; turning the
   router off mid-video ends it within about a minute ("The stream cut out —
   downloading the video to carry on" for the web video, "Playback stopped —
   the connection timed out" for the others); `errors.txt` has no "Option …
   not found" for `-rw_timeout` or `-reconnect`.
7. Music → History, on a Smart Pro and on Windows: after three songs (one
   skipped after 5 s), a one-minute pause, an audiobook stopped part way and a
   restart, Continue listening holds the audiobook at its place, Recently
   played the two full songs once each and Top artists the artist; A on each
   plays the right album or book from the right track; Clear listening history
   takes two presses and leaves no `musichistory.json.bak`; a later backup
   holds `musichistory.json`; the chips fit the Brick's canvas.
8. A radio station and a podcast in Music, router off mid-play: within about a
   minute "Playback stopped early — press Play to resume"; the podcast keeps
   its place and Play resumes it there, the station starts live; `errors.txt`
   has no "Option … not found" for `-rw_timeout` or `-reconnect` from the
   music decoder.
9. On a stock Smart Pro (SDL_image without WebP, ffmpeg installed) a .webp
   photo is listed in Files with a thumbnail, opens in the picture viewer and
   works as the lock screen; a .webp cover in `.media` shows in the
   Games library after one library refresh; a game whose .webp cover ffmpeg
   cannot read (an animated one) gets a downloaded cover; `webp-covers/` stays
   under 256 MB.
10. An .m4b audiobook with chapters lists them in the player; Next and
    Previous move between them (Previous restarts the current one first); the
    now-playing line names the chapter; after quitting mid-chapter the
    Audiobooks shelf says "Continue at <chapter>" and Play resumes there.
**Verify.** Results in `docs/HARDWARE_VALIDATION.md`.

### HW-7 — Device checklist: power, sleep, drivers and the other models (P1, M) [HW]
**Why.** Sleep and wake, the driver layer for the four TrimUIs, Power off after
sleeping and the system tools have not run on a handheld; the Smart Pro S and
Brick Pro have not run JukaHub at all. Brightness (SYS-16), the travel lock
(PWR-10) and JukaHub OS (SYS-22) have their own [HW] tasks.
**Where.** Smart Pro, Brick, Smart Pro S and Brick Pro on stock (CrossMix,
NextUI and spruceOS where named); Settings → About → Drivers & hardware;
`errors.txt`.
**Done when.** Each step holds, or a FIX task names what differs:
1. On each model, Drivers & hardware shows no "not found" for a part the model
   has, and the log's `device` line names the right model (also on CrossMix,
   NextUI and spruceOS).
2. Smart Pro S: the `render pipeline` line names its video driver; under KMSDRM
   a RetroArch game and a port start with picture and JukaHub comes back
   (`handover` "DRM master dropped/taken back"); both cpufreq policies follow
   Auto performance; volume changes speaker and headphones; Fan Quiet and Max
   take, Auto leaves `hardwareservice` alone and 85 °C forces full; Bluetooth
   comes up with and without the firmware's script (ttyAS1 at 1500000 baud is a
   guess); the backlight is off in sleep; the A523 layer's GPU devfreq node
   (`1800000.gpu`, a guess) exists.
3. Brick Pro: the `device` line names the Brick Pro (model code TG4040 or
   TG4070, `deviceModelCodeAliases` in `device.go`; record the code a real unit
   reports), brightness works (`/dev/disp` first), rumble stays at the 2.5 V
   cap, the sticks calibrate and Bluetooth (XR829) attaches.
4. Brick: the top bar and FN-key LEDs follow the light modes; XR829 still
   attaches when no daemon holds ttyS1.
5. JukaHub OS: one card boots all four; the other family's driver layer reads
   "Not for this handheld"; Return to the firmware restores every value.
6. Power off after sleeping (1 h) with a RetroArch game: after the deadline wake
   `/sys/class/rtc/rtc0/wakealarm` is empty (if a BSP keeps the old value, drop
   the "alarm no longer pending" test and rely on time asleep with no power
   press); the log shows the save request, the exit and the power-off; the next
   power-on resumes the game without the boot animation; `/sys/power/mem_sleep`
   and the suspend-state line are recorded per model, and so are both clocks
   when the wake line notes that BOOTTIME and the RTC disagree.
7. Inside a minarch game on NextUI (JukaHub the first UI), Power off after
   sleeping at 15 min: the power key's sleep logs "NextUI game asleep, …
   saved by minarch" (if not, the marker came too close to the suspend) and
   `wakealarm` then holds a time 15 min ahead (empty while playing); after the
   deadline wake the log shows "minarch saved … powering off" and "marker put
   back", and the next power-on resumes the game from slot 9. Record whether
   NextUI's suspend script clears the wake alarm (then nothing powers off),
   that a power press before the deadline wakes normally, and whether minarch
   writes slot 9 on SIGTERM.
8. After `kill -9` of JukaHub on the idle dock, the next start logs "CPU
   settings differed …; put back" and the firmware's governor is back.
9. The Hardware screen's Memory tile and About's Memory row match `VmRSS` in
   `/proc/<pid>/status`.
10. Command Deck: every category runs under the TrimUI busybox; pickers,
    favourites, macros and chips work with the pad alone.
11. Sleep and wake on the power key, 20 cycles each on Home, in a RetroArch
    game and during a video: the picture is back within about a second; the
    game and the video go on with sound and no stuck button; the clock, Wi-Fi
    and battery read fresh at once; nothing polls while asleep; an overnight
    sleep's battery drop goes into `docs/PERFORMANCE.md`.
12. Screen off (ambient dock, music) really darkens the panel (`bl_power` or
    brightness 0 under `/sys/class/backlight`, or the display driver's switch)
    and the level comes back on wake.
13. Tools → Patch → Install it later, with a release in the update cache: Next
    start installs it with the install bar over Home and restarts into the new
    build; When charging installs after 15 untouched minutes on the charger; a
    game started first holds it until the game ends.
14. Settings → About → Update channel → Beta with a prerelease published: Check
    for updates offers the prerelease and installs it; back on Stable the offer
    drops and nothing is downgraded until the next stable release; About and a
    bug report (one filed from the phone too) name the channel.
15. A design's Terminal button running a command that never ends is stopped
    after two minutes with "(stopped after 2m0s)" and leaves no process of it
    (`ps`); a command that prints without pause leaves JukaHub's memory flat.
16. Tools → Patch: a package installed before a restart is still listed as
    installed after it, History shows the earlier sessions, and Remove deletes
    its files from the card.
17. One press to sleep and one to wake in a RetroArch game, a PPSSPP game and
    a port, with Power button on Sleep and again on Nothing (the firmware's
    sleep): only the game is on screen at the wake, no second picture takes
    turns with it. Record the `display` line written 3 s after each press and
    wake ("the display is held by: …"), any "MainUI … is drawing beside the
    game" line and what started that MainUI, and whether "firmware handles the
    power key" appears (on a kernel whose BOOTTIME misses the suspend, the RTC
    now catches it).
**Verify.** Results, models and firmware versions in
`docs/HARDWARE_VALIDATION.md`.

### SYNC-1 — Save sync: first-sync choice, more save folders, safe launch order (P2, M)
**Why.** A second device's first Drive sync can push its older save over a newer
one (both kept, but surprising); ports, Android/HTML5 games and netplay get no
launch/exit sync; RomM covers RetroArch folders only (not PPSSPP, DraStic or
"sort saves by core" subfolders) and may hash a matched ROM on the launch path
without a budget; a 409 on push says a copy was kept when none was written;
relaunching a game while its exit push runs skips the pull.
**Where.** `savesync_auto.go`, `gdrive_sync.go`, `romm_autosync.go`,
`romm_server.go`, `ports.go` (early return in `launchLibraryGame`).
**Done when.** A never-synced device's first sync asks "Use Drive's copy / Keep
this one / Keep both" per game (with SYS-20's card); ports' and standalone
emulators' save folders take part; hashing on the launch path is budgeted; the
409 text is right; a launch waits for that game's running push.

### SYNC-2 — Cloud sync set up from the phone, and WebDAV, on a device (P1, S) [HW] [NET]
**Why.** The 2026-11-18 pass (`companion_cloud.go`, `webdav_client.go`,
`webdav_sync.go`, `cloudsync_ui.go`) was written without a device, a Google
client or a WebDAV server.
**Where.** Smart Pro on stock and CrossMix, a phone (iOS and Android), a fresh
Google Cloud project, Nextcloud, Koofr and a Synology/Apache WebDAV share.
**Done when.** 1. Own Google client: Set up Google sign-in → the QR opens
`/cloud#google`; the downloaded JSON is accepted and a Desktop client refused
with the type message; the phone shows the code, approving it signs the
handheld in and the first Compare runs; Remove signs out; a release built with
the `JUKAHUB_GOOGLE_CLIENT_ID`/`SECRET` secrets signs in with no setup. 2.
WebDAV: each server connects from the phone (Nextcloud with a bare
`https://SERVER`), a wrong app password is refused on the phone, `http://` to a
public name is refused and to a LAN IP works; Compare/Sync round-trips saves
between two handhelds; an edit in the server's web page is noticed; the launch
pull fits 6 s. 3. Logs: `webdav:` lines show the listing mode (whole tree or
folder by folder); no password or token appears.

### HW-11 — GreenOvercast on a TrimUI: Cloud Gaming and X1 (P1, S) [HW] [NET]
**Why.** Cloud Gaming (`cloudgaming.go`) and X1 (`xremote.go`) hand the
handheld to GreenOvercast through the build manager (`greenovercast.go`). Its
authors test it on H700 and RK3566 firmwares only, it needs glibc 2.38, and
both release asset URLs, the build-recognising file names and the build switch
were written from release notes without a device (session notes-B).
**Where.** `greenovercast.go`, `cloudgaming.go`, `xremote.go`.
**Done when.**
- *Knulli (Smart Pro):* the cloud build and the xHome build each install
  through `portsInstallZip`, headless and through PortMaster;
  `data/greenovercast.json` and the folder scan name the right build;
  switching builds replaces the port with no leftovers.
- *Streams:* Xbox Cloud Gaming and GeForce NOW sign in with the phone code and
  play (note the frame rate with software decoding at 720p/30); X1 streams a
  console on the xHome build and warns on the cloud build.
- *URLs:* `Producdevity/GreenOvercast` `releases/latest/download/greenovercast.zip`
  and the doomL `v0.3.0-xhome` asset resolve; fix the lists if not.
- *Stock firmware / CrossMix:* JukaHub downloads its checked private glibc on
  first launch and starts only the GreenOvercast binary with the new loader;
  verify sign-in and a live stream on the device.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### HW-12 — Display page, Hotspot and downloadable fonts on a device (P0, S) [HW] [NET]
**Why.** The 2026-11-17 night pass (Settings → Display, `displaysettings.go`,
`displaytune_soft.go`; Tools → Hotspot, `hotspot*.go`; Font, `uifont*.go`) was
written with no Go toolchain, no device and no web access (session 8700bcfa
notes-hotspot.md / notes-fonts.md list every guess).
**Device evidence.** The stock Smart Pro advertises AP mode and has
`softap_up`, `hostapd`, `udhcpd` and `dnsmasq`. The firmware tool's usage text
confirms the requested `softap_up` argument order. All 20 font catalogue URLs
responded, and a verified TTF download worked on the device after exporting
its existing CA bundle for the firmware's old `curl`. A phone join, Wi-Fi
restore, picture controls and applying fonts still need a live run.
**Where.** `displaytune*.go`, `hotspot_linux.go`, `uifont.go`.
**Done when.**
- *Display:* on Smart Pro, Brick and Smart Pro S the picture rows move the panel
  (log has no "could not set enhance_*"); after a sleep the picture is still
  tuned; on Windows contrast/exposure/colour temperature visibly change JukaHub
  and Saturation is greyed with its reason; LED lights / LED brightness match the
  Stick lights page.
- *Hotspot:* the log's `hotspot: tools: softap_up=… hostapd=…` line says which
  path the firmware offers; a phone and a second handheld join, get an address,
  open the Companion and play a LAN netplay room; turning it off (and sleeping,
  and quitting) rejoins the saved Wi-Fi. Fix the softap_up argument order, AP
  support on XR829/AIC8800 or the udhcpd applet guess if they differ.
- *Fonts:* each catalogue face downloads (raw.githubusercontent.com, then
  jsDelivr), installs into `data/fonts`, applies, survives a restart and
  removes; Noto Sans JP shows a Japanese ROM name; a 404 shows its toast.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### PORT-HW-2 — Ports Store on a device (P2, S) [HW] [NET]
**Why.** The store's installer, pictures and download counts were built from
documentation, not a run (session notes-A).
**Where.** `ports_native_install.go` (`pnInstall`, `pnParseRuntimes`,
`pnRuntimeGate`), `ports_store_install.go` (`pstoreHarbourInstall`,
`pstoreHMExports`), `ports_store_images.go` (`pstoreShotBase`),
`ports_store.go` (`pstoreStatsURL`).
**Done when.** On stock TrimUI (with and without TRIMUI_EX), CrossMix and
Knulli: a Ready to Run port installs through JukaHub's own installer
(`ports_native_install.go`; `store-install.log` shows "JukaHub's own
installer" and where the files went) with its runtime in PortMaster's `libs/`,
starts, and shows as installed in PortMaster's own menu and the firmware's
Ports tab; a port whose runtime was deleted downloads it again before it
starts; a zip copied into `autoinstall` installs when the Ports panel opens;
**Y → Update PortMaster** appears for a newer release and leaves ports and
`libs/` alone; the Emulator updates pass updates PortMaster by itself on the
charger (`emu-autoupdate.json` has a `portmaster` record; its `version` file
matches the release, or the pass stops after one install when the tag and the
file are written differently) and installs it on a card with ports and no
`Apps/PortMaster`; opening the Ports Store on a card without PortMaster
installs it once; with `portsStoreInstaller: harbourmaster` a port installs
headless (exit 0, launcher in a ports folder); with
`portsStoreInstaller: portmaster` several ports queue in `autoinstall` and
PortMaster opens once for all of them; screenshots appear for most cards (else
fix the address pattern — for example PortMaster-New's images release);
download counts match portmaster.games; Update all updates a port whose MD5
changed; the cards and the details fit the Brick's 1024×768.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### PORT-HW-3 — PortMaster opens on a stock Smart Pro and Brick (P1, S) [HW] [NET]
**Why.** On 2026-11-18 Juka reported that PortMaster does not open on the stock
Smart Pro, and on 2026-11-20 sent "Fix PortMaster" from a stock Brick
(JH-QCKJ9M, no PortMaster data in it). Its TrimUI launcher needs TRIMUI_EX
(`ex_config`, Python 3.11, a real bash, SDL2 linked into `/usr/lib`); JukaHub
now supplies a downloaded Python (whose `sitecustomize.py` replaces a
`PYSDL2_DLL_PATH` without SDL2), Ubuntu's bash on the private glibc, a real
`/bin/bash` inside each launch's own mount namespace and a patched copy of
`launch.sh`, all built from documentation and search results, not a run.
**Where.** `portmaster_runtime.go`, `portmaster_python.go`,
`portmaster_binbash.go`, `portmaster_report.go`, `glibc_runtime.go`
(signed-index install and weekly refresh), `ports.go` (`startPortScript` gate,
`startPortMasterRepair`), `ports_manage.go` (PortMaster's Y options).
**Done when.** On a stock Smart Pro 1.1.x and a stock Brick without TRIMUI_EX:
the launch log's output starts with "jukahub: /bin/bash is a real bash for
this launch" and `ls`/`mount` still work inside a port; a Report a bug sent
right after shows the PortMaster section with both log excerpts; the first open
downloads Python and the runtime with the install card, then PortMaster's menu
appears with working buttons and sound; `gamediag/ports/PortMaster.log` names
JukaHub's bash and python3 and, if `launch.sh` reads `ex_config`, the
`.jukahub-launch.sh` copy; PortMaster installs a Ready to Run port and that
port starts from JukaHub; **Y → Why didn't it open?** lists nothing on that
card and the missing pieces on a card with them removed; **Repair PortMaster**
keeps `libs/` and the ports; a CrossMix card is left on its own bash and
Python. Also check that `data/private-glibc/current` names a `runtime-2.39-…`
folder and GreenOvercast still starts through it, and whether PortMaster
2026.09.19+ reports the Smart Pro's analog sticks.
Testers on a Brick were also left on PortMaster's splash (2026-11-20, a photo;
`portmaster_splash.go`): check with `ps` while the splash shows that it is a
`love.<arch>` process on `utils/pmsplash` and which `/dev` display nodes
pugwash opens (`ls -l /proc/<pid>/fd`); that a pugwash killed by hand brings
JukaHub back within a few seconds with the "closed before its menu appeared"
toast; and that a working PortMaster is never cut short by the splash watch.
**Verify.** Record in the HW log with the PortMaster log; file a FIX task for
anything that differs (for example the `PYSDL2_DLL_PATH` names or the
`launch.sh` line forms).

### STEAM-HW-1 — Steam compatibility pass on a device and a PC (P1, S) [HW] [NET]
**Why.** The 2026-11-17 evening Steam pass (session 9911e29e) was written with
no Go toolchain, no device, no Steam and no web access: PortMaster's
`attr.store` layout, Steam's 64-bit shortcut link, the Flatpak/Snap launchers
and the ClientComm POST retry are read from documentation, not a run.
**Where.** `steam_ports.go`, `steam_compat.go`, `steam_shortcuts.go`,
`steam_other.go`, `steam_remote.go`, `steam_ui.go`.
**Done when.**
- *Handheld:* Apps → Steam → Handheld lists owned games that have a port
  (Stardew Valley or another store-linked port matched "by store link" in its
  sheet); Install port → PortMaster installs it; Copy files from a paired
  Windows PC fills `gamedata/`, the card shows progress, cancel leaves *Copy
  not finished*, resume completes it, and Play here starts the game.
- *Deck labels:* rows show Deck Verified/Playable/Unsupported for rated games;
  no 429 burst in the log while scrolling a large library.
- *PC:* a non-Steam game added in Steam starts from the library and from the
  handheld; on a Linux desktop the Flatpak and Snap Steam start a game.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### BOOT-HW-1 — Boot time on JukaHub OS and stock firmware (P1, S) [HW]
**Why.** On 2026-11-18 Juka asked for a boot under two seconds. The start-up
path was trimmed by reading the code only (no Go toolchain, no device): the
design file loads beside SDL, read-ahead hints, the collector held until the
first frame, warm-up deferred on JukaHub OS, no boot animation there by
default.
**Where.** `startup.go` (`startupGCHold`, `startupBootSummary`),
`startup_linux.go`, `startup_config.go`, `bootstyles.go` (`bootStyleDefault`),
`perf.go`, `ssh_server.go`, `hardware.go`, `quicksettings.go`, `wakeword.go`,
`startup-bench.sh`.
**Done when.** On a Brick with the JukaHub OS card, a cold power-on reaches
Home with no boot animation; the `startup:` line ends with
`kernel→start=…s start→first-frame=…s` (target: first frame within 0.6 s of
the start); `/tmp/jukahub-first-frame` holds one number; Settings → JukaHub OS
shows **Last boot** matching `boot.log`; a boot animation picked in Settings
still plays. On stock firmware JukaHub starts with no animation by default and
Settings, Quick Settings Wi-Fi, free space and an SSH password login work.
Memory at Home after a cold boot stays normal with the collector held. Run
`JUKAHUB_STARTUP_BENCH=1 JUKAHUB_BENCH_INITTRACE=1` once on the device and
list the slowest package inits; `dmesg | grep 'crng init done'` against
JukaHub's start time.
**Verify.** Record the numbers in the HW log and `docs/PERFORMANCE.md`; file a
FIX task for anything that differs.

### BOOT-HW-2 — Stock menu skipped at boot, no animation by default (P0, S) [HW]
**Why.** Since 2026-11-18 "Start JukaHub at login" skips the stock menu by
default (the one-boot stand-in over MainUI that JukaHub OS mode used), the boot
animation defaults to None everywhere and the startup chime plays over Home.
None of it ran on a device.
**Where.** `autostart.go` (`trimuiAutostartTemplate`, `.autostart-keep-menu`),
`setmodel.go` (`autoStartSkipMenu`), `bootstyles.go` (`bootStyleDefault`),
`bootchime.go` (`bootChimeOverHome`), `player/launch.sh`; `tmp/autostart-sim`.
**Done when.** On a stock Brick and Smart Pro with Start at login: power-on goes
firmware logo → Home with no stock menu and no animation (`autostart.log`: the
stock menu is skipped, the firmware started JukaHub in its place); Wi-Fi,
brightness, volume and sticks are as left (if not, name what the stock menu set
up and confirm that turning **Skip the stock menu at boot** off fixes it); with
the switch off the menu shows and is closed as before and
`.autostart-keep-menu` exists; the chime plays as Home appears; a picked
Cinematic still plays; a JukaHub frozen at start (`kill -STOP`, power held off)
makes the next boot stay on the stock menu. Note power-to-Home against the
previous build.
**Verify.** Record in the HW log with `autostart.log`; file a FIX task for
anything that differs.

### BOOT-HW-3 — Prefetch, first-frame cache and early start, measured (P1, S) [HW]
**Why.** The 2026-11-18 "as fast as possible" pass added bets that only device
numbers can confirm: the recorded boot prefetch, the first-frame label/icon
cache, the remembered device model, the boot guard's background flush, the
Smart Pro image starting JukaHub beside its driver loading, and a 1 MB SD
read-ahead on both images.
**Where.** `internal/bootprefetch`, `bootprefetch_record_linux.go`,
`startup_framecache.go`, `startup_preload.go`, `device_probe_cache.go`,
`bootguard.go`, `startup_displaywait.go`; `os/smartpro/init`, `launch.sh`;
`os/plastron/launch.sh`.
**Done when.** Two cold boots per setup on a Brick (JukaHub OS card and stock)
and a Smart Pro (image and stock): from the second boot the `startup:` line
shows `prefetch=…MB/…ms` (not `none`) and `labels`/`icons` near N/N, the log
does not save a new prefetch list every cold boot, and `start→first-frame`
drops against `data/boot-prefetch.off` + `JUKAHUB_FRAME_CACHE=0` (record both);
screenshots of Home match with and without the frame cache; on stock the
device-probe line says the model was remembered. Smart Pro image: `os.log`
shows JukaHub starting beside the drivers and the display marker, Home has
working pad, sticks and sound, and `EARLY_START=0` / `READAHEAD_KB=0` in
`boot.env` give the comparison; Brick image build prints what runs before
JukaHub at boot — send it with `boot.log`.
**Verify.** Put the numbers in `docs/PERFORMANCE.md`; turn off any bet that
does not help (its switch is in DEVELOPMENT.md) and file a FIX task.

### BOOT-HW-4 — The booter, Home from U-Boot and the stock boot split (P1, S) [HW]
**Why.** The 2026-11-18 late pass added a compiled process 1 for the Smart Pro
image (`jukaboot`: no forks, modules loaded in parallel along their
dependencies, its own timeline and a saved kernel log), the saved Home written
over the card's U-Boot logo so the bootloader shows it, and a stock-firmware
`autostart.log` line that splits each boot into the firmware's and JukaHub's
share. Nothing was built or booted.
**Where.** `player/cmd/jukaboot/`, `os/smartpro/build_image.py`
(`--jukaboot`, `--shell-init`, `--boot-profile`, `--initcall-blacklist`,
`replace_logo`), `bootframe_ubootlogo*.go`, `jukaos_settings.go` (**Show Home
from power-on**), `autostart.go` (`trimuiAutostartTemplate`).
**Done when.** Smart Pro card built with jukaboot: boots to Home with pad,
sticks, sound and Wi-Fi; `os.log` ends with the step timeline and
`boot-dmesg.txt` exists; `BOOTER=0` and `MODULES_PARALLEL=0` in `boot.env` give
the comparison (record power→Home for each); poweroff, reboot and a long Power
press all shut down cleanly. The bootloader shows the full-screen JukaHub logo
on the first boot and the saved Home from the second (log: "U-Boot logo: Home
picture written"), the picture stays through the kernel without a black gap,
and the startup lock puts the image's logo back. One `--boot-profile` card's
`boot-dmesg.txt` names the slowest initcalls for `--initcall-blacklist`. On a
stock Brick and Smart Pro, `autostart.log` has the boot-times line, the card's
mount options and a copy of `runtrimui.sh`.
**Verify.** Record the numbers in `docs/PERFORMANCE.md` and the HW log; keep
`--shell-init` as the default for releases until a jukaboot card has booted
cleanly ten times.

### BOOT-HW-5 — Silent boot and the quiet start, measured (P1, S) [HW]
**Why.** On 2026-11-21 Juka asked for a faster power-on by changing the
bootloader slightly. TrimUI's bootloader passes `loglevel=8`, and the stock
Smart Pro kernel took 4.2 s to reach its root while printing everything to an
unused 115200-baud port. Silent boot rewrites that one value in U-Boot's spare
environment copy; JukaHub's start and a new `S01` stock hook quiet the console
and expedite RCU. Built from code reading only: no Go toolchain, no device.
**Where.** `bootenv.go`, `bootenv_linux.go`, `setmodel.go` (`silentBoot`),
`startup_kerneltune_linux.go`, `startup.go` (`startupBootSummary`),
`os/smartpro/stock_fastboot/jukahub-quietboot` and `stock-fastboot.sh`,
`os/smartpro/build_image.py` (`quiet loglevel=1`).
**Done when.** On the stock Smart Pro: `JukaHub silent-boot` over SSH names
both copies, the one U-Boot loads and `loglevel=8 (passed on: true)`, or says
why it is Unavailable (send that line and the log's `bootenv` lines);
`silent-boot on` writes only the idle copy (two backups in `data/bootenv/`);
after a reboot the last `loglevel=` in `/proc/cmdline` is 1, the row says On and
`dmesg` still has the whole log. Kernel to Home and `dmesg`'s root-mount time
for three boots each way against 2026-11-19's 9.2–9.7 s; `silent-boot off`
brings `loglevel=8` back after the next reboot. `/tmp/jukahub-quietboot.log`
shows the `S01` hook ran; JukaHub's own "kernel tune" log line appears only
when it did not. One Brick on stock firmware too.
**Verify.** Record the numbers in `docs/HARDWARE_VALIDATION.md`; a row
that says No effect gets a FIX task with the `silent-boot` output (the stock
command line carries `loglevel=8` twice: which source adds the second).

### OSBOOT-HW-1 — Boot picture, radios and the Smart Pro command line (P1, S) [HW]
**Why.** The 2026-11-18 OS-image pass puts a saved picture of Home on the
framebuffer before JukaHub starts, starts Wi-Fi and Bluetooth after JukaHub's
first frame, remembers them in `boot.env`, and boots the Smart Pro image with
`quiet` and `bootdelay=0`. Neither image has been booted.
**Where.** `bootframe.go`, `bootframe_linux.go`, `jukaos_native.go`,
`jukaos_settings.go`, `systemcontrol.go`, `bluetooth_bringup.go`, `bootlock.go`;
`os/plastron/launch.sh`, `os/smartpro/init`, `launch.sh`, `build_image.py`,
`dump-board.sh`.
**Done when.** Brick card, second boot: Home appears right after the splash
(`boot.log`: boot frame on screen), JukaHub's first frame replaces it without a
black flash or tearing, and `boot.log` lists U-Boot's bootstage, the launcher,
JukaHub's start and the first frame (power to Home under two seconds is the
goal). A startup lock or a chosen boot animation removes `bootframe.fb` and the
next boot shows the splash. Wi-Fi and Bluetooth switched in JukaHub come back
after a restart as left, Wi-Fi gets an address and the phone companion loads.
A theme change shows in the next boot's picture. Smart Pro card: the same
picture checks over the stock logo, radios after the first frame in `os.log`,
`/proc/cmdline` with `quiet loglevel=1` and no `loglevel=8`. Also confirm plastron's
splash process name and Wi-Fi/Bluetooth init script names.
**Verify.** Record in the HW log with `boot.log`; file a FIX task for anything
that differs.

### SHADER-HW-1 — Shader speed check on a device (P2, S) [HW]
**Why.** The 2026-11-18 rewrite counts page flips of `/dev/fb0` while
RetroArch reports PLAYING and falls back to RetroArch's "Average monitor Hz"
log line after the game; whether the TrimUI GPU driver pans `/dev/fb0` once
per RetroArch frame is unknown.
**Where.** `shader_speed.go`, `shader_speed_linux.go`, `gamediag.go`
(`exited`), `gamepause_ra.go` (`raCoopPause`, `raCoopResume`).
**Done when.** On stock firmware and on the JukaHub OS image: CRT chosen for a
SNES game, 40 s of play, quit → a notice "CRT keeps this game at full speed —
… of 60 FPS." (or the below-speed sentence) and the same in the game's
options; the log's `shader` line says `(frames)` or `(log)`. Opening the
in-game menu twice in the first 30 s does not lower the reading. A heavy
shader's reading agrees with RetroArch's own FPS display. Note whether the
stock `retroarch.cfg` has `video_threaded = "true"` (then the row says why it
is unavailable).
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### PIXEL-HW-1 — Pixel names and Pixel cards on a device (P2, S) [HW]
**Why.** The console looks and their fly-in (2026-11-18) draw 4,000–10,000
small rectangles a frame on the A133P; speed and legibility are estimates.
**Where.** `game_library_pixel.go`, `game_library_pixel_look.go`,
`game_library_ui.go`.
**Done when.** On a Brick and a Smart Pro: Games library → **X** to Pixel
names, then Pixel cards; names fill the buttons and read clearly, no "?" in
console names; moving around the grid, the focused name flies in smoothly in
about half a second and its squares stay inside the grid; coming back from a
console's page plays it again; Reduce motion and Battery Saver put letters in
place; the look survives a restart; a console's list, 3D shelf and cover
column are unchanged.
**Verify.** Record in the HW log; if the flight stutters, drop the unfocused
names' shadow first.

### DROP-HW-1 — Send to the handheld from a phone and a PC browser (P1, S) [HW] [NET]
**Why.** The companion page's drop routing (2026-11-18) decides each file's
folder from the ROM manager's tables, a bounded slice of the file and the
library's names; nothing was uploaded for real.
**Where.** `companion_drop.go`, `companion_drop_route.go`,
`companion_files.go`, the page in `companion.go`.
**Done when.** On stock firmware, CrossMix and NextUI, from Android Chrome,
iOS Safari and a desktop browser: a mixed batch (a `.gba` with no GBA folder
yet, a `.gb`, a PS1 game as `.cue` + two `.bin` tracks, a PSP `.iso`, a `.chd`
→ picker, a one-game `.zip`, a PortMaster zip, `<game>.png`,
`<game> (USA).jpg`, a picture named like no game → closest games, three
`.mp3`, an `.mp4`, `scph5501.bin`) lands where the page said; the cue and its
tracks stay together; the covers show in the Games library (a game with a
downloaded cover asks first); songs appear in Music; one handheld notice; a
second drop of the same file asks before replacing; a 2–4 GB file over Wi-Fi
completes.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### USBNET-HW-1 — Browser over USB on each kernel (P2, S) [HW]
**Why.** The stock Smart Pro 1.1.0 kernel was built without RNDIS and ECM
(hardware log); NCM, the Brick, the Smart Pro S and the plastron kernel are
unchecked. The 2026-11-18 network link joins the USB gadget next to MTP and
adb and was not run.
**Where.** `usbnet.go`, `usbnet_dhcp.go`, `usbnet_linux.go`, `usbmtp.go`,
`usbmtp_linux.go`.
**Done when.** For each model and the plastron image: `mkdir` of
`functions/rndis.x`, `ecm.x` and `ncm.x` in the gadget recorded; with none,
Settings → Network says the kernel has no USB network function and MTP and adb
work exactly as before. Where one exists: Windows 11 and 10 bind the adapter
with no driver prompt, get 10.73.73.2/29 with no gateway and keep their
internet, `http://10.73.73.1` opens the companion and a drop lands; macOS
switches to ECM once after about 20 s; Linux binds without changing the
default route; with adb bound, the endpoints fit or the row says file transfer
only; quitting JukaHub (and a kill -9 then restart) puts the gadget's class,
revision and `os_desc` back.
**Verify.** Record in the HW log and `docs/DEVICES.md`; file a FIX task for
anything that differs.

### LOOK-HW-1 — Looks, Auto, Tune look and Download looks on a device (P1, S) [HW] [NET]
**Why.** The 2026-11-18 looks pass (PT, Auto per console, Download looks,
Tune look, Crop overscan) was written without a device or GitHub access: the
upstream preset paths, PT's `PT_GAME_SYSTEM` knob name, the layout of
SkyWalker541's overlay repo and of RetroArch's shader pack come from search
results. Auto is now the default for everyone who never chose a look.
**Where.** `shader_looks.go`, `shader_looks_tune.go`,
`shader_looks_download.go`, `videofilter.go`, `ratuning.go`,
`ratuning_settings.go`, `shader_speed.go`; docs/LOOKS.md.
**Done when.** On a Brick with Wi-Fi: Settings → Emulation → Download looks
reports PT, CRT and LCD ready plus the overlay count;
`shaders_glsl/handheld/PT_SkyWalker541.glslp` (+ `shaders/…glsl`),
`crt/zfast-crt.glslp`, `handheld/zfast-lcd.glslp` and
`overlays/SkyWalker541/…` exist. With Look = Auto a GB, GBC and GBA game show
PT, an NES game a CRT, a Game Gear game the LCD grid, a PSP/N64 game no
shader; while a GB game runs `<config>/global.glslp` holds the marker,
`#reference` and the PT system line, and it is gone after quitting. Tune look
on Game Boy lists PT's knobs, a changed Pixel effect shows in the game, Reset
look restores it. A GBA game with PT LCD Dot for a minute gives a speed
reading (and the Grid hint below full speed). A SkyWalker541 overlay picked
for Game Boy shows. Crop overscan Off on SNES shows the border where the core
follows it. Running Download looks again keeps a hand-edited file; offline
says so. On a Smart Pro nothing breaks. A card with only `filters/video` and
no shaders gets no look from Auto.
**Verify.** Record in the HW log; file a FIX task for anything that differs
(wrong upstream path, knob name, overlay picture paths).

### HOMEBREW-HW-1 — Get Homebrew against itch.io on a device (P1, S) [HW] [NET]
**Why.** Get Homebrew (2026-11-18) parses itch.io's RSS feeds, `data.json`,
uploads, owned keys and download sessions from field names found by search;
the tag slugs per console are guesses; nothing was run against itch.io.
**Where.** `homebrew_store.go`, `homebrew_store_itch.go`,
`homebrew_store_install.go`, `homebrew_store_key.go`,
`homebrew_store_images.go`, `homebrew_store_ui.go`; docs/HOMEBREW.md.
**Done when.** On a Smart Pro and a Brick with Wi-Fi: Start → Games → Get
Homebrew loads games for every console chip (note any empty chip: a dead tag
slug in `hbConsoles`), covers appear, Newest / Top rated / Free load when
picked, search narrows. A key added from the phone card and by typing shows
"Signed in as …". A free GB Studio game, a free NES game delivered as a zip
and a Pico-8 `.p8.png` cart each land in the right Roms folder with a clean
name and show on their shelf with their cover; Play starts them. A bought game
installs through its download key; a paid game not owned only offers Buy on
itch.io. Cancelling a download leaves no part file; Delete keeps saves. An
edited `installed.json` date brings an Update badge and Update keeps the file
name. Offline the saved lists show with their date. The Windows build installs
into the PC's Roms folder.
**Verify.** Record in the HW log; fix field names or slugs that differ.

### GBC-HW-1 — Game Boy Color carts on the Game Boy shelf (P2, S) [HW]
**Why.** The 2026-11-18 cartridge-header check starts Color carts named `.gb`
with the Game Boy Color emulator and the ROM manager moves them with their
saves; RetroArch's save folders with both sort options on and the stock
launchers were not tried.
**Where.** `gbcsort.go`, `game_library.go` (`findGameLaunchersFor`),
`romm_extras.go`, `game_rename.go` (helpers reused).
**Done when.** On a Brick and a Smart Pro: a Color-only (flag 0xC0) and a
dual-mode (0x80) homebrew as `.gb` in Roms/GB, one of them zipped, start from
the Game Boy shelf in color (log "is a Game Boy Color cartridge on the Game
Boy shelf"); a plain Game Boy game still uses its own emulator. The library
shows its notice once. ROM manager → Move Color games carries the ROM, its
`.srm`/`.rtc`/states (beside the ROM and in RetroArch's saves, also with "sort
by content directory" on), cover, play time, Buttons/Turbo and favourites to
Game Boy Color, and the game keeps its save. A same-named game already there
stays put.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### DISC-HW-1 — Multi-disc combine, start disc and in-game Disc swap (P1, S) [HW]
**Why.** The 2026-11-18 automatic disc pass, the options sheet's Disc row
(RetroArch's `.ldci` record, its folder order with both save sorts on is from
memory) and the in-game Disc row (DISK_EJECT_TOGGLE / DISK_NEXT / DISK_PREV)
were not run.
**Where.** `disctools.go`, `romsetup.go` (`romScanStartHook`),
`discpage.go`, `game_options_ui.go`, `gamemenu.go`.
**Done when.** Loose `Game (Disc 1).chd` … `(Disc 3).chd` in Roms/PS and a
`Game/Disc 1/…cue+bin`, `Game/Disc 2/…` folder set each show as one game after
a rescan (log "disc games: Added …"); an existing `.m3u` is untouched. Options
→ Disc → "Starts on disc 2 of 3" boots disc 2 with the stock PCSX-ReARMed
launcher (if RetroArch logs "Failed to set initial disc", record its path next
to the logged `image_path`). In the game, hold Select + L2 → Disc → Right → A
swaps discs (RetroArch shows the new disc); reopening the options shows the
last disc played. On a Smart Pro (720 px) the in-game menu with every row on
scrolls and shows its arrows.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### CTRL-HW-1 — Game buttons, turbo and in-game cheats on RetroArch (P1, S) [HW]
**Why.** The 2026-11-18 one-launch remap files, RetroArch Turbo Fire settings
(1.21+ keys and mode from release notes; ≤1.20 needs the pad's own button
number) and the in-game cheat walk over network commands were not run; whether
1.21+ turbo reads the button before or after a remap is unknown.
**Where.** `racontrols.go`, `racontrols_remap.go`, `racontrols_turbo.go`,
`racontrols_ui.go`, `gamemenu_cheats.go`, `gamemenu.go`.
**Done when.** On a Smart Pro and a Brick with the stock or CrossMix
RetroArch (note its version): Game buttons for → SNES → Swap A and B swaps A
and B in an SNES game from the library but not in RetroArch's menu; after
quitting the `.rmp` and `data/remaps-pending.json` are gone and a firmware
launch is unswapped; a remap saved in RetroArch's Quick Menu is left alone and
the Buttons row says so. Turbo A, Normal: holding A autofires, B does not;
Swap A and B + Turbo A autofires the physical A; `retroarch.cfg`'s turbo keys
are back after quitting (on ≤1.20 note whether `input_player1_a_btn` is a
number or the TrimUI profile was used). Two cheats on in the options, then the
in-game Cheats list switches one off and another on: RetroArch shows each
change, the effect follows, and the options sheet matches after quitting; try
the last cheat of a 100+ list.
**Verify.** Record in the HW log with `retroarch.cfg` and the autoconfig
profile; file a FIX task for anything that differs.

### LOOK-HW-2 — Letter scrubbing and Menu colour on a device (P2, S) [HW]
**Why.** The 2026-11-18 letter scrubbing polls the L2/R2 axes for its held
repeat and the Menu colour override sits in the shared theme painter; neither
ran on a pad.
**Where.** `game_library_scrub.go`, `game_library_ui.go`, `shoulders.go`,
`navrepeat.go`, `menucolour.go`, `design.go`.
**Done when.** On a Brick and a Smart Pro, a console with 100+ games: L2/R2
jump by letter in the list, 3D shelf and cover column, Right jumps in the list
and column, a held trigger keeps jumping and stops the moment it is released,
the letter bubble reads clearly and fades without stutter, the footer shows
the L2/R2 hint, and Year / Rating / Play time / Last played show their own
labels; with the Emulators page or a popover open over the library the
triggers do nothing. Menu colour: each colour on a dark theme and on Light
(rings visible, accent text readable), kept across theme presets, a console
look, Battery Saver and a reboot; Theme's own brings the theme's colour back.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### RAGLIBC-HW-1 — RetroArch on JukaHub's glibc 2.39 on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "use the latest private glibc instead of the TrimUI
glibc to run retrocores, so we can get latest updates". `player/ra_glibc.go`
starts RetroArch through the private runtime's loader (docs/PRIVATE_GLIBC.md
"RetroArch") in the game's own mount namespace — a wrapper bind-mounted over
`ra64.trimui`, the loader seen as `<data>/private-glibc/ra/ns/v<i>/ra64.trimui` —
for cores that need a newer glibc (setting **RetroArch's C library**, Automatic
by default), and the load check then accepts buildbot nightlies up to
GLIBC_2.39 / GLIBCXX_3.4.33. Written without a Go toolchain or a device: the
wrapper, setup script and probe parser were run off-device (x86-64 host
loader, dash, busybox 1.30, a fake `mount`); unsharing, the bind mounts on the
card, the PowerVR/Mali libraries and `ra64.trimui` under glibc 2.39 never ran.
**Where.** `ra_glibc.go`, `ra_glibc_linux.go`, `compat.go`
(`coreLoadEnvWith`), `emulatorupdates.go` (`fetchCoreUpdate`), `corefix.go`
(`coreFixScan`), `game_library.go`, `netplay.go`, `raemu.go`,
`gamelaunch_firmware.go`; `data/private-glibc/ra/probe.json`, the JukaHub log
(`emulators` lines "starts on JukaHub's glibc", "stays on the firmware's").
**Done when.** On a stock Smart Pro (then a Brick and a Smart Pro S): 1. With
no runtime installed and the GLIBC_2.35 buildbot mGBA active, a GBA game
toasts "Getting JukaHub's newer C library…", then starts by itself with sound,
saves and the in-game menu; the log shows the probe passing and "RetroArch
starts on JukaHub's glibc 2.39"; `/proc/<pid>/comm` of the game is
`ra64.trimui`. 2. `probe.json` holds `"ok": true` for `ra64.trimui`; a
restart's core look logs no `core fix:` line for mGBA. 3. The daily pass
installs buildbot nightlies it used to refuse for GLIBC (`core updates: N
installed`), and the N64/PSX GPU cores still pass LIB-27's probation. 4.
RetroArch's tile opens its menu and loads a nightly core. 5. **Firmware's
only** brings back kept builds at once (toast), **JukaHub's glibc 2.39** runs a
core that does not need it. 6. A game from MainUI with a 2.35 core still fails
(expected; noted in the HW log). If the probe refuses: record its `why` here;
if the namespace cannot be made (not root, no `mount`), record the setup
script's `jukahub:` line from the launcher output. 7. On a card whose runtime
is the pinned glibc-only fallback (`data/private-glibc/glibc-2.39-0ubuntu8.12/`,
no `current`), Emulators → ppsspp (`GLIBCXX_3.4.30`) installs the complete
runtime first (log: "installed private runtime") and then the core; with
ports.ubuntu.com blocked the toast says the complete one did not install, and
Emulators → mgba then installs JukaHub's `cores-aarch64` build (toast "·
JukaHub's build for TrimUI") (2026-11-22).
**Verify.** Record in the HW log; update LIB-26 with what the daily pass now
installs.

### RAGET-HW-1 — RetroArch installed on a card without one (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-22: "Download latest version of working RetroArch (if
it's not installed) … make sure GLIBC is private library that we use and that
it's compatible with Trimui Smart Pro and other Trimui devices".
`player/raget.go` installs TrimUI's newest `ra64.trimui` for the model from
`trimui/assets_*` on GitHub, reading the SD base package's zip through HTTP
range requests (`raget_remote.go`), checks each build with the firmware's and
the private loader, and starts it on glibc 2.39 (`ra_glibc.go`,
`raGetOwnsLaunch`). Written without a Go toolchain, without access to GitHub
(the package names, formats and folder layout come from TrimUI's release
notes and setup guides; the Smart Pro's own packages are `.7z`, which are
skipped) and without a device.
**Where.** `raget.go`, `raget_remote.go`, `raget_ui.go`, `ra_glibc.go`
(`raGlibcPlanFor`), `emulators.go`, `raemu.go`, `gamelaunch_firmware.go`; the
JukaHub log (`emulators` lines "RetroArch packages:", "RetroArch install:",
"RetroArch installed:"), `RetroArch/.jukahub-retroarch.json`.
**Done when.** On a stock Smart Pro (then a Brick, a Brick Pro and a Smart Pro
S), with the card's `RetroArch` folder renamed: 1. About a minute and a half
after start the install bar shows "Getting RetroArch for the Smart Pro"; the
log lists the packages found ("RetroArch packages: … in <asset> (N files,
size)") and the build taken ("… starts on the firmware's glibc and on JukaHub's
2.39"); the toast names the version. 2. A GBA game starts from Games with
sound, saves and the in-game menu, and the log says "RetroArch starts on
JukaHub's glibc 2.39 … JukaHub installed this RetroArch". 3. Where the build
came from another model's package (a Smart Pro takes the Brick's or Brick
Pro's), the picture fills the screen and the buttons are right; record what is
not. 4. The Emulators page's RetroArch tile opens RetroArch's menu, and reads
"installed by JukaHub". 5. With the card nearly full, the install leaves out
`.retroarch/assets` and RetroArch still starts. 6. Record, per model, the
asset names and sizes, how long the install took and how much was downloaded;
a package the log calls unreadable (a compression method, a 7z) gets a FIX
task.
**Verify.** Record in the HW log.

### CORESURVEY-HW-1 — The buildbot survey and the Emulators page on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-23: "Make sure all emulators that get updated work on
glibc...we want to show the emulators that Trimui devices can run...many of
emulator cores are asking for glibc 4.29 (GLIBCXX_3.4.29, the C++ library's
version)...". `player/native/survey-cores.py` (the survey job of
`.github/workflows/retroarch-cores.yml`) publishes what every buildbot
linux/aarch64 core needs as `buildbot-needs.json` in the `cores-aarch64`
release; `player/corecompat.go` judges each core against the handheld and
JukaHub's glibc, hides the ones that load neither way (unless JukaHub builds
them), refuses their builds before a download, and installs the complete
runtime ahead (`startCoreRuntimeEnsure`). The script was run against local
ELF files and a local HTTP server only; the workflow never ran, and nothing was
compiled or run on a device.
**Where.** `survey-cores.py`, `retroarch-cores.yml` (job `survey`),
`corecompat.go`, `coretrimui.go`, `emulators.go`, `emulatorupdates.go`
(`fetchCoreUpdate`, `coreRefusedError`), `emuupdatecheck.go`,
`emulatorautoupdate.go`; the JukaHub log (`emulators` lines "core survey:",
"JukaHub's glibc 2.39 with GCC 14's C++ library … is installed ahead"),
`data/performance/buildbot-needs.json`.
**Done when.** 1. Actions → RetroArch cores → Run: the survey job's summary
lists how many cores were read and the GLIBC/GLIBCXX they need; the release
carries `buildbot-needs.json`. 2. On a stock Smart Pro with the setting on
Automatic, three minutes after start the log says the complete runtime is
installed ahead (or why not). 3. Emulators: no tile for a core the summary
lists under desktop OpenGL; PPSSPP's tile says "runs on JukaHub's glibc 2.39"
and installs; a core JukaHub builds whose buildbot build cannot load says
"JukaHub's build for TrimUI" and installs that one without a buildbot download
in the log. 4. With the setting on Firmware's only, the cores that need the
runtime disappear from the list. 5. Record any core the page offers that then
fails to start, with its survey entry.
**Verify.** Record in the HW log.

### PSPPIC-HW-1 — A PSP game's picture on the A133P handhelds (P1, S) [HW]
**Why.** Juka, 2026-11-23: "I tried psp, but the screen was black and only
played game sound" — the third black PSP screen on his Smart Pro (2026-11-14,
11-15). JukaHub's PPSSPP now ends its shader chain with an opaque copy of the
picture (`ppsspp_opaque.go`; the A133P's framebuffer is ARGB and PPSSPP keeps
the PSP's alpha), looks at the panel for the first 45 seconds of a PSP game
(`ppsspp_picture.go`) and, when the picture never reaches it, restarts the game
with the device to itself through the screen hand-off (`hakux_handoff.go`).
Written without a Go toolchain and without a device: the cause is inferred from
two ports on the same chip, not seen.
**Where.** `ppsspp_opaque.go`, `ppsspp_picture.go`, `ppsspp_managed.go`
(`ppssppSeedHome`, `ppssppNoteForceQuit`, `ScreenAlone*`), `hakux_handoff.go`,
`ppsspp_managed_ui.go`, `emuspeed_ppsspp.go`; the JukaHub log (`ppsspp` lines
"picture check:", "opaque picture:"), the memstick's `PSP/shaders/jukahub-opaque.*`.
**Done when.** On a stock Smart Pro, then a Brick: 1. A PSP game started from
Games shows its picture beside JukaHub, and the log says "picture check: … is
on the panel …"; PPSSPP's Display layout & effects lists *JukaHub opaque
picture* as the last shader. 2. With the shader removed from the chain by hand
in `ppsspp.ini` before a start, record what the panel shows and the log's
verdict, display layers and framebuffer format. 3. With PSP games set to the
screen to themselves on the PPSSPP card, a PSP game shows its picture, PPSSPP's
Menu → Exit brings JukaHub back on the game in the library, and its play time
is counted. 4. A game that does show (step 1) never gets "picture check:"
warnings, a black loading screen of up to 45 s included. 5. Record any PSP game
whose picture breaks with the opaque shader on.
**Verify.** Record in the HW log.

### AUDIT-HW-1 — The Audit toolkit's installs on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: Aircrack-ng did not install on the handheld and
nothing said why, Nikto stopped at "Required module not found: XML::Writer", and
Python and Perl should arrive without a press. The fixes (the handheld recipe
that leaves out adapter helpers, hwloc and Python; unpack and wrapper repairs in
Linux packages; the repair before Terminal or Version; the failure sheet and log
line; the quiet-moment `auditinterp` updater) were written without a Go
toolchain or a device, and no log ever showed why Aircrack-ng failed.
**Where.** `secaudit.go`, `auditscene.go`, `audit_interp_auto.go`,
`linuxpkg_install.go`, `linuxpkg_cli.go`, `linuxpkg_runtime.go`,
`linuxpkg_ui.go`; docs/TOOLBOX.md, docs/LINUX_PACKAGES.md; the JukaHub log
(`audit`, `linuxpkg` and `emulator updates` lines).
**Done when.** On a stock Smart Pro, each step holds or a FIX task names what
differs:
1. Tools → Audit toolkit → Aircrack-ng → Install logs `linuxpkg: installed
   aircrack-ng: N packages, …` with "Leaving out what aircrack-ng does not need
   here: …"; **Version** answers with aircrack-ng's help text; `apt show
   aircrack-ng` in the Terminal prints "Installed without: ethtool, hwloc, iw,
   python3, rfkill, usbutils, wireless-tools (not needed here)". On a failure,
   record the sheet's reason, `audit: aircrack-ng install failed: …` and any
   `linuxpkg: <pkg>: N files left out` or `N program wrappers could not be
   written` line.
2. A Nikto installed before this build: Terminal or Version logs `audit: nikto
   is missing libxml-writer-perl, libjson-perl; installing through Linux
   packages before it opens`, then `nikto -Version` prints without "Required
   module not found".
3. First start after the update, idle or on the charger within 15 minutes,
   Emulator updates on Install automatically: `audit: installing python3, perl,
   … for the Audit toolkit at a quiet moment` and `emulator updates: Python and
   Perl installed`; the next day's pass reports it current.
**Verify.** Record in the HW log.

### AUDIT-HW-2 — The Audit toolkit's eight new tools on a device (P2, S) [HW] [NET]
**Why.** Juka, 2026-11-20: "Add more professional audit tools to audit toolkit
(that are open source and can be installed on Trimui Smart Pro)". testssl.sh,
sslscan, ssh-audit, tcpdump, YARA and Binwalk install from Ubuntu's arm64
packages, OSV-Scanner and Gitleaks from their GitHub arm64 releases. It was
written without a Go toolchain or a device, and three details rest on Ubuntu's
packaging as documented, not as seen: testssl.sh's command name (`testssl`) and
data folder (found through `TESTSSL_INSTALL_DIR`), and tcpdump's switch to a
`tcpdump` user (hence the sheet's `-Z root` advice).
**Where.** `secaudit.go` (catalog, `auditLinuxPkgProgram`, Remove keeping
testssl.sh's `bash`), `auditscene.go`, `linuxpkg_runtime.go`
(`TESTSSL_INSTALL_DIR`), `toolbox.go` (`osv-scanner_SHA256SUMS`), `tools.go`
(`allowedToolRepos`); docs/TOOLBOX.md, docs/LINUX_PACKAGES.md.
**Done when.** On a stock Smart Pro, each step holds or a FIX task names what
differs:
1. Install, then **Version**, on each Linux-packages tool answers with the
   tool's own report: `testssl --version` with no "No cipher mapping file
   found!" question, `sslscan --version`, `ssh-audit -h` (version on its first
   line), `tcpdump --version`, `yara --version`, `binwalk --help`.
2. OSV-Scanner and Gitleaks install with "Matched the SHA-256 the project
   publishes for this release." on the sheet, and **Version** prints their
   versions.
3. In the Terminal: `testssl` against a TLS host you own finishes; `tcpdump -Z
   root -i wlan0 -c 20 -w /mnt/SDCARD/t.pcap` writes a file Wireshark opens,
   and the same line without `-Z root` stops with "Couldn't find user
   'tcpdump'" (so the advice stays true); `ssh-audit` against an SSH host you
   own prints its report; `osv-scanner scan source -r` on a project folder and
   `gitleaks dir` on a folder finish.
4. **Remove** on testssl.sh leaves `bash` installed (`apt list --installed`)
   and puts the row back under the install group.
**Verify.** Record in the HW log.

### VSPEED-HW-1 — Video and music speed on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "Media player 2x (or changing to any other speed) is
not working". A speed change now restarts both ffmpeg decoders with
`setpts`/`atempo` (and `-skip_loop_filter`/`-skip_frame noref` above 1× on a
handheld), and the music position counts in the track's own time. The filter
chains were run with a desktop ffmpeg 4.4, never on the A133P.
**Where.** `videoplayer.go`, `avsync.go`, `partyscene.go`, `audioresume.go`;
docs/CONTROLS.md "Video player"; the JukaHub log (`video:` lines).
**Done when.** On a Smart Pro, each step holds or a FIX task names what differs:
1. A 720p YouTube video at 1.5×, 2× and 0.5× keeps picture and sound in sync
   with normal pitch, the clock running at that rate (`video: speed 2x at N s`;
   debug `First frame received: … speed=2x`). Record whether "the decoder cannot
   keep up" and "decoding lighter above 1x from now on" appear (and on which
   video; 720p60 is the likely one), whether 2× looks choppy after that, and
   whether 2× stalls on weak Wi-Fi where 1× did not (both decoders read the
   stream at twice the rate).
2. Paused, a speed change stays paused and Play resumes at the new speed from
   the same point; a ±10 s seek and a quality change at 1.5× stay at 1.5×; L2/R2
   to the next video keeps 1.5×; another video opened from the grid starts at
   1×.
3. A Live TV channel answers a speed press with "Live streams play at 1×";
   hosting a Watch Party over a 2× video drops it to 1×.
4. Music or an audiobook at 1.5× (keyboard `]`): the clock runs at 1.5× and a
   resume after leaving the track lands where it stopped.
**Verify.** Record in the HW log.

### MEMSWAP-HW-1 — Automatic memory freeing and swap on a device (P1, S) [HW]
**Why.** Juka, 2026-11-19: "only 140 MB left on Juka and AI doesn't launch
sometimes". The staged free-up before the offline AI and speech, the idle
low-memory watcher and **Extra memory (swap)** (zram, or a swap file on the
card) were written without a device; whether the stock 4.9 kernel has zram, or
takes a swap file on the card's file system, is unknown.
**Where.** `memfree.go`, `memfree_linux.go`, `memswap.go`, `memswap_linux.go`,
`adaptive.go`, `offline_ai.go`, `offline_ai_server.go`, `stt_stream.go`;
docs/LOCAL_AI.md; the JukaHub log (`mem` lines).
**Done when.** On a stock Smart Pro (then CrossMix or NextUI), each step holds
or a FIX task names what differs:
1. Two minutes after start, the `mem: swap:` line names the path taken: zram on
   `/dev/zram0`, a 256 MB swap file written and on at swappiness 10, the kernel
   refusing a swap file (file system and kernel named), or swap already on.
   Record `cat /proc/swaps`, the top of `/proc/meminfo` and `ls /sys/block |
   grep zram` from SSH.
2. Asking the offline AI with about 140 MB free (after leaving a game, or with a
   stray RetroArch running) logs `mem: freed N MB (…) for offline AI: …` and the
   AI answers; record which step mattered, and any `mem: closing apps left
   running in the background: …` line with its toast.
3. "Hey Juka" heard during a game closes no app and drops no cache.
4. Three minutes or more on Home: `mem: freed … for low memory` lines come at
   most every 3 minutes (15 after a run that freed little); record whether the
   200 MB floor is too eager.
5. Settings → Games & emulators → **Extra memory (swap)** off and on: swap goes
   off (or stays on until restart when memory is full) and comes back, with no
   stutter.
**Verify.** Record in the HW log.

### DOLPHIN-HW-1 — GameCube and Wii in the Dolphin core on a device (P1, M) [HW] [NET]
**Why.** Juka, 2026-11-19: "Improve Dolphin or Gamecube support on Trimui
devices". `dolphin*.go` plays GameCube and Wii in RetroArch's Dolphin core on
the Smart Pro S (on by default) and, with **GameCube & Wii → Try anyway**, on
the A133P models: a game's first start installs the buildbot's linux/aarch64
`dolphin_libretro` and `Dolphin.zip` (Sys), replaces a core from before the
libretro/dolphin#462 fix (2026-10-23), seeds `dolphin_shader_compilation_mode`
"3" and `dolphin_wait_for_shaders` "disabled", switches the override's video
driver after a start that could not draw, and boosts the game (xboxboost.go).
2026-11-20, Brick Pro: the Emulators page's install said the buildbot core
"cannot load on this firmware: needs GLIBCXX_3.4.32" (built with GCC 13), and
the private glibc was never tried because a new core's load check looked at no
cores folder (`coreUpdateCheckTarget` fixes that for every install). Since then
the core comes from JukaHub's own TrimUI build first (`cores-aarch64`,
`build-cores.sh` row `dolphin`: GCC 13 from the toolchain PPA, libstdc++ linked
in, a libudev stub for hidapi), the buildbot's on the private glibc second; a
press on a Dolphin tile runs `dolphinStartInstall` (Automatic on an A133P
becomes Try anyway); the daily core updates skip Dolphin
(`dolphinManagesCore`); and `coretrimui.go` hides published cores that cannot
work on TrimUI. 2026-11-21 (Juka: "a few fps" on the Smart Pro):
`dolphin_speed.go` writes, per launch through raScopeApply, Fast (dual core,
shader mode 3, native resolution, skipped duplicate frames, deferred EFB
copies; no run-ahead, preemptive frames or rewind) or Fastest (plus
`dolphin_efb_access_enable` off, `dolphin_cpu_clock_rate` 0.50, `video_vsync`
off); Automatic is Fastest on the A133P. 2026-11-21 (Juka: "I can't find
GameCube or Wii folders"): `dolphin_folders.go` makes `Roms/GC` and `Roms/WII`
(once per card at start-up and after a setup, whenever missing on a press of
the console's tile or when the setting is turned on) and always lists a
GameCube and a Wii tile on the Emulators page. 2026-11-22, a tester's Brick Pro
(CrossMix-OS): "Could not install GameCube games — the 2026-11-21 build was
refused here before: … needs libusb-1.0.so…" — JukaHub's own build was not
there (not published, or not yet when the list was read), and the buildbot's
nightly, a desktop build, links libusb and libudev. The same noon (Juka: "On
Trimui devices (and Trimui Brick Pro) libusb-1.0.so ...make sure to fix it and
make sure we can run Dolphin on Trimui Smart Pro") the morning's Ubuntu copies
(Linux packages, only inside the updater) gave way to stand-ins JukaHub writes
itself (`coredevstub.go`: an aarch64 ELF writer exporting exactly what the core
imports, with its symbol versions; libusb answers LIBUSB_ERROR_NOT_SUPPORTED,
libudev NULL, libbluetooth -1; libevdev gets none) into
`private-glibc/devlibs/`, made sure of by the updater, before every launch
(`raGlibcCoreNeeds`, `raGlibcPlanFor` — also a core RetroArch's own updater
installed, which used to start on the firmware's glibc without them), by the
start's look (`coreFixFindBroken`) and for RetroArch's own menu on JukaHub's
glibc (folder last on its path). The x86-64 twin of the same layout was loaded
by glibc 2.35's loader (versioned + unversioned imports, LD_BIND_NOW); the
aarch64 file was only read back (`readelf`, the Go test). A build these do not
complete is refused with what is still missing named (libevdev alone, say); the
setup's error names each source's outcome and a press re-reads
`cores-aarch64`'s index; `build-cores.sh` hands CMake the libudev stub
(`LIBUDEV_LIBRARY`) and removes the root's `libudev.so` link. Written without a
Go toolchain
or a device: whether either build loads and draws on the TrimUI firmware (its
DT_NEEDED, glibc), the option values the rebased core takes, whether
the CI build passes with its switches (target `dolphin_libretro`, output
under `Binaries/`, hidapi linking `udev`), which context the stock
RetroArch gives it (GLES 3 or Vulkan), Dolphin.zip's layout, the numeric option
values and the RetroArch log lines `dolphinVideoFailureLine` matches are from
web reports, not from a run.
**Where.** `dolphin.go`, `dolphin_setup.go`, `dolphin_video.go`,
`coretrimui.go`, `coredevlibs.go`, `coredevstub.go`, `ra_glibc.go`
(`raGlibcCoreNeeds`, `raGlibcPlanFor`), `corefix.go` (`coreFixFindBroken`),
`dolphin_speed.go`, `dolphin_folders.go`, `emulators.go`
(`deviceEmulatorEntries`), `emulatorupdates.go` (`fetchCoreUpdate`),
`emusystems.go` (`emuDesktopSystemFor`), `emupicker.go`, `game_library.go`,
`gamediag.go`, `xboxboost.go`, `compat.go`, `romsetup.go`,
`player/native/build-cores.sh`, `.github/workflows/retroarch-cores.yml`;
`data/dolphin.json` (`coreFrom`, `coreGlibc`),
`<system>/dolphin-emu/Sys`, `config/dolphin-emu/dolphin-emu.{opt,cfg}`; the
JukaHub log (`library` lines "GameCube:", `emulators` lines "GameCube:").
**Done when.** On a stock Smart Pro S (then a Smart Pro or the Brick Pro with
Try anyway), each step holds or a FIX task names what differs:
0. Actions → RetroArch cores for TrimUI → Run with `dolphin`: the runner
   publishes `dolphin_libretro.so.zip`, whose BUILDINFO names g++-13 and
   NEEDED without libstdc++ or libudev, glibc 2.31 at most; or record the
   first error of its log (the target name, a CMake switch, the libudev stub).
1. With no core, A on a GameCube `.rvz` shows the install card ("GameCube
   games"), then the game starts by itself; `cores/dolphin_libretro.so` and
   `<system>/dolphin-emu/Sys/GameSettings` exist; record the core's
   `readelf -d` NEEDED lines, newest GLIBC/GLIBCXX, and whether the log says it
   started on JukaHub's glibc. On the Brick Pro, X on the Emulators page's
   Dolphin tile with GameCube & Wii on Automatic installs it, the setting
   reads Try anyway after, and the toast and the log line
   `GameCube: Dolphin core … from …` name the source (JukaHub's build once
   step 0 published it, the buildbot's on JukaHub's glibc before).
2. The game reaches gameplay and plays for 2 minutes without freezing (the
   #462 hang came at about 10 s); record the in-game FPS (RetroArch's FPS
   counter) for one light game (e.g. a 2D or menu-heavy title) and one 3D game,
   with Xbox boost on and off.
3. `config/dolphin-emu/dolphin-emu.opt` was written once with the two seeded
   keys, and RetroArch's Core Options show "Asynchronous (Skip Drawing)" (or
   record what the build shows instead); a second start leaves the file alone.
4. Which video driver the stock `retroarch.cfg` uses, and whether Dolphin
   draws with it; set the other one by hand and record whether it draws too.
   If a start fails to draw, the card says which driver JukaHub switched to
   and the next start uses it.
5. A Wii game (`.wbfs`) starts with the Wiimote on the pad; quitting through
   the in-game menu returns to JukaHub with no hang.
6. GameCube & Wii → Off: the GC folder shows "not supported" with the reason,
   a game's start says how to turn it on; back to Automatic restores play.
7. The Brick Pro's Emulators page lists no `cemu`, `citra`, `play`, `kronos`
   or `test…` tile (record the tile count before and after this build), an
   installed core of any kind is still listed, and a press of L1 (Update all)
   leaves `dolphin_libretro.so` as it was.
8. GameCube speed on a Smart Pro, with one light and one 3D game: RetroArch's
   FPS counter under Dolphin's own, Fast and Fastest; the log line "GameCube
   speed for this launch" lists what was set; Core Options show those values
   during the game and the old ones after it. Record which option values the
   build refused (RetroArch's log), and any game Fastest breaks (the skipped
   EFB reads, the half-speed CPU) for a per-game exception.
9. The folders and tiles, on a card with neither folder: the first start makes
   `Roms/GC` and `Roms/WII` (log `GameCube: made …`; `dolphin.json` lists
   `folders`). The Emulators page shows a GameCube and a Wii tile ("A sets it
   up" before the core is in, the game count after) and no "GC — no emulator
   found" row; A on the Wii tile with no Wii games opens the library with the
   "No Wii games yet" toast. Delete `Roms/WII` and restart: it stays deleted,
   and A on the Wii tile makes it again. With a launcher folder of your own in
   `Emus/GC`, the GameCube tile is not listed and its games start there.
10. The buildbot fallback on a Smart Pro and the Brick Pro, while
    `cores-aarch64` has no Dolphin (or with it removed from the card's choice
    by hand): X on the GameCube tile installs the core with no Linux packages
    download — the log says `device libraries for RetroArch on JukaHub's
    glibc: JukaHub's stand-ins libusb-1.0.so.0 (N functions), libudev.so.1
    (…)`, and `data/private-glibc/devlibs/` holds those files (a few KB each)
    and `devlibs.json` — or it is refused naming what is still missing
    (record it: that is what else the buildbot links). A game's start logs
    `RetroArch starts on JukaHub's glibc … device libraries from …` and
    reaches gameplay with no "libusb-1.0.so.0: cannot open shared object
    file" in RetroArch's log (record any "Failed to init libusb" or panic
    text Dolphin shows); a SNES game right after logs no device libraries.
    Also: a Dolphin core put in by RetroArch's own Online Updater starts from
    JukaHub's library and from RetroArch's menu (Load Core → Dolphin) on
    JukaHub's glibc. With neither source giving a build, the card names both
    ("JukaHub's build for TrimUI: …; the libretro buildbot: …").
**Verify.** Record in the HW log.

### PS2-HW-1 — PlayStation 2 in the ARMSX2 core on a Smart Pro (P1, M) [HW] [NET]
**Why.** Juka, 2026-11-20: "Add support for PS2" (a developer runs an ARMSX2
fork on a Brick Hammer, light games at full speed), then "Focus on getting it
to work on Trimui Smart Pro...not on windows". `ps2*.go` plays PS2 games in
RetroArch's ARMSX2 core on every TrimUI model (on by default): a game's first
start installs ARMSX2's GitHub release asset `Linux-arm64-libretro-core.tar.zst`
(stable first, else nightly; SHA-256 from GitHub's `digest`), the matching
`resources` folder from the same release's `Linux-arm64-SDL-handheld.tar.zst`
into `<system>/pcsx2/resources`, copies a valid BIOS into `<system>/pcsx2/bios`,
seeds `armsx2_renderer` "OpenGL" (no Vulkan driver) and `armsx2_fast_boot`,
moves the renderer OpenGL → Software after a start that could not draw, and
boosts the game (xboxboost.go). Written without a Go toolchain or a device:
the asset names, that the core archive holds only the .so and the handheld
archive a `resources/` tree with `GameIndex.yaml`, the core's DT_NEEDED and
glibc/GLIBCXX needs (PR #716 links its dependencies statically; built on
Ubuntu 22.04), which GPU context the stock GLES RetroArch gives it on the
PowerVR GE8300, the option values ("OpenGL", "enabled") and the log lines
`ps2VideoFailureLine` matches are from web reports, not from a run.
**Where.** `ps2.go`, `ps2_setup.go`, `ps2_video.go`, `emusystems.go`
(`emuDesktopSystemFor`), `emupicker.go`, `game_library.go`, `gamediag.go`,
`emulatorinstall.go`, `emulatorupdates.go` / `emuupdatecheck.go` (buildbot
updates skip `armsx2`), `xboxboost.go`, `compat.go`, `romsetup.go`;
`data/armsx2.json`, `<system>/pcsx2/{bios,resources}`,
`config/ARMSX2/ARMSX2.opt`; the JukaHub log (`library` lines "PS2:", `emulators`
lines "PS2:").
**Done when.** On a stock Smart Pro (then a Brick or Hammer, and a Smart Pro
S), each step holds or a FIX task names what differs:
1. With no BIOS anywhere, A on a PS2 `.iso` says where to put one and downloads
   nothing. With `scph*.bin` in `BIOS/PS2`, A shows the install card ("PlayStation
   2 games"), then the game starts by itself; `cores/armsx2_libretro.so`,
   `<system>/pcsx2/resources/GameIndex.yaml` and the BIOS copy in
   `<system>/pcsx2/bios` exist. Record the release tag, both archive sizes, the
   core's `readelf -d` NEEDED lines, newest GLIBC/GLIBCXX, and whether the log
   says RetroArch started on JukaHub's glibc.
2. The BIOS boots into the game (fast boot) and plays for 2 minutes; record the
   FPS (RetroArch's counter) for a light game (e.g. Road Trip, Virtua Fighter
   10th Anniversary) and a 3D game, with Xbox boost on and off.
3. `config/ARMSX2/ARMSX2.opt` was written once with `armsx2_renderer` and
   `armsx2_fast_boot`; Quick Menu → Core Options shows OpenGL (or record the
   value spelling the build uses instead). Record whether OpenGL ES draws on the
   GE8300; if a start fails to draw, the card names the renderer JukaHub moved
   to and the next start uses it (Software draws, slowly).
4. Settings → PS2 emulator says the release; A says "already the newest" or
   installs a newer one (old core kept as `.bak`). The Emulators page's daily
   core pass leaves `armsx2_libretro.so` alone.
5. Both sticks, L2/R2 and L3/R3 reach the game; saving to the memory card and
   a save state survive a restart; quitting through RetroArch returns to
   JukaHub with no hang.
6. PlayStation 2 → Off: `Roms/PS2` shows "not supported" with the reason, a
   game's start says how to turn it on; back On restores play.
**Verify.** Record in the HW log.

### GODOT-HW-1 — The Godot port and Godot games on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "add Godot... There is Godot Trimui Smart Pro port
somewhere so use that". `godot.go` installs the TrimUI Smart Pro Godot port
(github.com/ogregorio/trimui-tsp-godot-port, Kiwi's `Emus/GODOT` as the
fallback) from the repository zip and lists `Roms/GODOT` (`.pck`) as the Godot
shelf, but GitHub could not be read from the build machine: the port's layout,
its `launch.sh`, its engine file names and which Godot versions it plays are
unverified, and nothing was compiled or run.
**Where.** `godot.go`, `emulators.go` (Godot tile), `emulatorinstall.go`,
`gamediag.go` (`godotDiagIssues`), `romsetup.go`; docs/GODOT.md; the JukaHub
log (`godot` lines).
**Done when.** On a stock Smart Pro (then a Brick and a Smart Pro S), each step
holds or a FIX task names what differs:
1. Emulators shows one **Godot** tile (no separate GODOT row); X installs the
   port: `Emus/GODOT/launch.sh` is there and runnable, `Roms/GODOT` exists, the
   tile turns to **Godot port** (with engine versions when the names carry
   them), and the stock menu lists GODOT too. Record the zip's top-level
   layout and `ls -lR Emus/GODOT`.
2. A Godot 3 `.pck` (with and without a `.png`/`.gptk` beside it) shows on the
   Godot shelf with its picture, starts from Games, plays with the pad and
   returns to JukaHub; the log has `godot: <file>: made with Godot x.y.z`.
3. A Godot 4 `.pck` and a renamed non-pack file: the launch report names the
   version mismatch / "not a Godot game pack".
4. X on the installed tile updates in place and keeps a file of your own in
   `Emus/GODOT` and `Roms/GODOT`.
**Verify.** Record in the HW log.

### JAVA-HW-1 — Java programs on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "can we add Java or ability to run Java files into
JukaHub?" `javart*.go` installs Eclipse Temurin (8, 11, 17, 21 or 25, JRE or
JDK) from Adoptium's GitHub releases, checks each archive against its published
SHA-256 and starts it once (`java -version`) before it is used; `javarun.go`
runs .jar, .java and .class files from Files; `javaterm.go` adds the Java
commands and typed input to the Terminal; `javaui.go` is Tools → Java. Nothing
was compiled or run.
**Where.** `javart.go`, `javart_install.go`, `javart_windows.go`,
`javarun.go`, `javaterm.go`, `javaui.go`, `terminal.go`; docs/JAVA.md; the
JukaHub log (`java` lines).
**Done when.** On a stock Smart Pro (then a Brick) and on Windows, each step
holds or a FIX task names what differs:
1. Tools → Java → Install (Java 25 JRE): note download size and time; it
   unpacks onto the FAT/exFAT card, the probe passes on the stock glibc 2.33,
   and the screen shows version, size and folder. Pull the power once during a
   download: the next start removes the leftovers.
2. Terminal: `java -version`; `java install 25 jdk` replaces the JRE only after
   its probe; `java update` with nothing newer; `java remove`.
3. Files → Java → Hello.java: note the start time of the source launcher; type
   a name with the on-screen keyboard and Start, then with a keyboard (Enter,
   Ctrl+D). Also a console .jar, a .class in a package folder, a .jar in
   Downloads (second press) and a Swing .jar on the handheld (an honest sheet;
   the headless run fails plainly).
4. `free -m` while a Java program runs beside JukaHub.
5. Windows: a system Java is found (registry, Program Files); JukaHub's own
   install works with Defender on; a Swing jar opens with javaw and JukaHub
   stays; Hello.java reads input in the Terminal with no console window.
**Verify.** Record in the HW log.

### J2ME-HW-1 — J2ME phone games on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "add J2ME and both". `j2me.go` and
`j2me_install.go` make `Roms/J2ME` (.jar, .kjx) a J2ME shelf in Games, played
in RetroArch with FreeJ2ME-Plus. On the handheld the core and its jar come
from JukaHub's own `cores-aarch64` release (`player/native/build-cores.sh`,
`.github/workflows/retroarch-cores.yml`), which has never run. Nothing was
compiled or run.
**Where.** `j2me.go`, `j2me_install.go`, `emupicker.go`, `game_library.go`,
`romsetup.go`, `files_open.go`, the cores workflow; docs/J2ME.md.
**Done when.** First run "RetroArch cores for TrimUI" (Actions, or a push to
main): its summary lists `freej2me_plus_libretro.so.zip` and a
`<date> <sha256> freej2me_plus-lr.jar freej2me_plus` line. Then on a stock
Smart Pro (then a Brick):
1. On a card without Java, a 2D J2ME game in `Roms/J2ME` is listed under J2ME;
   a .jar in `Roms/PORTS` or `Apps` is not.
2. Starting it offers Java ("J2ME games run on Java"); after that install the
   J2ME card fetches the core and jar and the game starts; the log names the
   pair and the Java it runs on.
3. In the game: picture, sound, text (headless Java may lack fonts), D-pad,
   OK/fire and both soft keys; Core Options lists FreeJ2ME-Plus's options.
4. Quitting from RetroArch and from JukaHub's in-game menu returns to JukaHub
   with no java process left.
5. A second start downloads nothing; a deleted jar comes back on the next
   start; an offline first start says so plainly.
6. A MIDlet .jar in Downloads plays from Files; with RetroArch on JukaHub's
   private glibc (`ra_glibc.go`) a J2ME game still starts.
7. Windows: without a FreeJ2ME core the message says where to get it; with
   the core and jar in RetroArch the game plays.
**Verify.** Record in the HW log.

### READY-HW-1 — Game Ready on a device and a PC (P1, S) [HW] [WIN]
**Why.** Juka, 2026-11-19 (the "smarter library" list): show the fix before a
game starts. `gameready.go` / `gameready_ui.go` hold a library launch through
`gamelaunch_hooks.go` when a BIOS the core cannot start without, a disc file,
the core or the emulator is missing, or a Local play pad is gone. Written
without a Go toolchain or a device; the cold-cache cost of the check on the
main thread was never measured.
**Where.** `gameready.go`, `gameready_ui.go`, `gamelaunch_hooks.go`,
`gamediag_bios.go`, `gamediag.go`, `main.go` (routing), `biosdoctor.go`;
`data/game-ready.json`; the JukaHub log (`library` lines "Game Ready").
**Done when.** On a stock Smart Pro (then a Brick) and on Windows, each step
holds or a FIX task names what differs:
1. Without `scph5501.bin`, a PS1 game in Beetle PSX or SwanStation shows the
   card with the folder read from `retroarch.cfg`; A opens BIOS Doctor and B
   from it returns to the library and the game page; with the file back, Y
   starts the game. PCSX ReARMed starts with no card.
2. A `.cue` whose `.bin` was renamed: "Missing disc file" chip and card; X
   plays anyway, a second X turns the card off for that game (More → Ready
   check → X turns it back on).
3. The power-on resume of a game with a missing BIOS starts with no card.
4. Two pads in Local play, one unplugged, a new game started: the card names
   the missing player; A removes the seat and starts the game.
5. Windows: a RetroArch core with a missing BIOS shows the card; with every
   controller unplugged only a "Good to know" line appears; a mouse click on
   the card's pills works and its release does not reach the library.
6. Log the time of a cold check (first Play after boot) for a PS1 `.m3u` and
   an N64 game on the Brick's card: well under 100 ms, or warm the cache when
   the library opens.
**Verify.** Record in the HW log.

### SHELF-HW-1 — ScummVM and DOS shelves on a device (P1, S) [HW]
**Why.** `shelf_*.go` make ScummVM and DOS folder games one game each, write
`.scummvm` files for recognised adventures, register the ScummVM and DOS
layouts, set DOSBox Pure's options per game and add Files → Add to DOS games.
The ScummVM ids, the `scummvm_mapper_*` options (ScummVM 2.8+), some DOSBox
Pure option values and CrossMix's ScummVM launcher are from web sources, not a
run.
**Where.** `shelf_common.go`, `shelf_scummvm.go`, `shelf_scummvm_ids.go`,
`shelf_dos.go`, `shelf_dos_settings.go`, `shelf_dos_import.go`,
`shelf_controls_card.go`, `romshelf.go`, `files_manager.go`;
`data/shelves.json`; docs/GAME_LIBRARY.md, "ScummVM and DOS shelves".
**Done when.** On a stock Smart Pro and a Brick (then CrossMix), each step holds
or a FIX task names what differs:
1. Beneath a Steel Sky and Flight of the Amazon Queen in their own folders in
   `Roms/SCUMMVM`: opening Games writes `<folder>.scummvm` (log "ScummVM:
   wrote …"), each lists once and starts with the firmware's ScummVM launcher
   and with `scummvm_libretro` started directly. A CrossMix-imported game
   (top-level shortcut + `GAMES/<Game>`) lists once and nothing is written into
   `GAMES/`.
2. On the Brick the D-pad moves the pointer, A/B click, L1 slows it, Start
   opens the ScummVM menu, Select the keyboard (record the core version; on a
   core without `scummvm_mapper_*` record what the buttons do).
3. A DOS game folder starts its own program, not SETUP; More → DOS settings →
   another start program, PC speed and memory apply to the next start and the
   core options file is back to its old values after RetroArch closes.
4. Select opens DOSBox Pure's on-screen keyboard first on both models.
5. Files → Y → Add to DOS games with a ~100 MB folder: progress toasts, it
   lists once with the right start program, a second add makes "<name> (2)";
   pulling the card mid-copy leaves no half game listed after the next start.
6. The controls card shows once before a shelf game's first start, from the
   game page and from Recents.
**Verify.** Record in the HW log.

### LAYOUT-HW-1 — Control layouts on a device (P1, S) [HW]
**Why.** `ctrllayout_*.go` merge a layout's buttons into racontrols' one-launch
remap and set DS touch options for the launch. MAME 2003-Plus's classic order,
FinalBurn Neo's Neo Geo order and RetroArch's stick-direction remap values
(16–23, the Brick's D-pad stylus) are from secondary sources; melonDS DS gets no
options (names unconfirmed); keyboard keys (`keys` in a layout) are not sent
because RetroArch's keyboard remap format was not confirmed.
**Where.** `ctrllayout.go`, `ctrllayout_builtin.go`, `ctrllayout_store.go`,
`ctrllayout_apply.go`, `ctrllayout_ui.go`, `racontrols.go`,
`racontrols_remap.go`, `game_options_ui.go`; `data/game-layouts.json`,
`data/control-layouts/`; docs/CONTROLS.md, "Control layouts".
**Done when.** On a stock Smart Pro (then a Brick), each step holds or a FIX
task names what differs:
1. Street Fighter Alpha 3 in FinalBurn Neo, then MAME 2003-Plus, with Arcade
   6-button and Buttons = Swap both for Arcade: punches on Y/X/L1, kicks on
   B/A/R1; the remap appears only for that start and is gone after exit.
2. KOF '98 in FinalBurn Neo with Arcade 4-button: Y=A, B=B, X=C, A=D.
3. A DS game in melonDS with DS stylus on the Smart Pro: the right stick moves
   the stylus, R2 taps, L2 swaps; the core's `.opt` is restored after exit.
   Then DeSmuME, then the Brick (D-pad stylus).
4. A user layout file with a bad entry: the entry is skipped and named in the
   log; the layout shows in the picker.
5. Keyboard keys: a remap with `input_player1_key_a = "13"` (with and without
   `input_libretro_device_p1 = "3"`) in DOSBox Pure — if it types Enter, send
   layout `keys` in `ctrllayout_apply.go` and drop the "not sent yet" note.
**Verify.** Record in the HW log.

### MANUAL-HW-1 — Game manuals on a device and a PC (P2, S) [HW] [WIN]
**Why.** `gamemanual*.go` find a game's manuals and guides, open them in the
Reader, text viewer, picture viewer or Browser by hiding the library and
bringing it back, and add a Manual page to the in-game menu. Nothing was run.
**Where.** `gamemanual.go`, `gamemanual_ui.go`, `gamemanual_add.go`,
`gamemanual_ingame.go`, `gamemenu.go`, `files_manager.go`;
`data/game-manuals.json`, `data/game-manuals/`; docs/GAME_LIBRARY.md, "Game
manuals".
**Done when.** On a stock Smart Pro (then a Brick) and on Windows, each step
holds or a FIX task names what differs:
1. A PDF next to a ROM, one in `Roms/<SYS>/manuals/`, an ES-DE
   `downloaded_media` manual and a folder of scanned pages: the chip shows and
   each opens; B returns to the game page with the list (no library flash over
   the Reader, no hidden library left behind; also Home from inside the
   Reader).
2. Files → Y → Use as this game's manual copies a file in; a guide link opens
   in the Browser; X twice removes them.
3. Select + L2 → Manual in a game with a text guide and a layout chart: the
   page fits 1280×720 and 1024×768, a 64 KB guide wraps without a visible
   stall, and the 3-minute cap closes it cleanly.
4. A 40-page scanned PDF and a 300-page manual opened from the game page: RAM
   and time to the first page on the Smart Pro.
5. A console with 3,000 games: the chip appears within a second of opening a
   page.
**Verify.** Record in the HW log.

### LAB-HW-1 — Game Lab measurements on a device (P1, S) [HW]
**Why.** `gamelab*.go` run a game in two setups and measure frames (the shader
speed flip counter), sound glitches (RetroArch/ALSA log lines) and battery
draw (sysfs every 5 s). Current RetroArch builds may log no underruns at all,
and which battery files the AXP2202/AXP717 expose is unverified.
**Where.** `gamelab.go`, `gamelab_measure.go`, `gamelab_ui.go`,
`gamelab_share.go`, `gameoptions.go` (`gameLabOptions`), `shader_speed*.go`;
`data/game-lab/`; docs/GAME_LIBRARY.md, "Game Lab".
**Done when.** On a stock Smart Pro (then a Brick and a Smart Pro S), each step
holds or a FIX task names what differs:
1. Run A on a RetroArch game with no shader look, then with one: fps matches
   the shader speed check's reading; a game paused from the in-game menu does
   not drag fps down.
2. Battery: watts land between 1.5 and 6 W; Battery saver against Performance
   on a PSP game shows a clear gap; on the charger the cell says "Not measured
   (charging)".
3. A PSP or N64 game at Battery saver: record whether `gamediag/retroarch.log`
   or `launcher.log` has any underrun line; if never, say so on the Sound row
   up front.
4. Run B at Battery saver, quit, open Game options: the game's own profile is
   unchanged, and a normal start runs with it. A Lab run held by Game Ready and
   then confirmed is still measured.
5. Use B, then Share: the export file validates when copied into
   `game-lab/import` on another device and shows under Shared setups.
**Verify.** Record in the HW log.

### PARTY-HW-1 — Party Mode with phones on a device (P1, S) [HW]
**Why.** Juka, 2026-11-19: "JukaHub Party Mode could be a signature TrimUI
feature … I'd start with Guess the Game." `partymode*.go` host a game show on
the LAN: phones join from a QR code and play Guess the Game against crops of
the library's covers. Nothing was compiled or run: the phone page was only
tried in jsdom against a scripted server, and the room's rules only read.
**Where.** `partymode.go`, `partymode_net.go`, `partymode_page.go`,
`partymode_ui.go`; docs/PARTY_MODE.md; the JukaHub log (`partymode` lines).
**Done when.** On a stock Smart Pro with two phones (one iPhone, one Android)
on the same Wi-Fi, each step holds or a FIX task names what differs:
1. Tools → Party Mode shows the QR code, the code and the address; both phones
   join by scanning; the lobby lists them with their colours; the lobby's
   count of covered games matches the library's covers.
2. A 5-round game: the crops zoom out on time on the handheld and both phones
   (a phone lags the handheld by well under a second), a buzz pauses the
   reveal everywhere, right/wrong/timeout score as documented, the result and
   winner screens show, and sounds play on the handheld and the phones.
3. The handheld seat: Y seats it, A buzzes, the D-pad and A pick; B and X do
   nothing while it picks.
4. A phone locked for 30 s shows as away and comes back on unlock; a reload
   keeps its seat; B on the handheld asks, then every phone shows that the
   party is over. The idle screen does not start while phones play.
5. With the handheld's hotspot on (after HW-12): both QR codes work, a phone
   joins the hotspot and the game with no other network.
6. Covers in `.webp` and a broken cover: the round takes another game.
**Verify.** Record in the HW log, with the phones' browsers and versions.

### HANDOFF-HW-1 — Save handoff between a PC and a handheld (P1, S) [HW] [WIN] [NET]
**Why.** `savehandoff*.go` compare one game's saves with a paired JukaHub over
Save sync's paired calls and send or get them with one press. Not run on two
devices; different RetroArch core folders on the two sides (mGBA on the PC,
gpSP on the handheld) are copied side by side, not merged.
**Where.** `savehandoff.go`, `savehandoff_ui.go`, `ssync.go`, `peernet.go`;
docs/GAME_LIBRARY.md, "Save handoff".
**Done when.** With a Windows PC and a stock Smart Pro paired, each step holds
or a FIX task names what differs:
1. Play a GBA game on the PC (battery save and a state); on the Smart Pro the
   chip says "Saves: newer on <PC>", More → Saves shows both sides with the
   PC's state picture, Get from copies, a re-check says the same on both, and
   Save Manager lists the snapshot. Then the other way (Send to).
2. Play the same game on both without syncing: "Changed here and on …" with
   Keep this one under each; both devices then hold the kept save under its
   name and the other as "(from <device>)"; RetroArch loads the kept one.
3. Wi-Fi off on the PC, a game running there, no pairing, Nearby devices off:
   each says so without freezing the page; with no pairing A opens Paired
   devices.
4. A card with many states and no Save sync hash cache: the page stays
   responsive and the chip appears later; after a minute of play with the other
   device reachable, the hint appears once (not again within 10 minutes).
**Verify.** Record in the HW log.

### VMEMO-HW-1 — Voice Memos and the microphone test on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "Voice Memos … The TrimUI microphone needs a
recording test first." `voicememo*.go` record 16 kHz WAVs through
`voicemic.go`, play them back and hand them to a phone; nothing was compiled
or run, and which capture device each model opens was never seen.
**Where.** `voicememo*.go`, `voicemic.go`, `bgmusic.go`; docs/EVERYDAY_APPS.md;
the JukaHub log (`mic` lines, "Voice Memos microphone test").
**Done when.** On a Smart Pro, a Brick and a Smart Pro S, each step holds or a
FIX task names what differs:
1. The first visit's Microphone test names the device that opened, hears a
   voice and plays the 3-second sample back at a sensible level through the
   speaker and through a Bluetooth headset; the log says which device and
   whether codec switches were set.
2. A 10-minute memo has no gaps, the right length, about 19 MB, and plays on a
   PC; music paused for it resumes, and "Hey Juka" listens again within a few
   seconds of the stop.
3. Send to phone downloads the WAV on an iPhone and an Android phone on the
   same Wi-Fi.
4. Pulling the power mid-recording leaves a "Recovered memo" on the next visit.
**Verify.** Record in the HW log.

### VOICE-HW-1 — The Voice tab's room on a public Mumble server (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: a free voice option "as a tab … 1 click to
connect … so I don't have to sign up". `chatvoice_mumble*.go` speak Mumble
over one TLS connection (Opus via the pure-Go gopus module) to
`mumble.gamemania.org:42069` and meet in "Private User-Channels/JukaHub".
The protocol was written from Mumble.proto and the legacy voice format,
gopus's API from its docs, and the voice gate's levels were never tried on a
TrimUI microphone. The 2026-11-20 Windows build compiled, connected and was
refused the room ("does not let guests open rooms here"); the same day's fix
steps into Private User-Channels before asking, then tries a lasting channel
and a one-time certificate registration — still unrun.
**Where.** `chatvoice_mumble.go`, `chatvoice_mumble_proto.go`,
`chatvoice_opus.go`, `chatvoice_scene.go`, `chatvoicechannel.go`;
docs/VOICE_CHANNEL.md; the JukaHub log (`chat` lines: "voice server …").
**Done when.** After `player\update-deps.bat` and a build, on a stock Smart Pro
and a Windows PC, each step holds or a FIX task names what differs:
1. A click on Chat's Voice tab (and L1/R1 onto it, then A) joins; the status
   reads Connected; a desktop Mumble client on the same server sees the
   JukaHub channel under Private User-Channels with both JukaHub users in it;
   the first join made `voice/mumble-identity.pem` in the data folder (the
   RSA key took how long on the Smart Pro?); the log's `voice: asking to open
   …` line shows the first, temporary request worked from inside Private
   User-Channels (no `voice server refused` line, no registration).
2. Speech both ways is clear with under half a second of delay; the green
   dots follow who speaks; silence sends nothing (the Mumble client shows the
   JukaHub user as not talking); with the handheld's speaker on, the PC user
   does not hear their own voice back.
3. Mute shows "muted" on the Mumble client; Leave removes the user and the
   server deletes the empty channel; a Wi-Fi drop reconnects by itself.
4. People → Y on two names posts an invitation; Join Voice on the PC opens the
   same private `JukaHub-…` room; the copied `mumble://` address opens it in
   Mumla on an Android phone.
5. Two devices with the same Chat name both get in (the second with a number).
**Verify.** Record in the HW log with the server's Mumble version.

### PAINT-HW-1 — Paint on a device (P2, S) [HW]
**Why.** Juka, 2026-11-19: "Paint + screenshot markup … Draw pixel art with the
D-pad or stick, circle something in a screenshot, undo mistakes, and save a
PNG as a wallpaper or game cover." `paint*.go` do that with the D-pad, the
left stick and a mouse; nothing was compiled or run.
**Where.** `paint.go`, `paint_canvas.go`, `paint_save.go`, `paint_ui.go`;
`paintStickAxis` in `controller.go`; Mark up in `imageviewer.go`;
docs/EVERYDAY_APPS.md.
**Done when.** On a stock Smart Pro (then a Brick), each step holds or a FIX
task names what differs:
1. On a 64×64 canvas the left stick creeps one pixel when barely pushed and
   crosses the canvas in under a second at full push, with no drift; a held
   D-pad repeats, then speeds up; L2/R2 zoom and the global keys still work.
2. A 1280×720 screenshot opened from the picture viewer (Start) takes 30
   strokes and full-screen fills while JukaHub's memory grows by no more than
   about 40 MB, and undo still works after going Home and back.
3. Use as wallpaper on a 32×32 drawing is crisp, centred and kept after a
   restart; Use as game cover on a Select + R1 screenshot preselects that game
   and the library shows the new cover at once.
4. A power cut a minute into a drawing: Continue brings it back.
**Verify.** Record in the HW log.

### TASKS-HW-1 — Tasks & reminders on a device (P2, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "Tasks and reminders: quick checklists tied to the
existing calendar and alarms, with easy typing from the phone companion."
`tasks_*.go` and `companion_tasks.go` were checked statically and the phone
card in jsdom only.
**Where.** `tasks_store.go`, `tasks_remind.go`, `tasks_ui.go`,
`tasks_sheet.go`, `tasks_cal.go`, `companion_tasks.go`; docs/EVERYDAY_APPS.md.
**Done when.** On a Smart Pro (or a Brick) and a PC, each step holds or a FIX
task names what differs:
1. Tasks typed with the on-screen keyboard and from the Phone Companion's
   Tasks card (the QR from Tasks → From your phone closes when the first task
   arrives) show on both sides at once.
2. A task 3 minutes ahead with Remind me and Ring as alarm: the toast, the bell
   entry, the alarm ringing with the task's name, and RetroArch's message
   during a library RetroArch game all arrive; ticking a daily task moves it
   and its alarm to tomorrow; deleting a task removes its alarm.
3. Booting with Wi-Fi off past a reminder's time shows no stale reminder once
   network time arrives.
4. The calendar popover's day marks and agenda rows read well at 1280×720,
   1024×768 and on the Brick; a power cut right after a change keeps
   `tasks.json` (or its `.bak`).
**Verify.** Record in the HW log.

### SWZIP-HW-1 — Stopwatch over a game and Create ZIP on a device (P2, S) [HW]
**Why.** `stopwatch*.go` keep a stopwatch and a countdown running under games
and speak through RetroArch's message line, the in-game menu and Quick
Settings; `files_zip.go` makes ZIPs in Files. Neither was compiled or run.
**Where.** `stopwatch.go`, `stopwatch_ui.go`, `gamemenu.go`, `qs_draw.go`,
`files_zip.go`, `files_manager.go`; docs/EVERYDAY_APPS.md.
**Done when.** On a Smart Pro (or a Brick), each step holds or a FIX task
names what differs:
1. A 1-minute countdown started before a library RetroArch game shows
   RetroArch messages at 1 minute and 10 seconds and "Time's up" at zero, on
   time, and the chime is heard over the game's sound.
2. In the in-game menu (Select + L2) the Timer row counts down live, A pauses
   it, and with the stopwatch running Right takes a lap; the Quick Settings
   button shows the running time.
3. Create ZIP of a ~1 GB ROM folder (mixed `.chd`, `.gba`, `.png`): the row's
   percent moves, the handheld stays usable, a cancel leaves no
   `.jukahub-zip-*.part`, and a finished ZIP extracts back with Extract. A
   folder of more than 4 GB of incompressible files on a FAT32 card ends with
   the "grew past 4 GB" message.
**Verify.** Record in the HW log.

### HOMEBREW-HW-2 — TIC-80 and RPG Maker from Get Homebrew on a device (P2, S) [HW] [NET]
**Why.** `homebrew_tic80.go` and `homebrew_easyrpg.go` add TIC-80 carts and
RPG Maker 2000/2003 folder games to Get Homebrew. The `tic80` and `easyrpg`
cores come from Get more cores; neither was loaded on a TrimUI, and where the
EasyRPG core keeps saves and the RTP was not seen.
**Where.** `homebrew_store*.go`, `homebrew_tic80.go`, `homebrew_easyrpg.go`,
`emumorecores.go`, `romsetup.go`; docs/HOMEBREW.md.
**Done when.** On a stock Smart Pro (then a Brick), each step holds or a FIX
task names what differs:
1. A free TIC-80 cart installs into `Roms/TIC` (or the card's own TIC-80
   folder), starts in the tic80 core and plays with the pad; a browser-only page
   shows "Plays in the browser only" and no Install.
2. A free RPG Maker 2003 game installs into `Roms/EASYRPG/<Title>/`, lists
   once, starts in the easyrpg core; A confirms, B cancels; saves land as
   `SaveXX.lsd` in the game folder; Update and Delete keep them.
3. A game without `FullPackageFlag=1` with the RTP in
   `RetroArch/system/rtp/2003`: graphics and music load. A Japanese zip with
   Shift-JIS names either installs or fails with a clear reason.
**Verify.** Record in the HW log.

### SDMOVE-HW-1 — Move to a new SD card on a device (P1, S) [HW]
**Why.** The SD-card move (`sdmove*.go`, 2026-11-19) was written without a Go
toolchain or a device. It mounts USB card readers, reads sysfs and raw block
devices, and drops the page cache so the verify pass really reads the new card;
only a TrimUI can confirm those.
**Where.** `sdmove.go`, `sdmove_copy.go`, `sdmove_check.go`, `sdmove_linux.go`,
`sdmove_ui.go`; docs/SD_CARD_MOVE.md.
**Done when.** On a Smart Pro (then an A133P device), each step holds or a FIX
task names what differs:
1. A ~30 GB card moves to an exFAT card in a USB-C reader and ends "ready";
   `sha256sum` of a few files matches on both cards.
2. Stop and Start again reuse the copied files; pulling the reader stops the
   run cleanly.
3. A FAT32 target lists the files of 4 GB or more and copies only after "Copy
   without them".
4. Another partition of the old card is refused as a destination.
5. After the swap the check reports "all N files are here" once and keeps its
   result; a byte flipped in one file on the new card (same date) is reported
   as different by Check again.
6. A JukaHub OS image card is detected as a system card; only a
   `JUKAHUB`-labelled volume on an imaged card is accepted as its destination.
**Verify.** Record in the HW log with the move's list of unreadable files;
`top` shows the check thread at nice 19.

### PROF-HW-1 — Profiles and Guest on a device (P1, S) [HW]
**Why.** Profiles (`profiles_*.go`, 2026-11-19) point RetroArch's save folders
at the person's folder through journaled `retroarch.cfg` edits and swap the
live settings; none of it ran on a TrimUI.
**Where.** `profiles_store.go`, `profiles_switch.go`, `profiles_saves.go`,
`profiles_ui.go`; hooks in `extras.go`, `core.go`, `controller.go`, `main.go`,
`startmenu.go`, `hotreload.go`, `factoryreset.go`, `bkup.go`,
`savemanager_ui.go`; docs/PROFILES.md.
**Done when.** On a Smart Pro and a Brick:
1. A second profile's GBA and SNES saves and states land in
   `<data>/profiles/<id>/`, the owner's are untouched, and `retroarch.cfg`
   holds the owner's folders again after the game exits and after a power cut
   mid-game (stock and CrossMix RetroArch; check RetroArch does not rewrite the
   folder paths when it saves its config on exit).
2. Continue, playtime, trophies, theme and language follow each switch; the
   owner's `jukauser.json` never gains another person's values.
3. Guest leaves nothing behind after a switch and after a power cycle, and no
   trophy pops up.
4. PPSSPP's first start as a non-owner shows the shared-saves notice; the
   top-bar chip fits on every Home layout.
5. "Ask who is playing at start" shows the picker before anything launches.
**Verify.** Record in the HW log.

### SAFE-HW-1 — Standalone emulators closed safely at low battery and power off (P1, S) [HW]
**Why.** `sessionsafe*.go` (2026-11-19) closes standalone emulators with
SIGTERM/SIGINT and trusts each emulator's own save-on-exit option; every recipe
was researched, none ran on a TrimUI.
**Where.** `sessionsafe.go`, `sessionsafe_recipes.go`, `sessionsafe_linux.go`;
`lowbattery.go` (`protectLowBattery`), `quickresume.go`;
docs/SESSION_SAFETY.md.
**Done when.** On a Smart Pro (and a Brick or Smart Pro S where the emulator
exists), for JukaHub's managed PPSSPP (with and without
`SaveStateOnExit = True`), the firmware PPSSPP, DraStic
(`enable_autosavestate = 1`), Flycast standalone (auto save and load on),
Mupen64Plus standalone, a PortMaster port, Pico-8 and Moonlight:
1. `kill -TERM <emulator pid>` makes the emulator exit within 9 s, the launch
   script's post-game lines still run, and for the save-on-exit ones a new
   state file appears where the recipe looks (record the real folder and
   extension).
2. DraStic's behaviour on SIGTERM is recorded; if it writes no auto state its
   recipe moves to the quit-only class.
3. An in-game save (SRAM, memory card) written just before the signal is
   intact.
4. The 10% warning buzzes over the game and is in the bell afterwards; a
   program from the Apps screen gets no warning.
5. At 3% the log shows the emulator being closed, then sync, then suspend.
6. Power off after sleeping 15 min with Flycast (auto save on) powers off and
   the next power-on resumes the game; with Mupen64Plus it sleeps again; at 5%
   or below it closes and powers off.
**Verify.** Record in the HW log with `data/sessionsafe-last.json` and the
state files' times.

### A11Y-9 — Spoken navigation on a device (P1, S) [HW]
**Why.** Narration of the focused control (`narrate*.go`, 2026-11-19) was
written without a device; its latency, interruption and coverage are unmeasured
on the handhelds' Cortex-A53/A55.
**Where.** `narrate*.go`; hooks in `setui.go`, `setsheet.go`, `tbxui.go`,
`startmenu.go`, `game_library_ui.go`, `vkui.go`, `core.go`, `extras.go`,
`main.go`; docs/ACCESSIBILITY.md.
**Done when.** On a Smart Pro and a Brick:
1. Holding L1+R1 for 3 s plays rising tones and says "Narration on" and the
   focus; again, falling tones. L1 and R1 still work normally in games and on
   the on-screen keyboard.
2. A fresh item with Piper warm speaks in under 600 ms, a cached one under
   150 ms (the debug log line `narrate: made "…" in …` times each piece).
3. Holding Down through 30 Settings rows reads only the last; a press
   mid-sentence cuts it off.
4. A question to the voice orb while browsing: narration never talks over the
   answer.
5. Coverage: Settings rows of every kind, option sheets, confirmations, the
   Start menu, Home, the Games grid and list, the top bar, keyboard keys and
   toasts.
6. A running game: silence, no extra Piper process, memory as before; 2.5× is
   still intelligible.
7. Fresh card, Wi-Fi off: turning narration on explains; with Wi-Fi back the
   voice downloads and "Narration voice ready" is said.
**Verify.** Record in the HW log; `ls narrate-cache | wc -l` stays at 900 or
fewer.

### LIB-HW-1 — The library index on a large card (P1, S) [HW]
**Why.** The library index (`libindex*.go`, 2026-11-19) was written without a
device: open times, inotify on the TrimUI kernels' FAT/exFAT and the background
CRC pass are untested.
**Where.** `libindex.go`, `libindex_watch_linux.go`, `libindex_crc.go`,
`romsetup.go` (`scanROMLibraryFrom`), `game_library_ui.go`,
`hometabs_games.go`; docs/GAME_LIBRARY.md.
**Done when.** On a Brick or Smart Pro with a 5,000-game card:
1. The time from A on Games to a full console grid, first and second open
   after boot, is recorded on an older build and on this one; after a restart
   the library appears in under a second.
2. The log line "library index: N games, M of K folders read" shows M = K on
   the first look and M near 0 later.
3. A ROM copied over USB (MTP) into `Roms/GBA` with the library open appears
   within about 10 s without moving the selection; deleting it removes it.
4. Rebuild library index rebuilds and toasts the count; the fingerprint pass
   pauses while a game plays and stops below 30% battery off the charger.
5. A card edited on a Windows PC shows its new games on the next start; a power
   cut during a scan leads to a rebuilt index, not a crash.
**Verify.** Record in the HW log with the size of `data/library-index.db`.

### NAS-HW-1 — Network drives on a device and a PC (P1, S) [HW] [WIN] [NET]
**Why.** Network drives (`nas*.go`, 2026-11-19) were written without a NAS, a
handheld or Windows.
**Where.** `nas*.go`; docs/NETWORK_DRIVE.md.
**Done when.** On a Smart Pro and on Windows:
1. An SMB share (Windows 11 and a Synology or QNAP), a WebDAV folder and an
   SFTP server are added; Find shared folders lists the NAS; SFTP asks to trust
   the key once and refuses a changed key.
2. Console folders map by themselves; games show on their shelves with the
   network badge and the count reads "· K games on network drives".
3. A .cue+.bin PS1 game, an .m3u multi-disc game and a DOS folder game
   download and play; Wi-Fi off mid-download, then Try again, resumes; record
   whether a file changed on the NAS between tries is noticed.
4. Wi-Fi off dims the network games with Offline within 30 s.
5. Remove downloaded copy deletes only the copied files and keeps the save.
**Verify.** Record in the HW log with `nas-cache.json`, `nas-downloads.json`
and no `.part` file left after a finished download.

### SMB-HW-1 — The SMB client against real servers (P1, S) [HW] [WIN] [NET]
**Why.** `smb2*.go` (2026-11-19), JukaHub's own read-only SMB2/3 client, was
written without a Go toolchain or a server. Its MD4, NTLMv2, AES-CMAC and key
derivations match published test vectors (checked in Python), but sign-in,
signing, encryption and the share list are unproven on the wire.
**Where.** `smb2.go`, `smb2_proto.go`, `smb2_ntlm.go`, `smb2_sign.go`,
`smb2_rpc.go`, `smb2_test.go`; `nas_smb.go`.
**Done when.** `go test ./player -run SMB2` passes, and on a TrimUI and a PC
the network drive lists shares, browses and copies a 100 MB+ game from Samba 4
or Synology DSM 7 (with a user and as guest), Windows 11 24H2 (signing
required) with a local account, a Windows share with "Encrypt data access"
(SMB 3.1.1, AES-GCM) and macOS file sharing. An SMB1-only server shows the SMB1
message, a wrong password says so, and a mistyped user name on Samba does not
silently sign in as guest (or the page says it did).
**Verify.** Copy speed is at least 80% of Explorer's on the same Wi-Fi; the log
line `smb: connected: SMB 3.1.1, signed true, …` matches the server; CodeQL's
MD5/RC4 alerts in `smb2_ntlm.go` are dismissed (NTLM requires them).

### CTL-HW-1 — Controller Mode over USB, Wi-Fi and DSU on a device and a PC (P1, S) [HW] [WIN] [NET]
**Why.** Controller Mode (`ctlmode*.go`, 2026-11-19; docs/CONTROLLER_MODE.md)
was written without a Go toolchain, a handheld or a PC: the raw-pad hook at the
top of main.go's event loop, Menu tap/hold, the USB link through the USB
controller engine's external mode (`ctlmode_usb.go` → `usbPadTurnOnWith`), the
JukaPad protocol and receiver (`ctlmode_wifi*.go`,
`player/ctlmode/jukapad_receiver.py`) and the DSU server (`ctlmode_dsu.go`).
The receiver's `--selftest` and the Go test vectors agree, but nothing ran on
the wire. Session notes: notes-ui/wifi, review-ui/wifi.
**Where.** `ctlmode.go`, `ctlmode_ui*.go`, `ctlmode_usb.go`, `ctlmode_wifi*.go`,
`ctlmode_dsu.go`, `player/ctlmode/`.
**Done when.**
1. `go test ./player -run 'JukaPad|DSU'` passes.
2. Page: opens from Tools, Apps, Start, Home search and Quick Settings; B always
   goes one level up; leaving stops the link; the backlight comes back after
   Menu, a lost link, an alarm, sleep and a game launch.
3. USB: Xbox 360 shape is an Xbox 360 controller in Windows (joy.cpl, a Steam
   game); Generic shape in Linux (`evtest`) and Android; Menu tap pauses (the
   PC sees all released), A resumes, hold Menu = Guide.
4. Wi-Fi: the page's download address serves both receiver files; a Windows PC
   (vgamepad + ViGEmBus) and a Linux PC (uinput) pair with matching codes,
   reconnect without a prompt, rumble reaches the handheld, pulling Wi-Fi
   releases every button within ~1 s; `--tsp` works on a network with AP
   isolation; Nearby devices running at the same time keeps 8321/8322.
5. DSU: Dolphin's DSU Client and Cemu's DSUController read every button with A
   at the bottom position, sticks the right way up; the page shows Connected
   while one reads it.
**Verify.** Record in the HW log with the `ctlmode`/`usbpad` log lines and the
PC tester's screenshots.

### CTL-HW-2 — Controller Mode's Bluetooth gamepad with real hosts (P2, S) [HW]
**Why.** The Bluetooth link (`ctlmode_bt*.go`, beta) is a BLE HID-over-GATT
peripheral driven through a hand-written D-Bus client (`dbusmini.go`). Its wire
bytes match libdbus/GLib in the VM, but whether the TrimUI firmware's
`bluetoothd` hosts GATT services and LE advertising, and whether hosts accept
the pad, is unknown. Session notes: notes-bt, review-bt.
**Where.** `dbusmini.go`, `dbusmini_test.go`, `ctlmode_bt.go`,
`ctlmode_bt_linux.go`.
**Done when.** `go test ./player -run DBus` passes; on a Smart Pro and a Brick
the link starts (or says plainly why BlueZ refuses); an Android phone, a
Windows 11 PC and a Linux PC pair as "JukaHub Pad" with the code confirmed on
the handheld; buttons, hat, sticks and triggers read right in a gamepad tester;
a paired host reconnects on the next start; Remove pairing works; the adapter's
name, Pairable and Discoverable are back after leaving, after `kill -9` and a
restart; a code-less pairing still asks on the handheld.
**Verify.** Record in the HW log with `bluetoothd --version`, `bluetoothctl
show` before/after and the `ctlmode` log lines.

### KOREADER-HW-1 — Open with KOReader on CrossMix and a copied KOReader (P2, S) [HW]
**Why.** `koreader.go` (2026-11-19) hands a book to KOReader's launch script
when the script takes arguments, else runs `luajit reader.lua <book>` from its
folder; neither CrossMix's KOReader folder nor its script was seen. Session
notes: notes-ko, review-ko-rec.
**Where.** `koreader.go`, `extapps.go`, `pdfreader.go`, `gamemanual_ui.go`,
`openwith.go`, `setmodel.go`.
**Done when.** On CrossMix with its KOReader: a `.cbz` and a `.djvu` from Files
open in KOReader at that book (not its file browser) and JukaHub is back on
Files when KOReader quits; the reader's Select → Open in KOReader appears once
and the JukaHub page is kept; Settings → Media & playback → Open books with →
KOReader sends PDF/EPUB from Files, chat and manuals there; without KOReader
every row and hint is gone within 30 s.
**Verify.** Record in the HW log with the "KOReader: opening …" log line.

### SCREC-HW-1 — Screen recording during games on a device (P1, S) [HW]
**Why.** `screenrec*.go` (2026-11-19) records `/dev/fb0` with the bundled
ffmpeg (mpeg4, fragmented MP4, 640 px, 30 fps, nice 10). The cost to a running
game, the stock ffmpeg's support for the flags and `q` on stdin, and KMS
models are unknown. Session notes: notes-rec, review-ko-rec.
**Where.** `screenrec.go`, `screenrec_linux.go`, `recordings.go`,
`quicksettings.go`, `gamemenu.go`.
**Done when.** Quick Settings → Record screen, More → Record this game and the
in-game menu (Select + L2) record JukaHub, a RetroArch game, a PortMaster port
and PPSSPP at the right orientation; clips play from Recordings; no ffmpeg is
left after stop, quit or `kill -9` of JukaHub; the space, battery and 30-minute
guards stop it; frame rate in a demanding game with recording on is noted
(lower the 30 fps cap if it stutters); a Smart Pro S game clip is not black (or
the docs say so); CrossMix Screen Recorder clips are listed.
**Verify.** Record in the HW log with `top` during a game and the clip sizes.

### FOLDER-HW-1 — Whole-folder sending from phone and PC browsers (P2, S) [HW] [NET]
**Why.** The Phone Companion's Send a folder (`companion_dropdir.go`,
2026-11-19) was tested with node and table cases only; browsers' folder
pickers and dropped-folder reading differ.
**Where.** `companion_dropdir.go`, `companion_drop.go`, `companion.go`.
**Done when.** Desktop Chrome, Firefox and Edge send a book folder, an album
(lands in Music, the media library lists it), a PS1 `.cue`+`.bin` folder (one
game in the library), a DOS game and a ScummVM game to their shelves, and a
PortMaster port to PhoneDrop; Android Chrome's folder picker works; iOS Safari
falls back to files; Cancel leaves no `.part` file; a nearly full card refuses
before any byte.
**Verify.** Record in the HW log with the handheld's summary toasts.

### LIGHTGUN-HW-1 — Light gun on a Smart Pro and a Brick (P1, S) [HW]
**Why.** Light-gun mode (2026-11-19, `lightgun*.go`) was written without a
device: it makes a uinput mouse before RetroArch starts and sets the gun's
device type, mouse index and gun binds in retroarch.cfg for one start.
Whether the handhelds' RetroArch (input driver sdl2) reads that mouse, which
device ids each core takes (FCEUmm 258 with `fceumm_zapper_mode = clightgun`,
Snes9x 260/516, Genesis Plus GX, PCSX-ReARMed, Beetle PSX, SwanStation,
FinalBurn Neo 4, MAME 2003-Plus) and whether the crosshair lines up are
unknown.
**Where.** `lightgun.go` (`lightgunCores`, `lightgunCfgValues`,
`lightgunPrepare`), `lightgun_linux.go` (anchor, pad codes),
`lightgun_aim.go` (`lightgunArea`, speeds), `vcursor_bridge_linux.go`;
docs/LIGHT_GUN.md.
**Done when.** On a Smart Pro with stock or CrossMix RetroArch: Duck Hunt
(FCEUmm), Super Scope 6 (Snes9x), Safari Hunt and a Menacer game (Genesis
Plus GX), Time Crisis (PCSX-ReARMed) and Operation Wolf (FinalBurn Neo) each
start with the gun given automatically, show a crosshair the right stick moves
in Move and in Point, R1 hits what the crosshair covers (Point with "Game
picture (4:3)" within a few pixels at the centre and corners — note any
offset), L1 reloads, R3 re-centres and Start still starts; the log shows the
"light gun mouse made" and "aiming with" lines; retroarch.cfg is back to its
old values after the game. Record per core whether the table's device id was
right (RetroArch log "Plugging device … into port …"). On a Brick the D-pad
aims and the game's D-pad does nothing; Mouse in games and apps does not grab
the pad during a gun game. If RetroArch never sees the mouse, record the SDL
version and input_driver and try `input_driver = "udev"`.
**Verify.** Record in the HW log.

### CHIP8-HW-1 — CHIP-8 player speed and sound on the A133P (P2, S) [HW]
**Why.** The CHIP-8 player (2026-11-19, `chip8_scene.go`) runs the
interpreter on the main thread at 60 fps; XO-CHIP games ask for 1000+
instructions a frame, and the beeper is a second SDL queue device beside the
UI sounds. Neither the frame time with a fast XO-CHIP game nor the beep
latency has been measured on a device.
**Where.** `chip8_scene.go`, `chip8_vm.go`, `chip8_options.go`;
docs/CHIP8.md.
**Done when.** On a Smart Pro and a Smart Pro S: an XO-CHIP archive game at
tickrate 1000 holds 60 fps with the frame time logged; Speed 10000 stays
playable or the Speed list is capped for the device; beeps start within
~50 ms with no clicks, stop at once on pause, and the device closes after the
game goes quiet; the Y-held keypad and the Start menu work with the stock key
layout; Get CHIP-8 games lists and installs from the archive.
**Verify.** Record in the HW log.

### WASM4-HW-1 — WASM-4 core and carts on a device and a PC (P2, S) [HW] [NET]
**Why.** `wasm4*.go` (2026-11-19) make `Roms/WASM4` a built-in WASM-4 shelf,
install the `wasm4` libretro core on the first start (JukaHub's
`cores-aarch64` build first, the buildbot nightly second; the buildbot DLL on
Windows) and add Get WASM-4 carts from wasm4.org. The cores workflow has
never built `wasm4` (a CMake row with `-DLIBRETRO=ON` in
`player/native/build-cores.sh`), wasm4.org's `carts.json` shape was not seen,
and nothing was compiled or run.
**Where.** `wasm4.go`, `wasm4_install.go`, `wasm4_carts.go`,
`wasm4_carts_ui.go`, `emupicker.go`, `player/native/build-cores.sh`;
docs/WASM4.md.
**Done when.** "RetroArch cores for TrimUI" with `wasm4` lists
`wasm4_libretro.so.zip` with glibc ≤ 2.31 and NEEDED only libc/libm (if it
"did not build", check where CMake put the `.so`). On a stock Smart Pro, a
Brick and a Smart Pro S: Get WASM-4 carts lists carts with screenshots (the
log says whether carts.json or the GitHub listing answered), installs one with
a sharp cover, and offline shows the saved list; its first start shows the
install card and plays with no private glibc; a heavy cart holds 60 fps on
the A133P; the picture is 640×640 and `video_scale_integer` is back to the
user's value after quitting; a cart's disk save survives a restart; Delete
removes the cart and cover, not the `.srm`; on Windows with RetroArch the
first start puts `wasm4_libretro.dll` in its cores folder and plays.
**Verify.** Record in the HW log.

### CHIPTUNE-HW-1 — Game soundtracks and SID on a device (P2, S) [HW]
**Why.** The soundtrack player (2026-11-19, `chiptune*.go`, `sidplay_*.go`)
hands console formats to ffmpeg's libgme demuxer (`-f libgme -track_index -t`)
and modules to libopenmpt, and renders PSID tunes into ffmpeg's stdin; nothing
was run, and whether the ffmpeg the music player uses on the handheld has
libgme and libopenmpt is unknown (BtbN's build should; the firmware's 6.1 on
PATH probably not).
**Where.** `chiptune.go`, `chiptune_decode.go`, `chiptune_meta.go`,
`chiptune_art.go`, `sidplay_psid.go`, `sidplay_cpu.go`, `sidplay_chip.go`;
hooks in `musicplayer.go`; docs/SOUNDTRACKS.md.
**Done when.** On a stock Smart Pro (then a Brick and a Smart Pro S): the log's
`chiptune: <ffmpeg>: libgme …, libopenmpt …` line names the binary used (with
only the firmware ffmpeg, an NSF says "This ffmpeg cannot play NSF/SPC/VGM
music" once and the list moves on); with libgme an NSF opens as its songs,
plays song n, stops after its length with a fade and steps gaplessly; an SPC
folder shows ID666 titles; a VGZ plays; seeking ±10 s and a speed change keep
the place. A PSID (a VBI tune, a CIA-timed tune, a play-address-0 tune and a
two-chip tune) plays at the right tempo and pitch, a deep seek stays
responsive, an RSID shows the refusal, and `top` with the screen off shows a
few percent of one core. A soundtrack of a game with a cover shows it in the
player, on the lock screen and the dock; an NSF playlist survives M3U export
and import.
**Verify.** Record in the HW log.

### TVMODE-HW-1 — TV mode on a Brick Pro with a TV (P2, S) [HW]
**Why.** TV mode (2026-11-19, `tvmode*.go`) watches every place a 4.9 kernel
may report a display (Type-C DisplayPort alt mode, extcon DP/HDMI, the switch
class, DRM connectors, Allwinner's disp dump and hdmi hpd, fb0's size), but
none of it ran on a Brick Pro: the real path, whether the firmware mirrors the
panel or makes the TV the screen, the output size and RetroArch's controller
numbering are guesses.
**Where.** `tvmode.go`, `tvmode_sysfs.go`, `tvmode_linux.go`; Settings →
Display → TV mode; Drivers & hardware → Video out; docs/TV_MODE.md.
**Done when.** On a Brick Pro with a USB-C → HDMI cable (then a dock with a
USB pad): with TV mode on Auto, plugging and unplugging toasts within ~4 s and
the log's `tvmode: video out:` lines name the file that flipped (if nothing
flips, record the logged `/sys/class` listing and the `*hpd*`/`state` files
with and without the cable, and add the path to `tvmode_sysfs.go`); record
mirror or TV-only, the TV's resolution, fb0's `mode`/`virtual_size` and the
start-time canvas line; Built-in screen off darkens only the panel and lights
it again on unplug and at exit; a Bluetooth and a USB pad become player 1 in a
RetroArch game started from JukaHub with the handheld's buttons as Port 2,
and `input_player1/2_joypad_index` are back afterwards (record
`input_joypad_driver`).
**Verify.** Record in the HW log.

### GBPRINT-HW-1 — Game Boy Printer with DoubleCherryGB on a device (P2, S) [HW] [NET]
**Why.** The printer scrapbook (2026-11-19, `gbprinter*.go`) installs
DoubleCherryGB (the only libretro core with printer emulation, since its
v0.18), pins it per game, sets `dcgb_emulated_gameboys = 1` for the launch and
finds prints by listing candidate folders before and after the game: the
printer option's key, where the PNGs are written and whether the buildbot's
aarch64 build is new enough (libretro's info file still says v0.17.0) are
unverified.
**Where.** `gbprinter.go`, `gbprinter_collect.go`, `gbprinter_ui.go`,
`gbprinter_companion.go`; `data/gbprinter/state.json` (`seen_options`,
`core_options`); docs/GB_PRINTER.md.
**Done when.** On a stock Smart Pro (then a Smart Pro S): Pokémon Yellow's
More → Game Boy Printer → Play with the printer installs DoubleCherryGB and
starts the game with its Gambatte save; after quitting, `seen_options` names
the core's printer key (put any needed value in `core_options` and the docs);
printing a Pokédex entry and a Game Boy Camera photo toasts "1 print saved to
the scrapbook" and the log names the folder the PNG came from (record it and
narrow `gbpPrintRoots` if it is fixed); Tools → Printer scrapbook shows it
sharp; the phone card's Save, 2× and 4× give 160, 320 and 640 px PNGs; Usual
emulator goes back to Gambatte with the progress made, and Pokémon Gold's
clock survives both copies.
**Verify.** Record in the HW log.

### USBPAD-HW-1 — The USB controller on real hardware (P2, S) [HW]
**Why.** The USB controller engine (2026-11-19, `usbpad*.go`, also Controller
Mode's USB link through `ctlmode_usb.go`) was written without a handheld or a
computer: which TrimUI kernels have the HID gadget function is unknown,
FunctionFS likely refuses the Xbox 360 interface's 0x21 descriptor (JukaHub
then offers the interface without it), and the grab, the UDC state file, the
hidg node, rumble and the ADB rebind are untested.
**Where.** `usbpad.go`, `usbpad_linux.go`, `usbpad_ui.go`, `ctlmode_usb.go`;
docs/USB_GAMEPAD.md.
**Done when.** On a Smart Pro (4.9), a Brick and a Smart Pro S (5.15): the
screen's check and the `usbpad: controller mode check:` log line are recorded
per model; in the Xbox 360 shape Windows 10/11 lists "Xbox 360 Controller for
Windows" without a warning, an XInput tester and one Steam game see every
button, both sticks and triggers, and the PC's rumble runs the motor; Linux
binds xpad; in the Generic HID shape macOS and an Android phone see all
buttons, the hat and both sticks; holding Select + Start for 2 s leaves and
the PC never sees Back+Start; unplugging and replugging reconnects; after
leaving, quitting and a `kill -9` plus restart, `adb devices` sees the
handheld again and USB file transfer comes back if it was on; Host mode
refuses the start with the USB & OTG hint; Controller Mode's USB link works
the same.
**Verify.** Record in the HW log.

### OPENWITH-HW-1 — Open with and default apps on a device and a PC (P2, S) [HW] [WIN]
**Why.** Files → Y → Open with… and per-extension defaults (`openwith*.go`,
2026-11-19) were written without a device: the hand-off to TrimUI apps, the
implicit default for formats JukaHub cannot show and the interplay with
`koreader.go`'s "Open books with" are unseen. Session notes: notes-openwith,
review-openwith.
**Where.** `openwith.go`, `openwith_ui.go`, `features.go`, `files_manager.go`,
`fileeditor.go`, `pdfv_view.go`, `pdfreader.go`; docs/OPEN_WITH.md.
**Done when.** On CrossMix CE with KOReader: a `.epub` → Open with lists the
Reader (JukaHub's choice), KOReader and Another app…; A opens the book in
KOReader and quitting it returns to Files; X makes it the default, Default
apps… lists and resets it; a `.cbz`/`.mobi` opens in the one verified reader
with its toast; removing KOReader with a default saved gives one toast and
JukaHub's viewer; a ROM in `Roms/GBA` still plays; the text page with unsaved
edits refuses a viewer and Get <App>; a double-click never runs a row; with
"Open books with: KOReader", X on Reader pins JukaHub's reader and Y forgets
it; the reader's menu shows exactly one "Open in KOReader". On Windows the
sheet lists Open with Windows and keyboard-only hints read as keys.
**Verify.** Record in the HW log with the toasts seen.

### PADS-HW-1 — Pads sample sequencer on the handheld (P2, S) [HW]
**Why.** Pads (`pads*.go`, 2026-11-21) was written without a compiler or a
device: its audio loop mixes sixteen voices, an echo, a reverb and the stick
effects at 48 kHz on the A133P, hold and loop pads are let go by polling the
pad's buttons every frame, and sounds other than WAV go through ffmpeg.
**Where.** `pads.go`, `pads_ui.go`, `pads_engine.go`, `pads_samples.go`,
`controller.go` (stick hook), `shoulders.go`; docs/PADS.md.
**Done when.** On a Smart Pro: the starter beat plays without crackle with all
eight stick effects pushed in turn (watch the CPU on the performance overlay);
finger drumming plays eight pads with no held-D-pad machine-gunning and a loop
pad stops when its button is let go; an MP3 from Music and a GrainBrick WAV load
onto pads, Chop at hits cuts a drum loop where the hits are; Record live hits
lands on the nearest step; a 128-step pattern pages on the Beat grid; Export to
WAV plays back in the Music player; leaving the page stops the sound within a
second and pads.json comes back after a restart. On a Brick (no sticks) the FX
page's Hold rows work.
**Verify.** Record in the HW log with the CPU figure.

### EDITOR-HW-1 — Syntax colours and the hex editor on the handheld (P2, S) [HW]
**Why.** The text editor's syntax colours (`fileeditor_syntax*.go`) and the hex
editor (`fileeditor_hex*.go`, 2026-11-21) were written without a device or a
compiler: a coloured row is several textures where it was one, the hex editor
draws a texture per byte cell, and its save renames a copy over a file on the
SD card.
**Where.** `fileeditor.go`, `fileeditor_syntax.go`, `fileeditor_syntax_draw.go`,
`fileeditor_hex.go`, `fileeditor_hex_ui.go`, `openwith.go`, `vkeyboard.go`,
`vkedit.go`; docs/TEXT_EDITOR.md.
**Done when.** On a Smart Pro: a 1 MB Python file and `retroarch.cfg` scroll
with L1/R1 and a held D-pad without dropped frames; the colours sit on their
text at every text size, with wrap on and off, and stay readable on a light
theme; a script with no extension but `#!/bin/sh` is coloured as Shell; a new
line typed in a `.py` file is not capitalised. In the hex editor a `.srm` save
opens from Open with, A + Up/Down changes a byte and B puts it back, X types
`4A 75`, Y saves and every other byte is unchanged (compare on a PC); a one-byte
change to a 4 GB `.iso` saves in place within a second; a file replaced over
USB while open refuses the save. On Windows a keyboard types hex digits and the
footer shows "0–F Type".
**Verify.** Record in the HW log with the timings.

### APPC-HW-1 — App Center installs, roll back and clean return on a device (P1, S) [HW] [NET]
**Why.** The App Center (`appcenter*.go`, 2026-11-19) installs TrimUI apps
from GitHub releases through `pkgstage.go` with `Apps` as the root, restores
CPU and input after an app and stops strays; no install, swap or launch has
run, and the Xbox Pad BT asset/folder, KOReader CE and Screen Recorder folder
names are unconfirmed. Session notes: notes-appcenter, review-appcenter.
**Where.** `appcenter.go`, `appcenter_install.go`, `appcenter_launch.go`,
`appcenter_open.go`, `appcenter_ui.go`, `apps.go`, `installbar.go`,
`pkgstage.go`, `recordings.go`; docs/APP_CENTER.md.
**Done when.** On stock and CrossMix (TSP, TSP S, Brick): the header names
the firmware and the chips match; Get installs TrimUI Drop (`Apps/SendFile`),
Ritder and Xbox Pad BT (record the zip's folder and any rename toast), each
listed once by MainUI and by JukaHub; Update twice, Roll back and forward
again keep the app's own settings; `.jukahub-*` folders never appear as apps;
a power cut mid-install leaves the old or the new version, never none;
Remove empties the folder and the kept copies; Open runs an app and JukaHub
is back within ~3 s with the sticks working even when the app restarts
`trimui_inputd` or MainUI; with Auto performance on, two books in one app
give one session and no "put back: CPU speed" toast; offline and on a PC the
page browses only; the Recordings button appears only where JukaHub's own
recorder cannot run.
**Verify.** Record in the HW log with the `appcenter: <app> exited` lines.

### TERM-HW-1 — Interactive Terminal sessions on a device and Windows (P1, S) [HW] [WIN] [NET]
**Why.** The PTY/ConPTY sessions, the VT emulator and the built-in SSH client
(`termpty*.go`, `termvt.go`, 2026-11-19) were never run; ConPTY's handle and
job details, the A53's speed on a flood of output, the TERM/locale chosen and
the monospace fallback are unknown. Session notes: notes-termpty,
review-termpty.
**Where.** `termpty.go`, `termpty_linux.go`, `termpty_windows.go`,
`termpty_ssh.go`, `termpty_ui.go`, `termvt.go`, `terminal.go`, `vkedit.go`;
docs/TERMINAL.md.
**Done when.** On a Smart Pro and a Brick: `shell` opens a prompt with
colour and `exit` returns with the transcript; busybox `top`, apt's `htop`
(F10 from Select), `vi` (B = Esc, `:wq⏎` saves), `nano` (Ctrl+O/X via the strip
and R2) and `dmesg | less` work; `cat` of a 5 MB file keeps the UI moving;
hold B leaves and Start resumes; `sudo`/`su` passwords show as dots; quitting
JukaHub with `sleep 999 &` running leaves no process; `ssh user@pc` without
an ssh binary asks to trust the key, hides the password, follows text-size
changes, refuses a changed key and `ssh forget` clears it; a USB keyboard
sends Ctrl+C, F1 and held Backspace to the program and Ctrl+Shift+Q leaves;
`echo $TERM` is recorded. On Windows 10/11: cmd.exe in ConPTY, `ssh host`,
End session kills a `start /b ping -t` child.
**Verify.** Record in the HW log with `echo $TERM $LANG` and `stty size`.

### DEVCHECK-HW-1 — Guided Device check on the Smart Pro, report in the hardware log (P0, S) [HW]
**Why.** The 2026-11-19 late pass added Settings → About → Device check
(`devcheck.go`, `devcheck_probe.go`, `devcheck_ui.go`;
[Pick up and play](../docs/PICK_UP_AND_PLAY.md#check-your-device)) with no Go
toolchain or device. It is also the quickest way through the open Smart Pro
items in the hardware log's "Next hands-on run": speaker and headphones, sleep
and wake, Bluetooth, USB DAC, game launch and exit.
**Where.** `devcheck*.go`; the Controller test (`ctrldiag.go`).
**Done when.**
- On a Smart Pro with a fresh ARM build, all nine checks run: the controller
  test opens and closes back to the page and its result lists 12 buttons, both
  sticks and L2/R2; the test tone is heard on the speaker, on headphones (the
  jack is noticed within about 2 s) and, if one is at hand, a USB DAC.
- Sleep and wake from the page shows a real suspend (5 s or more, state
  `mem`/`freeze`), the Wi-Fi and internet return times and the tone after the
  wake, and passes by itself; when it fails, the evidence names what did not
  come back (keep `errors.txt`).
- Bluetooth search from the firmware's off state reports a device count and
  switches Bluetooth off again; Wi-Fi shows connected, internet and signal.
- The game check launches the suggested game; after quitting, its evidence
  shows the run time, the exit code and the game's Tested on this device
  results, and the answer appears on the game's card as the player's own.
- **Save report** writes `JukaHub reports/device-check-smart-pro-<date>.md` on
  the card with no network name, address, Bluetooth name or time of day.
  Leaving the page mid-way and restarting JukaHub resumes the same check.
- The short version on a Brick (the buttons check passes without sticks) and
  on Windows (headphones, sleep and USB DAC say "Not on this device").
**Verify.** The saved report pasted as a new dated section of
`docs/HARDWARE_VALIDATION.md`.

### UPDFIX-HW-1 — A bad update undoes itself on the Smart Pro (P0, S) [HW]
**Why.** The 2026-11-19 release hung in Go package init on every start and
needed SSH to recover. `launch.sh` now watches an updated build's first starts
and puts the previous build back when it never reaches a working screen with
input (`updrescue.go`, `startcheck.go`, `uhist.go`, `bootguard.go`;
[Pick up and play](../docs/PICK_UP_AND_PLAY.md#updates-fix-themselves)). It
was written without a device and simulated under busybox ash and dash only.
**Where.** `player/launch.sh` (probation watchdog and restore),
`updrescue.go`, `startcheck.go`.
**Done when.** Two builds that both carry the feature: N installed and running,
N+1 available (Updates, the offline channel, or Install from file).
- *Hang:* with N running, put `.test-update-hang` containing `hang` in the app
  folder (`Apps/JukaHub-Trimui` on the stock card), then install N+1. Black
  screen; within about 2 minutes, with no button pressed, N's Home returns with
  the **Update undone** card naming both builds. MainUI never appears.
  `errors.txt` has the probation lines, a `SIGABRT` goroutine dump whose
  `goroutine 1` stack is in `main.init` / `startcheck.go`, and "is back;
  starting it". Updates no longer offers N+1 (reinstall it for the next case
  with Install from file, which ignores the skip list); About shows N; Send
  report opens Report a bug with the stack; OK, restart: no card again.
- *Crash* and *noinput* instead of `hang`: the card within seconds ("it closed
  before its first screen"), and after 30–40 s ("its screen came up, but the
  buttons did nothing").
- *Power off:* with `hang`, hold the power key on the black screen after about
  20 s; power on, open JukaHub from Apps: the old build is back within about
  45 s.
- *Good update:* without the flag, N+1 reaches Home; about 10 s later
  `errors.txt` says it started fine and `.update-probation` is gone; no card.
  Putting the handheld to sleep within 5 s of Home and waking it 3 minutes
  later changes nothing.
**Verify.** The `errors.txt` excerpts and card wording in the hardware log.
For quicker runs start `launch.sh` over SSH with `JUKAHUB_PROBATION_LIMIT=30`.

### VERIFY-HW-1 — Tested on this device: automatic checks and the screens on a Smart Pro and a Brick (P1, S) [HW]
**Why.** The 2026-11-19 late pass records what was seen to work per model
(`verified.go`, `verified_observe*.go`) and shows it (`verified_ui.go`,
`verified_page.go`; [Pick up and play](../docs/PICK_UP_AND_PLAY.md#does-it-work-on-my-handheld))
without a device, a Go toolchain or a real ALSA status file.
**Where.** `verified*.go`; the hook lines in `gamesuspend.go`,
`gamelaunch_watch.go`, `gamemenu.go`, `gameassist_linux.go`.
**Done when.** With a fresh log, for each step read `data/verified/verified.json`
(or the page) and the `verified` log lines:
- Start a GBA game from the library and play 30 s: launch pass, and the record's
  engine names the core (and its build when JukaHub installed it).
- With the speaker (no Bluetooth), sound passes within about 5 s of the game
  making sound; with a Bluetooth headset nothing is recorded. If the speaker
  case never passes, note `cat /proc/asound/card*/pcm*p/sub*/status` while it
  plays and the game's process group (`ps -o pid,pgid,comm`).
- 15 presses over a minute: controls pass. A power-key sleep of 10 s and 15 s
  of play after the wake: sleep pass.
- Quit from RetroArch's menu and, in another game, from JukaHub's in-game menu:
  return pass. Holding Select + Start: return fail. A ROM whose core is missing:
  launch fail matching the quick-exit card. PortMaster's own menu records
  nothing; a port and an app from Apps get their own records.
- On the Smart Pro (1280×720) and a Brick (1024×768): the game page shows the
  label right after Game Ready's; More → Tested on this device lists the five
  checks with marks, sources, release and engine, and its actions (Works /
  Doesn't work / Not checked, Describe a problem through the on-screen
  keyboard, Clear the problem, Forget with its confirm) work with pad, keyboard
  and mouse; Settings → About → Tested on this device switches tabs with L1/R1,
  opens a row's card and **Export results** writes `data/verified/export-*.json`.
**Verify.** The log lines, a copy of `verified.json` and two screenshots per
model in the hardware log.

### SAVELOG-HW-1 — Save ledger, last-save label and low-battery sleep on a Smart Pro (P1, S) [HW]
**Why.** The 2026-11-19 late pass checks every save of a library game and
shows the last good one (`saveledger.go`, `saveledger_ui.go`), shortens the
sleep at low battery (`quickresume.go` `quickSleepDeadline`) and sets
RetroArch's `autosave_interval` for JukaHub launches (`savestate_resume.go`,
`saveFlushSeconds`). RetroArch's state headers, its `SHOW_MSG` command and
whether the kernel's clock counts time asleep were taken from memory.
**Where.** `saveledger*.go`, `quickresume.go`, `savestate_resume.go`.
**Done when.**
- A GBA game saved in-game appears in More → Saves within a minute;
  `retroarch.cfg` reads `autosave_interval = "10"` during the game and the old
  value after it.
- Sleep right after an in-game save, wake 5 minutes later: RetroArch's message
  line says "Progress safe: in-game save 5 min ago" (or more).
- Quit with the auto state: the label says "Saved just now" and Saves lists the
  auto save state. Record whether the `.state.auto` starts with `#RZIPv` or
  `RASTATE`; a `.state1` cut short with `head -c 100000` is listed as not read
  back.
- Power off after sleeping Off, battery at 14–15 % unplugged, a RetroArch game,
  sleep: the log names the 30-minute cap; about 30 minutes later the handheld
  powers off; the next power-on resumes the game with "Back where you left
  off: saved before power off". At 20 % the cap is 2 h; at 30 % or on a charger
  there is none.
- `kill -SEGV` RetroArch after 3 minutes without a save, and separately a hard
  power off: the label says "Not saved since it closed unexpectedly".
- A 2-hour sleep: an entry from before it reads "2 h ago", not minutes.
**Verify.** `data/save-ledger.json` (no full paths on screen), the log lines
and the state headers in the hardware log.

### RACHEEVO-HW-1 — RetroAchievements settings, sign-in and unlock lights on a TrimUI (P2, S) [HW] [NET]
**Why.** Since 2026-11-20 JukaHub writes RetroArch's `cheevos_*` options
per launch and signs RetroArch in from the handheld (`raach_*.go`). The key
names were checked on the web only; issue reports say RetroArch 1.20 ignored
the newer per-message leaderboard switches, and whether RetroArch writes its
log line by line decides how fast an unlock lights the rings.
**Where.** `raach_settings.go`, `raach_signin.go`, `raach_unlocks.go`,
`ledsilky.go` (`ledAchievementFlash`); docs/RETROACHIEVEMENTS.md.
**Done when.** On a Smart Pro with Wi-Fi: Sign in for games logs RetroArch in
at the next game and `retroarch.cfg` then holds `cheevos_token` with an empty
password; each chosen row is in `retroarch.cfg` while the game runs and gone
after it; Leaderboards Trackers only and Off behave as labelled (note the
RetroArch version and the keys it read); an easy first-level achievement
flashes the rings within about 2 s and "1 achievement unlocked in …" shows
after quitting; with Hardcore On the in-game menu says "Hardcore mode is on"
on Load state and has no Rewind or Cheats.
**Verify.** Record in the HW log.

### HOTKEY-HW-1 — Custom hotkeys and aspect ratio on a TrimUI (P2, S) [HW]
**Why.** `rahotkeys*.go` copies button codes from RetroArch's player-1
bindings or the pad's autoconfig profile and `raratio*.go` writes
`aspect_ratio_index` from Batocera's table (Config 20, Square 21, Core 22).
Neither the TrimUI profile name, the Menu button's code nor the index table
was seen on a device.
**Where.** `rahotkeys.go`, `rahotkeys_binds.go`, `rahotkeys_store.go`,
`raratio.go`; docs/GAME_HOTKEYS.md, docs/LOOKS.md.
**Done when.** On a Smart Pro and a Brick, with Hotkey button = Select, Save
state = X and Fast forward (hold) = R2, a library RetroArch game saves on
Select + X and fast-forwards on Select + R2 (or the row refused R2 because
the assistant owns it); the firmware's own Menu shortcuts still work when the
hotkey button is RetroArch's own; `retroarch.cfg` has its old binds after
quitting; the log names the pad profile and no "cannot read … code" line for
the face buttons, shoulders, Start or Select; Aspect ratio 4:3, 8:7, 10:9,
Core provided and Full screen each give the expected shape (note the
RetroArch version and the index it saves for "Core provided" in its own
menu).
**Verify.** Record in the HW log.

### AITRANS-HW-1 — Translate the game screen on a Smart Pro and a Smart Pro S (P2, S) [HW] [NET]
**Why.** `aitranslate*.go` turns on RetroArch's AI service for one launch and
answers it from a 127.0.0.1 service that asks Groq's vision model. The mode
numbers (0 picture, 1 speech, 3 text), the `AI_SERVICE` network command and
whether the TrimUI RetroArch builds include the AI service at all are
unconfirmed; text mode needs RetroArch 1.17 or newer.
**Where.** `aitranslate.go`, `aitranslate_server.go`,
`aitranslate_settings.go`, `gamemenu.go` (`gmTranslate`);
docs/GAME_TRANSLATION.md.
**Done when.** With Translate games on and a Groq key, a Japanese GBA or SNES
dialogue screen → Select + L2 → Translate screen shows English subtitles
within about 10 s and the game keeps running; Read aloud plays the English
line; no key and Wi-Fi off each show RetroArch's one-line error; a custom
vgtranslate address in picture mode draws over the paused game; after the
game `retroarch.cfg` has the user's `ai_service_*`, `network_cmd_enable` and
OSD values back and nothing listens on 127.0.0.1 (`ss -ltn`); the log names
the mode that worked (fix the numbers if text mode shows nothing).
**Verify.** Record in the HW log.

### KIDMODE-HW-1 — Kid mode and Kiosk mode on a TrimUI (P2, S) [HW]
**Why.** `uimode*.go` gates screens in `switchScene`, filters every game list
and refuses unmarked games at the top of `launchLibraryGame`; the Konami code
with swapped buttons and the ports and Android launch paths were only read,
not run.
**Where.** `uimode.go`, `uimode_kids.go`, `uimode_unlock.go`,
`uimode_settings.go`; docs/KID_MODE.md.
**Done when.** On a Brick and a Smart Pro, Kid mode with a code shows only
the marked games in the library, Tabs Home, search, the switcher and random;
an unmarked favourite, port and Android game refuse to start; Start → Unlock
with the code leaves, six wrong codes wait 30 s and the wait survives a
reboot; the Konami code leaves without a code and opens the code card with
one; Select + Start and the power card's Exit do not reach the stock menu; in
Kiosk the Xbox, PS5 and Steam installs say Locked; deleting
`data/ui-mode.json` from a PC starts in Full mode.
**Verify.** Record in the HW log.

### FNSWITCH-HW-1 — FN switch actions, Silky lights and GPU boost on real TrimUIs (P2, S) [HW]
**Why.** `fnswitch.go` acts on gpio243 for Mute, Lights off and Airplane mode
as well as Travel lock; `ledsilky.go` adds the 5% pulse, Follow screen
brightness and the achievement rainbow; `gpuboost*.go` needs a GPU devfreq
node the Smart Pro and Brick may not have.
**Where.** `fnswitch.go`, `ledsilky.go`, `ledindicators.go`, `gpuboost.go`,
`gpuboost_linux.go`, `xboxboost_linux.go`; docs/DEVICE_TUNING.md.
**Done when.** On a Smart Pro, FN switch does → Mute, Lights off and Airplane
mode each engage and undo by sliding, on Home, in a RetroArch game and in a
port, with no toast over the game; sliding back restores the previous volume,
the user's light mode and only the radios that were on; a reboot with the
switch slid either way leaves the right state; Travel lock still locks and
its boot hook comes and goes with the choice; the 5% pulse, Follow screen
brightness and the rainbow look right on the rings; `ls /sys/class/devfreq`
is recorded on the Smart Pro, Brick and Smart Pro S, and where a GPU node
exists the log shows boost on and off for an N64 game and a port with the
governor or `min_freq` restored afterwards.
**Verify.** Record in the HW log.

### IDLEGAME-HW-1 — Idle in games, idle hooks and Battery in games on a TrimUI (P2, S) [HW]
**Why.** `idlewatch*.go` reads every input device without grabbing it, dims
through the display driver while RetroArch owns the screen and sleeps or
powers off from a goroutine; `gamehud.go` sends `SHOW_MSG` lines. None of
that ran on a device, and a noisy input device would keep the screen awake.
**Where.** `idlewatch.go`, `idlewatch_linux.go`, `gamehud.go`,
`batterywarn.go`; docs/IDLE.md.
**Done when.** With Dim 5 min and Then 5 min: the screen dims after 5 idle
minutes in a RetroArch game and a PPSSPP game, a resting stick does not keep
it lit, any button restores the exact brightness; Sleep sleeps and the power
key wakes into the game; Save and power off saves and powers off, and neither
acts on a charger; a Bluetooth pad connected mid-game counts as input;
`hooks/idle|extended|active/*.sh` run with the right `JUKAHUB_IDLE_*` values
in a game, on the idle screen and around a power-button sleep, and a 15 s
script is stopped at 10 s; Battery in games Every 5 min shows the line (the
"·" drawn) and the time when chosen, nothing while the in-game menu is open,
and the 20% line once while draining.
**Verify.** Record in the HW log.

### CARDCHECK-HW-1 — Card check on a Smart Pro and a Brick (P2, S) [HW]
**Why.** `cardcheck*.go` reads the partition's boot sector, the kernel log and
every file, and writes a fake-card test; the device names, the exFAT driver
TrimUI uses and whether stock firmware has an fsck were not seen.
**Where.** `cardcheck.go`, `cardcheck_linux.go`, `cardcheck_ui.go`;
docs/CARD_CHECK.md.
**Done when.** The check names the right partition, file system and card;
the health-flags step reads the boot sector (no "could not be read"); a card
pulled out while on shows "not properly unmounted" after a reboot; the read
test finishes a 64 GB card without menu stutter; the 1 GB fake test passes on
a genuine card and leaves no `.jukahub-cardcheck` behind; a power cut during
the test is cleaned up at the next start; the fsck step's result is noted.
**Verify.** Record in the HW log.

### DAVSHARE-HW-1 — The WebDAV share with real Windows, Mac and Linux clients (P2, S) [HW] [NET]
**Why.** `davshare*.go` is a WebDAV server written without a client to try:
Windows' WebClient, macOS Finder and GNOME Files each have their own quirks
(locks, Win32 properties, free-space queries, `._` files).
**Where.** `davshare.go`, `davshare_lock.go`, `davshare_props.go`,
`davshare_ui.go`, `fileserver_guard.go`; docs/NETWORK_SHARE.md.
**Done when.** Windows 11 Explorer maps `http://IP:8325/KEY/` with the
WebClient service running, copies a 40 MB file both ways, renames, deletes,
makes a folder and keeps the file date; macOS Finder connects read-write and
copies a folder; GNOME Files opens `dav://`; Roms and Ports refuse writes;
the share stops after 15 idle minutes and on a Wi-Fi change.
**Verify.** Record in the HW log.

### AUTOCOLL-HW-1 — Automatic collections and the game idle screens on a TrimUI (P2, S) [HW]
**Why.** `autocoll.go` builds folders from the library on the Collections tab
and `saverslides.go` decodes art and plays video snaps through ffmpeg at the
idle screen's CPU ceiling; neither was measured with a large library.
**Where.** `autocoll.go`, `hometabs_collections.go`, `saverslides.go`,
`saver_power.go`; docs/COLLECTIONS.md.
**Done when.** On a Smart Pro and a Smart Pro S with at least 1,000 games the
Collections tab opens and scrolls All games without a visible stall; Last
played reorders after a launch, also with the clock at 1970; Recently added
lists only a ROM copied in after the first scan; "Played N times" shows on a
played game's page; Game slideshow crossfades every 8 s with memory flat over
30 minutes; Game videos plays a 640×480 snap smoothly at the idle ceiling
(`scaling_max_freq` read back), moves on at the end, and one press wakes at
once with no ffmpeg left in `ps`.
**Verify.** Record in the HW log.

### RAOFF-HW-1 — Offline RetroAchievements on a TrimUI (P2, S) [HW] [NET]
**Why.** Since 2026-11-20 JukaHub runs a RetroAchievements relay on
127.0.0.1 and points RetroArch at it with `cheevos_custom_host` per launch.
The request fields, the unlock signature with its `o` (seconds since unlock)
and whether badges go through the relay were checked on the web only.
**Where.** `raoffline.go`, `raoffline_store.go`, `raoffline_replay.go`,
`raoffline_ui.go`, `raoffline_hash.go`; docs/RETROACHIEVEMENTS.md.
**Done when.** Softcore, Wi-Fi on: a library GBA game signs in, loads its set
and an unlock reaches the website, `retroarch.cfg` holds the relay address
only while the game runs. Wi-Fi off, same game: it signs in, earned
achievements do not pop again, a new unlock shows "1 unlock waiting"; Wi-Fi
on again sends it within a minute (note whether the website shows the real
unlock time). Hardcore with Wi-Fi writes no relay address; hardcore offline on
a cached game plays in Casual and is put back after. A game started right
after a wake waits for Wi-Fi at most 8 s. Cache recently played makes a
never-played NES/SNES/GB ROM load its set offline. `netstat -ltn` shows the
relay on 127.0.0.1 only and gone about 90 s after the game.
**Verify.** Record in the HW log.

### USBSTOR-HW-1 — USB storage mode on a Smart Pro and a Brick (P1, S) [HW]
**Why.** USB storage mode unmounts the SD card and lends it to a computer as a
mass-storage gadget from a helper in /tmp (`usbstorage_helper.sh`, started by
`launch.sh`). What holds /mnt/SDCARD on a stock boot, whether the Brick kernel
has the mass-storage function, and whether eject and unplug are seen were not
checked on a device; Smart Pro S and Brick Pro are left out on purpose.
**Where.** `usbstorage.go`, `usbstorage_linux.go`, `usbstorage_ui.go`,
`usbstorage_helper.sh`, `launch.sh` (`usb_storage_handoff`);
docs/USB_FILE_TRANSFER.md.
**Done when.** On a Smart Pro and a Brick (stock firmware): the row's check
either opens the sheet or names what holds the card; Windows 11, macOS and
Linux each see a removable drive, a 1 GB folder copies both ways, and Eject
brings JukaHub back within about 10 s; a button press, a pulled cable and 10
idle minutes each bring it back too; afterwards `/proc/mounts` shows
/mnt/SDCARD with its old options, `adb devices` sees the handheld, a card swap
file is back in /proc/swaps, and quitting JukaHub returns to the stock menu
with no black screen.
**Verify.** Record in the HW log.

### KEYPOWER-HW-1 — Keys, menus and power settings on TrimUIs (P2, S) [HW]
**Why.** The 2026-11-20 pass added Brick F1/F2 actions, a Smart Pro S Home key
reader whose key code (172 or 102) is a guess, D-pad as stick through
RetroArch remap values 16–19, Threaded video per launch, in-game menu entries,
Start in, Hold Menu → game switcher, Ask before power off, Battery percentage
and Lights in games — none seen on hardware.
**Where.** `frontkeys*.go`, `racontrols_dpad.go`, `ra_threaded.go`,
`gamemenu_entries.go`, `startup_target.go`, `menuhold_action.go`,
`power_options.go`; docs/CONTROLS.md, docs/CONFIGURATION.md.
**Done when.** Brick: each F1/F2 choice works on Home and the browser keeps
its own F1. Smart Pro S: `evtest` names the Home key's code, the log line
"Home key: reading key …" matches it, and a tap does the chosen thing in
menus and in a RetroArch game. Brick N64 and PSP games walk with the D-pad
and the one-launch remap is gone after quitting. Smart Pro S games have
`video_threaded = "false"` only while they run. Hidden in-game menu rows stay
hidden; Start in → Last game played starts it after a power cycle but not
after quitting to the stock menu and back, and Resume at power on still
wins. Hold Menu → Game switcher saves and opens the switcher, Menu with a
D-pad hotkey does not. Ask before power off Off powers off from the Home
tile and a 1.5 s power hold. Battery percentage modes leave no gap; Lights in
games Off keeps the rings dark in a game but shows charging and low battery.
**Verify.** Record in the HW log.

### MORESYS-HW-1 — Five more consoles, stock firmware helper and Tasks (P2, S) [HW]
**Why.** NAOMI/Atomiswave (Flycast), DSi (melonDS DS with
`melonds_console_mode = "dsi"` per launch), OpenBOR and CoCo were added from
docs only; the stock firmware row reads `/etc/version` and gives flashing
steps from guides that disagree for the Smart Pro S; Tasks moves emulator
settings to a reset trash.
**Where.** `extra_systems.go`, `libsort_articles.go`, `fwupdate.go`,
`about_card.go`, `maint_tasks.go`; docs/GAME_LIBRARY.md, docs/DEVICES.md,
docs/RECOVERY.md.
**Done when.** NAOMI and Atomiswave sets with their BIOS start in Flycast
(note fps on a Smart Pro and a Smart Pro S) and the BIOS zips are not listed
as games; a DSi-enhanced and a DSi-only cart start in DSi mode with the four
DSi files and the .opt is back to DS after; OpenBOR and CoCo games show what
to install. Stock firmware shows the same version as TrimUI's Device Info,
judges a renamed, zipped, misplaced and wrong-model `.awimg`, and one real
update per model follows the shown steps (record the Smart Pro S buttons).
Emulator versions match RetroArch's System Information. Each Tasks reset moves
its files to the reset trash with restore.txt, refuses while a game is
suspended, and RetroArch keeps its controls after.
**Verify.** Record in the HW log.

### VNC-HW-1 — VNC and Telnet servers on the devices (P2, S) [HW] [NET]
**Why.** The VNC server (`vncserver*.go`, 2026-11-20) reads a
game's picture from /dev/fb0 and types into games by writing events into the
firmware pad's evdev node; neither can be checked without the hardware, nor
can real viewers' handling of the RFB details (ZRLE 3-byte pixels,
DesktopSize, Apple's 3.889 version string). While the game assistant or the
games mouse has grabbed the pad, the kernel drops the injected events.
**Where.** `vncserver_frames.go` (vncFBLoop, vncBlit),
`vncserver_input_linux.go` (vncPadPress, vncPadOpenLocked),
`vncserver_proto.go` (newVncConv, vncZRLE), `telnetserve.go`; docs/VNC.md.
**Done when.** On a Smart Pro, a Brick and a Smart Pro S: RealVNC Viewer,
TigerVNC (ZRLE and Raw) and macOS Screen Sharing sign in with the sheet's
password and show JukaHub with correct colours; a RetroArch game and a
PortMaster port are shown (or the Smart Pro S's limit is documented) and
answer Enter/Esc/arrows/F1 from the viewer; TV mode resizes TigerVNC; New
password disconnects a viewer; PuTTY and `telnet` get a shell on port 23
with a working window size, and three wrong passwords drop the connection.
**Verify.** Record in the HW log.

### SMBSERVE-HW-1 — The SMB share with real Windows, Mac and Linux clients (P2, S) [HW] [NET]
**Why.** The SMB server (`smbserve*.go`, 2026-11-20) was
written from MS-SMB2, MS-NLMP and MS-SRVS without a compiler or a client; its
tests cover NTLMv2, signing, paths and hostile frames, not real Windows or
macOS behaviour. It sends no SPNEGO mechListMIC (as ksmbd), the main risk on
Windows 11 24H2.
**Where.** `smbserve.go`, `smbserve_conn.go`, `smbserve_ntlm.go`,
`smbserve_fs.go`, `smbserve_info.go`, `smbserve_rpc.go`, `smbserve_nbns.go`;
docs/SMB_SHARE.md.
**Done when.** Windows 11 24H2 opens `\\IP\JukaHub` with jukahub and the
password and "Remember" survives a share restart; Explorer at `\\IP` lists
JukaHub; `\\DEVICENAME\JukaHub` resolves; Finder connects and copies both
ways; a 4 GB file copies both ways (note MB/s against SFTP); read-only
refuses writes; New password disconnects a PC; ports 445/137 are free on the
stock firmware; the idle and network-change stops fire. If Windows rejects
the session for the missing mechListMIC, add NTLM's GSS_GetMIC over the
client's mechTypes in `smbserve_ntlm.go`.
**Verify.** Record in the HW log.

### TAILSCALE-HW-1 — Tailscale on the handheld: install, reach it, sign in, battery (P2, S) [HW] [NET]
**Why.** `tailscaled*.go` (2026-11-20) was written without a
device or network access: the pkgs.tailscale.com package list and `.sha256`
format, the CLI flags and userspace inbound forwarding come from Tailscale's
docs and third-party scripts, and nobody has measured what an idle tailnet
costs in battery.
**Where.** `tailscaled.go` (install, daemon, sign-in, status),
`tailscaled_linux.go`, `tailscaled_ui.go`; the servers' listen addresses
(`ssh_server.go`, `ftp_server.go`, companion, `vncserver*.go`,
`smbserve*.go`); docs/TAILSCALE.md.
**Done when.** On a Smart Pro and a Brick (and a Smart Pro S): Install
downloads, verifies and places both programs; switching on starts tailscaled
(nice 10); the QR code signs the handheld in from a phone and an auth key
works; from a phone on mobile data the 100.x address reaches SSH, the phone
companion, FTP, VNC and SMB (fix any server that binds only the Wi-Fi address
or refuses 127.0.0.1); Wi-Fi off stops it and Wi-Fi back starts it; below
15 % it stops; exit and a killed JukaHub leave no tailscaled; an idle hour
costs no more than about 2 % extra battery; the emulator updates pass updates
it and the daemon restarts outside a game.
**Verify.** Record in the HW log.

### SYNCTHING-HW-1 — Syncthing's extra folders and LAN web GUI on a device (P3, S) [HW] [NET]
**Why.** `syncthing_more.go` (2026-11-20) adds five extra
folders and opens the web GUI on 0.0.0.0; neither was run against a real
Syncthing.
**Where.** `syncthing_more.go`, the hooks in `syncthing.go` (the GUI address,
syncthingConfigure, syncthingShareFolders); docs/SAVE_MANAGER.md.
**Done when.** Turning on Screenshots creates and shares
`jukahub-extra-screenshots` and a paired PC receives new screenshots; turning
it off removes the folder from Syncthing and keeps the files; two JukaHubs
with Music on sync it with no further step; Web GUI on restarts Syncthing on
0.0.0.0, a PC browser signs in with `jukahub` and the shown password, and
turning it off makes that password fail and the page unreachable.
**Verify.** Record in the HW log.

### WIDE-HW-1 — Widescreen hacks and the PlayStation analog pad on a TrimUI (P2, S) [HW]
**Why.** Widescreen (`widescreen.go`) and the PlayStation analog controller
(`psxanalog.go`), 2026-11-20, write core options and device ids
taken from upstream docs and other front ends; several names (SwanStation's
`swanstation_GPU_WidescreenHack` and `swanstation_Display_AspectRatio`,
Beetle PSX's `*_widescreen_hack_aspect_ratio`, ParaLLEl N64's
`parallel-n64-aspectratiohint`, SwanStation's DualShock id 261) have not been
seen working on the handheld, and the option folders' names are assumed. A
per-core RetroArch override that sets the aspect ratio beats the 16:9 picture.
**Where.** `widescreen.go` (widescreenCoreOptions, widescreenCoreFolders),
`psxanalog.go` (psxAnalogDevice), the log lines "widescreen 16:9 for this
launch" and "psx analog: …"; docs/WIDESCREEN.md.
**Done when.** On a Smart Pro: Super Mario 64 (Mupen64Plus-Next), a 3D
PlayStation game in SwanStation and in Beetle PSX HW, a Dreamcast game in
Flycast and Sonic in Genesis Plus GX Wide each fill the screen with
Widescreen on and go back to 4:3 after exit; Ape Escape in PCSX-ReARMed and
in SwanStation shows a DualShock as port 1 with both sticks working, a
digital-only game keeps the standard pad, and the Brick's Auto leaves the pad
alone. Wrong keys or ids are fixed in the two tables.
**Verify.** Record in the HW log.

### LOOKPACK-HW-1 — Look packs, icon packs and the power-off picture on the devices (P2, S) [HW]
**Why.** Built without a device (2026-11-20): the power-off
picture relies on the panel keeping the last frame through SDL's teardown
until power-off, icon packs decode on the handheld's two decode slots, and a
pack's boot logo goes through the raw boot-logo writer.
**Where.** `shutdownscreen.go` (shutdownScreenHold, from `shutdown.go`),
`iconpacks_draw.go`, `lookpacks_ui.go` → `bootlogo.go`; docs/LOOK_PACKS.md.
**Done when.** On a Smart Pro, a Brick and a Smart Pro S: Power-off screen →
Picture (each built-in and a PNG in Themes/Shutdown) stays visible until the
screen goes dark on Power off and Restart, and quitting to the stock menu
ends on black; an icon pack (Icons/<pack>/Emus) shows on Games, the
Home tabs and Emulators without frame hitches and Console icons → JukaHub
brings the glyphs back; a pack's staged boot logo, written from Settings →
Boot logo, boots with that picture and Restore puts the old one back; the
stock menu does not list Themes/Packs or Themes/Shutdown as themes.
**Verify.** Record in the HW log.

### FLOW-HW-1 — Cover Flow, Cube and launch warm-up on a Smart Pro and a Brick (P2, S) [HW]
**Why.** The Cover Flow and Cube library views draw covers as turned quads
through SDL_RenderGeometry (flat fallback when it fails), and the launch
warm-up fadvises RetroArch, the core, its .info and the ROM into the page
cache at idle priority — none of it was seen on the A133P renderer or the 4.9
kernel's SD stack.
**Where.** `game_library_flow.go`, `game_library_cube.go`,
`game_library_covers.go`, `game_library_view.go`, `gamewarm*.go`;
docs/GAME_LIBRARY.md.
**Done when.** Both views hold 60 fps while sliding/turning on a Smart Pro and
a Brick (a held Up/Down on the Cube may snap between turns — judge whether it
should roll on from the current angle), a video snap plays on the centre
cover, clicks open the cover on top, and B/Left/Up leave as documented. The
launch log shows `launch warm-up for "…": finished, N files` for a GBA/SNES
game highlighted for a second, and A → first frame is measurably shorter
than with Warm up games before launch off (cold boot, same game, 3 runs each);
nothing is read during a game or at ≤ 15% battery.
**Verify.** Record in the HW log.

### GAMEKEYS-HW-1 — In-game F1/F2 rewind and fast forward, Menu + Select save and quit (P2, S) [HW]
**Why.** The game hotkeys read the pad without grabbing it and drive
RetroArch's command port: the Brick's front F1/F2 arrive as L3/R3 (0x13d /
0x13e) by reading of the code, the Brick Pro's own F1/F2 codes are unknown (it
uses L3/R3 for now), and nobody checked what the firmware's retroarch.cfg
binds to those buttons or to Menu + Select.
**Where.** `gamehotkeys.go`, `gamehotkeys_linux.go`, hooks in
`gameassist_linux.go`; docs/CONTROLS.md.
**Done when.** Brick, GBA game: holding F1 rewinds smoothly and stops on
release, F2 toggles fast forward and the in-game menu row agrees; on a Smart
Pro L3/R3 do the same; a DS game keeps L3/R3. Menu + Select saves and quits,
Home says "Saved — it continues…", the next start of the game continues
there, and a power cycle afterwards shows Home (no auto-start). Nothing fires
with the in-game menu open, in hardcore, or with a Select chord. `evtest` on a
Brick Pro names its F1/F2 codes (then add them to the table).
**Verify.** Record in the HW log.

### BATTWARN-HW-1 — Battery warning levels, Menu + volume brightness and battery on the lights (P2, S) [HW]
**Why.** Battery warnings (level, blink, "Battery N%" over RetroArch games,
save-and-suspend level), the Menu + volume brightness chord (Menu assumed to
be BTN_MODE, EVIOCGKEY to confirm it is held, the OS's volume step undone
0.7 s later) and the L2 + R2 battery flash on partial LEDs/zones were written
without a device.
**Where.** `batterywarn.go`, `lowbattery.go`, `statusbar_status.go`,
`brightkeys*.go`, `volumebuttons.go`, `ledbattlevel.go`, `ledindicators.go`,
`ledanim.go`; docs/BATTERY_WARNINGS.md.
**Done when.** With Auto everything warns exactly as before (5% message, 10%
lights, red below 15%, save at 3%); a 20% level turns the icon red and pulses
the lights at 20%, a RetroArch game shows "Battery 19%" once per percent and
never while charging; Save and suspend at 8% saves and sleeps at 8%. On a
JukaHub OS boot Menu + volume steps brightness in Home and in a game and the
volume ends where it was; on stock firmware with keymon nothing happens twice.
L2 + R2 on bare Home flashes the level on a Smart Pro's rings and a Brick's
lights (record which LEDs light), and lone L2/R2 still work everywhere.
**Verify.** Record in the HW log.

### UIPOD-HW-1 — The iPod layout, sound effects, visualizers, servers and scrobbling on a device (P1, M) [HW] [NET] [WIN]
**Why.** The UIPod-parity pass (2026-11-21) was written without a compiler or
a device: the iPod layout draws its transitions through render targets, the
sound effects run on every decoded buffer on the A133P, the visualizers read
a ring the device lock writes, and the Navidrome/Jellyfin proxy, the song
cache, Download Offline and the scrobble queue have only been reasoned about.
**Where.** `musicpod.go`, `musicpod_draw.go`, `musicpod_theme.go`,
`musicfx.go`, `musicfx_ui.go`, `musicviz.go`, `musicautoeq.go`,
`musicpanels.go`, `musicsrc*.go`, `musicproxy.go`, `musicscrobble.go`,
`companion_music.go`, `musicquiz.go`, `musicracer.go`, `brickbreaker.go`,
`sound.go` (the scope tap), `mlib.go` (index v3); docs/MUSIC_PLAYER.md,
docs/MUSIC_SERVERS.md.
**Done when.** `player\build.bat nolaunch` and `make check` pass. On a Smart
Pro and a Brick: the iPod layout scrolls a 5,000-song list at 60 fps with the
clicker on, every theme and transition draws (Dither and Zoom included), album
art appears dithered within a second; Tape Mode, each stereo effect and 1/16×
play without underruns (CPU under 15 % for the effects); the ten visualizers
follow the music they show at 30 fps or better; an AutoEQ profile downloads,
applies and still applies offline after a restart. Against a real Navidrome
and Jellyfin: browsing, play from the proxy (a seek mid-song, a Wi-Fi drop),
the cache filling and trimming, Download Offline landing in Music/Navidrome,
a Jellyfin H.264 film playing direct and an HEVC one through the HLS transcode
with a working seek and resume in the Jellyfin web app, Quick Connect. Last.fm
QR sign-in (with a key built in and with your own), ListenBrainz, Navidrome
counts, and twenty listens made offline arriving once online. Music Quiz,
Sound Racer (a song and an `.nsf`) and Brick Breaker play through.
**Verify.** Record in the HW log with CPU and frame timings.

### MUSICX-HW-1 — Music tidy, per-book speed, in-game music and Audio channels (P2, S) [HW]
**Why.** Tidy the Music folder renames on the card's FAT/exFAT (case folding,
no atomic no-replace rename), the in-game Music panel mutes RetroArch with
its MUTE toggle (journalled in racfgscope), and Audio channels wraps games'
ALSA output in a `route` plugin (also around BlueALSA) — none of it ran on
TrimUI's alsa-lib.
**Where.** `musictidy.go`, `ingame_music.go`, `audiochannels.go`,
`musicgame.go`, `audioresume.go`, `a11y.go`; docs/MUSIC_PLAYER.md.
**Done when.** Loose songs and a dropped album folder land in Music/Singles and
Music/Artist/Album after a scan, with favourites, playlists and a saved book
place following; a file still copying over USB/FTP is not moved. Two books
keep different speeds. From the in-game menu, music starts under a game that
was launched with music playing, the game goes quiet and comes back exactly
once when the music stops or the game ends, and the next game is not silent.
Mono, Mono left and Mono right are right on the speaker, wired headphones, a
USB DAC and a Bluetooth headset in a RetroArch game, a PPSSPP game and a port;
Stereo leaves the generated ALSA file byte-identical to before.
**Verify.** Record in the HW log.

### PLAYTIME-HW-1 — Play time by game across a power cycle and a clock jump (P3, S) [HW] [NET]
**Why.** The By game tab reads a new per-game per-day store
(`playtime-days.json`) that is re-dated when network time fixes a TrimUI's
power-on clock; that path only ran in a unit test.
**Where.** `playtime_periods.go`, `playtime_periods_ui.go`, hooks in
`playtime.go`, `trophy_room.go`, `game_options_ui.go`.
**Done when.** Play two games offline after a cold boot (clock at the
firmware's default date), then join Wi-Fi: both sessions show under Today
(not the bogus date) in By game, This week/month/year agree, X sums by
console, Ever matches the library's Playtime sort, and Game options shows
"This week … · Ever …". A profile switch shows the other person's numbers.
**Verify.** Record in the HW log.

### SEVENZ-HW-1 — .7z games, LaunchBox and ScreenScraper covers (P2, S) [HW] [NET]
**Why.** The pure-Go 7z reader (Copy/LZMA/LZMA2), the LaunchBox Games DB HTML
parse and platform names, and ScreenScraper's API v2 fields, system ids and
quota codes were written blind (both sites were unreachable from the build
sandbox). ScreenScraper also needs developer credentials: register a
developer account for the softname "JukaHub" and set the secrets
`JUKAHUB_SS_DEVID` / `JUKAHUB_SS_DEVPASSWORD` (CI) or the same environment
variables for build.bat / release.sh; without them the row says "Not in this
build".
**Where.** `sevenzip.go`, `gbcsort.go`, `gmeta_dat.go`, `romm_detect.go`,
`artwork_launchbox.go`, `artwork_screenscraper*.go`, `romsetup.go`
(fetchROMCoverAllSources), `artwork_choose.go`; docs/BOX_ART.md.
**Done when.** A .7z Game Boy Color game made by 7-Zip (LZMA2, the default) is
sorted into GBC and gets its libretro cover by CRC; a PPMd or BCJ2 .7z is left
as before with one log line. A game libretro lacks gets a LaunchBox front box
automatically (one request per second in the log), and Choose cover →
Search LaunchBox… finds and saves a cover. With credentials, signing in to
ScreenScraper fills cover, year, genre and synopsis for an unscraped game,
keeps a hand-written description, and a quota answer backs off.
**Verify.** Record in the HW log.

### OVERLAY-HW-1 — Your own overlays and Left stick as D-pad on TrimUIs (P2, S) [HW]
**Why.** Overlays/<Console>/ pictures become a generated RetroArch overlay
.cfg per launch, and Left stick as D-pad writes
`input_player1_analog_dpad_mode` through the per-launch scope; neither the
overlay keys nor the analog mode values were seen in TrimUI's RetroArch, and
fake-08 starting through RetroArch is unconfirmed.
**Where.** `useroverlays.go`, `ratuning.go`, `ratuning_settings.go`,
`ftp_server.go`, `rasticks.go`, `racontrols_ui.go`; docs/LOOKS.md,
docs/CONTROLS.md.
**Done when.** After a scan each console with games has its Overlays folder
(once; a deleted one stays deleted); a 1024×768 PNG dropped from the phone's
Files card shows in that console's next game and is listed under Settings →
Emulation → Overlay; retroarch.cfg's own `input_overlay` is back after the
game, a crash and a power cut. On a Smart Pro the left stick walks in a GBA
game, is a real stick in N64 and in a DualShock PlayStation game, and the
row is hidden on a Brick.
**Verify.** Record in the HW log.

### COMPUPD-HW-1 — Companion update card, logs.zip, folder guard and card tidy (P2, S) [HW] [NET]
**Why.** Install from the phone runs the handheld's own update path through
`tbxPost`; logs.zip masks Wi-Fi names, accounts, e-mail, MAC/BT and LAN
addresses with `redactLogPersonal`; the Files guard asks before renaming or
deleting a console folder; card tidy deletes Mac/Windows litter once per
boot — all unrun.
**Where.** `companion_update.go`, `companion_logs.go`, `companion_guard.go`,
`companion.go`, `companion_files.go`, `logredact.go`, `cardtidy.go`;
docs/USB_FILE_TRANSFER.md.
**Done when.** With an older build on the Beta channel the page shows the
newer version, Install updates and restarts JukaHub and the handheld says the
phone started it; Install is refused during a game and at low battery off
the charger. logs.zip opens on a PC and contains no Wi-Fi name, account name,
e-mail, MAC or 192.168.x address. Renaming Roms/GBA asks first, says what it
is for, and works after confirming. A card written by a Mac loses its `._`
files, .DS_Store and .Spotlight-V100 at the next start (count in the log),
and nothing else changes.
**Verify.** Record in the HW log.

### QMENU-HW-1 — The Quick menu, Menu on Home, theme pictures and busy chips (P2, S) [HW]
**Why.** The Quick menu (2026-11-20) opens from Start, a Home tile, a
control-profile button, the Brick's F1/F2, the Smart Pro S's Home key, Start in
and (opt-in) a Menu tap on Home; it reads NextUI theme pictures and the activity
strip now shows "Turning on Wi-Fi…" / "Turning on Bluetooth…". None of it was
seen on a device.
**Where.** `quickmenu.go`, `quickmenu_ui.go`, `quickmenu_bg.go`,
`activity_busy.go`, hooks in `voiceorb.go`, `frontkeys*.go`, `startup_target.go`,
`systemcontrol.go`, `bluetooth_bringup.go`; docs/QUICK_MENU.md.
**Done when.** On a Brick and a Smart Pro every tile and button acts (Recents →
switcher, Collections → the Tabs Home's Collections tab, Games, Tools, Settings)
and B returns to the Quick menu; Sleep wakes back into it; Restart/Power off ask
first with Cancel focused (one A with "Ask before power off" off). With Menu
button on Home = Quick menu a Menu tap opens and closes it, holding Menu still
talks to Juka and Hold Menu screenshots still work. A NextUI theme's
`.media/quick_*.png` shows per entry without a flash, Backgrounds only hides the
tiles, memory stays flat after many visits. Wi-Fi off → on shows the chip until
connected (≤ 12 s with no saved network); Bluetooth from cold shows its chip.
**Verify.** Record in the HW log.

### ART-HW-1 — Game art settings, console backgrounds and the Artwork Manager (P2, S) [HW] [NET]
**Why.** Art type (box / screenshot / title screen from the libretro
thumbnails), Art style Background, art width and corner radius, console
backgrounds and the Artwork Manager (2026-11-20) were written without a device:
rounded corners copy 2·r+1 strips per frame, the background picture decodes at
512 px and stretches, and counting reads every console folder.
**Where.** `artstyle.go`, `artstyle_fetch.go`, `artmanager.go`, hooks in
`game_library_ui.go`, `artcleanup.go`, `game_rename.go`, `game_delete.go`,
`gbcsort.go`; docs/GAME_LIBRARY.md.
**Done when.** Art type Screenshot → L1 (Fill gaps) fetches covers then
screenshots, the preview shows the screenshot (box art when there is none) and
L1 again stops both. Art style Background keeps titles readable on dark and
light themes and does not stutter while scrolling fast. Art width 60 % and
radius 24 px look right at 1280×720 and 1024×768, and Standard looks exactly as
before. A NextUI `Roms/<console>/.media/bg.png` and a
`data/backgrounds/<System>.png` show dimmed behind that console's games and go
with Console backgrounds off. The Artwork Manager counts, fetches one console
and all (progress in the header), Clear done hides complete consoles, A lists a
console's games with Box/Shot/Title marks, Reset asks on Cancel and moves the
files to the trash; B from it returns to the library on the same console.
Remove cover, Rename file and Delete game carry the `.snap`/`.title` files.
Record decode time and memory for a big card.
**Verify.** Record in the HW log.

### COLOUR-HW-1 — Colour picker, palettes, glass opacity and animation speed (P2, S) [HW]
**Why.** The RGB picker sheet, the Background/Surface/Text/Focus ring
overrides, eight palettes, Glass opacity and Animations (Comfy / Snappy / Off)
of 2026-11-20 were checked by script for contrast only, never on a screen.
**Where.** `colourpick.go`, `colourpick_theme.go`, `motionspeed.go`, hooks in
`design.go` (`ApplyThemeColors`), `menucolour.go`, `setui.go`, `ui_glass.go`,
`home_design.go`, `glass_real.go`, `hometabs_draw.go`, `anim.go`,
`homefocusglide.go`; docs/THEMES.md.
**Done when.** On a TSP and a Brick the picker fits; Left/Right step 5 with
held repeat, L1/R1 step 1 with held repeat; X takes a typed `#FF8800`; Y cycles
presets; A applies and B cancels. Paper on the Dark theme and Night on the
Light theme keep cards, borders, text and badges readable, survive a theme
switch and the light/dark schedule, and give way to High contrast (toggle and
theme). A red Focus ring paints rings on Settings, Home tiles, the Tabs Home and
sheets. Glass opacity 30 % thins tiles, the Tabs pill and the Realistic frost,
and Reduce transparency makes them solid. Snappy visibly speeds scene fades and
entrances; Off switches Reduce motion and its Quick Settings tile on; Comfy
both off. Two profiles with different palettes switch correctly.
**Verify.** Record in the HW log.

### HOMEVIEW-HW-1 — Backdrop and Minimal views, Tools tab, pinned games and the Home dashboard (P2, S) [HW]
**Why.** The Tabs Home's Backdrop view decodes a 640×480 picture 220 ms after
the selection rests and redraws it full screen every frame; pinned games are a
new `game:` tile kind; the Continue card now uses the save-state picture and
This month waits for a sane clock (2026-11-20). None of it ran on the A133P.
**Where.** `hometabs_backdrop.go`, `hometabs_minimal.go`, `hometabs_tools.go`,
`gamepin.go`, `home_dashboard.go`, hooks in `hometabs*.go`, `home_tiles.go`,
`home_continue.go`, `game_options_ui.go`; docs/HOME_TABS.md.
**Done when.** On a TSP and a Brick: Backdrop walked fast and slow shows the
picture about ¼ s after stopping, fading over the last, without stutter; a game
without art shows the normal background; Minimal is readable with the selection
in the accent colour. L2 → Y on Tools shows the tab and each entry opens. Grid
selection ring off removes the ring everywhere on the Tabs Home. Pin to Home
from the library leads Main with a Pinned badge and adds a cover tile on the
Widgets Home that plays the game; renaming the file keeps the pin, deleting the
game removes it. Remove from recents clears Recent, Continue and the switcher
(saves still load); Add to collection… opens the picker over the library. After
quitting a RetroArch game the Continue card shows the in-game picture within a
few seconds. Most played and This month match Playtime → By game; a cold boot
without Wi-Fi shows "Waiting for the clock" until NTP. The 13-row L2 sheet fits
on 1024×768.
**Verify.** Record in the HW log.

### DEVSYNC-HW-1 — Device Sync between two handhelds and NextUI collections (P2, S) [HW] [NET]
**Why.** Device Sync (2026-11-20) adds save states, BIOS, collections, recents
and play time, ROMs and Send my settings to Save sync over the paired HTTPS
link, with resumable copies; NextUI's `Collections/*.txt` are read and written
back. Two devices, NextUI's own reading of JukaHub's files and real Wi-Fi drops
were never tried.
**Where.** `devsync.go`, `devsync_data.go`, `devsync_ui.go`,
`collections_nextui.go`, hooks in `ssyncui.go`, `peernet.go`,
`hometabs_collections.go`; docs/DEVICE_SYNC.md, docs/COLLECTIONS.md.
**Done when.** Two paired TrimUIs: Save states off → only battery saves move;
BIOS on (on both) copies a file only one has and leaves a different same-named
file alone; BIOS off on one side refuses with "switched off on the other
handheld"; same-named collections merge both ways; Continue on B shows A's last
game and play time keeps the higher figure; ROMs on → per-console rows with
sizes, one console left out, the confirm shows the total, a Wi-Fi drop in a big
ISO resumes (the part grows, not restarts), and nothing in a ports folder or
any `.sh` is listed; Send my settings A → B waits on B until Use them, then theme,
wallpaper and text size change while Wi-Fi and the greeting stay; a peer on an
older JukaHub shows "has an older JukaHub" and saves still sync. On a NextUI
card with two collections (one naming a deleted game): both appear in order; a
game added in JukaHub shows in NextUI and the deleted game's line is kept with
the file's line endings; an edit made in NextUI or on a PC shows after a
restart; a new JukaHub collection appears in NextUI; a deleted one goes to
`Collections/.deleted` and NextUI hides that folder.
**Verify.** Record in the HW log.

### NETLINK-HW-1 — Game Boy link cable, GBA wireless adapter and four-player netplay (P2, S) [HW] [NET]
**Why.** The link modes (2026-11-20) set Gambatte's network-link options and
gpSP's `gpsp_serial = "rfu"` for one launch, with key names taken from libretro
docs, not from the firmware's cores; four-player netplay relies on RetroArch's
`netplay_request_device_pN`. Nothing ran on two devices.
**Where.** `netplay_link.go`, `netplay_link_beacon.go`, `netplay_link_ui.go`,
hooks in `netplay.go`, `netplay_rooms.go`, `netplay_ui.go`;
docs/GAME_LIBRARY.md (Netplay).
**Done when.** Two TrimUIs on one Wi-Fi, Pokémon Red on A and Blue on B
(Gambatte installed): A hosts Link cable, B finds the room and joins; the Cable
Club connects within about 30 s, a trade is saved on both cards, and the link
options are back to their old values (or gone) in the Gambatte `.opt` after
quitting. Also joined by typed address, and with A on its own hotspot. Record
whether the stock Gambatte has the network link and which address keys it uses
(12 digits or octets). A console whose usual core is another GB core says
"uses Gambatte for this session" and the next launch is back on the usual core.
FireRed and LeafGreen on gpSP see each other in the Union Room (record the
RetroArch version; 1.17 or newer is needed). An N64 game with two or three
guests seats everyone, "as player 3" seats B as player 3, and the host's
"N of 4 players here" follows joins and leaves. After every session
`retroarch.cfg`'s netplay request keys, spectator flag, command port and
autosave interval are back.
**Verify.** Record in the HW log.

### MEDIA-HW-1 — Audio and subtitle tracks, .ass/.sub files, song lyrics and the radio directory (P2, S) [HW] [NET]
**Why.** The track switch restarts the sound decoder with `-map 0:a:N` and
reads ffprobe's JSON with disposition flags; the `.ass`/`.ssa`/MicroDVD
parsers, lyrics from tags and the Radio Browser directory (2026-11-20) never
met the firmware's ffmpeg, real files or the device's TLS and DNS.
**Where.** `videotracks.go`, `videotracks_subs.go`, `lyrics_embedded.go`,
`radiodir.go`, `radiodir_ui.go`, hooks in `videoplayer.go`
(`runFFmpegAudio`), `videosubs.go`, `videosheet.go`, `music_shelf.go`;
docs/MUSIC_PLAYER.md, docs/CONTROLS.md.
**Done when.** On a Brick and a Smart Pro: an MKV with two audio tracks
switches language within about a second with picture and sound in sync, also
while paused, and reopening keeps the choice; an MKV with a picture subtitle
first and a text track second shows the text track, the Subtitles row steps Off
→ files → tracks, and a picture-only file says so; `Film.ass` and a MicroDVD
`Film.sub` show at the right times without `{\…}` codes. An MP3 with USLT, a
FLAC with LYRICS and an M4A with ©lyr show lyrics with no `.lrc` (a timed tag
syncs); record the cost of the extra ffprobe per track on the Brick. Music → the
Radio shelf → Radio directory: Top loads, Country opens on the device's
country, Genre "hip hop" and Search work, A plays and Next zaps, X saves to the
Radio shelf and IPStream's Radio tab, airplane mode shows the saved copy, and a
200-row list scrolls smoothly.
**Verify.** Record in the HW log.

### FILES-HW-1 — Picture rotate, rename and delete, two-pane Files and deleting a play record (P2, S) [HW]
**Why.** Rotate right decodes, turns and re-encodes in Go (up to three
full-size copies of a 20 MP picture in RAM), Rename types over the viewer, Two
panes swaps Files state between panes and copies or moves in the background,
and Play history can delete a game's record (2026-11-20). None of it ran on the
handheld.
**Where.** `imageviewer_edit.go`, `files_dualpane.go`,
`files_dualpane_xfer.go`, `game_history_forget.go`, hooks in `imageviewer.go`,
`files_manager.go`, `features.go`, `game_history_ui.go`; docs/CONTROLS.md,
docs/GAME_LIBRARY.md.
**Done when.** A 12 MP portrait phone JPEG (EXIF orientation 6) → X → Rotate
right saves within a few seconds, shows turned once (not twice) and its Files
thumbnail updates, without running out of memory on the Brick; a PNG saves; a
WebP says "turned on screen only". Rename with the pad over the viewer keeps the
extension and refuses a taken name; Delete (two presses) shows the next picture.
Y → Two panes on a Brick and a TSP is readable at half width, L1/R1 switch
panes, Copy to other pane of a 1 GB folder shows progress in the other bar with
the UI responsive and a second A stops it, a move SD → USB drive copies then
deletes and SD → SD is an instant rename. A game page's History shows Played N
times and Average session; X → A deletes the record, Playtime → By game and Ever
drop the game, and its saves are untouched.
**Verify.** Record in the HW log.

### QSET-HW-1 — The in-game Quick settings card and its Power off (P2, S) [HW]
**Why.** The in-game menu's Quick settings card (2026-11-20) changes
brightness, volume, radios, lights and rumble while the game is stopped under
the overlay, reads CPU/temperature/RAM/battery each second and powers off
through the save-first path; switching Bluetooth while a paused game holds a
BlueALSA headset was never tried.
**Where.** `gamemenu_quick.go`, hooks in `gamemenu.go`, `gamemenu_entries.go`;
docs/CONTROLS.md.
**Done when.** On a TSP and a Brick, in a RetroArch game and in PPSSPP or a
port (Select + L2 → Quick settings): brightness and volume change at once while
paused and match the header after quitting; Wi-Fi and Bluetooth say
"Switching…" and end where Quick Settings later agrees; Lights off darkens the
rings at once (low battery and charging still show); with music under a game
the Music in game level is heard at once; a new Rumble strength applies to the
next game; the system line updates each second. Power off: the game resumes
briefly, RetroArch writes a save state, the handheld switches off and the next
power-on resumes it (also with "Ask before power off" off). B goes back to the
menu, Select + L2 to the game, and the card fits at 1280×720 and 1024×768.
**Verify.** Record in the HW log.

### SAFEOFF-HW-1 — Safe power off, launch scripts, screenshot requests, USB awake, stock check and Developer page (P1, S) [HW]
**Why.** Safe power off (2026-11-20) sends SIGTERM to programs holding SD card
files, syncs and remounts the card read-only inside 3 s before every Restart
and Power off — on by default, never run on the TrimUI's vfat/exfat card. The
same pass added launch scripts, a screenshot request file read from the
framebuffer, staying awake on USB transfer, a stock-system check and a
Developer page.
**Where.** `safepoweroff.go`, `safepoweroff_linux.go`, `launchscripts*.go`,
`usbawake.go`, `stockcheck.go`, `devoptions.go`, hooks in `devpower.go`,
`gamesuspend.go`, `screengroup_unix.go`, `apps.go`, `screensaver.go`,
`logging.go`; docs/SESSION_SAFETY.md, docs/DEVELOPMENT.md,
docs/USB_FILE_TRANSFER.md.
**Done when.** Power off and Restart with Safe power off on: errors.txt says
"safe power off before poweroff: N of M card mount(s) read-only", the card has
no dirty flag or fsck repair at the next boot, an SSH `tail -f` on a card file
is closed, and tap-to-dark grows by under 3 s; a failed poweroff leaves the
card writable with its old mount options. A `data/hooks/pre-launch/10-log.sh`
plus a `GBA/` copy run in order with the `JUKAHUB_*` values before a GBA game
and an app (see `launch-scripts.log`); `exit 3` cancels with a toast; a
`sleep 30` script is killed at 5 s and the game still starts; post-exit scripts
get the exit code. `touch /tmp/jukahub/screenshot.request` during a RetroArch
game saves a PNG and writes its path to `screenshot.last` (also on a Smart Pro
S). With Auto sleep 3 min and the idle screen 1 min, a PC on MTP for 5 idle
minutes sees neither; unplugged, both return. On a card whose `runtrimui.sh`
NextUI or MinUI replaced: one toast, none after a restart, About → Stock system
names it; clean stock says Unchanged with no toast (record whether stock
mentions `.tmp_update`). Debug logging on shows DEBUG lines at once and after a
restart; Copy logs to the card writes a masked zip.
**Verify.** Record in the HW log.

### EMUTUNE-HW-1 — Fast-forward speed, N64 Light graphics and the Button layout (P2, S) [HW]
**Why.** Fast-forward speed writes `fastforward_ratio` as "2.000000" for one
launch; N64 graphics Light sets ParaLLEl N64's Rice plugin or cheaper
Mupen64Plus-Next options whose names were not checked against the firmware's
cores; Button layout Xbox flips the menu swap and makes RetroArch games default
to Swap both (2026-11-20).
**Where.** `raffspeed.go`, `n64light.go`, `buttonlayout.go`, hooks in
`ratuning_settings.go`, `inmap_settings.go`, `inputhints.go`;
docs/CONTROLS.md, docs/CONFIGURATION.md.
**Done when.** Fast-forward speed 2× → a GB game's fast forward runs about 2×
(not 10×) and `retroarch.cfg` has its own value back after quitting; GBA at 4×
while All consoles is 2× follows the console. With 4× set, change Fast-Forward
Rate to 2.5 in RetroArch's menu and quit: a toast says 2.5× is kept, the row
shows 2.5×, and after a sleep and a power-off the next game fast-forwards at
2.5×; the in-game menu's Fast forward line names it. On a Brick with N64 graphics Auto
an N64 game on ParaLLEl N64 logs "light N64 graphics for this launch" and shows
Rice in RetroArch's Core options, the `.opt` is restored after quitting, and
Mupen64Plus-Next gets 320x240 without frame-buffer emulation; a core option set
in RetroArch for a game logs "kept your own …"; a Smart Pro on Auto writes
nothing; a 16:9 widescreen launch stays Full. Record which games break without
frame-buffer emulation. Button layout Xbox on a Brick: the bottom button
confirms in menus, hints read by letter or role as chosen, a SNES game in
RetroArch has A on the bottom; Nintendo puts menus, hints and All consoles
Buttons back, and a console set to Normal stays Normal.
**Verify.** Record in the HW log.

### EMUSPEED-HW-1 — Emulator speed, PS2 speed and the wider Heavy-game boost on a Smart Pro and a Brick (P1, M) [HW]
**Why.** Juka, 2026-11-21: "improve performance of all emulators...especially
on Trimui Smart Pro and Trimui Brick XBOX, PS2, Gamecube, Wii support".
Settings → Emulation → Speed (`emuspeed.go`) writes per-launch core options
(the cores' automatic frameskip, threaded renderers, Fastest's trades) whose
keys and values come from the cores' option headers as remembered, not from
the firmware's builds; `emuspeed_ppsspp.go` edits JukaHub's PPSSPP's
`ppsspp.ini` per launch; Heavy-game boost (`xboxboost*.go`, formerly Xbox
boost) now also raises a memory-controller devfreq node and the game disk's
read-ahead, sets the swap up early, and covers PSP, Dreamcast, Saturn, Jaguar,
N64 and DS on the A133P; hakuX is offered (untested) on the Brick and Hammer.
All written without a Go toolchain or a device.
**Where.** `emuspeed.go`, `emuspeed_ppsspp.go`, `xboxboost.go`,
`xboxboost_linux.go`, `hakux.go`; hooks in `ratuning_settings.go`,
`n64light.go`, `gamediag.go` (`prepareGameDiag`), `setmodel.go`;
docs/DEVICE_TUNING.md (Emulator speed, Heavy-game boost), docs/CONFIGURATION.md,
docs/GAME_LIBRARY.md.
**Done when.** On a Smart Pro and a Brick, each step holds or a FIX task names
what differs:
1. A PlayStation game (PCSX-ReARMed) started twice logs `emulator speed for
   this launch … pcsx_rearmed_frameskip_type = auto` from the second start (the
   first may log "no options file yet"); Quick Menu → Core Options shows
   Frameskip Auto during the game, and the `.opt` holds `disabled` again after
   quitting.
2. A PSP game in JukaHub's PPSSPP logs `FrameSkip = 1, AutoFrameSkip = True`
   (and Fastest's keys on the A133P); PPSSPP's Graphics settings show them
   during the game; `ppsspp.ini` is as before and `emu-speed-ppsspp.json` gone
   after quitting; a value changed in PPSSPP during the game stays.
3. A PSP game in the PPSSPP core, a Dreamcast game (Flycast), an N64 game
   (Mupen64Plus-Next) and a DS game (melonDS or DeSmuME) each log what was
   written; note every key the firmware's builds lack or ignore (Core Options
   still show the old value) and every value RetroArch rejects.
4. A PS2 game on the A133P logs `armsx2_ee_cycle_rate` (record whether the
   build spells it `75%` or `-1`) and `armsx2_ee_cycle_skip = mild`; compare
   one game's fps at PS2 speed Fastest and Fast.
5. Record `/sys/class/devfreq` on the A133P and the A523 (a memory-controller
   node such as `dramfreq` or `dmc`?) and `/sys/block/mmcblk*/queue/read_ahead_kb`
   before, during and after a boosted game (the `boost on` line names both); a
   PSP game gets the boost on the Smart Pro and not on the Smart Pro S.
6. A heavy console's second start in one run has `libretro_log_level = 2` in
   the scoped edit; a first start that fails still shows its card.
7. On a Brick and a Smart Pro S, the Xbox tile offers hakuX marked untested;
   the install log names the binary `hakux_fit.go` replaced with spruceUI's
   `hakux.aarch64` (or why it could not). Record whether a game starts, whether
   the picture fills the panel, and whether hakuX's Menu maps a stick onto the
   D-pad. A GameCube game on a Brick starts with GameCube & Wii on Automatic.
8. Settings → Emulation → Adjust for GameCube → Speed and Games & emulators →
   GameCube speed show and change the same value.
9. During a boosted game `ps -L -o tid,ni,comm -p <JukaHub pid>` shows nice 10
   (the library index's thread stays 19) and 0 again after; on a Smart Pro S
   `taskset -p <RetroArch pid>` shows the fast cluster's CPUs. A PS1 game on
   the A133P logs `video_threaded = true`; compare its fps with Threaded video
   Off, and turn Auto's choice around if it is worse.
**Verify.** Record in the HW log; turn the fps notes into Auto changes where a
level costs more than it gives.

### HEAT-HW-1 — CPU boost in games and the heat guard on every TrimUI model (P1, M) [HW]
**Why.** Juka, 2026-11-21: "boost performance when running emulator by
adjusting CPU frequency safely...make sure to not overheat device by checking
temperature (All Trimui devices)". `cpuboost.go` holds a boosted game at the
top of its clock table 3 s after the start (the ceiling, the floor too under a
dynamic governor, parked cores online); `heatguard.go` lowers any session's
clock one operating point at a time while the hottest CPU/GPU zone or the
battery passes a per-model limit (Smart Pro 80/87/95 °C, Brick and Hammer
75/82/92, Brick Pro 76/83/92, Smart Pro S 82/88/95; battery 45/50/55; Heat
limit Cooler/Standard/Warmer), held 10 °C under the kernel's own critical
trip. Both run in Automatic CPU speed's tick (`autoperf.go`), which now also
follows the sessions it does not clock, for the guard alone. The limits, the
dial's pace (tuned on a lumped thermal model, not a device) and the 4.9
kernel's floor/ceiling write order were all written without a Go toolchain or
a device.
**Where.** `heatguard.go`, `cpuboost.go`, `autoperf.go` (session kinds,
`autoPerfSetPairLocked`, `autoPerfMarkerEntrySafe`); rows in `setmodel.go`;
`thermal.go` (Drivers & hardware), `gamemenu_quick.go` ("(cooling)"),
`aitools_games.go` (`performance_get`); docs/DEVICE_TUNING.md (CPU boost in
games and the heat guard), docs/CONFIGURATION.md.
**Done when.** On a Smart Pro, a Brick or Hammer, a Brick Pro and a Smart Pro
S, each step holds or a FIX task names what differs:
1. The first `heat` line (`guard on … chip …/…/… °C, battery …; kernel …;
   clock …`) names the model's limits, the kernel's trip points and the clock
   table; Settings → Games & emulators → Heat limit shows the temperature, and
   Drivers & hardware's Thermal line ends "JukaHub slows the CPU from … °C".
2. A PSP or N64 game shows `… · CPU boost · …` (or `heavy-game boost`) in the
   Automatic CPU speed row about 3 s after it starts; during it
   `scaling_max_freq` is the top (and `scaling_min_freq` too when the launch
   script left a dynamic governor); after quitting both are what they were
   before the game, with no `invalid argument` in the log.
3. A Game Boy game follows Automatic CPU speed on Auto, is boosted on Always,
   and on Off both games follow Automatic CPU speed and their launch scripts.
4. Twenty minutes of the heaviest game each model plays (Brick: N64 or
   Dreamcast; Smart Pro: PSP; Smart Pro S: GameCube) on Standard: record the
   `heat … peak` line, every `CPU limit … → …` step and the clock it settled
   at; the RetroArch message line shows "Hot (… °C): CPU slowed to …" once and
   "Cooled down: full CPU speed again" later, and the in-game menu's system
   line shows "(cooling)" meanwhile. Repeat on Cooler and Warmer.
5. With Automatic CPU speed off, and for a game whose options pick Battery
   saver or a CPU limit, the guard still lowers `scaling_max_freq` when hot and
   puts the game's value back once cool.
6. Playing while charging: record the battery's `temp` and whether a battery
   limit ever decides (`battery … °C` in the `heat` lines); a gauge stuck at
   25 °C is harmless.
7. `kill -9` JukaHub during a boosted game: the next start logs `restored …
   CPU setting(s)` and the floor, ceiling and parked cores are as before the
   game.
8. Smart Pro S: both policies step together, the slow cluster at the same
   share of its range, and the fan is at full speed before the guard's first
   step.
9. A NextUI pak (minarch on the userspace governor) shows "the emulator sets
   its own clock" and is only held under the dial.
**Verify.** Record in the HW log; turn each model's peaks into
`heatBaseLimits` changes where a model runs cooler or hotter than its limits
assume.

### MENUBOOST-HW-1 — Faster menus, the screen thread first and the post-game CPU restore on every TrimUI model (P1, M) [HW]
**Why.** Juka, 2026-11-22: "Optimize JukaHub to run even faster and have
better performance on Trimui devices...feel free to manage cpu freq".
`uiboost.go` lifts the hub session's ceiling to the top the heat allows and
its floor to 75% of the top for 1.5 s after a press (4 s after a game, 6 s at
the start, always in the Performance power mode), inside Automatic CPU
speed's own records (`autoperf.go`); `adaptive.go` lets a press lift Low
power's 24 fps cap and steps to 60 fps while boosted and frames cost 11 ms or
less; `saver_power.go` ends an awake frame's sleep on a press (8 ms poll);
`uiprio_linux.go` runs the main thread at nice −5 with SCHED_RESET_ON_FORK;
`powerprofile.go` puts back the floor, ceiling and parked cores a game's
launch script changed. Written without a Go toolchain or a device: the
`ondemand` governor's real sampling rate, the frame cost at 1.6 GHz and the
battery cost are guesses.
**Where.** `uiboost.go`, `autoperf.go` (`autoPerfLoop`,
`autoPerfFollowLocked`, `autoPerfWriteLocked`), `adaptive.go` (`Delay`,
`noteFrameCost`), `saver_power.go` (`framePressSleep`, `saverPowerTick`),
`uiprio_linux.go`, `powerprofile.go` (`launchHubCPUSnapshot`,
`launchHubCPURestore`); the row in `setmodel.go`; docs/DEVICE_TUNING.md
(Faster menus), docs/CONFIGURATION.md.
**Done when.** On a Smart Pro, a Brick, a Brick Pro and a Smart Pro S, each
step holds or a FIX task names what differs:
1. The log has `screen thread … at nice -5` and `faster menus: first boost,
   Home held at … (for 6s)`; field 19 of `/proc/<pid>/task/<pid>/stat` reads
   -5, and a game started from the library shows nice 0 in `top`.
2. Pressing Down on Home: `scaling_min_freq` reads the boost floor (1.6 GHz on
   the Smart Pro) at once and the old value 1.5 s after the last press; the
   Automatic CPU speed row reads `Home · 1.6–2.0 GHz while you press, then …`.
3. `JUKAHUB_FRAMESTATS=1` while scrolling a long list: the `[frame]` lines
   show about 60 fps where frames cost 11 ms or less and 30 fps on heavier
   screens; 24 fps only once nothing was pressed for 1.5 s (Low power on).
4. The dock or the starfield still lowers its clock after its timeout (no
   `read-only` warning in the `idle` lines), with no boost floor left.
5. Battery Saver, a warm chip (Heat limit Cooler, a long session) and the
   voice orb's answer get no boost; the Performance power mode keeps the floor
   up while the hub is in front and drops it in a game.
6. A GBA game whose `cpufreq.sh` parks cores: after quitting, every core that
   was online before is online (`/sys/devices/system/cpu/cpu*/online`),
   `scaling_min_freq` is back, and the log says `put back N CPU setting(s) the
   game's launch script changed`.
7. Battery and heat: an hour of browsing with Faster menus on and off (the
   battery graph), and the chip's temperature after ten minutes of scrolling.
**Verify.** Record in the HW log; move `uiBoostFloorPct`, `uiBoostPressHold`
or `frameFastMS` where a model needs it.

### FASTSTART-HW-1 — Faster game starts and the single launch sync on every TrimUI model (P1, M) [HW]
**Why.** Juka, 2026-11-23: "Improve performance on Trimui devices even
further...improve emulator launching...". `racfgscope.go` now writes a library
launch's RetroArch edits as they come and syncs the journal, then each file,
once just before the game starts (the launch batch; restores wait while it is
open). `faststart.go` starts a game's Automatic CPU speed session at the top
the heat allows, with the floor at 75%, for 8 s (`fastStartBurstHold`, not
learned). For 10 s after the start (`fastStartWindow`) it raises the game
disk's `read_ahead_kb` to 1024, steps JukaHub's threads to nice 10 and I/O
best-effort level 7 (SDL sound threads skipped, `faststart_linux.go`), and runs
the start warm-up (`gameWarmAtStart`). Written without a Go toolchain or a
device: the fsync cost per edit on a FAT card, the start-time gain, whether the
stock firmware's I/O scheduler honours I/O levels (CFQ does; noop and deadline
do not) and the 8 s / 10 s lengths are guesses.
**Where.** `faststart.go`, `faststart_linux.go`, `racfgscope.go`
(`raScopeBatch*`, `raScopeSaveJournalLocked`), `savestate_resume.go`
(`editRetroArchConfigWith`), `gamesuspend.go` (`screenChildStart`,
`noteGameChildExited`), `game_library.go` (`launchLibraryGame`), `autoperf.go`
(`autoPerfBeginLocked`, `autoPerfAdaptLocked`, `autoPerfWriteLocked`),
`heatguard.go` (`heatGuardCurIdxLocked`), `gamewarm.go`, the row in
`setmodel.go`; docs/DEVICE_TUNING.md (Faster game starts), docs/CONFIGURATION.md.
**Done when.** On a Smart Pro, a Brick, a Brick Pro and a Smart Pro S, each
step holds or a FIX task names what differs:
1. A GBA game played twice (so its level is learned): the log has `faster game
   start: … at 1.6–2.0 GHz for 8s, then …` (the Brick's clocks differ), and
   `scaling_max_freq` / `scaling_min_freq` read the burst values for about 8 s
   after the start and the learned level after it.
2. With Settings → Developer → Debug logging on, each RetroArch launch shows
   one `launch settings: N edits in M files synced once at the start` line; the
   game starts with every launch value in place (Continue resumes, the OSD
   preset shows, `data/gamediag/retroarch.log` is written).
3. Time from A to the first frame (a phone's slow-motion video) for a GBA, a
   PS1 and an N64 game with the row on and off, and with
   `JUKAHUB_LAUNCH_BATCH=0`: record the three numbers per model.
4. `cat /sys/block/mmcblk*/queue/read_ahead_kb` reads 1024 during the first
   10 s of a GBA game and the old value after; `cat
   /sys/block/mmcblk0/queue/scheduler` is recorded (CFQ or BFQ honour the I/O
   step back).
5. Field 19 of `/proc/<pid>/task/*/stat` shows 10 for JukaHub's threads during
   the window and the old values after, including -5 on the main thread;
   music played under a game does not stutter during the window.
6. A game quit within 5 s: the read-ahead and the priorities are back at once,
   and the debug line `… over after … (the game ended)` appears.
7. A heavy console (PSP on the Smart Pro): no burst line (CPU boost holds it),
   the read-ahead stays 1024 for the whole game (the heavy-game boost's), and
   no `threads behind the game` in the window line.
**Verify.** Record in the HW log; move `fastStartBurstHold`, `fastStartWindow`
or `fastStartHubNice` where a model needs it.

### BUTTONS-HW-1 — No stick clicks or keyboard keys in the hints, the library's Select menu and the music player's Start on every TrimUI model (P1, S) [HW]
**Why.** Juka, 2026-11-22: "I see some spots on the os where it says r3 as a
button, which is not included on trimui smart pro...or many other devices...
reconfigure menu appropriately...make sure buttons for the most part are
consistent". `stickclicks.go` (`deviceHW.ClickSticks`: Smart Pro S and Brick
Pro only) took every JukaHub action off L3 / R3: the Games library's Select
menu (`game_library_menu.go`), the browser's Select menu rows, the music
player's Start → Audio panel with Now playing rows
(`music_nowplaying_rows.go`, `sceneClaimsStart`), the light gun's spare R
shoulder; Game hotkeys, RetroArch hotkey buttons, the Chord button, the
controller test and the DS chart follow the model. Written without a Go
toolchain or a device: whether a Smart Pro's Menu key really reaches the
library as BACK with `padEventRaw` = GUIDE (so it still goes back) is read
from the code only.
**Where.** `stickclicks.go`, `game_library_menu.go`, `game_library_ui.go`
(`ctrlGameLibrary`, `gameLibraryFooterHints`), `game_random.go`, `browser.go`,
`musicplayer.go` (`ctrlMusicPlayer`, `musicPlayerHintLines`),
`music_nowplaying_rows.go`, `musicsettings.go`, `startmenu.go`,
`lightgun_aim.go`, `lightgun_ui.go`, `gamehotkeys.go`, `rahotkeys_ui.go`,
`a11y.go`, `ctrldiag.go`, `brickkeys.go`; docs/CONTROLS.md.
**Done when.** On a Smart Pro, a Brick, a Brick Pro and a Smart Pro S, each
step holds or a FIX task names what differs:
1. No footer, card or sheet names L3 or R3 on the Smart Pro or the Brick
   (Games library, browser, music player, light-gun practice, Settings →
   Controls, the controller test, a DS game's control chart); on a Brick the
   RetroArch hotkey rows say F1 / F2.
2. Games library: Select opens the menu, A on *Random game from …* lands on a
   game, and Select, A rolls another; the Menu key (voice off) still goes back;
   *Show only a tag…*, *Search*, *Fill missing box art* and *Rescan the card*
   do what L3, R1, L1 and Y do.
3. Music player (Auralis and StockOS): the footer names A, X, L1/R1, Y, Start,
   Select, L2, R2 and B; Start opens the Audio panel on *Full-screen
   visualizer*; Lyrics, Sleep timer, Shuffle, Repeat, Speed and Auto-exit
   change from it; Start closes it. iPod layout: Start plays and pauses.
4. On a Brick, F1 / F2 open the Controller card and Quick Settings in the
   browser and the music player too, and do nothing in the Games library.
5. Light gun: with R1 the trigger, R2 re-centres the crosshair in a gun game
   and on the practice target.
**Verify.** Record in the HW log with a screenshot per screen.

### LIBRARY-HW-1 — The Library theme on a Smart Pro and a Brick (P2, S) [HW]
**Why.** Juka, 2026-11-23, with a screenshot of a media-library app: "Create a
theme based on and call it library". The Library style (`themelibrary.go`,
`hometabs_library.go`) draws its banner, posters and boardwalk with
`SDL_RenderGeometry` fans and quads and sets its clock and titles in an
embedded serif (`librarytheme_serif.ttf`). Written without a Go toolchain or a
device: the frame cost, the serif's rendering and the banner picture are
unchecked.
**Where.** `themelibrary.go`, `hometabs_library.go`, `hometabs_backdrop.go`
(`tabsBackdropBehind`), `hometabs_draw.go` (`tabsDrawStrip`, `tabsDrawLevel`),
`hometabs.go` (`homeTabsViewsKeyNow`, `tabsMove`), `homelayout.go`
(`drawHeader`); docs/THEMES.md, docs/HOME_TABS.md.
**Done when.** On a Smart Pro and a Brick, each step holds or a FIX task names
what differs:
1. Theme presets → Styles → Library: Home is the Tabs Home; the top bar starts
   with the serif clock over the date and the right cluster shows no second
   time; the tabs sit in one capsule; every tab is in the Library view; the
   boardwalk runs from under the banner to the bottom edge.
2. Walking the Recent and Games rows holds the frame rate the Grid view holds;
   a game's screenshot or save-state picture fades in over its cover in the
   banner within about a second; box art stands upright at the banner's end.
3. Left from the first tab reaches the clock, Right goes back to the tabs, A on
   the clock opens the calendar, Down enters the page.
4. A plays the focused game and X opens its sheet; a Japanese title is set in
   Inter; Settings → Accessibility → Bold text sets the clock and titles in
   Inter.
5. Another theme brings back its own views; Library again brings back the
   Library views.
**Verify.** Record in the HW log with a screenshot per step (screenshot mode).

### MAIL-1 — Email and calendar on a device (P0, M) [HW] [NET]
**Why.** The 2026-11-17 Email and calendar pass (`mailcal_store.go`,
`mail_*.go`, `calsync*.go`, `mailcal_ui.go`, `companion_mailcal.go`,
`aitools_mailcal.go`; `docs/EMAIL_CALENDAR.md`) replaced the earlier email plan and was written
without a Go toolchain, a device or a real mailbox. Session notes
(notes-mail/cal/integ) list every guess.
**Done when.**
- *Phone cards:* Tools → Email & calendar → Add an account from your phone
  opens the companion at the Email card; a Gmail, an iCloud and a Fastmail
  address fill their servers and show the app-password help; a wrong password
  comes back as the card's error; a right one closes the code, toasts and the
  first sync runs. A Google secret iCal address and a Nextcloud CalDAV
  calendar add from the Calendars card; Remove on the page and on the handheld
  forgets them (cache files gone).
- *Mail:* inbox, reader, reply and a new message on Gmail (465/TLS) and an
  STARTTLS server (587); the unread badge on a pinned Mail tile and in the
  Start menu follows reads made on the phone after the next sync; new-mail
  notifications respect the switch.
- *Microsoft:* with a client ID in the build, Sign in with Microsoft adds an
  outlook.com account and it syncs and sends (XOAUTH2).
- *Calendar:* events show in the clock's calendar and on the Calendar widget
  (small and tall), a reminder pops before an event (and over a RetroArch
  game), recurring and all-day events land on the right days in the device's
  zone.
- *Juka AI:* "anything new in my mail?" and "what's on this week?" run
  `mail_inbox` and `calendar_agenda`.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### MAIL-2 — Calendars that write back (P2, L) [NET]
**Why.** Calendars are read-only: an event seen on the handheld cannot be
added, moved or accepted there, and Outlook calendars only come in as a
published ICS link.
**Where.** `calsync_caldav.go` (PUT/DELETE of a VEVENT with If-Match),
`mail_msauth.go` (a Graph `Calendars.ReadWrite` scope), `calendarpopover.go`.
**Done when.** The clock's calendar can add an event (title, day, time) to a
CalDAV calendar or a Microsoft account's calendar through Graph, edit and
delete one it shows, and a conflicting ETag reloads instead of overwriting.

### MAIL-3 — Push mail with IMAP IDLE (P2, M)
**Why.** Mail arrives on the next poll (every 5–60 min), so a notification can
be an hour late while battery is spent on polls that find nothing.
**Where.** `mail_imap.go`, `mail_sync.go`.
**Done when.** With JukaHub in the foreground and the server advertising IDLE,
one connection per enabled account waits in IDLE (renewed every 25 min,
dropped on sleep and restored on wake) and a new message notifies within
seconds; polling remains the fallback.

### MAIL-4 — HTML mail through the browser engine (P3, M)
**Why.** HTML mail is flattened to text: newsletters, receipts and tables lose
their layout and their links.
**Where.** `mail_ui.go`, `mail_mime.go`, the browser engine (`webdom*.go`,
`browser.go`).
**Done when.** A message with an HTML part opens in the reader through the
browser engine with remote images off by default (one "Show images" press),
scripts never run, links open in the Browser, and the plain-text view stays
one press away.

### RENDER-HW-1 — The browser's new drawing and the Calculator on a device (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-20: "Improve Calculator and Webbrowser (it needs to
render more accurately)". Written without a Go toolchain or a device: the
reader now rasterises SVG itself (`webpage_svg.go`), keeps inline images in
their lines and sets every line on one baseline (`webpage_inline.go`), lays
`display: inline-block` siblings out as a wrapping row, draws bold in Inter
SemiBold and code in a monospace face (`webpage_fonts.go`), paints soft
shadows (`webpage_paint.go`), rounds pictures with SDL_RenderGeometry
(`drawWebImageRounded`) and sizes images on the text's CSS-px scale; the
Calculator has a new expression engine, keypad, history file and keyboard
typing (`calculator.go`). None of it has been drawn or timed on a handheld.
2026-11-23 ("improve browser rendering even further"), also unbuilt: HTML
presentation attributes as CSS hints (`webpage_hints.go`), font sizes
relative to the parent and `rem` to the root, `inherit` keywords, the
browser's UA defaults (em headings, raised `<sup>`, centred `th`, 40px
quotes), `line-clamp`, gradient text, `<center>` and centred tables.
**Where.** `webpage_svg.go`, `webpage_inline.go`, `webpage_fonts.go`,
`webpage_paint.go`, `webpage_image.go`, `webpage.go`, `webpage_flow.go`,
`webpage_hints.go`, `calculator.go`; `calculator.json` beside JukaHub; the JukaHub log
(`browser` lines "RenderGeometry is unavailable", "Error loading the reader
face").
**Done when.** On a Smart Pro (then a Brick and a Windows PC), each step holds
or a FIX task names what differs:
1. github.com/jukaLang/JukaHubV2 shows the GitHub logo and the octicons in
   the text colour, the README's badge row on one line, and its code blocks
   monospaced on a PC (Consolas); on the handheld code stays in Inter unless
   a `*Mono*.ttf` is in the fonts folder.
2. A page with Material Icons (e.g. a Google product page) shows arrows and
   crosses, not words like `arrow_forward`; a Font Awesome page shows no
   stray "…".
3. A Bootstrap page with `.list-inline` / `.btn` rows and a WordPress nav
   built from `li { display: inline-block }` shows them side by side; a card
   row with `box-shadow: 0 2px 8px rgba(0,0,0,.15)` has a soft shadow; a round
   avatar (`border-radius: 50%`) is a circle. Record the frame time while
   scrolling such a page against the previous build.
4. A page with 100+ inline SVG icons opens within a second of the text, and
   memory stays within the image budget (Settings → System → Memory).
5. Calculator: `2^3^2` = 512, `−2²` = −4, `200+10%` = 220, `sin(30)` = 0.5 in
   DEG, `0.1+0.2` = 0.3; the keypad fits the Smart Pro and the Brick without
   overlap; Right from the keys walks into the history and A uses an answer;
   the history survives a restart; on a PC typing `sqrt(2)` and Enter answers
   and q/e do not switch screens.
6. news.ycombinator.com: the orange header bar, the beige 85 % table in the
   middle of the page and tight rank/vote columns (cellspacing 0); on a
   Wikipedia article the footnote markers ([1]) are small and raised, link
   included, and headings are sized like a desktop browser's.
7. A Tailwind page: headings without size classes are the size of the text
   around them, links without colour classes are not blue, and a gradient
   heading (`bg-clip-text text-transparent`) shows in the gradient's colour
   with no coloured slab behind it.
8. A news or shop page whose card titles use `-webkit-line-clamp: 2` cuts
   each title after two lines with "…", and a page built on
   `html { font-size: 62.5% }` reads at its normal size.

---

## Defects found by reading (FIX)

Found by reading, not by running: confirm each on a build before fixing, and
delete the task with the fix. Security defects are under SEC.

### FIX-2 — Does the handheld keep time across a cold boot? (P2, S) [HW]
**Why.** The Smart Pro has an RTC (`sunxi-rtc`, `/dev/rtc0`;
docs/HARDWARE_VALIDATION.md), but whether it keeps time with the power off, and
whether the Brick has one, is unknown. Release checks, bug-report clock checks,
crash-file names and anything stamped before the first network-time sync read
the wall clock.
**Where.** `ntpsync.go`, `minSaneClock` users, `docs/DEVICE_TUNING.md`.
**Done when.** On a Smart Pro and a Brick, `ls /sys/class/rtc` and `hwclock -r`
after a cold boot with no Wi-Fi are recorded in `docs/DEVICE_TUNING.md`; where
the RTC keeps time NET-12 goes ahead, otherwise the `minSaneClock` users are
reviewed and NET-12 is closed as moot.

### FIX-14 — Release pipelines disagree (P1, M) [NET]
**Why.** `release.sh` cross-builds with cgo and no `CC` (release.yml cannot
succeed), its Windows zip omits the SDL DLLs and its asset names differ from what
`patch_swap.go:146,149` fetches; `docker-image.yml` creates a tag and draft
release for every same-repo PR and publishes a release on every push to main —
the builds the updater installs.
**Done when.** One release workflow (see REL-8); PRs build artifacts but never
tag.

---

### FIX-15 — Save sync and names that differ only in case (P2, S)
**Why.** Found while reviewing the save handoff (2026-11-19): `ssync.go`
matches files case-sensitively, so "Mario.srm" on one device and "mario.srm"
on the other count as two files; on FAT32, exFAT and Windows the copy lands on
the other file without a snapshot. The handoff now leaves such pairs alone;
full Save sync does not. The pre-launch pull in `hoff.go` also does not take
Save sync's job slot, so it can overlap a handoff of the same game (files stay
safe; the last-sync record writes can race).
**Where.** `ssync.go` (the one-sided files in the comparison), `hoff.go`
(the pull before a continued game starts), `savehandoff.go` (the rule to copy).
**Done when.** Full Save sync skips (and reports) one-sided files whose name
matches a file on the other side when case is ignored, and the hoff pull takes
the job slot or waits for it.
**Verify.** A comparison with "Mario.srm" / "mario.srm" lists the pair as a
note and copies neither.

## Performance (PERF)

### PERF-1 — Device performance numbers in `docs/PERFORMANCE.md` (P1, M) [HW]
**Why.** Render batching, frame pacing, lazy regexps and deferred boot work were
estimated, not measured, and `docs/PERFORMANCE.md` does not exist; PERF-3 and
others need a baseline.
**Where.** On the device: `JUKAHUB_STARTUP_BENCH=1` (exits after the first
frame; `startup:` line in `errors.txt`), `GODEBUG=inittrace=1` for package-init
costs, `framestats.go` (one histogram line a minute). `startup-bench.sh` is the
host harness; a `JUKAHUB_BENCH_BIN` option would let it drive a prebuilt binary.
**Done when.** `docs/PERFORMANCE.md` records, for Smart Pro and Brick on one
build: cold-boot median of 5 runs and the top package-init costs, warm boot, the
frame-time histogram while navigating Home and while a video plays, JukaHub's
idle CPU on Home over 60 s (`top -d 5`) and RSS after 10 minutes of use; that
build is the baseline later tasks compare against, and the doc stays the one
place for device performance numbers.
**Verify.** Numbers and method in the doc; regressions filed as new tasks.

### PERF-3 — Dirty-frame presentation (P1, M)
**Why.** Idle screens still redraw 15–24 times a second; on a handheld that is
battery.
**Where.** `main.go` loop, `adaptive.go`, toast/notification, download and clock
code paths.
**Done when.** A frame is drawn and presented only when something changed
(input, scene/overlay change, toast, progress tick, clock minute, video frame,
animation, network status) plus a 1 Hz safety redraw; no visible staleness.
**Verify.** [HW] idle Home CPU drops ≥ 50% vs PERF-1's baseline.

### PERF-4 — Brick scaling quality (P1, S) [HW]
**Why.** The 960×720 canvas is scaled to the 1024×768 panel with nearest
sampling (uneven 1.067× pixel doubling on text).
**Where.** `uiprofile.go`, renderer setup in `main.go`.
**Done when.** Text renders evenly on the Brick: either linear scale quality for
UI textures (nearest kept for pixel art: minigames, JukaLand) or native
1024×768 rendering; decision recorded in `docs/DEVICE_TUNING.md`.
**Verify.** Before/after photos of Home and Settings on a Brick.

### PERF-7 — Trim heavy dependencies (P2, L) [WIN]
**Why.** Compile time, binary size (~36 MB) and page-in time from the SD card.
**Where.** goja: the Browser's DOM pass (`webdom_runtime.go` with the embedded
`webdom/*.js`, `webdom_parse.go`, `webdom_module.go`, `webdom_net.go`, the
legacy pass in `webscript.go`); gopsutil: `processes.go`, `systool_disk.go`,
`systool_hw.go`, `terminal.go`, `textbrowser.go`, `plugnative.go`; zeroconf:
`textbrowser.go`, `moonlight_discovery.go`; discordgo: `chat.go`.
**Done when.** goja and the DOM pass sit behind a `//go:build !nojs` split (in
the lite build a stub `webJSRunPageFor` returns the page as it arrived and
Settings → Browser → JavaScript is hidden); gopsutil is replaced by `/proc` +
`statfs` on Linux and two Win32 calls; zeroconf by a one-shot mDNS query that
still finds Moonlight hosts; `go mod tidy && go mod vendor` run; features
unchanged.
**Verify.** Binary size and cold-cache build time before/after in
`docs/PERFORMANCE.md`; `make check` green.

### PERF-8 — Deferred-startup audit (P2, S) [HW]
**Why.** `runDeferredStartupFor` spreads post-first-frame work at ~8 ms per
frame; some tasks may not need the main thread at all.
**Where.** `startup.go`, `main.go` (`deferStartupTask` call sites).
**Done when.** Tasks > 20 ms on the device are listed (they log at Info) and every
goroutine-safe one (no SDL, no main-loop state) runs off the main thread.
**Verify.** The first interactive second on the device has no frame > 50 ms.

### PERF-9 — SDL init cost per subsystem (P3, S) [HW]
**Where.** `main.go` (`sdl.Init`).
**Done when.** Time per subsystem (video/audio/joystick/gamecontroller) is known;
slow ones initialise on demand (`sdl.InitSubSystem`).
**Verify.** Numbers in `docs/PERFORMANCE.md`.

### PERF-10 — VSync evaluation (P3, S) [HW]
**Why.** The renderer has no `PRESENTVSYNC`; pacing is in software.
**Done when.** Tearing and pacing compared with `SDL_HINT_RENDER_VSYNC=1` on both
devices; the smoother option is the default.

### PERF-11 — Release scene-owned textures when a scene closes (P2, M)
**Why.** Caches are bounded but stay full after leaving a heavy scene (Browser,
Chat, Media hub, Library art) on a 1 GB device.
**Where.** `scene.go` (module registry), the per-scene caches listed in
`docs/DEVELOPMENT.md` (cache inventory).
**Done when.** Leaving a scene trims its scene-specific caches (not the shared
text/shape caches); re-entry warms them again without a visible pop.
**Verify.** `JUKAHUB_TEXSTATS=1` shows the drop after leaving Browser/Chat.

### PERF-12 — Per-frame allocation sweep (P2, M) [WIN]
**Why.** Per-frame text layout and some list snapshots still allocate (e.g.
`downloadRowsFor` snapshots and groups the whole queue every frame).
**Where.** `downloadsui.go`, list renderers, `textcache.go`.
**Done when.** `go test -bench` style allocation probes (temporary, not committed
as tests) show 0 allocs/frame on Home, Library and Downloads at rest.
**Verify.** pprof alloc profile of a 60 s idle session on Windows attached.

### PERF-13 — Binary size budget (P3, S) [WIN]
**Done when.** Size per package measured (`go tool nm -size -sort size`); the top
10 contributors listed in `docs/PERFORMANCE.md` with a plan; `-trimpath` added to
release builds.

### PERF-15 — What page scripts cost on the handheld (P1, S) [HW]
**Why.** On the A133P the 550 KB DOM prelude is compiled once per run (started
beside the first page's download by `webDOMPreludeWarm`), runs again for every
page, and goja interprets every page script on four Cortex-A53 cores inside a
10-second budget; none of it has been timed on a device.
**Where.** `webdom_runtime.go` (`webDOMPrelude`, `webDOMPreludeWarm`, `runPage`,
the `scripts on …` summary line), `webdom/*.js`, `docs/PERFORMANCE.md`.
**Done when.** `docs/PERFORMANCE.md` records, on a Smart Pro and a Brick, the
prelude compile, the per-page start (prelude run and boot) on a near-empty page,
and the whole pass on a jQuery site, a framework single-page app and a news
article with lazy images (add a debug line for compile and start if the summary
is not enough); the per-page start is under 300 ms, or the interfaces pages
rarely touch are installed on first use until it is.
**Verify.** The `scripts on …` lines in `logs.txt` with
`JUKAHUB_LOG_LEVEL=debug`.

### PERF-17 — Animated wallpapers on the handheld: memory and pacing (P2, S) [HW]
**Why.** GIF wallpapers are decoded on a goroutine and uploaded a few megabytes
per UI frame within a 40 MB texture budget; the budget, the 3 MB-per-frame
upload step and the decode time are estimates for the A53 and the PowerVR
driver.
**Where.** `gifwall.go` (log tag `wallpaper`; `gifWallEvict`,
`gifWallPauseForGame`), `wallpaper.go`, `adaptive.go`
(`EvictCachesForPressure`), Power tools → memory.
**Done when.** On a Smart Pro and a Brick: a 1280×720 photo GIF and a 1080p
100-frame GIF show their first frame within about half a second of being picked
and animate within a few seconds, with no frame over 50 ms in `framestats` while
frames upload; textures stay near the 40 MB budget and the big GIF logs how many
frames it kept; a 160×144 pixel-art GIF is crisp; Battery Saver and **Animate
wallpaper** off hold the first frame; the pick, and *None*, survive a restart;
an experience pack applied after a pick (and its Undo) behaves as the newest
choice; Power tools → memory shows the textures freed by a game launch (all
but the first frame) and critical memory pressure.
**Verify.** `framestats` while a GIF loads, Power tools → memory, `logs.txt`.

---

## Graphics and GPU (GPU)

### GPU-1 — Confirm the GLES2 path and batching on the stock image (P0, S) [HW]
**Why.** `opengles2` + `SDL_RENDER_BATCHING=1` are set on the device; whether
the stock SDL2 (2.30.8, `mali` fbdev-EGL backend on PowerVR) honours both is
unproven.
**Where.** `main.go` (hints), `adaptive.go` (`renderPipelineInfo`).
**Done when.** The `render pipeline` log line on Smart Pro and Brick reads
`renderer=opengles2 acceleration=gpu`; if not, the cause (video backend, missing
blob) is documented in `docs/DEVICE_TUNING.md` with a workaround.

### GPU-2 — Fewer render-target switches (P2, M)
**Why.** PowerVR is a tile-based GPU: every `SetRenderTarget` flushes the tile
memory. Shape/gradient/icon caches bake through render targets on demand, so a
cold screen can switch targets dozens of times in one frame.
**Where.** `shapeCache.go`, `dynamicbg.go` (`bgGrads`), `iconatlas.go`,
`warmSceneCaches` in `bootwork.go`.
**Done when.** Bakes are batched (one target switch per cache per frame, or
software surfaces uploaded once), and a scene change on the device has no frame
> 50 ms caused by baking.
**Verify.** framestats histogram during scene changes before/after.

### GPU-3 — Geometry instead of textures for flat shapes (P3, M)
**Why.** `SDL_RenderGeometry` (SDL ≥ 2.0.18) draws gradients and rounded fills
as vertex-coloured triangles: no texture memory, one batched draw.
**Where.** `shapeCache.go`, `ui_glass.go`, `backgroundfx.go`. `liquidbg.go`
(the liquid background) is the first `RenderGeometry` user: reuse its
allocation-free vertex/index pattern and its fall-back to the plain path when
the renderer refuses geometry, and read GPU-5's numbers before converting more.
**Done when.** Backdrop glows and glass fills draw through geometry where that
is cheaper (measured), with the texture path kept as fallback for the software
renderer.

### GPU-4 — Hardware video decode through the firmware's CedarX libraries (P2, L) [HW]
**Why.** ffmpeg software decode is the largest CPU cost while playing video.
**Where.** `docs/VIDEO_VPU_OFFLOAD.md` (design + five probe questions),
`videoplayer.go` (`ffmpegDecoderArgs` pipe contract: yuv420p).
**Done when.** The probes are answered on a TG5040 image; if `/dev/cedar_dev` +
the vendor libs work, a small aarch64 helper decodes H.264 to the same yuv420p
pipe with automatic ffmpeg fallback. Never generalise to the A523 Smart Pro S.
**Verify.** CPU during 720p H.264 playback before/after.

### GPU-5 — Liquid background on the stock GE8300 (P1, S) [HW]
**Why.** The liquid background is the first code that draws through
`SDL_RenderGeometry`: a 129×73-vertex mesh (97×55 under Low Power) plus an
additive glint mesh, every frame the water moves, and one read-back of a still
wallpaper on its first draw. Whether the stock SDL2 on PowerVR takes that at 30
fps, and how strong the refraction and glints look on the panel, is unmeasured.
**Where.** `liquidbg.go` (log tag `liquidbg`: "the renderer cannot draw the
water" means the geometry was refused; "the water costs … ms a frame" is the
perf guard stepping down), `liquidbg_motion.go`, Settings → Theme & look →
Liquid background (R2 cycles Off/Slight/Medium/Lively).
**Done when.** On a Smart Pro and a Brick at Medium and Lively, with Low Power
on and off: moving water holds about 30 fps in `framestats` with no step-down
line and costs at most about 3 ms a frame over the calm baseline; a focus move
ripples under the tile on Home, in Settings and on a module screen with no
facets, no seam under the header band and no blocky wallpaper pixels; over a
still and a GIF wallpaper the picture bends, the dim and blooms sit over it, and
the first draw over a new wallpaper hitches at most one frame; a light theme
keeps the ripples readable; two idle minutes later frame cost is back to the
no-water baseline; ten minutes of battery on Home with Medium water against none
are recorded in `docs/PERFORMANCE.md`. On a model with an accelerometer, tilting
piles the water downhill (else apply the IIO mount matrix).
**Verify.** `framestats` and the `liquidbg` debug lines; retune the refraction
and glint strengths in `liquidbg.go` from photos of the panel.

---

## Architecture and build (ARCH)

### ARCH-1 — Package split, step 1: pure leaves (P1, M) [WIN]
**Why.** Every edit recompiles ~300k lines of `package main`; that is the
compile-time problem. Only leaves can move without a rewrite.
**Where.** `vecraster.go`, `icondefs.go`, `retry.go`, `browserpsl.go`,
`ramsearch*.go`, `ppsspp_calibration.go`, `port_compat.go`, `lyrics.go`,
`textutil.go`, `scenetasks.go` (they reference no other file of the package).
**Done when.** Each lives in `player/internal/<name>` with exported API; `main`
keeps thin aliases (`var retryDo = retry.Do`) so call sites stay unchanged;
`make check` green after each move.
**Verify.** `go build -x` shows the moved packages cached on an edit to `core.go`.

### ARCH-2 — Package split, step 2: logging (P2, S) [WIN]
**Where.** `logging.go`, the `appLog` ring in `hardware.go`.
**Done when.** `internal/logx` owns the handler and ring; `main` keeps
`logDebug = logx.Debug` aliases; 127 dependents untouched.

### ARCH-3 — Package split, step 3: models and parsers (P2, L) [WIN]
**Where.** PDF (`pdfreader.go`, `pdfv_doc.go`), EPUB/HTML reader (`reader.go`),
web page model (`webpage*.go`: split layout from drawing), feeds/podcasts,
M3U playlists, patch engine (`patch_*.go`: journal, staging, signatures),
emulator catalogs (`emusystems.go`, compat tables), media library scanner
(`mlib.go`), JSON stores (atomic write + `.bak` from `config_health.go`).
**Done when.** Each moves with SDL-free code only; one PR per package.

### ARCH-4 — Package split, step 4: feature islands (P3, XL) [WIN]
**Where.** chat (Discord), Wi-Fi, party/peer, JukaLand, minigames, smart home,
RetroAchievements.
**Done when.** Each island talks to `main` through a small interface; split into
one task per island before starting.

### ARCH-5 — Shrink the main loop (P2, L) [WIN]
**Why.** `main.go` (~4.3k lines) still owns the loop, input routing and
teardown; `core.go` (~7.7k) is referenced by most of the package.
**Done when.** The loop body is a short sequence of named phases (input, update,
draw, present, pace) in their own files; `core.go` is split by responsibility
(render helpers, triggers, video entry points) without behaviour change.

### ARCH-6 — Context cancellation for the remaining feature fetches (P2, M)
**Why.** Leaving a screen should stop its network work, not just hide it.
**Where.** Bare GETs: `thumbstore.go:91` (`httpClientLong.Get`),
`chat.go:215,396`, `themecatalog.go:121`, `tools.go:764`; plus the files that
still build requests with `http.NewRequest` instead of `NewRequestWithContext`
(grep). `scenetasks.go` is the pattern.
**Done when.** Every scene-owned fetch runs under the scene context.
**Verify.** Leaving Podcasts, Weather or Library art mid-fetch closes the
request (server-side observation in a temporary test).

### ARCH-7 — Retire the 0.4.0 migrations on 2027-05-01 (P3, S)
**Why.** Upgrade shims (`home_migrate.go`, `migrateLegacyUserFiles` in
`extras.go`, `pkgMigrateLegacy` in `pkgstate.go`, `setLegacySection` in
`setmodel.go`, the Tools hub's legacy `Misc` scene, `legacyDecryptionKeys` in
`crypto.go`) protect users upgrading from 0.4.0. Six months of dated releases is
long enough for every installed copy to have migrated.
**Done when.** Each shim carries a `// remove after 2027-05-01` comment now; on
or after that date the shims are removed with their tests and `make check` stays
green.

---

## Tests and CI (TEST)

CI runs two jobs on a push (`ci.yml`) and the slow checks weekly (`weekly.yml`);
keep it that small: new checks go to `weekly.yml` or to `make` targets.

### TEST-1 — A scene-dump tool, not a test (P1, M) [WIN]
**Why.** The visual audit was deleted with the render tests; design work needs
every scene rendered to PNG.
**Where.** New `player/tools/scenedump/` (a `go run` program reusing the player
package through an exported entry point or a build tag).
**Done when.** `go run ./tools/scenedump -out tmp/scenes` writes every config
scene and the main Go pages at 1280×720 and 960×720 (SDL dummy driver, software
renderer) in under a minute.
**Verify.** The PNGs exist and look right; `docs/DEVELOPMENT.md` documents it.

### TEST-2 — Tests for the riskiest paths the cut left uncovered (P1, M)
**Why.** A few silent-failure paths have no test.
**Done when.** One fast table test each for: the self-update restart handshake
(`patch_swap.go` markers ↔ `launch.sh` protocol), browser cookie "forget" taking
the `.bak` with it (`browsercookies*.go`), and the Wi-Fi supplicant socket
transport (`wifi_ctrl.go`, stubbed socket). (`isLaunchableAppPath` is covered by
`paths_test.go`.)
**Verify.** `go test -run` for the three; total suite time stays under ~10 s.

### TEST-8 — Fuzz the parsers (P2, M)
**Why.** The weekly `fuzz` job in `weekly.yml` covers only the update decoders
(`internal/jdelta`, `internal/releasesign`); the external-file parsers in
package main have no fuzz target.
**Where.** `weekly.yml` (`fuzz`, CGO off — parsers in package main need a CGO
job or a move to `internal/`, see ARCH-3), the parser files.
**Done when.** `go test -fuzz` targets exist for M3U, CUE/GDI/M3U sheets, the
PDF and EPUB readers, zip/tar extraction (`extractTarMembers`), package
manifests, `/proc/bus/input/devices` and the XML config; the weekly checks run
each for 30 s and keep the corpus.

### TEST-15 — A reproducible build root for the helpers (P3, S) [NET]
**Why.** The Ubuntu 20.04 root is debootstrapped from the day's focal-updates
and the pip packages are pinned by version only, so two runs of one commit can
link different OpenSSL or compiler builds.
**Where.** `.github/scripts/build-root.sh` (`create`, which every arm64 build
uses), `player/native/build-helpers.sh`, `player/native/build-runtimes.sh`.
**Done when.** The root comes from a dated `snapshot.ubuntu.com` archive, pip
installs with `--require-hashes`, and the `BUILDINFO.txt` of the helpers,
AndroidRT and the Box64 pack (built in the same root, Box64 with gcc 10 from
focal's universe) names the snapshot.

### TEST-16 — A supported OpenSSL in the Remote Play helpers (P2, S) [NET]
**Why.** The helpers link OpenSSL statically from Ubuntu 20.04: 1.1.1f with
Ubuntu's security fixes up to the end of focal's standard support (May 2025),
frozen since then. Today the stream uses it only for chiaki's own crypto, not
for TLS (curl is linked but only PSN code calls it), but PSR-8 would put TLS on
it, and a newer chiaki-ng may expect OpenSSL 3 APIs.
**Where.** `player/native/build-helpers.sh` (native mode), `remote-play-helpers.yml`
(add `perl` for OpenSSL's configure).
**Done when.** OpenSSL 3.0 or 3.5 LTS is built `no-shared` from a
checksum-verified tarball into `DEPS_PREFIX` and found through
`-DOPENSSL_ROOT_DIR`; `BUILDINFO.txt` names its version; NEEDED is unchanged.

### TEST-17 — Every helper dependency from a verified source, offline configure (P2, S) [NET]
**Why.** miniupnpc reaches chiaki-ng through its external-project flags (fetched
by full commit id, but handed to FetchContent); if chiaki-ng's configure fetches
anything itself, that download is not checksum-verified. libevent is pinned at
2.1.12-stable; 2.1.13-stable (2026-07) carries security fixes in evbuffer,
bufferevent, evdns and evhttp.
**Where.** `player/native/build-helpers.sh` (`MINIUPNPC_*`, `LIBEVENT_VERSION`).
**Done when.** miniupnpc is built statically into `DEPS_PREFIX` like the other
dependencies; configure runs with `-DFETCHCONTENT_FULLY_DISCONNECTED=ON` and
BUILDINFO lists nothing fetched by configure; libevent is 2.1.13-stable with its
tarball's SHA-256 checked against the signed release.

### TEST-18 — A packager's digest for the Box64 source (P3, S) [NET]
**Why.** The Box64 pack's source is pinned by commit id (`BOX64_COMMIT`, v0.4.4)
and fetched with `git fetch --depth 1`; nothing outside GitHub vouches for that
tree.
**Where.** `player/native/build-runtimes.sh` (`BOX64_COMMIT`).
**Done when.** A packager's digest for the v0.4.4 release tarball (AUR, or
Debian or Alpine once they reach it) is checked once against the tree at the
pinned commit, and the comment above `BOX64_COMMIT` names that source.

### TEST-19 — Upload before the advisory steps, bound the weekly smoke, fix the cross-build docs (P2, S) [NET]
**Why.** In `remote-play-helpers.yml` (AndroidRT) and `wine-runtime.yml` the
verified package is uploaded only after an advisory step (the arm64 smoke suite,
the Wine start), so a failure or hang there can cost the package. The weekly
AndroidRT smoke step has no bound of its own inside its job's 10 minutes, so a
few hung cases turn the weekly run red. `docs/BUILDING.md` and
`build-runtimes.sh` offer `CROSS=aarch64-linux-gnu-` for AndroidRT, which
today's cross toolchains (glibc 2.34 or newer) always fail at the GLIBC 2.31
check.
**Where.** `.github/workflows/remote-play-helpers.yml`,
`.github/workflows/wine-runtime.yml`, `.github/workflows/weekly.yml`,
`docs/BUILDING.md`, `player/native/build-runtimes.sh` (usage text).
**Done when.** Each package's upload and keep steps run right after its build,
before the advisory step; the weekly smoke step has `timeout-minutes: 5`; the
docs say a cross build needs a sysroot whose glibc is no newer than 2.31.

---

## UX and design (UX)

The review behind these is [docs/UX_REVIEW.md](../docs/UX_REVIEW.md).

### UX-4 — One icon stroke weight (P2, M)
**Where.** `icondefs.go`, `icondraw.go`, `iconatlas.go`.
**Done when.** Every 32 px glyph uses one stroke weight and outline style (the
Packages glyph is a filled 3D box among outlines); atlas re-baked.

### UX-5 — Status cluster legibility (P2, S) [HW]
**Done when.** A connected Wi-Fi meter draws at full contrast (dim only for "no
signal"); battery/clock/bell readable on both canvases at arm's length.

### UX-7 — On-device design walk (P1, S) [HW]
**Done when.** Home, Start menu, Games library, Exit, Settings, Tools and Media
are walked on a Smart Pro and a Brick: focus halo visible over bright box art,
14 px floor text fits inside pills (package card overline, media hub badges,
Wi-Fi badge), Remote Play capsule width, footer hints on the 960 px canvas, the
first-run Continue card. Findings filed as tasks with photos.

### UX-8 — Touch targets and safe area everywhere (P2, M)
**Why.** `growTouchTarget` covers Home, the top bar, the bell and the browser;
scene-specific panels and the panel's rounded-corner margin are not done.
**Where.** `touchtarget.go`, scene panels.
**Done when.** Every pressable control has a ≥ 44 px hit area centred on what is
drawn; no control sits in the 32 px corner margin.

### UX-9 — Every state for every component (P2, M)
**Why.** Empty/loading/error/offline/success should look the same everywhere.
**Done when.** The ten most-used scenes use `UIEmptyState`/`UIErrorState`/
`UILoadingSkeleton` and the offline treatment from `offlineaware.go`; a list of
remaining exceptions is in the PR.

### UX-10 — First-run flow review (P2, S) [HW]
**Where.** `onboarding.go`, `setupchecklist.go`, `controlslegend.go`, Home's
first-run Continue card.
**Done when.** A new user reaches "add games / connect Wi-Fi / pick a theme" in
≤ 3 presses from first boot; the controller legend matches the device.

### UX-12 — Every screen on the tokens (P1, M)
**Why.** Hard-coded colours and sizes bypass themes, High Contrast and the
colour-blind palettes: `companionui.go` (opaque slate cards),
`quicksettings.go:109-133` (a literal colour per toggle),
`notifications.go:607-641` (literal priority dots), `dashboard.go` (16 literal
colours), `pomodoro.go`, `media_hub_render.go` (19 literal sizes),
`pkgui.go:349,400` (white/black overlays that break light themes), and
`videoplayer.go`, `dynamicbg.go`, `games.go`, `canvas.go`.
**Done when.** Each uses `ui_glass.go`/`design.go` tokens and roles; a grep for
`colorNew(`/`sdl.Color{R:` in UI code lists only documented exceptions (art,
game palettes).

### UX-15 — A motion system (P3, M)
**Done when.** Enter, exit, focus and sheet transitions use the curves in
`motion.go` everywhere, all honour reduced motion and the Animations speed
(Comfy / Snappy since 2026-11-20, `motionspeed.go`, which today scales scene
fades, overlay entrances and the focus glide), and the Start menu, sheets and
toasts share one entrance.

### UX-16 — Trophy set two on a device, and its leftovers (P2, S) [HW]
**Why.** About 386 trophies (`trophy_set2.go`, the event API in
`trophy_events.go`, one-line hooks across the features) have not run on a
device. Known gaps: games marked Completed and JukaLand blocks placed before the
update are not carried over; an artist is only known once Media's library was
opened in the session (`mlibCurrentModel`).
**Where.** `trophy_events.go`, `playtime.go`, `trophy_set2.go`,
`trophy_room.go`, `trophy_celebrate.go`, the hook sites (grep `trophyEvent`).
**Done when.** On a Smart Pro and on Windows the first start on an old save
records its catch-up quietly (no card, no row, no count on the cup) and a
restart keeps every trophy and setting; the catch-up seeds Completed games from
`gmeta` and JukaLand blocks from the saved JukaLand stats; artist trophies count
from the saved library index without opening Media; the Category chip lists the
groups with their counts; a screenshot, a track, a page and a Snake round move
their trophies' progress; the platinum's count leaves out what the setup cannot
award and the bonus trophies. RetroAchievements' 12 cards per sync
(`trophyRANewUnlocksMax`) stay a documented limit.

---

## Games and emulation (GAME)

### GAME-2 — Save timeline (P1, M)
**Why.** Lost saves are the loss that actually happens; snapshots already exist.
**Where.** `savemanager.go`, `savemanager_ui.go`, `timelapse.go` (knows when a
launch starts/ends).
**Done when.** While a game runs, its save roots are snapshotted every N minutes
(setting, default 10, bounded count); Save Manager shows a per-game timeline
with "restore this moment" (using the existing integrity-checked restore).
**Verify.** [HW] a RetroArch session's mid-play snapshot restores after exit.

### GAME-3 — Named checkpoints for RetroArch games (P2, M)
**Why.** The game switcher (`gameswitcher.go`) already covers quick switching
through the auto state (the design's Phase 2); there is still no way to keep a
named moment of a game and go back to it later.
**Where.** [docs/CHECKPOINTS.md](../docs/CHECKPOINTS.md) (Phases 0 and 1);
`savestate_resume.go`, `gmeta_states.go`, `savemanager.go` (snapshot format),
`game_options_ui.go`.
**Done when.** Phase 0's numbers (state size per core, free RAM during a
RetroArch session) are in the design doc; the options sheet's **Checkpoints**
row pins the auto state after a clean exit under a typed name (manifest with a
SHA-256 per file under `save-manager/checkpoints/<game>/`), and lists, restores
(verified, before launch), renames and deletes checkpoints; other launchers get
a one-line explanation. Suspended emulators (Phase 3) stay out.

### GAME-4 — Per-game controller remaps for RetroArch (P2, M)
**Where.** `gameoptions.go`, `game_options_ui.go`, `inmap*.go`.
**Done when.** The game options sheet writes a RetroArch per-content remap file
(and `--appendconfig` where needed) from a small editor; standalone emulators
are explicitly out.

### GAME-6 — Download RetroArch's shader and overlay assets (P3, S) [NET]
**Why.** Per-game picture looks (`videofilter.go`) and per-console overlays
(`ratuning_settings.go`) are offered only when their files are already on the
card; JukaHub cannot fetch them.
**Where.** `videofilter.go`, `ratuning.go` (overlay folders), `cheats_db.go`
(the buildbot download pattern to copy).
**Done when.** The picture and overlay rows offer a download when nothing is
found: libretro's buildbot `shaders_glsl.zip` and `overlays.zip` (the host
`cheats.zip` comes from) download over HTTPS with progress and cancel, unpack
into the folders RetroArch's config names, and the rows list them afterwards.

### GAME-8 — Rich metadata provider (P3, M) [NET] [OWNER]
**Done when.** Descriptions/genres/dates come from a provider with an account
(ScreenScraper or TheGamesDB) behind the existing downloader, opt-in, with the
key stored through the secret store.

### GAME-9 — RetroArch hotkeys per device, measured (P2, S) [HW]
**Why.** Hotkey binds are numeric button codes; guessing breaks controls.
**Where.** `osdpreset.go` (never writes `input_*` today, by design).
**Done when.** Measured profiles for Smart Pro and Brick exist and the OSD preset
can write menu/screenshot/fast-forward hotkeys for those devices only.

### GAME-10 — Netplay: invites, passwords, plain RetroArch hosts (P3, S)
**Why.** LAN rooms and the online lobby work (`netplay_rooms.go`), but a room
cannot be shared by link, password rooms are listed and refused, and a PC
running plain RetroArch is not listed.
**Where.** `netplay_rooms.go`, `netplay_ui.go`, `companion.go`.
**Done when.** A hosted game can be shared as a link/QR the phone companion
forwards to a friend's JukaHub; a password-protected online room asks for the
password (vkTarget, submit-only) and passes it via the host overlay's
`netplay_password`; RetroArch's own LAN discovery reply on UDP 55435 is parsed
so a PC running plain RetroArch is listed too (read the ad-packet layout from
RetroArch's `netplay_private.h` first; it changed in 1.10).

### GAME-11 — Moonlight: first compile, pin moonlight-common-c, device check (P1, S) [HW][NET]
**Why.** JukaHub's own Moonlight client shipped unbuilt on 2026-11-17
(moonlight_*.go, player/native/moonlight-stream.c, docs/PC_STREAMING.md). The
helper was only compiled against stub headers, and build-helpers.sh fetches
moonlight-common-c's master tip because no full commit id could be checked.
**Where.** `player/native/build-helpers.sh` (`MOONLIGHT_COMMON_COMMIT`),
`moonlight-stream.c`, `moonlight_stream.go`, `moonlight_ui.go`.
**Done when.** The helper job's BUILDINFO.txt has a `moonlight-stream` block
(its log is `moonlight-*.log` in the helper logs artifact when it fails) and
`MOONLIGHT_COMMON_COMMIT` holds the 40-hex id it reports; on a Smart Pro and a
Brick: PC streaming → Check this handheld passes (picture, sound, A press,
rumble; note `st_p_ms`, `st_upload_ms`, `st_cpu_mhz` and the governor from
moonlight.log in docs/HARDWARE_VALIDATION.md) and, paired with Vibepollo 2.x,
the Auto preset streams with `decode_ms` close to the check's, the Brick gets
a 1024x768 virtual screen without bars, rumble, mouse mode, Disconnect and Quit the game
on the PC behave as docs/PC_STREAMING.md says, and a Sunshine host streams too.
Then tag builds can set `MOONLIGHT_REQUIRED=1`.

### GAME-13 — Per-game environment, and options that survive a rename (P2, S)
**Why.** A game already carries its own arguments, emulator, CPU profile and
picture (`gameoptions.go`, `data/game-options.json`, applied in
`game_library.go`), but not environment variables, and the file is keyed by ROM
path, so a renamed or moved game loses its options.
**Where.** `gameoptions.go`, `game_options_ui.go`, `game_library.go` (where
`gameOptionsFor` feeds the launch).
**Done when.** The options sheet edits environment variables for one game
(validated names, bounded like `Args`) and the launch passes them to RetroArch
and standalone emulators; options under a path that no longer exists are
re-attached to the same content (size and hash) after a rename or move; a test
pins argument order and that an empty override changes nothing.

### GAME-16 — What else differs between JukaHub's and CrossMix's RetroArch (P2, S) [HW]
**Why.** The scoped retroarch.cfg edits and the restored environment fix the two
causes found by reading; others are possible and unverified.
**Where.** sound.go (`initAudioDevice` stays open during games), netplay.go (starts
`ra64.trimui` directly, bypassing CrossMix's common launcher: CPU, audio
switcher, inputd MENU hotkey), savemanager.go `resolveRetroArchSaveDir` and
recordings.go (`~` resolved with JukaHub's HOME, RetroArch's is its folder),
ports.go (PortMaster keeps JukaHub's environment), lowbattery.go
`retroArchConfigValue` (last occurrence of a duplicated key wins; RetroArch's
reader may keep the first).
**Done when.** Each is checked on a device (`/proc/asound/card0/pcm0p/sub0/status`
while RetroArch runs from JukaHub; netplay on CrossMix) and fixed or closed.
**Verify.** HW notes in the task; tests for any code change.

### GAME-17 — CrossMix's per-system RetroArch configs (P2, S) [HW]
**Why.** JukaHub's launch-time RetroArch settings (save-state resume,
recordings, the OSD preset and now the spatial-audio filter) go into the
configs `raScopeTargets` finds. A CrossMix launcher may start RetroArch with a
per-system config that never sees them.
**Where.** `racfgscope.go` (`raLaunchCandidates`, `raScopeTargets`),
`savestate_resume.go`, `recordings.go`, `osdpreset.go`, `spatial_games.go`.
**Done when.** The configs CrossMix's launchers pass are known and are part of
a launch's targets for all four, and a CrossMix game's RetroArch log shows the
values.

### GAME-18 — Device pass for the emulator updates (P1, S) [HW] [WIN]
**Why.** The daily pass that updates hakuX and, on a PC, the emulators and
RetroArch cores JukaHub installed has not run: the PC's idle moment (system-wide
idle, no full-screen program, mains power), the rollback record, the GitHub hold
and the Windows process and power calls are untested.
**Where.** `emuautoupdate_runtimes.go`, `emuautoupdate_desktop.go`,
`emuautoupdate_desktop_windows.go`, `emulatorautoupdate.go`; log lines `[patch]
emulator updates: …`.
**Done when.** On a Smart Pro: with a newer hakuX release out, about 22 minutes
after boot on the charger one toast says "Updated hakuX v…"; after R1 the next
day's pass logs that it was put back by hand and leaves it; a game started while
the card shows "Updating emulators" stops the pass within about 2 seconds, the
rest following after the game. On Windows: PPSSPP installed from the Emulators
page, with its manifest's tag edited older, updates after about 4 idle minutes
(`.backup\PPSSPP` kept, Roll back works, the next day's pass leaves it); an
emulator started from Explorer is skipped; an unplugged laptop, typing in
another app or a full-screen game outside JukaHub holds the pass, JukaHub itself
full screen and idle does not; R1 during an update is refused; with only Steam's
RetroArch the Core builds page says "Updates on demand" and its cores are never
touched; Notify me lists new builds once and installs nothing; an unchosen mode
with a JukaHub-installed emulator shows the one-time notice. With Install
automatically on, the Windows-app runtime and AndroidRT installs show a moving
byte bar, `emu-autoupdate.json` records `"<tag> (<8 hex>)"`, an old tag-only
record neither loops nor logs "put back by hand", and on Notify me the APK
Store's Runtime tab names the new AndroidRT.
**Verify.** The toasts and the `[patch] emulator updates:` lines, recorded in
the HW log.

### GAME-19 — The game assistant, second round (P3, M)
**Why.** The game assistant (`gameassist*.go`) treats every question as a new
one, shows an answer of up to eight lines, names only games started from the
library, and on a PC neither sees the game's window nor draws over it.
**Where.** `gameassist.go`, `gameassist_linux.go`, `voiceorb.go`.
**Done when.** A follow-up in the overlay keeps the last exchange; the D-pad
scrolls a long answer; launches from Apps and ports are named in the prompt; on
a PC **Juka can see the game screen** captures the game's window, and the answer
shows in an always-on-top overlay.

### GAME-20 — Fill in CrossMix's shipped core checksums (P2, S) [HW]
**Why.** A core whose CRC matches a build CrossMix ships is meant to be named
once and never replaced by the automatic pass, but `coreCrossMixShippedCRCs`
(`emulatorupdates.go`) is empty: the values must come from a real CrossMix card,
and a guessed checksum would hold back the wrong core. The "asked once" line is
also decided before the update plan, so once the table has data a held core may
be named although the index has no other build.
**Where.** `emulatorupdates.go` (`coreCrossMixShippedCRCs`),
`emulatorinstall.go` (the auto runner).
**Done when.** The table lists the CRC32 of every build the current CrossMix
release ships in `RetroArch/.retroarch/cores` (lower-case hex, CrossMix version
in a comment); the held-core line is decided after the plan, only for cores the
index has a different build of; a CrossMix card's first daily pass names those
cores once and replaces none.

### GAME-23 — FreeCell in Solitaire (P3, M)
**Why.** Juka, 2026-11-19: "Solitaire: Klondike with undo, auto-move, and a
saved game you can resume anytime. Add FreeCell later." The rules in
`solitaire.go` already work over typed piles (`solPileKind`, with
`solPileCell`), `canDrop` lets any card into an empty column outside Klondike
and handles free cells, and `nextAutoMove` plays from cells.
**Where.** `solitaire.go`, `solitaire_ui.go` (`solLayout`, `solTopRow`,
`solColumnOf`, `solTopAt` and the `solK*` constants are Klondike's).
**Done when.** A FreeCell variant (4 cells, 4 foundations, 8 columns, all 52
cards face up, 7/7/7/7/6/6/6/6) is chosen from a Game row in the menu; deals
are numbered with Microsoft's generator so deal 1 matches Microsoft FreeCell's
#1; runs obey the supermove limit, (free cells + 1) × 2^(empty columns),
halved onto an empty column; the smart move may use a free cell last;
auto-finish knows FreeCell; statistics are kept per variant (save version 2,
version 1's block read as Klondike's), with a Reset statistics row that asks
first.
**Verify.** A saved FreeCell game resumes exactly; a version-1 save still loads
with its Klondike statistics.

---

## PS Remote Play (PSR)

### PSR-1 — Wake from rest mode on real consoles (P1, S) [HW]
**Why.** The wake follows chiaki-ng's protocol byte for byte but has never met
a console, and its timings are guesses.
**Where.** `psremote_wake.go` (`psWakeSettle`, `psWakeReach`, `psWakeTimeout`),
`psremote_ui.go`.
**Done when.** On a PS5 and a PS4 in rest mode, A wakes the console and the
stream starts (raise `psWakeSettle`, 3 s, if the first stream after a wake
fails); X wakes without streaming; B stops waiting; a console shut down
completely ends in "did not answer" after 6 s and its row offers Try anyway;
the rest-mode menus the failure toast names match both consoles' firmware; a
console the router gave a new address is followed by its MAC on the next scan.
**Verify.** [HW] the above from a Smart Pro, and once from away: with the
router forwarding UDP 9302 (PS5) or 987 (PS4) to the console and its Internet
address set, does it answer discovery from outside its network and wake there?
Write the answer into `docs/REMOTE_PLAY.md`.

### PSR-2 — Every pad, rumble and motion in a real stream (P1, S) [HW]
**Why.** `chiaki-stream` opens every controller, merges them, follows hotplug,
sends the motion pad's gyro, accelerometer and orientation, and sends rumble to
the pad with the latest input (the handheld's motor only while that is the
built-in pad); only a harness with faked SDL and chiaki calls has run it, and
the motor's 30 % floor, its scale and the 10 s safety stop are guesses.
**Where.** `native/chiaki-stream.c` (`pad_open`, `pad_close`, `push_controller`,
`rumble_target`, `apply_rumble`), `native/stream_sys.c` (`ss_motion_*`, the
motor), `psremote_stream_opts.go`, `psremote.log`.
**Done when.** On a Smart Pro: the built-in pad works (the log lists it as
added) and Start + Select quits; a Bluetooth DualSense or DualShock 4 paired
before the stream, and one switched on during it, both appear in the log, and
their buttons, sticks, triggers and touchpad (click and fingers) reach the
console; a game that aims or steers with the gyro follows the pad; switching a
pad off while it holds a button leaves nothing held; with two pads, drift on the
built-in stick does not override the Bluetooth stick; a Switch-layout pad's
bottom button is Cross. Rumble: with a Bluetooth pad in hand the pad rumbles and
the motor stays still, back on the built-in controls the motor takes over; game
rumble feels like rumble, not a buzz or nothing, on a Smart Pro and a Brick; a
long rumble stops when the game stops it.
**Verify.** [HW] a stream for each case, `psremote.log` attached.

### PSR-7 — The bundled helpers start on every handheld (P1, S) [HW] [NET]
**Why.** `chiaki-stream` and `chiaki-regist` are built against glibc 2.31 for
firmware with glibc 2.33, and nothing has started them on a handheld.
**Where.** `.github/workflows/remote-play-helpers.yml`,
`native/build-helpers.sh`, `psremote_preflight.go`.
**Done when.** Each TrimUI archive has `bin/` with both helpers, and on a Smart
Pro and a Smart Pro S the panel's check lets them run; a refusal names what is
missing in the toast and in the log.
**Verify.** [HW] install a CI build, open PS Remote Play, start a stream.

### PSR-8 — Remote Play through PSN, without port forwarding (P2, L) [NET]
**Why.** Away from home, Remote Play needs a router port forward or a VPN
(`docs/REMOTE_PLAY.md` → Playing away from home). Sony's app and chiaki-ng reach
a console anywhere through PSN: a session through Sony's servers, then UDP hole
punching with UPnP.
**Where.** `psn_signin.go` (keeps only the Account-ID today), `psremote*.go`
(consoles from the PSN device list, a "through PSN" stream),
`native/chiaki-stream.c` (chiaki-ng's `lib/include/chiaki/remote/holepunch.h`:
session init, UPnP discovery, session create and start with the console's device
id, the control and data holes, handed to the session through
`ChiakiConnectInfo`; chiaki-lib at the pinned commit already builds the curl and
miniupnpc it uses). Read that header at the pinned commit before writing a call.
**Done when.** The PSN refresh token is kept only after the user turns on **Play
away from home**: encrypted with the player's key like the other API tokens
(`EncryptAPIToken`), never logged or put in a report, deleted on sign-out. A
paired console then streams from another network with no port forward, the token
renews itself, a refused or expired token says so, and the away-from-home
section of the docs describes it. Nothing changes for someone who never signs
in.

### PSR-10 — The stream controls and display on a device (P1, S) [HW]
**Why.** The stream helper's button map, on-screen display, login passcode,
codec choice and status file have run only against host fakes
(`native/tests/run-host-tests.sh`).
**Where.** `native/stream_input.c`, `native/stream_osd.c`,
`native/chiaki-stream.c`, `psremote_lastrun.go`, `psremote_stream_opts.go`.
**Done when.** On a Smart Pro and a Brick with a PS5 (and a PS4 if one is at
hand): Menu reaches the helper as the guide button and a tap opens the PS5's
control center, while a held Menu opens the stream menu instead and its **Hold
PS (Home)** reaches the PS5's home screen (the PS4's quick menu); Select clicks
the touchpad and Menu + Select is Create; Menu + D-pad swipes work in a game
that uses swipes, Menu + right stick moves a finger, and touches land where the
console expects them (PS5 1919×1079, PS4 1920×942); the display costs no visible
frames with the stream menu and the stats open; 720p30 in H.264 and in H.265 on
the A133P confirms the Auto codec choice; the log names the firmware's SDL
version (2.0.14 or newer enables a PlayStation pad's own touchpad); a profile
with a login passcode is asked for it and a saved one is entered; a failed
stream's toast and **Last stream** row appear on the next start; a console with
an Internet address streams through a port forward or a VPN; the battery
warnings appear at 15, 10 and 5 %.
**Verify.** [HW] `psremote.log` for each case; anything that is off becomes a
fix or a task.

### PSR-13 — Downloading the helpers on a handheld (P1, S) [HW] [NET]
**Why.** A JukaHub without `bin/` downloads the helpers when PS Remote Play
opens and in the background after start, and the Phone Companion's PlayStation
card sends the PSN Account-ID and follows that download; only a test server and
jsdom have run them, and no release has carried
`JukaHub-RemotePlay-linux-arm64.zip` yet.
**Where.** `psremote_helpers_install.go` (`psHelpersStop`),
`psremote_helpers_bg.go` (`psBgLoop`, `psBgScheduler.tick`,
`psHelpersActivityItem`), `psremote_ui.go` (`psOpenPanelFor`, `psPanelPair`,
`psPanelPlay`), `psremote_wake.go` (`psPanelPlayOrWake`),
`companion_psremote.go`, `companion.go` (the card), `setmodel.go` (the switch);
`.github/workflows/docker-image.yml` (the helper package upload).
**Done when.** On a Smart Pro with `Apps/JukaHub/bin/` deleted:
- *On open* (with **Download Remote Play automatically** off): *Downloading
  Remote Play…* with a moving percentage, then *Installing Remote Play…*, then
  the toast *PS Remote Play is installed*; `bin/` holds both helpers and no
  `.remote-play-install/`; A on an unpaired PS5 during the download says the PIN
  comes later, and the PIN keyboard opens by itself when it ends (pairing works
  with that PIN); Play pressed during the download streams when it ends, and B
  before then stops it; with Wi-Fi off the list offers *Install Remote Play* and
  Retry works once it is back; a download cut off mid-way resumes; the Logs
  screen shows `Remote Play helpers installed from JukaHub-…`.
- *In the background:* about a minute after start on Wi-Fi the **Remote Play**
  chip appears with a moving rail and the log shows `downloading the Remote Play
  helpers in the background`; started offline, it begins within 30 s of joining
  Wi-Fi; it does not start while a game runs; the chip's ✕ stops it (no ✕ while
  the helpers are copied into place) and the background download does not start
  again until JukaHub restarts, an Account-ID is saved or the switch is turned
  back on (opening PS Remote Play, Retry, Play or the phone's Download now still
  do); with the switch off nothing downloads until PS Remote Play opens.
- *Phone card* (same Wi-Fi): it says *No PSN Account-ID on the handheld yet*; a
  pasted Account-ID is saved (the handheld toasts *PSN Account-ID saved*) and a
  *redirect* address is traded for one; Sign in to get it opens the sign-in
  page; the card shows the download's percentage while it runs and *Remote Play
  is installed on the handheld.* after.
**Verify.** [HW] [NET] the steps above.

---

## Xbox and Xbox 360 (XBX)

hakuX runs original-Xbox games on the Smart Pro; on Windows, xemu runs them and
Xenia Canary runs Xbox 360 games (both installed and updated from the Emulators
page).

### XBX-1 — On-device hakuX validation (P1, M) [HW]
**Where.** `hakux.go`, `hakuxpage.go`.
**Done when.** On a Smart Pro: install from the Xbox tile, confirm the real zip
layout and launcher name, launch one XISO from the Games Library and from the
stock launcher, exit with Start+Select, R1 rollback after a second release (R1
returns at once and its toast follows; a file the update dropped waits in
`Apps/JukaHub/data/hakux/backup/` and comes back with R1); provenance
(`Emus/XBOX/.jukahub-hakux.json`) lists every file; with the clock set to 2001,
X answers "A secure connection could not be made — check the date and time".

### XBX-10 — hakuX installs itself; A on an Xbox game installs it (P1, S) [HW] [NET]
**Why.** Juka, 2026-11-19: "Xbox haiku X should auto install (in games show as
launcher for Xbox games)". The daily pass's once-per-card first install and the
Games Library hook were written without a Go toolchain or a device.
**Where.** `hakux_auto.go`, `emuautoupdate_hakux.go`, `emupicker.go`
(`chooseGameLauncher`), `romsetup.go`;
`Apps/JukaHub/data/hakux/auto-install.json`.
**Done when.** On a Smart Pro card without `Emus/XBOX` and Emulator updates on
Install automatically, each step holds or a FIX task names what differs:
1. On the charger, the daily pass installs hakuX (install card, tile pill, one
   toast saying where the games go) and logs `hakuX v… installed by the daily
   pass (no Xbox launcher was on the card)`; `auto-install.json` appears and
   `Roms/XBOX` exists.
2. Deleting `Emus/XBOX` and waiting a day does not reinstall it. A card with a
   stock or CrossMix `Emus/XBOX` is never touched (`hakuX: Xbox launcher folder
   … found — recorded…`); record whether either firmware ships one.
3. On a fresh card, A on an Xbox game in Games starts the install with its
   toast, and the game starts through `Emus/XBOX/launch.sh` once it is in
   (`hakuX … installed from a game's start; starting <file>`).
4. A card too full logs `hakuX v… not installed by itself: the card has … free
   …` and installs nothing.
**Verify.** Record in the HW log and fold the results into XBX-1.

### XBX-11 — Xbox boost on the Smart Pro (P1, S) [HW]
**Why.** Juka, 2026-11-19: "Improve XBOX performance". Xbox boost
(`xboxboost.go`) gives a hakuX game or a GreenOvercast stream parked cores,
full CPU and GPU speed, JukaHub's memory, nice −5 and (streams) Wi-Fi without
power saving; it was written without a Go toolchain or a device.
**Where.** `xboxboost.go`, `xboxboost_linux.go`; hooks in `game_library.go`
(`launchLibraryGame`), `ports.go` (`startPortScript`), `powerprofile.go`,
`autoperf.go`, `wakeword.go`; `/tmp/jukahub-xbox-boost.json`.
**Done when.** On a Smart Pro with hakuX and an Xbox game, each step holds or a
FIX task names what differs:
1. The log shows `boost on for <game>: …` with the caches line and `game at
   priority -5`, and `ps -o ni` (or `/proc/<pid>/stat`) shows −5 for hakuX.
2. Record whether `/sys/class/devfreq` has a GPU node on the A133P, and its
   governor during the game and after it.
3. `scaling_governor` reads `performance` and Settings → Automatic CPU speed
   reads `<game> · heavy-game boost` while it plays; after quitting, the log shows
   `boost off after … values put back` and the governor, ceiling and GPU are
   the firmware's again.
4. A GreenOvercast stream logs `Wi-Fi power saving off` where `iw` or
   `iwconfig` exists, and `iw dev wlan0 get power_save` reads on again after.
5. Note MemAvailable before/after from the `boost on` line and the in-game fps
   of one title with Xbox boost on and off.
Since 2026-11-22 an Xbox game started under launch.sh quits JukaHub instead
(XBX-12): steps 1, 3 and 5 then apply to a GameCube, Wii or PS2 game, and the
hand-off's own half of the boost is XBX-12's step 4.
**Verify.** Record in the HW log.

### XBX-12 — Xbox games with JukaHub closed (hakuX screen hand-off) (P1, S) [HW]
**Why.** A tester, 2026-11-21: the first Xbox game "stuck in the menu" with its
sound playing, and the hotkeys "only opened RetroArch menu and not the
standalone hakuX emulator". An Xbox game now quits JukaHub for its length
through launch.sh's screen-handoff protocol; written without a Go toolchain or
a device.
**Where.** `hakux_handoff.go` (hook in `game_library.go` `launchLibraryGame`),
`game_history.go` (`gameHistoryFinishHandoff`); `/tmp/jukahub-xbox-handoff.*`,
`Apps/JukaHub/data/gamediag/launcher.log`, `.screen-handoff`.
**Done when.** On a Smart Pro and a Brick with hakuX and an Xbox game, each step
holds or a FIX task names what differs:
1. A on the game shows "… is starting — JukaHub closes while it plays", JukaHub
   closes and the game's picture appears (no JukaHub frame left on the panel);
   errors.txt shows `screen handed to helper pid …`.
2. Menu opens hakuX's settings, not the firmware's quick menu; holding
   Start + Select about 1.2 s quits, and JukaHub comes back within a few
   seconds without the boot animation, on the Games Library's Xbox row.
3. The log's `back from <game> after …` line lists the script's steps; the
   game page's playtime grows by the session (sessions of 30 s or more).
4. With Heavy-game boost on, `scaling_governor` reads `performance` during
   the game and the firmware's value after; the log shows `boost for <game>
   while JukaHub is closed: …` and, after the return, `put back … value(s)`.
5. A launch.sh edited to `exit 1` at once brings JukaHub back with the launch
   card and the launcher's lines; under JukaHub OS no TrimUI quick menu is
   left running after the game.
**Verify.** Record in the HW log and fold the results into XBX-1.

### XBX-3 — Reproducible hakuX build (P3, M) [NET]
**Done when.** If the port documents its build (TrimUI SDK or
`ghcr.io/loveretro/tg5040-toolchain`), `.github/workflows/hakux.yml`
(workflow_dispatch, pinned tag) builds the package and attaches its SHA-256. Do
not guess build steps.

### XBX-7 — Xenia Canary on a Windows PC (P2, S) [WIN] [NET]
**Why.** The Emulators page installs and updates Xenia Canary and the Games
Library starts Xbox 360 games with it, written from its release pages and wiki
and never run on a PC.
**Where.** `emuwininstall.go` (the Xenia Canary spec, `emuWinHasVCRuntime`),
`emuxbox.go` (`emuStandaloneGameArgs`), `emusystems.go`, `game_library.go`.
**Done when.** On a Windows PC the newest Canary release installs
(`xenia_canary_windows.7z`; `xenia_canary.exe` found at the top or one folder
down), the manifest's tag is the release tag, a later build shows as an update
and R1 restores the one before, an `.iso` in `Roms\XBOX360` starts full screen
from the Games Library (`--fullscreen=true`), as do an extracted game (one
entry under its folder's name, with its own cover) and a `.zar`, and the Visual
C++ hint shows only
on a PC without the redistributable (also on Windows on ARM).

### XBX-8 — xemu on Windows, end to end (P2, S) [WIN] [NET]
**Why.** xemu's asset rules, the automatic `xemu.toml` fill and the
missing-firmware refusal have not met a real xemu release.
**Where.** `emuwininstall.go` (the xemu rules), `emuxbox.go` (`emuXemuScan`,
`emuXboxApply`, `emuStandaloneLaunchCheck`), `game_library.go`.
**Done when.** v0.8.139 or newer installs the non-debug build (the ARM64 one
from a windows/arm64 JukaHub), the HDD image lands in `BIOS\xbox`, BIOS files
dropped into that folder turn green on the open sheet within seconds, the
library refuses an ISO with the "xemu needs …" sentence until they are there and
then starts it full screen with `-dvd_path`, and a portable xemu whose
`xemu.toml` names its files by relative path launches with that file unchanged.

### CLOUD-1 — Validate private glibc in a live stream (P1, S) [HW] [NET]
**Implemented.** `glibc_runtime.go` installs Ubuntu noble's newest arm64
glibc 2.39 build (with libstdc++, libgcc and bash) through the signed
`Release` → `Packages` → `.deb` chain, or the pinned 0ubuntu8.12 package when
that index cannot be read, extracts bounded library files to JukaHub's data
folder, and probes its loader (2026-11-18: also PortMaster's bash). `glibc_port.go` makes a temporary
copy of GreenOvercast's launcher so PortMaster and firmware helpers stay on
the stock glibc while only the game binary uses the private loader. The
private loader resolved JukaHub and GreenOvercast on a stock TrimUI and ran
the firmware's `curl`; GreenOvercast passed glibc loading in a display-free
startup test. Archive extraction was tested against that exact package.
**Still to verify.** Build and deploy this JukaHub version on the TrimUI, then
sign in and play a live stream. Check video, audio, input, and clean exit.
The private loader is offered only for GreenOvercast; other ports still use
their normal compatibility checks. See `docs/PRIVATE_GLIBC.md`.

---

## Android games (AND)

### AND-1 — AndroidRT's present path on the Brick and the rest of the first run (P1, S) [HW]
**Why.** Penguin Dash has been played on a Smart Pro with AndroidRT
(`docs/ANDROID.md`), but the Brick, the backend log lines, a stop from the hub
and a pause/resume have not been checked; `sdl` is the stock-TrimUI default on
the PowerVR models, chosen from other ports' reports (NextOS ports issue #1).
**Where.** `player/androidrt/src/eglpresent.c`, `docs/ANDROID.md` (device trial
steps and log lines).
**Done when.** On a Smart Pro and a Brick, with the runtime the TrimUI archive
carries: the aarch64 fake games run, then Penguin Dash; the log shows `stock
firmware (<model>): auto chose the SDL-hosted backend`, `SDL2 … video driver
mali` and `backend sdl (…)`, and a frame appears on the panel. If the Brick
stays black, the same run with `ANDROIDPAK_PRESENT=native` (and
`ANDROIDPAK_EGL_WINDOW=null` if EGL refuses the window struct) is logged and the
default revisited; set `ANDROIDPAK_GLES2_LIB` if `SDL_GL_LoadLibrary` fails.
With sdl, the hub can still stop the game (SIGTERM), and a game that pauses and
resumes keeps presenting.

### AND-3 — Minecraft Bedrock first light (P2, L) [HW] [OWNER]
**Why.** The class pack is built from public knowledge only; it needs an owned
arm64 APK.
**Done when.** Every `[androidrt][jni] report:` entry and `undefined symbol` from
real runs becomes an implementation, run over run; the reached state (title
screen / license screen) is documented in `docs/ANDROID-MINECRAFT.md`. Store and
license answers stay honest.

### AND-4 — Text input for Android games (P2, M)
**Done when.** `showKeyboard` opens JukaHub's on-screen keyboard over the game
(IPC runner ↔ hub) and the text reaches the game through its natives.

### AND-5 — FMOD audio path on the device (P2, S) [HW]
**Done when.** FMOD falls back to OpenSL ES (`supportsAAudio=false`) and
`openslessim.c` latency is acceptable; an AAudio shim only if FMOD insists.

### AND-6 — Memory of big APKs (P2, S) [HW]
**Why.** A game whose assets unpack to more than 64 MiB keeps them in the APK on
the card (`androidPakStageAssetsMax`): only its libraries, manifest and
resources are staged into tmpfs and the runtime reads assets from the APK, so
the pre-flight counts far less. What such a run really costs in RSS and page
cache is unknown.
**Where.** `androidpak.go` (`androidPakRuntimeOverhead`, preflight floor,
`androidPakStageAssetsMax`).
**Done when.** On a Smart Pro a game with more than 64 MiB of assets logs `… MiB
of assets stay in the APK on the card` and `[androidrt] assets: APK indexed …`,
its asset reads work, its RSS and page-cache pressure are measured, and the
preflight refuses runs that cannot fit in 1 GB, with a clear message.

### AND-7 — Class packs by demand (P3, M)
**Done when.** SDL2's Java glue (`org/libsdl/app/SDLActivity`) and Unity's
`UnityPlayer` packs exist, built from real compatibility reports.

### AND-10 — Terraria through FNA with the user's PC files (P3, M) [NET]
**Why.** The Android build is Xamarin/Mono; AndroidRT refuses it at triage.
**Done when.** A package recipe (PortMaster-style) runs the PC version with FNA +
Mono on aarch64 from files the user copies; docs explain where they go.

### AND-13 — Auto's window fallback adopts a context-first game's context (P3, M)
**Why.** When `auto` hands over to SDL at the window surface, a game that
created its GLES context first (GLSurfaceView order) keeps that raw EGL context,
so its frames bypass `SDL_GL_SwapWindow` and stay black; today the log names
`ANDROIDPAK_PRESENT=sdl`. The PowerVR TrimUI models start on `sdl` and are not
affected; this matters where `auto` still starts native: the Smart Pro S
(tg5050, Mali) and firmware other than stock. An `auto` run there that logs "the
game made its GLES context before its window" decides it.
**Where.** `androidrt/src/eglpresent.c` (`pr_eglMakeCurrent`,
`pr_eglCreateContext`, `pr_eglDestroyContext`), `tests/smoke`
(`EGLTEST_CONTEXT_FIRST`).
**Done when.** A raw context that has never been current (so holds no GL
objects) is mapped to SDL's context at its first `eglMakeCurrent` with SDL's
surface (destroy and share handled), one that was current with a pbuffer stays
raw with today's log, and the window-fallback context-first smoke case asserts 5
`SDL_GL_SwapWindow` frames.

### AND-14 — The AndroidRT package: strict loader check and reproducible zip (P2, S) [NET]
**Why.** The CI's AndroidRT step accepts any clean exit of `loader --version`, and the zip changes on every build
(BUILDINFO's `built:` time, a version taken from the repository's head), so a
downloaded copy is offered a redundant update.
**Where.** `player/native/build-runtimes.sh` (`build_androidrt`, `make_zip`),
`player/androidrt/build-tsp.sh`, `player/androidrt_install.go`.
**Done when.** `build-runtimes.sh` refuses a `loader --version` answer other
than `AndroidRT loader <version> (aarch64, bionic loader)`, as the device's
probe does; the version is stamped from the last commit that touched
`player/androidrt` or the runtime build scripts, BUILDINFO's `built:` line is
that commit's date, and the tree is `touch -d`'d to it before `zip -X`, so a
rebuild from unchanged sources gives a byte-identical zip.
**Verify.** Two runs on unchanged sources print the same SHA-256 for
`JukaHub-AndroidRT-linux-arm64.zip`.

### AND-17 — First APK Store run on a Smart Pro (P1, M) [HW]
**Done when.** On a fresh card with the new TrimUI package the scan lists the
APKs in the store's folders and `PhoneDrop`; a Minecraft arm64 APK gets the
class-pack verdict and a Java app the "needs Android's Java runtime" one; labels
and icons read right; the Runtime row says "Ready · Bundled with JukaHub" and
Play reaches AndroidRT's launch log with that runtime (no build step). Add puts
the game in Game Profiles and on the Games Library's Android row with its icon,
and Play works from there; a second quick Play on a big game says "… is already
starting" (one game process); two phone-pulled `base.apk` games keep their own
covers and Remove takes only its own. With `Apps/JukaHub/androidrt` renamed
away the row says Missing and Install brings it from the release ("Installed
from JukaHub's releases (<tag>)", log `relpack androidrt … installed`); an
empty `Apps/AndroidRT` is listed under "Passed over"; leaving the store during
"Install and play" finishes the install without starting the game. The scan's
time and memory on a full card are noted in `docs/ANDROID.md`.

### AND-18 — Split APKs and app bundles (P3, M)
**Why.** Apps copied off a phone often come as split APKs (`base.apk` plus
`split_config.arm64_v8a.apk`) or as `.xapk`/`.apks` bundles. The native
libraries then sit in a split, and AndroidPak stages one APK, so such an app
cannot be added as one game yet.
**Where.** `apkstore_scan.go`, `apkstore_verdict.go`, `androidpak.go` (staging).
**Done when.** The store groups a base APK with its splits (same package and
version, side by side or inside a bundle), judges them as one app, and
AndroidPak stages the native libraries from the matching split.

### AND-20 — Minecraft screen, first device run (P1, M) [HW] [NET]
**Why.** Start → Games → Minecraft (`minecraft.go`, `minecraft_ui.go`,
`minecraft_peer.go`, `docs/MINECRAFT.md`) has not run on a device. Three guesses
need a real card: PortMaster's `ports.json` layout (the reader accepts the dict
and list forms), the Java port's folder (`.minecraft` inside the port folder or
`versions/` directly in it, found through the script's `GAMEDIR`), and that a
zip in `Apps/PortMaster/PortMaster/autoinstall` is installed with its runtimes
when JukaHub opens PortMaster on stock TrimUI.
**Done when.** On a Smart Pro with PortMaster: Install on ClassiCube, Luanti and
Java Edition each logs the catalogue entry, downloads and passes the MD5, and
PortMaster installs it; back in JukaHub the row says Play within a second and
Play starts the port. A paired Windows PC with 1.12.2 in `.minecraft` shows
"Copy 1.12.2 from <PC>", the copy lands where the port's launcher looks (adjust
`mcJavaGameDir` if not) and the port starts the version. The Bedrock row finds
an owned arm64 APK and opens the APK Store on it (the game itself is AND-3).
Results and timings go into `docs/MINECRAFT.md`.

### AND-21 — Minecraft copy: pick only what the port lacks (P3, S)
**Why.** The PC copy sends every library a version lists for Linux; the port
ships its own LWJGL for aarch64, so some of those jars are dead weight on the
card.
**Done when.** After AND-20 shows which libraries the port's launcher really
loads, the plan skips the ones the port provides (by the port's own file list),
and the copy row shows the size before A.

---

## Media (MED)

### MED-2 — Jellyfin/Plex client (P2, L) [NET]
**Done when.** A server can be added (address + login through the secret store),
libraries browse as shelves in the media hub, and items play through the existing
decoder with resume.

### MED-3 — Reactive visualiser from real audio (P3, S)
**Where.** `musicplayer.go` (synthetic bars today), `ledsync.go` (`audioBus`).
**Done when.** The bars come from an FFT of the decoded PCM (bounded cost per
frame).

### MED-4 — Scrobbling (P3, S) [NET]
**Done when.** ListenBrainz (token via secret store) receives plays from the
music player, queued offline.

### MED-5 — Screenshot gallery by game (P3, S)
**Where.** `screenshot.go`, `imageviewer.go`, `recordings.go`.
**Done when.** Screenshots open as a gallery with one section per game, newest
first: JukaHub's own captures and RetroArch's are grouped by the game named in
the file name, anything else under "Other"; A opens a picture full screen and
L1/R1 step between games.

### MED-6 — Subtitle style and language preferences (P2, M)
**Why.** Since 2026-11-20 the video settings sheet picks audio and subtitle
tracks and remembers them per file (`videotracks.go`), but nothing prefers a
language across files and subtitles have one fixed look.
**Done when.** A preferred audio and subtitle language picks the matching
tracks in any new video, and subtitles can be styled (size, background).

### MED-7 — Crossfade and ReplayGain (P3, S)
**Why.** Album playback is already gapless (`musicStartDecodeGapless`,
`musicplayer.go`); there is no crossfade and no loudness levelling.
**Where.** `musicplayer.go`, `musicsettings.go`.
**Done when.** An optional crossfade (0–10 s) overlaps a track's end with the
next one, and ReplayGain track/album tags (read by ffprobe) set the level when
switched on; both stay off on the bit-perfect path.

### MED-8 — Podcasts offline (P2, M)
**Done when.** Episodes download for offline listening (Downloads queue), OPML
import/export works, chapters are shown, played episodes clean up.

### MED-10 — Radio buffering and now-playing titles (P3, M) [NET]
**Done when.** The Radio Browser directory (Music → Radio, 2026-11-20,
`radiodir.go`) and saved stations show a buffering state while a stream
connects and the station's now-playing title (ICY metadata) while it plays.

### MED-11 — Comics (P3, M)
**Done when.** CBZ/CBR/CB7 open in a reader with fit width/height, two-page
spread and right-to-left (manga) mode, remembering the page.

### MED-13 — TV guide (P3, M) [NET]
**Done when.** XMLTV EPG for M3U playlists that name one (`url-tvg`), shown as
now/next in the channel list.

### MED-14 — Media servers on the LAN (P3, M)
**Done when.** DLNA/UPnP servers are discovered (SSDP) and browsable as a Media
source; a Kodi remote (parking lot) uses JSON-RPC.

### MED-15 — Search, tracks and folders in the music library (P3, S)
**Why.** Music browses the media index (`mlib.go`, `music_shelf.go`) by album,
artist and audiobook with incremental rescans, but has no search, no list of all
tracks and no folder view.
**Where.** `music_shelf.go`, `mlib.go`, `musicplayer.go`.
**Done when.** The music library has Tracks and Folders tabs with natural sort,
and a search (on-screen keyboard) over title, artist and album.
**Verify.** With 2,000 tracks on a Smart Pro, search answers without a visible
stall.

### MED-18 — Trim, rename and delete a recording (P2, M)
**Why.** The Recordings screen only lists and plays captures;
`docs/RECORDINGS.md` says trimming, renaming and deleting are a PC job, which is
exactly the moment the user is away from one.
**Where.** `recordings.go` (`scanRecordings`, `loadRecordings`), the scene that
draws them (`core.go` callers), the video player for the trim preview.
**Done when.** A recording can be renamed and deleted after a two-press
confirmation (moved to the APP-4 trash once it lands), and trimmed to in/out
points with ffmpeg stream copy where the codec allows it (re-encode otherwise),
keeping the original until the trimmed file plays; the list shows duration and
size.
**Verify.** Trim a 2-minute clip on Windows and on a Smart Pro; the result
plays and the original is intact until the trim is confirmed.

### MED-19 — A JavaScript runtime for yt-dlp on Windows (P1, M) [WIN] [NET]
**Why.** Since late 2025 yt-dlp needs an external JavaScript runtime (Deno by
default) for full YouTube support. Without one it is down to the android
client's 360p single-file format 18, which YouTube may stop serving at any time
— late-October 2026 reports already disagree on whether it plays without a
token. MED-33 covers the smaller QuickJS runtime on the handheld.
**Where.** `ytdlp_update.go` (the download, SHA-256 check and swap to copy),
`patch_engine.go` (`helperToolEnv` already puts the tools folder first on
PATH), `streaming.go` (`streamAttempts`).
**Done when.** JukaHub installs pinned, checksum-verified Deno (≥ 2.3) next to
yt-dlp on Windows; yt-dlp is pointed at it (`--js-runtimes`), stream attempts
prefer yt-dlp's default clients while it is present, and the Patch screen shows
its version. Playback above 360p also needs MED-20.
**Verify.** On Windows a video resolves through the default clients with Deno,
and still plays through format 18 without it.

### MED-20 — Stream the separate picture and sound, and HLS (P1, M) [HW]
**Why.** YouTube no longer serves this device a single-file stream (format 18 is
refused without a PO token), so every YouTube play pays for a failed stream
before the download fallback (`ytdownload.go`) joins the separate picture and
sound locally. The player already runs one ffmpeg for the picture and one for
the sound.
**Where.** `videoplayer.go` (`startEmbeddedPlayback`, `ffmpegDecoderArgs`,
`startFFmpegAudio`), `streaming.go` (`resolveStreamSource`,
`probePlayableStream`), `youtubenative.go` (adaptive formats, `hlsManifestUrl`),
`ffmpegtls.go` (the https relay already rewrites HLS playlists).
**Done when.** A YouTube video streams from an avc1 video address and an m4a
audio address in sync, and seeks; an HLS master plays as one input (yt-dlp's
visionos client reportedly returns one with muxed H.264 variants, itags 91–96);
the resolver tries these before the download fallback.
**Verify.** First by hand on a device: `yt-dlp -g -f 91/92/93 --extractor-args
youtube:player_client=visionos <id>` and play the URL with ffplay. Then a
10-minute 720p video plays and seeks on a Smart Pro and on Windows.

### MED-22 — Send the stream's own user agent to the player (P3, S) [HW]
**Why.** The download path already retries a googlevideo URL with the producing
client's own user agent (`ytdlAgentsFor`, `ytdownload.go`), but the probe, the
decoders and the relay always send `streamUserAgent`. Needed only once a device
logs the CDN check's "…HTTP 403 to the player's identity but HTTP 2xx to the
ANDROID app's own user agent…".
**Where.** `youtubenative.go` (`ytNativeClientUserAgent`), `videoplayer.go`
(`ffprobeStream`, `ffmpegDecoderArgs`, `startFFmpegAudio`), `videorecover.go`
(`ffmpegStreamInputArgs`), `ffmpegtls.go` (the relay's default headers).
**Done when.** For googlevideo URLs whose `c=` names a client the native table
knows, the probe, the decoders and the relay send that client's user agent;
other hosts are unchanged.

### MED-24 — A YouTube client table that updates without a release (P2, M) [NET]
**Why.** The native path's client names and versions are constants
(`youtubenative.go` `ytNativeClients`; the web search client takes the live
version from the homepage but falls back to a constant), and YouTube retires
versions every few months: the ANDROID_VR refusal of 2026-08-17 broke every
install until an app change.
**Where.** `youtubenative.go`, `youtubesearch.go`, the update channel.
**Done when.** A small JSON (client name, version, user agent, client ID,
order), fetched at most daily and checked (signature or a pinned hash list), is
cached and used ahead of the built-in table; a bad or unverifiable file is
ignored with a log line.

### MED-25 — YouTube on a stock Smart Pro (P1, S) [HW]
**Why.** How Media resolves YouTube (native client with a CDN check, yt-dlp
auto-update with rollback, failure messages that name the cause) and the stream
fallback (`videorecover.go`: a decoder that never delivered a frame becomes a
download or a message) have not run on a device.
**Where.** `youtubenative.go`, `streaming.go` (`tryStreamPlayback`),
`ytdlp_update.go`, `ytfailure.go`, `videorecover.go`, `videoplayer.go`
(`startFFmpegDecoder`); log tags `youtube`, `stream` and `tools`; errors.txt
lines starting `decoder for`.
**Done when.** With the tools folder's yt-dlp deleted, Media installs it and a
search result plays; an old yt-dlp (or one with its mtime set three weeks back)
is updated on the next Media visit and Patch shows the new version; a failing
search names its cause and says no API key is needed; a browser link without
text gets its title on the Continue card; a stream that fails shows "Streaming
didn't work — downloading the video instead" and then plays, its errors.txt line
says why (record it here: "Protocol not found", "403 Forbidden" or other), and
after two such failures the next video goes straight to the download; a live
YouTube stream still streams; a video paused for ten minutes carries on when
resumed (`-reconnect`); closing the player during the loading picture shows no
message; on Windows an update swaps a busy `yt-dlp.exe`.

### MED-30 — Subtitles and saves on a device (P1, S) [HW] [NET]
**Why.** CC uses YouTube's own caption list with yt-dlp as the fallback, and
Save offline / Extract audio exist; none of it has run on a device. Whether the
ANDROID client's caption URLs answer without a proof-of-origin token decides
whether the fast path ever works.
**Where.** `ytcaptions.go`, `videosubs.go`, `videosheet.go`, `mediaextract.go`,
`downloads.go` (the idle-queue subtitle step).
**Done when.** On a stock Smart Pro: CC on a YouTube video shows "Getting
English subtitles…" and then cues within a few seconds; the log says whether
"native captions" answered or stood down (record which here); auto-generated
captions show each line once; CC stays on for the next video; Y → Save offline →
Download video lands in Media → Downloads with `<name>.en.srt` beside it, and
that video plays with CC in flight mode; Extract audio on it gives a track in
the music player within seconds; a second Download press says "Already
downloading".

### MED-33 — A JavaScript runtime for yt-dlp on the handheld (P1, M) [HW] [NET]
**Why.** Without one, yt-dlp 2025.11 and later uses only the visionos client
("some formats may be missing"), and some connections get 403 even there.
JukaHub already names QuickJS-NG to yt-dlp when a `qjs` sits next to it
(`ytDlpJSRuntimeArgs`, `ytdownload.go`) but installs none. QuickJS-NG is one
small binary (Deno is about 100 MB unpacked); Windows gets Deno (MED-19).
**Where.** `ytdownload.go`, `streaming.go` (`streamAttempts`), the tools
installer (`ytdlp_update.go`, `patch_engine.go`, `tools.go`).
**Done when.** The installer fetches a pinned, SHA-256-checked quickjs-ng `qjs`
for linux-arm64 next to yt-dlp and Patch shows its version; while it
is present the stream attempts prefer yt-dlp's default clients; `yt-dlp -v`
shows `[jsc:quickjs]` and the tv client in the default set; a video's extraction
time on the A133P is recorded (over about 30 s: use the runtime for downloads
only).
**Verify.** On a Smart Pro a video resolves through the default clients with the
runtime, and still plays through the fallback without it.

### MED-38 — WebP, second round: chat pictures and covers without a refresh (P3, S)
**Why.** Where SDL_image has no WebP decoder, .webp files now open through
ffmpeg (`webpdecode.go`), but a chat picture bubble (`chatLocalAttachment`,
`image.Decode` in `chat.go`) still cannot draw a local .webp, and a cover queued
for conversion during a library scan draws the placeholder until the next
library build.
**Where.** `webpdecode.go` (`webpDecodeImage`, `webpCoverWorker`), `chat.go`,
`game_library_ui.go` (`decodeLibraryArt`).
**Done when.** A local .webp in Chat draws as a picture on the stock firmware;
a converted cover shows without a library refresh (the open library rebuilds
its art paths once when the worker drains, or `decodeLibraryArt` uses the
viewer's ffmpeg fallback).

### MED-39 — Audiobook chapters, second round (P3, S)
**Why.** Chapters inside one file show only in the Auralis player layout
(`renderMusicChapterRows`, `audiobook_chapters.go`); the chapter rows cannot be
picked (only Next and Previous move between them), and the Audiobooks shelf
still says "1 chapter(s)" for a one-file book.
**Where.** `musicskins.go` (`renderMusicStockOS`), `musicplayer.go` (row hits,
`ctrlMusicPlayer`), `music_shelf.go`, `audiobook_chapters.go`.
**Done when.** Both layouts list the chapters; Up/Down and A, a click or a tap
start the chosen one; the shelf counts a probed file's chapters.

---

## System and connectivity (SYS)

### SYS-1 — Bluetooth pads and pairings on a device (P1, M) [HW]
**Why.** The Bluetooth page, the top-bar button and the bring-up
(`bluetooth_devices*.go`, `statusbar_bluetooth.go`, `bluetooth_quick.go`,
`bluetooth_bringup*.go`, `bluetooth_attach.go`) were written without a device.
TrimUI's firmware promises only "BLE hid", so classic pads (DualShock 4,
DualSense) may connect without input, and stock pairings have been seen lost
after a reboot or a radio toggle.
**Where.** The files above; `bluetooth_diag.go` logs the whole stack when the
page opens and on every failed bring-up (also in the diagnostics bundle).
**Done when.** On a Smart Pro and a Brick, stock 1.1.x and CrossMix, recorded in
`docs/REMOTE_PLAY.md` and the HW log (reconnects, headset audio and the other
two models are HW-2 step 3, HW-3 step 14 and HW-7 steps 2–3):
- the diagnostics block: which tools exist, whether `hci0` is there, which
  processes run, which bring-up rungs ran (`bring-up:` lines), and whether the
  pairing store holds more than one adapter address;
- `bluetoothctl` output arrives line by line in the session; `agent
  NoInputNoOutput` and `default-agent` answer;
- a search lists a DualSense, a DualShock 4, an Xbox pad and an 8BitDo pad in
  pairing mode within 10 s; which get an input device (Bus=0005 with the pad's
  address) and which show **No input**;
- a pairing leaves a LinkKey under `/etc/lib/bluetooth/<adapter>/<device>/info`
  and survives a reboot and a radio toggle;
- with the stock switch off: is `hci0` gone or only DOWN, does the bar's rune
  show struck through, is `bluetoothd` running; whether `trimui_btmanager` or
  keymon switch the radio back off within about 30 s of JukaHub switching it on;
  what `system.json`'s Bluetooth key does;
- no new Wi-Fi stutter from the bar's two-second reading, and Remote Play
  stutter with a pad connected against Wi-Fi alone.

### SYS-2 — Wi-Fi verified on both stock images (P1, S) [HW]
**Why.** The Smart Pro's `wlan0` and `wpa_supplicant` are confirmed
(`docs/HARDWARE_VALIDATION.md`); the Brick's image and a live join/forget are
not.
**Where.** `wifi*.go`, `docs/WIFI.md`.
**Done when.** The Brick's interface name, `wpa_cli`/socket path and DHCP client
are confirmed; on both, join, forget and the diagnostics sheet work.

### SYS-3 — Netplay end to end (P2, S) [HW]
**Done when.** Through the Netplay panel: a LAN room hosted on one device is
listed and joined on another; **Host online** shows up in lobby.libretro.com and
a second device joins it from Online rooms through the relay (confirms
ra64.trimui accepts `--appendconfig` and the `host|port|session` --connect form);
check whether RetroArch writes the overlay's announce/relay keys back into
retroarch.cfg on exit.

### SYS-4 — Focus mode with a PIN (P3, M)
**Done when.** A persisted Simple mode shows only Home, Media, Games and
Settings → Simple; leaving needs a PIN (hashed in the secret store); the setting
says plainly it is not parental security.

### SYS-8 — Storage you can manage (P2, M)
**Why.** Disk usage is totals per volume; there is no "free up space".
**Done when.** Settings → Storage shows usage by kind (games, media, downloads,
saves, caches, backups, old versions, trash) with one-press clean-ups and a
warning at 90% full; the card's filesystem dirty flag is checked at boot
(`sdhealth.go`).

### SYS-9 — Move to a new device (P3, M)
**Why.** Switch 2 ships device-to-device transfer. Device Sync (2026-11-20,
`devsync*.go`) already carries saves, states, BIOS, collections, recents, play
time, ROMs and Send my settings between paired devices, but settings that name
a file (wallpaper, theme files) apply only when the file is already there, and
installed apps, emulators and cores do not follow.
**Done when.** Send my settings also copies the wallpaper and custom theme
files it names, and the installed package list (apps, emulators, cores) can be
reinstalled on the other device in one flow, with a summary of what moved.

### SYS-10 — Import from the stock launcher and other firmwares (P3, M)
**Done when.** First run offers to import TrimUI MainUI favourites, recents and
playtime, and muOS collections where their files are on the card (NextUI's
`Collections/*.txt` are shared both ways since 2026-11-20,
`collections_nextui.go`).

### SYS-14 — Chat files against the real channel (P1, S) [NET] [HW]
**Why.** Chat uploads use a hand-written streaming multipart POST
(`chatupload.go`: `payload_json` + `files[n]`, the session's
`Authorization`/`User-Agent`, `nonce` + `enforce_nonce`) that has not met
Discord. Its limits (20 MiB a file, 50 / 100 MiB on boost tier 2 / 3) may be out
of date, and text messages still go through discordgo without a nonce, so a text
post whose answer is lost can appear twice.
**Where.** `chatupload.go` (`chatDiscordUpload`, `chatUploadAnswer`,
`chatEchoCandidate`), `chatfiles.go` (downloads, `chatRefreshAttachmentURL`),
`chatsync.go`; docs/CONTROLS.md → "Sending files".
**Done when.** From a Smart Pro and from Windows: a picture, a 15 MB zip and a
log reach the channel with progress and show on the other device and in Discord;
a file over the server's limit is refused with "too big"; pulling Wi-Fi
mid-upload resumes without a second post; a picture older than a day renews its
link; Juka AI answers about an attached log. If a lost text answer still
duplicates, text posts move to the same raw POST with a nonce.

### SYS-15 — Archives on the card and on Windows (P1, S) [HW] [WIN]
**Why.** Extracting archives from Files has only been checked statically: the
in-process zip and tar readers, `os.Root` writes onto an exFAT card, the
free-space check, the staging folder's rename into place and the hand-off to
7-Zip (`7zz` from JukaCrossMix's `System/bin` on the handheld; an installed
7-Zip, the fetched `7zr.exe` or `tar.exe` on Windows).
**Where.** `files_extract.go` (`feExtractRun`, `feExtractZip`,
`feExtractTarStream`, `feExtractFinish`), `files_extract_tools.go`
(`fe7ZipPreflight`, `feRun7Zip`), `files_extract_ui.go` (the sheet,
`feExtractDrain`); docs/CONTROLS.md → "Archives".
**Done when.** On a Smart Pro: a 700 MB PS1 zip (cue and bins) extracts into its
own folder with the bar moving, and the library finds the game; a one-ROM zip
lands beside itself; **Extract here** over a folder that has the files asks
twice and replaces them; a .7z and a .rar unpack through `7zz`; a cancel
mid-way, and pulled power mid-way, leave no `.jukahub-extract-*` behind (the
second is swept by the next extraction there); a zip bigger than the free space
is refused before anything is written. On Windows: the same zip and a tar.gz,
and a .7z with and without 7-Zip installed (`7zr.exe` fetched once). On the
exFAT card and on a FAT32 card a debug log of `feVolumeOf` shows the real
cluster size and FAT flag, and a member of 4 GB or more bound for FAT32 is
refused before anything is written; on Windows a RAR unpacked with `tar.exe`
(no 7-Zip) lists and extracts, and one holding a symbolic link is refused.
Passwords (`7zz` on the handheld, 7-Zip on Windows): an AES zip, a 7z with
encrypted names (`-mhe`) and an encrypted RAR unpack after **Password…**; a
wrong password says "the password is wrong" and leaves the folder as it was.
On Windows a password with spaces, a trailing backslash and `a\b` unpacks, and
one containing `"` is refused with its message.

### SYS-16 — Screen brightness on a device (P1, S) [HW]
**Why.** Every brightness control drives the TrimUI backlight through the
display driver's ioctl on `/dev/disp` (DISP_LCD_SET/GET_BRIGHTNESS, raw 1..255)
in the firmware's 10 % steps, and writes `/mnt/UDISK/system.json` "brightness"
(0..10) so stock Settings continues from it. The read ioctl, the key and its
scale were checked on a stock Smart Pro; the full firmware curve and write
behavior still need a live JukaHub build.
**Where.** `brightnessctl.go` (`brightnessFindBackend` logs the backend once,
tag `brightness`; `trimuiLogBrightnessEvidence`; `brightnessFirmwareCurve`),
`brightness_disp_linux.go`, `brightness_trimui.go`
(`trimuiSystemBrightnessStep`, `trimuiBrightnessFlush`,
`trimuiSystemJSONRewrite`), `qrzoom.go`.
**Done when.** On a Smart Pro and a Brick the log names the display driver; the
ten steps run from dim-but-readable to full and feel even; the firmware's
stock Settings continues from a level set in JukaHub and the open card follows
changes made there; after sleep and wake, and after quitting
to the stock UI, the level JukaHub set stays (a jump when keymon re-applies the
file means its curve differs: match it); the pairing QR goes to full and gives
the level back; a `system.json` whose "brightness" is not 0..10 is left alone
(the log says so); booted straight into JukaHub with no keymon, JukaHub applies
the firmware's saved level itself. If "brightness" turns out not to be the
backlight or not 0..10, stop writing it and keep the display-driver path. keymon
still reacts after JukaHub wrote the file (same inode: `ls -i
/mnt/UDISK/system.json` before and after).

**Smart Pro evidence, 2026-11-17.** A read-only ARM64 probe using the same
`DISP_LCD_GET_BRIGHTNESS` ioctl returned raw 112, 133, 155 and 220 at firmware
steps 5, 6, 7 and 10. The user moved the stock Settings slider between reads;
there is no stock brightness shortcut on this Smart Pro. JukaHub's fallback
mapping now caps the unlearned steps at the firmware's measured maximum of
220. A reversible `DISP_LCD_SET_BRIGHTNESS` probe changed raw 112 to 113,
read 113 back and restored 112. The rest of the curve, JukaHub's UI path,
sleep restoration and keymon sync still need a live ARM build.

### SYS-17 — Boot logo installer on a device (P1, S) [HW]
**Why.** The firmware boot logo installer assumes Allwinner's layouts —
`bootlogo.bmp` on a FAT `boot-resource`/`bootloader` partition, or a raw
`bootlogo` partition — found by name through `/dev/by-name`, the kernel's
`partitions=` and sysfs. A stock Smart Pro 1.1.0 was found to have a 24 MiB
FAT `bootloader` partition with a 396×66, 32-bit `bootlogo.bmp`; the installer
now accepts that exact banner and renders a horizontal mark in it.
**Where.** `bootlogo.go`, `bootlogo_linux.go` (log tag `bootlogo`: the store,
its size and depth, every refusal), `bootlogo_layout.go`; docs/BOOT.md.
**Done when.** On a Smart Pro and a Brick (stock 1.0.4 and 1.1.x, and CrossMix):
the Boot logo row names a store or an honest reason; install shows the JukaHub
logo at the next power-on with nothing cropped or rotated; Restore brings the
TrimUI logo back; the backup sits in `Apps/JukaHub/data/bootlogo/`. The power-on
boot animation itself is HW-1 step 3.

**Smart Pro evidence, 2026-11-17.** `/dev/by-name/bootloader` resolves to
`/dev/mmcblk0p1`, a 24 MiB FAT partition. A read-only mount showed
`bootlogo.bmp` (104600 bytes, 396×66, 32-bit uncompressed); a copy was decoded
and its header checked. The old screen-height guard refused it. The banner
exception and an in-place BMP rewrite test now cover that format. Actual
installation, restore and power-on appearance still need a new ARM build.

### SYS-18 — Screen Share on the Smart Pro and the Brick (P1, M) [HW]
**Why.** Screen Share is JukaHub's answer to the Smart Pro having no HDMI port
and has not run on a device: whether `/dev/fb0` carries a game's GLES frames on
the stock and CrossMix images (the time-lapse recorder rests on the same
assumption), which way a portrait framebuffer must be turned, the render-thread
readback cost on the PowerVR and the pure-Go encoder's speed on the A53 are
estimates.
**Where.** `castcap.go` (UI capture, encoder, supervisor), `castcap_fb.go`
(framebuffer source, ffmpeg or JukaHub's reader; log tag `cast`),
`companion_cast.go` (viewer page, `/status`), `castui.go`; `timelapse.go`.
**Done when.** On a Smart Pro and a Brick, stock and CrossMix: Smooth to a
laptop browser keeps the UI at its normal frame rate in `framestats` while
watched; a RetroArch game started with a viewer connected streams live through
ffmpeg — not black, not sideways (else record the right `castRotate` per device
and make it the default) — and again with ffmpeg removed through JukaHub's
reader, with CPU and fps noted; a game whose picture stays black brings up the
`/status` note (then `/dev/fb0` does not carry GLES frames on that image and the
time-lapse is broken the same way); the Brick's 1024×768 panel under its 960×720
canvas keeps its aspect; a Samsung (Tizen) or LG (webOS) TV browser shows the
stream or its single-frame fallback, and `&tv=1` fills the screen; **Y** cuts
the old link off within a second; the LIVE pill shows on the handheld and in
neither the stream nor a screenshot.
**Verify.** `framestats` with and without a viewer, `top` during a shared game,
`logs.txt`, a photo of the TV.

### SYS-19 — Encrypted save backups to your own WebDAV server (P3, M) [NET]
**Why.** Saves sync to Google Drive (`gdrive_sync.go`) or through Syncthing, or
leave as an encrypted `.jukasaves` export, but there is no self-hosted target
such as Nextcloud.
**Where.** `ssync.go`, `ssyncui.go`, the secret store, and
`docs/SAVE_MANAGER.md`.
**Done when.** Save Sync can add a user-selected HTTPS WebDAV target (including
Nextcloud), upload versioned client-encrypted save snapshots, and list and
restore them through the existing import preview; the WebDAV app password stays
in the secret store, TLS certificates are validated, interrupted uploads leave
the previous snapshot usable, and restoring always takes a Save Manager snapshot
first. No save or passphrase is uploaded in plaintext.

### SYS-20 — Resolve save-sync conflicts from the comparison card (P2, M)
**Why.** `ssyncDecide` safely keeps both versions when both devices changed a save,
but it selects one as the filename the emulator will load; the user has to
inspect and rename files manually to choose the active version.
**Where.** `ssync.go` (`ssyncDecide`, `ssyncConflictName`), `ssyncui.go`, and
`docs/SAVE_MANAGER.md`.
**Done when.** A comparison with conflicts can pause for a per-file choice to
use this device's copy, the other device's copy, or leave the conflict
unresolved; applying a choice snapshots both devices first, sets the selected
version as the active filename on both, keeps the other version under its
conflict name, and never advances the sync base for an unresolved file.

### SYS-21 — Juka and pictures, second round (P3, M)
**Why.** Pictures and videos reach Juka only from Chat's Juka AI tab
(`vision_chat.go`); **Ask Juka** on a Community message sends only its text, and
a video's sound goes to Whisper as an uncompressed 16 kHz WAV (up to about 19
MB).
**Where.** `vision_chat.go`, `vision_video.go`, `chatactions.go`,
`imageviewer.go`.
**Done when.** A Community message's actions offer **Ask Juka** for its picture
(the Discord file is downloaded first); the image viewer sends the picture on
screen to Juka; the transcript upload is compressed (FLAC or Opus) when the
firmware's ffmpeg has the encoder, so a ten-minute clip goes faster on slow
Wi-Fi.

### SYS-22 — JukaHub OS on a device (P1, M) [HW]
**Why.** JukaHub OS — a private mount namespace with read-only bind-mounted
layers, a re-exec into it, kernel modules loaded only on an exact vermagic match
and whitelisted sysfs values, journaled in `/tmp/jukaos` and undone on quit —
has not run on a device or been checked against the TrimUI kernel config.
**Where.** `jukaos.go` (layer.json checks, journal), `jukaos_linux.go`
(`jukaosSessionEntry`, namespace, binds, modules), `jukaos_settings.go`
(Settings → JukaHub OS, escape hatch, OS guard), `launch.sh` (the os.json grep),
`childenv.go`; log tag `jukaos`; docs/JUKAHUB_OS.md.
**Done when.** On a Smart Pro and a Brick (stock 1.1.x and CrossMix):
`unshare(CLONE_NEWNS)` works as root (log "OS session: N layer(s), M bind(s)";
`/proc/<pid>/ns/mnt` differs from MainUI's parent); "/" accepts MS_REC|MS_SLAVE,
a bind is invisible from an SSH shell outside, a USB stick plugged in during a
session shows inside; a file bind from the FAT/exFAT card over `/usr/lib/x.so`
works, is executable (no `noexec`) and remounts read-only; the exec keeps the
pid (launch.sh's wait and `.last-exit`); after a failed exec the setns back
works on 4.9; `finit_module` exists (else the init_module fallback), the
vermagic of a stock `.ko` matches the parser, and module signing is not forced
(else the log says so); `drop_caches` is writable; a CrossMix RetroArch started
with the built-in "JukaHub libraries for games" layer and a newer libSDL2 in
`libs/` maps it (`/proc/<ra>/maps`); Select/B held through the power-on boot
gives the stock menu (physical B vs SDL B after the swaps); Return to <firmware>
brings MainUI back and a reboot is stock; a card made from
`JukaHub-OS-Trimui.zip` (`trimui/make-os-package.sh`) boots into JukaHub OS on
its first power-on without the app ever being opened. The per-model driver
layers are HW-7.

### SYS-23 — JukaHub OS, second round (P2, M)
**Why.** RetroArch, the firmware's emulators and netplay still guess among
several cores/system/saves roots, a game a layer fixes still reports a missing
library, layers install only by hand, and driver or value changes need **Restart
JukaHub OS**.
**Where.** `jukaos*.go`, `emulators.go` (`deviceRetroArchCoresDir`),
`gamelaunch_firmware.go` (`firmwareROMArg`), `elfcompat.go`, `ports_catalog.go`,
`files_extract_ui.go`, `home_power.go`.
**Done when.** A generated per-firmware paths layer dir-binds one stable tree
(RetroArch cores/system/saves, `Emus/*/`) so RetroArch, the firmware's emulators
and netplay stop guessing among five roots and MainUI's `../../Roms` form;
`elfcompat.go`/`ports_catalog.go` also search the session's
`JUKAHUB_OS_CHILD_LIBS`; the Files archive sheet offers **Install as JukaHub OS
layer** for a zip with `layer.json`; driver and value changes apply live where
possible; a per-session report page lists what was bound, loaded and skipped;
the power card can restart JukaHub OS.

### SYS-24 — JukaHub OS image on plastron's Linux (P3, L) [HW]
**Why.** A plastron card boots the Brick to its menu in about 1.9 s because the
card is the whole system; plastron (github.com/tarikbc/plastron, now ZyperOS) builds
U-Boot + TF-A and Allwinner's 4.9.191 A133 kernel from source. Its modules
cannot be JukaHub OS layers on the stock kernel (vermagic/CRCs); the Speed layer
(`trimui/os-layers/speed`) is the stock-kernel part. The image builders exist
but have not been boot-tested.
**Where.** `.github/workflows/plastron-image.yml`, `os/plastron/` (Brick),
`os/smartpro/` (Smart Pro: the card boots the device's own
boot0/U-Boot/kernel/rootfs saved by `dump-board.sh`, with `/jukahub/init` as
process 1; see its README), `docs/JUKAHUB_OS.md`.
**Done when.** The licences of plastron and TortOS allow redistribution (else
stop); a Brick boots the image into JukaHub (timed against 1.9 s) with
a working SDL2 video path — the PowerVR GE8300 userspace copied from the owner's
own stock firmware at install, or SDL2 KMSDRM/fbdev with software rendering —
plus Wi-Fi firmware, audio, sticks, LEDs and battery, and taking the card out
still boots the stock system. On the Smart Pro: boot0 copied from the eMMC loads
U-Boot from the card, U-Boot honours the rewritten `root=PARTUUID=…`, and the
stock daemons the hardware needs beyond `trimui_inputd` are listed.

### SYS-25 — Bluetooth, third round (P2, M)
**Why.** Remembered devices reconnect, a headset drop pauses, each output keeps
its volume and pads show battery (`bt_autoreconnect.go`, `bt_audiofollow.go`,
`bt_padinfo.go`), but the radio is never switched on at boot (the loop only
reconnects when the firmware or the user switched it on), a running game keeps
the speaker when a headset connects mid-game (ALSA opens its device once), and a
headset's own battery is not shown.
**Where.** `bt_autoreconnect.go`, `bluetooth_bringup*.go`, `audioroute*.go`,
`bluetooth_devices_ui.go`.
**Done when.** JukaHub remembers that Bluetooth was on and brings it up at start
(bounded, off the render thread); a headset that connects during a RetroArch
game gets its sound, through a BlueALSA-aware ALSA plug or a resume-safe restart
the user agrees to; a headset's row and sheet show its battery from BlueZ's
`Battery1` ("Battery Percentage" in `bluetoothctl info`) when it reports one.

### SYS-26 — Linux packages on a TrimUI card (P1, M) [HW] [NET]
**Why.** Linux packages (`linuxpkg_*.go`, docs/LINUX_PACKAGES.md) runs Ubuntu
Noble's own loader (glibc 2.39) and programs from the SD card on kernel 4.9;
none of it has run on a device.
**Where.** Stock Smart Pro (exFAT card), Tools → Linux packages, the Terminal,
an SSH shell; `data/linux/`.
**Done when.** 1. `apt update` verifies the Release signature (log: the
HTTPS→HTTP fallback line if any) and its time is recorded; `apt install python3
perl` logs "linuxpkg: loader runs", and `python3 -c 'import ssl, sqlite3;
print(ssl.OPENSSL_VERSION)'` and `perl -MIO::Socket::SSL -e 1` work in the
Terminal; the space used is recorded. 2. Audit toolkit: sqlmap with no Python
installs Python first on the same bar, Nikto installs Perl + TLS modules, Nmap /
John / Aircrack-ng install and show their version; `nmap -sS` against your own
router as root; John finds its config (else give it one). 3. `git clone
https://…`, htop and nano in an SSH session after `. <data>/linux/env.sh`, and
`apt install` from SSH asking [Y/n]. 4. Update all after a noble-updates
release upgrades and Remove deletes exactly the package's files.

---

## Sound and haptics (SND)

### SND-2 — One audio mixer on the speaker too (P2, M)
**Why.** On the headset route every JukaHub output already feeds one small mixer
on a single SDL device (`audioroute.go`), but on the speaker route each output
(UI sounds, video/music, background music, the synth, the boot chime) still
opens its own SDL device: up to five ALSA streams on a 1 GB handheld.
**Where.** `audioroute.go` (the `out*` functions and the mixer), `sound.go`,
`bootchime.go`, `ledsync.go` (`audioBus`).
**Done when.** The speaker route uses the same mixer on one stereo device (the
per-output passthrough is gone), and the LED analyser is fed from the mix.

### SND-3 — Sound packs (P3, M)
**Done when.** Chime and UI-sound variants install as a package category
(scores + constants as data, not samples) and are selectable in Settings.

### SND-5 — The tg5040 mixer, checked on a device (P1, S) [HW]
**Why.** Volume on the Smart Pro and the Brick drives the codec's "digital
volume" (an attenuation: 0 is loudest, 63 quietest), mutes through "DAC Volume"
and keeps `/mnt/UDISK/system.json` "vol" in step (`volume_trimui.go`). The
control names are confirmed on a Smart Pro (`docs/HARDWARE_VALIDATION.md`).
Stock 1.1.0 keymon maps step 0 to raw 63 and audible steps 1..20 to
`floor(48 × (20 − step) / 20)`; live steps 1 and 4 matched raw 45 and 38.
JukaHub now uses that curve on a stock Smart Pro and keeps the earlier curve
for NextUI. A reversible `amixer -c audiocodec` write moved raw 38 to 39 and
back without changing `system.json`. Listening quality and the interplay with
keymon during JukaHub playback still need a run.
**Where.** `volume_trimui.go` (`trimuiStockAttenuationFor`; the probe logs the
controls it found once, tag `volume`), `systemcontrol.go`, `volumectl.go`,
`alarmvol.go`.
**Done when.** On both devices 100% is the loudest and 0% is silent, the steps
feel even, un-mute restores the level, the hardware keys carry on from a level
set in JukaHub (or the drift is written down), and the alarm's ramp leaves
`system.json` alone.

### SND-6 — Follow the firmware's own volume changes (P2, M) [HW]
**Why.** The stock keymon owns the hardware keys and may re-apply its own level
— for example when headphones are plugged in, with a separate "hpvol" in
`system.json` — over a level JukaHub set. And a USB DAC present at boot becomes
ALSA card 0, so the codec's controls are not where `amixer`
looks.
**Where.** `volume_trimui.go` (probe and poll), `audiooutput.go` (the output
re-probe already sees headphones and Bluetooth), `volumectl.go`.
**Done when.** The volume card follows what keymon did after a headphone plug
or unplug (reading back, never fighting it), the probe finds the codec's card
by name instead of assuming card 0, and a USB or Bluetooth output gets the
generic mixer path or an honest "volume is set on the device" note.
**Smart Pro evidence, 2026-11-17.** Its wired jack is an evdev switch on the
`audiocodec sunxi Audio Jack` input device; the sysfs switch and extcon paths
are absent. The read-only `EVIOCGSW` query changed from unplugged to plugged
when headphones were inserted, and the audio-output probe now uses it. Volume
level read-back and USB/Bluetooth handoff are still untested.

**Card-order and stock-curve fixes, 2026-11-17.** The stock codec advertises ALSA ID
`audiocodec`, and `amixer -c audiocodec` worked on the Smart Pro. The mixer
now selects that ID from `/proc/asound/cards`, so a USB DAC assigned card 0
does not redirect built-in speaker volume changes to the DAC. A stock volume
step 1 read raw 45 and step 4 read raw 38; these matched the stock keymon's
linear attenuation formula and replaced the old logarithmic curve on this
model. A live boot with a USB DAC and JukaHub volume changes still need
checking, as does volume control on the DAC itself.

### SND-7 — Spatial audio listening test (P1, S) [HW]
**Why.** The virtual surround is a parametric model (a spherical head, four
virtual loudspeakers, a small room) checked against its reference by numbers,
never by ear; the defaults were chosen on paper.
**Where.** `player/spatialfx/jh_spatial_ref.py` (the spec: constants change
there only, then `player/testdata/spatial` is regenerated and both ports
follow), `spatial_dsp.go`, `spatialfx/jukahub_spatial.c`,
`docs/SPATIAL_AUDIO.md`.
**Done when.** On a Smart Pro and a Brick, with wired headphones and one
Bluetooth headset: the three rooms at 50, 75 and 100% and the Speakers mode on
the built-in pair are heard with music, a spoken-word track and a game; the
centre image, the width behind you and the loudness match are judged; the CPU
cost while a video plays is measured; the defaults are confirmed or tuned; and
the notes go into `docs/SPATIAL_AUDIO.md`.

### SND-9 — The game plugin's interfaces against the real headers (P1, S) [NET]
**Why.** The RetroArch DSP-filter and LADSPA declarations in
`player/spatialfx` were written from memory, without the upstream headers at
hand.
**Where.** `player/spatialfx/jukahub_spatial.h`, `jukahub_spatial.c`,
`README.md`; libretro-common's `libretro_dspfilter.h` and `dsp_filter.c`;
`ladspa.h`.
**Done when.** Every struct, typedef, flag and entry point matches the headers
(or is fixed) and the README says so; then DSP API v2 (RetroArch's int16
`process_i16` path) is decided.

### SND-10 — Discrete 5.1 and 7.1 sources (P3, M)
**Why.** Media reaches the spatializer already folded to stereo by ffmpeg
(`-ac 2`), so a film's surround channels come back only as far as the stereo
difference carries them.
**Where.** `videoplayer.go` (`startFFmpegAudio`), `audioresume.go`,
`spatial_dsp.go`, `jh_spatial_ref.py`.
**Done when.** With spatial audio on, a 5.1 or 7.1 source is decoded with its
own channels, each plays from its own virtual loudspeaker (centre ahead,
surrounds behind, LFE to both ears), the decoders' frame arithmetic follows the
channel count, and the spec has golden vectors for the multichannel path.

### SND-11 — The game filter on Windows (P3, S) [WIN]
**Why.** `player/spatialfx/build.sh` can build a Windows DLL with MinGW, but no
Windows package carries one, so **Spatial audio in games** says it is not
available there.
**Where.** `player/spatialfx/build.sh` (`TARGET=windows`), the Windows release
job, `spatial_games.go` (where it looks).
**Done when.** The Windows archive carries the DLL where `spatial_games.go`
looks, and a RetroArch game started from the library on Windows plays through
it.

### SND-13 — Spatial audio in games and apps, on a device (P1, S) [HW]
**Why.** Every screen-taking child gets an ALSA chain through the
`jukahub_spatial` LADSPA plugin (`spatial_alsa.go`: `ALSA_CONFIG_PATH`,
`AUDIODEV=jhspatial`, after `JukaHub --spatial-alsa-probe` passes), and
RetroArch keeps its own DSP filter only where its binary is proven to load it
(`spatial_games.go`). The chain was checked against a PC's alsa-lib only, and
whether `ra64.trimui` and CrossMix's RetroArch load filter libraries is unknown.
**Where.** `spatial_alsa.go`, `spatial_games.go` (the probe, its cache,
`prepareSpatialAudio`), `screengroup_unix.go`, `player/spatialfx`; the log's
`spatial` lines and RetroArch's `[DSP]` lines.
**Done when.** On a Smart Pro and a Brick: both probes' verdicts are logged
(record the firmware RetroArch's here); with a mode on, PPSSPP standalone,
DraStic and RetroArch (stock and CrossMix) are heard through the effect on the
speakers and on a Bluetooth headset; the firmware's `asound.conf`/`.asoundrc` is
recorded (a custom `default` falls back to SDL-only); a demanding core (N64,
PSP) keeps full speed; `retroarch.cfg` has the user's own value back after the
game; Juka's in-game voice still plays while a game holds the output.

### SND-14 — Juka's Groq voice, second round (P3, S)
**Why.** Juka's Groq voice (`tts_groq.go`, `tts_play.go`) fetches each chunk's
WAV whole before it plays, remembers only the terms and key refusals, plays at
unity gain, and is English only (Groq's Orpheus English model).
**Where.** `tts_play.go`, `tts_groq.go`, `tts_settings.go`.
**Done when.** If the device run (HW-3 step 2) shows that the first-word delay
is the download, the WAV body streams into the feeder as it arrives; a 400 or
404 for the speech model is remembered for the run like the terms; a voice
volume slider sits under **Voice**; a second language follows INP-18's choice
where Groq has a voice for it.

### SND-16 — Juka on the stick lights, on a device (P2, S) [HW]
**Why.** The Juka layer on the stick lights (`ledjuka.go`, setting `led_juka`)
changes hue only through dark so the TrimUI `led_anim` rings do not blink on a
re-arm; whether a re-arm at a near-zero scale is really invisible, and whether
the firmware lets JukaHub take the rings mid-game, has not been seen.
**Where.** `ledjuka.go`, `ledanim.go` (`applyLEDAnim`), `ledsync.go`.
**Done when.** On a Smart Pro: listening, thinking, speaking and answer phases
show without a flash between them (else write the scale before the re-arm); with
the lights off and **Always**, the rings light for Juka and return to the
firmware colour; the in-game assistant over a RetroArch game lights them and the
game-time colour comes back when it closes; levels feel right at the default Mic
sensitivity.

### SND-17 — Stick lights: the colour path, on a device (P1, S) [HW]
**Why.** The 2026-11-18 blink fix (`ledanim.go`) picks how a colour reaches the
TrimUI rings by reading `frame_hex` back: frame, direct, or a dip through dark.
None of these paths, nor whether `frame_hex` reads back the rings at all, has
been seen on a device; nor whether `max_scale` dims a `frame_hex` frame.
**Where.** `ledanim.go` (`ledAnimProbeDue`, `ledAnimLand`, `ledAnimCheck`),
`ledsync.go` (`ledMusicColor`, `ledRainbowSteps`), `ledindicators.go`.
**Done when.** On a Smart Pro, a Brick and a Smart Pro S, the "lights taken"
line and the proof lines are recorded; Colour/Solid shows no blink for 10
minutes (also while charging), Rainbow and Music change colour without a dark
frame (or with the soft dip only), Brightness and Off still dim a frame-driven
ring, and a charger plug-in gives at most one "changed by something else"
repaint. If Colour/Solid still blinks with no "colour written" line, record
`effect_duration_lr`/`effect_cycles_lr` and test the static effect's own cycle.

---

## Web builder (WEB)

### WEB-1 — Split `script.js` into modules (P1, L)
**Why.** ~330 KB single file: the largest single-file risk in the repo.
**Done when.** ES modules by concern (canvas, inspector, scenes, export/import,
validation, gallery, AI panel), the CSP still holds (`_headers`), and the jsdom
+ Playwright suites pass unchanged.

### WEB-2 — PWA with stale-cache recovery (P3, M)
**Done when.** A service worker with versioned cache and a "new version" prompt;
favicon/icons meet install minimums (branding assets exist).

### WEB-3 — Focus-order editor (P2, M)
**Done when.** The builder shows and edits D-pad order per scene, exported in a
form `focus.go` reads.

### WEB-4 — Reusable element components (P3, M)
**Done when.** Element markup comes from one component factory instead of per
type assembly.

---

## Security and privacy (SEC)

### SEC-1 — One extraction path through `os.Root` (P2, M)
**Why.** hakuX, the Packages store (`pkgExtractZip`), Files, `tools.go`,
`xpack.go` and the Windows runtime extract through `os.Root`; the other
extractors still guard paths by hand.
**Where.** `patch_engine.go` (`extractZipSafe`/`extractTarGzSafe`, used by
`patch_flow.go`, `ports.go`, `psremote_helpers_install.go`), `emu7z.go`,
`ports_store_install.go`, `ppsspp_managed.go`, `tts_install.go`,
`emulatorupdates.go`, `androidpak.go`, `relpack.go`, `ssyncbundle.go`, `bkup.go`
(restore).
**Done when.** Every archive extraction writes through an `os.Root` opened on
its destination (reusing `pkgExtractZip`'s refusals); traversal tests stay
green.

### SEC-6 — Plugin permissions and signing (P1, L) [OWNER]
**Why.** Plugin scripts run with JukaHub's privileges, unsigned
(`plugins.go` `plugExecute`, `docs/PLUGINS.md:8`).
**Done when.** Manifests declare permissions (network, files outside the
plugin folder, run programs, settings); the install sheet lists them; scripts run
with a minimal environment, a working directory inside the plugin, a timeout and
(on Linux) a dropped-privilege user if the image allows; catalogue plugins are
signed and sideloaded ones say so.

### SEC-8 — Secrets that are actually secret (P1, M)
**Why.** The default key is a public constant (`crypto.go` `defaultSecretText`),
so the secret store is obfuscation; the RetroAchievements key is plain JSON;
Discord login still accepts user tokens (`chat.go` `discordAuthCandidates`,
against Discord's terms).
**Where.** `crypto.go`, `retroachievements.go`, `chat.go`.
**Done when.** The key is device-bound (Windows DPAPI; TrimUI: random key in a
`0600` file outside the user-visible tree, created on first run); existing
secrets are re-encrypted; a config copied to another device asks for its secrets
again instead of failing; RA and every API key go through the store; Discord
uses bot or OAuth tokens only.

### SEC-10 — Supply chain in CI (P2, S) [NET]
**Done when.** The weekly govulncheck is blocking for reachable vulnerabilities,
CodeQL runs weekly on Go and JavaScript (`.github/codeql/` is empty; not a new
per-push job), the container image and SDL
tarballs are covered by dependabot or a pinned-digest check.

### SEC-11 — A privacy page that tells everything (P3, S)
**Done when.** Settings → Privacy lists every network feature, what it sends,
where, and whether it is on; "Export my data" writes a zip of every user file.

### SEC-16 — Signed package catalogue, hashes mandatory (P1, M) [OWNER] [NET]
**Why.** The store checks a `sha256` when the jukaLang/Packages registry publishes
one, but the registry publishes none yet and is not signed, so every store
install is "Not verified" and whoever controls the registry controls the hashes.
`patch_signed.go` signs only the Patch repository's own index format, with the
development key (REL-3 owns the production key).
**Where.** jukaLang/Packages (`packages.json`), `player/pkgverify.go`
(`pkgApplyIntegrity`), `player/pkgstate.go` (`pkgCatalogFetcher`),
`player/patch_signed.go`, `docs/PACKAGES.md`.
**Done when.** The registry's CI writes `sha256` and the exact `size` for every
entry and a detached Ed25519 signature (`packages.json.sig`, canonical bytes, key
id) with the production key from REL-3; the store verifies the signature when
fetching the list (offline keeps the last verified list); once every entry has a
hash, an entry without one is refused and the "Not verified" path is removed; a
test feeds a tampered list and a missing hash.

### SEC-19 — The pairing code never crosses the network in clear (P2, M)
**Why.** The Live Builder is plain HTTP: someone on the same Wi-Fi who sees one
request learns the code. Risky designs still wait for the owner, but an ordinary
layout push or a (redacted) pull would go through.
**Where.** `livebuilder.go`, `builder-device.js`.
**Done when.** Requests are authenticated with an HMAC of the method, path, body
hash and a timestamp/nonce keyed by the code (or TLS with a pinned self-signed
certificate shown as a QR), the code itself is never sent, replays are refused,
and tests cover a replay and a stale timestamp.

### SEC-21 — Package swap on Windows and FAT, on the device (P2, S) [HW]
**Why.** The store's crash safety rests on sync/fsync ordering on the handheld and
on per-file flushes plus rename retries on Windows; none of it has run on
hardware.
**Where.** `player/pkgstage.go`, `player/pkgverify.go`, `player/pkgstate.go`.
**Done when.** On a Smart Pro (exFAT), pulling power during "Installing" of an
update ten times leaves exactly one version with the user's files after each
restart; on Windows, updating a package while its folder is open in Explorer and
while the store scans either retries and succeeds or fails with "the installed
version is unchanged"; a card with less free space than the download is refused
before any byte is written; an entry with a wrong `sha256` is refused and a
correct one shows "Verified download".

### SEC-29 — Build the arm64 parts as an unprivileged user inside the build root (P3, S)
**Why.** `.github/scripts/build-root.sh` runs every third-party build (FFmpeg,
opus, json-c, libevent, SDL2, chiaki-ng with curl, Box64, the pip wheels) as
root in a chroot with the runner's `/proc` mounted, so a compromised pinned
source can read the runner's artifact token and leave a poisoned
`remote-play-helpers-<hash>`, `androidrt-pack-` or `wine-runtime-pack-` artifact
that later runs reuse.
**Where.** `.github/scripts/build-root.sh` (`in_root`, `build`, `check`),
`player/native/build-helpers.sh` and `build-runtimes.sh` (the folders they
write: `DEPS_PREFIX`, `/tmp/jukahub-*`, `player/native/bin` through `/src`).
**Done when.** `build` and `check` run the scripts as a build user (`chroot
--userspec`) that owns only the folders they write, `create` and the package
installs stay root, and the builds still pass.
**Verify.** One helper and one Box64 run on GitHub, and `id` in a build log.

### SEC-30 — Startup lock, the remaining paths (P2, M)
**Why.** The startup lock (`bootlock*.go`) covers the screen, the companion
(HTTP 423), FTP/SSH, USB file transfer, voice and Quick Settings, but not
everything that can act on a locked device.
**Where.** `peernet.go`, `livebuilder.go`, the Steam OpenID return
(`/steam/return`), `powersleep.go` and the parked game loop in `main.go`, the
Reset settings, backup restore and config-history paths, plugin startup hooks,
`bootlock_ui.go`.
**Done when.** Paired devices' save sync and hand-off and the Live Builder are
refused while locked; a Steam sign-in cannot complete behind the lock; with
"after sleep" a game paused by the sleep stays paused under the lock instead of
resuming playable; Reset settings, a backup restore or a config rollback cannot
remove or change the lock (or "Lock when") without the code; plugin startup
actions wait for the unlock; a PC mouse can click the lock's power choices; the
PBKDF2 cost is timed on the A133 ("Checking…" under about a second, else lower
the stored iteration count for new hashes).

---

## Docs and project (DOC)

### DOC-4 — Decisions recorded (P3, S)
**Done when.** ADRs exist for the input vocabulary (`inputhints.go`), the
string catalogue (I18N-1), the plugin sandbox (SEC-6) and the overlay approach
(SHELL-2).

---

## Input and controls (INP)

The hint layer (`inputhints.go`) decides whether hints name the pad's buttons or
the keyboard's keys; these tasks make the rest of the input match it.

### INP-1 — Keyboard parity, screen by screen (P1, M)
**Why.** XAG 112: every screen must be usable by keyboard *and* by controller
alone. Known gaps: the Terminal's history list and card show "Start Run" with no
key behind it on a keyboard (`terminal_ui.go`, the history footer); Core builds'
"Start Auto-update" has no key in `handleCoreBuildsKey`, and "L1/R1 Filter"
should read "L1/R1 / [ ]" (`corebuilds_ui.go`).
**Where.** Every `handle*Key` and its `*FooterHints`; `inputhints.go`
(`hintKeyboardTwin`, the `"pad / key"` badge convention); the "Keyboard keys,
screen by screen" table in `docs/CONTROLS.md`.
**Done when.** For each screen in `docs/CONTROLS.md`, every footer hint is
pressable on a keyboard: either the generic twin is true there or the hint is
written `"<pad> / <key>"`; the table stays in step.
**Verify.** INP-12's checker reports zero mismatches.

### INP-2 — Sentences that name buttons (P1, M)
**Why.** Prose such as "Press Y on any video…" or "A to type" bypasses the hint
layer and lies on a keyboard. `hintSentence`/`hintSentence2` (`inputhints.go`)
write such sentences in the hints' words and follow remaps, but the grep
`'"[^"]*\b(Press|press|Hold|hold) [ABXY]\b'` still matches literals in about 60
files.
**Where.** The files the grep lists; `hintSentence` in `inputhints.go`.
**Done when.** Each match goes through `hintSentence` or is rewritten without a
button, and the grep lists only the deliberate physical exceptions: hakuX's exit
chord, "Press A on a controller to join" (hotseat), the one-handed help text and
the `inmap*.go` Select+R1 chord (pad-only: remap, no keyboard words).

### INP-3 — Controller glyph sets (P2, M)
**Why.** A DualSense or Switch Pro pad on Windows shows Xbox letters; on a
PlayStation pad "A" is ✕ in the wrong place. SteamOS and Xbox show the glyphs
of the pad in hand.
**Where.** `inputhints.go` (a third vocabulary dimension: pad family), SDL
`GameControllerGetType` (go-sdl2 v0.4.40), `gamepad_hotplug.go`.
**Done when.** Xbox, PlayStation (✕ ○ □ △, L1/R1), Nintendo (B/A swapped
positions, L/R, ZL/ZR) and TrimUI families are named correctly in every hint;
Settings → Button hints gains "Pad style: Auto / Xbox / PlayStation / Nintendo".

### INP-5 — Type on a real keyboard everywhere (P2, M) [HW]
**Why.** With a keyboard plugged into the handheld, Chat (`chatinput.go:212`)
and the terminal (`terminal.go` ~1389) open the on-screen keyboard because the
code asks `P().IsHandheld()`, and search opens it unconditionally
(`features.go:109`).
**Done when.** Text fields accept physical typing whenever a keyboard is
attached (`hintAttachedKeyboardName() != ""`); the on-screen keyboard opens only
from the pad; verified with a USB keyboard on a Smart Pro.

### INP-6 — Verify keyboard detection on hardware (P1, S) [HW]
**Why.** `hintParseInputDevices` follows udev's keyboard rule and skips gpio and
uinput devices, but the firmware's own devices were never seen.
**Done when.** `/proc/bus/input/devices` from a Smart Pro and a Brick (stock
and with PortMaster's key mapper running) is pasted into a test fixture; a USB
keyboard (OTG), a Bluetooth keyboard and a Logitech Unifying receiver are tried;
the `keyboard attached:` log lines are recorded in `docs/CONTROLS.md`. Known
limit: a receiver or gaming mouse that exposes a keyboard interface counts as a
keyboard (the D-pad switches the hints back).

### INP-7 — Mouse and touch parity (P2, M)
**Why.** Many footer actions exist only as buttons (Clear all in the
notification centre is mouse-only; many are pad-only). On Windows the mouse is
the third input.
**Done when.** Every footer hint badge is clickable (generalise `emuHintHits`),
right-click is the screen's secondary action (X), the wheel scrolls every list.

### INP-8 — Keyboard profiles in the control-profile engine (P3, M)
**Where.** `inmap*.go` (profiles map pad buttons only).
**Done when.** A profile can bind keys to actions; the hint layer reads it, so
a remapped key is what the hint shows.

### INP-9 — Controller database and HIDAPI on Windows (P2, S) [WIN]
**Why.** Only one X360 mapping is added (`main.go:534`, `platform:Linux`, so
none on Windows) and no `SDL_HINT_JOYSTICK_HIDAPI*` hints are set. SDL's
defaults and built-in database cover most pads, but an unknown pad is a
"joystick" SDL cannot open as a controller — and then the hints say "keyboard"
(correctly, but the pad is useless).
**Done when.** `gamecontrollerdb.txt` (pinned, embedded) is loaded at boot,
HIDAPI hints enable DualSense/DS4/Switch Pro/8BitDo; a "Controller not
recognised — map it" flow uses the controller test (`ctrldiag.go`) to write a
mapping.

### INP-11 — Players and pad status beyond the Bluetooth page (P3, M)
**Why.** The Bluetooth page numbers connected pads, shows their battery and
warns at 15 % (`bt_padinfo.go`, `bt_autoreconnect.go`), but only for Bluetooth
pads on the handheld and only on that page; USB pads and Windows get nothing.
**Where.** `bt_padinfo.go`, `quicksettings.go`, `gamepad_hotplug.go`,
`hotseat.go`.
**Done when.** Quick Settings lists each connected pad with its player number
and battery (Bluetooth from `bt_padinfo.go`, others from
`SDL_JoystickCurrentPowerLevel`, so Windows too); the player LED is set where
the pad has one; hotseat uses the same numbering.

### INP-12 — Hint truth checker (P2, M) [WIN]
**Why.** INP-1 regresses silently unless something checks it.
**Where.** A `tools/hintcheck` program (or a scenedump mode, TEST-1) that, for
each scene and state, renders the footer in both vocabularies and replays each
hint's key through the scene's key handler with a recording stub.
**Done when.** A `make` target runs it and lists mismatches.

### INP-15 — The firmware button swap for the built-in pad only (P3, S) [HW]
**Why.** `applyButtonSwap` (`core.go`) flips A/B and X/Y for every pad on a
handheld, so an external Xbox-layout pad on a TrimUI (Local play opens extra
pads, `hotseat.go`) comes out mirrored on both pairs.
**Where.** `core.go` (`applyButtonSwap`, `padFaceButton`), the polling helpers
that mirror it (`inmapPhysicallyHeld`, `jlandPadHeld`, `vkLogicalHeld`), and the
synthetic presses (`padRepeatWhich`, `inmapSynthWhich`, the phone remote's
pushes, which use `Which` 0 — also the first real pad's instance).
**Done when.** The swaps apply to the firmware pad ("TRIMUI Player1", 045e:028e)
and to synthetic presses only, the polling helpers match per pad, and an
external pad on a Smart Pro reports its printed letters.

### INP-17 — Mouse mode, second round (P2, S) [HW]
**Why.** Mouse mode (hold Select + L1) and the games mouse (vcursor_bridge_linux.go)
shipped with known gaps.
**Where.** vcursor_mode.go, vcursor_bridge_linux.go, inputhints.go, the footer.
**Done when.** The footer says the D-pad moves the pointer while mouse mode is on;
the chord works from a second pad; the pointer hides over full-screen video in
mouse mode (partly done 2026-11-19: it dozes after 10 s unused,
`pointerIdleHide`; hiding at once when a video goes full screen is still open);
the games mouse honours the Mouse mode switch, offers the right
stick as a wheel and a speed setting, and — if HW-2 step 2 shows SDL apps miss a
mouse that appears mid-run — is created idle when the child starts or handed to
it through `SDL_EVDEV_DEVICES`; an LED colour while it is on (ledpanel).
**Verify.** Unit tests for the state machine; HW-2 step 2.

### INP-18 — Voice command in more languages (P2, M)
**Why.** Voice command understands English only: its phrase tables are English
and it asks Whisper for English.
**Where.** `voicecmd_intent.go` (phrase tables), `voicecmd.go` (the
transcription request); the keyboard layout's language
(`vkCurrentLayout().Lang`) or the UI language as the source.
**Done when.** At least one more language has its phrase table, is picked from
the layout or the UI language, and English stays the fallback.

### INP-19 — Offline voice recognition on Windows (P3, M) [WIN]
**Why.** Offline speech (`stt_local.go`: Vosk with a Whisper fallback, the
`JukaHub-OfflineSpeech-linux-arm64.zip` pack from `stt_install.go`) runs only on
the Smart Pro (`sttSupported`); on Windows, Voice command, the orb and
voice typing need a Groq key and internet.
**Where.** `stt_local.go`, `stt_install.go`, the release workflow that builds
the pack, `voicecmd.go`.
**Done when.** Decision: the same engines, not System.Speech — a
`JukaHub-OfflineSpeech-windows-amd64.zip` pack is built and published, installs
like the arm64 one, and on a PC without a Groq key Voice command, the orb and
voice typing work offline; with the pack missing they say so and keep the Groq
path.

### INP-20 — Voice on the handheld's microphone: the hardware proof (P2, S) [HW]
**Why.** The orb, Voice command and voice typing share one microphone path
(`voicemic.go`: SDL's default device, the kernel's capture PCMs through
`plughw`, SDL's list, `arecord`, a 1.2 s silence check, and the tg5040 codec's
"ADCL Input MIC1 Boost Switch" / "ADCR Input MIC2 Boost Switch"), built from the
Allwinner driver's control names; the orb's end-of-speech thresholds (`orbVAD`)
are guesses and the Voice command page still uses fixed ones.
**Where.** `voicemic.go` (candidates, mixer, channel choice), `voiceorb.go`
(`orbVAD`, `orbSilencePeak`), `voicecmd.go` (the page's fixed levels).
**Done when.** On a Smart Pro and a Brick the log's `mic` lines say which device
answered and which switches were set; a clip ends about a second after the
speech without cutting words; the thresholds come from measured levels and the
page uses `orbVAD` too; if the stock firmware has no capture path at all, the
orb says so and the docs say which firmware does.

### INP-22 — The voice orb from a pad on Windows (P3, S) [WIN]
**Why.** On a PC the orb's key is F4; a couch player with only a pad has no
voice button. The handheld's Menu is GUIDE, but on Windows the Xbox button
opens Game Bar first, and a PlayStation pad's PS button is GUIDE too.
**Where.** `voiceorb.go` (`voiceOrbMenuOwnsGuide`), Settings → Controls (the
"Menu button: voice" row is handheld-only).
**Done when.** An opt-in switch on Windows makes the pad's Guide (or a long
press of Back) open the orb, with the Game Bar caveat in its second line and
`docs/CONTROLS.md`.

---

## Shell, multitasking and notifications (SHELL)

### SHELL-1 — Game switcher on a device (P2, S) [HW]
**Why.** The Game switcher (`gameswitcher.go`, `gameswitcher_ui.go`) turns on
RetroArch's `savestate_thumbnail_enable` for JukaHub's launches through the
scoped config edit and shows each recent game's newest state picture; it has not
run on a handheld.
**Where.** `gameswitcher.go` (`prepareGameSwitcher`, `gswStates`,
`gameSwitcherFromGameMenu`), `gamemenu.go`, `racfgscope.go`.
**Done when.** On a Smart Pro: three RetroArch games played from the library
show the moment they were left (opened by X on Home and by Start → Games → Game
switcher); A resumes each where it was; the in-game menu's "Game switcher" row
quits the game and opens the switcher; the user's own
`savestate_thumbnail_enable` is back after the game exits; X removes a game and
keeps its saves; with the switcher off, a state saved in a library game still
shows its picture on the game page's states strip.

### SHELL-2 — Indicators drawn inside a running game (P2, XL) [HW]
**Why.** The in-game menu (`gamemenu.go`, hold Select + L2) pauses any game
under JukaHub's own overlay (resume, music, save/load state, rewind, fast
forward, Ask Juka, Game switcher, close background apps, sleep, quit, and since
2026-11-20 a Quick settings card with brightness, volume and radios), but it
has no performance-profile row, and nothing JukaHub draws is seen
while a game runs: volume, brightness and low-battery changes are invisible (the
low-battery LED in `ledindicators.go` is the only sign). muOS draws such
overlays through an `LD_PRELOAD` hook on SDL/GLES swap.
**Where.** `gamemenu.go` (`gmItems`), `powerprofile.go`; a small C hook library
next to `androidrt/` (swap-buffers hook, own GLES drawing); `volumebuttons.go`,
`lowbattery.go`.
**Done when.** The in-game menu gains a Performance profile row; an opt-in
hook draws a volume, brightness and low-battery indicator into
RetroArch and the standalone PPSSPP, with no frame-time cost visible in
RetroArch's own FPS readout, and a game whose renderer the hook cannot find runs
unchanged.

### SHELL-3 — Notifications that can be acted on and survive (P1, M)
**Why.** The centre (`notifications.go`) keeps 50 entries in memory, has no
actions, loses everything on restart, toasts replace each other, and "Clear all"
is mouse-only.
**Done when.** Entries persist (bounded file), carry a source and an optional
action ("Install update", "Open download", "Retry"), group by source, A runs the
action and X dismisses, Y clears all; toasts queue by priority instead of
replacing; quiet hours and "while playing" are settings.

### SHELL-5 — Commands in global search (P2, M)
**Done when.** Search understands verbs: "wifi off", "brightness 40",
"screenshot", "sleep", "update", "new note"; results show the current value;
typos within one edit still match (no fuzzy library, a bounded Damerau check).

### SHELL-6 — Back and focus that remember (P2, M)
**Why.** Leaving a screen and coming back resets focus in places; the back
stack (`sceneHistory`) restores the screen but not where the user was.
**Done when.** Each scene saves focus + scroll on leave and restores it on
return (bounded map); Home restores the last focused card after a game.

### SHELL-7 — Recent screens switcher (P3, S)
**Done when.** Holding Start (or Alt+Tab on a keyboard) shows the last five
screens as cards; release opens the highlighted one.

### SHELL-9 — The tip of the day takes A, a click and a tap (P2, S)
**Why.** Home draws the day's tip above the footer (`helpdoc.go`
`homeTipDraw`, called from `HomeLayout.drawFooter`) and Settings → About opens
Controls and Quick start offline, but the tip is not a Home focus item, so A
cannot reach it, and a click or tap is not routed to `homeTipClick`. The day a
tip was hidden (`homeTipHiddenDay`) is not listed as bookkeeping, so the
settings report shows it as a changed setting.
**Where.** `helpdoc.go` (`homeTipClick`, `homeTipOpen`), `homefocus.go` /
`homelayout.go` (a focus item for the tip), `home.go`
(`handleHomeMouseClick`), `hometabs*.go` for the Tabs Home, `cfgdoc.go`
(`cfgdocRuntimeKeys`).
**Done when.** The tip is focusable on Standard, Hero and Tabs (Down from the
last row, Up from the footer); A opens its CONTROLS.md section and X hides it
for the day; a click or tap does the same; `homeTipHiddenDay` is in
`cfgdocRuntimeKeys`.

### SHELL-10 — Clock faces and a real standby screen (P3, M)
**Done when.** The screensaver and dock offer three clock faces (analogue,
digital, minimal) with OLED-safe pixel shift; the choice is a theme-able token.

### SHELL-11 — Close all apps & games on a device (P2, S) [HW]
**Why.** Close all (`closeall*.go`) finds untracked apps by a `/proc` name and
folder table and never touches JukaHub's group, its ancestors, the paused
firmware loops (`/tmp/jukahub-paused`) or the offline AI servers; the table was
written from the code and has not met a running handheld.
**Where.** `closeall_linux.go` (`closeAllRules`, `closeAllNeverRules`),
`closeall.go`, `gamemenu.go`.
**Done when.** On a Smart Pro (stock and CrossMix): Close all over a RetroArch
game, a PortMaster port, a Wine program under Box64 and Remote Play closes each
(RetroArch saves on the interrupt; an orphan from a killed JukaHub saves on
SIGTERM) and names them in the toast; no firmware process matches a rule;
**Close background apps** from the in-game menu leaves the game paused and
resumable and the paused pids stopped; the in-game card fits on the Brick and
the Smart Pro; "Close apps and try again" after an offline-AI memory error
answers the question.

## Power, sleep and battery (PWR)

### PWR-3 — Save every kind of game before the battery dies or the power goes (P1, M) [HW]
**Why.** The 3 % guard (`lowbattery.go` `protectLowBattery`), Power off after sleeping and Power off
with a game running (`quickresume.go` `quickSaveHeldGame`,
`quickPowerOffSavesFirst`) save RetroArch only, through its network commands; a
standalone emulator (PPSSPP, DraStic, Flycast), a port or an Android/Windows app
is never saved, so the sleep deadline only sleeps again and the low-battery
guard suspends it unsaved.
**Where.** `lowbattery.go`, `quickresume.go`, `savestate_resume.go`,
`ppsspp_managed.go`, `gamesuspend.go`.
**Done when.** Each launcher family has a "save and stop" action — PPSSPP's
save-on-exit with auto-load of that state at the next start, DraStic's SIGTERM
handling, Flycast's auto-save setting; hakuX, AndroidRT and ports: `SIGSTOP` +
sync, never killed mid-write — and the low-battery guard, Power off after
sleeping and Power off use it, so those games also come back through Resume game
at power on (`resume-at-power-on.json`); a test covers each family's choice.

### PWR-6 — Profiles that follow the situation (P2, M)
**Why.** A per-game CPU profile (Auto / Battery saver / Balanced / Performance,
in the game options sheet) applies to the library, ports, emulators and LAN play
through `beginLaunchPowerProfile`, but `raemu.go` still uses the global
profile (`beginGamePowerProfile`), and nothing changes by system or on the
charger (SteamOS issue #3034 asks for the same).
**Where.** `powerprofile.go`, `raemu.go`, the game options sheet.
**Done when.** Every launch path goes through `beginLaunchPowerProfile`;
profiles exist per game, per system and per power source (battery / charger),
most specific wins; the game options sheet shows which one applies.

### PWR-9 — Battery estimates per activity (P3, M)
**Why.** `battery_eta.go` learns one drain rate for everything, while the menu,
an emulator, video and a Remote Play stream drain very differently; and
JukaHub does not run during a stream, so the estimate starts over when it
comes back.
**Where.** `battery_eta.go` (the learned rate in `data/battery_eta.json`),
`native/stream_sys.c` (the helper's status file), `psremote_lastrun.go`.
**Done when.** The learned rate is kept per activity and the estimate uses the
one that is running; the helper's status file carries the battery level at the
start and the end of a stream, and JukaHub learns the stream's rate from it.

### PWR-10 — Travel lock on the FN switch: verify on the device (P2, S) [HW]
**Why.** `travellock.go` reads the Smart Pro's FN switch as gpio243 (PH19)
and takes its position when the setting is turned on as
"unlocked", so the pin's polarity never had to be guessed; it has not run on a
device.
**Where.** `travellock.go`, `powersleep.go` (`travelLockKeepAsleep`, the
screen-off wait), the boot hook `System/starts/00-jukahub-travel-lock.sh`.
**Done when.** On a Smart Pro: gpio243 flips with the switch (exported by
JukaHub when nothing had); a locked sleep ignores power presses and a charger
plug and still wakes for an alarm; a power-on with the switch locked turns off
within a few seconds of the boot hook and leaves no stale `.autostart-pending`;
the stock "FnKey Setting" function (CPU mode, LED, mute) does not get in the
way. Find the Smart Pro S's switch (the row is hidden there) and, if the AXP717
can be told to ignore the power key for power-on, use that instead of booting
and powering off.

---

### PWR-11 — Power off saves without touching the player's save slot (P1, S) [HW]
**Why.** `retroArchSaveAndQuit` (`lowbattery.go`), used by the 3 % guard and by
Power off after sleeping — which a battery at 25 % or less now triggers even
with the setting off (`quickresume.go`) — sends `SAVE_STATE` before `QUIT`.
`SAVE_STATE` writes the player's current slot and can overwrite a manual save
there, while resume at power-on loads the `.state.auto` RetroArch writes on
quit (`savestate_resume.go` turns `savestate_auto_save` on for JukaHub
launches). Stable RetroArch has no `SAVE_STATE_SLOT`.
**Where.** `lowbattery.go` (`retroArchSaveAndQuit`), `quickresume.go`,
`savestate_resume.go`.
**Done when.** On a Smart Pro, `QUIT` alone with the auto-save switch on is
shown to write the `.state.auto` that resume loads; `SAVE_STATE` is then sent
only when that switch is off for the launch; a manual save in the current slot
survives a low-battery power off.

## Reliability, updates and recovery (REL)

The boot guard counts starts and keeps the last good settings (`bootguard.go`);
REL-10 proves it on a device, and these build on it.

### REL-3 — Signed updates only (P1, M) [OWNER]
**Why.** A release without `release.json` installs on the GitHub checksum alone
(`autoupdate.go:690`); the only embedded key is the development key
(`patch_signed.go:24-31`); no workflow publishes `release.json` today, so every
update is checked by the GitHub digest alone; TUF is a design note
(`docs/UPDATER_TUF.md`).
**Done when.** CI signs every release manifest with a production key held as a
secret (ceremony in `docs/UPDATER_TUF.md`), listing every asset the release
carries — the app archives and the `JukaHub-RemotePlay`, `JukaHub-AndroidRT` and
`JukaHub-Wine` zips, which the helper installer and `relpack.go` check against
it when present (`docs/PACKAGES.md` shows the command) — the app embeds that key
plus one rotation slot, and an unsigned or expired manifest is refused with a
clear message (sideload keeps its explicit trust prompt).

### REL-7 — JukaHub as the default shell: crash relaunch and the boot paths (P1, S) [HW]
**Why.** The TrimUI boot hook (`autostart.go` `trimuiAutostartTemplate`) skips
MainUI with a stand-in in JukaHub OS mode and otherwise queues JukaHub in
`/tmp/cmd_to_run.sh` and closes MainUI once it is up. A start that hangs keeps
the next boot on stock once (`.autostart-pending`), but a crash simply hands
back to MainUI.
**Where.** `autostart.go`, `launch.sh`, `crashreport.go`, `docs/BOOT.md`.
**Done when.** A crash relaunches JukaHub once, then falls back to stock with a
note at the next JukaHub start; the chord to reach stock is in `docs/BOOT.md`;
on stock 1.1.x and CrossMix, `Apps/JukaHub/autostart.log` shows which path ran
(stand-in, queue before the menu, after closing it, or the direct fallback) and
`docs/BOOT.md` records it.

### REL-8 — Reproducible releases with an SBOM (P1, M) [NET]
**Why.** `release.yml` builds TrimUI and Windows from a tag, but the builds are
not reproducible (date stamps, no `-trimpath` in
`.github/scripts/trimui-build.sh`, `docker-image.yml` or `build.bat`, unpinned
mingw/brew), release notes are GitHub's generated ones, and no SBOM or
provenance is attached.
**Where.** `.github/workflows/release.yml`, `.github/scripts/trimui-build.sh`,
`.github/workflows/docker-image.yml`, `player/build.bat`, `player/release.sh`.
**Done when.** Every release build uses `-trimpath` and `SOURCE_DATE_EPOCH`
instead of the date stamp, toolchains are pinned, an SPDX SBOM and SLSA
provenance are attached; asset names match `patch_swap.go`; release notes come
from the tag's CHANGELOG section (the in-app changelog reads them).

### REL-10 — Prove recovery on a device (P1, S) [HW]
**Why.** The boot guard, crash reports, the recovery card and the `.restart-now`
restart have never been exercised on a device.
**Where.** `bootguard.go`, `crashreport.go`, `recoverycard.go`,
`recoverysettings.go`, `safeboot.go`, `launch.sh`.
**Done when.** On a Smart Pro: a forced panic (on the main goroutine and on
another one) leaves `crash-*.txt` and `errors.txt` shows "JukaHub CRASHED"; one
on another goroutine while a RetroArch game runs logs "JukaHub left a game
running (process group N)", the game saves and quits and MainUI comes back
alone; the
next start shows the crash card; Send report opens a prefilled form; Restart in
safe mode comes back in safe mode through `launch.sh`; three kills before the
first frame start safe mode, while three kills after it give safe mode but no
rollback; a fresh build that crashes three starts is rolled back from Version
history, and a restored build that fails three more gets safe mode, not a second
rollback; `patch/lastgood/` appears 90 s into a normal start and is offered
first; on an install without Version history "Previous version" restores the
update backup with Restart now; the chord card's choices work with the pad and a
USB keyboard. On Windows: the respawn after Restart, crash files in
`%LOCALAPPDATA%\JukaHub\logs`, and `JukaHub.exe.old` gone after the next start.

### REL-12 — Count starts that die in SDL init (P3, S) [HW]
**Why.** The boot guard now runs right after `img.Init`, but a build that fails
`sdl.Init`/`ttf.Init`/`img.Init` (logFatal, exit 1) is never recorded: the chord
check needs SDL, and the record notes whether the chord was held.
**Where.** `player/main.go` (startup), `player/bootguard.go`.
**Done when.** The record is written (running, chord unknown) before `sdl.Init`
and amended after the chord check, or REL-10's device run shows it is not worth
it and the decision is recorded here.

### REL-13 — Turn on the bug-report service (P1, S) [OWNER] [NET]
**Why.** Every build posts Report a bug to
`https://generate.jukalang.com/api/bug-report`
(`netlify/functions/bug-report.mjs`), which files a public GitHub issue — but
only once the site has a token; until then devices fall back to the phone code.
**Where.** Netlify → generate.jukalang.com → Environment variables;
[docs/BUG_REPORTS.md](../docs/BUG_REPORTS.md).
**Done when.** `JUKAHUB_GITHUB_TOKEN` (a bot account's token with Issues
read/write on jukaLang/JukaHubV2) is set and deployed; `curl -i` on the address
answers 405 with `"configured": true`; a report from a device becomes an issue
with the `from-device` label and the sent sheet shows its number.

### REL-14 — A package counts as installed only while its files are there (P3, S)
**Why.** The Patch manager now reads `db.json` back after a restart, so
`resolveInstall` skips a dependency the database lists even when a reflash or a
card swap deleted its files, and Remove and History trust the record.
**Where.** `patch_db.go` (`dbIsInstalled`), `patch_resolve.go`, `patch_flow.go`
(`patchInstalledRelPath`).
**Done when.** A package counts as installed only while at least one file it
owns (mapped with `patchInstalledRelPath`) exists; a record with none left shows
as "files missing" with Reinstall and Forget.

---

### REL-15 — Watch updates on the JukaHub OS SD image too (P2, M)
**Why.** The update check lives in `player/launch.sh`. The standalone JukaHub
OS image (`player/cmd/jukaboot`, `os/smartpro/launch.sh`) starts JukaHub from
its own Go loop and does not export `JUKAHUB_UPDATE_PROBATION`, so an update
there writes no probation plan and a build that hangs before its first frame
is not put back.
**Where.** `player/cmd/jukaboot/launch_linux.go`, `os/smartpro/launch.sh`,
`player/updrescue.go`, `player/startcheck.go`.
**Done when.** jukaboot exports the variable and runs the same watchdog (ready
within the limit, a `SIGABRT` dump, one restore, `.update-rescued`), and
UPDFIX-HW-1's hang case passes on the SD image.

### REL-16 — Finish a restore that a power cut interrupted (P3, S)
**Why.** The update check copies every backup beside its target, then renames
them over one by one. A power cut between two renames leaves old and new files
mixed, and the next start drops the plan without a note.
**Where.** `player/launch.sh` (restore), `player/updrescue.go`.
**Done when.** A synced marker is written before the renames and removed after
them; a start that finds it finishes the remaining renames from the
`.rescue-tmp` copies before starting JukaHub and writes `.update-rescued` as
usual; a shell simulation stopped between two renames recovers.

### REL-17 — Version history says how each version started here (P3, S)
**Why.** `patch/versions/started.json` and `rescued.json` record "started fine
on this Smart Pro" and "didn't start here, skipped", but Settings → Updates →
Version history does not show them.
**Where.** `upch_ui.go`, `uhist.go`, `updrescue.go`.
**Done when.** The running version and each saved one show one line from those
records ("Started fine on this Smart Pro" / "Didn't start on this Smart Pro —
not offered again"), with a date only when the clock was plausible.

## Accessibility (A11Y)

### A11Y-8 — The legible reading font downloads on a device (P2, S) [HW] [NET]
**Why.** **Legible reading font** only found faces already installed, so a
TrimUI fell back to Inter. Turning it on now downloads Atkinson Hyperlegible
(Andika for Russian) into the fonts folder through `uifont.go`'s downloader;
offline it waits for the network, a language change fetches the language's face,
and faces that cannot draw the language are skipped (`legiblefont.go`,
`legiblefont_get.go`). Nothing is bundled, so no OFL text is needed in the
repository. Written without a Go toolchain, a device or network access.
**Where.** `legiblefont.go`, `legiblefont_get.go`, `uifont.go` (the `andika`
catalogue entry), `a11y_settings.go`; docs/ACCESSIBILITY.md.
**Done when.** Each step holds or a FIX task names what differs:
1. Smart Pro, Russian interface, online, Legible reading font on: the toast
   counts "Downloading Andika… N%", then says "Legible reading font: Andika";
   the log has `font: legible reading font: downloading Andika for ru` and
   `font: legible reading font: Andika is in …/data/fonts`; menus fit Andika's
   tall line metrics and no label ends in "…".
2. English interface, Wi-Fi off, switch on: "Legible reading font on · Atkinson
   Hyperlegible downloads when JukaHub is online"; with Wi-Fi back the download
   starts within about 30 s.
3. A restart with the setting on after deleting `data/fonts/atkinson-*.ttf`
   downloads it again about 20 s after start.
4. On a PC, google/fonts' `ofl/andika/Andika-Regular.ttf` and `Andika-Bold.ttf`
   (raw.githubusercontent.com, branch `main`) answer 200, and the `andika`
   entry's `KB` in `uiFontCatalog` matches their real size.
**Verify.** Record in the HW log.

---

## Languages and locales (I18N)

### I18N-1 — The string catalogue, finished (P1, M)
**Why.** `i18n.go` and `player/i18n/*.json` (English text as key, %d/%s
patterns) translate centrally in the measure/fit/draw helpers, and Settings →
General → Language offers es, pt-BR, fr, de, it and ru (machine-translated).
Missing: plural rules, the OS locale as the default, a report of untranslated
strings, and about 370 direct `SizeUTF8` measurements that bypass the catalogue.
**Where.** `i18n.go`, `player/i18n/`, the `SizeUTF8` call sites, a check under
`tools/`.
**Done when.** Plurals follow CLDR rules for each shipped language; every
measurement goes through the translated text; the first start picks the OS or
firmware locale when a catalogue exists; a CI check lists untranslated strings
per language; missing translations fall back to English.

### I18N-UK-1 — The Ukrainian catalogue, finished (P3, M)
**Why.** Ukrainian joined the interface languages on 2026-11-21 (`i18n/uk.json`,
1,717 strings): every label of up to 40 characters, the music player and the
iPod layout. Longer help texts, the first-run tour (`uk.tour.json` does not
exist) and the strings added since fall back to English.
**Where.** `player/i18n/uk.json`, a new `player/i18n/uk.tour.json`.
**Done when.** Every key of `ru.json` and `ru.tour.json` has a Ukrainian value
written or checked by a Ukrainian speaker, and the catalogue test passes.

### I18N-3 — Fonts for every script (P1, M) [HW]
**Why.** Only Inter ships, and Settings → Display → Font (`uifont.go`) replaces
it with one downloaded face at a time; there is no per-glyph fallback, so
`fonts.go` turns Chinese, Japanese, Korean, Arabic, Thai and Hindi titles into
look-alikes or "…".
**Where.** `fonts.go`, `uifont.go`, `cachebudget.go`.
**Done when.** A fallback chain loads Noto Sans CJK / Arabic / Thai / Devanagari
subsets on demand (downloaded like the `uifont.go` faces, never bundled in the
binary), bounded by `cachebudget.go`; SDL_ttf's HarfBuzz shaping is used if the
stock library has it (check the firmware's SDL_ttf version first).

### I18N-4 — Input methods (P2, L)
**Done when.** The on-screen keyboard has pinyin and kana composition with a
candidate strip (`vkeyboard.go` already has a suggestion strip); physical
keyboards get `SDL_TEXTEDITING` handling.

### I18N-5 — Human translations (P2, M) [OWNER]
**Why.** Six languages ship machine-translated (`player/i18n/`) and nobody
reviews them; Chinese and Japanese have no catalogue (they also need I18N-3's
fonts).
**Done when.** A translation platform (Weblate or Crowdin, free for OSS) is set
up on the catalogue; Spanish, Portuguese (Brazil), Russian, German and French
are reviewed for Home and Settings; Chinese (Simplified) and Japanese reach Home
+ Settings coverage.

### I18N-6 — Right-to-left layout (P3, L)
**Done when.** Arabic/Hebrew mirror the Home grid, lists and footers; text uses
bidi reordering (golang.org/x/text/unicode/bidi).

### I18N-7 — Dates in the mail, calendar, ports and Steam screens (P2, S)
**Why.** Dates, numbers and sizes go through `clockformat.go` and follow the
language and the Date format setting, except in files that were being edited
at the time: Mail, the mail calendar, calendar reminders, the Ports store and
Steam still hard-code "Jan 2, 2006", "Mon" and their own 12/24-hour switch, so
they stay English. Toasts and notifications (`wrapWordsMeasured`,
`notiftext.go`) still split words with `strings.Fields`, so a French or Russian
number can break at its no-break space.
**Where.** `mail_ui.go` (437-465, `mailSizeText`), `mail_compose.go:95`,
`mailcal_ui.go:653-673`, `calsync_remind.go:331`, `ports_store.go:828`,
`steam_ui.go:1482-1484`, `notiftext.go`; helpers in `clockformat.go`
(`formatDate`, `formatDateNear`, `formatDateBrief`, `formatWeekdayShort`,
`formatMonthYear`, `clockDigits`, `formatSize`, `isLineBreakSpace`).
**Done when.** None of those files calls `Format` with a month, weekday or clock
layout for text on screen, `mailSizeText` is `formatSize`, and
`wrapWordsMeasured` splits with `strings.FieldsFunc(para, isLineBreakSpace)`.

---

## Network and connectivity (NET)

### NET-2 — Wi-Fi for real networks (P2, M) [HW]
**Why.** No hidden SSIDs, WPA-Enterprise, static IP, proxy or QR join
(`wifi*.go`).
**Done when.** "Add network" takes a hidden SSID and security type, EAP-PEAP
with identity/password (through the secret store), static IPv4/DNS, and an
HTTP proxy used by every client in `httpclients`; a Wi-Fi QR (`WIFI:T:…;S:…;`)
can be shown for sharing and scanned through the phone companion.

### NET-3 — Verify network time on the device (P1, S) [HW]
**Why.** Clock sync (`ntpsync.go`, `clockset_linux.go`) has not run on a device.
**Where.** `ntpsync.go`, `clockjump.go`, `datetime.go`, `launch.sh` (TZ block).
**Done when.** On a Smart Pro and a Brick after a cold boot: joining Wi-Fi sets
the clock within ~20 s (the Logs show "clock set from …"), Settings → Date &
time → Clock last set shows it, the process really runs as root (settimeofday
succeeds), an alarm due in the corrected time rings, blocking UDP 123 falls back
to the HTTPS date, and with HTTPS blocked a clock more than 10 minutes off is
left alone (Clock last set says the HTTPS date did not confirm it). Whether the
image has a CA bundle (system store or one of `bugrepCAPaths`) is recorded here;
without one a cold boot's clock is set from SNTP alone (errors.txt: "HTTPS
confirmation is unavailable").

### NET-5 — Hotspot and tethering (P3, S) [HW]
**Done when.** The probe for AP mode (`iw list`, hostapd on the image) is
recorded; if supported, Quick Settings offers a hotspot with a QR to join.

### NET-6 — USB file transfer on a device (P2, M) [HW]
**Why.** USB file transfer (`usbmtp*.go`: JukaHub's own MTP over the kernel's
USB gadget, on by default, Settings → Network) has not met a computer on real
hardware. The stock Smart Pro 1.1.0 kernel explicitly lacks ECM and RNDIS
configfs functions, so USB networking is closed for this image; see
`docs/USB_FILE_TRANSFER.md`.
**Where.** `usbmtp.go`, `usbmtp_linux.go`, `usbmtp_proto.go`,
`docs/USB_FILE_TRANSFER.md`.
**Done when.** On a Smart Pro with Windows 11, a Mac (OpenMTP) and Linux: the
card appears, a 2 GB ROM copies both ways, a folder made on the computer shows
in Files, turning the switch off gives the port back to the firmware's mode
(ADB), and the startup lock hides the card; the throughput is in
`docs/USB_FILE_TRANSFER.md`.

### NET-7 — Network shares in Files and the library (P2, L)
**Done when.** SMB2/3 (go-smb2) and WebDAV locations can be added in Files
(credentials in the secret store), browsed, copied from, and used as a read-only
ROM or media source with local caching.

### NET-8 — Check the VPN on the device (P2, S) [HW]
**Why.** The VPN (Settings → Network → VPN) was checked against the kernel's
WireGuard, its routes and sockets in a network namespace (`wg_test.go` replays
the kernel transcripts), but never on the TrimUI's kernel.
**Where.** `vpn.go`, `vpn_linux.go`, `vpnd_linux.go`, `wg_*.go`, `docs/VPN.md`.
**Done when.** On a Smart Pro and a Brick: a provider's `.conf` imported from
the card and from the phone connects within seconds of switching the VPN on (the
row says Connected; Logs show "connected to …"); the browser shows the VPN's
address; netplay and a PS Remote Play stream away from home go through it while
the LAN (companion, FTP, Remote Play at home) stays reachable; switching it off
restores the routes and `resolv.conf`; a cold boot with the clock in the past
still connects; `kill -9` of the tunnel process falls back to Wi-Fi and the next
start restores `resolv.conf`. Whether the firmware has `/dev/net/tun` (or the
`tun` module) and runs JukaHub as root is recorded here. On Windows, with
WireGuard for Windows installed: one prompt per change, a service named JukaHub,
the LAN still reachable.

**Smart Pro prerequisite, 2026-11-17.** `/dev/net/tun` exists on stock 1.1.0
and JukaHub launches as root. A provider configuration and a live tunnel are
still needed for this check.

### NET-17 — A VPN kill switch (P3, S) [HW]
**Why.** While the VPN is on, a tunnel process that dies takes its routes with
it and the device falls back to its normal connection until the worker starts
a new one (5 s to a minute): traffic leaks meanwhile.
**Where.** `vpnd_linux.go`, `vpn.go`.
**Done when.** An optional "Block traffic while the VPN is down" adds
unreachable `0.0.0.0/1` and `128.0.0.0/1` routes at a higher metric than the
tunnel's, which outlive the tunnel process and go when the VPN is switched off
(and at boot); the LAN route still wins.

### NET-12 — Verify corrected time after a cold boot (P3, S) [HW]
**Why.** `rtcsync_linux.go` already writes the RTC through `RTC_SET_TIME` after
network time corrects the system clock, and logs the result. This stock Smart
Pro has `/dev/rtc0`; its RTC and system time agreed during the SSH inspection.
The remaining question is whether a correction survives an actual power cycle.
**Where.** `ntpsync.go`, `rtcsync_linux.go`, `docs/HARDWARE_VALIDATION.md`.
**Done when.** After deliberately correcting a stale clock through JukaHub's
network-time path, power off fully and boot without Wi-Fi; the clock starts
from the corrected RTC value. Repeat on the Brick. A device without an RTC
continues without a hardware-clock write.

### NET-15 — FTPS (explicit TLS) for the FTP server (P3, M)
**Why.** FTP sends the password and files in plaintext; SFTP covers clients that
speak it, but Windows Explorer only speaks FTP.
**Where.** `ftp_server.go`, `nettoolsscreen.go`.
**Done when.** `AUTH TLS` with a persisted self-signed certificate whose
fingerprint the sheet shows, `PROT P` data channels, and the sheet recommends
FTPS or SFTP.

### NET-16 — The script guard knows where the page itself lives (P2, M)
**Why.** Scripts on a public page may not reach the LAN (`webdom_net.go`). A
public DNS name that resolves to a LAN address — split-horizon DNS, a Tailscale
`*.ts.net` name — is therefore classed as public, and its own scripts cannot
reach their own site. Looking the name up again to classify it would reopen the
DNS-rebinding hole the dial-time check closes.
**Where.** `textbrowser.go` (`fetchTextBrowserOnce`), `webdom_net.go`
(`webJSNetPageGuard`, `newWebJSNet`), `webdom_runtime.go`.
**Done when.** The page fetch records the address it actually connected to
(`httptrace.ClientTrace.GotConn`) and the guard classes the page by that address:
a page whose server is on the LAN reaches the LAN, a public page still cannot,
and a test covers both.

### NET-20 — Windows notices a network change made outside JukaHub (P3, S) [WIN]
**Why.** A network picked in Windows' own flyout waits for the 20 s probe tick;
Windows has no Wi-Fi keeper to nudge the monitor.
**Where.** `wifi_windows.go`, `netmon.go` (`netNudge`).
**Done when.** `NotifyIpInterfaceChange` (or `NotifyAddrChange`) calls
`netNudge()`, and switching networks in Windows updates JukaHub's status within
about a second.

### NET-22 — Check the DNS choice on the device (P2, S) [HW]
**Why.** Settings → Network → DNS (2026-11-18) rewrites `resolv.conf` in place
and keeps writing it back after DHCP; it was written against the VPN's DNS code
and never run on a TrimUI, whose udhcpc script and `resolv.conf` link it relies
on.
**Where.** `dnschanger.go`, `dnschanger_linux.go`, `docs/DNS.md`.
**Done when.** On a Smart Pro and a Brick: picking Cloudflare puts
`nameserver 1.1.1.1` under JukaHub's marker line in `/etc/resolv.conf` (Wi-Fi
details show it) and the row says "for the whole device"; turning Wi-Fi off and
on (a new DHCP lease) gets the choice back within a few seconds and Logs show no
"no answer" line; AdGuard blocks ads in the browser and a PortMaster port;
Automatic, a quit and a `kill -9` followed by a start each put the router's
server back; the VPN with a `DNS =` line takes over while on and hands back
when off. Where `/etc/resolv.conf` leads on each firmware is recorded here.

---

## Game library and emulation (LIB)

### LIB-3 — Collections that build themselves (P1, M)
**Why.** The Tabs Home's Collections tab has user collections
(`collections.json`: ordered, renamed, deleted, up to 100 × 500) and the
automatic Most played, Recently added and Never played
(`hometabs_collections.go`), but no smart collections from metadata, and the
Games library does not show any of them.
**Where.** `hometabs_collections.go`, `gmeta_dat.go`, `game_library_ui.go`.
**Done when.** Smart collections — by genre, by decade, by player count (from
`gmeta_dat.go` metadata) — join the automatic three; the Games library shows the
same collections as Home (one store, `collections.json`); Settings can hide the
automatic ones; the 32-tag limit becomes a bounded list per collection.

### LIB-5 — Rich media per game (P2, M) [NET]
**Why.** A game has one picture: libretro box art, or a title screen or snap
when there is no box art (`artwork_match.go`); no descriptions, screenshots,
videos or manuals.
**Where.** `artwork_match.go`, `gmeta_ui.go`, the PDF reader, `cachebudget.go`.
**Done when.** Snaps and title screens from libretro-thumbnails show beside the
box art, and (with GAME-8's account) descriptions, screenshots, videos and
manuals come from ScreenScraper; a manual opens in the PDF reader; everything is
bounded by the cache budget.

### LIB-6 — Verified dumps (P3, M)
**Why.** Nothing tells a good dump from a bad one, which is behind many "the
game does not start" reports.
**Done when.** No-Intro/Redump DATs install as a package; the library verifies
CRC/SHA-1 in the background and badges games Verified / Unknown / Bad dump.

### LIB-7 — BIOS doctor with hashes (P2, S)
**Where.** `biosdoctor.go` (filenames for PS1/Dreamcast/Saturn; SHA-256 shown,
never compared).
**Done when.** A hash table (libretro `System.dat`) covers every system with a
BIOS; results say which file is missing or wrong and where it goes.

### LIB-12 — RetroAchievements, all the way (P2, M) [NET]
**Why.** `retroachievements.go` reads profile, recent unlocks and progress only;
the API key sits in plain JSON.
**Done when.** Per-game achievement lists with progress, a hardcore toggle that
writes `cheevos_*`, the key in the secret store, and an LED flourish on unlock
(`ledsync.go`).

### LIB-13 — Playtime you can trust (P2, M)
**Why.** Per-game time is keyed by title, capped at 64 games, days are pruned
at 90 (no year in review) and `jukauser.json` is rewritten every 30 s.
**Done when.** Sessions are appended to their own bounded log keyed by content
hash; 400 days are kept; Wrapped covers a year; muOS-style export (CSV/HTML) to
the card; the user config is no longer rewritten for playtime.

### LIB-14 — "Runs well here" labels (P2, M) [HW]
**Why.** Xbox's handheld labels and SteamOS's verified badges answer "will it
run?" before launch; `compat.go` has local data only.
**Done when.** A curated per-system/per-game table (package-delivered,
community-editable) shows Great / Playable / Poor on the game page with the
recommended core and profile.

### LIB-15 — A game page worth opening (P2, M)
**Done when.** The game page (`gmeta_ui.go`) shows the hero art, description,
screenshots, achievements, last played, playtime graph, the save-state strip
and Play / Resume / Options as the first three actions (manuals and guides are
under More → Manual & guide since 2026-11-19).

### LIB-17 — Pin builds the user trusts (P2, S) [HW]
**Why.** The libretro buildbot has no stable per-core tree for linux/aarch64 and
states only CRC32 (checked over HTTPS), so "Install automatically" always takes
the newest nightly; JukaHub keeps the replaced build but cannot tell a good
nightly from a bad one. hakuX, the runtimes and a PC's emulators are never
reinstalled after a rollback (`emuRuntimeRecord.Installed`), but cores still
are, and a PC's standalone emulator cannot be held at its version.
**Where.** `emulatorautoupdate.go`, `emulatorinstall.go` (`planCoreUpdates`,
`startEmulatorRollback`), `corebuilds_ui.go`, `corebuilds_history.go`,
`emuautoupdate_desktop.go`, `emutiles.go`.
**Done when.** A core can be pinned on the Core builds page and a PC emulator
from its tile (the automatic pass and "Update all" skip it, the badge says
"pinned"); every core rollback (Emulators R1 and both Core builds page paths)
records the CRC32 it gave up in a per-core record that the automatic pass skips
(not `coreRefusalRecord`, whose wording says the core cannot run here); a test
covers both.

### LIB-18 — Save-state warning on the device (P2, S) [HW]
**Why.** The game page's "made with another build" line is driven by
`core-history.json` and state file times; FAT time resolution, a clock read
before the first network sync and launch scripts that pick the core through
variables can all make it miss a state (it must never invent one).
**Where.** `player/corebuilds_history.go`, `player/gmeta_ui.go`.
**Done when.** On a Smart Pro: updating a core with states on the card makes the
game page show the line and name the build; rolling back with R1 makes it
disappear; a stock `Emus/<system>/launch.sh` for GBA, SNES and PS resolves to the
core it really starts (adjust `gameCoreFromLauncher` if not); a core replaced
while the clock read before 2020 produces no line until the next replacement
made with a sane clock; a replacement JukaHub did not record (a wrong clock, or
RetroArch's own updater) is labelled "another build", never with the wrong
build's date.

### LIB-24 — The recommended-emulator ranking, measured (P2, S) [HW]
**Why.** `emubest.go`'s handheld ranking comes from firmware notes, release
notes and search snippets; standalone Flycast ahead of its core, the
mGBA core ahead of standalone mGBA, the Saturn order and the order of the light
cores are judgement calls (docs/GAME_LIBRARY.md lists the sources).
**Where.** `emubest.go` (`emuBestTable`), docs/GAME_LIBRARY.md.
**Done when.** On a Smart Pro with CrossMix, two or three demanding games per
console (DS, PSP, N64, Dreamcast, Saturn, PS1, GBA) are timed in each ranked
emulator (frame rate or the emulator's own speed readout, audio crackle); the
table is reordered where a lower-ranked one is clearly better, and the doc's
table and sources say what was measured.

### LIB-26 — A core-update source TrimUI's glibc can run (P1, M) [NET] [HW]
**Why.** On a stock Smart Pro the core-update log refuses most libretro nightly
cores for needing GLIBC/GLIBCXX newer than the firmware's
(docs/HARDWARE_VALIDATION.md); the guard keeps the installed cores, so Install
automatically updates almost nothing on a TrimUI. RetroArch's own Online
Updater installs those nightlies unchecked: on 2026-11-18 Juka's stock Smart
Pro could start no GB, GBC or GBA game (`mgba_libretro.so` needed GLIBC_2.35,
libm has up to 2.32). Since then `.github/workflows/retroarch-cores.yml` +
`player/native/build-cores.sh` build 14 light cores in the glibc 2.31 root as
the rolling release `cores-aarch64` (buildbot index format), and
`player/corefix.go` replaces a core that cannot load (kept build first, else
that release's) after a failed game and a minute after start.
**Where.** `.github/workflows/retroarch-cores.yml`, `player/native/build-cores.sh`,
`player/corefix.go`; for the pass: `emulatorupdates.go` (`planCoreUpdates`),
`emulatorautoupdate.go`, `emumorecores.go`.
**Done when.** 1. The workflow has run: the release lists which of the 14 cores
built (gpSP and PCSX-ReARMed with their aarch64 dynarecs or the generic build,
which C++ cores kept libstdc++ dynamic and with which GLIBCXX), and
docs/COMPATIBILITY.md's list matches. 2. On the stock Smart Pro with the
GLIBC_2.35 mGBA: one GBA game fails, the card's first finding is "JukaHub is
fixing the mGBA core", the toast follows, and the game, a GB and a GBC
game then start with sound and saves; a restart logs no `core fix:` line (the
load-check cache), and a C++ core of the release (Gambatte, Snes9x) is accepted
by the device's GLIBCXX check or the refusal is recorded here. 3. The daily
pass and Get more cores take the release's build for a core whose buildbot
nightly the guard refuses, so a Smart Pro's daily pass logs no GLIBC refusal for
those cores. **Built 2026-11-19 as the default (RAGLIBC-HW-1 measures it; Juka,
2026-11-18: "make sure cores get updated to latest versions").** Start
RetroArch itself through Ubuntu's glibc 2.39 loader, the way GreenOvercast and Linux packages run
(docs/PRIVATE_GLIBC.md, docs/LINUX_PACKAGES.md): then the buildbot's nightlies
would load as published and the guard could accept anything up to GLIBC_2.39
for that RetroArch. It needs the firmware's PowerVR GLES/EGL libraries to run
under the newer glibc and the `Emus/*/launch.sh` RetroArch line wrapped per
launch (as the GreenOvercast launcher is); GPU cores stay under LIB-27's
probation either way.

### LIB-27 — GPU cores, probation and the one-time repair on a device (P0, S) [HW]
**Why.** The 2026-11-18 N64 fix (`corerisk.go`, `corebuilds_probation.go`)
decides from ELF metadata and RetroArch's log lines that have not been seen on a
TrimUI: whether a buildbot GPU core links libGL.so.1, the exact "Loading dynamic
libretro core from" / HW-context lines of ra64.trimui, whether ra64.trimui quits
or stays in its menu when a core cannot open, and whether the one-time repair
finds the firmware's kept N64 build.
**Where.** Stock Smart Pro (and CrossMix); `core-probation.json`,
`core-history.json`, the JukaHub log; `Emus/N64/launch.sh` and `config.json`
(RetroArch core or the standalone Mupen64Plus of SD base 1.1.0).
**Done when.** 1. First start of the build: the log shows the repair decision
for each GPU core and N64 games start. 2. `core for this launch:` names the N64
core, its GPU library and origin. 3. Installing a buildbot Mupen64Plus-Next by a
press is either refused ("desktop OpenGL") or, if it cannot start a game, rolled
back after that game with the toast, and the next daily pass does not fetch it.
4. A game that runs ends the probation (`ran its first game here`). 5. The daily
pass's `core updates: N installed …` line is recorded in
docs/HARDWARE_VALIDATION.md for LIB-26.

### LIB-28 — More cores and "Which emulator?" on a real card (P1, S) [HW]
**Why.** "Get more cores" (`emumorecores.go`) and the ask rule
(`emupicker.go`, `emuPickAsk`) were written without a device or the aarch64
buildbot's real list.
**Where.** Smart Pro, stock and CrossMix; `data/patch/emu-morecores.json`.
**Done when.** 1. Settings → Games & emulators → Get more cores with games for
SNES, GBA, PS1, MD, PCE, Arcade and NGP: the toast says what was installed and
refused ("cannot be used here" = glibc 2.33), the log line `more cores: …` names
the suggested cores the buildbot lacks, and one game per console starts in a
fetched core (picker → the core row) with sound and saves. 2. With Ask which
emulator = First time per game, a game with two choices asks once, A remembers
it (Game options → Emulator shows it), Y asks again next time, and Continue,
the game switcher and the power-on resume behave as docs/GAME_LIBRARY.md says.
3. With Install automatically, the daily pass fetches each console's cores once;
a deleted core is not fetched again; Notify me / Off fetch nothing.

---

### LIB-29 — Manual pages in the in-game menu (P3, M) [HW]
**Why.** The in-game menu's Manual page (`gamemanual_ingame.go`) shows the
controls and text guides only: the Reader's renderer holds one global
document, and scanned pages are big decodes while the paused game keeps its
memory.
**Where.** `gamemanual_ingame.go`, `pdfv_doc.go`, `pdfv_view.go`,
`gameassist.go` (the overlay).
**Done when.** A document's pages draw in the in-game overlay from an
overlay-owned pdfv document with a decode size cap, textures freed when the
overlay closes; RAM measured with a PS1 and a PSP game paused.

### LIB-30 — PSP and Dolphin saves in the save handoff (P3, M)
**Why.** The save handoff (`savehandoff.go`) moves RetroArch battery saves and
states, NextUI saves and DraStic states, matched by ROM name. PSP SAVEDATA and
PPSSPP states are named by the disc's ID, and Dolphin's memory cards and Wii
saves by the game's ID; nothing in the repo reads those IDs yet.
**Where.** `savehandoff.go`, `ppsspp_*.go`, `dolphin*.go`.
**Done when.** A PSP game's SAVEDATA folders and PPSSPP states (by the ID read
from its ISO/CSO/PBP) and a GameCube game's GCI saves move with the game.

### LIB-31 — Share Game Lab setups from the phone (P3, S)
**Why.** Game Lab's profiles are files in `data/game-lab/export` and `import`;
the companion's Files card only shares the FTP shares, so moving one needs a
card reader or FTP.
**Where.** `gamelab_share.go`, `companion_files.go`.
**Done when.** Share in Game Lab offers a QR to download the profile on a
phone, and the companion can upload a profile into `game-lab/import` (JSON
only, 64 KB cap).

### PROF-2 — Per-person RetroAchievements and standalone-emulator saves (P2, M)
**Why.** Profiles other than the owner's play with RetroAchievements off, and
PPSSPP, DraStic and PortMaster ports keep one save folder for everyone. A
notice says so before the first game, but ports skip it because
`launchPortLibraryGame` bypasses the launch gates.
**Where.** `retroachievements*.go`, `profiles_saves.go`, the managed PPSSPP
launch (its memory stick folder), DraStic's launch environment,
`ports_library.go`.
**Done when.** A profile can sign in to its own RetroAchievements account;
PPSSPP gets a memory stick folder per profile; ports show the shared-saves
notice.

## Apps platform (APP)

### APP-1 — A store worth browsing (P2, M) [NET]
**Why.** The package catalogue (`pkgcatalog.go`, `jukaLang/Packages`) has no
screenshots, ratings or real dependencies (displayed only, `pkgui.go:1614`).
**Done when.** Catalogue entries carry screenshots, a description, size,
licence, source link and dependencies that install first; "Update all"; a
changelog per update; installs stay verified and staged (`pkgstage.go`) and
the catalogue is signed (SEC-16).

### APP-2 — A plugin SDK (P2, M)
**Why.** Plugins (`plugins.go`, `plugx.go`) can add themes, ROM formats,
profiles and packs, but there is no versioned API, sample or scaffold.
**Done when.** `docs/PLUGINS.md` documents a versioned manifest (with the
permissions of SEC-6), a sample plugin lives in `examples/`, and
`JukaHub --new-plugin <name>` (or a script) scaffolds one.

### APP-4 — Files that forgive (P1, M)
**Why.** Deletes are permanent (`feDeleteEntry` in `files_manager.go`,
`os.RemoveAll`); Files can unpack archives but not make one; there is no
in-folder search.
**Done when.** Delete moves to a `.trash` per volume (bounded by size and age,
"Empty trash" in the storage view, SYS-8), **Compress to zip** makes an archive
of the marked rows, Files has a filter-as-you-type search, and copy and move
run off-thread with progress (the extraction queue in `files_extract.go` is
the model: one job at a time, a staging folder, a drain on the SDL thread).

### APP-6 — The phone as a second screen (P2, M)
**Why.** The companion (`companion.go`) types, acts as a remote, uploads and
views the screen, but cannot browse the library or launch a game.
**Done when.** The companion page lists the library with art and launches a
game, uploads a save to the right folder, and mirrors JukaHub notifications;
every action goes through the existing pairing and queue.

### APP-7 — Notes that travel (P3, S)
**Done when.** Notes sync between paired devices through `ssync` and render
Markdown (headings, lists, checkboxes) in the reader view.

### APP-8 — File uploads in forms (P2, M)
**Why.** Text fields now submit by GET or POST, including multipart text parts,
and POST responses keep cookies through redirects. The reader still skips file
inputs, so forms that require an upload cannot be completed.
**Where.** `browser.go` (form submission), `webpage.go` (file control), Files
picker, `docs/BROWSER.md`.
**Done when.** A file input opens a bounded local-file picker and sends
`multipart/form-data` with the selected file, while a local test server asserts
the filename, bytes, other fields and cookie round-trip.

### APP-9 — A profile per Windows app (P3, M) [HW]
**Why.** Every Wine app shares one prefix and one controller mapping; the docs
say conflicting DLL overrides need their own `WINEPREFIX` and the Game Profiles
controller field is informational, so two apps that disagree cannot both work.
**Where.** `windowsapps.go`, `windowsapps_ui.go`, `gameprofiles.go`,
`docs/WINDOWS_APPS.md`.
**Done when.** An app can opt into its own prefix (created on first launch),
DLL overrides and a controller mapping, stored beside its profile; the setup
page shows which prefix an app uses; a second app with a conflicting override
launches with its own.

### APP-10 — Live bindings between ES modules (P2, M)
**Why.** goja has no modules, so `webdom_module.go` rewrites each one into a
function, and an imported name is a copy taken when the importing module starts.
Inside an import cycle a name read before its module ran gets a forwarding stub
(`webdom/85_scripts.js`), which covers functions and objects but not a `let` the
other module assigns later.
**Where.** `webdom_module.go` (references to imported names),
`webdom/85_scripts.js` (`modContext`, the namespaces).
**Done when.** References to an imported name in a module body read the
exporting module's namespace (with scope analysis in the rewrite, so local
variables and property names of the same spelling stay untouched), cycles
evaluate in the standard's order, and a test links a two-module cycle that
reassigns an exported `let`.

### APP-11 — Linked stylesheets for page scripts (P2, S)
**Why.** `getComputedStyle` (`webdom/45_css.js`) cascades inline styles and the
page's own `<style>` sheets only; linked sheets are fetched after the script
pass, for the renderer (`fetchWebStylesheets`). A script asking whether a panel a
linked sheet hides is visible hears "yes", and jQuery's `.show()` on such an
element writes nothing.
**Where.** `textbrowser.go` (`browseWebPageResult`), `webpage.go`
(`fetchWebStylesheets`), `webdom_runtime.go` (boot), `webdom/45_css.js`.
**Done when.** The first linked sheets (the renderer's limits) are fetched beside
the page's scripts and handed to the prelude before the parser scripts run, the
renderer reuses the same bytes instead of a second download, and a test shows
`.show()` on an element a linked sheet hides.

### APP-12 — Ranges that follow the document (P3, S)
**Why.** A `Range` (`webdom/55_traversal.js`) keeps its boundary nodes and
offsets when the DOM changes under it, so editors and highlighters that keep a
range across a change act on the wrong text.
**Done when.** Live ranges are tracked per document and their boundaries move on
insert, remove, `splitText` and `normalize` as the DOM standard says, covered by
the jsdom comparison in `tests/web/webdom_dom.test.mjs`.

### APP-15 — 7z without 7-Zip (P3, M)
**Why.** A handheld without `7zz` cannot open a .7z at all, although the
vendored `github.com/ulikunitz/xz/lzma` decodes the LZMA and LZMA2 that most 7z
archives use (`emu7z.go` chose 7-Zip because no compiler was at hand to test a
reader).
**Done when.** A 7z whose folders use LZMA or LZMA2 (with or without the BCJ
x86 filter) and whose header is not encrypted unpacks in-process through the
same staging and `os.Root` rules, with fixtures made by 7-Zip in the tests;
other coders (BCJ2, PPMd, AES) still go to 7-Zip.

### APP-16 — Ideas borrowed from WPE Browser (P2, M) [HW]
**Why.** JukaHub no longer opens or installs [WPE
Browser](https://github.com/matweew/wpe-tsp) (2026-11-19: the Browser is
built-in only), but WPE showed where JukaHub's own video and browsing paths are
slower or clumsier; these are the parts worth having in the built-in ones.
**Where.** `videoplayer.go`, `browser_youtube.go`, `browser_engines.go` (home
page choice), `browserzoom.go`, `installbar.go`.
**Done when.** Videos play through mpv-tsp (A133P hardware H.264) when installed
(the plan is [docs/VIDEO_VPU_OFFLOAD.md](../docs/VIDEO_VPU_OFFLOAD.md)); the
home page choice has "Last page"; zoom is remembered per site; the install
bar's "Ready" card stops saying "restart JukaHub" after an app install.
**Verify.** Unit tests; on the device, a video through mpv-tsp.

### APP-17 — The Windows runtime on a stock Smart Pro (P1, M) [HW] [NET]
**Why.** Box64 + Wine 10.0, WestonPack and the prefix rules come from upstream
docs and HWSTD's port and have not run on a device; the first-run time, the
unpacked Wine size and the stock firmware's mount, loop and mkfs tools are
guesses.
**Where.** `wine_pack.go`, `wine_prefix.go`, `windows_runtime_install.go`,
`wine_setup.go`; `data/windows-runtime/logs/wineboot.log`.
**Done when.** With a release that carries the `JukaHub-Wine` pack, on a stock
Smart Pro: Windows Apps → Setup lists the runtime as missing (about 130 MB),
WestonPack ("from PortMaster" when PortMaster already has it) and DXVK 1.10.3 as
optional; A on the first row goes from the downloads through "Unpacking Wine
10.0" and "Preparing Windows (first run, a few minutes)…" to "Windows runtime
ready", leaving `data/windows-runtime/{pack,wine-10.0,bin}` and
`/mnt/UDISK/jukahub/wine-prefix` with `system.reg` and `dosdevices/c:`; an older
Hangover install is gone; Wi-Fi off and a nearly full card give their plain
errors; after a session on the image prefix nothing is left mounted or bound to
`prefix.img` (`mount`, `losetup -a`). The first-run time and the unpacked size
are recorded, and `wineUnpackFactor` and the "about 700 MB" text follow them.
**Verify.** `wineboot.log` and a bug report from the device.

### APP-18 — The first Windows program on a Smart Pro (P1, M) [HW]
**Why.** The launch path (WestonPack mounted from its image, udev labelling, the
pad as mouse and keys, a bounded clean-up) has not run on a device. The default
display mode, `drm gl kiosk system`, uses Weston's DRM/GBM backend, which the
stock firmware may not offer, and `docs/WINDOWS_APPS.md` says that mode is shown
through Crusty, which the Westonpack wiki does not.
**Where.** `windowsapps.go` (`wineRun`), `wine_display.go`,
`wine_display_linux.go`, `vcursor_bridge_linux.go`, `windowsapps_sample.go`,
`docs/WINDOWS_APPS.md`.
**Done when.** The sample program (Setup → Sample program, or A on the empty
list; `windowsapps_sample.go`) downloads, and both Notepad++ builds, 64-bit and
32-bit (WoW64), open a window with a visible pointer; the sticks, A, the D-pad,
Start, Y, Select + Start (Alt+F4) and Select + L1 (pad to the program and back)
behave as `docs/CONTROLS.md` says, and Y, A and Start do nothing in JukaHub
before the window appears; after the program closes the list is back, the pad
works in JukaHub, `ps` shows no weston, Xwayland or wineserver and
`/proc/mounts` no `/tmp/jukahub-weston`; a second start is faster. Leaving Setup
during Install & Run installs without starting the program, A during an install
or update is refused, and a game started during "Preparing Windows…" keeps the
screen. Whatever a black screen needed (the OpenGL toggle, `JUKAHUB_WESTON_MODE`
in `weston.env`, `CFW_NAME=TrimUI`) becomes the default, and the doc says which
mode works. On Windows, Windows Apps runs an `.exe` and a `.bat` from
`%APPDATA%\JukaHub\windows-apps`.
**Verify.** The app's log, `cat /proc/bus/input/devices`, `cat /proc/mounts`,
`ls -l /run/udev/rules.d /tmp/run/udev/rules.d` and a bug report.

### APP-19 — A pinned checksum for WestonPack (P2, S) [NET]
**Why.** WestonPack is the only executed part of the Windows runtime without a
checksum: its image is checked by content alone (SquashFS 4 superblock,
compression id, 20–200 MB), then loop-mounted, and its scripts run as root.
**Where.** `windows_runtime_install.go` (`installWestonRuntime`,
`windowsWestonRuntimeURL`).
**Done when.** The download URL names a PortMaster-New commit instead of `main`,
the file's SHA-256 is pinned beside it (from portmaster.games' runtimes page,
PortMaster-New's `.md5` files or harbourmaster's runtime index), and
`wineDownload` checks it before the rename.

### APP-20 — Unpacking Wine fast enough on the A133P (P3, S) [HW]
**Why.** Wine 10.0's tarball (about 62 MB of xz) is unpacked with the pure-Go
xz reader, which may take minutes on a Cortex-A53.
**Where.** `wine_pack.go` (the Wine tarball unpack).
**Done when.** The unpack time on a Smart Pro is measured; if it is several
minutes, a system `xz -dc` (when present) streams into the same Go tar
extractor, with the pure-Go reader kept as the fallback.

### APP-21 — DXVK 1.10.3 on the GE8300, or no DXVK row (P3, S) [HW]
**Why.** The Smart Pro's Vulkan 1.1 driver can run at most DXVK 1.x; Setup
offers 1.10.3 as an experiment nobody has tried on PowerVR.
**Where.** `windows_runtime_install.go` (`dxvkPick`), `wine_setup.go`,
`wine_runtime.go` (`dxvkUsableHere`).
**Done when.** One D3D9 and one D3D11 title are tried with DXVK on and off; if
DXVK never works there, the row is gone on TrimUI and `docs/WINDOWS_APPS.md`
says why.

### APP-22 — Typing in Windows programs (P2, M)
**Why.** The pad's keys in a Windows program cover arrows, Enter, Esc, Tab,
Space and Alt+F4, so a program that asks for a name, a path or a serial cannot
be used on the handheld.
**Where.** `vcursor_bridge_linux.go` (the "JukaHub Mouse" uinput device, which
already declares keys 1–255), `companion*.go`.
**Done when.** Text typed on the phone companion (or picked letter by letter
with a pad chord) reaches the running program as key presses through the
JukaHub Mouse device, and the pad is the mouse again afterwards.

### APP-25 — Show the Windows wait hint on screen (P3, M) [HW]
**Why.** A Windows session waits (up to 12 h) while programs a launcher left
behind are still running; when only a hidden tool is left the WestonPack screen
is black and the player gets a buzz but no text, because JukaHub's loop is
parked and Weston owns the display.
**Where.** `windowsapps.go` (`wineWaitStarted`), `wine_prefix.go`
(`wineWaitScript`), `wine_display.go`.
**Done when.** While the wait runs and no program window is mapped, the screen
shows "Waiting for <name>'s other Windows programs to finish — hold Select +
Start to end" (a tiny client inside the session, or the display handed back to
JukaHub while Weston has no mapped window), and nothing covers a game on screen.

---

## Windows desktop (DESK)

### DESK-1 — No console window, a real log (P1, S) [WIN]
**Why.** Release builds lack `-H windowsgui` (`build.bat:354`, `release.sh`), so
a console opens with the app. Crash reports already land in
`%LOCALAPPDATA%\JukaHub\logs` (`crashreport.go`), but WARN+ lines still go only
to stderr, which a GUI build does not have, and `errors.txt` exists only on the
handheld (`launch.sh`).
**Done when.** Release builds are GUI-subsystem; WARN+ lines are appended to
`%LOCALAPPDATA%\JukaHub\logs\errors.txt` with the handheld's 1 MiB rotation;
Report a bug and Diagnostics name the folder; Settings → Help → "Open logs
folder".

### DESK-2 — Sharp on high-DPI screens (P1, S) [WIN]
**Why.** No DPI manifest, no `SDL_HINT_WINDOWS_DPI_AWARENESS`, no
`WINDOW_ALLOW_HIGHDPI`: Windows bitmap-stretches the window above 100%.
**Done when.** Per-monitor-v2 awareness (manifest in `JukaHub.rc` +
hint), rendering at native pixels with the logical canvas scaled by SDL; text
is crisp at 150% and 200%.

### DESK-3 — Windows that remember (P2, S) [WIN]
**Done when.** Size, position, monitor and maximised state persist; a
borderless-window mode exists; the monitor can be chosen (today always display
0, `main.go:388`); the fullscreen cleanup lands first.

### DESK-4 — An installer and package-manager listings (P2, M) [WIN] [NET]
**Why.** Only portable zips exist; no VERSIONINFO (`JukaHub.rc` has an icon
only); no Start-menu entry or uninstaller.
**Done when.** An Inno Setup (or MSIX) installer, winget and Scoop manifests
generated by the release workflow, VERSIONINFO from the version in
`jukaconfig.json`; the portable zip remains.

### DESK-5 — Code signing (P2, S) [WIN] [OWNER]
**Done when.** Release executables and the installer are Authenticode-signed
(Azure Trusted Signing or SignPath's free OSS programme) so SmartScreen stops
warning.

### DESK-6 — Steam and Big Picture (P3, S) [WIN]
**Done when.** "Add JukaHub to Steam" writes a non-Steam shortcut
(`shortcuts.vdf`) with art from `branding/`; JukaHub behaves under Steam Input
(the virtual X360 pad is the hint vocabulary).

### DESK-7 — Start with Windows (and a desktop login) that works (P2, S) [WIN]
**Why.** The autostart launcher (`autoStartLauncherFor` in `autostart.go`: a
`.cmd` on Windows, a `.desktop` file on Linux) sets no working directory, while
the config path is relative (`main.go`
`LoadXMLConfigWithFallback("jukaconfig.json")`), so "Start with Windows" likely
fails with "Failed to load config"; the `.cmd` also flashes a console.
**Where.** `autostart.go` (`autoStartLauncherFor`), `main.go`, `xmlconfig.go`.
**Done when.** A relative config path is resolved from the executable's folder
(covering every launcher at once); Windows autostart writes a `.lnk` (or a `Run`
registry value) instead of the `.cmd`, with the working directory set and no
console.

### DESK-8 — Discord Rich Presence (P3, S) [WIN]
**Why.** Friends on Discord cannot see what is being played on a PC.
**Done when.** Opt-in presence over Discord's local IPC pipe shows the game or
the screen; no account tokens involved.

### DESK-9 — Open with JukaHub (P3, S) [WIN]
**Done when.** Command-line arguments open a file (media, ROM, PDF) or a scene
(`--scene Settings`); the installer registers "Open with JukaHub" for media and
ROM types.

### DESK-10 — Windows notifications (P3, S) [WIN]
**Done when.** While the window is minimised, download/alarm/update
notifications appear as Windows toasts (AUMID set by the installer).

### DESK-11 — Brightness on Windows, on real machines (P2, S) [WIN]
**Why.** On Windows the header's sun (`brightness_windows.go`) tries DDC/CI
(dxva2) on JukaHub's monitor, then the panel through WMI (one PowerShell CIM
process per read or write; eleven or fewer levels get a 10 % step), then dims
JukaHub's own picture (`brightnessSoftRender`, `brightnessctl.go`); none of it
has run on a Windows machine. The soft dimmer is not drawn on the shutdown card
or the boot animation (`shutdown.go`, `bootos.go` present their own frames), so
a dimmed PC flashes to full brightness there.
**Where.** `brightness_windows.go`, `brightnessctl.go`, `shutdown.go`,
`bootos.go`.
**Done when.** On a laptop (and a ROG Ally / Legion Go class handheld) the card
moves the same level as Windows' slider and follows it while open; on a desktop
with a DDC/CI monitor it moves the monitor's brightness; with neither (a VM,
Remote Desktop) the card says it dims JukaHub only and the dim survives a
restart; no console window flashes; the startup probe does not delay the first
frame; if a PowerShell run per step feels slow, one PowerShell process is kept
for the session (or WMI is called through COM); `shutdown.go` and `bootos.go`
call `brightnessSoftRender` before their `Present`.

### DESK-12 — Emulator downloads on a PC that resume and are checked (P2, S) [WIN]
**Why.** A PC's daily emulator pass restarts a cut-off download from zero
(RetroArch is about 250 MB) and installs `RetroArch.7z`, for which the buildbot
publishes no digest, on the strength of HTTPS alone.
**Where.** `emuautoupdate_desktop.go`, `emuwininstall.go`, `emulators.go`.
**Done when.** A cut-off emulator download resumes where it stopped;
`RetroArch.7z` is checked against a second channel before it is installed.
(Pinning an emulator's version is LIB-17.)

---

### DESK-13 — Install Azahar like the other Windows emulators (P2, S) [WIN] [NET]
**Why.** `emu_azahar.go` finds the player's own Azahar and starts 3DS games in
it with `-f`; its tile opens the releases page because no install rule exists.
Whether `-f` starts full screen in the current release (issue #2548) is
unverified.
**Where.** `emuwininstall.go` (`emuWinSpecs`), `emu_azahar.go`.
**Done when.** An `emuWinSpecs` rule for `azahar-emu/azahar` (assets
`azahar-windows-msvc-<ver>.zip` from 2125, `azahar-<ver>-windows-msvc.zip` up
to 2124.3; not `installer`, `msys2` or `room`; launcher `azahar.exe`) makes
Install, Update and Roll back work like Xenia Canary, and a `Roms\3DS` game
starts full screen from the library.
**Verify.** Record in the HW log.

## Hardware validation queue

Device work is every open `[HW]` heading above. The [hardware validation
log](../docs/HARDWARE_VALIDATION.md) holds the ID inventory and stock Smart Pro
evidence; walk the [release checklist](../docs/RELEASE_CHECKLIST.md) before tagging.

---

## Ideas parking lot

Not tasks yet: each needs a *Why* and a *Done when* before it moves up. Sizes
are rough.

- **Theme from an image** (M): median-cut palette into the 16 theme roles, on
  `themeloader.go`.
- **Seasonal themes by date** (S): the time-of-day hook exists; add a date
  table.
- **Toys** (S–M each): time capsule / sealed notes, secret modes, playable
  credits, virtual pet, retro OS skin pack, arcade kiosk.
- **Save forensics** (M): list + diff two snapshots.
- **Randomizer suite** (L): start with an IPS/BPS patch applier.
- **Speedrun timer/splits** (M): no auto-split (needs memory reads).
- **LAN arcade leaderboard** (S–M) for JukaHub's own games.
- **Bigger projects**: Steam Deck pairing (L), Juka playground (M), fantasy
  console (XL), pixel editor (L).
- **Per-game colour filters** (S, muOS has them): a colour matrix per game on
  top of the accessibility palettes.
- **"Play style" profile** (S): when and what you play, from LIB-13's session
  log.
- **Tabs Home extras** (S each): an unread dot on the tab that holds Chat; a
  per-tab accent tint of the strip's pill; each tab's scroll kept across
  restarts; hold A on the strip to arrange; a crossfade between tabs under
  Reduce motion.
- **Screen Share sound** (L): the stock firmware has no ALSA loopback, and a tap
  in JukaHub's mixer would cover only its own media, never a game.
- **Per-game hakuX settings** (S): once the package's per-title settings layout
  is known, the game options sheet opens it for Xbox games.
- **Event webhooks** (S): opt-in POSTs for game launched / trophy unlocked /
  install finished (bounded queue, no secrets in payloads).
- **Community widgets** (M): a widget for Home's Widgets layout
  (`home_widgetdraw.go`) as a sandboxed JSON descriptor (text or a number from a
  local source, refresh interval) delivered as a package; no code.
- **Ghost Challenges** (M–L, Juka 2026-11-19): record a compact input replay
  of a run in a supported game and send a friend a challenge to beat it (via
  Nearby devices or a Party Mode-style QR link). Start with one deterministic
  core and a fixed save state as the start; replays are inputs per frame, so
  they only hold for the same core version and ROM checksum.
- **Pocket Studio** (L, Juka 2026-11-19): make a tiny playable game with the
  D-pad (a tile map, a sprite, a few rules) and share its cartridge with
  another JukaHub; a single-file cart (like the fantasy consoles' picture
  carts) and the Nearby devices transfer are the natural carriers.
- **Xbox 360 digital games** (S): list STFS packages under `Roms/XBOX360`
  (no extension; `LIVE`, `PIRS` or `CON ` magic), titled from the package
  header, for Xenia Canary.
