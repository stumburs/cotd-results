package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"cotd-results/internal/model"
	"cotd-results/internal/nadeo"
)

const stateVersion = 1

type StoreData struct {
	Version int                        `json:"version"`
	Cups    map[string]*model.CupState `json:"cups"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data StoreData
}

func LoadStore(path string) (*Store, error) {
	store := &Store{
		path: path,
		data: StoreData{Version: stateVersion, Cups: make(map[string]*model.CupState)},
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(raw, &store.data); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	if store.data.Cups == nil {
		store.data.Cups = make(map[string]*model.CupState)
	}
	return store, nil
}

func cupKey(competitionID int64) string {
	return fmt.Sprintf("%d", competitionID)
}

func (s *Store) Get(competitionID int64) *model.CupState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Cups[cupKey(competitionID)]
}

func (s *Store) Put(state *model.CupState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Cups[cupKey(state.CompetitionID)] = state
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Prune(now time.Time, retentionDays int) {
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.data.Cups {
		if state.Completed && state.UpdatedAt < cutoff {
			delete(s.data.Cups, key)
		}
	}
}

func NewCupState(cup nadeo.Cup) *model.CupState {
	now := time.Now().Unix()
	name := cup.Competition.Name
	if name == "" {
		name = cup.Challenge.Name
	}
	partition := cup.Competition.Partition
	if partition == "" {
		partition = "crossplay"
	}
	return &model.CupState{
		CompetitionID:  cup.Competition.ID,
		CupID:          cup.ID,
		Edition:        cup.Edition,
		Name:           name,
		Partition:      partition,
		StartDate:      cup.StartDate,
		EndDate:        cup.EndDate,
		ChallengeStart: cup.Challenge.StartDate,
		ChallengeEnd:   cup.Challenge.EndDate,
		ChallengeID:    cup.Challenge.ID,
		MatchSize:      64,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// ---------------------------------------------------------------------------
// Display-name cache
// ---------------------------------------------------------------------------

type NameCache struct {
	mu       sync.Mutex
	path     string
	entries  map[string]NameEntry
	readOnly bool
}

type NameEntry struct {
	Name      string `json:"name"`
	FetchedAt int64  `json:"fetchedAt"`
}

// SetReadOnly prevents the cache from being written, which dry runs use.
func (n *NameCache) SetReadOnly(readOnly bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.readOnly = readOnly
}

func LoadNameCache(path string) (*NameCache, error) {
	cache := &NameCache{
		path:    path,
		entries: make(map[string]NameEntry),
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cache, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return cache, nil
	}
	if err := json.Unmarshal(raw, &cache.entries); err != nil {
		return nil, fmt.Errorf("parse name cache %s: %w", path, err)
	}
	return cache, nil
}

// Resolve returns names for ids, using the cache and only asking the OAuth API
// for missing/expired entries. Failures are logged and cached names are used.
func (n *NameCache) Resolve(ctx context.Context, oauth *nadeo.OAuth, ids []string, maxAgeDays int) map[string]string {
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names
	}

	now := time.Now().Unix()
	maxAge := int64(maxAgeDays) * 24 * 60 * 60
	if maxAge <= 0 {
		maxAge = 7 * 24 * 60 * 60
	}

	n.mu.Lock()
	var stale []string
	for _, id := range ids {
		entry, ok := n.entries[id]
		if ok && entry.Name != "" {
			names[id] = entry.Name
		}
		if !ok || entry.Name == "" || now-entry.FetchedAt > maxAge {
			stale = append(stale, id)
		}
	}
	n.mu.Unlock()

	if oauth != nil && len(stale) > 0 {
		sort.Strings(stale)
		fetched, err := oauth.DisplayNames(ctx, stale)
		if err != nil {
			slog.Warn("could not resolve display names", "err", err)
		} else {
			n.mu.Lock()
			for id, name := range fetched {
				if name != "" {
					n.entries[id] = NameEntry{Name: name, FetchedAt: now}
					names[id] = name
				}
			}
			n.mu.Unlock()
			if err := n.Save(); err != nil {
				slog.Warn("could not save name cache", "err", err)
			}
		}
	}

	return names
}

func (n *NameCache) Save() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.readOnly {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(n.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(n.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := n.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, n.path)
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

type HistoryCup struct {
	CompetitionID int64               `json:"competitionId"`
	CupID         int64               `json:"cupId"`
	Edition       int                 `json:"edition"`
	Name          string              `json:"name"`
	Partition     string              `json:"partition"`
	StartDate     string              `json:"startDate"`
	EndDate       string              `json:"endDate"`
	QualifierID   int64               `json:"qualifierChallengeId"`
	QualifierSize int                 `json:"qualifierCardinal"`
	SavedAt       string              `json:"savedAt"`
	Members       []model.MemberState `json:"members"`
}

func SaveHistory(dataDir string, state *model.CupState) (string, error) {
	dir := filepath.Join(dataDir, "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	date := time.Unix(state.StartDate, 0).UTC().Format("2006-01-02")
	filename := fmt.Sprintf("%s_%d.json", date, state.CompetitionID)
	path := filepath.Join(dir, filename)

	history := HistoryCup{
		CompetitionID: state.CompetitionID,
		CupID:         state.CupID,
		Edition:       state.Edition,
		Name:          state.Name,
		Partition:     state.Partition,
		StartDate:     time.Unix(state.StartDate, 0).UTC().Format(time.RFC3339),
		EndDate:       time.Unix(state.EndDate, 0).UTC().Format(time.RFC3339),
		QualifierID:   state.ChallengeID,
		QualifierSize: state.QualifierCardinal,
		SavedAt:       time.Now().UTC().Format(time.RFC3339),
		Members:       state.Members,
	}

	raw, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
