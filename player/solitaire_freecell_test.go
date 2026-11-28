package main

import (
	"os"
	"testing"
)

// Microsoft's numbered deals are part of the game's language: deal 1 and
// deal 617 are published card for card. The generator in solDealFreeCell
// must reproduce them so a number shared by players deals the same cards.
func TestSolFreeCellNumberedDeals(t *testing.T) {
	deal1 := []string{
		"JD 2D 9H JC 5D 7H 7C",
		"5H KD KC 9S 5S AD QC",
		"KH 3H 2S KS 9D QD JS",
		"AS AH 3C 4C 5C TS QH",
		"4H AC 4D 7S 3S TD",
		"4S TH 8H 2C JH 7D",
		"6D 8S 8D QS 6C 3D",
		"8C TC 6S 9C 2H 6H",
	}
	deal617 := []string{
		"7D AD 5C 3S 5S 8C 2D",
		"AH TD 7S QD AC 6D 8H",
		"AS KH TH QC 3H 9D 6S",
		"8D 3D TC KD 5H 9S 3C",
		"8S 7H 4D JS 4C QS 9C",
		"9H 7C 6H 2C 2S 4S TS",
		"2H 5D JC 6C JH QH JD",
		"KS KC 4H",
	}
	for _, tc := range []struct {
		seed int64
		want []string
	}{
		{1, deal1}, {617, deal617},
	} {
		g := solDealFor(solVariantFreeCell, tc.seed, 1)
		if g.variant != solVariantFreeCell || len(g.piles) != solFPiles {
			t.Fatalf("deal %d: wrong layout: %+v", tc.seed, g)
		}
		for p := range g.kinds {
			if p < solFTab0 && len(g.piles[p]) != 0 {
				t.Fatalf("deal %d: pile %d is not empty", tc.seed, p)
			}
		}
		var got []string
		for p := solFTab0; p < solFPiles; p++ {
			line := ""
			for _, c := range g.piles[p] {
				if !c.up {
					t.Fatalf("deal %d: a tableau card is face down", tc.seed)
				}
				line += c.code() + " "
			}
			got = append(got, trimSpace(line))
		}
		if len(got) != len(tc.want) {
			t.Fatalf("deal %d: %d columns, want %d", tc.seed, len(got), len(tc.want))
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("deal %d column %d:\n got %q\nwant %q", tc.seed, i+1, got[i], tc.want[i])
			}
		}
	}
}

func trimSpace(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

func solFreecellFixture() *solGame {
	g := &solGame{variant: solVariantFreeCell, kinds: solFreeCellKinds[:]}
	g.piles = make([][]solCard, solFPiles)
	for i := range g.piles {
		g.piles[i] = []solCard{}
	}
	return g
}

func TestSolFreeCellSupermoveLimit(t *testing.T) {
	g := solFreecellFixture()
	// Four free cells and one empty column: onto a column the limit is ten,
	// onto an empty column it is five (the column is consumed by the move).
	g.piles[8] = []solCard{
		{suit: solSpades, rank: 9, up: true}, {suit: solHearts, rank: 8, up: true},
		{suit: solClubs, rank: 7, up: true}, {suit: solDiamonds, rank: 6, up: true},
		{suit: solSpades, rank: 5, up: true},
	}
	for p := 9; p < 15; p++ {
		g.piles[p] = []solCard{{suit: solHearts, rank: 10, up: true}}
	}
	if got := g.supermoveLimit(9); got != 10 {
		t.Fatalf("limit onto an occupied column = %d, want 10", got)
	}
	if got := g.supermoveLimit(15); got != 5 {
		t.Fatalf("limit onto an empty column = %d, want 5", got)
	}
	// With the destination actually empty the cap is five cards.
	g.piles[15] = []solCard{}
	if !g.move(8, 5, 15) {
		t.Fatal("a five-card run was refused with four free cells and an empty column")
	}
}

func TestSolFreeCellSmartCellLast(t *testing.T) {
	g := solFreecellFixture()
	// One free cell, no foundation to build on and no fitting column.
	g.piles[0] = []solCard{{suit: solSpades, rank: 5, up: true}}
	for p := solFFound0; p < solFFound0+4; p++ {
		g.piles[p] = []solCard{{suit: uint8(p - solFFound0), rank: 2, up: true}}
	}
	for p := solFTab0; p < solFPiles; p++ {
		g.piles[p] = []solCard{{suit: solHearts, rank: 9, up: true}}
	}
	g.piles[2] = []solCard{{suit: solSpades, rank: 5, up: true}} // occupied cells
	g.piles[3] = []solCard{{suit: solSpades, rank: 5, up: true}}
	if got := g.smartTarget(0, 1, true); got != 1 {
		t.Fatalf("smart target = %d, want the empty cell 1", got)
	}
	if n, to := g.smartMoveFrom(0, 1); n != 1 || to != 1 {
		t.Fatalf("smart move = (%d,%d), want (1,1)", n, to)
	}
}

func TestSolFreeCellAutoFinish(t *testing.T) {
	g := solDealFor(solVariantFreeCell, 1, 1)
	if g.canAutoFinish() {
		t.Fatal("the first deal was declared finished: every FreeCell card starts face up")
	}
	// One card left on the table, face up and ready for its foundation.
	full := solFreecellFixture()
	for p := solFFound0; p < solFFound0+4; p++ {
		for r := uint8(1); r <= 13; r++ {
			if p == solFFound0 && r == 13 {
				continue
			}
			full.piles[p] = append(full.piles[p], solCard{suit: uint8(p - solFFound0), rank: r, up: true})
		}
	}
	full.piles[solFTab0] = []solCard{{suit: solSpades, rank: 13, up: true}}
	if !full.canAutoFinish() {
		t.Fatal("a single card left for a foundation was not finished")
	}
}

func TestSolFreeCellStatsVersion2(t *testing.T) {
	g := solDealFor(solVariantFreeCell, 7, 1)
	fc := solStats{Played: 2, Won: 1, Streak: 1, BestStreak: 1}
	klon := solStats{Played: 9, Won: 4}
	opts := solOptions{Draw: 1, Variant: solVariantFreeCell}
	data, err := solEncodeFile(g, fc, opts, map[string]solStats{
		solVariantFreeCell: fc, solVariantKlondike: klon,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/solitaire.json"
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, back, err := solLoadFile(path)
	if err != nil || back == nil || back.variant != solVariantFreeCell {
		t.Fatalf("FreeCell save did not load: %+v, %+v, %v", f, back, err)
	}
	if f.StatsByVariant[solVariantKlondike] != klon {
		t.Fatalf("Klondike stats were lost: %+v", f.StatsByVariant)
	}
	if f.Options.Variant != solVariantFreeCell {
		t.Fatalf("variant was not restored: %+v", f.Options)
	}

	// Version 1 had no map and no variant: both belong to Klondike.
	legacy := `{"version":1,"stats":{"played":3,"won":1},"options":{"draw":1}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _, err = solLoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.StatsByVariant[solVariantKlondike].Played != 3 || f.Options.Variant != solVariantKlondike {
		t.Fatalf("version-1 file was not read as Klondike: %+v", f)
	}
}
