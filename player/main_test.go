package main

import (
	"reflect"
	"testing"
)

func TestSplitShellArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{
			`yt-dlp "https://www.youtube.com/feed/trending" --flat-playlist --print "%(title)s|%(id)s" -f "best[height<=720]"`,
			[]string{"yt-dlp", "https://www.youtube.com/feed/trending", "--flat-playlist", "--print", "%(title)s|%(id)s", "-f", "best[height<=720]"},
		},
		{
			`ytsearch12:popular`,
			[]string{"ytsearch12:popular"},
		},
		{
			`yt-dlp "https://youtube.com/watch?v=abc" -f "best[height<=480]"`,
			[]string{"yt-dlp", "https://youtube.com/watch?v=abc", "-f", "best[height<=480]"},
		},
	}
	for _, c := range cases {
		got := splitShellArgs(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitShellArgs(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
