package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"cotd-results/internal/api"
	"cotd-results/internal/config"
	"cotd-results/internal/discord"
	"cotd-results/internal/model"
	"cotd-results/internal/nadeo"
	"cotd-results/internal/render"
	"cotd-results/internal/store"
)

type App struct {
	cfg     *config.Config
	nadeo   *nadeo.Nadeo
	oauth   *nadeo.OAuth
	discord *discord.Discord
	store   *store.Store
	names   *store.NameCache

	clubMu      sync.Mutex
	clubCache   map[string]struct{}
	clubCacheAt time.Time

	renderMu sync.Mutex
	rendered map[int64][]model.OutgoingMessage

	dryRun bool
}

func NewApp(cfg *config.Config, dryRun bool) (*App, error) {
	discordClient, err := discord.NewDiscord(cfg.Discord.WebhookURL, cfg.Discord.Username, dryRun)
	if err != nil {
		return nil, err
	}

	dataDir := cfg.Settings.DataDir
	stateStore, err := store.LoadStore(filepath.Join(dataDir, "state.json"))
	if err != nil {
		return nil, err
	}
	nameCache, err := store.LoadNameCache(filepath.Join(dataDir, "names.json"))
	if err != nil {
		return nil, err
	}
	nameCache.SetReadOnly(dryRun)

	return &App{
		cfg:      cfg,
		nadeo:    nadeo.NewNadeo(cfg.Nadeo.Login, cfg.Nadeo.Password, cfg.Nadeo.UserAgent, time.Duration(cfg.Settings.PageDelayMS)*time.Millisecond),
		oauth:    nadeo.NewOAuth(cfg.OAuth.ClientID, cfg.OAuth.ClientSecret, cfg.Nadeo.UserAgent),
		discord:  discordClient,
		store:    stateStore,
		names:    nameCache,
		rendered: make(map[int64][]model.OutgoingMessage),
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

const TestWebhookDelay = 5 * time.Second

// RunTest sends a qualification-style message, waits a few seconds, then
// edits the same message with simulated final results and exits. It needs no
// Nadeo credentials and does not write state or history.
func (a *App) RunTest(ctx context.Context) error {
	state := randomTestCupState()

	if err := a.syncMessages(ctx, state); err != nil {
		return fmt.Errorf("send test qualification message: %w", err)
	}
	slog.Info("test qualification message sent",
		"members", len(state.Members),
		"waiting", TestWebhookDelay,
	)

	if err := api.SleepContext(ctx, TestWebhookDelay); err != nil {
		return err
	}

	simulateTestFinals(state)
	if err := a.syncMessages(ctx, state); err != nil {
		return fmt.Errorf("send test final message: %w", err)
	}
	slog.Info("test final message sent", "members", len(state.Members))
	return nil
}

func randomTestCupState() *model.CupState {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	now := time.Now()
	matchSize := 64
	count := 18 + rng.Intn(15)

	ranks := rng.Perm(600)[:count]
	names := []string{
		"Alex", "Bea", "Casper", "Dana", "Emil", "Frida", "Gus", "Hana",
		"Iris", "Jonas", "Kira", "Leo", "Maja", "Nils", "Ola", "Pia",
		"Quinn", "Rune", "Sara", "Timo", "Uma", "Viktor", "Wera", "Yuki",
		"Zoe", "Nora", "Finn", "Lina", "Mats", "Elin", "Oscar", "Mila",
	}

	members := make([]model.MemberState, 0, count)
	for i, rank := range ranks {
		qualifierRank := rank + 1
		members = append(members, model.MemberState{
			AccountID:      fmt.Sprintf("test-account-%d", i),
			Name:           names[i%len(names)],
			QualifierRank:  qualifierRank,
			QualifierScore: int64(30000 + rng.Intn(20000)),
			Division:       model.DivisionForRank(qualifierRank, matchSize),
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].Division != members[j].Division {
			return members[i].Division < members[j].Division
		}
		return members[i].QualifierRank < members[j].QualifierRank
	})

	return &model.CupState{
		CompetitionID:      -1,
		Edition:            1,
		Name:               "Test Cup",
		Partition:          "crossplay",
		StartDate:          now.Unix(),
		EndDate:            now.Add(time.Hour).Unix(),
		ChallengeStart:     now.Add(-20 * time.Minute).Unix(),
		ChallengeEnd:       now.Add(-5 * time.Minute).Unix(),
		MatchSize:          matchSize,
		QualifierDone:      true,
		QualifierFetchedAt: now.Unix(),
		QualifierUnit:      "time",
		QualifierCardinal:  1234,
		Members:            members,
		CreatedAt:          now.Unix(),
		UpdatedAt:          now.Unix(),
	}
}

// simulateTestFinals shuffles each division and assigns unique match ranks, so
// the rendered order visibly changes from qualifier order to final order.
func simulateTestFinals(state *model.CupState) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	byDivision := make(map[int][]int)
	for i, member := range state.Members {
		byDivision[member.Division] = append(byDivision[member.Division], i)
	}
	for division, indexes := range byDivision {
		rng.Shuffle(len(indexes), func(i, j int) {
			indexes[i], indexes[j] = indexes[j], indexes[i]
		})
		for rank, index := range indexes {
			member := &state.Members[index]
			member.DivisionFinal = true
			member.MatchRank = rank + 1
			member.OverallRank = (division-1)*state.MatchSize + rank + 1
			member.NoResult = false
		}
	}
	state.Completed = true
	state.UpdatedAt = time.Now().Unix()
}

// HasOAuth reports whether display-name lookups are configured.
func (a *App) HasOAuth() bool { return a.oauth != nil }

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
			state = store.NewCupState(cup)
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

func (a *App) cupWanted(cup nadeo.Cup, now time.Time) bool {
	partition := cup.Competition.Partition
	if partition == "" {
		// Older/odd responses omit the field; COTD's default is crossplay.
		partition = "crossplay"
	}
	if !a.cfg.WantsPartition(partition) {
		return false
	}
	if !a.cfg.WantsEdition(cup.Edition) {
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

func (a *App) processCup(ctx context.Context, cup nadeo.Cup, state *model.CupState) error {
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
func (a *App) finishCup(ctx context.Context, state *model.CupState) error {
	if !state.HistorySaved {
		if a.dryRun {
			state.HistorySaved = true
		} else {
			path, err := store.SaveHistory(a.cfg.Settings.DataDir, state)
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
func (a *App) loadRounds(ctx context.Context, cup nadeo.Cup, state *model.CupState) error {
	rounds, err := a.nadeo.Rounds(ctx, cup.Competition.ID)
	if err != nil {
		return fmt.Errorf("load rounds: %w", err)
	}
	if len(rounds) == 0 {
		return errors.New("competition has no rounds yet")
	}

	var chosen *nadeo.Round
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
	state.MatchSize = nadeo.ParseMatchSize(cup.Competition.SpotStructure)
	return nil
}

func (a *App) loadQualifier(ctx context.Context, state *model.CupState) error {
	memberSet, err := a.clubMemberSet(ctx)
	if err != nil {
		return fmt.Errorf("load club members: %w", err)
	}

	var (
		members  []model.MemberState
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

			members = append(members, model.MemberState{
				AccountID:      result.Player,
				QualifierRank:  result.Rank,
				QualifierScore: result.Score,
				Division:       model.DivisionForRank(result.Rank, state.MatchSize),
			})
		}

		offset += len(page.Results)
		if len(memberSet) == 0 || len(page.Results) == 0 || (cardinal > 0 && offset >= cardinal) {
			break
		}
		if err := api.SleepContext(ctx, time.Duration(a.cfg.Settings.PageDelayMS)*time.Millisecond); err != nil {
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

func (a *App) updateFinals(ctx context.Context, state *model.CupState) error {
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

		byPlayer := make(map[string]nadeo.MatchResult, len(results))
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

func (a *App) allRoundMatches(ctx context.Context, roundID int64) ([]nadeo.Match, error) {
	var (
		all    []nadeo.Match
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
		if err := api.SleepContext(ctx, time.Duration(a.cfg.Settings.PageDelayMS)*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (a *App) allMatchResults(ctx context.Context, matchID int64) ([]nadeo.MatchResult, error) {
	var (
		all    []nadeo.MatchResult
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
		if err := api.SleepContext(ctx, time.Duration(a.cfg.Settings.PageDelayMS)*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (a *App) allFinal(state *model.CupState) bool {
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

func (a *App) finalizeMissing(state *model.CupState) {
	for i := range state.Members {
		if !state.Members[i].DivisionFinal {
			state.Members[i].DivisionFinal = true
			state.Members[i].NoResult = true
		}
	}
}

func (a *App) syncMessages(ctx context.Context, state *model.CupState) error {
	messages := render.BuildMessages(state, a.cfg)

	a.renderMu.Lock()
	previous := a.rendered[state.CompetitionID]
	if slices.Equal(previous, messages) && len(state.Messages) > 0 {
		a.renderMu.Unlock()
		return nil
	}
	a.renderMu.Unlock()

	messageIDs, syncErr := a.discord.Sync(ctx, state.Messages, previous, messages)
	// Persist partial progress too, so a retry does not post duplicates for
	// chunks that already succeeded.
	state.Messages = messageIDs
	state.WebhookID = a.discord.WebhookID()
	if syncErr != nil {
		return fmt.Errorf("sync Discord messages: %w", syncErr)
	}

	a.renderMu.Lock()
	a.rendered[state.CompetitionID] = messages
	a.renderMu.Unlock()
	return nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
