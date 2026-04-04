package controlplane

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type CreateTaskInput struct {
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
}

type AppendHistoryInput struct {
	TaskID         string
	Actor          string
	ActorAccountID string
	Kind           string
	Message        string
	Metadata       map[string]any
}

type AppendAuditInput struct {
	TaskID    string
	AccountID string
	Category  string
	Message   string
	Metadata  map[string]any
}

var defaultCapabilities = []CapabilityDefinition{
	{
		ID:                    "owner-task-intake",
		Name:                  "接收用户任务",
		Description:           "接收用户通过 IM 或 A2A 发起的任务，并建立任务上下文。",
		InputModes:            []string{"text/plain"},
		OutputModes:           []string{"text/plain"},
		RiskLevel:             "low",
		RequiresAuthorization: false,
		ImplementationHint:    "weclaw-handler",
	},
	{
		ID:                    "local-agent-execution",
		Name:                  "本地 Agent 执行",
		Description:           "调用底层模型 Agent 处理代码、文档、规划等任务。",
		InputModes:            []string{"text/plain"},
		OutputModes:           []string{"text/plain", "text/markdown"},
		RiskLevel:             "medium",
		RequiresAuthorization: false,
		ImplementationHint:    "configured-base-agent",
	},
	{
		ID:                    "cross-user-task-delegation",
		Name:                  "跨用户协作",
		Description:           "将任务委托给其他用户 Agent，在授权后完成协作。",
		InputModes:            []string{"text/plain"},
		OutputModes:           []string{"text/plain", "text/markdown"},
		RiskLevel:             "high",
		RequiresAuthorization: true,
		ImplementationHint:    "a2a-task-delegation",
	},
	{
		ID:                    "wechat-notification-delivery",
		Name:                  "微信通知",
		Description:           "通过绑定账号向用户发送任务状态、授权请求和结果。",
		InputModes:            []string{"text/plain"},
		OutputModes:           []string{"text/plain"},
		RiskLevel:             "medium",
		RequiresAuthorization: false,
		ImplementationHint:    "ilink-send-text",
	},
	{
		ID:                    "task-supervision-and-audit",
		Name:                  "任务监督与审计",
		Description:           "记录任务过程、审计日志，并在管理页展示。",
		InputModes:            []string{"application/json"},
		OutputModes:           []string{"application/json", "text/plain"},
		RiskLevel:             "low",
		RequiresAuthorization: false,
		ImplementationHint:    "console-audit-log",
	},
	{
		ID:                    "agent-card-publishing",
		Name:                  "A2A Card 发布",
		Description:           "生成并发布用户主 Agent 的 A2A Card。",
		InputModes:            []string{"application/json"},
		OutputModes:           []string{"application/json"},
		RiskLevel:             "low",
		RequiresAuthorization: false,
		ImplementationHint:    "a2a-agent-card",
	},
}

func DefaultDBPath() (string, error) {
	if custom := os.Getenv("WECLAW_A2A_DB_PATH"); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "a2a-control.db"), nil
}

