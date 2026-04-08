package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/google/uuid"
)

type Service struct {
	store               *Store
	publicBaseURL       string
	chat                ChatFunc
	sendText            SendTextFunc
	mu                  sync.RWMutex
	knownAccounts       map[string]struct{}
	availableBaseAgents []string
}

func NewService(store *Store, publicBaseURL string, chat ChatFunc, sendText SendTextFunc) *Service {
	return &Service{
		store:         store,
		publicBaseURL: strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
		chat:          chat,
		sendText:      sendText,
		knownAccounts: make(map[string]struct{}),
	}
}

func (s *Service) SetAvailableBaseAgents(names []string) {
	unique := make(map[string]struct{}, len(names))
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := unique[name]; ok {
			continue
		}
		unique[name] = struct{}{}
		filtered = append(filtered, name)
	}
	sort.Strings(filtered)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.availableBaseAgents = filtered
}

func (s *Service) AvailableBaseAgents() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.availableBaseAgents))
	copy(out, s.availableBaseAgents)
	return out
}

func (s *Service) SyncAccounts(accountOwners map[string]string, defaultAgent string) error {
	if err := s.store.SyncAccounts(accountOwners, defaultAgent); err != nil {
		return err
	}

	known := make(map[string]struct{}, len(accountOwners))
	for accountID := range accountOwners {
		accountID = strings.TrimSpace(accountID)
		if accountID != "" {
			known[accountID] = struct{}{}
		}
	}

	s.mu.Lock()
	s.knownAccounts = known
	s.mu.Unlock()
	return nil
}

func (s *Service) IsKnownAccount(accountID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.knownAccounts[accountID]
	return ok
}

func (s *Service) IsOwnerContact(accountID, fromUserID string) bool {
	profile, err := s.store.GetProfile(accountID)
	if err != nil || profile == nil {
		return false
	}
	return strings.TrimSpace(profile.OwnerContactID) != "" && profile.OwnerContactID == strings.TrimSpace(fromUserID)
}

func (s *Service) ListProfiles() ([]UserAgentProfile, error) {
	return s.store.ListProfiles()
}

func (s *Service) GetProfile(accountID string) (*UserAgentProfile, error) {
	return s.store.GetProfile(accountID)
}

func (s *Service) UpdateProfile(input UpdateProfileInput) error {
	return s.store.UpdateProfile(input)
}

func (s *Service) ListCapabilityBindings(accountID string) ([]CapabilityBinding, error) {
	return s.store.ListCapabilityBindings(accountID)
}

func (s *Service) ReplaceCapabilityBindings(accountID string, capabilityIDs []string) error {
	return s.store.ReplaceCapabilityBindings(accountID, capabilityIDs)
}

func (s *Service) ListCapabilities() ([]CapabilityDefinition, error) {
	return s.store.ListCapabilities()
}

func (s *Service) FindAccount(query string) (*UserAgentProfile, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return nil, err
	}

	lower := strings.ToLower(query)
	for _, profile := range profiles {
		if profile.AccountID == query {
			return &profile, nil
		}
	}
	for _, profile := range profiles {
		if strings.EqualFold(profile.DisplayName, query) || strings.ToLower(profile.AccountID) == lower {
			return &profile, nil
		}
	}
	for _, profile := range profiles {
		if strings.Contains(strings.ToLower(profile.DisplayName), lower) || strings.Contains(strings.ToLower(profile.AccountID), lower) {
			return &profile, nil
		}
	}
	return nil, nil
}

func (s *Service) BuildAgentCard(accountID string) (*AgentCard, error) {
	profile, err := s.store.GetProfile(accountID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, nil
	}

	bindings, err := s.store.ListCapabilityBindings(accountID)
	if err != nil {
		return nil, err
	}

	card := &AgentCard{
		ProtocolVersion:    "0.3.0",
		Name:               firstNonEmpty(profile.DisplayName, profile.AccountID),
		Description:        firstNonEmpty(profile.Description, "WeClaw 用户主 Agent"),
		URL:                s.userServiceURL(profile.AccountID),
		Version:            "v1",
		PreferredTransport: "JSONRPC",
		Provider: AgentProvider{
			Organization: "WeClaw",
			URL:          s.publicBaseURL,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain", "text/markdown"},
		Capabilities: AgentCapabilities{
			Streaming:              false,
			PushNotifications:      false,
			StateTransitionHistory: true,
		},
		Metadata: map[string]any{
			"specializationTags":     profile.SpecializationTags,
			"specializationExamples": profile.SpecializationExamples,
			"specializationAvoid":    profile.SpecializationAvoid,
			"delegationEnabled":      profile.DelegationEnabled,
		},
	}

	for _, binding := range bindings {
		if !binding.Enabled {
			continue
		}
		card.Skills = append(card.Skills, AgentSkill{
			ID:          binding.CapabilityID,
			Name:        binding.Name,
			Description: binding.Description,
			Tags:        []string{binding.RiskLevel, binding.ImplementationHint},
			InputModes:  binding.InputModes,
			OutputModes: binding.OutputModes,
		})
	}
	return card, nil
}

func (s *Service) BuildAllCards() (map[string]*AgentCard, error) {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*AgentCard, len(profiles))
	for _, profile := range profiles {
		card, err := s.BuildAgentCard(profile.AccountID)
		if err != nil {
			return nil, err
		}
		result[profile.AccountID] = card
	}
	return result, nil
}

func (s *Service) Snapshot() (*StoreSnapshot, error) {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return nil, err
	}
	bindings := make(map[string][]CapabilityBinding, len(profiles))
	for _, profile := range profiles {
		items, err := s.store.ListCapabilityBindings(profile.AccountID)
		if err != nil {
			return nil, err
		}
		bindings[profile.AccountID] = items
	}
	tasks, err := s.store.ListTasks(100)
	if err != nil {
		return nil, err
	}
	approvals, err := s.store.ListApprovals("", 100)
	if err != nil {
		return nil, err
	}
	audit, err := s.store.ListAudit(200)
	if err != nil {
		return nil, err
	}
	return &StoreSnapshot{
		Profiles:  profiles,
		Bindings:  bindings,
		Tasks:     tasks,
		Approvals: approvals,
		Audit:     audit,
	}, nil
}

func (s *Service) GetTask(taskID string, historyLimit int) (*TaskRecord, error) {
	return s.store.GetTask(taskID, historyLimit)
}

func (s *Service) GetTaskDetail(taskID string, historyLimit int) (*TaskDetail, error) {
	task, err := s.store.GetTask(taskID, historyLimit)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}

	var parent *TaskRecord
	if task.ParentTaskID != "" {
		parent, err = s.store.GetTask(task.ParentTaskID, historyLimit)
		if err != nil {
			return nil, err
		}
	}

	children, err := s.store.ListChildTasks(task.ID)
	if err != nil {
		return nil, err
	}
	approval, err := s.GetApproval(task.ApprovalID)
	if err != nil {
		return nil, err
	}
	audit, err := s.store.ListTaskAudit(task.ID, 100)
	if err != nil {
		return nil, err
	}

	return &TaskDetail{
		Task:     task,
		Parent:   parent,
		Children: children,
		Approval: approval,
		Audit:    audit,
		Workflow: buildWorkflowGraph(task, approval, children),
	}, nil
}

