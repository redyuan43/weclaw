package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

var sendCodexPendingTextReply = SendTextReply

var pendingCodexReleaseAge = 15 * time.Minute

type codexFailedNotification struct {
	FailedAt      string `json:"failed_at"`
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
	To            string `json:"to"`
	APIURL        string `json:"api_url"`
	Error         string `json:"error"`
	Message       string `json:"message"`
	RetryCount    int    `json:"retry_count,omitempty"`
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	Summary       bool   `json:"summary,omitempty"`
	ReleasedCount int    `json:"released_count,omitempty"`
}

// RetryPendingCodexNotifications resends locally queued Codex Stop notifications.
// Failed hook sends are stored by ~/.codex/hooks/codex_wechat_stop.py.
func RetryPendingCodexNotifications(ctx context.Context, client *ilink.Client, onlyUserID, contextToken string) {
	if client == nil {
		return
	}

	pendingRetryMu.Lock()
	defer pendingRetryMu.Unlock()

	retryPendingCodexNotifications(ctx, client, onlyUserID, contextToken, newPendingRetryBudget())
}

func retryPendingCodexNotifications(ctx context.Context, client *ilink.Client, onlyUserID, contextToken string, budget *pendingRetryBudget) pendingRetryResult {
	mergeNext := false
	for {
		files, err := pendingCodexNotificationFiles()
		if err != nil {
			log.Printf("[codex-pending] list failed notifications: %v", err)
			return pendingRetryDone
		}
		if len(files) == 0 {
			return pendingRetryDone
		}
		result, released := releaseStaleCodexNotifications(ctx, client, files, onlyUserID, contextToken, budget)
		if result == pendingRetryInvalidContext || result == pendingRetryBudgetExhausted {
			return result
		}
		if released {
			if result == pendingRetryDone {
				return pendingRetryDone
			}
			continue
		}
		if !budget.allow() {
			log.Printf("[codex-pending] retry limit reached, remaining notifications will be retried later")
			return pendingRetryBudgetExhausted
		}
		if mergeNext {
			result := retryMergedCodexNotifications(ctx, client, files, onlyUserID, contextToken, budget)
			if result == pendingRetryInvalidContext {
				return result
			}
			mergeNext = false
			if result != pendingRetryDone {
				continue
			}
		}

		delivered := false
		for _, path := range files {
			notification, err := readCodexFailedNotification(path)
			if err != nil {
				log.Printf("[codex-pending] read %s: %v", path, err)
				continue
			}
			if notification.To == "" || strings.TrimSpace(notification.Message) == "" {
				continue
			}
			if onlyUserID != "" && notification.To != onlyUserID {
				continue
			}

			token := ""
			if onlyUserID != "" && notification.To == onlyUserID {
				token = contextToken
			}
			if err := sendCodexPendingTextReply(ctx, client, notification.To, notification.Message, token, ""); err != nil {
				log.Printf("[codex-pending] resend session=%s to=%s failed: %v", notification.SessionID, notification.To, err)
				dead := markCodexNotificationAttempt(path, notification, err)
				if errors.Is(err, ErrInvalidContext) {
					return pendingRetryInvalidContext
				}
				if dead {
					continue
				}
				continue
			}
			if err := moveCodexNotificationToSent(path); err != nil {
				log.Printf("[codex-pending] mark sent %s: %v", path, err)
			}
			moveDuplicateCodexNotificationsToSent(files, path, notification)
			budget.consume()
			log.Printf("[codex-pending] resent session=%s to=%s", notification.SessionID, notification.To)
			if budget.exhausted() {
				return pendingRetryBudgetExhausted
			}
			before, err := countPendingCodexNotifications()
			if err != nil {
				log.Printf("[codex-pending] count pending before wait: %v", err)
			}
			ok, grew := waitBeforeNextPendingRetryAndDetectGrowth(ctx, countPendingCodexNotifications, before)
			if !ok {
				return pendingRetryDone
			}
			mergeNext = grew
			delivered = true
			break
		}
		if !delivered {
			return pendingRetryDone
		}
	}
}

