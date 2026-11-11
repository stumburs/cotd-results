package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	coreBase  = "https://prod.trackmania.core.nadeo.online"
	meetBase  = "https://meet.trackmania.nadeo.club"
	liveBase  = "https://live-services.trackmania.nadeo.live"
	oauthBase = "https://api.trackmania.com"
)

var errNoContent = errors.New("no content")

// apiClient handles throttling, the custom user agent, and retries for
// rate limits/server errors. Every Nadeo and OAuth request goes through it.
type apiClient struct {
	http        *http.Client
	userAgent   string
	minInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

func newAPIClient(userAgent string, minInterval time.Duration) *apiClient {
	return &apiClient{
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent:   userAgent,
		minInterval: minInterval,
	}
}

func (c *apiClient) throttle(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	wait := c.minInterval - now.Sub(c.last)
	if wait < 0 {
		wait = 0
		c.last = now
	} else {
		c.last = now.Add(wait)
	}
	c.mu.Unlock()

	if wait == 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *apiClient) send(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) (int, []byte, error) {
	for attempt := 0; ; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return 0, nil, err
		}

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if attempt < 2 {
				if err := sleepContext(ctx, time.Duration(attempt+1)*time.Second); err != nil {
					return 0, nil, err
				}
				continue
			}
			return 0, nil, err
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if readErr != nil {
			return 0, nil, readErr
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			delay := retryAfter(resp.Header.Get("Retry-After"), data)
			if attempt >= 4 {
				return resp.StatusCode, data, fmt.Errorf("rate limited by %s (retry after %s)", rawURL, delay)
			}
			slog.Warn("rate limited, backing off", "url", safeURL(rawURL), "delay", delay)
			if err := sleepContext(ctx, delay); err != nil {
				return 0, nil, err
			}
			continue

		case resp.StatusCode >= 500:
			if attempt < 4 {
				delay := time.Duration(1<<attempt) * time.Second
				slog.Warn("server error, retrying", "url", safeURL(rawURL), "status", resp.StatusCode, "delay", delay)
				if err := sleepContext(ctx, delay); err != nil {
					return 0, nil, err
				}
				continue
			}
		}

		return resp.StatusCode, data, nil
	}
}

// safeURL strips Discord webhook tokens before a URL is written to logs.
func safeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if strings.Contains(u.Path, "/api/webhooks/") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) > 0 {
			parts[len(parts)-1] = "REDACTED"
			u.Path = "/" + strings.Join(parts, "/")
		}
		u.RawQuery = ""
	}
	return u.String()
}

func retryAfter(header string, body []byte) time.Duration {
	if header != "" {
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second))
		}
	}
	// Discord uses a JSON body; Nadeo normally only uses the header.
	var payload struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.RetryAfter > 0 {
		return time.Duration(payload.RetryAfter * float64(time.Second))
	}
	return 2 * time.Second
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Nadeo game API (Meet + Live + Core auth)
// ---------------------------------------------------------------------------

type Nadeo struct {
	api *apiClient

	login    string
	password string

	authMu      sync.Mutex
	token       string
	tokenExpiry time.Time
}

func NewNadeo(login, password, userAgent string, pageDelay time.Duration) *Nadeo {
	return &Nadeo{
		api:      newAPIClient(userAgent, pageDelay),
		login:    login,
		password: password,
	}
}

func (n *Nadeo) tokenLocked(ctx context.Context) (string, error) {
	if n.token != "" && time.Now().Before(n.tokenExpiry) {
		return n.token, nil
	}

	payload, _ := json.Marshal(map[string]string{"audience": "NadeoLiveServices"})
	basic := base64.StdEncoding.EncodeToString([]byte(n.login + ":" + n.password))
	status, body, err := n.api.send(ctx, http.MethodPost, coreBase+"/v2/authentication/token/basic", payload, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Basic " + basic,
	})
	if err != nil {
		return "", fmt.Errorf("authenticate: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("authenticate: HTTP %d: %s", status, truncate(body, 300))
	}

	var tr struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decode auth response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", errors.New("authenticate: empty access token")
	}

	expiry := jwtExpiry(tr.AccessToken)
	if expiry.IsZero() {
		expiry = time.Now().Add(45 * time.Minute)
	}
	n.token = tr.AccessToken
	n.tokenExpiry = expiry.Add(-2 * time.Minute)
	return n.token, nil
}

func (n *Nadeo) invalidateToken() {
	n.authMu.Lock()
	n.token = ""
	n.tokenExpiry = time.Time{}
	n.authMu.Unlock()
}

