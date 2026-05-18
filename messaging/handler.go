package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/controlplane"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/google/uuid"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveDefaultFunc persists the default agent name to config file.
type SaveDefaultFunc func(name string) error

// AgentMeta holds static config info about an agent (for /status display).
type AgentMeta struct {
	Name    string
	Type    string // "acp", "cli", "http"
	Command string // binary path or endpoint
	Model   string
}

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	mu                    sync.RWMutex
	laneMu                sync.Mutex
	defaultName           string
	agents                map[string]agent.Agent // name -> running agent
	agentMetas            []AgentMeta            // all configured agents (for /status)
	agentWorkDirs         map[string]string      // agent name -> configured/runtime cwd
	customAliases         map[string]string      // custom alias -> agent name (from config)
	factory               AgentFactory
	saveDefault           SaveDefaultFunc
	mediaService          *MediaServiceClient
	contextTokens         sync.Map // map[userID]contextToken
	saveDir               string   // directory to save images/files to
	seenMsgs              sync.Map // map[int64]time.Time — dedup by message_id
	bridge                *BridgeRuntime
	inbox                 *InboxStore
	userAgents            *controlplane.Service
	localTimeout          time.Duration
	backgroundTaskTimeout time.Duration
	slowReplyDelay        time.Duration
	userLanes             map[string]chan queuedInbound
	sessionBinds          map[string]string // WeChat user ID -> provider session/thread ID
	userModes             sync.Map          // map[userID]string, e.g. "plan"
}

type queuedInbound struct {
	ctx    context.Context
	client *ilink.Client
	msg    ilink.WeixinMessage
}

type LocalAgentChatResult struct {
	AgentName string
	Info      agent.AgentInfo
	Reply     string
}

type agentReplyResult struct {
	reply string
	err   error
}

var (
	handlerSendTextReply     = SendTextReply
	handlerSendMediaFromURL  = SendMediaFromURL
	handlerSendMediaFromPath = SendMediaFromPath
)

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveDefault SaveDefaultFunc) *Handler {
	return &Handler{
		agents:                make(map[string]agent.Agent),
		agentWorkDirs:         make(map[string]string),
		factory:               factory,
		saveDefault:           saveDefault,
		localTimeout:          2 * time.Minute,
		backgroundTaskTimeout: time.Hour,
		slowReplyDelay:        3 * time.Second,
		userLanes:             make(map[string]chan queuedInbound),
		sessionBinds:          loadSessionBindings(),
	}
}

func (h *Handler) SetDefaultAgentName(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
}

// SetSaveDir sets the directory for saving images and files.
func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

// SetBridge configures optional in-process A2A bridge runtime.
func (h *Handler) SetBridge(bridge *BridgeRuntime) {
	h.bridge = bridge
}

// SetInboxStore configures a recent inbound-message store for local automation and testing.
func (h *Handler) SetInboxStore(store *InboxStore) {
	h.inbox = store
}

func (h *Handler) SetUserAgents(service *controlplane.Service) {
	h.userAgents = service
}

func (h *Handler) ContextTokenForUser(userID string) string {
	if userID == "" {
		return ""
	}
	if value, ok := h.contextTokens.Load(userID); ok {
		if token, ok := value.(string); ok {
			return token
		}
	}
	return ""
}

func (h *Handler) forgetContextTokenForUser(client *ilink.Client, userID string) {
	if userID == "" {
		return
	}
	h.contextTokens.Delete(userID)
	if client != nil {
		clearStaleContextToken(client, userID, "handler")
	}
}

func (h *Handler) noteSendError(client *ilink.Client, userID string, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrInvalidContext) {
		h.forgetContextTokenForUser(client, userID)
	}
}

func (h *Handler) storeContextToken(client *ilink.Client, msg ilink.WeixinMessage) {
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)
	if err := ilink.SaveContextToken(client.BotID(), msg.FromUserID, msg.ContextToken); err != nil {
		log.Printf("[handler] failed to save context token for %s: %v", msg.FromUserID, err)
	}
	go RetryPendingDeliveries(context.Background(), client, msg.FromUserID, msg.ContextToken)
}

// cleanSeenMsgs removes entries older than 5 minutes from the dedup cache.
func (h *Handler) cleanSeenMsgs() {
	cutoff := time.Now().Add(-5 * time.Minute)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
}

// SetCustomAliases sets custom alias mappings from config.
func (h *Handler) SetCustomAliases(aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customAliases = aliases
}

// SetAgentMetas sets the list of all configured agents (for /status).
func (h *Handler) SetAgentMetas(metas []AgentMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentMetas = metas
}

// SetAgentWorkDirs sets the configured working directory for each agent.
func (h *Handler) SetAgentWorkDirs(workDirs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agentWorkDirs = make(map[string]string, len(workDirs))
	for name, dir := range workDirs {
		h.agentWorkDirs[name] = dir
	}
}

// SetDefaultAgent sets the default agent (already started).
func (h *Handler) SetDefaultAgent(name string, ag agent.Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
	h.agents[name] = ag
	log.Printf("[handler] default agent ready: %s (%s)", name, ag.Info())
}

// getAgent returns a running agent by name, or starts it on demand via factory.
func (h *Handler) getAgent(ctx context.Context, name string) (agent.Agent, error) {
	// Fast path: already running
	h.mu.RLock()
	ag, ok := h.agents[name]
	h.mu.RUnlock()
	if ok {
		return ag, nil
	}

	// Slow path: create on demand
	if h.factory == nil {
		return nil, fmt.Errorf("agent %q not found and no factory configured", name)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if ag, ok := h.agents[name]; ok {
		return ag, nil
	}

	log.Printf("[handler] starting agent %q on demand...", name)
	ag = h.factory(ctx, name)
	if ag == nil {
		return nil, fmt.Errorf("agent %q not available", name)
	}

	h.agents[name] = ag
	log.Printf("[handler] agent started on demand: %s (%s)", name, ag.Info())
	return ag, nil
}

// getDefaultAgent returns the default agent (may be nil if not ready yet).
func (h *Handler) getDefaultAgent() agent.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" {
		return nil
	}
	return h.agents[h.defaultName]
}

