package main

// ppsspp_managed_ra.go — JukaHub's PPSSPP signs in to RetroAchievements with
// JukaHub's account (backlog GAME-21), the way minui-psp's launcher copies the
// launcher's login into PPSSPP before each game. Without it JukaHub's PPSSPP
// only had a login made inside PPSSPP, though JukaHub already knows the
// RetroArch one (retroachievements.go).
//
// What PPSSPP keeps (1.17 and later, its rcheevos client). The repository has
// no PPSSPP source, so these are the names as PPSSPP's Config.cpp and
// NativeApp.cpp use them, read conservatively; a device run confirms them:
//
//   - [Achievements] in PSP/SYSTEM/ppsspp.ini: AchievementsEnable,
//     AchievementsChallengeMode (hardcore) and AchievementsUserName;
//   - the login token alone in PSP/SYSTEM/ppsspp_retroachievements.dat (its
//     NativeSaveSecret store), never in the ini. PPSSPP stores no password.
//
// JukaHub's side is RetroArch's login: the account JukaHub's RetroAchievements
// page uses (raEffectiveCredentials) and the cheevos_token RetroArch keeps
// beside cheevos_username after a password sign-in, from the same config. The
// Web API key the page reads unlocks with is not a login token and is never
// written. The token is never logged, never put in the ini, a toast, the card
// or a report, and its file is 0600 with no backup copy beside it.
//
// The HOME this writes into is JukaHub's own (ppssppHomeDir): the firmware's
// PPSSPP and its settings are never touched.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

// ppssppRATokenFile is PPSSPP's RetroAchievements token store in PSP/SYSTEM.
const ppssppRATokenFile = "ppsspp_retroachievements.dat"

// ppssppRALoginFn reads JukaHub's login; the tests replace it.
var ppssppRALoginFn = ppssppRALogin

// ppssppRALast is what the last managed launch did about the sign-in, for the
// PPSSPP card (never a name or a token).
var ppssppRALast atomic.Pointer[string]

// ppssppRAPlan is what the sign-in does to PPSSPP's files before a launch.
type ppssppRAPlan struct {
	write  bool   // sign PPSSPP in: user and token, achievements on, hardcore off
	reason string // what was decided, for the log and the card (no secrets)
}

// ppssppRAMerge decides, from JukaHub's login (user, token) and what PPSSPP
// has — iniUser from [Achievements] AchievementsUserName, haveToken when its
// token file is not empty — whether PPSSPP is signed in with JukaHub's
// account. Pure.
//
//   - Off on the PPSSPP card, or no complete JukaHub login: nothing.
//   - Another account signed in inside PPSSPP: left alone.
//   - The same account with a token of its own: already signed in, left alone
//     (a hardcore mode the player chose there too).
//   - No account, or the same one without a token: user and token written,
//     achievements on, hardcore off (hardcore turns save states off).
func ppssppRAMerge(optOut bool, user, token, iniUser string, haveToken bool) ppssppRAPlan {
	iniUser = strings.TrimSpace(iniUser)
	switch {
	case optOut:
		return ppssppRAPlan{reason: "sign-in is off on this card"}
	case user == "" || token == "":
		return ppssppRAPlan{reason: "no RetroArch login to share"}
	case iniUser != "" && !strings.EqualFold(iniUser, user):
		return ppssppRAPlan{reason: "another account is signed in inside PPSSPP, left alone"}
	case iniUser != "" && haveToken:
		return ppssppRAPlan{reason: "PPSSPP was already signed in"}
	}
	return ppssppRAPlan{write: true, reason: "signed in with JukaHub's account, hardcore off"}
}

// ppssppRAValidUser accepts a RetroAchievements account name that can stand in
// an ini line as it is.
func ppssppRAValidUser(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// ppssppRAValidToken accepts a login token (RetroAchievements' are letters and
// digits).
func ppssppRAValidToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// ppssppRALogin is JukaHub's RetroAchievements login as PPSSPP takes it: the
// account JukaHub uses and RetroArch's login token for that same account, from
// the first RetroArch config (in raDetectUsername's order) that holds both.
// "" when there is none. Small file reads only: it runs before a launch.
func ppssppRALogin() (user, token string) {
	want := strings.TrimSpace(raEffectiveCredentials().Username)
	if want == "" {
		return "", ""
	}
	var files []string
	for _, pattern := range raRetroArchConfigGlobs {
		matches, _ := filepath.Glob(pattern)
		sort.Strings(matches)
		files = append(files, matches...)
	}
	files = append(files, raRetroArchConfigPaths...)
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		u := strings.TrimSpace(raConfigValue(string(data), "cheevos_username"))
		t := strings.TrimSpace(raConfigValue(string(data), "cheevos_token"))
		if !strings.EqualFold(u, want) || !ppssppRAValidUser(u) || !ppssppRAValidToken(t) {
			continue
		}
		return u, t
	}
	return "", ""
}

