package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type MediaServiceClient struct {
	processURL string
	healthURL  string
	httpClient *http.Client
}

type MediaProcessRequest struct {
	Source     string              `json:"source"`
	MessageRef MediaMessageRef     `json:"message_ref"`
	Texts      []string            `json:"texts,omitempty"`
	URLs       []string            `json:"urls,omitempty"`
	Images     []MediaAsset        `json:"images,omitempty"`
	Videos     []MediaAsset        `json:"videos,omitempty"`
	Voices     []MediaAsset        `json:"voices,omitempty"`
	FileCards  []MediaAsset        `json:"file_cards,omitempty"`
	Options    MediaProcessOptions `json:"options"`
}

type MediaMessageRef struct {
	ChatID    string `json:"chat_id,omitempty"`
	ChatName  string `json:"chat_name,omitempty"`
	Sender    string `json:"sender,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type MediaAsset struct {
	LocalPath   string  `json:"local_path,omitempty"`
	RemoteURL   string  `json:"remote_url,omitempty"`
	FileName    string  `json:"file_name,omitempty"`
	MIMEType    string  `json:"mime_type,omitempty"`
	SizeBytes   int64   `json:"size_bytes,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	DurationSec float64 `json:"duration_sec,omitempty"`
	Transcript  string  `json:"transcript,omitempty"`
	EncodeType  int     `json:"encode_type,omitempty"`
	PlaytimeMS  int     `json:"playtime_ms,omitempty"`
	Summary     string  `json:"summary,omitempty"`
	TextPreview string  `json:"text_preview,omitempty"`
	Codec       string  `json:"codec,omitempty"`
}

type MediaProcessOptions struct {
	TranscribeVoiceIfMissing bool `json:"transcribe_voice_if_missing"`
	ExtractFileText          bool `json:"extract_file_text"`
	AnalyzeImage             bool `json:"analyze_image"`
	AnalyzeVideo             bool `json:"analyze_video"`
}

type MediaProcessResponse struct {
	Status    string       `json:"status"`
	Summary   string       `json:"summary"`
	Texts     []string     `json:"texts,omitempty"`
	URLs      []string     `json:"urls,omitempty"`
	Images    []MediaAsset `json:"images,omitempty"`
	Videos    []MediaAsset `json:"videos,omitempty"`
	Voices    []MediaAsset `json:"voices,omitempty"`
	FileCards []MediaAsset `json:"file_cards,omitempty"`
	Documents []any        `json:"documents,omitempty"`
	Warnings  []string     `json:"warnings,omitempty"`
	Errors    []string     `json:"errors,omitempty"`
}

func NewMediaServiceClient(baseURL string) *MediaServiceClient {
	processURL, healthURL := normalizeMediaServiceURL(baseURL)
	return &MediaServiceClient{
		processURL: processURL,
		healthURL:  healthURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func normalizeMediaServiceURL(baseURL string) (string, string) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case strings.HasSuffix(base, "/process"):
		return base, strings.TrimSuffix(base, "/process") + "/health"
	case strings.HasSuffix(base, "/health"):
		root := strings.TrimSuffix(base, "/health")
		return root + "/process", base
	default:
		return base + "/process", base + "/health"
	}
}

func (c *MediaServiceClient) Process(ctx context.Context, req *MediaProcessRequest) (*MediaProcessResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal media request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.processURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create media request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call media service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(resp.Body)
		return nil, fmt.Errorf("media service HTTP %d: %s", resp.StatusCode, raw.String())
	}

	var result MediaProcessResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode media response: %w", err)
	}
	return &result, nil
}

func buildMediaAnalysisPrompt(userText string, payload any, note string) string {
	payloadJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		payloadJSON = []byte(`{"error":"marshal payload failed"}`)
	}

	userText = strings.TrimSpace(userText)
	if userText == "" {
		userText = "（无附加文本，请仅根据富媒体内容理解用户意图）"
	}

	var b strings.Builder
	b.WriteString("你正在通过微信回复用户。\n")
	b.WriteString("请基于下面的结构化富媒体结果理解用户意图，并直接输出最终回复文本。\n")
	b.WriteString("规则：\n")
	b.WriteString("1. 直接输出要发给用户的中文回复，不要解释 JSON。\n")
	b.WriteString("2. 如果需要发送图片，使用 Markdown 图片语法 ![](http://或https://链接)。\n")
	b.WriteString("3. 如果需要发送本地附件，只单独输出绝对路径，每行一个。\n")
	b.WriteString("4. 如果富媒体内容不足以判断，就明确说明你还需要用户补充什么。\n\n")
	if note != "" {
		b.WriteString("附加说明：")
		b.WriteString(note)
		b.WriteString("\n\n")
	}
	b.WriteString("用户附加文本：")
	b.WriteString(userText)
	b.WriteString("\n\n<wechat_media_payload>\n")
	b.Write(payloadJSON)
	b.WriteString("\n</wechat_media_payload>")
	return b.String()
}
