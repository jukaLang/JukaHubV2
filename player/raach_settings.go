package main

// raach_settings.go — RetroAchievements in games, set from JukaHub the way
// Batocera's EmulationStation sets them.
//
// JukaHub's RetroAchievements page (retroachievements.go) is a viewer of the
// player's profile on the website. The achievements that unlock while a game
// plays are RetroArch's own (its rcheevos client), and until now their
// switches — hardcore, leaderboards, the unlock sound — were only in
// RetroArch's menu, a long way from a handheld's library. Settings →
// RetroAchievements has them:
//
//   - Achievements in games      cheevos_enable
//   - Hardcore mode              cheevos_hardcore_mode_enable
//   - Leaderboards               see raachLeaderboardValues
//   - Challenge indicators       cheevos_challenge_indicators
//   - Unlock sound               cheevos_unlock_sound_enable
//   - Screenshot on unlock       cheevos_auto_screenshot
//   - Encore mode                cheevos_start_active
//   - Rich presence              cheevos_richpresence_enable
//   - Badges                     cheevos_badges_enable
//   - Verbose messages           cheevos_verbose_enable
//
// Every row starts on "RetroArch's own": nothing is written, so nobody's
// setup changes until they pick a value. A picked value goes into
// retroarch.cfg for one launch through the scoped edit (racfgscope.go) and
// is put back when the game exits, like every other launch setting. Only a
// RetroArch launch of the owner gets them: a profile's launch has
// achievements switched off by profiles_saves.go, and that is not fought.
// Offline RetroAchievements (raoffline*.go, registered after this file)
// may turn hardcore off while its proxy serves; its value wins.
//
// The row Sign in for games (raach_signin.go) writes RetroArch's own login,
// and raach_unlocks.go notices the unlocks of a running game.
//
// Hardcore honesty. With hardcore on RetroArch refuses to load a state, to
// rewind and to apply cheats (it turns hardcore off when cheats load). The
// launch settings that run before the hooks (cheats.go, retro_transport.go)
// ask raachHardcoreDecision — a game's cheats give way only to a Hardcore
// choice made here; under RetroArch's own values they load and end
// hardcore, as they always did (raachCheatsWin) — and the game page's
// "start from a state" asks raachHardcoreAhead; the in-game menu and the
// sleep power-off ask raachHardcoreRunning (raach_unlocks.go), which reads
// the config as the game started.

import (
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// raachSettingsSection is the Settings section of these rows.
	raachSettingsSection = "achievements"

	// The stored choices. A missing key is raachOwn.
	raachOwn           = "own"
	raachOn            = "on"
	raachOff           = "off"
	raachTrackers      = "trackers"
	raachNotifications = "notifications"

	raachKeyEnable       = "raachEnable"
	raachKeyHardcore     = "raachHardcore"
	raachKeyLeaderboards = "raachLeaderboards"
	raachKeyChallenge    = "raachChallenge"
	raachKeySound        = "raachUnlockSound"
	raachKeyShot         = "raachScreenshot"
	raachKeyEncore       = "raachEncore"
	raachKeyPresence     = "raachRichPresence"
	raachKeyBadges       = "raachBadges"
	raachKeyVerbose      = "raachVerbose"
)

// raachSwitch is one on/off setting: its Custom key, its row and the
// retroarch.cfg key it writes (names from RetroArch's configuration.c, as
// its own config files spell them).
type raachSwitch struct {
	key    string
	label  string
	detail string
	cfgKey string
}

// raachSwitches are the on/off rows in Settings order; Leaderboards sits
// after Hardcore mode (raachSettingsRows).
var raachSwitches = []raachSwitch{
	{raachKeyEnable, "Achievements in games", "RetroArch's achievements while you play; needs Sign in for games", "cheevos_enable"},
	{raachKeyHardcore, "Hardcore mode", "Unlocks count as hardcore; no state loading, rewind, cheats or slow motion", "cheevos_hardcore_mode_enable"},
	{raachKeyChallenge, "Challenge indicators", "A small badge while a challenge achievement can still be earned", "cheevos_challenge_indicators"},
	{raachKeySound, "Unlock sound", "A chime when an achievement unlocks", "cheevos_unlock_sound_enable"},
	{raachKeyShot, "Screenshot on unlock", "RetroArch saves a picture of every unlock", "cheevos_auto_screenshot"},
	{raachKeyEncore, "Encore mode", "Achievements you already have can unlock again", "cheevos_start_active"},
	{raachKeyPresence, "Rich presence", "Your profile shows what you are doing in the game", "cheevos_richpresence_enable"},
	{raachKeyBadges, "Badges", "Achievement pictures in RetroArch's own menu", "cheevos_badges_enable"},
	{raachKeyVerbose, "Verbose messages", "RetroArch's extra achievement messages: sign-in, game summary", "cheevos_verbose_enable"},
}

