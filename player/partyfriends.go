package main

// partyfriends.go — remembered party hosts.
//
// The room code alone is a one-night invite; keeping the last few hosts turns
// it into a friend list you can rejoin without retyping an address. Entries are
// just a display name plus the invite line the host showed, stored next to the
// other app data. This is deliberately local: no account, no server discovery.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	partyFriendsFileName = "partyfriends.json"
	partyFriendsLimit    = 12
)

type partyFriend struct {
	Name   string `json:"name"`
	Invite string `json:"invite"`
	Added  string `json:"added,omitempty"`
}

var partyFriendsStore struct {
	mu     sync.Mutex
	loaded bool
	items  []partyFriend
}

// partyFriendsPath is a variable so tests can use a temp file instead of the
// app data folder.
var partyFriendsPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, partyFriendsFileName)
}

func partyFriendsLoadLocked() {
	if partyFriendsStore.loaded {
		return
	}
	partyFriendsStore.loaded = true
	data, err := os.ReadFile(partyFriendsPath())
	if err != nil {
		return
	}
	var stored []partyFriend
	if json.Unmarshal(data, &stored) != nil {
		return
	}
	for _, f := range stored {
		if strings.TrimSpace(f.Invite) == "" {
			continue
		}
		partyFriendsStore.items = append(partyFriendsStore.items, f)
	}
}

func partyFriendsSaveLocked() error {
	path := partyFriendsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(partyFriendsStore.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// partyFriendAdd remembers a host. The invite line is the identity: rejoining
// the same address updates its name instead of adding a duplicate.
func partyFriendAdd(name, invite string) error {
	base, code, err := partyParseInvite(invite)
	if err != nil || code == "" {
		return errPartyUnknownRoom
	}
	invite = strings.TrimSpace(base + " " + code)
	name = partyMemberName(name)

	partyFriendsStore.mu.Lock()
	defer partyFriendsStore.mu.Unlock()
	partyFriendsLoadLocked()
	for i, f := range partyFriendsStore.items {
		if f.Invite == invite {
			partyFriendsStore.items[i].Name = name
			partyFriendsStore.items[i].Added = time.Now().UTC().Format(time.RFC3339)
			return partyFriendsSaveLocked()
		}
	}
	partyFriendsStore.items = append([]partyFriend{{
		Name:   name,
		Invite: invite,
		Added:  time.Now().UTC().Format(time.RFC3339),
	}}, partyFriendsStore.items...)
	if len(partyFriendsStore.items) > partyFriendsLimit {
		partyFriendsStore.items = partyFriendsStore.items[:partyFriendsLimit]
	}
	return partyFriendsSaveLocked()
}

func partyFriendRemove(invite string) {
	invite = strings.TrimSpace(invite)
	partyFriendsStore.mu.Lock()
	defer partyFriendsStore.mu.Unlock()
	partyFriendsLoadLocked()
	kept := partyFriendsStore.items[:0]
	for _, f := range partyFriendsStore.items {
		if f.Invite != invite {
			kept = append(kept, f)
		}
	}
	partyFriendsStore.items = kept
	_ = partyFriendsSaveLocked()
}

// partyFriends returns a copy so the caller can render while another thread
// saves without sharing the slice.
func partyFriends() []partyFriend {
	partyFriendsStore.mu.Lock()
	defer partyFriendsStore.mu.Unlock()
	partyFriendsLoadLocked()
	return append([]partyFriend(nil), partyFriendsStore.items...)
}

// partyHostName returns the display name of the room's host, used when saving a
// joined room to the friends list.
func partyHostName(snap partySnapshot) string {
	for _, m := range snap.Members {
		if m.ID == snap.HostID {
			return m.Name
		}
	}
	return snap.Name
}
