package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestNormalizeMediaServiceURL(t *testing.T) {
	processURL, healthURL := normalizeMediaServiceURL("http://127.0.0.1:5000/api/media")
	if processURL != "http://127.0.0.1:5000/api/media/process" {
		t.Fatalf("processURL = %q", processURL)
	}
	if healthURL != "http://127.0.0.1:5000/api/media/health" {
		t.Fatalf("healthURL = %q", healthURL)
	}

	processURL, healthURL = normalizeMediaServiceURL("http://127.0.0.1:5000/api/media/process")
	if processURL != "http://127.0.0.1:5000/api/media/process" {
		t.Fatalf("processURL(process) = %q", processURL)
	}
	if healthURL != "http://127.0.0.1:5000/api/media/health" {
		t.Fatalf("healthURL(process) = %q", healthURL)
	}
}

func TestBuildMediaAnalysisPrompt(t *testing.T) {
	prompt := buildMediaAnalysisPrompt("请帮我看一下这张图", map[string]any{
		"summary": "收到 1 张图片",
		"images":  []map[string]any{{"local_path": "/tmp/a.png", "width": 100, "height": 80}},
	}, "")

	if !strings.Contains(prompt, "<wechat_media_payload>") {
		t.Fatalf("prompt missing payload block: %q", prompt)
	}
	if !strings.Contains(prompt, "请帮我看一下这张图") {
		t.Fatalf("prompt missing user text: %q", prompt)
	}
	if !strings.Contains(prompt, "/tmp/a.png") {
		t.Fatalf("prompt missing serialized payload: %q", prompt)
	}
}

func TestCollectIncomingRichMediaVoice(t *testing.T) {
	h := NewHandler(nil, nil)
	msg := ilink.WeixinMessage{
		MessageID:    42,
		FromUserID:   "wxid_test",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList: []ilink.MessageItem{
			{
				Type: ilink.ItemTypeVoice,
				VoiceItem: &ilink.VoiceItem{
					Text:       "你好，你在吗",
					EncodeType: 7,
					Playtime:   3200,
				},
			},
		},
	}

	incoming, hasMedia, err := h.collectIncomingRichMedia(context.Background(), msg)
	if err != nil {
		t.Fatalf("collectIncomingRichMedia error: %v", err)
	}
	if !hasMedia {
		t.Fatal("expected hasMedia to be true")
	}
	if incoming.userText != "你好，你在吗" {
		t.Fatalf("userText = %q", incoming.userText)
	}
	if len(incoming.request.Voices) != 1 {
		t.Fatalf("voices = %d", len(incoming.request.Voices))
	}
	if incoming.request.Voices[0].Transcript != "你好，你在吗" {
		t.Fatalf("transcript = %q", incoming.request.Voices[0].Transcript)
	}
	if incoming.request.Voices[0].DurationSec != 3.2 {
		t.Fatalf("duration = %v", incoming.request.Voices[0].DurationSec)
	}
}
