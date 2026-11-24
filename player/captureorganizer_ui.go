package main

// captureorganizer_ui.go — grouped screenshots and recordings in Tools.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const captureOrganizerSceneName = "CaptureOrganizer"

const (
	captureShowPictures = iota
	captureShowRecordings
)

type capturePictureSet struct {
	name  string
	items []capturePicture
}

type captureOrganizerState struct {
	visit tbxVisit
	nav   tbxNav
	mode  int
	data  captureCollection
	gen   uint64
	busy  bool
	load  bool
}

var captureOrganizer captureOrganizerState

func captureOrganizerOpenScreen(config *Config) { tbxOpenScene(config, captureOrganizerSceneName) }

func captureOrganizerHubTile() toolsHubTile {
	return toolsHubTile{id: "captures", label: "Capture organizer", icon: "image",
		value: "Screenshots by game · play, trim and rename recordings"}
}

type captureOrganizerScene struct{ sceneBase }

func (captureOrganizerScene) render(r *sdl.Renderer, c *Config) bool {
	if captureOrganizer.visit.begin() {
		captureOrganizerEnter(c)
	}
	captureOrganizerRender(r, c)
	return true
}

func (captureOrganizerScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), captureOrganizerAction)
}

func (captureOrganizerScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), captureOrganizerAction)
}

func init() { registerScene(captureOrganizerScene{sceneBase{name: captureOrganizerSceneName}}) }

func captureOrganizerEnter(config *Config) {
	captureOrganizer.nav.scroll = 0
	captureOrganizer.nav.focus = "switch"
	captureOrganizerRescan(config)
}

func captureOrganizerRescan(config *Config) {
	captureOrganizer.gen++
	gen := captureOrganizer.gen
	captureOrganizer.load = true
	pictureDirs := capturePictureDirs()
	recordingDirs := captureRecordingDirs(config)
	go func() {
		data := captureCollection{
			pictures:   captureScanPictures(pictureDirs),
			recordings: captureScanRecordings(recordingDirs),
		}
		tbxPost(func() {
			if captureOrganizer.gen != gen {
				return
			}
			captureOrganizer.data = data
			captureOrganizer.load = false
		})
	}()
}

// capturePictureSets keeps the most recently used game group first; pictures
// inside a group remain newest first from the scan.
func capturePictureSets(pictures []capturePicture) []capturePictureSet {
	sets := make([]capturePictureSet, 0)
	index := make(map[string]int)
	for _, picture := range pictures {
		idx, ok := index[picture.group]
		if !ok {
			idx = len(sets)
			index[picture.group] = idx
			sets = append(sets, capturePictureSet{name: picture.group})
		}
		sets[idx].items = append(sets[idx].items, picture)
	}
	return sets
}

func captureOrganizerRender(renderer *sdl.Renderer, config *Config) {
	status, statusColor := "", ColorTextMuted()
	switch captureOrganizer.mode {
	case captureShowPictures:
		status = fmt.Sprintf("%d screenshots", len(captureOrganizer.data.pictures))
	case captureShowRecordings:
		status = fmt.Sprintf("%d recordings", len(captureOrganizer.data.recordings))
	}
	if captureOrganizer.load {
		status, statusColor = "Finding captures…", ToastWarnInk()
	} else if captureOrganizer.busy {
		status, statusColor = "Saving clip…", ToastWarnInk()
	}
	header := tbxHeader("Capture organizer", "Screenshots by game, recordings ready to play and edit", "image", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &captureOrganizer.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		if screen.Y+screen.H >= body.Y && screen.Y < body.Y+body.H {
			tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(id))
		}
		nav.add(id, rect, row, spec.dim)
		row++
		y += rowH + 8
	}
	section := func(title, icon string) {
		if y > 0 {
			y += 12
		}
		if top+y+tbxSectionHeight >= body.Y && top+y < body.Y+body.H {
			tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		}
		y += tbxSectionHeight
	}
	if captureOrganizer.mode == captureShowPictures {
		drawRow("switch", tbxRowSpec{icon: "video", title: "Show recordings", subtitle: "Play clips, rename them or keep a shorter part", pill: "Switch"})
	} else {
		drawRow("switch", tbxRowSpec{icon: "image", title: "Show screenshots", subtitle: "Browse pictures grouped by game", pill: "Switch"})
	}
	drawRow("refresh", tbxRowSpec{icon: "refresh", title: "Refresh captures", subtitle: "Look for new screenshots and recordings", pill: "Scan"})

	if captureOrganizer.mode == captureShowPictures {
		sets := capturePictureSets(captureOrganizer.data.pictures)
		for _, set := range sets {
			section(fmt.Sprintf("%s · %d", set.name, len(set.items)), "image")
			for _, picture := range set.items {
				id := "picture:" + picture.path
				drawRow(id, tbxRowSpec{icon: "image", title: picture.at.Format("Jan 2, 3:04 PM"),
					subtitle: picture.name, pill: "View"})
			}
		}
		if len(sets) == 0 && !captureOrganizer.load {
			section("Screenshots", "image")
			drawRow("empty", tbxRowSpec{icon: "image", title: "No screenshots yet",
				subtitle: "Take one with F12 or Select + R1 during a game", dim: true})
		}
	} else {
		section("Recordings", "video")
		for _, clip := range captureOrganizer.data.recordings {
			drawRow("recording:"+clip.path, tbxRowSpec{icon: "video",
				title: strings.TrimSuffix(clip.name, filepath.Ext(clip.name)),
				subtitle: clip.at.Format("Jan 2, 3:04 PM") + " · " + formatSize(clip.size), pill: "Open"})
		}
		if len(captureOrganizer.data.recordings) == 0 && !captureOrganizer.load {
			drawRow("empty", tbxRowSpec{icon: "video", title: "No recordings yet",
				subtitle: "Record from Quick Settings or a game's menu", dim: true})
		}
	}
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func captureOrganizerAction(config *Config, act tbxAction) bool {
	switch act {
	case tbxActConfirm:
		captureOrganizerActivate(config, captureOrganizer.nav.focus)
		return true
	case tbxActX:
		if clip, ok := captureOrganizerClip(captureOrganizer.nav.focus); ok {
			captureOrganizerRename(config, clip)
			return true
		}
	case tbxActY:
		if clip, ok := captureOrganizerClip(captureOrganizer.nav.focus); ok {
			captureOrganizerTrim(config, clip.path, 0)
			return true
		}
	case tbxActBack:
		return false
	}
	return tbxNavAction(&captureOrganizer.nav, act)
}

