# Changelog

Notable changes to JukaHub. The format is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow the
`Version` field in [`player/jukaconfig.json`](player/jukaconfig.json), which is
what the About screen and the updater read.

[GitHub releases](https://github.com/jukaLang/JukaHubV2/releases) are the record.

## [Unreleased]

### Added

- Opt-in event webhooks: with `eventWebhooks: true` and an HTTPS `eventWebhookURL` in `jukauser.json`, a game launch POSTs a small JSON event (`event`, `name`, `at`) through a bounded queue that drops the oldest when the receiver is down. No config, keys or device identifiers are sent, and a slow or offline endpoint never holds up play.

- `JukaHub --add-steam-shortcut [account id]` adds JukaHub to the signed-in Steam library without opening a window (idea: Add JukaHub to Steam). The binary KeyValues file is appended to rather than rewritten, so existing shortcuts keep their exact bytes and value types; tests check the entry and the append.

- Podcasts now work offline (MED-8). In the Podcasts panel, **D** queues the
  selected episode through the existing Downloads queue (a direct enclosure
  URL goes to yt-dlp like any other audio link), and the player uses the
  completed row's file instead of the stream; **O** writes the subscriptions to
  `podcasts.opml` in the data folder and **I** imports one, accepting nested
  folders, collapsing duplicates and refusing non-HTTP feeds. Podcasting 2.0
  chapter sidecars are read from `<podcast:chapters>` and loaded lazily when an
  episode plays, into the same chapter cache the player's chapter rows read
  (including a podcast episode in a multi-episode queue); a played-to-the-end
  downloaded episode removes its file and finished Downloads row. Backend:
  `podcast_offline.go`, `podcastEpisode.ChaptersURL`/`podcastEpisodeTracks` in
  `podcasts.go`, the podcast fields and end hook in `musicplayer.go`, and the
  podcast case in `musicChapters`/`musicPlayChapter` in
  `audiobook_chapters.go`; `podcast_offline_test.go` uses synthetic OPML and
  chapter JSON, an in-memory download runner writing a temporary file, and the
  real end-of-playback hook. The tests were run red against a temporary no-op
  overlay (seven failures) and green against the implementation. Not checked: a
  real yt-dlp/network transfer, a live chapters HTTP fetch, or the panel keys
  and chapter rows on a running screen.

- Music can crossfade and normalise loudness (MED-7): **Crossfade** in Audio settings is off at 0 and overlaps 0–10 seconds of the current track's tail with the next one, using an equal-power S16LE mixer; the next track's head is pre-decoded so its decoder starts at the mixed offset instead of replaying it. **ReplayGain** reads ffprobe's track gain tags (album gain when a track has none), applies the linear gain to the normal PCM path and limits it at the tagged peak; missing tags leave the track unchanged. Both gates are explicitly off in bit-perfect mode. Backend: `musicmix.go`, the normal decode loop and next-track offset in `musicplayer.go`, and the two rows in `musicsettings.go`; `musicmix_test.go` uses synthetic stereo buffers to check the equal-power envelope, sample-accurate mix boundaries, smooth join, overlap length, clipping, tag parsing, album fallback, missing tags, peak limiting, sample application and the bit-perfect gate. The tests were run red against a temporary identity-gain/stub overlay and green against the implementation. Not checked: how a real crossfade sounds on a device, or two real tagged tracks through a DAC.

- A **Seasonal palette** option under Settings → Display & theme: with the time-of-day schedule on, an unchosen day theme follows the date (lavender in winter, sakura in spring, sand in summer, sunset in autumn). A day theme picked by hand still wins, and turning the option off restores the previous default.

- The Box64 source record is exact and checked (TEST-18): the comment above `BOX64_COMMIT` now names its full commit (`2f130fa…`) alongside the AUR `box64 0.4.4-1` PKGBUILD and SHA-256 it was verified against; `TestBox64SourceRecord` requires the tag to be `v<version>`, the commit to be full 40-hex, and the comment to carry that commit, the AUR package/version, the v0.4.4 tarball name and a 64-hex digest. Verified this pass: the AUR digest `99c6de4…` matches the downloaded v0.4.4 tarball, and its regular files match a checkout of the pinned commit (the Git symlinks under `x64lib/` are the only archive members the Windows extraction could not create). Not checked: a full helper build.

- Two ideas from the parking lot are now features: `JukaHub --scene <name> [file]` starts on a named scene and opens a passed file or URL once the UI is up (pure argument parsing and tests in `openwith_args.go`), and the playtime HTML export names a play style ("Night owl", "Explorer", "One-game completionist", "Marathon player", "All-rounder") with the numbers behind it, from the sessions already kept for Wrapped.

- The Windows release archive now carries the game-audio plugin (SND-11): the `build-and-publish-windows` job runs `player/spatialfx/stage-windows.sh`, which builds `jukahub-spatial.dll` with `TARGET=windows`, runs `build.sh`'s architecture, import and export checks, and puts it at `lib/jukahub-spatial.dll` beside `JukaHub.exe` before `JukaHub-Windows.zip` is made — the path `defaultSpatialPluginLocate` already looks in. Backend: the new staging script, one workflow step, and `TestWindowsReleaseStagesTheGameAudioPlugin`, which checks the workflow wiring and builds and stages a real DLL, asserting its PE header and both exported filter symbols. Not checked: a RetroArch game playing through the filter on Windows (no Windows RetroArch or game run).

- Every icon draws its outlines at one weight (UX-4): the three glyphs that still overrode the base stroke — `juka`, `emu-xbox` and `emu-pce` — now use it, and the Packages box lost its solid top face, so it is an outline like the other object glyphs. Dots and knockout gaps keep their deliberate widths. Backend: `icondefs.go`; `iconstroke_test.go` parses all 415 definitions and fails on any non-dot, non-knockout op whose width is not the base 1, and on a filled Packages op. The atlas is built from these definitions at runtime, so it re-bakes on the next run. Not checked: the glyphs' appearance at 32 px (the rule is structural; no screen was reviewed).

- Every scene can be dumped as stills (TEST-1): `JukaHub --scene-dump <dir>` starts one child per canvas size (1280×720 and 960×720), walks every configured scene — 106 pages in the shipped config, Browser included — writes `<dir>/<size>/<scene>.png`, and quits. The walk switches scenes instantly instead of fading, keeps dirty-frame presentation awake so it does not park at 1 Hz, and skips Exit because opening it quits. Screenshots now encode with `BestSpeed`; the default compression spent most of a second on each 1280×720 frame. Backend: `screenshotmode.go` (`--scene-dump`, `--canvas`, `shotDumpSteps`, `shotDumpEnter`), `main.go` (the pre-SDL command and canvas override), `companion_screen.go`; `scenedump_test.go` pins the option parsing and that every scene, both sizes and unique names are planned; `docs/DEVELOPMENT.md` documents the command. Checked: a headless run (`SDL_VIDEODRIVER=dummy`) wrote 212 PNGs, 106 per size, every one at its size's dimensions with the same scene set, in 62 s. Not checked: the pictures' contents were not reviewed one by one.

- The first pure-leaf packages moved out of `package main` (ARCH-1): `internal/retry` owns bounded HTTP retries and `internal/textutil` owns the byte-preserving lowercase helper, with thin aliases so callers and tests are unchanged. The remaining "leaves" hold shared mutable state, so their split belongs with the ARCH-3/5 refactors rather than a mechanical move; the task is retired in favour of those.

- yt-dlp gets the JavaScript runtime its full format list needs (MED-19): on Windows, JukaHub installs QuickJS-NG v0.18.0 next to its own yt-dlp, pinned by asset name, exact size and SHA-256 taken from the release (`qjs-windows-x86_64.exe` for amd64, `qjs-windows-x86.exe` for x86). The download uses the helper tools' resumable transfer and is checked before the swap; a wrong size or hash never replaces a working runtime, and the old copy is moved aside first and restored if the rename fails. It runs best-effort after the first yt-dlp install and from a new Patch action, which also offers an update for an older or unverified copy; the yt-dlp row and the Component versions line report the runtime's `--version`. With a runtime beside the helper, `streamAttempts` asks yt-dlp's default clients first (they serve the best muxed formats) and keeps android as the fallback; without one the android-first order is unchanged. Backend: `ytdlp_jsruntime.go`, `ytDlpJSRuntimePath` in `ytdownload.go`, `streaming.go`, the Patch action in `patch.go`/`patch_engine.go` and the first-install hook in `core.go`; `ytdlp_jsruntime_test.go` pins the release, proves a wrong SHA-256 and a wrong size are refused without replacing the good copy, parses the version and checks the attempt order both ways. Not checked: a real YouTube extraction with the runtime, and the Patch row was not clicked through (the pinned release asset itself was downloaded by hand and its SHA-256 and `--version` matched).

- Every Go-side extractor now writes through `os.Root` (SEC-1): Piper's tarball, emulator core downloads and their provenance file, staged sync files, restored backups, staged PortMaster files and AndroidPak native libraries are confined to their destination folder, so a symlink planted inside a writable directory cannot redirect a verified file outside it. The 7-Zip-driven path is documented as the external-process exception; PPSSPP and relpack already went through `emuExtractZip`.

- Button sentences now follow the hints' vocabulary (INP-2): every user-facing sentence that names A or B goes through `hintSentence`, so a keyboard reads "Press Enter" where the pad reads "Press A" and the words follow an Accessibility remap. The sweep covers confirm and retry toasts, settings details, empty states and help cards across the apps, Music, Files, Tools, Network, Windows Apps and the boot-lock screens. Backend: `hintsentences_test.go` walks every non-test source file and fails on an unguarded sentence that names A, B, Start or Select, with the physical exceptions listed (chords, the hotseat join prompt, one-handed help, the pad-only remap and controller-test words, and presses read before JukaHub starts); the sweep touches 40 files. Not checked: the drawn keyboard wording on a screen-by-screen pass (the checker asserts the code path, not the pixels).

- Windows pads are recognised before the first controller is opened (INP-9): the pinned community `gamecontrollerdb.txt` (SHA-256 `70215ee7...`) is embedded and loaded at boot, the HIDAPI hints for DualSense, DualShock 4, Switch Pro and 8BitDo are set before `SDL_Init`, and the Controller test has a **Map it** flow that polls the raw joystick, records the twelve menu buttons and appends the mapping to `data/gamecontrollerdb-user.txt`, loaded after the embedded table on the next boot. Backend: `controllerdb.go`, the boot wiring in `main.go`, the recording state in `ctrldiag.go`; `controllerdb_test.go` checks the pin, the mapping line, refusal of an incomplete/duplicate recording, the user file and the recording state machine. Not checked: a real DualSense/DS4/Switch Pro/8BitDo on Windows, and the panel was not clicked through.
- Package main's parsers now have fuzz targets for M3U streams and channels, CUE/GDI sheets, PDF and EPUB text, tar member extraction, xpack manifests, `/proc/bus/input/devices` and the XML config (TEST-8); the weekly job builds the CGO test binary once, runs each target for 30 s and keeps crashing inputs as seeds. Backend: `parsers_fuzz_test.go`, the `fuzz-player` job in `.github/workflows/weekly.yml`. Not checked: the new weekly job has not run in Actions; locally each target also ran 2 s under the mutator, not the weekly 30 s.
- Browser and Chat already release their scene-specific caches when they are left (PERF-11, verified housekeeping): `browserReleaseMemory` and `chatSceneReleaseMemory` run from `completeSceneTransition`, the shared text and shape caches stay. No code change. Not checked: the `JUKAHUB_TEXSTATS=1` drop on a live session.

- Netplay games can be shared as a `jukahub://netplay` link or QR from the panel (GAME-10); the phone companion accepts the link and joins it, a locked room asks for its password on the masked keyboard and passes `netplay_password` in a one-launch `--appendconfig`, and plain RetroArch hosts answering their LAN discovery on UDP 55435 are parsed and listed beside JukaHub rooms. Backend: `netplay_invite.go`, the discovery scan in `netplay_rooms.go`, the panel and vkeyboard wiring in `netplay_ui.go`/`vkeyboard.go`, and the companion event in `companion.go`/`companionui.go`; tests in `netplay_invite_test.go` cover an invite round trip, a crafted RANS announcement, a locked room and the password overlay. Not checked: two real devices, a phone forwarding the link, or a PC running plain RetroArch.

- Groq's voice WAV now streams into the feeder as it arrives, so a first word no longer waits for the whole response (SND-14); the voice-volume slider, the first-word breakdown log and the 400/404 model memory were already in the tree. Backend: `ttsDecodeWAVStream` in `tts_wav.go`, `ttsGroqStream`/`ttsGroqOpen` in `tts_groq.go`, and the streaming sink in `tts_play.go`; `tts_stream_test.go` proves a PCM block reaches the sink before the response ends. Not checked: a real Groq key/voice; Groq's speech model is English-only, so the second-language clause has no voice for the keyboard's German/French/Russian/Greek layouts.

- Solitaire now plays FreeCell as well as Klondike (GAME-23): the Game row in the Start menu switches variants, deals use the published Microsoft numbered generator (deal 1 and 617 are verified card for card), eight face-up columns with four cells and four foundations honour the supermove limit, smart moves use a cell only as a last resort, and statistics are kept per variant in a version-2 save (version-1 files read as Klondike). Tests cover the deals, the move cap, smart-move order, auto-finish and save migration. Not checked: the eight-column layout and cell art on a real handheld.

- Sync can keep encrypted, versioned save snapshots on WebDAV (SYS-19): **Save a snapshot** writes one sealed version and leaves earlier ones, **Restore a snapshot** lists the newest versions, downloads the chosen one and opens the import preview with the snapshot's versions winning, and a wrong passphrase or a damaged file changes nothing. Backend: `webdav_snapshots.go` (`webdavSnapshotUpload`, `webdavSnapshotList`, `webdavSnapshotFetch`, `webdavSnapshotPreview`, `webdavSnapshotRestore`) and the rows in `ssyncui.go`; tests in `webdav_snapshots_test.go` cover upload/list/fetch/restore, an interrupted upload keeping the earlier version, and names that cannot leave the folder. Not checked: a real WebDAV server (the test runs one locally).

- A profile can sign in to its own RetroAchievements account (PROF-2): Settings → RetroAchievements → Sign in for games keeps the login in the profile's own sealed file and each of its RetroArch launches carries it (achievements stay off when it has none); the managed PPSSPP gets `JUKAHUB_PPSSPP_HOME` inside the profile, so its memstick and states do not mix with the owner's, and ports now show the shared-saves notice once. Backend: `profiles_ra.go`, `ppssppProfileHomeEnv` in `ppsspp_resume.go`, the launcher fallback in `ppsspp_managed.go`, the sign-in branch in `raach_signin.go`, and the gate in `ports_library.go`; tests in `profiles_extra_test.go` check the sealed account, the launch values and the profile's PPSSPP home. Not checked: a real RetroArch or PPSSPP launch, or the ports notice on a handheld.

- The builder's Inspector has a **D-pad order** tab: buttons move focusable elements up or down, the order is stored on the elements, exported as each scene's `focusOrder`, and applied by the player's focus graph (older scenes keep the old Y/X order). XML export carries the list too (WEB-3).
- A Community message that is only a picture offers Ask Juka, and Juka gets the picture itself: a Discord link is downloaded to a real file first, and the picture viewer has an Ask Juka chip that sends the picture on screen for both local files and chat images (SYS-21). A video's transcript is uploaded as FLAC, or Opus, when the firmware's ffmpeg has the encoder, falling back to the WAV; the multipart header names the compressed file. Backend: `juka_pictures.go` (`chatAskJukaAboutMessage`, `jukaPictureFromURL`, `imgvAskJuka`), the action in `chatactions.go`, the chip in `imageviewer.go`, and `visionPickSpeechEncoder`/`visionCompressSpeech`/`visionSpeechMIME` in `vision_video.go`; tests in `juka_pictures_test.go` cover a picture-only action, the downloaded bytes/type/name, the encoder choice and the upload MIME. Not checked: a real Discord attachment, the viewer chip on a handheld, or an ffmpeg with FLAC/Opus.

- Download looks can be cancelled by pressing it again while it runs (GAME-6): the row says "Cancelling…", the transfer and the overlay pass stop at the context, and a cancelled run says so instead of reporting a network failure. The Overlay row offers libretro's buildbot `overlays.zip`, with SkyWalker541's Brick pack, when no overlay is found. Backend: `looksDL.cancel`, `looksDownloadCancel`, the context through `looksDownload` and `looksFetchOverlays`'s fallback list, and the download stop in `raTuneOverlayOptions`; tests in `looks_download_test.go` cover the cancel seam, a cancelled transfer and the fallback URL. Not checked: a real buildbot download or a RetroArch launch on a handheld.

- A page's file input opens Files to pick a bounded local file (APP-8): the
  chosen path stays on the control, and sending the form puts the file in the
  multipart body with its base name beside the other fields and the page's
  cookies; a form that is not a multipart POST says so instead of silently
  dropping the file. Backend: the `file` control kind and its `Display` in
  `webpage.go`, the Files pick mode in `webfile_pick.go`, and
  `browserFormNavigationFiles` plus the submit path in `browser.go`; test
  `TestBrowserMultipartFormSendsAFile` serves a local form and upload endpoint
  and checks the filename, bytes, other fields, a cookie sent and one set by
  the upload response. Not checked: a real site's upload endpoint or a file
  picked on a handheld.

- A radio station's Now playing row says "Buffering" until its decoder has
  queued its first sample and shows the station's own ICY `StreamTitle` while
  it plays (MED-10): the Radio directory and the saved stations share that row,
  so both get it; a station without the `icy-metaint` header keeps its name.
  Backend: `radiometa.go` (`radioICYPoll`, `radioICYWatch`), the buffering and
  title fields in `musicNowState`/`musicNow` (`music_transport.go`), and the row
  in `radiodir_ui.go`; tests in `radiometa_test.go` cover a metadata block, a
  stream without the header, the buffering/playing row and the player state
  flipping at the first PCM. Not checked: a real station's ICY stream on a
  handheld (the test serves the metadata from a local server).

- Files unpacks a .7z itself when the archive's coders are ones the reader
  knows (APP-15): Copy, LZMA and LZMA2 with the x86 BCJ, ARM, ARM-Thumb and
  ARM64 branch filters are decoded in-process through the same staging folder,
  `os.Root` writes, space budget, cancellation and CRC checks as the other
  formats; BCJ2, PPMd, AES and unknown coders still fall back to the installed
  7-Zip after the staging folder is cleared. Backend: the stateful x86 BCJ
  inverse filter in `sevenzip.go`, `feExtract7z` in `files_extract_7z.go`, and
  the in-process-then-fallback switch in `files_extract.go`; tests in
  `sevenzip_test.go` decode the 7-Zip-made BCJ `Tools/flash.exe`, and
  `files_extract_test.go` unpacks the fixture end to end and checks every
  file's size and CRC. Not checked: a real 7z with a BCJ2/PPMd/AES coder on a
  handheld (the existing 7-Zip path handles it).

- Button hints name the pad in hand in its own family (INP-3): Settings →
  Controls → Button hints gains "Pad style: Auto / Xbox / PlayStation /
  Nintendo", and pad badges are renamed to ✕ ○ □ △ or Nintendo's swapped
  B/A-Y/X with L/R and ZL/ZR (chords and "Hold …" included), while the
  keyboard vocabulary is untouched; Auto reads the connected controller's SDL
  name. Backend: `hintPadFamily*` and `hintPadStyle*` in `inputhints.go`, the
  row in `inmap_settings.go`; tests in `inputhints_padstyle_test.go` cover the
  three families' badges, the name mapping, the setting and `hintKeyIn` after a
  style change. Not checked: SDL's controller type on hardware (go-sdl2 v0.4.40
  exposes no `GameControllerGetType` wrapper, so Auto maps the controller's
  name), or a real DualSense/Switch pad.

