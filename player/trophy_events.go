package main

// trophy_events.go — what the trophies count besides time.
//
// The first hundred trophies (playtime.go) read only the clock: time in the
// app and in each screen, days and streaks. The rest also count things done —
// tracks played, pages read, games launched, blocks placed in JukaHub's own
// sandbox — and a few remember a best (a Snake score, a download speed). A
// feature reports such a moment with one call where it happens:
//
//	trophyEvent(tkScreenshot)              // one more
//	trophyEventN(tkDownloadMB, mb)         // n more
//	trophyEventPeak(tkSnakeBest, score)    // a new best, if it is one
//	trophyEventDistinct(tkGameSystems, id) // one more distinct value
//
// The calls are safe from any goroutine and cheap: they add to a small pending
// record under a mutex and return. The render thread drains it once per tick
// (playtimeTick) into the playtime snapshot, so the counters are saved with the
// rest of the trophy data (jukauser.json, playtime_v1) and the unlock pass
// reads them from the same place as the time it counts. An event that arrives
// before the snapshot is loaded simply waits in the record.
//
// A distinct value is kept as a 32-bit FNV-1a hash of its lower-cased text,
// never as the text: a trophy needs "how many different sites", not which
// ones, and the save must not turn into a browsing history.

import (
	"hash/fnv"
	"sort"
	"strings"
	"sync"
)

// trophyKey names one counter, best or set of the trophy data.
type trophyKey string

