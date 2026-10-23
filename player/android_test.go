package main

// Tests for the Android container backend. Every filesystem, process and
// environment read goes through androidHost, so the whole detect → list →
// launch path is exercised without Waydroid, a binder device or a compositor.

import (
	"errors"
	"strings"
	"testing"
)

// androidFakeHost builds a host with just the pieces a test cares about.
type androidFake struct {
	binary    string
	lookErr   error
	binder    bool
	status    string
	statusErr error
	env       map[string]string
	weston    string
	westonErr string
	calls     [][]string
}

func (f *androidFake) host() androidHost {
	return androidHost{
		lookPath: func(string) (string, error) {
			if f.lookErr != nil {
				return "", f.lookErr
			}
			return f.binary, nil
		},
		run: func(name string, args ...string) (string, error) {
			f.calls = append(f.calls, append([]string{name}, args...))
			return f.status, f.statusErr
		},
		exists: func(path string) bool {
			return f.binder
		},
		getenv:    func(key string) string { return f.env[key] },
		weston:    f.weston,
		westonErr: f.westonErr,
	}
}

func androidOKHost() *androidFake {
	return &androidFake{
		binary: "/usr/bin/waydroid",
		binder: true,
		status: "Waydroid is running\nSession: RUNNING\n",
		env:    map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
	}
}

func TestResolveAndroidBackendWithoutBinder(t *testing.T) {
	fake := &androidFake{binary: "/usr/bin/waydroid"}
	backend, err := resolveAndroidBackend(fake.host())
	if err == nil {
		t.Fatal("a kernel without binder was accepted")
	}
	if !strings.Contains(err.Error(), "binder") {
		t.Errorf("error = %q, want it to name the missing binder device", err)
	}
	if backend.Binary != "/usr/bin/waydroid" {
		t.Errorf("backend binary = %q, want the found waydroid", backend.Binary)
	}
}

func TestResolveAndroidBackendReportsSession(t *testing.T) {
	fake := androidOKHost()
	backend, err := resolveAndroidBackend(fake.host())
	if err != nil {
		t.Fatalf("running container rejected: %v", err)
	}
	if backend.Kind != androidBackendWaydroid || backend.Session != androidSessionRunning {
		t.Fatalf("backend = %+v, want waydroid running", backend)
	}
	if len(fake.calls) != 1 || strings.Join(fake.calls[0], " ") != "/usr/bin/waydroid status" {
		t.Errorf("calls = %v, want one status probe", fake.calls)
	}

	// A stopped session is usable: the caller starts it.
	stopped := androidOKHost()
	stopped.status = "Session: STOPPED\n"
	backend, err = resolveAndroidBackend(stopped.host())
	if err != nil || backend.Session != androidSessionStopped {
		t.Fatalf("stopped session = %+v, %v; want it accepted as stopped", backend, err)
	}
}

func TestResolveAndroidBackendMissingContainer(t *testing.T) {
	fake := &androidFake{lookErr: errors.New("not found"), binder: true}
	if _, err := resolveAndroidBackend(fake.host()); err == nil || !strings.Contains(err.Error(), "Waydroid") {
		t.Fatalf("error = %v, want a Waydroid install hint", err)
	}

	unknown := androidOKHost()
	unknown.env["JUKAHUB_ANDROID_BACKEND"] = "anbox"
	if _, err := resolveAndroidBackend(unknown.host()); err == nil || !strings.Contains(err.Error(), "waydroid") {
		t.Fatalf("unknown backend error = %v", err)
	}
}

