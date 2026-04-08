package controlplane

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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
		AccountID:         "acct-a",
		DisplayName:       "账号A",
		Description:       "账号A主 Agent",
		OwnerContactID:    "owner-a",
		BaseAgentName:     "codex",
		DelegationEnabled: true,
	}); err != nil {
		t.Fatalf("update profile a: %v", err)
	}
	if err := service.UpdateProfile(UpdateProfileInput{
		AccountID:         "acct-b",
		DisplayName:       "账号B",
		Description:       "账号B主 Agent",
		OwnerContactID:    "owner-b",
		BaseAgentName:     "claude",
		DelegationEnabled: true,
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

func TestSubmitOwnerTaskAutoDelegatesBySpecialization(t *testing.T) {
	store, service, sent := newTestService(t)

	if err := service.UpdateProfile(UpdateProfileInput{
		AccountID:              "acct-b",
		DisplayName:            "Nano",
		Description:            "嵌入式和硬件方向 Agent",
		OwnerContactID:         "owner-b",
		BaseAgentName:          "claude",
		SpecializationTags:     []string{"嵌入式", "硬件", "板卡"},
		SpecializationExamples: []string{"Jetson 性能调优", "串口驱动排障"},
		DelegationEnabled:      true,
	}); err != nil {
		t.Fatalf("update profile specialization: %v", err)
	}

	decision, err := service.decideOwnerTask(context.Background(), &UserAgentProfile{
		AccountID:      "acct-a",
		DisplayName:    "账号A",
		Description:    "账号A主 Agent",
		BaseAgentName:  "codex",
		OwnerContactID: "owner-a",
	}, "owner-a", "帮我看一下这个 Jetson 板卡驱动问题")
	if err != nil {
		t.Fatalf("decide owner task: %v", err)
	}
	if decision.Action != "delegate" || decision.TargetAccountID != "acct-b" {
		t.Fatalf("decision = %#v, want delegate acct-b", decision)
	}

	task, err := service.SubmitOwnerTask(context.Background(), "acct-a", "owner-a", "帮我看一下这个 Jetson 板卡驱动问题")
	if err != nil {
		t.Fatalf("submit owner task with specialization: %v", err)
	}
	if task.Status != TaskStatusWaitingApproval {
		t.Fatalf("task.Status = %q, want %q", task.Status, TaskStatusWaitingApproval)
	}
	if task.TargetAccountID != "acct-b" {
		t.Fatalf("task.TargetAccountID = %q, want acct-b", task.TargetAccountID)
	}

	audit, err := store.ListTaskAudit(task.ID, 20)
	if err != nil {
		t.Fatalf("list task audit: %v", err)
	}
	found := false
	for _, item := range audit {
		if item.Category == "auto-delegation-selected" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected auto-delegation-selected audit, got %#v", audit)
	}
	if len(*sent) == 0 || (*sent)[0].accountID != "acct-b" {
		t.Fatalf("expected approval notice to acct-b owner, sent=%#v", *sent)
	}
}

func TestGetTaskDetailBuildsWorkflowGraph(t *testing.T) {
	_, service, _ := newTestService(t)

	task, grant, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "请帮我完成跨账号任务")
	if err != nil {
		t.Fatalf("create delegation: %v", err)
	}
	if _, err := service.ApproveGrant(context.Background(), grant.ID, "owner-b"); err != nil {
		t.Fatalf("approve grant: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := service.GetTaskDetail(task.ID, 50)
		if err != nil {
			t.Fatalf("get task detail: %v", err)
		}
		if detail != nil && detail.Task != nil && detail.Task.Status == TaskStatusCompleted {
			if detail.Workflow == nil || len(detail.Workflow.Nodes) < 4 {
				t.Fatalf("workflow = %#v, want populated graph", detail.Workflow)
			}
			if detail.Workflow.Status != TaskStatusCompleted {
				t.Fatalf("workflow.Status = %q, want %q", detail.Workflow.Status, TaskStatusCompleted)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	detail, err := service.GetTaskDetail(task.ID, 50)
	if err != nil {
		t.Fatalf("get task detail after wait: %v", err)
	}
	t.Fatalf("task detail did not reach completed state: %#v", detail)
}

func TestResolveIngressDecisionApprovesUniquePendingApproval(t *testing.T) {
	_, service, _ := newTestService(t)

	task, _, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "请帮我完成跨账号任务")
	if err != nil {
		t.Fatalf("create delegation: %v", err)
	}

	decision, err := service.ResolveIngressDecision("acct-b", "owner-b", "同意")
	if err != nil {
		t.Fatalf("resolve ingress decision: %v", err)
	}
	if decision.Kind != IngressDecisionApproval {
		t.Fatalf("decision.Kind = %q, want approval", decision.Kind)
	}
	if decision.ApprovalAction != "approve" {
		t.Fatalf("decision.ApprovalAction = %q, want approve", decision.ApprovalAction)
	}
	if decision.ApprovalID != task.ApprovalID {
		t.Fatalf("decision.ApprovalID = %q, want %q", decision.ApprovalID, task.ApprovalID)
	}

	shortID := shortCode(task.ApprovalID)
	decision, err = service.ResolveIngressDecision("acct-b", "owner-b", "/approve "+shortID)
	if err != nil {
		t.Fatalf("resolve ingress decision by short id: %v", err)
	}
	if decision.ApprovalID != task.ApprovalID {
		t.Fatalf("decision.ApprovalID by short id = %q, want %q", decision.ApprovalID, task.ApprovalID)
	}
}

func TestResolveIngressDecisionClarifiesMultiplePendingApprovals(t *testing.T) {
	_, service, _ := newTestService(t)

	if _, _, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "任务一"); err != nil {
		t.Fatalf("create delegation one: %v", err)
	}
	if _, _, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "任务二"); err != nil {
		t.Fatalf("create delegation two: %v", err)
	}

	decision, err := service.ResolveIngressDecision("acct-b", "owner-b", "同意")
	if err != nil {
		t.Fatalf("resolve ingress decision: %v", err)
	}
	if decision.Kind != IngressDecisionClarify {
		t.Fatalf("decision.Kind = %q, want clarify", decision.Kind)
	}
	if decision.ClarificationText == "" {
		t.Fatal("decision.ClarificationText is empty")
	}
	if !strings.Contains(decision.ClarificationText, "批准 第一个") {
		t.Fatalf("clarification text = %q, want ordinal guidance", decision.ClarificationText)
	}
}

func TestResolveIngressDecisionApprovesByOrdinal(t *testing.T) {
	_, service, _ := newTestService(t)

	taskOne, _, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "任务一")
	if err != nil {
		t.Fatalf("create delegation one: %v", err)
	}
	taskTwo, _, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "任务二")
	if err != nil {
		t.Fatalf("create delegation two: %v", err)
	}

	decision, err := service.ResolveIngressDecision("acct-b", "owner-b", "批准 第一个")
	if err != nil {
		t.Fatalf("resolve ingress decision: %v", err)
	}
	if decision.Kind != IngressDecisionApproval || decision.ApprovalAction != "approve" {
		t.Fatalf("decision = %#v, want approve decision", decision)
	}
	if decision.ApprovalID != taskTwo.ApprovalID {
		t.Fatalf("decision.ApprovalID = %q, want latest %q", decision.ApprovalID, taskTwo.ApprovalID)
	}

	rejectDecision, err := service.ResolveIngressDecision("acct-b", "owner-b", "拒绝 第二个")
	if err != nil {
		t.Fatalf("resolve reject decision: %v", err)
	}
	if rejectDecision.Kind != IngressDecisionApproval || rejectDecision.ApprovalAction != "reject" {
		t.Fatalf("rejectDecision = %#v, want reject decision", rejectDecision)
	}
	if rejectDecision.ApprovalID != taskOne.ApprovalID {
		t.Fatalf("rejectDecision.ApprovalID = %q, want older %q", rejectDecision.ApprovalID, taskOne.ApprovalID)
	}
}