// The keys. Each comment says exactly when the event is reported, so a second
// call site keeps the count honest; a key written by the playtime tracker
// itself says so.
const (
	// ── Games ──

	// tkGameLaunch: a game started from JukaHub, by any route (the library,
	// recents, favourites, the random pick, search, voice). Once per launch
	// that actually started.
	tkGameLaunch trophyKey = "game_launch"
	// tkGameTitles (distinct): the launched game's title, or its path.
	tkGameTitles trophyKey = "game_titles"
	// tkGameSystems (distinct): the launched game's system ("snes", "psp").
	tkGameSystems trophyKey = "game_systems"
	// tkGameRandom: a game started from the random pick.
	tkGameRandom trophyKey = "game_random"
	// tkGameComplete: a game marked Completed (the change to completed only).
	tkGameComplete trophyKey = "game_complete"
	// tkGameSecs: seconds spent in games launched from JukaHub (the playtime
	// tracker writes it).
	tkGameSecs trophyKey = "game_secs"
	// tkNetplay: a netplay session hosted or joined.
	tkNetplay trophyKey = "netplay"
	// tkLocalPlay: a local multiplayer session started with two players or more.
	tkLocalPlay trophyKey = "local_play"
	// tkRemotePlay: a PlayStation Remote Play stream started.
	tkRemotePlay trophyKey = "remote_play"
	// tkEmuInstall: an emulator, a core or an emulator update installed.
	tkEmuInstall trophyKey = "emu_install"
	// tkPortMaster: PortMaster installed by JukaHub, or its own menu opened
	// (it is the app that installs ports; JukaHub never installs a port itself).
	tkPortMaster trophyKey = "portmaster"
	// tkSaveBackup: a game save backed up or synced.
	tkSaveBackup trophyKey = "save_backup"
	// tkBoxArt: box art saved for a game (n per batch).
	tkBoxArt trophyKey = "box_art"
	// tkRAUnlock: a RetroAchievements unlock arrived (trophy_celebrate.go).
	tkRAUnlock trophyKey = "ra_unlock"

	// ── Arcade: the built-in minigames (games.go) ──

	// tkArcadePlay: a minigame started.
	tkArcadePlay trophyKey = "arcade_play"
	// tkArcadeGames (distinct): which minigame ("snake", "tetris", ...).
	tkArcadeGames trophyKey = "arcade_games"
	// tkSnakeBest (best): a Snake score.
	tkSnakeBest trophyKey = "snake_best"
	// tkTetrisLines: n lines cleared in Tetris.
	tkTetrisLines trophyKey = "tetris_lines"
	// tkTetrisQuad: four lines cleared with one piece.
	tkTetrisQuad trophyKey = "tetris_quad"
	// tk2048Tile (best): the highest tile made in 2048.
	tk2048Tile trophyKey = "2048_tile"
	// tkPongPoint: a point the player scored past the AI in Pong (a match never
	// ends, so there is no win to count).
	tkPongPoint trophyKey = "pong_point"
	// tkMinesWin: a Minesweeper board cleared.
	tkMinesWin trophyKey = "mines_win"
	// tkFlappyBest (best): pipes passed in one Flappy run.
	tkFlappyBest trophyKey = "flappy_best"

	// ── JukaLand ──

	// tkJlandPlace: a block placed.
	tkJlandPlace trophyKey = "jland_place"
	// tkJlandBreak: a block broken.
	tkJlandBreak trophyKey = "jland_break"
	// tkJlandOre: an ore block broken.
	tkJlandOre trophyKey = "jland_ore"
	// tkJlandWorld: a new world made on request (the pause sheet's New world).
	tkJlandWorld trophyKey = "jland_world"
	// tkJlandOreKinds (distinct): the kind of ore broken ("Coal" … "Diamond").
	tkJlandOreKinds trophyKey = "jland_ore_kinds"

	// ── Music and podcasts ──

	// tkTrackPlay: a music track started (a new track, not a resume).
	tkTrackPlay trophyKey = "track_play"
	// tkArtists (distinct): the started track's artist.
	tkArtists trophyKey = "artists"
	// tkPlaylist: a playlist created.
	tkPlaylist trophyKey = "playlist"
	// tkLyrics: lyrics opened for a track.
	tkLyrics trophyKey = "lyrics"
	// tkPodcastPlay: a podcast episode started.
	tkPodcastPlay trophyKey = "podcast_play"
	// tkPodcastSub: a podcast subscribed to.
	tkPodcastSub trophyKey = "podcast_sub"

	// ── Video ──

	// tkVideoPlay: a video started in the video player (YouTube, Tube, a
	// local file, a stream).
	tkVideoPlay trophyKey = "video_play"
	// tkShorts: a Short shown (each new one).
	tkShorts trophyKey = "shorts"
	// tkVideoSearch: a Tube or YouTube search submitted.
	tkVideoSearch trophyKey = "video_search"
	// tkSubtitles: subtitles fetched or turned on for a video.
	tkSubtitles trophyKey = "subtitles"
	// tkTVPlay: a Live TV or IP stream channel started.
	tkTVPlay trophyKey = "tv_play"
	// tkTVChannels (distinct): the started channel's name or address.
	tkTVChannels trophyKey = "tv_channels"
	// tkRecording: a game time-lapse made (JukaHub has no screen recorder; the
	// time-lapse of a game played long enough is its recording).
	tkRecording trophyKey = "recording"
	// tkCast: screen sharing or casting started.
	tkCast trophyKey = "cast"
	// tkWatchParty: a Watch Party hosted or joined.
	tkWatchParty trophyKey = "watch_party"
	// tkImageView: an image opened in the image viewer.
	tkImageView trophyKey = "image_view"

	// ── Downloads ──

	// tkDownload: a download finished successfully (tracks, videos, files).
	tkDownload trophyKey = "download"
	// tkDownloadMB: n megabytes (MiB, rounded down) a finished download saved.
	tkDownloadMB trophyKey = "download_mb"
	// tkAudioExtract: audio extracted from a video.
	tkAudioExtract trophyKey = "audio_extract"

	// ── Web ──

	// tkPageVisit: a page loaded in the Browser (a top-level navigation:
	// a link, the address bar, back or forward, a bookmark).
	tkPageVisit trophyKey = "page_visit"
	// tkSites (distinct): the loaded page's site (registrable domain, or host).
	tkSites trophyKey = "sites"
	// tkBookmark: a bookmark added.
	tkBookmark trophyKey = "bookmark"
	// tkAdsBlocked: n requests the ad blocker stopped.
	tkAdsBlocked trophyKey = "ads_blocked"
	// tkFindInPage: a find-in-page search run.
	tkFindInPage trophyKey = "find_in_page"
	// tkTextPage: a page opened in the Text Browser.
	tkTextPage trophyKey = "text_page"

	// ── Chat and other devices ──

	// tkChatSend: a chat message the user sent.
	tkChatSend trophyKey = "chat_send"
	// tkChatFile: a file the user sent in Chat.
	tkChatFile trophyKey = "chat_file"
	// tkAIAsk: a question put to the AI assistant.
	tkAIAsk trophyKey = "ai_ask"
	// tkPeerPair: paired with another device (Devices, Nearby).
	tkPeerPair trophyKey = "peer_pair"
	// tkFileTransfer: a file sent to or received from another device.
	tkFileTransfer trophyKey = "file_transfer"
	// tkFriend: a Watch Party room saved to the list of friends' rooms.
	tkFriend trophyKey = "friend"

	// ── Files ──

	// tkFileCopy: n items copied.
	tkFileCopy trophyKey = "file_copy"
	// tkFileMove: n items moved.
	tkFileMove trophyKey = "file_move"
	// tkFileDelete: n items deleted (or put in the trash).
	tkFileDelete trophyKey = "file_delete"
	// tkFileRename: an item renamed.
	tkFileRename trophyKey = "file_rename"
	// tkFolderNew: a folder created.
	tkFolderNew trophyKey = "folder_new"
	// tkFileEdit: a text file edited and saved in the file editor.
	tkFileEdit trophyKey = "file_edit"
	// tkExtract: an archive extracted successfully.
	tkExtract trophyKey = "extract"

	// ── Tools ──

	// tkTermCmd: a command run in the Terminal.
	tkTermCmd trophyKey = "term_cmd"
	// tkNoteSave: a note saved (new or edited).
	tkNoteSave trophyKey = "note_save"
	// tkCalc: a calculation evaluated in the Calculator.
	tkCalc trophyKey = "calc"
	// tkUnitConvert: a conversion made in the Unit Converter.
	tkUnitConvert trophyKey = "unit_convert"
	// tkDictLookup: a word looked up in the Dictionary.
	tkDictLookup trophyKey = "dict_lookup"
	// tkPdfOpen: a PDF opened.
	tkPdfOpen trophyKey = "pdf_open"
	// tkPdfPage: a new page shown in the PDF reader.
	tkPdfPage trophyKey = "pdf_page"
	// tkBenchmark: a benchmark run completed.
	tkBenchmark trophyKey = "benchmark"
	// tkDiskScan: a disk-space scan completed.
	tkDiskScan trophyKey = "disk_scan"
	// tkSpeedTest: a network speed test completed.
	tkSpeedTest trophyKey = "speed_test"
	// tkSpeedBest (best): a speed test's download speed, whole Mbit/s.
	tkSpeedBest trophyKey = "speed_best"
	// tkNetTool: a network tool run (ping, DNS, traceroute, a port check).
	tkNetTool trophyKey = "net_tool"
	// tkPomodoro: a Pomodoro focus period completed.
	tkPomodoro trophyKey = "pomodoro"
	// tkAlarmSet: an alarm created.
	tkAlarmSet trophyKey = "alarm_set"
	// tkAlarmWake: a ringing alarm dismissed.
	tkAlarmWake trophyKey = "alarm_wake"
	// tkAlarmSnooze: a ringing alarm snoozed.
	tkAlarmSnooze trophyKey = "alarm_snooze"
	// tkWeather: the forecast opened by the user (the popover or the screen).
	tkWeather trophyKey = "weather"

	// ── System ──

	// tkScreenshot: a screenshot saved.
	tkScreenshot trophyKey = "screenshot"
	// tkWake: a press woke the screen (the screensaver, or the dock's darkened
	// panel). JukaHub never sees the handheld's own suspend and resume.
	tkWake trophyKey = "wake"
	// tkUpdate: a JukaHub update installed.
	tkUpdate trophyKey = "update"
	// tkBackup: a backup file created.
	tkBackup trophyKey = "backup"
	// tkRestore: a backup restored.
	tkRestore trophyKey = "restore"
	// tkBugReport: a bug report sent (or queued to send).
	tkBugReport trophyKey = "bug_report"
	// tkWifiJoin: a Wi-Fi network joined at the user's request.
	tkWifiJoin trophyKey = "wifi_join"
	// tkWifiNets (distinct): the joined network's name.
	tkWifiNets trophyKey = "wifi_nets"
	// tkBTPair: a Bluetooth device paired.
	tkBTPair trophyKey = "bt_pair"
	// tkPads (distinct): a game controller connected (its name or GUID).
	tkPads trophyKey = "pads"
	// tkPackage: a package installed from Packages.
	tkPackage trophyKey = "package"
	// tkPatch: a package installed, updated or reinstalled on the Patch screen.
	tkPatch trophyKey = "patch"
	// tkPlugin: a plugin installed or turned on.
	tkPlugin trophyKey = "plugin"
	// tkSearch: a search run from Home or the global search (a result opened
	// or a query submitted, once per search).
	tkSearch trophyKey = "search"
	// tkStartMenu: the Start menu opened.
	tkStartMenu trophyKey = "start_menu"
	// tkQuickSettings: Quick Settings opened.
	tkQuickSettings trophyKey = "quick_settings"
	// tkVoiceCmd: a voice command understood and carried out.
	tkVoiceCmd trophyKey = "voice_cmd"
	// tkKeyChars: a character typed with an on-screen keyboard key (not a
	// hardware keyboard, a paste, a suggestion or dictation).
	tkKeyChars trophyKey = "key_chars"
	// tkFavorite: something added to Favorites.
	tkFavorite trophyKey = "favorite"
	// tkAppLaunch: an app launched (from Apps, Home search, voice or the
	// Windows apps panel).
	tkAppLaunch trophyKey = "app_launch"
	// tkLowBattery: the low-battery warning shown.
	tkLowBattery trophyKey = "low_battery"
	// tkTourDone: the welcome tour finished.
	tkTourDone trophyKey = "tour_done"
	// tkStickCal: a stick calibration saved.
	tkStickCal trophyKey = "stick_cal"
	// tkSmartHome: a smart-home device switched or set.
	tkSmartHome trophyKey = "smart_home"
	// tkNotifCenter: the notification centre opened.
	tkNotifCenter trophyKey = "notif_center"

	// ── Style ──

	// tkTheme: a theme preset applied.
	tkTheme trophyKey = "theme"
	// tkThemes (distinct): the applied preset's name.
	tkThemes trophyKey = "themes"
	// tkWallpaper: a wallpaper chosen (an image or a GIF).
	tkWallpaper trophyKey = "wallpaper"
	// tkHomeEdit: Home edited (a tile or widget added, moved or removed, or
	// the layout switched).
	tkHomeEdit trophyKey = "home_edit"
	// tkColorMode: light or dark mode switched by the user.
	tkColorMode trophyKey = "color_mode"

	// ── The trophy room and the easter eggs ──

	// tkRoomVisit: the trophy room opened (a new visit).
	tkRoomVisit trophyKey = "room_visit"
	// tkRoomDetails (distinct): a trophy whose details were opened (its id).
	tkRoomDetails trophyKey = "room_details"
	// tkReplay: a celebration replayed from the details.
	tkReplay trophyKey = "replay"
	// tkConfetti: the celebration style changed.
	tkConfetti trophyKey = "confetti"
	// tkEasterJuka: J-U-K-A typed on Home (the fireworks).
	tkEasterJuka trophyKey = "easter_juka"

	// ── Written by the playtime tracker (playtime.go) ──

	// tkSessionBest (best): seconds in JukaHub since it last started.
	tkSessionBest trophyKey = "session_best"
	// Seconds by time of day and day of the week.
	tkClockNight   trophyKey = "tod_night"   // midnight to 5 am
	tkClockEarly   trophyKey = "tod_early"   // 5 to 8 am
	tkClockLunch   trophyKey = "tod_lunch"   // noon to 2 pm
	tkClockEvening trophyKey = "tod_evening" // 7 to 11 pm
	tkClockThreeAM trophyKey = "tod_3am"     // the hour after 3 am
	tkClockWeekend trophyKey = "dow_weekend" // Saturdays and Sundays
	tkClockMonday  trophyKey = "dow_monday"  // Mondays
	// tkWeekdays (distinct): the days of the week JukaHub was used on.
	tkWeekdays trophyKey = "weekdays"
	// tkMidnight: in JukaHub (or a game started from it) as one day turned into
	// the next.
	tkMidnight trophyKey = "midnight"
	// The days of the year (1 when JukaHub was used on one).
	tkDayNewYear     trophyKey = "day_new_year"    // 1 January
	tkDayValentine   trophyKey = "day_valentine"   // 14 February
	tkDayLeap        trophyKey = "day_leap"        // 29 February
	tkDayPi          trophyKey = "day_pi"          // 14 March
	tkDayAprilFools  trophyKey = "day_april_fools" // 1 April
	tkDayMayFourth   trophyKey = "day_may_fourth"  // 4 May
	tkDaySolstice    trophyKey = "day_solstice"    // 21 June
	tkDayProgrammers trophyKey = "day_programmers" // the 256th day of the year
	tkDayHalloween   trophyKey = "day_halloween"   // 31 October
	tkDayChristmas   trophyKey = "day_christmas"   // 25 December
	tkDayNewYearsEve trophyKey = "day_nye"         // 31 December
	tkDayFriday13    trophyKey = "day_friday13"    // any Friday the 13th
	tkDayAnniversary trophyKey = "day_anniversary" // a year after the first day
)

