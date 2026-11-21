package main

// Kept from the subtitle tests when the suite was cut to its core; the
// isolation TestMain needs is in testhelpers_test.go.

import "testing"

// The subtitle language from the config becomes a yt-dlp argument: only a
// literal language code passes, never an option, a pattern or "all".
func TestSubtitleLanguageAcceptsOnlyLiteralCodes(t *testing.T) {
	for in, want := range map[string]string{"": "en", "pt-BR": "pt-BR", "  de ": "de", "zh-Hans": "zh-Hans"} {
		got, err := subtitleLanguage(&Config{Variables: Variables{SubtitleLanguage: in}})
		if err != nil || got != want {
			t.Errorf("subtitleLanguage(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"-en", "en.*", "1en", "en,de", "e n", "all", "ALL"} {
		if _, err := subtitleLanguage(&Config{Variables: Variables{SubtitleLanguage: bad}}); err == nil {
			t.Errorf("subtitleLanguage(%q) accepted a selector", bad)
		}
	}
}

// Subtitle files beside a video are outside data: .ass/.ssa Dialogue lines
// keep their timing and text only, and MicroDVD frames become seconds.
func TestParseASSAndMicroDVDSubtitles(t *testing.T) {
	ass := "[Script Info]\nTitle: x\n\n[V4+ Styles]\nFormat: Name, Fontname\nStyle: Default,Arial\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:05.00,0:00:07.50,Default,,0,0,0,,Second, with a comma\n" +
		"Comment: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,not shown\n" +
		"Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,{\\i1}Hello{\\i0}\\Nworld\n" +
		"Dialogue: 0,0:00:02.00,0:00:04.00,Default,,0,0,0,,{\\p1}m 0 0 l 10 10{\\p0}\n" +
		"Dialogue: 0,0:00:09.00,0:00:08.00,Default,,0,0,0,,backwards\n"
	got := parseASSSubtitles(ass)
	if len(got) != 2 || got[0].Start != 1 || got[0].End != 3 || got[0].Text != "Hello\nworld" ||
		got[1].Start != 5 || got[1].Text != "Second, with a comma" {
		t.Fatalf("parseASSSubtitles = %#v", got)
	}
	sub := "{1}{1}25\n{25}{75}First|{y:i}second\n{100}{}open end\nnot a line\n"
	cues := parseMicroDVDSubtitles(sub, 0)
	if len(cues) != 2 || cues[0].Start != 1 || cues[0].End != 3 || cues[0].Text != "First\nsecond" ||
		cues[1].Start != 4 || cues[1].End != 7 {
		t.Fatalf("parseMicroDVDSubtitles = %#v", cues)
	}
	if c := parseMicroDVDSubtitles("{50}{100}x\n", 50); len(c) != 1 || c[0].Start != 1 || c[0].End != 2 {
		t.Fatalf("MicroDVD with the video's rate = %#v", c)
	}
	if len(parseMicroDVDSubtitles("\x00\x00\x01\xba binary vobsub", 25)) != 0 {
		t.Fatal("a VobSub .sub read as text")
	}
}

// ffprobe's stream list for the track picker: tags are outside data, and
// streams count within their kind (0:a:N, 0:s:N).
func TestVideoTracksParseCountsStreamsByKind(t *testing.T) {
	data := `{"streams":[
		{"codec_type":"video","codec_name":"h264"},
		{"codec_type":"audio","codec_name":"aac","channels":2,"tags":{"language":"eng"},"disposition":{"default":1}},
		{"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle","tags":{"LANGUAGE":"eng"}},
		{"codec_type":"audio","codec_name":"ac3","channels":6,"channel_layout":"5.1(side)","tags":{"language":"ger","title":"Kommentar\n\u0007"}},
		{"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"und"},"disposition":{"forced":1}}]}`
	list, err := videoTracksParse([]byte(data))
	if err != nil || len(list.audio) != 2 || len(list.subs) != 2 {
		t.Fatalf("videoTracksParse = %#v, %v", list, err)
	}
	if a := list.audio[1]; a.Index != 1 || a.Lang != "ger" || a.Title != "Kommentar" || a.Channels != 6 {
		t.Errorf("second audio = %#v", a)
	}
	if s := list.subs[0]; !s.Bitmap || s.Lang != "eng" {
		t.Errorf("first subtitle = %#v", s)
	}
	if s := list.subs[1]; s.Bitmap || s.Index != 1 || s.Lang != "" || !s.Forced {
		t.Errorf("second subtitle = %#v", s)
	}
	if videoTracksAutoAudio(list) != 0 {
		t.Error("the default-flagged stream is not ffmpeg's pick")
	}
	if _, err := videoTracksParse([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}