func init() {
	gameLaunchRegisterPrepare(raachPrepareLaunch)
}

// ── Choices ──────────────────────────────────────────────────────────────

// raachChoice is the stored choice for key: raachOwn, raachOn or raachOff,
// and for Leaderboards also raachTrackers or raachNotifications. Anything
// else is raachOwn.
func raachChoice(c *Config, key string) string {
	v := strings.ToLower(strings.TrimSpace(raTuneRaw(c, key)))
	switch v {
	case raachOn, raachOff:
		return v
	case raachTrackers, raachNotifications:
		if key == raachKeyLeaderboards {
			return v
		}
	}
	return raachOwn
}

func raachSwitchOptions(*Config) []setOption {
	return []setOption{
		{Value: raachOwn, Label: "RetroArch's own", Note: "JukaHub writes nothing"},
		{Value: raachOn, Label: "On"},
		{Value: raachOff, Label: "Off"},
	}
}

func raachLeaderboardOptions(*Config) []setOption {
	return []setOption{
		{Value: raachOwn, Label: "RetroArch's own", Note: "JukaHub writes nothing"},
		{Value: raachOn, Label: "On", Note: "Notifications and on-screen trackers"},
		{Value: raachTrackers, Label: "Trackers only", Note: "The running score, and the result when it is sent"},
		{Value: raachNotifications, Label: "Notifications only", Note: "Started, sent and failed attempts; no trackers"},
		{Value: raachOff, Label: "Off", Note: "Nothing on screen; newer RetroArch still sends hardcore scores"},
	}
}

// raachLeaderboardValues are the keys a Leaderboards choice writes. RetroArch
// 1.9.1 made cheevos_leaderboards_enable a word ("false", "trackers",
// "notifications", "true"); the builds since early 2023 show and hide each
// leaderboard message with its own cheevos_visibility_lboard_* switch. Both
// are written, so the choice holds on either: a key a build does not know is
// ignored by it and put back after the game. Pure.
func raachLeaderboardValues(mode string) []raScopeValue {
	var legacy string
	var start, submit, cancel, trackers bool
	switch mode {
	case raachOn:
		legacy, start, submit, cancel, trackers = "true", true, true, true, true
	case raachTrackers:
		legacy, submit, trackers = "trackers", true, true
	case raachNotifications:
		legacy, start, submit, cancel = "notifications", true, true, true
	case raachOff:
		legacy = "false"
	default:
		return nil
	}
	return []raScopeValue{
		{Key: "cheevos_leaderboards_enable", Value: legacy},
		{Key: "cheevos_visibility_lboard_start", Value: boolString(start)},
		{Key: "cheevos_visibility_lboard_submit", Value: boolString(submit)},
		{Key: "cheevos_visibility_lboard_cancel", Value: boolString(cancel)},
		{Key: "cheevos_visibility_lboard_trackers", Value: boolString(trackers)},
	}
}

// raachLaunchValues are the retroarch.cfg values the choices in c ask for:
// none while every row is on RetroArch's own. Main thread (reads Custom).
func raachLaunchValues(c *Config) []raScopeValue {
	var out []raScopeValue
	for _, s := range raachSwitches {
		switch raachChoice(c, s.key) {
		case raachOn:
			out = append(out, raScopeValue{Key: s.cfgKey, Value: "true"})
		case raachOff:
			out = append(out, raScopeValue{Key: s.cfgKey, Value: "false"})
		}
	}
	return append(out, raachLeaderboardValues(raachChoice(c, raachKeyLeaderboards))...)
}

// raachOwnerPlaying reports whether a launch now is the owner's: the same
// test profiles_saves.go uses before it switches achievements off.
func raachOwnerPlaying() bool {
	a := profilesCurrent()
	return a == nil || a.Dir == ""
}