// isKnownAgent checks if a name corresponds to a configured agent.
func (h *Handler) isKnownAgent(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	// Check running agents
	if _, ok := h.agents[name]; ok {
		return true
	}
	// Check configured agents (metas)
	for _, meta := range h.agentMetas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

// agentAliases maps short aliases to agent config names.
var agentAliases = map[string]string{
	"cc":  "claude",
	"cx":  "codex",
	"oc":  "openclaw",
	"cs":  "cursor",
	"km":  "kimi",
	"gm":  "gemini",
	"ocd": "opencode",
	"pi":  "pi",
	"cp":  "copilot",
	"dr":  "droid",
	"if":  "iflow",
	"kr":  "kiro",
	"qw":  "qwen",
}

// resolveAlias returns the full agent name for an alias, or the original name if no alias matches.
// Checks custom aliases (from config) first, then built-in aliases.
func (h *Handler) resolveAlias(name string) string {
	h.mu.RLock()
	custom := h.customAliases
	h.mu.RUnlock()
	if custom != nil {
		if full, ok := custom[name]; ok {
			return full
		}
	}
	if full, ok := agentAliases[name]; ok {
		return full
	}
	return name
}

// parseCommand checks if text starts with "/" or "@" followed by agent name(s).
// Supports multiple agents: "@cc @cx hello" returns (["claude","codex"], "hello").
// Returns (agentNames, actualMessage). Aliases are resolved automatically.
// If no command prefix, returns (nil, originalText).
func (h *Handler) parseCommand(text string) ([]string, string) {
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
		return nil, text
	}

	// Parse consecutive @name or /name tokens from the start
	var names []string
	rest := text
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "@") {
			break
		}

		// Strip prefix
		after := rest[1:]
		idx := strings.IndexAny(after, " /@")
		var token string
		if idx < 0 {
			// Rest is just the name, no message
			token = after
			rest = ""
		} else if after[idx] == '/' || after[idx] == '@' {
			// Next token is another @name or /name
			token = after[:idx]
			rest = after[idx:]
		} else {
			// Space — name ends here
			token = after[:idx]
			rest = strings.TrimSpace(after[idx+1:])
		}

		if token != "" {
			names = append(names, h.resolveAlias(token))
		}

		if rest == "" {
			break
		}
	}

	// Deduplicate names preserving order
	seen := make(map[string]bool)
	unique := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	return unique, rest
}

// HandleMessage processes a single incoming message.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	h.enqueueMessage(ctx, client, msg)
}

func (h *Handler) enqueueMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	key := client.BotID() + "|" + msg.FromUserID

	h.laneMu.Lock()
	lane := h.userLanes[key]
	if lane == nil {
		lane = make(chan queuedInbound, 64)
		h.userLanes[key] = lane
		go h.runUserLane(key, lane)
	}
	h.laneMu.Unlock()

	lane <- queuedInbound{ctx: ctx, client: client, msg: msg}
}

func (h *Handler) runUserLane(key string, lane chan queuedInbound) {
	for item := range lane {
		h.processMessage(item.ctx, item.client, item.msg)
	}
}

