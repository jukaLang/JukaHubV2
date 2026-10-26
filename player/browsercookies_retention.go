package main

// browsercookies_retention.go — the saved session's age policy.
//
// Keeping the session is the point of data/browser-cookies.json: the launcher
// restarts every time the handheld sleeps, and retyping a login on a D-pad is
// miserable. Keeping it forever is not the same promise. A sign-in a user
// needed once — a consent gate on a page they will never open again — would
// otherwise sit on the card until they remembered the Forget chip exists.
//
// This file adds the missing half: a day count, chosen in Settings → General,
// after which a cookie stops being restored. The policy is opt-in (the shipped
// stop is Keep), it never touches the live jar's in-flight requests beyond a
// rebuild, and it is enforced at the two moments it can matter — when the
// session is restored from the card, and when the setting itself changes (so
// choosing 7 days drops the 10-day-old cookies immediately instead of
// pretending until the next launch).
//
// Age is measured from when a cookie was first stored, not from when the file
// was last written: a site that refreshes a token daily would otherwise look
// new forever. A record from a file written before this field existed has no
// first-seen stamp, so it is never treated as stale and its clock starts when
// the file is next loaded.

import (
	"fmt"
	"log"
	"time"
)

const (
	// cookieRetentionMaxDays bounds the config value. Beyond ten years the
	// setting is "Keep" with extra steps, and an absurd number must not turn
	// into a wrapped duration.
	cookieRetentionMaxDays = 3650
	// cookieRetentionDay is one day, spelled once so the comparison cannot
	// disagree with the label.
	cookieRetentionDay = 24 * time.Hour
)

// cookieRetentionSteps are the stops the Settings row cycles through, in days.
// Keep is first because it is the shipped default and the honest one: an empty
// card costs nothing, and a forgotten session is the failure mode the file was
// built to prevent.
var cookieRetentionSteps = []int{0, 7, 14, 30, 90}

// cookieRetentionDaysFromCustom reads the setting from a config's custom block.
// The value can be an int (written by the Settings row), a JSON float64 (the
// file on disk) or a string (a hand edit), so it goes through the same
// coercion every other custom setting uses.
func cookieRetentionDaysFromCustom(custom map[string]interface{}) int {
	v, ok := customValue(custom, "cookieRetentionDays")
	if !ok {
		return 0
	}
	days, ok := coerceInt(v)
	if !ok || days < 0 {
		return 0
	}
	if days > cookieRetentionMaxDays {
		days = cookieRetentionMaxDays
	}
	return days
}

// cookieRetentionDays is the setting as the browser uses it. A nil config — a
// test that never loaded one, or a jar built before startup finished — means
// off, never "expire everything".
func cookieRetentionDays(config *Config) int {
	if config == nil {
		return 0
	}
	return cookieRetentionDaysFromCustom(config.Variables.Custom)
}

// cookieRetentionLabel renders the setting for the Settings row and the toast.
func cookieRetentionLabel(days int) string {
	switch {
	case days <= 0:
		return "Keep"
	case days == 1:
		return "1 day"
	default:
		return fmt.Sprintf("%d days", days)
	}
}

// cookieRetentionShortLabel is the row's window in the fewest characters that
// stay readable. The row also carries when the session began, and "30 days"
// would push that date out of the 360px cell on the 800px panel; the toast and
// the docs keep the full words, where there is room for them.
func cookieRetentionShortLabel(days int) string {
	switch {
	case days <= 0:
		return "Keep"
	case days == 1:
		return "1d"
	default:
		return fmt.Sprintf("%dd", days)
	}
}

// browserSessionSince is when the session on the card began: the oldest
// first-seen stamp among the cookies the jar still holds. That is the reference
// point the retention window is measured from — a 30-day window on a session
// started today has 30 days left, not 30 days from whenever a page was last
// visited. A jar with no stamps (a file written before they existed, not yet
// stamped by a load) reports false rather than a zero date.
func browserSessionSince() (time.Time, bool) {
	jar, ok := browserCookies().(*browserCookieJar)
	if !ok {
		return time.Time{}, false
	}
	return jar.sessionSince()
}

