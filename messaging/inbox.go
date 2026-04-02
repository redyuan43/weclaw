package messaging

import (
	"strings"
	"sync"
	"time"
)

// InboxRecord is a lightweight view of an inbound text message for local automation.
type InboxRecord struct {
	Seq          int64  `json:"seq"`
	MessageID    int64  `json:"message_id,omitempty"`
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	Text         string `json:"text"`
	ContextToken string `json:"context_token,omitempty"`
	ReceivedAt   string `json:"received_at"`
	Suppressed   bool   `json:"suppressed"`
	Bridged      bool   `json:"bridged"`
}

// InboxStore keeps a bounded in-memory list of recent inbound messages.
type InboxStore struct {
	mu      sync.Mutex
	nextSeq int64
	maxSize int
	records []InboxRecord
}

func NewInboxStore(maxSize int) *InboxStore {
	if maxSize <= 0 {
		maxSize = 200
	}
	return &InboxStore{maxSize: maxSize}
}

func (s *InboxStore) Append(record InboxRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq++
	record.Seq = s.nextSeq
	record.ReceivedAt = time.Now().UTC().Format(time.RFC3339)
	s.records = append(s.records, record)
	if len(s.records) > s.maxSize {
		s.records = append([]InboxRecord(nil), s.records[len(s.records)-s.maxSize:]...)
	}
}

func (s *InboxStore) List(fromUserID string, afterSeq int64, limit int) []InboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	filtered := make([]InboxRecord, 0, limit)
	for _, record := range s.records {
		if afterSeq > 0 && record.Seq <= afterSeq {
			continue
		}
		if fromUserID != "" && !strings.EqualFold(record.FromUserID, fromUserID) {
			continue
		}
		filtered = append(filtered, record)
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	out := make([]InboxRecord, len(filtered))
	copy(out, filtered)
	return out
}

func (s *InboxStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = nil
}