func releaseStaleCodexNotifications(ctx context.Context, client *ilink.Client, files []string, onlyUserID, contextToken string, budget *pendingRetryBudget) (pendingRetryResult, bool) {
	group := collectStaleCodexReleaseGroup(files, onlyUserID, time.Now())
	if len(group) == 0 {
		return pendingRetryDone, false
	}
	if !budget.allow() {
		log.Printf("[codex-pending] release summary skipped because retry budget is exhausted")
		return pendingRetryBudgetExhausted, false
	}

	first := group[0]
	message := buildCodexReleasedSummary(group)
	token := ""
	if onlyUserID != "" && first.item.To == onlyUserID {
		token = contextToken
	}
	if err := sendCodexPendingTextReply(ctx, client, first.item.To, message, token, ""); err != nil {
		log.Printf("[codex-pending] send released summary count=%d to=%s failed: %v", len(group), first.item.To, err)
		if summaryPath, saveErr := saveCodexReleasedSummary(first.item.To, message, err, len(group)); saveErr != nil {
			log.Printf("[codex-pending] save released summary failed: %v", saveErr)
			if errors.Is(err, ErrInvalidContext) {
				return pendingRetryInvalidContext, false
			}
			return pendingRetryDone, false
		} else {
			log.Printf("[codex-pending] queued released summary: %s", summaryPath)
		}
		moveCodexReleaseGroup(group)
		if errors.Is(err, ErrInvalidContext) {
			return pendingRetryInvalidContext, true
		}
		return pendingRetryDone, true
	}

	if !moveCodexReleaseGroup(group) {
		return pendingRetryDone, true
	}
	budget.consume()
	log.Printf("[codex-pending] released stale count=%d to=%s", len(group), first.item.To)
	if budget.exhausted() {
		return pendingRetryBudgetExhausted, true
	}
	return pendingRetryBatchSent, true
}

func retryMergedCodexNotifications(ctx context.Context, client *ilink.Client, files []string, onlyUserID, contextToken string, budget *pendingRetryBudget) pendingRetryResult {
	group := collectCodexMergeGroup(files, onlyUserID)
	if len(group) < 2 {
		return pendingRetryDone
	}

	first := group[0]
	token := ""
	if onlyUserID != "" && first.item.To == onlyUserID {
		token = contextToken
	}
	message := joinCodexNotifications(group)
	if err := sendCodexPendingTextReply(ctx, client, first.item.To, message, token, ""); err != nil {
		log.Printf("[codex-pending] resend merged count=%d to=%s failed: %v", len(group), first.item.To, err)
		for _, entry := range group {
			markCodexNotificationAttempt(entry.path, entry.item, err)
		}
		if errors.Is(err, ErrInvalidContext) {
			return pendingRetryInvalidContext
		}
		return pendingRetryDone
	}

	for _, entry := range group {
		if err := moveCodexNotificationToSent(entry.path); err != nil {
			log.Printf("[codex-pending] mark merged sent %s: %v", entry.path, err)
		}
	}
	budget.consume()
	log.Printf("[codex-pending] resent merged count=%d to=%s", len(group), first.item.To)
	if budget.exhausted() {
		return pendingRetryBudgetExhausted
	}
	if !waitBeforeNextPendingRetry(ctx) {
		return pendingRetryDone
	}
	return pendingRetryBatchSent
}

type codexMergeEntry struct {
	path string
	item codexFailedNotification
}

