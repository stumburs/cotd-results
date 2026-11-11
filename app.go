package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

type App struct {
	cfg     *Config
	nadeo   *Nadeo
	oauth   *OAuth
	discord *Discord
	store   *Store
	names   *NameCache

	clubMu      sync.Mutex
	clubCache   map[string]struct{}
	clubCacheAt time.Time

	renderMu sync.Mutex
	rendered map[int64][]string

	dryRun bool
}

func NewApp(cfg *Config, dryRun bool) (*App, error) {
	discord, err := NewDiscord(cfg.Discord.WebhookURL, cfg.Discord.Username, dryRun)
	if err != nil {
		return nil, err
	}

	dataDir := cfg.Settings.DataDir
	store, err := LoadStore(filepath.Join(dataDir, "state.json"))
	if err != nil {
		return nil, err
	}
	names, err := LoadNameCache(filepath.Join(dataDir, "names.json"))
	if err != nil {
		return nil, err
	}
	names.readOnly = dryRun

	return &App{
		cfg:      cfg,
		nadeo:    NewNadeo(cfg.Nadeo.Login, cfg.Nadeo.Password, cfg.Nadeo.UserAgent, time.Duration(cfg.Settings.PageDelayMS)*time.Millisecond),
		oauth:    NewOAuth(cfg.OAuth.ClientID, cfg.OAuth.ClientSecret, cfg.Nadeo.UserAgent),
		discord:  discord,
		store:    store,
		names:    names,
		rendered: make(map[int64][]string),
		dryRun:   dryRun,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	if err := a.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("initial poll failed", "err", err)
	}

	ticker := time.NewTicker(time.Duration(a.cfg.Settings.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return a.saveState()
		case <-ticker.C:
			if err := a.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("poll failed", "err", err)
			}
		}
	}
}

func (a *App) RunOnce(ctx context.Context) error {
	if err := a.Tick(ctx); err != nil {
		return err
	}
	return a.saveState()
}

func (a *App) saveState() error {
	if a.dryRun {
		return nil
	}
	return a.store.Save()
}

func (a *App) Tick(ctx context.Context) error {
	now := time.Now()

	cups, err := a.nadeo.CupsOfTheDay(ctx, a.cfg.Cups.Type)
	if err != nil {
		return fmt.Errorf("discover cups: %w", err)
	}

	tracked := 0
	for _, cup := range cups {
		if !a.cupWanted(cup, now) {
			continue
		}
		tracked++

		state := a.store.Get(cup.Competition.ID)
		if state == nil {
			state = NewCupState(cup)
			a.store.Put(state)
			slog.Info("tracking cup",
				"competition", cup.Competition.ID,
				"name", state.Name,
				"edition", cup.Edition,
				"partition", cup.Competition.Partition,
				"start", time.Unix(cup.StartDate, 0).Format(time.RFC3339),
			)
		}
		if state.Finished {
			continue
		}

		if state.WebhookID != "" && state.WebhookID != a.discord.WebhookID() {
			slog.Warn("webhook changed, messages will be reposted", "competition", state.CompetitionID)
			state.Messages = nil
		}
		state.WebhookID = a.discord.WebhookID()

		if err := a.processCup(ctx, cup, state); err != nil {
			slog.Warn("cup processing failed", "competition", state.CompetitionID, "name", state.Name, "err", err)
		}
	}

	if err := a.saveState(); err != nil {
		slog.Warn("could not save state", "err", err)
	}
	a.store.Prune(now, a.cfg.Settings.StateRetentionDays)

	if tracked == 0 {
		slog.Debug("no cups in the active window")
	}
	return nil
}

func (a *App) cupWanted(cup Cup, now time.Time) bool {
	partition := cup.Competition.Partition
	if partition == "" {
		// Older/odd responses omit the field; COTD's default is crossplay.
		partition = "crossplay"
	}
	if !a.cfg.wantsPartition(partition) {
		return false
	}
	if !a.cfg.wantsEdition(cup.Edition) {
		return false
	}

	start := time.Unix(cup.StartDate, 0)
	if now.Before(start.Add(-time.Duration(a.cfg.Cups.LookaheadHours) * time.Hour)) {
		return false
	}
	end := time.Unix(cup.EndDate, 0)
	if now.After(end.Add(time.Duration(a.cfg.Cups.CatchupHours) * time.Hour)) {
		return false
	}
	return true
}

