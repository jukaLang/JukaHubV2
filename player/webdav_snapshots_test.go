package main

// webdav_snapshots_test.go — SYS-19: versioned snapshots on a WebDAV server.
// A fake server stands in for Nextcloud: PUT, GET, MKCOL, MOVE, DELETE and a
// depth-1 PROPFIND, over 127.0.0.1 (an address webdavCleanURL accepts).

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// webdavTestServer is an in-memory WebDAV folder.
type webdavTestServer struct {
	mu      sync.Mutex
	files   map[string][]byte
	dirs    map[string]bool
	failPut atomic.Bool
}

func (s *webdavTestServer) put(p string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[p] = append([]byte(nil), body...)
}

func (s *webdavTestServer) get(p string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[p]
	return append([]byte(nil), b...), ok
}

func (s *webdavTestServer) delete(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, p)
}

func (s *webdavTestServer) mkdir(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p != "/" && p != "" {
		s.dirs[p] = true
		p = strings.TrimSuffix(p[:strings.LastIndex(p, "/")+1], "/")
	}
	s.dirs["/"] = true
}

func (s *webdavTestServer) isDir(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == "/" {
		return true
	}
	return s.dirs[p] || s.dirs[strings.TrimSuffix(p, "/")]
}

// children lists the entries directly under dir.
func (s *webdavTestServer) children(dir string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir = strings.TrimSuffix(dir, "/") + "/"
	seen := map[string]bool{}
	var out []string
	for p := range s.files {
		if strings.HasPrefix(p, dir) {
			rest := strings.TrimPrefix(p, dir)
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				rest = rest[:i+1]
			}
			if rest != "" && !seen[rest] {
				seen[rest] = true
				out = append(out, dir+rest)
			}
		}
	}
	for p := range s.dirs {
		if strings.HasPrefix(p, dir) {
			rest := strings.TrimPrefix(p, dir)
			if rest != "" && !strings.Contains(strings.TrimSuffix(rest, "/"), "/") && !seen[rest] {
				seen[rest] = true
				out = append(out, dir+strings.TrimSuffix(rest, "/")+"/")
			}
		}
	}
	return out
}

