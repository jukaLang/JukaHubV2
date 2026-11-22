package main

// pads_samples.go — where the Pads page's sounds come from and how they are
// kept: decoding (WAV natively, anything else through ffmpeg), the built-in
// kit rendered by the Synth's engine, the cache and its memory budget, the
// chop (equal slices or by transients), the waveform the page draws, the
// sample browser's folders (Voice Memos, a Samples folder, GrainBrick,
// Music, Downloads/music) and the WAV export.
//
// GrainBrick is a separate granular instrument for TrimUI handhelds by
// another author. Pads only reads the WAV files a GrainBrick install has
// saved in its own folder (recordings, samples, exports); it contains none of
// GrainBrick's code. GrainBrick's own slice markers are not read: Chop cuts a
// sound into pads here.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	padsMaxSampleSecs = 30
	padsMaxWAVBytes   = 64 << 20
	padsSampleBudget  = 96 << 20 // bytes of decoded sound kept in memory
	padsDecodeRate    = 44100
	padsKitPrefix     = "kit:"
)

// padsAudioExts are the files the browser lists.
var padsAudioExts = map[string]bool{
	".wav": true, ".wave": true, ".mp3": true, ".ogg": true, ".oga": true, ".opus": true,
	".flac": true, ".m4a": true, ".aac": true, ".aif": true, ".aiff": true,
}

// padsIsAudio reports whether name looks like a sound file.
func padsIsAudio(name string) bool {
	return padsAudioExts[strings.ToLower(filepath.Ext(name))]
}

// padsLoadSem lets one decode run at a time: a bank of compressed files must
// not start eight ffmpegs at once on the handheld.
var padsLoadSem = make(chan struct{}, 1)

