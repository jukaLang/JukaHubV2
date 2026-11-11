package main

// trophy_set2.go — the second trophy set: 274 trophies here and 11 more
// collection rungs in playtime.go, 285 in all, which take the room from 101 to
// 386.
//
// The first set reads only the clock. This one reads it more finely (the hour
// of the day, the days of the year, the longest runs) and counts things done:
// games launched and finished, minigame scores, blocks placed, tracks, videos,
// pages, files, commands, screenshots… Those counters are reported by the
// features themselves through trophy_events.go and drained into the playtime
// snapshot, so every trophy here is a threshold on a number the snapshot keeps,
// exactly like the first set.
//
// Rows are grouped. A row whose group the first set already has (Lifetime,
// Days, Streaks, Records, This week, Explorer, Screens) joins that group in the
// room (trophyMergeGroups); the new groups follow in this order. The table is
// pure data: playtimeAchievements is a package-level variable built from it,
// so nothing here may call into the rest of the app (an initialization cycle).
//
// Tiers follow the effort, as in the first set: a first time, minutes or a
// handful are bronze; hours, dozens and weeks silver; days of play, hundreds
// or thousands and months gold. Long hauls and the jokes are hidden until
// earned. Scene names a screen the trophy cannot be earned without, so a setup
// without it leaves the trophy out of the room and the platinum; Gate does the
// same for a platform or an account.
//
// Nothing here is earned by starting JukaHub. The rows a start handed out —
// low rungs on the first set's time, day, streak and weekly ladders and on Home
// time, which any old save already met, and the "Start JukaHub N times" counts
// — were taken out on 2026-11-11 (trophyRetiredIDs in playtime.go lists them,
// and their ids may not come back).

// t2Count is a row that counts a key (Unit says how progress reads).
func t2Count(id, title, desc, group string, tier trophyTier, key trophyKey, goal int64, unit string) playtimeAchievement {
	a := trophyNew(id, title, desc, group, tier, trophyMetricCount, goal)
	a.Key, a.Unit = key, unit
	return a
}

// t2Best is a row on a key's best.
func t2Best(id, title, desc, group string, tier trophyTier, key trophyKey, goal int64, unit string) playtimeAchievement {
	a := trophyNew(id, title, desc, group, tier, trophyMetricBest, goal)
	a.Key, a.Unit = key, unit
	return a
}

// t2Set is a row on how many distinct values a key has seen.
func t2Set(id, title, desc, group string, tier trophyTier, key trophyKey, goal int64, unit string) playtimeAchievement {
	a := trophyNew(id, title, desc, group, tier, trophyMetricSet, goal)
	a.Key, a.Unit = key, unit
	return a
}

// t2Screen is a per-screen row graded like the first set's table: a visit of a
// few seconds is "Open …", an hour or more a habit.
func t2Screen(id, scene, title, desc string, seconds int64) playtimeAchievement {
	a := trophyNew(id, title, desc, "Screens", trophySceneTier(seconds), trophyMetricScene, seconds)
	a.Scene = scene
	return a
}

// secret marks a row hidden until earned.
func (a playtimeAchievement) secret() playtimeAchievement {
	a.Hidden = true
	return a
}

// needs ties a row to the screen it cannot be earned without.
func (a playtimeAchievement) needs(scene string) playtimeAchievement {
	a.Scene = scene
	return a
}

// gated ties a row to a platform or an account (trophyGateOpen).
func (a playtimeAchievement) gated(gate string) playtimeAchievement {
	a.Gate = gate
	return a
}

// bonus marks a row the platinum does not wait for: it needs someone or
// something else (another device or player, more controllers, a date years
// away).
func (a playtimeAchievement) bonus() playtimeAchievement {
	a.Bonus = true
	return a
}

// trophyVisitSeconds is how long a screen must be open to count as visited:
// long enough that passing through it on the way elsewhere does not.
const trophyVisitSeconds = 10

