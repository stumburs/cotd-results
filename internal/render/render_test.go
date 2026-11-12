package render

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"cotd-results/internal/config"
	"cotd-results/internal/model"
)

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
		Title: "**Division 1**",
		Lines: lines,
	}}, embedDescriptionLimit)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > embedDescriptionLimit {
			t.Errorf("chunk %d is %d bytes, limit is %d", i, len(chunk), embedDescriptionLimit)
		}
	}
	joined := strings.Join(chunks, "\n")
	for i := 1; i <= 200; i++ {
		needle := fmt.Sprintf("**Q#%d**", i)
		if strings.Count(joined, needle) != 1 {
			t.Errorf("expected exactly one %q, got %d", needle, strings.Count(joined, needle))
		}
	}
	if !strings.Contains(joined, "Division 1** (cont. 2)") {
		t.Error("expected a continuation heading")
	}
}

func TestBuildMessagesUsesFinalOrder(t *testing.T) {
	cfg := config.Default()
	state := &model.CupState{
		Name:              "COTD 2026-11-11 #1",
		Edition:           1,
		StartDate:         time.Date(2026, 10, 1, 18, 16, 0, 0, time.UTC).Unix(),
		QualifierDone:     true,
		QualifierCardinal: 1000,
		QualifierUnit:     "time",
		Members: []model.MemberState{
			{AccountID: "a", Name: "Alice", QualifierRank: 12, QualifierScore: 38106, Division: 1, DivisionFinal: true, MatchRank: 7, OverallRank: 7},
			{AccountID: "b", Name: "Bob", QualifierRank: 77, QualifierScore: 40000, Division: 2},
			{AccountID: "c", Name: "Charlie", QualifierRank: 20, QualifierScore: 39000, Division: 1, DivisionFinal: true, MatchRank: 2, OverallRank: 2},
		},
	}

	messages := BuildMessages(state, &cfg)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	text := messages[0].Description
	for _, want := range []string{
		"Cup of the Day",
		"3 club members qualified out of 1,000",
		"**Division 1**",
		"**Division 2**",
		"**#2** Charlie",
		"**#7** Alice",
		"**Q#77** Bob — 0:40.000",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered message is missing %q\n%s", want, text)
		}
	}
	if strings.Index(text, "**#2**") > strings.Index(text, "**#7**") {
		t.Errorf("final results are not ordered by position:\n%s", text)
	}
	if strings.Contains(text, "0:38.106") {
		t.Errorf("final results should not repeat qualifier times:\n%s", text)
	}
	if strings.Contains(text, "\"Q#20\"") || strings.Contains(text, "\"Q#12\"") {
		t.Errorf("final results should not repeat qualifier ranks:\n%s", text)
	}
}

func TestBuildMessagesQualifierOrdering(t *testing.T) {
	cfg := config.Default()
	state := &model.CupState{
		Edition:           1,
		StartDate:         time.Date(2026, 10, 1, 18, 16, 0, 0, time.UTC).Unix(),
		QualifierDone:     true,
		QualifierCardinal: 500,
		QualifierUnit:     "time",
		Members: []model.MemberState{
			{Name: "Division 2 racer", QualifierRank: 70, QualifierScore: 40000, Division: 2},
			{Name: "Division 1 racer", QualifierRank: 5, QualifierScore: 38106, Division: 1},
		},
	}

	text := BuildMessages(state, &cfg)[0].Description
	if !strings.Contains(text, "**Division 1**") || !strings.Contains(text, "**Division 2**") {
		t.Errorf("missing division sections:\n%s", text)
	}
	if !strings.Contains(text, "**Q#5** Division 1 racer — 0:38.106") {
		t.Errorf("missing qualifier line:\n%s", text)
	}
	if strings.Index(text, "Division 1 racer") > strings.Index(text, "Division 2 racer") {
		t.Errorf("qualifier view should be ordered by division:\n%s", text)
	}
}

func TestCupTitleWhenFinished(t *testing.T) {
	cfg := config.Default()
	state := &model.CupState{
		Edition:   1,
		StartDate: time.Date(2026, 10, 1, 18, 16, 0, 0, time.UTC).Unix(),
		Completed: true,
	}
	if got, want := BuildMessages(state, &cfg)[0].Title, "2026-10-01 COTD #1 has finished"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

func TestBuildMessagesNoMembers(t *testing.T) {
	cfg := config.Default()
	state := &model.CupState{
		Name:              "COTD 2026-11-11 #2",
		Edition:           2,
		QualifierDone:     true,
		QualifierCardinal: 500,
	}
	text := strings.Join(messageDescriptions(BuildMessages(state, &cfg)), "\n")
	if !strings.Contains(text, "Cup of the Night") {
		t.Errorf("missing edition label: %s", text)
	}
	if !strings.Contains(text, "No club members qualified (out of 500 players)") {
		t.Errorf("missing empty message: %s", text)
	}
}

func messageDescriptions(messages []model.OutgoingMessage) []string {
	descriptions := make([]string, len(messages))
	for i, message := range messages {
		descriptions[i] = message.Description
	}
	return descriptions
}
