package messaging

import (
	"context"
	"strings"
	"testing"
	"time"
)

type testBridgeSend struct {
	accountID    string
	toUserID     string
	text         string
	contextToken string
}

type testBridgeRecorder struct {
	chatReply    string
	chatCalls    int
	sendCalls    []testBridgeSend
	dispatches   []TaskRequest
	dispatchURLs []string
}

func newTestBridgeRuntime(recorder *testBridgeRecorder) *BridgeRuntime {
	if recorder == nil {
		recorder = &testBridgeRecorder{}
	}
	return NewBridgeRuntime(
		BridgeConfig{
			Enabled:           true,
			NodeID:            "local-node",
			PeerNodeID:        "remote-node",
			PeerBaseURL:       "http://peer.example",
			LocalUserID:       "local-user@im.wechat",
			LocalAgentAliases: []string{"MTM", "蜜桃喵"},
			PeerAgentAliases:  []string{"幽浮喵", "UFO"},
			PeerUserAliases:   []string{"NX1", "Ivan_NX1"},
			OutboundPrefix:    "[A2A-BRIDGE] ",
		},
		BridgeRuntimeDeps{
			Chat: func(_ context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error) {
				recorder.chatCalls++
				return &LocalAgentChatResult{AgentName: "codex", Reply: recorder.chatReply}, nil
			},
			SendText: func(_ context.Context, accountID, toUserID, text, contextToken string) error {
				recorder.sendCalls = append(recorder.sendCalls, testBridgeSend{
					accountID:    accountID,
					toUserID:     toUserID,
					text:         text,
					contextToken: contextToken,
				})
				return nil
			},
			Dispatch: func(_ context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error) {
				recorder.dispatchURLs = append(recorder.dispatchURLs, targetBaseURL)
				recorder.dispatches = append(recorder.dispatches, request)
				return &TaskResult{
					TaskID:   request.Envelope.MessageID,
					Status:   string(BridgeTaskPending),
					Accepted: true,
					Detail:   "accepted",
					Route:    "peer",
				}, nil
			},
		},
	)
}

func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

func TestBridgeRuntimeSuppressesPrefixedMessages(t *testing.T) {
	recorder := &testBridgeRecorder{}
	runtime := newTestBridgeRuntime(recorder)

	result, err := runtime.HandleWeClawInbound(context.Background(), WeClawInbound{
		FromUserID: "local-user@im.wechat",
		Text:       "[A2A-BRIDGE] internal message",
	})
	if err != nil {
		t.Fatalf("HandleWeClawInbound returned error: %v", err)
	}
	if !result.Accepted || result.Route != "suppressed" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if recorder.chatCalls != 0 {
		t.Fatalf("chatCalls = %d, want 0", recorder.chatCalls)
	}
	if len(recorder.sendCalls) != 0 {
		t.Fatalf("sendCalls = %#v, want none", recorder.sendCalls)
	}
}

func TestBridgeRuntimeLocalReplyUsesStructuredDecision(t *testing.T) {
	recorder := &testBridgeRecorder{
		chatReply: `{"action":"reply_local_user","message":"本地已处理","target_node":null,"rationale":"direct","follow_up_needed":false}`,
	}
	runtime := newTestBridgeRuntime(recorder)

	result, err := runtime.HandleWeClawInbound(context.Background(), WeClawInbound{
		AccountID:    "bot-1",
		FromUserID:   "local-user@im.wechat",
		Text:         "今天晚上我自己安排一下",
		ContextToken: "ctx-1",
	})
	if err != nil {
		t.Fatalf("HandleWeClawInbound returned error: %v", err)
	}
	if !result.Accepted || result.Route != "local" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if recorder.chatCalls != 1 {
		t.Fatalf("chatCalls = %d, want 1", recorder.chatCalls)
	}
	if len(recorder.sendCalls) != 1 {
		t.Fatalf("sendCalls = %#v, want one local reply", recorder.sendCalls)
	}
	if recorder.sendCalls[0].toUserID != "local-user@im.wechat" || recorder.sendCalls[0].text != "本地已处理" {
		t.Fatalf("unexpected send payload: %#v", recorder.sendCalls[0])
	}
	if recorder.sendCalls[0].contextToken != "ctx-1" {
		t.Fatalf("contextToken = %q, want ctx-1", recorder.sendCalls[0].contextToken)
	}
	if len(recorder.dispatches) != 0 {
		t.Fatalf("dispatches = %#v, want none", recorder.dispatches)
	}
}