// trophySetTwo is the second set's table.
func trophySetTwo() []playtimeAchievement {
	const (
		m  = int64(60)
		h  = int64(3600)
		gb = int64(1024) // MiB
	)
	return []playtimeAchievement{
		// ── Lifetime ──
		trophyNew("twenty_hours", "Part of the Furniture", "Spend 20 hours in JukaHub", "Lifetime", trophySilver, trophyMetricTotal, 20*h),
		trophyNew("seventyfive_hours", "Old Hand", "Spend 75 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 75*h),
		trophyNew("hundred_fifty_hours", "Seasoned", "Spend 150 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 150*h),
		trophyNew("three_hundred_hours", "Lifer", "Spend 300 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 300*h),
		trophyNew("seven_fifty_hours", "Hall of Famer", "Spend 750 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 750*h).secret(),

		// ── Days (every day ever) ──
		trophyNew("fortyfive_days", "Six Weeks In", "Use JukaHub on 45 days", "Days", trophySilver, trophyMetricLifeDays, 45),
		trophyNew("days_120", "Third of a Year", "Use JukaHub on 120 days", "Days", trophyGold, trophyMetricLifeDays, 120),
		trophyNew("days_180", "Half a Year", "Use JukaHub on 180 days", "Days", trophyGold, trophyMetricLifeDays, 180),
		trophyNew("days_365", "Full Orbit", "Use JukaHub on 365 days", "Days", trophyGold, trophyMetricLifeDays, 365),

		// ── Streaks (the longest run ever) ──
		trophyNew("run_5", "Workweek", "Use JukaHub 5 days in a row", "Streaks", trophyBronze, trophyMetricBestStreak, 5),
		trophyNew("run_10", "Perfect Ten", "Use JukaHub 10 days in a row", "Streaks", trophySilver, trophyMetricBestStreak, 10),
		trophyNew("run_21", "Habit Formed", "Use JukaHub 21 days in a row", "Streaks", trophySilver, trophyMetricBestStreak, 21),
		trophyNew("run_100", "Centurion", "Use JukaHub 100 days in a row", "Streaks", trophyGold, trophyMetricBestStreak, 100).secret(),

		// ── Records ──
		t2Best("session_1h", "One Sitting", "Spend 1 hour in JukaHub in one go", "Records", trophyBronze, tkSessionBest, h, "time"),
		t2Best("session_3h", "Glued to the Screen", "Spend 3 hours in JukaHub in one go", "Records", trophySilver, tkSessionBest, 3*h, "time"),
		t2Best("session_6h", "No Pause Button", "Spend 6 hours in JukaHub in one go", "Records", trophyGold, tkSessionBest, 6*h, "time").secret(),

		// ── This week ──
		trophyNew("week_15h", "Busy Bee", "Spend 15 hours in the last 7 days", "This week", trophySilver, trophyMetricWeek, 15*h),
		trophyNew("week_30h", "Second Job", "Spend 30 hours in the last 7 days", "This week", trophyGold, trophyMetricWeek, 30*h).secret(),

		// ── Explorer ──
		trophyNew("explorer_60", "No Stone Unturned", "Use 60 different screens", "Explorer", trophyGold, trophyMetricScreens, 60),

		// ── Screens: a visit to each screen the first set left out, and longer
		// habits on the big ones ──
		t2Screen("visit_wifi", wifiScrSceneName, "Signal Seeker", "Open the Wi-Fi screen", trophyVisitSeconds),
		t2Screen("visit_bluetooth", btSceneName, "Wireless Wonder", "Open the Bluetooth screen", trophyVisitSeconds).gated("bluetooth"),
		t2Screen("visit_backup", bkupSceneName, "Safety Net", "Open Backup & restore", trophyVisitSeconds),
		t2Screen("visit_report_bug", bugrepSceneName, "Bug Spotter", "Open Report a bug", trophyVisitSeconds),
		t2Screen("visit_keyboard", vkSettingsSceneName, "Key Change", "Open the keyboard settings", trophyVisitSeconds),
		t2Screen("visit_voice", voiceSceneName, "Say Something", "Open Voice command", trophyVisitSeconds),
		t2Screen("visit_packs", xpackSceneName, "Pack Leader", "Open Experience packs", trophyVisitSeconds),
		t2Screen("visit_nearby", nearbySceneName, "Neighbourhood Watch", "Open Nearby", trophyVisitSeconds),
		t2Screen("visit_party", partySceneName, "Party Starter", "Open Watch Party", trophyVisitSeconds),
		t2Screen("visit_devices", peerSceneName, "Device Whisperer", "Open Devices", trophyVisitSeconds),
		t2Screen("visit_apk_store", apkStoreSceneName, "Window Shopping", "Open the APK Store", trophyVisitSeconds).gated("handheld"),
		t2Screen("visit_rom_manager", rommSceneName, "ROM Ranger", "Open the ROM manager", trophyVisitSeconds),
		t2Screen("visit_speed_test", "NetSpeed", "Speedometer", "Open Speed test", trophyVisitSeconds),
		t2Screen("visit_trophies", playtimeSceneName, "Trophy Case", "Open Trophies", trophyVisitSeconds),
		t2Screen("tube_25h", "Tube", "Season Finale", "Spend 25 hours in Media", 25*h),
		t2Screen("music_5h", "MusicPlayer", "Mixtape", "Spend 5 hours in Music", 5*h),
		t2Screen("browser_25h", browserSceneName, "Netizen", "Browse for 25 hours", 25*h),
		t2Screen("jukaland_1h", "JukaLand", "Settler", "Spend 1 hour in JukaLand", h),

		// ── Clock ──
		t2Count("night_owl", "Night Owl", "Spend 30 minutes from midnight to 5 am", "Clock", trophyBronze, tkClockNight, 30*m, "time"),
		t2Count("night_shift", "Creature of the Night", "Spend 10 hours from midnight to 5 am", "Clock", trophyGold, tkClockNight, 10*h, "time").secret(),
		t2Count("witching_hour", "Witching Hour", "Use JukaHub between 3 and 4 am", "Clock", trophyBronze, tkClockThreeAM, m, "time").secret(),
		t2Count("dawn_patrol", "Dawn Patrol", "Spend 30 minutes between 5 and 8 am", "Clock", trophyBronze, tkClockEarly, 30*m, "time"),
		t2Count("sunrise_regular", "Sunrise Regular", "Spend 5 hours between 5 and 8 am", "Clock", trophySilver, tkClockEarly, 5*h, "time"),
		t2Count("lunch_break", "Lunch Break", "Spend 1 hour between noon and 2 pm", "Clock", trophyBronze, tkClockLunch, h, "time"),
		t2Count("evening_ritual", "Evening Ritual", "Spend 10 hours between 7 and 11 pm", "Clock", trophySilver, tkClockEvening, 10*h, "time"),
		t2Count("weekend_warrior", "Weekend Warrior", "Spend 5 hours on weekends", "Clock", trophyBronze, tkClockWeekend, 5*h, "time"),
		t2Count("weekend_legend", "Weekend Legend", "Spend 50 hours on weekends", "Clock", trophyGold, tkClockWeekend, 50*h, "time"),
		t2Count("case_of_the_mondays", "Case of the Mondays", "Spend 1 hour on Mondays", "Clock", trophyBronze, tkClockMonday, h, "time").secret(),
		t2Set("every_weekday", "Seven Days a Week", "Use JukaHub on every day of the week", "Clock", trophyBronze, tkWeekdays, 7, "days"),
		t2Count("midnight_crossing", "Midnight Crossing", "Be in JukaHub as the date changes", "Clock", trophyBronze, tkMidnight, 1, "").secret(),

		// ── Calendar ──
		t2Count("day_new_year", "Fresh Start", "Use JukaHub on 1 January", "Calendar", trophyBronze, tkDayNewYear, 1, ""),
		t2Count("day_valentine", "Be Mine", "Use JukaHub on Valentine's Day", "Calendar", trophyBronze, tkDayValentine, 1, ""),
		t2Count("day_leap", "Leap of Faith", "Use JukaHub on 29 February", "Calendar", trophySilver, tkDayLeap, 1, "").bonus(),
		t2Count("day_pi", "Irrational Fan", "Use JukaHub on Pi Day, 14 March", "Calendar", trophyBronze, tkDayPi, 1, ""),
		t2Count("day_april_fools", "Nothing Suspicious", "Use JukaHub on 1 April", "Calendar", trophyBronze, tkDayAprilFools, 1, ""),
		t2Count("day_may_fourth", "May the Fourth", "Use JukaHub on 4 May", "Calendar", trophyBronze, tkDayMayFourth, 1, ""),
		t2Count("day_solstice", "Longest Day", "Use JukaHub on 21 June", "Calendar", trophyBronze, tkDaySolstice, 1, ""),
		t2Count("day_programmers", "Day 256", "Use JukaHub on the 256th day of a year", "Calendar", trophyBronze, tkDayProgrammers, 1, ""),
		t2Count("day_halloween", "Spooky Session", "Use JukaHub on Halloween", "Calendar", trophyBronze, tkDayHalloween, 1, ""),
		t2Count("day_christmas", "Unwrapped", "Use JukaHub on Christmas Day", "Calendar", trophyBronze, tkDayChristmas, 1, ""),
		t2Count("day_new_years_eve", "Countdown", "Use JukaHub on 31 December", "Calendar", trophyBronze, tkDayNewYearsEve, 1, ""),
		t2Count("day_friday13", "Lucky Thirteen", "Use JukaHub on a Friday the 13th", "Calendar", trophyBronze, tkDayFriday13, 1, ""),
		t2Count("day_anniversary", "Happy Hubiversary", "Use JukaHub a year after your first day", "Calendar", trophyGold, tkDayAnniversary, 1, ""),

		// ── Games: launched from JukaHub ──
		t2Count("launch_1", "Press Start", "Launch a game from JukaHub", "Games", trophyBronze, tkGameLaunch, 1, "games"),
		t2Count("launch_10", "Insert Coin", "Launch 10 games", "Games", trophyBronze, tkGameLaunch, 10, "games"),
		t2Count("launch_50", "Continue?", "Launch 50 games", "Games", trophySilver, tkGameLaunch, 50, "games"),
		t2Count("launch_100", "Boot Loop", "Launch 100 games", "Games", trophySilver, tkGameLaunch, 100, "games"),
		t2Count("launch_250", "Cartridge Blower", "Launch 250 games", "Games", trophyGold, tkGameLaunch, 250, "games"),
		t2Set("titles_3", "Sampler", "Play 3 different games", "Games", trophyBronze, tkGameTitles, 3, "games"),
		t2Set("titles_10", "Variety Pack", "Play 10 different games", "Games", trophyBronze, tkGameTitles, 10, "games"),
		t2Set("titles_25", "Game Shelf", "Play 25 different games", "Games", trophySilver, tkGameTitles, 25, "games"),
		t2Set("titles_50", "Collector's Edition", "Play 50 different games", "Games", trophyGold, tkGameTitles, 50, "games"),
		t2Set("systems_2", "Multi-Platform", "Play games on 2 different systems", "Games", trophyBronze, tkGameSystems, 2, "systems"),
		t2Set("systems_3", "Console Hopper", "Play games on 3 different systems", "Games", trophyBronze, tkGameSystems, 3, "systems"),
		t2Set("systems_5", "Generations", "Play games on 5 different systems", "Games", trophySilver, tkGameSystems, 5, "systems"),
		t2Set("systems_8", "Console Wars Veteran", "Play games on 8 different systems", "Games", trophySilver, tkGameSystems, 8, "systems"),
		trophyNew("game_best_1h", "Hooked", "Play one game for 1 hour", "Games", trophyBronze, trophyMetricGameBest, h),
		trophyNew("game_best_10h", "Main Quest", "Play one game for 10 hours", "Games", trophySilver, trophyMetricGameBest, 10*h),
		trophyNew("game_best_50h", "Lifelong Save File", "Play one game for 50 hours", "Games", trophyGold, trophyMetricGameBest, 50*h),
		t2Count("game_time_5h", "Joypad Jockey", "Play games from JukaHub for 5 hours", "Games", trophyBronze, tkGameSecs, 5*h, "time"),
		t2Count("game_time_25h", "Controller Callus", "Play games from JukaHub for 25 hours", "Games", trophySilver, tkGameSecs, 25*h, "time"),
		t2Count("game_time_100h", "Hundred Hour Hero", "Play games from JukaHub for 100 hours", "Games", trophyGold, tkGameSecs, 100*h, "time"),
		t2Count("complete_1", "The End", "Mark a game as completed", "Games", trophyBronze, tkGameComplete, 1, "games"),
		t2Count("complete_5", "Credits Roll", "Mark 5 games as completed", "Games", trophySilver, tkGameComplete, 5, "games"),
		t2Count("complete_10", "Finisher", "Mark 10 games as completed", "Games", trophyGold, tkGameComplete, 10, "games"),
		t2Count("random_1", "Feeling Lucky", "Start a random game", "Games", trophyBronze, tkGameRandom, 1, "games"),
		t2Count("netplay_1", "Link Cable", "Host or join a netplay game", "Games", trophyBronze, tkNetplay, 1, "sessions"),
		t2Count("local_play_1", "Couch Co-op", "Start a local multiplayer session", "Games", trophyBronze, tkLocalPlay, 1, "sessions").needs(hotseatScene).bonus(),
		t2Count("remote_play_1", "Remote Control", "Stream a PlayStation with Remote Play", "Games", trophyBronze, tkRemotePlay, 1, "streams").gated("psremote").bonus(),
		t2Count("remote_play_10", "Living Room Link", "Stream a PlayStation 10 times", "Games", trophySilver, tkRemotePlay, 10, "streams").gated("psremote").bonus(),
		t2Count("emu_install_1", "Plug and Play", "Install an emulator", "Games", trophyBronze, tkEmuInstall, 1, "installs"),
		t2Count("portmaster_1", "Port of Call", "Install or open PortMaster", "Games", trophyBronze, tkPortMaster, 1, "").gated("handheld"),
		t2Count("save_backup_1", "Insurance Policy", "Back up a game save", "Games", trophyBronze, tkSaveBackup, 1, "backups"),
		t2Count("recording_1", "Director's Cut", "Get a time-lapse of a game you play", "Games", trophyBronze, tkRecording, 1, "time-lapses").gated("handheld"),
		t2Count("box_art_10", "Cover Story", "Get box art for 10 games", "Games", trophyBronze, tkBoxArt, 10, "covers"),
		t2Count("ra_1", "Retro Achiever", "Unlock a RetroAchievement", "Games", trophyBronze, tkRAUnlock, 1, "unlocks").gated("ra"),
		t2Count("ra_10", "Cheevo Collector", "Unlock 10 RetroAchievements", "Games", trophySilver, tkRAUnlock, 10, "unlocks").gated("ra"),
		t2Count("ra_50", "Retro Royalty", "Unlock 50 RetroAchievements", "Games", trophyGold, tkRAUnlock, 50, "unlocks").gated("ra"),

		// ── Arcade: the built-in minigames ──
		t2Count("arcade_10", "Coin-Op", "Start 10 minigames", "Arcade", trophyBronze, tkArcadePlay, 10, "games").needs("Games"),
		t2Count("arcade_100", "Arcade Rat", "Start 100 minigames", "Arcade", trophySilver, tkArcadePlay, 100, "games").needs("Games"),
		t2Set("arcade_all", "Full Cabinet", "Play all six minigames", "Arcade", trophyBronze, tkArcadeGames, 6, "minigames").needs("Games"),
		t2Best("snake_100", "Snack Time", "Score 100 in Snake", "Arcade", trophyBronze, tkSnakeBest, 100, "points").needs("Games"),
		t2Best("snake_250", "Long Boi", "Score 250 in Snake", "Arcade", trophySilver, tkSnakeBest, 250, "points").needs("Games"),
		t2Best("snake_500", "Ouroboros", "Score 500 in Snake", "Arcade", trophyGold, tkSnakeBest, 500, "points").needs("Games"),
		t2Count("tetris_10", "Line Cleaner", "Clear 10 lines in Tetris", "Arcade", trophyBronze, tkTetrisLines, 10, "lines").needs("Games"),
		t2Count("tetris_100", "Block Party", "Clear 100 lines in Tetris", "Arcade", trophySilver, tkTetrisLines, 100, "lines").needs("Games"),
		t2Count("tetris_1000", "Stack Overflow", "Clear 1,000 lines in Tetris", "Arcade", trophyGold, tkTetrisLines, 1000, "lines").needs("Games"),
		t2Count("tetris_quad", "Four Score", "Clear 4 lines at once in Tetris", "Arcade", trophyBronze, tkTetrisQuad, 1, "").needs("Games"),
		t2Best("tile_256", "Getting Somewhere", "Make a 256 tile in 2048", "Arcade", trophyBronze, tk2048Tile, 256, "").needs("Games"),
		t2Best("tile_1024", "Ten Twenty-Four", "Make a 1024 tile in 2048", "Arcade", trophySilver, tk2048Tile, 1024, "").needs("Games"),
		t2Best("tile_2048", "The Big One", "Make the 2048 tile", "Arcade", trophyGold, tk2048Tile, 2048, "").needs("Games"),
		t2Count("pong_point_1", "Paddle Power", "Get the ball past the AI in Pong", "Arcade", trophyBronze, tkPongPoint, 1, "goals").needs("Games"),
		t2Count("pong_point_10", "Table Tennis Pro", "Get 10 balls past the AI in Pong", "Arcade", trophySilver, tkPongPoint, 10, "goals").needs("Games"),
		t2Count("mines_1", "Sweeper", "Clear a Minesweeper board", "Arcade", trophyBronze, tkMinesWin, 1, "boards").needs("Games"),
		t2Count("mines_10", "Bomb Squad", "Clear 10 Minesweeper boards", "Arcade", trophySilver, tkMinesWin, 10, "boards").needs("Games"),
		t2Best("flappy_5", "First Flight", "Pass 5 pipes in one Flappy run", "Arcade", trophyBronze, tkFlappyBest, 5, "pipes").needs("Games"),
		t2Best("flappy_10", "Flight School", "Pass 10 pipes in one Flappy run", "Arcade", trophyBronze, tkFlappyBest, 10, "pipes").needs("Games"),
		t2Best("flappy_25", "Frequent Flapper", "Pass 25 pipes in one Flappy run", "Arcade", trophySilver, tkFlappyBest, 25, "pipes").needs("Games"),

		// ── JukaLand ──
		t2Count("jland_place_10", "First Brick", "Place 10 blocks in JukaLand", "JukaLand", trophyBronze, tkJlandPlace, 10, "blocks").needs("JukaLand"),
		t2Count("jland_place_1000", "Master Builder", "Place 1,000 blocks in JukaLand", "JukaLand", trophySilver, tkJlandPlace, 1000, "blocks").needs("JukaLand"),
		t2Count("jland_place_10000", "Megastructure", "Place 10,000 blocks in JukaLand", "JukaLand", trophyGold, tkJlandPlace, 10000, "blocks").needs("JukaLand").secret(),
		t2Count("jland_break_10", "Demolition Crew", "Break 10 blocks in JukaLand", "JukaLand", trophyBronze, tkJlandBreak, 10, "blocks").needs("JukaLand"),
		t2Count("jland_break_1000", "Excavator", "Break 1,000 blocks in JukaLand", "JukaLand", trophySilver, tkJlandBreak, 1000, "blocks").needs("JukaLand"),
		t2Count("jland_break_10000", "Strip Miner", "Break 10,000 blocks in JukaLand", "JukaLand", trophyGold, tkJlandBreak, 10000, "blocks").needs("JukaLand").secret(),
		t2Count("jland_ore_1", "Shiny!", "Mine an ore block in JukaLand", "JukaLand", trophyBronze, tkJlandOre, 1, "ores").needs("JukaLand"),
		t2Count("jland_ore_50", "Mother Lode", "Mine 50 ore blocks in JukaLand", "JukaLand", trophySilver, tkJlandOre, 50, "ores").needs("JukaLand"),
		t2Count("jland_world_1", "New Horizons", "Create a new JukaLand world", "JukaLand", trophyBronze, tkJlandWorld, 1, "worlds").needs("JukaLand"),
		t2Set("jland_ore_kinds", "Rock Collector", "Mine all four kinds of ore in JukaLand", "JukaLand", trophySilver, tkJlandOreKinds, 4, "kinds").needs("JukaLand"),

		// ── Music ──
		t2Count("track_1", "Needle Drop", "Play a track", "Music", trophyBronze, tkTrackPlay, 1, "tracks"),
		t2Count("track_10", "Shuffle Mode", "Play 10 tracks", "Music", trophyBronze, tkTrackPlay, 10, "tracks"),
		t2Count("track_100", "Heavy Rotation", "Play 100 tracks", "Music", trophySilver, tkTrackPlay, 100, "tracks"),
		t2Count("track_500", "Crate Digger", "Play 500 tracks", "Music", trophySilver, tkTrackPlay, 500, "tracks"),
		t2Count("track_1000", "Gold Record", "Play 1,000 tracks", "Music", trophyGold, tkTrackPlay, 1000, "tracks"),
		t2Count("track_5000", "Diamond Record", "Play 5,000 tracks", "Music", trophyGold, tkTrackPlay, 5000, "tracks").secret(),
		t2Set("artists_5", "Eclectic", "Play tracks by 5 artists", "Music", trophyBronze, tkArtists, 5, "artists"),
		t2Set("artists_25", "Festival Lineup", "Play tracks by 25 artists", "Music", trophySilver, tkArtists, 25, "artists"),
		t2Set("artists_100", "Music Encyclopedia", "Play tracks by 100 artists", "Music", trophyGold, tkArtists, 100, "artists"),
		t2Count("playlist_1", "Curated", "Create a playlist", "Music", trophyBronze, tkPlaylist, 1, "playlists"),
		t2Count("playlist_5", "Mixmaster", "Create 5 playlists", "Music", trophyBronze, tkPlaylist, 5, "playlists"),
		t2Count("lyrics_1", "Sing Along", "Show a track's .lrc lyrics", "Music", trophyBronze, tkLyrics, 1, "").secret().bonus(),
		t2Count("podcast_1", "Tuned In", "Play a podcast episode", "Music", trophyBronze, tkPodcastPlay, 1, "episodes"),
		t2Count("podcast_10", "Episode Binger", "Play 10 podcast episodes", "Music", trophySilver, tkPodcastPlay, 10, "episodes"),
		t2Count("podcast_sub_1", "Follow the Show", "Subscribe to a podcast", "Music", trophyBronze, tkPodcastSub, 1, ""),

		// ── Video ──
		t2Count("video_1", "Roll Film", "Watch a video", "Video", trophyBronze, tkVideoPlay, 1, "videos"),
		t2Count("video_10", "Matinee", "Watch 10 videos", "Video", trophyBronze, tkVideoPlay, 10, "videos"),
		t2Count("video_100", "Film Buff", "Watch 100 videos", "Video", trophySilver, tkVideoPlay, 100, "videos"),
		t2Count("video_500", "Cinephile", "Watch 500 videos", "Video", trophyGold, tkVideoPlay, 500, "videos"),
		t2Count("shorts_10_watched", "Snack Size", "Watch 10 Shorts", "Video", trophyBronze, tkShorts, 10, "Shorts"),
		t2Count("shorts_100_watched", "Thumb Workout", "Watch 100 Shorts", "Video", trophySilver, tkShorts, 100, "Shorts"),
		t2Count("shorts_1000_watched", "Infinite Scroll", "Watch 1,000 Shorts", "Video", trophyGold, tkShorts, 1000, "Shorts"),
		t2Count("video_search_1", "Seek and Find", "Search for a video", "Video", trophyBronze, tkVideoSearch, 1, "searches"),
		t2Count("subtitles_1", "Read Along", "Turn on subtitles for a video", "Video", trophyBronze, tkSubtitles, 1, ""),
		t2Count("tv_play_1", "Live and Direct", "Watch a live TV channel", "Video", trophyBronze, tkTVPlay, 1, "channels"),
		t2Set("tv_channels_5", "Flip Side", "Watch 5 different channels", "Video", trophyBronze, tkTVChannels, 5, "channels"),
		t2Set("tv_channels_25", "Cable Guy", "Watch 25 different channels", "Video", trophySilver, tkTVChannels, 25, "channels"),
		t2Count("cast_1", "Big Screen", "Share your screen", "Video", trophyBronze, tkCast, 1, ""),
		t2Count("watch_party_1", "Popcorn Ready", "Join or host a Watch Party", "Video", trophyBronze, tkWatchParty, 1, "parties").needs(partySceneName),

		// ── Downloads ──
		t2Count("download_1", "Saved for Later", "Finish a download", "Downloads", trophyBronze, tkDownload, 1, "downloads"),
		t2Count("download_10", "Offline Ready", "Finish 10 downloads", "Downloads", trophyBronze, tkDownload, 10, "downloads"),
		t2Count("download_50", "Hoarder", "Finish 50 downloads", "Downloads", trophySilver, tkDownload, 50, "downloads"),
		t2Count("download_250", "Data Dragon", "Finish 250 downloads", "Downloads", trophyGold, tkDownload, 250, "downloads"),
		t2Count("download_1gb", "First Gigabyte", "Download 1 GB", "Downloads", trophyBronze, tkDownloadMB, gb, "MB"),
		t2Count("download_10gb", "Bandwidth Baron", "Download 10 GB", "Downloads", trophySilver, tkDownloadMB, 10*gb, "MB"),
		t2Count("audio_extract_1", "Sound Only", "Extract the audio of a video", "Downloads", trophyBronze, tkAudioExtract, 1, ""),

		// ── Web ──
		t2Count("page_1", "Hello, Internet", "Load a web page", "Web", trophyBronze, tkPageVisit, 1, "pages").needs(browserSceneName),
		t2Count("page_10", "Surfer", "Load 10 web pages", "Web", trophyBronze, tkPageVisit, 10, "pages").needs(browserSceneName),
		t2Count("page_100", "Hyperlinked", "Load 100 web pages", "Web", trophySilver, tkPageVisit, 100, "pages").needs(browserSceneName),
		t2Count("page_1000", "Superhighway", "Load 1,000 web pages", "Web", trophyGold, tkPageVisit, 1000, "pages").needs(browserSceneName),
		t2Count("page_10000", "Web Legend", "Load 10,000 web pages", "Web", trophyGold, tkPageVisit, 10000, "pages").needs(browserSceneName).secret(),
		t2Set("sites_5", "Tourist", "Visit 5 different sites", "Web", trophyBronze, tkSites, 5, "sites").needs(browserSceneName),
		t2Set("sites_25", "Globetrotter", "Visit 25 different sites", "Web", trophySilver, tkSites, 25, "sites").needs(browserSceneName),
		t2Set("sites_100", "World Wide Wanderer", "Visit 100 different sites", "Web", trophyGold, tkSites, 100, "sites").needs(browserSceneName),
		t2Count("bookmark_1", "Dog-Eared", "Add a bookmark", "Web", trophyBronze, tkBookmark, 1, "bookmarks").needs(browserSceneName),
		t2Count("bookmark_10", "Well Marked", "Add 10 bookmarks", "Web", trophyBronze, tkBookmark, 10, "bookmarks").needs(browserSceneName),
		t2Count("ads_100", "Pop-Up Blocker", "Block 100 ads", "Web", trophyBronze, tkAdsBlocked, 100, "ads").needs(browserSceneName),
		t2Count("ads_10000", "Ad Astra", "Block 10,000 ads", "Web", trophySilver, tkAdsBlocked, 10000, "ads").needs(browserSceneName),
		t2Count("find_1", "Needle in a Haystack", "Find text on a page", "Web", trophyBronze, tkFindInPage, 1, "").needs(browserSceneName),

		// ── Social ──
		t2Count("chat_1", "First Words", "Send a chat message", "Social", trophyBronze, tkChatSend, 1, "messages").needs(chatSceneName),
		t2Count("chat_10", "Small Talk", "Send 10 chat messages", "Social", trophyBronze, tkChatSend, 10, "messages").needs(chatSceneName),
		t2Count("chat_100", "Social Butterfly", "Send 100 chat messages", "Social", trophySilver, tkChatSend, 100, "messages").needs(chatSceneName),
		t2Count("chat_1000", "Chatterbox", "Send 1,000 chat messages", "Social", trophyGold, tkChatSend, 1000, "messages").needs(chatSceneName).secret(),
		t2Count("chat_file_1", "Attachment", "Send a file in Chat", "Social", trophyBronze, tkChatFile, 1, "files").needs(chatSceneName),
		t2Count("ai_1", "Ask Me Anything", "Ask the AI a question", "Social", trophyBronze, tkAIAsk, 1, "questions").gated("ai"),
		t2Count("ai_25", "Curious Mind", "Ask the AI 25 questions", "Social", trophySilver, tkAIAsk, 25, "questions").gated("ai"),
		t2Count("peer_pair_1", "Better Together", "Pair with another device", "Social", trophyBronze, tkPeerPair, 1, "").bonus(),
		t2Count("transfer_1", "Special Delivery", "Send or receive a file between devices", "Social", trophyBronze, tkFileTransfer, 1, "files"),
		t2Count("friend_1", "Plus One", "Save a Watch Party room", "Social", trophyBronze, tkFriend, 1, "rooms").needs(partySceneName).bonus(),

		// ── Files ──
		t2Count("copy_1", "Copycat", "Copy a file", "Files", trophyBronze, tkFileCopy, 1, "items").needs("FileExplorer"),
		t2Count("copy_100", "Duplicator", "Copy 100 items", "Files", trophySilver, tkFileCopy, 100, "items").needs("FileExplorer"),
		t2Count("move_1", "Moving Day", "Move a file", "Files", trophyBronze, tkFileMove, 1, "items").needs("FileExplorer"),
		t2Count("move_100", "Heavy Lifter", "Move 100 items", "Files", trophySilver, tkFileMove, 100, "items").needs("FileExplorer"),
		t2Count("delete_1", "Clean Slate", "Delete a file", "Files", trophyBronze, tkFileDelete, 1, "items").needs("FileExplorer"),
		t2Count("delete_100", "Spring Cleaning", "Delete 100 items", "Files", trophySilver, tkFileDelete, 100, "items").needs("FileExplorer"),
		t2Count("rename_1", "Name Tag", "Rename a file", "Files", trophyBronze, tkFileRename, 1, "items").needs("FileExplorer"),
		t2Count("folder_1", "New Folder (2)", "Create a folder", "Files", trophyBronze, tkFolderNew, 1, "folders").needs("FileExplorer"),
		t2Count("file_edit_1", "Red Pen", "Edit and save a text file", "Files", trophyBronze, tkFileEdit, 1, "files").needs("FileExplorer"),
		t2Count("extract_1", "Unboxing", "Extract an archive", "Files", trophyBronze, tkExtract, 1, "archives"),
		t2Count("extract_10", "Unzipped", "Extract 10 archives", "Files", trophySilver, tkExtract, 10, "archives"),
		t2Count("image_10", "Slideshow", "View 10 images", "Files", trophyBronze, tkImageView, 10, "images"),

		// ── Tools ──
		t2Count("term_1", "Hello, Shell", "Run a Terminal command", "Tools", trophyBronze, tkTermCmd, 1, "commands").needs("Terminal"),
		t2Count("term_25", "Command Line", "Run 25 Terminal commands", "Tools", trophyBronze, tkTermCmd, 25, "commands").needs("Terminal"),
		t2Count("term_100", "Root Access", "Run 100 Terminal commands", "Tools", trophySilver, tkTermCmd, 100, "commands").needs("Terminal"),
		t2Count("term_500", "Sysadmin", "Run 500 Terminal commands", "Tools", trophyGold, tkTermCmd, 500, "commands").needs("Terminal"),
		t2Count("note_1", "Jot It Down", "Save a note", "Tools", trophyBronze, tkNoteSave, 1, "saves").needs("Notes"),
		t2Count("note_10", "Notebook", "Save notes 10 times", "Tools", trophyBronze, tkNoteSave, 10, "saves").needs("Notes"),
		t2Count("calc_1", "Two Plus Two", "Do a calculation", "Tools", trophyBronze, tkCalc, 1, "calculations").needs("Calculator"),
		t2Count("calc_50", "Mathlete", "Do 50 calculations", "Tools", trophySilver, tkCalc, 50, "calculations").needs("Calculator"),
		t2Count("unit_1", "Metric Mind", "Convert a unit", "Tools", trophyBronze, tkUnitConvert, 1, "conversions").needs("UnitConverter"),
		t2Count("dict_1", "Look It Up", "Look up a word", "Tools", trophyBronze, tkDictLookup, 1, "words").needs("Dictionary"),
		t2Count("dict_25", "Lexicon", "Look up 25 words", "Tools", trophySilver, tkDictLookup, 25, "words").needs("Dictionary"),
		t2Count("pdf_open_1", "Open Book", "Open a PDF", "Tools", trophyBronze, tkPdfOpen, 1, "PDFs").needs(pdfvSceneName),
		t2Count("pdf_page_100", "Chapter and Verse", "Turn 100 pages in the reader", "Tools", trophyBronze, tkPdfPage, 100, "pages").needs(pdfvSceneName),
		t2Count("pdf_page_1000", "Speed Reader", "Turn 1,000 pages in the reader", "Tools", trophySilver, tkPdfPage, 1000, "pages").needs(pdfvSceneName),
		t2Count("benchmark_1", "Benchmarked", "Run a benchmark", "Tools", trophyBronze, tkBenchmark, 1, "runs").needs("Benchmark"),
		t2Count("disk_scan_1", "Where Did It Go?", "Scan your storage", "Tools", trophyBronze, tkDiskScan, 1, "scans").needs("DiskSpace"),
		t2Count("speed_test_1", "Speed Check", "Run a speed test", "Tools", trophyBronze, tkSpeedTest, 1, "tests").needs("NetSpeed"),
		t2Best("speed_25", "Broadband", "Reach 25 Mbit/s in a speed test", "Tools", trophyBronze, tkSpeedBest, 25, "Mbit/s").needs("NetSpeed"),
		t2Best("speed_100", "Fast Lane", "Reach 100 Mbit/s in a speed test", "Tools", trophySilver, tkSpeedBest, 100, "Mbit/s").needs("NetSpeed"),
		t2Best("speed_300", "Fibre Optic", "Reach 300 Mbit/s in a speed test", "Tools", trophyGold, tkSpeedBest, 300, "Mbit/s").needs("NetSpeed").secret().bonus(),
		t2Count("net_tool_1", "Ping!", "Run a network tool", "Tools", trophyBronze, tkNetTool, 1, "runs").needs(netToolsSceneName),
		t2Count("pomodoro_1", "Tomato Timer", "Complete a Pomodoro", "Tools", trophyBronze, tkPomodoro, 1, "Pomodoros").needs("Pomodoro"),
		t2Count("pomodoro_10", "In the Zone", "Complete 10 Pomodoros", "Tools", trophySilver, tkPomodoro, 10, "Pomodoros").needs("Pomodoro"),
		t2Count("pomodoro_50", "Productivity Guru", "Complete 50 Pomodoros", "Tools", trophyGold, tkPomodoro, 50, "Pomodoros").needs("Pomodoro"),
		t2Count("alarm_set_1", "Wake-Up Call", "Set an alarm", "Tools", trophyBronze, tkAlarmSet, 1, "alarms").needs("Alarm"),
		t2Count("alarm_wake_1", "Rise and Shine", "Get up to an alarm", "Tools", trophyBronze, tkAlarmWake, 1, "alarms").needs("Alarm"),
		t2Count("alarm_wake_30", "Morning Person", "Get up to 30 alarms", "Tools", trophySilver, tkAlarmWake, 30, "alarms").needs("Alarm"),
		t2Count("snooze_10", "Five More Minutes", "Snooze an alarm 10 times", "Tools", trophyBronze, tkAlarmSnooze, 10, "snoozes").needs("Alarm").secret(),
		t2Count("weather_1", "Weather Watcher", "Check the forecast", "Tools", trophyBronze, tkWeather, 1, "forecasts"),
		t2Count("text_page_1", "Under the Hood", "Open a system page in Text", "Tools", trophyBronze, tkTextPage, 1, "pages").needs("TextBrowser"),

		// ── System ──
		t2Count("screenshot_1", "Say Cheese", "Take a screenshot", "System", trophyBronze, tkScreenshot, 1, "screenshots"),
		t2Count("screenshot_10", "Shutterbug", "Take 10 screenshots", "System", trophyBronze, tkScreenshot, 10, "screenshots"),
		t2Count("screenshot_100", "Photo Album", "Take 100 screenshots", "System", trophySilver, tkScreenshot, 100, "screenshots"),
		t2Count("wake_1", "Power Nap", "Wake JukaHub from the screensaver", "System", trophyBronze, tkWake, 1, "wakes"),
		t2Count("update_1", "Up to Date", "Install a JukaHub update", "System", trophyBronze, tkUpdate, 1, "updates"),
		t2Count("backup_1", "Belt and Braces", "Create a backup", "System", trophyBronze, tkBackup, 1, "backups"),
		t2Count("restore_1", "Back in Time", "Restore a backup", "System", trophyBronze, tkRestore, 1, "").secret(),
		t2Count("bug_report_1", "Exterminator", "Send a bug report", "System", trophyBronze, tkBugReport, 1, "reports").secret().bonus(),
		t2Count("wifi_join_1", "Connected", "Join a Wi-Fi network", "System", trophyBronze, tkWifiJoin, 1, ""),
		t2Set("wifi_nets_3", "Hotspot Hopper", "Join 3 different Wi-Fi networks", "System", trophySilver, tkWifiNets, 3, "networks").bonus(),
		t2Count("bt_pair_1", "Paired Up", "Pair a Bluetooth device", "System", trophyBronze, tkBTPair, 1, "devices").gated("bluetooth"),
		t2Set("pads_2", "Second Controller", "Connect 2 different controllers", "System", trophyBronze, tkPads, 2, "controllers").bonus(),
		t2Set("pads_3", "Controller Drawer", "Connect 3 different controllers", "System", trophySilver, tkPads, 3, "controllers").bonus(),
		t2Count("package_1", "Unpacked", "Install a package", "System", trophyBronze, tkPackage, 1, "packages").needs("Packages"),
		t2Count("patch_1", "Patch Notes", "Install a package in Patch", "System", trophyBronze, tkPatch, 1, "patches").needs("Patch"),
		t2Count("plugin_1", "Plugged In", "Turn on a plugin", "System", trophyBronze, tkPlugin, 1, "plugins"),
		t2Count("search_1", "Found It", "Open a search result", "System", trophyBronze, tkSearch, 1, "searches"),
		t2Count("search_100", "Search Party", "Open 100 search results", "System", trophySilver, tkSearch, 100, "searches"),
		t2Count("start_menu_25", "Start Me Up", "Open the Start menu 25 times", "System", trophyBronze, tkStartMenu, 25, "times"),
		t2Count("start_menu_500", "Menu Maestro", "Open the Start menu 500 times", "System", trophySilver, tkStartMenu, 500, "times"),
		t2Count("quick_settings_10", "Quick Fix", "Open Quick Settings 10 times", "System", trophyBronze, tkQuickSettings, 10, "times"),
		t2Count("voice_1", "Voice Activated", "Use a voice command", "System", trophyBronze, tkVoiceCmd, 1, "commands"),
		t2Count("voice_100", "Voice of Command", "Use 100 voice commands", "System", trophyGold, tkVoiceCmd, 100, "commands"),
		t2Count("keys_1000", "Two Thumbs", "Tap 1,000 on-screen keyboard keys", "System", trophyBronze, tkKeyChars, 1000, "keys"),
		t2Count("keys_25000", "Thumb Novelist", "Tap 25,000 on-screen keyboard keys", "System", trophyGold, tkKeyChars, 25000, "keys").secret(),
		t2Count("favorite_1", "Favourite Thing", "Add something to Favorites", "System", trophyBronze, tkFavorite, 1, "favourites"),
		t2Count("app_launch_1", "App Starter", "Launch an app", "System", trophyBronze, tkAppLaunch, 1, "apps"),
		t2Count("low_battery_1", "Living Dangerously", "Play on to a low-battery warning", "System", trophyBronze, tkLowBattery, 1, "").gated("handheld").secret(),
		t2Count("tour_done_1", "Welcome Aboard", "Finish the welcome tour", "System", trophyBronze, tkTourDone, 1, ""),
		t2Count("stick_cal_1", "True Centre", "Calibrate the sticks", "System", trophyBronze, tkStickCal, 1, "").needs(stickCalScene),
		t2Count("smart_home_1", "Smart Home", "Control a smart-home device", "System", trophyBronze, tkSmartHome, 1, "").gated("smarthome"),
		t2Count("notif_center_10", "In the Loop", "Open the notification centre 10 times", "System", trophyBronze, tkNotifCenter, 10, "times"),

		// ── Style ──
		t2Count("theme_1", "New Look", "Apply a theme", "Style", trophyBronze, tkTheme, 1, "themes"),
		t2Count("theme_10", "Quick-Change Artist", "Apply themes 10 times", "Style", trophyBronze, tkTheme, 10, "times"),
		t2Set("themes_5", "Colour Theory", "Try 5 different themes", "Style", trophyBronze, tkThemes, 5, "themes"),
		t2Count("wallpaper_1", "Interior Decorator", "Choose a wallpaper", "Style", trophyBronze, tkWallpaper, 1, "wallpapers"),
		t2Count("wallpaper_10", "Gallery Wall", "Change the wallpaper 10 times", "Style", trophyBronze, tkWallpaper, 10, "changes"),
		t2Count("home_edit_1", "Make It Yours", "Edit Home", "Style", trophyBronze, tkHomeEdit, 1, "edits"),
		t2Count("home_edit_25", "Feng Shui", "Edit Home 25 times", "Style", trophySilver, tkHomeEdit, 25, "edits"),
		t2Count("color_mode_1", "Light Switch", "Swap between light and dark themes", "Style", trophyBronze, tkColorMode, 1, ""),

		// ── Extras: the trophy room itself, and a secret ──
		t2Count("room_visits_10", "Trophy Hunter", "Open Trophies 10 times", "Extras", trophyBronze, tkRoomVisit, 10, "visits"),
		t2Count("room_visits_100", "Display Cabinet", "Open Trophies 100 times", "Extras", trophySilver, tkRoomVisit, 100, "visits"),
		t2Set("room_details_25", "Fine Print", "Read the details of 25 trophies", "Extras", trophyBronze, tkRoomDetails, 25, "trophies"),
		t2Count("replay_1", "Encore", "Replay a trophy's celebration", "Extras", trophyBronze, tkReplay, 1, "").secret(),
		t2Count("confetti_1", "Party Planner", "Change the celebration style", "Extras", trophyBronze, tkConfetti, 1, ""),
		t2Count("easter_juka", "Say My Name", "Type J-U-K-A on Home", "Extras", trophyBronze, tkEasterJuka, 1, "").secret().bonus(),
	}
}
