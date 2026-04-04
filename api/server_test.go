package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/messaging"
)

type apiFakeAgent struct {
	info        agent.AgentInfo
	reply       string
	lastConvID  string
	lastMessage string
}

func (f *apiFakeAgent) Chat(_ context.Context, conversationID string, message string) (string, error) {
	f.lastConvID = conversationID
	f.lastMessage = message
	return f.reply, nil
}

func (f *apiFakeAgent) ResetSession(_ context.Context, _ string) (string, error) { return "", nil }
func (f *apiFakeAgent) Info() agent.AgentInfo                                    { return f.info }
func (f *apiFakeAgent) SetCwd(_ string)                                          {}

func TestHandleAgentChat(t *testing.T) {
	ag := &apiFakeAgent{
		info:  agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-5.1-codex-mini", Command: "/usr/bin/codex", PID: 123},
		reply: `{"action":"reply_local_user","message":"ok","target_node":null,"rationale":"r","follow_up_needed":false}`,
	}
	handler := messaging.NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	handler.SetDefaultAgentName("codex")

	server := NewServer(nil, "", nil, handler)
	reqBody := AgentChatRequest{
		ConversationID: "a2a:local-node:conv-1",
		Message:        "hello",
	}
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	server.handleAgentChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload["reply"] != ag.reply {
		t.Fatalf("reply = %v, want %q", payload["reply"], ag.reply)
	}
	agentPayload := payload["agent"].(map[string]any)
	if agentPayload["name"] != "codex" {
		t.Fatalf("agent.name = %v, want codex", agentPayload["name"])
	}
	if ag.lastConvID != "a2a:local-node:conv-1" {
		t.Fatalf("conversation_id = %q, want a2a:local-node:conv-1", ag.lastConvID)
	}
	if ag.lastMessage != "hello" {
		t.Fatalf("message = %q, want hello", ag.lastMessage)
	}
}

func TestHandleBridgeInbound(t *testing.T) {
	ag := &apiFakeAgent{
		info:  agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-5.1-codex-mini", Command: "/usr/bin/codex", PID: 123},
		reply: `{"action":"reply_local_user","message":"peer ok","target_node":null,"rationale":"r","follow_up_needed":false}`,
	}
	var mu sync.Mutex
	var sentTo string
	var sentText string

	handler := messaging.NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	handler.SetDefaultAgentName("codex")
	handler.SetBridge(messaging.NewBridgeRuntime(
		messaging.BridgeConfig{
			Enabled:     true,
			NodeID:      "local-node",
			PeerNodeID:  "remote-node",
			PeerBaseURL: "http://peer.example",
			LocalUserID: "local-user@im.wechat",
		},
		messaging.BridgeRuntimeDeps{
			Chat: handler.ChatLocalAgent,
			SendText: func(_ context.Context, accountID, toUserID, text, contextToken string) error {
				mu.Lock()
				sentTo = toUserID
				sentText = text
				mu.Unlock()
				return nil
			},
			Dispatch: func(_ context.Context, targetBaseURL string, request messaging.TaskRequest) (*messaging.TaskResult, error) {
				return &messaging.TaskResult{
					TaskID:   request.Envelope.MessageID,
					Status:   "pending",
					Accepted: true,
					Detail:   "accepted",
				}, nil
			},
		},
	))

	server := NewServer(nil, "", nil, handler)
	reqBody := messaging.TaskRequest{
		Envelope: messaging.Envelope{
			MessageID:      "msg-1",
			ConversationID: "conv-1",
			SourceNode:     "remote-node",
			TargetNode:     "local-node",
			SourceTaskID:   "task-1",
		},
		TaskType: "peer_request",
		Payload: map[string]any{
			"text":          "帮忙处理",
			"delivery_mode": string(messaging.DeliveryModeDeliverToPeerUser),
		},
	}
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/bridge/inbound", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	server.handleBridgeInbound(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var payload messaging.TaskResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !payload.Accepted || payload.Route != "queued" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		gotTo := sentTo
		gotText := sentText
		mu.Unlock()
		if gotTo == "local-user@im.wechat" && gotText == "peer ok" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	gotTo := sentTo
	gotText := sentText
	mu.Unlock()
	if gotTo != "local-user@im.wechat" || gotText != "peer ok" {
		t.Fatalf("unexpected local send: to=%q text=%q", gotTo, gotText)
	}
}
