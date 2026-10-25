package main

import (
	"encoding/json"
	"testing"
)

// The config layer accepts numbers written by two different authors: the player
// itself (JSON numbers) and the web builder / XML dialect (strings). These
// tests pin what each reader accepts so the "why is my element the default
// size" question has an answer in one place.
//
// The division of labour is deliberate:
//   - width/height are pixel sizes and go through parsePosInt: a non-negative
//     int32, optional surrounding whitespace, no unit suffix.
//   - x/y/columns/rows go through intOrString, which is the lenient reader: it
//     tolerates "30", " 30 ", "30px" and floats (truncated).
//   - StringOrInt stores the width/height text verbatim, so the strict reader
//     stays the only place sizes are interpreted.

func TestParsePosIntAcceptance(t *testing.T) {
	tests := []struct {
		input string
		want  int32
		ok    bool
		why   string
	}{
		{"30", 30, true, "plain decimal"},
		{" 30 ", 30, true, "padded values arrive from XML attributes and JSON strings"},
		{"+30", 30, true, "ParseInt accepts a leading plus"},
		{"030", 30, true, "leading zeroes are decimal, not octal"},
		{"0", 0, true, "zero is a valid size"},
		{"2147483647", 2147483647, true, "largest int32"},
		{"", 0, false, "missing value"},
		{"   ", 0, false, "whitespace is not a number"},
		{"-1", 0, false, "sizes are non-negative"},
		{"-0", 0, true, "negative zero is zero"},
		{"30px", 0, false, "unit suffixes belong to x/y, not to sizes"},
		{"30.5", 0, false, "pixel sizes are integers"},
		{"1e3", 0, false, "exponent form is not a pixel size"},
		{"2147483648", 0, false, "overflow falls back rather than truncating"},
		{"٣٠", 0, false, "non-ASCII digits are not decoded"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := parsePosInt(tt.input)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parsePosInt(%q) = (%d, %t), want (%d, %t) — %s", tt.input, got, ok, tt.want, tt.ok, tt.why)
			}
		})
	}
}

func TestElementSizeWhitespaceIsTolerated(t *testing.T) {
	element := Element{Width: " 320 ", Height: " 48\n"}
	if got := getElementWidth(element, 100); got != 320 {
		t.Errorf("getElementWidth(padded) = %d, want 320", got)
	}
	if got := getElementHeight(element, 20); got != 48 {
		t.Errorf("getElementHeight(padded) = %d, want 48", got)
	}
}

func TestStringOrIntDecodesBuilderAndPlayerForms(t *testing.T) {
	var element Element
	// The builder writes strings, the player writes numbers; both must land in
	// the same field, verbatim, so the strict size reader sees what was typed.
	if err := json.Unmarshal([]byte(`{"width":"280","height":48}`), &element); err != nil {
		t.Fatalf("decode element: %v", err)
	}
	if element.Width != "280" || element.Height != "48" {
		t.Errorf("StringOrInt forms decoded to width=%q height=%q, want 280/48", element.Width, element.Height)
	}
}

func TestStringOrIntRejectsWhatSizesCannotBe(t *testing.T) {
	tests := []struct {
		json  string
		want  string
		ok    bool
		label string
	}{
		{`"30"`, "30", true, "string"},
		{`30`, "30", true, "integer"},
		{`null`, "", true, "null clears the field"},
		{`"30px"`, "30px", true, "kept verbatim; the size reader rejects it later"},
		{`30.5`, "", false, "a fractional size is a config error, not a truncation"},
		{`true`, "", false, "boolean is not a size"},
		{`{}`, "", false, "object is not a size"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			var got StringOrInt
			err := json.Unmarshal([]byte(tt.json), &got)
			if (err == nil) != tt.ok {
				t.Fatalf("json.Unmarshal(%s) error = %v, want ok=%t", tt.json, err, tt.ok)
			}
			if tt.ok && string(got) != tt.want {
				t.Errorf("json.Unmarshal(%s) = %q, want %q", tt.json, string(got), tt.want)
			}
		})
	}
}

func TestIntOrStringLenientReader(t *testing.T) {
	tests := []struct {
		json  string
		want  int32
		ok    bool
		label string
	}{
		{`30`, 30, true, "number"},
		{`"30"`, 30, true, "numeric string"},
		{`" 30 "`, 30, true, "padding"},
		{`"30px"`, 30, true, "unit suffix tolerated for x/y"},
		{`null`, 0, true, "null is zero"},
		{`""`, 0, true, "empty string is zero"},
		{`30.9`, 30, true, "floats truncate toward zero (coordinates are int32)"},
		{`-12`, -12, true, "coordinates may be negative"},
		{`"abc"`, 0, false, "non-numeric text is an error"},
		{`true`, 0, false, "boolean is an error"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			var got intOrString
			err := json.Unmarshal([]byte(tt.json), &got)
			if (err == nil) != tt.ok {
				t.Fatalf("json.Unmarshal(%s) error = %v, want ok=%t", tt.json, err, tt.ok)
			}
			if tt.ok && int32(got) != tt.want {
				t.Errorf("json.Unmarshal(%s) = %d, want %d", tt.json, int32(got), tt.want)
			}
		})
	}
}
