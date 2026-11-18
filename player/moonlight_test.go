package main

// Tests for the outside data Moonlight reads: a PC's serverinfo and applist
// replies, and the stream helper's status file.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMLParseServerInfo(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr bool
		want    mlServerInfo
	}{
		{
			name: "Sunshine with a game running",
			data: `<?xml version="1.0" encoding="utf-8"?>
<root status_code="200"><hostname>DESKTOP</hostname><appversion>7.1.431.-1</appversion><GfeVersion>3.23.0.74</GfeVersion><uniqueid>ABCDEF0123</uniqueid><HttpsPort>47984</HttpsPort><ExternalPort>47989</ExternalPort><mac>00:11:22:33:44:55</mac><LocalIP>192.168.1.50</LocalIP><ServerCodecModeSupport>259</ServerCodecModeSupport><PairStatus>1</PairStatus><currentgame>881448767</currentgame><state>SUNSHINE_SERVER_BUSY</state></root>`,
			want: mlServerInfo{Hostname: "DESKTOP", UUID: "ABCDEF0123", AppVersion: "7.1.431.-1", GfeVersion: "3.23.0.74",
				MAC: "00:11:22:33:44:55", LocalIP: "192.168.1.50", State: "SUNSHINE_SERVER_BUSY", HTTPSPort: 47984,
				ExternalPort: 47989, CodecSupport: 259, CurrentGame: 881448767, Paired: true, ServerKind: "Sunshine"},
		},
		{
			name: "Apollo idle: no current game, no zero MAC",
			data: `<root status_code="200"><hostname>Den</hostname><appversion>7.1.431.-1</appversion><uniqueid>U2</uniqueid><mac>00:00:00:00:00:00</mac><PairStatus>0</PairStatus><currentgame>5</currentgame><state>SUNSHINE_SERVER_FREE</state><VirtualDisplayCapable>true</VirtualDisplayCapable></root>`,
			want: mlServerInfo{Hostname: "Den", UUID: "U2", AppVersion: "7.1.431.-1", State: "SUNSHINE_SERVER_FREE",
				VirtualDisplayCapable: true, ServerKind: "Vibepollo / Apollo"},
		},
		{
			name: "GeForce Experience",
			data: `<root status_code="200"><hostname>Rig</hostname><appversion>7.1.402.0</appversion><uniqueid>U3</uniqueid><gputype>GeForce RTX 3080</gputype></root>`,
			want: mlServerInfo{Hostname: "Rig", UUID: "U3", AppVersion: "7.1.402.0", GPU: "GeForce RTX 3080", ServerKind: "GeForce Experience"},
		},
		{name: "refused", data: `<root status_code="401" status_message="The client is not authorized"></root>`, wantErr: true},
		{name: "no uniqueid", data: `<root status_code="200"><hostname>X</hostname></root>`, wantErr: true},
		{name: "not XML", data: `hello`, wantErr: true},
	}
	for _, c := range cases {
		got, err := mlParseServerInfo([]byte(c.data))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	_, err := mlParseServerInfo([]byte(`<root status_code="401" status_message="The client is not authorized"></root>`))
	if mlHTTPCode(err) != 401 {
		t.Errorf("a refusal reads as code %d (%v), want 401", mlHTTPCode(err), err)
	}
	if _, err := mlParseServerInfo([]byte(`<html><body>Router login</body></html>`)); !errors.Is(err, errMLNotGameStream) {
		t.Errorf("a web page reads as %v, want errMLNotGameStream", err)
	}
}