func (n *Nadeo) getJSON(ctx context.Context, rawURL string, out any) error {
	for authAttempt := 0; authAttempt < 2; authAttempt++ {
		n.authMu.Lock()
		token, err := n.tokenLocked(ctx)
		n.authMu.Unlock()
		if err != nil {
			return err
		}

		status, body, err := n.api.send(ctx, http.MethodGet, rawURL, nil, map[string]string{
			"Authorization": "nadeo_v1 t=" + token,
		})
		if err != nil {
			return err
		}

		if status == http.StatusUnauthorized && authAttempt == 0 {
			n.invalidateToken()
			continue
		}
		if status == http.StatusNoContent {
			return errNoContent
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET %s: HTTP %d: %s", rawURL, status, truncate(body, 300))
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w", rawURL, err)
		}
		return nil
	}
	return fmt.Errorf("GET %s: unauthorized", rawURL)
}

func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func truncate(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ---- Response types -------------------------------------------------------

type CupsResponse struct {
	COTDs []Cup `json:"COTDs"`
}

type Cup struct {
	ID          int64       `json:"id"`
	Edition     int         `json:"edition"`
	Competition Competition `json:"competition"`
	Challenge   Challenge   `json:"challenge"`
	StartDate   int64       `json:"startDate"`
	EndDate     int64       `json:"endDate"`
}

type Competition struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	StartDate     int64  `json:"startDate"`
	EndDate       int64  `json:"endDate"`
	Partition     string `json:"partition"`
	NBPlayers     int    `json:"nbPlayers"`
	SpotStructure string `json:"spotStructure"`
	LeaderboardID int64  `json:"leaderboardId"`
}

type Challenge struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	StartDate     int64  `json:"startDate"`
	EndDate       int64  `json:"endDate"`
	Status        string `json:"status"`
	LeaderboardID int64  `json:"leaderboardId"`
}

type Round struct {
	ID                   int64  `json:"id"`
	Position             int    `json:"position"`
	Name                 string `json:"name"`
	StartDate            int64  `json:"startDate"`
	EndDate              int64  `json:"endDate"`
	NBMatches            int    `json:"nbMatches"`
	QualifierChallengeID int64  `json:"qualifierChallengeId"`
}

type ChallengeLeaderboard struct {
	ChallengeID int64             `json:"challengeId"`
	Cardinal    int               `json:"cardinal"`
	ScoreUnit   string            `json:"scoreUnit"`
	Results     []ChallengeResult `json:"results"`
}

type ChallengeResult struct {
	Points int64  `json:"points"`
	Player string `json:"player"`
	Score  int64  `json:"score"`
	Rank   int    `json:"rank"`
	Zone   string `json:"zone"`
}

type MatchesPage struct {
	Matches []Match `json:"matches"`
}

type Match struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	ClubMatchLiveID string `json:"clubMatchLiveId"`
	Position        int    `json:"position"`
	IsCompleted     bool   `json:"isCompleted"`
}

type MatchResultsPage struct {
	MatchLiveID   string        `json:"matchLiveId"`
	RoundPosition int           `json:"roundPosition"`
	Results       []MatchResult `json:"results"`
	ScoreUnit     string        `json:"scoreUnit"`
}

type MatchResult struct {
	Participant string `json:"participant"`
	Rank        int    `json:"rank"`
	Score       int64  `json:"score"`
	Zone        string `json:"zone"`
}

type ClubMembersPage struct {
	ClubMemberList []ClubMember `json:"clubMemberList"`
	MaxPage        int          `json:"maxPage"`
	ItemCount      int          `json:"itemCount"`
}

type ClubMember struct {
	AccountID string `json:"accountId"`
	ClubID    int64  `json:"clubId"`
	Role      string `json:"role"`
}

// ---- Endpoints ------------------------------------------------------------

func (n *Nadeo) CupsOfTheDay(ctx context.Context, cupType string) ([]Cup, error) {
	rawURL := fmt.Sprintf("%s/api/cups-of-the-day?type=%s&length=100&offset=0",
		meetBase, url.QueryEscape(cupType))
	var response CupsResponse
	if err := n.getJSON(ctx, rawURL, &response); err != nil {
		return nil, err
	}
	return response.COTDs, nil
}

func (n *Nadeo) Rounds(ctx context.Context, competitionID int64) ([]Round, error) {
	var rounds []Round
	rawURL := fmt.Sprintf("%s/api/competitions/%d/rounds", meetBase, competitionID)
	if err := n.getJSON(ctx, rawURL, &rounds); err != nil {
		return nil, err
	}
	return rounds, nil
}

