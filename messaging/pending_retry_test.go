package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestRetryPendingCodexReleasesStaleNotificationsAsSummary(t *testing.T) {
	setPendingRetryDelayForTest(t, time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	staleAt := time.Now().Add(-16 * time.Minute).Format(time.RFC3339)
	writeCodexPending(t, "old-one.json", codexFailedNotification{
		FailedAt:  staleAt,
		SessionID: "session-one",
		CWD:       "/work/one",
		To:        "owner@im.wechat",
		Message:   codexStopMessage("device-a", "/work/one", "第一个任务已经完成。\n\n更多细节"),
	})
	writeCodexPending(t, "old-two.json", codexFailedNotification{
		FailedAt:  staleAt,
		SessionID: "session-two",
		CWD:       "/work/two",
		To:        "owner@im.wechat",
		Message:   codexStopMessage("device-b", "/work/two", "第二个任务已经完成。"),
	})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	var gotTexts []string
	origSend := sendCodexPendingTextReply
	t.Cleanup(func() {
		sendCodexPendingTextReply = origSend
	})
	sendCodexPendingTextReply = func(_ context.Context, _ *ilink.Client, _ string, text string, _ string, _ string) error {
		gotTexts = append(gotTexts, text)
		return nil
	}

	result := retryPendingCodexNotifications(context.Background(), client, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone {
		t.Fatalf("result = %v, want pendingRetryDone", result)
	}
	if len(gotTexts) != 1 {
		t.Fatalf("sent texts = %#v, want one summary", gotTexts)
	}
	if !strings.Contains(gotTexts[0], "释放 2 条任务") ||
		!strings.Contains(gotTexts[0], "设备: device-a | 文件夹: /work/one | 在线ID: session-one | 任务: 第一个任务已经完成。") ||
		!strings.Contains(gotTexts[0], "设备: device-b | 文件夹: /work/two | 在线ID: session-two | 任务: 第二个任务已经完成。") {
		t.Fatalf("summary text missing expected lines:\n%s", gotTexts[0])
	}
	files, err := pendingCodexNotificationFiles()
	if err != nil {
		t.Fatalf("pendingCodexNotificationFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("pending codex files = %d, want 0", len(files))
	}
	released := codexReleasedFiles(t)
	if len(released) != 2 {
		t.Fatalf("released files = %d, want 2", len(released))
	}
}

func TestRetryPendingCodexKeepsFreshNotificationsAfterStaleSummary(t *testing.T) {
	setPendingRetryDelayForTest(t, time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	writeCodexPending(t, "old.json", codexFailedNotification{
		FailedAt:  time.Now().Add(-16 * time.Minute).Format(time.RFC3339),
		SessionID: "old-session",
		CWD:       "/work/old",
		To:        "owner@im.wechat",
		Message:   codexStopMessage("device-old", "/work/old", "旧任务完成。"),
	})
	writeCodexPending(t, "fresh.json", codexFailedNotification{
		FailedAt:  time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
		SessionID: "fresh-session",
		CWD:       "/work/fresh",
		To:        "owner@im.wechat",
		Message:   "fresh message",
	})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	var gotTexts []string
	origSend := sendCodexPendingTextReply
	t.Cleanup(func() {
		sendCodexPendingTextReply = origSend
	})
	sendCodexPendingTextReply = func(_ context.Context, _ *ilink.Client, _ string, text string, _ string, _ string) error {
		gotTexts = append(gotTexts, text)
		return nil
	}

	result := retryPendingCodexNotifications(context.Background(), client, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone {
		t.Fatalf("result = %v, want pendingRetryDone", result)
	}
	if len(gotTexts) != 2 {
		t.Fatalf("sent texts = %#v, want summary then fresh", gotTexts)
	}
	if !strings.Contains(gotTexts[0], "释放 1 条任务") || gotTexts[1] != "fresh message" {
		t.Fatalf("unexpected send order/texts: %#v", gotTexts)
	}
}

func TestRetryPendingCodexSelectsFirstStaleTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	writeCodexPending(t, "aa-fresh-other.json", codexFailedNotification{
		FailedAt:  now.Add(-2 * time.Minute).Format(time.RFC3339),
		SessionID: "fresh-other",
		To:        "other@im.wechat",
		Message:   "fresh other",
	})
	staleOwner := writeCodexPending(t, "bb-stale-owner.json", codexFailedNotification{
		FailedAt:  now.Add(-16 * time.Minute).Format(time.RFC3339),
		SessionID: "stale-owner",
		To:        "owner@im.wechat",
		Message:   "stale owner",
	})
	writeCodexPending(t, "cc-stale-other.json", codexFailedNotification{
		FailedAt:  now.Add(-16 * time.Minute).Format(time.RFC3339),
		SessionID: "stale-other",
		To:        "other@im.wechat",
		Message:   "stale other",
	})
	files, err := pendingCodexNotificationFiles()
	if err != nil {
		t.Fatalf("pendingCodexNotificationFiles: %v", err)
	}

	group := collectStaleCodexReleaseGroup(files, "", now)

	if len(group) != 1 {
		t.Fatalf("group len = %d, want 1: %#v", len(group), group)
	}
	if group[0].path != staleOwner {
		t.Fatalf("group[0].path = %s, want %s", group[0].path, staleOwner)
	}
}

func TestReleaseStaleCodexStopsWhenMoveToReleasedFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeCodexPending(t, "old.json", codexFailedNotification{
		FailedAt:  time.Now().Add(-16 * time.Minute).Format(time.RFC3339),
		SessionID: "old-session",
		CWD:       "/work/old",
		To:        "owner@im.wechat",
		Message:   codexStopMessage("device-old", "/work/old", "旧任务完成。"),
	})
	releasedDir := filepath.Join(os.Getenv("HOME"), ".codex", "log", "weclaw-notify-released")
	if err := os.MkdirAll(filepath.Dir(releasedDir), 0o700); err != nil {
		t.Fatalf("mkdir codex log dir: %v", err)
	}
	if err := os.WriteFile(releasedDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write released dir blocker: %v", err)
	}

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	calls := 0
	origSend := sendCodexPendingTextReply
	t.Cleanup(func() {
		sendCodexPendingTextReply = origSend
	})
	sendCodexPendingTextReply = func(_ context.Context, _ *ilink.Client, _ string, _ string, _ string, _ string) error {
		calls++
		return nil
	}
	files, err := pendingCodexNotificationFiles()
	if err != nil {
		t.Fatalf("pendingCodexNotificationFiles: %v", err)
	}

	result, released := releaseStaleCodexNotifications(context.Background(), client, files, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone || !released {
		t.Fatalf("result, released = %v, %v; want pendingRetryDone, true", result, released)
	}
	if calls != 1 {
		t.Fatalf("send calls = %d, want 1", calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pending file moved despite released-dir failure: %v", err)
	}
}

func TestRetryPendingCodexQueuesSummaryWhenReleaseSummarySendFails(t *testing.T) {
	setPendingRetryDelayForTest(t, time.Millisecond)
	t.Setenv("HOME", t.TempDir())
	writeCodexPending(t, "old.json", codexFailedNotification{
		FailedAt:  time.Now().Add(-16 * time.Minute).Format(time.RFC3339),
		SessionID: "old-session",
		CWD:       "/work/old",
		To:        "owner@im.wechat",
		Message:   codexStopMessage("device-old", "/work/old", "旧任务完成。"),
	})

	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot@im.bot"})
	calls := 0
	origSend := sendCodexPendingTextReply
	t.Cleanup(func() {
		sendCodexPendingTextReply = origSend
	})
	sendCodexPendingTextReply = func(_ context.Context, _ *ilink.Client, _ string, _ string, _ string, _ string) error {
		calls++
		return errors.New("temporary send failure")
	}

	result := retryPendingCodexNotifications(context.Background(), client, "owner@im.wechat", "fresh-token", newPendingRetryBudget())

	if result != pendingRetryDone {
		t.Fatalf("result = %v, want pendingRetryDone", result)
	}
	if calls != 1 {
		t.Fatalf("send calls = %d, want 1", calls)
	}
	files, err := pendingCodexNotificationFiles()
	if err != nil {
		t.Fatalf("pendingCodexNotificationFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("pending codex files = %d, want 1 summary", len(files))
	}
	summary, err := readCodexFailedNotification(files[0])
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !summary.Summary || summary.ReleasedCount != 1 || !strings.Contains(summary.Message, "释放 1 条任务") {
		t.Fatalf("summary notification = %#v", summary)
	}
	released := codexReleasedFiles(t)
	if len(released) != 1 {
		t.Fatalf("released files = %d, want 1", len(released))
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

func codexReleasedFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".codex", "log", "weclaw-notify-released")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read released dir: %v", err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files
}

func codexStopMessage(device, cwd, body string) string {
	return "Codex 任务完成\n\nSession ID: generated\n目录: " + cwd + "\n设备: " + device + "\n\n" + body
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
