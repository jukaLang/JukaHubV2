package main

// homebrew_store_test.go — Get Homebrew's readers of outside data: itch.io's
// browse feeds, data.json, the uploads and owned-keys answers, the choice of
// cartridge among a game's files and inside a zip, installed.json, and the
// key and file-name rules that keep pasted text and archive names harmless.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomebrewParseFeed(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>itch.io</title>
<item><title>[Free] [Platformer] Tobu Tobu Girl</title><plainTitle>Tobu Tobu Girl</plainTitle>
<link>https://tangramgames.itch.io/tobutobugirl</link><guid>https://tangramgames.itch.io/tobutobugirl</guid>
<description><![CDATA[<p>An arcade platformer&nbsp;for the <b>Game Boy</b>.</p><ul><li>Free</li><li>Fun</li></ul><script>x()</script>]]></description>
<pubDate>Tue, 02 Jan 2018 10:00:00 GMT</pubDate><createDate>2017-12-30 09:00:00</createDate>
<imageurl>https://img.itch.zone/aW1n/315x250%23c/abc.png</imageurl><price>$0.00</price><currency>USD</currency></item>
<item><title>[$4.99] Paid Cart</title><link>https://dev.itch.io/paid-cart?ref=x</link><price>$4.99</price></item>
<item><title>Not a game page</title><link>https://example.com/game</link></item>
<item><title>Dup</title><plainTitle>Dup</plainTitle><link>https://tangramgames.itch.io/tobutobugirl/</link></item>
</channel></rss>`)
	items, err := hbParseFeed(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (bad link and duplicate dropped): %+v", len(items), items)
	}
	a := items[0]
	if a.Title != "Tobu Tobu Girl" || a.Link != "https://tangramgames.itch.io/tobutobugirl" || !hbPriceFree(a.Price) {
		t.Fatalf("first item: %+v", a)
	}
	if a.Image == "" || a.Pub.IsZero() || a.Created.IsZero() || a.Created.Year() != 2017 {
		t.Fatalf("image or dates lost: %+v", a)
	}
	if strings.Contains(a.Desc, "<") || strings.Contains(a.Desc, "x()") || !strings.Contains(a.Desc, "• Free") {
		t.Fatalf("description not plain text: %q", a.Desc)
	}
	b := items[1]
	if b.Title != "Paid Cart" || b.Link != "https://dev.itch.io/paid-cart" || hbPriceFree(b.Price) {
		t.Fatalf("second item: %+v", b)
	}
	if _, err := hbParseFeed([]byte("<html>not a feed")); err == nil {
		if items, _ := hbParseFeed([]byte("<html>not a feed")); len(items) != 0 {
			t.Fatal("a non-feed gave items")
		}
	}
}

func TestHomebrewLinksAndPrices(t *testing.T) {
	for in, want := range map[string]string{
		"https://Dev.itch.io/Game-1/":           "https://dev.itch.io/Game-1",
		"http://dev.itch.io/game":               "https://dev.itch.io/game",
		"https://itch.io/games/tag-nes":         "",
		"https://evil.com/x.itch.io/game":       "",
		"https://dev.itch.io.evil.com/game":     "",
		"https://user:pw@dev.itch.io/game":      "",
		"https://dev.itch.io/game/devlog":       "",
		"https://dev.itch.io:8443/game":         "",
		"https://dev.itch.io/..%2f..%2fetc":     "",
		"javascript:alert(1)//dev.itch.io/game": "",
	} {
		if got := hbNormalizeLink(in); got != want {
			t.Errorf("hbNormalizeLink(%q) = %q, want %q", in, got, want)
		}
	}
	for p, free := range map[string]bool{"$0.00": true, "0,00 €": true, "": true, "Free": true, "$4.99": false, "€1": false, "$10.00": false} {
		if hbPriceFree(p) != free {
			t.Errorf("hbPriceFree(%q) != %v", p, free)
		}
	}
}

func TestHomebrewParseGameData(t *testing.T) {
	d, err := hbParseGameData([]byte(`{"id":12345,"title":"Tobu","price":"$0.00","cover_image":"https://img.itch.zone/c.png",
		"authors":[{"name":"Tangram Games","url":"https://tangramgames.itch.io"}],"links":{"self":"x"}}`))
	if err != nil || d.ID != 12345 || d.Author != "Tangram Games" || d.Cover == "" || d.Title != "Tobu" {
		t.Fatalf("data.json: %+v %v", d, err)
	}
	if _, err := hbParseGameData([]byte(`{"title":"no id"}`)); err == nil {
		t.Fatal("a game without an id was accepted")
	}
	if d, _ := hbParseGameData([]byte(`{"id":"77","cover_image":"http://insecure/x.png"}`)); d.ID != 77 || d.Cover != "" {
		t.Fatalf("string id or insecure cover: %+v", d)
	}
}

func TestHomebrewUploadsAndPick(t *testing.T) {
	ups, err := hbParseUploads([]byte(`{"uploads":[
		{"id":1,"filename":"game-win.zip","type":"default","p_windows":true,"size":9000000},
		{"id":2,"filename":"soundtrack.zip","type":"soundtrack"},
		{"id":3,"filename":"Game_demo.gb","type":"default","demo":true,"size":65536},
		{"id":4,"filename":"Game.gbc","type":"default","size":131072,"md5_hash":"ABC","build_id":9,"updated_at":"2024-01-01T00:00:00Z"},
		{"id":5,"filename":"game.zip","type":"default","traits":["p_android"]},
		{"id":6,"filename":"web.zip","type":"html"},
		{"filename":"no-id.gb"}
	]}`))
	if err != nil || len(ups) != 6 {
		t.Fatalf("uploads: %d %v", len(ups), err)
	}
	if !ups[0].PC || !ups[4].Android || ups[3].MD5 != "abc" || ups[3].BuildID != 9 || ups[3].Updated == "" {
		t.Fatalf("fields: %+v", ups)
	}
	gb, gbc := uint16(1)<<uint(hbConsoleIndex("gb")), uint16(1)<<uint(hbConsoleIndex("gbc"))
	// Listed under Game Boy: the full .gbc beats the .gb demo (a full game
	// outranks a demo of another console only when the console matches).
	pick, ok := hbPickUpload(ups, gb|gbc)
	if !ok || pick.Upload.ID != 4 || pick.Zip || hbConsoles[pick.Console].id != "gbc" {
		t.Fatalf("pick: %+v %v", pick, ok)
	}
	pick, ok = hbPickUpload(ups, gb)
	if !ok || pick.Upload.ID != 3 {
		t.Fatalf("Game Boy pick: %+v %v", pick, ok)
	}
	// Only PC, Android, web and soundtrack files: no cartridge.
	if _, ok := hbPickUpload([]hbUpload{ups[0], ups[1], ups[4], ups[5]}, gb); ok {
		t.Fatal("a PC/Android/web/soundtrack file was picked")
	}
	// A plain zip is the last choice.
	if pick, ok := hbPickUpload([]hbUpload{{ID: 8, Filename: "rom.zip", Type: "default"}}, gb); !ok || !pick.Zip || pick.Console != -1 {
		t.Fatalf("zip pick: %+v %v", pick, ok)
	}
	// A Mega Drive .bin counts only for a Mega Drive game.
	md := uint16(1) << uint(hbConsoleIndex("md"))
	bin := []hbUpload{{ID: 9, Filename: "game.bin", Type: "default"}}
	if _, ok := hbPickUpload(bin, gb); ok {
		t.Fatal(".bin picked for a Game Boy game")
	}
	if p, ok := hbPickUpload(bin, md); !ok || hbConsoles[p.Console].id != "md" {
		t.Fatal(".bin not picked for a Mega Drive game")
	}
	if _, err := hbParseUploads([]byte(`{"errors":["invalid key"]}`)); err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("API error not surfaced: %v", err)
	}
}

func TestHomebrewUploadChanged(t *testing.T) {
	rec := hbInstallRec{UploadID: 4, MD5: "abc", BuildID: 9, Updated: "2024-01-01T00:00:00Z"}
	gbc := uint16(1) << uint(hbConsoleIndex("gbc"))
	same := []hbUpload{{ID: 4, Filename: "Game.gbc", MD5: "abc", BuildID: 9, Updated: "2024-01-01T00:00:00Z"}}
	if hbUploadChanged(rec, same, gbc) {
		t.Fatal("unchanged upload reported as an update")
	}
	newer := []hbUpload{{ID: 4, Filename: "Game.gbc", MD5: "def", BuildID: 9, Updated: "2024-01-01T00:00:00Z"}}
	if !hbUploadChanged(rec, newer, gbc) {
		t.Fatal("a new md5 is not an update")
	}
	replaced := []hbUpload{{ID: 10, Filename: "Game v2.gbc"}}
	if !hbUploadChanged(rec, replaced, gbc) {
		t.Fatal("a replaced cartridge is not an update")
	}
	if hbUploadChanged(rec, []hbUpload{{ID: 11, Filename: "Game.exe"}}, gbc) {
		t.Fatal("a PC build replacing the cartridge is an update")
	}
}

func TestHomebrewOwnedKeysAndProfile(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"page":1,"per_page":2,"owned_keys":[`)
	b.WriteString(`{"id":501,"game_id":42,"game":{"id":42,"title":"Bought","url":"https://dev.itch.io/bought","cover_url":"https://img.itch.zone/b.png","user":{"username":"dev","display_name":"Dev"}}},`)
	b.WriteString(`{"id":0,"game_id":43}]}`)
	got, more, err := hbParseOwnedKeys([]byte(b.String()))
	if err != nil || len(got) != 1 || !more {
		t.Fatalf("owned keys: %+v more=%v err=%v", got, more, err)
	}
	if o := got[0]; o.KeyID != 501 || o.GameID != 42 || o.Link != "https://dev.itch.io/bought" || o.Author != "Dev" {
		t.Fatalf("owned game: %+v", o)
	}
	// A short page may not be the last; only an empty one (itch.io writes an
	// empty list as {}) ends the list.
	if _, more, _ := hbParseOwnedKeys([]byte(`{"per_page":50,"owned_keys":[{"id":7,"game_id":8}]}`)); !more {
		t.Fatal("a short page ended the owned list")
	}
	if _, more, err := hbParseOwnedKeys([]byte(`{"owned_keys":{}}`)); more || err != nil {
		t.Fatalf("an empty page did not end the owned list: %v %v", more, err)
	}
	if name, err := hbParseProfile([]byte(`{"user":{"username":"juka","display_name":""}}`)); err != nil || name != "juka" {
		t.Fatalf("profile: %q %v", name, err)
	}
	if _, err := hbParseProfile([]byte(`{"errors":["invalid key"]}`)); err == nil {
		t.Fatal("an error answer read as a profile")
	}
	if u := hbParseDownloadURL([]byte(`{"url":"http://cdn/x"}`)); u != "" {
		t.Fatalf("insecure download address accepted: %q", u)
	}
}