// trophySetCap bounds one set. The largest distinct goal is a hundred, so 512
// values is room to spare and keeps the save small (it is copied as JSON on
// every save).
const trophySetCap = 512

// trophyPendingSetsCap bounds the distinct values waiting for a drain. A tick
// sees a handful; this only stops a runaway loop from growing it.
const trophyPendingSetsCap = 512

// trophyPendingSet is one distinct value waiting for the render thread.
type trophyPendingSet struct {
	key  trophyKey
	hash uint32
}

// trophyPending is what the features reported since the last drain.
var trophyPending struct {
	mu    sync.Mutex
	adds  map[trophyKey]int64
	peaks map[trophyKey]int64
	sets  []trophyPendingSet
}

// trophyEvent records one occurrence of key. Any goroutine.
func trophyEvent(key trophyKey) {
	trophyEventN(key, 1)
}

// trophyEventN records n occurrences of key; n below one is ignored. Any
// goroutine.
func trophyEventN(key trophyKey, n int64) {
	if key == "" || n <= 0 {
		return
	}
	trophyPending.mu.Lock()
	if trophyPending.adds == nil {
		trophyPending.adds = make(map[trophyKey]int64, 8)
	}
	trophyPending.adds[key] += n
	trophyPending.mu.Unlock()
}