func webdavTestHandler(s *webdavTestServer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch r.Method {
		case "PROPFIND":
			if !s.isDir(p) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
			writeOne := func(path string, dir bool, size int64) {
				href := (&url.URL{Path: path}).EscapedPath()
				_, _ = io.WriteString(w, `<D:response><D:href>`)
				_ = xml.EscapeText(w, []byte(href))
				_, _ = io.WriteString(w, `</D:href><D:propstat><D:prop>`)
				if dir {
					_, _ = io.WriteString(w, `<D:resourcetype><D:collection/></D:resourcetype>`)
				} else {
					_, _ = io.WriteString(w, `<D:resourcetype/><D:getcontentlength>`)
					_, _ = io.WriteString(w, fmt.Sprint(size))
					_, _ = io.WriteString(w, `</D:getcontentlength><D:getlastmodified>`)
					_, _ = io.WriteString(w, time.Now().UTC().Format(http.TimeFormat))
					_, _ = io.WriteString(w, `</D:getlastmodified>`)
				}
				_, _ = io.WriteString(w, `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
			}
			if p != "/" {
				writeOne(p, true, 0)
			} else {
				writeOne("/", true, 0)
			}
			if strings.TrimSpace(r.Header.Get("Depth")) != "0" {
				for _, child := range s.children(p) {
					if strings.HasSuffix(child, "/") {
						writeOne(child, true, 0)
					} else {
						b, _ := s.get(child)
						writeOne(child, false, int64(len(b)))
					}
				}
			}
			_, _ = io.WriteString(w, `</D:multistatus>`)
		case http.MethodGet:
			b, ok := s.get(p)
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(b)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if s.failPut.Load() {
				// A cut-off body is stored, then the request fails: the
				// upload must not touch the last good version.
				s.put(p, body[:len(body)/2])
				http.Error(w, "interrupted", http.StatusInternalServerError)
				return
			}
			s.put(p, body)
			w.WriteHeader(http.StatusCreated)
		case "MKCOL":
			s.mkdir(p)
			w.WriteHeader(http.StatusCreated)
		case "MOVE":
			dst, err := url.Parse(r.Header.Get("Destination"))
			if err != nil || dst.Path == "" {
				http.Error(w, "bad destination", http.StatusBadRequest)
				return
			}
			b, ok := s.get(p)
			if !ok {
				http.NotFound(w, r)
				return
			}
			s.put(dst.Path, b)
			s.delete(p)
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			s.delete(p)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	})
}

func webdavTestConn(t *testing.T) (*webdavConn, *webdavTestServer) {
	t.Helper()
	state := &webdavTestServer{files: map[string][]byte{}, dirs: map[string]bool{"/": true}}
	srv := httptest.NewServer(webdavTestHandler(state))
	t.Cleanup(srv.Close)
	base, err := webdavCleanURL(srv.URL + "/dav/")
	if err != nil {
		t.Fatalf("test server address: %v", err)
	}
	conn, err := webdavNewConn(base, "", "", "")
	if err != nil {
		t.Fatalf("webdavNewConn: %v", err)
	}
	state.mkdir(conn.top.Path)
	return conn, state
}

func webdavTestSnapshotAt() time.Time {
	return time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
}

func TestWebdavSnapshotUploadListFetchAndRestore(t *testing.T) {
	ssyncTestFastKDF(t)
	localDir, _ := ssyncTestState(t)
	ssyncTestWrite(t, localDir, "retroarch-saves/Game.srm", "SAVE-V1", time.Now())
	conn, state := webdavTestConn(t)
	const pass = "correct horse battery"
	ctx := context.Background()

	snap, err := webdavSnapshotUpload(ctx, conn, ssyncTestRoots(localDir), pass, webdavTestSnapshotAt(), nil)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !webdavSnapshotValidName(snap.Name) || snap.Size <= 0 {
		t.Fatalf("upload returned %+v", snap)
	}
	list, err := webdavSnapshotList(ctx, conn)
	if err != nil || len(list) != 1 || list[0].Name != snap.Name || list[0].Size != snap.Size {
		t.Fatalf("list = %+v, %v; want the uploaded snapshot", list, err)
	}
	raw, ok := state.get("/dav/JukaHub Saves/snapshots/" + snap.Name)
	if !ok {
		t.Fatal("the snapshot is not on the server")
	}
	if strings.Contains(string(raw), "SAVE-V1") {
		t.Fatal("the save left the device in plaintext")
	}
	if strings.Contains(string(raw), "retroarch-saves") {
		t.Fatal("the snapshot's file names are readable on the server")
	}

	// The version is restorable after the local save changed.
	ssyncTestWrite(t, localDir, "retroarch-saves/Game.srm", "SAVE-V2", time.Now())
	dir := t.TempDir()
	res, err := webdavSnapshotRestore(ctx, conn, list[0], pass, ssyncTestRoots(localDir), dir, nil)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Pulled == 0 {
		t.Fatalf("restore copied nothing: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(localDir, "retroarch-saves", "Game.srm"))
	if err != nil || string(got) != "SAVE-V1" {
		t.Fatalf("restored save = %q, %v; want SAVE-V1", got, err)
	}
	// The import takes a Save Manager snapshot of what it replaced first.
	entries, err := os.ReadDir(saveManagerDir())
	if err != nil || len(entries) == 0 {
		t.Fatalf("no Save Manager snapshot of the replaced save: %v", err)
	}
}

func TestWebdavSnapshotInterruptedUploadKeepsEarlierVersion(t *testing.T) {
	ssyncTestFastKDF(t)
	localDir, _ := ssyncTestState(t)
	ssyncTestWrite(t, localDir, "retroarch-saves/Game.srm", "SAVE-V1", time.Now())
	conn, state := webdavTestConn(t)
	const pass = "correct horse battery"
	ctx := context.Background()

	first, err := webdavSnapshotUpload(ctx, conn, ssyncTestRoots(localDir), pass, webdavTestSnapshotAt(), nil)
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	state.failPut.Store(true)
	if _, err := webdavSnapshotUpload(ctx, conn, ssyncTestRoots(localDir), pass, webdavTestSnapshotAt().Add(time.Minute), nil); err == nil {
		t.Fatal("an interrupted upload reported success")
	}
	list, err := webdavSnapshotList(ctx, conn)
	if err != nil || len(list) != 1 || list[0].Name != first.Name {
		t.Fatalf("after the interrupted upload list = %+v, %v; want only the first snapshot", list, err)
	}
	if _, err := webdavSnapshotFetch(ctx, conn, list[0].Name, t.TempDir()); err != nil {
		t.Fatalf("the earlier snapshot is no longer readable: %v", err)
	}
}

func TestWebdavSnapshotNamesStayInsideTheFolder(t *testing.T) {
	if got := webdavSnapshotName("Living Room PC", webdavTestSnapshotAt()); got != "snapshot-Living-Room-PC-20261128-120000.jukasaves" {
		t.Fatalf("name = %q", got)
	}
	for _, bad := range []string{"", "..", "../x.jukasaves", "a/b.jukasaves", `a\b.jukasaves`, "x.txt"} {
		if webdavSnapshotValidName(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
	for _, good := range []string{"snapshot-PC-20261128-120000.jukasaves", "a.jukasaves"} {
		if !webdavSnapshotValidName(good) {
			t.Fatalf("refused %q", good)
		}
	}
}
