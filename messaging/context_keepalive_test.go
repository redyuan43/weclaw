package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestKeepAliveContextsOnceClearsInvalidContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/getconfig" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(ilink.GetConfigResponse{Ret: errCodeInvalidContext})
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{
		ILinkBotID: "bot@im.bot",
		BotToken:   "token",
		BaseURL:    server.URL,
	})
	if err := ilink.SaveContextToken(client.BotID(), "owner@im.wechat", "stale-token"); err != nil {
		t.Fatalf("SaveContextToken: %v", err)
	}

	keepAliveContextsOnce(context.Background(), client, false)

	got, err := ilink.LoadContextToken(client.BotID(), "owner@im.wechat")
	if err != nil {
		t.Fatalf("LoadContextToken: %v", err)
	}
	if got != "" {
		t.Fatalf("token = %q, want cleared", got)
	}
}

func TestKeepAliveContextsOnceCanSendTypingCancel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	typingCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getconfig":
			_ = json.NewEncoder(w).Encode(ilink.GetConfigResponse{Ret: 0, TypingTicket: "ticket"})
		case "/ilink/bot/sendtyping":
			typingCalls++
			_ = json.NewEncoder(w).Encode(ilink.SendTypingResponse{Ret: 0})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{
		ILinkBotID: "bot@im.bot",
		BotToken:   "token",
		BaseURL:    server.URL,
	})
	if err := ilink.SaveContextToken(client.BotID(), "owner@im.wechat", "fresh-token"); err != nil {
		t.Fatalf("SaveContextToken: %v", err)
	}

	keepAliveContextsOnce(context.Background(), client, true)

	if typingCalls != 1 {
		t.Fatalf("typingCalls = %d, want 1", typingCalls)
	}
}