func TestParseAndroidSession(t *testing.T) {
	cases := map[string]string{
		"Session: RUNNING":          androidSessionRunning,
		"  session  :  running  ":   androidSessionRunning,
		"Session: STOPPED":          androidSessionStopped,
		"Session: STARTING":         "",
		"Waydroid is not installed": "",
		"":                          "",
	}
	for input, want := range cases {
		if got := parseAndroidSession(input); got != want {
			t.Errorf("parseAndroidSession(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseAndroidApps(t *testing.T) {
	block := `Name: Android Settings
Package: com.android.settings
Name: Chrome
Package: com.android.chrome
`
	apps := parseAndroidApps(block)
	if len(apps) != 2 {
		t.Fatalf("parsed %d apps from the block form: %+v", len(apps), apps)
	}
	if apps[0].label() != "Android Settings" || apps[0].Package != "com.android.settings" {
		t.Errorf("apps[0] = %+v, want the name paired with its own package", apps[0])
	}
	if apps[1].Package != "com.android.chrome" || apps[1].Name != "Chrome" {
		t.Errorf("apps[1] = %+v, want Chrome", apps[1])
	}

	// A list that puts the package first must keep its name as well.
	reversed := parseAndroidApps("Package: com.android.camera\nName: Camera\n")
	if len(reversed) != 1 || reversed[0].Name != "Camera" {
		t.Errorf("package-first list = %+v, want the Camera label", reversed)
	}

	// A tab-separated package list is accepted too, and duplicates collapse.
	columns := parseAndroidApps("com.example.game\tExample Game\ncom.example.game\tExample Game\ncom.android.chrome\t\n")
	private := 0
	for _, app := range columns {
		if app.Package == "com.example.game" {
			private++
		}
		if app.Package == "com.android.chrome" && app.Name != "" {
			t.Errorf("blank name overwrote a previous label: %+v", app)
		}
	}
	if private != 1 {
		t.Errorf("duplicate package listed %d times, want 1", private)
	}

	// A package name that is not one is never listed.
	if got := parseAndroidApps("Package: not a package\nPackage: -rf\nbadline\n"); len(got) != 0 {
		t.Errorf("junk produced %+v", got)
	}
	if got := parseAndroidApps("Name: Orphan\n"); len(got) != 0 {
		t.Errorf("a name with no package produced %+v", got)
	}
}

func TestAndroidPackageValid(t *testing.T) {
	valid := []string{
		"com.android.chrome",
		"org.example.game_v2",
		"a.b",
		"Com.Example.App9",
	}
	for _, pkg := range valid {
		if !androidPackageValid(pkg) {
			t.Errorf("%q was rejected", pkg)
		}
	}
	invalid := []string{
		"",
		"android",
		"-rf",
		"com..app",
		".com.app",
		"com.app.",
		"com.app; rm -rf /",
		"com.app name",
		"9com.app",
		"com.app/../etc",
	}
	for _, pkg := range invalid {
		if androidPackageValid(pkg) {
			t.Errorf("%q was accepted", pkg)
		}
	}
}

func TestAndroidLaunchCommandPrefersExistingWayland(t *testing.T) {
	fake := androidOKHost()
	name, args, err := androidLaunchCommand("com.android.chrome", "", fake.host())
	if err != nil {
		t.Fatalf("launch failed: %v", err)
	}
	if name != "/usr/bin/waydroid" {
		t.Errorf("command = %q, want waydroid", name)
	}
	if got := strings.Join(args, " "); got != "app launch com.android.chrome" {
		t.Errorf("args = %q, want the app launch argv", got)
	}
}

func TestAndroidLaunchCommandUsesWestonPack(t *testing.T) {
	fake := androidOKHost()
	fake.env = map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}
	fake.weston = "/data/weston/westonwrap.sh"
	name, args, err := androidLaunchCommand("com.android.settings", "auto", fake.host())
	if err != nil {
		t.Fatalf("launch failed: %v", err)
	}
	if name != fake.weston {
		t.Fatalf("command = %q, want the WestonPack wrapper", name)
	}
	joined := strings.Join(args, " ")
	want := "drm gl kiosk system XDG_RUNTIME_DIR=/run/user/1000 /usr/bin/waydroid app launch com.android.settings"
	if joined != want {
		t.Fatalf("wrapped args = %q, want %q", joined, want)
	}
}

func TestAndroidLaunchCommandNeedsACompositor(t *testing.T) {
	fake := androidOKHost()
	fake.env = map[string]string{}
	if _, _, err := androidLaunchCommand("com.android.chrome", "", fake.host()); err == nil {
		t.Fatal("a device with no compositor was accepted")
	} else if !strings.Contains(err.Error(), "Wayland") {
		t.Errorf("error = %q, want it to explain the missing compositor", err)
	}

	// Explicit wayland without a session is refused rather than silently
	// falling back to the wrapper.
	if _, _, err := androidLaunchCommand("com.android.chrome", "wayland", fake.host()); err == nil {
		t.Error("display=wayland without WAYLAND_DISPLAY was accepted")
	}
	// There is no headless Android app: the surface is the app.
	if _, _, err := androidLaunchCommand("com.android.chrome", "headless", fake.host()); err == nil {
		t.Error("display=headless was accepted for an Android app")
	}
}

func TestAndroidLaunchCommandRefusesOptionInjection(t *testing.T) {
	fake := androidOKHost()
	for _, pkg := range []string{"-rf", "--help", "com.app extra", "com.app;id"} {
		if _, args, err := androidLaunchCommand(pkg, "", fake.host()); err == nil {
			t.Errorf("%q was launched as %v", pkg, args)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("the container was contacted for a rejected package: %v", fake.calls)
	}
}

func TestListAndroidApps(t *testing.T) {
	fake := androidOKHost()
	fake.status = "Name: Chrome\nPackage: com.android.chrome\n"
	backend := androidBackend{Kind: androidBackendWaydroid, Binary: "/usr/bin/waydroid", Session: androidSessionRunning}
	apps, err := listAndroidApps(backend, fake.host())
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(apps) != 1 || apps[0].Package != "com.android.chrome" {
		t.Fatalf("apps = %+v", apps)
	}
	if got := strings.Join(fake.calls[0], " "); got != "/usr/bin/waydroid app list" {
		t.Errorf("calls = %q, want the list argv", got)
	}

	// A container that fails produces the error only when nothing was parsed.
	failing := androidOKHost()
	failing.status = "binder: no such device\n"
	failing.statusErr = errors.New("exit status 1")
	if _, err := listAndroidApps(backend, failing.host()); err == nil {
		t.Error("a failed list with no output was reported as success")
	}
	failing.status = "Package: com.android.chrome\nbinder: no such device\n"
	if apps, err := listAndroidApps(backend, failing.host()); err != nil || len(apps) != 1 {
		t.Errorf("partial list = %+v, %v; want the parsed app", apps, err)
	}
}

func TestAndroidSessionStartCommand(t *testing.T) {
	backend := androidBackend{Binary: "/usr/bin/waydroid"}
	name, args := androidSessionStartCommand(backend)
	if name != "/usr/bin/waydroid" || strings.Join(args, " ") != "session start" {
		t.Fatalf("session start = %q %v", name, args)
	}
}
