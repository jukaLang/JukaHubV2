package main

// liquidbg_motion.go — tilt for the liquid background, where the device can
// feel it.
//
// Two sources, both optional. On Linux the kernel lists accelerometers under
// /sys/bus/iio (SDL2 has no Linux sensor backend, so SDL would report none);
// a goroutine samples one about thirty times a second, because a sysfs read is
// a blocking file read the render thread must not wait on, and it stops by
// itself once nothing has asked for the tilt for a while. Elsewhere SDL's own
// sensor API covers tablets and convertible laptops; it is polled on the main
// thread, where SDL wants it, with its update events switched off so a 100 Hz
// sensor cannot flood the event queue (every event there would also keep the
// frame-rate governor awake).
//
// Neither is known to exist on the Smart Pro or the Brick. Without one the
// Settings row stays hidden and the sticks do all the stirring.

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// Where a tilt reading comes from.
const (
	liquidMotionNone = iota
	liquidMotionIIO
	liquidMotionSDL
)

const (
	// liquidMotionSettle is how long the first samples are averaged into the
	// resting pose: whatever angle the device is held at counts as level.
	liquidMotionSettle = 0.5
	// liquidMotionPoll is the IIO sampler's period.
	liquidMotionPoll = 33 * time.Millisecond
	// liquidMotionIdle stops a sampler nobody has read for this long (the
	// backdrop is off screen: a video, a game, the browser).
	liquidMotionIdle = 2 * time.Second
)

// liquidIIORoot is where Linux lists industrial-I/O sensors.
var liquidIIORoot = "/sys/bus/iio/devices"

// liquidMotionState is the accelerometer, when there is one. Everything but
// the IIO sample is main-thread state.
type liquidMotionState struct {
	probed bool
	kind   int
	iioDir string
	paths  [3]string  // the x, y and z raw channels
	scales [3]float64 // their scales, read once
	sdlIdx int

	active bool        // started and not stopped since
	sensor *sdl.Sensor // SDL source
	buf    [3]float32

	// The IIO sampler publishes here.
	mu       sync.Mutex
	sample   [3]float64
	have     bool
	running  bool
	stop     chan struct{} // closed to stop the sampler; main thread owns it
	lastRead atomic.Int64  // unix ms of the newest reader

	// Resting pose.
	started float64
	settled bool
	sum     [2]float64
	n       int
	neutral [2]float64
}

var liquidMotion liquidMotionState

// liquidMotionAvailable reports whether the device has an accelerometer the
// water can follow. It is looked up once per session, on the main thread (the
// SDL probe initialises SDL's sensor subsystem).
func liquidMotionAvailable() bool {
	m := &liquidMotion
	if m.probed {
		return m.kind != liquidMotionNone
	}
	m.probed = true
	m.kind = liquidMotionNone
	if runtime.GOOS == "linux" {
		if dir := liquidFindIIOAccel(liquidIIORoot); dir != "" {
			m.kind, m.iioDir = liquidMotionIIO, dir
			for i, axis := range [3]string{"x", "y", "z"} {
				m.paths[i] = filepath.Join(dir, "in_accel_"+axis+"_raw")
			}
			m.scales = liquidIIOScales(dir)
			logInfo("liquidbg", "tilt from %s", dir)
		}
	} else if idx, ok := liquidFindSDLAccel(); ok {
		m.kind, m.sdlIdx = liquidMotionSDL, idx
		logInfo("liquidbg", "tilt from %s", sdl.SensorGetDeviceName(idx))
	}
	return m.kind != liquidMotionNone
}

// liquidFindIIOAccel returns the first IIO device directory under root with x,
// y and z acceleration channels, or "".
func liquidFindIIOAccel(root string) string {
	matches, err := filepath.Glob(filepath.Join(root, "iio:device*"))
	if err != nil {
		return ""
	}
	for _, dir := range matches {
		complete := true
		for _, axis := range [3]string{"x", "y", "z"} {
			if _, err := os.Stat(filepath.Join(dir, "in_accel_"+axis+"_raw")); err != nil {
				complete = false
				break
			}
		}
		if complete {
			return dir
		}
	}
	return ""
}

// liquidIIOScales reads the channels' scales: a per-axis scale wins over the
// shared one, and a missing or broken one counts as 1 (only the direction of
// the reading is used, so a common factor does not matter).
func liquidIIOScales(dir string) [3]float64 {
	common := 1.0
	if v, ok := liquidReadFloat(filepath.Join(dir, "in_accel_scale")); ok && v > 0 {
		common = v
	}
	var out [3]float64
	for i, axis := range [3]string{"x", "y", "z"} {
		out[i] = common
		if v, ok := liquidReadFloat(filepath.Join(dir, "in_accel_"+axis+"_scale")); ok && v > 0 {
			out[i] = v
		}
	}
	return out
}