func (s *Service) GetApproval(approvalID string) (*AuthorizationGrant, error) {
	if strings.TrimSpace(approvalID) == "" {
		return nil, nil
	}
	return s.store.GetApproval(approvalID)
}

func (s *Service) ListActiveContexts(accountID string, limit int) ([]ActiveContext, error) {
	if limit <= 0 {
		limit = 10
	}
	approvals, err := s.store.ListApprovalsForApprover(accountID, ApprovalStatusPending, limit)
	if err != nil {
		return nil, err
	}
	contexts := make([]ActiveContext, 0, len(approvals))
	for _, approval := range approvals {
		taskTitle := approval.RequestReason
		if task, taskErr := s.store.GetTask(approval.TaskID, 0); taskErr == nil && task != nil {
			taskTitle = firstNonEmpty(task.Title, approval.RequestReason)
		}
		contexts = append(contexts, ActiveContext{
			Kind:          ActiveContextApproval,
			DisplayID:     shortCode(approval.ID),
			TaskID:        approval.TaskID,
			ApprovalID:    approval.ID,
			Title:         taskTitle,
			Status:        approval.Status,
			WaitingReason: "等待审批",
			UpdatedAt:     approval.CreatedAt,
		})
	}
	return contexts, nil
}

func (s *Service) ResolveIngressDecision(accountID, requesterContactID, text string) (IngressDecision, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return IngressDecision{Kind: IngressDecisionNewTask}, nil
	}

	activeContexts, err := s.ListActiveContexts(accountID, 10)
	if err != nil {
		return IngressDecision{}, err
	}
	normalized := normalizeIngressText(trimmed)

	if approvalID, extra, ok := explicitApprovalCommand(trimmed, "/approve"); ok {
		resolvedID, resolveErr := s.resolveApprovalReference(accountID, approvalID, activeContexts)
		if resolveErr == nil {
			return IngressDecision{
				Kind:           IngressDecisionApproval,
				ApprovalID:     resolvedID,
				ApprovalAction: "approve",
				ApprovalReason: extra,
				Reason:         "显式审批命令",
			}, nil
		}
		return IngressDecision{
			Kind:              IngressDecisionClarify,
			ClarificationText: resolveErr.Error(),
			Reason:            "审批编号未命中",
			ActiveContexts:    activeContexts,
		}, nil
	}
	if approvalID, extra, ok := explicitApprovalCommand(trimmed, "/reject"); ok {
		resolvedID, resolveErr := s.resolveApprovalReference(accountID, approvalID, activeContexts)
		if resolveErr == nil {
			return IngressDecision{
				Kind:           IngressDecisionApproval,
				ApprovalID:     resolvedID,
				ApprovalAction: "reject",
				ApprovalReason: extra,
				Reason:         "显式拒绝命令",
			}, nil
		}
		return IngressDecision{
			Kind:              IngressDecisionClarify,
			ClarificationText: resolveErr.Error(),
			Reason:            "审批编号未命中",
			ActiveContexts:    activeContexts,
		}, nil
	}

	if action, index, ok := ordinalApprovalReply(normalized); ok {
		if index <= 0 || index > len(activeContexts) {
			return IngressDecision{
				Kind:              IngressDecisionClarify,
				ClarificationText: buildApprovalClarification(activeContexts),
				Reason:            "序号超出待审批范围",
				ActiveContexts:    activeContexts,
			}, nil
		}
		return IngressDecision{
			Kind:           IngressDecisionApproval,
			ApprovalID:     activeContexts[index-1].ApprovalID,
			ApprovalAction: action,
			Reason:         "按序号命中待审批任务",
			ActiveContexts: activeContexts,
		}, nil
	}

	if looksLikeApprovalReply(normalized) {
		if len(activeContexts) == 1 {
			action := "approve"
			if looksLikeRejectReply(normalized) {
				action = "reject"
			}
			return IngressDecision{
				Kind:           IngressDecisionApproval,
				ApprovalID:     activeContexts[0].ApprovalID,
				ApprovalAction: action,
				Reason:         "唯一待审批上下文",
				ActiveContexts: activeContexts,
			}, nil
		}
		if len(activeContexts) > 1 {
			return IngressDecision{
				Kind:              IngressDecisionClarify,
				ClarificationText: buildApprovalClarification(activeContexts),
				Reason:            "多个待审批任务，拒绝猜测",
				ActiveContexts:    activeContexts,
			}, nil
		}
	}

	return IngressDecision{
		Kind:           IngressDecisionNewTask,
		Reason:         "没有命中活跃上下文",
		ActiveContexts: activeContexts,
	}, nil
}

func (s *Service) ExpandIngressCommands(accountID, text string) ([]string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}

	if commands := splitExplicitApprovalCommands(trimmed); len(commands) > 1 {
		return commands, nil
	}

	lines := splitNonEmptyLines(trimmed)
	if len(lines) > 1 {
		return lines, nil
	}

	normalized := normalizeIngressText(trimmed)
	if action, index, ok := ordinalApprovalReply(normalized); ok && strings.Contains(normalized, "其他都拒绝") {
		activeContexts, err := s.ListActiveContexts(accountID, 10)
		if err != nil {
			return nil, err
		}
		if len(activeContexts) == 0 {
			return []string{trimmed}, nil
		}
		if index <= 0 || index > len(activeContexts) {
			return []string{trimmed}, nil
		}

		var commands []string
		selected := activeContexts[index-1]
		if action == "approve" {
			commands = append(commands, "/approve "+selected.DisplayID)
		} else {
			commands = append(commands, "/reject "+selected.DisplayID+" 批量拒绝")
		}
		for idx, item := range activeContexts {
			if idx == index-1 {
				continue
			}
			commands = append(commands, "/reject "+item.DisplayID+" 批量拒绝")
		}
		return commands, nil
	}

	return []string{trimmed}, nil
}

func (s *Service) FormatTaskReply(task *TaskRecord) string {
	if task == nil {
		return ""
	}
	if strings.TrimSpace(task.ResultText) != "" {
		return task.ResultText
	}
	switch task.Status {
	case TaskStatusWaitingApproval:
		targetLabel := task.TargetAccountID
		if profile, err := s.store.GetProfile(task.TargetAccountID); err == nil && profile != nil {
			targetLabel = firstNonEmpty(profile.DisplayName, profile.AccountID)
		}
		if task.ApprovalID != "" {
			return fmt.Sprintf("已自动发起协作，正在等待 %s 批准。\n审批码：%s", targetLabel, shortCode(task.ApprovalID))
		}
		return fmt.Sprintf("已自动发起协作，正在等待 %s 批准。", targetLabel)
	case TaskStatusRejected:
		return firstNonEmpty(task.ErrorText, "协作任务已被拒绝")
	case TaskStatusFailed:
		return firstNonEmpty(task.ErrorText, "任务失败")
	case TaskStatusWorking:
		return "任务正在处理中。"
	default:
		return firstNonEmpty(task.ErrorText, task.RequestText)
	}
}