// ppssppRASignIn signs JukaHub's PPSSPP (memstick: the PSP folder under its
// HOME) in by ppssppRAMerge and returns what it did. The caller holds
// ppssppMgr.mu, before a managed launch, after ppssppSeedHome.
func ppssppRASignIn(memstick string, optOut bool, user, token string) string {
	if memstick == "" {
		return ""
	}
	sys := filepath.Join(memstick, "SYSTEM")
	ini := ppssppMemstickIni(memstick)
	tokenPath := filepath.Join(sys, ppssppRATokenFile)
	var lines []string
	if raw, err := os.ReadFile(ini); err == nil {
		lines = strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	}
	iniUser, _ := ppssppINIValue(lines, "Achievements", "AchievementsUserName")
	haveToken := false
	if info, err := os.Stat(tokenPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		haveToken = true
	}
	if user != "" && !ppssppRAValidUser(user) || token != "" && !ppssppRAValidToken(token) {
		user, token = "", ""
	}
	plan := ppssppRAMerge(optOut, user, token, iniUser, haveToken)
	if !plan.write {
		return plan.reason
	}
	if err := os.MkdirAll(sys, 0o755); err != nil {
		logWarn("ppsspp", "could not sign PPSSPP in to RetroAchievements: %v", err)
		return "could not sign PPSSPP in"
	}
	// The token first: a user name with no token is a PPSSPP that is simply
	// not signed in yet, and the next launch tries again.
	tmp := tokenPath + ".tmp"
	if err := writeFileSynced(tmp, []byte(token), 0o600); err != nil {
		_ = os.Remove(tmp)
		logWarn("ppsspp", "could not give PPSSPP the RetroAchievements sign-in: %v", err)
		return "could not sign PPSSPP in"
	}
	if err := os.Rename(tmp, tokenPath); err != nil {
		_ = os.Remove(tmp)
		logWarn("ppsspp", "could not give PPSSPP the RetroAchievements sign-in: %v", err)
		return "could not sign PPSSPP in"
	}
	edit := func(lines []string) []string {
		lines = ppssppSetSectionValue(lines, "Achievements", "AchievementsEnable", "True")
		lines = ppssppSetSectionValue(lines, "Achievements", "AchievementsChallengeMode", "False")
		return ppssppSetSectionValue(lines, "Achievements", "AchievementsUserName", user)
	}
	var err error
	if regularFile(ini) {
		err = writePPSSPPControls(ini, edit)
	} else {
		err = os.WriteFile(ini, []byte(strings.Join(edit(nil), "\n")+"\n"), 0o644)
	}
	if err != nil {
		logWarn("ppsspp", "could not set PPSSPP's RetroAchievements account: %v", err)
		return "could not sign PPSSPP in"
	}
	logInfo("ppsspp", "PPSSPP signed in to RetroAchievements with JukaHub's account (hardcore off)")
	return plan.reason
}

// ppssppRAOption is the PPSSPP card's RetroAchievements switch. Main thread: it
// reads the published state and the last launch's outcome only.
func ppssppRAOption(st ppssppState) recoveryOption {
	on := !st.RANoSignIn
	title := "RetroAchievements sign-in: On"
	detail := "Before each game: RetroArch's account, hardcore off · an account signed in inside PPSSPP stays"
	if p := ppssppRALast.Load(); p != nil && on {
		detail = "Last game: " + *p
	}
	if !on {
		title = "RetroAchievements sign-in: Off"
		detail = "PPSSPP's own sign-in is left alone · turn on to use JukaHub's account"
	}
	return recoveryOption{icon: "trophy", title: title, detail: detail,
		run: func(*Config) { recoveryCardClose(); ppssppSetRASignIn(!on) }}
}

// ppssppSetRASignIn turns the sign-in on or off, off the event thread (the
// state is written under ppssppMgr.mu, which only background work waits for).
func ppssppSetRASignIn(on bool) {
	go func() {
		ppssppMgr.mu.Lock()
		st := ppssppLockedState()
		st.RANoSignIn = !on
		err := ppssppSaveLocked()
		ppssppMgr.mu.Unlock()
		if err != nil {
			ShowToast("PPSSPP: "+err.Error(), ToastKindError)
			return
		}
		if on {
			ShowToast("PPSSPP signs in to RetroAchievements with JukaHub's account before each game", ToastKindSuccess)
			return
		}
		ppssppRALast.Store(nil)
		ShowToast("PPSSPP's RetroAchievements sign-in is left to PPSSPP", ToastKindInfo)
	}()
}