func (a *App) processCup(ctx context.Context, cup Cup, state *CupState) error {
	now := time.Now()
	state.UpdatedAt = now.Unix()

	if state.Completed {
		return a.finishCup(ctx, state)
	}

	if state.ChallengeID == 0 || state.QualifierRoundID == 0 {
		if err := a.loadRounds(ctx, cup, state); err != nil {
			return err
		}
	}

	if !state.QualifierDone {
		qualifierReady := time.Unix(state.ChallengeEnd, 0).
			Add(time.Duration(a.cfg.Cups.QualifierGraceSeconds) * time.Second)
		if now.Before(qualifierReady) {
			return nil
		}

		if state.NoQualifier {
			state.QualifierDone = true
		} else {
			if err := a.loadQualifier(ctx, state); err != nil {
				return err
			}
			state.QualifierDone = true
		}

		slog.Info("qualifier processed",
			"competition", state.CompetitionID,
			"name", state.Name,
			"qualified", len(state.Members),
			"total", state.QualifierCardinal,
		)
		if err := a.syncMessages(ctx, state); err != nil {
			return err
		}
	}

	// Matches are only generated around cup start. Scanning them earlier just
	// produces an empty page. Still retry the qualifier message in case the
	// Discord edit failed on a previous tick.
	if now.Before(time.Unix(state.StartDate, 0).Add(-60 * time.Second)) {
		if err := a.syncMessages(ctx, state); err != nil {
			return err
		}
		return a.saveState()
	}

	if err := a.updateFinals(ctx, state); err != nil {
		slog.Warn("could not update final results", "competition", state.CompetitionID, "err", err)
	}

	if a.allFinal(state) {
		state.Completed = true
	}
	if now.After(time.Unix(state.EndDate, 0).Add(time.Duration(a.cfg.Cups.FinalizeGraceSeconds) * time.Second)) {
		if !a.allFinal(state) {
			slog.Info("finalizing cup with missing match results", "competition", state.CompetitionID)
			a.finalizeMissing(state)
		}
		state.Completed = true
	}

	if state.Completed {
		return a.finishCup(ctx, state)
	}

	if err := a.syncMessages(ctx, state); err != nil {
		return err
	}
	return a.saveState()
}

// finishCup writes history and the final message once, then marks the cup as
// finished. If either step fails it is retried on the next poll.
func (a *App) finishCup(ctx context.Context, state *CupState) error {
	if !state.HistorySaved {
		if a.dryRun {
			state.HistorySaved = true
		} else {
			path, err := SaveHistory(a.cfg.Settings.DataDir, state)
			if err != nil {
				return fmt.Errorf("save cup history: %w", err)
			}
			state.HistorySaved = true
			slog.Info("saved cup history", "competition", state.CompetitionID, "path", path)
		}
	}

	if err := a.syncMessages(ctx, state); err != nil {
		return err
	}
	if !state.Finished {
		state.Finished = true
		slog.Info("cup complete",
			"competition", state.CompetitionID,
			"name", state.Name,
			"club_members", len(state.Members),
		)
	}
	return a.saveState()
}

// loadRounds finds the qualifier challenge and the round whose matches are the
// COTD divisions.
func (a *App) loadRounds(ctx context.Context, cup Cup, state *CupState) error {
	rounds, err := a.nadeo.Rounds(ctx, cup.Competition.ID)
	if err != nil {
		return fmt.Errorf("load rounds: %w", err)
	}
	if len(rounds) == 0 {
		return errors.New("competition has no rounds yet")
	}

	var chosen *Round
	for i := range rounds {
		round := &rounds[i]
		if round.QualifierChallengeID == 0 {
			continue
		}
		if chosen == nil || round.Position < chosen.Position {
			chosen = round
		}
	}
	if chosen == nil {
		slog.Warn("cup has no qualifier challenge; divisions cannot be derived", "competition", state.CompetitionID)
		state.NoQualifier = true
		state.QualifierDone = true
		return nil
	}

	state.ChallengeID = chosen.QualifierChallengeID
	state.QualifierRoundID = chosen.ID
	state.MatchSize = parseMatchSize(cup.Competition.SpotStructure)
	return nil
}