func captureOrganizerSceneClick(mx, my int32, config *Config) bool {
	item, ok := captureOrganizer.nav.hit(mx, my)
	if !ok {
		return false
	}
	captureOrganizer.nav.focus = item.id
	captureOrganizerActivate(config, item.id)
	return true
}

func captureOrganizerSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}}
	if _, ok := captureOrganizerClip(captureOrganizer.nav.focus); ok {
		hints = append(hints, FooterHint{Button: "X", Label: "Rename"}, FooterHint{Button: "Y", Label: "Trim"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func captureOrganizerClip(id string) (captureRecording, bool) {
	if !strings.HasPrefix(id, "recording:") {
		return captureRecording{}, false
	}
	path := strings.TrimPrefix(id, "recording:")
	for _, clip := range captureOrganizer.data.recordings {
		if clip.path == path {
			return clip, true
		}
	}
	return captureRecording{}, false
}

func captureOrganizerPicture(id string) (capturePicture, bool) {
	if !strings.HasPrefix(id, "picture:") {
		return capturePicture{}, false
	}
	path := strings.TrimPrefix(id, "picture:")
	for _, picture := range captureOrganizer.data.pictures {
		if picture.path == path {
			return picture, true
		}
	}
	return capturePicture{}, false
}

func captureOrganizerActivate(config *Config, id string) {
	switch id {
	case "switch":
		PlayActivateSound()
		captureOrganizer.mode = 1 - captureOrganizer.mode
		captureOrganizer.nav.scroll = 0
		captureOrganizer.nav.focus = "switch"
		return
	case "refresh":
		PlayActivateSound()
		captureOrganizerRescan(config)
		return
	}
	if picture, ok := captureOrganizerPicture(id); ok {
		captureOrganizerViewPicture(config, picture)
		return
	}
	if clip, ok := captureOrganizerClip(id); ok {
		captureOrganizerOpenRecording(config, clip)
	}
}

func captureOrganizerViewPicture(config *Config, picture capturePicture) {
	openImageViewer(config, picture.path)
	var paths []string
	index := 0
	for _, candidate := range captureOrganizer.data.pictures {
		if candidate.group != picture.group {
			continue
		}
		if candidate.path == picture.path {
			index = len(paths)
		}
		paths = append(paths, candidate.path)
	}
	if len(paths) > 0 {
		imageViewerFiles = paths
		imageViewerIndex = index
	}
}

func captureOrganizerOpenRecording(config *Config, clip captureRecording) {
	PlayActivateSound()
	var duration float64
	token := "capture-duration:" + strconv.FormatUint(captureOrganizer.gen+uint64(time.Now().UnixNano()), 10)
	buttons := []tbxSheetButton{
		{label: "Play", primary: true, run: func() {
			playVideoInfo(config, VideoInfo{Title: strings.TrimSuffix(clip.name, filepath.Ext(clip.name)), URL: clip.path})
		}},
		{label: "Rename", run: func() { captureOrganizerRename(config, clip) }},
		{label: "Trim", run: func() { captureOrganizerTrim(config, clip.path, duration) }},
		{label: "Close"},
	}
	body := fmt.Sprintf("File: %s\nSize: %s\nFolder: %s\n\nThe original clip stays safe when you trim it. Reading its length…",
		clip.name, formatSize(clip.size), filepath.Dir(clip.path))
	tbxOpenSheet(config, tbxSheetSpec{token: token, title: clip.name, icon: "video", body: body, buttons: buttons})
	probe := getToolPath("ffprobe", config)
	go func() {
		seconds := captureProbeDuration(context.Background(), probe, clip.path)
		tbxPost(func() {
			duration = seconds
			length := "Length unavailable — enter times in seconds or mm:ss"
			if seconds > 0 {
				length = "Length: " + captureDisplayTime(seconds)
			}
			body := fmt.Sprintf("File: %s\nSize: %s\n%s\nFolder: %s\n\nTrim makes a new clip and keeps this original.",
				clip.name, formatSize(clip.size), length, filepath.Dir(clip.path))
			tbxUpdateSheet(token, body, nil)
		})
	}()
}

func captureDisplayTime(seconds float64) string {
	whole := int(seconds + 0.5)
	return fmt.Sprintf("%d:%02d", whole/60, whole%60)
}

func captureOrganizerRename(config *Config, clip captureRecording) {
	if captureOrganizer.busy {
		ShowToast("Finish saving the current clip first", ToastKindInfo)
		return
	}
	seed := strings.TrimSuffix(clip.name, filepath.Ext(clip.name))
	osPromptOpen("Rename recording", "Name for this clip", "Rename", seed, 64, func(title string) {
		if title == "" {
			ShowToast("Enter a name for the recording", ToastKindWarning)
			return
		}
		captureOrganizer.busy = true
		dirs := captureRecordingDirs(config)
		go func() {
			to, err := captureRenameRecording(clip.path, title, dirs)
			tbxPost(func() {
				captureOrganizer.busy = false
				if err != nil {
					ShowToast("Rename failed: "+firstLine(err.Error()), ToastKindError)
					return
				}
				ShowToast("Renamed to "+filepath.Base(to), ToastKindSuccess)
				captureOrganizerRescan(config)
			})
		}()
	})
}

func captureOrganizerTrim(config *Config, path string, duration float64) {
	if captureOrganizer.busy {
		ShowToast("Finish saving the current clip first", ToastKindInfo)
		return
	}
	var askStart func()
	askStart = func() {
		osPromptOpen("Keep from", "Seconds or mm:ss", "Next", "0", 12, func(value string) {
			start, err := captureParseTime(value)
			if err != nil || duration > 0 && start >= duration {
				ShowToast("Enter a start time inside the recording", ToastKindWarning)
				askStart()
				return
			}
			seed := ""
			if duration > 0 {
				seed = captureDisplayTime(duration)
			}
			var askEnd func()
			askEnd = func() {
				osPromptOpen("Keep until", "Seconds or mm:ss", "Next", seed, 12, func(value string) {
					end, err := captureParseTime(value)
					if err != nil || end <= start+0.5 || duration > 0 && end > duration+0.25 {
						ShowToast("End must be after the start and within the clip", ToastKindWarning)
						askEnd()
						return
					}
					body := fmt.Sprintf("Keep %s to %s from %s.\n\nA new MP4 is saved beside the recording. The original is kept.",
						captureDisplayTime(start), captureDisplayTime(end), filepath.Base(path))
					tbxOpenSheet(config, tbxSheetSpec{title: "Create shorter clip?", icon: "video", body: body,
						buttons: []tbxSheetButton{{label: "Cancel"}, {label: "Create clip", primary: true,
							run: func() { captureOrganizerRunTrim(config, path, start, end) }}}})
				})
			}
			askEnd()
		})
	}
	askStart()
}

func captureOrganizerRunTrim(config *Config, path string, start, end float64) {
	if captureOrganizer.busy {
		ShowToast("Finish saving the current clip first", ToastKindInfo)
		return
	}
	captureOrganizer.busy = true
	ShowToast("Creating a shorter copy — the original is staying", ToastKindInfo)
	ffmpeg := screenRecFindFFmpeg(config)
	if !toolPathRunnable(ffmpeg) {
		ffmpeg = getToolPath("ffmpeg", config)
	}
	dirs := captureRecordingDirs(config)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		to, err := captureTrimRecording(ctx, ffmpeg, path, start, end, dirs)
		tbxPost(func() {
			captureOrganizer.busy = false
			if err != nil {
				ShowToast("Trim failed: "+firstLine(err.Error()), ToastKindError)
				return
			}
			ShowToast("Trimmed copy saved: "+filepath.Base(to), ToastKindSuccess)
			captureOrganizerRescan(config)
		})
	}()
}

// Keep the import of os meaningful: the detail sheet checks whether a clip
// still exists before offering it after a refresh race.
func captureOrganizerRecordingStillExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