func (h *Handler) processMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return
	}

	// Deduplicate by message_id to avoid processing the same message multiple times
	// (voice messages may trigger multiple finish-state updates)
	if msg.MessageID != 0 {
		if _, loaded := h.seenMsgs.LoadOrStore(msg.MessageID, time.Now()); loaded {
			return
		}
		// Clean up old entries periodically (fire-and-forget)
		go h.cleanSeenMsgs()
	}

	// Extract text from item list (text message or voice transcription)
	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] voice transcription from %s: %q", msg.FromUserID, truncate(text, 80))
		}
	}
	ownerMessage := h.userAgents != nil && h.userAgents.IsOwnerContact(client.BotID(), msg.FromUserID)
	if ownerMessage && strings.TrimSpace(text) != "" && !isBuiltInCommand(strings.TrimSpace(text)) && !h.hasSessionBinding(msg.FromUserID) {
		log.Printf("[handler] owner message routed to user-agent control plane account=%s from=%s", client.BotID(), msg.FromUserID)
		h.storeContextToken(client, msg)
		clientID := NewClientID()
		h.handleOwnerMessage(ctx, client, msg, strings.TrimSpace(text), clientID)
		return
	}
	if h.mediaService != nil {
		incomingMedia, hasMedia, err := h.collectIncomingRichMedia(ctx, msg)
		if err != nil {
			log.Printf("[handler] failed to collect incoming rich media from %s: %v", msg.FromUserID, err)
		} else if hasMedia {
			if incomingMedia.userText == "" {
				incomingMedia.userText = text
			}
			h.handleRichMediaMessage(ctx, client, msg, incomingMedia)
			return
		}
	}
	if text == "" {
		// Check for image message
		if img := extractImage(msg); img != nil && h.saveDir != "" {
			h.handleImageSave(ctx, client, msg, img)
			return
		}
		log.Printf("[handler] received non-text message from %s, skipping", msg.FromUserID)
		return
	}

	log.Printf("[handler] received from %s: %q", msg.FromUserID, truncate(text, 80))

	// Store context token for this user
	h.storeContextToken(client, msg)

	// Generate a clientID for this reply (used to correlate typing → finish)
	clientID := NewClientID()
	trimmed := strings.TrimSpace(text)

	if ownerMessage && !isBuiltInCommand(trimmed) && !h.hasSessionBinding(msg.FromUserID) {
		h.handleOwnerMessage(ctx, client, msg, trimmed, clientID)
		return
	}

	// Intercept URLs: save to Linkhoard directly without AI agent
	if h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] saving URL to linkhoard: %s", rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			var reply string
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
			}
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
				h.noteSendError(client, msg.FromUserID, err)
			}
			return
		}
	}

	// Built-in commands (no typing needed)
	if trimmed == "/info" {
		reply := h.buildStatus()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
		}
		return
	} else if trimmed == "/help" {
		reply := buildHelpText()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
		}
		return
	} else if trimmed == "/plan" {
		h.setUserMode(msg.FromUserID, "plan")
		reply := "已进入 Plan Mode：后续消息只会要求 Agent 做分析和计划，不执行修改。发送 /exec 恢复执行模式。"
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
		}
		return
	} else if trimmed == "/exec" || trimmed == "/execute" {
		h.clearUserMode(msg.FromUserID)
		reply := "已切回执行模式。"
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
		}
		return
	} else if trimmed == "/new" || trimmed == "/clear" {
		reply := h.resetDefaultSession(ctx, msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
		}
		return
	} else if strings.HasPrefix(trimmed, "/cwd") {
		reply := h.handleCwd(trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
		}
		return
	} else if strings.HasPrefix(trimmed, "/session") {
		reply := h.handleSession(ctx, msg.FromUserID, trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
		}
		return
	}

	// Route: "/agentname message" or "@agent1 @agent2 message" -> specific agent(s)
	agentNames, message := h.parseCommand(text)
	if len(agentNames) == 0 {
		if h.inbox != nil {
			normalized := text
			suppressed := false
			if h.bridge != nil {
				normalized, suppressed = h.bridge.NormalizeIncomingText(text)
			}
			h.inbox.Append(InboxRecord{
				MessageID:    msg.MessageID,
				FromUserID:   msg.FromUserID,
				ToUserID:     msg.ToUserID,
				Text:         normalized,
				ContextToken: msg.ContextToken,
				Suppressed:   suppressed,
			})
		}

		if h.bridge != nil {
			result, err := h.bridge.HandleWeClawInbound(ctx, WeClawInbound{
				AccountID:    client.BotID(),
				FromUserID:   msg.FromUserID,
				Text:         text,
				RouteMode:    "auto",
				MessageID:    fmt.Sprintf("%d", msg.MessageID),
				ContextToken: msg.ContextToken,
			})
			if err != nil {
				log.Printf("[handler] bridge runtime failed for %s: %v", msg.FromUserID, err)
				return
			}
			if result != nil {
				if h.inbox != nil && result.Route == "peer" {
					h.inbox.Append(InboxRecord{
						MessageID:    msg.MessageID,
						FromUserID:   msg.FromUserID,
						ToUserID:     msg.ToUserID,
						Text:         text,
						ContextToken: msg.ContextToken,
						Bridged:      true,
					})
				}
				if result.Route == "suppressed" {
					log.Printf("[handler] suppressed bridge-prefixed message from %s", msg.FromUserID)
				} else {
					log.Printf("[handler] bridge runtime handled message from %s (%s)", msg.FromUserID, result.Detail)
				}
			}
			return
		}

		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	// No message -> switch default agent (only first name)
	if message == "" {
		if len(agentNames) == 1 && h.isKnownAgent(agentNames[0]) {
			reply := h.switchDefault(ctx, agentNames[0])
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
				h.noteSendError(client, msg.FromUserID, err)
			}
		} else if len(agentNames) == 1 && !h.isKnownAgent(agentNames[0]) {
			// Unknown agent -> forward to default
			h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		} else {
			reply := "Usage: specify one agent to switch, or add a message to broadcast"
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
				h.noteSendError(client, msg.FromUserID, err)
			}
		}
		return
	}

	// Filter to known agents; if single unknown agent -> forward to default
	var knownNames []string
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}
	if len(knownNames) == 0 {
		// No known agents -> forward entire text to default agent
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	// Send typing indicator
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	if len(knownNames) == 1 {
		// Single agent
		h.sendToNamedAgent(ctx, client, msg, knownNames[0], message, clientID)
	} else {
		// Multi-agent broadcast: parallel dispatch, send replies as they arrive
		h.broadcastToAgents(ctx, client, msg, knownNames, message)
	}
}

