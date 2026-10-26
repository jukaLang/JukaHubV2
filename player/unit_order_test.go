package main

import "testing"

// The unit converter's Left/Right keys walk getUnitsForCategory, and the
// renderer picks its first and last entries as the default from/to when a
// category is chosen. Both used to iterate the conversion map, whose order Go
// randomizes per run — so the default pair was arbitrary and Right jumped
// unpredictably. These tests pin a stable order.

// TestUnitOrderIsDeterministic: calling twice returns the same slice, so the
// default units and the Left/Right walk no longer change between frames/runs.
func TestUnitOrderIsDeterministic(t *testing.T) {
	for _, cat := range []string{"length", "weight", "temperature", "volume", "speed", "time", "data", "area"} {
		first := getUnitsForCategory(cat)
		for i := 0; i < 5; i++ {
			again := getUnitsForCategory(cat)
			if len(first) != len(again) {
				t.Fatalf("%s: length changed %d -> %d between calls", cat, len(first), len(again))
			}
			for j := range first {
				if first[j] != again[j] {
					t.Fatalf("%s: order changed: %v vs %v", cat, first, again)
				}
			}
		}
	}
}

// TestUnitOrderMatchesConversionTable: every ordered unit must be convertible —
// present in the category's table (or a temperature key), so a default or a
// cycled selection can never produce "Error" on Enter.
func TestUnitOrderMatchesConversionTable(t *testing.T) {
	for _, cat := range []string{"length", "weight", "temperature", "volume", "speed", "time", "data", "area"} {
		for _, u := range getUnitsForCategory(cat) {
			if cat == "temperature" {
				// Temperature keys are case-insensitive letters; validate via convert.
				if _, ok := convertTemperature(0, u, "C"); !ok {
					t.Errorf("temperature unit %q does not convert", u)
				}
				continue
			}
			if _, ok := unitConversionTable[cat][u]; !ok {
				t.Errorf("%s: ordered unit %q is missing from the conversion table", cat, u)
			}
		}
	}
}

// TestUnitOrderCoversEveryTableEntry: no convertible unit is left out of the
// order, or it would become unreachable by Left/Right.
func TestUnitOrderCoversEveryTableEntry(t *testing.T) {
	for cat, table := range unitConversionTable {
		ordered := getUnitsForCategory(cat)
		have := map[string]bool{}
		for _, u := range ordered {
			have[u] = true
		}
		for u := range table {
			if !have[u] {
				t.Errorf("%s: table unit %q is missing from the display order", cat, u)
			}
		}
		if len(ordered) != len(table) {
			t.Errorf("%s: order has %d units, table has %d", cat, len(ordered), len(table))
		}
	}
}

// TestUnitDefaultsAreStable: the from/to a freshly-selected category lands on
// (first and last of the order) are fixed, matching the pre-map randomization
// intent (e.g. length defaults m -> mile by the ordered list).
func TestUnitDefaultsAreStable(t *testing.T) {
	units := getUnitsForCategory("length")
	if units[0] != "mm" || units[len(units)-1] != "mile" {
		t.Fatalf("length defaults = %s -> %s, want mm -> mile", units[0], units[len(units)-1])
	}
	// And a conversion across that default pair is valid.
	if _, ok := convertUnit(1, units[0], units[len(units)-1], "length"); !ok {
		t.Fatal("the default length pair does not convert")
	}
}
