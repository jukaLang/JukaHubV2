package main

// retroachievements.go — RetroAchievements on the handheld.
//
// The TrimUI usually already has an RA account configured: Cheevos keeps its
// Web API key in Saves/cheevos/apikey.txt, Spruce stores the username, and
// RetroArch's cheevos_username lives in the platform config. This module reads
// those files without changing them, then shows recent unlocks and completion
// progress in a controller-friendly overlay. The last successful sync is cached
// so the screen still works when Wi-Fi drops or the device boots offline.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	retroAchievementsEntryName = "RetroAchievements"

	raAPIBaseDefault = "https://retroachievements.org/API"

	raRecentWindowMinutes = 60 * 24 * 30 // 30 days of unlocks
	raMaxRecent           = 30
	raMaxProgress         = 30
	raCacheVersion        = 1
	raCacheTTL            = 10 * time.Minute
	raFetchTimeout        = 25 * time.Second
	raMaxResponseBytes    = 8 << 20
)

// raAPIBase is a variable so tests can point the client at an httptest server.
var raAPIBase = raAPIBaseDefault

// raHTTPClient is replaceable in tests.
var raHTTPClient = &http.Client{Timeout: raFetchTimeout}

// ── Credentials and settings ─────────────────────────────────────────────

type raSettings struct {
	Username string `json:"username"`
	APIKey   string `json:"apiKey,omitempty"`
}

type raCredentials struct {
	Username       string
	APIKey         string
	UsernameSource string
	KeySource      string
}

func (c raCredentials) complete() bool {
	return strings.TrimSpace(c.Username) != "" && strings.TrimSpace(c.APIKey) != ""
}

var (
	raSettingsPathOverride string
	raCachePathOverride    string

	// TrimUI/Spruce locations. These are read-only: JukaHub never edits the
	// Cheevos, Spruce or RetroArch files.
	raCheevosKeyPaths = []string{
		"/mnt/SDCARD/Saves/cheevos/apikey.txt",
		"/mnt/sdcard/Saves/cheevos/apikey.txt",
	}
	raSpruceConfigPaths = []string{
		"/mnt/SDCARD/Saves/spruce/spruce-config.json",
		"/mnt/sdcard/Saves/spruce/spruce-config.json",
	}
	raCheevosSettingsPaths = []string{
		"/mnt/SDCARD/Saves/cheevos/settings.json",
		"/mnt/sdcard/Saves/cheevos/settings.json",
	}
	raRetroArchConfigPaths = []string{
		"/mnt/SDCARD/RetroArch/.retroarch/retroarch.cfg",
		"/mnt/SDCARD/RetroArch/retroarch.cfg",
		"/mnt/sdcard/RetroArch/.retroarch/retroarch.cfg",
		"/mnt/sdcard/RetroArch/retroarch.cfg",
	}
	raRetroArchConfigGlobs = []string{
		"/mnt/SDCARD/Saves/ra-configs/retroarch-*.cfg",
		"/mnt/sdcard/Saves/ra-configs/retroarch-*.cfg",
	}
)

var raSettingsState struct {
	mu     sync.Mutex
	loaded bool
	data   raSettings
}

var raCredentialState struct {
	mu     sync.Mutex
	loaded bool
	creds  raCredentials
}

func raSettingsPath() string {
	if raSettingsPathOverride != "" {
		return raSettingsPathOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "retroachievements.json")
	}
	return "retroachievements.json"
}

func raCachePath() string {
	if raCachePathOverride != "" {
		return raCachePathOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "retroachievements-cache.json")
	}
	return "retroachievements-cache.json"
}

func raLoadSettings() raSettings {
	raSettingsState.mu.Lock()
	defer raSettingsState.mu.Unlock()
	if raSettingsState.loaded {
		return raSettingsState.data
	}
	raSettingsState.loaded = true
	path := raSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logRA("cannot read settings: %v", err)
		}
		return raSettings{}
	}
	var s raSettings
	if err := json.Unmarshal(data, &s); err != nil {
		// Never include the file content in the log: it holds the API key.
		logRA("settings file is not valid JSON")
		return raSettings{}
	}
	s.Username = strings.TrimSpace(s.Username)
	s.APIKey = strings.TrimSpace(s.APIKey)
	raSettingsState.data = s
	return s
}

func raSaveSettings(next raSettings) error {
	next.Username = strings.TrimSpace(next.Username)
	next.APIKey = strings.TrimSpace(next.APIKey)
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := raWriteFileAtomic(raSettingsPath(), data, 0o600); err != nil {
		return err
	}
	raSettingsState.mu.Lock()
	raSettingsState.loaded = true
	raSettingsState.data = next
	raSettingsState.mu.Unlock()
	raInvalidateCredentialCache()
	return nil
}

func raInvalidateCredentialCache() {
	raCredentialState.mu.Lock()
	raCredentialState.loaded = false
	raCredentialState.creds = raCredentials{}
	raCredentialState.mu.Unlock()
}

// raEffectiveCredentials combines JukaHub's saved settings with the account
// already configured for Cheevos/RetroArch. A saved value always wins.
func raEffectiveCredentials() raCredentials {
	settings := raLoadSettings()
	raCredentialState.mu.Lock()
	defer raCredentialState.mu.Unlock()
	if raCredentialState.loaded {
		c := raCredentialState.creds
		if settings.Username != "" {
			c.Username = settings.Username
			c.UsernameSource = "JukaHub"
		}
		if settings.APIKey != "" {
			c.APIKey = settings.APIKey
			c.KeySource = "JukaHub"
		}
		return c
	}

	creds := raCredentials{}
	if settings.Username != "" {
		creds.Username = settings.Username
		creds.UsernameSource = "JukaHub"
	} else {
		creds.Username, creds.UsernameSource = raDetectUsername()
	}
	if settings.APIKey != "" {
		creds.APIKey = settings.APIKey
		creds.KeySource = "JukaHub"
	} else {
		creds.KeySource, creds.APIKey = raDetectAPIKey()
	}
	raCredentialState.creds = creds
	raCredentialState.loaded = true
	return creds
}

