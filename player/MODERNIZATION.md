# JukaHub engineering backlog

Open tasks: **296** (audited 2026-11-17).

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
3. Boot styles: Cinematic's crack layer draws on the GE8300 at the UI frame rate
   and buzzes and knocks on impact; Roll is smooth at 30 fps; floor and horizon
   fit the Brick's 4:3 canvas; None goes straight to Home; Reduce motion and
   Battery Saver show the still logo; an app launch plays the short version.
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
    BlueALSA on D-Bus, its streams and the route.
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
5. **Open in WPE Browser** opens the page (does `launch.sh` take the URL as
   `$1`?); **Get WPE Browser** installs the real release (`extractZipSafe`
   refuses symlinks — does the zip hold any?); `free -m` with JukaHub parked is
   recorded.
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
   works as the lock screen; an NX Redux .webp cover in `.media` shows in the
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
**Where.** `ports_store_install.go` (`pstoreHarbourInstall`,
`pstoreHMExports`), `ports_store_images.go` (`pstoreShotBase`),
`ports_store.go` (`pstoreStatsURL`).
**Done when.** On stock TrimUI (PortMaster through TRIMUI_EX), CrossMix and
Knulli: a Ready to Run port installs headless (`store-install.log` shows exit 0
and the launcher appears in `Roms/PORTS` or `Ports`) with its runtime; with
`portsStoreInstaller: portmaster` several ports queue in `autoinstall` and
PortMaster opens once for all of them; screenshots appear for most cards (else
fix the address pattern — for example PortMaster-New's images release);
download counts match portmaster.games; Update all updates a port whose MD5
changed; the cards and the details fit the Brick's 1024×768.
**Verify.** Record in the HW log; file a FIX task for anything that differs.

### PORT-HW-3 — PortMaster opens on a stock Smart Pro (P1, S) [HW] [NET]
**Why.** On 2026-11-18 Juka reported that PortMaster does not open on the stock
Smart Pro. Its TrimUI launcher needs TRIMUI_EX (`ex_config`, Python 3.11, a
real bash); JukaHub now supplies a downloaded Python, Ubuntu's bash on the
private glibc and a patched copy of `launch.sh`, all built from documentation
and search results, not a run.
**Where.** `portmaster_runtime.go`, `portmaster_python.go`, `glibc_runtime.go`
(signed-index install and weekly refresh), `ports.go` (`startPortScript` gate,
`startPortMasterRepair`), `ports_manage.go` (PortMaster's Y options).
**Done when.** On a stock Smart Pro 1.1.x without TRIMUI_EX: the first open
downloads Python and the runtime with the install card, then PortMaster's menu
appears with working buttons and sound; `gamediag/ports/PortMaster.log` names
JukaHub's bash and python3 and, if `launch.sh` reads `ex_config`, the
`.jukahub-launch.sh` copy; PortMaster installs a Ready to Run port and that
port starts from JukaHub; **Y → Why didn't it open?** lists nothing on that
card and the missing pieces on a card with them removed; **Repair PortMaster**
keeps `libs/` and the ports; a CrossMix card is left on its own bash and
Python. Also check that `data/private-glibc/current` names a `runtime-2.39-…`
folder and GreenOvercast still starts through it, and whether PortMaster
2026.09.19+ reports the Smart Pro's analog sticks (NX Redux PR #142).
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
`/proc/cmdline` with `quiet` and no `loglevel=8`. Also confirm plastron's
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
choice; Power tools → memory shows the textures freed by the WPE Browser
hand-off, a game launch (all but the first frame) and critical memory pressure.
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

### ARCH-10 — The dead-code scan passes and runs in `make check` (P3, S)
**Why.** `tests/scripts/deadcode-scan.py` lists 45 functions that only tests
call; five have no comment saying why a test needs them: `savePSHosts`,
`psHandoffScript` (`psremote.go`), `psHelperPick`, `psInstallHelpers`
(`psremote_helpers_install.go`) and `ListThemePresets` (`themes.go`). Neither
`make` nor CI runs the scan, so new ones pile up unseen.
**Where.** Those files and their tests; `Makefile` (`check`).
**Done when.** Each is removed with its tests, wired into production or
documented as a seam; `python3 tests/scripts/deadcode-scan.py player` exits 0
and runs from `make check`.

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
`motion.go` everywhere (one duration scale), all honour reduced motion, and the
Start menu, sheets and toasts share one entrance.

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
(`trophyRANewUnlocksMax`) and unseen WPE Browser page loads stay as documented
limits.

### UX-17 — Two confirmations that still open on the action (P2, S)
**Why.** Confirmations open on their safe answer (`confirmfocus.go`, for the
shared sheet and the recovery card), but two cards bypass it: the Tabs Home's
"Delete collection" page lists Delete first and opens on it, and Remote Play's
console options forget a console or its saved passcode on one A press, with no
question at all.
**Where.** `hometabs_collections_ui.go` (`homeTabsSheetConfirm`,
`homeTabsSheetPage`), `psremote_console_opts.go` (`psActionOptsForget`,
`psActionOptsClearPIN`).
**Done when.** Both ask first, with the question opening on Keep or Cancel.

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
the PowerVR models, chosen from other ports' reports (NextOS ports issue #1,
nx-redux PR #152).
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

### MED-6 — Audio and subtitle tracks (P2, M)
**Why.** Only the first embedded subtitle or a sidecar file plays; there is no
audio-track picker.
**Done when.** The video settings sheet lists audio and subtitle tracks (from
ffprobe), remembers the choice per language, and styles subtitles (size,
background).

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

### MED-10 — Internet radio directory (P3, M) [NET]
**Done when.** Stations from radio-browser.info by country, tag and search,
favourites, buffering state and now-playing metadata.

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

### MED-31 — Kill yt-dlp's whole tree on Windows (P3, S) [WIN]
**Why.** A cancelled subtitle fetch kills the helper's process group on Linux
(`processGroupConfigure` + `screenChildKill`), but on Windows `screenChildKill`
is `Process.Kill`, which stops only the one-file launcher of `yt-dlp.exe`: its
unpacked child keeps running and `%TEMP%\juka-subs-*` folders pile up.
**Where.** `videosubs.go` (`ytdlpSubtitles`), `screengroup_windows.go`.
**Done when.** A Windows-only tree kill (`taskkill /T /F /PID`, hidden window,
falling back to `Process.Kill`) stops every process of a cancelled run and its
temp folder is gone.

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
DualSense) may connect without input, and spruceOS saw stock pairings lost after
a reboot or a radio toggle.
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
**Why.** Switch 2 ships device-to-device transfer; JukaHub has handoff and save
sync but no "move my setup".
**Done when.** Two paired devices (`peerpair.go`) can transfer settings,
themes, collections, playtime, saves and the package list over the LAN in one
flow, with a summary of what moved.

### SYS-10 — Import from the stock launcher and other firmwares (P3, M)
**Done when.** First run offers to import TrimUI MainUI favourites, recents and
playtime, and NextUI/muOS collections where their files are on the card.

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

### SYS-24 — JukaHub OS image on plastron, TortOS's Linux (P3, L) [HW]
**Why.** TortOS 2 boots the Brick to its menu in about 1.9 s because the card is
the whole system; plastron (github.com/tarikbc/plastron, now ZyperOS) builds
U-Boot + TF-A and Allwinner's 4.9.191 A133 kernel from source. Its modules
cannot be JukaHub OS layers on the stock kernel (vermagic/CRCs); the Speed layer
(`trimui/os-layers/speed`) is the stock-kernel part. The image builders exist
but have not been boot-tested.
**Where.** `.github/workflows/plastron-image.yml`, `os/plastron/` (Brick),
`os/smartpro/` (Smart Pro: the card boots the device's own
boot0/U-Boot/kernel/rootfs saved by `dump-board.sh`, with `/jukahub/init` as
process 1; see its README), `docs/JUKAHUB_OS.md`.
**Done when.** The licences of plastron and TortOS allow redistribution (else
stop); a Brick boots the image into JukaHub (timed against TortOS's 1.9 s) with
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
ALSA card 0 (NextUI issue #865), so the codec's controls are not where `amixer`
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

### SEC-15 — The update swap helper survives special characters in the install path (P3, S) [WIN]
**Why.** `patch_swap.go` (~:287) starts the generated `apply-update.bat`
directly; Windows runs it through cmd.exe with Go's argument quoting, which
breaks for an install folder with `&` or parentheses plus spaces.
**Where.** `patch_swap.go`, `winbatch.go`.
**Done when.** The helper starts through `winBatchCommand`, and a test pins its
command line.

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

### SEC-34 — Mail secrets added from the phone join the log redaction at once (P3, S)
**Why.** Logs blank the exact secrets kept in JukaHub's settings
(`logredact.go`), but that list is rebuilt only on a settings load or save, and
its mail half only when mailcal's store is free at that moment (`TryLock`). A
mail or calendar password added from the phone Companion (`mailcal_store.go`,
its own goroutine) joins the list only at the next settings save or start.
**Where.** `logredact.go` (`logStoredSecretsMailRefresh`,
`logStoredSecretsRebuild`), `mailcal_store.go` (`mailcalSettingsUpdate`).
**Done when.** A mail or calendar change publishes a generation
(`atomic.Uint64`) or hands a rebuild to the main thread (`tbxPost`), and the
main thread re-reads the mail half whenever the generation moved.

---

## Docs and project (DOC)

### DOC-4 — Decisions recorded (P3, S)
**Done when.** ADRs exist for the input vocabulary (`inputhints.go`), the
string catalogue (I18N-1), the plugin sandbox (SEC-6) and the overlay approach
(SHELL-2).

### DOC-5 — A player's quick start for the Help pages (P3, S)
**Why.** Settings → About → Quick start shows `docs/QUICK_START.md`, the
developer quick start (build, `make check`, pull requests), so a player who
opens it learns nothing about using JukaHub.
**Where.** A new `docs/GETTING_STARTED.md`; `helpdoc.go`
(`helpQuickStartSpec`, the embed); the copy steps in `Makefile`, `build.bat`
and `trimui-build.sh`.
**Done when.** A short player guide (first start, Home, games, Settings, where
help lives) is built in and opened by Quick start; QUICK_START.md stays the
developer page.

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
mouse mode; the games mouse honours the Mouse mode switch, offers the right
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
forward, Ask Juka, Game switcher, close background apps, sleep, quit), but it
has no brightness or performance-profile row, and nothing JukaHub draws is seen
while a game runs: volume, brightness and low-battery changes are invisible (the
low-battery LED in `ledindicators.go` is the only sign). muOS draws such
overlays through an `LD_PRELOAD` hook on SDL/GLES swap.
**Where.** `gamemenu.go` (`gmItems`), `powerprofile.go`; a small C hook library
next to `androidrt/` (swap-buffers hook, own GLES drawing); `volumebuttons.go`,
`lowbattery.go`.
**Done when.** The in-game menu gains Brightness and Performance profile rows;
an opt-in hook draws a volume, brightness and low-battery indicator into
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
through `beginLaunchPowerProfile`, but `raemu.go` and `brw_wpe.go` still use the
global profile (`beginGamePowerProfile`), and nothing changes by system or on
the charger (SteamOS issue #3034 asks for the same).
**Where.** `powerprofile.go`, `raemu.go`, `brw_wpe.go`, the game options sheet.
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
**Why.** `travellock.go` reads the Smart Pro's FN switch as gpio243 (PH19, the
pin spruceOS reads) and takes its position when the setting is turned on as
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

### REL-5 — Health-checked installs (P2, M)
**Why.** `patch_swap.go` copies files with a journal; a new binary that starts
but cannot draw is rolled back only after three failed starts (`bootguard.go`).
**Where.** `patch_swap.go`, `bootguard.go` (`bootGuardDecide` already counts a
build's boots and marks healthy starts: first frame plus 90 s).
**Done when.** After an update the first boot must reach "first frame + input
handled" within 20 s and write a health mark; otherwise the next boot rolls
back automatically and says so.

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

### REL-15 — Release builds copy the Help pages; filed reports name the channel (P3, S)
**Why.** The Help pages are built in from `player/helpdocs/`, which the
`Makefile`, `build.bat` and `trimui-build.sh` refresh from `docs/` before
`go build`, but the Windows release job, `release.sh` and `package-sdcard.sh`
build with the committed copies, which go stale when `docs/CONTROLS.md`
changes. Bug reports carry the update channel, but the report service drops
fields it does not know, so the issue it files does not show it.
**Where.** `.github/workflows/docker-image.yml` (the Windows release job),
`player/release.sh`, `player/package-sdcard.sh`;
`netlify/functions/bug-report.mjs` (`FACT_LIMITS`, the version fact row).
**Done when.** Every release build copies `docs/QUICK_START.md` and
`docs/CONTROLS.md` into `player/helpdocs/` with `tr -d '\r'` before
`go build`; the service accepts an optional `channel` (16 characters at most)
and the issue's version row reads "… · Beta channel".

---

## Accessibility (A11Y)

### A11Y-1 — Narration of the focused control (P1, L)
**Why.** XAG 106 and every 2026 console ship system text-to-speech; JukaHub
speaks Juka's answers (`tts_*.go`) but never the interface, which for a blind
player is the difference between usable and not.
**Where.** A `narrate(text)` service on the existing voices (`tts_local.go` on
the handheld, `tts_local_windows.go` on Windows, `tts_play.go`), off-thread;
focus changes in `focus.go`, Home, Settings, lists and toasts.
**Done when.** Settings → Accessibility → Narration reads the focused control's
label, value and hint ("Vibration, on, A toggles"); a new focus interrupts the
old speech (XAG 106); speed and voice are settings; narration of the settings
dialog itself works.

### A11Y-8 — Bundle the legible reading font (P3, S)
**Why.** **Legible reading font** resolves Atkinson Hyperlegible from installed
system fonts, but the face is not bundled and its OFL licence text is not in the
repository; a device without it falls back to Inter.
**Where.** `legiblefont.go`, `docs/DEPENDENCIES.md`, the installer asset lists.
**Done when.** Atkinson Hyperlegible ships with its OFL licence, the dependency
doc and installer lists name it, and the setting picks it up without a restart.

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

### LIB-1 — An incremental library index (P1, M)
**Why.** Every open rescans the card synchronously; `mlib.go` already has
an incremental index pattern.
**Done when.** A background indexer keeps a bbolt index of ROM paths, sizes,
mtimes, CRCs (≤ 64 MiB) and metadata; the library opens from it instantly and
refreshes changed folders after a launch, a copy or a manual "Rescan".

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
screenshots, manual, achievements, last played, playtime graph, the save-state
strip and Play / Resume / Options as the first three actions.

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
**Why.** `emubest.go`'s handheld ranking comes from CrossMix, KNULLI and
spruceOS notes and search snippets; standalone Flycast ahead of its core, the
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
those cores. **Alternative to measure first (Juka, 2026-11-18: "make sure cores
get updated to latest versions").** Start RetroArch itself through Ubuntu's
glibc 2.39 loader, the way GreenOvercast and Linux packages run
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

### APP-3 — A terminal with a real TTY (P3, M)
**Why.** `terminal_other.go` runs one `sh -c` per line without a pseudo-terminal,
so `vim`, `htop`, `ssh` and anything interactive fail.
**Done when.** Linux uses a PTY (creack/pty) with a minimal VT100 parser; Windows
uses ConPTY; the on-screen keyboard gains Ctrl/Esc/Tab/arrow keys.

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

### APP-16 — More from WPE Browser (P2, M) [HW]
**Why.** JukaHub hands pages to [WPE
Browser](https://github.com/matweew/wpe-tsp), installs it (`brw_wpe_install.go`,
launches ordered against its swap by `brwWpeGate`) and uses its ad filter; WPE's
video and YouTube paths are faster than JukaHub's own.
**Where.** `videoplayer.go`, `browser_youtube.go`, `browser_engines.go` (home
page choice), `browserzoom.go`, `installbar.go`.
**Done when.** Videos play through mpv-tsp (A133P hardware H.264) when installed
(the plan is [docs/VIDEO_VPU_OFFLOAD.md](../docs/VIDEO_VPU_OFFLOAD.md)); YouTube
streams resolve through WPE's RustyPipe `ytp` when present (≈2 s and ≈28 MB
instead of ≈9 s and ≈330 MB with yt-dlp, per its release notes); the home page
choice has "Last page"; zoom is remembered per site; the install bar's "Ready"
card stops saying "restart JukaHub" after an app install.
**Verify.** Unit tests; HW-6 step 5 for the device parts.

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
`wine_display_linux.go`, `vcursor_bridge_linux.go`, `docs/WINDOWS_APPS.md`.
**Done when.** A small 32-bit GDI program in `data/windows-apps` (an old Notepad
clone, WinMine) opens a window with a visible pointer; the sticks, A, the D-pad,
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
- **Xbox 360 digital games** (S): list STFS packages under `Roms/XBOX360`
  (no extension; `LIVE`, `PIRS` or `CON ` magic), titled from the package
  header, for Xenia Canary.