// trophyEventPeak records v as key's best when it beats the best so far; a
// value below one is ignored. Any goroutine.
func trophyEventPeak(key trophyKey, v int64) {
	if key == "" || v <= 0 {
		return
	}
	trophyPending.mu.Lock()
	if trophyPending.peaks == nil {
		trophyPending.peaks = make(map[trophyKey]int64, 4)
	}
	if v > trophyPending.peaks[key] {
		trophyPending.peaks[key] = v
	}
	trophyPending.mu.Unlock()
}

// trophyEventDistinct records value as one of key's distinct values. Case and
// surrounding space do not make a value new; an empty value is ignored. Any
// goroutine.
func trophyEventDistinct(key trophyKey, value string) {
	if key == "" {
		return
	}
	h, ok := trophyHash(value)
	if !ok {
		return
	}
	trophyPending.mu.Lock()
	if len(trophyPending.sets) < trophyPendingSetsCap {
		trophyPending.sets = append(trophyPending.sets, trophyPendingSet{key: key, hash: h})
	}
	trophyPending.mu.Unlock()
}

// trophyHash is a distinct value's stored form.
func trophyHash(value string) (uint32, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return 0, false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return h.Sum32(), true
}

// trophyDrainEvents moves everything reported since the last drain into d and
// reports whether d changed. Render thread (the owner of the snapshot).
func trophyDrainEvents(d *playtimeData) bool {
	trophyPending.mu.Lock()
	adds, peaks, sets := trophyPending.adds, trophyPending.peaks, trophyPending.sets
	if len(adds) == 0 && len(peaks) == 0 && len(sets) == 0 {
		trophyPending.mu.Unlock()
		return false
	}
	trophyPending.adds, trophyPending.peaks, trophyPending.sets = nil, nil, nil
	trophyPending.mu.Unlock()

	changed := false
	for key, n := range adds {
		if trophyCountAdd(d, key, n) {
			changed = true
		}
	}
	for key, v := range peaks {
		if trophyCountPeak(d, key, v) {
			changed = true
		}
	}
	for _, s := range sets {
		if trophySetAdd(d, s.key, s.hash) {
			changed = true
		}
	}
	return changed
}

