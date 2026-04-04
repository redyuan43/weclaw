package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/controlplane"
)

func newTestUserAgentService(t *testing.T) *controlplane.Service {
	t.Helper()

	store, err := controlplane.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})

	service := controlplane.NewService(
		store,
		"http://127.0.0.1:18011",
		func(_ context.Context, conversationID, message, agentName string) (controlplane.ChatResult, error) {
			return controlplane.ChatResult{
				Reply:     "处理结果: " + message,
				AgentName: agentName,
				Model:     "test-model",
			}, nil
		},
		nil,
	)
	service.SetAvailableBaseAgents([]string{"codex"})
	if err := service.SyncAccounts(map[string]string{"acct-a": "owner-a"}, "codex"); err != nil {
		t.Fatalf("sync accounts: %v", err)
	}
	if err := service.UpdateProfile(controlplane.UpdateProfileInput{
		AccountID:      "acct-a",
		DisplayName:    "账号A",
		Description:    "账号A主 Agent",
		OwnerContactID: "owner-a",
		BaseAgentName:  "codex",
	}); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	return service
}

func TestHandleAgentCard(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", nil, nil)
	server.SetUserAgents(newTestUserAgentService(t))

	req := httptest.NewRequest(http.MethodGet, "/a2a/users/acct-a/agent-card.json", nil)
	rec := httptest.NewRecorder()

	server.handleA2AUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var card controlplane.AgentCard
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	if card.Name != "账号A" {
		t.Fatalf("card.Name = %q, want 账号A", card.Name)
	}
}

func TestHandleA2AMessageSend(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", nil, nil)
	server.SetUserAgents(newTestUserAgentService(t))

	raw := []byte(`{
	  "jsonrpc":"2.0",
	  "id":"1",
	  "method":"message/send",
	  "params":{
	    "message":{
	      "role":"user",
	      "parts":[{"kind":"text","text":"请处理这个任务"}],
	      "contextId":"ctx-1"
	    },
	    "configuration":{"blocking":true,"historyLength":10}
	  }
	}`)

	req := httptest.NewRequest(http.MethodPost, "/a2a/users/acct-a", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	server.handleA2AUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var response controlplane.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("jsonrpc error = %#v", response.Error)
	}
	resultBytes, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var task controlplane.A2ATask
	if err := json.Unmarshal(resultBytes, &task); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if task.Status.State != controlplane.TaskStatusCompleted {
		t.Fatalf("task.Status.State = %q, want %q", task.Status.State, controlplane.TaskStatusCompleted)
	}
	if len(task.Artifacts) == 0 || task.Artifacts[0].Parts[0].Text == "" {
		t.Fatalf("task.Artifacts = %#v, want non-empty result", task.Artifacts)
	}
}
