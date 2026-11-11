package main

import "testing"

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

func TestSafeURLRedactsWebhookToken(t *testing.T) {
	got := safeURL("https://discord.com/api/webhooks/123/secret-token?thread_id=9")
	want := "https://discord.com/api/webhooks/123/REDACTED"
	if got != want {
		t.Errorf("safeURL = %q, want %q", got, want)
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