// ── The launch ───────────────────────────────────────────────────────────

// raachPrepareLaunch writes the chosen values for this launch
// (gameLaunchRegisterPrepare). RetroArch launches of the owner only.
func raachPrepareLaunch(launcher string, _ libraryGame) {
	if !retroArchLauncher(launcher) || !raachOwnerPlaying() {
		return
	}
	values := raachLaunchValues(appConfig)
	if len(values) == 0 {
		return
	}
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("achievements", "cannot apply the RetroAchievements settings in %s: %v", path, err)
		}
	}
}

// ── Reading RetroArch's config ───────────────────────────────────────────

// raachConfigText is the first config the launcher's RetroArch reads (any
// RetroArch's when launcher is ""), or "" when there is none.
func raachConfigText(launcher string) string {
	candidates := retroArchConfigCandidates()
	if launcher != "" {
		candidates = raLaunchCandidates(launcher, candidates)
	}
	for _, path := range raScopeTargets(candidates) {
		if raw, err := os.ReadFile(path); err == nil {
			return string(raw)
		}
	}
	return ""
}

// raachCfgBool reads a switch the way RetroArch does, with its default when
// the line is missing or empty. Pure.
func raachCfgBool(cfg, key string, def bool) bool {
	v, ok := raConfigGet(cfg, key)
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return def
	}
	return strings.EqualFold(v, "true") || v == "1"
}

// raachCfgUser is the account RetroArch signs in with, "" when none.
func raachCfgUser(cfg string) string {
	v, _ := raConfigGet(cfg, "cheevos_username")
	return strings.TrimSpace(v)
}

// raachCfgSignedIn reports whether cfg holds a login RetroArch can use: a
// username with its token or a password. Pure.
func raachCfgSignedIn(cfg string) bool {
	token, _ := raConfigGet(cfg, "cheevos_token")
	pass, _ := raConfigGet(cfg, "cheevos_password")
	return raachCfgUser(cfg) != "" && (strings.TrimSpace(token) != "" || pass != "")
}

// raachAchievementsIn reports whether RetroArch runs achievements with cfg:
// switched on (off by default) and signed in. Pure.
func raachAchievementsIn(cfg string) bool {
	return raachCfgBool(cfg, "cheevos_enable", false) && raachCfgSignedIn(cfg)
}

// raachHardcoreIn reports whether RetroArch plays in hardcore with cfg:
// achievements on and hardcore on (RetroArch's default since 1.10 when the
// line is missing). A game without achievements may still load states;
// JukaHub cannot tell, so it promises less rather than more. Pure.
func raachHardcoreIn(cfg string) bool {
	return raachAchievementsIn(cfg) && raachCfgBool(cfg, "cheevos_hardcore_mode_enable", true)
}

// raachHardcoreAhead reports whether a launch with this launcher will play
// in hardcore, before the launch hooks run: RetroArch's config with the
// choices of this file applied in memory. For the launch settings that run
// first (cheats.go) and the game page. Main thread.
func raachHardcoreAhead(launcher string) bool {
	if !retroArchLauncher(launcher) || !raachOwnerPlaying() {
		return false
	}
	cfg := raachConfigText(launcher)
	if cfg == "" {
		return false
	}
	cfg, _ = raScopeApplyText(cfg, raachLaunchValues(appConfig))
	return raachHardcoreIn(cfg)
}

// raachHardcoreDecision is raachHardcoreAhead when JukaHub decides it — a
// choice made on the Achievements or Hardcore row, or a profile's launch,
// which has achievements off — and decided=false when RetroArch's own
// values do (retro_transport.go then reads the file as before). Main thread.
func raachHardcoreDecision(launcher string) (hard, decided bool) {
	if !retroArchLauncher(launcher) {
		return false, false
	}
	if !raachOwnerPlaying() {
		return false, true
	}
	if raachChoice(appConfig, raachKeyEnable) == raachOwn && raachChoice(appConfig, raachKeyHardcore) == raachOwn {
		return false, false
	}
	return raachHardcoreAhead(launcher), true
}

