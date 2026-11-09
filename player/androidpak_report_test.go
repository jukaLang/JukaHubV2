package main

import (
	"reflect"
	"strings"
	"testing"
)

// Report lines exactly as the AndroidRT runner prints them (androidrt/src/
// jnienv.c for the JNI layer, bionicld.c for the loader).
var androidPakTestJavaReport = []string{
	"[androidrt][jni] report: 1 unknown classes, 0 APK classes without implementation, 2 unimplemented members (1 called)",
	"[androidrt][jni] report: FindClass returned null for (1):",
	"[androidrt][jni] report:   com/foo/Missing",
	"[androidrt][jni] report: unimplemented members (\"call ...\" = the game called it) (3):",
	"[androidrt][jni] report:   com/foo/Bar.baz(I)V",
	"[androidrt][jni] report:   call com/foo/Bar.baz(I)V",
	"[androidrt][jni] report:   com/foo/Bar.count:I",
}

// What a run that crashed leaves: the first-seen lines only, no report.
var androidPakTestJavaSeen = []string{
	"[androidrt][jni] FindClass (\"com/foo/A\") -> null (no implementation, not in the APK)",
	"[androidrt][jni] GetMethodID com/foo/B.m()V -> not implemented; calls return 0",
	"[androidrt][jni] -> native com/foo/B.nativeInit()V",
	"[androidrt][jni] called com/foo/B.m()V — not implemented, returned 0",
	"[androidrt][jni] GetStaticFieldID com/foo/B.f I -> not implemented; reads return 0 until the game stores a value",
}

func androidPakTestLog(parts ...[]string) string {
	var lines []string
	lines = append(lines, "=== game start 2026-11-09 ===", "[androidrt] manifest: com.foo 1.0, activity com.foo.Main (minSdk 21, targetSdk 28)")
	for _, p := range parts {
		lines = append(lines, p...)
	}
	return strings.Join(lines, "\r\n") // launch logs may carry CRLF
}

func TestParseAndroidPakJavaGaps(t *testing.T) {
	cases := []struct {
		name                     string
		log                      string
		classes, members, called []string
		total                    int
	}{
		{"exit report", androidPakTestLog(androidPakTestJavaReport),
			[]string{"com/foo/Missing"}, []string{"com/foo/Bar.baz(I)V", "com/foo/Bar.count:I"},
			[]string{"com/foo/Bar.baz(I)V"}, 3},
		{"first-seen lines (crashed run)", androidPakTestLog(androidPakTestJavaSeen),
			[]string{"com/foo/A"}, []string{"com/foo/B.m()V", "com/foo/B.f:I"}, []string{"com/foo/B.m()V"}, 0},
		{"the report wins over first-seen lines", androidPakTestLog(androidPakTestJavaSeen, androidPakTestJavaReport),
			[]string{"com/foo/Missing"}, []string{"com/foo/Bar.baz(I)V", "com/foo/Bar.count:I"},
			[]string{"com/foo/Bar.baz(I)V"}, 3},
		{"a class asked for twice (older runtime) is one gap", androidPakTestLog([]string{
			"[androidrt][jni] report: FindClass returned null for (2):",
			"[androidrt][jni] report:   com/foo/Twice",
			"[androidrt][jni] report:   com/foo/Twice",
			"[androidrt][jni] report:   ... and 7 more (each was logged when first seen)",
		}), []string{"com/foo/Twice"}, nil, nil, 0},
		{"a lookup on a null class is a consequence, not a gap", androidPakTestLog([]string{
			"[androidrt][jni] GetMethodID(class=null, getWindow, ()Landroid/view/Window;) -> null",
		}), nil, nil, nil, 0},
	}
	for _, c := range cases {
		r := parseAndroidPakRunReport(c.log)
		if !reflect.DeepEqual(r.JavaClasses, c.classes) || !reflect.DeepEqual(r.JavaMembers, c.members) ||
			!reflect.DeepEqual(r.JavaCalled, c.called) || r.JavaTotal != c.total {
			t.Errorf("%s: classes %q members %q called %q total %d; want %q %q %q %d", c.name, r.JavaClasses,
				r.JavaMembers, r.JavaCalled, r.JavaTotal, c.classes, c.members, c.called, c.total)
		}
	}
}

func TestAndroidPakRunMessage(t *testing.T) {
	const logPath = "/mnt/SDCARD/data/game-profiles/logs/game.log"
	cases := []struct {
		name    string
		log     string
		toast   bool
		wantHas []string
		wantNot []string
	}{
		{"the exit report names the first method the game called", androidPakTestLog(androidPakTestJavaReport), true,
			[]string{"the game called the Java method com/foo/Bar.baz(I)V", "(2 more Java gaps in the log)", logPath}, nil},
		{"a crashed run is named from the first-seen lines", androidPakTestLog(androidPakTestJavaSeen), true,
			[]string{"the Java method com/foo/B.m()V", "(2 more Java gaps in the log)"}, nil},
		{"a missing class when nothing unimplemented was called", androidPakTestLog([]string{
			"[androidrt][jni] report: 1 unknown classes, 0 APK classes without implementation, 0 unimplemented members (0 called)",
			"[androidrt][jni] report: FindClass returned null for (1):",
			"[androidrt][jni] report:   com/foo/Missing",
		}), true, []string{"the game asked for the Java class com/foo/Missing, which AndroidRT does not provide yet"},
			[]string{"more Java gaps"}},
		{"the old stub's FindClass(\"...\") form", androidPakTestLog([]string{
			"[androidrt][jni] FindClass(\"com/foo/Old\") -> null",
		}), true, []string{"the Java class com/foo/Old"}, nil},
		{"a member that was only looked up", androidPakTestLog([]string{
			"[androidrt][jni] GetFieldID com/foo/Bar.count I -> not implemented; reads return 0 until the game stores a value",
		}), true, []string{"looked up the Java member com/foo/Bar.count:I"}, nil},
		{"a bionic trap is the cause, not Java", androidPakTestLog([]string{
			"[androidrt] trap: libgame.so called unimplemented AChoreographer_getInstance — aborting (ANDROIDPAK_MISSING=zero returns 0 instead)",
		}, androidPakTestJavaSeen), true, []string{"the game called AChoreographer_getInstance"}, []string{"Java"}},
		{"a load failure is the cause, not Java", androidPakTestLog([]string{
			"[androidrt] could not load /stage/lib/armeabi-v7a/libgame.so: 32-bit ELF (ELFCLASS32)",
		}, androidPakTestJavaSeen), true, []string{"could not load the game: 32-bit ELF"}, []string{"Java"}},
		{"nothing Java-specific: the generic message, no toast", androidPakTestLog([]string{
			"[androidrt][jni] GetMethodID(class=null, getWindow, ()Landroid/view/Window;) -> null",
			"[androidrt] undefined symbol: apak_missing (func, needed by libgame.so, from libc.so@LIBC)",
		}), false, []string{"game first run failed (1 undefined symbols listed)"}, []string{"Java"}},
	}
	for _, c := range cases {
		msg, toast := androidPakRunMessage("game", parseAndroidPakRunReport(c.log), logPath)
		if toast != c.toast {
			t.Errorf("%s: toast = %v, want %v (%q)", c.name, toast, c.toast, msg)
		}
		for _, s := range c.wantHas {
			if !strings.Contains(msg, s) {
				t.Errorf("%s: %q lacks %q", c.name, msg, s)
			}
		}
		for _, s := range c.wantNot {
			if strings.Contains(msg, s) {
				t.Errorf("%s: %q should not contain %q", c.name, msg, s)
			}
		}
	}
}