func parseMatchSize(raw string) int {
	if raw == "" {
		return 64
	}
	var structure struct {
		Rounds []struct {
			MatchGeneratorData struct {
				MatchSize int `json:"matchSize"`
			} `json:"matchGeneratorData"`
		} `json:"rounds"`
	}
	if err := json.Unmarshal([]byte(raw), &structure); err != nil {
		return 64
	}
	if len(structure.Rounds) > 0 && structure.Rounds[0].MatchGeneratorData.MatchSize > 0 {
		return structure.Rounds[0].MatchGeneratorData.MatchSize
	}
	return 64
}

func (a *App) loadQualifier(ctx context.Context, state *CupState) error {
	memberSet, err := a.clubMemberSet(ctx)
	if err != nil {
		return fmt.Errorf("load club members: %w", err)
	}

	var (
		members  []MemberState
		seen     = make(map[string]struct{})
		offset   int
		cardinal int
		unit     string
		first    = true
	)

	for {
		page, err := a.nadeo.QualifierLeaderboard(ctx, state.ChallengeID, offset)
		if err != nil {
			return fmt.Errorf("load qualifier leaderboard at offset %d: %w", offset, err)
		}
		if first {
			cardinal = page.Cardinal
			unit = page.ScoreUnit
			first = false
		}
		for _, result := range page.Results {
			if _, ok := memberSet[result.Player]; !ok {
				continue
			}
			if _, ok := seen[result.Player]; ok {
				continue
			}
			seen[result.Player] = struct{}{}

			members = append(members, MemberState{
				AccountID:      result.Player,
				QualifierRank:  result.Rank,
				QualifierScore: result.Score,
				Division:       divisionForRank(result.Rank, state.MatchSize),
			})
		}

		offset += len(page.Results)
		if len(memberSet) == 0 || len(page.Results) == 0 || (cardinal > 0 && offset >= cardinal) {
			break
		}
		if err := sleepContext(ctx, time.Duration(a.cfg.Settings.PageDelayMS)*time.Millisecond); err != nil {
			return err
		}
	}

	if len(members) > 0 {
		ids := make([]string, 0, len(members))
		for _, member := range members {
			ids = append(ids, member.AccountID)
		}
		names := a.names.Resolve(ctx, a.oauth, ids, a.cfg.Settings.NameCacheDays)
		for i := range members {
			members[i].Name = names[members[i].AccountID]
			if members[i].Name == "" {
				members[i].Name = shortID(members[i].AccountID)
			}
		}
	}

	// A qualifier leaderboard can briefly be empty right after the session
	// closes. Retry for a while before accepting an empty result.
	if cardinal == 0 && time.Now().Unix() < state.StartDate+30*60 {
		return errors.New("qualifier leaderboard is not populated yet")
	}

	sort.Slice(members, func(i, j int) bool {
		if members[i].Division != members[j].Division {
			return members[i].Division < members[j].Division
		}
		return members[i].QualifierRank < members[j].QualifierRank
	})

	state.Members = members
	state.QualifierCardinal = cardinal
	state.QualifierUnit = unit
	state.QualifierFetchedAt = time.Now().Unix()
	return nil
}

func divisionForRank(rank, matchSize int) int {
	if rank <= 0 {
		return 0
	}
	if matchSize <= 0 {
		matchSize = 64
	}
	return (rank-1)/matchSize + 1
}

func (a *App) clubMemberSet(ctx context.Context) (map[string]struct{}, error) {
	a.clubMu.Lock()
	defer a.clubMu.Unlock()

	ttl := time.Duration(a.cfg.Settings.ClubCacheHours) * time.Hour
	if a.clubCache != nil && time.Since(a.clubCacheAt) < ttl {
		return a.clubCache, nil
	}

	set := make(map[string]struct{})
	for _, clubID := range a.cfg.Clubs.IDs {
		ids, err := a.nadeo.ClubMembers(ctx, clubID)
		if err != nil {
			return nil, err
		}
		slog.Info("loaded club members", "club", clubID, "count", len(ids))
		for _, id := range ids {
			set[id] = struct{}{}
		}
	}

	a.clubCache = set
	a.clubCacheAt = time.Now()
	return set, nil
}

