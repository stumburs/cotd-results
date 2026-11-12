// Package api provides the shared HTTP client used for every Nadeo, OAuth, and
// Discord request. It handles throttling, the custom user agent, and retries
// for rate limits and server errors.
package api

import (
	"bytes"
	"context"
	"encoding/json"
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

type Client struct {
	http        *http.Client
	userAgent   string
	minInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

func NewClient(userAgent string, minInterval time.Duration) *Client {
	return &Client{
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent:   userAgent,
		minInterval: minInterval,
	}
}

// SetTransport replaces the HTTP transport, which lets tests intercept
// requests without a live network.
func (c *Client) SetTransport(transport http.RoundTripper) {
	c.http.Transport = transport
}

func (c *Client) throttle(ctx context.Context) error {
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

func (c *Client) Send(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) (int, []byte, error) {
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
				if err := SleepContext(ctx, time.Duration(attempt+1)*time.Second); err != nil {
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
			delay := RetryAfter(resp.Header.Get("Retry-After"), data)
			if attempt >= 4 {
				return resp.StatusCode, data, fmt.Errorf("rate limited by %s (retry after %s)", rawURL, delay)
			}
			slog.Warn("rate limited, backing off", "url", SafeURL(rawURL), "delay", delay)
			if err := SleepContext(ctx, delay); err != nil {
				return 0, nil, err
			}
			continue

		case resp.StatusCode >= 500:
			if attempt < 4 {
				delay := time.Duration(1<<attempt) * time.Second
				slog.Warn("server error, retrying", "url", SafeURL(rawURL), "status", resp.StatusCode, "delay", delay)
				if err := SleepContext(ctx, delay); err != nil {
					return 0, nil, err
				}
				continue
			}
		}

		return resp.StatusCode, data, nil
	}
}

// SafeURL strips Discord webhook tokens before a URL is written to logs.
func SafeURL(rawURL string) string {
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

func RetryAfter(header string, body []byte) time.Duration {
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

func SleepContext(ctx context.Context, d time.Duration) error {
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

func Truncate(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
