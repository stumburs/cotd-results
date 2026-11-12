package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the on-disk configuration. See config.example.toml for the
// documented version of every field.
type Config struct {
	Discord  DiscordConfig  `toml:"discord"`
	Nadeo    NadeoConfig    `toml:"nadeo"`
	OAuth    OAuthConfig    `toml:"oauth"`
	Clubs    ClubsConfig    `toml:"clubs"`
	Cups     CupsConfig     `toml:"cups"`
	Settings SettingsConfig `toml:"settings"`
}

type DiscordConfig struct {
	WebhookURL string `toml:"webhook_url"`
	Username   string `toml:"username"`
}

type NadeoConfig struct {
	// Login/password of a Trackmania service account (or dedicated server
	// account). Create one at https://www.trackmania.com/player/service-account
	Login     string `toml:"login"`
	Password  string `toml:"password"`
	UserAgent string `toml:"user_agent"`
}

type OAuthConfig struct {
	// Identifiers of an app created at https://api.trackmania.com. Used only
	// for account ID -> display name lookups.
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

type ClubsConfig struct {
	IDs []int64 `toml:"ids"`
}

type CupsConfig struct {
	Type string `toml:"type"` // cotd, cotw, grand_race

	// Which partitions/editions to follow. Empty means "everything the API
	// returns". The default is the three crossplay cups.
	Partitions []string `toml:"partitions"`
	Editions   []int    `toml:"editions"`

	// Friendly names shown in Discord, keyed by edition number as a string.
	EditionLabels map[string]string `toml:"edition_labels"`

	// How long to wait after the qualifier closes before reading its
	// leaderboard, and after the cup's scheduled end before giving up on
	// missing match results.
	QualifierGraceSeconds int `toml:"qualifier_grace_seconds"`
	FinalizeGraceSeconds  int `toml:"finalize_grace_seconds"`

	// How far ahead to discover cups, and how far back to recover a cup that
	// ended while the bot was stopped.
	LookaheadHours int `toml:"lookahead_hours"`
	CatchupHours   int `toml:"catchup_hours"`
}

type SettingsConfig struct {
	PollIntervalSeconds int    `toml:"poll_interval_seconds"`
	PageDelayMS         int    `toml:"page_delay_ms"`
	DataDir             string `toml:"data_dir"`
	StateRetentionDays  int    `toml:"state_retention_days"`
	ClubCacheHours      int    `toml:"club_cache_hours"`
	NameCacheDays       int    `toml:"name_cache_days"`
}

func Default() Config {
	return Config{
		Discord: DiscordConfig{
			Username: "COTD Results",
		},
		Nadeo: NadeoConfig{
			UserAgent: "cotd-results/1.0 (+https://github.com/your-name/cotd-results)",
		},
		Cups: CupsConfig{
			Type:                  "cotd",
			Partitions:            []string{"crossplay"},
			Editions:              []int{1, 2, 3},
			EditionLabels:         cloneLabels(defaultEditionLabels),
			QualifierGraceSeconds: 20,
			FinalizeGraceSeconds:  120,
			LookaheadHours:        2,
			CatchupHours:          2,
		},
		Settings: SettingsConfig{
			PollIntervalSeconds: 60,
			PageDelayMS:         150,
			DataDir:             "data",
			StateRetentionDays:  7,
			ClubCacheHours:      6,
			NameCacheDays:       7,
		},
	}
}

var defaultEditionLabels = map[string]string{
	"1": "Cup of the Day",
	"2": "Cup of the Night",
	"3": "Cup of the Morning",
}

func cloneLabels(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func LoadConfig(path string) (*Config, error) {
	return loadConfig(path, false)
}

// LoadConfigForTest only requires the Discord webhook. The test-message mode
// does not touch the Nadeo API, so credentials and club IDs are optional.
func LoadConfigForTest(path string) (*Config, error) {
	return loadConfig(path, true)
}

func loadConfig(path string, testOnly bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := Default()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if cfg.Cups.EditionLabels == nil {
		cfg.Cups.EditionLabels = map[string]string{}
	}
	for edition, label := range defaultEditionLabels {
		if _, ok := cfg.Cups.EditionLabels[edition]; !ok {
			cfg.Cups.EditionLabels[edition] = label
		}
	}

	if testOnly {
		if err := cfg.validateTest(); err != nil {
			return nil, err
		}
	} else if err := cfg.validate(); err != nil {
		return nil, err
	}

	// Relative data paths are resolved next to config.toml, so running the
	// binary from a different working directory (e.g. Task Scheduler) still
	// finds the same state.
	if !filepath.IsAbs(cfg.Settings.DataDir) {
		if absolute, err := filepath.Abs(path); err == nil {
			cfg.Settings.DataDir = filepath.Join(filepath.Dir(absolute), cfg.Settings.DataDir)
		}
	}

	return &cfg, nil
}

func (c *Config) validateTest() error {
	if problem := c.webhookProblem(); problem != "" {
		return errors.New("invalid config:\n  - " + problem)
	}
	return nil
}

func (c *Config) webhookProblem() string {
	if strings.TrimSpace(c.Discord.WebhookURL) == "" {
		return "discord.webhook_url is required"
	}
	if u, err := url.Parse(c.Discord.WebhookURL); err != nil || u.Scheme != "https" || !strings.Contains(u.Path, "/api/webhooks/") {
		return "discord.webhook_url must be a Discord webhook URL"
	}
	return ""
}

func (c *Config) validate() error {
	var problems []string

	if problem := c.webhookProblem(); problem != "" {
		problems = append(problems, problem)
	}

	if strings.TrimSpace(c.Nadeo.Login) == "" || c.Nadeo.Password == "" {
		problems = append(problems, "nadeo.login and nadeo.password are required")
	}
	if strings.TrimSpace(c.Nadeo.UserAgent) == "" {
		problems = append(problems, "nadeo.user_agent is required (Nadeo asks for project/contact info)")
	}

	if len(c.Clubs.IDs) == 0 {
		problems = append(problems, "clubs.ids must contain at least one club ID")
	}
	for _, id := range c.Clubs.IDs {
		if id <= 0 {
			problems = append(problems, "clubs.ids must only contain positive club IDs")
			break
		}
	}

	if c.Cups.Type == "" {
		problems = append(problems, "cups.type must not be empty")
	}
	if c.Settings.PollIntervalSeconds < 15 {
		problems = append(problems, "settings.poll_interval_seconds must be at least 15")
	}
	if c.Settings.PageDelayMS < 0 {
		problems = append(problems, "settings.page_delay_ms must not be negative")
	}
	if c.Cups.LookaheadHours < 0 {
		problems = append(problems, "cups.lookahead_hours must not be negative")
	}
	if c.Cups.CatchupHours < 0 {
		problems = append(problems, "cups.catchup_hours must not be negative")
	}
	if c.Cups.QualifierGraceSeconds < 0 {
		problems = append(problems, "cups.qualifier_grace_seconds must not be negative")
	}
	if c.Cups.FinalizeGraceSeconds < 0 {
		problems = append(problems, "cups.finalize_grace_seconds must not be negative")
	}
	if c.Settings.ClubCacheHours < 0 {
		problems = append(problems, "settings.club_cache_hours must not be negative")
	}
	if c.Settings.NameCacheDays < 0 {
		problems = append(problems, "settings.name_cache_days must not be negative")
	}
	if c.Settings.DataDir == "" {
		problems = append(problems, "settings.data_dir must not be empty")
	}
	if c.Settings.StateRetentionDays < 1 {
		problems = append(problems, "settings.state_retention_days must be at least 1")
	}

	if len(problems) > 0 {
		return errors.New("invalid config:\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil
}

func (c *Config) EditionLabel(edition int) string {
	if label := c.Cups.EditionLabels[fmt.Sprintf("%d", edition)]; label != "" {
		return label
	}
	return fmt.Sprintf("Cup #%d", edition)
}

func (c *Config) WantsPartition(partition string) bool {
	if len(c.Cups.Partitions) == 0 {
		return true
	}
	for _, want := range c.Cups.Partitions {
		if strings.EqualFold(want, partition) {
			return true
		}
	}
	return false
}

func (c *Config) WantsEdition(edition int) bool {
	if len(c.Cups.Editions) == 0 {
		return true
	}
	for _, want := range c.Cups.Editions {
		if want == edition {
			return true
		}
	}
	return false
}
