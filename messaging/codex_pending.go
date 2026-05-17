package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

var sendCodexPendingTextReply = SendTextReply

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
