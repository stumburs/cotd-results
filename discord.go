package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const discordContentLimit = 1900 // Discord's hard limit is 2000; leave headroom.

type Discord struct {
	api        *apiClient
	webhookURL string
	username   string
	webhookID  string
	dryRun     bool
}

func NewDiscord(webhookURL, username string, dryRun bool) (*Discord, error) {
	u, err := url.Parse(webhookURL)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return nil, errors.New("invalid Discord webhook URL")
	}
	// /api/webhooks/{id}/{token}
	webhookID := parts[len(parts)-2]
	token := parts[len(parts)-1]
	if webhookID == "" || token == "" {
		return nil, errors.New("invalid Discord webhook URL")
	}

	return &Discord{
		api:        newAPIClient("cotd-results/1.0", 250*time.Millisecond),
		webhookURL: webhookURL,
		username:   username,
		webhookID:  webhookID,
		dryRun:     dryRun,
	}, nil
}

func (d *Discord) WebhookID() string { return d.webhookID }

func (d *Discord) webhookWithQuery(extra url.Values) string {
	u, err := url.Parse(d.webhookURL)
	if err != nil {
		return d.webhookURL
	}
	q := u.Query()
	for k, values := range extra {
		for _, v := range values {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (d *Discord) messageURL(messageID string) string {
	u, err := url.Parse(d.webhookURL)
	if err != nil {
		return d.webhookURL + "/messages/" + url.PathEscape(messageID)
	}
	// Append to the path, not the query string, so a ?thread_id=... survives.
	u.Path = strings.TrimRight(u.Path, "/") + "/messages/" + url.PathEscape(messageID)
	return u.String()
}

type discordPayload struct {
	Content         string          `json:"content"`
	Username        string          `json:"username,omitempty"`
	AllowedMentions *allowedMention `json:"allowed_mentions,omitempty"`
}

type allowedMention struct {
	Parse []string `json:"parse"`
}

func (d *Discord) Post(ctx context.Context, content string) (string, error) {
	if d.dryRun {
		fmt.Printf("\n----- Discord message -----\n%s\n---------------------------\n", content)
		return fmt.Sprintf("dry-%d", time.Now().UnixNano()), nil
	}

	body, _ := json.Marshal(discordPayload{
		Content:         content,
		Username:        d.username,
		AllowedMentions: &allowedMention{Parse: []string{}},
	})
	target := d.webhookWithQuery(url.Values{"wait": {"true"}})
	status, data, err := d.api.send(ctx, http.MethodPost, target, body, map[string]string{
		"Content-Type": "application/json",
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("discord post: HTTP %d: %s", status, truncate(data, 300))
	}

	var message struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return "", fmt.Errorf("decode discord post: %w", err)
	}
	if message.ID == "" {
		return "", errors.New("discord post: no message ID returned")
	}
	return message.ID, nil
}

func (d *Discord) Edit(ctx context.Context, messageID, content string) error {
	if d.dryRun {
		fmt.Printf("\n----- Discord edit %s -----\n%s\n---------------------------\n", messageID, content)
		return nil
	}

	body, _ := json.Marshal(discordPayload{
		Content:         content,
		AllowedMentions: &allowedMention{Parse: []string{}},
	})
	status, data, err := d.api.send(ctx, http.MethodPatch, d.messageURL(messageID), body, map[string]string{
		"Content-Type": "application/json",
	})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &discordStatusError{Status: status, Body: truncate(data, 300)}
	}
	return nil
}

func (d *Discord) Delete(ctx context.Context, messageID string) error {
	if d.dryRun {
		slog.Info("dry run: would delete Discord message", "message_id", messageID)
		return nil
	}
	status, data, err := d.api.send(ctx, http.MethodDelete, d.messageURL(messageID), nil, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("discord delete: HTTP %d: %s", status, truncate(data, 300))
	}
	return nil
}

// Sync makes the webhook's message group match chunks. Existing messages whose
// content did not change are left alone, changed chunks are edited in place,
// new chunks are posted, and surplus messages are deleted.
func (d *Discord) Sync(ctx context.Context, existing, previous, chunks []string) ([]string, error) {
	if len(chunks) == 0 {
		chunks = []string{"(no results)"}
	}

	result := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		if i < len(existing) && existing[i] != "" {
			if i < len(previous) && previous[i] == chunk {
				result = append(result, existing[i])
				continue
			}
			if err := d.Edit(ctx, existing[i], chunk); err != nil {
				// If the message was deleted manually, forget it and repost.
				var apiErr *discordStatusError
				if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
					slog.Warn("Discord message disappeared, reposting", "message_id", existing[i])
					id, postErr := d.Post(ctx, chunk)
					if postErr != nil {
						result = append(result, existing[i+1:]...)
						return result, postErr
					}
					result = append(result, id)
					continue
				}
				// Keep the unprocessed IDs so a retry does not repost them.
				result = append(result, existing[i:]...)
				return result, err
			}
			result = append(result, existing[i])
			continue
		}

		id, err := d.Post(ctx, chunk)
		if err != nil {
			if i < len(existing) {
				result = append(result, existing[i:]...)
			}
			return result, err
		}
		result = append(result, id)
	}

	if len(existing) > len(chunks) {
		for _, stale := range existing[len(chunks):] {
			if stale == "" {
				continue
			}
			if err := d.Delete(ctx, stale); err != nil {
				slog.Warn("could not delete stale Discord message", "message_id", stale, "err", err)
			}
		}
	}
	return result, nil
}

// discordStatusError lets Sync detect deleted messages and repost them.
type discordStatusError struct {
	Status int
	Body   string
}

func (e *discordStatusError) Error() string {
	return fmt.Sprintf("discord: HTTP %d: %s", e.Status, e.Body)
}