func (s *Service) SubmitOwnerTask(ctx context.Context, accountID, requesterContactID, text string) (*TaskRecord, error) {
	profile, err := s.store.GetProfile(accountID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, fmt.Errorf("unknown account: %s", accountID)
	}
	text = strings.TrimSpace(text)

	decision, err := s.decideOwnerTask(ctx, profile, requesterContactID, text)
	if err == nil && decision.Action == "delegate" && decision.TargetAccountID != "" && decision.TargetAccountID != accountID {
		task, _, createErr := s.CreateDelegation(
			ctx,
			accountID,
			requesterContactID,
			decision.TargetAccountID,
			firstNonEmpty(decision.Message, text),
		)
		if createErr == nil {
			_ = s.store.AppendTaskHistory(AppendHistoryInput{
				TaskID:         task.ID,
				Actor:          "system",
				ActorAccountID: accountID,
				Kind:           "auto-delegation-selected",
				Message:        firstNonEmpty(decision.Rationale, "主 Agent 自动选择跨用户协作"),
				Metadata: map[string]any{
					"target_account_id": decision.TargetAccountID,
				},
			})
			_ = s.store.AppendAudit(AppendAuditInput{
				TaskID:    task.ID,
				AccountID: accountID,
				Category:  "auto-delegation-selected",
				Message:   firstNonEmpty(decision.Rationale, "主 Agent 自动选择跨用户协作"),
				Metadata: map[string]any{
					"target_account_id": decision.TargetAccountID,
				},
			})
			return task, nil
		}
		_ = s.store.AppendAudit(AppendAuditInput{
			TaskID:    "",
			AccountID: accountID,
			Category:  "auto-delegation-fallback",
			Message:   createErr.Error(),
			Metadata: map[string]any{
				"target_account_id": decision.TargetAccountID,
			},
		})
	}

	task, err := s.store.CreateTask(CreateTaskInput{
		ID:                 uuid.NewString(),
		ContextID:          fmt.Sprintf("owner:%s:%s", accountID, requesterContactID),
		RequesterAccountID: accountID,
		RequesterContactID: requesterContactID,
		TargetAccountID:    accountID,
		OwnerAccountID:     accountID,
		Status:             TaskStatusSubmitted,
		TaskKind:           "owner_request",
		Title:              taskTitle(text),
		RequestText:        strings.TrimSpace(text),
		Blocking:           true,
		Metadata: map[string]any{
			"source": "wechat-owner",
		},
	})
	if err != nil {
		return nil, err
	}
	_ = s.store.AppendTaskHistory(AppendHistoryInput{
		TaskID:         task.ID,
		Actor:          "owner",
		ActorAccountID: accountID,
		Kind:           "request",
		Message:        task.RequestText,
	})
	_ = s.store.AppendAudit(AppendAuditInput{
		TaskID:    task.ID,
		AccountID: accountID,
		Category:  "task-created",
		Message:   "用户通过微信发起任务",
		Metadata: map[string]any{
			"requester_contact_id": requesterContactID,
		},
	})
	if decision.Action == "local" && strings.TrimSpace(decision.Rationale) != "" {
		_ = s.store.AppendTaskHistory(AppendHistoryInput{
			TaskID:         task.ID,
			Actor:          "system",
			ActorAccountID: accountID,
			Kind:           "auto-delegation-local-fallback",
			Message:        decision.Rationale,
		})
		_ = s.store.AppendAudit(AppendAuditInput{
			TaskID:    task.ID,
			AccountID: accountID,
			Category:  decisionAuditCategory(decision.Rationale),
			Message:   decision.Rationale,
		})
	}

	return s.processLocalTask(ctx, profile, task, false)
}

func (s *Service) decideOwnerTask(ctx context.Context, profile *UserAgentProfile, requesterContactID, text string) (OwnerTaskDecision, error) {
	decision, ok := s.heuristicOwnerTaskDecision(profile, text)
	if ok {
		return decision, nil
	}
	scores, err := s.scoreOwnerTaskCandidates(profile, text)
	if err != nil {
		return OwnerTaskDecision{Action: "local", Message: text}, err
	}
	if len(scores) == 0 {
		return OwnerTaskDecision{Action: "local", Message: text, Rationale: "没有可用的其他 Agent 专长画像，回退本地处理"}, nil
	}

	top := scores[0]
	if top.Disqualified {
		return OwnerTaskDecision{Action: "local", Message: text, Rationale: fmt.Sprintf("%s 命中禁做事项，回退本地处理", top.DisplayName)}, nil
	}
	if top.Score < 4 {
		return OwnerTaskDecision{Action: "local", Message: text, Rationale: "专长匹配分不足，回退本地处理"}, nil
	}
	if len(scores) > 1 && top.Score-scores[1].Score < 2 {
		return OwnerTaskDecision{Action: "local", Message: text, Rationale: "多个候选 Agent 匹配度过于接近，回退本地处理"}, nil
	}

	reason := fmt.Sprintf("根据专长画像与能力标签，%s 匹配分最高（%d 分）", top.DisplayName, top.Score)
	if len(top.Reasons) > 0 {
		reason += "，命中：" + strings.Join(top.Reasons, "、")
	}
	return OwnerTaskDecision{
		Action:          "delegate",
		TargetAccountID: top.AccountID,
		Message:         text,
		Rationale:       reason,
	}, nil
}

func (s *Service) heuristicOwnerTaskDecision(profile *UserAgentProfile, text string) (OwnerTaskDecision, bool) {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return OwnerTaskDecision{}, false
	}
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return OwnerTaskDecision{}, false
	}

	intentWords := []string{"让", "请", "找", "委托", "转给", "交给", "协作", "帮我联系", "帮我找", "请他", "请她", "请ta", "delegate"}
	hasIntent := false
	for _, word := range intentWords {
		if strings.Contains(normalized, strings.ToLower(word)) {
			hasIntent = true
			break
		}
	}
	if !hasIntent {
		return OwnerTaskDecision{}, false
	}

	for _, candidate := range profiles {
		if candidate.AccountID == profile.AccountID {
			continue
		}
		if !candidate.DelegationEnabled {
			continue
		}
		if strings.Contains(normalized, strings.ToLower(candidate.AccountID)) || strings.Contains(normalized, strings.ToLower(candidate.DisplayName)) {
			return OwnerTaskDecision{
				Action:          "delegate",
				TargetAccountID: candidate.AccountID,
				Message:         text,
				Rationale:       fmt.Sprintf("命中显式目标 %s，自动进入跨用户协作", firstNonEmpty(candidate.DisplayName, candidate.AccountID)),
			}, true
		}
	}
	return OwnerTaskDecision{}, false
}

func parseOwnerTaskDecision(raw string) (OwnerTaskDecision, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return OwnerTaskDecision{}, fmt.Errorf("empty route decision")
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		raw = raw[start : end+1]
	}
	var decision OwnerTaskDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return OwnerTaskDecision{}, err
	}
	decision.Action = strings.TrimSpace(decision.Action)
	decision.TargetAccountID = strings.TrimSpace(decision.TargetAccountID)
	decision.Message = strings.TrimSpace(decision.Message)
	decision.Rationale = strings.TrimSpace(decision.Rationale)
	if decision.Action == "" {
		decision.Action = "local"
	}
	return decision, nil
}