func TestBridgeRuntimeHeuristicRoutesPeerUserAlias(t *testing.T) {
	recorder := &testBridgeRecorder{}
	runtime := newTestBridgeRuntime(recorder)

	result, err := runtime.HandleWeClawInbound(context.Background(), WeClawInbound{
		FromUserID: "local-user@im.wechat",
		Text:       "帮我问一下 NX1 晚饭吃什么，告诉我结果",
	})
	if err != nil {
		t.Fatalf("HandleWeClawInbound returned error: %v", err)
	}
	if !result.Accepted || result.Route != "peer" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if recorder.chatCalls != 0 {
		t.Fatalf("chatCalls = %d, want heuristic path to skip chat", recorder.chatCalls)
	}
	if len(recorder.dispatches) != 1 {
		t.Fatalf("dispatches = %#v, want one peer dispatch", recorder.dispatches)
	}
	if recorder.dispatches[0].TaskType != "peer_user_question" {
		t.Fatalf("taskType = %q, want peer_user_question", recorder.dispatches[0].TaskType)
	}
	if recorder.dispatches[0].Payload["question_text"] != "帮我问一下 NX1 晚饭吃什么，告诉我结果" {
		t.Fatalf("question_text = %#v, want structured question_text", recorder.dispatches[0].Payload["question_text"])
	}
	if recorder.dispatches[0].Payload["requester_agent_label"] != "MTM" {
		t.Fatalf("requester_agent_label = %#v, want MTM", recorder.dispatches[0].Payload["requester_agent_label"])
	}
}

func TestBridgeRuntimeDeliverToPeerUserNormalizesToLocalReply(t *testing.T) {
	recorder := &testBridgeRecorder{
		chatReply: `{"action":"send_to_peer_agent","message":"直接回复本地用户","target_node":"remote-node","rationale":"bad","follow_up_needed":false}`,
	}
	runtime := newTestBridgeRuntime(recorder)

	result, err := runtime.ReceiveRequest(context.Background(), TaskRequest{
		Envelope: newEnvelope("conv-1", "remote-node", "local-node", "remote-user@im.wechat", "source-task", ""),
		TaskType: "peer_request",
		Payload: map[string]any{
			"text":          "请直接告诉本地用户",
			"delivery_mode": string(DeliveryModeDeliverToPeerUser),
		},
	})
	if err != nil {
		t.Fatalf("ReceiveRequest returned error: %v", err)
	}
	if !result.Accepted || result.Route != "queued" {
		t.Fatalf("unexpected result: %#v", result)
	}
	waitForCondition(t, func() bool { return len(recorder.sendCalls) == 1 })
	if len(recorder.sendCalls) != 1 {
		t.Fatalf("sendCalls = %#v, want one local delivery", recorder.sendCalls)
	}
	if recorder.sendCalls[0].toUserID != "local-user@im.wechat" || recorder.sendCalls[0].text != "直接回复本地用户" {
		t.Fatalf("unexpected send payload: %#v", recorder.sendCalls[0])
	}
	if len(recorder.dispatches) != 0 {
		t.Fatalf("dispatches = %#v, want none", recorder.dispatches)
	}
}

