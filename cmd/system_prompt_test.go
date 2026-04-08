package cmd

import (
	"strings"
	"testing"
)

func TestComposeWeclawSystemPrompt_Default(t *testing.T) {
	got := composeWeclawSystemPrompt("")
	if got != weclawPrivacySystemPrompt {
		t.Fatalf("composeWeclawSystemPrompt(\"\") = %q, want exact privacy prompt", got)
	}
}

func TestComposeWeclawSystemPrompt_AppendsCustomPrompt(t *testing.T) {
	got := composeWeclawSystemPrompt("custom behavior")
	if !strings.HasPrefix(got, weclawPrivacySystemPrompt+"\n\n") {
		t.Fatalf("composeWeclawSystemPrompt() should start with privacy prompt, got %q", got)
	}
	if !strings.HasSuffix(got, "custom behavior") {
		t.Fatalf("composeWeclawSystemPrompt() should end with custom prompt, got %q", got)
	}
}
