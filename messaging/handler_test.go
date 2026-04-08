package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeAgent struct {
	info           agent.AgentInfo
	lastConvID     string
	lastMessage    string
	reply          string
	err            error
	resetSessionID string
}

func (f *fakeAgent) Chat(_ context.Context, conversationID string, message string) (string, error) {
	f.lastConvID = conversationID
	f.lastMessage = message
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

func (f *fakeAgent) ResetSession(_ context.Context, _ string) (string, error) {
	return f.resetSessionID, nil
}

func (f *fakeAgent) Info() agent.AgentInfo {
	return f.info
}

func (f *fakeAgent) SetCwd(_ string) {}

func newTestHandler() *Handler {
	return &Handler{agents: make(map[string]agent.Agent)}
}

func TestParseCommand_NoPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("hello world")
	if len(names) != 0 {
		t.Errorf("expected nil names, got %v", names)
	}
	if msg != "hello world" {
		t.Errorf("expected full text, got %q", msg)
	}
}

func TestParseCommand_SlashWithAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_AtPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_MultiAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cx hello")
	if len(names) != 2 || names[0] != "claude" || names[1] != "codex" {
		t.Errorf("expected [claude codex], got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_MultiAgentDedup(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cc hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] (deduped), got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_SwitchOnly(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestParseCommand_Alias(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/cc write a function")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from /cc alias, got %v", names)
	}
	if msg != "write a function" {
		t.Errorf("expected 'write a function', got %q", msg)
	}
}

func TestParseCommand_CustomAlias(t *testing.T) {
	h := newTestHandler()
	h.customAliases = map[string]string{"ai": "claude", "c": "claude"}
	names, msg := h.parseCommand("/ai hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from custom alias, got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestResolveAlias(t *testing.T) {
	h := newTestHandler()
	tests := map[string]string{
		"cc":  "claude",
		"cx":  "codex",
		"oc":  "openclaw",
		"cs":  "cursor",
		"km":  "kimi",
		"gm":  "gemini",
		"ocd": "opencode",
	}
	for alias, want := range tests {
		got := h.resolveAlias(alias)
		if got != want {
			t.Errorf("resolveAlias(%q) = %q, want %q", alias, got, want)
		}
	}
	if got := h.resolveAlias("unknown"); got != "unknown" {
		t.Errorf("resolveAlias(unknown) = %q, want %q", got, "unknown")
	}
	h.customAliases = map[string]string{"cc": "custom-claude"}
	if got := h.resolveAlias("cc"); got != "custom-claude" {
		t.Errorf("resolveAlias(cc) with custom = %q, want custom-claude", got)
	}
}

func TestBuildHelpText(t *testing.T) {
	text := buildHelpText()
	if text == "" {
		t.Error("help text is empty")
	}
	if !strings.Contains(text, "/info") {
		t.Error("help text should mention /info")
	}
	if !strings.Contains(text, "/help") {
		t.Error("help text should mention /help")
	}
}

func TestBuildOwnerHelpTextUsesApprovalCode(t *testing.T) {
	text := buildOwnerHelpText()
	if !strings.Contains(text, "/approve 审批码") {
		t.Fatalf("owner help text = %q, want approval code command", text)
	}
	if !strings.Contains(text, "/reject 审批码 原因") {
		t.Fatalf("owner help text = %q, want reject approval code command", text)
	}
	if strings.Contains(text, "授权编号") {
		t.Fatalf("owner help text = %q, should not mention 授权编号", text)
	}
}

func TestChatLocalAgent_UsesConfiguredDefaultAgent(t *testing.T) {
	ag := &fakeAgent{
		info:  agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-5.1-codex-mini"},
		reply: "done",
	}
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgentName("codex")

	result, err := h.ChatLocalAgent(context.Background(), "a2a:conv-1", "hello", "")
	if err != nil {
		t.Fatalf("ChatLocalAgent returned error: %v", err)
	}
	if result.AgentName != "codex" {
		t.Fatalf("AgentName = %q, want codex", result.AgentName)
	}
	if result.Reply != "done" {
		t.Fatalf("Reply = %q, want done", result.Reply)
	}
	if ag.lastConvID != "a2a:conv-1" {
		t.Fatalf("conversationID = %q, want a2a:conv-1", ag.lastConvID)
	}
	if ag.lastMessage != "hello" {
		t.Fatalf("message = %q, want hello", ag.lastMessage)
	}
}

func TestChatLocalAgent_UsesExplicitAgentName(t *testing.T) {
	ag := &fakeAgent{
		info:  agent.AgentInfo{Name: "codex54", Type: "acp", Model: "gpt-5.4"},
		reply: "routed",
	}
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "codex54" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgentName("codex")

	result, err := h.ChatLocalAgent(context.Background(), "a2a:conv-2", "route this", "codex54")
	if err != nil {
		t.Fatalf("ChatLocalAgent returned error: %v", err)
	}
	if result.AgentName != "codex54" {
		t.Fatalf("AgentName = %q, want codex54", result.AgentName)
	}
	if ag.lastConvID != "a2a:conv-2" {
		t.Fatalf("conversationID = %q, want a2a:conv-2", ag.lastConvID)
	}
}