func Open(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = DefaultDBPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create control db dir: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) init() error {
	statements := []string{
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE IF NOT EXISTS user_agents (
			account_id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			description TEXT NOT NULL,
			owner_contact_id TEXT NOT NULL DEFAULT '',
			base_agent_name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS capabilities (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL,
			input_modes_json TEXT NOT NULL,
			output_modes_json TEXT NOT NULL,
			risk_level TEXT NOT NULL,
			requires_authorization INTEGER NOT NULL,
			implementation_hint TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS capability_bindings (
			account_id TEXT NOT NULL,
			capability_id TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			PRIMARY KEY (account_id, capability_id)
		);`,
		`CREATE TABLE IF NOT EXISTS a2a_tasks (
			id TEXT PRIMARY KEY,
			context_id TEXT NOT NULL,
			requester_account_id TEXT NOT NULL,
			requester_contact_id TEXT NOT NULL,
			target_account_id TEXT NOT NULL,
			owner_account_id TEXT NOT NULL,
			status TEXT NOT NULL,
			task_kind TEXT NOT NULL,
			title TEXT NOT NULL,
			request_text TEXT NOT NULL,
			result_text TEXT NOT NULL,
			error_text TEXT NOT NULL,
			parent_task_id TEXT NOT NULL,
			assigned_agent_name TEXT NOT NULL,
			approval_id TEXT NOT NULL,
			blocking INTEGER NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS task_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			actor TEXT NOT NULL,
			actor_account_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			message TEXT NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS approvals (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			requester_account_id TEXT NOT NULL,
			approver_account_id TEXT NOT NULL,
			status TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_reason TEXT NOT NULL,
			resolved_reason TEXT NOT NULL,
			created_at TEXT NOT NULL,
			resolved_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			category TEXT NOT NULL,
			message TEXT NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
	}

	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return s.seedCapabilities()
}

func (s *Store) seedCapabilities() error {
	for _, capability := range defaultCapabilities {
		inputModes, err := marshalJSON(capability.InputModes)
		if err != nil {
			return err
		}
		outputModes, err := marshalJSON(capability.OutputModes)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`INSERT INTO capabilities (id, name, description, input_modes_json, output_modes_json, risk_level, requires_authorization, implementation_hint)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
			   name = excluded.name,
			   description = excluded.description,
			   input_modes_json = excluded.input_modes_json,
			   output_modes_json = excluded.output_modes_json,
			   risk_level = excluded.risk_level,
			   requires_authorization = excluded.requires_authorization,
			   implementation_hint = excluded.implementation_hint`,
			capability.ID,
			capability.Name,
			capability.Description,
			inputModes,
			outputModes,
			capability.RiskLevel,
			boolToInt(capability.RequiresAuthorization),
			capability.ImplementationHint,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SyncAccounts(accountOwners map[string]string, defaultAgent string) error {
	accountIDs := make([]string, 0, len(accountOwners))
	for accountID := range accountOwners {
		accountIDs = append(accountIDs, accountID)
	}
	trimmed := make([]string, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		accountID = strings.TrimSpace(accountID)
		if accountID != "" {
			trimmed = append(trimmed, accountID)
		}
	}
	sort.Strings(trimmed)

	bindings, err := s.ListCapabilities()
	if err != nil {
		return err
	}

	now := nowString()
	for _, accountID := range trimmed {
		displayName := accountID
		ownerContactID := strings.TrimSpace(accountOwners[accountID])
		if _, err := s.db.Exec(
			`INSERT INTO user_agents (account_id, display_name, description, owner_contact_id, base_agent_name, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(account_id) DO UPDATE SET
			   display_name = CASE WHEN user_agents.display_name = '' THEN excluded.display_name ELSE user_agents.display_name END,
			   description = CASE WHEN user_agents.description = '' THEN excluded.description ELSE user_agents.description END,
			   owner_contact_id = CASE WHEN user_agents.owner_contact_id = '' THEN excluded.owner_contact_id ELSE user_agents.owner_contact_id END,
			   base_agent_name = CASE WHEN user_agents.base_agent_name = '' THEN excluded.base_agent_name ELSE user_agents.base_agent_name END,
			   updated_at = excluded.updated_at`,
			accountID,
			displayName,
			"用户主 Agent",
			ownerContactID,
			defaultAgent,
			now,
			now,
		); err != nil {
			return err
		}

		for _, capability := range bindings {
			if _, err := s.db.Exec(
				`INSERT OR IGNORE INTO capability_bindings (account_id, capability_id, enabled) VALUES (?, ?, 1)`,
				accountID,
				capability.ID,
			); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Store) ListProfiles() ([]UserAgentProfile, error) {
	rows, err := s.db.Query(
		`SELECT account_id, display_name, description, owner_contact_id, base_agent_name, created_at, updated_at
		 FROM user_agents
		 ORDER BY account_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []UserAgentProfile
	for rows.Next() {
		var profile UserAgentProfile
		if err := rows.Scan(
			&profile.AccountID,
			&profile.DisplayName,
			&profile.Description,
			&profile.OwnerContactID,
			&profile.BaseAgentName,
			&profile.CreatedAt,
			&profile.UpdatedAt,
		); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) GetProfile(accountID string) (*UserAgentProfile, error) {
	var profile UserAgentProfile
	err := s.db.QueryRow(
		`SELECT account_id, display_name, description, owner_contact_id, base_agent_name, created_at, updated_at
		 FROM user_agents WHERE account_id = ?`,
		accountID,
	).Scan(
		&profile.AccountID,
		&profile.DisplayName,
		&profile.Description,
		&profile.OwnerContactID,
		&profile.BaseAgentName,
		&profile.CreatedAt,
		&profile.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *Store) UpdateProfile(input UpdateProfileInput) error {
	_, err := s.db.Exec(
		`UPDATE user_agents
		 SET display_name = ?, description = ?, owner_contact_id = ?, base_agent_name = ?, updated_at = ?
		 WHERE account_id = ?`,
		strings.TrimSpace(input.DisplayName),
		strings.TrimSpace(input.Description),
		strings.TrimSpace(input.OwnerContactID),
		strings.TrimSpace(input.BaseAgentName),
		nowString(),
		input.AccountID,
	)
	return err
}

func (s *Store) ListCapabilities() ([]CapabilityDefinition, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, input_modes_json, output_modes_json, risk_level, requires_authorization, implementation_hint
		 FROM capabilities
		 ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CapabilityDefinition
	for rows.Next() {
		var (
			capability   CapabilityDefinition
			inputModes   string
			outputModes  string
			requiresAuth int
		)
		if err := rows.Scan(
			&capability.ID,
			&capability.Name,
			&capability.Description,
			&inputModes,
			&outputModes,
			&capability.RiskLevel,
			&requiresAuth,
			&capability.ImplementationHint,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(inputModes), &capability.InputModes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(outputModes), &capability.OutputModes); err != nil {
			return nil, err
		}
		capability.RequiresAuthorization = requiresAuth == 1
		result = append(result, capability)
	}
	return result, rows.Err()
}

func (s *Store) ListCapabilityBindings(accountID string) ([]CapabilityBinding, error) {
	rows, err := s.db.Query(
		`SELECT b.account_id, b.capability_id, b.enabled, c.name, c.description, c.risk_level, c.requires_authorization, c.implementation_hint, c.input_modes_json, c.output_modes_json
		 FROM capability_bindings b
		 JOIN capabilities c ON c.id = b.capability_id
		 WHERE b.account_id = ?
		 ORDER BY b.capability_id`,
		accountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CapabilityBinding
	for rows.Next() {
		var (
			binding      CapabilityBinding
			enabled      int
			requiresAuth int
			inputModes   string
			outputModes  string
		)
		if err := rows.Scan(
			&binding.AccountID,
			&binding.CapabilityID,
			&enabled,
			&binding.Name,
			&binding.Description,
			&binding.RiskLevel,
			&requiresAuth,
			&binding.ImplementationHint,
			&inputModes,
			&outputModes,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(inputModes), &binding.InputModes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(outputModes), &binding.OutputModes); err != nil {
			return nil, err
		}
		binding.Enabled = enabled == 1
		binding.RequiresAuthorization = requiresAuth == 1
		result = append(result, binding)
	}
	return result, rows.Err()
}

func (s *Store) ReplaceCapabilityBindings(accountID string, capabilityIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE capability_bindings SET enabled = 0 WHERE account_id = ?`, accountID); err != nil {
		return err
	}
	for _, capabilityID := range capabilityIDs {
		if _, err := tx.Exec(
			`INSERT INTO capability_bindings (account_id, capability_id, enabled)
			 VALUES (?, ?, 1)
			 ON CONFLICT(account_id, capability_id) DO UPDATE SET enabled = 1`,
			accountID,
			capabilityID,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) PutTask(task *TaskRecord) error {
	metadataJSON, err := marshalJSON(task.Metadata)
	if err != nil {
		return err
	}
	if task.CreatedAt == "" {
		task.CreatedAt = nowString()
	}
	task.UpdatedAt = nowString()
	_, err = s.db.Exec(
		`INSERT INTO a2a_tasks (
			id, context_id, requester_account_id, requester_contact_id, target_account_id, owner_account_id,
			status, task_kind, title, request_text, result_text, error_text, parent_task_id, assigned_agent_name,
			approval_id, blocking, metadata_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			context_id = excluded.context_id,
			requester_account_id = excluded.requester_account_id,
			requester_contact_id = excluded.requester_contact_id,
			target_account_id = excluded.target_account_id,
			owner_account_id = excluded.owner_account_id,
			status = excluded.status,
			task_kind = excluded.task_kind,
			title = excluded.title,
			request_text = excluded.request_text,
			result_text = excluded.result_text,
			error_text = excluded.error_text,
			parent_task_id = excluded.parent_task_id,
			assigned_agent_name = excluded.assigned_agent_name,
			approval_id = excluded.approval_id,
			blocking = excluded.blocking,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at`,
		task.ID,
		task.ContextID,
		task.RequesterAccountID,
		task.RequesterContactID,
		task.TargetAccountID,
		task.OwnerAccountID,
		task.Status,
		task.TaskKind,
		task.Title,
		task.RequestText,
		task.ResultText,
		task.ErrorText,
		task.ParentTaskID,
		task.AssignedAgentName,
		task.ApprovalID,
		boolToInt(task.Blocking),
		metadataJSON,
		task.CreatedAt,
		task.UpdatedAt,
	)
	return err
}

func (s *Store) CreateTask(input CreateTaskInput) (*TaskRecord, error) {
	task := &TaskRecord{
		ID:                 input.ID,
		ContextID:          input.ContextID,
		RequesterAccountID: input.RequesterAccountID,
		RequesterContactID: input.RequesterContactID,
		TargetAccountID:    input.TargetAccountID,
		OwnerAccountID:     input.OwnerAccountID,
		Status:             input.Status,
		TaskKind:           input.TaskKind,
		Title:              input.Title,
		RequestText:        input.RequestText,
		ResultText:         input.ResultText,
		ErrorText:          input.ErrorText,
		ParentTaskID:       input.ParentTaskID,
		AssignedAgentName:  input.AssignedAgentName,
		ApprovalID:         input.ApprovalID,
		Blocking:           input.Blocking,
		Metadata:           input.Metadata,
	}
	if err := s.PutTask(task); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) GetTask(taskID string, historyLimit int) (*TaskRecord, error) {
	row := s.db.QueryRow(
		`SELECT id, context_id, requester_account_id, requester_contact_id, target_account_id, owner_account_id,
		        status, task_kind, title, request_text, result_text, error_text, parent_task_id,
		        assigned_agent_name, approval_id, blocking, metadata_json, created_at, updated_at
		 FROM a2a_tasks WHERE id = ?`,
		taskID,
	)

	var (
		task         TaskRecord
		blocking     int
		metadataJSON string
	)
	err := row.Scan(
		&task.ID,
		&task.ContextID,
		&task.RequesterAccountID,
		&task.RequesterContactID,
		&task.TargetAccountID,
		&task.OwnerAccountID,
		&task.Status,
		&task.TaskKind,
		&task.Title,
		&task.RequestText,
		&task.ResultText,
		&task.ErrorText,
		&task.ParentTaskID,
		&task.AssignedAgentName,
		&task.ApprovalID,
		&blocking,
		&metadataJSON,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	task.Blocking = blocking == 1
	if err := json.Unmarshal([]byte(metadataJSON), &task.Metadata); err != nil {
		return nil, err
	}
	task.History, err = s.ListTaskHistory(taskID, historyLimit)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) ListTasks(limit int) ([]TaskRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, context_id, requester_account_id, requester_contact_id, target_account_id, owner_account_id,
		        status, task_kind, title, request_text, result_text, error_text, parent_task_id,
		        assigned_agent_name, approval_id, blocking, metadata_json, created_at, updated_at
		 FROM a2a_tasks
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TaskRecord
	for rows.Next() {
		var (
			task         TaskRecord
			blocking     int
			metadataJSON string
		)
		if err := rows.Scan(
			&task.ID,
			&task.ContextID,
			&task.RequesterAccountID,
			&task.RequesterContactID,
			&task.TargetAccountID,
			&task.OwnerAccountID,
			&task.Status,
			&task.TaskKind,
			&task.Title,
			&task.RequestText,
			&task.ResultText,
			&task.ErrorText,
			&task.ParentTaskID,
			&task.AssignedAgentName,
			&task.ApprovalID,
			&blocking,
			&metadataJSON,
			&task.CreatedAt,
			&task.UpdatedAt,
		); err != nil {
			return nil, err
		}
		task.Blocking = blocking == 1
		if err := json.Unmarshal([]byte(metadataJSON), &task.Metadata); err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

func (s *Store) AppendTaskHistory(input AppendHistoryInput) error {
	metadataJSON, err := marshalJSON(input.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO task_history (task_id, actor, actor_account_id, kind, message, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		input.TaskID,
		input.Actor,
		input.ActorAccountID,
		input.Kind,
		input.Message,
		metadataJSON,
		nowString(),
	)
	return err
}

func (s *Store) ListTaskHistory(taskID string, limit int) ([]TaskHistoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, task_id, actor, actor_account_id, kind, message, metadata_json, created_at
		 FROM task_history
		 WHERE task_id = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		taskID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []TaskHistoryEntry
	for rows.Next() {
		var (
			entry        TaskHistoryEntry
			metadataJSON string
		)
		if err := rows.Scan(
			&entry.ID,
			&entry.TaskID,
			&entry.Actor,
			&entry.ActorAccountID,
			&entry.Kind,
			&entry.Message,
			&metadataJSON,
			&entry.CreatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metadataJSON), &entry.Metadata); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, rows.Err()
}

func (s *Store) PutApproval(grant *AuthorizationGrant) error {
	if grant.CreatedAt == "" {
		grant.CreatedAt = nowString()
	}
	_, err := s.db.Exec(
		`INSERT INTO approvals (id, task_id, requester_account_id, approver_account_id, status, scope, request_reason, resolved_reason, created_at, resolved_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   task_id = excluded.task_id,
		   requester_account_id = excluded.requester_account_id,
		   approver_account_id = excluded.approver_account_id,
		   status = excluded.status,
		   scope = excluded.scope,
		   request_reason = excluded.request_reason,
		   resolved_reason = excluded.resolved_reason,
		   resolved_at = excluded.resolved_at`,
		grant.ID,
		grant.TaskID,
		grant.RequesterAccountID,
		grant.ApproverAccountID,
		grant.Status,
		grant.Scope,
		grant.RequestReason,
		grant.ResolvedReason,
		grant.CreatedAt,
		grant.ResolvedAt,
	)
	return err
}

func (s *Store) GetApproval(approvalID string) (*AuthorizationGrant, error) {
	var grant AuthorizationGrant
	err := s.db.QueryRow(
		`SELECT id, task_id, requester_account_id, approver_account_id, status, scope, request_reason, resolved_reason, created_at, resolved_at
		 FROM approvals WHERE id = ?`,
		approvalID,
	).Scan(
		&grant.ID,
		&grant.TaskID,
		&grant.RequesterAccountID,
		&grant.ApproverAccountID,
		&grant.Status,
		&grant.Scope,
		&grant.RequestReason,
		&grant.ResolvedReason,
		&grant.CreatedAt,
		&grant.ResolvedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

func (s *Store) ListApprovals(status string, limit int) ([]AuthorizationGrant, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT id, task_id, requester_account_id, approver_account_id, status, scope, request_reason, resolved_reason, created_at, resolved_at
	          FROM approvals`
	args := []any{}
	if strings.TrimSpace(status) != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []AuthorizationGrant
	for rows.Next() {
		var grant AuthorizationGrant
		if err := rows.Scan(
			&grant.ID,
			&grant.TaskID,
			&grant.RequesterAccountID,
			&grant.ApproverAccountID,
			&grant.Status,
			&grant.Scope,
			&grant.RequestReason,
			&grant.ResolvedReason,
			&grant.CreatedAt,
			&grant.ResolvedAt,
		); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func (s *Store) AppendAudit(input AppendAuditInput) error {
	metadataJSON, err := marshalJSON(input.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO audit_log (task_id, account_id, category, message, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		input.TaskID,
		input.AccountID,
		input.Category,
		input.Message,
		metadataJSON,
		nowString(),
	)
	return err
}

func (s *Store) ListAudit(limit int) ([]AuditRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, task_id, account_id, category, message, metadata_json, created_at
		 FROM audit_log
		 ORDER BY id DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AuditRecord
	for rows.Next() {
		var (
			record       AuditRecord
			metadataJSON string
		)
		if err := rows.Scan(
			&record.ID,
			&record.TaskID,
			&record.AccountID,
			&record.Category,
			&record.Message,
			&metadataJSON,
			&record.CreatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metadataJSON), &record.Metadata); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func marshalJSON(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
