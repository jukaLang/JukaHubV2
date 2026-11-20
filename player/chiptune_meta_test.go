package main

// chiptune_meta_test.go — the soundtrack header parsers read outside data:
// hand-built headers check the fields they take and the song addresses.

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func TestChiptuneSongPath(t *testing.T) {
	cases := []struct {
		in   string
		file string
		song int
		ok   bool
	}{
		{"/m/Mega Man 2.nsf#3", "/m/Mega Man 2.nsf", 3, true},
		{"/m/tune.SID#12", "/m/tune.SID", 12, true},
		{"/m/song.mp3#3", "/m/song.mp3#3", 0, false},
		{"/m/intro.spc#2", "/m/intro.spc#2", 0, false}, // SPC has no song list
		{"/m/x.nsf#", "/m/x.nsf#", 0, false},
		{"/m/x.nsf#0", "/m/x.nsf#0", 0, false},
		{"/m/x.nsf#2a", "/m/x.nsf#2a", 0, false},
		{"http://host/x.nsf#3", "http://host/x.nsf#3", 0, false},
	}
	for _, c := range cases {
		file, song, ok := chiptuneSplitPath(c.in)
		if file != c.file || song != c.song || ok != c.ok {
			t.Errorf("chiptuneSplitPath(%q) = %q, %d, %v; want %q, %d, %v", c.in, file, song, ok, c.file, c.song, c.ok)
		}
	}
	if p := chiptuneSongPath("/m/a.gbs", 7); p != "/m/a.gbs#7" || chiptuneFileOf(p) != "/m/a.gbs" {
		t.Errorf("song path round trip: %q", p)
	}
	if !isAudioFile("/m/a.gbs#7") || !isAudioFile("theme.vgz") || isAudioFile("go.mod") || !isAudioFile("song.xm") {
		t.Error("isAudioFile does not follow the soundtrack formats")
	}
}

