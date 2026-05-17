package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestInvalidContextErrorIsSentinel(t *testing.T) {
	err := invalidContextError("send message", "")
	if !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("errors.Is(%v, ErrInvalidContext) = false", err)
	}
}

func TestRetryPendingDeliveriesStopsAfterInvalidContext(t *testing.T) {
	setPendingRetryDelayForTest(t, time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	writeCodexPending(t, "one.json", codexFailedNotification{
		SessionID: "s-1",
		To:        "owner@im.wechat",
		Message:   "one",
	})
	writeCodexPending(t, "two.json", codexFailedNotification{
		SessionID: "s-2",
		To:        "owner@im.wechat",
		Message:   "two",
	})
	writeOutgoingPending(t, "outbox.json", PendingOutgoingSend{
		To:   "owner@im.wechat",
		Text: "outbox",
	})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	codexCalls := 0
	outboxCalls := 0
	origCodexSend := sendCodexPendingTextReply
	origOutboxSend := sendPendingOutgoingText
	t.Cleanup(func() {
		sendCodexPendingTextReply = origCodexSend
		sendPendingOutgoingText = origOutboxSend
	})
	sendCodexPendingTextReply = func(context.Context, *ilink.Client, string, string, string, string) error {
		codexCalls++
		return invalidContextError("send message", "")
	}
	sendPendingOutgoingText = func(context.Context, *ilink.Client, string, string, string, string) error {
		outboxCalls++
		return nil
	}

	RetryPendingDeliveries(context.Background(), client, "owner@im.wechat", "fresh-token")

	if codexCalls != 1 {
		t.Fatalf("codexCalls = %d, want 1", codexCalls)
	}
	if outboxCalls != 0 {
		t.Fatalf("outboxCalls = %d, want 0", outboxCalls)
	}
}

func TestRetryPendingOutgoingRetriesSequentially(t *testing.T) {
	setPendingRetryDelayForTest(t, time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	writeOutgoingPending(t, "one.json", PendingOutgoingSend{To: "owner@im.wechat", MediaPath: "/tmp/one.pdf"})
	writeOutgoingPending(t, "two.json", PendingOutgoingSend{To: "owner@im.wechat", MediaPath: "/tmp/two.pdf"})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	calls := 0
	origSend := sendPendingOutgoingMediaPath
	t.Cleanup(func() {
		sendPendingOutgoingMediaPath = origSend
	})
	sendPendingOutgoingMediaPath = func(context.Context, *ilink.Client, string, string, string) error {
		calls++
		return nil
	}

	result := retryPendingOutgoingSends(context.Background(), client, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone {
		t.Fatalf("result = %v, want pendingRetryDone", result)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	files, err := pendingOutgoingFiles()
	if err != nil {
		t.Fatalf("pendingOutgoingFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("remaining pending files = %d, want 0", len(files))
	}
}

func TestRetryPendingCodexRetriesTextNotificationsOneByOne(t *testing.T) {
	setPendingRetryDelayForTest(t, time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	writeCodexPending(t, "one.json", codexFailedNotification{
		SessionID: "s-1",
		To:        "owner@im.wechat",
		Message:   "one",
	})
	writeCodexPending(t, "two.json", codexFailedNotification{
		SessionID: "s-2",
		To:        "owner@im.wechat",
		Message:   "two",
	})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	calls := 0
	var gotTexts []string
	origSend := sendCodexPendingTextReply
	t.Cleanup(func() {
		sendCodexPendingTextReply = origSend
	})
	sendCodexPendingTextReply = func(_ context.Context, _ *ilink.Client, _ string, text string, _ string, _ string) error {
		calls++
		gotTexts = append(gotTexts, text)
		return nil
	}

	result := retryPendingCodexNotifications(context.Background(), client, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone {
		t.Fatalf("result = %v, want pendingRetryDone", result)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(gotTexts) != 2 || gotTexts[0] != "one" || gotTexts[1] != "two" {
		t.Fatalf("sent texts = %#v, want [one two]", gotTexts)
	}
	files, err := pendingCodexNotificationFiles()
	if err != nil {
		t.Fatalf("pendingCodexNotificationFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("pending codex files = %d, want 0", len(files))
	}
}

func TestRetryPendingOutgoingRetriesTextSendsOneByOne(t *testing.T) {
	setPendingRetryDelayForTest(t, time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	writeOutgoingPending(t, "one.json", PendingOutgoingSend{To: "owner@im.wechat", Text: "one"})
	writeOutgoingPending(t, "two.json", PendingOutgoingSend{To: "owner@im.wechat", Text: "two"})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	calls := 0
	var gotTexts []string
	origSend := sendPendingOutgoingText
	t.Cleanup(func() {
		sendPendingOutgoingText = origSend
	})
	sendPendingOutgoingText = func(_ context.Context, _ *ilink.Client, _ string, text string, _ string, _ string) error {
		calls++
		gotTexts = append(gotTexts, text)
		return nil
	}

	result := retryPendingOutgoingSends(context.Background(), client, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone {
		t.Fatalf("result = %v, want pendingRetryDone", result)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(gotTexts) != 2 || gotTexts[0] != "one" || gotTexts[1] != "two" {
		t.Fatalf("sent texts = %#v, want [one two]", gotTexts)
	}
	files, err := pendingOutgoingFiles()
	if err != nil {
		t.Fatalf("pendingOutgoingFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("pending outgoing files = %d, want 0", len(files))
	}
}

func TestRetryPendingOutgoingMergesWhenQueueGrowsDuringWait(t *testing.T) {
	setPendingRetryDelayForTest(t, 20*time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	writeOutgoingPending(t, "one.json", PendingOutgoingSend{To: "owner@im.wechat", Text: "one"})
	writeOutgoingPending(t, "two.json", PendingOutgoingSend{To: "owner@im.wechat", Text: "two"})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	calls := 0
	var gotTexts []string
	origSend := sendPendingOutgoingText
	t.Cleanup(func() {
		sendPendingOutgoingText = origSend
	})
	sendPendingOutgoingText = func(_ context.Context, _ *ilink.Client, _ string, text string, _ string, _ string) error {
		calls++
		gotTexts = append(gotTexts, text)
		if text == "one" {
			go func() {
				time.Sleep(5 * time.Millisecond)
				path := filepath.Join(os.Getenv("HOME"), ".weclaw", "pending-sends", "zz-three.json")
				raw, _ := json.MarshalIndent(PendingOutgoingSend{To: "owner@im.wechat", Text: "three"}, "", "  ")
				_ = os.WriteFile(path, raw, 0o600)
			}()
		}
		return nil
	}

	result := retryPendingOutgoingSends(context.Background(), client, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone {
		t.Fatalf("result = %v, want pendingRetryDone", result)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(gotTexts) != 2 || gotTexts[0] != "one" || gotTexts[1] != "two\n\n---\n\nthree" {
		t.Fatalf("sent texts = %#v, want one then merged two/three", gotTexts)
	}
	files, err := pendingOutgoingFiles()
	if err != nil {
		t.Fatalf("pendingOutgoingFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("pending outgoing files = %d, want 0", len(files))
	}
}

func setPendingRetryDelayForTest(t *testing.T, delay time.Duration) {
	t.Helper()
	old := pendingRetryDelay
	pendingRetryDelay = delay
	t.Cleanup(func() {
		pendingRetryDelay = old
	})
}

func TestMarkPendingOutgoingAttemptMovesDeadLetter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeOutgoingPending(t, "failed.json", PendingOutgoingSend{
		To:         "owner@im.wechat",
		Text:       "old",
		RetryCount: maxPendingRetryAttempts - 1,
	})

	if dead := markPendingOutgoingAttempt(path, PendingOutgoingSend{
		To:         "owner@im.wechat",
		Text:       "old",
		RetryCount: maxPendingRetryAttempts - 1,
	}, errors.New("still failing")); !dead {
		t.Fatal("markPendingOutgoingAttempt dead = false, want true")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pending file still exists or unexpected error: %v", err)
	}
	deadPath := filepath.Join(os.Getenv("HOME"), ".weclaw", "dead-sends", "failed.json")
	if _, err := os.Stat(deadPath); err != nil {
		t.Fatalf("dead-letter file missing: %v", err)
	}
}

func TestMoveDuplicateCodexNotificationsToSent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := writeCodexPending(t, "first.json", codexFailedNotification{
		SessionID: "same-session",
		To:        "owner@im.wechat",
		Message:   "same",
	})
	duplicate := writeCodexPending(t, "duplicate.json", codexFailedNotification{
		SessionID: "same-session",
		To:        "owner@im.wechat",
		Message:   "same",
	})
	other := writeCodexPending(t, "other.json", codexFailedNotification{
		SessionID: "other-session",
		To:        "owner@im.wechat",
		Message:   "other",
	})

	moveDuplicateCodexNotificationsToSent([]string{first, duplicate, other}, first, codexFailedNotification{
		SessionID: "same-session",
		To:        "owner@im.wechat",
		Message:   "same",
	})

	if _, err := os.Stat(duplicate); !os.IsNotExist(err) {
		t.Fatalf("duplicate still pending or unexpected error: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other pending file changed: %v", err)
	}
	sentPath := filepath.Join(os.Getenv("HOME"), ".codex", "log", "weclaw-notify-sent", "duplicate.json")
	if _, err := os.Stat(sentPath); err != nil {
		t.Fatalf("duplicate not marked sent: %v", err)
	}
}

func writeCodexPending(t *testing.T, name string, item codexFailedNotification) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".codex", "log", "weclaw-notify-failed")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir codex pending: %v", err)
	}
	path := filepath.Join(dir, name)
	writeJSON(t, path, item)
	return path
}

func writeOutgoingPending(t *testing.T, name string, item PendingOutgoingSend) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".weclaw", "pending-sends")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir outgoing pending: %v", err)
	}
	path := filepath.Join(dir, name)
	writeJSON(t, path, item)
	return path
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