func (a *App) updateFinals(ctx context.Context, state *CupState) error {
	if len(state.Members) == 0 {
		return nil
	}

	pending := make(map[int]bool)
	for i := range state.Members {
		if !state.Members[i].DivisionFinal {
			pending[state.Members[i].Division] = true
		}
	}
	if len(pending) == 0 {
		return nil
	}

	matches, err := a.allRoundMatches(ctx, state.QualifierRoundID)
	if err != nil {
		return err
	}

	for _, match := range matches {
		division := match.Position + 1
		if !pending[division] || !match.IsCompleted {
			continue
		}

		results, err := a.allMatchResults(ctx, match.ID)
		if err != nil {
			slog.Warn("could not load match results",
				"competition", state.CompetitionID,
				"match", match.ID,
				"division", division,
				"err", err,
			)
			continue
		}
		if len(results) == 0 {
			// The match may be marked complete a moment before its results
			// are readable. Retry on the next poll.
			slog.Warn("match results are empty, retrying",
				"competition", state.CompetitionID,
				"match", match.ID,
				"division", division,
			)
			continue
		}

		byPlayer := make(map[string]MatchResult, len(results))
		for _, result := range results {
			byPlayer[result.Participant] = result
		}

		for i := range state.Members {
			member := &state.Members[i]
			if member.Division != division || member.DivisionFinal {
				continue
			}
			member.MatchID = match.ID
			if result, ok := byPlayer[member.AccountID]; ok {
				member.MatchRank = result.Rank
				member.OverallRank = (division-1)*state.MatchSize + result.Rank
			} else {
				member.NoResult = true
			}
			member.DivisionFinal = true
		}

		delete(pending, division)
		slog.Info("division finished",
			"competition", state.CompetitionID,
			"division", division,
			"match", match.ID,
		)
	}
	return nil
}

func (a *App) allRoundMatches(ctx context.Context, roundID int64) ([]Match, error) {
	var (
		all    []Match
		offset int
	)
	for {
		page, err := a.nadeo.RoundMatches(ctx, roundID, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Matches...)
		if len(page.Matches) < 100 {
			break
		}
		offset += len(page.Matches)
		if err := sleepContext(ctx, time.Duration(a.cfg.Settings.PageDelayMS)*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (a *App) allMatchResults(ctx context.Context, matchID int64) ([]MatchResult, error) {
	var (
		all    []MatchResult
		offset int
	)
	for {
		page, err := a.nadeo.MatchResults(ctx, matchID, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if len(page.Results) < 255 {
			break
		}
		offset += len(page.Results)
		if err := sleepContext(ctx, time.Duration(a.cfg.Settings.PageDelayMS)*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (a *App) allFinal(state *CupState) bool {
	if !state.QualifierDone {
		return false
	}
	for i := range state.Members {
		if !state.Members[i].DivisionFinal {
			return false
		}
	}
	return true
}

func (a *App) finalizeMissing(state *CupState) {
	for i := range state.Members {
		if !state.Members[i].DivisionFinal {
			state.Members[i].DivisionFinal = true
			state.Members[i].NoResult = true
		}
	}
}

func (a *App) syncMessages(ctx context.Context, state *CupState) error {
	chunks := buildMessageChunks(state, a.cfg)

	a.renderMu.Lock()
	previous := a.rendered[state.CompetitionID]
	if slices.Equal(previous, chunks) && len(state.Messages) > 0 {
		a.renderMu.Unlock()
		return nil
	}
	a.renderMu.Unlock()

	messages, syncErr := a.discord.Sync(ctx, state.Messages, previous, chunks)
	// Persist partial progress too, so a retry does not post duplicates for
	// chunks that already succeeded.
	state.Messages = messages
	state.WebhookID = a.discord.WebhookID()
	if syncErr != nil {
		return fmt.Errorf("sync Discord messages: %w", syncErr)
	}

	a.renderMu.Lock()
	a.rendered[state.CompetitionID] = chunks
	a.renderMu.Unlock()
	return nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