func raSettingsReady() bool { return raEffectiveCredentials().complete() }

func raNormalizeAPIKey(raw string) (string, error) {
	key := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	key = strings.Trim(key, "\"'")
	key = strings.TrimSpace(key)
	if len(key) != 32 {
		return "", fmt.Errorf("API key must be 32 letters or digits")
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return "", fmt.Errorf("API key must be 32 letters or digits")
	}
	return key, nil
}

func raNormalizeUsername(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("username cannot be empty")
	}
	if len(name) > 64 {
		return "", fmt.Errorf("username is too long")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("username contains invalid characters")
		}
	}
	return name, nil
}

func raDetectUsername() (string, string) {
	for _, path := range raSpruceConfigPaths {
		if name := raUsernameFromSpruce(path); name != "" {
			return name, "Spruce settings"
		}
	}
	for _, prefix := range raRetroArchConfigGlobs {
		matches, _ := filepath.Glob(prefix)
		sort.Strings(matches)
		for _, path := range matches {
			if name := raUsernameFromConfig(path); name != "" {
				return name, "RetroArch config"
			}
		}
	}
	for _, path := range raRetroArchConfigPaths {
		if name := raUsernameFromConfig(path); name != "" {
			return name, "RetroArch config"
		}
	}
	for _, path := range raCheevosSettingsPaths {
		if name := raUsernameFromCheevosSettings(path); name != "" {
			return name, "Cheevos app"
		}
	}
	return "", ""
}

func raDetectAPIKey() (string, string) {
	for _, path := range raCheevosKeyPaths {
		if key := raValidKeyFromFile(path); key != "" {
			return "Cheevos app", key
		}
	}
	for _, path := range raSpruceConfigPaths {
		if key := raWebAPIKeyFromSpruce(path); key != "" {
			return "Spruce settings", key
		}
	}
	return "", ""
}

func raUsernameFromConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(raConfigValue(string(data), "cheevos_username"))
}

func raConfigValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), key) {
			continue
		}
		return strings.Trim(strings.TrimSpace(parts[1]), "\"'")
	}
	return ""
}