// padsDecode reads a sound into a padsSample.
func padsDecode(path string) (*padsSample, error) {
	if strings.HasPrefix(path, padsKitPrefix) {
		return padsKitSample(path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("is a folder")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if (ext == ".wav" || ext == ".wave") && info.Size() <= padsMaxWAVBytes {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		pcm, rate, derr := ttsDecodeWAV(data, false)
		if derr == nil && len(pcm) > 0 {
			return padsCap(pcm, rate), nil
		}
		// A compressed WAV (ADPCM, µ-law): ffmpeg can still read it.
	}
	return padsDecodeFFmpeg(path)
}

// padsCap keeps the first padsMaxSampleSecs of a sound.
func padsCap(pcm []int16, rate int) *padsSample {
	if limit := padsMaxSampleSecs * rate; len(pcm) > limit {
		pcm = append([]int16(nil), pcm[:limit]...)
	}
	return &padsSample{data: pcm, rate: rate}
}

// padsDecodeFFmpeg decodes anything else to mono 44.1 kHz through ffmpeg.
func padsDecodeFFmpeg(path string) (*padsSample, error) {
	ffmpeg, ok := checkToolPresence("ffmpeg")
	if !ok {
		return nil, errors.New("only WAV files open without ffmpeg, and ffmpeg is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	args := []string{"-nostdin", "-v", "error", "-i", path, "-vn",
		"-t", fmt.Sprint(padsMaxSampleSecs), "-f", "s16le", "-acodec", "pcm_s16le",
		"-ac", "1", "-ar", fmt.Sprint(padsDecodeRate), "pipe:1"}
	cmd := P().CommandContext(ctx, ffmpeg, args...)
	alarmHideConsole(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	raw, rerr := io.ReadAll(io.LimitReader(stdout, int64(padsMaxSampleSecs*padsDecodeRate*2)))
	cancel()
	_ = cmd.Wait()
	if rerr != nil {
		return nil, rerr
	}
	n := len(raw) / 2
	if n < 64 {
		return nil, errors.New("no sound in the file")
	}
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return &padsSample{data: pcm, rate: padsDecodeRate}, nil
}

// padsKitSample renders a built-in sound with the Synth's engine: its drums
// and its melodic voices played once, without echo, normalised.
func padsKitSample(id string) (*padsSample, error) {
	const rate = 44100
	p := synthDefaultProject()
	p.Key, p.Scale = 9, 0 // A minor, the Synth's demo key
	p.DelayFB = 0
	p.Master = 0.8
	for i := range p.Drums {
		p.Drums[i].Delay, p.Drums[i].Pan, p.Drums[i].Volume = 0, 0.5, 0.9
	}
	for i := range p.Tones {
		p.Tones[i].Delay, p.Tones[i].Pan = 0, 0.5
	}
	e := newSynthEngine(rate)
	e.proj = &p
	secs := 0.8
	gate := int(0.3 * rate)
	switch id {
	case "kit:kick":
		e.drumHit(synthLaneKick, 1)
	case "kit:kick-deep":
		p.Drums[synthLaneKick].Tune, p.Drums[synthLaneKick].Decay = 0.15, 0.8
		e.drumHit(synthLaneKick, 1)
		secs = 1.2
	case "kit:snare":
		e.drumHit(synthLaneSnare, 1)
	case "kit:snare-tight":
		p.Drums[synthLaneSnare].Tune, p.Drums[synthLaneSnare].Decay = 0.7, 0.15
		e.drumHit(synthLaneSnare, 1)
		secs = 0.4
	case "kit:hat":
		p.Drums[synthLaneHat].Decay = 0.12
		e.drumHit(synthLaneHat, 1)
		secs = 0.3
	case "kit:hat-open":
		p.Drums[synthLaneHat].Decay = 0.85
		e.drumHit(synthLaneHat, 1)
		secs = 1.0
	case "kit:clap":
		e.drumHit(synthLaneClap, 1)
	case "kit:clap-long":
		p.Drums[synthLaneClap].Decay = 0.8
		e.drumHit(synthLaneClap, 1)
		secs = 1.1
	case "kit:bass-1", "kit:bass-4", "kit:bass-5", "kit:bass-7":
		deg := map[string]int{"kit:bass-1": 0, "kit:bass-4": 3, "kit:bass-5": 4, "kit:bass-7": 6}[id]
		e.updateRT()
		e.noteOn(synthInstBass, synthDegreeMidi(&p, synthInstBass, deg), 1, false, gate)
	case "kit:lead-1", "kit:lead-5":
		deg := 0
		if id == "kit:lead-5" {
			deg = 4
		}
		e.updateRT()
		e.noteOn(synthInstLead, synthDegreeMidi(&p, synthInstLead, deg), 1, false, gate)
	case "kit:chord-1", "kit:chord-6":
		deg := 0
		if id == "kit:chord-6" {
			deg = 5
		}
		e.updateRT()
		e.chordOn(deg, 1, false, int(0.5*rate))
		secs = 1.4
	default:
		return nil, errors.New("unknown built-in sound")
	}
	frames := int(secs * rate)
	buf := make([]float32, 2*256)
	pcm := make([]float64, 0, frames)
	peak := 0.0
	for done := 0; done < frames; done += 256 {
		n := min(256, frames-done)
		e.render(buf, n)
		for i := 0; i < n; i++ {
			v := float64(buf[2*i]+buf[2*i+1]) * 0.5
			pcm = append(pcm, v)
			peak = math.Max(peak, math.Abs(v))
		}
	}
	gain := 1.0
	if peak > 0.0001 {
		gain = 0.9 / peak
	}
	out := make([]int16, len(pcm))
	for i, v := range pcm {
		out[i] = int16(math.Max(-1, math.Min(1, v*gain)) * 32767)
	}
	return &padsSample{data: out, rate: rate}, nil
}

// ── Cache ────────────────────────────────────────────────────────────────

// padsSampleFor is the loaded sound at path, or nil. Caller holds padsSt.mu.
func padsSampleFor(path string) *padsSample {
	if path == "" {
		return nil
	}
	return padsSt.samples[path]
}

// padsSampleState says how path is doing: "" loaded, "loading", or an error.
func padsSampleState(path string) string {
	padsSt.mu.Lock()
	defer padsSt.mu.Unlock()
	if path == "" || padsSt.samples[path] != nil {
		return ""
	}
	if padsSt.loading[path] {
		return "loading"
	}
	if err, ok := padsSt.failed[path]; ok {
		return err
	}
	return "loading"
}

// padsRequest loads path in the background unless it is loaded, loading or
// known bad. done runs on the loader goroutine with the result.
func padsRequest(path string, done func(*padsSample, error)) {
	if path == "" {
		return
	}
	padsSt.mu.Lock()
	if s := padsSt.samples[path]; s != nil {
		padsSt.mu.Unlock()
		if done != nil {
			done(s, nil)
		}
		return
	}
	if padsSt.loading[path] {
		padsSt.mu.Unlock()
		return
	}
	if done == nil {
		if _, bad := padsSt.failed[path]; bad {
			padsSt.mu.Unlock()
			return
		}
	}
	delete(padsSt.failed, path)
	padsSt.loading[path] = true
	padsSt.mu.Unlock()
	go func() {
		padsLoadSem <- struct{}{}
		smp, err := padsDecode(path)
		<-padsLoadSem
		padsSt.mu.Lock()
		delete(padsSt.loading, path)
		if err == nil {
			err = padsStoreLocked(path, smp)
		}
		if err != nil {
			padsSt.failed[path] = err.Error()
			logWarn("pads", "loading %s: %v", filepath.Base(path), err)
		}
		padsSt.mu.Unlock()
		if done != nil {
			done(smp, err)
		}
	}()
}

// padsStoreLocked keeps smp under path within the memory budget, dropping
// sounds no pad plays first. Caller holds padsSt.mu.
func padsStoreLocked(path string, smp *padsSample) error {
	size := int64(len(smp.data) * 2)
	if padsSt.bytes+size > padsSampleBudget {
		used := map[string]bool{padsSt.preview: true}
		for i := range padsSt.proj.Pads {
			used[padsSt.proj.Pads[i].Sample] = true
		}
		for k, s := range padsSt.samples {
			if !used[k] {
				padsSt.bytes -= int64(len(s.data) * 2)
				delete(padsSt.samples, k)
			}
		}
	}
	if padsSt.bytes+size > padsSampleBudget {
		return errors.New("the pads already hold as much sound as fits in memory; clear some pads first")
	}
	if old := padsSt.samples[path]; old != nil {
		padsSt.bytes -= int64(len(old.data) * 2)
	}
	padsSt.samples[path] = smp
	padsSt.bytes += size
	return nil
}

// padsRequestAll starts loading every sound the project's pads play.
func padsRequestAll() {
	padsSt.mu.Lock()
	var paths []string
	seen := map[string]bool{}
	for i := range padsSt.proj.Pads {
		s := padsSt.proj.Pads[i].Sample
		if s != "" && !seen[s] && padsSt.samples[s] == nil {
			seen[s] = true
			paths = append(paths, s)
		}
	}
	padsSt.mu.Unlock()
	for _, p := range paths {
		padsRequest(p, nil)
	}
}

// ── Chop ─────────────────────────────────────────────────────────────────

// padsOnsets finds up to n-1 places in data[lo:hi] where a new hit starts
// (rises in short-time energy), as fractions of the whole sound, with lo
// first. It returns fewer when the sound has fewer clear hits.
func padsOnsets(smp *padsSample, lo, hi float64, n int) []float64 {
	if smp == nil || n < 2 {
		return []float64{lo}
	}
	total := len(smp.data)
	a, b := int(lo*float64(total)), int(hi*float64(total))
	hop := max(64, smp.rate/200) // 5 ms
	win := hop * 2
	if b-a < win*4 {
		return []float64{lo}
	}
	var energy []float64
	for x := a; x+win <= b; x += hop {
		var sum float64
		for _, v := range smp.data[x : x+win] {
			f := float64(v) / 32768
			sum += f * f
		}
		energy = append(energy, math.Sqrt(sum/float64(win)))
	}
	type cand struct {
		at   int
		rise float64
	}
	var cands []cand
	for i := 1; i < len(energy)-1; i++ {
		rise := energy[i] - energy[i-1]
		if rise > 0.02 && rise >= energy[i+1]-energy[i] {
			cands = append(cands, cand{i, rise})
		}
	}
	sort.Slice(cands, func(x, y int) bool { return cands[x].rise > cands[y].rise })
	minGap := max(1, smp.rate/12/hop) // ~80 ms between hits
	picked := []int{0}
	for _, c := range cands {
		if len(picked) >= n {
			break
		}
		ok := true
		for _, p := range picked {
			if d := c.at - p; d < minGap && d > -minGap {
				ok = false
				break
			}
		}
		if ok {
			picked = append(picked, c.at)
		}
	}
	sort.Ints(picked)
	out := make([]float64, len(picked))
	for i, p := range picked {
		at := a + p*hop
		if i == 0 {
			at = a
		}
		out[i] = float64(at) / float64(total)
	}
	return out
}

// padsChopInto writes n slices of pad src's sound onto src and the pads after
// it in the same bank onward (never past D16). byHits cuts at the hits it
// finds instead of in equal parts. It reports how many pads it wrote.
func padsChopInto(p *padsProject, src, n int, byHits bool, smp *padsSample) int {
	if src < 0 || src >= padsCount || n < 2 {
		return 0
	}
	base := p.Pads[src]
	if base.Sample == "" {
		return 0
	}
	lo, hi := padsClamp01(base.Start), padsClamp01(base.End)
	if hi <= lo {
		lo, hi = 0, 1
	}
	cuts := make([]float64, 0, n+1)
	if byHits && smp != nil {
		cuts = append(cuts, padsOnsets(smp, lo, hi, n)...)
	}
	if len(cuts) < 2 {
		cuts = cuts[:0]
		for i := 0; i < n; i++ {
			cuts = append(cuts, lo+(hi-lo)*float64(i)/float64(n))
		}
	}
	cuts = append(cuts, hi)
	name := strings.TrimSpace(base.Name)
	if name == "" {
		name = "Slice"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	wrote := 0
	for i := 0; i+1 < len(cuts) && src+i < padsCount; i++ {
		d := base
		d.Start, d.End = cuts[i], cuts[i+1]
		d.Name = name + " " + fmt.Sprint(i+1)
		d.Mode = padsModeOneShot
		p.Pads[src+i] = d
		wrote++
	}
	return wrote
}

// ── Waveform ─────────────────────────────────────────────────────────────

// padsWaveKey names one drawn waveform.
type padsWaveKey struct {
	path       string
	start, end float64
	cols       int
}

// padsWaveCache holds drawn waveforms (main thread only).
var padsWaveCache = map[padsWaveKey][]float32{}

// padsWave is the peak level of cols columns over [start, end] of path's
// sound, or nil while it loads.
func padsWave(path string, start, end float64, cols int) []float32 {
	if path == "" || cols < 2 {
		return nil
	}
	key := padsWaveKey{path, start, end, cols}
	if w, ok := padsWaveCache[key]; ok {
		return w
	}
	padsSt.mu.Lock()
	smp := padsSt.samples[path]
	padsSt.mu.Unlock()
	if smp == nil {
		return nil
	}
	n := len(smp.data)
	a, b := int(padsClamp01(start)*float64(n)), int(padsClamp01(end)*float64(n))
	if b-a < cols {
		a, b = 0, n
	}
	w := make([]float32, cols)
	per := float64(b-a) / float64(cols)
	for c := 0; c < cols; c++ {
		x0 := a + int(float64(c)*per)
		x1 := min(b, a+int(float64(c+1)*per))
		// At most 256 reads per column: plenty for a peak on screen.
		stride := max(1, (x1-x0)/256)
		var pk int32
		for x := x0; x < x1; x += stride {
			v := int32(smp.data[x])
			if v < 0 {
				v = -v
			}
			if v > pk {
				pk = v
			}
		}
		w[c] = float32(pk) / 32768
	}
	if len(padsWaveCache) > 256 {
		clear(padsWaveCache)
	}
	padsWaveCache[key] = w
	return w
}

// ── Browser folders ──────────────────────────────────────────────────────

// padsBrowseRow is one line of the sample browser.
type padsBrowseRow struct {
	label  string
	detail string
	path   string // a folder, a file or a built-in sound
	dir    bool
	action string // "record": open Voice Memos
}

// padsSamplesDir is the user's own Samples folder (SD card top, or
// Documents/JukaHub/Samples on a PC).
func padsSamplesDir() string { return osAppsUserDir("Samples") }

// padsGrainBrickDirs finds GrainBrick installs: $PP_GB_DIR, then folders named like GrainBrick among the ports and
// apps folders of the card.
func padsGrainBrickDirs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if d != "" && !seen[d] && dirExists(d) {
			seen[d] = true
			out = append(out, d)
		}
	}
	add(strings.TrimSpace(os.Getenv("PP_GB_DIR")))
	sd := strings.TrimSpace(defaultTrimuiSDRoot())
	if sd == "" {
		sd = "/mnt/SDCARD"
	}
	parents := []string{
		filepath.Join(sd, "Roms", "PORTS"), filepath.Join(sd, "Roms", "PORTS64"),
		filepath.Join(sd, "Roms", "Ports"), filepath.Join(sd, "Roms", "ports"),
		filepath.Join(sd, "ports"), filepath.Join(sd, "Apps"), filepath.Join(sd, "App"),
		"/userdata/roms/ports", "/roms/ports", "/roms2/ports",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		parents = append(parents, home, filepath.Join(home, "Documents"), filepath.Join(home, "Music"))
	}
	for _, parent := range parents {
		ents, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, e := range ents {
			name := strings.ToLower(e.Name())
			if e.IsDir() && (strings.Contains(name, "grainbrick") || strings.Contains(name, "grainbernic") || strings.Contains(name, "grainmm")) {
				add(filepath.Join(parent, e.Name()))
			}
		}
	}
	return out
}

// padsMusicDir is the first music folder that exists.
func padsMusicDir() string {
	for _, d := range saverMusicDirCandidates(appConfig) {
		if d != "" && d != "." && dirExists(d) {
			return d
		}
	}
	return ""
}

// padsSources is the browser's first page.
func padsSources() []padsBrowseRow {
	rows := []padsBrowseRow{
		{label: "Built-in kit", detail: "Drums, bass, leads and chords from the Synth", path: padsKitPrefix, dir: true},
		{label: "Record a new sample", detail: "Opens Voice Memos; the recording is listed under Voice Memos", action: "record"},
	}
	add := func(label, detail, dir string) {
		if dir != "" && dirExists(dir) {
			rows = append(rows, padsBrowseRow{label: label, detail: detail, path: dir, dir: true})
		}
	}
	add("Voice Memos", "Your recordings, newest first", osAppsUserDir(vmFolderName))
	samples := padsSamplesDir()
	if dirExists(samples) {
		add("Samples", "Your Samples folder", samples)
	} else {
		rows = append(rows, padsBrowseRow{label: "Samples", detail: "Put sounds in " + samples + " (made on first use)", path: samples, dir: true})
	}
	for _, gb := range padsGrainBrickDirs() {
		add("GrainBrick · "+filepath.Base(gb), "Recordings, samples and exports GrainBrick saved", gb)
	}
	add("Music", "Your music folder", padsMusicDir())
	add("Downloads/music", "Synth and Pads exports, downloaded music", downloadsMusicDir())
	return rows
}

// padsKitRows lists the built-in sounds.
func padsKitRows() []padsBrowseRow {
	rows := make([]padsBrowseRow, 0, len(padsKitIDs))
	for _, id := range padsKitIDs {
		rows = append(rows, padsBrowseRow{label: padsKitName(id), detail: "Built-in", path: id})
	}
	return rows
}

// padsListDir lists a folder: subfolders, then sound files (Voice Memos
// newest first, everything else by name). At most 2000 rows.
func padsListDir(dir string) ([]padsBrowseRow, error) {
	if dir == padsKitPrefix {
		return padsKitRows(), nil
	}
	if dir == padsSamplesDir() && !dirExists(dir) {
		_ = os.MkdirAll(dir, 0o755)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type file struct {
		row padsBrowseRow
		mod time.Time
	}
	var dirs []padsBrowseRow
	var files []file
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			dirs = append(dirs, padsBrowseRow{label: name, detail: "Folder", path: full, dir: true})
			continue
		}
		if !padsIsAudio(name) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		detail := strings.ToUpper(strings.TrimPrefix(filepath.Ext(name), "."))
		if kb := info.Size() / 1024; kb >= 1024 {
			detail += fmt.Sprintf(" · %.1f MB", float64(kb)/1024)
		} else {
			detail += fmt.Sprintf(" · %d KB", kb)
		}
		files = append(files, file{padsBrowseRow{label: strings.TrimSuffix(name, filepath.Ext(name)), detail: detail, path: full}, info.ModTime()})
		if len(files) >= 2000 {
			break
		}
	}
	sort.Slice(dirs, func(a, b int) bool { return strings.ToLower(dirs[a].label) < strings.ToLower(dirs[b].label) })
	newest := filepath.Clean(dir) == filepath.Clean(osAppsUserDir(vmFolderName))
	sort.SliceStable(files, func(a, b int) bool {
		if newest {
			return files[a].mod.After(files[b].mod)
		}
		return strings.ToLower(files[a].row.label) < strings.ToLower(files[b].row.label)
	})
	rows := dirs
	for _, f := range files {
		rows = append(rows, f.row)
	}
	return rows, nil
}

// ── Export ───────────────────────────────────────────────────────────────

// padsRenderWAV renders the song (once through the chain in song mode, else
// four times the pattern) plus two seconds of tail into Downloads/music.
func padsRenderWAV(proj padsProject, samples map[string]*padsSample) (string, int, error) {
	const rate = 44100
	eng := newPadsEngine(rate, samples)
	eng.proj = &proj
	var order []int
	if proj.SongMode && len(proj.Song) > 0 {
		order = proj.Song
	} else {
		order = []int{proj.Current, proj.Current, proj.Current, proj.Current}
	}
	var song float64
	for _, pat := range order {
		for s := 0; s < eng.patternSteps(pat); s++ {
			song += eng.stepSamples(s)
		}
	}
	songFrames := int(song)
	frames := songFrames + 2*rate
	if frames > 10*60*rate {
		frames = 10 * 60 * rate
		songFrames = min(songFrames, frames)
	}
	eng.start()
	pcm := make([]byte, 44, 44+frames*4)
	buf := make([]float32, 512*2)
	for done := 0; done < frames; {
		n := min(512, frames-done)
		if eng.playing && done >= songFrames {
			eng.stop()
		}
		if eng.playing && done+n > songFrames {
			n = songFrames - done // stop exactly at the end of the last bar
		}
		eng.render(buf, n)
		for i := 0; i < 2*n; i++ {
			s := int16(math.Max(-1, math.Min(1, float64(buf[i]))) * 32767)
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(s))
		}
		done += n
	}
	dataLen := len(pcm) - 44
	copy(pcm[0:], "RIFF")
	binary.LittleEndian.PutUint32(pcm[4:], uint32(36+dataLen))
	copy(pcm[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(pcm[16:], 16)
	binary.LittleEndian.PutUint16(pcm[20:], 1) // PCM
	binary.LittleEndian.PutUint16(pcm[22:], 2)
	binary.LittleEndian.PutUint32(pcm[24:], rate)
	binary.LittleEndian.PutUint32(pcm[28:], rate*4)
	binary.LittleEndian.PutUint16(pcm[32:], 4)
	binary.LittleEndian.PutUint16(pcm[34:], 16)
	copy(pcm[36:], "data")
	binary.LittleEndian.PutUint32(pcm[40:], uint32(dataLen))

	dir := downloadsMusicDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	var path string
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("Pads %03d.wav", i))
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			path = cand
			break
		}
	}
	if path == "" {
		return "", 0, errors.New("the music folder already holds 999 Pads exports")
	}
	if err := AtomicWrite(path, pcm, 0o644); err != nil {
		return "", 0, err
	}
	return path, frames / rate, nil
}