// trophyEventsDiscard drops whatever is waiting. Tests use it so one test's
// events cannot land in another's snapshot.
func trophyEventsDiscard() {
	trophyPending.mu.Lock()
	trophyPending.adds, trophyPending.peaks, trophyPending.sets = nil, nil, nil
	trophyPending.mu.Unlock()
}

// trophyCountAdd adds n to key's counter in d. Render thread.
func trophyCountAdd(d *playtimeData, key trophyKey, n int64) bool {
	if n <= 0 || key == "" {
		return false
	}
	if d.Counts == nil {
		d.Counts = map[string]int64{}
	}
	cur := d.Counts[string(key)]
	if cur > (1<<62)-n {
		return false // a counter that large is a bug elsewhere; do not wrap
	}
	d.Counts[string(key)] = cur + n
	return true
}

// trophyCountPeak raises key's best in d to v when v beats it. Render thread.
func trophyCountPeak(d *playtimeData, key trophyKey, v int64) bool {
	if v <= 0 || key == "" {
		return false
	}
	if d.Counts == nil {
		d.Counts = map[string]int64{}
	}
	if v <= d.Counts[string(key)] {
		return false
	}
	d.Counts[string(key)] = v
	return true
}

// trophySetAdd adds a hashed value to key's set in d, keeping the set sorted,
// and reports whether it was new. A full set takes nothing more. Render
// thread.
func trophySetAdd(d *playtimeData, key trophyKey, h uint32) bool {
	if key == "" {
		return false
	}
	if d.Sets == nil {
		d.Sets = map[string][]uint32{}
	}
	set := d.Sets[string(key)]
	i := sort.Search(len(set), func(i int) bool { return set[i] >= h })
	if i < len(set) && set[i] == h {
		return false
	}
	if len(set) >= trophySetCap {
		return false
	}
	set = append(set, 0)
	copy(set[i+1:], set[i:])
	set[i] = h
	d.Sets[string(key)] = set
	return true
}

