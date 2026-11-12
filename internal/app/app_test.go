package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cotd-results/internal/config"
	"cotd-results/internal/model"
	"cotd-results/internal/nadeo"
	"cotd-results/internal/render"
)

type mockNadeo struct {
	mu        sync.Mutex
	finalDone bool
	now       time.Time
}

func (m *mockNadeo) setFinalDone(done bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finalDone = done
}

func (m *mockNadeo) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	finalDone := m.finalDone
	m.mu.Unlock()

	path := req.URL.Path
	switch {
	case path == "/v2/authentication/token/basic":
		return jsonResponse(200, `{"accessToken":"fake","refreshToken":"fake"}`), nil

	case path == "/api/cups-of-the-day":
		start := m.now.Add(-5 * time.Minute).Unix()
		end := m.now.Add(10 * time.Minute).Unix()
		cup := nadeo.Cup{
			ID:      1,
			Edition: 1,
			Competition: nadeo.Competition{
				ID:            46610,
				Name:          "COTD test",
				Partition:     "crossplay",
				SpotStructure: `{"version":1,"rounds":[{"matchGeneratorData":{"matchSize":64}}]}`,
				StartDate:     start,
				EndDate:       end,
			},
			Challenge: nadeo.Challenge{
				ID:        23951,
				Name:      "COTD test - Challenge",
				StartDate: start - 900,
				EndDate:   start - 120,
			},
			StartDate: start,
			EndDate:   end,
		}
		return jsonResponse(200, marshal(nadeo.CupsResponse{COTDs: []nadeo.Cup{cup}})), nil

	case path == "/api/competitions/46610/rounds":
		return jsonResponse(200, `[{"id":9078,"position":0,"name":"Round 1","qualifierChallengeId":23951}]`), nil

	case path == "/api/challenges/23951/leaderboard":
		if req.URL.Query().Get("offset") != "0" {
			return jsonResponse(200, `{"challengeId":23951,"cardinal":3,"scoreUnit":"time","results":[]}`), nil
		}
		return jsonResponse(200, `{
			"challengeId": 23951,
			"cardinal": 3,
			"scoreUnit": "time",
			"results": [
				{"player":"acc-a","rank":1,"score":38106},
				{"player":"acc-b","rank":65,"score":40000},
				{"player":"acc-c","rank":130,"score":41000}
			]
		}`), nil

	case path == "/api/token/club/1/member":
		return jsonResponse(200, `{
			"clubMemberList":[
				{"accountId":"acc-a"},
				{"accountId":"acc-b"},
				{"accountId":"acc-c"}
			],
			"maxPage":1,
			"itemCount":3
		}`), nil

	case path == "/api/rounds/9078/matches":
		return jsonResponse(200, fmt.Sprintf(`{
			"matches":[
				{"id":100,"position":0,"isCompleted":true},
				{"id":101,"position":1,"isCompleted":true},
				{"id":102,"position":2,"isCompleted":%t}
			]
		}`, finalDone)), nil

	case path == "/api/matches/100/results":
		return jsonResponse(200, `{"results":[{"participant":"acc-a","rank":5,"score":100}]}`), nil
	case path == "/api/matches/101/results":
		return jsonResponse(200, `{"results":[{"participant":"acc-b","rank":10,"score":90}]}`), nil
	case path == "/api/matches/102/results":
		return jsonResponse(200, `{"results":[{"participant":"acc-c","rank":2,"score":80}]}`), nil

	default:
		return jsonResponse(404, `{"error":"unexpected request: `+path+`"}`), nil
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func marshal(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func newTestApp(t *testing.T, transport http.RoundTripper) (*App, *config.Config) {
	t.Helper()

	cfg := config.Default()
	cfg.Discord.WebhookURL = "https://discord.com/api/webhooks/123/token"
	cfg.Nadeo.Login = "login"
	cfg.Nadeo.Password = "password"
	cfg.Clubs.IDs = []int64{1}
	cfg.Cups.Editions = []int{1}
	cfg.Cups.QualifierGraceSeconds = 0
	cfg.Settings.DataDir = t.TempDir()
	cfg.Settings.PageDelayMS = 0

	// Keep persistence enabled in the test, but silence the actual webhook.
	app, err := NewApp(&cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	app.discord.SetDryRun(true)
	app.nadeo.SetTransport(transport)
	return app, &cfg
}

func TestCupLifecycle(t *testing.T) {
	mock := &mockNadeo{now: time.Now()}
	app, cfg := newTestApp(t, mock)

	if err := app.Tick(t.Context()); err != nil {
		t.Fatalf("first tick: %v", err)
	}

	state := app.store.Get(46610)
	if state == nil {
		t.Fatal("cup was not tracked")
	}
	if !state.QualifierDone {
		t.Fatal("qualifier was not processed")
	}
	if len(state.Members) != 3 {
		t.Fatalf("got %d members, want 3", len(state.Members))
	}

	wantDivisions := map[string]int{"acc-a": 1, "acc-b": 2, "acc-c": 3}
	for _, member := range state.Members {
		if member.Division != wantDivisions[member.AccountID] {
			t.Errorf("%s division = %d, want %d", member.AccountID, member.Division, wantDivisions[member.AccountID])
		}
	}

	if state.Completed {
		t.Fatal("cup should not be complete while division 3 is racing")
	}
	if len(state.Messages) == 0 {
		t.Fatal("qualifier message was not posted")
	}

	// Finish division 3 and poll again.
	mock.setFinalDone(true)
	if err := app.Tick(t.Context()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	state = app.store.Get(46610)
	if !state.Completed {
		t.Fatal("cup should be complete")
	}
	if !state.Finished {
		t.Fatal("cup should be marked finished")
	}
	if !state.HistorySaved {
		t.Fatal("history should be saved")
	}

	want := map[string]struct {
		matchRank   int
		overallRank int
	}{
		"acc-a": {5, 5},
		"acc-b": {10, 74},
		"acc-c": {2, 130},
	}
	for _, member := range state.Members {
		if !member.DivisionFinal {
			t.Errorf("%s division is not final", member.AccountID)
		}
		if member.MatchRank != want[member.AccountID].matchRank {
			t.Errorf("%s match rank = %d, want %d", member.AccountID, member.MatchRank, want[member.AccountID].matchRank)
		}
		if member.OverallRank != want[member.AccountID].overallRank {
			t.Errorf("%s overall rank = %d, want %d", member.AccountID, member.OverallRank, want[member.AccountID].overallRank)
		}
	}

	historyDir := filepath.Join(cfg.Settings.DataDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d history files, want 1", len(entries))
	}

	text := strings.Join(messageDescriptions(render.BuildMessages(state, cfg)), "\n")
	for _, want := range []string{
		"**Division 1**",
		"**Division 2**",
		"**Division 3**",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("final message missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "**#10**") {
		t.Errorf("final message missing relative Division 2 rank:\n%s", text)
	}
	if strings.Contains(text, "**#130**") {
		t.Errorf("final message should show relative, not absolute, rank:\n%s", text)
	}
	if strings.Contains(text, "\"Q#") {
		t.Errorf("final message should not show qualifier ranks:\n%s", text)
	}
	title := render.BuildMessages(state, cfg)[0].Title
	if !strings.HasSuffix(title, "COTD #1 has finished") {
		t.Errorf("final title = %q, want it to end with %q", title, "COTD #1 has finished")
	}
}

func messageDescriptions(messages []model.OutgoingMessage) []string {
	descriptions := make([]string, len(messages))
	for i, message := range messages {
		descriptions[i] = message.Description
	}
	return descriptions
}

func TestRandomTestCupStateFinishes(t *testing.T) {
	state := randomTestCupState()
	if len(state.Members) == 0 {
		t.Fatal("test cup has no members")
	}
	simulateTestFinals(state)
	if !state.Completed {
		t.Error("test cup should be completed after simulation")
	}

	seen := make(map[int]bool)
	for _, member := range state.Members {
		if !member.DivisionFinal {
			t.Errorf("%s is not final", member.Name)
		}
		if member.OverallRank <= 0 {
			t.Errorf("%s has no overall rank", member.Name)
		}
		if seen[member.OverallRank] {
			t.Errorf("duplicate overall rank %d", member.OverallRank)
		}
		seen[member.OverallRank] = true
	}
}