// sessionSince is browserSessionSince for a jar that is already in hand.
func (j *browserCookieJar) sessionSince() (time.Time, bool) {
	if j == nil {
		return time.Time{}, false
	}
	j.mu.Lock()
	records := j.snapshotLocked()
	j.mu.Unlock()
	var oldest time.Time
	found := false
	for _, rec := range records {
		seen, ok := cookieFirstSeen(rec)
		if !ok {
			continue
		}
		if !found || seen.Before(oldest) {
			oldest, found = seen, true
		}
	}
	return oldest, found
}

// cookieRetentionRowLabel is the Settings row's value: the window it cycles,
// then when the session that window measures was first stored. The date is part
// of the value rather than a second line because the answer to "30 days from
// when?" is what makes the setting legible.
func cookieRetentionRowLabel(days int) string {
	value := cookieRetentionShortLabel(days)
	if since, ok := browserSessionSince(); ok {
		value += " · since " + since.Local().Format("Jan 2")
	}
	return value
}

// cookieRowLabelKey is the Settings row's substituted value.
const cookieRowLabelKey = "cookieRowLabel"

// refreshCookieRetentionRow re-seeds the row from the jar as it stands now, so
// the date disappears when the session is forgotten and appears once a page has
// stored one. Cheap enough for a scene entry: it locks the jar and reads stamps,
// it does not touch the card.
func refreshCookieRetentionRow(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	value := cookieRetentionRowLabel(cookieRetentionDays(config))
	if current, _ := config.Variables.Custom[cookieRowLabelKey].(string); current != value {
		config.Variables.Custom[cookieRowLabelKey] = value
	}
}

// nextCookieRetentionDays returns the stop after current, wrapping at the end.
// A value that is not a stop (0 handled above, or a hand-edited 45) moves to
// the first stop that is larger, so the row still cycles sensibly.
func nextCookieRetentionDays(current int) int {
	for _, s := range cookieRetentionSteps {
		if s > current {
			return s
		}
	}
	return cookieRetentionSteps[0]
}

// cookieFirstSeen parses a record's first-seen stamp. A missing or unparsable
// stamp means "unknown", which is never stale.
func cookieFirstSeen(rec browserCookieRecord) (time.Time, bool) {
	if rec.FirstSeen == "" {
		return time.Time{}, false
	}
	seen, err := time.Parse(time.RFC3339, rec.FirstSeen)
	if err != nil {
		return time.Time{}, false
	}
	return seen, true
}

// staleBrowserCookie reports whether a record is past the retention window.
func staleBrowserCookie(rec browserCookieRecord, days int, now time.Time) bool {
	if days <= 0 {
		return false
	}
	seen, ok := cookieFirstSeen(rec)
	if !ok {
		return false
	}
	return now.Sub(seen) > time.Duration(days)*cookieRetentionDay
}

// stampBrowserCookies fills in a first-seen stamp for records saved before the
// field existed and reports whether anything changed, so the caller knows to
// write the file back once and start every clock from the upgrade.
func stampBrowserCookies(records []browserCookieRecord, now time.Time) bool {
	stamped := false
	for i := range records {
		if records[i].FirstSeen != "" {
			continue
		}
		records[i].FirstSeen = now.UTC().Format(time.RFC3339)
		stamped = true
	}
	return stamped
}

// browserStaleCookieSplit divides restored records into the ones the policy
// keeps and the ones it drops.
func browserStaleCookieSplit(records []browserCookieRecord, days int, now time.Time) (kept []browserCookieRecord, dropped int) {
	kept = records
	if days <= 0 {
		return kept, 0
	}
	kept = make([]browserCookieRecord, 0, len(records))
	for _, rec := range records {
		if staleBrowserCookie(rec, days, now) {
			dropped++
			continue
		}
		kept = append(kept, rec)
	}
	return kept, dropped
}

