package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Loopback CalDAV (HTTP) server for write-back tests ──────────────────

type fakeCalDAV struct {
	mu      sync.Mutex
	store   map[string]fakeCalDAVRes // path -> resource
	created atomic.Int32
	updated atomic.Int32
	deleted atomic.Int32
}

type fakeCalDAVRes struct {
	body string
	etag string
}

func newFakeCalDAV(t *testing.T) (*httptest.Server, *fakeCalDAV) {
	t.Helper()
	f := &fakeCalDAV{store: map[string]fakeCalDAVRes{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handle(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeCalDAV) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	switch r.Method {
	case "PUT":
		inm := r.Header.Get("If-None-Match")
		im := r.Header.Get("If-Match")
		if inm == "*" {
			if _, ok := f.store[path]; ok {
				w.WriteHeader(412)
				return
			}
			f.store[path] = fakeCalDAVRes{body: readBody(r), etag: fmt.Sprintf("\"e%d\"", f.created.Add(1))}
			w.Header().Set("ETag", f.store[path].etag)
			w.WriteHeader(201)
			f.created.Add(0) // noop
			return
		}
		res, ok := f.store[path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if im != "" && im != res.etag {
			w.WriteHeader(412)
			return
		}
		res.body = readBody(r)
		res.etag = fmt.Sprintf("\"u%d\"", f.updated.Add(1))
		f.store[path] = res
		w.Header().Set("ETag", res.etag)
		w.WriteHeader(204)
	case "DELETE":
		res, ok := f.store[path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if im := r.Header.Get("If-Match"); im != "" && im != res.etag {
			w.WriteHeader(412)
			return
		}
		delete(f.store, path)
		f.deleted.Add(1)
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestCalDAVPutCreateUpdateConflictDelete(t *testing.T) {
	srv, f := newFakeCalDAV(t)
	prev := caldavWriteHTTPClient
	caldavWriteHTTPClient = func() *http.Client { return srv.Client() }
	t.Cleanup(func() { caldavWriteHTTPClient = prev })

	src := calSource{kind: mailcalCalCalDAV, url: srv.URL + "/cal/", user: "u", pass: "p"}
	ev := calEvent{UID: "ev1", Title: "Dentist", Start: mustParseTime("2026-01-02T10:00:00Z"), End: mustParseTime("2026-01-02T11:00:00Z")}

	// Create.
	got, err := calsyncPushEvent(context.Background(), src, ev)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.URL == "" || got.ETag == "" {
		t.Fatalf("create did not return URL/ETag: %+v", got)
	}
	if f.created.Load() != 1 {
		t.Fatalf("created count = %d, want 1", f.created.Load())
	}

	// Update with the right ETag.
	got.Title = "Dentist (moved)"
	got2, err := calsyncPushEvent(context.Background(), src, got)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got2.ETag == got.ETag {
		t.Fatalf("update did not refresh ETag: %s", got2.ETag)
	}

	// Update with a stale ETag must conflict.
	stale := got
	stale.ETag = "\"stale\""
	stale.Title = "should not land"
	if _, err := calsyncPushEvent(context.Background(), src, stale); err == nil {
		t.Fatal("stale update did not conflict")
	} else if !strings.Contains(err.Error(), "changed on the server") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Delete.
	if err := calsyncDeleteEvent(context.Background(), src, got2); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if f.deleted.Load() != 1 {
		t.Fatalf("deleted count = %d, want 1", f.deleted.Load())
	}

	// Delete again -> not found.
	if err := calsyncDeleteEvent(context.Background(), src, got2); err == nil {
		t.Fatal("second delete did not error")
	}
}

func TestCalDAVReadOnlyRefusesWrites(t *testing.T) {
	src := calSource{kind: "file", url: "http://x", user: "u", pass: "p"}
	ev := calEvent{UID: "x", Title: "t"}
	if _, err := calsyncPushEvent(context.Background(), src, ev); err == nil {
		t.Fatal("push to read-only source did not error")
	}
	if err := calsyncDeleteEvent(context.Background(), src, ev); err == nil {
		t.Fatal("delete on read-only source did not error")
	}
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
