package main

// wasm4_test.go — the WASM-4 carts screen's readers of outside data:
// wasm4.org's carts.json (read loosely), GitHub's listing of the carts
// folder, a cart's manual, and the check that a download is a WebAssembly
// module. The ids they accept become URLs and file names, so the id rule is
// pinned too.

import "testing"

func TestWASM4ParseCartsJSON(t *testing.T) {
	raw := []byte(`{"carts": [
		{"slug": "rolly-dango", "title": "Rolly Dango", "author": "William <wguimont>", "date": "2022-01-15T10:00:00Z"},
		{"id": "zxz", "name": "ZXZ", "author": {"name": "Jane", "github": "jane"}},
		{"wasm": "/carts/plctfarmer.wasm", "authors": ["A <a@example.com>", "B"]},
		{"slug": "../etc/passwd", "title": "Bad"},
		{"slug": "rolly-dango", "title": "Duplicate"},
		{"title": "No id"}
	]}`)
	carts, err := wasm4ParseCartsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(carts) != 3 {
		t.Fatalf("got %d carts, want 3 (bad id, duplicate and no id dropped): %+v", len(carts), carts)
	}
	// Sorted by title: Plctfarmer, Rolly Dango, ZXZ.
	if c := carts[0]; c.Slug != "plctfarmer" || c.Title != "Plctfarmer" || c.Author != "A, B" {
		t.Fatalf("id from the file name: %+v", c)
	}
	if c := carts[1]; c.Slug != "rolly-dango" || c.Title != "Rolly Dango" || c.Author != "William" || c.Date != "2022-01-15" {
		t.Fatalf("first form: %+v", c)
	}
	if c := carts[2]; c.Slug != "zxz" || c.Title != "ZXZ" || c.Author != "Jane" {
		t.Fatalf("id/name and an author object: %+v", c)
	}
	// A bare list works too; a list with no cart is an error.
	if carts, err := wasm4ParseCartsJSON([]byte(`[{"slug": "nyancat"}]`)); err != nil || len(carts) != 1 || carts[0].Title != "Nyancat" {
		t.Fatalf("bare list: %+v %v", carts, err)
	}
	for _, bad := range []string{`{}`, `[]`, `{"carts": [{"slug": "a/b"}]}`, `not json`} {
		if _, err := wasm4ParseCartsJSON([]byte(bad)); err == nil {
			t.Fatalf("%s: want an error", bad)
		}
	}
}

func TestWASM4ParseCartTree(t *testing.T) {
	tree := []byte(`{"sha": "x", "tree": [
		{"path": "rolly-dango.wasm", "type": "blob"},
		{"path": "rolly-dango.png", "type": "blob"},
		{"path": "rolly-dango.md", "type": "blob"},
		{"path": "sub", "type": "tree"},
		{"path": "sub/x.wasm", "type": "blob"},
		{"path": ".hidden.wasm", "type": "blob"},
		{"path": "minesweeper.wasm", "type": "blob"}
	], "truncated": false}`)
	carts, err := wasm4ParseCartTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(carts) != 2 || carts[0].Slug != "minesweeper" || carts[1].Slug != "rolly-dango" || carts[1].Title != "Rolly Dango" {
		t.Fatalf("trees API: %+v", carts)
	}
	contents := []byte(`[{"name": "zoopzoop.wasm", "type": "file"}, {"name": "zoopzoop.md", "type": "file"}, {"name": "dir.wasm", "type": "dir"}]`)
	if carts, err := wasm4ParseCartTree(contents); err != nil || len(carts) != 1 || carts[0].Slug != "zoopzoop" {
		t.Fatalf("contents API: %+v %v", carts, err)
	}
	if _, err := wasm4ParseCartTree([]byte(`{"message": "Not Found"}`)); err == nil {
		t.Fatal("an API error answer must not read as an empty list")
	}
}

func TestWASM4ParseCartMD(t *testing.T) {
	md := []byte("\ufeff---\r\nauthor: Bruno Garcia <aduros>\r\ndate: 2021-09-01\r\n---\r\n\r\n# Snake\r\n\r\nEat the fruit.\r\n# Not the title\r\n")
	title, author, date := wasm4ParseCartMD(md)
	if title != "Snake" || author != "Bruno Garcia" || date != "2021-09-01" {
		t.Fatalf("got %q %q %q", title, author, date)
	}
	if title, author, date := wasm4ParseCartMD([]byte("# Only a title\n")); title != "Only a title" || author != "" || date != "" {
		t.Fatalf("no front matter: %q %q %q", title, author, date)
	}
	if _, _, date := wasm4ParseCartMD([]byte("---\ndate: yesterday\n---\n")); date != "" {
		t.Fatalf("a bad date must be dropped, got %q", date)
	}
}

func TestWASM4CartChecks(t *testing.T) {
	if !wasm4CartOK([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0x01}) {
		t.Fatal("a WebAssembly module was refused")
	}
	for _, bad := range [][]byte{nil, []byte("\x00asm"), []byte("<html>\x00\x00\x00\x00"), {0x00, 0x61, 0x73, 0x6d, 0x02, 0x00, 0x00, 0x00}} {
		if wasm4CartOK(bad) {
			t.Fatalf("%q accepted as a cart", bad)
		}
	}
	for _, ok := range []string{"rolly-dango", "zxz", "A_1"} {
		if !wasm4SlugOK(ok) {
			t.Fatalf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-x", "../x", "a/b", "a.b", "a b", "con\x00", "con", "NUL", "com1"} {
		if wasm4SlugOK(bad) {
			t.Fatalf("%q accepted as a cart id", bad)
		}
	}
}
