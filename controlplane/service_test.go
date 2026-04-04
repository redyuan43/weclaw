package controlplane

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type testSentMessage struct {
	accountID string
	toUserID  string
	text      string
}

func newTestService(t *testing.T) (*Store, *Service, *[]testSentMessage) {
	t.Helper()

	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})

	var sent []testSentMessage
	service := NewService(
		store,
		"http://127.0.0.1:18011",
		func(_ context.Context, conversationID, message, agentName string) (ChatResult, error) {
			return ChatResult{
				Reply:     fmt.Sprintf("[%s] %s | %s", agentName, conversationID, message),
				AgentName: agentName,
				Model:     "test-model",
			}, nil
		},
		func(_ context.Context, accountID, toUserID, text, _ string) error {
			sent = append(sent, testSentMessage{
				accountID: accountID,
				toUserID:  toUserID,
				text:      text,
			})
			return nil
		},
	)
	service.SetAvailableBaseAgents([]string{"codex", "claude"})
	if err := service.SyncAccounts(map[string]string{
		"acct-a": "owner-a",
		"acct-b": "owner-b",
	}, "codex"); err != nil {
		t.Fatalf("sync accounts: %v", err)
	}
	if err := service.UpdateProfile(UpdateProfileInput{
		AccountID:      "acct-a",
		DisplayName:    "账号A",
		Description:    "账号A主 Agent",
		OwnerContactID: "owner-a",
		BaseAgentName:  "codex",
	}); err != nil {
		t.Fatalf("update profile a: %v", err)
	}
	if err := service.UpdateProfile(UpdateProfileInput{
		AccountID:      "acct-b",
		DisplayName:    "账号B",
		Description:    "账号B主 Agent",
		OwnerContactID: "owner-b",
		BaseAgentName:  "claude",
	}); err != nil {
		t.Fatalf("update profile b: %v", err)
	}

	return store, service, &sent
}

func TestBuildAgentCard(t *testing.T) {
	_, service, _ := newTestService(t)

	card, err := service.BuildAgentCard("acct-a")
	if err != nil {
		t.Fatalf("build agent card: %v", err)
	}
	if card == nil {
		t.Fatal("card = nil")
	}
	if card.Name != "账号A" {
		t.Fatalf("card.Name = %q, want 账号A", card.Name)
	}
	if card.URL == "" {
		t.Fatal("card.URL is empty")
	}
	if len(card.Skills) == 0 {
		t.Fatal("card.Skills is empty")
	}
}

func TestDelegationApprovalFlow(t *testing.T) {
	store, service, sent := newTestService(t)

	task, grant, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "请帮我完成跨账号任务")
	if err != nil {
		t.Fatalf("create delegation: %v", err)
	}
	if task.Status != TaskStatusWaitingApproval {
		t.Fatalf("task.Status = %q, want %q", task.Status, TaskStatusWaitingApproval)
	}
	if grant.Status != ApprovalStatusPending {
		t.Fatalf("grant.Status = %q, want %q", grant.Status, ApprovalStatusPending)
	}
	if len(*sent) == 0 || (*sent)[0].toUserID != "owner-b" {
		t.Fatalf("expected approval message to owner-b, sent=%#v", *sent)
	}

	if _, err := service.ApproveGrant(context.Background(), grant.ID, "owner-b"); err != nil {
		t.Fatalf("approve grant: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		parent, err := store.GetTask(task.ID, 20)
		if err != nil {
			t.Fatalf("get parent task: %v", err)
		}
		if parent != nil && parent.Status == TaskStatusCompleted {
			if parent.ResultText == "" {
				t.Fatal("parent.ResultText is empty")
			}
			if len(*sent) < 2 {
				t.Fatalf("expected requester notification, sent=%#v", *sent)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	parent, err := store.GetTask(task.ID, 20)
	if err != nil {
		t.Fatalf("get parent task after wait: %v", err)
	}
	t.Fatalf("task did not complete, final state=%#v", parent)
}

func TestSubmitOwnerTaskAutoDelegatesByHeuristic(t *testing.T) {
	store, service, sent := newTestService(t)

	task, err := service.SubmitOwnerTask(context.Background(), "acct-a", "owner-a", "请账号B帮我完成这件事")
	if err != nil {
		t.Fatalf("submit owner task: %v", err)
	}
	if task.Status != TaskStatusWaitingApproval {
		t.Fatalf("task.Status = %q, want %q", task.Status, TaskStatusWaitingApproval)
	}
	if task.TargetAccountID != "acct-b" {
		t.Fatalf("task.TargetAccountID = %q, want acct-b", task.TargetAccountID)
	}

	grant, err := store.GetApproval(task.ApprovalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if grant == nil || grant.Status != ApprovalStatusPending {
		t.Fatalf("grant = %#v, want pending", grant)
	}
	if len(*sent) == 0 || (*sent)[0].accountID != "acct-b" {
		t.Fatalf("expected approval notice to acct-b owner, sent=%#v", *sent)
	}
}
