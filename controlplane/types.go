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
	AccountID      string
	DisplayName    string
	Description    string
	OwnerContactID string
	BaseAgentName  string
	CreatedAt      string
	UpdatedAt      string
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
	AccountID      string
	DisplayName    string
	Description    string
	OwnerContactID string
	BaseAgentName  string
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339)
}