func TestBridgeRuntimeAssistAndReturnNormalizesToPeerResult(t *testing.T) {
	recorder := &testBridgeRecorder{
		chatReply: `{"action":"reply_local_user","message":"处理好了","target_node":null,"rationale":"bad","follow_up_needed":false}`,
	}
	runtime := newTestBridgeRuntime(recorder)

	result, err := runtime.ReceiveRequest(context.Background(), TaskRequest{
		Envelope: newEnvelope("conv-2", "remote-node", "local-node", "remote-user@im.wechat", "source-task", ""),
		TaskType: "peer_request",
		Payload: map[string]any{
			"text":          "请协助后回传",
			"delivery_mode": string(DeliveryModeAssistAndReturn),
		},
	})
	if err != nil {
		t.Fatalf("ReceiveRequest returned error: %v", err)
	}
	if !result.Accepted || result.Route != "queued" {
		t.Fatalf("unexpected result: %#v", result)
	}
	waitForCondition(t, func() bool { return len(recorder.dispatches) == 1 })
	if len(recorder.dispatches) != 1 {
		t.Fatalf("dispatches = %#v, want one peer_result dispatch", recorder.dispatches)
	}
	if recorder.dispatches[0].TaskType != "peer_result" {
		t.Fatalf("taskType = %q, want peer_result", recorder.dispatches[0].TaskType)
	}
	if recorder.dispatches[0].Payload["text"] != "处理好了" {
		t.Fatalf("payload text = %#v, want 处理好了", recorder.dispatches[0].Payload["text"])
	}
	if len(recorder.sendCalls) != 0 {
		t.Fatalf("sendCalls = %#v, want none", recorder.sendCalls)
	}
}

