package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type BridgeTaskStatus string

const (
	BridgeTaskPending          BridgeTaskStatus = "pending"
	BridgeTaskRouting          BridgeTaskStatus = "routing"
	BridgeTaskWaitingPeerAgent BridgeTaskStatus = "waiting_peer_agent"
	BridgeTaskWaitingPeerUser  BridgeTaskStatus = "waiting_peer_user"
	BridgeTaskWaitingLocalUser BridgeTaskStatus = "waiting_local_user"
	BridgeTaskCompleted        BridgeTaskStatus = "completed"
	BridgeTaskFailed           BridgeTaskStatus = "failed"
)

type DeliveryMode string

const (
	DeliveryModeDeliverToPeerUser DeliveryMode = "deliver_to_peer_user"
	DeliveryModeAssistAndReturn   DeliveryMode = "assist_and_return"
)

type BridgeConfig struct {
	Enabled           bool
	NodeID            string
	ListenAddr        string
	PublicBaseURL     string
	PeerNodeID        string
	PeerBaseURL       string
	LocalUserID       string
	LocalAgentAliases []string
	PeerAgentAliases  []string
	PeerUserAliases   []string
	OutboundPrefix    string
	Timeout           time.Duration
}

type WeClawInbound struct {
	AccountID    string `json:"account_id,omitempty"`
	FromUserID   string `json:"from_user_id"`
	Text         string `json:"text"`
	RouteMode    string `json:"route_mode,omitempty"`
	MessageID    string `json:"message_id,omitempty"`
	ContextToken string `json:"context_token,omitempty"`
}

type Envelope struct {
	MessageID      string `json:"message_id"`
	ConversationID string `json:"conversation_id"`
	SourceNode     string `json:"source_node"`
	TargetNode     string `json:"target_node"`
	SourceUser     string `json:"source_user,omitempty"`
	SourceTaskID   string `json:"source_task_id,omitempty"`
	ReplyToTaskID  string `json:"reply_to_task_id,omitempty"`
	TraceID        string `json:"trace_id"`
	CreatedAt      string `json:"created_at"`
}

type TaskRequest struct {
	Envelope Envelope       `json:"envelope"`
	TaskType string         `json:"task_type"`
	Payload  map[string]any `json:"payload,omitempty"`
}

