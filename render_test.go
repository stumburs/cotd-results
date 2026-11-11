package main

import (
	"fmt"
	"strings"
	"testing"
)

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
		if got := divisionForRank(tt.rank, tt.matchSize); got != tt.want {
			t.Errorf("divisionForRank(%d, %d) = %d, want %d", tt.rank, tt.matchSize, got, tt.want)
		}
	}
}

func TestFormatTime(t *testing.T) {
	tests := map[int64]string{
		0:       "0:00.000",
		38106:   "0:38.106",
		60000:   "1:00.000",
		3600000: "1:00:00.000",
	}
	for input, want := range tests {
		if got := formatTime(input); got != want {
			t.Errorf("formatTime(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatInt(t *testing.T) {
	tests := map[int]string{
		0:       "0",
		999:     "999",
		1000:    "1,000",
		1234567: "1,234,567",
	}
	for input, want := range tests {
		if got := formatInt(input); got != want {
			t.Errorf("formatInt(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestChunkSectionsRespectsLimitAndKeepsLines(t *testing.T) {
	header := "**COTD test** — Cup of the Day\n100 club members qualified"
	lines := make([]string, 0, 200)
	for i := 1; i <= 200; i++ {
		lines = append(lines, fmt.Sprintf("**Q#%d** Player-%d — 0:39.%03d", i, i, i))
	}
	chunks := chunkSections(header, []messageSection{{
		Title: "**Division 1** — final",
		Lines: lines,
	}}, discordContentLimit)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > discordContentLimit {
			t.Errorf("chunk %d is %d bytes, limit is %d", i, len(chunk), discordContentLimit)
		}
	}
	joined := strings.Join(chunks, "\n")
	for i := 1; i <= 200; i++ {
		needle := fmt.Sprintf("**Q#%d**", i)
		if strings.Count(joined, needle) != 1 {
			t.Errorf("expected exactly one %q, got %d", needle, strings.Count(joined, needle))
		}
	}
	if !strings.Contains(joined, "Division 1** — final (cont. 2)") {
		t.Error("expected a continuation heading")
	}
}

func TestBuildMessageChunksIncludesQualifierAndFinalRanks(t *testing.T) {
	cfg := defaultConfig()
	state := &CupState{
		Name:              "COTD 2026-11-11 #1",
		Edition:           1,
		QualifierDone:     true,
		QualifierCardinal: 1000,
		QualifierUnit:     "time",
		Members: []MemberState{
			{AccountID: "a", Name: "Alice", QualifierRank: 12, QualifierScore: 38106, Division: 1, DivisionFinal: true, MatchRank: 7, OverallRank: 7},
			{AccountID: "b", Name: "Bob", QualifierRank: 77, QualifierScore: 40000, Division: 2},
		},
	}

	chunks := buildMessageChunks(state, &cfg)
	text := strings.Join(chunks, "\n")
	for _, want := range []string{
		"Cup of the Day",
		"2 club members qualified out of 1,000",
		"**Division 1** — final",
		"**Division 2** — racing",
		"**Q#12** Alice — 0:38.106 → **#7** (overall **#7**)",
		"**Q#77** Bob — 0:40.000",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered message is missing %q\n%s", want, text)
		}
	}
}

func TestBuildMessageChunksNoMembers(t *testing.T) {
	cfg := defaultConfig()
	state := &CupState{
		Name:              "COTD 2026-11-11 #2",
		Edition:           2,
		QualifierDone:     true,
		QualifierCardinal: 500,
	}
	text := strings.Join(buildMessageChunks(state, &cfg), "\n")
	if !strings.Contains(text, "Cup of the Night") {
		t.Errorf("missing edition label: %s", text)
	}
	if !strings.Contains(text, "No club members qualified (out of 500 players)") {
		t.Errorf("missing empty message: %s", text)
	}
}

func TestParseMatchSize(t *testing.T) {
	raw := `{"version":1,"rounds":[{"matchGeneratorData":{"matchSize":64,"matchGeneratorType":"daily_cup"},"matchGeneratorType":"daily_cup"}]}`
	if got := parseMatchSize(raw); got != 64 {
		t.Errorf("parseMatchSize = %d, want 64", got)
	}
	if got := parseMatchSize(""); got != 64 {
		t.Errorf("parseMatchSize(empty) = %d, want 64", got)
	}
	if got := parseMatchSize(`{"rounds":[{"matchGeneratorData":{"matchSize":32}}]}`); got != 32 {
		t.Errorf("parseMatchSize = %d, want 32", got)
	}
}
