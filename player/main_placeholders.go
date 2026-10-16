package main

// --- placeholder config shapes needed by the current compile errors ---

import "sync"

type Config struct {
	Scenes      []SceneConfig
	Version     string
	Variables   Variables
	Screensaver ScreensaverConfig
	Audio       AudioConfig
	Video       VideoConfig
}

type Variables struct {
	ButtonColor        userColor
	LabelColor         userColor
	InputColor         userColor
	Fullscreen         bool
	FileExplorerRoot   string
	WeatherEnabled     bool
	WeatherUnit        string
	TSPUsername        string
	PlaybackResolution string
	AudioBackend       string
	ReducedMotion      bool
	LowPower           bool
	GridColumns        int
	SearchWidth        int
	DynamicBg          bool
	ScreensaverTimeout int
	SocialNotifs       bool
	Custom             map[string]interface{}
}

type userColor struct{ R, G, B uint8 }

type DeviceModel struct{ ID string }

func (d DeviceModel) IsTrimui() bool   { return false }
func (d DeviceModel) hardwareModelID() string { return d.ID }

type SceneConfig struct {
	Name        string
	Layout      string
	Icon        string
	Description string
}

// Element is a placeholder for workspace/UI elements referenced by canvas.go and chat.go.
type Element struct {
	Type  string
	ID    string
	X     float64
	Y     float64
	W     float64
	H     float64
	Text  string
	Color string
}

// journal is a placeholder for the workspace journal referenced by patch_resolve.go.
// Real workspace journals are structs; this placeholder keeps the compile path alive.
var journal struct{ recordReplace func(string) error }


type ScreensaverConfig struct{}
type AudioConfig struct{}
type VideoConfig struct{}

// VideoInfo is a placeholder for the workspace video info type referenced by favorites/shorts.
type VideoInfo struct {
	Title string
	URL   string
}

// --- placeholder types for the patch infrastructure ---

// Placeholder declarations are intentionally omitted here because the real
// definitions live in patch_flow.go. Keeping duplicates here would shadow
// those real declarations and break callers.
//
// type PatchComponent string
// type PatchRowStatus string
// type PatchSnapshot struct{ Backups []string }

// PatchRow is a placeholder for the workspace patch row type referenced by patch.go.
type PatchRow struct {
	ID    string
	State string
}

// --- partial signature constants/helpers referenced by patch_flow.go ---

const (
	StatusChecking   PatchRowStatus = "checking"
	StatusOffline    PatchRowStatus = "offline"
	StatusPlanning   PatchRowStatus = "planning"
	StatusFailed     PatchRowStatus = "failed"
	StatusCurrent    PatchRowStatus = "current"
	StatusUnavailable PatchRowStatus = "unavailable"
	StatusAvailable  PatchRowStatus = "available"
	StatusRollingBack PatchRowStatus = "rolling-back"
)

const (
	ComponentApp    PatchComponent = "app"
	ComponentAssets PatchComponent = "assets"
	ComponentTools  PatchComponent = "tools"
)

var appConfig *Config