func (h *Handler) handleOwnerMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, trimmed, clientID string) {
	if h.userAgents != nil {
		commands, expandErr := h.userAgents.ExpandIngressCommands(client.BotID(), trimmed)
		if expandErr == nil && len(commands) > 0 {
			if handled, reply := h.handleOwnerIngressCommands(ctx, client, msg, commands); handled {
				_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
				return
			}
		}
	}

	switch {
	case trimmed == "/help" || trimmed == "/ua-help":
		_ = SendTextReply(ctx, client, msg.FromUserID, buildOwnerHelpText(), msg.ContextToken, clientID)
		return

	case trimmed == "/tasks":
		tasks, err := h.userAgents.ListTasksForAccount(client.BotID(), 20)
		if err != nil {
			_ = SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("读取任务失败：%v", err), msg.ContextToken, clientID)
			return
		}
		reply := formatOwnerTaskList(client.BotID(), tasks)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return

	case trimmed == "/agents":
		profiles, err := h.userAgents.ListProfiles()
		if err != nil {
			_ = SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("读取 Agent 列表失败：%v", err), msg.ContextToken, clientID)
			return
		}
		_ = SendTextReply(ctx, client, msg.FromUserID, formatOwnerAgentList(profiles), msg.ContextToken, clientID)
		return

	case trimmed == "/capabilities":
		bindings, err := h.userAgents.ListCapabilityBindings(client.BotID())
		if err != nil {
			_ = SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("读取能力失败：%v", err), msg.ContextToken, clientID)
			return
		}
		_ = SendTextReply(ctx, client, msg.FromUserID, formatOwnerCapabilities(bindings), msg.ContextToken, clientID)
		return

	case strings.HasPrefix(trimmed, "/approve "):
		approvalID := strings.TrimSpace(strings.TrimPrefix(trimmed, "/approve"))
		grant, err := h.userAgents.ApproveGrant(ctx, approvalID, msg.FromUserID)
		if err != nil {
			_ = SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("批准失败：%v", err), msg.ContextToken, clientID)
			return
		}
		reply := fmt.Sprintf("已批准协作请求 %s，任务开始执行。", shortTaskID(grant.ID))
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return

	case strings.HasPrefix(trimmed, "/reject "):
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "/reject"))
		parts := strings.SplitN(payload, " ", 2)
		approvalID := strings.TrimSpace(parts[0])
		reason := ""
		if len(parts) == 2 {
			reason = strings.TrimSpace(parts[1])
		}
		grant, err := h.userAgents.RejectGrant(ctx, approvalID, msg.FromUserID, reason)
		if err != nil {
			_ = SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("拒绝失败：%v", err), msg.ContextToken, clientID)
			return
		}
		reply := fmt.Sprintf("已拒绝协作请求 %s。", shortTaskID(grant.ID))
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return

	case strings.HasPrefix(trimmed, "/delegate "):
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "/delegate"))
		parts := strings.SplitN(payload, " ", 2)
		if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			_ = SendTextReply(ctx, client, msg.FromUserID, "用法：/delegate 目标账号或显示名 任务内容", msg.ContextToken, clientID)
			return
		}
		task, grant, err := h.userAgents.CreateDelegation(ctx, client.BotID(), msg.FromUserID, parts[0], parts[1])
		if err != nil {
			_ = SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("发起协作失败：%v", err), msg.ContextToken, clientID)
			return
		}
		reply := fmt.Sprintf("已发起协作任务 %s。\n审批码：%s\n等待对方批准后执行。", shortTaskID(task.ID), shortTaskID(grant.ID))
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	task, err := h.userAgents.SubmitOwnerTask(ctx, client.BotID(), msg.FromUserID, trimmed)
	if err != nil {
		_ = SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("处理失败：%v", err), msg.ContextToken, clientID)
		return
	}
	reply := fmt.Sprintf("[任务 %s]\n%s", shortTaskID(task.ID), h.userAgents.FormatTaskReply(task))
	h.sendReplyWithMedia(ctx, client, msg, task.AssignedAgentName, reply, clientID)
}

func (h *Handler) handleOwnerIngressCommands(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, commands []string) (bool, string) {
	if h.userAgents == nil || len(commands) == 0 {
		return false, ""
	}

	var replies []string
	for _, command := range commands {
		decision, err := h.userAgents.ResolveIngressDecision(client.BotID(), msg.FromUserID, command)
		if err != nil {
			replies = append(replies, fmt.Sprintf("处理失败：%v", err))
			return true, strings.Join(replies, "\n")
		}
		switch decision.Kind {
		case controlplane.IngressDecisionClarify:
			replies = append(replies, decision.ClarificationText)
			return true, strings.Join(replies, "\n")
		case controlplane.IngressDecisionApproval:
			if decision.ApprovalAction == "approve" {
				resolvedBy := msg.FromUserID
				if strings.TrimSpace(decision.ApprovalReason) != "" {
					resolvedBy = decision.ApprovalReason
				}
				grant, approveErr := h.userAgents.ApproveGrant(ctx, decision.ApprovalID, resolvedBy)
				if approveErr != nil {
					replies = append(replies, fmt.Sprintf("批准失败：%v", approveErr))
					return true, strings.Join(replies, "\n")
				}
				replies = append(replies, fmt.Sprintf("已批准协作请求 %s。", shortTaskID(grant.ID)))
				continue
			}
			if decision.ApprovalAction == "reject" {
				grant, rejectErr := h.userAgents.RejectGrant(ctx, decision.ApprovalID, msg.FromUserID, decision.ApprovalReason)
				if rejectErr != nil {
					replies = append(replies, fmt.Sprintf("拒绝失败：%v", rejectErr))
					return true, strings.Join(replies, "\n")
				}
				replies = append(replies, fmt.Sprintf("已拒绝协作请求 %s。", shortTaskID(grant.ID)))
				continue
			}
		default:
			if len(replies) == 0 {
				return false, ""
			}
			replies = append(replies, fmt.Sprintf("未识别命令：%s", command))
			return true, strings.Join(replies, "\n")
		}
	}

	if len(replies) == 0 {
		return false, ""
	}
	return true, strings.Join(replies, "\n")
}

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) {
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	h.mu.RLock()
	defaultName := h.defaultName
	h.mu.RUnlock()

	ag := h.getDefaultAgent()
	if ag != nil {
		if sessionID := h.sessionBindingFor(msg.FromUserID); sessionID != "" {
			if err := ag.UseSession(ctx, msg.FromUserID, sessionID); err != nil {
				log.Printf("[handler] failed to apply session binding for %s: %v", msg.FromUserID, err)
			}
		}
		h.sendAgentReplyWithBackground(ctx, client, msg, defaultName, ag, text, clientID, "")
		return
	}

	log.Printf("[handler] agent not ready, using echo mode for %s", msg.FromUserID)
	reply := "[echo] " + text
	h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, name, message, clientID string) {
	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := fmt.Sprintf("Agent %q is not available: %v", name, agErr)
		h.sendTextReplyWithPending(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	h.sendAgentReplyWithBackground(ctx, client, msg, name, ag, message, clientID, "")
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, names []string, message string) {
	for _, name := range names {
		ag, err := h.getAgent(ctx, name)
		if err != nil {
			reply := fmt.Sprintf("[%s] Error: %v", name, err)
			h.sendReplyWithMedia(ctx, client, msg, name, reply, NewClientID())
			continue
		}
		h.sendAgentReplyWithBackground(ctx, client, msg, name, ag, message, NewClientID(), fmt.Sprintf("[%s] ", name))
	}
}

func (h *Handler) sendAgentReplyWithBackground(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName string, ag agent.Agent, message, clientID, replyPrefix string) {
	resultCh := make(chan agentReplyResult, 1)
	bgCtx, cancel := h.withBackgroundAgentTimeout()
	go func() {
		defer cancel()
		reply, err := h.chatWithAgent(bgCtx, ag, msg.FromUserID, message)
		resultCh <- agentReplyResult{reply: reply, err: err}
	}()

	stopNotice := h.startSlowReplyNotice(ctx, client, msg)
	foregroundTimer := time.NewTimer(h.localTimeout)
	defer foregroundTimer.Stop()

	select {
	case result := <-resultCh:
		stopNotice()
		reply := result.reply
		if result.err != nil {
			reply = formatAgentError(result.err, h.backgroundTaskTimeout)
		}
		h.sendReplyWithMedia(ctx, client, msg, agentName, replyPrefix+reply, clientID)
	case <-foregroundTimer.C:
		stopNotice()
		notice := "任务仍在后台执行，完成后会自动发送结果。"
		h.sendTextReplyWithPending(ctx, client, msg.FromUserID, notice, msg.ContextToken, NewClientID())
		go h.deliverBackgroundAgentResult(client, msg, agentName, resultCh, replyPrefix)
	case <-ctx.Done():
		stopNotice()
		go h.deliverBackgroundAgentResult(client, msg, agentName, resultCh, replyPrefix)
	}
}

func (h *Handler) deliverBackgroundAgentResult(client *ilink.Client, msg ilink.WeixinMessage, agentName string, resultCh <-chan agentReplyResult, replyPrefix string) {
	result := <-resultCh
	reply := result.reply
	if result.err != nil {
		reply = formatAgentError(result.err, h.backgroundTaskTimeout)
	}
	h.sendReplyWithMedia(context.Background(), client, msg, agentName, replyPrefix+reply, NewClientID())
}

func (h *Handler) withBackgroundAgentTimeout() (context.Context, context.CancelFunc) {
	if h.backgroundTaskTimeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), h.backgroundTaskTimeout)
}