// browserPruneStaleCookies drops cookies older than the policy from the live
// jar and the card, and reports how many went. Nothing is rebuilt when every
// cookie is young enough, so calling it on a setting change is free.
func browserPruneStaleCookies(days int) int {
	if days <= 0 {
		return 0
	}
	browserJar.mu.Lock()
	jar := browserJar.jar
	browserJar.mu.Unlock()
	if jar == nil {
		return 0
	}

	now := time.Now()
	jar.mu.Lock()
	kept := make([]browserCookieRecord, 0, len(jar.recs))
	dropped := 0
	for _, rec := range jar.recs {
		if staleBrowserCookie(rec, days, now) {
			dropped++
			continue
		}
		kept = append(kept, rec)
	}
	jar.mu.Unlock()
	if dropped == 0 {
		return 0
	}
	browserReplaceCookieJar(kept)
	return dropped
}

// browserReplaceCookieJar rebuilds the session from the cookies that survive a
// policy change and retires the old jar, so a page load already on the wire
// cannot write a dropped cookie back. It is the same door Forget closes.
func browserReplaceCookieJar(records []browserCookieRecord) {
	fresh := newBrowserCookieJar(browserCookiesPath())
	if fresh == nil {
		return
	}
	fresh.restore(records)
	fresh.save()

	browserJar.mu.Lock()
	old := browserJar.jar
	browserJar.jar = fresh
	browserJar.dead = false
	browserJar.mu.Unlock()
	if old != nil && old != fresh {
		old.retire()
	}
}

// applyCookieRetention writes the chosen policy, prunes what it already makes
// stale, and returns how many cookies the change removed so the caller can say
// so instead of leaving the count to be discovered.
func applyCookieRetention(config *Config, days int) int {
	if days < 0 {
		days = 0
	}
	if days > cookieRetentionMaxDays {
		days = cookieRetentionMaxDays
	}
	if config != nil {
		config.Variables.Custom["cookieRetentionDays"] = days
		config.Variables.Custom["cookieRetentionLabel"] = cookieRetentionLabel(days)
		saveUserConfigDebounced(userConfigFromVariables(config))
	}
	pruned := browserPruneStaleCookies(days)
	if pruned > 0 {
		log.Printf("[browser] retention %s removed %d cookie(s)", cookieRetentionLabel(days), pruned)
	}
	// The prune can drop the session's oldest cookie, moving the start date the
	// row quotes, so the label follows the jar rather than the press.
	refreshCookieRetentionRow(config)
	return pruned
}

// forgetSavedCookiesFromSettings is the "Forget now" press beside the retention
// row: the same drop the Browser's chip runs, without leaving Settings and
// without reloading a page nobody is looking at. It shares the chip's confirm
// window, because it discards the same thing — a session that costs a login on a
// D-pad to get back — and one armed state is enough for both entry points.
func forgetSavedCookiesFromSettings(config *Config) bool {
	stored := browserCookiesCount()
	if stored == 0 {
		browserForgetConfirm.armedAt = time.Time{}
		ShowToast("No cookies are stored", ToastKindInfo)
		return false
	}
	if browserForgetConfirm.armedAt.IsZero() || time.Since(browserForgetConfirm.armedAt) > browserForgetConfirmWindow {
		browserForgetConfirm.armedAt = time.Now()
		PlayNavSound()
		ShowToast("Press again to drop "+browserCookieCountLabel(stored)+" from the card", ToastKindWarning)
		return false
	}
	browserForgetConfirm.armedAt = time.Time{}
	dropped, err := browserForgetCookies()
	if err != nil {
		ShowToast("Cannot forget cookies: "+err.Error(), ToastKindError)
		return false
	}
	PlayActivateSound()
	ShowToast("Forgot "+browserCookieCountLabel(dropped)+" — you can sign in again in the Browser", ToastKindInfo)
	// The session is gone, so its start date is too: the row must not keep
	// quoting a day that no cookie remembers.
	refreshCookieRetentionRow(config)
	return true
}