func TestChiptuneHeaders(t *testing.T) {
	put := func(b []byte, off int, s string) { copy(b[off:], s) }

	// NSF: 24 songs from song 2.
	nsf := make([]byte, 0x80)
	put(nsf, 0, "NESM\x1a")
	nsf[5], nsf[6], nsf[7] = 1, 24, 2
	put(nsf, 0x0E, "Mega Man 2")
	put(nsf, 0x2E, "Takashi Tateishi")
	put(nsf, 0x4E, "1988 Capcom")
	m, err := chiptuneParseNSF(nsf)
	if err != nil || m.Songs != 24 || m.Start != 2 || m.Game != "Mega Man 2" || m.Artist != "Takashi Tateishi" || m.System != "NES" {
		t.Fatalf("NSF = %+v, %v", m, err)
	}
	if secs, fade := m.timing(1); secs+fade != chiptuneDefaultSeconds {
		t.Errorf("NSF default length = %v + %v", secs, fade)
	}

	// NSFE: a plst of two songs (3 then 1) with titles and times.
	var nsfe bytes.Buffer
	nsfe.WriteString("NSFE")
	chunk := func(id string, data []byte) {
		_ = binary.Write(&nsfe, binary.LittleEndian, uint32(len(data)))
		nsfe.WriteString(id)
		nsfe.Write(data)
	}
	ms := func(v ...int32) []byte {
		var b bytes.Buffer
		for _, x := range v {
			_ = binary.Write(&b, binary.LittleEndian, x)
		}
		return b.Bytes()
	}
	chunk("INFO", []byte{0, 0x80, 0, 0x80, 0, 0x80, 0, 0, 3, 0})
	chunk("DATA", make([]byte, 64))
	chunk("auth", []byte("Game\x00Composer\x00(c)\x00Ripper\x00"))
	chunk("tlbl", []byte("One\x00Two\x00Three\x00"))
	chunk("time", ms(60000, -1, 90500))
	chunk("fade", ms(-1, -1, 2000))
	chunk("plst", []byte{2, 0})
	chunk("NEND", nil)
	data := nsfe.Bytes()
	m, err = chiptuneParseNSFE(bytes.NewReader(data), int64(len(data)))
	if err != nil || m.Songs != 2 || m.Game != "Game" || m.Artist != "Composer" ||
		m.title(1) != "Three" || m.title(2) != "One" || m.track(2) != 1 {
		t.Fatalf("NSFE = %+v, %v", m, err)
	}
	if secs, fade := m.timing(1); secs != 90.5 || fade != 2 {
		t.Errorf("NSFE song 1 timing = %v, %v", secs, fade)
	}

	// GBS.
	gbs := make([]byte, 0x70)
	put(gbs, 0, "GBS")
	gbs[3], gbs[4], gbs[5] = 1, 30, 1
	put(gbs, 0x10, "Link's Awakening")
	if m, err = chiptuneParseGBS(gbs); err != nil || m.Songs != 30 || m.Game != "Link's Awakening" || m.System != "Game Boy" {
		t.Fatalf("GBS = %+v, %v", m, err)
	}

	// SPC with a text ID666 tag: 180 s, fade 10000 ms.
	spc := make([]byte, 0x100)
	put(spc, 0, "SNES-SPC700 Sound File Data v0.30")
	spc[0x21], spc[0x22], spc[0x23] = 26, 26, 26
	put(spc, 0x2E, "Corridors of Time")
	put(spc, 0x4E, "Chrono Trigger")
	put(spc, 0xA9, "180")
	put(spc, 0xAC, "10000")
	put(spc, 0xB1, "Yasunori Mitsuda")
	m, err = chiptuneParseSPC(spc)
	if err != nil || m.title(1) != "Corridors of Time" || m.Game != "Chrono Trigger" || m.Artist != "Yasunori Mitsuda" {
		t.Fatalf("SPC = %+v, %v", m, err)
	}
	if secs, fade := m.timing(1); secs != 180 || fade != 10 {
		t.Errorf("SPC timing = %v, %v", secs, fade)
	}

	// VGM 1.50 with a YM2612, a loop and a GD3 tag.
	vgm := make([]byte, 0x40)
	put(vgm, 0, "Vgm ")
	le := binary.LittleEndian
	le.PutUint32(vgm[0x08:], 0x150)
	le.PutUint32(vgm[0x18:], 44100*60)
	le.PutUint32(vgm[0x20:], 44100*20)
	le.PutUint32(vgm[0x2C:], 7670453)
	le.PutUint32(vgm[0x34:], 0x0C)
	var gd3 []byte
	for _, s := range []string{"Green Hill Zone", "", "Sonic the Hedgehog", "", "Sega Mega Drive / Genesis", "", "Masato Nakamura", "", "1991", "", ""} {
		for _, r := range utf16.Encode([]rune(s)) {
			gd3 = le.AppendUint16(gd3, r)
		}
		gd3 = append(gd3, 0, 0)
	}
	le.PutUint32(vgm[0x14:], uint32(len(vgm)-0x14))
	vgm = append(vgm, "Gd3 "...)
	vgm = le.AppendUint32(vgm, 0x100)
	vgm = le.AppendUint32(vgm, uint32(len(gd3)))
	vgm = append(vgm, gd3...)
	m, err = chiptuneParseVGM(bytes.NewReader(vgm), int64(len(vgm)))
	if err != nil || m.title(1) != "Green Hill Zone" || m.Game != "Sonic the Hedgehog" || m.System != "Mega Drive" || m.Artist != "Masato Nakamura" {
		t.Fatalf("VGM = %+v, %v", m, err)
	}
	if secs, fade := m.timing(1); secs != 80 || fade != chiptuneDefaultFade {
		t.Errorf("VGM timing = %v, %v", secs, fade)
	}

	// PSID v2: load address in the data, three songs, CIA speed on song 2.
	psid := make([]byte, 0x7C)
	put(psid, 0, "PSID")
	be := binary.BigEndian
	be.PutUint16(psid[0x04:], 2)
	be.PutUint16(psid[0x06:], 0x7C)
	be.PutUint16(psid[0x0A:], 0x1000)
	be.PutUint16(psid[0x0C:], 0x1003)
	be.PutUint16(psid[0x0E:], 3)
	be.PutUint16(psid[0x10:], 2)
	be.PutUint32(psid[0x12:], 2)
	put(psid, 0x16, "Commando")
	put(psid, 0x36, "Rob Hubbard")
	put(psid, 0x56, "1985 Elite")
	be.PutUint16(psid[0x76:], 0x24) // PAL, 8580
	psid = append(psid, 0x00, 0x10, 0x60, 0x60, 0x60, 0x60)
	tune, err := sidParse(psid)
	if err != nil || tune.Load != 0x1000 || tune.Init != 0x1000 || tune.Play != 0x1003 || tune.Songs != 3 ||
		tune.Start != 2 || tune.Title != "Commando" || !tune.Model8580 || tune.NTSC || len(tune.Data) != 4 {
		t.Fatalf("PSID = %+v, %v", tune, err)
	}
	rsid := append([]byte("RSID"), psid[4:]...)
	if tune, err = sidParse(rsid); err != nil || tune.playable() != errSIDNeedsC64 {
		t.Errorf("RSID not refused: %v", err)
	}
	// PSID v3 with a second chip at $D420: its writes must miss the one chip.
	psid3 := append([]byte(nil), psid...)
	be.PutUint16(psid3[0x04:], 3)
	psid3[0x7A] = 0x42
	if tune, err = sidParse(psid3); err != nil || tune.ExtraSID[0] != 0xD420 {
		t.Errorf("PSID v3 second SID = %#x, %v", tune.ExtraSID[0], err)
	}

	// SAP: a TIME that is not a number takes the default length.
	sap := []byte("SAP\r\nSONGS 2\r\nTIME nan\r\nTIME 01:30.5 LOOP\r\n\xff\xff")
	if m, err = chiptuneParseSAP(sap); err != nil || m.Songs != 2 || m.seconds(1) != chiptuneDefaultSeconds || m.Secs[1] != 90.5 {
		t.Errorf("SAP = %+v, %v", m, err)
	}

	// Truncated and foreign headers are refused, not misread.
	for name, parse := range map[string]func([]byte) (chiptuneMeta, error){
		"nsf": chiptuneParseNSF, "gbs": chiptuneParseGBS, "spc": chiptuneParseSPC,
		"ay": chiptuneParseAY, "sap": chiptuneParseSAP, "hes": chiptuneParseHES,
	} {
		if _, err := parse([]byte("RIFF....WAVEfmt ")); err == nil {
			t.Errorf("%s parser accepted a WAV header", name)
		}
	}
}
