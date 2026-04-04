package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

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

// SendRequest is the JSON body for POST /api/send.
type SendRequest struct {
	AccountID string `json:"account_id,omitempty"`
	To        string `json:"to"`
	Text      string `json:"text,omitempty"`
	MediaURL  string `json:"media_url,omitempty"`
}

type AgentChatRequest struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	AgentName      string `json:"agent_name,omitempty"`
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/agent/chat", s.handleAgentChat)
	mux.HandleFunc("/api/bridge/inbound", s.handleBridgeInbound)
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
	if req.Text == "" && req.MediaURL == "" {
		http.Error(w, `"text" or "media_url" is required`, http.StatusBadRequest)
		return
	}
	client, err := s.selectClient(req.AccountID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	if req.Text != "" {
		if err := messaging.SendTextReply(ctx, client, req.To, req.Text, "", ""); err != nil {
			log.Printf("[api] send text failed: %v", err)
			http.Error(w, "send text failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent text to %s via %s: %q", req.To, client.BotID(), req.Text)
		for _, imgURL := range messaging.ExtractImageURLs(req.Text) {
			if err := messaging.SendMediaFromURL(ctx, client, req.To, imgURL, ""); err != nil {
				log.Printf("[api] send extracted image failed: %v", err)
			} else {
				log.Printf("[api] sent extracted image to %s: %s", req.To, imgURL)
			}
		}
	}

	if req.MediaURL != "" {
		if err := messaging.SendMediaFromURL(ctx, client, req.To, req.MediaURL, ""); err != nil {
			log.Printf("[api] send media failed: %v", err)
			http.Error(w, "send media failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent media to %s: %s", req.To, req.MediaURL)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
