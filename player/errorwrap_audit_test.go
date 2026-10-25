package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// errorWrapVerb finds the verb of every conversion in a fmt format string.
var errorWrapVerb = regexp.MustCompile(`%[-+#0-9.\[\]*]*([a-zA-Z%])`)

// TestErrorWrapsKeepTheChain enforces the convention CONTRIBUTING.md states: a
// wrapped error uses %w, so errors.Is / errors.As still see the cause. A %v
// there silently erases the chain — that is how the launch-failure dialog lost
// exec.ErrNotFound and offered a generic hint for a missing launcher.
func TestErrorWrapsKeepTheChain(t *testing.T) {
	const root = "."

	var offenders []string
	walkErr := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			return nil
		}
		found, scanErr := errorWrapOffenders(file)
		if scanErr != nil {
			return scanErr
		}
		offenders = append(offenders, found...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d fmt.Errorf call(s) format an error with %%v, which breaks errors.Is/errors.As; use %%w:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// TestErrorWrapAuditRule proves the audit itself works, so a rule that silently
// stopped matching would not look like a clean codebase.
func TestErrorWrapAuditRule(t *testing.T) {
	const source = `package main

import "fmt"

func f(err error, rollbackErr error, value string) error {
	_ = fmt.Errorf("loses the chain: %v", err)
	_ = fmt.Errorf("keeps the chain: %w", err)
	_ = fmt.Errorf("not an error: %v", value)
	_ = fmt.Errorf("one wrap is enough: %w; context: %v", err, rollbackErr)
	_ = fmt.Errorf("suffix names count: %v", resolveErr)
	_ = fmt.Errorf("formatting is fine: %q", err)
	return nil
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	offenders, err := errorWrapOffenders(file)
	if err != nil {
		t.Fatalf("scan sample: %v", err)
	}
	if len(offenders) != 2 {
		t.Fatalf("flagged %d call(s), want 2 (the %%v on err and on resolveErr):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
	for _, line := range offenders {
		if !strings.Contains(line, "loses the chain") && !strings.Contains(line, "suffix names count") {
			t.Errorf("unexpected offender: %s", line)
		}
	}
}

// TestLaunchFailureHintSurvivesWrapping is the behaviour the convention buys:
// a launch failure wrapped with %w still satisfies errors.Is, so the dialog
// picks the specific suggestion instead of the generic one.
func TestLaunchFailureHintSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("could not start RetroArch: %w", exec.ErrNotFound)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatal("wrapped launch failure lost exec.ErrNotFound")
	}
	hint := launchFailureSuggestion(err)
	if !strings.Contains(strings.ToLower(hint), "launcher or a required runtime is missing") {
		t.Errorf("hint for a missing launcher = %q, want the reinstall suggestion", hint)
	}
}

// errorWrapOffenders reports fmt.Errorf calls in one Go file that format an
// error with %v. The audit is deliberately narrow: it looks at arguments that
// are plainly errors (err, xxxErr), and it accepts a message that already uses
// %w for one cause, because Go allows exactly one wrapped error.
func errorWrapOffenders(file string) ([]string, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		return nil, err
	}
	var offenders []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 || !isFmtErrorf(call.Fun) {
			return true
		}
		format, ok := call.Args[0].(*ast.BasicLit)
		if !ok || format.Kind != token.STRING {
			return true
		}
		text, unquoteErr := strconv.Unquote(format.Value)
		if unquoteErr != nil {
			return true
		}
		verbs := errorWrapVerb.FindAllStringSubmatch(text, -1)
		for _, verb := range verbs {
			if verb[1] == "w" {
				return true
			}
		}
		for i, arg := range call.Args[1:] {
			if i >= len(verbs) || verbs[i][1] != "v" || !isErrorIdent(arg) {
				continue
			}
			pos := fset.Position(call.Pos())
			offenders = append(offenders, filepath.ToSlash(pos.String())+": "+format.Value)
		}
		return true
	})
	return offenders, nil
}

func isFmtErrorf(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Errorf" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "fmt"
}

// isErrorIdent recognizes the argument shapes that are certainly an error: a
// variable named err, or one with an Err/Error suffix (resolveErr, tarErr).
func isErrorIdent(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident.Name == "_" {
		return false
	}
	name := ident.Name
	return name == "err" || strings.HasSuffix(name, "Err") || strings.HasSuffix(name, "Error")
}