type routingScore struct {
	AccountID    string
	DisplayName  string
	Score        int
	Reasons      []string
	Disqualified bool
	Profile      UserAgentProfile
}

func (s *Service) scoreOwnerTaskCandidates(profile *UserAgentProfile, text string) ([]routingScore, error) {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return nil, err
	}
	normalized := normalizeRoutingText(text)
	if normalized == "" {
		return nil, nil
	}

	scores := make([]routingScore, 0, len(profiles))
	for _, candidate := range profiles {
		if candidate.AccountID == profile.AccountID || !candidate.DelegationEnabled {
			continue
		}
		score := routingScore{
			AccountID:   candidate.AccountID,
			DisplayName: firstNonEmpty(candidate.DisplayName, candidate.AccountID),
			Profile:     candidate,
		}

		for _, avoid := range candidate.SpecializationAvoid {
			if tokenMatch(normalized, avoid) {
				score.Disqualified = true
				score.Reasons = append(score.Reasons, "禁做:"+strings.TrimSpace(avoid))
				break
			}
		}
		if score.Disqualified {
			scores = append(scores, score)
			continue
		}

		for _, tag := range candidate.SpecializationTags {
			if tokenMatch(normalized, tag) {
				score.Score += 3
				score.Reasons = append(score.Reasons, "专长:"+strings.TrimSpace(tag))
			}
		}
		for _, example := range candidate.SpecializationExamples {
			if tokenMatch(normalized, example) {
				score.Score += 2
				score.Reasons = append(score.Reasons, "案例:"+strings.TrimSpace(example))
			}
		}

		bindings, bindingsErr := s.store.ListCapabilityBindings(candidate.AccountID)
		if bindingsErr != nil {
			return nil, bindingsErr
		}
		for _, binding := range bindings {
			if !binding.Enabled {
				continue
			}
			for _, tag := range binding.RoutingTags {
				if tokenMatch(normalized, tag) {
					score.Score++
					score.Reasons = append(score.Reasons, "能力:"+strings.TrimSpace(tag))
				}
			}
		}
		scores = append(scores, score)
	}

	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Disqualified != scores[j].Disqualified {
			return !scores[i].Disqualified
		}
		if scores[i].Score == scores[j].Score {
			return scores[i].DisplayName < scores[j].DisplayName
		}
		return scores[i].Score > scores[j].Score
	})
	return scores, nil
}

func (s *Service) CreateDelegation(ctx context.Context, sourceAccountID, requesterContactID, targetQuery, text string) (*TaskRecord, *AuthorizationGrant, error) {
	targetProfile, err := s.FindAccount(targetQuery)
	if err != nil {
		return nil, nil, err
	}
	if targetProfile == nil {
		return nil, nil, fmt.Errorf("未找到目标 Agent: %s", targetQuery)
	}

	parentTask, err := s.store.CreateTask(CreateTaskInput{
		ID:                 uuid.NewString(),
		ContextID:          fmt.Sprintf("delegate:%s:%s", sourceAccountID, requesterContactID),
		RequesterAccountID: sourceAccountID,
		RequesterContactID: requesterContactID,
		TargetAccountID:    targetProfile.AccountID,
		OwnerAccountID:     sourceAccountID,
		Status:             TaskStatusWaitingApproval,
		TaskKind:           "delegation_request",
		Title:              taskTitle(text),
		RequestText:        strings.TrimSpace(text),
		Blocking:           false,
		Metadata: map[string]any{
			"delegate_to_account_id": targetProfile.AccountID,
			"source":                 "wechat-owner",
		},
	})
	if err != nil {
		return nil, nil, err
	}

	grant := &AuthorizationGrant{
		ID:                 uuid.NewString(),
		TaskID:             parentTask.ID,
		RequesterAccountID: sourceAccountID,
		ApproverAccountID:  targetProfile.AccountID,
		Status:             ApprovalStatusPending,
		Scope:              "cross_user_task_delegation",
		RequestReason:      strings.TrimSpace(text),
	}
	if err := s.store.PutApproval(grant); err != nil {
		return nil, nil, err
	}
	parentTask.ApprovalID = grant.ID
	if err := s.store.PutTask(parentTask); err != nil {
		return nil, nil, err
	}

	sourceProfile, _ := s.store.GetProfile(sourceAccountID)
	_ = s.store.AppendTaskHistory(AppendHistoryInput{
		TaskID:         parentTask.ID,
		Actor:          firstNonEmpty(sourceProfileName(sourceProfile), sourceAccountID),
		ActorAccountID: sourceAccountID,
		Kind:           "delegation-requested",
		Message:        fmt.Sprintf("请求 %s 协作处理任务", firstNonEmpty(targetProfile.DisplayName, targetProfile.AccountID)),
		Metadata: map[string]any{
			"approval_id":       grant.ID,
			"target_account_id": targetProfile.AccountID,
			"requester_contact": requesterContactID,
		},
	})
	_ = s.store.AppendAudit(AppendAuditInput{
		TaskID:    parentTask.ID,
		AccountID: sourceAccountID,
		Category:  "delegation-requested",
		Message:   "发起跨用户 Agent 协作授权请求",
		Metadata: map[string]any{
			"target_account_id": targetProfile.AccountID,
			"approval_id":       grant.ID,
		},
	})

	if notifyErr := s.notifyApprovalRequest(ctx, parentTask, grant, sourceProfile, targetProfile); notifyErr != nil {
		_ = s.store.AppendAudit(AppendAuditInput{
			TaskID:    parentTask.ID,
			AccountID: targetProfile.AccountID,
			Category:  "approval-notify-failed",
			Message:   notifyErr.Error(),
		})
	}

	return parentTask, grant, nil
}

