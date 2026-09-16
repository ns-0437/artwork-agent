package agent

import "testing"

// Regression test for the deploy bug where a trailing newline survived a
// `grep | cut | gcloud secrets create --data-file=-` pipeline into
// Secret Manager: Go's net/http rejected the resulting Authorization header
// at request time, and the worker's safe-escalate fallback made that look
// identical to "the model chose to escalate" - see infra/gcp/README.md and
// CLAUDE.md for the full incident. NewGroqAdapter must catch this at
// construction instead.
// This is the exact shape of the real incident: a `grep | cut | gcloud
// secrets create --data-file=-` pipeline left a trailing "\n" in the
// secret. TrimSpace must fix this silently - a trailing newline is not the
// unrecoverable case, and rejecting it here would just move the same
// deploy bug from "silent 401-shaped escalation" to "silent nil provider,"
// still with no clear signal of what's actually wrong.
func TestNewGroqAdapter_TrailingNewlineTrimmedAndAccepted(t *testing.T) {
	adapter, err := NewGroqAdapter("gsk_realkey123\n", "some-model")
	if err != nil {
		t.Fatalf("expected a trailing newline to be trimmed and accepted, got error: %v", err)
	}
	if adapter.apiKey != "gsk_realkey123" {
		t.Fatalf("expected trimmed key %q, got %q", "gsk_realkey123", adapter.apiKey)
	}
}

func TestNewGroqAdapter_TrailingCarriageReturnTrimmedAndAccepted(t *testing.T) {
	adapter, err := NewGroqAdapter("gsk_realkey123\r\n", "some-model")
	if err != nil {
		t.Fatalf("expected a trailing CRLF to be trimmed and accepted, got error: %v", err)
	}
	if adapter.apiKey != "gsk_realkey123" {
		t.Fatalf("expected trimmed key %q, got %q", "gsk_realkey123", adapter.apiKey)
	}
}

// An EMBEDDED newline (mid-string, not surrounding whitespace) can't be
// fixed by trimming - this is the one case that must still be rejected,
// since sending it to net/http would hit the same "invalid header field
// value" failure the trailing-newline case used to.
func TestNewGroqAdapter_EmbeddedNewlineRejected(t *testing.T) {
	if _, err := NewGroqAdapter("gsk_real\nkey123", "some-model"); err == nil {
		t.Fatal("expected an error for a key with an embedded newline, got nil")
	}
}

func TestNewGroqAdapter_EmptyKeyRejected(t *testing.T) {
	if _, err := NewGroqAdapter("", "some-model"); err == nil {
		t.Fatal("expected an error for an empty key, got nil")
	}
	if _, err := NewGroqAdapter("   ", "some-model"); err == nil {
		t.Fatal("expected an error for a whitespace-only key, got nil")
	}
}

func TestNewGroqAdapter_SurroundingWhitespaceTrimmed(t *testing.T) {
	adapter, err := NewGroqAdapter("  gsk_realkey123  ", "some-model")
	if err != nil {
		t.Fatalf("expected surrounding spaces/tabs to be trimmed and accepted, got error: %v", err)
	}
	if adapter.apiKey != "gsk_realkey123" {
		t.Fatalf("expected trimmed key %q, got %q", "gsk_realkey123", adapter.apiKey)
	}
}

func TestNewGroqAdapter_ValidKeyAccepted(t *testing.T) {
	adapter, err := NewGroqAdapter("gsk_realkey123", "")
	if err != nil {
		t.Fatalf("expected a valid key to be accepted, got error: %v", err)
	}
	if adapter.model == "" {
		t.Fatal("expected a default model when none is given")
	}
}