// liquidReadFloat reads one number from a sysfs attribute.
func liquidReadFloat(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// liquidReadIIO reads one acceleration sample: raw counts times their scales.
func liquidReadIIO(paths [3]string, scales [3]float64) ([3]float64, bool) {
	var out [3]float64
	for i, path := range paths {
		raw, ok := liquidReadFloat(path)
		if !ok {
			return out, false
		}
		out[i] = raw * scales[i]
	}
	return out, true
}

// liquidFindSDLAccel initialises SDL's sensor subsystem if nothing has and
// returns the first accelerometer it lists.
func liquidFindSDLAccel() (int, bool) {
	if sdl.WasInit(sdl.INIT_SENSOR) == 0 {
		if err := sdl.InitSubSystem(sdl.INIT_SENSOR); err != nil {
			return 0, false
		}
	}
	// The water reads the sensor when it draws; update events would only fill
	// the queue at the sensor's own rate.
	sdl.EventState(sdl.SENSORUPDATE, sdl.IGNORE)
	for i := 0; i < sdl.NumSensors(); i++ {
		if sdl.SensorGetDeviceType(i) == sdl.SENSOR_ACCEL {
			return i, true
		}
	}
	return 0, false
}

// liquidMotionStart makes sure the sensor is being read. It is called every
// frame the water wants tilt, so it is cheap when already running. restarted
// is true when it (re)started the reading — the IIO sampler spawned again
// after idling out, or the SDL sensor opened — which also starts measuring a
// new resting pose, so the caller must forget any tilt it applied against the
// old one.
func liquidMotionStart(now float64) (restarted bool) {
	m := &liquidMotion
	if !liquidMotionAvailable() {
		return false
	}
	m.lastRead.Store(time.Now().UnixMilli())
	switch m.kind {
	case liquidMotionIIO:
		m.mu.Lock()
		running := m.running
		if !running {
			m.running = true
			m.have = false
		}
		m.mu.Unlock()
		if !running {
			// A sampler that stopped by itself left its channel open.
			if m.stop != nil {
				close(m.stop)
			}
			m.stop = make(chan struct{})
			go liquidMotionSample(m.paths, m.scales, m.stop)
			m.resetPose(now)
			restarted = true
		}
	case liquidMotionSDL:
		if m.sensor == nil {
			m.sensor = sdl.SensorOpen(m.sdlIdx)
			if m.sensor == nil {
				// Listed but not openable: treat it as absent for the session.
				m.kind = liquidMotionNone
				return false
			}
			m.resetPose(now)
			restarted = true
		}
	}
	m.active = true
	return restarted
}

// liquidMotionSample is the IIO sampler.
func liquidMotionSample(paths [3]string, scales [3]float64, stop chan struct{}) {
	m := &liquidMotion
	tick := time.NewTicker(liquidMotionPoll)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		if time.Now().UnixMilli()-m.lastRead.Load() > liquidMotionIdle.Milliseconds() {
			m.mu.Lock()
			m.running = false
			m.have = false
			m.mu.Unlock()
			return
		}
		if v, ok := liquidReadIIO(paths, scales); ok {
			m.mu.Lock()
			m.sample, m.have = v, true
			m.mu.Unlock()
		}
	}
}

// liquidMotionStop stops reading the sensor (the water is off, or tilt is).
func liquidMotionStop() {
	m := &liquidMotion
	if !m.active {
		return
	}
	m.active = false
	if m.stop != nil {
		close(m.stop)
		m.stop = nil
	}
	m.mu.Lock()
	m.running = false
	m.have = false
	m.mu.Unlock()
	if m.sensor != nil {
		m.sensor.Close()
		m.sensor = nil
	}
	m.settled = false
}

// resetPose starts measuring the resting pose again.
func (m *liquidMotionState) resetPose(now float64) {
	m.started = now
	m.settled = false
	m.sum = [2]float64{}
	m.n = 0
}

// liquidMotionTilt is how far the device is tilted from its resting pose: the
// change of the "up" direction projected on the screen, in screen axes (x
// right, y down), as the sine of the angle (0.17 is about ten degrees). ok is
// false while the resting pose is still being measured and when there is no
// reading.
func liquidMotionTilt(now float64) (tx, ty float64, ok bool) {
	m := &liquidMotion
	var a [3]float64
	have := false
	switch m.kind {
	case liquidMotionIIO:
		m.lastRead.Store(time.Now().UnixMilli())
		m.mu.Lock()
		a, have = m.sample, m.have
		m.mu.Unlock()
	case liquidMotionSDL:
		if m.sensor != nil {
			sdl.SensorUpdate()
			if err := m.sensor.GetData(m.buf[:]); err == nil {
				a = [3]float64{float64(m.buf[0]), float64(m.buf[1]), float64(m.buf[2])}
				have = true
			}
		}
	}
	if !have {
		return 0, 0, false
	}
	ux, uy, valid := liquidUpProjection(a[0], a[1], a[2])
	if !valid {
		return 0, 0, false
	}
	if !m.settled {
		m.sum[0] += ux
		m.sum[1] += uy
		m.n++
		if now-m.started >= liquidMotionSettle {
			m.neutral = liquidPoseAverage(m.sum, m.n)
			m.settled = true
		}
		return 0, 0, false
	}
	return ux - m.neutral[0], uy - m.neutral[1], true
}

// liquidPoseAverage is the resting pose from n summed projections.
func liquidPoseAverage(sum [2]float64, n int) [2]float64 {
	if n <= 0 {
		return [2]float64{}
	}
	return [2]float64{sum[0] / float64(n), sum[1] / float64(n)}
}

// liquidUpProjection turns one accelerometer reading into the "up" direction
// projected on the screen. The reading is in device axes — +x right, +y up,
// +z out of the screen, reading +g away from the ground at rest, which is
// both SDL's convention and Linux IIO's — and the result is in screen axes,
// where +y points down. ok is false for a reading too small to have a
// direction (free fall, a dead sensor).
func liquidUpProjection(ax, ay, az float64) (ux, uy float64, ok bool) {
	n := math.Sqrt(ax*ax + ay*ay + az*az)
	if n < 1e-6 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, 0, false
	}
	return ax / n, -ay / n, true
}

// liquidTiltPlane is the slosh a change of tilt gives the water: the plane's
// height change across the width and the height of the pool. The "up"
// direction leans toward the edge that rose, and the water runs the other
// way, so the plane falls toward the raised edge.
func liquidTiltPlane(dx, dy, gain float64) (ax, ay float32) {
	return float32(-dx * gain), float32(-dy * gain)
}