// mDNS instance names arrive DNS-escaped: \DDD is a byte (UTF-8 once
// joined), \c is c.
func TestMLMDNSName(t *testing.T) {
	for in, want := range map[string]string{
		`Den\226\128\153s\ PC`:   "Den’s PC",
		`Living\ Room\.Sunshine`: "Living Room.Sunshine",
		`Bad\255byte`:            "Bad?byte",
		``:                       "gaming-rig",
	} {
		if got := mlMDNSName(in, "gaming-rig.local."); got != want {
			t.Errorf("mlMDNSName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMLParseAppList(t *testing.T) {
	data := `<?xml version="1.0" encoding="utf-8"?>
<root status_code="200">
<App><IsHdrSupported>0</IsHdrSupported><AppTitle>Desktop</AppTitle><ID>881448767</ID></App>
<App><IsHdrSupported>1</IsHdrSupported><AppTitle>Steam Big Picture &amp; more</AppTitle><ID>1093255277</ID><UUID>3A1E5C2F-1111-2222-3333-444455556666</UUID></App>
<App><AppTitle>No id</AppTitle><ID>x</ID></App>
</root>`
	apps, err := mlParseAppList([]byte(data))
	if err != nil {
		t.Fatalf("applist: %v", err)
	}
	want := []mlApp{
		{ID: 881448767, Title: "Desktop"},
		{ID: 1093255277, Title: "Steam Big Picture & more", UUID: "3A1E5C2F-1111-2222-3333-444455556666", HDR: true},
	}
	if len(apps) != len(want) {
		t.Fatalf("apps = %+v, want %+v", apps, want)
	}
	for i := range want {
		if apps[i] != want[i] {
			t.Errorf("app %d = %+v, want %+v", i, apps[i], want[i])
		}
	}
	if _, err := mlParseAppList([]byte(`<root status_code="503" status_message="busy"/>`)); mlHTTPCode(err) != 503 {
		t.Errorf("a refused applist reads as %v, want code 503", err)
	}
}

func TestMLParseLastRun(t *testing.T) {
	full := "version=1\nrc=0\nresult=ok\nmessage=Stream ended.\nconnected=1\nseconds=754\nframes=45012\n" +
		"host=192.168.1.50\ncodec=h264\nquit_app=1\nstage=\nerror_code=0\nport_flags=\ndecode_ms=7.4\nfps_shown=59.2\n" +
		"handoff_step=starting moonlight-stream\n"
	run, ok := mlParseLastRun([]byte(full))
	if !ok || run.Result != mlRunOK || run.RC != 0 || !run.Connected || run.Seconds != 754 || run.Frames != 45012 ||
		run.Host != "192.168.1.50" || run.Codec != "h264" || !run.QuitApp || run.DecodeMS != 7.4 || run.FPSShown != 59.2 ||
		run.LogOffset != -1 || run.HandoffStep != "starting moonlight-stream" {
		t.Errorf("full note = %+v, %v", run, ok)
	}

	failed := "result=connect_failed\nstage=RTSP handshake\nerror_code=10060\nport_flags=TCP 48010\n"
	run, ok = mlParseLastRun([]byte(failed))
	if !ok || run.Result != mlRunConnectFailed || run.Stage != "RTSP handshake" || run.ErrorCode != 10060 || run.PortFlags != "TCP 48010" {
		t.Errorf("connect_failed note = %+v, %v", run, ok)
	}

	started := "version=1\nresult=started\nhost=pc.example\nlog_offset=120\nhandoff_log=/etc/passwd\n"
	run, ok = mlParseLastRun([]byte(started))
	if !ok || run.Result != mlRunStarted || run.LogOffset != 120 || run.HandoffLog != "" {
		t.Errorf("started note = %+v, %v (handoff_log must be the script's own fallback only)", run, ok)
	}
	run, ok = mlParseLastRun([]byte("handoff_log=" + mlFallbackLogPath + "\nresult=started\n"))
	if !ok || run.HandoffLog != mlFallbackLogPath {
		t.Errorf("fallback log = %q, %v", run.HandoffLog, ok)
	}

	for rc, want := range map[string]string{"11": mlRunConnectionLost, "8": mlRunConnectFailed, "127": mlRunNotStarted, "139": mlRunCrashed} {
		run, ok = mlParseLastRun([]byte("rc=" + rc + "\n"))
		if !ok || run.Result != want {
			t.Errorf("rc=%s reads as %q, want %q", rc, run.Result, want)
		}
	}

	if _, ok := mlParseLastRun([]byte("hello\nworld=1\n")); ok {
		t.Error("a note with neither result nor rc was read as one")
	}
}

// The handheld check's note (contract section 5b) and the toast it becomes.
func TestMLParseSelfTest(t *testing.T) {
	full := "version=1\nrc=0\nresult=self_test\nmessage=Everything works.\nst_video=1\nst_renderer=opengles2\n" +
		"st_decode=1\nst_p_ms=7.9\nst_upload_ms=1.6\nst_audio=1\nst_opus=1\nst_core=1\nst_pads=1\nst_input=1\n" +
		"st_motor=1\nST_Bad-Key=1\nst_=1\nhandoff_step=Wi-Fi wlan0: power saving off (iw)\n"
	rep, ok := mlParseSelfTest([]byte(full))
	if !ok || !rep.checked() || !rep.HaveRC || rep.RC != 0 || rep.Result != mlSelfTestResult ||
		rep.Values["st_p_ms"] != "7.9" || rep.Values["st_renderer"] != "opengles2" || len(rep.Values) != 11 {
		t.Fatalf("full note = %+v, %v", rep, ok)
	}
	c := mlDeviceCheckFrom(rep, time.Time{}, 1280, 720)
	if !c.Picture || !c.Sound || !c.Controls || !c.Rumble || !c.Engine || c.Recommended != mlPresetBalanced {
		t.Errorf("full check = %+v", c)
	}
	msg, kind := mlSelfTestSummary(c, 1280, 720)
	for _, want := range []string{"picture, sound, controls and rumble work", "720p", "9.5 ms", "Balanced (60 fps)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary %q lacks %q", msg, want)
		}
	}
	if kind != ToastKindSuccess {
		t.Errorf("full check toast kind = %v, want success", kind)
	}

	failed := "result=self_test\nrc=10\nst_video=0\nst_audio=1\nst_opus=1\nst_pads=1\nst_input=0\nst_p_ms=5\n"
	rep, ok = mlParseSelfTest([]byte(failed))
	if !ok || !rep.checked() || rep.RC != 10 {
		t.Fatalf("failed note = %+v, %v", rep, ok)
	}
	c = mlDeviceCheckFrom(rep, time.Time{}, 1280, 720)
	if c.Picture || !c.Sound || c.Controls || c.Recommended != "" {
		t.Errorf("failed check = %+v", c)
	}
	msg, kind = mlSelfTestSummary(c, 1280, 720)
	for _, want := range []string{"sound works", "the picture did not open", "no button press came"} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary %q lacks %q", msg, want)
		}
	}
	if kind != ToastKindError {
		t.Errorf("failed check toast kind = %v, want error", kind)
	}

	// The helper never wrote: JukaHub's "started" note with the hand-off's steps.
	started := "version=1\nresult=started\nlog_offset=10\nhandoff_step=started; waiting for JukaHub (pid 1) to close\n" +
		"handoff_step=the streaming helper is missing: /x\nhandoff_log=/etc/passwd\n"
	rep, ok = mlParseSelfTest([]byte(started))
	if !ok || rep.checked() || rep.Result != mlRunStarted || rep.LogOffset != 10 || rep.HandoffLog != "" ||
		rep.HandoffStep != "the streaming helper is missing: /x" {
		t.Errorf("started note = %+v, %v", rep, ok)
	}

	// A note cut off mid-line keeps what it has.
	rep, ok = mlParseSelfTest([]byte("st_video=1\nst_dec"))
	if !ok || !rep.checked() || len(rep.Values) != 1 || rep.Values["st_video"] != "1" {
		t.Errorf("partial note = %+v, %v", rep, ok)
	}

	if _, ok := mlParseSelfTest([]byte("hello\nworld=1\n")); ok {
		t.Error("a note with no result, rc or check value was read as one")
	}
}

