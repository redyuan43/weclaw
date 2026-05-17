package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
)

type fakeAgent struct {
	info           agent.AgentInfo
	lastConvID     string
	lastMessage    string
	reply          string
	err            error
	resetSessionID string
	wait           <-chan struct{}
}

func (f *fakeAgent) Chat(ctx context.Context, conversationID string, message string) (string, error) {
	f.lastConvID = conversationID
	f.lastMessage = message
	if f.wait != nil {
		select {
		case <-f.wait:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

func (f *fakeAgent) ResetSession(_ context.Context, _ string) (string, error) {
	return f.resetSessionID, nil
}

func (f *fakeAgent) UseSession(_ context.Context, _ string, _ string) error {
	return nil
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
	if !strings.Contains(text, "/plan") || !strings.Contains(text, "/exec") {
		t.Error("help text should mention /plan and /exec")
	}
}

func TestPlanAndExecAreBuiltInCommands(t *testing.T) {
	if !isBuiltInCommand("/plan") {
		t.Fatal("/plan should be a built-in command")
	}
	if !isBuiltInCommand("/exec") {
		t.Fatal("/exec should be a built-in command")
	}
	if !isBuiltInCommand("/execute") {
		t.Fatal("/execute should be a built-in command")
	}
}

func TestMessageForUserModeWrapsPlanMode(t *testing.T) {
	h := newTestHandler()
	h.setUserMode("user-1", "plan")

	got := h.messageForUserMode("user-1", "请修改文件")
	if !strings.Contains(got, "当前微信会话处于 Plan Mode") {
		t.Fatalf("plan prompt missing mode header: %q", got)
	}
	if !strings.Contains(got, "不要修改文件") {
		t.Fatalf("plan prompt missing mutation guard: %q", got)
	}
	if !strings.Contains(got, "请修改文件") {
		t.Fatalf("plan prompt missing original message: %q", got)
	}

	h.clearUserMode("user-1")
	if got := h.messageForUserMode("user-1", "请修改文件"); got != "请修改文件" {
		t.Fatalf("exec mode message = %q, want original", got)
	}
}

func TestMessageForUserModeWrapsOwnerConversation(t *testing.T) {
	h := newTestHandler()
	h.setUserMode("user-1", "plan")

	got := h.messageForUserMode("owner:account:user-1", "继续执行")
	if !strings.Contains(got, "Plan Mode") {
		t.Fatalf("owner conversation message = %q, want plan wrapper", got)
	}
	if !strings.Contains(got, "继续执行") {
		t.Fatalf("owner conversation message = %q, want original text", got)
	}
}

func TestChatWithAgentAppliesPlanMode(t *testing.T) {
	ag := &fakeAgent{
		info:  agent.AgentInfo{Name: "codex", Type: "acp", Model: "test-model"},
		reply: "planned",
	}
	h := newTestHandler()
	h.setUserMode("user-1", "plan")

	reply, err := h.chatWithAgent(context.Background(), ag, "user-1", "实现这个功能")
	if err != nil {
		t.Fatalf("chatWithAgent returned error: %v", err)
	}
	if reply != "planned" {
		t.Fatalf("reply = %q, want planned", reply)
	}
	if !strings.Contains(ag.lastMessage, "Plan Mode") {
		t.Fatalf("lastMessage = %q, want plan wrapper", ag.lastMessage)
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

func TestHandlerClearsMemoryContextOnInvalidContext(t *testing.T) {
	h := NewHandler(nil, nil)
	h.contextTokens.Store("owner@im.wechat", "stale-token")

	h.noteSendError(nil, "owner@im.wechat", ErrInvalidContext)

	if got := h.ContextTokenForUser("owner@im.wechat"); got != "" {
		t.Fatalf("ContextTokenForUser() = %q, want empty", got)
	}
}

func TestSendAgentReplyWithBackgroundSendsFinalBeforeForegroundTimeout(t *testing.T) {
	h := NewHandler(nil, nil)
	h.localTimeout = 50 * time.Millisecond
	h.slowReplyDelay = time.Hour
	h.backgroundTaskTimeout = time.Second

	ag := &fakeAgent{
		info:  agent.AgentInfo{Name: "codex", Type: "acp"},
		reply: "done",
	}
	var sent []string
	restore := stubHandlerTextSender(t, func(_ context.Context, _ *ilink.Client, _ string, text string, _ string, _ string) error {
		sent = append(sent, text)
		return nil
	})
	defer restore()

	h.sendAgentReplyWithBackground(context.Background(), nil, testWeixinMessage(), "codex", ag, "work", "client-1", "")

	if len(sent) != 1 || sent[0] != "done" {
		t.Fatalf("sent = %#v, want only final reply", sent)
	}
}

func TestSendAgentReplyWithBackgroundContinuesAfterForegroundTimeout(t *testing.T) {
	h := NewHandler(nil, nil)
	h.localTimeout = 10 * time.Millisecond
	h.slowReplyDelay = time.Hour
	h.backgroundTaskTimeout = time.Second

	release := make(chan struct{})
	ag := &fakeAgent{
		info:  agent.AgentInfo{Name: "codex", Type: "acp"},
		reply: "done later",
		wait:  release,
	}
	sentCh := make(chan string, 4)
	restore := stubHandlerTextSender(t, func(_ context.Context, _ *ilink.Client, _ string, text string, _ string, _ string) error {
		sentCh <- text
		return nil
	})
	defer restore()

	h.sendAgentReplyWithBackground(context.Background(), nil, testWeixinMessage(), "codex", ag, "work", "client-1", "")

	first := <-sentCh
	if first != "任务仍在后台执行，完成后会自动发送结果。" {
		t.Fatalf("first sent = %q, want background notice", first)
	}
	close(release)

	select {
	case got := <-sentCh:
		if got != "done later" {
			t.Fatalf("background final = %q, want done later", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background final reply")
	}
}

func TestSendTextReplyWithPendingQueuesInvalidContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler(nil, nil)
	restore := stubHandlerTextSender(t, func(context.Context, *ilink.Client, string, string, string, string) error {
		return ErrInvalidContext
	})
	defer restore()

	h.sendTextReplyWithPending(context.Background(), nil, "owner@im.wechat", "final", "stale", "client-1")

	files, err := pendingOutgoingFiles()
	if err != nil {
		t.Fatalf("pendingOutgoingFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("pending files = %d, want 1", len(files))
	}
	item, err := readPendingOutgoing(files[0])
	if err != nil {
		t.Fatalf("readPendingOutgoing: %v", err)
	}
	if item.To != "owner@im.wechat" || item.Text != "final" {
		t.Fatalf("queued item = %#v, want text final to owner", item)
	}
}

func TestSendTextReplyWithPendingSkipsPermanentErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler(nil, nil)
	restore := stubHandlerTextSender(t, func(context.Context, *ilink.Client, string, string, string, string) error {
		return errors.New("permission denied")
	})
	defer restore()

	h.sendTextReplyWithPending(context.Background(), nil, "owner@im.wechat", "final", "token", "client-1")

	files, err := pendingOutgoingFiles()
	if err != nil {
		t.Fatalf("pendingOutgoingFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("pending files = %d, want 0", len(files))
	}
}

func testWeixinMessage() ilink.WeixinMessage {
	return ilink.WeixinMessage{
		FromUserID:   "owner@im.wechat",
		ToUserID:     "bot@im.bot",
		ContextToken: "fresh-token",
	}
}

func stubHandlerTextSender(t *testing.T, fn func(context.Context, *ilink.Client, string, string, string, string) error) func() {
	t.Helper()
	origText := handlerSendTextReply
	origURL := handlerSendMediaFromURL
	origPath := handlerSendMediaFromPath
	handlerSendTextReply = fn
	handlerSendMediaFromURL = func(context.Context, *ilink.Client, string, string, string) error {
		return nil
	}
	handlerSendMediaFromPath = func(context.Context, *ilink.Client, string, string, string) error {
		return nil
	}
	return func() {
		handlerSendTextReply = origText
		handlerSendMediaFromURL = origURL
		handlerSendMediaFromPath = origPath
	}
}
