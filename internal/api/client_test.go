package api

import "testing"

func TestSafeURLRedactsWebhookToken(t *testing.T) {
	got := SafeURL("https://discord.com/api/webhooks/123/secret-token?thread_id=9")
	want := "https://discord.com/api/webhooks/123/REDACTED"
	if got != want {
		t.Errorf("SafeURL = %q, want %q", got, want)
	}
}
