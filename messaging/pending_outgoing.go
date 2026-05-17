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
	"github.com/google/uuid"
)

var (
	sendPendingOutgoingText      = SendTextReply
	sendPendingOutgoingMediaURL  = SendMediaFromURL
	sendPendingOutgoingMediaPath = SendMediaFromPath
)

// PendingOutgoingSend is a queued /api/send request that failed transiently.
type PendingOutgoingSend struct {
	CreatedAt     string `json:"created_at"`
	AccountID     string `json:"account_id,omitempty"`
	To            string `json:"to"`
	Text          string `json:"text,omitempty"`
	MediaURL      string `json:"media_url,omitempty"`
	MediaPath     string `json:"media_path,omitempty"`
	Error         string `json:"error,omitempty"`
	RetryCount    int    `json:"retry_count,omitempty"`
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
}

// SavePendingOutgoingSend stores a failed outgoing send for later retry.
func SavePendingOutgoingSend(accountID, to, text, mediaURL, mediaPath, cause string) (string, error) {
	if to == "" {
		return "", nil
	}
	if text == "" && mediaURL == "" && mediaPath == "" {
		return "", nil
	}

	dir, err := weclawQueueDir("pending-sends")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	item := PendingOutgoingSend{
		CreatedAt: time.Now().Format(time.RFC3339),
		AccountID: accountID,
		To:        to,
		Text:      text,
		MediaURL:  mediaURL,
		MediaPath: mediaPath,
		Error:     cause,
	}
	name := fmt.Sprintf("%s-%s.json", time.Now().Format("20060102-150405.000000000"), uuid.NewString())
	path := filepath.Join(dir, name)
	raw, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RetryPendingOutgoingSends resends queued /api/send requests.
func RetryPendingOutgoingSends(ctx context.Context, client *ilink.Client, onlyUserID, contextToken string) {
	if client == nil {
		return
	}

	pendingRetryMu.Lock()
	defer pendingRetryMu.Unlock()

	retryPendingOutgoingSends(ctx, client, onlyUserID, contextToken, newPendingRetryBudget())
}

func retryPendingOutgoingSends(ctx context.Context, client *ilink.Client, onlyUserID, contextToken string, budget *pendingRetryBudget) pendingRetryResult {
	mergeNext := false
	for {
		files, err := pendingOutgoingFiles()
		if err != nil {
			log.Printf("[outbox] list pending sends: %v", err)
			return pendingRetryDone
		}
		if len(files) == 0 {
			return pendingRetryDone
		}
		if !budget.allow() {
			log.Printf("[outbox] retry limit reached, remaining sends will be retried later")
			return pendingRetryBudgetExhausted
		}
		if mergeNext {
			result := retryMergedPendingOutgoingTexts(ctx, client, files, onlyUserID, contextToken, budget)
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
			item, err := readPendingOutgoing(path)
			if err != nil {
				log.Printf("[outbox] read %s: %v", path, err)
				continue
			}
			if item.To == "" {
				continue
			}
			if item.AccountID != "" && item.AccountID != client.BotID() {
				continue
			}
			if onlyUserID != "" && item.To != onlyUserID {
				continue
			}

			token := ""
			if onlyUserID != "" && item.To == onlyUserID {
				token = contextToken
			}
			if err := sendPendingOutgoing(ctx, client, item, token); err != nil {
				log.Printf("[outbox] resend to=%s file=%s failed: %v", item.To, filepath.Base(path), err)
				dead := markPendingOutgoingAttempt(path, item, err)
				if errors.Is(err, ErrInvalidContext) {
					return pendingRetryInvalidContext
				}
				if dead {
					continue
				}
				continue
			}
			if err := movePendingOutgoingToSent(path); err != nil {
				log.Printf("[outbox] mark sent %s: %v", path, err)
			}
			budget.consume()
			log.Printf("[outbox] resent to=%s file=%s", item.To, filepath.Base(path))
			if budget.exhausted() {
				return pendingRetryBudgetExhausted
			}
			before, err := countPendingOutgoingSends()
			if err != nil {
				log.Printf("[outbox] count pending before wait: %v", err)
			}
			ok, grew := waitBeforeNextPendingRetryAndDetectGrowth(ctx, countPendingOutgoingSends, before)
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

func retryMergedPendingOutgoingTexts(ctx context.Context, client *ilink.Client, files []string, onlyUserID, contextToken string, budget *pendingRetryBudget) pendingRetryResult {
	group := collectPendingOutgoingTextMergeGroup(files, client.BotID(), onlyUserID)
	if len(group) < 2 {
		return pendingRetryDone
	}

	first := group[0]
	token := ""
	if onlyUserID != "" && first.item.To == onlyUserID {
		token = contextToken
	}
	message := joinPendingOutgoingTexts(group)
	if err := sendPendingOutgoingText(ctx, client, first.item.To, message, token, ""); err != nil {
		log.Printf("[outbox] resend merged text count=%d to=%s failed: %v", len(group), first.item.To, err)
		for _, entry := range group {
			markPendingOutgoingAttempt(entry.path, entry.item, err)
		}
		if errors.Is(err, ErrInvalidContext) {
			return pendingRetryInvalidContext
		}
		return pendingRetryDone
	}

	for _, entry := range group {
		if err := movePendingOutgoingToSent(entry.path); err != nil {
			log.Printf("[outbox] mark merged sent %s: %v", entry.path, err)
		}
	}
	budget.consume()
	log.Printf("[outbox] resent merged text count=%d to=%s", len(group), first.item.To)
	if budget.exhausted() {
		return pendingRetryBudgetExhausted
	}
	if !waitBeforeNextPendingRetry(ctx) {
		return pendingRetryDone
	}
	return pendingRetryBatchSent
}

type pendingOutgoingTextMergeEntry struct {
	path string
	item PendingOutgoingSend
}

func collectPendingOutgoingTextMergeGroup(files []string, botID, onlyUserID string) []pendingOutgoingTextMergeEntry {
	var group []pendingOutgoingTextMergeEntry
	target := ""
	for _, path := range files {
		item, err := readPendingOutgoing(path)
		if err != nil {
			continue
		}
		if item.To == "" || strings.TrimSpace(item.Text) == "" || item.MediaURL != "" || item.MediaPath != "" {
			continue
		}
		if item.AccountID != "" && item.AccountID != botID {
			continue
		}
		if onlyUserID != "" && item.To != onlyUserID {
			continue
		}
		if target == "" {
			target = item.To
		}
		if item.To != target {
			continue
		}
		group = append(group, pendingOutgoingTextMergeEntry{path: path, item: item})
	}
	return group
}

func joinPendingOutgoingTexts(group []pendingOutgoingTextMergeEntry) string {
	parts := make([]string, 0, len(group))
	for _, entry := range group {
		text := strings.TrimSpace(entry.item.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func sendPendingOutgoing(ctx context.Context, client *ilink.Client, item PendingOutgoingSend, contextToken string) error {
	if item.Text != "" {
		if err := sendPendingOutgoingText(ctx, client, item.To, item.Text, contextToken, ""); err != nil {
			return err
		}
	}
	if item.MediaURL != "" {
		if err := sendPendingOutgoingMediaURL(ctx, client, item.To, item.MediaURL, contextToken); err != nil {
			return err
		}
	}
	if item.MediaPath != "" {
		if err := sendPendingOutgoingMediaPath(ctx, client, item.To, item.MediaPath, contextToken); err != nil {
			return err
		}
	}
	return nil
}

func pendingOutgoingFiles() ([]string, error) {
	dir, err := weclawQueueDir("pending-sends")
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

func countPendingOutgoingSends() (int, error) {
	files, err := pendingOutgoingFiles()
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

func readPendingOutgoing(path string) (PendingOutgoingSend, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PendingOutgoingSend{}, err
	}
	var item PendingOutgoingSend
	if err := json.Unmarshal(raw, &item); err != nil {
		return PendingOutgoingSend{}, err
	}
	return item, nil
}

func movePendingOutgoingToSent(path string) error {
	dir, err := weclawQueueDir("sent-sends")
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

func movePendingOutgoingToDead(path string) error {
	dir, err := weclawQueueDir("dead-sends")
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

func markPendingOutgoingAttempt(path string, item PendingOutgoingSend, cause error) bool {
	item.RetryCount++
	item.LastAttemptAt = time.Now().Format(time.RFC3339)
	if cause != nil {
		item.Error = cause.Error()
	}
	if item.RetryCount >= maxPendingRetryAttempts {
		if err := movePendingOutgoingToDead(path); err != nil {
			log.Printf("[outbox] move dead-letter %s: %v", path, err)
		} else {
			log.Printf("[outbox] moved %s to dead-letter after %d attempts", filepath.Base(path), item.RetryCount)
		}
		return true
	}
	raw, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		log.Printf("[outbox] marshal retry metadata %s: %v", path, err)
		return false
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		log.Printf("[outbox] write retry metadata %s: %v", path, err)
	}
	return false
}

func weclawQueueDir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", name), nil
}