func (s *Service) ApproveGrant(ctx context.Context, approvalID, resolvedBy string) (*AuthorizationGrant, error) {
	grant, err := s.store.GetApproval(approvalID)
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return nil, fmt.Errorf("未找到授权请求: %s", approvalID)
	}
	if grant.Status != ApprovalStatusPending {
		return grant, nil
	}

	grant.Status = ApprovalStatusApproved
	grant.ResolvedReason = strings.TrimSpace(resolvedBy)
	grant.ResolvedAt = nowString()
	if err := s.store.PutApproval(grant); err != nil {
		return nil, err
	}

	parentTask, err := s.store.GetTask(grant.TaskID, 50)
	if err != nil {
		return nil, err
	}
	if parentTask == nil {
		return nil, fmt.Errorf("授权关联任务不存在: %s", grant.TaskID)
	}
	parentTask.Status = TaskStatusWorking
	if err := s.store.PutTask(parentTask); err != nil {
		return nil, err
	}
	_ = s.store.AppendTaskHistory(AppendHistoryInput{
		TaskID:         parentTask.ID,
		Actor:          "system",
		ActorAccountID: grant.ApproverAccountID,
		Kind:           "approval-approved",
		Message:        fmt.Sprintf("授权 %s 已批准，开始执行委派任务", grant.ID),
	})
	_ = s.store.AppendAudit(AppendAuditInput{
		TaskID:    parentTask.ID,
		AccountID: grant.ApproverAccountID,
		Category:  "approval-approved",
		Message:   "授权已批准，开始执行协作任务",
	})

	childTask, err := s.store.CreateTask(CreateTaskInput{
		ID:                 uuid.NewString(),
		ContextID:          fmt.Sprintf("delegate-exec:%s:%s", grant.ApproverAccountID, parentTask.ID),
		RequesterAccountID: grant.RequesterAccountID,
		RequesterContactID: parentTask.RequesterContactID,
		TargetAccountID:    grant.ApproverAccountID,
		OwnerAccountID:     grant.ApproverAccountID,
		Status:             TaskStatusSubmitted,
		TaskKind:           "delegated_execution",
		Title:              parentTask.Title,
		RequestText:        parentTask.RequestText,
		ParentTaskID:       parentTask.ID,
		Blocking:           false,
		Metadata: map[string]any{
			"approval_id": approvalID,
			"source_task": parentTask.ID,
		},
	})
	if err != nil {
		return nil, err
	}
	_ = s.store.AppendTaskHistory(AppendHistoryInput{
		TaskID:         childTask.ID,
		Actor:          "system",
		ActorAccountID: grant.ApproverAccountID,
		Kind:           "delegated-task-created",
		Message:        fmt.Sprintf("收到来自 %s 的协作任务", grant.RequesterAccountID),
	})

	go s.processDelegatedTask(parentTask.ID, childTask.ID)
	return grant, nil
}

func (s *Service) RejectGrant(ctx context.Context, approvalID, reason string) (*AuthorizationGrant, error) {
	grant, err := s.store.GetApproval(approvalID)
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return nil, fmt.Errorf("未找到授权请求: %s", approvalID)
	}
	if grant.Status != ApprovalStatusPending {
		return grant, nil
	}

	grant.Status = ApprovalStatusRejected
	grant.ResolvedReason = strings.TrimSpace(reason)
	grant.ResolvedAt = nowString()
	if err := s.store.PutApproval(grant); err != nil {
		return nil, err
	}

	parentTask, err := s.store.GetTask(grant.TaskID, 50)
	if err != nil {
		return nil, err
	}
	if parentTask != nil {
		parentTask.Status = TaskStatusRejected
		parentTask.ErrorText = firstNonEmpty(strings.TrimSpace(reason), "授权被拒绝")
		if err := s.store.PutTask(parentTask); err != nil {
			return nil, err
		}
		_ = s.store.AppendTaskHistory(AppendHistoryInput{
			TaskID:         parentTask.ID,
			Actor:          "system",
			ActorAccountID: grant.ApproverAccountID,
			Kind:           "approval-rejected",
			Message:        fmt.Sprintf("授权 %s 已拒绝", grant.ID),
		})
		_ = s.store.AppendAudit(AppendAuditInput{
			TaskID:    parentTask.ID,
			AccountID: grant.ApproverAccountID,
			Category:  "approval-rejected",
			Message:   "协作授权被拒绝",
			Metadata: map[string]any{
				"reason": reason,
			},
		})
		_ = s.notifyRequester(context.Background(), parentTask, fmt.Sprintf("协作请求被拒绝：%s", firstNonEmpty(strings.TrimSpace(reason), "未提供原因")))
	}
	return grant, nil
}

func (s *Service) ListTasks(limit int) ([]TaskRecord, error) {
	return s.store.ListTasks(limit)
}

func (s *Service) ListApprovals(status string, limit int) ([]AuthorizationGrant, error) {
	return s.store.ListApprovals(status, limit)
}

func (s *Service) ListAudit(limit int) ([]AuditRecord, error) {
	return s.store.ListAudit(limit)
}

func (s *Service) HandleJSONRPC(ctx context.Context, targetAccountID string, request JSONRPCRequest) (any, error) {
	if request.JSONRPC != "2.0" {
		return nil, fmt.Errorf("unsupported jsonrpc version: %s", request.JSONRPC)
	}
	if !s.IsKnownAccount(targetAccountID) {
		return nil, fmt.Errorf("unknown account: %s", targetAccountID)
	}

	switch request.Method {
	case "message/send":
		var params MessageSendParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, err
		}
		text := ExtractMessageText(params.Message)
		if text == "" {
			return nil, fmt.Errorf("message text is required")
		}

		delegateTo := firstNonEmpty(stringMetadata(params.Metadata, "delegateToAccountID"), stringMetadata(params.Message.Metadata, "delegateToAccountID"))
		requesterAccountID := firstNonEmpty(stringMetadata(params.Metadata, "requesterAccountID"), targetAccountID)
		requesterContactID := firstNonEmpty(stringMetadata(params.Metadata, "requesterContactID"), "")
		historyLength := 20
		blocking := true
		if params.Configuration != nil {
			if params.Configuration.HistoryLength > 0 {
				historyLength = params.Configuration.HistoryLength
			}
			blocking = params.Configuration.Blocking
		}

		if delegateTo != "" && delegateTo != targetAccountID {
			task, grant, err := s.CreateDelegation(ctx, targetAccountID, requesterContactID, delegateTo, text)
			if err != nil {
				return nil, err
			}
			result, err := s.store.GetTask(task.ID, historyLength)
			if err != nil {
				return nil, err
			}
			out := s.taskToA2ATask(result)
			if out.Metadata == nil {
				out.Metadata = map[string]any{}
			}
			out.Metadata["approvalId"] = grant.ID
			return out, nil
		}

		task, err := s.store.CreateTask(CreateTaskInput{
			ID:                 uuid.NewString(),
			ContextID:          firstNonEmpty(params.Message.ContextID, fmt.Sprintf("a2a:%s:%s", targetAccountID, requesterAccountID)),
			RequesterAccountID: requesterAccountID,
			RequesterContactID: requesterContactID,
			TargetAccountID:    targetAccountID,
			OwnerAccountID:     targetAccountID,
			Status:             TaskStatusSubmitted,
			TaskKind:           "a2a_message",
			Title:              taskTitle(text),
			RequestText:        text,
			Blocking:           blocking,
			Metadata: map[string]any{
				"source": "a2a",
			},
		})
		if err != nil {
			return nil, err
		}
		_ = s.store.AppendTaskHistory(AppendHistoryInput{
			TaskID:         task.ID,
			Actor:          requesterAccountID,
			ActorAccountID: requesterAccountID,
			Kind:           "a2a-message",
			Message:        text,
		})

		profile, err := s.store.GetProfile(targetAccountID)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, fmt.Errorf("unknown target account: %s", targetAccountID)
		}

		if blocking {
			updatedTask, err := s.processLocalTask(ctx, profile, task, false)
			if err != nil {
				return nil, err
			}
			return s.taskToA2ATask(updatedTask), nil
		}

		go s.processLocalTask(context.Background(), profile, task, true)
		return s.taskToA2ATask(task), nil

	case "tasks/get":
		var params TaskQueryParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, err
		}
		task, err := s.store.GetTask(params.ID, params.HistoryLength)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("task not found: %s", params.ID)
		}
		return s.taskToA2ATask(task), nil

	case "tasks/cancel":
		var params TaskCancelParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, err
		}
		task, err := s.store.GetTask(params.ID, 50)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("task not found: %s", params.ID)
		}
		if !isTerminalStatus(task.Status) {
			task.Status = TaskStatusCanceled
			task.ErrorText = "任务已取消"
			if err := s.store.PutTask(task); err != nil {
				return nil, err
			}
			_ = s.store.AppendTaskHistory(AppendHistoryInput{
				TaskID:         task.ID,
				Actor:          "system",
				ActorAccountID: targetAccountID,
				Kind:           "task-canceled",
				Message:        "任务已取消",
			})
		}
		return s.taskToA2ATask(task), nil
	}

	return nil, fmt.Errorf("unsupported method: %s", request.Method)
}

