package main

// steam_test.go — the parsers of Steam's outside data (VDF files, the profile
// games page) and the OpenID checks that run before Steam is asked.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSteamParseVDF(t *testing.T) {
	text := `"libraryfolders"
{
	// a comment
	"0"
	{
		"path"		"C:\\Program Files (x86)\\Steam"
		"apps" { "620" "123" }
	}
	"1"		"D:\\Old\\Library"
	"2" { "path" "E:\\Games" }
}`
	lf := parseVDF(text).child("LibraryFolders")
	if lf == nil {
		t.Fatal("no libraryfolders block")
	}
	if got := lf.child("0").get("path"); got != `C:\Program Files (x86)\Steam` {
		t.Errorf("path = %q", got)
	}
	if got := lf.child("1").Value; got != `D:\Old\Library` {
		t.Errorf("old-format path = %q", got)
	}
	if got := lf.child("0").child("apps").get("620"); got != "123" {
		t.Errorf("apps.620 = %q", got)
	}
	if len(lf.Order) != 3 || lf.child("2").get("path") != `E:\Games` {
		t.Errorf("order = %v", lf.Order)
	}
	// Broken input never panics and keeps what it could read.
	for _, bad := range []string{`"a" {`, `}}}"x"`, `"k"`, `"a" { "b" }`, `"unterminated`} {
		_ = parseVDF(bad)
	}
}

func TestSteamParseProfileGames(t *testing.T) {
	page := `<html><template id="gameslist_config" data-profile-gameslist="{&quot;strProfileName&quot;:&quot;Juka&quot;,&quot;rgGames&quot;:[{&quot;appid&quot;:620,&quot;name&quot;:&quot;Portal 2&quot;,&quot;playtime_forever&quot;:754,&quot;rtime_last_played&quot;:1700000000},{&quot;appid&quot;:400,&quot;name&quot;:&quot;Portal&quot;}]}"></template></html>`
	games, name, err := steamParseProfileGames([]byte(page))
	if err != nil || name != "Juka" || len(games) != 2 {
		t.Fatalf("games=%v name=%q err=%v", games, name, err)
	}
	if games[0].AppID != 620 || games[0].Playtime != 754 || games[0].LastPlayed != 1700000000 {
		t.Errorf("first game = %+v", games[0])
	}
	old := "<script>\nvar rgGames = [{\"appid\":10,\"name\":\"Counter-Strike\",\"hours_forever\":\"1,234.5\",\"last_played\":5}];\n</script>"
	games, _, err = steamParseProfileGames([]byte(old))
	if err != nil || len(games) != 1 || games[0].Playtime != 74070 || games[0].LastPlayed != 5 {
		t.Errorf("old page: games=%v err=%v", games, err)
	}
	if _, _, err := steamParseProfileGames([]byte(`<div class="profile_private_info">This profile is private.</div>`)); err != errSteamPrivate {
		t.Errorf("private page: err = %v", err)
	}
	empty := `<template data-profile-gameslist="{&quot;rgGames&quot;:[]}"></template>`
	if _, _, err := steamParseProfileGames([]byte(empty)); err != errSteamPrivate {
		t.Errorf("empty list: err = %v", err)
	}
}