// sendReplyWithMedia sends a text reply and any extracted media references.
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName, reply, clientID string) {
	imageURLs := ExtractImageURLs(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	allowedRoots := h.allowedAttachmentRoots(agentName)

	var sentPaths, failedPaths []string
	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			log.Printf("[handler] rejected attachment outside allowed roots for agent %q: %s", agentName, attachmentPath)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		if err := handlerSendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
			h.queuePendingSend(client, msg.FromUserID, "", "", attachmentPath, err)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}
	textReply := rewriteReplyWithAttachmentResults(reply, sentPaths, failedPaths)
	h.sendTextReplyWithPending(ctx, client, msg.FromUserID, textReply, msg.ContextToken, clientID)

	for _, imgURL := range imageURLs {
		if err := handlerSendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send image to %s: %v", msg.FromUserID, err)
			h.noteSendError(client, msg.FromUserID, err)
			h.queuePendingSend(client, msg.FromUserID, "", imgURL, "", err)
		}
	}
}

func (h *Handler) sendTextReplyWithPending(ctx context.Context, client *ilink.Client, toUserID, text, contextToken, clientID string) {
	if err := handlerSendTextReply(ctx, client, toUserID, text, contextToken, clientID); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", toUserID, err)
		h.noteSendError(client, toUserID, err)
		h.queuePendingSend(client, toUserID, text, "", "", err)
	}
}

func (h *Handler) queuePendingSend(client *ilink.Client, toUserID, text, mediaURL, mediaPath string, cause error) {
	if !shouldQueuePendingOutgoing(cause) {
		return
	}
	accountID := ""
	if client != nil {
		accountID = client.BotID()
	}
	path, err := SavePendingOutgoingSend(accountID, toUserID, text, mediaURL, mediaPath, cause.Error())
	if err != nil {
		log.Printf("[handler] queue pending send failed: %v", err)
		return
	}
	if path != "" {
		log.Printf("[handler] queued pending send: %s", path)
	}
}

func shouldQueuePendingOutgoing(cause error) bool {
	if cause == nil {
		return false
	}
	if errors.Is(cause, ErrInvalidContext) {
		return true
	}
	message := cause.Error()
	return strings.Contains(message, "ret=-2") ||
		strings.Contains(message, "CDN upload HTTP 5") ||
		strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "EOF")
}

func (h *Handler) allowedAttachmentRoots(agentName string) []string {
	roots := []string{defaultAttachmentWorkspace()}
	roots = append(roots, defaultUserAttachmentRoots()...)
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		roots = append(roots, cwd)
	}

	h.mu.RLock()
	agentDir := h.agentWorkDirs[agentName]
	h.mu.RUnlock()

	if agentDir != "" {
		roots = append(roots, agentDir)
	}

	return roots
}

// chatWithAgent sends a message to an agent and returns the reply, with logging.
func (h *Handler) chatWithAgent(ctx context.Context, ag agent.Agent, userID, message string) (string, error) {
	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)

	message = h.messageForUserMode(userID, message)

	start := time.Now()
	reply, err := ag.Chat(ctx, userID, message)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] agent error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	log.Printf("[handler] agent replied (%s, elapsed=%s): %q", info, elapsed, truncate(reply, 100))
	return reply, nil
}

