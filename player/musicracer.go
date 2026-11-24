package main

// musicracer.go — Sound Racer: a racing game whose road is the song. The
// song is listened to first (decoded once to mono at 11 kHz) and turned into
// a song map: how loud each twentieth of a second is, where the road bends
// (the balance of bass and treble), a gem on every onset (in the lane its
// pitch suggests: low, middle, high) and an obstacle on the heaviest bass
// hits. The map is kept (racer-maps/ in the data folder), so the second race
// on a song starts at once. Game soundtracks (.nsf and the other chiptune
// formats, chiptune_decode.go) race like any song.
//
// The race runs in time with what is heard (musicHeardPosition): Left/Right
// change lane, gems score more on a combo, an obstacle breaks it. The race
// ends with the song.

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	racerRate      = 11025
	racerStep      = 0.05 // seconds a segment
	racerMapV      = 1
	racerView      = 80 // segments ahead on screen
	racerBestKey   = "musicRacerBest"
	racerMaxSecond = 20 * 60
)

// racerMap is a song as a road.
type racerMap struct {
	V        int       `json:"v"`
	Energy   []float32 `json:"energy"`
	Curve    []float32 `json:"curve"`
	Notes    []int8    `json:"notes"`
	Blocks   []int8    `json:"blocks"`
	Duration float64   `json:"duration"`
}

var racer struct {
	m        *racerMap
	item     musicItem
	loading  bool
	err      string
	lane     int
	carX     float64
	score    int
	combo    int
	best     int
	hits     int
	gems     int
	lastSeg  int
	flashAt  time.Time
	done     bool
	playedAt time.Time
	at       time.Time
}

// musicRacerStart opens Sound Racer on the song playing, or a random one.
func musicRacerStart(config *Config) {
	openMusicPlayerScene(config)
	mgame.mode = mgameRacer
	racer.m, racer.err, racer.done = nil, "", false
	racer.score, racer.combo, racer.hits, racer.gems = 0, 0, 0, 0
	racer.lane, racer.carX, racer.lastSeg = 1, 0, -1
	racer.best, _ = strconv.Atoi(customStringValue(config.Variables.Custom, racerBestKey))
	racer.loading = true
	music.mu.Lock()
	cur := music.current
	path, name, dur := "", "", 0.0
	if cur >= 0 && cur < len(music.tracks) {
		path, name, dur = music.tracks[cur].Path, music.tracks[cur].Name, music.tracks[cur].Duration
	}
	music.mu.Unlock()
	if path != "" {
		meta, _ := musicMetaFor(path)
		it := musicItem{Kind: "track", Src: meta.Src, ID: meta.ID, Title: dlFirstNonEmpty(meta.Title, musicTrackTitle(name)),
			Artist: meta.Artist, Album: meta.Album, Art: meta.Art, Path: path, Duration: dur,
			Suffix: musicSafeExt(filepath.Ext(name))}
		musicStop()
		musicRacerLoad(config, it)
	} else {
		mgamePool(config, func(pool []musicItem, err error) {
			if len(pool) == 0 {
				racer.loading = false
				racer.err = "Sound Racer needs a song: play one, or add music to your library"
				if err != nil {
					racer.err = firstLine(err.Error())
				}
				return
			}
			musicRacerLoad(config, pool[int(time.Now().UnixNano()%int64(len(pool)))])
		})
	}
	PlayActivateSound()
}

// musicRacerLoad makes (or reads) the song's map, then starts the race.
func musicRacerLoad(config *Config, it musicItem) {
	racer.item = it
	racer.loading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		m, err := racerMapFor(ctx, config, it.Path)
		tbxPost(func() {
			if mgame.mode != mgameRacer || racer.item.Path != it.Path {
				return
			}
			racer.loading = false
			if err != nil {
				racer.err = "Could not listen to that song: " + firstLine(err.Error())
				return
			}
			racer.m = m
			racer.lastSeg = -1
			racer.playedAt = time.Now()
			mgamePlay(it, 0)
		})
	}()
}

// racerMapKey names a song's kept map: a file by its path, size and time; a
// stream by its address.
func racerMapKey(path string) string {
	key := path
	if st, err := os.Stat(path); err == nil {
		key += "|" + strconv.FormatInt(st.Size(), 10) + "|" + strconv.FormatInt(st.ModTime().Unix(), 10)
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:12])
}

