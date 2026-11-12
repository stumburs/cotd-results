package model

import "testing"

func TestDivisionForRank(t *testing.T) {
	tests := []struct {
		rank      int
		matchSize int
		want      int
	}{
		{rank: 1, matchSize: 64, want: 1},
		{rank: 64, matchSize: 64, want: 1},
		{rank: 65, matchSize: 64, want: 2},
		{rank: 128, matchSize: 64, want: 2},
		{rank: 129, matchSize: 64, want: 3},
		{rank: 0, matchSize: 64, want: 0},
		{rank: 65, matchSize: 0, want: 2},
	}
	for _, tt := range tests {
		if got := DivisionForRank(tt.rank, tt.matchSize); got != tt.want {
			t.Errorf("DivisionForRank(%d, %d) = %d, want %d", tt.rank, tt.matchSize, got, tt.want)
		}
	}
}