func TestExpandIngressCommandsHandlesBatchNaturalLanguage(t *testing.T) {
	_, service, _ := newTestService(t)

	if _, _, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "任务一"); err != nil {
		t.Fatalf("create delegation one: %v", err)
	}
	if _, _, err := service.CreateDelegation(context.Background(), "acct-a", "owner-a", "acct-b", "任务二"); err != nil {
		t.Fatalf("create delegation two: %v", err)
	}

	commands, err := service.ExpandIngressCommands("acct-b", "批准 第一个，其他都拒绝")
	if err != nil {
		t.Fatalf("expand ingress commands: %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("len(commands) = %d, want 2, commands=%#v", len(commands), commands)
	}
	if !strings.HasPrefix(commands[0], "/approve ") || !strings.HasPrefix(commands[1], "/reject ") {
		t.Fatalf("commands = %#v, want approve/reject", commands)
	}
}

func TestExpandIngressCommandsSplitsMultipleSlashCommands(t *testing.T) {
	_, service, _ := newTestService(t)

	commands, err := service.ExpandIngressCommands("acct-b", "/reject aa111111 不批准 /reject bb222222 先不处理")
	if err != nil {
		t.Fatalf("expand ingress commands: %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("len(commands) = %d, want 2, commands=%#v", len(commands), commands)
	}
}