func racerMapPath(path string) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "racer-maps", racerMapKey(path)+".json")
}

// racerMapFor reads the kept map or makes one.
func racerMapFor(ctx context.Context, config *Config, path string) (*racerMap, error) {
	file := racerMapPath(path)
	if file != "" {
		if data, err := readSmallFile(file, 4<<20); err == nil {
			var m racerMap
			if json.Unmarshal(data, &m) == nil && m.V == racerMapV && len(m.Energy) > 20 {
				return &m, nil
			}
		}
	}
	m, err := racerAnalyse(ctx, config, path)
	if err != nil {
		return nil, err
	}
	if file != "" {
		if data, err := json.Marshal(m); err == nil {
			_ = os.MkdirAll(filepath.Dir(file), 0o755)
			_ = AtomicWrite(file, data, 0o644)
		}
	}
	return m, nil
}

// racerAnalyse decodes the song and builds its map.
func racerAnalyse(ctx context.Context, config *Config, path string) (*racerMap, error) {
	if config == nil {
		config = &Config{}
	}
	ffmpeg, err := helperToolPath("ffmpeg", config)
	if err != nil {
		return nil, err
	}
	var args []string
	var setup func(*exec.Cmd)
	if chiptuneDecodes(path) {
		args, setup, err = chiptuneDecodeArgs(ffmpeg, path, 0, 1, racerRate)
		if err != nil {
			return nil, err
		}
	} else {
		input := ffmpegReadableInput(ffmpeg, path)
		args = []string{"-nostdin", "-v", "error"}
		args = append(args, ffmpegStreamInputArgs(input, "")...)
		args = append(args, "-i", input, "-vn", "-t", strconv.Itoa(racerMaxSecond), "-f", "s16le",
			"-acodec", "pcm_s16le", "-ac", "2", "-ar", strconv.Itoa(racerRate), "pipe:1")
	}
	// The same launcher the player decodes with (bundled-tool environment).
	cmd := P().CommandContext(ctx, ffmpeg, args...)
	if cmd == nil {
		return nil, errors.New("ffmpeg could not start")
	}
	if setup != nil {
		setup(cmd)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	step := racerStep
	frames := int(racerRate * step)
	buf := make([]byte, frames*4)
	var energy, low, zcr []float64
	lp := 0.0
	alpha := 1 - math.Exp(-2*math.Pi*200/racerRate)
	for {
		if _, err := io.ReadFull(out, buf); err != nil {
			break
		}
		var sum, lsum float64
		cross := 0
		prev := 0.0
		for i := 0; i < frames; i++ {
			l := float64(int16(binary.LittleEndian.Uint16(buf[i*4:])))
			r := float64(int16(binary.LittleEndian.Uint16(buf[i*4+2:])))
			v := (l + r) / 65536
			sum += v * v
			lp += alpha * (v - lp)
			lsum += lp * lp
			if (v >= 0) != (prev >= 0) {
				cross++
			}
			prev = v
		}
		energy = append(energy, math.Sqrt(sum/float64(frames)))
		low = append(low, math.Sqrt(lsum/float64(frames)))
		zcr = append(zcr, float64(cross)/float64(frames))
	}
	_ = cmd.Wait()
	if len(energy) < 40 {
		return nil, errors.New("the song is too short or could not be read")
	}
	return racerBuild(energy, low, zcr), nil
}

// racerBuild turns the loudness, bass and brightness of each segment into a
// map. Pure.
func racerBuild(energy, low, zcr []float64) *racerMap {
	n := len(energy)
	norm := func(v []float64) []float64 {
		s := append([]float64(nil), v...)
		sort.Float64s(s)
		top := s[len(s)*95/100]
		if top <= 0 {
			top = 1
		}
		out := make([]float64, len(v))
		for i, x := range v {
			out[i] = math.Min(1, x/top)
		}
		return out
	}
	e, lo := norm(energy), norm(low)
	m := &racerMap{V: racerMapV, Duration: float64(n) * racerStep,
		Energy: make([]float32, n), Curve: make([]float32, n), Notes: make([]int8, n), Blocks: make([]int8, n)}
	// Brightness terciles pick a gem's lane.
	zs := append([]float64(nil), zcr...)
	sort.Float64s(zs)
	t1, t2 := zs[n/3], zs[2*n/3]
	// Onsets: the loudness jumping above its recent average.
	var flux []float64
	for i := 0; i < n; i++ {
		avg := 0.0
		k := 0
		for j := i - 8; j < i; j++ {
			if j >= 0 {
				avg += e[j]
				k++
			}
		}
		if k > 0 {
			avg /= float64(k)
		}
		flux = append(flux, math.Max(0, e[i]-avg))
	}
	mean, sd := 0.0, 0.0
	for _, f := range flux {
		mean += f
	}
	mean /= float64(n)
	for _, f := range flux {
		sd += (f - mean) * (f - mean)
	}
	sd = math.Sqrt(sd / float64(n))
	lastNote, lastBlock := -100, -100
	for i := 0; i < n; i++ {
		m.Energy[i] = float32(e[i])
		m.Notes[i], m.Blocks[i] = -1, -1
		if i < 60 || i > n-20 {
			continue // a clear start and finish
		}
		peak := flux[i] > mean+1.3*sd && (i+1 >= n || flux[i] >= flux[i+1]) && flux[i] >= flux[i-1]
		if peak && i-lastNote >= 5 {
			lane := int8(1)
			switch {
			case zcr[i] < t1:
				lane = 0
			case zcr[i] > t2:
				lane = 2
			}
			m.Notes[i] = lane
			lastNote = i
		}
		if lo[i] > 0.85 && i-lastBlock >= 24 && i-lastNote > 2 {
			lane := int8((i / 7) % 3)
			if m.Notes[i] == lane {
				lane = (lane + 1) % 3
			}
			m.Blocks[i] = lane
			lastBlock = i
		}
	}
	// Bends: bass against treble, smoothed over two seconds, with a slow
	// sway so a steady song still winds.
	for i := 0; i < n; i++ {
		sum, k := 0.0, 0
		for j := i - 20; j <= i+20; j++ {
			if j >= 0 && j < n {
				sum += lo[j] - (zcr[j]-t1)/(t2-t1+1e-9)*0.5
				k++
			}
		}
		c := sum/float64(k) - 0.25 + 0.35*math.Sin(float64(i)*racerStep*0.45)
		m.Curve[i] = float32(math.Max(-1, math.Min(1, c)))
	}
	return m
}

// ── Playing ─────────────────────────────────────────────────────────────────

func musicRacerKey(sym sdl.Keycode, config *Config) {
	switch sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		mgameQuit()
		return
	case sdl.K_LEFT, sdl.K_a:
		if racer.lane > 0 {
			racer.lane--
		}
	case sdl.K_RIGHT, sdl.K_d:
		if racer.lane < 2 {
			racer.lane++
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if racer.done || racer.err != "" {
			racer.done = false
			if racer.item.Path != "" && racer.err == "" {
				racer.score, racer.combo, racer.hits, racer.gems, racer.lastSeg = 0, 0, 0, 0, -1
				racer.playedAt = time.Now()
				mgamePlay(racer.item, 0)
			} else {
				musicRacerStart(config)
			}
		}
	}
}

// musicRacerTick advances the race to what is heard.
func musicRacerTick(config *Config) {
	m := racer.m
	if m == nil || racer.done {
		return
	}
	now := time.Now()
	dt := now.Sub(racer.at).Seconds()
	if dt > 0.1 || dt < 0 {
		dt = 0.1
	}
	racer.at = now
	target := float64(racer.lane - 1)
	racer.carX += (target - racer.carX) * math.Min(1, dt*14)
	pos := musicHeardPosition()
	seg := int(pos / racerStep)
	if seg >= len(m.Energy) {
		seg = len(m.Energy) - 1
	}
	carLane := int(math.Round(racer.carX + 1))
	for s := racer.lastSeg + 1; s <= seg && s >= 0; s++ {
		if note := m.Notes[s]; note >= 0 {
			if int(note) == carLane {
				racer.combo++
				racer.gems++
				racer.score += 10 * (1 + racer.combo/10)
			} else {
				racer.combo = 0
			}
		}
		if block := m.Blocks[s]; block >= 0 && int(block) == carLane {
			racer.combo = 0
			racer.hits++
			racer.score -= 25
			if racer.score < 0 {
				racer.score = 0
			}
			racer.flashAt = now
			playHaptic(hapticErrorPulse)
		}
	}
	if seg > racer.lastSeg {
		racer.lastSeg = seg
	}
	music.mu.Lock()
	playing := music.playing
	music.mu.Unlock()
	if !playing && time.Since(racer.playedAt) > 3*time.Second {
		racer.done = true
		if racer.score > racer.best {
			racer.best = racer.score
			if config.Variables.Custom == nil {
				config.Variables.Custom = map[string]interface{}{}
			}
			config.Variables.Custom[racerBestKey] = strconv.Itoa(racer.best)
		}
	}
}

// ── Drawing ─────────────────────────────────────────────────────────────────

func renderMusicRacer(renderer *sdl.Renderer, config *Config) {
	musicRacerTick(config)
	col := musicPodColors(config)
	pal := musicPodPalette(config)
	small, _ := getCachedFont(config, "small")
	med, _ := getCachedFont(config, "medium")
	big, _ := getCachedFont(config, "big")
	if small == nil || med == nil || big == nil {
		return
	}
	area := sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	fill := func(c sdl.Color, r sdl.Rect) {
		renderer.SetDrawColor(c.R, c.G, c.B, 255)
		renderer.FillRect(&r)
	}
	fill(pal[0], area)
	m := racer.m
	switch {
	case racer.err != "":
		podWrapped(renderer, config, med, racer.err, pal[3], area, screenHeight/3)
		podCentered(renderer, config, small, hintLine(FooterHint{"A", "Try Another"}, FooterHint{"B", "Back"}), pal[2], area, screenHeight-int32(small.Height())-16)
		return
	case racer.loading || m == nil:
		podCentered(renderer, config, big, "Sound Racer", pal[3], area, screenHeight/3)
		renderSpinner(renderer, screenWidth/2, screenHeight/2+20, 18, pal[3])
		podCentered(renderer, config, small, "Listening to "+racer.item.Title+"…", pal[2], area, screenHeight/2+56)
		return
	}
	w, h := float64(screenWidth), float64(screenHeight)
	horizon := h * 0.42
	pos := musicHeardPosition()
	seg := int(pos / racerStep)
	if seg < 0 {
		seg = 0
	}
	energy := 0.0
	if seg < len(m.Energy) {
		energy = float64(m.Energy[seg])
	}
	// Sky: a striped sun that swells with the music.
	sunR := h * (0.16 + 0.06*energy)
	cx := w / 2
	for y := int(horizon - sunR*1.3); y < int(horizon); y++ {
		dy := float64(y) - (horizon - sunR*0.3)
		span := sunR*sunR - dy*dy
		if span <= 0 || (y/3)%4 == 0 && float64(y) > horizon-sunR*0.6 {
			continue
		}
		dx := math.Sqrt(span)
		fill(pal[2], sdl.Rect{X: int32(cx - dx), Y: int32(y), W: int32(2 * dx), H: 1})
	}
	// The road's world offset at each segment ahead: twice summed bends.
	var xs [racerView + 2]float64
	dx, x := 0.0, 0.0
	for k := 0; k <= racerView+1; k++ {
		s := seg + k
		c := 0.0
		if s < len(m.Curve) {
			c = float64(m.Curve[s])
		}
		dx += c * 0.012
		x += dx
		xs[k] = x
	}
	const persp = 4.2 // segments: p = persp/(k+persp)
	roadBottom := w * 0.46
	for y := int(horizon) + 1; y < int(screenHeight); y++ {
		p := (float64(y) - horizon) / (h - horizon)
		k := persp*(1/p) - persp
		if k > racerView {
			continue
		}
		ki := int(k)
		f := k - float64(ki)
		off := xs[ki]*(1-f) + xs[ki+1]*f
		center := cx + off*p*w*0.6
		half := roadBottom * p
		band := ((seg + ki) / 4) % 2
		grass, rumble := pal[1], pal[2]
		if band == 1 {
			grass, rumble = pal[0], pal[3]
		}
		fill(grass, sdl.Rect{X: 0, Y: int32(y), W: screenWidth, H: 1})
		fill(pal[1], sdl.Rect{X: int32(center - half), Y: int32(y), W: int32(2 * half), H: 1})
		rw := half * 0.08
		fill(rumble, sdl.Rect{X: int32(center - half - rw), Y: int32(y), W: int32(rw), H: 1})
		fill(rumble, sdl.Rect{X: int32(center + half), Y: int32(y), W: int32(rw), H: 1})
		if band == 0 {
			for _, lx := range []float64{-1.0 / 3, 1.0 / 3} {
				fill(pal[3], sdl.Rect{X: int32(center + lx*half*2 - 1), Y: int32(y), W: max32(1, int32(half*0.012)), H: 1})
			}
		}
	}
	// Gems and blocks, far to near.
	for k := racerView; k >= 1; k-- {
		s := seg + k
		if s >= len(m.Notes) {
			continue
		}
		note, block := m.Notes[s], m.Blocks[s]
		if note < 0 && block < 0 {
			continue
		}
		p := persp / (float64(k) + persp)
		y := horizon + p*(h-horizon)
		center := cx + xs[k]*p*w*0.6
		half := roadBottom * p
		size := math.Max(3, 70*p)
		if note >= 0 {
			gx := center + float64(note-1)*half*2/3
			fill(pal[3], sdl.Rect{X: int32(gx - size/2), Y: int32(y - size), W: int32(size), H: int32(size)})
			fill(pal[2], sdl.Rect{X: int32(gx - size/4), Y: int32(y - size*3/4), W: int32(size / 2), H: int32(size / 2)})
		}
		if block >= 0 {
			bx := center + float64(block-1)*half*2/3
			fill(pal[0], sdl.Rect{X: int32(bx - size), Y: int32(y - size*1.2), W: int32(size * 2), H: int32(size * 1.2)})
			podStroke(renderer, sdl.Rect{X: int32(bx - size), Y: int32(y - size*1.2), W: int32(size * 2), H: int32(size * 1.2)}, pal[3])
		}
	}
	// The car.
	carW, carH := w*0.1, h*0.07
	carX := cx + racer.carX*roadBottom*2/3 - carW/2
	carY := h - carH - h*0.04
	fill(pal[3], sdl.Rect{X: int32(carX), Y: int32(carY), W: int32(carW), H: int32(carH)})
	fill(pal[2], sdl.Rect{X: int32(carX + carW*0.2), Y: int32(carY - carH*0.4), W: int32(carW * 0.6), H: int32(carH * 0.5)})
	fill(pal[0], sdl.Rect{X: int32(carX + carW*0.1), Y: int32(carY + carH*0.7), W: int32(carW * 0.2), H: int32(carH * 0.3)})
	fill(pal[0], sdl.Rect{X: int32(carX + carW*0.7), Y: int32(carY + carH*0.7), W: int32(carW * 0.2), H: int32(carH * 0.3)})
	if time.Since(racer.flashAt) < 150*time.Millisecond {
		renderer.SetDrawColor(pal[3].R, pal[3].G, pal[3].B, 120)
		renderer.FillRect(&area)
	}
	// The heads-up line.
	hud := fmt.Sprintf("%d   ·   combo %d   ·   %s", racer.score, racer.combo, formatTime(pos))
	renderText(renderer, config, med, hud, pal[3], 16, 12)
	title := ellipsize(small, racer.item.Title+dlPrefix(" — ", racer.item.Artist), screenWidth/2)
	tw, _ := measureText(small, title)
	renderText(renderer, config, small, title, pal[2], screenWidth-tw-16, 16)
	if racer.done {
		box := sdl.Rect{X: screenWidth / 4, Y: screenHeight / 4, W: screenWidth / 2, H: screenHeight / 2}
		fill(col.bg, box)
		podStroke(renderer, box, col.ink)
		lines := []string{"Finish!", fmt.Sprintf("Score %d", racer.score),
			fmt.Sprintf("%d gems · %d crashes", racer.gems, racer.hits), fmt.Sprintf("Best %d", racer.best)}
		ly := box.Y + 24
		for i, l := range lines {
			font := med
			if i == 0 {
				font = big
			}
			podCentered(renderer, config, font, l, col.ink, box, ly)
			ly += int32(font.Height()) + 10
		}
		podCentered(renderer, config, small, hintLine(FooterHint{"A", "Race Again"}, FooterHint{"B", "Quit"}), col.dim, box, box.Y+box.H-int32(small.Height())-14)
	}
}

// dlPrefix is sep+s, or "" for an empty s.
func dlPrefix(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}
