package controlplane

import (
	"context"
	"time"
)

const (
	TaskStatusSubmitted       = "submitted"
	TaskStatusWorking         = "working"
	TaskStatusWaitingApproval = "waiting-approval"
	TaskStatusCompleted       = "completed"
	TaskStatusFailed          = "failed"
	TaskStatusCanceled        = "canceled"
	TaskStatusRejected        = "rejected"

	ApprovalStatusPending  = "pending"
	ApprovalStatusApproved = "approved"
	ApprovalStatusRejected = "rejected"
)

type UserAgentProfile struct {
	AccountID              string
	DisplayName            string
	Description            string
	OwnerContactID         string
	BaseAgentName          string
	SpecializationTags     []string
	SpecializationExamples []string
	SpecializationAvoid    []string
	DelegationEnabled      bool
	CreatedAt              string
	UpdatedAt              string
}

type CapabilityDefinition struct {
	ID                    string
	Name                  string
	Description           string
	InputModes            []string
	OutputModes           []string
	RiskLevel             string
	RequiresAuthorization bool
	ImplementationHint    string
	RoutingTags           []string
}

type CapabilityBinding struct {
	AccountID             string
	CapabilityID          string
	Enabled               bool
	Name                  string
	Description           string
	RiskLevel             string
	RequiresAuthorization bool
	ImplementationHint    string
	InputModes            []string
	OutputModes           []string
	RoutingTags           []string
}

type TaskHistoryEntry struct {
	ID             int64
	TaskID         string
	Actor          string
	ActorAccountID string
	Kind           string
	Message        string
	Metadata       map[string]any
	CreatedAt      string
}

type TaskRecord struct {
	ID                 string
	ContextID          string
	RequesterAccountID string
	RequesterContactID string
	TargetAccountID    string
	OwnerAccountID     string
	Status             string
	TaskKind           string
	Title              string
	RequestText        string
	ResultText         string
	ErrorText          string
	ParentTaskID       string
	AssignedAgentName  string
	ApprovalID         string
	Blocking           bool
	Metadata           map[string]any
	History            []TaskHistoryEntry
	CreatedAt          string
	UpdatedAt          string
}

type AuthorizationGrant struct {
	ID                 string
	TaskID             string
	RequesterAccountID string
	ApproverAccountID  string
	Status             string
	Scope              string
	RequestReason      string
	ResolvedReason     string
	CreatedAt          string
	ResolvedAt         string
}

type AuditRecord struct {
	ID        int64
	TaskID    string
	AccountID string
	Category  string
	Message   string
	Metadata  map[string]any
	CreatedAt string
}

type ChatResult struct {
	Reply     string
	AgentName string
	Model     string
}

type OwnerTaskDecision struct {
	Action          string `json:"action"`
	TargetAccountID string `json:"target_account_id,omitempty"`
	Message         string `json:"message,omitempty"`
	Rationale       string `json:"rationale,omitempty"`
}

type ActiveContextKind string

const (
	ActiveContextApproval ActiveContextKind = "approval"
)

type ActiveContext struct {
	Kind          ActiveContextKind `json:"kind"`
	DisplayID     string            `json:"display_id,omitempty"`
	TaskID        string            `json:"task_id"`
	ApprovalID    string            `json:"approval_id,omitempty"`
	Title         string            `json:"title"`
	Status        string            `json:"status"`
	WaitingReason string            `json:"waiting_reason,omitempty"`
	UpdatedAt     string            `json:"updated_at"`
}

type IngressDecisionKind string

const (
	IngressDecisionApproval IngressDecisionKind = "approval_reply"
	IngressDecisionNewTask  IngressDecisionKind = "new_task"
	IngressDecisionClarify  IngressDecisionKind = "clarify"
)

type IngressDecision struct {
	Kind              IngressDecisionKind `json:"kind"`
	ApprovalID        string              `json:"approval_id,omitempty"`
	ApprovalAction    string              `json:"approval_action,omitempty"`
	ApprovalReason    string              `json:"approval_reason,omitempty"`
	ClarificationText string              `json:"clarification_text,omitempty"`
	Reason            string              `json:"reason,omitempty"`
	ActiveContexts    []ActiveContext     `json:"active_contexts,omitempty"`
}

type ChatFunc func(ctx context.Context, conversationID, message, agentName string) (ChatResult, error)

type SendTextFunc func(ctx context.Context, accountID, toUserID, text, contextToken string) error

type StoreSnapshot struct {
	Profiles  []UserAgentProfile
	Bindings  map[string][]CapabilityBinding
	Tasks     []TaskRecord
	Approvals []AuthorizationGrant
	Audit     []AuditRecord
}

type UpdateProfileInput struct {
	AccountID              string
	DisplayName            string
	Description            string
	OwnerContactID         string
	BaseAgentName          string
	SpecializationTags     []string
	SpecializationExamples []string
	SpecializationAvoid    []string
	DelegationEnabled      bool
}

type WorkflowGraph struct {
	TaskID        string         `json:"task_id"`
	Title         string         `json:"title"`
	Status        string         `json:"status"`
	BlockedReason string         `json:"blocked_reason,omitempty"`
	Nodes         []WorkflowNode `json:"nodes"`
	Edges         []WorkflowEdge `json:"edges"`
}

type WorkflowNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Order  int    `json:"order"`
}

type WorkflowEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

type TaskDetail struct {
	Task     *TaskRecord
	Parent   *TaskRecord
	Children []TaskRecord
	Approval *AuthorizationGrant
	Audit    []AuditRecord
	Workflow *WorkflowGraph
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339)
}