// trophySetAddValue is trophySetAdd for a value not yet hashed.
func trophySetAddValue(d *playtimeData, key trophyKey, value string) bool {
	h, ok := trophyHash(value)
	if !ok {
		return false
	}
	return trophySetAdd(d, key, h)
}

// trophyCountOf is key's counter or best in d.
func trophyCountOf(d playtimeData, key trophyKey) int64 {
	return d.Counts[string(key)]
}

// trophySetLen is how many distinct values key's set in d holds.
func trophySetLen(d playtimeData, key trophyKey) int64 {
	return int64(len(d.Sets[string(key)]))
}

// trophyFirstFor reports whether value differs from the last one key let
// through, and remembers it. A site whose event can repeat for the same thing —
// a quality switch restarts a Live TV channel, a restart fetches the same
// subtitles again — counts only the first. A→B→A still counts three times.
// Any goroutine.
func trophyFirstFor(key trophyKey, value string) bool {
	trophyLast.mu.Lock()
	defer trophyLast.mu.Unlock()
	if trophyLast.last == nil {
		trophyLast.last = make(map[trophyKey]string, 4)
	}
	if trophyLast.last[key] == value {
		return false
	}
	trophyLast.last[key] = value
	return true
}

// trophyLast is trophyFirstFor's memory: the last value let through per key.
var trophyLast struct {
	mu   sync.Mutex
	last map[trophyKey]string
}