func (n *Nadeo) QualifierLeaderboard(ctx context.Context, challengeID int64, offset int) (*ChallengeLeaderboard, error) {
	var page ChallengeLeaderboard
	rawURL := fmt.Sprintf("%s/api/challenges/%d/leaderboard?length=100&offset=%d", meetBase, challengeID, offset)
	if err := n.getJSON(ctx, rawURL, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (n *Nadeo) RoundMatches(ctx context.Context, roundID int64, offset int) (*MatchesPage, error) {
	var page MatchesPage
	rawURL := fmt.Sprintf("%s/api/rounds/%d/matches?length=100&offset=%d", meetBase, roundID, offset)
	if err := n.getJSON(ctx, rawURL, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (n *Nadeo) MatchResults(ctx context.Context, matchID int64, offset int) (*MatchResultsPage, error) {
	var page MatchResultsPage
	rawURL := fmt.Sprintf("%s/api/matches/%d/results?length=255&offset=%d", meetBase, matchID, offset)
	if err := n.getJSON(ctx, rawURL, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (n *Nadeo) ClubMembers(ctx context.Context, clubID int64) ([]string, error) {
	var (
		ids    []string
		offset int
	)
	for {
		var page ClubMembersPage
		rawURL := fmt.Sprintf("%s/api/token/club/%d/member?length=250&offset=%d", liveBase, clubID, offset)
		if err := n.getJSON(ctx, rawURL, &page); err != nil {
			return nil, fmt.Errorf("club %d members: %w", clubID, err)
		}
		for _, member := range page.ClubMemberList {
			if member.AccountID != "" {
				ids = append(ids, member.AccountID)
			}
		}
		offset += len(page.ClubMemberList)
		if len(page.ClubMemberList) < 250 || (page.ItemCount > 0 && offset >= page.ItemCount) {
			break
		}
		if err := sleepContext(ctx, 150*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// OAuth API (display names)
// ---------------------------------------------------------------------------

type OAuth struct {
	api          *apiClient
	clientID     string
	clientSecret string

	authMu      sync.Mutex
	token       string
	tokenExpiry time.Time
}

func NewOAuth(clientID, clientSecret, userAgent string) *OAuth {
	if clientID == "" || clientSecret == "" {
		return nil
	}
	return &OAuth{
		api:          newAPIClient(userAgent, 150*time.Millisecond),
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

func (o *OAuth) tokenLocked(ctx context.Context) (string, error) {
	if o.token != "" && time.Now().Before(o.tokenExpiry) {
		return o.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {o.clientID},
		"client_secret": {o.clientSecret},
	}
	status, body, err := o.api.send(ctx, http.MethodPost, oauthBase+"/api/access_token", []byte(form.Encode()), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("oauth token: HTTP %d: %s", status, truncate(body, 300))
	}

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("oauth token: empty access token")
	}
	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	o.token = tr.AccessToken
	o.tokenExpiry = time.Now().Add(time.Duration(expiresIn)*time.Second - 2*time.Minute)
	return o.token, nil
}

func (o *OAuth) DisplayNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += 50 {
		end := start + 50
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		// The API documents the literal `accountId[]=...` form, so keep the
		// brackets unescaped and only escape the UUIDs.
		query := make([]string, 0, len(chunk))
		for _, id := range chunk {
			query = append(query, "accountId[]="+url.QueryEscape(id))
		}
		rawURL := oauthBase + "/api/display-names?" + strings.Join(query, "&")

		var page map[string]string
		for authAttempt := 0; authAttempt < 2; authAttempt++ {
			o.authMu.Lock()
			token, err := o.tokenLocked(ctx)
			o.authMu.Unlock()
			if err != nil {
				return names, err
			}
			status, body, err := o.api.send(ctx, http.MethodGet, rawURL, nil, map[string]string{
				"Authorization": "Bearer " + token,
			})
			if err != nil {
				return names, err
			}
			if status == http.StatusUnauthorized && authAttempt == 0 {
				o.authMu.Lock()
				o.token = ""
				o.tokenExpiry = time.Time{}
				o.authMu.Unlock()
				continue
			}
			if status != http.StatusOK {
				return names, fmt.Errorf("display names: HTTP %d: %s", status, truncate(body, 300))
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return names, err
			}
			break
		}

		for id, name := range page {
			names[id] = name
		}
		if end < len(ids) {
			if err := sleepContext(ctx, 150*time.Millisecond); err != nil {
				return names, err
			}
		}
	}
	return names, nil
}