func (s *Service) processDelegatedTask(parentTaskID, childTaskID string) {
	childTask, err := s.store.GetTask(childTaskID, 50)
	if err != nil || childTask == nil {
		return
	}
	profile, err := s.store.GetProfile(childTask.TargetAccountID)
	if err != nil || profile == nil {
		return
	}

	updatedChild, err := s.processLocalTask(context.Background(), profile, childTask, false)
	parentTask, parentErr := s.store.GetTask(parentTaskID, 50)
	if parentErr != nil || parentTask == nil {
		return
	}

	if err != nil {
		parentTask.Status = TaskStatusFailed
		parentTask.ErrorText = err.Error()
		_ = s.store.PutTask(parentTask)
		_ = s.store.AppendTaskHistory(AppendHistoryInput{
			TaskID:         parentTask.ID,
			Actor:          "system",
			ActorAccountID: childTask.TargetAccountID,
			Kind:           "delegated-task-failed",
			Message:        err.Error(),
		})
		_ = s.notifyRequester(context.Background(), parentTask, fmt.Sprintf("协作任务失败：%v", err))
		return
	}

	parentTask.Status = TaskStatusCompleted
	parentTask.ResultText = fmt.Sprintf("来自 %s 的协作结果：\n%s", firstNonEmpty(profile.DisplayName, profile.AccountID), updatedChild.ResultText)
	parentTask.AssignedAgentName = updatedChild.AssignedAgentName
	parentTask.ErrorText = ""
	if err := s.store.PutTask(parentTask); err != nil {
		return
	}
	_ = s.store.AppendTaskHistory(AppendHistoryInput{
		TaskID:         parentTask.ID,
		Actor:          firstNonEmpty(profile.DisplayName, profile.AccountID),
		ActorAccountID: profile.AccountID,
		Kind:           "delegated-task-completed",
		Message:        updatedChild.ResultText,
	})
	_ = s.store.AppendAudit(AppendAuditInput{
		TaskID:    parentTask.ID,
		AccountID: profile.AccountID,
		Category:  "delegated-task-completed",
		Message:   "跨用户协作任务已完成",
		Metadata: map[string]any{
			"child_task_id": childTaskID,
		},
	})

	_ = s.notifyRequester(context.Background(), parentTask, fmt.Sprintf("协作任务已完成：\n%s", parentTask.ResultText))
	_ = s.notifyOwner(context.Background(), profile.AccountID, fmt.Sprintf("你已完成一项协作任务：%s", parentTask.Title))
}

func (s *Service) processLocalTask(ctx context.Context, profile *UserAgentProfile, task *TaskRecord, notify bool) (*TaskRecord, error) {
	task.Status = TaskStatusWorking
	if err := s.store.PutTask(task); err != nil {
		return nil, err
	}
	_ = s.store.AppendTaskHistory(AppendHistoryInput{
		TaskID:         task.ID,
		Actor:          "system",
		ActorAccountID: profile.AccountID,
		Kind:           "task-started",
		Message:        "开始调用底层 Agent 处理任务",
	})

	if s.chat == nil {
		task.Status = TaskStatusFailed
		task.ErrorText = "未配置底层 Agent 执行器"
		_ = s.store.PutTask(task)
		return task, fmt.Errorf("%s", task.ErrorText)
	}

	result, err := s.chat(ctx, task.ContextID, task.RequestText, profile.BaseAgentName)
	if err != nil {
		task.Status = TaskStatusFailed
		task.ErrorText = err.Error()
		_ = s.store.PutTask(task)
		_ = s.store.AppendTaskHistory(AppendHistoryInput{
			TaskID:         task.ID,
			Actor:          profile.BaseAgentName,
			ActorAccountID: profile.AccountID,
			Kind:           "task-failed",
			Message:        err.Error(),
		})
		_ = s.store.AppendAudit(AppendAuditInput{
			TaskID:    task.ID,
			AccountID: profile.AccountID,
			Category:  "task-failed",
			Message:   err.Error(),
		})
		if notify {
			_ = s.notifyRequester(context.Background(), task, fmt.Sprintf("任务失败：%v", err))
		}
		return task, err
	}

	task.Status = TaskStatusCompleted
	task.ResultText = strings.TrimSpace(result.Reply)
	task.AssignedAgentName = firstNonEmpty(result.AgentName, profile.BaseAgentName)
	task.ErrorText = ""
	if err := s.store.PutTask(task); err != nil {
		return nil, err
	}
	_ = s.store.AppendTaskHistory(AppendHistoryInput{
		TaskID:         task.ID,
		Actor:          firstNonEmpty(result.AgentName, profile.BaseAgentName),
		ActorAccountID: profile.AccountID,
		Kind:           "task-completed",
		Message:        task.ResultText,
	})
	_ = s.store.AppendAudit(AppendAuditInput{
		TaskID:    task.ID,
		AccountID: profile.AccountID,
		Category:  "task-completed",
		Message:   "底层 Agent 已完成任务",
		Metadata: map[string]any{
			"agent_name": task.AssignedAgentName,
			"model":      result.Model,
		},
	})
	if notify {
		_ = s.notifyRequester(context.Background(), task, fmt.Sprintf("任务已完成：\n%s", task.ResultText))
	}
	return task, nil
}

func (s *Service) notifyApprovalRequest(ctx context.Context, task *TaskRecord, grant *AuthorizationGrant, sourceProfile, targetProfile *UserAgentProfile) error {
	shortApproval := shortCode(grant.ID)
	message := fmt.Sprintf(
		"收到来自 %s 的协作请求。\n任务: %s\n说明: %s\n审批码: %s\n如果当前只有这一条待审批，直接回复“同意”或“拒绝”也可以。\n如果有多条待审批，请回复“批准 第一个”或“拒绝 第二个 原因”。\n也可以发送 /approve %s 或 /reject %s 原因。",
		firstNonEmpty(sourceProfileName(sourceProfile), grant.RequesterAccountID),
		task.Title,
		task.RequestText,
		shortApproval,
		shortApproval,
		shortApproval,
	)
	return s.notifyOwner(ctx, targetProfile.AccountID, message)
}

func (s *Service) notifyOwner(ctx context.Context, accountID, text string) error {
	profile, err := s.store.GetProfile(accountID)
	if err != nil {
		return err
	}
	if profile == nil || strings.TrimSpace(profile.OwnerContactID) == "" || s.sendText == nil {
		return nil
	}
	return s.sendText(ctx, accountID, profile.OwnerContactID, text, "")
}

