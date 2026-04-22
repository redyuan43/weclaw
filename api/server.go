package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/controlplane"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
)

// Server provides a local HTTP API for sending messages and observing recent inbound IM traffic.
type Server struct {
	clients       map[string]*ilink.Client
	defaultClient *ilink.Client
	addr          string
	inbox         *messaging.InboxStore
	handler       *messaging.Handler
	userAgents    *controlplane.Service
	debugInject   func(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage)
}

// NewServer creates an API server.
func NewServer(clients []*ilink.Client, addr string, inbox *messaging.InboxStore, handler *messaging.Handler) *Server {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	mapped := make(map[string]*ilink.Client, len(clients))
	var defaultClient *ilink.Client
	for _, client := range clients {
		if client == nil {
			continue
		}
		if defaultClient == nil {
			defaultClient = client
		}
		mapped[client.BotID()] = client
	}
	return &Server{clients: mapped, defaultClient: defaultClient, addr: addr, inbox: inbox, handler: handler}
}

func (s *Server) SetUserAgents(service *controlplane.Service) {
	s.userAgents = service
}

func (s *Server) SetDebugMessageInjector(fn func(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage)) {
	s.debugInject = fn
}

// SendRequest is the JSON body for POST /api/send.
type SendRequest struct {
	AccountID               string `json:"account_id,omitempty"`
	To                      string `json:"to"`
	Text                    string `json:"text,omitempty"`
	MediaURL                string `json:"media_url,omitempty"`
	MediaPath               string `json:"media_path,omitempty"`
	MediaMode               string `json:"media_mode,omitempty"`
	ContextToken            string `json:"context_token,omitempty"`
	WaitContextTokenSeconds int    `json:"wait_context_token_seconds,omitempty"`
}

type AgentChatRequest struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	AgentName      string `json:"agent_name,omitempty"`
}

