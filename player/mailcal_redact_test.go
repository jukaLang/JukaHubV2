package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMailcalSecretChangeRefreshesLogRedaction(t *testing.T) {
	oldPath := mailcalSettingsPath
	path := filepath.Join(t.TempDir(), "mailcal.json")
	mailcalSettingsPath = func() string { return path }
	mailcalStore.mu.Lock()
	oldLoaded, oldValue := mailcalStore.loaded, mailcalStore.value
	mailcalStore.loaded, mailcalStore.value = true, mailcalSettings{}
	mailcalStore.mu.Unlock()
	oldList := logStoredSecrets.Load()
	oldFrom, oldMailSum, oldMail := logStoredSecretsFrom, logStoredSecretsMailSum, logStoredSecretsMail
	oldConfig := appConfig
	appConfig = &Config{}
	logStoredSecretsFrom, logStoredSecretsMailSum, logStoredSecretsMail = 0, 0, nil
	logStoredSecretsSet(nil, nil)
	tbxUIQueue.mu.Lock()
	oldQueue := tbxUIQueue.fns
	tbxUIQueue.fns = nil
	tbxUIQueue.mu.Unlock()
	t.Cleanup(func() {
		tbxUIQueue.mu.Lock()
		tbxUIQueue.fns = oldQueue
		tbxUIQueue.mu.Unlock()
		mailcalStore.mu.Lock()
		mailcalStore.loaded, mailcalStore.value = oldLoaded, oldValue
		mailcalStore.mu.Unlock()
		mailcalSettingsPath = oldPath
		logStoredSecrets.Store(oldList)
		logStoredSecretsFrom, logStoredSecretsMailSum, logStoredSecretsMail = oldFrom, oldMailSum, oldMail
		appConfig = oldConfig
	})

	if err := mailcalSettingsUpdate(func(s *mailcalSettings) { s.NoMailNotify = true }); err != nil {
		t.Fatal(err)
	}
	tbxUIQueue.mu.Lock()
	queued := len(tbxUIQueue.fns)
	tbxUIQueue.mu.Unlock()
	if queued != 0 {
		t.Fatal("a non-secret setting queued a redaction rebuild")
	}

	const oldSecret = "falcon-rose-8424"
	done := make(chan error, 1)
	go func() {
		done <- mailcalSettingsUpdate(func(s *mailcalSettings) {
			s.Accounts = []mailcalAccount{{ID: "mail", Password: oldSecret}}
		})
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	line := "imap server echoed " + oldSecret
	// The store can still be held by a later sync when the first queued
	// refresh lands. It must retry, then use the latest stored value.
	mailcalStore.mu.Lock()
	tbxDrainUI()
	mailcalStore.mu.Unlock()
	if got := redactLogSecrets(line); got != line {
		t.Fatalf("redaction rebuilt while the store was busy: %q", got)
	}
	tbxDrainUI()
	if got := redactLogSecrets(line); strings.Contains(got, oldSecret) || !strings.Contains(got, "[redacted]") {
		t.Fatalf("new mail password reached the log: %q", got)
	}

	const nextSecret = "cobalt-iris-6517"
	if err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		s.Accounts[0].Password = nextSecret
	}); err != nil {
		t.Fatal(err)
	}
	tbxDrainUI()
	if got := redactLogSecrets("imap server echoed " + nextSecret); strings.Contains(got, nextSecret) {
		t.Fatalf("replacement mail password reached the log: %q", got)
	}
	if got := redactLogSecrets(line); got != line {
		t.Fatalf("old password still redacted after replacement: %q", got)
	}

	const calendarSecret = "wren-ginger-4677"
	if err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		s.Calendars = []mailcalCalendar{{ID: "cal", Password: calendarSecret}}
	}); err != nil {
		t.Fatal(err)
	}
	tbxDrainUI()
	if got := redactLogSecrets("calendar reported " + calendarSecret); strings.Contains(got, calendarSecret) {
		t.Fatalf("new calendar password reached the log: %q", got)
	}
}