func (h *Handler) ChatLocalAgent(ctx context.Context, conversationID, message, agentName string) (*LocalAgentChatResult, error) {
	name := strings.TrimSpace(agentName)
	if name == "" {
		h.mu.RLock()
		name = h.defaultName
		h.mu.RUnlock()
	}
	if name == "" {
		return nil, fmt.Errorf("no default agent configured")
	}

	ag, err := h.getAgent(ctx, name)
	if err != nil {
		return nil, err
	}

	chatCtx, cancel := h.withLocalAgentTimeout(ctx)
	defer cancel()

	reply, err := h.chatWithAgent(chatCtx, ag, conversationID, message)
	if err != nil {
		return nil, err
	}
	return &LocalAgentChatResult{
		AgentName: name,
		Info:      ag.Info(),
		Reply:     reply,
	}, nil
}

func (h *Handler) HandleBridgeRequest(ctx context.Context, request TaskRequest) (*TaskResult, error) {
	if h.bridge == nil {
		return nil, fmt.Errorf("bridge runtime unavailable")
	}
	return h.bridge.ReceiveRequest(ctx, request)
}

func (h *Handler) withLocalAgentTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if h.localTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, h.localTimeout)
}

func (h *Handler) startSlowReplyNotice(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) func() {
	if h.slowReplyDelay <= 0 {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(h.slowReplyDelay)
		defer timer.Stop()

		select {
		case <-timer.C:
			notice := "已收到，正在处理中，请稍候。"
			h.sendTextReplyWithPending(ctx, client, msg.FromUserID, notice, msg.ContextToken, NewClientID())
		case <-done:
		case <-ctx.Done():
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
		})
	}
}

func formatAgentError(err error, timeout time.Duration) string {
	if errors.Is(err, context.DeadlineExceeded) {
		if timeout > 0 {
			return fmt.Sprintf("处理超时（超过 %s），请缩小问题范围后重试。", timeout.Round(time.Second))
		}
		return "处理超时，请稍后重试。"
	}
	return fmt.Sprintf("Error: %v", err)
}

// switchDefault switches the default agent. Starts it on demand if needed.
// The change is persisted to config file.
func (h *Handler) switchDefault(ctx context.Context, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("Failed to switch to %q: %v", name, err)
	}

	h.mu.Lock()
	old := h.defaultName
	h.defaultName = name
	h.agents[name] = ag
	h.mu.Unlock()

	// Persist to config file
	if h.saveDefault != nil {
		if err := h.saveDefault(name); err != nil {
			log.Printf("[handler] failed to save default agent to config: %v", err)
		} else {
			log.Printf("[handler] saved default agent %q to config", name)
		}
	}

	info := ag.Info()
	log.Printf("[handler] switched default agent: %s -> %s (%s)", old, name, info)
	return fmt.Sprintf("switch to %s", name)
}

// resetDefaultSession resets the session for the given userID on the default agent.
func (h *Handler) resetDefaultSession(ctx context.Context, userID string) string {
	ag, name, err := h.defaultAgentForCommand(ctx)
	if err != nil {
		return err.Error()
	}

	oldSessionID := h.currentSessionID(ag, userID)
	sessionID, err := ag.ResetSession(ctx, userID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", userID, err)
		if oldSessionID != "" {
			return fmt.Sprintf("Failed to reset session: %v\n当前仍停留在原会话\n%s", err, oldSessionID)
		}
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	if sessionID != "" {
		if err := h.setSessionBinding(userID, sessionID); err != nil {
			log.Printf("[handler] failed to persist new session binding for %s: %v", userID, err)
		}
		return formatNewSessionReply(name, sessionID, oldSessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
func (h *Handler) handleCwd(trimmed string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		// No path provided — show current cwd of default agent
		ag := h.getDefaultAgent()
		if ag == nil {
			return "No agent running."
		}
		info := ag.Info()
		return fmt.Sprintf("cwd: (check agent config)\nagent: %s", info.Name)
	}

	// Expand ~ to home directory
	if arg == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = home
		}
	} else if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = filepath.Join(home, arg[2:])
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Sprintf("Invalid path: %v", err)
	}

	// Verify directory exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Sprintf("Path not found: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Sprintf("Not a directory: %s", absPath)
	}

	// Update cwd on all running agents
	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()

	for name, ag := range agents {
		ag.SetCwd(absPath)
		log.Printf("[handler] updated cwd for agent %s: %s", name, absPath)
	}

	h.mu.Lock()
	for name := range agents {
		h.agentWorkDirs[name] = absPath
	}
	h.mu.Unlock()

	return fmt.Sprintf("cwd: %s", absPath)
}

// handleSession binds the current WeChat conversation to an existing agent session.
func (h *Handler) handleSession(ctx context.Context, userID string, trimmed string) string {
	sessionID := strings.TrimSpace(strings.TrimPrefix(trimmed, "/session"))
	if sessionID == "" {
		return "Usage: /session list | /session <codex-thread-id-or-session-id>"
	}
	switch strings.ToLower(sessionID) {
	case "new":
		return "请使用 /new 创建新的 Codex 会话。"
	case "list":
		return h.handleSessionList(userID)
	}

	ag, _, err := h.defaultAgentForCommand(ctx)
	if err != nil {
		return err.Error()
	}

	info := ag.Info()
	if err := ag.UseSession(ctx, userID, sessionID); err != nil {
		log.Printf("[handler] bind session failed for %s: %v", userID, err)
		return fmt.Sprintf("Failed to bind session: %v", err)
	}
	if err := h.setSessionBinding(userID, sessionID); err != nil {
		log.Printf("[handler] failed to persist session binding for %s: %v", userID, err)
	}
	if info.Type == "acp" && strings.Contains(filepath.Base(info.Command), "codex") {
		return fmt.Sprintf("已接入 Codex 会话\n%s", sessionID)
	}
	return fmt.Sprintf("已接入%s会话\n%s", info.Name, sessionID)
}

