package main

// winbatch.go — running a Windows batch file (.bat/.cmd) without letting its
// path or arguments reach cmd.exe as syntax (SEC-12).
//
// Windows runs a batch file through cmd.exe, which parses the command line by
// its own rules: outside quotes & | < > ^ ( ) @ split or redirect commands,
// and %NAME% is replaced everywhere, quotes or not. Go quotes arguments for
// CommandLineToArgvW, not for cmd.exe — the os/exec documentation says so and
// points at SysProcAttr.CmdLine — so exec.Command("cmd", "/c", path) broke
// "Game (USA).bat" at the parenthesis and ran whatever followed a '&' in a
// file name. Arguments passed that way are the class of bug behind
// CVE-2024-24576 ("BatBadBut").
//
// winBatchCommandLine builds the whole line instead:
//
//	"<cmd.exe>" /d /v:off /s /c ""<script>" "<arg>" …"
//
//   - /d skips the AutoRun commands the registry can attach to every cmd.exe;
//   - /v:off turns delayed expansion off, so '!' is an ordinary character;
//   - /s makes cmd.exe strip exactly the outer pair of quotes and keep the rest
//     as written, so every piece inside stays quoted;
//   - inside quotes & | < > ^ ( ) @ are literal.
//
// What quoting cannot make literal is refused, not escaped: '"' (it would end
// the quoted piece early), '%' (expanded even inside quotes) and CR, LF or NUL
// (they end the command). The launchers pass paths from their own scans, so a
// refusal means a file to rename, never a command run by mistake.

import (
	"errors"
	"fmt"
	"strings"
)

// errWinBatchUnsafe reports a path or argument cmd.exe could not take as text.
var errWinBatchUnsafe = errors.New("contains a character cmd.exe would read as a command (\" or %)")

// winBatchUnsafeChars cannot be made literal on a cmd.exe command line.
const winBatchUnsafeChars = "\"%\r\n\x00"

// winBatchComspec is the cmd.exe to start: comspec (%ComSpec%) when it is a
// drive-rooted path quoting can hold, else cmd.exe in systemRoot's System32
// (%SystemRoot%), else C:\Windows\System32\cmd.exe — never a bare "cmd.exe"
// looked up along PATH. winBatchCommand uses one answer for the program it
// starts and the line it hands it.
func winBatchComspec(comspec, systemRoot string) string {
	comspec = strings.TrimSpace(comspec)
	if winBatchDrivePath(comspec) && !strings.ContainsAny(comspec, winBatchUnsafeChars) {
		return comspec
	}
	systemRoot = strings.TrimRight(strings.TrimSpace(systemRoot), `\/`)
	if !winBatchDrivePath(systemRoot+`\`) || strings.ContainsAny(systemRoot, winBatchUnsafeChars) {
		systemRoot = `C:\Windows`
	}
	return systemRoot + `\System32\cmd.exe`
}

// winBatchDrivePath reports whether p starts with a drive root, "C:\" or "C:/".
func winBatchDrivePath(p string) bool {
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z'
}

// winBatchCommandLine is the raw command line that runs script (a .bat or
// .cmd file) with args through comspec (cmd.exe), for SysProcAttr.CmdLine. It
// fails closed on anything quoting cannot protect.
func winBatchCommandLine(comspec, script string, args []string) (string, error) {
	if strings.TrimSpace(comspec) == "" || strings.ContainsAny(comspec, winBatchUnsafeChars) {
		comspec = "cmd.exe"
	}
	if strings.TrimSpace(script) == "" {
		return "", errors.New("no batch file to run")
	}
	if strings.ContainsAny(script, winBatchUnsafeChars) {
		return "", fmt.Errorf("the batch file's path %w", errWinBatchUnsafe)
	}
	var b strings.Builder
	b.WriteString(`"` + comspec + `" /d /v:off /s /c "`)
	b.WriteString(`"` + script + `"`)
	for i, arg := range args {
		if strings.ContainsAny(arg, winBatchUnsafeChars) {
			return "", fmt.Errorf("argument %d %w", i+1, errWinBatchUnsafe)
		}
		b.WriteString(` "` + arg + `"`)
	}
	b.WriteString(`"`)
	return b.String(), nil
}
