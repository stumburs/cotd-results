package discord

import (
	"testing"

	"cotd-results/internal/model"
)

func TestMessageURLPreservesThreadQuery(t *testing.T) {
	d := &Discord{
		webhookURL: "https://discord.com/api/webhooks/123/token?thread_id=456",
	}
	got := d.messageURL("789")
	want := "https://discord.com/api/webhooks/123/token/messages/789?thread_id=456"
	if got != want {
		t.Errorf("messageURL = %q, want %q", got, want)
	}
}

func TestMessageURLWithWaitQuery(t *testing.T) {
	d := &Discord{
		webhookURL: "https://discord.com/api/webhooks/123/token?wait=true",
	}
	got := d.messageURL("789")
	want := "https://discord.com/api/webhooks/123/token/messages/789?wait=true"
	if got != want {
		t.Errorf("messageURL = %q, want %q", got, want)
	}
}

func TestEmbedForUsesRedStripe(t *testing.T) {
	embed := embedFor(model.OutgoingMessage{Title: "title", Description: "body"})
	if embed.Title != "title" || embed.Description != "body" {
		t.Errorf("embed = %+v", embed)
	}
	if embed.Color != discordEmbedStripeColor {
		t.Errorf("embed color = %#x, want %#x", embed.Color, discordEmbedStripeColor)
	}
}

func TestWebhookID(t *testing.T) {
	d, err := NewDiscord("https://discord.com/api/webhooks/123456/abcdef?thread_id=9", "test", false)
	if err != nil {
		t.Fatal(err)
	}
	if d.WebhookID() != "123456" {
		t.Errorf("webhook ID = %q, want 123456", d.WebhookID())
	}
}