- A RetroArch game's buttons can be remapped one at a time (GAME-4): the game
  options sheet shows a row for each RetroPad button (A, B, X, Y, L/R, L2/R2,
  L3/R3, Select, Start, D-pad) that cycles through every RetroPad button and
  None; the game's own map is stored beside its Buttons/Turbo choices and laid
  over the preset and control layout when the one-launch remap file is
  written, so the existing cleanup and the "never shadow a user's remap" rule
  still hold, and standalone emulators are unchanged. Backend: `gameButtonsMap*`
  in `racontrols_map.go`, the `map` field and merge in `racontrols.go`, the
  rows in `game_options_ui.go`; tests in `racontrols_map_test.go` cover the
  merge and its remap file, the target ladder, the row names and a map-only
  record surviving a reload. Not checked: a real RetroArch launch reading the
  file on a handheld.

- Fuzz seeds for the download resume `Content-Range` parser and the persisted window geometry, so malformed server headers or a hand-edited `jukauser.json` cannot panic startup.

- The FTP server speaks FTPS (NET-15): `AUTH TLS` upgrades the control
  connection before sign-in and `PROT P` puts the passive data channels behind
  TLS, with one self-signed certificate kept in the app data folder so the
  fingerprint a client pinned survives restarts; the Network tools sheet shows
  its SHA-256 and recommends FTPS or SFTP. Backend: `ftp_tls.go`
  (`ftpLoadOrCreateCert`, `ftpCertFingerprint`, `authTLS`), the `AUTH`/`PBSZ`/
  `PROT` commands and TLS data path in `ftp_server.go`, the sheet in
  `nettoolsscreen.go`; tests in `ftp_tls_test.go` cover the upgraded control
  connection with a certificate that matches the sheet's fingerprint, a TLS
  upload and download, `PBSZ`/`PROT` refused before `AUTH TLS` and after
  sign-in, and the certificate's persistence, validity and replacement. Not
  checked: a desktop FTP client (FileZilla and friends) against the handheld,
  or TLS on a real device.

- A split APK's pieces count as one app (AND-18): the store folds
  `split_config.*.apk` files whose manifest names the same package and version
  into the base's row and judges it by the libraries in those pieces, and
  AndroidPak stages the matching split's native libraries with the base's
  manifest and assets. Backend: `androidPakScanZip`, `androidPakSplitPeers`,
  `inspectAndroidPak` and the split-aware staging and memory footprint in
  `androidpak.go`, `apkStoreGroupSplits` and `SplitCount` in
  `apkstore_scan.go`; tests in `androidpak_splits_test.go` cover a base whose
  library lives in its split (inspection and the staged bytes), a split of
  another version being ignored, and the store folding the pieces into one
  addable row while an orphaned piece keeps its own. Not checked: a real App
  Bundle install on a handheld, a split inside an `.apks`/`.xapk` bundle, or a
  split whose manifest omits the version code.

- The Live Builder proves each request with a signature, never the pairing code
  (SEC-19): the browser signs the method, path, body digest, timestamp and a
  one-time nonce with an HMAC-SHA256 keyed by the code; the device refuses a
  replay, an old signature, and a request carrying only the old code header.
  The signature and nonce headers are the only credential on the wire, and the
  builder keeps the code in its own fields. Backend: the WebCrypto signer in
  `builder-device.js`, `liveBuilderRequestMAC`, `checkAuth` and the bounded
  nonce table in `livebuilder.go`; tests in `livebuilder_hmac_test.go` cover a
  signed request with no code header, a replay and a stale timestamp. Not
  checked: a real browser-to-handheld push, a packet capture, or a phone.

- Voice command understands Spanish and asks Whisper for the keyboard's
  language (INP-18): the folded Spanish verbs, volume, mute, time, battery,
  screenshot, Home, Back, music and close-all phrases are translated onto the
  existing English grammar, so English remains the fallback; the transcription
  request now sends `language: "es"` when the current keyboard layout is
  Spanish. Backend: `voiceSpanishEnglish`, `voiceCommandLanguage` in
  `voicecmd_intent.go` and the language field in `voicecmd.go`; tests in
  `voicecmd_lang_test.go` cover the Spanish intents and the multipart language
  field. Not checked: a microphone recording, a live Groq transcription, or a
  Spanish hardware keyboard.

- Notes sync between paired devices and checkboxes render in the reader
  (APP-7): the store now lives in `notes/notes.json` under the data folder,
  which `saveRootsFor` offers to Save Sync as the **JukaHub notes** root; the
  old store beside the app is copied there once and left in place. Task-list
  lines (`- [ ]`, `- [x]`, `* [X]`) draw as `☐`/`☑` glyphs in the reader while
  keeping the same rune width, so the caret still points at the body. Backend:
  `notesDataDir`, `notesStorePath`, `notesMigrateLegacy` and
  `notesCheckboxShown` (notes.go), the root in `savemanager.go`; tests in
  `notes_sync_test.go` cover the glyphs, the store path, the save root and the
  `notes.json` entry in an `ssync` manifest. Not checked: two paired handhelds
  exchanging a real note, or a phone edit after a sync.

- Back returns to the row the player was looking at (SHELL-6): every scene
  remembers `scrollY` and `textBrowserScrollY` when it is left and restores
  them on return, and both the focus and scroll maps are bounded (oldest entry
  evicted first). Backend: `scenememory.go`, the `switchScene` /
  `completeSceneTransition` calls in `core.go`, and `focusPersistMax` in
  `focus.go`; tests in `scenememory_test.go` cover restore, update, eviction and
  the focus bound. Not checked: a real Back press on a handheld.