func (s *Service) notifyRequester(ctx context.Context, task *TaskRecord, text string) error {
	if s.sendText == nil || strings.TrimSpace(task.RequesterContactID) == "" {
		return nil
	}
	return s.sendText(ctx, task.RequesterAccountID, task.RequesterContactID, text, "")
}

func (s *Service) taskToA2ATask(task *TaskRecord) *A2ATask {
	if task == nil {
		return nil
	}
	statusMessage := &Message{
		Role: "assistant",
		Parts: []Part{{
			Kind: "text",
			Text: firstNonEmpty(task.ResultText, task.ErrorText, task.RequestText),
		}},
	}
	if strings.TrimSpace(statusMessage.Parts[0].Text) == "" {
		statusMessage = nil
	}

	out := &A2ATask{
		Kind:      "task",
		ID:        task.ID,
		ContextID: task.ContextID,
		Status: TaskStatus{
			State:     task.Status,
			Message:   statusMessage,
			Timestamp: task.UpdatedAt,
		},
		Metadata: map[string]any{
			"requesterAccountId": task.RequesterAccountID,
			"targetAccountId":    task.TargetAccountID,
			"ownerAccountId":     task.OwnerAccountID,
			"taskKind":           task.TaskKind,
			"approvalId":         task.ApprovalID,
		},
	}

	for _, entry := range task.History {
		out.History = append(out.History, Message{
			Kind:      "message",
			ContextID: task.ContextID,
			TaskID:    task.ID,
			Role:      historyRole(entry.Kind),
			Parts: []Part{{
				Kind: "text",
				Text: entry.Message,
			}},
			Metadata: entry.Metadata,
		})
	}
	if strings.TrimSpace(task.ResultText) != "" {
		out.Artifacts = append(out.Artifacts, Artifact{
			ID:          "result",
			Name:        "final-response",
			Description: "任务最终结果",
			Parts: []Part{{
				Kind:     "text",
				Text:     task.ResultText,
				MIMEType: "text/plain",
			}},
		})
	}
	return out
}

func buildWorkflowGraph(task *TaskRecord, approval *AuthorizationGrant, children []TaskRecord) *WorkflowGraph {
	if task == nil {
		return nil
	}

	graph := &WorkflowGraph{
		TaskID: task.ID,
		Title:  task.Title,
		Status: task.Status,
	}

	addNode := func(id, label, nodeType, status, detail string) {
		graph.Nodes = append(graph.Nodes, WorkflowNode{
			ID:     id,
			Label:  label,
			Type:   nodeType,
			Status: status,
			Detail: detail,
			Order:  len(graph.Nodes),
		})
		if len(graph.Nodes) > 1 {
			prev := graph.Nodes[len(graph.Nodes)-2]
			graph.Edges = append(graph.Edges, WorkflowEdge{From: prev.ID, To: id})
		}
	}

	switch task.TaskKind {
	case "delegated_execution":
		addNode("intake", "收到委派", "intake", "completed", task.RequestText)
		addNode("local_execution", "本地执行", "local_execution", executionNodeStatus(task), firstNonEmpty(task.AssignedAgentName, task.OwnerAccountID))
		addNode("result_delivery", "结果产出", "result_delivery", resultNodeStatus(task), firstNonEmpty(task.ResultText, task.ErrorText))
	default:
		addNode("intake", "接收任务", "intake", "completed", task.RequestText)
		addNode("route", "任务路由", "route", routeNodeStatus(task), routeDetail(task))
		if task.ApprovalID != "" || task.TaskKind == "delegation_request" || task.TargetAccountID != task.OwnerAccountID {
			approvalStatus, blockedReason := approvalNodeState(task, approval)
			if blockedReason != "" {
				graph.BlockedReason = blockedReason
			}
			addNode("approval", "等待授权", "approval", approvalStatus, approvalDetail(approval))
			delegatedStatus := delegatedTaskNodeStatus(task, approval, children)
			delegatedDetail := ""
			if len(children) > 0 {
				delegatedDetail = firstNonEmpty(children[0].ResultText, children[0].ErrorText, children[0].Status)
			}
			addNode("delegated_task", "远端执行", "delegated_task", delegatedStatus, delegatedDetail)
		} else {
			addNode("local_execution", "本地执行", "local_execution", executionNodeStatus(task), firstNonEmpty(task.AssignedAgentName, task.OwnerAccountID))
		}
		addNode("result_delivery", "结果回传", "result_delivery", resultNodeStatus(task), firstNonEmpty(task.ResultText, task.ErrorText))
	}

	finalStatus := task.Status
	if finalStatus == "" {
		finalStatus = "pending"
	}
	addNode("final", "结束", "final", finalStatus, firstNonEmpty(task.ErrorText, task.ResultText))
	return graph
}

func (s *Service) userServiceURL(accountID string) string {
	if s.publicBaseURL == "" {
		return "/a2a/users/" + url.PathEscape(accountID)
	}
	return s.publicBaseURL + "/a2a/users/" + url.PathEscape(accountID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeRoutingText(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, " ", "")))
}

func normalizeIngressText(value string) string {
	replacer := strings.NewReplacer(" ", "", "，", "", ",", "", "。", "", ".", "", "！", "", "!", "", "？", "", "?", "")
	return strings.ToLower(strings.TrimSpace(replacer.Replace(value)))
}

func looksLikeApprovalReply(normalized string) bool {
	return looksLikeApproveReply(normalized) || looksLikeRejectReply(normalized)
}

func looksLikeApproveReply(normalized string) bool {
	phrases := []string{"同意", "批准", "可以", "行", "好的", "继续"}
	for _, phrase := range phrases {
		if normalized == phrase {
			return true
		}
	}
	return false
}

func looksLikeRejectReply(normalized string) bool {
	phrases := []string{"拒绝", "不行", "不要", "先不处理", "先不做"}
	for _, phrase := range phrases {
		if normalized == phrase {
			return true
		}
	}
	return false
}

func explicitApprovalCommand(text, prefix string) (string, string, bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), prefix) {
		return "", "", false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), prefix))
	if payload == "" {
		return "", "", false
	}
	fields := strings.Fields(payload)
	if len(fields) == 0 {
		return "", "", false
	}
	rest := ""
	if len(fields) > 1 {
		rest = strings.TrimSpace(strings.TrimPrefix(payload, fields[0]))
	}
	return strings.TrimSpace(fields[0]), rest, true
}

func buildApprovalClarification(contexts []ActiveContext) string {
	if len(contexts) == 0 {
		return "主人，现在没有待审批任务喵。"
	}
	var lines []string
	lines = append(lines, "主人，现在有多个待审批任务，蜜桃喵不敢乱认喵，请明确回复：")
	for idx, item := range contexts {
		lines = append(lines, fmt.Sprintf("%d. [%s] %s", idx+1, item.DisplayID, item.Title))
	}
	lines = append(lines, "可以直接回复：批准 第一个")
	lines = append(lines, "或者：拒绝 第二个 原因")
	lines = append(lines, "也可以发送：/approve 审批码")
	lines = append(lines, "或者：/reject 审批码 原因")
	return strings.Join(lines, "\n")
}

