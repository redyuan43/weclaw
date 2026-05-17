package messaging

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

const (
	maxPendingRetryAttempts = 5
)

var pendingRetryMu sync.Mutex

var pendingRetryDelay = 15 * time.Second

type pendingRetryBudget struct{}

func newPendingRetryBudget() *pendingRetryBudget {
	return &pendingRetryBudget{}
}

func (b *pendingRetryBudget) allow() bool {
	return b != nil
}

func (b *pendingRetryBudget) consume() {
}

func (b *pendingRetryBudget) exhausted() bool {
	return b == nil
}

type pendingRetryResult int

const (
	pendingRetryDone pendingRetryResult = iota
	pendingRetryBudgetExhausted
	pendingRetryInvalidContext
	pendingRetryBatchSent
)

// RetryPendingDeliveries serializes all backlog replay paths for one account.
// Pending sends are replayed one by one with a fixed delay. ret=-2 indicates
// that the current context token is no longer usable, so retrying stops there.
func RetryPendingDeliveries(ctx context.Context, client *ilink.Client, onlyUserID, contextToken string) {
	if client == nil {
		return
	}

	pendingRetryMu.Lock()
	defer pendingRetryMu.Unlock()

	budget := newPendingRetryBudget()
	if result := retryPendingCodexNotifications(ctx, client, onlyUserID, contextToken, budget); result == pendingRetryInvalidContext {
		log.Printf("[pending] stopping retry after invalid context from codex queue")
		return
	}
	if result := retryPendingOutgoingSends(ctx, client, onlyUserID, contextToken, budget); result == pendingRetryInvalidContext {
		log.Printf("[pending] stopping retry after invalid context from outbox")
	}
}

func waitBeforeNextPendingRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(pendingRetryDelay):
		return true
	}
}

func waitBeforeNextPendingRetryAndDetectGrowth(ctx context.Context, countPending func() (int, error), before int) (bool, bool) {
	if !waitBeforeNextPendingRetry(ctx) {
		return false, false
	}
	after, err := countPending()
	if err != nil {
		log.Printf("[pending] count pending after wait: %v", err)
		return true, false
	}
	return true, after > before
}
