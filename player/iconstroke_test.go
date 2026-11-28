package main

// UX-4 — one icon stroke weight.
//
// The icon language allows a per-operation width, which is right for the two
// things that are not outlines: a dot (a moveto-only path, drawn as a round
// cap) and a knockout (K, the gap between overlapping shapes). Every path that
// actually draws an outline must use the base stroke, so a glyph does not get
// a heavier edge than its neighbours at 32 px. The Packages glyph — a filled
// 3D box among outlined objects — is the one object that was still solid; it
// has to be outlines like the rest.
//
// The atlas is built at runtime from these definitions (iconatlas.go), so a
// change here is re-baked on the next run; there is no version to bump.

import "testing"

// iconDotPath reports whether every command is a moveto: the path has no
// length, so the base-width round cap is the dot itself. Such an op may name
// a width to keep the dot legible, without being an outline of another weight.
func iconDotPath(op iconOp) bool {
	if len(op.cmds) == 0 {
		return false
	}
	for _, c := range op.cmds {
		if c.op != 'M' {
			return false
		}
	}
	return true
}

func TestIconVisibleStrokesUseOneWeight(t *testing.T) {
	parsed := 0
	for _, def := range iconDefs {
		ops, err := iconParse(def.src)
		if err != nil {
			t.Fatalf("%s: %v", def.name, err)
		}
		parsed++
		for i, op := range ops {
			if op.kind == 'K' || iconDotPath(op) {
				continue
			}
			if op.width != 1 {
				t.Errorf("%s op %d (%c): width %g, want the base width 1", def.name, i, op.kind, op.width)
			}
		}
	}
	if parsed != len(iconDefs) {
		t.Fatalf("parsed %d of %d icons", parsed, len(iconDefs))
	}
}

func TestPackagesIconIsOutlined(t *testing.T) {
	for _, def := range iconDefs {
		if def.name != "packages" {
			continue
		}
		ops, err := iconParse(def.src)
		if err != nil {
			t.Fatalf("packages: %v", err)
		}
		if len(ops) == 0 {
			t.Fatal("packages has no paint operations")
		}
		for i, op := range ops {
			if op.kind != 's' {
				t.Errorf("packages op %d is %q, want a stroke: the box is outlines, not a filled body", i, string(op.kind))
			}
		}
		return
	}
	t.Fatal("no packages icon in iconDefs")
}
