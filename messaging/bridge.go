package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

type BridgeConfig struct {
	Enabled        bool
	Endpoint       string
	Fallback       string
	ChatAllowlist  map[string]struct{}
	IgnorePrefixes []string
	Timeout        time.Duration
}

type BridgeRequest struct {
	AccountID    string `json:"account_id"`
	FromUserID   string `json:"from_user_id"`
	MessageID    int64  `json:"message_id,omitempty"`
	Text         string `json:"text"`
	ContextToken string `json:"context_token,omitempty"`
	RouteMode    string `json:"route_mode"`
}

type BridgeResponse struct {
	TaskID   string `json:"task_id,omitempty"`
	Status   string `json:"status,omitempty"`
	Accepted bool   `json:"accepted"`
	Detail   string `json:"detail,omitempty"`
	Route    string `json:"route,omitempty"`
}

type BridgeClient struct {
	cfg        BridgeConfig
	httpClient *http.Client
}

func NewBridgeClient(cfg BridgeConfig) *BridgeClient {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &BridgeClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

func (b *BridgeClient) Eligible(msg ilink.WeixinMessage, text string) (routeMode string, normalized string, allowed bool, suppressed bool) {
	if b == nil || !b.cfg.Enabled || b.cfg.Endpoint == "" {
		return "", text, false, false
	}
	trimmed := strings.TrimSpace(text)
	for _, prefix := range b.cfg.IgnorePrefixes {
		if prefix != "" && strings.HasPrefix(trimmed, prefix) {
			return "", strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), false, true
		}
	}
	if _, ok := b.cfg.ChatAllowlist[msg.FromUserID]; !ok {
		return "", text, false, false
	}
	if strings.HasPrefix(trimmed, "/a2a") {
		return "explicit_a2a", strings.TrimSpace(strings.TrimPrefix(trimmed, "/a2a")), true, false
	}
	if strings.HasPrefix(trimmed, "/local") {
		return "explicit_local", strings.TrimSpace(strings.TrimPrefix(trimmed, "/local")), true, false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "@") {
		return "", text, false, false
	}
	return "auto", trimmed, true, false
}

func (b *BridgeClient) Forward(ctx context.Context, accountID string, msg ilink.WeixinMessage, text, routeMode string) (*BridgeResponse, error) {
	if b == nil {
		return nil, fmt.Errorf("bridge disabled")
	}
	payload := BridgeRequest{
		AccountID:    accountID,
		FromUserID:   msg.FromUserID,
		MessageID:    msg.MessageID,
		Text:         text,
		ContextToken: msg.ContextToken,
		RouteMode:    routeMode,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := b.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var out BridgeResponse
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		out.Accepted = false
	}
	return &out, nil
}