func TestMLRecommendFor(t *testing.T) {
	cases := []struct {
		pMS, uploadMS float64
		w, h          int
		want          string
	}{
		{7.9, 1.6, 1280, 720, mlPresetBalanced},
		{10, 1, 1280, 720, mlPresetBalanced}, // exactly the 11 ms budget
		{14, 2, 1280, 720, mlPresetLight},    // 16 ms; 9 ms at 3/4 size
		{20, 2, 1280, 720, mlPresetSteady30}, // 22 ms; 12.4 ms at 3/4 size
		{10, 1, 1024, 768, mlPresetBalanced}, // 9.4 ms on the Brick's panel
		{12, 1, 1024, 768, mlPresetLight},    // 11.1 ms on the Brick's panel
		{10, 1, 0, 0, mlPresetBalanced},      // unknown panel: 1280x720
		{0, 1.6, 1280, 720, ""},              // no decode measured
	}
	for _, c := range cases {
		if got := mlRecommendFor(c.pMS, c.uploadMS, c.w, c.h); got != c.want {
			t.Errorf("mlRecommendFor(%v, %v, %dx%d) = %q, want %q", c.pMS, c.uploadMS, c.w, c.h, got, c.want)
		}
	}
	// No picture, no recommendation, whatever the timing says.
	c := mlDeviceCheck{Values: map[string]string{"st_video": "1", "st_decode": "0", "st_p_ms": "5"}}
	if got := c.recommend(1280, 720); got != "" {
		t.Errorf("a check whose clip did not decode recommends %q", got)
	}
}