func (h *Handler) handleSessionList(userID string) string {
	sessions, err := listLocalCodexThreads(20)
	if err != nil {
		log.Printf("[handler] list sessions failed for %s: %v", userID, err)
		return fmt.Sprintf("Failed to list sessions: %v", err)
	}
	if len(sessions) == 0 {
		return "没有找到本机 Codex Thread 历史。"
	}

	currentID := h.sessionBindingFor(userID)
	if ag := h.getDefaultAgent(); ag != nil {
		if inspector, ok := ag.(agent.SessionInspector); ok {
			if sessionID := inspector.CurrentSessionID(userID); sessionID != "" {
				currentID = sessionID
			}
		}
	}
	lines := []string{"本机最近 Codex Thread:"}
	for i, session := range sessions {
		marker := ""
		if session.ID == currentID {
			marker = " *当前"
		}
		updated := ""
		if !session.UpdatedAt.IsZero() {
			updated = " " + session.UpdatedAt.Format("01-02 15:04")
		}
		lines = append(lines, fmt.Sprintf("%d. %s%s%s", i+1, session.ID, marker, updated))
	}
	lines = append(lines, "\n切换: /session <id>")
	return strings.Join(lines, "\n")
}

type localCodexThread struct {
	ID        string
	UpdatedAt time.Time
}

func listLocalCodexThreads(limit int) ([]localCodexThread, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".codex", "sessions")
	entries := make([]localCodexThread, 0)
	seen := make(map[string]bool)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		threadID := codexThreadIDFromSessionFile(entry.Name())
		if threadID == "" || seen[threadID] {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		seen[threadID] = true
		entries = append(entries, localCodexThread{
			ID:        threadID,
			UpdatedAt: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func codexThreadIDFromSessionFile(name string) string {
	const uuidLen = 36
	name = strings.TrimSuffix(name, ".jsonl")
	if len(name) < uuidLen {
		return ""
	}
	candidate := name[len(name)-uuidLen:]
	if !isUUIDLike(candidate) {
		return ""
	}
	return candidate
}

func isUUIDLike(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}

func (h *Handler) defaultAgentForCommand(ctx context.Context) (agent.Agent, string, error) {
	h.mu.RLock()
	name := h.defaultName
	h.mu.RUnlock()
	if name == "" {
		return nil, "", fmt.Errorf("No agent configured.")
	}
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to start agent %q for session command: %v", name, err)
		return nil, "", fmt.Errorf("Failed to start agent %q: %v", name, err)
	}
	return ag, name, nil
}

func (h *Handler) currentSessionID(ag agent.Agent, userID string) string {
	if inspector, ok := ag.(agent.SessionInspector); ok {
		if sessionID := inspector.CurrentSessionID(userID); sessionID != "" {
			return sessionID
		}
	}
	return h.sessionBindingFor(userID)
}

func formatNewSessionReply(agentName, newSessionID, oldSessionID string) string {
	if agentName == "" {
		agentName = "Agent"
	}
	if oldSessionID == "" {
		return fmt.Sprintf("已创建新的%s会话\n\n当前会话:\n%s\n\n未找到可切回的上一个会话。", agentName, newSessionID)
	}
	return fmt.Sprintf("已创建新的%s会话\n\n当前会话:\n%s\n\n上一个会话:\n%s\n\n切回上一个会话:\n/session %s", agentName, newSessionID, oldSessionID, oldSessionID)
}

func (h *Handler) hasSessionBinding(userID string) bool {
	return h.sessionBindingFor(userID) != ""
}

func (h *Handler) sessionBindingFor(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessionBinds[userID]
}

func (h *Handler) setSessionBinding(userID string, sessionID string) error {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	if userID == "" || sessionID == "" {
		return nil
	}

	h.mu.Lock()
	h.sessionBinds[userID] = sessionID
	snapshot := make(map[string]string, len(h.sessionBinds))
	for key, value := range h.sessionBinds {
		snapshot[key] = value
	}
	h.mu.Unlock()

	return saveSessionBindings(snapshot)
}

func sessionBindingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "session-bindings.json"), nil
}

func loadSessionBindings() map[string]string {
	bindings := make(map[string]string)
	path, err := sessionBindingsPath()
	if err != nil {
		return bindings
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[handler] failed to read session bindings: %v", err)
		}
		return bindings
	}
	if err := json.Unmarshal(data, &bindings); err != nil {
		log.Printf("[handler] failed to parse session bindings: %v", err)
		return make(map[string]string)
	}
	return bindings
}

