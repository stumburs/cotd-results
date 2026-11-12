package discord

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

	"cotd-results/internal/api"
	"cotd-results/internal/model"
)

// Discord's red, used for the stripe on the left side of every embed.
const discordEmbedStripeColor = 0xFF0000

type Discord struct {
	api        *api.Client
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
		api:        api.NewClient("cotd-results/1.0", 250*time.Millisecond),
		webhookURL: webhookURL,
		username:   username,
		webhookID:  webhookID,
		dryRun:     dryRun,
	}, nil
}

func (d *Discord) WebhookID() string { return d.webhookID }

// SetDryRun toggles printing messages instead of sending them, which tests use
// to keep the real webhook untouched.
func (d *Discord) SetDryRun(dryRun bool) { d.dryRun = dryRun }

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
	// Always send content, even when empty: an edit that omits it would leave
	// the old plain-text content next to the new embed when upgrading from an
	// older version.
	Content         string          `json:"content"`
	Username        string          `json:"username,omitempty"`
	AllowedMentions *allowedMention `json:"allowed_mentions,omitempty"`
	Embeds          []discordEmbed  `json:"embeds"`
}

type discordEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color"`
}

type allowedMention struct {
	Parse []string `json:"parse"`
}

func embedFor(message model.OutgoingMessage) discordEmbed {
	return discordEmbed{
		Title:       message.Title,
		Description: message.Description,
		Color:       discordEmbedStripeColor,
	}
}

func (d *Discord) Post(ctx context.Context, message model.OutgoingMessage) (string, error) {
	if d.dryRun {
		fmt.Printf("\n----- Discord embed -----\n[%s]\n%s\n-------------------------\n", message.Title, message.Description)
		return fmt.Sprintf("dry-%d", time.Now().UnixNano()), nil
	}

	body, _ := json.Marshal(discordPayload{
		Username:        d.username,
		AllowedMentions: &allowedMention{Parse: []string{}},
		Embeds:          []discordEmbed{embedFor(message)},
	})
	target := d.webhookWithQuery(url.Values{"wait": {"true"}})
	status, data, err := d.api.Send(ctx, http.MethodPost, target, body, map[string]string{
		"Content-Type": "application/json",
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("discord post: HTTP %d: %s", status, api.Truncate(data, 300))
	}

	var posted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &posted); err != nil {
		return "", fmt.Errorf("decode discord post: %w", err)
	}
	if posted.ID == "" {
		return "", errors.New("discord post: no message ID returned")
	}
	return posted.ID, nil
}

func (d *Discord) Edit(ctx context.Context, messageID string, message model.OutgoingMessage) error {
	if d.dryRun {
		fmt.Printf("\n----- Discord edit %s -----\n[%s]\n%s\n---------------------------\n", messageID, message.Title, message.Description)
		return nil
	}

	body, _ := json.Marshal(discordPayload{
		AllowedMentions: &allowedMention{Parse: []string{}},
		Embeds:          []discordEmbed{embedFor(message)},
	})
	status, data, err := d.api.Send(ctx, http.MethodPatch, d.messageURL(messageID), body, map[string]string{
		"Content-Type": "application/json",
	})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &discordStatusError{Status: status, Body: api.Truncate(data, 300)}
	}
	return nil
}

func (d *Discord) Delete(ctx context.Context, messageID string) error {
	if d.dryRun {
		slog.Info("dry run: would delete Discord message", "message_id", messageID)
		return nil
	}
	status, data, err := d.api.Send(ctx, http.MethodDelete, d.messageURL(messageID), nil, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("discord delete: HTTP %d: %s", status, api.Truncate(data, 300))
	}
	return nil
}

// Sync makes the webhook's message group match messages. Existing messages
// whose content did not change are left alone, changed chunks are edited in
// place, new chunks are posted, and surplus messages are deleted.
func (d *Discord) Sync(ctx context.Context, existing []string, previous []model.OutgoingMessage, messages []model.OutgoingMessage) ([]string, error) {
	if len(messages) == 0 {
		messages = []model.OutgoingMessage{{Description: "(no results)"}}
	}

	result := make([]string, 0, len(messages))
	for i, message := range messages {
		if i < len(existing) && existing[i] != "" {
			if i < len(previous) && previous[i] == message {
				result = append(result, existing[i])
				continue
			}
			if err := d.Edit(ctx, existing[i], message); err != nil {
				// If the message was deleted manually, forget it and repost.
				var apiErr *discordStatusError
				if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
					slog.Warn("Discord message disappeared, reposting", "message_id", existing[i])
					id, postErr := d.Post(ctx, message)
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

		id, err := d.Post(ctx, message)
		if err != nil {
			if i < len(existing) {
				result = append(result, existing[i:]...)
			}
			return result, err
		}
		result = append(result, id)
	}

	if len(existing) > len(messages) {
		for _, stale := range existing[len(messages):] {
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