- A port that stops under BusyBox's sh, because the firmware's Ports launcher
  started it with `/bin/sh`, offers **Try it directly** on its launch card: A
  starts it again under the bash JukaHub supplies and keeps that choice for
  that port alone (`data/ports-direct.txt`), overriding `portLaunchVia` for
  nobody else. The card opens with it focused when the launcher caused the
  error (JH-AN6U7W: OldBits Hub's `export -f`). Backend: `portDirectChosen` /
  `portDirectRemember` (ports_env.go), the button and `launchFailureTryDirect`
  (launch_errors.go), `gameDiagPortOffersDirect` (gamediag_port.go); tests in
  `ports_test.go` cover the per-port store and when the button is offered.
  Not checked: a handheld, or the button on a real screen.

- Standalone emulators save a state when JukaHub closes them for the battery or
  a power off (PWR-12): a library launch through DraStic, Flycast or Mednafen
  turns the emulator's own save-on-exit option on for that launch only, in the
  config its last run read. The user's value is journaled before the edit and
  put back when the game exits or at the next start after a crash; a value the
  player changed during the game is kept. Flycast also gets its auto-load key
  and Mednafen's autosave loads at the next start; DraStic's resume is not
  claimed. Backend: `standalonesave.go`, the format-aware journal in
  `racfgscope.go` and the launch hook in `game_library.go`; tests in
  `standalonesave_test.go` cover the edit, both restores and the kept player
  change. Not checked: a real DraStic, Flycast or Mednafen exit on a handheld.

- PSP SAVEDATA and GameCube GCI saves move with the game in the save handoff
  (LIB-30): the PSP disc ID is read from a PBP, ISO or CISO's PARAM.SFO and
  matches SAVEDATA folders and PPSSPP `.ppst` states; GameCube GCI exports
  match the six-character disc ID, and Dolphin's memory-card folders are save
  roots. Backend: `discids.go`, `savehandoff.go` and `savemanager.go`; tests in
  `discids_test.go` cover the ID readers, the save-name matching and the
  Dolphin root. Not checked: a real PSP or GameCube save handoff between two
  devices.

- Minecraft copies only what the installed port lacks (AND-21): before the
  space check, files already present at the right size and checksum are left
  out, so the port's own ARM libraries and assets are not downloaded over.
  A copy row now says the version's declared size ("about 1.5 GB") before A,
  from the PC's version metadata. Backend: `mcDeclaredSize`,
  `mcPlanKeepOnlyPortLacks` (minecraft_peer.go) and the size on the copy row
  (minecraft_ui.go); tests in `minecraft_copy_test.go` cover the culled plan,
  the declared size in the version list and the row's size. Not checked: a
  copy from a real paired PC into a real PortMaster Minecraft install.

- CPU profiles follow the system and the power source (PWR-6): a launch uses
  the game's own profile first, then its library system's, then the battery or
  charger profile, and finally the global setting; a game's CPU limit still
  wins over all of them. System profiles are set with the custom value
  `powerProfileSystems` (a JSON object or `PSP=performance; GBA=balanced`),
  and the source profiles with `powerProfileOnBattery` / `powerProfileOnCharger`.
  The game options sheet's CPU profile row names the profile in force and where
  it came from ("Auto · Performance (PSP)"). Backend: `powerLaunchSystem`,
  `powerSourceState`, `effectivePowerProfile` and `launchPowerProfileConfig`
  (powerprofile.go), the options row (gameoptions.go, game_options_ui.go);
  tests in `powerprofile_profiles_test.go` cover the precedence, config
  parsing, the row's label and the system profile reaching a launch. Not
  checked: a handheld switching profiles from its real battery/charger state.

- Keyboard settings → Typing → **Wrap around**: Left/Right carry on from the other end of a key row, and Up/Down wrap between the first and last rows. It is off by default, so the old edge behaviour stays; with it off, Up from the first row still moves to the text field.

- Notifications reach the Phone Companion (APP-6): the main page has a
  Notifications card and `/notifications` mirrors the handheld's centre with
  each row's source, priority, age and unread state. A row's action is offered
  only when it is safe from a phone (opening a screen, Downloads, a file, or
  retrying a download); install offers stay on the handheld. The action's
  argument never leaves the device and the request only names the row and the
  kind the page drew. Backend: `companionNotifSnapshot`, the `/notifications`
  page and `/api/notifications` handlers (companion_notifications.go), route
  registration and the card (companion.go); tests in
  `companion_notifications_test.go` cover the list, the safe-action allowlist,
  refusal of an install or a stale kind, main-thread dispatch, the page and
  the PIN requirement. Not checked: the page in a phone browser or an action
  against a live handheld.

- Privacy & data tells everything that uses the network and exports the user's
  files (SEC-11): the Settings section appends a read-only row for each of 24
  network surfaces — updates, emulator and package downloads, artwork, weather,
  the browser, media servers, cloud and device sync, RomM, RetroAchievements,
  the Phone Companion, Remote Play, Steam, Smart Home, the assistant, mail,
  chat, diagnostics, reports, netplay, file servers, VPN and community feeds —
  saying what leaves the device, where it goes and whether it is on. "Export my
  data" writes a zip with every backup category, whole plugin folders and the
  game saves, off the render thread, and reports where it landed. Backend:
  `privacyNetworkFeatures`/`privacyNetworkRows` and `privacyExportData`
  (privacy.go), the Privacy & data rows (setmodel.go); tests in
  `privacy_test.go` cover the inventory, its Settings wiring and the exported
  zip's verified contents. Not checked: the section's rendering on a device
  screen or the completion toast after a real export.

- The music library has Tracks and Folders shelves (MED-15): Tracks lists every
  song in a numeric-aware title order, Folders lists the directories that hold
  songs and opens one into its own track list, and Y (or Search) opens the
  on-screen keyboard to filter title, artist and album. Playing from Tracks or
  a folder plays the visible list, so a search plays only its matches. Backend:
  `musicShelfBuildRows`, `musicShelfTracksAll`, `musicShelfFolders` and
  `musicNaturalLess` (music_shelf.go), `musicShelfPlayRows`; tests in
  `music_shelf_test.go` cover ordering, folders, search and a 2,000-track
  library. Not checked: the seven shelves and the search keyboard on a device
  screen.

- Game Lab setups move between a handheld and a phone (LIB-31): Share writes
  the profile as before and now opens a QR for the companion's Game Lab page,
  where the exported JSON can be downloaded; the page's upload checks the
  64 KB cap and the Game Lab schema, then stores a re-marshalled profile in
  `game-lab/import` for the game's Shared setups list. Backend:
  `gameLabShareQR` (gamelab_share.go), the `/gamelab` page and `/api/game-lab`
  handlers (companion_gamelab.go), route registration (companion.go); tests in
  `companion_gamelab_test.go` cover the QR URL, list and download, traversal
  refusal, upload validation, duplicate naming and the size cap. Not checked:
  scanning the QR or uploading from a phone browser.

- The screenshot gallery steps between games (MED-5): the capture organizer
  hands the viewer one section per game (JukaHub's `game-<title>_<time>` names
  and RetroArch's `<title> - <time>` names, anything else under "Other"), and
  L1/R1 in the viewer move to the previous or next game while L2/R2 keep
  walking the current game's pictures. A folder with no sections keeps L1/R1
  for zoom. Backend: `imgvGameGroup` and `imgvGroupStep` (imageviewer.go),
  `captureOrganizerViewPicture` (captureorganizer_ui.go). Not checked: pad
  input on hardware.

- The tip of the day is a real Home control (SHELL-9): Down from any card in
  the last row reaches it and Up from the power button returns to it; A (or a
  click/tap) opens the CONTROLS.md section it names and X hides it for the day.
  The stop exists only while the line is drawn, and `homeTipHiddenDay` is now
  registered as runtime bookkeeping so the settings reference does not offer
  it. Backend: `HomeFocusTip` and `homeRowAboveTip` (homefocus.go),
  `homeTipVisible`/`homeTipFocusable` (helpdoc.go), the Home actions (home.go).

- Live ranges follow the document in the web DOM (APP-12): every Range is
  registered on its document and its boundaries move on insert, remove,
  `replaceData`, `splitText` and `normalize` as the DOM standard says, with a
  boundary inside a removed subtree moving to the removed node's parent. A
  jsdom comparison in `tests/web/webdom_dom.test.mjs` covers each mutation.

- Recordings are finished media (MED-18): trimming tries a stream copy first
  (H.264 stays H.264, the original quality and speed intact) and falls back to
  a re-encode when the container or codec cannot carry it; the original stays
  until the copy is complete. The organizer's list now shows each clip's
  length beside its size once ffprobe has read it, and Delete asks for a second
  press within six seconds before removing a recording. Backend:
  `captureTrimRun`, `captureDeleteRecording`, `captureRecordingMeta`
  (captureorganizer.go), the sheet's Delete button (captureorganizer_ui.go).
  Not checked: the delete button on hardware.

- Plugins have a versioned starting point (APP-2): `JukaHub --new-plugin
  <name>` writes `plugins/<name>/` with a `schemaVersion` 1 manifest, a POSIX
  sh entry and a README, without overwriting an existing folder; a newer
  manifest schema is listed as a problem instead of being half-read. The
  manifest's permission names are documented, and `examples/hello-plugin/` is
  a complete copyable sample. Backend: `newPluginScaffold` and
  `plugSchemaProblem` (pluginscaffold.go), the loader check (plugins.go),
  `docs/PLUGINS.md`. Not checked: the generated plugin on a handheld.

- The log ring and slog handler live in `internal/logx` (ARCH-2), with the
  app's redaction, warning file and Debug-logging switch wired in from
  `logging.go`; `logDebug`/`logInfo`/`logWarn`/`logError` are aliases to the
  package's entry points and every call site is unchanged. The ring keeps the
  same line and byte caps, append marks and snapshots the Logs screen reads.

- Version history says how each version started here (REL-17): the running
  build's head line and every saved version's row carry the record
  `startcheck.go` and `updrescue.go` keep in `versions/` — "Started fine on
  this Smart Pro" or "Didn't start on this Smart Pro — not offered again" —
  with a date only when the clock was plausible. Backend: `uhistStartNote` and
  `uhistStartLine` (uhist.go), the head line and rows (upch_ui.go).

- The music player's artwork-tile bars follow the real audio spectrum (MED-3):
  `musicUpdateVisualizer` reads the FFT bands of the queued PCM
  (`musicVizBars`/`musicScopeRead`, musicviz.go) and falls back to the drawn
  waves only until audio reaches the device. Not checked: device audio output.

- The pad's Guide button can be the voice orb on Windows (INP-22). Settings →
  Controls → **Guide button: voice** is off until chosen, because Windows Game
  Bar may take Guide first; the row says so, and `docs/CONTROLS.md` explains
  the caveat. On a handheld the existing Menu-button switch is unchanged.

- WestonPack is fetched from the PortMaster-New commit that last changed the
  image, not the moving `main` branch, and its pinned SHA-256 and size are
  checked before the image is mounted or unpacked (APP-19). A changed or
  truncated download is refused with the existing "unverified" message.

- Start with Windows without a console flash (DESK-7): the setting now writes
  `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, value `JukaHub`,
  which starts the GUI executable directly; an older Startup folder
  `JukaHub.cmd` is removed when the setting is changed. The release build is a
  Windows GUI-subsystem executable, so logon opens no console at all.

- Plex (MED-40): a Plex account signs in with the plex.tv/link PIN flow, the
  handheld picks a home-network server connection before a relay, and Media ›
  Movies & TV lists On Deck, Movies and TV shows. An 8-bit H.264/MPEG-4 file up
  to 1080p plays directly; other files use Plex's universal 720p H.264/AAC
  transcode. Playback is reported on Plex's timeline (every 30 seconds and on
  stop) so Continue Watching keeps the place, the transcode is stopped with the
  player, and the token stays encrypted and inside the loopback proxy. Backend:
  plex.go, musicPlexConf (musicsrc.go), /plexv, /plexh and /plexi routes
  (musicproxy.go), Plex rows and cards (media_movies.go), timeline dispatch
  (musicscrobble.go). Not checked: a real Plex server and device playback.

- WebP, second round (MED-38): a local .webp now draws in Chat on the stock
  firmware through the card's ffmpeg (decodeImageRGBA falls back to
  webpDecodeBytes when Go has no decoder), and decodeLibraryArt uses the ffmpeg
  fallback for .webp covers so a converted cover shows without a library
  refresh. Backend: webpDecodeBytes (webpdecode.go), decodeImageRGBA (bounded.go),
  decodeLibraryArt (game_library_ui.go). Not checked: a real TrimUI with ffmpeg
  and the library refresh path on device.

- Audiobook chapters, second round (MED-39): both music layouts now list a
  chaptered file's chapters; Up/Down moves the selection and A, a click or a
  tap starts the chosen chapter; the Audiobooks shelf counts a probed file's
  chapters. Backend: renderMusicChapterRows (audiobook_chapters.go),
  musicPlayChapter/musicChapters, row click and key/pad handling (musicplayer.go),
  renderMusicStockOS (musicskins.go), musicShelfBuildRows/musicShelfRefresh
  (music_shelf.go). Not checked: device rendering of the StockOS chapter list.

- HTML mail through the browser engine (MAIL-4): a message with an HTML part
  opens in the reader through the browser engine (parseWebPage) with remote
  images off by default and one "Show images" press, scripts stripped, links
  opening in the Browser, and the plain-text view one press away. Backend:
  mailBody.HTML/FromHTML (mail_mime.go), mailSafeHTML, mailRenderHTMLReader,
  readHTML/showImages toggles (mail_ui.go). Not checked: rendering on device.

- Per-game environment variables (GAME-13): a game's options carry validated,
  bounded environment variables that the launch merges into RetroArch's and
  standalone emulators' environments; options re-attach on rename/move; an empty
  override changes nothing. Backend: `parseGameEnv`/`validateGameEnv` in
  `gameoptions.go`, launch wiring in `game_library.go`. Not checked: the options
  sheet row (UI not added).

- Package integrity (REL-14): a package counts as installed only while at least
  one of its managed files still exists; a record whose files are all gone shows
  as "files missing" with Reinstall and Forget. Backend: IsPackageInstalled /
  PackageFilesMissing (patch_repo.go); UI: patch.go row + pkg: menu + confirm-forget.
  Not checked: visual rendering of the "files missing" row.

- Calendars that write back (MAIL-2): CalDAV PUT/DELETE of a VEVENT with
  If-Match/If-None-Match (412 conflict → reload) plus Microsoft Graph
  create/update/delete (Calendars.ReadWrite scope, If-Match on update/delete,
  412 conflict). A/D keys in the clock's calendar add on the selected day and
  delete the picked event. Tests: TestCalDAVPutCreateUpdateConflictDelete and
  TestCalDAVReadOnlyRefusesWrites pass against a loopback TLS CalDAV server.
  Not checked: a real CalDAV or Microsoft Graph server; only the loopback
  server was used, and the Graph path is compile-checked + vet-clean only.

- Push mail with IMAP IDLE (MAIL-3): one connection per enabled account waits
  in IDLE while the app is in the foreground, re-armed every 25 minutes, dropped
  when the app sleeps or the screen goes off and restored on wake; a new
  message syncs and notifies within a second. A server that does not advertise
  IDLE, a background app, a sleep or a failing connection all fall back to the
  existing poll loop. Tests: `player/mail_idle_test.go` exercises the real
  `mailIdleSession` path against a loopback TLS IMAP server (handshake, LOGIN,
  EXAMINE, IDLE with `+`, pushed EXISTS, DONE and LOGOUT): notification within
  506 µs of the server's EXISTS, renewal after 200 ms, the no-IDLE fallback,
  and the sleep/wake drop-and-restore with two connections for one account.
  Not checked: a real IMAP server's IDLE; only the loopback server was used.

- Playtime you can trust (LIB-13): `playtime.json` of its own (moved out of
  `jukauser.json`, old key taken across once then deleted), a session log in
  `playtime-sessions.json` keyed by the ROM's content hash, bounded to 5,000
  records and 400 days, Wrapped over 53 weeks, and CSV/HTML export on the
  Playtime screen's Stats tab (X). Tests: `player/playtime_store_test.go`.
  Measured 1.0 ms / 5 allocs per finished session at the log's cap
  (`BenchmarkPlaytimeSessionNote`). Not checked: the drawn heatmap and the
  export toast — no display on this host.
- The handheld no longer offers a core a faster emulator already covers
- Collections that build themselves, and the Games library lists them too
- Dirty-frame presentation: an unchanged screen is no longer drawn
- A port that needs your game files says exactly where they go
- Tests for the three paths the cut left uncovered
- Windows: no console window, and a real error log
- Browser: WOFF2 web fonts, every kind of CSS gradient, and LAN pages' scripts reach the LAN
- Browser: gradients at their real angle and radial gradients; widths in `calc(100% - …)`; scripts see the site's linked stylesheets
- Every emulator JukaHub sets up now updates by itself, and the handheld offers only emulators that run on it
- Browser: pages appear sooner, then complete in place, and the Smart Pro paints them on its GPU
- AmneziaWG 3.0 and 3.1
- The Browser sets pages in their own fonts, icon fonts included
- A launch that fails files its own bug report
- CPU frequency set by hand
- Notes: typing on a handheld, notes as files, and your notes on the phone
- QR maker saves its code as a picture
- Wallpapers for the themes
- Wine test: a Windows program built into JukaHub
- Video memory (VRAM) in Settings
- A Smart Home tab, set up from the handheld or the phone
- Windows notices a network change made outside JukaHub
- Three more architecture decision records
- Azahar installs on a Windows PC like the other emulators
- In-game indicators, and the game's CPU profile from the in-game menu
- Notifications you can act on, kept across restarts, toasts that wait their turn
- Commands in Home search
- Ghost Challenges
- Japanese, Korean and Chinese (Simplified)
- Screen Share: the TV hears the handheld and plays it
- Universal instant resume: any game comes back where it was after a power off or a flat battery
- PSP games resume exactly in JukaHub's own PPSSPP build
- Xbox and PSP games JukaHub closes for are covered at low battery too
- Boot animation: pick several
- Sync: one page and one QR code for every place your saves follow
- VPN: AmneziaWG configurations connect
- Settings › General › Weather location
- Browser: videos, songs and live streams play from web pages
- Media: Movies & TV
- Tools: search, pins and five quick tools
- The browser on the Smart Pro S, and faster on every TrimUI
- Faster game starts
- Library theme: Home as a media library's front room
- Emulators offers only the cores that load on this TrimUI, and JukaHub's glibc is ready before they need it
- RetroArch for a card that has none, on JukaHub's glibc
- Faster menus: the CPU speeds up the moment you press
- The music player has everything UIPod has
- More speed for heavy games
- Silent boot: the bootloader starts Linux quietly
- A quieter, quicker start on the stock firmware
- GameCube, Wii, PS2 and Xbox on every TrimUI model
- CPU boost in games, and a heat guard on every TrimUI
- Emulator speed, per console, and a wider Heavy-game boost
- Pads: a 64-pad sample sequencer
- Windows Apps downloads a program to try Wine with
- Fast forward keeps the speed you pick in RetroArch
- The text editor colours code, and a hex editor opens any file
- The browser draws pages closer to a real browser
- GameCube speed
- Audit toolkit: eight more tools that install on the Smart Pro
- Quick menu, game art, colours, Home views, Device Sync and more
- Quick menu
- "Turning on Wi-Fi…" and "Turning on Bluetooth…"
- Game art settings and the Artwork Manager
- Colours of your own
- Tabs Home: Backdrop and Minimal views, a Tools tab, pinned games
- Home dashboard
- NextUI collections
- Device Sync
- Game Boy link cable and the GBA wireless adapter over Wi-Fi
- Netplay for up to four players
- Audio and subtitle tracks in videos
- Lyrics inside songs and a radio directory
- Pictures, two panes and play records
- Quick settings in games
- Launch scripts and screenshots for scripts
- Safe power off
- Awake on USB, a stock-system check and a Developer page
- Fast-forward speed, light N64 graphics and one button layout
- Library layouts, in-game hotkeys, battery warnings, music, play time and .7z games
- Cover Flow and Cube library views
- Games start sooner
- In-game hotkeys
- Battery warnings you can set
- Menu + volume sets the brightness
- Battery level on the lights
- Tidy the Music folder
- Every audiobook keeps its own listening speed
- Music from inside a game
- Audio channels: Stereo, Mono, Mono left or Mono right
- Play time by game
- Games in .7z archives
- More box art
- Your own overlays
- Left stick as D-pad, per console
- Phone Companion: updates, logs and folders that ask first
- Tidy the card at start
- Servers, sharing, widescreen, discs and look packs
- VNC server: see and control the handheld from any VNC app
- Telnet server
- SMB share: the SD card as a Windows or Mac network drive
- Tailscale built in: reach the handheld from anywhere
- Syncthing beyond saves
- PlayStation analog controller
- Widescreen (16:9)
- CUE generator
- Look packs, console icon packs and console names
- Start on tab and a power-off picture
- Achievement options, hotkeys, game translation, Kid mode, idle and lights
- RetroAchievements in games
- Custom RetroArch hotkeys and aspect ratio, per console and per game
- Translate the game screen
- Kid mode and Kiosk mode
- The FN switch, Silky lights and GPU boost
- Idle in games, idle hooks and the battery in games
- Card check and a WebDAV share
- Automatic collections and two game idle screens
- Offline achievements, USB storage, keys, power settings and five more consoles
- Offline RetroAchievements
- USB storage mode
- In-game menu entries, Start in and Hold Menu
- Brick F1 / F2 keys, the Smart Pro S Home key and D-pad as stick
- Power, battery and lights
- Stock firmware, card space, emulator versions and Tasks
- New consoles and a smarter name sort
- PlayStation 2 on the TrimUI handhelds
- Ports without PortMaster's app
- PortMaster installs and updates itself
- Tested on this device
- Your progress is safe — and JukaHub shows it
- A low battery shortens the sleep
- In-game saves reach the card every 10 seconds
- Updates fix themselves
- Device check
- Covers for the arcade games
- Open with and default apps
- App Center
- Interactive Terminal
- Light-gun mode
- CHIP-8 player and Get CHIP-8 games
- WASM-4 games and Get WASM-4 carts
- Retro soundtrack player
- TV mode
- Game Boy Printer scrapbook
- USB controller
- Controller Mode
- Record the screen
- Open with KOReader
- Send whole folders from the phone
- Move to a new SD card
- Profiles for a shared handheld, and Guest
- Never lose a session in standalone emulators
- Spoken navigation
- Big game cards open at once
- Games on a home network drive
- Voice tab in Chat: a free voice room, one click, nothing to sign up for
- Everyday apps: Tasks, Voice Memos, Paint, Stopwatch, Solitaire and Create ZIP
- Party Mode: Guess the Game
- Game Ready
- More on a game's page
- ScummVM and DOS shelves
- Per-game control layouts
- Game manuals
- Game Lab
- Save handoff
- TIC-80 and RPG Maker in Get Homebrew
- Nintendo 3DS on Windows with Azahar
- GameCube and Wii on the TrimUI handhelds
- Java programs
- J2ME phone games
- Godot games on the handheld, through the TrimUI Smart Pro Godot port
- Xbox boost: the whole handheld for an Xbox game or stream
- hakuX installs itself on the Smart Pro and plays Xbox games from Games
- Python and Perl arrive by themselves for the Audit toolkit
- Legible reading font downloads a readable face by itself
- Memory is freed by itself so the offline AI starts
- Extra memory (swap) on 1 GB handhelds
- RetroArch on JukaHub's glibc 2.39, so the newest cores install and run
- Internet radio on the idle screen when the card has no music
- A look for every console, chosen for you
- Get Homebrew: new games from itch.io
- Game Boy Color games on the Game Boy shelf play in color
- Multi-disc games become one game by themselves, with a Disc row
- More in the ROM manager
- Game buttons and turbo
- Cheats in the in-game menu
- Letter scrubbing in the Games library
- Menu colour
- JukaHub's own booter, and Home from the moment the Smart Pro powers on
- Select + R1 takes a screenshot in a game
- Boot prefetch
- Home's first frame comes ready from the last start
- JukaHub OS shows Home almost at once when it boots
- JukaHub reaches Home sooner, and the log says where the boot went
- Console look: Pixel names and Pixel cards
- Drop games, covers and music from any browser, and each file goes where it belongs
- Browser over USB
- Rename file, Remove cover and Delete game
- Covers from gameplay
- DNS changer
- Linux packages: apt for the TrimUI
- Get more cores
- Google Drive sign-in you can set up from your phone, and WebDAV save sync
- A new core build is on probation until a game runs
- Packages: Run installed apps
- Quick Settings: Tiles, Compact and List layouts, and L2 to edit it
- Every emulator a console has, offered when its game starts
- Touch screens work outside the keyboard, and the Tabs Home takes swipes
- The manual on the device, and a tip a day
- Update channels: Stable and Beta
- JukaHub's PPSSPP signs in to RetroAchievements with your account
- Update the Windows app runtime from its setup page
- Choose a game's cover, and the name its covers are found by
- Chapters inside one audiobook file
- Auto power off after sleep: 15 minutes to 48 hours, NextUI games too
- Factory reset
- Install an update when it suits you
- Listening history in Music
- Daily challenge
- Passwords for encrypted archives
- PC streaming: JukaHub's own Moonlight client, made for Vibepollo
- PC streaming on the Smart Pro: Check this handheld, Auto quality, CPU and Wi-Fi for the stream
- JukaHub's PPSSPP: Update all and Go back
- Xbox Remote Play and Steam in Start → Games
- AndroidRT: the surface probe measures SDL, and GLES comes from beside libEGL
- Settings → Display: the screen in one place
- Hotspot
- Fonts you can download
- Emulator upgrades are easier to spot
- Device release walk
- Goodbye and greetings said in your name
- Voice orb: a size bigger, and "Type instead"
- Ports Store
- Cloud Gaming: Xbox Cloud Gaming (Game Pass) and GeForce NOW
- Steam: more of your games play, in more setups
- Email and calendar
- Pick up and play: sleep that lasts, Syncthing, a USB DAC player, NextUI in place
- USB file transfer
- Emulation settings, cheats, the game switcher, display and lights
- Driver update for all four TrimUI handhelds
- Native PICO-8, a new Home look, full setting descriptions and more covers
- Voice unlock for the startup lock
- USB & OTG tools
- Battery calibration
- Community themes update over the air
- Google Drive save sync, built in
- RomM saves sync by themselves
- PortMaster that behaves like on the firmware
- Juka AI with device tools
- Command Deck: run common commands without typing
- Travel lock: the FN switch holds the power button
- RetroAchievements and RomM from your phone; pairing code in the Start menu
- Useful plugins instead of toys
- Automatic CPU speed
- The Tabs Home (JukaOS v2)
- Games, Favourites and Collections on the Tabs Home
- Boot animation styles
- Bluetooth that behaves like a phone's
- The recommended emulator for each console
- Natural offline voice for TrimUI handhelds
- Hold Select + Start for three seconds to end a stuck game
- JukaHub OS SD-card image for the Smart Pro (experimental, not yet boot-tested)
- JukaHub OS
- JukaHub OS package
- Startup lock
- Close all apps & games
- The stick lights react to Juka
- Xbox One Style
- Switch 2 Style is now the console's home screen
- PS5 Style is now the console's welcome hub; the old look is PS4 Style
- Wi-Fi, Bluetooth and Power tiles you can pin to Home
- Focus bounce on every screen, on the Smart Pro too, with a setting
- Which emulator? when several can play a game
- X1: Xbox Remote Play next to PS4 and PS5
- Realistic glass (Settings → Display & theme → Glass)
- Consoles and Experience tabs in Theme presets
- Simpler Settings
- Juka can see the screen
- Minecraft (Start → Games → Minecraft)
- "Hey Juka" wake word (Smart Pro)
- The voice assistant changes settings
- Spatial audio for every game and app
- The chat's paperclip is selectable
- Start sends from the on-screen keyboard
- Console-style themes
- Stick lights: Smooth effect
- R2 steps the liquid background, on any screen
- Voice orb: "Speed up by providing a Groq key"
- Home's focus highlight bounces between buttons
- The Synth in the Games library, and covers for the built-in games
- Stick lights that react to your voice
- Stick lights have their own page in Settings
- Screenshots flash and show what they caught
- Hold Menu for a screenshot, anywhere — in a game too
- Notifications during videos (Settings → Media), off by default
- Videos that have to be downloaded first can download in the background
- JukaHub builds its own PPSSPP for the Smart Pro and Brick
- Unread chat badge on Home
- Liquid background switch in the Start menu
- Background music
- The boot and the shutdown greet you by name
- Arrange the top bar
- Steam library in Apps
- PS4 / PS5 controllers over Bluetooth in Remote Play
- Save slots in the in-game menu
- Automatic save with a previous copy
- Sleep on the power button, and in the in-game menu
- Game diagnosis
- Video filters for RetroArch games
- CPU profile per game
- Interface language
- Synth: a pocket groovebox
- Ideas from Mosaik CFW (TrimUI Smart Pro / Brick Pro)
- Netplay: rooms on your Wi-Fi and online play
- In-game menu: hold Select + L2 to leave any game
- Paste the Groq key from your phone
- L2 edits Home
- Resize any Home tile, on square cells
- The game assistant is on out of the box, and talks back
- Set the date and time by hand
- Bluetooth in every top bar
- Connect or disconnect a Bluetooth device with one press
- Bluetooth starts itself when the firmware left it stopped
- The system's own Bluetooth settings, one press from JukaHub on a PC
- The APK Store is one press from Packages
- Juka talks
- Teach the voice assistant your own words
- Emulators and their menus by voice
- Ask Juka about the game you're playing
- Juka looks at pictures and videos
- R2 is the voice typing button
- JukaHub shows how much memory it uses
- Quick Settings opens the Bluetooth page
- The Smart Home Tailscale row names the device's address and its peers
- Customized built-in elements work in the web DOM
- A typed console address is listed while it pairs
- Fuzz tests for the updater's decoders
- The D-pad reachability check is a test now
- More console folders in the game library
- Windows programs on the Smart Pro
- WestonPack puts Windows programs on the handheld's screen
- The pad is a mouse and keyboard in Windows programs
- Windows Apps on Windows
- AndroidRT ships with JukaHub
- Android games in the Games Library (AND-16)
- Xbox 360 on Windows
- xemu sets itself up
- Emulator updates keep everything JukaHub installed current
- CI builds AndroidRT for the handheld
- CI builds a Box64 for the handheld
- The static checker ships with the repo
- A broken doc link fails `make check`
- A VPN for the whole device
- Each screen names itself once
- One five-step type scale
- A legible reading font
- Switch access
- A magnifier that follows the focus
- Accessibility is a Settings section
- Adjustable timing and sticky chords
- Audio access and readable captions
- The weekly weather card now brings each forecast to life
- An old video finishing no longer clears a newer play
- Liquid background animates as soon as it is enabled
- A guessed protected value always counts as a change in Live Builder
- A waiting Live Builder design restores secrets as they are at approval time
- A kept value inside a list is restored only into the list it came from
- Held keys act once, except where holding means moving or editing
- yt-dlp search commands are allowlisted
- Bold text uses Inter SemiBold everywhere
- Reduce Transparency and Increase Contrast work on every screen
- A backup carries the handheld's time zone
- Books remember where you left them
- Quiet Mode in Quick Settings
- PS Remote Play gets ready by itself, and the phone can send the PSN Account-ID
- Optional daily game limits and break reminders
- Patch can update yt-dlp on demand
- 285 more trophies — 386 in all
- The phone companion keeps the handheld's screen at the top
- Enter the Groq API key from your phone
- The voice orb: press Menu and talk
- Eight new themed Total conversions
- Six new palettes and six complete Home conversions
- Four new palettes and four complete Home conversions
- Theme authors have a complete file reference
- Spatial audio is one step away in Quick Settings
- PS Remote Play installs its own helpers
- Trophies in the top bar
- Bug reports in one press, no typing
- APK Store: which Android games this handheld might run, in one screen
- Voice command: say what to open
- Spatial audio (virtual surround)
- Spatial audio in RetroArch games
- The backdrop can be water
- Animated wallpapers
- The wallpaper picker, redone
- Screen Share: the Smart Pro's missing HDMI port, in a browser
- Report a bug sends the report from the device
- Save a video, or just its sound, from the player
- Downloaded videos come with their subtitles
- Emulator cores now update by themselves
- A core build that cannot run on this firmware is never installed
- PSP games can run JukaHub's own, current PPSSPP on the handheld, and the Emulators page says which PPSSPP plays them
- Mouse mode: hold Select + L1 for a pointer anywhere in JukaHub
- Mouse in games and apps (TrimUI, opt-in)
- Sign in with PlayStation to pair a PS5 or PS4: no more hunting for the Base64 Account-ID
- Sites the reader can't show open in WPE Browser on the handheld
- The Browser blocks far more ads and trackers, and says how many
- Box art finds itself
- RetroArch has a tile on the handheld's Emulators page, and core rows lead to their games
- Booting into JukaHub looks like an operating system starting
- A JukaHub boot logo for the TrimUI firmware
- A brightness button beside the volume button
- The Browser fills the screen, and Select opens a menu with everything
- Home can be a carousel, or widgets you arrange
- Files extracts archives
- The Games Library opens without walking the card on the render thread
- The library remembers how you like to browse
- Plugin ROM formats reach the library
- Settings search opens the exact row
- The Browser runs page JavaScript against a real document
- Settings → Browser → JavaScript
- Send files in chat
- Light & dark by time of day
- JukaHub starts with your TrimUI
- Plugins do something when you switch them on
- Choose the browser's search engine and home page
- Stick calibration and dead zones
- Virtual cursor on the right stick
- Screenshot mode, for pictures you can publish
- M3U8 playlists go both ways
- Settings → Date & time
- A time-zone picker on the handheld
- The clock sets itself from the network
- JukaHub saves a crash report and offers it at the next start
- The recovery chord opens a recovery card
- Three failed starts in a row start safe mode by themselves
- A healthy start keeps the settings it started with
- Roll back a package
- Save states made with another core build are flagged
- See who is connected to FTP and SFTP
- Android games can present through the firmware's own SDL2
- Hints follow Accessibility's button remaps
- Sentences that name a button speak the hints' words
- A volume button in the top-right corner
- The Home pairing code is a control
- yt-dlp keeps itself up to date
- Wake your PS4 or PS5 from the handheld
- Pair Bluetooth controllers and headphones in JukaHub
- Remote Play uses every controller, including one connected mid-stream
- PS Remote Play maps every DualSense input
- The Remote Play stream has an on-screen display
- A PlayStation login passcode no longer ends the stream
- PS Remote Play from another network
- Stream settings and console options in PS Remote Play
- Remote Play says how a stream ended
- Battery time left
- Host tests for the Remote Play helper
- YouTube links in the browser play in Media
- The power button is back on Home
- Hints name the keys you actually have
- F10 opens the Start menu from the keyboard,
- F1 shows the keys for the screen you are on
- Startup and shutdown chimes
- Xbox on the Smart Pro, through hakuX
- AndroidRT grows a Java side
- A UX review
- Screens that need the internet say so before the press
- A ringing alarm takes over the screen
- Community theme library
- The web gallery reads a shared community index
- Continue now includes podcasts
- Saved playlists for music
- In-game OSD presets
- Box art finds more of your library
- A 12-hour clock, if you want one
- The web builder ships its own fonts
- The design check lints for the handheld panel
- The idle screen can take its own backdrop photo away
- The Benchmark screen's Run button says it is running
- Picture and video thumbnails in Files
- A playback queue: results can be lined up, and each one starts when the last finishes
- Vibration strength: Soft, Normal or Strong
- Box art for ROM names that do not match the reference title
- Files is a file manager, and it moves files between two consoles
- The lock-screen image can be cleared again, and clearing it never touches the photo
- The Wi-Fi diagnostics sheet can be copied, and it is in the support bundle
- Podcasts are a subscription library, not a URL box
- The reader opens EPUB books, and PDF pages are page-accurate
- An LED Style panel for mode, effect, colour and brightness
- Favorites is a library, not a flat list of links
- Files can keep the picture it opens as the lock-screen image
- The Streams button opens a menu that can always start content
- Earned badges show their unlock date
- Playtime has 100 badges on a page that scrolls, earned badges first
- About now shows the device identification evidence, not just the log
- A Wi-Fi diagnostics sheet, for a handheld that cannot connect and has no shell
- Playtime now answers "have I been consistent?" instead of "what did I use?"
- The Wi-Fi picker now works on Windows
- A persistent activity strip now shows background work on every scene
- Frame cost is now measured on the device instead of inferred from a desktop
- The Quality row now says what the stream actually got
- A long chat and browser session is now measured, not assumed
- The scene-navigation memory harness now measures the shipping ceilings for every cache it prints, not just box art
- An emulator update can be rolled back from the Emulators page
- Every texture and byte cache is now bounded in bytes, not just entries
- The Emulators page installs emulators, not just lists them
- The browser can fill in and send a form
- One page header across every screen
- The browser remembers where you were reading
- Text zoom in the browser
- A zoom chip on the browser toolbar
- The named Power Mode is now in the Accessibility panel
- Dropping a file on the window plays it (desktop)
- Podcast feeds survive an offline boot
- A bounded image-decode pool and a general semaphore
- A finger-sized hit area for compact controls
- The web builder stamps `schemaVersion`
- The builder's canvas now draws the shipped design the way the device does
- Every tool screen draws the app's top bar
- The top-bar calendar is a month view you can page through
- `render_bench_test.go` also benches a config-driven page
- The diagnostics bundle carries the cookie session's date and retention window
- Each site's row in the saved-cookie picker carries its own session start
- The Games Library header carries the clip count, the same figure Settings reports
- Pressing an installed package row offers Remove or Reinstall instead of running Install again
- The Settings cookie row says when the saved session was first stored
- The Video snaps row says how many games on the card actually have a clip
- The Browser can forget one site's cookies instead of the whole jar
- The Package Manager list marks what is already on the card
- Clearing the saved browser session is one press from Settings
- Every page resolves D-pad direction the way its arrows were drawn
- Video snaps can be switched off from Settings
- The first-run tour and the controller legend now point at the Toolbox and Security & trust pages
- Three network scanners in the Toolbox, behind a one-time prompt
- Saved browser cookies can expire on their own
- The Settings row for the screensaver timeout now renders its value
- The Browser toolbar now says how many saved cookies are on the card
- The Games Library can play a video snap for the focused game
- The Browser keeps a session across a relaunch
- The Wi-Fi meter in the status corner now tells the truth about a missing link
- The Browser reads more of the CSS real sheets are written in, and draws the parts it read correctly
- The Settings menu is a launcher page, the same treatment the Tools menu got
- Two shared components, and the three screens that were drawing them by hand moved onto them
- CSS gradients in the text-browser are painted as real multi-stop ramps
- A SCROLL / LINKS chip in the web browser's toolbar
- Per-button remapping in the Accessibility panel
- Hold-to-repeat on the D-pad outside the on-screen keyboard
- More of the CSS a real page ships, in the text browser: `line-height` (unitless, percentage and length), `border-radius`, `opacity`, gradients…
- Cookies in the text browser
- A Recordings screen for the captures an emulator writes
- Conditional requests for the shared fetchers: `httpcache.go` keeps the last successful body next to its ETag / Last-Modified (bounded at 64 URLs and…
- One-file support export: Patch → Export diagnostics now writes the redacted report *and* the last few hundred log lines to…
- Architecture decision records: [docs/adr/](docs/adr/) now holds the decisions other code has to live with, with the process itself…
- A documentation link check in the test suite (`player/docs_links_test.go`): every relative Markdown link in the checkout must resolve, so a renamed…
- Component versions in Patch (and in the diagnostics report): a new row lists the running app, launcher script, design config, helper tools and…
- A real button-remapping layer
- LED policies: the LED sync is no longer audio-only
- LAN Play has a switch
- Controller legend ([player/controlslegend.go](player/controlslegend.go)): a first-run card that appears once the setup tour is completed, and stays…
- AndroidPak loader wiring for a real NDK APK's first run
- OpenSL ES shim audio path for AndroidRT: `player/androidrt/src/openslessim.c` now plays — real `Enqueue`/`SetPlayState` streaming of S16 1/2-channel…
- Input shim for AndroidRT: NativeActivity games get a real `AInputQueue` instead of the silent no-op
- Android engine work: AndroidPak launches gained a memory pre-flight, and AndroidRT gained a host-testable probe for the GBM+EGL surface stack…
- Hardware decode investigation doc: docs/VIDEO_VPU_OFFLOAD.md records the desk pass over offloading the video player's decoder to the A133P's Cedar…
- Icon atlas for the shared rendered glyph set: every home card's icon tile, tool/emulator tile and launcher empty-state glyph used to be built from…
- "While you were playing": background work that finishes while a game, port or Wine app owns the screen — saved-track downloads, helper-tool and…
- Config-driven home cards: every menu card on the home screen (Continue, Media, Files, Packages, Apps, Favorites, Browser, Chat, Tools, Emulator…
- Embedded YouTube videos in the reader show the video's real thumbnail on its play card: the embed's video id maps to a `mqdefault.jpg` URL on the…
- Per-video playback quality: the video settings sheet's new Quality row (cycling Auto → 360p → 480p → 720p → 1080p) pins a yt-dlp format ceiling to…
- AndroidPak
- `Makefile` as the single entry point for the developer loop: `make dev`, `build`, `run`, `test`, `test-web`, `e2e`, `vet`, `fmt`, `lint`, `bench`…
- Issue forms for bug reports and feature requests, with the version, device and redacted diagnostics report asked for in the form, plus contact links…
- Pull request template that records the definition of done and separates a quick win from a change that needs a migration
- `.editorconfig` and an optional `.pre-commit-config.yaml` (gofmt, `go vet`, `node --check` on the builder scripts) so formatting noise stops reaching…
- The game power profile now lifts the CPU frequency ceiling, not just the governor: the TrimUI Smart Pro advertises a 2 GHz top OPP while some…
- The browser's toolbar reports how many ad and tracker requests a page load skipped, and a Filter chip turns filtering off for one site when a page…
- Audio
- Per-game launch options in the Games Library
- Backing out of a video no longer stops it
- The now-playing title in the top bar is a button: a click or tap, or A with the controller on the chip, opens what it names — the audio player for a…
- A random game picker in the Games Library
- Browser pages in the reader no longer lose their videos: a resolvable `<video>` element or video-bearing embed (YouTube, Vimeo, Dailymotion, Twitch,
- Scene-scoped background work: leaving a screen now cancels what it started
- `Ctrl+L` opens the log viewer from any scene
- Log exports no longer destroy the previous file: an export moves `logs.txt` to `logs.txt.1` and writes the new one atomically, so exporting after a…
- 720p frame budgets for the home screen: `go test -run '^$' -bench BenchmarkHome` reports a warm frame, a cold-text frame (theme switch) and one D-pad…
- Shorts feed cells and search-result cards in the Media browser now fall back to real artwork when yt-dlp ships none: a YouTube entry without a…
- Tools → Toolbox: install popular command-line tools on demand, and run them from the app
- Tools → Security & trust

- CJK text wraps without breaking kinsoku rules (I18N-CJK-1): `wrapWordsMeasured` /
  `preferTokenCut` in `player/notiftext.go` now keep a CJK run breaking between
  characters while never starting a line with 、。」 and never ending one with 「;
  `cjkBreakLegal` guards both the separator cuts and the default cut. Tests in
  `player/notiftext_test.go` (`TestWrapWordsMeasuredCJK`) cover the wrap case.
  Not checked: the ja/ko/zh-CN first-run tour catalogues and their translations,
  and CJK breaking in `wrapTextLines`/`core.go`.


### Removed

- The upgrade shims are gone (ARCH-7): an older config's Home layout is no longer moved to the current grid when it loads, its retired Vault button and cached Vault variables are no longer stripped, an old Settings section id (`datetime`, `keyboard`) no longer redirects to the section that took its rows, and `AppName` is no longer rewritten from "JukaHub 0.4.0". Backend: `player/home_migrate.go` deleted; `setSectionAlias` removed from the Settings catalogue, with `dateTimeOpenSettings` naming `general`; `removeRetiredVaultElements`, `retiredVaultVar` and `dropRetiredVaultVars` removed with `config_retired_vault_test.go`; the duplicate `AppName` check dropped from `finalizeConfig` and `Validate`. The Date & time and Keyboard fold into General and Controls stays, now as `setFoldedSections`. Not checked: a device carrying a config saved before this change.

### Changed

- The first YouTube extraction attempt now requires a format with sound too: a higher-ranked video-only MP4 can no longer leave a clip silent. The same requirement is asserted by a test for both site and YouTube selectors.
- A cut-off Windows emulator download now resumes from its `.part` with a Range request, keeps the part if the network dies again, and uses one part per release so a nightly tail cannot be appended to a stable archive. Range/Content-Range decisions are pure helpers with tests (DESK-12, partial).
- Windows builds carry a VERSIONINFO resource from `jukaconfig.json`; `packaging/windows/generate.py` renders the same resource plus an Inno Setup script and winget/Scoop manifests from the release's SHA-256, with `make test-packaging` checking the generator (DESK-4, partial).
- Juka's voice has its own volume setting, logs how much of the first word was waiting for Groq versus the device, and remembers a 400/404 missing speech model for the rest of the run instead of retrying it (SND-14, partial).
- Supply chain checks tightened: the weekly govulncheck now fails on reachable findings, a weekly CodeQL workflow covers the Go player and the builder's JavaScript, and the Windows build verifies the three SDL dev tarballs against the same SHA-256 pins CI uses (SEC-10).
- Windows builds declare per-monitor-v2 DPI awareness in an embedded manifest and keep SDL's DPI handling in step. A remembered desktop window now restores its size, position, monitor, maximized and borderless state on the next start, with clamping after a monitor is unplugged or resized; Display gains borderless and monitor rows, and F11 keeps the windowed geometry. Verified with a full `build.bat` build; the visual DPI checks are logged as pending in the hardware validation log (DESK-2, DESK-3).
- The arm64 helper build root is pinned to the dated Ubuntu focal snapshot `20250530T000000Z` with hash-checked pip wheels, and the helpers build their own supported OpenSSL 3.5.9 and miniupnpc statically. chiaki-ng configures fully disconnected, BUILDINFO names the root and each pinned source, and Box64's v0.4.4 tarball digest from the AUR was checked once against the pinned commit (TEST-15..18).
- Bluetooth left off stays off after a sleep or wake. The stored choice is re-read after every resume and on a slow watchdog instead of only in a short start-up window, an explicit Off clears the "on this session" latch, and internal scans no longer remember themselves as an On or write a next-boot On intent. Airplane-mode restore also respects the stored Off.
- A typed short-video site's Play now prefers a stream that carries both picture and sound; the old format ranking could pick a video-only MP4 and leave the clip silent. The whole-file fallback asks for a muxed file too, keeping the joined-stream fallback for sites that have none.
- No built-in text names a particular short-video host: the site screen and docs speak generically, and only a domain you typed appears on its own tile or in a search message.
- Less work on a frame: thumbnail crop sizes are cached with the decoded texture instead of queried through SDL for every card, the browser memoizes containing-block rectangles and closes paint runs with a string builder, the media hub reuses the network state sampled by its tick, and the video overlay reads the frame texture and its size under one lock. Playtime pruning filters its log in place rather than allocating a second 5000-record slice on every append (`BenchmarkPlaytimeSessionNote`: ~391 µs/525 KB → ~53 µs/360 B); smart-folder grouping no longer copies year/player strings or lowercases each genre twice (~980 µs/6224 allocs → ~765 µs/5724 allocs).
- The idle screen and Auto sleep wait out a loading screen: a video being resolved before playback starts, the modal loading card, a scene transition and a video-site search all keep the panel awake. The idle countdown restarts when the wait ends instead of dropping straight onto the screensaver.
- Video links on sites other than YouTube start faster: one neutral yt-dlp call instead of three YouTube player-client attempts, the first few site results warmed in the background, and downloads try the muxed file first. Site searches reuse their token and fetch posters in parallel.
- The game audio plugin's RetroArch and LADSPA interfaces are checked field by field against the upstream headers, and the DSP API version 1 choice is recorded (SND-9)
- Media player: smoother, with controls that step aside
- Leaving a screen stops its downloads
- Archives are unpacked through a rooted folder
- A shorter engineering backlog
- Dates in Mail, the calendar, the Ports store and Steam follow the language, the date order and the 12-hour clock
- The AndroidRT package is reproducible, and its loader must give the exact answer the handheld expects
- The release workflows upload what they built before any advisory step
- The low-battery guard acts once per low-battery episode, not once per JukaHub start
- Cinematic boot: the ground breaks like stone
- The browser removes ads from pages, not just their requests, and the page closes up where they were
- The browser floats figures, infoboxes and columns, and wraps text round them line by line
- The browser's layering engine stacks heroes, badges and sticky headers like a browser
- The browser lays out buttons, badges and inline boxes as the CSS says, and resolves the cascade like a browser
- The browser draws the page's colours and puts its items where the page does
- The browser uses the whole screen and pins fixed bars and sticky headers
- The browser's layering engine
- The browser's layout engine, further
- The browser draws older and simpler pages like a desktop browser, and more modern CSS right
- Cinematic boot: the logo falls from the screen to the ground
- No more buttons the handheld does not have
- Hints name the keys in your hands on every screen
- Last boot says "Kernel to Home" on the stock firmware
- Calculator: a real scientific calculator
- JukaHub's own PPSSPP build is PPSSPP 1.20.3, with no X11
- Downloads panel avoids repeated queue sorting
- More RAM returns when picture-heavy screens close
- Browser releases page memory when closed
- Pointers hide after 10 seconds unused
- The dead-code scan now runs with `make check`
- Quick start in Help now guides players through JukaHub
- The Patch package menu only lists real choices
- The Browser is JukaHub's built-in browser again, on every device — no WPE
- Cheats work for NextUI games too
- Boot as fast as possible: no boot animation and no stock menu by default
- "Which emulator?" asks whenever a game has more than one
- Audit toolkit installs what its tools need on the handheld
- Logs hide the exact secrets kept in JukaHub's settings
- Dates, numbers and sizes follow your language and clock
- Confirmations open on the safe answer
- Radio switches refresh the network status at once
- Automatic box art reuses the library's scan
- Dead code behind the test seams removed
- The chime reference plays the boot styles' knocks
- FTP keeps every folder that holds programs read-only
- Save-state pictures for every library game
- Test-only functions marked
- Tabs Home layout
- PS Remote Play: Play no longer freezes the panel
- Core updates on a full CrossMix card
- Runtime updates show their progress
- Game Profiles start games in the background
- Smoother moving backgrounds
- Tabs Home action tiles
- Maintainer checks
- Screenshots show where they were saved and stay in JukaHub's folder
- Cover art matching is much less strict
- Faster offline voice on the TrimUI handhelds
- JukaOS v2 defaults: the Tabs Home and the Cinematic boot
- Juka's offline voice answers faster
- Chat uses the device name when Your name is empty
- The Games library fills the screen, with a button for every console
- RetroArch is searched for, not just looked up
- The release no longer waits for the offline AI build
- Offline voice commands are faster
- Juka's offline voice no longer goes quiet between sentences
- Offline speech answers right after you stop talking
- Offline answers start in seconds
- JukaHub's own PPSSPP for TrimUI is built automatically
- Offline speech is no longer inside JukaHub-Trimui.zip
- The liquid background is off by default
- HTML5 games run at full speed on the handheld
- The SDL DLLs are no longer kept in git
- Juka answers out loud sooner
- Microphone stick lights stay on and move smoothly
- The Smart Pro's offline voice downloads by itself and is the default without a Groq key
- Box art, emulator updates and PPSSPP on start
- Sharper, ring-shaped ripples on the Smart Pro
- Ripples spill the way the focus went, and overlays bounce
- The liquid background moves like water, not stamped circles
- The liquid background is on, and moving around stirs it
- The liquid background is easy to see
- The liquid background is calm and smooth
- A small Home bounce
- Phone Companion shows only the IP address
- The VPN's "Send one from your phone" opens the VPN card
- Alarm editor rebuilt for the pad
- Bluetooth's state comes from the kernel, not from `bluetoothctl show`
- Quick Settings switches Bluetooth on through the same bring-up
- The liquid background moves smoothly
- Back online faster after the handheld sleeps
- Starting with the device is one hand-over
- A time zone picked in Settings reaches games without a restart
- The config watcher sleeps while the Live Builder is unpaired
- Wine's Windows folder lives where symbolic links work
- DXVK only where it can run
- A Windows program starts in the background
- Android games draw through the firmware's SDL2 on stock TrimUI
- Big Android games stay on the card
- The Emulator updates setting says what it covers
- The weekly checks run AndroidRT's host smoke suite (TEST-4)
- CI checks less on every push, and the test suite is back to its core
- Remote Cast is now Screen Share
- Trophies are one press from Home
- In a Remote Play stream, Select is the touchpad and the PS button fires when Menu is let go
- The startup splash became the boot's quick hello
- DuckDuckGo Lite is the default search engine and home page
- A panic no longer exits as a normal quit
- Core updates ask before they install
- A failed Android game's toast names the Java piece it was missing
- AndroidRT builds without a single compiler warning
- Dates follow one format
- The controls guide lists the keyboard keys screen by screen
- A damaged config no longer stops JukaHub when its backup is good
- macOS and Windows metadata no longer appear as games, albums or files
- Scene-change toasts name the screen, not its internal ID
- The remaining package-level regexps compile on first use
- Synthetic input events are safe on every path
- Contributor guide has a first-task path
- The video player has new controls
- The Home pairing QR code is bigger
- One log, with real levels
- Home puts games first and reads at arm's length
- Faster start on the handheld
- A lean test suite
- About 5,300 lines of dead code removed
- One engineering backlog
- The builder forgets Font Awesome
- A new logo, in the glass design
- The goodbye scene's leftovers are gone
- Startup and shutdown are one glass card each
- Button sounds are a soft thump
- The legacy log is silent unless asked for
- Every screen now wears Home's design
- Focus, pills and highlights use the theme's accent
- The dark palette's surfaces are Home's blue-grey
- Lighter frames on the handheld
- The release offer is a quiet system chip, not a warning banner
- The Benchmark screen has one Run button that measures both phases
- The notification bell is a header button, not a headline
- Background polling stops while JukaHub is not on screen
- The handheld keeps its emulator cores current by itself, and stops piling up the builds it replaces
- A home frame no longer pays for work it covers up, and the focus lift no longer mints half a megabyte of texture per step
- The scene-navigation memory harness measures the box-art ceiling that actually ships
- One top bar on every screen, and a Back button you can find at arm's length
- Text is measured once instead of on every frame, which is what the handheld was spending its frame on
- The Browser's saved-cookie count now says which sites it belongs to
- The Package Manager's rows are half their old height
- The Wi-Fi picker's connected row now reads the same signal chain as the corner meter
- The classic Tools menu (the config's `Misc` scene) is a launcher page instead of a wall of wide buttons: a titled header with the Wi-Fi address…
- The disk pie chart only refreshes while its screen is up
- Element `width`/`height` values with surrounding whitespace are accepted, so a padded number from the XML dialect behaves like the same number in…
- The shell now prefers the device's GLES2 GPU renderer on handheld targets (the A133P's PowerVR GE8300 where the vendor stack provides it) and logs…
- Rounded cards, rings and circles are drawn from a cached render-target texture instead of repeating every scanline fill every frame…
- B — on the video player no longer stops playback: it closes the settings sheet, then leaves full screen, then floats the video into the mini window…
- [CONTRIBUTING.md](CONTRIBUTING.md) points setup and the pre-pull-request checks at the make targets, and keeps the raw commands for anyone without…
- The Emulators page states availability instead of hinting at it: each emulator tile carries an Available / Unavailable / Not installed pill at its…
- The launcher app grid is now row-virtualized: each frame visits only the rows the panel can show (plus one slack row for the partially visible row…
- Thumbnail decode budget: a freshly scrolled grid used to start a JPEG decode and texture upload for every visible row in the same presented frame…
- The Tools panel reads like the rest of the shell: its header opens with the same tools glyph as the Home tile, and each section heading ("FTP & IP"…
- The browser's D-pad model is reachable on real gamepads and follows the page: L3 (left stick click) cycles scroll ⇄ link mode alongside Tab, the…
- Power tools reads as a list instead of a stack of labels

### Fixed

- A game started on JukaHub's glibc gets its own `/dev/shm` when the firmware has none (JH-HW3M9G): the buildbot's SwanStation build of 2026-11-28 keeps its memory arena in a POSIX shared mapping, CrossMix on the Smart Pro S mounts no `/dev/shm`, so `shm_open` returned `ENOENT` and the game stopped after 1.1 s with `[Create] shm_open failed: 2` and `Failed to allocate memory` — which the card read as a bad core build and rolled back. The launch's setup script now creates the folder and mounts a mode-1777 tmpfs over it inside the game's own mount namespace, keeps a `/dev/shm` the firmware does have as it is, and says so on the launch's stderr when neither works; nothing outside the game sees a change. Backend: `raGlibcSetupText` in `player/ra_glibc.go` (the firmware's `mkdir` beside `mount` and `readlink`); `ra_glibc_test.go` pins the step and that it sits after the namespace check. Not checked: a launch on a handheld with no `/dev/shm` (the fix is reasoned from the report's RetroArch log; no device run), and the shell script was only syntax-checked, not run.

- The Live Builder's pairing proof was always an empty signature: WebCrypto returns `ArrayBuffer`s and the byte-to-hex helper treated them as empty arrays. It now wraps them as `Uint8Array`, and the web tests check the signature/nonce headers instead of the old plain code header.
- Configs written with the pre-normalisation built-in salt decrypt again: `decryptData` retries the original derivation when the current key fails, without guessing at user-supplied key material.
- `TestPkgPublishApps` no longer asserts a Unix executable bit on Windows, where NTFS cannot store one.
- The shared SDL installer finds `curl.exe`/`wget` when `curl` is not on a Git Bash PATH, and the TrimUI build test skips on host toolchains that emit no `GLIBC_*` symbol versions instead of calling a good binary bad; both still fail on CI.
- The repo's own `make check` is green again: gofmt, vet, the Go and web suites, workflow/link/backlog checks and the test-only seam comments all pass.

- A Windows launch that stopped because a library this firmware has is too old says so on its card and in its report: JukaHub reads Weston's own `Failed to load module: …: undefined symbol: X, version V` line from the session's log and names the function, the library that should answer it and the version — `libz.so.1 is too old: no inflateValidate (ZLIB_1.2.9)`, which `libpng16.so.16` needs — instead of only "Weston stopped while it was starting", and the same three are the report's first finding (JH-6G5TZF, JH-2S4CSH; `wine_weston_cause.go`)
- Notepad++ and Wine Test start on a stock Smart Pro again: WestonPack's libpng16 asks zlib for `inflateValidate`, the one function zlib 1.2.9 added, and the firmware loads zlib 1.2.8, so Weston stopped at `undefined symbol: inflateValidate, version ZLIB_1.2.9` in every mode. JukaHub gives the session a shim that answers that call and keeps the firmware's own zlib underneath, so everything else is still its code (JH-6G5TZF, JH-2S4CSH)
- Minecraft's Java port starts on a stock Smart Pro: PortMaster's LÖVE runtime (the port's own launcher menu) loads libmpg123, libvorbis and libvorbisfile from the firmware, which has none of them, so JukaHub installs Ubuntu's ARM64 copies (pinned packages, SHA-256 checked) on the first port start with Wi-Fi on and puts them last on the port's library path (JH-TKWGE5)
- The launch card no longer says a runtime is missing when a port's own rename of PortMaster's `*.aarch64.squashfs` images fails with every runtime its `port.json` declares in place — Minecraft's launcher does that on every launch after the first
- OldBits Hub starts on stock firmware: a port under Roms/Ports that needs bash runs under JukaHub's, not the firmware Ports launcher's BusyBox sh
- A port's launch card names BusyBox's `export: illegal option -f` as the cause, and shows the line it printed instead of "JukaHub found nothing" when the log holds an error no known cause matches
- Browser: an ad cut out of a page takes the box it was the whole of, so its slot no longer draws as a blank gap
- Notepad++ and Wine Test start again: Weston's own modules are given the one library the WestonPack image leaves to the firmware
- A port is given no shell option set from JukaHub, and a script that stopped on an unset variable is named on its launch card
- A port that crashed is no longer blamed on memory when PortMaster's own cleanup stops its key mapper
- A port that needs PortMaster's files installs them instead of failing
- A port whose own game files are missing says so, and the launch card stops blaming PortMaster or a probe command
- Smart Pro launches for GameCube, PSP, Xbox and Windows apps
- PlayStation 2 on every TrimUI model: a folder, a tile, and a start that draws
- Media player: frames on time, seeks without jumps, volume kept
- Browser: a page's video plays in its place and opens in the player; long pages load all their pictures
- Music: internet radio stops, and the player's media controls take the pad
- Browser: tapping a video plays it; script players get a play card; storage stubs; nested layers at their own foot
- Player: caption size works, captions readable without a panel, films start in your languages; the Browser plays `<audio>`
- Windows apps: Wine gets as far as the program, and a display that never comes up no longer freezes the handheld
- Download links download; nested positioned boxes; cleaner, faster pages
- Heavy pages such as cnn.com open, and open sooner
- GreenOvercast's launch card names the real cause, and JukaHub works round a saved sign-in GreenOvercast cannot read
- PPSSPP opens from its Emulators tile, and the launch card no longer blames setterm
- Windows apps now reach Wine through WestonPack on the Smart Pro
- Animated content shows, drop caps and small caps draw
- List markers follow CSS, and newer CSS is read
- Lists in the Browser, as on Wikipedia's Main Page
- PSP games stopped at once after one bad PPSSPP run
- Submitting a launch report froze the screen for 7.5 s
- Bug reports' recent log was filled with frame-rate lines
- Tables in the Browser: cells with paragraphs, nested tables, unclosed markup
- Emulators page: an emulator that could not open said nothing, and some could not open at all
- Bug reports kept the oldest half of a long log
- Pages in other scripts draw their words, not "…"
- Off-canvas menus stay off the screen, and floated menus line up
- DuckDuckGo Lite's results read like a browser's again
- Search forms look like their page made them
- The first-run tour fits its card again
- JukaHub started from another folder works from its own
- The software dimmer covers the boot and the goodbye too
- Continuing a game from another device no longer copies saves beside a running sync
- A power cut while an update is being undone no longer leaves half of each version
- GameCube on TrimUI: the buildbot's Dolphin gets the last library it lacked
- Xbox games on a Brick Pro: hakuX gets the build for its screen before the game starts
- No games started: RetroArch is installed when the games' one is gone
- Windows apps: Notepad++ starts on a stock Smart Pro
- Stick lights: no flicker every two seconds
- Bluetooth stays off after a restart
- Brightness and volume come back after a restart
- Browser: padded cards and panels are as wide as their box again
- Browser: centred and justified lines of an indented paragraph stay inside it
- Browser: no debugging line on every page layout
- Browser: a table's last row keeps its bottom padding on the page
- Browser: a table row holding one spanning cell no longer paints over the row above it, so a trailing or row-wide `colspan` reads as every column it…
- RetroArch could not be installed on a Smart Pro whose card had none
- Browser: a painted or indented block no longer takes its top inset twice
- "RetroArch cores folder not found" on cards without a cores folder
- JukaHub's threads no longer stay in the idle I/O class after a heavy-game boost
- A new game's launch settings are no longer undone halfway through its preparation
- PSP games no longer play to a black screen on the A133P handhelds
- Emulator downloads on a Smart Pro no longer all stop at "needs GLIBCXX_3.4.30"
- A Bluetooth headset gets its sound back when BlueALSA stops answering
- JukaHub builds, and so releases, again
- The menus get every core back after a game
- Dolphin no longer stops at "libusb-1.0.so.0" on TrimUI handhelds
- Dolphin installs on a Brick Pro when JukaHub's own build is not there
- JukaHub's own Dolphin build can no longer pick up libudev in CI
- Chat no longer freezes for seconds on a photo
- Xbox games no longer stick on JukaHub's screen, and Menu opens hakuX
- GameCube and Wii have their folders now
- Pulling a Go dependency update no longer stops the build
- "Can't update the hub, it failed during update"
- Dolphin "cannot load on this firmware", and cores that cannot work on a TrimUI handheld
- Voice tab: "This voice server does not let guests open rooms here"
- A game and another screen taking turns after a sleep
- The exit status after a stop
- Tetris froze, 2048 stopped adding tiles
- PortMaster stuck on its splash
- PortMaster on a stock Brick or Smart Pro, again
- A bug report says what PortMaster did
- Sending a bug report no longer freezes the screen
- Next no longer drops a song from the play-next queue
- A port that stops at once now says why
- An offline AI question can now clear the full set of caches immediately after an automatic light trim, instead of waiting for the trim's 15-second…
- Video speed really changes the speed
- Music and audiobooks keep their place at another speed
- Aircrack-ng and Nikto install and run on the handheld
- Juka's vision cache now has an 8 MB handheld / 12 MB desktop byte limit for processed pictures and video frames, and releases them under memory…
- Chat image downloads now use one pending slot instead of accumulating multiple 32 MB buffers; avatar downloads use four 1 MB slots
- Release builds embed current Help pages and filed reports show the update channel
- Cancelling Windows subtitle fetches stops the entire yt-dlp process tree
- Windows update helpers start safely from folders with `&` or parentheses
- New mail and calendar secrets join log redaction on the next frame
- Destructive choices on Tabs Home and Remote Play now open on Keep
- John the Ripper runs on the handheld instead of stopping at its config
- The X1 pill is green only while an Xbox is actually there
- Quick Settings' Wi-Fi and Airplane Mode switches work without a working `rfkill` program
- The in-game menu fits short screens
- Game Boy, Game Boy Color and GBA games start again when a core update broke mGBA
- JukaHub's icon in the TrimUI Apps menu is the size of the others
- PS4/PS5 Remote Play reconnects after the handheld sleeps
- The shader speed check measures something now
- Stick lights no longer blink
- N64 (and other GPU cores) no longer broken by the automatic core update
- Boost no longer resets a game's other core options
- PortMaster opens on a stock TrimUI Smart Pro
- The private glibc is Ubuntu's newest signed build, with bash and the C++ runtime
- Quick Settings ignored the mouse, and L2/R2 never reached it
- The Patch manager remembers installed packages, and Remove deletes them
- A restore writes only JukaHub's own data files
- Browser cookies: fewer writes to the card
- Scene Terminal commands stop after two minutes
- Radio and podcasts no longer go silent for good when the stream stops
- WebP pictures open on the stock Smart Pro
- Picture and video questions to Juka survive a sleep
- The surface probe measures the EGL a game run uses on the stock firmware
- A stream that stops sending no longer freezes the player
- Browser cookies keep their HttpOnly flag
- A crash no longer leaves a game running under the menu
- Xbox 360 library lists what Xenia opens
- Windows launchers keep their game on screen
- A backup can no longer replace JukaHub's own files
- Stock TrimUI HTTPS tools and system log
- Stock Smart Pro startup and time-lapse codecs
- PortMaster catalog screenshots
- Watch Party guests play YouTube rooms
- Wi-Fi sign-in pages and network changes
- Juka after a sleep
- The Downloads queue never picks AV1 on the handheld
- Time-lapse for Wine profiles
- Home search opens pages, not rows
- The Games tab's Install badge knows per-game emulators
- Command Deck and Juka tools
- Archive extraction trusts less
- Secrets stay out of every log
- PS5 pairing helper errors
- Remote Play helper check
- Wine profile timelapses
- Music: no lost last second, no silent race through the playlist in games
- Internet browser: pages load whole and styled
- Code-scanning alerts, November batch
- A smooth focus bounce on the Smart Pro
- JukaHub OS boots straight into JukaHub
- The interface no longer hangs
- Bluetooth headphones: a BlueALSA that is running but not on D-Bus is dealt with
- Battery "Cold" no longer tells you to stop charging
- jukauser.json is no longer rewritten when nothing changed, nor ten times a second from Accessibility rows
- Diagnostics' frame-time length read "31m8000000000s"
- Hey Juka stopped swapping microphones in a quiet room
- Diagnostics name where RetroArch was found
- Automatic box art no longer re-reads a console's cover list for every game it cannot get
- Diagnostics wrote "30m3000000000s" for a frame-time window
- Screen time is written to the card every two minutes instead of every 30 seconds
- PSP games: the pad works and Menu gets you out
- PSP games start again on the TrimUI Smart Pro
- RetroArch games and saves are found on the handheld
- Wi-Fi reconnects by itself on TrimUI
- CI no longer sits on "Build optional offline AI"
- Offline answers no longer stop at "3." in "3.14" or at "U.S.",
- Offline game hints start faster:
- Settings no longer offers an offline AI update that would download the same files again,
- Hey Juka no longer switches itself off for the session when the SD card is slow at startup,
- Offline game hints are read out to the end
- A stalled offline-voice download stops after a minute
- Restores and reloads keep current settings
- Box art reached only the first consoles; numbered PSP ISOs got none
- Bluetooth headphones on the Smart Pro got no sound
- PPSSPP did not update itself on the Smart Pro
- Asking "is Wi-Fi on?" turned Wi-Fi on
- CrossMix: RetroArch games start from JukaHub again
- TrimUI Brick Pro on CrossMix: games that did not start said nothing
- Saved videos and tracks finish again
- Stick rings blinking every two seconds
- Games with no exact box-art match got no picture
- Stick lights stayed dark on the TrimUI
- Settings → Stick lights is tidier
- Bluetooth pass (switch, pairing page, headphones, controllers)
- Offline voice: "voice settings download: checksum did not match"
- Video and sound stay together
- Background music that started by itself can be found and stopped
- A RetroArch game keeps its sound after the in-game menu (Brick report)
- YouTube streams again once JukaHub's own ffmpeg is installed (TrimUI)
- RetroArch is found wherever it is installed
- Bluetooth headphones get JukaHub's sound
- PS Remote Play Play button now streams from a renamed JukaHub folder
- APK Store appears in Packages on Windows
- One name for you everywhere
- The Continue card follows the theme
- Settings audit
- Bluetooth can start the XR829 on the TrimUI Smart Pro when the firmware left it off
- Box art is found for games again, automatically and from Fill gaps
- Bluetooth on the TrimUI Smart Pro: the chip is found and started
- Quick Settings no longer leaves Bluetooth unpowered behind an rfkill block
- A Bluetooth device that does not connect gets advice on the stock firmware
- Leaving JukaHub no longer leaves CrossMix glitchy
- A video that would not stream now really downloads, and shows it
- Saving YouTube videos uses yt-dlp's own clients
- Labels cannot print a protected setting
- Long About values stay inside their cards
- Remote Play uses the home address only when this console answers there
- PS Remote Play's console options cannot overwrite a pairing saved at the same moment
- The Phone Companion's PIN budget is per address
- A clock jump cannot replay a scheduled task's missed runs
- A cross-origin script's error no longer leaks its text or address
- An x86-64 PC no longer asks for Box64 to run its own Wine
- The Smart Pro no longer installs Hangover, which its firmware cannot load
- Runtime archives unpack on exFAT and FAT cards
- A WestonPack that resets every device is refused
- An empty or broken `Apps/AndroidRT` no longer hides the AndroidRT that ships with JukaHub
- xemu installs can no longer take the debug build
- A final release beats its release candidate
- hakuX housekeeping (XBX-6)
- Install failures say what went wrong
- Rolling back waits for a running update
- The Core builds page no longer says "Auto-update on" on a Linux desktop
- Starting JukaHub no longer hands out trophies
- What changed in a session survives the restart
- No `(s)` plurals and no `N/A` in the UI
- The progress track follows the theme
- A scrollbar thumb stays in its track
- The full-screen now-playing title centres on what it draws
- The Downloads list stays inside its panel
- Regexps compile on first use
- Attribute values serialize like a browser's
- The Remote Play helper build gets past chiaki-ng's libevent check, with a readable log
- A key that opens a text field no longer types its own letter on Live TV or Media
- "Use JukaHub on 90 days" meant 90 of the last 91
- An update that kept coming back after it was installed is fixed
- Thermal throttling is visible on the handheld
- Labels no longer expand protected settings
- Animated wallpapers release frames while a game owns the screen
- Focused game covers no longer decode during drawing
- UI font sizes share one in-memory source
- Voice finds the Smart Pro's microphone
- The power-on boot moves on the handheld
- WPE Browser gets the animated wallpaper's memory too
- A release offer that is gone no longer answers a tap
- Updated the game library and accessibility docs to match the app
- Launch failure help survives wrapped errors
- The Remote Play helper build gets past chiaki-ng's configure
- Remote Play looks for its helpers where an update puts them
- The Smart Pro has a microphone
- A wallpaper picked in the picker is kept
- Watching the screen no longer stutters the handheld
- YouTube subtitles say "no subtitles" much less often, and say why when they do
- Leaving JukaHub no longer puts two launchers on the TrimUI's screen
- PS Remote Play hands the screen over cleanly and brings you back to JukaHub
- RetroArch started from CrossMix's or the stock menu behaves as it did before JukaHub
- Games and emulators start with the firmware's environment, not JukaHub's
- Local play and per-game options no longer break RetroArch on stock and CrossMix launchers
- Core updates land where RetroArch loads them, and the first one comes soon
- No more phantom PPSSPP, mGBA and Flycast "updates" on the handheld
- The PS Remote Play panel no longer sends people to Link Device for the Account-ID, and PS4 pairing asks for it too
- PS Remote Play's native helpers finally ship
- Two stream-helper bugs that ended streams
- Remote Play's PIN fields open on the digits, and the helpers run without their execute bit
- The TrimUI firmware keeps hearing its settings file
- Files' action sheet: a held key no longer confirms what it armed
- "Start JukaHub at login" now really boots a TrimUI into JukaHub
- A tag with a stray quote no longer freezes a page load
- Pages no longer draw blank because of attribute or pseudo selectors
- The `hidden` attribute hides
- Long notifications are shown whole
- A picture attached in Chat left Files in attach mode
- Pictures attached in Chat stopped showing after Files moved
- Another device's message no longer vanishes as an "echo"
- A chat message's last actions could be out of reach
- A video that will not stream no longer flashes black and vanishes
- YouTube plays on a stock TrimUI
- Game details no longer draw over each other
- Quitting no longer writes a goroutine dump
- CrossMix's emulators, PPSSPP and RetroArch are found
- Fullscreen is one mode everywhere
- A huge download size can no longer crash the free-space message
- A config import's safety backup can no longer prune itself
- Damaged configs and patch databases stay bounded
- A backup restore reloads the design file once
- The SD-card package ships the one launcher script
- The boot guard can now roll back a bad update on a handheld without a clock
- Rolling back to a backed-up executable works while JukaHub is running
- A safety backup can no longer be deleted right after it is written
- First-run "Connect to Wi-Fi" and "Set your time zone" work on the handheld
- An alarm can no longer be silenced by a clock that ran ahead
- On a Linux computer the file servers no longer take your home folder for the SD card
- FTP folders show as folders
- An endless FTP command line can no longer exhaust memory
- A failed reinstall no longer removes the package
- Replacing an emulator core can no longer leave it missing or empty
- A buildbot index with a size column no longer reads as empty
- A game asking for a missing Java class every frame no longer grows memory
- Android games use exactly the EGL library the present layer chose
- The browser's Search works from a keyboard
- Keyboard X and Y in the video player do what the pad's X and Y do
- Footer hints that named only pad buttons now name their keys
- Missing keyboard twins added
- Holding X on the alarm list no longer flips the alarm on and off
- The controller test speaks keyboard on a keyboard
- A Wi-Fi login page no longer looks like internet
- Opening a link can no longer run a command on Windows
- An Xbox game with no launcher names the fix
- The metadata filter covers every scanner it was meant to
- Links opened in the browser from another screen load
- The video player's settings sheet answers taps where it is drawn
- A click no longer seeks a video that is not on screen
- Ctrl+Q no longer switches the screen too
- Pomodoro's keyboard matches its footer and the pad
- Notes' shortcut letters stay out of the text
- Navigation runs at the frame rate it promises
- SDL batches draw calls on the device again
- The live-builder pairing code is no longer in the repository
- An EPUB without a title no longer crashes the app
- The Hero grid fits the Brick
- An x86-only Android package is refused up front
- `errors.txt` stops growing on the card
- The handheld's Appearance page has the clock row again
- The gallery dialog uses the screen
- Home's grid matches the cards again
- B leaves Apps and Favorites
- The Windows release build no longer waits for a macOS runner
- The Windows release build reuses the previous release's compile work
- The TrimUI release build is fast too
- JukaHub-Trimui.zip no longer waits for the Box64 build, and the Remote Play helpers build faster and less often
- staticcheck is clean again
- X and Y match their labels on the TrimUI Smart Pro and Brick
- The PS Remote Play keyboard types again
- Four more panels let their keyboard have the input, and the key that opens a keyboard no longer types into it
- Charging shows in the corner
- Volume control works on the Smart Pro and the Brick
- YouTube stops leaning on a client it retired — the best current bet, not yet tried on a device
- YouTube failures say what happened, and that no API key is needed
- A google_api_key no longer slows search down
- Videos opened from a link get their title
- Remote Play says why its helper cannot run, before JukaHub quits
- Starting a stream reset Quality and Volume and erased the PSN Account-ID
- A console that got a new address from the router kept its pairing on the old one
- Embedded subtitle extraction is bounded
- Native YouTube client cooldowns survive restarts
- Whole-program audit, November 2026
- Brightness and the stick LEDs were never found on the Smart Pro or the Brick
- Joining one Wi-Fi network disabled every other saved network
- Performance mode never switched the CPU — and its restore would have left the hub at the lowest clock
- The self-update could kill its own helper
- Settings came back after Settings → Save, and some never saved at all
- Files: Delete on the ".." row deleted the parent folder
- Transfers and the phone companion
- Pausing and seeking a video could freeze the whole UI
- Input
- Games
- Browser
- Chat, Watch Party and Smart Home
- Apps
- The handheld config opened the new Home on the old grid
- Colour blends towards a darker colour came back unchanged
- A wallpaper that failed to load was retried on every frame
- Smaller fixes found on the way
- An SFTP client's `stat` of a folder described one of its children
- A damaged snapshot can no longer half-overwrite live saves
- An archive cannot write outside its folder through a link it shipped
- The search log can no longer be derived from the API key
- The parsers are fuzzed instead of only reviewed
- The browser follows a redirect, and the address bar says where it landed
- The dark blue palette now separates, and its text is readable
- The Emulators page is the same page as every other overlay
- Play Link asks for a link and plays it
- The Playtime badge list scrolls with the D-pad and the arrow keys
- The Files, Packages and Emulator cards draw real pictures
- A Brick is identified even when its firmware names no model
- Close and back are two buttons on the video player, and the windowed player answers taps
- The Wi-Fi switch no longer turns the radio off
- The Brick's Wi-Fi picker finds (and if needed starts) its supplicant
- A Brick is identified by its model code, not only by its panel
- The header stops offering an update the device already has
- Wi-Fi works on a TrimUI Brick, and on any image that ships no `wpa_cli`
- A 4:3 panel fills the screen even when the hardware cannot be named
- "Could not install ffplay" no longer fails on every attempt on the TrimUI Brick
- `ffplay` is gone from every download
- The Brick's front F1 / F2 keys do something
- The header's "Update" pill can be selected from where it is drawn
- The Podcasts list no longer extends past the screen bottom on the Brick
- Opening the Wi-Fi picker no longer answers `exit status 255`
- A cancelled quality switch puts the Quality row back on the stream that is actually playing
- Discovery asks the supplicant which interface it drives when nothing on the card names it
- Play is modal from the press, and B cancels all of it
- The Wi-Fi picker finds the radio on a device that is online
- Every JSON store that holds your work now survives a power cut and a damaged file
- The game library releases box art it is done with
- The rendered-text texture cache has a byte budget
- A launcher full of broken icons no longer grows the icon cache
- Notes, alarms and the IPTV list survive a power cut
- The reader's form controls draw glyphs the bundled font has
- An unclosed form control no longer swallows the rest of the page
- An empty field's prompt reads as a prompt
- Typing into a page's field no longer overwrites the page
- The browser opens on its start page, not on whatever the last run was reading
- The browser's bottom chips fit their labels again
- Five ways a "Wi-Fi stopped working" report turned out to be code
- A tap in the Media gallery plays the row it lands on
- The browser's toolbar and cookie strip are full-size tap targets
- The Internet Browser's toolbar chips fit their labels on both panels
- The screensaver's hint line no longer runs under the Cookies row in Settings → General
- The toolbox scan-gate test no longer races the tool it launches
- The screensaver auto-tick tests no longer depend on what earlier tests left behind
- The first-run tour's controls hint no longer runs across the card
- Buttons now measure the label they draw, so a row's derived value is neither clipped nor printed raw
- A video snap ffmpeg cannot decode is now remembered, instead of being restarted on the next frame
- The Wi-Fi picker reads the right adapter when Windows reports several
- The Package Manager shows which package is selected
- The Wi-Fi meter shows a real level on images without `/proc/net/wireless`
- The plaintext-secret warning never fired
- The handheld build no longer loses apps to a stale scene list
- Config writes are crash-safe now
- A LAN Play launch that cannot start RetroArch now keeps the underlying cause, so the failure dialog suggests reinstalling the launcher instead of…
- The intermittent `SDL_RenderReadPixels` crash in the test suite is fixed at its cause
- One touch tap on a video card in the browser resolved the video twice
- The `player-race` CI job is green again
- A CPU cap below the ceiling the firmware booted with was ignored: `raiseFrequencyCeiling` only ever wrote a higher `scaling_max_freq`, so a device…
- The Wi-Fi picker is usable by touch from the status bar: the signal meter's tap target now spans the top bar and is at least 44px wide (it was the…
- The app could appear locked after the TrimUI's power button: the stop signal killed the process before any cleanup, a running game kept the…
- Launching a library game crashed on Linux when no config was loaded: the time-lapse probe reached `getToolPath` with a nil config, which dereferenced…
- `beginGamePowerProfile` no longer logs "no cpufreq governors on this device" and shows a "Performance mode unavailable" toast on machines that have…
- The browser address-bar test queues SDL events through union-sized wrappers, so `go test -race` no longer dies in checkptr with "converted pointer…
- Two staticcheck findings in tests: a tautological tint comparison (`SA4000`) and a parsed base URL that was immediately overwritten (`SA4006`)
- Chat overlays let go of the pad when you leave the chat scene

### Removed

- The invented "social notifications" feed is gone

### Security

- The Screen Share link is view-only
- Page scripts are kept off the handheld and the LAN, and away from other sites' cookies
- The live builder can no longer be driven by a web page, a guess or a tricky design
- The live builder's pairing code file is out of the repository
- FTP now asks for a password and shares only your games, saves and media
- SFTP shares the SD card, not the whole system
- The file servers switch themselves off
- Batch files start without cmd.exe reading their names as commands
- Package installs are checked, staged and swapped whole