func saveSessionBindings(bindings map[string]string) error {
	path, err := sessionBindingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(bindings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func isBuiltInCommand(trimmed string) bool {
	return trimmed == "/info" ||
		trimmed == "/help" ||
		trimmed == "/plan" ||
		trimmed == "/exec" ||
		trimmed == "/execute" ||
		trimmed == "/new" ||
		trimmed == "/clear" ||
		strings.HasPrefix(trimmed, "/cwd") ||
		strings.HasPrefix(trimmed, "/session")
}

func (h *Handler) setUserMode(userID string, mode string) {
	userID = strings.TrimSpace(userID)
	mode = strings.TrimSpace(mode)
	if userID == "" || mode == "" {
		return
	}
	h.userModes.Store(userID, mode)
}

func (h *Handler) clearUserMode(userID string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	h.userModes.Delete(userID)
}

func (h *Handler) userMode(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	value, ok := h.userModes.Load(userID)
	if !ok {
		return ""
	}
	mode, _ := value.(string)
	return mode
}

func (h *Handler) messageForUserMode(userID string, message string) string {
	if h.userMode(userID) != "plan" && h.userMode(ownerUserIDFromConversation(userID)) != "plan" {
		return message
	}
	return `当前微信会话处于 Plan Mode。
请只做只读分析、需求澄清和实施计划；不要修改文件、不要执行有副作用的命令、不要提交或推送代码。
如果用户要求执行，请提醒用户先发送 /exec 切回执行模式。

用户消息：
` + message
}

func ownerUserIDFromConversation(conversationID string) string {
	parts := strings.SplitN(strings.TrimSpace(conversationID), ":", 3)
	if len(parts) == 3 && parts[0] == "owner" {
		return strings.TrimSpace(parts[2])
	}
	return ""
}

// buildStatus returns a short status string showing the current default agent.
func (h *Handler) buildStatus() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.defaultName == "" {
		return "agent: none (echo mode)"
	}

	ag, ok := h.agents[h.defaultName]
	if !ok {
		return fmt.Sprintf("agent: %s (not started)", h.defaultName)
	}

	info := ag.Info()
	return fmt.Sprintf("agent: %s\ntype: %s\nmodel: %s", h.defaultName, info.Type, info.Model)
}

func buildHelpText() string {
	return `Available commands:
@agent or /agent - Switch default agent
@agent msg or /agent msg - Send to a specific agent
@a @b msg - Broadcast to multiple agents
/new or /clear - Start a new session
/plan - Enter planning mode
/exec - Return to execution mode
/cwd /path - Switch workspace directory
/session list - List recent local Codex threads
/session <id> - Attach current chat to an existing agent session
/info - Show current agent info
/help - Show this help message

Aliases: /cc(claude) /cx(codex) /cs(cursor) /km(kimi) /gm(gemini) /oc(openclaw) /ocd(opencode) /pi(pi) /cp(copilot) /dr(droid) /if(iflow) /kr(kiro) /qw(qwen)`
}

func buildOwnerHelpText() string {
	return `用户 Agent 命令:
/tasks - 查看最近任务
/agents - 查看已注册用户 Agent
/capabilities - 查看当前账号启用能力
/delegate 目标 任务 - 发起跨用户协作
/approve 审批码 - 同意协作请求
/reject 审批码 原因 - 拒绝协作请求
/help - 查看帮助

直接发送普通文本，会交给当前账号的主 Agent 处理。`
}

func formatOwnerTaskList(accountID string, tasks []controlplane.TaskRecord) string {
	var lines []string
	for _, task := range tasks {
		if task.RequesterAccountID != accountID && task.TargetAccountID != accountID && task.OwnerAccountID != accountID {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s [%s] %s", shortTaskID(task.ID), task.Status, task.Title))
		if len(lines) >= 8 {
			break
		}
	}
	if len(lines) == 0 {
		return "最近没有任务。"
	}
	return "最近任务：\n" + strings.Join(lines, "\n")
}

func formatOwnerAgentList(profiles []controlplane.UserAgentProfile) string {
	if len(profiles) == 0 {
		return "暂无用户 Agent。"
	}
	var lines []string
	for _, profile := range profiles {
		lines = append(lines, fmt.Sprintf("%s -> %s", profile.AccountID, firstNonBlank(profile.DisplayName, profile.BaseAgentName)))
	}
	return "用户 Agent：\n" + strings.Join(lines, "\n")
}

func formatOwnerCapabilities(bindings []controlplane.CapabilityBinding) string {
	if len(bindings) == 0 {
		return "当前账号未启用能力。"
	}
	var lines []string
	for _, binding := range bindings {
		state := "关闭"
		if binding.Enabled {
			state = "开启"
		}
		lines = append(lines, fmt.Sprintf("%s [%s]", binding.Name, state))
	}
	return "当前能力：\n" + strings.Join(lines, "\n")
}

func shortTaskID(taskID string) string {
	if len(taskID) <= 8 {
		return taskID
	}
	return taskID[:8]
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func extractImage(msg ilink.WeixinMessage) *ilink.ImageItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeImage && item.ImageItem != nil {
			return item.ImageItem
		}
	}
	return nil
}

func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}

func (h *Handler) handleImageSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, img *ilink.ImageItem) {
	clientID := NewClientID()
	log.Printf("[handler] received image from %s, saving to %s", msg.FromUserID, h.saveDir)

	// Download image data
	var data []byte
	var err error

	if img.URL != "" {
		// Direct URL download
		data, _, err = downloadFile(ctx, img.URL)
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		// CDN encrypted download
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
	} else {
		log.Printf("[handler] image has no URL or media info from %s", msg.FromUserID)
		return
	}

	if err != nil {
		log.Printf("[handler] failed to download image from %s: %v", msg.FromUserID, err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Detect extension from content
	ext := detectImageExt(data)

	// Generate filename with timestamp
	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s%s", ts, ext)
	filePath := filepath.Join(h.saveDir, fileName)

	// Ensure save directory exists
	if err := os.MkdirAll(h.saveDir, 0o755); err != nil {
		log.Printf("[handler] failed to create save dir: %v", err)
		return
	}

	// Write image file
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("[handler] failed to write image: %v", err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Write sidecar file
	sidecarPath := filePath + ".sidecar.md"
	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", uuid.New().String())
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		log.Printf("[handler] failed to write sidecar: %v", err)
	}

	log.Printf("[handler] saved image to %s (%d bytes)", filePath, len(data))
	reply := fmt.Sprintf("Saved: %s", fileName)
	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
	}
}

func detectImageExt(data []byte) string {
	if len(data) < 4 {
		return ".bin"
	}
	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return ".png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return ".gif"
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 && data[0] == 0x52 && data[1] == 0x49 && data[8] == 0x57 && data[9] == 0x45 {
		return ".webp"
	}
	// BMP: 42 4D
	if data[0] == 0x42 && data[1] == 0x4D {
		return ".bmp"
	}
	return ".jpg" // default to jpg for WeChat images
}