type DebugInboundRequest struct {
	AccountID    string `json:"account_id"`
	FromUserID   string `json:"from_user_id"`
	Text         string `json:"text"`
	ContextToken string `json:"context_token,omitempty"`
	MessageID    int64  `json:"message_id,omitempty"`
	Mode         string `json:"mode,omitempty"` // text | voice
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/agent/chat", s.handleAgentChat)
	mux.HandleFunc("/api/bridge/inbound", s.handleBridgeInbound)
	mux.HandleFunc("/api/debug/inbound", s.handleDebugInbound)
	mux.HandleFunc("/api/inbox", s.handleInbox)
	mux.HandleFunc("/api/inbox/clear", s.handleClearInbox)
	mux.HandleFunc("/.well-known/agent-card.json", s.handleWellKnownAgentCard)
	mux.HandleFunc("/a2a/users/", s.handleA2AUser)
	mux.HandleFunc("/console", s.handleConsole)
	mux.HandleFunc("/console/", s.handleConsoleRoute)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{Addr: s.addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Printf("[api] listening on %s", s.addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return
	}
	if req.Text == "" && req.MediaURL == "" && req.MediaPath == "" {
		http.Error(w, `"text", "media_url", or "media_path" is required`, http.StatusBadRequest)
		return
	}
	if req.MediaURL != "" && req.MediaPath != "" {
		http.Error(w, `"media_url" and "media_path" are mutually exclusive`, http.StatusBadRequest)
		return
	}
	if req.MediaMode != "" && req.MediaMode != "auto" && req.MediaMode != "image" && req.MediaMode != "file" {
		http.Error(w, `"media_mode" must be one of: auto, image, file`, http.StatusBadRequest)
		return
	}
	if req.WaitContextTokenSeconds < 0 {
		http.Error(w, `"wait_context_token_seconds" must be non-negative`, http.StatusBadRequest)
		return
	}
	client, err := s.selectClient(req.AccountID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	contextToken, contextSource := s.resolveSendContextToken(req)
	if req.Text != "" {
		if err := messaging.SendTextReply(ctx, client, req.To, req.Text, contextToken, ""); err != nil {
			log.Printf("[api] send text failed: %v", err)
			http.Error(w, "send text failed: "+withContextHint(err.Error(), contextToken, req.To), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent text to %s via %s (context=%s): %q", req.To, client.BotID(), contextSource, req.Text)
		for _, imgURL := range messaging.ExtractImageURLs(req.Text) {
			if err := messaging.SendMediaFromURL(ctx, client, req.To, imgURL, contextToken); err != nil {
				log.Printf("[api] send extracted image failed: %v", err)
			} else {
				log.Printf("[api] sent extracted image to %s: %s", req.To, imgURL)
			}
		}
	}

	if req.MediaURL != "" {
		if err := messaging.SendMediaFromURL(ctx, client, req.To, req.MediaURL, contextToken); err != nil {
			log.Printf("[api] send media failed: %v", err)
			http.Error(w, "send media failed: "+withContextHint(err.Error(), contextToken, req.To), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent media to %s (context=%s): %s", req.To, contextSource, req.MediaURL)
	}

	if req.MediaPath != "" {
		if err := messaging.SendMediaFromPathAs(ctx, client, req.To, req.MediaPath, contextToken, effectiveMediaMode(req)); err != nil {
			log.Printf("[api] send media failed: %v", err)
			http.Error(w, "send media failed: "+withContextHint(err.Error(), contextToken, req.To), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent media to %s (context=%s mode=%s): %s", req.To, contextSource, effectiveMediaMode(req), req.MediaPath)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":               "ok",
		"context_token_source": contextSource,
	})
}

func (s *Server) resolveSendContextToken(req SendRequest) (string, string) {
	token := strings.TrimSpace(req.ContextToken)
	if token != "" {
		return token, "request"
	}
	if s.handler == nil {
		return "", "none"
	}
	deadline := time.Now().Add(time.Duration(req.WaitContextTokenSeconds) * time.Second)
	for {
		token = strings.TrimSpace(s.handler.ContextTokenForUser(req.To))
		if token != "" {
			return token, "handler-cache"
		}
		if req.WaitContextTokenSeconds <= 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return "", "none"
}

func effectiveMediaMode(req SendRequest) string {
	mode := strings.TrimSpace(req.MediaMode)
	if mode == "" {
		return "file"
	}
	return mode
}

func withContextHint(message, contextToken, toUserID string) string {
	if strings.TrimSpace(contextToken) != "" {
		return message
	}
	return fmt.Sprintf("%s (no cached context_token for %s; send the bot a fresh message from that WeChat user and retry)", message, toUserID)
}

func (s *Server) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.handler == nil {
		http.Error(w, "agent handler unavailable", http.StatusServiceUnavailable)
		return
	}

	var req AgentChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ConversationID == "" {
		http.Error(w, `"conversation_id" is required`, http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, `"message" is required`, http.StatusBadRequest)
		return
	}

	result, err := s.handler.ChatLocalAgent(r.Context(), req.ConversationID, req.Message, req.AgentName)
	if err != nil {
		http.Error(w, "agent chat failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(
		map[string]any{
			"reply": result.Reply,
			"agent": map[string]any{
				"name":    result.AgentName,
				"type":    result.Info.Type,
				"model":   result.Info.Model,
				"command": result.Info.Command,
				"pid":     result.Info.PID,
			},
		},
	)
}

func (s *Server) handleBridgeInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.handler == nil {
		http.Error(w, "bridge handler unavailable", http.StatusServiceUnavailable)
		return
	}

	var req messaging.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	result, err := s.handler.HandleBridgeRequest(r.Context(), req)
	if err != nil {
		result = &messaging.TaskResult{
			TaskID:   req.Envelope.MessageID,
			Status:   "failed",
			Accepted: false,
			Detail:   err.Error(),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleDebugInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.handler == nil && s.debugInject == nil {
		http.Error(w, "message handler unavailable", http.StatusServiceUnavailable)
		return
	}

	var req DebugInboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.AccountID == "" {
		http.Error(w, `"account_id" is required`, http.StatusBadRequest)
		return
	}
	if req.FromUserID == "" {
		http.Error(w, `"from_user_id" is required`, http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, `"text" is required`, http.StatusBadRequest)
		return
	}

	client, err := s.selectClient(req.AccountID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	mode := req.Mode
	if mode == "" {
		mode = "text"
	}
	var item ilink.MessageItem
	switch mode {
	case "text":
		item = ilink.MessageItem{
			Type:     ilink.ItemTypeText,
			TextItem: &ilink.TextItem{Text: req.Text},
		}
	case "voice":
		item = ilink.MessageItem{
			Type: ilink.ItemTypeVoice,
			VoiceItem: &ilink.VoiceItem{
				Text: req.Text,
			},
		}
	default:
		http.Error(w, `"mode" must be "text" or "voice"`, http.StatusBadRequest)
		return
	}

	messageID := req.MessageID
	if messageID == 0 {
		messageID = time.Now().UnixNano()
	}
	msg := ilink.WeixinMessage{
		MessageID:    messageID,
		FromUserID:   req.FromUserID,
		ToUserID:     client.BotID(),
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList:     []ilink.MessageItem{item},
		ContextToken: req.ContextToken,
	}

	injector := s.debugInject
	if injector == nil {
		injector = s.handler.HandleMessage
	}
	injector(context.WithoutCancel(r.Context()), client, msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"account_id":   req.AccountID,
		"from_user_id": req.FromUserID,
		"message_id":   messageID,
		"mode":         mode,
		"to_user_id":   client.BotID(),
	})
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.inbox == nil {
		http.Error(w, "inbox disabled", http.StatusServiceUnavailable)
		return
	}
	from := r.URL.Query().Get("from")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"messages": s.inbox.List(from, afterSeq, limit)})
}

func (s *Server) handleClearInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.inbox != nil {
		s.inbox.Clear()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"cleared": true})
}

func (s *Server) selectClient(accountID string) (*ilink.Client, error) {
	if accountID != "" {
		client, ok := s.clients[accountID]
		if !ok {
			return nil, fmt.Errorf("unknown account_id: %s", accountID)
		}
		return client, nil
	}
	if s.defaultClient == nil {
		return nil, fmt.Errorf("no accounts configured")
	}
	return s.defaultClient, nil
}