// TestSteamOpenIDRejects covers the checks made before Steam is asked to
// confirm a sign-in: none of these may reach the network or succeed.
func TestSteamOpenIDRejects(t *testing.T) {
	const ret = "http://192.168.1.5:8765/steam/return?s=00112233445566778899aabbccddeeff"
	good := url.Values{
		"openid.ns":          {steamOpenIDNS},
		"openid.mode":        {"id_res"},
		"openid.signed":      {"signed,op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle"},
		"openid.op_endpoint": {steamOpenIDEndpoint},
		"openid.return_to":   {ret},
		"openid.claimed_id":  {"https://steamcommunity.com/openid/id/76561197960435530"},
		"openid.identity":    {"https://steamcommunity.com/openid/id/76561197960435530"},
	}
	mutate := func(k, v string) url.Values {
		q := url.Values{}
		for key, vs := range good {
			q[key] = append([]string(nil), vs...)
		}
		q.Set(k, v)
		return q
	}
	cases := map[string]url.Values{
		"cancel":        mutate("openid.mode", "cancel"),
		"other op":      mutate("openid.op_endpoint", "https://evil.example/openid/login"),
		"other return":  mutate("openid.return_to", ret+"x"),
		"other host id": mutate("openid.claimed_id", "https://evil.example/openid/id/76561197960435530"),
		"not an id":     mutate("openid.claimed_id", "https://steamcommunity.com/openid/id/123"),
		"plain http id": mutate("openid.claimed_id", "http://steamcommunity.com/openid/id/76561197960435530"),
		"unsigned":      mutate("openid.signed", "op_endpoint,claimed_id"),
		"other ns":      mutate("openid.ns", "http://openid.net/signon/1.1"),
		"identity diff": mutate("openid.identity", "https://steamcommunity.com/openid/id/76561197960435531"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a check that got past the fields would fail here, not reach Steam
	for name, q := range cases {
		if id, err := steamCheckClaim(ctx, q, ret); err == nil {
			t.Errorf("%s: accepted as %s", name, id)
		}
	}
	// One-use tokens: taken once, never twice, and only the right length.
	link, err := steamNewSignIn("http://192.168.1.5:8765")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	back, _ := url.Parse(u.Query().Get("openid.return_to"))
	token := back.Query().Get("s")
	if got, ok := steamTakeSignIn(token); !ok || got != u.Query().Get("openid.return_to") {
		t.Fatalf("token not taken: %q %v", got, ok)
	}
	if _, ok := steamTakeSignIn(token); ok {
		t.Error("a token was taken twice")
	}
	if steamSafeHost("evil.example/x") || steamSafeHost("a b") || !steamSafeHost("jukahub.local:8765") {
		t.Error("steamSafeHost")
	}
}

// TestSteamAuthParsers covers what is read from Steam's login server and its
// tokens: the protobuf walk, the flexible JSON ids and the JWT claims.
func TestSteamAuthParsers(t *testing.T) {
	var msg []byte
	msg = steamPBVarint(msg, 1, 1234567890123)
	msg = steamPBString(msg, 2, "https://s.team/q/1/42")
	msg = steamPBBytes(msg, 3, []byte{0, 1, 2})
	msg = steamPBInt32(msg, 4, -500)
	msg = steamPBFixed64(msg, 5, 76561197960435530)
	fields, err := steamPBDecode(msg)
	if err != nil || len(fields) != 5 {
		t.Fatalf("decode: %v %d", err, len(fields))
	}
	if fields[0].value != 1234567890123 || string(fields[1].data) != "https://s.team/q/1/42" ||
		len(fields[2].data) != 3 || int32(fields[3].value) != -500 || fields[4].value != 76561197960435530 {
		t.Errorf("fields = %+v", fields)
	}
	// Truncated or lying input stops with an error and never panics.
	for _, bad := range [][]byte{{0x0a, 0x7f}, {0x09, 1, 2}, {0xff}, {0x0b}} {
		if _, err := steamPBDecode(bad); err == nil {
			t.Errorf("%x decoded", bad)
		}
	}
	var u struct {
		A steamFlexUint `json:"a"`
		B steamFlexUint `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":"18446744073709551615","b":42}`), &u); err != nil || u.A != 18446744073709551615 || u.B != 42 {
		t.Errorf("flex uint = %+v %v", u, err)
	}
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"76561197960435530","exp":1900000000,"aud":["web"]}`))
	if sub, exp, ok := steamJWTClaims("x." + claims + ".y"); !ok || sub != "76561197960435530" || exp != 1900000000 {
		t.Errorf("claims = %q %d %v", sub, exp, ok)
	}
	bad := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"123"}`))
	if _, _, ok := steamJWTClaims("x." + bad + ".y"); ok {
		t.Error("a token naming no account was accepted")
	}
	if _, _, ok := steamJWTClaims("not-a-token"); ok {
		t.Error("garbage accepted")
	}
}

// TestSteamManifestGame covers the download and size fields of an
// appmanifest_*.acf.
func TestSteamManifestGame(t *testing.T) {
	acf := `"AppState" { "appid" "620" "name" "Portal 2" "StateFlags" "1026" "SizeOnDisk" "0"
		"BytesToDownload" "1000" "BytesDownloaded" "250" }`
	g := steamManifestGame(620, "Portal 2", parseVDF(acf).child("appstate"))
	if g.Installed || !g.steamDownloading() || steamPercent(g.DlDone, g.DlTotal) != 25 {
		t.Errorf("downloading = %+v", g)
	}
	acf = `"AppState" { "StateFlags" "4" "SizeOnDisk" "12345" "BytesToDownload" "10" "BytesDownloaded" "10" }`
	g = steamManifestGame(620, "Portal 2", parseVDF(acf).child("appstate"))
	if !g.Installed || g.steamDownloading() || g.Size != 12345 {
		t.Errorf("installed = %+v", g)
	}
}