// raachCheatsWin reports whether the game's cheats end hardcore for this
// launch: cheats switched on for it (cheats.go) while JukaHub does not
// decide hardcore. RetroArch's own rule then stands, as before these
// settings: the cheats load and RetroArch turns hardcore off. A Hardcore
// choice made here wins instead (prepareCheats skips the cheats). Main
// thread.
func raachCheatsWin(launcher string, game libraryGame) bool {
	if _, decided := raachHardcoreDecision(launcher); decided {
		return false
	}
	rec, ok := cheatsRecordFor(game.Path)
	return ok && len(rec.On) > 0
}

// ── Settings ─────────────────────────────────────────────────────────────

// raachStatus caches the sign-in read-out: Settings draws it every frame and
// it comes from a file on the card. Main thread.
var raachStatus struct {
	mu   sync.Mutex
	at   time.Time
	user string
	in   bool
	cfg  bool
}

// raachStatusNow is the account RetroArch signs in with, whether it can,
// and whether a RetroArch config exists at all; read at most every 5 s.
func raachStatusNow() (user string, signedIn, haveConfig bool) {
	raachStatus.mu.Lock()
	defer raachStatus.mu.Unlock()
	if raachStatus.at.IsZero() || time.Since(raachStatus.at) >= 5*time.Second {
		cfg := raachConfigText("")
		raachStatus.at = time.Now()
		raachStatus.user, raachStatus.in, raachStatus.cfg = raachCfgUser(cfg), raachCfgSignedIn(cfg), cfg != ""
	}
	return raachStatus.user, raachStatus.in, raachStatus.cfg
}

// raachStatusInvalidate makes the next read-out read the file again.
func raachStatusInvalidate() {
	raachStatus.mu.Lock()
	raachStatus.at = time.Time{}
	raachStatus.mu.Unlock()
}

// raachOptionLabel is the label of value among options.
func raachOptionLabel(options []setOption, value string) string {
	if i := raTuneOptionIndex(options, value); i >= 0 {
		return options[i].Label
	}
	return "RetroArch's own"
}

// raachChoiceRow is one row of the section.
func raachChoiceRow(key, label, detail string, options func(*Config) []setOption) setRow {
	return setRow{
		ID: key, Label: label, Kind: setKindChoice,
		Detail:  setStatic(detail),
		Options: options,
		Current: func(c *Config) int { return raTuneOptionIndex(options(c), raachChoice(c, key)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, key, value)
			setToast(label + ": " + raachOptionLabel(options(c), value))
		},
	}
}

// raachSettingsRows are Settings → RetroAchievements (setmodel.go).
func raachSettingsRows() []setRow {
	rows := []setRow{
		{
			ID: "raachNoRA", Label: "RetroArch", Kind: setKindInfo,
			Detail: setStatic("Not found yet: these settings apply once RetroArch is installed"),
			Value:  setStatic("Not found"),
			Visible: func(*Config) bool {
				_, _, found := raachStatusNow()
				return !found
			},
		},
		{
			ID: "raachSignIn", Label: "Sign in for games", Kind: setKindAction,
			Detail: func(*Config) string {
				user, in, _ := raachStatusNow()
				switch {
				case in:
					return "RetroArch signs in as " + user + " · " + hintKeyFor("A", "Sign in") + " signs in with another account"
				case user != "":
					return "RetroArch knows " + user + " but has no login for it · " + hintKeyFor("A", "Sign in") + " signs in"
				}
				return "Your retroachievements.org account, for unlocks while you play"
			},
			Value: func(*Config) string {
				if user, in, _ := raachStatusNow(); in {
					return user
				}
				return "Not signed in"
			},
			Open: raachOpenSignIn,
		},
	}
	for _, s := range raachSwitches {
		rows = append(rows, raachChoiceRow(s.key, s.label, s.detail, raachSwitchOptions))
		if s.key == raachKeyHardcore {
			rows = append(rows, raachChoiceRow(raachKeyLeaderboards, "Leaderboards",
				"Leaderboard messages and the running score on screen", raachLeaderboardOptions))
		}
	}
	return append(rows, setRow{
		ID: "raachProfile", Label: "Your RetroAchievements", Kind: setKindAction,
		Detail:  setStatic("Recent unlocks and progress from the website (Web API key)"),
		Visible: func(*Config) bool { return retroAchievementsSupported() },
		Open:    func(*Config) { openRetroAchievementsPanel() },
	})
}
