package main

// driverstatus_power.go — the Drivers & hardware page's CPU, Audio, Battery
// and Charger rows (registerDriverStatus, devicedrivers.go). Every function
// here only reads small sysfs/procfs files and the mixer probe's cached
// answer: no writes, no processes.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	registerDriverStatus(driverRowCPU, driverCPUStatus)
	registerDriverStatus(driverRowAudio, driverAudioStatus)
	registerDriverStatus(driverRowBattery, driverBatteryStatus)
	registerDriverStatus(driverRowCharger, driverChargerStatus)
}

// driverCPUStatus lists each cpufreq policy (one on the A133P, two clusters
// on the A523): cores, range, ceiling and governor.
func driverCPUStatus() driverStatus {
	dirs := cpufreqPolicyDirs(cpufreqRoot)
	if len(dirs) == 0 {
		return driverStatus{State: driverMissing, Driver: "cpufreq", Node: cpufreqRoot, Detail: "no CPU speed control"}
	}
	var parts []string
	for _, dir := range dirs {
		cores := len(strings.Fields(saverReadTrimmed(filepath.Join(dir, "related_cpus"))))
		lo := readKHzFile(filepath.Join(dir, "cpuinfo_min_freq"))
		part := fmt.Sprintf("%d cores %d–%d MHz, max %d, %s", cores, lo/1000,
			advertisedMaxKHz(dir)/1000, readKHzFile(filepath.Join(dir, "scaling_max_freq"))/1000,
			strings.TrimSpace(saverReadTrimmed(filepath.Join(dir, "scaling_governor"))))
		parts = append(parts, part)
	}
	driver := strings.TrimSpace(saverReadTrimmed(filepath.Join(dirs[0], "scaling_driver")))
	if driver == "" {
		driver = "cpufreq"
	}
	node := "1 policy"
	if len(dirs) > 1 {
		node = fmt.Sprintf("%d policies (clusters)", len(dirs))
	}
	return driverStatus{State: driverOK, Driver: driver, Node: node, Detail: strings.Join(parts, "; ")}
}

// driverAudioStatus is the sound card and the volume control the mixer
// probe chose (volume_trimui.go).
func driverAudioStatus() driverStatus {
	card := driverFirstSoundCard()
	st := driverStatus{State: driverOK, Driver: card, Node: "/proc/asound/cards"}
	if card == "" {
		st.State, st.Driver = driverMissing, "ALSA"
	}
	if !IsTrimuiDevice() {
		return st
	}
	line, ok := trimuiMixerStatus()
	st.Detail = "volume: " + line
	if card != "" && !ok && line != "not probed yet" {
		st.State = driverProblem
	}
	return st
}

// driverFirstSoundCard is the first card's long name from /proc/asound/cards
// (" 0 [audiocodec ]: audiocodec - audiocodec"), "" without one.
func driverFirstSoundCard() string {
	data, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] < '0' || line[0] > '9' {
			continue
		}
		if i := strings.Index(line, "]:"); i >= 0 {
			return strings.TrimSpace(line[i+2:])
		}
		return line
	}
	return ""
}

// driverBatteryStatus is the handheld's own battery: which power_supply
// entry, its level and state, and the pack size.
func driverBatteryStatus() driverStatus {
	dir := batterySysfsDir()
	if dir == "" {
		if IsTrimuiDevice() {
			return driverStatus{State: driverMissing, Driver: "power_supply", Node: powerSupplyRoot, Detail: "no battery entry"}
		}
		return driverStatus{State: driverNotUsed, Driver: "power_supply"}
	}
	st := driverStatus{State: driverOK, Driver: filepath.Base(dir), Node: dir}
	var facts []string
	if pct := batteryPercentIn(dir); pct >= 0 {
		facts = append(facts, fmt.Sprintf("%d%%", pct))
	} else {
		st.State = driverProblem
		facts = append(facts, "no level")
	}
	if s := batteryStatusIn(dir); s != "" {
		facts = append(facts, s)
	}
	if uv, ok := readBatteryCounter(filepath.Join(dir, "voltage_now")); ok && uv > 0 {
		facts = append(facts, fmt.Sprintf("%.2f V", float64(uv)/1e6))
	}
	if t, ok := readBatteryCounter(filepath.Join(dir, "temp")); ok && t > -400 && t < 1000 {
		facts = append(facts, fmt.Sprintf("%.0f °C", float64(t)/10))
	}
	if mah := batteryHealthReport().DesignCapacity; mah > 0 {
		facts = append(facts, fmt.Sprintf("%d mAh pack", mah))
	}
	st.Detail = strings.Join(facts, ", ")
	return st
}

// driverChargerStatus lists the charger inputs (USB, mains) and which has
// power on it.
func driverChargerStatus() driverStatus {
	entries, err := os.ReadDir(powerSupplyRoot)
	if err != nil {
		return driverStatus{State: driverUnknown, Driver: "power_supply"}
	}
	var names []string
	online := false
	for _, e := range entries {
		path := filepath.Join(powerSupplyRoot, e.Name())
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			continue
		}
		isCharger, on := powerSupplyCharger(path, e.Name())
		if !isCharger {
			continue
		}
		if on {
			online = true
			names = append(names, e.Name()+" (plugged in)")
		} else {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		if IsTrimuiDevice() {
			return driverStatus{State: driverMissing, Driver: "power_supply", Detail: "no charger input; charging read from the battery"}
		}
		return driverStatus{State: driverNotUsed, Driver: "power_supply"}
	}
	detail := "unplugged"
	if online {
		detail = "on external power"
	}
	return driverStatus{State: driverOK, Driver: strings.Join(names, ", "), Node: powerSupplyRoot, Detail: detail}
}
