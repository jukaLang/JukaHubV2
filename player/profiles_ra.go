package main

// profiles_ra.go — a profile's own RetroAchievements account and PPSSPP home.
//
// A profile's RetroArch launch gets its save folders from profiles_saves.go;
// this file adds the account those launches sign in with, and the folder the
// profile's managed PPSSPP build keeps its memstick in.
//
// The login lives in the profile's folder (retroachievements.json), not in
// RetroArch's config. The password is sealed with EncryptString before it is
// written, and the file is 0600; the plaintext exists only while the sign-in
// is typed and while one launch's scoped edits hold it. Writing it into each
// RetroArch config instead would leave it in every install's file, shared by
// every person — the owner's login is written there (raach_signin.go) because
// it is the device's, but a profile's is not.
//
// The scoped edits restore the owner's account when the game exits
// (racfgscope.go), and profilesScopeKey names the keys even when the profile
// has no account, so a crash still puts them back.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const profilesRAName = "retroachievements.json"

// profilesRALogin is one profile's account. Pass holds the sealed token as
// stored; profilesRALoad opens it.
type profilesRALogin struct {
	User string `json:"user"`
	Pass string `json:"pass,omitempty"`
}

// profilesRAPath is where a profile's account lives.
func profilesRAPath(dir string) string {
	return filepath.Join(dir, profilesRAName)
}

// profilesRALoad reads and opens a profile's account. A missing, unreadable
// or damaged file is no account: launches then play with achievements off,
// as every profile's did before this file existed.
func profilesRALoad(dir string) profilesRALogin {
	if strings.TrimSpace(dir) == "" {
		return profilesRALogin{}
	}
	var stored profilesRALogin
	if _, err := readJSONWithBackup(profilesRAPath(dir), &stored); err != nil {
		if !os.IsNotExist(err) {
			logWarn("profiles", "cannot read a profile's RetroAchievements account: %v", err)
		}
		return profilesRALogin{}
	}
	stored.User = strings.TrimSpace(stored.User)
	if stored.Pass != "" {
		pass, err := DecryptString(stored.Pass)
		if err != nil {
			logWarn("profiles", "cannot open a profile's RetroAchievements password: %v", err)
			stored.Pass = ""
		} else {
			stored.Pass = pass
		}
	}
	if stored.User == "" || stored.Pass == "" {
		return profilesRALogin{}
	}
	return stored
}

// profilesRASave writes a profile's account, sealing the password. The caller
// has already validated the username (ppssppRAValidUser).
func profilesRASave(dir, user, pass string) error {
	user = strings.TrimSpace(user)
	if dir == "" || user == "" || pass == "" {
		return fmt.Errorf("an account needs a username and a password")
	}
	sealed, err := EncryptString(pass)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(profilesRALogin{User: user, Pass: sealed}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return AtomicWrite(profilesRAPath(dir), append(data, '\n'), 0o600)
}

// profilesRAValues are the one-launch achievement settings for dir's account.
// Pure apart from reading the profile's file: with no account every account
// key is empty and cheevos_enable is off, which is what a profile's launch
// had before per-profile accounts.
func profilesRAValues(dir string) []raScopeValue {
	acct := profilesRALoad(dir)
	enable := "false"
	if acct.User != "" && acct.Pass != "" {
		enable = "true"
	}
	return []raScopeValue{
		{Key: "cheevos_enable", Value: enable},
		{Key: "cheevos_username", Value: acct.User},
		// An owner token left in the file must not be tried for this person.
		{Key: "cheevos_password", Value: acct.Pass},
		{Key: "cheevos_token", Value: ""},
	}
}

// profilesPPSSPPHome is the profile's PPSSPP home (its memstick and config),
// handed to the managed launcher through JUKAHUB_PPSSPP_HOME.
func profilesPPSSPPHome(dir string) string {
	return filepath.Join(dir, "ppsspp")
}

// raachSignInProfileDir is the profile whose account the open sign-in writes;
// "" for the owner's. Set by raachOpenSignIn, read by raachPassSubmit. Main
// thread.
var raachSignInProfileDir string

// raachSignInDetail is the Sign in for games row's line: the playing
// profile's account while someone else plays, the owner's otherwise.
func raachSignInDetail() string {
	if a := profilesCurrent(); a != nil {
		if a.Dir == "" {
			return "A guest session cannot keep an account: sign in from your own profile"
		}
		if acct := profilesRALoad(a.Dir); acct.User != "" {
			return "This profile's games sign in as " + acct.User + " · " + hintKeyFor("A", "Sign in") + " signs in with another account"
		}
		return "This profile's retroachievements.org account, for their unlocks"
	}
	user, in, _ := raachStatusNow()
	switch {
	case in:
		return "RetroArch signs in as " + user + " · " + hintKeyFor("A", "Sign in") + " signs in with another account"
	case user != "":
		return "RetroArch knows " + user + " but has no login for it · " + hintKeyFor("A", "Sign in") + " signs in"
	}
	return "Your retroachievements.org account, for unlocks while you play"
}

// raachSignInValue is the row's right-hand value.
func raachSignInValue() string {
	if a := profilesCurrent(); a != nil {
		if a.Dir == "" {
			return "Guest"
		}
		if user := profilesRALoad(a.Dir).User; user != "" {
			return user
		}
		return "Not signed in"
	}
	if user, in, _ := raachStatusNow(); in {
		return user
	}
	return "Not signed in"
}