func TestBridgeRuntimeAssistAndReturnRoundTrip(t *testing.T) {
	recorderA := &testBridgeRecorder{}
	recorderB := &testBridgeRecorder{}

	var runtimeA *BridgeRuntime
	var runtimeB *BridgeRuntime

	runtimeA = NewBridgeRuntime(
		BridgeConfig{
			Enabled:           true,
			NodeID:            "local-node",
			PeerNodeID:        "remote-node",
			PeerBaseURL:       "http://peer-a.example",
			LocalUserID:       "local-user-a@im.wechat",
			LocalAgentAliases: []string{"MTM", "蜜桃喵"},
			PeerAgentAliases:  []string{"幽浮喵", "UFO"},
			PeerUserAliases:   []string{"NX1"},
		},
		BridgeRuntimeDeps{
			Chat: func(_ context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error) {
				recorderA.chatCalls++
				reply := `{"action":"reply_local_user","message":"B 端已经拿到结果 #ROUNDTRIP-1","target_node":null,"rationale":"resume-finalize","follow_up_needed":false}`
				if !strings.Contains(message, "B 端已经拿到结果 #ROUNDTRIP-1") {
					reply = `{"action":"send_to_peer_agent","message":"请帮我问一下#ROUNDTRIP-1","target_node":"remote-node","rationale":"delegate","follow_up_needed":false}`
				}
				return &LocalAgentChatResult{AgentName: "codex", Reply: reply}, nil
			},
			SendText: func(_ context.Context, accountID, toUserID, text, contextToken string) error {
				recorderA.sendCalls = append(recorderA.sendCalls, testBridgeSend{
					accountID:    accountID,
					toUserID:     toUserID,
					text:         text,
					contextToken: contextToken,
				})
				return nil
			},
			Dispatch: func(ctx context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error) {
				recorderA.dispatchURLs = append(recorderA.dispatchURLs, targetBaseURL)
				recorderA.dispatches = append(recorderA.dispatches, request)
				return runtimeB.ReceiveRequest(ctx, request)
			},
		},
	)

	runtimeB = NewBridgeRuntime(
		BridgeConfig{
			Enabled:           true,
			NodeID:            "remote-node",
			PeerNodeID:        "local-node",
			PeerBaseURL:       "http://peer-b.example",
			LocalUserID:       "local-user-b@im.wechat",
			LocalAgentAliases: []string{"幽浮喵", "UFO"},
			PeerAgentAliases:  []string{"MTM", "蜜桃喵"},
			PeerUserAliases:   []string{"nano"},
		},
		BridgeRuntimeDeps{
			Chat: func(_ context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error) {
				recorderB.chatCalls++
				return &LocalAgentChatResult{AgentName: "codex", Reply: `{"action":"reply_local_user","message":"B 端已经拿到结果 #ROUNDTRIP-1","target_node":null,"rationale":"done","follow_up_needed":false}`}, nil
			},
			SendText: func(_ context.Context, accountID, toUserID, text, contextToken string) error {
				recorderB.sendCalls = append(recorderB.sendCalls, testBridgeSend{
					accountID:    accountID,
					toUserID:     toUserID,
					text:         text,
					contextToken: contextToken,
				})
				return nil
			},
			Dispatch: func(ctx context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error) {
				recorderB.dispatchURLs = append(recorderB.dispatchURLs, targetBaseURL)
				recorderB.dispatches = append(recorderB.dispatches, request)
				return runtimeA.ReceiveRequest(ctx, request)
			},
		},
	)

	result, err := runtimeA.HandleWeClawInbound(context.Background(), WeClawInbound{
		AccountID:    "bot-a",
		FromUserID:   "local-user-a@im.wechat",
		Text:         "请联系幽浮喵，问一下 #ROUNDTRIP-1，并把结果告诉我",
		ContextToken: "ctx-a",
	})
	if err != nil {
		t.Fatalf("HandleWeClawInbound returned error: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("result = %#v, want accepted", result)
	}
	waitForCondition(t, func() bool {
		task := runtimeA.store.Get(result.TaskID)
		return task != nil && task.Status == BridgeTaskCompleted && len(recorderA.sendCalls) == 1
	})
	if len(recorderA.sendCalls) != 1 {
		t.Fatalf("A sendCalls = %#v, want exactly one final local reply", recorderA.sendCalls)
	}
	if got := recorderA.sendCalls[0].text; got != "B 端已经拿到结果 #ROUNDTRIP-1" {
		t.Fatalf("A final reply = %q, want round-trip result", got)
	}
	if len(recorderB.sendCalls) != 0 {
		t.Fatalf("B sendCalls = %#v, want no direct local delivery for assist_and_return", recorderB.sendCalls)
	}
	if recorderA.chatCalls != 1 {
		t.Fatalf("A chatCalls = %d, want 1 (resume path only; initial path is heuristic)", recorderA.chatCalls)
	}
	if recorderB.chatCalls != 1 {
		t.Fatalf("B chatCalls = %d, want 1", recorderB.chatCalls)
	}
	task := runtimeA.store.Get(result.TaskID)
	if task == nil {
		t.Fatalf("local task %q not found after round trip", result.TaskID)
	}
	if task.Status != BridgeTaskCompleted {
		t.Fatalf("local task status = %s, want %s", task.Status, BridgeTaskCompleted)
	}
}

func TestBridgeRuntimePeerUserQuestionIsRewrittenByPeerAgent(t *testing.T) {
	recorder := &testBridgeRecorder{
		chatReply: `{"action":"need_more_info_from_local_user","message":"主人，MTM 那边想问你：今天晚上的作业做完了吗？","target_node":null,"rationale":"peer-user-proxy rewrite","follow_up_needed":true}`,
	}
	runtime := newTestBridgeRuntime(recorder)

	result, err := runtime.ReceiveRequest(context.Background(), TaskRequest{
		Envelope: newEnvelope("conv-proxy-1", "remote-node", "local-node", "remote-user@im.wechat", "task-remote", "task-local"),
		TaskType: "peer_user_question",
		Payload: map[string]any{
			"text":                  "问一下NX1，今天晚上的作业你有做完吗？",
			"question_text":         "今天晚上的作业你做完了吗？",
			"requester_agent_label": "MTM",
			"requester_user_id":     "user-a@im.wechat",
		},
	})
	if err != nil {
		t.Fatalf("ReceiveRequest returned error: %v", err)
	}
	if !result.Accepted || result.Route != "clarify" {
		t.Fatalf("unexpected result: %#v", result)
	}

	waitForCondition(t, func() bool { return len(recorder.sendCalls) == 1 })
	if got := recorder.sendCalls[0].text; got != "主人，MTM 那边想问你：今天晚上的作业做完了吗？" {
		t.Fatalf("rewritten question = %q, want B-side rewritten voice", got)
	}
	if strings.Contains(recorder.sendCalls[0].text, "问一下NX1") {
		t.Fatalf("question still contains raw forwarded wording: %q", recorder.sendCalls[0].text)
	}
	if recorder.chatCalls != 1 {
		t.Fatalf("chatCalls = %d, want 1", recorder.chatCalls)
	}
}

func TestBridgeRuntimePeerUserProxyMetaReplyStaysLocal(t *testing.T) {
	recorder := &testBridgeRecorder{}
	runtime := NewBridgeRuntime(
		BridgeConfig{
			Enabled:           true,
			NodeID:            "remote-node",
			PeerNodeID:        "local-node",
			PeerBaseURL:       "http://peer.example",
			LocalUserID:       "local-user@im.wechat",
			LocalAgentAliases: []string{"幽浮喵", "UFO"},
		},
		BridgeRuntimeDeps{
			Chat: func(_ context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error) {
				recorder.chatCalls++
				if strings.Contains(message, "Allowed kinds: answer_pending_question, clarify_identity_and_reask, new_local_request.") {
					return &LocalAgentChatResult{
						AgentName: "codex",
						Reply:     `{"kind":"clarify_identity_and_reask","message":"主人，我是幽浮喵，MTM 那边想问你：今天晚上的作业做完了吗？","rationale":"meta clarification"}`,
					}, nil
				}
				return &LocalAgentChatResult{
					AgentName: "codex",
					Reply:     `{"action":"need_more_info_from_local_user","message":"主人，我是幽浮喵，MTM 那边想问你：今天晚上的作业做完了吗？","target_node":null,"rationale":"peer-user-proxy rewrite","follow_up_needed":true}`,
				}, nil
			},
			SendText: func(_ context.Context, accountID, toUserID, text, contextToken string) error {
				recorder.sendCalls = append(recorder.sendCalls, testBridgeSend{
					accountID:    accountID,
					toUserID:     toUserID,
					text:         text,
					contextToken: contextToken,
				})
				return nil
			},
			Dispatch: func(_ context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error) {
				recorder.dispatches = append(recorder.dispatches, request)
				return &TaskResult{TaskID: request.Envelope.MessageID, Status: "queued", Accepted: true, Detail: "accepted"}, nil
			},
		},
	)

	question, err := runtime.ReceiveRequest(context.Background(), TaskRequest{
		Envelope: newEnvelope("conv-proxy-2", "local-node", "remote-node", "local-user@im.wechat", "task-a", "task-a"),
		TaskType: "peer_user_question",
		Payload: map[string]any{
			"text":                  "问一下NX1，今天晚上的作业你有做完吗？",
			"question_text":         "今天晚上的作业做完了吗？",
			"requester_agent_label": "MTM",
			"requester_user_id":     "user-a@im.wechat",
		},
	})
	if err != nil {
		t.Fatalf("ReceiveRequest returned error: %v", err)
	}
	if !question.Accepted {
		t.Fatalf("question result = %#v, want accepted", question)
	}

	waitForCondition(t, func() bool { return len(recorder.sendCalls) == 1 })
	recorder.sendCalls = nil

	result, err := runtime.HandleWeClawInbound(context.Background(), WeClawInbound{
		AccountID:  "bot-remote",
		FromUserID: "local-user@im.wechat",
		Text:       "你是谁啊",
	})
	if err != nil {
		t.Fatalf("HandleWeClawInbound returned error: %v", err)
	}
	if !result.Accepted || result.Route != "clarify" {
		t.Fatalf("unexpected result: %#v", result)
	}
	waitForCondition(t, func() bool { return len(recorder.sendCalls) == 1 })
	if got := recorder.sendCalls[0].text; got != "主人，我是幽浮喵，MTM 那边想问你：今天晚上的作业做完了吗？" {
		t.Fatalf("clarification reply = %q, want local clarification", got)
	}
	if len(recorder.dispatches) != 0 {
		t.Fatalf("dispatches = %#v, want no peer dispatch for meta reply", recorder.dispatches)
	}
	pending := runtime.store.PendingForUser("local-user@im.wechat")
	if pending == nil {
		t.Fatal("pending proxy question was cleared, want it preserved")
	}
}

func TestBridgeRuntimePeerUserProxyAnswerForwardsToPeer(t *testing.T) {
	recorder := &testBridgeRecorder{}
	runtime := NewBridgeRuntime(
		BridgeConfig{
			Enabled:           true,
			NodeID:            "remote-node",
			PeerNodeID:        "local-node",
			PeerBaseURL:       "http://peer.example",
			LocalUserID:       "local-user@im.wechat",
			LocalAgentAliases: []string{"幽浮喵", "UFO"},
		},
		BridgeRuntimeDeps{
			Chat: func(_ context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error) {
				recorder.chatCalls++
				if strings.Contains(message, "Allowed kinds: answer_pending_question, clarify_identity_and_reask, new_local_request.") {
					return &LocalAgentChatResult{
						AgentName: "codex",
						Reply:     `{"kind":"answer_pending_question","message":"做完了","rationale":"direct answer"}`,
					}, nil
				}
				return &LocalAgentChatResult{
					AgentName: "codex",
					Reply:     `{"action":"need_more_info_from_local_user","message":"主人，我是幽浮喵，MTM 那边想问你：今天晚上的作业做完了吗？","target_node":null,"rationale":"peer-user-proxy rewrite","follow_up_needed":true}`,
				}, nil
			},
			SendText: func(_ context.Context, accountID, toUserID, text, contextToken string) error {
				recorder.sendCalls = append(recorder.sendCalls, testBridgeSend{
					accountID:    accountID,
					toUserID:     toUserID,
					text:         text,
					contextToken: contextToken,
				})
				return nil
			},
			Dispatch: func(_ context.Context, targetBaseURL string, request TaskRequest) (*TaskResult, error) {
				recorder.dispatches = append(recorder.dispatches, request)
				return &TaskResult{TaskID: request.Envelope.MessageID, Status: "queued", Accepted: true, Detail: "accepted"}, nil
			},
		},
	)

	question, err := runtime.ReceiveRequest(context.Background(), TaskRequest{
		Envelope: newEnvelope("conv-proxy-3", "local-node", "remote-node", "local-user@im.wechat", "task-a", "task-a"),
		TaskType: "peer_user_question",
		Payload: map[string]any{
			"question_text":         "今天晚上的作业做完了吗？",
			"requester_agent_label": "MTM",
			"requester_user_id":     "user-a@im.wechat",
		},
	})
	if err != nil {
		t.Fatalf("ReceiveRequest returned error: %v", err)
	}
	if !question.Accepted {
		t.Fatalf("question result = %#v, want accepted", question)
	}
	waitForCondition(t, func() bool {
		pending := runtime.store.PendingForUser("local-user@im.wechat")
		return pending != nil && len(recorder.sendCalls) == 1
	})
	recorder.sendCalls = nil

	result, err := runtime.HandleWeClawInbound(context.Background(), WeClawInbound{
		AccountID:  "bot-remote",
		FromUserID: "local-user@im.wechat",
		Text:       "做完了",
	})
	if err != nil {
		t.Fatalf("HandleWeClawInbound returned error: %v", err)
	}
	if !result.Accepted || result.Route != "peer" {
		t.Fatalf("unexpected result: %#v", result)
	}
	waitForCondition(t, func() bool { return len(recorder.dispatches) == 1 })
	if recorder.dispatches[0].TaskType != "peer_user_answer" {
		t.Fatalf("taskType = %q, want peer_user_answer", recorder.dispatches[0].TaskType)
	}
	if recorder.dispatches[0].Payload["text"] != "做完了" {
		t.Fatalf("forwarded answer = %#v, want 做完了", recorder.dispatches[0].Payload["text"])
	}
	if len(recorder.sendCalls) != 0 {
		t.Fatalf("sendCalls = %#v, want no local clarification for direct answer", recorder.sendCalls)
	}
}

func TestInboxStoreFiltersByAfterSeq(t *testing.T) {
	store := NewInboxStore(10)
	store.Append(InboxRecord{FromUserID: "a", Text: "one"})
	store.Append(InboxRecord{FromUserID: "b", Text: "two"})
	store.Append(InboxRecord{FromUserID: "a", Text: "three"})

	items := store.List("a", 1, 10)
	if len(items) != 1 || items[0].Text != "three" {
		t.Fatalf("unexpected inbox items: %#v", items)
	}
}
