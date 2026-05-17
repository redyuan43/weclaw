package ilink

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextTokenStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	botID := "bot@example"
	userID := "user@im.wechat"
	token := "context-token"

	if err := SaveContextToken(botID, userID, token); err != nil {
		t.Fatalf("SaveContextToken() error = %v", err)
	}

	got, err := LoadContextToken(botID, userID)
	if err != nil {
		t.Fatalf("LoadContextToken() error = %v", err)
	}
	if got != token {
		t.Fatalf("LoadContextToken() = %q, want %q", got, token)
	}

	path := filepath.Join(home, ".weclaw", "accounts", "bot-example.contexts.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("context token file was not written: %v", err)
	}
}

func TestClearContextTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	botID := "bot@example"
	userID := "user@im.wechat"

	if err := SaveContextToken(botID, userID, "context-token"); err != nil {
		t.Fatalf("SaveContextToken() error = %v", err)
	}
	if err := ClearContextTokens(botID); err != nil {
		t.Fatalf("ClearContextTokens() error = %v", err)
	}

	got, err := LoadContextToken(botID, userID)
	if err != nil {
		t.Fatalf("LoadContextToken() error = %v", err)
	}
	if got != "" {
		t.Fatalf("LoadContextToken() = %q, want empty", got)
	}
}

func TestLoadContextTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	botID := "bot@example"
	if err := SaveContextToken(botID, "one@im.wechat", "token-one"); err != nil {
		t.Fatalf("SaveContextToken(one) error = %v", err)
	}
	if err := SaveContextToken(botID, "two@im.wechat", "token-two"); err != nil {
		t.Fatalf("SaveContextToken(two) error = %v", err)
	}

	tokens, err := LoadContextTokens(botID)
	if err != nil {
		t.Fatalf("LoadContextTokens() error = %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("len(tokens) = %d, want 2", len(tokens))
	}
	if tokens["one@im.wechat"] != "token-one" || tokens["two@im.wechat"] != "token-two" {
		t.Fatalf("tokens = %#v", tokens)
	}
}

func TestClearContextTokenOnlyRemovesOneUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	botID := "bot@example"

	if err := SaveContextToken(botID, "one@im.wechat", "token-one"); err != nil {
		t.Fatalf("SaveContextToken(one) error = %v", err)
	}
	if err := SaveContextToken(botID, "two@im.wechat", "token-two"); err != nil {
		t.Fatalf("SaveContextToken(two) error = %v", err)
	}
	if err := ClearContextToken(botID, "one@im.wechat"); err != nil {
		t.Fatalf("ClearContextToken() error = %v", err)
	}

	got, err := LoadContextToken(botID, "one@im.wechat")
	if err != nil {
		t.Fatalf("LoadContextToken(one) error = %v", err)
	}
	if got != "" {
		t.Fatalf("LoadContextToken(one) = %q, want empty", got)
	}

	got, err = LoadContextToken(botID, "two@im.wechat")
	if err != nil {
		t.Fatalf("LoadContextToken(two) error = %v", err)
	}
	if got != "token-two" {
		t.Fatalf("LoadContextToken(two) = %q, want token-two", got)
	}
}