func shortCode(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func (s *Service) resolveApprovalReference(accountID, ref string, activeContexts []ActiveContext) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("审批失败：请补充审批码喵。")
	}

	var matches []ActiveContext
	for _, item := range activeContexts {
		if item.ApprovalID == ref ||
			item.DisplayID == ref ||
			strings.HasPrefix(item.ApprovalID, ref) ||
			strings.HasPrefix(item.TaskID, ref) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return matches[0].ApprovalID, nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("审批码 %s 命中了多个待审批任务喵，请回复“批准 第一个”这种形式，或者看页面里的审批码。", ref)
	}

	if approval, err := s.store.GetApproval(ref); err == nil && approval != nil {
		return approval.ID, nil
	}

	return "", fmt.Errorf("找不到待审批任务 %s 喵，请看最新的审批提醒或页面上的短审批码。", ref)
}

func ordinalApprovalReply(normalized string) (string, int, bool) {
	pairs := map[string]string{
		"批准第": "approve",
		"同意第": "approve",
		"拒绝第": "reject",
	}
	for prefix, action := range pairs {
		if strings.HasPrefix(normalized, prefix) {
			index := parseOrdinalPrefix(strings.TrimPrefix(normalized, prefix))
			if index > 0 {
				return action, index, true
			}
		}
	}
	return "", 0, false
}

func parseOrdinalPrefix(value string) int {
	candidates := []struct {
		prefix string
		index  int
	}{
		{"一个", 1},
		{"1个", 1},
		{"1条", 1},
		{"一条", 1},
		{"一", 1},
		{"二个", 2},
		{"两个", 2},
		{"2个", 2},
		{"2条", 2},
		{"二条", 2},
		{"二", 2},
		{"三个", 3},
		{"3个", 3},
		{"3条", 3},
		{"三条", 3},
		{"三", 3},
		{"4个", 4},
		{"四个", 4},
		{"4条", 4},
		{"四条", 4},
		{"四", 4},
		{"5个", 5},
		{"五个", 5},
		{"5条", 5},
		{"五条", 5},
		{"五", 5},
	}
	for _, item := range candidates {
		if strings.HasPrefix(value, item.prefix) {
			return item.index
		}
	}
	switch value {
	case "1", "第1个", "第1条":
		return 1
	case "2", "第2个", "第2条":
		return 2
	case "3", "第3个", "第3条":
		return 3
	case "4", "第4个", "第4条":
		return 4
	case "5", "第5个", "第5条":
		return 5
	}
	return 0
}

var explicitApprovalCommandPattern = regexp.MustCompile(`/(approve|reject)\s+`)

func splitExplicitApprovalCommands(text string) []string {
	indices := explicitApprovalCommandPattern.FindAllStringIndex(text, -1)
	if len(indices) <= 1 {
		return nil
	}

	commands := make([]string, 0, len(indices))
	for idx, bounds := range indices {
		start := bounds[0]
		end := len(text)
		if idx+1 < len(indices) {
			end = indices[idx+1][0]
		}
		command := strings.TrimSpace(text[start:end])
		if command != "" {
			commands = append(commands, command)
		}
	}
	return commands
}

func splitNonEmptyLines(text string) []string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func tokenMatch(normalizedText, candidate string) bool {
	normalizedCandidate := normalizeRoutingText(candidate)
	if normalizedCandidate == "" {
		return false
	}
	if strings.Contains(normalizedText, normalizedCandidate) {
		return true
	}
	for _, token := range splitRoutingTokens(candidate) {
		token = normalizeRoutingText(token)
		if len([]rune(token)) < 2 {
			continue
		}
		if strings.Contains(normalizedText, token) {
			return true
		}
	}
	return false
}

func splitRoutingTokens(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("，,。.;；、/|:：()[]{}<>-_", r)
	})
}

func decisionAuditCategory(reason string) string {
	if strings.Contains(reason, "接近") {
		return "auto-delegation-ambiguous"
	}
	return "auto-delegation-local-fallback"
}

func executionNodeStatus(task *TaskRecord) string {
	switch task.Status {
	case TaskStatusWorking:
		return "running"
	case TaskStatusCompleted:
		return "completed"
	case TaskStatusFailed:
		return "failed"
	case TaskStatusRejected:
		return "rejected"
	default:
		return "pending"
	}
}

func resultNodeStatus(task *TaskRecord) string {
	switch task.Status {
	case TaskStatusCompleted:
		return "completed"
	case TaskStatusFailed:
		return "failed"
	case TaskStatusRejected:
		return "rejected"
	case TaskStatusWaitingApproval:
		return "pending"
	default:
		if strings.TrimSpace(task.ResultText) != "" || strings.TrimSpace(task.ErrorText) != "" {
			return "completed"
		}
		return "pending"
	}
}

func routeNodeStatus(task *TaskRecord) string {
	if task.Status == TaskStatusSubmitted {
		return "running"
	}
	return "completed"
}

func routeDetail(task *TaskRecord) string {
	if task.ApprovalID != "" || task.TargetAccountID != task.OwnerAccountID {
		return "已选择跨用户协作"
	}
	return "已选择本地处理"
}

func approvalNodeState(task *TaskRecord, approval *AuthorizationGrant) (string, string) {
	if approval == nil {
		if task.Status == TaskStatusWaitingApproval {
			return "blocked", "等待对方用户批准"
		}
		return "pending", ""
	}
	switch approval.Status {
	case ApprovalStatusPending:
		return "blocked", "等待对方用户批准"
	case ApprovalStatusApproved:
		return "completed", ""
	case ApprovalStatusRejected:
		return "rejected", "协作请求已被拒绝"
	default:
		return "pending", ""
	}
}

func approvalDetail(approval *AuthorizationGrant) string {
	if approval == nil {
		return ""
	}
	return firstNonEmpty(approval.RequestReason, approval.ResolvedReason)
}

func delegatedTaskNodeStatus(task *TaskRecord, approval *AuthorizationGrant, children []TaskRecord) string {
	if approval != nil && approval.Status == ApprovalStatusRejected {
		return "rejected"
	}
	if len(children) == 0 {
		if approval != nil && approval.Status == ApprovalStatusPending {
			return "pending"
		}
		if task.Status == TaskStatusWorking {
			return "running"
		}
		return "pending"
	}
	child := children[0]
	switch child.Status {
	case TaskStatusCompleted:
		return "completed"
	case TaskStatusFailed:
		return "failed"
	case TaskStatusRejected:
		return "rejected"
	case TaskStatusWorking:
		return "running"
	default:
		return "pending"
	}
}

func taskTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "未命名任务"
	}
	runes := []rune(text)
	if len(runes) > 48 {
		return string(runes[:48]) + "..."
	}
	return text
}

func sourceProfileName(profile *UserAgentProfile) string {
	if profile == nil {
		return ""
	}
	return firstNonEmpty(profile.DisplayName, profile.AccountID)
}

func stringMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func historyRole(kind string) string {
	switch kind {
	case "request", "a2a-message":
		return "user"
	default:
		return "assistant"
	}
}

func isTerminalStatus(status string) bool {
	switch status {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCanceled, TaskStatusRejected:
		return true
	default:
		return false
	}
}