func collectCodexMergeGroup(files []string, onlyUserID string) []codexMergeEntry {
	var group []codexMergeEntry
	target := ""
	seen := map[string]struct{}{}
	for _, path := range files {
		notification, err := readCodexFailedNotification(path)
		if err != nil {
			continue
		}
		if notification.To == "" || strings.TrimSpace(notification.Message) == "" {
			continue
		}
		if notification.Summary {
			continue
		}
		if onlyUserID != "" && notification.To != onlyUserID {
			continue
		}
		if target == "" {
			target = notification.To
		}
		if notification.To != target {
			continue
		}
		key := notification.SessionID
		if key == "" {
			key = strings.TrimSpace(notification.Message)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		group = append(group, codexMergeEntry{path: path, item: notification})
	}
	return group
}

func collectStaleCodexReleaseGroup(files []string, onlyUserID string, now time.Time) []codexMergeEntry {
	var group []codexMergeEntry
	target := ""
	for _, path := range files {
		notification, err := readCodexFailedNotification(path)
		if err != nil {
			log.Printf("[codex-pending] read stale candidate %s: %v", path, err)
			continue
		}
		if notification.Summary || notification.To == "" || strings.TrimSpace(notification.Message) == "" {
			continue
		}
		if onlyUserID != "" && notification.To != onlyUserID {
			continue
		}
		stamp, err := codexNotificationFailedTime(path, notification)
		if err != nil {
			log.Printf("[codex-pending] stale age unavailable for %s: %v", path, err)
			continue
		}
		if now.Sub(stamp) < pendingCodexReleaseAge {
			continue
		}
		if target == "" {
			target = notification.To
		}
		if notification.To != target {
			continue
		}
		group = append(group, codexMergeEntry{path: path, item: notification})
	}
	return group
}

func moveCodexReleaseGroup(group []codexMergeEntry) bool {
	movedAll := true
	for _, entry := range group {
		if err := moveCodexNotificationToReleased(entry.path); err != nil {
			log.Printf("[codex-pending] release stale %s: %v", entry.path, err)
			movedAll = false
		}
	}
	return movedAll
}

func joinCodexNotifications(group []codexMergeEntry) string {
	parts := make([]string, 0, len(group))
	for _, entry := range group {
		message := strings.TrimSpace(entry.item.Message)
		if message != "" {
			parts = append(parts, message)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func buildCodexReleasedSummary(group []codexMergeEntry) string {
	lines := make([]string, 0, len(group)+2)
	lines = append(lines, fmt.Sprintf("Codex pending 超过15分钟，已停止逐条重传，释放 %d 条任务：", len(group)))
	for _, entry := range group {
		lines = append(lines, summarizeCodexReleasedNotification(entry.item))
	}
	return strings.Join(lines, "\n")
}

func summarizeCodexReleasedNotification(notification codexFailedNotification) string {
	device := extractCodexMessageField(notification.Message, "设备:")
	if device == "" {
		device = "unknown"
	}
	cwd := strings.TrimSpace(notification.CWD)
	if cwd == "" {
		cwd = extractCodexMessageField(notification.Message, "目录:")
	}
	if cwd == "" {
		cwd = "unknown"
	}
	sessionID := strings.TrimSpace(notification.SessionID)
	if sessionID == "" {
		sessionID = "unknown"
	}
	description := summarizeCodexMessageBody(notification.Message)
	if description == "" {
		description = "无任务描述"
	}
	return fmt.Sprintf("- 设备: %s | 文件夹: %s | 在线ID: %s | 任务: %s", oneLine(device, 80), oneLine(cwd, 160), oneLine(sessionID, 100), oneLine(description, 220))
}

func extractCodexMessageField(message, prefix string) string {
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func summarizeCodexMessageBody(message string) string {
	lines := strings.Split(message, "\n")
	bodyStart := 0
	for idx, line := range lines {
		if strings.TrimSpace(line) == "" && idx >= 4 {
			bodyStart = idx + 1
			break
		}
	}
	if bodyStart == 0 && len(lines) > 0 {
		bodyStart = len(lines) - 1
	}
	var parts []string
	for _, line := range lines[bodyStart:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(parts) > 0 {
				break
			}
			continue
		}
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, " ")
}

func oneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func codexNotificationFailedTime(path string, notification codexFailedNotification) (time.Time, error) {
	if notification.FailedAt != "" {
		if stamp, err := time.Parse(time.RFC3339, notification.FailedAt); err == nil {
			return stamp, nil
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func pendingCodexNotificationFiles() ([]string, error) {
	dir, err := codexNotifyLogDir("weclaw-notify-failed")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func countPendingCodexNotifications() (int, error) {
	files, err := pendingCodexNotificationFiles()
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

func readCodexFailedNotification(path string) (codexFailedNotification, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return codexFailedNotification{}, err
	}
	var notification codexFailedNotification
	if err := json.Unmarshal(raw, &notification); err != nil {
		return codexFailedNotification{}, err
	}
	return notification, nil
}

func moveCodexNotificationToSent(path string) error {
	dir, err := codexNotifyLogDir("weclaw-notify-sent")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(dir, filepath.Base(path))
	if _, err := os.Stat(target); err == nil {
		ext := filepath.Ext(target)
		base := strings.TrimSuffix(target, ext)
		target = base + "." + time.Now().Format("20060102-150405.000000000") + ext
	}
	return os.Rename(path, target)
}

func moveCodexNotificationToDead(path string) error {
	dir, err := codexNotifyLogDir("weclaw-notify-dead")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(dir, filepath.Base(path))
	if _, err := os.Stat(target); err == nil {
		ext := filepath.Ext(target)
		base := strings.TrimSuffix(target, ext)
		target = base + "." + time.Now().Format("20060102-150405.000000000") + ext
	}
	return os.Rename(path, target)
}

func moveCodexNotificationToReleased(path string) error {
	dir, err := codexNotifyLogDir("weclaw-notify-released")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(dir, filepath.Base(path))
	if _, err := os.Stat(target); err == nil {
		ext := filepath.Ext(target)
		base := strings.TrimSuffix(target, ext)
		target = base + "." + time.Now().Format("20060102-150405.000000000") + ext
	}
	return os.Rename(path, target)
}

func saveCodexReleasedSummary(to, message string, cause error, releasedCount int) (string, error) {
	dir, err := codexNotifyLogDir("weclaw-notify-failed")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	notification := codexFailedNotification{
		FailedAt:      time.Now().Format(time.RFC3339),
		SessionID:     "released-summary-" + time.Now().Format("20060102-150405.000000000"),
		HookEventName: "Stop",
		To:            to,
		Error:         cause.Error(),
		Message:       message,
		Summary:       true,
		ReleasedCount: releasedCount,
	}
	raw, err := json.MarshalIndent(notification, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, notification.SessionID+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func markCodexNotificationAttempt(path string, notification codexFailedNotification, cause error) bool {
	notification.RetryCount++
	notification.LastAttemptAt = time.Now().Format(time.RFC3339)
	if cause != nil {
		notification.Error = cause.Error()
	}
	if notification.RetryCount >= maxPendingRetryAttempts {
		if err := moveCodexNotificationToDead(path); err != nil {
			log.Printf("[codex-pending] move dead-letter %s: %v", path, err)
		} else {
			log.Printf("[codex-pending] moved %s to dead-letter after %d attempts", filepath.Base(path), notification.RetryCount)
		}
		return true
	}
	raw, err := json.MarshalIndent(notification, "", "  ")
	if err != nil {
		log.Printf("[codex-pending] marshal retry metadata %s: %v", path, err)
		return false
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		log.Printf("[codex-pending] write retry metadata %s: %v", path, err)
	}
	return false
}

func moveDuplicateCodexNotificationsToSent(files []string, sentPath string, sent codexFailedNotification) {
	if strings.TrimSpace(sent.SessionID) == "" && strings.TrimSpace(sent.Message) == "" {
		return
	}
	for _, path := range files {
		if path == sentPath {
			continue
		}
		notification, err := readCodexFailedNotification(path)
		if err != nil {
			continue
		}
		if !sameCodexNotification(sent, notification) {
			continue
		}
		if err := moveCodexNotificationToSent(path); err != nil {
			log.Printf("[codex-pending] mark duplicate sent %s: %v", path, err)
			continue
		}
		log.Printf("[codex-pending] marked duplicate session=%s file=%s as sent", sent.SessionID, filepath.Base(path))
	}
}

func sameCodexNotification(a, b codexFailedNotification) bool {
	if a.To != b.To {
		return false
	}
	if a.SessionID != "" && b.SessionID != "" {
		return a.SessionID == b.SessionID
	}
	return strings.TrimSpace(a.Message) != "" && strings.TrimSpace(a.Message) == strings.TrimSpace(b.Message)
}

func codexNotifyLogDir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "log", name), nil
}
