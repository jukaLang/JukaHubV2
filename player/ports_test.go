package main

import "testing"

// TestPortsRowNameWidthReservesChipAndStatus is the Ports row's layout
// contract: the title budget plus the chip, its gap and the right-aligned
// status exactly fill the row. It used to measure the full title, so a long
// port name painted through the chip and status and off the card.
func TestPortsRowNameWidthReservesChipAndStatus(t *testing.T) {
	cases := []struct {
		name                  string
		rowW, badgeW, statusW int32
		want                  int32
	}{
		{name: "name only", rowW: 400, want: 368},
		{name: "status", rowW: 400, statusW: 70, want: 298},
		{name: "chip and status", rowW: 400, badgeW: 48, statusW: 70, want: 242},
		{name: "narrow row floors at zero", rowW: 40, badgeW: 48, statusW: 70, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := portsRowNameWidth(tc.rowW, tc.badgeW, tc.statusW)
			if got != tc.want {
				t.Fatalf("portsRowNameWidth(%d, %d, %d) = %d, want %d",
					tc.rowW, tc.badgeW, tc.statusW, got, tc.want)
			}
			if got > 0 {
				fill := got + 32 + tc.statusW
				if tc.badgeW > 0 {
					fill += tc.badgeW + 8
				}
				if fill != tc.rowW {
					t.Fatalf("title + status + chip fill %d px of a %d px row", fill, tc.rowW)
				}
			}
		})
	}
}