func raUsernameFromSpruce(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		MenuOptions map[string]json.RawMessage `json:"menuOptions"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	for name, raw := range doc.MenuOptions {
		if !strings.Contains(strings.ToLower(name), "retroachievements") {
			continue
		}
		var opt struct {
			Username struct {
				Selected string `json:"selected"`
			} `json:"username"`
		}
		if json.Unmarshal(raw, &opt) == nil {
			if name := strings.TrimSpace(opt.Username.Selected); name != "" {
				return name
			}
		}
	}
	return ""
}

func raUsernameFromCheevosSettings(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]interface{}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	if name, ok := doc["username"].(string); ok {
		return strings.TrimSpace(name)
	}
	return ""
}

func raWebAPIKeyFromSpruce(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc interface{}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	if raw := raFindJSONString(doc, "webApiKey", 0); raw != "" {
		if key, err := raNormalizeAPIKey(raw); err == nil {
			return key
		}
	}
	return ""
}

// raFindJSONString finds an exact key in decoded JSON. It is only used for
// Spruce's webApiKey, never to hunt for arbitrary password fields.
func raFindJSONString(v interface{}, key string, depth int) string {
	if depth > 12 {
		return ""
	}
	switch node := v.(type) {
	case map[string]interface{}:
		if raw, ok := node[key].(string); ok {
			return strings.TrimSpace(raw)
		}
		for _, child := range node {
			if found := raFindJSONString(child, key, depth+1); found != "" {
				return found
			}
		}
	case []interface{}:
		for _, child := range node {
			if found := raFindJSONString(child, key, depth+1); found != "" {
				return found
			}
		}
	}
	return ""
}

func raValidKeyFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(raDecodeTextFile(data), "\n") {
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		candidate = strings.Trim(candidate, "\"'")
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if key, err := raNormalizeAPIKey(candidate); err == nil {
			return key
		}
	}
	return ""
}

// raDecodeTextFile accepts the UTF-8 and UTF-16 files Windows Notepad writes,
// because the Cheevos key file is commonly edited on a PC.
func raDecodeTextFile(data []byte) string {
	if len(data) >= 2 {
		switch {
		case data[0] == 0xff && data[1] == 0xfe:
			return string(utf16.Decode(raUTF16Words(data[2:], binary.LittleEndian)))
		case data[0] == 0xfe && data[1] == 0xff:
			return string(utf16.Decode(raUTF16Words(data[2:], binary.BigEndian)))
		}
	}
	return strings.TrimPrefix(string(data), "\ufeff")
}

func raUTF16Words(data []byte, order binary.ByteOrder) []uint16 {
	words := make([]uint16, 0, len(data)/2)
	for len(data) >= 2 {
		words = append(words, order.Uint16(data[:2]))
		data = data[2:]
	}
	return words
}

// ── Cache ────────────────────────────────────────────────────────────────

type raProfile struct {
	User            string `json:"user"`
	UserPic         string `json:"userPic"`
	MemberSince     string `json:"memberSince"`
	RichPresence    string `json:"richPresenceMsg"`
	TotalPoints     int    `json:"totalPoints"`
	TotalTruePoints int    `json:"totalTruePoints"`
	Motto           string `json:"motto"`
}

type raAchievement struct {
	Date          string `json:"Date"`
	HardcoreMode  int    `json:"HardcoreMode"`
	AchievementID int    `json:"AchievementID"`
	Title         string `json:"Title"`
	Description   string `json:"Description"`
	BadgeName     string `json:"BadgeName"`
	Points        int    `json:"Points"`
	TrueRatio     int    `json:"TrueRatio"`
	GameTitle     string `json:"GameTitle"`
	GameID        int    `json:"GameID"`
	ConsoleName   string `json:"ConsoleName"`
}

type raGameProgress struct {
	GameID                int    `json:"gameId"`
	Title                 string `json:"title"`
	ImageIcon             string `json:"imageIcon"`
	ConsoleName           string `json:"consoleName"`
	MaxPossible           int    `json:"maxPossible"`
	NumAwarded            int    `json:"numAwarded"`
	NumAwardedHardcore    int    `json:"numAwardedHardcore"`
	MostRecentAwardedDate string `json:"mostRecentAwardedDate"`
	HighestAwardKind      string `json:"highestAwardKind"`
	HighestAwardDate      string `json:"highestAwardDate"`
}

type raCompletionResponse struct {
	Count   int              `json:"Count"`
	Total   int              `json:"Total"`
	Results []raGameProgress `json:"Results"`
}

type raCache struct {
	Version   int              `json:"version"`
	Username  string           `json:"username"`
	UpdatedAt int64            `json:"updatedAt"`
	Profile   raProfile        `json:"profile"`
	Recent    []raAchievement  `json:"recent"`
	Progress  []raGameProgress `json:"progress"`
}

type raStateData struct {
	mu      sync.Mutex
	loaded  bool
	cache   raCache
	loading bool
	err     string
	reqID   int
}

var raState raStateData

func raEnsureCacheLoaded() {
	raState.mu.Lock()
	if raState.loaded {
		raState.mu.Unlock()
		return
	}
	raState.mu.Unlock()

	cache, err := raReadCacheFile()
	raState.mu.Lock()
	defer raState.mu.Unlock()
	if raState.loaded {
		return
	}
	if err == nil && cache.Version == raCacheVersion {
		raState.cache = cache
	}
	raState.loaded = true
}

func raReadCacheFile() (raCache, error) {
	data, err := os.ReadFile(raCachePath())
	if err != nil {
		return raCache{}, err
	}
	var cache raCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return raCache{}, err
	}
	return cache, nil
}

func raSaveCache(cache raCache) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return raWriteFileAtomic(raCachePath(), data, 0o600)
}

func raWriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	// FAT32 (the SD card) and Windows do not implement Unix modes. The
	// requested mode is best-effort; failing the write over it would make the
	// whole feature unusable on the handheld.
	_ = os.Chmod(tmp, perm)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ── API ──────────────────────────────────────────────────────────────────

var (
	errRAAuth    = errors.New("retroachievements rejected the API key")
	errRANetwork = errors.New("retroachievements network error")
)

type raAPIError struct {
	Status  int
	Message string
}

func (e *raAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("retroachievements HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("retroachievements HTTP %d", e.Status)
}

func raFetchAll(ctx context.Context, creds raCredentials, client *http.Client) (raCache, error) {
	if client == nil {
		client = raHTTPClient
	}
	creds.Username = strings.TrimSpace(creds.Username)
	creds.APIKey = strings.TrimSpace(creds.APIKey)
	if !creds.complete() {
		return raCache{}, errRAAuth
	}

	var profile raProfile
	if err := raAPIGet(ctx, client, "API_GetUserProfile.php", creds, nil, &profile); err != nil {
		return raCache{}, err
	}
	if strings.TrimSpace(profile.User) == "" {
		return raCache{}, &raAPIError{Status: http.StatusOK, Message: "empty profile"}
	}

	var recent []raAchievement
	q := url.Values{}
	q.Set("m", strconv.Itoa(raRecentWindowMinutes))
	if err := raAPIGet(ctx, client, "API_GetUserRecentAchievements.php", creds, q, &recent); err != nil {
		return raCache{}, err
	}

	var completion raCompletionResponse
	q = url.Values{}
	q.Set("c", "500")
	q.Set("o", "0")
	if err := raAPIGet(ctx, client, "API_GetUserCompletionProgress.php", creds, q, &completion); err != nil {
		return raCache{}, err
	}

	now := time.Now()
	cache := raCache{
		Version:   raCacheVersion,
		Username:  profile.User,
		UpdatedAt: now.Unix(),
		Profile:   profile,
	}
	for _, a := range recent {
		if a.AchievementID == 101000001 || strings.TrimSpace(a.Title) == "" {
			continue
		}
		cache.Recent = append(cache.Recent, a)
	}
	sort.SliceStable(cache.Recent, func(i, j int) bool {
		return raTimeValue(cache.Recent[i].Date).After(raTimeValue(cache.Recent[j].Date))
	})
	if len(cache.Recent) > raMaxRecent {
		cache.Recent = cache.Recent[:raMaxRecent]
	}

	for _, g := range completion.Results {
		if g.MaxPossible <= 0 {
			continue
		}
		cache.Progress = append(cache.Progress, g)
	}
	sort.SliceStable(cache.Progress, func(i, j int) bool {
		return raTimeValue(cache.Progress[i].MostRecentAwardedDate).After(raTimeValue(cache.Progress[j].MostRecentAwardedDate))
	})
	if len(cache.Progress) > raMaxProgress {
		cache.Progress = cache.Progress[:raMaxProgress]
	}
	return cache, nil
}

func raAPIGet(ctx context.Context, client *http.Client, endpoint string, creds raCredentials, extra url.Values, out interface{}) error {
	q := url.Values{}
	for key, values := range extra {
		for _, value := range values {
			q.Add(key, value)
		}
	}
	q.Set("u", creds.Username)
	q.Set("y", creds.APIKey)
	requestURL := strings.TrimRight(raAPIBase, "/") + "/" + endpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		// Never surface the request URL: it contains the Web API key.
		return errRANetwork
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w", errRANetwork)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, raMaxResponseBytes+1))
	if err != nil {
		return errRANetwork
	}
	if len(body) > raMaxResponseBytes {
		return &raAPIError{Status: resp.StatusCode, Message: "response too large"}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errRAAuth
	}
	if resp.StatusCode != http.StatusOK {
		return &raAPIError{Status: resp.StatusCode, Message: raBodyErrorMessage(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &raAPIError{Status: resp.StatusCode, Message: "unexpected response"}
	}
	return nil
}

func raBodyErrorMessage(body []byte) string {
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		msg := strings.TrimSpace(payload.Error)
		if msg == "" {
			msg = strings.TrimSpace(payload.Message)
		}
		if msg != "" {
			if len(msg) > 120 {
				msg = msg[:120]
			}
			return msg
		}
	}
	return "request failed"
}

func raFriendlyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errRAAuth):
		return "RetroAchievements rejected this key — check Settings"
	case errors.Is(err, errRANetwork):
		return "Offline — showing saved achievements"
	}
	var apiErr *raAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusTooManyRequests:
			return "Rate limited by RetroAchievements — try again shortly"
		case http.StatusOK:
			return "RetroAchievements returned an unexpected response"
		default:
			if apiErr.Status >= 500 {
				return "RetroAchievements is unavailable — showing saved data"
			}
			return "Could not sync — showing saved achievements"
		}
	}
	return "Could not sync — showing saved achievements"
}

func raMaybeRefresh(force bool) {
	creds := raEffectiveCredentials()
	if !creds.complete() {
		return
	}
	raEnsureCacheLoaded()

	raState.mu.Lock()
	fresh := raState.cache.UpdatedAt > 0 &&
		strings.EqualFold(raState.cache.Username, creds.Username) &&
		time.Since(time.Unix(raState.cache.UpdatedAt, 0)) < raCacheTTL
	if raState.loading || (!force && fresh) {
		raState.mu.Unlock()
		return
	}
	raState.reqID++
	id := raState.reqID
	raState.loading = true
	raState.err = ""
	raState.mu.Unlock()

	go raRunFetch(id, creds)
}

func raRunFetch(id int, creds raCredentials) {
	ctx, cancel := context.WithTimeout(context.Background(), raFetchTimeout)
	defer cancel()
	cache, err := raFetchAll(ctx, creds, raHTTPClient)

	current := raEffectiveCredentials()
	if current.Username != creds.Username || current.APIKey != creds.APIKey {
		return
	}

	raState.mu.Lock()
	if raState.reqID != id {
		raState.mu.Unlock()
		return
	}
	raState.loading = false
	if err == nil {
		raState.cache = cache
		raState.loaded = true
		raState.err = ""
	} else {
		raState.err = raFriendlyError(err)
		logRA("sync failed: %s", raState.err)
	}
	raState.mu.Unlock()

	if err == nil {
		if saveErr := raSaveCache(cache); saveErr != nil {
			logRA("cannot write cache: %v", saveErr)
		}
	}
}

func raResetStateForTests() {
	raOnCredentialsChanged()
	raSettingsState.mu.Lock()
	raSettingsState.loaded = false
	raSettingsState.data = raSettings{}
	raSettingsState.mu.Unlock()
	raCredentialState.mu.Lock()
	raCredentialState.loaded = false
	raCredentialState.creds = raCredentials{}
	raCredentialState.mu.Unlock()
}

func logRA(format string, args ...interface{}) {
	// Keep RA messages in one place so a rejected key never ends up in a
	// generic config line (the key itself is never passed here).
	log.Printf("[retroachievements] "+format, args...)
}

// ── Panel state ──────────────────────────────────────────────────────────

const (
	raEditNone = iota
	raEditUsername
	raEditAPIKey
)

type raPanelState struct {
	open      bool
	settings  bool
	cursor    int
	scroll    int
	rows      int
	edit      int
	input     string
	returnEmu bool // opened from the Emulators panel; B returns there
}

var raPanel raPanelState

type raRowHit struct {
	kind  string
	index int
	rect  sdl.Rect
}

var (
	raPanelRect    sdl.Rect
	raCloseRect    sdl.Rect
	raRefreshRect  sdl.Rect
	raSettingsRect sdl.Rect
	raRowHits      []raRowHit
)

const (
	raHitRecent   = "recent"
	raHitUsername = "username"
	raHitAPIKey   = "apiKey"
)

func retroAchievementsSupported() bool {
	if IsTrimuiDevice() || P().IsHandheld() {
		return true
	}
	return raSettingsReady()
}

func retroAchievementsEmuEntry() emuEntry {
	creds := raEffectiveCredentials()
	detail := "Set up your account to sync unlocks"
	if creds.complete() {
		detail = creds.Username + " — recent unlocks and game progress"
	}
	return emuEntry{
		Name:   retroAchievementsEntryName,
		Detail: detail,
		Status: emuAvailable,
	}
}

func openRetroAchievementsPanel() {
	raOpenPanel(false)
}

// openRetroAchievementsPanelFromEmu opens RA from the Emulators list and makes
// B return to that list instead of dropping all the way back to the scene.
func openRetroAchievementsPanelFromEmu() {
	raOpenPanel(true)
}

func raOpenPanel(returnEmu bool) {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	raPanel = raPanelState{open: true, returnEmu: returnEmu}
	raPanelRect = sdl.Rect{}
	raRowHits = nil
	raEnsureCacheLoaded()

	creds := raEffectiveCredentials()
	if !creds.complete() {
		raPanel.settings = true
		if creds.Username != "" {
			raPanel.cursor = 1
		}
	} else {
		raMaybeRefresh(false)
	}
	PlayActivateSound()
}

func closeRetroAchievementsPanel() {
	if !raPanel.open {
		return
	}
	returnEmu := raPanel.returnEmu
	raPanel.open = false
	raPanel.settings = false
	raPanel.edit = raEditNone
	raPanel.returnEmu = false
	if returnEmu {
		// The emulator cursor, scroll and cache were left intact while RA was
		// on top, so this restores the list exactly where the user left it.
		emuPanel.open = true
		raRowHits = nil
	}
	PlayBackSound()
}

func raPanelBack() {
	if raPanel.settings && raSettingsReady() {
		raPanel.settings = false
		raPanel.cursor = 0
		raPanel.scroll = 0
		PlayBackSound()
		return
	}
	closeRetroAchievementsPanel()
}

func raToggleSettings() {
	if raPanel.settings {
		if !raSettingsReady() {
			return
		}
		raPanel.settings = false
		raPanel.cursor = 0
		raPanel.scroll = 0
		PlayBackSound()
		return
	}
	raPanel.settings = true
	raPanel.cursor = 0
	creds := raEffectiveCredentials()
	if creds.Username != "" && creds.APIKey == "" {
		raPanel.cursor = 1
	}
	PlayActivateSound()
}

func raPanelMove(delta int) {
	if raPanel.settings {
		next := raPanel.cursor + delta
		if next < 0 {
			next = 1
		}
		if next > 1 {
			next = 0
		}
		raPanel.cursor = next
		PlayNavSound()
		return
	}
	count := raAchievementCount()
	if count == 0 {
		return
	}
	raPanel.cursor += delta
	if raPanel.cursor < 0 {
		raPanel.cursor = count - 1
	}
	if raPanel.cursor >= count {
		raPanel.cursor = 0
	}
	raClampScroll(count)
	PlayNavSound()
}

func raClampScroll(count int) {
	rows := raPanel.rows
	if rows <= 0 {
		rows = 6
	}
	if raPanel.cursor < raPanel.scroll {
		raPanel.scroll = raPanel.cursor
	}
	if raPanel.cursor >= raPanel.scroll+rows {
		raPanel.scroll = raPanel.cursor - rows + 1
	}
	if raPanel.scroll > count-rows {
		raPanel.scroll = count - rows
	}
	if raPanel.scroll < 0 {
		raPanel.scroll = 0
	}
}

func raAchievementCount() int {
	creds := raEffectiveCredentials()
	raState.mu.Lock()
	defer raState.mu.Unlock()
	if !raCacheMatchesCreds(raState.cache, creds) {
		return 0
	}
	return len(raState.cache.Recent)
}

func raCacheMatchesCreds(cache raCache, creds raCredentials) bool {
	return strings.TrimSpace(creds.Username) != "" && strings.EqualFold(cache.Username, creds.Username)
}

// raOnCredentialsChanged drops the in-memory account snapshot. The next open
// re-reads the cache file, and raMaybeRefresh replaces it with the new user.
func raOnCredentialsChanged() {
	raState.mu.Lock()
	raState.loaded = false
	raState.cache = raCache{}
	raState.loading = false
	raState.err = ""
	raState.reqID++
	raState.mu.Unlock()
}

func raBeginCredentialEdit(field int) {
	creds := raEffectiveCredentials()
	switch field {
	case raEditUsername:
		raPanel.input = creds.Username
	case raEditAPIKey:
		// Start empty: the existing key is masked, so seeding it with 32
		// bullets would only force the user to erase them before typing.
		raPanel.input = ""
	default:
		return
	}
	raPanel.edit = field
	vkOpenRetroAchievementsInput()
}

func raSubmitCredential(text string) {
	field := raPanel.edit
	raPanel.edit = raEditNone
	if field == raEditNone {
		return
	}
	settings := raLoadSettings()

	switch field {
	case raEditUsername:
		name, err := raNormalizeUsername(text)
		if err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		settings.Username = name
	case raEditAPIKey:
		if strings.TrimSpace(text) == "" {
			// An empty field means cancel, not "erase my working key".
			return
		}
		key, err := raNormalizeAPIKey(text)
		if err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		settings.APIKey = key
	default:
		return
	}
	if err := raSaveSettings(settings); err != nil {
		logRA("cannot save settings: %v", err)
		ShowToast("Could not save the RetroAchievements settings", ToastKindError)
		return
	}
	invalidateEmulatorCache()
	raOnCredentialsChanged()
	raEnsureCacheLoaded()
	creds := raEffectiveCredentials()
	if creds.complete() {
		raPanel.settings = false
		raPanel.cursor = 0
		raPanel.scroll = 0
		raMaybeRefresh(true)
		ShowToast("RetroAchievements connected as "+creds.Username, ToastKindSuccess)
	} else {
		raPanel.settings = true
		ShowToast("Account saved — add the other field to finish", ToastKindInfo)
	}
}

// ── Input ────────────────────────────────────────────────────────────────

func handleRetroAchievementsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !raPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		raPanelBack()
	case sdl.K_UP:
		raPanelMove(-1)
	case sdl.K_DOWN:
		raPanelMove(1)
	case sdl.K_y, sdl.K_r:
		if !raSettingsReady() {
			raPanel.settings = true
			ShowToast("Set up RetroAchievements first", ToastKindInfo)
			return true
		}
		raPanel.settings = false
		raMaybeRefresh(true)
		ShowToast("Refreshing RetroAchievements…", ToastKindInfo)
	case sdl.K_x:
		raToggleSettings()
	case sdl.K_RETURN, sdl.K_SPACE:
		if raPanel.settings {
			raBeginCredentialEdit(raPanel.cursor)
		}
	}
	return true
}

func ctrlRetroAchievements(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		raPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		raPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		if raPanel.settings {
			raBeginCredentialEdit(raPanel.cursor)
		}
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_START:
		raToggleSettings()
	case sdl.CONTROLLER_BUTTON_Y:
		if !raSettingsReady() {
			raPanel.settings = true
			ShowToast("Set up RetroAchievements first", ToastKindInfo)
			break
		}
		raPanel.settings = false
		raMaybeRefresh(true)
		ShowToast("Refreshing RetroAchievements…", ToastKindInfo)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		raPanelBack()
	}
	return true
}

func handleRetroAchievementsMouse(mx, my int32, config *Config) bool {
	if !raPanel.open {
		return false
	}
	if raCloseRect.W > 0 && pointInRect(mx, my, raCloseRect) {
		closeRetroAchievementsPanel()
		return true
	}
	if raRefreshRect.W > 0 && pointInRect(mx, my, raRefreshRect) {
		if !raSettingsReady() {
			raPanel.settings = true
			ShowToast("Set up RetroAchievements first", ToastKindInfo)
			return true
		}
		raPanel.settings = false
		raMaybeRefresh(true)
		ShowToast("Refreshing RetroAchievements…", ToastKindInfo)
		return true
	}
	if raSettingsRect.W > 0 && pointInRect(mx, my, raSettingsRect) {
		raToggleSettings()
		return true
	}
	for _, hit := range raRowHits {
		if !pointInRect(mx, my, hit.rect) {
			continue
		}
		switch hit.kind {
		case raHitRecent:
			raPanel.cursor = hit.index
			raClampScroll(raAchievementCount())
		case raHitUsername:
			raBeginCredentialEdit(raEditUsername)
		case raHitAPIKey:
			raBeginCredentialEdit(raEditAPIKey)
		}
		return true
	}
	if pointInRect(mx, my, raPanelRect) {
		return true
	}
	closeRetroAchievementsPanel()
	return true
}

// ── Rendering ────────────────────────────────────────────────────────────

func renderRetroAchievementsPanel(renderer *sdl.Renderer, config *Config) {
	if !raPanel.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	med, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if med == nil {
		med = font
	}
	smallH := int32(font.Height())
	medH := int32(med.Height())

	panelW := int32(1120)
	if panelW > screenWidth-32 {
		panelW = screenWidth - 32
	}
	panelH := int32(620)
	if panelH > screenHeight-32 {
		panelH = screenHeight - 32
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	raPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	raRowHits = raRowHits[:0]
	raCloseRect, raRefreshRect, raSettingsRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 120)
	renderer.FillRect(&sdl.Rect{X: panelX + 20, Y: panelY + 1, W: panelW - 40, H: 2})

	renderText(renderer, config, med, "RetroAchievements", ColorTextPrimary(), panelX+28, panelY+18)

	// Header buttons are touch targets too.
	closeW := int32(84)
	refreshW := int32(104)
	settingsW := int32(104)
	btnGap := int32(8)
	raCloseRect = sdl.Rect{X: panelX + panelW - 28 - closeW, Y: panelY + 18, W: closeW, H: 34}
	if !raPanel.settings {
		raSettingsRect = sdl.Rect{X: raCloseRect.X - btnGap - settingsW, Y: panelY + 18, W: settingsW, H: 34}
	}
	raRefreshRect = sdl.Rect{X: raSettingsRect.X - btnGap - refreshW, Y: panelY + 18, W: refreshW, H: 34}
	if raPanel.settings {
		raRefreshRect.X = raCloseRect.X - btnGap - refreshW
	}
	raDrawButton(renderer, config, font, raRefreshRect, "Refresh")
	if !raPanel.settings {
		raDrawButton(renderer, config, font, raSettingsRect, "Settings")
	}
	raDrawButton(renderer, config, font, raCloseRect, "Close")

	creds := raEffectiveCredentials()
	raState.mu.Lock()
	cache := raState.cache
	loading := raState.loading
	syncErr := raState.err
	raState.mu.Unlock()
	if !raCacheMatchesCreds(cache, creds) {
		cache = raCache{}
	}

	status := "Not synced yet"
	statusCol := ColorTextTertiary()
	if loading {
		status = "Syncing with RetroAchievements…"
		statusCol = ColorTextSecondary()
	} else if syncErr != "" {
		status = syncErr
		statusCol = ColorToastWarn
	} else if cache.UpdatedAt > 0 {
		status = "Synced " + raRelativeTime(time.Unix(cache.UpdatedAt, 0))
		statusCol = ColorTextTertiary()
	}
	subtitle := strings.TrimSpace(creds.Username)
	if subtitle == "" {
		subtitle = "Not connected"
	}
	if creds.complete() && !strings.EqualFold(cache.Username, creds.Username) && cache.Username != "" {
		subtitle = creds.Username + " · switching account"
	}
	subtitle += " · " + status
	maxSubtitleW := raRefreshRect.X - (panelX + 28) - 16
	renderText(renderer, config, font, ellipsize(font, subtitle, maxSubtitleW), statusCol, panelX+28, panelY+18+medH+4)

	contentY := panelY + 18 + medH + smallH + 22
	footerY := panelY + panelH - 30
	if raPanel.settings {
		raRenderSettings(renderer, config, font, med, panelX+120, contentY, panelW-240, footerY-contentY-10, creds)
	} else {
		raRenderOverview(renderer, config, font, med, panelX, contentY, panelW, footerY, cache, creds, loading)
	}

	hint := hintLine(FooterHint{"D-Pad", "Navigate"}, FooterHint{"X", "Settings"},
		FooterHint{"Y", "Refresh"}, FooterHint{"B", "Close"})
	if raPanel.settings {
		hint = hintLine(FooterHint{"A", "Edit"}, FooterHint{"Y", "Refresh"}, FooterHint{"B", "Back"})
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+28, footerY)
	if loading {
		label := "Syncing…"
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextTertiary(), panelX+panelW-28-int32(lw), footerY)
	}
}

func raRenderOverview(renderer *sdl.Renderer, config *Config, font, med *ttf.Font, panelX, contentY, panelW, footerY int32, cache raCache, creds raCredentials, loading bool) {
	smallH := int32(font.Height())
	medH := int32(med.Height())
	// The shipped device config uses 24px for both "small" and "medium";
	// leave room for the name, points and rich-presence lines.
	profileH := int32(104)
	drawCard(renderer, panelX+28, contentY, panelW-56, profileH, 14)

	name := cache.Username
	if name == "" {
		name = creds.Username
	}
	if name == "" {
		name = "RetroAchievements"
	}
	renderText(renderer, config, med, name, ColorTextPrimary(), panelX+52, contentY+14)
	meta := "Waiting for the first sync"
	if cache.Profile.TotalPoints > 0 || cache.UpdatedAt > 0 {
		meta = fmt.Sprintf("%s points · %s true points", raNumber(cache.Profile.TotalPoints), raNumber(cache.Profile.TotalTruePoints))
	}
	renderText(renderer, config, font, meta, ColorTextSecondary(), panelX+52, contentY+14+medH+4)

	if cache.Profile.RichPresence != "" {
		playing := cache.Profile.RichPresence
		if !strings.HasPrefix(strings.ToLower(playing), "playing") {
			playing = "Playing: " + playing
		}
		pw := panelW - 112 - 360
		if pw < 120 {
			pw = 120
		}
		renderText(renderer, config, font, ellipsize(font, playing, pw), ColorTextTertiary(), panelX+52, contentY+14+medH+4+smallH+4)
	}
	if loading || (cache.UpdatedAt == 0 && creds.complete()) {
		label := "Syncing…"
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextTertiary(), panelX+panelW-52-int32(lw), contentY+14)
	}

	columnsY := contentY + profileH + 14
	columnsH := footerY - 10 - columnsY
	innerW := panelW - 56
	leftW := innerW * 60 / 100
	rightX := panelX + 28 + leftW + 16
	rightW := panelX + panelW - 28 - rightX
	leftX := panelX + 28

	drawCard(renderer, leftX, columnsY, leftW, columnsH, 16)
	drawCard(renderer, rightX, columnsY, rightW, columnsH, 16)
	renderText(renderer, config, font, "RECENT UNLOCKS", ColorTextTertiary(), leftX+20, columnsY+14)
	renderText(renderer, config, font, "GAME PROGRESS", ColorTextTertiary(), rightX+20, columnsY+14)

	rowH := int32(64)
	listY := columnsY + 40
	rows := int32((columnsH - 52) / rowH)
	if rows < 1 {
		rows = 1
	}
	raPanel.rows = int(rows)

	count := len(cache.Recent)
	if raPanel.cursor >= count {
		raPanel.cursor = count - 1
	}
	if raPanel.cursor < 0 {
		raPanel.cursor = 0
	}
	raClampScroll(count)

	if count == 0 {
		msg := "No unlocks in the last 30 days."
		if !creds.complete() {
			msg = "Connect your account to see unlocks."
		} else if loading {
			msg = "Fetching your recent unlocks…"
		}
		renderText(renderer, config, font, msg, ColorTextSecondary(), leftX+20, listY+8)
	} else {
		focusColor := installHighlight()
		for i := int32(0); i < rows; i++ {
			idx := raPanel.scroll + int(i)
			if idx >= count {
				break
			}
			a := cache.Recent[idx]
			r := sdl.Rect{X: leftX + 16, Y: listY + i*rowH, W: leftW - 32, H: rowH - 8}
			raRowHits = append(raRowHits, raRowHit{kind: raHitRecent, index: idx, rect: r})
			selected := idx == raPanel.cursor
			bg := WithAlpha(ColorSurfaceRow, 170)
			if selected {
				bg = WithAlpha(focusColor, 42)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 11, bg)
			if selected {
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 11, 1, focusColor)
			}
			tileCol := WithAlpha(accentColor, 210)
			if a.HardcoreMode == 0 {
				tileCol = WithAlpha(ColorTextTertiary(), 160)
			}
			fillRoundedRect(renderer, r.X+10, r.Y+10, 36, 36, 10, WithAlpha(tileCol, 45))
			pts := "+" + strconv.Itoa(a.Points)
			pw, ph, _ := font.SizeUTF8(pts)
			renderText(renderer, config, font, pts, tileCol, r.X+10+(36-int32(pw))/2, r.Y+10+(36-int32(ph))/2)

			titleW := r.W - 220
			renderText(renderer, config, med, ellipsize(med, a.Title, titleW), ColorTextPrimary(), r.X+56, r.Y+6)
			game := a.GameTitle
			if a.ConsoleName != "" {
				game += " · " + a.ConsoleName
			}
			renderText(renderer, config, font, ellipsize(font, game, titleW), ColorTextTertiary(), r.X+56, r.Y+6+medH+1)
			when := raShortWhen(a.Date)
			timeCol := ColorTextTertiary()
			if a.HardcoreMode != 0 {
				when = "HC · " + when
				timeCol = accentColor
			}
			ww, _, _ := font.SizeUTF8(when)
			renderText(renderer, config, font, when, timeCol, r.X+r.W-12-int32(ww), r.Y+8)
		}
		if count > int(rows) {
			info := fmt.Sprintf("%d–%d of %d", raPanel.scroll+1, minInt(raPanel.scroll+int(rows), count), count)
			iw, _, _ := font.SizeUTF8(info)
			renderText(renderer, config, font, info, ColorTextTertiary(), leftX+leftW-20-int32(iw), columnsY+14)
		}
	}

	progress := cache.Progress
	if len(progress) == 0 {
		msg := "Progress appears after your first sync."
		if loading {
			msg = "Fetching completion progress…"
		}
		renderText(renderer, config, font, msg, ColorTextSecondary(), rightX+20, listY+8)
		return
	}
	progressRows := int((columnsH - 52) / 58)
	if progressRows < 1 {
		progressRows = 1
	}
	if progressRows > len(progress) {
		progressRows = len(progress)
	}
	for i := 0; i < progressRows; i++ {
		g := progress[i]
		y := listY + int32(i)*58
		rX := rightX + 16
		rW := rightW - 32
		fillRoundedRect(renderer, rX, y, rW, 52, 10, WithAlpha(ColorSurfaceRow, 150))
		titleW := rW - 140
		renderText(renderer, config, font, ellipsize(font, g.Title, titleW), ColorTextPrimary(), rX+10, y+5)
		pct := raAwardPercent(g.NumAwarded, g.MaxPossible)
		label := fmt.Sprintf("%d%% · %d/%d", pct, g.NumAwarded, g.MaxPossible)
		if g.MaxPossible > 0 && g.NumAwarded >= g.MaxPossible {
			label = fmt.Sprintf("Mastered · %d/%d", g.NumAwarded, g.MaxPossible)
		}
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextSecondary(), rX+rW-10-int32(lw), y+5)
		barY := y + 36
		barW := rW - 20
		fillRoundedRect(renderer, rX+10, barY, barW, 6, 3, WithAlpha(ColorBorder, 110))
		if pct > 0 {
			fillRoundedRect(renderer, rX+10, barY, barW*int32(pct)/100, 6, 3, accentColor)
		}
	}
}

func raRenderSettings(renderer *sdl.Renderer, config *Config, font, med *ttf.Font, x, y, w, h int32, creds raCredentials) {
	drawCard(renderer, x, y, w, h, 16)
	renderText(renderer, config, med, "Account & sync", ColorTextPrimary(), x+24, y+18)
	renderText(renderer, config, font, "JukaHub reads the RetroAchievements account already set up for this SD card.", ColorTextTertiary(), x+24, y+18+int32(med.Height())+4)

	rowW := w - 48
	rowH := int32(76)
	// The subtitle is 24px tall on the shipped device config; start the rows
	// below it rather than letting the first value overlap the second line.
	firstY := y + 78
	focusColor := installHighlight()
	rows := []struct {
		kind  string
		label string
		value string
	}{
		{raHitUsername, "USERNAME", creds.Username},
		{raHitAPIKey, "WEB API KEY", raKeyStatus(creds)},
	}
	for i, row := range rows {
		r := sdl.Rect{X: x + 24, Y: firstY + int32(i)*(rowH+8), W: rowW, H: rowH}
		raRowHits = append(raRowHits, raRowHit{kind: row.kind, index: i, rect: r})
		selected := raPanel.cursor == i
		bg := WithAlpha(ColorSurfaceRow, 190)
		if selected {
			bg = WithAlpha(focusColor, 40)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, bg)
		if selected {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}
		renderText(renderer, config, font, row.label, ColorTextTertiary(), r.X+16, r.Y+10)
		value := row.value
		if value == "" || (row.kind == raHitAPIKey && creds.APIKey == "") {
			value = "Not set"
		}
		renderText(renderer, config, med, ellipsize(med, value, r.W-140), ColorTextPrimary(), r.X+16, r.Y+30)
		a := hintLine(FooterHint{"A", "Edit"})
		aw, _, _ := font.SizeUTF8(a)
		renderText(renderer, config, font, a, ColorTextTertiary(), r.X+r.W-16-int32(aw), r.Y+30)
	}

	helpY := firstY + 2*(rowH+8) + 12
	lines := []string{
		"Create or copy your Web API key at retroachievements.org/settings → Keys.",
	}
	if strings.EqualFold(creds.KeySource, "Cheevos app") {
		lines = append(lines, "Using the key saved by the Cheevos app; JukaHub does not modify it.")
	} else if strings.EqualFold(creds.KeySource, "Spruce settings") {
		lines = append(lines, "Using Spruce's saved Web API key; JukaHub does not modify it.")
	} else {
		lines = append(lines, "The key is stored only in JukaHub's data folder and is never shown in full.")
	}
	for i, line := range lines {
		renderText(renderer, config, font, ellipsize(font, line, w-48), ColorTextTertiary(), x+24, helpY+int32(i)*int32(font.Height()+4))
	}
	if creds.complete() {
		renderText(renderer, config, font, "Ready to sync. Press Y to refresh now.", ColorTextSecondary(), x+24, y+h-30)
	} else {
		renderText(renderer, config, font, "A username and a 32-character Web API key are both required.", ColorToastWarn, x+24, y+h-30)
	}
}

func raDrawButton(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, label string) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	UIButton(renderer, config, rect, label, UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       WithAlpha(ColorSurfaceRow, 200),
		Border:     ColorBorderSubtle,
		LabelColor: ColorTextSecondary(),
		Font:       font,
		Radius:     RadiusSM,
		NoShadow:   true,
	}, UIButtonState{})
}

// ── Formatting helpers ───────────────────────────────────────────────────

func raKeyStatus(creds raCredentials) string {
	if creds.APIKey == "" {
		return ""
	}
	source := strings.TrimSpace(creds.KeySource)
	if source == "" {
		source = "saved"
	}
	return "Set (32 chars) · " + source
}

func raNumber(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func raAwardPercent(awarded, possible int) int {
	if possible <= 0 {
		return 0
	}
	pct := awarded * 100 / possible
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func raTimeValue(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}

func raShortWhen(raw string) string {
	t := raTimeValue(raw)
	if t.IsZero() {
		if raw != "" {
			return raw
		}
		return ""
	}
	return raRelativeTime(t)
}

func raRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("Jan 2")
	}
}
