package main

// psremote_regist.go — PS5 pairing through the native regist helper.
//
// PS4 pairing is pure Go (psremote_ps4.go). A PS5 additionally needs the PSN
// Account-ID, which the console checks against the account that holds the
// Remote Play entitlement; that handshake lives in libchiaki's regist code, so
// JukaHub writes the helper's small input file and reads its result file
// instead of reimplementing it. The helper is the only PS5-specific binary:
// streaming uses the same `chiaki-stream` as the PS4 path with ps5=1.

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ps5RegistInput is what the helper expects: host, the 8-digit PIN and the
// base64 account id. The helper removes the file after reading it, and it
// decides the target itself (it only speaks the PS5 handshake).
func ps5RegistInput(host, pin, accountID string) string {
	return "host=" + host + "\n" +
		"pin=" + pin + "\n" +
		"account_id=" + accountID + "\n"
}

// ps5ParseResult turns the helper's result file into a console.
func ps5ParseResult(values map[string]string) (psConsole, error) {
	target := psTargetPS5_1
	if raw := strings.TrimSpace(values["target"]); raw != "" {
		if parsed, err := parseIntDefault(raw, psTargetPS5_1); err == nil {
			target = parsed
		}
	}
	if target < psTargetPS5 {
		return psConsole{}, errors.New("the helper paired a PS4 target for a PS5 console")
	}
	registKey := strings.ToLower(strings.TrimSpace(values["regist_key"]))
	rawRegist, err := hex.DecodeString(registKey)
	if err != nil || len(rawRegist) == 0 {
		return psConsole{}, errors.New("the PS5 helper returned an unreadable registration key")
	}
	// libchiaki stores the key as raw ASCII; a helper that prints the key bytes
	// is accepted the same way the PS4 parser accepts them.
	trimmed := strings.TrimRight(string(rawRegist), "\x00")
	if !allHexBytes([]byte(trimmed)) {
		registKey = strings.ToLower(hex.EncodeToString([]byte(trimmed)))
	} else {
		registKey = strings.ToLower(trimmed)
	}
	if !psRegistKeyValid(registKey) {
		return psConsole{}, errors.New("the PS5 helper returned an invalid registration key")
	}
	rawRP, err := hex.DecodeString(strings.TrimSpace(values["rp_key"]))
	if err != nil || len(rawRP) != 16 {
		return psConsole{}, errors.New("the PS5 helper returned an unreadable session key")
	}
	mac, err := hex.DecodeString(strings.TrimSpace(values["server_mac"]))
	if err != nil || len(mac) != 6 {
		return psConsole{}, errors.New("the PS5 helper returned an unreadable MAC address")
	}
	rpKeyType := 0
	if raw := strings.TrimSpace(values["rp_key_type"]); raw != "" {
		if parsed, err := parseIntDefault(raw, 0); err == nil {
			rpKeyType = parsed
		}
	}
	return psConsole{
		Name:      strings.TrimSpace(values["name"]),
		PS5:       true,
		Target:    target,
		RegistKey: registKey,
		RPKey:     base64.StdEncoding.EncodeToString(rawRP),
		RPKeyType: rpKeyType,
		ServerMAC: strings.ToLower(hex.EncodeToString(mac)),
		PairedAt:  time.Now(),
	}, nil
}

// psHelperEnv gives the helper its libraries. The Brick Pro carries an OpenSSL
// 1.1 build next to the binary and firmware 3.x must not win.
func psHelperEnv(engine psEngine) []string {
	env := os.Environ()
	runtimeDir := psEngineRuntimeDir(engine)
	if runtimeDir == "" {
		return env
	}
	libs := runtimeDir
	if current := strings.TrimSpace(os.Getenv("LD_LIBRARY_PATH")); current != "" {
		libs += ":" + current
	}
	env = append(env, "LD_LIBRARY_PATH="+libs)
	var preload []string
	for _, name := range []string{"libcrypto.so.1.1", "libssl.so.1.1"} {
		path := filepath.Join(runtimeDir, name)
		if regularFile(path) {
			preload = append(preload, path)
		}
	}
	if len(preload) > 0 {
		if existing := strings.TrimSpace(os.Getenv("LD_PRELOAD")); existing != "" {
			preload = append(preload, existing)
		}
		env = append(env, "LD_PRELOAD="+strings.Join(preload, ":"))
	}
	return env
}

// ps5Register pairs with a PS5 by running the native regist helper.
func ps5Register(engine psEngine, host, pin, accountID string, timeout time.Duration) (psConsole, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return psConsole{}, errors.New("no console address")
	}
	if _, err := ps4ParsePin(pin); err != nil {
		return psConsole{}, err
	}
	account, offline, err := ps4DecodeAccountID(accountID)
	if err != nil {
		return psConsole{}, err
	}
	if offline {
		return psConsole{}, errors.New("a PS5 needs the PSN Account-ID from Link Device")
	}
	if !regularFile(engine.Regist) {
		return psConsole{}, errors.New("the PS5 pairing helper is missing (chiaki-regist)")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	dir := psSessionDir()
	inFile, err := os.CreateTemp(dir, "jukahub-ps5-*.conf")
	if err != nil {
		return psConsole{}, err
	}
	inPath := inFile.Name()
	defer os.Remove(inPath)
	_ = inFile.Chmod(0o600)
	if _, err := inFile.WriteString(ps5RegistInput(host, strings.TrimSpace(pin), base64.StdEncoding.EncodeToString(account[:]))); err != nil {
		inFile.Close()
		return psConsole{}, err
	}
	if err := inFile.Close(); err != nil {
		return psConsole{}, err
	}

	outPath := filepath.Join(dir, fmt.Sprintf("jukahub-ps5-result-%d.conf", time.Now().UnixNano()))
	defer os.Remove(outPath)

	cmd := exec.Command(engine.Regist, inPath, outPath)
	cmd.Dir = filepath.Dir(engine.Regist)
	cmd.Env = psHelperEnv(engine)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return psConsole{}, fmt.Errorf("could not start the pairing helper: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			stage := psHelperStage(output.String())
			return psConsole{}, fmt.Errorf("PS5 pairing failed at %s: %s", stage, psHelperReason(stage, output.String()))
		}
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return psConsole{}, errors.New("the PS5 did not answer in time — check that Link Device is open and the PIN is current")
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		return psConsole{}, errors.New("the pairing helper produced no result file")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	console, err := ps5ParseResult(values)
	if err != nil {
		return psConsole{}, err
	}
	console.Host = host
	return console, nil
}

// psHelperStage reads the furthest stage the helper reached.
func psHelperStage(output string) string {
	stage := "start"
	for _, candidate := range []string{"input", "start", "network", "protocol", "result"} {
		if strings.Contains(output, "stage="+candidate) {
			stage = candidate
		}
	}
	return stage
}

// psHelperReason picks the most useful line of helper output for the panel.
func psHelperReason(stage, output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "stage=") {
			continue
		}
		if _, detail, ok := strings.Cut(line, "upstream="); ok {
			if detail = strings.TrimSpace(detail); detail != "" {
				return detail
			}
		}
	}
	if stage == "network" {
		return "could not reach the console"
	}
	if stage == "protocol" {
		return "the console rejected the pairing — check the PIN and Account-ID"
	}
	return "stage " + stage
}

func parseIntDefault(value string, fallback int) (int, error) {
	var parsed int
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed)
	if err != nil {
		return fallback, err
	}
	return parsed, nil
}