type TaskResult struct {
	TaskID   string         `json:"task_id"`
	Status   string         `json:"status"`
	Accepted bool           `json:"accepted"`
	Detail   string         `json:"detail"`
	Route    string         `json:"route,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

type BridgeDecision struct {
	Action         string  `json:"action"`
	Message        string  `json:"message"`
	TargetNode     *string `json:"target_node"`
	Rationale      string  `json:"rationale"`
	FollowUpNeeded bool    `json:"follow_up_needed"`
}

type ProxyReplyDecision struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Rationale string `json:"rationale"`
}

type BridgeHistoryEntry struct {
	Actor   string `json:"actor"`
	Content string `json:"content"`
}

type BridgeTask struct {
	TaskID          string               `json:"task_id"`
	ConversationID  string               `json:"conversation_id"`
	NodeID          string               `json:"node_id"`
	OwnerNodeID     string               `json:"owner_node_id"`
	LocalUserID     string               `json:"local_user_id"`
	ReplyUserID     string               `json:"reply_user_id"`
	OriginKind      string               `json:"origin_kind"`
	CurrentInput    string               `json:"current_input"`
	Status          BridgeTaskStatus     `json:"status"`
	RequesterNodeID string               `json:"requester_node_id,omitempty"`
	ParentTaskID    string               `json:"parent_task_id,omitempty"`
	PeerNodeID      string               `json:"peer_node_id,omitempty"`
	FinalResponse   string               `json:"final_response,omitempty"`
	LastAction      string               `json:"last_action,omitempty"`
	Error           string               `json:"error,omitempty"`
	Metadata        map[string]string    `json:"metadata,omitempty"`
	History         []BridgeHistoryEntry `json:"history,omitempty"`
	CreatedAt       string               `json:"created_at"`
	UpdatedAt       string               `json:"updated_at"`
}

const (
	waitingScopeLocalUserFollowUp = "local_user_follow_up"
	waitingScopePeerUserProxy     = "peer_user_proxy"

	proxyReplyAnswerPending   = "answer_pending_question"
	proxyReplyClarifyAndReask = "clarify_identity_and_reask"
	proxyReplyNewLocalRequest = "new_local_request"
)

type BridgeRuntimeDeps struct {
	Chat     func(ctx context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error)
	SendText func(ctx context.Context, accountID, toUserID, text, contextToken string) error
	Dispatch func(ctx context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error)
}

type BridgeRuntime struct {
	cfg      BridgeConfig
	store    *BridgeTaskStore
	chat     func(ctx context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error)
	sendText func(ctx context.Context, accountID, toUserID, text, contextToken string) error
	dispatch func(ctx context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error)
}

type BridgeTaskStore struct {
	mu            sync.RWMutex
	tasks         map[string]*BridgeTask
	pendingByUser map[string]string
}

func NewBridgeRuntime(cfg BridgeConfig, deps BridgeRuntimeDeps) *BridgeRuntime {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	runtime := &BridgeRuntime{
		cfg:      cfg,
		store:    NewBridgeTaskStore(),
		chat:     deps.Chat,
		sendText: deps.SendText,
		dispatch: deps.Dispatch,
	}
	if runtime.dispatch == nil {
		runtime.dispatch = runtime.defaultDispatch
	}
	return runtime
}

func NewBridgeTaskStore() *BridgeTaskStore {
	return &BridgeTaskStore{
		tasks:         make(map[string]*BridgeTask),
		pendingByUser: make(map[string]string),
	}
}

func (s *BridgeTaskStore) Save(task *BridgeTask) {
	if task == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.TaskID] = task
	if task.ReplyUserID != "" && task.Status == BridgeTaskWaitingLocalUser {
		s.pendingByUser[task.ReplyUserID] = task.TaskID
		return
	}
	if task.ReplyUserID != "" && s.pendingByUser[task.ReplyUserID] == task.TaskID {
		delete(s.pendingByUser, task.ReplyUserID)
	}
}

func (s *BridgeTaskStore) Get(taskID string) *BridgeTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tasks[taskID]
}

func (s *BridgeTaskStore) PendingForUser(userID string) *BridgeTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	taskID := s.pendingByUser[userID]
	if taskID == "" {
		return nil
	}
	return s.tasks[taskID]
}

func (r *BridgeRuntime) NormalizeIncomingText(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if r == nil {
		return trimmed, false
	}
	prefix := strings.TrimSpace(r.cfg.OutboundPrefix)
	if prefix == "" {
		return trimmed, false
	}
	if strings.HasPrefix(trimmed, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), true
	}
	return trimmed, false
}

func (r *BridgeRuntime) HandleWeClawInbound(ctx context.Context, inbound WeClawInbound) (*TaskResult, error) {
	if r == nil {
		return nil, fmt.Errorf("bridge runtime unavailable")
	}
	r.logf(nil, "local inbound from=%s route=%s account=%s message_id=%s text=%q", inbound.FromUserID, firstNonBlank(inbound.RouteMode, "auto"), inbound.AccountID, inbound.MessageID, truncate(strings.TrimSpace(inbound.Text), 120))
	text, suppressed := r.NormalizeIncomingText(inbound.Text)
	if suppressed {
		r.logf(nil, "suppressed prefixed bridge message from=%s message_id=%s", inbound.FromUserID, inbound.MessageID)
		return &TaskResult{
			TaskID:   firstNonBlank(inbound.MessageID, uuid.NewString()),
			Status:   string(BridgeTaskCompleted),
			Accepted: true,
			Detail:   "suppressed prefixed bridge message",
			Route:    "suppressed",
		}, nil
	}
	if text == "" {
		r.logf(nil, "rejected empty inbound text from=%s message_id=%s", inbound.FromUserID, inbound.MessageID)
		return &TaskResult{
			TaskID:   firstNonBlank(inbound.MessageID, uuid.NewString()),
			Status:   string(BridgeTaskFailed),
			Accepted: false,
			Detail:   "empty inbound text",
		}, nil
	}
	if pending := r.store.PendingForUser(inbound.FromUserID); pending != nil {
		r.logf(pending, "matched pending local question for user=%s", inbound.FromUserID)
		if pending.Metadata["waiting_scope"] == waitingScopePeerUserProxy {
			return r.handlePeerUserProxyReply(ctx, pending, text, inbound)
		}
		return r.resumeFromLocalUser(ctx, pending, text, inbound)
	}

	task := newLocalBridgeTask(r.cfg.NodeID, r.cfg.LocalUserID, inbound.FromUserID, text)
	task.Metadata["route_mode"] = "auto"
	task.Metadata["account_id"] = inbound.AccountID
	task.Metadata["context_token"] = inbound.ContextToken
	task.Metadata["external_message_id"] = inbound.MessageID
	task.appendHistory("local_user", text)
	r.store.Save(task)
	r.logf(task, "created local task account=%s route=%s text=%q", inbound.AccountID, task.Metadata["route_mode"], truncate(text, 120))
	return r.processTask(ctx, task)
}

func (r *BridgeRuntime) ReceiveRequest(ctx context.Context, request TaskRequest) (*TaskResult, error) {
	if r == nil {
		return nil, fmt.Errorf("bridge runtime unavailable")
	}
	r.logf(nil, "peer inbound type=%s source=%s target=%s conversation=%s source_task=%s reply_to=%s text=%q",
		request.TaskType,
		request.Envelope.SourceNode,
		request.Envelope.TargetNode,
		request.Envelope.ConversationID,
		request.Envelope.SourceTaskID,
		request.Envelope.ReplyToTaskID,
		truncate(stringValue(request.Payload["text"]), 120),
	)

	switch request.TaskType {
	case "peer_request":
		text := strings.TrimSpace(stringValue(request.Payload["text"]))
		if text == "" {
			return &TaskResult{TaskID: request.Envelope.MessageID, Status: string(BridgeTaskFailed), Accepted: false, Detail: "missing text"}, nil
		}
		deliveryMode := stringValue(request.Payload["delivery_mode"])
		if deliveryMode == "" {
			deliveryMode = string(DeliveryModeAssistAndReturn)
		}
		task := newPeerBridgeTask(
			r.cfg.NodeID,
			request.Envelope.SourceNode,
			request.Envelope.SourceNode,
			firstNonBlank(request.Envelope.SourceTaskID, request.Envelope.MessageID),
			r.cfg.LocalUserID,
			r.cfg.LocalUserID,
			request.Envelope.ConversationID,
			text,
		)
		task.PeerNodeID = request.Envelope.SourceNode
		task.Metadata["route_mode"] = firstNonBlank(stringValue(request.Payload["route_mode"]), "auto")
		task.Metadata["delivery_mode"] = deliveryMode
		task.Metadata["origin_reply_user_id"] = firstNonBlank(stringValue(request.Payload["reply_user_id"]), request.Envelope.SourceUser)
		task.appendHistory("peer_agent", text)
		r.store.Save(task)
		r.logf(task, "created peer task delivery_mode=%s route=%s requester=%s", deliveryMode, task.Metadata["route_mode"], task.RequesterNodeID)
		r.processTaskAsync(task)
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: true,
			Detail:   "accepted",
			Route:    "queued",
		}, nil

	case "peer_user_question":
		questionText := strings.TrimSpace(stringValue(request.Payload["question_text"]))
		if questionText == "" {
			questionText = strings.TrimSpace(stringValue(request.Payload["text"]))
		}
		if questionText == "" {
			return &TaskResult{TaskID: request.Envelope.MessageID, Status: string(BridgeTaskFailed), Accepted: false, Detail: "missing question_text"}, nil
		}
		task := newPeerBridgeTask(
			r.cfg.NodeID,
			request.Envelope.SourceNode,
			request.Envelope.SourceNode,
			firstNonBlank(request.Envelope.ReplyToTaskID, request.Envelope.SourceTaskID, request.Envelope.MessageID),
			r.cfg.LocalUserID,
			r.cfg.LocalUserID,
			request.Envelope.ConversationID,
			questionText,
		)
		task.PeerNodeID = request.Envelope.SourceNode
		task.Metadata["delivery_mode"] = string(DeliveryModeAssistAndReturn)
		task.Metadata["waiting_scope"] = waitingScopePeerUserProxy
		task.Metadata["context_token"] = ""
		task.Metadata["proxy_question_text"] = questionText
		task.Metadata["requester_agent_label"] = firstNonBlank(stringValue(request.Payload["requester_agent_label"]), request.Envelope.SourceNode)
		task.Metadata["requester_user_id"] = firstNonBlank(stringValue(request.Payload["requester_user_id"]), request.Envelope.SourceUser)
		task.appendHistory("peer_agent", questionText)
		r.store.Save(task)
		r.logf(task, "created proxy question requester_agent_label=%s question=%q", task.Metadata["requester_agent_label"], truncate(questionText, 120))
		r.processTaskAsync(task)
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: true,
			Detail:   "peer user question queued",
			Route:    "clarify",
		}, nil

	case "peer_result", "peer_user_answer":
		replyToTaskID := request.Envelope.ReplyToTaskID
		if replyToTaskID == "" {
			return &TaskResult{TaskID: request.Envelope.MessageID, Status: string(BridgeTaskFailed), Accepted: false, Detail: "missing reply_to_task_id"}, nil
		}
		task := r.store.Get(replyToTaskID)
		if task == nil {
			return &TaskResult{TaskID: replyToTaskID, Status: string(BridgeTaskFailed), Accepted: false, Detail: "task not found"}, nil
		}
		task.CurrentInput = strings.TrimSpace(stringValue(request.Payload["text"]))
		task.Metadata["resume_from"] = request.TaskType
		task.Metadata["context_token"] = ""
		actor := "peer_agent"
		if request.TaskType == "peer_user_answer" {
			actor = "peer_user"
		}
		task.appendHistory(actor, task.CurrentInput)
		r.store.Save(task)
		r.logf(task, "resuming task from=%s text=%q", request.TaskType, truncate(task.CurrentInput, 120))
		r.processTaskAsync(task)
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: true,
			Detail:   "resume queued",
			Route:    "queued",
		}, nil
	}

	return &TaskResult{
		TaskID:   request.Envelope.MessageID,
		Status:   string(BridgeTaskFailed),
		Accepted: false,
		Detail:   fmt.Sprintf("unsupported task_type: %s", request.TaskType),
	}, nil
}

func (r *BridgeRuntime) processTask(ctx context.Context, task *BridgeTask) (*TaskResult, error) {
	task.setStatus(BridgeTaskRouting, "routing")
	r.store.Save(task)
	r.logf(task, "processing task current_input=%q resume_from=%s delivery_mode=%s", truncate(task.CurrentInput, 120), task.Metadata["resume_from"], task.Metadata["delivery_mode"])

	decision, err := r.decide(ctx, task)
	if err != nil {
		task.fail(err.Error())
		r.store.Save(task)
		r.sendFailureNotice(ctx, task, err)
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: false,
			Detail:   err.Error(),
		}, nil
	}

	decision = r.normalizeDecision(task, decision)
	task.appendHistory("agent", marshalDecision(decision))
	r.logf(task, "decision action=%s target=%s rationale=%q follow_up=%v message=%q",
		decision.Action,
		ptrValue(decision.TargetNode),
		decision.Rationale,
		decision.FollowUpNeeded,
		truncate(decision.Message, 120),
	)
	route, applyErr := r.applyDecision(ctx, task, decision)
	if applyErr != nil {
		task.fail(applyErr.Error())
		r.store.Save(task)
		r.sendFailureNotice(ctx, task, applyErr)
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: false,
			Detail:   applyErr.Error(),
		}, nil
	}

	return &TaskResult{
		TaskID:   task.TaskID,
		Status:   string(task.Status),
		Accepted: true,
		Detail:   "accepted",
		Route:    route,
	}, nil
}

func (r *BridgeRuntime) processTaskAsync(task *BridgeTask) {
	if task == nil {
		return
	}
	r.logf(task, "queueing async processing")
	go func(taskID string) {
		task := r.store.Get(taskID)
		if task == nil {
			log.Printf("[bridge] task=%s missing before async processing", taskID)
			return
		}
		if _, err := r.processTask(context.Background(), task); err != nil {
			r.logf(task, "async processing error: %v", err)
		}
	}(task.TaskID)
}

func (r *BridgeRuntime) decide(ctx context.Context, task *BridgeTask) (BridgeDecision, error) {
	targetNode := firstNonBlank(task.PeerNodeID, r.cfg.PeerNodeID)
	if heuristic, ok := r.heuristicDecision(task, targetNode); ok {
		r.logf(task, "heuristic decision action=%s target=%s rationale=%q", heuristic.Action, ptrValue(heuristic.TargetNode), heuristic.Rationale)
		return heuristic, nil
	}
	if r.chat == nil {
		return BridgeDecision{}, fmt.Errorf("bridge chat dependency is not configured")
	}

	chatResult, err := r.chat(ctx, r.conversationID(task), r.buildDecisionPrompt(task, targetNode), "")
	if err != nil {
		r.logf(task, "agent decision failed: %v", err)
		return BridgeDecision{}, err
	}
	rawReply := ""
	if chatResult != nil {
		rawReply = chatResult.Reply
	}
	r.logf(task, "agent raw reply=%q", truncate(strings.TrimSpace(rawReply), 160))
	decision, parseErr := parseBridgeDecision(rawReply)
	if parseErr == nil {
		return decision, nil
	}

	fallbackMessage := strings.TrimSpace(stripMarkdownFences(rawReply))
	if fallbackMessage == "" {
		return BridgeDecision{}, parseErr
	}
	log.Printf("[bridge] falling back from non-JSON decision for task %s: %v", task.TaskID, parseErr)
	if task.OriginKind == "peer_agent" && r.deliveryModeForTask(task) == DeliveryModeAssistAndReturn {
		return BridgeDecision{
			Action:     "send_to_peer_agent",
			Message:    fallbackMessage,
			TargetNode: stringPtr(task.RequesterNodeID),
			Rationale:  "fallback from non-json peer response",
		}, nil
	}
	return BridgeDecision{
		Action:    "reply_local_user",
		Message:   fallbackMessage,
		Rationale: "fallback from non-json local response",
	}, nil
}

func (r *BridgeRuntime) normalizeDecision(task *BridgeTask, decision BridgeDecision) BridgeDecision {
	if decision.Message == "" {
		decision.Message = task.CurrentInput
	}

	if decision.Action == "send_to_peer_agent" || decision.Action == "ask_peer_user" {
		if firstNonBlank(ptrValue(decision.TargetNode), task.PeerNodeID, task.RequesterNodeID, r.cfg.PeerNodeID) == "" {
			return BridgeDecision{
				Action:         "need_more_info_from_local_user",
				Message:        "当前没有可用的对端节点可协作，请稍后重试。",
				Rationale:      "no available peer node",
				FollowUpNeeded: true,
			}
		}
	}

	if task.OriginKind == "local_user" {
		switch task.Metadata["resume_from"] {
		case "peer_result", "peer_user_answer":
			if decision.Action != "reply_local_user" {
				return BridgeDecision{
					Action:    "reply_local_user",
					Message:   decision.Message,
					Rationale: "normalized local completion after peer response",
				}
			}
		}
		return decision
	}

	if task.Metadata["waiting_scope"] == waitingScopePeerUserProxy {
		if decision.Action != "need_more_info_from_local_user" {
			return BridgeDecision{
				Action:         "need_more_info_from_local_user",
				Message:        decision.Message,
				Rationale:      firstNonBlank(decision.Rationale, "normalized peer-user proxy question"),
				FollowUpNeeded: true,
			}
		}
		decision.FollowUpNeeded = true
		return decision
	}

	if r.deliveryModeForTask(task) == DeliveryModeDeliverToPeerUser {
		if decision.Action != "reply_local_user" && decision.Action != "need_more_info_from_local_user" {
			return BridgeDecision{
				Action:    "reply_local_user",
				Message:   decision.Message,
				Rationale: "normalized peer delivery to local user",
			}
		}
		return decision
	}

	if decision.Action == "reply_local_user" {
		return BridgeDecision{
			Action:     "send_to_peer_agent",
			Message:    decision.Message,
			TargetNode: stringPtr(task.RequesterNodeID),
			Rationale:  "normalized peer return to requester",
		}
	}

	return decision
}

func (r *BridgeRuntime) applyDecision(ctx context.Context, task *BridgeTask, decision BridgeDecision) (string, error) {
	switch decision.Action {
	case "reply_local_user":
		if task.OriginKind != "local_user" && r.deliveryModeForTask(task) != DeliveryModeDeliverToPeerUser {
			return "", fmt.Errorf("reply_local_user is invalid for task %s", task.TaskID)
		}
		r.logf(task, "sending local reply to=%s account=%s", task.ReplyUserID, task.Metadata["account_id"])
		if err := r.sendText(ctx, task.Metadata["account_id"], task.ReplyUserID, decision.Message, task.Metadata["context_token"]); err != nil {
			return "", err
		}
		task.complete(decision.Message)
		r.store.Save(task)
		r.logf(task, "completed with local reply")
		return "local", nil

	case "need_more_info_from_local_user":
		r.logf(task, "asking local user=%s for more info", task.ReplyUserID)
		if err := r.sendText(ctx, task.Metadata["account_id"], task.ReplyUserID, decision.Message, task.Metadata["context_token"]); err != nil {
			return "", err
		}
		if task.Metadata["waiting_scope"] != waitingScopePeerUserProxy {
			task.Metadata["waiting_scope"] = waitingScopeLocalUserFollowUp
		}
		task.setStatus(BridgeTaskWaitingLocalUser, "need_more_info_from_local_user")
		r.store.Save(task)
		r.logf(task, "waiting for local user follow-up")
		return "clarify", nil

	case "ask_peer_user":
		targetNode := firstNonBlank(ptrValue(decision.TargetNode), task.PeerNodeID, r.cfg.PeerNodeID)
		if targetNode == "" {
			return "", fmt.Errorf("ask_peer_user requires target_node")
		}
		request := TaskRequest{
			Envelope: newEnvelope(task.ConversationID, r.cfg.NodeID, targetNode, task.ReplyUserID, task.TaskID, task.TaskID),
			TaskType: "peer_user_question",
			Payload: map[string]any{
				"text":                  decision.Message,
				"question_text":         decision.Message,
				"requester_agent_label": firstNonBlank(firstAlias(r.cfg.LocalAgentAliases), r.cfg.NodeID),
				"requester_user_id":     task.ReplyUserID,
			},
		}
		r.logf(task, "dispatching peer_user_question to node=%s text=%q", targetNode, truncate(decision.Message, 120))
		result, err := r.dispatchToPeer(ctx, targetNode, request)
		if err != nil {
			return "", err
		}
		if !result.Accepted {
			return "", fmt.Errorf("%s", result.Detail)
		}
		task.PeerNodeID = targetNode
		task.Metadata["delivery_mode"] = string(DeliveryModeAssistAndReturn)
		task.Metadata["context_token"] = ""
		task.setStatus(BridgeTaskWaitingPeerUser, "ask_peer_user")
		r.store.Save(task)
		r.logf(task, "waiting for peer user on node=%s", targetNode)
		return "peer", nil

	case "send_to_peer_agent":
		targetNode := firstNonBlank(ptrValue(decision.TargetNode), task.RequesterNodeID, task.PeerNodeID, r.cfg.PeerNodeID)
		if targetNode == "" {
			return "", fmt.Errorf("send_to_peer_agent requires target_node")
		}
		if task.OriginKind == "peer_agent" && task.RequesterNodeID != "" && targetNode == task.RequesterNodeID {
			request := TaskRequest{
				Envelope: newEnvelope(task.ConversationID, r.cfg.NodeID, targetNode, task.ReplyUserID, task.TaskID, task.ParentTaskID),
				TaskType: "peer_result",
				Payload: map[string]any{
					"text": decision.Message,
				},
			}
			r.logf(task, "returning peer_result to requester=%s text=%q", targetNode, truncate(decision.Message, 120))
			result, err := r.dispatchToPeer(ctx, targetNode, request)
			if err != nil {
				return "", err
			}
			if !result.Accepted {
				return "", fmt.Errorf("%s", result.Detail)
			}
			task.complete(decision.Message)
			r.store.Save(task)
			r.logf(task, "completed after peer_result return")
			return "peer", nil
		}

		deliveryMode := r.deliveryModeForPeerRequest(task, decision)
		task.Metadata["delivery_mode"] = string(deliveryMode)
		request := TaskRequest{
			Envelope: newEnvelope(task.ConversationID, r.cfg.NodeID, targetNode, task.ReplyUserID, task.TaskID, ""),
			TaskType: "peer_request",
			Payload: map[string]any{
				"text":          decision.Message,
				"reply_user_id": task.ReplyUserID,
				"delivery_mode": string(deliveryMode),
				"route_mode":    firstNonBlank(task.Metadata["route_mode"], "auto"),
				"account_id":    task.Metadata["account_id"],
			},
		}
		r.logf(task, "dispatching peer_request to node=%s delivery_mode=%s text=%q", targetNode, deliveryMode, truncate(decision.Message, 120))
		result, err := r.dispatchToPeer(ctx, targetNode, request)
		if err != nil {
			return "", err
		}
		if !result.Accepted {
			return "", fmt.Errorf("%s", result.Detail)
		}

		task.PeerNodeID = targetNode
		task.Metadata["context_token"] = ""
		if task.Status == BridgeTaskCompleted || task.Status == BridgeTaskFailed {
			r.store.Save(task)
			r.logf(task, "preserving task status=%s after synchronous peer callback", task.Status)
			return "peer", nil
		}
		if task.OriginKind == "local_user" && deliveryMode == DeliveryModeDeliverToPeerUser {
			handoff := r.buildPeerHandoffConfirmation(targetNode)
			r.logf(task, "sending local handoff confirmation for node=%s", targetNode)
			if err := r.sendText(ctx, task.Metadata["account_id"], task.ReplyUserID, handoff, ""); err != nil {
				return "", err
			}
			task.complete(handoff)
			r.store.Save(task)
			r.logf(task, "completed with peer handoff confirmation")
			return "peer", nil
		}

		task.setStatus(BridgeTaskWaitingPeerAgent, "send_to_peer_agent")
		r.store.Save(task)
		r.logf(task, "waiting for peer agent node=%s", targetNode)
		return "peer", nil
	}

	return "", fmt.Errorf("unsupported action: %s", decision.Action)
}

func (r *BridgeRuntime) resumeFromLocalUser(ctx context.Context, task *BridgeTask, content string, inbound WeClawInbound) (*TaskResult, error) {
	task.Metadata["account_id"] = firstNonBlank(task.Metadata["account_id"], inbound.AccountID)
	task.Metadata["context_token"] = inbound.ContextToken
	task.appendHistory("local_user", content)
	r.logf(task, "received local user follow-up scope=%s text=%q", task.Metadata["waiting_scope"], truncate(content, 120))

	if task.Metadata["waiting_scope"] == waitingScopePeerUserProxy {
		request := TaskRequest{
			Envelope: newEnvelope(task.ConversationID, r.cfg.NodeID, task.RequesterNodeID, task.ReplyUserID, task.TaskID, task.ParentTaskID),
			TaskType: "peer_user_answer",
			Payload: map[string]any{
				"text": content,
			},
		}
		r.logf(task, "dispatching peer_user_answer to requester=%s", task.RequesterNodeID)
		result, err := r.dispatchToPeer(ctx, task.RequesterNodeID, request)
		if err != nil {
			task.fail(err.Error())
			r.store.Save(task)
			return &TaskResult{TaskID: task.TaskID, Status: string(task.Status), Accepted: false, Detail: err.Error()}, nil
		}
		if !result.Accepted {
			task.fail(result.Detail)
			r.store.Save(task)
			return &TaskResult{TaskID: task.TaskID, Status: string(task.Status), Accepted: false, Detail: result.Detail}, nil
		}
		task.complete(content)
		r.store.Save(task)
		r.logf(task, "completed after peer_user_answer return")
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: true,
			Detail:   "peer user answer queued",
			Route:    "peer",
		}, nil
	}

	task.CurrentInput = content
	task.Metadata["resume_from"] = "local_user_reply"
	task.setStatus(BridgeTaskPending, "local_user_reply")
	r.store.Save(task)
	r.logf(task, "queued local_user_reply for reprocessing")
	return r.processTask(ctx, task)
}

func (r *BridgeRuntime) handlePeerUserProxyReply(ctx context.Context, task *BridgeTask, content string, inbound WeClawInbound) (*TaskResult, error) {
	task.Metadata["account_id"] = firstNonBlank(task.Metadata["account_id"], inbound.AccountID)
	task.Metadata["context_token"] = inbound.ContextToken
	task.appendHistory("local_user", content)
	r.logf(task, "classifying proxy reply text=%q", truncate(content, 120))

	decision, err := r.classifyPeerUserProxyReply(ctx, task, content)
	if err != nil {
		r.logf(task, "proxy reply classification failed, falling back to clarify: %v", err)
		decision = ProxyReplyDecision{
			Kind:      proxyReplyClarifyAndReask,
			Message:   r.defaultProxyClarification(task),
			Rationale: "fallback after classification failure",
		}
	}
	r.logf(task, "proxy reply classified kind=%s rationale=%q message=%q", decision.Kind, decision.Rationale, truncate(decision.Message, 120))

	switch decision.Kind {
	case proxyReplyAnswerPending:
		forward := firstNonBlank(strings.TrimSpace(decision.Message), content)
		request := TaskRequest{
			Envelope: newEnvelope(task.ConversationID, r.cfg.NodeID, task.RequesterNodeID, task.ReplyUserID, task.TaskID, task.ParentTaskID),
			TaskType: "peer_user_answer",
			Payload: map[string]any{
				"text": forward,
			},
		}
		r.logf(task, "dispatching classified peer_user_answer to requester=%s text=%q", task.RequesterNodeID, truncate(forward, 120))
		result, dispatchErr := r.dispatchToPeer(ctx, task.RequesterNodeID, request)
		if dispatchErr != nil {
			task.fail(dispatchErr.Error())
			r.store.Save(task)
			return &TaskResult{TaskID: task.TaskID, Status: string(task.Status), Accepted: false, Detail: dispatchErr.Error()}, nil
		}
		if !result.Accepted {
			task.fail(result.Detail)
			r.store.Save(task)
			return &TaskResult{TaskID: task.TaskID, Status: string(task.Status), Accepted: false, Detail: result.Detail}, nil
		}
		task.complete(forward)
		r.store.Save(task)
		r.logf(task, "completed after classified peer_user_answer return")
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: true,
			Detail:   "peer user answer queued",
			Route:    "peer",
		}, nil

	case proxyReplyNewLocalRequest:
		newText := firstNonBlank(strings.TrimSpace(decision.Message), content)
		newTask := newLocalBridgeTask(r.cfg.NodeID, r.cfg.LocalUserID, inbound.FromUserID, newText)
		newTask.Metadata["route_mode"] = "auto"
		newTask.Metadata["account_id"] = inbound.AccountID
		newTask.Metadata["context_token"] = inbound.ContextToken
		newTask.Metadata["external_message_id"] = inbound.MessageID
		newTask.appendHistory("local_user", newText)
		r.store.Save(newTask)
		r.logf(newTask, "created new local task while proxy question remains pending")
		r.processTaskAsync(newTask)
		return &TaskResult{
			TaskID:   newTask.TaskID,
			Status:   string(newTask.Status),
			Accepted: true,
			Detail:   "new local request queued",
			Route:    "queued",
		}, nil

	default:
		reply := firstNonBlank(strings.TrimSpace(decision.Message), r.defaultProxyClarification(task))
		r.logf(task, "answering proxy clarification locally and keeping pending")
		if err := r.sendText(ctx, task.Metadata["account_id"], task.ReplyUserID, reply, task.Metadata["context_token"]); err != nil {
			task.fail(err.Error())
			r.store.Save(task)
			return &TaskResult{TaskID: task.TaskID, Status: string(task.Status), Accepted: false, Detail: err.Error()}, nil
		}
		task.setStatus(BridgeTaskWaitingLocalUser, "proxy_clarification")
		r.store.Save(task)
		return &TaskResult{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Accepted: true,
			Detail:   "proxy clarification answered locally",
			Route:    "clarify",
		}, nil
	}
}

func (r *BridgeRuntime) sendFailureNotice(ctx context.Context, task *BridgeTask, err error) {
	if task == nil || r.sendText == nil {
		return
	}
	r.logf(task, "sending failure notice err=%v", err)
	if task.OriginKind != "local_user" && r.deliveryModeForTask(task) != DeliveryModeDeliverToPeerUser {
		return
	}
	_ = r.sendText(ctx, task.Metadata["account_id"], task.ReplyUserID, fmt.Sprintf("处理失败：%v", err), "")
}

func (r *BridgeRuntime) defaultDispatch(ctx context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error) {
	r.logf(nil, "http dispatch type=%s target_base=%s target_node=%s conversation=%s reply_to=%s", request.TaskType, targetBaseURL, request.Envelope.TargetNode, request.Envelope.ConversationID, request.Envelope.ReplyToTaskID)
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(targetBaseURL, "/")+"/api/bridge/inbound", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: r.cfg.Timeout}).Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("bridge inbound failed: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}

	var result TaskResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BridgeRuntime) dispatchToPeer(ctx context.Context, targetNode string, request TaskRequest) (*TaskResult, error) {
	baseURL, err := r.peerBaseURLFor(targetNode)
	if err != nil {
		return nil, err
	}
	if r.dispatch == nil {
		return nil, fmt.Errorf("bridge dispatch dependency is not configured")
	}
	r.logf(nil, "dispatching type=%s to node=%s base=%s text=%q", request.TaskType, targetNode, baseURL, truncate(stringValue(request.Payload["text"]), 120))
	return r.dispatch(ctx, baseURL, request)
}

func (r *BridgeRuntime) peerBaseURLFor(targetNode string) (string, error) {
	if targetNode == "" {
		return "", fmt.Errorf("missing target node")
	}
	if r.cfg.PeerBaseURL == "" {
		return "", fmt.Errorf("peer_base_url is not configured")
	}
	if r.cfg.PeerNodeID != "" && targetNode != r.cfg.PeerNodeID {
		return "", fmt.Errorf("unsupported peer node: %s", targetNode)
	}
	return r.cfg.PeerBaseURL, nil
}

func (r *BridgeRuntime) conversationID(task *BridgeTask) string {
	return fmt.Sprintf("a2a:%s:%s", firstNonBlank(r.cfg.NodeID, "node"), task.TaskID)
}

func (r *BridgeRuntime) buildDecisionPrompt(task *BridgeTask, targetNode string) string {
	history := task.History
	if len(history) > 6 {
		history = history[len(history)-6:]
	}
	compact := map[string]any{
		"node_id":             r.cfg.NodeID,
		"local_user_id":       r.cfg.LocalUserID,
		"local_agent_aliases": r.cfg.LocalAgentAliases,
		"peer_node_id":        r.cfg.PeerNodeID,
		"peer_agent_aliases":  r.cfg.PeerAgentAliases,
		"peer_user_aliases":   r.cfg.PeerUserAliases,
		"task": map[string]any{
			"task_id":           task.TaskID,
			"conversation_id":   task.ConversationID,
			"origin_kind":       task.OriginKind,
			"requester_node_id": task.RequesterNodeID,
			"target_node":       targetNode,
			"current_input":     task.CurrentInput,
			"metadata":          task.Metadata,
		},
		"history": history,
	}
	return "Return one JSON object only with keys: action, message, target_node, rationale, follow_up_needed.\n" +
		"Allowed actions: reply_local_user, send_to_peer_agent, ask_peer_user, need_more_info_from_local_user.\n" +
		"Rules:\n" +
		"- reply_local_user means reply to this node's own local user.\n" +
		"- send_to_peer_agent means send to the peer node's assistant.\n" +
		"- ask_peer_user means ask the peer node to question its own local human user and return the answer.\n" +
		"- need_more_info_from_local_user means ask this node's own local user for clarification.\n" +
		"- If origin_kind=peer_agent and metadata.delivery_mode=deliver_to_peer_user, prefer reply_local_user unless clarification is required.\n" +
		"- If origin_kind=peer_agent and metadata.delivery_mode=assist_and_return, prefer sending the answer back to requester_node_id.\n" +
		"- peer_agent_aliases are names for the remote assistant. peer_user_aliases are names for the remote human user. Do not confuse them.\n" +
		"- If metadata.waiting_scope=peer_user_proxy, you are not answering yet. Rewrite the question into this node's own assistant voice and use need_more_info_from_local_user.\n" +
		"- For peer_user_proxy, treat metadata.proxy_question_text as the source question and metadata.requester_agent_label as the remote assistant label.\n" +
		"- For peer_user_proxy, do not mechanically repeat the original wording. Ask the local human naturally, as if you are speaking to your own owner.\n" +
		"- Treat shortened or fuzzy references to configured aliases as valid mentions.\n" +
		"- Keep message concise and directly usable.\n" +
		"- If unsure, choose need_more_info_from_local_user.\n" +
		"Context:\n" + mustJSON(compact)
}

func (r *BridgeRuntime) buildProxyReplyClassificationPrompt(task *BridgeTask, content string) string {
	compact := map[string]any{
		"node_id":             r.cfg.NodeID,
		"local_agent_aliases": r.cfg.LocalAgentAliases,
		"requester_agent":     task.Metadata["requester_agent_label"],
		"pending_question":    task.Metadata["proxy_question_text"],
		"latest_local_reply":  content,
		"history":             task.History,
	}
	return "Return one JSON object only with keys: kind, message, rationale.\n" +
		"Allowed kinds: answer_pending_question, clarify_identity_and_reask, new_local_request.\n" +
		"Rules:\n" +
		"- answer_pending_question: the user is answering the pending question. message should be the answer to forward.\n" +
		"- clarify_identity_and_reask: the user is asking who you are, who asked, why you are asking, or other context questions. message must explain who you are and who asked, then restate the pending question in one natural sentence.\n" +
		"- new_local_request: the user is starting a clearly separate new request unrelated to the pending question. message should be the new local request text.\n" +
		"- Do not forward clarification or meta questions as answers.\n" +
		"- Do not use markdown fences.\n" +
		"Context:\n" + mustJSON(compact)
}

func (r *BridgeRuntime) heuristicDecision(task *BridgeTask, targetNode string) (BridgeDecision, bool) {
	if task.OriginKind != "local_user" || targetNode == "" {
		return BridgeDecision{}, false
	}

	text := task.CurrentInput
	normalized := normalizeBridgeText(text)
	mentionsPeerAgent := matchesAnyAlias(normalized, r.cfg.PeerAgentAliases)
	mentionsPeerUser := matchesAnyAlias(normalized, r.cfg.PeerUserAliases)
	asksPeer := containsBridgeKeyword(text,
		"联系", "联络", "问一下", "询问", "通知", "转告", "帮我问", "帮我联系",
		"contact", "ask", "notify", "coordinate", "check with", "talk to",
	)
	if !asksPeer || (!mentionsPeerAgent && !mentionsPeerUser) {
		return BridgeDecision{}, false
	}

	if mentionsPeerUser {
		return BridgeDecision{
			Action:         "ask_peer_user",
			Message:        text,
			TargetNode:     stringPtr(targetNode),
			Rationale:      "heuristic peer-user coordination request",
			FollowUpNeeded: true,
		}, true
	}

	return BridgeDecision{
		Action:     "send_to_peer_agent",
		Message:    text,
		TargetNode: stringPtr(targetNode),
		Rationale:  "heuristic peer-agent coordination request",
	}, true
}

func (r *BridgeRuntime) deliveryModeForTask(task *BridgeTask) DeliveryMode {
	if strings.EqualFold(task.Metadata["delivery_mode"], string(DeliveryModeDeliverToPeerUser)) {
		return DeliveryModeDeliverToPeerUser
	}
	return DeliveryModeAssistAndReturn
}

func (r *BridgeRuntime) deliveryModeForPeerRequest(task *BridgeTask, decision BridgeDecision) DeliveryMode {
	if task.OriginKind == "peer_agent" {
		return r.deliveryModeForTask(task)
	}
	if decision.Action == "ask_peer_user" || wantsReplyBack(task.CurrentInput) {
		return DeliveryModeAssistAndReturn
	}
	return DeliveryModeDeliverToPeerUser
}

func (r *BridgeRuntime) buildPeerHandoffConfirmation(targetNode string) string {
	return fmt.Sprintf("[%s] 已转交给 %s，将由对端本地用户继续处理。", r.cfg.NodeID, targetNode)
}

func newLocalBridgeTask(nodeID, localUserID, replyUserID, text string) *BridgeTask {
	now := time.Now().UTC().Format(time.RFC3339)
	return &BridgeTask{
		TaskID:         uuid.NewString(),
		ConversationID: uuid.NewString(),
		NodeID:         nodeID,
		OwnerNodeID:    nodeID,
		LocalUserID:    localUserID,
		ReplyUserID:    replyUserID,
		OriginKind:     "local_user",
		CurrentInput:   text,
		Status:         BridgeTaskPending,
		Metadata:       make(map[string]string),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func newPeerBridgeTask(nodeID, ownerNodeID, requesterNodeID, parentTaskID, localUserID, replyUserID, conversationID, text string) *BridgeTask {
	now := time.Now().UTC().Format(time.RFC3339)
	return &BridgeTask{
		TaskID:          uuid.NewString(),
		ConversationID:  conversationID,
		NodeID:          nodeID,
		OwnerNodeID:     ownerNodeID,
		LocalUserID:     localUserID,
		ReplyUserID:     replyUserID,
		OriginKind:      "peer_agent",
		CurrentInput:    text,
		Status:          BridgeTaskPending,
		RequesterNodeID: requesterNodeID,
		ParentTaskID:    parentTaskID,
		Metadata:        make(map[string]string),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func (t *BridgeTask) setStatus(status BridgeTaskStatus, lastAction string) {
	t.Status = status
	t.LastAction = lastAction
	t.Error = ""
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

func (t *BridgeTask) complete(response string) {
	t.Status = BridgeTaskCompleted
	t.FinalResponse = response
	t.Error = ""
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

func (t *BridgeTask) fail(reason string) {
	t.Status = BridgeTaskFailed
	t.Error = reason
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

func (t *BridgeTask) appendHistory(actor, content string) {
	t.History = append(t.History, BridgeHistoryEntry{Actor: actor, Content: content})
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

func parseBridgeDecision(raw string) (BridgeDecision, error) {
	var decision BridgeDecision
	if err := json.Unmarshal([]byte(stripMarkdownFences(raw)), &decision); err != nil {
		return BridgeDecision{}, err
	}
	switch decision.Action {
	case "reply_local_user", "send_to_peer_agent", "ask_peer_user", "need_more_info_from_local_user":
		return decision, nil
	default:
		return BridgeDecision{}, fmt.Errorf("unsupported action: %s", decision.Action)
	}
}

func stripMarkdownFences(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 {
		return trimmed
	}
	lines = lines[1:]
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func normalizeBridgeText(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(value), "")), "_", "")
}

func matchesAnyAlias(normalizedText string, aliases []string) bool {
	for _, alias := range aliases {
		normalizedAlias := normalizeBridgeText(alias)
		if normalizedAlias != "" && strings.Contains(normalizedText, normalizedAlias) {
			return true
		}
	}
	return false
}

func containsBridgeKeyword(text string, keywords ...string) bool {
	lowered := strings.ToLower(text)
	for _, keyword := range keywords {
		if keyword == "" {
			continue
		}
		if strings.Contains(text, keyword) || strings.Contains(lowered, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func wantsReplyBack(text string) bool {
	return containsBridgeKeyword(text, "回复后再转告我", "再转告我", "告诉我结果", "告诉我", "回我", "reply back", "tell me the result", "let me know")
}

func (r *BridgeRuntime) classifyPeerUserProxyReply(ctx context.Context, task *BridgeTask, content string) (ProxyReplyDecision, error) {
	if r.chat == nil {
		return ProxyReplyDecision{}, fmt.Errorf("bridge chat dependency is not configured")
	}
	chatResult, err := r.chat(ctx, fmt.Sprintf("a2a-proxy-reply:%s:%s", firstNonBlank(r.cfg.NodeID, "node"), task.TaskID), r.buildProxyReplyClassificationPrompt(task, content), "")
	if err != nil {
		return ProxyReplyDecision{}, err
	}
	rawReply := ""
	if chatResult != nil {
		rawReply = chatResult.Reply
	}
	r.logf(task, "proxy classifier raw reply=%q", truncate(strings.TrimSpace(rawReply), 160))

	var decision ProxyReplyDecision
	if err := json.Unmarshal([]byte(stripMarkdownFences(rawReply)), &decision); err != nil {
		return ProxyReplyDecision{}, err
	}
	switch decision.Kind {
	case proxyReplyAnswerPending, proxyReplyClarifyAndReask, proxyReplyNewLocalRequest:
		return decision, nil
	default:
		return ProxyReplyDecision{}, fmt.Errorf("unsupported proxy reply kind: %s", decision.Kind)
	}
}

func (r *BridgeRuntime) defaultProxyClarification(task *BridgeTask) string {
	localAgent := firstNonBlank(firstAlias(r.cfg.LocalAgentAliases), r.cfg.NodeID)
	requesterAgent := firstNonBlank(task.Metadata["requester_agent_label"], task.RequesterNodeID, "对端助手")
	question := firstNonBlank(task.Metadata["proxy_question_text"], task.CurrentInput)
	return fmt.Sprintf("主人，我是%s，%s 那边想问你：%s", localAgent, requesterAgent, question)
}

func firstAlias(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newEnvelope(conversationID, sourceNode, targetNode, sourceUser, sourceTaskID, replyToTaskID string) Envelope {
	return Envelope{
		MessageID:      uuid.NewString(),
		ConversationID: conversationID,
		SourceNode:     sourceNode,
		TargetNode:     targetNode,
		SourceUser:     sourceUser,
		SourceTaskID:   sourceTaskID,
		ReplyToTaskID:  replyToTaskID,
		TraceID:        uuid.NewString(),
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func marshalDecision(decision BridgeDecision) string {
	data, err := json.Marshal(decision)
	if err != nil {
		return decision.Action
	}
	return string(data)
}

func ptrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func (r *BridgeRuntime) logf(task *BridgeTask, format string, args ...any) {
	prefix := "[bridge]"
	if task != nil {
		prefix = fmt.Sprintf("[bridge] task=%s conv=%s node=%s origin=%s status=%s", task.TaskID, task.ConversationID, task.NodeID, task.OriginKind, task.Status)
	}
	log.Printf(prefix+" "+format, args...)
}