func TestSteamLocalLibraryScanAndLaunchURI(t *testing.T) {
	root := t.TempDir()
	apps := filepath.Join(root, "steamapps")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `"AppState" { "appid" "620" "name" "Portal 2" "StateFlags" "4" "SizeOnDisk" "12345" }`
	if err := os.WriteFile(filepath.Join(apps, "appmanifest_620.acf"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	games := steamInstalledGames(root, "", false)
	if len(games) != 1 || games[0].AppID != 620 || !games[0].Installed || games[0].Name != "Portal 2" {
		t.Fatalf("installed games = %+v", games)
	}
	if got := steamRunURI(620, steamActRun); got != "steam://rungameid/620" {
		t.Errorf("play URI = %q", got)
	}
	if got := steamRunURI(620, steamActInstall); got != "steam://install/620" {
		t.Errorf("install URI = %q", got)
	}
}

func TestSteamPercentLargeBytes(t *testing.T) {
	if got := steamPercent(math.MaxInt64/2, math.MaxInt64); got != 50 {
		t.Errorf("large download progress = %d, want 50", got)
	}
	if got := steamPercent(math.MaxInt64-1, math.MaxInt64); got != 99 {
		t.Errorf("nearly complete download progress = %d, want 99", got)
	}
}

func TestSteamSignOutInvalidatesInFlightLibrary(t *testing.T) {
	oldDir := steamDirOverride
	steamDirOverride = t.TempDir()
	t.Cleanup(func() { steamDirOverride = oldDir })
	const id = "76561197960435530"
	if err := steamSignInAs(id, "profile"); err != nil {
		t.Fatal(err)
	}
	_, _, generation := steamCurrentAccountSnapshot()
	if err := steamSignOut(); err != nil {
		t.Fatal(err)
	}
	stale := steamLibraryFile{SteamID: id, Games: []steamGame{{AppID: 620, Name: "Portal 2"}}}
	if err := steamSaveLibrary(stale, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(steamLibraryPath()); !os.IsNotExist(err) {
		t.Fatalf("signed-out library was restored: %v", err)
	}
	_, _, current := steamCurrentAccountSnapshot()
	if err := steamSaveLibrary(steamLibraryFile{Games: []steamGame{{AppID: 400, Name: "Portal"}}}, current); err != nil {
		t.Fatal(err)
	}
	if got := steamLoadLibrary(); len(got.Games) != 1 || got.Games[0].AppID != 400 {
		t.Fatalf("current library was not saved: %+v", got)
	}
}

func TestSteamRequestErrorDoesNotLogCredentials(t *testing.T) {
	cause := errors.New("connection refused")
	err := steamRequestError(&url.Error{Op: "Get", URL: "https://api.steampowered.com/?access_token=secret-token", Err: cause})
	if strings.Contains(err.Error(), "secret-token") || !errors.Is(err, cause) {
		t.Fatalf("request error leaked a token or lost its cause: %v", err)
	}
}

func TestSteamOwnedGamesEmptyLibrary(t *testing.T) {
	if games, err := steamParseOwnedGames([]byte(`{"response":{"game_count":0}}`)); err != nil || len(games) != 0 {
		t.Fatalf("empty account: games=%v err=%v", games, err)
	}
	if _, err := steamParseOwnedGames([]byte(`{"response":{}}`)); !errors.Is(err, errSteamPrivate) {
		t.Fatalf("hidden account: err=%v", err)
	}
}

func TestSteamGamesLoginRedirect(t *testing.T) {
	start, _ := url.Parse("https://steamcommunity.com/profiles/76561197960435530/games/?tab=all")
	login, _ := url.Parse("https://steamcommunity.com/login/?redir=games")
	if !steamGamesLoginRedirect(start, login) {
		t.Fatal("Steam login redirect was not recognized")
	}
	profile, _ := url.Parse("https://steamcommunity.com/profiles/76561197960435530/")
	if steamGamesLoginRedirect(start, profile) {
		t.Fatal("ordinary profile redirect was mistaken for a login")
	}
}