func TestHomebrewZipPickAndNames(t *testing.T) {
	gb := uint16(1) << uint(hbConsoleIndex("gb"))
	names := []string{"../../evil.gb", "__MACOSX/._game.gb", "docs/manual.pdf", "rom/Game.GB", "extra/other.nes", ".hidden.gb"}
	sizes := []int64{10, 10, 500, 65536, 99999, 10}
	idx, c := hbZipRomPick(names, sizes, gb)
	if idx != 3 || hbConsoles[c].id != "gb" {
		// "../../evil.gb" is a .gb too: it may be chosen only by its bytes,
		// never by its path, but the bigger, plainly named one wins here.
		t.Fatalf("zip pick = %d (%d)", idx, c)
	}
	if idx, _ := hbZipRomPick([]string{"readme.txt", "game.exe"}, []int64{1, 2}, gb); idx != -1 {
		t.Fatal("a zip without a cartridge gave one")
	}
	if hbRomExt("Celeste.P8.png") != ".p8.png" || hbConsoleForExt(".p8.png", 0) != hbConsoleIndex("pico8") {
		t.Fatal("PICO-8 cart pictures are not carts")
	}
	for in, want := range map[string]string{
		`Tobu Tobu Girl`:         "Tobu Tobu Girl",
		`../../etc/passwd`:       "etc passwd",
		`a:b*c?"d<e>f|g\h`:       "a b c d e f g h",
		"  ..  ":                 "fallback",
		"con":                    "fallback",
		"COM1":                   "fallback",
		strings.Repeat("x", 200): strings.Repeat("x", 80),
	} {
		if got := hbCleanFileName(in, "fallback"); got != want {
			t.Errorf("hbCleanFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHomebrewInstallsAndKey(t *testing.T) {
	dir := t.TempDir()
	f := hbInstallsFile{Version: 1, Games: []hbInstallRec{
		{Link: "https://dev.itch.io/game", Console: "gb", Path: filepath.Join(dir, "Game.gb"), GameID: 1, UploadID: 2},
		{Link: "https://evil.com/x", Console: "gb", Path: filepath.Join(dir, "x.gb")},
		{Link: "https://dev.itch.io/rel", Console: "gb", Path: "relative.gb"},
		{Link: "https://dev.itch.io/odd", Console: "n64", Path: filepath.Join(dir, "odd.z64")},
	}}
	raw, _ := json.Marshal(f)
	recs, _, err := hbParseInstalls(raw)
	if err != nil || len(recs) != 1 || recs["https://dev.itch.io/game"].UploadID != 2 {
		t.Fatalf("installs: %+v %v", recs, err)
	}
	if _, _, err := hbParseInstalls([]byte("{broken")); err == nil {
		t.Fatal("a broken installed.json parsed")
	}
	if k, err := hbCleanKey("  \"abcdefghijklmnopqrstuvwxyz0123\"  "); err != nil || k != "abcdefghijklmnopqrstuvwxyz0123" {
		t.Fatalf("key trim: %q %v", k, err)
	}
	for _, bad := range []string{"short", "has space in the middle of it ok", strings.Repeat("k", 300), "abcdefghijklmnopqrstuvwxyz\n0123"} {
		if _, err := hbCleanKey(bad); err == nil {
			t.Errorf("hbCleanKey accepted %q", bad)
		}
	}
}

func TestHomebrewEasyRPGZip(t *testing.T) {
	for _, c := range []struct {
		names []string
		root  string
		ok    bool
	}{
		{[]string{"RPG_RT.ldb", "RPG_RT.lmt", "Map0001.lmu"}, "", true},
		{[]string{"My Game/", "My Game/rpg_rt.LDB", "My Game/RPG_RT.lmt", "__MACOSX/My Game/._RPG_RT.ldb"}, "My Game", true},
		{[]string{`Game\RPG_RT.ldb`, `Game\RPG_RT.lmt`, "Backup/Old/RPG_RT.ldb", "Backup/Old/RPG_RT.lmt"}, "Game", true},
		{[]string{"a/b/RPG_RT.ldb", "a/b/RPG_RT.lmt"}, "", false},         // too deep
		{[]string{"RPG_RT.ldb"}, "", false},                               // no map tree
		{[]string{"Game/www/js/rpg_core.js", "Game/Game.exe"}, "", false}, // RPG Maker MV
		{[]string{"setup.exe", "readme.txt"}, "", false},
	} {
		root, err := hbEasyRPGZipRoot(c.names)
		if (err == nil) != c.ok || root != c.root {
			t.Errorf("hbEasyRPGZipRoot(%q) = %q, %v", c.names, root, err)
		}
	}
	if !hbEasyRPGFullPackage([]byte("\xef\xbb\xbf[RPG_RT]\r\nGameTitle=X\r\nFullPackageFlag = 1\r\n")) ||
		hbEasyRPGFullPackage([]byte("[RPG_RT]\nFullPackageFlag=0\n")) {
		t.Fatal("RPG_RT.ini FullPackageFlag misread")
	}
}
